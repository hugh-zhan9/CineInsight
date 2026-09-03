package main

import (
	"context"
	"log"
	"video-master/models"
	"video-master/services"
	"video-master/services/subtitleparser"
)

// ===== Subtitle Methods =====

// GetSubtitleEngineStatuses 获取字幕引擎可用性状态
func (a *App) GetSubtitleEngineStatuses() ([]services.SubtitleEngineStatus, error) {
	return a.subtitleService.GetEngineStatuses()
}

// PrepareSubtitleEngine 准备指定字幕引擎所需依赖
func (a *App) PrepareSubtitleEngine(engine services.SubtitleEngine) error {
	return a.subtitleService.PrepareEngine(engine)
}

// CheckSubtitleDependencies 检查字幕生成依赖
func (a *App) CheckSubtitleDependencies() (map[string]bool, error) {
	return a.subtitleService.CheckDependencies()
}

// DownloadSubtitleDependencies 下载字幕生成依赖
func (a *App) DownloadSubtitleDependencies() error {
	return a.subtitleService.DownloadDependencies()
}

// GenerateSubtitle 生成字幕
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

// ForceGenerateSubtitle 强制生成字幕（跳过幻觉检测）
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

func subtitleGenerateOptionsFromSettings(settings *models.Settings, force bool) services.SubtitleGenerateOptions {
	options := services.SubtitleGenerateOptions{
		BilingualLang: "zh",
		ForceGenerate: force,
		RecognitionConfig: services.SubtitleRecognitionConfig{
			WhisperXModel:       "medium",
			WhisperXBatchSize:   8,
			WhisperXComputeType: "int8",
		},
		TranslationConfig: services.SubtitleTranslationConfig{Provider: "deepl"},
	}
	if settings == nil {
		return options
	}
	options.BilingualEnabled = settings.BilingualEnabled
	options.BilingualLang = settings.BilingualLang
	options.TranslationConfig = services.SubtitleTranslationConfig{
		Provider:    settings.SubtitleTranslationProvider,
		DeepLAPIKey: settings.DeepLApiKey,
		BaseURL:     settings.SubtitleTranslationBaseURL,
		APIKey:      settings.SubtitleTranslationAPIKey,
		Model:       settings.SubtitleTranslationModel,
	}
	options.RecognitionConfig.WhisperXModel = settings.SubtitleWhisperXModel
	options.RecognitionConfig.WhisperXBatchSize = settings.SubtitleWhisperXBatchSize
	return options
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
	return document, err
}

func subtitleEditEntryCount(document *services.SubtitleEditDocument) int {
	if document == nil {
		return 0
	}
	return len(document.Entries)
}

// ValidateSubtitleEditDocument validates an in-memory edit without touching the source SRT.
func (a *App) ValidateSubtitleEditDocument(request services.SubtitleSaveRequest) services.SubtitleValidationResult {
	return a.subtitleWorkbench.Validate(request.Entries)
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

// ===== Translation Glossary Methods =====
//
// 术语表是无状态服务（只读写库），所以不进 App 结构体，每次现取一个。

// ListGlossaryEntries 列出一个作用域的术语条目；collectionID 传 0 表示全局表。
func (a *App) ListGlossaryEntries(collectionID uint) ([]models.TranslationGlossaryEntry, error) {
	entries, err := services.NewTranslationGlossaryService().List(glossaryScopeArgument(collectionID))
	log.Printf("API ListGlossaryEntries collection_id=%d entries=%d err=%v", collectionID, len(entries), err)
	return entries, err
}

// UpsertGlossaryEntry 新增或改写一条术语；entry.collection_id 为空表示全局条目。
func (a *App) UpsertGlossaryEntry(entry models.TranslationGlossaryEntry) (*models.TranslationGlossaryEntry, error) {
	saved, err := services.NewTranslationGlossaryService().Upsert(entry)
	log.Printf("API UpsertGlossaryEntry id=%d scope_key=%d err=%v", entry.ID, entry.ScopeKey, err)
	return saved, err
}

// DeleteGlossaryEntry 删除一条术语。
func (a *App) DeleteGlossaryEntry(id uint) error {
	err := services.NewTranslationGlossaryService().Delete(id)
	log.Printf("API DeleteGlossaryEntry id=%d err=%v", id, err)
	return err
}

// glossaryScopeArgument 把前端惯用的 0 表示全局翻译成服务层的 nil 作用域。
func glossaryScopeArgument(collectionID uint) *uint {
	if collectionID == 0 {
		return nil
	}
	return &collectionID
}
