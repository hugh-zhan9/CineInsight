package services

import "video-master/models"

type PlaybackAttemptResult struct {
	Video             *models.Video            `json:"video,omitempty"`
	DispatchSucceeded bool                     `json:"dispatch_succeeded"`
	UserMessage       string                   `json:"user_message,omitempty"`
	ReasonCode        string                   `json:"reason_code,omitempty"`
	ReconcileResult   *PlaybackReconcileResult `json:"reconcile_result,omitempty"`
}

type PlaybackReconcileResult struct {
	VideoID            uint          `json:"video_id"`
	DidMarkStale       bool          `json:"did_mark_stale"`
	DidRelocate        bool          `json:"did_relocate"`
	DidRefreshMetadata bool          `json:"did_refresh_metadata"`
	NeedsReload        bool          `json:"needs_reload"`
	UpdatedVideo       *models.Video `json:"updated_video,omitempty"`
	ReasonCode         string        `json:"reason_code,omitempty"`
}
