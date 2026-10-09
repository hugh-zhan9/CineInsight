package services

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

type fakeWallpaperPlatform struct {
	available                   bool
	width, height               int
	status                      wallpaperNativeStatus
	inspect                     func()
	inspectErr, setErr, stopErr error
	imagePath, videoPath        string
	starts, stops               int
}

func (p *fakeWallpaperPlatform) Available() bool { return p.available }
func (p *fakeWallpaperPlatform) Inspect(string, string) (int, int, error) {
	if p.inspect != nil {
		p.inspect()
	}
	return p.width, p.height, p.inspectErr
}
func (p *fakeWallpaperPlatform) SetImage(path string) error {
	if p.setErr == nil {
		p.imagePath = path
		p.Stop()
	}
	return p.setErr
}
func (p *fakeWallpaperPlatform) StartVideo(path string) error {
	if p.setErr == nil {
		p.videoPath = path
		p.starts++
		p.status = wallpaperNativeStatus{state: "playing"}
	}
	return p.setErr
}
func (p *fakeWallpaperPlatform) Status() wallpaperNativeStatus { return p.status }
func (p *fakeWallpaperPlatform) Stop() error {
	if p.stopErr != nil {
		return p.stopErr
	}
	p.stops++
	p.status = wallpaperNativeStatus{state: "idle"}
	return nil
}
func (p *fakeWallpaperPlatform) Close() { p.Stop() }

func wallpaperTestService(t *testing.T) (*WallpaperService, *fakeWallpaperPlatform, *wallpaperMedia) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.jpg")
	if err := os.WriteFile(path, []byte("original image bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	media := &wallpaperMedia{path: path, name: "sample", width: 1920, height: 1080}
	native := &fakeWallpaperPlatform{available: true, width: 1920, height: 1080, status: wallpaperNativeStatus{state: "idle"}}
	service := &WallpaperService{native: native, dataDir: t.TempDir(), load: func(string, uint) (wallpaperMedia, error) { return *media, nil }}
	return service, native, media
}

func TestWallpaperResolutionBoundaries(t *testing.T) {
	for _, test := range []struct {
		width, height int
		ok            bool
	}{
		{1920, 720, true}, {720, 1920, true}, {1920, 800, true}, {1080, 1920, true}, {3840, 2160, true},
		{1280, 720, false}, {1920, 600, false}, {1919, 1080, false}, {1920, 719, false}, {0, 1080, false}, {-1, 2160, false},
	} {
		if got := wallpaperResolutionOK(test.width, test.height); got != test.ok {
			t.Errorf("%dx%d: got %v", test.width, test.height, got)
		}
	}
}

func TestWallpaperPreflightRejectsWithoutStarting(t *testing.T) {
	for _, test := range []struct {
		name, code string
		change     func(*WallpaperService, *fakeWallpaperPlatform, *wallpaperMedia)
		kind       string
		id         uint
	}{
		{"unsupported", "unsupported_platform", func(_ *WallpaperService, p *fakeWallpaperPlatform, _ *wallpaperMedia) { p.available = false }, "video", 1},
		{"invalid kind", "invalid_media", nil, "audio", 1}, {"zero ID", "invalid_media", nil, "video", 0},
		{"stale", "media_unavailable", func(_ *WallpaperService, _ *fakeWallpaperPlatform, m *wallpaperMedia) { m.stale = true }, "video", 1},
		{"deleted", "media_unavailable", func(s *WallpaperService, _ *fakeWallpaperPlatform, _ *wallpaperMedia) {
			s.load = func(string, uint) (wallpaperMedia, error) { return wallpaperMedia{}, errors.New("deleted") }
		}, "video", 1},
		{"missing", "file_missing", func(_ *WallpaperService, _ *fakeWallpaperPlatform, m *wallpaperMedia) { os.Remove(m.path) }, "video", 1},
		{"directory", "file_unreadable", func(_ *WallpaperService, _ *fakeWallpaperPlatform, m *wallpaperMedia) { m.path = filepath.Dir(m.path) }, "image", 1},
		{"unknown", "dimensions_unknown", func(_ *WallpaperService, _ *fakeWallpaperPlatform, m *wallpaperMedia) { m.width = 0 }, "image", 1},
		{"low", "resolution_too_low", func(_ *WallpaperService, _ *fakeWallpaperPlatform, m *wallpaperMedia) { m.width = 1280; m.height = 720 }, "video", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, p, m := wallpaperTestService(t)
			if test.change != nil {
				test.change(s, p, m)
			}
			check := s.Preflight(test.kind, test.id)
			if check.Eligible || check.ReasonCode != test.code || check.Message == "" {
				t.Fatalf("unexpected preflight: %+v", check)
			}
			if _, err := s.Set(test.kind, test.id); err == nil {
				t.Fatal("set accepted rejected media")
			}
			if p.starts != 0 || p.imagePath != "" {
				t.Fatal("rejected media altered wallpaper")
			}
		})
	}
}

func TestWallpaperSetPreservesOldVideoOnFailedReplacement(t *testing.T) {
	for _, reason := range []string{"actual dimensions", "decode", "changed source", "native start"} {
		t.Run(reason, func(t *testing.T) {
			s, p, m := wallpaperTestService(t)
			if _, err := s.Set("video", 1); err != nil {
				t.Fatal(err)
			}
			switch reason {
			case "actual dimensions":
				p.width = 1280
				p.height = 720
			case "decode":
				p.inspectErr = errors.New("unsupported codec")
			case "changed source":
				p.inspect = func() {
					if err := os.WriteFile(m.path, []byte("changed"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "native start":
				p.setErr = errors.New("cannot create windows")
			}
			status, err := s.Set("video", 2)
			if err == nil || status.VideoID != 1 || status.State != "playing" || p.starts != 1 {
				t.Fatalf("old wallpaper not protected: %+v, %v", status, err)
			}
		})
	}
}

func TestWallpaperImageCopyAndStop(t *testing.T) {
	s, p, m := wallpaperTestService(t)
	if _, err := s.Set("video", 1); err != nil {
		t.Fatal(err)
	}
	status, err := s.Set("image", 2)
	if err != nil {
		t.Fatal(err)
	}
	if status.VideoID != 0 || status.State != "idle" || p.stops != 1 {
		t.Fatalf("video not stopped: %+v", status)
	}
	if filepath.Dir(p.imagePath) != filepath.Join(s.dataDir, "wallpapers") || p.imagePath == m.path {
		t.Fatalf("not an independent copy: %s", p.imagePath)
	}
	want, err := os.ReadFile(m.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(m.path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p.imagePath)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("copy lost after source deletion: %v", err)
	}
	s.Stop()
	s.Stop()
	if _, err := os.Stat(p.imagePath); err != nil {
		t.Fatal("stop removed static wallpaper")
	}
}

func TestWallpaperFailedImageKeepsDynamicVideo(t *testing.T) {
	s, p, _ := wallpaperTestService(t)
	if _, err := s.Set("video", 1); err != nil {
		t.Fatal(err)
	}
	p.setErr = errors.New("system refused image")
	status, err := s.Set("image", 2)
	if err == nil || status.VideoID != 1 || p.stops != 0 {
		t.Fatalf("failed image stopped old video: %+v %v", status, err)
	}
}

func TestWallpaperFailedStopKeepsVisibleSession(t *testing.T) {
	s, p, _ := wallpaperTestService(t)
	if _, err := s.Set("video", 1); err != nil {
		t.Fatal(err)
	}
	p.stopErr = errors.New("system is not responding")
	status, err := s.Stop()
	if err == nil || status.VideoID != 1 || status.State != "playing" {
		t.Fatalf("failed stop hid a running wallpaper: %+v %v", status, err)
	}
	p.stopErr = nil
	status, err = s.Stop()
	if err != nil || status.State != "idle" || status.VideoID != 0 {
		t.Fatalf("explicit retry did not stop: %+v %v", status, err)
	}
}

func TestWallpaperCopyRejectsUnavailableDataDirAndLargeImage(t *testing.T) {
	s, p, m := wallpaperTestService(t)
	s.dataDir = ""
	if _, err := s.Set("image", 1); err == nil || p.imagePath != "" {
		t.Fatal("published image without data directory")
	}
	file, err := os.OpenFile(m.path, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(wallpaperMaxImageSize + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if s.Preflight("image", 1).Eligible {
		t.Fatal("oversized image accepted")
	}
}

func TestWallpaperCloseSerializesWithSet(t *testing.T) {
	s, p, _ := wallpaperTestService(t)
	entered, release := make(chan struct{}), make(chan struct{})
	p.inspect = func() { close(entered); <-release }
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); s.Set("video", 1) }()
	<-entered
	go func() { defer workers.Done(); s.Close() }()
	close(release)
	workers.Wait()
	s.Close()
	s.Stop()
	if _, err := s.Set("video", 2); err == nil {
		t.Fatal("set resurrected closed service")
	}
	if status := s.Status(); status.VideoID != 0 || status.State != "idle" || p.starts != 1 {
		t.Fatalf("bad final state: %+v", status)
	}
}

func TestWallpaperDoesNotWriteViewingState(t *testing.T) {
	database.DB = dbtest.Open(t)
	s, p, m := wallpaperTestService(t)
	s.load = loadWallpaperMedia
	video := models.Video{Name: "sample", Path: m.path, Width: 1920, Height: 1080, PlayCount: 7, RandomPlayCount: 3, WatchPositionSeconds: 42}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set("video", video.ID); err != nil {
		t.Fatal(err)
	}
	p.status = wallpaperNativeStatus{state: "failed", message: "decode failed"}
	if s.Status().State != "failed" {
		t.Fatal("asynchronous failure hidden")
	}
	s.Stop()
	var after models.Video
	if err := database.DB.First(&after, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.PlayCount != 7 || after.RandomPlayCount != 3 || after.WatchPositionSeconds != 42 || after.IsWatched {
		t.Fatalf("viewing state changed: %+v", after)
	}
	var events int64
	database.DB.Model(&models.PlayEvent{}).Where("video_id = ?", video.ID).Count(&events)
	if events != 0 {
		t.Fatal("wallpaper created viewing events")
	}
}
