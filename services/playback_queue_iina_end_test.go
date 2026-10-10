package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"video-master/models"
)

type queueIINAEndHarness struct {
	mu     sync.Mutex
	props  map[string]any
	events []InlineQueueEvent
	refuse bool
	player *queueIINAPlayer
	proc   *queueIINAProcess
}

func newQueueIINAEndHarness(t *testing.T) *queueIINAEndHarness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(path, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	h := &queueIINAEndHarness{props: map[string]any{"path": path, "playlist/0/id": 2, "eof-reached": false, "seeking": false, "time-pos": 120.0}}
	client := queueFakeMPV(t, func(command []any) (any, string) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if value, ok := h.props[command[1].(string)]; ok {
			return value, "success"
		}
		return nil, "property unavailable"
	})
	h.proc = &queueIINAProcess{client: client}
	session := &queueIINASession{entry: 2, token: "current", cycle: 1, paused: true, media: &queueMediaLease{video: models.Video{ID: 1, Path: path}, source: playbackProxyFingerprintOf(info)}, sink: h.sink}
	h.player = &queueIINAPlayer{process: h.proc, session: session}
	return h
}
func (h *queueIINAEndHarness) sink(e InlineQueueEvent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
	if e.Kind == "ended" && h.refuse {
		return ErrQueueEndIgnored
	}
	return nil
}
func (h *queueIINAEndHarness) set(name string, value any) {
	h.mu.Lock()
	h.props[name] = value
	h.mu.Unlock()
}
func (h *queueIINAEndHarness) change(name string, value any) {
	data, _ := json.Marshal(value)
	h.player.event(h.proc, queueMPVMessage{Event: "property-change", Name: name, Data: data, entry: 2})
}
func (h *queueIINAEndHarness) kinds() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []string{}
	for _, e := range h.events {
		out = append(out, e.Kind)
	}
	return out
}

// Paused, seek past the end: eof-reached arrives while mpv still seeks, then
// the seek completes without unpausing. The completed seek must be reported and
// the deferred EOF re-verified, so the backend and adapter agree on one end.
func TestQueueIINASeekCompletedWhilePausedReportsSeekedThenEnd(t *testing.T) {
	h := newQueueIINAEndHarness(t)
	h.set("seeking", true)
	h.change("seeking", true)
	h.set("eof-reached", true)
	h.change("eof-reached", true)
	h.set("seeking", false)
	h.change("seeking", false)
	got := h.kinds()
	want := []string{"seeking", "seeked", "ended"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("events=%v want %v", got, want)
	}
}

// A refused end (backend still considered the seek in flight) must not open a
// new view cycle on unpause; only a consumed end does.
func TestQueueIINARefusedEndKeepsViewCycle(t *testing.T) {
	h := newQueueIINAEndHarness(t)
	h.refuse = true
	h.set("eof-reached", true)
	h.change("eof-reached", true)
	h.change("pause", false)
	h.mu.Lock()
	last := h.events[len(h.events)-1]
	h.mu.Unlock()
	if last.Kind != "playing" || last.ViewCycle != 1 {
		t.Fatalf("refused end opened a new cycle: %+v (all %v)", last, h.kinds())
	}
	h.refuse = false
	h.change("pause", true)
	h.set("eof-reached", false)
	h.change("eof-reached", false)
	h.set("eof-reached", true)
	h.change("eof-reached", true)
	h.change("pause", false)
	h.mu.Lock()
	last = h.events[len(h.events)-1]
	h.mu.Unlock()
	if last.Kind != "playing" || last.ViewCycle != 2 {
		t.Fatalf("consumed end did not open exactly one new cycle: %+v (all %v)", last, h.kinds())
	}
}

// The backend decides "reached by seeking" from the seeked landing position, so
// the adapter must report where mpv actually landed, not a time-pos sample from
// before the seek. The eof that follows carries the same position.
func TestQueueIINASeekedCarriesLiveLandingPosition(t *testing.T) {
	h := newQueueIINAEndHarness(t)
	h.player.session.position = 50
	h.set("seeking", true)
	h.change("seeking", true)
	h.set("time-pos", 119.96)
	h.set("eof-reached", true)
	h.change("eof-reached", true)
	h.set("seeking", false)
	h.change("seeking", false)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.events) != 3 || h.events[1].Kind != "seeked" || h.events[1].Position != 119.96 || h.events[2].Kind != "ended" || h.events[2].Position != 119.96 {
		t.Fatalf("landing not reported from the player: %+v", h.events)
	}
}
