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
	ExternalAction *PreviewExternalAction   `json:"external_action,omitempty"`
	ReasonCode     string                   `json:"reason_code,omitempty"`
	ReasonMessage  string                   `json:"reason_message,omitempty"`
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
