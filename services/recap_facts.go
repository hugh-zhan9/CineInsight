package services

import (
	"time"
	"video-master/models"

	"gorm.io/gorm"
)

// recapPreviousCounts 数对比窗口里的播放次数、不同视频数与图片查看次数。
func recapPreviousCounts(db *gorm.DB, start, end time.Time) (plays, videos, views int64, err error) {
	var row struct {
		Plays  int64
		Videos int64
	}
	if !start.Before(end) {
		return 0, 0, 0, nil
	}
	if err = db.Model(&models.PlayEvent{}).Select("COUNT(*) AS plays, COUNT(DISTINCT video_id) AS videos").
		Where("played_at >= ? AND played_at < ?", start, end).Scan(&row).Error; err != nil {
		return
	}
	err = db.Model(&models.ImageViewEvent{}).Where("viewed_at >= ? AND viewed_at < ?", start, end).Count(&views).Error
	return row.Plays, row.Videos, views, err
}

// recapChange 是相对上一期的百分比变化；上一期为 0 时没有可比的基数，返回 nil。
func recapChange(current, previous int64) *float64 {
	if previous <= 0 {
		return nil
	}
	change := float64(current-previous) * 100 / float64(previous)
	return &change
}

// recapPriorVideoIDs 返回期内有非 legacy 播放、且在本期开始之前就已经有任意事件的视频（重温候选）。
// 一条按时间窗与 (video_id, played_at) 索引走的查询，不把候选 ID 搬回 Go 再拼 IN。
func recapPriorVideoIDs(db *gorm.DB, start, end time.Time) (map[uint]bool, error) {
	var ids []uint
	window := db.Session(&gorm.Session{NewDB: true}).Model(&models.PlayEvent{}).Select("video_id").
		Where("played_at >= ? AND played_at < ? AND source <> ?", start, end, models.PlayEventSourceLegacy)
	if err := db.Model(&models.PlayEvent{}).Distinct("video_id").
		Where("played_at < ? AND video_id IN (?)", start, window).Pluck("video_id", &ids).Error; err != nil {
		return nil, err
	}
	prior := make(map[uint]bool, len(ids))
	for _, id := range ids {
		prior[id] = true
	}
	return prior, nil
}

// recapFavoriteCounts 数 favorited_at 落在期内、至今仍在收藏的视频与图片（取消收藏会清空该列）。
func recapFavoriteCounts(db *gorm.DB, start, end time.Time) (videos, images int64, err error) {
	if err = db.Model(&models.Video{}).Where("is_favorite = ? AND favorited_at >= ? AND favorited_at < ?", true, start, end).
		Count(&videos).Error; err != nil {
		return
	}
	err = db.Model(&models.Image{}).Where("is_favorite = ? AND favorited_at >= ? AND favorited_at < ?", true, start, end).
		Count(&images).Error
	return
}

// recapDiaryRated 数观看日期落在期内且带评分的观影日记条目（watched_on 是本地日期串，按字符串区间比较）。
func recapDiaryRated(db *gorm.DB, start, end time.Time) (int64, error) {
	var count int64
	err := db.Model(&models.ViewingDiaryEntry{}).
		Where("rating IS NOT NULL AND watched_on >= ? AND watched_on < ?", start.Format("2006-01-02"), end.Format("2006-01-02")).
		Count(&count).Error
	return count, err
}
