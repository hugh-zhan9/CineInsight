package main

import (
	"video-master/services"
)

func (a *App) GetPlaybackSubtitle(request services.PlaybackSubtitleRequest) (*services.PlaybackSubtitle, error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, err
	}
	defer done()

	if err := a.subtitleWorkbenchReady(); err != nil {
		return nil, err
	}
	if request.QueueToken != "" {
		queue, err := a.queueService()
		if err != nil {
			return nil, err
		}
		return queue.GetPlaybackSubtitle(ctx, a.subtitleWorkbench, request)
	}
	return a.subtitleWorkbench.GetPlaybackSubtitle(ctx, request)
}
