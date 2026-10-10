package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"video-master/models"
)

func queueFakeMPV(t *testing.T, respond func([]any) (any, string)) *queueMPVClient {
	t.Helper()
	left, right := net.Pipe()
	client := newQueueMPVClient(left)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer right.Close()
		scanner := bufio.NewScanner(right)
		for scanner.Scan() {
			var request struct {
				Command []any  `json:"command"`
				ID      uint64 `json:"request_id"`
			}
			if json.Unmarshal(scanner.Bytes(), &request) != nil {
				return
			}
			data, code := respond(request.Command)
			if err := json.NewEncoder(right).Encode(map[string]any{"request_id": request.ID, "error": code, "data": data}); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { client.shutdown(errors.New("test done")); right.Close(); <-done })
	return client
}
func TestQueueIINAReadsActualDurationAndKeepsUnknownUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		data any
		code string
		want float64
		fail bool
	}{{"actual", 2.5, "success", 2.5, false}, {"null", nil, "success", 0, false}, {"unavailable", nil, "property unavailable", 0, false}, {"protocol", nil, "error running command", 0, true}} {
		t.Run(tc.name, func(t *testing.T) {
			c := queueFakeMPV(t, func(command []any) (any, string) {
				if len(command) != 2 || command[1] != "duration" {
					t.Error("unexpected command", command)
				}
				return tc.data, tc.code
			})
			duration, err := queueIINADuration(context.Background(), c)
			if (err != nil) != tc.fail || duration != tc.want {
				t.Fatalf("duration=%v err=%v", duration, err)
			}
		})
	}
}
func TestQueueIINADelayedOldStartChecksActualIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(path, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	actualEntry := int64(2)
	client := queueFakeMPV(t, func(command []any) (any, string) {
		if command[1] == "path" {
			return path, "success"
		}
		if command[1] == "playlist/0/id" {
			return actualEntry, "success"
		}
		return nil, "property unavailable"
	})
	process := &queueIINAProcess{client: client}
	events := []InlineQueueEvent{}
	session := &queueIINASession{entry: 2, token: "current", cycle: 1, media: &queueMediaLease{video: models.Video{ID: 1, Path: path}, source: playbackProxyFingerprintOf(info)}, sink: func(e InlineQueueEvent) error { events = append(events, e); return nil }}
	player := &queueIINAPlayer{process: process, session: session}
	player.event(process, queueMPVMessage{Event: "start-file", PlaylistEntryID: 1, entry: 1})
	if len(events) != 0 {
		t.Fatal("delayed old entry stopped the new file", events)
	}
	actualEntry = 3
	player.event(process, queueMPVMessage{Event: "start-file", PlaylistEntryID: 3, entry: 3})
	if len(events) != 1 || events[0].Kind != "error" {
		t.Fatal("external replacement was not detected", events)
	}
}
