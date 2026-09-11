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
	// 热力图读账本：给近期播放的那条补一条事件，一年窗口之外的那条也补上，
	// 用来证明窗口过滤仍然生效。
	for _, item := range []struct {
		videoIndex int
		playedAt   time.Time
	}{{0, recent}, {1, old}} {
		if err := database.DB.Create(&models.PlayEvent{
			VideoID: videos[item.videoIndex].ID, PlayedAt: item.playedAt,
			Source: models.PlayEventSourceDesktopPlay,
		}).Error; err != nil {
			t.Fatalf("创建播放事件失败: %v", err)
		}
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
	// 一年窗口之外的事件不进热力图，但仍计入总数。
	if stats.TotalPlayEvents != 2 || stats.PlaysBySource[models.PlayEventSourceDesktopPlay] != 2 {
		t.Fatalf("play events total=%d bySource=%#v", stats.TotalPlayEvents, stats.PlaysBySource)
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
	if stats.Summary.VideoCount != 0 || stats.Summary.ViewedCount != 0 || stats.Summary.ViewedPercent != 0 || stats.Summary.WatchedPercent != 0 || len(stats.WatchHeatmap) != 0 {
		t.Fatalf("empty stats=%#v", stats)
	}
}

func TestLibraryStatsCountsRecentlyAddedVideos(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()

	fresh := models.Video{Name: "fresh.mp4", Path: root + "/fresh.mp4", Directory: root, Size: 10}
	if err := database.DB.Create(&fresh).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	old := models.Video{Name: "old.mp4", Path: root + "/old.mp4", Directory: root, Size: 10}
	if err := database.DB.Create(&old).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	// created_at 由 GORM 自动写入，这里显式改成 60 天前。
	if err := database.DB.Model(&models.Video{}).Where("id = ?", old.ID).
		UpdateColumn("created_at", time.Now().AddDate(0, 0, -60)).Error; err != nil {
		t.Fatalf("回拨创建时间失败: %v", err)
	}

	stats, err := NewLibraryStatsService().GetStats()
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if stats.Summary.VideoCount != 2 {
		t.Fatalf("总数应为 2，实际 %d", stats.Summary.VideoCount)
	}
	if stats.Summary.RecentAddedCount != 1 {
		t.Fatalf("最近 30 天新增应为 1，实际 %d", stats.Summary.RecentAddedCount)
	}
}

// 这条测试必须在两个后端上都跑。只跑 SQLite 恰好会掩盖它要防的问题：
// CAST(played_at AS DATE) 在 SQLite 上返回 "2026"，一整年落进同一个格子。
func TestLibraryWatchHeatmapGroupsByLocalDateOnBothBackends(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	now := time.Now()

	// 同一天两条、另一天一条，外加一条落在一年窗口之外。
	day := func(offsetDays int, hour int) time.Time {
		base := now.AddDate(0, 0, -offsetDays)
		return time.Date(base.Year(), base.Month(), base.Day(), hour, 30, 0, 0, time.Local)
	}
	fixtures := []struct {
		name     string
		playedAt time.Time
	}{
		{"a.mp4", day(2, 1)},
		{"b.mp4", day(2, 23)},
		{"c.mp4", day(5, 12)},
		{"old.mp4", now.AddDate(-2, 0, 0)},
	}
	for _, item := range fixtures {
		playedAt := item.playedAt
		video := models.Video{
			Name: item.name, Path: root + "/" + item.name, Directory: root,
			LastPlayedAt: &playedAt,
		}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatalf("创建视频失败: %v", err)
		}
		// 热力图读的是账本，不再是 videos.last_played_at。
		if err := database.DB.Create(&models.PlayEvent{
			VideoID: video.ID, PlayedAt: playedAt, Source: models.PlayEventSourceDesktopPlay,
		}).Error; err != nil {
			t.Fatalf("创建播放事件失败: %v", err)
		}
	}

	heatmap, err := libraryWatchHeatmap(now)
	if err != nil {
		t.Fatalf("统计热力图失败: %v", err)
	}

	got := map[string]int64{}
	for _, entry := range heatmap {
		// 分组键必须是完整的 YYYY-MM-DD。SQLite 上的旧写法会返回 "2026"，
		// 长度断言直接把那种退化钉死。
		if len(entry.Date) != len("2006-01-02") {
			t.Fatalf("分组键必须是完整日期，实际 %q", entry.Date)
		}
		got[entry.Date] = entry.Count
	}

	twoDaysAgo := day(2, 0).Format("2006-01-02")
	fiveDaysAgo := day(5, 0).Format("2006-01-02")
	if got[twoDaysAgo] != 2 {
		t.Fatalf("同一天的两条应合并计数：%+v", got)
	}
	if got[fiveDaysAgo] != 1 {
		t.Fatalf("另一天应单独成组：%+v", got)
	}
	if len(heatmap) != 2 {
		t.Fatalf("一年窗口之外的记录不应入组：%+v", heatmap)
	}
	// 顺序必须递增，前端按这个顺序填坐标轴。
	if heatmap[0].Date > heatmap[1].Date {
		t.Fatalf("分组应按日期升序：%+v", heatmap)
	}
}

func TestLibraryStatsViewedCoverageDeduplicatesAllEvidence(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Now()
	videos := []models.Video{
		{Name: "marked", Path: "/marked", IsWatched: true},
		{Name: "desktop", Path: "/desktop", PlayCount: 3},
		{Name: "random", Path: "/random", RandomPlayCount: 2},
		{Name: "legacy", Path: "/legacy", LastPlayedAt: &now},
		{Name: "progress", Path: "/progress", WatchPositionSeconds: 12},
		{Name: "event-only", Path: "/event-only"},
		{Name: "unseen", Path: "/unseen"},
		// A progress callback at zero does not prove that any content was watched.
		{Name: "zero-progress", Path: "/zero-progress", WatchProgressUpdatedAt: &now},
		{Name: "deleted", Path: "/deleted", IsWatched: true, PlayCount: 5, WatchPositionSeconds: 20},
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 1, 1, 5, 5, 8} {
		if err := database.DB.Create(&models.PlayEvent{VideoID: videos[i].ID, Source: models.PlayEventSourceDesktopPlay, PlayedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Delete(&videos[8]).Error; err != nil {
		t.Fatal(err)
	}
	stats, err := NewLibraryStatsService().GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Summary.VideoCount != 8 || stats.Summary.ViewedCount != 6 || stats.Summary.ViewedPercent != 75 {
		t.Fatalf("coverage must count each active video once: %+v", stats.Summary)
	}
	if stats.Summary.WatchedCount != 1 || stats.Summary.WatchedPercent != 12.5 {
		t.Fatalf("completion marks changed: %+v", stats.Summary)
	}
	if stats.TotalPlayEvents != 6 {
		t.Fatalf("historical ledger scope changed: %d", stats.TotalPlayEvents)
	}
	var watched int64
	if err := database.DB.Model(&models.Video{}).Where("is_watched = ?", true).Count(&watched).Error; err != nil || watched != 1 {
		t.Fatalf("read-only stats mutated watched flags: %d %v", watched, err)
	}
}
