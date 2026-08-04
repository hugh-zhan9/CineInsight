package services

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrCollectionNameConflict  = errors.New("collection_name_conflict")
	ErrCollectionOrderConflict = errors.New("collection_order_conflict")
)

type CollectionListItem struct {
	Collection       models.MediaCollection `json:"collection"`
	CoverURL         string                 `json:"cover_url"`
	ActiveVideoCount int64                  `json:"active_video_count"`
	CursorName       string                 `json:"cursor_name"`
}

type CollectionVideoItem struct {
	Video    models.Video `json:"video"`
	Position int          `json:"position"`
}

type CollectionDetail struct {
	Collection CollectionListItem    `json:"collection"`
	Videos     []CollectionVideoItem `json:"videos"`
}

type CollectionService struct {
	images *ManagedImageService
	mu     sync.Mutex
}

func NewCollectionService(dataDir string) *CollectionService {
	return &CollectionService{images: NewManagedImageService(dataDir)}
}

func validateCollectionFields(name, description string) (string, string, string, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" {
		return "", "", "", errors.New("collection name is required")
	}
	if utf8.RuneCountInString(name) > 200 {
		return "", "", "", errors.New("collection name exceeds 200 characters")
	}
	if utf8.RuneCountInString(description) > 4000 {
		return "", "", "", errors.New("collection description exceeds 4000 characters")
	}
	return name, strings.ToLower(name), description, nil
}

func (s *CollectionService) CreateCollection(name, description string) (*models.MediaCollection, error) {
	name, normalizedName, description, err := validateCollectionFields(name, description)
	if err != nil {
		return nil, err
	}
	collection := models.MediaCollection{Name: name, NormalizedName: normalizedName, Description: description}
	if err := database.DB.Create(&collection).Error; err != nil {
		return nil, classifyCollectionWriteError(err)
	}
	return &collection, nil
}

func (s *CollectionService) UpdateCollection(id uint, name, description string) (*models.MediaCollection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name, normalizedName, description, err := validateCollectionFields(name, description)
	if err != nil {
		return nil, err
	}
	result := database.DB.Model(&models.MediaCollection{}).Where("id = ?", id).Updates(map[string]any{
		"name":            name,
		"normalized_name": normalizedName,
		"description":     description,
	})
	if result.Error != nil {
		return nil, classifyCollectionWriteError(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var collection models.MediaCollection
	if err := database.DB.First(&collection, id).Error; err != nil {
		return nil, err
	}
	return &collection, nil
}

func classifyCollectionWriteError(err error) error {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unique") && (strings.Contains(message, "normalized_name") || strings.Contains(message, "idx_media_collections_name_active")) {
		return fmt.Errorf("%w: %v", ErrCollectionNameConflict, err)
	}
	return fmt.Errorf("write collection: %w", err)
}

func (s *CollectionService) ListCollections(keyword, cursorName string, cursorID uint, limit int) ([]CollectionListItem, error) {
	limit = normalizeEntityPageLimit(limit)
	query := database.DB.Model(&models.MediaCollection{})
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		pattern := "%" + strings.ToLower(escapeSQLLike(keyword)) + "%"
		query = query.Where("(normalized_name LIKE ? ESCAPE '\\' OR LOWER(description) LIKE ? ESCAPE '\\')", pattern, pattern)
	}
	if cursorName = strings.ToLower(strings.TrimSpace(cursorName)); cursorName != "" || cursorID != 0 {
		query = query.Where("(normalized_name > ? OR (normalized_name = ? AND id > ?))", cursorName, cursorName, cursorID)
	}
	var collections []models.MediaCollection
	if err := query.Order("normalized_name ASC").Order("id ASC").Limit(limit).Find(&collections).Error; err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	collectionIDs := make([]uint, 0, len(collections))
	for _, collection := range collections {
		collectionIDs = append(collectionIDs, collection.ID)
	}
	counts, err := activeVideoCountsByCollection(collectionIDs)
	if err != nil {
		return nil, err
	}
	items := make([]CollectionListItem, 0, len(collections))
	for _, collection := range collections {
		items = append(items, collectionListItemWithCount(collection, counts[collection.ID]))
	}
	return items, nil
}

func (s *CollectionService) collectionListItem(collection models.MediaCollection) (CollectionListItem, error) {
	var count int64
	err := database.DB.Model(&models.Video{}).
		Joins("JOIN collection_videos ON collection_videos.video_id = videos.id").
		Where("collection_videos.collection_id = ?", collection.ID).
		Count(&count).Error
	if err != nil {
		return CollectionListItem{}, fmt.Errorf("count collection videos: %w", err)
	}
	return collectionListItemWithCount(collection, count), nil
}

func collectionListItemWithCount(collection models.MediaCollection, count int64) CollectionListItem {
	item := CollectionListItem{Collection: collection, ActiveVideoCount: count, CursorName: collection.NormalizedName}
	if collection.CoverPath != "" {
		item.CoverURL = fmt.Sprintf("/preview/collection-cover/%d", collection.ID)
	}
	return item
}

func activeVideoCountsByCollection(collectionIDs []uint) (map[uint]int64, error) {
	counts := make(map[uint]int64, len(collectionIDs))
	if len(collectionIDs) == 0 {
		return counts, nil
	}
	var rows []struct {
		EntityID uint  `gorm:"column:entity_id"`
		Count    int64 `gorm:"column:count"`
	}
	if err := database.DB.Model(&models.Video{}).
		Select("collection_videos.collection_id AS entity_id, COUNT(*) AS count").
		Joins("JOIN collection_videos ON collection_videos.video_id = videos.id").
		Where("collection_videos.collection_id IN ?", collectionIDs).
		Group("collection_videos.collection_id").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("count active collection videos: %w", err)
	}
	for _, row := range rows {
		counts[row.EntityID] = row.Count
	}
	return counts, nil
}

func (s *CollectionService) GetCollectionDetail(id uint) (*CollectionDetail, error) {
	var collection models.MediaCollection
	if err := database.DB.First(&collection, id).Error; err != nil {
		return nil, err
	}
	item, err := s.collectionListItem(collection)
	if err != nil {
		return nil, err
	}
	var relations []models.CollectionVideo
	if err := database.DB.Where("collection_id = ?", id).Order("position ASC").Order("video_id ASC").Find(&relations).Error; err != nil {
		return nil, fmt.Errorf("list collection members: %w", err)
	}
	videoIDs := make([]uint, 0, len(relations))
	for _, relation := range relations {
		videoIDs = append(videoIDs, relation.VideoID)
	}
	var activeVideos []models.Video
	if len(videoIDs) > 0 {
		if err := database.DB.Where("id IN ?", videoIDs).Find(&activeVideos).Error; err != nil {
			return nil, fmt.Errorf("load collection member videos: %w", err)
		}
	}
	videoByID := make(map[uint]models.Video, len(activeVideos))
	for _, video := range activeVideos {
		videoByID[video.ID] = video
	}
	videos := make([]CollectionVideoItem, 0, len(relations))
	for _, relation := range relations {
		video, active := videoByID[relation.VideoID]
		if !active {
			continue
		}
		videos = append(videos, CollectionVideoItem{Video: video, Position: relation.Position})
	}
	return &CollectionDetail{Collection: item, Videos: videos}, nil
}

func (s *CollectionService) AddCollectionVideo(collectionID, videoID uint) error {
	return s.AddCollectionVideos(collectionID, []uint{videoID})
}

// AddCollectionVideos atomically appends multiple active videos while
// preserving the caller's order and ignoring existing relationships.
func (s *CollectionService) AddCollectionVideos(collectionID uint, videoIDs []uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	videoIDs = uniqueUintIDs(videoIDs)
	if collectionID == 0 || len(videoIDs) == 0 {
		return fmt.Errorf("collection and at least one video are required")
	}
	return database.Transaction(func(tx *gorm.DB) error {
		if err := lockActiveCollection(tx, collectionID); err != nil {
			return err
		}
		var videoCount int64
		if err := tx.Model(&models.Video{}).Where("id IN ?", videoIDs).Count(&videoCount).Error; err != nil {
			return err
		}
		if videoCount != int64(len(videoIDs)) {
			return fmt.Errorf("one or more videos do not exist")
		}
		var existingVideoIDs []uint
		if err := tx.Model(&models.CollectionVideo{}).Where("collection_id = ? AND video_id IN ?", collectionID, videoIDs).Pluck("video_id", &existingVideoIDs).Error; err != nil {
			return err
		}
		existing := idSet(existingVideoIDs)
		var maxPosition int
		if err := tx.Model(&models.CollectionVideo{}).Where("collection_id = ?", collectionID).Select("COALESCE(MAX(position), 0)").Scan(&maxPosition).Error; err != nil {
			return err
		}
		relations := make([]models.CollectionVideo, 0, len(videoIDs))
		for _, videoID := range videoIDs {
			if _, found := existing[videoID]; found {
				continue
			}
			maxPosition++
			relations = append(relations, models.CollectionVideo{CollectionID: collectionID, VideoID: videoID, Position: maxPosition})
		}
		if len(relations) == 0 {
			return nil
		}
		return tx.Create(&relations).Error
	})
}

func (s *CollectionService) RemoveCollectionVideo(collectionID, videoID uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return database.Transaction(func(tx *gorm.DB) error {
		if err := lockActiveCollection(tx, collectionID); err != nil {
			return err
		}
		result := tx.Where("collection_id = ? AND video_id = ?", collectionID, videoID).Delete(&models.CollectionVideo{})
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		return compactCollectionPositions(tx, collectionID)
	})
}

func (s *CollectionService) ReorderCollectionVideos(collectionID uint, activeVideoIDs []uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return database.Transaction(func(tx *gorm.DB) error {
		if err := lockActiveCollection(tx, collectionID); err != nil {
			return err
		}
		var relations []models.CollectionVideo
		if err := tx.Where("collection_id = ?", collectionID).Order("position ASC").Order("video_id ASC").Find(&relations).Error; err != nil {
			return err
		}
		var activeIDs []uint
		if err := tx.Model(&models.Video{}).
			Joins("JOIN collection_videos ON collection_videos.video_id = videos.id").
			Where("collection_videos.collection_id = ?", collectionID).
			Order("collection_videos.position ASC").
			Pluck("videos.id", &activeIDs).Error; err != nil {
			return err
		}
		if !sameUniqueIDSet(activeIDs, activeVideoIDs) {
			return ErrCollectionOrderConflict
		}
		activeSet := idSet(activeIDs)
		slots := make([]int, 0, len(activeIDs))
		for _, relation := range relations {
			if _, active := activeSet[relation.VideoID]; active {
				slots = append(slots, relation.Position)
			}
		}
		for index, videoID := range activeVideoIDs {
			result := tx.Model(&models.CollectionVideo{}).
				Where("collection_id = ? AND video_id = ?", collectionID, videoID).
				Update("position", slots[index])
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrCollectionOrderConflict
			}
		}
		return nil
	})
}

func sameUniqueIDSet(existing, requested []uint) bool {
	if len(existing) != len(requested) {
		return false
	}
	requestedSet := make(map[uint]struct{}, len(requested))
	for _, id := range requested {
		if id == 0 {
			return false
		}
		if _, duplicate := requestedSet[id]; duplicate {
			return false
		}
		requestedSet[id] = struct{}{}
	}
	for _, id := range existing {
		if _, exists := requestedSet[id]; !exists {
			return false
		}
	}
	return true
}

func compactCollectionPositions(tx *gorm.DB, collectionID uint) error {
	var relations []models.CollectionVideo
	if err := tx.Where("collection_id = ?", collectionID).Order("position ASC").Order("video_id ASC").Find(&relations).Error; err != nil {
		return err
	}
	for index, relation := range relations {
		position := index + 1
		if relation.Position == position {
			continue
		}
		if err := tx.Model(&models.CollectionVideo{}).
			Where("collection_id = ? AND video_id = ?", collectionID, relation.VideoID).
			Update("position", position).Error; err != nil {
			return err
		}
	}
	return nil
}

func lockActiveCollection(tx *gorm.DB, collectionID uint) error {
	var collection models.MediaCollection
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&collection, collectionID).Error
}

func (s *CollectionService) DeleteCollection(collectionID uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var coverPath string
	err := database.Transaction(func(tx *gorm.DB) error {
		var collection models.MediaCollection
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&collection, collectionID).Error; err != nil {
			return err
		}
		coverPath = collection.CoverPath
		if err := tx.Where("collection_id = ?", collectionID).Delete(&models.CollectionVideo{}).Error; err != nil {
			return err
		}
		return tx.Delete(&collection).Error
	})
	if err != nil {
		return err
	}
	if coverPath != "" {
		if err := s.images.Remove(coverPath); err != nil {
			return fmt.Errorf("collection deleted but cover cleanup failed: %w", err)
		}
	}
	return nil
}

func (s *CollectionService) SetCollectionCover(collectionID uint, sourcePath string) (*models.MediaCollection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var collection models.MediaCollection
	if err := database.DB.First(&collection, collectionID).Error; err != nil {
		return nil, err
	}
	imported, err := s.images.Import("collections", collectionID, sourcePath)
	if err != nil {
		return nil, err
	}
	oldPath := collection.CoverPath
	if err := database.DB.Model(&models.MediaCollection{}).Where("id = ?", collectionID).Update("cover_path", imported.RelativePath).Error; err != nil {
		if imported.Created {
			if cleanupErr := s.images.Remove(imported.RelativePath); cleanupErr != nil {
				return nil, fmt.Errorf("update collection cover: %w", errors.Join(err, fmt.Errorf("remove unreferenced managed image: %w", cleanupErr)))
			}
		}
		return nil, fmt.Errorf("update collection cover: %w", err)
	}
	collection.CoverPath = imported.RelativePath
	if oldPath != "" && oldPath != imported.RelativePath {
		if err := s.images.Remove(oldPath); err != nil {
			return &collection, fmt.Errorf("collection cover updated but old image cleanup failed: %w", err)
		}
	}
	return &collection, nil
}

func (s *CollectionService) RemoveCollectionCover(collectionID uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var collection models.MediaCollection
	if err := database.DB.First(&collection, collectionID).Error; err != nil {
		return err
	}
	if collection.CoverPath == "" {
		return nil
	}
	if err := database.DB.Model(&models.MediaCollection{}).Where("id = ?", collectionID).Update("cover_path", "").Error; err != nil {
		return fmt.Errorf("clear collection cover: %w", err)
	}
	if err := s.images.Remove(collection.CoverPath); err != nil {
		return fmt.Errorf("collection cover cleared but image cleanup failed: %w", err)
	}
	return nil
}

func (s *CollectionService) ResolveCollectionCover(collectionID uint) (ManagedImageAsset, error) {
	var collection models.MediaCollection
	if err := database.DB.Select("id", "cover_path").First(&collection, collectionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ManagedImageAsset{}, os.ErrNotExist
		}
		return ManagedImageAsset{}, err
	}
	if collection.CoverPath == "" {
		return ManagedImageAsset{}, os.ErrNotExist
	}
	return s.images.Resolve(collection.CoverPath)
}
