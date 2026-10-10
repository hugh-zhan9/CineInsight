package database

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
	"video-master/models"
)

// Backfill is restartable and retains deleted diary claims. Old mobile events
// did not consistently imply effective viewing, so they remain playback history.
func migrateViewingDiary(db *gorm.DB) error {
	var cursor uint
	for {
		var events []models.PlayEvent
		err := db.Where("id > ? AND source IN ?", cursor, []string{"inline_view", "jellyfin_view"}).
			Where("NOT EXISTS (SELECT 1 FROM viewing_diary_entries d WHERE d.source_play_event_id = play_events.id)").
			Order("id ASC").Limit(500).Find(&events).Error
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		err = db.Transaction(func(tx *gorm.DB) error {
			ids := make([]uint, 0, len(events))
			for _, event := range events {
				ids = append(ids, event.VideoID)
			}
			var videos []models.Video
			if err := tx.Unscoped().Select("id", "name", "display_title").Where("id IN ?", ids).Find(&videos).Error; err != nil {
				return err
			}
			byID := make(map[uint]models.Video, len(videos))
			for _, video := range videos {
				byID[video.ID] = video
			}
			rows := make([]models.ViewingDiaryEntry, 0, len(events))
			for _, event := range events {
				if event.PlayedAt.IsZero() {
					continue
				}
				video, ok := byID[event.VideoID]
				if !ok {
					continue
				}
				title := strings.TrimSpace(video.DisplayTitle)
				if title == "" {
					title = video.Name
				}
				at := event.PlayedAt.In(time.Local)
				if at.Year() < 1 || at.Year() > 9999 {
					continue
				}
				// Historical dates use today's configured zone, not a claimed original zone.
				rows = append(rows, models.ViewingDiaryEntry{VideoID: &event.VideoID, Title: title,
					WatchedOn: at.Format(time.DateOnly), RecordedAt: &event.PlayedAt, DateBasis: models.DiaryDateImported,
					Origin: models.DiaryOriginHistorical, Source: event.Source, SourcePlayEventID: &event.ID, Revision: 1})
			}
			if len(rows) == 0 {
				return nil
			}
			return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
		})
		if err != nil {
			return err
		}
		cursor = events[len(events)-1].ID
	}
}
