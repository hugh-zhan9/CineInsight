package services

import (
	"context"
	"errors"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const playbackQueueCapacity = 10000

var (
	ErrQueueChanged          = errors.New("queue_changed: 待播队列已改变，请刷新后重试")
	ErrQueueInvalid          = errors.New("queue_invalid: 请检查队列操作和分页参数")
	ErrQueueFull             = errors.New("queue_full: 待播队列最多一万项，单次最多加入一千个视频")
	ErrQueueMediaUnavailable = errors.New("queue_media_unavailable: 视频或作品集已不可用")
)

type QueueQuery struct {
	Revision       uint64 `json:"revision"`
	CursorPosition int64  `json:"cursor_position"`
	CursorID       uint   `json:"cursor_id"`
	Limit          int    `json:"limit"`
}

type QueueEdit struct {
	ExpectedRevision uint64 `json:"expected_revision"`
	Action           string `json:"action"`
	VideoIDs         []uint `json:"video_ids"`
	CollectionID     uint   `json:"collection_id"`
	StartVideoID     uint   `json:"start_video_id"`
	StartAfter       bool   `json:"start_after"`
	EntryID          uint   `json:"entry_id"`
	BeforeEntryID    uint   `json:"before_entry_id"`
	Player           string `json:"player"`
	Autoplay         *bool  `json:"autoplay"`
}

type QueueEntryDTO struct {
	models.PlaybackQueueEntry
	SourceAvailable bool `json:"source_available"`
}

type QueuePage struct {
	State          models.PlaybackQueueState `json:"state"`
	Items          []QueueEntryDTO           `json:"items"`
	Current        *QueueEntryDTO            `json:"current"`
	Total          int64                     `json:"total"`
	HasMore        bool                      `json:"has_more"`
	CursorPosition int64                     `json:"cursor_position"`
	CursorID       uint                      `json:"cursor_id"`
}

func initializePlaybackQueue(ctx context.Context) error {
	return database.TransactionWithContext(ctx, func(db *gorm.DB) error {
		initial := models.PlaybackQueueState{ID: 1, Revision: 1, Player: "system", Status: "idle"}
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&initial).Error; err != nil {
			return err
		}
		return db.Model(&models.PlaybackQueueState{}).Where("id = ? AND (active_token <> ? OR status IN ?)", 1, "", []string{"starting", "playing", "paused", "dispatched"}).Updates(map[string]any{"active_token": "", "status": "interrupted", "revision": gorm.Expr("revision + 1"), "last_error_code": "", "last_error_message": ""}).Error
	})
}

func queueStateFrom(db *gorm.DB) (models.PlaybackQueueState, error) {
	var state models.PlaybackQueueState
	err := db.First(&state, 1).Error
	return state, err
}

// All state mutation, including controller transitions, claims this same CAS in
// its transaction. No queue-owned database lock or process-only write guarantee.
func claimQueueRevision(db *gorm.DB, revision uint64) error {
	if revision == 0 || revision >= 9007199254740991 {
		return ErrQueueInvalid
	}
	r := db.Model(&models.PlaybackQueueState{}).Where("id = ? AND revision = ?", 1, revision).Updates(map[string]any{"revision": gorm.Expr("revision + 1"), "updated_at": time.Now()})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrQueueChanged
	}
	return nil
}

func loadQueuePage(ctx context.Context, q QueueQuery) (*QueuePage, error) {
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > 200 || q.CursorPosition < 0 || (q.CursorID == 0) != (q.CursorPosition == 0) || q.CursorID != 0 && q.Revision == 0 {
		return nil, ErrQueueInvalid
	}
	page := &QueuePage{Items: []QueueEntryDTO{}}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		state, err := queueStateFrom(db)
		if err != nil {
			return err
		}
		if q.Revision != 0 && state.Revision != q.Revision {
			return ErrQueueChanged
		}
		page.State = state
		if err := db.Model(&models.PlaybackQueueEntry{}).Count(&page.Total).Error; err != nil {
			return err
		}
		query := db.Order("position ASC").Order("id ASC")
		if q.CursorID != 0 {
			query = query.Where("position > ? OR (position = ? AND id > ?)", q.CursorPosition, q.CursorPosition, q.CursorID)
		}
		var rows []models.PlaybackQueueEntry
		if err := query.Limit(q.Limit + 1).Find(&rows).Error; err != nil {
			return err
		}
		page.HasMore = len(rows) > q.Limit
		if page.HasMore {
			rows = rows[:q.Limit]
		}
		ids := make([]uint, 0, len(rows)+1)
		for _, row := range rows {
			ids = append(ids, row.VideoID)
		}
		if state.CurrentEntryID != nil {
			var current models.PlaybackQueueEntry
			if err := db.First(&current, *state.CurrentEntryID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					latest, checkErr := queueStateFrom(db)
					if checkErr != nil {
						return checkErr
					}
					if latest.Revision != state.Revision {
						return ErrQueueChanged
					}
				}
				return err
			}
			page.Current = &QueueEntryDTO{PlaybackQueueEntry: current}
			ids = append(ids, current.VideoID)
		}
		available, err := noteAvailableVideos(db, ids)
		if err != nil {
			return err
		}
		for _, row := range rows {
			page.Items = append(page.Items, QueueEntryDTO{PlaybackQueueEntry: row, SourceAvailable: available[row.VideoID]})
		}
		if page.Current != nil {
			page.Current.SourceAvailable = available[page.Current.VideoID]
		}
		if len(rows) > 0 {
			last := rows[len(rows)-1]
			page.CursorID = last.ID
			page.CursorPosition = last.Position
		}
		latest, err := queueStateFrom(db)
		if err != nil {
			return err
		}
		if latest.Revision != state.Revision {
			return ErrQueueChanged
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}

func validateQueueEdit(in QueueEdit) error {
	if in.ExpectedRevision == 0 {
		return ErrQueueInvalid
	}
	// Reject irrelevant payloads instead of silently acting on an ambiguous form.
	allowed := in
	allowed.ExpectedRevision = 0
	allowed.Action = ""
	switch in.Action {
	case "append_videos":
		if len(in.VideoIDs) == 0 {
			return ErrQueueInvalid
		}
		if len(in.VideoIDs) > 1000 {
			return ErrQueueFull
		}
		for _, id := range in.VideoIDs {
			if id == 0 {
				return ErrQueueInvalid
			}
		}
		allowed.VideoIDs = nil
	case "append_collection", "replace_collection":
		if in.CollectionID == 0 {
			return ErrQueueInvalid
		}
		allowed.CollectionID = 0
		if in.StartAfter && in.StartVideoID == 0 {
			return ErrQueueInvalid
		}
		allowed.StartAfter = false
		allowed.StartVideoID = 0
	case "move":
		if in.EntryID == 0 || in.EntryID == in.BeforeEntryID {
			return ErrQueueInvalid
		}
		allowed.EntryID = 0
		allowed.BeforeEntryID = 0
	case "remove":
		if in.EntryID == 0 {
			return ErrQueueInvalid
		}
		allowed.EntryID = 0
	case "clear":
	case "configure":
		if (in.Player != "system" && in.Player != "inline" && in.Player != "iina") || in.Autoplay == nil || in.Player == "system" && *in.Autoplay {
			return ErrQueueInvalid
		}
		allowed.Player = ""
		allowed.Autoplay = nil
	default:
		return ErrQueueInvalid
	}
	if len(allowed.VideoIDs) > 0 || allowed.CollectionID != 0 || allowed.StartVideoID != 0 || allowed.StartAfter || allowed.EntryID != 0 || allowed.BeforeEntryID != 0 || allowed.Player != "" || allowed.Autoplay != nil {
		return ErrQueueInvalid
	}
	return nil
}

type preparedQueueEdit struct {
	input  QueueEdit
	state  models.PlaybackQueueState
	videos []models.Video
}

// A bounded immutable plan belongs to the queue command owner. A collection
// change after this read cannot widen the approved append/replace.
func preparePlaybackQueueEdit(ctx context.Context, in QueueEdit) (*preparedQueueEdit, error) {
	if err := validateQueueEdit(in); err != nil {
		return nil, err
	}
	plan := &preparedQueueEdit{input: in}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		state, err := queueStateFrom(db)
		if err != nil {
			return err
		}
		if state.Revision != in.ExpectedRevision {
			return ErrQueueChanged
		}
		plan.state = state
		switch in.Action {
		case "append_videos", "append_collection", "replace_collection":
			if in.Action == "append_videos" {
				plan.videos, err = queueVideosFrom(db, in.VideoIDs)
			} else {
				plan.videos, err = (&CollectionService{}).orderedActiveVideosFrom(db, in.CollectionID, in.StartVideoID, in.StartAfter, playbackQueueCapacity+1)
			}
			if err == nil && len(plan.videos) == 0 && in.StartAfter {
				return ErrQueueAtEnd
			}
			if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && len(plan.videos) == 0 {
				return ErrQueueMediaUnavailable
			}
			if err != nil {
				return err
			}
			var count int64
			if in.Action != "replace_collection" {
				if err = db.Model(&models.PlaybackQueueEntry{}).Count(&count).Error; err != nil {
					return err
				}
			}
			if count+int64(len(plan.videos)) > playbackQueueCapacity {
				return ErrQueueFull
			}
		case "move", "remove":
			ids := []uint{in.EntryID}
			if in.BeforeEntryID != 0 {
				ids = append(ids, in.BeforeEntryID)
			}
			var count int64
			if err := db.Model(&models.PlaybackQueueEntry{}).Where("id IN ?", ids).Count(&count).Error; err != nil {
				return err
			}
			if count != int64(len(ids)) {
				return ErrQueueChanged
			}
		}
		latest, err := queueStateFrom(db)
		if err != nil {
			return err
		}
		if latest.Revision != state.Revision {
			return ErrQueueChanged
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}
func editPlaybackQueue(ctx context.Context, in QueueEdit) (*models.PlaybackQueueState, error) {
	plan, err := preparePlaybackQueueEdit(ctx, in)
	if err != nil {
		return nil, err
	}
	return applyPreparedQueueEdit(ctx, plan)
}

func applyPreparedQueueEdit(ctx context.Context, plan *preparedQueueEdit) (*models.PlaybackQueueState, error) {
	in := plan.input
	var result models.PlaybackQueueState
	err := database.TransactionWithContext(ctx, func(db *gorm.DB) error {
		if err := claimQueueRevision(db, in.ExpectedRevision); err != nil {
			return err
		}
		state, err := queueStateFrom(db)
		if err != nil {
			return err
		}
		switch in.Action {
		case "append_videos", "append_collection", "replace_collection":
			videos := plan.videos
			if in.Action == "replace_collection" {
				if err = db.Where("1 = 1").Delete(&models.PlaybackQueueEntry{}).Error; err != nil {
					return err
				}
				if err = resetQueueCurrent(db); err != nil {
					return err
				}
			}
			var position int64
			if err = db.Model(&models.PlaybackQueueEntry{}).Select("COALESCE(MAX(position), 0)").Scan(&position).Error; err != nil {
				return err
			}
			entries := make([]models.PlaybackQueueEntry, 0, len(videos))
			for _, video := range videos {
				position++
				entries = append(entries, models.PlaybackQueueEntry{VideoID: video.ID, Title: diaryVideoTitle(video), Position: position})
			}
			if err = db.CreateInBatches(&entries, 200).Error; err != nil {
				return err
			}
		case "move":
			if err = moveQueueEntry(db, in.EntryID, in.BeforeEntryID); err != nil {
				return err
			}
		case "remove":
			r := db.Delete(&models.PlaybackQueueEntry{}, in.EntryID)
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				return ErrQueueChanged
			}
			if state.CurrentEntryID != nil && *state.CurrentEntryID == in.EntryID {
				if err = resetQueueCurrent(db); err != nil {
					return err
				}
			}
		case "clear":
			if err = db.Where("1 = 1").Delete(&models.PlaybackQueueEntry{}).Error; err != nil {
				return err
			}
			if err = resetQueueCurrent(db); err != nil {
				return err
			}
		case "configure":
			updates := map[string]any{"player": in.Player, "autoplay": *in.Autoplay}
			if state.Player != in.Player {
				updates["status"] = "stopped"
				updates["active_token"] = ""
				updates["last_error_code"] = ""
				updates["last_error_message"] = ""
			}
			if err = db.Model(&models.PlaybackQueueState{}).Where("id = ?", 1).Updates(updates).Error; err != nil {
				return err
			}
		}
		result, err = queueStateFrom(db)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func resetQueueCurrent(db *gorm.DB) error {
	return db.Model(&models.PlaybackQueueState{}).Where("id = ?", 1).Updates(map[string]any{"current_entry_id": nil, "status": "idle", "active_token": "", "last_error_code": "", "last_error_message": ""}).Error
}

func queueVideosFrom(db *gorm.DB, ids []uint) ([]models.Video, error) {
	var videos []models.Video
	if err := db.Select("id", "name", "display_title").Where("id IN ?", ids).Find(&videos).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]models.Video, len(videos))
	for _, video := range videos {
		byID[video.ID] = video
	}
	ordered := make([]models.Video, 0, len(ids))
	for _, id := range ids {
		video, ok := byID[id]
		if !ok {
			return nil, ErrQueueMediaUnavailable
		}
		ordered = append(ordered, video)
	}
	return ordered, nil
}

func moveQueueEntry(db *gorm.DB, id, beforeID uint) error {
	var entry models.PlaybackQueueEntry
	if err := db.First(&entry, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrQueueChanged
		}
		return err
	}
	var target int64
	if beforeID == 0 {
		if err := db.Model(&models.PlaybackQueueEntry{}).Select("COALESCE(MAX(position), 0) + 1").Scan(&target).Error; err != nil {
			return err
		}
	} else {
		var before models.PlaybackQueueEntry
		if err := db.First(&before, beforeID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrQueueChanged
			}
			return err
		}
		target = before.Position
	}
	// Shift only the affected range. Position need not be dense after removals.
	query := db.Model(&models.PlaybackQueueEntry{})
	if target > entry.Position {
		if err := query.Where("position > ? AND position < ?", entry.Position, target).Update("position", gorm.Expr("position - 1")).Error; err != nil {
			return err
		}
		target--
	} else {
		if err := query.Where("position >= ? AND position < ?", target, entry.Position).Update("position", gorm.Expr("position + 1")).Error; err != nil {
			return err
		}
	}
	return db.Model(&models.PlaybackQueueEntry{}).Where("id = ?", id).Update("position", target).Error
}
