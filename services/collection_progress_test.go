package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

// D-P2-02「追到哪了」：版本组合并、续播、下一部的四种情形、全部看完、单部作品集不进候选、排序与 Limit。

type progressFixture struct {
	t          *testing.T
	service    *CollectionService
	versions   *VersionGroupService
	base       time.Time
	videoCount int
}

func newProgressFixture(t *testing.T) *progressFixture {
	t.Helper()
	setupVideoServiceTestDB(t)
	return &progressFixture{t: t, service: NewCollectionService(t.TempDir()), versions: NewVersionGroupService(),
		base: time.Date(2026, 5, 1, 20, 0, 0, 0, time.Local)}
}

// video 建一部片；mutate 可设已看 / 进度 / 时间。
func (f *progressFixture) video(title string, mutate func(*models.Video)) models.Video {
	f.t.Helper()
	f.videoCount++
	video := models.Video{Name: fmt.Sprintf("v%02d.mp4", f.videoCount), DisplayTitle: title,
		Path: fmt.Sprintf("/library/progress/v%02d.mp4", f.videoCount), Directory: "/library/progress", Duration: 3600, Size: 100}
	if mutate != nil {
		mutate(&video)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		f.t.Fatal(err)
	}
	return video
}

func (f *progressFixture) at(minutes int) *time.Time {
	value := f.base.Add(time.Duration(minutes) * time.Minute)
	return &value
}

func (f *progressFixture) watched(minutes int) func(*models.Video) {
	return func(v *models.Video) { v.IsWatched, v.WatchedAt, v.LastPlayedAt = true, f.at(minutes), f.at(minutes) }
}

func (f *progressFixture) inProgress(minutes int, position float64) func(*models.Video) {
	return func(v *models.Video) { v.WatchPositionSeconds, v.WatchProgressUpdatedAt = position, f.at(minutes) }
}

func (f *progressFixture) collection(name string, videos ...models.Video) uint {
	f.t.Helper()
	collection, err := f.service.CreateCollection(name, "")
	if err != nil {
		f.t.Fatal(err)
	}
	ids := make([]uint, 0, len(videos))
	for _, video := range videos {
		ids = append(ids, video.ID)
	}
	if len(ids) > 0 {
		if err := f.service.AddCollectionVideos(collection.ID, ids); err != nil {
			f.t.Fatal(err)
		}
	}
	return collection.ID
}

func (f *progressFixture) group(videos ...models.Video) {
	f.t.Helper()
	ids := make([]uint, 0, len(videos))
	for _, video := range videos {
		ids = append(ids, video.ID)
	}
	if _, err := f.versions.Create(context.Background(), ids, videos[0].DisplayTitle); err != nil {
		f.t.Fatal(err)
	}
}

func (f *progressFixture) progress(id uint) CollectionProgress {
	f.t.Helper()
	result, err := f.service.ListCollectionProgress(CollectionProgressRequest{CollectionIDs: []uint{id}})
	if err != nil || len(result) != 1 {
		f.t.Fatalf("进度查询失败: %+v err=%v", result, err)
	}
	return result[0]
}

func requireNext(t *testing.T, label string, progress CollectionProgress, videoID uint, index int, resume bool) {
	t.Helper()
	if progress.Next == nil {
		t.Fatalf("%s：应有下一部: %+v", label, progress)
	}
	if progress.Next.VideoID != videoID || progress.Next.WorkIndex != index || progress.Next.Resume != resume {
		t.Fatalf("%s：下一部不对 got=%+v want video=%d index=%d resume=%v", label, *progress.Next, videoID, index, resume)
	}
}

func TestCollectionProgressVersionGroupCountsAsOneWork(t *testing.T) {
	f := newProgressFixture(t)
	first := f.video("第一部", f.watched(10))
	secondMain := f.video("第二部", nil)
	secondAlt := f.video("第二部 导演剪辑版", f.watched(20)) // 组内任一成员看过即算看过
	third := f.video("第三部", nil)
	f.group(secondMain, secondAlt)
	id := f.collection("三部曲", first, secondMain, third, secondAlt) // 组成员分散在作品集里，位置取组内最小 position
	progress := f.progress(id)
	if progress.TotalWorks != 3 || progress.WatchedWorks != 2 || progress.InProgressWorks != 0 || progress.Complete {
		t.Fatalf("版本组应合并成一部: %+v", progress)
	}
	requireNext(t, "看过第二部之后", progress, third.ID, 3, false)
	if progress.LastActivityAt == nil || !progress.LastActivityAt.Equal(*f.at(20)) {
		t.Fatalf("活动时间应取成员最大值: %v", progress.LastActivityAt)
	}

	// 下一部若是版本组，播的是组代表（版本顺序第一位）。
	f2 := f.video("前传", f.watched(30))
	repMain := f.video("正传", nil)
	repAlt := f.video("正传 4K", nil)
	f.group(repMain, repAlt)
	id2 := f.collection("正传系列", f2, repAlt, repMain)
	requireNext(t, "组代表", f.progress(id2), repMain.ID, 2, false)
}

func TestCollectionProgressNextWorkCases(t *testing.T) {
	f := newProgressFixture(t)

	// ① 最近一次活动的那一部还在看：续播它（后面还有没看的也不跳过），播的是有进度的那个成员。
	a1 := f.video("A1", f.watched(10))
	a2Main := f.video("A2", nil)
	a2Alt := f.video("A2 加长版", f.inProgress(50, 1200))
	a3 := f.video("A3", nil)
	f.group(a2Main, a2Alt)
	resume := f.progress(f.collection("续播", a1, a2Main, a2Alt, a3))
	requireNext(t, "续播", resume, a2Alt.ID, 2, true)
	if resume.Next.WatchPositionSeconds != 1200 || resume.Next.Duration != 3600 || resume.InProgressWorks != 1 || resume.WatchedWorks != 1 {
		t.Fatalf("续播信息不对: %+v next=%+v", resume, *resume.Next)
	}

	// ② 最近看过的那一部之后第一部没看完的（它前面还有没看的，也不回头）。
	b1 := f.video("B1", nil)
	b2 := f.video("B2", f.watched(60))
	b3 := f.video("B3", f.watched(70))
	b4 := f.video("B4", f.watched(20))
	b5 := f.video("B5", nil)
	requireNext(t, "之后第一部", f.progress(f.collection("往后", b1, b2, b3, b4, b5)), b5.ID, 5, false)

	// ③ 后面都看完了：取最前面一部没看完的。
	c1 := f.video("C1", nil)
	c2 := f.video("C2", f.watched(10))
	c3 := f.video("C3", f.watched(90))
	requireNext(t, "回到开头", f.progress(f.collection("回头", c1, c2, c3)), c1.ID, 1, false)

	// ④ 一部都没看：第一部；作品集页也要能「从下一部播放」。
	d1 := f.video("D1", nil)
	d2 := f.video("D2", nil)
	fresh := f.progress(f.collection("没开始", d1, d2))
	requireNext(t, "没开始", fresh, d1.ID, 1, false)
	if fresh.LastActivityAt != nil {
		t.Fatal("没有活动时间")
	}

	// 全部看完：作品集算看完，没有下一部。
	e1 := f.video("E1", f.watched(1))
	e2 := f.video("E2", f.watched(2))
	done := f.progress(f.collection("看完", e1, e2))
	if !done.Complete || done.Next != nil || done.WatchedWorks != 2 {
		t.Fatalf("全部看完应 complete 且没有下一部: %+v", done)
	}

	// 同一活动时间：取靠后的一部作为「最近」那一部。
	g1 := f.video("G1", f.watched(40))
	g2 := f.video("G2", f.watched(40))
	g3 := f.video("G3", nil)
	g4 := f.video("G4", nil)
	requireNext(t, "同时间", f.progress(f.collection("同时", g1, g2, g4, g3)), g4.ID, 3, false)

	// 看过但又留了断点（重看）不算在看。
	h1 := f.video("H1", func(v *models.Video) {
		f.watched(30)(v)
		v.WatchPositionSeconds, v.WatchProgressUpdatedAt = 600, f.at(31)
	})
	h2 := f.video("H2", nil)
	rewatch := f.progress(f.collection("重看", h1, h2))
	if rewatch.InProgressWorks != 0 {
		t.Fatalf("看过的不算在看: %+v", rewatch)
	}
	requireNext(t, "重看之后", rewatch, h2.ID, 2, false)
}

func TestCollectionProgressContinueCandidatesOrderAndLimit(t *testing.T) {
	f := newProgressFixture(t)
	ids := map[string]uint{}
	// 三个在追的作品集，最近活动时间各不相同。
	for index, name := range []string{"早", "中", "晚"} {
		ids[name] = f.collection(name, f.video(name+"1", f.watched(10*(index+1))), f.video(name+"2", nil))
	}
	ids["单部"] = f.collection("单部", f.video("单部1", f.inProgress(500, 30)))                          // 只有一部：不进候选
	ids["看完"] = f.collection("看完", f.video("看完1", f.watched(600)), f.video("看完2", f.watched(601))) // 全部看完：不进
	ids["没看"] = f.collection("没看", f.video("没看1", nil), f.video("没看2", nil))                       // 没有活动：不进
	staleWatched := f.video("失效1", func(v *models.Video) { f.watched(700)(v); v.IsStale = true })
	ids["失效"] = f.collection("失效", staleWatched, f.video("失效2", nil)) // 唯一有活动的成员失效了：不进
	// 只有断点、没看过任何一部：在看也算。
	ids["在看"] = f.collection("在看", f.video("在看1", f.inProgress(5, 100)), f.video("在看2", nil))

	result, err := f.service.ListCollectionProgress(CollectionProgressRequest{ContinueOnly: true, CollectionIDs: []uint{ids["看完"]}})
	if err != nil {
		t.Fatal(err)
	}
	got := []uint{}
	for _, item := range result {
		got = append(got, item.CollectionID)
	}
	requireUintSlice(t, "候选按最近活动倒序", got, []uint{ids["晚"], ids["中"], ids["早"], ids["在看"]})

	limited, err := f.service.ListCollectionProgress(CollectionProgressRequest{ContinueOnly: true, Limit: 2})
	if err != nil || len(limited) != 2 || limited[0].CollectionID != ids["晚"] {
		t.Fatalf("Limit 应截取最近的两个: %+v err=%v", limited, err)
	}
	// 默认 12、最大 50。
	for i := 0; i < 55; i++ {
		f.collection(fmt.Sprintf("批量%02d", i), f.video(fmt.Sprintf("批量%02d-1", i), f.watched(1000+i)), f.video(fmt.Sprintf("批量%02d-2", i), nil))
	}
	if all, err := f.service.ListCollectionProgress(CollectionProgressRequest{ContinueOnly: true}); err != nil || len(all) != collectionProgressDefaultLimit {
		t.Fatalf("默认 Limit 应为 12: %d err=%v", len(all), err)
	}
	if all, err := f.service.ListCollectionProgress(CollectionProgressRequest{ContinueOnly: true, Limit: 500}); err != nil || len(all) != collectionProgressMaxLimit {
		t.Fatalf("Limit 上限应为 50: %d err=%v", len(all), err)
	}
}

func TestCollectionProgressByIDsSkipsUnknownAndKeepsOrder(t *testing.T) {
	f := newProgressFixture(t)
	gone := f.video("已删", f.watched(5))
	first := f.collection("一", f.video("一1", nil), gone)
	second := f.collection("二", f.video("二1", f.watched(1)))
	empty := f.collection("空")
	if err := database.DB.Delete(&models.Video{}, gone.ID).Error; err != nil {
		t.Fatal(err)
	}
	deleted := f.collection("删掉的作品集", f.video("删1", nil))
	if err := database.DB.Delete(&models.MediaCollection{}, deleted).Error; err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ListCollectionProgress(CollectionProgressRequest{CollectionIDs: []uint{second, 9999, first, deleted, empty, second}})
	if err != nil {
		t.Fatal(err)
	}
	got := []uint{}
	for _, item := range result {
		got = append(got, item.CollectionID)
	}
	requireUintSlice(t, "不存在与已删除的跳过、按传入顺序去重", got, []uint{second, first, empty})
	if result[1].TotalWorks != 1 || result[1].WatchedWorks != 0 {
		t.Fatalf("软删视频不算: %+v", result[1])
	}
	if result[2].TotalWorks != 0 || result[2].Complete || result[2].Next != nil {
		t.Fatalf("空作品集: %+v", result[2])
	}
	if result[0].Name != "二" || !result[0].Complete {
		t.Fatalf("单部看完的作品集: %+v", result[0])
	}
	tooMany := make([]uint, collectionProgressMaxIDs+1)
	for i := range tooMany {
		tooMany[i] = uint(i + 1)
	}
	if _, err := f.service.ListCollectionProgress(CollectionProgressRequest{CollectionIDs: tooMany}); err == nil {
		t.Fatal("超过 200 个应报错")
	}
	if none, err := f.service.ListCollectionProgress(CollectionProgressRequest{}); err != nil || len(none) != 0 || none == nil {
		t.Fatalf("空请求返回空数组: %v %v", none, err)
	}
}

// include_works：作品集页一次拿到逐部明细（成员、代表、状态、续播成员）与失效成员，不必再逐个成员查版本组。
func TestCollectionProgressIncludeWorks(t *testing.T) {
	f := newProgressFixture(t)
	first := f.video("第一部", f.watched(10))
	secondMain := f.video("第二部", nil)
	secondAlt := f.video("第二部 加长版", f.inProgress(20, 900))
	third := f.video("第三部", nil)
	offline := f.video("第四部（离线）", func(v *models.Video) { v.IsStale = true; f.watched(30)(v) })
	f.group(secondMain, secondAlt)
	id := f.collection("系列", first, secondAlt, offline, secondMain, third)

	plain := f.progress(id)
	if plain.Works != nil || plain.OfflineVideoIDs != nil {
		t.Fatalf("不带 include_works 时不给明细: %+v", plain)
	}
	result, err := f.service.ListCollectionProgress(CollectionProgressRequest{CollectionIDs: []uint{id}, IncludeWorks: true})
	if err != nil || len(result) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	progress := result[0]
	if len(progress.Works) != 3 || progress.TotalWorks != 3 {
		t.Fatalf("应有 3 部（离线成员不算）: %+v", progress.Works)
	}
	want := []CollectionProgressWork{
		{WorkIndex: 1, VideoIDs: []uint{first.ID}, RepresentativeVideoID: first.ID, State: CollectionWorkWatched},
		{WorkIndex: 2, VideoIDs: []uint{secondMain.ID, secondAlt.ID}, RepresentativeVideoID: secondMain.ID, State: CollectionWorkInProgress, ResumeVideoID: secondAlt.ID},
		{WorkIndex: 3, VideoIDs: []uint{third.ID}, RepresentativeVideoID: third.ID, State: CollectionWorkUnwatched},
	}
	if fmt.Sprintf("%+v", progress.Works) != fmt.Sprintf("%+v", want) {
		t.Fatalf("逐部明细不对:\n got %+v\nwant %+v", progress.Works, want)
	}
	requireUintSlice(t, "离线成员", progress.OfflineVideoIDs, []uint{offline.ID})
	requireNext(t, "明细与下一部一致", progress, secondAlt.ID, 2, true)

	if _, err := f.service.ListCollectionProgress(CollectionProgressRequest{ContinueOnly: true, IncludeWorks: true}); err == nil {
		t.Fatal("include_works 不能与 continue_only 一起用")
	}
	tooMany := make([]uint, collectionProgressMaxIDsWithWorks+1)
	for i := range tooMany {
		tooMany[i] = uint(i + 1)
	}
	if _, err := f.service.ListCollectionProgress(CollectionProgressRequest{CollectionIDs: tooMany, IncludeWorks: true}); err == nil {
		t.Fatal("带明细时超过上限应报错")
	}
}
