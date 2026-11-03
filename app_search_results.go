package main

import (
	"context"
	"video-master/services"
)

func (a *App) beginSearchResults() (context.Context, func(), error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, nil, err
	}
	if a.sceneSearch == nil {
		done()
		return nil, nil, errSceneServicesUnavailable
	}
	request, cancel := context.WithTimeout(ctx, sceneSearchTimeout)
	return request, func() { cancel(); done() }, nil
}
func (a *App) SearchDialogueVideoGroups(request services.DialogueSearchRequest) services.DialogueVideoResult {
	ctx, done, err := a.beginSearchResults()
	if err != nil {
		return services.DialogueVideoResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.sceneSearch.SearchDialogueVideoGroups(ctx, request)
	if err != nil {
		return services.DialogueVideoResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.DialogueVideoResult{Status: "ok", Data: data}
}
func (a *App) ListDialogueVideoHits(request services.DialogueSearchRequest) services.DialogueHitResult {
	ctx, done, err := a.beginSearchResults()
	if err != nil {
		return services.DialogueHitResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.sceneSearch.ListDialogueVideoHits(ctx, request)
	if err != nil {
		return services.DialogueHitResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.DialogueHitResult{Status: "ok", Data: data}
}
func (a *App) SearchVisualResults(request services.SceneSearchRequest) services.RankedSearchResult {
	ctx, done, err := a.beginSearchResults()
	if err != nil {
		return services.RankedSearchResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.sceneSearch.SearchVisualResults(ctx, request)
	if err != nil {
		return services.RankedSearchResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.RankedSearchResult{Status: "ok", Data: data}
}
func (a *App) SearchImageResults(request services.SceneImageSearchRequest) services.RankedSearchResult {
	ctx, done, err := a.beginSearchResults()
	if err != nil {
		return services.RankedSearchResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.sceneSearch.SearchImageResults(ctx, request)
	if err != nil {
		return services.RankedSearchResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.RankedSearchResult{Status: "ok", Data: data}
}
func (a *App) PreviewSearchClips(request services.SearchClipRequest) services.ClipQueuePreviewResult {
	ctx, done, err := a.beginClipOperation()
	if err != nil {
		return services.ClipQueuePreviewResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	queue, err := a.queueService()
	if err != nil {
		return services.ClipQueuePreviewResult{Status: "error", Error: a.experienceError(err)}
	}
	data, err := queue.PreviewSearchClips(ctx, request)
	if err != nil {
		return services.ClipQueuePreviewResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ClipQueuePreviewResult{Status: "ok", Data: data}
}
