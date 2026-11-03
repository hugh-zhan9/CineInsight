package services

import (
	"context"
	"database/sql"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"strings"
	"time"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"
)

type SearchClipInput struct {
	VideoID       uint   `json:"video_id"`
	Title         string `json:"title"`
	StartMS       int64  `json:"start_ms"`
	EndMS         int64  `json:"end_ms"`
	SourceVersion string `json:"source_version"`
}
type SearchClipRequest struct {
	Items         []SearchClipInput `json:"items"`
	QueueRevision uint64            `json:"queue_revision"`
}

// PreviewSearchClips reuses the queue-owned frozen snapshot and commit path;
// transient search selections never create persistent bookmarks.
func (s *PlaybackQueueService) PreviewSearchClips(owner context.Context, request SearchClipRequest) (*ClipQueuePreview, error) {
	if len(request.Items) == 0 || request.QueueRevision == 0 || request.QueueRevision >= experienceMaxSafeInteger {
		return nil, ErrQueueInvalid
	}
	if len(request.Items) > 1000 {
		return nil, ErrQueueFull
	}
	for _, item := range request.Items {
		if item.VideoID == 0 || item.VideoID > experienceMaxSafeInteger || item.StartMS < 0 || item.EndMS <= item.StartMS || item.EndMS > experienceMaxSafeInteger || utf8.RuneCountInString(item.Title) > 200 || len(item.SourceVersion) > 128 {
			return nil, ErrQueueInvalid
		}
	}
	unlock, err := rLockLibraryPathsContext(owner)
	if err != nil {
		return nil, err
	}
	defer unlock()
	result := &ClipQueuePreview{TopicTitle: "搜索命中预览", QueueRevision: request.QueueRevision, Items: []ClipQueueItem{}}
	err = s.command(owner, func(ctx context.Context) error {
		snapshot := &clipQueueSnapshot{db: database.DB, expires: time.Now().Add(10 * time.Minute), queueRevision: request.QueueRevision}
		err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
			return db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
				state, err := queueStateFrom(tx)
				if err != nil {
					return err
				}
				if state.Revision != request.QueueRevision {
					return ErrQueueChanged
				}
				snapshot.player = state.Player
				result.RequiresInline = state.Player != "inline"
				result.WillInterrupt = state.ActiveToken != ""
				if state.Player == "system" && result.WillInterrupt {
					result.Notice = "系统播放器无法由队列自动停止，请先在系统播放器停止当前视频。"
				}
				var count int64
				if err := tx.Model(&models.PlaybackQueueEntry{}).Count(&count).Error; err != nil {
					return err
				}
				if count+int64(len(request.Items)) > playbackQueueCapacity {
					return ErrQueueFull
				}
				for _, input := range request.Items {
					item := ClipQueueItem{VideoID: input.VideoID, Title: strings.TrimSpace(input.Title), StartMS: input.StartMS, EndMS: input.EndMS, SourceVersion: input.SourceVersion}
					video, info, err := bookmarkSourceInScope(tx, input.VideoID)
					frozen := frozenClip{}
					switch {
					case errors.Is(err, ErrBookmarkSourceUnavailable):
						item.Reason = "原片不可用或不在当前范围"
					case errors.Is(err, ErrBookmarkSourceChanged):
						item.Reason = "原片已变化，请重新检索"
					case err != nil:
						return err
					case input.SourceVersion == "" || input.SourceVersion != videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano()):
						item.Reason = "原片已变化，请重新检索"
					case !validClipBounds(video, input.StartMS, input.EndMS):
						item.Reason = "片长未知或区间越界，请核对资料"
					default:
						frozen.sourceSize = info.Size()
						frozen.sourceMtime = info.ModTime().UnixNano()
					}
					if item.Title == "" {
						item.Title = video.DisplayTitle
						if item.Title == "" {
							item.Title = video.Name
						}
					}
					if item.Reason != "" {
						result.Conflicts++
					}
					frozen.item = item
					snapshot.clips = append(snapshot.clips, frozen)
					result.Items = append(result.Items, item)
				}
				return nil
			}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		})
		if err != nil {
			return err
		}
		if result.Conflicts == 0 {
			token, err := s.cacheClipPreview(snapshot)
			if err != nil {
				return err
			}
			result.PreviewToken = token
			result.ExpiresAt = snapshot.expires
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
