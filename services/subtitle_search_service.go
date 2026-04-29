package services

import (
	"os"
	"path/filepath"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SubtitleSearchMatch struct {
	Video   models.Video           `json:"video"`
	Segment subtitleparser.Segment `json:"segment"`
}

type SubtitleSearchService struct{}

func (s *SubtitleSearchService) SearchSubtitleMatches(keyword string, limit int) ([]SubtitleSearchMatch, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return []SubtitleSearchMatch{}, nil
	}
	if limit <= 0 {
		limit = 20
	}

	matches, stale, err := s.searchIndexedSubtitleMatches(keyword, limit)
	if err != nil {
		return nil, err
	}
	if len(matches) >= limit && !stale {
		return matches, nil
	}
	if stale {
		matches, stale, err = s.searchIndexedSubtitleMatches(keyword, limit)
		if err != nil {
			return nil, err
		}
		if len(matches) >= limit && !stale {
			return matches, nil
		}
	}

	if err := syncSubtitleIndexesFromFilesystem(); err != nil {
		return nil, err
	}
	matches, _, err = s.searchIndexedSubtitleMatches(keyword, limit)
	return matches, err
}

func (s *SubtitleSearchService) searchIndexedSubtitleMatches(keyword string, limit int) ([]SubtitleSearchMatch, bool, error) {
	pattern := "%" + strings.ToLower(escapeSQLLike(keyword)) + "%"
	type firstHit struct {
		VideoID      uint
		SegmentIndex int
	}

	var hits []firstHit
	err := database.DB.Model(&models.SubtitleSegment{}).
		Select("video_id, MIN(segment_index) AS segment_index").
		Where("LOWER(text) LIKE ? ESCAPE '\\'", pattern).
		Group("video_id").
		Order("video_id desc").
		Limit(limit).
		Scan(&hits).Error
	if err != nil {
		return nil, false, err
	}
	if len(hits) == 0 {
		return []SubtitleSearchMatch{}, false, nil
	}

	videoIDs := make([]uint, 0, len(hits))
	for _, hit := range hits {
		videoIDs = append(videoIDs, hit.VideoID)
	}

	var videos []models.Video
	if err := database.DB.Preload("Tags").Where("id IN ?", videoIDs).Find(&videos).Error; err != nil {
		return nil, false, err
	}
	videosByID := make(map[uint]models.Video, len(videos))
	for _, video := range videos {
		videosByID[video.ID] = video
	}

	matches := make([]SubtitleSearchMatch, 0, len(hits))
	staleIndex := false
	for _, hit := range hits {
		video, ok := videosByID[hit.VideoID]
		if !ok {
			staleIndex = true
			_ = deleteSubtitleIndex(hit.VideoID)
			continue
		}
		current, err := isSubtitleIndexCurrent(video, subtitleparser.SRTPathForVideo(video.Path))
		if err != nil || !current {
			staleIndex = true
			_ = ensureSubtitleIndexForVideo(video)
			continue
		}

		var indexed models.SubtitleSegment
		if err := database.DB.
			Where("video_id = ? AND segment_index = ?", hit.VideoID, hit.SegmentIndex).
			First(&indexed).Error; err != nil {
			staleIndex = true
			continue
		}
		matches = append(matches, SubtitleSearchMatch{
			Video: video,
			Segment: subtitleparser.Segment{
				Index:       indexed.SegmentIndex,
				StartTimeMs: indexed.StartTimeMs,
				EndTimeMs:   indexed.EndTimeMs,
				Text:        indexed.Text,
				Lines:       splitSubtitleLines(indexed.Text),
			},
		})
	}

	return matches, staleIndex, nil
}

func syncSubtitleIndexesFromFilesystem() error {
	var videos []models.Video
	if err := database.DB.Order("id desc").Find(&videos).Error; err != nil {
		return err
	}
	for _, video := range videos {
		_ = ensureSubtitleIndexForVideo(video)
	}
	return nil
}

func ensureSubtitleIndexForVideo(video models.Video) error {
	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	if _, err := os.Stat(srtPath); err != nil {
		if os.IsNotExist(err) {
			if err := deleteSubtitleSegments(video.ID); err != nil {
				return err
			}
			return upsertSubtitleIndexState(video.ID, srtPath, 0, 0, 0)
		}
		return err
	}

	current, err := isSubtitleIndexCurrent(video, srtPath)
	if err == nil && current {
		return nil
	}

	segments, err := subtitleparser.ParseFile(srtPath)
	if err != nil {
		return err
	}
	return replaceSubtitleIndex(video, srtPath, segments)
}

func indexSubtitleFileForVideoID(videoID uint, srtPath string) error {
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return err
	}
	if srtPath == "" {
		srtPath = subtitleparser.SRTPathForVideo(video.Path)
	}
	srtPath = filepath.Clean(srtPath)
	segments, err := subtitleparser.ParseFile(srtPath)
	if err != nil {
		return err
	}
	return replaceSubtitleIndex(video, srtPath, segments)
}

func replaceSubtitleIndex(video models.Video, srtPath string, segments []subtitleparser.Segment) error {
	info, err := os.Stat(srtPath)
	if err != nil {
		return err
	}

	return database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("video_id = ?", video.ID).Delete(&models.SubtitleSegment{}).Error; err != nil {
			return err
		}
		if len(segments) == 0 {
			return nil
		}

		indexed := make([]models.SubtitleSegment, 0, len(segments))
		for idx, segment := range segments {
			text := strings.TrimSpace(segment.Text)
			if text == "" {
				continue
			}
			indexed = append(indexed, models.SubtitleSegment{
				VideoID:         video.ID,
				SegmentIndex:    idx + 1,
				StartTimeMs:     segment.StartTimeMs,
				EndTimeMs:       segment.EndTimeMs,
				Text:            text,
				SubtitlePath:    srtPath,
				SubtitleModTime: info.ModTime().UnixNano(),
			})
		}
		if len(indexed) == 0 {
			return upsertSubtitleIndexStateTx(tx, video.ID, srtPath, info.ModTime().UnixNano(), info.Size(), 0)
		}
		if err := tx.CreateInBatches(indexed, 500).Error; err != nil {
			return err
		}
		return upsertSubtitleIndexStateTx(tx, video.ID, srtPath, info.ModTime().UnixNano(), info.Size(), len(indexed))
	})
}

func deleteSubtitleIndex(videoID uint) error {
	return database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("video_id = ?", videoID).Delete(&models.SubtitleSegment{}).Error; err != nil {
			return err
		}
		return tx.Where("video_id = ?", videoID).Delete(&models.SubtitleIndexState{}).Error
	})
}

func deleteSubtitleSegments(videoID uint) error {
	return database.DB.Where("video_id = ?", videoID).Delete(&models.SubtitleSegment{}).Error
}

func isSubtitleIndexCurrent(video models.Video, srtPath string) (bool, error) {
	info, err := os.Stat(srtPath)
	if err != nil {
		return false, err
	}

	var state models.SubtitleIndexState
	err = database.DB.
		Where("video_id = ?", video.ID).
		First(&state).Error
	if err != nil {
		return false, err
	}

	return filepath.Clean(state.SubtitlePath) == filepath.Clean(srtPath) &&
		state.SubtitleModTime == info.ModTime().UnixNano() &&
		state.SubtitleSize == info.Size(), nil
}

func upsertSubtitleIndexState(videoID uint, srtPath string, modTime int64, size int64, segmentCount int) error {
	return upsertSubtitleIndexStateTx(database.DB, videoID, srtPath, modTime, size, segmentCount)
}

func upsertSubtitleIndexStateTx(tx *gorm.DB, videoID uint, srtPath string, modTime int64, size int64, segmentCount int) error {
	now := time.Now()
	state := models.SubtitleIndexState{
		VideoID:         videoID,
		SubtitlePath:    filepath.Clean(srtPath),
		SubtitleModTime: modTime,
		SubtitleSize:    size,
		SegmentCount:    segmentCount,
		LastCheckedAt:   now,
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "video_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"subtitle_path":     state.SubtitlePath,
			"subtitle_mod_time": state.SubtitleModTime,
			"subtitle_size":     state.SubtitleSize,
			"segment_count":     state.SegmentCount,
			"last_checked_at":   state.LastCheckedAt,
			"updated_at":        now,
		}),
	}).Create(&state).Error
}

func splitSubtitleLines(text string) []string {
	lines := strings.Split(text, "\n")
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			clean = append(clean, line)
		}
	}
	if len(clean) == 0 && strings.TrimSpace(text) != "" {
		return []string{strings.TrimSpace(text)}
	}
	return clean
}
