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

func setupImageSemanticIndexTestDB(t *testing.T) database.SemanticVectorCapability {
	t.Helper()
	setupVideoServiceTestDB(t)
	// 视频侧元数据一并迁移：真实库两侧共存，用例需要断言图片索引不写视频表。
	if videoCapability := database.PrepareSemanticVectorStorage(database.DB); videoCapability.ReasonCode == "metadata_migration_failed" {
		t.Fatalf("migrate video semantic metadata: %s", videoCapability.Message)
	}
	capability := database.PrepareImageSemanticVectorStorage(database.DB)
	if capability.ReasonCode == "metadata_migration_failed" {
		t.Fatalf("migrate image semantic metadata: %s", capability.Message)
	}
	return database.SemanticVectorCapability{Available: true, Backend: "test"}
}

func imageSemanticTestProvider(model string) SemanticIndexConfigProvider {
	return SemanticIndexConfigProviderFunc(func() (SemanticIndexConfig, error) {
		return SemanticIndexConfig{BaseURL: "http://unused", Model: model}, nil
	})
}

func createImageSemanticTestImage(t *testing.T, name, description string, tags ...models.Tag) models.Image {
	t.Helper()
	image := models.Image{Name: name, Path: "/library/" + name, Tags: tags}
	if err := database.DB.Create(&image).Error; err != nil {
		t.Fatalf("create image: %v", err)
	}
	if description != "" {
		if err := database.DB.Create(&models.ImageAIDescription{
			ImageID: image.ID, Status: imageAIDescriptionStatusCompleted, Description: description,
		}).Error; err != nil {
			t.Fatalf("create image description: %v", err)
		}
	}
	return image
}

func waitImageSemanticIndex(t *testing.T, service *ImageSemanticIndexService) ImageSemanticIndexStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := service.Status()
		if !status.Running {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("image semantic index worker did not stop: %+v", service.Status())
	return ImageSemanticIndexStatus{}
}

func TestImageSemanticIndexSkipsImagesWithoutCompletedDescription(t *testing.T) {
	capability := setupImageSemanticIndexTestDB(t)
	described := createImageSemanticTestImage(t, "cat.jpg", "一只在窗台晒太阳的橘猫")
	noRow := createImageSemanticTestImage(t, "dog.jpg", "")
	failedRow := createImageSemanticTestImage(t, "bird.jpg", "")
	if err := database.DB.Create(&models.ImageAIDescription{
		ImageID: failedRow.ID, Status: imageAIDescriptionStatusFailed, LastError: "endpoint down",
	}).Error; err != nil {
		t.Fatalf("create failed description: %v", err)
	}
	// 视频侧既有行：图片索引运行不得触碰视频语义表（零交集）。
	video := models.Video{Name: "movie.mp4", Path: "/library/movie.mp4"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	if err := database.DB.Create(&models.VideoSemanticIndex{
		VideoID: video.ID, ModelIdentifier: "embed-v1", Dimension: 2, Generation: 1,
		ContentFingerprint: "video-fp", IndexedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("create video semantic row: %v", err)
	}

	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	calls := 0
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			calls++
			return []float64{0.1, 0.2}, nil
		})
	}
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("start image semantic indexing: %v", err)
	}
	status := waitImageSemanticIndex(t, service)
	if !status.Completed || status.Total != 3 || status.Succeeded != 1 || status.Skipped != 2 || status.Failed != 0 {
		t.Fatalf("unexpected final status: %+v", status)
	}
	if calls != 1 {
		t.Fatalf("embedding calls = %d", calls)
	}

	// profile 缺失时按同款 CAS 创建。
	var profile models.SemanticIndexProfile
	if err := database.DB.First(&profile, "id = ?", 1).Error; err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if profile.ActiveModel != "embed-v1" || profile.Generation != 1 || profile.Dimension != 2 {
		t.Fatalf("unexpected profile: %+v", profile)
	}

	var rows []models.ImageSemanticIndex
	if err := database.DB.Find(&rows).Error; err != nil {
		t.Fatalf("load image semantic rows: %v", err)
	}
	if len(rows) != 1 || rows[0].ImageID != described.ID || rows[0].ContentFingerprint == "" {
		t.Fatalf("unexpected image semantic rows: %+v", rows)
	}
	if noRow.ID == rows[0].ImageID || failedRow.ID == rows[0].ImageID {
		t.Fatalf("skipped image was indexed: %+v", rows)
	}
	var videoRows, videoAttempts int64
	if err := database.DB.Model(&models.VideoSemanticIndex{}).Count(&videoRows).Error; err != nil || videoRows != 1 {
		t.Fatalf("video semantic rows changed by image run: count=%d err=%v", videoRows, err)
	}
	if err := database.DB.Model(&models.SemanticIndexAttempt{}).Count(&videoAttempts).Error; err != nil || videoAttempts != 0 {
		t.Fatalf("image run wrote video attempts: count=%d err=%v", videoAttempts, err)
	}
}

func TestImageSemanticIndexPipelineSanitizesIndexText(t *testing.T) {
	capability := setupImageSemanticIndexTestDB(t)
	tag := models.Tag{Name: "夜景", IsActive: true}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("create tag: %v", err)
	}
	image := createImageSemanticTestImage(t, "night.jpg",
		"雨后的城市夜景，霓虹倒影。原图位于 /Users/private/photos/night.jpg，鉴权 secret-key。", tag)

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
	service := NewImageSemanticIndexService(database.DB, capability, provider)
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("start image semantic indexing: %v", err)
	}
	status := waitImageSemanticIndex(t, service)
	if !status.Completed || status.Succeeded != 1 || status.Dimension != 3 {
		t.Fatalf("unexpected final status: %+v", status)
	}

	input := <-requestText
	for _, forbidden := range []string{"secret-key", "/Users/private"} {
		if strings.Contains(input, forbidden) {
			t.Errorf("embedding input contains %q: %s", forbidden, input)
		}
	}
	for _, expected := range []string{"标题: night.jpg", "标签: 夜景", "AI 描述:", "雨后的城市夜景"} {
		if !strings.Contains(input, expected) {
			t.Errorf("embedding input missing %q: %s", expected, input)
		}
	}
	var row models.ImageSemanticIndex
	if err := database.DB.First(&row, "image_id = ?", image.ID).Error; err != nil {
		t.Fatalf("load image semantic row: %v", err)
	}
	if row.ModelIdentifier != "embed-v1" || row.Dimension != 3 || row.ContentFingerprint == "" {
		t.Fatalf("unexpected image semantic row: %+v", row)
	}
}

func TestImageSemanticIndexFingerprintResumeAndDescriptionUpdateReindexes(t *testing.T) {
	capability := setupImageSemanticIndexTestDB(t)
	image := createImageSemanticTestImage(t, "cat.jpg", "一只橘猫")
	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	calls := 0
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			calls++
			return []float64{0.1, 0.2}, nil
		})
	}
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("start first run: %v", err)
	}
	if status := waitImageSemanticIndex(t, service); !status.Completed || status.Succeeded != 1 || calls != 1 {
		t.Fatalf("first run status=%+v calls=%d", status, calls)
	}
	var indexed models.ImageSemanticIndex
	if err := database.DB.First(&indexed, "image_id = ?", image.ID).Error; err != nil {
		t.Fatalf("load indexed row: %v", err)
	}
	firstFingerprint := indexed.ContentFingerprint

	// 指纹一致 → 续跑跳过，不再调用 embedding。
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("start resume run: %v", err)
	}
	if status := waitImageSemanticIndex(t, service); status.Skipped != 1 || status.Succeeded != 0 || calls != 1 {
		t.Fatalf("resume run status=%+v calls=%d", status, calls)
	}

	// 描述更新 → 指纹失配 → 下次任务重建该图（设计 4.7.4）。
	if err := database.DB.Model(&models.ImageAIDescription{}).
		Where("image_id = ?", image.ID).Update("description", "一只趴在键盘上的橘猫").Error; err != nil {
		t.Fatalf("update description: %v", err)
	}
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("start reindex run: %v", err)
	}
	if status := waitImageSemanticIndex(t, service); status.Succeeded != 1 || status.Skipped != 0 || calls != 2 {
		t.Fatalf("reindex run status=%+v calls=%d", status, calls)
	}
	if err := database.DB.First(&indexed, "image_id = ?", image.ID).Error; err != nil {
		t.Fatalf("reload indexed row: %v", err)
	}
	if indexed.ContentFingerprint == firstFingerprint {
		t.Fatalf("fingerprint did not change after description update: %s", indexed.ContentFingerprint)
	}
}

func TestImageSemanticIndexRejectsModelMismatchOrNeedsRebuildProfile(t *testing.T) {
	capability := setupImageSemanticIndexTestDB(t)
	createImageSemanticTestImage(t, "cat.jpg", "一只橘猫")
	if err := database.DB.Create(&models.SemanticIndexProfile{ID: 1, ActiveModel: "other-model", Generation: 1}).Error; err != nil {
		t.Fatalf("create profile: %v", err)
	}
	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			t.Error("embedding must not be called when profile rejects the run")
			return []float64{0.1}, nil
		})
	}
	if _, err := service.Start(context.Background()); !errors.Is(err, ErrImageSemanticIndexRebuildRequired) {
		t.Fatalf("model mismatch error = %v", err)
	}
	if err := database.DB.Model(&models.SemanticIndexProfile{}).Where("id = ?", 1).
		Updates(map[string]any{"active_model": "embed-v1", "needs_rebuild": true}).Error; err != nil {
		t.Fatalf("mark needs_rebuild: %v", err)
	}
	if _, err := service.Start(context.Background()); !errors.Is(err, ErrImageSemanticIndexRebuildRequired) {
		t.Fatalf("needs_rebuild error = %v", err)
	}
	if service.Status().Running {
		t.Fatalf("service must stay idle after rejected starts")
	}
}

func TestImageSemanticIndexDimensionMismatchLeavesFailureTrail(t *testing.T) {
	capability := setupImageSemanticIndexTestDB(t)
	createImageSemanticTestImage(t, "cat.jpg", "一只橘猫")
	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	vector := []float64{0.1, 0.2}
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) { return vector, nil })
	}
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("start initial run: %v", err)
	}
	if status := waitImageSemanticIndex(t, service); !status.Completed || status.Dimension != 2 {
		t.Fatalf("initial status = %+v", status)
	}

	// 维度漂移（端点换模型未换名）：失败留痕 + needs_rebuild。
	mismatched := createImageSemanticTestImage(t, "dog.jpg", "一只柴犬")
	vector = []float64{0.1, 0.2, 0.3}
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("start drifted run: %v", err)
	}
	status := waitImageSemanticIndex(t, service)
	if status.Completed || !status.NeedsRebuild || status.Failed != 1 || len(status.Failures) != 1 || status.Failures[0].Code != "dimension_mismatch" {
		t.Fatalf("dimension mismatch status = %+v", status)
	}
	var attempt models.ImageSemanticIndexAttempt
	if err := database.DB.First(&attempt, "image_id = ?", mismatched.ID).Error; err != nil {
		t.Fatalf("load failed attempt: %v", err)
	}
	if attempt.Status != models.SemanticIndexAttemptFailed || attempt.ErrorCode != "dimension_mismatch" || !strings.Contains(attempt.LastError, "dimension changed") {
		t.Fatalf("failed attempt = %+v", attempt)
	}
	var profile models.SemanticIndexProfile
	if err := database.DB.First(&profile, "id = ?", 1).Error; err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if !profile.NeedsRebuild {
		t.Fatalf("profile needs_rebuild not set: %+v", profile)
	}
	if _, err := service.Start(context.Background()); !errors.Is(err, ErrImageSemanticIndexRebuildRequired) {
		t.Fatalf("retry without rebuild error = %v", err)
	}
}

func TestImageSemanticIndexCancellationResumesWithoutRepeatingSuccess(t *testing.T) {
	capability := setupImageSemanticIndexTestDB(t)
	createImageSemanticTestImage(t, "first.jpg", "第一张")
	createImageSemanticTestImage(t, "second.jpg", "第二张")
	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	blocking := &cancellingSemanticEmbedder{secondStarted: make(chan struct{})}
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient { return blocking }
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("start image semantic indexing: %v", err)
	}
	select {
	case <-blocking.secondStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("second embedding did not start")
	}
	if err := service.Cancel(); err != nil {
		t.Fatalf("cancel image semantic indexing: %v", err)
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
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("resume image semantic indexing: %v", err)
	}
	status = waitImageSemanticIndex(t, service)
	if !status.Completed || status.Succeeded != 1 || status.Skipped != 1 {
		t.Fatalf("resumed status = %+v", status)
	}
	var count int64
	if err := database.DB.Model(&models.ImageSemanticIndex{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("image semantic row count = %d, err=%v", count, err)
	}
}

func TestImageSemanticIndexRejectsConcurrentStart(t *testing.T) {
	capability := setupImageSemanticIndexTestDB(t)
	createImageSemanticTestImage(t, "cat.jpg", "一只橘猫")
	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	started := make(chan struct{})
	var once sync.Once
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(ctx context.Context, _ string) ([]float64, error) {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return nil, ctx.Err()
		})
	}
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("start image semantic indexing: %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("embedding did not start")
	}
	status, err := service.Start(context.Background())
	if err == nil {
		t.Fatalf("second Start must be rejected while running: %+v", status)
	}
	if !status.Running {
		t.Fatalf("rejected Start should report the running snapshot: %+v", status)
	}
	if err := service.Cancel(); err != nil {
		t.Fatalf("cancel image semantic indexing: %v", err)
	}
	service.StopAndWait()
}

func TestImageSemanticIndexUnavailableCapabilityIsExplicit(t *testing.T) {
	setupVideoServiceTestDB(t)
	capability := database.PrepareImageSemanticVectorStorage(database.DB)
	if capability.Available {
		t.Fatalf("sqlite must not report pgvector capability: %+v", capability)
	}
	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	status, err := service.Start(context.Background())
	if !errors.Is(err, ErrImageSemanticIndexUnavailable) {
		t.Fatalf("start error = %v", err)
	}
	if status.Available || status.Unavailable == "" || service.Status().Running {
		t.Fatalf("unavailable status is not explicit: %+v", status)
	}
	if _, err := service.SearchImagesSemantic(context.Background(), ImageSemanticSearchRequest{Query: "夜景"}); !errors.Is(err, ErrImageSemanticIndexUnavailable) {
		t.Fatalf("search error = %v", err)
	}
}

func TestImageSemanticIndexExcludesSoftDeletedImages(t *testing.T) {
	capability := setupImageSemanticIndexTestDB(t)
	kept := createImageSemanticTestImage(t, "kept.jpg", "保留的图片")
	removed := createImageSemanticTestImage(t, "removed.jpg", "被软删的图片")
	if err := database.DB.Delete(&models.Image{}, removed.ID).Error; err != nil {
		t.Fatalf("soft delete image: %v", err)
	}
	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			return []float64{0.1, 0.2}, nil
		})
	}
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("start image semantic indexing: %v", err)
	}
	status := waitImageSemanticIndex(t, service)
	if status.Total != 1 || status.Succeeded != 1 {
		t.Fatalf("soft-deleted image entered the snapshot: %+v", status)
	}
	var rows []models.ImageSemanticIndex
	if err := database.DB.Find(&rows).Error; err != nil {
		t.Fatalf("load image semantic rows: %v", err)
	}
	if len(rows) != 1 || rows[0].ImageID != kept.ID {
		t.Fatalf("unexpected indexed rows: %+v", rows)
	}
	var profile models.SemanticIndexProfile
	if err := database.DB.First(&profile, "id = ?", 1).Error; err != nil {
		t.Fatalf("load profile: %v", err)
	}
	coverage, err := service.imageSemanticCoverage(context.Background(), profile)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage.Total != 1 || coverage.Indexed != 1 {
		t.Fatalf("软删除图片不应计入覆盖率: %+v", coverage)
	}
}
