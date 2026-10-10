package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type queueNotifyingConn struct {
	net.Conn
	entered chan struct{}
	once    sync.Once
}

func (c *queueNotifyingConn) Write(data []byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	return c.Conn.Write(data)
}
func TestQueueMPVCancelsBlockedWrite(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	conn := &queueNotifyingConn{Conn: left, entered: make(chan struct{})}
	client := newQueueMPVClient(conn)
	defer client.shutdown(errors.New("test done"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.command(ctx, "get_property", "path"); done <- err }()
	<-conn.entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel ignored")
		}
	case <-time.After(500 * time.Millisecond):
		right.Close()
		<-done
		t.Fatal("blocked IPC write ignored cancellation")
	}
}
func TestQueueMPVDemultiplexesEventsAndReplies(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	client := newQueueMPVClient(left)
	defer client.shutdown(errors.New("test done"))
	go func() {
		scanner := bufio.NewScanner(right)
		if !scanner.Scan() {
			return
		}
		var request struct {
			RequestID uint64 `json:"request_id"`
		}
		json.Unmarshal(scanner.Bytes(), &request)
		fmt.Fprintln(right, `{"event":"start-file","playlist_entry_id":42}`)
		fmt.Fprintf(right, "{\"request_id\":%d,\"error\":\"success\",\"data\":true}\n", request.RequestID)
	}()
	var paused bool
	if err := client.property(context.Background(), "pause", &paused); err != nil || !paused {
		t.Fatal(paused, err)
	}
	event := queueMPVNextWithin(t, client, time.Second)
	if event.entry != 42 || event.Event != "start-file" {
		t.Fatal(event)
	}
}
func TestQueueMPVRejectsOversizeAndEventOverflow(t *testing.T) {
	for _, scenario := range []string{"oversize", "overflow", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			left, right := net.Pipe()
			defer right.Close()
			client := newQueueMPVClient(left)
			defer client.shutdown(errors.New("test done"))
			done := make(chan struct{})
			go func() {
				defer close(done)
				switch scenario {
				case "oversize":
					fmt.Fprintln(right, strings.Repeat("x", (1<<20)+1))
				case "malformed":
					fmt.Fprintln(right, "bad JSON")
				case "overflow":
					for i := 0; i < 130; i++ {
						if _, err := fmt.Fprintln(right, `{"event":"property-change","name":"pause","data":false}`); err != nil {
							return
						}
					}
				}
			}()
			select {
			case <-client.closed:
			case <-time.After(time.Second):
				t.Fatal("invalid stream remained live")
			}
			right.Close()
			<-done
		})
	}
}
