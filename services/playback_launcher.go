package services

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"video-master/models"
)

// 续播口径（用户裁决：这个行为要可配置）。
const (
	PlaybackResumeModeResume         = "resume"
	PlaybackResumeModeRestart        = "restart"
	PlaybackResumeModeRestartWatched = "restart_watched"
)

// iinaCLIPath 是 IINA 的命令行入口。只有走它才能带参数启动；
// 用系统默认打开方式（open）没有办法告诉播放器"这次别续播"。
const iinaCLIPath = "/Applications/IINA.app/Contents/MacOS/iina-cli"

// 测试接缝
var (
	iinaCLILookup = func() (string, bool) {
		info, err := os.Stat(iinaCLIPath)
		if err != nil || info.IsDir() || info.Mode().Perm()&0111 == 0 {
			return "", false
		}
		return iinaCLIPath, true
	}
	runIINACommand = func(binary string, args ...string) error {
		return exec.Command(binary, args...).Start()
	}
)

// normalizePlaybackResumeMode 把空值和不认识的取值都归到默认口径。
func normalizePlaybackResumeMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case PlaybackResumeModeRestart:
		return PlaybackResumeModeRestart
	case PlaybackResumeModeRestartWatched:
		return PlaybackResumeModeRestartWatched
	default:
		return PlaybackResumeModeResume
	}
}

// shouldRestartPlayback 判断这次播放要不要强制从头。
func shouldRestartPlayback(mode string, video *models.Video) bool {
	switch normalizePlaybackResumeMode(mode) {
	case PlaybackResumeModeRestart:
		return true
	case PlaybackResumeModeRestartWatched:
		return video != nil && video.IsWatched
	default:
		return false
	}
}

// launchPlayback 打开视频。需要从头播时走 iina-cli 并显式关掉续播——
// 这样不用去删 IINA 的断点文件，进度记录能保住。IINA 不在时退回系统默认方式：
// 别的播放器本来也不会自动续播。
func launchPlayback(video *models.Video, mode string) error {
	if video == nil || strings.TrimSpace(video.Path) == "" {
		return fmt.Errorf("视频路径为空")
	}
	if !shouldRestartPlayback(mode, video) {
		return openWithDefaultFn(video.Path, false)
	}
	binary, ok := iinaCLILookup()
	if !ok {
		return openWithDefaultFn(video.Path, false)
	}
	return runIINACommand(binary, "--mpv-resume-playback=no", video.Path)
}
