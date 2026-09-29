package services

import (
	"context"
	"errors"
	"testing"

	"video-master/database"
	"video-master/models"
)

// 本文件验收 P-019 独立评审的修复项（APP-07 / APP-05）。

func (h *movieChartMarkHarness) useWatchlistSources(chains map[WatchlistMetadataKind][]WatchlistMetadataSource) {
	h.watchlist.enrich.sources = func() (watchlistEnrichmentSources, error) {
		return watchlistEnrichmentSources{registry: newWatchlistMetadataRegistry(chains), poster: nil}, nil
	}
}

// I-1：沙丘误匹配 1984 版 → 改选 2021 版 → 取消 1984 的 want 不得删除条目。
func TestAPP07ReselectReleasesStaleWantClaim(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	old := h.seedEntry("1984", "沙丘", "1984-12-14")
	h.seedEntry("2021", "沙丘", "2021-10-22")
	if _, err := h.service.MarkEntry(old.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	entries := h.watchlistEntries()
	if len(entries) != 1 {
		t.Fatalf("应建出一条榜单条目: %+v", entries)
	}
	entryID := entries[0].ID
	h.useWatchlistSources(map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {staticWatchlistSource(WatchlistMetadataSourceDouban, WatchlistMetadataDetail{
			WatchlistMetadataCandidate: WatchlistMetadataCandidate{SourceItemID: "2021", Title: "沙丘", Year: 2021},
		})},
	})

	if err := h.watchlist.ApplyCandidate(entryID, "2021"); err != nil {
		t.Fatal(err)
	}
	if row, _ := h.markRow("1984"); row.Mark != "" || row.WatchlistEntryID != 0 {
		t.Fatalf("旧 want 标记应被释放: %+v", row)
	}
	if row, _ := h.markRow("2021"); row.Mark != models.MovieChartMarkWant || row.WatchlistEntryID != entryID {
		t.Fatalf("新 ID 应认领条目: %+v", row)
	}
	if err := h.service.ClearMark("1984"); err != nil {
		t.Fatal(err)
	}
	if len(h.watchlistEntries()) != 1 {
		t.Fatalf("取消 1984 的 want 不得删除已改选的条目")
	}
}

// I-1：改选到非豆瓣来源同样释放旧标记。
func TestAPP07ReselectToNonDoubanReleasesWantClaim(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	old := h.seedEntry("1984", "沙丘", "1984-12-14")
	if _, err := h.service.MarkEntry(old.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	entryID := h.watchlistEntries()[0].ID
	h.useWatchlistSources(map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {staticWatchlistSource(WatchlistMetadataSourceTMDB, WatchlistMetadataDetail{
			WatchlistMetadataCandidate: WatchlistMetadataCandidate{SourceItemID: "438631", Title: "沙丘", Year: 2021},
		})},
	})
	if err := h.watchlist.ApplyCandidate(entryID, "438631"); err != nil {
		t.Fatal(err)
	}
	if row, _ := h.markRow("1984"); row.Mark != "" || row.WatchlistEntryID != 0 {
		t.Fatalf("改选到非豆瓣来源应释放旧标记: %+v", row)
	}
}

// I-2：复用 TMDB 条目 → 删除条目 → 榜单不再显示 want；无关的 want 不动。
func TestAPP07DeleteReusedTMDBEntryRevokesWant(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	manual := mustCreateEnrichedEntry(t, "沙丘", "tmdb", "438631")
	entry := h.seedEntry("201", "沙丘", "2026-03-20")
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	other := h.seedEntry("202", "别的片", "2026-03-20")
	if _, err := h.service.MarkEntry(other.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	if err := h.watchlist.Delete(manual.ID); err != nil {
		t.Fatal(err)
	}
	if row, _ := h.markRow("201"); row.Mark != "" {
		t.Fatalf("复用条目被删后 want 应撤销: %+v", row)
	}
	if row, _ := h.markRow("202"); row.Mark != models.MovieChartMarkWant {
		t.Fatalf("其他 want 不得被撤销: %+v", row)
	}
}

// I-4：批量建议一次扫描，结果与单条接口一致。
func TestAPP06SuggestLibraryMatchesBatch(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	for _, v := range []models.Video{
		{Name: "沙丘.2021.mkv", Path: "/m/a.mkv", DisplayTitle: "沙丘"},
		{Name: "阿凡达.mkv", Path: "/m/b.mkv", DisplayTitle: "阿凡达"},
	} {
		v := v
		if err := h.db.Create(&v).Error; err != nil {
			t.Fatal(err)
		}
	}
	queries := []LibraryMatchQuery{{Title: "沙丘", Year: 2021}, {Title: "阿凡达", Year: 0}, {Title: "无此片", Year: 0}, {Title: " ！ ", Year: 0}}
	got, err := h.service.SuggestLibraryMatchesBatch(queries)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("每个查询都应有键: %v", got)
	}
	if len(got[LibraryMatchKey("沙丘", 2021)]) != 1 || len(got[LibraryMatchKey("阿凡达", 0)]) != 1 ||
		len(got[LibraryMatchKey("无此片", 0)]) != 0 || len(got[LibraryMatchKey(" ！ ", 0)]) != 0 {
		t.Fatalf("批量结果不对: %v", got)
	}
	single, err := h.service.SuggestLibraryMatches("沙丘", 2021)
	if err != nil || len(single) != 1 || single[0] != got[LibraryMatchKey("沙丘", 2021)][0] {
		t.Fatalf("单条应是批量的特例: %v %v", single, err)
	}
}

// Minor：手动改名清空来源 ID；被 want 认领的条目保持来源。
func TestAPP07RenameClearsSourceUnlessClaimed(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	plain := mustCreateEnrichedEntry(t, "旧名", "tmdb", "11")
	if err := h.watchlist.Update(plain.ID, "新名"); err != nil {
		t.Fatal(err)
	}
	if got := reloadWatchlistEntry(t, plain.ID); got.SourceName != "" || got.SourceItemID != "" ||
		got.EnrichmentStatus != models.WatchlistEnrichmentManual {
		t.Fatalf("改名应清空来源且状态仍为 manual: %+v", got)
	}

	chartEntry := h.seedEntry("201", "榜单片", "2026-01-01")
	if _, err := h.service.MarkEntry(chartEntry.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	entries := h.watchlistEntries()
	claimed := entries[len(entries)-1]
	if err := h.watchlist.Update(claimed.ID, "榜单片改名"); err != nil {
		t.Fatal(err)
	}
	if got := reloadWatchlistEntry(t, claimed.ID); got.SourceItemID != "201" {
		t.Fatalf("被认领的条目改名不得清来源: %+v", got)
	}
}

// Minor：补全写回撞唯一键落 title_conflict，不再是 source_error。
func TestAPP07EnrichmentTitleConflictHasOwnFailureCode(t *testing.T) {
	douban := staticWatchlistSource(WatchlistMetadataSourceDouban, WatchlistMetadataDetail{
		WatchlistMetadataCandidate: WatchlistMetadataCandidate{SourceItemID: "301", Title: "同名片", Year: 2021},
	})
	h := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {douban},
	}, nil)
	// 先有一条已带 douban:301 的同名 movie 条目，第二条 pending 补全后会写成同一个键。
	first := models.WatchlistEntry{Title: "同名片", Kind: models.WatchlistKindMovie,
		EnrichmentStatus: models.WatchlistEnrichmentSucceeded, SourceName: WatchlistMetadataSourceDouban, SourceItemID: "301"}
	if err := database.DB.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	second := models.WatchlistEntry{Title: "同名片", Kind: models.WatchlistKindMovie,
		EnrichmentStatus: models.WatchlistEnrichmentPending, SourceItemID: "x"}
	if err := database.DB.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	h.service.runEnrichmentOnce(context.Background())
	got := reloadWatchlistEntry(t, second.ID)
	if got.EnrichmentStatus != models.WatchlistEnrichmentFailed || got.EnrichmentError != string(WatchlistMetadataFailureTitleConflict) {
		t.Fatalf("撞唯一键应落 title_conflict: %+v", got)
	}
}

// Minor：补图判据排除空标记行。
func TestAPP07PosterBacklogIgnoresEmptyMarkRow(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	entry := models.MovieChartEntry{DoubanID: "301", Year: 2026, Title: "被排除", ReleaseScope: models.MovieChartScopeExcluded,
		PosterURL: "https://img2.doubanio.com/x.jpg", DetailStatus: models.MovieChartDetailSucceeded}
	if err := h.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&models.MovieChartMark{DoubanID: "301", Mark: "", Title: "被排除", MarkedAt: h.service.now()}).Error; err != nil {
		t.Fatal(err)
	}
	n, err := h.service.countPosterBacklog(2026)
	if err != nil || n != 0 {
		t.Fatalf("空标记行不该让 excluded 条目进入补图: n=%d err=%v", n, err)
	}
}

// Minor：补全正在运行的条目，榜单复用它时不回填豆瓣来源。
func TestAPP07EnsureChartEntryDoesNotBackfillRunningEntry(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewWatchlistService(t.TempDir())
	running := models.WatchlistEntry{Title: "沙丘", Kind: models.WatchlistKindMovie,
		EnrichmentStatus: models.WatchlistEnrichmentRunning, EnrichmentClaim: "abc"}
	if err := database.DB.Create(&running).Error; err != nil {
		t.Fatal(err)
	}
	id, created, err := svc.EnsureChartEntry("沙丘", "201")
	if err != nil || created || id != running.ID {
		t.Fatalf("应复用运行中的条目: id=%d created=%v err=%v", id, created, err)
	}
	if got := reloadWatchlistEntry(t, running.ID); got.SourceName != "" || got.SourceItemID != "" {
		t.Fatalf("运行中的条目不得回填来源: %+v", got)
	}
}

func TestAPP07DeleteMissingEntryReportsNotFound(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewWatchlistService(t.TempDir())
	if err := svc.Delete(9999); !errors.Is(err, ErrWatchlistEntryNotFound) {
		t.Fatalf("不存在的条目应报不存在: %v", err)
	}
}
