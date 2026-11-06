package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func playbackSubtitleToken(fingerprint SubtitleFingerprint) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s", fingerprint.Size, fingerprint.ModTimeNS, fingerprint.SHA256)))
	return hex.EncodeToString(digest[:])
}
func validNativeSubtitlePreferences(p NativeSubtitlePreferences) bool {
	return (p.Size == "small" || p.Size == "medium" || p.Size == "large" || p.Size == "xlarge") && (p.Background == "none" || p.Background == "translucent" || p.Background == "solid") && (p.Position == "bottom" || p.Position == "middle" || p.Position == "top")
}
func (s *NativePlaybackService) ConfigureSubtitles(ctx context.Context, r NativePlaybackSubtitles) (result NativePlaybackSubtitleResult, err error) {
	if r.Revision == 0 || r.Revision > 9007199254740991 || r.OffsetMS < -60000 || r.OffsetMS > 60000 || !validNativeSubtitlePreferences(r.Preferences) {
		return result, fmt.Errorf("native_invalid_request: 字幕设置无效")
	}
	ref := NativePlaybackRef{OwnerID: r.OwnerID, OwnerToken: r.OwnerToken, SessionID: r.SessionID}
	n, err := s.session(ref)
	if err != nil {
		return result, err
	}
	digest := nativeDigest(r)
	n.mu.Lock()
	if n.closing || n.state.State == "error" {
		n.mu.Unlock()
		return result, ErrNativeSessionClosed
	}
	if r.Revision < n.subtitleRevision {
		n.mu.Unlock()
		return result, fmt.Errorf("native_stale_command: 字幕设置已过期")
	}
	if r.Revision == n.subtitleRevision {
		if digest != n.subtitleDigest {
			n.mu.Unlock()
			return result, ErrNativeConflict
		}
		done := n.subtitleWait
		n.mu.Unlock()
		if err = waitNative(ctx, done); err != nil {
			return result, err
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		if r.Revision != n.subtitleRevision {
			return result, fmt.Errorf("native_stale_command: 字幕设置已过期")
		}
		return n.subtitleResult, n.subtitleError
	}
	done := make(chan struct{})
	n.subtitleRevision = r.Revision
	n.subtitleDigest = digest
	n.subtitleWait = done
	// Clear the old document before any source IO. Failed refresh cannot leave it visible.
	err = n.player.Subtitles(map[string]any{"cues": []PlaybackSubtitleCue{}, "offset_ms": 0, "preferences": r.Preferences})
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		if n.subtitleRevision == r.Revision {
			n.subtitleResult = result
			n.subtitleError = err
		}
		close(done)
		n.mu.Unlock()
	}()
	if err != nil {
		return result, err
	}
	result = NativePlaybackSubtitleResult{AppliedRevision: r.Revision, Status: "absent"}
	if !r.Enabled {
		return result, nil
	}
	if s.subtitle == nil {
		return result, fmt.Errorf("subtitle_unavailable: 字幕读取服务未初始化")
	}
	read, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(n.ctx, cancel)
	defer func() { stop(); cancel() }()
	document, err := s.subtitle(read, PlaybackSubtitleRequest{VideoID: n.source.videoID, SourceVersion: n.source.version, QueueToken: n.request.QueueToken})
	if err != nil {
		return result, err
	}
	if document.Status != "ready" {
		return result, nil
	}
	token := playbackSubtitleToken(document.SubtitleFingerprint)
	if token != r.SubtitleFingerprint {
		return result, fmt.Errorf("subtitle_conflict: 字幕已变化，请刷新字幕")
	}
	if _, err = s.session(ref); err != nil {
		return result, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closing || read.Err() != nil {
		return result, ErrNativeSessionClosed
	}
	if n.subtitleRevision != r.Revision {
		return result, fmt.Errorf("native_stale_command: 字幕设置已过期")
	}
	if err = n.player.Subtitles(map[string]any{"cues": document.Cues, "offset_ms": r.OffsetMS, "preferences": r.Preferences}); err != nil {
		return result, err
	}
	result.Status = "ready"
	result.SubtitleFingerprint = token
	return result, nil
}
