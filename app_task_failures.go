package main

import (
	"context"
	"fmt"
	"video-master/models"
	"video-master/services"
)

type TaskFailureRuns struct {
	Runs    []models.TaskFailureRun `json:"runs"`
	Warning string                  `json:"warning"`
}

func (a *App) taskFailureService() (context.Context, *services.TaskFailureService, func(), error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, nil, nil, err
	}
	a.functionalMu.Lock()
	store, warning := a.taskFailures, a.taskFailureInitError
	a.functionalMu.Unlock()
	if store == nil {
		done()
		if warning == "" {
			warning = "失败诊断尚未就绪，请稍后重试"
		}
		return nil, nil, nil, fmt.Errorf("%s", warning)
	}
	return ctx, store, done, nil
}
func (a *App) ListTaskFailureRuns(taskKey string) (*TaskFailureRuns, error) {
	ctx, s, done, err := a.taskFailureService()
	if err != nil {
		return nil, err
	}
	defer done()
	runs, err := s.ListRunsContext(ctx, taskKey)
	if err != nil {
		return nil, err
	}
	return &TaskFailureRuns{Runs: runs, Warning: s.Warning(taskKey)}, nil
}
func (a *App) ListTaskFailures(request services.TaskFailureListRequest) (*services.TaskFailurePage, error) {
	ctx, s, done, err := a.taskFailureService()
	if err != nil {
		return nil, err
	}
	defer done()
	return s.ListContext(ctx, request)
}
func (a *App) PreviewTaskFailureRetry(request services.TaskFailureRetryRequest) (*services.TaskFailureRetryPreview, error) {
	ctx, s, done, err := a.taskFailureService()
	if err != nil {
		return nil, err
	}
	defer done()
	return s.PreviewContext(ctx, request)
}
func (a *App) ConfirmTaskFailureRetry(token string) (string, error) {
	ctx, s, done, err := a.taskFailureService()
	if err != nil {
		return "", err
	}
	defer done()
	return s.Confirm(ctx, token)
}
func (a *App) GetTaskFailureRetryResults(operationID string, offset, limit int) (*services.TaskFailureRetryPage, error) {
	_, s, done, err := a.taskFailureService()
	if err != nil {
		return nil, err
	}
	defer done()
	return s.Results(operationID, offset, limit)
}
func (a *App) CancelTaskFailureRetry(operationID string) error {
	_, s, done, err := a.taskFailureService()
	if err != nil {
		return err
	}
	defer done()
	return s.Cancel(operationID)
}
