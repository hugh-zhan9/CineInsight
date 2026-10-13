package services

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

// D-P2-03 想看已在库：完全相等 + 年份相同 + 唯一才自动连；前缀、年份缺失、多部命中都不连；版本组算一部；
// 关联只住 watchlist_video_links（与来源无关），自动关联不进 movie_video_links、不牵动榜单已看同步；
// 解除按作品生效、榜单解除也记 dismissed；触发时机；并发与竞争。

type watchlistMatchFixture struct {
	t       *testing.T
	service *WatchlistService
	count   int
}

func newWatchlistMatchFixture(t *testing.T) *watchlistMatchFixture {
	t.Helper()
	setupVideoServiceTestDB(t)
	return &watchlistMatchFixture{t: t, service: NewWatchlistService(t.TempDir())}
}

// withChart 像应用里一样把榜单服务挂上（片单页对豆瓣条目的关联 / 解除会同步到 movie_video_links）。
func (f *watchlistMatchFixture) withChart() *MovieChartService {
	f.t.Helper()
	return NewMovieChartService(database.DB, f.service)
}

func (f *watchlistMatchFixture) dismissed(entryID uint) []uint {
	f.t.Helper()
	return f.state(entryID).Dismissed
}

func (f *watchlistMatchFixture) entry(title string, year int, doubanID string) models.WatchlistEntry {
	f.t.Helper()
	entry := models.WatchlistEntry{Title: title, Kind: models.WatchlistKindMovie, Year: year, EnrichmentStatus: models.WatchlistEnrichmentManual}
	if doubanID != "" {
		entry.SourceName, entry.SourceItemID = WatchlistMetadataSourceDouban, doubanID
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		f.t.Fatal(err)
	}
	return entry
}

func (f *watchlistMatchFixture) video(displayTitle string, year *int, mutate func(*models.Video)) models.Video {
	f.t.Helper()
	f.count++
	video := models.Video{Name: fmt.Sprintf("file-%02d.mkv", f.count), DisplayTitle: displayTitle, Year: year,
		Path: fmt.Sprintf("/library/watchlist/%02d.mkv", f.count), Directory: "/library/watchlist", Size: 100, Duration: 6000}
	if mutate != nil {
		mutate(&video)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		f.t.Fatal(err)
	}
	return video
}

func (f *watchlistMatchFixture) matchAll() int {
	f.t.Helper()
	linked, err := f.service.runLibraryMatchRound(context.Background(), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return linked
}

func (f *watchlistMatchFixture) state(entryID uint) WatchlistLibraryState {
	f.t.Helper()
	states, err := f.service.ListLibraryState([]uint{entryID})
	if err != nil {
		f.t.Fatal(err)
	}
	return states[entryID]
}

func (f *watchlistMatchFixture) requireLinked(label string, entryID uint, want ...uint) []WatchlistLinkedVideo {
	f.t.Helper()
	linked := f.state(entryID).Linked
	got := make([]uint, 0, len(linked))
	for _, item := range linked {
		got = append(got, item.VideoID)
	}
	requireUintSlice(f.t, label, got, want)
	return linked
}

func countLinkRows(t *testing.T, model any, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Model(model).Where(query, args...).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWatchlistAutoMatchRequiresExactTitleSameYearAndOneWork(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	exact := f.entry("沙丘", 2021, "")
	exactVideo := f.video("沙丘", intPtr(2021), nil)
	f.video("沙丘2", intPtr(2021), nil) // 前缀：不算
	prefixOnly := f.entry("星际", 2014, "")
	f.video("星际穿越", intPtr(2014), nil)
	noEntryYear := f.entry("花样年华", 0, "")
	f.video("花样年华", intPtr(2000), nil)
	noVideoYear := f.entry("阳光灿烂的日子", 1994, "")
	f.video("阳光灿烂的日子", nil, nil)
	otherYear := f.entry("小妇人", 2019, "")
	f.video("小妇人", intPtr(1994), nil)
	twoWorks := f.entry("寄生虫", 2019, "")
	f.video("寄生虫", intPtr(2019), nil)
	f.video("", intPtr(2019), func(v *models.Video) { v.OriginalTitle = "寄生虫" })
	byFileName := f.entry("Dune: Part Two", 2024, "")
	fileVideo := f.video("", intPtr(2024), func(v *models.Video) { v.Name = "Dune Part Two.mkv" })
	byOriginal := f.entry("Arrival", 2016, "")
	originalVideo := f.video("降临", intPtr(2016), func(v *models.Video) { v.OriginalTitle = "ARRIVAL" })
	staleOnly := f.entry("失效片", 2001, "")
	f.video("失效片", intPtr(2001), func(v *models.Video) { v.IsStale = true })

	if linked := f.matchAll(); linked != 3 {
		t.Fatalf("应自动关联 3 条，实际 %d", linked)
	}
	if got := f.requireLinked("完全相等", exact.ID, exactVideo.ID); got[0].Origin != models.WatchlistLinkOriginAuto || got[0].Title != "沙丘" {
		t.Fatalf("自动关联应标 auto: %+v", got)
	}
	f.requireLinked("文件名去扩展名后规范化相等", byFileName.ID, fileVideo.ID)
	f.requireLinked("原始片名规范化相等", byOriginal.ID, originalVideo.ID)
	for label, entry := range map[string]models.WatchlistEntry{"前缀": prefixOnly, "条目没年份": noEntryYear, "视频没年份": noVideoYear,
		"年份不同": otherYear, "命中两部": twoWorks, "只有失效视频": staleOnly} {
		f.requireLinked(label, entry.ID)
	}
	if again := f.matchAll(); again != 0 {
		t.Fatalf("已关联的条目不再匹配: %d", again)
	}
}

func TestWatchlistAutoMatchCollapsesVersionGroupToRepresentative(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	entry := f.entry("银翼杀手2049", 2017, "")
	main := f.video("银翼杀手2049", intPtr(2017), nil)
	alt := f.video("银翼杀手 2049", intPtr(2017), nil)
	if _, err := NewVersionGroupService().Create(context.Background(), []uint{alt.ID, main.ID}, "银翼杀手2049"); err != nil {
		t.Fatal(err)
	}
	if linked := f.matchAll(); linked != 1 {
		t.Fatalf("版本组算一部，应关联: %d", linked)
	}
	f.requireLinked("关联到组代表（版本顺序第一位）", entry.ID, alt.ID)
}

func TestWatchlistLinksLiveInOneTableIndependentOfSource(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	chart := f.withChart()
	douban := f.entry("沙丘", 2021, "3001114")
	doubanVideo := f.video("沙丘", intPtr(2021), nil)
	f.matchAll()
	// 自动关联只写 watchlist_video_links，即使条目是豆瓣来源。
	if n := countLinkRows(t, &models.WatchlistVideoLink{}, "watchlist_entry_id = ? AND video_id = ? AND origin = ?", douban.ID, doubanVideo.ID, models.WatchlistLinkOriginAuto); n != 1 {
		t.Fatalf("自动关联应写 watchlist_video_links(origin=auto): %d", n)
	}
	if n := countLinkRows(t, &models.MovieVideoLink{}, "1 = 1"); n != 0 {
		t.Fatalf("自动关联不写 movie_video_links: %d", n)
	}
	// 榜单页建的关联（movie_video_links）对豆瓣条目读作 manual，与自动的合并显示。
	imax := f.video("沙丘 IMAX", nil, nil)
	if err := chart.LinkMovieToVideo("3001114", imax.ID); err != nil {
		t.Fatal(err)
	}
	linked := f.requireLinked("合并读", douban.ID, doubanVideo.ID, imax.ID)
	if linked[0].Origin != models.WatchlistLinkOriginAuto || linked[1].Origin != models.WatchlistLinkOriginManual {
		t.Fatalf("来源标注不对: %+v", linked)
	}

	// 来源在豆瓣与其他之间切换（补全重试、改选候选、榜单复用、改名清来源），片单一侧的关联都还在。
	plain := f.entry("Dune", 2021, "")
	remux := f.video("Dune 4K remux", intPtr(2021), nil) // 片名对不上，自动匹配找不回来
	if err := f.service.LinkVideo(plain.ID, remux.ID); err != nil {
		t.Fatal(err)
	}
	setSource := func(entryID uint, name, item string) {
		if err := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", entryID).
			Updates(map[string]any{"source_name": name, "source_item_id": item}).Error; err != nil {
			t.Fatal(err)
		}
	}
	setSource(plain.ID, WatchlistMetadataSourceDouban, "3001199")
	f.matchAll()
	f.requireLinked("非豆瓣 → 豆瓣", plain.ID, remux.ID)
	setSource(plain.ID, "tmdb", "438631")
	f.requireLinked("豆瓣 → 其他", plain.ID, remux.ID)
	setSource(douban.ID, "", "")
	f.requireLinked("豆瓣条目清掉来源：自动关联还在，榜单那行不再算它的", douban.ID, doubanVideo.ID)
}

// I-1：自动关联不进 movie_video_links，看完那个视频不会删掉榜单建的想看条目、不会把榜单标成已看。
func TestWatchlistAutoLinkDoesNotDriveChartWatchedSync(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	h.seedEntry("601", "沙丘", "2021-10-22")
	if _, err := h.service.MarkEntry("601", models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	entries := h.watchlistEntries()
	if len(entries) != 1 {
		t.Fatalf("想看应建一条条目: %+v", entries)
	}
	entry := entries[0]
	if err := h.db.Model(&models.WatchlistEntry{}).Where("id = ?", entry.ID).Update("year", 2021).Error; err != nil {
		t.Fatal(err)
	}
	year := 2021
	video := models.Video{Name: "Dune.mkv", DisplayTitle: "沙丘", Year: &year, Path: "/lib/dune.mkv", Directory: "/lib"}
	if err := h.db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if linked, err := h.watchlist.runLibraryMatchRound(context.Background(), nil); err != nil || linked != 1 {
		t.Fatalf("应自动关联: %d %v", linked, err)
	}
	if n := h.linkCount(); n != 0 {
		t.Fatalf("自动关联不该写 movie_video_links: %d", n)
	}
	h.service.OnVideoWatchedChanged(video.ID, true)
	if row, _ := h.markRow("601"); row.Mark != models.MovieChartMarkWant {
		t.Fatalf("榜单标记不该被自动关联改成已看: %q", row.Mark)
	}
	if got := h.watchlistEntries(); len(got) != 1 {
		t.Fatalf("想看条目不该被删: %+v", got)
	}

	// 用户在片单页手动确认的关联照旧同步到榜单（movie_video_links），已看双向同步照旧生效。
	if err := h.watchlist.LinkVideo(entry.ID, video.ID); err != nil {
		t.Fatal(err)
	}
	if n := h.linkCount(); n != 1 {
		t.Fatalf("手动确认的豆瓣条目应写 movie_video_links: %d", n)
	}
	h.service.OnVideoWatchedChanged(video.ID, true)
	if row, _ := h.markRow("601"); row.Mark != models.MovieChartMarkWatched {
		t.Fatalf("手动关联之后已看应同步到榜单: %q", row.Mark)
	}
}

// I-2：榜单页解除记 dismissed，下一轮不会再连回来（无论那一对原来是榜单页建的还是自动的）。
func TestChartUnlinkRecordsDismissalSoAutoMatchStaysOff(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	chart := f.withChart()
	fromChart := f.entry("沙丘", 2021, "3001114")
	chartVideo := f.video("沙丘", intPtr(2021), nil)
	if err := chart.LinkMovieToVideo("3001114", chartVideo.ID); err != nil {
		t.Fatal(err)
	}
	f.requireLinked("榜单页建的", fromChart.ID, chartVideo.ID)
	if err := chart.UnlinkMovieVideo("3001114", chartVideo.ID); err != nil {
		t.Fatal(err)
	}
	if relinked := f.matchAll(); relinked != 0 {
		t.Fatalf("榜单解除后自动匹配不该连回来: %d", relinked)
	}
	f.requireLinked("榜单解除后", fromChart.ID)
	requireUintSlice(t, "dismissed", f.dismissed(fromChart.ID), []uint{chartVideo.ID})

	auto := f.entry("降临", 2016, "3001115")
	autoVideo := f.video("降临", intPtr(2016), nil)
	plain := f.entry("降临", 2016, "")
	f.matchAll()
	f.requireLinked("自动关联", auto.ID, autoVideo.ID)
	if err := chart.UnlinkMovieVideo("3001115", autoVideo.ID); err != nil {
		t.Fatal(err)
	}
	f.matchAll()
	f.requireLinked("榜单解除盖过自动关联", auto.ID)
	f.requireLinked("别的来源的条目不受榜单解除影响", plain.ID, autoVideo.ID)
}

func TestWatchlistManualLinkAndUnlinkSyncDoubanToChart(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	f.withChart()
	douban := f.entry("沙丘", 2021, "3001114")
	plain := f.entry("降临", 2016, "")
	doubanVideo := f.video("沙丘", intPtr(2021), nil)
	plainVideo := f.video("降临", intPtr(2016), nil)
	f.matchAll()

	for _, pair := range []struct {
		entry models.WatchlistEntry
		video models.Video
	}{{douban, doubanVideo}, {plain, plainVideo}} {
		if err := f.service.UnlinkVideo(pair.entry.ID, pair.video.ID); err != nil {
			t.Fatal(err)
		}
		f.requireLinked("解除后", pair.entry.ID)
		if n := countLinkRows(t, &models.WatchlistVideoLink{}, "watchlist_entry_id = ? AND video_id = ? AND origin = ?", pair.entry.ID, pair.video.ID, models.WatchlistLinkOriginDismissed); n != 1 {
			t.Fatalf("应记 dismissed: %d", n)
		}
	}
	if relinked := f.matchAll(); relinked != 0 {
		t.Fatalf("解除过的一对不再自动关联: %d", relinked)
	}
	for _, pair := range []struct {
		entry models.WatchlistEntry
		video models.Video
	}{{douban, doubanVideo}, {plain, plainVideo}} {
		if err := f.service.LinkVideo(pair.entry.ID, pair.video.ID); err != nil {
			t.Fatal(err)
		}
		linked := f.requireLinked("手动确认", pair.entry.ID, pair.video.ID)
		if linked[0].Origin != models.WatchlistLinkOriginManual || len(f.dismissed(pair.entry.ID)) != 0 {
			t.Fatalf("手动确认应标 manual 并清掉 dismissed: %+v %v", linked, f.dismissed(pair.entry.ID))
		}
	}
	if n := countLinkRows(t, &models.MovieVideoLink{}, "douban_id = ? AND video_id = ?", "3001114", doubanVideo.ID); n != 1 {
		t.Fatalf("豆瓣条目手动确认应同步到 movie_video_links: %d", n)
	}
	if n := countLinkRows(t, &models.MovieVideoLink{}, "video_id = ?", plainVideo.ID); n != 0 {
		t.Fatalf("非豆瓣条目不写 movie_video_links: %d", n)
	}
	if err := f.service.UnlinkVideo(douban.ID, doubanVideo.ID); err != nil {
		t.Fatal(err)
	}
	if n := countLinkRows(t, &models.MovieVideoLink{}, "douban_id = ?", "3001114"); n != 0 {
		t.Fatalf("豆瓣条目解除应删掉 movie_video_links 那一行: %d", n)
	}

	stranger := f.video("别的片", nil, nil)
	if err := f.service.UnlinkVideo(plain.ID, stranger.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.LinkVideo(9999, plainVideo.ID); err != ErrWatchlistEntryNotFound {
		t.Fatalf("条目不存在: %v", err)
	}
	if err := f.service.LinkVideo(plain.ID, 9999); err != ErrMovieLinkVideoNotFound {
		t.Fatalf("视频不存在: %v", err)
	}
	if err := database.DB.Delete(&models.Video{}, stranger.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.service.LinkVideo(plain.ID, stranger.ID); err != ErrMovieLinkVideoNotFound {
		t.Fatalf("软删的视频不能关联: %v", err)
	}
}

// minor 2：解除按作品生效——解除过版本组里任一版本，自动匹配就不再连这部作品；手动确认清掉整部作品的解除。
func TestWatchlistDismissalIsPerWork(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	entry := f.entry("银翼杀手2049", 2017, "")
	rep := f.video("银翼杀手2049", intPtr(2017), nil)
	other := f.video("银翼杀手2049", intPtr(2017), nil)
	if _, err := NewVersionGroupService().Create(context.Background(), []uint{rep.ID, other.ID}, "银翼"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.LinkVideo(entry.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.UnlinkVideo(entry.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if linked := f.matchAll(); linked != 0 {
		t.Fatalf("解除过这部作品的一个版本，不该自动连它的代表: %d", linked)
	}
	f.requireLinked("作品级解除", entry.ID)
	requireUintSlice(t, "dismissed 含整部作品", f.dismissed(entry.ID), []uint{rep.ID, other.ID})

	// 榜单页之类的来源仍连着同一部作品的另一个版本：读的时候也按作品减掉。
	if err := database.DB.Create(&models.WatchlistVideoLink{WatchlistEntryID: entry.ID, VideoID: rep.ID, Origin: models.WatchlistLinkOriginAuto}).Error; err != nil {
		t.Fatal(err)
	}
	f.requireLinked("同一作品的其他版本也不显示", entry.ID)

	if err := f.service.LinkVideo(entry.ID, rep.ID); err != nil {
		t.Fatal(err)
	}
	f.requireLinked("手动确认清掉整部作品的解除", entry.ID, rep.ID)
	if got := f.dismissed(entry.ID); len(got) != 0 {
		t.Fatalf("dismissed 应清空: %v", got)
	}
}

// minor 3：作品任一版本看过就算看过，不进「想看的片片库里已有」。
func TestWatchlistInLibraryTreatsWorkWatchedIfAnyVersionWatched(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	entry := f.entry("银翼杀手2049", 2017, "")
	rep := f.video("银翼杀手2049 A", intPtr(2017), nil)
	other := f.video("银翼杀手2049 B", intPtr(2017), func(v *models.Video) { v.IsWatched = true })
	if _, err := NewVersionGroupService().Create(context.Background(), []uint{rep.ID, other.ID}, "银翼"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.LinkVideo(entry.ID, rep.ID); err != nil {
		t.Fatal(err)
	}
	items, err := f.service.ListInLibrary(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("作品有一个版本看过就不列: %+v", items)
	}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", other.ID).Update("is_watched", false).Error; err != nil {
		t.Fatal(err)
	}
	if items, err := f.service.ListInLibrary(0); err != nil || len(items) != 1 || items[0].VideoID != rep.ID {
		t.Fatalf("都没看过时应列出并播代表: %+v %v", items, err)
	}
	// 看过的那个版本离线（失效）了：看过仍是看过，不列；要播的代表在线就够了。
	if err := database.DB.Model(&models.Video{}).Where("id = ?", other.ID).Updates(map[string]any{"is_watched": true, "is_stale": true}).Error; err != nil {
		t.Fatal(err)
	}
	if items, err := f.service.ListInLibrary(0); err != nil || len(items) != 0 {
		t.Fatalf("离线版本看过也算看过: %+v %v", items, err)
	}
	// 代表离线、另一个版本在线且没看过：播在线的那个。
	if err := database.DB.Model(&models.Video{}).Where("id = ?", other.ID).Updates(map[string]any{"is_watched": false, "is_stale": false}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", rep.ID).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.service.LinkVideo(entry.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if items, err := f.service.ListInLibrary(0); err != nil || len(items) != 1 || items[0].VideoID != other.ID {
		t.Fatalf("代表离线时播在线的版本: %+v %v", items, err)
	}
}

func TestWatchlistLibraryStateAndInLibraryList(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	f.withChart()
	first := f.entry("沙丘", 2021, "")
	second := f.entry("降临", 2016, "3001115")
	watched := f.entry("看过了", 2010, "")
	stale := f.entry("失效了", 2011, "")
	grouped := f.entry("银翼杀手2049", 2017, "")
	firstVideo := f.video("沙丘", intPtr(2021), nil)
	secondVideo := f.video("降临", intPtr(2016), nil)
	watchedVideo := f.video("看过了", intPtr(2010), func(v *models.Video) { v.IsWatched = true })
	staleVideo := f.video("失效了", intPtr(2011), nil)
	groupMain := f.video("银翼杀手 2049 主版本", nil, nil)
	groupAlt := f.video("银翼杀手2049", intPtr(2017), nil)
	if _, err := NewVersionGroupService().Create(context.Background(), []uint{groupMain.ID, groupAlt.ID}, "银翼"); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]uint{{first.ID, firstVideo.ID}, {second.ID, secondVideo.ID}, {watched.ID, watchedVideo.ID}, {stale.ID, staleVideo.ID}, {grouped.ID, groupAlt.ID}} {
		if err := f.service.LinkVideo(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond) // 关联时间先后分明
	}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", staleVideo.ID).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}

	states, err := f.service.ListLibraryState([]uint{first.ID, second.ID, 9999})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := states[9999]; ok || len(states) != 2 || len(states[second.ID].Linked) != 1 || states[second.ID].Linked[0].Title != "降临" {
		t.Fatalf("状态应只含存在的条目: %+v", states)
	}
	if states[first.ID].Dismissed == nil || len(states[first.ID].Dismissed) != 0 {
		t.Fatalf("没解除过时 dismissed 是空数组: %#v", states[first.ID].Dismissed)
	}
	tooMany := make([]uint, watchlistLibraryStateMaxEntries+1)
	for i := range tooMany {
		tooMany[i] = uint(i + 1)
	}
	if _, err := f.service.ListLibraryState(tooMany); err == nil {
		t.Fatal("超过 200 条应报错")
	}

	inLibrary := func() []uint {
		items, err := f.service.ListInLibrary(0)
		if err != nil {
			t.Fatal(err)
		}
		ids := []uint{}
		for _, item := range items {
			ids = append(ids, item.EntryID)
		}
		return ids
	}
	items, _ := f.service.ListInLibrary(0)
	// 看过的、失效的不列；版本组播组代表；按关联时间倒序。
	requireUintSlice(t, "想看已在库", inLibrary(), []uint{grouped.ID, second.ID, first.ID})
	if items[0].VideoID != groupMain.ID || items[1].Origin != models.WatchlistLinkOriginManual || items[2].VideoTitle != "沙丘" {
		t.Fatalf("条目内容不对: %+v", items)
	}
	if limited, err := f.service.ListInLibrary(1); err != nil || len(limited) != 1 {
		t.Fatalf("limit: %v %v", limited, err)
	}
	// minor 6：解除后再手动关联，关联时间是这一次，排到最前。
	time.Sleep(5 * time.Millisecond)
	if err := f.service.UnlinkVideo(first.ID, firstVideo.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.LinkVideo(first.ID, firstVideo.ID); err != nil {
		t.Fatal(err)
	}
	requireUintSlice(t, "重新关联排到最前", inLibrary(), []uint{first.ID, grouped.ID, second.ID})
	// 软删的视频不算关联。
	if err := database.DB.Delete(&models.Video{}, firstVideo.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.requireLinked("软删视频不算", first.ID)
}

func TestWatchlistAutoMatchTriggersOnCreateRenameAndEnrichment(t *testing.T) {
	// 改名：条目带年份，改成与片库一致的片名后立刻关联。
	f := newWatchlistMatchFixture(t)
	video := f.video("沙丘", intPtr(2021), nil)
	entry := f.entry("沙丘（暂名）", 2021, "")
	if err := f.service.Update(entry.ID, "沙丘"); err != nil {
		t.Fatal(err)
	}
	f.requireLinked("改名触发", entry.ID, video.ID)

	// 新增：片名一致但还没年份，不关联（等补全）。
	created, err := f.service.Create("降临", "")
	if err != nil {
		t.Fatal(err)
	}
	f.video("降临", intPtr(2016), nil)
	f.requireLinked("新增时没有年份", created.ID)

	// 在线补全成功：写回年份后关联（豆瓣来源同样只写 watchlist_video_links），且在进度事件发出之前已经连上。
	for _, source := range []string{"tmdb", WatchlistMetadataSourceDouban} {
		t.Run(source, func(t *testing.T) {
			detail := WatchlistMetadataDetail{WatchlistMetadataCandidate: WatchlistMetadataCandidate{SourceItemID: "3001116", Title: "降临", Year: 2016}}
			harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
				WatchlistMetadataKindMovie: {staticWatchlistSource(source, detail)},
			}, nil)
			arrival := models.Video{Name: "arrival.mkv", DisplayTitle: "降临", Year: intPtr(2016), Path: "/library/arrival.mkv", Directory: "/library"}
			if err := database.DB.Create(&arrival).Error; err != nil {
				t.Fatal(err)
			}
			linkedAtEvent := -1
			harness.service.SetEnrichmentEventEmitter(func(progress WatchlistEnrichProgress) {
				if progress.Status == models.WatchlistEnrichmentSucceeded {
					states, _ := harness.service.ListLibraryState([]uint{progress.EntryID})
					linkedAtEvent = len(states[progress.EntryID].Linked)
				}
			})
			pending := mustCreateWatchlistEntry(t, "降临", models.WatchlistKindMovie)
			harness.service.runEnrichmentOnce(context.Background())
			if linkedAtEvent != 1 {
				t.Fatalf("补全成功的进度事件发出时应已关联: %d", linkedAtEvent)
			}
			if n := countLinkRows(t, &models.WatchlistVideoLink{}, "watchlist_entry_id = ? AND video_id = ? AND origin = ?", pending.ID, arrival.ID, models.WatchlistLinkOriginAuto); n != 1 {
				t.Fatalf("%s 补全后应自动关联: %d", source, n)
			}
			if n := countLinkRows(t, &models.MovieVideoLink{}, "1 = 1"); n != 0 {
				t.Fatalf("%s 自动关联不写 movie_video_links: %d", source, n)
			}
		})
	}
}

// minor 9：视频的片名 / 年份变了、回收站恢复之后，防抖再跑一轮后台匹配；连着几次变化只跑一轮。
func TestWatchlistMetadataChangesTriggerDebouncedRound(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	f.service.match.delay = 500 * time.Millisecond
	SetLibraryMatchInputListener(f.service.TriggerLibraryMatchSoon)
	t.Cleanup(func() {
		SetLibraryMatchInputListener(nil)
		f.service.StopLibraryMatch() // 防抖中的计时器不能在用例结束后再触发
	})
	waitLinked := func(label string, entryID uint, want ...uint) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			f.service.WaitLibraryMatch()
			if len(f.state(entryID).Linked) == len(want) {
				f.requireLinked(label, entryID, want...)
				return
			}
			if time.Now().After(deadline) {
				f.requireLinked(label, entryID, want...)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// 详情编辑改片名与年份（经 UpdateVideoTitleFields）。
	dune := f.entry("沙丘", 2021, "")
	duneVideo := f.video("Dune", nil, nil)
	dataDir := t.TempDir()
	details := NewVideoDetailService(NewPersonService(dataDir), NewCollectionService(dataDir))
	before := f.service.match.rounds.Load()
	year := 2021
	for _, title := range []string{"沙", "沙丘"} { // 两次改动，防抖合成一轮
		if _, err := details.UpdateVideoDetails(VideoDetailsUpdate{VideoID: duneVideo.ID, DisplayTitle: title, Year: &year}); err != nil {
			t.Fatal(err)
		}
	}
	waitLinked("详情编辑后", dune.ID, duneVideo.ID)
	if rounds := f.service.match.rounds.Load() - before; rounds != 1 {
		t.Fatalf("连着两次改动应只跑一轮: %d", rounds)
	}

	// 批量编辑年份。
	arrival := f.entry("降临", 2016, "")
	arrivalVideo := f.video("降临", nil, nil)
	value := 2016.0
	if err := writeBatchScalar(database.DB, BatchEditKindVideo, BatchEditFieldYear, &value, []uint{arrivalVideo.ID}); err != nil {
		t.Fatal(err)
	}
	waitLinked("批量改年份后", arrival.ID, arrivalVideo.ID)

	// 回收站恢复。
	restoredEntry := f.entry("restore-me", 2020, "")
	restoredVideo := p010Video(t, filepath.Join(t.TempDir(), "restore-me.mp4"), "restore")
	if err := database.DB.Model(&models.Video{}).Where("id = ?", restoredVideo.ID).UpdateColumn("year", 2020).Error; err != nil {
		t.Fatal(err)
	}
	svc := &VideoService{}
	if err := svc.DeleteVideo(restoredVideo.ID, true); err != nil {
		t.Fatal(err)
	}
	f.service.WaitLibraryMatch()
	f.requireLinked("删除之后", restoredEntry.ID)
	if _, err := svc.RestoreTrashEntry(p010VideoEntry(t, restoredVideo.ID).ID); err != nil {
		t.Fatal(err)
	}
	waitLinked("回收站恢复后", restoredEntry.ID, restoredVideo.ID)
}

func TestWatchlistBackgroundMatchIsSingleFlightWithRerun(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	entry := f.entry("沙丘", 2021, "")
	video := f.video("沙丘", intPtr(2021), nil)

	// 一轮正在跑（这里用持有轮次锁模拟）时连续触发十次：只起一个后台轮，外加一次「再跑」。
	f.service.match.runMu.Lock()
	before := f.service.match.rounds.Load()
	for i := 0; i < 10; i++ {
		f.service.TriggerLibraryMatch("scan")
	}
	f.service.match.runMu.Unlock()
	f.service.WaitLibraryMatch()
	if rounds := f.service.match.rounds.Load() - before; rounds != 2 {
		t.Fatalf("应只跑一轮加一次再跑，实际 %d 轮", rounds)
	}
	f.requireLinked("后台轮", entry.ID, video.ID)
	if n := countLinkRows(t, &models.WatchlistVideoLink{}, "watchlist_entry_id = ?", entry.ID); n != 1 {
		t.Fatalf("不应重复写: %d", n)
	}

	// 单条与全量同时跑：唯一索引兜底，只有一行。
	other := f.entry("降临", 2016, "3001117")
	otherVideo := f.video("降临", intPtr(2016), nil)
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids := []uint(nil)
			if i%2 == 0 {
				ids = []uint{other.ID}
			}
			linked, err := f.service.runLibraryMatchRound(context.Background(), ids)
			if err != nil {
				t.Error(err)
			}
			results <- linked
		}(i)
	}
	wg.Wait()
	close(results)
	total := 0
	for linked := range results {
		total += linked
	}
	if total != 1 || countLinkRows(t, &models.WatchlistVideoLink{}, "watchlist_entry_id = ? AND video_id = ?", other.ID, otherVideo.ID) != 1 {
		t.Fatalf("并发轮次只应写一次: total=%d", total)
	}
	if created, err := writeAutoWatchlistLink(database.DB, other.ID, otherVideo.ID); err != nil || created {
		t.Fatalf("冲突应视为已关联: created=%v err=%v", created, err)
	}

	// 退出之后不再起新轮。
	f.service.StopLibraryMatch()
	stopped := f.service.match.rounds.Load()
	f.service.TriggerLibraryMatch("scan")
	f.service.TriggerLibraryMatchSoon("video-metadata")
	f.service.WaitLibraryMatch()
	if f.service.match.rounds.Load() != stopped {
		t.Fatal("StopLibraryMatch 之后不该再跑")
	}
}

// minor 1：解除夹在一轮的中间（这一轮已经决定要连这一对），解除总是赢。
func TestWatchlistUnlinkRacingRoundKeepsDismissal(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	entry := f.entry("沙丘", 2021, "")
	video := f.video("沙丘", intPtr(2021), nil)
	f.service.match.beforeAutoWrite = func(entryID, videoID uint) {
		if err := f.service.UnlinkVideo(entryID, videoID); err != nil {
			t.Error(err)
		}
	}
	if linked := f.matchAll(); linked != 0 {
		t.Fatalf("解除抢先写入后，这一轮不该再连: %d", linked)
	}
	f.service.match.beforeAutoWrite = nil
	f.requireLinked("解除赢", entry.ID)
	if n := countLinkRows(t, &models.WatchlistVideoLink{}, "watchlist_entry_id = ? AND video_id = ? AND origin = ?", entry.ID, video.ID, models.WatchlistLinkOriginDismissed); n != 1 {
		t.Fatalf("应留下 dismissed: %d", n)
	}
	// 反过来：先自动连上，再解除。
	other := f.entry("降临", 2016, "")
	otherVideo := f.video("降临", intPtr(2016), nil)
	f.matchAll()
	if err := f.service.UnlinkVideo(other.ID, otherVideo.ID); err != nil {
		t.Fatal(err)
	}
	f.matchAll()
	f.requireLinked("先连后解除", other.ID)
}

// minor 7：单条同步匹配不等后台全量轮。
func TestWatchlistSingleEntryMatchDoesNotWaitForFullRound(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	entry := f.entry("沙丘", 2021, "")
	video := f.video("沙丘", intPtr(2021), nil)
	f.service.match.runMu.Lock() // 模拟一轮很慢的全量匹配
	defer f.service.match.runMu.Unlock()
	done := make(chan struct{})
	go func() {
		f.service.matchEntryToLibrary(entry.ID, "rename")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("单条匹配被全量轮挡住了")
	}
	f.requireLinked("单条匹配", entry.ID, video.ID)
}

// 复审 minor 1：豆瓣条目在片单页关联 / 解除时榜单服务没挂上，片单一侧写好、报出「榜单没同步」，重试补上。
func TestWatchlistDoubanLinkWithoutChartLinkerReportsAndRetries(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	entry := f.entry("沙丘", 2021, "3001114")
	video := f.video("Dune remux", intPtr(2021), nil)
	chart := &MovieChartService{db: database.DB} // 只用来造榜单页的关联，不挂到片单服务上
	if err := chart.LinkMovieToVideo("3001114", video.ID); err != nil {
		t.Fatal(err)
	}
	err := f.service.UnlinkVideo(entry.ID, video.ID)
	if !errors.Is(err, ErrWatchlistChartLinkerUnavailable) {
		t.Fatalf("没挂榜单服务时解除豆瓣条目应报错: %v", err)
	}
	f.requireLinked("片单一侧已解除", entry.ID)
	if n := countLinkRows(t, &models.MovieVideoLink{}, "douban_id = ? AND video_id = ?", "3001114", video.ID); n != 1 {
		t.Fatalf("榜单那一行还在（等重试）: %d", n)
	}
	f.service.SetChartLinker(chart)
	if err := f.service.UnlinkVideo(entry.ID, video.ID); err != nil {
		t.Fatalf("挂上之后重试应成功: %v", err)
	}
	if n := countLinkRows(t, &models.MovieVideoLink{}, "douban_id = ?", "3001114"); n != 0 {
		t.Fatalf("重试后榜单那一行应删掉: %d", n)
	}
	f.service.SetChartLinker(nil)
	if err := f.service.LinkVideo(entry.ID, video.ID); !errors.Is(err, ErrWatchlistChartLinkerUnavailable) {
		t.Fatalf("没挂榜单服务时关联豆瓣条目也应报出没同步: %v", err)
	}
	f.requireLinked("片单一侧已关联", entry.ID, video.ID)
	plain := f.entry("降临", 2016, "")
	if err := f.service.LinkVideo(plain.ID, video.ID); err != nil {
		t.Fatalf("非豆瓣条目不需要榜单服务: %v", err)
	}
}

// 复审 minor 2：榜单页重新关联清掉想看一侧对这部作品的「解除过」（与榜单页解除对称）。
func TestChartRelinkClearsWatchlistDismissal(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	chart := f.withChart()
	entry := f.entry("沙丘", 2021, "3001114")
	other := f.entry("沙丘", 2021, "")
	imax := f.video("沙丘 IMAX", nil, nil)
	alt := f.video("沙丘 IMAX 3D", nil, nil)
	if _, err := NewVersionGroupService().Create(context.Background(), []uint{imax.ID, alt.ID}, "沙丘 IMAX"); err != nil {
		t.Fatal(err)
	}
	if err := chart.LinkMovieToVideo("3001114", imax.ID); err != nil {
		t.Fatal(err)
	}
	if err := chart.UnlinkMovieVideo("3001114", imax.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.UnlinkVideo(other.ID, alt.ID); err != nil { // 别的条目自己的解除不受影响
		t.Fatal(err)
	}
	f.requireLinked("榜单解除后", entry.ID)
	// 重新关联同一部作品的另一个版本：整部作品的解除都清掉。
	if err := chart.LinkMovieToVideo("3001114", alt.ID); err != nil {
		t.Fatal(err)
	}
	f.requireLinked("榜单重新关联后", entry.ID, alt.ID)
	if got := f.dismissed(entry.ID); len(got) != 0 {
		t.Fatalf("重新关联应清掉这部作品的解除: %v", got)
	}
	requireUintSlice(t, "非豆瓣条目的解除不动", f.dismissed(other.ID), []uint{imax.ID, alt.ID})
}

// 复审 minor 4：条目改名删掉旧片名带来的自动关联（手动的留着），并按新片名重新匹配。
func TestWatchlistRenameDropsAutoLinksAndRematches(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	entry := f.entry("沙丘", 2021, "")
	dune := f.video("沙丘", intPtr(2021), nil)
	arrival := f.video("降临", intPtr(2021), nil)
	f.matchAll()
	f.requireLinked("改名前", entry.ID, dune.ID)
	if err := f.service.Update(entry.ID, "降临"); err != nil {
		t.Fatal(err)
	}
	linked := f.requireLinked("改名后按新片名", entry.ID, arrival.ID)
	if linked[0].Origin != models.WatchlistLinkOriginAuto {
		t.Fatalf("新关联应是自动的: %+v", linked)
	}

	manual := f.entry("银翼杀手", 2017, "")
	remux := f.video("Blade Runner remux", intPtr(2017), nil)
	auto := f.video("银翼杀手", intPtr(2017), nil)
	if err := f.service.LinkVideo(manual.ID, remux.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.WatchlistVideoLink{WatchlistEntryID: manual.ID, VideoID: auto.ID, Origin: models.WatchlistLinkOriginAuto}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.service.Update(manual.ID, "银翼杀手 2049"); err != nil {
		t.Fatal(err)
	}
	f.requireLinked("手动关联留着、自动关联删掉", manual.ID, remux.ID)
	// 只改大小写之外什么都不变的保存（片名没变）不动关联。
	if err := f.service.Update(manual.ID, "银翼杀手 2049"); err != nil {
		t.Fatal(err)
	}
	f.requireLinked("片名没变", manual.ID, remux.ID)
}
