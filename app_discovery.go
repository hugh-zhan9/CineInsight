package main

import (
	"context"
	"fmt"
	"time"
	"video-master/services"
)

func (a *App) SuggestTonightVideos(request services.TonightRequest) ([]services.TonightSuggestion, error) {
	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	if a.videoService == nil {
		return nil, fmt.Errorf("视频服务未初始化")
	}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 30*time.Second)
	defer cancel()
	return a.videoService.SuggestTonight(ctx, request)
}
