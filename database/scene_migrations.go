package database

import (
	"fmt"
	"video-master/models"

	"gorm.io/gorm"
)

// DefaultSceneVisualIntervalSeconds 是场景检索画面采样间隔的默认值（场景检索合同「画面索引」）。
const DefaultSceneVisualIntervalSeconds = 5

// migrateSceneSettings 把 scene_visual_interval_seconds 补成默认 5（D-MW-SCENES）。
//
// 该列不带 gorm default（默认非零的数值列，同清理阈值）。合法取值是 2–30，≤0 与 NULL
// 本来就等价于默认值，因此不需要「列刚建出来」的判据：用户设过的正值不会被碰，
// 重复执行不变。
func migrateSceneSettings(db *gorm.DB) error {
	if err := db.Model(&models.Settings{}).
		Where("scene_visual_interval_seconds IS NULL OR scene_visual_interval_seconds <= ?", 0).
		UpdateColumn("scene_visual_interval_seconds", DefaultSceneVisualIntervalSeconds).Error; err != nil {
		return fmt.Errorf("补默认场景采样间隔失败: %w", err)
	}
	return nil
}
