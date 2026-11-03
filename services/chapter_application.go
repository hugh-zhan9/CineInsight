package services

import (
	"context"
	"database/sql"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"time"
	"video-master/database"
	"video-master/models"
)

type ChapterApplicationRequest struct {
	JobID                   uint   `json:"job_id"`
	ExpectedJobRevision     uint64 `json:"expected_job_revision"`
	ExpectedChapterRevision uint64 `json:"expected_chapter_revision"`
}
type ChapterApplicationPreview struct {
	ApplyToken string         `json:"apply_token"`
	ExpiresAt  time.Time      `json:"expires_at" ts_type:"string"`
	JobID      uint           `json:"job_id"`
	Scope      ChapterScope   `json:"scope"`
	RemoveIDs  []uint         `json:"remove_ids"`
	Additions  []ChapterInput `json:"additions"`
	Retained   int            `json:"retained"`
	Conflicts  []ChapterInput `json:"conflicts"`
	Code       string         `json:"code"`
}
type ChapterApplicationReceipt struct {
	JobID    uint   `json:"job_id"`
	SetID    string `json:"set_id"`
	Revision uint64 `json:"revision"`
}
type chapterApplicationSnapshot struct {
	db       *gorm.DB
	expires  time.Time
	job      models.ChapterDraftJob
	chapters []ChapterInput
	receipt  *ChapterApplicationReceipt
}

func mergeChapterApplication(existing []ChapterInput, draft []ChapterDraftItem, scope ChapterScope) ([]ChapterInput, []uint, []ChapterInput, error) {
	merged := []ChapterInput{}
	removed := []uint{}
	conflicts := []ChapterInput{}
	for _, chapter := range existing {
		if scope.Whole || chapter.StartMS >= scope.StartMS && chapter.EndMS <= scope.EndMS {
			removed = append(removed, chapter.ID)
			continue
		}
		if chapter.StartMS < scope.EndMS && chapter.EndMS > scope.StartMS {
			conflicts = append(conflicts, chapter)
		}
		merged = append(merged, chapter)
	}
	if len(conflicts) > 0 {
		return nil, removed, conflicts, ErrChapterRangeConflict
	}
	// Preserve the order and identity of chapters outside the reviewed range.
	position := 0
	for position < len(merged) && merged[position].EndMS <= scope.StartMS {
		position++
	}
	additions := make([]ChapterInput, 0, len(draft))
	for _, item := range draft {
		chapter := item.ChapterInput
		chapter.ID = 0
		additions = append(additions, chapter)
	}
	result := append([]ChapterInput{}, merged[:position]...)
	result = append(result, additions...)
	result = append(result, merged[position:]...)
	return result, removed, conflicts, nil
}
func (s *ChapterService) pruneChapterPreviewsLocked() {
	for token, snapshot := range s.owner.previews {
		if snapshot.db != database.DB || !time.Now().Before(snapshot.expires) {
			delete(s.owner.previews, token)
		}
	}
}
func (s *ChapterService) PreviewChapterApplication(ctx context.Context, request ChapterApplicationRequest) (*ChapterApplicationPreview, error) {
	draft, err := s.GetChapterDraft(ctx, request.JobID)
	if err != nil {
		return nil, err
	}
	job := draft.Job
	if job.Compacted || job.Status != "draft_ready" || job.VideoID == nil {
		return nil, ErrChapterDraftState
	}
	if job.Revision != request.ExpectedJobRevision || job.ExpectedChapterRevision != request.ExpectedChapterRevision {
		return nil, ErrChapterConflict
	}
	result := &ChapterApplicationPreview{JobID: job.ID, Scope: ChapterScope{StartMS: job.ScopeStartMS, EndMS: job.ScopeEndMS, Whole: job.Whole}, RemoveIDs: []uint{}, Additions: []ChapterInput{}, Conflicts: []ChapterInput{}}
	snapshot := &chapterApplicationSnapshot{db: database.DB, expires: time.Now().Add(10 * time.Minute), job: job}
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
			var set models.ChapterSet
			err := tx.Where("video_id = ?", *job.VideoID).First(&set).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if set.Revision != request.ExpectedChapterRevision {
				return ErrChapterConflict
			}
			existing := []ChapterInput{}
			if set.ID != 0 {
				rows, err := chapterRows(tx, set.ID)
				if err != nil {
					return err
				}
				existing = chapterInputs(rows)
			}
			merged, removed, conflicts, err := mergeChapterApplication(existing, draft.Chapters, result.Scope)
			result.RemoveIDs = removed
			result.Conflicts = conflicts
			result.Retained = len(existing) - len(removed)
			for _, item := range draft.Chapters {
				chapter := item.ChapterInput
				chapter.ID = 0
				result.Additions = append(result.Additions, chapter)
			}
			if errors.Is(err, ErrChapterRangeConflict) {
				result.Code = "chapter_range_conflict"
				return nil
			}
			if err != nil {
				return err
			}
			video, _, err := bookmarkSourceInScope(tx, *job.VideoID)
			if err != nil {
				return err
			}
			snapshot.chapters, err = normalizeChapters(merged, chapterDuration(video))
			return err
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	})
	if err != nil {
		return nil, err
	}
	if len(result.Conflicts) > 0 {
		return result, nil
	}
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	if s.owner.closed {
		return nil, ErrPlaybackQuiesced
	}
	s.pruneChapterPreviewsLocked()
	pending := 0
	for _, preview := range s.owner.previews {
		if preview.receipt == nil {
			pending++
		}
	}
	if pending >= 16 || len(s.owner.previews) >= 4096 {
		return nil, ErrChapterDraftBusy
	}
	result.ApplyToken = uuid.NewString()
	result.ExpiresAt = snapshot.expires
	s.owner.previews[result.ApplyToken] = snapshot
	return result, nil
}
func (s *ChapterService) ApplyChapterDraft(ctx context.Context, token string) (*ChapterApplicationReceipt, error) {
	changed := false
	s.owner.mu.Lock()
	defer func() {
		s.owner.mu.Unlock()
		if changed {
			s.changed()
		}
	}()
	if s.owner.closed {
		return nil, ErrPlaybackQuiesced
	}
	s.pruneChapterPreviewsLocked()
	snapshot := s.owner.previews[token]
	if snapshot == nil {
		return nil, ErrChapterDraftObsolete
	}
	if snapshot.receipt != nil {
		receipt := *snapshot.receipt
		return &receipt, nil
	}
	request := chapterJobRequest(snapshot.job)
	var view *ChapterSetView
	_, err := s.withChapterInput(ctx, request.VideoID, request.Scope, func(current *chapterInputSnapshot, db *gorm.DB) error {
		if !chapterMatchesRequest(current, request) {
			return ErrChapterDraftObsolete
		}
		return db.Transaction(func(tx *gorm.DB) error {
			job := snapshot.job
			ok, err := chapterJobTransition(tx, &job, "draft_ready", map[string]any{"status": "applied", "draft_json": ""}, &request.ExpectedChapterRevision)
			if err != nil {
				return err
			}
			if !ok {
				return ErrChapterConflict
			}
			view, err = saveChapterSetTx(tx, current.video, current.size, current.mtime, request.ExpectedChapterRevision, snapshot.chapters, &job.ID)
			if err != nil {
				return err
			}
			return compactChapterJobs(tx)
		})
	})
	if err != nil {
		return nil, err
	}
	changed = true
	snapshot.receipt = &ChapterApplicationReceipt{JobID: snapshot.job.ID, SetID: view.SetID, Revision: view.Revision}
	snapshot.chapters = nil
	snapshot.job.DraftJSON = ""
	receipt := *snapshot.receipt
	return &receipt, nil
}
func (s *ChapterService) DiscardChapterApplication(ctx context.Context, token string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	s.pruneChapterPreviewsLocked()
	snapshot := s.owner.previews[token]
	if snapshot != nil && snapshot.receipt == nil {
		delete(s.owner.previews, token)
		return true, nil
	}
	return false, nil
}
