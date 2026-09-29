package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"video-master/models"

	"gorm.io/gorm"
)

// 超分产物继承原片信息（D-PC24「把原片的标签、人物、作品集与外挂字幕复制到产物」）。
//
// 这两个函数只负责「怎么复制」。「这一次要不要复制」来自 CreateEnhancementTask 的
// copy_metadata 入参，而任务可能跨重启才发布，这个开关存在哪里设计没有给出——
// 见 P-025 交付报告的停下项，接线等主代理裁决后在 publishOutput 的事务里调用。

// copyEnhancementSourceMetadataTx 在产物入库的同一事务里，把原片的非自动标签、人物、作品集
// 成员关系复制给产物。自动标签（automatic_kind 非空）由产物自己的规则重新判定，不复制；
// 已删除的标签与作品集跳过；作品集里产物追加到末尾；已有的关系不重复写。
func copyEnhancementSourceMetadataTx(tx *gorm.DB, sourceID, outputID uint) error {
	if sourceID == 0 || outputID == 0 || sourceID == outputID {
		return errors.New("复制原片信息需要不同的原片与产物")
	}
	var tagIDs []uint
	if err := tx.Table("video_tags").
		Joins("JOIN tags ON tags.id = video_tags.tag_id").
		Where("video_tags.video_id = ? AND COALESCE(tags.automatic_kind, '') = '' AND tags.deleted_at IS NULL", sourceID).
		Order("video_tags.tag_id ASC").
		Pluck("video_tags.tag_id", &tagIDs).Error; err != nil {
		return fmt.Errorf("读取原片标签失败: %w", err)
	}
	for _, tagID := range tagIDs {
		if err := tx.Exec(`INSERT INTO video_tags (video_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, outputID, tagID).Error; err != nil {
			return fmt.Errorf("复制标签失败: %w", err)
		}
	}

	if err := tx.Exec(`INSERT INTO video_people (video_id, person_id, created_at)
		SELECT ?, person_id, ? FROM video_people WHERE video_id = ?
		ON CONFLICT (video_id, person_id) DO NOTHING`, outputID, time.Now(), sourceID).Error; err != nil {
		return fmt.Errorf("复制人物失败: %w", err)
	}

	var collectionIDs []uint
	if err := tx.Model(&models.CollectionVideo{}).
		Joins("JOIN media_collections ON media_collections.id = collection_videos.collection_id").
		Where("collection_videos.video_id = ? AND media_collections.deleted_at IS NULL", sourceID).
		Order("collection_videos.collection_id ASC").
		Pluck("collection_videos.collection_id", &collectionIDs).Error; err != nil {
		return fmt.Errorf("读取原片所在作品集失败: %w", err)
	}
	for _, collectionID := range collectionIDs {
		var existing int64
		if err := tx.Model(&models.CollectionVideo{}).
			Where("collection_id = ? AND video_id = ?", collectionID, outputID).Count(&existing).Error; err != nil {
			return fmt.Errorf("读取作品集成员失败: %w", err)
		}
		if existing > 0 {
			continue
		}
		var maxPosition int
		if err := tx.Model(&models.CollectionVideo{}).Where("collection_id = ?", collectionID).
			Select("COALESCE(MAX(position), 0)").Scan(&maxPosition).Error; err != nil {
			return fmt.Errorf("读取作品集末位失败: %w", err)
		}
		member := models.CollectionVideo{CollectionID: collectionID, VideoID: outputID, Position: maxPosition + 1}
		if err := tx.Create(&member).Error; err != nil {
			return fmt.Errorf("把产物加入作品集失败: %w", err)
		}
	}
	return nil
}

// copyEnhancementSidecarSubtitle 把原片的同名外挂字幕（<原片名>.srt）经字幕写入器复制为产物的
// 同名 .srt（D-PC24、详细设计 §4.2）。原片没有外挂字幕时什么都不做。返回值是给用户看的警告：
// 字幕复制失败不影响产物入库（产物已经发布），只提示一句，不含路径。
func copyEnhancementSidecarSubtitle(ctx context.Context, writer *SubtitleFileWriter, source, output models.Video) string {
	sourceSRT := strings.TrimSuffix(source.Path, filepath.Ext(source.Path)) + ".srt"
	info, err := os.Lstat(sourceSRT)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		return "原片外挂字幕读取失败，未复制到产物"
	}
	if !info.Mode().IsRegular() {
		return "原片外挂字幕不是普通文件，未复制到产物"
	}
	if writer == nil {
		return "字幕写入器不可用，原片外挂字幕未复制到产物"
	}
	content, err := os.ReadFile(sourceSRT)
	if err != nil {
		return "原片外挂字幕读取失败，未复制到产物"
	}
	target := strings.TrimSuffix(output.Path, filepath.Ext(output.Path)) + ".srt"
	unlock := lockSubtitleFile(target)
	defer unlock()
	if _, err := writer.Replace(ctx, output.ID, target, content); err != nil {
		logEnhancement("output=%d sidecar subtitle copy failed: %s", output.ID, sanitizeEnhancementError(err.Error()))
		return "原片外挂字幕复制失败，可稍后手动处理"
	}
	return ""
}
