package services

import (
	"context"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// videoBackfillQuery 与片库共用扫描范围和黑名单，且不处理失效记录。
// 候选发现与实际执行前都调用，避免排队/等待空闲期间的设置变化失效。
// 读取范围失败必须终止查询，不能退回全库。
func videoBackfillQuery(ctx context.Context) *gorm.DB {
	query := database.DB.WithContext(ctx).Model(&models.Video{}).Where("videos.is_stale = ?", false)
	scoped, err := applyScanRootScope(query)
	if err != nil {
		query.AddError(err)
		return query
	}
	return scoped
}
