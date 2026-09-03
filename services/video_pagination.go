package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// GetAllVideos 获取所有视频（已废弃，使用分页方式）
func (s *VideoService) GetAllVideos() ([]models.Video, error) {
	var videos []models.Video
	err := database.DB.Preload("Tags").Order("created_at desc").Limit(50).Find(&videos).Error
	return videos, err
}

// GetVideosByIDs 按传入顺序返回这些视频（含标签）。已删除或查不到的 ID 直接跳过，
// 调用方据此知道哪些条目已经不在库里。
func (s *VideoService) GetVideosByIDs(ids []uint) ([]models.Video, error) {
	orderedIDs := uniqueUintIDs(ids)
	if len(orderedIDs) == 0 {
		return []models.Video{}, nil
	}
	var found []models.Video
	if err := database.DB.Preload("Tags").Where("id IN ?", orderedIDs).Find(&found).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]models.Video, len(found))
	for _, video := range found {
		byID[video.ID] = video
	}
	ordered := make([]models.Video, 0, len(found))
	for _, id := range orderedIDs {
		if video, ok := byID[id]; ok {
			ordered = append(ordered, video)
		}
	}
	return ordered, nil
}

// getPlayWeight 获取播放权重配置
func (s *VideoService) getPlayWeight() (float64, error) {
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return 0, fmt.Errorf("获取设置失败: %w", err)
	}
	w := settings.PlayWeight
	if w < 0.1 {
		w = 0.1
	}
	return w, nil
}

// scoreExprForTable 返回播放分数的 SQL 表达式片段，使用 fmt.Sprintf 将权重直接嵌入 SQL，
// 避免在复合 WHERE 条件中反复传递 ? 占位符导致参数计数出错。
func scoreExprForTable(tablePrefix string, playWeight float64) string {
	return fmt.Sprintf("(%splay_count * %g + %srandom_play_count)", tablePrefix, playWeight, tablePrefix)
}

// applyCursorCondition 为查询添加游标分页的 WHERE 条件
// 排序规则：score ASC, size DESC, id DESC
func applyCursorCondition(query *gorm.DB, scoreSql string, cursorScore float64, cursorSize int64, cursorID uint, tablePrefix string) *gorm.DB {
	if cursorID == 0 {
		return query
	}
	sizeCol := tablePrefix + "size"
	idCol := tablePrefix + "id"
	// 三元组游标条件：(score > ?) OR (score = ? AND size < ?) OR (score = ? AND size = ? AND id < ?)
	cond := fmt.Sprintf(
		"(%s > ?) OR (%s = ? AND %s < ?) OR (%s = ? AND %s = ? AND %s < ?)",
		scoreSql, scoreSql, sizeCol, scoreSql, sizeCol, idCol,
	)
	return query.Where(cond, cursorScore, cursorScore, cursorSize, cursorScore, cursorSize, cursorID)
}

// GetVideosPaginated 游标分页获取视频（按概率优先排序）
func (s *VideoService) GetVideosPaginated(cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	playWeight, err := s.getPlayWeight()
	if err != nil {
		return nil, err
	}

	var videos []models.Video
	scoreSql := scoreExprForTable("videos.", playWeight)
	query := database.DB.Model(&models.Video{}).Preload("Tags").
		Order(scoreSql + " ASC").
		Order("videos.size desc").
		Order("videos.id desc")

	query = applyCursorCondition(query, scoreSql, cursorScore, cursorSize, cursorID, "videos.")

	err = query.Limit(limit).Find(&videos).Error
	return videos, err
}

// SearchVideos 搜索视频（按名称）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideos(keyword string, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	return s.SearchVideosWithFilters(keyword, nil, 0, 0, 0, 0, cursorScore, cursorSize, cursorID, limit)
}

// SearchVideosByTags 按标签搜索（多选 AND）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideosByTags(tagIDs []uint, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	return s.SearchVideosWithFilters("", tagIDs, 0, 0, 0, 0, cursorScore, cursorSize, cursorID, limit)
}

// SearchVideosWithFilters 组合搜索（关键词 + 标签 + 体积 + 分辨率 AND）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideosWithFilters(keyword string, tagIDs []uint, minSize, maxSize int64, minHeight, maxHeight int, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	var videos []models.Video
	playWeight, err := s.getPlayWeight()
	if err != nil {
		return nil, err
	}

	scoreSql := scoreExprForTable("videos.", playWeight)
	query := database.DB.Model(&models.Video{}).Preload("Tags").
		Order(scoreSql + " ASC").
		Order("videos.size desc").
		Order("videos.id desc")

	if strings.TrimSpace(keyword) != "" {
		kw := "%" + strings.TrimSpace(keyword) + "%"
		query = query.Where("(videos.name LIKE ? OR videos.path LIKE ?)", kw, kw)
	}

	if minSize > 0 {
		query = query.Where("videos.size >= ?", minSize)
	}
	if maxSize > 0 {
		query = query.Where("videos.size < ?", maxSize)
	}
	if minHeight > 0 {
		query = query.Where("videos.height >= ?", minHeight)
	}
	if maxHeight > 0 {
		query = query.Where("videos.height <= ?", maxHeight)
	}

	if len(tagIDs) > 0 {
		query = query.Joins("JOIN video_tags ON video_tags.video_id = videos.id").
			Where("video_tags.tag_id IN ?", tagIDs)
		query = query.Group("videos.id").
			Having("COUNT(DISTINCT video_tags.tag_id) = ?", len(tagIDs))
	}

	query = applyCursorCondition(query, scoreSql, cursorScore, cursorSize, cursorID, "videos.")

	err = query.Limit(limit).Find(&videos).Error
	return videos, err
}

// GetVideosByDirectory 按目录获取视频记录
func (s *VideoService) GetVideosByDirectory(dir string) ([]models.Video, error) {
	var videos []models.Video
	cleanDir := filepath.Clean(strings.TrimSpace(dir))
	childPrefix := escapeSQLLike(cleanDir+string(os.PathSeparator)) + "%"
	err := database.DB.Preload("Tags").
		Where("directory = ? OR directory LIKE ? ESCAPE '\\'", cleanDir, childPrefix).
		Order("id desc").
		Find(&videos).Error
	return videos, err
}

func escapeSQLLike(input string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	)
	return replacer.Replace(input)
}
