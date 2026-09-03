package services

import (
	"testing"

	"video-master/models"
)

func TestShouldRestartPlaybackHonoursMode(t *testing.T) {
	watched := &models.Video{IsWatched: true}
	unwatched := &models.Video{}
	cases := []struct {
		mode  string
		video *models.Video
		want  bool
	}{
		{PlaybackResumeModeResume, watched, false},
		{PlaybackResumeModeResume, unwatched, false},
		{PlaybackResumeModeRestart, watched, true},
		{PlaybackResumeModeRestart, unwatched, true},
		{PlaybackResumeModeRestartWatched, watched, true},
		{PlaybackResumeModeRestartWatched, unwatched, false},
		// 空值和乱填都归到默认口径，不能因为设置里存了脏数据就改变播放行为
		{"", watched, false},
		{"nonsense", watched, false},
	}
	for _, item := range cases {
		if got := shouldRestartPlayback(item.mode, item.video); got != item.want {
			t.Fatalf("mode=%q watched=%v 得到 %v，期望 %v", item.mode, item.video.IsWatched, got, item.want)
		}
	}
}

func TestLaunchPlaybackUsesIINAOnlyWhenRestarting(t *testing.T) {
	video := &models.Video{Path: "/media/a.mp4"}

	var openedPaths []string
	var iinaArgs [][]string
	restoreOpen := openWithDefaultFn
	restoreLookup := iinaCLILookup
	restoreRun := runIINACommand
	defer func() {
		openWithDefaultFn = restoreOpen
		iinaCLILookup = restoreLookup
		runIINACommand = restoreRun
	}()
	openWithDefaultFn = func(path string, reveal bool) error {
		openedPaths = append(openedPaths, path)
		return nil
	}
	iinaCLILookup = func() (string, bool) { return "/fake/iina-cli", true }
	runIINACommand = func(binary string, args ...string) error {
		iinaArgs = append(iinaArgs, append([]string{binary}, args...))
		return nil
	}

	// 续播口径：走系统默认打开方式，让播放器自己决定
	if err := launchPlayback(video, PlaybackResumeModeResume); err != nil {
		t.Fatalf("续播模式启动失败: %v", err)
	}
	if len(openedPaths) != 1 || len(iinaArgs) != 0 {
		t.Fatalf("续播模式不该动用 iina-cli: open=%v iina=%v", openedPaths, iinaArgs)
	}

	// 从头播：必须显式关掉续播，且不去删断点文件
	if err := launchPlayback(video, PlaybackResumeModeRestart); err != nil {
		t.Fatalf("从头播启动失败: %v", err)
	}
	if len(iinaArgs) != 1 {
		t.Fatalf("从头播应当走 iina-cli: %v", iinaArgs)
	}
	if iinaArgs[0][1] != "--mpv-resume-playback=no" || iinaArgs[0][2] != "/media/a.mp4" {
		t.Fatalf("参数不对: %v", iinaArgs[0])
	}
}

func TestLaunchPlaybackFallsBackWhenIINAMissing(t *testing.T) {
	video := &models.Video{Path: "/media/a.mp4"}
	var opened int
	restoreOpen := openWithDefaultFn
	restoreLookup := iinaCLILookup
	defer func() {
		openWithDefaultFn = restoreOpen
		iinaCLILookup = restoreLookup
	}()
	openWithDefaultFn = func(path string, reveal bool) error { opened++; return nil }
	iinaCLILookup = func() (string, bool) { return "", false }

	// 没装 IINA 时不能因为"要从头播"就播不了，退回系统默认方式
	if err := launchPlayback(video, PlaybackResumeModeRestart); err != nil {
		t.Fatalf("没有 IINA 时应当退回系统默认方式: %v", err)
	}
	if opened != 1 {
		t.Fatalf("应当用系统默认方式打开一次，实际 %d", opened)
	}
}
