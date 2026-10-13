package services

import (
	"context"
	"fmt"
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
	desktopDirectPlayAudioCodecs = map[string]struct{}{
		"aac": {}, "mp3": {}, "alac": {}, "ac3": {}, "eac3": {},
		// 相机 / ProRes 的 .mov 多是 LPCM（2026-10-13 评审修订）。
		"pcm_s16le": {}, "pcm_s16be": {}, "pcm_s24le": {}, "pcm_s24be": {}, "pcm_s32le": {}, "pcm_f32le": {},
	}
)

// previewCodecProbeTimeout 是打开预览时补探技术快照的上限：探不出来就按扩展名沿用旧行为。
// 是变量只为测试能缩短它。
var previewCodecProbeTimeout = 5 * time.Second

// previewFFmpegAvailable 判断本机有没有 ffmpeg：没有就不自动生成代理（按扩展名沿用旧行为，也不会落失败记录）。
var previewFFmpegAvailable = func() bool {
	_, err := findThumbnailFFmpeg()
	return err == nil
}

// 自动生成代理的大小上限（2026-10-13 评审修订，集成方裁决）：预计产物超过 min(10 GiB, 代理上限的 20%) 时
// 不自动生成，只给手动「生成播放代理」。代理上限为 0（不限）时取 10 GiB。
const (
	autoProxyMaxBytes     int64 = 10 << 30
	autoProxyLimitPercent int64 = 20
)

type desktopPreviewRoute int

const (
	// desktopPreviewLegacy：编码判不出来（快照缺失且补探失败），按扩展名沿用旧行为。
	desktopPreviewLegacy desktopPreviewRoute = iota
	// desktopPreviewDirect：源文件直接内嵌。
	desktopPreviewDirect
	// desktopPreviewProxy：要经播放代理（是否自动生成还要过大小上限与 ffmpeg 检查）。
	desktopPreviewProxy
)

type desktopPreviewDecision struct {
	Route desktopPreviewRoute
	// MIME 只在 Direct 时有值。
	MIME string
	// Snapshot 是判定用的技术快照（Proxy 时用来估算产物大小）。
	Snapshot playbackProxySnapshot
}

// decideDesktopPreview 是判定表本身（纯函数）：
//   - mp4 / m4v / mov：容器确是 ISO-BMFF，视频 h264（8 位 4:2:0）/ hevc（不是 hev1）/ prores，音频在
//     desktopDirectPlayAudioCodecs 里或没有音频 → 直放；快照判不出来 → 旧行为；其余 → 代理。
//   - webm / ogv / ogg：照旧按扩展名直放。
//   - 其余扩展名（mkv、avi、wmv、flv、ts…）→ 代理；快照判不出（探测失败）→ 旧行为（代理任务同样会探不出来）。
func decideDesktopPreview(path string, snapshot playbackProxySnapshot, snapshotReady bool) desktopPreviewDecision {
	containerMIME, codecGated := desktopDirectPlayContainerMIMEs[strings.ToLower(filepath.Ext(path))]
	if !codecGated {
		if mimeType, ok := inlinePreviewMIME(path); ok {
			return desktopPreviewDecision{Route: desktopPreviewDirect, MIME: mimeType}
		}
	}
	if !snapshotReady {
		return desktopPreviewDecision{Route: desktopPreviewLegacy}
	}
	if codecGated && desktopDirectPlayable(snapshot) {
		return desktopPreviewDecision{Route: desktopPreviewDirect, MIME: containerMIME, Snapshot: snapshot}
	}
	return desktopPreviewDecision{Route: desktopPreviewProxy, Snapshot: snapshot}
}

func desktopDirectPlayable(snapshot playbackProxySnapshot) bool {
	if !snapshot.HasVideo || !isoBMFFFormat(snapshot.FormatName) {
		return false
	}
	if _, ok := desktopDirectPlayVideoCodecs[snapshot.VideoCodec]; !ok {
		return false
	}
	// WebKit 只认 hvc1 标签的 HEVC：hev1 走 remux 代理（代理一律打 hvc1）。标签未知（老快照）不否决。
	if snapshot.VideoCodec == "hevc" && snapshot.VideoCodecTag == "hev1" {
		return false
	}
	// 不是 8 位 4:2:0 的 H.264 解不出来，走转码代理。
	if snapshot.VideoCodec == "h264" && !h264PixelFormatPlayable(snapshot.VideoPixelFormat) {
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

// desktopPreviewPlan 查判定表。webm / ogv / ogg 不看快照；其余都要技术快照：缺失或不新鲜时用播放代理
// 同一套探测（MediaProbeService.Refresh + loadPlaybackProxySnapshot）限时补探一次。
func (s *VideoService) desktopPreviewPlan(video models.Video, source playbackProxyFingerprint) desktopPreviewDecision {
	_, codecGated := desktopDirectPlayContainerMIMEs[strings.ToLower(filepath.Ext(video.Path))]
	if _, whitelisted := inlinePreviewMIME(video.Path); whitelisted && !codecGated {
		return decideDesktopPreview(video.Path, playbackProxySnapshot{}, false)
	}
	snapshot, ready := s.previewCodecSnapshot(video, source)
	return decideDesktopPreview(video.Path, snapshot, ready)
}

// previewProbeKey：探测失败按「视频 + 源指纹」记在内存里（本次运行有效），源文件变了才会再探。
type previewProbeKey struct {
	videoID uint
	source  playbackProxyFingerprint
}

func (s *VideoService) previewCodecSnapshot(video models.Video, source playbackProxyFingerprint) (playbackProxySnapshot, bool) {
	snapshot, ready, err := loadPlaybackProxySnapshot(video)
	if err == nil && ready {
		return snapshot, true
	}
	// 只用注入的探测服务：没有注入（测试里的裸 VideoService）就不探，按旧行为走。
	key := previewProbeKey{videoID: video.ID, source: source}
	if err != nil || s.mediaProbe == nil || s.previewProbeFailedBefore(key) {
		return playbackProxySnapshot{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), previewCodecProbeTimeout)
	defer cancel()
	if err := s.mediaProbe.Refresh(ctx, video.ID); err != nil {
		log.Printf("预览编码探测失败，按扩展名沿用旧行为 video_id=%d err=%s", video.ID, scrubPlaybackProxyPaths(err.Error()))
		s.rememberPreviewProbeFailure(key)
		return playbackProxySnapshot{}, false
	}
	snapshot, ready, err = loadPlaybackProxySnapshot(video)
	if err != nil || !ready {
		// 探出来了但没有可用视频流：同样记下，下次直接按旧行为。
		s.rememberPreviewProbeFailure(key)
		return playbackProxySnapshot{}, false
	}
	return snapshot, true
}

func (s *VideoService) previewProbeFailedBefore(key previewProbeKey) bool {
	s.previewProbeMu.Lock()
	defer s.previewProbeMu.Unlock()
	_, failed := s.previewProbeFailures[key]
	return failed
}

func (s *VideoService) rememberPreviewProbeFailure(key previewProbeKey) {
	s.previewProbeMu.Lock()
	defer s.previewProbeMu.Unlock()
	if s.previewProbeFailures == nil {
		s.previewProbeFailures = make(map[previewProbeKey]struct{})
	}
	s.previewProbeFailures[key] = struct{}{}
}

// autoProxySizeLimit = min(10 GiB, 代理上限 × 20%)；上限为 0（不限）时取 10 GiB。
func autoProxySizeLimit(cacheLimitBytes int64) int64 {
	if cacheLimitBytes <= 0 {
		return autoProxyMaxBytes
	}
	return min(autoProxyMaxBytes, cacheLimitBytes*autoProxyLimitPercent/100)
}

// estimatePlaybackProxyBytes 估算代理大小：remux 取源大小；转码按（目标视频码率 + 音频码率）× 时长，
// 目标码率与转码实际用的一致（min(源码率, 8 Mbps)）；时长未知时退到源大小。
func estimatePlaybackProxyBytes(snapshot playbackProxySnapshot, sourceSize int64, durationSeconds float64) int64 {
	if playbackProxyRemuxable(snapshot) || durationSeconds <= 0 {
		return sourceSize
	}
	bitsPerSecond := playbackProxyTargetBitrate(snapshot)
	if snapshot.HasAudio {
		bitsPerSecond += playbackProxyAudioBitsPerSecond
	}
	return int64(float64(bitsPerSecond) * durationSeconds / 8)
}

// autoProxyTooLargeMessage 是大小超限时会话里的说明。
func autoProxyTooLargeMessage(estimate int64) string {
	return fmt.Sprintf("文件较大（约 %.1f GB），生成播放代理会占用相同大小的空间", float64(estimate)/float64(1<<30))
}
