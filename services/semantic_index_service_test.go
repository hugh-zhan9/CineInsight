package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

type semanticEmbeddingClientFunc func(context.Context, string) ([]float64, error)

func (f semanticEmbeddingClientFunc) Embed(ctx context.Context, text string) ([]float64, error) {
	return f(ctx, text)
}

func setupSemanticIndexTestDB(t *testing.T) database.SemanticVectorCapability {
	t.Helper()
	setupVideoServiceTestDB(t)
	capability := database.PrepareSemanticVectorStorage(database.DB)
	if capability.ReasonCode == "metadata_migration_failed" {
		t.Fatalf("migrate semantic metadata: %s", capability.Message)
	}
	return database.SemanticVectorCapability{Available: true, Backend: "test"}
}

func waitSemanticIndex(t *testing.T, service *SemanticIndexService) SemanticIndexStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := service.Status()
		if !status.Running {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("semantic index worker did not stop: %+v", service.Status())
	return SemanticIndexStatus{}
}

func TestSemanticIndexPipelineUsesEmbeddingsAndSanitizesIndexText(t *testing.T) {
	capability := setupSemanticIndexTestDB(t)
	tag := models.Tag{Name: "动作", IsActive: true}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("create tag: %v", err)
	}
	video := models.Video{
		Name:          "movie.mp4",
		DisplayTitle:  "深夜追逐",
		OriginalTitle: "Night Chase",
		Description:   `源文件 /Users/private/library/movie.mp4，镜像位于 \\server\private\movie.mp4`,
		Path:          "/Users/private/library/movie.mp4",
		Tags:          []models.Tag{tag},
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	if err := database.DB.Create(&models.AITagCandidate{
		VideoID: video.ID, SuggestedName: "追逐", NormalizedName: "追逐",
		Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending,
		Reasoning:     "画面是高速追逐，secret-key 不应泄露",
		SourceSummary: `{"path":"/Users/private/library/movie.mp4","directory":"/Users/private/library","subtitle_text":"private","safe":"夜间车辆追逐"}`,
	}).Error; err != nil {
		t.Fatalf("create AI candidate: %v", err)
	}
	if err := database.DB.Create(&models.SubtitleSegment{VideoID: video.ID, SegmentIndex: 0, Text: "快追上他"}).Error; err != nil {
		t.Fatalf("create subtitle: %v", err)
	}

	requestText := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/embeddings" {
			t.Errorf("embedding path = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer secret-key" {
			t.Errorf("Authorization = %q", got)
		}
		var payload struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload.Model != "embed-v1" {
			t.Errorf("model = %q", payload.Model)
		}
		requestText <- payload.Input
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer server.Close()

	provider := SemanticIndexConfigProviderFunc(func() (SemanticIndexConfig, error) {
		return SemanticIndexConfig{BaseURL: server.URL, APIKey: "secret-key", Model: "embed-v1"}, nil
	})
	service := NewSemanticIndexService(database.DB, capability, provider)
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); err != nil {
		t.Fatalf("start semantic indexing: %v", err)
	}
	status := waitSemanticIndex(t, service)
	if !status.Completed || status.Succeeded != 1 || status.Dimension != 3 {
		t.Fatalf("unexpected final status: %+v", status)
	}

	input := <-requestText
	for _, forbidden := range []string{"secret-key", "/Users/private", `\\server\private`, `"path"`, `"directory"`, `"subtitle_text"`} {
		if strings.Contains(input, forbidden) {
			t.Errorf("embedding input contains %q: %s", forbidden, input)
		}
	}
	for _, expected := range []string{"深夜追逐", "Night Chase", "动作", "夜间车辆追逐", "快追上他"} {
		if !strings.Contains(input, expected) {
			t.Errorf("embedding input missing %q: %s", expected, input)
		}
	}
	var row models.VideoSemanticIndex
	if err := database.DB.First(&row, "video_id = ?", video.ID).Error; err != nil {
		t.Fatalf("load semantic row: %v", err)
	}
	if row.ModelIdentifier != "embed-v1" || row.Dimension != 3 || row.ContentFingerprint == "" {
		t.Fatalf("unexpected semantic row: %+v", row)
	}
}

func TestSemanticCoverageExcludesSoftDeletedVideos(t *testing.T) {
	capability := setupSemanticIndexTestDB(t)
	first := models.Video{Name: "first.mp4", DisplayTitle: "第一部", Path: "/library/first.mp4"}
	second := models.Video{Name: "second.mp4", DisplayTitle: "第二部", Path: "/library/second.mp4"}
	for _, video := range []*models.Video{&first, &second} {
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatalf("create video: %v", err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer server.Close()
	provider := SemanticIndexConfigProviderFunc(func() (SemanticIndexConfig, error) {
		return SemanticIndexConfig{BaseURL: server.URL, Model: "embed-v1"}, nil
	})
	service := NewSemanticIndexService(database.DB, capability, provider)
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); err != nil {
		t.Fatalf("start semantic indexing: %v", err)
	}
	status := waitSemanticIndex(t, service)
	if status.Succeeded != 2 {
		t.Fatalf("expected both videos indexed: %+v", status)
	}

	if err := database.DB.Delete(&models.Video{}, second.ID).Error; err != nil {
		t.Fatalf("soft delete video: %v", err)
	}

	var profile models.SemanticIndexProfile
	if err := database.DB.First(&profile, "id = ?", 1).Error; err != nil {
		t.Fatalf("load profile: %v", err)
	}
	coverage, err := service.semanticCoverage(context.Background(), profile)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage.Total != 1 || coverage.Indexed != 1 {
		t.Fatalf("软删除视频不应计入覆盖率: %+v", coverage)
	}
	service.refreshStoredStatus()
	refreshed := service.Status()
	if refreshed.Total != 1 || refreshed.Processed != 1 {
		t.Fatalf("状态刷新应排除软删除视频: %+v", refreshed)
	}
}

func TestSemanticIndexUnavailableCapabilityIsExplicit(t *testing.T) {
	setupVideoServiceTestDB(t)
	capability := database.PrepareSemanticVectorStorage(database.DB)
	service := NewSemanticIndexService(database.DB, capability, SemanticIndexConfigProviderFunc(func() (SemanticIndexConfig, error) {
		return SemanticIndexConfig{BaseURL: "http://unused", Model: "embed-v1"}, nil
	}))
	status, err := service.Start(context.Background(), SemanticIndexBuildRequest{})
	if !errors.Is(err, ErrSemanticIndexUnavailable) {
		t.Fatalf("start error = %v", err)
	}
	if status.Available || status.Unavailable == "" || service.Status().Running {
		t.Fatalf("unavailable status is not explicit: %+v", status)
	}
}

func TestSemanticIndexExplicitRebuildReindexesSameModelAndDimension(t *testing.T) {
	capability := setupSemanticIndexTestDB(t)
	for _, name := range []string{"first.mp4", "second.mp4"} {
		if err := database.DB.Create(&models.Video{Name: name, Path: "/library/" + name}).Error; err != nil {
			t.Fatal(err)
		}
	}
	provider := SemanticIndexConfigProviderFunc(func() (SemanticIndexConfig, error) {
		return SemanticIndexConfig{BaseURL: "http://unused", Model: "embed-v1"}, nil
	})
	service := NewSemanticIndexService(database.DB, capability, provider)
	calls := 0
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			calls++
			return []float64{0.1, 0.2}, nil
		})
	}
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); err != nil {
		t.Fatal(err)
	}
	_ = waitSemanticIndex(t, service)
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{Rebuild: true}); err != nil {
		t.Fatal(err)
	}
	status := waitSemanticIndex(t, service)
	if calls != 4 || status.Generation != 2 || status.Succeeded != 2 {
		t.Fatalf("same-dimension rebuild calls=%d status=%+v", calls, status)
	}
	var rows []models.VideoSemanticIndex
	if err := database.DB.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Generation != 2 || rows[1].Generation != 2 {
		t.Fatalf("rebuild rows=%+v", rows)
	}
}

func TestSemanticIndexRecordsPerVideoFailureAndRetriesIt(t *testing.T) {
	capability := setupSemanticIndexTestDB(t)
	for _, name := range []string{"first.mp4", "second.mp4"} {
		if err := database.DB.Create(&models.Video{Name: name, Path: "/library/" + name}).Error; err != nil {
			t.Fatalf("create video: %v", err)
		}
	}
	provider := SemanticIndexConfigProviderFunc(func() (SemanticIndexConfig, error) {
		return SemanticIndexConfig{BaseURL: "http://unused", Model: "embed-v1"}, nil
	})
	service := NewSemanticIndexService(database.DB, capability, provider)
	calls := 0
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("temporary endpoint failure")
			}
			return []float64{0.1, 0.2}, nil
		})
	}
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); err != nil {
		t.Fatalf("start semantic indexing: %v", err)
	}
	status := waitSemanticIndex(t, service)
	if !status.Completed || status.Failed != 1 || status.Succeeded != 1 || len(status.Failures) != 1 {
		t.Fatalf("failure status = %+v", status)
	}
	var failed models.SemanticIndexAttempt
	if err := database.DB.First(&failed, "status = ?", models.SemanticIndexAttemptFailed).Error; err != nil {
		t.Fatalf("load failed attempt: %v", err)
	}
	if failed.ErrorCode != "embedding_failed" || !strings.Contains(failed.LastError, "temporary endpoint failure") || failed.AttemptCount != 1 {
		t.Fatalf("failed attempt = %+v", failed)
	}

	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			return []float64{0.3, 0.4}, nil
		})
	}
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); err != nil {
		t.Fatalf("retry semantic indexing: %v", err)
	}
	status = waitSemanticIndex(t, service)
	if !status.Completed || status.Succeeded != 1 || status.Skipped != 1 || status.Failed != 0 {
		t.Fatalf("retry status = %+v", status)
	}
	if err := database.DB.First(&failed, failed.ID).Error; err != nil {
		t.Fatalf("reload attempt: %v", err)
	}
	if failed.Status != models.SemanticIndexAttemptCompleted || failed.AttemptCount != 2 || failed.LastError != "" || failed.CompletedAt == nil {
		t.Fatalf("retried attempt = %+v", failed)
	}
}

type cancellingSemanticEmbedder struct {
	mu            sync.Mutex
	calls         int
	secondStarted chan struct{}
	once          sync.Once
}

func (e *cancellingSemanticEmbedder) Embed(ctx context.Context, _ string) ([]float64, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	e.mu.Unlock()
	if call == 1 {
		return []float64{0.1, 0.2}, nil
	}
	e.once.Do(func() { close(e.secondStarted) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSemanticIndexCancellationResumesWithoutRepeatingSuccess(t *testing.T) {
	capability := setupSemanticIndexTestDB(t)
	for _, name := range []string{"first.mp4", "second.mp4"} {
		if err := database.DB.Create(&models.Video{Name: name, Path: "/library/" + name}).Error; err != nil {
			t.Fatalf("create video: %v", err)
		}
	}
	provider := SemanticIndexConfigProviderFunc(func() (SemanticIndexConfig, error) {
		return SemanticIndexConfig{BaseURL: "http://unused", Model: "embed-v1"}, nil
	})
	service := NewSemanticIndexService(database.DB, capability, provider)
	blocking := &cancellingSemanticEmbedder{secondStarted: make(chan struct{})}
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient { return blocking }
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); err != nil {
		t.Fatalf("start semantic indexing: %v", err)
	}
	select {
	case <-blocking.secondStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("second embedding did not start")
	}
	if status, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); err != nil || !status.Running {
		t.Fatalf("second Start should observe the same worker: status=%+v err=%v", status, err)
	}
	if err := service.Cancel(); err != nil {
		t.Fatalf("cancel semantic indexing: %v", err)
	}
	service.StopAndWait()
	status := service.Status()
	if !status.Cancelled || status.Succeeded != 1 {
		t.Fatalf("cancelled status = %+v", status)
	}

	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			return []float64{0.3, 0.4}, nil
		})
	}
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); err != nil {
		t.Fatalf("resume semantic indexing: %v", err)
	}
	status = waitSemanticIndex(t, service)
	if !status.Completed || status.Succeeded != 1 || status.Skipped != 1 {
		t.Fatalf("resumed status = %+v", status)
	}
	var count int64
	if err := database.DB.Model(&models.VideoSemanticIndex{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("semantic row count = %d, err=%v", count, err)
	}
}

func TestSemanticIndexRequiresExplicitRebuildForModelAndDimensionChanges(t *testing.T) {
	capability := setupSemanticIndexTestDB(t)
	video := models.Video{Name: "movie.mp4", DisplayTitle: "Version one", Path: "/library/movie.mp4"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	model := "embed-v1"
	provider := SemanticIndexConfigProviderFunc(func() (SemanticIndexConfig, error) {
		return SemanticIndexConfig{BaseURL: "http://unused", Model: model}, nil
	})
	service := NewSemanticIndexService(database.DB, capability, provider)
	vector := []float64{0.1, 0.2}
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) { return vector, nil })
	}
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); err != nil {
		t.Fatalf("start initial indexing: %v", err)
	}
	if status := waitSemanticIndex(t, service); !status.Completed || status.Dimension != 2 {
		t.Fatalf("initial status = %+v", status)
	}
	if err := service.ValidateSearchEmbedding(context.Background(), "embed-v1", []float64{1, 2}); err != nil {
		t.Fatalf("validate matching vector: %v", err)
	}
	if err := service.ValidateSearchEmbedding(context.Background(), "embed-v2", []float64{1, 2}); !errors.Is(err, ErrSemanticIndexModelMismatch) {
		t.Fatalf("model mismatch error = %v", err)
	}
	if err := service.ValidateSearchEmbedding(context.Background(), "embed-v1", []float64{1, 2, 3}); !errors.Is(err, ErrSemanticIndexDimensionMismatch) {
		t.Fatalf("dimension mismatch error = %v", err)
	}

	model = "embed-v2"
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); !errors.Is(err, ErrSemanticIndexRebuildRequired) {
		t.Fatalf("model change error = %v", err)
	}
	model = "embed-v1"
	if err := database.DB.Model(&video).Update("display_title", "Version two").Error; err != nil {
		t.Fatalf("update video: %v", err)
	}
	vector = []float64{0.1, 0.2, 0.3}
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); err != nil {
		t.Fatalf("start changed-dimension indexing: %v", err)
	}
	status := waitSemanticIndex(t, service)
	if !status.NeedsRebuild || status.Completed || status.Failed != 1 {
		t.Fatalf("dimension mismatch status = %+v", status)
	}
	if err := service.ValidateSearchEmbedding(context.Background(), model, vector); !errors.Is(err, ErrSemanticIndexRebuildRequired) {
		t.Fatalf("search should be disabled pending rebuild: %v", err)
	}
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); !errors.Is(err, ErrSemanticIndexRebuildRequired) {
		t.Fatalf("retry without rebuild error = %v", err)
	}

	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{Rebuild: true}); err != nil {
		t.Fatalf("start explicit rebuild: %v", err)
	}
	status = waitSemanticIndex(t, service)
	if !status.Completed || status.Dimension != 3 || status.Generation != 2 {
		t.Fatalf("rebuild status = %+v", status)
	}
	var rows []models.VideoSemanticIndex
	if err := database.DB.Order("dimension ASC").Find(&rows).Error; err != nil {
		t.Fatalf("load isolated vector rows: %v", err)
	}
	if len(rows) != 2 || rows[0].Dimension != 2 || rows[1].Dimension != 3 {
		t.Fatalf("vectors were not isolated by dimension: %+v", rows)
	}

	model = "embed-v2"
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{Rebuild: true}); err != nil {
		t.Fatalf("start model rebuild: %v", err)
	}
	status = waitSemanticIndex(t, service)
	if !status.Completed || status.Model != "embed-v2" || status.Generation != 3 {
		t.Fatalf("model rebuild status = %+v", status)
	}
	if err := database.DB.Order("model_identifier ASC, dimension ASC").Find(&rows).Error; err != nil {
		t.Fatalf("reload isolated vector rows: %v", err)
	}
	if len(rows) != 3 || rows[2].ModelIdentifier != "embed-v2" || rows[2].Dimension != 3 {
		t.Fatalf("vectors were not isolated by model: %+v", rows)
	}
}
