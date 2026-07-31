package services

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"github.com/fsnotify/fsnotify"
)

type fakeLibraryWatchBackend struct {
	events  chan fsnotify.Event
	errors  chan error
	mu      sync.Mutex
	added   []string
	removed []string
	failAdd bool
	closed  bool

	closeEntered chan struct{}
	closeGate    chan struct{}
}

func newFakeLibraryWatchBackend() *fakeLibraryWatchBackend {
	return &fakeLibraryWatchBackend{events: make(chan fsnotify.Event, 32), errors: make(chan error, 8)}
}

func (f *fakeLibraryWatchBackend) Add(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAdd {
		// Real backends report failures as path-carrying errors.
		return &fs.PathError{Op: "watch", Path: filepath.Clean(path), Err: errors.New("watch limit reached")}
	}
	f.added = append(f.added, filepath.Clean(path))
	return nil
}

func (f *fakeLibraryWatchBackend) Remove(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, filepath.Clean(path))
	return nil
}

func (f *fakeLibraryWatchBackend) Close() error {
	f.mu.Lock()
	f.closed = true
	entered, gate := f.closeEntered, f.closeGate
	f.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if gate != nil {
		<-gate
	}
	return nil
}

// blockClose makes Close park until the returned release func runs, so tests can hold a
// shutdown open and drive another lifecycle call into the window.
func (f *fakeLibraryWatchBackend) blockClose() (entered <-chan struct{}, release func()) {
	enteredCh := make(chan struct{})
	gate := make(chan struct{})
	f.mu.Lock()
	f.closeEntered = enteredCh
	f.closeGate = gate
	f.mu.Unlock()
	return enteredCh, func() { close(gate) }
}

func (f *fakeLibraryWatchBackend) Events() <-chan fsnotify.Event { return f.events }
func (f *fakeLibraryWatchBackend) Errors() <-chan error          { return f.errors }

func (f *fakeLibraryWatchBackend) addedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	paths := append([]string(nil), f.added...)
	sort.Strings(paths)
	return paths
}

func (f *fakeLibraryWatchBackend) setFailAdd(fail bool) {
	f.mu.Lock()
	f.failAdd = fail
	f.mu.Unlock()
}

func (f *fakeLibraryWatchBackend) removedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	paths := append([]string(nil), f.removed...)
	sort.Strings(paths)
	return paths
}

func TestLibraryWatcherRegistersRecursivelyAndCoalescesStableEvents(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	file := filepath.Join(nested, "movie.mp4")
	if err := os.WriteFile(file, []byte("stable"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	backend := newFakeLibraryWatchBackend()
	reconciled := make(chan []string, 4)
	service := newTestLibraryWatcher(backend, func(_ []models.ScanDirectory, affected []string) *ScanSyncResult {
		reconciled <- append([]string(nil), affected...)
		return &ScanSyncResult{}
	})
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer service.Close()

	if got := backend.addedPaths(); len(got) != 2 || got[0] != root || got[1] != nested {
		t.Fatalf("recursive watches = %#v", got)
	}
	backend.events <- fsnotify.Event{Name: file, Op: fsnotify.Create}
	backend.events <- fsnotify.Event{Name: file, Op: fsnotify.Write}
	backend.events <- fsnotify.Event{Name: file, Op: fsnotify.Write}

	select {
	case affected := <-reconciled:
		if len(affected) != 1 || affected[0] != nested {
			t.Fatalf("affected directories = %#v", affected)
		}
	case <-time.After(time.Second):
		t.Fatal("watcher did not reconcile stable events")
	}
	select {
	case extra := <-reconciled:
		t.Fatalf("event burst triggered an extra reconciliation: %#v", extra)
	case <-time.After(40 * time.Millisecond):
	}
}

func TestLibraryWatcherWaitsUntilGrowingFileIsStable(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "growing.mp4")
	if err := os.WriteFile(file, []byte("a"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	backend := newFakeLibraryWatchBackend()
	reconciled := make(chan struct{}, 1)
	service := newTestLibraryWatcher(backend, func(_ []models.ScanDirectory, _ []string) *ScanSyncResult {
		reconciled <- struct{}{}
		return &ScanSyncResult{}
	})
	service.stabilityInterval = 30 * time.Millisecond
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer service.Close()
	backend.events <- fsnotify.Event{Name: file, Op: fsnotify.Write}
	time.AfterFunc(20*time.Millisecond, func() { _ = os.WriteFile(file, []byte("still growing"), 0644) })

	select {
	case <-reconciled:
		t.Fatal("reconciled before two stable snapshots")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("did not reconcile after file stabilized")
	}
}

func TestLibraryWatcherAddsNewDirectoryAndSupportsRetry(t *testing.T) {
	root := t.TempDir()
	backend := newFakeLibraryWatchBackend()
	service := newTestLibraryWatcher(backend, func(_ []models.ScanDirectory, _ []string) *ScanSyncResult { return &ScanSyncResult{} })
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer service.Close()

	child := filepath.Join(root, "new-child")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatalf("create child: %v", err)
	}
	backend.events <- fsnotify.Event{Name: child, Op: fsnotify.Create}
	waitForWatcherCondition(t, func() bool {
		paths := backend.addedPaths()
		return len(paths) == 2 && paths[1] == child
	})

	backend.setFailAdd(true)
	if err := service.Reconfigure([]models.ScanDirectory{{ID: 1, Path: root}, {ID: 2, Path: filepath.Join(root, "missing")}}); err != nil {
		t.Fatalf("reconfigure should surface per-root state, not fail the service: %v", err)
	}
	status := service.Snapshot()
	if rootState(status, 2) != LibraryWatchStateUnavailable {
		t.Fatalf("failed root status = %#v", status)
	}
	backend.setFailAdd(false)
	if _, err := service.RetryRoot(2); err == nil {
		t.Fatal("retrying a still-missing root should fail")
	}
	if err := os.Mkdir(filepath.Join(root, "missing"), 0755); err != nil {
		t.Fatalf("create retry root: %v", err)
	}
	if retried, err := service.RetryRoot(2); err != nil || retried.State != LibraryWatchStateWatching {
		t.Fatalf("retry result=%#v err=%v", retried, err)
	}
}

func TestLibraryWatcherRootRemovalBecomesUnavailableWithoutReconciliation(t *testing.T) {
	root := t.TempDir()
	backend := newFakeLibraryWatchBackend()
	reconciled := make(chan struct{}, 1)
	service := newTestLibraryWatcher(backend, func(_ []models.ScanDirectory, _ []string) *ScanSyncResult {
		reconciled <- struct{}{}
		return &ScanSyncResult{}
	})
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer service.Close()

	backend.events <- fsnotify.Event{Name: root, Op: fsnotify.Remove}
	waitForWatcherCondition(t, func() bool {
		return rootState(service.Snapshot(), 1) == LibraryWatchStateUnavailable
	})
	select {
	case <-reconciled:
		t.Fatal("removed root triggered reconciliation outside the configured root")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestLibraryWatcherMarksUnsupportedFilesystemUnavailable(t *testing.T) {
	root := t.TempDir()
	backend := newFakeLibraryWatchBackend()
	service := newTestLibraryWatcher(backend, func(_ []models.ScanDirectory, _ []string) *ScanSyncResult { return &ScanSyncResult{} })
	service.rootSupport = func(string) (bool, string, error) {
		return false, "此文件系统不支持可靠的实时监听", nil
	}
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer service.Close()

	status := service.Snapshot()
	if rootState(status, 1) != LibraryWatchStateUnavailable {
		t.Fatalf("unsupported root status = %#v", status)
	}
	if added := backend.addedPaths(); len(added) != 0 {
		t.Fatalf("unsupported root registered OS watches: %#v", added)
	}
}

func TestLibraryWatcherReconfigureInvalidatesInFlightBatch(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, file)
	backend := newFakeLibraryWatchBackend()
	reconciled := make(chan struct{}, 1)
	service := newTestLibraryWatcher(backend, func(_ []models.ScanDirectory, _ []string) *ScanSyncResult {
		reconciled <- struct{}{}
		return &ScanSyncResult{}
	})
	service.stabilityInterval = 100 * time.Millisecond
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer service.Close()

	backend.events <- fsnotify.Event{Name: file, Op: fsnotify.Write}
	waitForWatcherCondition(t, func() bool { return watcherRootProcessing(service, 1) })
	if err := service.Reconfigure(nil); err != nil {
		t.Fatalf("remove watch root: %v", err)
	}
	select {
	case <-reconciled:
		t.Fatal("stale in-flight batch reconciled after root removal")
	case <-time.After(250 * time.Millisecond):
	}
}

func TestLibraryWatcherPathUpdateKeepsOldWatchUntilNewRootRegisters(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	backend := newFakeLibraryWatchBackend()
	service := newTestLibraryWatcher(backend, func(_ []models.ScanDirectory, _ []string) *ScanSyncResult { return &ScanSyncResult{} })
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: oldRoot}}); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer service.Close()

	backend.setFailAdd(true)
	if err := service.Reconfigure([]models.ScanDirectory{{ID: 1, Path: newRoot}}); err != nil {
		t.Fatalf("reconfigure watcher: %v", err)
	}
	status := service.Snapshot()
	if rootState(status, 1) != LibraryWatchStateError {
		t.Fatalf("path update failure status = %#v", status)
	}
	if removed := backend.removedPaths(); len(removed) != 0 {
		t.Fatalf("old watch was removed before the new root registered: %#v", removed)
	}

	backend.setFailAdd(false)
	if retried, err := service.RetryRoot(1); err != nil || retried.State != LibraryWatchStateWatching {
		t.Fatalf("retry result=%#v err=%v", retried, err)
	}
	if removed := backend.removedPaths(); len(removed) != 1 || removed[0] != oldRoot {
		t.Fatalf("old watch removal after successful swap = %#v", removed)
	}
}

func TestLibraryWatcherFailedRetryKeepsExistingWatches(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "season-1"), 0755); err != nil {
		t.Fatalf("create subdirectory: %v", err)
	}
	backend := newFakeLibraryWatchBackend()
	service := newTestLibraryWatcher(backend, func(_ []models.ScanDirectory, _ []string) *ScanSyncResult { return &ScanSyncResult{} })
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer service.Close()

	watching := service.Snapshot()
	established := rootStatus(t, watching, 1).WatchCount
	if established < 2 {
		t.Fatalf("fixture should establish nested watches, got %#v", watching)
	}

	// A transient probe failure while retrying must not strip the watches that still work.
	service.rootSupport = func(string) (bool, string, error) { return false, "", errors.New("volume busy") }
	retried, err := service.RetryRoot(1)
	if err == nil {
		t.Fatal("expected the retry to fail")
	}
	if retried.WatchCount != established {
		t.Fatalf("failed retry dropped watches: %d, want %d", retried.WatchCount, established)
	}
	if removed := backend.removedPaths(); len(removed) != 0 {
		t.Fatalf("failed retry removed live watches: %#v", removed)
	}
}

func TestLibraryWatcherEventBurstDoesNotFloodStatusEvents(t *testing.T) {
	root := t.TempDir()
	backend := newFakeLibraryWatchBackend()
	service := newTestLibraryWatcher(backend, func(_ []models.ScanDirectory, _ []string) *ScanSyncResult { return &ScanSyncResult{} })
	var statusEmissions atomic.Int64
	service.SetEventEmitters(func(LibraryWatcherStatus) { statusEmissions.Add(1) }, nil)
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer service.Close()

	baseline := statusEmissions.Load()
	file := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(file, []byte("stable"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	const burst = 200
	for i := 0; i < burst; i++ {
		backend.events <- fsnotify.Event{Name: file, Op: fsnotify.Write}
	}
	waitForWatcherCondition(t, func() bool { return len(backend.events) == 0 })

	// Copying many files must not turn into one full status broadcast per event.
	if extra := statusEmissions.Load() - baseline; extra > 10 {
		t.Fatalf("event burst produced %d status emissions", extra)
	}
}

func TestLibraryWatcherRestartDuringShutdownStaysUsable(t *testing.T) {
	root := t.TempDir()
	closing := newFakeLibraryWatchBackend()
	restarted := newFakeLibraryWatchBackend()
	service := newTestLibraryWatcher(closing, func(_ []models.ScanDirectory, _ []string) *ScanSyncResult { return &ScanSyncResult{} })
	backends := []*fakeLibraryWatchBackend{closing, restarted}
	var handed int
	var handMu sync.Mutex
	service.backendFactory = func() (libraryWatchBackend, error) {
		handMu.Lock()
		defer handMu.Unlock()
		backend := backends[handed]
		if handed < len(backends)-1 {
			handed++
		}
		return backend, nil
	}
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatalf("start watcher: %v", err)
	}

	entered, release := closing.blockClose()
	closed := make(chan error, 1)
	go func() { closed <- service.Close() }()
	<-entered

	// Turning the feature back on while the previous shutdown is still draining must not
	// leave the watcher running against a backend the shutdown is about to discard.
	started := make(chan error, 1)
	go func() { started <- service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}) }()
	time.Sleep(20 * time.Millisecond)
	release()

	if err := <-closed; err != nil {
		t.Fatalf("close watcher: %v", err)
	}
	if err := <-started; err != nil {
		t.Fatalf("restart watcher: %v", err)
	}
	defer service.Close()

	if err := service.Reconfigure([]models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatalf("watcher is unusable after a restart during shutdown: %v", err)
	}
	if rootState(service.Snapshot(), 1) != LibraryWatchStateWatching {
		t.Fatalf("restarted watcher status = %#v", service.Snapshot())
	}
}

func TestLibraryWatcherErrorMessagesOmitAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "private-collection")
	if err := os.MkdirAll(secret, 0755); err != nil {
		t.Fatalf("create subdirectory: %v", err)
	}
	backend := newFakeLibraryWatchBackend()
	service := newTestLibraryWatcher(backend, func(_ []models.ScanDirectory, _ []string) *ScanSyncResult {
		return nil
	})
	service.reconcile = func(_ []models.ScanDirectory, _ []string) *ScanSyncResult { return nil }
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer service.Close()

	// Registering a second root fails with a path-carrying error; the resulting status
	// is what the settings page renders, so it must not disclose the path.
	secondRoot := t.TempDir()
	deepSecret := filepath.Join(secondRoot, "another-private-dir")
	if err := os.MkdirAll(deepSecret, 0755); err != nil {
		t.Fatalf("create second subdirectory: %v", err)
	}
	backend.setFailAdd(true)
	if err := service.Reconfigure([]models.ScanDirectory{{ID: 1, Path: root}, {ID: 2, Path: secondRoot}}); err != nil {
		t.Fatalf("reconfigure watcher: %v", err)
	}
	if rootState(service.Snapshot(), 2) == LibraryWatchStateWatching {
		t.Fatal("second root should not be watching")
	}

	payload, err := json.Marshal(service.Snapshot())
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	for _, leaked := range []string{root, secret, secondRoot, deepSecret} {
		if strings.Contains(string(payload), leaked) {
			t.Fatalf("watcher status leaked %q: %s", leaked, payload)
		}
	}
}

func rootStatus(t *testing.T, status LibraryWatcherStatus, directoryID uint) LibraryWatchRootStatus {
	t.Helper()
	for _, root := range status.Roots {
		if root.DirectoryID == directoryID {
			return root
		}
	}
	t.Fatalf("watch root %d missing from %#v", directoryID, status)
	return LibraryWatchRootStatus{}
}

func TestLibraryReconcileEventContainsCountsWithoutErrorPaths(t *testing.T) {
	sensitivePath := filepath.Join(t.TempDir(), "private", "movie.mp4")
	summary := summarizeLibraryReconciliation(&ScanSyncResult{
		Scanned: 4,
		Added:   2,
		Errors:  []ScanSyncError{{Operation: "scan", Path: sensitivePath, Error: "failed"}},
	})
	event := LibraryReconcileEvent{DirectoryID: 1, Result: summary}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if summary.ErrorCount != 1 || summary.Scanned != 4 || summary.Added != 2 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if strings.Contains(string(payload), sensitivePath) {
		t.Fatalf("event leaked an absolute error path: %s", payload)
	}
}

func TestLibraryWatcherRealBackendImportsCreatedVideo(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	service := NewLibraryWatcherService(NewVideoService(nil))
	service.coalesceWindow = 20 * time.Millisecond
	service.stabilityInterval = 20 * time.Millisecond
	service.stabilityTimeout = 2 * time.Second
	service.tickInterval = 5 * time.Millisecond
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatalf("start real watcher: %v", err)
	}
	defer service.Close()

	path := filepath.Join(root, "created.mp4")
	mustCreateFile(t, path)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		if err := database.DB.Model(&models.Video{}).Where("path = ?", path).Count(&count).Error; err != nil {
			t.Fatalf("query imported video: %v", err)
		}
		if count == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("real filesystem event did not import the created video")
}

func TestLibraryWatcherCloseCancelsPendingReconciliation(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(file, []byte("file"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	backend := newFakeLibraryWatchBackend()
	var reconciled bool
	service := newTestLibraryWatcher(backend, func(_ []models.ScanDirectory, _ []string) *ScanSyncResult {
		reconciled = true
		return &ScanSyncResult{}
	})
	service.coalesceWindow = 200 * time.Millisecond
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	backend.events <- fsnotify.Event{Name: file, Op: fsnotify.Write}
	if err := service.Close(); err != nil {
		t.Fatalf("close watcher: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	if reconciled {
		t.Fatal("pending batch reconciled after close")
	}
	backend.mu.Lock()
	closed := backend.closed
	backend.mu.Unlock()
	if !closed {
		t.Fatal("watch backend was not closed")
	}
}

func newTestLibraryWatcher(backend *fakeLibraryWatchBackend, reconcile func([]models.ScanDirectory, []string) *ScanSyncResult) *LibraryWatcherService {
	service := NewLibraryWatcherService(&VideoService{})
	service.backendFactory = func() (libraryWatchBackend, error) { return backend, nil }
	service.reconcile = reconcile
	service.coalesceWindow = 15 * time.Millisecond
	service.stabilityInterval = 10 * time.Millisecond
	service.stabilityTimeout = 300 * time.Millisecond
	service.tickInterval = 2 * time.Millisecond
	return service
}

func waitForWatcherCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("watcher condition was not satisfied")
}

func rootState(status LibraryWatcherStatus, id uint) LibraryWatchState {
	for _, root := range status.Roots {
		if root.DirectoryID == id {
			return root.State
		}
	}
	return ""
}

func watcherRootProcessing(service *LibraryWatcherService, id uint) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	root := service.roots[id]
	return root != nil && root.processing
}
