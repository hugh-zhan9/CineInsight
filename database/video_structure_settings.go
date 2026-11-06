package database

import (
	"gorm.io/gorm"
	"video-master/models"
)

// A newly added nullable column is recoverable after any interrupted startup.
// Explicit false is a saved user choice and is never changed.
func migrateStructureSettings(db *gorm.DB) error {
	return db.Model(&models.Settings{}).Where("auto_structure_enabled IS NULL").Update("auto_structure_enabled", true).Error
}
func initializeStructureCutoff(db *gorm.DB) error {
	var cutoff uint
	if err := db.Model(&models.Video{}).Select("COALESCE(MAX(id),0)").Scan(&cutoff).Error; err != nil {
		return err
	}
	return db.Model(&models.Settings{}).Where("auto_structure_initial_cutoff_id IS NULL").Update("auto_structure_initial_cutoff_id", cutoff).Error
}
