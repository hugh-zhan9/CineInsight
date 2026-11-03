package database_test

import (
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// Use the same selectable-backend harness as the existing feature upgrades.
type chapterLegacyTechnical struct {
	VideoID    uint `gorm:"primarykey;autoIncrement:false"`
	FormatName string
	ProbedAt   *time.Time
}

func (chapterLegacyTechnical) TableName() string { return "video_technical_metadata" }
func TestChapterUpgradeKeepsOldSnapshotsUnread(t *testing.T) {
	db := dbtest.Open(t)
	if err := db.Migrator().DropTable(&models.VideoTechnicalMetadata{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&chapterLegacyTechnical{}); err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "old", Path: "/fixture/chapter-old.mp4"}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&chapterLegacyTechnical{VideoID: video.ID, FormatName: "mp4", ProbedAt: &at}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	var got models.VideoTechnicalMetadata
	if err := db.First(&got, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.ChaptersJSON != nil || got.ChaptersError != "" || got.FormatName != "mp4" || got.ProbedAt == nil || !got.ProbedAt.Equal(at) {
		t.Fatalf("old chapter state forged %+v", got)
	}
	if err := db.Model(&got).Update("chapters_json", "[]").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&got, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.ChaptersJSON == nil || *got.ChaptersJSON != "[]" {
		t.Fatal("read empty snapshot reverted to unread")
	}
}
