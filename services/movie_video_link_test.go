package services

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"video-master/models"
)

// 本文件验收 P-019 的片库关联与已看同步（D-PC52，问题清单 APP-05、APP-06）。

func (h *movieChartMarkHarness) seedVideo(name, displayTitle string) models.Video {
	h.t.Helper()
	video := models.Video{Name: name, DisplayTitle: displayTitle, Path: "/library/" + name}
	if err := h.db.Create(&video).Error; err != nil {
		h.t.Fatalf("写入视频失败: %v", err)
	}
	return video
}

func (h *movieChartMarkHarness) linkCount() int64 {
	h.t.Helper()
	var count int64
	if err := h.db.Model(&models.MovieVideoLink{}).Count(&count).Error; err != nil {
		h.t.Fatal(err)
	}
	return count
}

// fakeWatchSetter 是 LinkedVideoWatchSetter 的替身；reenter 非空时模拟一个违约的实现：
// 回写时又把状态通知回观察者。
type fakeWatchSetter struct {
	mu      sync.Mutex
	calls   []string
	reenter func(videoID uint, watched bool)
}

func (f *fakeWatchSetter) SetVideoWatchedFromLink(videoID uint, watched bool) error {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%d:%t", videoID, watched))
	reenter := f.reenter
	f.mu.Unlock()
	if reenter != nil {
		reenter(videoID, watched)
	}
	return nil
}

func (f *fakeWatchSetter) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestAPP06LinkAndUnlinkMovieVideo(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	video := h.seedVideo("沙丘.2021.mkv", "")
	gone := h.seedVideo("已删.mkv", "")
	if err := h.db.Delete(&gone).Error; err != nil {
		t.Fatal(err)
	}

	if err := h.service.LinkMovieToVideo("201", video.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.service.LinkMovieToVideo("201", video.ID); err != nil {
		t.Fatalf("重复关联应幂等: %v", err)
	}
	if h.linkCount() != 1 {
		t.Fatalf("重复关联不得多出行: %d", h.linkCount())
	}
	if err := h.service.LinkMovieToVideo("201", gone.ID); !errors.Is(err, ErrMovieLinkVideoNotFound) {
		t.Fatalf("已移除的视频不能关联: %v", err)
	}
	if err := h.service.LinkMovieToVideo("201", 99999); !errors.Is(err, ErrMovieLinkVideoNotFound) {
		t.Fatalf("不存在的视频不能关联: %v", err)
	}
	if err := h.service.LinkMovieToVideo("abc", video.ID); !errors.Is(err, ErrMovieLinkInvalidDoubanID) {
		t.Fatalf("非法豆瓣 ID 应拒绝: %v", err)
	}
	links, err := h.service.ListMovieVideoLinks("201")
	if err != nil || len(links) != 1 || links[0].VideoID != video.ID {
		t.Fatalf("列出关联错误: %+v %v", links, err)
	}
	if err := h.service.UnlinkMovieVideo("201", video.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.service.UnlinkMovieVideo("201", video.ID); err != nil {
		t.Fatalf("重复解除应幂等: %v", err)
	}
	if h.linkCount() != 0 {
		t.Fatalf("解除后应无关联")
	}
}

func TestAPP06SuggestLibraryMatches(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	exact := h.seedVideo("[x]随便.mkv", "沙丘")
	byName := h.seedVideo("沙丘.mp4", "")
	yearPrefix := h.seedVideo("Dune Part One 2021.mkv", "")
	_ = yearPrefix
	prefix := h.seedVideo("沙丘：第二部.mkv", "")
	withYear := h.seedVideo("沙丘 (2021) 1080p.mkv", "")
	byOriginal := h.seedVideo("a.mkv", "")
	if err := h.db.Model(&byOriginal).Update("original_title", "沙丘!").Error; err != nil {
		t.Fatal(err)
	}
	stale := h.seedVideo("沙丘 失效.mkv", "")
	if err := h.db.Model(&stale).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	deleted := h.seedVideo("沙丘 已删.mkv", "")
	if err := h.db.Delete(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	h.seedVideo("无关.mkv", "别的片")

	got, err := h.service.SuggestLibraryMatches("  沙丘 ", 2021)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("最多取前 5 条，实际 %d: %+v", len(got), got)
	}
	// 完全相等（含标点规范化）先于前缀；同分按视频 ID；有年份的前缀排在无年份的前面。
	wantOrder := []uint{exact.ID, byName.ID, byOriginal.ID, withYear.ID, prefix.ID}
	for i, id := range wantOrder {
		if got[i].VideoID != id {
			t.Fatalf("第 %d 名应是视频 %d，实际 %+v", i, id, got)
		}
	}
	for _, s := range got {
		if s.VideoID == stale.ID || s.VideoID == deleted.ID {
			t.Fatalf("失效与已移除的视频不得出现: %+v", s)
		}
	}
	if got[0].DisplayTitle != "沙丘" || got[1].Name != "沙丘.mp4" {
		t.Fatalf("返回字段错误: %+v", got[:2])
	}
	// 年份未知时不加分：withYear 与 prefix 只按 ID 排。
	noYear, err := h.service.SuggestLibraryMatches("沙丘", 0)
	if err != nil {
		t.Fatal(err)
	}
	if noYear[3].VideoID != prefix.ID {
		t.Fatalf("年份未知不应加分: %+v", noYear)
	}
	if empty, err := h.service.SuggestLibraryMatches(" ！？ ", 0); err != nil || len(empty) != 0 {
		t.Fatalf("规范化后为空应返回空: %+v %v", empty, err)
	}
	if h.linkCount() != 0 {
		t.Fatalf("建议是只读的，不得写关联")
	}
}

func TestAPP06WatchedPageCarriesLinkedVideos(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	entry := h.seedEntry("201", "沙丘", "2021-10-22")
	video := h.seedVideo("沙丘.mkv", "")
	if err := h.service.LinkMovieToVideo("201", video.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWatched); err != nil {
		t.Fatal(err)
	}
	groups, err := h.service.ListWatched()
	if err != nil || len(groups) != 1 {
		t.Fatalf("已看页错误: %+v %v", groups, err)
	}
	if got := groups[0].Items[0].LinkedVideoIDs; len(got) != 1 || got[0] != video.ID {
		t.Fatalf("已看卡片应带关联视频: %+v", groups[0].Items[0])
	}
}

// 视频翻转为已看 → 关联的豆瓣 ID 标 watched；取消已看 → 撤销 watched。这条内部写入
// 不触发任何回写。
func TestAPP05VideoWatchedSyncsChartMarkWithoutCallback(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	setter := &fakeWatchSetter{}
	h.service.SetLinkedVideoWatchSetter(setter)
	entry := h.seedEntry("201", "沙丘", "2021-10-22")
	skipEntry := h.seedEntry("202", "别的", "2021-10-22")
	video := h.seedVideo("沙丘.mkv", "")
	for _, id := range []string{entry.DoubanID, skipEntry.DoubanID} {
		if err := h.service.LinkMovieToVideo(id, video.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.service.MarkEntry(skipEntry.DoubanID, models.MovieChartMarkSkip); err != nil {
		t.Fatal(err)
	}

	h.service.OnVideoWatchedChanged(video.ID, true)
	if row, ok := h.markRow("201"); !ok || row.Mark != models.MovieChartMarkWatched || row.Title != "沙丘" {
		t.Fatalf("已看应同步成 watched: %+v", row)
	}
	if row, _ := h.markRow("202"); row.Mark != models.MovieChartMarkWatched {
		t.Fatalf("skip 也应被已看覆盖: %+v", row)
	}
	h.service.OnVideoWatchedChanged(video.ID, false)
	if _, ok := h.markRow("201"); ok {
		t.Fatalf("取消已看应撤销 watched 标记")
	}
	if len(setter.snapshot()) != 0 {
		t.Fatalf("视频侧来源的同步不得回写视频: %v", setter.snapshot())
	}
	// 其他标记不被取消已看误伤。
	if _, err := h.service.MarkEntry("201", models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	h.service.OnVideoWatchedChanged(video.ID, false)
	if row, _ := h.markRow("201"); row.Mark != models.MovieChartMarkWant {
		t.Fatalf("取消已看只撤 watched，不动 want: %+v", row)
	}
	// 想看 → 已看要走撤销副作用：榜单建的片单条目随之删除。
	h.service.OnVideoWatchedChanged(video.ID, true)
	if got := len(h.watchlistEntries()); got != 0 {
		t.Fatalf("want 变 watched 应删掉榜单建的片单条目，剩余 %d", got)
	}
	if len(setter.snapshot()) != 0 {
		t.Fatalf("仍不得回写视频: %v", setter.snapshot())
	}
}

// 还有别的关联视频是已看时，取消其中一个不撤 watched。
func TestAPP05UnwatchKeepsMarkWhileAnotherLinkedVideoWatched(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	h.seedEntry("201", "沙丘", "2021-10-22")
	a := h.seedVideo("沙丘 A.mkv", "")
	b := h.seedVideo("沙丘 B.mkv", "")
	for _, v := range []models.Video{a, b} {
		if err := h.service.LinkMovieToVideo("201", v.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.db.Model(&models.Video{}).Where("id IN ?", []uint{a.ID, b.ID}).Update("is_watched", true).Error; err != nil {
		t.Fatal(err)
	}
	h.service.OnVideoWatchedChanged(a.ID, true)
	if err := h.db.Model(&a).Update("is_watched", false).Error; err != nil {
		t.Fatal(err)
	}
	h.service.OnVideoWatchedChanged(a.ID, false)
	if row, ok := h.markRow("201"); !ok || row.Mark != models.MovieChartMarkWatched {
		t.Fatalf("另一个视频仍是已看，标记应保留: %+v", row)
	}
	if err := h.db.Model(&b).Update("is_watched", false).Error; err != nil {
		t.Fatal(err)
	}
	h.service.OnVideoWatchedChanged(b.ID, false)
	if _, ok := h.markRow("201"); ok {
		t.Fatalf("全部取消后应撤销标记")
	}
}

// 榜单侧置 / 取消 watched 后回写全部关联视频；重复点同一标记与非 watched 变化不回写。
func TestAPP05ChartWatchedWritesBackToLinkedVideos(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	setter := &fakeWatchSetter{}
	h.service.SetLinkedVideoWatchSetter(setter)
	entry := h.seedEntry("201", "沙丘", "2021-10-22")
	a := h.seedVideo("A.mkv", "")
	b := h.seedVideo("B.mkv", "")
	for _, v := range []models.Video{a, b} {
		if err := h.service.LinkMovieToVideo("201", v.ID); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	if len(setter.snapshot()) != 0 {
		t.Fatalf("want 不涉及已看，不应回写: %v", setter.snapshot())
	}
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWatched); err != nil {
		t.Fatal(err)
	}
	want := []string{fmt.Sprintf("%d:true", a.ID), fmt.Sprintf("%d:true", b.ID)}
	if got := setter.snapshot(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("置已看应回写全部关联视频，得到 %v", got)
	}
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWatched); err != nil {
		t.Fatal(err)
	}
	if len(setter.snapshot()) != 2 {
		t.Fatalf("重复点已看不应再回写: %v", setter.snapshot())
	}
	if err := h.service.ClearMark(entry.DoubanID); err != nil {
		t.Fatal(err)
	}
	want = append(want, fmt.Sprintf("%d:false", a.ID), fmt.Sprintf("%d:false", b.ID))
	if got := setter.snapshot(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("取消已看应回写 false，得到 %v", got)
	}
	// watched → skip 同样是离开已看。
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWatched); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkSkip); err != nil {
		t.Fatal(err)
	}
	if got := setter.snapshot(); len(got) != 8 || got[7] != fmt.Sprintf("%d:false", b.ID) {
		t.Fatalf("已看改成别的标记应回写 false，得到 %v", got)
	}
}

// 即使回写方违约、把状态又通知回观察者，也不会死锁，也不会继续往外回写。
func TestAPP05NoLoopWhenSetterReentersObserver(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	setter := &fakeWatchSetter{}
	setter.reenter = func(videoID uint, watched bool) { h.service.OnVideoWatchedChanged(videoID, watched) }
	h.service.SetLinkedVideoWatchSetter(setter)
	entry := h.seedEntry("201", "沙丘", "2021-10-22")
	video := h.seedVideo("沙丘.mkv", "")
	if err := h.service.LinkMovieToVideo("201", video.ID); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWatched); err != nil {
			t.Errorf("标记失败: %v", err)
		}
	}()
	<-done
	if got := setter.snapshot(); len(got) != 1 {
		t.Fatalf("回写应恰好一次，得到 %v", got)
	}
	if row, _ := h.markRow("201"); row.Mark != models.MovieChartMarkWatched {
		t.Fatalf("最终应是 watched: %+v", row)
	}
	// 观察者方向同样只走一遍：视频侧翻转不产生回写。
	before := len(setter.snapshot())
	h.service.OnVideoWatchedChanged(video.ID, true)
	if len(setter.snapshot()) != before {
		t.Fatalf("观察者路径不得回写: %v", setter.snapshot())
	}
}
