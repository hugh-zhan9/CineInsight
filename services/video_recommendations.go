package services

import (
	"fmt"
	"strings"

	"gorm.io/gorm"

	"video-master/database"
	"video-master/models"
)

// 详情页「推荐」（2026-10-27 用户要求）：按上下文分三条——同目录（同一文件夹里的其他片）、
// 同标签（共享非自动标签，也就是这个应用里的「类型」）与同人物（前端仍沿用 GetPersonDetail 取）。
// 这里只算前两条。三条都排除本片与同一版本组的其他版本（版本在「版本」一节里，不重复推荐），
// 候选统一走默认片库的可见范围（扫描根、黑名单、非失效、未删除，visibleVideoQuery）。
const videoRecommendationLimit = 8

// VideoRecommendationItem 是一部推荐视频；同标签一条另给出共享的标签名，卡片副标题用它。
type VideoRecommendationItem struct {
	Video      models.Video `json:"video"`
	SharedTags []string     `json:"shared_tags"`
}

type VideoRecommendations struct {
	Directory []VideoRecommendationItem `json:"directory"`
	Tags      []VideoRecommendationItem `json:"tags"`
}

// GetVideoRecommendations 返回详情页推荐里由后端算的两组。视频不存在时把 GORM 的 not found 原样返回，
// 与 GetVideoDetails 一致。两组都总是数组（没有推荐时是空数组），前端不必再判 null。
func (s *VideoService) GetVideoRecommendations(videoID uint) (*VideoRecommendations, error) {
	var source models.Video
	if err := database.DB.Preload("Tags").First(&source, videoID).Error; err != nil {
		return nil, err
	}
	siblingIDs, err := versionSiblingIDs(videoID)
	if err != nil {
		return nil, err
	}
	excluded := map[uint]bool{videoID: true}
	for _, id := range siblingIDs {
		excluded[id] = true
	}
	result := &VideoRecommendations{
		Directory: []VideoRecommendationItem{},
		Tags:      []VideoRecommendationItem{},
	}
	if result.Directory, err = directoryRecommendations(source, excluded); err != nil {
		return nil, err
	}
	// 同标签一条不再重复同目录已经给出的片（同一张卡出现在相邻两条里像是坏了）。
	for _, item := range result.Directory {
		excluded[item.Video.ID] = true
	}
	if result.Tags, err = tagRecommendations(source, excluded); err != nil {
		return nil, err
	}
	return result, nil
}

// versionSiblingIDs 是同一版本组里的其他视频；窗口只覆盖已分组的成员，本片自己由调用方单独排除。
func versionSiblingIDs(videoID uint) ([]uint, error) {
	ids := []uint{}
	err := database.DB.Table("video_version_members").
		Where("group_id IN (SELECT group_id FROM video_version_members WHERE video_id = ?)", videoID).
		Pluck("video_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("读取版本组成员失败: %w", err)
	}
	return ids, nil
}

// directoryRecommendations：同一个 videos.directory 里的其他片，按文件名排（剧集保持集数顺序）。
func directoryRecommendations(source models.Video, excluded map[uint]bool) ([]VideoRecommendationItem, error) {
	items := []VideoRecommendationItem{}
	directory := strings.TrimSpace(source.Directory)
	if directory == "" {
		return items, nil
	}
	query := database.DB.Model(&models.Video{}).Where("videos.directory = ?", directory)
	query, err := visibleVideoQuery(query)
	if err != nil {
		return nil, err
	}
	query = excludeRecommendedVideos(query, excluded)
	var videos []models.Video
	if err := query.Order("LOWER(videos.name) ASC").Order("videos.id ASC").
		Limit(videoRecommendationLimit).Find(&videos).Error; err != nil {
		return nil, fmt.Errorf("读取同目录推荐失败: %w", err)
	}
	for _, video := range videos {
		items = append(items, VideoRecommendationItem{Video: video, SharedTags: []string{}})
	}
	return items, nil
}

// tagRecommendations：与本片共享非自动标签的片，共享得多的在前；同数按评分（未评分排后）、
// 入库时间与 ID 排。共享的标签名一并带出，卡片副标题写「科幻 · 剧情」。
func tagRecommendations(source models.Video, excluded map[uint]bool) ([]VideoRecommendationItem, error) {
	items := []VideoRecommendationItem{}
	tagIDs := make([]uint, 0, len(source.Tags))
	for _, tag := range source.Tags {
		if tag.ID != 0 && strings.TrimSpace(tag.AutomaticKind) == "" {
			tagIDs = append(tagIDs, tag.ID)
		}
	}
	if len(tagIDs) == 0 {
		return items, nil
	}
	query := database.DB.Model(&models.Video{}).
		Select("videos.id AS id, COUNT(*) AS shared").
		Joins("JOIN video_tags vt ON vt.video_id = videos.id").
		Joins("JOIN tags t ON t.id = vt.tag_id").
		Where("vt.tag_id IN ?", tagIDs).
		Where("t.deleted_at IS NULL").
		Where("COALESCE(t.automatic_kind, '') = ''")
	query, err := visibleVideoQuery(query)
	if err != nil {
		return nil, err
	}
	query = excludeRecommendedVideos(query, excluded)
	var ranked []struct {
		ID     uint
		Shared int64
	}
	if err := query.Group("videos.id").
		Order("shared DESC").
		Order("CASE WHEN videos.personal_rating IS NULL THEN 1 ELSE 0 END ASC").
		Order("videos.personal_rating DESC").
		Order("videos.created_at DESC").
		Order("videos.id DESC").
		Limit(videoRecommendationLimit).Scan(&ranked).Error; err != nil {
		return nil, fmt.Errorf("读取同标签推荐失败: %w", err)
	}
	if len(ranked) == 0 {
		return items, nil
	}
	ids := make([]uint, 0, len(ranked))
	for _, row := range ranked {
		ids = append(ids, row.ID)
	}
	sharedNames, err := sharedRecommendationTagNames(ids, tagIDs)
	if err != nil {
		return nil, err
	}
	var videos []models.Video
	if err := database.DB.Preload("Tags").Where("id IN ?", ids).Find(&videos).Error; err != nil {
		return nil, fmt.Errorf("读取同标签推荐失败: %w", err)
	}
	byID := make(map[uint]models.Video, len(videos))
	for _, video := range videos {
		byID[video.ID] = video
	}
	for _, id := range ids {
		video, ok := byID[id]
		if !ok {
			continue
		}
		names := sharedNames[id]
		if names == nil {
			names = []string{}
		}
		items = append(items, VideoRecommendationItem{Video: video, SharedTags: names})
	}
	return items, nil
}

// sharedRecommendationTagNames 读这批候选与本片的共享标签名；按标签 ID 排，同一部片的理由顺序稳定。
func sharedRecommendationTagNames(videoIDs, tagIDs []uint) (map[uint][]string, error) {
	var rows []struct {
		VideoID uint
		Name    string
	}
	err := database.DB.Table("video_tags").
		Select("video_tags.video_id AS video_id, tags.name AS name").
		Joins("JOIN tags ON tags.id = video_tags.tag_id").
		Where("video_tags.video_id IN ? AND video_tags.tag_id IN ?", videoIDs, tagIDs).
		Where("tags.deleted_at IS NULL").
		Where("COALESCE(tags.automatic_kind, '') = ''").
		Order("tags.id ASC").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("读取共享标签失败: %w", err)
	}
	names := make(map[uint][]string, len(videoIDs))
	for _, row := range rows {
		names[row.VideoID] = append(names[row.VideoID], row.Name)
	}
	return names, nil
}

// excludeRecommendedVideos 按 ID 排除本片与版本组成员；没有要排除的就不加条件（空 IN 在部分库里没有意义）。
func excludeRecommendedVideos(query *gorm.DB, excluded map[uint]bool) *gorm.DB {
	if len(excluded) == 0 {
		return query
	}
	ids := make([]uint, 0, len(excluded))
	for id := range excluded {
		ids = append(ids, id)
	}
	return query.Where("videos.id NOT IN ?", ids)
}
