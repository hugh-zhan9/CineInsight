package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"video-master/database"
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

// ===== 扫描隐藏（D-PC06 图片侧） =====

// 图片被扫描隐藏的原因。原因由路径与图片目录的在线状态现算，不落库（图片表没有 stale_reason 列）。
const (
	HiddenImageReasonOfflineRoot = models.StaleReasonOfflineRoot
	HiddenImageReasonMissingFile = models.StaleReasonMissingFile
	HiddenImageReasonRemovedRoot = models.StaleReasonRemovedRoot
)

// HiddenImage 是一张被扫描器软删（隐藏）的图片。
type HiddenImage struct {
	ID        uint       `json:"id"`
	Name      string     `json:"name"`
	Path      string     `json:"path"`
	Directory string     `json:"directory"`
	Reason    string     `json:"reason"`
	DeletedAt *time.Time `json:"deleted_at" ts_type:"string"`
}

// HiddenImagePage 是一页扫描隐藏的图片。NextCursor 作为下一次的 cursor；HasMore 为 false 时没有更多。
type HiddenImagePage struct {
	Items      []HiddenImage `json:"items"`
	NextCursor uint          `json:"next_cursor"`
	HasMore    bool          `json:"has_more"`
}

// ListHiddenImages 按 id 倒序分页列出被扫描隐藏的图片：被扫描器软删的（deleted_by='scanner'），
// 带 is_stale 恢复标记的软删图片，以及仍然活跃但 is_stale 的图片（它们已从图库视图里消失，Minor 11）。
// 用户主动删除的图片（deleted_by='user'）不在这里，它们在回收站。
func (s *ImageService) ListHiddenImages(cursor uint, limit int) (*HiddenImagePage, error) {
	limit = normalizeEntityPageLimit(limit)
	query := database.DB.Unscoped().Model(&models.Image{}).
		Where("(deleted_at IS NOT NULL AND deleted_by = ?) OR is_stale = ?", "scanner", true)
	if cursor > 0 {
		query = query.Where("id < ?", cursor)
	}
	var images []models.Image
	if err := query.Order("id DESC").Limit(limit + 1).Find(&images).Error; err != nil {
		return nil, fmt.Errorf("列出扫描隐藏的图片失败: %w", err)
	}
	page := &HiddenImagePage{Items: make([]HiddenImage, 0, len(images))}
	if len(images) > limit {
		page.HasMore = true
		images = images[:limit]
	}
	var dirs []models.ImageDirectory
	if err := database.DB.Find(&dirs).Error; err != nil {
		return nil, fmt.Errorf("读取图片扫描目录失败: %w", err)
	}
	roots := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		root := filepath.Clean(strings.TrimSpace(dir.Path))
		if root != "" && root != "." {
			roots = append(roots, root)
		}
	}
	rootOnline := make(map[string]bool, len(roots))
	for _, image := range images {
		var deletedAt *time.Time
		if value, _ := image.DeletedAt.Value(); value != nil {
			if at, ok := value.(time.Time); ok {
				deletedAt = &at
			}
		}
		page.Items = append(page.Items, HiddenImage{
			ID: image.ID, Name: image.Name, Path: image.Path, Directory: image.Directory,
			Reason: hiddenImageReason(image.Path, roots, rootOnline), DeletedAt: deletedAt,
		})
	}
	if len(page.Items) > 0 {
		page.NextCursor = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

// hiddenImageReason 推算隐藏原因：不在任何已配置的图片目录下 → removed_root；所在目录当前
// 不可访问 → offline_root；目录在线 → missing_file。
func hiddenImageReason(path string, roots []string, rootOnline map[string]bool) string {
	for _, root := range roots {
		if path != root && !strings.HasPrefix(path, scanRootChildPrefix(root)) {
			continue
		}
		online, cached := rootOnline[root]
		if !cached {
			info, err := os.Stat(root)
			online = err == nil && info.IsDir()
			rootOnline[root] = online
		}
		if !online {
			return HiddenImageReasonOfflineRoot
		}
		return HiddenImageReasonMissingFile
	}
	return HiddenImageReasonRemovedRoot
}

// RecheckImages 重新检查被隐藏的图片。图片侧没有窄扫描（既有限制），所以 ids 只用来确认调用方
// 确实选了图片；实际执行的是一次全量 SyncImageDirectories，文件回来的图片由扫描自动恢复。
func (s *ImageService) RecheckImages(ids []uint) (*ImageScanResult, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("没有选择要重新检查的图片")
	}
	return s.SyncImageDirectories()
}
