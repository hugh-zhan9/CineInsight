package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	Person    models.Person `json:"person"`
	AvatarURL string        `json:"avatar_url"`
	// ActiveVideoCount 与 ActiveImageCount 各自只数活跃（未软删除）媒体；
	// 人物同时覆盖两种媒体后两个计数都要有，前端才能判断「这是最后一条关系吗」。
	ActiveVideoCount int64  `json:"active_video_count"`
	ActiveImageCount int64  `json:"active_image_count"`
	CursorName       string `json:"cursor_name"`
}

// PersonDetail 的视频与图片各自独立分页、不混排（D-021）：Videos 由
// GetPersonDetail 的 cursorVideoID 推进，Images 只给首页，后续翻页走 GetPersonImages。
type PersonDetail struct {
	Person      PersonListItem `json:"person"`
	Videos      []models.Video `json:"videos"`
	NextVideoID uint           `json:"next_video_id"`
	Images      []models.Image `json:"images"`
	NextImageID uint           `json:"next_image_id"`
}

// PersonImagePage 是人物详情「图片」区块的一页游标结果。
type PersonImagePage struct {
	Images      []models.Image `json:"images"`
	NextImageID uint           `json:"next_image_id"`
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
	imageCounts, err := activeImageCountsByPerson(personIDs)
	if err != nil {
		return nil, err
	}
	items := make([]PersonListItem, 0, len(rows))
	for _, row := range rows {
		item := personListItemWithCount(row.Person, counts[row.ID], imageCounts[row.ID])
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
	var activeImageCount int64
	err = database.DB.Model(&models.Image{}).
		Joins("JOIN image_people ON image_people.image_id = images.id").
		Where("image_people.person_id = ?", person.ID).
		Count(&activeImageCount).Error
	if err != nil {
		return PersonListItem{}, fmt.Errorf("count active person images: %w", err)
	}
	return personListItemWithCount(person, activeVideoCount, activeImageCount), nil
}

func personListItemWithCount(person models.Person, activeVideoCount, activeImageCount int64) PersonListItem {
	item := PersonListItem{Person: person, ActiveVideoCount: activeVideoCount, ActiveImageCount: activeImageCount}
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

// activeImageCountsByPerson 与 activeVideoCountsByPerson 同形：只数活跃图片，
// 软删除图片的关系仍在 image_people 里但不计入。
func activeImageCountsByPerson(personIDs []uint) (map[uint]int64, error) {
	counts := make(map[uint]int64, len(personIDs))
	if len(personIDs) == 0 {
		return counts, nil
	}
	var rows []struct {
		EntityID uint  `gorm:"column:entity_id"`
		Count    int64 `gorm:"column:count"`
	}
	if err := database.DB.Model(&models.Image{}).
		Select("image_people.person_id AS entity_id, COUNT(*) AS count").
		Joins("JOIN image_people ON image_people.image_id = images.id").
		Where("image_people.person_id IN ?", personIDs).
		Group("image_people.person_id").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("count active person images: %w", err)
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
	// 图片区块只在首屏给（cursorVideoID == 0）：视频与图片各自分页、互不影响
	// （D-021）。翻视频页时前端本来就丢弃响应里的图片字段，再查一次纯属浪费；
	// 后续图片翻页走 GetPersonImages。签名不变是为了不动既有前端调用。
	if cursorVideoID == 0 {
		imagePage, err := s.listPersonImages(id, 0, limit)
		if err != nil {
			return nil, err
		}
		detail.Images = imagePage.Images
		detail.NextImageID = imagePage.NextImageID
	}
	return detail, nil
}

// GetPersonImages 按 id 倒序游标翻页人物的活跃关联图片，与视频区块同一套口径。
// 人物不存在时返回 gorm.ErrRecordNotFound，与 GetPersonDetail 对称——否则调用方
// 分不清「这个人物没有图片」和「这个人物根本不在了」。
func (s *PersonService) GetPersonImages(personID, cursorImageID uint, limit int) (*PersonImagePage, error) {
	var person models.Person
	if err := database.DB.Select("id").First(&person, personID).Error; err != nil {
		return nil, err
	}
	return s.listPersonImages(personID, cursorImageID, limit)
}

// listPersonImages 是不校验人物存在的内部查询：GetPersonDetail 在它自己那次
// First 之后调用，不需要为同一个人物再查一遍。
func (s *PersonService) listPersonImages(personID, cursorImageID uint, limit int) (*PersonImagePage, error) {
	limit = normalizeEntityPageLimit(limit)
	query := database.DB.Model(&models.Image{}).
		Joins("JOIN image_people ON image_people.image_id = images.id").
		Where("image_people.person_id = ?", personID)
	if cursorImageID != 0 {
		query = query.Where("images.id < ?", cursorImageID)
	}
	var images []models.Image
	if err := query.Order("images.id DESC").Limit(limit).Find(&images).Error; err != nil {
		return nil, fmt.Errorf("list person images: %w", err)
	}
	page := &PersonImagePage{Images: images}
	if len(images) == limit {
		page.NextImageID = images[len(images)-1].ID
	}
	return page, nil
}

// AddPersonVideo creates one relationship from the person side without
// replacing the video's other people. Repeating the same request is safe.
func (s *PersonService) AddPersonVideo(personID, videoID uint) error {
	return s.AddPersonVideos(personID, []uint{videoID})
}

// AddPersonVideos atomically adds multiple video relationships from the
// person side. Repeated video IDs and already-related videos are ignored.
func (s *PersonService) AddPersonVideos(personID uint, videoIDs []uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	videoIDs = uniqueUintIDs(videoIDs)
	if personID == 0 || len(videoIDs) == 0 {
		return fmt.Errorf("person and at least one video are required")
	}

	err := database.Transaction(func(tx *gorm.DB) error {
		var videoCount int64
		if err := tx.Model(&models.Video{}).Where("id IN ?", videoIDs).Count(&videoCount).Error; err != nil {
			return err
		}
		if videoCount != int64(len(videoIDs)) {
			return fmt.Errorf("one or more videos do not exist")
		}
		var person models.Person
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&person, personID).Error; err != nil {
			return err
		}
		relations := make([]models.VideoPerson, 0, len(videoIDs))
		for _, videoID := range videoIDs {
			relations = append(relations, models.VideoPerson{VideoID: videoID, PersonID: personID})
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&relations).Error
	})
	if err != nil {
		return fmt.Errorf("add person videos: %w", err)
	}
	return nil
}

// RemovePersonVideo removes one relationship from the person side. It returns
// true when this was the person's final relationship and the person was
// consequently cleaned up, matching SetVideoPeople's existing semantics.
func (s *PersonService) RemovePersonVideo(personID, videoID uint) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	personDeleted := false
	avatarPath := ""
	err := database.Transaction(func(tx *gorm.DB) error {
		var video models.Video
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&video, videoID).Error; err != nil {
			return err
		}
		var person models.Person
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&person, personID).Error; err != nil {
			return err
		}
		result := tx.Where("video_id = ? AND person_id = ?", videoID, personID).Delete(&models.VideoPerson{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		hasRelations, err := personHasRemainingRelations(tx, personID)
		if err != nil {
			return err
		}
		if hasRelations {
			return nil
		}
		if err := tx.Delete(&person).Error; err != nil {
			return err
		}
		personDeleted = true
		avatarPath = person.AvatarPath
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("remove person video: %w", err)
	}
	if avatarPath != "" {
		if err := s.images.Remove(avatarPath); err != nil {
			return personDeleted, fmt.Errorf("person video removed but orphan avatar cleanup failed: %w", err)
		}
	}
	return personDeleted, nil
}

// AddPersonImages atomically adds multiple image relationships from the person
// side, mirroring AddPersonVideos. Repeated image IDs and already-related
// images are ignored.
func (s *PersonService) AddPersonImages(personID uint, imageIDs []uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	imageIDs = uniqueUintIDs(imageIDs)
	if personID == 0 || len(imageIDs) == 0 {
		return fmt.Errorf("person and at least one image are required")
	}

	err := database.Transaction(func(tx *gorm.DB) error {
		var imageCount int64
		if err := tx.Model(&models.Image{}).Where("id IN ?", imageIDs).Count(&imageCount).Error; err != nil {
			return err
		}
		if imageCount != int64(len(imageIDs)) {
			return fmt.Errorf("one or more images do not exist")
		}
		var person models.Person
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&person, personID).Error; err != nil {
			return err
		}
		relations := make([]models.ImagePerson, 0, len(imageIDs))
		for _, imageID := range imageIDs {
			relations = append(relations, models.ImagePerson{ImageID: imageID, PersonID: personID})
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&relations).Error
	})
	if err != nil {
		return fmt.Errorf("add person images: %w", err)
	}
	return nil
}

// RemovePersonImage removes one image relationship from the person side. Like
// RemovePersonVideo it returns true when this was the person's final
// relationship across both media kinds and the person was consequently
// cleaned up.
func (s *PersonService) RemovePersonImage(personID, imageID uint) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	personDeleted := false
	avatarPath := ""
	err := database.Transaction(func(tx *gorm.DB) error {
		var image models.Image
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&image, imageID).Error; err != nil {
			return err
		}
		var person models.Person
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&person, personID).Error; err != nil {
			return err
		}
		result := tx.Where("image_id = ? AND person_id = ?", imageID, personID).Delete(&models.ImagePerson{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		hasRelations, err := personHasRemainingRelations(tx, personID)
		if err != nil {
			return err
		}
		if hasRelations {
			return nil
		}
		if err := tx.Delete(&person).Error; err != nil {
			return err
		}
		personDeleted = true
		avatarPath = person.AvatarPath
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("remove person image: %w", err)
	}
	if avatarPath != "" {
		if err := s.images.Remove(avatarPath); err != nil {
			return personDeleted, fmt.Errorf("person image removed but orphan avatar cleanup failed: %w", err)
		}
	}
	return personDeleted, nil
}

// personHasRemainingRelations 是「最后一条关系」的唯一判定口径（D-015）：
// 人物同时覆盖视频与图片之后，只有 video_people 与 image_people 都没有这个人时
// 才算最后一条关系。任何只看单侧的判定都会把仍有另一侧关系的人物连带删掉——
// 人物删除会级联清空两张关系表，那是不可恢复的。
//
// 与 ActiveVideoCount / ActiveImageCount 的口径刻意不同：这里数的是关系行本身，
// 软删除媒体保留下来的关系也算数，所以软删除不会触发清理。
func personHasRemainingRelations(tx *gorm.DB, personID uint) (bool, error) {
	var videoRelations int64
	if err := tx.Model(&models.VideoPerson{}).Where("person_id = ?", personID).Count(&videoRelations).Error; err != nil {
		return false, err
	}
	if videoRelations != 0 {
		return true, nil
	}
	var imageRelations int64
	if err := tx.Model(&models.ImagePerson{}).Where("person_id = ?", personID).Count(&imageRelations).Error; err != nil {
		return false, err
	}
	return imageRelations != 0, nil
}

// 人物管理的哨兵错误；消息即错误码，前端按码分支。
var (
	ErrPersonNotFound = errors.New("person_not_found")
	ErrInvalidMerge   = errors.New("invalid_merge")
)

// PersonDeletionImpact 是删除人物前要给用户看的影响范围，按关系行统计（含软删除媒体）。
type PersonDeletionImpact struct {
	VideoCount       int64 `json:"video_count"`
	ImageCount       int64 `json:"image_count"`
	FaceClusterCount int64 `json:"face_cluster_count"`
}

// MergePeopleResult 是合并回执。
type MergePeopleResult struct {
	Target          PersonListItem `json:"target"`
	MergedCount     int            `json:"merged_count"`
	VideoLinksMoved int            `json:"video_links_moved"`
	ImageLinksMoved int            `json:"image_links_moved"`
}

func (s *PersonService) GetPersonDeletionImpact(id uint) (*PersonDeletionImpact, error) {
	var person models.Person
	if err := database.DB.Select("id").First(&person, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPersonNotFound
		}
		return nil, err
	}
	impact := &PersonDeletionImpact{}
	if err := database.DB.Model(&models.VideoPerson{}).Where("person_id = ?", id).Count(&impact.VideoCount).Error; err != nil {
		return nil, err
	}
	if err := database.DB.Model(&models.ImagePerson{}).Where("person_id = ?", id).Count(&impact.ImageCount).Error; err != nil {
		return nil, err
	}
	if err := database.DB.Model(&models.FaceCluster{}).Where("person_id = ?", id).Count(&impact.FaceClusterCount).Error; err != nil {
		return nil, err
	}
	return impact, nil
}

// deletePersonTx 硬删人物：两张关系表的行、人脸候选与命名簇的指向一并清理，簇回到未命名
// （与 reconcileFaceClusterPeople 同口径，但在同一事务里做，不依赖外键 SET NULL 与后续对账）。
func deletePersonTx(tx *gorm.DB, personID uint) error {
	if err := tx.Where("person_id = ?", personID).Delete(&models.VideoPerson{}).Error; err != nil {
		return err
	}
	if err := tx.Where("person_id = ?", personID).Delete(&models.ImagePerson{}).Error; err != nil {
		return err
	}
	if err := tx.Where("person_id = ?", personID).Delete(&models.FacePersonCandidate{}).Error; err != nil {
		return err
	}
	if err := tx.Model(&models.FaceCluster{}).
		Where("person_id = ? AND status = ?", personID, models.FaceClusterStatusNamed).
		Updates(map[string]any{"person_id": nil, "status": models.FaceClusterStatusUnnamed}).Error; err != nil {
		return err
	}
	if err := tx.Model(&models.FaceCluster{}).Where("person_id = ?", personID).
		Update("person_id", nil).Error; err != nil {
		return err
	}
	return tx.Delete(&models.Person{}, personID).Error
}

// DeletePerson 删除人物及其全部关系与头像（D-PC32）；头像文件在事务提交之后才删。
func (s *PersonService) DeletePerson(id uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	avatarPath := ""
	err := database.Transaction(func(tx *gorm.DB) error {
		var person models.Person
		if err := tx.First(&person, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPersonNotFound
			}
			return err
		}
		avatarPath = person.AvatarPath
		return deletePersonTx(tx, id)
	})
	if err != nil {
		if errors.Is(err, ErrPersonNotFound) {
			return err
		}
		return fmt.Errorf("delete person: %w", err)
	}
	if avatarPath != "" {
		if err := s.images.Remove(avatarPath); err != nil {
			return fmt.Errorf("person deleted but avatar cleanup failed: %w", err)
		}
	}
	return nil
}

// rewriteSavedViewPersonIDsTx 把活跃保存视图 person_ids_json 里的来源人物 ID 改写为目标 ID 并去重（升序）。
// 只改包含来源 ID 的视图；JSON 解析不了的行保持原样（不因一行脏数据阻断合并）。
func rewriteSavedViewPersonIDsTx(tx *gorm.DB, sources []uint, targetID uint) error {
	sourceSet := make(map[uint]struct{}, len(sources))
	for _, id := range sources {
		sourceSet[id] = struct{}{}
	}
	var views []models.SavedLibraryView
	if err := tx.Where("person_ids_json <> ?", "[]").Find(&views).Error; err != nil {
		return err
	}
	for _, view := range views {
		var ids []uint
		if err := json.Unmarshal([]byte(view.PersonIDsJSON), &ids); err != nil {
			continue
		}
		touched := false
		for index, id := range ids {
			if _, isSource := sourceSet[id]; isSource {
				ids[index] = targetID
				touched = true
			}
		}
		if !touched {
			continue
		}
		ids = uniqueUintIDs(ids)
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		encoded, err := json.Marshal(ids)
		if err != nil {
			return err
		}
		if err := tx.Model(&models.SavedLibraryView{}).Where("id = ?", view.ID).
			Update("person_ids_json", string(encoded)).Error; err != nil {
			return err
		}
	}
	return nil
}

// MergePeople 把 sourceIDs 并入 targetID（D-PC32）：单一事务改写关系并去重、簇改指向目标、
// 目标无头像时复制来源头像，最后硬删来源人物；来源头像文件在提交之后才删。
func (s *PersonService) MergePeople(targetID uint, sourceIDs []uint) (*MergePeopleResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sources := uniqueSortedIDs(sourceIDs)
	for _, id := range sourceIDs {
		if id == targetID {
			return nil, ErrInvalidMerge
		}
	}
	if targetID == 0 || len(sources) == 0 {
		return nil, ErrInvalidMerge
	}
	result := &MergePeopleResult{MergedCount: len(sources)}
	var orphanAvatars []string
	err := database.Transaction(func(tx *gorm.DB) error {
		var target models.Person
		if err := tx.First(&target, targetID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPersonNotFound
			}
			return err
		}
		var sourcePeople []models.Person
		if err := tx.Where("id IN ?", sources).Order("id").Find(&sourcePeople).Error; err != nil {
			return err
		}
		if len(sourcePeople) != len(sources) {
			return ErrPersonNotFound
		}
		// ① 关系改写为目标并去重，再删来源残行。
		videoInsert := tx.Exec(`INSERT INTO video_people (video_id, person_id, created_at)
			SELECT video_id, ?, MIN(created_at) FROM video_people WHERE person_id IN ? GROUP BY video_id
			ON CONFLICT (video_id, person_id) DO NOTHING`, targetID, sources)
		if videoInsert.Error != nil {
			return videoInsert.Error
		}
		result.VideoLinksMoved = int(videoInsert.RowsAffected)
		imageInsert := tx.Exec(`INSERT INTO image_people (image_id, person_id, created_at)
			SELECT image_id, ?, MIN(created_at) FROM image_people WHERE person_id IN ? GROUP BY image_id
			ON CONFLICT (image_id, person_id) DO NOTHING`, targetID, sources)
		if imageInsert.Error != nil {
			return imageInsert.Error
		}
		result.ImageLinksMoved = int(imageInsert.RowsAffected)
		if err := tx.Where("person_id IN ?", sources).Delete(&models.VideoPerson{}).Error; err != nil {
			return err
		}
		if err := tx.Where("person_id IN ?", sources).Delete(&models.ImagePerson{}).Error; err != nil {
			return err
		}
		// ② 命名簇改指向目标。
		if err := tx.Model(&models.FaceCluster{}).Where("person_id IN ?", sources).
			Update("person_id", targetID).Error; err != nil {
			return err
		}
		// ③ 目标没有头像时，从第一个有头像的来源复制。
		importedAvatar := managedImageImport{}
		if target.AvatarPath == "" {
			for _, source := range sourcePeople {
				if source.AvatarPath == "" {
					continue
				}
				asset, err := s.images.Resolve(source.AvatarPath)
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						continue
					}
					return fmt.Errorf("读取来源头像失败: %w", err)
				}
				importedAvatar, err = s.images.Import("people", targetID, asset.Path)
				if err != nil {
					return fmt.Errorf("复制来源头像失败: %w", err)
				}
				if err := tx.Model(&models.Person{}).Where("id = ?", targetID).
					Update("avatar_path", importedAvatar.RelativePath).Error; err != nil {
					if importedAvatar.Created {
						_ = s.images.Remove(importedAvatar.RelativePath)
					}
					return err
				}
				break
			}
		}
		// ③b 活跃保存视图的人物条件：来源 ID 改写为目标 ID（去重），与标签条件同语义，
		// 合并后视图不会静默放宽（Minor 5）。
		if err := rewriteSavedViewPersonIDsTx(tx, sources, targetID); err != nil {
			if importedAvatar.Created {
				_ = s.images.Remove(importedAvatar.RelativePath)
			}
			return err
		}
		// ④ 硬删来源人物（关系与簇已迁走，deletePersonTx 只会清掉残余候选）。
		for _, source := range sourcePeople {
			if err := deletePersonTx(tx, source.ID); err != nil {
				if importedAvatar.Created {
					_ = s.images.Remove(importedAvatar.RelativePath)
				}
				return err
			}
			if source.AvatarPath != "" {
				orphanAvatars = append(orphanAvatars, source.AvatarPath)
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrPersonNotFound) || errors.Is(err, ErrInvalidMerge) {
			return nil, err
		}
		return nil, fmt.Errorf("merge people: %w", err)
	}
	for _, path := range orphanAvatars {
		if err := s.images.Remove(path); err != nil {
			log.Printf("合并人物后清理来源头像失败: %v", err)
		}
	}
	var target models.Person
	if err := database.DB.First(&target, targetID).Error; err != nil {
		return nil, err
	}
	item, err := s.personListItem(target)
	if err != nil {
		return nil, err
	}
	result.Target = item
	return result, nil
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
	err := database.Transaction(func(tx *gorm.DB) error {
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
			hasRelations, err := personHasRemainingRelations(tx, personID)
			if err != nil {
				return err
			}
			if hasRelations {
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

// SetImagePeople replaces one image's people, mirroring SetVideoPeople: the
// removal side prunes people that no longer have any relationship at all.
func (s *PersonService) SetImagePeople(imageID uint, personIDs []uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	desired := uniqueSortedIDs(personIDs)
	orphanAvatarPaths := make([]string, 0)
	err := database.Transaction(func(tx *gorm.DB) error {
		var image models.Image
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&image, imageID).Error; err != nil {
			return err
		}
		var oldRelations []models.ImagePerson
		if err := tx.Where("image_id = ?", imageID).Find(&oldRelations).Error; err != nil {
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
			if err := tx.Where("image_id = ? AND person_id = ?", imageID, personID).Delete(&models.ImagePerson{}).Error; err != nil {
				return err
			}
			removed = append(removed, personID)
		}
		for _, personID := range desired {
			if _, exists := oldSet[personID]; exists {
				continue
			}
			relation := models.ImagePerson{ImageID: imageID, PersonID: personID}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&relation).Error; err != nil {
				return err
			}
		}
		for _, personID := range removed {
			hasRelations, err := personHasRemainingRelations(tx, personID)
			if err != nil {
				return err
			}
			if hasRelations {
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
		return fmt.Errorf("set image people: %w", err)
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

// RemoveManagedAvatar 删除一个托管头像文件；供撤销标签转人物删掉新建人物时清理头像用。
func (s *PersonService) RemoveManagedAvatar(relativePath string) error {
	return s.images.Remove(relativePath)
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
