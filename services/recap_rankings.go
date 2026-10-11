package services

import (
	"time"

	"gorm.io/gorm"
)

// 标签 / 人物 / 作品集排行：在数据库里按时间窗把播放事件连到关系表上分组计数，只取前 N 行。
// Weight 是带它的视频在期内的播放次数之和（同一部片播三次算三），并列按 ID 升序。
// 标签排除带 automatic_kind 的自动标签（COALESCE：老库里该列可能是 NULL）。

type recapRankRow struct {
	ID     uint
	Name   string
	Extra  string
	Weight int64
	Videos int64
}

func recapRankQuery(db *gorm.DB, start, end time.Time) *gorm.DB {
	return db.Table("play_events").
		Where("play_events.played_at >= ? AND play_events.played_at < ?", start, end)
}

func recapTagRanks(db *gorm.DB, start, end time.Time, limit int) ([]RecapTagRank, error) {
	var rows []recapRankRow
	err := recapRankQuery(db, start, end).
		Select("tags.id AS id, tags.name AS name, tags.color AS extra, COUNT(*) AS weight, COUNT(DISTINCT play_events.video_id) AS videos").
		Joins("JOIN video_tags ON video_tags.video_id = play_events.video_id").
		Joins("JOIN tags ON tags.id = video_tags.tag_id").
		Where("tags.deleted_at IS NULL AND COALESCE(tags.automatic_kind, '') = ''").
		Group("tags.id, tags.name, tags.color").Order("weight DESC").Order("tags.id ASC").Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	ranks := make([]RecapTagRank, 0, len(rows))
	for _, row := range rows {
		ranks = append(ranks, RecapTagRank{Tag: RecapTag{ID: row.ID, Name: row.Name, Color: row.Extra}, Weight: row.Weight, Videos: row.Videos})
	}
	return ranks, nil
}

func recapPeopleRanks(db *gorm.DB, start, end time.Time, limit int) ([]RecapEntityRank, error) {
	var rows []recapRankRow
	err := recapRankQuery(db, start, end).
		Select("people.id AS id, people.display_name AS name, people.avatar_path AS extra, COUNT(*) AS weight, COUNT(DISTINCT play_events.video_id) AS videos").
		Joins("JOIN video_people ON video_people.video_id = play_events.video_id").
		Joins("JOIN people ON people.id = video_people.person_id").
		Group("people.id, people.display_name, people.avatar_path").Order("weight DESC").Order("people.id ASC").Limit(limit).
		Scan(&rows).Error
	return recapEntityRanks(rows, "person-avatar"), err
}

func recapCollectionRanks(db *gorm.DB, start, end time.Time, limit int) ([]RecapEntityRank, error) {
	var rows []recapRankRow
	err := recapRankQuery(db, start, end).
		Select("media_collections.id AS id, media_collections.name AS name, media_collections.cover_path AS extra, COUNT(*) AS weight, COUNT(DISTINCT play_events.video_id) AS videos").
		Joins("JOIN collection_videos ON collection_videos.video_id = play_events.video_id").
		Joins("JOIN media_collections ON media_collections.id = collection_videos.collection_id").
		Where("media_collections.deleted_at IS NULL").
		Group("media_collections.id, media_collections.name, media_collections.cover_path").Order("weight DESC").Order("media_collections.id ASC").Limit(limit).
		Scan(&rows).Error
	return recapEntityRanks(rows, "collection-cover"), err
}

func recapEntityRanks(rows []recapRankRow, route string) []RecapEntityRank {
	ranks := make([]RecapEntityRank, 0, len(rows))
	for _, row := range rows {
		ranks = append(ranks, RecapEntityRank{Entity: RecapEntity{ID: row.ID, Name: row.Name, ImageURL: recapEntityURL(route, row.ID, row.Extra)},
			Weight: row.Weight, Videos: row.Videos})
	}
	return ranks
}
