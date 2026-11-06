//go:build darwin && cgo

// Package playback contains the low-level native renderer. It does not select files,
// record viewing, or grant frontend access; those responsibilities belong to its caller.
package playback

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Cocoa -framework WebKit -framework OpenGL
#include <stdlib.h>
#include "bridge.h"
*/
import "C"
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unsafe"
)

type Player struct {
	mu  sync.Mutex
	ptr *C.CineNativePlayer
}

func New(library string) (*Player, error) {
	canonical, err := filepath.EvalSymlinks(library)
	if err != nil {
		return nil, fmt.Errorf("native runtime path: %w", err)
	}
	library = canonical
	path := C.CString(library)
	defer C.free(unsafe.Pointer(path))
	var ptr *C.CineNativePlayer
	var message [1024]C.char
	if C.cine_native_create(path, &ptr, &message[0], 1024) < 0 {
		return nil, fmt.Errorf("native player: %s", C.GoString(&message[0]))
	}
	return &Player{ptr: ptr}, nil
}
func (p *Player) Load(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ptr == nil {
		return fmt.Errorf("native player closed")
	}
	value := C.CString(path)
	defer C.free(unsafe.Pointer(value))
	if code := C.cine_native_load(p.ptr, value); code < 0 {
		return fmt.Errorf("native load: %d", code)
	}
	return nil
}
func (p *Player) Command(kind string, value float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ptr == nil {
		return fmt.Errorf("native player closed")
	}
	name := C.CString(kind)
	defer C.free(unsafe.Pointer(name))
	if code := C.cine_native_command(p.ptr, name, C.double(value)); code < 0 {
		return fmt.Errorf("native command: %d", code)
	}
	return nil
}
func (p *Player) Viewport(x, y, w, h float64, visible bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ptr == nil {
		return fmt.Errorf("native player closed")
	}
	flag := 0
	if visible {
		flag = 1
	}
	if C.cine_native_viewport(p.ptr, C.double(x), C.double(y), C.double(w), C.double(h), C.int(flag)) < 0 {
		return fmt.Errorf("native_invalid_request: 播放区域超出窗口范围")
	}
	return nil
}
func (p *Player) Snapshot() (Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ptr == nil {
		return Snapshot{}, fmt.Errorf("native player closed")
	}
	data := C.cine_native_snapshot(p.ptr)
	defer C.cine_native_free(unsafe.Pointer(data))
	var result Snapshot
	err := json.Unmarshal([]byte(C.GoString(data)), &result)
	return result, err
}
func (p *Player) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ptr != nil {
		C.cine_native_destroy(p.ptr)
		p.ptr = nil
	}
}

// ProbePixels is a one-shot readback for the isolated composition fixture.
func (p *Player) ProbePixels() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ptr == nil {
		return -1
	}
	return int(C.cine_native_probe_pixels(p.ptr))
}

func (p *Player) Subtitles(document any) error {
	data, err := json.Marshal(document)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ptr == nil {
		return fmt.Errorf("native player closed")
	}
	value := C.CString(string(data))
	defer C.free(unsafe.Pointer(value))
	if C.cine_native_subtitles(p.ptr, value) < 0 {
		return fmt.Errorf("native subtitle configuration failed")
	}
	return nil
}
func (p *Player) ProbeClick(x, y float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ptr != nil {
		C.cine_native_probe_click(p.ptr, C.double(x), C.double(y))
	}
}

// RegisterWindow binds the unique initial Wails WebView, never the current key window.
func RegisterWindow() error {
	if C.cine_native_register_window() < 0 {
		return fmt.Errorf("native main WebView unavailable or ambiguous")
	}
	return nil
}
func UnregisterWindow()   { C.cine_native_unregister_window() }
func WindowEpoch() uint64 { return uint64(C.cine_native_window_epoch()) }

func (p *Player) ProbeDiagnostics() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ptr != nil {
		C.cine_native_probe_diagnostics(p.ptr)
	}
}

// LoadFile keeps the decoder on the descriptor that the source owner verified.
// fd:// borrows it; the caller closes it only after Player.Close completes.
func (p *Player) LoadFile(file *os.File) error { return p.Load(fmt.Sprintf("fd://%d", file.Fd())) }

func (p *Player) LoadFileAt(file *os.File, startMS int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ptr == nil {
		return fmt.Errorf("native player closed")
	}
	value := C.CString(fmt.Sprintf("fd://%d", file.Fd()))
	defer C.free(unsafe.Pointer(value))
	if code := C.cine_native_load_at(p.ptr, value, C.double(float64(startMS)/1000)); code < 0 {
		return fmt.Errorf("native load: %d", code)
	}
	return nil
}
