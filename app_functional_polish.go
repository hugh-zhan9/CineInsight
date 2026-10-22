package main

import (
	"context"
	"fmt"
	"video-master/services"
)

// beginFunctionalPolish admits a bounded generation of App calls. Cancellation
// precedes waiting, so a confirmation waiting for a subtitle writer can leave.
func (a *App) beginFunctionalPolish() (context.Context, func(), error) {
	a.functionalMu.Lock()
	defer a.functionalMu.Unlock()
	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, nil, fmt.Errorf("%s", reason)
	}
	if a.functionalPaused || a.functionalShuttingDown {
		return nil, nil, fmt.Errorf("功能正在停止或数据库维护中，请稍后重试")
	}
	if a.functionalContext == nil {
		a.functionalContext, a.functionalCancel = context.WithCancel(context.Background())
	}
	a.functionalActive.Add(1)
	return a.functionalContext, a.functionalActive.Done, nil
}

// initializeFunctionalPolish runs after schema setup, before owners start. A
// diagnostics failure disables only bulk retry; original workers remain usable.
func (a *App) initializeFunctionalPolish() error {
	a.functionalTransitionMu.Lock()
	defer a.functionalTransitionMu.Unlock()
	a.functionalMu.Lock()
	if a.functionalShuttingDown {
		a.functionalMu.Unlock()
		return services.ErrPlaybackQuiesced
	}
	if a.functionalInitialized && !a.functionalPaused {
		a.functionalMu.Unlock()
		return nil
	}
	a.functionalPaused = true
	if a.functionalCancel != nil {
		a.functionalCancel()
	}
	previous := a.taskFailures
	a.functionalMu.Unlock()
	a.functionalActive.Wait()
	if previous != nil {
		previous.StopAndWait()
	}
	store := services.NewTaskFailureService()
	var err error
	if reason := a.databaseUnavailableReason(); reason != "" {
		err = fmt.Errorf("%s", reason)
	} else {
		err = store.RecoverInterrupted()
	}
	if err != nil {
		store.StopAndWait()
		store = nil
	} else {
		store.RegisterOwners(a.aiTaggingService, a.technicalBackfill, a.playbackProxies)
	}
	if a.aiTaggingService != nil {
		a.aiTaggingService.SetTaskFailureService(store)
	}
	if a.technicalBackfill != nil {
		a.technicalBackfill.SetTaskFailureService(store)
	}
	if a.playbackProxies != nil {
		a.playbackProxies.SetTaskFailureService(store)
	}
	if a.subtitleWorkbench != nil {
		a.subtitleWorkbench.ResumeSubtitleExtraction()
	}
	a.functionalMu.Lock()
	a.functionalInitialized = true
	a.taskFailures = store
	a.taskFailureInitError = ""
	if err != nil {
		a.taskFailureInitError = "失败诊断恢复未完成，批量重试暂不可用；原任务仍可使用。请重新打开应用后重试。"
	}
	a.functionalContext, a.functionalCancel = context.WithCancel(context.Background())
	a.functionalPaused = false
	a.functionalMu.Unlock()
	return err
}
func (a *App) stopFunctionalPolish(shutdown bool) {
	a.functionalTransitionMu.Lock()
	defer a.functionalTransitionMu.Unlock()
	a.functionalMu.Lock()
	a.functionalPaused = true
	a.functionalShuttingDown = a.functionalShuttingDown || shutdown
	if a.functionalCancel != nil {
		a.functionalCancel()
	}
	store := a.taskFailures
	a.functionalMu.Unlock()
	if a.subtitleWorkbench != nil {
		a.subtitleWorkbench.QuiesceSubtitleExtraction()
	}
	a.functionalActive.Wait()
	if store != nil {
		store.StopAndWait()
	}
}
func (a *App) wireFunctionalPolish(emit func(string, any)) {
	services.SetSubtitleChangedListener(func() { emit("subtitle-changed", nil) })
	if a.videoService != nil {
		a.videoService.SetControlledPlaybackListener(func(state services.ControlledPlaybackState) { emit("controlled-playback-state", state) })
	}
}
func (a *App) functionalPolishWarnings() []string {
	a.functionalMu.Lock()
	store, warning := a.taskFailures, a.taskFailureInitError
	a.functionalMu.Unlock()
	result := []string{}
	if warning != "" {
		result = append(result, warning)
	}
	if store != nil {
		for _, key := range []string{services.TaskFailureAI, services.TaskFailureTechnical, services.TaskFailureProxy} {
			if message := store.Warning(key); message != "" {
				result = append(result, message)
			}
		}
	}
	return result
}
func (a *App) stopFailureOwners() {
	if a.aiTaggingService != nil {
		a.aiTaggingService.StopAndWait()
	}
	if a.technicalBackfill != nil {
		a.technicalBackfill.StopAndWait()
	}
	if a.playbackProxies != nil {
		a.playbackProxies.StopAndWait()
	}
}

// startDiagnosticOwner is serialized with closing this batch's submission generation.
func (a *App) startDiagnosticOwner(ctx context.Context) {
	a.functionalTransitionMu.Lock()
	defer a.functionalTransitionMu.Unlock()
	a.functionalMu.Lock()
	closed := a.functionalPaused || a.functionalShuttingDown
	a.functionalMu.Unlock()
	if !closed && a.aiTaggingService != nil {
		a.aiTaggingService.Start(ctx)
	}
}
