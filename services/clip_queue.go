package services

import (
	"context"
	"database/sql"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"math"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"
)

var ErrClipPreviewExpired = errors.New("clip_preview_expired: 预览已过期或来源已变化，请重新预览")
var ErrClipPlayerUnsupported = errors.New("clip_player_unsupported: 区间预览需要明确选择应用内播放器")
var ErrClipBatchOrder = errors.New("clip_batch_order: 预览批次不能局部重排，请回到专题调整顺序后重新加入")

type ClipQueueRequest struct {
	TopicID       uint   `json:"topic_id"`
	TopicRevision uint64 `json:"topic_revision"`
	BookmarkIDs   []uint `json:"bookmark_ids"`
	All           bool   `json:"all"`
	QueueRevision uint64 `json:"queue_revision"`
}
type ClipQueueItem struct {
	BookmarkID       uint   `json:"bookmark_id"`
	BookmarkRevision uint64 `json:"bookmark_revision"`
	VideoID          uint   `json:"video_id"`
	Title            string `json:"title"`
	StartMS          int64  `json:"start_ms"`
	EndMS            int64  `json:"end_ms"`
	SourceVersion    string `json:"source_version"`
	Reason           string `json:"reason"`
}
type ClipQueuePreview struct {
	PreviewToken   string          `json:"preview_token"`
	ExpiresAt      time.Time       `json:"expires_at" ts_type:"string"`
	TopicID        uint            `json:"topic_id"`
	TopicTitle     string          `json:"topic_title"`
	QueueRevision  uint64          `json:"queue_revision"`
	Items          []ClipQueueItem `json:"items"`
	Conflicts      int             `json:"conflicts"`
	RequiresInline bool            `json:"requires_inline"`
	WillInterrupt  bool            `json:"will_interrupt"`
	Notice         string          `json:"notice"`
}
type AppendClipQueueRequest struct {
	PreviewToken   string `json:"preview_token"`
	SwitchToInline bool   `json:"switch_to_inline"`
}
type ClipQueueAppendResult struct {
	EntryIDs      []uint `json:"entry_ids"`
	FirstEntryID  uint   `json:"first_entry_id"`
	BatchID       string `json:"batch_id"`
	PlayRequested bool   `json:"play_requested"`
	Warning       string `json:"warning"`
}
type frozenClip struct {
	item        ClipQueueItem
	sourceSize  int64
	sourceMtime int64
}
type clipQueueSnapshot struct {
	db            *gorm.DB
	expires       time.Time
	topicID       uint
	topicRevision uint64
	queueRevision uint64
	player        string
	clips         []frozenClip
	result        *ClipQueueAppendResult
}

func validClipBounds(video models.Video, start, end int64) bool {
	return !math.IsNaN(video.Duration) && !math.IsInf(video.Duration, 0) && video.Duration > 0 && start >= 0 && end > start && end <= experienceMaxSafeInteger && float64(start) < video.Duration*1000 && float64(end) <= video.Duration*1000
}
func validQueueEntryShape(entry models.PlaybackQueueEntry) bool {
	if entry.EntryKind == "video" {
		return entry.ClipStartMS == nil && entry.ClipEndMS == nil && entry.SourceSize == nil && entry.SourceModTimeNS == nil && entry.OriginBookmarkID == nil && entry.PreviewBatchID == nil && entry.PreviewPosition == nil && entry.PreviewCount == nil
	}
	if entry.EntryKind != "clip" || entry.ClipStartMS == nil || entry.ClipEndMS == nil || entry.SourceSize == nil || entry.SourceModTimeNS == nil || entry.PreviewBatchID == nil || entry.PreviewPosition == nil || entry.PreviewCount == nil {
		return false
	}
	_, err := uuid.Parse(*entry.PreviewBatchID)
	return err == nil && *entry.ClipStartMS >= 0 && *entry.ClipEndMS > *entry.ClipStartMS && *entry.ClipEndMS <= experienceMaxSafeInteger && *entry.SourceSize >= 0 && *entry.PreviewPosition >= 1 && *entry.PreviewPosition <= *entry.PreviewCount && *entry.PreviewCount <= 1000 && (entry.OriginBookmarkID == nil || *entry.OriginBookmarkID > 0)
}
func (s *PlaybackQueueService) pruneClipPreviews() {
	for token, preview := range s.clipPreviews {
		if preview.db != database.DB || !time.Now().Before(preview.expires) {
			delete(s.clipPreviews, token)
		}
	}
}
func (s *PlaybackQueueService) cacheClipPreview(snapshot *clipQueueSnapshot) (string, error) {
	s.pruneClipPreviews()
	pending := 0
	for _, entry := range s.clipPreviews {
		if entry.result == nil {
			pending++
		}
	}
	if pending >= 16 || len(s.clipPreviews) >= 4096 {
		return "", errors.New("invalid_input: 未确认预览过多，请稍后重试")
	}
	if s.clipPreviews == nil {
		s.clipPreviews = map[string]*clipQueueSnapshot{}
	}
	token := uuid.NewString()
	s.clipPreviews[token] = snapshot
	return token, nil
}
func (s *PlaybackQueueService) PreviewClipQueue(owner context.Context, request ClipQueueRequest) (*ClipQueuePreview, error) {
	if !validClipTopicIdentity(request.TopicID, request.TopicRevision) || request.QueueRevision == 0 || request.QueueRevision >= experienceMaxSafeInteger || request.All && len(request.BookmarkIDs) != 0 || !request.All && len(request.BookmarkIDs) == 0 || len(request.BookmarkIDs) > 1000 {
		return nil, ErrClipTopicInvalid
	}
	selected := map[uint]bool{}
	for _, id := range request.BookmarkIDs {
		if id == 0 || id > experienceMaxSafeInteger || selected[id] {
			return nil, ErrClipTopicInvalid
		}
		selected[id] = true
	}
	unlock, err := rLockLibraryPathsContext(owner)
	if err != nil {
		return nil, err
	}
	defer unlock()
	result := &ClipQueuePreview{TopicID: request.TopicID, QueueRevision: request.QueueRevision, Items: []ClipQueueItem{}}
	err = s.command(owner, func(ctx context.Context) error {
		snapshot := &clipQueueSnapshot{db: database.DB, expires: time.Now().Add(10 * time.Minute), topicID: request.TopicID, topicRevision: request.TopicRevision, queueRevision: request.QueueRevision}
		err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
			return db.Transaction(func(tx *gorm.DB) error {
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
				var topic models.ClipTopic
				if err := tx.First(&topic, request.TopicID).Error; err != nil {
					return err
				}
				if topic.Revision != request.TopicRevision {
					return ErrClipTopicChanged
				}
				result.TopicTitle = topic.Name
				query := tx.Where("topic_id = ?", request.TopicID)
				if !request.All {
					query = query.Where("bookmark_id IN ?", request.BookmarkIDs)
				}
				var members []models.ClipTopicMember
				if err := query.Order("position ASC,bookmark_id ASC").Limit(1001).Find(&members).Error; err != nil {
					return err
				}
				if len(members) == 0 {
					return ErrClipTopicInvalid
				}
				if len(members) > 1000 {
					return ErrQueueFull
				}
				if !request.All && len(members) != len(selected) {
					return ErrClipTopicChanged
				}
				var queued int64
				if err := tx.Model(&models.PlaybackQueueEntry{}).Count(&queued).Error; err != nil {
					return err
				}
				if queued+int64(len(members)) > playbackQueueCapacity {
					return ErrQueueFull
				}
				for _, member := range members {
					var book models.VideoBookmark
					if err := tx.First(&book, member.BookmarkID).Error; err != nil {
						return err
					}
					item := ClipQueueItem{BookmarkID: book.ID, BookmarkRevision: book.Revision, VideoID: book.VideoID, Title: book.Title, StartMS: book.StartMS}
					if book.EndMS != nil {
						item.EndMS = *book.EndMS
					}
					frozen := frozenClip{item: item, sourceSize: book.SourceSize, sourceMtime: book.SourceModTimeNS}
					video, info, err := bookmarkSourceInScope(tx, book.VideoID)
					switch {
					case errors.Is(err, ErrBookmarkSourceUnavailable):
						item.Reason = "原片不可用或不在当前范围"
					case errors.Is(err, ErrBookmarkSourceChanged):
						item.Reason = "原片已变化，请重新核对书签"
					case err != nil:
						return err
					case book.SourceSize != info.Size() || book.SourceModTimeNS != info.ModTime().UnixNano():
						item.Reason = "原片已变化，请重新核对书签"
					case book.EndMS == nil:
						item.Reason = "只有时间点，请先补充结束时间"
					case !validClipBounds(video, book.StartMS, item.EndMS):
						item.Reason = "片长未知或区间越界，请补齐资料并核对时间"
					}
					if item.Reason == "" {
						item.SourceVersion = videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano())
					} else {
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
func cloneClipAppendResult(result *ClipQueueAppendResult) *ClipQueueAppendResult {
	cloned := *result
	cloned.EntryIDs = append([]uint{}, result.EntryIDs...)
	return &cloned
}
func validateFrozenClips(tx *gorm.DB, snapshot *clipQueueSnapshot) error {
	ids := []uint{}
	for _, clip := range snapshot.clips {
		if clip.item.BookmarkID != 0 {
			ids = append(ids, clip.item.BookmarkID)
		}
	}
	if len(ids) > 0 {
		var rows []models.VideoBookmark
		if err := tx.Where("id IN ?", ids).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != len(ids) {
			return ErrClipPreviewExpired
		}
		byID := map[uint]models.VideoBookmark{}
		for _, book := range rows {
			byID[book.ID] = book
		}
		for _, clip := range snapshot.clips {
			if clip.item.BookmarkID == 0 {
				continue
			}
			book := byID[clip.item.BookmarkID]
			if book.Revision != clip.item.BookmarkRevision || book.EndMS == nil || book.StartMS != clip.item.StartMS || *book.EndMS != clip.item.EndMS || book.VideoID != clip.item.VideoID {
				return ErrClipPreviewExpired
			}
		}
	}
	if snapshot.topicID != 0 {
		var topic models.ClipTopic
		if err := tx.First(&topic, snapshot.topicID).Error; err != nil {
			return err
		}
		if topic.Revision != snapshot.topicRevision {
			return ErrClipTopicChanged
		}
	}
	for _, clip := range snapshot.clips {
		video, info, err := bookmarkSourceInScope(tx, clip.item.VideoID)
		if err != nil {
			return err
		}
		if info.Size() != clip.sourceSize || info.ModTime().UnixNano() != clip.sourceMtime || !validClipBounds(video, clip.item.StartMS, clip.item.EndMS) {
			return ErrClipPreviewExpired
		}
	}
	return nil
}
func (s *PlaybackQueueService) AppendClipQueue(owner context.Context, request AppendClipQueueRequest) (*ClipQueueAppendResult, error) {
	unlock, err := rLockLibraryPathsContext(owner)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var result *ClipQueueAppendResult
	err = s.command(owner, func(ctx context.Context) error {
		s.pruneClipPreviews()
		snapshot := s.clipPreviews[request.PreviewToken]
		if snapshot == nil {
			return ErrClipPreviewExpired
		}
		if snapshot.result != nil {
			result = cloneClipAppendResult(snapshot.result)
			return nil
		}
		if snapshot.player != "inline" && !request.SwitchToInline {
			return ErrClipPlayerUnsupported
		}
		result = &ClipQueueAppendResult{EntryIDs: []uint{}, BatchID: uuid.NewString()}
		err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
			if err := validateFrozenClips(tx, snapshot); err != nil {
				return err
			}
			if err := claimClipQueueSnapshot(tx, snapshot); err != nil {
				return err
			}
			state, err := queueStateFrom(tx)
			if err != nil {
				return err
			}
			if state.Player != snapshot.player {
				return ErrQueueChanged
			}
			var count int64
			if err := tx.Model(&models.PlaybackQueueEntry{}).Count(&count).Error; err != nil {
				return err
			}
			if count+int64(len(snapshot.clips)) > playbackQueueCapacity {
				return ErrQueueFull
			}
			var position int64
			if err := tx.Model(&models.PlaybackQueueEntry{}).Select("COALESCE(MAX(position),0)").Scan(&position).Error; err != nil {
				return err
			}
			if position > experienceMaxSafeInteger-int64(len(snapshot.clips)) {
				return ErrQueueInvalid
			}
			entries := []models.PlaybackQueueEntry{}
			for i, clip := range snapshot.clips {
				start, end, size, mtime, batchPosition, batchCount := clip.item.StartMS, clip.item.EndMS, clip.sourceSize, clip.sourceMtime, i+1, len(snapshot.clips)
				batchID := result.BatchID
				entry := models.PlaybackQueueEntry{VideoID: clip.item.VideoID, Title: clip.item.Title, Position: position + int64(i) + 1, EntryKind: "clip", ClipStartMS: &start, ClipEndMS: &end, SourceSize: &size, SourceModTimeNS: &mtime, PreviewBatchID: &batchID, PreviewPosition: &batchPosition, PreviewCount: &batchCount}
				if clip.item.BookmarkID != 0 {
					id := clip.item.BookmarkID
					entry.OriginBookmarkID = &id
				}
				entries = append(entries, entry)
			}
			if err := tx.CreateInBatches(&entries, 200).Error; err != nil {
				return err
			}
			for _, entry := range entries {
				result.EntryIDs = append(result.EntryIDs, entry.ID)
			}
			result.FirstEntryID = entries[0].ID
			if state.Player != "inline" {
				if err := tx.Model(&models.PlaybackQueueState{}).Where("id = 1").Update("player", "inline").Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		snapshot.result = result
		snapshot.clips = nil
		if snapshot.player != "inline" && s.run != nil {
			if err := s.stopRuntimeLocked(false); err != nil {
				result.Warning = "片段已加入，但原播放器尚未清理，请先在队列中重试停止。"
				s.changed()
				result = cloneClipAppendResult(snapshot.result)
				return nil
			}
		}
		if err := s.playLocked(ctx, result.FirstEntryID, snapshot.queueRevision+1); err != nil {
			result.Warning = "片段已加入，但未能开始，请打开队列后手动播放。"
		} else {
			result.PlayRequested = true
		}
		s.changed()
		result = cloneClipAppendResult(snapshot.result)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func validateClipLease(ctx context.Context, lease *queueMediaLease, entry models.PlaybackQueueEntry) error {
	if !validQueueEntryShape(entry) || entry.EntryKind != "clip" || lease == nil || lease.source.size != *entry.SourceSize || lease.source.modTimeNS != *entry.SourceModTimeNS || !validClipBounds(lease.video, *entry.ClipStartMS, *entry.ClipEndMS) {
		return ErrQueueSourceChanged
	}
	return clipVideoVisible(ctx, entry.VideoID)
}
func clipVideoVisible(ctx context.Context, id uint) error {
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		query, err := visibleVideoQuery(db.Model(&models.Video{}))
		if err != nil {
			return err
		}
		var count int64
		if err := query.Where("videos.id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrQueueMediaUnavailable
		}
		return nil
	})
}

// All source revisions participate in the same conditional queue-root write.
// A concurrent edit cannot be accepted from a separately read stale decision.
func claimClipQueueSnapshot(tx *gorm.DB, snapshot *clipQueueSnapshot) error {
	query := tx.Model(&models.PlaybackQueueState{}).Where("id = 1 AND revision = ?", snapshot.queueRevision)
	if snapshot.topicID != 0 {
		topic := tx.Model(&models.ClipTopic{}).Select("1").Where("id = ? AND revision = ?", snapshot.topicID, snapshot.topicRevision)
		query = query.Where("EXISTS (?)", topic)
	}
	ids := []uint{}
	args := []any{}
	var expression strings.Builder
	expression.WriteString("CASE id")
	for _, clip := range snapshot.clips {
		if clip.item.BookmarkID == 0 {
			continue
		}
		ids = append(ids, clip.item.BookmarkID)
		expression.WriteString(" WHEN ? THEN ?")
		args = append(args, clip.item.BookmarkID, clip.item.BookmarkRevision)
	}
	if len(ids) > 0 {
		expression.WriteString(" ELSE CAST(-1 AS BIGINT) END")
		books := tx.Model(&models.VideoBookmark{}).Select("COUNT(*)").Where("id IN ?", ids).Where("revision = "+expression.String(), args...)
		query = query.Where("(?) = ?", books, len(ids))
	}
	update := query.Updates(map[string]any{"revision": gorm.Expr("revision + 1"), "updated_at": time.Now()})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		state, err := queueStateFrom(tx)
		if err != nil {
			return err
		}
		if state.Revision != snapshot.queueRevision {
			return ErrQueueChanged
		}
		return ErrClipPreviewExpired
	}
	return nil
}

// DiscardClipQueuePreview retires an unsubmitted preview; committed receipts stay idempotent.
func (s *PlaybackQueueService) DiscardClipQueuePreview(ctx context.Context, token string) (bool, error) {
	discarded := false
	err := s.command(ctx, func(context.Context) error {
		s.pruneClipPreviews()
		preview := s.clipPreviews[token]
		if preview != nil && preview.result == nil {
			delete(s.clipPreviews, token)
			discarded = true
		}
		return nil
	})
	return discarded, err
}
