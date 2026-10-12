package services

import (
	"context"
	"log"
	"time"
	"video-master/models"
)

// 下载完自动查重在下载服务这一侧的部分（D-DD-01 / D-DD-03 / D-DD-04）：任务落到「完成」之后排进
// 一个单 goroutine 的查重队列，查完把结论写回任务（内存或表），有匹配时调用 DuplicateFound。
// 查重本身由 deps.CheckDuplicates 注入（App 接上 DownloadDuplicateChecker），下载服务不直接
// 依赖清理与指纹服务（D-DD-05）。查重中、失败都不改变下载状态。
//
// 查重队列有自己的生命周期：Wails 给的启动上下文在退出时不会被取消，退出路径要显式调
// StopDuplicateChecks，否则 Wait 会等完整个队列（2026-10-12 审查 I-5）。

// enqueueDuplicateCheck 把一个已完成的任务排进查重队列；没有注入查重或已停止时什么都不做。
func (s *BrowserDownloadService) enqueueDuplicateCheck(id string) {
	if s == nil || s.deps.CheckDuplicates == nil {
		return
	}
	s.mu.Lock()
	if s.dupStopped {
		s.mu.Unlock()
		return
	}
	if s.dupCtx == nil {
		base := s.baseCtx
		if base == nil {
			base = context.Background()
		}
		s.dupCtx, s.dupCancel = context.WithCancel(base)
	}
	s.dupQueue = append(s.dupQueue, id)
	start := !s.dupRunning
	if start {
		s.dupRunning = true
		s.wg.Add(1)
	}
	s.mu.Unlock()
	if start {
		go s.runDuplicateChecks()
	}
}

// StopDuplicateChecks 取消正在进行的查重并清空队列，之后不再接受新的查重（应用退出时调用）。
func (s *BrowserDownloadService) StopDuplicateChecks() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.dupStopped = true
	s.dupQueue = nil
	cancel := s.dupCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *BrowserDownloadService) runDuplicateChecks() {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		ctx := s.dupCtx
		if len(s.dupQueue) == 0 || ctx.Err() != nil {
			s.dupQueue = nil
			s.dupRunning = false
			s.mu.Unlock()
			return
		}
		id := s.dupQueue[0]
		s.dupQueue = s.dupQueue[1:]
		s.mu.Unlock()
		// 已经被裁出内存的任务照样查：从表里读，片库记录按路径现算（审查 Minor 3）。
		task, ok := s.duplicateTarget(id)
		if !ok {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, downloadDuplicateCheckTimeout)
		result, err := s.deps.CheckDuplicates(checkCtx, task.OutputPath, task.VideoID)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			text := downloadDuplicateErrorText(err)
			log.Printf("browser download: 查重失败 task=%s reason=%s", id, text)
			result = DownloadDuplicateResult{Status: DownloadDuplicateFailed, CheckedAt: s.deps.Now(), Matches: []DownloadDuplicateMatch{}, Error: text, VideoID: task.VideoID}
		}
		updated, saved := s.saveDuplicateResult(id, result)
		if saved && updated.Duplicate != nil && updated.Duplicate.Status == DownloadDuplicateFound && s.deps.DuplicateFound != nil {
			s.deps.DuplicateFound(updated)
		}
	}
}

// duplicateTarget 取要查重的任务：必须已完成；不在内存里的历史任务按路径现算片库记录。
func (s *BrowserDownloadService) duplicateTarget(id string) (BrowserDownloadTask, bool) {
	task, found, inMemory, err := s.findTask(id)
	if err != nil || !found || task.State != browserDownloadStateDone {
		return BrowserDownloadTask{}, false
	}
	if !inMemory {
		task = s.annotateHistoryTask(task)
	}
	return task, true
}

// annotateHistoryTask 给不在内存里的历史任务补上按路径现算的片库记录与入库状态（与历史列表同口径）。
func (s *BrowserDownloadService) annotateHistoryTask(task BrowserDownloadTask) BrowserDownloadTask {
	if db := s.db(); db != nil {
		tasks := []BrowserDownloadTask{task}
		annotateBrowserDownloadHistoryImport(db, tasks)
		task = tasks[0]
	}
	return task
}

// saveDuplicateResult 把查重结论写回任务：内存里有就改内存（随后照常落库），已经被裁出内存的
// 直接改表并刷新历史快照；两种情况都立刻推送一次（不受进度推送的节流影响，审查 I-3）。
// 返回补好片库记录与界面视图的任务快照；任务已不存在时 saved 为 false。
func (s *BrowserDownloadService) saveDuplicateResult(id string, result DownloadDuplicateResult) (BrowserDownloadTask, bool) {
	raw := encodeDownloadDuplicateResult(result)
	task, found, inMemory, err := s.findTask(id)
	if err != nil || !found {
		return BrowserDownloadTask{}, false
	}
	if inMemory {
		var updated bool
		s.update(id, func(current *BrowserDownloadTask) {
			current.duplicateResult = raw
			updated = true
		})
		if !updated {
			// 刚好在这一刻被裁出内存：改表。
			inMemory = false
		}
	}
	if !inMemory {
		db := s.db()
		if db == nil {
			return BrowserDownloadTask{}, false
		}
		if err := db.Model(&models.BrowserDownloadTask{}).Where("task_uid = ?", id).
			Updates(map[string]any{"duplicate_result": raw, "updated_at": time.Now()}).Error; err != nil {
			log.Printf("browser download: 写入查重结果失败 task=%s err=%v", id, err)
			return BrowserDownloadTask{}, false
		}
		s.refreshHistory()
		task = s.annotateHistoryTask(task)
	}
	task.duplicateResult = raw
	views := []BrowserDownloadTask{task}
	resolveDownloadDuplicateViews(s.db(), views)
	task.Duplicate = views[0].Duplicate
	if inMemory {
		s.update(id, func(current *BrowserDownloadTask) { current.Duplicate = task.Duplicate })
	}
	s.notify(true)
	return task, true
}

// DismissDownloadDuplicate 处理查重提示里的「不是同一部」：「其他版本」的匹配写进近似重复忽略
// （与清理中心同一张表，以后两边都不再提示这一对）；「相同文件」只从这条提示里拿掉（「保留两份」），
// 不写全局忽略。
func (s *BrowserDownloadService) DismissDownloadDuplicate(taskUID string, videoID uint) (BrowserDownloadActionResult, error) {
	return s.resolveDuplicateMatch(taskUID, videoID, true)
}

// ClearDownloadDuplicate 只把这一项从提示里拿掉、不写任何忽略：例如已经和它建立了版本组。
func (s *BrowserDownloadService) ClearDownloadDuplicate(taskUID string, videoID uint) (BrowserDownloadActionResult, error) {
	return s.resolveDuplicateMatch(taskUID, videoID, false)
}

func (s *BrowserDownloadService) resolveDuplicateMatch(taskUID string, videoID uint, writeDismissal bool) (BrowserDownloadActionResult, error) {
	if s == nil {
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeServiceUnavailable, Message: "下载服务未启用"}, nil
	}
	task, found, inMemory, err := s.findTask(taskUID)
	if err != nil {
		return BrowserDownloadActionResult{}, err
	}
	if !found {
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeTaskNotFound, Message: "没有这个下载任务"}, nil
	}
	// 历史任务的片库记录不落库、按路径现算（与历史列表同口径），否则已入库的历史下载写不了忽略。
	if !inMemory {
		task = s.annotateHistoryTask(task)
	}
	result, ok := decodeDownloadDuplicateResult(task.duplicateResult)
	if !ok {
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeDuplicateNotFound, Message: "这条下载没有查重结果"}, nil
	}
	// 结论查的已经不是这次下载现在的那一份（同名路径换了文件、重新入库换了记录）：作废，不据此写忽略。
	if !downloadDuplicateStillApplies(result, task) {
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeDuplicateNotFound, Message: "这条提示已经过期，请刷新后再看"}, nil
	}
	kept := make([]DownloadDuplicateMatch, 0, len(result.Matches))
	var removed *DownloadDuplicateMatch
	for index, match := range result.Matches {
		if match.VideoID == videoID && removed == nil {
			removed = &result.Matches[index]
			continue
		}
		kept = append(kept, match)
	}
	if removed == nil {
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeDuplicateNotFound, Message: "这条提示里没有这部视频"}, nil
	}
	if writeDismissal && removed.Kind == DownloadDuplicateVersion && task.VideoID != 0 {
		if s.deps.DismissDuplicatePair == nil {
			return BrowserDownloadActionResult{Code: BrowserDownloadCodeServiceUnavailable, Message: "忽略记录不可用"}, nil
		}
		if err := s.deps.DismissDuplicatePair(task.VideoID, videoID); err != nil {
			return BrowserDownloadActionResult{}, err
		}
	}
	result.Matches = kept
	if len(kept) == 0 && result.Status == DownloadDuplicateFound {
		result.Status = DownloadDuplicateNone
	}
	updated, saved := s.saveDuplicateResult(taskUID, result)
	if !saved {
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeTaskNotFound, Message: "没有这个下载任务"}, nil
	}
	return BrowserDownloadActionResult{Code: BrowserDownloadCodeOK, Task: &updated}, nil
}
