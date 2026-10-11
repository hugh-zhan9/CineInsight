package main

import (
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

func TestTonightAppBindingAndUnavailableGuards(t *testing.T) {
	setupAppTestDB(t)
	a := &App{videoService: &services.VideoService{}}
	// 选片与随机播放共用均衡加权，权重来自设置。
	if err := database.DB.Create(&models.Settings{PlayWeight: 2}).Error; err != nil {
		t.Fatal(err)
	}
	v := models.Video{Name: "fixture", Path: "/fixture/tonight", Directory: "/fixture", Duration: 60}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
	result, err := a.SuggestTonightVideos(services.RandomPlayRequest{MaxDurationSeconds: 60}, 0)
	if err != nil || len(result) != 1 || result[0].Video.ID != v.ID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	release := database.BeginMaintenance()
	_, err = a.SuggestTonightVideos(services.RandomPlayRequest{}, 0)
	release()
	if err == nil || !strings.Contains(err.Error(), "数据库") {
		t.Fatalf("maintenance: %v", err)
	}
	if _, err := (&App{}).SuggestTonightVideos(services.RandomPlayRequest{}, 0); err == nil {
		t.Fatal("missing service accepted")
	}
}
