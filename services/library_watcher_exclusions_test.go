package services

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
	"video-master/models"

	"github.com/fsnotify/fsnotify"
)

func TestLibraryWatcherExcludesBrokenSymlinkSubtreeWithRealBackend(t *testing.T) {
	root := t.TempDir()
	excluded := filepath.Join(root, "hugh_")
	public := filepath.Join(excluded, "docker", "public")
	allowed := filepath.Join(root, "hugh_movies")
	for _, path := range []string{public, allowed} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "missing-container-uploads"), filepath.Join(public, "pic")); err != nil {
		t.Fatal(err)
	}
	service := NewLibraryWatcherService(nil)
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}, excluded); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	wantWatches := 2
	if libraryBackendRecursive(service.backend) {
		wantWatches = 1
	}
	status := service.Snapshot().Roots[0]
	if status.State != LibraryWatchStateWatching || status.WatchCount != wantWatches {
		t.Fatalf("excluded broken link must not prevent watching the root and allowed sibling: %+v", status)
	}
	if status, err := service.RetryRoot(1); err != nil || status.WatchCount != wantWatches {
		t.Fatalf("retry lost exclusions: %+v, %v", status, err)
	}
}

func TestLibraryWatcherExclusionChangesRemoveAndRestoreWatches(t *testing.T) {
	root := t.TempDir()
	excluded := filepath.Join(root, "hugh_")
	child := filepath.Join(excluded, "child")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	backend := newFakeLibraryWatchBackend()
	service := newTestLibraryWatcher(backend, nil)
	service.coalesceWindow = time.Hour
	dirs := []models.ScanDirectory{{ID: 1, Path: root}, {ID: 2, Path: excluded}}
	if err := service.Start(context.Background(), dirs); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	service.handleEvent(fsnotify.Event{Name: filepath.Join(child, "old.mp4"), Op: fsnotify.Write})
	if err := service.Reconfigure(dirs, excluded); err != nil {
		t.Fatal(err)
	}
	if got := backend.removedPaths(); !slices.Equal(got, []string{excluded, child}) {
		t.Fatalf("excluded watches were not removed across overlapping roots: %v", got)
	}
	status := service.Snapshot()
	if status.Roots[0].WatchCount != 1 || status.Roots[1].State != LibraryWatchStateDisabled || status.Roots[1].ReasonCode != "excluded" {
		t.Fatalf("unexpected status after exclusion: %+v", status)
	}
	service.handleEvent(fsnotify.Event{Name: child, Op: fsnotify.Create})
	service.handleEvent(fsnotify.Event{Name: filepath.Join(child, "new.mp4"), Op: fsnotify.Write})
	service.handleEvent(fsnotify.Event{Name: excluded, Op: fsnotify.Remove})
	service.mu.Lock()
	for _, watchedRoot := range service.roots {
		if len(watchedRoot.pending) != 0 {
			t.Errorf("excluded events or old pending work survived: %v", watchedRoot.pending)
		}
	}
	service.mu.Unlock()
	if err := service.Reconfigure(dirs); err != nil {
		t.Fatal(err)
	}
	status = service.Snapshot()
	if status.Roots[0].WatchCount != 3 || status.Roots[1].State != LibraryWatchStateWatching || status.Roots[1].WatchCount != 2 {
		t.Fatalf("removing exclusion did not restore watches: %+v", status)
	}
}

func TestLibraryWatcherNewSubtreeAndStabilitySkipExclusions(t *testing.T) {
	root := t.TempDir()
	created := filepath.Join(root, "incoming")
	excluded := filepath.Join(created, "hugh_")
	backend := newFakeLibraryWatchBackend()
	service := newTestLibraryWatcher(backend, nil)
	service.coalesceWindow = time.Hour
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}, excluded); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err := os.MkdirAll(filepath.Join(excluded, "child"), 0755); err != nil {
		t.Fatal(err)
	}
	allowedFile := filepath.Join(created, "movie.mp4")
	for _, path := range []string{allowedFile, filepath.Join(excluded, "child", "ignored.mp4")} {
		if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	service.handleEvent(fsnotify.Event{Name: created, Op: fsnotify.Create})
	if got := backend.addedPaths(); !slices.Equal(got, []string{root, created}) {
		t.Fatalf("new subtree registered excluded directories: %v", got)
	}
	snapshot, err := snapshotWatchDirectories([]string{created}, excluded)
	if err != nil || len(snapshot) != 1 {
		t.Fatalf("stability probe traversed excluded files: %v, %v", snapshot, err)
	}
	if _, ok := snapshot[allowedFile]; !ok {
		t.Fatal("stability probe lost allowed file")
	}
}

func TestLibraryWatcherExcludingRootRecoversRegistrationError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	backend := newFakeLibraryWatchBackend()
	service := newTestLibraryWatcher(backend, nil)
	dirs := []models.ScanDirectory{{ID: 1, Path: root}}
	if err := service.Start(context.Background(), dirs); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if rootState(service.Snapshot(), 1) != LibraryWatchStateUnavailable {
		t.Fatal("missing root should initially be unavailable")
	}
	if err := service.Reconfigure(dirs, root); err != nil {
		t.Fatal(err)
	}
	if status, err := service.RetryRoot(1); err != nil || status.State != LibraryWatchStateDisabled || status.ReasonCode != "excluded" {
		t.Fatalf("excluded root must be skipped before touching the filesystem: %+v, %v", status, err)
	}
}
