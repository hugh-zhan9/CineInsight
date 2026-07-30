package services

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestLibraryStateUpdatesAreAdditiveAndIdempotent(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "library.mp4", Path: "/tmp/library.mp4", Directory: "/tmp", Duration: 100}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	svc := &VideoService{}

	updated, err := svc.SetVideoFavorite(video.ID, true)
	if err != nil || !updated.IsFavorite {
		t.Fatalf("收藏失败 video=%+v err=%v", updated, err)
	}
	updated, err = svc.UpdateVideoWatchProgress(video.ID, 125, false)
	if err != nil || updated.WatchPositionSeconds != 100 || updated.IsWatched {
		t.Fatalf("进度应夹紧且不自动已看 video=%+v err=%v", updated, err)
	}
	updated, err = svc.UpdateVideoWatchProgress(video.ID, 99, true)
	if err != nil || !updated.IsWatched || updated.WatchedAt == nil || updated.WatchPositionSeconds != 100 {
		t.Fatalf("完成播放应标记已看 video=%+v err=%v", updated, err)
	}
	updated, err = svc.SetVideoWatched(video.ID, false)
	if err != nil || updated.IsWatched || updated.WatchedAt != nil || updated.WatchPositionSeconds != 100 {
		t.Fatalf("标记未看不应清空位置 video=%+v err=%v", updated, err)
	}
	if _, err := svc.UpdateVideoWatchProgress(video.ID, math.NaN(), false); err == nil {
		t.Fatalf("NaN 进度应被拒绝")
	}
}

func TestLibraryFiltersCoverBuiltInViewsAndSubtitleKeyword(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Now()
	root := t.TempDir()
	videos := []models.Video{
		{Name: "favorite.mp4", Path: filepath.Join(root, "favorite.mp4"), Directory: root, IsFavorite: true, CreatedAt: now},
		{Name: "continue.mp4", Path: filepath.Join(root, "continue.mp4"), Directory: root, WatchPositionSeconds: 12, CreatedAt: now},
		{Name: "watched.mp4", Path: filepath.Join(root, "watched.mp4"), Directory: root, IsWatched: true, CreatedAt: now.Add(-60 * 24 * time.Hour)},
	}
	for index := range videos {
		if err := database.DB.Create(&videos[index]).Error; err != nil {
			t.Fatalf("创建视频失败: %v", err)
		}
	}
	for _, video := range videos {
		if err := os.WriteFile(video.Path, []byte("video"), 0644); err != nil {
			t.Fatalf("创建视频夹具失败: %v", err)
		}
	}
	continueSRT := "1\n00:00:01,000 --> 00:00:02,000\nA Unique Subtitle Phrase literal 100%_done\n"
	decoySRT := "1\n00:00:03,000 --> 00:00:04,000\nliteral 100XXdone\n"
	if err := os.WriteFile(filepath.Join(root, "continue.srt"), []byte(continueSRT), 0644); err != nil {
		t.Fatalf("创建字幕夹具失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "watched.srt"), []byte(decoySRT), 0644); err != nil {
		t.Fatalf("创建干扰字幕夹具失败: %v", err)
	}
	svc := &VideoService{}

	favorites, err := svc.SearchLibraryVideos(LibraryFilter{SmartView: LibraryViewFavorites}, 0, 0, 0, 20)
	if err != nil || len(favorites) != 1 || favorites[0].ID != videos[0].ID {
		t.Fatalf("收藏视图错误 videos=%+v err=%v", favorites, err)
	}
	continuing, err := svc.SearchLibraryVideos(LibraryFilter{SmartView: LibraryViewContinueWatching}, 0, 0, 0, 20)
	if err != nil || len(continuing) != 1 || continuing[0].ID != videos[1].ID {
		t.Fatalf("继续观看视图错误 videos=%+v err=%v", continuing, err)
	}
	subtitles, err := svc.SearchLibraryVideos(LibraryFilter{SearchMode: LibrarySearchModeSubtitle, Keyword: "unique subtitle"}, 0, 0, 0, 20)
	if err != nil || len(subtitles) != 1 || subtitles[0].ID != videos[1].ID {
		t.Fatalf("字幕过滤错误 videos=%+v err=%v", subtitles, err)
	}
	literalSubtitles, err := svc.SearchLibraryVideos(LibraryFilter{SearchMode: LibrarySearchModeSubtitle, Keyword: "100%_done"}, 0, 0, 0, 20)
	if err != nil || len(literalSubtitles) != 1 || literalSubtitles[0].ID != videos[1].ID {
		t.Fatalf("字幕通配符应按字面量匹配 videos=%+v err=%v", literalSubtitles, err)
	}
	hits, err := svc.GetLibrarySubtitleHits("100%_done", []uint{videos[2].ID, videos[1].ID})
	if err != nil || len(hits) != 1 || hits[0].VideoID != videos[1].ID || hits[0].Segment.StartTimeMs != 1000 {
		t.Fatalf("当前页字幕命中补充错误 hits=%+v err=%v", hits, err)
	}
	withoutSubtitles, err := svc.SearchLibraryVideos(LibraryFilter{SmartView: LibraryViewNoSubtitle}, 0, 0, 0, 20)
	if err != nil || len(withoutSubtitles) != 1 || withoutSubtitles[0].ID != videos[0].ID {
		t.Fatalf("零片段索引状态应进入无字幕视图 videos=%+v err=%v", withoutSubtitles, err)
	}
}

func TestRecentlyPlayedWithFilterPaginatesAfterDatabaseFiltering(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Now()
	videos := []models.Video{
		{Name: "old-favorite.mp4", Path: "/tmp/old-favorite.mp4", Directory: "/tmp", IsFavorite: true, LastPlayedAt: timePointer(now.Add(-time.Hour))},
		{Name: "new-other.mp4", Path: "/tmp/new-other.mp4", Directory: "/tmp", LastPlayedAt: timePointer(now)},
		{Name: "new-favorite.mp4", Path: "/tmp/new-favorite.mp4", Directory: "/tmp", IsFavorite: true, LastPlayedAt: timePointer(now.Add(-time.Minute))},
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatalf("创建最近播放视频失败: %v", err)
	}
	svc := &VideoService{}
	filter := LibraryFilter{SmartView: LibraryViewFavorites}
	first, err := svc.ListRecentlyPlayedWithFilter(filter, "", 0, 1)
	if err != nil || len(first) != 1 || first[0].ID != videos[2].ID {
		t.Fatalf("最近播放首个筛选页错误 videos=%+v err=%v", first, err)
	}
	second, err := svc.ListRecentlyPlayedWithFilter(filter, first[0].LastPlayedAt.Format(time.RFC3339Nano), first[0].ID, 1)
	if err != nil || len(second) != 1 || second[0].ID != videos[0].ID {
		t.Fatalf("最近播放第二个筛选页错误 videos=%+v err=%v", second, err)
	}
}

func TestNoSubtitleViewSynchronizesFilesystemBeforeFirstQuery(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	withSubtitle := models.Video{Name: "with-subtitle.mp4", Path: filepath.Join(root, "with-subtitle.mp4"), Directory: root}
	withoutSubtitle := models.Video{Name: "without-subtitle.mp4", Path: filepath.Join(root, "without-subtitle.mp4"), Directory: root}
	for _, video := range []*models.Video{&withSubtitle, &withoutSubtitle} {
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatalf("创建无字幕视图夹具失败: %v", err)
		}
	}
	for _, path := range []string{withSubtitle.Path, withoutSubtitle.Path} {
		if err := os.WriteFile(path, []byte("video"), 0644); err != nil {
			t.Fatalf("创建视频夹具失败: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "with-subtitle.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nindexed on first no-subtitle query\n"), 0644); err != nil {
		t.Fatalf("创建字幕夹具失败: %v", err)
	}

	videos, err := (&VideoService{}).SearchLibraryVideos(LibraryFilter{SmartView: LibraryViewNoSubtitle}, 0, 0, 0, 20)
	if err != nil || len(videos) != 1 || videos[0].ID != withoutSubtitle.ID {
		t.Fatalf("无字幕视图首次查询前应同步磁盘字幕 videos=%+v err=%v", videos, err)
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func TestSavedLibraryViewsPersistAndRejectDuplicateActiveName(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	input := SavedLibraryViewInput{
		Name:          " 我的收藏 ",
		LibraryFilter: LibraryFilter{SearchMode: LibrarySearchModeFile, SmartView: LibraryViewFavorites, TagIDs: []uint{3, 3, 1}},
	}
	created, err := svc.SaveLibraryView(input)
	if err != nil {
		t.Fatalf("保存视图失败: %v", err)
	}
	if created.Name != "我的收藏" || created.TagIDsJSON != "[1,3]" {
		t.Fatalf("保存视图未规范化: %+v", created)
	}
	if _, err := svc.SaveLibraryView(input); err == nil {
		t.Fatalf("活跃同名视图应被拒绝")
	}
	views, err := svc.ListSavedLibraryViews()
	if err != nil || len(views) != 1 || views[0].ID != created.ID {
		t.Fatalf("列出视图失败 views=%+v err=%v", views, err)
	}
	if err := svc.DeleteSavedLibraryView(created.ID); err != nil {
		t.Fatalf("删除视图失败: %v", err)
	}
	if _, err := svc.SaveLibraryView(input); err != nil {
		t.Fatalf("软删除后应允许复用名称: %v", err)
	}
	if _, err := svc.SaveLibraryView(SavedLibraryViewInput{
		Name: "无效范围", LibraryFilter: LibraryFilter{MinSize: 100, MaxSize: 10},
	}); err == nil {
		t.Fatalf("倒置的筛选范围应被拒绝")
	}
}

func TestFilteredRandomPlayHonorsViewModeStaleAndRecentExclusions(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	eligiblePath := root + "/eligible.mp4"
	stalePath := root + "/stale.mp4"
	mustCreateFile(t, eligiblePath)
	mustCreateFile(t, stalePath)
	if err := os.WriteFile(root+"/eligible.srt", []byte("1\n00:00:01,000 --> 00:00:02,000\nliteral 100%_done\n"), 0644); err != nil {
		t.Fatalf("创建随机播放字幕夹具失败: %v", err)
	}
	eligible := models.Video{Name: "eligible.mp4", Path: eligiblePath, Directory: root, IsFavorite: true}
	stale := models.Video{Name: "stale.mp4", Path: stalePath, Directory: root, IsFavorite: true, IsStale: true}
	if err := database.DB.Create(&eligible).Error; err != nil {
		t.Fatalf("创建候选失败: %v", err)
	}
	if err := database.DB.Create(&stale).Error; err != nil {
		t.Fatalf("创建失效候选失败: %v", err)
	}
	oldOpen := openWithDefaultFn
	openWithDefaultFn = func(path string, isDir bool) error { return nil }
	defer func() { openWithDefaultFn = oldOpen }()

	svc := &VideoService{}
	result, err := svc.PlayRandomVideoWithFilter(RandomPlayRequest{Filter: LibraryFilter{
		SearchMode: LibrarySearchModeSubtitle, Keyword: "100%_done",
	}})
	if err != nil || !result.DispatchSucceeded || result.Video == nil || result.Video.ID != eligible.ID {
		t.Fatalf("字幕筛选随机选择错误 result=%+v err=%v", result, err)
	}
	result, err = svc.PlayRandomVideoWithFilter(RandomPlayRequest{Mode: RandomPlayModeFavorites})
	if err != nil || !result.DispatchSucceeded || result.Video == nil || result.Video.ID != eligible.ID {
		t.Fatalf("筛选随机选择错误 result=%+v err=%v", result, err)
	}
	result, err = svc.PlayRandomVideoWithFilter(RandomPlayRequest{
		Mode: RandomPlayModeFavorites, ExcludeIDs: []uint{eligible.ID, eligible.ID},
	})
	if err != nil || result.DispatchSucceeded || result.ReasonCode != "no_filtered_videos" {
		t.Fatalf("排除后应明确空集 result=%+v err=%v", result, err)
	}
}
