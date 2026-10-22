package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"sort"
	"time"
	"video-master/database"
	"video-master/models"
)

type ImageAnniversaryCursor struct {
	Signature string    `json:"signature"`
	TakenAt   time.Time `json:"taken_at" ts_type:"string"`
	ID        uint      `json:"id"`
}
type ImageAnniversaryRequest struct {
	Date      string                  `json:"date"`
	PersonIDs []uint                  `json:"person_ids"`
	Cursor    *ImageAnniversaryCursor `json:"cursor,omitempty"`
	Limit     int                     `json:"limit"`
}
type ImageAnniversaryPage struct {
	Items      []models.Image          `json:"items"`
	Total      int64                   `json:"total"`
	NextCursor *ImageAnniversaryCursor `json:"next_cursor,omitempty"`
}

// ListImageAnniversaries uses EXIF wall-clock days only; reading never records a view.
func (s *ImageLibraryService) ListImageAnniversaries(request ImageAnniversaryRequest) (*ImageAnniversaryPage, error) {
	return s.ListImageAnniversariesContext(context.Background(), request)
}
func (s *ImageLibraryService) ListImageAnniversariesContext(ctx context.Context, request ImageAnniversaryRequest) (*ImageAnniversaryPage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Date == "" {
		request.Date = time.Now().Format("2006-01-02")
	}
	day, err := time.ParseInLocation("2006-01-02", request.Date, time.Local)
	if err != nil {
		return nil, fmt.Errorf("anniversary_date_invalid: 日期无效")
	}
	if len(request.PersonIDs) > 200 {
		return nil, fmt.Errorf("anniversary_people_limit: 一次最多筛选 200 个人物")
	}
	ids := uniqueUintIDs(request.PersonIDs)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	encoded, _ := json.Marshal(struct {
		Date string
		IDs  []uint
	}{request.Date, ids})
	sum := sha256.Sum256(encoded)
	signature := hex.EncodeToString(sum[:])
	if request.Cursor != nil && (request.Cursor.Signature != signature || request.Cursor.ID == 0 || request.Cursor.TakenAt.IsZero()) {
		return nil, fmt.Errorf("anniversary_cursor_mismatch: 筛选已改变，请重新加载")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 60
	}
	if limit > 200 {
		limit = 200
	}
	page := &ImageAnniversaryPage{Items: []models.Image{}}
	err = database.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		base := func() *gorm.DB {
			q := applyImageVisibility(tx.Model(&models.Image{}), tx).Where("images.taken_at IS NOT NULL")
			if len(ids) > 0 {
				q = q.Where("images.id IN (?)", tx.Table("image_people").Select("image_id").Where("person_id IN ?", ids).Group("image_id").Having("COUNT(DISTINCT person_id) = ?", len(ids)))
			}
			return q
		}
		// Discover years only, allowing adjacent UTC years because EXIF is a
		// local wall clock. Evaluate bounded OR groups in the same snapshot.
		yearExpr := "CAST(strftime('%Y', images.taken_at) AS INTEGER)"
		if tx.Dialector.Name() == "postgres" {
			yearExpr = "CAST(EXTRACT(YEAR FROM images.taken_at) AS INTEGER)"
		}
		var observed []int
		if err := base().Distinct(yearExpr).Pluck(yearExpr, &observed).Error; err != nil {
			return err
		}
		yearSet := map[int]bool{}
		for _, year := range observed {
			for _, candidate := range []int{year - 1, year, year + 1} {
				if candidate > 0 && candidate < day.Year() {
					yearSet[candidate] = true
				}
			}
		}
		years := make([]int, 0, len(yearSet))
		for year := range yearSet {
			years = append(years, year)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(years)))
		for startIndex := 0; startIndex < len(years); startIndex += 200 {
			ranges := tx.Session(&gorm.Session{NewDB: true}).Where("1 = 0")
			for _, year := range years[startIndex:min(startIndex+200, len(years))] {
				start := time.Date(year, day.Month(), day.Day(), 0, 0, 0, 0, time.Local)
				if start.Month() != day.Month() || start.Day() != day.Day() {
					continue
				}
				ranges = ranges.Or("images.taken_at >= ? AND images.taken_at < ?", start, start.AddDate(0, 0, 1))
			}
			var count int64
			if err := base().Where(ranges).Count(&count).Error; err != nil {
				return err
			}
			page.Total += count
			if len(page.Items) >= limit+1 {
				continue
			}
			q := base().Where(ranges)
			if c := request.Cursor; c != nil {
				q = q.Where("images.taken_at < ? OR (images.taken_at = ? AND images.id < ?)", c.TakenAt, c.TakenAt, c.ID)
			}
			var rows []models.Image
			if err := q.Order("images.taken_at DESC").Order("images.id DESC").Limit(limit + 1 - len(page.Items)).Find(&rows).Error; err != nil {
				return err
			}
			page.Items = append(page.Items, rows...)
		}

		if len(page.Items) > limit {
			page.Items = page.Items[:limit]
			last := page.Items[len(page.Items)-1]
			page.NextCursor = &ImageAnniversaryCursor{Signature: signature, TakenAt: *last.TakenAt, ID: last.ID}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return page, err
}
