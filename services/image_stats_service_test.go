package services

import (
	"reflect"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestImageStatsEmptyLibrary(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	service := NewImageStatsService()
	service.now = func() time.Time { return now }
	stats, err := service.GetImageInsights()
	if err != nil {
		t.Fatal(err)
	}
	if !stats.GeneratedAt.Equal(now) {
		t.Fatalf("generated_at=%v", stats.GeneratedAt)
	}
	if stats.Summary.ImageCount != 0 || stats.Summary.TotalSize != 0 || stats.Summary.FavoriteCount != 0 {
		t.Fatalf("empty summary=%#v", stats.Summary)
	}
	if len(stats.StorageByDirectory) != 0 || len(stats.StorageByFormat) != 0 {
		t.Fatalf("empty buckets: dir=%#v format=%#v", stats.StorageByDirectory, stats.StorageByFormat)
	}
}

func TestImageStatsAggregatesFixture(t *testing.T) {
	setupVideoServiceTestDB(t)
	images := []models.Image{
		{Name: "a.jpg", Path: "/pics/a/a.jpg", Directory: "/pics/a", Size: 100, Format: "jpg", IsFavorite: true},
		{Name: "b.jpg", Path: "/pics/a/b.jpg", Directory: "/pics/a", Size: 200, Format: "jpg"},
		{Name: "c.heic", Path: "/pics/b/c.heic", Directory: "/pics/b", Size: 400, Format: "heic", IsFavorite: true},
		{Name: "d.png", Path: "/pics/b/d.png", Directory: "/pics/b", Size: 50, Format: "png"},
	}
	if err := database.DB.Create(&images).Error; err != nil {
		t.Fatal(err)
	}

	// 软删除的图片不得进入任何聚合口径（摘要、目录、格式、收藏计数）。
	trashed := models.Image{Name: "gone.jpg", Path: "/pics/a/gone.jpg", Directory: "/pics/a", Size: 9999, Format: "jpg", IsFavorite: true}
	if err := database.DB.Create(&trashed).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&models.Image{}, trashed.ID).Error; err != nil {
		t.Fatal(err)
	}

	stats, err := NewImageStatsService().GetImageInsights()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Summary.ImageCount != 4 || stats.Summary.TotalSize != 750 || stats.Summary.FavoriteCount != 2 {
		t.Fatalf("summary=%#v", stats.Summary)
	}
	wantDirs := []ImageStatsBucket{{Label: "/pics/b", TotalSize: 450}, {Label: "/pics/a", TotalSize: 300}}
	if !reflect.DeepEqual(stats.StorageByDirectory, wantDirs) {
		t.Fatalf("directory buckets=%#v", stats.StorageByDirectory)
	}
	wantFormats := []ImageStatsBucket{{Label: "heic", TotalSize: 400}, {Label: "jpg", TotalSize: 300}, {Label: "png", TotalSize: 50}}
	if !reflect.DeepEqual(stats.StorageByFormat, wantFormats) {
		t.Fatalf("format buckets=%#v", stats.StorageByFormat)
	}
}

// TestImageStatsLibraryInsightsUnchanged 固定 TC-9 的后半句：图片入库前后
// GetLibraryInsights 的返回结构与数值一字不变。
func TestImageStatsLibraryInsightsUnchanged(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-24 * time.Hour)
	videos := []models.Video{
		{Name: "one.mp4", Path: "/library/a/one.mp4", Directory: "/library/a", Size: 100, Duration: 60, Height: 1080, IsWatched: true, LastPlayedAt: &recent, PersonalRating: float64Pointer(8.5)},
		{Name: "two.mp4", Path: "/library/b/two.mp4", Directory: "/library/b", Size: 300, Duration: 120, Height: 2160},
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
	tag := models.Tag{Name: "剧情", Color: "#fff", IsSystem: true, IsActive: true}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&videos[0]).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}

	libraryService := NewLibraryStatsService()
	libraryService.now = func() time.Time { return now }
	before, err := libraryService.GetStats()
	if err != nil {
		t.Fatal(err)
	}

	// 图片入库，且与视频共享 tags 表：给图片打同一个标签，覆盖 image_tags
	// 关联不得污染视频统计口径的情形。
	images := []models.Image{
		{Name: "a.jpg", Path: "/library/a/a.jpg", Directory: "/library/a", Size: 12345, Format: "jpg", IsFavorite: true},
		{Name: "b.heic", Path: "/pics/b.heic", Directory: "/pics", Size: 54321, Format: "heic"},
	}
	if err := database.DB.Create(&images).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&images[0]).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}

	after, err := libraryService.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("GetLibraryInsights changed after image ingestion:\nbefore=%#v\nafter=%#v", before, after)
	}
	if after.Summary.VideoCount != 2 || after.Summary.TotalSize != 400 {
		t.Fatalf("video summary=%#v", after.Summary)
	}
	if len(after.StorageByTag) != 1 || after.StorageByTag[0].Count != 1 || after.StorageByTag[0].Bytes != 100 {
		t.Fatalf("tag buckets=%#v", after.StorageByTag)
	}
}
