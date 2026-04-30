package services

import (
	"video-master/models"
)

type AITaggingReviewItem struct {
	ID             uint          `json:"id"`
	VideoID        uint          `json:"video_id"`
	Video          *models.Video `json:"video,omitempty"`
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
	ConfigAvailable bool  `json:"config_available"`
	Pending         int64 `json:"pending"`
	Processing      int64 `json:"processing"`
	Completed       int64 `json:"completed"`
	Skipped         int64 `json:"skipped"`
	Failed          int64 `json:"failed"`
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
}
