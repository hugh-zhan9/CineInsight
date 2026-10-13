package database_test

import (
	"testing"

	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// UI 二期（D-P2-01 / D-P2-03）的表结构：片名排序键列与复合索引、想看关联表与级联；两个后端都跑。
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
	if !migrator.HasIndex(&models.WatchlistVideoLink{}, "idx_watchlist_video_links_pair") {
		t.Fatal("watchlist_video_links 应有 (条目, 视频) 唯一索引")
	}
	if migrator.HasColumn(&models.MovieVideoLink{}, "origin") {
		t.Fatal("movie_video_links 不再有 origin 列")
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
	// 删除想看条目级联删掉它的关联。
	if err := db.Delete(&models.WatchlistEntry{}, entry.ID).Error; err != nil {
		t.Fatal(err)
	}
	var remaining int64
	if err := db.Model(&models.WatchlistVideoLink{}).Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("删条目应级联删关联: %d %v", remaining, err)
	}
}

// 本分支早先的提交给 movie_video_links 加过 origin 列（未发布）；留着这一列的库照常迁移、照常写关联。
func TestLeftoverMovieVideoLinkOriginColumnIsHarmless(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE movie_video_links ADD COLUMN origin varchar(16) NOT NULL DEFAULT ''").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("留着 origin 列的库应能再次迁移(%s): %v", dbtest.Backend(), err)
	}
	video := models.Video{Name: "a.mkv", Path: "/library/a.mkv", Directory: "/library"}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.MovieVideoLink{DoubanID: "1", VideoID: video.ID}).Error; err != nil {
		t.Fatalf("留着 origin 列时照常写关联: %v", err)
	}
}
