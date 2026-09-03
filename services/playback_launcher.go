package services

import (
	"fmt"
	"log"
	"net/url"
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
	// openURLSchemeFn 交给 LaunchServices 打开自定义协议地址。
	openURLSchemeFn = func(deepLink string) error {
		return exec.Command("/usr/bin/open", deepLink).Start()
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

// LaunchStreamInIINA 把一条流交给本机的 IINA 播放。
//
// 走 iina:// 这个 URL scheme，**不用 iina-cli**。实测：同一个本机地址，curl 拿得到
// 200，iina-cli 却一次请求都不发（连之前成功过的简单 mp4 也一样），而 URL scheme
// 一次就通。scheme 走的是 LaunchServices，比命令行那条路稳。
//
// scheme 唯一的短板是带不了请求头——而这一点已经由本地流代理解决了：交给播放器的
// 是 127.0.0.1 上的地址，请求头由代理在取源站时加。所以这里只需要一个地址。
func LaunchStreamInIINA(streamURL string, headers map[string]string) error {
	target := strings.TrimSpace(streamURL)
	if target == "" {
		return fmt.Errorf("播放地址为空")
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		return fmt.Errorf("只支持 http/https 地址")
	}
	// 请求头到这里应当已经由代理接管；还带着说明调用方没走代理，记一笔便于排查。
	if len(headers) > 0 {
		log.Printf("IINA 播放：调用方仍带着 %d 个请求头，但 URL scheme 传不了，已忽略（应当走代理）", len(headers))
	}

	deepLink := "iina://weblink?url=" + url.QueryEscape(target)
	log.Printf("IINA 播放：打开 %s", deepLink)
	if err := openURLSchemeFn(deepLink); err != nil {
		return fmt.Errorf("起 IINA 失败: %w", err)
	}
	return nil
}
