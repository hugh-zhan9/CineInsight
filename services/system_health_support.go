package services

import (
	"context"
	"time"
	"video-master/database"
	"video-master/models"
)

// MediaToolStatus reports executable discovery without exposing local paths.
type MediaToolStatus struct {
	Name      string
	Available bool
}

// WaitingSnapshot reads only the idle gate's in-memory waiters, without loading settings or probing the system.
func (g *IdleGate) WaitingSnapshot() ([]IdleWaitingTask, bool) {
	if g == nil || !g.mu.TryLock() {
		return nil, false
	}
	defer g.mu.Unlock()
	items := []IdleWaitingTask{}
	for _, key := range backgroundTaskKeyOrder {
		if waiter := g.earliestWaiterLocked(string(key)); waiter != nil {
			items = append(items, IdleWaitingTask{TaskKey: string(key), Reason: waiter.reason, Since: waiter.since})
		}
	}
	return items, true
}

// MediaToolAvailability uses the same resolvers as subtitle extraction and media probing.
func (s *SubtitleService) MediaToolAvailability() []MediaToolStatus {
	_, probeErr := findFFProbeBinary()
	return []MediaToolStatus{{Name: "ffmpeg", Available: s.findBinary("ffmpeg") != ""}, {Name: "ffprobe", Available: probeErr == nil}}
}

// CachedEngineStatuses never starts Python or waits behind an in-flight engine probe.
func (s *SubtitleService) CachedEngineStatuses() ([]SubtitleEngineStatus, time.Time, bool) {
	if s == nil || !s.engineStatusMu.TryLock() {
		return nil, time.Time{}, false
	}
	defer s.engineStatusMu.Unlock()
	if s.engineStatusCache == nil {
		return nil, time.Time{}, false
	}
	return append([]SubtitleEngineStatus(nil), s.engineStatusCache...), s.engineStatusAt, true
}

// CachedStatus returns the last completed face status check without Python, file IO or configuration callbacks.
func (r *FaceRuntime) CachedStatus() (FaceRuntimeStatus, time.Time, bool) {
	if r == nil {
		return FaceRuntimeStatus{}, time.Time{}, false
	}
	r.observedMu.Lock()
	defer r.observedMu.Unlock()
	if r.observedStatus == nil {
		return FaceRuntimeStatus{}, time.Time{}, false
	}
	return *r.observedStatus, r.observedAt, true
}

// SemanticHealthSnapshot describes stored index data, not external API reachability.
type SemanticHealthSnapshot struct {
	Available    bool
	Built        bool
	NeedsRebuild bool
	Coverage     SemanticSearchCoverage
}

// HealthSnapshot reads only the published video generation, with a deadline on every query.
func (s *SemanticIndexService) HealthSnapshot(ctx context.Context) (SemanticHealthSnapshot, error) {
	if s == nil || s.db == nil || !s.capability.Available {
		return SemanticHealthSnapshot{}, nil
	}
	var profile models.SemanticIndexProfile
	if err := s.db.WithContext(ctx).First(&profile, "id = ?", 1).Error; err != nil {
		return SemanticHealthSnapshot{}, err
	}
	coverage, err := s.semanticCoverage(ctx, profile)
	return SemanticHealthSnapshot{Available: true, Built: profile.Dimension > 0, NeedsRebuild: profile.NeedsRebuild, Coverage: coverage}, err
}

// HealthSnapshot reads only the published image generation, with a deadline on every query.
func (s *ImageSemanticIndexService) HealthSnapshot(ctx context.Context) (SemanticHealthSnapshot, error) {
	if s == nil || s.db == nil || !s.capability.Available {
		return SemanticHealthSnapshot{}, nil
	}
	var profile models.SemanticIndexProfile
	if err := s.db.WithContext(ctx).First(&profile, "id = ?", 1).Error; err != nil {
		return SemanticHealthSnapshot{}, err
	}
	coverage, err := s.imageSemanticCoverage(ctx, profile)
	return SemanticHealthSnapshot{Available: true, Built: profile.Dimension > 0, NeedsRebuild: profile.NeedsRebuild, Coverage: SemanticSearchCoverage{Indexed: coverage.Indexed, Total: coverage.Total}}, err
}

// RecordedHealthUsage aggregates registered proxies without enumerating cached or source files.
func (s *PlaybackProxyService) RecordedHealthUsage(ctx context.Context) (PlaybackProxyUsage, error) {
	settings, err := (&SettingsService{}).GetSettingsContext(ctx)
	if err != nil {
		return PlaybackProxyUsage{}, err
	}
	usage := PlaybackProxyUsage{LimitBytes: NormalizeProxyCacheLimitBytes(settings.ProxyCacheLimitBytes)}
	var aggregate struct {
		Count int
		Bytes int64
	}
	err = database.DB.WithContext(ctx).Model(&models.VideoPlaybackProxy{}).
		Select("COUNT(*) AS count, COALESCE(SUM(output_size), 0) AS bytes").
		Where("status = ?", models.PlaybackProxyStatusReady).Scan(&aggregate).Error
	usage.Count, usage.TotalBytes = aggregate.Count, aggregate.Bytes
	return usage, err
}
