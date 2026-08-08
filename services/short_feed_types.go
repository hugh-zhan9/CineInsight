package services

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ShortFeedMediaKind 一条 feed 内容的媒体类型。图片 ID 与视频 ID 各自从 1 开始，
// 共用一个命名空间会直接撞车，所以媒体路由、排除集与互动接口一律带类型。
type ShortFeedMediaKind string

const (
	ShortFeedMediaVideo ShortFeedMediaKind = "video"
	ShortFeedMediaImage ShortFeedMediaKind = "image"
)

// ShortFeedMediaRef 是类型化的媒体标识，跨 DTO、URL 与排除集通用。
type ShortFeedMediaRef struct {
	Kind ShortFeedMediaKind `json:"media_kind"`
	ID   uint               `json:"id"`
}

// Key 是排除集与客户端最近列表使用的稳定文本形式，例如 "video:12"。
func (r ShortFeedMediaRef) Key() string {
	return fmt.Sprintf("%s:%d", r.Kind, r.ID)
}

func (r ShortFeedMediaRef) Valid() bool {
	return r.ID > 0 && (r.Kind == ShortFeedMediaVideo || r.Kind == ShortFeedMediaImage)
}

// ParseShortFeedMediaKind 只接受已知类型，未知类型一律拒绝而不是回退成视频。
func ParseShortFeedMediaKind(value string) (ShortFeedMediaKind, bool) {
	switch ShortFeedMediaKind(strings.TrimSpace(value)) {
	case ShortFeedMediaVideo:
		return ShortFeedMediaVideo, true
	case ShortFeedMediaImage:
		return ShortFeedMediaImage, true
	default:
		return "", false
	}
}

// ParseShortFeedMediaRef 解析 "video:12" 形式的键。
func ParseShortFeedMediaRef(value string) (ShortFeedMediaRef, bool) {
	kindText, idText, found := strings.Cut(strings.TrimSpace(value), ":")
	if !found {
		return ShortFeedMediaRef{}, false
	}
	kind, ok := ParseShortFeedMediaKind(kindText)
	if !ok {
		return ShortFeedMediaRef{}, false
	}
	id, err := strconv.ParseUint(strings.TrimSpace(idText), 10, 64)
	if err != nil || id == 0 {
		return ShortFeedMediaRef{}, false
	}
	return ShortFeedMediaRef{Kind: kind, ID: uint(id)}, true
}

const (
	DefaultShortFeedMaxDurationMinutes = 5
	defaultShortFeedMaxDurationSeconds = 300.0
	ShortFeedPreferenceBoostCap        = 0.5
	ShortFeedPreferenceStep            = 0.25
	DefaultShortFeedPortStart          = 18088
	DefaultShortFeedPortEnd            = 18108
)

type ShortFeedTagDTO struct {
	ID    uint   `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// ShortFeedItemDTO 是手机端拿到的一条内容。Duration 只对视频有意义，图片为 0。
type ShortFeedItemDTO struct {
	MediaKind     ShortFeedMediaKind `json:"media_kind"`
	ID            uint               `json:"id"`
	Name          string             `json:"name"`
	Duration      float64            `json:"duration"`
	Width         int                `json:"width"`
	Height        int                `json:"height"`
	Tags          []ShortFeedTagDTO  `json:"tags"`
	MediaURL      string             `json:"media_url"`
	MediaMIME     string             `json:"media_mime"`
	Description   string             `json:"description,omitempty"`
	Liked         bool               `json:"liked"`
	Favorited     bool               `json:"favorited"`
	ReasonCode    string             `json:"reason_code,omitempty"`
	ReasonMessage string             `json:"reason_message,omitempty"`
}

// Ref 返回这一条的类型化标识。
func (d ShortFeedItemDTO) Ref() ShortFeedMediaRef {
	return ShortFeedMediaRef{Kind: d.MediaKind, ID: d.ID}
}

type ShortFeedInteractionDTO struct {
	MediaKind    ShortFeedMediaKind `json:"media_kind"`
	MediaID      uint               `json:"media_id"`
	Liked        bool               `json:"liked"`
	Favorited    bool               `json:"favorited"`
	ViewCount    int                `json:"view_count"`
	LastViewedAt *time.Time         `json:"last_viewed_at,omitempty"`
	LikedAt      *time.Time         `json:"liked_at,omitempty"`
	FavoritedAt  *time.Time         `json:"favorited_at,omitempty"`
}

type ShortFeedServerStatus struct {
	Running       bool     `json:"running"`
	BindAddress   string   `json:"bind_address"`
	Port          int      `json:"port"`
	URL           string   `json:"url"`
	LANURLs       []string `json:"lan_urls"`
	StartupError  string   `json:"startup_error"`
	FallbackUsed  bool     `json:"fallback_used"`
	AllowedAccess string   `json:"allowed_access"`
}

type ShortFeedPlayRequest struct {
	Source string `json:"source"`
}

type ShortFeedLikeRequest struct {
	Liked bool `json:"liked"`
}

type ShortFeedFavoriteRequest struct {
	Favorited bool `json:"favorited"`
}

type ShortFeedDeleteRequest struct {
	ConfirmMoveToTrash bool `json:"confirm_move_to_trash"`
}
