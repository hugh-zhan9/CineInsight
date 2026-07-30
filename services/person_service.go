package services

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const defaultEntityPageLimit = 50

type PersonListItem struct {
	Person           models.Person `json:"person"`
	AvatarURL        string        `json:"avatar_url"`
	ActiveVideoCount int64         `json:"active_video_count"`
	CursorName       string        `json:"cursor_name"`
}

type PersonDetail struct {
	Person      PersonListItem `json:"person"`
	Videos      []models.Video `json:"videos"`
	NextVideoID uint           `json:"next_video_id"`
}

type PersonService struct {
	images *ManagedImageService
	mu     sync.Mutex
}

func NewPersonService(dataDir string) *PersonService {
	return &PersonService{images: NewManagedImageService(dataDir)}
}

func validatePersonNames(displayName, originalName string) (string, string, error) {
	displayName = strings.TrimSpace(displayName)
	originalName = strings.TrimSpace(originalName)
	if displayName == "" {
		return "", "", errors.New("person display name is required")
	}
	if utf8.RuneCountInString(displayName) > 200 {
		return "", "", errors.New("person display name exceeds 200 characters")
	}
	if utf8.RuneCountInString(originalName) > 200 {
		return "", "", errors.New("person original name exceeds 200 characters")
	}
	return displayName, originalName, nil
}

func (s *PersonService) CreatePerson(displayName, originalName string) (*models.Person, error) {
	displayName, originalName, err := validatePersonNames(displayName, originalName)
	if err != nil {
		return nil, err
	}
	person := models.Person{DisplayName: displayName, OriginalName: originalName}
	if err := database.DB.Create(&person).Error; err != nil {
		return nil, fmt.Errorf("create person: %w", err)
	}
	return &person, nil
}

func (s *PersonService) UpdatePerson(id uint, displayName, originalName string) (*models.Person, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	displayName, originalName, err := validatePersonNames(displayName, originalName)
	if err != nil {
		return nil, err
	}
	result := database.DB.Model(&models.Person{}).Where("id = ?", id).Updates(map[string]any{
		"display_name":  displayName,
		"original_name": originalName,
	})
	if result.Error != nil {
		return nil, fmt.Errorf("update person: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var person models.Person
	if err := database.DB.First(&person, id).Error; err != nil {
		return nil, err
	}
	return &person, nil
}

func (s *PersonService) ListPeople(keyword, cursorName string, cursorID uint, limit int) ([]PersonListItem, error) {
	limit = normalizeEntityPageLimit(limit)
	query := database.DB.Model(&models.Person{})
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		pattern := "%" + strings.ToLower(escapeSQLLike(keyword)) + "%"
		query = query.Where("(LOWER(display_name) LIKE ? ESCAPE '\\' OR LOWER(original_name) LIKE ? ESCAPE '\\')", pattern, pattern)
	}
	if cursorName = strings.ToLower(strings.TrimSpace(cursorName)); cursorName != "" || cursorID != 0 {
		query = query.Where("(LOWER(display_name) > ? OR (LOWER(display_name) = ? AND id > ?))", cursorName, cursorName, cursorID)
	}
	var rows []struct {
		models.Person
		CursorName string `gorm:"column:cursor_name"`
	}
	if err := query.Select("people.*, LOWER(display_name) AS cursor_name").
		Order("LOWER(display_name) ASC").Order("id ASC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	personIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		personIDs = append(personIDs, row.ID)
	}
	counts, err := activeVideoCountsByPerson(personIDs)
	if err != nil {
		return nil, err
	}
	items := make([]PersonListItem, 0, len(rows))
	for _, row := range rows {
		item := personListItemWithCount(row.Person, counts[row.ID])
		item.CursorName = row.CursorName
		items = append(items, item)
	}
	return items, nil
}

func (s *PersonService) personListItem(person models.Person) (PersonListItem, error) {
	var activeVideoCount int64
	err := database.DB.Model(&models.Video{}).
		Joins("JOIN video_people ON video_people.video_id = videos.id").
		Where("video_people.person_id = ?", person.ID).
		Count(&activeVideoCount).Error
	if err != nil {
		return PersonListItem{}, fmt.Errorf("count active person videos: %w", err)
	}
	return personListItemWithCount(person, activeVideoCount), nil
}

func personListItemWithCount(person models.Person, activeVideoCount int64) PersonListItem {
	item := PersonListItem{Person: person, ActiveVideoCount: activeVideoCount}
	if person.AvatarPath != "" {
		item.AvatarURL = fmt.Sprintf("/preview/person-avatar/%d", person.ID)
	}
	return item
}

func activeVideoCountsByPerson(personIDs []uint) (map[uint]int64, error) {
	counts := make(map[uint]int64, len(personIDs))
	if len(personIDs) == 0 {
		return counts, nil
	}
	var rows []struct {
		EntityID uint  `gorm:"column:entity_id"`
		Count    int64 `gorm:"column:count"`
	}
	if err := database.DB.Model(&models.Video{}).
		Select("video_people.person_id AS entity_id, COUNT(*) AS count").
		Joins("JOIN video_people ON video_people.video_id = videos.id").
		Where("video_people.person_id IN ?", personIDs).
		Group("video_people.person_id").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("count active person videos: %w", err)
	}
	for _, row := range rows {
		counts[row.EntityID] = row.Count
	}
	return counts, nil
}

func (s *PersonService) GetPersonDetail(id, cursorVideoID uint, limit int) (*PersonDetail, error) {
	var person models.Person
	if err := database.DB.First(&person, id).Error; err != nil {
		return nil, err
	}
	item, err := s.personListItem(person)
	if err != nil {
		return nil, err
	}
	limit = normalizeEntityPageLimit(limit)
	query := database.DB.Model(&models.Video{}).
		Joins("JOIN video_people ON video_people.video_id = videos.id").
		Where("video_people.person_id = ?", id)
	if cursorVideoID != 0 {
		query = query.Where("videos.id < ?", cursorVideoID)
	}
	var videos []models.Video
	if err := query.Order("videos.id DESC").Limit(limit).Find(&videos).Error; err != nil {
		return nil, fmt.Errorf("list person videos: %w", err)
	}
	detail := &PersonDetail{Person: item, Videos: videos}
	if len(videos) == limit {
		detail.NextVideoID = videos[len(videos)-1].ID
	}
	return detail, nil
}

func normalizeEntityPageLimit(limit int) int {
	if limit <= 0 {
		return defaultEntityPageLimit
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func (s *PersonService) SetVideoPeople(videoID uint, personIDs []uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	desired := uniqueSortedIDs(personIDs)
	orphanAvatarPaths := make([]string, 0)
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		var video models.Video
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&video, videoID).Error; err != nil {
			return err
		}
		var oldRelations []models.VideoPerson
		if err := tx.Where("video_id = ?", videoID).Find(&oldRelations).Error; err != nil {
			return err
		}
		oldIDs := make([]uint, 0, len(oldRelations))
		for _, relation := range oldRelations {
			oldIDs = append(oldIDs, relation.PersonID)
		}
		oldIDs = uniqueSortedIDs(oldIDs)
		lockIDs := uniqueSortedIDs(append(append([]uint(nil), oldIDs...), desired...))
		if len(lockIDs) > 0 {
			var people []models.Person
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", lockIDs).Order("id ASC").Find(&people).Error; err != nil {
				return err
			}
			found := make(map[uint]struct{}, len(people))
			for _, person := range people {
				found[person.ID] = struct{}{}
			}
			for _, personID := range desired {
				if _, exists := found[personID]; !exists {
					return fmt.Errorf("person %d: %w", personID, gorm.ErrRecordNotFound)
				}
			}
		}
		oldSet := idSet(oldIDs)
		desiredSet := idSet(desired)
		removed := make([]uint, 0)
		for _, personID := range oldIDs {
			if _, keep := desiredSet[personID]; keep {
				continue
			}
			if err := tx.Where("video_id = ? AND person_id = ?", videoID, personID).Delete(&models.VideoPerson{}).Error; err != nil {
				return err
			}
			removed = append(removed, personID)
		}
		for _, personID := range desired {
			if _, exists := oldSet[personID]; exists {
				continue
			}
			relation := models.VideoPerson{VideoID: videoID, PersonID: personID}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&relation).Error; err != nil {
				return err
			}
		}
		for _, personID := range removed {
			var count int64
			if err := tx.Model(&models.VideoPerson{}).Where("person_id = ?", personID).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				continue
			}
			var person models.Person
			if err := tx.First(&person, personID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return err
			}
			if err := tx.Delete(&person).Error; err != nil {
				return err
			}
			if person.AvatarPath != "" {
				orphanAvatarPaths = append(orphanAvatarPaths, person.AvatarPath)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("set video people: %w", err)
	}
	for _, avatarPath := range orphanAvatarPaths {
		if err := s.images.Remove(avatarPath); err != nil {
			return fmt.Errorf("person relationship updated but orphan avatar cleanup failed: %w", err)
		}
	}
	return nil
}

func uniqueSortedIDs(ids []uint) []uint {
	set := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if id != 0 {
			set[id] = struct{}{}
		}
	}
	result := make([]uint, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func idSet(ids []uint) map[uint]struct{} {
	set := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

func (s *PersonService) SetPersonAvatar(personID uint, sourcePath string) (*models.Person, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var person models.Person
	if err := database.DB.First(&person, personID).Error; err != nil {
		return nil, err
	}
	imported, err := s.images.Import("people", personID, sourcePath)
	if err != nil {
		return nil, err
	}
	oldPath := person.AvatarPath
	if err := database.DB.Model(&models.Person{}).Where("id = ?", personID).Update("avatar_path", imported.RelativePath).Error; err != nil {
		if imported.Created {
			if cleanupErr := s.images.Remove(imported.RelativePath); cleanupErr != nil {
				return nil, fmt.Errorf("update person avatar: %w", errors.Join(err, fmt.Errorf("remove unreferenced managed image: %w", cleanupErr)))
			}
		}
		return nil, fmt.Errorf("update person avatar: %w", err)
	}
	person.AvatarPath = imported.RelativePath
	if oldPath != "" && oldPath != imported.RelativePath {
		if err := s.images.Remove(oldPath); err != nil {
			return &person, fmt.Errorf("person avatar updated but old image cleanup failed: %w", err)
		}
	}
	return &person, nil
}

func (s *PersonService) RemovePersonAvatar(personID uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var person models.Person
	if err := database.DB.First(&person, personID).Error; err != nil {
		return err
	}
	if person.AvatarPath == "" {
		return nil
	}
	if err := database.DB.Model(&models.Person{}).Where("id = ?", personID).Update("avatar_path", "").Error; err != nil {
		return fmt.Errorf("clear person avatar: %w", err)
	}
	if err := s.images.Remove(person.AvatarPath); err != nil {
		return fmt.Errorf("person avatar cleared but image cleanup failed: %w", err)
	}
	return nil
}

func (s *PersonService) ResolvePersonAvatar(personID uint) (ManagedImageAsset, error) {
	var person models.Person
	if err := database.DB.Select("id", "avatar_path").First(&person, personID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ManagedImageAsset{}, os.ErrNotExist
		}
		return ManagedImageAsset{}, err
	}
	if person.AvatarPath == "" {
		return ManagedImageAsset{}, os.ErrNotExist
	}
	return s.images.Resolve(person.AvatarPath)
}
