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

// ImageSemanticFilter 描述照片页语义检索的筛选边界（设计 4.7.2）。
type ImageSemanticFilter struct {
	TagIDs       []uint   `json:"tag_ids"`
	FavoriteOnly bool     `json:"favorite_only"`
	MinRating    *float64 `json:"min_rating"`
	MaxRating    *float64 `json:"max_rating"`
	MinSize      int64    `json:"min_size"`
	MaxSize      int64    `json:"max_size"`
}

// ImageSemanticSearchRequest 是照片页语义检索的入参，offset/limit 分页。
type ImageSemanticSearchRequest struct {
	Query  string              `json:"query"`
	Filter ImageSemanticFilter `json:"filter"`
	Offset int                 `json:"offset"`
	Limit  int                 `json:"limit"`
}

// ImageSemanticSearchHit 是单条命中，Score = clamp(1-余弦距离, -1, 1)。
type ImageSemanticSearchHit struct {
	Image models.Image `json:"image"`
	Score float64      `json:"score"`
}

// ImageSemanticCoverage 报告活跃图片中已建索引的覆盖情况。
type ImageSemanticCoverage struct {
	Indexed int64 `json:"indexed"`
	Total   int64 `json:"total"`
}

// ImageSemanticSearchPage 是照片页语义检索的单页结果。
type ImageSemanticSearchPage struct {
	Hits     []ImageSemanticSearchHit `json:"hits"`
	Coverage ImageSemanticCoverage    `json:"coverage"`
	HasMore  bool                     `json:"has_more"`
}

type imageSemanticDistanceRow struct {
	ImageID  uint    `gorm:"column:image_id"`
	Distance float64 `gorm:"column:distance"`
}

// SearchImagesSemantic 在照片页内做语义检索；SQL 镜像视频侧 querySemanticDistances，
// 只查 images 与 image_semantic_vectors，结果不含视频（AC-6）。
func (s *ImageSemanticIndexService) SearchImagesSemantic(ctx context.Context, request ImageSemanticSearchRequest) (*ImageSemanticSearchPage, error) {
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
	if err := s.validateSearchEmbedding(ctx, config.Model, embedding); err != nil {
		return nil, err
	}
	literal, err := database.SemanticVectorLiteral(embedding)
	if err != nil {
		return nil, err
	}
	rows, hasMore, err := s.queryImageSemanticDistances(ctx, request.Filter, profile, literal, request.Offset, request.Limit)
	if err != nil {
		return nil, err
	}
	return s.buildImageSemanticSearchPage(ctx, rows, profile, hasMore)
}

// activeSearchProfile 镜像视频侧：能力不可用返回图片版哨兵；profile 与当前配置
// 模型不符或待重建时拒绝检索。
func (s *ImageSemanticIndexService) activeSearchProfile() (models.SemanticIndexProfile, SemanticIndexConfig, error) {
	if s.db == nil || !s.capability.Available || s.capability.Backend != "postgres" {
		reason := strings.TrimSpace(s.capability.Message)
		if reason == "" {
			reason = "pgvector 不可用"
		}
		return models.SemanticIndexProfile{}, SemanticIndexConfig{}, fmt.Errorf("%w: %s", ErrImageSemanticIndexUnavailable, reason)
	}
	config, err := s.configProvider.Load()
	if err != nil {
		return models.SemanticIndexProfile{}, SemanticIndexConfig{}, fmt.Errorf("%w: %v", ErrImageSemanticIndexUnavailable, err)
	}
	config, err = normalizeImageSemanticIndexConfig(config)
	if err != nil {
		return models.SemanticIndexProfile{}, SemanticIndexConfig{}, err
	}
	var profile models.SemanticIndexProfile
	if err := s.db.First(&profile, "id = ?", 1).Error; err != nil {
		return profile, config, err
	}
	if profile.NeedsRebuild || profile.Dimension <= 0 || profile.ActiveModel != config.Model {
		return profile, config, ErrImageSemanticIndexRebuildRequired
	}
	return profile, config, nil
}

// validateSearchEmbedding 防止查询向量与共享 profile 的模型或维度混用，镜像视频侧。
func (s *ImageSemanticIndexService) validateSearchEmbedding(ctx context.Context, model string, embedding []float64) error {
	var profile models.SemanticIndexProfile
	if err := s.db.WithContext(ctx).First(&profile, "id = ?", 1).Error; err != nil {
		return err
	}
	if profile.NeedsRebuild || profile.Dimension <= 0 {
		return ErrImageSemanticIndexRebuildRequired
	}
	if strings.TrimSpace(model) != profile.ActiveModel {
		return fmt.Errorf("%w: active=%q requested=%q", ErrSemanticIndexModelMismatch, profile.ActiveModel, model)
	}
	if len(embedding) != profile.Dimension {
		return fmt.Errorf("%w: active=%d requested=%d", ErrSemanticIndexDimensionMismatch, profile.Dimension, len(embedding))
	}
	return nil
}

// embedSearchQuery 带 TTL/容量上限的查询向量缓存，key=sha256(baseURL+model+query)，镜像视频侧。
func (s *ImageSemanticIndexService) embedSearchQuery(ctx context.Context, config SemanticIndexConfig, query string) ([]float64, error) {
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

// normalizeImageSemanticPage 归一化 offset/limit：limit 默认 20、上限 100，offset 非负。
func normalizeImageSemanticPage(offset, limit int) (int, int) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return offset, limit
}

// trimImageSemanticRows 用 limit+1 探测行裁剪出单页并报告 hasMore。
func trimImageSemanticRows(rows []imageSemanticDistanceRow, limit int) ([]imageSemanticDistanceRow, bool) {
	if len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}

// imageSemanticScore 把余弦距离换算为 [-1,1] 的相似度分数。
func imageSemanticScore(distance float64) float64 {
	return math.Max(-1, math.Min(1, 1-distance))
}

// imageSemanticDistanceQuery 构建距离查询：images JOIN image_semantic_vectors、
// 模型/维度/代际匹配、排除软删除、距离升序并以 id 决胜；绝不引用视频侧任何表。
func (s *ImageSemanticIndexService) imageSemanticDistanceQuery(ctx context.Context, filter ImageSemanticFilter, profile models.SemanticIndexProfile, literal string, offset, limit int) *gorm.DB {
	query := s.db.WithContext(ctx).Table("images").
		Select("images.id AS image_id, image_semantic_vectors.embedding <=> CAST(? AS vector) AS distance", literal).
		Joins("JOIN image_semantic_vectors ON image_semantic_vectors.image_id = images.id").
		Where("image_semantic_vectors.model_identifier = ? AND image_semantic_vectors.dimension = ? AND image_semantic_vectors.generation = ?", profile.ActiveModel, profile.Dimension, profile.Generation).
		Where("images.deleted_at IS NULL")
	query = s.applyImageSemanticFilter(query, filter)
	return query.Order("distance ASC, images.id ASC").Offset(offset).Limit(limit + 1)
}

func (s *ImageSemanticIndexService) queryImageSemanticDistances(ctx context.Context, filter ImageSemanticFilter, profile models.SemanticIndexProfile, literal string, offset, limit int) ([]imageSemanticDistanceRow, bool, error) {
	offset, limit = normalizeImageSemanticPage(offset, limit)
	var rows []imageSemanticDistanceRow
	if err := s.imageSemanticDistanceQuery(ctx, filter, profile, literal, offset, limit).Scan(&rows).Error; err != nil {
		return nil, false, err
	}
	rows, hasMore := trimImageSemanticRows(rows, limit)
	return rows, hasMore, nil
}

// applyImageSemanticFilter 手写应用照片页筛选条件，语义与 applyImageFilter 一致。
func (s *ImageSemanticIndexService) applyImageSemanticFilter(query *gorm.DB, filter ImageSemanticFilter) *gorm.DB {
	if filter.FavoriteOnly {
		query = query.Where("images.is_favorite = ?", true)
	}
	if filter.MinSize > 0 {
		query = query.Where("images.size >= ?", filter.MinSize)
	}
	if filter.MaxSize > 0 {
		query = query.Where("images.size < ?", filter.MaxSize)
	}
	if filter.MinRating != nil {
		query = query.Where("images.personal_rating >= ?", *filter.MinRating)
	}
	if filter.MaxRating != nil {
		query = query.Where("images.personal_rating <= ?", *filter.MaxRating)
	}
	if tagIDs := uniqueUintIDs(filter.TagIDs); len(tagIDs) > 0 {
		subquery := s.db.Table("image_tags").Select("image_id").
			Where("tag_id IN ?", tagIDs).
			Group("image_id").
			Having("COUNT(DISTINCT tag_id) = ?", len(tagIDs))
		query = query.Where("images.id IN (?)", subquery)
	}
	return query
}

// buildImageSemanticSearchPage 装配一页命中。与视频侧同款已知边界：offset 分页
// 在索引补全/重建间隙可能漏或重；两次查询之间消失（如软删除）的图片行被丢弃
// 产生短页，hasMore 保持不变，继续加载即可补齐。
func (s *ImageSemanticIndexService) buildImageSemanticSearchPage(ctx context.Context, rows []imageSemanticDistanceRow, profile models.SemanticIndexProfile, hasMore bool) (*ImageSemanticSearchPage, error) {
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ImageID)
	}
	var images []models.Image
	if len(ids) > 0 {
		if err := s.db.WithContext(ctx).Preload("Tags").Where("id IN ?", ids).Find(&images).Error; err != nil {
			return nil, err
		}
	}
	byID := make(map[uint]models.Image, len(images))
	for _, image := range images {
		byID[image.ID] = image
	}
	hits := make([]ImageSemanticSearchHit, 0, len(rows))
	for _, row := range rows {
		image, exists := byID[row.ImageID]
		if !exists {
			continue
		}
		hits = append(hits, ImageSemanticSearchHit{Image: image, Score: imageSemanticScore(row.Distance)})
	}
	coverage, err := s.imageSemanticCoverage(ctx, profile)
	if err != nil {
		return nil, err
	}
	return &ImageSemanticSearchPage{Hits: hits, Coverage: coverage, HasMore: hasMore}, nil
}

func (s *ImageSemanticIndexService) imageSemanticCoverage(ctx context.Context, profile models.SemanticIndexProfile) (ImageSemanticCoverage, error) {
	coverage := ImageSemanticCoverage{}
	if err := s.db.WithContext(ctx).Model(&models.Image{}).Count(&coverage.Total).Error; err != nil {
		return coverage, err
	}
	if err := s.db.WithContext(ctx).Model(&models.ImageSemanticIndex{}).
		Where("model_identifier = ? AND dimension = ? AND generation = ?", profile.ActiveModel, profile.Dimension, profile.Generation).
		Where("image_id IN (?)", s.db.Model(&models.Image{}).Select("id")).
		Distinct("image_id").Count(&coverage.Indexed).Error; err != nil {
		return coverage, err
	}
	return coverage, nil
}
