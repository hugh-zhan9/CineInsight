package services

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// ErrImageExists 图片已存在（路径已有记录，含软删除记录，镜像视频侧 ErrVideoExists 语义）。
var ErrImageExists = errors.New("IMAGE_EXISTS")

// imagePathMutationMu 串行化图片路径变更操作，镜像 libraryPathMutationMu 的读写锁语义：
// 常规路径读写方持读锁，独占维护方持写锁。与视频侧锁互不相干。
var imagePathMutationMu sync.RWMutex

// ImageService 图片实体生命周期服务（设计 4.1：扫描对账与目录管理）。
type ImageService struct {
	scanSyncMu sync.Mutex
}

func NewImageService() *ImageService {
	return &ImageService{}
}

// ImageScanError 单条对账失败信息（目录级或文件级）。
type ImageScanError struct {
	Operation string `json:"operation"`
	Directory string `json:"directory,omitempty"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error"`
}

// ImageScanResult 图片目录对账扫描结果。
type ImageScanResult struct {
	Added     int              `json:"added"`
	Restored  int              `json:"restored"`
	Relocated int              `json:"relocated"`
	Removed   int              `json:"removed"`
	Skipped   int              `json:"skipped"`
	Errors    []ImageScanError `json:"errors"`
}

func (r *ImageScanResult) recordError(operation, directory, path string, err error) {
	if err == nil {
		return
	}
	r.Errors = append(r.Errors, ImageScanError{
		Operation: operation,
		Directory: directory,
		Path:      path,
		Error:     err.Error(),
	})
	log.Printf("图片扫描同步失败 op=%s dir=%s path=%s err=%v", operation, directory, path, err)
}

// ===== 目录管理（镜像 DirectoryService 四件套） =====

// GetAllImageDirectories 获取所有图片扫描目录
func (s *ImageService) GetAllImageDirectories() ([]models.ImageDirectory, error) {
	var dirs []models.ImageDirectory
	err := database.DB.Order("created_at desc").Find(&dirs).Error
	return dirs, err
}

// AddImageDirectory 添加图片扫描目录
func (s *ImageService) AddImageDirectory(path, alias string) (*models.ImageDirectory, error) {
	imagePathMutationMu.RLock()
	defer imagePathMutationMu.RUnlock()
	dir := &models.ImageDirectory{
		Path:  path,
		Alias: alias,
	}
	err := database.DB.Create(dir).Error
	return dir, err
}

// UpdateImageDirectory 更新图片扫描目录
func (s *ImageService) UpdateImageDirectory(id uint, path, alias string) error {
	imagePathMutationMu.RLock()
	defer imagePathMutationMu.RUnlock()
	return database.DB.Model(&models.ImageDirectory{}).Where("id = ?", id).Updates(map[string]interface{}{
		"path":  path,
		"alias": alias,
	}).Error
}

// DeleteImageDirectory 删除图片扫描目录（软删除）
func (s *ImageService) DeleteImageDirectory(id uint) error {
	imagePathMutationMu.RLock()
	defer imagePathMutationMu.RUnlock()
	return database.DB.Delete(&models.ImageDirectory{}, id).Error
}

// ===== 扫描对账（镜像 VideoService.SyncScanDirectories 的对账语义，D-003/D-005） =====

// SyncImageDirectories 对账扫描全部活跃图片目录：新增/失踪求差 → name+size 双向唯一
// 迁移匹配 → 剩余新增入库 → 剩余失踪仅软删记录（不动文件、不建回收站条目）。
func (s *ImageService) SyncImageDirectories() (*ImageScanResult, error) {
	imagePathMutationMu.RLock()
	defer imagePathMutationMu.RUnlock()
	s.scanSyncMu.Lock()
	defer s.scanSyncMu.Unlock()
	// 旧版回收站目录集合每轮同步只刷新一次（m7），各根的遍历（scanImageDirectory）只读这份缓存。
	refreshLegacyTrashDirs()

	dirs, err := s.GetAllImageDirectories()
	if err != nil {
		return nil, fmt.Errorf("读取图片扫描目录失败: %w", err)
	}

	var settings models.Settings
	if err := database.DB.Select("image_extensions, scan_exclude_paths, image_scan_exclude_paths").First(&settings).Error; err != nil {
		return nil, fmt.Errorf("获取设置失败: %w", err)
	}
	extensions := parseImageExtensions(settings.ImageExtensions)
	// 图片黑名单独立配置；空值回退共用视频黑名单，保持老库行为。
	excludedPaths := parseScanExcludePaths(settings.ImageScanExcludePaths)
	if len(excludedPaths) == 0 {
		excludedPaths = parseScanExcludePaths(settings.ScanExcludePaths)
	}

	result := &ImageScanResult{Errors: make([]ImageScanError, 0)}
	scannedByPath := make(map[string]ScannedFile)
	removalGuard := make(scanRemovalGuard)
	roots := make([]string, 0, len(dirs))
	// configuredRoots 是"当前配置了哪些目录"，与 roots（本轮成功扫到的根）有意分开：
	// 移动硬盘没插时那个根扫不了、进不了 roots，但它仍然配置着，底下的记录不能
	// 因此被当成孤儿隐藏掉。
	configuredRoots := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		root := filepath.Clean(strings.TrimSpace(dir.Path))
		if root == "" || root == "." {
			result.recordError("scan", dir.Path, "", fmt.Errorf("扫描目录为空"))
			continue
		}
		configuredRoots = append(configuredRoots, root)
		if isScanPathExcluded(root, excludedPaths) {
			continue
		}
		if err := removalGuard.capture(root); err != nil {
			result.recordError("scan", root, "", err)
			if errors.Is(err, os.ErrNotExist) {
				images, loadErr := s.getActiveImagesUnderRoots([]string{root})
				if loadErr != nil {
					result.recordError("load_offline", root, "", loadErr)
				}
				for _, image := range images {
					if err := s.markMissingImageStale(image.ID); err != nil {
						result.recordError("mark_stale", root, image.Path, err)
					}
				}
			}
			continue
		}
		scannedFiles, scanErr := scanImageDirectory(root, extensions, excludedPaths)
		if scanErr != nil {
			result.recordError("scan", root, "", scanErr)
			delete(removalGuard, root)
			continue
		}
		roots = append(roots, root)
		for _, file := range scannedFiles {
			scannedByPath[file.Path] = file
		}
	}

	existingByPath := make(map[string]models.Image)
	allExisting := make([]models.Image, 0)
	duplicateImages := make([]models.Image, 0)
	loadedExisting, err := s.getActiveImagesUnderRoots(roots)
	if err != nil {
		result.recordError("load_existing", "", "", err)
	} else {
		for _, image := range loadedExisting {
			if isScanPathExcluded(image.Path, excludedPaths) {
				continue
			}
			if kept, exists := existingByPath[image.Path]; exists {
				if image.ID != kept.ID {
					duplicateImages = append(duplicateImages, image)
				}
				continue
			}
			existingByPath[image.Path] = image
			allExisting = append(allExisting, image)
		}
	}

	missingImages := make([]models.Image, 0)
	for _, image := range allExisting {
		if _, exists := scannedByPath[image.Path]; !exists {
			if _, statErr := os.Stat(image.Path); errors.Is(statErr, os.ErrNotExist) {
				if missing, guardErr := removalGuard.missing(image.Path, excludedPaths); guardErr != nil {
					result.recordError("check_missing_root", image.Directory, image.Path, guardErr)
					if err := s.markMissingImageStale(image.ID); err != nil {
						result.recordError("mark_stale", image.Directory, image.Path, err)
					}
				} else if missing {
					missingImages = append(missingImages, image)
				}
			} else if statErr != nil {
				result.recordError("check_missing", image.Directory, image.Path, statErr)
			}
			continue
		}
		if image.IsStale {
			if err := database.DB.Model(&models.Image{}).Where("id = ?", image.ID).Update("is_stale", false).Error; err != nil {
				result.recordError("clear_stale", image.Directory, image.Path, err)
			} else {
				result.Restored++
			}
		}
	}

	newFiles := make([]ScannedFile, 0)
	for _, file := range scannedByPath {
		if _, exists := existingByPath[file.Path]; !exists {
			newFiles = append(newFiles, file)
		}
	}
	sortScannedFiles(newFiles)

	// 迁移匹配：{文件名, 大小} 指纹在失踪集与新文件集各恰好一条才认定迁移，否则不猜测。
	relocatedImageIDs := make(map[uint]struct{})
	consumedNewPaths := make(map[string]struct{})
	missingByFingerprint := make(map[scanFileFingerprint][]models.Image)
	newFileCounts := make(map[scanFileFingerprint]int)
	for _, image := range missingImages {
		missingByFingerprint[fingerprintImage(image)] = append(missingByFingerprint[fingerprintImage(image)], image)
	}
	for _, file := range newFiles {
		newFileCounts[fingerprintScannedFile(file)]++
	}

	for _, file := range newFiles {
		key := fingerprintScannedFile(file)
		candidates := missingByFingerprint[key]
		if len(candidates) != 1 || newFileCounts[key] != 1 {
			continue
		}
		image := candidates[0]
		if _, used := relocatedImageIDs[image.ID]; used {
			continue
		}
		if err := s.relocateImage(image.ID, file.Path); err != nil {
			result.recordError("relocate", image.Directory, file.Path, err)
			continue
		}
		result.Relocated++
		relocatedImageIDs[image.ID] = struct{}{}
		consumedNewPaths[file.Path] = struct{}{}
	}

	for _, file := range newFiles {
		if _, consumed := consumedNewPaths[file.Path]; consumed {
			continue
		}
		// A previous scan may have soft-deleted a record while a removable
		// volume was unavailable. Revive only records explicitly marked as
		// auto-removed (IsStale), never records deleted by an intentional user
		// action.
		if restored, restoreErr := s.restoreStaleImage(file.Path, file.Size); restoreErr != nil {
			result.recordError("restore", filepath.Dir(file.Path), file.Path, restoreErr)
			continue
		} else if restored {
			result.Restored++
			continue
		}
		// 访达「放回原处」：原路径上的文件与回收站条目身份一致，就地恢复原记录（I3）。
		if restored, putBackErr := s.restoreImageIfPutBack(file.Path); putBackErr != nil {
			result.recordError("restore", filepath.Dir(file.Path), file.Path, putBackErr)
			continue
		} else if restored {
			result.Restored++
			continue
		}
		if _, err := s.addImage(file.Path); err != nil {
			if errors.Is(err, ErrImageExists) {
				result.Skipped++
				continue
			}
			result.recordError("add", filepath.Dir(file.Path), file.Path, err)
			continue
		}
		result.Added++
	}

	s.reconcileOrphanedImages(configuredRoots, result)

	if err := database.DB.Select("scan_exclude_paths, image_scan_exclude_paths").First(&settings).Error; err != nil {
		result.recordError("reload_scan_blacklist", "", "", err)
		return result, nil
	}
	excludedPaths = parseScanExcludePaths(settings.ImageScanExcludePaths)
	if len(excludedPaths) == 0 {
		excludedPaths = parseScanExcludePaths(settings.ScanExcludePaths)
	}
	for _, image := range append(duplicateImages, missingImages...) {
		if _, relocated := relocatedImageIDs[image.ID]; relocated {
			continue
		}
		missing, guardErr := removalGuard.missing(image.Path, excludedPaths)
		if guardErr != nil {
			result.recordError("check_delete", image.Directory, image.Path, guardErr)
			if err := s.markMissingImageStale(image.ID); err != nil {
				result.recordError("mark_stale", image.Directory, image.Path, err)
			}
			continue
		}
		if !missing {
			continue
		}
		if err := s.deleteMissingImageRecord(image.ID); err != nil {
			result.recordError("delete", image.Directory, image.Path, err)
			continue
		}
		result.Removed++
	}

	for root := range removalGuard {
		if err := removalGuard.verify(root); err != nil {
			result.recordError("verify_root", root, "", err)
			images, loadErr := s.getActiveImagesUnderRoots([]string{root})
			if loadErr != nil {
				result.recordError("load_offline", root, "", loadErr)
			}
			for _, image := range images {
				if err := s.markMissingImageStale(image.ID); err != nil {
					result.recordError("mark_stale", root, image.Path, err)
				}
			}
		}
	}

	log.Printf("图片目录对账完成 dirs=%d scanned=%d added=%d restored=%d relocated=%d removed=%d skipped=%d errors=%d",
		len(roots), len(scannedByPath), result.Added, result.Restored, result.Relocated, result.Removed, result.Skipped, len(result.Errors))
	return result, nil
}

// scanImageDirectory 按扩展名遍历单个图片目录。过滤链（设计 4.1.2）：
// ScanExcludePaths → 隐藏路径 → 回收站目录 SkipDir → isTrashPath。
//
// 回收站目录与视频扫描同一套按路径的判定（IMG-13，并入 LIB-14）：isTrashDir / isTrashPath 只认
// 有 legacy_trash 条目的目录、按旧版特征认出的 trash/（基名恰为 trash、父目录下有旧版时间段内无条目的
// 软删媒体且文件名对得上）与 .Trash / .Trashes；用户自己恰好叫 trash / Trash 的目录照常收录。
func scanImageDirectory(dir string, extensions []string, excludedPaths []string) ([]ScannedFile, error) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "." {
		return nil, fmt.Errorf("扫描根目录为空")
	}
	// 旧版回收站目录集合由入口（SyncImageDirectories）每轮刷新一次，这里只读缓存（m7）。
	rootInfo, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("扫描根目录不可用: %w", err)
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("扫描根路径不是目录: %s", dir)
	}
	imageFiles := make([]ScannedFile, 0)
	if isScanPathExcluded(dir, excludedPaths) {
		log.Printf("跳过黑名单图片扫描目录 dir=%s", dir)
		return imageFiles, nil
	}

	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if isScanPathExcluded(path, excludedPaths) || (info != nil && (shouldSkipHiddenPath(info) || (info.IsDir() && isTrashDir(path)))) {
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir() || isTrashPath(path) {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		for _, imageExt := range extensions {
			if ext == imageExt {
				imageFiles = append(imageFiles, ScannedFile{Path: path, Size: info.Size()})
				break
			}
		}
		return nil
	})
	log.Printf("扫描图片目录完成 dir=%s files=%d", dir, len(imageFiles))
	return imageFiles, err
}

// parseImageExtensions 解析扩展名清单；空值回退 database.DefaultImageExtensions（D-004）。
func parseImageExtensions(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		raw = database.DefaultImageExtensions
	}
	parts := strings.Split(raw, ",")
	extensions := make([]string, 0, len(parts))
	for _, part := range parts {
		ext := strings.ToLower(strings.TrimSpace(part))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		extensions = append(extensions, ext)
	}
	return extensions
}

func fingerprintImage(image models.Image) scanFileFingerprint {
	return scanFileFingerprint{Name: image.Name, Size: image.Size}
}

// reconcileOrphanedImages 处理"不属于任何已配置图片目录"的记录。
//
// 这类记录从哪来：用户删掉过某个图片目录（旧版本只删配置行、不动记录），或者把
// 目录改成了别的路径。而两条对账都只在配置目录之下取候选，所以它们过去永远碰不到，
// 记录就停在"扫描看不见、图库看得见"的夹缝里——删了目录图片还在列表上就是这么来的。
//
// 处理方式与失踪对账一致（只标 is_stale，不软删、不动磁盘文件、不建回收站条目），
// 把那个目录加回来时扫描清除 is_stale。
//
// 一个目录都没配置时，所有记录都是孤儿、全部隐藏——这正是"没有扫描目录就不该有内容"
// 的应有之义。调用方必须先确认目录清单是读成功的：读失败时绝不能走到这里，
// 否则会把整库藏起来。
func (s *ImageService) reconcileOrphanedImages(configuredRoots []string, result *ImageScanResult) {
	var images []models.Image
	if err := database.DB.Select("id", "path", "directory", "is_stale").Find(&images).Error; err != nil {
		result.recordError("load_orphans", "", "", err)
		return
	}
	for _, image := range images {
		if len(configuredRoots) > 0 && imageBelongsToRoots(image, configuredRoots) {
			continue
		}
		if image.IsStale {
			continue
		}
		if err := s.markMissingImageStale(image.ID); err != nil {
			result.recordError("orphan_hide", image.Directory, image.Path, err)
			continue
		}
		result.Removed++
	}
}

// MarkImagesStaleUnderRemovedRoot 把只属于被移除扫描根的图片按"失踪对账"处理（D-S04）。
//
// 与视频一致：只标失效，查询过滤；目录加回后扫描恢复原记录。
//
// 嵌套目录同视频：同时落在另一个仍配置着的根之下的图片保持原样。
// 返回实际处理的条数。
func (s *ImageService) MarkImagesStaleUnderRemovedRoot(removedRoot string, remainingRoots []string) (int, error) {
	imagePathMutationMu.RLock()
	defer imagePathMutationMu.RUnlock()

	root := filepath.Clean(strings.TrimSpace(removedRoot))
	if root == "" || root == "." {
		return 0, fmt.Errorf("图片扫描目录为空")
	}
	remaining := make([]string, 0, len(remainingRoots))
	for _, candidate := range remainingRoots {
		cleaned := filepath.Clean(strings.TrimSpace(candidate))
		if cleaned == "" || cleaned == "." || cleaned == root {
			continue
		}
		remaining = append(remaining, cleaned)
	}

	candidates, err := s.getActiveImagesUnderRoots([]string{root})
	if err != nil {
		return 0, fmt.Errorf("读取该目录下的图片失败: %w", err)
	}

	marked := 0
	for _, image := range candidates {
		if len(remaining) > 0 && imageBelongsToRoots(image, remaining) {
			continue
		}
		if image.IsStale {
			continue
		}
		if err := s.markMissingImageStale(image.ID); err != nil {
			return marked, fmt.Errorf("标记图片失效失败: %w", err)
		}
		marked++
	}
	if marked > 0 {
		log.Printf("图片扫描目录移除，按失踪对账处理 root=%s marked=%d", root, marked)
	}
	return marked, nil
}

func (s *ImageService) getActiveImagesUnderRoots(roots []string) ([]models.Image, error) {
	if len(roots) == 0 {
		return []models.Image{}, nil
	}
	// 先在 SQL 内收窄避免整库加载；LIKE 对含通配符路径可能过匹配，imageBelongsToRoots 兜底裁决。
	query := database.DB.Model(&models.Image{})
	conditions := database.DB.Session(&gorm.Session{NewDB: true})
	for index, root := range roots {
		prefix := escapeSQLLikePrefix(root+string(os.PathSeparator)) + "%"
		clause := database.DB.Session(&gorm.Session{NewDB: true}).
			Where("directory = ?", root).
			Or(`directory LIKE ? ESCAPE '\'`, prefix).
			Or(`path LIKE ? ESCAPE '\'`, prefix)
		if index == 0 {
			conditions = conditions.Where(clause)
			continue
		}
		conditions = conditions.Or(clause)
	}
	var images []models.Image
	if err := query.Where(conditions).Find(&images).Error; err != nil {
		return nil, err
	}
	filtered := images[:0]
	for _, image := range images {
		if imageBelongsToRoots(image, roots) {
			filtered = append(filtered, image)
		}
	}
	return filtered, nil
}

func imageBelongsToRoots(image models.Image, roots []string) bool {
	for _, root := range roots {
		prefix := root + string(os.PathSeparator)
		if image.Directory == root || strings.HasPrefix(image.Directory, prefix) || strings.HasPrefix(image.Path, prefix) {
			return true
		}
	}
	return false
}

// addImage 新增图片记录：os.Stat → Unscoped 路径预查 → 仅写 name/path/directory/size，
// 随后即时解析一次 EXIF（双轨回填的入库一轨）。
// 尺寸探测与 dHash 回填由缩略图管线（4.2）异步承担，本方法不做。
func (s *ImageService) addImage(path string) (*models.Image, error) {
	path = filepath.Clean(strings.TrimSpace(path))

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("文件不存在: %w", err)
	}

	// 活跃记录一律算已存在；同路径只剩软删行时按身份判定（D-PC03，与视频侧同一张表）。
	var existingImage models.Image
	if result := database.DB.Where("path = ?", path).Limit(1).Find(&existingImage); result.Error != nil {
		return nil, result.Error
	} else if result.RowsAffected == 1 {
		log.Printf("跳过已存在图片 path=%s", path)
		return &existingImage, ErrImageExists
	}
	if row, skipErr, err := softDeletedImagePathSkip(path, info); err != nil {
		return nil, err
	} else if skipErr != nil {
		log.Printf("跳过同路径的已删除图片 path=%s", path)
		return row, skipErr
	}

	image := &models.Image{
		Name:      filepath.Base(path),
		Path:      path,
		Directory: filepath.Dir(path),
		Size:      info.Size(),
		Format:    strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
	}
	if err := database.DB.Create(image).Error; err != nil {
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "unique") || strings.Contains(errMsg, "constraint") {
			if findErr := database.DB.Where("path = ?", path).First(&existingImage).Error; findErr == nil {
				return &existingImage, ErrImageExists
			}
		}
		return nil, err
	}
	// EXIF 解析失败不阻塞入库：exif_parsed_at 保持 NULL，由补全任务下次接手。
	if _, err := refreshImageEXIF(database.DB, image.ID, image.Path, time.Now); err != nil {
		log.Printf("[ImageEXIF] 入库解析失败 image=%d path=%s err=%v", image.ID, image.Path, err)
	}
	log.Printf("新增图片 path=%s", path)
	return image, nil
}

// restoreStaleImage revives a row that this scanner previously removed after
// its source path disappeared. The IsStale marker is the guard that keeps an
// intentional user deletion (which leaves IsStale=false) from being undone by
// a later scan when the original file is still on disk.
func (s *ImageService) restoreStaleImage(path string, size int64) (bool, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	// 墓碑的软删行按已硬删处理，不挡住同路径更早的扫描器软删行（修复 L m4）。
	var image models.Image
	if err := withoutTrashTombstones(database.DB.Unscoped().Where("path = ? AND deleted_at IS NOT NULL", path), imageTrashKind).
		Order("deleted_at DESC, id DESC").First(&image).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if !image.IsStale || image.DeletedBy == "user" {
		return false, nil
	}
	result := database.DB.Unscoped().Model(&models.Image{}).Where("id = ? AND deleted_at IS NOT NULL AND is_stale = ?", image.ID, true).Updates(map[string]interface{}{
		"deleted_at": nil,
		"is_stale":   false,
		"size":       size,
		"name":       filepath.Base(path),
		"directory":  filepath.Dir(path),
	})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	return true, nil
}

// relocateImage 迁移场景原地改写 path/directory，保留标签/收藏/评分等其余字段（D-005）。
func (s *ImageService) relocateImage(id uint, newPath string) error {
	newPath = filepath.Clean(strings.TrimSpace(newPath))

	if _, err := os.Stat(newPath); err != nil {
		return fmt.Errorf("目标文件不存在: %w", err)
	}

	var existing models.Image
	if err := database.DB.Where("path = ? AND id != ?", newPath, id).First(&existing).Error; err == nil {
		return fmt.Errorf("目标路径已被其他记录占用: %s", newPath)
	}

	result := database.DB.Model(&models.Image{}).Where("id = ?", id).Updates(map[string]interface{}{
		"path":      newPath,
		"directory": filepath.Dir(newPath),
		"is_stale":  false,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("图片记录不存在: %d", id)
	}
	log.Printf("图片迁移更新路径 id=%d newPath=%s", id, newPath)
	return nil
}

// deleteMissingImageRecord 失踪对账仅软删记录：不动磁盘文件，不建回收站条目。
// is_stale 是后续自动恢复的来源标记。
func (s *ImageService) deleteMissingImageRecord(id uint) error {
	// Preserve a recovery breadcrumb before soft deletion. If a removable
	// volume disappears temporarily, the next scan can revive the same row and
	// retain its tags, rating, and other curated metadata.
	if err := database.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.Image{}).Where("id = ?", id).Updates(map[string]interface{}{"is_stale": true, "deleted_by": "scanner"})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("图片记录不存在或已删除: %d", id)
		}
		return tx.Delete(&models.Image{}, id).Error
	}); err != nil {
		return err
	}
	log.Printf("图片记录软删除 id=%d deleted_by=scanner delete_file=false", id)
	return nil
}

// markMissingImageStale 对离线磁盘或移除的扫描范围只做隐藏。
func (s *ImageService) markMissingImageStale(id uint) error {
	return database.DB.Model(&models.Image{}).Where("id = ?", id).Update("is_stale", true).Error
}

// ===== 回收站（镜像视频侧四态状态机 pending_move/deleted/restoring/rollback，设计 4.5 / D-008） =====

// DeleteImage 删除图片。deleteFile=false 软删记录并建 record_only 条目（文件不动、记录身份，
// 之后同一文件不会被扫描重新收录，可在回收站「允许重新收录」）；deleteFile=true 走完整状态机：
// pending_move 条目 → 移入系统废纸篓 → 身份校验 → 事务内 CAS 置 deleted + 软删图片记录。
// 所在磁盘不支持废纸篓时返回 ErrTrashUnsupportedVolume，记录与条目保持原状。
func (s *ImageService) DeleteImage(id uint, deleteFile bool) error {
	imagePathMutationMu.RLock()
	defer imagePathMutationMu.RUnlock()
	return s.deleteImage(id, deleteFile)
}

// BatchDeleteImages 逐张删除并按项记录失败原因，无顶层 error。
func (s *ImageService) BatchDeleteImages(imageIDs []uint, deleteFile bool) *BatchImageOperationResult {
	result := newBatchImageOperationResult(imageIDs)
	batchID := newDeleteBatchID()
	for _, imageID := range imageIDs {
		imagePathMutationMu.RLock()
		_, err := s.deleteImageBatchItem(imageID, deleteFile, batchID)
		imagePathMutationMu.RUnlock()
		result.record(imageID, err)
	}
	return result
}

// BatchDeleteImagesInDirectory 把某个文件夹里的图片整体移入回收站。只处理这个目录
// 直属的图片，不递归子目录——文件夹视图本来就是按直属目录分组的，递归会删掉用户
// 在界面上根本没看到的东西。磁盘上的目录本身不动。
func (s *ImageService) BatchDeleteImagesInDirectory(directory string, deleteFile bool) (*BatchImageOperationResult, error) {
	ids, err := s.imageIDsInDirectory(directory)
	if err != nil {
		return nil, err
	}
	return s.BatchDeleteImages(ids, deleteFile), nil
}

// imageIDsInDirectory 返回目录直属的活跃图片 ID；目录为空或没有图片时报错。
func (s *ImageService) imageIDsInDirectory(directory string) ([]uint, error) {
	cleaned := strings.TrimSpace(directory)
	if cleaned == "" {
		return nil, fmt.Errorf("目录为空")
	}
	var ids []uint
	if err := database.DB.Model(&models.Image{}).
		Where("directory = ?", cleaned).
		Order("id ASC").
		Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("目录里没有可删除的图片：%s", cleaned)
	}
	return ids, nil
}

// OpenImageDirectory 打开图片目录。只接受库里确实存在图片的目录，
// 避免把"用系统默认程序打开任意路径"变成一个无约束的接口。
func (s *ImageService) OpenImageDirectory(directory string) error {
	cleaned := strings.TrimSpace(directory)
	if cleaned == "" {
		return fmt.Errorf("目录为空")
	}
	// 含软删除行：清理审阅会把刚删掉的成员留在结果里，这时"打开目录"仍应可用。
	var count int64
	if err := database.DB.Unscoped().Model(&models.Image{}).Where("directory = ?", cleaned).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("目录不在图片库中：%s", cleaned)
	}
	if _, err := os.Stat(cleaned); err != nil {
		return fmt.Errorf("目录不可访问：%w", err)
	}
	return openPath(cleaned, true)
}

// RevealImage 在系统文件管理器里定位到这张图片本身。
func (s *ImageService) RevealImage(imageID uint) error {
	var image models.Image
	if err := database.DB.Unscoped().First(&image, imageID).Error; err != nil {
		return fmt.Errorf("图片不存在：%w", err)
	}
	if _, err := os.Stat(image.Path); err != nil {
		return fmt.Errorf("源文件不可访问（可能已移入回收站）：%w", err)
	}
	return revealPath(image.Path)
}

// ListImageTrashEntries 按最新删除优先返回可恢复条目。墓碑（trashStateRemoved，修复 I I-A）不列出。
func (s *ImageService) ListImageTrashEntries() ([]models.ImageTrashEntry, error) {
	var entries []models.ImageTrashEntry
	if err := database.DB.
		Where("state <> ?", trashStateRemoved).
		Order("created_at DESC, id DESC").
		Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("列出图片回收站条目失败: %w", err)
	}
	return entries, nil
}

// RestoreImageTrashEntry 将一个软删除图片恢复到原路径。
func (s *ImageService) RestoreImageTrashEntry(entryID uint) (*models.Image, error) {
	imagePathMutationMu.Lock()
	defer imagePathMutationMu.Unlock()

	var entry models.ImageTrashEntry
	if err := database.DB.First(&entry, entryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errTrashEntryNotFound(entryID)
		}
		return nil, fmt.Errorf("读取图片回收站条目失败: %w", err)
	}
	// 墓碑与不存在的条目同一个错误（修复 L m6）。
	if entry.State == trashStateRemoved {
		return nil, errTrashEntryNotFound(entry.ID)
	}
	if entry.State == trashStatePendingMove || entry.State == trashStateRollback {
		return s.cancelInterruptedImageDeletion(&entry)
	}
	return s.restoreImageTrashEntry(&entry)
}

// ReconcileImageTrashEntries 恢复上次进程中断时尚未完成的文件与数据库操作：
// pending_move 未移文件回滚取消、已移文件补提交；restoring 续做恢复；rollback 完成回滚。
func (s *ImageService) ReconcileImageTrashEntries() error {
	imagePathMutationMu.Lock()
	defer imagePathMutationMu.Unlock()

	var entries []models.ImageTrashEntry
	if err := database.DB.Where("state IN ?", []string{trashStatePendingMove, trashStateRestoring, trashStateRollback}).Find(&entries).Error; err != nil {
		return err
	}
	var reconcileErrors []error
	for idx := range entries {
		entry := &entries[idx]
		var err error
		switch entry.State {
		case trashStatePendingMove:
			_, err = s.reconcileImagePendingDelete(entry)
		case trashStateRestoring:
			_, err = s.restoreImageTrashEntry(entry)
		case trashStateRollback:
			err = reconcileImageTrashRollback(entry)
		}
		if err != nil {
			_ = recordImageTrashEntryError(entry.ID, err)
			reconcileErrors = append(reconcileErrors, fmt.Errorf("图片回收站条目 %d 对账失败: %w", entry.ID, err))
		}
	}
	// 旧版 trash/ 目录已不存在的墓碑不再需要登记，清理掉（修复 L m4）。
	if err := sweepGoneTrashTombstones(imageTrashKind); err != nil {
		reconcileErrors = append(reconcileErrors, err)
	}
	return errors.Join(reconcileErrors...)
}

func (s *ImageService) deleteImage(id uint, deleteFile bool) error {
	_, err := s.deleteImageBatchItem(id, deleteFile, newDeleteBatchID())
	return err
}

// deleteImageBatchItem 是带批次标识与结果码的单项删除，语义与 deleteVideoRecordBatch 一致：
// deleteFile=false 建 record_only 条目（记录身份，不再是「不建条目」）；deleteFile=true 且文件在
// 走系统废纸篓；文件不在则 mode=missing。建条目的每条路径都写非空 mode 与 delete_batch_id。
func (s *ImageService) deleteImageBatchItem(id uint, deleteFile bool, batchID string) (string, error) {
	if batchID == "" {
		batchID = newDeleteBatchID()
	}
	var image models.Image
	if err := database.DB.First(&image, id).Error; err != nil {
		return "", err
	}
	var existingEntry models.ImageTrashEntry
	existingResult := database.DB.Where("image_id = ?", image.ID).Limit(1).Find(&existingEntry)
	if existingResult.Error != nil {
		return "", fmt.Errorf("检查既有回收站条目失败: %w", existingResult.Error)
	}
	if existingResult.RowsAffected == 1 {
		switch existingEntry.State {
		case trashStatePendingMove:
			completed, reconcileErr := s.reconcileImagePendingDelete(&existingEntry)
			if reconcileErr != nil {
				_ = recordImageTrashEntryError(existingEntry.ID, reconcileErr)
				return "", fmt.Errorf("处理上次中断删除失败: %w", reconcileErr)
			}
			if completed {
				return TrashResultOK, nil
			}
		case trashStateRollback:
			if reconcileErr := reconcileImageTrashRollback(&existingEntry); reconcileErr != nil {
				_ = recordImageTrashEntryError(existingEntry.ID, reconcileErr)
				return "", fmt.Errorf("完成上次删除回滚失败: %w", reconcileErr)
			}
		case trashStateRemoved:
			// 上次恢复成功、旧版 trash/ 里的残留名字没清掉时留下的墓碑（修复 L m5）：先补做清理、删掉墓碑，再照常删除。
			if err := settleRestoredTrashTombstone(imageTrashKind, existingEntry.ID, existingEntry.Mode, existingEntry.FileMoved,
				existingEntry.OriginalPath, existingEntry.TrashPath); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("图片已有回收站条目，不能重复删除: %d", existingEntry.ID)
		}
	}

	entry := models.ImageTrashEntry{
		DeletedBy:     "user",
		FileSize:      image.Size,
		ImageID:       image.ID,
		ImageName:     image.Name,
		OriginalPath:  image.Path,
		State:         trashStateDeleted,
		DeleteBatchID: batchID,
	}
	sourceInfo, err := os.Stat(image.Path)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("检查待删除文件失败: %w", err)
	}
	if err == nil {
		if sourceInfo.IsDir() {
			return "", fmt.Errorf("图片路径不是文件: %s", image.Path)
		}
		entry.FileSize = sourceInfo.Size()
		entry.FileModTime = sourceInfo.ModTime().UnixNano()
		entry.FileIdentity = stableFileIdentity(sourceInfo)
	} else {
		sourceInfo = nil
	}

	code := TrashResultOK
	switch {
	case sourceInfo == nil && deleteFile:
		if err := mediaPathUnavailable(image.Path); err != nil {
			return "", err
		}
		entry.Mode = models.TrashModeMissing
		code = TrashResultFileMissing
	case sourceInfo == nil, !deleteFile, isRecordedTrashPath(image.Path):
		// 只有确实登记过的回收站位置才降级为只删记录；启发式旧版 trash/ 目录不算（I-3）。
		entry.Mode = models.TrashModeRecordOnly
	default:
		entry.Mode = models.TrashModeTrash
	}

	if entry.Mode != models.TrashModeTrash {
		return code, database.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&entry).Error; err != nil {
				return err
			}
			return softDeleteImageByUserTx(tx, &image)
		})
	}
	return s.deleteImageToSystemTrash(&image, &entry)
}

// softDeleteImageByUserTx 软删图片并把 deleted_by 记为 user、清掉扫描器的恢复标记，
// 让之后的扫描不会撤销这次有意的删除。
func softDeleteImageByUserTx(tx *gorm.DB, image *models.Image) error {
	if err := tx.Model(image).Updates(map[string]interface{}{"deleted_by": "user", "is_stale": false}).Error; err != nil {
		return err
	}
	return tx.Delete(image).Error
}

func (s *ImageService) deleteImageToSystemTrash(image *models.Image, entry *models.ImageTrashEntry) (string, error) {
	trashService := NewTrashService()
	entry.State = trashStatePendingMove
	entry.TrashPath = ""
	if err := database.DB.Create(entry).Error; err != nil {
		return "", fmt.Errorf("记录待删除文件失败: %w", err)
	}
	cancelPending := func() {
		_ = database.DB.Where("id = ? AND state = ?", entry.ID, trashStatePendingMove).Delete(&models.ImageTrashEntry{}).Error
	}

	info, statErr := os.Stat(image.Path)
	if statErr != nil {
		cancelPending()
		return "", fmt.Errorf("检查待删除文件失败: %w", statErr)
	}
	want := trashFileID{Size: entry.FileSize, ModTimeNS: entry.FileModTime, Identity: entry.FileIdentity}
	if !want.strictMatch(info) {
		cancelPending()
		return "", fmt.Errorf("文件在删除过程中发生了变化，已取消删除")
	}

	trashedPath, moveErr := trashService.MoveToTrash(image.Path)
	if errors.Is(moveErr, errTrashLocationUnknown) {
		// 文件已进废纸篓但系统没回报位置：保留条目，走崩溃恢复的按身份查找（Minor 1）。
		completed, recErr := s.reconcileImagePendingTrashDelete(entry)
		if recErr != nil {
			return "", fmt.Errorf("文件已移入废纸篓，但无法确认位置: %w", recErr)
		}
		if !completed {
			return "", fmt.Errorf("移动文件到回收站失败: %w", moveErr)
		}
		return TrashResultOK, nil
	}
	if moveErr != nil {
		cancelPending()
		if errors.Is(moveErr, os.ErrNotExist) {
			// 卷此刻离线时不是「文件没了」，不降级（I6）。
			if err := mediaPathUnavailable(image.Path); err != nil {
				return "", err
			}
			entry.ID = 0
			entry.CreatedAt = time.Time{}
			entry.UpdatedAt = time.Time{}
			entry.State = trashStateDeleted
			entry.Mode = models.TrashModeMissing
			err := database.Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(entry).Error; err != nil {
					return err
				}
				return softDeleteImageByUserTx(tx, image)
			})
			return TrashResultFileMissing, err
		}
		if errors.Is(moveErr, ErrTrashUnsupportedVolume) || errors.Is(moveErr, ErrTrashPermissionDenied) {
			return "", moveErr
		}
		return "", fmt.Errorf("移动文件到回收站失败: %w", moveErr)
	}

	entry.TrashPath = trashedPath
	if err := recordTrashedPath("image_trash_entries", entry.ID, trashedPath); err != nil {
		if errors.Is(err, errTrashEntryStateChanged) {
			// 条目状态已被他方改变：先重读，他方已终结就不回滚文件（Minor 2）。
			var current models.ImageTrashEntry
			reread := database.DB.Where("id = ?", entry.ID).Limit(1).Find(&current)
			if reread.Error == nil && reread.RowsAffected == 1 {
				if current.State == trashStateDeleted {
					return TrashResultOK, nil
				}
				// 文件已进废纸篓，条目状态被并发操作改变：如实说明，不回滚文件（Minor 12）。
				return "", fmt.Errorf("文件已移入废纸篓，但删除记录的状态已被其他操作改变，数据库状态待对账: %w", err)
			}
			// 条目已不存在（他方取消了删除）：文件却在废纸篓里，落到下面的回滚逻辑把它放回原处。
		}
		if rollbackErr := trashService.RestoreFromTrashVerified(trashedPath, image.Path, want); rollbackErr != nil {
			return "", fmt.Errorf("记录废纸篓位置失败: %w；文件回滚失败，将在下次启动时按文件身份对账: %v", err, rollbackErr)
		}
		cancelPending()
		return "", fmt.Errorf("记录废纸篓位置失败，已撤销删除: %w", err)
	}
	trashInfo, err := os.Stat(trashedPath)
	if err != nil {
		return "", fmt.Errorf("读取回收站文件信息失败: %w", err)
	}
	if !want.strictMatch(trashInfo) {
		return "", ErrTrashIdentityMismatch
	}
	log.Printf("图片已移入系统废纸篓 image_id=%d", image.ID)

	err = database.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.ImageTrashEntry{}).
			Where("id = ? AND state = ?", entry.ID, trashStatePendingMove).
			Updates(map[string]interface{}{
				"state":      trashStateDeleted,
				"file_moved": true,
				"last_error": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("待删除条目状态已变化: %d", entry.ID)
		}
		return softDeleteImageByUserTx(tx, image)
	})
	if err == nil {
		return TrashResultOK, nil
	}
	committed, rolledBack, confirmErr := confirmImageDeleteTransactionOutcome(image.ID, entry.ID)
	if confirmErr != nil {
		_ = recordImageTrashEntryError(entry.ID, fmt.Errorf("删除提交结果无法确认: %w", err))
		return "", fmt.Errorf("删除提交结果无法确认，文件和操作日志已保留供启动对账: %w", err)
	}
	if committed {
		return TrashResultOK, nil
	}
	if !rolledBack {
		_ = recordImageTrashEntryError(entry.ID, fmt.Errorf("删除状态不一致: %w", err))
		return "", fmt.Errorf("删除状态不一致，未执行文件补偿: %w", err)
	}

	_ = database.DB.Model(entry).Where("state = ?", trashStatePendingMove).Update("state", trashStateRollback).Error
	if rollbackErr := trashService.RestoreFromTrashVerified(entry.TrashPath, image.Path, want); rollbackErr != nil {
		_ = recordImageTrashEntryError(entry.ID, rollbackErr)
		return "", fmt.Errorf("删除数据库记录失败: %w；文件回滚失败: %v", err, rollbackErr)
	}
	if cleanupErr := database.DB.Delete(entry).Error; cleanupErr != nil {
		_ = recordImageTrashEntryError(entry.ID, cleanupErr)
		return "", fmt.Errorf("删除数据库记录失败: %w；清理待删除条目失败: %v", err, cleanupErr)
	}
	return "", fmt.Errorf("删除数据库记录失败: %w", err)
}

// DeleteImagesDetailed 是桌面端使用的批量删除：逐项结果码、整批一个 batch_id，可上报进度与取消。
func (s *ImageService) DeleteImagesDetailed(imageIDs []uint, deleteFile bool, opts BatchDeleteOptions) *BatchResult {
	batchID := newDeleteBatchID()
	result := newBatchResult(len(imageIDs), batchID)
	cancelled, finish := opts.begin()
	defer finish()
	for index, imageID := range imageIDs {
		if cancelled() {
			result.addCode(imageID, TrashResultCancelled, "")
			continue
		}
		imagePathMutationMu.RLock()
		code, err := s.deleteImageBatchItem(imageID, deleteFile, batchID)
		imagePathMutationMu.RUnlock()
		result.addOutcome(imageID, code, err)
		opts.report(index+1, len(imageIDs))
	}
	return result
}

// DeleteImagesInDirectoryDetailed 与 BatchDeleteImagesInDirectory 选取同一批图片（目录直属、不递归）。
func (s *ImageService) DeleteImagesInDirectoryDetailed(directory string, deleteFile bool, opts BatchDeleteOptions) (*BatchResult, error) {
	ids, err := s.imageIDsInDirectory(directory)
	if err != nil {
		return nil, err
	}
	return s.DeleteImagesDetailed(ids, deleteFile, opts), nil
}

// PermanentlyDeleteImages 永久删除图片文件与记录，只用于 trash_unsupported 之后用户明确选择
// 「永久删除」。流程：身份核对 → 删文件 → 硬删记录（级联）。
func (s *ImageService) PermanentlyDeleteImages(imageIDs []uint) *BatchResult {
	result := newBatchResult(len(imageIDs), "")
	for _, imageID := range imageIDs {
		imagePathMutationMu.Lock()
		code, err := s.permanentlyDeleteImage(imageID)
		imagePathMutationMu.Unlock()
		result.addOutcome(imageID, code, err)
	}
	return result
}

func (s *ImageService) permanentlyDeleteImage(id uint) (string, error) {
	// 只接受活跃记录（默认 scope），并拒绝已有回收站条目的图片（I5）。
	var image models.Image
	if err := database.DB.First(&image, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrPermanentDeleteNotActive
		}
		return "", err
	}
	var existingEntry models.ImageTrashEntry
	existingResult := database.DB.Where("image_id = ?", image.ID).Limit(1).Find(&existingEntry)
	if existingResult.Error != nil {
		return "", fmt.Errorf("检查回收站条目失败: %w", existingResult.Error)
	}
	if existingResult.RowsAffected == 1 {
		if existingEntry.State != trashStateRemoved {
			return "", ErrPermanentDeleteHasTrashEntry
		}
		// 恢复成功后留下的墓碑（修复 L m5）：先补做残留清理、删掉墓碑；清不掉就不删文件。
		if err := settleRestoredTrashTombstone(imageTrashKind, existingEntry.ID, existingEntry.Mode, existingEntry.FileMoved,
			existingEntry.OriginalPath, existingEntry.TrashPath); err != nil {
			return "", err
		}
	}
	code := TrashResultOK
	info, err := os.Stat(image.Path)
	switch {
	case err == nil:
		if info.IsDir() {
			return "", fmt.Errorf("图片路径不是文件")
		}
		if image.Size != 0 && info.Size() != image.Size {
			return "", ErrTrashIdentityMismatch
		}
		if err := os.Remove(image.Path); err != nil {
			if errors.Is(err, os.ErrPermission) {
				return "", ErrTrashPermissionDenied
			}
			return "", fmt.Errorf("删除文件失败: %w", err)
		}
	case os.IsNotExist(err):
		if err := mediaPathUnavailable(image.Path); err != nil {
			return "", err
		}
		code = TrashResultFileMissing
	default:
		return "", fmt.Errorf("检查待删除文件失败: %w", err)
	}
	if err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("image_id = ?", image.ID).Delete(&models.ImageTrashEntry{}).Error; err != nil {
			return err
		}
		return hardDeleteImageTx(tx, image.ID)
	}); err != nil {
		return "", fmt.Errorf("删除数据库记录失败: %w", err)
	}
	log.Printf("图片永久删除 image_id=%d code=%s", image.ID, code)
	return code, nil
}

func (s *ImageService) restoreImageTrashEntry(entry *models.ImageTrashEntry) (*models.Image, error) {
	var image models.Image
	if err := database.DB.Unscoped().First(&image, entry.ImageID).Error; err != nil {
		return nil, fmt.Errorf("读取已删除图片失败: %w", err)
	}
	if !image.DeletedAt.IsValid() {
		return nil, fmt.Errorf("图片记录当前不是已删除状态: %d", image.ID)
	}
	if entry.State == models.TrashStateFileGone {
		return nil, ErrTrashFileGone
	}
	// 墓碑（修复 I I-A）：记录已被「移除记录」移除，对回收站接口一律按不存在处理（修复 L m6）。
	if entry.State == trashStateRemoved {
		return nil, errTrashEntryNotFound(entry.ID)
	}
	// 原路径被新的活跃记录复用时拒绝恢复（设计 4.5.4：部分唯一索引语义前置成明确报错）。
	var occupant models.Image
	occupantResult := database.DB.Where("path = ? AND id != ?", entry.OriginalPath, entry.ImageID).Limit(1).Find(&occupant)
	if occupantResult.Error != nil {
		return nil, fmt.Errorf("检查原路径活跃记录失败: %w", occupantResult.Error)
	}
	if occupantResult.RowsAffected == 1 {
		// 恢复中断的行：文件不在原处就退回 deleted，不让它卡在 restoring（修复 L m2）。
		if entry.State == trashStateRestoring {
			if err := releaseOccupiedRestoringEntry(imageTrashKind, entry.ID, imageEntryFacts(*entry), entry.OriginalPath); err != nil {
				return nil, err
			}
		}
		return nil, ErrTrashPathOccupied
	}

	if entry.State == trashStateDeleted {
		result := database.DB.Model(entry).
			Where("state = ?", trashStateDeleted).
			Updates(map[string]interface{}{"state": trashStateRestoring, "last_error": ""})
		if result.Error != nil {
			return nil, fmt.Errorf("标记恢复状态失败: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return nil, fmt.Errorf("回收站条目状态已变化: %d", entry.ID)
		}
		entry.State = trashStateRestoring
	} else if entry.State != trashStateRestoring {
		return nil, fmt.Errorf("回收站条目当前不可恢复: %s", entry.State)
	}

	// movedFromTrash 只在这一次把文件从废纸篓移回原处时为 true；事务失败时只有这种情形才补偿（I-1）。
	movedFromTrash := false
	trashService := NewTrashService()
	if entry.Mode == models.TrashModeRecordOnly {
		// 只删记录：文件一直在原地没动过，恢复只还原数据库，不碰文件。
	} else if entry.FileMoved {
		var err error
		movedFromTrash, err = ensureImageTrashEntryFileRestored(trashService, *entry)
		if err != nil {
			_ = markImageTrashEntryRecoverable(entry.ID, err)
			return nil, err
		}
	} else if info, err := os.Stat(entry.OriginalPath); err != nil {
		restoreErr := fmt.Errorf("原文件不可用，无法恢复记录: %w", err)
		if os.IsNotExist(err) {
			if unavailable := mediaPathUnavailable(entry.OriginalPath); unavailable != nil {
				restoreErr = unavailable
			}
		}
		_ = markImageTrashEntryRecoverable(entry.ID, restoreErr)
		return nil, restoreErr
	} else if info.IsDir() {
		restoreErr := fmt.Errorf("原路径不是图片文件: %s", entry.OriginalPath)
		_ = markImageTrashEntryRecoverable(entry.ID, restoreErr)
		return nil, restoreErr
	} else if !imageTrashEntryFileMatches(entry.OriginalPath, info, *entry) {
		restoreErr := fmt.Errorf("原路径文件与删除记录不一致: %s", entry.OriginalPath)
		_ = markImageTrashEntryRecoverable(entry.ID, restoreErr)
		return nil, restoreErr
	}

	// 旧版 trash/ 里还留着同一个文件的另一个名字时，事务里条目改为墓碑，提交之后清理成功才删掉（修复 L m5）。
	residue := legacyRestoreResidue(entry.Mode, entry.FileMoved, entry.OriginalPath, entry.TrashPath)
	var restored models.Image
	err := database.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.Image{}).
			Unscoped().
			Where("id = ? AND deleted_at IS NOT NULL", image.ID).
			Updates(map[string]interface{}{"deleted_at": nil, "is_stale": false})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("图片记录已不再处于可恢复状态: %d", image.ID)
		}
		if err := tx.Preload("Tags").First(&restored, image.ID).Error; err != nil {
			return err
		}
		if residue {
			return retireRestoredEntryAsTombstoneTx(tx, imageTrashKind, entry.ID)
		}
		return tx.Delete(entry).Error
	})
	if err != nil {
		committed, rolledBack, confirmErr := confirmImageRestoreTransactionOutcome(image.ID, entry.ID)
		if confirmErr != nil {
			_ = recordImageTrashEntryError(entry.ID, fmt.Errorf("恢复提交结果无法确认: %w", err))
			return nil, fmt.Errorf("恢复提交结果无法确认，已保留当前文件和恢复日志供启动对账: %w", err)
		}
		if committed {
			finishRestoredLegacyResidue(imageTrashKind, residue, entry.ID, entry.Mode, entry.FileMoved, entry.OriginalPath, entry.TrashPath)
			if loadErr := database.DB.Preload("Tags").First(&restored, image.ID).Error; loadErr != nil {
				return nil, fmt.Errorf("恢复已提交，但读取结果失败: %w", loadErr)
			}
			return &restored, nil
		}
		if !rolledBack {
			_ = recordImageTrashEntryError(entry.ID, fmt.Errorf("恢复状态不一致: %w", err))
			return nil, fmt.Errorf("恢复状态不一致，未执行文件补偿: %w", err)
		}
		if movedFromTrash {
			if rollbackErr := trashService.RestoreFromTrash(entry.OriginalPath, entry.TrashPath); rollbackErr != nil {
				_ = recordImageTrashEntryError(entry.ID, rollbackErr)
				return nil, fmt.Errorf("恢复数据库记录失败: %w；文件回滚失败: %v", err, rollbackErr)
			}
		}
		_ = database.DB.Model(entry).Updates(map[string]interface{}{"state": trashStateDeleted, "last_error": err.Error()}).Error
		return nil, fmt.Errorf("恢复数据库记录失败: %w", err)
	}
	finishRestoredLegacyResidue(imageTrashKind, residue, entry.ID, entry.Mode, entry.FileMoved, entry.OriginalPath, entry.TrashPath)
	return &restored, nil
}

// cancelInterruptedImageDeletion 取消一次在 pending_move/rollback 中断的删除：
// 文件回到原路径、条目物理删除、图片记录保持活跃。
func (s *ImageService) cancelInterruptedImageDeletion(entry *models.ImageTrashEntry) (*models.Image, error) {
	if !isLegacyTrashMode(entry.Mode) && entry.State == trashStatePendingMove {
		return s.cancelInterruptedImageTrashDeletion(entry)
	}
	var image models.Image
	if err := database.DB.Preload("Tags").First(&image, entry.ImageID).Error; err != nil {
		return nil, fmt.Errorf("读取活动图片失败: %w", err)
	}
	originalInfo, originalExists, err := regularFileState(entry.OriginalPath)
	if err != nil {
		_ = recordImageTrashEntryError(entry.ID, err)
		return nil, err
	}
	trashInfo, trashExists, err := regularFileState(entry.TrashPath)
	if err != nil {
		_ = recordImageTrashEntryError(entry.ID, err)
		return nil, err
	}
	if originalExists {
		if !imageTrashEntryFileMatches(entry.OriginalPath, originalInfo, *entry) {
			err := fmt.Errorf("原路径已被其他文件占用: %s", entry.OriginalPath)
			_ = recordImageTrashEntryError(entry.ID, err)
			return nil, err
		}
		if trashExists && os.SameFile(originalInfo, trashInfo) {
			// 只有两侧是同一个普通文件的两个硬链接时才删 trash/ 里的残留名字（m1）。
			if !hardLinkedRegularNames(entry.OriginalPath, entry.TrashPath) {
				_ = recordImageTrashEntryError(entry.ID, errLegacyResidueNotHardLink)
				return nil, errLegacyResidueNotHardLink
			}
			if err := os.Remove(entry.TrashPath); err != nil {
				_ = recordImageTrashEntryError(entry.ID, err)
				return nil, err
			}
		}
	} else {
		if !trashExists {
			err := fmt.Errorf("原路径与回收站路径均不存在文件")
			_ = recordImageTrashEntryError(entry.ID, err)
			return nil, err
		}
		if !imageTrashEntryFileMatches(entry.TrashPath, trashInfo, *entry) {
			err := fmt.Errorf("回收站文件与删除记录不一致: %s", entry.TrashPath)
			_ = recordImageTrashEntryError(entry.ID, err)
			return nil, err
		}
		if err := NewTrashService().RestoreFromTrash(entry.TrashPath, entry.OriginalPath); err != nil {
			_ = recordImageTrashEntryError(entry.ID, err)
			return nil, err
		}
	}
	if err := database.DB.Delete(entry).Error; err != nil {
		return nil, fmt.Errorf("清理中断删除日志失败: %w", err)
	}
	return &image, nil
}

// cancelInterruptedImageTrashDeletion 撤销一次 mode=trash 中断在 pending_move 的删除，语义同视频侧。
func (s *ImageService) cancelInterruptedImageTrashDeletion(entry *models.ImageTrashEntry) (*models.Image, error) {
	var image models.Image
	if err := database.DB.Preload("Tags").First(&image, entry.ImageID).Error; err != nil {
		return nil, fmt.Errorf("读取活动图片失败: %w", err)
	}
	want := trashFileID{Size: entry.FileSize, ModTimeNS: entry.FileModTime, Identity: entry.FileIdentity}
	location, foundPath, err := resolvePendingTrashMove(entry.OriginalPath, entry.TrashPath, want)
	if err != nil {
		_ = recordImageTrashEntryError(entry.ID, err)
		return nil, err
	}
	if location == pendingFileUnknown {
		if err := mediaPathUnavailable(entry.OriginalPath); err != nil {
			return nil, err
		}
		_ = recordImageTrashEntryError(entry.ID, ErrTrashFileGone)
		return nil, ErrTrashFileGone
	}
	if location == pendingFileInTrash {
		if err := NewTrashService().RestoreFromTrashVerified(foundPath, entry.OriginalPath, want); err != nil {
			_ = recordImageTrashEntryError(entry.ID, err)
			return nil, err
		}
	}
	if err := database.DB.Delete(entry).Error; err != nil {
		return nil, fmt.Errorf("清理中断删除日志失败: %w", err)
	}
	return &image, nil
}

// reconcileImagePendingDelete 对账 pending_move 条目：文件未移动则取消本次删除
// （删除条目，图片记录保持活跃，completed=false）；文件已移入回收站则补提交事务
// （置 deleted + 软删图片，completed=true）。
func (s *ImageService) reconcileImagePendingDelete(entry *models.ImageTrashEntry) (bool, error) {
	if !isLegacyTrashMode(entry.Mode) {
		return s.reconcileImagePendingTrashDelete(entry)
	}
	var image models.Image
	if err := database.DB.Unscoped().First(&image, entry.ImageID).Error; err != nil {
		return false, err
	}
	originalInfo, originalExists, err := regularFileState(entry.OriginalPath)
	if err != nil {
		return false, err
	}
	trashInfo, trashExists, err := regularFileState(entry.TrashPath)
	if err != nil {
		return false, err
	}
	if originalExists {
		if !imageTrashEntryFileMatches(entry.OriginalPath, originalInfo, *entry) {
			return false, fmt.Errorf("原文件与待删除记录不一致: %s", entry.OriginalPath)
		}
		if trashExists && os.SameFile(originalInfo, trashInfo) {
			// 只有两侧是同一个普通文件的两个硬链接时才删 trash/ 里的残留名字（m1）。
			if !hardLinkedRegularNames(entry.OriginalPath, entry.TrashPath) {
				return false, errLegacyResidueNotHardLink
			}
			if err := os.Remove(entry.TrashPath); err != nil {
				return false, fmt.Errorf("清理中断删除的回收站副本失败: %w", err)
			}
		}
		if err := database.DB.Delete(entry).Error; err != nil {
			return false, fmt.Errorf("取消中断删除失败: %w", err)
		}
		log.Printf("图片删除中断且文件未移动，已取消 image_id=%d path=%s", entry.ImageID, entry.OriginalPath)
		return false, nil
	}
	if !trashExists {
		return false, fmt.Errorf("原路径与回收站路径均不存在文件")
	}
	if !imageTrashEntryFileMatches(entry.TrashPath, trashInfo, *entry) {
		return false, fmt.Errorf("回收站文件与待删除记录不一致: %s", entry.TrashPath)
	}
	err = database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(entry).Updates(map[string]interface{}{"state": trashStateDeleted, "file_moved": true, "last_error": ""}).Error; err != nil {
			return err
		}
		if err := tx.Model(&image).Update("deleted_by", entry.DeletedBy).Error; err != nil {
			return err
		}
		return tx.Delete(&image).Error
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// reconcileImagePendingTrashDelete 是 mode=trash 的 pending_move 崩溃恢复三分支，语义同视频侧
// reconcilePendingTrashDelete：文件没动 → 取消；已进废纸篓（按记录的路径或身份找到）→ 补提交；
// 位置未知 → 置 deleted 并写「文件位置未知」。
func (s *ImageService) reconcileImagePendingTrashDelete(entry *models.ImageTrashEntry) (bool, error) {
	var image models.Image
	if err := database.DB.Unscoped().First(&image, entry.ImageID).Error; err != nil {
		return false, err
	}
	want := trashFileID{Size: entry.FileSize, ModTimeNS: entry.FileModTime, Identity: entry.FileIdentity}
	location, foundPath, err := resolvePendingTrashMove(entry.OriginalPath, entry.TrashPath, want)
	if err != nil {
		return false, err
	}
	if location == pendingFileUnknown {
		// 卷离线（或因权限读不到）时废纸篓里也读不到：保持 pending_move 等卷回来（I1、m3）。
		if err := mediaPathUnavailable(entry.OriginalPath); err != nil {
			return false, err
		}
	}
	if location == pendingFileAtOriginal {
		result := database.DB.Where("id = ? AND state = ?", entry.ID, trashStatePendingMove).Delete(&models.ImageTrashEntry{})
		if result.Error != nil {
			return false, fmt.Errorf("取消中断删除失败: %w", result.Error)
		}
		log.Printf("图片删除中断且文件未移动，已取消 image_id=%d", entry.ImageID)
		return false, nil
	}
	updates := map[string]interface{}{"state": trashStateDeleted, "file_moved": true, "last_error": ""}
	if location == pendingFileUnknown {
		updates = map[string]interface{}{"state": trashStateDeleted, "file_moved": false, "trash_path": "", "last_error": trashUnknownLocationMessage}
	}
	err = database.Transaction(func(tx *gorm.DB) error {
		if location == pendingFileInTrash && foundPath != entry.TrashPath {
			if err := claimTrashPathTx(tx, "image_trash_entries", entry.ID, foundPath); err != nil {
				return err
			}
			updates["trash_path"] = foundPath
		}
		result := tx.Model(&models.ImageTrashEntry{}).
			Where("id = ? AND state = ?", entry.ID, trashStatePendingMove).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("待删除条目状态已变化: %d", entry.ID)
		}
		return softDeleteImageByUserTx(tx, &image)
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func reconcileImageTrashRollback(entry *models.ImageTrashEntry) error {
	trashService := NewTrashService()
	originalInfo, originalExists, err := regularFileState(entry.OriginalPath)
	if err != nil {
		return err
	}
	trashInfo, trashExists, err := regularFileState(entry.TrashPath)
	if err != nil {
		return err
	}
	if originalExists && trashExists {
		if os.SameFile(originalInfo, trashInfo) {
			// 只有两侧是同一个普通文件的两个硬链接时才删 trash/ 里的名字（m1）。
			if !hardLinkedRegularNames(entry.OriginalPath, entry.TrashPath) {
				return errLegacyResidueNotHardLink
			}
			if err := os.Remove(entry.TrashPath); err != nil {
				return fmt.Errorf("清理回滚后的回收站副本失败: %w", err)
			}
			trashExists = false
		} else {
			return fmt.Errorf("回滚时原路径已被其他文件占用")
		}
	}
	if !originalExists {
		if !trashExists {
			return fmt.Errorf("回滚时原路径与回收站路径均不存在文件")
		}
		if err := trashService.RestoreFromTrash(entry.TrashPath, entry.OriginalPath); err != nil {
			return err
		}
	} else if !imageTrashEntryFileMatches(entry.OriginalPath, originalInfo, *entry) {
		return fmt.Errorf("回滚后的原文件与删除记录不一致")
	}
	return database.DB.Delete(entry).Error
}

func movePendingImageTrashEntryFile(entry *models.ImageTrashEntry, trashService *TrashService) error {
	for attempt := 0; attempt < 10000; attempt++ {
		info, err := os.Stat(entry.OriginalPath)
		if err != nil {
			return err
		}
		if !imageTrashEntryFileMatches(entry.OriginalPath, info, *entry) {
			return fmt.Errorf("原文件与待删除记录的强身份不一致: %s", entry.OriginalPath)
		}
		if err := trashService.MoveToTrashAt(entry.OriginalPath, entry.TrashPath); err == nil {
			return nil
		} else if !errors.Is(err, ErrTrashTargetExists) {
			return err
		}

		updatedPath := false
		for nextAttempt := attempt + 1; nextAttempt < 10000; nextAttempt++ {
			nextPath := trashService.TrashTargetPath(entry.OriginalPath, nextAttempt)
			result := database.DB.Model(entry).
				Where("state = ?", trashStatePendingMove).
				Update("trash_path", nextPath)
			if result.Error == nil && result.RowsAffected == 1 {
				entry.TrashPath = nextPath
				attempt = nextAttempt - 1
				updatedPath = true
				break
			}
			if result.Error == nil {
				return fmt.Errorf("待删除条目状态已变化: %d", entry.ID)
			}
			if result.Error != nil && !imageTrashPathAlreadyRecorded(nextPath) {
				return result.Error
			}
		}
		if !updatedPath {
			return fmt.Errorf("无法记录新的回收站路径: %s", entry.OriginalPath)
		}
	}
	return fmt.Errorf("无法生成未占用的回收站路径: %s", entry.OriginalPath)
}

func imageTrashPathAlreadyRecorded(path string) bool {
	var count int64
	return database.DB.Model(&models.ImageTrashEntry{}).Where("trash_path = ?", path).Count(&count).Error == nil && count > 0
}

// ensureImageTrashEntryFileRestored 与 ensureTrashEntryFileRestored 同义：返回值只在这一次把文件
// 从废纸篓移回原处时为 true；文件本来就在原路径时为 false（I-1）。trash 模式下原路径严格一致即判定
// 「已在原处」、不读废纸篓一侧（I-A）；legacy_trash 旧行按大小 + inode 同样判定（I-1）；硬链接不删废纸篓
// 那个名字（M1）；原路径用 Lstat 读取，是符号链接时返回 ErrTrashOriginalNotRegular、什么都不动（m1）。
func ensureImageTrashEntryFileRestored(trashService *TrashService, entry models.ImageTrashEntry) (bool, error) {
	want := trashFileID{Size: entry.FileSize, ModTimeNS: entry.FileModTime, Identity: entry.FileIdentity}
	strict := !isLegacyTrashMode(entry.Mode)
	matches := func(path string, info os.FileInfo) bool {
		if strict {
			return want.strictMatch(info)
		}
		return imageTrashEntryFileMatches(path, info, entry)
	}
	mismatch := func(format string) error {
		if strict {
			return ErrTrashIdentityMismatch
		}
		return fmt.Errorf(format, entry.OriginalPath)
	}

	originalInfo, originalExists, err := originalFileState(entry.OriginalPath)
	if err != nil {
		return false, err
	}
	if originalExists {
		if strict && want.strictMatch(originalInfo) {
			return false, nil
		}
		if !strict && entryFileAtOriginal(imageEntryFacts(entry), originalInfo.Size(), originalInfo.ModTime().UnixNano(), stableFileIdentity(originalInfo)) {
			// 记录了 file_sha256 的 legacy 行先核对内容哈希，不一致不恢复（修复 I m-b）。
			switch content, contentErr := checkLegacyPutBackContent(imageEntryFacts(entry), entry.FileSHA256, entry.OriginalPath); content {
			case legacyContentMismatch:
				return false, mismatch("原路径文件与删除记录不一致: %s")
			case legacyContentUndetermined:
				// 读不出哈希、无法判定（修复 L m1）：显式恢复同样拒绝，什么都不动。
				return false, contentErr
			}
			return false, nil
		}
	}
	trashInfo, trashExists, err := regularFileState(entry.TrashPath)
	if err != nil {
		return false, err
	}
	if originalExists && trashExists {
		if !os.SameFile(originalInfo, trashInfo) {
			return false, ErrTrashPathOccupied
		}
		// 硬链接：原路径上本来就有这个文件。trash 模式走到这里说明它与条目不符（严格一致已在上面返回）。
		if strict {
			return false, ErrTrashIdentityMismatch
		}
		return false, nil
	}
	if originalExists {
		if !matches(entry.OriginalPath, originalInfo) {
			return false, mismatch("原路径文件与删除记录不一致: %s")
		}
		// 文件本来就在原路径：这次没有动它。
		return false, nil
	}
	// 原路径上没有文件时先看卷在不在（Minor 3）：离线时既不能判「废纸篓里的文件已不存在」，
	// 也不能往未挂载卷留下的空挂载点里恢复。读不到（权限）同样不动（m3）。
	if err := trashEntryUnavailable(entry.OriginalPath, entry.TrashPath); err != nil {
		return false, err
	}
	if !trashExists {
		return false, ErrTrashFileGone
	}
	if !matches(entry.TrashPath, trashInfo) {
		return false, mismatch("回收站文件与删除记录不一致: %s")
	}
	if err := trashService.RestoreFromTrash(entry.TrashPath, entry.OriginalPath); err != nil {
		return false, err
	}
	return true, nil
}

// imageTrashEntryFileMatches 按删除时记录的指纹核对文件：大小必须一致；
// 强身份（inode，不含设备号）命中即通过，否则回退 SHA-256 全量比对。
func imageTrashEntryFileMatches(path string, info os.FileInfo, entry models.ImageTrashEntry) bool {
	if info == nil {
		return false
	}
	if entry.FileSize != 0 && info.Size() != entry.FileSize {
		return false
	}
	if entry.FileIdentity != "" && sameFileInode(entry.FileIdentity, info) {
		return true
	}
	if entry.FileSHA256 == "" {
		return false
	}
	digest, err := fileSHA256Hex(path)
	return err == nil && digest == entry.FileSHA256
}

func confirmImageDeleteTransactionOutcome(imageID uint, entryID uint) (bool, bool, error) {
	var image models.Image
	if err := database.DB.Unscoped().First(&image, imageID).Error; err != nil {
		return false, false, err
	}
	var entry models.ImageTrashEntry
	if err := database.DB.First(&entry, entryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, false, nil
		}
		return false, false, err
	}
	if image.DeletedAt.IsValid() && entry.State == trashStateDeleted {
		return true, false, nil
	}
	if !image.DeletedAt.IsValid() && entry.State == trashStatePendingMove {
		return false, true, nil
	}
	return false, false, nil
}

func confirmImageRestoreTransactionOutcome(imageID uint, entryID uint) (bool, bool, error) {
	var image models.Image
	if err := database.DB.Unscoped().First(&image, imageID).Error; err != nil {
		return false, false, err
	}
	var entry models.ImageTrashEntry
	err := database.DB.First(&entry, entryID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if !image.DeletedAt.IsValid() {
			return true, false, nil
		}
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	// 恢复事务在有旧版残留名字时把条目改为墓碑而不是删掉（修复 L m5）：记录已活跃、条目是墓碑同样是已提交。
	if !image.DeletedAt.IsValid() && entry.State == trashStateRemoved {
		return true, false, nil
	}
	if image.DeletedAt.IsValid() && entry.State == trashStateRestoring {
		return false, true, nil
	}
	return false, false, nil
}

func recordImageTrashEntryError(entryID uint, cause error) error {
	if cause == nil {
		return nil
	}
	return database.DB.Model(&models.ImageTrashEntry{}).Where("id = ?", entryID).Update("last_error", cause.Error()).Error
}

func markImageTrashEntryRecoverable(entryID uint, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return database.DB.Model(&models.ImageTrashEntry{}).
		Where("id = ?", entryID).
		Updates(map[string]interface{}{"state": trashStateDeleted, "last_error": message}).Error
}
