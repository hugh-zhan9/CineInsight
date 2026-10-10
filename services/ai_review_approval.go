package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"sort"
	"strconv"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// Length-prefix the raw field bytes; delimiter characters and invalid UTF-8 cannot collide.
func reviewRevision(fields ...string) [32]byte {
	hash := sha256.New()
	var size [8]byte
	for _, field := range fields {
		binary.LittleEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(field))
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func reviewUint(id uint) string { return strconv.FormatUint(uint64(id), 10) }
func reviewOptionalUint(id *uint) string {
	if id == nil {
		return "nil"
	}
	return reviewUint(*id)
}
func reviewTime(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }
func reviewOptionalTime(at *time.Time) string {
	if at == nil {
		return "nil"
	}
	return reviewTime(*at)
}

func videoReviewRevision(c models.AITagCandidate) [32]byte {
	return reviewRevision(reviewUint(c.ID), reviewUint(c.VideoID), reviewOptionalUint(c.MatchedTagID), reviewOptionalUint(c.RunID), c.SuggestedName, c.NormalizedName, c.Confidence, c.Reasoning, c.SourceSummary, c.Status, reviewTime(c.CreatedAt), reviewTime(c.UpdatedAt), reviewOptionalTime(c.ApprovedAt), reviewOptionalTime(c.RejectedAt))
}
func imageReviewRevision(c models.ImageAITagCandidate) [32]byte {
	return reviewRevision(reviewUint(c.ID), reviewUint(c.ImageID), reviewOptionalUint(c.MatchedTagID), c.SuggestedName, c.NormalizedName, c.Confidence, c.Reasoning, c.SourceSummary, c.Status, reviewTime(c.CreatedAt), reviewTime(c.UpdatedAt), reviewOptionalTime(c.ApprovedAt), reviewOptionalTime(c.RejectedAt))
}

type reviewApprovalMember struct {
	frozenReviewCandidate
	mediaID, tagID uint
}

func eligibleReviewCandidate(status, confidence string, activeMediaID uint, tag *models.Tag) bool {
	return status == models.AITagCandidateStatusPending && (confidence == models.AITagConfidenceHigh || confidence == models.AITagConfidenceMedium) && activeMediaID != 0 && tag != nil && isAITagEligible(*tag)
}

func loadVideoApprovalMembers(ctx context.Context, ids []uint) ([]reviewApprovalMember, error) {
	var rows []models.AITagCandidate
	if err := database.DB.WithContext(ctx).Where("id IN ?", ids).
		Preload("Video", func(tx *gorm.DB) *gorm.DB { return tx.Select("id") }).Preload("MatchedTag").Find(&rows).Error; err != nil {
		return nil, err
	}
	members := make([]reviewApprovalMember, 0, len(rows))
	for _, row := range rows {
		if eligibleReviewCandidate(row.Status, row.Confidence, row.Video.ID, row.MatchedTag) {
			members = append(members, reviewApprovalMember{frozenReviewCandidate{row.ID, videoReviewRevision(row)}, row.VideoID, *row.MatchedTagID})
		}
	}
	return members, nil
}

func (s *ImageAITaggingService) loadImageApprovalMembers(ctx context.Context, ids []uint) ([]reviewApprovalMember, error) {
	var rows []models.ImageAITagCandidate
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).
		Preload("Image", func(tx *gorm.DB) *gorm.DB { return tx.Select("id") }).Preload("MatchedTag").Find(&rows).Error; err != nil {
		return nil, err
	}
	members := make([]reviewApprovalMember, 0, len(rows))
	for _, row := range rows {
		if eligibleReviewCandidate(row.Status, row.Confidence, row.Image.ID, row.MatchedTag) {
			members = append(members, reviewApprovalMember{frozenReviewCandidate{row.ID, imageReviewRevision(row)}, row.ImageID, *row.MatchedTagID})
		}
	}
	return members, nil
}

func buildReviewApproval(ctx context.Context, db *gorm.DB, source reviewSearchSource, request ReviewApprovalRequest, load func(context.Context, []uint) ([]reviewApprovalMember, error)) (reviewApprovalBuild, error) {
	built := reviewApprovalBuild{preview: ReviewApprovalPreview{Scope: request.Scope}}
	if db == nil {
		return built, errors.New("review_database_unavailable: 数据库未初始化")
	}
	if request.Scope != "loaded" && request.Scope != "filtered" {
		return built, errors.New("review_scope_invalid: 无效的批准范围")
	}
	if len(request.IDs) > maxReviewPreviewMembers {
		return built, ErrReviewLimit
	}
	filter, err := normalizeReviewSearch(request.Filter)
	if err != nil {
		return built, err
	}
	if filter.Status != models.AITagCandidateStatusPending {
		return built, errors.New("review_status_invalid: 只能批准待审候选")
	}
	filter.CursorID, filter.Limit = 0, 200
	ids := make([]uint, 0, len(request.IDs))
	seen := map[uint]bool{}
	for _, id := range request.IDs {
		if id == 0 {
			return built, errors.New("review_candidate_invalid: 无效的候选编号")
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	if request.Scope == "filtered" && len(ids) > 0 {
		return built, errors.New("review_scope_invalid: 全部筛选范围不能同时指定候选编号")
	}
	// Deterministic descending order also preserves the query's duplicate resolution order.
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	media := map[uint]bool{}
	links := map[[2]uint]bool{}
	for offset := 0; ; offset += 200 {
		if err := ctx.Err(); err != nil {
			return built, err
		}
		var pageIDs []uint
		var matchedRevisions map[uint][32]byte
		next := uint(0)
		if request.Scope == "filtered" {
			matchedRevisions = make(map[uint][32]byte)
			pageIDs, next, err = queryReviewCandidateIDs(ctx, db, source, filter, matchedRevisions)
			if err != nil {
				return built, err
			}
		} else if offset < len(ids) {
			pageIDs = ids[offset:min(offset+200, len(ids))]
		}
		if len(pageIDs) == 0 {
			break
		}
		built.preview.Matched += len(pageIDs)
		if built.preview.Matched > maxReviewPreviewMembers {
			return built, ErrReviewLimit
		}
		members, err := load(ctx, pageIDs)
		if err != nil {
			return built, err
		}
		byID := make(map[uint]reviewApprovalMember, len(members))
		for _, member := range members {
			byID[member.ID] = member
		}
		for _, id := range pageIDs {
			if member, ok := byID[id]; ok {
				if request.Scope == "filtered" && matchedRevisions[id] != member.Revision {
					continue
				}
				built.members = append(built.members, member.frozenReviewCandidate)
				media[member.mediaID] = true
				links[[2]uint{member.mediaID, member.tagID}] = true
			}
		}
		if request.Scope == "filtered" {
			if next == 0 {
				break
			}
			filter.CursorID = next
		}
	}
	built.preview.MediaCount, built.preview.LinkCount = len(media), len(links)
	return built, nil
}

func reviewApprovalFailure(ctx context.Context, db *gorm.DB, id uint, err error) (ReviewApprovalOutcome, error) {
	if ctx.Err() != nil || errors.Is(err, database.ErrMaintenance) || errors.Is(err, sql.ErrConnDone) {
		return ReviewApprovalOutcome{}, err
	}
	outcome := ReviewApprovalOutcome{ID: id, State: "failed", Code: "approval_failed"}
	switch {
	case errors.Is(err, ErrReviewChanged):
		outcome.State, outcome.Code = "skipped", "changed"
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, ErrAITagCandidateNoTag), errors.Is(err, ErrAITagCandidateTagDeleted), errors.Is(err, ErrAITagCandidateTagUnavailable):
		outcome.State, outcome.Code = "skipped", "unavailable"
	case errors.Is(err, ErrAITagCandidateNotPending), errors.Is(err, ErrAITagCandidateSuperseded), errors.Is(err, ErrAITagCandidateNotApprovable):
		outcome.State, outcome.Code = "skipped", "not_pending"
	}
	if outcome.State == "failed" {
		// A connection failure is a batch boundary, not a million separate candidate
		// failures. This is a bounded read-only availability check, never an approval retry.
		sqlDB, accessErr := db.DB()
		if accessErr != nil {
			return ReviewApprovalOutcome{}, accessErr
		}
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if accessErr = sqlDB.PingContext(checkCtx); accessErr != nil {
			return ReviewApprovalOutcome{}, accessErr
		}
	}
	return outcome, nil
}

func (s *AITaggingService) ReviewApproval() *ReviewApprovalWorkflow {
	s.reviewMu.Lock()
	defer s.reviewMu.Unlock()
	if s.reviewApproval == nil {
		s.reviewApproval = newReviewApprovalWorkflow(func(ctx context.Context, request ReviewApprovalRequest) (reviewApprovalBuild, error) {
			return buildReviewApproval(ctx, database.DB, videoReviewSource, request, loadVideoApprovalMembers)
		}, func(ctx context.Context, member frozenReviewCandidate) (ReviewApprovalOutcome, error) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			row, err := s.approveCandidateCommitted(ctx, member.ID, &member.Revision)
			if err != nil {
				return reviewApprovalFailure(ctx, database.DB, member.ID, err)
			}
			return ReviewApprovalOutcome{ID: row.ID, MediaID: row.VideoID, TagID: *row.MatchedTagID, State: "approved"}, nil
		}, func() *BackgroundTaskRegistry {
			s.workerMu.Lock()
			defer s.workerMu.Unlock()
			return s.registry
		}, BackgroundTaskAIReview)
	}
	return s.reviewApproval
}

func (s *ImageAITaggingService) ReviewApproval() *ReviewApprovalWorkflow {
	s.reviewMu.Lock()
	defer s.reviewMu.Unlock()
	if s.reviewApproval == nil {
		s.reviewApproval = newReviewApprovalWorkflow(func(ctx context.Context, request ReviewApprovalRequest) (reviewApprovalBuild, error) {
			return buildReviewApproval(ctx, s.db, imageReviewSource, request, s.loadImageApprovalMembers)
		}, func(ctx context.Context, member frozenReviewCandidate) (ReviewApprovalOutcome, error) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			row, err := s.approveImageCandidateCommitted(ctx, member.ID, &member.Revision)
			if err != nil {
				return reviewApprovalFailure(ctx, s.db, member.ID, err)
			}
			return ReviewApprovalOutcome{ID: row.ID, MediaID: row.ImageID, TagID: *row.MatchedTagID, State: "approved"}, nil
		}, func() *BackgroundTaskRegistry {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.registry
		}, BackgroundTaskImageAIReview)
	}
	return s.reviewApproval
}

// ResetReviewApproval is called only after CloseAndWait, on failed maintenance recovery.
func (s *AITaggingService) ResetReviewApproval() {
	s.reviewMu.Lock()
	defer s.reviewMu.Unlock()
	s.reviewApproval = nil
}

func (s *ImageAITaggingService) ResetReviewApproval() {
	s.reviewMu.Lock()
	defer s.reviewMu.Unlock()
	s.reviewApproval = nil
}
