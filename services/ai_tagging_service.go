package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const aiTaggingWorkerInterval = 5 * time.Minute

type AITaggingService struct {
	configProvider AITaggingConfigProvider
	clientFactory  func(AITaggingConfig) AITaggingAIClient
	extractor      *AITaggingExtractor
	now            func() time.Time
	workerMu       sync.Mutex
	workerCancel   context.CancelFunc
}

func NewAITaggingService() *AITaggingService {
	return &AITaggingService{
		configProvider: EnvAITaggingConfigProvider{},
		clientFactory:  NewOpenAICompatibleAITaggingClient,
		extractor:      NewAITaggingExtractor(),
		now:            time.Now,
	}
}

func (s *AITaggingService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if s.workerCancel != nil {
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.workerCancel = cancel
	go s.workerLoop(workerCtx)
}

func (s *AITaggingService) Stop() {
	if s == nil {
		return
	}
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if s.workerCancel != nil {
		s.workerCancel()
		s.workerCancel = nil
	}
}

func (s *AITaggingService) workerLoop(ctx context.Context) {
	s.runWorkerOnce(ctx)
	ticker := time.NewTicker(aiTaggingWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runWorkerOnce(ctx)
		}
	}
}

func (s *AITaggingService) runWorkerOnce(ctx context.Context) {
	config, err := s.configProvider.Load()
	if err != nil {
		log.Printf("[AITagging] config unavailable; background worker idle")
		return
	}
	batchSize := config.StartupBatchSize
	if batchSize <= 0 {
		batchSize = defaultAITaggingStartupBatchSize
	}
	videos, err := s.findUntaggedVideos(batchSize)
	if err != nil {
		log.Printf("[AITagging] find untagged videos failed: %v", err)
		return
	}
	for _, video := range videos {
		if ctx.Err() != nil {
			return
		}
		if err := s.processVideoWithConfig(ctx, video, config); err != nil {
			log.Printf("[AITagging] process video id=%d failed: %v", video.ID, err)
		}
	}
}

func (s *AITaggingService) ProcessVideo(ctx context.Context, videoID uint) error {
	var video models.Video
	if err := database.DB.Preload("Tags").First(&video, videoID).Error; err != nil {
		return err
	}
	config, err := s.configProvider.Load()
	if err != nil {
		return s.markState(video.ID, models.AITaggingStateStatusSkipped, "config_unavailable", "", err.Error())
	}
	return s.processVideoWithConfig(ctx, video, config)
}

func (s *AITaggingService) processVideoWithConfig(ctx context.Context, video models.Video, config AITaggingConfig) error {
	if len(video.Tags) > 0 {
		return s.markState(video.ID, models.AITaggingStateStatusSkipped, "already_tagged", "", "")
	}
	existingTags, err := s.loadActiveTags()
	if err != nil {
		return err
	}
	evidence := s.extractor.Collect(ctx, video, config)
	fingerprint := buildEvidenceFingerprint(video, existingTags, evidence)
	if skip, err := s.shouldSkipForCurrentFingerprint(video.ID, fingerprint); err != nil {
		return err
	} else if skip {
		return nil
	}
	if err := s.setProcessing(video.ID, fingerprint); err != nil {
		return err
	}
	client := s.clientFactory(config)
	suggestions, err := client.AnalyzeTags(ctx, AITaggingRequest{
		Video:        video,
		ExistingTags: existingTags,
		Evidence:     evidence,
	})
	if err != nil {
		return s.markState(video.ID, models.AITaggingStateStatusFailed, "", fingerprint, err.Error())
	}
	created, err := s.persistSuggestions(video, existingTags, evidence, suggestions)
	if err != nil {
		return s.markState(video.ID, models.AITaggingStateStatusFailed, "", fingerprint, err.Error())
	}
	if created == 0 {
		return s.markState(video.ID, models.AITaggingStateStatusSkipped, "no_high_or_medium_confidence", fingerprint, "")
	}
	return s.markState(video.ID, models.AITaggingStateStatusCompleted, "", fingerprint, "")
}

func (s *AITaggingService) findUntaggedVideos(limit int) ([]models.Video, error) {
	var videos []models.Video
	err := database.DB.Model(&models.Video{}).
		Preload("Tags").
		Where("is_stale = ?", false).
		Where("NOT EXISTS (SELECT 1 FROM video_tags WHERE video_tags.video_id = videos.id)").
		Order("id").
		Limit(limit).
		Find(&videos).Error
	return videos, err
}

func (s *AITaggingService) loadActiveTags() ([]models.Tag, error) {
	var tags []models.Tag
	if err := database.DB.Order("id").Find(&tags).Error; err != nil {
		return nil, err
	}
	return tags, nil
}

func (s *AITaggingService) shouldSkipForCurrentFingerprint(videoID uint, fingerprint string) (bool, error) {
	var state models.AITaggingState
	err := database.DB.Where("video_id = ?", videoID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state.EvidenceFingerprint != fingerprint {
		if err := database.DB.Model(&models.AITagCandidate{}).
			Where("video_id = ? AND status = ?", videoID, models.AITagCandidateStatusPending).
			Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
			return false, err
		}
		return false, nil
	}
	if state.Status == models.AITaggingStateStatusPending || state.Status == models.AITaggingStateStatusFailed {
		return false, nil
	}
	var pendingCount int64
	if err := database.DB.Model(&models.AITagCandidate{}).
		Where("video_id = ? AND status = ?", videoID, models.AITagCandidateStatusPending).
		Count(&pendingCount).Error; err != nil {
		return false, err
	}
	return pendingCount > 0 || state.Status == models.AITaggingStateStatusCompleted || state.Status == models.AITaggingStateStatusSkipped, nil
}

func (s *AITaggingService) setProcessing(videoID uint, fingerprint string) error {
	now := s.now()
	return database.DB.Transaction(func(tx *gorm.DB) error {
		var state models.AITaggingState
		err := tx.Where("video_id = ?", videoID).First(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			state = models.AITaggingState{
				VideoID:             videoID,
				Status:              models.AITaggingStateStatusProcessing,
				EvidenceFingerprint: fingerprint,
				AttemptCount:        1,
				LastProcessedAt:     &now,
			}
			return tx.Create(&state).Error
		}
		if err != nil {
			return err
		}
		return tx.Model(&state).Updates(map[string]interface{}{
			"status":               models.AITaggingStateStatusProcessing,
			"skip_reason":          "",
			"evidence_fingerprint": fingerprint,
			"attempt_count":        state.AttemptCount + 1,
			"last_error":           "",
			"last_processed_at":    &now,
		}).Error
	})
}

func (s *AITaggingService) markState(videoID uint, status, skipReason, fingerprint, lastError string) error {
	now := s.now()
	updates := map[string]interface{}{
		"status":            status,
		"skip_reason":       skipReason,
		"last_error":        lastError,
		"last_processed_at": &now,
	}
	if fingerprint != "" {
		updates["evidence_fingerprint"] = fingerprint
	}
	var state models.AITaggingState
	err := database.DB.Where("video_id = ?", videoID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		state = models.AITaggingState{
			VideoID:             videoID,
			Status:              status,
			SkipReason:          skipReason,
			EvidenceFingerprint: fingerprint,
			LastError:           lastError,
			LastProcessedAt:     &now,
		}
		return database.DB.Create(&state).Error
	}
	if err != nil {
		return err
	}
	return database.DB.Model(&state).Updates(updates).Error
}

func (s *AITaggingService) persistSuggestions(video models.Video, tags []models.Tag, evidence AITaggingEvidence, suggestions []AITagSuggestion) (int, error) {
	tagsByName := make(map[string]models.Tag, len(tags))
	for _, tag := range tags {
		tagsByName[normalizeAITagName(tag.Name)] = tag
	}
	created := 0
	for _, suggestion := range suggestions {
		confidence := normalizeAIConfidence(suggestion.Confidence)
		if confidence == "" || confidence == models.AITagConfidenceLow {
			continue
		}
		label := strings.TrimSpace(suggestion.Label)
		normalized := normalizeAITagName(label)
		if normalized == "" {
			continue
		}
		var matchedTagID *uint
		if matched, ok := tagsByName[normalizeAITagName(suggestion.MatchedExistingName)]; ok {
			id := matched.ID
			matchedTagID = &id
		} else if matched, ok := tagsByName[normalized]; ok {
			id := matched.ID
			matchedTagID = &id
		}
		if matchedTagID == nil && confidence != models.AITagConfidenceHigh {
			continue
		}
		candidate := models.AITagCandidate{
			VideoID:        video.ID,
			SuggestedName:  label,
			NormalizedName: normalized,
			MatchedTagID:   matchedTagID,
			Confidence:     confidence,
			Reasoning:      strings.TrimSpace(suggestion.Reasoning),
			SourceSummary:  evidence.SummaryJSON(),
			Status:         models.AITagCandidateStatusPending,
		}
		var existing models.AITagCandidate
		err := database.DB.Where("video_id = ? AND normalized_name = ? AND status = ?", candidate.VideoID, candidate.NormalizedName, models.AITagCandidateStatusPending).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := database.DB.Create(&candidate).Error; err != nil {
				return created, err
			}
			created++
			continue
		}
		if err != nil {
			return created, err
		}
		if err := database.DB.Model(&existing).Updates(map[string]interface{}{
			"suggested_name": candidate.SuggestedName,
			"matched_tag_id": candidate.MatchedTagID,
			"confidence":     candidate.Confidence,
			"reasoning":      candidate.Reasoning,
			"source_summary": candidate.SourceSummary,
		}).Error; err != nil {
			return created, err
		}
	}
	return created, nil
}

func (s *AITaggingService) ListCandidates(videoID uint, confidence string, status string) ([]AITaggingReviewItem, error) {
	query := database.DB.Model(&models.AITagCandidate{}).Preload("Video").Preload("Video.Tags").Preload("MatchedTag")
	if videoID > 0 {
		query = query.Where("video_id = ?", videoID)
	}
	if confidence = normalizeAIConfidence(confidence); confidence != "" {
		query = query.Where("confidence = ?", confidence)
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = models.AITagCandidateStatusPending
	}
	query = query.Where("status = ?", status)
	var candidates []models.AITagCandidate
	if err := query.Order("created_at desc, id desc").Find(&candidates).Error; err != nil {
		return nil, err
	}
	items := make([]AITaggingReviewItem, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, aiTagCandidateReviewItem(candidate))
	}
	return items, nil
}

func (s *AITaggingService) ApproveCandidate(candidateID uint) (*AITaggingReviewItem, error) {
	var approved models.AITagCandidate
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		var candidate models.AITagCandidate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", candidateID).First(&candidate).Error; err != nil {
			return err
		}
		if candidate.Status != models.AITagCandidateStatusPending {
			return fmt.Errorf("candidate is not pending")
		}
		if candidate.Confidence != models.AITagConfidenceHigh && candidate.Confidence != models.AITagConfidenceMedium {
			return fmt.Errorf("candidate confidence is not approvable")
		}
		hasManualTags, err := s.hasManualOfficialTagsInTx(tx, candidate.VideoID)
		if err != nil {
			return err
		}
		if hasManualTags {
			now := s.now()
			if err := tx.Model(&models.AITagCandidate{}).
				Where("video_id = ? AND status = ?", candidate.VideoID, models.AITagCandidateStatusPending).
				Updates(map[string]interface{}{
					"status":      models.AITagCandidateStatusSuperseded,
					"rejected_at": &now,
				}).Error; err != nil {
				return err
			}
			approved = candidate
			approved.Status = models.AITagCandidateStatusSuperseded
			approved.RejectedAt = &now
			return nil
		}
		tagID, err := s.resolveOfficialTagInTx(tx, candidate)
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, candidate.VideoID, tagID).Error; err != nil {
			return err
		}
		now := s.now()
		result := tx.Model(&models.AITagCandidate{}).
			Where("id = ? AND status = ?", candidate.ID, models.AITagCandidateStatusPending).
			Updates(map[string]interface{}{
				"status":      models.AITagCandidateStatusApproved,
				"approved_at": &now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("candidate is no longer pending")
		}
		approvalRecord := models.AITagApprovalRecord{
			VideoID:     candidate.VideoID,
			TagID:       tagID,
			CandidateID: candidate.ID,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&approvalRecord).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.AITagCandidate{}).
			Where("video_id = ? AND normalized_name = ? AND id <> ? AND status = ?", candidate.VideoID, candidate.NormalizedName, candidate.ID, models.AITagCandidateStatusPending).
			Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
			return err
		}
		approved = candidate
		approved.Status = models.AITagCandidateStatusApproved
		approved.ApprovedAt = &now
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := database.DB.Preload("Video").Preload("Video.Tags").Preload("MatchedTag").First(&approved, candidateID).Error; err != nil {
		return nil, err
	}
	item := aiTagCandidateReviewItem(approved)
	return &item, nil
}

func (s *AITaggingService) hasManualOfficialTagsInTx(tx *gorm.DB, videoID uint) (bool, error) {
	var officialCount int64
	if err := tx.Table("video_tags").Where("video_id = ?", videoID).Count(&officialCount).Error; err != nil {
		return false, err
	}
	if officialCount == 0 {
		return false, nil
	}
	var aiApprovedCount int64
	if err := tx.Table("video_tags AS vt").
		Joins("INNER JOIN ai_tag_approval_records AS ar ON ar.video_id = vt.video_id AND ar.tag_id = vt.tag_id").
		Where("vt.video_id = ?", videoID).
		Count(&aiApprovedCount).Error; err != nil {
		return false, err
	}
	return officialCount > aiApprovedCount, nil
}

func (s *AITaggingService) resolveOfficialTagInTx(tx *gorm.DB, candidate models.AITagCandidate) (uint, error) {
	if candidate.MatchedTagID != nil {
		var tag models.Tag
		if err := tx.First(&tag, *candidate.MatchedTagID).Error; err != nil {
			return 0, err
		}
		return tag.ID, nil
	}
	name := strings.TrimSpace(candidate.SuggestedName)
	if name == "" {
		return 0, fmt.Errorf("empty suggested tag name")
	}
	var existing models.Tag
	if err := tx.Where("name = ?", name).First(&existing).Error; err == nil {
		return existing.ID, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	var deleted models.Tag
	if err := tx.Unscoped().Where("name = ? AND deleted_at IS NOT NULL", name).First(&deleted).Error; err == nil {
		deleted.DeletedAt.Clear()
		if err := tx.Unscoped().Save(&deleted).Error; err != nil {
			return 0, err
		}
		return deleted.ID, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	var count int64
	if err := tx.Model(&models.Tag{}).Count(&count).Error; err != nil {
		return 0, err
	}
	tag := models.Tag{Name: name, Color: tagColorPalette[int(count)%len(tagColorPalette)]}
	if err := tx.Create(&tag).Error; err != nil {
		return 0, err
	}
	return tag.ID, nil
}

func (s *AITaggingService) RejectCandidate(candidateID uint) error {
	now := s.now()
	result := database.DB.Model(&models.AITagCandidate{}).
		Where("id = ? AND status = ?", candidateID, models.AITagCandidateStatusPending).
		Updates(map[string]interface{}{
			"status":      models.AITagCandidateStatusRejected,
			"rejected_at": &now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("candidate is not pending")
	}
	return nil
}

func (s *AITaggingService) RetryVideo(videoID uint) error {
	return database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.AITagCandidate{}).
			Where("video_id = ? AND status = ?", videoID, models.AITagCandidateStatusPending).
			Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
			return err
		}
		var state models.AITaggingState
		err := tx.Where("video_id = ?", videoID).First(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			state = models.AITaggingState{VideoID: videoID, Status: models.AITaggingStateStatusPending}
			return tx.Create(&state).Error
		}
		if err != nil {
			return err
		}
		return tx.Model(&state).Updates(map[string]interface{}{
			"status":               models.AITaggingStateStatusPending,
			"skip_reason":          "",
			"evidence_fingerprint": "",
			"last_error":           "",
		}).Error
	})
}

func (s *AITaggingService) StatusSummary() (*AITaggingStatusSummary, error) {
	_, configErr := s.configProvider.Load()
	summary := &AITaggingStatusSummary{ConfigAvailable: configErr == nil}
	if err := database.DB.Model(&models.AITagCandidate{}).Where("status = ?", models.AITagCandidateStatusPending).Count(&summary.Pending).Error; err != nil {
		return nil, err
	}
	countState := func(status string, target *int64) error {
		return database.DB.Model(&models.AITaggingState{}).Where("status = ?", status).Count(target).Error
	}
	if err := countState(models.AITaggingStateStatusProcessing, &summary.Processing); err != nil {
		return nil, err
	}
	if err := countState(models.AITaggingStateStatusCompleted, &summary.Completed); err != nil {
		return nil, err
	}
	if err := countState(models.AITaggingStateStatusSkipped, &summary.Skipped); err != nil {
		return nil, err
	}
	if err := countState(models.AITaggingStateStatusFailed, &summary.Failed); err != nil {
		return nil, err
	}
	return summary, nil
}

func normalizeAIConfidence(confidence string) string {
	switch strings.ToLower(strings.TrimSpace(confidence)) {
	case models.AITagConfidenceHigh:
		return models.AITagConfidenceHigh
	case models.AITagConfidenceMedium:
		return models.AITagConfidenceMedium
	case models.AITagConfidenceLow:
		return models.AITagConfidenceLow
	default:
		return ""
	}
}

func normalizeAITagName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func aiTagCandidateReviewItem(candidate models.AITagCandidate) AITaggingReviewItem {
	var video *models.Video
	if candidate.Video.ID != 0 {
		v := candidate.Video
		video = &v
	}
	return AITaggingReviewItem{
		ID:             candidate.ID,
		VideoID:        candidate.VideoID,
		Video:          video,
		SuggestedName:  candidate.SuggestedName,
		NormalizedName: candidate.NormalizedName,
		MatchedTagID:   candidate.MatchedTagID,
		MatchedTag:     candidate.MatchedTag,
		Confidence:     candidate.Confidence,
		Reasoning:      candidate.Reasoning,
		SourceSummary:  candidate.SourceSummary,
		Status:         candidate.Status,
		CreatedAt:      candidate.CreatedAt.Format(time.RFC3339),
		UpdatedAt:      candidate.UpdatedAt.Format(time.RFC3339),
	}
}
