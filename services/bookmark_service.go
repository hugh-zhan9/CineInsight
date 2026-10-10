package services

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"gorm.io/gorm"
	"math"
	"os"
	"strings"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"
)

type BookmarkService struct{}
type BookmarkInput struct {
	ID                uint     `json:"id"`
	Revision          uint64   `json:"revision"`
	VideoID           uint     `json:"video_id"`
	StartMS           int64    `json:"start_ms"`
	EndMS             *int64   `json:"end_ms"`
	Title             string   `json:"title"`
	Note              string   `json:"note"`
	Tags              []string `json:"tags"`
	SourceToken       string   `json:"source_token"`
	AcceptSourceToken string   `json:"accept_source_token"`
}
type BookmarkQuery struct {
	VideoID  uint   `json:"video_id"`
	Keyword  string `json:"keyword"`
	CursorID uint   `json:"cursor_id"`
	Limit    int    `json:"limit"`
}
type BookmarkDTO struct {
	models.VideoBookmark
	Tags            []string `json:"tags"`
	SourceAvailable bool     `json:"source_available"`
}
type BookmarkPage struct {
	Items    []BookmarkDTO `json:"items"`
	HasMore  bool          `json:"has_more"`
	CursorID uint          `json:"cursor_id"`
}
type BookmarkResolution struct {
	Status      string        `json:"status"`
	Bookmark    BookmarkDTO   `json:"bookmark"`
	Video       *models.Video `json:"video"`
	SourceToken string        `json:"source_token"`
}

func bookmarkTags(tags []string) ([]string, error) {
	result := make([]string, 0, len(tags))
	seen := map[string]bool{}
	if len(tags) > 20 {
		return nil, ErrViewingNoteInvalid
	}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if !utf8.ValidString(tag) || utf8.RuneCountInString(tag) > 40 {
			return nil, ErrViewingNoteInvalid
		}
		if !seen[tag] {
			result = append(result, tag)
			seen[tag] = true
		}
	}
	return result, nil
}
func bookmarkDTO(row models.VideoBookmark) (BookmarkDTO, error) {
	dto := BookmarkDTO{VideoBookmark: row, Tags: []string{}}
	if err := json.Unmarshal([]byte(row.TagsJSON), &dto.Tags); err != nil {
		return dto, err
	}
	if dto.Tags == nil {
		dto.Tags = []string{}
	}
	return dto, nil
}
func validBookmarkPosition(start int64, end *int64) bool {
	return start >= 0 && start <= 9007199254740991 && (end == nil || *end > start && *end <= 9007199254740991)
}

func bookmarkSource(db *gorm.DB, id uint) (models.Video, os.FileInfo, error) {
	var video models.Video
	if err := db.First(&video, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return video, nil, ErrBookmarkSourceUnavailable
		}
		return video, nil, err
	}
	if video.IsStale {
		return video, nil, ErrBookmarkSourceUnavailable
	}
	info, err := os.Stat(video.Path)
	if err != nil || !info.Mode().IsRegular() {
		return video, nil, ErrBookmarkSourceUnavailable
	}
	// Open verifies readability without consuming the media or hashing its bytes.
	file, err := os.Open(video.Path)
	if err != nil {
		return video, nil, ErrBookmarkSourceUnavailable
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		return video, nil, ErrBookmarkSourceChanged
	}
	return video, actual, nil
}
func checkBookmarkDuration(db *gorm.DB, video models.Video, size, mtime int64, start int64, end *int64) error {
	var metadata models.VideoTechnicalMetadata
	err := db.First(&metadata, "video_id = ?", video.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if metadata.ProbedAt == nil || metadata.SuccessfulSourceSize == nil || metadata.SuccessfulSourceModTimeNS == nil || *metadata.SuccessfulSourceSize != size || *metadata.SuccessfulSourceModTimeNS != mtime || video.Duration <= 0 || math.IsNaN(video.Duration) || math.IsInf(video.Duration, 0) {
		return nil
	}
	duration := video.Duration * 1000
	if float64(start) >= duration || end != nil && float64(*end) > duration {
		return ErrViewingNoteInvalid
	}
	return nil
}

func (s *BookmarkService) Save(ctx context.Context, in BookmarkInput) (*BookmarkDTO, error) {
	in.Title = strings.TrimSpace(in.Title)
	if !validNoteText(in.Title, in.Note) || !validBookmarkPosition(in.StartMS, in.EndMS) || in.VideoID == 0 {
		return nil, ErrViewingNoteInvalid
	}
	tags, err := bookmarkTags(in.Tags)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(tags)
	if in.ID == 0 && (in.SourceToken == "" || in.Revision != 0 || in.AcceptSourceToken != "") {
		return nil, ErrViewingNoteInvalid
	}
	// Match maintenance's path-before-database ordering. Ordinary text edits need
	// no filesystem access and retain the recorded source version.
	if in.ID == 0 || in.AcceptSourceToken != "" {
		unlock, err := rLockLibraryPaths()
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	var result BookmarkDTO
	err = database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		var row models.VideoBookmark
		if in.ID != 0 {
			if err := tx.First(&row, in.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrViewingNoteConflict
				}
				return err
			}
			if row.Revision != in.Revision || row.VideoID != in.VideoID {
				return ErrViewingNoteConflict
			}
		}
		if in.ID == 0 || in.AcceptSourceToken != "" {
			video, info, err := bookmarkSource(tx, in.VideoID)
			if err != nil {
				return err
			}
			token := in.SourceToken
			if in.ID != 0 {
				token = in.AcceptSourceToken
			}
			if token != videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano()) {
				return ErrBookmarkSourceChanged
			}
			if err := checkBookmarkDuration(tx, video, info.Size(), info.ModTime().UnixNano(), in.StartMS, in.EndMS); err != nil {
				return err
			}
			row.VideoTitle = diaryVideoTitle(video)
			row.SourceSize = info.Size()
			row.SourceModTimeNS = info.ModTime().UnixNano()
		} else {
			// A text edit may not stat the source. Enforce bounds only for metadata which
			// still represents the bookmark's recorded fingerprint.
			var video models.Video
			lookup := tx.First(&video, in.VideoID).Error
			if lookup != nil && !errors.Is(lookup, gorm.ErrRecordNotFound) {
				return lookup
			}
			if lookup == nil {
				if err := checkBookmarkDuration(tx, video, row.SourceSize, row.SourceModTimeNS, in.StartMS, in.EndMS); err != nil {
					return err
				}
			}
		}
		if in.ID == 0 {
			row.VideoID = in.VideoID
			row.StartMS = in.StartMS
			row.EndMS = in.EndMS
			row.Title = in.Title
			row.Note = in.Note
			row.TagsJSON = string(encoded)
			row.Revision = 1
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		} else {
			update := tx.Model(&models.VideoBookmark{}).Where("id = ? AND revision = ?", in.ID, in.Revision).Updates(map[string]any{
				"start_ms": in.StartMS, "end_ms": in.EndMS, "title": in.Title, "note": in.Note, "tags_json": string(encoded), "revision": gorm.Expr("revision + 1"),
				"video_title": row.VideoTitle, "source_size": row.SourceSize, "source_mod_time_ns": row.SourceModTimeNS,
			})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return ErrViewingNoteConflict
			}
			if err := tx.First(&row, in.ID).Error; err != nil {
				return err
			}
		}
		dto, err := bookmarkDTO(row)
		if err != nil {
			return err
		}
		available, err := noteAvailableVideos(tx, []uint{row.VideoID})
		dto.SourceAvailable = available[row.VideoID]
		result = dto
		return err
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
func (s *BookmarkService) Delete(ctx context.Context, id uint, revision uint64) error {
	if id == 0 || revision == 0 {
		return ErrViewingNoteInvalid
	}
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		result := db.Where("id = ? AND revision = ?", id, revision).Delete(&models.VideoBookmark{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrViewingNoteConflict
		}
		return nil
	})
}
func (s *BookmarkService) List(ctx context.Context, q BookmarkQuery) (*BookmarkPage, error) {
	limit, err := notePageLimit(q.Limit)
	if err != nil {
		return nil, err
	}
	page := &BookmarkPage{Items: []BookmarkDTO{}}
	lower := cases.Lower(language.Und)
	keyword := lower.String(trimLiteralKeyword(q.Keyword))
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		cursor := q.CursorID
		for len(page.Items) <= limit {
			query := db.Model(&models.VideoBookmark{})
			if q.VideoID != 0 {
				query = query.Where("video_id = ?", q.VideoID)
			}
			if cursor != 0 {
				query = query.Where("id < ?", cursor)
			}
			var rows []models.VideoBookmark
			if err := query.Order("id DESC").Limit(256).Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				dto, err := bookmarkDTO(row)
				if err != nil {
					return err
				}
				fields := []string{row.Title, row.Note, row.VideoTitle}
				fields = append(fields, dto.Tags...)
				matched := keyword == ""
				for _, field := range fields {
					if strings.Contains(lower.String(field), keyword) {
						matched = true
						break
					}
				}
				if matched {
					page.Items = append(page.Items, dto)
					if len(page.Items) > limit {
						break
					}
				}
			}
			if len(rows) < 256 || len(page.Items) > limit {
				break
			}
			cursor = rows[len(rows)-1].ID
		}
		if len(page.Items) > limit {
			page.HasMore = true
			page.Items = page.Items[:limit]
		}
		ids := make([]uint, 0, len(page.Items))
		for _, row := range page.Items {
			ids = append(ids, row.VideoID)
		}
		available, err := noteAvailableVideos(db, ids)
		if err != nil {
			return err
		}
		for i := range page.Items {
			page.Items[i].SourceAvailable = available[page.Items[i].VideoID]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(page.Items) > 0 {
		page.CursorID = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}
func (s *BookmarkService) Resolve(ctx context.Context, id uint) (*BookmarkResolution, error) {
	if id == 0 {
		return nil, ErrViewingNoteInvalid
	}
	unlock, err := rLockLibraryPaths()
	if err != nil {
		return nil, err
	}
	defer unlock()
	var result BookmarkResolution
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var row models.VideoBookmark
		if err := db.First(&row, id).Error; err != nil {
			return err
		}
		dto, err := bookmarkDTO(row)
		if err != nil {
			return err
		}
		result.Bookmark = dto
		result.Status = "unavailable"
		video, info, err := bookmarkSource(db, row.VideoID)
		if errors.Is(err, ErrBookmarkSourceUnavailable) {
			return nil
		}
		if err != nil {
			return err
		}
		result.Video = &video
		result.Bookmark.SourceAvailable = true
		result.SourceToken = videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano())
		result.Status = "ready"
		if row.SourceSize != info.Size() || row.SourceModTimeNS != info.ModTime().UnixNano() {
			result.Status = "source_changed"
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
