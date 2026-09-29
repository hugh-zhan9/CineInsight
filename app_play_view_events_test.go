package main

import (
	"testing"

	"video-master/database"
	"video-master/models"
	"video-master/services"
)

// P-023 的 App 绑定：有效观看事件按会话去重（PLAY-07），失效令牌的「换一个」不抽取也不写库（PLAY-08）。
func TestPlayViewEventAppBindingsPLAY07PLAY08(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2.0}).Error; err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	video := models.Video{Name: "view.mp4", Path: "/media/view.mp4", Directory: "/media", Duration: 600}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}

	session := "app-binding-session-p023"
	if recorded, err := app.RecordViewEvent(video.ID, services.PlayEventSourceInlineView, session); err != nil || !recorded {
		t.Fatalf("首次上报应写入: recorded=%v err=%v", recorded, err)
	}
	if recorded, err := app.RecordViewEvent(video.ID, services.PlayEventSourceInlineView, session); err != nil || recorded {
		t.Fatalf("同一会话再次上报不应写入: recorded=%v err=%v", recorded, err)
	}
	if _, err := app.RecordViewEvent(video.ID, models.PlayEventSourceMobileFeed, "other"); err == nil {
		t.Fatalf("mobile_feed 不走这个入口")
	}

	result, err := app.RerollRandom("0123456789abcdef0123456789abcdef")
	if err != nil || result == nil || result.DispatchSucceeded || result.ReasonCode != "reroll_expired" {
		t.Fatalf("不认识的令牌应返回 reroll_expired: %+v err=%v", result, err)
	}

	var events []models.PlayEvent
	if err := database.DB.Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Source != services.PlayEventSourceInlineView {
		t.Fatalf("应只有一条 inline_view: %+v", events)
	}
}
