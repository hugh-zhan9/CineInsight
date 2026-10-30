package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"gorm.io/gorm"
	"video-master/models"
)

var ErrBTPathNotInLibrary = errors.New("path_not_in_library: 下载位置不在片库扫描范围内，请配置扫描目录")

// ValidateBTDownloadRoot checks the planned namespace, not its parent. It shares
// the importer's lexical AND physical authorization and blacklist policy.
func ValidateBTDownloadRoot(ctx context.Context, downloadRoot string) error {
	if !filepath.IsAbs(downloadRoot) {
		return ErrBTPath
	}
	info, err := os.Stat(downloadRoot)
	if err != nil || !info.IsDir() {
		return ErrBTPath
	}
	planned := filepath.Join(downloadRoot, BTNamespaceDirectory)
	real, err := btCanonicalIncludingMissing(planned)
	if err != nil {
		return ErrBTPath
	}
	return btTaskOperation(ctx, func(db *gorm.DB) error {
		_, allowed, err := btAuthorizedScanPath(db, planned, real)
		if err != nil {
			return ErrBTTask
		}
		if !allowed {
			return ErrBTPathNotInLibrary
		}
		return nil
	})
}
func btAuthorizedScanPath(db *gorm.DB, path, real string) (models.Settings, bool, error) {
	var dirs []models.ScanDirectory
	if err := db.Find(&dirs).Error; err != nil {
		return models.Settings{}, false, err
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		return settings, false, err
	}
	lexicalCovered := false
	covered := false
	for _, dir := range dirs {
		if btWithin(path, dir.Path) {
			lexicalCovered = true
		}
		if btWithin(path, dir.Path) && btWithin(real, dir.Path) {
			covered = true
		}
		if canonical, err := btCanonicalIncludingMissing(dir.Path); err == nil && btWithin(real, canonical) {
			covered = true
		}
	}
	excluded := parseScanExcludePaths(settings.ScanExcludePaths)
	for _, root := range append([]string{}, excluded...) {
		if canonical, err := btCanonicalIncludingMissing(root); err == nil {
			excluded = append(excluded, canonical)
		}
	}

	return settings, lexicalCovered && covered && !isScanPathExcluded(path, excluded) && !isScanPathExcluded(real, excluded), nil
}
func btRootValidationError(err error) error {
	if errors.Is(err, ErrBTPathNotInLibrary) {
		return ErrBTPathNotInLibrary
	}
	return ErrBTPath
}
