//go:build darwin && cgo

package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/models"

	"github.com/fsnotify/fsnotify"
)

func newTestFSEventsBackend(t *testing.T) *fseventsLibraryWatchBackend {
	t.Helper()
	backend, err := newLibraryWatchBackend()
	if err != nil {
		t.Fatal(err)
	}
	b := backend.(*fseventsLibraryWatchBackend)
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func waitFSEvent(t *testing.T, b *fseventsLibraryWatchBackend, path string, op fsnotify.Op) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-b.Events():
			if event.Name == path && event.Op&op != 0 {
				return
			}
		case err := <-b.Errors():
			t.Fatalf("FSEvents error: %v", err)
		case <-timer.C:
			t.Fatalf("missing FSEvents %v event for %s", op, path)
		}
	}
}

func TestLibraryFSEventsRealFileAndRootLifecycle(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "media")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	b := newTestFSEventsBackend(t)
	if err := b.Add(root); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "new", "nested")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(child, "movie.mp4")
	if err := os.WriteFile(path, []byte("first"), 0644); err != nil {
		t.Fatal(err)
	}
	waitFSEvent(t, b, path, fsnotify.Create)
	if err := os.WriteFile(path, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	waitFSEvent(t, b, path, fsnotify.Write)
	renamed := filepath.Join(child, "renamed.mp4")
	if err := os.Rename(path, renamed); err != nil {
		t.Fatal(err)
	}
	waitFSEvent(t, b, renamed, fsnotify.Create)
	if err := os.Remove(renamed); err != nil {
		t.Fatal(err)
	}
	waitFSEvent(t, b, renamed, fsnotify.Remove)
	if err := os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	waitFSEvent(t, b, root, fsnotify.Remove)
	if err := b.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(root + "-moved"); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(root + "-moved"); err != fsnotify.ErrClosed {
		t.Fatalf("add after close: %v", err)
	}
}

func TestLibraryFSEventsLargeTreeUsesBoundedFileDescriptors(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 12000; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%05d.jpg", i)), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	// A broken symlink must not be opened by registration, even outside exclusions.
	if err := os.Symlink(filepath.Join(root, "absent"), filepath.Join(root, "broken")); err != nil {
		t.Fatal(err)
	}
	countFDs := func() int {
		dir, err := os.Open("/dev/fd")
		if err != nil {
			t.Fatal(err)
		}
		defer dir.Close()
		entries, err := dir.Readdirnames(-1)
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	before := countFDs()
	b := newTestFSEventsBackend(t)
	if err := b.Add(root); err != nil {
		t.Fatal(err)
	}
	after := countFDs()
	t.Logf("12000 files: descriptors before=%d after=%d growth=%d", before, after, after-before)
	if after-before > 32 {
		t.Fatalf("watching files consumed %d descriptors", after-before)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	// CoreServices/libdispatch lazily open process-wide descriptors. Compare
	// repeated start/stop cycles after that initialization, rather than requiring
	// the process to return to its pre-CoreServices descriptor count.
	warmed := countFDs()
	for i := 0; i < 20; i++ {
		next := newTestFSEventsBackend(t)
		if err := next.Add(root); err != nil {
			t.Fatal(err)
		}
		if err := next.Close(); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := countFDs()
		if got-warmed <= 8 {
			t.Logf("after 20 start/stop cycles: descriptors=%d (warm baseline=%d)", got, warmed)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("descriptors leaked across 20 start/stop cycles: before=%d after=%d", warmed, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLibraryFSEventsCloseWithSaturatedNativeStream(t *testing.T) {
	root := t.TempDir()
	b := newTestFSEventsBackend(t)
	if err := b.Add(root); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(b.events); i++ {
		b.events <- fsnotify.Event{}
	}
	if err := os.WriteFile(filepath.Join(root, "trigger"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.errors:
	case <-time.After(5 * time.Second):
		t.Fatal("native callback did not report overflow")
	}
	closed := make(chan struct{})
	go func() { _ = b.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("native Stop blocked on saturated channel")
	}
}

// Opt-in read-only smoke test for mounted media volumes; never enumerates files.
func TestLibraryFSEventsExternalRoot(t *testing.T) {
	root := os.Getenv("CINEINSIGHT_TEST_FSEVENTS_ROOT")
	if root == "" {
		t.Skip("external root not configured")
	}
	b := newTestFSEventsBackend(t)
	started := time.Now()
	if err := b.Add(root); err != nil {
		t.Fatal(err)
	}
	t.Logf("registered mounted root recursively in %s, streams=%d", time.Since(started), len(b.streams))
}

func TestLibraryFSEventsOverflowAndPathTranslation(t *testing.T) {
	b := newTestFSEventsBackend(t)
	stream := &libraryFSEventStream{backend: b, path: "/var/media", canonical: "/private/var/media"}
	stream.deliver("/private/var/media/nested", fseMustScan)
	if got := <-b.events; got.Name != "/var/media/nested" || got.Op != libraryWatchRescan {
		t.Fatalf("rescan: %+v", got)
	}
	stream.deliver("/private/var/media-other/file", fseCreated)
	stream.deliver("/private/var/media/link", fseCreated|fseIsSymlink)
	if len(b.events) != 0 {
		t.Fatal("out-of-root or symlink event escaped filtering")
	}
	stream.deliver("/private/var/media", fseDropped)
	if err := <-b.errors; err != errLibraryWatchOverflow {
		t.Fatal(err)
	}
	for i := 0; i <= cap(b.events); i++ {
		stream.deliver("/private/var/media/file", fseModified)
	}
	if err := <-b.errors; err != errLibraryWatchOverflow {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { _ = b.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close blocked on full event channel")
	}
}

func TestLibraryFSEventsServiceSkipsExcludedAndHiddenEvents(t *testing.T) {
	root := t.TempDir()
	excluded := filepath.Join(root, "hugh_")
	service := NewLibraryWatcherService(nil)
	service.coalesceWindow = time.Hour
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}, excluded); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for _, path := range []string{filepath.Join(excluded, "movie.mp4"), filepath.Join(root, ".hidden", "movie.mp4")} {
		service.handleEvent(fsnotify.Event{Name: path, Op: fsnotify.Create})
	}
	service.mu.Lock()
	if len(service.roots[1].pending) != 0 {
		t.Error("excluded or hidden event was queued")
	}
	service.mu.Unlock()
	service.handleEvent(fsnotify.Event{Name: root, Op: libraryWatchRescan})
	service.mu.Lock()
	if _, ok := service.roots[1].pending[root]; !ok {
		t.Error("root rescan escaped to its parent")
	}
	service.roots[1].pending = make(map[string]struct{})
	service.mu.Unlock()
	service.handleBackendError(errLibraryWatchOverflow)
	service.mu.Lock()
	if _, ok := service.roots[1].pending[root]; !ok {
		t.Error("overflow did not schedule recovery")
	}
	service.mu.Unlock()
}
