package services

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 「查看全部」列表（ListRecapItems）：每一类的 total 与 GetRecap 页面上的数字相等，含已不在库中的项
// （available=false），顺序与页面上的样本 / 排行相同，翻页不重不漏。

func (f *recapFixture) list(now time.Time, request RecapListRequest) *RecapListPage {
	f.t.Helper()
	service := &RecapService{now: func() time.Time { return now }, loc: recapTestZone}
	page, err := service.ListRecapItems(context.Background(), request)
	if err != nil {
		f.t.Fatalf("ListRecapItems(%+v) 失败: %v", request, err)
	}
	return page
}

// recapListEntry 把一页里不同形状的条目拍平成「键 + 能否打开」，便于比较顺序与口径。
type recapListEntry struct {
	key       string
	available bool
}

func recapListEntries(page *RecapListPage) []recapListEntry {
	var entries []recapListEntry
	add := func(key string, available bool) { entries = append(entries, recapListEntry{key, available}) }
	for _, item := range page.Videos {
		add(fmt.Sprintf("v%d", item.ID), item.Available)
	}
	for _, item := range page.VideoRanks {
		add(fmt.Sprintf("v%d", item.Video.ID), item.Video.Available)
	}
	for _, item := range page.ImageRanks {
		add(fmt.Sprintf("i%d", item.Image.ID), item.Image.Available)
	}
	for _, item := range page.Favorites {
		add(fmt.Sprintf("%s%d", item.Kind[:1], item.ID), item.Available)
	}
	for _, item := range page.Diary {
		add(fmt.Sprintf("d%d", item.ID), true)
	}
	for _, item := range page.Feed {
		add(fmt.Sprintf("%s%d@%s", item.Kind[:1], item.ID, item.At.Format(time.RFC3339)), item.Available)
	}
	for _, item := range page.Tags {
		add(fmt.Sprintf("t%d", item.Tag.ID), true)
	}
	for _, item := range page.Entities {
		add(fmt.Sprintf("e%d", item.Entity.ID), true)
	}
	return entries
}

// listAll 以 limit 一页翻到底：每页 has_more、offset 与条数都要对；与按最大页（200）翻到底拼出的结果相同。
func (f *recapFixture) listAll(now time.Time, request RecapListRequest, limit int) (int64, []recapListEntry) {
	f.t.Helper()
	total, whole := f.listPages(now, request, recapListMaxLimit)
	_, paged := f.listPages(now, request, limit)
	if fmt.Sprint(paged) != fmt.Sprint(whole) || int64(len(paged)) != total {
		f.t.Fatalf("%s 按 %d 条一页拼起来应与按 200 条一页相同（共 %d）: %d / %d 条", request.Kind, limit, total, len(paged), len(whole))
	}
	return total, paged
}

func (f *recapFixture) listPages(now time.Time, request RecapListRequest, limit int) (int64, []recapListEntry) {
	f.t.Helper()
	var all []recapListEntry
	var total int64 = -1
	for offset := 0; ; offset += limit {
		request.Offset, request.Limit = offset, limit
		page := f.list(now, request)
		entries := recapListEntries(page)
		if page.Kind != request.Kind || page.Offset != offset || (total >= 0 && page.Total != total) {
			f.t.Fatalf("%s 第 %d 条起的一页元数据不对: kind=%s offset=%d total=%d", request.Kind, offset, page.Kind, page.Offset, page.Total)
		}
		total = page.Total
		if page.HasMore != (int64(offset+len(entries)) < page.Total) || (page.HasMore && len(entries) != limit) {
			f.t.Fatalf("%s 第 %d 条起 has_more=%v 条数=%d total=%d", request.Kind, offset, page.HasMore, len(entries), page.Total)
		}
		all = append(all, entries...)
		if !page.HasMore {
			return total, all
		}
	}
}

// recapAvailablePrefix 是完整列表里能打开的前 n 个的键：页面上的样本 / 排行应正好是它们。
func recapAvailablePrefix(entries []recapListEntry, n int) []string {
	keys := []string{}
	for _, entry := range entries {
		if entry.available && len(keys) < n {
			keys = append(keys, entry.key)
		}
	}
	return keys
}

func recapVideoKeys(videos []RecapVideo) []string {
	keys := []string{}
	for _, video := range videos {
		keys = append(keys, fmt.Sprintf("v%d", video.ID))
	}
	return keys
}

func (f *recapFixture) remove(model any, id uint, deleted bool) {
	f.t.Helper()
	var err error
	if deleted {
		err = database.DB.Delete(model, id).Error
	} else {
		err = database.DB.Model(model).Where("id = ?", id).Update("is_stale", true).Error
	}
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *recapFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if err := database.DB.Exec(sql, args...).Error; err != nil {
		f.t.Fatal(err)
	}
}

func recapUnavailable(entries []recapListEntry) int {
	count := 0
	for _, entry := range entries {
		if !entry.available {
			count++
		}
	}
	return count
}

// seedRecapListMonth 在 2026 年 10 月造一期比页面展示上限多的数据：14 部视频（前 8 部期前就看过 = 重温，
// 第 9、10 部已删除 / 已移除）+ 1 部只有补记的、15 张图片（两张不在库中）、12 个标签、7 个人物、6 个作品集、
// 12 个新收藏（一部已移除的视频照算，回收站里的不算）、9 条带评分的日记。
func seedRecapListMonth(f *recapFixture) {
	before := recapAt(2026, time.September, 30, 22, 0)
	videos := make([]models.Video, 14)
	for i := range videos {
		videos[i] = f.video(fmt.Sprintf("视频%02d.mp4", i))
		for n := 0; n <= i%3; n++ {
			f.play(videos[i].ID, recapAt(2026, time.October, 1+i, 20, n), models.PlayEventSourceDesktopPlay)
		}
		if i < 8 {
			f.play(videos[i].ID, before, models.PlayEventSourceDesktopPlay)
		}
		if i < 12 {
			tag := f.tag(fmt.Sprintf("标签%02d", i), "")
			f.tagVideo(videos[i].ID, tag)
		}
		if i < 7 {
			person := models.Person{DisplayName: fmt.Sprintf("人物%d", i)}
			if err := database.DB.Create(&person).Error; err != nil {
				f.t.Fatal(err)
			}
			f.exec("INSERT INTO video_people (video_id, person_id, created_at) VALUES (?, ?, ?)", videos[i].ID, person.ID, before)
		}
		if i < 6 {
			collection := models.MediaCollection{Name: fmt.Sprintf("作品集%d", i), NormalizedName: fmt.Sprintf("作品集%d", i)}
			if err := database.DB.Create(&collection).Error; err != nil {
				f.t.Fatal(err)
			}
			f.exec("INSERT INTO collection_videos (collection_id, video_id, position, created_at) VALUES (?, ?, 1, ?)", collection.ID, videos[i].ID, before)
		}
		if i >= 2 && i <= 9 {
			f.favorite(&models.Video{}, videos[i].ID, recapAt(2026, time.October, 1+i, 10, 0))
		}
	}
	f.tagVideo(videos[0].ID, f.tag("自动标签", "auto"))
	legacy := f.video("补记.mp4")
	f.play(legacy.ID, recapAt(2026, time.October, 20, 9, 0), models.PlayEventSourceLegacy)
	f.remove(&models.Video{}, videos[8].ID, true)
	f.remove(&models.Video{}, videos[9].ID, false)
	images := make([]models.Image, 15)
	for i := range images {
		images[i] = f.image(fmt.Sprintf("图片%02d.jpg", i))
		for n := 0; n <= i%2; n++ {
			f.view(images[i].ID, recapAt(2026, time.October, 1+i, 21, n), models.ImageViewSourceDesktopViewer)
		}
		if i >= 1 && i <= 6 {
			f.favorite(&models.Image{}, images[i].ID, recapAt(2026, time.October, 1+i, 10, 0))
		}
	}
	f.remove(&models.Image{}, images[0].ID, false)
	f.remove(&models.Image{}, images[1].ID, true)
	rating := 8.5
	for day := 1; day <= 9; day++ {
		f.diary(fmt.Sprintf("日记%d", day), fmt.Sprintf("2026-10-%02d", day), &rating, nil)
	}
	f.diary("没评分", "2026-10-03", nil, nil)
}

// 每一类：total 等于页面上的数字（且比页面上展示的多）；完整列表里能打开的前 N 个正好是页面上的样本 / 排行
// （同一个顺序）；新发现只列在库项，其余种类保留 unavailable；按 4 条一页翻到底不重不漏。
func TestRecapListTotalsMatchPageCountsAndIncludeUnavailable(t *testing.T) {
	f := newRecapFixture(t)
	seedRecapListMonth(f)
	now := recapAt(2026, time.November, 15, 12, 0)
	result := f.recap(now, RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	fresh := result.Freshness
	keys := struct{ top, images, favorites, diary, tags, people, collections []string }{}
	for _, item := range result.TopVideos {
		keys.top = append(keys.top, fmt.Sprintf("v%d", item.Video.ID))
	}
	for _, item := range result.TopImages {
		keys.images = append(keys.images, fmt.Sprintf("i%d", item.Image.ID))
	}
	for _, item := range fresh.NewFavoriteSamples {
		keys.favorites = append(keys.favorites, fmt.Sprintf("%s%d", item.Kind[:1], item.ID))
	}
	for _, item := range fresh.DiaryRatedSamples {
		keys.diary = append(keys.diary, fmt.Sprintf("d%d", item.ID))
	}
	for _, item := range result.TopTags {
		keys.tags = append(keys.tags, fmt.Sprintf("t%d", item.Tag.ID))
	}
	for _, item := range result.TopPeople {
		keys.people = append(keys.people, fmt.Sprintf("e%d", item.Entity.ID))
	}
	for _, item := range result.TopCollections {
		keys.collections = append(keys.collections, fmt.Sprintf("e%d", item.Entity.ID))
	}
	cases := []struct {
		kind        string
		total, want int64
		shown       []string
		unavailable int
	}{
		{RecapListNew, 4, 4, recapVideoKeys(fresh.NewSamples), 0},
		{RecapListRewatched, fresh.RewatchedVideos, 8, recapVideoKeys(fresh.RewatchedSamples), 0},
		{RecapListVideos, result.Overview.Videos, 15, keys.top, 2},
		{RecapListImages, result.Overview.Images, 15, keys.images, 2},
		{RecapListFavorites, fresh.NewFavoriteVideos + fresh.NewFavoriteImages, 12, keys.favorites, 1},
		{RecapListDiary, fresh.DiaryRated, 9, keys.diary, 0},
		{RecapListTags, result.TopTagsTotal, 12, keys.tags, 0},
		{RecapListPeople, result.TopPeopleTotal, 7, keys.people, 0},
		{RecapListCollections, result.TopCollectionsTotal, 6, keys.collections, 0},
	}
	for _, c := range cases {
		if c.total != c.want || c.total < int64(len(c.shown)) {
			t.Fatalf("%s: 列表数字应为 %d 且不少于展示的 %d 个，实际 %d", c.kind, c.want, len(c.shown), c.total)
		}
		total, entries := f.listAll(now, RecapListRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10, Kind: c.kind}, 4)
		if total != c.total {
			t.Fatalf("%s: 列表 total %d 应等于页面上的 %d", c.kind, total, c.total)
		}
		if got := recapAvailablePrefix(entries, len(c.shown)); fmt.Sprint(got) != fmt.Sprint(c.shown) {
			t.Fatalf("%s: 列表顺序应与页面一致: 列表里能打开的前 %d 个 %v，页面 %v", c.kind, len(c.shown), got, c.shown)
		}
		if got := recapUnavailable(entries); got != c.unavailable {
			t.Fatalf("%s: 不在库中的应有 %d 个（标 available=false），实际 %d: %v", c.kind, c.unavailable, got, entries)
		}
	}
}

// 实时流水：total 是今天的全部事件（= 播放次数 + 看图次数），含已删除视频的那条（灰显）；能打开的前 30 条
// 就是页面上的今日流水。limit 不传取 60，超过 200 按 200。
func TestRecapListLiveFeedCoversTheWholeDay(t *testing.T) {
	f := newRecapFixture(t)
	video, gone, image := f.video("今天.mp4"), f.video("删了.mp4"), f.image("今天.jpg")
	f.play(video.ID, recapAt(2026, time.October, 7, 23, 0), models.PlayEventSourceDesktopPlay)
	for n := 0; n < 203; n++ {
		f.play(video.ID, recapAt(2026, time.October, 8, 0, n), models.PlayEventSourceDesktopPlay)
	}
	f.play(video.ID, recapAt(2026, time.October, 8, 22, 40), models.PlayEventSourceDesktopPlay)
	f.view(image.ID, recapAt(2026, time.October, 8, 22, 40), models.ImageViewSourceDesktopViewer)
	f.view(image.ID, recapAt(2026, time.October, 8, 22, 30), models.ImageViewSourceDesktopViewer)
	f.play(gone.ID, recapAt(2026, time.October, 8, 22, 50), models.PlayEventSourceDesktopPlay)
	f.remove(&models.Video{}, gone.ID, true)
	now := recapAt(2026, time.October, 8, 23, 0)
	result := f.recap(now, RecapRequest{Period: RecapPeriodLive})
	total, entries := f.listAll(now, RecapListRequest{Period: RecapPeriodLive, Kind: RecapListFeed}, 50)
	if want := result.Overview.Plays + result.Overview.ImageViews; total != want || want != 207 {
		t.Fatalf("今日流水 total 应为今天全部 207 条事件，实际 %d（页面 %d）", total, want)
	}
	var shown []string
	for _, item := range result.Feed {
		shown = append(shown, fmt.Sprintf("%s%d@%s", item.Kind[:1], item.ID, item.At.Format(time.RFC3339)))
	}
	if got := recapAvailablePrefix(entries, len(shown)); len(shown) != recapFeedLimit || fmt.Sprint(got) != fmt.Sprint(shown) {
		t.Fatalf("能打开的前 30 条应是页面上的今日流水: %v / %v", got, shown)
	}
	if entries[0].key != fmt.Sprintf("v%d@%s", gone.ID, recapAt(2026, time.October, 8, 22, 50).Format(time.RFC3339)) || entries[0].available {
		t.Fatalf("已删除视频的那条应在最前且不可打开: %v", entries[0])
	}
	if entries[1].key[0] != 'v' || entries[2].key[0] != 'i' {
		t.Fatalf("同一时刻视频在前: %v", entries[:3])
	}
	if got := f.list(now, RecapListRequest{Period: RecapPeriodLive, Kind: RecapListFeed}); len(got.Feed) != recapListDefaultLimit || !got.HasMore {
		t.Fatalf("limit 不传应取 60 条: %d", len(got.Feed))
	}
	if got := f.list(now, RecapListRequest{Period: RecapPeriodLive, Kind: RecapListFeed, Limit: 1000}); len(got.Feed) != recapListMaxLimit {
		t.Fatalf("limit 最多 200: %d", len(got.Feed))
	}
}

// 非法种类、负的 offset、非法期次报错；翻过末尾得到空页（各切片是空数组，不是 null）。
func TestRecapListRejectsBadRequestsAndPagesPastTheEnd(t *testing.T) {
	f := newRecapFixture(t)
	video := f.video("一部.mp4")
	f.play(video.ID, recapAt(2026, time.October, 3, 20, 0), models.PlayEventSourceDesktopPlay)
	now := recapAt(2026, time.November, 15, 12, 0)
	service := &RecapService{now: func() time.Time { return now }, loc: recapTestZone}
	bad := []struct {
		request RecapListRequest
		want    error
	}{
		{RecapListRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10, Kind: "bogus"}, ErrRecapListKindInvalid},
		{RecapListRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10, Kind: ""}, ErrRecapListKindInvalid},
		{RecapListRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10, Kind: RecapListVideos, Offset: -1}, ErrRecapListOffsetInvalid},
		{RecapListRequest{Period: RecapPeriodMonth, Year: 2026, Month: 13, Kind: RecapListVideos}, ErrRecapInvalidPeriod},
	}
	for _, c := range bad {
		if _, err := service.ListRecapItems(context.Background(), c.request); !errors.Is(err, c.want) {
			t.Fatalf("%+v 应报 %v，实际 %v", c.request, c.want, err)
		}
	}
	for kind := range recapListKinds {
		page := f.list(now, RecapListRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10, Kind: kind, Offset: 5})
		if page.HasMore || len(recapListEntries(page)) != 0 || page.Videos == nil || page.VideoRanks == nil || page.ImageRanks == nil ||
			page.Favorites == nil || page.Diary == nil || page.Feed == nil || page.Tags == nil || page.Entities == nil {
			t.Fatalf("%s 翻过末尾应是空页、切片都是空数组: %+v", kind, page)
		}
	}
}

func TestRecapListCalendarDayFiltersBeforePaging(t *testing.T) {
	f := newRecapFixture(t)
	v, image := f.video("日历视频.mp4"), f.image("日历图片.jpg")
	for _, at := range []time.Time{recapAt(2026, time.October, 7, 23, 59), recapAt(2026, time.October, 8, 0, 0), recapAt(2026, time.October, 8, 23, 59), recapAt(2026, time.October, 9, 0, 0)} {
		f.play(v.ID, at, models.PlayEventSourceDesktopPlay)
	}
	f.view(image.ID, recapAt(2026, time.October, 8, 12, 0), models.ImageViewSourceDesktopViewer)
	now := recapAt(2026, time.November, 1, 12, 0)
	request := RecapListRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10, Date: "2026-10-08", Kind: RecapListFeed, Limit: 2}
	first := f.list(now, request)
	if first.Total != 3 || !first.HasMore || len(first.Feed) != 2 || first.Feed[0].At.Hour() != 23 || first.Feed[1].Kind != RecapActivityImage {
		t.Fatalf("day first page: %+v", first)
	}
	request.Offset = 2
	last := f.list(now, request)
	if last.HasMore || len(last.Feed) != 1 || last.Feed[0].At.Hour() != 0 {
		t.Fatalf("day last page: %+v", last)
	}
	request.Date, request.Offset = "2026-10-10", 0
	empty := f.list(now, request)
	if empty.Total != 0 || empty.HasMore || len(empty.Feed) != 0 {
		t.Fatalf("empty day: %+v", empty)
	}
	request.Date = ""
	all := f.list(now, request)
	if all.Total != 5 {
		t.Fatalf("legacy monthly request: %+v", all)
	}
}

func TestRecapListCalendarDateValidationAndDST(t *testing.T) {
	f := newRecapFixture(t)
	now := recapAt(2026, time.October, 15, 12, 0)
	svc := &RecapService{now: func() time.Time { return now }, loc: recapTestZone}
	for _, request := range []RecapListRequest{
		{Period: RecapPeriodMonth, Year: 2026, Month: 10, Kind: RecapListFeed, Date: "2026-10-32"},
		{Period: RecapPeriodMonth, Year: 2026, Month: 10, Kind: RecapListFeed, Date: "2026-9-30"},
		{Period: RecapPeriodMonth, Year: 2026, Month: 10, Kind: RecapListFeed, Date: "2026-09-30"},
		{Period: RecapPeriodMonth, Year: 2026, Month: 10, Kind: RecapListFeed, Date: "2026-10-16"},
		{Period: RecapPeriodYear, Year: 2026, Kind: RecapListVideos, Date: "2026-10-08"},
	} {
		if _, err := svc.ListRecapItems(context.Background(), request); !errors.Is(err, ErrRecapListDateInvalid) {
			t.Fatalf("%+v: %v", request, err)
		}
	}
	v := f.video("DST.mp4")
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"2026-03-08", "2026-11-01"} {
		start, err := time.ParseInLocation("2006-01-02", date, loc)
		if err != nil {
			t.Fatal(err)
		}
		end := start.AddDate(0, 0, 1)
		f.play(v.ID, start.Add(-time.Second), models.PlayEventSourceDesktopPlay)
		f.play(v.ID, start, models.PlayEventSourceDesktopPlay)
		f.play(v.ID, end.Add(-time.Second), models.PlayEventSourceDesktopPlay)
		f.play(v.ID, end, models.PlayEventSourceDesktopPlay)
		service := &RecapService{now: func() time.Time { return end.Add(time.Hour) }, loc: loc}
		page, err := service.ListRecapItems(context.Background(), RecapListRequest{Period: RecapPeriodYear, Year: 2026, Kind: RecapListFeed, Date: date})
		if err != nil || page.Total != 2 {
			t.Fatalf("DST day %s: %+v / %v", date, page, err)
		}
	}
}
