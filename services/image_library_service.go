package services

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 照片页排序模式（D-013/D-017）。默认仍是 ImageSortRecent。
const (
	ImageSortRecent = "recent"
	ImageSortSize   = "size"
	ImageSortRating = "rating"
	ImageSortTaken  = "taken"
)

// imageTakenSortKeyExpr 是「拍摄时间」排序键：优先 EXIF 拍摄时间，缺失时回退入库时间。
// images 表没有文件 mtime 列（hash_source_mod_time_ns 只在 dHash 回填后才有值，且是
// 纳秒整数无法与时间戳列 COALESCE），created_at 是唯一恒有值的时间等价物。
const imageTakenSortKeyExpr = "COALESCE(images.taken_at, images.created_at)"

// ImageLibraryService 提供照片页的查询、标签与收藏/评分能力。
type ImageLibraryService struct{}

// NewImageLibraryService 构造照片页数据服务。
func NewImageLibraryService() *ImageLibraryService {
	return &ImageLibraryService{}
}

// ImageFilter 描述照片页的筛选边界。TakenAfter/TakenBefore 是 EXIF 拍摄时间的闭区间，
// 只命中有 taken_at 的图片。
type ImageFilter struct {
	Keyword   string `json:"keyword"`
	Directory string `json:"directory"`
	TagIDs    []uint `json:"tag_ids"`
	// PersonIDs 与 TagIDs 同为 AND 语义：命中的图片必须关联全部所列人物；
	// 空切片等同不筛（D-015）。
	PersonIDs    []uint     `json:"person_ids"`
	FavoriteOnly bool       `json:"favorite_only"`
	MinRating    *float64   `json:"min_rating"`
	MaxRating    *float64   `json:"max_rating"`
	MinSize      int64      `json:"min_size"`
	MaxSize      int64      `json:"max_size"`
	TakenAfter   *time.Time `json:"taken_after,omitempty" ts_type:"string"`
	TakenBefore  *time.Time `json:"taken_before,omitempty" ts_type:"string"`
	SortMode     string     `json:"sort_mode"`
	// AITagState 按 AI 打标状态筛选，取值见 ImageAITagState* 常量；空串表示不筛。
	AITagState string `json:"ai_tag_state"`
}

// ImageFolderCover 是文件夹图集卡片使用的最小封面信息。
type ImageFolderCover struct {
	ID     uint   `json:"id"`
	Name   string `json:"name"`
	Format string `json:"format"`
}

// ImageFolderGroup 是图片库按直属文件夹展示时的一个图集。
// Images.directory 已经是文件所在的直接父目录，因此同一组不会递归包含子目录。
type ImageFolderGroup struct {
	Directory string `json:"directory"`
	Name      string `json:"name"`
	Count     int    `json:"count"`
	// TotalSize 是组内图片字节数之和：文件夹卡上有"删除文件夹"，总得知道会释放多少。
	TotalSize int64              `json:"total_size"`
	Covers    []ImageFolderCover `json:"covers"`
}

// AI 打标筛选取值。pending 是"有候选等着你审"，untagged 覆盖"从未打标""跳过""失败"
// 三种情况，因为它们对用户是同一件事：这张图现在既没有 AI 标签也没有待审候选。
const (
	ImageAITagStateAny      = ""
	ImageAITagStatePending  = "pending"
	ImageAITagStateTagged   = "tagged"
	ImageAITagStateUntagged = "untagged"
)

// ImageCursor 是 SearchImagePage 的稳定分页游标，字段按排序模式取用。
type ImageCursor struct {
	SortMode  string `json:"sort_mode"`
	CreatedAt string `json:"created_at,omitempty"`
	// TakenAt 是 taken 排序键 COALESCE(taken_at, created_at) 的取值，不是原始 EXIF 时间。
	TakenAt      string   `json:"taken_at,omitempty"`
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

// ImageDetail 是单张图片的详情。AI 标签候选不放在这里：候选有自己的审阅接口，
// 且接受/拒绝之后要能单独刷新，塞进详情会逼前端为了一条候选重拉整个详情。
type ImageDetail struct {
	Image models.Image `json:"image"`
	// People 是这张图片当前关联的人物（D-015）。放在详情里而不是列表行上：
	// 照片网格一屏几百张，为人物维护多查一次关系表只在打开单图时值得。
	People []PersonListItem `json:"people"`
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
	filter.Directory = strings.TrimSpace(filter.Directory)
	if filter.Directory != "" {
		filter.Directory = filepath.Clean(filter.Directory)
	}
	filter.TagIDs = uniqueUintIDs(filter.TagIDs)
	sort.Slice(filter.TagIDs, func(i, j int) bool { return filter.TagIDs[i] < filter.TagIDs[j] })
	filter.PersonIDs = uniqueUintIDs(filter.PersonIDs)
	sort.Slice(filter.PersonIDs, func(i, j int) bool { return filter.PersonIDs[i] < filter.PersonIDs[j] })
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
	if filter.TakenAfter != nil && filter.TakenBefore != nil && filter.TakenAfter.After(*filter.TakenBefore) {
		return ImageFilter{}, fmt.Errorf("拍摄时间筛选上限不能早于下限")
	}
	filter.AITagState = strings.TrimSpace(filter.AITagState)
	switch filter.AITagState {
	case ImageAITagStateAny, ImageAITagStatePending, ImageAITagStateTagged, ImageAITagStateUntagged:
	default:
		return ImageFilter{}, fmt.Errorf("不支持的 AI 打标筛选: %s", filter.AITagState)
	}
	filter.SortMode = strings.TrimSpace(filter.SortMode)
	if filter.SortMode == "" {
		filter.SortMode = ImageSortRecent
	}
	switch filter.SortMode {
	case ImageSortRecent, ImageSortSize, ImageSortRating, ImageSortTaken:
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
	if filter.Directory != "" {
		query = query.Where("images.directory = ?", filter.Directory)
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
	if filter.TakenAfter != nil {
		query = query.Where("images.taken_at >= ?", *filter.TakenAfter)
	}
	if filter.TakenBefore != nil {
		query = query.Where("images.taken_at <= ?", *filter.TakenBefore)
	}
	// 「已打标」看的是审批记录而不是 image_tags：手工标签不算 AI 打标的成果。
	switch filter.AITagState {
	case ImageAITagStatePending:
		query = query.Where("EXISTS (SELECT 1 FROM image_ai_tag_candidates c WHERE c.image_id = images.id AND c.status = ?)", models.AITagCandidateStatusPending)
	case ImageAITagStateTagged:
		query = query.Where("EXISTS (SELECT 1 FROM image_ai_tag_approval_records r WHERE r.image_id = images.id)")
	case ImageAITagStateUntagged:
		query = query.
			Where("NOT EXISTS (SELECT 1 FROM image_ai_tag_approval_records r WHERE r.image_id = images.id)").
			Where("NOT EXISTS (SELECT 1 FROM image_ai_tag_candidates c WHERE c.image_id = images.id AND c.status = ?)", models.AITagCandidateStatusPending)
	}
	if len(filter.TagIDs) > 0 {
		subquery := database.DB.Table("image_tags").Select("image_id").
			Where("tag_id IN ?", filter.TagIDs).
			Group("image_id").
			Having("COUNT(DISTINCT tag_id) = ?", len(filter.TagIDs))
		query = query.Where("images.id IN (?)", subquery)
	}
	// 人物筛选与标签同法：分组计数保证 AND 语义，而不是任一命中。
	if len(filter.PersonIDs) > 0 {
		subquery := database.DB.Table("image_people").Select("image_id").
			Where("person_id IN ?", filter.PersonIDs).
			Group("image_id").
			Having("COUNT(DISTINCT person_id) = ?", len(filter.PersonIDs))
		query = query.Where("images.id IN (?)", subquery)
	}
	return query
}

func orderImageQuery(query *gorm.DB, sortMode string) *gorm.DB {
	switch sortMode {
	case ImageSortTaken:
		return query.Order(imageTakenSortKeyExpr + " DESC").Order("images.id DESC")
	case ImageSortSize:
		return query.Order("images.size DESC").Order("images.id DESC")
	case ImageSortRating:
		return query.Order("images.personal_rating DESC NULLS LAST").Order("images.id DESC")
	default:
		return query.Order("images.created_at DESC").Order("images.id DESC")
	}
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
	case ImageSortTaken:
		if cursor.Rating != nil || cursor.RatingIsNull {
			return errors.New("taken 游标包含评分字段")
		}
		if _, err := time.Parse(time.RFC3339Nano, cursor.TakenAt); err != nil {
			return fmt.Errorf("照片拍摄时间游标无效: %w", err)
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

// imageTakenSortKey 复现 imageTakenSortKeyExpr 的取值，用于构造下一页游标。
func imageTakenSortKey(image models.Image) time.Time {
	return imageTakenSortKeyOf(image.TakenAt, image.CreatedAt)
}

// imageTakenSortKeyOf 是 imageTakenSortKeyExpr 在 Go 侧的唯一同义实现，
// 游标构造与时间线分组共用它，避免两处各写一套口径。
func imageTakenSortKeyOf(takenAt *time.Time, createdAt time.Time) time.Time {
	if takenAt != nil {
		return *takenAt
	}
	return createdAt
}

// SearchImagePage 按 DTO 游标稳定分页照片页（AC-4/D-017）：recent=created_at DESC（默认）、
// size=体积 DESC、rating=评分 DESC 且 NULL 排后、taken=拍摄时间 DESC（缺失回退入库时间），
// 均以 id DESC 决胜。
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
	case ImageSortTaken:
		if cursor != nil {
			cursorTime, err := time.Parse(time.RFC3339Nano, cursor.TakenAt)
			if err != nil {
				return nil, fmt.Errorf("照片拍摄时间游标无效: %w", err)
			}
			query = query.Where(
				fmt.Sprintf("(%s < ? OR (%s = ? AND images.id < ?))", imageTakenSortKeyExpr, imageTakenSortKeyExpr),
				cursorTime, cursorTime, cursor.ID)
		}
		query = query.Order(imageTakenSortKeyExpr + " DESC").Order("images.id DESC")
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
		case ImageSortTaken:
			next.TakenAt = imageTakenSortKey(last).Format(time.RFC3339Nano)
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

type imageFolderCoverRow struct {
	ID        uint   `gorm:"column:id"`
	Name      string `gorm:"column:name"`
	Directory string `gorm:"column:directory"`
	Format    string `gorm:"column:format"`
	Size      int64  `gorm:"column:size"`
}

// ListImageFolderGroups 按直属目录汇总当前筛选命中的图片。
// 查询只投影分组和封面所需字段，避免为了文件夹视图把整库图片及标签预加载到内存。
func (s *ImageLibraryService) ListImageFolderGroups(filter ImageFilter) ([]ImageFolderGroup, error) {
	normalized, err := normalizeImageFilter(filter)
	if err != nil {
		return nil, err
	}

	query := applyImageFilter(database.DB.Model(&models.Image{}), normalized).
		Select("images.id, images.name, images.directory, images.format, images.size")
	query = orderImageQuery(query, normalized.SortMode)

	var rows []imageFolderCoverRow
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}

	groups := make([]ImageFolderGroup, 0)
	byDirectory := make(map[string]int)
	for _, row := range rows {
		index, ok := byDirectory[row.Directory]
		if !ok {
			index = len(groups)
			byDirectory[row.Directory] = index
			groups = append(groups, ImageFolderGroup{
				Directory: row.Directory,
				Name:      imageFolderDisplayName(row.Directory),
				Covers:    make([]ImageFolderCover, 0, 4),
			})
		}
		group := &groups[index]
		group.Count++
		group.TotalSize += row.Size
		if len(group.Covers) < 4 {
			group.Covers = append(group.Covers, ImageFolderCover{
				ID: row.ID, Name: row.Name, Format: row.Format,
			})
		}
	}
	return groups, nil
}

func imageFolderDisplayName(directory string) string {
	cleaned := filepath.Clean(strings.TrimSpace(directory))
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return cleaned
	}
	return filepath.Base(cleaned)
}

// ImageTimelineBucket 是时间线分组浏览的一个年月桶（P-013）。Month 为 1–12。
type ImageTimelineBucket struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Count int `json:"count"`
}

// imageTimelineRow 只投影排序键所需的两列：分组计数不需要整行，也不需要 Preload 标签。
type imageTimelineRow struct {
	TakenAt   *time.Time
	CreatedAt time.Time
}

// ListImageTimelineBuckets 按与 imageTakenSortKeyExpr 完全相同的口径
// （COALESCE(taken_at, created_at)）统计年月分组计数，按时间倒序返回；只含活跃行。
//
// 归并放在 Go 侧而不是下推成 SQL 的 GROUP BY，有两个原因：
//   - 年月提取在 SQLite（strftime）与 PostgreSQL（EXTRACT/to_char）上写法不同，下推要写
//     两套分支，而测试只跑得到 SQLite 那套；
//   - Postgres 的 EXTRACT 对 timestamptz 取会话时区，而 EXIF 拍摄时间本就是按本机时区读入的
//     墙钟时间（见 models.Image.TakenAt 注释），按 time.Local 归并才与前端分组头显示一致。
//
// filter.SortMode 只参与校验，不影响分组口径——时间线分组恒按拍摄时间键。
func (s *ImageLibraryService) ListImageTimelineBuckets(filter ImageFilter) ([]ImageTimelineBucket, error) {
	normalized, err := normalizeImageFilter(filter)
	if err != nil {
		return nil, err
	}

	var rows []imageTimelineRow
	query := applyImageFilter(database.DB.Model(&models.Image{}), normalized).
		Select("images.taken_at", "images.created_at")
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}

	type yearMonth struct{ year, month int }
	counts := make(map[yearMonth]int)
	for _, row := range rows {
		key := imageTakenSortKeyOf(row.TakenAt, row.CreatedAt).In(time.Local)
		counts[yearMonth{year: key.Year(), month: int(key.Month())}]++
	}

	buckets := make([]ImageTimelineBucket, 0, len(counts))
	for key, count := range counts {
		buckets = append(buckets, ImageTimelineBucket{Year: key.year, Month: key.month, Count: count})
	}
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i].Year != buckets[j].Year {
			return buckets[i].Year > buckets[j].Year
		}
		return buckets[i].Month > buckets[j].Month
	})
	return buckets, nil
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
	people, err := imagePeopleListItems(imageID)
	if err != nil {
		return nil, err
	}
	return &ImageDetail{Image: image, People: people}, nil
}

// imagePeopleListItems 与视频详情的人物区块同形：按显示名排序，附带两侧活跃计数。
func imagePeopleListItems(imageID uint) ([]PersonListItem, error) {
	var people []models.Person
	if err := database.DB.Model(&models.Person{}).
		Joins("JOIN image_people ON image_people.person_id = people.id").
		Where("image_people.image_id = ?", imageID).
		Order("LOWER(people.display_name) ASC").Order("people.id ASC").
		Find(&people).Error; err != nil {
		return nil, fmt.Errorf("load image people: %w", err)
	}
	personIDs := make([]uint, 0, len(people))
	for _, person := range people {
		personIDs = append(personIDs, person.ID)
	}
	videoCounts, err := activeVideoCountsByPerson(personIDs)
	if err != nil {
		return nil, err
	}
	imageCounts, err := activeImageCountsByPerson(personIDs)
	if err != nil {
		return nil, err
	}
	items := make([]PersonListItem, 0, len(people))
	for _, person := range people {
		items = append(items, personListItemWithCount(person, videoCounts[person.ID], imageCounts[person.ID]))
	}
	return items, nil
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
