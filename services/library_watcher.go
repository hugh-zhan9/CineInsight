package services

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"video-master/models"

	"github.com/fsnotify/fsnotify"
)

type LibraryWatchState string

const (
	LibraryWatchStateWatching    LibraryWatchState = "watching"
	LibraryWatchStateUnavailable LibraryWatchState = "unavailable"
	LibraryWatchStateError       LibraryWatchState = "error"
	LibraryWatchStateDisabled    LibraryWatchState = "disabled"
)

type LibraryWatchRootStatus struct {
	DirectoryID uint              `json:"directory_id"`
	State       LibraryWatchState `json:"state"`
	ReasonCode  string            `json:"reason_code"`
	Message     string            `json:"message"`
	WatchCount  int               `json:"watch_count"`
	UpdatedAt   time.Time         `json:"updated_at" ts_type:"string"`
}

type LibraryWatcherStatus struct {
	Running bool                     `json:"running"`
	Roots   []LibraryWatchRootStatus `json:"roots"`
}

type LibraryReconcileSummary struct {
	Scanned           int `json:"scanned"`
	Added             int `json:"added"`
	Relocated         int `json:"relocated"`
	Stale             int `json:"stale"`
	MetadataRefreshed int `json:"metadata_refreshed"`
	Skipped           int `json:"skipped"`
	ErrorCount        int `json:"error_count"`
}

type LibraryReconcileEvent struct {
	DirectoryID uint                     `json:"directory_id"`
	Affected    int                      `json:"affected_directories"`
	Result      *LibraryReconcileSummary `json:"result,omitempty"`
	Error       string                   `json:"error,omitempty"`
	CompletedAt time.Time                `json:"completed_at" ts_type:"string"`
}

type libraryWatchBackend interface {
	Add(string) error
	Remove(string) error
	Close() error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
}

type fsnotifyLibraryWatchBackend struct {
	watcher *fsnotify.Watcher
}

func (b *fsnotifyLibraryWatchBackend) Add(path string) error         { return b.watcher.Add(path) }
func (b *fsnotifyLibraryWatchBackend) Remove(path string) error      { return b.watcher.Remove(path) }
func (b *fsnotifyLibraryWatchBackend) Close() error                  { return b.watcher.Close() }
func (b *fsnotifyLibraryWatchBackend) Events() <-chan fsnotify.Event { return b.watcher.Events }
func (b *fsnotifyLibraryWatchBackend) Errors() <-chan error          { return b.watcher.Errors }

type libraryWatchRoot struct {
	directory        models.ScanDirectory
	pendingDirectory *models.ScanDirectory
	watches          map[string]struct{}
	pending          map[string]struct{}
	due              time.Time
	processing       bool
	generation       uint64
	status           LibraryWatchRootStatus
}

type LibraryWatcherService struct {
	lifecycleMu         sync.Mutex
	mu                  sync.Mutex
	videoService        *VideoService
	backend             libraryWatchBackend
	backendFactory      func() (libraryWatchBackend, error)
	rootSupport         func(string) (bool, string, error)
	reconcile           func([]models.ScanDirectory, []string) *ScanSyncResult
	emitStatus          func(LibraryWatcherStatus)
	emitReconcile       func(LibraryReconcileEvent)
	roots               map[uint]*libraryWatchRoot
	watchRefs           map[string]int
	ctx                 context.Context
	cancel              context.CancelFunc
	running             bool
	coalesceWindow      time.Duration
	stabilityInterval   time.Duration
	stabilityTimeout    time.Duration
	tickInterval        time.Duration
	nextGeneration      uint64
	lastStatusSignature string
	loopWG              sync.WaitGroup
	workerWG            sync.WaitGroup
}

func NewLibraryWatcherService(videoService *VideoService) *LibraryWatcherService {
	service := &LibraryWatcherService{
		videoService:      videoService,
		roots:             make(map[uint]*libraryWatchRoot),
		watchRefs:         make(map[string]int),
		coalesceWindow:    750 * time.Millisecond,
		stabilityInterval: time.Second,
		stabilityTimeout:  30 * time.Second,
		tickInterval:      100 * time.Millisecond,
	}
	service.backendFactory = func() (libraryWatchBackend, error) {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, err
		}
		return &fsnotifyLibraryWatchBackend{watcher: watcher}, nil
	}
	service.rootSupport = classifyLibraryWatchRoot
	service.reconcile = func(dirs []models.ScanDirectory, affected []string) *ScanSyncResult {
		if service.videoService == nil {
			return &ScanSyncResult{Errors: []ScanSyncError{{Operation: "watch_reconcile", Error: "video service unavailable"}}}
		}
		return service.videoService.SyncAffectedDirectories(dirs, affected)
	}
	return service
}

func (s *LibraryWatcherService) SetEventEmitters(status func(LibraryWatcherStatus), reconcile func(LibraryReconcileEvent)) {
	s.mu.Lock()
	s.emitStatus = status
	s.emitReconcile = reconcile
	s.mu.Unlock()
}

func (s *LibraryWatcherService) Start(parent context.Context, dirs []models.ScanDirectory) error {
	// Serialize against Close: a restart that slips into an in-flight shutdown would be
	// torn down by it, and both would contend for the same wait groups.
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return s.Reconfigure(dirs)
	}
	backend, err := s.backendFactory()
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("create library watcher: %w", err)
	}
	if parent == nil {
		parent = context.Background()
	}
	s.ctx, s.cancel = context.WithCancel(parent)
	s.backend = backend
	s.running = true
	s.roots = make(map[uint]*libraryWatchRoot)
	s.watchRefs = make(map[string]int)
	s.loopWG.Add(1)
	go s.eventLoop(s.ctx, backend)
	s.mu.Unlock()

	if err := s.Reconfigure(dirs); err != nil {
		_ = s.closeLocked()
		return err
	}
	return nil
}

func (s *LibraryWatcherService) Reconfigure(dirs []models.ScanDirectory) error {
	s.mu.Lock()
	if !s.running || s.backend == nil {
		s.mu.Unlock()
		return fmt.Errorf("library watcher is not running")
	}
	desired := make(map[uint]models.ScanDirectory, len(dirs))
	for _, dir := range dirs {
		if dir.ID == 0 {
			continue
		}
		dir.Path = filepath.Clean(strings.TrimSpace(dir.Path))
		desired[dir.ID] = dir
	}
	for id, root := range s.roots {
		if _, exists := desired[id]; !exists {
			root.generation = s.newGenerationLocked()
			s.removeRootWatchesLocked(root)
			delete(s.roots, id)
		}
	}
	for id, dir := range desired {
		if current := s.roots[id]; current != nil && dir.Path == current.directory.Path {
			current.directory = dir
			if current.pendingDirectory != nil {
				current.pendingDirectory = nil
				current.status.State = LibraryWatchStateWatching
				current.status.ReasonCode = "watching"
				current.status.Message = "实时同步中"
				current.status.WatchCount = len(current.watches)
				current.status.UpdatedAt = time.Now()
			}
			continue
		}
		candidate := &libraryWatchRoot{
			directory:  dir,
			watches:    make(map[string]struct{}),
			pending:    make(map[string]struct{}),
			generation: s.newGenerationLocked(),
			status: LibraryWatchRootStatus{
				DirectoryID: id, State: LibraryWatchStateError, ReasonCode: "not_registered",
				Message: "目录尚未注册监听", UpdatedAt: time.Now(),
			},
		}
		if current := s.roots[id]; current != nil {
			current.generation = s.newGenerationLocked()
			current.processing = false
			current.pending = make(map[string]struct{})
			current.due = time.Time{}
			if err := s.registerRootLocked(candidate); err != nil {
				pending := dir
				current.pendingDirectory = &pending
				s.setRootErrorLocked(current, "path_update_failed", "新目录监听注册失败，仍保留原目录监听")
				continue
			}
			s.roots[id] = candidate
			s.removeRootWatchesLocked(current)
			continue
		}
		s.roots[id] = candidate
		_ = s.registerRootLocked(candidate)
	}
	snapshot, emitter := s.statusEmitLocked()
	s.mu.Unlock()
	if emitter != nil {
		emitter(snapshot)
	}
	return nil
}

func (s *LibraryWatcherService) RetryRoot(directoryID uint) (LibraryWatchRootStatus, error) {
	s.mu.Lock()
	root := s.roots[directoryID]
	if root == nil {
		s.mu.Unlock()
		return LibraryWatchRootStatus{}, fmt.Errorf("watch root %d not found", directoryID)
	}
	root.generation = s.newGenerationLocked()
	root.pending = make(map[string]struct{})
	root.due = time.Time{}
	root.processing = false
	target := root.directory
	if root.pendingDirectory != nil {
		target = *root.pendingDirectory
	}
	// Register the replacement first; watch refs are counted, so re-adding the same
	// paths is safe and a failed retry must never leave the root with zero watches.
	candidate := &libraryWatchRoot{
		directory:  target,
		watches:    make(map[string]struct{}),
		pending:    make(map[string]struct{}),
		generation: s.newGenerationLocked(),
	}
	err := s.registerRootLocked(candidate)
	if err == nil {
		s.roots[directoryID] = candidate
		s.removeRootWatchesLocked(root)
	} else if root.pendingDirectory != nil {
		s.setRootErrorLocked(root, "path_update_failed", "新目录监听注册失败，仍保留原目录监听")
	} else {
		root.status = candidate.status
		root.status.WatchCount = len(root.watches)
	}
	status := s.roots[directoryID].status
	snapshot, emitter := s.statusEmitLocked()
	s.mu.Unlock()
	if emitter != nil {
		emitter(snapshot)
	}
	return status, err
}

func (s *LibraryWatcherService) Snapshot() LibraryWatcherStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *LibraryWatcherService) Close() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.closeLocked()
}

// closeLocked runs the shutdown sequence; callers must hold lifecycleMu.
func (s *LibraryWatcherService) closeLocked() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	if s.cancel != nil {
		s.cancel()
	}
	backend := s.backend
	for _, root := range s.roots {
		root.pending = make(map[string]struct{})
		root.due = time.Time{}
		root.status.State = LibraryWatchStateDisabled
		root.status.ReasonCode = "disabled"
		root.status.Message = "实时同步已停止"
		root.status.UpdatedAt = time.Now()
	}
	s.mu.Unlock()

	var closeErr error
	if backend != nil {
		closeErr = backend.Close()
	}
	s.loopWG.Wait()
	s.workerWG.Wait()
	s.mu.Lock()
	s.backend = nil
	s.watchRefs = make(map[string]int)
	s.mu.Unlock()
	return closeErr
}

func (s *LibraryWatcherService) registerRootLocked(root *libraryWatchRoot) error {
	path := filepath.Clean(root.directory.Path)
	info, err := os.Stat(path)
	if err != nil {
		s.setRootUnavailableLocked(root, "root_unavailable", "扫描目录当前不可用")
		return err
	}
	if !info.IsDir() {
		err := fmt.Errorf("scan root is not a directory")
		s.setRootErrorLocked(root, "root_not_directory", "扫描根路径不是目录")
		return err
	}
	if s.rootSupport != nil {
		supported, reason, probeErr := s.rootSupport(path)
		if probeErr != nil {
			s.setRootErrorLocked(root, "filesystem_probe_failed", "无法识别扫描目录的文件系统")
			return probeErr
		}
		if !supported {
			if strings.TrimSpace(reason) == "" {
				reason = "此文件系统不支持可靠的实时监听"
			}
			s.setRootUnavailableLocked(root, "unsupported_filesystem", reason)
			return fmt.Errorf("unsupported filesystem")
		}
	}
	added := make([]string, 0)
	err = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if current != path && (strings.HasPrefix(entry.Name(), ".") || isTrashDirName(entry.Name())) {
			return filepath.SkipDir
		}
		current = filepath.Clean(current)
		if err := s.addWatchRefLocked(current); err != nil {
			return err
		}
		root.watches[current] = struct{}{}
		added = append(added, current)
		return nil
	})
	if err != nil {
		for _, current := range added {
			s.removeWatchRefLocked(current)
			delete(root.watches, current)
		}
		s.setRootErrorLocked(root, "watch_add_failed", fmt.Sprintf("目录监听注册失败：%s", libraryWatchReason(err)))
		return err
	}
	root.status = LibraryWatchRootStatus{
		DirectoryID: root.directory.ID,
		State:       LibraryWatchStateWatching,
		ReasonCode:  "watching",
		Message:     "实时同步中",
		WatchCount:  len(root.watches),
		UpdatedAt:   time.Now(),
	}
	return nil
}

func (s *LibraryWatcherService) registerCreatedSubtreeLocked(root *libraryWatchRoot, path string) error {
	path = filepath.Clean(path)
	return filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if current != path && (strings.HasPrefix(entry.Name(), ".") || isTrashDirName(entry.Name())) {
			return filepath.SkipDir
		}
		current = filepath.Clean(current)
		if _, exists := root.watches[current]; exists {
			return nil
		}
		if err := s.addWatchRefLocked(current); err != nil {
			return err
		}
		root.watches[current] = struct{}{}
		root.status.WatchCount = len(root.watches)
		root.status.UpdatedAt = time.Now()
		return nil
	})
}

func (s *LibraryWatcherService) removeRootWatchesLocked(root *libraryWatchRoot) {
	for path := range root.watches {
		s.removeWatchRefLocked(path)
	}
	root.watches = make(map[string]struct{})
	root.status.WatchCount = 0
}

func (s *LibraryWatcherService) removeSubtreeWatchesLocked(root *libraryWatchRoot, path string) {
	for watched := range root.watches {
		if pathBelongsToAny(watched, []string{path}) {
			s.removeWatchRefLocked(watched)
			delete(root.watches, watched)
		}
	}
	root.status.WatchCount = len(root.watches)
}

func (s *LibraryWatcherService) addWatchRefLocked(path string) error {
	if s.watchRefs[path] == 0 {
		if err := s.backend.Add(path); err != nil {
			return err
		}
	}
	s.watchRefs[path]++
	return nil
}

func (s *LibraryWatcherService) removeWatchRefLocked(path string) {
	count := s.watchRefs[path]
	if count <= 1 {
		delete(s.watchRefs, path)
		_ = s.backend.Remove(path)
		return
	}
	s.watchRefs[path] = count - 1
}

func (s *LibraryWatcherService) eventLoop(ctx context.Context, backend libraryWatchBackend) {
	defer s.loopWG.Done()
	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-backend.Events():
			if !ok {
				return
			}
			s.handleEvent(event)
		case err, ok := <-backend.Errors():
			if !ok {
				return
			}
			s.handleBackendError(err)
		case now := <-ticker.C:
			s.dispatchDueBatches(now)
		}
	}
}

func (s *LibraryWatcherService) handleEvent(event fsnotify.Event) {
	if event.Op == 0 || event.Op == fsnotify.Chmod {
		return
	}
	path := filepath.Clean(event.Name)
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") || isTrashPath(path) {
		return
	}
	// Stat once per event rather than once per root.
	createdDirectory := false
	if event.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			createdDirectory = true
		}
	}
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	for _, root := range s.roots {
		if !pathBelongsToAny(path, []string{root.directory.Path}) {
			continue
		}
		if path == filepath.Clean(root.directory.Path) && event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
			root.generation = s.newGenerationLocked()
			root.processing = false
			root.pending = make(map[string]struct{})
			root.due = time.Time{}
			s.removeRootWatchesLocked(root)
			s.setRootUnavailableLocked(root, "root_unavailable", "扫描目录当前不可用")
			continue
		}
		affected := filepath.Dir(path)
		if createdDirectory {
			if err := s.registerCreatedSubtreeLocked(root, path); err != nil {
				s.setRootErrorLocked(root, "watch_add_failed", fmt.Sprintf("新目录监听注册失败：%s", libraryWatchReason(err)))
			}
			affected = path
		}
		if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
			if _, watched := root.watches[path]; watched {
				s.removeSubtreeWatchesLocked(root, path)
			}
		}
		root.pending[affected] = struct{}{}
		root.due = now.Add(s.coalesceWindow)
	}
	snapshot, emitter := s.statusEmitLocked()
	s.mu.Unlock()
	if emitter != nil {
		emitter(snapshot)
	}
}

func (s *LibraryWatcherService) handleBackendError(err error) {
	s.mu.Lock()
	for _, root := range s.roots {
		s.setRootErrorLocked(root, "watch_backend_error", fmt.Sprintf("文件监听错误：%v", err))
	}
	snapshot, emitter := s.statusEmitLocked()
	s.mu.Unlock()
	if emitter != nil {
		emitter(snapshot)
	}
}

func (s *LibraryWatcherService) dispatchDueBatches(now time.Time) {
	type batch struct {
		rootID     uint
		generation uint64
		affected   []string
		dirs       []models.ScanDirectory
	}
	batches := make([]batch, 0)
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	dirs := s.configuredDirectoriesLocked()
	for id, root := range s.roots {
		if root.processing || len(root.pending) == 0 || root.due.IsZero() || now.Before(root.due) {
			continue
		}
		affected := make([]string, 0, len(root.pending))
		for path := range root.pending {
			affected = append(affected, path)
		}
		sort.Strings(affected)
		root.pending = make(map[string]struct{})
		root.due = time.Time{}
		root.processing = true
		batches = append(batches, batch{rootID: id, generation: root.generation, affected: affected, dirs: dirs})
	}
	for _, item := range batches {
		s.workerWG.Add(1)
		go s.processBatch(item.rootID, item.generation, item.dirs, item.affected)
	}
	s.mu.Unlock()
}

func (s *LibraryWatcherService) processBatch(rootID uint, generation uint64, dirs []models.ScanDirectory, affected []string) {
	defer s.workerWG.Done()
	if !s.batchIsCurrent(rootID, generation) {
		return
	}
	err := waitForStableDirectories(s.ctx, affected, s.stabilityInterval, s.stabilityTimeout)
	var result *ScanSyncResult
	if err == nil && s.batchIsCurrent(rootID, generation) {
		result = s.reconcile(dirs, affected)
		if result != nil && len(result.Errors) > 0 {
			err = fmt.Errorf("reconciliation completed with %d errors", len(result.Errors))
		}
	}

	s.mu.Lock()
	root := s.roots[rootID]
	current := root != nil && root.generation == generation
	if current {
		root.processing = false
		if err != nil && !errors.Is(err, context.Canceled) {
			reason := "reconcile_failed"
			if errors.Is(err, errLibraryWatchStabilityTimeout) {
				reason = "stability_timeout"
			}
			s.setRootErrorLocked(root, reason, libraryWatchReason(err))
		} else if err == nil {
			root.status.State = LibraryWatchStateWatching
			root.status.ReasonCode = "watching"
			root.status.Message = "实时同步中"
			root.status.UpdatedAt = time.Now()
		}
	}
	snapshot, statusEmitter := s.statusEmitLocked()
	reconcileEmitter := s.emitReconcile
	s.mu.Unlock()
	if statusEmitter != nil {
		statusEmitter(snapshot)
	}
	if reconcileEmitter != nil && current && !errors.Is(err, context.Canceled) {
		event := LibraryReconcileEvent{DirectoryID: rootID, Affected: len(affected), Result: summarizeLibraryReconciliation(result), CompletedAt: time.Now()}
		if err != nil {
			event.Error = libraryWatchReason(err)
		}
		reconcileEmitter(event)
	}
}

// libraryWatchReason keeps filesystem failures readable without putting library paths
// into WebView status messages or reconcile events.
func libraryWatchReason(err error) string {
	if err == nil {
		return ""
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return fmt.Sprintf("%s: %v", pathErr.Op, pathErr.Err)
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return fmt.Sprintf("%s: %v", linkErr.Op, linkErr.Err)
	}
	return err.Error()
}

func (s *LibraryWatcherService) batchIsCurrent(rootID uint, generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	root := s.roots[rootID]
	return s.running && root != nil && root.processing && root.generation == generation
}

func summarizeLibraryReconciliation(result *ScanSyncResult) *LibraryReconcileSummary {
	if result == nil {
		return nil
	}
	return &LibraryReconcileSummary{
		Scanned:           result.Scanned,
		Added:             result.Added,
		Relocated:         result.Relocated,
		Stale:             result.Stale,
		MetadataRefreshed: result.MetadataRefreshed,
		Skipped:           result.Skipped,
		ErrorCount:        len(result.Errors),
	}
}

func (s *LibraryWatcherService) configuredDirectoriesLocked() []models.ScanDirectory {
	dirs := make([]models.ScanDirectory, 0, len(s.roots))
	for _, root := range s.roots {
		dirs = append(dirs, root.directory)
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].ID < dirs[j].ID })
	return dirs
}

func (s *LibraryWatcherService) setRootErrorLocked(root *libraryWatchRoot, code, message string) {
	root.status = LibraryWatchRootStatus{
		DirectoryID: root.directory.ID,
		State:       LibraryWatchStateError,
		ReasonCode:  code,
		Message:     message,
		WatchCount:  len(root.watches),
		UpdatedAt:   time.Now(),
	}
}

func (s *LibraryWatcherService) setRootUnavailableLocked(root *libraryWatchRoot, code, message string) {
	root.status = LibraryWatchRootStatus{
		DirectoryID: root.directory.ID,
		State:       LibraryWatchStateUnavailable,
		ReasonCode:  code,
		Message:     message,
		WatchCount:  len(root.watches),
		UpdatedAt:   time.Now(),
	}
}

func (s *LibraryWatcherService) newGenerationLocked() uint64 {
	s.nextGeneration++
	return s.nextGeneration
}

// statusEmitLocked returns the snapshot plus the emitter to invoke after releasing s.mu.
// The emitter is nil when the reported status is unchanged, so an event burst (copying
// thousands of files) cannot flood the frontend with identical snapshots.
func (s *LibraryWatcherService) statusEmitLocked() (LibraryWatcherStatus, func(LibraryWatcherStatus)) {
	snapshot := s.snapshotLocked()
	signature := libraryWatchStatusSignature(snapshot)
	if signature == s.lastStatusSignature {
		return snapshot, nil
	}
	s.lastStatusSignature = signature
	return snapshot, s.emitStatus
}

func libraryWatchStatusSignature(status LibraryWatcherStatus) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "%t", status.Running)
	for _, root := range status.Roots {
		fmt.Fprintf(&builder, "\x00%d|%s|%s|%s|%d|%d",
			root.DirectoryID, root.State, root.ReasonCode, root.Message, root.WatchCount, root.UpdatedAt.UnixNano())
	}
	return builder.String()
}

func (s *LibraryWatcherService) snapshotLocked() LibraryWatcherStatus {
	status := LibraryWatcherStatus{Running: s.running, Roots: make([]LibraryWatchRootStatus, 0, len(s.roots))}
	for _, root := range s.roots {
		status.Roots = append(status.Roots, root.status)
	}
	sort.Slice(status.Roots, func(i, j int) bool { return status.Roots[i].DirectoryID < status.Roots[j].DirectoryID })
	return status
}

var errLibraryWatchStabilityTimeout = errors.New("files did not stabilize before timeout")

type libraryWatchFileSnapshot struct {
	Size      int64
	ModTimeNS int64
}

func waitForStableDirectories(ctx context.Context, directories []string, interval, timeout time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	previous, err := snapshotWatchDirectories(directories)
	if err != nil {
		return err
	}
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return errLibraryWatchStabilityTimeout
		case <-timer.C:
		}
		current, err := snapshotWatchDirectories(directories)
		if err != nil {
			return err
		}
		if watchSnapshotsEqual(previous, current) {
			return nil
		}
		previous = current
	}
}

func snapshotWatchDirectories(directories []string) (map[string]libraryWatchFileSnapshot, error) {
	snapshot := make(map[string]libraryWatchFileSnapshot)
	for _, directory := range directories {
		directory = filepath.Clean(directory)
		err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				if path != directory && (strings.HasPrefix(entry.Name(), ".") || isTrashDirName(entry.Name())) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(entry.Name(), ".") || isTrashPath(path) {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				snapshot[filepath.Clean(path)] = libraryWatchFileSnapshot{Size: info.Size(), ModTimeNS: info.ModTime().UnixNano()}
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				snapshot["!missing:"+directory] = libraryWatchFileSnapshot{Size: -1}
				continue
			}
			return nil, err
		}
	}
	return snapshot, nil
}

func watchSnapshotsEqual(left, right map[string]libraryWatchFileSnapshot) bool {
	if len(left) != len(right) {
		return false
	}
	for path, value := range left {
		if right[path] != value {
			return false
		}
	}
	return true
}
