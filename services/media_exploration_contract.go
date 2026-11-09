package services

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const explorationSceneLimit = 1024
const explorationBatchLimit = 256

var (
	ErrExplorationInvalid     = errors.New("invalid exploration request")
	ErrExplorationConflict    = errors.New("exploration revision changed")
	ErrExplorationScene       = errors.New("exploration scene changed")
	ErrExplorationDatabase    = errors.New("exploration database changed")
	ErrExplorationUnavailable = errors.New("exploration media unavailable")
)

// ExplorationView is a camera projection; changing it never changes media order.
type ExplorationView struct {
	Mode       string  `json:"mode"`
	Scale      float64 `json:"scale"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	SelectedID uint    `json:"selected_id"`
	GalleryID  uint    `json:"gallery_id"`
	GalleryIDs []uint  `json:"gallery_entry_ids"`
}

// ExplorationContext binds requests to one current database and scene.
type ExplorationContext struct {
	DatabaseEpoch string `json:"database_epoch"`
	Scope         string `json:"scope"`
	SceneID       string `json:"scene_id"`
}

type ExplorationRequest struct {
	ExplorationContext
	RoundID          string           `json:"round_id"`
	ExpectedRevision uint64           `json:"expected_revision"`
	RequestID        string           `json:"request_id"`
	Action           string           `json:"action"`
	EntryIDs         []uint           `json:"entry_ids,omitempty"`
	View             *ExplorationView `json:"view,omitempty"`
	EntryID          uint             `json:"entry_id,omitempty"`
	SourceVersion    string           `json:"source_version,omitempty"`
}

type ExplorationItemsRequest struct {
	ExplorationContext
	EntryIDs []uint `json:"entry_ids"`
}

type ExplorationSample struct {
	StartMS       int64  `json:"start_ms"`
	EndMS         int64  `json:"end_ms"`
	SourceVersion string `json:"source_version"`
}

type ExplorationSceneEntry struct {
	EntryID   uint                `json:"entry_id"`
	Slot      int                 `json:"slot"`
	Kind      string              `json:"kind"`
	MediaID   uint                `json:"media_id"`
	Presented bool                `json:"presented"`
	Samples   []ExplorationSample `json:"samples"`
}

type ExplorationReceipt struct {
	RequestID string `json:"request_id"`
	Revision  uint64 `json:"revision"`
	SceneID   string `json:"scene_id"`
}

type ExplorationSnapshot struct {
	ExplorationContext
	RoundID        string                  `json:"round_id"`
	Revision       uint64                  `json:"revision"`
	View           ExplorationView         `json:"view_state"`
	Status         string                  `json:"status"`
	AvailableCount int64                   `json:"available_count"`
	PresentedCount int64                   `json:"presented_count"`
	RemainingCount int64                   `json:"remaining_count"`
	Entries        []ExplorationSceneEntry `json:"entries"`
	LastReceipt    ExplorationReceipt      `json:"last_receipt"`
}

type ExplorationCard struct {
	EntryID           uint     `json:"entry_id"`
	Kind              string   `json:"kind"`
	MediaID           uint     `json:"media_id"`
	Title             string   `json:"title"`
	ThumbnailURL      string   `json:"thumbnail_url"`
	AspectRatio       *float64 `json:"aspect_ratio"`
	DurationMS        *int64   `json:"duration_ms"`
	UnavailableReason string   `json:"unavailable_reason"`
}

type ExplorationResult struct {
	Status string               `json:"status"`
	Data   *ExplorationSnapshot `json:"data"`
	Error  *ExperienceError     `json:"error"`
}

type ExplorationCards struct {
	Items []ExplorationCard `json:"items"`
}
type ExplorationCardsResult struct {
	Status string            `json:"status"`
	Data   *ExplorationCards `json:"data"`
	Error  *ExperienceError  `json:"error"`
}

func validExplorationScope(scope string) bool {
	return scope == "all" || scope == "video" || scope == "image"
}

func validateExplorationRequest(r ExplorationRequest) error {
	if !validExplorationScope(r.Scope) || r.DatabaseEpoch == "" || r.SceneID == "" || r.RoundID == "" || r.ExpectedRevision == 0 || r.ExpectedRevision >= experienceMaxSafeInteger {
		return ErrExplorationInvalid
	}
	base, suffix, ok := strings.Cut(r.RequestID, ":")
	if !ok || base != strconv.FormatUint(r.ExpectedRevision, 10) || len(suffix) != 36 {
		return ErrExplorationInvalid
	}
	if _, err := uuid.Parse(suffix); err != nil {
		return ErrExplorationInvalid
	}
	switch r.Action {
	case "present", "replace":
		if len(r.EntryIDs) == 0 || len(r.EntryIDs) > explorationBatchLimit {
			return ErrExplorationInvalid
		}
		seen := map[uint]bool{}
		for _, id := range r.EntryIDs {
			if id == 0 || id > experienceMaxSafeInteger || seen[id] {
				return ErrExplorationInvalid
			}
			seen[id] = true
		}
	case "save_view":
		v := r.View
		if v == nil || (v.Mode != "wall" && v.Mode != "gallery") || math.IsNaN(v.Scale) || math.IsInf(v.Scale, 0) || v.Scale < .2 || v.Scale > 2 || math.IsNaN(v.X) || math.IsInf(v.X, 0) || math.IsNaN(v.Y) || math.IsInf(v.Y, 0) || math.Abs(v.X) > 100000 || math.Abs(v.Y) > 100000 || v.SelectedID > experienceMaxSafeInteger || v.GalleryID > experienceMaxSafeInteger {
			return ErrExplorationInvalid
		}
		if len(v.GalleryIDs) > 24 {
			return ErrExplorationInvalid
		}
		seen := map[uint]bool{}
		for _, id := range v.GalleryIDs {
			if id == 0 || id > experienceMaxSafeInteger || seen[id] {
				return ErrExplorationInvalid
			}
			seen[id] = true
		}
	case "next", "reshuffle", "refresh", "replenish":
	case "sample":
		if r.EntryID == 0 || r.EntryID > experienceMaxSafeInteger || r.SourceVersion == "" || len(r.SourceVersion) > 256 {
			return ErrExplorationInvalid
		}
	default:
		return ErrExplorationInvalid
	}
	if r.Action != "present" && r.Action != "replace" && len(r.EntryIDs) != 0 || r.Action != "save_view" && r.View != nil || r.Action != "sample" && (r.EntryID != 0 || r.SourceVersion != "") {
		return ErrExplorationInvalid
	}
	return nil
}
