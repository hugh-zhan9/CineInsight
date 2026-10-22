package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"video-master/database"
	"video-master/models"
)

var ErrCollectionEpisodeConflict = errors.New("collection_episode_conflict: 季集资料已变化，请刷新")

type CollectionEpisodeEdit struct {
	VideoID          uint  `json:"video_id"`
	Season           *int  `json:"season"`
	Episode          *int  `json:"episode"`
	Excluded         bool  `json:"excluded"`
	ExpectedRevision int64 `json:"expected_revision"`
}
type CollectionEpisodeMember struct {
	VideoID        uint   `json:"video_id"`
	Name           string `json:"name"`
	Season         *int   `json:"season"`
	Episode        *int   `json:"episode"`
	Excluded       bool   `json:"excluded"`
	Revision       int64  `json:"revision"`
	Offline        bool   `json:"offline"`
	Issue          string `json:"issue"`
	VersionGroupID uint   `json:"version_group_id"`
}
type EpisodeMissingRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
type CollectionSeasonReport struct {
	Season            int                   `json:"season"`
	Recognized        int                   `json:"recognized"`
	Playable          int                   `json:"playable"`
	Offline           int                   `json:"offline"`
	DuplicateEpisodes int                   `json:"duplicate_episodes"`
	ExpectedCount     *int                  `json:"expected_count"`
	Revision          int64                 `json:"revision"`
	MissingRanges     []EpisodeMissingRange `json:"missing_ranges"`
	RangeConflicts    int                   `json:"range_conflicts"`
}
type CollectionEpisodeReport struct {
	Seasons     []CollectionSeasonReport  `json:"seasons"`
	Members     []CollectionEpisodeMember `json:"members"`
	Total       int                       `json:"total"`
	Uncertain   int                       `json:"uncertain"`
	NextCursor  uint                      `json:"next_cursor"`
	MixedSeries bool                      `json:"mixed_series"`
}

// SetEpisodeAssignments validates all revisions before changing any member.
func (s *CollectionService) SetEpisodeAssignments(collectionID uint, edits []CollectionEpisodeEdit) error {
	return s.SetEpisodeAssignmentsContext(context.Background(), collectionID, edits)
}
func (s *CollectionService) SetEpisodeAssignmentsContext(ctx context.Context, collectionID uint, edits []CollectionEpisodeEdit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(edits) == 0 || len(edits) > 200 {
		return fmt.Errorf("collection_episode_selection: 请选择 1 至 200 项")
	}
	seen := map[uint]bool{}
	for _, edit := range edits {
		if edit.VideoID == 0 || seen[edit.VideoID] || edit.ExpectedRevision < 0 || edit.Season != nil && (*edit.Season < 0 || *edit.Season > 999) || edit.Episode != nil && (*edit.Episode < 1 || *edit.Episode > 1000000) {
			return fmt.Errorf("collection_episode_invalid: 季集编号无效")
		}
		seen[edit.VideoID] = true
	}
	return database.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var collection models.MediaCollection
		if err := tx.First(&collection, collectionID).Error; err != nil {
			return err
		}
		for _, edit := range edits {
			var count int64
			if err := tx.Model(&models.CollectionVideo{}).Where("collection_id = ? AND video_id = ?", collectionID, edit.VideoID).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("collection_member_missing")
			}
			var row models.CollectionEpisodeAssignment
			err := tx.Where("collection_id = ? AND video_id = ?", collectionID, edit.VideoID).First(&row).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if edit.ExpectedRevision != 0 {
					return ErrCollectionEpisodeConflict
				}
			} else if err != nil {
				return err
			} else if row.Revision != edit.ExpectedRevision {
				return ErrCollectionEpisodeConflict
			}
		}
		for _, edit := range edits {
			var r *gorm.DB
			if edit.ExpectedRevision == 0 {
				r = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.CollectionEpisodeAssignment{CollectionID: collectionID, VideoID: edit.VideoID, Season: edit.Season, Episode: edit.Episode, Excluded: edit.Excluded, Revision: 1})
			} else {
				r = tx.Model(&models.CollectionEpisodeAssignment{}).Where("collection_id = ? AND video_id = ? AND revision = ?", collectionID, edit.VideoID, edit.ExpectedRevision).Updates(map[string]any{"season": edit.Season, "episode": edit.Episode, "excluded": edit.Excluded, "revision": edit.ExpectedRevision + 1})
			}
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				return ErrCollectionEpisodeConflict
			}
		}
		return nil
	})
}
func (s *CollectionService) SetSeasonExpectation(collectionID uint, season, count int, expectedRevision int64) error {
	return s.SetSeasonExpectationContext(context.Background(), collectionID, season, count, expectedRevision)
}
func (s *CollectionService) SetSeasonExpectationContext(ctx context.Context, collectionID uint, season, count int, expectedRevision int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if season < 0 || season > 999 || count < 1 || count > 1000000 || expectedRevision < 0 {
		return fmt.Errorf("collection_episode_invalid: 季或总集数无效")
	}
	return database.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var collection models.MediaCollection
		if err := tx.First(&collection, collectionID).Error; err != nil {
			return err
		}
		var r *gorm.DB
		if expectedRevision == 0 {
			r = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.CollectionSeasonExpectation{CollectionID: collectionID, Season: season, EpisodeCount: count, Revision: 1})
		} else {
			r = tx.Model(&models.CollectionSeasonExpectation{}).Where("collection_id = ? AND season = ? AND revision = ?", collectionID, season, expectedRevision).Updates(map[string]any{"episode_count": count, "revision": expectedRevision + 1})
		}
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrCollectionEpisodeConflict
		}
		return nil
	})
}

var collectionMultiEpisodeRe = regexp.MustCompile(`(?i)(?:E\d+[._\s]*(?:-|\+|&|E|~|至)[._\s]*(?:E)?\d+)|(?:第\s*\d+\s*[-~至]\s*\d+\s*[集话話])`)

func parseCollectionEpisode(video models.Video) ParsedEpisode {
	stem := strings.TrimSuffix(video.Name, filepath.Ext(video.Name))
	if collectionMultiEpisodeRe.MatchString(stem) || len(suggestionSeasonEpisodeRe.FindAllStringIndex(stem, -1)) > 1 {
		return ParsedEpisode{}
	}
	parsed := ParseEpisodeFromFileName(stem)
	if parsed.OK && parsed.Season == nil {
		folder := filepath.Base(video.Directory)
		if match := folderSuggestionSeasonMarkRe.FindStringSubmatch(folder); match != nil {
			for _, value := range match[1:] {
				if value != "" {
					if number, ok := parseFolderNumber(value); ok {
						parsed.Season = &number
					}
					break
				}
			}
		}
	}
	return parsed
}

// GetEpisodeReport separates offline membership from genuinely absent numbering.
func (s *CollectionService) GetEpisodeReport(collectionID, cursor uint, limit int) (*CollectionEpisodeReport, error) {
	return s.GetEpisodeReportContext(context.Background(), collectionID, cursor, limit)
}
func (s *CollectionService) GetEpisodeReportContext(ctx context.Context, collectionID, cursor uint, limit int) (*CollectionEpisodeReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	report := &CollectionEpisodeReport{Seasons: []CollectionSeasonReport{}, Members: []CollectionEpisodeMember{}}
	err := database.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var collection models.MediaCollection
		if err := tx.First(&collection, collectionID).Error; err != nil {
			return err
		}
		var videos []models.Video
		// Collection membership is the access boundary, as in GetCollectionDetail;
		// exclude soft-deleted media but retain offline members for the report.
		q := tx.Model(&models.Video{}).Joins("JOIN collection_videos cv ON cv.video_id = videos.id").Where("cv.collection_id = ?", collectionID)
		scoped, err := applyScanRootScope(q)
		if err != nil {
			return err
		}
		q = scoped

		if err := q.Order("videos.id ASC").Find(&videos).Error; err != nil {
			return err
		}
		var assignments []models.CollectionEpisodeAssignment
		if err := tx.Where("collection_id = ?", collectionID).Find(&assignments).Error; err != nil {
			return err
		}
		overrides := map[uint]models.CollectionEpisodeAssignment{}
		for _, row := range assignments {
			overrides[row.VideoID] = row
		}
		ids := []uint{}
		series := map[string]bool{}
		parsed := map[uint]ParsedEpisode{}
		for _, video := range videos {
			if err := ctx.Err(); err != nil {
				return err
			}
			ids = append(ids, video.ID)
			p := parseCollectionEpisode(video)
			parsed[video.ID] = p
			if _, manual := overrides[video.ID]; !manual && p.OK {
				series[p.NormalizedSeries] = true
			}
		}
		report.MixedSeries = len(series) > 1
		groups, err := videoGroupsOf(tx, ids)
		if err != nil {
			return err
		}
		seasons := map[int]*CollectionSeasonReport{}
		episodes := map[int]map[int][]CollectionEpisodeMember{}
		all := []CollectionEpisodeMember{}
		for _, video := range videos {
			if err := ctx.Err(); err != nil {
				return err
			}
			p := parsed[video.ID]
			m := CollectionEpisodeMember{VideoID: video.ID, Name: video.Name, Season: p.Season, Episode: p.Episode, Offline: video.IsStale, VersionGroupID: groups[video.ID]}
			if row, ok := overrides[video.ID]; ok {
				m.Season = row.Season
				m.Episode = row.Episode
				m.Excluded = row.Excluded
				m.Revision = row.Revision
			} else if !p.OK || report.MixedSeries {
				m.Issue = "需确认系列或多集文件"
				m.Season = nil
				m.Episode = nil
			}
			if !m.Excluded {
				if m.Season == nil || m.Episode == nil {
					report.Uncertain++
					if m.Issue == "" {
						m.Issue = "季或集号未知"
					}
				} else {
					season := *m.Season
					if seasons[season] == nil {
						seasons[season] = &CollectionSeasonReport{Season: season, MissingRanges: []EpisodeMissingRange{}}
						episodes[season] = map[int][]CollectionEpisodeMember{}
					}
					episodes[season][*m.Episode] = append(episodes[season][*m.Episode], m)
				}
			}
			all = append(all, m)
		}
		var expectations []models.CollectionSeasonExpectation
		if err := tx.Where("collection_id = ?", collectionID).Find(&expectations).Error; err != nil {
			return err
		}
		for _, row := range expectations {
			if seasons[row.Season] == nil {
				seasons[row.Season] = &CollectionSeasonReport{Season: row.Season, MissingRanges: []EpisodeMissingRange{}}
				episodes[row.Season] = map[int][]CollectionEpisodeMember{}
			}
			summary := seasons[row.Season]
			n := row.EpisodeCount
			summary.ExpectedCount = &n
			summary.Revision = row.Revision
		}
		for season, summary := range seasons {
			numbers := []int{}
			for episode, members := range episodes[season] {
				numbers = append(numbers, episode)
				summary.Recognized++
				online := false
				for _, m := range members {
					if !m.Offline {
						online = true
					}
				}
				if online {
					summary.Playable++
				} else {
					summary.Offline++
				}
				if len(members) > 1 {
					summary.DuplicateEpisodes++
				}
				if summary.ExpectedCount != nil && episode > *summary.ExpectedCount {
					summary.RangeConflicts++
				}
			}
			sort.Ints(numbers)
			low, high := 0, 0
			if len(numbers) > 0 {
				low = numbers[0]
				high = numbers[len(numbers)-1]
			}
			if summary.ExpectedCount != nil {
				low = 1
				high = *summary.ExpectedCount
			}
			next := low
			for _, n := range numbers {
				if n < low || n > high {
					continue
				}
				if n > next {
					summary.MissingRanges = append(summary.MissingRanges, EpisodeMissingRange{next, n - 1})
				}
				next = n + 1
			}
			if low > 0 && next <= high {
				summary.MissingRanges = append(summary.MissingRanges, EpisodeMissingRange{next, high})
			}
			report.Seasons = append(report.Seasons, *summary)
		}
		sort.Slice(report.Seasons, func(i, j int) bool { return report.Seasons[i].Season < report.Seasons[j].Season })
		report.Total = len(all)
		for _, m := range all {
			if m.VideoID > cursor {
				report.Members = append(report.Members, m)
			}
		}
		if len(report.Members) > limit {
			report.Members = report.Members[:limit]
			report.NextCursor = report.Members[len(report.Members)-1].VideoID
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return report, err
}
