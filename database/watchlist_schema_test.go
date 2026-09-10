package database_test

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestWatchlistUpgradeDeduplicatesAndEnforcesUniqueTitle(t *testing.T) {
	db := dbtest.Open(t)
	if err := db.Migrator().DropIndex(&models.WatchlistEntry{}, "idx_watchlist_title"); err != nil {
		t.Fatal(err)
	}
	old := []models.WatchlistEntry{{Title: "沙丘"}, {Title: "  沙丘  "}, {Title: "沙丘（1984）"}}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := database.ApplySchema(db); err != nil {
			t.Fatal(err)
		}
	}
	var entries []models.WatchlistEntry
	if err := db.Order("id").Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != old[0].ID || entries[0].CreatedAt.UnixMicro() != old[0].CreatedAt.UnixMicro() || entries[1].ID != old[2].ID {
		t.Fatalf("migration must preserve oldest and different titles: %+v", entries)
	}
	if err := db.Create(&models.WatchlistEntry{Title: "沙丘"}).Error; err == nil {
		t.Fatal("database must reject duplicate writes")
	}
}
