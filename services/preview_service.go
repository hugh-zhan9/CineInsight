package services

import (
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const previewMediaRoutePrefix = "/preview/media/"

type PreviewSession struct {
	VideoID        uint                     `json:"video_id"`
	Mode           string                   `json:"mode"`
	DisplayName    string                   `json:"display_name"`
	InlineSource   *PreviewSourceDescriptor `json:"inline_source,omitempty"`
	SeekSprite     *SeekSpriteDescriptor    `json:"seek_sprite,omitempty"`
	ExternalAction *PreviewExternalAction   `json:"external_action,omitempty"`
	ReasonCode     string                   `json:"reason_code,omitempty"`
	ReasonMessage  string                   `json:"reason_message,omitempty"`
	// Proxy 只在这次内嵌预览走的是播放代理时出现（D-004），供界面标注「经代理」。
	Proxy *PreviewProxyDescriptor `json:"proxy,omitempty"`
}

// PreviewProxyDescriptor 描述本次预览命中的播放代理。
// 不含路径：代理是隐藏派生文件，路由形态与无代理时逐字节一致。
type PreviewProxyDescriptor struct {
	Strategy string `json:"strategy"`
	Size     int64  `json:"size"`
}

type PreviewSourceDescriptor struct {
	LocatorStrategy string `json:"locator_strategy"`
	LocatorValue    string `json:"locator_value"`
	MIME            string `json:"mime"`
}

type PreviewExternalAction struct {
	ActionID    string `json:"action_id"`
	ButtonLabel string `json:"button_label"`
	Hint        string `json:"hint"`
}

type PreviewMedia struct {
	Path        string
	DisplayName string
	MIME        string
	ModTime     time.Time
}

// playbackProxyMIME 是代理产物的 MIME。代理一律是 mp4，所以是个常量而不是查表——
// 这一行有意与 inlinePreviewMIMEs 分开：那张白名单说的是"源文件能不能直接内嵌"，
// 一个字都不许为代理改动（4.1.5 不变行为）。
const playbackProxyMIME = "video/mp4"

var inlinePreviewMIMEs = map[string]string{
	".mp4":  "video/mp4",
	".m4v":  "video/x-m4v",
	".webm": "video/webm",
	".ogv":  "video/ogg",
	".ogg":  "video/ogg",
}

func (s *VideoService) GetPreviewSession(videoID uint) (*PreviewSession, error) {
	video, err := s.GetVideo(videoID)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(video.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return &PreviewSession{
				VideoID:       video.ID,
				Mode:          "unsupported",
				DisplayName:   video.Name,
				ReasonCode:    "file_missing",
				ReasonMessage: "源文件不存在，当前无法预览。",
			}, nil
		}
		return nil, fmt.Errorf("检查预览文件失败: %w", err)
	}
	if info.IsDir() {
		return &PreviewSession{
			VideoID:       video.ID,
			Mode:          "unsupported",
			DisplayName:   video.Name,
			ReasonCode:    "path_is_directory",
			ReasonMessage: "当前路径不是可预览的视频文件。",
		}, nil
	}

	if mimeType, ok := inlinePreviewMIME(video.Path); ok {
		return &PreviewSession{
			VideoID:     video.ID,
			Mode:        "inline",
			DisplayName: video.Name,
			InlineSource: &PreviewSourceDescriptor{
				LocatorStrategy: "asset_route",
				LocatorValue:    previewMediaPath(video.ID),
				MIME:            mimeType,
			},
			SeekSprite: SeekSpriteIndex(video.ID, video.Duration),
		}, nil
	}

	// 白名单不命中时再看有没有可用的播放代理（D-004）。
	// 路由形态一个字都不改：locator 仍是 previewMediaPath(id)，
	// 换源发生在 ResolvePreviewMedia 里，前端与统计语义都察觉不到区别。
	if proxy := s.playbackProxies().resolveValidProxy(video.ID, playbackProxyFingerprintOf(info), true); proxy != nil {
		return &PreviewSession{
			VideoID:     video.ID,
			Mode:        "inline",
			DisplayName: video.Name,
			InlineSource: &PreviewSourceDescriptor{
				LocatorStrategy: "asset_route",
				LocatorValue:    previewMediaPath(video.ID),
				MIME:            playbackProxyMIME,
			},
			SeekSprite: SeekSpriteIndex(video.ID, video.Duration),
			Proxy:      &PreviewProxyDescriptor{Strategy: proxy.Strategy, Size: proxy.OutputSize},
		}, nil
	}

	return &PreviewSession{
		VideoID:     video.ID,
		Mode:        "external-preview",
		DisplayName: video.Name,
		ExternalAction: &PreviewExternalAction{
			ActionID:    "preview_externally",
			ButtonLabel: "使用系统播放器预览",
			Hint:        "将使用系统播放器进行预览，不计正式播放统计，这不是正式播放。",
		},
		ReasonCode:    "inline_not_supported",
		ReasonMessage: "当前文件格式不适合在应用内稳定预览，可改用系统播放器预览。",
	}, nil
}

func (s *VideoService) ResolvePreviewMedia(videoID uint) (*PreviewMedia, error) {
	video, err := s.GetVideo(videoID)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(video.Path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("预览路径不是文件")
	}

	mimeType, ok := inlinePreviewMIME(video.Path)
	if !ok {
		// 白名单不命中时才可能有代理：有的话下发代理字节（D-004）。
		if proxy := s.playbackProxies().resolveValidProxy(video.ID, playbackProxyFingerprintOf(info), true); proxy != nil {
			proxyInfo, statErr := os.Stat(proxy.Path)
			if statErr == nil {
				return &PreviewMedia{
					Path:        proxy.Path,
					DisplayName: video.Name,
					MIME:        playbackProxyMIME,
					ModTime:     proxyInfo.ModTime(),
				}, nil
			}
		}
		mimeType = fallbackVideoMIME(video.Path)
	}

	return &PreviewMedia{
		Path:        video.Path,
		DisplayName: video.Name,
		MIME:        mimeType,
		ModTime:     info.ModTime(),
	}, nil
}

func (s *VideoService) PreviewExternally(videoID uint) error {
	video, err := s.GetVideo(videoID)
	if err != nil {
		return err
	}
	return openWithDefaultFn(video.Path, false)
}

func previewMediaPath(videoID uint) string {
	return fmt.Sprintf("%s%d", previewMediaRoutePrefix, videoID)
}

func inlinePreviewMIME(path string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	mimeType, ok := inlinePreviewMIMEs[ext]
	return mimeType, ok
}

func fallbackVideoMIME(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if mimeType := mime.TypeByExtension(ext); mimeType != "" {
		return mimeType
	}
	return "application/octet-stream"
}
