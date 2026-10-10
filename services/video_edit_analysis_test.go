package services

import (
	"context"
	"testing"
	"time"

	"video-master/models"
	"video-master/services/editalign"
)

func fakeEditAnalysisReaders(service *VideoEditService) {
	service.readHashes = func(ctx context.Context, ffmpeg, path string, startMS, durationMS, stepMS int64) (editalign.Sequence, error) {
		return editalign.Sequence{StartMS: startMS, StepMS: stepMS, Hashes: make([]uint64, durationMS/stepMS)}, ctx.Err()
	}
	service.grayReader = func(ffmpeg, path string, side int) editalign.GrayFrameReader {
		return func(context.Context, int64, int64) ([]editalign.GrayFrame, error) { return nil, nil }
	}
}

// Q1/Q2：片头识别结果以 confirmed=false 写回未确认项（detected 写区间，undetected 只标状态），
// 已确认项不被覆盖；读取窗口按配方（默认 10 分钟，短片取全长）。
func TestVideoEditAnalyzeIntrosWritesUnconfirmedItems(t *testing.T) {
	service, media := newFakeEditService(t)
	fakeEditAnalysisReaders(service)
	dir := t.TempDir()
	a, b, c := media.addFakeSource(t, dir, "e1.mkv", 1_500_000), media.addFakeSource(t, dir, "e2.mkv", 1_500_000), media.addFakeSource(t, dir, "e3.mkv", 300_000)
	var windows []int64
	service.readHashes = func(ctx context.Context, ffmpeg, path string, startMS, durationMS, stepMS int64) (editalign.Sequence, error) {
		windows = append(windows, durationMS)
		return editalign.Sequence{StepMS: stepMS}, nil
	}
	var gotSources []editalign.Source
	service.detectIntros = func(ctx context.Context, sources []editalign.Source, opts editalign.IntroOptions) ([]editalign.IntroResult, error) {
		gotSources = sources
		return []editalign.IntroResult{
			{SourceID: a.ID, Status: editalign.IntroDetected, StartMS: 1200, EndMS: 91200, MatchRate: 0.93, MatchedWith: 2},
			{SourceID: b.ID, Status: editalign.IntroUndetected},
			{SourceID: c.ID, Status: editalign.IntroDetected, StartMS: 0, EndMS: 90000, MatchRate: 0.9},
		}, nil
	}
	view := mustEditProject(t, service, models.VideoEditKindTrimIntro, a.ID, b.ID, c.ID)
	view = mustUpdateRecipe(t, service, view, func(recipe *EditRecipe) {
		recipe.TrimIntro.Items[2] = EditTrimItem{VideoID: c.ID, RemoveStartMS: 5000, RemoveEndMS: 65000, Origin: EditOriginManual, Confirmed: true}
	})
	started, err := service.AnalyzeProject(context.Background(), EditAnalyzeRequest{ProjectID: view.ID, ExpectedRevision: view.Revision})
	if err != nil || started.Status != models.VideoEditStatusAnalyzing {
		t.Fatalf("应进入 analyzing: %+v %v", started, err)
	}
	if _, err := service.AnalyzeProject(context.Background(), EditAnalyzeRequest{ProjectID: view.ID, ExpectedRevision: view.Revision}); err == nil {
		t.Fatal("同一时间只允许一轮分析")
	}
	done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusDraft)
	items := done.Recipe.TrimIntro.Items
	if items[0].RemoveStartMS != 1200 || items[0].RemoveEndMS != 91200 || items[0].Origin != EditOriginDetected ||
		items[0].Confirmed || items[0].DetectStatus != EditDetectDetected || items[0].Confidence != 0.93 {
		t.Fatalf("识别出的项应写区间且未确认: %+v", items[0])
	}
	if items[1].DetectStatus != EditDetectUndetected || items[1].Confirmed {
		t.Fatalf("未识别项应标 undetected: %+v", items[1])
	}
	if items[2].RemoveStartMS != 5000 || items[2].RemoveEndMS != 65000 || !items[2].Confirmed {
		t.Fatalf("已确认项不能被分析覆盖: %+v", items[2])
	}
	if done.Revision != view.Revision+1 || done.Analysis == nil || done.Analysis.Status != "completed" || len(done.Analysis.Intros) != 3 || done.Analysis.Intros[2].Applied {
		t.Fatalf("分析结果应写入 analysis 并 revision+1: %+v", done.Analysis)
	}
	if len(gotSources) != 3 || len(windows) != 3 || windows[0] != videoEditDefaultIntroWindowMS || windows[2] != 300_000 {
		t.Fatalf("应按默认窗口读取前 10 分钟（短片取全长）: %v", windows)
	}
}

// 分析取消：回到 draft，配方与 revision 不变，分析状态记 cancelled。
func TestVideoEditAnalysisCancelKeepsRecipe(t *testing.T) {
	service, media := newFakeEditService(t)
	fakeEditAnalysisReaders(service)
	dir := t.TempDir()
	long, hd := media.addFakeSource(t, dir, "long.mkv", 60000), media.addFakeSource(t, dir, "hd.mkv", 30000)
	entered := make(chan struct{})
	service.alignSegments = func(ctx context.Context, long, hd editalign.Source, opts editalign.AlignOptions, progress func(int, int)) ([]editalign.AlignedSegment, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	view := mustEditProject(t, service, models.VideoEditKindHDReplace, long.ID, hd.ID)
	confirmed := EditHDSegment{LongStartMS: 1000, LongEndMS: 5000, HDStartMS: 0, HDEndMS: 4000, Origin: EditOriginManual, Confirmed: true}
	view = mustUpdateRecipe(t, service, view, func(recipe *EditRecipe) { recipe.HDReplace.Segments = []EditHDSegment{confirmed} })
	if _, err := service.AnalyzeProject(context.Background(), EditAnalyzeRequest{ProjectID: view.ID, ExpectedRevision: view.Revision}); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := service.CancelAnalysis(context.Background(), view.ID); err != nil {
		t.Fatal(err)
	}
	current, _ := service.GetProject(context.Background(), view.ID)
	if current.Status != models.VideoEditStatusDraft || current.Revision != view.Revision || current.Analysis.Status != "cancelled" ||
		len(current.Recipe.HDReplace.Segments) != 1 || current.Recipe.HDReplace.Segments[0] != confirmed {
		t.Fatalf("取消分析不能改配方: %+v analysis=%+v", current.Recipe.HDReplace, current.Analysis)
	}
}

// 高清对齐成功：未确认段整体换成新结果（confirmed=false、默认长版音频），与已确认段重叠的结果不写入。
func TestVideoEditAnalyzeAlignmentKeepsConfirmedSegments(t *testing.T) {
	service, media := newFakeEditService(t)
	fakeEditAnalysisReaders(service)
	dir := t.TempDir()
	long, hd := media.addFakeSource(t, dir, "long.mkv", 60000), media.addFakeSource(t, dir, "hd.mkv", 40000)
	service.alignSegments = func(ctx context.Context, long, hd editalign.Source, opts editalign.AlignOptions, progress func(int, int)) ([]editalign.AlignedSegment, error) {
		progress(1, 2)
		return []editalign.AlignedSegment{
			{LongStartMS: 2000, LongEndMS: 6000, HDStartMS: 0, HDEndMS: 4000, MatchRate: 0.97, Status: editalign.SegmentMatched},
			{LongStartMS: 20000, LongEndMS: 30000, HDStartMS: 10000, HDEndMS: 20000, MatchRate: 0.88, Status: editalign.SegmentMatched},
			{LongStartMS: 40000, LongEndMS: 45000, HDStartMS: 30000, HDEndMS: 35000, MatchRate: 0.6, Status: editalign.SegmentConflict},
		}, nil
	}
	view := mustEditProject(t, service, models.VideoEditKindHDReplace, long.ID, hd.ID)
	confirmed := EditHDSegment{LongStartMS: 1000, LongEndMS: 5000, HDStartMS: 0, HDEndMS: 4000, Origin: EditOriginManual, Confirmed: true}
	stale := EditHDSegment{LongStartMS: 50000, LongEndMS: 52000, HDStartMS: 38000, HDEndMS: 40000, Origin: EditOriginDetected}
	view = mustUpdateRecipe(t, service, view, func(recipe *EditRecipe) { recipe.HDReplace.Segments = []EditHDSegment{stale, confirmed} })
	if _, err := service.AnalyzeProject(context.Background(), EditAnalyzeRequest{ProjectID: view.ID, ExpectedRevision: view.Revision}); err != nil {
		t.Fatal(err)
	}
	done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusDraft)
	segments := done.Recipe.HDReplace.Segments
	if len(segments) != 3 || segments[0] != confirmed || segments[1].LongStartMS != 20000 || segments[1].Confirmed ||
		segments[1].AudioSource != EditAudioSourceLong || segments[2].Status != "conflict" {
		t.Fatalf("对齐结果合并不对: %+v", segments)
	}
	if done.Analysis.Segments[0].Applied || !done.Analysis.Segments[1].Applied {
		t.Fatalf("与已确认段重叠的结果应标未写入: %+v", done.Analysis.Segments)
	}
}
