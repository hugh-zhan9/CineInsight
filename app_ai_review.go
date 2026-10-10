package main

import (
	"context"
	"fmt"
	"time"
	"video-master/services"
)

func (a *App) aiReviewWorkflow(kind string) (*services.ReviewApprovalWorkflow, error) {
	switch kind {
	case "video":
		if a.aiTaggingService != nil {
			workflow := a.aiTaggingService.ReviewApproval()
			return workflow, nil
		}
	case "image":
		if service := a.imageAITaggingService(); service != nil {
			workflow := service.ReviewApproval()
			return workflow, nil
		}
	default:
		return nil, fmt.Errorf("review_kind_invalid: 无效的审阅媒体类型")
	}
	return nil, fmt.Errorf("review_unavailable: 审阅服务未初始化")
}

// Claim the workflow under the existing maintenance lock. Its own closed gate then
// covers the interval between releasing this lock and registering a preview/job.
func (a *App) writableAIReviewWorkflow(kind string) (*services.ReviewApprovalWorkflow, error) {
	if !a.restoreMu.TryLock() {
		return nil, errDatabaseMaintenanceBusy
	}
	defer a.restoreMu.Unlock()
	if a.restoreTerminal {
		return nil, services.ErrReviewClosed
	}
	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	workflow, err := a.aiReviewWorkflow(kind)
	if err == nil {
		workflow.SetProgressNotifier(a.notifyTaskCenterChanged)
	}
	return workflow, err
}

func (a *App) PreviewAIReviewApproval(kind string, request services.ReviewApprovalRequest) (services.ReviewApprovalPreview, error) {
	workflow, err := a.writableAIReviewWorkflow(kind)
	if err != nil {
		return services.ReviewApprovalPreview{}, err
	}
	return workflow.Preview(a.backgroundContext(), request)
}
func (a *App) StartAIReviewApproval(kind, token string) (services.ReviewApprovalState, error) {
	workflow, err := a.writableAIReviewWorkflow(kind)
	if err != nil {
		return services.ReviewApprovalState{}, err
	}
	return workflow.Start(a.backgroundContext(), token)
}
func (a *App) GetAIReviewApproval(kind, token string, after, limit int) (services.ReviewApprovalState, error) {
	workflow, err := a.aiReviewWorkflow(kind)
	if err != nil {
		return services.ReviewApprovalState{}, err
	}
	return workflow.State(token, after, limit)
}
func (a *App) CancelAIReviewApproval(kind, token string) error {
	workflow, err := a.aiReviewWorkflow(kind)
	if err != nil {
		return err
	}
	return workflow.Cancel(token)
}

func (a *App) stopAIReviewApprovals() {
	if a.aiTaggingService != nil {
		a.aiTaggingService.ReviewApproval().CloseAndWait()
	}
	if service := a.imageAITaggingService(); service != nil {
		service.ReviewApproval().CloseAndWait()
	}
}

func (a *App) GetAIReviewCandidates(kind string, ids []uint) (services.ReviewCandidateRefresh, error) {
	result := services.ReviewCandidateRefresh{VideoItems: []services.AITaggingReviewItem{}, ImageItems: []services.ImageAITaggingReviewItem{}}
	if reason := a.databaseUnavailableReason(); reason != "" {
		return result, fmt.Errorf("%s", reason)
	}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 5*time.Second)
	defer cancel()
	var err error
	switch kind {
	case "video":
		if a.aiTaggingService == nil {
			return result, fmt.Errorf("审阅服务未初始化")
		}
		result.VideoItems, err = a.aiTaggingService.RefreshPendingReviewCandidates(ctx, ids)
	case "image":
		service := a.imageAITaggingService()
		if service == nil {
			return result, fmt.Errorf("审阅服务未初始化")
		}
		result.ImageItems, err = service.RefreshPendingReviewCandidates(ctx, ids)
	default:
		return result, fmt.Errorf("review_kind_invalid: 无效的审阅媒体类型")
	}
	return result, err
}
