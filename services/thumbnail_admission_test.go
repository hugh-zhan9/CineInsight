package services

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"video-master/database"
	"video-master/internal/perfdiag"
	"video-master/models"
)

func TestThumbnailColdAdmissionAndWarmBypass(t *testing.T) {
	s, warmID := timeoutThumbnailFixture(t)
	if err := os.MkdirAll(s.cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(s.cacheDir, strconv.FormatUint(uint64(warmID), 10)+".jpg")
	if err := os.WriteFile(cache, []byte("existing placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	ids := make([]uint, posterConcurrency+3)
	for i := range ids {
		path := filepath.Join(t.TempDir(), "cold.mp4")
		mustCreateFile(t, path)
		v := models.Video{Name: "cold.mp4", Path: path, Directory: filepath.Dir(path), Duration: 10}
		if err := database.DB.Create(&v).Error; err != nil {
			t.Fatal(err)
		}
		ids[i] = v.ID
	}
	started := make(chan struct{}, len(ids))
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	s.runFFmpeg = func(ctx context.Context, _, _, out string, _ float64) error {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return os.WriteFile(out, []byte("synthetic poster"), 0600)
		}
	}
	errs := make(chan error, len(ids))
	for _, id := range ids {
		go func() { _, err := s.ResolveThumbnail(context.Background(), id); errs <- err }()
	}
	for range posterConcurrency {
		select {
		case <-started:
		case <-time.After(4 * time.Second):
			t.Fatal("cold workers did not reach their expected concurrency")
		}
	}
	select {
	case <-started:
		t.Fatal("too many cold poster workers")
	case <-time.After(80 * time.Millisecond):
	}
	warmCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if media, err := s.ResolveThumbnail(warmCtx, warmID); err != nil || media.Path != cache {
		t.Fatalf("warm hit must bypass cold queue: %v, %v", media, err)
	}
	close(release)
	for range ids {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(4 * time.Second):
			t.Fatal("cold request did not resume after admission")
		}
	}
}

func TestQueuedThumbnailDoesNotHoldPathFence(t *testing.T) {
	s, _ := timeoutThumbnailFixture(t)
	for range posterConcurrency {
		s.posterSem <- struct{}{}
	}
	waiting := make(chan struct{})
	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := resolveBoundedPoster(cancelCtx, s, false, func(ctx context.Context, admitted bool) (*ThumbnailMedia, error) {
			if admitted {
				return nil, errors.New("unexpected generation")
			}
			unlock, err := rLockLibraryPathsContext(ctx)
			if err != nil {
				return nil, err
			}
			defer unlock()
			close(waiting)
			return nil, errPosterCold
		})
		done <- err
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("lookup did not start")
	}
	writeCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	unlock, err := lockLibraryPathsContext(writeCtx)
	if err != nil {
		t.Fatal("queue holds library path fence", err)
	}
	unlock()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("queue did not honor cancellation")
	}
}

func TestThumbnailExplorationLeavesForegroundSlot(t *testing.T) {
	s := NewThumbnailService(nil, t.TempDir())
	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bgStarted := make(chan struct{})
	bgDone := make(chan error, 1)
	go func() {
		_, err := resolveBoundedPoster(bgCtx, s, true, func(ctx context.Context, admitted bool) (*ThumbnailMedia, error) {
			if !admitted {
				return nil, errPosterCold
			}
			close(bgStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		bgDone <- err
	}()
	select {
	case <-bgStarted:
	case <-time.After(time.Second):
		t.Fatal("first exploration request did not start")
	}
	fgCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := resolveBoundedPoster(fgCtx, s, false, func(_ context.Context, admitted bool) (*ThumbnailMedia, error) {
		if !admitted {
			return nil, errPosterCold
		}
		return &ThumbnailMedia{}, nil
	}); err != nil {
		t.Fatalf("background left no foreground capacity: %v", err)
	}
	blocked, end := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer end()
	if _, err := resolveBoundedPoster(blocked, s, true, func(_ context.Context, admitted bool) (*ThumbnailMedia, error) {
		if admitted {
			return nil, errors.New("second exploration generation should be queued")
		}
		return nil, errPosterCold
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second exploration must wait while first is admitted: %v", err)
	}
	cancel()
	select {
	case err := <-bgDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("first exploration work did not cancel")
	}
}

func TestThumbnailPerfSink(t *testing.T) {
	s, id := timeoutThumbnailFixture(t)
	s.runFFmpeg = func(_ context.Context, _, _, path string, _ float64) error {
		return os.WriteFile(path, []byte("synthetic poster"), 0600)
	}
	file := filepath.Join(t.TempDir(), "perf.log")
	if err := perfdiag.Configure(file, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = perfdiag.Close() })
	var ordinary bytes.Buffer
	priorWriter := log.Writer()
	log.SetOutput(&ordinary)
	t.Cleanup(func() { log.SetOutput(priorWriter) })

	if _, err := s.ResolveThumbnail(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "perf component=poster stage=generate success=true elapsed_ms=") ||
		!strings.Contains(got, "perf component=poster stage=resolve exploration=false outcome=cold cache_check_ms=") ||
		!strings.Contains(got, "queue_ms=") || !strings.Contains(got, "cold_work_ms=") ||
		strings.Contains(got, "clip.mp4") || strings.Contains(got, s.cacheDir) {
		t.Fatalf("expected both sanitized poster diagnostics; got %q", got)
	}
	if strings.Contains(ordinary.String(), "perf component=poster") {
		t.Fatalf("poster diagnostics leaked to ordinary logs: %s", ordinary.String())
	}
}
