package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"

	"gorm.io/gorm"
)

func newAppConsolidationFixture(t *testing.T, dataDir string) *App {
	t.Helper()
	a := &App{cleanupService: &services.CleanupService{}, videoService: services.NewVideoService(nil), settingsService: &services.SettingsService{}, backgroundTasks: services.NewBackgroundTaskRegistry()}
	a.cleanupService.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.configureCleanupConsolidation(dataDir)
	a.enableCleanupConsolidationStart()
	if a.consolidationLifecycle.initErr != nil {
		t.Fatal(a.consolidationLifecycle.initErr)
	}
	t.Cleanup(func() { _ = a.stopCleanupConsolidationAndWait() })
	return a
}

func appConsolidationRelease(t *testing.T) (chan struct{}, func()) {
	t.Helper()
	ch := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	return ch, release
}

func waitAppConsolidation(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("集中整理未在期限内完成")
		}
		time.Sleep(time.Millisecond)
	}
}

func createAppConsolidationPreview(t *testing.T, a *App) (*services.CleanupConsolidationPreview, models.Video, models.Video) {
	t.Helper()
	root, target := t.TempDir(), t.TempDir()
	videos := []models.Video{}
	for _, name := range []string{"a", "b"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "movie.mp4")
		if err := os.WriteFile(path, []byte("identical video content"), 0644); err != nil {
			t.Fatal(err)
		}
		video := models.Video{Path: path, Directory: dir, Name: "movie.mp4", Size: int64(len("identical video content")), Duration: 60, Resolution: "1920x1080", Width: 1920, Height: 1080}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		videos = append(videos, video)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", LibraryWatchEnabled: false}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := a.cleanupService.StartAnalysis(services.CleanupCriteria{}); err != nil {
		t.Fatal(err)
	}
	waitAppConsolidation(t, func() bool { return len(a.backgroundTasks.Snapshot()) == 0 })
	status := a.cleanupService.Status()
	if !status.Completed || status.Analysis == nil || len(status.Analysis.DuplicateGroups) != 1 {
		t.Fatalf("analysis: %+v", status)
	}
	ids := []uint{videos[0].ID, videos[1].ID}
	req := services.CleanupConsolidationRequest{Destination: target,
		Groups:      []services.CleanupConsolidationGroup{{Kind: "exact", MemberIDs: ids, KeeperID: videos[0].ID, KeeperPinned: true, SelectedIDs: ids[1:]}},
		Protections: []services.CleanupConsolidationProtection{{Kind: "exact", MemberIDs: ids, KeeperID: videos[0].ID, KeeperPinned: true}},
	}
	preview, err := a.PreviewCleanupConsolidation(req)
	if err != nil || preview == nil || preview.PreviewID == "" || len(preview.Errors) != 0 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	return preview, videos[0], videos[1]
}

func TestAppConsolidationBindingsMoveOnlyAndReopenScopedReview(t *testing.T) {
	setupAppTestDB(t)
	dataDir := t.TempDir()
	a := newAppConsolidationFixture(t, dataDir)
	if status, err := a.GetCleanupConsolidationStatus(0); err != nil || status != nil {
		t.Fatalf("empty latest: %+v %v", status, err)
	}
	if _, err := a.StartCleanupConsolidation("forged", false); err == nil {
		t.Fatal("start accepted without preview")
	}
	preview, keeper, duplicate := createAppConsolidationPreview(t, a)
	var eventsMu sync.Mutex
	var summaries []services.CleanupConsolidationSummary
	a.consolidationLifecycle.emit = func(event string, payload any) {
		if event != cleanupConsolidationProgressEvent {
			t.Errorf("event=%s", event)
			return
		}
		summary := payload.(services.CleanupConsolidationSummary)
		encoded, _ := json.Marshal(summary)
		if strings.Contains(string(encoded), "items") || strings.Contains(string(encoded), "owner_scope") || strings.Contains(string(encoded), "source") {
			t.Errorf("heavy/private summary: %s", encoded)
		}
		eventsMu.Lock()
		summaries = append(summaries, summary)
		eventsMu.Unlock()
	}
	started, err := a.StartCleanupConsolidation(preview.PreviewID, true)
	if err != nil {
		t.Fatal(err)
	}
	waitAppConsolidation(t, func() bool { return len(a.backgroundTasks.Snapshot()) == 0 })
	result, err := a.GetCleanupConsolidationStatus(started.ID)
	if err != nil || result.Status != "completed" || result.Completed != 1 || result.Items[0].VideoID != keeper.ID {
		t.Fatalf("result: %+v %v", result, err)
	}
	latest, err := a.GetCleanupConsolidationStatus(0)
	if err != nil || latest.ID != started.ID {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	if again, err := a.StartCleanupConsolidation(preview.PreviewID, false); err != nil || again.ID != started.ID {
		t.Fatalf("idempotent: %+v %v", again, err)
	}
	var count int64
	if err := database.DB.Model(&models.Video{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("migration deleted videos", count, err)
	}
	if _, err := os.Stat(duplicate.Path); err != nil {
		t.Fatal("migration removed unselected duplicate", err)
	}
	var moved models.Video
	if err := database.DB.First(&moved, keeper.ID).Error; err != nil || moved.Path != preview.Items[0].DestinationPath {
		t.Fatalf("path: %+v %v", moved, err)
	}
	if err := database.DB.Model(&models.ScanDirectory{}).Where("path = ?", preview.Destination).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("explicit scan root choice lost", count, err)
	}
	// Reopening uses persisted task identity and paths, not the old in-memory analysis.
	if err := a.stopCleanupConsolidationAndWait(); err != nil {
		t.Fatal(err)
	}
	reopened := newAppConsolidationFixture(t, dataDir)
	review, err := reopened.GetCleanupConsolidationReview(started.ID)
	if err != nil || review.TaskID != started.ID || len(review.Groups) != 1 || review.Groups[0].KeeperID != keeper.ID || !reflect.DeepEqual(review.Groups[0].SelectedIDs, []uint{duplicate.ID}) {
		t.Fatalf("review: %+v %v", review, err)
	}
	if len(review.Analysis.DuplicateGroups) != 1 {
		t.Fatal("lost task-specific analysis")
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	if len(summaries) == 0 || summaries[len(summaries)-1].Status != "completed" {
		t.Fatalf("missing terminal summary: %+v", summaries)
	}
}

func TestAppConsolidationUnavailableAndMaintenanceGuards(t *testing.T) {
	setupAppTestDB(t)
	a := &App{cleanupService: &services.CleanupService{}, videoService: services.NewVideoService(nil)}
	a.configureCleanupConsolidation("")
	if a.startupError != "" || a.cleanupConsolidationWarning() == "" {
		t.Fatal("feature error must not fail entire startup")
	}
	if _, err := a.PreviewCleanupConsolidation(services.CleanupConsolidationRequest{}); err == nil {
		t.Fatal("unconfigured preview accepted")
	}
	if _, err := a.StartCleanupConsolidation("bad", false); err == nil {
		t.Fatal("unconfigured start accepted")
	}
	if _, err := a.GetCleanupConsolidationStatus(0); err == nil {
		t.Fatal("unconfigured status accepted")
	}
	if _, err := a.GetCleanupConsolidationReview(1); err == nil {
		t.Fatal("unconfigured review accepted")
	}
	if err := (&App{}).CancelCleanupConsolidation(); err == nil {
		t.Fatal("missing service cancellation")
	}
	a = newAppConsolidationFixture(t, t.TempDir())
	release := database.BeginMaintenance()
	t.Cleanup(release)
	for name, read := range map[string]func() error{
		"preview": func() error { _, e := a.PreviewCleanupConsolidation(services.CleanupConsolidationRequest{}); return e },
		"start":   func() error { _, e := a.StartCleanupConsolidation("bad", false); return e },
		"status":  func() error { _, e := a.GetCleanupConsolidationStatus(0); return e },
		"review":  func() error { _, e := a.GetCleanupConsolidationReview(1); return e },
	} {
		if err := read(); err == nil || !strings.Contains(err.Error(), "数据库") {
			t.Errorf("%s maintenance error=%v", name, err)
		}
	}
	release()
	if _, err := a.PreviewCleanupConsolidation(services.CleanupConsolidationRequest{}); err == nil || !strings.Contains(err.Error(), "分析") {
		t.Fatalf("service error not preserved: %v", err)
	}
	if _, err := a.GetCleanupConsolidationStatus(999); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing task: %v", err)
	}
}

func TestAppConsolidationRecoveryWaitsBeforeMaintenanceAndResumes(t *testing.T) {
	setupAppTestDB(t)
	a := newAppConsolidationFixture(t, t.TempDir())
	slot := "video_cleanup"
	foreign := models.CleanupConsolidationTask{PreviewID: "foreign", OwnerScope: "another-owner", ActiveSlot: &slot, Status: "running", Total: 1}
	if err := database.DB.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	entered, cancelled := make(chan struct{}), make(chan struct{})
	release, releaseWait := appConsolidationRelease(t)
	var intercepted atomic.Bool
	callback := "test:consolidation_recovery_wait"
	if err := database.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "cleanup_consolidation_tasks" || !intercepted.CompareAndSwap(false, true) {
			return
		}
		close(entered)
		<-tx.Statement.Context.Done()
		close(cancelled)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.DB.Callback().Query().Remove(callback) })
	a.startCleanupConsolidationRecovery()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not start asynchronously")
	}
	if err := a.CancelCleanupConsolidation(); err != nil {
		t.Fatal(err)
	}
	<-cancelled
	maintenance := make(chan error, 1)
	go func() { maintenance <- a.enterDatabaseRestoreMode(false) }()
	waitAppConsolidation(t, func() bool {
		a.consolidationLifecycle.mu.Lock()
		defer a.consolidationLifecycle.mu.Unlock()
		return a.consolidationLifecycle.stopped
	})
	if database.MaintenanceActive() {
		t.Fatal("database fenced before recovery finished")
	}
	select {
	case err := <-maintenance:
		t.Fatalf("maintenance did not wait: %v", err)
	default:
	}
	if err := a.CancelCleanupConsolidation(); err != nil {
		t.Fatal("cancel must still reach local executor after admission closes", err)
	}
	if _, err := a.GetCleanupConsolidationStatus(0); err == nil || !strings.Contains(err.Error(), "停止接受") {
		t.Fatal("closed admission accepted new read or leaked service error", err)
	}
	a.startCleanupConsolidationRecovery() // closed admission must not queue late work
	releaseWait()
	if err := <-maintenance; err != nil {
		t.Fatal(err)
	}
	if !database.MaintenanceActive() {
		t.Fatal("maintenance fence missing")
	}
	var row models.CleanupConsolidationTask
	if err := database.WithMaintenanceAccess(database.DB).First(&row, foreign.ID).Error; err != nil || row.Status != "running" || row.ActiveSlot == nil {
		t.Fatalf("cancelled recovery changed foreign task: %+v %v", row, err)
	}
	a.resumeAfterDatabaseRestoreFailure() // ctx nil: exercises the shared recovery restart before optional services
	waitAppConsolidation(t, func() bool {
		a.consolidationLifecycle.mu.Lock()
		defer a.consolidationLifecycle.mu.Unlock()
		return !a.consolidationLifecycle.recovering
	})
	if database.MaintenanceActive() {
		t.Fatal("failure did not release fence")
	}
	if a.cleanupConsolidationWarning() == "" {
		t.Fatal("recovery failure was invisible")
	}
	if err := database.DB.First(&row, foreign.ID).Error; err != nil || row.Status != "running" || row.ActiveSlot == nil {
		t.Fatalf("foreign task taken over: %+v %v", row, err)
	}
	if _, done, err := a.beginCleanupConsolidation(); err != nil {
		t.Fatal("failure did not resume admission", err)
	} else {
		done()
	}
}

func TestAppConsolidationShutdownWaitsForAcceptedRead(t *testing.T) {
	setupAppTestDB(t)
	a := newAppConsolidationFixture(t, t.TempDir())
	entered := make(chan struct{})
	release, releaseWait := appConsolidationRelease(t)
	callback := "test:consolidation_status_wait"
	var once sync.Once
	if err := database.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "cleanup_consolidation_tasks" {
			once.Do(func() { close(entered); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.DB.Callback().Query().Remove(callback) })
	read := make(chan error, 1)
	go func() { _, err := a.GetCleanupConsolidationStatus(0); read <- err }()
	<-entered
	shutdown := make(chan struct{})
	t.Cleanup(func() { releasePlaybackAfterShutdownTest(a) })
	go func() { a.shutdown(context.Background()); close(shutdown) }()
	waitAppConsolidation(t, func() bool {
		a.consolidationLifecycle.mu.Lock()
		defer a.consolidationLifecycle.mu.Unlock()
		return a.consolidationLifecycle.stopped
	})
	select {
	case <-shutdown:
		t.Fatal("shutdown returned with database reader outstanding")
	default:
	}
	releaseWait()
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	select {
	case <-shutdown:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if _, err := a.GetCleanupConsolidationStatus(0); err == nil {
		t.Fatal("shutdown reopened admission")
	}
}

func TestAppConsolidationRecoveryOnlyReconcilesCommittedTask(t *testing.T) {
	setupAppTestDB(t)
	dataDir := t.TempDir()
	a := newAppConsolidationFixture(t, dataDir)
	preview, keeper, duplicate := createAppConsolidationPreview(t, a)
	started, err := a.StartCleanupConsolidation(preview.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	waitAppConsolidation(t, func() bool { return len(a.backgroundTasks.Snapshot()) == 0 })
	if err := a.stopCleanupConsolidationAndWait(); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the final item commit but before task finalization.
	if err := database.DB.Model(&models.CleanupConsolidationTask{}).Where("id = ?", started.ID).Updates(map[string]any{"status": "running", "active_slot": "video_cleanup", "finished_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	reopened := newAppConsolidationFixture(t, dataDir)
	reopened.startCleanupConsolidationRecovery()
	waitAppConsolidation(t, func() bool {
		reopened.consolidationLifecycle.mu.Lock()
		defer reopened.consolidationLifecycle.mu.Unlock()
		return !reopened.consolidationLifecycle.recovering
	})
	result, err := reopened.GetCleanupConsolidationStatus(started.ID)
	if err != nil || result.Status != "interrupted" || result.Completed != 1 {
		t.Fatalf("recovery: %+v %v", result, err)
	}
	if _, err := reopened.GetCleanupConsolidationReview(started.ID); err == nil {
		t.Fatal("interrupted task exposed deletion review")
	}
	for _, path := range []string{preview.Items[0].DestinationPath, duplicate.Path} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("recovery deleted retained file", path, err)
		}
	}
	var got models.Video
	if err := database.DB.First(&got, keeper.ID).Error; err != nil || got.Path != preview.Items[0].DestinationPath {
		t.Fatalf("recovery moved keeper again: %+v %v", got, err)
	}
}

func TestAppConsolidationStartWaitsForRuntimeContext(t *testing.T) {
	setupAppTestDB(t)
	a := newAppConsolidationFixture(t, t.TempDir())
	a.consolidationLifecycle.runtimeReady = false
	if _, err := a.StartCleanupConsolidation("bad", false); err == nil || !strings.Contains(err.Error(), "尚未就绪") {
		t.Fatalf("early startup start: %v", err)
	}
	if status, err := a.GetCleanupConsolidationStatus(0); err != nil || status != nil {
		t.Fatalf("early read: %+v %v", status, err)
	}
	a.enableCleanupConsolidationStart()
	if _, err := a.StartCleanupConsolidation("bad", false); err == nil || !strings.Contains(err.Error(), "预览") {
		t.Fatalf("ready start did not reach service: %v", err)
	}
}

func TestAppConsolidationDelayedRecoveryCannotQueryAfterStop(t *testing.T) {
	setupAppTestDB(t)
	dataDir := t.TempDir()
	// One logical processor makes stop win the queued-goroutine window reliably;
	// the assertion also covers a goroutine that managed to enter before stop.
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	for i := 0; i < 10; i++ {
		a := newAppConsolidationFixture(t, dataDir)
		callback := "test:late_consolidation_recovery"
		var queriesAfterStop atomic.Int32
		var stopFinished atomic.Bool
		if err := database.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "cleanup_consolidation_tasks" {
				if stopFinished.Load() {
					queriesAfterStop.Add(1)
				}
			}
		}); err != nil {
			t.Fatal(err)
		}
		a.startCleanupConsolidationRecovery()
		if err := a.stopCleanupConsolidationAndWait(); err != nil {
			t.Fatal(err)
		}
		stopFinished.Store(true)
		a.consolidationLifecycle.mu.Lock()
		recovering := a.consolidationLifecycle.recovering
		a.consolidationLifecycle.mu.Unlock()
		if recovering {
			t.Error("stop returned before queued recovery exited")
		}
		if err := database.DB.Callback().Query().Remove(callback); err != nil {
			t.Fatal(err)
		}
		if queriesAfterStop.Load() != 0 {
			t.Fatal("queued startup recovery queried DB after stop")
		}
	}
}

func TestAppConsolidationMaintenanceWaitsForLocalFileExecutor(t *testing.T) {
	setupAppTestDB(t)
	a := newAppConsolidationFixture(t, t.TempDir())
	preview, keeper, duplicate := createAppConsolidationPreview(t, a)
	entered := make(chan struct{})
	release, releaseWait := appConsolidationRelease(t)
	callback := "test:consolidation_executor_wait"
	var once sync.Once
	if err := database.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "cleanup_consolidation_task_items" {
			once.Do(func() { close(entered); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.DB.Callback().Update().Remove(callback) })
	started, err := a.StartCleanupConsolidation(preview.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	maintenance := make(chan error, 1)
	go func() { maintenance <- a.enterDatabaseRestoreMode(false) }()
	waitAppConsolidation(t, func() bool {
		a.consolidationLifecycle.mu.Lock()
		defer a.consolidationLifecycle.mu.Unlock()
		return a.consolidationLifecycle.stopped
	})
	select {
	case err := <-maintenance:
		t.Fatalf("maintenance did not wait for file executor: %v", err)
	default:
	}
	if database.MaintenanceActive() {
		t.Fatal("fence installed before executor cleanup")
	}
	releaseWait()
	if err := <-maintenance; err != nil {
		t.Fatal(err)
	}
	a.releaseDatabaseRestoreMode()
	a.releasePlaybackMaintenance()
	row := models.CleanupConsolidationTask{}
	if err := database.DB.First(&row, started.ID).Error; err != nil || row.Status != "cancelled" || row.ActiveSlot != nil {
		t.Fatalf("executor was not cancelled/finalized: %+v %v", row, err)
	}
	for _, path := range []string{keeper.Path, duplicate.Path} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("cancel moved/deleted source", path, err)
		}
	}
}
