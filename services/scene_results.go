package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"math"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"
)

var ErrSearchCursorInvalid = errors.New("search_cursor_invalid")

type SearchResultHit struct {
	SceneHit
	SegmentID      uint   `json:"segment_id"`
	SourceVersion  string `json:"source_version"`
	PreviewStartMS int64  `json:"preview_start_ms"`
	PreviewEndMS   int64  `json:"preview_end_ms"`
}
type DialogueSearchRequest struct {
	Query   string         `json:"query"`
	Filter  *LibraryFilter `json:"filter"`
	VideoID uint           `json:"video_id"`
	Cursor  string         `json:"cursor"`
	Limit   int            `json:"limit"`
}
type DialogueVideoGroup struct {
	VideoID    uint              `json:"video_id"`
	Title      string            `json:"title"`
	HitCount   int64             `json:"hit_count"`
	Hits       []SearchResultHit `json:"hits"`
	NextCursor string            `json:"next_cursor"`
}
type DialogueVideoPage struct {
	Items      []DialogueVideoGroup `json:"items"`
	NextCursor string               `json:"next_cursor"`
	Coverage   SceneCoverage        `json:"coverage"`
	Notices    []string             `json:"notices"`
	Extent     string               `json:"extent"`
}
type DialogueHitPage struct {
	Items      []SearchResultHit `json:"items"`
	HitCount   int64             `json:"hit_count"`
	NextCursor string            `json:"next_cursor"`
	Extent     string            `json:"extent"`
}
type RankedSearchPage struct {
	Items     []SearchResultHit `json:"items"`
	Coverage  SceneCoverage     `json:"coverage"`
	Notices   []string          `json:"notices"`
	Extent    string            `json:"extent"`
	Truncated bool              `json:"truncated"`
}
type dialogueCursor struct {
	Binding   string
	VideoID   uint
	StartMS   int64
	SegmentID uint
	Expires   int64
}
type signedDialogueCursor struct {
	Value dialogueCursor
	MAC   []byte
}

func normalizeDialogueRequest(request DialogueSearchRequest, grouped bool) (DialogueSearchRequest, error) {
	normalized, err := normalizeSceneSearchRequest(SceneSearchRequest{Query: request.Query, Mode: SceneModeDialogue})
	if err != nil {
		return request, err
	}
	request.Query = strings.ToLower(normalized.Query)
	filter := LibraryFilter{}
	if request.Filter != nil {
		filter = *request.Filter
	}
	filter.SortMode = ""
	filter.CollapseVersions = false
	filter, err = normalizeLibraryFilter(filter)
	if err != nil {
		return request, err
	}
	request.Filter = &filter
	if grouped && request.VideoID != 0 || !grouped && (request.VideoID == 0 || request.VideoID > experienceMaxSafeInteger) {
		return request, ErrSceneQueryInvalid
	}
	max, def := 200, 30
	if grouped {
		max, def = 50, 20
	}
	if request.Limit <= 0 {
		request.Limit = def
	}
	if request.Limit > max {
		request.Limit = max
	}
	return request, nil
}
func dialogueBinding(request DialogueSearchRequest, grouped bool) string {
	encoded, _ := json.Marshal(struct {
		Query   string
		Filter  *LibraryFilter
		VideoID uint
		Grouped bool
		DB      string
	}{request.Query, request.Filter, request.VideoID, grouped, fmt.Sprintf("%p", database.DB)})
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}
func (s *SceneSearchService) signDialogueCursor(value dialogueCursor) string {
	s.cursorOnce.Do(func() { s.cursorKey = []byte(uuid.NewString()) })
	encoded, _ := json.Marshal(value)
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write(encoded)
	signed, _ := json.Marshal(signedDialogueCursor{value, mac.Sum(nil)})
	return base64.RawURLEncoding.EncodeToString(signed)
}
func (s *SceneSearchService) readDialogueCursor(request DialogueSearchRequest, grouped bool) (dialogueCursor, error) {
	value := dialogueCursor{Binding: dialogueBinding(request, grouped), Expires: time.Now().Add(10 * time.Minute).Unix()}
	if request.Cursor == "" {
		return value, nil
	}
	if len(request.Cursor) > 4096 {
		return value, ErrSearchCursorInvalid
	}
	encoded, err := base64.RawURLEncoding.DecodeString(request.Cursor)
	if err != nil {
		return value, ErrSearchCursorInvalid
	}
	var signed signedDialogueCursor
	if json.Unmarshal(encoded, &signed) != nil {
		return value, ErrSearchCursorInvalid
	}
	s.cursorOnce.Do(func() { s.cursorKey = []byte(uuid.NewString()) })
	raw, _ := json.Marshal(signed.Value)
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write(raw)
	if !hmac.Equal(mac.Sum(nil), signed.MAC) || signed.Value.Binding != value.Binding || signed.Value.Expires <= time.Now().Unix() || signed.Value.VideoID == 0 || signed.Value.VideoID > experienceMaxSafeInteger || signed.Value.StartMS < 0 || signed.Value.StartMS > experienceMaxSafeInteger || signed.Value.SegmentID > experienceMaxSafeInteger || grouped && signed.Value.SegmentID != 0 || !grouped && (signed.Value.VideoID != request.VideoID || signed.Value.SegmentID == 0) {
		return value, ErrSearchCursorInvalid
	}
	return signed.Value, nil
}
func dialogueScope(tx *gorm.DB, filter *LibraryFilter) (*gorm.DB, error) {
	scope, err := applyLibraryFilter(tx.Model(&models.Video{}), *filter, time.Now())
	if err != nil {
		return nil, err
	}
	return scope.Where("videos.is_stale = ?", false), nil
}
func dialogueMatches(tx *gorm.DB, request DialogueSearchRequest) (*gorm.DB, error) {
	scope, err := dialogueScope(tx, request.Filter)
	if err != nil {
		return nil, err
	}
	query := tx.Model(&models.SubtitleSegment{}).Where("video_id IN (?)", scope.Select("videos.id")).Where(`LOWER(text) LIKE ? ESCAPE '\'`, "%"+escapeSQLLike(request.Query)+"%")
	if request.VideoID != 0 {
		query = query.Where("video_id = ?", request.VideoID)
	}
	return query, nil
}
func dialogueCoverage(tx *gorm.DB, request DialogueSearchRequest) (SceneCoverage, error) {
	scope, err := dialogueScope(tx, request.Filter)
	if err != nil {
		return SceneCoverage{}, err
	}
	var row struct {
		Total     int64
		Indexed   int64
		Unindexed int64
	}
	err = scope.Select("COUNT(*) AS total, COALESCE(SUM(CASE WHEN " + sceneSubtitleIndexedSQL + " THEN 1 ELSE 0 END),0) AS indexed, COALESCE(SUM(CASE WHEN NOT " + sceneSubtitleIndexedSQL + " AND (EXISTS (SELECT 1 FROM subtitle_index_states sis2 WHERE sis2.video_id = videos.id AND sis2.has_sidecar) OR " + hasEmbeddedSubtitleSQL + ") THEN 1 ELSE 0 END),0) AS unindexed").Scan(&row).Error
	return SceneCoverage{TotalVideos: row.Total, SubtitleIndexed: row.Indexed, SubtitleUnindexed: row.Unindexed}, err
}

// Scan at most 1000 candidate videos per call. Every returned video has its
// own SRT checked; removed candidates cannot expose unchecked replacements.
// If the bound is reached, the signed cursor continues after the examined IDs.
func (s *SceneSearchService) refreshDialoguePage(ctx context.Context, request DialogueSearchRequest, cursor dialogueCursor, grouped bool) ([]uint, uint, bool, error) {
	if s.syncSubtitles != nil {
		s.syncSubtitles()
	}
	verified := []uint{}
	last := cursor.VideoID
	exhausted := false
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		if !grouped {
			scope, err := dialogueScope(db, request.Filter)
			if err != nil {
				return err
			}
			var videos []models.Video
			if err := scope.Where("videos.id = ?", request.VideoID).Find(&videos).Error; err != nil {
				return err
			}
			for _, video := range videos {
				if err := ensureSubtitleIndexForVideoOn(db, video, nil); err != nil {
					return err
				}
			}
			exhausted = true
			return nil
		}
		checked := 0
		for checked < 1000 && len(verified) <= request.Limit {
			if err := ctx.Err(); err != nil {
				return err
			}
			query, err := dialogueMatches(db, request)
			if err != nil {
				return err
			}
			var ids []uint
			batch := min(50, 1000-checked)
			if err := query.Where("video_id > ?", last).Distinct("video_id").Order("video_id ASC").Limit(batch).Pluck("video_id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				exhausted = true
				break
			}
			for _, id := range ids {
				last = id
				checked++
				var video models.Video
				if err := db.First(&video, id).Error; errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				} else if err != nil {
					return err
				}
				if err := ensureSubtitleIndexForVideoOn(db, video, nil); err != nil {
					return err
				}
				current, err := dialogueMatches(db, request)
				if err != nil {
					return err
				}
				var count int64
				if err := current.Where("video_id = ?", id).Count(&count).Error; err != nil {
					return err
				}
				if count > 0 {
					verified = append(verified, id)
				}
				if len(verified) > request.Limit {
					break
				}
			}
			if len(verified) <= request.Limit && len(ids) < batch {
				exhausted = true
				break
			}
		}
		return nil
	})
	return verified, last, exhausted, err
}
func sourceResultHit(hit SceneHit, video models.Video, source string) SearchResultHit {
	result := SearchResultHit{SceneHit: hit, SourceVersion: source, PreviewStartMS: hit.StartMS, PreviewEndMS: hit.EndMS}
	if hit.Source == SceneSourceDialogue {
		result.PreviewStartMS = max(int64(0), hit.StartMS-2000)
		result.PreviewEndMS = hit.EndMS + 2000
		if video.Duration > 0 && !math.IsInf(video.Duration, 0) && !math.IsNaN(video.Duration) && video.Duration*1000 < experienceMaxSafeInteger {
			result.PreviewEndMS = min(result.PreviewEndMS, int64(math.Floor(video.Duration*1000)))
		}
	}
	return result
}
func (s *SceneSearchService) dialogueHitsFrom(tx *gorm.DB, request DialogueSearchRequest, cursor dialogueCursor) (*DialogueHitPage, error) {
	result := &DialogueHitPage{Items: []SearchResultHit{}, Extent: "exhaustive_page"}
	query, err := dialogueMatches(tx, request)
	if err != nil {
		return nil, err
	}
	if err := query.Count(&result.HitCount).Error; err != nil {
		return nil, err
	}
	video, info, sourceErr := bookmarkSourceInScope(tx, request.VideoID)
	if sourceErr != nil && !errors.Is(sourceErr, ErrBookmarkSourceUnavailable) && !errors.Is(sourceErr, ErrBookmarkSourceChanged) {
		return nil, sourceErr
	}
	source := ""
	if sourceErr == nil {
		source = videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano())
	}
	query, err = dialogueMatches(tx, request)
	if err != nil {
		return nil, err
	}
	if cursor.SegmentID != 0 {
		query = query.Where("start_time_ms > ? OR (start_time_ms = ? AND id > ?)", cursor.StartMS, cursor.StartMS, cursor.SegmentID)
	}
	var rows []models.SubtitleSegment
	if err := query.Order("start_time_ms ASC,id ASC").Limit(request.Limit + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	more := len(rows) > request.Limit
	if more {
		rows = rows[:request.Limit]
	}
	title := strings.TrimSpace(video.DisplayTitle)
	if title == "" {
		title = video.Name
	}
	for _, row := range rows {
		hit := sourceResultHit(SceneHit{VideoID: row.VideoID, Title: title, StartMS: row.StartTimeMs, EndMS: row.EndTimeMs, Source: SceneSourceDialogue, Score: 1, Text: row.Text}, video, source)
		hit.SegmentID = row.ID
		result.Items = append(result.Items, hit)
	}
	if more {
		last := rows[len(rows)-1]
		cursor.VideoID = request.VideoID
		cursor.StartMS = last.StartTimeMs
		cursor.SegmentID = last.ID
		result.NextCursor = s.signDialogueCursor(cursor)
	}
	return result, nil
}
func (s *SceneSearchService) SearchDialogueVideoGroups(ctx context.Context, request DialogueSearchRequest) (*DialogueVideoPage, error) {
	request, err := normalizeDialogueRequest(request, true)
	if err != nil {
		return nil, err
	}
	cursor, err := s.readDialogueCursor(request, true)
	if err != nil {
		return nil, err
	}
	verified, examined, exhausted, err := s.refreshDialoguePage(ctx, request, cursor, true)
	if err != nil {
		return nil, err
	}
	result := &DialogueVideoPage{Items: []DialogueVideoGroup{}, Notices: []string{}, Extent: "exhaustive_page"}
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
			query, err := dialogueMatches(tx, request)
			if err != nil {
				return err
			}
			var ids []uint
			if err := query.Where("video_id IN ?", verified).Distinct("video_id").Order("video_id ASC").Limit(request.Limit+1).Pluck("video_id", &ids).Error; err != nil {
				return err
			}
			more := len(ids) > request.Limit
			if more {
				ids = ids[:request.Limit]
			}
			for _, id := range ids {
				hitRequest := request
				hitRequest.VideoID = id
				hitRequest.Limit = 3
				hitRequest.Cursor = ""
				hitCursor, err := s.readDialogueCursor(hitRequest, false)
				if err != nil {
					return err
				}
				hits, err := s.dialogueHitsFrom(tx, hitRequest, hitCursor)
				if err != nil {
					return err
				}
				title := ""
				if len(hits.Items) > 0 {
					title = hits.Items[0].Title
				}
				result.Items = append(result.Items, DialogueVideoGroup{VideoID: id, Title: title, HitCount: hits.HitCount, Hits: hits.Items, NextCursor: hits.NextCursor})
			}
			if more || !exhausted {
				cursor.VideoID = examined
				if len(ids) > 0 {
					cursor.VideoID = ids[len(ids)-1]
				}
				result.NextCursor = s.signDialogueCursor(cursor)
			}
			result.Coverage, err = dialogueCoverage(tx, request)
			if result.Coverage.SubtitleIndexed == 0 {
				result.Notices = append(result.Notices, SceneNoticeSubtitleIndexEmpty)
			}
			return err
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	})
	return result, err
}
func (s *SceneSearchService) ListDialogueVideoHits(ctx context.Context, request DialogueSearchRequest) (*DialogueHitPage, error) {
	request, err := normalizeDialogueRequest(request, false)
	if err != nil {
		return nil, err
	}
	cursor, err := s.readDialogueCursor(request, false)
	if err != nil {
		return nil, err
	}
	if _, _, _, err := s.refreshDialoguePage(ctx, request, cursor, false); err != nil {
		return nil, err
	}
	var result *DialogueHitPage
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
			var err error
			result, err = s.dialogueHitsFrom(tx, request, cursor)
			return err
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	})
	return result, err
}
