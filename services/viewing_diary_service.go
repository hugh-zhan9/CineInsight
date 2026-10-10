package services

import (
	"context"
	"errors"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"gorm.io/gorm"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"
)

type ViewingDiaryService struct{}
type DiaryInput struct {
	ID        uint     `json:"id"`
	Revision  uint64   `json:"revision"`
	VideoID   *uint    `json:"video_id"`
	Title     string   `json:"title"`
	WatchedOn string   `json:"watched_on"`
	Rating    *float64 `json:"rating"`
	Note      string   `json:"note"`
}
type DiaryQuery struct {
	Year       int    `json:"year"`
	Keyword    string `json:"keyword"`
	CursorDate string `json:"cursor_date"`
	CursorID   uint   `json:"cursor_id"`
	Limit      int    `json:"limit"`
}
type DiaryEntryDTO struct {
	models.ViewingDiaryEntry
	SourceAvailable bool `json:"source_available"`
}
type DiaryPage struct {
	Items      []DiaryEntryDTO `json:"items"`
	HasMore    bool            `json:"has_more"`
	CursorDate string          `json:"cursor_date"`
	CursorID   uint            `json:"cursor_id"`
}

func validDiaryDate(date string) bool {
	parsed, err := time.Parse(time.DateOnly, date)
	return err == nil && parsed.Year() >= 1 && parsed.Year() <= 9999 && parsed.Format(time.DateOnly) == date
}
func validDiaryRating(rating *float64) bool {
	return rating == nil || (!math.IsNaN(*rating) && !math.IsInf(*rating, 0) && *rating >= 0 && *rating <= 10 && math.Trunc(*rating*2) == *rating*2)
}
func sameVideoReference(a, b *uint) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (s *ViewingDiaryService) Save(ctx context.Context, in DiaryInput) (*DiaryEntryDTO, error) {
	in.Title = strings.TrimSpace(in.Title)
	if !validDiaryDate(in.WatchedOn) || !validDiaryRating(in.Rating) || !utf8.ValidString(in.Note) || utf8.RuneCountInString(in.Note) > 10000 {
		return nil, ErrViewingNoteInvalid
	}
	if in.VideoID != nil && *in.VideoID == 0 {
		return nil, ErrViewingNoteInvalid
	}
	var row models.ViewingDiaryEntry
	save := func(tx *gorm.DB) error {
		if in.ID == 0 {
			if in.Revision != 0 {
				return ErrViewingNoteInvalid
			}
			if in.VideoID != nil {
				var video models.Video
				if err := tx.First(&video, *in.VideoID).Error; err != nil {
					return err
				}
				// A selected library item snapshots its title, even when that existing
				// title is longer than the manual-entry limit.
				in.Title = diaryVideoTitle(video)
			} else if !validNoteText(in.Title, in.Note) {
				return ErrViewingNoteInvalid
			}
			row = models.ViewingDiaryEntry{VideoID: in.VideoID, Title: in.Title, WatchedOn: in.WatchedOn, DateBasis: models.DiaryDateManual, Origin: models.DiaryOriginManual, Rating: in.Rating, Note: in.Note, Revision: 1}
			return tx.Create(&row).Error
		}
		if err := tx.First(&row, in.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrViewingNoteConflict
			}
			return err
		}
		if in.Revision != row.Revision || !sameVideoReference(in.VideoID, row.VideoID) {
			return ErrViewingNoteConflict
		}
		if !validNoteText(in.Title, in.Note) && in.Title != row.Title {
			return ErrViewingNoteInvalid
		}
		basis := row.DateBasis
		if in.WatchedOn != row.WatchedOn {
			if row.Origin == models.DiaryOriginManual {
				basis = models.DiaryDateManual
			} else {
				basis = models.DiaryDateOverride
			}
		}
		result := tx.Model(&models.ViewingDiaryEntry{}).Where("id = ? AND revision = ?", in.ID, in.Revision).Updates(map[string]any{
			"title": in.Title, "watched_on": in.WatchedOn, "date_basis": basis, "rating": in.Rating, "note": in.Note, "revision": gorm.Expr("revision + 1"),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrViewingNoteConflict
		}
		return tx.First(&row, in.ID).Error
	}
	var dto DiaryEntryDTO
	err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		if err := save(tx); err != nil {
			return err
		}
		dto.ViewingDiaryEntry = row
		if row.VideoID == nil {
			return nil
		}
		available, err := noteAvailableVideos(tx, []uint{*row.VideoID})
		dto.SourceAvailable = available[*row.VideoID]
		return err
	})
	if err != nil {
		return nil, err
	}
	return &dto, nil
}

func (s *ViewingDiaryService) Delete(ctx context.Context, id uint, revision uint64) error {
	if id == 0 || revision == 0 {
		return ErrViewingNoteInvalid
	}
	return database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		var row models.ViewingDiaryEntry
		if err := tx.First(&row, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrViewingNoteConflict
			}
			return err
		}
		query := tx.Where("id = ? AND revision = ?", id, revision)
		var result *gorm.DB
		if row.SourcePlayEventID == nil && row.SourceSessionKey == nil {
			result = query.Unscoped().Delete(&models.ViewingDiaryEntry{})
		} else {
			result = query.Model(&models.ViewingDiaryEntry{}).Updates(map[string]any{
				"video_id": nil, "title": "", "watched_on": "", "recorded_at": nil, "date_basis": "", "utc_offset_seconds": nil,
				"rating": nil, "note": "", "deleted_at": time.Now(), "revision": gorm.Expr("revision + 1"),
			})
		}
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrViewingNoteConflict
		}
		return nil
	})
}

func diaryYearRange(year int) (string, string, error) {
	if year == 0 {
		year = time.Now().Year()
	}
	if year < 1 || year > 9999 {
		return "", "", ErrViewingNoteInvalid
	}
	// A string upper bound for year 9999 remains valid without manufacturing a date.
	return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), nil
}
func (s *ViewingDiaryService) List(ctx context.Context, q DiaryQuery) (*DiaryPage, error) {
	limit, err := notePageLimit(q.Limit)
	if err != nil {
		return nil, err
	}
	first, last, err := diaryYearRange(q.Year)
	if err != nil {
		return nil, err
	}
	if (q.CursorID == 0) != (q.CursorDate == "") || q.CursorID != 0 && !validDiaryDate(q.CursorDate) {
		return nil, ErrViewingNoteInvalid
	}
	page := &DiaryPage{Items: []DiaryEntryDTO{}}
	lower := cases.Lower(language.Und)
	keyword := lower.String(trimLiteralKeyword(q.Keyword))
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		date, id := q.CursorDate, q.CursorID
		for len(page.Items) <= limit {
			query := db.Where("watched_on >= ? AND watched_on <= ?", first, last)
			if id != 0 {
				query = query.Where("watched_on < ? OR (watched_on = ? AND id < ?)", date, date, id)
			}
			var rows []models.ViewingDiaryEntry
			if err := query.Order("watched_on DESC, id DESC").Limit(256).Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				if keyword == "" || strings.Contains(lower.String(row.Title), keyword) || strings.Contains(lower.String(row.Note), keyword) {
					page.Items = append(page.Items, DiaryEntryDTO{ViewingDiaryEntry: row})
					if len(page.Items) > limit {
						break
					}
				}
			}
			if len(rows) < 256 || len(page.Items) > limit {
				break
			}
			date, id = rows[len(rows)-1].WatchedOn, rows[len(rows)-1].ID
		}
		if len(page.Items) > limit {
			page.HasMore = true
			page.Items = page.Items[:limit]
		}
		ids := make([]uint, 0, len(page.Items))
		for _, row := range page.Items {
			if row.VideoID != nil {
				ids = append(ids, *row.VideoID)
			}
		}
		available, err := noteAvailableVideos(db, ids)
		if err != nil {
			return err
		}
		for i := range page.Items {
			if page.Items[i].VideoID != nil {
				page.Items[i].SourceAvailable = available[*page.Items[i].VideoID]
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(page.Items) > 0 {
		row := page.Items[len(page.Items)-1]
		page.CursorDate = row.WatchedOn
		page.CursorID = row.ID
	}
	return page, nil
}

type DiaryRepeat struct {
	VideoID *uint  `json:"video_id"`
	Title   string `json:"title"`
	Count   int    `json:"count"`
}
type ViewingYearReview struct {
	Year          int           `json:"year"`
	Total         int           `json:"total"`
	Automatic     int           `json:"automatic"`
	Historical    int           `json:"historical"`
	Manual        int           `json:"manual"`
	Days          int           `json:"days"`
	Months        []int         `json:"months"`
	RatedCount    int           `json:"rated_count"`
	AverageRating *float64      `json:"average_rating"`
	MostWatched   []DiaryRepeat `json:"most_watched"`
}

func (s *ViewingDiaryService) YearReview(ctx context.Context, year int) (*ViewingYearReview, error) {
	if year == 0 {
		year = time.Now().Year()
	}
	first, last, err := diaryYearRange(year)
	if err != nil {
		return nil, err
	}
	result := &ViewingYearReview{Year: year, Months: make([]int, 12), MostWatched: []DiaryRepeat{}}
	days := map[string]bool{}
	groups := map[string]*DiaryRepeat{}
	sum := 0.0
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var cursor uint
		for {
			var rows []models.ViewingDiaryEntry
			if err := db.Select("id", "video_id", "title", "watched_on", "origin", "rating").Where("id > ? AND watched_on >= ? AND watched_on <= ?", cursor, first, last).Order("id ASC").Limit(500).Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				result.Total++
				days[row.WatchedOn] = true
				switch row.Origin {
				case models.DiaryOriginAutomatic:
					result.Automatic++
				case models.DiaryOriginHistorical:
					result.Historical++
				case models.DiaryOriginManual:
					result.Manual++
				}
				if !validDiaryDate(row.WatchedOn) {
					return ErrViewingNoteInvalid
				}
				month, err := strconv.Atoi(row.WatchedOn[5:7])
				if err != nil || month < 1 || month > 12 {
					return ErrViewingNoteInvalid
				}
				result.Months[month-1]++
				if row.Rating != nil {
					result.RatedCount++
					sum += *row.Rating
				}
				key := "title:" + row.Title
				if row.VideoID != nil {
					key = "video:" + strconv.FormatUint(uint64(*row.VideoID), 10)
				}
				if groups[key] == nil {
					groups[key] = &DiaryRepeat{VideoID: row.VideoID, Title: row.Title}
				}
				groups[key].Count++
			}
			if len(rows) < 500 {
				return nil
			}
			cursor = rows[len(rows)-1].ID
		}
	})
	if err != nil {
		return nil, err
	}
	result.Days = len(days)
	if result.RatedCount > 0 {
		average := sum / float64(result.RatedCount)
		result.AverageRating = &average
	}
	for _, group := range groups {
		result.MostWatched = append(result.MostWatched, *group)
	}
	sort.Slice(result.MostWatched, func(i, j int) bool {
		a, b := result.MostWatched[i], result.MostWatched[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		if a.VideoID == nil {
			return b.VideoID != nil
		}
		if b.VideoID == nil {
			return false
		}
		return *a.VideoID < *b.VideoID
	})
	if len(result.MostWatched) > 10 {
		result.MostWatched = result.MostWatched[:10]
	}
	return result, nil
}
