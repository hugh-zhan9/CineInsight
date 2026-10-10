package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

const (
	maxReviewPreviewMembers = 1_000_000
	maxReviewPreviews       = 2
	reviewPreviewLifetime   = 10 * time.Minute
)

var (
	ErrReviewBusy    = errors.New("review_batch_busy: 已有审阅预览或批准批次正在处理")
	ErrReviewClosed  = errors.New("review_batch_closed: 审阅正在停止，请稍后重新预览")
	ErrReviewExpired = errors.New("review_batch_expired: 预览或批次已失效，请重新预览确认")
	ErrReviewLimit   = errors.New("review_batch_limit: 匹配结果或预览容量超过上限，请缩小筛选或关闭已有预览")
	ErrReviewChanged = errors.New("review_candidate_changed: 候选已变化，请重新审阅")
)

type ReviewApprovalRequest struct {
	Scope  string              `json:"scope"`
	IDs    []uint              `json:"ids"`
	Filter ReviewSearchRequest `json:"filter"`
}

type ReviewApprovalPreview struct {
	Token      string    `json:"token"`
	Scope      string    `json:"scope"`
	Matched    int       `json:"matched"`
	Eligible   int       `json:"eligible"`
	MediaCount int       `json:"media_count"`
	LinkCount  int       `json:"link_count"`
	Excluded   int       `json:"excluded"`
	ExpiresAt  time.Time `json:"expires_at" ts_type:"string"`
}

type ReviewApprovalOutcome struct {
	ID      uint   `json:"id"`
	MediaID uint   `json:"media_id"`
	TagID   uint   `json:"tag_id"`
	State   string `json:"state"`
	Code    string `json:"code"`
}

type ReviewApprovalState struct {
	Token      string                  `json:"token"`
	State      string                  `json:"state"`
	Total      int                     `json:"total"`
	Processed  int                     `json:"processed"`
	Succeeded  int                     `json:"succeeded"`
	Skipped    int                     `json:"skipped"`
	Failed     int                     `json:"failed"`
	Remaining  int                     `json:"remaining"`
	StartedAt  *time.Time              `json:"started_at" ts_type:"string"`
	FinishedAt *time.Time              `json:"finished_at" ts_type:"string"`
	Code       string                  `json:"code"`
	Results    []ReviewApprovalOutcome `json:"results"`
	NextAfter  int                     `json:"next_after"`
	HasMore    bool                    `json:"has_more"`
}

type frozenReviewCandidate struct {
	ID       uint
	Revision [32]byte
}

type reviewApprovalBuild struct {
	preview ReviewApprovalPreview
	members []frozenReviewCandidate
}

type reviewApprovalJob struct {
	state   ReviewApprovalState
	results []ReviewApprovalOutcome
	cancel  context.CancelFunc
}

// ReviewApprovalWorkflow owns only transient previews and this process's latest batch.
// Candidate reads and transactions are supplied by the existing media-specific service.
type ReviewApprovalWorkflow struct {
	mu          sync.Mutex
	workers     sync.WaitGroup
	closed      bool
	generation  uint64
	building    bool
	buildCancel context.CancelFunc
	previews    map[string]reviewApprovalBuild
	job         *reviewApprovalJob
	now         func() time.Time
	build       func(context.Context, ReviewApprovalRequest) (reviewApprovalBuild, error)
	approve     func(context.Context, frozenReviewCandidate) (ReviewApprovalOutcome, error)
	registry    func() *BackgroundTaskRegistry
	key         BackgroundTaskKey
	onProgress  func()
}

func newReviewApprovalWorkflow(build func(context.Context, ReviewApprovalRequest) (reviewApprovalBuild, error), approve func(context.Context, frozenReviewCandidate) (ReviewApprovalOutcome, error), registry func() *BackgroundTaskRegistry, key BackgroundTaskKey) *ReviewApprovalWorkflow {
	return &ReviewApprovalWorkflow{now: time.Now, build: build, approve: approve, registry: registry, key: key, previews: map[string]reviewApprovalBuild{}}
}

func (w *ReviewApprovalWorkflow) SetProgressNotifier(notify func()) {
	w.mu.Lock()
	w.onProgress = notify
	w.mu.Unlock()
}

func (w *ReviewApprovalWorkflow) purgePreviewsLocked() {
	for token, preview := range w.previews {
		if !w.now().Before(preview.preview.ExpiresAt) {
			delete(w.previews, token)
		}
	}
}

func (w *ReviewApprovalWorkflow) Preview(ctx context.Context, request ReviewApprovalRequest) (ReviewApprovalPreview, error) {
	if ctx == nil {
		return ReviewApprovalPreview{}, errors.New("review_context_required: 审阅上下文不能为空")
	}
	w.mu.Lock()
	w.purgePreviewsLocked()
	if w.closed {
		w.mu.Unlock()
		return ReviewApprovalPreview{}, ErrReviewClosed
	}
	if w.building || (w.job != nil && w.job.state.State == "running") {
		w.mu.Unlock()
		return ReviewApprovalPreview{}, ErrReviewBusy
	}
	if len(w.previews) >= maxReviewPreviews {
		w.mu.Unlock()
		return ReviewApprovalPreview{}, ErrReviewLimit
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	w.building, w.buildCancel = true, cancel
	generation := w.generation
	w.workers.Add(1)
	w.mu.Unlock()
	defer w.workers.Done()
	defer cancel()

	built, err := w.build(ctx, request)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.building, w.buildCancel = false, nil
	// A slow driver can finish after cancellation. It must never republish old-library IDs.
	if w.closed || generation != w.generation {
		return ReviewApprovalPreview{}, ErrReviewClosed
	}
	if err != nil {
		return ReviewApprovalPreview{}, err
	}
	if err := ctx.Err(); err != nil {
		return ReviewApprovalPreview{}, err
	}
	w.purgePreviewsLocked()
	total := len(built.members)
	for _, existing := range w.previews {
		total += len(existing.members)
	}
	if built.preview.Matched > maxReviewPreviewMembers || total > maxReviewPreviewMembers {
		return ReviewApprovalPreview{}, ErrReviewLimit
	}
	built.preview.Eligible = len(built.members)
	built.preview.Excluded = built.preview.Matched - len(built.members)
	built.preview.ExpiresAt = w.now().Add(reviewPreviewLifetime)
	if len(built.members) == 0 {
		return built.preview, nil
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return ReviewApprovalPreview{}, err
	}
	built.preview.Token = hex.EncodeToString(bytes[:])
	w.previews[built.preview.Token] = built
	return built.preview, nil
}

func (w *ReviewApprovalWorkflow) Start(ctx context.Context, token string) (ReviewApprovalState, error) {
	if ctx == nil {
		return ReviewApprovalState{}, errors.New("review_context_required: 审阅上下文不能为空")
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ReviewApprovalState{}, ErrReviewClosed
	}
	if token != "" && w.job != nil && w.job.state.Token == token {
		state := w.stateLocked(0, 0)
		w.mu.Unlock()
		return state, nil
	}
	w.purgePreviewsLocked()
	preview, exists := w.previews[token]
	if !exists || token == "" {
		w.mu.Unlock()
		return ReviewApprovalState{}, ErrReviewExpired
	}
	if w.building || (w.job != nil && w.job.state.State == "running") {
		w.mu.Unlock()
		return ReviewApprovalState{}, ErrReviewBusy
	}
	if err := ctx.Err(); err != nil {
		w.mu.Unlock()
		return ReviewApprovalState{}, err
	}
	delete(w.previews, token)
	ctx, cancel := context.WithCancel(ctx)
	now := w.now()
	job := &reviewApprovalJob{state: ReviewApprovalState{Token: token, State: "running", Total: len(preview.members), StartedAt: &now}, cancel: cancel}
	w.job = job
	w.workers.Add(1)
	state := w.stateLocked(0, 0)
	w.mu.Unlock()
	// Registry callbacks may read our state, so they must run without the workflow mutex.
	registry := w.registry()
	registry.Begin(w.key)
	go w.run(ctx, job, preview.members, registry)
	return state, nil
}

func (w *ReviewApprovalWorkflow) run(ctx context.Context, job *reviewApprovalJob, members []frozenReviewCandidate, registry *BackgroundTaskRegistry) {
	defer w.workers.Done()
	defer registry.End(w.key)
	defer job.cancel()
	terminal, code := "completed", ""
	lastProgressAt := w.now()
	for _, member := range members {
		if ctx.Err() != nil {
			terminal, code = "cancelled", "cancelled"
			break
		}
		outcome, fatal := w.approve(ctx, member)
		if fatal != nil {
			terminal, code = "failed", "database_unavailable"
			if ctx.Err() != nil {
				terminal, code = "cancelled", "cancelled"
			}
			break
		}
		w.mu.Lock()
		job.results = append(job.results, outcome)
		job.state.Processed++
		switch outcome.State {
		case "approved":
			job.state.Succeeded++
		case "skipped":
			job.state.Skipped++
		default:
			job.state.Failed++
		}
		var notify func()
		if now := w.now(); now.Sub(lastProgressAt) >= time.Second {
			lastProgressAt, notify = now, w.onProgress
		}
		w.mu.Unlock()
		if notify != nil {
			notify()
		}
	}
	w.mu.Lock()
	now := w.now()
	job.state.State, job.state.Code, job.state.FinishedAt = terminal, code, &now
	w.mu.Unlock()
}

func (w *ReviewApprovalWorkflow) State(token string, after, limit int) (ReviewApprovalState, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.purgePreviewsLocked()
	if token != "" && (w.job == nil || w.job.state.Token != token) {
		return ReviewApprovalState{}, ErrReviewExpired
	}
	if after < 0 || (after > 0 && token == "") || (w.job != nil && after > len(w.job.results)) {
		return ReviewApprovalState{}, errors.New("review_result_cursor: 无效的批次结果游标")
	}
	return w.stateLocked(after, min(max(limit, 0), 200)), nil
}

func (w *ReviewApprovalWorkflow) stateLocked(after, limit int) ReviewApprovalState {
	state := ReviewApprovalState{State: "idle", Results: []ReviewApprovalOutcome{}}
	if w.job == nil {
		return state
	}
	state = w.job.state
	state.Remaining = state.Total - state.Processed
	end := min(after+limit, len(w.job.results))
	state.Results = append([]ReviewApprovalOutcome{}, w.job.results[after:end]...)
	state.NextAfter, state.HasMore = end, end < len(w.job.results)
	return state
}

func (w *ReviewApprovalWorkflow) Cancel(token string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if token != "" {
		if _, exists := w.previews[token]; exists {
			delete(w.previews, token)
			return nil
		}
	}
	if w.job != nil && (token == "" || w.job.state.Token == token) {
		if w.job.state.State == "running" {
			w.job.cancel()
		}
		return nil
	}
	if token == "" {
		return nil
	}
	return ErrReviewExpired
}

// CloseAndWait fences both construction and execution before database replacement or exit.
func (w *ReviewApprovalWorkflow) CloseAndWait() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.closed = true
	w.generation++
	w.previews = map[string]reviewApprovalBuild{}
	if w.buildCancel != nil {
		w.buildCancel()
	}
	if w.job != nil && w.job.state.State == "running" {
		w.job.cancel()
	}
	w.mu.Unlock()
	w.workers.Wait()
}
