package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const aiTaggingWorkerInterval = 5 * time.Minute

// aiTaggingSkipReasonManualRetry 落在 pending 状态行的 skip_reason 上，表示这是用户显式
// 要求的重新分析（D-PC28 规则 5）：worker 对它跳过「已有人工标签」检查。
// 状态行本来就有 skip_reason 列，借它持久化这个意图，不必加列，重启后也不丢。
// setProcessing 会把 skip_reason 清空，所以一次显式重试只生效一轮。
const aiTaggingSkipReasonManualRetry = "manual_retry"

type AITaggingService struct {
	configProvider AITaggingConfigProvider
	clientFactory  func(AITaggingConfig) AITaggingAIClient
	extractor      *AITaggingExtractor
	transcript     TemporaryTranscriptProvider
	sameSource     AISameSourceProvider
	now            func() time.Time
	workerMu       sync.Mutex
	workerRunMu    sync.Mutex
	workerCancel   context.CancelFunc
	workerWake     chan struct{}
	registry       *BackgroundTaskRegistry
	idleGate       *IdleGate
	// gatedMu 保护 gatedActive / gatedCancel：worker 循环里"过门等待中的自动轮次"。
	gatedMu     sync.Mutex
	gatedActive bool
	gatedCancel context.CancelFunc
}

// SetIdleGate 接入空闲门（D-PC19）：worker 的启动批次与定时轮次经 IdleGate.Run，
// 显式唤醒通道仍然直通。传 nil 等价于不过门。App 启动时调用属于 P-029 接线项。
func (s *AITaggingService) SetIdleGate(gate *IdleGate) {
	if s == nil {
		return
	}
	s.workerMu.Lock()
	s.idleGate = gate
	s.workerMu.Unlock()
}

// SetBackgroundTaskRegistry 接入后台任务登记表（D-014）。
//
// 这里不装项间检查点：worker 循环同时服务自动唤醒与用户显式的
// TriggerAITagging / RetryVideo，钩子装上去会把显式任务也挡住（D-030 明令禁止）。
// 自动唤醒改在 app 层过门。
func (s *AITaggingService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	if s == nil {
		return
	}
	s.workerMu.Lock()
	s.registry = registry
	s.workerMu.Unlock()
}

func NewAITaggingService() *AITaggingService {
	extractor := NewAITaggingExtractor()
	return &AITaggingService{
		configProvider: SettingsAITaggingConfigProvider{},
		clientFactory:  NewOpenAICompatibleAITaggingClient,
		extractor:      extractor,
		sameSource:     NewAISameSourceService(extractor),
		now:            time.Now,
	}
}

func (s *AITaggingService) SetTemporaryTranscriptProvider(provider TemporaryTranscriptProvider) {
	s.transcript = provider
}

func (s *AITaggingService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if s.workerCancel != nil {
		return
	}
	if err := s.recoverInterruptedRuns(); err != nil {
		log.Printf("[AITagging] recover interrupted runs failed err=%v", err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.workerCancel = cancel
	s.workerWake = make(chan struct{}, 1)
	go s.workerLoop(workerCtx, s.workerWake)
}

func (s *AITaggingService) Stop() {
	if s == nil {
		return
	}
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if s.workerCancel != nil {
		s.workerCancel()
		s.workerCancel = nil
	}
}

func (s *AITaggingService) StopAndWait() {
	if s == nil {
		return
	}
	s.Stop()
	s.workerRunMu.Lock()
	s.workerRunMu.Unlock()
}

func (s *AITaggingService) Trigger() bool {
	if s == nil {
		return false
	}
	s.workerMu.Lock()
	wake := s.workerWake
	running := s.workerCancel != nil
	s.workerMu.Unlock()
	if !running || wake == nil {
		return false
	}
	select {
	case wake <- struct{}{}:
	default:
	}
	return true
}

func (s *AITaggingService) workerLoop(ctx context.Context, wake <-chan struct{}) {
	s.runWorkerOnceGated(ctx)
	ticker := time.NewTicker(aiTaggingWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			// 显式唤醒直通：先摘掉还在门口排队的自动轮次，再直接执行。
			s.cancelGatedWait()
			s.runWorkerOnce(ctx)
		case <-ticker.C:
			s.runWorkerOnceGated(ctx)
		}
	}
}

// runWorkerOnceGated 是自动轮次（启动批次与定时轮次）的入口。
//
// 没接空闲门时同步直通。接了门时把"过门等待"放到独立 goroutine：门口的等待可能持续
// 数小时，若卡在 workerLoop 里，显式唤醒通道就没人消费了。空闲调度关闭时
// IdleGate.Run 立刻放行，行为与直通一致。同一时刻最多一个等待中的自动轮次，
// 后到的合并进去（与 IdleGate 对同一 taskKey 的合并语义一致）。
func (s *AITaggingService) runWorkerOnceGated(ctx context.Context) {
	s.workerMu.Lock()
	gate := s.idleGate
	s.workerMu.Unlock()
	if gate == nil {
		s.runWorkerOnce(ctx)
		return
	}
	s.gatedMu.Lock()
	if s.gatedActive {
		s.gatedMu.Unlock()
		return
	}
	waitCtx, cancel := context.WithCancel(ctx)
	s.gatedActive = true
	s.gatedCancel = cancel
	s.gatedMu.Unlock()
	go func() {
		defer func() {
			cancel()
			s.gatedMu.Lock()
			s.gatedActive = false
			s.gatedCancel = nil
			s.gatedMu.Unlock()
		}()
		// waitCtx 只管"等门"；放行后的执行用 worker 自己的 ctx，
		// 显式唤醒取消等待时不会误伤已经放行、正在处理的一轮。
		err := gate.Run(waitCtx, string(BackgroundTaskAITagging), func(context.Context) error {
			s.runWorkerOnce(ctx)
			return nil
		})
		// 已有自动唤醒在排队是预期的合并，不是中断，不记日志。
		if err != nil && !errors.Is(err, ErrIdleGateTaskAlreadyWaiting) && ctx.Err() == nil && waitCtx.Err() == nil {
			log.Printf("[AITagging] gated round aborted err=%v", err)
		}
	}()
}

// cancelGatedWait 摘掉门口排队的自动轮次。只取消"等待"，已放行执行中的一轮不受影响。
func (s *AITaggingService) cancelGatedWait() {
	s.gatedMu.Lock()
	cancel := s.gatedCancel
	s.gatedMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *AITaggingService) runWorkerOnce(ctx context.Context) {
	s.workerRunMu.Lock()
	defer s.workerRunMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	config, err := s.configProvider.Load()
	if err != nil {
		log.Printf("[AITagging] config unavailable; background worker idle err=%v", err)
		return
	}
	log.Printf("[AITagging] worker config model=%q images_per_request=%d subtitle_char_limit=%d startup_batch_size=%d",
		config.Model,
		config.ImagesPerRequest,
		config.SubtitleCharLimit,
		config.StartupBatchSize,
	)
	batchSize := config.StartupBatchSize
	if batchSize <= 0 {
		batchSize = defaultAITaggingStartupBatchSize
	}
	videos, err := s.findUntaggedVideos(batchSize)
	if err != nil {
		log.Printf("[AITagging] find untagged videos failed: %v", err)
		return
	}
	if len(videos) == 0 {
		return
	}
	s.workerMu.Lock()
	registry := s.registry
	s.workerMu.Unlock()
	registry.Begin(BackgroundTaskAITagging)
	defer registry.End(BackgroundTaskAITagging)
	for _, video := range videos {
		if ctx.Err() != nil {
			return
		}
		if err := s.processVideoWithConfig(ctx, video, config); err != nil {
			log.Printf("[AITagging] process video id=%d failed: %v", video.ID, err)
		}
	}
}

func (s *AITaggingService) ProcessVideo(ctx context.Context, videoID uint) error {
	var video models.Video
	if err := database.DB.Preload("Tags").First(&video, videoID).Error; err != nil {
		return err
	}
	config, err := s.configProvider.Load()
	if err != nil {
		return s.markState(video.ID, models.AITaggingStateStatusSkipped, "config_unavailable", "", err.Error())
	}
	return s.processVideoWithConfig(ctx, video, config)
}

func (s *AITaggingService) processVideoWithConfig(ctx context.Context, video models.Video, config AITaggingConfig) (returnErr error) {
	log.Printf("[AITagging] start video_id=%d tags=%d config={model:%q images_per_request:%d subtitle_char_limit:%d}",
		video.ID,
		len(video.Tags),
		config.Model,
		config.ImagesPerRequest,
		config.SubtitleCharLimit,
	)
	// 显式重试标记在 setProcessing 之后就没了，所以入口处读一次：既用于放行人工标签检查，
	// 也用于词表变化自动重排时把用户的显式请求带下去。
	explicit, err := s.isExplicitRetry(video.ID)
	if err != nil {
		return err
	}
	if hasNonAutomaticTags(video.Tags) {
		if !explicit {
			log.Printf("[AITagging] skip already tagged video_id=%d", video.ID)
			return s.markState(video.ID, models.AITaggingStateStatusSkipped, "already_tagged", "", "")
		}
	}
	existingTags, err := s.loadActiveTags()
	if err != nil {
		return err
	}
	if len(existingTags) == 0 {
		log.Printf("[AITagging] skip empty tag library video_id=%d", video.ID)
		return s.markState(video.ID, models.AITaggingStateStatusSkipped, "empty_tag_library", "", "")
	}
	evidence := s.extractor.Collect(ctx, video, config)
	log.Printf("[AITagging] evidence video_id=%d subtitle_len=%d frames=%d warning_count=%d",
		video.ID,
		len([]rune(evidence.SubtitleText)),
		len(evidence.Frames),
		len(evidence.Warnings),
	)
	fingerprint := buildEvidenceFingerprint(video, existingTags, evidence)
	if skip, err := s.shouldSkipForCurrentFingerprint(video.ID, fingerprint); err != nil {
		return err
	} else if skip {
		return nil
	}
	run, err := s.createRun(video.ID, config)
	if err != nil {
		return err
	}
	client := s.clientFactory(config)
	runStatus := models.AITaggingStateStatusFailed
	failureCode := "processing_failed"
	defer func() {
		if err := s.finishRun(run, runStatus, failureCode, client); err != nil {
			if returnErr == nil {
				returnErr = err
			} else {
				log.Printf("[AITagging] finish run failed run_id=%d err=%v", run.ID, err)
			}
		}
	}()
	ctx = withAIRunAttribution(ctx, run)
	if err := s.setProcessing(video.ID, fingerprint); err != nil {
		return err
	}
	evidence, err = s.runAgentEvidenceLoop(ctx, video, existingTags, evidence, fingerprint, config, client)
	if err != nil {
		failureCode = "agent_evidence_failed"
		log.Printf("[AITagging] agent evidence loop failed video_id=%d err=%v", video.ID, err)
		return s.markState(video.ID, models.AITaggingStateStatusFailed, "", fingerprint, err.Error())
	}
	suggestions, err := client.AnalyzeTags(ctx, AITaggingRequest{
		Video:        video,
		ExistingTags: existingTags,
		Evidence:     evidence,
	})
	if err != nil {
		failureCode = "analysis_failed"
		log.Printf("[AITagging] analyze failed video_id=%d err=%v", video.ID, err)
		return s.markState(video.ID, models.AITaggingStateStatusFailed, "", fingerprint, err.Error())
	}
	latestTags, err := s.loadActiveTags()
	if err != nil {
		failureCode = "tag_library_reload_failed"
		return s.markState(video.ID, models.AITaggingStateStatusFailed, "", fingerprint, err.Error())
	}
	if tagLibraryHash(latestTags) != tagLibraryHash(existingTags) {
		runStatus = models.AITaggingStateStatusSkipped
		failureCode = "tag_library_changed"
		log.Printf("[AITagging] tag library changed during analysis; retry scheduled video_id=%d", video.ID)
		// 自动重排：本轮是用户显式重试时保留 manual_retry，不抹掉用户的请求；否则不带
		// 标记，人工标签检查照旧。
		requeueReason := ""
		if explicit {
			requeueReason = aiTaggingSkipReasonManualRetry
		}
		return s.requeueVideo(video.ID, requeueReason)
	}
	log.Printf("[AITagging] analyze succeeded video_id=%d suggestions=%d", video.ID, len(suggestions))
	runID := run.ID
	created, err := s.persistSuggestions(video, existingTags, evidence, suggestions, &runID)
	if err != nil {
		failureCode = "suggestion_persist_failed"
		log.Printf("[AITagging] persist failed video_id=%d err=%v", video.ID, err)
		return s.markState(video.ID, models.AITaggingStateStatusFailed, "", fingerprint, err.Error())
	}
	if created == 0 {
		runStatus = models.AITaggingStateStatusSkipped
		failureCode = "no_high_or_medium_confidence"
		log.Printf("[AITagging] skipped no high/medium confidence video_id=%d", video.ID)
		return s.markState(video.ID, models.AITaggingStateStatusSkipped, "no_high_or_medium_confidence", fingerprint, "")
	}
	runStatus = models.AITaggingStateStatusCompleted
	failureCode = ""
	log.Printf("[AITagging] completed video_id=%d created=%d", video.ID, created)
	return s.markState(video.ID, models.AITaggingStateStatusCompleted, "", fingerprint, "")
}

// isExplicitRetry 判断这个视频当前是否处于用户显式要求的重新分析中。
func (s *AITaggingService) isExplicitRetry(videoID uint) (bool, error) {
	var count int64
	if err := database.DB.Model(&models.AITaggingState{}).
		Where("video_id = ? AND status = ? AND skip_reason = ?", videoID, models.AITaggingStateStatusPending, aiTaggingSkipReasonManualRetry).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func hasNonAutomaticTags(tags []models.Tag) bool {
	for _, tag := range tags {
		if tag.AutomaticKind == "" {
			return true
		}
	}
	return false
}

func (s *AITaggingService) findUntaggedVideos(limit int) ([]models.Video, error) {
	var videos []models.Video
	err := database.DB.Model(&models.Video{}).
		Preload("Tags").
		Where("is_stale = ?", false).
		// 规则 1：自动路径只打没有人工标签的视频；显式重新分析（规则 5）的视频例外。
		Where(`(NOT EXISTS (
			SELECT 1 FROM video_tags
			INNER JOIN tags ON tags.id = video_tags.tag_id
			WHERE video_tags.video_id = videos.id
				AND COALESCE(tags.automatic_kind, '') = ''
		) OR EXISTS (
			SELECT 1 FROM ai_tagging_states
			WHERE ai_tagging_states.video_id = videos.id
				AND ai_tagging_states.status = ?
				AND ai_tagging_states.skip_reason = ?
		))`, models.AITaggingStateStatusPending, aiTaggingSkipReasonManualRetry).
		Where("NOT EXISTS (SELECT 1 FROM ai_tag_candidates WHERE ai_tag_candidates.video_id = videos.id AND ai_tag_candidates.status = ?)", models.AITagCandidateStatusPending).
		Where(`NOT EXISTS (
			SELECT 1 FROM ai_tagging_states
			WHERE ai_tagging_states.video_id = videos.id
				AND ai_tagging_states.status IN ?
		)`, []string{
			models.AITaggingStateStatusProcessing,
			models.AITaggingStateStatusCompleted,
			models.AITaggingStateStatusSkipped,
		}).
		Order("id").
		Limit(limit).
		Find(&videos).Error
	return videos, err
}

func (s *AITaggingService) loadActiveTags() ([]models.Tag, error) {
	var tags []models.Tag
	if err := database.DB.
		Where("COALESCE(automatic_kind, '') = ''").
		// 规则 6：「人物」分类的人名不进词表，避免把人名交给视觉模型。
		Where("TRIM(COALESCE(namespace, '')) <> ?", personTagNamespace).
		Order("namespace asc, sort_order asc, id asc").
		Find(&tags).Error; err != nil {
		return nil, err
	}
	return tags, nil
}

func (s *AITaggingService) shouldSkipForCurrentFingerprint(videoID uint, fingerprint string) (bool, error) {
	var state models.AITaggingState
	err := database.DB.Where("video_id = ?", videoID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state.EvidenceFingerprint != fingerprint {
		if err := database.DB.Model(&models.AITagCandidate{}).
			Where("video_id = ? AND status = ?", videoID, models.AITagCandidateStatusPending).
			Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
			return false, err
		}
		return false, nil
	}
	if state.Status == models.AITaggingStateStatusPending || state.Status == models.AITaggingStateStatusFailed {
		return false, nil
	}
	var pendingCount int64
	if err := database.DB.Model(&models.AITagCandidate{}).
		Where("video_id = ? AND status = ?", videoID, models.AITagCandidateStatusPending).
		Count(&pendingCount).Error; err != nil {
		return false, err
	}
	return pendingCount > 0 || state.Status == models.AITaggingStateStatusCompleted || state.Status == models.AITaggingStateStatusSkipped, nil
}

func (s *AITaggingService) setProcessing(videoID uint, fingerprint string) error {
	now := s.now()
	return database.Transaction(func(tx *gorm.DB) error {
		var state models.AITaggingState
		err := tx.Where("video_id = ?", videoID).First(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			state = models.AITaggingState{
				VideoID:             videoID,
				Status:              models.AITaggingStateStatusProcessing,
				EvidenceFingerprint: fingerprint,
				AttemptCount:        1,
				LastProcessedAt:     &now,
			}
			return tx.Create(&state).Error
		}
		if err != nil {
			return err
		}
		return tx.Model(&state).Updates(map[string]interface{}{
			"status":               models.AITaggingStateStatusProcessing,
			"skip_reason":          "",
			"evidence_fingerprint": fingerprint,
			"attempt_count":        state.AttemptCount + 1,
			"last_error":           "",
			"last_processed_at":    &now,
		}).Error
	})
}

func (s *AITaggingService) markState(videoID uint, status, skipReason, fingerprint, lastError string) error {
	now := s.now()
	updates := map[string]interface{}{
		"status":            status,
		"skip_reason":       skipReason,
		"last_error":        lastError,
		"last_processed_at": &now,
	}
	if fingerprint != "" {
		updates["evidence_fingerprint"] = fingerprint
	}
	var state models.AITaggingState
	err := database.DB.Where("video_id = ?", videoID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		state = models.AITaggingState{
			VideoID:             videoID,
			Status:              status,
			SkipReason:          skipReason,
			EvidenceFingerprint: fingerprint,
			LastError:           lastError,
			LastProcessedAt:     &now,
		}
		return database.DB.Create(&state).Error
	}
	if err != nil {
		return err
	}
	return database.DB.Model(&state).Updates(updates).Error
}

func (s *AITaggingService) persistSuggestions(video models.Video, tags []models.Tag, evidence AITaggingEvidence, suggestions []AITagSuggestion, runIDs ...*uint) (int, error) {
	var runID *uint
	if len(runIDs) > 0 {
		runID = runIDs[0]
	}
	matcher := newAITagMatcher(tags)
	// 规则 3：用户拒绝过的 (视频, 标签) 不再生成候选，拒绝跟着标签 id 走，改名后依然有效。
	rejected, err := s.rejectedCandidateTagIDs(video.ID)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, suggestion := range suggestions {
		confidence := normalizeAIConfidence(suggestion.Confidence)
		if confidence == "" || confidence == models.AITagConfidenceLow {
			continue
		}
		label := strings.TrimSpace(suggestion.Label)
		normalized := normalizeAITagName(label)
		if normalized == "" {
			continue
		}
		var matchedTagID *uint
		if matched, ok := matcher.match(suggestion.MatchedExistingName, label); ok {
			id := matched.ID
			matchedTagID = &id
			label = matched.Name
			normalized = normalizeAITagName(label)
		}
		// AI suggestions must always match the unified, non-automatic tag library.
		if matchedTagID == nil {
			log.Printf("[AITagging] drop out-of-library suggestion video_id=%d", video.ID)
			continue
		}
		if _, denied := rejected[*matchedTagID]; denied {
			continue
		}
		reasoning := strings.TrimSpace(suggestion.Reasoning)
		if evidence.SubtitleTemporary {
			reasoning = "使用本地临时字幕作为辅助证据；临时字幕正文未保存。"
		}
		candidate := models.AITagCandidate{
			VideoID:        video.ID,
			SuggestedName:  label,
			NormalizedName: normalized,
			MatchedTagID:   matchedTagID,
			RunID:          runID,
			Confidence:     confidence,
			Reasoning:      reasoning,
			SourceSummary:  evidence.SummaryJSON(),
			Status:         models.AITagCandidateStatusPending,
		}
		var existing models.AITagCandidate
		err := database.DB.Where("video_id = ? AND matched_tag_id = ? AND status = ?", candidate.VideoID, candidate.MatchedTagID, models.AITagCandidateStatusPending).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := database.DB.Create(&candidate).Error; err != nil {
				return created, err
			}
			created++
			continue
		}
		if err != nil {
			return created, err
		}
		if err := database.DB.Model(&existing).Updates(map[string]interface{}{
			"suggested_name": candidate.SuggestedName,
			"matched_tag_id": candidate.MatchedTagID,
			"confidence":     candidate.Confidence,
			"reasoning":      candidate.Reasoning,
			"source_summary": candidate.SourceSummary,
			"run_id":         candidate.RunID,
		}).Error; err != nil {
			return created, err
		}
	}
	return created, nil
}

// rejectedCandidateTagIDs 返回该视频已被拒绝的候选所对应的标签 id 集合。
func (s *AITaggingService) rejectedCandidateTagIDs(videoID uint) (map[uint]struct{}, error) {
	var ids []uint
	if err := database.DB.Model(&models.AITagCandidate{}).
		Where("video_id = ? AND status = ? AND matched_tag_id IS NOT NULL", videoID, models.AITagCandidateStatusRejected).
		Distinct().Pluck("matched_tag_id", &ids).Error; err != nil {
		return nil, err
	}
	denied := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		denied[id] = struct{}{}
	}
	return denied, nil
}

// aiTagCandidateQuery 是全量列表与游标翻页共用的查询：预载、筛选与排序只有一份。
// 排序从 (created_at desc, id desc) 收成单键 id desc——键集分页需要单一稳定键，
// 而 id 自增、与 created_at 同向增长，用户看到的顺序不变。
func aiTagCandidateQuery(videoID uint, confidence string, status string) *gorm.DB {
	query := database.DB.
		Model(&models.AITagCandidate{}).
		Preload("Video", func(db *gorm.DB) *gorm.DB { return db.Unscoped() }).
		Preload("Video.Tags").
		Preload("MatchedTag")
	if videoID > 0 {
		query = query.Where("ai_tag_candidates.video_id = ?", videoID)
	}
	if confidence = normalizeAIConfidence(confidence); confidence != "" {
		query = query.Where("ai_tag_candidates.confidence = ?", confidence)
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = models.AITagCandidateStatusPending
	}
	return query.Where("ai_tag_candidates.status = ?", status).Order("ai_tag_candidates.id desc")
}

func aiTagCandidateReviewItems(candidates []models.AITagCandidate) []AITaggingReviewItem {
	items := make([]AITaggingReviewItem, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, aiTagCandidateReviewItem(candidate))
	}
	return items
}

func (s *AITaggingService) ListCandidates(videoID uint, confidence string, status string) ([]AITaggingReviewItem, error) {
	var candidates []models.AITagCandidate
	if err := aiTagCandidateQuery(videoID, confidence, status).Find(&candidates).Error; err != nil {
		return nil, err
	}
	return aiTagCandidateReviewItems(candidates), nil
}

// ListCandidatePage 按候选 id 游标取一页。待审候选没有上限，全量下发在大库上
// 既压 IPC 又要前端一次渲染上千行；审阅工作台改走这条。cursorID 为 0 表示第一页。
func (s *AITaggingService) ListCandidatePage(videoID uint, confidence string, status string, cursorID uint, limit int) (*AITagCandidatePage, error) {
	limit = normalizeEntityPageLimit(limit)
	query := aiTagCandidateQuery(videoID, confidence, status)
	if cursorID > 0 {
		query = query.Where("ai_tag_candidates.id < ?", cursorID)
	}
	var candidates []models.AITagCandidate
	if err := query.Limit(limit).Find(&candidates).Error; err != nil {
		return nil, err
	}
	page := &AITagCandidatePage{Items: aiTagCandidateReviewItems(candidates)}
	// 只有这一页满员才给下一页游标：短页即末页，不用再多发一次空请求。
	if len(candidates) == limit {
		page.NextID = candidates[len(candidates)-1].ID
	}
	return page, nil
}

func activeVideoExistsInTx(tx *gorm.DB, videoID uint) error {
	if videoID == 0 {
		return gorm.ErrRecordNotFound
	}
	var video models.Video
	return tx.Select("id").First(&video, videoID).Error
}

func (s *AITaggingService) ApproveCandidate(candidateID uint) (*AITaggingReviewItem, error) {
	var approved models.AITagCandidate
	err := database.Transaction(func(tx *gorm.DB) error {
		var candidate models.AITagCandidate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", candidateID).First(&candidate).Error; err != nil {
			return err
		}
		if err := activeVideoExistsInTx(tx, candidate.VideoID); err != nil {
			return err
		}
		if candidate.Status == models.AITagCandidateStatusSuperseded {
			return ErrAITagCandidateSuperseded
		}
		if candidate.Status != models.AITagCandidateStatusPending {
			return ErrAITagCandidateNotPending
		}
		if candidate.Confidence != models.AITagConfidenceHigh && candidate.Confidence != models.AITagConfidenceMedium {
			return ErrAITagCandidateNotApprovable
		}
		tagID, err := s.resolveOfficialTagInTx(tx, candidate)
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, candidate.VideoID, tagID).Error; err != nil {
			return err
		}
		now := s.now()
		result := tx.Model(&models.AITagCandidate{}).
			Where("id = ? AND status = ?", candidate.ID, models.AITagCandidateStatusPending).
			Updates(map[string]interface{}{
				"status":      models.AITagCandidateStatusApproved,
				"approved_at": &now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAITagCandidateNotPending
		}
		approvalRecord := models.AITagApprovalRecord{
			VideoID:     candidate.VideoID,
			TagID:       tagID,
			CandidateID: candidate.ID,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&approvalRecord).Error; err != nil {
			return err
		}
		// 连带作废的 updated_at 显式写成这次批准的 approved_at：supersededCandidateReason 靠
		// 「同标签候选的批准时间不晚于本候选的作废时间」认出这种作废（META-11 M-4），两者
		// 必须是同一个时间，不能一个取注入的时钟、一个取 GORM 的自动时间。
		if err := tx.Model(&models.AITagCandidate{}).
			Where("video_id = ? AND matched_tag_id = ? AND id <> ? AND status = ?", candidate.VideoID, candidate.MatchedTagID, candidate.ID, models.AITagCandidateStatusPending).
			Updates(map[string]interface{}{
				"status":     models.AITagCandidateStatusSuperseded,
				"updated_at": now,
			}).Error; err != nil {
			return err
		}
		approved = candidate
		approved.Status = models.AITagCandidateStatusApproved
		approved.ApprovedAt = &now
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := database.DB.Preload("Video").Preload("Video.Tags").Preload("MatchedTag").First(&approved, candidateID).Error; err != nil {
		return nil, err
	}
	item := aiTagCandidateReviewItem(approved)
	return &item, nil
}

func (s *AITaggingService) resolveOfficialTagInTx(tx *gorm.DB, candidate models.AITagCandidate) (uint, error) {
	if candidate.MatchedTagID == nil {
		return 0, ErrAITagCandidateNoTag
	}
	var tag models.Tag
	// Unscoped：标签被软删（删除 / 合并）时要报「标签已删除或已合并」，不能落成「候选不存在」。
	if err := tx.Unscoped().First(&tag, *candidate.MatchedTagID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrAITagCandidateTagDeleted
		}
		return 0, err
	}
	if tag.DeletedAt.IsValid() {
		return 0, ErrAITagCandidateTagDeleted
	}
	if !isAITagEligible(tag) {
		return 0, ErrAITagCandidateTagUnavailable
	}
	return tag.ID, nil
}

func (s *AITaggingService) RejectCandidate(candidateID uint) error {
	now := s.now()
	result := database.DB.Model(&models.AITagCandidate{}).
		Where("id = ? AND status = ?", candidateID, models.AITagCandidateStatusPending).
		Updates(map[string]interface{}{
			"status":      models.AITagCandidateStatusRejected,
			"rejected_at": &now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("candidate is not pending")
	}
	return nil
}

func (s *AITaggingService) RejectPendingCandidatesByVideo(videoID uint) (int64, error) {
	now := s.now()
	var rejected int64
	err := database.Transaction(func(tx *gorm.DB) error {
		if err := activeVideoExistsInTx(tx, videoID); err != nil {
			return err
		}
		result := tx.Model(&models.AITagCandidate{}).
			Where("video_id = ? AND status = ?", videoID, models.AITagCandidateStatusPending).
			Updates(map[string]interface{}{
				"status":      models.AITagCandidateStatusRejected,
				"rejected_at": &now,
			})
		if result.Error != nil {
			return result.Error
		}
		rejected = result.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return rejected, nil
}

// RetryVideo 是用户显式的「重新分析」：对已有人工标签的视频同样生效（规则 5），
// 通过 pending 状态行上的 manual_retry 标记让 worker 跳过人工标签检查。
func (s *AITaggingService) RetryVideo(videoID uint) error {
	return s.requeueVideo(videoID, aiTaggingSkipReasonManualRetry)
}

// requeueVideo 作废待审候选并把状态置回 pending。skipReason 为空是自动重排。
func (s *AITaggingService) requeueVideo(videoID uint, skipReason string) error {
	return database.Transaction(func(tx *gorm.DB) error {
		if err := activeVideoExistsInTx(tx, videoID); err != nil {
			return err
		}
		if err := tx.Model(&models.AITagCandidate{}).
			Where("video_id = ? AND status = ?", videoID, models.AITagCandidateStatusPending).
			Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
			return err
		}
		var state models.AITaggingState
		err := tx.Where("video_id = ?", videoID).First(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			state = models.AITaggingState{VideoID: videoID, Status: models.AITaggingStateStatusPending, SkipReason: skipReason}
			return tx.Create(&state).Error
		}
		if err != nil {
			return err
		}
		return tx.Model(&state).Updates(map[string]interface{}{
			"status":               models.AITaggingStateStatusPending,
			"skip_reason":          skipReason,
			"evidence_fingerprint": "",
			"last_error":           "",
		}).Error
	})
}

func (s *AITaggingService) StatusSummary() (*AITaggingStatusSummary, error) {
	_, configErr := s.configProvider.Load()
	summary := &AITaggingStatusSummary{ConfigAvailable: configErr == nil}
	if err := database.DB.Model(&models.AITagCandidate{}).
		Joins("INNER JOIN videos ON videos.id = ai_tag_candidates.video_id AND videos.deleted_at IS NULL").
		Where("ai_tag_candidates.status = ?", models.AITagCandidateStatusPending).
		Count(&summary.Pending).Error; err != nil {
		return nil, err
	}
	if s.sameSource != nil {
		if sameSourceService, ok := s.sameSource.(*AISameSourceService); ok {
			unread, err := sameSourceService.UnreadCount()
			if err != nil {
				return nil, err
			}
			summary.SameSourceUnread = unread
		}
	}
	countState := func(status string, target *int64) error {
		return database.DB.Model(&models.AITaggingState{}).
			Joins("INNER JOIN videos ON videos.id = ai_tagging_states.video_id AND videos.deleted_at IS NULL").
			Where("ai_tagging_states.status = ?", status).
			Count(target).Error
	}
	if err := countState(models.AITaggingStateStatusProcessing, &summary.Processing); err != nil {
		return nil, err
	}
	if err := countState(models.AITaggingStateStatusCompleted, &summary.Completed); err != nil {
		return nil, err
	}
	if err := countState(models.AITaggingStateStatusSkipped, &summary.Skipped); err != nil {
		return nil, err
	}
	if err := countState(models.AITaggingStateStatusFailed, &summary.Failed); err != nil {
		return nil, err
	}
	return summary, nil
}

func (s *AITaggingService) ListSameSourceRelations(status string, unreadOnly bool) ([]VideoSameSourceReviewItem, error) {
	service, ok := s.sameSource.(*AISameSourceService)
	if !ok {
		return nil, fmt.Errorf("same-source service unavailable")
	}
	return service.ListRelations(status, unreadOnly)
}

// UnconfirmedSameSourceCount 返回待确认的同源关系总数（口径同 ListSameSourceRelations，不截断）。
func (s *AITaggingService) UnconfirmedSameSourceCount() (int64, error) {
	service, ok := s.sameSource.(*AISameSourceService)
	if !ok {
		return 0, fmt.Errorf("same-source service unavailable")
	}
	return service.UnconfirmedCount()
}

func (s *AITaggingService) MarkSameSourceRelationRead(relationID uint) error {
	service, ok := s.sameSource.(*AISameSourceService)
	if !ok {
		return fmt.Errorf("same-source service unavailable")
	}
	return service.MarkRelationRead(relationID)
}

// SameSourceService 返回底层同源服务实例，供确定性来源功能（如视频超分
// 发布后的指纹缓存）复用；不可用时返回 nil。
func (s *AITaggingService) SameSourceService() *AISameSourceService {
	service, _ := s.sameSource.(*AISameSourceService)
	return service
}

func (s *AITaggingService) ConfirmSameSourceRelation(relationID uint) error {
	service, ok := s.sameSource.(*AISameSourceService)
	if !ok {
		return fmt.Errorf("same-source service unavailable")
	}
	return service.ConfirmRelation(relationID)
}

func (s *AITaggingService) RejectSameSourceRelation(relationID uint) error {
	service, ok := s.sameSource.(*AISameSourceService)
	if !ok {
		return fmt.Errorf("same-source service unavailable")
	}
	return service.RejectRelation(relationID)
}

func normalizeAIConfidence(confidence string) string {
	switch strings.ToLower(strings.TrimSpace(confidence)) {
	case models.AITagConfidenceHigh:
		return models.AITagConfidenceHigh
	case models.AITagConfidenceMedium:
		return models.AITagConfidenceMedium
	case models.AITagConfidenceLow:
		return models.AITagConfidenceLow
	default:
		return ""
	}
}

func normalizeAITagName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func aiTagCandidateReviewItem(candidate models.AITagCandidate) AITaggingReviewItem {
	var video *models.Video
	if candidate.Video.ID != 0 {
		v := candidate.Video
		video = &v
	}
	return AITaggingReviewItem{
		ID:             candidate.ID,
		VideoID:        candidate.VideoID,
		Video:          video,
		VideoDeleted:   candidate.Video.ID != 0 && candidate.Video.DeletedAt.IsValid(),
		SuggestedName:  candidate.SuggestedName,
		NormalizedName: candidate.NormalizedName,
		MatchedTagID:   candidate.MatchedTagID,
		MatchedTag:     candidate.MatchedTag,
		Confidence:     candidate.Confidence,
		Reasoning:      candidate.Reasoning,
		SourceSummary:  candidate.SourceSummary,
		Status:         candidate.Status,
		CreatedAt:      candidate.CreatedAt.Format(time.RFC3339),
		UpdatedAt:      candidate.UpdatedAt.Format(time.RFC3339),
	}
}

// SupersedeCandidatesForManualTag 在用户手动给视频加标签时，把同视频 pending 且
// matched_tag_id 等于该标签的候选条件更新为 superseded（规则 2）。
// 由 AddTagToVideo 在同一事务内调用（P-020 接入）；返回被作废的候选 id，供前端局部移除。
func SupersedeCandidatesForManualTag(tx *gorm.DB, videoID, tagID uint) ([]uint, error) {
	var ids []uint
	if err := tx.Model(&models.AITagCandidate{}).
		Where("video_id = ? AND matched_tag_id = ? AND status = ?", videoID, tagID, models.AITagCandidateStatusPending).
		Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	now := time.Now()
	if err := tx.Model(&models.AITagCandidate{}).
		Where("id IN ? AND status = ?", ids, models.AITagCandidateStatusPending).
		Updates(map[string]interface{}{
			"status":      models.AITagCandidateStatusSuperseded,
			"rejected_at": &now,
		}).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// 批准候选的可识别失败。英文文案保持不变（单条批准的前端按子串判断「已不在待审」），
// 批量结果里由 aiTagApproveMessage 翻成中文。
var (
	ErrAITagCandidateNotPending     = errors.New("candidate is not pending")
	ErrAITagCandidateSuperseded     = fmt.Errorf("%w (superseded by another candidate of the same tag)", ErrAITagCandidateNotPending)
	ErrAITagCandidateNotApprovable  = errors.New("candidate confidence is not approvable")
	ErrAITagCandidateNoTag          = errors.New("candidate is not matched to the configured tag library")
	ErrAITagCandidateTagUnavailable = errors.New("candidate tag is no longer available in the configured tag library")
	// ErrAITagCandidateTagDeleted：候选指向的标签已被删除或合并（软删 / 行已不在）。
	ErrAITagCandidateTagDeleted = fmt.Errorf("%w (tag deleted or merged)", ErrAITagCandidateTagUnavailable)
)

// 批量批准里 superseded 项的文案，按候选真正被作废的原因给（META-11）。
const (
	aiTagSupersededBySibling    = "同标签的其他候选已批准"
	aiTagSupersededByManualTag  = "已手动添加该标签"
	aiTagSupersededByReanalysis = "视频已重新分析（词表变化或手动重新分析），这条候选已失效"
	aiTagSupersededByTagDeleted = "标签已删除或已合并"
	aiTagSupersededOutOfLibrary = "候选标签已不在 AI 词表中"
	aiTagSupersededUnknown      = "候选已失效"
)

// aiTagApproveMessage 把批准失败翻成给用户看的中文。superseded 的具体原因由
// supersededCandidateReason 查库得出，这里只给兜底文案。
func aiTagApproveMessage(err error) string {
	switch {
	case errors.Is(err, ErrAITagCandidateSuperseded):
		return aiTagSupersededUnknown
	case errors.Is(err, ErrAITagCandidateTagDeleted):
		return aiTagSupersededByTagDeleted + "，无法批准"
	case errors.Is(err, ErrAITagCandidateNotPending):
		return "候选已不在待审状态"
	case errors.Is(err, ErrAITagCandidateNotApprovable):
		return "候选置信度过低，不可批准"
	case errors.Is(err, ErrAITagCandidateNoTag):
		return "候选未匹配到词表中的标签"
	case errors.Is(err, ErrAITagCandidateTagUnavailable):
		return "候选标签已不在 AI 词表中"
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "候选不存在，或所属视频已被删除"
	}
	return "批准失败：" + err.Error()
}

// supersededCandidateReason 查出一条 superseded 候选真正被作废的原因（META-11）。候选表没有
// 原因列，按作废路径各自留下的痕迹倒推，先查痕迹最确定的：
//  1. rejected_at 非空：只有 SupersedeCandidatesForManualTag（手动加标签）会给 superseded 写它；
//  2. 同视频同标签有一条候选的批准时间落在 [本候选生成, 本候选作废] 之间：被那次批准连带作废。
//     作废时间就是本候选的 updated_at（连带作废与批准同一事务、同一个时间，见 ApproveCandidate）。
//     只看「生成之后批准」不够（M-4）：早先因重新分析作废的候选，之后同标签另一条被批准，
//     批准时间晚于它的作废时间——那不是它作废的原因；
//  3. 标签已不在 / 已软删：删除或合并；标签还在但出了 AI 词表（改成人物分类等）；
//  4. 视频上已有该标签（未留痕的旧路径手动添加）；
//  5. 其余是重新分析（证据指纹变化、词表变化重排、手动重新分析）作废的。
//
// 读库失败只影响文案，给兜底的「候选已失效」，不让一次查询失败把整批结果变成错误。
// 时间比较放在 Go 里做：SQLite 把时间存成文本，按字符串比较在小数位数不同时会排错。
func (s *AITaggingService) supersededCandidateReason(candidateID uint) string {
	var candidate models.AITagCandidate
	if err := database.DB.Select("id", "video_id", "matched_tag_id", "created_at", "updated_at", "rejected_at").
		First(&candidate, candidateID).Error; err != nil {
		return aiTagSupersededUnknown
	}
	if candidate.RejectedAt != nil {
		return aiTagSupersededByManualTag
	}
	if candidate.MatchedTagID == nil {
		return aiTagSupersededByTagDeleted
	}
	tagID := *candidate.MatchedTagID
	var siblings []models.AITagCandidate
	if err := database.DB.Select("id", "approved_at").
		Where("video_id = ? AND matched_tag_id = ? AND id <> ? AND status = ? AND approved_at IS NOT NULL",
			candidate.VideoID, tagID, candidate.ID, models.AITagCandidateStatusApproved).
		Find(&siblings).Error; err != nil {
		return aiTagSupersededUnknown
	}
	// laterApproval：本候选作废之后，同标签另一条才被批准。那次批准会把标签挂到视频上，下面
	// 「视频上已有该标签 → 手动添加」的推断因此不成立——标签是后来批准的，不是本候选作废的原因。
	laterApproval := false
	for _, sibling := range siblings {
		approvedAt := *sibling.ApprovedAt
		if approvedAt.Before(candidate.CreatedAt) {
			continue
		}
		if !approvedAt.After(candidate.UpdatedAt) {
			return aiTagSupersededBySibling
		}
		laterApproval = true
	}
	var tag models.Tag
	if err := database.DB.Unscoped().First(&tag, tagID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return aiTagSupersededByTagDeleted
		}
		return aiTagSupersededUnknown
	}
	if tag.DeletedAt.IsValid() {
		return aiTagSupersededByTagDeleted
	}
	if !isAITagEligible(tag) {
		return aiTagSupersededOutOfLibrary
	}
	if laterApproval {
		return aiTagSupersededByReanalysis
	}
	var tagged int64
	if err := database.DB.Table("video_tags").Where("video_id = ? AND tag_id = ?", candidate.VideoID, tagID).
		Count(&tagged).Error; err != nil {
		return aiTagSupersededUnknown
	}
	if tagged > 0 {
		return aiTagSupersededByManualTag
	}
	return aiTagSupersededByReanalysis
}

// ApproveCandidates 逐项沿用单条批准的事务（D-PC29）；一条失败不影响其余。
// 重复 id 只处理一次。已经作废（含同视频同标签里先批准的一条连带作废其余）的项标为
// superseded（不算失败），文案按真实原因给。
func (s *AITaggingService) ApproveCandidates(ids []uint) AITagBatchResult {
	result := AITagBatchResult{Results: make([]AITagBatchItemResult, 0, len(ids))}
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		result.Requested++
		item, err := s.ApproveCandidate(id)
		if errors.Is(err, ErrAITagCandidateSuperseded) {
			result.Superseded++
			result.Results = append(result.Results, AITagBatchItemResult{ID: id, Superseded: true, Message: s.supersededCandidateReason(id)})
			continue
		}
		if err != nil {
			result.Failed++
			result.Results = append(result.Results, AITagBatchItemResult{ID: id, Message: aiTagApproveMessage(err)})
			continue
		}
		result.Succeeded++
		result.Results = append(result.Results, AITagBatchItemResult{ID: id, OK: true, Item: item})
	}
	return result
}

// ErrAITagFilterTagRequired：按筛选批准必须先选标签，空筛选不执行（避免一键批准全部待审）。
var ErrAITagFilterTagRequired = errors.New("请先选择一个标签再批准筛选结果")

// approvableCandidate 是按筛选解析出的一条可批准候选。
type approvableCandidate struct {
	ID      uint
	VideoID uint
}

// approvableCandidatesByFilter 把筛选条件解析成候选（按 id 升序）。口径与列表查询一致：标签
// 精确匹配、置信度（可选）精确匹配、状态 pending；另外只含视频未删除、置信度可批准
// （high / medium）、且标签仍在 AI 词表里（未删除、非自动、非人物分类）的候选——这些条件
// 任何一条不满足都批不了，计入预览会让「预览 N 条、批准 M 条」对不上。
func (s *AITaggingService) approvableCandidatesByFilter(filter AITagCandidateFilter) ([]approvableCandidate, error) {
	if filter.TagID == 0 {
		return nil, ErrAITagFilterTagRequired
	}
	query := database.DB.Model(&models.AITagCandidate{}).
		Joins("INNER JOIN videos ON videos.id = ai_tag_candidates.video_id AND videos.deleted_at IS NULL").
		Joins(`INNER JOIN tags ON tags.id = ai_tag_candidates.matched_tag_id AND tags.deleted_at IS NULL
			AND COALESCE(tags.automatic_kind, '') = '' AND TRIM(COALESCE(tags.namespace, '')) <> ?`, personTagNamespace).
		Where("ai_tag_candidates.status = ?", models.AITagCandidateStatusPending).
		Where("ai_tag_candidates.matched_tag_id = ?", filter.TagID).
		Where("ai_tag_candidates.confidence IN ?", []string{models.AITagConfidenceHigh, models.AITagConfidenceMedium})
	if strings.TrimSpace(filter.Confidence) != "" {
		confidence := normalizeAIConfidence(filter.Confidence)
		if confidence == "" {
			return nil, fmt.Errorf("无效的置信度：%q", filter.Confidence)
		}
		query = query.Where("ai_tag_candidates.confidence = ?", confidence)
	}
	var candidates []approvableCandidate
	if err := query.Select("ai_tag_candidates.id AS id, ai_tag_candidates.video_id AS video_id").
		Order("ai_tag_candidates.id").Scan(&candidates).Error; err != nil {
		return nil, err
	}
	return candidates, nil
}

// CountCandidatesByFilter 给前端在批准前显示计数预览，口径与 ApproveCandidatesByFilter 的
// 实际批准数相同：筛选固定了标签，同一视频的多条候选只会批准第一条（其余被它连带作废），
// 所以按视频去重计数。
func (s *AITaggingService) CountCandidatesByFilter(filter AITagCandidateFilter) (int, error) {
	candidates, err := s.approvableCandidatesByFilter(filter)
	if err != nil {
		return 0, err
	}
	videos := make(map[uint]struct{}, len(candidates))
	for _, candidate := range candidates {
		videos[candidate.VideoID] = struct{}{}
	}
	return len(videos), nil
}

// ApproveCandidatesByFilter 在服务端把筛选条件解析成候选 id，再交给 ApproveCandidates。
// 同视频的后几条会以 superseded（同标签的其他候选已批准）出现在逐项结果里，前端据此局部移除。
func (s *AITaggingService) ApproveCandidatesByFilter(filter AITagCandidateFilter) (AITagBatchResult, error) {
	candidates, err := s.approvableCandidatesByFilter(filter)
	if err != nil {
		return AITagBatchResult{}, err
	}
	ids := make([]uint, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	return s.ApproveCandidates(ids), nil
}
