package main

import (
	"context"
	"errors"
	"log"
	"time"

	"video-master/services"
)

// 场景检索（D-MW-SCENES，场景检索合同）：运行时准备、画面索引任务与检索查询的 Wails 绑定。
// 日志只记模式、条数、状态与错误代码，不记查询词、字幕文本、描述或任何路径。

// sceneSearchTimeout 是一次检索的整体期限（含首次拉起 worker 加载模型）。
const sceneSearchTimeout = 2 * time.Minute

var errSceneServicesUnavailable = errors.New("scene_services_unavailable")

// GetSceneRuntimeStatus 返回本地运行时状态与缺什么的原因。
func (a *App) GetSceneRuntimeStatus() services.SceneRuntimeStatus {
	return a.sceneRuntime.Status()
}

// PrepareSceneRuntime 显式准备运行时：托管 Python、venv、依赖与模型（会联网）。
func (a *App) PrepareSceneRuntime() (services.SceneRuntimeStatus, error) {
	status, err := a.sceneRuntime.Prepare(a.backgroundContext())
	log.Printf("API PrepareSceneRuntime state=%s preparing=%v err=%v", status.State, status.Preparing, err)
	return status, err
}

// CancelSceneRuntimePrepare 取消正在进行的准备。
func (a *App) CancelSceneRuntimePrepare() error {
	err := a.sceneRuntime.CancelPrepare()
	log.Printf("API CancelSceneRuntimePrepare err=%v", err)
	return err
}

// StartSceneIndex 为选定范围建立画面索引（单 worker、FIFO、可取消）。
func (a *App) StartSceneIndex(request services.SceneIndexRequest) (services.SceneIndexStatus, error) {
	if a.sceneIndex == nil {
		return services.SceneIndexStatus{}, errSceneServicesUnavailable
	}
	if reason := a.databaseUnavailableReason(); reason != "" {
		return services.SceneIndexStatus{}, errors.New(reason)
	}
	status, err := a.sceneIndex.Start(a.backgroundContext(), request)
	log.Printf("API StartSceneIndex ids=%d filter=%v provider=%s running=%v err=%v",
		len(request.VideoIDs), request.Filter != nil, status.Provider, status.Running, err)
	return status, err
}

// GetSceneIndexStatus 返回建索引任务的当前状态。
func (a *App) GetSceneIndexStatus() services.SceneIndexStatus {
	if a.sceneIndex == nil {
		return services.SceneIndexStatus{Failures: []services.SceneIndexFailure{}}
	}
	return a.sceneIndex.Status()
}

// CancelSceneIndex 取消建索引任务（含排队中的请求）。
func (a *App) CancelSceneIndex() error {
	if a.sceneIndex == nil {
		return services.ErrSceneIndexNotRunning
	}
	err := a.sceneIndex.Cancel()
	log.Printf("API CancelSceneIndex err=%v", err)
	return err
}

// ClearStaleSceneIndex 删除非当前模型的全部画面段与状态。
func (a *App) ClearStaleSceneIndex() (services.SceneIndexCleanupResult, error) {
	if a.sceneIndex == nil {
		return services.SceneIndexCleanupResult{}, errSceneServicesUnavailable
	}
	if reason := a.databaseUnavailableReason(); reason != "" {
		return services.SceneIndexCleanupResult{}, errors.New(reason)
	}
	result, err := a.sceneIndex.ClearStaleIndex(a.backgroundContext())
	log.Printf("API ClearStaleSceneIndex segments=%d states=%d err=%v", result.DeletedSegments, result.DeletedStates, err)
	return result, err
}

// SearchScenes 在字幕对白与画面里检索场景，返回时间区间、来源、覆盖率与提示。
func (a *App) SearchScenes(request services.SceneSearchRequest) (services.SceneSearchResult, error) {
	if a.sceneSearch == nil {
		return services.SceneSearchResult{}, errSceneServicesUnavailable
	}
	if reason := a.databaseUnavailableReason(); reason != "" {
		return services.SceneSearchResult{}, errors.New(reason)
	}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), sceneSearchTimeout)
	defer cancel()
	result, err := a.sceneSearch.Search(ctx, request)
	log.Printf("API SearchScenes mode=%s limit=%d filter=%v hits=%d notices=%v err=%v",
		request.Mode, request.Limit, request.Filter != nil, len(result.Hits), result.Notices, err)
	return result, err
}

// GetSceneCoverage 返回筛选范围内的字幕与画面索引覆盖（不执行检索）。
func (a *App) GetSceneCoverage(filter services.LibraryFilter) (services.SceneCoverage, error) {
	if a.sceneSearch == nil {
		return services.SceneCoverage{}, errSceneServicesUnavailable
	}
	if reason := a.databaseUnavailableReason(); reason != "" {
		return services.SceneCoverage{}, errors.New(reason)
	}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 30*time.Second)
	defer cancel()
	return a.sceneSearch.GetCoverage(ctx, &filter)
}

// healthSceneRuntime 只读运行时的缓存状态：不启动 Python、不读文件、不读设置。
func (a *App) healthSceneRuntime() HealthItem {
	item := healthItem("scene", "场景检索模型", "unknown", "not_initialized", "action:settings:scenes")
	if a.sceneRuntime == nil {
		return item
	}
	status, checkedAt, checked := a.sceneRuntime.CachedStatus()
	if !checked {
		return healthItem("scene", "场景检索模型", "unknown", "not_checked", "action:settings:scenes")
	}
	code := "missing_runtime"
	switch status.State {
	case services.SceneRuntimeStateAvailable:
		code = "ready"
	case services.SceneRuntimeStateMissingModel:
		code = "missing_model"
	case services.SceneRuntimeStateIncompatible:
		code = "unsupported_platform"
	case services.SceneRuntimeStateDownloadFailed:
		code = "check_failed"
	}
	item = healthAvailable("scene", "场景检索模型", status.State == services.SceneRuntimeStateAvailable, code, "action:settings:scenes")
	if status.Preparing {
		item = healthItem("scene", "场景检索模型", "warning", "preparing", "action:settings:scenes")
	}
	item.Detail += "（缓存检查于 " + checkedAt.Local().Format("2006-01-02 15:04:05") + "）"
	item.ObservedAt = &checkedAt
	return item
}

// healthSceneIndex 描述当前模型的画面索引覆盖（库内已发布数据，不证明外部接口可达）。
func (a *App) healthSceneIndex(ctx context.Context) HealthItem {
	item := healthItem("scene_visual", "场景画面索引", "unknown", "not_initialized", "nav:page:scenes")
	if a.sceneSearch == nil {
		return item
	}
	coverage, err := a.sceneSearch.GetCoverage(ctx, nil)
	if err != nil {
		return healthItem("scene_visual", "场景画面索引", "unknown", "check_failed", "nav:page:scenes")
	}
	item = healthItem("scene_visual", "场景画面索引", "ok", "snapshot", "nav:page:scenes")
	if coverage.VisualIndexed == 0 {
		item.State, item.ReasonCode, item.Detail = "warning", "not_checked", "尚未建立画面索引"
	}
	if a.sceneIndex != nil && a.sceneIndex.Status().Running {
		item.State, item.ReasonCode, item.Detail = "warning", "running", healthReasonText["running"]
	}
	item.Metrics = []HealthMetric{
		healthMetric("visual_indexed", "画面已索引", coverage.VisualIndexed, "部"),
		healthMetric("subtitle_indexed", "有字幕索引", coverage.SubtitleIndexed, "部"),
		healthMetric("total", "活跃视频", coverage.TotalVideos, "部"),
	}
	return item
}
