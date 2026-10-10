package services

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestViewingFactContextCancelsBehindExhaustedPoolAndDoesNotHoldLRULock(t *testing.T) {
	video, _ := notesFixture(t)
	pool, err := database.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	held, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	dedup := newViewEventDedup(8)
	// A legacy caller is blocked in BeginTx. It must not own the process-wide
	// LRU lock and prevent an independently cancelled queue caller from returning.
	done := make(chan error, 1)
	go func() { _, err := dedup.record(video.ID, PlayEventSourceInlineView, "old", time.Now()); done <- err }()
	deadline := time.Now().Add(time.Second)
	for pool.Stats().WaitCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if pool.Stats().WaitCount == 0 {
		t.Fatal("legacy request never waited for connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() {
		_, err := dedup.recordContext(ctx, video.ID, PlayEventSourceInlineView, "queue", time.Now())
		second <- err
	}()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wrong cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queue context stuck behind LRU/database wait")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("legacy request did not finish")
	}
	if recorded, err := dedup.recordContext(context.Background(), video.ID, PlayEventSourceInlineView, "queue", time.Now()); err != nil || !recorded {
		t.Fatalf("cancelled fact was cached: %v %v", recorded, err)
	}
}

func TestWatchProgressContextCancelsActualSQLAndObserverLock(t *testing.T) {
	t.Run("SQL", func(t *testing.T) {
		video, _ := notesFixture(t)
		pool, _ := database.DB.DB()
		pool.SetMaxOpenConns(1)
		held, err := pool.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer held.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := (&VideoService{}).UpdateVideoWatchProgressContext(ctx, video.ID, 20, 120, false, WatchProgressOriginStart)
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("progress SQL ignored cancellation")
		}
	})
	t.Run("observer-lock", func(t *testing.T) {
		h := newMovieChartMarkHarness(t)
		video := h.seedVideo("observed.mp4", "")
		h.seedEntry("201", "片名", "2024-01-01")
		if err := h.service.LinkMovieToVideo("201", video.ID); err != nil {
			t.Fatal(err)
		}
		svc := &VideoService{}
		svc.SetWatchStateObserver(h.service)
		h.service.markMu.Lock()
		defer h.service.markMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := svc.UpdateVideoWatchProgressContext(ctx, video.ID, 120, 120, true, WatchProgressOriginStart)
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancelled observer did not propagate to final DTO read: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("observer ignored cancellation while waiting for chart gate")
		}
		var current models.Video
		if err := h.db.First(&current, video.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !current.IsWatched {
			t.Fatal("committed primary watching fact was undone")
		}
		if _, exists := h.markRow("201"); exists {
			t.Fatal("cancelled secondary observer wrote a chart mark")
		}
	})
}

func TestWatchObserverContextReachesWatchlistDeleteTransaction(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	video := h.seedVideo("watchlist.mp4", "")
	h.seedEntry("201", "片名", "2024-01-01")
	if _, err := h.service.MarkEntry("201", models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	if err := h.service.LinkMovieToVideo("201", video.ID); err != nil {
		t.Fatal(err)
	}
	before, exists := h.markRow("201")
	if !exists || before.WatchlistEntryID == 0 {
		t.Fatal("missing watchlist fixture")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reached bool
	if err := h.db.Callback().Query().After("gorm:query").Register("test:cancel_watchlist_delete", func(tx *gorm.DB) {
		if tx.Statement.Table == "watchlist_entries" {
			reached = true
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer h.db.Callback().Query().Remove("test:cancel_watchlist_delete")
	h.service.OnVideoWatchedChangedContext(ctx, video.ID, true)
	if !reached {
		t.Fatal("observer did not reach watchlist deletion")
	}
	var count int64
	if err := h.db.Model(&models.WatchlistEntry{}).Where("id = ?", before.WatchlistEntryID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("cancelled deletion changed watchlist")
	}
	after, exists := h.markRow("201")
	if !exists || after.Mark != models.MovieChartMarkWant {
		t.Fatal("cancelled observer changed chart ownership")
	}
}

func TestWatchObserverCancellationDoesNotWaitForPosterImportLock(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	video := h.seedVideo("poster.mp4", "")
	h.seedEntry("201", "片名", "2024-01-01")
	if _, err := h.service.MarkEntry("201", models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	if err := h.service.LinkMovieToVideo("201", video.ID); err != nil {
		t.Fatal(err)
	}
	mark, exists := h.markRow("201")
	if !exists {
		t.Fatal("missing mark")
	}
	relative := fmt.Sprintf("watchlist/%d/poster.png", mark.WatchlistEntryID)
	poster := filepath.Join(h.watchlist.images.root, relative)
	if err := os.MkdirAll(filepath.Dir(poster), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poster, []byte("owned poster fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Model(&models.WatchlistEntry{}).Where("id = ?", mark.WatchlistEntryID).Update("poster_path", relative).Error; err != nil {
		t.Fatal(err)
	}
	h.watchlist.images.mu.Lock()
	defer h.watchlist.images.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { h.service.OnVideoWatchedChangedContext(ctx, video.ID, true); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled observer remained blocked behind poster import")
	}
	var count int64
	if err := h.db.Model(&models.WatchlistEntry{}).Where("id = ?", mark.WatchlistEntryID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("already committed record deletion was undone")
	}
	bytes, err := os.ReadFile(poster)
	if err != nil || string(bytes) != "owned poster fixture" {
		t.Fatalf("cancelled poster cleanup changed file %q %v", bytes, err)
	}
	current, exists := h.markRow("201")
	if !exists || current.Mark != models.MovieChartMarkWant {
		t.Fatal("failed poster cleanup falsely completed chart update")
	}
}

func TestQuiesceCancelsOrdinaryPlaybackWaitingForDatabase(t *testing.T) {
	video, _ := notesFixture(t)
	pool, _ := database.DB.DB()
	pool.SetMaxOpenConns(1)
	held, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var release func()
	quiesced := make(chan func(), 1)
	t.Cleanup(func() {
		_ = held.Close()
		if release == nil {
			select {
			case release = <-quiesced:
			case <-time.After(time.Second):
				t.Error("quiesce cleanup timed out")
			}
		}
		if release != nil {
			release()
		}
	})
	finished := make(chan error, 1)
	go func() { _, err := (&VideoService{}).PlayVideo(video.ID); finished <- err }()
	deadline := time.Now().Add(time.Second)
	for pool.Stats().WaitCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if pool.Stats().WaitCount == 0 {
		t.Fatal("playback did not reach database wait")
	}
	go func() { quiesced <- QuiescePlayback() }()
	select {
	case release = <-quiesced:
	case <-time.After(time.Second):
		t.Fatal("ordinary dispatch prevented quiescence")
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wrong dispatch error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatch did not return")
	}
}

func TestPlaybackCancellationRecheckedImmediatelyBeforeDispatch(t *testing.T) {
	video, _ := notesFixture(t)
	previousOpen, previousLookup := openWithDefaultFn, iinaCLILookup
	defer func() { openWithDefaultFn = previousOpen; iinaCLILookup = previousLookup }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	iinaCLILookup = func() (string, bool) { cancel(); return "", false }
	openWithDefaultFn = func(string, bool) error { calls++; return nil }
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("playback_resume_mode", PlaybackResumeModeRestart).Error; err != nil {
		t.Fatal(err)
	}
	result, err := (&VideoService{}).PlayVideoContext(ctx, video.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.DispatchSucceeded || calls != 0 {
		t.Fatalf("cancelled preparation launched: %+v calls=%d", result, calls)
	}
	mustNoteCount(t, &models.PlayEvent{}, 0)
}

func TestRelocationAndProbeActualReadsFollowCancellation(t *testing.T) {
	video, _ := notesFixture(t)
	pool, _ := database.DB.DB()
	pool.SetMaxOpenConns(1)
	held, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	for _, name := range []string{"directories", "scan-config", "probe"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				switch name {
				case "directories":
					_, _, err = (&VideoService{}).findRelocatedVideoCandidate(ctx, &video)
				case "scan-config":
					_, err = (&VideoService{}).scanDirectoryCollectContext(ctx, video.Directory, false, nil, nil)
				case "probe":
					probe := newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
						t.Error("cancelled database wait invoked ffprobe")
						return nil, "", nil
					})
					err = probe.Refresh(ctx, video.ID)
				}
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("%s ignored deadline: %v", name, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s stuck in actual database read", name)
			}
		})
	}
}

func TestRelocateContextLeavesPathLockWaitWithoutChangingMedia(t *testing.T) {
	video, _ := notesFixture(t)
	unlock := BeginLibraryMaintenance()
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- (&VideoService{}).RelocateVideoContext(ctx, video.ID, video.Path) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("relocate did not leave cancelled path-lock wait")
	}
	mustNoteCount(t, &models.VideoTechnicalMetadata{}, 0)
}
