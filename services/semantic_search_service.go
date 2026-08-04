package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

const (
	semanticQueryCacheTTL = 10 * time.Minute
	semanticQueryCacheMax = 8
)

type semanticQueryCacheEntry struct {
	Embedding []float64
	CreatedAt time.Time
}

type SemanticSearchCoverage struct {
	Indexed int64 `json:"indexed"`
	Total   int64 `json:"total"`
}

type SemanticSearchHit struct {
	Video models.Video `json:"video"`
	Score float64      `json:"score"`
}

type SemanticSearchRequest struct {
	Query  string        `json:"query"`
	Filter LibraryFilter `json:"filter"`
	Offset int           `json:"offset"`
	Limit  int           `json:"limit"`
}

type SemanticSimilarRequest struct {
	VideoID uint          `json:"video_id"`
	Filter  LibraryFilter `json:"filter"`
	Offset  int           `json:"offset"`
	Limit   int           `json:"limit"`
}

type SemanticSearchPage struct {
	Hits     []SemanticSearchHit    `json:"hits"`
	Coverage SemanticSearchCoverage `json:"coverage"`
	HasMore  bool                   `json:"has_more"`
}

type semanticDistanceRow struct {
	VideoID  uint    `gorm:"column:video_id"`
	Distance float64 `gorm:"column:distance"`
}

func (s *SemanticIndexService) Search(ctx context.Context, request SemanticSearchRequest) (*SemanticSearchPage, error) {
	queryText := strings.TrimSpace(request.Query)
	if queryText == "" {
		return nil, errors.New("语义搜索内容不能为空")
	}
	profile, config, err := s.activeSearchProfile()
	if err != nil {
		return nil, err
	}
	embedding, err := s.embedSearchQuery(ctx, config, queryText)
	if err != nil {
		return nil, fmt.Errorf("语义查询向量生成失败: %w", err)
	}
	if err := s.ValidateSearchEmbedding(ctx, config.Model, embedding); err != nil {
		return nil, err
	}
	literal, err := database.SemanticVectorLiteral(embedding)
	if err != nil {
		return nil, err
	}
	rows, hasMore, err := s.querySemanticDistances(ctx, request.Filter, profile, literal, 0, request.Offset, request.Limit)
	if err != nil {
		return nil, err
	}
	return s.buildSemanticSearchPage(ctx, rows, profile, hasMore)
}

func (s *SemanticIndexService) embedSearchQuery(ctx context.Context, config SemanticIndexConfig, query string) ([]float64, error) {
	digest := sha256.Sum256([]byte(config.BaseURL + "\x00" + config.Model + "\x00" + query))
	key := hex.EncodeToString(digest[:])
	now := s.now()
	s.queryMu.Lock()
	if cached, exists := s.queryCache[key]; exists && now.Sub(cached.CreatedAt) <= semanticQueryCacheTTL {
		embedding := append([]float64(nil), cached.Embedding...)
		s.queryMu.Unlock()
		return embedding, nil
	}
	s.queryMu.Unlock()
	embedding, err := s.embedderFactory(config).Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	s.queryMu.Lock()
	if s.queryCache == nil {
		s.queryCache = make(map[string]semanticQueryCacheEntry)
	}
	if len(s.queryCache) >= semanticQueryCacheMax {
		oldestKey := ""
		var oldest time.Time
		for candidateKey, candidate := range s.queryCache {
			if oldestKey == "" || candidate.CreatedAt.Before(oldest) {
				oldestKey, oldest = candidateKey, candidate.CreatedAt
			}
		}
		delete(s.queryCache, oldestKey)
	}
	s.queryCache[key] = semanticQueryCacheEntry{Embedding: append([]float64(nil), embedding...), CreatedAt: now}
	s.queryMu.Unlock()
	return embedding, nil
}

func (s *SemanticIndexService) clearSemanticQueryCache() {
	s.queryMu.Lock()
	s.queryCache = nil
	s.queryMu.Unlock()
}

func (s *SemanticIndexService) FindSimilar(ctx context.Context, request SemanticSimilarRequest) (*SemanticSearchPage, error) {
	if request.VideoID == 0 {
		return nil, errors.New("视频 ID 不能为空")
	}
	profile, _, err := s.activeSearchProfile()
	if err != nil {
		return nil, err
	}
	rows, hasMore, err := s.querySemanticDistances(ctx, request.Filter, profile, "", request.VideoID, request.Offset, request.Limit)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		var indexed int64
		if err := s.db.WithContext(ctx).Model(&models.VideoSemanticIndex{}).
			Where("video_id = ? AND model_identifier = ? AND dimension = ? AND generation = ?", request.VideoID, profile.ActiveModel, profile.Dimension, profile.Generation).
			Count(&indexed).Error; err != nil {
			return nil, err
		}
		if indexed == 0 {
			return nil, errors.New("当前视频尚未建立语义索引")
		}
	}
	return s.buildSemanticSearchPage(ctx, rows, profile, hasMore)
}

func (s *SemanticIndexService) activeSearchProfile() (models.SemanticIndexProfile, SemanticIndexConfig, error) {
	if s.db == nil || !s.capability.Available || s.capability.Backend != "postgres" {
		reason := strings.TrimSpace(s.capability.Message)
		if reason == "" {
			reason = "pgvector 不可用"
		}
		return models.SemanticIndexProfile{}, SemanticIndexConfig{}, fmt.Errorf("%w: %s", ErrSemanticIndexUnavailable, reason)
	}
	config, err := s.configProvider.Load()
	if err != nil {
		return models.SemanticIndexProfile{}, SemanticIndexConfig{}, fmt.Errorf("%w: %v", ErrSemanticIndexUnavailable, err)
	}
	config, err = normalizeSemanticIndexConfig(config)
	if err != nil {
		return models.SemanticIndexProfile{}, SemanticIndexConfig{}, err
	}
	var profile models.SemanticIndexProfile
	if err := s.db.First(&profile, "id = ?", 1).Error; err != nil {
		return profile, config, err
	}
	if profile.NeedsRebuild || profile.Dimension <= 0 || profile.ActiveModel != config.Model {
		return profile, config, ErrSemanticIndexRebuildRequired
	}
	return profile, config, nil
}

func (s *SemanticIndexService) querySemanticDistances(ctx context.Context, filter LibraryFilter, profile models.SemanticIndexProfile, literal string, sourceVideoID uint, offset, limit int) ([]semanticDistanceRow, bool, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	filter.SearchMode = LibrarySearchModeFile
	filter.Keyword = ""
	filter.SortMode = LibrarySortBalanced

	var query *gorm.DB
	if sourceVideoID == 0 {
		query = s.db.WithContext(ctx).Table("videos").
			Select("videos.id AS video_id, video_semantic_vectors.embedding <=> CAST(? AS vector) AS distance", literal).
			Joins("JOIN video_semantic_vectors ON video_semantic_vectors.video_id = videos.id").
			Where("video_semantic_vectors.model_identifier = ? AND video_semantic_vectors.dimension = ? AND video_semantic_vectors.generation = ?", profile.ActiveModel, profile.Dimension, profile.Generation)
	} else {
		query = s.db.WithContext(ctx).Table("videos").
			Select("videos.id AS video_id, video_semantic_vectors.embedding <=> semantic_source.embedding AS distance").
			Joins("JOIN video_semantic_vectors ON video_semantic_vectors.video_id = videos.id").
			Joins("JOIN video_semantic_vectors AS semantic_source ON semantic_source.video_id = ? AND semantic_source.model_identifier = ? AND semantic_source.dimension = ? AND semantic_source.generation = ?", sourceVideoID, profile.ActiveModel, profile.Dimension, profile.Generation).
			Where("video_semantic_vectors.model_identifier = ? AND video_semantic_vectors.dimension = ? AND video_semantic_vectors.generation = ?", profile.ActiveModel, profile.Dimension, profile.Generation).
			Where("videos.id <> ?", sourceVideoID)
	}
	query = query.Where("videos.deleted_at IS NULL")
	query, err := applyLibraryFilter(query, filter, time.Now())
	if err != nil {
		return nil, false, err
	}
	var rows []semanticDistanceRow
	if err := query.Order("distance ASC, videos.id ASC").Offset(offset).Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	return rows, hasMore, nil
}

// buildSemanticSearchPage 装配一页命中。已知边界：分页是基于活动索引的
// offset 语义，索引在翻页间隙被补全/重建时相邻页可能漏或重；两次查询之间
// 消失的视频行会被丢弃，产生短页——此时 hasMore 保持为 true，前端继续加
// 载即可补齐，不会误判为结束。
func (s *SemanticIndexService) buildSemanticSearchPage(ctx context.Context, rows []semanticDistanceRow, profile models.SemanticIndexProfile, hasMore bool) (*SemanticSearchPage, error) {
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.VideoID)
	}
	var videos []models.Video
	if len(ids) > 0 {
		if err := s.db.WithContext(ctx).Preload("Tags").Where("id IN ?", ids).Find(&videos).Error; err != nil {
			return nil, err
		}
	}
	byID := make(map[uint]models.Video, len(videos))
	for _, video := range videos {
		byID[video.ID] = video
	}
	hits := make([]SemanticSearchHit, 0, len(rows))
	for _, row := range rows {
		video, exists := byID[row.VideoID]
		if !exists {
			continue
		}
		hits = append(hits, SemanticSearchHit{Video: video, Score: math.Max(-1, math.Min(1, 1-row.Distance))})
	}
	coverage, err := s.semanticCoverage(ctx, profile)
	if err != nil {
		return nil, err
	}
	return &SemanticSearchPage{Hits: hits, Coverage: coverage, HasMore: hasMore}, nil
}

func (s *SemanticIndexService) semanticCoverage(ctx context.Context, profile models.SemanticIndexProfile) (SemanticSearchCoverage, error) {
	coverage := SemanticSearchCoverage{}
	if err := s.db.WithContext(ctx).Model(&models.Video{}).Count(&coverage.Total).Error; err != nil {
		return coverage, err
	}
	if err := s.db.WithContext(ctx).Model(&models.VideoSemanticIndex{}).
		Where("model_identifier = ? AND dimension = ? AND generation = ?", profile.ActiveModel, profile.Dimension, profile.Generation).
		Where("video_id IN (?)", s.db.Model(&models.Video{}).Select("id")).
		Distinct("video_id").Count(&coverage.Indexed).Error; err != nil {
		return coverage, err
	}
	return coverage, nil
}

