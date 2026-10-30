package services

import (
	"fmt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"path/filepath"
	"testing"
	"video-master/database"
)

// Synthetic SQLite fixture for the real query path; no production DB or file access.
func BenchmarkCollectionProgressPage(b *testing.B) {
	for _, count := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("collections_%d", count), func(b *testing.B) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(b.TempDir(), "progress.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				b.Fatal(err)
			}
			old := database.DB
			database.DB = db
			b.Cleanup(func() { database.DB = old; sqlDB, _ := db.DB(); sqlDB.Close() })
			statements := []string{
				`CREATE TABLE media_collections (id INTEGER PRIMARY KEY, name TEXT, cover_path TEXT, deleted_at DATETIME)`,
				`CREATE TABLE videos (id INTEGER PRIMARY KEY, display_title TEXT, name TEXT, is_watched BOOLEAN, watch_position_seconds REAL, duration REAL, last_played_at DATETIME, watch_progress_updated_at DATETIME, watched_at DATETIME, deleted_at DATETIME, is_stale BOOLEAN)`,
				`CREATE TABLE collection_videos (collection_id INTEGER, video_id INTEGER, position INTEGER, PRIMARY KEY(collection_id,video_id))`,
				`CREATE TABLE video_version_members (video_id INTEGER PRIMARY KEY, group_id INTEGER, position INTEGER)`,
				fmt.Sprintf(`WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x < %d) INSERT INTO media_collections SELECT x, 'Series ' || x, '', NULL FROM seq`, count),
				fmt.Sprintf(`WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x < %d) INSERT INTO videos SELECT x, 'Episode', 'ep.mp4', x%%20=1, 0, 3600, CASE WHEN x%%20=1 THEN '2026-10-01 00:00:00' END, NULL, NULL, NULL, 0 FROM seq`, count*20),
				`INSERT INTO collection_videos SELECT (id-1)/20+1,id,(id-1)%20 FROM videos`,
			}
			for _, sql := range statements {
				if err := db.Exec(sql).Error; err != nil {
					b.Fatal(err)
				}
			}
			svc := &CollectionService{}
			b.ReportAllocs()
			b.ReportMetric(float64(count*20), "fixture_members")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				page, err := svc.ListCollectionProgressPage(CollectionProgressRequest{ContinueOnly: true, Limit: 12})
				if err != nil || len(page.Items) != 12 || page.Total != count {
					b.Fatalf("page=%+v err=%v", page, err)
				}
			}
		})
	}
}
