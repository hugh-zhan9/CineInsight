package services

import (
	"errors"
	"video-master/models"

	"gorm.io/gorm"
)

// Query-time exclusions hide existing records without modifying their deletion
// state; removing an exclusion makes them visible again immediately.
func applyImageVisibility(query, db *gorm.DB) *gorm.DB {
	query = query.Where("images.is_stale = ?", false)
	var settings models.Settings
	err := db.Session(&gorm.Session{NewDB: true}).Select("scan_exclude_paths", "image_scan_exclude_paths").First(&settings).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		query.AddError(err)
		return query
	}
	excluded := parseScanExcludePaths(settings.ImageScanExcludePaths)
	if len(excluded) == 0 {
		excluded = parseScanExcludePaths(settings.ScanExcludePaths)
	}
	for _, path := range excluded {
		query = query.Where(`NOT (images.path = ? OR images.path LIKE ? ESCAPE '\')`, path, escapeSQLLikePrefix(scanRootChildPrefix(path))+"%")
	}
	return query
}
