package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"video-master/database"
	"video-master/models"
)

// 本文件验收 P-019 的片单侧（D-PC52、问题清单 APP-07）：撞名语义、榜单建条目的复用、
// 按豆瓣 ID 补全、双向同步。

func mustCreateEnrichedEntry(t *testing.T, title, sourceName, sourceItemID string) models.WatchlistEntry {
	t.Helper()
	entry := models.WatchlistEntry{
		Title:            title,
		Kind:             models.WatchlistKindMovie,
		EnrichmentStatus: models.WatchlistEnrichmentSucceeded,
		SourceName:       sourceName,
		SourceItemID:     sourceItemID,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("创建已补全条目失败: %v", err)
	}
	return entry
}

func watchlistCount(t *testing.T) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Model(&models.WatchlistEntry{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

// 已补全条目（source_item_id 非空）不再被数据库唯一索引挡住手动同名，服务层必须在
// 所有条目里查：手动新建与手动改名都报既有文案。
func TestAPP07ManualCreateAndRenameConflictWithEnrichedEntry(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewWatchlistService(t.TempDir())
	mustCreateEnrichedEntry(t, "沙丘", "tmdb", "438631")

	if _, err := svc.Create("沙丘", models.WatchlistKindMovie); !errors.Is(err, ErrWatchlistTitleExists) {
		t.Fatalf("已补全条目遇到手动新建应报撞名: %v", err)
	} else if err.Error() != "该片名已在想看片单中" {
		t.Fatalf("撞名文案必须不变，实际 %q", err.Error())
	}
	other, err := svc.Create("另一部片", models.WatchlistKindMovie)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Update(other.ID, "沙丘"); !errors.Is(err, ErrWatchlistTitleExists) {
		t.Fatalf("已补全条目遇到手动改名应报撞名: %v", err)
	}
	if got := reloadWatchlistEntry(t, other.ID); got.Title != "另一部片" {
		t.Fatalf("撞名的改名不得落库: %+v", got)
	}
	// 同名不同类型仍然可以共存。
	if _, err := svc.Create("沙丘", models.WatchlistKindTV); err != nil {
		t.Fatalf("同名不同类型应可共存: %v", err)
	}
}

// 榜单 want 遇到已补全（TMDB）的同名手动条目：复用它，不再建一条重复条目，也不改写
// 它的来源（那个 TMDB ID 不能被豆瓣 ID 顶掉）。
func TestAPP07ChartWantReusesEnrichedManualEntry(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	manual := mustCreateEnrichedEntry(t, "沙丘", "tmdb", "438631")
	entry := h.seedEntry("201", "沙丘", "2026-03-20")

	result, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant)
	if err != nil {
		t.Fatal(err)
	}
	if !result.WatchlistConflict || result.WatchlistCreated {
		t.Fatalf("应复用而不是新建: %+v", result)
	}
	entries := h.watchlistEntries()
	if len(entries) != 1 || entries[0].ID != manual.ID {
		t.Fatalf("不得出现重复条目: %+v", entries)
	}
	if entries[0].SourceName != "tmdb" || entries[0].SourceItemID != "438631" {
		t.Fatalf("已有来源的条目不得被改写: %+v", entries[0])
	}
	if row, _ := h.markRow(entry.DoubanID); row.WatchlistEntryID != 0 {
		t.Fatalf("复用的是用户的条目，不得记归属: %+v", row)
	}
}

// 榜单 want 复用来源为空的手动条目：补写豆瓣来源，之后补全按 ID 取详情。
func TestAPP07ChartWantBackfillsSourceOnManualEntry(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	manual, err := h.watchlist.Create("沙丘", models.WatchlistKindMovie)
	if err != nil {
		t.Fatal(err)
	}
	entry := h.seedEntry("201", "沙丘", "2026-03-20")
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	entries := h.watchlistEntries()
	if len(entries) != 1 || entries[0].ID != manual.ID {
		t.Fatalf("不得出现重复条目: %+v", entries)
	}
	if entries[0].SourceName != WatchlistMetadataSourceDouban || entries[0].SourceItemID != "201" {
		t.Fatalf("应补写豆瓣来源: %+v", entries[0])
	}
}

// 榜单新建的条目带着已知的豆瓣 ID 与 pending 状态；同名但豆瓣 ID 不同的两部电影
// 可以共存，各自归属。
func TestAPP07ChartWantCreatesEntryWithDoubanIDAndSameTitleCoexists(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	first := h.seedEntry("201", "同名片", "2026-03-20")
	second := h.seedEntry("202", "同名片", "2025-03-20")

	for _, id := range []string{first.DoubanID, second.DoubanID} {
		result, err := h.service.MarkEntry(id, models.MovieChartMarkWant)
		if err != nil {
			t.Fatalf("标记 %s 失败: %v", id, err)
		}
		if !result.WatchlistCreated || result.WatchlistConflict {
			t.Fatalf("同名不同豆瓣 ID 应各建一条: %s %+v", id, result)
		}
	}
	entries := h.watchlistEntries()
	if len(entries) != 2 {
		t.Fatalf("应共存两条: %+v", entries)
	}
	for i, want := range []string{"201", "202"} {
		got := entries[i]
		if got.SourceName != WatchlistMetadataSourceDouban || got.SourceItemID != want ||
			got.EnrichmentStatus != models.WatchlistEnrichmentPending {
			t.Errorf("条目 %d 来源或状态错误: %+v", i, got)
		}
		if row, _ := h.markRow(want); row.WatchlistEntryID != got.ID {
			t.Errorf("标记 %s 应归属条目 %d: %+v", want, got.ID, row)
		}
	}
	// 手动再建同名条目，两条榜单条目都在，报撞名。
	if _, err := h.watchlist.Create("同名片", models.WatchlistKindMovie); !errors.Is(err, ErrWatchlistTitleExists) {
		t.Fatalf("手动同名应报撞名: %v", err)
	}
}

// 删除带豆瓣 ID 的条目，同事务撤销对应 want（mark 清空、归属清零）；指向别的条目的
// want 不动；撤销 want 仍按既有逻辑删条目。
func TestAPP07DeleteEntryRevokesChartWantAndClearMarkDeletesEntry(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	owned := h.seedEntry("201", "甲", "2026-01-01")
	reused := h.seedEntry("202", "乙", "2026-01-01")
	other := h.seedEntry("203", "丙", "2026-01-01")
	if _, err := h.service.MarkEntry(owned.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	manual, _ := h.watchlist.Create("乙", models.WatchlistKindMovie)
	if _, err := h.service.MarkEntry(reused.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.MarkEntry(other.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}

	ownedRow, _ := h.markRow("201")
	if err := h.watchlist.Delete(ownedRow.WatchlistEntryID); err != nil {
		t.Fatal(err)
	}
	row, ok := h.markRow("201")
	if !ok || row.Mark != "" || row.WatchlistEntryID != 0 {
		t.Fatalf("删条目应清空 want（mark 空、归属 0）: %+v", row)
	}
	// 复用（归属 0）的条目被删同样撤销。
	if err := h.watchlist.Delete(manual.ID); err != nil {
		t.Fatal(err)
	}
	if row, _ := h.markRow("202"); row.Mark != "" {
		t.Fatalf("复用条目被删也应撤销 want: %+v", row)
	}
	if row, _ := h.markRow("203"); row.Mark != models.MovieChartMarkWant || row.WatchlistEntryID == 0 {
		t.Fatalf("别的条目的 want 不得受影响: %+v", row)
	}

	// 撤销 want 仍删除归属的条目，且标记行被删。
	if err := h.service.ClearMark("203"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.markRow("203"); ok {
		t.Fatalf("撤销后标记行应被删除")
	}
	if got := watchlistCount(t); got != 0 {
		t.Fatalf("撤销 want 应删掉榜单建的条目，剩余 %d", got)
	}
	// 被清空的行可以再标记。
	if _, err := h.service.MarkEntry("201", models.MovieChartMarkWant); err != nil {
		t.Fatalf("清空后应可再次想看: %v", err)
	}
}

// 补全遇到 source_name=douban 且带 ID 的条目直接按 ID 取详情，一次搜索都不发。
func TestAPP07EnrichmentByDoubanIDSkipsSearch(t *testing.T) {
	var detailIDs []string
	douban := &fakeWatchlistSource{
		name: WatchlistMetadataSourceDouban,
		searchFn: func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
			t.Errorf("已知豆瓣 ID 不得再按片名搜索")
			return nil, errors.New("不应搜索")
		},
		detailFn: func(_ context.Context, _ WatchlistMetadataKind, id string) (*WatchlistMetadataDetail, error) {
			detailIDs = append(detailIDs, id)
			return &WatchlistMetadataDetail{WatchlistMetadataCandidate: WatchlistMetadataCandidate{
				SourceName: WatchlistMetadataSourceDouban, SourceItemID: id, Title: "沙丘", Year: 2021,
			}}, nil
		},
	}
	tmdb := &fakeWatchlistSource{name: "tmdb", searchFn: func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
		t.Errorf("不得问到 TMDB")
		return nil, errors.New("不应搜索")
	}}
	h := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {douban, tmdb},
	}, nil)
	entryID, created, err := h.service.EnsureChartEntry("沙丘", "301")
	if err != nil || !created {
		t.Fatalf("建条目失败: %v created=%v", err, created)
	}

	h.service.runEnrichmentOnce(context.Background())

	saved := reloadWatchlistEntry(t, entryID)
	if saved.EnrichmentStatus != models.WatchlistEnrichmentSucceeded ||
		saved.SourceName != WatchlistMetadataSourceDouban || saved.SourceItemID != "301" || saved.Year != 2021 {
		t.Fatalf("应按 ID 补全成功且保留豆瓣 ID: %+v", saved)
	}
	if len(detailIDs) != 1 || detailIDs[0] != "301" {
		t.Fatalf("应按 ID 取一次详情，实际 %v", detailIDs)
	}
}

// 手动条目补全写回豆瓣 ID 时，该 ID 没有标记则创建 want（快照 + 指向条目）；已有标记
// 的不覆盖。撤销这个 want 会删除它指向的条目（既有逻辑）。
func TestAPP07ManualEntryBoundToDoubanIDCreatesWantMark(t *testing.T) {
	douban := staticWatchlistSource(WatchlistMetadataSourceDouban, WatchlistMetadataDetail{
		WatchlistMetadataCandidate: WatchlistMetadataCandidate{SourceItemID: "301", Title: "沙丘", Year: 2021},
	})
	h := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {douban},
	}, nil)
	chart := NewMovieChartService(database.DB, h.service)
	manual := mustCreateWatchlistEntry(t, "沙丘", models.WatchlistKindMovie)

	h.service.runEnrichmentOnce(context.Background())

	var mark models.MovieChartMark
	if err := database.DB.Where("douban_id = ?", "301").First(&mark).Error; err != nil {
		t.Fatalf("补全写回豆瓣 ID 后应有 want 标记: %v", err)
	}
	if mark.Mark != models.MovieChartMarkWant || mark.WatchlistEntryID != manual.ID ||
		mark.Title != "沙丘" || mark.ReleaseYear != 2021 {
		t.Fatalf("want 标记快照或归属错误: %+v", mark)
	}

	// 该 ID 已有标记时不覆盖：用户在榜单上先标了「不想看」。
	skipped := mustCreateWatchlistEntry(t, "另一部", models.WatchlistKindMovie)
	if err := database.DB.Create(&models.MovieChartMark{DoubanID: "302", Mark: models.MovieChartMarkSkip,
		Title: "另一部", MarkedAt: chart.now()}).Error; err != nil {
		t.Fatal(err)
	}
	chart.OnWatchlistDoubanBound(skipped.ID, "302", "另一部", 2020)
	var skip models.MovieChartMark
	database.DB.Where("douban_id = ?", "302").First(&skip)
	if skip.Mark != models.MovieChartMarkSkip || skip.WatchlistEntryID != 0 {
		t.Fatalf("已有标记不得被覆盖: %+v", skip)
	}

	// 撤销这个 want 删除它指向的条目。
	if err := chart.ClearMark("301"); err != nil {
		t.Fatal(err)
	}
	var gone int64
	database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", manual.ID).Count(&gone)
	if gone != 0 {
		t.Fatalf("撤销 want 应删除归属条目")
	}
}

// 补全写回豆瓣 ID 撞上另一条同名同 ID 的记录：ApplyCandidate 返回可读的撞名错误，
// 而不是笼统的「应用候选失败」。
func TestAPP07ApplyCandidateCollisionIsReadable(t *testing.T) {
	douban := staticWatchlistSource(WatchlistMetadataSourceDouban, WatchlistMetadataDetail{
		WatchlistMetadataCandidate: WatchlistMetadataCandidate{SourceItemID: "301", Title: "同名片", Year: 2021},
	})
	h := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {douban},
	}, nil)
	if _, _, err := h.service.EnsureChartEntry("同名片", "301"); err != nil {
		t.Fatal(err)
	}
	second, created, err := h.service.EnsureChartEntry("同名片", "302")
	if err != nil || !created {
		t.Fatalf("同名不同豆瓣 ID 应可共存: %v created=%v", err, created)
	}

	err = h.service.ApplyCandidate(second, "301")
	if !errors.Is(err, ErrWatchlistTitleExists) {
		t.Fatalf("撞唯一键应返回撞名错误: %v", err)
	}
	if strings.Contains(err.Error(), "应用候选失败") {
		t.Fatalf("不应是笼统文案: %v", err)
	}
	if got := reloadWatchlistEntry(t, second); got.SourceItemID != "302" {
		t.Fatalf("撞名时不得改写条目: %+v", got)
	}
}

// 片单页给每条标出来源：被 want 标记认领的是榜单，其余是手动。
func TestAPP07ListMarksEntryOrigin(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	manual, _ := h.watchlist.Create("手动片", models.WatchlistKindMovie)
	entry := h.seedEntry("201", "榜单片", "2026-01-01")
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	page, err := h.watchlist.List("", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("应有两条: %+v", page.Entries)
	}
	for _, item := range page.Entries {
		want := WatchlistOriginChart
		if item.ID == manual.ID {
			want = WatchlistOriginManual
		}
		if page.Origins[item.ID] != want {
			t.Errorf("条目 %d(%s) 来源应为 %s，实际 %q", item.ID, item.Title, want, page.Origins[item.ID])
		}
	}
}
