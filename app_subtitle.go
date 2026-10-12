package main

import (
	"context"
	"errors"
	"log"
	"video-master/models"
	"video-master/services"
	"video-master/services/subtitleparser"

	"gorm.io/gorm"
)

// ===== Subtitle Methods =====

// GetSubtitleEngineStatuses 获取字幕引擎可用性状态（60 秒缓存，准备前后失效，D-PC22）
func (a *App) GetSubtitleEngineStatuses() ([]services.SubtitleEngineStatus, error) {
	return a.subtitleService.GetEngineStatuses()
}

// PrepareSubtitleEngine 准备指定字幕引擎所需依赖。被 CancelSubtitleEnginePreparation 取消时
// 返回「已取消字幕引擎准备」；结果另经桌面通知告知。
func (a *App) PrepareSubtitleEngine(engine services.SubtitleEngine) error {
	err := a.subtitleService.PrepareEngine(engine)
	log.Printf("API PrepareSubtitleEngine engine=%s err=%v", engine, err)
	return err
}

// CancelSubtitleEnginePreparation 取消正在进行的字幕引擎准备（取消系统辅助程序，D-PC22）。
func (a *App) CancelSubtitleEnginePreparation() {
	cancelled := a.subtitleService.CancelEnginePreparation()
	log.Printf("API CancelSubtitleEnginePreparation cancelled=%v", cancelled)
}

// GenerateSubtitle 生成字幕。收尾写回失败时返回 error_code=subtitle_replace_failed、
// pending_retained=true 的结果（映射在服务层完成），ForceGenerateSubtitle 可复用临时文件重试收尾。
func (a *App) GenerateSubtitle(req services.SubtitleGenerateRequest) (*services.SubtitleGenerateResult, error) {
	video, err := a.videoService.GetVideo(req.VideoID)
	if err != nil {
		log.Printf("API GenerateSubtitle id=%d failed to get video: %v", req.VideoID, err)
		return nil, err
	}
	settings, _ := a.settingsService.GetSettings()
	options := subtitleGenerateOptionsFromSettings(settings, false)
	req.VideoName = video.Name
	log.Printf("API GenerateSubtitle id=%d path=%s engine=%s bilingual=%v lang=%s source=%s provider=%s", req.VideoID, video.Path, req.Engine, options.BilingualEnabled, options.BilingualLang, req.SourceLang, options.TranslationConfig.Provider)
	return a.subtitleService.GenerateSubtitle(req, video.Path, options)
}

// ForceGenerateSubtitle 强制生成字幕（跳过幻觉检测）；有待确认的临时字幕时复用它，不重跑识别。
// 写回失败的映射与 GenerateSubtitle 相同。
func (a *App) ForceGenerateSubtitle(req services.SubtitleGenerateRequest) (*services.SubtitleGenerateResult, error) {
	video, err := a.videoService.GetVideo(req.VideoID)
	if err != nil {
		return nil, err
	}
	settings, _ := a.settingsService.GetSettings()
	options := subtitleGenerateOptionsFromSettings(settings, true)
	req.VideoName = video.Name
	log.Printf("API ForceGenerateSubtitle id=%d path=%s engine=%s source=%s", req.VideoID, video.Path, req.Engine, req.SourceLang)
	return a.subtitleService.GenerateSubtitle(req, video.Path, options)
}

// CancelSubtitle 取消正在进行的字幕生成任务
func (a *App) CancelSubtitle() {
	a.subtitleService.CancelGeneration()
	log.Printf("API CancelSubtitle")
}

// CancelSubtitleTask 取消指定的字幕任务。
func (a *App) CancelSubtitleTask(taskID uint) error {
	err := a.subtitleService.CancelSubtitleTask(taskID)
	log.Printf("API CancelSubtitleTask task_id=%d err=%v", taskID, err)
	return err
}

// GetSubtitleQueueState 返回当前字幕任务队列。
func (a *App) GetSubtitleQueueState() services.SubtitleQueueSnapshot {
	return a.subtitleService.GetSubtitleQueueState()
}

// ===== 字幕任务中心（D-PC20） =====

// ResolveSubtitleJob 处理一条字幕任务：action 取 force（复用临时字幕强制生成 / 重试收尾）、
// discard（放弃临时字幕）或 retry（重新排队）。force / retry 入队后立即返回。
func (a *App) ResolveSubtitleJob(jobID uint, action string) (*services.SubtitleJobResolveResult, error) {
	result, err := a.resolveSubtitleJob(jobID, services.SubtitleJobAction(action))
	status := services.SubtitleQueueTaskStatus("")
	if result != nil {
		status = result.Status
	}
	log.Printf("API ResolveSubtitleJob job_id=%d action=%s status=%s err=%v", jobID, action, status, err)
	return result, err
}

func (a *App) resolveSubtitleJob(jobID uint, action services.SubtitleJobAction) (*services.SubtitleJobResolveResult, error) {
	input := services.SubtitleJobResolveInput{}
	if action == services.SubtitleJobActionForce || action == services.SubtitleJobActionRetry {
		job, err := a.subtitleService.GetSubtitleJob(jobID)
		if err != nil {
			return nil, err
		}
		video, err := a.videoService.GetVideo(job.VideoID)
		if err != nil {
			return nil, err
		}
		settings, _ := a.settingsService.GetSettings()
		input = services.SubtitleJobResolveInput{
			VideoPath: video.Path,
			VideoName: video.Name,
			Options:   subtitleGenerateOptionsFromSettings(settings, action == services.SubtitleJobActionForce),
		}
	}
	return a.subtitleService.ResolveSubtitleJob(jobID, action, input)
}

// GetInterruptedSubtitleJobs 返回「上次中断 N 个字幕任务」提示所需的任务。
func (a *App) GetInterruptedSubtitleJobs() (services.SubtitleInterruptedJobs, error) {
	return a.subtitleService.GetInterruptedSubtitleJobs()
}

// RequeueInterruptedSubtitleJobs 是提示上的「全部重新排队」：逐个按当前设置重试，返回成功入队的条数。
// 没能入队的任务改为 failed、写明真实原因（例如「视频已删除，无法重新排队」），留在任务中心可以单独
// 处理；不再随「忽略」标成「已忽略」——用户并没有点忽略（m7）。已被别的操作处理的行（重复点击、另一个
// 窗口已重试）状态以那边为准，不动。
func (a *App) RequeueInterruptedSubtitleJobs() (int, error) {
	summary, err := a.subtitleService.GetInterruptedSubtitleJobs()
	if err != nil {
		return 0, err
	}
	requeued := 0
	for _, jobID := range summary.JobIDs {
		result, err := a.resolveSubtitleJob(jobID, services.SubtitleJobActionRetry)
		if err == nil && result != nil && result.ErrorCode == "" {
			requeued++
			continue
		}
		log.Printf("API RequeueInterruptedSubtitleJobs job_id=%d result=%+v err=%v", jobID, result, err)
		if errors.Is(err, services.ErrSubtitleJobNotFound) || (result != nil && result.ErrorCode == services.SubtitleErrorJobConflict) {
			continue
		}
		if _, markErr := a.subtitleService.FailInterruptedSubtitleJob(jobID, interruptedRequeueFailureReason(result, err)); markErr != nil {
			// 这一行仍是 interrupted：下次提示里还在，不会被静默丢掉。
			log.Printf("API RequeueInterruptedSubtitleJobs mark failed job_id=%d err=%v", jobID, markErr)
		}
	}
	log.Printf("API RequeueInterruptedSubtitleJobs requeued=%d total=%d", requeued, len(summary.JobIDs))
	return requeued, nil
}

// interruptedRequeueFailureReason 是「全部重新排队」没能入队时写进任务的原因。视频已删除（进了回收站）
// 是最常见的一种，单独给一句说得清的话；其余沿用重试返回的说明（服务层落库前擦掉路径）。
func interruptedRequeueFailureReason(result *services.SubtitleJobResolveResult, err error) string {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "视频已删除，无法重新排队"
	case err != nil:
		return "重新排队失败：" + err.Error()
	case result != nil && result.Message != "":
		return "重新排队失败：" + result.Message
	default:
		return "重新排队失败"
	}
}

// DismissInterruptedSubtitleJobs 是提示上的「忽略」：中断任务改为已取消，不再提示，仍留在历史里可单独重试。
func (a *App) DismissInterruptedSubtitleJobs() error {
	return a.subtitleService.DismissInterruptedSubtitleJobs()
}

// ===== 字幕索引同步（D-PC23） =====

// GetSubtitleIndexSyncStatus 返回全库字幕索引同步的状态（「上次同步时间」）。
func (a *App) GetSubtitleIndexSyncStatus() services.SubtitleIndexSyncStatus {
	return a.subtitleSearchService.GetSubtitleIndexSyncStatus()
}

// SyncSubtitleIndexNow 是「立即同步」：后台开始一轮全库同步并立即返回，完成后发 subtitle-index-synced。
func (a *App) SyncSubtitleIndexNow() (services.SubtitleIndexSyncStatus, error) {
	status, err := a.subtitleSearchService.SyncSubtitleIndexNow()
	log.Printf("API SyncSubtitleIndexNow running=%v err=%v", status.Running, err)
	return status, err
}

func subtitleGenerateOptionsFromSettings(settings *models.Settings, force bool) services.SubtitleGenerateOptions {
	options := services.SubtitleGenerateOptions{BilingualLang: "zh", ForceGenerate: force, TranslationConfig: services.SubtitleTranslationConfig{Provider: "apple"}}
	if settings != nil {
		options.BilingualEnabled = settings.BilingualEnabled
		options.BilingualLang = settings.BilingualLang
	}
	return options
}

func (a *App) TranslateSubtitle(req services.SubtitleTranslateRequest) (*services.SubtitleTranslateResult, error) {
	video, err := a.videoService.GetVideo(req.VideoID)
	if err != nil {
		log.Printf("API TranslateSubtitle id=%d failed to get video: %v", req.VideoID, err)
		return nil, err
	}
	settings, err := a.settingsService.GetSettings()
	if err != nil {
		return nil, err
	}
	config := subtitleGenerateOptionsFromSettings(settings, false).TranslationConfig
	result, err := a.subtitleService.TranslateSubtitleFile(a.backgroundContext(), video.Path, req, config)
	log.Printf("API TranslateSubtitle id=%d target=%s mode=%s provider=%s err=%v", req.VideoID, req.TargetLang, req.Mode, config.Provider, err)
	// 带错误码的失败（字幕缺失、非 UTF-8、非同名 .srt）放进返回结构的 error_code，让前端分流（G-3）。
	if coded := asSubtitleCodedError(err); coded != nil {
		return &services.SubtitleTranslateResult{
			VideoID: req.VideoID, Mode: req.Mode, TargetLang: req.TargetLang,
			ErrorCode: coded.Code, Message: coded.Message, DetectedEncoding: coded.DetectedEncoding, Candidates: coded.Candidates,
		}, nil
	}
	return result, err
}

// CancelSubtitleTranslation 取消某个视频正在跑的字幕翻译。
func (a *App) CancelSubtitleTranslation(videoID uint) {
	a.subtitleService.CancelSubtitleTranslation(videoID)
	log.Printf("API CancelSubtitleTranslation id=%d", videoID)
}

// GetSubtitleSegments 获取已生成字幕的结构化片段
func (a *App) GetSubtitleSegments(videoID uint) ([]subtitleparser.Segment, error) {
	video, err := a.videoService.GetVideo(videoID)
	if err != nil {
		return nil, err
	}

	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	segments, err := subtitleparser.ParseFile(srtPath)
	if err != nil {
		log.Printf("API GetSubtitleSegments id=%d path=%s err=%v", videoID, srtPath, err)
		return nil, err
	}

	log.Printf("API GetSubtitleSegments id=%d path=%s segments=%d", videoID, srtPath, len(segments))
	return segments, nil
}

// GetSubtitleEditDocument loads the external SRT through the strict editor parser.
func (a *App) GetSubtitleEditDocument(videoID uint) (*services.SubtitleEditDocument, error) {
	video, err := a.videoService.GetVideo(videoID)
	if err != nil {
		return nil, err
	}
	document, err := a.subtitleWorkbench.GetDocument(*video)
	log.Printf("API GetSubtitleEditDocument id=%d entries=%d err=%v", videoID, subtitleEditEntryCount(document), err)
	// subtitle_missing / subtitle_not_sidecar_srt / subtitle_encoding_not_utf8 以 error_code 返回，
	// 前端据此显示「新建空白字幕」或「转换为 UTF-8」（D-PC14、D-PC15、D-PC17）。
	if coded := asSubtitleCodedError(err); coded != nil {
		return &services.SubtitleEditDocument{
			VideoID: videoID, Entries: []subtitleparser.EditorSegment{}, Issues: []subtitleparser.DocumentIssue{},
			ErrorCode: coded.Code, Message: coded.Message, DetectedEncoding: coded.DetectedEncoding, Candidates: coded.Candidates,
		}, nil
	}
	return document, err
}

// CreateBlankSubtitleDocument 以空文档打开还没有字幕的视频，保存时才创建 .srt（D-PC15）。
func (a *App) CreateBlankSubtitleDocument(videoID uint) (*services.SubtitleEditDocument, error) {
	video, err := a.videoService.GetVideo(videoID)
	if err != nil {
		return nil, err
	}
	document, err := a.subtitleWorkbench.NewBlankDocument(*video)
	log.Printf("API CreateBlankSubtitleDocument id=%d err=%v", videoID, err)
	return document, err
}

// ConvertSubtitleToUTF8 把 GBK/Big5/UTF-16 字幕转成 UTF-8，写回前先备份（D-PC14）。
func (a *App) ConvertSubtitleToUTF8(videoID uint, fromEncoding string) (*services.SubtitleConvertResult, error) {
	video, err := a.videoService.GetVideo(videoID)
	if err != nil {
		return nil, err
	}
	result, err := a.subtitleWorkbench.ConvertToUTF8(*video, fromEncoding)
	log.Printf("API ConvertSubtitleToUTF8 id=%d from=%s err=%v", videoID, fromEncoding, err)
	// subtitle_encoding_ambiguous 带候选回给前端，让用户看预览选定编码后再转（MEDIA-02）；
	// 其余带码失败（字幕缺失、编码无法识别）同样放进 error_code（G-3）。
	if coded := asSubtitleCodedError(err); coded != nil {
		return &services.SubtitleConvertResult{
			ErrorCode: coded.Code, Message: coded.Message, DetectedEncoding: coded.DetectedEncoding, Candidates: coded.Candidates,
		}, nil
	}
	return result, err
}

// GetSubtitleOverwriteInfo 报告生成字幕是否会覆盖现有 .srt，以及哪些视频共用它（D-PC13）。
func (a *App) GetSubtitleOverwriteInfo(videoID uint) (*services.SubtitleOverwriteInfo, error) {
	video, err := a.videoService.GetVideo(videoID)
	if err != nil {
		return nil, err
	}
	return a.subtitleService.GetSubtitleOverwriteInfo(*video)
}

// ListSubtitleBackups 列出视频的字幕备份（最新 5 份）。
func (a *App) ListSubtitleBackups(videoID uint) ([]services.SubtitleBackup, error) {
	video, err := a.videoService.GetVideo(videoID)
	if err != nil {
		return nil, err
	}
	return a.subtitleService.ListSubtitleBackups(*video)
}

// RestoreSubtitleBackup 恢复某份字幕备份；恢复前会先备份当前字幕。
func (a *App) RestoreSubtitleBackup(videoID uint, backupID string) (*services.SubtitleRestoreResult, error) {
	video, err := a.videoService.GetVideo(videoID)
	if err != nil {
		return nil, err
	}
	result, err := a.subtitleService.RestoreSubtitleBackup(*video, backupID)
	log.Printf("API RestoreSubtitleBackup id=%d backup=%s err=%v", videoID, backupID, err)
	return result, err
}

// DiscardPendingSubtitle 放弃校验未通过的临时字幕（用户选择不「强制生成」时调用）。
func (a *App) DiscardPendingSubtitle(videoID uint) error {
	err := a.subtitleService.DiscardPendingSubtitle(videoID)
	log.Printf("API DiscardPendingSubtitle id=%d err=%v", videoID, err)
	return err
}

func asSubtitleCodedError(err error) *services.SubtitleCodedError {
	var coded *services.SubtitleCodedError
	if errors.As(err, &coded) {
		return coded
	}
	return nil
}

func subtitleEditEntryCount(document *services.SubtitleEditDocument) int {
	if document == nil {
		return 0
	}
	return len(document.Entries)
}

// RetranslateSubtitleEntries translates a selection without persisting it.
func (a *App) RetranslateSubtitleEntries(request services.SubtitleRetranslateRequest) (*services.SubtitleRetranslateResult, error) {
	settings, err := a.settingsService.GetSettings()
	if err != nil {
		return nil, err
	}
	config := subtitleGenerateOptionsFromSettings(settings, false).TranslationConfig
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := a.subtitleWorkbench.Retranslate(ctx, request, config)
	log.Printf("API RetranslateSubtitleEntries id=%d entries=%d err=%v", request.VideoID, len(request.Entries), err)
	return result, err
}

// SaveSubtitleEditDocument atomically replaces the current SRT if its fingerprint is unchanged.
func (a *App) SaveSubtitleEditDocument(request services.SubtitleSaveRequest) (*services.SubtitleSaveResult, error) {
	video, err := a.videoService.GetVideo(request.VideoID)
	if err != nil {
		return nil, err
	}
	result, err := a.subtitleWorkbench.SaveDocument(*video, request)
	status := services.SubtitleSaveStatus("")
	if result != nil {
		status = result.Status
	}
	log.Printf("API SaveSubtitleEditDocument id=%d entries=%d status=%s err=%v", request.VideoID, len(request.Entries), status, err)
	return result, err
}

// PrepareAppleSubtitleLanguage prepares only Apple-managed speech or translation resources.
func (a *App) PrepareAppleSubtitleLanguage(source, target string, translation bool) error {
	return a.subtitleService.PrepareAppleLanguage(source, target, translation)
}

// DetectSubtitleLanguage offers an editable language suggestion before generation.
func (a *App) DetectSubtitleLanguage(videoID uint) (services.AppleLanguageDetection, error) {
	video, err := a.videoService.GetVideo(videoID)
	if err != nil {
		return services.AppleLanguageDetection{}, err
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.subtitleService.DetectSubtitleLanguage(ctx, video.Path)
}
