package database_test

import (
	"fmt"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestViewingDiaryMigrationRestartTombstonesAndSourceLifetime(t *testing.T) {
	db := dbtest.Open(t)
	video := models.Video{Name: "旧片名", DisplayTitle: "完整快照", Path: "/fixture/old.mp4"}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Date(2024, 2, 29, 18, 45, 0, 0, time.UTC)
	events := make([]models.PlayEvent, 503)
	for i := range events {
		events[i] = models.PlayEvent{VideoID: video.ID, PlayedAt: at, Source: "inline_view"}
		if i%2 == 0 {
			events[i].Source = "jellyfin_view"
		}
	}
	if err := db.CreateInBatches(&events, 100).Error; err != nil {
		t.Fatal(err)
	}
	unknown := []models.PlayEvent{{VideoID: video.ID, PlayedAt: at, Source: models.PlayEventSourceMobileFeed}, {VideoID: video.ID, PlayedAt: at, Source: models.PlayEventSourceDesktopPlay}, {VideoID: video.ID, Source: "inline_view"}}
	if err := db.Create(&unknown).Error; err != nil {
		t.Fatal(err)
	}
	// Emulate a committed prior batch and a user-deleted historical entry.
	occupied := models.ViewingDiaryEntry{SourcePlayEventID: &events[0].ID, Origin: models.DiaryOriginHistorical, Revision: 2}
	occupied.DeletedAt.Set(time.Now())
	if err := db.Unscoped().Create(&occupied).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 2; run++ {
		if err := database.ApplySchema(db); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	var rows []models.ViewingDiaryEntry
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 502 {
		t.Fatalf("backfill rows=%d", len(rows))
	}
	for _, row := range rows {
		if row.Title != video.DisplayTitle || row.RecordedAt == nil || !row.RecordedAt.Equal(at) || row.DateBasis != models.DiaryDateImported || row.Origin != models.DiaryOriginHistorical || row.SourceSessionKey != nil || row.UTCOffsetSeconds != nil || row.Rating != nil {
			t.Fatalf("invented/lost facts: %+v", row)
		}
	}
	var tomb models.ViewingDiaryEntry
	if err := db.Unscoped().First(&tomb, occupied.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !tomb.DeletedAt.IsValid() || tomb.Title != "" {
		t.Fatal("backfill resurrected deleted diary")
	}
	bookmark := models.VideoBookmark{VideoID: video.ID, VideoTitle: video.Name, Title: "保留笔记", Revision: 1}
	if err := db.Create(&bookmark).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		model any
		want  int64
	}{{&models.PlayEvent{}, 0}, {&models.ViewingDiaryEntry{}, 502}, {&models.VideoBookmark{}, 1}} {
		var count int64
		if err := db.Model(check.model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != check.want {
			t.Fatal(fmt.Sprintf("%T count=%d want=%d", check.model, count, check.want))
		}
	}
}
