package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
)

func scenePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 以图搜片：上传的图片向量化后按画面段打分；临时文件用完即删，不走文字编码器。
func TestSceneImageSearchRanksSegmentsAndRemovesTempFile(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	query, near, mid, far := sceneTestVectors(77)
	a := seedSceneVideo(t, "a.mp4", []byte("aaaa"), 60)
	b := seedSceneVideo(t, "b.mp4", []byte("bbbbb"), 60)
	seedSceneIndex(t, a, SceneLocalModelID, 5000, []sceneSegmentFixture{
		{StartMS: 0, EndMS: 5000, Vector: far},
		{StartMS: 20000, EndMS: 25000, Vector: near},
	})
	seedSceneIndex(t, b, SceneLocalModelID, 5000, []sceneSegmentFixture{{StartMS: 30000, EndMS: 35000, Vector: mid}})
	var seenPath string
	vectors := &fakeSceneVectors{image: func(path string) []float32 {
		seenPath = path
		if _, err := os.Stat(path); err != nil {
			t.Errorf("worker 读的时候临时文件应该在: %v", err)
		}
		return query
	}}
	encoded := "data:image/png;base64," + base64.StdEncoding.EncodeToString(scenePNG(t))
	result, err := newTestSceneSearch(vectors, true).SearchByImage(context.Background(), SceneImageSearchRequest{ImageBase64: encoded, Limit: 2})
	if err != nil {
		t.Fatalf("以图搜片失败: %v", err)
	}
	if len(result.Hits) != 2 || result.Hits[0].VideoID != a.ID || result.Hits[0].StartMS != 20000 || result.Hits[1].VideoID != b.ID {
		t.Fatalf("应按相似度排：a 的 20s 段在前，b 其次: %+v", result.Hits)
	}
	if result.Hits[0].Source != SceneSourceVisual || result.Hits[0].Title != "a.mp4" {
		t.Fatalf("命中应标为画面来源并带标题: %+v", result.Hits[0])
	}
	if vectors.imageCalls != 1 || vectors.textCalls != 0 {
		t.Fatalf("只调用图片编码器一次: image=%d text=%d", vectors.imageCalls, vectors.textCalls)
	}
	if _, err := os.Stat(seenPath); !os.IsNotExist(err) {
		t.Fatalf("临时文件用完应删除: %v", err)
	}
}

// 用某部片的画面找相似：排除来源片子里这一刻前后 10 秒的段，同一部片别处的相似镜头与别的片照常返回。
func TestSceneImageSearchFromFrameExcludesSourceMoment(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	query, near, mid, _ := sceneTestVectors(78)
	a := seedSceneVideo(t, "a.mp4", []byte("aaaa"), 120)
	b := seedSceneVideo(t, "b.mp4", []byte("bbbbb"), 60)
	seedSceneIndex(t, a, SceneLocalModelID, 5000, []sceneSegmentFixture{
		{StartMS: 40000, EndMS: 45000, Vector: query}, // 来源这一刻本身
		{StartMS: 50000, EndMS: 55000, Vector: near},  // 来源后 10 秒内，也排除
		{StartMS: 90000, EndMS: 95000, Vector: near},  // 同一部片别处的相似镜头，保留
	})
	seedSceneIndex(t, b, SceneLocalModelID, 5000, []sceneSegmentFixture{{StartMS: 0, EndMS: 5000, Vector: mid}})
	frame := scenePNG(t)
	var rendered []any
	search := newTestSceneSearch(&fakeSceneVectors{image: func(string) []float32 { return query }}, true)
	search.SetFrameRenderer(func(_ context.Context, videoID uint, ms int64, width int) ([]byte, error) {
		rendered = append(rendered, videoID, ms, width)
		return frame, nil
	})
	result, err := search.SearchByImage(context.Background(), SceneImageSearchRequest{VideoID: a.ID, AtMS: 42000, Limit: 10})
	if err != nil {
		t.Fatalf("找相似画面失败: %v", err)
	}
	if len(rendered) != 3 || rendered[0] != a.ID || rendered[1] != int64(42000) || rendered[2] != sceneSimilarFrameWidth {
		t.Fatalf("应取来源片子这一刻的画面: %v", rendered)
	}
	if len(result.Hits) != 2 || result.Hits[0].VideoID != a.ID || result.Hits[0].StartMS != 90000 || result.Hits[1].VideoID != b.ID {
		t.Fatalf("应排除来源附近、保留别处与别的片: %+v", result.Hits)
	}
}

func TestSceneImageSearchValidatesInput(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	search := newTestSceneSearch(&fakeSceneVectors{image: func(string) []float32 { return nil }}, true)
	png64 := base64.StdEncoding.EncodeToString(scenePNG(t))
	huge := base64.StdEncoding.EncodeToString(append(scenePNG(t), make([]byte, sceneImageMaxBytes)...))
	for name, request := range map[string]SceneImageSearchRequest{
		"什么都没给":     {},
		"图片和画面都给了":  {ImageBase64: png64, VideoID: 3},
		"不是 base64": {ImageBase64: "%%%"},
		"不是图片":      {ImageBase64: base64.StdEncoding.EncodeToString([]byte("hello, not an image at all"))},
		"超过 20 MB":  {ImageBase64: huge},
		"时间点为负":     {VideoID: 3, AtMS: -1},
	} {
		if _, err := search.SearchByImage(context.Background(), request); !errors.Is(err, ErrSceneImageInvalid) {
			t.Fatalf("%s 应返回 scene_image_invalid，实际 %v", name, err)
		}
	}
}

// 外部描述没有向量、本地模型没就绪、还没有画面索引、推理出错：都只给提示，不报错也不调用别的提供方。
func TestSceneImageSearchNotices(t *testing.T) {
	png64 := base64.StdEncoding.EncodeToString(scenePNG(t))
	run := func(provider string, available bool, vectors *fakeSceneVectors, seed bool) (SceneSearchResult, *fakeSceneVectors) {
		t.Helper()
		setupSceneDB(t, provider)
		if seed {
			query, _, _, _ := sceneTestVectors(5)
			video := seedSceneVideo(t, "a.mp4", []byte("aaaa"), 60)
			seedSceneIndex(t, video, SceneLocalModelID, 5000, []sceneSegmentFixture{{StartMS: 0, EndMS: 5000, Vector: query}})
		}
		result, err := newTestSceneSearch(vectors, available).SearchByImage(context.Background(), SceneImageSearchRequest{ImageBase64: png64})
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		return result, vectors
	}
	ok := func() *fakeSceneVectors {
		return &fakeSceneVectors{image: func(string) []float32 { return []float32{1} }}
	}

	result, vectors := run(SceneProviderExternal, true, ok(), false)
	if strings.Join(result.Notices, ",") != SceneNoticeImageSearchUnsupported || vectors.imageCalls != 0 {
		t.Fatalf("外部描述：%v calls=%d", result.Notices, vectors.imageCalls)
	}
	result, vectors = run(SceneProviderLocal, false, ok(), true)
	if strings.Join(result.Notices, ",") != SceneNoticeRuntimeUnavailable || vectors.imageCalls != 0 {
		t.Fatalf("模型没就绪：%v calls=%d", result.Notices, vectors.imageCalls)
	}
	result, vectors = run(SceneProviderLocal, true, ok(), false)
	if strings.Join(result.Notices, ",") != SceneNoticeVisualIndexEmpty || vectors.imageCalls != 0 {
		t.Fatalf("没有画面索引：%v calls=%d", result.Notices, vectors.imageCalls)
	}
	failing := &fakeSceneVectors{imageErr: errors.New("scene_worker_error: inference_failed")}
	result, _ = run(SceneProviderLocal, true, failing, true)
	if strings.Join(result.Notices, ",") != SceneNoticeVisualFailed || len(result.Hits) != 0 {
		t.Fatalf("推理出错：%v %+v", result.Notices, result.Hits)
	}
}
