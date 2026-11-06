package playback

type Snapshot struct {
	DecodedFrames int64   `json:"decoded_frames"`
	FrontWindow   bool    `json:"front_window"`
	WindowVisible bool    `json:"window_visible"`
	Seeking       bool    `json:"seeking"`
	SubtitleText  string  `json:"subtitle_text"`
	X             float64 `json:"x"`
	Y             float64 `json:"y"`
	RenderWidth   float64 `json:"render_width"`
	RenderHeight  float64 `json:"render_height"`
	Position      float64 `json:"position"`
	Duration      float64 `json:"duration"`
	Paused        bool    `json:"paused"`
	Ended         bool    `json:"ended"`
	Muted         bool    `json:"muted"`
	Volume        float64 `json:"volume"`
	Rate          float64 `json:"rate"`
	Width         int64   `json:"width"`
	Height        int64   `json:"height"`
	Frames        int64   `json:"frames"`
	Hidden        bool    `json:"hidden"`
	Error         int     `json:"error"`
}
