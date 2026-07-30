package services

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	TechnicalStateUnprobed = "unprobed"
	TechnicalStateCurrent  = "current"
	TechnicalStateStale    = "stale"
	TechnicalStateError    = "error"
)

type VideoDetailsUpdate struct {
	VideoID        uint     `json:"video_id"`
	DisplayTitle   string   `json:"display_title"`
	OriginalTitle  string   `json:"original_title"`
	PersonalRating *float64 `json:"personal_rating"`
	PersonIDs      []uint   `json:"person_ids"`
	CollectionIDs  []uint   `json:"collection_ids"`
}

type ExternalSubtitleDetails struct {
	Path             string `json:"path"`
	Language         string `json:"language"`
	SegmentCount     int    `json:"segment_count"`
	LastSegmentIndex int    `json:"last_segment_index"`
}

type TechnicalSnapshotStatus struct {
	State    string `json:"state"`
	IsStale  bool   `json:"is_stale"`
	HasError bool   `json:"has_error"`
}

type VideoDetails struct {
	Video             models.Video                   `json:"video"`
	EffectiveTitle    string                         `json:"effective_title"`
	People            []PersonListItem               `json:"people"`
	Collections       []CollectionListItem           `json:"collections"`
	TechnicalMetadata *models.VideoTechnicalMetadata `json:"technical_metadata"`
	Streams           []models.MediaStream           `json:"streams"`
	ExternalSubtitle  *ExternalSubtitleDetails       `json:"external_subtitle"`
	TechnicalStatus   TechnicalSnapshotStatus        `json:"technical_status"`
}

type VideoDetailService struct {
	people      *PersonService
	collections *CollectionService
}

func NewVideoDetailService(people *PersonService, collections *CollectionService) *VideoDetailService {
	return &VideoDetailService{people: people, collections: collections}
}

func (s *VideoDetailService) GetVideoDetails(videoID uint) (*VideoDetails, error) {
	var video models.Video
	if err := database.DB.Preload("Tags").First(&video, videoID).Error; err != nil {
		return nil, err
	}
	detail := &VideoDetails{Video: video, EffectiveTitle: strings.TrimSpace(video.DisplayTitle)}
	if detail.EffectiveTitle == "" {
		detail.EffectiveTitle = video.Name
	}

	var people []models.Person
	if err := database.DB.Model(&models.Person{}).
		Joins("JOIN video_people ON video_people.person_id = people.id").
		Where("video_people.video_id = ?", videoID).
		Order("LOWER(people.display_name) ASC").Order("people.id ASC").
		Find(&people).Error; err != nil {
		return nil, fmt.Errorf("load video people: %w", err)
	}
	personIDs := make([]uint, 0, len(people))
	for _, person := range people {
		personIDs = append(personIDs, person.ID)
	}
	personCounts, err := activeVideoCountsByPerson(personIDs)
	if err != nil {
		return nil, err
	}
	detail.People = make([]PersonListItem, 0, len(people))
	for _, person := range people {
		detail.People = append(detail.People, personListItemWithCount(person, personCounts[person.ID]))
	}

	var collections []models.MediaCollection
	if err := database.DB.Model(&models.MediaCollection{}).
		Joins("JOIN collection_videos ON collection_videos.collection_id = media_collections.id").
		Where("collection_videos.video_id = ?", videoID).
		Order("media_collections.normalized_name ASC").Order("media_collections.id ASC").
		Find(&collections).Error; err != nil {
		return nil, fmt.Errorf("load video collections: %w", err)
	}
	collectionIDs := make([]uint, 0, len(collections))
	for _, collection := range collections {
		collectionIDs = append(collectionIDs, collection.ID)
	}
	collectionCounts, err := activeVideoCountsByCollection(collectionIDs)
	if err != nil {
		return nil, err
	}
	detail.Collections = make([]CollectionListItem, 0, len(collections))
	for _, collection := range collections {
		detail.Collections = append(detail.Collections, collectionListItemWithCount(collection, collectionCounts[collection.ID]))
	}

	var metadata models.VideoTechnicalMetadata
	if err := database.DB.First(&metadata, "video_id = ?", videoID).Error; err == nil {
		detail.TechnicalMetadata = &metadata
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if detail.TechnicalMetadata != nil && detail.TechnicalMetadata.ProbedAt != nil {
		if err := database.DB.Where("video_id = ?", videoID).Order("stream_index ASC").Find(&detail.Streams).Error; err != nil {
			return nil, err
		}
	}
	detail.TechnicalStatus = technicalSnapshotStatus(video.Path, detail.TechnicalMetadata)

	var subtitle models.SubtitleIndexState
	if err := database.DB.Where("video_id = ?", videoID).First(&subtitle).Error; err == nil {
		lastIndex := -1
		if subtitle.SegmentCount > 0 {
			if err := database.DB.Model(&models.SubtitleSegment{}).Where("video_id = ?", videoID).Select("COALESCE(MAX(segment_index), -1)").Scan(&lastIndex).Error; err != nil {
				return nil, err
			}
		}
		detail.ExternalSubtitle = &ExternalSubtitleDetails{
			Path: subtitle.SubtitlePath, Language: externalSubtitleLanguage(subtitle.SubtitlePath),
			SegmentCount: subtitle.SegmentCount, LastSegmentIndex: lastIndex,
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return detail, nil
}

func technicalSnapshotStatus(videoPath string, metadata *models.VideoTechnicalMetadata) TechnicalSnapshotStatus {
	if metadata == nil {
		return TechnicalSnapshotStatus{State: TechnicalStateUnprobed}
	}
	status := TechnicalSnapshotStatus{HasError: metadata.LastError != ""}
	if metadata.ProbedAt == nil || metadata.SuccessfulSourceSize == nil || metadata.SuccessfulSourceModTimeNS == nil {
		if status.HasError {
			status.State = TechnicalStateError
		} else {
			status.State = TechnicalStateUnprobed
		}
		return status
	}
	info, err := os.Stat(videoPath)
	status.IsStale = err != nil || info.Size() != *metadata.SuccessfulSourceSize || info.ModTime().UnixNano() != *metadata.SuccessfulSourceModTimeNS
	if status.IsStale {
		status.State = TechnicalStateStale
	} else {
		status.State = TechnicalStateCurrent
	}
	return status
}

func externalSubtitleLanguage(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	parts := strings.Split(name, ".")
	if len(parts) < 2 {
		return "unknown"
	}
	candidate := strings.ToLower(parts[len(parts)-1])
	if len(candidate) == 2 || len(candidate) == 3 {
		return candidate
	}
	return "unknown"
}

func validateRatingValue(rating *float64) error {
	if rating == nil {
		return nil
	}
	if math.IsNaN(*rating) || math.IsInf(*rating, 0) || *rating < 0 || *rating > 10 {
		return errors.New("rating must be between 0 and 10")
	}
	doubled := *rating * 2
	if math.Abs(doubled-math.Round(doubled)) > 1e-9 {
		return errors.New("rating must use 0.5 increments")
	}
	return nil
}

func validateVideoDetailInput(input VideoDetailsUpdate) (VideoDetailsUpdate, error) {
	if input.VideoID == 0 {
		return VideoDetailsUpdate{}, errors.New("video ID is required")
	}
	input.DisplayTitle = strings.TrimSpace(input.DisplayTitle)
	input.OriginalTitle = strings.TrimSpace(input.OriginalTitle)
	if utf8.RuneCountInString(input.DisplayTitle) > 255 || utf8.RuneCountInString(input.OriginalTitle) > 255 {
		return VideoDetailsUpdate{}, errors.New("video title exceeds 255 characters")
	}
	if err := validateRatingValue(input.PersonalRating); err != nil {
		return VideoDetailsUpdate{}, err
	}
	input.PersonIDs = uniqueSortedIDs(input.PersonIDs)
	input.CollectionIDs = uniqueSortedIDs(input.CollectionIDs)
	return input, nil
}

func (s *VideoDetailService) UpdateVideoDetails(input VideoDetailsUpdate) (*VideoDetails, error) {
	input, err := validateVideoDetailInput(input)
	if err != nil {
		return nil, err
	}
	s.people.mu.Lock()
	defer s.people.mu.Unlock()
	s.collections.mu.Lock()
	defer s.collections.mu.Unlock()

	orphanAvatars := make([]string, 0)
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		var video models.Video
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&video, input.VideoID).Error; err != nil {
			return err
		}
		if err := validateDetailTargets(tx, input.PersonIDs, input.CollectionIDs); err != nil {
			return err
		}
		if err := tx.Model(&video).Updates(map[string]any{
			"display_title":   input.DisplayTitle,
			"original_title":  input.OriginalTitle,
			"personal_rating": input.PersonalRating,
		}).Error; err != nil {
			return err
		}
		avatars, err := replaceVideoPeopleInTransaction(tx, input.VideoID, input.PersonIDs)
		if err != nil {
			return err
		}
		orphanAvatars = append(orphanAvatars, avatars...)
		return replaceVideoCollectionsInTransaction(tx, input.VideoID, input.CollectionIDs)
	})
	if err != nil {
		return nil, fmt.Errorf("update video details: %w", err)
	}
	for _, avatar := range orphanAvatars {
		if err := s.people.images.Remove(avatar); err != nil {
			return nil, fmt.Errorf("video details updated but orphan avatar cleanup failed: %w", err)
		}
	}
	return s.GetVideoDetails(input.VideoID)
}

func validateDetailTargets(tx *gorm.DB, personIDs, collectionIDs []uint) error {
	if len(personIDs) > 0 {
		var people []models.Person
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", personIDs).Order("id ASC").Find(&people).Error; err != nil {
			return err
		}
		if len(people) != len(personIDs) {
			return errors.New("one or more people do not exist")
		}
	}
	if len(collectionIDs) > 0 {
		var collections []models.MediaCollection
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", collectionIDs).Order("id ASC").Find(&collections).Error; err != nil {
			return err
		}
		if len(collections) != len(collectionIDs) {
			return errors.New("one or more collections do not exist")
		}
	}
	return nil
}

func replaceVideoPeopleInTransaction(tx *gorm.DB, videoID uint, desired []uint) ([]string, error) {
	var relations []models.VideoPerson
	if err := tx.Where("video_id = ?", videoID).Find(&relations).Error; err != nil {
		return nil, err
	}
	oldIDs := make([]uint, 0, len(relations))
	for _, relation := range relations {
		oldIDs = append(oldIDs, relation.PersonID)
	}
	lockIDs := uniqueSortedIDs(append(append([]uint(nil), oldIDs...), desired...))
	if len(lockIDs) > 0 {
		var locked []models.Person
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", lockIDs).Order("id ASC").Find(&locked).Error; err != nil {
			return nil, err
		}
	}
	oldSet, desiredSet := idSet(oldIDs), idSet(desired)
	removed := make([]uint, 0)
	for _, personID := range oldIDs {
		if _, keep := desiredSet[personID]; keep {
			continue
		}
		if err := tx.Where("video_id = ? AND person_id = ?", videoID, personID).Delete(&models.VideoPerson{}).Error; err != nil {
			return nil, err
		}
		removed = append(removed, personID)
	}
	for _, personID := range desired {
		if _, exists := oldSet[personID]; exists {
			continue
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.VideoPerson{VideoID: videoID, PersonID: personID}).Error; err != nil {
			return nil, err
		}
	}
	orphanAvatars := make([]string, 0)
	for _, personID := range removed {
		var count int64
		if err := tx.Model(&models.VideoPerson{}).Where("person_id = ?", personID).Count(&count).Error; err != nil {
			return nil, err
		}
		if count != 0 {
			continue
		}
		var person models.Person
		if err := tx.First(&person, personID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		if err := tx.Delete(&person).Error; err != nil {
			return nil, err
		}
		if person.AvatarPath != "" {
			orphanAvatars = append(orphanAvatars, person.AvatarPath)
		}
	}
	return orphanAvatars, nil
}

func replaceVideoCollectionsInTransaction(tx *gorm.DB, videoID uint, desired []uint) error {
	var relations []models.CollectionVideo
	if err := tx.Where("video_id = ?", videoID).Find(&relations).Error; err != nil {
		return err
	}
	oldIDs := make([]uint, 0, len(relations))
	for _, relation := range relations {
		oldIDs = append(oldIDs, relation.CollectionID)
	}
	lockIDs := uniqueSortedIDs(append(append([]uint(nil), oldIDs...), desired...))
	if len(lockIDs) > 0 {
		var locked []models.MediaCollection
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", lockIDs).Order("id ASC").Find(&locked).Error; err != nil {
			return err
		}
	}
	oldSet, desiredSet := idSet(oldIDs), idSet(desired)
	for _, collectionID := range oldIDs {
		if _, keep := desiredSet[collectionID]; keep {
			continue
		}
		if err := tx.Where("collection_id = ? AND video_id = ?", collectionID, videoID).Delete(&models.CollectionVideo{}).Error; err != nil {
			return err
		}
		if err := compactCollectionPositions(tx, collectionID); err != nil {
			return err
		}
	}
	for _, collectionID := range desired {
		if _, exists := oldSet[collectionID]; exists {
			continue
		}
		var maxPosition int
		if err := tx.Model(&models.CollectionVideo{}).Where("collection_id = ?", collectionID).Select("COALESCE(MAX(position), 0)").Scan(&maxPosition).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.CollectionVideo{CollectionID: collectionID, VideoID: videoID, Position: maxPosition + 1}).Error; err != nil {
			return err
		}
	}
	return nil
}
