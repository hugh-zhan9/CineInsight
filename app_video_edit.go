package main

import (
	"context"
	"errors"
	"log"
	"strconv"
	"time"

	"video-master/services"
)

// 视频工作台（视频编辑合同「接口」）。App 只接线：数据库不可读时直接报 video_edit_unavailable，
// 其余一律交给 VideoEditService；返回给界面的错误擦掉绝对路径。

const videoEditStateEvent = "video-edit-state"

const (
	videoEditCallTimeout      = 30 * time.Second
	videoEditPreflightTimeout = 10 * time.Minute
)

func (a *App) videoEditService() (*services.VideoEditService, error) {
	if a.videoEdit == nil {
		return nil, errors.New("video_edit_unavailable: 视频工作台未初始化")
	}
	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, errors.New("video_edit_unavailable: " + reason)
	}
	return a.videoEdit, nil
}

func (a *App) videoEditCall(timeout time.Duration) (*services.VideoEditService, context.Context, context.CancelFunc, error) {
	service, err := a.videoEditService()
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), timeout)
	return service, ctx, cancel, nil
}

func videoEditResult[T any](name string, value T, err error) (T, error) {
	if err != nil {
		log.Printf("API %s err=%v", name, services.WithoutAbsolutePaths(err))
		var zero T
		return zero, services.WithoutAbsolutePaths(err)
	}
	return value, nil
}

// CreateEditProject 新建编辑项目（kind=merge|trim_intro|hd_replace；hd_replace 的 video_ids 为 [长版, 高清版]）。
func (a *App) CreateEditProject(request services.EditProjectCreateRequest) (*services.EditProjectView, error) {
	service, ctx, cancel, err := a.videoEditCall(videoEditCallTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	view, err := service.CreateProject(ctx, request)
	return videoEditResult("CreateEditProject", view, err)
}

// DuplicateEditProject 以已有项目的配方新建草稿副本，供排队后无法原地修正的项目重新编辑。
func (a *App) DuplicateEditProject(projectID uint) (*services.EditProjectView, error) {
	service, ctx, cancel, err := a.videoEditCall(videoEditCallTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	view, err := service.DuplicateProject(ctx, projectID)
	return videoEditResult("DuplicateEditProject", view, err)
}

// ListEditProjects 列出最近的编辑项目（limit≤0 时 50，上限 200）。
func (a *App) ListEditProjects(limit int) ([]services.EditProjectSummary, error) {
	service, ctx, cancel, err := a.videoEditCall(videoEditCallTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	list, err := service.ListProjects(ctx, limit)
	return videoEditResult("ListEditProjects", list, err)
}

// GetEditProject 返回项目详情（配方、分析结果、导出项）。
func (a *App) GetEditProject(projectID uint) (*services.EditProjectView, error) {
	service, ctx, cancel, err := a.videoEditCall(videoEditCallTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	view, err := service.GetProject(ctx, projectID)
	return videoEditResult("GetEditProject", view, err)
}

// UpdateEditRecipe 以 expected_revision 乐观更新草稿配方；冲突返回 edit_project_conflict。
func (a *App) UpdateEditRecipe(request services.EditRecipeUpdateRequest) (*services.EditProjectView, error) {
	service, ctx, cancel, err := a.videoEditCall(videoEditCallTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	view, err := service.UpdateRecipe(ctx, request)
	return videoEditResult("UpdateEditRecipe", view, err)
}

// AnalyzeEditProject 启动片头识别（trim_intro）或分段对齐（hd_replace），立即返回 analyzing 状态的项目。
func (a *App) AnalyzeEditProject(request services.EditAnalyzeRequest) (*services.EditProjectView, error) {
	service, ctx, cancel, err := a.videoEditCall(videoEditCallTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	view, err := service.AnalyzeProject(ctx, request)
	return videoEditResult("AnalyzeEditProject", view, err)
}

// CancelEditAnalysis 取消进行中的分析（已确认的配方项不变）。
func (a *App) CancelEditAnalysis(projectID uint) error {
	service, ctx, cancel, err := a.videoEditCall(videoEditCallTimeout)
	if err != nil {
		return err
	}
	defer cancel()
	_, err = videoEditResult("CancelEditAnalysis", struct{}{}, service.CancelAnalysis(ctx, projectID))
	return err
}

// PreflightEditProject 对当前配方做预检（来源、规格、轨道映射、快速模式切点、空间与警告）。
func (a *App) PreflightEditProject(projectID uint) (*services.EditPreflight, error) {
	service, ctx, cancel, err := a.videoEditCall(videoEditPreflightTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	result, err := service.PreflightProject(ctx, projectID)
	return videoEditResult("PreflightEditProject", result, err)
}

// QueueEditProject 重新预检并在无错误、警告全部确认时冻结计划排队；queued=false 时看 preflight/unacknowledged。
func (a *App) QueueEditProject(request services.EditQueueRequest) (*services.EditQueueResult, error) {
	service, ctx, cancel, err := a.videoEditCall(videoEditPreflightTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	result, err := service.QueueProject(ctx, request)
	return videoEditResult("QueueEditProject", result, err)
}

// CancelEditProject 取消排队/运行中的导出；已发布的成品保持完成。
func (a *App) CancelEditProject(projectID uint) error {
	service, ctx, cancel, err := a.videoEditCall(videoEditCallTimeout)
	if err != nil {
		return err
	}
	defer cancel()
	_, err = videoEditResult("CancelEditProject", struct{}{}, service.CancelProject(ctx, projectID))
	return err
}

// RequeueEditProject 对中断/失败/部分成功的项目「继续未完成项」。
func (a *App) RequeueEditProject(request services.EditQueueRequest) (*services.EditQueueResult, error) {
	service, ctx, cancel, err := a.videoEditCall(videoEditPreflightTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	result, err := service.RequeueProject(ctx, request)
	return videoEditResult("RequeueEditProject", result, err)
}

// DeleteEditProject 删除非排队/运行/分析中的项目记录；不删来源、不删成品。
func (a *App) DeleteEditProject(projectID uint) error {
	service, ctx, cancel, err := a.videoEditCall(videoEditCallTimeout)
	if err != nil {
		return err
	}
	defer cancel()
	_, err = videoEditResult("DeleteEditProject", struct{}{}, service.DeleteProject(ctx, projectID))
	return err
}

// GetVideoEditStatus 返回工作台整体状态；数据库不可读时只有内存部分。
func (a *App) GetVideoEditStatus() services.VideoEditStatus {
	if a.videoEdit == nil {
		return services.VideoEditStatus{}
	}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 5*time.Second)
	defer cancel()
	if a.databaseUnavailableReason() != "" {
		return a.videoEdit.MemoryStatus()
	}
	return a.videoEdit.Status(ctx)
}

// recoverVideoEdit 是启动对账与孤儿工作目录清扫（startup 里异步调用）。
func (a *App) recoverVideoEdit(ctx context.Context) {
	if a.videoEdit == nil || a.databaseUnavailableReason() != "" {
		return
	}
	a.videoEdit.RecoverOnStartup(ctx)
	if removed, err := a.videoEdit.SweepOrphanWorkdirs(ctx); err != nil {
		log.Printf("App startup video edit workdir sweep failed err=%v", services.WithoutAbsolutePaths(err))
	} else if removed > 0 {
		log.Printf("App startup video edit workdir sweep removed=%d", removed)
	}
}

// ===== 任务中心与退出确认 =====

func (a *App) taskCenterVideoEditProjects(src *taskCenterSources) []services.EditProjectSummary {
	if a.videoEdit == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 5*time.Second)
	defer cancel()
	projects, err := a.videoEdit.ListProjects(ctx, taskCenterRecentPerKind)
	if err != nil {
		log.Printf("API GetTaskCenterSnapshot list video edit projects err=%v", services.WithoutAbsolutePaths(err))
		src.warn("视频工作台任务记录读取失败")
	}
	return projects
}

func videoEditRecentJob(project services.EditProjectSummary) TaskRecentJob {
	job := TaskRecentJob{
		Kind: TaskRecentKindVideoEdit, ID: strconv.FormatUint(uint64(project.ID), 10), Title: project.Title,
		Status: project.Status, ErrorCode: project.ErrorCode, Message: project.ErrorMessage, FinishedAt: project.FinishedAt,
		Actions: []string{},
	}
	if project.Status == "queued" || project.Status == "running" {
		job.Actions = append(job.Actions, "cancel")
	}
	job.Actions = append(job.Actions, "open_video_edit")
	return job
}

// videoEditQuitTask：有排队或运行中的导出项时拦下退出（合同「维护与退出」）。
func (a *App) videoEditQuitTask(running map[string]bool) (QuitBlockingTask, bool) {
	task := QuitBlockingTask{Key: string(services.BackgroundTaskVideoEdit), Names: []string{}}
	if a.videoEdit != nil && a.databaseUnavailableReason() == "" {
		ctx, cancel := context.WithTimeout(a.backgroundContext(), 2*time.Second)
		queued, active, err := a.videoEdit.ActiveItemCounts(ctx)
		cancel()
		if err == nil {
			task.Queued, task.Running = queued, active
		}
	}
	if task.Running == 0 && running[task.Key] {
		task.Running = 1
	}
	return task, task.Running+task.Queued > 0
}
