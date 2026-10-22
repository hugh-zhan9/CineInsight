package services

import (
	"context"
	"fmt"
	"gorm.io/gorm"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestWatchlistAutoReconcilesChangedIdentity(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	year := 2021
	e := f.entry("沙丘", year, "")
	v := f.video("沙丘", &year, nil)
	if _, err := f.service.runLibraryMatchRound(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := UpdateVideoTitleFields(database.DB.Model(&models.Video{}).Where("id = ?", v.ID), v.ID, map[string]interface{}{"display_title": "降临"}).Error; err != nil {
		t.Fatal(err)
	}
	state, err := f.service.ListLibraryState([]uint{e.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(state[e.ID].Linked) != 0 {
		t.Fatal("stale auto link exposed before reconciliation")
	}
	replacement := f.video("沙丘", &year, nil)
	if _, err := f.service.runLibraryMatchRound(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	state, err = f.service.ListLibraryState([]uint{e.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(state[e.ID].Linked) != 1 || state[e.ID].Linked[0].VideoID != replacement.ID {
		t.Fatalf("replacement missing: %+v", state)
	}
	var count int64
	database.DB.Model(&models.WatchlistVideoLink{}).Where("watchlist_entry_id = ? AND video_id = ?", e.ID, v.ID).Count(&count)
	if count != 0 {
		t.Fatal("stale persisted link remains")
	}
}

func TestWatchlistAutoOfflineIdentityPreserved(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	year := 2021
	e := f.entry("沙丘", year, "")
	v := f.video("沙丘", &year, nil)
	f.service.runLibraryMatchRound(context.Background(), nil)
	database.DB.Model(&models.Video{}).Where("id = ?", v.ID).Update("is_stale", true)
	if _, err := f.service.runLibraryMatchRound(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	state, err := f.service.ListLibraryState([]uint{e.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(state[e.ID].Linked) != 1 {
		t.Fatal("offline is not identity mismatch")
	}
}

func TestWatchlistAutoAmbiguityAndManualRace(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	year := 2021
	e := f.entry("沙丘", year, "")
	v := f.video("沙丘", &year, nil)
	if _, err := f.service.runLibraryMatchRound(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	f.video("沙丘", &year, nil)
	state, err := f.service.ListLibraryState([]uint{e.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(state[e.ID].Linked) != 0 {
		t.Fatal("ambiguous auto projected")
	}
	f.service.match.beforeAutoWrite = func(entryID, videoID uint) {
		if err := f.service.LinkVideo(entryID, videoID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.service.runLibraryMatchRound(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	state, err = f.service.ListLibraryState([]uint{e.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(state[e.ID].Linked) != 1 || state[e.ID].Linked[0].Origin != models.WatchlistLinkOriginManual || state[e.ID].Linked[0].VideoID != v.ID {
		t.Fatalf("manual lost to CAS %+v", state)
	}
}

func TestWatchlistMatchingManySameYearEntries(t *testing.T) {
	setupVideoServiceTestDB(t)
	entries := make([]models.WatchlistEntry, 40000)
	for i := range entries {
		entries[i] = models.WatchlistEntry{ID: uint(i + 1), Title: "A", Year: 2020}
	}
	if _, err := watchlistMatchingWorks(database.DB, entries); err != nil {
		t.Fatalf("40k entries with one distinct year must be bounded: %v", err)
	}
}

func TestWatchlistMatchingOneEntryWithLargeYearCohort(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<40000) INSERT INTO videos(name,path,directory,year,is_stale,is_watched) SELECT 'other','/fixture/'||x,'/fixture',2020,false,false FROM n`).Error; err != nil {
		t.Fatal(err)
	}
	entries := []models.WatchlistEntry{{ID: 1, Title: "Only", Year: 2020}}
	if _, err := watchlistMatchingWorks(database.DB, entries); err != nil {
		t.Fatalf("ONE entry and 40k unrelated same-year videos must be bounded: %v", err)
	}
}

func TestWatchlistStartupReconciliationReadsBoundedBatches(t *testing.T) {
	setupVideoServiceTestDB(t)
	entries := make([]models.WatchlistEntry, 1001)
	for i := range entries {
		entries[i] = models.WatchlistEntry{Title: fmt.Sprintf("No match %d", i), Year: 2020, Kind: models.WatchlistKindMovie}
	}
	if err := database.DB.CreateInBatches(&entries, 200).Error; err != nil {
		t.Fatal(err)
	}
	queries, total := 0, int64(0)
	if err := database.DB.Callback().Query().After("gorm:query").Register("test:watchlist_bounded_batch", func(tx *gorm.DB) {
		if tx.Statement.Table == "watchlist_entries" {
			queries++
			total += tx.RowsAffected
			if tx.RowsAffected > 500 {
				t.Errorf("unbounded startup batch %d", tx.RowsAffected)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Query().Remove("test:watchlist_bounded_batch")
	svc := NewWatchlistService(t.TempDir())
	if _, err := svc.runLibraryMatchRound(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if queries < 3 || total != 1001 {
		t.Fatalf("startup batches=%d rows=%d", queries, total)
	}
}
