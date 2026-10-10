package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

type queueMPVCommandError struct{ Code string }

func (e *queueMPVCommandError) Error() string { return "IINA IPC 命令失败：" + e.Code }

type queueMPVMessage struct {
	Event           string          `json:"event"`
	Name            string          `json:"name"`
	Data            json.RawMessage `json:"data"`
	Error           string          `json:"error"`
	RequestID       uint64          `json:"request_id"`
	PlaylistEntryID int64           `json:"playlist_entry_id"`
	Reason          string          `json:"reason"`
	entry           int64
}

type queueMPVClient struct {
	conn    net.Conn
	mu      sync.Mutex
	writes  contextMutex
	pending map[uint64]chan queueMPVMessage
	next    uint64
	entry   int64
	err     error
	closed  chan struct{}
	done    chan struct{}
	queue   []queueMPVMessage // pending events in arrival order, guarded by mu
	ready   chan struct{}
	wake    chan struct{}
	once    sync.Once
}

func newQueueMPVClient(conn net.Conn) *queueMPVClient {
	c := &queueMPVClient{conn: conn, pending: map[uint64]chan queueMPVMessage{}, closed: make(chan struct{}), done: make(chan struct{}), ready: make(chan struct{}, 1), wake: make(chan struct{}, 1)}
	go c.read()
	return c
}
func (c *queueMPVClient) shutdown(err error) {
	c.once.Do(func() { c.mu.Lock(); c.err = err; c.mu.Unlock(); close(c.closed); _ = c.conn.Close() })
}
func (c *queueMPVClient) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return errors.New("IINA IPC 已关闭")
}

const queueMPVEventCapacity = 128

// Only position samples are latest-wins. A run of consecutive time-pos changes
// collapses into its newest value; every other event keeps its own slot and
// order, and exceeding the bound with them still stops control.
func queueMPVCoalescible(m queueMPVMessage) bool {
	return m.Event == "property-change" && m.Name == "time-pos"
}
func (c *queueMPVClient) enqueue(m queueMPVMessage) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := len(c.queue); n > 0 && queueMPVCoalescible(m) && queueMPVCoalescible(c.queue[n-1]) && c.queue[n-1].entry == m.entry {
		c.queue[n-1] = m
	} else if n >= queueMPVEventCapacity {
		return false
	} else {
		c.queue = append(c.queue, m)
	}
	select {
	case c.ready <- struct{}{}:
	default:
	}
	return true
}

// nextEvent blocks for the oldest pending event; false once the client is closed.
func (c *queueMPVClient) nextEvent() (queueMPVMessage, bool) {
	for {
		select {
		case <-c.closed:
			return queueMPVMessage{}, false
		default:
		}
		c.mu.Lock()
		if len(c.queue) > 0 {
			m := c.queue[0]
			c.queue = c.queue[1:]
			c.mu.Unlock()
			return m, true
		}
		c.mu.Unlock()
		select {
		case <-c.ready:
		case <-c.closed:
		}
	}
}
func (c *queueMPVClient) setEntry(entry int64) { c.mu.Lock(); c.entry = entry; c.mu.Unlock() }
func (c *queueMPVClient) read() {
	defer close(c.done)
	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var m queueMPVMessage
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			c.shutdown(errors.New("IINA IPC 消息格式无效"))
			return
		}
		c.mu.Lock()
		if m.RequestID != 0 {
			reply := c.pending[m.RequestID]
			c.mu.Unlock()
			if reply != nil {
				select {
				case reply <- m:
				default:
				}
			}
			continue
		}
		if m.Event == "start-file" {
			c.entry = m.PlaylistEntryID
		}
		m.entry = c.entry
		if m.Event == "end-file" {
			m.entry = m.PlaylistEntryID
		}
		c.mu.Unlock()
		if m.Event == "" {
			c.shutdown(errors.New("IINA IPC 消息缺少事件或响应标识"))
			return
		}
		select {
		case c.wake <- struct{}{}:
		default:
		}
		if !c.enqueue(m) {
			c.shutdown(errors.New("IINA IPC 事件积压，已停止队列控制"))
			return
		}
	}
	err := scanner.Err()
	if err == nil {
		err = errors.New("IINA IPC 连接已断开")
	}
	c.shutdown(err)
}
func (c *queueMPVClient) command(parent context.Context, args ...any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if err := c.writes.LockContext(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.next++
	id := c.next
	reply := make(chan queueMPVMessage, 1)
	c.pending[id] = reply
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	data, err := json.Marshal(map[string]any{"command": args, "request_id": id})
	if err == nil {
		deadline, _ := ctx.Deadline()
		_ = c.conn.SetWriteDeadline(deadline)
		interrupted := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { _ = c.conn.SetWriteDeadline(time.Now()); close(interrupted) })
		_, err = c.conn.Write(append(data, '\n'))
		// Finish the cancellation callback while still owning the write slot;
		// it must not overwrite a subsequent command's deadline.
		if !stop() {
			<-interrupted
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}
	c.writes.Unlock()
	if err != nil {
		c.shutdown(err)
		return nil, err
	}
	select {
	case m := <-reply:
		if m.Error != "success" {
			return nil, &queueMPVCommandError{Code: m.Error}
		}
		return m.Data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, c.failure()
	}
}
func (c *queueMPVClient) property(ctx context.Context, name string, dest any) error {
	data, err := c.command(ctx, "get_property", name)
	if err != nil {
		return err
	}
	if len(data) == 0 || string(data) == "null" {
		return fmt.Errorf("IINA 属性尚未就绪：%s", name)
	}
	return json.Unmarshal(data, dest)
}
