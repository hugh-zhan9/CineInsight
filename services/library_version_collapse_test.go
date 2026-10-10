package services

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"video-master/database"
	"video-master/models"
)

func pageVideoIDs(page *LibraryVideoPage) []uint {
	ids := make([]uint, 0, len(page.Videos))
	for _, video := range page.Videos {
		ids = append(ids, video.ID)
	}
	return ids
}

func sortedIDs(ids []uint) []uint {
	out := append([]uint{}, ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func mustLibraryPage(t *testing.T, filter LibraryFilter) *LibraryVideoPage {
	t.Helper()
	page, err := (&VideoService{}).SearchLibraryVideoPage(filter, nil, 200)
	if err != nil {
		t.Fatalf("片库分页失败: %v", err)
	}
	return page
}

func mustLibraryCount(t *testing.T, filter LibraryFilter) int64 {
	t.Helper()
	count, err := (&VideoService{}).CountLibraryVideos(filter)
	if err != nil {
		t.Fatalf("片库计数失败: %v", err)
	}
	return count
}

func height(h int) func(*models.Video) { return func(v *models.Video) { v.Height = h } }

func TestLibraryCollapseRepresentativeFollowsFilterAndSummaries(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	service := NewVersionGroupService()
	a, b, c := seedVersionVideo(t, "a", height(480)), seedVersionVideo(t, "b", height(1080)), seedVersionVideo(t, "c", height(1080))
	d := seedVersionVideo(t, "d", height(1080))
	e, f := seedVersionVideo(t, "e", height(480)), seedVersionVideo(t, "f", height(480))
	high, low := 8.0, 6.5
	h := seedVersionVideo(t, "h", func(v *models.Video) { v.Height, v.PersonalRating, v.IsWatched = 720, &high, true })
	i := seedVersionVideo(t, "i", func(v *models.Video) { v.Height, v.PersonalRating = 720, &low })

	filter := LibraryFilter{MinHeight: 720}
	for _, sortMode := range []string{LibrarySortBalanced, LibrarySortRatingDesc} {
		filter.SortMode = sortMode
		beforeGroups := pageVideoIDs(mustLibraryPage(t, filter))
		if len(beforeGroups) != 5 {
			t.Fatalf("夹具应有 5 个视频符合筛选: %v", beforeGroups)
		}
		defer func(sortMode string, want []uint) {
			// 关闭开关时与没有任何组时完全一致（顺序、条数与计数），且不带 version_groups。
			off := LibraryFilter{MinHeight: 720, SortMode: sortMode}
			page := mustLibraryPage(t, off)
			requireUintSlice(t, "关闭聚合的结果应与旧结果一致", pageVideoIDs(page), want)
			if len(page.VersionGroups) != 0 || mustLibraryCount(t, off) != int64(len(want)) {
				t.Fatalf("关闭聚合时不应返回版本组: %+v", page.VersionGroups)
			}
		}(sortMode, beforeGroups)
	}

	g1, err := service.Create(ctx, versionIDs(a, b, c), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, versionIDs(e, f), ""); err != nil {
		t.Fatal(err)
	}
	g3, err := service.Create(ctx, versionIDs(h, i), "双版本")
	if err != nil {
		t.Fatal(err)
	}

	for _, sortMode := range []string{LibrarySortBalanced, LibrarySortRatingDesc, LibrarySortRatingAsc} {
		collapsed := LibraryFilter{MinHeight: 720, SortMode: sortMode, CollapseVersions: true}
		page := mustLibraryPage(t, collapsed)
		// 主版本 a 不符合筛选，由次位 b 代表；e、f 全不符合，组不出现；c、i 不是代表。
		requireUintSlice(t, sortMode+" 代表集合", sortedIDs(pageVideoIDs(page)), sortedIDs(versionIDs(b, d, h)))
		if got := mustLibraryCount(t, collapsed); got != 3 {
			t.Fatalf("%s 计数应按卡片计: %d", sortMode, got)
		}
		if len(page.VersionGroups) != 2 {
			t.Fatalf("%s 只应为两个代表返回组: %+v", sortMode, page.VersionGroups)
		}
		first := page.VersionGroups[b.ID]
		if first.GroupID != g1.GroupID || first.MemberCount != 3 || first.Revision != g1.Revision {
			t.Fatalf("组汇总不对: %+v", first)
		}
		requireUintSlice(t, "展开列表含全部活跃成员", memberIDs(first.Members), versionIDs(a, b, c))
		if first.RatedCount != 0 || first.MinRating != nil || first.MaxRating != nil {
			t.Fatalf("没有评分时应为空而不是 0: %+v", first)
		}
		third := page.VersionGroups[h.ID]
		if third.Title != "双版本" || third.GroupID != g3.GroupID || third.WatchedCount != 1 || third.RatedCount != 2 ||
			third.MinRating == nil || *third.MinRating != 6.5 || third.MaxRating == nil || *third.MaxRating != 8 {
			t.Fatalf("评分与已看汇总不对: %+v", third)
		}
		if _, ok := page.VersionGroups[d.ID]; ok {
			t.Fatal("不在组里的视频不应带组")
		}
	}

	// 关键词只命中组里的非主版本：照样由它代表。
	keyword := mustLibraryPage(t, LibraryFilter{Keyword: "c.mp4", CollapseVersions: true})
	requireUintSlice(t, "关键词命中次位成员", pageVideoIDs(keyword), []uint{c.ID})
	if keyword.VersionGroups[c.ID].MemberCount != 3 {
		t.Fatalf("次位代表也应带组汇总: %+v", keyword.VersionGroups)
	}
}

// collectLibraryPages 按游标翻完全部页，返回出现顺序（检查重复与遗漏）。
func collectLibraryPages(t *testing.T, filter LibraryFilter, limit int) []uint {
	t.Helper()
	var ids []uint
	var cursor *LibraryVideoCursor
	for round := 0; round < 50; round++ {
		page, err := (&VideoService{}).SearchLibraryVideoPage(filter, cursor, limit)
		if err != nil {
			t.Fatalf("翻页失败: %v", err)
		}
		ids = append(ids, pageVideoIDs(page)...)
		if page.NextCursor == nil {
			return ids
		}
		cursor = page.NextCursor
	}
	t.Fatal("翻页没有结束")
	return nil
}

func TestLibraryCollapseCursorPagingAndCount(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	service := NewVersionGroupService()
	var videos []models.Video
	for index := 0; index < 9; index++ {
		rating := float64(index%4) * 2.5
		videos = append(videos, seedVersionVideo(t, string(rune('a'+index)), func(v *models.Video) {
			v.Size = int64(1000 + index*10)
			v.PlayCount = index % 3
			if index%3 != 1 {
				v.PersonalRating = &rating
			}
		}))
	}
	// 组 1：主版本是评分最低、体积最小的那个，代表仍按成员顺序取它；组 2 两个成员。
	if _, err := service.Create(ctx, versionIDs(videos[0], videos[5], videos[7]), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, versionIDs(videos[8], videos[2]), ""); err != nil {
		t.Fatal(err)
	}
	wantCollapsed := sortedIDs(versionIDs(videos[0], videos[1], videos[3], videos[4], videos[6], videos[8]))
	allIDs := sortedIDs(versionIDs(videos...))
	for _, sortMode := range []string{LibrarySortBalanced, LibrarySortRatingDesc, LibrarySortRatingAsc} {
		for _, limit := range []int{1, 2, 4} {
			collapsed := collectLibraryPages(t, LibraryFilter{SortMode: sortMode, CollapseVersions: true}, limit)
			requireUintSlice(t, sortMode+" 聚合翻页", sortedIDs(collapsed), wantCollapsed)
			full := collectLibraryPages(t, LibraryFilter{SortMode: sortMode}, limit)
			requireUintSlice(t, sortMode+" 关闭聚合翻页", sortedIDs(full), allIDs)
			// 聚合只删去非代表行，相对顺序与关闭时一致（排序键和游标沿用代表行）。
			var filtered []uint
			keep := map[uint]bool{}
			for _, id := range wantCollapsed {
				keep[id] = true
			}
			for _, id := range full {
				if keep[id] {
					filtered = append(filtered, id)
				}
			}
			requireUintSlice(t, sortMode+" 聚合后的顺序", collapsed, filtered)
		}
		if got := mustLibraryCount(t, LibraryFilter{SortMode: sortMode, CollapseVersions: true}); got != int64(len(wantCollapsed)) {
			t.Fatalf("聚合计数不对: %d", got)
		}
		if got := mustLibraryCount(t, LibraryFilter{SortMode: sortMode}); got != int64(len(allIDs)) {
			t.Fatalf("关闭聚合的计数不对: %d", got)
		}
	}
}

func TestLibraryCollapseSkipsStaleAndSoftDeletedMembers(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	service := NewVersionGroupService()
	a, b, c := seedVersionVideo(t, "a", nil), seedVersionVideo(t, "b", nil), seedVersionVideo(t, "c", nil)
	if _, err := service.Create(ctx, versionIDs(a, b, c), ""); err != nil {
		t.Fatal(err)
	}
	on := LibraryFilter{CollapseVersions: true}
	requireUintSlice(t, "主版本代表", pageVideoIDs(mustLibraryPage(t, on)), []uint{a.ID})

	// 主版本失效：默认视图不含失效记录，由次位代表；失效视图里它自己出现。
	if err := database.DB.Model(&models.Video{}).Where("id = ?", a.ID).
		Updates(map[string]interface{}{"is_stale": true, "stale_reason": models.StaleReasonMissingFile}).Error; err != nil {
		t.Fatal(err)
	}
	page := mustLibraryPage(t, on)
	requireUintSlice(t, "失效主版本由次位代表", pageVideoIDs(page), []uint{b.ID})
	members := page.VersionGroups[b.ID].Members
	if len(members) != 3 || !members[0].IsStale {
		t.Fatalf("展开列表应标出失效成员: %+v", members)
	}
	requireUintSlice(t, "失效视图", pageVideoIDs(mustLibraryPage(t, LibraryFilter{SmartView: LibraryViewStale, CollapseVersions: true})), []uint{a.ID})
	if err := database.DB.Model(&models.Video{}).Where("id = ?", a.ID).
		Updates(map[string]interface{}{"is_stale": false, "stale_reason": ""}).Error; err != nil {
		t.Fatal(err)
	}

	// 主版本软删除：不参与展示与汇总；恢复后回到代表位置。
	if err := database.DB.Delete(&a).Error; err != nil {
		t.Fatal(err)
	}
	page = mustLibraryPage(t, on)
	requireUintSlice(t, "软删主版本由次位代表", pageVideoIDs(page), []uint{b.ID})
	requireUintSlice(t, "软删成员不在展开列表", memberIDs(page.VersionGroups[b.ID].Members), versionIDs(b, c))
	if err := database.DB.Delete(&c).Error; err != nil {
		t.Fatal(err)
	}
	page = mustLibraryPage(t, on)
	if len(page.VersionGroups) != 0 || mustLibraryCount(t, on) != 1 {
		t.Fatalf("活跃成员不足两个时按普通单文件卡片展示: %+v", page.VersionGroups)
	}
	restoreVersionVideo(t, a)
	restoreVersionVideo(t, c)
	page = mustLibraryPage(t, on)
	requireUintSlice(t, "恢复后回到组内", pageVideoIDs(page), []uint{a.ID})
	if page.VersionGroups[a.ID].MemberCount != 3 {
		t.Fatalf("恢复后汇总不对: %+v", page.VersionGroups)
	}
}

// 合同：随机、最近播放、继续观看、旧数组接口等文件级入口一律忽略 collapse_versions。
func TestLibraryCollapseIgnoredByFileLevelConsumers(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	played := time.Now().Add(-time.Hour)
	progress := func(v *models.Video) {
		v.LastPlayedAt, v.WatchPositionSeconds, v.WatchProgressUpdatedAt = &played, 30, &played
	}
	a, b := seedVersionVideo(t, "a", progress), seedVersionVideo(t, "b", progress)
	if _, err := NewVersionGroupService().Create(ctx, versionIDs(a, b), ""); err != nil {
		t.Fatal(err)
	}
	service := &VideoService{}
	on := LibraryFilter{CollapseVersions: true}
	want := sortedIDs(versionIDs(a, b))
	recent, err := service.ListRecentlyPlayedWithFilter(on, "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	continuing, err := service.ListContinueWatchingWithFilter(LibraryFilter{SmartView: LibraryViewContinueWatching, CollapseVersions: true}, "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := service.SearchLibraryVideos(on, 0, 0, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	picked, err := service.PickRandomVideos(RandomPlayRequest{Filter: on}, 2)
	if err != nil {
		t.Fatal(err)
	}
	for label, videos := range map[string][]models.Video{
		"最近播放": recent, "继续观看": continuing.Videos, "旧数组接口": legacy, "随机取样": picked.Videos,
	} {
		ids := make([]uint, 0, len(videos))
		for _, video := range videos {
			ids = append(ids, video.ID)
		}
		requireUintSlice(t, label+"应返回全部文件", sortedIDs(ids), want)
	}
}

// 保存视图不持久化聚合开关：表里没有这一列，模型上也没有这个字段。
func TestSavedLibraryViewDoesNotPersistCollapseVersions(t *testing.T) {
	setupVideoServiceTestDB(t)
	if database.DB.Migrator().HasColumn(&models.SavedLibraryView{}, "collapse_versions") {
		t.Fatal("saved_library_views 不应有 collapse_versions 列")
	}
	view, err := (&VideoService{}).SaveLibraryView(SavedLibraryViewInput{Name: "v", LibraryFilter: LibraryFilter{CollapseVersions: true}})
	if err != nil || view == nil || view.ID == 0 {
		t.Fatalf("带开关的输入也应能保存（开关被丢弃）: %+v %v", view, err)
	}
}

// 复审 #1：聚合与标签、人物、字幕筛选叠加时，代表取「符合筛选的成员里顺序最靠前的」，计数按卡片。
func TestLibraryCollapseWithTagPersonAndSubtitleFilters(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	root := t.TempDir()
	at := func(name string) func(*models.Video) {
		return func(v *models.Video) { v.Path, v.Directory = filepath.Join(root, name+".mp4"), root }
	}
	a, b, c := seedVersionVideo(t, "a", at("a")), seedVersionVideo(t, "b", at("b")), seedVersionVideo(t, "c", at("c"))
	d, e := seedVersionVideo(t, "d", at("d")), seedVersionVideo(t, "e", at("e"))
	if _, err := NewVersionGroupService().Create(ctx, versionIDs(a, b, c), ""); err != nil {
		t.Fatal(err)
	}
	tag := models.Tag{Name: "夜景", Color: "#000000"}
	person := models.Person{DisplayName: "某人"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	// 标签只挂在非主版本 b、c 与组外的 d 上；人物只挂在 c 与 e 上；字幕只在 c 与 d 里出现。
	for _, video := range []models.Video{b, c, d} {
		if err := database.DB.Exec("INSERT INTO video_tags (video_id, tag_id) VALUES (?, ?)", video.ID, tag.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, video := range []models.Video{c, e} {
		if err := database.DB.Omit("Video", "Person").Create(&models.VideoPerson{VideoID: video.ID, PersonID: person.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, video := range []models.Video{a, b, c, d, e} {
		if err := os.WriteFile(video.Path, []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srt := "1\n00:00:01,000 --> 00:00:02,000\nmoonlight harbour\n"
	for _, name := range []string{"c", "d"} {
		if err := os.WriteFile(filepath.Join(root, name+".srt"), []byte(srt), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	syncSubtitleIndexForTest(t)

	cases := []struct {
		name   string
		filter LibraryFilter
		want   []uint
		group  uint
	}{
		{"标签", LibraryFilter{TagIDs: []uint{tag.ID}}, versionIDs(b, d), b.ID},
		{"人物", LibraryFilter{PersonIDs: []uint{person.ID}}, versionIDs(c, e), c.ID},
		{"字幕", LibraryFilter{SearchMode: LibrarySearchModeSubtitle, Keyword: "moonlight"}, versionIDs(c, d), c.ID},
		{"标签+人物", LibraryFilter{TagIDs: []uint{tag.ID}, PersonIDs: []uint{person.ID}}, versionIDs(c), c.ID},
	}
	for _, tc := range cases {
		for _, sortMode := range []string{LibrarySortBalanced, LibrarySortRatingDesc} {
			on := tc.filter
			on.SortMode, on.CollapseVersions = sortMode, true
			page := mustLibraryPage(t, on)
			requireUintSlice(t, tc.name+"/"+sortMode+" 代表", sortedIDs(pageVideoIDs(page)), sortedIDs(tc.want))
			if got := mustLibraryCount(t, on); got != int64(len(tc.want)) {
				t.Fatalf("%s/%s 计数应按卡片: %d", tc.name, sortMode, got)
			}
			if page.VersionGroups[tc.group].MemberCount != 3 {
				t.Fatalf("%s/%s 代表应带全组汇总: %+v", tc.name, sortMode, page.VersionGroups)
			}
		}
	}
}

// captureQuerySQL 记录之后在 database.DB 上执行（含 DryRun 构建子查询）的每条查询 SQL。
func captureQuerySQL(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var statements []string
	name := "test:capture_" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := database.DB.Callback().Query().After("gorm:query").Register(name, func(db *gorm.DB) {
		mu.Lock()
		statements = append(statements, db.Statement.SQL.String())
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.DB.Callback().Query().Remove(name) })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := append([]string{}, statements...)
		statements = nil
		return out
	}
}

func countMatching(statements []string, fragment string) int {
	count := 0
	for _, statement := range statements {
		if strings.Contains(statement, fragment) {
			count++
		}
	}
	return count
}

// 复审 #1：没有任何版本组成员时不建窗口子查询；有成员时扫描范围只读一次（内外两层共用一份已解析的条件）。
func TestVersionCollapseSkipsWithoutMembersAndResolvesScopeOnce(t *testing.T) {
	setupVideoServiceTestDB(t)
	a, b, c := seedVersionVideo(t, "a", nil), seedVersionVideo(t, "b", nil), seedVersionVideo(t, "c", nil)
	on := LibraryFilter{CollapseVersions: true}
	statements := captureQuerySQL(t)

	page := mustLibraryPage(t, on)
	requireUintSlice(t, "无组时结果", sortedIDs(pageVideoIDs(page)), sortedIDs(versionIDs(a, b, c)))
	got := statements()
	if countMatching(got, "ROW_NUMBER") != 0 || countMatching(got, "video_version_members") != 1 {
		t.Fatalf("没有成员时只应做一次 LIMIT 1 探测、不建窗口:\n%s", strings.Join(got, "\n"))
	}
	if mustLibraryCount(t, on) != 3 {
		t.Fatal("无组时计数应等于文件数")
	}
	statements()

	if _, err := NewVersionGroupService().Create(context.Background(), versionIDs(a, b), ""); err != nil {
		t.Fatal(err)
	}
	statements()
	page = mustLibraryPage(t, on)
	requireUintSlice(t, "有组时结果", sortedIDs(pageVideoIDs(page)), sortedIDs(versionIDs(a, c)))
	got = statements()
	if countMatching(got, "ROW_NUMBER") == 0 {
		t.Fatalf("有成员时应建窗口子查询:\n%s", strings.Join(got, "\n"))
	}
	if reads := countMatching(got, "scan_directories"); reads != 1 {
		t.Fatalf("扫描范围应只读一次，实际 %d 次:\n%s", reads, strings.Join(got, "\n"))
	}
	if mustLibraryCount(t, on) != 2 {
		t.Fatal("有组时计数应按卡片")
	}
}
