package services

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

type DirectoryService struct{}

// 编辑扫描目录路径时的两种语义（D-PC07）。
const (
	// DirectoryUpdateModeRemap：目录只是换了位置（例如外接盘挂载名变了），保留全部记录，
	// 把旧路径下的所有持久化路径改写到新路径。
	DirectoryUpdateModeRemap = "remap"
	// DirectoryUpdateModeReplace：用一个新目录替换旧目录，旧目录下的记录标为 removed_root。
	DirectoryUpdateModeReplace = "replace"
)

// DirectoryUpdateResult 是 UpdateDirectory 的结果，供调用方决定是否需要窄对账与重配监听。
type DirectoryUpdateResult struct {
	Mode        string            `json:"mode"`
	OldPath     string            `json:"old_path"`
	NewPath     string            `json:"new_path"`
	PathChanged bool              `json:"path_changed"`
	Rewritten   PathRewriteCounts `json:"rewritten"`
	MarkedStale int64             `json:"marked_stale"`
}

// ScanDirectoryValidation 是把目录加入扫描根之前的预检（LIB-14）。
type ScanDirectoryValidation struct {
	Exists bool `json:"exists"`
	// DuplicateOf 是与之完全相同的已配置根（空串表示没有）。
	DuplicateOf string `json:"duplicate_of"`
	// NestedIn 是包含它的、最近的已配置根。
	NestedIn string `json:"nested_in"`
	// Contains 是被它包含的已配置根。
	Contains []string `json:"contains"`
}

// GetAllDirectories 获取所有扫描目录
func (s *DirectoryService) GetAllDirectories() ([]models.ScanDirectory, error) {
	var dirs []models.ScanDirectory
	err := database.DB.Order("created_at desc").Find(&dirs).Error
	return dirs, err
}

// AddDirectory 添加扫描目录
func (s *DirectoryService) AddDirectory(path, alias string) (*models.ScanDirectory, error) {
	unlock, err := rLockLibraryPaths()
	if err != nil {
		return nil, err
	}
	defer unlock()
	dir := &models.ScanDirectory{
		Path:  path,
		Alias: alias,
	}
	return dir, database.DB.Create(dir).Error
}

// ValidateScanDirectory 检查一个候选目录：是否存在、是否与已配置根重复、是否互相嵌套。
// 只读，不改任何状态。
func (s *DirectoryService) ValidateScanDirectory(path string) (*ScanDirectoryValidation, error) {
	candidate := filepath.Clean(strings.TrimSpace(path))
	if candidate == "" || candidate == "." {
		return nil, fmt.Errorf("目录路径不能为空")
	}
	var dirs []models.ScanDirectory
	if err := database.DB.Find(&dirs).Error; err != nil {
		return nil, fmt.Errorf("读取扫描目录失败: %w", err)
	}
	result := &ScanDirectoryValidation{Contains: make([]string, 0)}
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		result.Exists = true
	}
	for _, root := range cleanScanRoots(dirs) {
		switch {
		case root == candidate:
			result.DuplicateOf = root
		case pathIsEqualOrInside(candidate, root):
			if len(root) > len(result.NestedIn) {
				result.NestedIn = root
			}
		case pathIsEqualOrInside(root, candidate):
			result.Contains = append(result.Contains, root)
		}
	}
	sort.Strings(result.Contains)
	return result, nil
}

// UpdateDirectory 更新扫描目录的别名与路径。路径没有变化时只改别名；路径变化时 mode 必须是
// remap 或 replace（D-PC07）。
//
// remap 要求新路径存在，且不与其他根相同或互相嵌套，然后在同一个事务里改写旧路径下的全部
// 持久化路径（保留记录 ID、标签、进度）。replace 沿用旧行为，只是先把旧根下只属于它的记录
// 标为 removed_root。两种模式都不做对账与监听重配——调用方在成功后处理（窄对账新路径、
// reconfigureLibraryWatcher）。
func (s *DirectoryService) UpdateDirectory(id uint, path, alias, mode string) (*DirectoryUpdateResult, error) {
	newPath := filepath.Clean(strings.TrimSpace(path))
	if newPath == "" || newPath == "." {
		return nil, fmt.Errorf("目录路径不能为空")
	}
	var current models.ScanDirectory
	if err := database.DB.First(&current, id).Error; err != nil {
		return nil, fmt.Errorf("扫描目录不存在: %w", err)
	}
	oldPath := filepath.Clean(strings.TrimSpace(current.Path))
	result := &DirectoryUpdateResult{Mode: mode, OldPath: oldPath, NewPath: newPath, PathChanged: newPath != oldPath}

	if !result.PathChanged {
		unlock, err := rLockLibraryPaths()
		if err != nil {
			return nil, err
		}
		defer unlock()
		return result, database.DB.Model(&models.ScanDirectory{}).Where("id = ?", id).Update("alias", alias).Error
	}
	switch mode {
	case DirectoryUpdateModeRemap:
		return s.remapDirectory(current, newPath, alias, result)
	case DirectoryUpdateModeReplace:
		return s.replaceDirectory(current, newPath, alias, result)
	default:
		return nil, fmt.Errorf("不支持的目录更新方式: %q", mode)
	}
}

func (s *DirectoryService) remapDirectory(current models.ScanDirectory, newPath, alias string, result *DirectoryUpdateResult) (*DirectoryUpdateResult, error) {
	// 重映射也会改写图片侧的路径（rewriteLibraryPathPrefixTx 覆盖 images / image_directories），
	// 与 MoveDirectory / RenameDirectory 同样按「视频锁在前、图片锁在后」的固定顺序持锁。
	defer lockLibraryPathRewrite()()

	oldPath := result.OldPath
	if info, err := os.Stat(newPath); err != nil {
		return nil, fmt.Errorf("新路径不可用: %w", err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("新路径不是目录")
	}
	if pathIsEqualOrInside(newPath, oldPath) || pathIsEqualOrInside(oldPath, newPath) {
		return nil, fmt.Errorf("新路径不能与原路径互相包含；如果只是换了一个目录，请选择「替换为新目录」")
	}
	var others []models.ScanDirectory
	if err := database.DB.Where("id <> ?", current.ID).Find(&others).Error; err != nil {
		return nil, fmt.Errorf("读取扫描目录失败: %w", err)
	}
	for _, root := range cleanScanRoots(others) {
		if root == newPath {
			return nil, fmt.Errorf("新路径已经是另一个扫描目录")
		}
		if pathIsEqualOrInside(newPath, root) || pathIsEqualOrInside(root, newPath) {
			return nil, fmt.Errorf("新路径与另一个扫描目录互相嵌套")
		}
	}
	if err := checkRemapConflicts(oldPath, newPath); err != nil {
		return nil, err
	}

	err := database.Transaction(func(tx *gorm.DB) error {
		counts, err := rewriteLibraryPathPrefixTx(tx, oldPath, newPath)
		if err != nil {
			return err
		}
		result.Rewritten = counts
		return tx.Model(&models.ScanDirectory{}).Where("id = ?", current.ID).Update("alias", alias).Error
	})
	if err != nil {
		return nil, fmt.Errorf("重映射目录失败: %w", err)
	}
	return result, nil
}

// checkRemapConflicts 在改写之前找出「改写后会与新路径下已有的活跃记录撞路径」的视频与图片，
// 给出可读的错误，而不是让唯一索引在事务中途失败。重映射同样改写图片路径（rewriteLibraryPathPrefixTx），
// 所以图片的路径唯一索引（idx_images_path_active）也要预检（M7）。
func checkRemapConflicts(oldPath, newPath string) error {
	videoConflicts, err := countRemapPathConflicts(&models.Video{}, oldPath, newPath)
	if err != nil {
		return fmt.Errorf("检查新路径下的已有视频记录失败: %w", err)
	}
	if videoConflicts > 0 {
		return fmt.Errorf("新路径下已有 %d 条视频记录与原记录路径重复，请先处理这些记录", videoConflicts)
	}
	imageConflicts, err := countRemapPathConflicts(&models.Image{}, oldPath, newPath)
	if err != nil {
		return fmt.Errorf("检查新路径下的已有图片记录失败: %w", err)
	}
	if imageConflicts > 0 {
		return fmt.Errorf("新路径下已有 %d 张图片记录与原记录路径重复，请先处理这些图片", imageConflicts)
	}
	return nil
}

// countRemapPathConflicts 统计 model（视频或图片；默认作用域只含活跃行）里位于 oldPath 之下的活跃行
// 改写到 newPath 之后，有多少条的新路径已被活跃行占用。路径唯一索引只覆盖 deleted_at IS NULL 的行，
// 软删行改写后不会撞索引，不参与预检。
func countRemapPathConflicts(model interface{}, oldPath, newPath string) (int, error) {
	like := escapeSQLLikePrefix(scanRootChildPrefix(oldPath)) + "%"
	var paths []string
	if err := database.DB.Model(model).Where(`path = ? OR path LIKE ? ESCAPE '\'`, oldPath, like).Pluck("path", &paths).Error; err != nil {
		return 0, err
	}
	projected := make([]string, 0, len(paths))
	for _, path := range paths {
		// LIKE 对含通配符的路径可能过匹配，这里以路径包含关系为准。
		if !pathIsEqualOrInside(path, oldPath) {
			continue
		}
		rewritten, err := replacePathPrefix(path, oldPath, newPath)
		if err != nil {
			return 0, err
		}
		projected = append(projected, rewritten)
	}
	conflicts := 0
	for start := 0; start < len(projected); start += 500 {
		end := min(start+500, len(projected))
		var count int64
		if err := database.DB.Model(model).Where("path IN ?", projected[start:end]).Count(&count).Error; err != nil {
			return 0, err
		}
		conflicts += int(count)
	}
	return conflicts, nil
}

func (s *DirectoryService) replaceDirectory(current models.ScanDirectory, newPath, alias string, result *DirectoryUpdateResult) (*DirectoryUpdateResult, error) {
	unlock, err := rLockLibraryPaths()
	if err != nil {
		return nil, err
	}
	defer unlock()

	var others []models.ScanDirectory
	if err := database.DB.Where("id <> ?", current.ID).Find(&others).Error; err != nil {
		return nil, fmt.Errorf("读取扫描目录失败: %w", err)
	}
	remaining := append(cleanScanRoots(others), newPath)
	// 顺序与 DeleteDirectory 一致：先标失效再改配置。标记失败就不改路径，
	// 否则旧记录会掉进「扫描看不见、列表看得见」的夹缝。
	marked, err := markVideosStaleUnderRemovedRoot(result.OldPath, remaining)
	if err != nil {
		return nil, err
	}
	result.MarkedStale = marked
	err = database.DB.Model(&models.ScanDirectory{}).Where("id = ?", current.ID).Updates(map[string]interface{}{
		"path":  newPath,
		"alias": alias,
	}).Error
	return result, err
}

// DeleteDirectory 删除扫描目录
func (s *DirectoryService) DeleteDirectory(id uint) error {
	unlock, err := rLockLibraryPaths()
	if err != nil {
		return err
	}
	defer unlock()
	return database.DB.Delete(&models.ScanDirectory{}, id).Error
}
