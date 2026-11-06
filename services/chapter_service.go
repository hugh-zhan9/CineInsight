package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"
)

var ErrChapterInvalid = errors.New("chapter_invalid")
var ErrChapterConflict = errors.New("chapter_conflict")
var ErrChapterDurationUnknown = errors.New("chapter_duration_unknown")
var ErrChapterRangeConflict = errors.New("chapter_range_conflict")

type ChapterInput struct {
	ID      uint   `json:"id"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}
type ChapterSetView struct {
	SetID         string         `json:"set_id"`
	Kind          string         `json:"kind"`
	Revision      uint64         `json:"revision"`
	SourceVersion string         `json:"source_version"`
	SourceStatus  string         `json:"source_status"`
	Chapters      []ChapterInput `json:"chapters"`
}
type VideoChapterSets struct {
	Automatic     *VideoStructureView `json:"automatic,omitempty"`
	VideoID       uint                `json:"video_id"`
	Title         string              `json:"title"`
	DurationMS    int64               `json:"duration_ms"`
	SourceVersion string              `json:"source_version"`
	Embedded      *ChapterSetView     `json:"embedded"`
	Manual        *ChapterSetView     `json:"manual"`
}
type SaveChaptersRequest struct {
	VideoID          uint           `json:"video_id"`
	ExpectedRevision uint64         `json:"expected_revision"`
	SourceVersion    string         `json:"source_version"`
	Chapters         []ChapterInput `json:"chapters"`
	Clear            bool           `json:"clear"`
}
type ResolveChapterRequest struct {
	SetID     string `json:"set_id"`
	ChapterID uint   `json:"chapter_id"`
	Revision  uint64 `json:"revision"`
}
type ChapterResolution struct {
	VideoID       uint   `json:"video_id"`
	StartMS       int64  `json:"start_ms"`
	SourceVersion string `json:"source_version"`
}
type ChapterService struct {
	textAI *TextAIService
	owner  chapterOwner
}

func NewChapterService(textAI *TextAIService) *ChapterService {
	return &ChapterService{textAI: textAI, owner: chapterOwner{budget: chapterDraftBudget, active: map[uint]chapterRuntime{}, work: NewMediaWorkSlot(), previews: map[string]*chapterApplicationSnapshot{}}}
}
func chapterDuration(video models.Video) int64 {
	ms := video.Duration * 1000
	if math.IsNaN(ms) || math.IsInf(ms, 0) || ms <= 0 || ms > experienceMaxSafeInteger {
		return 0
	}
	return int64(math.Floor(ms))
}
func normalizeChapters(input []ChapterInput, duration int64) ([]ChapterInput, error) {
	if duration <= 0 {
		return nil, ErrChapterDurationUnknown
	}
	if len(input) > 500 {
		return nil, ErrChapterInvalid
	}
	result := make([]ChapterInput, 0, len(input))
	seen := map[uint]bool{}
	var end int64
	for _, chapter := range input {
		chapter.Title = strings.TrimSpace(chapter.Title)
		chapter.Summary = strings.TrimSpace(chapter.Summary)
		if chapter.ID > experienceMaxSafeInteger || chapter.ID != 0 && seen[chapter.ID] || !utf8.ValidString(chapter.Title) || !utf8.ValidString(chapter.Summary) || utf8.RuneCountInString(chapter.Title) < 1 || utf8.RuneCountInString(chapter.Title) > 200 || utf8.RuneCountInString(chapter.Summary) > 2000 || chapter.StartMS < end || chapter.EndMS <= chapter.StartMS || chapter.EndMS > duration {
			return nil, ErrChapterInvalid
		}
		seen[chapter.ID] = chapter.ID != 0
		end = chapter.EndMS
		result = append(result, chapter)
	}
	return result, nil
}
func chapterRows(tx *gorm.DB, setID uint) ([]models.VideoChapter, error) {
	var rows []models.VideoChapter
	err := tx.Where("set_id = ?", setID).Order("position ASC").Find(&rows).Error
	return rows, err
}
func chapterInputs(rows []models.VideoChapter) []ChapterInput {
	result := make([]ChapterInput, 0, len(rows))
	for _, row := range rows {
		result = append(result, ChapterInput{ID: row.ID, StartMS: row.StartMS, EndMS: row.EndMS, Title: row.Title, Summary: row.Summary})
	}
	return result
}
func manualChapterView(set models.ChapterSet, rows []models.VideoChapter, current string) *ChapterSetView {
	source := videoSourceVersion(set.VideoID, set.SourceSize, set.SourceModTimeNS)
	status := "ready"
	if current == "" {
		status = "unavailable"
	} else if source != current {
		status = "changed"
	}
	return &ChapterSetView{SetID: fmt.Sprintf("manual:%d", set.ID), Kind: "manual", Revision: set.Revision, SourceVersion: source, SourceStatus: status, Chapters: chapterInputs(rows)}
}
func (s *ChapterService) GetVideoChapterSets(ctx context.Context, id uint) (*VideoChapterSets, error) {
	if id == 0 || id > experienceMaxSafeInteger {
		return nil, ErrChapterInvalid
	}
	result := &VideoChapterSets{VideoID: id}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(db *gorm.DB) error {
			video, info, sourceErr := bookmarkSourceInScope(db, id)
			if sourceErr != nil && !errors.Is(sourceErr, ErrBookmarkSourceUnavailable) && !errors.Is(sourceErr, ErrBookmarkSourceChanged) {
				return sourceErr
			}
			if sourceErr == nil {
				result.SourceVersion = videoSourceVersion(id, info.Size(), info.ModTime().UnixNano())
				result.DurationMS = chapterDuration(video)
			}
			result.Title = video.DisplayTitle
			if result.Title == "" {
				result.Title = video.Name
			}
			var set models.ChapterSet
			err := db.Where("video_id = ?", id).First(&set).Error
			if err == nil {
				rows, err := chapterRows(db, set.ID)
				if err != nil {
					return err
				}
				result.Manual = manualChapterView(set, rows, result.SourceVersion)
				if result.Title == "" {
					result.Title = set.TitleSnapshot
				}
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if result.SourceVersion == "" && result.Manual == nil {
				return ErrBookmarkSourceUnavailable
			}
			embedded := &ChapterSetView{SetID: fmt.Sprintf("embedded:%d", id), Kind: "embedded", SourceStatus: "unread", Chapters: []ChapterInput{}}
			var metadata models.VideoTechnicalMetadata
			err = db.Where("video_id = ?", id).First(&metadata).Error
			if err == nil && metadata.ChaptersError != "" {
				embedded.SourceStatus = "invalid"
			} else if err == nil && metadata.ChaptersJSON != nil {
				if json.Unmarshal([]byte(*metadata.ChaptersJSON), &embedded.Chapters) != nil {
					return ErrChapterInvalid
				}
				if metadata.SuccessfulSourceSize != nil && metadata.SuccessfulSourceModTimeNS != nil {
					embedded.SourceVersion = videoSourceVersion(id, *metadata.SuccessfulSourceSize, *metadata.SuccessfulSourceModTimeNS)
				}
				digest := sha256.Sum256([]byte(embedded.SourceVersion + ":" + *metadata.ChaptersJSON))
				embedded.Revision = (binary.BigEndian.Uint64(digest[:8]) & ((1 << 52) - 1)) + 1
				embedded.SourceStatus = "ready"
				if result.SourceVersion == "" {
					embedded.SourceStatus = "unavailable"
				} else if embedded.SourceVersion != result.SourceVersion {
					embedded.SourceStatus = "changed"
				}
			} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			result.Embedded = embedded
			if video.ID != 0 {
				automatic, err := readVideoStructureView(db, id, info, sourceErr)
				if err != nil {
					return err
				}
				result.Automatic = automatic
			}
			return nil
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	})
	return result, err
}
func replaceChapterRows(tx *gorm.DB, setID uint, chapters []ChapterInput, origins map[uint]*uint, newOrigin *uint) error {
	if err := tx.Where("set_id = ?", setID).Delete(&models.VideoChapter{}).Error; err != nil {
		return err
	}
	rows := make([]models.VideoChapter, 0, len(chapters))
	for i, item := range chapters {
		origin := newOrigin
		if item.ID != 0 {
			origin = origins[item.ID]
		}
		rows = append(rows, models.VideoChapter{ID: item.ID, SetID: setID, Position: i + 1, StartMS: item.StartMS, EndMS: item.EndMS, Title: item.Title, Summary: item.Summary, OriginJobID: origin})
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.CreateInBatches(&rows, 100).Error
}
func saveChapterSetTx(tx *gorm.DB, video models.Video, size, mtime int64, expected uint64, chapters []ChapterInput, origin *uint) (*ChapterSetView, error) {
	var set models.ChapterSet
	err := tx.Where("video_id = ?", video.ID).First(&set).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	origins := map[uint]*uint{}
	if err == nil {
		rows, err := chapterRows(tx, set.ID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			origins[row.ID] = row.OriginJobID
		}
	}
	for _, item := range chapters {
		if item.ID != 0 {
			if _, ok := origins[item.ID]; !ok {
				return nil, ErrChapterInvalid
			}
		}
	}
	title := video.DisplayTitle
	if title == "" {
		title = video.Name
	}
	if set.ID == 0 {
		if expected != 0 {
			return nil, ErrChapterConflict
		}
		set = models.ChapterSet{VideoID: video.ID, TitleSnapshot: title, SourceSize: size, SourceModTimeNS: mtime, Revision: 1}
		created := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "video_id"}}, DoNothing: true}).Create(&set)
		if created.Error != nil {
			return nil, created.Error
		}
		if created.RowsAffected != 1 {
			return nil, ErrChapterConflict
		}
	} else {
		if expected == 0 || expected >= experienceMaxSafeInteger {
			return nil, ErrChapterConflict
		}
		updated := tx.Model(&models.ChapterSet{}).Where("id = ? AND revision = ?", set.ID, expected).Updates(map[string]any{"revision": gorm.Expr("revision + 1"), "title_snapshot": title, "source_size": size, "source_mod_time_ns": mtime})
		if updated.Error != nil {
			return nil, updated.Error
		}
		if updated.RowsAffected != 1 {
			return nil, ErrChapterConflict
		}
		set.Revision = expected + 1
		set.SourceSize = size
		set.SourceModTimeNS = mtime
	}
	if err := replaceChapterRows(tx, set.ID, chapters, origins, origin); err != nil {
		return nil, err
	}
	rows, err := chapterRows(tx, set.ID)
	if err != nil {
		return nil, err
	}
	return manualChapterView(set, rows, videoSourceVersion(video.ID, size, mtime)), nil
}
func (s *ChapterService) SaveVideoChapters(ctx context.Context, request SaveChaptersRequest) (*ChapterSetView, error) {
	if request.VideoID == 0 || request.VideoID > experienceMaxSafeInteger || request.ExpectedRevision >= experienceMaxSafeInteger || len(request.Chapters) == 0 && !request.Clear || len(request.Chapters) > 0 && request.Clear {
		return nil, ErrChapterInvalid
	}
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var result *ChapterSetView
	err = database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		tx = tx.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		video, info, err := bookmarkSourceInScope(tx, request.VideoID)
		if err != nil {
			return err
		}
		if request.SourceVersion != videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano()) {
			return ErrBookmarkSourceChanged
		}
		chapters, err := normalizeChapters(request.Chapters, chapterDuration(video))
		if err != nil {
			return err
		}
		result, err = saveChapterSetTx(tx, video, info.Size(), info.ModTime().UnixNano(), request.ExpectedRevision, chapters, nil)
		return err
	})
	return result, err
}
func (s *ChapterService) ResolveChapter(ctx context.Context, request ResolveChapterRequest) (*ChapterResolution, error) {
	parts := strings.Split(request.SetID, ":")
	if len(parts) != 2 || request.ChapterID == 0 || request.ChapterID > experienceMaxSafeInteger || request.Revision == 0 || request.Revision > experienceMaxSafeInteger {
		return nil, ErrChapterInvalid
	}
	id, err := strconv.ParseUint(parts[1], 10, 53)
	if err != nil || id == 0 {
		return nil, ErrChapterInvalid
	}
	videoID := uint(id)
	if parts[0] == "manual" {
		err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
			var set models.ChapterSet
			if err := db.First(&set, id).Error; err != nil {
				return err
			}
			videoID = set.VideoID
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else if parts[0] != "embedded" {
		return nil, ErrChapterInvalid
	}
	sets, err := s.GetVideoChapterSets(ctx, videoID)
	if err != nil {
		return nil, err
	}
	set := sets.Embedded
	if parts[0] == "manual" {
		set = sets.Manual
	}
	if set == nil || set.SetID != request.SetID || set.Revision != request.Revision {
		return nil, ErrChapterConflict
	}
	if set.SourceStatus == "unavailable" {
		return nil, ErrBookmarkSourceUnavailable
	}
	if set.SourceStatus != "ready" {
		return nil, ErrBookmarkSourceChanged
	}
	for _, chapter := range set.Chapters {
		if chapter.ID == request.ChapterID {
			if sets.DurationMS <= 0 {
				return nil, ErrChapterDurationUnknown
			}
			if chapter.StartMS < 0 || chapter.EndMS <= chapter.StartMS || chapter.EndMS > sets.DurationMS {
				return nil, ErrChapterInvalid
			}
			return &ChapterResolution{VideoID: videoID, StartMS: chapter.StartMS, SourceVersion: set.SourceVersion}, nil
		}
	}
	return nil, ErrChapterConflict
}

type ChapterSetQuery struct {
	Keyword  string `json:"keyword"`
	CursorID uint   `json:"cursor_id"`
	Limit    int    `json:"limit"`
}
type ChapterSetPage struct {
	Items      []models.ChapterSet `json:"items"`
	NextCursor uint                `json:"next_cursor"`
}

func (s *ChapterService) ListChapterSets(ctx context.Context, request ChapterSetQuery) (*ChapterSetPage, error) {
	if request.CursorID > experienceMaxSafeInteger || utf8.RuneCountInString(request.Keyword) > 200 {
		return nil, ErrChapterInvalid
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	result := &ChapterSetPage{Items: []models.ChapterSet{}}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		query := db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		if request.CursorID != 0 {
			query = query.Where("id < ?", request.CursorID)
		}
		if keyword := strings.TrimSpace(request.Keyword); keyword != "" {
			query = query.Where(`LOWER(title_snapshot) LIKE ? ESCAPE '\'`, "%"+strings.ToLower(escapeSQLLike(keyword))+"%")
		}
		if err := query.Order("id DESC").Limit(limit + 1).Find(&result.Items).Error; err != nil {
			return err
		}
		if len(result.Items) > limit {
			result.Items = result.Items[:limit]
			result.NextCursor = result.Items[len(result.Items)-1].ID
		}
		return nil
	})
	return result, err
}
