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
	v := models.Video{Name: "fixture", Path: "/fixture/tonight", Directory: "/fixture", Duration: 60}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
	result, err := a.SuggestTonightVideos(services.TonightRequest{MaxDurationSeconds: 60})
	if err != nil || len(result) != 1 || result[0].Video.ID != v.ID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	release := database.BeginMaintenance()
	_, err = a.SuggestTonightVideos(services.TonightRequest{})
	release()
	if err == nil || !strings.Contains(err.Error(), "数据库") {
		t.Fatalf("maintenance: %v", err)
	}
	if _, err := (&App{}).SuggestTonightVideos(services.TonightRequest{}); err == nil {
		t.Fatal("missing service accepted")
	}
}
