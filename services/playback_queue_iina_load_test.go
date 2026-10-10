package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// queueScriptedMPV answers like mpv: loadfile replies at once but the new
// playlist entry appears asynchronously, announced by a start-file event.
type queueScriptedMPV struct {
	mu       sync.Mutex
	write    sync.Mutex
	props    map[string]any
	commands []string
	nextID   int64
	right    net.Conn
}

func newQueueScriptedMPV(t *testing.T, props map[string]any, nextID int64) (*queueScriptedMPV, *queueMPVClient) {
	t.Helper()
	left, right := net.Pipe()
	f := &queueScriptedMPV{props: props, nextID: nextID, right: right}
	client := newQueueMPVClient(left)
	done := make(chan struct{})
	go f.serve(done)
	t.Cleanup(func() { client.shutdown(errors.New("test done")); right.Close(); <-done })
	return f, client
}
func (f *queueScriptedMPV) send(v any) {
	f.write.Lock()
	defer f.write.Unlock()
	_ = json.NewEncoder(f.right).Encode(v)
}
func (f *queueScriptedMPV) serve(done chan struct{}) {
	defer close(done)
	scanner := bufio.NewScanner(f.right)
	for scanner.Scan() {
		var request struct {
			Command []any  `json:"command"`
			ID      uint64 `json:"request_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return
		}
		name, _ := request.Command[0].(string)
		f.mu.Lock()
		f.commands = append(f.commands, name)
		var data any
		code := "success"
		switch name {
		case "get_property":
			value, ok := f.props[request.Command[1].(string)]
			if ok {
				data = value
			} else {
				code = "property unavailable"
			}
		case "set_property":
			f.props[request.Command[1].(string)] = request.Command[2]
		case "loadfile":
			id, path := f.nextID, request.Command[1]
			f.nextID++
			time.AfterFunc(20*time.Millisecond, func() {
				f.mu.Lock()
				f.props["playlist/0/id"], f.props["path"], f.props["pause"] = id, path, true
				f.mu.Unlock()
				f.send(map[string]any{"event": "start-file", "playlist_entry_id": id})
			})
		}
		f.mu.Unlock()
		f.send(map[string]any{"request_id": request.ID, "error": code, "data": data})
	}
}
func (f *queueScriptedMPV) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, command := range f.commands {
		if command == name {
			n++
		}
	}
	return n
}

// A first Load cancelled during the handshake leaves a live process whose
// last recorded entry is 0 while entry 1 of the same file is still loaded.
// Reusing it must observe properties exactly once per process and bind only to
// the entry created by this replacement, never the stale one.
func TestQueueIINAReusedProcessObservesOnceAndBindsNewEntry(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProxyTestVideo(t, "one.mp4", 120)
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatal(err)
	}
	fake, client := newQueueScriptedMPV(t, map[string]any{"path": video.Path, "playlist/0/id": 1, "pause": true, "video-params": map[string]any{"w": 1}, "duration": 120.0}, 2)
	process := &queueIINAProcess{client: client}
	player := &queueIINAPlayer{process: process}
	lease := &queueMediaLease{video: video, source: playbackProxyFingerprintOf(info)}
	sink := func(InlineQueueEvent) error { return nil }
	for round, want := range []int64{2, 3} {
		if err := player.Load(context.Background(), lease, "token", sink); err != nil {
			t.Fatal(round, err)
		}
		player.mu.Lock()
		entry := player.session.entry
		player.mu.Unlock()
		if entry != want {
			t.Fatalf("round %d bound to entry %d, want %d", round, entry, want)
		}
		if got := fake.count("observe_property"); got != 6 {
			t.Fatalf("round %d observe_property sent %d times, want 6 per process", round, got)
		}
	}
}
