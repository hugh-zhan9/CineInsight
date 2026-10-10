package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// 场景检索测试共用的夹具：库、视频、索引行、可控向量的 worker 替身与计数的外部客户端替身。

func setupSceneDB(t *testing.T, provider string) {
	t.Helper()
	database.DB = dbtest.Open(t)
	settings := models.Settings{ID: 1, VideoExtensions: ".mp4", SceneVisualProvider: provider, SceneVisualIntervalSeconds: 5}
	if err := database.DB.Create(&settings).Error; err != nil {
		t.Fatalf("创建设置失败(%s): %v", dbtest.Backend(), err)
	}
}

func seedSceneVideo(t *testing.T, name string, payload []byte, durationSeconds float64) models.Video {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("写视频文件失败: %v", err)
	}
	video := models.Video{Name: name, Path: path, Directory: root, Size: int64(len(payload)), Duration: durationSeconds}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败(%s): %v", dbtest.Backend(), err)
	}
	return video
}

type sceneSegmentFixture struct {
	StartMS int64
	EndMS   int64
	Vector  []float32
	Caption string
}

// seedSceneIndex 直接写一个视频的索引状态与段（状态的 source_size 取 videos.size）。
func seedSceneIndex(t *testing.T, video models.Video, modelID string, intervalMS int, segments []sceneSegmentFixture) {
	t.Helper()
	now := time.Now()
	state := models.SceneIndexState{
		VideoID: video.ID, ModelID: modelID, Provider: SceneProviderLocal, SourceSize: video.Size, SourceMtimeNS: 1,
		IntervalMS: intervalMS, SegmentCount: len(segments), Status: models.SceneIndexStatusIndexed, IndexedAt: &now,
	}
	if err := database.DB.Create(&state).Error; err != nil {
		t.Fatalf("写索引状态失败(%s): %v", dbtest.Backend(), err)
	}
	for _, fixture := range segments {
		row := models.SceneVisualSegment{VideoID: video.ID, ModelID: modelID, StartMS: fixture.StartMS, EndMS: fixture.EndMS, Caption: fixture.Caption}
		if fixture.Vector != nil {
			blob, err := quantizeSceneVector(fixture.Vector)
			if err != nil {
				t.Fatal(err)
			}
			row.Embedding = blob
		}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatalf("写画面段失败(%s): %v", dbtest.Backend(), err)
		}
	}
}

// fakeSceneVectors 是 worker 替身：文本向量查表，图片向量由 image 回调按帧路径给出。
type fakeSceneVectors struct {
	mu         sync.Mutex
	texts      map[string][]float32
	image      func(path string) []float32
	imageErr   error
	textCalls  int
	imageCalls int
	onImages   func()
}

func (f *fakeSceneVectors) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.textCalls++
	out := make([][]float32, 0, len(texts))
	for _, text := range texts {
		vector, ok := f.texts[text]
		if !ok {
			return nil, errors.New("scene_worker_error: inference_failed")
		}
		out = append(out, vector)
	}
	return out, nil
}

func (f *fakeSceneVectors) EmbedImages(ctx context.Context, paths []string) ([][]float32, error) {
	f.mu.Lock()
	f.imageCalls++
	hook, err := f.onImages, f.imageErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out := make([][]float32, 0, len(paths))
	for _, path := range paths {
		out = append(out, f.image(path))
	}
	return out, nil
}

// configureSceneExternalEndpoint 把 AI 打标接口指向一个只计数的本地假服务（并清掉环境变量兜底），
// 返回读取请求次数的函数：断言"外部接口一次都没被碰"用。
func configureSceneExternalEndpoint(t *testing.T, model string) func() int {
	t.Helper()
	for _, key := range []string{envAITaggingBaseURL, envAITaggingModel, envAITaggingAPIKey} {
		t.Setenv(key, "")
	}
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	if err := database.DB.Model(&models.Settings{}).Where("id = ?", 1).
		Updates(map[string]any{"ai_tagging_base_url": server.URL, "ai_tagging_model": model}).Error; err != nil {
		t.Fatal(err)
	}
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return requests
	}
}

// countingCaptions 是外部描述客户端替身：记录工厂调用与描述请求次数。
type countingCaptions struct {
	mu           sync.Mutex
	model        string
	factoryCalls int
	captionCalls int
	frames       int
	caption      func(index int) string
}

func (c *countingCaptions) factory() (SceneCaptionClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.factoryCalls++
	return c, nil
}

func (c *countingCaptions) ModelName() string { return c.model }

func (c *countingCaptions) CaptionFrames(_ context.Context, jpegs [][]byte) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.captionCalls++
	out := make([]string, len(jpegs))
	for index := range jpegs {
		out[index] = c.caption(c.frames)
		c.frames++
	}
	return out, nil
}

func (c *countingCaptions) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.factoryCalls, c.captionCalls
}
