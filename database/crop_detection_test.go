package database_test

import (
	"testing"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestCropDetectionSchemaDefaultAndCascade(t *testing.T) {
	db := dbtest.Open(t)
	settings := models.Settings{}
	if e := db.Create(&settings).Error; e != nil {
		t.Fatal(e)
	}
	if settings.AutoCropDetection {
		t.Fatal("auto detector must be opt in")
	}
	v := models.Video{Name: "a.mp4", Path: "/a.mp4"}
	if e := db.Create(&v).Error; e != nil {
		t.Fatal(e)
	}
	state := models.VideoCropDetectionState{VideoID: v.ID, Revision: 1, AlgorithmVersion: 1, Status: "undetected", ResultJSON: "{}"}
	if e := db.Create(&state).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Unscoped().Delete(&v).Error; e != nil {
		t.Fatal(e)
	}
	var n int64
	if e := db.Model(&models.VideoCropDetectionState{}).Count(&n).Error; e != nil || n != 0 {
		t.Fatal(n, e)
	}
}
