package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"video-master/native/playback"
)

type fakeNativePlayer struct {
	mu          sync.Mutex
	state       playback.Snapshot
	closed      atomic.Int32
	documents   int
	commandHook func(string) error
}

func (p *fakeNativePlayer) LoadFile(*os.File) error { return nil }
func (p *fakeNativePlayer) Close()                  { p.closed.Add(1) }
func (p *fakeNativePlayer) Subtitles(any) error {
	p.mu.Lock()
	p.documents++
	p.mu.Unlock()
	return nil
}
func (p *fakeNativePlayer) Viewport(float64, float64, float64, float64, bool) error { return nil }
func (p *fakeNativePlayer) Snapshot() (playback.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.Frames++
	p.state.DecodedFrames++
	if !p.state.Paused && !p.state.Ended {
		p.state.Position += .1
	}
	return p.state, nil
}
func (p *fakeNativePlayer) Command(kind string, value float64) error {
	if p.commandHook != nil {
		if err := p.commandHook(kind); err != nil {
			return err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch kind {
	case "play":
		p.state.Paused = false
	case "pause":
		p.state.Paused = true
	case "seek":
		p.state.Position = value
		p.state.Frames++
		p.state.DecodedFrames++
		p.state.Ended = false
	case "volume":
		p.state.Volume = value
	case "muted":
		p.state.Muted = value != 0
	case "rate":
		p.state.Rate = value
	}
	return nil
}
func nativeManagerFixture(t *testing.T) (*NativePlaybackService, *atomic.Uint64, chan *fakeNativePlayer) {
	t.Helper()
	service := NewNativePlaybackService(nil)
	epoch := &atomic.Uint64{}
	epoch.Store(1)
	service.epoch = epoch.Load
	service.registeredEpoch = 1
	service.ready = true
	service.registerWindow = func() error { return nil }
	service.unregisterWindow = func() {}
	service.runtime = &nativeRuntime{checked: true, capability: NativePlaybackCapability{Available: true}, library: "fixture"}
	path := filepath.Join(t.TempDir(), "source.avi")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	service.resolve = func(ctx context.Context, r NativePlaybackOpenRequest) (*nativeSource, error) {
		release, err := service.admitSource(ctx, path)
		if err != nil {
			return nil, err
		}
		file, err := os.Open(path)
		if err != nil {
			release()
			return nil, err
		}
		info, _ := file.Stat()
		return &nativeSource{file: file, path: path, info: info, videoID: r.VideoID, version: r.SourceVersion, duration: 60, unlock: release}, ctx.Err()
	}
	service.verify = func(context.Context, *nativeSource) error { return nil }
	players := make(chan *fakeNativePlayer, 8)
	service.factory = func(string) (nativePlayer, error) {
		player := &fakeNativePlayer{state: playback.Snapshot{Width: 640, Height: 360, Frames: 1, DecodedFrames: 1, Duration: 60, Paused: true, Muted: true, Volume: 1, Rate: 1}}
		players <- player
		return player, nil
	}
	service.supervisorDone = make(chan struct{})
	go service.supervise()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return service, epoch, players
}
func nativeOpenFixture(t *testing.T, s *NativePlaybackService, nonce string) (NativeOwner, NativePlaybackState) {
	t.Helper()
	owner, err := s.Acquire(NativeOwnerRequest{WindowEpoch: 1, ComponentNonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.Open(context.Background(), NativePlaybackOpenRequest{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, RequestID: "open", VideoID: 1, SourceVersion: "fingerprint", Paused: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetViewport(NativePlaybackViewport{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, SessionID: state.SessionID, Revision: 1, Width: 100, Height: 100, Visible: true}); err != nil {
		t.Fatal(err)
	}
	return owner, state
}
func waitNativeTest(t *testing.T, test func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if test() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native state did not settle")
}
func TestNativeOwnerIdempotencyAndIsolation(t *testing.T) {
	s, _, players := nativeManagerFixture(t)
	owner, state := nativeOpenFixture(t, s, "component")
	if _, err := s.Snapshot(NativePlaybackRef{OwnerID: owner.OwnerID, OwnerToken: "wrong-token", SessionID: state.SessionID}); !errors.Is(err, ErrNativeOwnerRevoked) {
		t.Fatal("invalid owner capability accepted", err)
	}
	same, err := s.Acquire(NativeOwnerRequest{WindowEpoch: 1, ComponentNonce: "component"})
	if err != nil || same != owner {
		t.Fatal("acquire was not idempotent")
	}
	replay, err := s.Open(context.Background(), NativePlaybackOpenRequest{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, RequestID: "open", VideoID: 1, SourceVersion: "fingerprint", Paused: true})
	if err != nil || replay.SessionID != state.SessionID || len(players) != 1 {
		t.Fatal("open created duplicate decoder")
	}
	_, err = s.Open(context.Background(), NativePlaybackOpenRequest{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, RequestID: "open", VideoID: 2, SourceVersion: "fingerprint", Paused: true})
	if !errors.Is(err, ErrNativeConflict) {
		t.Fatal(err)
	}
	other, err := s.Acquire(NativeOwnerRequest{WindowEpoch: 1, ComponentNonce: "other"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Snapshot(NativePlaybackRef{OwnerID: other.OwnerID, OwnerToken: other.OwnerToken, SessionID: state.SessionID})
	if !errors.Is(err, ErrNativeSessionClosed) {
		t.Fatal("cross-owner session accepted", err)
	}
}
func TestNativeReleaseWaitsForPendingOpenAndClosesLateDecoder(t *testing.T) {
	s, _, _ := nativeManagerFixture(t)
	owner, err := s.Acquire(NativeOwnerRequest{WindowEpoch: 1, ComponentNonce: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	entered, allow := make(chan struct{}), make(chan struct{})
	player := &fakeNativePlayer{}
	s.factory = func(string) (nativePlayer, error) { close(entered); <-allow; return player, nil }
	opened := make(chan error, 1)
	go func() {
		_, err := s.Open(context.Background(), NativePlaybackOpenRequest{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, RequestID: "open", VideoID: 1, SourceVersion: "fingerprint", Paused: true})
		opened <- err
	}()
	<-entered
	released := make(chan error, 1)
	go func() {
		released <- s.Release(context.Background(), NativeOwnerRequestRef{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken})
	}()
	select {
	case <-released:
		t.Fatal("release returned before pending decoder drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(allow)
	if err := <-opened; err == nil {
		t.Fatal("revoked open returned a session")
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if player.closed.Load() != 1 {
		t.Fatal("late decoder leaked or closed twice")
	}
}
func TestNativeEpochRevokesAndDrainsResources(t *testing.T) {
	s, epoch, players := nativeManagerFixture(t)
	owner, state := nativeOpenFixture(t, s, "epoch")
	player := <-players
	epoch.Store(2)
	if _, err := s.Snapshot(NativePlaybackRef{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, SessionID: state.SessionID}); !errors.Is(err, ErrNativeOwnerRevoked) {
		t.Fatal(err)
	}
	waitNativeTest(t, func() bool { return player.closed.Load() == 1 })
}
func TestNativeCommandsAreOrderedAndNativeAudioDoesNotOverlap(t *testing.T) {
	s, _, players := nativeManagerFixture(t)
	first, a := nativeOpenFixture(t, s, "first")
	p1 := <-players
	second, b := nativeOpenFixture(t, s, "second")
	p2 := <-players
	waitNativeTest(t, func() bool {
		state, _ := s.Snapshot(NativePlaybackRef{OwnerID: first.OwnerID, OwnerToken: first.OwnerToken, SessionID: a.SessionID})
		return state.Seekable
	})
	command := NativePlaybackCommand{OwnerID: first.OwnerID, OwnerToken: first.OwnerToken, SessionID: a.SessionID, Seq: 1, Kind: "play"}
	if _, err := s.Command(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Command(context.Background(), command); err != nil {
		t.Fatal("idempotent command rejected", err)
	}
	command.Kind = "pause"
	if _, err := s.Command(context.Background(), command); !errors.Is(err, ErrNativeConflict) {
		t.Fatal(err)
	}
	waitNativeTest(t, func() bool {
		state, _ := s.Snapshot(NativePlaybackRef{OwnerID: second.OwnerID, OwnerToken: second.OwnerToken, SessionID: b.SessionID})
		return state.Seekable
	})
	if _, err := s.Command(context.Background(), NativePlaybackCommand{OwnerID: second.OwnerID, OwnerToken: second.OwnerToken, SessionID: b.SessionID, Seq: 1, Kind: "play"}); err != nil {
		t.Fatal(err)
	}
	p1.mu.Lock()
	paused := p1.state.Paused
	p1.mu.Unlock()
	p2.mu.Lock()
	playing := !p2.state.Paused
	p2.mu.Unlock()
	if !paused || !playing {
		t.Fatal("native audio handoff overlapped")
	}
}
func TestNativeHiddenViewportPausesAndDoesNotResume(t *testing.T) {
	s, _, players := nativeManagerFixture(t)
	owner, state := nativeOpenFixture(t, s, "hidden")
	p := <-players
	_, err := s.SetViewport(NativePlaybackViewport{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, SessionID: state.SessionID, Revision: 2, Width: 100, Height: 100, Visible: false})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SetViewport(NativePlaybackViewport{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, SessionID: state.SessionID, Revision: 3, Width: 100, Height: 100, Visible: true})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.state.Paused {
		t.Fatal("returning page silently resumed")
	}
}

func (p *fakeNativePlayer) LoadFileAt(file *os.File, start int64) error {
	p.mu.Lock()
	p.state.Position = float64(start) / 1000
	p.mu.Unlock()
	return nil
}

func TestNativeOldEpochCleanupPreservesNewDocumentOwner(t *testing.T) {
	s, epoch, _ := nativeManagerFixture(t)
	old, _ := s.Acquire(NativeOwnerRequest{WindowEpoch: 1, ComponentNonce: "old"})
	epoch.Store(2)
	s.mu.Lock()
	s.registeredEpoch = 2
	s.ready = true
	s.mu.Unlock()
	fresh, err := s.Acquire(NativeOwnerRequest{WindowEpoch: 2, ComponentNonce: "fresh"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.revokeOldEpoch(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, err = s.ownerLocked(fresh.OwnerID, fresh.OwnerToken)
	_, oldErr := s.ownerLocked(old.OwnerID, old.OwnerToken)
	s.mu.Unlock()
	if err != nil || !errors.Is(oldErr, ErrNativeOwnerRevoked) {
		t.Fatal("cleanup crossed document epochs", err, oldErr)
	}
	if _, err = s.Acquire(NativeOwnerRequest{WindowEpoch: 1, ComponentNonce: "late-old"}); !errors.Is(err, ErrNativeOwnerRevoked) {
		t.Fatal("old document acquired new capability", err)
	}
}
func TestNativeMutationClosesOnlyMatchingSources(t *testing.T) {
	s, _, players := nativeManagerFixture(t)
	owner, state := nativeOpenFixture(t, s, "mutation")
	player := <-players
	n, err := s.session(NativePlaybackRef{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, SessionID: state.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeforeSourceMutation(context.Background(), n.source.path+"-different"); err != nil {
		t.Fatal(err)
	}
	if player.closed.Load() != 0 {
		t.Fatal("unrelated source was closed")
	}
	if err = s.BeforeSourceMutation(context.Background(), filepath.Dir(n.source.path)); err != nil {
		t.Fatal(err)
	}
	if player.closed.Load() != 1 {
		t.Fatal("matching source not drained")
	}
}

func TestNativePendingPlayReplayWaitsForItsFailure(t *testing.T) {
	s, _, players := nativeManagerFixture(t)
	first, _ := nativeOpenFixture(t, s, "old-audio")
	p1 := <-players
	_ = first
	owner, state := nativeOpenFixture(t, s, "pending-play")
	<-players
	waitNativeTest(t, func() bool {
		snapshot, _ := s.Snapshot(NativePlaybackRef{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, SessionID: state.SessionID})
		return snapshot.Seekable
	})
	entered, allow := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var pauseAttempts atomic.Int32
	p1.commandHook = func(kind string) error {
		if kind == "pause" && pauseAttempts.Add(1) == 1 {
			once.Do(func() { close(entered) })
			<-allow
			return errors.New("old pause failed")
		}
		return nil
	}
	request := NativePlaybackCommand{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, SessionID: state.SessionID, Seq: 1, Kind: "play"}
	results := make(chan error, 2)
	go func() { _, err := s.Command(context.Background(), request); results <- err }()
	<-entered
	go func() { _, err := s.Command(context.Background(), request); results <- err }()
	select {
	case <-results:
		t.Fatal("pending play was acknowledged early")
	case <-time.After(20 * time.Millisecond):
	}
	time.Sleep(100 * time.Millisecond) // worker has observed the pending play intent
	close(allow)
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			t.Fatal("failed activation replayed as success")
		}
	}
	n, err := s.session(NativePlaybackRef{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, SessionID: state.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	wanted := n.desiredPaused
	n.mu.Unlock()
	if !wanted {
		t.Fatal("failed play remained eligible for automatic activation")
	}
	time.Sleep(100 * time.Millisecond)
	n.mu.Lock()
	raw, _ := n.player.Snapshot()
	n.mu.Unlock()
	if !raw.Paused {
		t.Fatal("worker retried failed play")
	}
}
func TestNativeStopRetriesWindowUnregisterAfterTimeout(t *testing.T) {
	s, _, _ := nativeManagerFixture(t)
	s.mu.Lock()
	s.windowRegistered = true
	s.mu.Unlock()
	// Stop cannot complete the supervisor barrier during the first deadline.
	s.cancel()
	<-s.supervisorDone
	s.supervisorDone = make(chan struct{})
	var unregister atomic.Int32
	s.unregisterWindow = func() { unregister.Add(1) }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := s.Stop(ctx); err == nil {
		t.Fatal("incomplete stop reported success")
	}
	if unregister.Load() != 0 {
		t.Fatal("unregistered before the barrier")
	}
	close(s.supervisorDone)
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if unregister.Load() != 1 {
		t.Fatal("retry lost unregister obligation")
	}
}

func TestNativeMutationBlocksNewMatchingOpenUntilItsRelease(t *testing.T) {
	s, _, players := nativeManagerFixture(t)
	owner, state := nativeOpenFixture(t, s, "before-move")
	player := <-players
	n, err := s.session(NativePlaybackRef{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, SessionID: state.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.BeginSourceMutation(context.Background(), n.source.path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if player.closed.Load() != 1 {
		t.Fatal("mutation did not drain old decoder")
	}
	other, err := s.Acquire(NativeOwnerRequest{WindowEpoch: 1, ComponentNonce: "during-move"})
	if err != nil {
		t.Fatal(err)
	}
	request := NativePlaybackOpenRequest{OwnerID: other.OwnerID, OwnerToken: other.OwnerToken, RequestID: "blocked", VideoID: 1, SourceVersion: "fingerprint", Paused: true}
	if _, err = s.Open(context.Background(), request); err == nil {
		t.Fatal("Open crossed the mutation admission barrier")
	}
	release()
	request.RequestID = "after-release"
	if _, err = s.Open(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}
func TestNativeMutationWaitsForFDRegistrationBeforeItsSnapshot(t *testing.T) {
	s, _, _ := nativeManagerFixture(t)
	path := filepath.Join(t.TempDir(), "pending.avi")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	fdOpened, register := make(chan struct{}), make(chan struct{})
	var file *os.File
	s.resolve = func(ctx context.Context, r NativePlaybackOpenRequest) (*nativeSource, error) {
		release, err := s.admitSource(ctx, path)
		if err != nil {
			return nil, err
		}
		file, err = os.Open(path)
		if err != nil {
			release()
			return nil, err
		}
		info, _ := file.Stat()
		close(fdOpened)
		<-register
		return &nativeSource{file: file, path: path, info: info, videoID: r.VideoID, version: r.SourceVersion, duration: 60, unlock: release}, nil
	}
	owner, err := s.Acquire(NativeOwnerRequest{WindowEpoch: 1, ComponentNonce: "unregistered"})
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan error, 1)
	go func() {
		_, err := s.Open(context.Background(), NativePlaybackOpenRequest{OwnerID: owner.OwnerID, OwnerToken: owner.OwnerToken, RequestID: "open", VideoID: 1, SourceVersion: "fingerprint", Paused: true})
		opened <- err
	}()
	<-fdOpened
	mutation := make(chan error, 1)
	go func() {
		release, err := s.BeginSourceMutation(context.Background(), path)
		if err == nil {
			release()
		}
		mutation <- err
	}()
	select {
	case <-mutation:
		t.Fatal("mutation skipped an opened but unregistered FD")
	case <-time.After(20 * time.Millisecond):
	}
	close(register)
	<-opened
	if err := <-mutation; err != nil {
		t.Fatal(err)
	}
	if _, err = file.Stat(); err == nil {
		t.Fatal("mutation returned with a matching FD alive")
	}
}
func TestNativeExistingSourceVerificationDoesNotWaitForUnrelatedPathWriter(t *testing.T) {
	_, video := structureTestFixture(t)
	version := structureState(t, video.ID).SourceVersion
	source, err := resolveNativeSource(context.Background(), NativePlaybackOpenRequest{VideoID: video.ID, SourceVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	source.releasePath()
	defer source.close()
	libraryPathMutationMu.Lock()
	defer libraryPathMutationMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := verifyNativeSource(ctx, source); err != nil {
		t.Fatalf("unrelated path writer stopped a valid existing descriptor: %v", err)
	}
}
