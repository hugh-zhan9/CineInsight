package services

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"
	"video-master/models"
)

// 桌面内嵌预览按实际编码判定能不能直放（2026-10-13 用户裁决「直接播 + 自动代理」）。
//
// 这套判定只服务桌面预览（GetPreviewSession / ResolvePreviewMedia）。手机端与 Jellyfin 仍按
// inlinePreviewMIMEs 的扩展名白名单（TestInlinePreviewMIMEsUnchanged），两边有意分开。

// desktopDirectPlayContainerMIMEs 是按编码判定直放的容器（ISO-BMFF 一族）与源文件下发时的 MIME。
var desktopDirectPlayContainerMIMEs = map[string]string{
	".mp4": "video/mp4",
	".m4v": "video/x-m4v",
	".mov": "video/quicktime",
}

var (
	desktopDirectPlayVideoCodecs = map[string]struct{}{"h264": {}, "hevc": {}, "prores": {}}
	desktopDirectPlayAudioCodecs = map[string]struct{}{"aac": {}, "mp3": {}, "alac": {}, "ac3": {}, "eac3": {}}
)

// previewCodecProbeTimeout 是打开预览时补探技术快照的上限：探不出来就按扩展名沿用旧行为。
const previewCodecProbeTimeout = 5 * time.Second

type desktopPreviewRoute int

const (
	// desktopPreviewLegacy：编码判不出来（快照缺失且补探失败），按扩展名沿用旧行为。
	desktopPreviewLegacy desktopPreviewRoute = iota
	// desktopPreviewDirect：源文件直接内嵌。
	desktopPreviewDirect
	// desktopPreviewProxy：要经播放代理，打开预览时自动生成。
	desktopPreviewProxy
)

type desktopPreviewDecision struct {
	Route desktopPreviewRoute
	// MIME 只在 Direct 时有值。
	MIME string
}

// decideDesktopPreview 是判定表本身（纯函数）：
//   - mp4 / m4v / mov：容器确是 ISO-BMFF，视频 h264 / hevc / prores，音频 aac / mp3 / alac / ac3 / eac3
//     或没有音频 → 直放；快照判不出来 → 旧行为；其余 → 代理。
//   - webm / ogv / ogg：照旧按扩展名直放。
//   - 其余扩展名（mkv、avi、wmv、flv、ts…）→ 代理，不必看快照。
func decideDesktopPreview(path string, snapshot playbackProxySnapshot, snapshotReady bool) desktopPreviewDecision {
	containerMIME, codecGated := desktopDirectPlayContainerMIMEs[strings.ToLower(filepath.Ext(path))]
	if !codecGated {
		if mimeType, ok := inlinePreviewMIME(path); ok {
			return desktopPreviewDecision{Route: desktopPreviewDirect, MIME: mimeType}
		}
		return desktopPreviewDecision{Route: desktopPreviewProxy}
	}
	if !snapshotReady {
		return desktopPreviewDecision{Route: desktopPreviewLegacy}
	}
	if desktopDirectPlayable(snapshot) {
		return desktopPreviewDecision{Route: desktopPreviewDirect, MIME: containerMIME}
	}
	return desktopPreviewDecision{Route: desktopPreviewProxy}
}

func desktopDirectPlayable(snapshot playbackProxySnapshot) bool {
	if !snapshot.HasVideo || !isoBMFFFormat(snapshot.FormatName) {
		return false
	}
	if _, ok := desktopDirectPlayVideoCodecs[snapshot.VideoCodec]; !ok {
		return false
	}
	if !snapshot.HasAudio {
		return true
	}
	_, ok := desktopDirectPlayAudioCodecs[snapshot.AudioCodec]
	return ok
}

// isoBMFFFormat 确认扩展名是 mp4/m4v/mov 的文件实际也是那一族容器（改了扩展名的 mkv 不能直放）。
// ffprobe 给这三种的 format_name 都是 "mov,mp4,m4a,3gp,3g2,mj2"；快照没有 format_name 时不据此否决。
func isoBMFFFormat(formatName string) bool {
	name := strings.ToLower(strings.TrimSpace(formatName))
	if name == "" {
		return true
	}
	for _, part := range strings.Split(name, ",") {
		if part == "mov" || part == "mp4" {
			return true
		}
	}
	return false
}

// desktopPreviewPlan 查判定表。只有 mp4 / m4v / mov 需要技术快照：缺失或不新鲜时用播放代理同一套
// 探测（MediaProbeService.Refresh + loadPlaybackProxySnapshot）限时补探一次。
func (s *VideoService) desktopPreviewPlan(video models.Video) desktopPreviewDecision {
	if _, codecGated := desktopDirectPlayContainerMIMEs[strings.ToLower(filepath.Ext(video.Path))]; !codecGated {
		return decideDesktopPreview(video.Path, playbackProxySnapshot{}, false)
	}
	snapshot, ready := s.previewCodecSnapshot(video)
	return decideDesktopPreview(video.Path, snapshot, ready)
}

func (s *VideoService) previewCodecSnapshot(video models.Video) (playbackProxySnapshot, bool) {
	snapshot, ready, err := loadPlaybackProxySnapshot(video)
	if err == nil && ready {
		return snapshot, true
	}
	// 只用注入的探测服务：没有注入（测试里的裸 VideoService）就不探，按旧行为走。
	if err != nil || s.mediaProbe == nil {
		return playbackProxySnapshot{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), previewCodecProbeTimeout)
	defer cancel()
	if err := s.mediaProbe.Refresh(ctx, video.ID); err != nil {
		log.Printf("预览编码探测失败，按扩展名沿用旧行为 video_id=%d err=%s", video.ID, scrubPlaybackProxyPaths(err.Error()))
		return playbackProxySnapshot{}, false
	}
	snapshot, ready, err = loadPlaybackProxySnapshot(video)
	return snapshot, err == nil && ready
}
