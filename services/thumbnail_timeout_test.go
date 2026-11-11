package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"gorm.io/gorm"

	"video-master/database"
	"video-master/models"
)

func timeoutThumbnailFixture(t *testing.T) (*ThumbnailService, uint) {
	t.Helper()
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "clip.mp4")
	mustCreateFile(t, path)
	video := models.Video{Name: "clip.mp4", Path: path, Directory: root, Duration: 10}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	s := NewThumbnailService(&VideoService{}, filepath.Join(root, "cache"))
	s.findFFmpeg = func() (string, error) { return "fake-ffmpeg", nil }
	return s, video.ID
}

// A hung one-frame extraction must relinquish the path read lock so export can publish.
func TestThumbnailTimeoutReleasesExportPathLock(t *testing.T) {
	s, id := timeoutThumbnailFixture(t)
	started := make(chan bool, 1)
	s.runFFmpeg = func(ctx context.Context, _, _, path string, _ float64) error {
		deadline, ok := ctx.Deadline()
		bounded := ok && time.Until(deadline) <= 10*time.Second
		started <- bounded
		if !bounded {
			return errors.New("thumbnail extraction has no bounded deadline")
		}
		if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := s.ResolveThumbnail(context.Background(), id); done <- err }()
	if !<-started {
		<-done
		t.Fatal("thumbnail extraction has no bounded deadline")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	release, err := lockLibraryPathsContext(ctx)
	if err != nil {
		t.Fatalf("export cannot acquire path lock after thumbnail timeout: %v", err)
	}
	release()
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected thumbnail deadline, got %v", err)
	}
	entries, err := os.ReadDir(s.cacheDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("timed out extraction published or leaked partial cache: %v, %v", entries, err)
	}
}

func TestThumbnailWaiterCancellationDoesNotWaitForGenerator(t *testing.T) {
	s, id := timeoutThumbnailFixture(t)
	started := make(chan struct{})
	stop := make(chan struct{})
	done := make(chan error, 1)
	s.runFFmpeg = func(ctx context.Context, _, _, _ string, _ float64) error {
		select {
		case <-started:
		default:
			close(started)
		}
		<-stop
		return errors.New("stopped")
	}
	go func() { _, err := s.ResolveThumbnail(context.Background(), id); done <- err }()
	<-started
	defer func() { close(stop); <-done }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queried := make(chan struct{}, 1)
	const callback = "test:thumbnail_waiter"
	if err := database.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "videos" {
			select {
			case queried <- struct{}{}:
			default:
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Query().Remove(callback)
	waiter := make(chan error, 1)
	go func() { _, err := s.ResolveThumbnail(ctx, id); waiter <- err }()
	<-queried
	cancel()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled thumbnail request is stuck behind the generator")
	}
}

func TestThumbnailFFmpegStopsOnDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "blocked-ffmpeg")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := runThumbnailFFmpeg(ctx, binary, "synthetic.mp4", filepath.Join(root, "out.jpg"), 0)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("thumbnail subprocess did not stop at deadline: %v (%s)", err, time.Since(start))
	}
}
