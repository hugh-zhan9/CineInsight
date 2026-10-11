package services

import (
	"video-master/models"

	"gorm.io/gorm"
)

// 已删除（进回收站）或已移除（路径失效）的视频与图片没有预览图，回顾不再展示它们：
// 最爱、排行、新发现样本、每月之最、实时流水与正在播放只取仍可用的媒体。总数、习惯分布与
// 新发现/重温计数是期内的事实，照常统计。

const recapAvailabilityChunk = 500

// recapAvailableIDs 返回 ids 中未删除且未失效的媒体 ID（按 500 个一批查询，Model 自带软删过滤）。
func recapAvailableIDs(db *gorm.DB, model any, ids []uint) (map[uint]bool, error) {
	available := make(map[uint]bool, len(ids))
	for start := 0; start < len(ids); start += recapAvailabilityChunk {
		chunk := ids[start:min(start+recapAvailabilityChunk, len(ids))]
		var found []uint
		if err := db.Model(model).Where("id IN ? AND is_stale = ?", chunk, false).Pluck("id", &found).Error; err != nil {
			return nil, err
		}
		for _, id := range found {
			available[id] = true
		}
	}
	return available, nil
}

// loadAvailability 为期内出现过的全部视频与图片一次性判定可展示性。
func (b *recapBuilder) loadAvailability() error {
	var err error
	if b.availableVideos, err = recapAvailableIDs(b.db, &models.Video{}, recapTallyIDs(b.tally.videos)); err != nil {
		return err
	}
	b.availableImages, err = recapAvailableIDs(b.db, &models.Image{}, recapTallyIDs(b.tally.images))
	return err
}

func recapTallyIDs(items map[uint]*recapMediaTally) []uint {
	ids := make([]uint, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	return ids
}

// recapOnlyAvailable 保持原有排名次序，只去掉不可展示的媒体。
func recapOnlyAvailable(items []*recapMediaTally, available map[uint]bool) []*recapMediaTally {
	kept := make([]*recapMediaTally, 0, len(items))
	for _, item := range items {
		if available[item.id] {
			kept = append(kept, item)
		}
	}
	return kept
}
