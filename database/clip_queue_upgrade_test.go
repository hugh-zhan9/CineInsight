package database_test

import (
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

type preClipQueueEntry struct {
	ID        uint `gorm:"primaryKey"`
	VideoID   uint
	Title     string
	Position  int64
	CreatedAt time.Time
}

func (preClipQueueEntry) TableName() string { return "playback_queue_entries" }
func TestClipQueueUpgradePreservesWholeVideoOrderAndState(t *testing.T) {
	db := dbtest.Open(t)
	if err := db.Migrator().DropTable(&models.PlaybackQueueEntry{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&preClipQueueEntry{}); err != nil {
		t.Fatal(err)
	}
	legacy := preClipQueueEntry{VideoID: 999, Title: "legacy whole video", Position: 37}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	state := models.PlaybackQueueState{ID: 1, Revision: 9, Player: "iina", Autoplay: true, Status: "stopped", CurrentEntryID: &legacy.ID}
	if err := db.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	var entry models.PlaybackQueueEntry
	if err := db.First(&entry, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if entry.EntryKind != "video" || entry.Position != 37 || entry.Title != legacy.Title || entry.ClipStartMS != nil || entry.ClipEndMS != nil || entry.SourceSize != nil || entry.SourceModTimeNS != nil || entry.PreviewBatchID != nil || entry.PreviewPosition != nil || entry.PreviewCount != nil || entry.OriginBookmarkID != nil {
		t.Fatalf("legacy entry changed %+v", entry)
	}
	var current models.PlaybackQueueState
	if err := db.First(&current, 1).Error; err != nil {
		t.Fatal(err)
	}
	if current.Revision != 9 || current.Player != "iina" || !current.Autoplay || current.CurrentEntryID == nil || *current.CurrentEntryID != legacy.ID || current.Status != "stopped" {
		t.Fatalf("queue state changed %+v", current)
	}
	if err := db.Create(&models.PlaybackQueueEntry{EntryKind: "clip", VideoID: 999, Title: "incomplete clip", Position: 38}).Error; err == nil {
		t.Fatal("partial clip fields passed schema")
	}
	start := int64(0)
	if err := db.Create(&models.PlaybackQueueEntry{EntryKind: "video", VideoID: 999, Title: "mixed kind", Position: 38, ClipStartMS: &start}).Error; err == nil {
		t.Fatal("video carried clip fields")
	}
}
