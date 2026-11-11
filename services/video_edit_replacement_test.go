package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"
)

func replacementFixture(t *testing.T) (*VideoEditService, models.VideoEditItem, editPlan, string) {
	t.Helper()
	setupVideoServiceTestDB(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "original.avi")
	if err = os.WriteFile(path, []byte("original source retained until commit"), 0644); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	video := models.Video{Name: "original.avi", Path: path, Directory: dir, Size: st.Size(), Description: "manual", IsFavorite: true, PlayCount: 7, Duration: 10}
	if err = database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	project := models.VideoEditProject{Kind: "crop", Title: "replace", Status: models.VideoEditStatusRunning, Revision: 1, Mode: "precise", RecipeJSON: "{}", AnalysisJSON: "{}", AcknowledgedJSON: "[]"}
	if err = database.DB.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	sha, _ := replacementSHA(path)
	plan := editPlan{V: 1, Kind: "crop", PublishMode: "replace_source", OutputDir: dir, OutputBase: "original", OutputExt: ".avi", DurationMS: 10000, Video: editPlanVideo{Width: 256, Height: 176}, Sources: []editPlanSource{{VideoID: video.ID, Path: path, Size: st.Size(), RecordSize: video.Size, ModTimeNS: st.ModTime().UnixNano(), UpdatedAt: video.UpdatedAt, SHA256: sha}}}
	raw, _ := json.Marshal(plan)
	item := models.VideoEditItem{ProjectID: project.ID, Seq: 1, Status: models.VideoEditStatusRunning, Phase: models.VideoEditPhasePublish, PlanJSON: string(raw), OutputName: "original.avi"}
	if err = database.DB.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	item.WorkDir = editWorkdirFor(plan, item.ID)
	if err = os.Mkdir(item.WorkDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = database.DB.Model(&item).Update("work_dir", item.WorkDir).Error; err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(item.WorkDir, "final.part")
	if err = os.WriteFile(staged, []byte("verified encoded crop"), 0644); err != nil {
		t.Fatal(err)
	}
	s := NewVideoEditService(nil, NewMediaWorkSlot(), dir)
	s.diskFree = func(string) (uint64, error) { return 1 << 30, nil }
	t.Cleanup(s.StopAndWait)
	return s, item, plan, staged
}
func TestEditReplacementSameIdentityAndManualMetadata(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	if err := s.publishEditReplacement(context.Background(), item, plan, staged); err != nil {
		t.Fatal(err)
	}
	var got models.Video
	if err := database.DB.First(&got, plan.Sources[0].VideoID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Name != "original.avi" || got.Path != plan.Sources[0].Path || got.Description != "manual" || !got.IsFavorite || got.PlayCount != 7 || got.Width != 256 {
		t.Fatalf("source identity/metadata changed: %+v", got)
	}
	var count int64
	database.DB.Model(&models.Video{}).Count(&count)
	if count != 1 {
		t.Fatalf("new source row created %d", count)
	}
	current, err := loadEditItemByID(context.Background(), item.ID)
	if err != nil || current.OutputVideoID == nil || *current.OutputVideoID != got.ID || current.Status != models.VideoEditStatusCompleted {
		t.Fatal(current, err)
	}
	if replacementJournalExists(item.WorkDir) {
		t.Fatal("completed journal retained")
	}
}
func TestEditReplacementRollbackAndCrashWindows(t *testing.T) {
	for _, phase := range []string{"prepared", "backup_directory_synced", "backed_up", "installed", "committed"} {
		t.Run(phase, func(t *testing.T) {
			s, item, plan, staged := replacementFixture(t)
			s.replacementHook = func(p string) error {
				if p == phase {
					return errors.New("simulated process loss")
				}
				return nil
			}
			if err := s.publishEditReplacement(context.Background(), item, plan, staged); VideoEditErrorCode(err) != "replace_recovery_pending" {
				t.Fatal(err)
			}
			if !replacementJournalExists(item.WorkDir) {
				t.Fatal("crash evidence removed")
			}
			s.replacementHook = nil
			if _, err := s.RetryReplacementRecovery(context.Background(), item.ID); err != nil {
				t.Fatal(err)
			}
			current, _ := loadEditItemByID(context.Background(), item.ID)
			if phase == "prepared" || phase == "backup_directory_synced" || phase == "backed_up" {
				if !replacementHasSHA(plan.Sources[0].Path, plan.Sources[0].SHA256) {
					t.Fatal("old source not restored")
				}
			} else if current.Status != models.VideoEditStatusCompleted {
				t.Fatal("installed output not committed")
			}
		})
	}
}
func TestEditReplacementDatabaseFailureRestoresSource(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	s.publishTxHook = func(*gorm.DB) error { return errors.New("transaction failed") }
	if err := s.publishEditReplacement(context.Background(), item, plan, staged); err == nil {
		t.Fatal("missing transaction error")
	}
	if !replacementHasSHA(plan.Sources[0].Path, plan.Sources[0].SHA256) {
		t.Fatal("source lost on DB rollback")
	}
	var video models.Video
	database.DB.First(&video, plan.Sources[0].VideoID)
	if video.Width != 0 || video.Description != "manual" {
		t.Fatal("partial database commit")
	}
}
func TestEditReplacementRejectsChangedSourceAndDiskFull(t *testing.T) {
	for _, kind := range []string{"source", "space"} {
		t.Run(kind, func(t *testing.T) {
			s, item, plan, staged := replacementFixture(t)
			if kind == "source" {
				os.WriteFile(plan.Sources[0].Path, []byte("external edit"), 0600)
			} else {
				s.diskFree = func(string) (uint64, error) { return 0, nil }
			}
			if err := s.publishEditReplacement(context.Background(), item, plan, staged); err == nil {
				t.Fatal("unsafe publish succeeded")
			}
			if replacementJournalExists(item.WorkDir) {
				t.Fatal("source touched before admission failed")
			}
		})
	}
}
func TestEditReplacementExternalConflictKeepsBothFiles(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	s.replacementHook = func(p string) error {
		if p == "backed_up" {
			if e := os.WriteFile(plan.Sources[0].Path, []byte("external owner"), 0600); e != nil {
				return e
			}
		}
		return nil
	}
	if err := s.publishEditReplacement(context.Background(), item, plan, staged); VideoEditErrorCode(err) != "replace_recovery_pending" {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(plan.Sources[0].Path)
	if string(got) != "external owner" {
		t.Fatal("external file overwritten")
	}
	if !replacementHasSHA(filepath.Join(item.WorkDir, "original.backup"), plan.Sources[0].SHA256) {
		t.Fatal("backup lost")
	}
	removeEditWorkdir(item.WorkDir)
	if !replacementJournalExists(item.WorkDir) {
		t.Fatal("generic cleanup deleted unique backup")
	}
}
func TestEditReplacementJournalRejectsOtherSource(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	s.replacementHook = func(p string) error {
		if p == "prepared" {
			return errors.New("crash")
		}
		return nil
	}
	_ = s.publishEditReplacement(context.Background(), item, plan, staged)
	j, err := readReplacementJournal(item, plan)
	if err != nil {
		t.Fatal(err)
	}
	j.Source.Path = filepath.Join(plan.OutputDir, "unrelated.avi")
	writeReplacementJournal(item.WorkDir, j)
	if _, err = s.RetryReplacementRecovery(context.Background(), item.ID); VideoEditErrorCode(err) != "replace_recovery_pending" {
		t.Fatal("untrusted journal accepted", err)
	}
}

func TestEditReplacementCopyFallbackAndPartialRestore(t *testing.T) {
	for _, point := range []string{"", "copying_created", "restoring_created"} {
		t.Run(point, func(t *testing.T) {
			s, item, plan, staged := replacementFixture(t)
			s.replacementRename = func(string, string) error { return errReplacementNoReplace }
			fired := false
			s.replacementHook = func(p string) error {
				if p == point && !fired {
					fired = true
					return errors.New("simulated power loss")
				}
				return nil
			}
			if point == "restoring_created" {
				s.publishTxHook = func(*gorm.DB) error { return errors.New("database unavailable") }
			}
			err := s.publishEditReplacement(context.Background(), item, plan, staged)
			if point == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("injected failure ignored")
			}
			s.replacementHook = nil
			s.publishTxHook = nil
			if replacementJournalExists(item.WorkDir) {
				if _, e := s.RetryReplacementRecovery(context.Background(), item.ID); e != nil {
					t.Fatal(e)
				}
			}
			if point == "copying_created" || point == "restoring_created" {
				if !replacementHasSHA(plan.Sources[0].Path, plan.Sources[0].SHA256) {
					t.Fatal("partial copy/restore destroyed old source")
				}
			}
		})
	}
}

func TestEditReplacementRecoveryLostCommitReplyNeverRestoresOldFile(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	s.replacementHook = func(p string) error {
		if p == "installed" {
			return errors.New("crash before commit")
		}
		return nil
	}
	if err := s.publishEditReplacement(context.Background(), item, plan, staged); err == nil {
		t.Fatal("crash missing")
	}
	j, err := readReplacementJournal(item, plan)
	if err != nil {
		t.Fatal(err)
	}
	s.replacementHook = nil
	s.replacementCommitReply = func(err error) error {
		if err != nil {
			return err
		}
		return errors.New("committed response lost")
	}
	if _, err = s.RetryReplacementRecovery(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	if !replacementHasSHA(plan.Sources[0].Path, j.NewSHA) {
		t.Fatal("successful commit was rolled back on disk")
	}
	current, err := loadEditItemByID(context.Background(), item.ID)
	if err != nil || current.Status != models.VideoEditStatusCompleted {
		t.Fatal(current, err)
	}
}

func TestEditReplacementCompletedCleanupFailureCanRetry(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	s.replacementHook = func(p string) error {
		if p == "cleanup" {
			return errors.New("cleanup unavailable")
		}
		return nil
	}
	err := s.publishEditReplacement(context.Background(), item, plan, staged)
	if err == nil {
		t.Fatal("cleanup failure missing")
	}
	s.finishItem(context.Background(), item, err)
	current, e := loadEditItemByID(context.Background(), item.ID)
	if e != nil || current.Status != models.VideoEditStatusCompleted || current.ErrorCode != "replace_recovery_pending" || current.WorkDir == "" {
		t.Fatal("completed recovery state lost", current, e)
	}
	s.replacementHook = nil
	if _, e = s.RetryReplacementRecovery(context.Background(), item.ID); e != nil {
		t.Fatal(e)
	}
	current, e = loadEditItemByID(context.Background(), item.ID)
	if e != nil || current.WorkDir != "" || current.ErrorCode != "" || current.Status != models.VideoEditStatusCompleted {
		t.Fatal(current, e)
	}
}
func TestEditReplacementRollbackPreservesFingerprintAndPermissions(t *testing.T) {
	for _, copyRestore := range []bool{false, true} {
		t.Run(fmt.Sprint(copyRestore), func(t *testing.T) {
			s, item, plan, staged := replacementFixture(t)
			before, _ := os.Stat(plan.Sources[0].Path)
			if copyRestore {
				s.replacementRename = func(string, string) error { return errReplacementNoReplace }
			}
			s.publishTxHook = func(*gorm.DB) error { return errors.New("fail transaction") }
			if e := s.publishEditReplacement(context.Background(), item, plan, staged); e == nil {
				t.Fatal("failure missing")
			}
			after, e := os.Stat(plan.Sources[0].Path)
			if e != nil {
				t.Fatal(e)
			}
			if before.Size() != after.Size() || before.ModTime().UnixNano() != after.ModTime().UnixNano() || before.Mode().Perm() != after.Mode().Perm() || !replacementHasSHA(plan.Sources[0].Path, plan.Sources[0].SHA256) {
				t.Fatal("rollback changed source fingerprint or permissions")
			}
		})
	}
}

func TestEditReplacementSourceSymlinkConflictKeepsUniqueBackup(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	s.replacementHook = func(p string) error {
		if p == "backed_up" {
			return os.Symlink(filepath.Join(item.WorkDir, "original.backup"), plan.Sources[0].Path)
		}
		return nil
	}
	if err := s.publishEditReplacement(context.Background(), item, plan, staged); VideoEditErrorCode(err) != "replace_recovery_pending" {
		t.Fatal(err)
	}
	if !replacementHasSHA(filepath.Join(item.WorkDir, "original.backup"), plan.Sources[0].SHA256) {
		t.Fatal("symlink conflict deleted backup")
	}
	if _, err := s.RetryReplacementRecovery(context.Background(), item.ID); VideoEditErrorCode(err) != "replace_recovery_pending" {
		t.Fatal(err)
	}
}
func TestEditReplacementRepeatedRestoreInterruptionCanRecover(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	s.replacementRename = func(string, string) error { return errReplacementNoReplace }
	s.publishTxHook = func(*gorm.DB) error { return errors.New("force restore") }
	s.replacementHook = func(p string) error {
		if p == "restoring_created" {
			return errors.New("power loss")
		}
		return nil
	}
	_ = s.publishEditReplacement(context.Background(), item, plan, staged)
	for i := 0; i < 2; i++ {
		if _, err := s.RetryReplacementRecovery(context.Background(), item.ID); VideoEditErrorCode(err) != "replace_recovery_pending" {
			t.Fatal("restore unexpectedly completed", err)
		}
	}
	s.replacementHook = nil
	s.publishTxHook = nil
	if _, err := s.RetryReplacementRecovery(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	if !replacementHasSHA(plan.Sources[0].Path, plan.Sources[0].SHA256) {
		t.Fatal("old source not restored")
	}
}

func TestEditReplacementPendingRestoreClearsActualInterruptedState(t *testing.T) {
	for _, phase := range []string{"prepared", "backed_up", "restoring_created"} {
		t.Run(phase, func(t *testing.T) {
			s, item, plan, staged := replacementFixture(t)
			if phase == "restoring_created" {
				s.replacementRename = func(string, string) error { return errReplacementNoReplace }
				s.publishTxHook = func(*gorm.DB) error { return errors.New("force rollback") }
			}
			s.replacementHook = func(p string) error {
				if p == phase {
					return errors.New("power loss")
				}
				return nil
			}
			err := s.publishEditReplacement(context.Background(), item, plan, staged)
			if err == nil {
				t.Fatal("failure missing")
			}
			s.finishItem(context.Background(), item, err)
			before, _ := loadEditItemByID(context.Background(), item.ID)
			if before.Status != models.VideoEditStatusInterrupted {
				t.Fatal("missing pending transition", before)
			}
			s.replacementHook = nil
			s.publishTxHook = nil
			if _, err = s.RetryReplacementRecovery(context.Background(), item.ID); err != nil {
				t.Fatal(err)
			}
			after, _ := loadEditItemByID(context.Background(), item.ID)
			if after.ErrorCode != "" || after.WorkDir != "" || after.Status != models.VideoEditStatusInterrupted {
				t.Fatal("interrupted cleanup stuck", after)
			}
			if replacementJournalExists(item.WorkDir) {
				t.Fatal("journal not cleaned after confirmed DB clear")
			}
		})
	}
}
func TestEditReplacementCleanupDBFailurePreservesRecoverableJournal(t *testing.T) {
	for _, phase := range []string{"cleanup_db", "cleanup_db_cleared"} {
		t.Run(phase, func(t *testing.T) {
			s, item, plan, staged := replacementFixture(t)
			s.replacementHook = func(p string) error {
				if p == phase {
					return errors.New("cleanup SQL response or crash")
				}
				return nil
			}
			err := s.publishEditReplacement(context.Background(), item, plan, staged)
			if err == nil {
				t.Fatal("failure missing")
			}
			if !replacementJournalExists(item.WorkDir) {
				t.Fatal("DB cleanup lost journal evidence")
			}
			s.finishItem(context.Background(), item, err)
			s.replacementHook = nil
			if _, err = s.RetryReplacementRecovery(context.Background(), item.ID); err != nil {
				t.Fatal(err)
			}
			after, _ := loadEditItemByID(context.Background(), item.ID)
			if after.Status != models.VideoEditStatusCompleted || after.WorkDir != "" || after.ErrorCode != "" {
				t.Fatal(after)
			}
		})
	}
}

func TestEditReplacementCleanupUpdateErrorKeepsEvidence(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	name := "test:replacement_cleanup_update_error"
	hook := database.DB.Callback().Update()
	if err := hook.Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "video_edit_items" {
			return
		}
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			if v, exists := values["work_dir"]; exists && v == "" {
				tx.AddError(errors.New("cleanup SQL failed"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	err := s.publishEditReplacement(context.Background(), item, plan, staged)
	hook.Remove(name)
	if err == nil || !replacementJournalExists(item.WorkDir) {
		t.Fatal("SQL clear failed without evidence", err)
	}
	if _, err = s.RetryReplacementRecovery(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := loadEditItemByID(context.Background(), item.ID)
	if after.Status != models.VideoEditStatusCompleted || after.WorkDir != "" || after.ErrorCode != "" {
		t.Fatal(after)
	}
}

func TestEditReplacementRetryAfterCleanupIsIdempotent(t *testing.T) {
	for _, phase := range []string{"prepared", "installed"} {
		t.Run(phase, func(t *testing.T) {
			s, item, plan, staged := replacementFixture(t)
			s.replacementHook = func(p string) error {
				if p == phase {
					return errors.New("crash")
				}
				return nil
			}
			err := s.publishEditReplacement(context.Background(), item, plan, staged)
			if err == nil {
				t.Fatal("crash not injected")
			}
			s.finishItem(context.Background(), item, err)
			s.replacementHook = nil
			if _, err = s.RetryReplacementRecovery(context.Background(), item.ID); err != nil {
				t.Fatal(err)
			}
			before, _ := loadEditItemByID(context.Background(), item.ID)
			bytesBefore, _ := os.ReadFile(plan.Sources[0].Path)
			if _, err = s.RetryReplacementRecovery(context.Background(), item.ID); err != nil {
				t.Fatal("duplicate retry not idempotent", err)
			}
			after, _ := loadEditItemByID(context.Background(), item.ID)
			bytesAfter, _ := os.ReadFile(plan.Sources[0].Path)
			if before.Status != after.Status || after.WorkDir != "" || after.ErrorCode != "" || string(bytesAfter) != string(bytesBefore) {
				t.Fatal("duplicate retry changed recovered item", before, after)
			}
			if editReplacementPathPending(database.DB, plan.Sources[0].Path) {
				t.Fatal("duplicate retry recreated source fence")
			}
		})
	}
}
func TestEditReplacementRetryRejectsOtherItemWithoutMutation(t *testing.T) {
	for _, kind := range []string{"merge", "crop"} {
		t.Run(kind, func(t *testing.T) {
			s, item, plan, staged := replacementFixture(t)
			plan.Kind = kind
			plan.PublishMode = "save_copy"
			raw, _ := json.Marshal(plan)
			if err := database.DB.Model(&models.VideoEditItem{}).Where("id = ?", item.ID).Updates(map[string]any{"plan_json": string(raw), "status": models.VideoEditStatusCompleted, "error_code": "", "work_dir": ""}).Error; err != nil {
				t.Fatal(err)
			}
			before, _ := loadEditItemByID(context.Background(), item.ID)
			sourceBefore, _ := os.ReadFile(plan.Sources[0].Path)
			stagedBefore, _ := os.ReadFile(staged)
			if _, err := s.RetryReplacementRecovery(context.Background(), item.ID); err == nil {
				t.Fatal("non-replacement accepted")
			}
			after, _ := loadEditItemByID(context.Background(), item.ID)
			sourceAfter, _ := os.ReadFile(plan.Sources[0].Path)
			stagedAfter, _ := os.ReadFile(staged)
			if before.Status != after.Status || before.ErrorCode != after.ErrorCode || before.WorkDir != after.WorkDir || string(sourceBefore) != string(sourceAfter) || string(stagedBefore) != string(stagedAfter) {
				t.Fatal("non-replacement item was mutated", before, after)
			}
		})
	}
}

func TestEditReplacementDBClearWindowKeepsProjectAuthority(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	s.replacementHook = func(p string) error {
		if p == "prepared" {
			return errors.New("crash")
		}
		return nil
	}
	err := s.publishEditReplacement(context.Background(), item, plan, staged)
	s.finishItem(context.Background(), item, err)
	s.replacementHook = func(p string) error {
		if p == "cleanup_db_cleared" {
			return errors.New("crash after DB clear")
		}
		return nil
	}
	if _, err = s.RetryReplacementRecovery(context.Background(), item.ID); err == nil {
		t.Fatal("cleanup crash missing")
	}
	// Recreate the exact crash window without the live request's compensating pending write.
	if err = database.DB.Model(&models.VideoEditItem{}).Where("id = ?", item.ID).Updates(map[string]any{"work_dir": "", "error_code": "", "error_message": "", "status": models.VideoEditStatusInterrupted}).Error; err != nil {
		t.Fatal(err)
	}
	if err = database.DB.Model(&models.VideoEditProject{}).Where("id = ?", item.ProjectID).Update("status", models.VideoEditStatusInterrupted).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteProject(context.Background(), item.ProjectID); VideoEditErrorCode(err) != "replace_recovery_pending" {
		t.Fatal("delete bypassed derived journal", err)
	}
	if _, err = s.RequeueProject(context.Background(), EditQueueRequest{ProjectID: item.ProjectID, ExpectedRevision: 1}); VideoEditErrorCode(err) != "replace_recovery_pending" {
		t.Fatal("requeue bypassed derived journal", err)
	}
	s.finishItem(context.Background(), item, replacementPending())
	current, _ := loadEditItemByID(context.Background(), item.ID)
	if current.ErrorCode != "replace_recovery_pending" || current.WorkDir == "" {
		t.Fatal("finish missed derived journal", current)
	}
	s.replacementHook = nil
	if _, err = s.RetryReplacementRecovery(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteProject(context.Background(), item.ProjectID); err != nil {
		t.Fatal("clean project cannot be deleted", err)
	}
}
func TestEditReplacementPendingSourceCannotEnterAnotherEdit(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	s.replacementHook = func(p string) error {
		if p == "backed_up" {
			return errors.New("crash")
		}
		return nil
	}
	_ = s.publishEditReplacement(context.Background(), item, plan, staged)
	pre := &EditPreflight{}
	if _, err := s.collectEditSources(context.Background(), pre, []uint{plan.Sources[0].VideoID}); err != nil {
		t.Fatal(err)
	}
	if len(pre.Errors) == 0 || pre.Errors[0].Code != "replace_recovery_pending" {
		t.Fatal("another edit consumed pending source", pre)
	}
}
