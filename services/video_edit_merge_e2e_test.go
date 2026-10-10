package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

// TC-20 / TC-22：三个来源顺序合并（精确模式）。成品时长与轨道数、旁挂 srt 按累计偏移拼接、
// 默认名被文件 / 同名 .srt / 库记录占用时依次顺延，原片字节不变。
func TestVideoEditMergeThreeSourcesEndToEnd(t *testing.T) {
	ffmpeg, ffprobe := requireEditFFmpeg(t)
	service := newRealEditService(t)
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "第一集.mkv"), filepath.Join(dir, "第二集.mkv"), filepath.Join(dir, "第三集.mkv")}
	seconds := []float64{2, 3, 2}
	videos := []models.Video{}
	hashes := []string{}
	for index, path := range paths {
		makeEditFixture(t, ffmpeg, path, editFixture{width: 320, height: 180, seconds: seconds[index]})
		videos = append(videos, insertEditVideo(t, path))
	}
	writeEditTestFile(t, filepath.Join(dir, "第一集.srt"), "1\n00:00:00,500 --> 00:00:01,000\n第一集台词\n")
	writeEditTestFile(t, filepath.Join(dir, "第三集.srt"), "1\n00:00:00,500 --> 00:00:01,000\n第三集台词\n\n2\n00:00:01,500 --> 00:00:01,900\n第三集结尾\n")
	for _, path := range paths {
		hashes = append(hashes, sha256OfFile(t, path))
	}
	// 默认名、(2) 的 .srt、(3) 的库记录都被占用 → 应落到 (4)。
	writeEditTestFile(t, filepath.Join(dir, "第一集 (合并).mkv"), "occupied")
	writeEditTestFile(t, filepath.Join(dir, "第一集 (合并) (2).srt"), "1\n00:00:00,000 --> 00:00:01,000\n占位\n")
	if err := database.DB.Create(&models.Video{Name: "第一集 (合并) (3).mkv", Path: filepath.Join(dir, "第一集 (合并) (3).mkv"), Directory: dir}).Error; err != nil {
		t.Fatal(err)
	}

	view, err := service.CreateProject(context.Background(), EditProjectCreateRequest{Kind: models.VideoEditKindMerge,
		VideoIDs: []uint{videos[0].ID, videos[1].ID, videos[2].ID}})
	if err != nil {
		t.Fatal(err)
	}
	pre, err := service.PreflightProject(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pre.Outputs) != 1 || pre.Outputs[0].PlannedName != "第一集 (合并) (4).mkv" || pre.Outputs[0].Container != "mkv" {
		t.Fatalf("预检输出不对: %+v", pre.Outputs)
	}
	if got := len(pre.Outputs[0].AudioTracks); got != 2 || len(pre.Outputs[0].SubtitleTracks) != 1 {
		t.Fatalf("输出轨道数不对: audio=%d subtitle=%d", got, len(pre.Outputs[0].SubtitleTracks))
	}
	queueAllAcknowledged(t, service, view.ID)
	done := waitEditProject(t, service, view.ID, 3*time.Minute, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
	if done.Status != models.VideoEditStatusCompleted {
		t.Fatalf("合并应完成: %+v", done.Items)
	}
	output, outputPath := outputPathOf(t, done, 1)
	if filepath.Base(outputPath) != "第一集 (合并) (4).mkv" || output.Name != "第一集 (合并) (4).mkv" {
		t.Fatalf("成品名应顺延到 (4)，实际 %s", outputPath)
	}
	probed := probeOutputFile(t, ffprobe, outputPath)
	if diff := probed.durationMS - 7000; diff > 300 || diff < -300 {
		t.Fatalf("成品时长应约 7 秒，实际 %d ms", probed.durationMS)
	}
	if done.Items[0].ErrorMessage != "" {
		t.Fatalf("完成项不应带警告: %q", done.Items[0].ErrorMessage)
	}
	if len(probed.audio) != 2 || probed.audio[0] != "jpn" || probed.audio[1] != "eng" || probed.subtitles != 1 {
		t.Fatalf("成品轨道不对: %+v", probed)
	}
	// 第三集的起点 = 前两段按 25fps 量化到整帧后的时长之和（与成品画面时间线一致）。
	offset := int64(0)
	for _, path := range paths[:2] {
		offset += editQuantizeMS(probeOutputFile(t, ffprobe, path).durationMS, "25/1")
	}
	srt := readFileString(t, strings.TrimSuffix(outputPath, ".mkv")+".srt")
	for _, want := range []string{"00:00:00,500 --> 00:00:01,000\n第一集台词",
		formatEditSRTTime(offset+500) + " --> " + formatEditSRTTime(offset+1000) + "\n第三集台词",
		formatEditSRTTime(offset+1500) + " --> " + formatEditSRTTime(offset+1900) + "\n第三集结尾"} {
		if !strings.Contains(srt, want) {
			t.Fatalf("旁挂字幕缺少 %q:\n%s", want, srt)
		}
	}
	if readFileString(t, filepath.Join(dir, "第一集 (合并).mkv")) != "occupied" {
		t.Fatal("被占用的默认名文件不能被覆盖")
	}
	for index, path := range paths {
		if sha256OfFile(t, path) != hashes[index] {
			t.Fatalf("原片 %s 字节被改动", filepath.Base(path))
		}
	}
	if entries, _ := os.ReadDir(dir); hasEditWorkdir(entries) {
		t.Fatal("完成后工作目录应被删除")
	}
	if output.Duration <= 0 {
		t.Fatalf("成品入库后应刷新技术快照（时长），实际 %+v", output)
	}
}

func hasEditWorkdir(entries []os.DirEntry) bool {
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), editWorkdirPrefix) {
			return true
		}
	}
	return false
}

// mkv 字体附件从全部来源按文件名去重后附加到成品（同名字体只留第一个来源的那份）。
func TestVideoEditMergeDedupesFontAttachments(t *testing.T) {
	ffmpeg, ffprobe := requireEditFFmpeg(t)
	service := newRealEditService(t)
	dir, fonts := t.TempDir(), t.TempDir()
	commonA, commonB := filepath.Join(fonts, "a", "common.ttf"), filepath.Join(fonts, "b", "common.ttf")
	only := filepath.Join(fonts, "a", "only.ttf")
	for path, content := range map[string]string{commonA: "font-a", commonB: "font-b", only: "font-only"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeEditTestFile(t, path, content)
	}
	first, second := filepath.Join(dir, "上.mkv"), filepath.Join(dir, "下.mkv")
	makeEditFixture(t, ffmpeg, first, editFixture{width: 320, height: 180, seconds: 1, fonts: []string{commonA, only}})
	makeEditFixture(t, ffmpeg, second, editFixture{width: 320, height: 180, seconds: 1, fonts: []string{commonB}})
	a, b := insertEditVideo(t, first), insertEditVideo(t, second)
	view, err := service.CreateProject(context.Background(), EditProjectCreateRequest{Kind: models.VideoEditKindMerge, VideoIDs: []uint{a.ID, b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	pre, err := service.PreflightProject(context.Background(), view.ID)
	if err != nil || pre.Outputs[0].Attachments != 2 || pre.Sources[0].FontAttachments != 2 || pre.Sources[1].FontAttachments != 1 {
		t.Fatalf("预检应列出去重后的 2 个字体附件: %v %+v %+v", err, pre.Outputs, pre.Sources)
	}
	queueAllAcknowledged(t, service, view.ID)
	done := waitEditProject(t, service, view.ID, 3*time.Minute, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
	if done.Status != models.VideoEditStatusCompleted {
		t.Fatalf("应完成: %+v", done.Items)
	}
	_, output := outputPathOf(t, done, 1)
	listing := runFixtureCommand(t, ffprobe, "-v", "error", "-select_streams", "t", "-show_entries", "stream_tags=filename", "-of", "csv=p=0", output)
	names := strings.Fields(listing)
	if len(names) != 2 || names[0] != "common.ttf" || names[1] != "only.ttf" {
		t.Fatalf("成品附件应为去重后的 common.ttf 与 only.ttf: %q", listing)
	}
}
