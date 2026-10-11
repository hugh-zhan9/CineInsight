package services

import (
	"time"
	"video-master/models"

	"gorm.io/gorm"
)

// 本期最爱的标签 / 人物 / 作品集，以及「相关视频还有」。标签只取非自动标签，与排行同一口径。

func recapVideoTags(db *gorm.DB, videoID uint) ([]RecapTag, error) {
	tags := []RecapTag{}
	err := db.Table("video_tags").Select("tags.id AS id, tags.name AS name, tags.color AS color").
		Joins("JOIN tags ON tags.id = video_tags.tag_id").
		Where("video_tags.video_id = ? AND tags.deleted_at IS NULL AND COALESCE(tags.automatic_kind, '') = ''", videoID).
		Order("tags.id ASC").Scan(&tags).Error
	return tags, err
}

func recapImageTags(db *gorm.DB, imageID uint) ([]RecapTag, error) {
	tags := []RecapTag{}
	err := db.Table("image_tags").Select("tags.id AS id, tags.name AS name, tags.color AS color").
		Joins("JOIN tags ON tags.id = image_tags.tag_id").
		Where("image_tags.image_id = ? AND tags.deleted_at IS NULL AND COALESCE(tags.automatic_kind, '') = ''", imageID).
		Order("tags.id ASC").Scan(&tags).Error
	return tags, err
}

// recapPeople 读一部视频（relation=video_people, column=video_id）或一张图片的人物。
func recapPeople(db *gorm.DB, relation, column string, mediaID uint) ([]RecapEntity, error) {
	var rows []recapRankRow
	if err := db.Table(relation).Select("people.id AS id, people.display_name AS name, people.avatar_path AS extra").
		Joins("JOIN people ON people.id = "+relation+".person_id").
		Where(relation+"."+column+" = ?", mediaID).Order("people.id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return recapEntities(rows, "person-avatar"), nil
}

func recapVideoCollections(db *gorm.DB, videoID uint) ([]RecapEntity, error) {
	var rows []recapRankRow
	if err := db.Table("collection_videos").
		Select("media_collections.id AS id, media_collections.name AS name, media_collections.cover_path AS extra").
		Joins("JOIN media_collections ON media_collections.id = collection_videos.collection_id").
		Where("collection_videos.video_id = ? AND media_collections.deleted_at IS NULL", videoID).
		Order("media_collections.id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return recapEntities(rows, "collection-cover"), nil
}

func recapEntities(rows []recapRankRow, route string) []RecapEntity {
	entities := make([]RecapEntity, 0, len(rows))
	for _, row := range rows {
		entities = append(entities, RecapEntity{ID: row.ID, Name: row.Name, ImageURL: recapEntityURL(route, row.ID, row.Extra)})
	}
	return entities
}

// recapRelatedLimit 是「相关视频还有」的最多部数。
const recapRelatedLimit = 6

// recapSharedVideosSQL 数每部视频与来源共享的非自动标签、人物、作品集个数（UNION ALL 后分组，
// 共享两个标签就算 2）。参数依次是来源 ID ×4。
const recapSharedVideosSQL = `SELECT rel.video_id AS video_id, COUNT(*) AS shared FROM (
	SELECT vt.video_id AS video_id FROM video_tags vt JOIN tags t ON t.id = vt.tag_id
	WHERE vt.tag_id IN (SELECT tag_id FROM video_tags WHERE video_id = ?) AND t.deleted_at IS NULL AND COALESCE(t.automatic_kind, '') = ''
	UNION ALL
	SELECT vp.video_id AS video_id FROM video_people vp WHERE vp.person_id IN (SELECT person_id FROM video_people WHERE video_id = ?)
	UNION ALL
	SELECT cv.video_id AS video_id FROM collection_videos cv JOIN media_collections mc ON mc.id = cv.collection_id
	WHERE mc.deleted_at IS NULL AND cv.collection_id IN (SELECT collection_id FROM collection_videos WHERE video_id = ?)
) rel WHERE rel.video_id <> ? GROUP BY rel.video_id`

// recapRelatedVideos 返回与来源视频共享最多的片库视频：只看默认视图可见的视频（扫描根、黑名单、
// 非失效、未删除），排除来源自己与本期已经播放过的视频；按共享数、评分（未评分排后）、最近入库、ID 排序。
func recapRelatedVideos(db *gorm.DB, sourceID uint, start, end time.Time) ([]RecapRelatedVideo, error) {
	query := db.Model(&models.Video{}).
		Select("videos.id AS id, videos.name AS name, videos.display_title AS display_title, videos.duration AS duration, videos.personal_rating AS personal_rating, rs.shared AS shared").
		Joins("JOIN ("+recapSharedVideosSQL+") rs ON rs.video_id = videos.id", sourceID, sourceID, sourceID, sourceID).
		Where("NOT EXISTS (SELECT 1 FROM play_events pe WHERE pe.video_id = videos.id AND pe.played_at >= ? AND pe.played_at < ?)", start, end)
	query, err := visibleVideoQuery(query)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID             uint
		Name           string
		DisplayTitle   string
		Duration       float64
		PersonalRating *float64
		Shared         int64
	}
	if err := query.Order("rs.shared DESC").
		Order("CASE WHEN videos.personal_rating IS NULL THEN 1 ELSE 0 END ASC").Order("videos.personal_rating DESC").
		Order("videos.created_at DESC").Order("videos.id DESC").Limit(recapRelatedLimit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	related := make([]RecapRelatedVideo, 0, len(rows))
	for _, row := range rows {
		related = append(related, RecapRelatedVideo{Shared: row.Shared, Video: RecapVideo{ID: row.ID, Name: row.Name,
			Title: recapVideoTitle(row.DisplayTitle, row.Name), Duration: row.Duration, PersonalRating: row.PersonalRating, Available: true}})
	}
	return related, nil
}

const recapSharedImagesSQL = `SELECT rel.image_id AS image_id, COUNT(*) AS shared FROM (
	SELECT it.image_id AS image_id FROM image_tags it JOIN tags t ON t.id = it.tag_id
	WHERE it.tag_id IN (SELECT tag_id FROM image_tags WHERE image_id = ?) AND t.deleted_at IS NULL AND COALESCE(t.automatic_kind, '') = ''
	UNION ALL
	SELECT ip.image_id AS image_id FROM image_people ip WHERE ip.person_id IN (SELECT person_id FROM image_people WHERE image_id = ?)
) rel WHERE rel.image_id <> ? GROUP BY rel.image_id`

// recapRelatedImages 与视频同理：共享标签 / 人物最多、照片页可见、本期没看过的图片。
func recapRelatedImages(db *gorm.DB, sourceID uint, start, end time.Time) ([]RecapRelatedImage, error) {
	query := db.Model(&models.Image{}).
		Select("images.id AS id, images.name AS name, images.width AS width, images.height AS height, images.directory AS directory, rs.shared AS shared").
		Joins("JOIN ("+recapSharedImagesSQL+") rs ON rs.image_id = images.id", sourceID, sourceID, sourceID).
		Where("NOT EXISTS (SELECT 1 FROM image_view_events ive WHERE ive.image_id = images.id AND ive.viewed_at >= ? AND ive.viewed_at < ?)", start, end)
	query = applyImageVisibility(query, db)
	var rows []struct {
		RecapImage
		Shared int64
	}
	if err := query.Order("rs.shared DESC").
		Order("CASE WHEN images.personal_rating IS NULL THEN 1 ELSE 0 END ASC").Order("images.personal_rating DESC").
		Order("images.created_at DESC").Order("images.id DESC").Limit(recapRelatedLimit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	related := make([]RecapRelatedImage, 0, len(rows))
	for _, row := range rows {
		image := row.RecapImage
		image.Available = true
		related = append(related, RecapRelatedImage{Image: image, Shared: row.Shared})
	}
	return related, nil
}
