package main

import (
	"context"
	"errors"
	"video-master/services"
)

func (a *App) beginChapterOperation(generation bool) (context.Context, func(), error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, nil, err
	}
	a.functionalMu.Lock()
	warning := a.chapterInitError
	a.functionalMu.Unlock()
	if a.chapters == nil || generation && warning != "" {
		done()
		return nil, nil, errors.New("章节服务尚未就绪")
	}
	return ctx, done, nil
}
func (a *App) GetVideoChapterSets(videoID uint) services.ChapterSetsResult {
	ctx, done, err := a.beginChapterOperation(false)
	if err != nil {
		return services.ChapterSetsResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.chapters.GetVideoChapterSets(ctx, videoID)
	if err != nil {
		return services.ChapterSetsResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ChapterSetsResult{Status: "ok", Data: data}
}
func (a *App) SaveVideoChapters(request services.SaveChaptersRequest) services.ChapterSetResult {
	ctx, done, err := a.beginChapterOperation(false)
	if err != nil {
		return services.ChapterSetResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.chapters.SaveVideoChapters(ctx, request)
	if err != nil {
		return services.ChapterSetResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ChapterSetResult{Status: "ok", Data: data}
}
func (a *App) ResolveChapter(request services.ResolveChapterRequest) services.ChapterResolutionResult {
	ctx, done, err := a.beginChapterOperation(false)
	if err != nil {
		return services.ChapterResolutionResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.chapters.ResolveChapter(ctx, request)
	if err != nil {
		return services.ChapterResolutionResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ChapterResolutionResult{Status: "ok", Data: data}
}
func (a *App) PrepareChapterDraft(request services.PrepareChapterRequest) services.ChapterPreparationResult {
	ctx, done, err := a.beginChapterOperation(true)
	if err != nil {
		return services.ChapterPreparationResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.chapters.PrepareChapterDraft(ctx, request)
	if err != nil {
		return services.ChapterPreparationResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ChapterPreparationResult{Status: "ok", Data: data}
}
func (a *App) StartChapterDraft(request services.StartChapterRequest) services.ChapterStartResult {
	ctx, done, err := a.beginChapterOperation(true)
	if err != nil {
		return services.ChapterStartResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.chapters.StartChapterDraft(ctx, request)
	if err != nil {
		return services.ChapterStartResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ChapterStartResult{Status: "ok", Data: data}
}
func (a *App) ListChapterDraftJobs(request services.ChapterJobQuery) services.ChapterJobListResult {
	ctx, done, err := a.beginChapterOperation(false)
	if err != nil {
		return services.ChapterJobListResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.chapters.ListChapterDraftJobs(ctx, request)
	if err != nil {
		return services.ChapterJobListResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ChapterJobListResult{Status: "ok", Data: data}
}
func (a *App) GetChapterDraft(jobID uint) services.ChapterDraftResult {
	ctx, done, err := a.beginChapterOperation(false)
	if err != nil {
		return services.ChapterDraftResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.chapters.GetChapterDraft(ctx, jobID)
	if err != nil {
		return services.ChapterDraftResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ChapterDraftResult{Status: "ok", Data: data}
}
func (a *App) PreviewChapterApplication(request services.ChapterApplicationRequest) services.ChapterApplicationPreviewResult {
	ctx, done, err := a.beginChapterOperation(false)
	if err != nil {
		return services.ChapterApplicationPreviewResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.chapters.PreviewChapterApplication(ctx, request)
	if err != nil {
		return services.ChapterApplicationPreviewResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ChapterApplicationPreviewResult{Status: "ok", Data: data}
}
func (a *App) ApplyChapterDraft(token string) services.ChapterApplicationResult {
	ctx, done, err := a.beginChapterOperation(false)
	if err != nil {
		return services.ChapterApplicationResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.chapters.ApplyChapterDraft(ctx, token)
	if err != nil {
		return services.ChapterApplicationResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ChapterApplicationResult{Status: "ok", Data: data}
}
func (a *App) CancelChapterDraft(id uint, revision uint64) services.ExperienceMutationResult {
	ctx, done, err := a.beginChapterOperation(false)
	if err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	if err := a.chapters.CancelChapterDraft(ctx, id, revision); err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ExperienceMutationResult{Status: "ok", Data: &services.ExperienceMutation{Changed: true}}
}
func (a *App) DiscardChapterDraft(id uint, revision uint64) services.ExperienceMutationResult {
	ctx, done, err := a.beginChapterOperation(false)
	if err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	if err := a.chapters.DiscardChapterDraft(ctx, id, revision); err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ExperienceMutationResult{Status: "ok", Data: &services.ExperienceMutation{Changed: true}}
}
func (a *App) DiscardChapterApplication(token string) services.ExperienceMutationResult {
	ctx, done, err := a.beginChapterOperation(false)
	if err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	changed, err := a.chapters.DiscardChapterApplication(ctx, token)
	if err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ExperienceMutationResult{Status: "ok", Data: &services.ExperienceMutation{Changed: changed}}
}
func (a *App) CancelAllChapterDrafts() services.ExperienceMutationResult {
	_, done, err := a.beginChapterOperation(false)
	if err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	changed := a.chapters.CancelActiveDrafts() > 0
	return services.ExperienceMutationResult{Status: "ok", Data: &services.ExperienceMutation{Changed: changed}}
}

func (a *App) ListChapterSets(request services.ChapterSetQuery) services.ChapterSetListResult {
	ctx, done, err := a.beginChapterOperation(false)
	if err != nil {
		return services.ChapterSetListResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.chapters.ListChapterSets(ctx, request)
	if err != nil {
		return services.ChapterSetListResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ChapterSetListResult{Status: "ok", Data: data}
}
