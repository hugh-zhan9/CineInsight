package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// fakeSceneExtractor 往 dir 里写 frames 张"帧"（内容是帧序号），返回按时间排序的路径。
func fakeSceneExtractor(frames int) sceneFrameExtractor {
	return func(ctx context.Context, _ string, dir string, _ int64) ([]string, error) {
		paths := make([]string, 0, frames)
		for index := 0; index < frames; index++ {
			path := filepath.Join(dir, fmt.Sprintf("f_%06d.jpg", index+1))
			if err := os.WriteFile(path, []byte(fmt.Sprintf("frame-%d", index)), 0o644); err != nil {
				return nil, err
			}
			paths = append(paths, path)
		}
		return paths, ctx.Err()
	}
}

// frameIndexOf 从 f_%06d.jpg 还原帧序号（从 0 起）。
func frameIndexOf(path string) int {
	var index int
	_, _ = fmt.Sscanf(filepath.Base(path), "f_%06d.jpg", &index)
	return index - 1
}

func newTestSceneIndex(t *testing.T, vectors sceneVectorSource, captions sceneCaptionClientFactory, extract sceneFrameExtractor, available bool) *SceneIndexService {
	t.Helper()
	tempRoot := t.TempDir()
	return &SceneIndexService{
		vectors:          vectors,
		captions:         captions,
		extract:          extract,
		runtimeAvailable: func() bool { return available },
		tempRoot:         func() string { return tempRoot },
		now:              time.Now,
		status:           SceneIndexStatus{Failures: []SceneIndexFailure{}},
	}
}

func waitSceneIndex(t *testing.T, service *SceneIndexService, accept func(SceneIndexStatus) bool) SceneIndexStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if status := service.Status(); accept(status) {
			return status
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待建索引状态超时: %+v", service.Status())
	return SceneIndexStatus{}
}

func sceneIndexDone(status SceneIndexStatus) bool { return !status.Running }

func loadSceneRows(t *testing.T, videoID uint) (models.SceneIndexState, []models.SceneVisualSegment, bool) {
	t.Helper()
	var state models.SceneIndexState
	found := database.DB.Where("video_id = ?", videoID).Limit(1).Find(&state).RowsAffected == 1
	var segments []models.SceneVisualSegment
	if err := database.DB.Where("video_id = ?", videoID).Order("start_ms ASC").Find(&segments).Error; err != nil {
		t.Fatal(err)
	}
	return state, segments, found
}

// 本地建索引：抽帧 → 向量 → 分段 → 事务落库；源未变化时第二轮直接跳过。
func TestSceneIndexBuildsSegmentsAndSkipsFreshVideos(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	rng := rand.New(rand.NewSource(61))
	a, b := unitVector(rng), unitVector(rng)
	frames := [][]float32{a, blendVector(a, b, 0.05), a, blendVector(a, b, 0.45), b}
	vectors := &fakeSceneVectors{image: func(path string) []float32 { return frames[frameIndexOf(path)] }}
	service := newTestSceneIndex(t, vectors, nil, fakeSceneExtractor(len(frames)), true)
	registry := NewBackgroundTaskRegistry()
	service.SetBackgroundTaskRegistry(registry)
	video := seedSceneVideo(t, "clip.mp4", []byte("clip-bytes"), 23)

	if _, err := service.Start(context.Background(), SceneIndexRequest{}); err != nil {
		t.Fatalf("启动失败(%s): %v", dbtest.Backend(), err)
	}
	status := waitSceneIndex(t, service, sceneIndexDone)
	if !status.Completed || status.Succeeded != 1 || status.Failed != 0 || status.Total != 1 || status.ModelID != SceneLocalModelID {
		t.Fatalf("一轮应成功处理 1 项: %+v", status)
	}
	state, segments, found := loadSceneRows(t, video.ID)
	info, _ := os.Stat(video.Path)
	if !found || state.Status != models.SceneIndexStatusIndexed || state.ModelID != SceneLocalModelID || state.IntervalMS != 5000 ||
		state.SourceSize != info.Size() || state.SourceMtimeNS != info.ModTime().UnixNano() || state.SegmentCount != 3 || state.IndexedAt == nil {
		t.Fatalf("索引状态不对: %+v", state)
	}
	if len(segments) != 3 || segments[0].EndMS != 15000 || segments[2].EndMS != 23000 || len(segments[0].Embedding) != sceneQuantizedBytes {
		t.Fatalf("段不对: %+v", segments)
	}
	if entries, _ := os.ReadDir(service.tempRoot()); len(entries) != 0 {
		t.Fatalf("每项结束应删除临时帧目录，残留 %d 项", len(entries))
	}
	if len(registry.Snapshot()) != 0 {
		t.Fatalf("结束后登记表应清空: %+v", registry.Snapshot())
	}

	if _, err := service.Start(context.Background(), SceneIndexRequest{}); err != nil {
		t.Fatal(err)
	}
	status = waitSceneIndex(t, service, sceneIndexDone)
	if status.Total != 0 || vectors.imageCalls != 1 {
		t.Fatalf("源未变化的视频不该重做: %+v calls=%d", status, vectors.imageCalls)
	}

	// 源文件变了：重建并替换旧段。
	if err := os.WriteFile(video.Path, []byte("clip-bytes-longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	frames = [][]float32{a, b}
	service.extract = fakeSceneExtractor(2)
	if _, err := service.Start(context.Background(), SceneIndexRequest{VideoIDs: []uint{video.ID}}); err != nil {
		t.Fatal(err)
	}
	waitSceneIndex(t, service, sceneIndexDone)
	state, segments, _ = loadSceneRows(t, video.ID)
	if len(segments) != 2 || state.SegmentCount != 2 || state.SourceSize != int64(len("clip-bytes-longer")) {
		t.Fatalf("重建应替换旧段: state=%+v segments=%d", state, len(segments))
	}
}

// 取消：正在处理的那一项被丢弃，不写段也不写状态。
func TestSceneIndexCancelDiscardsInFlightItem(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	rng := rand.New(rand.NewSource(67))
	vector := unitVector(rng)
	var service *SceneIndexService
	vectors := &fakeSceneVectors{image: func(string) []float32 { return vector }}
	vectors.onImages = func() { _ = service.Cancel() }
	service = newTestSceneIndex(t, vectors, nil, fakeSceneExtractor(3), true)
	video := seedSceneVideo(t, "cancel.mp4", []byte("cancel"), 15)
	if _, err := service.Start(context.Background(), SceneIndexRequest{}); err != nil {
		t.Fatal(err)
	}
	status := waitSceneIndex(t, service, sceneIndexDone)
	if !status.Cancelled || status.Completed || status.Succeeded != 0 {
		t.Fatalf("应以取消收尾: %+v", status)
	}
	if _, segments, found := loadSceneRows(t, video.ID); found || len(segments) != 0 {
		t.Fatalf("取消的那一项不该落库: found=%v segments=%d", found, len(segments))
	}
	if err := service.Cancel(); !errors.Is(err, ErrSceneIndexNotRunning) {
		t.Fatalf("没有任务时取消应返回 ErrSceneIndexNotRunning: %v", err)
	}
}

// 源文件在推理期间变了：结果整批丢弃，状态记 source_changed，下一轮重试。
func TestSceneIndexSourceChangeMidRunDiscardsResult(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	rng := rand.New(rand.NewSource(71))
	vector := unitVector(rng)
	video := seedSceneVideo(t, "growing.mp4", []byte("partial"), 10)
	vectors := &fakeSceneVectors{image: func(string) []float32 { return vector }}
	vectors.onImages = func() {
		_ = os.WriteFile(video.Path, []byte("partial+appended"), 0o644)
	}
	service := newTestSceneIndex(t, vectors, nil, fakeSceneExtractor(2), true)
	if _, err := service.Start(context.Background(), SceneIndexRequest{}); err != nil {
		t.Fatal(err)
	}
	status := waitSceneIndex(t, service, sceneIndexDone)
	if status.Failed != 1 || len(status.Failures) != 1 || status.Failures[0].Error != sceneErrorSourceChanged {
		t.Fatalf("应记一次 source_changed 失败: %+v", status)
	}
	state, segments, found := loadSceneRows(t, video.ID)
	if !found || state.Status != models.SceneIndexStatusFailed || state.LastError != sceneErrorSourceChanged || len(segments) != 0 {
		t.Fatalf("源变化后不该留下段，状态应为 failed/source_changed: %+v segments=%d", state, len(segments))
	}
}

// 失败原因落库前抹掉绝对路径；内存里的失败清单同样不带路径。
func TestSceneIndexFailureRecordsScrubbedError(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	video := seedSceneVideo(t, "broken.mp4", []byte("broken"), 10)
	extract := func(context.Context, string, string, int64) ([]string, error) {
		return nil, fmt.Errorf("ffmpeg 抽帧失败: exit status 1: %s: Invalid data found", video.Path)
	}
	service := newTestSceneIndex(t, &fakeSceneVectors{}, nil, extract, true)
	if _, err := service.Start(context.Background(), SceneIndexRequest{}); err != nil {
		t.Fatal(err)
	}
	status := waitSceneIndex(t, service, sceneIndexDone)
	state, _, found := loadSceneRows(t, video.ID)
	if !found || state.Status != models.SceneIndexStatusFailed || state.Provider != SceneProviderLocal {
		t.Fatalf("失败应写 failed 状态: %+v", state)
	}
	for _, message := range []string{state.LastError, status.Failures[0].Error} {
		if strings.Contains(message, video.Path) || strings.Contains(message, video.Directory) || !strings.Contains(message, "<path>") {
			t.Fatalf("失败原因必须抹掉路径: %q", message)
		}
	}
}

// TC-18：本地运行时不可用、或请求外部但设置没有启用外部时，只报错，外部客户端一次也不碰；
// 本地推理失败同样只记本地失败，提供方不切换。
func TestSceneIndexLocalFailureNeverSwitchesProvider(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	video := seedSceneVideo(t, "local.mp4", []byte("local"), 10)
	captions := &countingCaptions{model: "vision-x", caption: func(int) string { return "不该出现" }}
	unavailable := newTestSceneIndex(t, &fakeSceneVectors{}, captions.factory, fakeSceneExtractor(2), false)
	if _, err := unavailable.Start(context.Background(), SceneIndexRequest{}); !errors.Is(err, ErrSceneRuntimeUnavailable) {
		t.Fatalf("运行时不可用应返回 scene_runtime_unavailable: %v", err)
	}
	if _, err := unavailable.Start(context.Background(), SceneIndexRequest{Provider: SceneProviderExternal}); !errors.Is(err, ErrSceneExternalNotEnabled) {
		t.Fatalf("设置未启用外部时请求外部应被拒绝: %v", err)
	}
	failing := newTestSceneIndex(t, &fakeSceneVectors{imageErr: errors.New("scene_worker_error: inference_failed")}, captions.factory, fakeSceneExtractor(2), true)
	if _, err := failing.Start(context.Background(), SceneIndexRequest{}); err != nil {
		t.Fatal(err)
	}
	status := waitSceneIndex(t, failing, sceneIndexDone)
	state, segments, _ := loadSceneRows(t, video.ID)
	if status.Failed != 1 || state.Provider != SceneProviderLocal || state.Status != models.SceneIndexStatusFailed || len(segments) != 0 {
		t.Fatalf("本地失败应只记本地失败: status=%+v state=%+v", status, state)
	}
	if factoryCalls, captionCalls := captions.counts(); factoryCalls != 0 || captionCalls != 0 {
		t.Fatalf("外部客户端调用次数必须为 0: factory=%d caption=%d", factoryCalls, captionCalls)
	}
}

// 外部提供方：只有设置为 external 时才向 AI 打标配置的接口发请求（每 8 帧一次），
// 描述存进 caption、不存向量，模型标识为 external-caption:<模型名>@1。
func TestSceneIndexExternalProviderCallsHTTPOnlyWhenEnabled(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	var mu sync.Mutex
	var requests, frames int
	var authorization string
	snapshot := func() (int, string) {
		mu.Lock()
		defer mu.Unlock()
		return requests, authorization
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		authorization = r.Header.Get("Authorization")
		var body struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var parts []map[string]any
		_ = json.Unmarshal(body.Messages[len(body.Messages)-1].Content, &parts)
		captions := []string{}
		for _, part := range parts {
			if part["type"] == "image_url" {
				captions = append(captions, fmt.Sprintf("第%d帧：海边的灯塔与很长很长很长很长很长很长很长很长很长的描述", frames))
				frames++
			}
		}
		content, _ := json.Marshal(map[string]any{"captions": captions})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": string(content)}}}})
	}))
	defer server.Close()
	if err := database.DB.Model(&models.Settings{}).Where("id = ?", 1).Updates(map[string]any{
		"ai_tagging_base_url": server.URL, "ai_tagging_model": "vision-x", "ai_tagging_api_key": "key-123",
	}).Error; err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(73))
	vector := unitVector(rng)
	video := seedSceneVideo(t, "lighthouse.mp4", []byte("lighthouse"), 50)
	service := newTestSceneIndex(t, &fakeSceneVectors{image: func(string) []float32 { return vector }}, defaultSceneCaptionClientFactory, fakeSceneExtractor(10), true)

	if _, err := service.Start(context.Background(), SceneIndexRequest{}); err != nil {
		t.Fatal(err)
	}
	waitSceneIndex(t, service, sceneIndexDone)
	if requests, _ := snapshot(); requests != 0 {
		t.Fatalf("提供方为 local 时不该有任何外部请求: %d", requests)
	}

	if err := database.DB.Model(&models.Settings{}).Where("id = ?", 1).Update("scene_visual_provider", SceneProviderExternal).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(context.Background(), SceneIndexRequest{}); err != nil {
		t.Fatalf("外部建索引启动失败: %v", err)
	}
	status := waitSceneIndex(t, service, sceneIndexDone)
	gotRequests, gotAuthorization := snapshot()
	if status.Succeeded != 1 || gotRequests != 2 || gotAuthorization != "Bearer key-123" {
		t.Fatalf("10 帧应分 2 次请求: status=%+v requests=%d auth=%q", status, gotRequests, gotAuthorization)
	}
	state, segments, _ := loadSceneRows(t, video.ID)
	if state.Provider != SceneProviderExternal || state.ModelID != "external-caption:vision-x@1" || len(segments) != 10 {
		t.Fatalf("外部索引状态或段不对: %+v segments=%d", state, len(segments))
	}
	if segments[0].Embedding != nil || segments[0].ModelID != state.ModelID || len([]rune(segments[0].Caption)) > sceneCaptionMaxRunes {
		t.Fatalf("外部段应只存不超过 30 字的描述: %+v", segments[0])
	}
}

// FIFO：运行中再次启动时请求排到队尾，两个视频都处理完。
func TestSceneIndexQueuesRequestsWhileRunning(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	rng := rand.New(rand.NewSource(79))
	vector := unitVector(rng)
	first := seedSceneVideo(t, "first.mp4", []byte("first"), 10)
	second := seedSceneVideo(t, "second.mp4", []byte("second"), 10)
	release := make(chan struct{})
	var service *SceneIndexService
	vectors := &fakeSceneVectors{image: func(string) []float32 { return vector }}
	vectors.onImages = func() {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}
	service = newTestSceneIndex(t, vectors, nil, fakeSceneExtractor(1), true)
	if _, err := service.Start(context.Background(), SceneIndexRequest{VideoIDs: []uint{first.ID}}); err != nil {
		t.Fatal(err)
	}
	waitSceneIndex(t, service, func(status SceneIndexStatus) bool { return status.CurrentVideoID == first.ID })
	queued, err := service.Start(context.Background(), SceneIndexRequest{VideoIDs: []uint{first.ID, second.ID}})
	if err != nil || !queued.Running {
		t.Fatalf("运行中再次启动应排队: %+v %v", queued, err)
	}
	close(release)
	status := waitSceneIndex(t, service, sceneIndexDone)
	if status.Succeeded != 2 || status.Total != 2 {
		t.Fatalf("两个视频都应处理且不重复计数: %+v", status)
	}
	if _, segments, _ := loadSceneRows(t, second.ID); len(segments) != 1 {
		t.Fatal("排队的请求应被处理")
	}
}

// 清理旧索引：删除非当前模型的段与状态；建索引进行中拒绝。
func TestSceneClearStaleIndexKeepsCurrentModel(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	_, near, _, _ := sceneTestVectors(83)
	current := seedSceneVideo(t, "current.mp4", []byte("c"), 10)
	old := seedSceneVideo(t, "old.mp4", []byte("o"), 10)
	seedSceneIndex(t, current, SceneLocalModelID, 5000, []sceneSegmentFixture{{StartMS: 0, EndMS: 5000, Vector: near}})
	seedSceneIndex(t, old, "cn-clip-vit-b16-q8@0", 5000, []sceneSegmentFixture{{StartMS: 0, EndMS: 5000, Vector: near}, {StartMS: 5000, EndMS: 9000, Vector: near}})
	service := newTestSceneIndex(t, &fakeSceneVectors{}, nil, fakeSceneExtractor(1), true)
	result, err := service.ClearStaleIndex(context.Background())
	if err != nil || result.DeletedSegments != 2 || result.DeletedStates != 1 || result.ModelID != SceneLocalModelID {
		t.Fatalf("清理结果不对(%s): %+v %v", dbtest.Backend(), result, err)
	}
	if _, segments, found := loadSceneRows(t, current.ID); !found || len(segments) != 1 {
		t.Fatal("当前模型的索引必须保留")
	}
	service.status.Running = true
	if _, err := service.ClearStaleIndex(context.Background()); !errors.Is(err, ErrSceneIndexRunning) {
		t.Fatalf("建索引进行中应拒绝清理: %v", err)
	}
}
