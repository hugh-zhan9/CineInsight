package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"gorm.io/gorm"
)

// ReviewSearchRequest keeps the keyword and structured filters on every cursor request.
type ReviewSearchRequest struct {
	MediaID    uint   `json:"media_id"`
	Keyword    string `json:"keyword"`
	TagID      uint   `json:"tag_id"`
	Confidence string `json:"confidence"`
	Status     string `json:"status"`
	CursorID   uint   `json:"cursor_id"`
	Limit      int    `json:"limit"`
}

const reviewSearchBatchSize = 256

type reviewSearchSource struct {
	candidates string
	media      string
	mediaID    string
	relations  string
}

var videoReviewSource = reviewSearchSource{"ai_tag_candidates", "videos", "video_id", "video_tags"}
var imageReviewSource = reviewSearchSource{"image_ai_tag_candidates", "images", "image_id", "image_tags"}

type reviewSearchRow struct {
	ID            uint
	MediaID       uint
	Name          string
	Path          string
	SuggestedName string
	MatchedName   string
	Reasoning     string
	// These fields are selected only when building an approval preview, so its
	// matched evidence and revision come from the same database row observation.
	MatchedTagID   *uint
	RunID          *uint
	NormalizedName string
	Confidence     string
	Status         string
	SourceSummary  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ApprovedAt     *time.Time
	RejectedAt     *time.Time
}

func (r reviewSearchRow) revision(source reviewSearchSource) [32]byte {
	if source == videoReviewSource {
		return videoReviewRevision(models.AITagCandidate{ID: r.ID, VideoID: r.MediaID, MatchedTagID: r.MatchedTagID, RunID: r.RunID, SuggestedName: r.SuggestedName, NormalizedName: r.NormalizedName, Confidence: r.Confidence, Reasoning: r.Reasoning, SourceSummary: r.SourceSummary, Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ApprovedAt: r.ApprovedAt, RejectedAt: r.RejectedAt})
	}
	return imageReviewRevision(models.ImageAITagCandidate{ID: r.ID, ImageID: r.MediaID, MatchedTagID: r.MatchedTagID, SuggestedName: r.SuggestedName, NormalizedName: r.NormalizedName, Confidence: r.Confidence, Reasoning: r.Reasoning, SourceSummary: r.SourceSummary, Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ApprovedAt: r.ApprovedAt, RejectedAt: r.RejectedAt})
}

func normalizeReviewSearch(request ReviewSearchRequest) (ReviewSearchRequest, error) {
	request.Status = strings.TrimSpace(request.Status)
	if request.Status == "" {
		request.Status = models.AITagCandidateStatusPending
	}
	switch request.Status {
	case models.AITagCandidateStatusPending, models.AITagCandidateStatusApproved, models.AITagCandidateStatusRejected, models.AITagCandidateStatusSuperseded:
	default:
		return request, errors.New("invalid_review_status: 无效的候选状态")
	}
	request.Confidence = strings.ToLower(strings.TrimSpace(request.Confidence))
	if request.Confidence != "" && normalizeAIConfidence(request.Confidence) == "" {
		return request, errors.New("invalid_review_confidence: 无效的置信度")
	}
	request.Keyword = trimLiteralKeyword(request.Keyword)
	request.Limit = normalizeEntityPageLimit(request.Limit)
	return request, nil
}

// SearchCandidatePage filters the entire candidate range but hydrates only the matched page.
func (s *AITaggingService) SearchCandidatePage(ctx context.Context, request ReviewSearchRequest) (*AITagCandidatePage, error) {
	request, err := normalizeReviewSearch(request)
	if err != nil {
		return nil, err
	}
	ids, next, err := searchReviewCandidateIDs(ctx, database.DB, videoReviewSource, request)
	if err != nil {
		return nil, err
	}
	page := &AITagCandidatePage{Items: []AITaggingReviewItem{}, NextID: next}
	if len(ids) == 0 {
		return page, nil
	}
	var candidates []models.AITagCandidate
	if err := aiTagCandidateQuery(request.MediaID, "", request.Status).WithContext(ctx).Where("ai_tag_candidates.id IN ?", ids).Find(&candidates).Error; err != nil {
		return nil, err
	}
	page.Items = aiTagCandidateReviewItems(candidates)
	return page, nil
}

// SearchCandidatePage applies the same bounded keyword search to image candidates.
func (s *ImageAITaggingService) SearchCandidatePage(ctx context.Context, request ReviewSearchRequest) (*ImageAITagCandidatePage, error) {
	request, err := normalizeReviewSearch(request)
	if err != nil {
		return nil, err
	}
	ids, next, err := searchReviewCandidateIDs(ctx, s.db, imageReviewSource, request)
	if err != nil {
		return nil, err
	}
	page := &ImageAITagCandidatePage{Items: []ImageAITaggingReviewItem{}, NextID: next}
	if len(ids) == 0 {
		return page, nil
	}
	var candidates []models.ImageAITagCandidate
	if err := s.candidateQuery(request.MediaID, "", request.Status).WithContext(ctx).Where("image_ai_tag_candidates.id IN ?", ids).Find(&candidates).Error; err != nil {
		return nil, err
	}
	page.Items = imageAITagCandidateReviewItems(candidates)
	return page, nil
}

// Source identifiers below come only from the two private definitions, never from a request.
func searchReviewCandidateIDs(ctx context.Context, db *gorm.DB, source reviewSearchSource, request ReviewSearchRequest) ([]uint, uint, error) {
	return queryReviewCandidateIDs(ctx, db, source, request, nil)
}

func queryReviewCandidateIDs(ctx context.Context, db *gorm.DB, source reviewSearchSource, request ReviewSearchRequest, revisions map[uint][32]byte) ([]uint, uint, error) {
	if db == nil {
		return nil, 0, errors.New("review_database_unavailable: 数据库未初始化")
	}
	if ctx == nil {
		return nil, 0, errors.New("review_context_required: 查询上下文不能为空")
	}
	lower := cases.Lower(language.Und)
	keyword := lower.String(request.Keyword)
	ids := make([]uint, 0, request.Limit+1)
	cursor := request.CursorID
	var matchingTags []uint
	tagsLoaded := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		columns := fmt.Sprintf("c.id, c.%s AS media_id, COALESCE(m.name, '') AS name, COALESCE(m.path, '') AS path, c.suggested_name, COALESCE(mt.name, '') AS matched_name, c.reasoning", source.mediaID)
		if revisions != nil {
			columns += ", c.matched_tag_id, c.normalized_name, c.confidence, c.status, c.source_summary, c.created_at, c.updated_at, c.approved_at, c.rejected_at"
			if source == videoReviewSource {
				columns += ", c.run_id"
			}
		}
		query := db.WithContext(ctx).Table(source.candidates+" AS c").
			Select(columns).
			Joins(fmt.Sprintf("LEFT JOIN %s AS m ON m.id = c.%s", source.media, source.mediaID)).
			Joins("LEFT JOIN tags AS mt ON mt.id = c.matched_tag_id AND mt.deleted_at IS NULL").
			Where("c.status = ?", request.Status).Order("c.id DESC").Limit(reviewSearchBatchSize)
		if cursor > 0 {
			query = query.Where("c.id < ?", cursor)
		}
		if request.MediaID > 0 {
			query = query.Where("c."+source.mediaID+" = ?", request.MediaID)
		}
		if request.TagID > 0 {
			query = query.Where("c.matched_tag_id = ?", request.TagID)
		}
		if request.Confidence != "" {
			query = query.Where("LOWER(c.confidence) = ?", request.Confidence)
		}
		var rows []reviewSearchRow
		if err := query.Scan(&rows).Error; err != nil {
			return nil, 0, err
		}
		if len(rows) == 0 {
			return ids, 0, nil
		}
		direct := make(map[uint]bool, len(rows))
		mediaIDs := []uint{}
		seenMedia := map[uint]bool{}
		for _, row := range rows {
			match := keyword == ""
			for _, field := range []string{row.Name, row.Path, row.SuggestedName, row.MatchedName, row.Reasoning} {
				if !match && strings.Contains(lower.String(field), keyword) {
					match = true
				}
			}
			direct[row.ID] = match
			if !match && !seenMedia[row.MediaID] {
				mediaIDs = append(mediaIDs, row.MediaID)
				seenMedia[row.MediaID] = true
			}
		}
		if len(mediaIDs) > 0 && !tagsLoaded {
			var err error
			matchingTags, err = matchingReviewTagIDs(ctx, db, keyword, &lower)
			if err != nil {
				return nil, 0, err
			}
			tagsLoaded = true
		}
		taggedMedia := map[uint]bool{}
		if len(mediaIDs) > 0 && len(matchingTags) > 0 {
			for start := 0; start < len(matchingTags) && len(taggedMedia) < len(mediaIDs); start += reviewSearchBatchSize {
				end := min(start+reviewSearchBatchSize, len(matchingTags))
				remaining := make([]uint, 0, len(mediaIDs)-len(taggedMedia))
				for _, id := range mediaIDs {
					if !taggedMedia[id] {
						remaining = append(remaining, id)
					}
				}
				var relations []struct{ MediaID uint }
				if err := db.WithContext(ctx).Table(source.relations).
					Select("DISTINCT "+source.mediaID+" AS media_id").Where(source.mediaID+" IN ?", remaining).
					Where("tag_id IN ?", matchingTags[start:end]).Scan(&relations).Error; err != nil {
					return nil, 0, err
				}
				for _, relation := range relations {
					taggedMedia[relation.MediaID] = true
				}
			}
		}
		for _, row := range rows {
			if direct[row.ID] || taggedMedia[row.MediaID] {
				ids = append(ids, row.ID)
				if revisions != nil {
					revisions[row.ID] = row.revision(source)
				}
				if len(ids) > request.Limit {
					return ids[:request.Limit], ids[request.Limit-1], nil
				}
			}
		}
		cursor = rows[len(rows)-1].ID
		if len(rows) < reviewSearchBatchSize {
			return ids, 0, nil
		}
	}
}

func matchingReviewTagIDs(ctx context.Context, db *gorm.DB, keyword string, lower *cases.Caser) ([]uint, error) {
	matched := []uint{}
	cursor := uint(0)
	for {
		var tags []models.Tag
		if err := db.WithContext(ctx).Model(&models.Tag{}).Select("id", "name").Where("id > ?", cursor).Order("id ASC").Limit(reviewSearchBatchSize).Find(&tags).Error; err != nil {
			return nil, err
		}
		for _, tag := range tags {
			if strings.Contains(lower.String(tag.Name), keyword) {
				matched = append(matched, tag.ID)
			}
		}
		if len(tags) < reviewSearchBatchSize {
			return matched, nil
		}
		cursor = tags[len(tags)-1].ID
	}
}
