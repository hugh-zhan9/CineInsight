package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"video-master/database"
	"video-master/models"
	"video-master/services"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// resetImageAITaggingService 在数据库就绪后（启动或恢复失败续跑）重建图片 AI 打标服务。
func (a *App) resetImageAITaggingService() {
	a.imageAITagMu.Lock()
	old := a.imageAITagging
	a.imageAITagging = nil
	a.imageAITagMu.Unlock()
	if old != nil {
		old.StopAndWait()
	}
	if database.DB == nil {
		return
	}
	svc := services.NewImageAITaggingService(database.DB, a.imageThumbnail, services.SettingsAITaggingConfigProvider{})
	svc.SetBackgroundTaskRegistry(a.backgroundTasks)
	if err := svc.RecoverInterruptedImageTagging(); err != nil {
		log.Printf("App startup image AI tagging recovery failed err=%v", err)
	}
	svc.SetEventEmitter(func(status services.ImageAITaggingStatus) {
		if a.ctx != nil && a.ctx.Err() == nil {
			runtime.EventsEmit(a.ctx, "image-ai-tagging-progress", status)
		}
	})
	a.imageAITagMu.Lock()
	a.imageAITagging = svc
	a.imageAITagMu.Unlock()
}

func (a *App) imageAITaggingService() *services.ImageAITaggingService {
	a.imageAITagMu.RLock()
	defer a.imageAITagMu.RUnlock()
	return a.imageAITagging
}

// triggerImageAITaggingAuto 在后台增量打标：目标集与手动启动一致，重复调用由证据指纹拦。
// 配置缺失与运行中都静默跳过，等下次触发或手动启动。
//
// 这是自动路径，先过空闲门（D-030）：门在前、互斥锁在后，等待空闲期间不占着
// 那把只服务于自动触发的锁。
func (a *App) triggerImageAITaggingAuto(reason string) {
	err := a.runGatedAutoTask(string(services.BackgroundTaskImageAITagging), func(ctx context.Context, hook services.TaskPauseHook) error {
		a.startImageAITaggingAuto(ctx, reason, hook)
		return nil
	})
	if err != nil {
		log.Printf("image AI tagging auto-start gate stopped (%s): %v", reason, err)
	}
}

func (a *App) startImageAITaggingAuto(ctx context.Context, reason string, hook services.TaskPauseHook) {
	a.imageAITagAutoMu.Lock()
	defer a.imageAITagAutoMu.Unlock()
	svc := a.imageAITaggingService()
	if svc == nil {
		return
	}
	if ctx == nil || ctx.Err() != nil {
		ctx = context.Background()
	}
	// 装钩子与翻 Running 由服务在自己的锁里一起完成：已经在跑的那一轮可能是用户
	// 显式启动的，服务会拒绝给它补装检查点（D-030 明令禁止把显式任务关进门里）。
	if _, err := svc.StartImageAITaggingWithPauseHook(ctx, hook); err != nil {
		if errors.Is(err, services.ErrImageAITaggingConfigUnavailable) || errors.Is(err, services.ErrImageAITaggingBusy) {
			log.Printf("image AI tagging auto-start skipped (%s): %v", reason, err)
			return
		}
		log.Printf("image AI tagging auto-start failed (%s): %v", reason, err)
		return
	}
	log.Printf("image AI tagging auto-started (%s)", reason)
}

// imageSemanticIndexService 以读锁返回当前图片语义索引服务指针。
func (a *App) imageSemanticIndexService() *services.ImageSemanticIndexService {
	a.imageSemanticMu.RLock()
	defer a.imageSemanticMu.RUnlock()
	return a.imageSemanticIndex
}

// resetImageSemanticIndexService 在数据库就绪后重建图片语义索引服务，镜像 resetSemanticIndexService。
func (a *App) resetImageSemanticIndexService() {
	a.imageSemanticMu.Lock()
	old := a.imageSemanticIndex
	a.imageSemanticIndex = nil
	a.imageSemanticMu.Unlock()
	if old != nil {
		old.StopAndWait()
	}
	if database.DB == nil {
		return
	}
	capability := database.PrepareImageSemanticVectorStorage(database.DB)
	provider := services.SemanticIndexConfigProviderFunc(func() (services.SemanticIndexConfig, error) {
		config, err := (services.SettingsAITaggingConfigProvider{}).Load()
		if err != nil {
			return services.SemanticIndexConfig{}, err
		}
		semanticConfig := services.SemanticIndexConfigFromAITagging(config)
		var settings models.Settings
		if err := database.DB.First(&settings).Error; err == nil && strings.TrimSpace(settings.SemanticEmbeddingModel) != "" {
			semanticConfig.Model = strings.TrimSpace(settings.SemanticEmbeddingModel)
		}
		return semanticConfig, nil
	})
	service := services.NewImageSemanticIndexService(database.DB, capability, provider)
	service.SetBackgroundTaskRegistry(a.backgroundTasks)
	service.SetDesktopNotifier(a.desktopNotify)
	service.SetEventEmitter(func(status services.ImageSemanticIndexStatus) {
		if a.ctx != nil && a.ctx.Err() == nil {
			runtime.EventsEmit(a.ctx, "image-semantic-index-state", status)
		}
	})
	a.imageSemanticMu.Lock()
	a.imageSemanticIndex = service
	a.imageSemanticMu.Unlock()
}

// ===== Image Directory Methods =====

// GetAllImageDirectories 获取所有图片扫描目录
func (a *App) GetAllImageDirectories() ([]models.ImageDirectory, error) {
	dirs, err := a.imageService.GetAllImageDirectories()
	log.Printf("API GetAllImageDirectories result=%d err=%v", len(dirs), err)
	return dirs, err
}

// AddImageDirectory 添加图片扫描目录
func (a *App) AddImageDirectory(path, alias string) (*models.ImageDirectory, error) {
	dir, err := a.imageService.AddImageDirectory(path, alias)
	log.Printf("API AddImageDirectory path=%s alias=%s err=%v", path, alias, err)
	if err == nil {
		// 加回一个曾经删掉的图片目录时，之前被按失踪对账软删的记录要能自动回来
		// （D-S03、D-S04）：扫描发现文件还在就由 restoreStaleImage 复活，标签评分都还在。
		// 放后台跑，别把添加目录的对话框卡住。图片侧只有全量扫描，没有按目录的窄扫描。
		go a.rescanAfterImageDirectoryAdded(path)
	}
	return dir, err
}

// rescanAfterImageDirectoryAdded 加入图片目录后跑一次对账，把之前失效的记录接回来。
func (a *App) rescanAfterImageDirectoryAdded(path string) {
	result, err := a.imageService.SyncImageDirectories()
	if err != nil {
		log.Printf("加入图片目录后的恢复扫描失败 path=%s err=%v", path, err)
		return
	}
	log.Printf("加入图片目录后的恢复扫描完成 path=%s added=%d restored=%d",
		path, result.Added, result.Restored)
}

// UpdateImageDirectory 更新图片扫描目录
func (a *App) UpdateImageDirectory(id uint, path, alias string) error {
	err := a.imageService.UpdateImageDirectory(id, path, alias)
	log.Printf("API UpdateImageDirectory id=%d path=%s alias=%s err=%v", id, path, alias, err)
	return err
}

// DeleteImageDirectory 删除图片扫描目录（软删除）
// DeleteImageDirectory 删除图片扫描目录。
//
// 与视频目录同一口径（D-S04）：删配置行之前先把该目录下的图片按失踪对账处理，
// 记录留在库里、磁盘文件不动，把同一路径加回来时自动恢复。标记失败就不删配置行。
func (a *App) DeleteImageDirectory(id uint) error {
	dirs, err := a.imageService.GetAllImageDirectories()
	if err != nil {
		log.Printf("API DeleteImageDirectory load dirs err=%v", err)
		return err
	}
	var removedPath string
	found := false
	remaining := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if dir.ID == id {
			removedPath = dir.Path
			found = true
			continue
		}
		remaining = append(remaining, dir.Path)
	}
	if !found {
		return fmt.Errorf("图片扫描目录不存在: %d", id)
	}

	marked, err := a.imageService.MarkImagesStaleUnderRemovedRoot(removedPath, remaining)
	if err != nil {
		log.Printf("API DeleteImageDirectory mark stale id=%d path=%s err=%v", id, removedPath, err)
		return err
	}

	err = a.imageService.DeleteImageDirectory(id)
	log.Printf("API DeleteImageDirectory id=%d path=%s marked_stale=%d err=%v", id, removedPath, marked, err)
	return err
}

// SyncImageDirectories 对账扫描全部活跃图片目录
func (a *App) SyncImageDirectories() (*services.ImageScanResult, error) {
	result, err := a.imageService.SyncImageDirectories()
	if err != nil {
		log.Printf("API SyncImageDirectories err=%v", err)
		return nil, err
	}
	log.Printf("API SyncImageDirectories added=%d relocated=%d removed=%d skipped=%d errors=%d",
		result.Added, result.Relocated, result.Removed, result.Skipped, len(result.Errors))
	// 扫到新图后后台增量生成描述；勿阻塞对账返回。
	if result.Added > 0 {
		go a.triggerImageAITaggingAuto("image-scan")
	}
	// EXIF/GPS 补全同样按设置决定要不要自动接着跑。自动路径经空闲门，
	// 与用户显式点的 StartImageEXIFBackfill 走两条不同的入口（D-030）。
	if result.Added > 0 || result.Relocated > 0 {
		if settings, err := a.settingsService.GetSettings(); err == nil && settings.AutoImageEXIFBackfill {
			go func() {
				if err := a.runGatedAutoTask(
					string(services.BackgroundTaskEXIF),
					func(ctx context.Context, hook services.TaskPauseHook) error {
						_, err := a.imageEXIFBackfill.StartImageEXIFBackfillWithPauseHook(ctx, hook)
						return err
					},
				); err != nil {
					log.Printf("扫描后自动补全图片 EXIF 失败 err=%v", err)
				}
			}()
		}
	}
	return result, nil
}

// ===== Image Library Methods =====

// SearchImagePage 照片页游标分页查询。
func (a *App) SearchImagePage(request services.ImagePageRequest) (*services.ImagePage, error) {
	page, err := a.imageLibraryService.SearchImagePage(request)
	if page != nil {
		log.Printf("API SearchImagePage sort=%s result=%d hasNext=%v err=%v", request.Filter.SortMode, len(page.Images), page.NextCursor != nil, err)
	} else {
		log.Printf("API SearchImagePage sort=%s result=nil err=%v", request.Filter.SortMode, err)
	}
	return page, err
}

// ListImageTimelineBuckets 返回照片时间线分组的年月计数摘要（供分组头显示总张数）。
func (a *App) ListImageTimelineBuckets(filter services.ImageFilter) ([]services.ImageTimelineBucket, error) {
	buckets, err := a.imageLibraryService.ListImageTimelineBuckets(filter)
	log.Printf("API ListImageTimelineBuckets buckets=%d err=%v", len(buckets), err)
	return buckets, err
}

// ListImageFolderGroups 返回图片库按直属目录分组的图集摘要。
func (a *App) ListImageFolderGroups(filter services.ImageFilter) ([]services.ImageFolderGroup, error) {
	groups, err := a.imageLibraryService.ListImageFolderGroups(filter)
	log.Printf("API ListImageFolderGroups groups=%d err=%v", len(groups), err)
	return groups, err
}

// GetImageDetail 返回图片详情（含标签与 AI 描述）。
func (a *App) GetImageDetail(imageID uint) (*services.ImageDetail, error) {
	detail, err := a.imageLibraryService.GetImageDetail(imageID)
	log.Printf("API GetImageDetail image_id=%d err=%v", imageID, err)
	return detail, err
}

// SetImageFavorite 更新照片收藏状态。
func (a *App) SetImageFavorite(imageID uint, favorite bool) (*models.Image, error) {
	image, err := a.imageLibraryService.SetImageFavorite(imageID, favorite)
	log.Printf("API SetImageFavorite image_id=%d favorite=%v err=%v", imageID, favorite, err)
	return image, err
}

// SetImageRating 更新照片个人评分（0–10 半分制，nil 清空）。
func (a *App) SetImageRating(imageID uint, rating *float64) (*models.Image, error) {
	image, err := a.imageLibraryService.SetImageRating(imageID, rating)
	log.Printf("API SetImageRating image_id=%d rating=%v err=%v", imageID, rating, err)
	return image, err
}

// AddTagToImage 为图片添加标签
func (a *App) AddTagToImage(imageID uint, tagID uint) error {
	err := a.imageLibraryService.AddTagToImage(imageID, tagID)
	log.Printf("API AddTagToImage imageID=%d tagID=%d err=%v", imageID, tagID, err)
	return err
}

// RemoveTagFromImage 移除图片标签
func (a *App) RemoveTagFromImage(imageID uint, tagID uint) error {
	err := a.imageLibraryService.RemoveTagFromImage(imageID, tagID)
	log.Printf("API RemoveTagFromImage imageID=%d tagID=%d err=%v", imageID, tagID, err)
	return err
}

// BatchAddTagToImages 批量为图片添加标签
func (a *App) BatchAddTagToImages(imageIDs []uint, tagID uint) *services.BatchImageOperationResult {
	result := a.imageLibraryService.BatchAddTagToImages(imageIDs, tagID)
	log.Printf("API BatchAddTagToImages requested=%d succeeded=%d failed=%d tagID=%d", result.Requested, result.Succeeded, result.Failed, tagID)
	return result
}

// BatchRemoveTagFromImages 批量移除图片标签
func (a *App) BatchRemoveTagFromImages(imageIDs []uint, tagID uint) *services.BatchImageOperationResult {
	result := a.imageLibraryService.BatchRemoveTagFromImages(imageIDs, tagID)
	log.Printf("API BatchRemoveTagFromImages requested=%d succeeded=%d failed=%d tagID=%d", result.Requested, result.Succeeded, result.Failed, tagID)
	return result
}

// ===== Image Trash Methods =====

// DeleteImage 删除图片（deleteFile=false 仅软删记录，不建回收站条目）。
func (a *App) DeleteImage(id uint, deleteFile bool) error {
	err := a.imageService.DeleteImage(id, deleteFile)
	log.Printf("API DeleteImage id=%d deleteFile=%v err=%v", id, deleteFile, err)
	if err == nil && a.imageCleanupService != nil {
		a.imageCleanupService.InvalidateAnalysis()
	}
	return err
}

// BatchDeleteImages 批量删除图片
func (a *App) BatchDeleteImages(imageIDs []uint, deleteFile bool) *services.BatchImageOperationResult {
	result := a.imageService.BatchDeleteImages(imageIDs, deleteFile)
	log.Printf("API BatchDeleteImages requested=%d succeeded=%d failed=%d deleteFile=%v", result.Requested, result.Succeeded, result.Failed, deleteFile)
	if result.Succeeded > 0 && a.imageCleanupService != nil {
		a.imageCleanupService.InvalidateAnalysis()
	}
	return result
}

// BatchDeleteImagesInDirectory 删除某个文件夹下的全部图片（移入回收站，可恢复）。
func (a *App) BatchDeleteImagesInDirectory(directory string, deleteFile bool) (*services.BatchImageOperationResult, error) {
	result, err := a.imageService.BatchDeleteImagesInDirectory(directory, deleteFile)
	if err != nil {
		log.Printf("API BatchDeleteImagesInDirectory directory=%s err=%v", directory, err)
		return nil, err
	}
	log.Printf("API BatchDeleteImagesInDirectory directory=%s requested=%d succeeded=%d failed=%d deleteFile=%v",
		directory, result.Requested, result.Succeeded, result.Failed, deleteFile)
	if result.Succeeded > 0 && a.imageCleanupService != nil {
		a.imageCleanupService.InvalidateAnalysis()
	}
	return result, nil
}

// OpenImageDirectory 在系统文件管理器中打开图片目录。
func (a *App) OpenImageDirectory(directory string) error {
	err := a.imageService.OpenImageDirectory(directory)
	log.Printf("API OpenImageDirectory directory=%s err=%v", directory, err)
	return err
}

// RevealImage 在系统文件管理器中定位到指定图片文件。
func (a *App) RevealImage(imageID uint) error {
	err := a.imageService.RevealImage(imageID)
	log.Printf("API RevealImage image_id=%d err=%v", imageID, err)
	return err
}

// ListImageTrashEntries 返回当前可恢复的图片删除记录。
func (a *App) ListImageTrashEntries() ([]models.ImageTrashEntry, error) {
	entries, err := a.imageService.ListImageTrashEntries()
	log.Printf("API ListImageTrashEntries result=%d err=%v", len(entries), err)
	return entries, err
}

// RestoreImageTrashEntry 将一张图片恢复到删除前的路径。
func (a *App) RestoreImageTrashEntry(entryID uint) (*models.Image, error) {
	image, err := a.imageService.RestoreImageTrashEntry(entryID)
	log.Printf("API RestoreImageTrashEntry entryID=%d err=%v", entryID, err)
	if err == nil && image != nil && a.imageCleanupService != nil {
		a.imageCleanupService.InvalidateAnalysis()
	}
	return image, err
}

// ===== Image Semantic Methods =====

// StartImageSemanticIndex 启动图片语义索引构建任务
func (a *App) StartImageSemanticIndex() (services.ImageSemanticIndexStatus, error) {
	svc := a.imageSemanticIndexService()
	if svc == nil {
		return services.ImageSemanticIndexStatus{}, services.ErrImageSemanticIndexUnavailable
	}
	// 全局互斥（D-010）：视频语义索引运行中时拒绝图片任务。
	if video := a.semanticIndexService(); video != nil && video.Status().Running {
		return services.ImageSemanticIndexStatus{}, fmt.Errorf("已有语义索引任务运行中（视频），请先取消后再启动图片索引")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	status, err := svc.Start(ctx)
	log.Printf("API StartImageSemanticIndex err=%v", err)
	return status, err
}

// GetImageSemanticIndexStatus 返回图片语义索引任务状态
func (a *App) GetImageSemanticIndexStatus() services.ImageSemanticIndexStatus {
	svc := a.imageSemanticIndexService()
	if svc == nil {
		return services.ImageSemanticIndexStatus{Available: false, Unavailable: "数据库未初始化"}
	}
	return svc.Status()
}

// CancelImageSemanticIndex 取消图片语义索引任务
func (a *App) CancelImageSemanticIndex() error {
	svc := a.imageSemanticIndexService()
	if svc == nil {
		return services.ErrImageSemanticIndexUnavailable
	}
	err := svc.Cancel()
	log.Printf("API CancelImageSemanticIndex err=%v", err)
	return err
}

// SearchImagesSemantic 照片页内语义检索
func (a *App) SearchImagesSemantic(request services.ImageSemanticSearchRequest) (*services.ImageSemanticSearchPage, error) {
	svc := a.imageSemanticIndexService()
	if svc == nil {
		return nil, services.ErrImageSemanticIndexUnavailable
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	page, err := svc.SearchImagesSemantic(ctx, request)
	log.Printf("API SearchImagesSemantic offset=%d limit=%d err=%v", request.Offset, request.Limit, err)
	return page, err
}

// ===== Image Cleanup Methods =====

// StartImageCleanupAnalysis 启动图片清理候选分析（异步，进度走 image-cleanup-progress 事件）。
func (a *App) StartImageCleanupAnalysis() (*services.ImageCleanupStatus, error) {
	status, err := a.imageCleanupService.StartImageCleanupAnalysis()
	log.Printf("API StartImageCleanupAnalysis err=%v", err)
	return status, err
}

// GetImageCleanupStatus 返回图片清理分析的当前状态与结果快照。
func (a *App) GetImageCleanupStatus() *services.ImageCleanupStatus {
	return a.imageCleanupService.GetImageCleanupStatus()
}

// DismissImageNearDuplicateGroup 忽略一组近似重复图片，后续分析不再报告。
func (a *App) DismissImageNearDuplicateGroup(imageIDs []uint) error {
	err := services.DismissImageNearDuplicateGroup(imageIDs)
	log.Printf("API DismissImageNearDuplicateGroup images=%d err=%v", len(imageIDs), err)
	if err == nil && a.imageCleanupService != nil {
		a.imageCleanupService.InvalidateAnalysis()
	}
	return err
}

// ===== Image AI Tagging Methods =====

// StartImageAITagging 启动图片 AI 打标批量任务
func (a *App) StartImageAITagging() (services.ImageAITaggingStatus, error) {
	svc := a.imageAITaggingService()
	if svc == nil {
		return services.ImageAITaggingStatus{}, fmt.Errorf("数据库未初始化")
	}
	// StartImageAITagging 会在服务锁内摘掉当前这一轮的项间检查点（D-030）。
	status, err := svc.StartImageAITagging(a.ctx)
	log.Printf("API StartImageAITagging err=%v", err)
	return status, err
}

// GetImageAITaggingStatus 返回图片 AI 打标任务状态
func (a *App) GetImageAITaggingStatus() services.ImageAITaggingStatus {
	svc := a.imageAITaggingService()
	if svc == nil {
		return services.ImageAITaggingStatus{}
	}
	return svc.GetImageAITaggingStatus()
}

// CancelImageAITagging 取消图片 AI 打标批量任务
func (a *App) CancelImageAITagging() error {
	svc := a.imageAITaggingService()
	if svc == nil {
		return fmt.Errorf("数据库未初始化")
	}
	err := svc.CancelImageAITagging()
	log.Printf("API CancelImageAITagging err=%v", err)
	return err
}

// ListImageAITagCandidates 列出图片 AI 标签候选。imageID 为 0 表示不限图片
func (a *App) ListImageAITagCandidates(imageID uint, confidence string, status string) ([]services.ImageAITaggingReviewItem, error) {
	svc := a.imageAITaggingService()
	if svc == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	return svc.ListImageAITagCandidates(imageID, confidence, status)
}

// ApproveImageAITagCandidate 接受一个图片标签候选，写入官方标签
func (a *App) ApproveImageAITagCandidate(candidateID uint) (*services.ImageAITaggingReviewItem, error) {
	svc := a.imageAITaggingService()
	if svc == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	item, err := svc.ApproveImageAITagCandidate(candidateID)
	log.Printf("API ApproveImageAITagCandidate candidate=%d err=%v", candidateID, err)
	return item, err
}

// RejectImageAITagCandidate 拒绝一个图片标签候选
func (a *App) RejectImageAITagCandidate(candidateID uint) error {
	svc := a.imageAITaggingService()
	if svc == nil {
		return fmt.Errorf("数据库未初始化")
	}
	err := svc.RejectImageAITagCandidate(candidateID)
	log.Printf("API RejectImageAITagCandidate candidate=%d err=%v", candidateID, err)
	return err
}

// RejectImageAITagCandidatesByImage 拒绝某张图片的全部待审候选
func (a *App) RejectImageAITagCandidatesByImage(imageID uint) (int64, error) {
	svc := a.imageAITaggingService()
	if svc == nil {
		return 0, fmt.Errorf("数据库未初始化")
	}
	rejected, err := svc.RejectPendingImageAITagCandidatesByImage(imageID)
	log.Printf("API RejectImageAITagCandidatesByImage image=%d rejected=%d err=%v", imageID, rejected, err)
	return rejected, err
}

// GetImageAITaggingSummary 返回图片标签候选的待审汇总
func (a *App) GetImageAITaggingSummary() (*services.ImageAITaggingSummary, error) {
	svc := a.imageAITaggingService()
	if svc == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	return svc.GetImageAITaggingSummary()
}

// RetagImage 对单张图片同步重跑 AI 打标，返回该图当前的待审候选
func (a *App) RetagImage(imageID uint) ([]models.ImageAITagCandidate, error) {
	svc := a.imageAITaggingService()
	if svc == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	candidates, err := svc.RetagImage(imageID)
	log.Printf("API RetagImage image=%d err=%v", imageID, err)
	return candidates, err
}

// ===== Image EXIF Backfill Methods =====

// StartImageEXIFBackfill 启动历史图片的 EXIF 补全任务
func (a *App) StartImageEXIFBackfill() (services.ImageEXIFBackfillStatus, error) {
	if a.imageEXIFBackfill == nil {
		return services.ImageEXIFBackfillStatus{}, fmt.Errorf("数据库未初始化")
	}
	// StartImageEXIFBackfill 会在服务锁内摘掉当前这一轮的项间检查点（D-030）。
	status, err := a.imageEXIFBackfill.StartImageEXIFBackfill(a.ctx)
	log.Printf("API StartImageEXIFBackfill err=%v", err)
	return status, err
}

// GetImageEXIFBackfillStatus 返回 EXIF 补全任务状态
func (a *App) GetImageEXIFBackfillStatus() services.ImageEXIFBackfillStatus {
	if a.imageEXIFBackfill == nil {
		return services.ImageEXIFBackfillStatus{}
	}
	return a.imageEXIFBackfill.GetImageEXIFBackfillStatus()
}

// CancelImageEXIFBackfill 取消 EXIF 补全任务
func (a *App) CancelImageEXIFBackfill() error {
	if a.imageEXIFBackfill == nil {
		return fmt.Errorf("数据库未初始化")
	}
	err := a.imageEXIFBackfill.CancelImageEXIFBackfill()
	log.Printf("API CancelImageEXIFBackfill err=%v", err)
	return err
}

// ===== Image Insights Methods =====

// GetImageInsights 返回图片维度洞察统计
func (a *App) GetImageInsights() (*services.ImageStats, error) {
	stats, err := a.imageStatsService.GetImageInsights()
	log.Printf("API GetImageInsights err=%v", err)
	return stats, err
}
