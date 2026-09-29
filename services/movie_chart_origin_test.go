package services

import (
	"testing"

	"video-master/models"
)

// APP05/APP07：movie_chart_marks.watchlist_entry_origin 的写入点与自动路径行为。

func seedManualWatchlistEntry(t *testing.T, h *movieChartMarkHarness, title string) models.WatchlistEntry {
	t.Helper()
	entry := models.WatchlistEntry{Title: title, Kind: models.WatchlistKindMovie, EnrichmentStatus: models.WatchlistEnrichmentManual}
	if err := h.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	return entry
}

// 视频看完的自动 want → watched 只删榜单创建（origin=chart）的片单条目；
// 补全绑定（origin=enrichment）的用户手动条目保留，认领清空。
func TestAPP05AutoWatchedDeletesChartEntryButKeepsEnrichmentEntry(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	h.seedEntry("501", "榜单片", "2021-10-22")
	h.seedEntry("502", "手动片", "2021-10-22")
	video := h.seedVideo("双片.mkv", "")
	for _, id := range []string{"501", "502"} {
		if err := h.service.LinkMovieToVideo(id, video.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.service.MarkEntry("501", models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	chartRow, _ := h.markRow("501")
	if chartRow.WatchlistEntryOrigin != models.MovieChartOriginChart || chartRow.WatchlistEntryID == 0 {
		t.Fatalf("榜单想看应记 origin=chart: %+v", chartRow)
	}
	manual := seedManualWatchlistEntry(t, h, "手动片")
	h.service.OnWatchlistDoubanBound(manual.ID, "502", "手动片", 2021)
	boundRow, _ := h.markRow("502")
	if boundRow.WatchlistEntryOrigin != models.MovieChartOriginEnrichment || boundRow.WatchlistEntryID != manual.ID {
		t.Fatalf("补全绑定应记 origin=enrichment: %+v", boundRow)
	}

	h.service.OnVideoWatchedChanged(video.ID, true)

	entries := h.watchlistEntries()
	if len(entries) != 1 || entries[0].ID != manual.ID {
		t.Fatalf("自动路径只应删 chart 条目、保留 enrichment 条目: %+v", entries)
	}
	for _, id := range []string{"501", "502"} {
		row, _ := h.markRow(id)
		if row.Mark != models.MovieChartMarkWatched || row.WatchlistEntryID != 0 || row.WatchlistEntryOrigin != "" {
			t.Fatalf("%s 应为 watched 且认领与来源清空: %+v", id, row)
		}
	}
}

// 手动路径语义不变：榜单上手动改标记 / 撤销 want，enrichment 条目照旧随之删除。
func TestAPP07ManualPathStillDeletesEnrichmentEntry(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	h.seedEntry("511", "手动片", "2021-10-22")
	h.seedEntry("512", "手动片二", "2021-10-22")
	for _, spec := range []struct{ id, title string }{{"511", "手动片"}, {"512", "手动片二"}} {
		entry := seedManualWatchlistEntry(t, h, spec.title)
		h.service.OnWatchlistDoubanBound(entry.ID, spec.id, spec.title, 2021)
	}
	if _, err := h.service.MarkEntry("511", models.MovieChartMarkWatched); err != nil {
		t.Fatal(err)
	}
	if err := h.service.ClearMark("512"); err != nil {
		t.Fatal(err)
	}
	if got := len(h.watchlistEntries()); got != 0 {
		t.Fatalf("手动路径应删除认领的条目，剩余 %d", got)
	}
}

// 释放认领（条目来源改选）时 origin 一并清空。
func TestAPP07ReleaseClearsEntryOrigin(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	h.seedEntry("521", "手动片", "2021-10-22")
	entry := seedManualWatchlistEntry(t, h, "手动片")
	h.service.OnWatchlistDoubanBound(entry.ID, "521", "手动片", 2021)
	h.service.OnWatchlistSourceChanged(entry.ID, "999")
	row, _ := h.markRow("521")
	if row.Mark != "" || row.WatchlistEntryID != 0 || row.WatchlistEntryOrigin != "" {
		t.Fatalf("释放后认领与来源应清空: %+v", row)
	}
}

// 删除条目撤销认领（revokeChartWantForEntry）同样清空 origin。
func TestAPP07DeleteEntryRevokeClearsOrigin(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	h.seedEntry("531", "手动片", "2021-10-22")
	entry := seedManualWatchlistEntry(t, h, "手动片")
	h.service.OnWatchlistDoubanBound(entry.ID, "531", "手动片", 2021)
	if err := h.watchlist.Delete(entry.ID); err != nil {
		t.Fatal(err)
	}
	row, _ := h.markRow("531")
	if row.Mark != "" || row.WatchlistEntryID != 0 || row.WatchlistEntryOrigin != "" {
		t.Fatalf("删除条目后认领与来源应清空: %+v", row)
	}
}
