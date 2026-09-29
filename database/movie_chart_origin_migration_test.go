package database_test

import (
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// APP05/APP07：movie_chart_marks.watchlist_entry_origin 的回填迁移。

// 老库：本批次之前认领了条目的行只可能来自榜单创建，回填为 chart；未认领行保持空串；
// 重复执行结果不变。
func TestAPP07MigrateMovieChartMarkEntryOriginBackfillsLegacyRows(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows := []models.MovieChartMark{
		{DoubanID: "1001", Mark: models.MovieChartMarkWant, WatchlistEntryID: 11, MarkedAt: now},
		{DoubanID: "1002", Mark: models.MovieChartMarkWant, WatchlistEntryID: 0, MarkedAt: now},
		{DoubanID: "1003", Mark: models.MovieChartMarkWatched, MarkedAt: now},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	// 退回没有 watchlist_entry_origin 列的老库形状。
	if err := db.Migrator().DropColumn(&models.MovieChartMark{}, "watchlist_entry_origin"); err != nil {
		t.Fatalf("模拟老库删除列失败(%s): %v", dbtest.Backend(), err)
	}
	recycleConnections(t, db)

	applySchemaTwice(t, db)

	want := map[string]string{"1001": models.MovieChartOriginChart, "1002": "", "1003": ""}
	for doubanID, origin := range want {
		var mark models.MovieChartMark
		if err := db.Where("douban_id = ?", doubanID).First(&mark).Error; err != nil {
			t.Fatal(err)
		}
		if mark.WatchlistEntryOrigin != origin {
			t.Fatalf("douban=%s origin=%q，期望 %q", doubanID, mark.WatchlistEntryOrigin, origin)
		}
	}
}

// 老库里已有 enrichment 行时（列已存在）再跑迁移不得改写；新库无数据空转，之后写入的值保留。
func TestAPP07MigrateMovieChartMarkEntryOriginFreshDatabaseKeepsExplicitValues(t *testing.T) {
	db := dbtest.OpenRaw(t)
	applySchemaTwice(t, db)
	now := time.Now()
	if err := db.Create(&models.MovieChartMark{
		DoubanID: "2001", Mark: models.MovieChartMarkWant, WatchlistEntryID: 21,
		WatchlistEntryOrigin: models.MovieChartOriginEnrichment, MarkedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	applySchemaTwice(t, db)
	var mark models.MovieChartMark
	if err := db.Where("douban_id = ?", "2001").First(&mark).Error; err != nil {
		t.Fatal(err)
	}
	if mark.WatchlistEntryOrigin != models.MovieChartOriginEnrichment {
		t.Fatalf("显式来源不得被回填改写: %+v", mark)
	}
}
