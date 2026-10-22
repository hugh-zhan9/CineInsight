package main

import (
	"fmt"
	"video-master/services"
)

func (a *App) subtitleWorkbenchReady() error {
	if reason := a.databaseUnavailableReason(); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	if a.subtitleWorkbench == nil {
		return fmt.Errorf("字幕服务未初始化")
	}
	return nil
}
func (a *App) ListExtractableSubtitleTracks(videoID uint) (*services.ExtractableSubtitleTracks, error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, err
	}
	defer done()

	if err := a.subtitleWorkbenchReady(); err != nil {
		return nil, err
	}
	return a.subtitleWorkbench.ListExtractableSubtitleTracks(ctx, videoID)
}
func (a *App) PreviewSubtitleExtraction(request services.SubtitleExtractRequest) (*services.SubtitleExtractPreview, error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, err
	}
	defer done()

	if err := a.subtitleWorkbenchReady(); err != nil {
		return nil, err
	}
	return a.subtitleWorkbench.PreviewSubtitleExtraction(ctx, request)
}
func (a *App) ConfirmSubtitleExtraction(request services.SubtitleExtractConfirm) (*services.SubtitleSaveResult, error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, err
	}
	defer done()

	if err := a.subtitleWorkbenchReady(); err != nil {
		return nil, err
	}
	return a.subtitleWorkbench.ConfirmSubtitleExtraction(ctx, request)
}
func (a *App) CancelSubtitleExtraction(videoID uint) {
	if a.subtitleWorkbench != nil {
		a.subtitleWorkbench.CancelSubtitleExtraction(videoID)
	}
}
