package services

import (
	"context"
	"strings"
)

type NativePlaybackSource struct {
	VideoID       uint   `json:"video_id"`
	SourceVersion string `json:"source_version"`
}

func SelectNativePlaybackSession(session *PreviewSession, capability NativePlaybackCapability) *PreviewSession {
	if session == nil || session.SourceVersion == "" || !strings.HasPrefix(capability.Platform, "darwin/") {
		return session
	}
	switch session.Mode {
	case "proxy-preparing", "proxy-manual", "external-preview":
	default:
		return session
	}
	result := *session
	result.InlineSource = nil
	result.NativeSource = nil
	if capability.Available {
		result.Mode = "native"
		result.NativeSource = &NativePlaybackSource{VideoID: session.VideoID, SourceVersion: session.SourceVersion}
		result.ReasonCode = ""
		result.ReasonMessage = ""
	} else {
		result.Mode = "native-unavailable"
		result.ReasonCode = capability.ReasonCode
		result.ReasonMessage = capability.ReasonMessage
	}
	return &result
}
func (s *PlaybackQueueService) AuthorizeNativeSource(ctx context.Context, r NativePlaybackOpenRequest) error {
	return s.command(ctx, func(context.Context) error {
		run := s.run
		if run == nil || run.failed || run.player != "inline" || run.lease == nil || run.token != r.QueueToken || run.lease.video.ID != r.VideoID || run.lease.sourceVersion() != r.SourceVersion || run.entry.EntryKind == "clip" {
			return ErrQueueLeaseExpired
		}
		return nil
	})
}

func (s *VideoService) SetNativePlaybackCapabilityProvider(provider func() NativePlaybackCapability) {
	if provider == nil {
		s.nativeCapability.Store(nil)
	} else {
		s.nativeCapability.Store(&provider)
	}
}
func (s *VideoService) nativePlaybackAvailable() bool {
	provider := s.nativeCapability.Load()
	return provider != nil && (*provider)().Available
}
