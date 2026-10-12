package services

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 元数据完整度（D-MC-02）与「缺失某项」筛选（D-MC-03）：两边共用下面这一份缺失判定，
// 完整度页的缺失数与「去处理」打开的列表条数因此同口径。

// ErrInvalidMissingFilter 表示「缺失某项」筛选取值未知。
var ErrInvalidMissingFilter = errors.New("invalid_missing_filter")

// 视频的完整度项。
const (
	MetadataFieldTitle       = "title"
	MetadataFieldYear        = "year"
	MetadataFieldDescription = "description"
	MetadataFieldPoster      = "poster"
	MetadataFieldFanart      = "fanart"
	MetadataFieldPeople      = "people"
	MetadataFieldTags        = "tags"
	MetadataFieldSubtitles   = "subtitles"
	MetadataFieldTechnical   = "technical"
	// MetadataFieldThumbnail 只统计、不进列表筛选：判定要列缩略图目录，不在 SQL 里。
	MetadataFieldThumbnail = "thumbnail"
	// 两个「待处理」项：本地元数据状态，单独展示，不进列表筛选（走现有的本地资料视图）。
	MetadataFieldNFOUpdate = "nfo_update"
	MetadataFieldNFOError  = "nfo_error"
	// 图片专有项：只在 EXIF 解析过的图片里判定。
	MetadataFieldTakenAt = "taken_at"
	MetadataFieldGPS     = "gps"
)

// untaggedVideoSQL 是「没有人工标签」的判定式：带 automatic_kind 的自动分类标签不算（D-PC33）。
// 「未打标签」智能视图与完整度的标签项共用它。
const untaggedVideoSQL = "NOT EXISTS (SELECT 1 FROM video_tags vt JOIN tags t ON t.id = vt.tag_id WHERE vt.video_id = videos.id AND COALESCE(t.automatic_kind, '') = '')"

// videoMissingSQL 是视频各项的缺失判定（详细设计 2.2 的表），列名全部限定（PG 42702）。
// 键也是 LibraryFilter.Missing 的合法取值；缩略图与两个 nfo_* 不在这里。
var videoMissingSQL = map[string]string{
	MetadataFieldTitle:       "videos.display_title = ''",
	MetadataFieldYear:        "videos.year IS NULL",
	MetadataFieldDescription: "videos.description = ''",
	MetadataFieldPoster:      "videos.poster_path = ''",
	MetadataFieldFanart:      "videos.fanart_path = ''",
	MetadataFieldPeople:      "NOT EXISTS (SELECT 1 FROM video_people vpm WHERE vpm.video_id = videos.id)",
	MetadataFieldTags:        untaggedVideoSQL,
	MetadataFieldSubtitles:   "NOT " + hasAnySubtitleSQL,
	MetadataFieldTechnical: "(NOT EXISTS (SELECT 1 FROM video_technical_metadata vtm WHERE vtm.video_id = videos.id AND vtm.probed_at IS NOT NULL)" +
		" AND NOT EXISTS (SELECT 1 FROM media_streams msv WHERE msv.video_id = videos.id AND msv.stream_type = 'video'))",
}

// videoPendingSQL 是两个「待处理」项：本地元数据有更新 / 读取出错。
var videoPendingSQL = map[string]string{
	MetadataFieldNFOUpdate: "EXISTS (SELECT 1 FROM video_local_metadata_states lmu WHERE lmu.video_id = videos.id AND lmu.status = '" + LocalMetadataStateUpdateAvailable + "')",
	MetadataFieldNFOError:  "EXISTS (SELECT 1 FROM video_local_metadata_states lme WHERE lme.video_id = videos.id AND lme.status = '" + LocalMetadataStateError + "')",
}

// imageExifParsedSQL 是拍摄时间与地理位置两项的分母：没解析过 EXIF 的图片不算缺。
const imageExifParsedSQL = "images.exif_parsed_at IS NOT NULL"

// imageMissingSQL 是图片各项的缺失判定，键也是 ImageFilter.Missing 的合法取值。
var imageMissingSQL = map[string]string{
	MetadataFieldTakenAt: "(" + imageExifParsedSQL + " AND images.taken_at IS NULL)",
	MetadataFieldGPS:     "(" + imageExifParsedSQL + " AND images.gps_latitude IS NULL)",
	MetadataFieldTags:    "NOT EXISTS (SELECT 1 FROM image_tags itm JOIN tags t ON t.id = itm.tag_id WHERE itm.image_id = images.id AND COALESCE(t.automatic_kind, '') = '')",
	MetadataFieldPeople:  "NOT EXISTS (SELECT 1 FROM image_people ipm WHERE ipm.image_id = images.id)",
}

// 统计与目录分组时各项的固定顺序（也是界面上的顺序）。
var (
	videoMissingFieldOrder = []string{MetadataFieldTitle, MetadataFieldYear, MetadataFieldDescription, MetadataFieldPoster,
		MetadataFieldFanart, MetadataFieldPeople, MetadataFieldTags, MetadataFieldSubtitles, MetadataFieldTechnical}
	videoPendingFieldOrder = []string{MetadataFieldNFOUpdate, MetadataFieldNFOError}
	imageMissingFieldOrder = []string{MetadataFieldTakenAt, MetadataFieldGPS, MetadataFieldTags, MetadataFieldPeople}
)

// normalizeMissingFilter 校验「缺失某项」取值：空串不筛，其余必须是 allowed 的键。
func normalizeMissingFilter(value string, allowed map[string]string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if _, ok := allowed[value]; !ok {
		return "", fmt.Errorf("%w: %s", ErrInvalidMissingFilter, value)
	}
	return value, nil
}

// MetadataCompletenessItem 是完整度的一项：Missing 条缺这一项，占比的分母是 Total。
type MetadataCompletenessItem struct {
	Field   string `json:"field"`
	Missing int64  `json:"missing"`
	Total   int64  `json:"total"`
}

// MetadataCompleteness 是完整度页签的统计（D-MC-02）。视频在默认可见范围（扫描根 + 排除列表 + 非失效）内，
// 图片在 applyImageVisibility 范围内。
type MetadataCompleteness struct {
	VideoTotal int64                      `json:"video_total"`
	Videos     []MetadataCompletenessItem `json:"videos"`
	// VideoPending 是「待处理」：本地资料有更新 / 读取出错，不算缺失。
	VideoPending []MetadataCompletenessItem `json:"video_pending"`
	ImageTotal   int64                      `json:"image_total"`
	// ImageExifUnparsed 是还没解析过 EXIF 的图片数：它们不进拍摄时间与地理位置的分母。
	ImageExifUnparsed int64                      `json:"image_exif_unparsed"`
	Images            []MetadataCompletenessItem `json:"images"`
}

// MetadataCompletenessDirectory 是「缺得最多的目录」的一行。
type MetadataCompletenessDirectory struct {
	Directory string `gorm:"column:directory" json:"directory"`
	Missing   int64  `gorm:"column:missing_count" json:"missing"`
	Total     int64  `gorm:"column:total_count" json:"total"`
}

// 「缺得最多的目录」的条数：默认 10，最多 100。
const (
	metadataCompletenessDirectoryDefault = 10
	metadataCompletenessDirectoryMax     = 100
)

// MetadataCompletenessService 是元数据完整度统计（D-MC-02），只读。
type MetadataCompletenessService struct {
	thumbnailDir string
}

func NewMetadataCompletenessService(dataDir string) *MetadataCompletenessService {
	return &MetadataCompletenessService{thumbnailDir: filepath.Join(dataDir, "thumbnails")}
}

// GetMetadataCompleteness 统计视频与图片各项的缺失数。视频与图片各用一条 CASE WHEN 聚合，
// 写法同 LibraryStatsService.GetStats；缩略图另列一次目录在 Go 侧求差。
func (s *MetadataCompletenessService) GetMetadataCompleteness() (*MetadataCompleteness, error) {
	if database.DB == nil {
		return nil, errors.New("database is not initialized")
	}
	result := &MetadataCompleteness{}
	videoFields := append(append([]string{}, videoMissingFieldOrder...), videoPendingFieldOrder...)
	videoCounts, err := s.aggregateVideoCounts(videoFields)
	if err != nil {
		return nil, err
	}
	result.VideoTotal = videoCounts[0]
	for index, field := range videoFields {
		item := MetadataCompletenessItem{Field: field, Missing: videoCounts[index+1], Total: result.VideoTotal}
		if _, pending := videoPendingSQL[field]; pending {
			result.VideoPending = append(result.VideoPending, item)
		} else {
			result.Videos = append(result.Videos, item)
		}
	}
	thumbnailMissing, err := s.countMissingThumbnails()
	if err != nil {
		return nil, err
	}
	result.Videos = append(result.Videos, MetadataCompletenessItem{Field: MetadataFieldThumbnail, Missing: thumbnailMissing, Total: result.VideoTotal})

	imageCounts, err := s.aggregateImageCounts()
	if err != nil {
		return nil, err
	}
	result.ImageTotal, result.ImageExifUnparsed = imageCounts[0], imageCounts[1]
	for index, field := range imageMissingFieldOrder {
		total := result.ImageTotal
		if field == MetadataFieldTakenAt || field == MetadataFieldGPS {
			total -= result.ImageExifUnparsed
		}
		result.Images = append(result.Images, MetadataCompletenessItem{Field: field, Missing: imageCounts[index+2], Total: total})
	}
	return result, nil
}

// aggregateVideoCounts 返回 [总数, fields 各项命中数...]。
func (s *MetadataCompletenessService) aggregateVideoCounts(fields []string) ([]int64, error) {
	query, err := visibleVideoQuery(database.DB.Model(&models.Video{}))
	if err != nil {
		return nil, err
	}
	selects := []string{"COUNT(*)"}
	for _, field := range fields {
		selects = append(selects, metadataCountCase(metadataVideoPredicate(field)))
	}
	return scanMetadataCounts(query, selects)
}

// aggregateImageCounts 返回 [总数, 未解析 EXIF 数, imageMissingFieldOrder 各项缺失数...]。
func (s *MetadataCompletenessService) aggregateImageCounts() ([]int64, error) {
	query := applyImageVisibility(database.DB.Model(&models.Image{}), database.DB)
	selects := []string{"COUNT(*)", metadataCountCase("NOT (" + imageExifParsedSQL + ")")}
	for _, field := range imageMissingFieldOrder {
		selects = append(selects, metadataCountCase(imageMissingSQL[field]))
	}
	return scanMetadataCounts(query, selects)
}

func metadataCountCase(predicate string) string {
	return "COALESCE(SUM(CASE WHEN " + predicate + " THEN 1 ELSE 0 END), 0)"
}

// metadataVideoPredicate 返回视频某项的判定（缺失项或待处理项）；未知项返回空串。
func metadataVideoPredicate(field string) string {
	if predicate, ok := videoMissingSQL[field]; ok {
		return predicate
	}
	return videoPendingSQL[field]
}

func scanMetadataCounts(query *gorm.DB, selects []string) ([]int64, error) {
	counts := make([]int64, len(selects))
	targets := make([]any, len(selects))
	for index := range counts {
		targets[index] = &counts[index]
	}
	rows, err := query.Select(strings.Join(selects, ", ")).Rows()
	if err != nil {
		return nil, fmt.Errorf("统计元数据完整度失败: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, fmt.Errorf("统计元数据完整度失败: %w", errors.Join(rows.Err(), sql.ErrNoRows))
	}
	if err := rows.Scan(targets...); err != nil {
		return nil, fmt.Errorf("统计元数据完整度失败: %w", err)
	}
	return counts, rows.Err()
}

// thumbnailVideoIDs 列一次缩略图缓存目录，返回有 <id>.jpg 的视频 ID。目录还不存在等于一张都没有。
func (s *MetadataCompletenessService) thumbnailVideoIDs() (map[uint]struct{}, error) {
	entries, err := os.ReadDir(s.thumbnailDir)
	if errors.Is(err, os.ErrNotExist) {
		return map[uint]struct{}{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取缩略图目录失败: %w", err)
	}
	ids := make(map[uint]struct{}, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jpg") {
			continue
		}
		id, parseErr := strconv.ParseUint(strings.TrimSuffix(name, ".jpg"), 10, 64)
		if parseErr != nil || id == 0 {
			continue
		}
		ids[uint(id)] = struct{}{}
	}
	return ids, nil
}

// visibleVideoDirectories 取可见视频的 ID 与所在目录，缩略图项在 Go 侧和目录列表求差。
func visibleVideoDirectories() ([]metadataVideoDirectoryRow, error) {
	query, err := visibleVideoQuery(database.DB.Model(&models.Video{}))
	if err != nil {
		return nil, err
	}
	var rows []metadataVideoDirectoryRow
	if err := query.Select("videos.id AS id, videos.directory AS directory").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// metadataVideoDirectoryRow 是缩略图求差用的最小视频行。
type metadataVideoDirectoryRow struct {
	ID        uint
	Directory string
}

func (s *MetadataCompletenessService) countMissingThumbnails() (int64, error) {
	present, err := s.thumbnailVideoIDs()
	if err != nil {
		return 0, err
	}
	rows, err := visibleVideoDirectories()
	if err != nil {
		return 0, err
	}
	var missing int64
	for _, row := range rows {
		if _, ok := present[row.ID]; !ok {
			missing++
		}
	}
	return missing, nil
}

// GetMetadataCompletenessByDirectory 返回某一项缺得最多的前 limit 个目录（默认 10），按缺失数降序、目录名升序。
// kind 为 video / image；field 是该媒体的完整度项（视频含缩略图与两个待处理项）。
func (s *MetadataCompletenessService) GetMetadataCompletenessByDirectory(kind, field string, limit int) ([]MetadataCompletenessDirectory, error) {
	if database.DB == nil {
		return nil, errors.New("database is not initialized")
	}
	if limit <= 0 {
		limit = metadataCompletenessDirectoryDefault
	}
	if limit > metadataCompletenessDirectoryMax {
		limit = metadataCompletenessDirectoryMax
	}
	field = strings.TrimSpace(field)
	var query *gorm.DB
	var column, predicate string
	switch strings.TrimSpace(kind) {
	case "video":
		if field == MetadataFieldThumbnail {
			return s.thumbnailDirectories(limit)
		}
		predicate = metadataVideoPredicate(field)
		visible, err := visibleVideoQuery(database.DB.Model(&models.Video{}))
		if err != nil {
			return nil, err
		}
		query, column = visible, "videos.directory"
	case "image":
		predicate = imageMissingSQL[field]
		query, column = applyImageVisibility(database.DB.Model(&models.Image{}), database.DB), "images.directory"
		// 拍摄时间与地理位置只在解析过 EXIF 的图片里算，目录的分母同口径。
		if field == MetadataFieldTakenAt || field == MetadataFieldGPS {
			query = query.Where(imageExifParsedSQL)
		}
	}
	if query == nil || predicate == "" {
		return nil, fmt.Errorf("%w: %s/%s", ErrInvalidMissingFilter, kind, field)
	}
	missingCount := metadataCountCase(predicate)
	rows := []MetadataCompletenessDirectory{}
	err := query.Select(column + " AS directory, COUNT(*) AS total_count, " + missingCount + " AS missing_count").
		Group(column).Having(missingCount + " > 0").
		Order("missing_count DESC").Order(column + " ASC").Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// thumbnailDirectories 按目录汇总缺缩略图的视频（列目录求差，不进 SQL）。
func (s *MetadataCompletenessService) thumbnailDirectories(limit int) ([]MetadataCompletenessDirectory, error) {
	present, err := s.thumbnailVideoIDs()
	if err != nil {
		return nil, err
	}
	rows, err := visibleVideoDirectories()
	if err != nil {
		return nil, err
	}
	byDirectory := map[string]*MetadataCompletenessDirectory{}
	for _, row := range rows {
		group := byDirectory[row.Directory]
		if group == nil {
			group = &MetadataCompletenessDirectory{Directory: row.Directory}
			byDirectory[row.Directory] = group
		}
		group.Total++
		if _, ok := present[row.ID]; !ok {
			group.Missing++
		}
	}
	result := []MetadataCompletenessDirectory{}
	for _, group := range byDirectory {
		if group.Missing > 0 {
			result = append(result, *group)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Missing != result[j].Missing {
			return result[i].Missing > result[j].Missing
		}
		return result[i].Directory < result[j].Directory
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
