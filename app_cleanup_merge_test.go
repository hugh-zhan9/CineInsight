package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
	"video-master/services"

	"gorm.io/gorm"
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

// P-032 复审（修复 R）Minor：数据库合并已提交、之后补已看失败（错误带 merge_committed: 前缀）时，
// 整理项已经变了，App 层同样把清理缓存标为可能过期；其余错误（例如保留项不存在）照旧不动缓存。
func TestMergeMediaMetadataAppInvalidatesCacheOnCommittedErrorIMG03(t *testing.T) {
	setupAppTestDB(t)
	app := &App{
		videoService:   services.NewVideoService(services.NewMediaProbeService()),
		cleanupService: &services.CleanupService{},
	}
	// 空库跑一轮分析，拿到一份已完成、未过期的缓存结果。
	if _, err := app.cleanupService.StartAnalysis(services.CleanupCriteria{}); err != nil {
		t.Fatalf("启动清理分析失败: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for status := app.GetCleanupStatus(); status.Running || !status.Completed; status = app.GetCleanupStatus() {
		if time.Now().After(deadline) {
			t.Fatalf("清理分析未结束: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if app.GetCleanupStatus().Stale {
		t.Fatal("前置：刚算完的结果不应标为过期")
	}

	root := t.TempDir()
	watchedAt := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	keeper := models.Video{Name: "keep.mp4", Path: filepath.Join(root, "keep.mp4"), Directory: root, Duration: 600}
	source := models.Video{Name: "copy.mp4", Path: filepath.Join(root, "copy.mp4"), Directory: root, Duration: 600, IsWatched: true, WatchedAt: &watchedAt}
	for _, video := range []*models.Video{&keeper, &source} {
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatalf("创建视频失败: %v", err)
		}
	}

	// 普通错误：保留项不存在，没有任何写入，缓存不动。
	_, err := app.MergeMediaMetadata(services.MediaMergeKindVideo, keeper.ID+source.ID+100, []uint{source.ID}, services.MediaMetadataMergeOptions{})
	if err == nil || strings.HasPrefix(err.Error(), services.MediaMergeCommittedErrorPrefix) {
		t.Fatalf("保留项不存在时应返回不带前缀的错误: %v", err)
	}
	if app.GetCleanupStatus().Stale {
		t.Fatal("普通合并错误不应把缓存标为过期")
	}

	// 翻转保留项的已看时数据库报错：合并事务已经提交，错误带前缀。
	callback := "test:fail-watched-flip"
	if err := database.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			if _, flips := values["is_watched"]; flips {
				tx.AddError(errors.New("数据库繁忙"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Update().Remove(callback)
	_, err = app.MergeMediaMetadata(services.MediaMergeKindVideo, keeper.ID, []uint{source.ID}, services.MediaMetadataMergeOptions{SkipSubtitle: true})
	if err == nil || !strings.HasPrefix(err.Error(), services.MediaMergeCommittedErrorPrefix) {
		t.Fatalf("补已看失败时应返回带 %q 前缀的错误: %v", services.MediaMergeCommittedErrorPrefix, err)
	}
	if !app.GetCleanupStatus().Stale {
		t.Fatal("合并已提交后，即使补已看失败，缓存也应标为可能过期")
	}
}
