package services

// Native playback contracts contain capabilities and media identity, never paths.
type NativePlaybackCapability struct {
	WindowEpoch    uint64 `json:"window_epoch"`
	WindowReady    bool   `json:"window_ready"`
	Available      bool   `json:"available"`
	RuntimeVersion string `json:"runtime_version"`
	Platform       string `json:"platform"`
	ReasonCode     string `json:"reason_code"`
	ReasonMessage  string `json:"reason_message"`
}
type NativeOwnerRequest struct {
	WindowEpoch    uint64 `json:"window_epoch"`
	ComponentNonce string `json:"component_nonce"`
}
type NativeOwner struct {
	OwnerID    string `json:"owner_id"`
	OwnerToken string `json:"owner_token"`
}
type NativeOwnerRequestRef struct {
	OwnerID    string `json:"owner_id"`
	OwnerToken string `json:"owner_token"`
}
type NativePlaybackRef struct {
	OwnerID    string `json:"owner_id"`
	OwnerToken string `json:"owner_token"`
	SessionID  string `json:"session_id"`
}
type NativePlaybackOpenRequest struct {
	RequestID     string   `json:"request_id"`
	OwnerID       string   `json:"owner_id"`
	OwnerToken    string   `json:"owner_token"`
	VideoID       uint     `json:"video_id"`
	SourceVersion string   `json:"source_version"`
	QueueToken    string   `json:"queue_token"`
	StartMS       int64    `json:"start_ms"`
	Paused        bool     `json:"paused"`
	Muted         *bool    `json:"muted"`
	Volume        *float64 `json:"volume"`
}
type NativePlaybackCommand struct {
	OwnerID    string `json:"owner_id"`
	OwnerToken string `json:"owner_token"`
	SessionID  string `json:"session_id"`
	Seq        uint64 `json:"seq"`
	Kind       string `json:"kind"`
	Value      any    `json:"value,omitempty"`
}
type NativePlaybackCommandResult struct {
	AcceptedSeq uint64 `json:"accepted_seq"`
}
type NativePlaybackViewport struct {
	OwnerID    string  `json:"owner_id"`
	OwnerToken string  `json:"owner_token"`
	SessionID  string  `json:"session_id"`
	Revision   uint64  `json:"revision"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Width      float64 `json:"width"`
	Height     float64 `json:"height"`
	Visible    bool    `json:"visible"`
	Occluded   bool    `json:"occluded"`
}
type NativePlaybackRevision struct {
	AppliedRevision uint64 `json:"applied_revision"`
}
type NativePlaybackCloseRequest struct {
	OwnerID    string `json:"owner_id"`
	OwnerToken string `json:"owner_token"`
	SessionID  string `json:"session_id"`
	Reason     string `json:"reason"`
}
type NativeSubtitlePreferences struct {
	Size       string `json:"size"`
	Background string `json:"background"`
	Position   string `json:"position"`
}
type NativePlaybackSubtitles struct {
	OwnerID             string                    `json:"owner_id"`
	OwnerToken          string                    `json:"owner_token"`
	SessionID           string                    `json:"session_id"`
	Revision            uint64                    `json:"revision"`
	SubtitleFingerprint string                    `json:"subtitle_fingerprint"`
	Enabled             bool                      `json:"enabled"`
	OffsetMS            int64                     `json:"offset_ms"`
	Preferences         NativeSubtitlePreferences `json:"preferences"`
}
type NativePlaybackSubtitleResult struct {
	AppliedRevision     uint64 `json:"applied_revision"`
	Status              string `json:"status"`
	SubtitleFingerprint string `json:"subtitle_fingerprint"`
}
type NativePlaybackState struct {
	SessionID     string  `json:"session_id"`
	Seq           uint64  `json:"seq"`
	VideoID       uint    `json:"video_id"`
	SourceVersion string  `json:"source_version"`
	State         string  `json:"state"`
	PositionMS    int64   `json:"position_ms"`
	DurationMS    int64   `json:"duration_ms"`
	VideoWidth    int64   `json:"video_width"`
	VideoHeight   int64   `json:"video_height"`
	Paused        bool    `json:"paused"`
	Muted         bool    `json:"muted"`
	Volume        float64 `json:"volume"`
	Rate          float64 `json:"rate"`
	Seekable      bool    `json:"seekable"`
	ErrorCode     string  `json:"error_code,omitempty"`
	ErrorMessage  string  `json:"error_message,omitempty"`
}
