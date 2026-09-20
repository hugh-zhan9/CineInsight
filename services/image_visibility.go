package services

import (
	"errors"
	"video-master/models"

	"gorm.io/gorm"
)

// imageScanExcludedPaths 读图片扫描黑名单：图片黑名单优先，为空则回退通用黑名单
// （与扫描行为一致）。设置表还不存在时返回空列表，不算错误。
//
// 抽出来共用：黑名单是"哪些图片算数"的唯一口径，清理审阅、补全任务与列表查询各自
// 抄一份的话，迟早出现一边算、一边不算——补全任务的目标集就曾经漏了这一项，白花
// CPU 解码用户明确排除掉的目录。
func imageScanExcludedPaths(db *gorm.DB) ([]string, error) {
	var settings models.Settings
	err := db.Session(&gorm.Session{NewDB: true}).Select("scan_exclude_paths", "image_scan_exclude_paths").First(&settings).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	excluded := parseScanExcludePaths(settings.ImageScanExcludePaths)
	if len(excluded) == 0 {
		excluded = parseScanExcludePaths(settings.ScanExcludePaths)
	}
	return excluded, nil
}

// applyImageExclusions 把黑名单翻成 SQL 条件。调用方自己决定 is_stale 那一项。
func applyImageExclusions(query *gorm.DB, excluded []string) *gorm.DB {
	for _, path := range excluded {
		query = query.Where(`NOT (images.path = ? OR images.path LIKE ? ESCAPE '\')`, path, escapeSQLLikePrefix(scanRootChildPrefix(path))+"%")
	}
	return query
}

// Query-time exclusions hide existing records without modifying their deletion
// state; removing an exclusion makes them visible again immediately.
func applyImageVisibility(query, db *gorm.DB) *gorm.DB {
	query = query.Where("images.is_stale = ?", false)
	excluded, err := imageScanExcludedPaths(db)
	if err != nil {
		query.AddError(err)
		return query
	}
	return applyImageExclusions(query, excluded)
}
