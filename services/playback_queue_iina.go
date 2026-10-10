package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

type queueIINASession struct {
	token                  string
	media                  *queueMediaLease
	entry                  int64
	seq, cycle             uint64
	position, duration     float64
	paused, seeking, ended bool
	eof                    bool // latest eof-reached; re-verified when a seek completes
	lastProgress           time.Time
	sink                   func(InlineQueueEvent) error
}
type queueIINAProcess struct {
	lastEntry int64
	observed  bool // property observers registered; at most once per process
	cmd       *exec.Cmd
	client    *queueMPVClient
	workdir   string
	done      chan struct{}
}
type queueIINAPlayer struct {
	operations contextMutex
	mu         sync.Mutex
	process    *queueIINAProcess
	session    *queueIINASession
}

func NewQueueIINAPlayer() *queueIINAPlayer { return &queueIINAPlayer{} }
func queueIINABinary() (string, bool) {
	if runtime.GOOS != "darwin" {
		return "", false
	}
	paths := []string{"/Applications/IINA.app/Contents/MacOS/IINA"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, "Applications/IINA.app/Contents/MacOS/IINA"))
	}
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return path, true
		}
	}
	return "", false
}
func (p *queueIINAPlayer) Available() (bool, string) {
	if _, ok := queueIINABinary(); ok {
		return true, ""
	}
	return false, "需要在 macOS 的应用程序目录安装 IINA"
}

func queueIINAArguments(socket, workdir, path string, start float64) []string {
	args := []string{}
	// Per-process overrides. No defaults writes, shared watch_later, user mpv
	// config/scripts, online subtitles, or directory-driven playlist expansion.
	for _, pair := range [][2]string{{"recordPlaybackHistory", "NO"}, {"recordRecentFiles", "NO"}, {"trackAllFilesInRecentOpenMenu", "NO"}, {"pauseWhenOpen", "YES"}, {"resumeLastPosition", "NO"}, {"playlistAutoAdd", "NO"}, {"playlistAutoPlayNext", "NO"}, {"autoSearchOnlineSub", "NO"}, {"iinaEnablePluginSystem", "NO"}, {"useUserDefinedConfDir", "NO"}, {"enableAdvancedSettings", "NO"}, {"enableThumbnailPreview", "NO"}, {"fullScreenWhenOpen", "NO"}, {"alwaysFloatOnTop", "NO"}, {"pauseWhenInactive", "NO"}, {"quitWhenNoOpenedWindow", "NO"}, {"keepOpenOnFileEnd", "YES"}, {"NSQuitAlwaysKeepsWindows", "NO"}, {"ApplePersistenceIgnoreState", "YES"}, {"SUEnableAutomaticChecks", "NO"}} {
		args = append(args, "-"+pair[0], pair[1])
	}
	return append(args, "--mpv-input-ipc-server="+socket, "--mpv-pause=yes", "--mpv-keep-open=yes", "--mpv-loop-file=no", "--mpv-loop-playlist=no", "--mpv-save-position-on-quit=no", "--mpv-resume-playback=no", "--mpv-watch-later-directory="+filepath.Join(workdir, "watch-later"), "--mpv-config=no", "--mpv-load-scripts=no", "--mpv-start="+strconv.FormatFloat(start, 'f', 3, 64), path)
}
func (p *queueIINAPlayer) startProcess(ctx context.Context, m *queueMediaLease) (*queueIINAProcess, error) {
	binary, ok := queueIINABinary()
	if !ok {
		return nil, errors.New("queue_player_unavailable: IINA 不可用")
	}
	// Darwin Unix paths are short. os.MkdirTemp under /tmp also avoids the much
	// longer per-user TMPDIR; creation mode is 0700.
	work, err := os.MkdirTemp("/tmp", "ci-iina-")
	if err != nil {
		return nil, err
	}
	socket := filepath.Join(work, "ipc.sock")
	cmd := exec.Command(binary, queueIINAArguments(socket, work, m.video.Path, m.start)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := ctx.Err(); err != nil {
		os.RemoveAll(work)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		os.RemoveAll(work)
		return nil, err
	}
	process := &queueIINAProcess{cmd: cmd, workdir: work, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(process.done) }()
	p.mu.Lock()
	p.process = process
	p.mu.Unlock()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err == nil {
			process.client = newQueueMPVClient(conn)
			go p.events(process)
			return process, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-process.done:
			return nil, errors.New("IINA 在 IPC 就绪前退出")
		case <-ticker.C:
		}
	}
}

func (p *queueIINAPlayer) Load(parent context.Context, m *queueMediaLease, token string, sink func(InlineQueueEvent) error) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if err := p.operations.LockContext(ctx); err != nil {
		return err
	}
	defer p.operations.Unlock()
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := validateQueueIINASource(ctx, m); err != nil {
		return err
	}
	p.mu.Lock()
	process := p.process
	previousEntry := int64(-1)
	if process != nil {
		previousEntry = process.lastEntry
	}
	p.session = nil
	p.mu.Unlock()
	if process != nil {
		select {
		case <-process.client.closed:
			return process.client.failure()
		default:
		}
	}
	reused := process != nil
	if !reused {
		process, err = p.startProcess(ctx, m)
		if err != nil {
			_ = p.closeProcess()
			return err
		}
	} else {
		// Entry IDs only grow within one mpv process. A cancelled earlier load
		// may not have recorded its entry, so bind strictly after whatever is
		// loaded now; a quick replay of the same path cannot match the old one.
		var live int64
		if process.client.property(ctx, "playlist/0/id", &live) == nil && live > previousEntry {
			previousEntry = live
		}
	}
	// Observers belong to the process, not to a load: register them right
	// after IPC connects (or on the first reuse of a process that lacks them),
	// before any handshake can be cancelled. A partial registration is not
	// retried on the same process.
	if !process.observed {
		if err := observeQueueIINA(ctx, process.client); err != nil {
			_ = p.closeProcess()
			return err
		}
		process.observed = true
	}
	if reused {
		if _, err = process.client.command(ctx, "set_property", "pause", true); err != nil {
			return err
		}
		if _, err = process.client.command(ctx, "loadfile", m.video.Path, "replace", -1, map[string]string{"pause": "yes", "start": strconv.FormatFloat(m.start, 'f', 3, 64)}); err != nil {
			return err
		}
	}
	entry, err := waitQueueIINALoad(ctx, process.client, m.video.Path, previousEntry)
	if err != nil {
		return err
	}
	if err := validateQueueIINASource(ctx, m); err != nil {
		return err
	}
	duration, err := queueIINADuration(ctx, process.client)
	if err != nil {
		return err
	}
	process.client.setEntry(entry)
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	process.lastEntry = entry
	p.session = &queueIINASession{token: token, media: m, entry: entry, cycle: 1, position: m.start, duration: duration, paused: true, sink: sink}
	p.mu.Unlock()
	return nil
}
func observeQueueIINA(ctx context.Context, c *queueMPVClient) error {
	for index, name := range []string{"pause", "time-pos", "duration", "seeking", "eof-reached", "path"} {
		if _, err := c.command(ctx, "observe_property", index+1, name); err != nil {
			return err
		}
	}
	return nil
}
func validateQueueIINASource(ctx context.Context, m *queueMediaLease) error {
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var video models.Video
		if err := db.Select("id", "path", "is_stale").First(&video, m.video.ID).Error; err != nil {
			return err
		}
		if video.IsStale || filepath.Clean(video.Path) != filepath.Clean(m.video.Path) {
			return ErrQueueSourceChanged
		}
		info, err := os.Stat(video.Path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || !m.source.matches(playbackProxyFingerprintOf(info)) {
			return ErrQueueSourceChanged
		}
		return ctx.Err()
	})
}
func waitQueueIINALoad(ctx context.Context, c *queueMPVClient, path string, previous int64) (int64, error) {
	for {
		var loaded string
		var entry int64
		var paused bool
		var params map[string]any
		if c.property(ctx, "path", &loaded) == nil && c.property(ctx, "playlist/0/id", &entry) == nil && entry != previous && filepath.Clean(loaded) == filepath.Clean(path) && c.property(ctx, "pause", &paused) == nil && paused && c.property(ctx, "video-params", &params) == nil && len(params) > 0 {
			return entry, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-c.closed:
			return 0, c.failure()
		case <-c.wake:
		}
	}
}
func (p *queueIINAPlayer) Control(ctx context.Context, action string) error {
	if action != "pause" && action != "resume" {
		return ErrQueueInvalid
	}
	if err := p.operations.LockContext(ctx); err != nil {
		return err
	}
	defer p.operations.Unlock()
	p.mu.Lock()
	process, session := p.process, p.session
	p.mu.Unlock()
	if process == nil || session == nil {
		return ErrQueueChanged
	}
	if err := checkQueueIINAIdentity(ctx, process.client, session, false); err != nil {
		return err
	}
	_, err := process.client.command(ctx, "set_property", "pause", action == "pause")
	return err
}
func (p *queueIINAPlayer) Stop(ctx context.Context) error {
	if err := p.operations.LockContext(ctx); err != nil {
		return err
	}
	defer p.operations.Unlock()
	p.mu.Lock()
	process := p.process
	p.session = nil
	p.mu.Unlock()
	if process == nil || process.client == nil {
		return nil
	}
	_, err := process.client.command(ctx, "stop")
	return err
}
func (p *queueIINAPlayer) Close() error {
	p.operations.Lock()
	defer p.operations.Unlock()
	return p.closeProcess()
}
func (p *queueIINAPlayer) closeProcess() error {
	p.mu.Lock()
	process := p.process
	p.process = nil
	p.session = nil
	p.mu.Unlock()
	if process == nil {
		return nil
	}
	if process.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _ = process.client.command(ctx, "quit")
		cancel()
		process.client.shutdown(errors.New("IINA 队列已关闭"))
	}
	select {
	case <-process.done:
	case <-time.After(2 * time.Second):
		if err := process.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		select {
		case <-process.done:
		case <-time.After(2 * time.Second):
			return errors.New("专用 IINA 进程尚未退出")
		}
	}
	if process.client != nil {
		<-process.client.done
	}
	return os.RemoveAll(process.workdir)
}
func checkQueueIINAIdentity(ctx context.Context, c *queueMPVClient, s *queueIINASession, eof bool) error {
	var path string
	var entry int64
	if err := c.property(ctx, "path", &path); err != nil {
		return err
	}
	if err := c.property(ctx, "playlist/0/id", &entry); err != nil {
		return err
	}
	if entry != s.entry || filepath.Clean(path) != filepath.Clean(s.media.video.Path) {
		return errors.New("IINA 已换入其他文件，队列控制已停止")
	}
	if eof {
		var ended, seeking bool
		if err := c.property(ctx, "eof-reached", &ended); err != nil {
			return err
		}
		if err := c.property(ctx, "seeking", &seeking); err != nil {
			return err
		}
		if !ended || seeking {
			return ErrQueueChanged
		}
	}
	info, err := os.Stat(s.media.video.Path)
	if err != nil {
		return err
	}
	if !s.media.source.matches(playbackProxyFingerprintOf(info)) {
		return ErrQueueSourceChanged
	}
	return ctx.Err()
}
func (p *queueIINAPlayer) emit(process *queueIINAProcess, session *queueIINASession, kind, message string) error {
	p.mu.Lock()
	if p.process != process || p.session != session {
		p.mu.Unlock()
		return ErrQueueChanged
	}
	session.seq++
	event := InlineQueueEvent{Token: session.token, VideoID: session.media.video.ID, SourceVersion: session.media.sourceVersion(), Seq: session.seq, ViewCycle: session.cycle, Kind: kind, Position: session.position, Duration: session.duration, Message: message}
	sink := session.sink
	p.mu.Unlock()
	if sink == nil {
		return nil
	}
	return sink(event)
}
func (p *queueIINAPlayer) events(process *queueIINAProcess) {
	for {
		m, ok := process.client.nextEvent()
		if !ok {
			p.mu.Lock()
			session := p.session
			p.mu.Unlock()
			if session != nil {
				p.emit(process, session, "error", queueSafeError(process.client.failure()))
			}
			return
		}
		p.event(process, m)
	}
}
func (p *queueIINAPlayer) event(process *queueIINAProcess, m queueMPVMessage) {
	p.mu.Lock()
	s := p.session
	if p.process != process || s == nil {
		p.mu.Unlock()
		return
	}
	if m.entry != s.entry {
		p.mu.Unlock()
		if m.Event == "start-file" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := checkQueueIINAIdentity(ctx, process.client, s, false)
			cancel()
			if err != nil {
				p.emit(process, s, "error", queueSafeError(err))
			}
		}
		return
	}
	kind, message := "", ""
	var follow []string // further facts of the same change, in order
	endCandidate := false
	if m.Event == "end-file" {
		kind = "error"
		message = "IINA 已停止播放（" + m.Reason + "）"
	}
	if m.Event == "property-change" && string(m.Data) != "null" {
		switch m.Name {
		case "duration":
			var value float64
			if json.Unmarshal(m.Data, &value) == nil && value > 0 {
				s.duration = value
			}
		case "time-pos":
			var value float64
			if json.Unmarshal(m.Data, &value) == nil && value >= 0 {
				s.position = value
				if !s.paused && !s.seeking && time.Since(s.lastProgress) >= time.Second {
					s.lastProgress = time.Now()
					kind = "progress"
				}
			}
		case "pause":
			var value bool
			if json.Unmarshal(m.Data, &value) == nil {
				s.paused = value
				if value {
					kind = "paused"
				} else {
					if s.ended {
						s.cycle++
						s.ended = false
					}
					kind = "playing"
				}
			}
		case "seeking":
			var value bool
			if json.Unmarshal(m.Data, &value) == nil {
				s.seeking = value
				if value {
					kind = "seeking"
				} else {
					// A completed seek is a fact even while paused; an EOF that
					// arrived during the seek is only now re-verified.
					kind = "seeked"
					if !s.paused {
						follow = append(follow, "playing")
					}
					endCandidate = s.eof && !s.ended
				}
			}
		case "path":
			var path string
			if json.Unmarshal(m.Data, &path) == nil && path != "" && filepath.Clean(path) != filepath.Clean(s.media.video.Path) {
				kind = "error"
				message = "IINA 已换入其他文件，队列控制已停止"
			}
		case "eof-reached":
			var value bool
			if json.Unmarshal(m.Data, &value) == nil {
				s.eof = value
				endCandidate = value && !s.ended
			}
		}
	}
	p.mu.Unlock()
	if kind == "seeked" {
		// The queue judges "end reached by seeking" against this landing
		// point; a time-pos sample from before the seek would understate it.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var landing float64
		err := process.client.property(ctx, "time-pos", &landing)
		cancel()
		if err == nil && landing >= 0 {
			p.mu.Lock()
			if p.session == s {
				s.position = landing
			}
			p.mu.Unlock()
		}
	}
	if kind == "progress" {
		info, err := os.Stat(s.media.video.Path)
		if err != nil || !s.media.source.matches(playbackProxyFingerprintOf(info)) {
			kind = "error"
			message = "原片已改变或不可用，请重新播放"
		}
	}
	if kind != "" {
		p.emit(process, s, kind, message)
	}
	if kind == "error" {
		return
	}
	for _, next := range follow {
		p.emit(process, s, next, "")
	}
	if endCandidate {
		p.consumeEnd(process, s)
	}
}

// consumeEnd re-verifies an eof-reached candidate against the live player and
// reports it once. The session keeps "ended" only when the queue consumed it,
// so a later unpause opens a new view cycle exactly when the backend did.
func (p *queueIINAPlayer) consumeEnd(process *queueIINAProcess, s *queueIINASession) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := checkQueueIINAIdentity(ctx, process.client, s, true)
	var position float64
	if err == nil {
		err = process.client.property(ctx, "time-pos", &position)
	}
	cancel()
	if errors.Is(err, ErrQueueChanged) {
		return
	}
	if err != nil {
		p.emit(process, s, "error", queueSafeError(err))
		return
	}
	p.mu.Lock()
	if p.process != process || p.session != s || s.ended {
		p.mu.Unlock()
		return
	}
	s.ended = true
	s.position = position
	p.mu.Unlock()
	if err := p.emit(process, s, "ended", ""); err != nil {
		p.mu.Lock()
		if p.session == s {
			s.ended = false
		}
		p.mu.Unlock()
	}
}

// duration may legitimately be unavailable (live/unknown input). A protocol or
// connection failure remains an error; never substitute stale library metadata.
func queueIINADuration(ctx context.Context, c *queueMPVClient) (float64, error) {
	data, err := c.command(ctx, "get_property", "duration")
	if err != nil {
		var commandError *queueMPVCommandError
		if errors.As(err, &commandError) && commandError.Code == "property unavailable" {
			return 0, nil
		}
		return 0, err
	}
	if string(data) == "null" {
		return 0, nil
	}
	var duration float64
	if err := json.Unmarshal(data, &duration); err != nil {
		return 0, err
	}
	if !math.IsNaN(duration) && !math.IsInf(duration, 0) && duration > 0 {
		return duration, nil
	}
	return 0, nil
}
