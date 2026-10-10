package services

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errDiarySessionExists = errors.New("diary_session_exists")

func diarySessionKey(source string, videoID uint, sessionID string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s", source, videoID, sessionID))))
}

// Includes tombstones: deleting user text does not authorize replaying a fact.
func diarySessionRecorded(db *gorm.DB, key string) (bool, error) {
	var count int64
	err := db.Unscoped().Model(&models.ViewingDiaryEntry{}).Where("source_session_key = ?", key).Count(&count).Error
	return count > 0, err
}

func diaryVideoTitle(video models.Video) string {
	if title := strings.TrimSpace(video.DisplayTitle); title != "" {
		return title
	}
	return video.Name
}

// appendViewingDiaryTx is owned by the diary, called inside the playback fact's
// transaction. A lost unique-key claim must roll that whole transaction back.
func appendViewingDiaryTx(tx *gorm.DB, video models.Video, event models.PlayEvent, key string) error {
	if event.ID == 0 || event.PlayedAt.IsZero() || key == "" {
		return errors.New("invalid_diary_fact")
	}
	at := event.PlayedAt.In(time.Local)
	_, offset := at.Zone()
	row := models.ViewingDiaryEntry{
		VideoID: &video.ID, Title: diaryVideoTitle(video), WatchedOn: at.Format(time.DateOnly),
		RecordedAt: &event.PlayedAt, DateBasis: models.DiaryDateRecorded, UTCOffsetSeconds: &offset,
		Origin: models.DiaryOriginAutomatic, Source: event.Source, SourcePlayEventID: &event.ID,
		SourceSessionKey: &key, Revision: 1,
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errDiarySessionExists
	}
	return nil
}
