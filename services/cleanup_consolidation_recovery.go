package services

import (
	"context"
	"errors"
	"fmt"
	"video-master/database"
	"video-master/models"
)

// RecoverConsolidations 只对账本机范围的未完成任务，不重新执行计划。另一个活跃
// 实例仍持锁时保留 running 和活动槽；不同 OwnerScope 也不能接管。
func (s *CleanupService) RecoverConsolidations(ctx context.Context) error {
	s.consolidationMu.Lock()
	if s.consolidationBlocked {
		s.consolidationMu.Unlock()
		return fmt.Errorf("集中整理已停止接受任务")
	}
	if s.consolidationDataDir == "" {
		s.consolidationMu.Unlock()
		return fmt.Errorf("集中整理运行配置未初始化")
	}
	if s.consolidationDone != nil {
		s.consolidationMu.Unlock()
		return nil
	}
	if err := validateConsolidationDirectory(s.consolidationDirectory); err != nil {
		s.consolidationMu.Unlock()
		return err
	}
	s.mu.Lock()
	video, registry := s.consolidationVideo, s.registry
	s.mu.Unlock()
	if video == nil {
		s.consolidationMu.Unlock()
		return fmt.Errorf("集中整理迁移服务未初始化")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.consolidationDone, s.consolidationCancel = done, cancel
	s.consolidationMu.Unlock()
	registry.Begin(BackgroundTaskCleanupConsolidation)
	defer func() {
		cancel()
		registry.End(BackgroundTaskCleanupConsolidation)
		s.consolidationMu.Lock()
		s.consolidationCancel = nil
		s.consolidationDone = nil
		close(done)
		s.consolidationMu.Unlock()
	}()
	var rows []models.CleanupConsolidationTask
	if err := database.DB.WithContext(runCtx).Where("status = ?", "running").Order("id").Find(&rows).Error; err != nil {
		return err
	}
	var failures []error
	for _, row := range rows {
		if err := runCtx.Err(); err != nil {
			return errors.Join(errors.Join(failures...), err)
		}
		if row.OwnerScope != s.consolidationOwner {
			failures = append(failures, fmt.Errorf("任务 %d 属于其他本机/数据目录范围，保留活动槽与全部文件", row.ID))
			continue
		}
		release, err := acquireConsolidationLock(s.consolidationDataDir, row.PreviewID)
		if errors.Is(err, ErrConsolidationBusy) {
			continue
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if err = validateConsolidationDirectory(s.consolidationDirectory); err == nil {
			err = s.recoverConsolidation(runCtx, video, row.ID)
		}
		release()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *CleanupService) recoverConsolidation(ctx context.Context, video *VideoService, id uint) error {
	var row models.CleanupConsolidationTask
	if err := database.DB.WithContext(ctx).First(&row, id).Error; err != nil {
		return err
	}
	if row.Status != "running" {
		return nil
	}
	if row.OwnerScope != s.consolidationOwner {
		return ErrConsolidationConflict
	}
	plan, journal, err := decodeConsolidation(ctx, row)
	if err != nil {
		return err
	}
	execution := consolidationExecution{task: row, plan: plan, journal: journal, onChange: s.consolidationOnChange, hooks: s.consolidationHooks}
	reconcileErr := video.reconcileConsolidation(ctx, &execution)
	if errors.Is(reconcileErr, ErrConsolidationConflict) || errors.Is(reconcileErr, errConsolidationPersistence) {
		return reconcileErr
	}
	// 取消只表示本次对账停止；保留 running/活动槽，让下次恢复继续核验。
	if err := ctx.Err(); err != nil {
		return err
	}
	err = execution.finish("interrupted", errors.Join(fmt.Errorf("上次集中整理已中断；已对账，未自动继续或清理视频"), reconcileErr))
	if execution.task.Completed > 0 {
		s.InvalidateAnalysis()
	}
	return errors.Join(reconcileErr, err)
}
