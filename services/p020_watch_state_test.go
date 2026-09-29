package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// P-020：观看状态（D-PC41、D-PC42、D-PC47、D-PC52 视频侧）与手动标签接入（D-PC28 规则 2）。

// recordingWatchObserver 记录 OnVideoWatchedChanged 的调用；panicOn 非零时对该视频 panic，
// 用来确认观察者失败不会影响 setter。
type recordingWatchObserver struct {
	mu      sync.Mutex
	calls   []string
	panicOn uint
}

func (o *recordingWatchObserver) OnVideoWatchedChanged(videoID uint, watched bool) {
	o.mu.Lock()
	o.calls = append(o.calls, watchCallKey(videoID, watched))
	o.mu.Unlock()
	if o.panicOn != 0 && o.panicOn == videoID {
		panic("observer failed")
	}
}

func (o *recordingWatchObserver) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.calls...)
}

func watchCallKey(videoID uint, watched bool) string {
	return fmt.Sprintf("%d:%v", videoID, watched)
}

func p020Video(t *testing.T, video models.Video) models.Video {
	t.Helper()
	if video.Directory == "" {
		video.Directory = "/media"
	}
	if video.Path == "" {
		video.Path = "/media/" + video.Name
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	return video
}

func p020Reload(t *testing.T, id uint) models.Video {
	t.Helper()
	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	return video
}

// PLAY-11：片尾区间 = min(时长 × 5%, 180 秒)。这组样例与前端 utils/watchState.js 共用。
func TestWatchCompletionSamplesPLAY11(t *testing.T) {
	cases := []struct {
		name     string
		position float64
		duration float64
		tail     float64
		want     bool
	}{
		{"两小时片停在 1:57:30", 7050, 7200, 180, true},
		{"两小时片片尾区间边界", 7020, 7200, 180, true},
		{"两小时片差 181 秒", 7019, 7200, 180, false},
		{"一小时片区间正好 180 秒", 3420, 3600, 180, true},
		{"二十八秒片 5%", 26.6, 28, 1.4, true},
		{"二十八秒片出区间", 26.5, 28, 1.4, false},
		{"亚秒片位置 0 不算", 0, 0.8, 0.04, false},
		{"时长未知不算", 9999, 0, 0, false},
		{"位置为 0 不算", 0, 7200, 180, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := watchCompletionTail(testCase.duration); testCase.duration > 0 && (got < testCase.tail-1e-9 || got > testCase.tail+1e-9) {
				t.Fatalf("片尾区间 duration=%v got=%v want=%v", testCase.duration, got, testCase.tail)
			}
			if got := isWatchCompleted(testCase.position, testCase.duration); got != testCase.want {
				t.Fatalf("isWatchCompleted(%v, %v)=%v want %v", testCase.position, testCase.duration, got, testCase.want)
			}
		})
	}
}

// PLAY-10：resumable 与 resumableSQL 必须同口径——Go 用于续播，SQL 用于「继续观看」与 Jellyfin。
func TestResumableMatchesResumableSQLPLAY10(t *testing.T) {
	setupVideoServiceTestDB(t)
	base := time.Now().Truncate(time.Second)
	before, after := base.Add(-time.Hour), base.Add(time.Hour)
	cases := []struct {
		name  string
		video models.Video
		want  bool
	}{
		{"没有断点", models.Video{Name: "a.mp4"}, false},
		{"没看完有断点", models.Video{Name: "b.mp4", WatchPositionSeconds: 10}, true},
		{"已看但不知道何时标的", models.Video{Name: "c.mp4", WatchPositionSeconds: 10, IsWatched: true}, true},
		{"标已看之前留下的断点", models.Video{Name: "d.mp4", WatchPositionSeconds: 10, IsWatched: true, WatchedAt: &base, WatchProgressUpdatedAt: &before}, false},
		{"标已看之后重看", models.Video{Name: "e.mp4", WatchPositionSeconds: 10, IsWatched: true, WatchedAt: &base, WatchProgressUpdatedAt: &after}, true},
		{"已看且没有进度时间", models.Video{Name: "f.mp4", WatchPositionSeconds: 10, IsWatched: true, WatchedAt: &base}, false},
		{"已看且断点为 0", models.Video{Name: "g.mp4", IsWatched: true, WatchedAt: &base, WatchProgressUpdatedAt: &after}, false},
		{"同一时刻写入不算重看", models.Video{Name: "h.mp4", WatchPositionSeconds: 10, IsWatched: true, WatchedAt: &base, WatchProgressUpdatedAt: &base}, false},
	}
	expected := map[uint]bool{}
	for index := range cases {
		created := p020Video(t, cases[index].video)
		reloaded := p020Reload(t, created.ID)
		if got := resumable(&reloaded); got != cases[index].want {
			t.Fatalf("%s: resumable=%v want %v", cases[index].name, got, cases[index].want)
		}
		expected[created.ID] = cases[index].want
	}
	var ids []uint
	if err := database.DB.Model(&models.Video{}).Where(resumableSQL).Order("id").Pluck("id", &ids).Error; err != nil {
		t.Fatalf("resumableSQL 查询失败: %v", err)
	}
	got := map[uint]bool{}
	for _, id := range ids {
		got[id] = true
	}
	for id, want := range expected {
		if got[id] != want {
			t.Fatalf("video %d: resumableSQL=%v 与 resumable=%v 不一致", id, got[id], want)
		}
	}
}

// PLAY-10：从字幕命中 / 跳转开始的会话（origin=jump）不回写更早的位置；从断点或片头开始的可以。
func TestUpdateVideoWatchProgressJumpOnlyMovesForwardPLAY10(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video := p020Video(t, models.Video{Name: "jump.mp4", Duration: 7200})
	if _, err := svc.UpdateVideoWatchProgress(video.ID, 3000, 0, false, WatchProgressOriginResume); err != nil {
		t.Fatalf("记录断点失败: %v", err)
	}
	stored := p020Reload(t, video.ID)

	updated, err := svc.UpdateVideoWatchProgress(video.ID, 100, 0, false, WatchProgressOriginJump)
	if err != nil {
		t.Fatalf("jump 上报失败: %v", err)
	}
	if updated.WatchPositionSeconds != 3000 {
		t.Fatalf("jump 会话不该把断点往回写: %v", updated.WatchPositionSeconds)
	}
	if !updated.WatchProgressUpdatedAt.Equal(*stored.WatchProgressUpdatedAt) {
		t.Fatalf("没有写入时进度更新时间也不该变 before=%v after=%v", stored.WatchProgressUpdatedAt, updated.WatchProgressUpdatedAt)
	}
	if updated, err = svc.UpdateVideoWatchProgress(video.ID, 3500, 0, false, WatchProgressOriginJump); err != nil || updated.WatchPositionSeconds != 3500 {
		t.Fatalf("jump 会话往前推应当写入: %+v err=%v", updated, err)
	}
	if updated, err = svc.UpdateVideoWatchProgress(video.ID, 200, 0, false, WatchProgressOriginStart); err != nil || updated.WatchPositionSeconds != 200 {
		t.Fatalf("从片头开始的会话可以回写更早的位置: %+v err=%v", updated, err)
	}
	if updated, err = svc.UpdateVideoWatchProgress(video.ID, 150, 0, false, WatchProgressOriginResume); err != nil || updated.WatchPositionSeconds != 150 {
		t.Fatalf("从断点开始的会话可以回写更早的位置: %+v err=%v", updated, err)
	}
	// 看完与断点方向无关：jump 会话播到片尾照样判看完。
	if updated, err = svc.UpdateVideoWatchProgress(video.ID, 100, 0, true, WatchProgressOriginJump); err != nil || !updated.IsWatched || updated.WatchPositionSeconds != 0 {
		t.Fatalf("jump 会话的 completed 仍应生效: %+v err=%v", updated, err)
	}

	// 已看之前留下的旧断点不是有效断点：重看时从字幕命中起播，进度照样写得进去。
	stalePoint := p020Video(t, models.Video{Name: "stale-point.mp4", Duration: 7200})
	if _, err := svc.UpdateVideoWatchProgress(stalePoint.ID, 2400, 0, false, WatchProgressOriginResume); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := svc.SetVideoWatched(stalePoint.ID, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if updated, err = svc.UpdateVideoWatchProgress(stalePoint.ID, 600, 0, false, WatchProgressOriginJump); err != nil || updated.WatchPositionSeconds != 600 || !resumable(updated) {
		t.Fatalf("旧断点不该挡住重看的 jump 进度: %+v err=%v", updated, err)
	}

	for _, origin := range []string{"", "seek"} {
		if _, err := svc.UpdateVideoWatchProgress(video.ID, 10, 0, false, origin); err == nil {
			t.Fatalf("起播来源 %q 应被拒绝", origin)
		}
	}
	if _, err := svc.UpdateVideoWatchProgress(video.ID, 10, -1, false, WatchProgressOriginStart); err == nil {
		t.Fatal("负的播放时长应被拒绝")
	}
}

// PLAY-10：已看片重看写下的断点会被续播；看完后重看的断点在「继续观看」里出现。
func TestRewatchProgressIsResumablePLAY10(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video := p020Video(t, models.Video{Name: "rewatch.mp4", Duration: 7200})
	finished, err := svc.UpdateVideoWatchProgress(video.ID, 7200, 0, false, WatchProgressOriginResume)
	if err != nil || !finished.IsWatched || resumable(finished) {
		t.Fatalf("看完后不应可续播: %+v err=%v", finished, err)
	}
	time.Sleep(5 * time.Millisecond)
	again, err := svc.UpdateVideoWatchProgress(video.ID, 600, 0, false, WatchProgressOriginStart)
	if err != nil {
		t.Fatalf("重看上报失败: %v", err)
	}
	if !again.IsWatched || again.WatchPositionSeconds != 600 || !resumable(again) {
		t.Fatalf("重看写下的断点应当可续播: %+v", again)
	}
	page, err := svc.ListContinueWatchingWithFilter(LibraryFilter{}, "", 0, 20)
	if err != nil || len(page.Videos) != 1 || page.Videos[0].ID != video.ID {
		t.Fatalf("重看中的已看片应出现在继续观看: %+v err=%v", page, err)
	}
	// 手动标已看（误点保护：断点不动），断点早于这次已看时间，不再续播。
	manual, err := svc.SetVideoWatched(video.ID, true)
	if err != nil || manual.WatchPositionSeconds != 600 || resumable(manual) {
		t.Fatalf("手动标已看后断点保留但不再续播: %+v err=%v", manual, err)
	}
}

// 播放器报的时长只在库里时长未知时使用。
func TestUpdateVideoWatchProgressReportedDurationOnlyWhenUnknownPLAY11(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	unknown := p020Video(t, models.Video{Name: "unknown.mp4"})
	updated, err := svc.UpdateVideoWatchProgress(unknown.ID, 7050, 7200, false, WatchProgressOriginResume)
	if err != nil || !updated.IsWatched || updated.WatchPositionSeconds != 0 {
		t.Fatalf("库里时长未知时应按播放器时长判看完: %+v err=%v", updated, err)
	}
	known := p020Video(t, models.Video{Name: "known.mp4", Duration: 7200})
	updated, err = svc.UpdateVideoWatchProgress(known.ID, 3000, 3050, false, WatchProgressOriginResume)
	if err != nil || updated.IsWatched || updated.WatchPositionSeconds != 3000 {
		t.Fatalf("库里有时长时以库里为准: %+v err=%v", updated, err)
	}
}

// PLAY-09：「继续观看」按进度更新时间倒序的键集分页，老数据（无进度时间）排在最后。
func TestContinueWatchingOrdersByProgressUpdatedAtPLAY09(t *testing.T) {
	setupVideoServiceTestDB(t)
	base := time.Now().Truncate(time.Second).Add(-time.Hour)
	at := func(minutes int) *time.Time {
		value := base.Add(time.Duration(minutes) * time.Minute)
		return &value
	}
	v1 := p020Video(t, models.Video{Name: "v1.mp4", WatchPositionSeconds: 100, WatchProgressUpdatedAt: at(1), PlayCount: 99})
	v2 := p020Video(t, models.Video{Name: "v2.mp4", WatchPositionSeconds: 200, WatchProgressUpdatedAt: at(3)})
	v3 := p020Video(t, models.Video{Name: "v3.mp4", WatchPositionSeconds: 300})
	v4 := p020Video(t, models.Video{Name: "v4.mp4", WatchPositionSeconds: 400, WatchProgressUpdatedAt: at(2)})
	p020Video(t, models.Video{Name: "v5.mp4", WatchProgressUpdatedAt: at(9)})
	p020Video(t, models.Video{Name: "v6.mp4", WatchPositionSeconds: 50, IsWatched: true, WatchedAt: at(10), WatchProgressUpdatedAt: at(5)})
	v7 := p020Video(t, models.Video{Name: "v7.mp4", WatchPositionSeconds: 60, IsWatched: true, WatchedAt: at(0), WatchProgressUpdatedAt: at(4)})
	v8 := p020Video(t, models.Video{Name: "v8.mp4", WatchPositionSeconds: 500})

	svc := &VideoService{}
	want := []uint{v7.ID, v2.ID, v4.ID, v1.ID, v8.ID, v3.ID}
	var got []uint
	cursorAt, cursorID := "", uint(0)
	for pageIndex := 0; pageIndex < 5; pageIndex++ {
		page, err := svc.ListContinueWatchingWithFilter(LibraryFilter{}, cursorAt, cursorID, 2)
		if err != nil {
			t.Fatalf("第 %d 页失败: %v", pageIndex, err)
		}
		if len(page.Videos) == 0 {
			break
		}
		for _, video := range page.Videos {
			got = append(got, video.ID)
		}
		last := page.Videos[len(page.Videos)-1]
		cursorAt, cursorID = "", last.ID
		if last.WatchProgressUpdatedAt != nil {
			cursorAt = last.WatchProgressUpdatedAt.Format(time.RFC3339Nano)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("继续观看结果不对 got=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("继续观看顺序不对 got=%v want=%v", got, want)
		}
	}
	// 通用片库查询的「继续观看」视图用同一个 resumableSQL 口径。
	count, err := svc.CountLibraryVideos(LibraryFilter{SmartView: LibraryViewContinueWatching})
	if err != nil || count != int64(len(want)) {
		t.Fatalf("继续观看计数应为 %d，实际 %d err=%v", len(want), count, err)
	}
	if _, err := svc.ListContinueWatchingWithFilter(LibraryFilter{}, base.Format(time.RFC3339Nano), 0, 2); err == nil {
		t.Fatal("只有时间没有 id 的游标应被拒绝")
	}
	if _, err := svc.ListContinueWatchingWithFilter(LibraryFilter{}, "yesterday", 3, 2); err == nil {
		t.Fatal("无效的时间游标应被拒绝")
	}
}

type launcherSpy struct {
	iinaArgs [][]string
	opened   []string
	launched []string
}

func stubPlaybackLauncher(t *testing.T, iinaAvailable bool, launchErr error) *launcherSpy {
	t.Helper()
	spy := &launcherSpy{}
	restoreOpen, restoreLookup, restoreRun := openWithDefaultFn, iinaCLILookup, runIINACommand
	t.Cleanup(func() {
		openWithDefaultFn, iinaCLILookup, runIINACommand = restoreOpen, restoreLookup, restoreRun
		SetPlaybackLaunchedHook(nil)
	})
	openWithDefaultFn = func(path string, reveal bool) error {
		spy.opened = append(spy.opened, path)
		return launchErr
	}
	iinaCLILookup = func() (string, bool) { return "/fake/iina-cli", iinaAvailable }
	runIINACommand = func(binary string, args ...string) error {
		spy.iinaArgs = append(spy.iinaArgs, args)
		return launchErr
	}
	SetPlaybackLaunchedHook(func(videoID uint, path string, startPosition float64) {
		spy.launched = append(spy.launched, strings.Join([]string{path, formatSeconds(startPosition)}, "@"))
	})
	return spy
}

func formatSeconds(value float64) string { return mpvStartArg(value)[len("--mpv-start="):] }

// PLAY-06：续播模式下库内断点有效时，带 --mpv-start 启动 IINA，并登记本次会话的起播位置。
func TestLaunchPlaybackPassesLibraryResumePointPLAY06(t *testing.T) {
	spy := stubPlaybackLauncher(t, true, nil)
	resumeVideo := &models.Video{ID: 7, Path: "/media/resume.mp4", WatchPositionSeconds: 125.46}
	if err := launchPlayback(resumeVideo, PlaybackResumeModeResume); err != nil {
		t.Fatalf("续播启动失败: %v", err)
	}
	if len(spy.iinaArgs) != 1 || spy.iinaArgs[0][0] != "--mpv-start=125.5" || spy.iinaArgs[0][1] != "/media/resume.mp4" {
		t.Fatalf("续播应带库内断点（1 位小数）: %v", spy.iinaArgs)
	}
	watchedAt := time.Now()
	earlier := watchedAt.Add(-time.Hour)
	stale := &models.Video{ID: 8, Path: "/media/stale.mp4", WatchPositionSeconds: 40, IsWatched: true, WatchedAt: &watchedAt, WatchProgressUpdatedAt: &earlier}
	if err := launchPlayback(stale, PlaybackResumeModeResume); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if len(spy.opened) != 1 || len(spy.iinaArgs) != 1 {
		t.Fatalf("断点不可续时保持系统默认方式: open=%v iina=%v", spy.opened, spy.iinaArgs)
	}
	if err := launchPlayback(resumeVideo, PlaybackResumeModeRestart); err != nil {
		t.Fatalf("从头播启动失败: %v", err)
	}
	if len(spy.iinaArgs) != 2 || spy.iinaArgs[1][0] != "--mpv-resume-playback=no" {
		t.Fatalf("从头播模式不变: %v", spy.iinaArgs)
	}
	wantLaunched := []string{"/media/resume.mp4@125.5", "/media/stale.mp4@0.0", "/media/resume.mp4@0.0"}
	if strings.Join(spy.launched, ",") != strings.Join(wantLaunched, ",") {
		t.Fatalf("会话登记不对 got=%v want=%v", spy.launched, wantLaunched)
	}
}

func TestLaunchPlaybackDoesNotRegisterFailedLaunchPLAY03(t *testing.T) {
	spy := stubPlaybackLauncher(t, true, errors.New("启动失败"))
	if err := launchPlayback(&models.Video{ID: 9, Path: "/media/x.mp4", WatchPositionSeconds: 30}, PlaybackResumeModeResume); err == nil {
		t.Fatal("启动失败应当报错")
	}
	if len(spy.launched) != 0 {
		t.Fatalf("启动失败不该登记会话: %v", spy.launched)
	}
}

func newIINATestService(t *testing.T) (*IINAProgressService, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, iinaWatchLaterRelativeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return NewIINAProgressService(home), dir
}

// PLAY-03：从断点续播后播到结尾（watch_later 被删），按墙钟推算判为看完。
func TestIINARemovedEntryAfterResumeMarksWatchedPLAY03(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newIINATestService(t)
	var notified []string
	service.SetWatchedNotifier(func(videoID uint, watched bool) { notified = append(notified, watchCallKey(videoID, watched)) })
	video := p020Video(t, models.Video{Name: "resume.mp4", Duration: 7200, WatchPositionSeconds: 4200})
	now := time.Now()
	service.RegisterLaunchedSession(video.ID, video.Path, 4200, now.Add(-3000*time.Second))

	change, ok, err := service.settleRemovedEntry(iinaWatchLaterName(video.Path), now)
	if err != nil || !ok {
		t.Fatalf("续播后播完应当结算: ok=%v err=%v", ok, err)
	}
	if change.VideoID != video.ID || !change.Watched || change.WatchPositionSeconds != 0 {
		t.Fatalf("回报给界面的变更不对: %+v", change)
	}
	refreshed := p020Reload(t, video.ID)
	if !refreshed.IsWatched || refreshed.WatchedAt == nil || refreshed.WatchPositionSeconds != 0 {
		t.Fatalf("应标已看并清零断点: %+v", refreshed)
	}
	if len(notified) != 1 || notified[0] != watchCallKey(video.ID, true) {
		t.Fatalf("已看翻转应转发给观察者: %v", notified)
	}
	// 一次会话只结算一次。
	if _, ok, _ := service.settleRemovedEntry(iinaWatchLaterName(video.Path), now); ok {
		t.Fatal("同一会话不该结算两次")
	}
}

// PLAY-03：从头播完的已知缺口（设计 §8.2）——删除事件时墙钟推算不到片尾就不判；
// 未登记、过期、时长未知、文件其实还在，一律不动。
func TestIINARemovedEntryIgnoredOutsideSessionPLAY03(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, dir := newIINATestService(t)
	now := time.Now()

	fromStart := p020Video(t, models.Video{Name: "from-start.mp4", Duration: 7200})
	service.RegisterLaunchedSession(fromStart.ID, fromStart.Path, 0, now.Add(-10*time.Minute))
	unregistered := p020Video(t, models.Video{Name: "unregistered.mp4", Duration: 7200, WatchPositionSeconds: 7000})
	expired := p020Video(t, models.Video{Name: "expired.mp4", Duration: 7200, WatchPositionSeconds: 7000})
	service.RegisterLaunchedSession(expired.ID, expired.Path, 7000, now.Add(-13*time.Hour))
	unknown := p020Video(t, models.Video{Name: "unknown.mp4", WatchPositionSeconds: 7000})
	service.RegisterLaunchedSession(unknown.ID, unknown.Path, 7000, now.Add(-time.Hour))
	rewritten := p020Video(t, models.Video{Name: "rewritten.mp4", Duration: 7200, WatchPositionSeconds: 7000})
	service.RegisterLaunchedSession(rewritten.ID, rewritten.Path, 7000, now.Add(-time.Hour))
	writeIINAEntry(t, dir, rewritten.Path, "start=7100\n")

	for _, video := range []models.Video{fromStart, unregistered, expired, unknown, rewritten} {
		if _, ok, err := service.settleRemovedEntry(iinaWatchLaterName(video.Path), now); ok || err != nil {
			t.Fatalf("%s 不该被结算 ok=%v err=%v", video.Name, ok, err)
		}
		if refreshed := p020Reload(t, video.ID); refreshed.IsWatched {
			t.Fatalf("%s 不该被标已看", video.Name)
		}
	}
	// 文件被重写不是删除，会话保留；之后真的删掉再结算。
	if err := os.Remove(filepath.Join(dir, iinaWatchLaterName(rewritten.Path))); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := service.settleRemovedEntry(iinaWatchLaterName(rewritten.Path), now); !ok || err != nil {
		t.Fatalf("文件真正删除后应结算 ok=%v err=%v", ok, err)
	}
}

// PLAY-03：最后已知位置取三者最大值；达到时长 90% 也判看完。
func TestIINARemovedEntryUsesSessionPositionAndNinetyPercentPLAY03(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, dir := newIINATestService(t)
	now := time.Now()

	// 90%：从头播 6500 秒后删除，未进片尾区间（7020）但 ≥ 6480。
	ninety := p020Video(t, models.Video{Name: "ninety.mp4", Duration: 7200})
	service.RegisterLaunchedSession(ninety.ID, ninety.Path, 0, now.Add(-6500*time.Second))
	if _, ok, err := service.settleRemovedEntry(iinaWatchLaterName(ninety.Path), now); !ok || err != nil {
		t.Fatalf("达到 90%% 应判看完 ok=%v err=%v", ok, err)
	}

	// 会话期间读到的断点：库里有更新的写入，同步不采用文件位置，但会话记下它。
	future := now.Add(time.Hour)
	parsed := p020Video(t, models.Video{Name: "parsed.mp4", Duration: 7200, WatchPositionSeconds: 100, WatchProgressUpdatedAt: &future})
	service.RegisterLaunchedSession(parsed.ID, parsed.Path, 0, now.Add(-100*time.Second))
	writeIINAEntry(t, dir, parsed.Path, "start=6600\n")
	if _, err := service.Sync(); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if refreshed := p020Reload(t, parsed.ID); refreshed.WatchPositionSeconds != 100 {
		t.Fatalf("比库里旧的断点不该写库: %+v", refreshed)
	}
	if err := os.Remove(filepath.Join(dir, iinaWatchLaterName(parsed.Path))); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := service.settleRemovedEntry(iinaWatchLaterName(parsed.Path), now); !ok || err != nil {
		t.Fatalf("会话期间读到 6600 应判看完 ok=%v err=%v", ok, err)
	}
	if refreshed := p020Reload(t, parsed.ID); !refreshed.IsWatched || refreshed.WatchPositionSeconds != 0 {
		t.Fatalf("应标已看并清零: %+v", refreshed)
	}
}

// PLAY-03：监听到删除事件时发 iina-progress-synced（Changes 里 Watched=true）。
func TestIINAWatchLoopSettlesRemovedEntryPLAY03(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, dir := newIINATestService(t)
	video := p020Video(t, models.Video{Name: "loop.mp4", Duration: 7200, WatchPositionSeconds: 6500})
	writeIINAEntry(t, dir, video.Path, "start=6500\n")
	service.RegisterLaunchedSession(video.ID, video.Path, 6500, time.Now().Add(-10*time.Minute))

	synced := make(chan IINAProgressSyncResult, 8)
	service.SetOnSynced(func(result IINAProgressSyncResult) { synced <- result })
	if err := service.StartWatching(); err != nil {
		t.Fatalf("启动监听失败: %v", err)
	}
	defer service.StopWatching()
	if err := os.Remove(filepath.Join(dir, iinaWatchLaterName(video.Path))); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case result := <-synced:
			for _, change := range result.Changes {
				if change.VideoID == video.ID && change.Watched {
					if refreshed := p020Reload(t, video.ID); !refreshed.IsWatched {
						t.Fatalf("事件已发但没落库: %+v", refreshed)
					}
					return
				}
			}
		case <-deadline:
			t.Fatal("删除断点文件后没有结算看完")
		}
	}
}

// PLAY-06：已看视频的陈旧断点跳过；标已看之后 IINA 里重看写下的断点照样采用。
func TestIINASyncAdoptsRewatchAfterWatchedPLAY06(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, dir := newIINATestService(t)
	watchedAt := time.Now().Add(-time.Hour)
	stale := p020Video(t, models.Video{Name: "stale.mp4", Duration: 7200, IsWatched: true, WatchedAt: &watchedAt})
	rewatch := p020Video(t, models.Video{Name: "rewatch.mp4", Duration: 7200, IsWatched: true, WatchedAt: &watchedAt})
	writeIINAEntry(t, dir, stale.Path, "start=1800\n")
	mustSetFileModTime(t, filepath.Join(dir, iinaWatchLaterName(stale.Path)), watchedAt.Add(-time.Hour))
	writeIINAEntry(t, dir, rewatch.Path, "start=2400\n")

	// 以最近写入为准：IINA 里往回拖、比库里更新的断点，即使位置更早也采用。
	earlierWrite := time.Now().Add(-time.Hour)
	rewound := p020Video(t, models.Video{Name: "rewound.mp4", Duration: 7200, WatchPositionSeconds: 3000, WatchProgressUpdatedAt: &earlierWrite})
	writeIINAEntry(t, dir, rewound.Path, "start=1200\n")

	result, err := service.Sync()
	if err != nil || result.Updated != 2 || result.Skipped != 1 {
		t.Fatalf("同步结果不对: %+v err=%v", result, err)
	}
	if refreshed := p020Reload(t, rewound.ID); refreshed.WatchPositionSeconds != 1200 {
		t.Fatalf("更新的断点即使更早也应采用: %+v", refreshed)
	}
	if refreshed := p020Reload(t, stale.ID); refreshed.WatchPositionSeconds != 0 {
		t.Fatalf("标已看之前的断点不该把已看片拉回在看: %+v", refreshed)
	}
	refreshed := p020Reload(t, rewatch.ID)
	if refreshed.WatchPositionSeconds != 2400 || !refreshed.IsWatched || !resumable(&refreshed) {
		t.Fatalf("重看写下的断点应当采用并可续播: %+v", refreshed)
	}
}

// PLAY-14：设置页的 IINA 同步状态。
func TestIINASyncStatusPLAY14(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, dir := newIINATestService(t)
	if status := service.Status(); status.Enabled || status.WatchingDir != "" || status.LastSyncAt != nil || status.LastError != "" {
		t.Fatalf("初始状态不对: %+v", status)
	}
	if _, err := service.Sync(); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if status := service.Status(); status.LastSyncAt == nil || status.LastError != "" {
		t.Fatalf("同步成功后应记录时间: %+v", status)
	}
	if err := service.StartWatching(); err != nil {
		t.Fatalf("启动监听失败: %v", err)
	}
	if status := service.Status(); !status.Enabled || status.WatchingDir != dir {
		t.Fatalf("监听中应报告目录: %+v", status)
	}
	service.StopWatching()
	if status := service.Status(); status.Enabled || status.WatchingDir != "" {
		t.Fatalf("停止后不再报告监听: %+v", status)
	}

	missing := NewIINAProgressService(t.TempDir())
	if _, err := missing.Sync(); err == nil {
		t.Fatal("没有断点目录应当报错")
	}
	status := missing.Status()
	if status.LastError == "" || strings.Contains(status.LastError, "/") {
		t.Fatalf("失败原因应记录且不含路径: %+v", status)
	}
}

// APP-05（D-PC52 视频侧）：已看状态实际翻转时才通知观察者；回写入口不通知；观察者失败不影响写入。
func TestWatchStateObserverNotifiedOnFlipOnlyAPP05(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	observer := &recordingWatchObserver{}
	svc.SetWatchStateObserver(observer)
	video := p020Video(t, models.Video{Name: "flip.mp4", Duration: 100})

	if _, err := svc.SetVideoWatched(video.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetVideoWatched(video.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetVideoWatched(video.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetVideoWatched(video.ID, false); err != nil {
		t.Fatal(err)
	}
	want := []string{watchCallKey(video.ID, true), watchCallKey(video.ID, false)}
	if got := observer.snapshot(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("只有翻转才通知 got=%v want=%v", got, want)
	}

	// 自动判看完同样算翻转；已看后再播到片尾不重复通知。
	if _, err := svc.UpdateVideoWatchProgress(video.ID, 100, 0, false, WatchProgressOriginResume); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateVideoWatchProgress(video.ID, 100, 0, false, WatchProgressOriginResume); err != nil {
		t.Fatal(err)
	}
	want = append(want, watchCallKey(video.ID, true))
	if got := observer.snapshot(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("自动看完应通知一次 got=%v want=%v", got, want)
	}

	// 榜单回写入口：状态照写，观察者不通知；已是目标状态时是空操作，不刷新已看时间。
	before := p020Reload(t, video.ID)
	if err := svc.SetVideoWatchedFromLink(video.ID, true); err != nil {
		t.Fatal(err)
	}
	if after := p020Reload(t, video.ID); !after.WatchedAt.Equal(*before.WatchedAt) {
		t.Fatalf("回写已看不该刷新已看时间 before=%v after=%v", before.WatchedAt, after.WatchedAt)
	}
	if err := svc.SetVideoWatchedFromLink(video.ID, false); err != nil {
		t.Fatal(err)
	}
	if refreshed := p020Reload(t, video.ID); refreshed.IsWatched || refreshed.WatchedAt != nil {
		t.Fatalf("回写取消已看应生效: %+v", refreshed)
	}
	if got := observer.snapshot(); len(got) != len(want) {
		t.Fatalf("回写入口不该通知观察者: %v", got)
	}
	if err := svc.SetVideoWatchedFromLink(999999, true); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("不存在的视频应返回未找到: %v", err)
	}
	if _, err := svc.SetVideoWatched(999999, true); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("不存在的视频应返回未找到: %v", err)
	}

	// 观察者失败只记日志。
	observer.panicOn = video.ID
	updated, err := svc.SetVideoWatched(video.ID, true)
	if err != nil || !updated.IsWatched {
		t.Fatalf("观察者失败不该影响写入: %+v err=%v", updated, err)
	}

	// IINA 同步经 NotifyWatchStateChanged 转发到同一个观察者。
	observer.panicOn = 0
	iina, dir := newIINATestService(t)
	iina.SetWatchedNotifier(svc.NotifyWatchStateChanged)
	other := p020Video(t, models.Video{Name: "iina-end.mp4", Duration: 28})
	writeIINAEntry(t, dir, other.Path, "start=27.9\n")
	if _, err := iina.Sync(); err != nil {
		t.Fatal(err)
	}
	got := observer.snapshot()
	if got[len(got)-1] != watchCallKey(other.ID, true) {
		t.Fatalf("IINA 判看完应通知观察者: %v", got)
	}
}

// META-06：手动加标签在同一事务里作废同视频匹配该标签的待审候选；其他候选照常可以批准。
func TestAddTagToVideoSupersedesMatchingCandidateMETA06(t *testing.T) {
	setupVideoServiceTestDB(t)
	tagA := p015Tag(t, "动作", "custom")
	tagB := p015Tag(t, "悬疑", "custom")
	video := p015Video(t, "manual-then-approve.mp4")
	matching := p015Candidate(t, video.ID, tagA, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	other := p015Candidate(t, video.ID, tagB, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)

	if err := (&VideoService{}).AddTagToVideo(video.ID, tagA.ID); err != nil {
		t.Fatalf("手动加标签失败: %v", err)
	}
	if got := p015CandidateStatus(t, matching.ID); got != models.AITagCandidateStatusSuperseded {
		t.Fatalf("匹配候选应作废，实际 %s", got)
	}
	if got := p015CandidateStatus(t, other.ID); got != models.AITagCandidateStatusPending {
		t.Fatalf("其他候选不该被动，实际 %s", got)
	}
	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	if _, err := svc.ApproveCandidate(other.ID); err != nil {
		t.Fatalf("已有人工标签时批准不该被作废: %v", err)
	}
	var count int64
	if err := database.DB.Table("video_tags").Where("video_id = ? AND tag_id IN ?", video.ID, []uint{tagA.ID, tagB.ID}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("两个标签都应在视频上 count=%d err=%v", count, err)
	}
}

// IMG-08：图片手动加标签同样作废同图匹配候选。
func TestAddTagToImageSupersedesMatchingCandidateIMG08(t *testing.T) {
	newImageAITaggingTestService(t, imageTaggingClientFunc(nil))
	library := imageAITaggingTestLibrary(t, "海边", "日落")
	img := imageAITaggingTestImage(t, "jpg")
	matching := seedImageAITagCandidate(t, img.ID, library[0], models.AITagConfidenceHigh)
	other := seedImageAITagCandidate(t, img.ID, library[1], models.AITagConfidenceHigh)

	if err := NewImageLibraryService().AddTagToImage(img.ID, library[0].ID); err != nil {
		t.Fatalf("手动加标签失败: %v", err)
	}
	statusOf := func(id uint) string {
		var candidate models.ImageAITagCandidate
		if err := database.DB.First(&candidate, id).Error; err != nil {
			t.Fatal(err)
		}
		return candidate.Status
	}
	if statusOf(matching.ID) != models.AITagCandidateStatusSuperseded || statusOf(other.ID) != models.AITagCandidateStatusPending {
		t.Fatalf("状态不对 matching=%s other=%s", statusOf(matching.ID), statusOf(other.ID))
	}
	if !imageHasTag(t, img.ID, library[0].ID) {
		t.Fatal("标签应已加到图片上")
	}
}

// META-13（D-PC36）：片库列表载荷批量带出人工「加上」的自动标签，一页只查一次。
func TestLibraryVideoPageCarriesAutomaticOverrideKindsMETA13(t *testing.T) {
	setupVideoServiceTestDB(t)
	a := p020Video(t, models.Video{Name: "a.mp4"})
	b := p020Video(t, models.Video{Name: "b.mp4"})
	c := p020Video(t, models.Video{Name: "c.mp4"})
	overrides := []models.VideoAutomaticTagOverride{
		{VideoID: a.ID, AutomaticKind: lowResolutionAutomaticTagKind, Present: true},
		{VideoID: a.ID, AutomaticKind: shortVideoAutomaticTagKind, Present: true},
		{VideoID: b.ID, AutomaticKind: shortVideoAutomaticTagKind, Present: false},
	}
	if err := database.DB.Create(&overrides).Error; err != nil {
		t.Fatal(err)
	}

	var queries int
	const callbackName = "p020:count-override-queries"
	if err := database.DB.Callback().Query().After("gorm:query").Register(callbackName, func(db *gorm.DB) {
		if db.Statement.Table == "video_automatic_tag_overrides" {
			queries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.DB.Callback().Query().Remove(callbackName) })

	page, err := (&VideoService{}).SearchLibraryVideoPage(LibraryFilter{}, nil, 20)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if queries != 1 {
		t.Fatalf("自动标签覆盖应整页一次查询，实际 %d 次", queries)
	}
	wantA := []string{lowResolutionAutomaticTagKind, shortVideoAutomaticTagKind}
	if lowResolutionAutomaticTagKind > shortVideoAutomaticTagKind {
		wantA = []string{shortVideoAutomaticTagKind, lowResolutionAutomaticTagKind}
	}
	if got := page.AutomaticOverrideKinds[a.ID]; strings.Join(got, ",") != strings.Join(wantA, ",") {
		t.Fatalf("a 的覆盖不对 got=%v want=%v", got, wantA)
	}
	if _, ok := page.AutomaticOverrideKinds[b.ID]; ok {
		t.Fatalf("手动去掉（present=false）的覆盖没有标签可挂角标: %v", page.AutomaticOverrideKinds)
	}
	if _, ok := page.AutomaticOverrideKinds[c.ID]; ok {
		t.Fatalf("没有覆盖的视频不该出现: %v", page.AutomaticOverrideKinds)
	}

	empty, err := (&VideoService{}).SearchLibraryVideoPage(LibraryFilter{Keyword: "no-such-video"}, nil, 20)
	if err != nil || empty.AutomaticOverrideKinds == nil || len(empty.AutomaticOverrideKinds) != 0 {
		t.Fatalf("空页也应返回空映射: %+v err=%v", empty, err)
	}
}

// APP-05：VideoService 与 MovieChartService 双向接好之后，任一侧改已看只同步一次、不成环。
func TestVideoAndChartWatchedSyncWithoutLoopAPP05(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	svc := &VideoService{}
	svc.SetWatchStateObserver(h.service)
	h.service.SetLinkedVideoWatchSetter(svc)
	h.seedEntry("201", "沙丘", "2021-10-22")
	video := h.seedVideo("沙丘.mkv", "")
	if err := h.service.LinkMovieToVideo("201", video.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SetVideoWatched(video.ID, true); err != nil {
		t.Fatal(err)
	}
	if row, ok := h.markRow("201"); !ok || row.Mark != models.MovieChartMarkWatched {
		t.Fatalf("视频标已看应同步到观影记录: %+v", row)
	}
	if _, err := svc.SetVideoWatched(video.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.markRow("201"); ok {
		t.Fatal("视频取消已看应撤销观影记录")
	}
	if _, err := h.service.MarkEntry("201", models.MovieChartMarkWatched); err != nil {
		t.Fatal(err)
	}
	if refreshed := p020Reload(t, video.ID); !refreshed.IsWatched {
		t.Fatalf("观影记录标已看应回写关联视频: %+v", refreshed)
	}
	if err := h.service.ClearMark("201"); err != nil {
		t.Fatal(err)
	}
	if refreshed := p020Reload(t, video.ID); refreshed.IsWatched {
		t.Fatalf("观影记录撤销应回写关联视频: %+v", refreshed)
	}
}
