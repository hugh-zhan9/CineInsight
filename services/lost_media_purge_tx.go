package services

import (
	"context"
	"time"
	"video-master/models"

	"gorm.io/gorm"
)

// 清除（D-LD-01/04）在事务里处理现在硬删漏掉的引用（详细设计 1.3「需手动删除」一栏），外键级联的不动。
// 只在调用方的事务里执行：SQLite 的 immediate 事务不能嵌套。

// removeFromVersionGroupTx 把视频移出它所在的版本组：先用 bumpVersionGroupRevision 做条件 +1（打开着的编辑界面
// 会按冲突重读），再删成员行；组里剩不足两个成员行（含软删成员，与 cleanupUndersizedVersionGroups 同口径）时解散。
func removeFromVersionGroupTx(tx *gorm.DB, videoID uint, now time.Time) error {
	var member models.VideoVersionMember
	found := tx.Where("video_id = ?", videoID).Limit(1).Find(&member)
	if found.Error != nil || found.RowsAffected == 0 {
		return found.Error
	}
	var group models.VideoVersionGroup
	if err := tx.Select("id", "revision").First(&group, member.GroupID).Error; err != nil {
		return err
	}
	if err := bumpVersionGroupRevision(tx, group.ID, group.Revision, now); err != nil {
		return err
	}
	if err := tx.Where("group_id = ? AND video_id = ?", group.ID, videoID).Delete(&models.VideoVersionMember{}).Error; err != nil {
		return err
	}
	var remaining int64
	if err := tx.Model(&models.VideoVersionMember{}).Where("group_id = ?", group.ID).Count(&remaining).Error; err != nil {
		return err
	}
	if remaining >= versionGroupMinMembers {
		return nil
	}
	if err := tx.Where("group_id = ?", group.ID).Delete(&models.VideoVersionMember{}).Error; err != nil {
		return err
	}
	return tx.Where("id = ?", group.ID).Delete(&models.VideoVersionGroup{}).Error
}

// deleteLostReferencesTx 删掉没有外键、硬删不会带走的引用行，返回被删掉的人脸观测（提交后删它们的裁剪图）。
// 人脸观测删掉后在同一事务里按实际观测重算受影响的簇（recomputeFaceClustersTx）。
func deleteLostReferencesTx(ctx context.Context, tx *gorm.DB, kind string, id uint) ([]models.FaceObservation, error) {
	execs := [][]any{}
	if kind == LostMediaKindVideo {
		execs = append(execs,
			[]any{"DELETE FROM near_duplicate_dismissals WHERE video_low_id = ? OR video_high_id = ?", id, id},
			[]any{"DELETE FROM clip_dismissals WHERE video_full_id = ? OR video_clip_id = ?", id, id},
			[]any{"DELETE FROM video_version_suggestion_dismissals WHERE video_low_id = ? OR video_high_id = ?", id, id},
		)
	} else {
		execs = append(execs, []any{"DELETE FROM image_near_duplicate_dismissals WHERE image_low_id = ? OR image_high_id = ?", id, id})
	}
	execs = append(execs,
		[]any{"DELETE FROM face_relation_writes WHERE media_kind = ? AND media_id = ?", kind, id},
		[]any{"DELETE FROM automation_rule_writes WHERE media_kind = ? AND media_id = ?", kind, id},
		[]any{"DELETE FROM metadata_batch_edit_items WHERE media_id = ? AND batch_id IN (SELECT id FROM metadata_batch_edits WHERE kind = ?)", id, kind},
	)
	for _, statement := range execs {
		if err := tx.Exec(statement[0].(string), statement[1:]...).Error; err != nil {
			return nil, err
		}
	}
	var observations []models.FaceObservation
	if err := tx.Select("id", "crop_path", "cluster_id").Where("media_kind = ? AND media_id = ?", kind, id).
		Find(&observations).Error; err != nil {
		return nil, err
	}
	if len(observations) > 0 {
		if err := tx.Where("media_kind = ? AND media_id = ?", kind, id).Delete(&models.FaceObservation{}).Error; err != nil {
			return nil, err
		}
		if _, err := recomputeFaceClustersTx(ctx, tx, faceClusterIDsOf(observations)); err != nil {
			return nil, err
		}
	}
	if kind == LostMediaKindVideo {
		if err := deleteQueueEntriesForVideoTx(tx, id); err != nil {
			return nil, err
		}
		// 观影日记按设计比视频活得久，只把它对这条视频播放记录的引用置空（播放记录随外键级联删除）。
		if err := tx.Exec("UPDATE viewing_diary_entries SET source_play_event_id = NULL WHERE source_play_event_id IN (SELECT id FROM play_events WHERE video_id = ?)", id).Error; err != nil {
			return nil, err
		}
	}
	return observations, nil
}

// deleteQueueEntriesForVideoTx 删掉播放队列里这条视频的条目；当前条目被删时队列回到空闲（与「移出队列」同口径），
// 有删除就让队列 revision +1，打开着的队列面板重读。
func deleteQueueEntriesForVideoTx(tx *gorm.DB, videoID uint) error {
	var entryIDs []uint
	if err := tx.Model(&models.PlaybackQueueEntry{}).Where("video_id = ?", videoID).Pluck("id", &entryIDs).Error; err != nil {
		return err
	}
	if len(entryIDs) == 0 {
		return nil
	}
	if err := tx.Where("id IN ?", entryIDs).Delete(&models.PlaybackQueueEntry{}).Error; err != nil {
		return err
	}
	var state models.PlaybackQueueState
	found := tx.Where("id = ?", 1).Limit(1).Find(&state)
	if found.Error != nil || found.RowsAffected == 0 {
		return found.Error
	}
	if state.CurrentEntryID != nil {
		for _, entryID := range entryIDs {
			if *state.CurrentEntryID == entryID {
				if err := resetQueueCurrent(tx); err != nil {
					return err
				}
				break
			}
		}
	}
	return tx.Model(&models.PlaybackQueueState{}).Where("id = ?", 1).
		Updates(map[string]any{"revision": gorm.Expr("revision + 1"), "updated_at": time.Now()}).Error
}
