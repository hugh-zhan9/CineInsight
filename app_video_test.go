package main

import (
	"testing"

	"video-master/database"
	"video-master/models"
	"video-master/services"
)

// P-020 的 App 绑定：桌面点赞（PLAY-02）、IINA 同步状态（PLAY-14）、带起播来源的进度上报与
// 「继续观看」键集分页（PLAY-09 / PLAY-10）。
func TestVideoWatchAppBindingsPLAY02PLAY14(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2.0}).Error; err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	video := models.Video{Name: "bind.mp4", Path: "/media/bind.mp4", Directory: "/media", Duration: 7200}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}

	liked, err := app.SetVideoLiked(video.ID, true)
	if err != nil || !liked.IsLiked {
		t.Fatalf("桌面点赞失败: %+v err=%v", liked, err)
	}
	if unliked, err := app.SetVideoLiked(video.ID, false); err != nil || unliked.IsLiked {
		t.Fatalf("取消点赞失败: %+v err=%v", unliked, err)
	}

	if status := app.GetIINASyncStatus(); status.Enabled && status.WatchingDir == "" {
		t.Fatalf("监听中必须报告目录: %+v", status)
	}

	if _, err := app.UpdateVideoWatchProgress(video.ID, 3000, 0, false, services.WatchProgressOriginResume); err != nil {
		t.Fatalf("进度上报失败: %v", err)
	}
	if kept, err := app.UpdateVideoWatchProgress(video.ID, 100, 0, false, services.WatchProgressOriginJump); err != nil || kept.WatchPositionSeconds != 3000 {
		t.Fatalf("jump 不应回写更早的位置: %+v err=%v", kept, err)
	}
	page, err := app.ListContinueWatchingWithFilter(services.LibraryFilter{}, "", 0, 20)
	if err != nil || len(page.Videos) != 1 || page.Videos[0].ID != video.ID || page.AutomaticOverrideKinds == nil {
		t.Fatalf("继续观看结果不对: %+v err=%v", page, err)
	}
}
