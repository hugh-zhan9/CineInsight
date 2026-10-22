package main

import (
	"fmt"
	"video-master/models"
	"video-master/services"
)

func (a *App) SetRecommendationFeedback(request services.RecommendationFeedbackRequest) (*models.RecommendationFeedback, error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, err
	}
	defer done()

	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	if a.videoService == nil {
		return nil, fmt.Errorf("视频服务未初始化")
	}
	return a.videoService.SetRecommendationFeedback(ctx, request)
}
func (a *App) ListRecommendationFeedback() ([]services.RecommendationFeedbackItem, error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, err
	}
	defer done()

	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	if a.videoService == nil {
		return nil, fmt.Errorf("视频服务未初始化")
	}
	return a.videoService.ListRecommendationFeedback(ctx)
}
func (a *App) RevokeRecommendationFeedback(id uint, revision int64) error {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return err
	}
	defer done()

	if reason := a.databaseUnavailableReason(); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	if a.videoService == nil {
		return fmt.Errorf("视频服务未初始化")
	}
	return a.videoService.RevokeRecommendationFeedback(ctx, id, revision)
}
