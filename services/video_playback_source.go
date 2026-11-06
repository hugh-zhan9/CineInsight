package services

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"net/url"
	"video-master/database"
)

// VisiblePlaybackSource is a read-only validation result, not a playback lease.
// Opening the actual player must still revalidate this source fingerprint.
type VisiblePlaybackSource struct {
	VideoID       uint   `json:"video_id"`
	SourceVersion string `json:"source_version"`
}

func (s *VideoService) ResolveVisibleVideoForPlayback(ctx context.Context, id uint) (*VisiblePlaybackSource, error) {
	if id == 0 || id > experienceMaxSafeInteger {
		return nil, errors.New("invalid_input: 视频ID无效")
	}
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var result *VisiblePlaybackSource
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		video, info, err := bookmarkSourceInScope(db, id)
		if err != nil {
			return err
		}
		result = &VisiblePlaybackSource{VideoID: video.ID, SourceVersion: videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano())}
		return nil
	})
	return result, err
}

// GetVerifiedPreviewSession preserves a prior selection's source identity through
// both session creation and subsequent Range requests from the embedded player.
func (s *VideoService) GetVerifiedPreviewSession(ctx context.Context, id uint, expected string) (*PreviewSession, error) {
	source, err := s.ResolveVisibleVideoForPlayback(ctx, id)
	if err != nil {
		return nil, err
	}
	if expected == "" || source.SourceVersion != expected {
		return nil, ErrBookmarkSourceChanged
	}
	session, err := s.GetPreviewSession(id)
	if err != nil {
		return nil, err
	}
	if session.SourceVersion != expected {
		return nil, ErrBookmarkSourceChanged
	}
	if session.InlineSource != nil {
		session.InlineSource.LocatorValue += "?source_version=" + url.QueryEscape(expected)
	}
	return session, nil
}
