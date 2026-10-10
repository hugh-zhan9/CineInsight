package services

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"video-master/models"
)

const trimSidecarFixture = "1\n00:00:00,500 --> 00:00:01,500\n片头前\n\n2\n00:00:02,500 --> 00:00:03,500\n片头中\n\n" +
	"3\n00:00:03,800 --> 00:00:04,600\n跨切点\n\n4\n00:00:05,000 --> 00:00:05,500\n片头后\n"

func setTrimRecipe(t *testing.T, service *VideoEditService, view *EditProjectView, mode string, items ...EditTrimItem) *EditProjectView {
	t.Helper()
	recipe := view.Recipe
	recipe.TrimIntro.Items = items
	updated, err := service.UpdateRecipe(context.Background(), EditRecipeUpdateRequest{ProjectID: view.ID, ExpectedRevision: view.Revision, Recipe: recipe, Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

// TC-20：精确模式去片头。移除区间恰好消失（时长、切点后首帧对应原片 remove_end），旁挂字幕截断平移，原片不变。
func TestVideoEditTrimIntroPreciseEndToEnd(t *testing.T) {
	ffmpeg, ffprobe := requireEditFFmpeg(t)
	service := newRealEditService(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "剧集.mkv")
	makeEditFixture(t, ffmpeg, source, editFixture{width: 320, height: 180, seconds: 6})
	writeEditTestFile(t, filepath.Join(dir, "剧集.srt"), trimSidecarFixture)
	hash := sha256OfFile(t, source)
	video := insertEditVideo(t, source)
	view, err := service.CreateProject(context.Background(), EditProjectCreateRequest{Kind: models.VideoEditKindTrimIntro, VideoIDs: []uint{video.ID}})
	if err != nil {
		t.Fatal(err)
	}
	setTrimRecipe(t, service, view, "", EditTrimItem{VideoID: video.ID, RemoveStartMS: 2000, RemoveEndMS: 4000, Origin: EditOriginManual, Confirmed: true})
	queueAllAcknowledged(t, service, view.ID)
	done := waitEditProject(t, service, view.ID, 3*time.Minute, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
	if done.Status != models.VideoEditStatusCompleted {
		t.Fatalf("去片头应完成: %+v", done.Items)
	}
	_, output := outputPathOf(t, done, 1)
	if filepath.Base(output) != "剧集 (去片头).mkv" {
		t.Fatalf("成品名不对: %s", output)
	}
	sourceDuration := probeOutputFile(t, ffprobe, source).durationMS
	want := editQuantizeMS(2000, "25/1") + editQuantizeMS(sourceDuration-4000, "25/1")
	probed := probeOutputFile(t, ffprobe, output)
	if diff := probed.durationMS - want; diff > 100 || diff < -100 {
		t.Fatalf("成品时长应约 %d ms，实际 %d", want, probed.durationMS)
	}
	if len(probed.audio) != 2 || probed.subtitles != 1 {
		t.Fatalf("去片头应保留全部音轨与字幕: %+v", probed)
	}
	after := grayFrame(t, ffmpeg, output, 2.02)
	matched, removed := meanAbsDiff(after, grayFrame(t, ffmpeg, source, 4.02)), meanAbsDiff(after, grayFrame(t, ffmpeg, source, 2.02))
	if matched > 8 || matched >= removed {
		t.Fatalf("切点后首帧应对应原片 4.0 秒（差 %.2f），而不是 2.0 秒（差 %.2f）", matched, removed)
	}
	before := meanAbsDiff(grayFrame(t, ffmpeg, output, 1.02), grayFrame(t, ffmpeg, source, 1.02))
	if before > 8 {
		t.Fatalf("切点前画面应与原片一致，差 %.2f", before)
	}
	srt := readFileString(t, strings.TrimSuffix(output, ".mkv")+".srt")
	for _, want := range []string{"00:00:00,500 --> 00:00:01,500\n片头前", "00:00:02,000 --> 00:00:02,600\n跨切点", "00:00:03,000 --> 00:00:03,500\n片头后"} {
		if !strings.Contains(srt, want) {
			t.Fatalf("旁挂字幕缺少 %q:\n%s", want, srt)
		}
	}
	if strings.Contains(srt, "片头中") {
		t.Fatalf("移除区间内的字幕应被删掉:\n%s", srt)
	}
	if sha256OfFile(t, source) != hash {
		t.Fatal("原片字节被改动")
	}
}

// Q8：快速模式切点前移到不晚于请求时间的关键帧（GOP 1 秒，请求 4.3 秒 → 实际 4.0 秒），预检先给出实际时间，
// 成品与旁挂字幕都按实际切点。
func TestVideoEditTrimIntroFastSnapsToKeyframe(t *testing.T) {
	ffmpeg, ffprobe := requireEditFFmpeg(t)
	service := newRealEditService(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "快速.mkv")
	makeEditFixture(t, ffmpeg, source, editFixture{width: 320, height: 180, seconds: 6, gop: 25})
	writeEditTestFile(t, filepath.Join(dir, "快速.srt"), trimSidecarFixture)
	hash := sha256OfFile(t, source)
	video := insertEditVideo(t, source)
	view, err := service.CreateProject(context.Background(), EditProjectCreateRequest{Kind: models.VideoEditKindTrimIntro, VideoIDs: []uint{video.ID}})
	if err != nil {
		t.Fatal(err)
	}
	view = setTrimRecipe(t, service, view, models.VideoEditModeFast,
		EditTrimItem{VideoID: video.ID, RemoveStartMS: 2000, RemoveEndMS: 4300, Origin: EditOriginManual, Confirmed: true})
	if view.Mode != models.VideoEditModeFast {
		t.Fatalf("模式应改为 fast: %s", view.Mode)
	}
	pre, err := service.PreflightProject(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !pre.Fast.Available || len(pre.Fast.CutPoints) != 1 || pre.Fast.CutPoints[0].RequestedMS != 4300 || pre.Fast.CutPoints[0].ActualMS != 4000 {
		t.Fatalf("快速模式切点应前移到 4000: %+v", pre.Fast)
	}
	if len(pre.Errors) != 0 || len(pre.Warnings) == 0 || !strings.HasPrefix(pre.Warnings[len(pre.Warnings)-1].Key, "fast_cut_points:") {
		t.Fatalf("快速模式应只给出切点警告: errors=%+v warnings=%+v", pre.Errors, pre.Warnings)
	}
	unacked, err := service.QueueProject(context.Background(), EditQueueRequest{ProjectID: view.ID, ExpectedRevision: view.Revision})
	if err != nil || unacked.Queued || len(unacked.Unacknowledged) == 0 {
		t.Fatalf("未确认切点警告时不能排队: %+v %v", unacked, err)
	}
	queueAllAcknowledged(t, service, view.ID)
	done := waitEditProject(t, service, view.ID, 3*time.Minute, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
	if done.Status != models.VideoEditStatusCompleted {
		t.Fatalf("快速去片头应完成: %+v", done.Items)
	}
	_, output := outputPathOf(t, done, 1)
	sourceDuration := probeOutputFile(t, ffprobe, source).durationMS
	probed := probeOutputFile(t, ffprobe, output)
	if want := 2000 + sourceDuration - 4000; probed.durationMS-want > 300 || want-probed.durationMS > 300 {
		t.Fatalf("快速模式成品时长应约 %d ms（按关键帧 4.0 秒切），实际 %d", want, probed.durationMS)
	}
	srt := readFileString(t, strings.TrimSuffix(output, ".mkv")+".srt")
	if !strings.Contains(srt, "00:00:02,000 --> 00:00:02,600\n跨切点") {
		t.Fatalf("旁挂字幕应按实际关键帧切点平移:\n%s", srt)
	}
	if sha256OfFile(t, source) != hash {
		t.Fatal("原片字节被改动")
	}
}

// Q8：来源编码参数不同（分辨率不同需要缩放）时快速模式不可用，给出原因并阻止导出，不静默改成精确模式。
func TestVideoEditFastModeRefusedWhenParametersDiffer(t *testing.T) {
	ffmpeg, _ := requireEditFFmpeg(t)
	service := newRealEditService(t)
	dir := t.TempDir()
	small, large := filepath.Join(dir, "小.mkv"), filepath.Join(dir, "大.mkv")
	makeEditFixture(t, ffmpeg, small, editFixture{width: 320, height: 180, seconds: 2})
	makeEditFixture(t, ffmpeg, large, editFixture{width: 640, height: 360, seconds: 2})
	a, b := insertEditVideo(t, small), insertEditVideo(t, large)
	view, err := service.CreateProject(context.Background(), EditProjectCreateRequest{Kind: models.VideoEditKindMerge, VideoIDs: []uint{a.ID, b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	recipe := view.Recipe
	recipe.Merge.SpecSourceVideoID = b.ID
	view, err = service.UpdateRecipe(context.Background(), EditRecipeUpdateRequest{ProjectID: view.ID, ExpectedRevision: view.Revision, Recipe: recipe, Mode: models.VideoEditModeFast})
	if err != nil {
		t.Fatal(err)
	}
	pre, err := service.PreflightProject(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pre.Fast.Available || len(pre.Fast.Reasons) == 0 || !editIssueCodes(pre.Errors)["fast_unavailable"] {
		t.Fatalf("参数不同时快速模式应不可用并阻止导出: fast=%+v errors=%+v", pre.Fast, pre.Errors)
	}
	if len(pre.SpecOptions) != 2 {
		t.Fatalf("分辨率不同应给出规格选项: %+v", pre.SpecOptions)
	}
	result, err := service.QueueProject(context.Background(), EditQueueRequest{ProjectID: view.ID, ExpectedRevision: view.Revision})
	if err != nil || result.Queued {
		t.Fatalf("快速模式不可用时不能排队: %+v %v", result, err)
	}
}

func editIssueCodes(issues []EditIssue) map[string]bool {
	codes := map[string]bool{}
	for _, issue := range issues {
		codes[issue.Code] = true
	}
	return codes
}

// 容器规则：来源都是 mp4、映射后没有字幕流/附件且只有一条音轨时保持 mp4。
func TestVideoEditTrimKeepsMP4Container(t *testing.T) {
	ffmpeg, ffprobe := requireEditFFmpeg(t)
	service := newRealEditService(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "短片.mp4")
	makeEditFixture(t, ffmpeg, source, editFixture{width: 320, height: 180, seconds: 4, singleAudio: true, noSubtitle: true})
	video := insertEditVideo(t, source)
	view, err := service.CreateProject(context.Background(), EditProjectCreateRequest{Kind: models.VideoEditKindTrimIntro, VideoIDs: []uint{video.ID}})
	if err != nil {
		t.Fatal(err)
	}
	setTrimRecipe(t, service, view, "", EditTrimItem{VideoID: video.ID, RemoveStartMS: 0, RemoveEndMS: 1000, Origin: EditOriginUniform, Confirmed: true})
	queueAllAcknowledged(t, service, view.ID)
	done := waitEditProject(t, service, view.ID, 3*time.Minute, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
	if done.Status != models.VideoEditStatusCompleted {
		t.Fatalf("应完成: %+v", done.Items)
	}
	_, output := outputPathOf(t, done, 1)
	probed := probeOutputFile(t, ffprobe, output)
	if filepath.Ext(output) != ".mp4" || len(probed.audio) != 1 || probed.durationMS < 2800 || probed.durationMS > 3200 {
		t.Fatalf("应输出约 3 秒的 mp4: %s %+v", output, probed)
	}
}
