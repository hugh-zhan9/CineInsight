package services

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"os"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"
)

// PlaybackSubtitleRequest identifies a media session; callers never supply a file path.
type PlaybackSubtitleRequest struct {
	VideoID       uint   `json:"video_id"`
	SourceVersion string `json:"source_version"`
	QueueToken    string `json:"queue_token,omitempty"`
}
type PlaybackSubtitleCue struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
}
type PlaybackSubtitle struct {
	Status              string                `json:"status"`
	SubtitleFingerprint SubtitleFingerprint   `json:"subtitle_fingerprint"`
	Cues                []PlaybackSubtitleCue `json:"cues"`
}

// GetPlaybackSubtitle reads a bounded, strict projection without changing the source or playback state.
// A queue owner must authorize QueueToken before calling this method.
func (s *SubtitleWorkbenchService) GetPlaybackSubtitle(ctx context.Context, request PlaybackSubtitleRequest) (*PlaybackSubtitle, error) {
	if request.VideoID == 0 {
		return nil, errors.New("视频信息无效")
	}
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var result *PlaybackSubtitle
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		query, err := applyScanScope(db.Model(&models.Video{}), true)
		if err != nil {
			return err
		}
		var video models.Video
		if err := query.Where("videos.is_stale = ?", false).First(&video, request.VideoID).Error; err != nil {
			return err
		}
		before, err := os.Stat(video.Path)
		if err != nil {
			return fmt.Errorf("读取视频失败: %s", subtitleIOReason(err))
		}
		if !before.Mode().IsRegular() || request.SourceVersion == "" || videoSourceVersion(video.ID, before.Size(), before.ModTime().UnixNano()) != request.SourceVersion {
			return ErrQueueSourceChanged
		}
		doc, err := s.GetDocument(video)
		if err != nil {
			var coded *SubtitleCodedError
			if errors.As(err, &coded) && (coded.Code == "subtitle_missing" || coded.Code == "subtitle_not_sidecar_srt") {
				result = &PlaybackSubtitle{Status: "absent", Cues: []PlaybackSubtitleCue{}}
			} else {
				return err
			}
		} else {
			if validation := s.Validate(doc.Entries); !validation.Valid {
				return fmt.Errorf("字幕格式不可播放，请在字幕工作台修复")
			}
			_, fingerprint, _, err := readSubtitleForEditing(subtitleparser.SRTPathForVideo(video.Path))
			if err != nil {
				return err
			}
			if fingerprint != doc.Fingerprint {
				return fmt.Errorf("subtitle_conflict: 字幕已变化，请重新加载")
			}
			result = &PlaybackSubtitle{Status: "ready", SubtitleFingerprint: doc.Fingerprint, Cues: make([]PlaybackSubtitleCue, 0, len(doc.Entries))}
			for _, entry := range doc.Entries {
				result.Cues = append(result.Cues, PlaybackSubtitleCue{StartMS: entry.StartTimeMs, EndMS: entry.EndTimeMs, Text: entry.Text})
			}
		}
		after, err := os.Stat(video.Path)
		if err != nil {
			return ErrQueueSourceChanged
		}
		if !os.SameFile(before, after) || videoSourceVersion(video.ID, after.Size(), after.ModTime().UnixNano()) != request.SourceVersion {
			return ErrQueueSourceChanged
		}
		return ctx.Err()
	})
	return result, err
}

// GetPlaybackSubtitle holds queue ownership throughout the read, excluding stale inline sessions.
func (s *PlaybackQueueService) GetPlaybackSubtitle(ctx context.Context, workbench *SubtitleWorkbenchService, request PlaybackSubtitleRequest) (*PlaybackSubtitle, error) {
	var result *PlaybackSubtitle
	err := s.command(ctx, func(ctx context.Context) error {
		if s.run == nil || s.run.failed || s.run.lease == nil || s.run.player != "inline" || s.run.token != request.QueueToken || s.run.lease.video.ID != request.VideoID || s.run.lease.sourceVersion() != request.SourceVersion {
			return ErrQueueLeaseExpired
		}
		var err error
		result, err = workbench.GetPlaybackSubtitle(ctx, request)
		return err
	})
	return result, err
}
