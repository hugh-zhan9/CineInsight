package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"video-master/services"
)

const cleanupConsolidationProgressEvent = "cleanup-consolidation-progress"

// Only lifecycle admission lives here. Persistent task state belongs to CleanupService.
// The wait group includes accepted reads/previews and recovery before its goroutine starts,
// so no late startup work can cross a database maintenance or shutdown boundary.
type cleanupConsolidationLifecycle struct {
	mu           sync.Mutex
	wg           sync.WaitGroup
	ctx          context.Context
	cancel       context.CancelFunc
	configured   bool
	runtimeReady bool
	stopped      bool
	recovering   bool
	initErr      error
	recoveryErr  error
	emit         func(string, any)
}

func (a *App) configureCleanupConsolidation(dataDir string) {
	state := &a.consolidationLifecycle
	state.ctx, state.cancel = context.WithCancel(context.Background())
	a.cleanupService.SetConsolidationVideoService(a.videoService)
	state.initErr = a.cleanupService.ConfigureConsolidation(dataDir, a.emitCleanupConsolidationProgress)
	state.configured = true
	if state.initErr != nil {
		log.Printf("集中整理初始化失败 err=%v", state.initErr)
	}
}

// Called only after startup sets CleanupService's runtime context. Wails may expose
// bindings earlier; Start must not race that one-time context assignment.
func (a *App) enableCleanupConsolidationStart() {
	a.consolidationLifecycle.mu.Lock()
	a.consolidationLifecycle.runtimeReady = true
	a.consolidationLifecycle.mu.Unlock()
}

func (a *App) emitCleanupConsolidationProgress(summary services.CleanupConsolidationSummary) {
	state := &a.consolidationLifecycle
	state.mu.Lock()
	emit := state.emit
	state.mu.Unlock()
	if emit != nil {
		emit(cleanupConsolidationProgressEvent, summary)
	}
}

func (a *App) cleanupConsolidationWarning() string {
	state := &a.consolidationLifecycle
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.initErr != nil {
		return boundTaskFailures([]string{"集中整理不可用：" + state.initErr.Error()})[0]
	}
	if state.recoveryErr != nil {
		return boundTaskFailures([]string{"集中整理中断任务对账失败：" + state.recoveryErr.Error()})[0]
	}
	return ""
}

// Caller holds state.mu; Add and admission closure are serialized before Wait.
func (a *App) beginCleanupConsolidationLocked() (context.Context, error) {
	state := &a.consolidationLifecycle
	if a.cleanupService == nil || !state.configured {
		return nil, errors.New("集中整理尚未初始化")
	}
	if state.initErr != nil {
		return nil, fmt.Errorf("集中整理不可用: %w", state.initErr)
	}
	if state.stopped {
		return nil, errors.New("集中整理已停止接受操作，请等待数据库维护完成或重启应用")
	}
	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, errors.New(reason)
	}
	state.wg.Add(1)
	return state.ctx, nil
}

func (a *App) beginCleanupConsolidation() (context.Context, func(), error) {
	state := &a.consolidationLifecycle
	state.mu.Lock()
	ctx, err := a.beginCleanupConsolidationLocked()
	state.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	return ctx, state.wg.Done, nil
}

// Recovery reconciles recorded paths only. It never starts a migration or cleanup.
func (a *App) startCleanupConsolidationRecovery() {
	state := &a.consolidationLifecycle
	state.mu.Lock()
	if state.recovering {
		state.mu.Unlock()
		return
	}
	ctx, err := a.beginCleanupConsolidationLocked()
	if err != nil {
		state.mu.Unlock()
		return
	}
	state.recovering = true
	state.mu.Unlock()
	go func() {
		defer state.wg.Done()
		err := a.cleanupService.RecoverConsolidations(ctx)
		state.mu.Lock()
		state.recovering = false
		if ctx.Err() == nil {
			state.recoveryErr = err
		}
		emit := state.emit
		state.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			log.Printf("集中整理启动对账失败 err=%v", err)
		}
		// The task center includes the recovery warning even when no task row changed.
		if emit != nil && ctx.Err() == nil {
			emit(taskCenterChangedEvent, a.GetTaskCenterSnapshot())
		}
	}()
}

func (a *App) stopCleanupConsolidationAndWait() error {
	state := &a.consolidationLifecycle
	state.mu.Lock()
	state.stopped = true
	if state.cancel != nil {
		state.cancel()
	}
	state.mu.Unlock()
	var err error
	if a.cleanupService != nil {
		err = a.cleanupService.StopConsolidationsAndWait()
	}
	state.wg.Wait()
	return err
}

func (a *App) resumeCleanupConsolidation() {
	state := &a.consolidationLifecycle
	state.mu.Lock()
	if !state.configured || state.initErr != nil || !state.stopped {
		state.mu.Unlock()
		return
	}
	state.ctx, state.cancel = context.WithCancel(context.Background())
	a.cleanupService.ResumeConsolidations()
	state.stopped = false
	state.mu.Unlock()
	// A cancelled reconciliation retains its active slot; reconcile again after reopening.
	a.startCleanupConsolidationRecovery()
}

func (a *App) PreviewCleanupConsolidation(request services.CleanupConsolidationRequest) (*services.CleanupConsolidationPreview, error) {
	ctx, done, err := a.beginCleanupConsolidation()
	if err != nil {
		return nil, err
	}
	defer done()
	return a.cleanupService.PreviewConsolidation(ctx, request)
}

func (a *App) StartCleanupConsolidation(previewID string, addToScanRoots bool) (*services.CleanupConsolidationStatus, error) {
	ctx, done, err := a.beginCleanupConsolidation()
	if err != nil {
		return nil, err
	}
	defer done()
	a.consolidationLifecycle.mu.Lock()
	ready := a.consolidationLifecycle.runtimeReady
	a.consolidationLifecycle.mu.Unlock()
	if !ready {
		return nil, errors.New("应用尚未就绪，请稍后开始集中整理")
	}
	status, err := a.cleanupService.StartConsolidation(ctx, previewID, addToScanRoots)
	if err == nil && addToScanRoots {
		a.reconfigureLibraryWatcher()
	}
	return status, err
}

func (a *App) GetCleanupConsolidationStatus(taskID uint) (*services.CleanupConsolidationStatus, error) {
	_, done, err := a.beginCleanupConsolidation()
	if err != nil {
		return nil, err
	}
	defer done()
	return a.cleanupService.ConsolidationStatus(taskID)
}

// Cancellation remains available while admission is closed or the database is unavailable.
func (a *App) CancelCleanupConsolidation() error {
	if a.cleanupService == nil {
		return errors.New("集中整理尚未初始化")
	}
	return a.cleanupService.CancelConsolidation()
}

func (a *App) GetCleanupConsolidationReview(taskID uint) (*services.CleanupConsolidationReview, error) {
	ctx, done, err := a.beginCleanupConsolidation()
	if err != nil {
		return nil, err
	}
	defer done()
	return a.cleanupService.ReviewConsolidation(ctx, taskID)
}
