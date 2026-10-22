package main

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

func functionalPolishFixture(t *testing.T) *App {
	t.Helper()
	setupAppTestDB(t)
	if err := database.DB.AutoMigrate(&models.RecommendationFeedback{}, &models.TaskFailureRun{}, &models.TaskFailureItem{}, &models.CollectionEpisodeAssignment{}, &models.CollectionSeasonExpectation{}); err != nil {
		t.Fatal(err)
	}
	subtitle := services.NewSubtitleService(t.TempDir())
	a := &App{videoService: services.NewVideoService(nil), subtitleService: subtitle, subtitleWorkbench: services.NewSubtitleWorkbenchService(subtitle), aiTaggingService: services.NewAITaggingService(), technicalBackfill: services.NewTechnicalBackfillService(nil), playbackProxies: services.NewPlaybackProxyService(t.TempDir(), nil), collectionService: services.NewCollectionService(t.TempDir()), imageLibraryService: services.NewImageLibraryService()}
	t.Cleanup(func() {
		a.stopFunctionalPolish(false)
		a.stopFailureOwners()
		_ = a.quiescePlaybackForMaintenance()
		a.releaseDatabaseRestoreMode()
		releasePlaybackAfterShutdownTest(a)
	})
	return a
}
func TestFunctionalDiagnosticsRecoveryFailureIsIsolated(t *testing.T) {
	a := functionalPolishFixture(t)
	name := "test:diagnostic_recovery_failure"
	if err := database.DB.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "task_failure_runs" {
			tx.AddError(errors.New("injected recovery failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.initializeFunctionalPolish(); err == nil {
		t.Fatal("recovery failure hidden")
	}
	database.DB.Callback().Update().Remove(name)
	if a.taskFailures != nil || a.startupError != "" || len(a.GetTaskCenterSnapshot().Warnings) == 0 {
		t.Fatal("diagnostic failure was published or blocked app")
	}
	if _, err := a.ListTaskFailureRuns(services.TaskFailureAI); err == nil {
		t.Fatal("unrecovered diagnostic subsystem admitted retry read")
	}
	if _, err := a.ListImageAnniversaries(services.ImageAnniversaryRequest{Date: "2026-10-22"}); err != nil {
		t.Fatalf("unrelated feature blocked: %v", err)
	}
	a.stopFunctionalPolish(false)
	if err := a.initializeFunctionalPolish(); err != nil {
		t.Fatal(err)
	}
	if a.taskFailures == nil || len(a.functionalPolishWarnings()) != 0 {
		t.Fatal("recovery did not rebuild ready store")
	}
}
func TestFunctionalStartupRecoveryAndRebuild(t *testing.T) {
	a := functionalPolishFixture(t)
	run := models.TaskFailureRun{TaskKey: services.TaskFailureAI, State: "running", RecordsComplete: true, StartedAt: time.Now()}
	if err := database.DB.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.initializeFunctionalPolish(); err != nil {
		t.Fatal(err)
	}
	first := a.taskFailures
	var restored models.TaskFailureRun
	database.DB.First(&restored, run.ID)
	if restored.State != "interrupted" || restored.RecordsComplete {
		t.Fatal(restored)
	}
	a.stopFunctionalPolish(false)
	a.resumeAfterDatabaseRestoreFailure()
	if a.taskFailures == nil || a.taskFailures == first {
		t.Fatal("stopped store reused")
	}
	if _, err := first.Preview(services.TaskFailureRetryRequest{RunID: run.ID}); err == nil {
		t.Fatal("old store reopened")
	}
}
func seedFunctionalFailures(t *testing.T, a *App) (uint, []uint) {
	t.Helper()
	if err := a.initializeFunctionalPolish(); err != nil {
		t.Fatal(err)
	}
	run, err := a.taskFailures.Begin(services.TaskFailureAI)
	if err != nil {
		t.Fatal(err)
	}
	ids := []uint{}
	for i := 0; i < 3; i++ {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("%d.mp4", i))
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		video := models.Video{Name: "fixture", Path: path, Directory: filepath.Dir(path)}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Create(&models.AITaggingState{VideoID: video.ID, Status: models.AITaggingStateStatusFailed, AttemptCount: 1}).Error; err != nil {
			t.Fatal(err)
		}
		if err := a.taskFailures.Record(run, services.TaskFailureAI, video.ID, fmt.Sprint(i), "", "failed", "failed"); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, video.ID)
	}
	return run, ids
}
func TestFunctionalPreviewCancelledBeforeMaintenanceWait(t *testing.T) {
	a := functionalPolishFixture(t)
	run, _ := seedFunctionalFailures(t, a)
	entered := make(chan struct{})
	var once sync.Once
	name := "test:preview_cancellable_query"
	database.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "task_failure_items" {
			once.Do(func() { close(entered) })
			<-tx.Statement.Context.Done()
			tx.AddError(tx.Statement.Context.Err())
		}
	})
	defer database.DB.Callback().Query().Remove(name)
	done := make(chan error, 1)
	go func() { _, err := a.PreviewTaskFailureRetry(services.TaskFailureRetryRequest{RunID: run}); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("preview did not begin")
	}
	stopped := make(chan struct{})
	go func() { a.stopFunctionalPolish(false); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("maintenance waited without cancelling preview")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("preview not cancelled: %v", err)
	}
}
func TestFunctionalConfirmedBatchOutlivesRPCAndStopsWithMaintenance(t *testing.T) {
	a := functionalPolishFixture(t)
	run, _ := seedFunctionalFailures(t, a)
	preview, err := a.PreviewTaskFailureRetry(services.TaskFailureRetryRequest{RunID: run})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	name := "test:confirmed_async_claim"
	database.DB.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "ai_tagging_states" {
			once.Do(func() {
				close(entered)
				select {
				case <-release:
				case <-tx.Statement.Context.Done():
					tx.AddError(tx.Statement.Context.Err())
				}
			})
		}
	})
	defer database.DB.Callback().Update().Remove(name)
	operation, err := a.ConfirmTaskFailureRetry(preview.Token)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("RPC return cancelled asynchronous claims")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result, err := a.GetTaskFailureRetryResults(operation, 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		if result.State == "completed" {
			if result.Accepted != 3 {
				t.Fatal(result)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background claims did not complete")
}
func TestFunctionalQueueCleanupFailureRetainsIdentityAndRetry(t *testing.T) {
	a := functionalPolishFixture(t)
	if err := a.initializeFunctionalPolish(); err != nil {
		t.Fatal(err)
	}
	if err := a.startPlaybackQueue(); err != nil {
		t.Fatal(err)
	}
	retained := a.playbackQueue
	a.playbackQueueClose = func(*services.PlaybackQueueService) error { return errors.New("owned process close failed") }
	if err := a.enterDatabaseRestoreMode(false); err == nil {
		t.Fatal("maintenance entered after failed cleanup")
	}
	if database.MaintenanceActive() || a.playbackQueue != retained || !a.playbackQueueQuiesced {
		t.Fatal("failed cleanup reference/fence wrong")
	}
	a.resumeAfterDatabaseRestoreFailure()
	if a.playbackQueue != retained {
		t.Fatal("recovery bypassed retained instance")
	}
	if err := a.startPlaybackQueue(); err == nil {
		t.Fatal("quiesced instance treated as ready")
	}
	if _, err := a.GetPlaybackQueue(services.QueueQuery{}); err == nil || !strings.Contains(err.Error(), "queue_cleanup_pending:") {
		t.Fatalf("no cleanup state: %v", err)
	}
	token := a.playbackQueueCleanupToken
	a.playbackQueueClose = nil
	if err := a.StopPlaybackQueue(token); err != nil {
		t.Fatal(err)
	}
	if a.playbackQueue == nil || a.playbackQueue == retained {
		t.Fatal("cleanup did not rebuild usable queue")
	}
	if err := a.StopPlaybackQueue(token); !errors.Is(err, services.ErrQueueChanged) {
		t.Fatalf("late cleanup stop touched new instance: %v", err)
	}
}
func TestFunctionalOrdinaryStopDoesNotCreateQueue(t *testing.T) {
	a := functionalPolishFixture(t)
	if err := a.StopControlledPlayback(); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := database.DB.Model(&models.PlaybackQueueState{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 || a.playbackQueue != nil {
		t.Fatal("ordinary stop initialized queue")
	}
}
func TestFunctionalNewBindingsRespectMaintenanceAndMissingOwners(t *testing.T) {
	a := functionalPolishFixture(t)
	calls := []func() error{
		func() error { _, err := a.GetPlaybackSubtitle(services.PlaybackSubtitleRequest{}); return err },
		func() error { _, err := a.ListExtractableSubtitleTracks(1); return err },
		func() error { _, err := a.PreviewSubtitleExtraction(services.SubtitleExtractRequest{}); return err },
		func() error { _, err := a.ConfirmSubtitleExtraction(services.SubtitleExtractConfirm{}); return err },
		func() error { _, err := a.ListRecommendationFeedback(); return err },
		func() error {
			_, err := a.SetRecommendationFeedback(services.RecommendationFeedbackRequest{})
			return err
		},
		func() error { return a.RevokeRecommendationFeedback(1, 1) },
		func() error { _, err := a.GetCollectionEpisodeReport(1, 0, 10); return err },
		func() error { return a.SetCollectionEpisodeAssignments(1, nil) },
		func() error { return a.SetCollectionSeasonExpectation(1, 1, 1, 0) },
		func() error { _, err := a.ListImageAnniversaries(services.ImageAnniversaryRequest{}); return err },
		func() error { _, err := a.GetControlledPlaybackState(); return err },
		func() error { _, err := a.ListTaskFailureRuns(services.TaskFailureAI); return err },
		func() error { _, err := a.ListTaskFailures(services.TaskFailureListRequest{}); return err },
		func() error { _, err := a.PreviewTaskFailureRetry(services.TaskFailureRetryRequest{}); return err },
		func() error { _, err := a.ConfirmTaskFailureRetry("missing"); return err },
		func() error { _, err := a.GetTaskFailureRetryResults("missing", 0, 10); return err },
		func() error { return a.CancelTaskFailureRetry("missing") },
	}
	release := database.BeginMaintenance()
	for i, call := range calls {
		if err := call(); err == nil || !strings.Contains(err.Error(), "数据库") {
			t.Errorf("binding %d ignored maintenance: %v", i, err)
		}
	}
	release()
	empty := &App{}
	if _, err := empty.GetCollectionEpisodeReport(1, 0, 10); err == nil {
		t.Fatal("missing collection service admitted")
	}
	if _, err := empty.ListImageAnniversaries(services.ImageAnniversaryRequest{}); err == nil {
		t.Fatal("missing image service admitted")
	}
}

func TestFunctionalMaintenanceCancelsUnsubmittedBatchItems(t *testing.T) {
	a := functionalPolishFixture(t)
	run, _ := seedFunctionalFailures(t, a)
	preview, err := a.PreviewTaskFailureRetry(services.TaskFailureRetryRequest{RunID: run})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	var once sync.Once
	name := "test:cancel_background_claim"
	database.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "videos" {
			once.Do(func() { close(entered); <-tx.Statement.Context.Done(); tx.AddError(tx.Statement.Context.Err()) })
		}
	})
	defer database.DB.Callback().Query().Remove(name)
	operation, err := a.ConfirmTaskFailureRetry(preview.Token)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("claim did not begin")
	}
	a.stopFunctionalPolish(false)
	result, err := a.taskFailures.Results(operation, 0, 200)
	if err != nil || result.Accepted != 0 || result.State != "cancelled" || len(result.Items) != 3 {
		t.Fatalf("pending claims escaped maintenance: %+v %v", result, err)
	}
}
func TestFunctionalDiagnosticPointerPublicationRace(t *testing.T) {
	a := functionalPolishFixture(t)
	if err := a.initializeFunctionalPolish(); err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 30; j++ {
				_, _ = a.ListTaskFailureRuns(services.TaskFailureAI)
				_ = a.functionalPolishWarnings()
			}
		}()
	}
	for i := 0; i < 4; i++ {
		a.stopFunctionalPolish(false)
		if err := a.initializeFunctionalPolish(); err != nil {
			t.Fatal(err)
		}
	}
	readers.Wait()
}
func TestFunctionalControlledCleanupRecoveryRemainsReadable(t *testing.T) {
	a := functionalPolishFixture(t)
	if err := a.initializeFunctionalPolish(); err != nil {
		t.Fatal(err)
	}
	if err := a.quiescePlaybackForMaintenance(); err != nil {
		t.Fatal(err)
	}
	// Process-owner tests establish failed Close retains its run. Here the App
	// failure flag isolates the recovery/admission/UI-read sequence from a GUI process.
	a.playbackLifecycleMu.Lock()
	a.playbackControlledCleanupError = errors.New("cleanup still pending")
	a.playbackLifecycleMu.Unlock()
	a.resumeAfterDatabaseRestoreFailure()
	if a.playbackControlledRelease == nil || a.playbackMaintenanceRelease == nil {
		t.Fatal("failed cleanup admission reopened")
	}
	if _, err := a.GetControlledPlaybackState(); err != nil {
		t.Fatalf("cleanup state hidden after failed maintenance: %v", err)
	}
	if err := a.StopControlledPlayback(); err != nil {
		t.Fatal(err)
	}
	if a.playbackControlledRelease != nil || a.playbackMaintenanceRelease != nil {
		t.Fatal("explicit cleanup did not release admission")
	}
}

func TestFunctionalNewBindingsRejectPendingRelaunch(t *testing.T) {
	dataDir, _ := prepareConfigOnlySwitchBack(t)
	a := newMaintenanceTestApp(dataDir)
	t.Cleanup(func() {
		a.releaseDatabaseRestoreMode()
		a.databaseSwitchService = nil
		releasePlaybackAfterShutdownTest(a)
	})
	if result, err := a.SwitchBackendConfigOnly("sqlite"); err != nil || !result.Switched {
		t.Fatalf("test switch failed %+v %v", result, err)
	}
	calls := []func() error{
		func() error { _, err := a.ListRecommendationFeedback(); return err },
		func() error { _, err := a.ListImageAnniversaries(services.ImageAnniversaryRequest{}); return err },
		func() error { _, err := a.GetCollectionEpisodeReport(1, 0, 50); return err },
		func() error { _, err := a.GetPlaybackSubtitle(services.PlaybackSubtitleRequest{}); return err },
		func() error { _, err := a.ListTaskFailureRuns(services.TaskFailureAI); return err },
	}
	for i, call := range calls {
		if err := call(); err == nil || !strings.Contains(err.Error(), "重启") {
			t.Errorf("pending relaunch binding %d: %v", i, err)
		}
	}
}
