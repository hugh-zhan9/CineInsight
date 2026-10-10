package services

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"

	"gorm.io/gorm"
)

func TestQueueMediaPinsDescriptorAndIndependentReaders(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProxyTestVideo(t, "电影.mp4", 120)
	if err := os.WriteFile(video.Path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	proxy, _ := newProxyTestService(t)
	svc := newProxyBackedVideoService(proxy)
	lease, err := svc.prepareQueueMedia(context.Background(), video.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	r1, mime, _, err := lease.reader(context.Background())
	if err != nil || mime != "video/mp4" {
		t.Fatal(mime, err)
	}
	r2, _, _, err := lease.reader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r1.Seek(6, io.SeekStart)
	a := make([]byte, 2)
	r1.Read(a)
	b := make([]byte, 3)
	r2.Read(b)
	if string(a) != "67" || string(b) != "012" {
		t.Fatalf("shared seek state %q %q", a, b)
	}
	// Renaming the same source and updating its row is safe; no pathname reopen.
	moved := filepath.Join(video.Directory, "renamed.mp4")
	if err := os.Rename(video.Path, moved); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&video).Update("path", moved).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := lease.reader(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(moved, []byte("replacement longer content"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := lease.reader(context.Background()); !errors.Is(err, ErrQueueSourceChanged) {
		t.Fatalf("changed source accepted: %v", err)
	}
	lease.Close()
	if _, _, _, err := lease.reader(context.Background()); !errors.Is(err, ErrQueueLeaseExpired) {
		t.Fatal(err)
	}
	if _, err := r2.Read(b); err == nil {
		t.Fatal("reader remained open after stop")
	}
}

func TestQueueMediaProxyReplacementDoesNotReopenPath(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProxyTestVideo(t, "movie.mkv", 120)
	proxy, _ := newProxyTestService(t)
	path := seedReadyProxy(t, proxy, video, 10, time.Now().Add(-time.Hour))
	lease, err := newProxyBackedVideoService(proxy).prepareQueueMedia(context.Background(), video.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	replacement := filepath.Join(filepath.Dir(path), "replacement")
	if err := os.WriteFile(replacement, []byte("new-new-new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	r, _, _, err := lease.reader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	if err != nil || string(data) != strings.Repeat("x", 10) {
		t.Fatalf("reopened proxy path: %q %v", data, err)
	}
	if err := os.WriteFile(video.Path, []byte("source changed while proxy FD stayed the same"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := lease.reader(context.Background()); !errors.Is(err, ErrQueueSourceChanged) {
		t.Fatalf("proxy hid original source change: %v", err)
	}
	if err := database.DB.Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := lease.reader(context.Background()); !errors.Is(err, ErrQueueMediaUnavailable) {
		t.Fatalf("deleted media readable: %v", err)
	}
}

func TestQueueMediaDetectsInPlaceProxyMutation(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProxyTestVideo(t, "movie.mp4", 120)
	proxy, _ := newProxyTestService(t)
	path := seedReadyProxy(t, proxy, video, 10, time.Now())
	lease, err := newProxyBackedVideoService(proxy).prepareQueueMedia(context.Background(), video.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := os.WriteFile(path, []byte("changed delivered bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := lease.reader(context.Background()); !errors.Is(err, ErrQueueSourceChanged) {
		t.Fatalf("proxy modification undetected: %v", err)
	}
}

func TestQueueMediaSourceAndResumeModes(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProxyTestVideo(t, "movie.mkv", 120)
	proxy, _ := newProxyTestService(t)
	svc := newProxyBackedVideoService(proxy)
	if _, err := svc.prepareQueueMedia(context.Background(), video.ID, true); !errors.Is(err, ErrQueueInlineUnsupported) {
		t.Fatal(err)
	}
	lease, err := svc.prepareQueueMedia(context.Background(), video.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	video.WatchPositionSeconds = 42
	for _, tc := range []struct {
		mode    string
		watched bool
		want    float64
	}{{"resume", false, 42}, {"restart", false, 0}, {"restart_watched", false, 42}, {"restart_watched", true, 0}} {
		video.IsWatched = tc.watched
		start, origin := queueStartPosition(video, tc.mode)
		if start != tc.want || (start == 0) != (origin == WatchProgressOriginStart) {
			t.Fatalf("%+v start=%v origin=%s", tc, start, origin)
		}
	}
}

// Hold the only SQL connection immediately before a named actual downstream
// statement, after all its upstream reads succeeded. Cancellation must reach
// the statement rather than just an outer check.
func TestQueueMediaContextReachesProxyReadTouchAndDiscard(t *testing.T) {
	for _, stage := range []string{"read", "touch", "discard"} {
		t.Run(stage, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			video := createProxyTestVideo(t, "movie.mp4", 120)
			proxy, _ := newProxyTestService(t)
			seedReadyProxy(t, proxy, video, 10, time.Now().Add(-time.Hour))
			if stage == "discard" {
				if err := os.WriteFile(video.Path, []byte("source now changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			pool, _ := database.DB.DB()
			pool.SetMaxOpenConns(1)
			var held *sql.Conn
			var once sync.Once
			callback := func(tx *gorm.DB) {
				if tx.Statement.Table != "video_playback_proxies" {
					return
				}
				once.Do(func() {
					var err error
					held, err = pool.Conn(context.Background())
					if err != nil {
						tx.AddError(err)
					}
				})
			}
			name := "test:queue_proxy_wait"
			switch stage {
			case "read":
				database.DB.Callback().Query().Before("gorm:query").Register(name, callback)
				defer database.DB.Callback().Query().Remove(name)
			case "touch":
				database.DB.Callback().Update().Before("gorm:begin_transaction").Register(name, callback)
				defer database.DB.Callback().Update().Remove(name)
			case "discard":
				database.DB.Callback().Delete().Before("gorm:begin_transaction").Register(name, callback)
				defer database.DB.Callback().Delete().Remove(name)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := newProxyBackedVideoService(proxy).prepareQueueMedia(ctx, video.ID, true)
			if held != nil {
				held.Close()
			}
			if held == nil || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s did not cancel its actual SQL: held=%v err=%v", stage, held != nil, err)
			}
		})
	}
}

func TestQueueMediaReadErrorDoesNotFallBackToOriginal(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProxyTestVideo(t, "movie.mp4", 120)
	proxy, _ := newProxyTestService(t)
	seedReadyProxy(t, proxy, video, 10, time.Now())
	name := "test:proxy_read_failure"
	sentinel := errors.New("proxy database failed")
	database.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_playback_proxies" {
			tx.AddError(sentinel)
		}
	})
	defer database.DB.Callback().Query().Remove(name)
	svc := newProxyBackedVideoService(proxy)
	if _, err := svc.prepareQueueMedia(context.Background(), video.ID, true); !errors.Is(err, sentinel) {
		t.Fatalf("queue hid database error: %v", err)
	}
	// Ordinary preview's existing best-effort proxy policy stays unchanged.
	old, err := svc.GetPreviewSession(video.ID)
	if err != nil || old.Mode != "inline" || old.Proxy != nil {
		t.Fatalf("legacy behavior changed: %+v %v", old, err)
	}
}

func TestQueueMediaActiveReadFollowsContext(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProxyTestVideo(t, "movie.mp4", 120)
	proxy, _ := newProxyTestService(t)
	lease, err := newProxyBackedVideoService(proxy).prepareQueueMedia(context.Background(), video.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	pool, _ := database.DB.DB()
	pool.SetMaxOpenConns(1)
	held, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, _, _, err := lease.reader(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("active reader ignored context: %v", err)
	}
}
