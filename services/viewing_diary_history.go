package services

import (
	"context"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"gorm.io/gorm"
	"strings"
	"video-master/database"
	"video-master/models"
)

type HistoryQuery struct {
	Keyword  string `json:"keyword"`
	CursorID uint   `json:"cursor_id"`
	Limit    int    `json:"limit"`
}
type UnconfirmedPlayback struct {
	models.PlayEvent
	Title           string `json:"title"`
	SourceAvailable bool   `json:"source_available"`
}
type PlaybackHistoryPage struct {
	Items    []UnconfirmedPlayback `json:"items"`
	HasMore  bool                  `json:"has_more"`
	CursorID uint                  `json:"cursor_id"`
}

func (s *ViewingDiaryService) History(ctx context.Context, q HistoryQuery) (*PlaybackHistoryPage, error) {
	limit, err := notePageLimit(q.Limit)
	if err != nil {
		return nil, err
	}
	result := &PlaybackHistoryPage{Items: []UnconfirmedPlayback{}}
	lower := cases.Lower(language.Und)
	keyword := lower.String(trimLiteralKeyword(q.Keyword))
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		cursor := q.CursorID
		for len(result.Items) <= limit {
			query := db.Table("play_events p").Select("p.*, CASE WHEN v.display_title <> '' THEN v.display_title ELSE v.name END AS title, (v.deleted_at IS NULL AND NOT v.is_stale) AS source_available").
				Joins("JOIN videos v ON v.id = p.video_id").Where("NOT EXISTS (SELECT 1 FROM viewing_diary_entries d WHERE d.source_play_event_id = p.id)")
			if cursor != 0 {
				query = query.Where("p.id < ?", cursor)
			}
			var rows []UnconfirmedPlayback
			if err := query.Order("p.id DESC").Limit(256).Scan(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				if keyword == "" || strings.Contains(lower.String(row.Title), keyword) {
					result.Items = append(result.Items, row)
					if len(result.Items) > limit {
						break
					}
				}
			}
			if len(rows) < 256 || len(result.Items) > limit {
				break
			}
			cursor = rows[len(rows)-1].ID
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(result.Items) > limit {
		result.HasMore = true
		result.Items = result.Items[:limit]
	}
	if len(result.Items) > 0 {
		result.CursorID = result.Items[len(result.Items)-1].ID
	}
	return result, nil
}
