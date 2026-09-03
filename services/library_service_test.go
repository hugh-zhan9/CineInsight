package services

import (
	"fmt"
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

func TestLibraryFilterRestrictsResultsToPathPrefix(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	insideRoot := filepath.Join(root, "inside")
	insideNested := filepath.Join(insideRoot, "season-1")
	outsideRoot := filepath.Join(root, "outside")
	videos := []models.Video{
		{Name: "inside.mp4", Path: filepath.Join(insideRoot, "inside.mp4"), Directory: insideRoot},
		{Name: "nested.mp4", Path: filepath.Join(insideNested, "nested.mp4"), Directory: insideNested},
		{Name: "outside.mp4", Path: filepath.Join(outsideRoot, "outside.mp4"), Directory: outsideRoot},
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatalf("创建目录筛选夹具失败: %v", err)
	}

	filtered, err := (&VideoService{}).SearchLibraryVideos(LibraryFilter{PathPrefix: insideRoot}, 0, 0, 0, 20)
	if err != nil {
		t.Fatalf("目录范围筛选失败: %v", err)
	}
	if len(filtered) != 2 || filtered[0].ID != videos[1].ID || filtered[1].ID != videos[0].ID {
		t.Fatalf("目录范围筛选结果错误: %+v", filtered)
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

func TestRandomPlayHalfLifeReturnsOldPlaybackToHigherProbability(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	oldPlayedAt := now.Add(-180 * 24 * time.Hour)
	recentPlayedAt := now.Add(-24 * time.Hour)
	rows := []videoScoreRow{
		{ID: 1, PlayCount: 5, LastPlayedAt: &oldPlayedAt},
		{ID: 2, PlayCount: 2, LastPlayedAt: &recentPlayedAt},
	}
	weights, total := randomSelectionWeights(rows, 2, 90, now)
	if !(weights[0] > weights[1]) {
		t.Fatalf("久未播放的视频应获得更高选择权重: weights=%v", weights)
	}
	if math.Abs(total-(weights[0]+weights[1])) > 1e-12 {
		t.Fatalf("总权重错误 total=%v weights=%v", total, weights)
	}
}

func TestRandomPlayHalfLifeZeroExactlyPreservesLegacyScores(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	oldPlayedAt := now.Add(-10 * 365 * 24 * time.Hour)
	rows := []videoScoreRow{
		{ID: 1, PlayCount: 1, RandomPlayCount: 2, LastPlayedAt: &oldPlayedAt},
		{ID: 2, PlayCount: 3, RandomPlayCount: 0, LastPlayedAt: nil},
	}
	weights, total := randomSelectionWeights(rows, 2, 0, now)
	legacyScores := []float64{4, 6}
	legacyMax := 6.0
	want := []float64{legacyMax - legacyScores[0] + 1, legacyMax - legacyScores[1] + 1}
	if weights[0] != want[0] || weights[1] != want[1] || total != want[0]+want[1] {
		t.Fatalf("半衰期为 0 必须精确保留旧算法 got=%v total=%v want=%v", weights, total, want)
	}
}

func TestPickRandomVideosSharesRandomPlayBoundariesAndLeavesStatsUntouched(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := &VideoService{}

	created := make([]models.Video, 0, 6)
	for index := 0; index < 6; index++ {
		path := fmt.Sprintf("%s/pick-%d.mp4", root, index)
		mustCreateFile(t, path)
		video := models.Video{
			Name:      fmt.Sprintf("pick-%d.mp4", index),
			Path:      path,
			Directory: root,
			// 只有偶数号是收藏，用来验证模式过滤和随机播放一致。
			IsFavorite: index%2 == 0,
		}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatalf("创建候选失败: %v", err)
		}
		created = append(created, video)
	}
	// 失效条目不该出现在取样里，和 PlayRandomVideoWithFilter 的边界保持一致。
	stalePath := root + "/stale.mp4"
	mustCreateFile(t, stalePath)
	stale := models.Video{Name: "stale.mp4", Path: stalePath, Directory: root, IsFavorite: true, IsStale: true}
	if err := database.DB.Create(&stale).Error; err != nil {
		t.Fatalf("创建失效候选失败: %v", err)
	}

	result, err := svc.PickRandomVideos(RandomPlayRequest{Mode: RandomPlayModeFavorites}, 10)
	if err != nil {
		t.Fatalf("随机取样失败: %v", err)
	}
	if len(result.Videos) != 3 {
		t.Fatalf("候选不足 10 条时应返回全部 3 条收藏，实际 %d", len(result.Videos))
	}
	seen := make(map[uint]struct{}, len(result.Videos))
	for _, video := range result.Videos {
		if _, duplicated := seen[video.ID]; duplicated {
			t.Fatalf("取样结果不能重复: id=%d", video.ID)
		}
		seen[video.ID] = struct{}{}
		if video.ID == stale.ID {
			t.Fatalf("失效视频不应进入取样结果")
		}
		if !video.IsFavorite {
			t.Fatalf("收藏模式取样命中了非收藏视频: id=%d", video.ID)
		}
	}
	if result.SelectionReason != randomModeReason(RandomPlayModeFavorites) {
		t.Fatalf("取样理由应与随机播放一致: %s", result.SelectionReason)
	}

	// 取样只挑不播，播放统计必须原样不动。
	for _, video := range created {
		stats := previewStatsSnapshot(t, video.ID)
		if stats.RandomPlayCount != 0 || stats.PlayCount != 0 || stats.LastPlayedAt != nil {
			t.Fatalf("随机取样不应写播放统计: id=%d stats=%+v", video.ID, stats)
		}
	}

	excluded, err := svc.PickRandomVideos(RandomPlayRequest{
		Mode:       RandomPlayModeFavorites,
		ExcludeIDs: []uint{created[0].ID, created[2].ID, created[4].ID},
	}, 10)
	if err != nil {
		t.Fatalf("排除取样失败: %v", err)
	}
	if len(excluded.Videos) != 0 || excluded.ReasonCode != "no_filtered_videos" {
		t.Fatalf("排除全部候选后应返回空集: %+v", excluded)
	}
}

func TestPickRandomVideosCapsCountAndRejectsInvalidCount(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := &VideoService{}

	for index := 0; index < 12; index++ {
		path := fmt.Sprintf("%s/cap-%d.mp4", root, index)
		mustCreateFile(t, path)
		video := models.Video{Name: fmt.Sprintf("cap-%d.mp4", index), Path: path, Directory: root}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatalf("创建候选失败: %v", err)
		}
	}

	result, err := svc.PickRandomVideos(RandomPlayRequest{}, 10)
	if err != nil {
		t.Fatalf("随机取样失败: %v", err)
	}
	if len(result.Videos) != 10 {
		t.Fatalf("候选充足时应恰好返回 10 条，实际 %d", len(result.Videos))
	}

	if _, err := svc.PickRandomVideos(RandomPlayRequest{}, 0); err == nil {
		t.Fatalf("条数为 0 应被拒绝")
	}
	if _, err := svc.PickRandomVideos(RandomPlayRequest{}, RandomPickMaxCount+1); err == nil {
		t.Fatalf("超过上限的条数应被拒绝")
	}
	if _, err := svc.PickRandomVideos(RandomPlayRequest{Mode: "nope"}, 10); err == nil {
		t.Fatalf("非法随机模式应被拒绝")
	}
}

func TestWeightedSampleWithoutReplacementCoversWholePoolWithoutDuplicates(t *testing.T) {
	weights := []float64{1, 5, 2, 8}
	total := 16.0
	picked := weightedSampleWithoutReplacement(weights, total, 10)
	if len(picked) != len(weights) {
		t.Fatalf("请求条数超过候选数时应取完整池，实际 %d", len(picked))
	}
	seen := make(map[int]struct{}, len(picked))
	for _, index := range picked {
		if _, duplicated := seen[index]; duplicated {
			t.Fatalf("抽样下标重复: %v", picked)
		}
		seen[index] = struct{}{}
	}
	if len(weightedSampleWithoutReplacement(weights, total, 1)) != 1 {
		t.Fatalf("请求 1 条时只应抽一条")
	}
	if len(weightedSampleWithoutReplacement(nil, 0, 3)) != 0 {
		t.Fatalf("空候选池应抽不到任何条目")
	}
}

func TestGetVideosByIDsPreservesOrderAndSkipsMissing(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := &VideoService{}

	tag := models.Tag{Name: "取样标签"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	ids := make([]uint, 0, 3)
	for index := 0; index < 3; index++ {
		path := fmt.Sprintf("%s/order-%d.mp4", root, index)
		mustCreateFile(t, path)
		video := models.Video{Name: fmt.Sprintf("order-%d.mp4", index), Path: path, Directory: root}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatalf("创建视频失败: %v", err)
		}
		ids = append(ids, video.ID)
	}
	if err := database.DB.Model(&models.Video{ID: ids[1]}).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("挂标签失败: %v", err)
	}
	if err := database.DB.Delete(&models.Video{}, ids[2]).Error; err != nil {
		t.Fatalf("删除视频失败: %v", err)
	}

	got, err := svc.GetVideosByIDs([]uint{ids[1], 0, ids[2], ids[0], ids[1]})
	if err != nil {
		t.Fatalf("按 ID 查询失败: %v", err)
	}
	if len(got) != 2 || got[0].ID != ids[1] || got[1].ID != ids[0] {
		t.Fatalf("应按传入顺序去重返回并跳过已删除条目: %+v", got)
	}
	if len(got[0].Tags) != 1 || got[0].Tags[0].ID != tag.ID {
		t.Fatalf("按 ID 查询应带出标签: %+v", got[0].Tags)
	}
	empty, err := svc.GetVideosByIDs(nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空 ID 列表应返回空结果: %v %v", empty, err)
	}
}

func TestGetLibraryCountsCountsOnlyActiveRows(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()

	counts, err := svc.GetLibraryCounts()
	if err != nil {
		t.Fatalf("空库计数失败: %v", err)
	}
	if counts.VideoCount != 0 || counts.ImageCount != 0 {
		t.Fatalf("空库应返回 0/0，实际 %+v", counts)
	}

	ids := make([]uint, 0, 3)
	for index := 0; index < 3; index++ {
		path := fmt.Sprintf("%s/count-%d.mp4", root, index)
		mustCreateFile(t, path)
		video := models.Video{Name: fmt.Sprintf("count-%d.mp4", index), Path: path, Directory: root}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatalf("创建视频失败: %v", err)
		}
		ids = append(ids, video.ID)
	}
	image := models.Image{Name: "one.jpg", Path: root + "/one.jpg", Directory: root}
	if err := database.DB.Create(&image).Error; err != nil {
		t.Fatalf("创建图片失败: %v", err)
	}
	// 软删除的行不该计入头部总数。
	if err := database.DB.Delete(&models.Video{}, ids[0]).Error; err != nil {
		t.Fatalf("软删除失败: %v", err)
	}

	counts, err = svc.GetLibraryCounts()
	if err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if counts.VideoCount != 2 || counts.ImageCount != 1 {
		t.Fatalf("期望 2 视频 1 图片，实际 %+v", counts)
	}
}

func TestCountLibraryVideosMatchesTheListedPage(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()

	favoriteIDs := 0
	for index := 0; index < 7; index++ {
		path := fmt.Sprintf("%s/count-filter-%d.mp4", root, index)
		mustCreateFile(t, path)
		video := models.Video{
			Name:       fmt.Sprintf("count-filter-%d.mp4", index),
			Path:       path,
			Directory:  root,
			Size:       int64(index+1) * 1000,
			IsFavorite: index%3 == 0,
		}
		if video.IsFavorite {
			favoriteIDs++
		}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatalf("创建视频失败: %v", err)
		}
	}

	total, err := svc.CountLibraryVideos(LibraryFilter{})
	if err != nil {
		t.Fatalf("全库计数失败: %v", err)
	}
	if total != 7 {
		t.Fatalf("全库计数应为 7，实际 %d", total)
	}

	filter := LibraryFilter{SmartView: LibraryViewFavorites}
	filtered, err := svc.CountLibraryVideos(filter)
	if err != nil {
		t.Fatalf("筛选计数失败: %v", err)
	}
	if filtered != int64(favoriteIDs) {
		t.Fatalf("收藏计数应为 %d，实际 %d", favoriteIDs, filtered)
	}

	// 计数必须和真正翻完页数出来的条数一致，否则结果条会骗人。
	listed := 0
	var cursor *LibraryVideoCursor
	for {
		page, err := svc.SearchLibraryVideoPage(filter, cursor, 1)
		if err != nil {
			t.Fatalf("翻页失败: %v", err)
		}
		listed += len(page.Videos)
		if page.NextCursor == nil {
			break
		}
		cursor = page.NextCursor
	}
	if int64(listed) != filtered {
		t.Fatalf("计数 %d 与翻页得到的 %d 不一致", filtered, listed)
	}

	if _, err := svc.CountLibraryVideos(LibraryFilter{SmartView: "nope"}); err == nil {
		t.Fatalf("非法智能视图应被拒绝")
	}
}

// 点赞与收藏在主片库这一侧必须完全同构：各自一个真实列、各自一个智能视图。
// 早先点赞走的是一个自动标签，用户裁决改成列（2026-09-01）。
func TestLikedSmartViewMirrorsFavorites(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()

	create := func(name string, favorite, liked bool) models.Video {
		path := root + "/" + name
		mustCreateFile(t, path)
		video := models.Video{Name: name, Path: path, Directory: root, IsFavorite: favorite, IsLiked: liked}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatalf("建视频失败: %v", err)
		}
		return video
	}
	likedOnly := create("liked.mp4", false, true)
	favoriteOnly := create("favorite.mp4", true, false)
	both := create("both.mp4", true, true)
	create("plain.mp4", false, false)

	idsOf := func(view string) []uint {
		page, err := svc.SearchLibraryVideoPage(LibraryFilter{SmartView: view}, nil, 50)
		if err != nil {
			t.Fatalf("查询 %s 失败: %v", view, err)
		}
		ids := make([]uint, 0, len(page.Videos))
		for _, video := range page.Videos {
			ids = append(ids, video.ID)
		}
		return ids
	}
	contains := func(ids []uint, want uint) bool {
		for _, id := range ids {
			if id == want {
				return true
			}
		}
		return false
	}

	liked := idsOf(LibraryViewLiked)
	if len(liked) != 2 || !contains(liked, likedOnly.ID) || !contains(liked, both.ID) {
		t.Fatalf("点赞视图应只含点赞过的两条: %v", liked)
	}
	if contains(liked, favoriteOnly.ID) {
		t.Fatalf("点赞视图不应把只收藏的算进来: %v", liked)
	}

	favorites := idsOf(LibraryViewFavorites)
	if len(favorites) != 2 || contains(favorites, likedOnly.ID) {
		t.Fatalf("收藏视图不应受点赞影响: %v", favorites)
	}

	// 计数口径要与列表一致，否则结果条会骗人。
	count, err := svc.CountLibraryVideos(LibraryFilter{SmartView: LibraryViewLiked})
	if err != nil {
		t.Fatalf("点赞计数失败: %v", err)
	}
	if count != 2 {
		t.Fatalf("点赞计数应为 2，实际 %d", count)
	}
}

// 改窄扫描根后，落在范围外的旧记录不该再出现在片库视图里；把根改回去，
// 同一条记录要自己回来（记录本身不删）。
func TestLibraryViewsScopeResultsToConfiguredScanRoots(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	keptDir := filepath.Join(root, "Ellieli-Collection")
	droppedDir := filepath.Join(root, "xxxx")
	videos := []models.Video{
		{Name: "kept.mp4", Path: filepath.Join(keptDir, "kept.mp4"), Directory: keptDir},
		{Name: "dropped.mp4", Path: filepath.Join(droppedDir, "dropped.mp4"), Directory: droppedDir},
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatalf("创建扫描范围夹具失败: %v", err)
	}
	svc := &VideoService{}

	// 根还是整个卷时，两条都在范围内。
	wideDir := models.ScanDirectory{Path: root}
	if err := database.DB.Create(&wideDir).Error; err != nil {
		t.Fatalf("创建扫描目录失败: %v", err)
	}
	total, err := svc.CountLibraryVideos(LibraryFilter{})
	if err != nil || total != 2 {
		t.Fatalf("宽根下应计入两条 total=%d err=%v", total, err)
	}

	// 把根改窄到子目录：范围外那条从列表和计数里消失，但记录还在库里。
	if err := database.DB.Model(&models.ScanDirectory{}).Where("id = ?", wideDir.ID).Update("path", keptDir).Error; err != nil {
		t.Fatalf("改窄扫描目录失败: %v", err)
	}
	page, err := svc.SearchLibraryVideoPage(LibraryFilter{}, nil, 20)
	if err != nil {
		t.Fatalf("窄根下查询失败: %v", err)
	}
	if len(page.Videos) != 1 || page.Videos[0].ID != videos[0].ID {
		t.Fatalf("窄根下只应剩范围内那条: %+v", page.Videos)
	}
	total, err = svc.CountLibraryVideos(LibraryFilter{})
	if err != nil || total != 1 {
		t.Fatalf("窄根下计数应为 1 total=%d err=%v", total, err)
	}
	var stillThere models.Video
	if err := database.DB.First(&stillThere, videos[1].ID).Error; err != nil {
		t.Fatalf("范围外记录不该被删除: %v", err)
	}

	// 根恢复：范围外那条自己回来，不需要重新扫描。
	if err := database.DB.Model(&models.ScanDirectory{}).Where("id = ?", wideDir.ID).Update("path", root).Error; err != nil {
		t.Fatalf("恢复扫描目录失败: %v", err)
	}
	total, err = svc.CountLibraryVideos(LibraryFilter{})
	if err != nil || total != 2 {
		t.Fatalf("恢复宽根后应重新计入两条 total=%d err=%v", total, err)
	}
}

// 一个扫描目录都没配置时不做范围裁剪：此时没有"范围"可言，不能把整库藏起来。
func TestLibraryViewsSkipScopeWhenNoScanDirectoryConfigured(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	video := models.Video{Name: "orphan.mp4", Path: filepath.Join(dir, "orphan.mp4"), Directory: dir}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	total, err := (&VideoService{}).CountLibraryVideos(LibraryFilter{})
	if err != nil || total != 1 {
		t.Fatalf("未配置扫描目录时不应裁剪 total=%d err=%v", total, err)
	}
}

// 卷根（"/"）做扫描根：filepath.Clean 会保留尾部分隔符，前缀拼接必须不重复加，
// 否则整库都会被裁掉。
func TestLibraryViewsScopeHandlesVolumeRootWithoutHidingEverything(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := "/Volumes/think plus"
	video := models.Video{Name: "root.mp4", Path: filepath.Join(dir, "root.mp4"), Directory: dir}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: "/"}).Error; err != nil {
		t.Fatalf("创建卷根扫描目录失败: %v", err)
	}

	total, err := (&VideoService{}).CountLibraryVideos(LibraryFilter{})
	if err != nil || total != 1 {
		t.Fatalf("卷根应覆盖其下全部视频 total=%d err=%v", total, err)
	}
}

// 顶栏计数和列表结果条必须是同一个口径，否则两个数字并排显示却对不上。
func TestLibraryCountsMatchScopedListing(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	insideDir := filepath.Join(root, "inside")
	outsideDir := filepath.Join(root, "outside")
	videos := []models.Video{
		{Name: "inside.mp4", Path: filepath.Join(insideDir, "inside.mp4"), Directory: insideDir},
		{Name: "outside.mp4", Path: filepath.Join(outsideDir, "outside.mp4"), Directory: outsideDir},
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: insideDir}).Error; err != nil {
		t.Fatalf("创建扫描目录失败: %v", err)
	}
	svc := &VideoService{}

	counts, err := svc.GetLibraryCounts()
	if err != nil {
		t.Fatalf("读取库计数失败: %v", err)
	}
	listed, err := svc.CountLibraryVideos(LibraryFilter{})
	if err != nil {
		t.Fatalf("读取列表计数失败: %v", err)
	}
	if counts.VideoCount != listed {
		t.Fatalf("顶栏计数与列表计数不一致 header=%d list=%d", counts.VideoCount, listed)
	}
	if counts.VideoCount != 1 {
		t.Fatalf("范围外记录不该计入 header=%d", counts.VideoCount)
	}
}
