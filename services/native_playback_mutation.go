package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func nativePathMatches(source string, paths []string) bool {
	source = filepath.Clean(source)
	for _, path := range paths {
		relative, err := filepath.Rel(filepath.Clean(path), source)
		if err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
			return true
		}
	}
	return false
}

// BeforeSourceMutation is called while the existing path writer owns its fence.
// It closes only matching native descriptors and never queries a database.
func (s *NativePlaybackService) BeforeSourceMutation(ctx context.Context, paths ...string) error {
	s.mu.Lock()
	var pending []*nativeOpenRecord
	var sessions []*nativeSession
	for _, owner := range s.owners {
		for _, record := range owner.records {
			if record.source != nil && nativePathMatches(record.source.path, paths) {
				record.cancel()
				pending = append(pending, record)
				if record.session != nil {
					sessions = append(sessions, record.session)
				}
			}
		}
	}
	s.mu.Unlock()
	for _, session := range sessions {
		session.mu.Lock()
		session.state.ErrorCode = "source_changed"
		session.state.ErrorMessage = "文件正在移动或删除，请重新打开"
		session.mu.Unlock()
		session.cancel()
	}
	for _, record := range pending {
		if err := waitNative(ctx, record.done); err != nil {
			return err
		}
		if record.session != nil {
			record.session.cancel()
			if err := waitNative(ctx, record.session.done); err != nil {
				return err
			}
		}
	}
	return nil
}

// The existing file mutation owners call this resource port under their path
// fence. The native service remains the only owner of decoder leases.
var nativeMutationCloser atomic.Pointer[func(context.Context, []string) (func(), error)]

func RegisterNativePlaybackSourceCloser(closer func(context.Context, []string) (func(), error)) func() {
	pointer := &closer
	nativeMutationCloser.Store(pointer)
	return func() { nativeMutationCloser.CompareAndSwap(pointer, nil) }
}
func beginNativeSourceMutation(paths ...string) (func(), error) {
	closer := nativeMutationCloser.Load()
	if closer == nil {
		return func() {}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return (*closer)(ctx, paths)
}
func nativeRemovePath(path string) error {
	release, err := beginNativeSourceMutation(path)
	if err != nil {
		return err
	}
	defer release()
	return os.Remove(path)
}
func nativeRemoveAllPaths(path string) error {
	release, err := beginNativeSourceMutation(path)
	if err != nil {
		return err
	}
	defer release()
	return os.RemoveAll(path)
}
func nativeRenamePath(oldPath, newPath string) error {
	release, err := beginNativeSourceMutation(oldPath)
	if err != nil {
		return err
	}
	defer release()
	return os.Rename(oldPath, newPath)
}
func nativeSystemTrashMove(path string) (string, error) {
	release, err := beginNativeSourceMutation(path)
	if err != nil {
		return "", err
	}
	defer release()
	return systemTrashMove(path)
}

func waitNativeGate(ctx context.Context, try func() bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if try() {
			return nil
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// This short gate is acquired only after the existing path read fence. It spans
// opening the FD and registering it, never decoder creation or playback.
func (s *NativePlaybackService) admitSource(ctx context.Context, path string) (func(), error) {
	if err := waitNativeGate(ctx, s.sourceGate.TryRLock); err != nil {
		return nil, err
	}
	s.mu.Lock()
	blocked := false
	for mutating := range s.mutatingPaths {
		if nativePathMatches(path, []string{mutating}) {
			blocked = true
			break
		}
	}
	s.mu.Unlock()
	if blocked {
		s.sourceGate.RUnlock()
		return nil, ErrQueueSourceChanged
	}
	return s.sourceGate.RUnlock, nil
}
func (s *NativePlaybackService) BeginSourceMutation(ctx context.Context, paths ...string) (func(), error) {
	if err := waitNativeGate(ctx, s.sourceGate.TryLock); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.mutatingPaths == nil {
		s.mutatingPaths = map[string]int{}
	}
	for _, path := range paths {
		s.mutatingPaths[filepath.Clean(path)]++
	}
	s.mu.Unlock()
	s.sourceGate.Unlock()
	release := sync.OnceFunc(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, path := range paths {
			key := filepath.Clean(path)
			s.mutatingPaths[key]--
			if s.mutatingPaths[key] == 0 {
				delete(s.mutatingPaths, key)
			}
		}
	})
	if err := s.BeforeSourceMutation(ctx, paths...); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
