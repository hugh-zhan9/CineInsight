package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func cropFixture(t *testing.T) (*CropDetectionService, models.Video) {
	t.Helper()
	setupVideoServiceTestDB(t)
	p := filepath.Join(t.TempDir(), "source.mp4")
	if e := os.WriteFile(p, []byte("source"), 0600); e != nil {
		t.Fatal(e)
	}
	v := models.Video{Name: "source.mp4", Path: p, Directory: filepath.Dir(p), Size: 6}
	if e := database.DB.Create(&v).Error; e != nil {
		t.Fatal(e)
	}
	s := NewCropDetectionService(NewVideoEditService(nil, NewMediaWorkSlot(), t.TempDir()), NewMediaWorkSlot())
	s.detect = func(context.Context, uint) (EditCropResult, error) {
		size, mtime, _ := editFileFingerprint(p)
		return EditCropResult{VideoID: v.ID, Status: "detected", Rect: &EditCropRect{X: 2, Y: 2, Width: 96, Height: 96}, DisplayWidth: 100, DisplayHeight: 100, DurationMS: 1000, SourceVersion: editCropSourceVersion(size, mtime)}, nil
	}
	return s, v
}
func TestCropDetectionDraftOnlyAndSameVersionDedup(t *testing.T) {
	s, v := cropFixture(t)
	for i := 0; i < 2; i++ {
		if e := s.process(context.Background(), v, false); e != nil {
			t.Fatal(e)
		}
	}
	var ps []models.VideoEditProject
	database.DB.Find(&ps)
	if len(ps) != 1 || ps[0].Status != "draft" {
		t.Fatal(ps)
	}
	var items int64
	database.DB.Model(&models.VideoEditItem{}).Count(&items)
	if items != 0 {
		t.Fatal("automatic export", items)
	}
	var r EditRecipe
	json.Unmarshal([]byte(ps[0].RecipeJSON), &r)
	if r.Crop.PublishPolicy != "replace_full" || r.Crop.Items[0].Confirmed || r.Crop.Items[0].Rect == nil {
		t.Fatal(r)
	}
	database.DB.Delete(&ps[0])
	if e := s.process(context.Background(), v, false); e != nil {
		t.Fatal(e)
	}
	database.DB.Model(&models.VideoEditProject{}).Count(&items)
	if items != 0 {
		t.Fatal("deleted draft revived")
	}
}
func TestCropDetectionNoBorderFailureManualRetryAndSourceChange(t *testing.T) {
	for _, result := range []string{"no_borders", "undetected", "failed"} {
		t.Run(result, func(t *testing.T) {
			s, v := cropFixture(t)
			calls := 0
			s.detect = func(context.Context, uint) (EditCropResult, error) {
				calls++
				size, mtime, _ := editFileFingerprint(v.Path)
				if result == "failed" {
					return EditCropResult{}, errors.New("decode failed")
				}
				return EditCropResult{VideoID: v.ID, Status: result, SourceVersion: editCropSourceVersion(size, mtime)}, nil
			}
			for i := 0; i < 2; i++ {
				if e := s.process(context.Background(), v, false); e != nil {
					t.Fatal(e)
				}
			}
			if calls != 1 {
				t.Fatal("duplicate automatic decoding", calls)
			}
			var count int64
			database.DB.Model(&models.VideoEditProject{}).Count(&count)
			if count != 0 {
				t.Fatal("noncandidate draft")
			}
			if result == "failed" {
				if e := s.process(context.Background(), v, true); e != nil {
					t.Fatal(e)
				}
				if calls != 2 {
					t.Fatal("manual retry skipped")
				}
			}
			os.WriteFile(v.Path, []byte("new version"), 0600)
			if e := s.process(context.Background(), v, false); e != nil {
				t.Fatal(e)
			}
			if calls < 2 {
				t.Fatal("version change skipped")
			}
		})
	}
}
func TestCropDetectionCancelledAndChangedResultNotCommitted(t *testing.T) {
	s, v := cropFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.detect = func(context.Context, uint) (EditCropResult, error) { cancel(); return EditCropResult{}, ctx.Err() }
	if e := s.process(ctx, v, false); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	var n int64
	database.DB.Model(&models.VideoCropDetectionState{}).Count(&n)
	if n != 0 {
		t.Fatal("cancel committed")
	}
}

func TestCropDetectionCommitFailureRollsBackDraft(t *testing.T) {
	s, v := cropFixture(t)
	name := "test:crop_detection_failure"
	callback := database.DB.Callback().Create()
	if e := callback.Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "video_edit_projects" {
			tx.AddError(errors.New("database failure"))
		}
	}); e != nil {
		t.Fatal(e)
	}
	defer callback.Remove(name)
	if e := s.process(context.Background(), v, false); e == nil {
		t.Fatal("missing DB error")
	}
	var n int64
	database.DB.Model(&models.VideoCropDetectionState{}).Count(&n)
	if n != 0 {
		t.Fatal("state committed without draft")
	}
	database.DB.Model(&models.VideoEditProject{}).Count(&n)
	if n != 0 {
		t.Fatal("orphan draft")
	}
}
func TestCropDetectionChangedVersionKeepsEditedDraft(t *testing.T) {
	s, v := cropFixture(t)
	if e := s.process(context.Background(), v, false); e != nil {
		t.Fatal(e)
	}
	var first models.VideoEditProject
	database.DB.First(&first)
	database.DB.Model(&first).Update("title", "user title")
	if e := os.WriteFile(v.Path, []byte("new source version"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := s.process(context.Background(), v, false); e != nil {
		t.Fatal(e)
	}
	var count int64
	database.DB.Model(&models.VideoEditProject{}).Count(&count)
	if count != 2 {
		t.Fatal("new version missing", count)
	}
	database.DB.First(&first, first.ID)
	if first.Title != "user title" {
		t.Fatal("overwrote user draft")
	}
}

func TestCropDetectionCompetingStateDoesNotCreateOrphan(t *testing.T) {
	s, v := cropFixture(t)
	s.edit.SetEventEmitter(func(VideoEditStateEvent) { t.Fatal("CAS loser emitted draft event") })
	original := s.detect
	s.detect = func(ctx context.Context, id uint) (EditCropResult, error) {
		r, e := original(ctx, id)
		row := models.VideoCropDetectionState{VideoID: id, SourceVersion: r.SourceVersion, AlgorithmVersion: 1, Revision: 1, Status: "no_borders", ResultJSON: "{}"}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		return r, e
	}
	if e := s.process(context.Background(), v, false); e != nil {
		t.Fatal(e)
	}
	var n int64
	database.DB.Model(&models.VideoEditProject{}).Count(&n)
	if n != 0 {
		t.Fatal("lost unique insert still created draft")
	}
}
func TestCropDetectionEmptyAndStopResumeLifecycle(t *testing.T) {
	s, v := cropFixture(t)
	database.DB.Delete(&v)
	if _, e := s.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	s.StopAndWait()
	if s.Status().Running {
		t.Fatal("worker not stopped")
	}
	s.Resume()
	if _, e := s.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	s.StopAndWait()
}

func TestCropDetectionRevisionConflictDoesNotOverwriteWinner(t *testing.T) {
	s, v := cropFixture(t)
	size, mtime, _ := editFileFingerprint(v.Path)
	old := models.VideoCropDetectionState{VideoID: v.ID, SourceVersion: editCropSourceVersion(size, mtime), AlgorithmVersion: 1, Revision: 1, Status: "failed", ResultJSON: "{}"}
	if e := database.DB.Create(&old).Error; e != nil {
		t.Fatal(e)
	}
	original := s.detect
	s.detect = func(ctx context.Context, id uint) (EditCropResult, error) {
		r, e := original(ctx, id)
		if err := database.DB.Model(&models.VideoCropDetectionState{}).Where("video_id = ?", id).Updates(map[string]any{"revision": 2, "status": "no_borders"}).Error; err != nil {
			t.Fatal(err)
		}
		return r, e
	}
	if e := s.process(context.Background(), v, true); e != nil {
		t.Fatal(e)
	}
	var got models.VideoCropDetectionState
	database.DB.First(&got, "video_id = ?", v.ID)
	if got.Revision != 2 || got.Status != "no_borders" {
		t.Fatal("overwrote winning revision", got)
	}
	var n int64
	database.DB.Model(&models.VideoEditProject{}).Count(&n)
	if n != 0 {
		t.Fatal("CAS loser created orphan project")
	}
}

type cropTestPause struct {
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
	enteredOnce sync.Once
}

func (h *cropTestPause) Wait(ctx context.Context, notify func(TaskGateState)) error {
	h.enteredOnce.Do(func() { close(h.entered) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.release:
		return nil
	}
}
func (h *cropTestPause) Release() { h.once.Do(func() { close(h.release) }) }
func TestCropDetectionManualStartReleasesAutomaticPause(t *testing.T) {
	s, _ := cropFixture(t)
	original := s.detect
	detectEntered := make(chan struct{})
	finish := make(chan struct{})
	var finishOnce sync.Once
	var detectOnce sync.Once
	release := func() { finishOnce.Do(func() { close(finish) }) }
	t.Cleanup(func() { release(); s.StopAndWait() })
	s.detect = func(ctx context.Context, id uint) (EditCropResult, error) {
		detectOnce.Do(func() { close(detectEntered) })
		select {
		case <-ctx.Done():
			return EditCropResult{}, ctx.Err()
		case <-finish:
			return original(ctx, id)
		}
	}
	h := &cropTestPause{entered: make(chan struct{}), release: make(chan struct{})}
	if _, e := s.StartWithPauseHook(context.Background(), h); e != nil {
		t.Fatal(e)
	}
	select {
	case <-h.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("automatic pause not entered")
	}
	if _, e := s.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	select {
	case <-detectEntered:
	case <-time.After(time.Second):
		t.Fatal("manual start did not release gate")
	}
	incoming := &cropTestPause{entered: make(chan struct{}), release: make(chan struct{})}
	if _, e := s.StartWithPauseHook(context.Background(), incoming); e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	hook := s.pauseHook
	s.mu.Unlock()
	if hook != nil {
		t.Fatal("auto wake attached gate to manual run")
	}
	release()
	s.StopAndWait()
	if _, e := s.Start(context.Background()); e == nil {
		t.Fatal("stopped service reopened without Resume")
	}
	s.Resume()
	if _, e := s.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	s.StopAndWait()
}

func TestCropDetectionSourceQueryFailureStopsWithoutFailedState(t *testing.T) {
	s, first := cropFixture(t)
	s.edit.findFFprobe = func() (string, error) { return "ffprobe", nil }
	s.edit.findFFmpeg = func() (string, error) { return "ffmpeg", nil }
	makeVideo := func(name string) models.Video {
		path := filepath.Join(filepath.Dir(first.Path), name)
		if e := os.WriteFile(path, []byte("source"), 0600); e != nil {
			t.Fatal(e)
		}
		v := models.Video{Name: name, Path: path, Directory: filepath.Dir(path), Size: 6}
		if e := database.DB.Create(&v).Error; e != nil {
			t.Fatal(e)
		}
		return v
	}
	second, third := makeVideo("second.mp4"), makeVideo("third.mp4")
	sentinel := errors.New("injected source lookup failure")
	hookName := "test:crop_source_query_failure"
	if e := database.DB.Callback().Query().Before("gorm:query").Register(hookName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "videos" {
			return
		}
		where, ok := tx.Statement.Clauses["WHERE"].Expression.(clause.Where)
		if !ok {
			return
		}
		for _, expression := range where.Exprs {
			expr, ok := expression.(clause.Expr)
			if !ok || expr.SQL != "id IN ?" || len(expr.Vars) != 1 {
				continue
			}
			ids, ok := expr.Vars[0].([]uint)
			if ok && len(ids) == 1 && ids[0] == second.ID {
				tx.AddError(sentinel)
			}
		}
	}); e != nil {
		t.Fatal(e)
	}
	defer database.DB.Callback().Query().Remove(hookName)
	firstDetect := s.detect
	thirdCalls := 0
	s.detect = func(ctx context.Context, id uint) (EditCropResult, error) {
		if id == first.ID {
			return firstDetect(ctx, id)
		}
		if id == third.ID {
			thirdCalls++
			return firstDetect(ctx, id)
		}
		return s.detectVideo(ctx, id)
	}
	s.run(context.Background(), nil, false)
	var states []models.VideoCropDetectionState
	if e := database.DB.Find(&states).Error; e != nil {
		t.Fatal(e)
	}
	if len(states) != 1 || states[0].VideoID != first.ID || states[0].Status != "detected" {
		t.Fatal("DB failure committed media failure or lost previous result", states)
	}
	if thirdCalls != 0 {
		t.Fatal("DB failure did not stop the batch")
	}
	var projects int64
	database.DB.Model(&models.VideoEditProject{}).Count(&projects)
	if projects != 1 {
		t.Fatal("DB failure created orphan or removed previous draft", projects)
	}
	if status := s.Status(); status.Failed == 0 || len(status.Failures) == 0 {
		t.Fatal("batch failure not observable", status)
	}
}

func TestCropDetectionNotifiesEditOwnerOnlyAfterCommittedNewDraft(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			s, v := cropFixture(t)
			events := []VideoEditStateEvent{}
			s.edit.SetEventEmitter(func(event VideoEditStateEvent) {
				var visible models.VideoEditProject
				if e := database.DB.First(&visible, event.ProjectID).Error; e != nil {
					t.Fatal("notified before draft was committed", e)
				}
				var state models.VideoCropDetectionState
				if e := database.DB.First(&state, "video_id = ?", v.ID).Error; e != nil || state.ProjectID == nil || *state.ProjectID != event.ProjectID {
					t.Fatal("notification preceded delivery link", state, e)
				}
				events = append(events, event)
			})
			hookName := "test:crop_notify_rollback"
			if fail {
				if e := database.DB.Callback().Create().Before("gorm:create").Register(hookName, func(tx *gorm.DB) {
					if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "video_edit_projects" {
						tx.AddError(errors.New("draft transaction failed"))
					}
				}); e != nil {
					t.Fatal(e)
				}
				defer database.DB.Callback().Create().Remove(hookName)
			}
			e := s.process(context.Background(), v, false)
			if fail {
				if e == nil || len(events) != 0 {
					t.Fatal("rolled-back draft emitted event", e, events)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if len(events) != 1 || events[0].ProjectID == 0 || events[0].Status != models.VideoEditStatusDraft {
				t.Fatal("new committed draft notification missing", events)
			}
			if e = s.process(context.Background(), v, false); e != nil {
				t.Fatal(e)
			}
			if len(events) != 1 {
				t.Fatal("same version notified twice", events)
			}
		})
	}
}
