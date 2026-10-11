package main

import (
	"context"
	"fmt"
	"time"
	"video-master/services"
)

// SuggestTonightVideos 是「今晚看什么」：与随机播放同一套条件，按均衡加权抽 limit 部候选，只读。
func (a *App) SuggestTonightVideos(request services.RandomPlayRequest, limit int) ([]services.TonightSuggestion, error) {
	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	if a.videoService == nil {
		return nil, fmt.Errorf("视频服务未初始化")
	}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 30*time.Second)
	defer cancel()
	return a.videoService.SuggestTonight(ctx, request, limit)
}
