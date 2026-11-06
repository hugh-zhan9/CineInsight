package services

import (
	"gorm.io/gorm"
	"time"
	"video-master/models"
)

// Playback hints only control background admission. They never record viewing.
func (s *VideoStructureService) SetPlaybackActivity(owner string, playing bool) {
	if owner == "" || len(owner) > 128 {
		return
	}
	s.mu.Lock()
	if s.playback == nil {
		s.playback = map[string]time.Time{}
	}
	now := s.now()
	for id, until := range s.playback {
		if !until.After(now) {
			delete(s.playback, id)
		}
	}
	if !playing {
		delete(s.playback, owner)
	} else if len(s.playback) < 64 {
		s.playback[owner] = now.Add(5 * time.Second)
	}
	cancel := s.windowCancel
	s.mu.Unlock()
	if playing && cancel != nil {
		cancel()
	}
}
func (s *VideoStructureService) MediaTransfer(active bool) {
	s.mu.Lock()
	if active {
		s.transfers++
	} else if s.transfers > 0 {
		s.transfers--
		s.mediaQuietUntil = s.now().Add(30 * time.Second)
	}
	cancel := s.windowCancel
	s.mu.Unlock()
	if active && cancel != nil {
		cancel()
	}
}
func (s *VideoStructureService) ForegroundBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for owner, until := range s.playback {
		if !until.After(now) {
			delete(s.playback, owner)
		}
	}
	return len(s.playback) > 0 || s.transfers > 0 || s.mediaQuietUntil.After(now)
}

// SourceObserved runs inside the original probe database operation after its snapshot commits.
// The borrowed database cannot follow a global DB swap; stop drains admitted callbacks.
// Conditional invalidation survives ABA and cannot be lost to a checkpoint revision bump.
func (s *VideoStructureService) SourceObserved(db *gorm.DB, id uint, size, mtime int64) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.sourceCallbacks.Add(1)
	s.mu.Unlock()
	defer s.sourceCallbacks.Done()
	source := videoSourceVersion(id, size, mtime)
	s.mu.Lock()
	different := s.activeVideo == id && s.activeSource != "" && s.activeSource != source
	cancelWindow := s.windowCancel
	s.mu.Unlock()
	if different && cancelWindow != nil {
		cancelWindow()
	}
	result := db.Model(&models.VideoStructureState{}).Where("video_id = ? AND source_version <> ?", id, source).Updates(map[string]interface{}{"source_version": source, "source_size": size, "source_mod_time_ns": mtime, "status": "source_changed", "revision": gorm.Expr("revision + 1"), "generation": gorm.Expr("generation + 1"), "run_token": "", "checkpoint_ms": 0, "pending_start_ms": 0, "pending_frame_pts": 0, "previous_sample_pts": 0, "previous_sample": nil, "phase": "analyze", "duration_ms": 0})
	if result.RowsAffected > 0 || result.Error != nil {
		s.RequestDiscovery()
	}
}
