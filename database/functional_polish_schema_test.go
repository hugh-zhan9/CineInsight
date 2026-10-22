package database_test

import (
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

type legacyPolishPlayEvent struct {
	ID     uint   `gorm:"primaryKey"`
	Source string `gorm:"size:16;not null"`
}

func (legacyPolishPlayEvent) TableName() string { return "play_events" }

type legacyPolishDiary struct {
	ID     uint   `gorm:"primaryKey"`
	Source string `gorm:"size:16;not null;default:''"`
}

func (legacyPolishDiary) TableName() string { return "viewing_diary_entries" }

func TestFunctionalPolishLegacyUpgradePreservesRowsAndDefaults(t *testing.T) {
	db := dbtest.Open(t)
	// Construct the exact previous shape at the changed boundary: all prior tables,
	// 16-character sources, no five new tables or owner authorization columns.
	for _, model := range []any{&models.TaskFailureItem{}, &models.TaskFailureRun{}, &models.CollectionEpisodeAssignment{}, &models.CollectionSeasonExpectation{}, &models.RecommendationFeedback{}} {
		if err := db.Migrator().DropTable(model); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		model  any
		column string
	}{{&models.Settings{}, "desktop_player"}, {&models.AITaggingState{}, "failure_retry_source_version"}, {&models.VideoTechnicalMetadata{}, "failure_retry_claim"}, {&models.VideoPlaybackProxy{}, "failure_retry_claim"}} {
		if err := db.Migrator().DropColumn(item.model, item.column); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrator().AlterColumn(&legacyPolishPlayEvent{}, "Source"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().AlterColumn(&legacyPolishDiary{}, "Source"); err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "before-upgrade.mp4", Path: "/legacy/before.mp4", Directory: "/legacy"}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 21, 1, 2, 3, 123456000, time.UTC)
	oldEvent := models.PlayEvent{VideoID: video.ID, Source: "desktop_play", PlayedAt: at}
	if err := db.Create(&oldEvent).Error; err != nil {
		t.Fatal(err)
	}
	oldDiary := models.ViewingDiaryEntry{VideoID: &video.ID, Title: "旧日记", WatchedOn: "2026-10-21", Source: "desktop_play", Revision: 3, CreatedAt: at, UpdatedAt: at}
	if err := db.Create(&oldDiary).Error; err != nil {
		t.Fatal(err)
	}
	for table, row := range map[string]map[string]any{
		"settings":                 {"id": 1, "playback_resume_mode": "restart", "play_weight": 2},
		"ai_tagging_states":        {"video_id": video.ID, "status": "failed", "last_error": "old AI failure", "attempt_count": 7},
		"video_technical_metadata": {"video_id": video.ID, "format_name": "matroska", "last_error": "old probe failure"},
		"video_playback_proxies":   {"video_id": video.ID, "source_size": 123, "source_mod_time_ns": 456, "strategy": "remux", "status": "failed", "last_used_at": at, "last_error": "old proxy failure"},
	} {
		if err := db.Table(table).Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if db.Dialector.Name() == "postgres" {
		if err := db.Create(&models.PlayEvent{VideoID: video.ID, Source: "desktop_controlled_view", PlayedAt: at}).Error; err == nil {
			t.Fatal("legacy source width fixture was not 16")
		}
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("repeat upgrade: %v", err)
	}
	var event models.PlayEvent
	if err := db.First(&event, oldEvent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if event.Source != "desktop_play" || !event.PlayedAt.Equal(at) {
		t.Fatal("old playback fact changed")
	}
	var diary models.ViewingDiaryEntry
	if err := db.First(&diary, oldDiary.ID).Error; err != nil {
		t.Fatal(err)
	}
	if diary.Source != "desktop_play" || diary.Note != "" || diary.Title != "旧日记" || diary.Revision != 3 {
		t.Fatal("old diary changed")
	}
	var ai models.AITaggingState
	db.Where("video_id = ?", video.ID).First(&ai)
	if ai.FailureRetrySourceVersion != "" || ai.Status != "failed" || ai.LastError != "old AI failure" || ai.AttemptCount != 7 {
		t.Fatalf("AI upgrade %+v", ai)
	}
	var tech models.VideoTechnicalMetadata
	db.First(&tech, "video_id = ?", video.ID)
	if tech.FailureRetryClaim != "" || tech.LastError != "old probe failure" || tech.FormatName != "matroska" {
		t.Fatalf("technical upgrade %+v", tech)
	}
	var proxy models.VideoPlaybackProxy
	db.First(&proxy, "video_id = ?", video.ID)
	if proxy.FailureRetryClaim != "" || proxy.LastError != "old proxy failure" || proxy.SourceSize != 123 {
		t.Fatalf("proxy upgrade %+v", proxy)
	}
	var settings models.Settings
	db.First(&settings)
	if settings.DesktopPlayer != "" || settings.PlaybackResumeMode != "restart" {
		t.Fatalf("changed existing preference %+v", settings)
	}
	for _, model := range []any{&models.TaskFailureItem{}, &models.TaskFailureRun{}, &models.CollectionEpisodeAssignment{}, &models.CollectionSeasonExpectation{}, &models.RecommendationFeedback{}} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("upgrade invented history for %T", model)
		}
	}
	if err := db.Create(&models.PlayEvent{VideoID: video.ID, Source: "desktop_controlled_view", PlayedAt: at}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ViewingDiaryEntry{VideoID: &video.ID, Title: "new controlled", WatchedOn: "2026-10-22", Source: "desktop_controlled_view", Revision: 1}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestFunctionalPolishProductionCompositeAndHistoryConstraints(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	videos := []models.Video{{Name: "one", Path: "/constraint/one"}, {Name: "two", Path: "/constraint/two"}}
	for i := range videos {
		if err := db.Create(&videos[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	c := models.MediaCollection{Name: "series", NormalizedName: "series"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	member := models.CollectionVideo{CollectionID: c.ID, VideoID: videos[0].ID, Position: 1}
	if err := db.Create(&member).Error; err != nil {
		t.Fatal(err)
	}
	assignment := models.CollectionEpisodeAssignment{CollectionID: c.ID, VideoID: videos[0].ID, Excluded: false, Revision: 2}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.CollectionEpisodeAssignment{CollectionID: c.ID, VideoID: videos[1].ID, Revision: 1}).Error; err == nil {
		t.Fatal("independent parent IDs bypassed composite membership FK")
	}
	if err := db.Delete(&member).Error; err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Model(&models.CollectionEpisodeAssignment{}).Count(&n)
	if n != 0 {
		t.Fatal("member deletion did not cascade correction")
	}
	run := models.TaskFailureRun{TaskKey: "technical", State: "interrupted", RecordsComplete: false}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	item := models.TaskFailureItem{RunID: run.ID, MediaKind: "video", MediaID: videos[0].ID, AttemptKey: "same-attempt", SourceVersion: "source", StateVersion: "failure", ErrorCode: "failed", ErrorSummary: "history"}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := item
	duplicate.ID = 0
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate attempt accepted")
	}
	if err := db.Unscoped().Delete(&videos[0]).Error; err != nil {
		t.Fatal(err)
	}
	db.Model(&models.TaskFailureItem{}).Count(&n)
	if n != 1 {
		t.Fatal("logical media deletion erased failure history")
	}
	if err := db.Delete(&run).Error; err != nil {
		t.Fatal(err)
	}
	db.Model(&models.TaskFailureItem{}).Count(&n)
	if n != 0 {
		t.Fatal("diagnostic run deletion did not cascade items")
	}
}
