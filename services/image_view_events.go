package services

import (
	"context"
	"errors"
	"fmt"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// imageViewDedupeWindow：同一图片同一来源在这段时间内只记一条查看事件（D-RECAP-SOURCES），
// 查看器里来回翻同一张图不灌水。判定读数据库里该图该来源最近的事件，不靠内存，重启不失效。
const imageViewDedupeWindow = 30 * time.Minute

var errImageViewMissing = errors.New("图片不存在或已删除")

// ImageViewResult 是一次查看上报的结果：是否新记了事件，以及记完之后的累计查看次数。
type ImageViewResult struct {
	ImageID      uint       `json:"image_id"`
	Recorded     bool       `json:"recorded"`
	ViewCount    int        `json:"view_count"`
	LastViewedAt *time.Time `json:"last_viewed_at" ts_type:"string"`
}

// RecordImageView 记录一次桌面查看器里的有效查看（同一张图连续显示满 1.5 秒由前端判定）。
// 只接受 desktop_viewer；手机端走 ShortFeedService 既有的浏览计数路径。
func (s *ImageService) RecordImageView(ctx context.Context, imageID uint, source string) (*ImageViewResult, error) {
	if source != models.ImageViewSourceDesktopViewer {
		return nil, fmt.Errorf("不支持的图片查看来源: %s", source)
	}
	if imageID == 0 {
		return nil, errImageViewMissing
	}
	at := time.Now()
	result := &ImageViewResult{ImageID: imageID}
	err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		recorded, err := recordImageViewTx(tx, imageID, source, at)
		if err != nil {
			return err
		}
		result.Recorded = recorded
		var image models.Image
		if err := tx.Select("id", "view_count", "last_viewed_at").First(&image, imageID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errImageViewMissing
			}
			return err
		}
		result.ViewCount = image.ViewCount
		result.LastViewedAt = image.LastViewedAt
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result.Recorded {
		notifyRecapActivity(RecapActivityImage, source, at)
	}
	return result, nil
}

// recordImageViewTx 在调用方的事务里追加一条查看事件，并把 images.view_count / last_viewed_at
// 同步推进；30 分钟窗口内已有同来源事件时什么也不写，返回 false。图片已软删或不存在时返回
// errImageViewMissing，事务随之回滚。last_viewed_at 只向前推进。
func recordImageViewTx(tx *gorm.DB, imageID uint, source string, at time.Time) (bool, error) {
	var recent []uint
	if err := tx.Model(&models.ImageViewEvent{}).
		Where("image_id = ? AND source = ? AND viewed_at > ?", imageID, source, at.Add(-imageViewDedupeWindow)).
		Limit(1).Pluck("id", &recent).Error; err != nil {
		return false, err
	}
	if len(recent) > 0 {
		return false, nil
	}
	counted := tx.Model(&models.Image{}).Where("id = ?", imageID).
		UpdateColumn("view_count", gorm.Expr("view_count + 1"))
	if counted.Error != nil {
		return false, counted.Error
	}
	if counted.RowsAffected == 0 {
		return false, errImageViewMissing
	}
	if err := tx.Model(&models.Image{}).
		Where("id = ? AND (last_viewed_at IS NULL OR last_viewed_at < ?)", imageID, at).
		UpdateColumn("last_viewed_at", at).Error; err != nil {
		return false, err
	}
	if err := tx.Create(&models.ImageViewEvent{ImageID: imageID, ViewedAt: at, Source: source}).Error; err != nil {
		return false, err
	}
	return true, nil
}
