package main

import (
	"context"
	"errors"
	"video-master/services"
)

func (a *App) beginClipOperation() (context.Context, func(), error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, nil, err
	}
	if a.videoService == nil {
		done()
		return nil, nil, errors.New("片库服务尚未就绪")
	}
	return ctx, done, nil
}
func (a *App) SaveClipTopic(request services.ClipTopicInput) services.ClipTopicResult {
	ctx, done, err := a.beginClipOperation()
	if err != nil {
		return services.ClipTopicResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := (&services.BookmarkService{}).SaveClipTopic(ctx, request)
	if err != nil {
		return services.ClipTopicResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ClipTopicResult{Status: "ok", Data: data}
}
func (a *App) ListClipTopics(request services.ClipTopicQuery) services.ClipTopicListResult {
	ctx, done, err := a.beginClipOperation()
	if err != nil {
		return services.ClipTopicListResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := (&services.BookmarkService{}).ListClipTopics(ctx, request)
	if err != nil {
		return services.ClipTopicListResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ClipTopicListResult{Status: "ok", Data: data}
}
func (a *App) ListClipTopicMembers(request services.ClipTopicMembersQuery) services.ClipTopicMembersResult {
	ctx, done, err := a.beginClipOperation()
	if err != nil {
		return services.ClipTopicMembersResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := (&services.BookmarkService{}).ListClipTopicMembers(ctx, request)
	if err != nil {
		return services.ClipTopicMembersResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ClipTopicMembersResult{Status: "ok", Data: data}
}
func (a *App) EditClipTopic(request services.ClipTopicEdit) services.ClipTopicResult {
	ctx, done, err := a.beginClipOperation()
	if err != nil {
		return services.ClipTopicResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := (&services.BookmarkService{}).EditClipTopic(ctx, request)
	if err != nil {
		return services.ClipTopicResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ClipTopicResult{Status: "ok", Data: data}
}
func (a *App) SaveClipBookmark(request services.ClipBookmarkInput) services.ClipBookmarkSaveResult {
	ctx, done, err := a.beginClipOperation()
	if err != nil {
		return services.ClipBookmarkSaveResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := (&services.BookmarkService{}).SaveClipBookmark(ctx, request)
	if err != nil {
		return services.ClipBookmarkSaveResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ClipBookmarkSaveResult{Status: "ok", Data: data}
}
func (a *App) GetBookmarkTopicReferences(id uint, revision uint64) services.BookmarkTopicReferencesResult {
	ctx, done, err := a.beginClipOperation()
	if err != nil {
		return services.BookmarkTopicReferencesResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := (&services.BookmarkService{}).BookmarkTopicReferences(ctx, id, revision)
	if err != nil {
		return services.BookmarkTopicReferencesResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.BookmarkTopicReferencesResult{Status: "ok", Data: data}
}
func (a *App) PreviewClipQueue(request services.ClipQueueRequest) services.ClipQueuePreviewResult {
	ctx, done, err := a.beginClipOperation()
	if err != nil {
		return services.ClipQueuePreviewResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	queue, err := a.queueService()
	if err != nil {
		return services.ClipQueuePreviewResult{Status: "error", Error: a.experienceError(err)}
	}
	data, err := queue.PreviewClipQueue(ctx, request)
	if err != nil {
		return services.ClipQueuePreviewResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ClipQueuePreviewResult{Status: "ok", Data: data}
}
func (a *App) AppendClipQueue(request services.AppendClipQueueRequest) services.ClipQueueResult {
	ctx, done, err := a.beginClipOperation()
	if err != nil {
		return services.ClipQueueResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	queue, err := a.queueService()
	if err != nil {
		return services.ClipQueueResult{Status: "error", Error: a.experienceError(err)}
	}
	data, err := queue.AppendClipQueue(ctx, request)
	if err != nil {
		return services.ClipQueueResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ClipQueueResult{Status: "ok", Data: data}
}
func (a *App) DeleteClipTopic(id uint, revision uint64) services.ExperienceMutationResult {
	ctx, done, err := a.beginClipOperation()
	if err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	if err := (&services.BookmarkService{}).DeleteClipTopic(ctx, id, revision); err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ExperienceMutationResult{Status: "ok", Data: &services.ExperienceMutation{Changed: true}}
}
func (a *App) DiscardClipQueuePreview(token string) services.ExperienceMutationResult {
	ctx, done, err := a.beginClipOperation()
	if err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	queue, err := a.queueService()
	if err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	changed, err := queue.DiscardClipQueuePreview(ctx, token)
	if err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ExperienceMutationResult{Status: "ok", Data: &services.ExperienceMutation{Changed: changed}}
}
