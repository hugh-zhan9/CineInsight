package main

import (
	"context"
	"errors"
	"testing"
	"time"
	"video-master/services"
)

// A cancelled migration invokes recovery while shutdown waits for restoreMu.
// Recovery must not admit a new player in that interval.
func TestPlaybackShutdownDoesNotReopenAfterMigrationRecovery(t *testing.T) {
	setupAppTestDB(t)
	a := &App{}
	a.quiescePlaybackForMaintenance()
	a.quiescePlaybackForShutdown()
	t.Cleanup(func() { releasePlaybackAfterShutdownTest(a) })
	a.resumeAfterDatabaseRestoreFailure()
	_, err := services.NewVideoService(nil).PlayVideo(0)
	if !errors.Is(err, services.ErrPlaybackQuiesced) {
		t.Fatalf("migration recovery reopened playback during shutdown: %v", err)
	}
}

func releasePlaybackAfterShutdownTest(a *App) {
	a.playbackLifecycleMu.Lock()
	a.playbackShuttingDown = false
	a.playbackLifecycleMu.Unlock()
	a.releasePlaybackMaintenance()
}

func TestPlaybackShutdownClosesBeforeWaitingForMigration(t *testing.T) {
	setupAppTestDB(t)
	a := NewApp()
	a.quiescePlaybackForMaintenance()
	a.restoreMu.Lock()
	done := make(chan struct{})
	go func() { a.shutdown(context.Background()); close(done) }()
	defer func() {
		a.restoreMu.Unlock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("shutdown did not finish after migration released restoreMu")
		}
		releasePlaybackAfterShutdownTest(a)
	}()
	waitAppConsolidation(t, func() bool {
		a.playbackLifecycleMu.Lock()
		defer a.playbackLifecycleMu.Unlock()
		return a.playbackShuttingDown
	})
	// This is the migration failure callback, still under its restoreMu.
	a.resumeAfterDatabaseRestoreFailure()
	_, err := a.videoService.PlayVideo(0)
	if !errors.Is(err, services.ErrPlaybackQuiesced) {
		t.Fatalf("playback admitted while shutdown waited for migration: %v", err)
	}
}

func TestPlaybackMaintenanceFailureReopensAfterRecovery(t *testing.T) {
	setupAppTestDB(t)
	a := &App{}
	a.quiescePlaybackForMaintenance()
	t.Cleanup(a.releasePlaybackMaintenance)
	a.resumeAfterDatabaseRestoreFailure()
	_, err := services.NewVideoService(nil).PlayVideo(0)
	if errors.Is(err, services.ErrPlaybackQuiesced) {
		t.Fatal("ordinary failed maintenance did not reopen playback")
	}
}
