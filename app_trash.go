package main

import (
	"log"
	"strings"
	"video-master/services"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// 回收站中心的 App 层入口（P-010，详细设计 §2.1 / §2.4 / §9.4）。
//
// 视频与图片共用一套统一接口，用 kind（video / image）区分。旧的单列表 / 单条恢复绑定已由 P-040 删除。

// trashCenter 每次调用现建：TrashService 除批量取消登记（包级）外没有状态。
func (a *App) trashCenter() *services.TrashService {
	return services.NewTrashCenter(a.videoService, a.imageService)
}

// batchDeleteOptions 组装批量删除的可选参数。requestID 非空时每处理一项发一次
// batch-delete-progress {request_id, done, total}，并可被 CancelBatchDelete 取消。
func (a *App) batchDeleteOptions(requestID string) services.BatchDeleteOptions {
	requestID = strings.TrimSpace(requestID)
	options := services.BatchDeleteOptions{RequestID: requestID}
	if requestID != "" {
		options.Progress = func(done, total int) {
			if a.ctx == nil {
				return
			}
			runtime.EventsEmit(a.ctx, "batch-delete-progress", struct {
				RequestID string `json:"request_id"`
				Done      int    `json:"done"`
				Total     int    `json:"total"`
			}{requestID, done, total})
		}
	}
	return options
}

// CancelBatchDelete 取消进行中的批量删除：在两项之间生效，已完成的项保留，未处理的项计为 cancelled。
func (a *App) CancelBatchDelete(requestID string) bool {
	cancelled := services.CancelBatchDelete(requestID)
	log.Printf("API CancelBatchDelete requestID=%s found=%v", requestID, cancelled)
	return cancelled
}

// ListTrashEntries 分页列出回收站条目（游标分页，当页对账 file_gone）。
// 用户在访达里「放回原处」的条目以 put_back=true、actions=[restore] 返回，列表本身不恢复；
// 需要调用 RestoreTrashEntries 才会还原记录（复审 Minor 1）。
func (a *App) ListTrashEntries(filter services.TrashFilter) (*services.TrashPage, error) {
	page, err := a.trashCenter().ListTrashEntries(filter)
	if err != nil {
		log.Printf("API ListTrashEntries kind=%s err=%v", filter.Kind, err)
		return nil, err
	}
	log.Printf("API ListTrashEntries kind=%s result=%d hasMore=%v", filter.Kind, len(page.Items), page.HasMore)
	return page, nil
}

// GetTrashUsage 返回回收站用量（视频、图片、迁移残留）。
func (a *App) GetTrashUsage() (*services.TrashUsage, error) {
	usage, err := a.trashCenter().GetTrashUsage()
	log.Printf("API GetTrashUsage err=%v", err)
	return usage, err
}

// RestoreTrashEntries 按条目 ID 逐项恢复。
func (a *App) RestoreTrashEntries(kind string, ids []uint) (*services.BatchResult, error) {
	result, err := a.trashCenter().RestoreTrashEntries(kind, ids)
	a.afterTrashChange(kind, result)
	logTrashResult("RestoreTrashEntries", kind, result, err)
	return result, err
}

// RestoreTrashBatch 恢复同一次删除操作留下的全部条目（撤销本次删除）。
func (a *App) RestoreTrashBatch(kind, batchID string) (*services.BatchResult, error) {
	result, err := a.trashCenter().RestoreTrashBatch(kind, batchID)
	a.afterTrashChange(kind, result)
	logTrashResult("RestoreTrashBatch", kind, result, err)
	return result, err
}

// PurgeTrashEntries 清除废纸篓里的文件并硬删记录。
func (a *App) PurgeTrashEntries(kind string, ids []uint) (*services.BatchResult, error) {
	result, err := a.trashCenter().PurgeTrashEntries(kind, ids)
	logTrashResult("PurgeTrashEntries", kind, result, err)
	return result, err
}

// RemoveGoneTrashEntries 执行列表项的 remove_record 动作：移除「废纸篓文件已被清除」的条目，以及文件已放回
// 原处、却已由原位置上另一条活跃记录收录的重复条目（claimed_by_active，修复 G I-1）。只删记录，不动文件。
func (a *App) RemoveGoneTrashEntries(kind string, ids []uint) (*services.BatchResult, error) {
	result, err := a.trashCenter().RemoveGoneTrashEntries(kind, ids)
	logTrashResult("RemoveGoneTrashEntries", kind, result, err)
	return result, err
}

// ForceRemoveTrashRecords 是「仍然移除记录（不动文件）」（修复 G m3）：清除或移除记录因磁盘离线、没有权限等
// 被拒绝时的出口。只硬删记录与条目，不做任何文件操作，也不要求磁盘在线。confirmText 必须原样等于
// 「移除记录」，否则整批拒绝。
func (a *App) ForceRemoveTrashRecords(kind string, ids []uint, confirmText string) (*services.BatchResult, error) {
	result, err := a.trashCenter().ForceRemoveTrashRecords(kind, ids, confirmText)
	logTrashResult("ForceRemoveTrashRecords", kind, result, err)
	return result, err
}

// TrashStagedSources 把迁移残留的暂存源文件移入系统废纸篓。
func (a *App) TrashStagedSources(ids []uint) *services.BatchResult {
	result := a.trashCenter().TrashStagedSources(ids)
	log.Printf("API TrashStagedSources requested=%d succeeded=%d failed=%d", result.Requested, result.Succeeded, result.Failed)
	return result
}

// afterTrashChange 恢复成功后让清理分析结果失效（与旧的单项恢复方法一致）。
func (a *App) afterTrashChange(kind string, result *services.BatchResult) {
	if result == nil || result.Succeeded == 0 {
		return
	}
	if kind == "video" && a.cleanupService != nil {
		a.cleanupService.InvalidateAnalysis()
	}
	if kind == "image" && a.imageCleanupService != nil {
		a.imageCleanupService.InvalidateAnalysis()
	}
}

func logTrashResult(method, kind string, result *services.BatchResult, err error) {
	if result == nil {
		log.Printf("API %s kind=%s err=%v", method, kind, err)
		return
	}
	log.Printf("API %s kind=%s requested=%d succeeded=%d failed=%d err=%v", method, kind, result.Requested, result.Succeeded, result.Failed, err)
}
