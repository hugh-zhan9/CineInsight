package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/url"
	"os"
	"strconv"
	"strings"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// ExplorationAssetContext is carried on desktop exploration-only resource URLs.
type ExplorationAssetContext struct {
	ExplorationContext
	EntryID       uint   `json:"entry_id"`
	SourceVersion string `json:"source_version"`
}

func explorationAssetURL(path string, c ExplorationContext, id uint, source string) string {
	query := url.Values{"exploration_scope": {c.Scope}, "exploration_epoch": {c.DatabaseEpoch}, "exploration_scene": {c.SceneID}, "exploration_entry": {strconv.FormatUint(uint64(id), 10)}}
	if source != "" {
		query.Set("exploration_source", source)
	}
	return path + "?" + query.Encode()
}

func explorationCheckContext(db *gorm.DB, c ExplorationContext) (*models.MediaExplorationState, error) {
	if !validExplorationScope(c.Scope) || c.SceneID == "" {
		return nil, ErrExplorationInvalid
	}
	var state models.MediaExplorationState
	if err := db.Where("scope = ?", c.Scope).First(&state).Error; err != nil {
		return nil, err
	}
	if state.SceneID != c.SceneID {
		return nil, ErrExplorationScene
	}
	return &state, nil
}

// Items projects only requested scene members and reapplies current visibility.
func (s *MediaExplorationService) Items(ctx context.Context, r ExplorationItemsRequest) (*ExplorationCards, error) {
	if len(r.EntryIDs) == 0 || len(r.EntryIDs) > explorationBatchLimit {
		return nil, ErrExplorationInvalid
	}
	seen := map[uint]bool{}
	for _, id := range r.EntryIDs {
		if id == 0 || id > experienceMaxSafeInteger || seen[id] {
			return nil, ErrExplorationInvalid
		}
		seen[id] = true
	}
	result := &ExplorationCards{Items: []ExplorationCard{}}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		epoch, err := s.databaseEpoch(db)
		if err != nil {
			return err
		}
		if epoch != r.DatabaseEpoch {
			return ErrExplorationDatabase
		}
		return explorationDB(db).Transaction(func(tx *gorm.DB) error {
			state, err := explorationCheckContext(tx, r.ExplorationContext)
			if err != nil {
				return err
			}
			var rows []models.MediaExplorationEntry
			if err := tx.Where("state_id = ? AND scene_id = ? AND id IN ?", state.ID, state.SceneID, r.EntryIDs).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) != len(r.EntryIDs) {
				return ErrExplorationInvalid
			}
			videos, images, membership, err := explorationMediaProjection(tx, rows)
			if err != nil {
				return err
			}
			byID := map[uint]models.MediaExplorationEntry{}
			for _, row := range rows {
				byID[row.ID] = row
			}
			for _, id := range r.EntryIDs {
				row := byID[id]
				card := ExplorationCard{EntryID: row.ID, Kind: row.MediaKind, MediaID: row.MediaID}
				if row.MediaKind == "video" {
					video, ok := videos[row.MediaID]
					if !ok {
						card.UnavailableReason = "视频已不在当前可见片库"
					} else if !explorationMembershipMatches(row, membership) {
						card.UnavailableReason = "版本关系已变化，请换一墙"
					} else {
						card.Title = video.DisplayTitle
						if card.Title == "" {
							card.Title = video.Name
						}
						card.ThumbnailURL = explorationAssetURL(ThumbnailPath(video.ID), r.ExplorationContext, row.ID, "")
						if isFinitePositive(video.Duration) && video.Duration*1000 <= experienceMaxSafeInteger {
							value := int64(video.Duration * 1000)
							card.DurationMS = &value
						}
						if video.Width > 0 && video.Height > 0 {
							value := float64(video.Width) / float64(video.Height)
							card.AspectRatio = &value
						}
					}
				} else {
					photo, ok := images[row.MediaID]
					if !ok {
						card.UnavailableReason = "图片已不在当前可见图库"
					} else {
						card.Title = photo.Name
						card.ThumbnailURL = explorationAssetURL(ImageThumbnailPath(photo.ID), r.ExplorationContext, row.ID, "")
						if photo.Width > 0 && photo.Height > 0 {
							value := float64(photo.Width) / float64(photo.Height)
							card.AspectRatio = &value
						}
					}
				}
				result.Items = append(result.Items, card)
			}
			return nil
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	})
	return result, err
}

func explorationMediaProjection(db *gorm.DB, rows []models.MediaExplorationEntry) (map[uint]models.Video, map[uint]models.Image, []models.VideoVersionMember, error) {
	videoIDs, imageIDs, groupIDs := []uint{}, []uint{}, []uint{}
	for _, row := range rows {
		if row.MediaKind == "video" {
			videoIDs = append(videoIDs, row.MediaID)
		} else {
			imageIDs = append(imageIDs, row.MediaID)
		}
		if suffix, ok := strings.CutPrefix(row.UnitKey, "version-group:"); ok {
			id, err := strconv.ParseUint(suffix, 10, 64)
			if err != nil {
				return nil, nil, nil, err
			}
			groupIDs = append(groupIDs, uint(id))
		}
	}
	videos, images := map[uint]models.Video{}, map[uint]models.Image{}
	membership := []models.VideoVersionMember{}
	if len(videoIDs) > 0 {
		query, err := explorationVideoQuery(db)
		if err != nil {
			return nil, nil, nil, err
		}
		var values []models.Video
		if err := query.Where("videos.id IN ?", videoIDs).Select("videos.id, videos.name, videos.display_title, videos.path, videos.width, videos.height, videos.duration").Find(&values).Error; err != nil {
			return nil, nil, nil, err
		}
		for _, value := range values {
			videos[value.ID] = value
		}
		if err := db.Where("video_id IN ? OR group_id IN ?", videoIDs, groupIDs).Order("video_id").Find(&membership).Error; err != nil {
			return nil, nil, nil, err
		}
	}
	if len(imageIDs) > 0 {
		var values []models.Image
		if err := applyImageFilterFrom(db.Model(&models.Image{}), ImageFilter{}, db).Where("images.id IN ?", imageIDs).Select("images.id, images.name, images.path, images.width, images.height").Find(&values).Error; err != nil {
			return nil, nil, nil, err
		}
		for _, value := range values {
			images[value.ID] = value
		}
	}
	return videos, images, membership, nil
}

func explorationMembershipMatches(row models.MediaExplorationEntry, members []models.VideoVersionMember) bool {
	var group uint
	for _, member := range members {
		if member.VideoID == row.MediaID {
			group = member.GroupID
			break
		}
	}
	if group == 0 {
		return row.UnitKey == fmt.Sprintf("video:%d", row.MediaID)
	}
	if row.UnitKey != fmt.Sprintf("version-group:%d", group) {
		return false
	}
	ids := []uint{}
	for _, member := range members {
		if member.GroupID == group {
			ids = append(ids, member.VideoID)
		}
	}
	encoded, _ := json.Marshal(ids)
	return row.MemberIDsJSON == string(encoded)
}

func (s *MediaExplorationService) prepareSample(ctx context.Context, request ExplorationRequest) ([]ExplorationSample, error) {
	var points []ExplorationSample
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		epoch, err := s.databaseEpoch(db)
		if err != nil {
			return err
		}
		if epoch != request.DatabaseEpoch {
			return ErrExplorationDatabase
		}
		db = explorationDB(db)
		state, err := explorationCheckContext(db, request.ExplorationContext)
		if err != nil {
			return err
		}
		var row models.MediaExplorationEntry
		if err := db.Where("id = ? AND state_id = ? AND scene_id = ? AND media_kind = ?", request.EntryID, state.ID, state.SceneID, "video").First(&row).Error; err != nil {
			return ErrExplorationUnavailable
		}
		videos, _, members, err := explorationMediaProjection(db, []models.MediaExplorationEntry{row})
		if err != nil {
			return err
		}
		video, ok := videos[row.MediaID]
		if !ok || !explorationMembershipMatches(row, members) {
			return ErrExplorationUnavailable
		}
		info, err := os.Stat(video.Path)
		if err != nil {
			return ErrExplorationUnavailable
		}
		if !info.Mode().IsRegular() {
			return ErrExplorationUnavailable
		}
		version := videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano())
		if version != request.SourceVersion {
			return ErrBookmarkSourceChanged
		}
		if err := json.Unmarshal([]byte(row.SamplePointsJSON), &points); err != nil {
			return err
		}
		// A lost sample reply must return the saved point, not consume RNG again.
		if state.LastRequestID == request.RequestID {
			return nil
		}
		if state.Revision != request.ExpectedRevision || state.RoundID != request.RoundID {
			return ErrExplorationConflict
		}
		if !isFinitePositive(video.Duration) || video.Duration*1000 > experienceMaxSafeInteger {
			return ErrChapterDurationUnknown
		}
		duration := int64(math.Floor(video.Duration * 1000))
		if duration < 1 {
			return ErrChapterDurationUnknown
		}
		lo, hi := int64(0), duration-1
		if duration >= 10000 {
			lo, hi = duration/10, duration*9/10
		}
		valid := []ExplorationSample{}
		for _, point := range points {
			if point.SourceVersion == version {
				valid = append(valid, point)
			}
		}
		points = valid
		at := lo + rand.Int63n(hi-lo+1)
		for attempt := 0; attempt < 4; attempt++ {
			duplicate := false
			for _, point := range points {
				if point.StartMS == at {
					duplicate = true
					break
				}
			}
			if !duplicate {
				break
			}
			at = lo + (at-lo+1)%(hi-lo+1)
			if attempt == 3 {
				return ErrExplorationUnavailable
			}
		}
		end := at + 8000
		if end > duration {
			end = duration
		}
		points = append(points, ExplorationSample{StartMS: at, EndMS: end, SourceVersion: version})
		if len(points) > 3 {
			points = points[len(points)-3:]
		}
		return ctx.Err()
	})
	return points, err
}
