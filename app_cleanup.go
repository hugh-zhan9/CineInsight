package main

import (
	"context"
	"log"
	"time"
	"video-master/services"
)

// GetCleanupCandidates 获取清理候选（轻量规则）
func (a *App) GetCleanupCandidates(minDurationSeconds int, minWidth int, minHeight int) (*services.CleanupAnalysis, error) {
	criteria := services.CleanupCriteria{
		MinDuration: time.Duration(minDurationSeconds) * time.Second,
		MinWidth:    minWidth,
		MinHeight:   minHeight,
	}
	startedAt := time.Now()
	log.Printf("API GetCleanupCandidates begin duration=%d width=%d height=%d", minDurationSeconds, minWidth, minHeight)
	analysis, err := a.cleanupService.AnalyzeCleanupCandidates(criteria)
	if err != nil {
		log.Printf("API GetCleanupCandidates duration=%d width=%d height=%d elapsed=%s err=%v",
			minDurationSeconds, minWidth, minHeight, time.Since(startedAt).Round(time.Millisecond), err)
		return nil, err
	}

	log.Printf("API GetCleanupCandidates duration=%d width=%d height=%d elapsed=%s duplicate_groups=%d low_duration=%d low_resolution=%d",
		minDurationSeconds, minWidth, minHeight,
		time.Since(startedAt).Round(time.Millisecond),
		len(analysis.DuplicateGroups), len(analysis.LowDuration), len(analysis.LowResolution),
	)
	return analysis, nil
}

func (a *App) StartCleanupAnalysis(minDurationSeconds int, minWidth int, minHeight int) (*services.CleanupStatus, error) {
	criteria := services.CleanupCriteria{
		MinDuration: time.Duration(minDurationSeconds) * time.Second,
		MinWidth:    minWidth,
		MinHeight:   minHeight,
	}
	status, err := a.cleanupService.StartAnalysis(criteria)
	log.Printf("API StartCleanupAnalysis duration=%d width=%d height=%d running=%v completed=%v err=%v",
		minDurationSeconds, minWidth, minHeight, status != nil && status.Running, status != nil && status.Completed, err)
	return status, err
}

func (a *App) GetCleanupStatus() *services.CleanupStatus {
	status := a.cleanupService.Status()
	log.Printf("API GetCleanupStatus running=%v completed=%v hasAnalysis=%v err=%q",
		status.Running, status.Completed, status.Analysis != nil, status.Error)
	return status
}

// DismissNearDuplicateGroup 持久忽略一组近似重复视频，后续分析不再报出。
func (a *App) DismissNearDuplicateGroup(videoIDs []uint) error {
	err := services.DismissNearDuplicateGroup(videoIDs)
	log.Printf("API DismissNearDuplicateGroup videos=%d err=%v", len(videoIDs), err)
	return err
}

// ===== 帧哈希序列与截取片段识别（D-026、D-028）=====

// StartFrameHashBackfill 显式启动帧哈希回填。
// Start 会在服务锁内摘掉当前这一轮的项间检查点，用户点的任务永不被空闲门挡住（D-030）。
func (a *App) StartFrameHashBackfill() (services.FrameHashStatus, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	status, err := a.frameHash.Start(ctx)
	log.Printf("API StartFrameHashBackfill running=%v total=%d err=%v", status.Running, status.Total, err)
	return status, err
}

func (a *App) GetFrameHashBackfillStatus() services.FrameHashStatus {
	return a.frameHash.Status()
}

func (a *App) CancelFrameHashBackfill() error {
	err := a.frameHash.Cancel()
	log.Printf("API CancelFrameHashBackfill err=%v", err)
	return err
}

// DismissClipCandidate 持久忽略一对"完整片 + 截取片段"，双方文件都没变时不再报出。
func (a *App) DismissClipCandidate(fullID uint, clipID uint) error {
	err := services.DismissClipCandidate(fullID, clipID)
	log.Printf("API DismissClipCandidate full=%d clip=%d err=%v", fullID, clipID, err)
	return err
}
