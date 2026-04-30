package main

import (
	"path/filepath"
	"testing"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLoadSqliteDataIncludesSoftDeleted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	tag := models.Tag{Name: "运动", Color: "#fff"}
	if err := db.Create(&tag).Error; err != nil {
		t.Fatalf("create tag: %v", err)
	}
	video := models.Video{Name: "cat.mp4", Path: "/tmp/cat.mp4", Directory: "/tmp", Size: 1}
	if err := db.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	if err := db.Model(&video).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("bind tag: %v", err)
	}
	if err := db.Delete(&tag).Error; err != nil {
		t.Fatalf("soft delete tag: %v", err)
	}
	if err := db.Delete(&video).Error; err != nil {
		t.Fatalf("soft delete video: %v", err)
	}

	settings := models.Settings{ConfirmBeforeDelete: true}
	if err := db.Create(&settings).Error; err != nil {
		t.Fatalf("create settings: %v", err)
	}
	dir := models.ScanDirectory{Path: "/tmp", Alias: "tmp"}
	if err := db.Create(&dir).Error; err != nil {
		t.Fatalf("create scan dir: %v", err)
	}

	snapshot, err := loadSqliteData(db)
	if err != nil {
		t.Fatalf("load sqlite data: %v", err)
	}
	if len(snapshot.Videos) != 1 {
		t.Fatalf("expected 1 video, got %d", len(snapshot.Videos))
	}
	if !snapshot.Videos[0].DeletedAt.IsValid() {
		t.Fatalf("expected soft deleted video preserved")
	}
	if len(snapshot.Tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(snapshot.Tags))
	}
	if !snapshot.Tags[0].DeletedAt.IsValid() {
		t.Fatalf("expected soft deleted tag preserved")
	}
	if len(snapshot.Settings) != 1 {
		t.Fatalf("expected 1 settings, got %d", len(snapshot.Settings))
	}
	if len(snapshot.ScanDirectories) != 1 {
		t.Fatalf("expected 1 scan directory, got %d", len(snapshot.ScanDirectories))
	}
	if len(snapshot.VideoTags) != 1 {
		t.Fatalf("expected 1 video_tags, got %d", len(snapshot.VideoTags))
	}
}
