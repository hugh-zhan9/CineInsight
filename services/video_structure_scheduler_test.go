package services

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestStructureFreshGateDoesNotUseThirtySecondCache(t *testing.T) {
	gate := NewIdleGate()
	gate.probeSupport = true
	gate.sample = IdleSample{Idle: time.Hour, OnACPower: true}
	gate.sampledAt = time.Now()
	gate.SetProbe(func(context.Context) (IdleSample, error) { return IdleSample{Idle: 0, OnACPower: true}, nil })
	gate.sample = IdleSample{Idle: time.Hour, OnACPower: true}
	gate.sampledAt = time.Now()
	allowed, reason := gate.FreshStructureDecision(context.Background(), IdleSchedulerSettings{Enabled: false, ThresholdMinutes: 5})
	if allowed || reason != IdleWaitReasonUserActive {
		t.Fatalf("stale cache bypassed strict idle: %v %s", allowed, reason)
	}
}
func TestStructureRunningWindowStopsWhenUserReturns(t *testing.T) {
	service, video := structureTestFixture(t)
	state := structureState(t, video.ID)
	var active atomic.Bool
	service.SetActivityCheck(func(context.Context) (bool, string) { return !active.Load(), IdleWaitReasonUserActive })
	started := make(chan struct{})
	service.extract = func(ctx context.Context, _ string, _, _ int64) ([]structureFrame, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		done <- service.idleOperation(context.Background(), func(ctx context.Context) error { return service.ProcessWindow(ctx, state.ID) })
	}()
	<-started
	start := time.Now()
	active.Store(true)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("busy cancellation exceeded five seconds")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("late cancellation")
	}
	state = structureState(t, video.ID)
	if state.Status != "waiting_idle" || state.CheckpointMS != 0 {
		t.Fatalf("cancelled window committed: %+v", state)
	}
}
func TestStructurePlaybackHintsAreBoundedAndExpire(t *testing.T) {
	service, _ := structureTestFixture(t)
	now := time.Now()
	service.now = func() time.Time { return now }
	service.SetPlaybackActivity("viewer", true)
	if !service.ForegroundBusy() {
		t.Fatal("playback was ignored")
	}
	service.SetPlaybackActivity("viewer", false)
	if service.ForegroundBusy() {
		t.Fatal("pause did not release admission")
	}
	service.SetPlaybackActivity("retired-page", true)
	now = now.Add(6 * time.Second)
	if service.ForegroundBusy() {
		t.Fatal("retired page blocks forever")
	}
	service.MediaTransfer(true)
	if !service.ForegroundBusy() {
		t.Fatal("active transfer ignored")
	}
	service.MediaTransfer(false)
	now = now.Add(29 * time.Second)
	if !service.ForegroundBusy() {
		t.Fatal("transfer quiet period missing")
	}
	now = now.Add(2 * time.Second)
	if service.ForegroundBusy() {
		t.Fatal("transfer quiet period never expires")
	}
}

func TestStructureStopDrainsSourceCallbacksAndRejectsLateNotifications(t *testing.T) {
	service, video := structureTestFixture(t)
	state := structureState(t, video.ID)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	db := database.DB
	if err := db.Callback().Update().Before("gorm:update").Register("structure_test_source_wait", func(tx *gorm.DB) {
		if tx.Statement.Table == "video_structure_states" {
			once.Do(func() { close(entered); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Update().Remove("structure_test_source_wait")
	observed := make(chan struct{})
	go func() {
		service.SourceObserved(db, video.ID, state.SourceSize+1, state.SourceModTimeNS)
		close(observed)
	}()
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- service.StopAndWait(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("returned before source callback drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	<-observed
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	before := structureState(t, video.ID)
	service.SourceObserved(db, video.ID, state.SourceSize+2, state.SourceModTimeNS)
	after := structureState(t, video.ID)
	if before.Generation != after.Generation || before.SourceVersion != after.SourceVersion {
		t.Fatal("stopped owner accepted late source notification")
	}
}

func TestStructureSchedulerServesOldLibraryWhileNewWorkKeepsArriving(t *testing.T) {
	service, old := structureTestFixture(t)
	db := database.DB
	cutoff := old.ID
	var settings models.Settings
	if err := db.First(&settings).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		settings.AutoStructureInitialCutoffID = &cutoff
		if err = db.Create(&settings).Error; err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	} else if err = db.Model(&settings).Update("auto_structure_initial_cutoff_id", cutoff).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		path := filepath.Join(old.Directory, fmt.Sprintf("new-%d.mp4", i))
		if err := os.WriteFile(path, []byte("source"), 0600); err != nil {
			t.Fatal(err)
		}
		video := models.Video{Name: fmt.Sprintf("new-%d", i), Path: path, Directory: old.Directory, Duration: 720}
		if err := db.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
	}
	service.SetActivityCheck(func(context.Context) (bool, string) { return true, "" })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chosen := make(chan uint, 8)
	var selections atomic.Int32
	service.SetEventEmitter(func(status VideoStructureStatus) {
		if status.Running && status.CurrentVideoID != 0 {
			chosen <- status.CurrentVideoID
			if selections.Add(1) == 5 {
				cancel()
			}
			service.RequestDiscovery()
		}
	})
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var got []uint
	for len(got) < 5 {
		select {
		case id := <-chosen:
			got = append(got, id)
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("scheduler stalled")
		}
	}
	cancel()
	if err := service.StopAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got[0] != old.ID || got[4] != old.ID {
		t.Fatalf("old work starved: %v", got)
	}
	for _, id := range got[1:4] {
		if id <= cutoff {
			t.Fatalf("new work allocation lost: %v", got)
		}
	}
}

func TestStructureCountsOnlyVisibleLibrary(t *testing.T) {
	service, video := structureTestFixture(t)
	service.refreshCounts(context.Background())
	if service.Status().Pending != 1 {
		t.Fatal("visible pending video missing")
	}
	if err := database.DB.Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	service.refreshCounts(context.Background())
	status := service.Status()
	if status.Pending+status.Ready+status.Failed+status.Unavailable != 0 {
		t.Fatalf("hidden video remained in status: %+v", status)
	}
}
