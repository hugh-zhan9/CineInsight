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
	roots := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		root := filepath.Clean(strings.TrimSpace(dir.Path))
		if root == "" || root == "." {
			result.recordError("scan", dir.Path, "", fmt.Errorf("扫描目录为空"))
			continue
		}
		scannedFiles, scanErr := scanImageDirectory(root, extensions, excludedPaths)
		if scanErr != nil {
			result.recordError("scan", root, "", scanErr)
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
			missingImages = append(missingImages, image)
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

	for _, image := range append(duplicateImages, missingImages...) {
		if _, relocated := relocatedImageIDs[image.ID]; relocated {
			continue
		}
		if err := s.deleteImageRecord(image.ID); err != nil {
			result.recordError("delete", image.Directory, image.Path, err)
			continue
		}
		result.Removed++
	}

	log.Printf("图片目录对账完成 dirs=%d scanned=%d added=%d relocated=%d removed=%d skipped=%d errors=%d",
		len(roots), len(scannedByPath), result.Added, result.Relocated, result.Removed, result.Skipped, len(result.Errors))
	return result, nil
}

// scanImageDirectory 按扩展名遍历单个图片目录。过滤链（设计 4.1.2）：
// ScanExcludePaths → 隐藏路径 → trash 目录名 SkipDir → isTrashPath。
func scanImageDirectory(dir string, extensions []string, excludedPaths []string) ([]ScannedFile, error) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "." {
		return nil, fmt.Errorf("扫描根目录为空")
	}
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
		if err != nil {
			return nil // 跳过错误的文件
		}
		if isScanPathExcluded(path, excludedPaths) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if shouldSkipHiddenPath(info) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			if isTrashDirName(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if isTrashPath(path) {
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

	var existingImage models.Image
	if err := database.DB.Unscoped().Where("path = ?", path).First(&existingImage).Error; err == nil {
		log.Printf("跳过已存在图片 path=%s", path)
		return &existingImage, ErrImageExists
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

// deleteImageRecord 失踪对账仅软删记录：不动磁盘文件，不建回收站条目。
func (s *ImageService) deleteImageRecord(id uint) error {
	result := database.DB.Delete(&models.Image{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("图片记录不存在或已删除: %d", id)
	}
	log.Printf("图片记录软删除（失踪对账，不动文件） id=%d", id)
	return nil
}

// ===== 回收站（镜像视频侧四态状态机 pending_move/deleted/restoring/rollback，设计 4.5 / D-008） =====

// DeleteImage 删除图片。deleteFile=false 仅软删记录（不建回收站条目、不动文件）；
// deleteFile=true 走完整状态机：pending_move 条目 → 移文件入回收站 → 指纹校验 →
// 事务内 CAS 置 deleted + 软删图片记录。
func (s *ImageService) DeleteImage(id uint, deleteFile bool) error {
	imagePathMutationMu.RLock()
	defer imagePathMutationMu.RUnlock()
	return s.deleteImage(id, deleteFile)
}

// BatchDeleteImages 逐张删除并按项记录失败原因，无顶层 error。
func (s *ImageService) BatchDeleteImages(imageIDs []uint, deleteFile bool) *BatchImageOperationResult {
	result := newBatchImageOperationResult(imageIDs)
	for _, imageID := range imageIDs {
		result.record(imageID, s.DeleteImage(imageID, deleteFile))
	}
	return result
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

// ListImageTrashEntries 按最新删除优先返回可恢复条目。
func (s *ImageService) ListImageTrashEntries() ([]models.ImageTrashEntry, error) {
	var entries []models.ImageTrashEntry
	if err := database.DB.
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
		return nil, fmt.Errorf("读取图片回收站条目失败: %w", err)
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
	return errors.Join(reconcileErrors...)
}

func (s *ImageService) deleteImage(id uint, deleteFile bool) error {
	var image models.Image
	if err := database.DB.First(&image, id).Error; err != nil {
		return err
	}
	var existingEntry models.ImageTrashEntry
	existingResult := database.DB.Where("image_id = ?", image.ID).Limit(1).Find(&existingEntry)
	if existingResult.Error != nil {
		return fmt.Errorf("检查既有回收站条目失败: %w", existingResult.Error)
	}
	if existingResult.RowsAffected == 1 {
		switch existingEntry.State {
		case trashStatePendingMove:
			completed, reconcileErr := s.reconcileImagePendingDelete(&existingEntry)
			if reconcileErr != nil {
				_ = recordImageTrashEntryError(existingEntry.ID, reconcileErr)
				return fmt.Errorf("处理上次中断删除失败: %w", reconcileErr)
			}
			if completed {
				return nil
			}
		case trashStateRollback:
			if reconcileErr := reconcileImageTrashRollback(&existingEntry); reconcileErr != nil {
				_ = recordImageTrashEntryError(existingEntry.ID, reconcileErr)
				return fmt.Errorf("完成上次删除回滚失败: %w", reconcileErr)
			}
		default:
			return fmt.Errorf("图片已有回收站条目，不能重复删除: %d", existingEntry.ID)
		}
	}

	if !deleteFile {
		return s.deleteImageRecord(image.ID)
	}

	entry := models.ImageTrashEntry{
		ImageID:      image.ID,
		ImageName:    image.Name,
		OriginalPath: image.Path,
		State:        trashStateDeleted,
	}
	sourceInfo, err := os.Stat(image.Path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("检查待删除文件失败: %w", err)
	}
	if err == nil {
		if sourceInfo.IsDir() {
			return fmt.Errorf("图片路径不是文件: %s", image.Path)
		}
		entry.FileSize = sourceInfo.Size()
		entry.FileModTime = sourceInfo.ModTime().UnixNano()
		entry.FileIdentity = stableFileIdentity(sourceInfo)
		entry.FileSHA256, err = fileSHA256Hex(image.Path)
		if err != nil {
			return fmt.Errorf("计算待删除文件摘要失败: %w", err)
		}
	}

	// 源文件本就不存在（或已在回收站内）时不移动文件，仅落条目并软删记录（镜像视频侧）。
	shouldMoveFile := sourceInfo != nil && !isTrashPath(image.Path)
	if !shouldMoveFile {
		return database.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&entry).Error; err != nil {
				return err
			}
			return tx.Delete(&image).Error
		})
	}

	trashService := NewTrashService()
	entry.State = trashStatePendingMove
	if err := createPendingImageTrashEntry(&entry, trashService); err != nil {
		return fmt.Errorf("记录待删除文件失败: %w", err)
	}
	if err := movePendingImageTrashEntryFile(&entry, trashService); err != nil {
		_ = recordImageTrashEntryError(entry.ID, err)
		return fmt.Errorf("移动文件到回收站失败: %w", err)
	}
	trashInfo, err := os.Stat(entry.TrashPath)
	if err != nil {
		return fmt.Errorf("读取回收站文件信息失败: %w", err)
	}
	if !imageTrashEntryFileMatches(entry.TrashPath, trashInfo, entry) {
		return fmt.Errorf("回收站文件与删除前内容不一致: %s", entry.TrashPath)
	}
	log.Printf("图片已移入回收站 src=%s dst=%s", image.Path, entry.TrashPath)

	err = database.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.ImageTrashEntry{}).
			Where("id = ? AND state = ?", entry.ID, trashStatePendingMove).
			Updates(map[string]interface{}{
				"state":       trashStateDeleted,
				"file_moved":  true,
				"file_sha256": entry.FileSHA256,
				"last_error":  "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("待删除条目状态已变化: %d", entry.ID)
		}
		return tx.Delete(&image).Error
	})
	if err == nil {
		return nil
	}
	committed, rolledBack, confirmErr := confirmImageDeleteTransactionOutcome(image.ID, entry.ID)
	if confirmErr != nil {
		_ = recordImageTrashEntryError(entry.ID, fmt.Errorf("删除提交结果无法确认: %w", err))
		return fmt.Errorf("删除提交结果无法确认，文件和操作日志已保留供启动对账: %w", err)
	}
	if committed {
		return nil
	}
	if !rolledBack {
		_ = recordImageTrashEntryError(entry.ID, fmt.Errorf("删除状态不一致: %w", err))
		return fmt.Errorf("删除状态不一致，未执行文件补偿: %w", err)
	}

	_ = database.DB.Model(&entry).Update("state", trashStateRollback).Error
	if rollbackErr := trashService.RestoreFromTrash(entry.TrashPath, image.Path); rollbackErr != nil {
		_ = recordImageTrashEntryError(entry.ID, rollbackErr)
		return fmt.Errorf("删除数据库记录失败: %w；文件回滚失败: %v", err, rollbackErr)
	}
	if cleanupErr := database.DB.Delete(&entry).Error; cleanupErr != nil {
		_ = recordImageTrashEntryError(entry.ID, cleanupErr)
		return fmt.Errorf("删除数据库记录失败: %w；清理待删除条目失败: %v", err, cleanupErr)
	}
	return fmt.Errorf("删除数据库记录失败: %w", err)
}

func (s *ImageService) restoreImageTrashEntry(entry *models.ImageTrashEntry) (*models.Image, error) {
	var image models.Image
	if err := database.DB.Unscoped().First(&image, entry.ImageID).Error; err != nil {
		return nil, fmt.Errorf("读取已删除图片失败: %w", err)
	}
	if !image.DeletedAt.IsValid() {
		return nil, fmt.Errorf("图片记录当前不是已删除状态: %d", image.ID)
	}
	// 原路径被新的活跃记录复用时拒绝恢复（设计 4.5.4：部分唯一索引语义前置成明确报错）。
	var occupant models.Image
	occupantResult := database.DB.Where("path = ? AND id != ?", entry.OriginalPath, entry.ImageID).Limit(1).Find(&occupant)
	if occupantResult.Error != nil {
		return nil, fmt.Errorf("检查原路径活跃记录失败: %w", occupantResult.Error)
	}
	if occupantResult.RowsAffected == 1 {
		return nil, fmt.Errorf("原路径已被其他活跃图片记录占用，拒绝恢复: %s", entry.OriginalPath)
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

	fileAtOriginal := false
	trashService := NewTrashService()
	if entry.FileMoved {
		var err error
		fileAtOriginal, err = ensureImageTrashEntryFileRestored(trashService, *entry)
		if err != nil {
			_ = markImageTrashEntryRecoverable(entry.ID, err)
			return nil, err
		}
	} else if info, err := os.Stat(entry.OriginalPath); err != nil {
		restoreErr := fmt.Errorf("原文件不可用，无法恢复记录: %w", err)
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

	var restored models.Image
	err := database.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.Image{}).
			Unscoped().
			Where("id = ? AND deleted_at IS NOT NULL", image.ID).
			Update("deleted_at", nil)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("图片记录已不再处于可恢复状态: %d", image.ID)
		}
		if err := tx.Preload("Tags").First(&restored, image.ID).Error; err != nil {
			return err
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
			if loadErr := database.DB.Preload("Tags").First(&restored, image.ID).Error; loadErr != nil {
				return nil, fmt.Errorf("恢复已提交，但读取结果失败: %w", loadErr)
			}
			return &restored, nil
		}
		if !rolledBack {
			_ = recordImageTrashEntryError(entry.ID, fmt.Errorf("恢复状态不一致: %w", err))
			return nil, fmt.Errorf("恢复状态不一致，未执行文件补偿: %w", err)
		}
		if fileAtOriginal {
			if rollbackErr := trashService.RestoreFromTrash(entry.OriginalPath, entry.TrashPath); rollbackErr != nil {
				_ = recordImageTrashEntryError(entry.ID, rollbackErr)
				return nil, fmt.Errorf("恢复数据库记录失败: %w；文件回滚失败: %v", err, rollbackErr)
			}
		}
		_ = database.DB.Model(entry).Updates(map[string]interface{}{"state": trashStateDeleted, "last_error": err.Error()}).Error
		return nil, fmt.Errorf("恢复数据库记录失败: %w", err)
	}
	return &restored, nil
}

// cancelInterruptedImageDeletion 取消一次在 pending_move/rollback 中断的删除：
// 文件回到原路径、条目物理删除、图片记录保持活跃。
func (s *ImageService) cancelInterruptedImageDeletion(entry *models.ImageTrashEntry) (*models.Image, error) {
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

// reconcileImagePendingDelete 对账 pending_move 条目：文件未移动则取消本次删除
// （删除条目，图片记录保持活跃，completed=false）；文件已移入回收站则补提交事务
// （置 deleted + 软删图片，completed=true）。
func (s *ImageService) reconcileImagePendingDelete(entry *models.ImageTrashEntry) (bool, error) {
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
		return tx.Delete(&image).Error
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

func createPendingImageTrashEntry(entry *models.ImageTrashEntry, trashService *TrashService) error {
	for attempt := 0; attempt < 10000; attempt++ {
		entry.TrashPath = trashService.TrashTargetPath(entry.OriginalPath, attempt)
		if err := database.DB.Create(entry).Error; err == nil {
			return nil
		} else if !imageTrashPathAlreadyRecorded(entry.TrashPath) {
			return err
		}
		entry.ID = 0
		entry.CreatedAt = time.Time{}
		entry.UpdatedAt = time.Time{}
	}
	return fmt.Errorf("无法记录唯一回收站路径: %s", entry.OriginalPath)
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

func ensureImageTrashEntryFileRestored(trashService *TrashService, entry models.ImageTrashEntry) (bool, error) {
	originalInfo, originalExists, err := regularFileState(entry.OriginalPath)
	if err != nil {
		return false, err
	}
	trashInfo, trashExists, err := regularFileState(entry.TrashPath)
	if err != nil {
		return false, err
	}
	if originalExists && trashExists {
		if os.SameFile(originalInfo, trashInfo) {
			if err := os.Remove(entry.TrashPath); err != nil {
				return false, fmt.Errorf("清理已恢复的回收站副本失败: %w", err)
			}
			return true, nil
		}
		return false, fmt.Errorf("原路径已被占用，拒绝覆盖: %s", entry.OriginalPath)
	}
	if originalExists {
		if !imageTrashEntryFileMatches(entry.OriginalPath, originalInfo, entry) {
			return false, fmt.Errorf("原路径文件与删除记录不一致: %s", entry.OriginalPath)
		}
		return true, nil
	}
	if !trashExists {
		return false, fmt.Errorf("回收站文件不存在: %s", entry.TrashPath)
	}
	if !imageTrashEntryFileMatches(entry.TrashPath, trashInfo, entry) {
		return false, fmt.Errorf("回收站文件与删除记录不一致: %s", entry.TrashPath)
	}
	if err := trashService.RestoreFromTrash(entry.TrashPath, entry.OriginalPath); err != nil {
		return false, err
	}
	return true, nil
}

// imageTrashEntryFileMatches 按删除时记录的指纹核对文件：大小必须一致；
// 强身份（dev:inode）命中即通过，否则回退 SHA-256 全量比对。
func imageTrashEntryFileMatches(path string, info os.FileInfo, entry models.ImageTrashEntry) bool {
	if info == nil {
		return false
	}
	if entry.FileSize != 0 && info.Size() != entry.FileSize {
		return false
	}
	if entry.FileIdentity != "" && stableFileIdentity(info) == entry.FileIdentity {
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
