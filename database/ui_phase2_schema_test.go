package database_test

import (
	"testing"

	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// UI 二期（D-P2-01 / D-P2-03）的表结构：片名排序键列与复合索引、想看关联表与级联、榜单关联的来源列；两个后端都跑。
func TestUIPhase2SchemaOnBothBackends(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	migrator := db.Migrator()
	if !migrator.HasColumn(&models.Video{}, "title_sort_key") {
		t.Fatal("videos 应有 title_sort_key")
	}
	if !migrator.HasIndex(&models.Video{}, "idx_videos_title_sort_active") {
		t.Fatal("应建 (title_sort_key, id) 复合索引")
	}
	if !migrator.HasColumn(&models.MovieVideoLink{}, "origin") {
		t.Fatal("movie_video_links 应有 origin")
	}
	if !migrator.HasIndex(&models.WatchlistVideoLink{}, "idx_watchlist_video_links_pair") {
		t.Fatal("watchlist_video_links 应有 (条目, 视频) 唯一索引")
	}

	video := models.Video{Name: "a.mkv", Path: "/library/a.mkv", Directory: "/library"}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	entry := models.WatchlistEntry{Title: "沙丘", Kind: models.WatchlistKindMovie, EnrichmentStatus: models.WatchlistEnrichmentManual}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	link := models.WatchlistVideoLink{WatchlistEntryID: entry.ID, VideoID: video.ID, Origin: models.WatchlistLinkOriginAuto}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := models.WatchlistVideoLink{WatchlistEntryID: entry.ID, VideoID: video.ID, Origin: models.WatchlistLinkOriginManual}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("同一对不能有两行")
	}
	// 存量写法（不带 origin）落成空串 = 手动。
	if err := db.Exec("INSERT INTO movie_video_links (douban_id, video_id, created_at, updated_at) VALUES (?, ?, ?, ?)", "1", video.ID, video.CreatedAt, video.CreatedAt).Error; err != nil {
		t.Fatal(err)
	}
	var origin string
	if err := db.Raw("SELECT origin FROM movie_video_links WHERE douban_id = ?", "1").Scan(&origin).Error; err != nil || origin != "" {
		t.Fatalf("存量行 origin 应为空串: %q %v", origin, err)
	}
	// 删除想看条目级联删掉它的关联。
	if err := db.Delete(&models.WatchlistEntry{}, entry.ID).Error; err != nil {
		t.Fatal(err)
	}
	var remaining int64
	if err := db.Model(&models.WatchlistVideoLink{}).Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("删条目应级联删关联: %d %v", remaining, err)
	}
}
