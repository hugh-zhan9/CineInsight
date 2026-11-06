package services

import (
	"context"
	"errors"
	"math"
	"time"
)

func (s *NativePlaybackService) runSession(ctx context.Context, n *nativeSession) {
	started := time.Now()
	lastVerify := time.Time{}
	defer func() {
		n.mu.Lock()
		n.closing = true
		n.player.Close()
		n.source.close()
		if n.state.State != "error" {
			n.state.State = "closed"
		}
		n.state.Paused = true
		n.state.Seq++
		state := n.state
		n.mu.Unlock()
		s.mu.Lock()
		s.slots--
		if state.State != "error" && n.owner.current == n {
			n.owner.current = nil
		}
		s.mu.Unlock()
		s.emit(state)
		close(n.done)
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		n.mu.Lock()
		capturedCommand, capturedViewport := n.commandSeq, n.viewport.Revision
		raw, err := n.player.Snapshot()
		needVerify := time.Since(lastVerify) > time.Second || (!n.initialized && raw.Width > 0) || raw.Ended
		n.mu.Unlock()
		if err != nil {
			s.failNative(n, "native_decode_failed", "本机播放器读取失败")
			return
		}
		if needVerify {
			check, cancel := context.WithTimeout(ctx, time.Second)
			err = s.verify(check, n.source)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				s.failNative(n, nativeSourceError(err), "原片已变化或当前不可用，请重新打开")
				return
			}
			lastVerify = time.Now()
		}
		if raw.Error < 0 || !finiteNative(raw.Position) || !finiteNative(raw.Duration) || raw.Duration > 9007199254740 {
			s.failNative(n, "native_decode_failed", "本机播放器无法解码此文件")
			return
		}
		n.mu.Lock()
		if ctx.Err() != nil {
			n.mu.Unlock()
			return
		}
		if capturedCommand != n.commandSeq || capturedViewport != n.viewport.Revision {
			n.mu.Unlock()
			continue
		}
		previous := n.state
		if !n.initialized && raw.Width > 0 {
			n.initialized = true
			n.lastLanding = raw.Position
		}
		if n.request.StartMS > 0 && raw.Duration > 0 && float64(n.request.StartMS) > raw.Duration*1000 {
			n.mu.Unlock()
			s.failNative(n, "native_seek_failed", "起播位置超出片长")
			return
		}
		if raw.DecodedFrames == 0 && time.Since(started) > 30*time.Second {
			n.mu.Unlock()
			s.failNative(n, "native_decode_failed", "文件在限定时间内没有准备好")
			return
		}

		n.state.PositionMS = int64(math.Round(math.Max(0, raw.Position) * 1000))
		n.state.DurationMS = int64(math.Round(math.Max(0, raw.Duration) * 1000))
		n.state.VideoWidth = raw.Width
		n.state.VideoHeight = raw.Height
		n.state.Muted = raw.Muted
		n.state.Volume = raw.Volume
		n.state.Rate = raw.Rate
		n.state.Paused = raw.Paused
		n.state.Seekable = n.initialized && raw.Duration > 0
		if n.seekTarget != nil {
			if !raw.Seeking && raw.DecodedFrames > n.seekFrames && math.Abs(raw.Position-*n.seekTarget) < .5 {
				n.seekTarget = nil
				n.lastLanding = raw.Position
				n.advanced = false
			} else if time.Since(n.seekAt) > 10*time.Second {
				n.mu.Unlock()
				s.failNative(n, "native_seek_failed", "无法定位到所选位置，文件索引可能已损坏")
				return
			}
		}
		if !raw.Paused && !raw.Seeking && n.seekTarget == nil && raw.Position > n.lastLanding+.02 {
			n.advanced = true
		}
		switch {
		case !n.initialized || raw.DecodedFrames == 0:
			n.state.State = "opening"
		case raw.Seeking || n.seekTarget != nil:
			n.state.State = "seeking"
		case raw.Ended && n.advanced:
			if raw.Duration > 0 && raw.Position+2 < raw.Duration {
				n.mu.Unlock()
				s.failNative(n, "native_decode_failed", "文件在正常片尾之前结束，可能不完整")
				return
			}
			n.state.State = "ended"
			n.desiredPaused = true
		case raw.Paused:
			n.state.State = "paused"
		default:
			n.state.State = "playing"
		}
		if err = n.applyViewportLocked(n.viewport); err != nil {
			n.mu.Unlock()
			s.failNative(n, "native_decode_failed", "无法显示播放画面")
			return
		}

		activate := n.initialized && n.seekTarget == nil && !n.desiredPaused && raw.Paused && !raw.Ended && n.viewport.Visible && n.viewport.Width > 0 && n.viewport.Height > 0
		compare := n.state
		compare.PositionMS = previous.PositionMS
		compare.Seq = previous.Seq
		emit := compare != previous || time.Since(n.lastEmit) >= 250*time.Millisecond
		if emit {
			n.state.Seq++
			n.lastEmit = time.Now()
		}
		state := n.state
		n.mu.Unlock()
		if emit {
			s.emit(state)
		}
		if activate {
			n.commandMu.Lock()
			err = s.activate(ctx, n)
			n.commandMu.Unlock()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				s.failNative(n, "native_decode_failed", "无法开始播放")
				return
			}
		}
	}
}
func finiteNative(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func (s *NativePlaybackService) failNative(n *nativeSession, code, message string) {
	n.mu.Lock()
	n.state.State = "error"
	n.state.ErrorCode = code
	n.state.ErrorMessage = message
	n.state.Seq++
	n.mu.Unlock()
}

// All native play transitions share this gate. Decoder locks never enclose DB IO.
func (s *NativePlaybackService) activate(ctx context.Context, target *nativeSession) error {
	s.audioMu.Lock()
	defer s.audioMu.Unlock()
	target.mu.Lock()
	cancelled := target.closing || target.state.State == "error"
	wanted := !target.desiredPaused && target.initialized && target.seekTarget == nil
	target.mu.Unlock()
	if cancelled {
		return ErrNativeSessionClosed
	}
	if !wanted {
		return nil
	}

	s.mu.Lock()
	others := []*nativeSession{}
	for _, owner := range s.owners {
		if owner.current != nil && owner.current != target {
			others = append(others, owner.current)
		}
	}
	s.mu.Unlock()
	for _, other := range others {
		other.mu.Lock()
		terminal := other.closing || other.state.State == "error"
		if !other.closing && other.state.State != "error" {
			other.desiredPaused = true
			if err := other.player.Command("pause", 0); err != nil {
				other.mu.Unlock()
				return err
			}
			raw, err := other.player.Snapshot()
			if err != nil || !raw.Paused {
				other.mu.Unlock()
				return errors.New("native_pause_pending: 之前的播放尚未暂停")
			}
		}
		other.mu.Unlock()
		if terminal {
			if err := waitNative(ctx, other.done); err != nil {
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if target.closing || target.state.State == "error" {
		return ErrNativeSessionClosed
	}
	if !target.viewport.Visible || target.viewport.Width <= 0 || target.viewport.Height <= 0 {
		return errors.New("native_invalid_state: 播放区域当前不可见")
	}
	if target.desiredPaused || target.seekTarget != nil || !target.initialized {
		return nil
	}
	return target.player.Command("play", 0)
}
func (s *NativePlaybackService) Command(ctx context.Context, r NativePlaybackCommand) (NativePlaybackCommandResult, error) {
	if r.Seq == 0 || r.Seq > 9007199254740991 {
		return NativePlaybackCommandResult{}, errors.New("native_invalid_request: 命令序号无效")
	}
	value := 0.0
	switch r.Kind {
	case "play", "pause":
		if r.Value != nil {
			return NativePlaybackCommandResult{}, errors.New("native_invalid_request: 此命令不接受值")
		}
	case "set_muted":
		v, ok := r.Value.(bool)
		if !ok {
			return NativePlaybackCommandResult{}, errors.New("native_invalid_request: 静音值无效")
		}
		if v {
			value = 1
		}
	case "seek", "set_volume", "set_rate":
		v, ok := r.Value.(float64)
		if !ok || !finiteNative(v) {
			return NativePlaybackCommandResult{}, errors.New("native_invalid_request: 命令值无效")
		}
		value = v
	default:
		return NativePlaybackCommandResult{}, errors.New("native_invalid_request: 不支持的播放命令")
	}
	if (r.Kind == "seek" && (value < 0 || value != math.Trunc(value) || value > 9007199254740991)) || (r.Kind == "set_volume" && (value < 0 || value > 1)) || (r.Kind == "set_rate" && (value < .25 || value > 4)) {
		return NativePlaybackCommandResult{}, errors.New("native_invalid_request: 命令值超出范围")
	}
	n, err := s.session(NativePlaybackRef{OwnerID: r.OwnerID, OwnerToken: r.OwnerToken, SessionID: r.SessionID})
	if err != nil {
		return NativePlaybackCommandResult{}, err
	}
	n.commandMu.Lock()
	defer n.commandMu.Unlock()
	n.mu.Lock()
	digest := nativeDigest(r)
	if r.Seq <= n.commandSeq {
		seq, previousDigest, previousError := n.commandSeq, n.commandDigest, n.commandError
		n.mu.Unlock()
		if r.Seq == seq && digest == previousDigest {
			return NativePlaybackCommandResult{AcceptedSeq: r.Seq}, previousError
		}
		if r.Seq == seq {
			return NativePlaybackCommandResult{}, ErrNativeConflict
		}
		return NativePlaybackCommandResult{}, errors.New("native_stale_command: 播放命令已过期")
	}
	if n.closing || n.state.State == "error" {
		n.mu.Unlock()
		return NativePlaybackCommandResult{}, ErrNativeSessionClosed
	}
	kind := r.Kind
	switch kind {
	case "play":
		if !n.viewport.Visible || n.viewport.Width <= 0 || n.viewport.Height <= 0 {
			n.mu.Unlock()
			return NativePlaybackCommandResult{}, errors.New("native_invalid_state: 播放区域当前不可见")
		}
		n.desiredPaused = false
	case "pause":
		n.desiredPaused = true
		err = n.player.Command("pause", 0)
	case "seek":
		if !n.state.Seekable || value > float64(n.state.DurationMS) {
			n.mu.Unlock()
			return NativePlaybackCommandResult{}, errors.New("native_invalid_state: 当前不能定位到所选时间")
		}
		raw, e := n.player.Snapshot()
		if e != nil {
			err = e
			break
		}
		target := value / 1000
		err = n.player.Command("seek", target)
		if err == nil {
			n.seekTarget = &target
			n.seekFrames = raw.DecodedFrames
			n.seekAt = time.Now()
			n.advanced = false
		}
	case "set_volume":
		err = n.player.Command("volume", value)
	case "set_muted":
		err = n.player.Command("muted", value)
	case "set_rate":
		err = n.player.Command("rate", value)
	}
	var fact *NativePlaybackState
	if err == nil && kind == "seek" {
		n.state.State = "seeking"
		n.state.Seq++
		copy := n.state
		fact = &copy
	}
	if err == nil {
		n.commandSeq = r.Seq
		n.commandDigest = digest
		n.commandError = nil
	}
	n.mu.Unlock()
	if err != nil {
		return NativePlaybackCommandResult{}, err
	}
	if fact != nil {
		s.emit(*fact)
	}
	if kind == "play" {
		if err = s.activate(ctx, n); err != nil {
			n.mu.Lock()
			n.desiredPaused = true
			n.commandError = err
			n.mu.Unlock()
			return NativePlaybackCommandResult{}, err
		}
	}
	return NativePlaybackCommandResult{AcceptedSeq: r.Seq}, nil
}
func (s *NativePlaybackService) SetViewport(r NativePlaybackViewport) (NativePlaybackRevision, error) {
	if r.Revision == 0 || r.Revision > 9007199254740991 || !finiteNative(r.X) || !finiteNative(r.Y) || !finiteNative(r.Width) || !finiteNative(r.Height) || math.Abs(r.X) > 65536 || math.Abs(r.Y) > 65536 || r.Width < 0 || r.Height < 0 || r.Width > 16384 || r.Height > 16384 {
		return NativePlaybackRevision{}, errors.New("native_invalid_request: 播放区域无效")
	}
	n, err := s.session(NativePlaybackRef{OwnerID: r.OwnerID, OwnerToken: r.OwnerToken, SessionID: r.SessionID})
	if err != nil {
		return NativePlaybackRevision{}, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closing {
		return NativePlaybackRevision{}, ErrNativeSessionClosed
	}
	digest := nativeDigest(r)
	if r.Revision < n.viewport.Revision {
		return NativePlaybackRevision{AppliedRevision: n.viewport.Revision}, nil
	}
	if r.Revision == n.viewport.Revision {
		if digest != n.viewportDigest {
			return NativePlaybackRevision{}, ErrNativeConflict
		}
		return NativePlaybackRevision{AppliedRevision: r.Revision}, nil
	}
	visible := r.Visible && r.Width > 0 && r.Height > 0
	if err = n.applyViewportLocked(r); err != nil {
		return NativePlaybackRevision{}, err
	}
	if !visible {
		if err = n.player.Command("pause", 0); err != nil {
			return NativePlaybackRevision{}, err
		}
		n.desiredPaused = true
	}
	n.viewport = r
	n.viewportDigest = digest
	return NativePlaybackRevision{AppliedRevision: r.Revision}, nil
}

// Keep drawing during a seek so a decoded replacement frame can be observed.
func (n *nativeSession) applyViewportLocked(r NativePlaybackViewport) error {
	visible := r.Visible && r.Width > 0 && r.Height > 0 && !r.Occluded && n.initialized && n.state.State != "ended" && n.state.State != "error"
	if r == n.appliedViewport && visible == n.appliedVisible {
		return nil
	}
	if err := n.player.Viewport(r.X, r.Y, r.Width, r.Height, visible); err != nil {
		return err
	}
	n.appliedViewport = r
	n.appliedVisible = visible
	return nil
}
