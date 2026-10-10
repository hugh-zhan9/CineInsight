package main

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
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

func appQueueFixture(t *testing.T) (*App, string, *services.QueueSnapshot) {
	t.Helper()
	setupAppTestDB(t)
	path := filepath.Join(t.TempDir(), "fixture.mp4")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "fixture", Path: path, Directory: filepath.Dir(path), Duration: 120}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	vs := services.NewVideoService(nil)
	vs.SetPlaybackProxyService(services.NewPlaybackProxyService(t.TempDir(), nil))
	a := &App{videoService: vs}
	if err := a.startPlaybackQueue(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.quiescePlaybackForMaintenance()
		a.releaseDatabaseRestoreMode()
		releasePlaybackAfterShutdownTest(a)
	})
	if err := a.EditPlaybackQueue(services.QueueEdit{Action: "configure", Player: "inline", Autoplay: appQueueBool(false), ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := a.EditPlaybackQueue(services.QueueEdit{Action: "append_videos", VideoIDs: []uint{video.ID}, ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	p, err := a.GetPlaybackQueue(services.QueueQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PlayPlaybackQueue(p.Items[0].ID, p.State.Revision); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p, err = a.GetPlaybackQueue(services.QueueQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if p.Session != nil {
			return a, path, p
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("inline session not ready")
	return nil, "", nil
}
func appQueueBool(v bool) *bool { return &v }
func TestQueueAssetRouteRangeHeadAndExpiredIdentity(t *testing.T) {
	a, _, p := appQueueFixture(t)
	handler := newAssetHandler(a)
	url := p.Session.Locator
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request := httptest.NewRequest(http.MethodGet, url, nil)
			request.Header.Set("Range", "bytes=3-5")
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, request)
			if out.Code != 206 || out.Body.String() != "345" || out.Header().Get("Content-Range") != "bytes 3-5/10" {
				t.Errorf("range: %d %q %v", out.Code, out.Body.String(), out.Header())
			}
		}()
	}
	wg.Wait()
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"HEAD", url, 200}, {"POST", url, 405}, {"GET", "/preview/queue/invalid", 410}, {"GET", "/preview/queue/00000000000000000000000000000000", 410}} {
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest(tc.method, tc.path, nil))
		if out.Code != tc.status {
			t.Fatalf("%+v: %d", tc, out.Code)
		}
		if out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("queue body cacheable")
		}
	}
	if err := a.StopPlaybackQueue(p.State.ActiveToken); err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, httptest.NewRequest("GET", url, nil))
	if out.Code != 410 {
		t.Fatal(out.Code)
	}
}
func TestQueueAssetChangedSourceFailsWithoutServingNewBytes(t *testing.T) {
	a, path, p := appQueueFixture(t)
	if err := os.WriteFile(path, []byte("new source is not this session"), 0600); err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	newAssetHandler(a).ServeHTTP(out, httptest.NewRequest("GET", p.Session.Locator, nil))
	if out.Code != 409 || out.Body.String() != "Conflict\n" {
		t.Fatalf("changed source served: %d %q", out.Code, out.Body.String())
	}
	state, err := a.GetPlaybackQueue(services.QueueQuery{})
	if err != nil || state.State.Status != "failed" || state.Session != nil {
		t.Fatalf("queue not failed: %+v %v", state, err)
	}
}
func TestQueueMaintenanceClosesReadersBeforeDatabaseFence(t *testing.T) {
	a, _, p := appQueueFixture(t)
	queue, err := a.queueService()
	if err != nil {
		t.Fatal(err)
	}
	reader, _, _, err := queue.Media(context.Background(), p.State.ActiveToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.enterDatabaseRestoreMode(false); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(reader); err == nil {
		t.Fatal("maintenance left body open")
	}
	if _, err := a.GetPlaybackQueue(services.QueueQuery{}); err == nil || !strings.HasPrefix(err.Error(), "queue_unavailable:") {
		t.Fatalf("maintenance needs a stable queue-unavailable code: %v", err)
	}
	a.releaseDatabaseRestoreMode()
	a.releasePlaybackMaintenance()
	if err := a.startPlaybackQueue(); err != nil {
		t.Fatal(err)
	}
	restored, err := a.GetPlaybackQueue(services.QueueQuery{})
	if err != nil || restored.Session != nil || restored.State.Status != "interrupted" || restored.Total != 1 {
		t.Fatalf("restart played or lost queue: %+v %v", restored, err)
	}
	a.quiescePlaybackForShutdown()
	if err := a.startPlaybackQueue(); !errors.Is(err, services.ErrPlaybackQuiesced) {
		t.Fatalf("shutdown reopened queue: %v", err)
	}
}

func TestQueueRPCErrorsKeepCodesWithoutPaths(t *testing.T) {
	a, _, _ := appQueueFixture(t)
	path := filepath.Join(t.TempDir(), "private folder", "secret movie.mp4")
	name := "test:queue_rpc_path_error"
	database.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "playback_queue_states" {
			tx.AddError(fmt.Errorf("queue_control_failed: %w", &os.PathError{Op: "stat", Path: path, Err: os.ErrPermission}))
		}
	})
	defer database.DB.Callback().Query().Remove(name)
	_, err := a.GetPlaybackQueue(services.QueueQuery{})
	if err == nil || !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "queue_control_failed") {
		t.Fatalf("error chain or code lost: %v", err)
	}
	if strings.Contains(err.Error(), "secret movie") || strings.Contains(err.Error(), filepath.Dir(path)) {
		t.Fatalf("RPC exposed path: %v", err)
	}
}
