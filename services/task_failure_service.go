package services

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log"
	"sort"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"
)

const (
	TaskFailureAI        = "ai_tagging"
	TaskFailureTechnical = "technical"
	TaskFailureProxy     = "proxy"
)

var ErrTaskFailureIncomplete = errors.New("task_failure_records_incomplete: 失败记录不完整，不能重试全部失败")

type TaskFailureListRequest struct {
	TaskKey  string `json:"task_key"`
	RunID    uint   `json:"run_id"`
	CursorID uint   `json:"cursor_id"`
	Limit    int    `json:"limit"`
}
type TaskFailurePage struct {
	Items           []models.TaskFailureItem `json:"items"`
	Total           int64                    `json:"total"`
	NextCursor      uint                     `json:"next_cursor"`
	RecordsComplete bool                     `json:"records_complete"`
}
type TaskFailureRetryRequest struct {
	RunID   uint   `json:"run_id"`
	ItemIDs []uint `json:"item_ids"`
}
type TaskFailureRetryPreview struct {
	Token         string    `json:"token"`
	EligibleCount int       `json:"eligible_count"`
	SkippedCount  int       `json:"skipped_count"`
	ExpiresAt     time.Time `json:"expires_at" ts_type:"string"`
}
type TaskFailureRetryItemResult struct {
	ItemID  uint   `json:"item_id"`
	MediaID uint   `json:"media_id"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
}
type TaskFailureRetryPage struct {
	OperationID string                       `json:"operation_id"`
	State       string                       `json:"state"`
	Total       int                          `json:"total"`
	Accepted    int                          `json:"accepted"`
	Skipped     int                          `json:"skipped"`
	Failed      int                          `json:"failed"`
	Items       []TaskFailureRetryItemResult `json:"items"`
	NextOffset  int                          `json:"next_offset"`
}
type taskFailureFrozen struct {
	Item   models.TaskFailureItem
	Reason string
}
type taskFailurePreview struct {
	RunID   uint
	TaskKey string
	Expires time.Time
	Items   []taskFailureFrozen
}
type taskFailureOperation struct {
	Accepted   int
	Skipped    int
	Failed     int
	FinishedAt time.Time
	State      string
	Total      int
	Results    []TaskFailureRetryItemResult
	Cancel     context.CancelFunc
}
type taskFailureClaim func(context.Context, []taskFailureFrozen, func(TaskFailureRetryItemResult)) []TaskFailureRetryItemResult

// TaskFailureService stores diagnostics and composes explicit retries; original
// services alone own execution. Tokens and unfinished submissions never survive restart.
type TaskFailureService struct {
	pruning        bool
	pruneCancel    context.CancelFunc
	warningReaders map[string]func() string
	mu             sync.Mutex
	previews       map[string]taskFailurePreview
	operations     map[string]*taskFailureOperation
	claims         map[string]taskFailureClaim
	incomplete     map[uint]bool
	stopped        bool
	worker         sync.WaitGroup
	now            func() time.Time
}

func NewTaskFailureService() *TaskFailureService {
	return &TaskFailureService{previews: map[string]taskFailurePreview{}, operations: map[string]*taskFailureOperation{}, claims: map[string]taskFailureClaim{}, incomplete: map[uint]bool{}, now: time.Now}
}
func (s *TaskFailureService) RegisterOwners(ai *AITaggingService, technical *TechnicalBackfillService, proxy *PlaybackProxyService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warningReaders = map[string]func() string{}
	if ai != nil {
		s.warningReaders[TaskFailureAI] = ai.TaskFailureWarning
		s.claims[TaskFailureAI] = ai.claimTaskFailures
	}
	if technical != nil {
		s.warningReaders[TaskFailureTechnical] = technical.TaskFailureWarning
		s.claims[TaskFailureTechnical] = technical.claimTaskFailures
	}
	if proxy != nil {
		s.warningReaders[TaskFailureProxy] = proxy.TaskFailureWarning
		s.claims[TaskFailureProxy] = proxy.claimTaskFailures
	}
}
func (s *TaskFailureService) RecoverInterrupted() error {
	return database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.VideoTechnicalMetadata{}).Where("failure_retry_claim <> ''").UpdateColumn("failure_retry_claim", "").Error; err != nil {
			return err
		}
		if err := tx.Model(&models.VideoPlaybackProxy{}).Where("failure_retry_claim <> ''").UpdateColumn("failure_retry_claim", "").Error; err != nil {
			return err
		}
		return tx.Model(&models.TaskFailureRun{}).Where("state = ?", "running").Updates(map[string]any{"state": "interrupted", "finished_at": s.now(), "records_complete": false}).Error
	})
}

func (s *TaskFailureService) Begin(taskKey string) (uint, error) {
	row := models.TaskFailureRun{TaskKey: taskKey, StartedAt: s.now(), State: "running", RecordsComplete: true}
	err := database.DB.Create(&row).Error
	return row.ID, err
}
func (s *TaskFailureService) markIncomplete(runID uint) {
	s.mu.Lock()
	s.incomplete[runID] = true
	s.mu.Unlock()
	database.DB.Model(&models.TaskFailureRun{}).Where("id = ?", runID).Update("records_complete", false)
}
func (s *TaskFailureService) Record(runID uint, taskKey string, videoID uint, attemptKey, sourceVersion, code, message string) error {
	snapshot, err := readTaskFailureSnapshot(database.DB, taskKey, videoID, false)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.markIncomplete(runID)
		return err
	}
	if sourceVersion == "" {
		sourceVersion = snapshot.SourceVersion
	}
	if attemptKey == "" {
		attemptKey = fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", taskKey, videoID, snapshot.Version))))
	}
	row := models.TaskFailureItem{RunID: runID, MediaKind: "video", MediaID: videoID, AttemptKey: attemptKey, SourceVersion: sourceVersion, StateVersion: snapshot.Version, ErrorCode: code, ErrorSummary: taskFailureSafeSummary(taskKey, code, message), FailedAt: s.now()}
	result := database.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil {
		s.markIncomplete(runID)
	}
	return result.Error
}
func (s *TaskFailureService) Finish(runID uint, state string) error {
	if state != "completed" && state != "cancelled" && state != "interrupted" {
		return fmt.Errorf("task_failure_run_transition_invalid")
	}
	s.mu.Lock()
	incomplete := s.incomplete[runID]
	s.mu.Unlock()
	updates := map[string]any{"state": state, "finished_at": s.now()}
	if incomplete {
		updates["records_complete"] = false
	}
	r := database.DB.Model(&models.TaskFailureRun{}).Where("id = ? AND state = ?", runID, "running").Updates(updates)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return fmt.Errorf("task_failure_run_conflict")
	}
	s.schedulePrune()
	return nil
}
func (s *TaskFailureService) ListRuns(taskKey string) ([]models.TaskFailureRun, error) {
	return s.ListRunsContext(context.Background(), taskKey)
}
func (s *TaskFailureService) ListRunsContext(ctx context.Context, taskKey string) ([]models.TaskFailureRun, error) {
	rows := []models.TaskFailureRun{}
	err := database.DB.WithContext(ctx).Where("task_key = ?", taskKey).Order("id DESC").Find(&rows).Error
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range rows {
		if s.incomplete[rows[i].ID] {
			rows[i].RecordsComplete = false
		}
	}
	return rows, err
}
func (s *TaskFailureService) List(request TaskFailureListRequest) (*TaskFailurePage, error) {
	return s.ListContext(context.Background(), request)
}
func (s *TaskFailureService) ListContext(ctx context.Context, request TaskFailureListRequest) (*TaskFailurePage, error) {
	db := database.DB.WithContext(ctx)
	var run models.TaskFailureRun
	if err := db.First(&run, request.RunID).Error; err != nil {
		return nil, err
	}
	if request.TaskKey != "" && request.TaskKey != run.TaskKey {
		return nil, fmt.Errorf("task_failure_run_mismatch")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	p := &TaskFailurePage{Items: []models.TaskFailureItem{}, RecordsComplete: run.RecordsComplete}
	s.mu.Lock()
	if s.incomplete[run.ID] {
		p.RecordsComplete = false
	}
	s.mu.Unlock()
	q := db.Model(&models.TaskFailureItem{}).Where("run_id = ?", run.ID)
	if err := q.Count(&p.Total).Error; err != nil {
		return nil, err
	}
	if err := q.Where("id > ?", request.CursorID).Order("id ASC").Limit(limit + 1).Find(&p.Items).Error; err != nil {
		return nil, err
	}
	if len(p.Items) > limit {
		p.Items = p.Items[:limit]
		p.NextCursor = p.Items[len(p.Items)-1].ID
	}
	return p, nil
}
func (s *TaskFailureService) Preview(request TaskFailureRetryRequest) (*TaskFailureRetryPreview, error) {
	return s.PreviewContext(context.Background(), request)
}

// PreviewContext freezes exactly the reviewed failure set within a cancellable App operation.
func (s *TaskFailureService) PreviewContext(ctx context.Context, request TaskFailureRetryRequest) (*TaskFailureRetryPreview, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db := database.DB.WithContext(ctx)
	if request.ItemIDs != nil && len(request.ItemIDs) == 0 {
		return nil, fmt.Errorf("task_failure_empty_selection")
	}
	var run models.TaskFailureRun
	if err := db.First(&run, request.RunID).Error; err != nil {
		return nil, err
	}
	s.mu.Lock()
	incomplete := s.incomplete[run.ID]
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		return nil, fmt.Errorf("task_failure_stopped")
	}
	if !run.RecordsComplete || incomplete {
		return nil, ErrTaskFailureIncomplete
	}
	var items []models.TaskFailureItem
	ids := uniqueUintIDs(request.ItemIDs)
	if len(ids) > 1000000 {
		return nil, fmt.Errorf("task_failure_member_limit")
	}
	if request.ItemIDs != nil {
		for _, chunk := range chunkUintIDs(ids, 500) {
			var rows []models.TaskFailureItem
			if err := db.Where("run_id = ? AND id IN ?", run.ID, chunk).Find(&rows).Error; err != nil {
				return nil, err
			}
			items = append(items, rows...)
		}
		if len(items) != len(ids) {
			return nil, fmt.Errorf("task_failure_selection_mismatch")
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	} else {
		if err := db.Where("run_id = ?", run.ID).Order("id ASC").Limit(1000001).Find(&items).Error; err != nil {
			return nil, err
		}
		if len(items) > 1000000 {
			return nil, fmt.Errorf("task_failure_member_limit")
		}
	}

	preview := taskFailurePreview{RunID: run.ID, TaskKey: run.TaskKey, Expires: s.now().Add(10 * time.Minute)}
	out := &TaskFailureRetryPreview{Token: uuid.NewString(), ExpiresAt: preview.Expires}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reason, err := taskFailureEligibility(db, run.TaskKey, item)
		if err != nil {
			return nil, err
		}
		preview.Items = append(preview.Items, taskFailureFrozen{Item: item, Reason: reason})
		if reason == "" {
			out.EligibleCount++
		} else {
			out.SkippedCount++
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, fmt.Errorf("task_failure_stopped")
	}
	for key, p := range s.previews {
		if !p.Expires.After(s.now()) {
			delete(s.previews, key)
		}
	}
	if len(s.previews) >= 2 {
		var oldest string
		var at time.Time
		for key, p := range s.previews {
			if oldest == "" || p.Expires.Before(at) {
				oldest = key
				at = p.Expires
			}
		}
		delete(s.previews, oldest)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.previews[out.Token] = preview
	return out, nil
}
func (s *TaskFailureService) Confirm(ctx context.Context, token string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.previews[token]
	if !ok || !p.Expires.After(s.now()) {
		delete(s.previews, token)
		return "", fmt.Errorf("task_failure_token_expired")
	}
	if s.stopped {
		return "", fmt.Errorf("task_failure_stopped")
	}
	var run models.TaskFailureRun
	if err := database.DB.WithContext(ctx).First(&run, p.RunID).Error; err != nil {
		return "", err
	}
	if !run.RecordsComplete || s.incomplete[p.RunID] {
		return "", ErrTaskFailureIncomplete
	}
	claim := s.claims[p.TaskKey]
	if claim == nil {
		return "", fmt.Errorf("task_failure_owner_unavailable")
	}
	completed := []string{}
	for id, op := range s.operations {
		if op.State != "running" {
			completed = append(completed, id)
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		return s.operations[completed[i]].FinishedAt.Before(s.operations[completed[j]].FinishedAt)
	})
	for len(completed) > 1 {
		delete(s.operations, completed[0])
		completed = completed[1:]
	} // retain last two finished submissions including this one
	if len(s.operations) >= 4 {
		return "", fmt.Errorf("task_failure_operations_busy")
	}
	delete(s.previews, token)
	id := uuid.NewString()
	runCtx, cancel := context.WithCancel(ctx)
	op := &taskFailureOperation{State: "running", Total: len(p.Items), Results: []TaskFailureRetryItemResult{}, Cancel: cancel}
	s.operations[id] = op
	s.worker.Add(1)
	go func() {
		defer s.worker.Done()
		defer cancel()
		claim(runCtx, p.Items, func(result TaskFailureRetryItemResult) {
			s.mu.Lock()
			op.Results = append(op.Results, result)
			switch result.Status {
			case "accepted":
				op.Accepted++
			case "skipped":
				op.Skipped++
			case "failed":
				op.Failed++
			}
			s.mu.Unlock()
		})
		s.mu.Lock()
		if runCtx.Err() != nil {
			op.State = "cancelled"
		} else {
			op.State = "completed"
		}
		op.FinishedAt = s.now()
		s.mu.Unlock()
	}()
	return id, nil
}
func (s *TaskFailureService) Results(id string, offset, limit int) (*TaskFailureRetryPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.operations[id]
	if op == nil {
		return nil, fmt.Errorf("task_failure_operation_missing")
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	p := &TaskFailureRetryPage{OperationID: id, State: op.State, Total: op.Total, Items: []TaskFailureRetryItemResult{}}
	p.Accepted = op.Accepted
	p.Skipped = op.Skipped
	p.Failed = op.Failed

	if offset < len(op.Results) {
		end := min(offset+limit, len(op.Results))
		p.Items = append(p.Items, op.Results[offset:end]...)
		if end < len(op.Results) {
			p.NextOffset = end
		}
	}
	return p, nil
}
func (s *TaskFailureService) Cancel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.operations[id]
	if op == nil {
		return fmt.Errorf("task_failure_operation_missing")
	}
	op.Cancel()
	return nil
}
func (s *TaskFailureService) StopAndWait() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	if s.pruneCancel != nil {
		s.pruneCancel()
	}
	s.previews = map[string]taskFailurePreview{}
	for _, op := range s.operations {
		op.Cancel()
	}
	s.mu.Unlock()
	s.worker.Wait()
}

// Prune keeps twenty resolved intervals per owner; unresolved and incomplete facts survive.
func (s *TaskFailureService) Prune() error { return s.prune(context.Background()) }
func (s *TaskFailureService) prune(ctx context.Context) error {
	db := database.DB.WithContext(ctx)
	var runs []models.TaskFailureRun
	if err := db.Where("state <> ? AND records_complete = ?", "running", true).Order("id DESC").Find(&runs).Error; err != nil {
		return err
	}
	resolved := map[string]int{}
	remove := []uint{}
	for _, run := range runs {
		var items []models.TaskFailureItem
		if err := db.Where("run_id = ?", run.ID).Find(&items).Error; err != nil {
			return err
		}
		pending := false
		for _, item := range items {
			snapshot, err := readTaskFailureSnapshot(db, run.TaskKey, item.MediaID, false)
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && snapshot.Failed {
				pending = true
				break
			}
		}
		if pending {
			continue
		}
		resolved[run.TaskKey]++
		if resolved[run.TaskKey] > 20 {
			remove = append(remove, run.ID)
		}
	}
	sort.Slice(remove, func(i, j int) bool { return remove[i] < remove[j] })
	return db.Transaction(func(tx *gorm.DB) error {
		for _, chunk := range chunkUintIDs(remove, 200) {
			if err := tx.Where("run_id IN ?", chunk).Delete(&models.TaskFailureItem{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ? AND state <> ?", chunk, "running").Delete(&models.TaskFailureRun{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *TaskFailureService) Warning(key string) string {
	s.mu.Lock()
	read := s.warningReaders[key]
	s.mu.Unlock()
	if read != nil {
		return read()
	}
	return ""
}

func taskFailureSafeSummary(key, code, message string) string {
	if key == TaskFailureAI {
		return "AI 分析失败，请检查资料源配置与服务可用性"
	}
	return truncateMediaProbeError(scrubPlaybackProxyPaths(message))
}

func (s *TaskFailureService) schedulePrune() {
	s.mu.Lock()
	if s.stopped || s.pruning {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.pruneCancel = cancel
	s.pruning = true
	s.worker.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.worker.Done()
		defer cancel()
		if err := s.prune(ctx); err != nil && ctx.Err() == nil {
			log.Printf("task failure retention: %v", err)
		}
		s.mu.Lock()
		s.pruning = false
		s.pruneCancel = nil
		s.mu.Unlock()
	}()
}
