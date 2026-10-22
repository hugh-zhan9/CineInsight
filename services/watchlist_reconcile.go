package services

import (
	"gorm.io/gorm"
	"sort"
	"strconv"
	"video-master/models"
)

// watchlistMatchingWorks batches identity matching for reconciliation and reads.
// Only exact title hits enter representative lookups. Two distinct works already
// prove ambiguity, so repeated titles never accumulate an entries × videos matrix.
func watchlistMatchingWorks(db *gorm.DB, entries []models.WatchlistEntry) (map[uint]map[int64]uint, error) {
	result := map[uint]map[int64]uint{}
	entryKeys := map[uint]string{}
	hitsByKey := map[string]map[int64]uint{}
	yearSet := map[int]bool{}
	for _, entry := range entries {
		result[entry.ID] = map[int64]uint{}
		title := normalizeLibraryTitle(entry.Title)
		if entry.Year <= 0 || title == "" {
			continue
		}
		key := title + "|" + strconv.Itoa(entry.Year)
		entryKeys[entry.ID] = key
		yearSet[entry.Year] = true
		if hitsByKey[key] == nil {
			hitsByKey[key] = map[int64]uint{}
		}
	}
	years := make([]int, 0, len(yearSet))
	for year := range yearSet {
		years = append(years, year)
	}
	sort.Ints(years)
	for start := 0; start < len(years); start += 200 {
		var batch []models.Video
		err := db.Select("id", "name", "display_title", "original_title", "year").Where("year IN ?", years[start:min(start+200, len(years))]).FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			ids := []uint{}
			keysByVideo := map[uint][]string{}
			for _, video := range batch {
				if video.Year == nil {
					continue
				}
				seen := map[string]bool{}
				for _, field := range libraryMatchFields(video) {
					key := field + "|" + strconv.Itoa(*video.Year)
					hits, needed := hitsByKey[key]
					if !needed || len(hits) >= 2 || seen[key] {
						continue
					}
					seen[key] = true
					keysByVideo[video.ID] = append(keysByVideo[video.ID], key)
				}
				if len(keysByVideo[video.ID]) > 0 {
					ids = append(ids, video.ID)
				}
			}
			if len(ids) == 0 {
				return nil
			}
			groups, err := videoGroupsOf(db, ids)
			if err != nil {
				return err
			}
			works, err := loadPlayableWorks(db, ids)
			if err != nil {
				return err
			}
			for _, id := range ids {
				target := id
				if work, ok := works[id]; ok {
					target = work.representative.ID
				}
				for _, key := range keysByVideo[id] {
					hits := hitsByKey[key]
					if len(hits) < 2 {
						hits[workKeyOf(id, groups)] = target
					}
				}
			}
			return nil
		}).Error
		if err != nil {
			return nil, err
		}
	}
	for entryID, key := range entryKeys {
		result[entryID] = hitsByKey[key]
	}
	return result, nil
}
