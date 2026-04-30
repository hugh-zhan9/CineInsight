package services

import "time"

const (
	ShortFeedMaxDurationSeconds = 300.0
	ShortFeedPreferenceBoostCap = 0.5
	ShortFeedPreferenceStep     = 0.25
	DefaultShortFeedPortStart   = 18088
	DefaultShortFeedPortEnd     = 18108
)

type ShortFeedTagDTO struct {
	ID    uint   `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type ShortFeedVideoDTO struct {
	ID            uint              `json:"id"`
	Name          string            `json:"name"`
	Duration      float64           `json:"duration"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	Tags          []ShortFeedTagDTO `json:"tags"`
	MediaURL      string            `json:"media_url"`
	MediaMIME     string            `json:"media_mime"`
	Liked         bool              `json:"liked"`
	Favorited     bool              `json:"favorited"`
	ReasonCode    string            `json:"reason_code,omitempty"`
	ReasonMessage string            `json:"reason_message,omitempty"`
}

type ShortFeedInteractionDTO struct {
	VideoID      uint       `json:"video_id"`
	Liked        bool       `json:"liked"`
	Favorited    bool       `json:"favorited"`
	ViewCount    int        `json:"view_count"`
	LastViewedAt *time.Time `json:"last_viewed_at,omitempty"`
	LikedAt      *time.Time `json:"liked_at,omitempty"`
	FavoritedAt  *time.Time `json:"favorited_at,omitempty"`
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
