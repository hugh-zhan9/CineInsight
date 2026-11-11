package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"video-master/database"
	"video-master/models"
)

// Startup recovery must rely on the persisted item/plan/journal, not the old service's hooks or memory.
func TestEditReplacementNewServiceStartupCompletesInstalledJournal(t *testing.T) {
	old, item, plan, staged := replacementFixture(t)
	expectedSHA, err := replacementSHA(staged)
	if err != nil {
		t.Fatal(err)
	}
	old.replacementHook = func(phase string) error {
		if phase == "installed" {
			return errors.New("process stopped after installing output")
		}
		return nil
	}
	if err = old.publishEditReplacement(context.Background(), item, plan, staged); VideoEditErrorCode(err) != "replace_recovery_pending" {
		t.Fatal(err)
	}
	if !replacementJournalExists(item.WorkDir) {
		t.Fatal("installed crash did not leave a journal")
	}
	old.StopAndWait()

	restarted := NewVideoEditService(nil, NewMediaWorkSlot(), old.dataDir)
	t.Cleanup(restarted.StopAndWait)
	var encodes atomic.Int32
	restarted.runProgress = func(context.Context, string, []string, func(int64)) (string, error) {
		encodes.Add(1)
		return "", errors.New("startup must not re-encode")
	}
	restarted.RecoverOnStartup(context.Background())

	current, err := loadEditItemByID(context.Background(), item.ID)
	if err != nil || current.Status != models.VideoEditStatusCompleted || current.OutputVideoID == nil || *current.OutputVideoID != plan.Sources[0].VideoID {
		t.Fatal("new-service startup did not commit the installed output", current, err)
	}
	if current.WorkDir != "" || current.ErrorCode != "" || replacementJournalExists(item.WorkDir) {
		t.Fatal("startup did not finish journal cleanup", current)
	}
	if !replacementHasSHA(plan.Sources[0].Path, expectedSHA) {
		t.Fatal("startup replaced the installed output with old bytes")
	}
	var video models.Video
	if err = database.DB.First(&video, plan.Sources[0].VideoID).Error; err != nil {
		t.Fatal(err)
	}
	if video.Path != plan.Sources[0].Path || video.Name != "original.avi" || video.Width != plan.Video.Width {
		t.Fatal("startup changed original identity or missed metadata", video)
	}
	var count int64
	if err = database.DB.Model(&models.Video{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("startup created a second video", count, err)
	}
	if encodes.Load() != 0 {
		t.Fatal("startup re-encoded a persisted output")
	}
}

func TestEditReplacementNewServiceStartupAndSweepKeepExternalConflict(t *testing.T) {
	old, item, plan, staged := replacementFixture(t)
	old.replacementHook = func(phase string) error {
		if phase == "backed_up" {
			return errors.New("process stopped after preserving old file")
		}
		return nil
	}
	if err := old.publishEditReplacement(context.Background(), item, plan, staged); VideoEditErrorCode(err) != "replace_recovery_pending" {
		t.Fatal(err)
	}
	old.StopAndWait()
	external := []byte("external file created while application was stopped")
	if err := os.WriteFile(plan.Sources[0].Path, external, 0600); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: plan.OutputDir}).Error; err != nil {
		t.Fatal(err)
	}
	// A real orphan proves the sweep visits the seeded root while preserving the journal-bearing directory.
	orphan := filepath.Join(plan.OutputDir, ".cineinsight-edit-999999")
	if err := os.Mkdir(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "owned-orphan"), []byte("temporary"), 0600); err != nil {
		t.Fatal(err)
	}

	restarted := NewVideoEditService(nil, NewMediaWorkSlot(), old.dataDir)
	t.Cleanup(restarted.StopAndWait)
	restarted.RecoverOnStartup(context.Background())
	removed, err := restarted.SweepOrphanWorkdirs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("sweep did not visit exactly the disposable orphan: %d", removed)
	}
	if _, err = os.Lstat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("control orphan was not swept", err)
	}
	current, err := loadEditItemByID(context.Background(), item.ID)
	if err != nil || current.ErrorCode != "replace_recovery_pending" || current.WorkDir != item.WorkDir || current.Status == models.VideoEditStatusCompleted {
		t.Fatal("startup lost pending replacement ownership", current, err)
	}
	if !replacementJournalExists(item.WorkDir) || !replacementHasSHA(filepath.Join(item.WorkDir, "original.backup"), plan.Sources[0].SHA256) {
		t.Fatal("startup or sweep removed the unique backup/journal")
	}
	actual, err := os.ReadFile(plan.Sources[0].Path)
	if err != nil || string(actual) != string(external) {
		t.Fatal("startup overwrote the external conflict", err)
	}
	if !editReplacementPathPending(database.DB, plan.Sources[0].Path) {
		t.Fatal("startup dropped the pending source fence")
	}
}
