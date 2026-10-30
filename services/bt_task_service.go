package services

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"video-master/database"
	"video-master/models"
	"video-master/services/btclient"
	"video-master/services/btmanifest"
)

// BTService serializes external task changes; downloader state remains in qBittorrent.
type BTService struct {
	deps            BTServiceDeps
	dataDir         string
	connection      *BTConnectionService
	serial          chan struct{}
	mu              sync.Mutex
	accepting       bool
	quiesced        bool
	stopped         bool
	configuring     bool
	operationCancel context.CancelFunc
	lifetime        context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	wake            chan struct{}
	clients         *BTConnectionClients
	remote          BTRemote
	search          *BTSearchService
	loadedID        string
	loadedRevision  uint64
	needsRecovery   bool
	pollCursor      time.Time
	pollID          string
	cacheSweep      *os.File
	cacheSweepScope string
}

func NewBTService(dataDir string, deps BTServiceDeps) *BTService {
	s := &BTService{dataDir: dataDir, deps: deps, serial: make(chan struct{}, 1), accepting: true, wake: make(chan struct{}, 1), needsRecovery: true}
	s.connection = NewBTConnectionService(deps.Credentials, BTConnectionHooks{Drain: s.drainConfiguration, Invalidate: s.invalidate, Resume: s.resumeConfiguration})
	return s
}
func (s *BTService) Connection() *BTConnectionService { return s.connection }
func (s *BTService) enter(ctx context.Context) (context.Context, func(), error) {
	select {
	case s.serial <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	s.mu.Lock()
	if !s.accepting {
		s.mu.Unlock()
		<-s.serial
		return nil, nil, ErrBTMaintenance
	}
	operation, cancel := context.WithCancel(ctx)
	s.operationCancel = cancel
	recoverNeeded := s.needsRecovery
	s.mu.Unlock()
	if recoverNeeded {
		if err := s.recoverTransient(operation); err != nil {
			cancel()
			s.mu.Lock()
			s.operationCancel = nil
			s.mu.Unlock()
			<-s.serial
			return nil, nil, err
		}
		s.mu.Lock()
		s.needsRecovery = false
		s.mu.Unlock()
	}
	return operation, func() { cancel(); s.mu.Lock(); s.operationCancel = nil; s.mu.Unlock(); <-s.serial }, nil
}
func (s *BTService) Drain(ctx context.Context) error {
	s.mu.Lock()
	s.accepting = false
	if s.operationCancel != nil {
		s.operationCancel()
	}
	s.mu.Unlock()
	select {
	case s.serial <- struct{}{}:
		<-s.serial
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *BTService) invalidate(revision uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients != nil {
		s.clients.Close()
	}
	s.clients = nil
	s.remote = nil
	s.loadedID = ""
	s.loadedRevision = 0
	s.pollCursor = time.Time{}
	s.pollID = ""
	if s.cacheSweep != nil {
		s.cacheSweep.Close()
		s.cacheSweep = nil
	}
	s.cacheSweepScope = ""
	if s.search != nil {
		s.search.Configure(nil, revision)
	}
}

// Configuration and maintenance own independent pause reasons. Clearing one
// reason cannot reopen another owner's pause, and terminal Stop always wins.
func (s *BTService) refreshAdmissionLocked() bool {
	s.accepting = !s.configuring && !s.quiesced && !s.stopped
	return s.accepting
}
func (s *BTService) drainConfiguration(ctx context.Context) error {
	s.mu.Lock()
	blocked := s.quiesced || s.stopped
	if !blocked {
		s.configuring = true
		s.refreshAdmissionLocked()
	}
	s.mu.Unlock()
	if blocked {
		return ErrBTMaintenance
	}
	return s.Drain(ctx)
}
func (s *BTService) resumeConfiguration() {
	s.mu.Lock()
	s.configuring = false
	allowed := s.refreshAdmissionLocked()
	s.mu.Unlock()
	if allowed {
		s.notify()
	}
}
func (s *BTService) Resume() {
	s.mu.Lock()
	s.quiesced = false
	allowed := s.refreshAdmissionLocked()
	s.mu.Unlock()
	if allowed {
		s.notify()
	}
}
func (s *BTService) Quiesce(ctx context.Context) error {
	s.mu.Lock()
	s.quiesced = true
	s.refreshAdmissionLocked()
	s.needsRecovery = true
	s.mu.Unlock()
	if err := s.Drain(ctx); err != nil {
		return err
	}
	s.invalidate(0)
	s.mu.Lock()
	s.needsRecovery = true
	s.mu.Unlock()
	return nil
}
func (s *BTService) Start(ctx context.Context) {
	s.mu.Lock()
	if s.done != nil || s.stopped {
		s.mu.Unlock()
		return
	}
	s.lifetime, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	s.mu.Unlock()
	go s.loop()
}
func (s *BTService) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.stopped = true
	s.refreshAdmissionLocked()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if err := s.Quiesce(ctx); err != nil {
		return err
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (s *BTService) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *BTService) loop() {
	defer close(s.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.lifetime.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(s.lifetime, 2*time.Minute)
		_ = s.Poll(ctx)
		_ = s.ExpireDrafts(ctx)
		cancel()
	}
}
func (s *BTService) loadClients(ctx context.Context) error {
	row, err := s.connection.current(ctx)
	if err != nil || row.ID == "" || !row.Enabled {
		return ErrBTConnection
	}
	s.mu.Lock()
	ready := s.clients != nil && s.loadedID == row.ID && s.loadedRevision == row.Revision
	s.mu.Unlock()
	if ready {
		return nil
	}
	pair, err := s.connection.Clients(ctx)
	if err != nil {
		return err
	}
	if err = pair.AuthenticateQB(ctx); err != nil {
		pair.Close()
		return err
	}
	var remote BTRemote = pair.QB
	if s.deps.RemoteFactory != nil {
		remote = s.deps.RemoteFactory(pair.QB)
	}
	if err = remote.CheckQBProfile(ctx); err != nil {
		pair.Close()
		return err
	}
	var source BTSearchSource = pair.Prowlarr
	if s.deps.SearchFactory != nil {
		source = s.deps.SearchFactory(pair.Prowlarr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients != nil {
		s.clients.Close()
	}
	s.clients = pair
	s.remote = remote
	s.loadedID = row.ID
	s.loadedRevision = row.Revision
	if s.search == nil {
		s.search = NewBTSearchService(source, row.Revision)
	} else {
		s.search.Configure(source, row.Revision)
	}
	return nil
}
func (s *BTService) Search(ctx context.Context, query string, indexerIDs []int) (*BTSearchResult, error) {
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	if err = s.loadClients(ctx); err != nil {
		return nil, err
	}
	return s.search.Search(ctx, query, indexerIDs)
}
func (s *BTService) Indexers(ctx context.Context) ([]BTIndexerView, error) {
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	if err = s.loadClients(ctx); err != nil {
		return nil, err
	}
	s.search.mu.Lock()
	source := s.search.source
	s.search.mu.Unlock()
	rows, err := source.Indexers(ctx)
	if err != nil {
		return nil, err
	}
	out := []BTIndexerView{}
	for _, row := range rows {
		if row.Protocol != "torrent" {
			continue
		}
		out = append(out, BTIndexerView{ID: row.ID, Name: row.Name, Protocol: row.Protocol, Enabled: row.Enable, Health: "unknown"})
	}
	return out, nil
}
func (s *BTService) changed(task models.BTTask) {
	if s.deps.Changed != nil {
		s.deps.Changed(task.ID, task.Revision)
	}
}
func (s *BTService) writeAllowed(ctx context.Context) error {
	if s.deps.EnsureNamespace == nil || s.deps.ImportFile == nil || s.deps.ValidateRoot == nil {
		return ErrBTConnection
	}
	if database.MaintenanceActive() {
		return ErrBTMaintenance
	}
	return ctx.Err()
}
func btErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrBTTaskBusy):
		return "bt_task_busy"
	case errors.Is(err, btmanifest.ErrInvalid):
		return "torrent_invalid"
	case errors.Is(err, ErrBTResultExpired):
		return "result_expired"
	case errors.Is(err, errBTMissing):
		return "download_missing"
	case errors.Is(err, ErrBTExternal):
		return "existing_external_torrent"
	case errors.Is(err, ErrBTChanged):
		return "external_changed"
	case errors.Is(err, ErrBTPathNotInLibrary):
		return "path_not_in_library"
	case errors.Is(err, ErrBTPath):
		return "path_unsafe"
	case errors.Is(err, ErrBTSelection):
		return "selection_invalid"
	case errors.Is(err, ErrBTMetadata):
		return "metadata_preview_unsupported"
	case errors.Is(err, ErrBTConflict):
		return "bt_conflict"
	case errors.Is(err, btclient.ErrAuthentication):
		return "auth_failed"
	default:
		return "service_unavailable"
	}
}

// TestConnection participates in ordinary command draining; Configure deliberately does not.
func (s *BTService) TestConnection(ctx context.Context) (BTConnectionTestResult, error) {
	operation, done, err := s.enter(ctx)
	if err != nil {
		return BTConnectionTestResult{}, err
	}
	defer done()
	return s.connection.Test(operation)
}
