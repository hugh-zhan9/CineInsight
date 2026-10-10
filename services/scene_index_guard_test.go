package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 外部描述的假接口：每次请求先跑 onRequest（可在这里改设置），再按图片数回描述。
func sceneCaptionServer(t *testing.T, onRequest func(count int)) func() int {
	t.Helper()
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		count := requests
		mu.Unlock()
		onRequest(count)
		var body struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var parts []map[string]any
		_ = json.Unmarshal(body.Messages[len(body.Messages)-1].Content, &parts)
		captions := []string{}
		for range parts[1:] {
			captions = append(captions, "海边的灯塔")
		}
		content, _ := json.Marshal(map[string]any{"captions": captions})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": string(content)}}}})
	}))
	t.Cleanup(server.Close)
	for _, key := range []string{envAITaggingBaseURL, envAITaggingModel, envAITaggingAPIKey} {
		t.Setenv(key, "")
	}
	if err := database.DB.Model(&models.Settings{}).Where("id = ?", 1).Updates(map[string]any{
		"ai_tagging_base_url": server.URL, "ai_tagging_model": "vision-x", "ai_tagging_api_key": "key-1",
	}).Error; err != nil {
		t.Fatal(err)
	}
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return requests
	}
}

// I-1：外部描述只在设置仍为 external 且接口/模型/Key 未变时才发请求；中途改回本地或换 Key，
// 之后一个请求也不发，剩余项按 scene_external_revoked 跳过，不写库。
func TestSceneIndexStopsExternalCaptioningWhenSettingChanges(t *testing.T) {
	for name, change := range map[string]map[string]any{
		"switch_to_local": {"scene_visual_provider": SceneProviderLocal},
		"rotate_api_key":  {"ai_tagging_api_key": "key-2"},
	} {
		t.Run(name, func(t *testing.T) {
			setupSceneDB(t, SceneProviderExternal)
			requests := sceneCaptionServer(t, func(count int) {
				if count == 1 {
					_ = database.DB.Model(&models.Settings{}).Where("id = ?", 1).Updates(change).Error
				}
			})
			videos := []models.Video{}
			for index := 0; index < 3; index++ {
				videos = append(videos, seedSceneVideo(t, fmt.Sprintf("ext-%d.mp4", index), []byte("external"), 50))
			}
			service := newTestSceneIndex(t, &fakeSceneVectors{}, defaultSceneCaptionClientFactory, fakeSceneExtractor(10), true)
			if _, err := service.Start(context.Background(), SceneIndexRequest{}); err != nil {
				t.Fatal(err)
			}
			status := waitSceneIndex(t, service, sceneIndexDone)
			if got := requests(); got != 1 {
				t.Fatalf("设置变更后不得再发外部请求，实际共 %d 次: %+v", got, status)
			}
			if status.Succeeded != 0 || status.Skipped != 3 || len(status.Failures) == 0 || status.Failures[0].Error != "scene_external_revoked" {
				t.Fatalf("剩余项应按 scene_external_revoked 跳过: %+v", status)
			}
			for _, video := range videos {
				if _, segments, found := loadSceneRows(t, video.ID); found || len(segments) != 0 {
					t.Fatalf("撤销外部后不该写任何状态或段: video=%d found=%v", video.ID, found)
				}
			}
		})
	}
}

// I-2：源文件读不到（移动硬盘拔了）时保留已有的有效索引，只在没有状态时记一条不删数据的失败。
func TestSceneIndexUnreadableSourceKeepsExistingIndex(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	_, near, _, _ := sceneTestVectors(89)
	indexed := seedSceneVideo(t, "offline.mp4", []byte("offline"), 30)
	seedSceneIndex(t, indexed, SceneLocalModelID, 5000, []sceneSegmentFixture{
		{StartMS: 0, EndMS: 5000, Vector: near}, {StartMS: 5000, EndMS: 10000, Vector: near},
	})
	fresh := seedSceneVideo(t, "never-indexed.mp4", []byte("never"), 30)
	for _, video := range []models.Video{indexed, fresh} {
		if err := os.Remove(video.Path); err != nil {
			t.Fatal(err)
		}
	}
	extracted := 0
	extract := func(ctx context.Context, path, dir string, interval int64) ([]string, error) {
		extracted++
		return fakeSceneExtractor(1)(ctx, path, dir, interval)
	}
	service := newTestSceneIndex(t, &fakeSceneVectors{}, nil, extract, true)
	if _, err := service.Start(context.Background(), SceneIndexRequest{}); err != nil {
		t.Fatal(err)
	}
	status := waitSceneIndex(t, service, sceneIndexDone)
	state, segments, found := loadSceneRows(t, indexed.ID)
	if !found || state.Status != models.SceneIndexStatusIndexed || len(segments) != 2 {
		t.Fatalf("读不到源文件时不得删除已有索引: state=%+v segments=%d", state, len(segments))
	}
	missing, _, found := loadSceneRows(t, fresh.ID)
	if !found || missing.Status != models.SceneIndexStatusFailed || !strings.Contains(missing.LastError, "source_unreadable") {
		t.Fatalf("没有状态的视频应记一条 source_unreadable: %+v", missing)
	}
	if extracted != 0 || status.Failed != 2 {
		t.Fatalf("读不到的源不该抽帧，两项都按失败计: extract=%d status=%+v", extracted, status)
	}
}

// M-2：改了采样间隔之后，旧间隔建的段不再参与检索与覆盖率。
func TestSceneSearchIgnoresSegmentsFromOldInterval(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	query, near, _, _ := sceneTestVectors(97)
	video := seedSceneVideo(t, "interval.mp4", []byte("interval"), 30)
	seedSceneIndex(t, video, SceneLocalModelID, 5000, []sceneSegmentFixture{{StartMS: 0, EndMS: 5000, Vector: near}})
	vectors := &fakeSceneVectors{texts: map[string][]float32{"q": query}}
	search := newTestSceneSearch(vectors, true)
	if result, err := search.Search(context.Background(), SceneSearchRequest{Query: "q", Mode: SceneModeVisual}); err != nil || len(result.Hits) != 1 {
		t.Fatalf("间隔一致时应命中: %+v %v", result, err)
	}
	if err := database.DB.Model(&models.Settings{}).Where("id = ?", 1).Update("scene_visual_interval_seconds", 10).Error; err != nil {
		t.Fatal(err)
	}
	result, err := search.Search(context.Background(), SceneSearchRequest{Query: "q", Mode: SceneModeVisual})
	if err != nil || len(result.Hits) != 0 || result.Coverage.VisualIndexed != 0 || !containsString(result.Notices, SceneNoticeVisualIndexEmpty) {
		t.Fatalf("间隔改为 10 秒后旧段不该参与检索与覆盖率: %+v %v", result, err)
	}
}

// M-3：取消后紧接着启动，不能静默丢掉新请求：要么跑，要么明确报错。
func TestSceneIndexStartRightAfterCancelIsNotSilentlyDropped(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	rng := rand.New(rand.NewSource(101))
	vector := unitVector(rng)
	first := seedSceneVideo(t, "first.mp4", []byte("first"), 10)
	second := seedSceneVideo(t, "second.mp4", []byte("second"), 10)
	release := make(chan struct{})
	entered := make(chan struct{})
	var enteredOnce sync.Once
	vectors := &fakeSceneVectors{image: func(string) []float32 { return vector }}
	vectors.onImages = func() {
		enteredOnce.Do(func() { close(entered) })
		<-release
	}
	service := newTestSceneIndex(t, vectors, nil, fakeSceneExtractor(1), true)
	if _, err := service.Start(context.Background(), SceneIndexRequest{VideoIDs: []uint{first.ID}}); err != nil {
		t.Fatal(err)
	}
	// 等 worker 真正卡在取向量里再取消：只看 CurrentVideoID 时，取消可能在抽帧/查库阶段就收尾，
	// 第二次启动合法地直接成功，断言就成了时序竞争（PG 负载下复现过）。
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("worker 没有进入取向量阶段")
	}
	if err := service.Cancel(); err != nil {
		t.Fatal(err)
	}
	_, err := service.Start(context.Background(), SceneIndexRequest{VideoIDs: []uint{second.ID}})
	if err == nil || !strings.Contains(err.Error(), "scene_index_cancelling") {
		t.Fatalf("取消尚未收尾时启动应明确返回 scene_index_cancelling，实际 %v", err)
	}
	close(release)
	waitSceneIndex(t, service, sceneIndexDone)
	if _, err := service.Start(context.Background(), SceneIndexRequest{VideoIDs: []uint{second.ID}}); err != nil {
		t.Fatalf("取消收尾后应能重新启动: %v", err)
	}
	if status := waitSceneIndex(t, service, sceneIndexDone); status.Succeeded != 1 {
		t.Fatalf("重新启动的请求应被处理: %+v", status)
	}
}

// M-4：本地运行时不可用或 worker 起不来时整轮停止，不再逐个视频抽帧。
func TestSceneIndexAbortsWholeRunWhenLocalRuntimeIsGone(t *testing.T) {
	for name, setup := range map[string]func(*SceneIndexService, *fakeSceneVectors){
		"worker_reports_unavailable": func(_ *SceneIndexService, vectors *fakeSceneVectors) {
			vectors.imageErr = ErrSceneRuntimeUnavailable
		},
		"runtime_gone_after_start": func(service *SceneIndexService, _ *fakeSceneVectors) {
			calls := 0
			service.runtimeAvailable = func() bool { calls++; return calls == 1 }
		},
	} {
		t.Run(name, func(t *testing.T) {
			setupSceneDB(t, SceneProviderLocal)
			for index := 0; index < 3; index++ {
				seedSceneVideo(t, fmt.Sprintf("abort-%d.mp4", index), []byte("abort"), 10)
			}
			extracted := 0
			extract := func(ctx context.Context, path, dir string, interval int64) ([]string, error) {
				extracted++
				return fakeSceneExtractor(1)(ctx, path, dir, interval)
			}
			vectors := &fakeSceneVectors{image: func(string) []float32 { return make([]float32, sceneEmbeddingDims) }}
			service := newTestSceneIndex(t, vectors, nil, extract, true)
			setup(service, vectors)
			if _, err := service.Start(context.Background(), SceneIndexRequest{}); err != nil {
				t.Fatal(err)
			}
			status := waitSceneIndex(t, service, sceneIndexDone)
			if extracted > 1 || status.Failed != 1 || status.Skipped != 2 || status.Failures[0].Error != "scene_runtime_unavailable" {
				t.Fatalf("运行时不可用应整轮停止: extract=%d status=%+v", extracted, status)
			}
			var states int64
			database.DB.Model(&models.SceneIndexState{}).Count(&states)
			if states != 0 {
				t.Fatalf("运行时问题不是视频的错，不该写失败状态: %d", states)
			}
		})
	}
}
