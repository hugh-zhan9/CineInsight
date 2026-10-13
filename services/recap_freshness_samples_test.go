package services

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 新鲜度四类样本（新发现 / 重温 / 新收藏 / 日记评分）：与计数同口径，最多 6 个，只展示仍可打开的媒体，
// 重温、新收藏、日记按本期内最近一次在前。

func (f *recapFixture) favorite(model any, id uint, at time.Time) {
	f.t.Helper()
	if err := database.DB.Model(model).Where("id = ?", id).Updates(map[string]any{"is_favorite": true, "favorited_at": at}).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f *recapFixture) diary(title, watchedOn string, rating *float64, videoID *uint) models.ViewingDiaryEntry {
	f.t.Helper()
	entry := models.ViewingDiaryEntry{Title: title, WatchedOn: watchedOn, Rating: rating, VideoID: videoID, Revision: 1}
	if err := database.DB.Create(&entry).Error; err != nil {
		f.t.Fatal(err)
	}
	return entry
}

func recapSampleIDs(videos []RecapVideo) []uint {
	ids := make([]uint, 0, len(videos))
	for _, video := range videos {
		ids = append(ids, video.ID)
	}
	return ids
}

// 重温样本：只取仍在库的、按期内最后一次播放从近到远（同一时刻 ID 小的在前），最多 6；
// 期前的播放只用来判重温，期末之后的播放不影响「最后一次」。月度、年度、实时都给。
func TestRecapRewatchedSamplesNewestFirstCappedAndAvailableOnly(t *testing.T) {
	f := newRecapFixture(t)
	before := recapAt(2026, time.September, 30, 23, 59)
	videos := make([]models.Video, 9)
	for index := range videos {
		videos[index] = f.video(fmt.Sprintf("重温%d.mp4", index))
		f.play(videos[index].ID, before, models.PlayEventSourceDesktopPlay)
	}
	// videos[0]：期内第一刻播一次（最旧）；videos[1..7]：10 月 2..8 日各播一次，越往后越新；
	// videos[8] 与 videos[7] 同一时刻（并列按 ID 小的在前）。videos[1] 在下个月第一刻又播了一次，不算本期。
	f.play(videos[0].ID, recapAt(2026, time.October, 1, 0, 0), models.PlayEventSourceDesktopPlay)
	for index := 1; index <= 7; index++ {
		f.play(videos[index].ID, recapAt(2026, time.October, index+1, 20, 0), models.PlayEventSourceDesktopPlay)
	}
	f.play(videos[8].ID, recapAt(2026, time.October, 8, 20, 0), models.PlayEventSourceDesktopPlay)
	f.play(videos[1].ID, recapAt(2026, time.November, 1, 0, 0), models.PlayEventSourceDesktopPlay)
	// videos[1] 期内还多播两次：次数最多也不该改变「按最近一次」的顺序。
	f.play(videos[1].ID, recapAt(2026, time.October, 1, 9, 0), models.PlayEventSourceDesktopPlay)
	f.play(videos[1].ID, recapAt(2026, time.October, 1, 10, 0), models.PlayEventSourceDesktopPlay)
	// 已删除、已移除的重温片在期内最晚播放：照样计数，但不进样本。
	deleted, removed := f.video("已删除重温.mp4"), f.video("已移除重温.mp4")
	for _, video := range []models.Video{deleted, removed} {
		f.play(video.ID, before, models.PlayEventSourceDesktopPlay)
		f.play(video.ID, recapAt(2026, time.October, 30, 22, 0), models.PlayEventSourceDesktopPlay)
	}
	if err := database.DB.Delete(&models.Video{}, deleted.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", removed.ID).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	// 期内只有 legacy 播放的不算重温；新片不进重温样本。
	legacy, fresh := f.video("老账.mp4"), f.video("新片.mp4")
	f.play(legacy.ID, before, models.PlayEventSourceDesktopPlay)
	f.play(legacy.ID, recapAt(2026, time.October, 31, 23, 0), models.PlayEventSourceLegacy)
	f.play(fresh.ID, recapAt(2026, time.October, 31, 23, 30), models.PlayEventSourceDesktopPlay)
	now := recapAt(2026, time.November, 15, 12, 0)

	month := f.recap(now, RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10}).Freshness
	if month.RewatchedVideos != 11 || month.NewVideos != 1 {
		t.Fatalf("重温应计 11 部（含已删除、已移除），新发现 1 部: %+v", month)
	}
	want := []uint{videos[7].ID, videos[8].ID, videos[6].ID, videos[5].ID, videos[4].ID, videos[3].ID}
	if got := recapSampleIDs(month.RewatchedSamples); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("重温样本应按期内最后一次播放从近到远、最多 6: got %v want %v", got, want)
	}
	for _, sample := range month.RewatchedSamples {
		if !sample.Available || sample.Title == "" {
			t.Fatalf("重温样本应水合且可打开: %+v", sample)
		}
	}
	if len(month.NewSamples) != 1 || month.NewSamples[0].ID != fresh.ID {
		t.Fatalf("新发现样本不受影响: %+v", month.NewSamples)
	}

	// 9 月只有期前那一条播放：没有重温。
	september := f.recap(now, RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 9}).Freshness
	if september.RewatchedVideos != 0 || len(september.RewatchedSamples) != 0 || september.RewatchedSamples == nil {
		t.Fatalf("9 月不应有重温，样本是空数组: %+v", september)
	}

	// 年度：期前（2026 年之前）没有事件，全部是新发现，没有重温。
	year := f.recap(now, RecapRequest{Period: RecapPeriodYear, Year: 2026}).Freshness
	if year.RewatchedVideos != 0 || len(year.RewatchedSamples) != 0 {
		t.Fatalf("2026 年开始前没有事件，不应有重温: %+v", year)
	}

	// 实时：今天的播放 + 今天之前有记录的是重温。
	live := f.recap(recapAt(2026, time.October, 8, 23, 0), RecapRequest{Period: RecapPeriodLive}).Freshness
	if live.RewatchedVideos != 2 || fmt.Sprint(recapSampleIDs(live.RewatchedSamples)) != fmt.Sprint([]uint{videos[7].ID, videos[8].ID}) {
		t.Fatalf("实时的重温样本不对: %+v", live)
	}
}

// 新收藏样本：视频与图片合并，按 favorited_at 从近到远（同一时刻视频在前），最多 6；
// 期初第一刻算本期、下期第一刻不算；已失效的计数但不进样本，进回收站的两边都不算。
func TestRecapNewFavoriteSamplesMergeVideosAndImagesNewestFirst(t *testing.T) {
	f := newRecapFixture(t)
	// 10 月 1 日（实时用）与 10 月 3 日各有一次播放，让各期都不是空期。
	anchor := f.video("锚点.mp4")
	f.play(anchor.ID, recapAt(2026, time.October, 1, 8, 0), models.PlayEventSourceDesktopPlay)
	f.play(anchor.ID, recapAt(2026, time.October, 3, 20, 0), models.PlayEventSourceDesktopPlay)
	at := func(day, hour int) time.Time { return recapAt(2026, time.October, day, hour, 0) }

	first := f.video("期初.mp4")
	f.favorite(&models.Video{}, first.ID, recapAt(2026, time.October, 1, 0, 0))
	titled := models.Video{Name: "titled.mp4", DisplayTitle: "显示标题", Path: filepath.Join(f.root, "titled.mp4"), Directory: f.root, Duration: 600}
	if err := database.DB.Create(&titled).Error; err != nil {
		t.Fatal(err)
	}
	f.favorite(&models.Video{}, titled.ID, at(20, 21))
	sameTimeVideo, sameTimeImage := f.video("同刻.mp4"), f.image("同刻.jpg")
	f.favorite(&models.Video{}, sameTimeVideo.ID, at(18, 9))
	f.favorite(&models.Image{}, sameTimeImage.ID, at(18, 9))
	older := f.image("较早.jpg")
	f.favorite(&models.Image{}, older.ID, at(10, 9))
	middle := f.video("中间.mp4")
	f.favorite(&models.Video{}, middle.ID, at(15, 9))
	oldest := f.image("最早.jpg")
	f.favorite(&models.Image{}, oldest.ID, at(2, 9))
	// 不在本期：上月最后一刻、下月第一刻。
	lastMonth, nextMonth := f.video("上月.mp4"), f.image("下月.jpg")
	f.favorite(&models.Video{}, lastMonth.ID, recapAt(2026, time.September, 30, 23, 59))
	f.favorite(&models.Image{}, nextMonth.ID, recapAt(2026, time.November, 1, 0, 0))
	// 已移除（失效）的视频收藏得最晚：计数，不进样本。进回收站的图片：两边都不算。
	removed := f.video("已移除.mp4")
	f.favorite(&models.Video{}, removed.ID, at(31, 23))
	if err := database.DB.Model(&models.Video{}).Where("id = ?", removed.ID).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	trashed := f.image("回收站.jpg")
	f.favorite(&models.Image{}, trashed.ID, at(30, 23))
	if err := database.DB.Delete(&models.Image{}, trashed.ID).Error; err != nil {
		t.Fatal(err)
	}
	now := recapAt(2026, time.November, 15, 12, 0)

	month := f.recap(now, RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10}).Freshness
	if month.NewFavoriteVideos != 5 || month.NewFavoriteImages != 3 {
		t.Fatalf("新收藏计数应为视频 5（含已移除）图片 3: %+v", month)
	}
	type sample struct {
		kind  string
		id    uint
		title string
	}
	want := []sample{
		{RecapActivityVideo, titled.ID, "显示标题"},
		{RecapActivityVideo, sameTimeVideo.ID, "同刻.mp4"},
		{RecapActivityImage, sameTimeImage.ID, "同刻.jpg"},
		{RecapActivityVideo, middle.ID, "中间.mp4"},
		{RecapActivityImage, older.ID, "较早.jpg"},
		{RecapActivityImage, oldest.ID, "最早.jpg"},
	}
	if len(month.NewFavoriteSamples) != len(want) {
		t.Fatalf("新收藏样本应最多 6 个: %+v", month.NewFavoriteSamples)
	}
	for index, item := range month.NewFavoriteSamples {
		if got := (sample{item.Kind, item.ID, item.Title}); got != want[index] || !item.Available {
			t.Fatalf("第 %d 个新收藏样本: got %+v want %+v（全部 %+v）", index, item, want[index], month.NewFavoriteSamples)
		}
	}
	if !month.NewFavoriteSamples[0].FavoritedAt.Equal(at(20, 21)) || month.NewFavoriteSamples[0].FavoritedAt.Location() != recapTestZone {
		t.Fatalf("收藏时间应按本机时区返回: %v", month.NewFavoriteSamples[0].FavoritedAt)
	}

	// 期初第一刻那一部排在第 7，被上限截掉；只看 10 月 1 日这一天（实时）时它就在样本里。
	live := f.recap(recapAt(2026, time.October, 1, 12, 0), RecapRequest{Period: RecapPeriodLive})
	if live.Freshness.NewFavoriteVideos != 1 || len(live.Freshness.NewFavoriteSamples) != 1 || live.Freshness.NewFavoriteSamples[0].ID != first.ID {
		t.Fatalf("期初第一刻的收藏应算在 10 月 1 日: %+v", live.Freshness)
	}

	// 年度：同样的 8 个计数，样本仍只取最近 6 个。
	year := f.recap(now, RecapRequest{Period: RecapPeriodYear, Year: 2026}).Freshness
	if year.NewFavoriteVideos != 6 || year.NewFavoriteImages != 4 || len(year.NewFavoriteSamples) != 6 {
		t.Fatalf("年度新收藏应含上月与下月那两项、样本仍是 6 个: %+v", year)
	}
	if year.NewFavoriteSamples[0].ID != nextMonth.ID || year.NewFavoriteSamples[0].Kind != RecapActivityImage {
		t.Fatalf("年度新收藏样本最新的应是 11 月 1 日那张图: %+v", year.NewFavoriteSamples)
	}

	// 同一类超过 6 项时，取的是最近的 6 项（不是最早的 6 项再倒过来）。
	f.play(anchor.ID, recapAt(2026, time.December, 1, 20, 0), models.PlayEventSourceDesktopPlay)
	december := make([]models.Image, 8)
	for index := range december {
		december[index] = f.image(fmt.Sprintf("12月%d.jpg", index))
		f.favorite(&models.Image{}, december[index].ID, recapAt(2026, time.December, index+1, 9, 0))
	}
	dec := f.recap(recapAt(2027, time.January, 15, 12, 0), RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 12}).Freshness
	if dec.NewFavoriteImages != 8 || len(dec.NewFavoriteSamples) != 6 {
		t.Fatalf("12 月应有 8 张新收藏、样本 6 张: %+v", dec)
	}
	for index, item := range dec.NewFavoriteSamples {
		if want := december[7-index].ID; item.ID != want || item.Kind != RecapActivityImage {
			t.Fatalf("12 月第 %d 个样本应是 %d: %+v", index, want, dec.NewFavoriteSamples)
		}
	}
}

// 日记评分样本：观看日期在期内且带评分，按日期从近到远、同一天 ID 大的在前，最多 6；
// 关联视频还在库才能直接打开，没有关联或视频已删除 / 已移除时 video_available=false。
func TestRecapDiaryRatedSamplesNewestFirstWithLinkedVideos(t *testing.T) {
	f := newRecapFixture(t)
	// 10 月 1 日（实时用）与 10 月 3 日各有一次播放，让各期都不是空期。
	anchor := f.video("锚点.mp4")
	f.play(anchor.ID, recapAt(2026, time.October, 1, 8, 0), models.PlayEventSourceDesktopPlay)
	f.play(anchor.ID, recapAt(2026, time.October, 3, 20, 0), models.PlayEventSourceDesktopPlay)
	linked, deleted, removed := f.video("关联.mp4"), f.video("已删除.mp4"), f.video("已移除.mp4")
	rating := func(value float64) *float64 { return &value }
	id := func(value uint) *uint { return &value }

	firstDay := f.diary("期初", "2026-10-01", rating(6), nil)
	withVideo := f.diary("有关联", "2026-10-20", rating(9.5), id(linked.ID))
	noVideo := f.diary("无关联", "2026-10-20", rating(7), nil)
	gone := f.diary("视频已删除", "2026-10-18", rating(8), id(deleted.ID))
	stale := f.diary("视频已移除", "2026-10-31", rating(5.5), id(removed.ID))
	mid := f.diary("中间", "2026-10-10", rating(8.5), nil)
	early := f.diary("较早", "2026-10-05", rating(7.5), nil)
	f.diary("未评分", "2026-10-25", nil, id(linked.ID))
	f.diary("上月", "2026-09-30", rating(9), nil)
	f.diary("下月", "2026-11-01", rating(9), nil)
	removedEntry := f.diary("已删日记", "2026-10-30", rating(9), nil)
	if err := database.DB.Delete(&models.ViewingDiaryEntry{}, removedEntry.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&models.Video{}, deleted.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", removed.ID).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	now := recapAt(2026, time.November, 15, 12, 0)

	month := f.recap(now, RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10}).Freshness
	if month.DiaryRated != 7 {
		t.Fatalf("10 月带评分的日记应为 7 条: %+v", month)
	}
	type sample struct {
		id        uint
		title     string
		rating    float64
		watchedOn string
		videoID   uint
		available bool
	}
	want := []sample{
		{stale.ID, "视频已移除", 5.5, "2026-10-31", removed.ID, false},
		{noVideo.ID, "无关联", 7, "2026-10-20", 0, false},
		{withVideo.ID, "有关联", 9.5, "2026-10-20", linked.ID, true},
		{gone.ID, "视频已删除", 8, "2026-10-18", deleted.ID, false},
		{mid.ID, "中间", 8.5, "2026-10-10", 0, false},
		{early.ID, "较早", 7.5, "2026-10-05", 0, false},
	}
	if len(month.DiaryRatedSamples) != len(want) {
		t.Fatalf("日记样本应最多 6 条: %+v", month.DiaryRatedSamples)
	}
	for index, item := range month.DiaryRatedSamples {
		got := sample{item.ID, item.Title, item.Rating, item.WatchedOn, 0, item.VideoAvailable}
		if item.VideoID != nil {
			got.videoID = *item.VideoID
		}
		if got != want[index] {
			t.Fatalf("第 %d 条日记样本: got %+v want %+v", index, got, want[index])
		}
	}

	live := f.recap(recapAt(2026, time.October, 1, 12, 0), RecapRequest{Period: RecapPeriodLive})
	if live.Freshness.DiaryRated != 1 || len(live.Freshness.DiaryRatedSamples) != 1 || live.Freshness.DiaryRatedSamples[0].ID != firstDay.ID {
		t.Fatalf("实时只取今天的日记: %+v", live.Freshness)
	}

	year := f.recap(now, RecapRequest{Period: RecapPeriodYear, Year: 2026}).Freshness
	if year.DiaryRated != 9 || len(year.DiaryRatedSamples) != 6 || year.DiaryRatedSamples[0].Title != "下月" {
		t.Fatalf("年度日记应 9 条、样本 6 条且最新的是 11 月 1 日: %+v", year)
	}
}

// 样本列表在 JSON 里永远是数组：空期、以及有记录但四类都没有样本的期次。
func TestRecapFreshnessSamplesAreNeverNull(t *testing.T) {
	f := newRecapFixture(t)
	now := recapAt(2026, time.November, 15, 12, 0)
	check := func(label string, result *RecapResult) {
		t.Helper()
		payload, err := json.Marshal(result.Freshness)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"new_samples", "rewatched_samples", "new_favorite_samples", "diary_rated_samples"} {
			if !strings.Contains(string(payload), fmt.Sprintf(`"%s":[]`, key)) {
				t.Fatalf("%s: %s 应是空数组: %s", label, key, payload)
			}
		}
	}
	check("空期", f.recap(now, RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10}))
	legacyOnly := f.video("只有老账.mp4")
	f.play(legacyOnly.ID, recapAt(2026, time.October, 2, 20, 0), models.PlayEventSourceLegacy)
	result := f.recap(now, RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	if result.Empty {
		t.Fatal("有 legacy 播放的期次不是空期")
	}
	check("有记录无样本", result)
	check("实时", f.recap(now, RecapRequest{Period: RecapPeriodLive}))
	check("年度", f.recap(now, RecapRequest{Period: RecapPeriodYear, Year: 2025}))
}
