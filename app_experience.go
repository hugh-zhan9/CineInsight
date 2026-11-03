package main

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"strings"
	"video-master/database"
	"video-master/services"
)

func (a *App) experienceError(err error) *services.ExperienceError {
	if a.databaseSwitchService != nil && a.databaseSwitchService.RelaunchPending() {
		return &services.ExperienceError{Code: "relaunch_pending", Message: "请先重新打开应用"}
	}
	a.functionalMu.Lock()
	paused := a.functionalPaused || a.functionalShuttingDown
	a.functionalMu.Unlock()
	if paused || database.MaintenanceActive() || errors.Is(err, database.ErrMaintenance) {
		return &services.ExperienceError{Code: "maintenance", Message: "正在维护或停止，请稍后重试"}
	}
	code, message := "dependency_unavailable", "操作未完成，请刷新后重试"
	switch {
	case errors.Is(err, services.ErrPhotoFilterInvalid):
		code, message = "photo_filter_invalid", "请核对地图范围、日期与人物条件；失效人物不会自动移除"
	case errors.Is(err, services.ErrPhotoCursorInvalid):
		code, message = "photo_cursor_invalid", "查询条件已变化或分页已过期，请刷新后重新选择"
	case errors.Is(err, services.ErrPhotoRecapConflict):
		code, message = "conflict", "回顾条件已变化，请刷新后重新核对"
	case errors.Is(err, services.ErrMapTileInvalid):
		code, message = "invalid_input", "请核对底图参数并明确确认联网范围"
	case errors.Is(err, services.ErrMapTileChanged):
		code, message = "map_tile_changed", "底图状态已变化，请刷新后重试"
	case errors.Is(err, services.ErrMapTilesDisabled):
		code, message = "map_tiles_disabled", "联网底图已关闭，该区域没有可用缓存"
	case errors.Is(err, services.ErrMapCacheFull):
		code, message = "map_cache_full", "底图缓存已达预算，请清理缓存后再加载新区域"
	case errors.Is(err, services.ErrMapCacheUnavailable):
		code, message = "map_cache_unavailable", "底图缓存不可用，可清理底图缓存；照片数据不受影响"
	case errors.Is(err, services.ErrMapTileUnavailable):
		code, message = "map_tile_unavailable", "底图暂不可用，照片点与本地回顾仍可使用"
	case errors.Is(err, services.ErrChapterInvalid):
		code, message = "invalid_input", "请检查章节标题与起止时间；章节须按时间排列且不能重叠"
	case errors.Is(err, services.ErrChapterConflict):
		code, message = "conflict", "章节或草稿已变化，请刷新后重新核对"
	case errors.Is(err, services.ErrChapterDurationUnknown):
		code, message = "chapter_duration_unknown", "片长未知，请先补全技术信息"
	case errors.Is(err, services.ErrChapterSubtitleUnavailable):
		code, message = "chapter_subtitle_unavailable", "需要有效的同名SRT字幕，请先到字幕工作台准备或修复"
	case errors.Is(err, services.ErrChapterSummaryTooLarge):
		code, message = "summary_input_too_large", "字幕范围过大或单条字幕过长，请缩小范围或整理字幕后重新生成"
	case errors.Is(err, services.ErrChapterDraftObsolete):
		code, message = "chapter_draft_obsolete", "原片、字幕或人工章节已变化，请重新核对生成范围"
	case errors.Is(err, services.ErrChapterDraftState):
		code, message = "chapter_draft_state", "草稿状态已变化，当前不能执行此操作"
	case errors.Is(err, services.ErrChapterRequestUsed):
		code, message = "chapter_request_used", "这次请求编号已用于不同范围，请重新发起"
	case errors.Is(err, services.ErrChapterDraftBusy):
		code, message = "limit_exceeded", "待处理章节任务或预览过多，请先处理已有项目"
	case errors.Is(err, services.ErrSearchCursorInvalid):
		code, message = "search_cursor_invalid", "分页条件已变化或游标过期，请刷新这一组结果"
	case errors.Is(err, services.ErrSceneQueryInvalid), errors.Is(err, services.ErrSceneImageInvalid):
		code, message = "invalid_input", "请检查搜索文字或查询图片"
	case errors.Is(err, services.ErrClipTopicChanged), errors.Is(err, services.ErrViewingNoteConflict), errors.Is(err, services.ErrQueueChanged):
		code, message = "conflict", "资料已变化，请刷新后重新核对"
	case errors.Is(err, services.ErrClipTopicInvalid), errors.Is(err, services.ErrViewingNoteInvalid), errors.Is(err, services.ErrQueueInvalid):
		code, message = "invalid_input", "请检查专题、书签和预览范围"
	case errors.Is(err, services.ErrClipTopicFull), errors.Is(err, services.ErrQueueFull):
		code, message = "limit_exceeded", "数量超过上限，请分批处理"
	case errors.Is(err, services.ErrClipPreviewExpired):
		code, message = "clip_preview_expired", "预览或来源已变化，请重新预览"
	case errors.Is(err, services.ErrClipPlayerUnsupported):
		code, message = "clip_player_unsupported", "区间预览需要明确选择应用内播放器"
	case errors.Is(err, services.ErrClipBatchOrder):
		code, message = "clip_batch_order", "预览批次不能局部重排，请回专题调整后重新加入"
	case errors.Is(err, services.ErrBookmarkSourceChanged), errors.Is(err, services.ErrQueueSourceChanged):
		code, message = "source_changed", "原片已变化，请重新核对时间位置"
	case errors.Is(err, services.ErrBookmarkSourceUnavailable), errors.Is(err, services.ErrQueueMediaUnavailable):
		code, message = "not_found", "原片不可用或已离开当前范围"

	case errors.Is(err, services.ErrLibraryQueryRequestUsed):
		code, message = "query_request_used", "这次解析已处理，请重新发起"
	case errors.Is(err, services.ErrLibraryQueryInvalid):
		code, message = "invalid_input", "请补全条件、选择重名实体或明确移除不支持的内容"
	case errors.Is(err, services.ErrLibraryQueryExpired):
		code, message = "query_draft_expired", "查询草稿或实体已变化，请重新解析"
	case errors.Is(err, services.ErrTextAIDisabled):
		code, message = "text_ai_disabled", "此文本用途尚未启用，请在设置中明确开启"
	case errors.Is(err, services.ErrTextAIConfigChanged):
		code, message = "text_ai_config_changed", "服务配置已变化，请重新核对发送范围并解析"
	case errors.Is(err, services.ErrTextAIUnavailable):
		code, message = "dependency_unavailable", "请先配置接口地址和模型"
	case errors.Is(err, services.ErrTextAIFailed):
		code, message = "dependency_unavailable", "文本模型调用失败，请检查服务配置后手动重试"
	case errors.Is(err, services.ErrTextAIOutput):
		code, message = "ai_output_invalid", "模型返回的条件无法校验，请改写查询或手动切换本地解析"

	case errors.Is(err, services.ErrEpisodeSeriesConfirmation):
		code, message = "episode_series_confirmation_required", "作品集包含不同系列，请回到作品集重新核对缺集与搜索词"
	case errors.Is(err, services.ErrEpisodeAlreadyFilled):
		code, message = "episode_already_filled", "这一集已补齐，新下载的文件保留为未关联"
	case errors.Is(err, services.ErrEpisodeContextChanged):
		code, message = "episode_context_changed", "季集资料或来源已变化，请重新核对"
	case errors.Is(err, services.ErrCollectionOrderConflict), errors.Is(err, services.ErrCollectionEpisodeConflict), errors.Is(err, services.ErrBTConflict):
		code, message = "conflict", "资料已变化，请刷新后重新确认"
	case errors.Is(err, services.ErrBTSelection):
		code, message = "invalid_input", "请选择有效的视频文件"
	case errors.Is(err, services.ErrBTResultExpired):
		code, message = "result_expired", "资源清单已过期，请重新搜索"
	case errors.Is(err, services.ErrBTRestorePending):
		code, message = "dependency_unavailable", "下载恢复尚未核实，请稍后重试"
	case errors.Is(err, services.ErrBTMaintenance):
		code, message = "maintenance", "下载协调已暂停，请稍后重试"
	case errors.Is(err, services.ErrRecommendationFeedbackConflict):
		code, message = "conflict", "资料已变化，请刷新后重新确认"
	case errors.Is(err, context.Canceled):
		code, message = "cancelled", "操作已取消"
	case errors.Is(err, context.DeadlineExceeded):
		code, message = "dependency_timeout", "操作超时，请稍后重试"
	case errors.Is(err, gorm.ErrRecordNotFound):
		code, message = "not_found", "目标已不存在或不可访问"
	case strings.HasPrefix(err.Error(), "invalid_input:"):
		code, message = "invalid_input", "输入或操作范围无效，请检查后重试"
	}
	return &services.ExperienceError{Code: code, Message: message}
}

func (a *App) SetRecommendationTagPreference(input services.RecommendationTagPreferenceRequest) services.RecommendationPreferenceResult {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return services.RecommendationPreferenceResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	if a.videoService == nil {
		return services.RecommendationPreferenceResult{Status: "error", Error: &services.ExperienceError{Code: "dependency_unavailable", Message: "视频服务尚未就绪"}}
	}
	row, err := a.videoService.SetRecommendationTagPreference(ctx, input)
	if err != nil {
		return services.RecommendationPreferenceResult{Status: "error", Error: a.experienceError(err)}
	}
	data := &services.RecommendationPreferenceState{}
	if row != nil {
		data.Preference = &services.RecommendationFeedbackItem{ID: row.ID, TargetKind: row.TargetKind, TargetID: row.TargetID, Effect: row.Effect, ExpiresAt: row.ExpiresAt, Revision: row.Revision, Active: true}
	}
	return services.RecommendationPreferenceResult{Status: "ok", Data: data}
}

func (a *App) PreviewTagMergePreferences(sources []uint, target uint) services.TagMergePreviewResult {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return services.TagMergePreviewResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	if a.tagService == nil {
		return services.TagMergePreviewResult{Status: "error", Error: &services.ExperienceError{Code: "dependency_unavailable", Message: "标签服务尚未就绪"}}
	}
	data, err := a.tagService.PreviewTagMergePreferences(ctx, sources, target)
	if err != nil {
		return services.TagMergePreviewResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.TagMergePreviewResult{Status: "ok", Data: data}
}

func (a *App) MergeTagsFromPreferencePreview(token string) services.ReviewedTagMergeResult {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return services.ReviewedTagMergeResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	if a.tagService == nil {
		return services.ReviewedTagMergeResult{Status: "error", Error: &services.ExperienceError{Code: "dependency_unavailable", Message: "标签服务尚未就绪"}}
	}
	data, err := a.tagService.MergeTagsFromPreferencePreview(ctx, token)
	if err != nil {
		return services.ReviewedTagMergeResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ReviewedTagMergeResult{Status: "ok", Data: data}
}
