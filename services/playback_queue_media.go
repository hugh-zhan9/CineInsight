package services

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

var (
	ErrQueueSourceChanged     = errors.New("queue_source_changed: 原片已改变，请重新播放")
	ErrQueueLeaseExpired      = errors.New("queue_lease_expired: 当前播放会话已结束")
	ErrQueueInlineUnsupported = errors.New("queue_inline_unsupported: 当前文件不能在应用内播放，请先生成播放代理或选择播放器")
)

// Owns an already-open descriptor, not a pathname to reopen on later Range
// requests. os.File supports concurrent ReadAt and Close; each body has its own
// SectionReader, and no body holds a database/path lock.
type queueMediaLease struct {
	mu       sync.Mutex
	file     *os.File
	video    models.Video
	source   playbackProxyFingerprint
	delivery os.FileInfo
	mime     string
	start    float64
	origin   string
}

func (s *VideoService) prepareQueueMedia(ctx context.Context, id uint, inline bool) (*queueMediaLease, error) {
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var lease *queueMediaLease
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var video models.Video
		if err := db.First(&video, id).Error; err != nil {
			return err
		}
		if video.IsStale {
			return ErrQueueMediaUnavailable
		}
		info, err := os.Stat(video.Path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrQueueMediaUnavailable
		}
		path := video.Path
		mimeType, _ := inlinePreviewMIME(path)
		if inline {
			proxy, err := s.playbackProxies().resolveValidProxyFrom(db, video.ID, playbackProxyFingerprintOf(info), true)
			if err != nil {
				return err
			}
			if proxy != nil {
				path = proxy.Path
				mimeType = playbackProxyMIME
			} else if mimeType == "" {
				return ErrQueueInlineUnsupported
			}
		}
		var settings models.Settings
		if err := db.Select("playback_resume_mode").First(&settings).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		start, origin := queueStartPosition(video, settings.PlaybackResumeMode)
		observed := info
		if path != video.Path {
			observed, err = os.Stat(path)
			if err != nil {
				return err
			}
		}
		if !observed.Mode().IsRegular() {
			return ErrQueueMediaUnavailable
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		accepted := false
		defer func() {
			if !accepted {
				file.Close()
			}
		}()
		actual, err := file.Stat()
		if err != nil {
			return err
		}
		if !actual.Mode().IsRegular() || !os.SameFile(observed, actual) || !playbackProxyFingerprintOf(observed).matches(playbackProxyFingerprintOf(actual)) {
			return ErrQueueSourceChanged
		}
		current, err := os.Stat(video.Path)
		if err != nil {
			return err
		}
		if !os.SameFile(info, current) || !playbackProxyFingerprintOf(info).matches(playbackProxyFingerprintOf(current)) {
			return ErrQueueSourceChanged
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		lease = &queueMediaLease{file: file, video: video, source: playbackProxyFingerprintOf(info), delivery: actual, mime: mimeType, start: start, origin: origin}
		accepted = true
		return nil
	})
	return lease, err
}

func queueStartPosition(video models.Video, mode string) (float64, string) {
	if !shouldRestartPlayback(mode, &video) && resumable(&video) {
		return video.WatchPositionSeconds, WatchProgressOriginResume
	}
	return 0, WatchProgressOriginStart
}

func (m *queueMediaLease) Close() {
	m.mu.Lock()
	file := m.file
	m.file = nil
	m.mu.Unlock()
	if file != nil {
		_ = file.Close()
	}
}

func (m *queueMediaLease) sourceVersion() string {
	return videoSourceVersion(m.video.ID, m.source.size, m.source.modTimeNS)
}

// Called only after the service has authorized the exact active token. The
// controller rechecks ownership before handing this reader to the HTTP handler.
func (m *queueMediaLease) reader(ctx context.Context) (*io.SectionReader, string, time.Time, error) {
	m.mu.Lock()
	file := m.file
	m.mu.Unlock()
	if file == nil {
		return nil, "", time.Time{}, ErrQueueLeaseExpired
	}
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	defer unlock()
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var video models.Video
		if err := db.Select("id", "path", "is_stale").First(&video, m.video.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrQueueMediaUnavailable
			}
			return err
		}
		if video.IsStale {
			return ErrQueueMediaUnavailable
		}
		info, err := os.Stat(video.Path)
		if err != nil {
			return ErrQueueMediaUnavailable
		}
		if !info.Mode().IsRegular() || !m.source.matches(playbackProxyFingerprintOf(info)) {
			return ErrQueueSourceChanged
		}
		actual, err := file.Stat()
		if err != nil {
			return ErrQueueLeaseExpired
		}
		if !actual.Mode().IsRegular() || !os.SameFile(m.delivery, actual) || !playbackProxyFingerprintOf(m.delivery).matches(playbackProxyFingerprintOf(actual)) {
			return ErrQueueSourceChanged
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, "", time.Time{}, err
	}
	return io.NewSectionReader(file, 0, m.delivery.Size()), m.mime, m.delivery.ModTime(), nil
}
