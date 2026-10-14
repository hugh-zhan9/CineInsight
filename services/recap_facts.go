package services

import (
	"sort"
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

// recapNewFavorites 是 favorited_at 落在期内、至今仍在收藏的视频或图片（取消收藏会清空该列；Model 自带软删过滤）。
func recapNewFavorites(db *gorm.DB, model any, start, end time.Time) *gorm.DB {
	return db.Model(model).Where("is_favorite = ? AND favorited_at >= ? AND favorited_at < ?", true, start, end)
}

// recapFavoriteCounts 数本期新收藏的视频与图片。
func recapFavoriteCounts(db *gorm.DB, start, end time.Time) (videos, images int64, err error) {
	if err = recapNewFavorites(db, &models.Video{}, start, end).Count(&videos).Error; err != nil {
		return
	}
	err = recapNewFavorites(db, &models.Image{}, start, end).Count(&images).Error
	return
}

type recapNewFavoriteRow struct {
	ID           uint
	Name         string
	DisplayTitle string
	FavoritedAt  time.Time
	IsStale      bool
}

// recapNewFavoriteList 取本期新收藏的视频与图片，按收藏时间从近到远合并后取 [offset, offset+limit)；
// 同一时刻视频在前、再按 ID 大的在前。两边各取前 offset+limit 个再合并，结果与整体排序后取同一段相同。
// onlyAvailable 时只取未失效的（页面样本）；否则含已失效的、Available=false（「查看全部」，与计数同口径）。
func recapNewFavoriteList(db *gorm.DB, start, end time.Time, offset, limit int, onlyAvailable bool, loc *time.Location) ([]RecapNewFavorite, error) {
	var videos, images []recapNewFavoriteRow
	recent := func(model any, columns string, rows *[]recapNewFavoriteRow) error {
		query := recapNewFavorites(db, model, start, end)
		if onlyAvailable {
			query = query.Where("is_stale = ?", false)
		}
		return query.Select(columns).Order("favorited_at DESC").Order("id DESC").Limit(offset + limit).Scan(rows).Error
	}
	if err := recent(&models.Video{}, "id, name, display_title, favorited_at, is_stale", &videos); err != nil {
		return nil, err
	}
	if err := recent(&models.Image{}, "id, name, favorited_at, is_stale", &images); err != nil {
		return nil, err
	}
	samples := make([]RecapNewFavorite, 0, len(videos)+len(images))
	for _, row := range videos {
		samples = append(samples, RecapNewFavorite{Kind: RecapActivityVideo, ID: row.ID, Title: recapVideoTitle(row.DisplayTitle, row.Name),
			FavoritedAt: row.FavoritedAt.In(loc), Available: !row.IsStale})
	}
	for _, row := range images {
		samples = append(samples, RecapNewFavorite{Kind: RecapActivityImage, ID: row.ID, Title: row.Name, FavoritedAt: row.FavoritedAt.In(loc), Available: !row.IsStale})
	}
	sort.SliceStable(samples, func(i, j int) bool {
		a, b := samples[i], samples[j]
		if !a.FavoritedAt.Equal(b.FavoritedAt) {
			return a.FavoritedAt.After(b.FavoritedAt)
		}
		if a.Kind != b.Kind {
			return a.Kind == RecapActivityVideo
		}
		return a.ID > b.ID
	})
	return samples[min(offset, len(samples)):min(offset+limit, len(samples))], nil
}

// recapDiaryRatedEntries 是观看日期落在期内且带评分的观影日记条目（watched_on 是本地日期串，按字符串区间比较）。
func recapDiaryRatedEntries(db *gorm.DB, start, end time.Time) *gorm.DB {
	return db.Model(&models.ViewingDiaryEntry{}).
		Where("rating IS NOT NULL AND watched_on >= ? AND watched_on < ?", start.Format("2006-01-02"), end.Format("2006-01-02"))
}

// recapDiaryRated 数本期带评分的观影日记条目。
func recapDiaryRated(db *gorm.DB, start, end time.Time) (int64, error) {
	var count int64
	err := recapDiaryRatedEntries(db, start, end).Count(&count).Error
	return count, err
}

// recapDiaryRatedList 取本期带评分的观影日记 [offset, offset+limit)，按观看日期从近到远、同一天 ID 大的在前
// （走 idx_diary_day），并标出关联的视频还能不能打开。
func recapDiaryRatedList(db *gorm.DB, start, end time.Time, offset, limit int) ([]RecapDiaryRated, error) {
	var rows []struct {
		ID        uint
		Title     string
		Rating    *float64
		WatchedOn string
		VideoID   *uint
	}
	if err := recapDiaryRatedEntries(db, start, end).Select("id, title, rating, watched_on, video_id").
		Order("watched_on DESC").Order("id DESC").Offset(offset).Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	videoIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		if row.VideoID != nil {
			videoIDs = append(videoIDs, *row.VideoID)
		}
	}
	available, err := recapAvailableIDs(db, &models.Video{}, videoIDs)
	if err != nil {
		return nil, err
	}
	samples := make([]RecapDiaryRated, 0, len(rows))
	for _, row := range rows {
		sample := RecapDiaryRated{ID: row.ID, Title: row.Title, WatchedOn: row.WatchedOn, VideoID: row.VideoID}
		if row.Rating != nil {
			sample.Rating = *row.Rating
		}
		if row.VideoID != nil {
			sample.VideoAvailable = available[*row.VideoID]
		}
		samples = append(samples, sample)
	}
	return samples, nil
}
