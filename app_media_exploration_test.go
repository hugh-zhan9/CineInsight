package main

import (
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"

	"github.com/google/uuid"
)

func explorationTestApp(t *testing.T) *App {
	t.Helper()
	setupAppTestDB(t)
	video := services.NewVideoService(nil)
	return &App{ctx: context.Background(), mediaExploration: services.NewMediaExplorationService(), videoService: video, thumbnailService: services.NewThumbnailService(video, t.TempDir()), imageThumbnail: services.NewImageThumbnailService(t.TempDir()), backgroundTasks: services.NewBackgroundTaskRegistry()}
}

func TestExplorationBindingsAndScopedAssets(t *testing.T) {
	a := explorationTestApp(t)
	defer a.stopExplorationQuit()
	root := t.TempDir()
	path := filepath.Join(root, "synthetic.jpg")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 320, 180)), nil); err != nil {
		t.Fatal(err)
	}
	f.Close()
	photo := models.Image{Name: "synthetic", Path: path, Directory: root, Format: "jpg"}
	if err := database.DB.Create(&photo).Error; err != nil {
		t.Fatal(err)
	}
	opened := a.OpenMediaExploration("image")
	if opened.Status != "ok" || len(opened.Data.Entries) != 1 {
		t.Fatalf("open %+v", opened)
	}
	state := opened.Data
	cards := a.GetMediaExplorationItems(services.ExplorationItemsRequest{ExplorationContext: state.ExplorationContext, EntryIDs: []uint{state.Entries[0].EntryID}})
	if cards.Status != "ok" || len(cards.Data.Items) != 1 {
		t.Fatalf("items %+v", cards)
	}
	assetURL := cards.Data.Items[0].ThumbnailURL + "&edge=96"
	recorder := httptest.NewRecorder()
	newAssetHandler(a).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, assetURL, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("asset %d %s", recorder.Code, recorder.Body.String())
	}
	cfg, err := jpeg.DecodeConfig(recorder.Body)
	if err != nil || cfg.Width != 96 || cfg.Height != 54 {
		t.Fatalf("small tier %+v %v", cfg, err)
	}
	parsed, _ := url.Parse(assetURL)
	q := parsed.Query()
	q.Set("exploration_scene", "stale")
	parsed.RawQuery = q.Encode()
	recorder = httptest.NewRecorder()
	newAssetHandler(a).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, parsed.String(), nil))
	if recorder.Code != http.StatusConflict {
		t.Fatal("stale scene not refused", recorder.Code)
	}
	request := services.ExplorationRequest{ExplorationContext: state.ExplorationContext, RoundID: state.RoundID, ExpectedRevision: state.Revision, RequestID: fmt.Sprintf("%d:%s", state.Revision, uuid.NewString()), Action: "present", EntryIDs: []uint{state.Entries[0].EntryID}}
	updated := a.UpdateMediaExploration(request)
	if updated.Status != "ok" || updated.Data.PresentedCount != 1 {
		t.Fatalf("present %+v", updated)
	}
	request.DatabaseEpoch = "wrong"
	bad := a.UpdateMediaExploration(request)
	if bad.Status != "error" || bad.Error.Code != "exploration_database_changed" {
		t.Fatalf("wire error %+v", bad)
	}
	release := database.BeginMaintenance()
	defer release()
	blocked := a.OpenMediaExploration("all")
	if blocked.Status != "error" || blocked.Error.Code != "maintenance" {
		t.Fatalf("maintenance wire %+v", blocked)
	}
}

func TestExplorationCloseFlushThenExistingTaskGuard(t *testing.T) {
	resetQuitGuardForTest(t)
	events := stubRuntimeEvents(t)
	quits := stubQuitRuntime(t)
	a := &App{ctx: context.Background(), backgroundTasks: services.NewBackgroundTaskRegistry()}
	defer a.stopExplorationQuit()
	a.explorationQuit.clientSeen = true
	if !a.beforeClose(a.ctx) || !a.beforeClose(a.ctx) {
		t.Fatal("normal close skipped flush")
	}
	if len(events()) != 1 || events()[0].name != "exploration-close-flush" {
		t.Fatalf("duplicate close not coalesced %+v", events())
	}
	id := a.explorationQuit.pending
	if a.FinishMediaExplorationClose("stale", "saved") == nil {
		t.Fatal("stale ack accepted")
	}
	a.backgroundTasks.Begin(services.BackgroundTaskEnhancement)
	if err := a.FinishMediaExplorationClose(id, "saved"); err != nil {
		t.Fatal(err)
	}
	waitQuit(t, quits)
	if !a.beforeClose(a.ctx) {
		t.Fatal("flush bypassed task guard")
	}
	if got := events(); got[len(got)-1].name != quitConfirmRequiredEvent {
		t.Fatal("task confirmation lost")
	}
	if !a.beforeClose(a.ctx) || a.explorationQuit.pending == id {
		t.Fatal("new close reused old flush")
	}
	if err := a.FinishMediaExplorationClose(a.explorationQuit.pending, "cancel"); err != nil {
		t.Fatal(err)
	}
	allowInternalQuit()
	if a.beforeClose(a.ctx) {
		t.Fatal("internal quit blocked")
	}
}

func TestExplorationCloseExpiredAckRequiresExplicitChoice(t *testing.T) {
	resetQuitGuardForTest(t)
	stubRuntimeEvents(t)
	quits := stubQuitRuntime(t)
	a := &App{ctx: context.Background(), backgroundTasks: services.NewBackgroundTaskRegistry()}
	defer a.stopExplorationQuit()
	a.explorationQuit.clientSeen = true
	a.beforeClose(a.ctx)
	id := a.explorationQuit.pending
	a.explorationQuit.mu.Lock()
	a.explorationQuit.deadline = time.Now().Add(-time.Second)
	a.explorationQuit.mu.Unlock()
	if a.FinishMediaExplorationClose(id, "saved") == nil {
		t.Fatal("late ack auto-quit")
	}
	select {
	case <-quits:
		t.Fatal("timed out flush quit")
	default:
	}
	if err := a.FinishMediaExplorationClose(id, "discard"); err != nil {
		t.Fatal(err)
	}
	waitQuit(t, quits)
	if a.beforeClose(a.ctx) {
		t.Fatal("explicit exit did not resume original guard")
	}
}
