package services

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"
)

type ChapterDraftView struct {
	ExpectedChapterRevision uint64                 `json:"expected_chapter_revision"`
	SourceVersion           string                 `json:"source_version"`
	Job                     models.ChapterDraftJob `json:"job"`
	Chapters                []ChapterDraftItem     `json:"chapters"`
}
type ChapterDraftStart struct {
	JobID  uint   `json:"job_id"`
	Status string `json:"status"`
}
type ChapterJobQuery struct {
	VideoID  uint `json:"video_id"`
	CursorID uint `json:"cursor_id"`
	Limit    int  `json:"limit"`
}
type ChapterJobPage struct {
	Items      []models.ChapterDraftJob `json:"items"`
	NextCursor uint                     `json:"next_cursor"`
}
type chapterRuntime struct {
	cancel context.CancelFunc
	db     *gorm.DB
}

const chapterDraftBudget = 15 * time.Minute

type chapterOwner struct {
	budget   time.Duration
	warning  string
	mu       sync.Mutex
	closed   bool
	active   map[uint]chapterRuntime
	work     *MediaWorkSlot
	workers  sync.WaitGroup
	registry *BackgroundTaskRegistry
	emit     func()
	previews map[string]*chapterApplicationSnapshot
}

func (s *ChapterService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	s.owner.registry = registry
}
func (s *ChapterService) SetChangedListener(emit func()) {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	s.owner.emit = emit
}
func (s *ChapterService) changed() {
	s.owner.mu.Lock()
	emit := s.owner.emit
	s.owner.mu.Unlock()
	if emit != nil {
		emit()
	}
}
func chapterRequestDigest(request StartChapterRequest) string {
	encoded, _ := json.Marshal(request)
	return fmtDigest(sha256.Sum256(encoded))
}
func (s *ChapterService) StartChapterDraft(ctx context.Context, request StartChapterRequest) (*ChapterDraftStart, error) {
	if !validLibraryQueryRequestID(request.RequestID) || !request.ConfirmedScope || request.ExpectedChapterRevision >= experienceMaxSafeInteger {
		return nil, ErrChapterInvalid
	}
	digest := chapterRequestDigest(request)
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	if s.owner.closed {
		return nil, ErrPlaybackQuiesced
	}
	var existing models.ChapterDraftJob
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error { return db.Where("request_id = ?", request.RequestID).First(&existing).Error })
	if err == nil {
		return chapterExistingRequest(existing, digest)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if len(s.owner.active) >= 16 {
		return nil, ErrChapterDraftBusy
	}
	if s.textAI == nil {
		return nil, ErrTextAIUnavailable
	}
	if err := s.textAI.Validate(ctx, TextAIChapterPurpose, request.ProviderRevision); err != nil {
		return nil, err
	}
	snapshot, err := s.readChapterInput(ctx, request.VideoID, request.Scope)
	if err != nil {
		return nil, err
	}
	if snapshot.source != request.SourceVersion || snapshot.subtitle != request.SubtitleFingerprint || snapshot.chapterRevision != request.ExpectedChapterRevision || snapshot.scope != request.Scope {
		return nil, ErrChapterDraftObsolete
	}
	status, err := s.textAI.Status(ctx)
	if err != nil {
		return nil, err
	}
	if status.SummaryRevision != request.ProviderRevision {
		return nil, ErrTextAIConfigChanged
	}
	videoID := request.VideoID
	title := snapshot.video.DisplayTitle
	if title == "" {
		title = snapshot.video.Name
	}
	job := models.ChapterDraftJob{RequestID: request.RequestID, RequestDigest: digest, VideoID: &videoID, TitleSnapshot: title, Status: "queued", Revision: 1, SourceSize: snapshot.size, SourceModTimeNS: snapshot.mtime, SubtitleFingerprint: snapshot.subtitle, ExpectedChapterRevision: snapshot.chapterRevision, ProviderRevision: request.ProviderRevision, Model: status.Model, ScopeStartMS: snapshot.scope.StartMS, ScopeEndMS: snapshot.scope.EndMS, Whole: snapshot.scope.Whole, TotalChunks: len(snapshot.chunks)}
	created := false
	var jobDB *gorm.DB
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		jobDB = database.DB
		result := db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "request_id"}}, DoNothing: true}).Create(&job)
		if result.Error != nil {
			return result.Error
		}
		created = result.RowsAffected == 1
		if !created {
			return db.Where("request_id = ?", request.RequestID).First(&existing).Error
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !created {
		return chapterExistingRequest(existing, digest)
	}
	runCtx, cancel := context.WithDeadline(ctx, job.CreatedAt.Add(s.owner.budget))
	s.owner.active[job.ID] = chapterRuntime{cancel: cancel, db: jobDB}
	s.owner.workers.Add(1)
	registry := s.owner.registry
	// Registration and notification run outside the owner lock; callbacks may read status.
	go func() {
		registry.Begin(BackgroundTaskChapterSummary)
		defer registry.End(BackgroundTaskChapterSummary)
		defer s.owner.workers.Done()
		defer cancel()
		defer func() { s.owner.mu.Lock(); delete(s.owner.active, job.ID); s.owner.mu.Unlock(); s.changed() }()
		s.runChapterDraft(runCtx, jobDB, job, request)
	}()
	return &ChapterDraftStart{JobID: job.ID, Status: job.Status}, nil
}
func chapterExistingRequest(job models.ChapterDraftJob, digest string) (*ChapterDraftStart, error) {
	if job.RequestDigest != digest {
		return nil, ErrChapterRequestUsed
	}
	status := job.Status
	if job.Compacted {
		status = "job_compacted"
	}
	return &ChapterDraftStart{JobID: job.ID, Status: status}, nil
}
func chapterMatchesRequest(snapshot *chapterInputSnapshot, request StartChapterRequest) bool {
	return snapshot.source == request.SourceVersion && snapshot.subtitle == request.SubtitleFingerprint && snapshot.chapterRevision == request.ExpectedChapterRevision && snapshot.scope == request.Scope
}
func chapterRevisionCondition(tx *gorm.DB, query *gorm.DB, videoID uint, revision uint64) *gorm.DB {
	root := tx.Model(&models.ChapterSet{}).Select("1").Where("video_id = ?", videoID)
	if revision == 0 {
		return query.Where("NOT EXISTS (?)", root)
	}
	return query.Where("EXISTS (?)", root.Where("revision = ?", revision))
}
func chapterJobTransition(db *gorm.DB, job *models.ChapterDraftJob, from string, updates map[string]any, chapterRevision *uint64) (bool, error) {
	query := db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Model(&models.ChapterDraftJob{}).Where("id = ? AND status = ? AND revision = ? AND attempt = ?", job.ID, from, job.Revision, job.Attempt)
	if chapterRevision != nil && job.VideoID != nil {
		query = chapterRevisionCondition(db, query, *job.VideoID, *chapterRevision)
	}
	updates["revision"] = gorm.Expr("revision + 1")
	updates["updated_at"] = time.Now()
	result := query.Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, nil
	}
	job.Revision++
	if status, ok := updates["status"].(string); ok {
		job.Status = status
	}
	return true, nil
}
func (s *ChapterService) finishChapterDraft(db *gorm.DB, job models.ChapterDraftJob, state, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// This captured DB remains open until StopAndWait completes, including maintenance.
	db = db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	result := db.Model(&models.ChapterDraftJob{}).Where("id = ? AND status IN ?", job.ID, []string{"queued", "running"}).Updates(map[string]any{"status": state, "error_code": code, "revision": gorm.Expr("revision + 1"), "updated_at": time.Now()})
	err := result.Error
	if err == nil {
		err = compactChapterJobs(db)
	}
	if err != nil {
		s.owner.mu.Lock()
		s.owner.warning = "章节任务状态或终态整理未能保存，请重新打开后核对"
		s.owner.mu.Unlock()
	}
	s.changed()
}
func chapterInputObsolete(err error) bool {
	return errors.Is(err, ErrBookmarkSourceChanged) || errors.Is(err, ErrBookmarkSourceUnavailable) || errors.Is(err, ErrChapterSubtitleUnavailable) || errors.Is(err, ErrChapterSummaryTooLarge) || errors.Is(err, ErrChapterDurationUnknown) || errors.Is(err, ErrChapterDraftObsolete)
}
func (s *ChapterService) runChapterDraft(ctx context.Context, db *gorm.DB, job models.ChapterDraftJob, request StartChapterRequest) {
	fail := func(err error) {
		state, code := "failed", "summary_failed"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			state, code = "failed", "summary_timeout"
		} else if ctx.Err() != nil || errors.Is(err, ErrTextAIDisabled) || errors.Is(err, ErrTextAIConfigChanged) || errors.Is(err, context.Canceled) {
			state, code = "cancelled", "cancelled"
		} else if chapterInputObsolete(err) {
			state, code = "obsolete", "input_changed"
		} else if errors.Is(err, ErrTextAIOutput) {
			code = "ai_output_invalid"
		}
		s.finishChapterDraft(db, job, state, code)
	}
	if err := s.owner.work.Acquire(ctx); err != nil {
		fail(err)
		return
	}
	defer s.owner.work.Release()
	if err := s.textAI.Validate(ctx, TextAIChapterPurpose, request.ProviderRevision); err != nil {
		fail(err)
		return
	}
	snapshot, err := s.readChapterInput(ctx, request.VideoID, request.Scope)
	if err != nil {
		fail(err)
		return
	}
	if !chapterMatchesRequest(snapshot, request) {
		fail(ErrChapterDraftObsolete)
		return
	}
	ok, err := chapterJobTransition(db.WithContext(ctx), &job, "queued", map[string]any{"status": "running", "attempt": gorm.Expr("attempt + 1")}, &request.ExpectedChapterRevision)
	if err != nil {
		fail(err)
		return
	}
	if !ok {
		fail(ErrChapterDraftObsolete)
		return
	}
	job.Attempt++
	s.changed()
	chapters := []ChapterDraftItem{}
	for i, chunk := range snapshot.chunks {
		if err := ctx.Err(); err != nil {
			fail(err)
			return
		}
		text, err := s.textAI.Complete(ctx, TextAIChapterPurpose, request.ProviderRevision, chapterSummaryInstruction, encodeChapterChunk(chunk))
		if err != nil {
			fail(err)
			return
		}
		parsed, err := parseChapterSummary(text, chunk)
		if err != nil {
			fail(err)
			return
		}
		chapters = append(chapters, parsed...)
		if len(chapters) > 500 {
			fail(ErrTextAIOutput)
			return
		}
		ok, err := chapterJobTransition(db.WithContext(ctx), &job, "running", map[string]any{"completed_chunks": i + 1}, nil)
		if err != nil {
			fail(err)
			return
		}
		if !ok {
			return
		}
		s.changed()
	}
	snapshot = nil // Drop all input text before validating and persisting the output.
	inputs := []ChapterInput{}
	for _, chapter := range chapters {
		inputs = append(inputs, chapter.ChapterInput)
	}
	if _, err := normalizeChapters(inputs, request.Scope.EndMS); err != nil {
		fail(ErrTextAIOutput)
		return
	}
	encoded, err := json.Marshal(chapters)
	if err != nil {
		fail(err)
		return
	}
	_, err = s.withChapterInput(ctx, request.VideoID, request.Scope, func(current *chapterInputSnapshot, admitted *gorm.DB) error {
		if !chapterMatchesRequest(current, request) {
			return ErrChapterDraftObsolete
		}
		return s.textAI.CommitPurpose(ctx, TextAIChapterPurpose, request.ProviderRevision, func() error {
			committed, err := chapterJobTransition(admitted, &job, "running", map[string]any{"status": "draft_ready", "draft_json": string(encoded)}, &request.ExpectedChapterRevision)
			if err != nil {
				return err
			}
			if !committed {
				return ErrChapterDraftObsolete
			}
			return nil
		})
	})
	if err != nil {
		fail(err)
		return
	}
	s.changed()
}
func compactChapterJobs(db *gorm.DB) error {
	var ids []uint
	terminal := []string{"cancelled", "failed", "obsolete", "applied", "discarded", "interrupted"}
	if err := db.Model(&models.ChapterDraftJob{}).Where("status IN ? AND draft_json = '' AND compacted = ?", terminal, false).Order("id DESC").Offset(20).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return db.Model(&models.ChapterDraftJob{}).Where("id IN ? AND status IN ? AND draft_json = ''", ids, terminal).Updates(map[string]any{"compacted": true, "video_id": nil, "title_snapshot": "", "source_size": 0, "source_mod_time_ns": 0, "subtitle_fingerprint": "", "expected_chapter_revision": 0, "provider_revision": "", "model": "", "scope_start_ms": 0, "scope_end_ms": 0, "whole": false, "completed_chunks": 0, "total_chunks": 0, "error_code": "", "revision": gorm.Expr("revision + 1")}).Error
}
func (s *ChapterService) ListChapterDraftJobs(ctx context.Context, request ChapterJobQuery) (*ChapterJobPage, error) {
	if request.VideoID > experienceMaxSafeInteger || request.CursorID > experienceMaxSafeInteger {
		return nil, ErrChapterInvalid
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	result := &ChapterJobPage{Items: []models.ChapterDraftJob{}}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		query := db.Where("compacted = ?", false)
		if request.VideoID != 0 {
			query = query.Where("video_id = ?", request.VideoID)
		}
		if request.CursorID != 0 {
			query = query.Where("id < ?", request.CursorID)
		}
		if err := query.Omit("draft_json").Order("id DESC").Limit(limit + 1).Find(&result.Items).Error; err != nil {
			return err
		}
		if len(result.Items) > limit {
			result.Items = result.Items[:limit]
			result.NextCursor = result.Items[len(result.Items)-1].ID
		}
		return nil
	})
	return result, err
}

// ListChapterJobsWithOpen keeps active work visible independently of recent history.
func (s *ChapterService) ListChapterJobsWithOpen(ctx context.Context, recent int) ([]models.ChapterDraftJob, error) {
	recent = min(100, max(1, recent))
	items := []models.ChapterDraftJob{}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		open := []string{"queued", "running"}
		history := db.Model(&models.ChapterDraftJob{}).Select("id").
			Where("compacted = ? AND status NOT IN ?", false, open).Order("id DESC").Limit(recent)
		return db.Where("compacted = ? AND (status IN ? OR id IN (?))", false, open, history).
			Omit("draft_json").Order("id DESC").Find(&items).Error
	})
	return items, err
}

func chapterJobRequest(job models.ChapterDraftJob) StartChapterRequest {
	id := uint(0)
	if job.VideoID != nil {
		id = *job.VideoID
	}
	return StartChapterRequest{RequestID: job.RequestID, VideoID: id, SourceVersion: videoSourceVersion(id, job.SourceSize, job.SourceModTimeNS), SubtitleFingerprint: job.SubtitleFingerprint, ExpectedChapterRevision: job.ExpectedChapterRevision, ProviderRevision: job.ProviderRevision, Scope: ChapterScope{StartMS: job.ScopeStartMS, EndMS: job.ScopeEndMS, Whole: job.Whole}, ConfirmedScope: true}
}
func (s *ChapterService) GetChapterDraft(ctx context.Context, id uint) (*ChapterDraftView, error) {
	if id == 0 || id > experienceMaxSafeInteger {
		return nil, ErrChapterInvalid
	}
	result := &ChapterDraftView{Chapters: []ChapterDraftItem{}}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error { return db.First(&result.Job, id).Error })
	if err != nil {
		return nil, err
	}
	if result.Job.Status == "draft_ready" {
		request := chapterJobRequest(result.Job)
		current, err := s.readChapterInput(ctx, request.VideoID, request.Scope)
		if err != nil && !chapterInputObsolete(err) {
			return nil, err
		}
		if err != nil || !chapterMatchesRequest(current, request) {
			err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
				_, err := chapterJobTransition(db, &result.Job, "draft_ready", map[string]any{"status": "obsolete", "error_code": "input_changed"}, nil)
				if err != nil {
					return err
				}
				return db.First(&result.Job, id).Error
			})
			if err != nil {
				return nil, err
			}
		}
	}
	if result.Job.DraftJSON != "" {
		if json.Unmarshal([]byte(result.Job.DraftJSON), &result.Chapters) != nil {
			return nil, ErrChapterInvalid
		}
	}
	result.ExpectedChapterRevision = result.Job.ExpectedChapterRevision
	if result.Job.VideoID != nil {
		result.SourceVersion = videoSourceVersion(*result.Job.VideoID, result.Job.SourceSize, result.Job.SourceModTimeNS)
	}
	return result, nil
}
func (s *ChapterService) CancelChapterDraft(ctx context.Context, id uint, revision uint64) error {
	if id == 0 || id > experienceMaxSafeInteger || revision == 0 || revision >= experienceMaxSafeInteger {
		return ErrChapterInvalid
	}
	err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		result := tx.Model(&models.ChapterDraftJob{}).Where("id = ? AND revision = ? AND status IN ?", id, revision, []string{"queued", "running"}).Updates(map[string]any{"status": "cancelled", "revision": gorm.Expr("revision + 1"), "error_code": "cancelled", "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrChapterConflict
		}
		return compactChapterJobs(tx)
	})
	if err == nil {
		s.owner.mu.Lock()
		run, ok := s.owner.active[id]
		s.owner.mu.Unlock()
		if ok {
			run.cancel()
		}
		s.changed()
	}
	return err
}
func (s *ChapterService) DiscardChapterDraft(ctx context.Context, id uint, revision uint64) error {
	if id == 0 || id > experienceMaxSafeInteger || revision == 0 || revision >= experienceMaxSafeInteger {
		return ErrChapterInvalid
	}
	err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		result := tx.Model(&models.ChapterDraftJob{}).Where("id = ? AND revision = ? AND status = ?", id, revision, "draft_ready").Updates(map[string]any{"status": "discarded", "draft_json": "", "revision": gorm.Expr("revision + 1"), "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrChapterConflict
		}
		return compactChapterJobs(tx)
	})
	if err == nil {
		s.changed()
	}
	return err
}
func (s *ChapterService) CancelActiveDrafts() int {
	s.owner.mu.Lock()
	runs := make([]chapterRuntime, 0, len(s.owner.active))
	for _, run := range s.owner.active {
		runs = append(runs, run)
	}
	s.owner.mu.Unlock()
	for _, run := range runs {
		run.cancel()
	}
	return len(runs)
}
func (s *ChapterService) StopAndWait() {
	s.owner.mu.Lock()
	s.owner.closed = true
	s.owner.previews = map[string]*chapterApplicationSnapshot{}
	for _, run := range s.owner.active {
		run.cancel()
	}
	s.owner.mu.Unlock()
	s.owner.workers.Wait()
}
func (s *ChapterService) RecoverInterrupted(ctx context.Context) error {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	if len(s.owner.active) != 0 {
		return fmt.Errorf("chapter owner still active")
	}
	s.owner.closed = true
	err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		if err := tx.Model(&models.ChapterDraftJob{}).Where("status IN ?", []string{"queued", "running"}).Updates(map[string]any{"status": "interrupted", "revision": gorm.Expr("revision + 1"), "error_code": "interrupted", "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		return compactChapterJobs(tx)
	})
	if err == nil {
		s.owner.closed = false
		s.owner.warning = ""
	}
	return err
}

func (s *ChapterService) Warning() string {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	return s.owner.warning
}
