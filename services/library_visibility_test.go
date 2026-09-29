package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// libVisIDs 用 applyLibraryFilter 取命中的视频 ID（升序），与列表、计数、随机共用同一条边界。
func libVisIDs(t *testing.T, filter LibraryFilter) []uint {
	t.Helper()
	query, err := applyLibraryFilter(database.DB.Model(&models.Video{}), filter, time.Now())
	if err != nil {
		t.Fatalf("applyLibraryFilter: %v", err)
	}
	ids := []uint{}
	if err := query.Order("videos.id ASC").Pluck("videos.id", &ids).Error; err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	return ids
}

// libVisVideo 创建一条视频；path 必须以 "/x.mp4" 结尾，目录取其父路径。
func libVisVideo(t *testing.T, path string, mutate func(*models.Video)) models.Video {
	t.Helper()
	dir := path[:len(path)-len("/x.mp4")]
	v := models.Video{Name: "x.mp4", Path: path, Directory: dir}
	if mutate != nil {
		mutate(&v)
	}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	return v
}

func libVisTag(t *testing.T, videoID, tagID uint) {
	t.Helper()
	if err := database.DB.Exec("INSERT INTO video_tags (video_id, tag_id) VALUES (?, ?)", videoID, tagID).Error; err != nil {
		t.Fatalf("挂标签失败: %v", err)
	}
}

func TestLIB01StaleViewShowsRecordsOutsideScanRootsOthersUnchanged(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.Create(&models.ScanDirectory{Path: "/lib/in"}).Error; err != nil {
		t.Fatal(err)
	}
	live := libVisVideo(t, "/lib/in/a/x.mp4", nil)
	removedRoot := libVisVideo(t, "/lib/gone/x.mp4", func(v *models.Video) {
		v.IsStale, v.StaleReason, v.IsFavorite = true, models.StaleReasonRemovedRoot, true
	})
	staleInside := libVisVideo(t, "/lib/in/b/x.mp4", func(v *models.Video) {
		v.IsStale, v.IsFavorite = true, true // 历史失效行：空原因
	})
	libVisVideo(t, "/lib/gone2/x.mp4", func(v *models.Video) { v.IsFavorite = true })
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", "/lib/in/blocked").Error; err != nil {
		t.Fatal(err)
	}
	libVisVideo(t, "/lib/in/blocked/x.mp4", func(v *models.Video) { v.IsStale = true })

	// 失效视图：根外的失效记录也出现；黑名单仍排除；非失效记录不出现。
	got := libVisIDs(t, LibraryFilter{SmartView: LibraryViewStale})
	if want := []uint{removedRoot.ID, staleInside.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("失效视图应含根外失效记录并排除黑名单 got=%v want=%v", got, want)
	}
	// 受保护：默认视图与收藏视图仍按扫描根裁剪、排除失效。
	if got := libVisIDs(t, LibraryFilter{}); !reflect.DeepEqual(got, []uint{live.ID}) {
		t.Fatalf("默认视图边界被改动: %v", got)
	}
	if got := libVisIDs(t, LibraryFilter{SmartView: LibraryViewFavorites}); len(got) != 0 {
		t.Fatalf("收藏视图不应含根外或失效记录: %v", got)
	}
	// stale_reason 精确匹配与 unknown。
	if got := libVisIDs(t, LibraryFilter{SmartView: LibraryViewStale, StaleReason: models.StaleReasonRemovedRoot}); !reflect.DeepEqual(got, []uint{removedRoot.ID}) {
		t.Fatalf("按原因筛选失败: %v", got)
	}
	if got := libVisIDs(t, LibraryFilter{SmartView: LibraryViewStale, StaleReason: StaleReasonUnknown}); !reflect.DeepEqual(got, []uint{staleInside.ID}) {
		t.Fatalf("unknown 应匹配空原因: %v", got)
	}
	// stale_reason 在非失效视图里被忽略，不误伤。
	if got := libVisIDs(t, LibraryFilter{StaleReason: models.StaleReasonRemovedRoot}); !reflect.DeepEqual(got, []uint{live.ID}) {
		t.Fatalf("非失效视图不应受 stale_reason 影响: %v", got)
	}
	if _, err := applyLibraryFilter(database.DB.Model(&models.Video{}), LibraryFilter{SmartView: LibraryViewStale, StaleReason: "bogus"}, time.Now()); err == nil {
		t.Fatalf("未知失效原因应被拒绝")
	}
	counts, err := (&VideoService{}).ListStaleReasonCounts()
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{models.StaleReasonRemovedRoot: 1, StaleReasonUnknown: 1}; !reflect.DeepEqual(counts, want) {
		t.Fatalf("失效原因计数 got=%v want=%v", counts, want)
	}
}

func TestLIB01StaleViewWithoutScanRootsStillExcludesBlacklist(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", "/lib/blocked").Error; err != nil {
		t.Fatal(err)
	}
	keep := libVisVideo(t, "/lib/ok/x.mp4", func(v *models.Video) { v.IsStale = true })
	libVisVideo(t, "/lib/blocked/x.mp4", func(v *models.Video) { v.IsStale = true })
	if got := libVisIDs(t, LibraryFilter{SmartView: LibraryViewStale}); !reflect.DeepEqual(got, []uint{keep.ID}) {
		t.Fatalf("got=%v", got)
	}
}

func TestLIB08HeaderAndInsightTotalsUseDefaultViewVisibility(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.Create(&models.ScanDirectory{Path: "/lib/in"}).Error; err != nil {
		t.Fatal(err)
	}
	libVisVideo(t, "/lib/in/a/x.mp4", func(v *models.Video) { v.Size, v.Height, v.IsWatched = 100, 1080, true })
	libVisVideo(t, "/lib/in/b/x.mp4", func(v *models.Video) { v.Size, v.Height = 50, 1080 })
	libVisVideo(t, "/lib/in/c/x.mp4", func(v *models.Video) { v.IsStale, v.Size = true, 999 })
	libVisVideo(t, "/lib/out/x.mp4", func(v *models.Video) { v.Size = 777 })

	counts, err := (&VideoService{}).GetLibraryCounts()
	if err != nil {
		t.Fatal(err)
	}
	listCount, err := (&VideoService{}).CountLibraryVideos(LibraryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if counts.VideoCount != 2 || counts.VideoCount != listCount {
		t.Fatalf("头部总数应等于默认视图列表数: header=%d list=%d", counts.VideoCount, listCount)
	}

	stats, err := NewLibraryStatsService().GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Summary.VideoCount != 2 || stats.Summary.TotalSize != 150 || stats.Summary.WatchedCount != 1 {
		t.Fatalf("洞察页摘要应只统计可见视频: %+v", stats.Summary)
	}
	total := int64(0)
	for _, b := range stats.StorageByDirectory {
		total += b.Count
	}
	if total != 2 {
		t.Fatalf("目录分布口径不一致: %+v", stats.StorageByDirectory)
	}
	for _, b := range stats.StorageByResolution {
		if b.Count != 2 || b.Bytes != 150 {
			t.Fatalf("分辨率分布口径不一致: %+v", b)
		}
	}
}

func TestPLAY07InsightTagAndRatingBucketsUseVisibleScope(t *testing.T) {
	setupVideoServiceTestDB(t)
	// 配置扫描根：根之外的非失效视频同样不可见，统计必须按扫描根裁剪。
	if err := database.DB.Create(&models.ScanDirectory{Path: "/lib/a"}).Error; err != nil {
		t.Fatal(err)
	}
	visible := libVisVideo(t, "/lib/a/x.mp4", func(v *models.Video) { v.Size = 10; v.PersonalRating = float64Pointer(7) })
	hidden := libVisVideo(t, "/lib/b/x.mp4", func(v *models.Video) {
		v.Size, v.IsStale, v.PersonalRating = 20, true, float64Pointer(7)
	})
	outsideRoots := libVisVideo(t, "/lib/z/x.mp4", func(v *models.Video) {
		v.Size, v.PersonalRating = 30, float64Pointer(7)
	})
	tag := models.Tag{Name: "口径", IsActive: true}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	libVisTag(t, visible.ID, tag.ID)
	libVisTag(t, hidden.ID, tag.ID)
	libVisTag(t, outsideRoots.ID, tag.ID)
	stats, err := NewLibraryStatsService().GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.StorageByTag) != 1 || stats.StorageByTag[0].Count != 1 || stats.StorageByTag[0].Bytes != 10 {
		t.Fatalf("标签分布应排除失效视频: %+v", stats.StorageByTag)
	}
	if len(stats.RatingDistribution) != 1 || stats.RatingDistribution[0].Count != 1 {
		t.Fatalf("评分分布应排除失效视频: %+v", stats.RatingDistribution)
	}
}

func TestMETA02PersonFilterUsesAndSemanticsAndSavedViewRoundTrips(t *testing.T) {
	setupVideoServiceTestDB(t)
	p1, p2 := models.Person{DisplayName: "甲"}, models.Person{DisplayName: "乙"}
	if err := database.DB.Create(&p1).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&p2).Error; err != nil {
		t.Fatal(err)
	}
	both := libVisVideo(t, "/lib/a/x.mp4", nil)
	onlyOne := libVisVideo(t, "/lib/b/x.mp4", nil)
	libVisVideo(t, "/lib/c/x.mp4", nil)
	for _, link := range []models.VideoPerson{{VideoID: both.ID, PersonID: p1.ID}, {VideoID: both.ID, PersonID: p2.ID}, {VideoID: onlyOne.ID, PersonID: p1.ID}} {
		if err := database.DB.Create(&link).Error; err != nil {
			t.Fatal(err)
		}
	}
	if got := libVisIDs(t, LibraryFilter{PersonIDs: []uint{p1.ID}}); !reflect.DeepEqual(got, []uint{both.ID, onlyOne.ID}) {
		t.Fatalf("单人物筛选: %v", got)
	}
	if got := libVisIDs(t, LibraryFilter{PersonIDs: []uint{p2.ID, p1.ID, p1.ID}}); !reflect.DeepEqual(got, []uint{both.ID}) {
		t.Fatalf("多人物应为 AND 语义: %v", got)
	}
	if got := libVisIDs(t, LibraryFilter{PersonIDs: []uint{}}); len(got) != 3 {
		t.Fatalf("空人物数组表示不筛: %v", got)
	}
	n, err := normalizeLibraryFilter(LibraryFilter{PersonIDs: []uint{9, 3, 9, 0}})
	if err != nil || !reflect.DeepEqual(n.PersonIDs, []uint{3, 9}) {
		t.Fatalf("规范化: %v err=%v", n.PersonIDs, err)
	}

	svc := &VideoService{}
	view, err := svc.SaveLibraryView(SavedLibraryViewInput{Name: "人物", LibraryFilter: LibraryFilter{PersonIDs: []uint{p2.ID, p1.ID}}})
	wantJSON := fmt.Sprintf("[%d,%d]", p1.ID, p2.ID)
	if err != nil || view.PersonIDsJSON != wantJSON {
		t.Fatalf("保存视图应写 person_ids_json: %+v err=%v", view, err)
	}
	empty, err := svc.SaveLibraryView(SavedLibraryViewInput{Name: "空"})
	if err != nil || empty.PersonIDsJSON != "[]" {
		t.Fatalf("空人物应存 []: %+v err=%v", empty, err)
	}
	views, err := svc.ListSavedLibraryViews()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range views {
		if v.ID == view.ID {
			found = v.PersonIDsJSON == wantJSON
		}
	}
	if !found {
		t.Fatalf("往返不一致: %+v", views)
	}
	// 往返的后半程：把保存的 JSON 解析回筛选条件再应用，命中与直接筛选一致。
	var restored []uint
	if err := json.Unmarshal([]byte(view.PersonIDsJSON), &restored); err != nil {
		t.Fatalf("person_ids_json 应可解析: %v", err)
	}
	if got := libVisIDs(t, LibraryFilter{PersonIDs: restored, SearchMode: view.SearchMode, SortMode: view.SortMode}); !reflect.DeepEqual(got, []uint{both.ID}) {
		t.Fatalf("解析回筛选再应用应命中同样的视频: %v", got)
	}
}

func TestMETA10UntaggedViewIgnoresAutomaticTags(t *testing.T) {
	setupVideoServiceTestDB(t)
	bare := libVisVideo(t, "/lib/a/x.mp4", nil)
	autoOnly := libVisVideo(t, "/lib/b/x.mp4", nil)
	manual := libVisVideo(t, "/lib/c/x.mp4", nil)
	auto := models.Tag{Name: "自动分类", AutomaticKind: "category", IsActive: true}
	hand := models.Tag{Name: "手工", IsActive: true}
	if err := database.DB.Create(&auto).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&hand).Error; err != nil {
		t.Fatal(err)
	}
	libVisTag(t, autoOnly.ID, auto.ID)
	libVisTag(t, manual.ID, auto.ID)
	libVisTag(t, manual.ID, hand.ID)
	if got := libVisIDs(t, LibraryFilter{SmartView: LibraryViewUntagged}); !reflect.DeepEqual(got, []uint{bare.ID, autoOnly.ID}) {
		t.Fatalf("未打标签应只看非自动标签: %v", got)
	}
}

func TestMEDIA08NoSubtitleViewUsesAllThreeSources(t *testing.T) {
	setupVideoServiceTestDB(t)
	none := libVisVideo(t, "/lib/a/x.mp4", nil)
	segments := libVisVideo(t, "/lib/b/x.mp4", nil)
	sidecar := libVisVideo(t, "/lib/c/x.mp4", nil)
	embedded := libVisVideo(t, "/lib/d/x.mp4", nil)
	audioOnly := libVisVideo(t, "/lib/e/x.mp4", nil)
	emptyIndex := libVisVideo(t, "/lib/f/x.mp4", nil)
	states := []models.SubtitleIndexState{
		{VideoID: segments.ID, SegmentCount: 3},
		{VideoID: sidecar.ID},
		{VideoID: emptyIndex.ID},
	}
	for i := range states {
		if err := database.DB.Create(&states[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 显式写 has_sidecar，不依赖零值与 gorm default 标签的交互。
	if err := database.DB.Model(&models.SubtitleIndexState{}).Where("video_id = ?", sidecar.ID).Update("has_sidecar", true).Error; err != nil {
		t.Fatal(err)
	}
	for _, s := range []models.MediaStream{
		{VideoID: embedded.ID, StreamIndex: 2, StreamType: "subtitle"},
		{VideoID: audioOnly.ID, StreamIndex: 1, StreamType: "audio"},
	} {
		s := s
		if err := database.DB.Create(&s).Error; err != nil {
			t.Fatal(err)
		}
	}
	got := libVisIDs(t, LibraryFilter{SmartView: LibraryViewNoSubtitle})
	want := []uint{none.ID, audioOnly.ID, emptyIndex.ID}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("无字幕应排除三种来源 got=%v want=%v", got, want)
	}
}

func TestMETA09LocalMetadataUpdatedSmartView(t *testing.T) {
	setupVideoServiceTestDB(t)
	upd := libVisVideo(t, "/lib/a/x.mp4", nil)
	cur := libVisVideo(t, "/lib/b/x.mp4", nil)
	libVisVideo(t, "/lib/c/x.mp4", nil)
	for _, st := range []models.VideoLocalMetadataState{
		{VideoID: upd.ID, Status: LocalMetadataStateUpdateAvailable, LastCheckedAt: time.Now()},
		{VideoID: cur.ID, Status: LocalMetadataStateCurrent, LastCheckedAt: time.Now()},
	} {
		st := st
		if err := database.DB.Create(&st).Error; err != nil {
			t.Fatal(err)
		}
	}
	if got := libVisIDs(t, LibraryFilter{SmartView: LibraryViewLocalMetadataUpdated}); !reflect.DeepEqual(got, []uint{upd.ID}) {
		t.Fatalf("got=%v", got)
	}
}

func TestLIB15META07UpdateSavedLibraryViewAndActiveTagIDs(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	a, err := svc.SaveLibraryView(SavedLibraryViewInput{Name: "甲视图", LibraryFilter: LibraryFilter{SmartView: LibraryViewFavorites}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveLibraryView(SavedLibraryViewInput{Name: "乙视图"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateSavedLibraryView(a.ID, " 乙视图 ", LibraryFilter{}); !errors.Is(err, ErrSavedViewNameTaken) || err.Error() != "saved_view_name_taken" {
		t.Fatalf("重名应返回 saved_view_name_taken: %v", err)
	}
	// 沿用自己的名字不算重名；条件被整体覆盖。
	updated, err := svc.UpdateSavedLibraryView(a.ID, "甲视图", LibraryFilter{SmartView: LibraryViewWatched, PersonIDs: []uint{2, 1}, MinHeight: 720})
	if err != nil || updated.SmartView != LibraryViewWatched || updated.PersonIDsJSON != "[1,2]" || updated.MinHeight != 720 {
		t.Fatalf("更新失败: %+v err=%v", updated, err)
	}
	renamed, err := svc.UpdateSavedLibraryView(a.ID, "改名", LibraryFilter{SmartView: LibraryViewWatched})
	if err != nil || renamed.Name != "改名" || renamed.PersonIDsJSON != "[]" {
		t.Fatalf("重命名: %+v err=%v", renamed, err)
	}
	if _, err := svc.UpdateSavedLibraryView(9999, "x", LibraryFilter{}); err == nil {
		t.Fatalf("不存在的视图应报错")
	}
	if _, err := svc.UpdateSavedLibraryView(a.ID, "", LibraryFilter{}); err == nil {
		t.Fatalf("空名称应被拒绝")
	}

	live := models.Tag{Name: "活跃", IsActive: true}
	dead := models.Tag{Name: "已删", IsActive: true}
	if err := database.DB.Create(&live).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&dead).Error; err != nil {
		t.Fatal(err)
	}
	res, err := svc.FilterActiveTagIDs([]uint{dead.ID, live.ID, live.ID, 9999})
	if err != nil || !reflect.DeepEqual(res.TagIDs, []uint{live.ID}) || res.Dropped != 2 {
		t.Fatalf("activeTagIDs: %+v err=%v", res, err)
	}
	res, err = svc.FilterActiveTagIDs(nil)
	if err != nil || len(res.TagIDs) != 0 || res.Dropped != 0 {
		t.Fatalf("空输入: %+v err=%v", res, err)
	}
}
