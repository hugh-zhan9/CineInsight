package services

import "video-master/models"

type PlaybackAttemptResult struct {
	Video             *models.Video `json:"video,omitempty"`
	DispatchSucceeded bool          `json:"dispatch_succeeded"`
	UserMessage       string        `json:"user_message,omitempty"`
	ReasonCode        string        `json:"reason_code,omitempty"`
	// Reason 是失败原因的分类：offline_root / missing_file / error（D-PC11）。
	Reason          string                   `json:"reason,omitempty"`
	SelectionReason string                   `json:"selection_reason,omitempty"`
	ReconcileResult *PlaybackReconcileResult `json:"reconcile_result,omitempty"`
	// RerollToken 只在筛选内随机播放启动成功时给出：30 秒内凭它「换一个」（D-PC44、R10）。
	RerollToken string `json:"reroll_token,omitempty"`
}

type PlaybackReconcileResult struct {
	VideoID            uint          `json:"video_id"`
	Reason             string        `json:"reason,omitempty"`
	DidMarkStale       bool          `json:"did_mark_stale"`
	DidRelocate        bool          `json:"did_relocate"`
	DidRefreshMetadata bool          `json:"did_refresh_metadata"`
	NeedsReload        bool          `json:"needs_reload"`
	UpdatedVideo       *models.Video `json:"updated_video,omitempty"`
	ReasonCode         string        `json:"reason_code,omitempty"`
}
