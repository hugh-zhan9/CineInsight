package services

import (
	"context"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 回顾测试统一用一个非 UTC 的固定时区：事件、时间窗与「今天」都按它计算，
// 能抓出把 UTC 日期当成本地日期的错误（东八区 0:30 在 UTC 还是前一天）。
var recapTestZone = time.FixedZone("UTC+8", 8*3600)

type recapFixture struct {
	t    *testing.T
	root string
}

func newRecapFixture(t *testing.T) *recapFixture {
	t.Helper()
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	return &recapFixture{t: t, root: root}
}

func recapAt(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, recapTestZone)
}

func (f *recapFixture) video(name string) models.Video {
	f.t.Helper()
	video := models.Video{Name: name, Path: filepath.Join(f.root, name), Directory: f.root, Duration: 600}
	if err := database.DB.Create(&video).Error; err != nil {
		f.t.Fatal(err)
	}
	return video
}

func (f *recapFixture) image(name string) models.Image {
	f.t.Helper()
	image := models.Image{Name: name, Path: filepath.Join(f.root, name), Directory: f.root, Format: "jpg"}
	if err := database.DB.Create(&image).Error; err != nil {
		f.t.Fatal(err)
	}
	return image
}

func (f *recapFixture) play(videoID uint, at time.Time, source string) {
	f.t.Helper()
	if err := database.DB.Create(&models.PlayEvent{VideoID: videoID, PlayedAt: at, Source: source}).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f *recapFixture) view(imageID uint, at time.Time, source string) {
	f.t.Helper()
	if err := database.DB.Create(&models.ImageViewEvent{ImageID: imageID, ViewedAt: at, Source: source}).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f *recapFixture) tag(name string, automaticKind string) models.Tag {
	f.t.Helper()
	tag := models.Tag{Name: name, Color: "#336699", AutomaticKind: automaticKind}
	if err := database.DB.Create(&tag).Error; err != nil {
		f.t.Fatal(err)
	}
	return tag
}

func (f *recapFixture) tagVideo(videoID uint, tags ...models.Tag) {
	f.t.Helper()
	for _, tag := range tags {
		if err := database.DB.Exec("INSERT INTO video_tags (video_id, tag_id) VALUES (?, ?)", videoID, tag.ID).Error; err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *recapFixture) recap(now time.Time, request RecapRequest) *RecapResult {
	f.t.Helper()
	service := &RecapService{now: func() time.Time { return now }, loc: recapTestZone}
	result, err := service.GetRecap(context.Background(), request)
	if err != nil {
		f.t.Fatalf("GetRecap(%+v) 失败: %v", request, err)
	}
	return result
}

// 自然月按本机时区取 [1 日 0 点, 下月 1 日 0 点)：月末最后一刻与月初第一刻各归各月；
// 东八区 10 月 1 日 0:30 在 UTC 是 9 月 30 日，仍算 10 月。年度同理覆盖跨年。
func TestRecapMonthAndYearWindowsUseLocalCalendarBoundaries(t *testing.T) {
	f := newRecapFixture(t)
	video := f.video("边界.mp4")
	lastSecondOfSeptember := recapAt(2026, time.September, 30, 23, 59).Add(59*time.Second + 999*time.Millisecond)
	f.play(video.ID, lastSecondOfSeptember, models.PlayEventSourceDesktopPlay)
	f.play(video.ID, recapAt(2026, time.October, 1, 0, 0), models.PlayEventSourceDesktopPlay)
	f.play(video.ID, recapAt(2026, time.October, 1, 0, 30), models.PlayEventSourceDesktopPlay)
	f.play(video.ID, recapAt(2026, time.October, 31, 23, 59), models.PlayEventSourceDesktopPlay)
	f.play(video.ID, recapAt(2026, time.November, 1, 0, 0), models.PlayEventSourceDesktopPlay)
	f.play(video.ID, recapAt(2025, time.December, 31, 23, 59), models.PlayEventSourceDesktopPlay)
	now := recapAt(2026, time.November, 15, 12, 0)

	october := f.recap(now, RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	if october.Overview.Plays != 3 || october.InProgress || october.Label != "2026年10月" {
		t.Fatalf("10 月应恰好 3 次且已结束: plays=%d inProgress=%v label=%s", october.Overview.Plays, october.InProgress, october.Label)
	}
	if len(october.Habits.Days) != 31 || october.Habits.Days[0].Date != "2026-10-01" || october.Habits.Days[0].Plays != 2 {
		t.Fatalf("10 月应有 31 个日期桶，1 日 2 次: %+v", october.Habits.Days[:1])
	}
	if october.Habits.Hours[0].Plays != 2 || october.Habits.Hours[23].Plays != 1 {
		t.Fatalf("小时分布应按东八区: %+v", october.Habits.Hours)
	}
	september := f.recap(now, RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 9})
	if september.Overview.Plays != 1 || september.Habits.Days[29].Plays != 1 {
		t.Fatalf("9 月最后一刻应归 9 月: %d", september.Overview.Plays)
	}
	year := f.recap(now, RecapRequest{Period: RecapPeriodYear, Year: 2026})
	if year.Overview.Plays != 5 || !year.InProgress || len(year.Months) != 12 || year.Months[9].Plays != 3 || year.Months[10].Plays != 1 {
		t.Fatalf("2026 年应 5 次（不含 2025 年最后一分钟）: plays=%d months=%+v", year.Overview.Plays, year.Months)
	}
	if len(year.Habits.Days) != 365 {
		t.Fatalf("2026 年应有 365 个日期桶，实际 %d", len(year.Habits.Days))
	}
	leap := f.recap(now, RecapRequest{Period: RecapPeriodYear, Year: 2024})
	if !leap.Empty || len(leap.Habits.Days) != 366 || len(leap.Titles) != 0 || leap.Favorite != nil {
		t.Fatalf("2024 年是空期（闰年 366 天）且不出称号: empty=%v days=%d titles=%+v", leap.Empty, len(leap.Habits.Days), leap.Titles)
	}
	for _, bad := range []RecapRequest{{Period: "week"}, {Period: RecapPeriodMonth, Year: 2026, Month: 13}, {Period: RecapPeriodYear, Year: 0}} {
		if _, err := (&RecapService{loc: recapTestZone}).GetRecap(context.Background(), bad); err == nil {
			t.Fatalf("非法期次应报错: %+v", bad)
		}
	}
}

// 空期：没有任何事件时标 Empty、不出称号、没有最爱；上一期为 0 时不给百分比。
func TestRecapEmptyPeriodHasNoTitlesAndNoPercent(t *testing.T) {
	f := newRecapFixture(t)
	f.video("没看.mp4")
	result := f.recap(recapAt(2026, time.October, 11, 12, 0), RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	if !result.Empty || result.Favorite != nil || result.FavoriteImage != nil || len(result.Titles) != 0 || len(result.TopVideos) != 0 {
		t.Fatalf("空期不应有最爱 / 称号 / 排行: %+v", result)
	}
	if result.Overview.Previous.PlaysChange != nil || result.Overview.Previous.ImageViewsChange != nil {
		t.Fatalf("上一期为 0 时不应给百分比: %+v", result.Overview.Previous)
	}
	if result.Habits.PeakHour != -1 || result.Habits.PeakSlot != "" || result.Habits.BusiestDay != nil {
		t.Fatalf("空期没有高峰: %+v", result.Habits)
	}
}

// 本期最爱：次数最多；并列取最近一次播放更晚的；再并列取 ID 小的。排行与最爱同一口径。
func TestRecapFavoriteAndRankingTieBreaks(t *testing.T) {
	f := newRecapFixture(t)
	early, late, sameAsLate, once := f.video("早.mp4"), f.video("晚.mp4"), f.video("同刻.mp4"), f.video("一次.mp4")
	for _, v := range []models.Video{early, late, sameAsLate} {
		f.play(v.ID, recapAt(2026, time.October, 2, 20, 0), models.PlayEventSourceDesktopPlay)
	}
	f.play(early.ID, recapAt(2026, time.October, 3, 20, 0), PlayEventSourceInlineView)
	f.play(late.ID, recapAt(2026, time.October, 5, 20, 0), models.PlayEventSourceDesktopPlay)
	f.play(sameAsLate.ID, recapAt(2026, time.October, 5, 20, 0), models.PlayEventSourceMobileFeed)
	f.play(once.ID, recapAt(2026, time.October, 6, 20, 0), models.PlayEventSourceDesktopPlay)
	result := f.recap(recapAt(2026, time.October, 11, 12, 0), RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	if result.Favorite == nil || result.Favorite.Video.ID != late.ID || result.Favorite.Count != 2 || result.Favorite.Video.Title != "晚.mp4" {
		t.Fatalf("并列 2 次时应取最近播放更晚、ID 更小的「晚」: %+v", result.Favorite)
	}
	got := []uint{}
	for _, rank := range result.TopVideos {
		got = append(got, rank.Video.ID)
	}
	want := []uint{late.ID, sameAsLate.ID, early.ID, once.ID}
	if len(got) != len(want) {
		t.Fatalf("排行长度不对: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("排行顺序应为 %v，实际 %v", want, got)
		}
	}
	if result.Overview.Videos != 4 || result.Overview.Plays != 7 || !result.Favorite.Video.Available {
		t.Fatalf("总览不对: %+v", result.Overview)
	}
}

// 标签排行按播放加权、排除带 automatic_kind 的自动标签；automatic_kind 为 NULL 的老标签照常计入；
// 并列按标签 ID。软删除的标签不计。
func TestRecapTagRankingExcludesAutomaticTags(t *testing.T) {
	f := newRecapFixture(t)
	a, b := f.video("a.mp4"), f.video("b.mp4")
	scifi, drama, short, legacyNull, removed := f.tag("科幻", ""), f.tag("剧情", ""), f.tag("短视频", "short_video"), f.tag("老标签", ""), f.tag("已删", "")
	if err := database.DB.Exec("UPDATE tags SET automatic_kind = NULL WHERE id = ?", legacyNull.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&removed).Error; err != nil {
		t.Fatal(err)
	}
	f.tagVideo(a.ID, scifi, short, legacyNull, removed)
	f.tagVideo(b.ID, drama, short)
	for day := 1; day <= 3; day++ {
		f.play(a.ID, recapAt(2026, time.October, day, 21, 0), models.PlayEventSourceDesktopPlay)
	}
	f.play(b.ID, recapAt(2026, time.October, 4, 21, 0), models.PlayEventSourceDesktopPlay)
	result := f.recap(recapAt(2026, time.October, 11, 12, 0), RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	names := []string{}
	for _, rank := range result.TopTags {
		names = append(names, rank.Tag.Name)
		if rank.Tag.ID == short.ID || rank.Tag.ID == removed.ID {
			t.Fatalf("自动标签与已删标签不应进排行: %+v", result.TopTags)
		}
	}
	if len(names) != 3 || names[0] != "科幻" || names[1] != "老标签" || names[2] != "剧情" || result.TopTags[0].Weight != 3 {
		t.Fatalf("标签排行应为 科幻(3) 老标签(3，ID 大排后) 剧情(1): %v %+v", names, result.TopTags)
	}
	if result.Favorite == nil || len(result.Favorite.Tags) != 2 {
		t.Fatalf("最爱只附非自动、未删除的标签: %+v", result.Favorite)
	}
}

// 「相关视频还有」：与最爱共享非自动标签 / 人物 / 作品集最多、本期没看过、片库可见的视频；
// 排除最爱自己、本期看过的、只共享自动标签的、软删与失效的；按共享数、评分（未评分排后）、最近入库、ID 排序。
func TestRecapRelatedVideosRankingAndExclusions(t *testing.T) {
	f := newRecapFixture(t)
	favorite := f.video("最爱.mp4")
	sharesTwo, ratedHigh, unrated, autoOnly, watched, deleted, stale := f.video("共享二.mp4"), f.video("高分.mp4"),
		f.video("未评分.mp4"), f.video("只共享自动.mp4"), f.video("本期看过.mp4"), f.video("已删.mp4"), f.video("失效.mp4")
	manual, auto := f.tag("悬疑", ""), f.tag("低分辨率", "low_resolution")
	f.tagVideo(favorite.ID, manual, auto)
	f.tagVideo(sharesTwo.ID, manual)
	f.tagVideo(ratedHigh.ID, manual)
	f.tagVideo(unrated.ID, manual)
	f.tagVideo(autoOnly.ID, auto)
	f.tagVideo(watched.ID, manual)
	f.tagVideo(deleted.ID, manual)
	f.tagVideo(stale.ID, manual)
	person := models.Person{DisplayName: "演员甲"}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{favorite.ID, sharesTwo.ID} {
		if err := database.DB.Create(&models.VideoPerson{VideoID: id, PersonID: person.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	rating := 9.0
	updates := []struct {
		id     uint
		column string
		value  any
	}{{ratedHigh.ID, "personal_rating", &rating}, {stale.ID, "is_stale", true}, {unrated.ID, "created_at", recapAt(2026, time.September, 1, 0, 0)}}
	for _, u := range updates {
		if err := database.DB.Model(&models.Video{}).Where("id = ?", u.id).Update(u.column, u.value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Delete(&models.Video{}, deleted.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.play(favorite.ID, recapAt(2026, time.October, 2, 20, 0), models.PlayEventSourceDesktopPlay)
	f.play(favorite.ID, recapAt(2026, time.October, 3, 20, 0), models.PlayEventSourceDesktopPlay)
	f.play(watched.ID, recapAt(2026, time.October, 4, 20, 0), models.PlayEventSourceDesktopPlay)
	f.play(sharesTwo.ID, recapAt(2026, time.September, 4, 20, 0), models.PlayEventSourceDesktopPlay) // 上期看过不影响
	result := f.recap(recapAt(2026, time.October, 11, 12, 0), RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	if result.Favorite == nil || result.Favorite.Video.ID != favorite.ID || len(result.Favorite.People) != 1 {
		t.Fatalf("最爱不对: %+v", result.Favorite)
	}
	got := []uint{}
	for _, item := range result.Favorite.Related {
		got = append(got, item.Video.ID)
	}
	want := []uint{sharesTwo.ID, ratedHigh.ID, unrated.ID}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("相关视频应为 %v，实际 %v", want, got)
	}
	if result.Favorite.Related[0].Shared != 2 || result.Favorite.Related[1].Shared != 1 {
		t.Fatalf("共享数不对: %+v", result.Favorite.Related)
	}
}

// 新发现 / 重温：本期开始前有任意事件（含 legacy）为重温；首次出现即在本期为新发现；
// 期内最早一条是 legacy 的不算新发现；只有 legacy 的不参与判定。legacy 照样计入总数并单独标注。
func TestRecapFreshnessClassifiesNewRewatchAndLegacy(t *testing.T) {
	f := newRecapFixture(t)
	rewatch, fresh, legacyBefore, legacyFirst, legacyOnly := f.video("重温.mp4"), f.video("新片.mp4"), f.video("老账.mp4"), f.video("期内老账.mp4"), f.video("只有老账.mp4")
	before := recapAt(2026, time.September, 20, 20, 0)
	f.play(rewatch.ID, before, models.PlayEventSourceDesktopPlay)
	f.play(legacyBefore.ID, before, models.PlayEventSourceLegacy)
	f.play(legacyFirst.ID, recapAt(2026, time.October, 1, 8, 0), models.PlayEventSourceLegacy)
	f.play(legacyOnly.ID, recapAt(2026, time.October, 1, 9, 0), models.PlayEventSourceLegacy)
	for _, v := range []models.Video{rewatch, fresh, legacyBefore, legacyFirst} {
		f.play(v.ID, recapAt(2026, time.October, 5, 20, 0), models.PlayEventSourceDesktopPlay)
	}
	favoritedAt := recapAt(2026, time.October, 6, 10, 0)
	if err := database.DB.Model(&models.Video{}).Where("id = ?", fresh.ID).Updates(map[string]any{"is_favorite": true, "favorited_at": favoritedAt}).Error; err != nil {
		t.Fatal(err)
	}
	rating := 8.5
	for _, entry := range []models.ViewingDiaryEntry{{Title: "有评分", WatchedOn: "2026-10-05", Rating: &rating, Revision: 1}, {Title: "无评分", WatchedOn: "2026-10-05", Revision: 1}, {Title: "上月", WatchedOn: "2026-09-30", Rating: &rating, Revision: 1}} {
		if err := database.DB.Create(&entry).Error; err != nil {
			t.Fatal(err)
		}
	}
	result := f.recap(recapAt(2026, time.October, 11, 12, 0), RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	fr := result.Freshness
	if fr.NewVideos != 1 || fr.RewatchedVideos != 2 || len(fr.NewSamples) != 1 || fr.NewSamples[0].ID != fresh.ID {
		t.Fatalf("应新发现 1（新片）、重温 2（重温、老账）: %+v", fr)
	}
	if fr.NewFavoriteVideos != 1 || fr.DiaryRated != 1 {
		t.Fatalf("新收藏 / 日记评分不对: %+v", fr)
	}
	if result.Overview.Plays != 6 || result.Overview.LegacyPlays != 2 || result.Overview.Videos != 5 {
		t.Fatalf("legacy 应计入总数并单独标注: %+v", result.Overview)
	}
}

// 与上一同期对比：进行中的本月只与上月同样长的一段比；上一期为 0 时不给百分比。
func TestRecapPreviousComparisonUsesSameElapsedSpan(t *testing.T) {
	f := newRecapFixture(t)
	video := f.video("比较.mp4")
	img := f.image("比较.jpg")
	f.play(video.ID, recapAt(2026, time.September, 5, 20, 0), models.PlayEventSourceDesktopPlay)
	f.play(video.ID, recapAt(2026, time.September, 11, 11, 59), models.PlayEventSourceDesktopPlay)
	f.play(video.ID, recapAt(2026, time.September, 11, 12, 0), models.PlayEventSourceDesktopPlay) // 恰在截止时刻，不算
	f.play(video.ID, recapAt(2026, time.September, 20, 20, 0), models.PlayEventSourceDesktopPlay)
	for day := 1; day <= 3; day++ {
		f.play(video.ID, recapAt(2026, time.October, day, 20, 0), models.PlayEventSourceDesktopPlay)
	}
	f.view(img.ID, recapAt(2026, time.October, 2, 20, 0), models.ImageViewSourceDesktopViewer)
	result := f.recap(recapAt(2026, time.October, 11, 12, 0), RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	prev := result.Overview.Previous
	if !result.InProgress || prev.Label != "上月同期" || prev.Plays != 2 || !prev.End.Equal(recapAt(2026, time.September, 11, 12, 0)) {
		t.Fatalf("上月同期应为 9/1–9/11 12:00，2 次: %+v", prev)
	}
	if prev.PlaysChange == nil || *prev.PlaysChange != 50 || prev.ImageViewsChange != nil {
		t.Fatalf("播放应 +50%%，图片上期为 0 不给百分比: %+v", prev)
	}
	finished := f.recap(recapAt(2026, time.November, 2, 12, 0), RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	if finished.InProgress || finished.Overview.Previous.Label != "上月" || finished.Overview.Previous.Plays != 4 {
		t.Fatalf("已结束的月份与整个上月比: %+v", finished.Overview.Previous)
	}
}

// 称号阈值含边界：夜猫子 30%、周末 60%、重温 / 探索 50%、连续 7 天、标签专一 40%、图片控 看图≥播放；
// 占比类要求至少 3 条；一个都不满足时给只陈述条数的「记录者」；最多三个。
func TestRecapTitleThresholdBoundaries(t *testing.T) {
	keys := func(facts recapTitleFacts) map[string]bool {
		set := map[string]bool{}
		for _, title := range recapTitles(facts) {
			set[title.Key] = true
		}
		return set
	}
	cases := []struct {
		name  string
		facts recapTitleFacts
		key   string
		want  bool
	}{
		{"夜猫子 30% 边界", recapTitleFacts{plays: 10, lateNight: 3}, "night_owl", true},
		{"夜猫子 29%", recapTitleFacts{plays: 100, lateNight: 29}, "night_owl", false},
		{"夜猫子样本不足", recapTitleFacts{plays: 2, lateNight: 2}, "night_owl", false},
		{"周末 60% 边界", recapTitleFacts{plays: 10, weekend: 6}, "weekend_fan", true},
		{"周末 59%", recapTitleFacts{plays: 100, weekend: 59}, "weekend_fan", false},
		{"重温 50% 边界", recapTitleFacts{plays: 4, rewatched: 2, newVideos: 2}, "rewatcher", true},
		{"探索 50% 边界", recapTitleFacts{plays: 4, rewatched: 2, newVideos: 2}, "explorer", true},
		{"重温 49%", recapTitleFacts{plays: 100, rewatched: 49, newVideos: 51}, "rewatcher", false},
		{"连续 7 天", recapTitleFacts{plays: 1, longestStreak: 7}, "streak", true},
		{"连续 6 天", recapTitleFacts{plays: 1, longestStreak: 6}, "streak", false},
		{"标签专一 40% 边界", recapTitleFacts{plays: 10, topTagName: "科幻", topTagWeight: 4}, "tag_loyal", true},
		{"标签专一 39%", recapTitleFacts{plays: 100, topTagName: "科幻", topTagWeight: 39}, "tag_loyal", false},
		{"图片控 相等", recapTitleFacts{plays: 5, views: 5}, "image_lover", true},
		{"图片控 少一次", recapTitleFacts{plays: 5, views: 4}, "image_lover", false},
		{"都不满足给记录者", recapTitleFacts{plays: 2}, "recorder", true},
	}
	for _, c := range cases {
		if got := keys(c.facts)[c.key]; got != c.want {
			t.Fatalf("%s: %s=%v，期望 %v（%+v）", c.name, c.key, got, c.want, recapTitles(c.facts))
		}
	}
	all := recapTitles(recapTitleFacts{plays: 10, views: 30, lateNight: 40, weekend: 40, longestStreak: 30, topTagName: "x", topTagWeight: 10})
	if len(all) != 3 {
		t.Fatalf("最多三个称号: %+v", all)
	}
	if len(recapTitles(recapTitleFacts{})) != 0 {
		t.Fatal("空期不出称号")
	}
	if detail := recapTitles(recapTitleFacts{plays: 10, lateNight: 3})[0].Detail; detail != "30% 的记录发生在 0–5 点（3/10）" {
		t.Fatalf("称号要带依据数字: %q", detail)
	}
}

// 实时：今天 0 点至今；近 7 天逐日；今日流水按时间倒序合并视频与图片；正在播放取 2 小时内最后一条播放，
// 待播队列当前项正在播放时优先取它；昨天的事件只进周柱状。
func TestRecapLiveWindowFeedWeekAndNowPlaying(t *testing.T) {
	f := newRecapFixture(t)
	today, yesterday, queued := f.video("今天.mp4"), f.video("昨天.mp4"), f.video("队列.mp4")
	img := f.image("今天.jpg")
	now := recapAt(2026, time.October, 11, 10, 0)
	f.play(yesterday.ID, recapAt(2026, time.October, 10, 23, 30), models.PlayEventSourceDesktopPlay)
	f.play(yesterday.ID, recapAt(2026, time.October, 4, 23, 30), models.PlayEventSourceDesktopPlay) // 窗口外
	f.play(today.ID, recapAt(2026, time.October, 11, 9, 0), PlayEventSourceInlineView)
	f.view(img.ID, recapAt(2026, time.October, 11, 9, 30), models.ImageViewSourceDesktopViewer)
	result := f.recap(now, RecapRequest{Period: RecapPeriodLive})
	if result.Label != "今天" || !result.InProgress || result.Overview.Plays != 1 || result.Overview.ImageViews != 1 {
		t.Fatalf("实时只算今天: %+v", result.Overview)
	}
	if len(result.Week) != 7 || result.Week[0].Date != "2026-10-05" || result.Week[5].Plays != 1 || result.Week[6].Plays != 1 || result.Week[6].Views != 1 {
		t.Fatalf("近 7 天柱状不对: %+v", result.Week)
	}
	if len(result.Feed) != 2 || result.Feed[0].Kind != RecapActivityImage || result.Feed[0].Title != "今天.jpg" || result.Feed[1].ID != today.ID || !result.Feed[1].Available {
		t.Fatalf("今日流水应倒序合并: %+v", result.Feed)
	}
	if result.NowPlaying == nil || result.NowPlaying.Basis != "recent_play" || result.NowPlaying.Video.ID != today.ID {
		t.Fatalf("2 小时内最后一条播放应作为提示: %+v", result.NowPlaying)
	}
	if prev := result.Overview.Previous; prev.Label != "昨天同一时段" || prev.Plays != 0 || prev.PlaysChange != nil {
		t.Fatalf("昨天 0–10 点没有播放: %+v", prev)
	}
	late := f.recap(recapAt(2026, time.October, 11, 11, 30), RecapRequest{Period: RecapPeriodLive})
	if late.NowPlaying != nil {
		t.Fatalf("超过 2 小时不再提示: %+v", late.NowPlaying)
	}
	entry := models.PlaybackQueueEntry{VideoID: queued.ID, Title: "队列.mp4", Position: 1}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	state := models.PlaybackQueueState{ID: 1, Revision: 1, Player: "inline", CurrentEntryID: &entry.ID, Status: "playing"}
	if err := database.DB.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	queue := f.recap(recapAt(2026, time.October, 11, 11, 30), RecapRequest{Period: RecapPeriodLive})
	if queue.NowPlaying == nil || queue.NowPlaying.Basis != "queue" || queue.NowPlaying.Video.ID != queued.ID {
		t.Fatalf("队列正在播放时应取队列当前项: %+v", queue.NowPlaying)
	}
}

// 习惯：周末与深夜（0–5 点）占比、最常看的时段、最忙的一天（并列取更早）、来源占比。
func TestRecapHabitsSharesPeaksAndSources(t *testing.T) {
	f := newRecapFixture(t)
	video, img := f.video("习惯.mp4"), f.image("习惯.jpg")
	f.play(video.ID, recapAt(2026, time.October, 3, 1, 0), models.PlayEventSourceDesktopPlay)   // 周六 深夜
	f.play(video.ID, recapAt(2026, time.October, 3, 2, 0), models.PlayEventSourceDesktopRandom) // 周六 深夜
	f.play(video.ID, recapAt(2026, time.October, 6, 21, 0), models.PlayEventSourceMobileFeed)   // 周二 晚上
	f.view(img.ID, recapAt(2026, time.October, 6, 22, 0), models.ImageViewSourceMobileFeed)     // 周二 晚上
	result := f.recap(recapAt(2026, time.October, 11, 12, 0), RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	h := result.Habits
	if h.Total != 4 || h.WeekendCount != 2 || h.WeekendShare != 0.5 || h.LateNightCount != 2 || h.LateNightShare != 0.5 {
		t.Fatalf("占比不对: %+v", h)
	}
	if h.PeakHour != 1 || h.PeakSlot != "late_night" || h.BusiestDay == nil || h.BusiestDay.Date != "2026-10-03" {
		t.Fatalf("高峰不对（并列取更早）: hour=%d slot=%s day=%+v", h.PeakHour, h.PeakSlot, h.BusiestDay)
	}
	if len(h.Sources) != 4 || h.Sources[0].Count != 1 || h.Sources[0].Kind != RecapActivityVideo {
		t.Fatalf("来源占比不对: %+v", h.Sources)
	}
	if result.Overview.ActiveDays != 2 || result.Overview.LongestStreak != 1 {
		t.Fatalf("活跃天数 / 连续不对: %+v", result.Overview)
	}
	if result.FavoriteImage == nil || result.FavoriteImage.Image.ID != img.ID || len(result.TopImages) != 1 {
		t.Fatalf("图片最爱不对: %+v", result.FavoriteImage)
	}
}

// 软删除的视频：事件保留并继续计入回顾，只标 Available=false；永久删除时事件级联消失。
func TestRecapCountsSoftDeletedVideosAndDropsHardDeleted(t *testing.T) {
	f := newRecapFixture(t)
	video := f.video("回收站.mp4")
	f.play(video.ID, recapAt(2026, time.October, 2, 20, 0), models.PlayEventSourceDesktopPlay)
	if err := database.DB.Delete(&models.Video{}, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	now := recapAt(2026, time.October, 11, 12, 0)
	soft := f.recap(now, RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	if soft.Overview.Plays != 1 || soft.Favorite == nil || soft.Favorite.Video.Available || soft.Favorite.Video.Title != "回收站.mp4" {
		t.Fatalf("软删视频应照样计入且标不可用: %+v", soft.Favorite)
	}
	if err := database.DB.Unscoped().Delete(&models.Video{}, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	hard := f.recap(now, RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	if !hard.Empty || hard.Overview.Plays != 0 {
		t.Fatalf("永久删除后事件应级联消失: %+v", hard.Overview)
	}
}

// 本期最爱图片附它的标签、人物与相关图片：共享多者在前，排除自己、本期看过的与失效的。
func TestRecapFavoriteImageRelatedImages(t *testing.T) {
	f := newRecapFixture(t)
	top, sharesTwo, sharesOne, viewed, stale := f.image("最爱.jpg"), f.image("二.jpg"), f.image("一.jpg"), f.image("看过.jpg"), f.image("失效.jpg")
	tag := f.tag("海边", "")
	person := models.Person{DisplayName: "乙"}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	for _, img := range []models.Image{top, sharesTwo, sharesOne, viewed, stale} {
		if err := database.DB.Exec("INSERT INTO image_tags (image_id, tag_id) VALUES (?, ?)", img.ID, tag.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, img := range []models.Image{top, sharesTwo} {
		if err := database.DB.Create(&models.ImagePerson{ImageID: img.ID, PersonID: person.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Model(&models.Image{}).Where("id = ?", stale.ID).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	f.view(top.ID, recapAt(2026, time.October, 2, 20, 0), models.ImageViewSourceDesktopViewer)
	f.view(top.ID, recapAt(2026, time.October, 3, 20, 0), models.ImageViewSourceMobileFeed)
	f.view(viewed.ID, recapAt(2026, time.October, 3, 21, 0), models.ImageViewSourceDesktopViewer)
	result := f.recap(recapAt(2026, time.October, 11, 12, 0), RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	fav := result.FavoriteImage
	if fav == nil || fav.Image.ID != top.ID || fav.Count != 2 || len(fav.Tags) != 1 || len(fav.People) != 1 {
		t.Fatalf("图片最爱不对: %+v", fav)
	}
	if len(fav.Related) != 2 || fav.Related[0].Image.ID != sharesTwo.ID || fav.Related[0].Shared != 2 || fav.Related[1].Image.ID != sharesOne.ID {
		t.Fatalf("相关图片不对: %+v", fav.Related)
	}
	if !result.Empty && len(result.Titles) == 0 {
		t.Fatal("非空期至少有一个称号")
	}
}

// 播放与内嵌观看在事务提交后通知 recap-activity；写入失败不通知。
func TestRecapActivityNotifiedAfterPlaybackCommit(t *testing.T) {
	f := newRecapFixture(t)
	video := f.video("通知.mp4")
	var got []RecapActivity
	SetRecapActivityListener(func(activity RecapActivity) { got = append(got, activity) })
	t.Cleanup(func() { SetRecapActivityListener(nil) })
	at := time.Now()
	if err := recordFormalPlaybackStats(&video, map[string]interface{}{"play_count": 1}, at, models.PlayEventSourceDesktopPlay); err != nil {
		t.Fatal(err)
	}
	if err := recordDeferredRandomPlaybackStats(video.ID, at); err != nil {
		t.Fatal(err)
	}
	if err := recordDeferredRandomPlaybackStats(987654, at); err == nil {
		t.Fatal("不存在的视频应失败")
	}
	if recorded, err := (&VideoService{}).RecordViewEvent(video.ID, PlayEventSourceInlineView, "recap-session"); err != nil || !recorded {
		t.Fatalf("内嵌观看应记录: %v %v", recorded, err)
	}
	if len(got) != 3 || got[0].Source != models.PlayEventSourceDesktopPlay || got[1].Source != models.PlayEventSourceDesktopRandom || got[2].Source != PlayEventSourceInlineView {
		t.Fatalf("应按提交顺序通知三次: %+v", got)
	}
}
