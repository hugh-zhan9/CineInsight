package services

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"video-master/services/btclient"
)

type btCheckingAddRemote struct {
	*btTaskFakeRemote
	states []string
	reads  atomic.Int32
	onRead func(int, *btFakeRemoteTask)
}

func (r *btCheckingAddRemote) QBAddStopped(ctx context.Context, o btclient.QBAddOptions) (string, error) {
	hash, err := r.btTaskFakeRemote.QBAddStopped(ctx, o)
	r.mu.Lock()
	if row := r.tasks[hash]; row != nil {
		row.info.State = "checkingResumeData"
	}
	r.mu.Unlock()
	return hash, err
}
func (r *btCheckingAddRemote) QBTorrents(ctx context.Context, hash string) ([]btclient.QBTorrent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if row := r.tasks[hash]; row != nil {
		read := int(r.reads.Add(1))
		if len(r.states) > 0 {
			row.info.State = r.states[0]
			r.states = r.states[1:]
		}
		if r.onRead != nil {
			r.onRead(read, row)
		}
		return []btclient.QBTorrent{row.info}, nil
	}
	return nil, nil
}
func btCheckingFixture(t *testing.T, states []string) (*BTService, *btCheckingAddRemote, string) {
	t.Helper()
	s, r := newBTTaskTest(t)
	view := btSubmitFirst(t, s)
	checking := &btCheckingAddRemote{btTaskFakeRemote: r, states: states}
	s.mu.Lock()
	s.remote = checking
	s.mu.Unlock()
	return s, checking, view.ID
}
func TestBTTaskAddWaitsReadOnlyForCheckingResumeData(t *testing.T) {
	s, r, id := btCheckingFixture(t, []string{"checkingResumeData", "checkingResumeData", "stoppedDL"})
	r.onRead = func(read int, row *btFakeRemoteTask) {
		if read <= 3 && len(r.actions) != 0 {
			t.Error("write before stopped readiness", r.actions)
		}
	}
	if err := s.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	view, err := s.Get(context.Background(), id)
	if err != nil || view.Stage != "active" || r.adds != 1 || strings.Join(r.actions, ",") != "priority:0,priority:1,start" || r.reads.Load() < 3 {
		t.Fatal(view.Stage, r.adds, r.actions, r.reads.Load(), err)
	}
}
func TestBTTaskAddCheckingDeadlineDoesNotReplay(t *testing.T) {
	s, r, id := btCheckingFixture(t, nil)
	started := time.Now()
	if err := s.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	view, err := s.Get(context.Background(), id)
	if err != nil || view.Stage != "uncertain" || !view.UnresolvedWrite || view.PendingAction != "add" || len(r.actions) != 0 || r.adds != 1 || elapsed > 7*time.Second || r.reads.Load() < 2 {
		t.Fatal(view.Stage, view.PendingAction, view.UnresolvedWrite, elapsed, r.actions, r.adds, err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if r.adds != 1 || len(r.actions) != 0 {
		t.Fatal("uncertain checking outcome replayed", r.adds, r.actions)
	}
}
func TestBTTaskAddCheckingCancellationPreservesSentIntent(t *testing.T) {
	s, r, id := btCheckingFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.onRead = func(read int, _ *btFakeRemoteTask) {
		if read == 1 {
			cancel()
		}
	}
	started := time.Now()
	_ = s.Poll(ctx)
	task, _, err := btLoadTask(context.Background(), id)
	if err != nil || task.Stage != "add_sent" || !task.UnresolvedWrite || task.PendingAction != "add" || time.Since(started) > time.Second || r.adds != 1 || len(r.actions) != 0 {
		t.Fatal(task.Stage, task.PendingAction, r.actions, err)
	}
	if err = s.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	view, err := s.Get(context.Background(), id)
	if err != nil || view.Stage != "uncertain" || r.adds != 1 || len(r.actions) != 0 {
		t.Fatal("cancelled add replayed", view.Stage, r.adds, r.actions, err)
	}
}
func TestBTTaskAddCheckingStillRejectsChangedOwnershipHashManifestAndRunning(t *testing.T) {
	for _, kind := range []string{"owner", "hash", "manifest", "running"} {
		t.Run(kind, func(t *testing.T) {
			s, r, id := btCheckingFixture(t, []string{"checkingResumeData", "stoppedDL"})
			r.onRead = func(read int, row *btFakeRemoteTask) {
				if read != 2 {
					return
				}
				switch kind {
				case "owner":
					row.info.Tags = "other-owner"
				case "hash":
					row.properties.InfoHashV1 = strings.Repeat("f", 40)
				case "manifest":
					row.files[0].Size++
				case "running":
					row.info.State = "downloading"
				}
			}
			if err := s.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			view, err := s.Get(context.Background(), id)
			if err != nil || view.Stage != "conflict" || r.reads.Load() < 2 || r.adds != 1 || len(r.actions) != 0 {
				t.Fatal(kind, view.Stage, r.reads.Load(), r.actions, err)
			}
		})
	}
}
