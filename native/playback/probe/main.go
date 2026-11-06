//go:build nativeplaybackprobe && darwin

package main

import (
	"context"
	"fmt"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"video-master/native/playback"
	"video-master/services"
)

type ProbeApp struct {
	ownerQueue              *services.PlaybackQueueService
	ownerManager            *services.NativePlaybackService
	ownerVideos             *services.VideoService
	ownerSubtitles          *services.SubtitleWorkbenchService
	ownerDirectory          string
	ownerCloser             func()
	ready                   chan struct{}
	readyOnce               sync.Once
	readyErr                error
	ctx                     context.Context
	mu                      sync.Mutex
	player                  *playback.Player
	sourceFile              *os.File
	library, source, report string
}

func (p *ProbeApp) Create() error {
	<-p.ready
	if p.readyErr != nil {
		return p.readyErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.player != nil {
		return nil
	}
	if err := playback.RegisterWindow(); err != nil {
		return err
	}
	if _, err := playback.New(p.library + ".missing"); err == nil {
		return fmt.Errorf("invalid library accepted")
	}
	player, err := playback.New(p.library)
	if err != nil {
		return err
	}
	p.player = player
	if os.Getenv("CINE_NATIVE_PROBE_MODE") == "cold37" {
		player.ProbeDiagnostics()
	}
	file, err := os.Open(p.source)
	if err != nil {
		return err
	}
	p.sourceFile = file
	start, _ := strconv.ParseInt(os.Getenv("CINE_NATIVE_PROBE_START_MS"), 10, 64)
	return player.LoadFileAt(file, start)
}
func (p *ProbeApp) Command(kind string, value float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.player == nil {
		return fmt.Errorf("not open")
	}
	return p.player.Command(kind, value)
}
func (p *ProbeApp) Viewport(x, y, w, h float64, visible bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.player == nil {
		return fmt.Errorf("not open")
	}
	return p.player.Viewport(x, y, w, h, visible)
}
func (p *ProbeApp) Snapshot() (playback.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.player == nil {
		return playback.Snapshot{}, fmt.Errorf("not open")
	}
	return p.player.Snapshot()
}
func (p *ProbeApp) Subtitles(cues []map[string]interface{}, offset int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.player.Subtitles(map[string]interface{}{"cues": cues, "offset_ms": offset, "preferences": map[string]string{"size": "medium", "background": "solid", "position": "bottom"}})
}
func (p *ProbeApp) Click(x, y float64) { p.mu.Lock(); defer p.mu.Unlock(); p.player.ProbeClick(x, y) }
func (p *ProbeApp) Pixels() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.player == nil {
		return -1
	}
	return p.player.ProbePixels()
}
func (p *ProbeApp) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.player != nil {
		p.player.Close()
		p.player = nil
	}
	if p.sourceFile != nil {
		p.sourceFile.Close()
		p.sourceFile = nil
	}
}
func (p *ProbeApp) SampleIndex() int {
	value, _ := strconv.Atoi(os.Getenv("CINE_NATIVE_PROBE_INDEX"))
	return value
}
func (p *ProbeApp) Mode() string  { return os.Getenv("CINE_NATIVE_PROBE_MODE") }
func (p *ProbeApp) Epoch() uint64 { return playback.WindowEpoch() }
func (p *ProbeApp) Stress() error {
	for i := 0; i < 20; i++ {
		second, err := playback.New(p.library)
		if err != nil {
			return err
		}
		if err = second.Load(p.source); err != nil {
			second.Close()
			return err
		}
		_ = second.Command("play", 0)
		_ = second.Command("pause", 0)
		second.Close()
	}
	return nil
}

// Only the isolated fixture can supply this crafted source.
func (p *ProbeApp) RejectReferences() error {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(404) }))
	defer server.Close()
	file, err := os.CreateTemp("", "cine-native-reference-*.avi")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.WriteString("#EXTM3U\n" + server.URL + "/forbidden.mp4\n"); err != nil {
		file.Close()
		return err
	}
	file.Close()
	other, err := playback.New(p.library)
	if err != nil {
		return err
	}
	defer other.Close()
	if err = other.Load(file.Name()); err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, err := other.Snapshot()
		if err != nil {
			return err
		}
		if state.Error < 0 && state.Width == 0 && calls.Load() == 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("reference source was not rejected; network requests=%d", calls.Load())
}
func (p *ProbeApp) Finish(report string) error {
	if err := p.stopOwner(); err != nil {
		return err
	}
	p.Close()
	playback.UnregisterWindow()
	if err := os.WriteFile(p.report, []byte(report), 0600); err != nil {
		return err
	}
	go func() { time.Sleep(100 * time.Millisecond); runtime.Quit(p.ctx) }()
	return nil
}
func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: probe <assets> <libmpv> <synthetic-media> <report>")
		os.Exit(2)
	}
	app := &ProbeApp{ready: make(chan struct{}), library: os.Args[2], source: os.Args[3], report: os.Args[4]}
	if os.Getenv("CINE_NATIVE_PROBE_MODE") == "owner" {
		if err := app.setupOwner(); err != nil {
			panic(err)
		}
		defer os.RemoveAll(app.ownerDirectory)
	}
	err := wails.Run(&options.App{Title: "CineInsight native playback probe", Width: 960, Height: 720, AssetServer: &assetserver.Options{Assets: os.DirFS(os.Args[1])}, OnStartup: func(ctx context.Context) { app.ctx = ctx }, OnDomReady: func(context.Context) {
		app.readyOnce.Do(func() { app.readyErr = playback.RegisterWindow(); close(app.ready) })
		if app.ownerManager != nil {
			if err := app.ownerManager.RegisterWindow(app.ctx); err != nil {
				panic(err)
			}
			runtime.EventsEmit(app.ctx, "native-playback-ready", app.ownerManager.Capability())
		}
	}, OnBeforeClose: func(context.Context) bool { return app.stopOwner() != nil }, OnShutdown: func(context.Context) {
		app.Close()
		if app.ownerCloser != nil {
			app.ownerCloser()
		}
	}, Bind: []interface{}{app}, Mac: &mac.Options{TitleBar: mac.TitleBarDefault(), Preferences: &mac.Preferences{FullscreenEnabled: mac.Enabled}}})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
