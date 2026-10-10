package services

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
	"video-master/services/subtitleparser"
)

func newTestSceneSearch(vectors sceneVectorSource, available bool) *SceneSearchService {
	return &SceneSearchService{vectors: vectors, runtimeAvailable: func() bool { return available }}
}

// writeSceneSRT 在视频旁写一份 200 条的 .srt 并建逐条索引；第 180 条在片尾（01:58:00）。
func writeSceneSRT(t *testing.T, video models.Video, special map[int]string) {
	t.Helper()
	var builder strings.Builder
	for index := 0; index < 200; index++ {
		startMS := int64(index) * 35_400
		if index == 180 {
			startMS = 7_080_000
		}
		endMS := startMS + 3_500
		text := fmt.Sprintf("普通台词 %d", index)
		if value, ok := special[index]; ok {
			text = value
		}
		fmt.Fprintf(&builder, "%d\n%s --> %s\n%s\n\n", index+1, formatSRTTimestamp(startMS), formatSRTTimestamp(endMS), text)
	}
	if err := os.WriteFile(subtitleparser.SRTPathForVideo(video.Path), []byte(builder.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureSubtitleIndexForVideo(video); err != nil {
		t.Fatalf("建字幕索引失败(%s): %v", dbtest.Backend(), err)
	}
}

// TC-17 后半段对白：命中位于片尾的条目，起止时间与上下文正确；%、_、\ 按字面匹配。
func TestSceneDialogueSearchFindsLateLineWithContextAndLiteralLike(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	video := seedSceneVideo(t, "long-film.mp4", []byte("film"), 7200)
	writeSceneSRT(t, video, map[int]string{
		179: "暗号之前",
		180: `最后的100%_答案\在这里`,
		181: "暗号之后",
		20:  "最后的100xx答案在这里", // 只有把 % 和 _ 当通配符时才会命中
	})
	service := newTestSceneSearch(nil, false)
	result, err := service.Search(context.Background(), SceneSearchRequest{Query: `100%_答案\在`, Mode: SceneModeDialogue})
	if err != nil {
		t.Fatalf("检索失败(%s): %v", dbtest.Backend(), err)
	}
	if len(result.Hits) != 1 {
		t.Fatalf("应只命中片尾那一条(%s)，实际 %+v", dbtest.Backend(), result.Hits)
	}
	hit := result.Hits[0]
	if hit.StartMS != 7_080_000 || hit.EndMS != 7_083_500 || hit.Source != SceneSourceDialogue || hit.VideoID != video.ID {
		t.Fatalf("命中区间或来源不对: %+v", hit)
	}
	if hit.ContextBefore != "暗号之前" || hit.ContextAfter != "暗号之后" || hit.Title != "long-film.mp4" {
		t.Fatalf("上下文或标题不对: %+v", hit)
	}
	if result.Coverage.TotalVideos != 1 || result.Coverage.SubtitleIndexed != 1 {
		t.Fatalf("覆盖率不对: %+v", result.Coverage)
	}
	for _, notice := range result.Notices {
		if notice == SceneNoticeRuntimeUnavailable {
			t.Fatal("对白模式不该执行画面部分，也就不该报运行时不可用")
		}
	}
}

func sceneTestVectors(seed int64) (query, near, mid, far []float32) {
	rng := rand.New(rand.NewSource(seed))
	query = unitVector(rng)
	near = blendVector(query, unitVector(rng), 0.1)
	mid = blendVector(query, unitVector(rng), 0.5)
	far = unitVector(rng)
	return
}

// TC-17 无字幕画面：替身给出可控向量，命中正确段并把相邻段合并成一个区间。
func TestSceneVisualSearchRanksAndMergesAdjacentSegments(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	query, near, mid, far := sceneTestVectors(41)
	a := seedSceneVideo(t, "a.mp4", []byte("aaaa"), 60)
	b := seedSceneVideo(t, "b.mp4", []byte("bbbbb"), 60)
	seedSceneIndex(t, a, SceneLocalModelID, 5000, []sceneSegmentFixture{
		{StartMS: 0, EndMS: 5000, Vector: far},
		{StartMS: 5000, EndMS: 10000, Vector: near},
		{StartMS: 10000, EndMS: 20000, Vector: blendVector(near, far, 0.02)},
		{StartMS: 40000, EndMS: 45000, Vector: mid},
	})
	seedSceneIndex(t, b, SceneLocalModelID, 5000, []sceneSegmentFixture{{StartMS: 30000, EndMS: 35000, Vector: mid}})
	vectors := &fakeSceneVectors{texts: map[string][]float32{"夜晚的街道": query}}
	result, err := newTestSceneSearch(vectors, true).Search(context.Background(), SceneSearchRequest{Query: "夜晚的街道", Mode: SceneModeVisual, Limit: 4})
	if err != nil {
		t.Fatalf("检索失败(%s): %v", dbtest.Backend(), err)
	}
	if len(result.Hits) < 2 {
		t.Fatalf("命中太少: %+v", result.Hits)
	}
	top := result.Hits[0]
	if top.VideoID != a.ID || top.StartMS != 5000 || top.EndMS != 20000 || top.Source != SceneSourceVisual {
		t.Fatalf("首条应为 a 的合并区间 [5000,20000): %+v", top)
	}
	if top.Score < 0.8 {
		t.Fatalf("合并区间分数应取最大（≈near）: %.3f", top.Score)
	}
	for _, hit := range result.Hits[1:] {
		if hit.Score > top.Score {
			t.Fatalf("结果应按分数降序: %+v", result.Hits)
		}
		if hit.VideoID == a.ID && hit.StartMS == 0 {
			t.Fatalf("limit=4 时与查询无关的段不该挤进来: %+v", result.Hits)
		}
	}
	if vectors.textCalls != 1 || result.Coverage.VisualIndexed != 2 || result.Coverage.VisualModelID != SceneLocalModelID {
		t.Fatalf("worker 调用或覆盖率不对: calls=%d coverage=%+v", vectors.textCalls, result.Coverage)
	}
}

// TC-17 源变化与模型更换：videos.size 变了、模型标识不同、状态失败、视频失效或已删除的段一律不返回。
func TestSceneVisualSearchIgnoresStaleIndexes(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	query, near, _, _ := sceneTestVectors(43)
	segments := []sceneSegmentFixture{{StartMS: 0, EndMS: 5000, Vector: near}}
	sizeChanged := seedSceneVideo(t, "size-changed.mp4", []byte("one"), 30)
	otherModel := seedSceneVideo(t, "other-model.mp4", []byte("two"), 30)
	failed := seedSceneVideo(t, "failed.mp4", []byte("three"), 30)
	stale := seedSceneVideo(t, "stale.mp4", []byte("four"), 30)
	deleted := seedSceneVideo(t, "deleted.mp4", []byte("five"), 30)
	fresh := seedSceneVideo(t, "fresh.mp4", []byte("six"), 30)
	for _, video := range []models.Video{sizeChanged, failed, stale, deleted, fresh} {
		seedSceneIndex(t, video, SceneLocalModelID, 5000, segments)
	}
	seedSceneIndex(t, otherModel, "cn-clip-vit-b16-q8@0", 5000, segments)
	// 防御：段是当前模型、状态却属于别的模型（单写者下不会出现），也不能返回。
	mixed := seedSceneVideo(t, "mixed.mp4", []byte("seven"), 30)
	seedSceneIndex(t, mixed, SceneLocalModelID, 5000, segments)
	if err := database.DB.Model(&models.SceneIndexState{}).Where("video_id = ?", mixed.ID).Update("model_id", "cn-clip-vit-b16-q8@0").Error; err != nil {
		t.Fatal(err)
	}
	mustExec := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	mustExec(database.DB.Model(&models.Video{}).Where("id = ?", sizeChanged.ID).Update("size", 999).Error)
	mustExec(database.DB.Model(&models.SceneIndexState{}).Where("video_id = ?", failed.ID).Update("status", models.SceneIndexStatusFailed).Error)
	mustExec(database.DB.Model(&models.Video{}).Where("id = ?", stale.ID).Updates(map[string]any{"is_stale": true, "stale_reason": models.StaleReasonMissingFile}).Error)
	mustExec(database.DB.Delete(&models.Video{}, deleted.ID).Error)

	vectors := &fakeSceneVectors{texts: map[string][]float32{"q": query}}
	result, err := newTestSceneSearch(vectors, true).Search(context.Background(), SceneSearchRequest{Query: "q", Mode: SceneModeVisual})
	if err != nil {
		t.Fatalf("检索失败(%s): %v", dbtest.Backend(), err)
	}
	if len(result.Hits) != 1 || result.Hits[0].VideoID != fresh.ID {
		t.Fatalf("只应返回源未变化、当前模型的那个视频(%s): %+v", dbtest.Backend(), result.Hits)
	}
	if result.Coverage.VisualIndexed != 1 || result.Coverage.TotalVideos != 5 {
		t.Fatalf("覆盖率应只数有效索引、总数不含失效与已删除: %+v", result.Coverage)
	}
}

// TC-18：本地运行时不可用且未启用外部 → 提示 scene_runtime_unavailable，外部客户端调用 0 次，
// 对白部分照常返回。
func TestSceneSearchRuntimeUnavailableNeverCallsExternal(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	video := seedSceneVideo(t, "film.mp4", []byte("film"), 7200)
	writeSceneSRT(t, video, map[int]string{180: "雨夜里的告白"})
	vectors := &fakeSceneVectors{texts: map[string][]float32{}}
	requests := configureSceneExternalEndpoint(t, "vision-x") // 外部接口已配置也不能被碰
	result, err := newTestSceneSearch(vectors, false).Search(context.Background(), SceneSearchRequest{Query: "告白", Mode: SceneModeAll})
	if err != nil {
		t.Fatalf("检索失败(%s): %v", dbtest.Backend(), err)
	}
	if !containsString(result.Notices, SceneNoticeRuntimeUnavailable) {
		t.Fatalf("应提示 scene_runtime_unavailable: %v", result.Notices)
	}
	if got := requests(); got != 0 {
		t.Fatalf("本地不可用时绝不能向外部接口发请求: %d", got)
	}
	if vectors.textCalls != 0 {
		t.Fatal("运行时不可用时不该拉起 worker")
	}
	if len(result.Hits) != 1 || result.Hits[0].Source != SceneSourceDialogue {
		t.Fatalf("对白命中应照常返回: %+v", result.Hits)
	}
}

// 本地推理出错只给提示，不改用外部。
func TestSceneSearchLocalInferenceFailureStaysLocal(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	_, near, _, _ := sceneTestVectors(47)
	video := seedSceneVideo(t, "v.mp4", []byte("v"), 10)
	seedSceneIndex(t, video, SceneLocalModelID, 5000, []sceneSegmentFixture{{StartMS: 0, EndMS: 5000, Vector: near}})
	requests := configureSceneExternalEndpoint(t, "vision-x")
	vectors := &fakeSceneVectors{texts: map[string][]float32{}} // 任何文本都报 inference_failed
	result, err := newTestSceneSearch(vectors, true).Search(context.Background(), SceneSearchRequest{Query: "猫", Mode: SceneModeVisual})
	if err != nil {
		t.Fatalf("推理失败不该让整个检索报错: %v", err)
	}
	if !containsString(result.Notices, SceneNoticeVisualFailed) || len(result.Hits) != 0 {
		t.Fatalf("应只提示 scene_visual_failed: %+v", result)
	}
	if got := requests(); got != 0 {
		t.Fatalf("本地失败绝不改用外部: %d", got)
	}
}

func TestSceneSearchVisualIndexEmptyNotice(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	seedSceneVideo(t, "v.mp4", []byte("v"), 10)
	vectors := &fakeSceneVectors{texts: map[string][]float32{}}
	result, err := newTestSceneSearch(vectors, true).Search(context.Background(), SceneSearchRequest{Query: "猫", Mode: SceneModeVisual})
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(result.Notices, SceneNoticeVisualIndexEmpty) || vectors.textCalls != 0 {
		t.Fatalf("没有画面索引时应提示 visual_index_empty 且不拉起 worker: %+v calls=%d", result.Notices, vectors.textCalls)
	}
}

// all 模式：两路各取 limit 条，按倒数排名融合（k=60）；每条保留来源，不把两种分数相加。
func TestFuseSceneHitsReciprocalRankKeepsSources(t *testing.T) {
	dialogue := []SceneHit{{VideoID: 1, StartMS: 1, Source: SceneSourceDialogue}, {VideoID: 1, StartMS: 2, Source: SceneSourceDialogue}}
	visual := []SceneHit{
		{VideoID: 2, StartMS: 3, Source: SceneSourceVisual, Score: 0.9},
		{VideoID: 2, StartMS: 4, Source: SceneSourceVisual, Score: 0.8},
		{VideoID: 2, StartMS: 5, Source: SceneSourceVisual, Score: 0.7},
	}
	fused := fuseSceneHits(dialogue, visual, 4)
	wantStarts := []int64{1, 3, 2, 4}
	if len(fused) != 4 {
		t.Fatalf("应截到 limit=4: %+v", fused)
	}
	for index, hit := range fused {
		if hit.StartMS != wantStarts[index] {
			t.Fatalf("融合顺序应为 %v: %+v", wantStarts, fused)
		}
	}
	if fused[0].Source != SceneSourceDialogue || fused[1].Source != SceneSourceVisual {
		t.Fatalf("来源应保留: %+v", fused)
	}
	if fused[0].Score != 1.0/61 || fused[3].Score != 1.0/62 {
		t.Fatalf("分数应为 RRF 1/(60+rank): %+v", fused)
	}
}

func TestSceneSearchValidatesRequest(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	service := newTestSceneSearch(nil, false)
	for _, query := range []string{"", "   ", strings.Repeat("长", 201)} {
		if _, err := service.Search(context.Background(), SceneSearchRequest{Query: query}); !errors.Is(err, ErrSceneQueryInvalid) {
			t.Fatalf("查询 %q 应被拒绝: %v", query, err)
		}
	}
	if _, err := service.Search(context.Background(), SceneSearchRequest{Query: "猫", Mode: "smell"}); !errors.Is(err, ErrSceneModeInvalid) {
		t.Fatalf("未知模式应被拒绝: %v", err)
	}
	request, err := normalizeSceneSearchRequest(SceneSearchRequest{Query: strings.Repeat("长", 200), Limit: 999})
	if err != nil || request.Limit != 200 || request.Mode != SceneModeAll {
		t.Fatalf("200 字应合法、limit 夹到 200、模式默认 all: %+v %v", request, err)
	}
	if request, _ := normalizeSceneSearchRequest(SceneSearchRequest{Query: "x"}); request.Limit != 50 {
		t.Fatalf("limit 默认 50: %d", request.Limit)
	}
}

// 覆盖率：范围内总数、有逐条字幕索引、只有其他格式或内嵌字幕、当前模型画面已索引；筛选生效。
func TestSceneCoverageCountsWithinFilter(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	indexed := seedSceneVideo(t, "indexed.mp4", []byte("1"), 10)
	sidecar := seedSceneVideo(t, "sidecar.mp4", []byte("2"), 10)
	embedded := seedSceneVideo(t, "embedded.mp4", []byte("3"), 10)
	seedSceneVideo(t, "bare.mp4", []byte("4"), 10)
	visual := seedSceneVideo(t, "visual.mp4", []byte("5"), 10)
	stale := seedSceneVideo(t, "stale.mp4", []byte("6"), 10)
	rows := []any{
		&models.SubtitleIndexState{VideoID: indexed.ID, SubtitlePath: "/x/indexed.srt", SegmentCount: 3},
		&models.SubtitleIndexState{VideoID: sidecar.ID, SubtitlePath: "", SegmentCount: 0, HasSidecar: true},
		&models.MediaStream{VideoID: embedded.ID, StreamIndex: 2, StreamType: "subtitle"},
	}
	for _, row := range rows {
		if err := database.DB.Create(row).Error; err != nil {
			t.Fatalf("写夹具失败(%s): %v", dbtest.Backend(), err)
		}
	}
	_, near, _, _ := sceneTestVectors(53)
	seedSceneIndex(t, visual, SceneLocalModelID, 5000, []sceneSegmentFixture{{StartMS: 0, EndMS: 5000, Vector: near}})
	seedSceneIndex(t, stale, SceneLocalModelID, 5000, []sceneSegmentFixture{{StartMS: 0, EndMS: 5000, Vector: near}})
	if err := database.DB.Model(&models.Video{}).Where("id = ?", stale.ID).Updates(map[string]any{"is_stale": true, "stale_reason": models.StaleReasonMissingFile}).Error; err != nil {
		t.Fatal(err)
	}
	service := newTestSceneSearch(nil, true)
	coverage, err := service.GetCoverage(context.Background(), nil)
	if err != nil {
		t.Fatalf("读覆盖率失败(%s): %v", dbtest.Backend(), err)
	}
	want := SceneCoverage{TotalVideos: 5, SubtitleIndexed: 1, SubtitleUnindexed: 2, VisualIndexed: 1, VisualModelID: SceneLocalModelID, VisualProvider: SceneProviderLocal}
	if coverage != want {
		t.Fatalf("覆盖率不对(%s):\n got %+v\nwant %+v", dbtest.Backend(), coverage, want)
	}
	// 失效视图也不让失效视频进来：检索范围只含活跃非失效视频。
	staleView, err := service.GetCoverage(context.Background(), &LibraryFilter{SmartView: LibraryViewStale})
	if err != nil || staleView.TotalVideos != 0 {
		t.Fatalf("失效视图下范围应为空: %+v %v", staleView, err)
	}
	scoped, err := service.GetCoverage(context.Background(), &LibraryFilter{PathPrefix: indexed.Directory})
	if err != nil || scoped.TotalVideos != 1 || scoped.SubtitleIndexed != 1 || scoped.VisualIndexed != 0 {
		t.Fatalf("按路径筛选后的覆盖率不对: %+v %v", scoped, err)
	}
}

// 外部描述：只对已存的 caption 做字面匹配，检索从不调用外部接口（只读配置里的模型名）。
func TestSceneExternalCaptionSearchIsLiteralAndOffline(t *testing.T) {
	setupSceneDB(t, SceneProviderExternal)
	video := seedSceneVideo(t, "beach.mp4", []byte("b"), 60)
	seedSceneIndex(t, video, sceneExternalModelID("vision-x"), 5000, []sceneSegmentFixture{
		{StartMS: 0, EndMS: 5000, Caption: "城市夜景车流"},
		{StartMS: 5000, EndMS: 15000, Caption: "海边日落两人散步"},
	})
	other := seedSceneVideo(t, "old.mp4", []byte("o"), 60)
	seedSceneIndex(t, other, sceneExternalModelID("vision-old"), 5000, []sceneSegmentFixture{{StartMS: 0, EndMS: 5000, Caption: "海边日落"}})
	requests := configureSceneExternalEndpoint(t, "vision-x")
	vectors := &fakeSceneVectors{texts: map[string][]float32{}}
	result, err := newTestSceneSearch(vectors, false).Search(context.Background(), SceneSearchRequest{Query: "海边日落", Mode: SceneModeVisual})
	if err != nil {
		t.Fatalf("检索失败(%s): %v", dbtest.Backend(), err)
	}
	if len(result.Hits) != 1 || result.Hits[0].VideoID != video.ID || result.Hits[0].StartMS != 5000 || result.Hits[0].Text != "海边日落两人散步" {
		t.Fatalf("应只命中当前外部模型的描述: %+v", result.Hits)
	}
	if got := requests(); got != 0 || vectors.textCalls != 0 {
		t.Fatalf("检索不该调用外部接口或本地 worker: external=%d worker=%d", got, vectors.textCalls)
	}
	if result.Coverage.VisualProvider != SceneProviderExternal || result.Coverage.VisualIndexed != 1 {
		t.Fatalf("覆盖率应按外部模型计数: %+v", result.Coverage)
	}
}
