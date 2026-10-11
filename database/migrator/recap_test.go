package migrator

import (
	"context"
	"testing"
	"time"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

// seedRecapRows 给观看回顾（D-RECAP-SOURCES）造数：一张带查看计数与最近查看时间的图片，
// 以及两条不同来源的查看事件。view_count 用非 1 的值，验证往返不被默认值改写。
func seedRecapRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	viewedAt := time.Unix(1_700_000_500, 0).UTC()
	image := models.Image{Name: "回顾.jpg", Path: "/pics/回顾.jpg", Directory: "/pics", Format: "jpg", ViewCount: 3, LastViewedAt: &viewedAt}
	if err := db.Create(&image).Error; err != nil {
		t.Fatalf("建回顾图片夹具失败: %v", err)
	}
	events := []models.ImageViewEvent{
		{ImageID: image.ID, ViewedAt: viewedAt.Add(-time.Hour), Source: models.ImageViewSourceMobileFeed},
		{ImageID: image.ID, ViewedAt: viewedAt, Source: models.ImageViewSourceDesktopViewer},
	}
	if err := db.Omit("Image").Create(&events).Error; err != nil {
		t.Fatalf("建图片查看事件夹具失败: %v", err)
	}
}

// 图片查看事件表与 images 的两个新列随迁移原样往返。
func TestMigrateCarriesImageViewEventsAndCounters(t *testing.T) {
	source := dbtest.Open(t)
	middle := dbtest.Open(t)
	back := dbtest.Open(t)
	seedSource(t, source)
	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		if _, err := Migrate(context.Background(), Options{
			Source: step.from, Target: step.to, TargetBackend: backendOfTest(),
		}); err != nil {
			t.Fatalf("迁移失败: %v", err)
		}
	}
	if want, got := countUnscoped(t, source, &models.ImageViewEvent{}), countUnscoped(t, back, &models.ImageViewEvent{}); want < 2 || want != got {
		t.Fatalf("往返后查看事件行数不一致: source=%d back=%d", want, got)
	}
	var image models.Image
	if err := back.Where("path = ?", "/pics/回顾.jpg").First(&image).Error; err != nil {
		t.Fatalf("往返后读不到回顾图片: %v", err)
	}
	viewedAt := time.Unix(1_700_000_500, 0).UTC()
	if image.ViewCount != 3 || image.LastViewedAt == nil || !image.LastViewedAt.Equal(viewedAt) {
		t.Fatalf("view_count / last_viewed_at 往返后应原样保留: %d %v", image.ViewCount, image.LastViewedAt)
	}
	var events []models.ImageViewEvent
	if err := back.Where("image_id = ?", image.ID).Order("viewed_at ASC").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Source != models.ImageViewSourceMobileFeed || !events[1].ViewedAt.Equal(viewedAt) {
		t.Fatalf("查看事件往返后不一致: %+v", events)
	}
}
