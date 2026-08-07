package services

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 照片页排序模式（D-013/D-017）。
const (
	ImageSortRecent = "recent"
	ImageSortSize   = "size"
	ImageSortRating = "rating"
)

// ImageLibraryService 提供照片页的查询、标签与收藏/评分能力。
type ImageLibraryService struct{}

// NewImageLibraryService 构造照片页数据服务。
func NewImageLibraryService() *ImageLibraryService {
	return &ImageLibraryService{}
}

// ImageFilter 描述照片页的筛选边界。
type ImageFilter struct {
	Keyword      string   `json:"keyword"`
	TagIDs       []uint   `json:"tag_ids"`
	FavoriteOnly bool     `json:"favorite_only"`
	MinRating    *float64 `json:"min_rating"`
	MaxRating    *float64 `json:"max_rating"`
	MinSize      int64    `json:"min_size"`
	MaxSize      int64    `json:"max_size"`
	SortMode     string   `json:"sort_mode"`
}

// ImageCursor 是 SearchImagePage 的稳定分页游标，字段按排序模式取用。
type ImageCursor struct {
	SortMode     string   `json:"sort_mode"`
	CreatedAt    string   `json:"created_at,omitempty"`
	Size         int64    `json:"size"`
	Rating       *float64 `json:"rating,omitempty"`
	RatingIsNull bool     `json:"rating_is_null"`
	ID           uint     `json:"id"`
}

// ImagePage 是照片页单页结果，NextCursor 为空表示已到末页。
type ImagePage struct {
	Images     []models.Image `json:"images"`
	NextCursor *ImageCursor   `json:"next_cursor,omitempty"`
}

// ImagePageRequest keeps the optional cursor inside a generated DTO so
// frontend callers can omit it instead of passing an untyped null argument.
type ImagePageRequest struct {
	Filter ImageFilter  `json:"filter"`
	Cursor *ImageCursor `json:"cursor,omitempty"`
	Limit  int          `json:"limit"`
}

// ImageDetail 聚合单张图片与已生成的 AI 描述；描述由后续切片回填，未生成时为空串。
type ImageDetail struct {
	Image         models.Image `json:"image"`
	AIDescription string       `json:"ai_description"`
}

// BatchImageOperationError 记录批量操作中单张图片的失败原因。
type BatchImageOperationError struct {
	ImageID uint   `json:"image_id"`
	Error   string `json:"error"`
}

// BatchImageOperationWarning 记录批量操作中单张图片的非致命提示。
type BatchImageOperationWarning struct {
	ImageID uint   `json:"image_id"`
	Warning string `json:"warning"`
}

// BatchImageOperationResult 镜像 BatchVideoOperationResult：逐项失败原因、无顶层 error。
type BatchImageOperationResult struct {
	Requested int                          `json:"requested"`
	Succeeded int                          `json:"succeeded"`
	Failed    int                          `json:"failed"`
	Errors    []BatchImageOperationError   `json:"errors"`
	Warnings  []BatchImageOperationWarning `json:"warnings"`
}

func newBatchImageOperationResult(ids []uint) *BatchImageOperationResult {
	return &BatchImageOperationResult{
		Requested: len(ids),
		Errors:    make([]BatchImageOperationError, 0),
		Warnings:  make([]BatchImageOperationWarning, 0),
	}
}

func (r *BatchImageOperationResult) record(imageID uint, err error) {
	if err == nil {
		r.Succeeded++
		return
	}
	r.Failed++
	r.Errors = append(r.Errors, BatchImageOperationError{
		ImageID: imageID,
		Error:   err.Error(),
	})
}

func normalizeImageFilter(filter ImageFilter) (ImageFilter, error) {
	filter.Keyword = strings.TrimSpace(filter.Keyword)
	filter.TagIDs = uniqueUintIDs(filter.TagIDs)
	sort.Slice(filter.TagIDs, func(i, j int) bool { return filter.TagIDs[i] < filter.TagIDs[j] })
	if filter.MinSize < 0 || filter.MaxSize < 0 {
		return ImageFilter{}, fmt.Errorf("筛选范围不能为负数")
	}
	if filter.MaxSize > 0 && filter.MinSize >= filter.MaxSize {
		return ImageFilter{}, fmt.Errorf("体积筛选上限必须大于下限")
	}
	if err := validateRatingValue(filter.MinRating); err != nil {
		return ImageFilter{}, fmt.Errorf("最低评分无效: %w", err)
	}
	if err := validateRatingValue(filter.MaxRating); err != nil {
		return ImageFilter{}, fmt.Errorf("最高评分无效: %w", err)
	}
	if filter.MinRating != nil && filter.MaxRating != nil && *filter.MinRating > *filter.MaxRating {
		return ImageFilter{}, fmt.Errorf("评分筛选上限不能小于下限")
	}
	filter.SortMode = strings.TrimSpace(filter.SortMode)
	if filter.SortMode == "" {
		filter.SortMode = ImageSortRecent
	}
	switch filter.SortMode {
	case ImageSortRecent, ImageSortSize, ImageSortRating:
	default:
		return ImageFilter{}, fmt.Errorf("不支持的排序模式: %s", filter.SortMode)
	}
	return filter, nil
}

// applyImageFilter 假设 filter 已经 normalizeImageFilter；只查活跃行由软删除默认作用域保证。
func applyImageFilter(query *gorm.DB, filter ImageFilter) *gorm.DB {
	if filter.Keyword != "" {
		pattern := "%" + strings.ToLower(escapeSQLLike(filter.Keyword)) + "%"
		query = query.Where("LOWER(images.name) LIKE ? ESCAPE '\\'", pattern)
	}
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
	if len(filter.TagIDs) > 0 {
		subquery := database.DB.Table("image_tags").Select("image_id").
			Where("tag_id IN ?", filter.TagIDs).
			Group("image_id").
			Having("COUNT(DISTINCT tag_id) = ?", len(filter.TagIDs))
		query = query.Where("images.id IN (?)", subquery)
	}
	return query
}

func validateImageCursor(sortMode string, cursor *ImageCursor) error {
	if cursor == nil {
		return nil
	}
	if cursor.SortMode != sortMode {
		return errors.New("照片游标排序模式不匹配")
	}
	if cursor.ID == 0 {
		return errors.New("照片游标 ID 无效")
	}
	switch sortMode {
	case ImageSortRecent:
		if cursor.Rating != nil || cursor.RatingIsNull {
			return errors.New("recent 游标包含评分字段")
		}
		if _, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt); err != nil {
			return fmt.Errorf("照片时间游标无效: %w", err)
		}
		return nil
	case ImageSortSize:
		if cursor.Rating != nil || cursor.RatingIsNull {
			return errors.New("size 游标包含评分字段")
		}
		if cursor.Size < 0 {
			return errors.New("照片体积游标无效")
		}
		return nil
	default:
		if cursor.RatingIsNull {
			if cursor.Rating != nil {
				return errors.New("NULL 评分游标不能包含评分值")
			}
			return nil
		}
		if cursor.Rating == nil {
			return errors.New("评分游标缺少评分值")
		}
		return validateRatingValue(cursor.Rating)
	}
}

// SearchImagePage 按 DTO 游标稳定分页照片页（AC-4/D-017）：
// recent=created_at DESC（默认）、size=体积 DESC、rating=评分 DESC 且 NULL 排后，均以 id DESC 决胜。
func (s *ImageLibraryService) SearchImagePage(request ImagePageRequest) (*ImagePage, error) {
	filter, err := normalizeImageFilter(request.Filter)
	if err != nil {
		return nil, err
	}
	limit := request.Limit
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	cursor := request.Cursor
	if err := validateImageCursor(filter.SortMode, cursor); err != nil {
		return nil, err
	}

	query := applyImageFilter(database.DB.Model(&models.Image{}).Preload("Tags"), filter)
	switch filter.SortMode {
	case ImageSortRecent:
		if cursor != nil {
			cursorTime, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt)
			if err != nil {
				return nil, fmt.Errorf("照片时间游标无效: %w", err)
			}
			query = query.Where("(images.created_at < ? OR (images.created_at = ? AND images.id < ?))", cursorTime, cursorTime, cursor.ID)
		}
		query = query.Order("images.created_at DESC").Order("images.id DESC")
	case ImageSortSize:
		if cursor != nil {
			query = query.Where("(images.size < ? OR (images.size = ? AND images.id < ?))", cursor.Size, cursor.Size, cursor.ID)
		}
		query = query.Order("images.size DESC").Order("images.id DESC")
	default:
		if cursor != nil {
			if cursor.RatingIsNull {
				query = query.Where("images.personal_rating IS NULL AND images.id < ?", cursor.ID)
			} else {
				query = query.Where("(images.personal_rating < ? OR (images.personal_rating = ? AND images.id < ?) OR images.personal_rating IS NULL)", *cursor.Rating, *cursor.Rating, cursor.ID)
			}
		}
		query = query.Order("images.personal_rating DESC NULLS LAST").Order("images.id DESC")
	}

	var images []models.Image
	if err := query.Limit(limit + 1).Find(&images).Error; err != nil {
		return nil, err
	}
	page := &ImagePage{Images: images}
	if len(images) > limit {
		page.Images = images[:limit]
		last := page.Images[len(page.Images)-1]
		next := &ImageCursor{SortMode: filter.SortMode, ID: last.ID}
		switch filter.SortMode {
		case ImageSortRecent:
			next.CreatedAt = last.CreatedAt.Format(time.RFC3339Nano)
		case ImageSortSize:
			next.Size = last.Size
		default:
			next.RatingIsNull = last.PersonalRating == nil
			if last.PersonalRating != nil {
				rating := *last.PersonalRating
				next.Rating = &rating
			}
		}
		page.NextCursor = next
	}
	return page, nil
}

// GetImageDetail 返回图片（含标签）与已生成的 AI 描述；描述行缺失时为空串。
func (s *ImageLibraryService) GetImageDetail(imageID uint) (*ImageDetail, error) {
	if imageID == 0 {
		return nil, fmt.Errorf("图片 ID 不能为空")
	}
	var image models.Image
	if err := database.DB.Preload("Tags").First(&image, imageID).Error; err != nil {
		return nil, err
	}
	detail := &ImageDetail{Image: image}
	var description models.ImageAIDescription
	err := database.DB.Where("image_id = ?", imageID).First(&description).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err == nil {
		detail.AIDescription = description.Description
	}
	return detail, nil
}

// SetImageFavorite 更新照片收藏状态。
func (s *ImageLibraryService) SetImageFavorite(imageID uint, favorite bool) (*models.Image, error) {
	if imageID == 0 {
		return nil, fmt.Errorf("图片 ID 不能为空")
	}
	result := database.DB.Model(&models.Image{}).Where("id = ?", imageID).Update("is_favorite", favorite)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	return s.getImageWithTags(imageID)
}

// SetImageRating 更新照片个人评分（0–10 半分制，nil 清空，镜像视频侧校验）。
func (s *ImageLibraryService) SetImageRating(imageID uint, rating *float64) (*models.Image, error) {
	if imageID == 0 {
		return nil, fmt.Errorf("图片 ID 不能为空")
	}
	if err := validateRatingValue(rating); err != nil {
		return nil, err
	}
	result := database.DB.Model(&models.Image{}).Where("id = ?", imageID).Update("personal_rating", rating)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	return s.getImageWithTags(imageID)
}

func (s *ImageLibraryService) getImageWithTags(imageID uint) (*models.Image, error) {
	var image models.Image
	if err := database.DB.Preload("Tags").First(&image, imageID).Error; err != nil {
		return nil, err
	}
	return &image, nil
}

// AddTagToImage 为图片添加标签：重复打标幂等，软删除标签与自动标签被拒绝（镜像视频侧）。
func (s *ImageLibraryService) AddTagToImage(imageID uint, tagID uint) error {
	var image models.Image
	var tag models.Tag

	if err := database.DB.First(&image, imageID).Error; err != nil {
		return err
	}
	if err := database.DB.First(&tag, tagID).Error; err != nil {
		return err
	}
	if tag.AutomaticKind != "" {
		return fmt.Errorf("自动标签由应用维护，不能手动添加")
	}

	return database.DB.Model(&image).Association("Tags").Append(&tag)
}

// RemoveTagFromImage 移除图片的标签。
func (s *ImageLibraryService) RemoveTagFromImage(imageID uint, tagID uint) error {
	var image models.Image
	var tag models.Tag

	if err := database.DB.First(&image, imageID).Error; err != nil {
		return err
	}
	if err := database.DB.First(&tag, tagID).Error; err != nil {
		return err
	}
	if tag.AutomaticKind != "" {
		return fmt.Errorf("自动标签由应用维护，不能手动移除")
	}

	return database.DB.Model(&image).Association("Tags").Delete(&tag)
}

// BatchAddTagToImages 批量打标，逐项记录失败原因。
func (s *ImageLibraryService) BatchAddTagToImages(imageIDs []uint, tagID uint) *BatchImageOperationResult {
	result := newBatchImageOperationResult(imageIDs)
	for _, imageID := range imageIDs {
		result.record(imageID, s.AddTagToImage(imageID, tagID))
	}
	return result
}

// BatchRemoveTagFromImages 批量去标，逐项记录失败原因。
func (s *ImageLibraryService) BatchRemoveTagFromImages(imageIDs []uint, tagID uint) *BatchImageOperationResult {
	result := newBatchImageOperationResult(imageIDs)
	for _, imageID := range imageIDs {
		result.record(imageID, s.RemoveTagFromImage(imageID, tagID))
	}
	return result
}
