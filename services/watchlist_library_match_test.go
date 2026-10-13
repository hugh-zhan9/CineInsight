package services

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

// D-P2-03 想看已在库：完全相等 + 年份相同 + 唯一才自动连；前缀、年份缺失、多部命中都不连；版本组算一部；
// 解除后不再自动连；豆瓣与非豆瓣各走各的表；三个触发时机；并发两轮不重复写。

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

func TestWatchlistDoubanAndOtherSourcesUseTheirOwnTables(t *testing.T) {
	f := newWatchlistMatchFixture(t)
	douban := f.entry("沙丘", 2021, "3001114")
	plain := f.entry("降临", 2016, "")
	doubanVideo := f.video("沙丘", intPtr(2021), nil)
	plainVideo := f.video("降临", intPtr(2016), nil)
	f.matchAll()
	if n := countLinkRows(t, &models.MovieVideoLink{}, "douban_id = ? AND video_id = ? AND origin = ?", "3001114", doubanVideo.ID, models.WatchlistLinkOriginAuto); n != 1 {
		t.Fatalf("豆瓣条目应写 movie_video_links(origin=auto): %d", n)
	}
	if n := countLinkRows(t, &models.WatchlistVideoLink{}, "watchlist_entry_id = ?", douban.ID); n != 0 {
		t.Fatalf("豆瓣条目不写 watchlist_video_links: %d", n)
	}
	if n := countLinkRows(t, &models.WatchlistVideoLink{}, "watchlist_entry_id = ? AND video_id = ? AND origin = ?", plain.ID, plainVideo.ID, models.WatchlistLinkOriginAuto); n != 1 {
		t.Fatalf("非豆瓣条目应写 watchlist_video_links(origin=auto): %d", n)
	}
	if n := countLinkRows(t, &models.MovieVideoLink{}, "video_id = ?", plainVideo.ID); n != 0 {
		t.Fatalf("非豆瓣条目不写 movie_video_links: %d", n)
	}

	// 榜单页手动关联的另一部（存量写法，origin 空串）读出来是 manual，与自动的合并显示。
	chart := &MovieChartService{db: database.DB}
	other := f.video("沙丘 IMAX", nil, nil)
	if err := chart.LinkMovieToVideo("3001114", other.ID); err != nil {
		t.Fatal(err)
	}
	linked := f.requireLinked("豆瓣条目读 movie_video_links", douban.ID, doubanVideo.ID, other.ID)
	if linked[0].Origin != models.WatchlistLinkOriginAuto || linked[1].Origin != models.WatchlistLinkOriginManual {
		t.Fatalf("来源标注不对: %+v", linked)
	}
	// 榜单解除不写 dismissed：想看的自动匹配记录不受影响。
	if err := chart.UnlinkMovieVideo("3001114", other.ID); err != nil {
		t.Fatal(err)
	}
	if n := countLinkRows(t, &models.WatchlistVideoLink{}, "origin = ?", models.WatchlistLinkOriginDismissed); n != 0 {
		t.Fatalf("UnlinkMovieVideo 不应写 dismissed: %d", n)
	}
}

func TestWatchlistUnlinkRecordsDismissedAndBlocksAutoRelink(t *testing.T) {
	f := newWatchlistMatchFixture(t)
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
	if n := countLinkRows(t, &models.MovieVideoLink{}, "douban_id = ?", "3001114"); n != 0 {
		t.Fatalf("豆瓣条目解除应删 movie_video_links 那一行: %d", n)
	}
	if relinked := f.matchAll(); relinked != 0 {
		t.Fatalf("解除过的一对不再自动关联: %d", relinked)
	}
	// 榜单那边又把这一对连上了：想看一侧仍按 dismissed 减掉。
	chart := &MovieChartService{db: database.DB}
	if err := chart.LinkMovieToVideo("3001114", doubanVideo.ID); err != nil {
		t.Fatal(err)
	}
	f.requireLinked("dismissed 减掉榜单的同一对", douban.ID)

	// 手动确认：清掉 dismissed，豆瓣写 movie_video_links（手动），其余写 manual。
	for _, pair := range []struct {
		entry models.WatchlistEntry
		video models.Video
	}{{douban, doubanVideo}, {plain, plainVideo}} {
		if err := f.service.LinkVideo(pair.entry.ID, pair.video.ID); err != nil {
			t.Fatal(err)
		}
		linked := f.requireLinked("手动确认", pair.entry.ID, pair.video.ID)
		if linked[0].Origin != models.WatchlistLinkOriginManual {
			t.Fatalf("手动确认应标 manual: %+v", linked)
		}
		if n := countLinkRows(t, &models.WatchlistVideoLink{}, "watchlist_entry_id = ? AND origin = ?", pair.entry.ID, models.WatchlistLinkOriginDismissed); n != 0 {
			t.Fatalf("手动确认应清掉 dismissed: %d", n)
		}
	}
	// 未关联的一对也能直接记 dismissed；不存在的条目 / 视频报错。
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

func TestWatchlistLibraryStateAndInLibraryList(t *testing.T) {
	f := newWatchlistMatchFixture(t)
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
		time.Sleep(2 * time.Millisecond) // 关联时间先后分明
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
	tooMany := make([]uint, watchlistLibraryStateMaxEntries+1)
	for i := range tooMany {
		tooMany[i] = uint(i + 1)
	}
	if _, err := f.service.ListLibraryState(tooMany); err == nil {
		t.Fatal("超过 200 条应报错")
	}

	items, err := f.service.ListInLibrary(0)
	if err != nil {
		t.Fatal(err)
	}
	got := []uint{}
	for _, item := range items {
		got = append(got, item.EntryID)
	}
	// 看过的、失效的不列；版本组播组代表；按关联时间倒序。
	requireUintSlice(t, "想看已在库", got, []uint{grouped.ID, second.ID, first.ID})
	if items[0].VideoID != groupMain.ID || items[1].Origin != models.WatchlistLinkOriginManual || items[2].VideoTitle != "沙丘" {
		t.Fatalf("条目内容不对: %+v", items)
	}
	if limited, err := f.service.ListInLibrary(1); err != nil || len(limited) != 1 {
		t.Fatalf("limit: %v %v", limited, err)
	}
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

	// 在线补全成功：写回年份后关联，且在进度事件发出之前已经连上（前端据事件刷新这一行）。
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
			table := &models.WatchlistVideoLink{}
			var model any = table
			query := "watchlist_entry_id = ? AND video_id = ? AND origin = 'auto'"
			args := []any{pending.ID, arrival.ID}
			if source == WatchlistMetadataSourceDouban {
				model, query, args = &models.MovieVideoLink{}, "douban_id = ? AND video_id = ? AND origin = 'auto'", []any{"3001116", arrival.ID}
			}
			if n := countLinkRows(t, model, query, args...); n != 1 {
				t.Fatalf("%s 补全后应自动关联: %d", source, n)
			}
		})
	}
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

	// 两轮同时跑（单条与全量各自进来）：轮次锁串行，唯一索引兜底，只有一行。
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
	if total != 1 || countLinkRows(t, &models.MovieVideoLink{}, "douban_id = ? AND video_id = ?", "3001117", otherVideo.ID) != 1 {
		t.Fatalf("并发轮次只应写一次: total=%d", total)
	}
	// 唯一索引冲突视为已关联，不报错。
	if created, err := writeAutoWatchlistLink(database.DB, other, otherVideo.ID); err != nil || created {
		t.Fatalf("冲突应视为已关联: created=%v err=%v", created, err)
	}
}
