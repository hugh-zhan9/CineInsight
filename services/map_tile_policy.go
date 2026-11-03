package services

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"video-master/database"
	"video-master/models"
)

var ErrMapTilesDisabled = errors.New("map_tiles_disabled")
var ErrMapTileInvalid = errors.New("map_tile_invalid")
var ErrMapTileChanged = errors.New("map_tile_changed")
var ErrMapCacheFull = errors.New("map_cache_full")
var ErrMapCacheUnavailable = errors.New("map_cache_unavailable")
var ErrMapTileUnavailable = errors.New("map_tile_unavailable")

func (s *SettingsService) MapTilePolicy(ctx context.Context) (bool, error) {
	var settings models.Settings
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error { return db.Select("id", "map_tiles_enabled").First(&settings).Error })
	return settings.MapTilesEnabled, err
}
func (s *SettingsService) SetMapTilePolicy(ctx context.Context, expected, enabled bool) error {
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var settings models.Settings
		if err := db.Select("id").First(&settings).Error; err != nil {
			return err
		}
		result := db.Model(&models.Settings{}).Where("id = ? AND map_tiles_enabled = ?", settings.ID, expected).Update("map_tiles_enabled", enabled)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrMapTileChanged
		}
		return nil
	})
}
