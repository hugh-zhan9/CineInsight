package services

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func newSuggestionService(t *testing.T) *CollectionSuggestionService {
	t.Helper()
	return NewCollectionSuggestionService(NewCollectionService(t.TempDir()))
}

func createSuggestionScanRoot(t *testing.T, path string) string {
	t.Helper()
	if err := database.DB.Create(&models.ScanDirectory{Path: path, Alias: filepath.Base(path)}).Error; err != nil {
		t.Fatalf("创建扫描目录失败: %v", err)
	}
	return path
}

func createSuggestionVideo(t *testing.T, root, name string) models.Video {
	t.Helper()
	video := models.Video{
		Name:      name,
		Path:      filepath.Join(root, name),
		Directory: root,
		Size:      1,
		Duration:  1,
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建测试视频失败 name=%s: %v", name, err)
	}
	return video
}

func analyzeSuggestionsOnce(t *testing.T, svc *CollectionSuggestionService) []CollectionSuggestionView {
	t.Helper()
	if err := svc.analyzeOnce(context.Background()); err != nil {
		t.Fatalf("剧集分析失败: %v", err)
	}
	views, err := svc.List()
	if err != nil {
		t.Fatalf("读取剧集候选失败: %v", err)
	}
	return views
}

func suggestionMemberIDs(view CollectionSuggestionView) []uint {
	ids := make([]uint, 0, len(view.Members))
	for _, member := range view.Members {
		ids = append(ids, member.VideoID)
	}
	return ids
}

// 分组键是 (scan_root, normalized_series)：跨扫描根不合并，成员少于 2 不成候选，
// 没有剧集模式的文件根本不参与（D-023）。
func TestCollectionSuggestionGroupsPerScanRootAndNeedsTwoMembers(t *testing.T) {
	setupVideoServiceTestDB(t)
	base := t.TempDir()
	rootA := createSuggestionScanRoot(t, filepath.Join(base, "rootA"))
	rootB := createSuggestionScanRoot(t, filepath.Join(base, "rootB"))

	first := createSuggestionVideo(t, rootA, "The.Expanse.S01E01.1080p.WEB-DL.mkv")
	second := createSuggestionVideo(t, rootA, "The.Expanse.S01E02.1080p.WEB-DL.mkv")
	createSuggestionVideo(t, rootA, "Interstellar (2014) 2160p.mkv")
	createSuggestionVideo(t, rootA, "Lonely.Show.S01E01.mkv")
	createSuggestionVideo(t, rootB, "The.Expanse.S01E03.1080p.WEB-DL.mkv")
	// 不在任何扫描根下的视频不参与分组：凑一个空扫描根等于跨根合并。
	createSuggestionVideo(t, filepath.Join(base, "outside"), "The.Expanse.S01E04.mkv")

	svc := newSuggestionService(t)
	views := analyzeSuggestionsOnce(t, svc)
	if len(views) != 1 {
		t.Fatalf("应当只有一条候选: %#v", views)
	}
	view := views[0]
	if view.ScanRoot != rootA {
		t.Fatalf("扫描根不符: got=%q want=%q", view.ScanRoot, rootA)
	}
	if view.SeriesName != "The Expanse" {
		t.Fatalf("系列名不符: %q", view.SeriesName)
	}
	if got := suggestionMemberIDs(view); len(got) != 2 || got[0] != first.ID || got[1] != second.ID {
		t.Fatalf("成员不符: got=%v want=[%d %d]", got, first.ID, second.ID)
	}
	if view.Members[0].Season == nil || *view.Members[0].Season != 1 {
		t.Fatalf("季号未落库: %#v", view.Members[0])
	}
	if view.Members[0].ThumbnailURL == "" {
		t.Fatalf("成员应带缩略图地址: %#v", view.Members[0])
	}
}

// 已经在任何作品集里的视频不进入候选（D-025）。
func TestCollectionSuggestionExcludesVideosAlreadyInCollection(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := createSuggestionScanRoot(t, t.TempDir())
	first := createSuggestionVideo(t, root, "Show.S01E01.mkv")
	second := createSuggestionVideo(t, root, "Show.S01E02.mkv")
	third := createSuggestionVideo(t, root, "Show.S01E03.mkv")

	collections := NewCollectionService(t.TempDir())
	existing, err := collections.CreateCollection("手工整理", "")
	if err != nil {
		t.Fatalf("创建作品集失败: %v", err)
	}
	if err := collections.AddCollectionVideo(existing.ID, first.ID); err != nil {
		t.Fatalf("加入作品集失败: %v", err)
	}

	svc := NewCollectionSuggestionService(collections)
	views := analyzeSuggestionsOnce(t, svc)
	if len(views) != 1 {
		t.Fatalf("应当只有一条候选: %#v", views)
	}
	got := suggestionMemberIDs(views[0])
	if len(got) != 2 || got[0] != second.ID || got[1] != third.ID {
		t.Fatalf("已入集视频不该成为成员: got=%v", got)
	}

	// 只剩一个没入集的视频时整组消失。
	if err := collections.AddCollectionVideo(existing.ID, second.ID); err != nil {
		t.Fatalf("加入作品集失败: %v", err)
	}
	views = analyzeSuggestionsOnce(t, svc)
	if len(views) != 0 {
		t.Fatalf("成员不足 2 应当没有候选: %#v", views)
	}
}

// 成员按 (season, episode) 排序；同一集的多个版本共用一个 position 并被标注。
func TestCollectionSuggestionSortsBySeasonEpisodeAndSharesPositionForVersions(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := createSuggestionScanRoot(t, t.TempDir())
	secondSeason := createSuggestionVideo(t, root, "Show.S02E01.mkv")
	firstEpisodeRepack := createSuggestionVideo(t, root, "Show.S01E01.PROPER.mkv")
	secondEpisode := createSuggestionVideo(t, root, "Show.S01E02.mkv")
	firstEpisode := createSuggestionVideo(t, root, "Show.S01E01.mkv")

	svc := newSuggestionService(t)
	views := analyzeSuggestionsOnce(t, svc)
	if len(views) != 1 {
		t.Fatalf("应当只有一条候选: %#v", views)
	}
	got := suggestionMemberIDs(views[0])
	want := []uint{firstEpisodeRepack.ID, firstEpisode.ID, secondEpisode.ID, secondSeason.ID}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("成员顺序不符: got=%v want=%v", got, want)
		}
	}
	positions := make([]int, 0, len(views[0].Members))
	for _, member := range views[0].Members {
		positions = append(positions, member.Position)
	}
	if positions[0] != 1 || positions[1] != 1 || positions[2] != 2 || positions[3] != 3 {
		t.Fatalf("同集多版本应共用 position: %v", positions)
	}
	if !views[0].Members[0].MultipleVersions || !views[0].Members[1].MultipleVersions {
		t.Fatalf("同集多版本应被标注: %#v", views[0].Members)
	}
	if views[0].Members[2].MultipleVersions {
		t.Fatalf("单版本不该标注同集多版本: %#v", views[0].Members[2])
	}
}

// 短横线集号的同集多版本（03 与 03v2）同样共用 position。
func TestCollectionSuggestionTreatsDashVersionSuffixAsSameEpisode(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := createSuggestionScanRoot(t, t.TempDir())
	plain := createSuggestionVideo(t, root, "Frieren - 03.mkv")
	revision := createSuggestionVideo(t, root, "Frieren - 03v2.mkv")

	svc := newSuggestionService(t)
	views := analyzeSuggestionsOnce(t, svc)
	if len(views) != 1 {
		t.Fatalf("应当只有一条候选: %#v", views)
	}
	if len(views[0].Members) != 2 {
		t.Fatalf("成员数不符: %#v", views[0].Members)
	}
	for _, member := range views[0].Members {
		if member.Position != 1 || !member.MultipleVersions {
			t.Fatalf("同集多版本应共用 position 且被标注: %#v", member)
		}
		if member.Episode == nil || *member.Episode != 3 {
			t.Fatalf("集号应为 3: %#v", member)
		}
	}
	if views[0].Members[0].VideoID != plain.ID || views[0].Members[1].VideoID != revision.ID {
		t.Fatalf("同集内应按视频 ID 稳定排序: %v", suggestionMemberIDs(views[0]))
	}
}

// 忽略按 fingerprint 记忆：成员不变不再出现，成员集合一变就是新候选（D-025）。
func TestCollectionSuggestionDismissIsRememberedUntilMembersChange(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := createSuggestionScanRoot(t, t.TempDir())
	createSuggestionVideo(t, root, "Show.S01E01.mkv")
	createSuggestionVideo(t, root, "Show.S01E02.mkv")

	svc := newSuggestionService(t)
	views := analyzeSuggestionsOnce(t, svc)
	if len(views) != 1 {
		t.Fatalf("应当只有一条候选: %#v", views)
	}
	dismissedID := views[0].ID
	if err := svc.Dismiss(dismissedID); err != nil {
		t.Fatalf("忽略候选失败: %v", err)
	}
	if err := svc.Dismiss(dismissedID); !errors.Is(err, ErrCollectionSuggestionNotPending) {
		t.Fatalf("重复忽略应报 suggestion_not_pending: %v", err)
	}

	views = analyzeSuggestionsOnce(t, svc)
	if len(views) != 0 {
		t.Fatalf("被忽略的组不该再出现: %#v", views)
	}
	// 忽略只翻状态，成员一个不动——记忆键就是这些成员。
	var memberCount int64
	if err := database.DB.Model(&models.CollectionSuggestionMember{}).Where("suggestion_id = ?", dismissedID).Count(&memberCount).Error; err != nil {
		t.Fatalf("统计成员失败: %v", err)
	}
	if memberCount != 2 {
		t.Fatalf("忽略不应删除成员: count=%d", memberCount)
	}

	createSuggestionVideo(t, root, "Show.S01E03.mkv")
	views = analyzeSuggestionsOnce(t, svc)
	if len(views) != 1 {
		t.Fatalf("成员集合变化后应重新出现: %#v", views)
	}
	if views[0].ID == dismissedID {
		t.Fatalf("新成员集合必须是新候选: id=%d", views[0].ID)
	}
	if len(views[0].Members) != 3 {
		t.Fatalf("新候选应有 3 个成员: %#v", views[0].Members)
	}
}

// 成员集合变化会让旧的 pending 候选作废，不留一堆过期候选。
func TestCollectionSuggestionRetiresStalePendingWhenMembersChange(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := createSuggestionScanRoot(t, t.TempDir())
	createSuggestionVideo(t, root, "Show.S01E01.mkv")
	createSuggestionVideo(t, root, "Show.S01E02.mkv")

	svc := newSuggestionService(t)
	first := analyzeSuggestionsOnce(t, svc)
	if len(first) != 1 {
		t.Fatalf("应当只有一条候选: %#v", first)
	}
	createSuggestionVideo(t, root, "Show.S01E03.mkv")
	second := analyzeSuggestionsOnce(t, svc)
	if len(second) != 1 {
		t.Fatalf("应当只有一条候选: %#v", second)
	}
	if second[0].ID == first[0].ID {
		t.Fatalf("成员变化后应是新候选: %d", second[0].ID)
	}
	var rows int64
	if err := database.DB.Model(&models.CollectionSuggestion{}).Count(&rows).Error; err != nil {
		t.Fatalf("统计候选失败: %v", err)
	}
	if rows != 1 {
		t.Fatalf("过期的 pending 候选应被清掉: count=%d", rows)
	}
	var orphanMembers int64
	if err := database.DB.Model(&models.CollectionSuggestionMember{}).Where("suggestion_id = ?", first[0].ID).Count(&orphanMembers).Error; err != nil {
		t.Fatalf("统计成员失败: %v", err)
	}
	if orphanMembers != 0 {
		t.Fatalf("候选删除应级联删成员: count=%d", orphanMembers)
	}
}

// 确认走 CreateCollection + AddCollectionVideos + ReorderCollectionVideos，
// 作品集顺序与集号一致，且视频的 display_title / name / path 一个字都不动（4.5.5）。
func TestCollectionSuggestionConfirmBuildsOrderedCollectionAndLeavesVideosUntouched(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := createSuggestionScanRoot(t, t.TempDir())
	createSuggestionVideo(t, root, "Show.S01E02.mkv")
	createSuggestionVideo(t, root, "Show.S01E01.mkv")
	createSuggestionVideo(t, root, "Show.S02E01.mkv")

	var before []models.Video
	if err := database.DB.Order("id ASC").Find(&before).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}

	svc := newSuggestionService(t)
	views := analyzeSuggestionsOnce(t, svc)
	if len(views) != 1 {
		t.Fatalf("应当只有一条候选: %#v", views)
	}
	ordered := suggestionMemberIDs(views[0])
	detail, err := svc.Confirm(views[0].ID, "剧集合集", ordered)
	if err != nil {
		t.Fatalf("确认候选失败: %v", err)
	}
	if detail.Collection.Collection.Name != "剧集合集" {
		t.Fatalf("作品集名称不符: %#v", detail.Collection.Collection)
	}
	if len(detail.Videos) != len(ordered) {
		t.Fatalf("作品集成员数不符: %#v", detail.Videos)
	}
	for index, item := range detail.Videos {
		if item.Video.ID != ordered[index] {
			t.Fatalf("作品集顺序应与集号一致: got=%d want=%d index=%d", item.Video.ID, ordered[index], index)
		}
		if item.Position != index+1 {
			t.Fatalf("position 应从 1 连续: %#v", item)
		}
	}

	var after []models.Video
	if err := database.DB.Order("id ASC").Find(&after).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("视频条数不该变: got=%d want=%d", len(after), len(before))
	}
	for index := range before {
		if after[index].DisplayTitle != before[index].DisplayTitle ||
			after[index].Name != before[index].Name ||
			after[index].Path != before[index].Path {
			t.Fatalf("确认不得改视频标题或文件名: before=%#v after=%#v", before[index], after[index])
		}
	}

	var confirmed models.CollectionSuggestion
	if err := database.DB.First(&confirmed, views[0].ID).Error; err != nil {
		t.Fatalf("读取候选失败: %v", err)
	}
	if confirmed.Status != models.CollectionSuggestionStatusConfirmed {
		t.Fatalf("候选应标记 confirmed: %q", confirmed.Status)
	}
	if _, err := svc.Confirm(views[0].ID, "剧集合集", ordered); !errors.Is(err, ErrCollectionSuggestionNotPending) {
		t.Fatalf("重复确认应报 suggestion_not_pending: %v", err)
	}

	// 已确认的组即使退出作品集也不再提：fingerprint 记着这一组的判断（D-025）。
	if err := svc.collections.DeleteCollection(detail.Collection.Collection.ID); err != nil {
		t.Fatalf("删除作品集失败: %v", err)
	}
	if views = analyzeSuggestionsOnce(t, svc); len(views) != 0 {
		t.Fatalf("已确认的组不该重新出现: %#v", views)
	}
}

// 同名活跃作品集已存在时确认为"加入"，不新建，也不打乱人家原有的次序。
func TestCollectionSuggestionConfirmJoinsExistingActiveCollection(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := createSuggestionScanRoot(t, t.TempDir())
	createSuggestionVideo(t, root, "Show.S01E01.mkv")
	createSuggestionVideo(t, root, "Show.S01E02.mkv")
	unrelated := createSuggestionVideo(t, root, "Prologue.mkv")

	collections := NewCollectionService(t.TempDir())
	existing, err := collections.CreateCollection("Show", "")
	if err != nil {
		t.Fatalf("创建作品集失败: %v", err)
	}
	if err := collections.AddCollectionVideo(existing.ID, unrelated.ID); err != nil {
		t.Fatalf("加入作品集失败: %v", err)
	}

	svc := NewCollectionSuggestionService(collections)
	views := analyzeSuggestionsOnce(t, svc)
	if len(views) != 1 {
		t.Fatalf("应当只有一条候选: %#v", views)
	}
	ordered := suggestionMemberIDs(views[0])
	// 规范化同名（大小写不同）也算同一个活跃作品集。
	detail, err := svc.Confirm(views[0].ID, " show ", ordered)
	if err != nil {
		t.Fatalf("确认候选失败: %v", err)
	}
	if detail.Collection.Collection.ID != existing.ID {
		t.Fatalf("应当加入同名活跃作品集: got=%d want=%d", detail.Collection.Collection.ID, existing.ID)
	}
	var collectionCount int64
	if err := database.DB.Model(&models.MediaCollection{}).Count(&collectionCount).Error; err != nil {
		t.Fatalf("统计作品集失败: %v", err)
	}
	if collectionCount != 1 {
		t.Fatalf("不该新建作品集: count=%d", collectionCount)
	}
	want := append([]uint{unrelated.ID}, ordered...)
	for index, item := range detail.Videos {
		if item.Video.ID != want[index] {
			t.Fatalf("确认的成员应排在原有成员之后: got=%v want=%v", detail.Videos, want)
		}
	}
}

// 确认只接受候选成员，名称为空时回退到系列名。
func TestCollectionSuggestionConfirmValidatesSelectionAndName(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := createSuggestionScanRoot(t, t.TempDir())
	createSuggestionVideo(t, root, "Show.S01E01.mkv")
	createSuggestionVideo(t, root, "Show.S01E02.mkv")
	outsider := createSuggestionVideo(t, root, "Other.S01E01.mkv")
	createSuggestionVideo(t, root, "Other.S01E02.mkv")

	svc := newSuggestionService(t)
	views := analyzeSuggestionsOnce(t, svc)
	if len(views) != 2 {
		t.Fatalf("应当有两条候选: %#v", views)
	}
	var target CollectionSuggestionView
	for _, view := range views {
		if view.SeriesName == "Show" {
			target = view
		}
	}
	if target.ID == 0 {
		t.Fatalf("没找到 Show 候选: %#v", views)
	}
	members := suggestionMemberIDs(target)
	if _, err := svc.Confirm(target.ID, "x", []uint{members[0], outsider.ID}); !errors.Is(err, ErrCollectionSuggestionMemberMismatch) {
		t.Fatalf("非成员应报 member_mismatch: %v", err)
	}
	if _, err := svc.Confirm(target.ID, "x", []uint{members[0], members[0]}); !errors.Is(err, ErrCollectionSuggestionMemberMismatch) {
		t.Fatalf("重复成员应报 member_mismatch: %v", err)
	}
	if _, err := svc.Confirm(target.ID, "x", []uint{members[0]}); !errors.Is(err, ErrCollectionSuggestionMemberMismatch) {
		t.Fatalf("只留一个成员应报 member_mismatch: %v", err)
	}
	// 去掉一个成员是允许的，但一组至少留两个；名称留空则用系列名。
	detail, err := svc.Confirm(target.ID, "   ", members)
	if err != nil {
		t.Fatalf("确认候选失败: %v", err)
	}
	if detail.Collection.Collection.Name != "Show" {
		t.Fatalf("名称留空应回退系列名: %#v", detail.Collection.Collection)
	}
}

// 去掉成员后只写入保留的那些，被去掉的视频不进作品集。
func TestCollectionSuggestionConfirmHonoursRemovedMembers(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := createSuggestionScanRoot(t, t.TempDir())
	first := createSuggestionVideo(t, root, "Show.S01E01.mkv")
	second := createSuggestionVideo(t, root, "Show.S01E02.mkv")
	third := createSuggestionVideo(t, root, "Show.S01E03.mkv")

	svc := newSuggestionService(t)
	views := analyzeSuggestionsOnce(t, svc)
	if len(views) != 1 {
		t.Fatalf("应当只有一条候选: %#v", views)
	}
	detail, err := svc.Confirm(views[0].ID, "Show", []uint{third.ID, first.ID})
	if err != nil {
		t.Fatalf("确认候选失败: %v", err)
	}
	if len(detail.Videos) != 2 {
		t.Fatalf("只应写入保留的成员: %#v", detail.Videos)
	}
	if detail.Videos[0].Video.ID != third.ID || detail.Videos[1].Video.ID != first.ID {
		t.Fatalf("应按调用方给的顺序写入: %#v", detail.Videos)
	}
	var relations int64
	if err := database.DB.Model(&models.CollectionVideo{}).Where("video_id = ?", second.ID).Count(&relations).Error; err != nil {
		t.Fatalf("统计关系失败: %v", err)
	}
	if relations != 0 {
		t.Fatalf("被去掉的成员不该进作品集: count=%d", relations)
	}
}

// 异步入口：登记表 Begin/End 配对，状态与事件都收在终态上。
func TestCollectionSuggestionAnalyzeRegistersBackgroundTaskAndCompletes(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := createSuggestionScanRoot(t, t.TempDir())
	createSuggestionVideo(t, root, "Show.S01E01.mkv")
	createSuggestionVideo(t, root, "Show.S01E02.mkv")

	// 登记表回调与事件发射都可能来自 worker goroutine（End / 终态 update），
	// 测试自己也从 Analyze 这条路径同步收到几次。共享状态一律加锁：
	// 不加锁的 append + 读会被 -race 抓住，而这是测试侧的错，不是服务的。
	registry := NewBackgroundTaskRegistry()
	var (
		snapshotMu sync.Mutex
		snapshots  [][]string
	)
	cleared := make(chan struct{})
	var clearedOnce sync.Once
	registry.SetOnChange(func(running []string) {
		snapshotMu.Lock()
		snapshots = append(snapshots, running)
		snapshotMu.Unlock()
		if len(running) == 0 {
			clearedOnce.Do(func() { close(cleared) })
		}
	})
	svc := newSuggestionService(t)
	svc.SetBackgroundTaskRegistry(registry)
	done := make(chan struct{})
	var doneOnce sync.Once
	svc.SetEventEmitter(func(status CollectionSuggestionStatus) {
		if status.Running {
			return
		}
		doneOnce.Do(func() { close(done) })
	})

	status, err := svc.Analyze(context.Background())
	if err != nil {
		t.Fatalf("启动剧集分析失败: %v", err)
	}
	if !status.Running || status.Total != 2 {
		t.Fatalf("启动状态不符: %#v", status)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("剧集分析未在预期时间内结束: %#v", svc.Status())
	}
	// 终态事件先于 registry.End（后者是 run 的 defer），所以还要等登记表清空，
	// 否则"结束后登记表应清空"这条断言读到的可能是 End 之前的快照。
	select {
	case <-cleared:
	case <-time.After(30 * time.Second):
		t.Fatalf("登记表未在预期时间内清空: %#v", registry.Snapshot())
	}
	final := svc.Status()
	if final.Running || !final.Completed || final.Cancelled {
		t.Fatalf("终态不符: %#v", final)
	}
	if final.Scanned != 2 || final.Matched != 2 || final.Pending != 1 {
		t.Fatalf("统计不符: %#v", final)
	}
	if final.LastError != "" {
		t.Fatalf("不该有错误: %q", final.LastError)
	}
	// 取快照仍然过锁，读到的是一份副本。
	snapshotMu.Lock()
	recorded := append([][]string(nil), snapshots...)
	snapshotMu.Unlock()
	if len(recorded) < 2 {
		t.Fatalf("登记表应记到开始与结束: %#v", recorded)
	}
	if len(recorded[0]) != 1 || recorded[0][0] != string(BackgroundTaskCollectionSuggest) {
		t.Fatalf("首个快照应是本任务: %#v", recorded[0])
	}
	if len(recorded[len(recorded)-1]) != 0 {
		t.Fatalf("结束后登记表应清空: %#v", recorded[len(recorded)-1])
	}
	if err := svc.Cancel(); err == nil {
		t.Fatalf("没有在跑时取消应报错")
	}
}

// 取消后不写候选：分析中途退出，库里干干净净。
func TestCollectionSuggestionAnalyzeCancelledWritesNothing(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := createSuggestionScanRoot(t, t.TempDir())
	createSuggestionVideo(t, root, "Show.S01E01.mkv")
	createSuggestionVideo(t, root, "Show.S01E02.mkv")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := newSuggestionService(t)
	if err := svc.analyzeOnce(ctx); err != nil {
		t.Fatalf("取消不应算失败: %v", err)
	}
	var rows int64
	if err := database.DB.Model(&models.CollectionSuggestion{}).Count(&rows).Error; err != nil {
		t.Fatalf("统计候选失败: %v", err)
	}
	if rows != 0 {
		t.Fatalf("取消后不应写候选: count=%d", rows)
	}
}

// 没有扫描根就没有候选：分组键的一半缺了，不能拿目录名或空串顶上。
func TestCollectionSuggestionWithoutScanRootsProducesNothing(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	createSuggestionVideo(t, root, "Show.S01E01.mkv")
	createSuggestionVideo(t, root, "Show.S01E02.mkv")

	svc := newSuggestionService(t)
	if views := analyzeSuggestionsOnce(t, svc); len(views) != 0 {
		t.Fatalf("没有扫描根时不该有候选: %#v", views)
	}
}
