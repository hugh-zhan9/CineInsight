package main

import (
	"context"
	"errors"
	"time"
	"video-master/services"
)

func (a *App) beginExplorationOperation() (context.Context, func(), error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, nil, err
	}
	if a.mediaExploration == nil {
		done()
		return nil, nil, errors.New("探索服务尚未就绪")
	}
	limited, cancel := context.WithTimeout(ctx, 30*time.Second)
	return limited, func() { cancel(); done() }, nil
}

func (a *App) explorationError(err error) *services.ExperienceError {
	code, message := "", ""
	switch {
	case errors.Is(err, services.ErrExplorationInvalid):
		code, message = "invalid_request", "请核对探索操作后重试"
	case errors.Is(err, services.ErrExplorationConflict):
		code, message = "exploration_conflict", "浏览进度已变化，请刷新当前场景"
	case errors.Is(err, services.ErrExplorationScene):
		code, message = "exploration_stale_scene", "这一墙已变化，请重新打开探索"
	case errors.Is(err, services.ErrExplorationDatabase):
		code, message = "exploration_database_changed", "数据库已重新连接，请重新打开探索"
	case errors.Is(err, services.ErrExplorationUnavailable):
		code, message = "exploration_unavailable", "内容暂时无法预览，或已不在当前片库范围"
	case errors.Is(err, services.ErrChapterDurationUnknown):
		code, message = "duration_unknown", "片长未知，请先在详情中补全技术信息"
	case errors.Is(err, services.ErrBookmarkSourceChanged):
		code, message = "source_changed", "原文件已变化，请关闭预览后重新打开"
	}
	if code != "" {
		return &services.ExperienceError{Code: code, Message: message}
	}
	return a.experienceError(err)
}

func (a *App) OpenMediaExploration(scope string) services.ExplorationResult {
	ctx, done, err := a.beginExplorationOperation()
	if err != nil {
		return services.ExplorationResult{Status: "error", Error: a.explorationError(err)}
	}
	defer done()
	data, err := a.mediaExploration.Open(ctx, scope)
	if err != nil {
		return services.ExplorationResult{Status: "error", Error: a.explorationError(err)}
	}
	a.explorationQuit.mu.Lock()
	a.explorationQuit.clientSeen = true
	a.explorationQuit.mu.Unlock()
	return services.ExplorationResult{Status: "ok", Data: data}
}
func (a *App) GetMediaExplorationItems(request services.ExplorationItemsRequest) services.ExplorationCardsResult {
	ctx, done, err := a.beginExplorationOperation()
	if err != nil {
		return services.ExplorationCardsResult{Status: "error", Error: a.explorationError(err)}
	}
	defer done()
	data, err := a.mediaExploration.Items(ctx, request)
	if err != nil {
		return services.ExplorationCardsResult{Status: "error", Error: a.explorationError(err)}
	}
	return services.ExplorationCardsResult{Status: "ok", Data: data}
}
func (a *App) UpdateMediaExploration(request services.ExplorationRequest) services.ExplorationResult {
	ctx, done, err := a.beginExplorationOperation()
	if err != nil {
		return services.ExplorationResult{Status: "error", Error: a.explorationError(err)}
	}
	defer done()
	data, err := a.mediaExploration.Update(ctx, request)
	if err != nil {
		return services.ExplorationResult{Status: "error", Error: a.explorationError(err)}
	}
	return services.ExplorationResult{Status: "ok", Data: data}
}
