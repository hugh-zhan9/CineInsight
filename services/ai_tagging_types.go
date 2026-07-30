package services

import (
	"context"
	"video-master/models"
)

type AITaggingReviewItem struct {
	ID             uint          `json:"id"`
	VideoID        uint          `json:"video_id"`
	Video          *models.Video `json:"video,omitempty"`
	VideoDeleted   bool          `json:"video_deleted"`
	SuggestedName  string        `json:"suggested_name"`
	NormalizedName string        `json:"normalized_name"`
	MatchedTagID   *uint         `json:"matched_tag_id,omitempty"`
	MatchedTag     *models.Tag   `json:"matched_tag,omitempty"`
	Confidence     string        `json:"confidence"`
	Reasoning      string        `json:"reasoning"`
	SourceSummary  string        `json:"source_summary"`
	Status         string        `json:"status"`
	CreatedAt      string        `json:"created_at"`
	UpdatedAt      string        `json:"updated_at"`
}

type AITaggingStatusSummary struct {
	ConfigAvailable  bool  `json:"config_available"`
	Pending          int64 `json:"pending"`
	SameSourceUnread int64 `json:"same_source_unread"`
	Processing       int64 `json:"processing"`
	Completed        int64 `json:"completed"`
	Skipped          int64 `json:"skipped"`
	Failed           int64 `json:"failed"`
}

type AITagSuggestion struct {
	Label               string `json:"label"`
	Confidence          string `json:"confidence"`
	MatchType           string `json:"match_type"`
	MatchedExistingName string `json:"matched_existing_name"`
	Reasoning           string `json:"reasoning"`
}

type AITaggingRequest struct {
	Video        models.Video
	ExistingTags []models.Tag
	Evidence     AITaggingEvidence
	BatchIndex   int
	BatchCount   int
	TotalFrames  int
}

type AITagAgentDecision struct {
	Action              string `json:"action"`
	RequestedFrameCount int    `json:"requested_frame_count"`
	Reasoning           string `json:"reasoning"`
}

type AITagAgentObservation struct {
	Tool           string `json:"tool"`
	Status         string `json:"status"`
	Code           string `json:"code"`
	RequestedCount int    `json:"requested_count,omitempty"`
	ActualCount    int    `json:"actual_count,omitempty"`
}

type AITagAgentDecisionRequest struct {
	Video                models.Video
	ExistingTags         []models.Tag
	Evidence             AITaggingEvidence
	Observations         []AITagAgentObservation
	Round                int
	MaxRounds            int
	RemainingExtraFrames int
	TranscriptUsed       bool
	SameSourceUsed       bool
}

type TemporaryTranscriptEvidence struct {
	Text         string
	DetectedLang string
	Engine       string
}

type TemporaryTranscriptProvider interface {
	GenerateTemporaryTranscript(ctx context.Context, video models.Video, charLimit int, recognitionConfig SubtitleRecognitionConfig) (TemporaryTranscriptEvidence, error)
}

type AISameSourceCandidate struct {
	Video              models.Video
	LocalScore         int
	ReferenceFrames    []AITaggingFrame
	CandidateFrames    []AITaggingFrame
	ContentFingerprint string
}

type AISameSourceComparisonRequest struct {
	Video     models.Video
	Candidate models.Video
	Left      []AITaggingFrame
	Right     []AITaggingFrame
}

type AISameSourceComparison struct {
	SameSource bool   `json:"same_source"`
	Confidence string `json:"confidence"`
	Reasoning  string `json:"reasoning"`
}

type AISameSourceEvidence struct {
	Relations []VideoSameSourceReviewItem
	Summary   string
}

type AISameSourceProvider interface {
	FindSameSource(ctx context.Context, video models.Video, client AITaggingAIClient) (AISameSourceEvidence, error)
}

type VideoSameSourceReviewItem struct {
	ID            uint          `json:"id"`
	VideoAID      uint          `json:"video_a_id"`
	VideoA        *models.Video `json:"video_a,omitempty"`
	VideoADeleted bool          `json:"video_a_deleted"`
	VideoBID      uint          `json:"video_b_id"`
	VideoB        *models.Video `json:"video_b,omitempty"`
	VideoBDeleted bool          `json:"video_b_deleted"`
	Status        string        `json:"status"`
	Confidence    string        `json:"confidence"`
	Reasoning     string        `json:"reasoning"`
	IsUnread      bool          `json:"is_unread"`
	CreatedAt     string        `json:"created_at"`
	UpdatedAt     string        `json:"updated_at"`
}

type AITagLibraryInput struct {
	ID             uint   `json:"id"`
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	Color          string `json:"color"`
	ReviewRequired bool   `json:"review_required"`
	IsActive       bool   `json:"is_active"`
}
