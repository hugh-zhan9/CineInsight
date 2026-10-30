package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"video-master/services/btclient"
)

type btDelayedRunRemote struct {
	*btTaskFakeRemote
	states    []string
	pending   bool
	calls     int
	postReads atomic.Int32
	allReads  atomic.Int32
	onRead    func(int, *btFakeRemoteTask)
	onAnyRead func(int, *btFakeRemoteTask)
}

func (r *btDelayedRunRemote) QBRun(ctx context.Context, hash string, start bool) error {
	r.mu.Lock()
	prior := r.tasks[hash].info.State
	r.mu.Unlock()
	err := r.btTaskFakeRemote.QBRun(ctx, hash, start)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if err == nil {
		r.pending = true
		r.tasks[hash].info.State = prior
	}
	return err
}
func (r *btDelayedRunRemote) QBTorrents(ctx context.Context, hash string) ([]btclient.QBTorrent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if row := r.tasks[hash]; row != nil {
		count := int(r.allReads.Add(1))
		if r.onAnyRead != nil {
			r.onAnyRead(count, row)
		}
		if r.pending {
			read := int(r.postReads.Add(1))
			if len(r.states) > 0 {
				row.info.State = r.states[0]
				r.states = r.states[1:]
				if len(r.states) == 0 {
					r.pending = false
				}
			}
			if r.onRead != nil {
				r.onRead(read, row)
			}
		}
		return []btclient.QBTorrent{row.info}, nil
	}
	return nil, nil
}
func btDelayedRunFixture(t *testing.T, start bool, states []string) (*BTService, *btDelayedRunRemote, string) {
	t.Helper()
	s, base := newBTTaskTest(t)
	view := btSubmitFirst(t, s)
	if !start {
		if err := s.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
		current, err := s.Get(context.Background(), view.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Control(context.Background(), BTControlRequest{TaskID: view.ID, ExpectedRevision: current.Revision, Action: "stop"}); err != nil {
			t.Fatal(err)
		}
	}
	remote := &btDelayedRunRemote{btTaskFakeRemote: base, states: states}
	s.mu.Lock()
	s.remote = remote
	s.mu.Unlock()
	return s, remote, view.ID
}
func TestBTTaskRunWaitsForAcknowledgedTargetWithoutReissuing(t *testing.T) {
	for _, start := range []bool{true, false} {
		t.Run(fmt.Sprint(start), func(t *testing.T) {
			old, target := "stoppedDL", "downloading"
			if !start {
				old, target = target, old
			}
			s, r, id := btDelayedRunFixture(t, start, []string{old, old, target})
			if err := s.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			view, err := s.Get(context.Background(), id)
			if err != nil || view.Stage != "active" || r.calls != 1 || r.postReads.Load() < 3 || btStopped(view.RemoteState) == start {
				t.Fatal(view.Stage, view.RemoteState, r.calls, r.postReads.Load(), err)
			}
		})
	}
}
func TestBTTaskRunWaitDeadlineAndCancellationKeepUnknownWrites(t *testing.T) {
	for _, start := range []bool{true, false} {
		for _, cancelled := range []bool{false, true} {
			t.Run(fmt.Sprintf("start=%t/cancel=%t", start, cancelled), func(t *testing.T) {
				s, r, id := btDelayedRunFixture(t, start, nil)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if cancelled {
					r.onRead = func(read int, _ *btFakeRemoteTask) {
						if read == 3 {
							cancel()
						}
					}
				}
				began := time.Now()
				_ = s.Poll(ctx)
				task, _, err := btLoadTask(context.Background(), id)
				action := "start"
				if !start {
					action = "stop"
				}
				want := "uncertain"
				if cancelled {
					want = action + "_sent"
				}
				if err != nil || task.Stage != want || task.PendingAction != action || !task.UnresolvedWrite || r.calls != 1 || r.postReads.Load() < 3 {
					t.Fatal(task.Stage, task.PendingAction, task.UnresolvedWrite, r.calls, r.postReads.Load(), err)
				}
				elapsed := time.Since(began)
				if cancelled && elapsed > time.Second || !cancelled && (elapsed < 4*time.Second || elapsed > 7*time.Second) {
					t.Fatal("confirmation budget/cancellation", elapsed)
				}
				before := len(r.actions)
				_ = s.Poll(context.Background())
				_ = s.Poll(context.Background())
				if r.calls != 1 || len(r.actions) != before {
					t.Fatal("unknown run request replayed", r.calls, r.actions)
				}
			})
		}
	}
}
func TestBTTaskRunWaitRevalidatesIdentityManifestAndSelection(t *testing.T) {
	for _, kind := range []string{"owner", "hash", "manifest", "selection"} {
		t.Run(kind, func(t *testing.T) {
			s, r, id := btDelayedRunFixture(t, true, []string{"stoppedDL", "stoppedDL", "downloading"})
			r.onRead = func(read int, row *btFakeRemoteTask) {
				if read != 3 {
					return
				}
				switch kind {
				case "owner":
					row.info.Tags = "other"
				case "hash":
					row.properties.InfoHashV1 = strings.Repeat("f", 40)
				case "manifest":
					row.files[0].Size++
				case "selection":
					row.files[0].Priority = 0
				}
			}
			_ = s.Poll(context.Background())
			view, err := s.Get(context.Background(), id)
			if err != nil || view.Stage != "conflict" || r.calls != 1 || r.postReads.Load() < 3 {
				t.Fatal(kind, view.Stage, r.calls, r.postReads.Load(), err)
			}
		})
	}
}
func TestBTTaskRunUnknownResponseNeverWaitsOrAcknowledges(t *testing.T) {
	s, r, id := btDelayedRunFixture(t, true, nil)
	r.startErr = errors.New("response lost")
	_ = s.Poll(context.Background())
	view, err := s.Get(context.Background(), id)
	if err != nil || view.Stage != "uncertain" || !view.UnresolvedWrite || r.calls != 1 || r.postReads.Load() != 0 {
		t.Fatal(view.Stage, r.calls, r.postReads.Load(), err)
	}
	_ = s.Poll(context.Background())
	if r.calls != 1 {
		t.Fatal("ambiguous run replayed")
	}
}
func TestBTTaskRunWithoutWriteDoesNotWaitForExternalStateChange(t *testing.T) {
	s, r, id := btDelayedRunFixture(t, false, nil)
	// The queued stop already has its target state; an external start then occurs
	// between preflight and confirmation. No successful local write authorizes waiting.
	r.mu.Lock()
	for _, row := range r.tasks {
		row.info.State = "stoppedDL"
	}
	r.mu.Unlock()
	r.onAnyRead = func(read int, row *btFakeRemoteTask) {
		if read == 3 {
			row.info.State = "downloading"
		}
	}
	began := time.Now()
	_ = s.Poll(context.Background())
	view, err := s.Get(context.Background(), id)
	if err != nil || view.Stage != "uncertain" || r.calls != 0 || time.Since(began) > time.Second {
		t.Fatal(view.Stage, r.calls, err)
	}
}
