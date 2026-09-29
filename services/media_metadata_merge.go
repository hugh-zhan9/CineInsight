package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 合并元数据的媒体类别（与回收站 kind 同一组取值）。
const (
	MediaMergeKindVideo = "video"
	MediaMergeKindImage = "image"
)

// MediaMergeWatchedSetter 是翻转已看状态的入口（*VideoService 满足）。合并提交之后，
// keeper 的 is_watched 由 false 变 true 时经它翻转，已看同步观察者（D-PC52）由它负责通知。
type MediaMergeWatchedSetter interface {
	SetVideoWatched(videoID uint, watched bool) (*models.Video, error)
}

// MediaMetadataMergeDeps 是合并在数据库事务之外要用到的能力，由 App 层注入。
type MediaMetadataMergeDeps struct {
	// Watched 用于提交后翻转 keeper 的已看状态；视频合并必填。
	Watched MediaMergeWatchedSetter
	// Subtitles 用于把来源的同名 .srt 写成 keeper 的同名 .srt（D-PC13 写入器）；为 nil 时字幕迁移记警告。
	Subtitles *SubtitleFileWriter
}

// MediaMetadataMergeResult 是一次合并的结果。Warnings 是不影响数据库合并的问题（字幕迁移失败等），
// 文案不含绝对路径。
type MediaMetadataMergeResult struct {
	Kind             string   `json:"kind"`
	KeeperID         uint     `json:"keeper_id"`
	SourceIDs        []uint   `json:"source_ids"`
	TagsAdded        int      `json:"tags_added"`
	PeopleAdded      int      `json:"people_added"`
	CollectionsAdded int      `json:"collections_added"`
	FavoriteChanged  bool     `json:"favorite_changed"`
	LikedChanged     bool     `json:"liked_changed"`
	RatingChanged    bool     `json:"rating_changed"`
	ProgressChanged  bool     `json:"progress_changed"`
	WatchedChanged   bool     `json:"watched_changed"`
	SubtitleMoved    bool     `json:"subtitle_moved"`
	Warnings         []string `json:"warnings"`
}

// MergeMediaMetadata 把 sourceIDs 上的整理成果合并到 keeperID（D-PC48），供清理中心在删除
// 被合并项之前单独调用（不嵌套删除事务）。数据库部分是单一事务，失败整体中止、不进入删除：
//   - 非自动标签取并集，人物关系取并集；
//   - 收藏 / 点赞取或，favorited_at 取最早（收藏为真时一定带时间，规则同 favoriteColumns）；
//   - 评分取最大（NULL 视为最小）；
//   - 视频：keeper 加入来源所在的每个作品集，位置放在来源的位置；keeper 未看时断点取最大。
//
// 事务提交之后：keeper 的已看由 false 变 true 时经 deps.Watched 翻转（观察者照常收到通知），
// 失败返回错误（数据库合并已提交且可重复执行，调用方不要进入删除）；keeper 没有同名 .srt
// 而来源有时，经 deps.Subtitles 写成 keeper 的同名 .srt 并刷新索引，失败只记警告
// （字幕仍在来源旁，随来源进入废纸篓，可恢复）。
func MergeMediaMetadata(kind string, keeperID uint, sourceIDs []uint, deps MediaMetadataMergeDeps) (*MediaMetadataMergeResult, error) {
	if kind != MediaMergeKindVideo && kind != MediaMergeKindImage {
		return nil, fmt.Errorf("未知的媒体类别：%s", kind)
	}
	if keeperID == 0 {
		return nil, errors.New("合并元数据需要保留项")
	}
	sources := uniqueUintIDs(sourceIDs)
	if len(sources) == 0 {
		return nil, errors.New("合并元数据至少需要一个被合并项")
	}
	if containsUintID(sources, keeperID) {
		return nil, errors.New("保留项不能同时是被合并项")
	}
	result := &MediaMetadataMergeResult{Kind: kind, KeeperID: keeperID, SourceIDs: sources, Warnings: []string{}}
	if kind == MediaMergeKindImage {
		if err := database.Transaction(func(tx *gorm.DB) error {
			return mergeImageMetadataTx(tx, keeperID, sources, result)
		}); err != nil {
			return nil, err
		}
		return result, nil
	}

	if deps.Watched == nil {
		return nil, errors.New("缺少已看状态写入器，无法合并视频元数据")
	}
	var plan videoMergeFollowUp
	if err := database.Transaction(func(tx *gorm.DB) error {
		var err error
		plan, err = mergeVideoMetadataTx(tx, keeperID, sources, result)
		return err
	}); err != nil {
		return nil, err
	}
	if plan.markWatched {
		if _, err := deps.Watched.SetVideoWatched(keeperID, true); err != nil {
			return nil, fmt.Errorf("合并已看状态失败：%w", err)
		}
		result.WatchedChanged = true
	}
	moveMergedSubtitle(plan.keeper, plan.sources, deps.Subtitles, result)
	return result, nil
}

// videoMergeFollowUp 是事务提交之后还要做的事。
type videoMergeFollowUp struct {
	keeper      models.Video
	sources     []models.Video
	markWatched bool
}

func loadMergeVideos(tx *gorm.DB, keeperID uint, sourceIDs []uint) (models.Video, []models.Video, error) {
	var keeper models.Video
	if err := tx.First(&keeper, keeperID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return keeper, nil, errors.New("保留的视频不存在或已删除")
		}
		return keeper, nil, err
	}
	var sources []models.Video
	if err := tx.Where("id IN ?", sourceIDs).Order("id ASC").Find(&sources).Error; err != nil {
		return keeper, nil, err
	}
	if len(sources) != len(sourceIDs) {
		return keeper, nil, errors.New("部分被合并的视频不存在或已删除")
	}
	// 按调用方给的顺序排列：字幕只取第一个有同名 .srt 的来源。
	byID := make(map[uint]models.Video, len(sources))
	for _, source := range sources {
		byID[source.ID] = source
	}
	ordered := make([]models.Video, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		ordered = append(ordered, byID[id])
	}
	return keeper, ordered, nil
}

func mergeVideoMetadataTx(tx *gorm.DB, keeperID uint, sourceIDs []uint, result *MediaMetadataMergeResult) (videoMergeFollowUp, error) {
	keeper, sources, err := loadMergeVideos(tx, keeperID, sourceIDs)
	if err != nil {
		return videoMergeFollowUp{}, err
	}
	plan := videoMergeFollowUp{keeper: keeper, sources: sources}

	// ① 非自动标签取并集。新加上的等同于手动打标：同标签的待审 AI 候选随之作废（规则 2）。
	var tagIDs []uint
	if err := tx.Table("video_tags").
		Joins("JOIN tags ON tags.id = video_tags.tag_id").
		Where("video_tags.video_id IN ? AND COALESCE(tags.automatic_kind, '') = '' AND tags.deleted_at IS NULL", sourceIDs).
		Distinct().Pluck("video_tags.tag_id", &tagIDs).Error; err != nil {
		return plan, err
	}
	for _, tagID := range uniqueUintIDs(tagIDs) {
		inserted := tx.Exec(`INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, keeperID, tagID)
		if inserted.Error != nil {
			return plan, inserted.Error
		}
		if inserted.RowsAffected == 1 {
			result.TagsAdded++
			if _, err := SupersedeCandidatesForManualTag(tx, keeperID, tagID); err != nil {
				return plan, err
			}
		}
	}

	// ② 人物关系取并集。保留项已有同一人物的关系（插入撞上唯一键）时，被合并项的关系确认了它：
	// 释放保留项上人脸链路的写入记录，关系归用户所有（META-04 A-m3），之后解除簇关联不会删它。
	var personIDs []uint
	if err := tx.Model(&models.VideoPerson{}).Where("video_id IN ?", sourceIDs).Distinct().Pluck("person_id", &personIDs).Error; err != nil {
		return plan, err
	}
	now := time.Now()
	for _, personID := range uniqueUintIDs(personIDs) {
		inserted := tx.Exec(`INSERT INTO video_people (video_id, person_id, created_at) VALUES (?, ?, ?) ON CONFLICT (video_id, person_id) DO NOTHING`, keeperID, personID, now)
		if inserted.Error != nil {
			return plan, inserted.Error
		}
		if inserted.RowsAffected == 1 {
			result.PeopleAdded++
			continue
		}
		if err := releaseFaceRelationWrites(tx, personID, models.FaceMediaKindVideo, []uint{keeperID}); err != nil {
			return plan, err
		}
	}

	// ③ 作品集：keeper 加入来源所在的每个（未删除的）作品集，位置放在来源的位置。
	// 同一作品集里有多个来源时取最靠前的那个位置；keeper 已在其中的不动。
	var memberships []models.CollectionVideo
	if err := tx.Model(&models.CollectionVideo{}).
		Joins("JOIN media_collections ON media_collections.id = collection_videos.collection_id").
		Where("collection_videos.video_id IN ? AND media_collections.deleted_at IS NULL", sourceIDs).
		Select("collection_videos.collection_id", "collection_videos.video_id", "collection_videos.position").
		Order("collection_videos.collection_id ASC").Order("collection_videos.position ASC").
		Find(&memberships).Error; err != nil {
		return plan, err
	}
	positions := make(map[uint]int)
	order := make([]uint, 0)
	for _, membership := range memberships {
		if current, seen := positions[membership.CollectionID]; !seen || membership.Position < current {
			if !seen {
				order = append(order, membership.CollectionID)
			}
			positions[membership.CollectionID] = membership.Position
		}
	}
	for _, collectionID := range order {
		row := models.CollectionVideo{CollectionID: collectionID, VideoID: keeperID, Position: positions[collectionID]}
		inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if inserted.Error != nil {
			return plan, inserted.Error
		}
		if inserted.RowsAffected == 1 {
			result.CollectionsAdded++
		}
	}

	// ④ 收藏 / 点赞 / 评分。
	sourceFavorites := make([]mergeFavoriteState, 0, len(sources))
	anyLiked, anyWatched := false, false
	var bestRating *float64
	var bestProgress *models.Video
	for i := range sources {
		source := sources[i]
		sourceFavorites = append(sourceFavorites, mergeFavoriteState{favorite: source.IsFavorite, at: source.FavoritedAt})
		anyLiked = anyLiked || source.IsLiked
		anyWatched = anyWatched || source.IsWatched
		bestRating = maxMergeRating(bestRating, source.PersonalRating)
		if source.WatchPositionSeconds > 0 && (bestProgress == nil || source.WatchPositionSeconds > bestProgress.WatchPositionSeconds) {
			bestProgress = &sources[i]
		}
	}
	changed, err := mergeFavoriteTx(tx, &models.Video{}, keeperID, mergeFavoriteState{favorite: keeper.IsFavorite, at: keeper.FavoritedAt}, sourceFavorites, now)
	if err != nil {
		return plan, err
	}
	result.FavoriteChanged = changed
	if result.LikedChanged, err = mergeLikedTx(tx, &models.Video{}, keeperID, keeper.IsLiked, anyLiked); err != nil {
		return plan, err
	}
	if result.RatingChanged, err = mergeRatingTx(tx, &models.Video{}, keeperID, keeper.PersonalRating, bestRating); err != nil {
		return plan, err
	}

	// ⑤ 观看：keeper 已看时断点不动；未看时断点取最大（按 keeper 的时长截断）。
	if !keeper.IsWatched && bestProgress != nil {
		position := bestProgress.WatchPositionSeconds
		if keeper.Duration > 0 && position > keeper.Duration {
			position = keeper.Duration
		}
		if position > keeper.WatchPositionSeconds {
			updatedAt := now
			if bestProgress.WatchProgressUpdatedAt != nil {
				updatedAt = *bestProgress.WatchProgressUpdatedAt
			}
			updated := tx.Model(&models.Video{}).
				Where("id = ? AND is_watched = ? AND watch_position_seconds < ?", keeperID, false, position).
				Updates(map[string]interface{}{"watch_position_seconds": position, "watch_progress_updated_at": &updatedAt})
			if updated.Error != nil {
				return plan, updated.Error
			}
			result.ProgressChanged = updated.RowsAffected == 1
		}
	}
	plan.markWatched = !keeper.IsWatched && anyWatched
	return plan, nil
}

func mergeImageMetadataTx(tx *gorm.DB, keeperID uint, sourceIDs []uint, result *MediaMetadataMergeResult) error {
	var keeper models.Image
	if err := tx.First(&keeper, keeperID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("保留的图片不存在或已删除")
		}
		return err
	}
	var sources []models.Image
	if err := tx.Where("id IN ?", sourceIDs).Find(&sources).Error; err != nil {
		return err
	}
	if len(sources) != len(sourceIDs) {
		return errors.New("部分被合并的图片不存在或已删除")
	}

	var tagIDs []uint
	if err := tx.Table("image_tags").
		Joins("JOIN tags ON tags.id = image_tags.tag_id").
		Where("image_tags.image_id IN ? AND COALESCE(tags.automatic_kind, '') = '' AND tags.deleted_at IS NULL", sourceIDs).
		Distinct().Pluck("image_tags.tag_id", &tagIDs).Error; err != nil {
		return err
	}
	for _, tagID := range uniqueUintIDs(tagIDs) {
		inserted := tx.Exec(`INSERT INTO image_tags(image_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, keeperID, tagID)
		if inserted.Error != nil {
			return inserted.Error
		}
		if inserted.RowsAffected == 1 {
			result.TagsAdded++
			if _, err := SupersedeImageCandidatesForManualTag(tx, keeperID, tagID); err != nil {
				return err
			}
		}
	}

	var personIDs []uint
	if err := tx.Model(&models.ImagePerson{}).Where("image_id IN ?", sourceIDs).Distinct().Pluck("person_id", &personIDs).Error; err != nil {
		return err
	}
	now := time.Now()
	for _, personID := range uniqueUintIDs(personIDs) {
		inserted := tx.Exec(`INSERT INTO image_people (image_id, person_id, created_at) VALUES (?, ?, ?) ON CONFLICT (image_id, person_id) DO NOTHING`, keeperID, personID, now)
		if inserted.Error != nil {
			return inserted.Error
		}
		if inserted.RowsAffected == 1 {
			result.PeopleAdded++
			continue
		}
		// 同视频：保留项已有的关系被合并项确认，归用户所有（META-04 A-m3）。
		if err := releaseFaceRelationWrites(tx, personID, models.FaceMediaKindImage, []uint{keeperID}); err != nil {
			return err
		}
	}

	sourceFavorites := make([]mergeFavoriteState, 0, len(sources))
	anyLiked := false
	var bestRating *float64
	for _, source := range sources {
		sourceFavorites = append(sourceFavorites, mergeFavoriteState{favorite: source.IsFavorite, at: source.FavoritedAt})
		anyLiked = anyLiked || source.IsLiked
		bestRating = maxMergeRating(bestRating, source.PersonalRating)
	}
	changed, err := mergeFavoriteTx(tx, &models.Image{}, keeperID, mergeFavoriteState{favorite: keeper.IsFavorite, at: keeper.FavoritedAt}, sourceFavorites, now)
	if err != nil {
		return err
	}
	result.FavoriteChanged = changed
	if result.LikedChanged, err = mergeLikedTx(tx, &models.Image{}, keeperID, keeper.IsLiked, anyLiked); err != nil {
		return err
	}
	if result.RatingChanged, err = mergeRatingTx(tx, &models.Image{}, keeperID, keeper.PersonalRating, bestRating); err != nil {
		return err
	}
	return nil
}

type mergeFavoriteState struct {
	favorite bool
	at       *time.Time
}

// mergeFavoriteTx 收藏取或、favorited_at 取最早。只看收藏为真的一方的时间：取消收藏后残留的
// 旧时间不能被带回来（与 favoriteColumns 同一不变量：收藏为真必带时间，为假必为空）。
// 收藏的各方都没有时间（升级前的旧行）时记为 now，与 setter 首次收藏的做法一致。
func mergeFavoriteTx(tx *gorm.DB, model interface{}, keeperID uint, keeper mergeFavoriteState, sources []mergeFavoriteState, now time.Time) (bool, error) {
	anyFavorite := false
	var earliest *time.Time
	consider := func(state mergeFavoriteState) {
		if !state.favorite {
			return
		}
		anyFavorite = true
		if state.at != nil && (earliest == nil || state.at.Before(*earliest)) {
			at := *state.at
			earliest = &at
		}
	}
	sourceFavorite := false
	for _, source := range sources {
		sourceFavorite = sourceFavorite || source.favorite
		consider(source)
	}
	if !sourceFavorite {
		return false, nil
	}
	consider(keeper)
	if !anyFavorite {
		return false, nil
	}
	target := now
	if earliest != nil {
		target = *earliest
	}
	if keeper.favorite && keeper.at != nil && keeper.at.Equal(target) {
		return false, nil
	}
	updated := tx.Model(model).Where("id = ?", keeperID).Updates(map[string]interface{}{"is_favorite": true, "favorited_at": &target})
	if updated.Error != nil {
		return false, updated.Error
	}
	if updated.RowsAffected != 1 {
		return false, gorm.ErrRecordNotFound
	}
	return true, nil
}

func mergeLikedTx(tx *gorm.DB, model interface{}, keeperID uint, keeperLiked, anySourceLiked bool) (bool, error) {
	if keeperLiked || !anySourceLiked {
		return false, nil
	}
	updated := tx.Model(model).Where("id = ? AND is_liked = ?", keeperID, false).Update("is_liked", true)
	if updated.Error != nil {
		return false, updated.Error
	}
	return updated.RowsAffected == 1, nil
}

// maxMergeRating 取两个评分中较大的一个，NULL 视为最小。
func maxMergeRating(current, candidate *float64) *float64 {
	if candidate == nil {
		return current
	}
	if current == nil || *candidate > *current {
		value := *candidate
		return &value
	}
	return current
}

func mergeRatingTx(tx *gorm.DB, model interface{}, keeperID uint, keeperRating, bestSource *float64) (bool, error) {
	if bestSource == nil || (keeperRating != nil && *keeperRating >= *bestSource) {
		return false, nil
	}
	updated := tx.Model(model).
		Where("id = ? AND (personal_rating IS NULL OR personal_rating < ?)", keeperID, *bestSource).
		Update("personal_rating", *bestSource)
	if updated.Error != nil {
		return false, updated.Error
	}
	return updated.RowsAffected == 1, nil
}

// moveMergedSubtitle 在 keeper 没有同名 .srt 时，把第一个有同名 .srt 的来源字幕写成 keeper 的同名
// .srt（经写入器：临时文件 + 原子替换），再刷新 keeper 的字幕索引。任何失败只记警告。
func moveMergedSubtitle(keeper models.Video, sources []models.Video, writer *SubtitleFileWriter, result *MediaMetadataMergeResult) {
	target := subtitleparser.SRTPathForVideo(keeper.Path)
	if keeperHasSubtitle, err := subtitlePathExists(target); err != nil {
		result.Warnings = append(result.Warnings, "读取保留项字幕状态失败："+subtitleIOReason(err)+"，字幕未迁移")
		return
	} else if keeperHasSubtitle {
		return
	}
	for _, source := range sources {
		sourcePath := subtitleparser.SRTPathForVideo(source.Path)
		info, err := os.Stat(sourcePath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if writer == nil {
			result.Warnings = append(result.Warnings, "字幕写入器不可用，字幕未迁移（仍在被合并项旁边）")
			return
		}
		content, err := os.ReadFile(sourcePath)
		if err != nil {
			result.Warnings = append(result.Warnings, "读取被合并项字幕失败："+subtitleIOReason(err)+"，字幕未迁移")
			return
		}
		moved, err := replaceMissingSubtitle(writer, keeper.ID, target, content)
		if err != nil {
			log.Printf("[MergeMediaMetadata] subtitle move failed keeper=%d source=%d err=%v", keeper.ID, source.ID, err)
			result.Warnings = append(result.Warnings, "字幕迁移失败："+err.Error()+"（字幕仍在被合并项旁边）")
			return
		}
		if !moved {
			// 加锁之后发现保留项已经有了同名 .srt（期间有别的写入）：不覆盖它。
			return
		}
		result.SubtitleMoved = true
		if err := indexSubtitleFileForVideoID(keeper.ID, target); err != nil {
			log.Printf("[MergeMediaMetadata] subtitle index refresh failed keeper=%d err=%v", keeper.ID, err)
			result.Warnings = append(result.Warnings, "字幕已迁移，但搜索索引刷新失败")
		}
		return
	}
}

// replaceMissingSubtitle 持字幕文件锁再确认一次目标不存在，然后经写入器写入；目标已存在时不动它。
func replaceMissingSubtitle(writer *SubtitleFileWriter, videoID uint, target string, content []byte) (bool, error) {
	unlock := lockSubtitleFile(target)
	defer unlock()
	exists, err := subtitlePathExists(target)
	if err != nil {
		return false, errors.New("读取保留项字幕状态失败：" + subtitleIOReason(err))
	}
	if exists {
		return false, nil
	}
	if _, err := writer.Replace(context.Background(), videoID, target, content); err != nil {
		return false, err
	}
	return true, nil
}

func subtitlePathExists(path string) (bool, error) {
	if _, err := os.Lstat(path); err == nil {
		return true, nil
	} else if os.IsNotExist(err) {
		return false, nil
	} else {
		return false, err
	}
}
