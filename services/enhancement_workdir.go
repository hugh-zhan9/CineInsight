package services

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 超分工作目录的收尾（MEDIA-03 复审 I-3）：放弃保留的进度，以及启动时清扫孤儿工作目录。
//
// 工作目录建在源视频旁边（enhancementWorkdir：<视频所在目录>/.cineinsight-enhance-<任务 id>），
// 空间不足与用户取消会把它连同检查点留下来等重试（D-PC24）。留下来的目录有三种方式变成孤儿：
// 任务行被删（视频永久删除时任务随之删除）、任务之后以别的结束码收尾、视频换了目录（续跑只会去
// 新位置找检查点）。它们都是动辄几 GB 的分段，没人清就一直占着用户的盘。

// enhancementWorkdirPrefix 是工作目录名的前缀，后面紧跟十进制任务 id（见 enhancementWorkdir）。
const enhancementWorkdirPrefix = ".cineinsight-enhance-"

// ErrEnhancementNoRetainedProgress 表示任务没有可放弃的保留进度：不是 cancelled / disk_insufficient
// 收尾的任务，或正在排队 / 运行。
var ErrEnhancementNoRetainedProgress = errors.New("该任务没有保留的进度")

// DiscardTaskProgress 放弃一个任务保留的进度（I-3）：结束码从 cancelled / disk_insufficient 改为
// checkpoint_discarded、删掉工作目录，之后重试从头开始。
//
// 先条件更新、更新成功才删目录（与 discardRetainedCheckpoints 同一顺序，G-2）：条件落空说明任务
// 恰好被重试排回了队列，那时它的检查点正要被用，不能动。已经放弃过的任务再放弃一次是幂等的。
func (s *EnhancementService) DiscardTaskProgress(taskID uint) (*EnhancementTaskView, error) {
	var task models.VideoEnhancementTask
	if err := database.DB.Preload("Video", func(db *gorm.DB) *gorm.DB { return db.Unscoped() }).
		First(&task, taskID).Error; err != nil {
		return nil, err
	}
	if task.ErrorCode == enhancementCodeCheckpointDiscarded &&
		(task.Status == models.EnhancementStatusFailed || task.Status == models.EnhancementStatusCancelled) {
		view := s.taskView(task, task.Video.Name)
		return &view, nil
	}
	if !enhancementCheckpointKept(task.ErrorCode) ||
		(task.Status != models.EnhancementStatusFailed && task.Status != models.EnhancementStatusCancelled) {
		return nil, ErrEnhancementNoRetainedProgress
	}
	reason := "已取消"
	if task.ErrorCode == enhancementCodeDiskInsufficient {
		reason = "空间不足"
	}
	summary := reason + "；保留的进度已放弃，重试将从头开始"
	result := database.DB.Model(&models.VideoEnhancementTask{}).
		Where("id = ? AND status = ? AND error_code = ?", task.ID, task.Status, task.ErrorCode).
		Updates(map[string]any{
			"error_code":    enhancementCodeCheckpointDiscarded,
			"error_summary": summary,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, errors.New("任务状态已变化，请刷新后再试")
	}
	s.cleanupTaskWorkdir(task)
	logEnhancement("task=%d retained checkpoint discarded by user", task.ID)
	s.emitTaskByID(task.ID)
	task.ErrorCode = enhancementCodeCheckpointDiscarded
	task.ErrorSummary = summary
	view := s.taskView(task, task.Video.Name)
	return &view, nil
}

// EnhancementWorkdirSweepResult 是一次孤儿工作目录清扫的计数。
type EnhancementWorkdirSweepResult struct {
	// Found 是找到的 .cineinsight-enhance-<id> 目录数。
	Found int `json:"found"`
	// Removed 是删掉的孤儿目录数；Kept 是仍有任务要用、原样留下的目录数。
	Removed int `json:"removed"`
	Kept    int `json:"kept"`
	// Errors 是读不了的目录与删除失败的次数（都跳过，不中断清扫）。
	Errors int `json:"errors"`
}

// SweepOrphanWorkdirs 走遍所有扫描根，删掉孤儿工作目录（I-3），供启动时在后台调用一次。
//
// 一个 .cineinsight-enhance-<id> 目录留下的条件（enhancementWorkdirWanted）：对应任务仍在排队或运行，
// 或任务以「保留检查点」收尾（cancelled / disk_insufficient，且没有被放弃），并且这个目录就是任务
// 当前的工作目录位置。其余一律删除：任务行不存在、任务已完成、以其他结束码（含
// checkpoint_discarded）收尾、视频已换目录。读库失败或位置判断不了时留下——宁可多留一个目录，
// 也不在拿不准时删掉用户花了几个小时算出来的进度。
//
// 名字不是「前缀 + 规范十进制 id」（见 enhancementWorkdirTaskID，带前导零的也不算）的同前缀目录
// 不是本应用建的，不碰、也不往里走。
// 读不了的目录跳过（计入 Errors），离线的扫描根直接跳过。
func (s *EnhancementService) SweepOrphanWorkdirs(ctx context.Context) (EnhancementWorkdirSweepResult, error) {
	result := EnhancementWorkdirSweepResult{}
	var dirs []models.ScanDirectory
	if err := database.DB.Find(&dirs).Error; err != nil {
		return result, fmt.Errorf("读取扫描目录失败: %w", err)
	}
	for _, root := range cleanScanRoots(dirs) {
		walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					result.Errors++
				}
				if entry != nil && entry.IsDir() && path != root {
					return fs.SkipDir
				}
				return nil
			}
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), enhancementWorkdirPrefix) {
				return nil
			}
			id, ok := enhancementWorkdirTaskID(entry.Name())
			if !ok {
				return fs.SkipDir
			}
			result.Found++
			if s.enhancementWorkdirWanted(id, path) {
				result.Kept++
				return fs.SkipDir
			}
			if removeErr := os.RemoveAll(path); removeErr != nil {
				result.Errors++
				logEnhancement("orphan workdir task=%d remove failed: %v", id, sanitizeEnhancementError(removeErr.Error()))
				return fs.SkipDir
			}
			result.Removed++
			logEnhancement("orphan workdir task=%d removed", id)
			return fs.SkipDir
		})
		if walkErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			result.Errors++
		}
	}
	logEnhancement("orphan workdir sweep found=%d removed=%d kept=%d errors=%d", result.Found, result.Removed, result.Kept, result.Errors)
	return result, nil
}

// enhancementWorkdirTaskID 从目录名解析任务 id：只有「前缀 + 规范十进制 id」才是本应用建的工作目录
// （B-m-2）。后缀必须与 strconv.FormatUint(id, 10) 完全一致——前导零（-007）、符号、0 都不算，
// 否则 .cineinsight-enhance-007 这种别人的目录会被当成任务 7 的孤儿删掉。
func enhancementWorkdirTaskID(name string) (uint, bool) {
	suffix, found := strings.CutPrefix(name, enhancementWorkdirPrefix)
	if !found {
		return 0, false
	}
	id, err := strconv.ParseUint(suffix, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != suffix || uint64(uint(id)) != id {
		return 0, false
	}
	return uint(id), true
}

// enhancementWorkdirWanted 判断在 path 找到的任务 taskID 的工作目录还要不要（见 SweepOrphanWorkdirs）。
func (s *EnhancementService) enhancementWorkdirWanted(taskID uint, path string) bool {
	s.mu.Lock()
	current := s.currentTaskID == taskID
	s.mu.Unlock()
	if current {
		return true
	}
	var task models.VideoEnhancementTask
	err := database.DB.Preload("Video", func(db *gorm.DB) *gorm.DB { return db.Unscoped() }).First(&task, taskID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false
	}
	if err != nil {
		return true
	}
	switch task.Status {
	case models.EnhancementStatusQueued, models.EnhancementStatusRunning, models.EnhancementStatusCancelRequested:
		return true
	case models.EnhancementStatusFailed, models.EnhancementStatusCancelled:
		if !enhancementCheckpointKept(task.ErrorCode) {
			return false
		}
	default:
		return false
	}
	// 保留检查点的任务：只有它当前的工作目录位置才算数。比较目录身份而不是字符串，
	// 大小写不敏感的卷、符号链接挂载的扫描根都不会因写法不同被误判；判断不了就留下。
	if task.Video.Path == "" {
		return true
	}
	found, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return true
	}
	expected, err := os.Stat(filepath.Dir(task.Video.Path))
	if err != nil {
		return true
	}
	return os.SameFile(found, expected)
}
