package services

import (
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestLibraryStatsAggregatesFixture(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(-2, 0, 0)
	recent := now.Add(-24 * time.Hour)
	videos := []models.Video{
		{Name: "one.mp4", Path: "/library/a/one.mp4", Directory: "/library/a", Size: 100, Duration: 60, Height: 1080, IsWatched: true, LastPlayedAt: &recent, PersonalRating: float64Pointer(8.5)},
		{Name: "two.mp4", Path: "/library/b/two.mp4", Directory: "/library/b", Size: 300, Duration: 120, Height: 2160, LastPlayedAt: &old, PersonalRating: float64Pointer(8.5)},
		{Name: "three.mp4", Path: "/library/b/three.mp4", Directory: "/library/b", Size: 200, Duration: 0, Height: 0},
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
	if err := database.DB.Model(&videos[1]).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}

	// 软删除的视频与标签不得进入任何聚合口径。
	trashed := models.Video{Name: "trashed.mp4", Path: "/library/a/trashed.mp4", Directory: "/library/a", Size: 9999, Duration: 999, Height: 2160, IsWatched: true, LastPlayedAt: &recent, PersonalRating: float64Pointer(9.5)}
	if err := database.DB.Create(&trashed).Error; err != nil {
		t.Fatal(err)
	}
	deletedTag := models.Tag{Name: "已删标签", Color: "#000", IsActive: true}
	if err := database.DB.Create(&deletedTag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&trashed).Association("Tags").Append(&deletedTag); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&models.Video{}, trashed.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&models.Tag{}, deletedTag.ID).Error; err != nil {
		t.Fatal(err)
	}

	service := NewLibraryStatsService()
	service.now = func() time.Time { return now }
	stats, err := service.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Summary.VideoCount != 3 || stats.Summary.TotalSize != 600 || stats.Summary.TotalDuration != 180 || stats.Summary.WatchedCount != 1 {
		t.Fatalf("summary mismatch: %#v", stats.Summary)
	}
	if stats.Summary.WatchedPercent < 33.3 || stats.Summary.WatchedPercent > 33.4 {
		t.Fatalf("watched percent=%f", stats.Summary.WatchedPercent)
	}
	if len(stats.StorageByDirectory) != 2 || stats.StorageByDirectory[0].Label != "/library/b" || stats.StorageByDirectory[0].Bytes != 500 {
		t.Fatalf("directory buckets=%#v", stats.StorageByDirectory)
	}
	if len(stats.StorageByTag) != 1 || stats.StorageByTag[0].Count != 2 || stats.StorageByTag[0].Bytes != 400 {
		t.Fatalf("tag buckets=%#v", stats.StorageByTag)
	}
	if len(stats.TopAITags) != 1 || stats.TopAITags[0].Label != "剧情" {
		t.Fatalf("AI tags=%#v", stats.TopAITags)
	}
	if len(stats.WatchHeatmap) != 1 || stats.WatchHeatmap[0].Count != 1 {
		t.Fatalf("watch heatmap=%#v", stats.WatchHeatmap)
	}
	if len(stats.RatingDistribution) != 1 || stats.RatingDistribution[0].Rating != 8.5 || stats.RatingDistribution[0].Count != 2 {
		t.Fatalf("ratings=%#v", stats.RatingDistribution)
	}
}

func TestLibraryStatsEmptyLibrary(t *testing.T) {
	setupVideoServiceTestDB(t)
	stats, err := NewLibraryStatsService().GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Summary.VideoCount != 0 || stats.Summary.WatchedPercent != 0 || len(stats.WatchHeatmap) != 0 {
		t.Fatalf("empty stats=%#v", stats)
	}
}
