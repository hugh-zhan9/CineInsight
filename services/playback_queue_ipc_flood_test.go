package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

// While the adapter's sink is blocked (database, command lock) mpv keeps
// sending time-pos at frame rate. Position updates are latest-wins, so the
// connection must survive a flood that exceeds the event bound.
func TestQueueMPVProgressFloodDoesNotOverflow(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	client := newQueueMPVClient(left)
	defer client.shutdown(errors.New("test done"))
	go func() {
		for i := 0; i < 1000; i++ {
			if _, err := fmt.Fprintf(right, "{\"event\":\"property-change\",\"name\":\"time-pos\",\"data\":%d}\n", i); err != nil {
				return
			}
		}
		scanner := bufio.NewScanner(right)
		if !scanner.Scan() {
			return
		}
		var request struct {
			RequestID uint64 `json:"request_id"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &request)
		fmt.Fprintf(right, "{\"request_id\":%d,\"error\":\"success\",\"data\":true}\n", request.RequestID)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var paused bool
	if err := client.property(ctx, "pause", &paused); err != nil {
		t.Fatalf("progress flood closed the control connection: %v", err)
	}
	select {
	case <-client.closed:
		t.Fatalf("progress flood closed the client: %v", client.failure())
	default:
	}
}

func queueMPVNextWithin(t *testing.T, c *queueMPVClient, d time.Duration) queueMPVMessage {
	t.Helper()
	got := make(chan queueMPVMessage, 1)
	go func() {
		if m, ok := c.nextEvent(); ok {
			got <- m
		}
	}()
	select {
	case m := <-got:
		return m
	case <-time.After(d):
		t.Fatal("event lost")
		return queueMPVMessage{}
	}
}

// Coalescing never reorders or drops a critical fact: each run of time-pos
// keeps only its newest value, at its own place before the next critical event.
func TestQueueMPVCoalescesProgressButKeepsCriticalOrder(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	client := newQueueMPVClient(left)
	defer client.shutdown(errors.New("test done"))
	lines := []string{}
	for i := 0; i < 300; i++ {
		lines = append(lines, fmt.Sprintf(`{"event":"property-change","name":"time-pos","data":%d}`, i))
	}
	lines = append(lines, `{"event":"property-change","name":"pause","data":true}`)
	for i := 300; i < 600; i++ {
		lines = append(lines, fmt.Sprintf(`{"event":"property-change","name":"time-pos","data":%d}`, i))
	}
	lines = append(lines, `{"event":"property-change","name":"eof-reached","data":true}`, `{"event":"end-file","reason":"eof","playlist_entry_id":1}`)
	for _, line := range lines {
		if _, err := fmt.Fprintln(right, line); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"time-pos=299", "pause=true", "time-pos=599", "eof-reached=true", "end-file"}
	for _, expected := range want {
		m := queueMPVNextWithin(t, client, time.Second)
		got := m.Event
		if m.Event == "property-change" {
			got = m.Name + "=" + string(m.Data)
		}
		if got != expected {
			t.Fatalf("got %q want %q", got, expected)
		}
	}
}
