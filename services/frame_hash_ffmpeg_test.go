package services

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 真实 ffmpeg 的端到端 fixture（TC-08 的"真实视频抽帧只做一次人工"那一半）：
// 合成一段 40 秒的 testsrc，再无损截出第 10 秒起的 15 秒，回填两者的帧哈希序列，
// MatchClip 必须认出这一对并把偏移算到 10 秒附近。
//
// 本机没有 ffmpeg 就跳过：这条用例验证的是"我们给 ffmpeg 的参数是对的"，
// 没有 ffmpeg 时它无从谈起，而不是应该失败。
func TestFrameHashRealFFmpegClipFixture(t *testing.T) {
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("本机没有 ffmpeg，跳过真实抽帧 fixture: %v", err)
	}
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	fullPath := filepath.Join(root, "feature.mp4")
	clipPath := filepath.Join(root, "excerpt.mp4")

	// 片源用 mandelbrot 而不是 testsrc：testsrc 缩到 9×8 灰度之后 20 帧只剩 9 个
	// 不同的 dHash，帧间汉明距离普遍小于容差，任何偏移都"命中"，偏移断言就成了
	// 一句空话（实测：testsrc 在偏移 0 处命中率也是 1.000）。mandelbrot 逐帧缩放，
	// 20 帧 20 个不同的哈希，才撑得起"对齐到第 10 秒"这条断言。
	//
	// -g 10（每秒一个关键帧）是为了让下面的 -c copy 能从第 10 秒精确切开：
	// 默认关键帧间隔是 250 帧，在 10 fps 下就是 25 秒，无损截取只能从第 0 秒开始。
	runFFmpeg(t, ffmpegBin, "-v", "error", "-f", "lavfi",
		"-i", "mandelbrot=s=320x240:rate=10", "-t", "40",
		"-c:v", "libx264", "-g", "10", "-pix_fmt", "yuv420p", fullPath)
	runFFmpeg(t, ffmpegBin, "-v", "error", "-ss", "10", "-t", "15", "-i", fullPath, "-c", "copy", clipPath)

	full := registerFFmpegFixtureVideo(t, root, "feature.mp4", fullPath)
	clip := registerFFmpegFixtureVideo(t, root, "excerpt.mp4", clipPath)

	service := NewFrameHashService(NewMediaWorkSlot())
	startedAt := time.Now()
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("启动帧哈希回填失败: %v", err)
	}
	status := waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Completed && !status.Running })
	service.StopAndWait()
	if status.Succeeded != 2 || status.Failed != 0 {
		t.Fatalf("两个 fixture 都该回填成功: %+v", status)
	}
	t.Logf("真实 ffmpeg 回填 2 个 fixture（40s + 15s）耗时 %s", time.Since(startedAt).Round(time.Millisecond))

	fullRow := frameHashSequenceRow(t, full.ID)
	clipRow := frameHashSequenceRow(t, clip.ID)
	if fullRow.IntervalMS != 2000 || clipRow.IntervalMS != 2000 {
		t.Fatalf("采样间隔应为 2000: full=%d clip=%d", fullRow.IntervalMS, clipRow.IntervalMS)
	}
	// 40 秒按 2 秒一帧是 20 帧，15 秒是 7–8 帧（尾帧取决于容器时长）。
	if fullRow.FrameCount < 19 || fullRow.FrameCount > 21 {
		t.Fatalf("40 秒的片应抽出约 20 帧，实际 %d", fullRow.FrameCount)
	}
	if clipRow.FrameCount < 7 || clipRow.FrameCount > 9 {
		t.Fatalf("15 秒的片段应抽出约 8 帧，实际 %d", clipRow.FrameCount)
	}

	fullHashes := decodeFrameHashes(fullRow.Hashes)
	clipHashes := decodeFrameHashes(clipRow.Hashes)
	offset, rate, ok := MatchClip(fullHashes, clipHashes, fullRow.IntervalMS)
	if !ok {
		t.Fatalf("真实截取片段应被识别出来: offset=%d rate=%.3f full=%d clip=%d 帧",
			offset, rate, len(fullHashes), len(clipHashes))
	}
	offsetSeconds := float64(offset) * clipFrameIntervalSeconds(fullRow.IntervalMS)
	if offsetSeconds < 8 || offsetSeconds > 12 {
		t.Fatalf("对齐偏移应落在 10 秒附近，实际 %.1f 秒（offset=%d 帧，rate=%.3f）", offsetSeconds, offset, rate)
	}
	t.Logf("真实截取片段命中：偏移 %.1f 秒，命中率 %.3f", offsetSeconds, rate)
}

func runFFmpeg(t *testing.T, ffmpegBin string, args ...string) {
	t.Helper()
	output, err := exec.Command(ffmpegBin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %v 失败: %v: %s", args, err, string(output))
	}
}

func registerFFmpegFixtureVideo(t *testing.T, root, name, path string) models.Video {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取 fixture 文件失败: %v", err)
	}
	video := models.Video{Name: name, Path: path, Directory: root, Size: info.Size(), Duration: 40}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	return video
}
