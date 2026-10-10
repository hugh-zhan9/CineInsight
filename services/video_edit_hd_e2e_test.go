package services

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"video-master/models"
)

// TC-21：320×180 长版 + 640×360 高清版（画面取反以便区分、音轨静音）。两段替换：第一段沿用长版音频，
// 第二段切到高清音频；未覆盖部分保留长版画面并放大加黑边；长版旁挂 .srt 按片段重写（切点都在整帧上，
// 结果与原文件一致）；原片不变。
func TestVideoEditHDReplaceEndToEnd(t *testing.T) {
	ffmpeg, ffprobe := requireEditFFmpeg(t)
	service := newRealEditService(t)
	dir := t.TempDir()
	longPath, hdPath := filepath.Join(dir, "长版.mkv"), filepath.Join(dir, "高清.mkv")
	makeEditFixture(t, ffmpeg, longPath, editFixture{width: 320, height: 180, seconds: 10})
	makeEditFixture(t, ffmpeg, hdPath, editFixture{width: 640, height: 360, seconds: 6, offsetSeconds: 2, negate: true, silentAudio: true, noSubtitle: true})
	longSRT := "1\n00:00:01,000 --> 00:00:02,000\n长版字幕\n\n2\n00:00:06,000 --> 00:00:07,000\n替换段字幕\n"
	writeEditTestFile(t, filepath.Join(dir, "长版.srt"), longSRT)
	hashes := []string{sha256OfFile(t, longPath), sha256OfFile(t, hdPath)}
	long, hd := insertEditVideo(t, longPath), insertEditVideo(t, hdPath)
	view, err := service.CreateProject(context.Background(), EditProjectCreateRequest{Kind: models.VideoEditKindHDReplace, VideoIDs: []uint{long.ID, hd.ID}})
	if err != nil {
		t.Fatal(err)
	}
	recipe := view.Recipe
	recipe.HDReplace.Segments = []EditHDSegment{
		{LongStartMS: 5000, LongEndMS: 8000, HDStartMS: 3000, HDEndMS: 6000, AudioSource: EditAudioSourceHD, Origin: EditOriginManual, Confirmed: true},
		{LongStartMS: 2000, LongEndMS: 5000, HDStartMS: 0, HDEndMS: 3000, AudioSource: EditAudioSourceLong, Origin: EditOriginManual, Confirmed: true},
	}
	if _, err := service.UpdateRecipe(context.Background(), EditRecipeUpdateRequest{ProjectID: view.ID, ExpectedRevision: view.Revision, Recipe: recipe}); err != nil {
		t.Fatal(err)
	}
	pre, err := service.PreflightProject(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !editIssueCodes(pre.Warnings)["upscale"] || pre.Outputs[0].Width != 640 || pre.Outputs[0].Sidecar != "retime" {
		t.Fatalf("长版放大须警告确认、输出取高清宽高、长版字幕按片段重写: warnings=%+v outputs=%+v", pre.Warnings, pre.Outputs)
	}
	replaced := 0
	for _, segment := range pre.Outputs[0].Segments {
		if segment.Replaced {
			replaced++
		}
	}
	if len(pre.Outputs[0].Segments) != 4 || replaced != 2 {
		t.Fatalf("时间线应为 长版/高清/高清/长版 四段: %+v", pre.Outputs[0].Segments)
	}
	queueAllAcknowledged(t, service, view.ID)
	done := waitEditProject(t, service, view.ID, 3*time.Minute, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
	if done.Status != models.VideoEditStatusCompleted {
		t.Fatalf("高清替换应完成: %+v", done.Items)
	}
	_, output := outputPathOf(t, done, 1)
	probed := probeOutputFile(t, ffprobe, output)
	if probed.width != 640 || probed.height != 360 || len(probed.audio) != 2 || probed.subtitles != 1 {
		t.Fatalf("成品规格或轨道不对: %+v", probed)
	}
	if want := editQuantizeMS(probeOutputFile(t, ffprobe, longPath).durationMS, "25/1"); probed.durationMS-want > 150 || want-probed.durationMS > 150 {
		t.Fatalf("成品应沿用长版时长 %d ms，实际 %d", want, probed.durationMS)
	}
	hdFrame := grayFrame(t, ffmpeg, output, 3.02)
	if fromHD, fromLong := meanAbsDiff(hdFrame, grayFrame(t, ffmpeg, hdPath, 1.02)), meanAbsDiff(hdFrame, grayFrame(t, ffmpeg, longPath, 3.02)); fromHD > 10 || fromHD >= fromLong {
		t.Fatalf("替换段画面应取自高清版（差 %.2f），而不是长版（差 %.2f）", fromHD, fromLong)
	}
	if kept := meanAbsDiff(grayFrame(t, ffmpeg, output, 1.02), grayFrame(t, ffmpeg, longPath, 1.02)); kept > 10 {
		t.Fatalf("未替换部分应保留长版画面，差 %.2f", kept)
	}
	if longAudio, hdAudio := meanVolume(t, ffmpeg, output, 2.3, 2.4), meanVolume(t, ffmpeg, output, 5.3, 2.4); longAudio < -40 || hdAudio > -60 {
		t.Fatalf("第一段应为长版音频（%.1f dB），第二段应切到静音的高清音频（%.1f dB）", longAudio, hdAudio)
	}
	if got := readFileString(t, strings.TrimSuffix(output, ".mkv")+".srt"); got != longSRT {
		t.Fatalf("切点都在整帧上时，按片段重写的长版字幕应与原文件一致:\n%s", got)
	}
	for index, path := range []string{longPath, hdPath} {
		if sha256OfFile(t, path) != hashes[index] {
			t.Fatalf("原片 %s 字节被改动", filepath.Base(path))
		}
	}
}
