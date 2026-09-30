package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
	"video-master/services"
)

// P-032 复审 m2：App.MergeMediaMetadata 必须把 options 原样交给服务层。截取片段组传
// {skip_playback_state, skip_subtitle} 都为 true 时，保留项的已看、断点与字幕都不动；
// 同一对视频改用零值选项再合并一次，已看与断点随之合并——证明前一次没动是选项生效，而不是夹具本来就合并不了。
func TestMergeMediaMetadataAppPassesOptionsToServiceIMG03(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	app := &App{
		videoService:    services.NewVideoService(services.NewMediaProbeService()),
		subtitleService: services.NewSubtitleService(t.TempDir()),
	}

	keeper := models.Video{Name: "full.mp4", Path: filepath.Join(root, "full.mp4"), Directory: root, Duration: 600, WatchPositionSeconds: 10}
	watchedAt := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	source := models.Video{Name: "clip.mp4", Path: filepath.Join(root, "clip.mp4"), Directory: root, Duration: 600,
		IsWatched: true, WatchedAt: &watchedAt, WatchPositionSeconds: 100}
	for _, video := range []*models.Video{&keeper, &source} {
		if err := os.WriteFile(video.Path, []byte(video.Name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatalf("创建视频失败: %v", err)
		}
	}
	sourceSubtitle := filepath.Join(root, "clip.srt")
	if err := os.WriteFile(sourceSubtitle, []byte("1\n00:00:01,000 --> 00:00:02,000\n片段字幕\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keeperSubtitle := filepath.Join(root, "full.srt")
	reload := func() models.Video {
		t.Helper()
		var video models.Video
		if err := database.DB.First(&video, keeper.ID).Error; err != nil {
			t.Fatalf("读回保留项失败: %v", err)
		}
		return video
	}

	result, err := app.MergeMediaMetadata(services.MediaMergeKindVideo, keeper.ID, []uint{source.ID},
		services.MediaMetadataMergeOptions{SkipPlaybackState: true, SkipSubtitle: true})
	if err != nil {
		t.Fatalf("App 合并失败: %v", err)
	}
	if result.WatchedChanged || result.ProgressChanged || result.SubtitleMoved {
		t.Fatalf("跳过观看状态与字幕时结果不应报告这几项变化: %+v", result)
	}
	if merged := reload(); merged.IsWatched || merged.WatchedAt != nil || merged.WatchPositionSeconds != 10 {
		t.Fatalf("保留项的已看与断点应保持不变: watched=%v at=%v position=%v", merged.IsWatched, merged.WatchedAt, merged.WatchPositionSeconds)
	}
	if _, err := os.Stat(keeperSubtitle); !os.IsNotExist(err) {
		t.Fatalf("跳过字幕时不应给保留项写同名 .srt: %v", err)
	}

	result, err = app.MergeMediaMetadata(services.MediaMergeKindVideo, keeper.ID, []uint{source.ID}, services.MediaMetadataMergeOptions{})
	if err != nil {
		t.Fatalf("App 合并失败: %v", err)
	}
	if !result.WatchedChanged || !result.ProgressChanged || !result.SubtitleMoved {
		t.Fatalf("零值选项应合并已看、断点与字幕: %+v", result)
	}
	if merged := reload(); !merged.IsWatched {
		t.Fatalf("零值选项下保留项应变为已看: %+v", merged)
	}
	if _, err := os.Stat(keeperSubtitle); err != nil {
		t.Fatalf("零值选项下字幕应复制成保留项的同名 .srt: %v", err)
	}
}
