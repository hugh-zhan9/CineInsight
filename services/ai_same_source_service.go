package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"sort"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	sameSourceAnchorCount       = 5
	sameSourceShortlistLimit    = 200
	sameSourceAIComparisonLimit = 5
)

type AITaggingFatalError struct {
	Err error
}

func (e *AITaggingFatalError) Error() string {
	if e == nil || e.Err == nil {
		return "fatal AI tagging error"
	}
	return e.Err.Error()
}

func (e *AITaggingFatalError) Unwrap() error { return e.Err }

type AISameSourceService struct {
	extractor *AITaggingExtractor
	now       func() time.Time
}

func NewAISameSourceService(extractor *AITaggingExtractor) *AISameSourceService {
	if extractor == nil {
		extractor = NewAITaggingExtractor()
	}
	return &AISameSourceService{extractor: extractor, now: time.Now}
}

type scoredSameSourceCandidate struct {
	video       models.Video
	fingerprint models.VideoVisualFingerprint
	median      int
}

func (s *AISameSourceService) FindSameSource(ctx context.Context, video models.Video, client AITaggingAIClient) (AISameSourceEvidence, error) {
	comparisonClient, ok := client.(AITaggingSameSourceClient)
	if !ok {
		return AISameSourceEvidence{}, fmt.Errorf("AI client does not support same-source comparison")
	}
	referenceFingerprint, _, err := s.ensureFingerprint(ctx, video, false)
	if err != nil {
		return AISameSourceEvidence{}, err
	}
	referencePayload, err := decodeSameSourceFingerprint(referenceFingerprint.FrameHashesJSON)
	if err != nil {
		return AISameSourceEvidence{}, err
	}
	candidates, err := s.loadDurationCandidates(video)
	if err != nil {
		return AISameSourceEvidence{}, &AITaggingFatalError{Err: err}
	}
	scored := make([]scoredSameSourceCandidate, 0)
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return AISameSourceEvidence{}, ctx.Err()
		}
		candidateFingerprint, _, fingerprintErr := s.ensureFingerprint(ctx, candidate, false)
		if fingerprintErr != nil {
			continue
		}
		blocked, blockErr := rejectedSameSourcePairStillCurrent(video.ID, candidate.ID, referenceFingerprint.ContentFingerprint, candidateFingerprint.ContentFingerprint)
		if blockErr != nil {
			return AISameSourceEvidence{}, &AITaggingFatalError{Err: blockErr}
		}
		if blocked {
			continue
		}
		candidatePayload, decodeErr := decodeSameSourceFingerprint(candidateFingerprint.FrameHashesJSON)
		if decodeErr != nil {
			continue
		}
		median, _, match := scoreSameSourceFingerprints(referencePayload, candidatePayload)
		if match {
			scored = append(scored, scoredSameSourceCandidate{video: candidate, fingerprint: candidateFingerprint, median: median})
		}
	}
	sort.SliceStable(scored, func(left, right int) bool { return scored[left].median < scored[right].median })
	if len(scored) > sameSourceAIComparisonLimit {
		scored = scored[:sameSourceAIComparisonLimit]
	}
	if len(scored) == 0 {
		return AISameSourceEvidence{Summary: "未发现本地同源候选"}, nil
	}
	_, referenceFrames, err := s.ensureFingerprint(ctx, video, true)
	if err != nil {
		return AISameSourceEvidence{}, err
	}
	result := AISameSourceEvidence{Relations: make([]VideoSameSourceReviewItem, 0)}
	evidenceDescriptions := make([]string, 0)
	for _, candidate := range scored {
		_, candidateFrames, frameErr := s.ensureFingerprint(ctx, candidate.video, true)
		if frameErr != nil {
			continue
		}
		comparison, compareErr := comparisonClient.CompareSameSource(ctx, AISameSourceComparisonRequest{
			Video: video, Candidate: candidate.video, Left: referenceFrames, Right: candidateFrames,
		})
		if compareErr != nil {
			return result, &AITaggingFatalError{Err: compareErr}
		}
		if !comparison.SameSource || comparison.Confidence != models.AITagConfidenceHigh {
			continue
		}
		relation, persistErr := s.persistDetectedRelation(video, candidate.video, referenceFingerprint, candidate.fingerprint, comparison, aiRunAttributionFromContext(ctx))
		if persistErr != nil {
			return result, &AITaggingFatalError{Err: persistErr}
		}
		if relation.Status != models.VideoSameSourceStatusDetected {
			continue
		}
		item := sameSourceReviewItem(relation)
		result.Relations = append(result.Relations, item)
		description := candidate.video.Name
		if tagNames := nonAutomaticTagNames(candidate.video.Tags); len(tagNames) > 0 {
			description += "（已有标签仅供证据参考：" + strings.Join(tagNames, "、") + "）"
		}
		evidenceDescriptions = append(evidenceDescriptions, description)
	}
	if len(result.Relations) == 0 {
		result.Summary = "同源候选经复核后证据不足"
		return result, nil
	}
	result.Summary = fmt.Sprintf("发现 %d 个高置信同源视频：%s。已有标签只能作为证据，不能直接复制或写入正式标签。", len(result.Relations), strings.Join(evidenceDescriptions, "、"))
	return result, nil
}

func (s *AISameSourceService) loadDurationCandidates(video models.Video) ([]models.Video, error) {
	if video.Duration <= 0 {
		return nil, nil
	}
	tolerance := video.Duration * 0.02
	if tolerance < 2 {
		tolerance = 2
	}
	var videos []models.Video
	err := database.DB.Preload("Tags").Where("id <> ? AND is_stale = ? AND duration BETWEEN ? AND ?", video.ID, false, video.Duration-tolerance, video.Duration+tolerance).
		Order(clause.Expr{SQL: "ABS(duration - ?) ASC", Vars: []interface{}{video.Duration}}).
		Order("id ASC").
		Limit(sameSourceShortlistLimit).
		Find(&videos).Error
	return videos, err
}

func nonAutomaticTagNames(tags []models.Tag) []string {
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag.AutomaticKind == "" {
			names = append(names, tag.Name)
		}
	}
	return names
}

func (s *AISameSourceService) ensureFingerprint(ctx context.Context, video models.Video, includeFrames bool) (models.VideoVisualFingerprint, []AITaggingFrame, error) {
	contentFingerprint, err := sampledFileContentFingerprint(video.Path)
	if err != nil {
		return models.VideoVisualFingerprint{}, nil, err
	}
	var cached models.VideoVisualFingerprint
	cacheErr := database.DB.Where("video_id = ?", video.ID).First(&cached).Error
	cacheValid := cacheErr == nil && cached.ContentFingerprint == contentFingerprint && cached.AlgorithmVersion == sameSourceFingerprintVersion
	if cacheErr != nil && !errors.Is(cacheErr, gorm.ErrRecordNotFound) {
		return cached, nil, &AITaggingFatalError{Err: cacheErr}
	}
	if cacheValid && !includeFrames {
		return cached, nil, nil
	}
	positions := planAITaggingFramePositions(video.Duration, sameSourceAnchorCount)
	frames, warnings := sampleAITaggingFrames(ctx, video.Path, positions, 1)
	if len(frames) != sameSourceAnchorCount {
		return cached, frames, fmt.Errorf("same-source frame sampling got %d/%d frames: %s", len(frames), sameSourceAnchorCount, strings.Join(warnings, "; "))
	}
	if cacheValid {
		return cached, frames, nil
	}
	payload := sameSourceFingerprintPayload{Positions: make([]float64, 0, len(frames)), Hashes: make([][]uint64, 0, len(frames))}
	for _, frame := range frames {
		imageValue, decodeErr := decodeAITaggingFrame(frame)
		if decodeErr != nil {
			return cached, frames, decodeErr
		}
		payload.Positions = append(payload.Positions, frame.Position)
		payload.Hashes = append(payload.Hashes, sameSourceFrameHashes(imageValue))
	}
	encoded, err := encodeSameSourceFingerprint(payload)
	if err != nil {
		return cached, frames, err
	}
	fingerprint := models.VideoVisualFingerprint{
		VideoID: video.ID, ContentFingerprint: contentFingerprint, AlgorithmVersion: sameSourceFingerprintVersion,
		Duration: video.Duration, FrameHashesJSON: encoded, SampleCount: len(frames),
	}
	if err := database.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "video_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"content_fingerprint", "algorithm_version", "duration", "frame_hashes_json", "sample_count", "updated_at"}),
	}).Create(&fingerprint).Error; err != nil {
		return fingerprint, frames, &AITaggingFatalError{Err: err}
	}
	if err := database.DB.Where("video_id = ?", video.ID).First(&fingerprint).Error; err != nil {
		return fingerprint, frames, &AITaggingFatalError{Err: err}
	}
	return fingerprint, frames, nil
}

func decodeAITaggingFrame(frame AITaggingFrame) (image.Image, error) {
	separator := strings.IndexByte(frame.DataURL, ',')
	if separator < 0 {
		return nil, fmt.Errorf("invalid frame data URL")
	}
	data, err := base64.StdEncoding.DecodeString(frame.DataURL[separator+1:])
	if err != nil {
		return nil, fmt.Errorf("decode frame data URL: %w", err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode frame image: %w", err)
	}
	return decoded, nil
}

func rejectedSameSourcePairStillCurrent(leftID, rightID uint, leftFingerprint, rightFingerprint string) (bool, error) {
	videoAID, videoBID, err := normalizedVideoPair(leftID, rightID)
	if err != nil {
		return false, err
	}
	if leftID != videoAID {
		leftFingerprint, rightFingerprint = rightFingerprint, leftFingerprint
	}
	var relation models.VideoSameSourceRelation
	err = database.DB.Where("video_a_id = ? AND video_b_id = ?", videoAID, videoBID).First(&relation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return relation.Status == models.VideoSameSourceStatusRejected &&
		relation.VideoAFingerprint == leftFingerprint && relation.VideoBFingerprint == rightFingerprint, nil
}

func (s *AISameSourceService) persistDetectedRelation(left, right models.Video, leftFingerprint, rightFingerprint models.VideoVisualFingerprint, comparison AISameSourceComparison, attributions ...aiRunAttribution) (models.VideoSameSourceRelation, error) {
	videoAID, videoBID, err := normalizedVideoPair(left.ID, right.ID)
	if err != nil {
		return models.VideoSameSourceRelation{}, err
	}
	if left.ID != videoAID {
		leftFingerprint, rightFingerprint = rightFingerprint, leftFingerprint
	}
	attribution := aiRunAttribution{}
	if len(attributions) > 0 {
		attribution = attributions[0]
	}
	var relation models.VideoSameSourceRelation
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		createEvaluation := false
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("video_a_id = ? AND video_b_id = ?", videoAID, videoBID).
			First(&relation).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			relation = models.VideoSameSourceRelation{
				VideoAID: videoAID, VideoBID: videoBID,
				VideoAFingerprint: leftFingerprint.ContentFingerprint, VideoBFingerprint: rightFingerprint.ContentFingerprint,
				Status: models.VideoSameSourceStatusDetected, Confidence: comparison.Confidence, Reasoning: comparison.Reasoning,
				DetectionVersion: sameSourceFingerprintVersion, IsUnread: true,
			}
			if err := tx.Create(&relation).Error; err != nil {
				return err
			}
			createEvaluation = true
		} else if findErr != nil {
			return findErr
		} else {
			fingerprintsChanged := relation.VideoAFingerprint != leftFingerprint.ContentFingerprint || relation.VideoBFingerprint != rightFingerprint.ContentFingerprint
			if relation.Status == models.VideoSameSourceStatusRejected && !fingerprintsChanged {
				return nil
			}
			// This exact fingerprint pair may already have a sample, for example after a
			// file was re-encoded and then restored. Reuse it instead of inserting a
			// duplicate, and keep honouring a rejection recorded against that content.
			var known models.AISameSourceEvaluation
			knownErr := tx.Where("relation_id = ? AND left_fingerprint = ? AND right_fingerprint = ?",
				relation.ID, leftFingerprint.ContentFingerprint, rightFingerprint.ContentFingerprint).First(&known).Error
			if knownErr != nil && !errors.Is(knownErr, gorm.ErrRecordNotFound) {
				return knownErr
			}
			if knownErr == nil {
				if known.Status == models.VideoSameSourceStatusRejected {
					return nil
				}
				if err := tx.Model(&relation).Updates(map[string]interface{}{
					"video_a_fingerprint":   leftFingerprint.ContentFingerprint,
					"video_b_fingerprint":   rightFingerprint.ContentFingerprint,
					"status":                models.VideoSameSourceStatusDetected,
					"confidence":            comparison.Confidence,
					"reasoning":             comparison.Reasoning,
					"detection_version":     sameSourceFingerprintVersion,
					"rejected_at":           nil,
					"current_evaluation_id": known.ID,
				}).Error; err != nil {
					return err
				}
				relation.Status = models.VideoSameSourceStatusDetected
				relation.CurrentEvaluationID = &known.ID
				return nil
			}
			updates := map[string]interface{}{
				"video_a_fingerprint": leftFingerprint.ContentFingerprint, "video_b_fingerprint": rightFingerprint.ContentFingerprint,
				"status": models.VideoSameSourceStatusDetected, "confidence": comparison.Confidence, "reasoning": comparison.Reasoning,
				"detection_version": sameSourceFingerprintVersion, "rejected_at": nil,
			}
			if fingerprintsChanged || relation.Status != models.VideoSameSourceStatusDetected {
				updates["is_unread"] = true
			}
			if err := tx.Model(&relation).Updates(updates).Error; err != nil {
				return err
			}
			relation.Status = models.VideoSameSourceStatusDetected
			createEvaluation = fingerprintsChanged || relation.CurrentEvaluationID == nil
		}
		if !createEvaluation {
			return nil
		}
		var runID *uint
		if attribution.RunID != 0 {
			runID = &attribution.RunID
		}
		now := s.now()
		evaluation := models.AISameSourceEvaluation{
			RelationID: relation.ID, LeftVideoID: videoAID, RightVideoID: videoBID, RunID: runID,
			LeftFingerprint: leftFingerprint.ContentFingerprint, RightFingerprint: rightFingerprint.ContentFingerprint,
			Status: models.VideoSameSourceStatusDetected, Confidence: comparison.Confidence,
			ModelIdentifier:         sanitizeAIAttribution(attribution.ModelIdentifier),
			ComparisonPromptVersion: sanitizeAIAttribution(attribution.ComparisonPromptVersion),
			DetectionVersion:        sameSourceFingerprintVersion, DetectedAt: now,
		}
		if err := tx.Create(&evaluation).Error; err != nil {
			return err
		}
		if err := tx.Model(&relation).Update("current_evaluation_id", evaluation.ID).Error; err != nil {
			return err
		}
		relation.CurrentEvaluationID = &evaluation.ID
		return nil
	})
	if err != nil {
		return relation, err
	}
	if err := database.DB.Preload("VideoA").Preload("VideoB").First(&relation, relation.ID).Error; err != nil {
		return relation, err
	}
	return relation, nil
}

func (s *AISameSourceService) ListRelations(status string, unreadOnly bool) ([]VideoSameSourceReviewItem, error) {
	query := database.DB.
		Preload("VideoA", func(db *gorm.DB) *gorm.DB { return db.Unscoped() }).
		Preload("VideoB", func(db *gorm.DB) *gorm.DB { return db.Unscoped() })
	if strings.TrimSpace(status) == "" {
		status = models.VideoSameSourceStatusDetected
	}
	query = query.Where("status = ?", status)
	if unreadOnly {
		query = query.Where("is_unread = ?", true)
	}
	var relations []models.VideoSameSourceRelation
	if err := query.Order("video_same_source_relations.updated_at DESC, video_same_source_relations.id DESC").Limit(sameSourceShortlistLimit).Find(&relations).Error; err != nil {
		return nil, err
	}
	items := make([]VideoSameSourceReviewItem, 0, len(relations))
	for _, relation := range relations {
		items = append(items, sameSourceReviewItem(relation))
	}
	return items, nil
}

func (s *AISameSourceService) MarkRelationRead(relationID uint) error {
	result := database.DB.Model(&models.VideoSameSourceRelation{}).
		Where("id = ? AND status = ?", relationID, models.VideoSameSourceStatusDetected).
		Update("is_unread", false)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		var relation models.VideoSameSourceRelation
		if err := database.DB.Select("id").Where("id = ? AND status = ? AND is_unread = ?", relationID, models.VideoSameSourceStatusDetected, false).First(&relation).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *AISameSourceService) RejectRelation(relationID uint) error {
	now := s.now()
	return database.DB.Transaction(func(tx *gorm.DB) error {
		var relation models.VideoSameSourceRelation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&relation, relationID).Error; err != nil {
			return err
		}
		if relation.Status == models.VideoSameSourceStatusRejected {
			return nil
		}
		if relation.Status != models.VideoSameSourceStatusDetected {
			return errors.New("same-source relation is not detected")
		}
		if err := tx.Model(&relation).Updates(map[string]interface{}{
			"status": models.VideoSameSourceStatusRejected, "is_unread": false, "rejected_at": &now,
		}).Error; err != nil {
			return err
		}
		if relation.CurrentEvaluationID != nil {
			if err := tx.Model(&models.AISameSourceEvaluation{}).
				Where("id = ? AND status = ?", *relation.CurrentEvaluationID, models.VideoSameSourceStatusDetected).
				Updates(map[string]interface{}{"status": models.VideoSameSourceStatusRejected, "rejected_at": &now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *AISameSourceService) UnreadCount() (int64, error) {
	var count int64
	err := database.DB.Model(&models.VideoSameSourceRelation{}).
		Joins("INNER JOIN videos AS same_source_video_a ON same_source_video_a.id = video_same_source_relations.video_a_id AND same_source_video_a.deleted_at IS NULL").
		Joins("INNER JOIN videos AS same_source_video_b ON same_source_video_b.id = video_same_source_relations.video_b_id AND same_source_video_b.deleted_at IS NULL").
		Where("status = ? AND is_unread = ?", models.VideoSameSourceStatusDetected, true).
		Count(&count).Error
	return count, err
}

func sameSourceReviewItem(relation models.VideoSameSourceRelation) VideoSameSourceReviewItem {
	item := VideoSameSourceReviewItem{
		ID: relation.ID, VideoAID: relation.VideoAID, VideoBID: relation.VideoBID,
		Status: relation.Status, Confidence: relation.Confidence, Reasoning: relation.Reasoning, IsUnread: relation.IsUnread,
		CreatedAt: relation.CreatedAt.Format(time.RFC3339), UpdatedAt: relation.UpdatedAt.Format(time.RFC3339),
	}
	if relation.VideoA.ID != 0 {
		video := relation.VideoA
		item.VideoA = &video
		item.VideoADeleted = relation.VideoA.DeletedAt.IsValid()
	}
	if relation.VideoB.ID != 0 {
		video := relation.VideoB
		item.VideoB = &video
		item.VideoBDeleted = relation.VideoB.DeletedAt.IsValid()
	}
	return item
}
