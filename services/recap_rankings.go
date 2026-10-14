package services

import (
	"time"

	"gorm.io/gorm"
)

// 标签 / 人物 / 作品集排行：在数据库里按时间窗把播放事件连到关系表上分组计数，只取 [offset, offset+limit) 那几行，
// 另数一次分组总数（页面据此知道排行是否截断，「查看全部」按同一顺序分页）。
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

// recapRankPage 在 join 好的查询上先数不同分组键的个数，再按权重降序、键升序取一页。
// columns 是「键, 名称, 附加列」三列（按 id / name / extra 取别名），也是分组列。
func recapRankPage(joined func() *gorm.DB, columns [3]string, offset, limit int) ([]recapRankRow, int64, error) {
	key := columns[0]
	var total int64
	if err := joined().Select("COUNT(DISTINCT " + key + ")").Scan(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []recapRankRow{}
	if total == 0 || int64(offset) >= total {
		return rows, total, nil
	}
	err := joined().Select(columns[0] + " AS id, " + columns[1] + " AS name, " + columns[2] +
		" AS extra, COUNT(*) AS weight, COUNT(DISTINCT play_events.video_id) AS videos").
		Group(columns[0] + ", " + columns[1] + ", " + columns[2]).Order("weight DESC").Order(key + " ASC").
		Offset(offset).Limit(limit).Scan(&rows).Error
	return rows, total, err
}

func recapTagRanks(db *gorm.DB, start, end time.Time, offset, limit int) ([]RecapTagRank, int64, error) {
	rows, total, err := recapRankPage(func() *gorm.DB {
		return recapRankQuery(db, start, end).
			Joins("JOIN video_tags ON video_tags.video_id = play_events.video_id").
			Joins("JOIN tags ON tags.id = video_tags.tag_id").
			Where("tags.deleted_at IS NULL AND COALESCE(tags.automatic_kind, '') = ''")
	}, [3]string{"tags.id", "tags.name", "tags.color"}, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	ranks := make([]RecapTagRank, 0, len(rows))
	for _, row := range rows {
		ranks = append(ranks, RecapTagRank{Tag: RecapTag{ID: row.ID, Name: row.Name, Color: row.Extra}, Weight: row.Weight, Videos: row.Videos})
	}
	return ranks, total, nil
}

func recapPeopleRanks(db *gorm.DB, start, end time.Time, offset, limit int) ([]RecapEntityRank, int64, error) {
	rows, total, err := recapRankPage(func() *gorm.DB {
		return recapRankQuery(db, start, end).
			Joins("JOIN video_people ON video_people.video_id = play_events.video_id").
			Joins("JOIN people ON people.id = video_people.person_id")
	}, [3]string{"people.id", "people.display_name", "people.avatar_path"}, offset, limit)
	return recapEntityRanks(rows, "person-avatar"), total, err
}

func recapCollectionRanks(db *gorm.DB, start, end time.Time, offset, limit int) ([]RecapEntityRank, int64, error) {
	rows, total, err := recapRankPage(func() *gorm.DB {
		return recapRankQuery(db, start, end).
			Joins("JOIN collection_videos ON collection_videos.video_id = play_events.video_id").
			Joins("JOIN media_collections ON media_collections.id = collection_videos.collection_id").
			Where("media_collections.deleted_at IS NULL")
	}, [3]string{"media_collections.id", "media_collections.name", "media_collections.cover_path"}, offset, limit)
	return recapEntityRanks(rows, "collection-cover"), total, err
}

func recapEntityRanks(rows []recapRankRow, route string) []RecapEntityRank {
	ranks := make([]RecapEntityRank, 0, len(rows))
	for _, row := range rows {
		ranks = append(ranks, RecapEntityRank{Entity: RecapEntity{ID: row.ID, Name: row.Name, ImageURL: recapEntityURL(route, row.ID, row.Extra)},
			Weight: row.Weight, Videos: row.Videos})
	}
	return ranks
}
