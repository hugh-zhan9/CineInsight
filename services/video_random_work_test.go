package services

import (
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestRandomWorksUseGroupHistoryAndEligibleRepresentative(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.AutoMigrate(&models.RecommendationFeedback{}); err != nil {
		t.Fatal(err)
	}
	rows := []models.Video{
		{Name: "primary", Path: "/fixture/a", Directory: "/fixture", IsWatched: true, IsStale: true, PlayCount: 10},
		{Name: "alternate", Path: "/fixture/b", Directory: "/fixture", IsFavorite: true},
		{Name: "third", Path: "/fixture/c", Directory: "/fixture", IsFavorite: true},
	}
	for i := range rows {
		if err := database.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	group := models.VideoVersionGroup{Title: "work", Revision: 1}
	if err := database.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		if err := database.DB.Create(&models.VideoVersionMember{VideoID: rows[i].ID, GroupID: group.ID, Position: i + 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &VideoService{}
	pool, err := svc.collectRandomCandidatesFrom(database.DB, RandomPlayRequest{FavoritesOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(pool.rows) != 1 || pool.rows[0].ID != rows[1].ID {
		t.Fatalf("expected one work with first eligible version, got %+v", pool.rows)
	}
	if score := decayedPlayScore(pool.rows[0], pool.playWeight, pool.halfLifeDays, time.Now()); score != 20 {
		t.Fatalf("offline history lost: %v", score)
	}
	pool, err = svc.collectRandomCandidatesFrom(database.DB, RandomPlayRequest{WatchState: RandomWatchUnwatched})
	if err != nil || len(pool.rows) != 0 {
		t.Fatalf("watched offline member must exclude work: %+v %v", pool, err)
	}
	pool, err = svc.collectRandomCandidatesFrom(database.DB, RandomPlayRequest{ExcludeIDs: []uint{rows[0].ID}})
	if err != nil || len(pool.rows) != 0 {
		t.Fatalf("excluded version must exclude work: %+v %v", pool, err)
	}
	pool, err = svc.collectRandomCandidatesFrom(database.DB, RandomPlayRequest{WatchState: RandomWatchWatched, Filter: LibraryFilter{SmartView: LibraryViewUnwatched}})
	if err != nil || len(pool.rows) != 0 {
		t.Fatalf("contradictory hard conditions relaxed: %+v %v", pool, err)
	}
}
