package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"video-master/native/playback"
)

var ErrNativeOwnerRevoked = errors.New("native_owner_revoked: 播放宿主已关闭")
var ErrNativeSessionClosed = errors.New("native_session_closed: 播放会话已结束")
var ErrNativeConflict = errors.New("native_request_conflict: 相同请求标识的参数发生变化")

type nativePlayer interface {
	LoadFile(*os.File) error
	LoadFileAt(*os.File, int64) error
	Command(string, float64) error
	Viewport(float64, float64, float64, float64, bool) error
	Snapshot() (playback.Snapshot, error)
	Subtitles(any) error
	Close()
}
type nativeSource struct {
	file     *os.File
	path     string
	info     os.FileInfo
	videoID  uint
	version  string
	duration float64
	unlock   func()
}

func (n *nativeSource) releasePath() {
	if n.unlock != nil {
		n.unlock()
		n.unlock = nil
	}
}
func (n *nativeSource) close() {
	n.releasePath()
	if n.file != nil {
		n.file.Close()
	}
}

type nativeOpenRecord struct {
	digest  string
	done    chan struct{}
	cancel  context.CancelFunc
	source  *nativeSource
	session *nativeSession
	result  NativePlaybackState
	err     error
}
type nativeOwner struct {
	NativeOwner
	nonce   string
	epoch   uint64
	revoked bool
	records map[string]*nativeOpenRecord
	current *nativeSession
}
type nativeSession struct {
	commandMu        sync.Mutex
	appliedViewport  NativePlaybackViewport
	appliedVisible   bool
	ctx              context.Context
	subtitleWait     chan struct{}
	subtitleError    error
	mu               sync.Mutex
	id               string
	owner            *nativeOwner
	source           *nativeSource
	player           nativePlayer
	cancel           context.CancelFunc
	done             chan struct{}
	state            NativePlaybackState
	request          NativePlaybackOpenRequest
	commandError     error
	commandSeq       uint64
	commandDigest    string
	viewport         NativePlaybackViewport
	viewportDigest   string
	subtitleRevision uint64
	subtitleDigest   string
	subtitleResult   NativePlaybackSubtitleResult
	desiredPaused    bool
	seekTarget       *float64
	seekFrames       int64
	seekAt           time.Time
	lastLanding      float64
	advanced         bool
	initialized      bool
	closing          bool
	lastEmit         time.Time
}

// NativePlaybackService owns ephemeral decoder resources. Viewing facts remain
// with InlineVideoPlayer and PlaybackQueueService.
type NativePlaybackService struct {
	sourceGate       sync.RWMutex
	mutatingPaths    map[string]int
	authorizeQueue   func(context.Context, NativePlaybackOpenRequest) error
	windowRegistered bool
	registerWindow   func() error
	unregisterWindow func()
	mu               sync.Mutex
	audioMu          sync.Mutex
	owners           map[string]*nativeOwner
	nonces           map[string]string
	slots            int
	ready            bool
	quiesced         bool
	registeredEpoch  uint64
	runtime          *nativeRuntime
	epoch            func() uint64
	factory          func(string) (nativePlayer, error)
	resolve          func(context.Context, NativePlaybackOpenRequest) (*nativeSource, error)
	verify           func(context.Context, *nativeSource) error
	subtitle         func(context.Context, PlaybackSubtitleRequest) (*PlaybackSubtitle, error)
	emitter          func(NativePlaybackState)
	ctx              context.Context
	cancel           context.CancelFunc
	supervisorDone   chan struct{}
}

func NewNativePlaybackService(loader func(context.Context, PlaybackSubtitleRequest) (*PlaybackSubtitle, error)) *NativePlaybackService {
	ctx, cancel := context.WithCancel(context.Background())
	service := &NativePlaybackService{owners: map[string]*nativeOwner{}, nonces: map[string]string{}, runtime: &nativeRuntime{}, registerWindow: playback.RegisterWindow, unregisterWindow: playback.UnregisterWindow, epoch: playback.WindowEpoch, factory: func(path string) (nativePlayer, error) { return playback.New(path) }, resolve: resolveNativeSource, verify: verifyNativeSource, subtitle: loader, ctx: ctx, cancel: cancel}
	service.resolve = func(ctx context.Context, r NativePlaybackOpenRequest) (*nativeSource, error) {
		return resolveNativeSourceGuarded(ctx, r, service.admitSource)
	}
	return service
}
func (s *NativePlaybackService) SetEmitter(emit func(NativePlaybackState)) {
	s.mu.Lock()
	s.emitter = emit
	s.mu.Unlock()
}
func (s *NativePlaybackService) emit(state NativePlaybackState) {
	s.mu.Lock()
	emit := s.emitter
	s.mu.Unlock()
	if emit != nil {
		emit(state)
	}
}
func (s *NativePlaybackService) Capability() NativePlaybackCapability {
	capability, _ := s.runtime.probe()
	s.mu.Lock()
	capability.WindowEpoch = s.epoch()
	capability.WindowReady = s.ready && !s.quiesced && s.registeredEpoch == capability.WindowEpoch
	s.mu.Unlock()
	return capability
}
func (s *NativePlaybackService) RegisterWindow(ctx context.Context) error {
	if err := s.registerWindow(); err != nil {
		return err
	}
	s.mu.Lock()
	s.windowRegistered = true
	s.mu.Unlock()
	// Drain the previous document before admitting capabilities for the new one.
	epoch := s.epoch()
	if err := s.revokeOldEpoch(ctx, epoch); err != nil {
		return err
	}
	s.mu.Lock()
	if s.epoch() != epoch {
		s.mu.Unlock()
		return ErrNativeOwnerRevoked
	}
	s.registeredEpoch = epoch
	s.ready = true
	if s.supervisorDone == nil {
		s.supervisorDone = make(chan struct{})
		go s.supervise()
	}
	s.mu.Unlock()
	return nil
}
func (s *NativePlaybackService) supervise() {
	defer close(s.supervisorDone)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			changed := s.ready && s.registeredEpoch != s.epoch()
			if changed {
				s.ready = false
			}
			s.mu.Unlock()
			if changed {
				_ = s.revokeOldEpoch(s.ctx, s.epoch())
			}
		}
	}
}
func nativeToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
func nativeDigest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (s *NativePlaybackService) Acquire(request NativeOwnerRequest) (NativeOwner, error) {
	if request.ComponentNonce == "" || len(request.ComponentNonce) > 128 || request.WindowEpoch == 0 {
		return NativeOwner{}, errors.New("native_invalid_request: 播放宿主标识无效")
	}
	if cap := s.Capability(); !cap.Available {
		return NativeOwner{}, fmt.Errorf("%s: %s", cap.ReasonCode, cap.ReasonMessage)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quiesced || s.ctx.Err() != nil {
		return NativeOwner{}, ErrPlaybackQuiesced
	}
	if !s.ready || s.registeredEpoch != s.epoch() || request.WindowEpoch != s.registeredEpoch {
		return NativeOwner{}, ErrNativeOwnerRevoked
	}
	if id := s.nonces[request.ComponentNonce]; id != "" {
		if owner := s.owners[id]; owner != nil && !owner.revoked && owner.epoch == s.registeredEpoch {
			return owner.NativeOwner, nil
		}
	}
	if len(s.owners) >= 4 {
		return NativeOwner{}, errors.New("native_owner_limit: 播放宿主数量已达上限")
	}
	id, err := nativeToken()
	if err != nil {
		return NativeOwner{}, err
	}
	token, err := nativeToken()
	if err != nil {
		return NativeOwner{}, err
	}
	owner := &nativeOwner{NativeOwner: NativeOwner{OwnerID: id, OwnerToken: token}, nonce: request.ComponentNonce, epoch: s.registeredEpoch, records: map[string]*nativeOpenRecord{}}
	s.owners[id] = owner
	s.nonces[owner.nonce] = id
	return owner.NativeOwner, nil
}
func (s *NativePlaybackService) ownerLocked(id, token string) (*nativeOwner, error) {
	owner := s.owners[id]
	if owner == nil || owner.OwnerToken != token || owner.revoked || owner.epoch != s.epoch() || !s.ready || s.quiesced {
		return nil, ErrNativeOwnerRevoked
	}
	return owner, nil
}
func nativeOpenValid(r NativePlaybackOpenRequest) bool {
	return r.RequestID != "" && len(r.RequestID) <= 128 && r.VideoID > 0 && r.VideoID <= experienceMaxSafeInteger && r.SourceVersion != "" && len(r.SourceVersion) <= 128 && len(r.QueueToken) <= 128 && r.StartMS >= 0 && r.StartMS <= 9007199254740991 && (r.Volume == nil || (!math.IsNaN(*r.Volume) && !math.IsInf(*r.Volume, 0) && *r.Volume >= 0 && *r.Volume <= 1))
}
func (s *NativePlaybackService) Open(ctx context.Context, r NativePlaybackOpenRequest) (NativePlaybackState, error) {
	if !nativeOpenValid(r) {
		return NativePlaybackState{}, errors.New("native_invalid_request: 播放参数无效")
	}
	cap, library := s.runtime.probe()
	if !cap.Available {
		return NativePlaybackState{}, fmt.Errorf("%s: %s", cap.ReasonCode, cap.ReasonMessage)
	}
	digest := nativeDigest(r)
	s.mu.Lock()
	owner, err := s.ownerLocked(r.OwnerID, r.OwnerToken)
	if err != nil {
		s.mu.Unlock()
		return NativePlaybackState{}, err
	}
	if old := owner.records[r.RequestID]; old != nil {
		s.mu.Unlock()
		if old.digest != digest {
			return NativePlaybackState{}, ErrNativeConflict
		}
		select {
		case <-ctx.Done():
			return NativePlaybackState{}, ctx.Err()
		case <-old.done:
			return old.result, old.err
		}
	}
	if owner.current != nil || s.slots >= 2 || len(owner.records) >= 64 {
		s.mu.Unlock()
		return NativePlaybackState{}, errors.New("native_session_limit: 请先关闭之前的播放会话")
	}
	// A pending open is a slot too; a second request cannot bypass admission.
	for _, record := range owner.records {
		select {
		case <-record.done:
		default:
			s.mu.Unlock()
			return NativePlaybackState{}, errors.New("native_session_limit: 播放会话正在准备")
		}
	}
	pending, cancel := context.WithCancel(ctx)
	record := &nativeOpenRecord{digest: digest, done: make(chan struct{}), cancel: cancel}
	owner.records[r.RequestID] = record
	s.slots++
	authorizeQueue := s.authorizeQueue
	s.mu.Unlock()
	var source *nativeSource
	var player nativePlayer
	committed := false
	defer func() {
		cancel()
		if !committed {
			if player != nil {
				player.Close()
			}
			if source != nil {
				source.close()
			}
			s.mu.Lock()
			s.slots--
			s.mu.Unlock()
		}
		s.mu.Lock()
		record.err = err
		close(record.done)
		s.mu.Unlock()
	}()
	if r.QueueToken != "" {
		if authorizeQueue == nil {
			err = ErrQueueLeaseExpired
			return NativePlaybackState{}, err
		}
		if err = authorizeQueue(pending, r); err != nil {
			return NativePlaybackState{}, err
		}
	}
	source, err = s.resolve(pending, r)
	if err != nil {
		return NativePlaybackState{}, err
	}
	s.mu.Lock()
	record.source = source
	s.mu.Unlock()
	source.releasePath()
	if err = pending.Err(); err != nil {
		return NativePlaybackState{}, err
	}
	if source.duration > 0 && float64(r.StartMS) > source.duration*1000 {
		err = errors.New("native_invalid_request: 起播位置超出片长")
		return NativePlaybackState{}, err
	}
	player, err = s.factory(library)
	if err != nil {
		return NativePlaybackState{}, err
	}
	if err = player.LoadFileAt(source.file, r.StartMS); err != nil {
		return NativePlaybackState{}, err
	}
	muted := true
	if r.Muted != nil {
		muted = *r.Muted
	}
	volume := 1.0
	if r.Volume != nil {
		volume = *r.Volume
	}
	if err = player.Command("volume", volume); err == nil {
		value := 0.0
		if muted {
			value = 1
		}
		err = player.Command("muted", value)
	}
	if err != nil {
		return NativePlaybackState{}, err
	}
	id, tokenErr := nativeToken()
	if tokenErr != nil {
		err = tokenErr
		return NativePlaybackState{}, err
	}
	s.mu.Lock()
	_, err = s.ownerLocked(r.OwnerID, r.OwnerToken)
	if err == nil {
		err = pending.Err()
	}
	if err != nil {
		s.mu.Unlock()
		return NativePlaybackState{}, err
	}
	run, cancelRun := context.WithCancel(s.ctx)
	session := &nativeSession{ctx: run, id: id, owner: owner, source: source, player: player, cancel: cancelRun, done: make(chan struct{}), request: r, desiredPaused: r.Paused, state: NativePlaybackState{SessionID: id, VideoID: r.VideoID, SourceVersion: r.SourceVersion, State: "opening", Paused: true, Muted: muted, Volume: volume, Rate: 1}}
	owner.current = session
	record.session = session
	record.result = session.state
	committed = true
	s.mu.Unlock()
	go s.runSession(run, session)
	return record.result, nil
}
func waitNative(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *NativePlaybackService) Release(ctx context.Context, ref NativeOwnerRequestRef) error {
	s.mu.Lock()
	owner := s.owners[ref.OwnerID]
	if owner == nil {
		s.mu.Unlock()
		return nil
	}
	if owner.OwnerToken != ref.OwnerToken {
		s.mu.Unlock()
		return ErrNativeOwnerRevoked
	}
	owner.revoked = true
	records := make([]*nativeOpenRecord, 0, len(owner.records))
	for _, record := range owner.records {
		record.cancel()
		records = append(records, record)
	}
	session := owner.current
	s.mu.Unlock()
	if session != nil {
		session.cancel()
	}
	for _, record := range records {
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
	s.mu.Lock()
	if s.owners[owner.OwnerID] == owner {
		delete(s.owners, owner.OwnerID)
		if s.nonces[owner.nonce] == owner.OwnerID {
			delete(s.nonces, owner.nonce)
		}
	}
	s.mu.Unlock()
	return nil
}
func (s *NativePlaybackService) revoke(ctx context.Context, quiesce bool) error {
	s.mu.Lock()
	if quiesce {
		s.quiesced = true
	}
	refs := make([]NativeOwnerRequestRef, 0, len(s.owners))
	for _, owner := range s.owners {
		owner.revoked = true
		for _, record := range owner.records {
			record.cancel()
		}
		refs = append(refs, NativeOwnerRequestRef{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken})
	}
	s.mu.Unlock()
	for _, ref := range refs {
		if err := s.Release(ctx, ref); err != nil {
			return err
		}
	}
	return nil
}
func (s *NativePlaybackService) Quiesce(ctx context.Context) error { return s.revoke(ctx, true) }
func (s *NativePlaybackService) Resume() {
	s.mu.Lock()
	if s.ctx.Err() == nil {
		s.quiesced = false
	}
	s.mu.Unlock()
}
func (s *NativePlaybackService) Stop(ctx context.Context) error {
	if err := s.Quiesce(ctx); err != nil {
		return err
	}
	s.cancel()
	s.mu.Lock()
	done := s.supervisorDone
	registered := s.windowRegistered
	s.ready = false
	s.mu.Unlock()
	if done != nil {
		if err := waitNative(ctx, done); err != nil {
			return err
		}
	}
	if registered {
		s.unregisterWindow()
		s.mu.Lock()
		s.windowRegistered = false
		s.mu.Unlock()
	}
	return nil
}
func (s *NativePlaybackService) session(ref NativePlaybackRef) (*nativeSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, err := s.ownerLocked(ref.OwnerID, ref.OwnerToken)
	if err != nil {
		return nil, err
	}
	if owner.current == nil || owner.current.id != ref.SessionID {
		return nil, ErrNativeSessionClosed
	}
	return owner.current, nil
}
func (s *NativePlaybackService) Snapshot(ref NativePlaybackRef) (NativePlaybackState, error) {
	session, err := s.session(ref)
	if err != nil {
		return NativePlaybackState{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closing && session.state.State != "error" {
		return NativePlaybackState{}, ErrNativeSessionClosed
	}
	return session.state, nil
}
func (s *NativePlaybackService) Close(ctx context.Context, r NativePlaybackCloseRequest) error {
	switch r.Reason {
	case "navigate", "replaced", "hidden", "maintenance", "shutdown", "user":
	default:
		return errors.New("native_invalid_request: 关闭原因无效")
	}
	session, err := s.session(NativePlaybackRef{OwnerID: r.OwnerID, OwnerToken: r.OwnerToken, SessionID: r.SessionID})
	if errors.Is(err, ErrNativeSessionClosed) {
		return nil
	}
	if err != nil {
		return err
	}
	session.cancel()
	if err := waitNative(ctx, session.done); err != nil {
		return err
	}
	s.mu.Lock()
	if session.owner.current == session {
		session.owner.current = nil
	}
	s.mu.Unlock()
	return nil
}

func (s *NativePlaybackService) revokeOldEpoch(ctx context.Context, epoch uint64) error {
	s.mu.Lock()
	var refs []NativeOwnerRequestRef
	for _, owner := range s.owners {
		if owner.epoch < epoch {
			owner.revoked = true
			for _, record := range owner.records {
				record.cancel()
			}
			refs = append(refs, NativeOwnerRequestRef{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken})
		}
	}
	s.mu.Unlock()
	for _, ref := range refs {
		if err := s.Release(ctx, ref); err != nil {
			return err
		}
	}
	return nil
}
func (s *NativePlaybackService) SetQueueAuthorizer(authorize func(context.Context, NativePlaybackOpenRequest) error) {
	s.mu.Lock()
	s.authorizeQueue = authorize
	s.mu.Unlock()
}
