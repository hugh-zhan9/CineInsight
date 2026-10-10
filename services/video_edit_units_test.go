package services

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"
)

// 旁挂字幕重写：区间外的条目丢弃，跨边界的截断，余下按片段在成品里的起点平移。
func TestRetimeEditCuesTruncatesAndShifts(t *testing.T) {
	cues := []subtitleparser.Segment{
		{StartTimeMs: 500, EndTimeMs: 1500, Lines: []string{"之前"}},
		{StartTimeMs: 1800, EndTimeMs: 2600, Lines: []string{"跨入"}},
		{StartTimeMs: 3000, EndTimeMs: 3500, Lines: []string{"区间内", ""}},
		{StartTimeMs: 4800, EndTimeMs: 5600, Lines: []string{"跨出"}},
		{StartTimeMs: 6000, EndTimeMs: 7000, Lines: []string{"之后"}},
	}
	got := retimeEditCues(cues, 2000, 3000, 10000)
	want := []editCue{
		{StartMS: 10000, EndMS: 10600, Lines: []string{"跨入"}},
		{StartMS: 11000, EndMS: 11500, Lines: []string{"区间内"}},
		{StartMS: 12800, EndMS: 13000, Lines: []string{"跨出"}},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("重写结果不对:\n got %v\nwant %v", got, want)
	}
	if text := string(formatEditSRT(got)); !strings.HasPrefix(text, "1\n00:00:10,000 --> 00:00:10,600\n跨入\n\n2\n") {
		t.Fatalf("SRT 序列化不对:\n%s", text)
	}
}

// 文件名不覆盖：默认名被文件、同名 .srt 或库记录（含回收站）占用时依次顺延；(2)…(99) 都占用时报不可用。
func TestFirstFreeEditOutputName(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	writeEditTestFile(t, filepath.Join(dir, "片 (合并).mkv"), "x")
	writeEditTestFile(t, filepath.Join(dir, "片 (合并) (2).srt"), "x")
	trashed := models.Video{Name: "片 (合并) (3).mkv", Path: filepath.Join(dir, "片 (合并) (3).mkv"), Directory: dir}
	if err := database.DB.Create(&trashed).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&trashed).Error; err != nil {
		t.Fatal(err)
	}
	name, free, err := firstFreeEditOutputName(context.Background(), dir, "片 (合并)", ".mkv")
	if err != nil || !free || name != "片 (合并) (4).mkv" {
		t.Fatalf("应顺延到 (4)，实际 %q free=%v err=%v", name, free, err)
	}
	for attempt := 4; attempt <= editMaxNameAttempts; attempt++ {
		writeEditTestFile(t, filepath.Join(dir, editCandidateName("片 (合并)", ".mkv", attempt)), "x")
	}
	if _, free, err := firstFreeEditOutputName(context.Background(), dir, "片 (合并)", ".mkv"); err != nil || free {
		t.Fatalf("99 个名字都占用时应不可用: free=%v err=%v", free, err)
	}
}

func TestSanitizeEditBaseNameAndEncoders(t *testing.T) {
	cases := map[string]string{"a/b:c": "a_b_c", "  ..隐藏  ": "隐藏", "": "视频", "line\nbreak": "line break"}
	for input, want := range cases {
		if got := sanitizeEditBaseName(input); got != want {
			t.Fatalf("sanitize(%q) = %q, want %q", input, got, want)
		}
	}
	encoders := parseEditEncoders("Encoders:\n V..... = Video\n ------\n V....D libx264              libx264 H.264\n A....D aac                  AAC\n")
	if !encoders["libx264"] || !encoders["aac"] || encoders["libx265"] {
		t.Fatalf("编码器解析不对: %v", encoders)
	}
	if editVideoEncoderName("darwin", true) != "hevc_videotoolbox" || editVideoEncoderName("linux", false) != "libx264" {
		t.Fatal("编码器选择不对")
	}
	if editQuantizeMS(2021, "25/1") != 2040 || editQuantizeMS(1001, "24000/1001") != 1001 {
		t.Fatalf("整帧量化不对: %d %d", editQuantizeMS(2021, "25/1"), editQuantizeMS(1001, "24000/1001"))
	}
}

// 编码器缺失报 encoder_unavailable，不换用其他编码。
func TestVideoEditPreflightEncoderUnavailable(t *testing.T) {
	service, media := newFakeEditService(t)
	service.listEncoders = func(context.Context, string) (map[string]bool, error) { return map[string]bool{"aac": true}, nil }
	dir := t.TempDir()
	a, b := media.addFakeSource(t, dir, "a.mkv", 3000), media.addFakeSource(t, dir, "b.mkv", 3000)
	view := mustEditProject(t, service, models.VideoEditKindMerge, a.ID, b.ID)
	pre, err := service.PreflightProject(context.Background(), view.ID)
	if err != nil || !editIssueCodes(pre.Errors)["encoder_unavailable"] {
		t.Fatalf("应报 encoder_unavailable: %v %+v", err, pre.Errors)
	}
}
