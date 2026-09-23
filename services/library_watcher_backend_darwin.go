//go:build darwin && cgo

package services

/*
#cgo LDFLAGS: -framework CoreServices
#include "library_fsevents_darwin.h"
*/
import "C"

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/cgo"
	"strings"
	"sync"
	"unsafe"

	"github.com/fsnotify/fsnotify"
)

// One stream per configured root, independent of the number of files beneath it.
// Callback channels are bounded and nonblocking: overflow requests reconciliation,
// and Stop can always drain the native dispatch queue without waiting for s.mu.
type fseventsLibraryWatchBackend struct {
	mu      sync.Mutex
	streams map[string]*libraryFSEventStream
	events  chan fsnotify.Event
	errors  chan error
	closed  bool
}

type libraryFSEventStream struct {
	backend   *fseventsLibraryWatchBackend
	path      string
	canonical string
	handle    cgo.Handle
	native    *C.CineLibraryStream
}

func newLibraryWatchBackend() (libraryWatchBackend, error) {
	return &fseventsLibraryWatchBackend{
		streams: make(map[string]*libraryFSEventStream),
		events:  make(chan fsnotify.Event, 1024), errors: make(chan error, 1),
	}, nil
}

func (b *fseventsLibraryWatchBackend) Recursive() bool               { return true }
func (b *fseventsLibraryWatchBackend) Events() <-chan fsnotify.Event { return b.events }
func (b *fseventsLibraryWatchBackend) Errors() <-chan error          { return b.errors }

func (b *fseventsLibraryWatchBackend) Add(path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return fsnotify.ErrClosed
	}
	path = filepath.Clean(path)
	if b.streams[path] != nil {
		return nil
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("FSEvents requires a directory")
	}
	stream := &libraryFSEventStream{backend: b, path: path, canonical: canonical}
	stream.handle = cgo.NewHandle(stream)
	name := C.CString(canonical)
	stream.native = C.cineLibraryStart(name, C.uintptr_t(stream.handle))
	C.free(unsafe.Pointer(name))
	if stream.native == nil {
		stream.handle.Delete()
		return fmt.Errorf("无法启动 FSEvents 目录监听")
	}
	b.streams[path] = stream
	return nil
}

func (b *fseventsLibraryWatchBackend) Remove(path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if stream := b.streams[filepath.Clean(path)]; stream != nil {
		C.cineLibraryStop(stream.native)
		stream.handle.Delete()
		delete(b.streams, filepath.Clean(path))
	}
	return nil
}

func (b *fseventsLibraryWatchBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	for path, stream := range b.streams {
		C.cineLibraryStop(stream.native)
		stream.handle.Delete()
		delete(b.streams, path)
	}
	close(b.events)
	close(b.errors)
	return nil
}

//export cineLibraryFSEvent
func cineLibraryFSEvent(token C.uintptr_t, path *C.char, flags C.uint32_t) {
	stream := cgo.Handle(token).Value().(*libraryFSEventStream)
	stream.deliver(C.GoString(path), uint32(flags))
}

const (
	fseMustScan    = uint32(C.kFSEventStreamEventFlagMustScanSubDirs)
	fseDropped     = uint32(C.kFSEventStreamEventFlagUserDropped | C.kFSEventStreamEventFlagKernelDropped | C.kFSEventStreamEventFlagEventIdsWrapped)
	fseHistoryDone = uint32(C.kFSEventStreamEventFlagHistoryDone)
	fseRootChanged = uint32(C.kFSEventStreamEventFlagRootChanged)
	fseUnmount     = uint32(C.kFSEventStreamEventFlagUnmount)
	fseMount       = uint32(C.kFSEventStreamEventFlagMount)
	fseCreated     = uint32(C.kFSEventStreamEventFlagItemCreated)
	fseRemoved     = uint32(C.kFSEventStreamEventFlagItemRemoved)
	fseRenamed     = uint32(C.kFSEventStreamEventFlagItemRenamed)
	fseModified    = uint32(C.kFSEventStreamEventFlagItemModified)
	fseIsDir       = uint32(C.kFSEventStreamEventFlagItemIsDir)
	fseIsSymlink   = uint32(C.kFSEventStreamEventFlagItemIsSymlink)
)

func (b *fseventsLibraryWatchBackend) overflow() {
	select {
	case b.errors <- errLibraryWatchOverflow:
	default:
	}
}

func (stream *libraryFSEventStream) deliver(path string, flags uint32) {
	if flags&fseHistoryDone != 0 {
		return
	}
	if flags&fseDropped != 0 {
		stream.backend.overflow()
		return
	}
	path = filepath.Clean(path)
	if flags&fseRootChanged != 0 || flags&fseUnmount != 0 && pathBelongsToAny(stream.canonical, []string{path}) {
		stream.send(fsnotify.Event{Name: stream.path, Op: fsnotify.Remove})
		return
	}
	rel, err := filepath.Rel(stream.canonical, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		if flags&fseMustScan != 0 && pathBelongsToAny(stream.canonical, []string{path}) {
			stream.send(fsnotify.Event{Name: stream.path, Op: libraryWatchRescan})
		}
		return
	}
	path = filepath.Join(stream.path, rel)
	if flags&fseMustScan != 0 {
		stream.send(fsnotify.Event{Name: path, Op: libraryWatchRescan})
		return
	}
	if flags&fseIsSymlink != 0 {
		return
	}
	var op fsnotify.Op
	if flags&fseCreated != 0 {
		op |= fsnotify.Create
	}
	if flags&fseRemoved != 0 {
		op |= fsnotify.Remove
	}
	if flags&fseRenamed != 0 {
		// Both names are reported for a rename. Create makes the service check
		// whether this is an existing destination directory before reconciling.
		op |= fsnotify.Create | fsnotify.Rename
	}
	if flags&(fseModified|fseMount) != 0 {
		op |= fsnotify.Write
	}
	if op == 0 {
		return
	}
	if flags&fseIsDir != 0 && op&fsnotify.Remove == 0 {
		op |= libraryWatchRescan
	}
	stream.send(fsnotify.Event{Name: path, Op: op})
}

func (stream *libraryFSEventStream) send(event fsnotify.Event) {
	select {
	case stream.backend.events <- event:
	default:
		stream.backend.overflow()
	}
}
