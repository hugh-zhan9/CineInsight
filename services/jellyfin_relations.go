package services

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"video-master/database"
	"video-master/models"
)

func jellyfinTypedIDs(raw, kind string) ([]uint, error) {
	parts := strings.Split(raw, ",")
	if len(parts) > 200 {
		return nil, errJellyfinQuery
	}
	ids := make([]uint, 0, len(parts))
	for _, part := range parts {
		k, id, err := jellyfinParseID(strings.TrimSpace(part))
		if err != nil || k != kind {
			return nil, errJellyfinQuery
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *JellyfinServer) peopleQuery(r *http.Request) (*gorm.DB, error) {
	videos, err := s.videoQuery(r, LibraryFilter{})
	if err != nil {
		return nil, err
	}
	links := database.DB.Model(&models.VideoPerson{}).Select("person_id").Where("video_id IN (?)", videos.Select("videos.id"))
	return database.DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).WithContext(r.Context()).Model(&models.Person{}).Where("people.id IN (?)", links), nil
}

func (s *JellyfinServer) visiblePerson(r *http.Request, id uint) (models.Person, error) {
	query, err := s.peopleQuery(r)
	if err != nil {
		return models.Person{}, err
	}
	var person models.Person
	err = query.First(&person, id).Error
	return person, err
}

func jellyfinArtworkTag(path string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(path))) }

func (s *JellyfinServer) personDTO(person models.Person) map[string]interface{} {
	item := s.folder(jellyPerson, person.ID, person.DisplayName, "Person", "")
	item["IsFolder"] = false
	if person.AvatarPath != "" {
		item["ImageTags"] = map[string]string{"Primary": jellyfinArtworkTag(person.AvatarPath)}
	}
	return item
}

func (s *JellyfinServer) serveEntityImage(w http.ResponseWriter, r *http.Request, kind string, id uint) {
	if r.Method != "GET" && r.Method != "HEAD" {
		jellyfinError(w, 405, "方法不支持")
		return
	}
	var asset ManagedImageAsset
	var err error
	if kind == jellyPerson {
		if _, err := s.visiblePerson(r, id); s.libraryError(w, err) {
			return
		}
		if s.people == nil {
			err = os.ErrNotExist
		} else {
			asset, err = s.people.ResolvePersonAvatar(id)
		}
	} else {
		query, queryErr := s.videoQuery(r, LibraryFilter{})
		if s.libraryError(w, queryErr) {
			return
		}
		var collection models.MediaCollection
		visible := database.DB.Model(&models.CollectionVideo{}).Select("collection_id").Where("video_id IN (?)", query.Select("videos.id"))
		if err := database.DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).WithContext(r.Context()).Where("id IN (?)", visible).First(&collection, id).Error; s.libraryError(w, err) {
			return
		}
		if s.collections == nil {
			err = os.ErrNotExist
		} else {
			asset, err = s.collections.ResolveCollectionCover(id)
		}
	}
	if err != nil {
		jellyfinError(w, 404, "图片不可用")
		return
	}
	jellyfinServeFile(w, r, asset.Path, asset.MIME, "")
}

func (s *JellyfinServer) addDetailPeople(r *http.Request, item map[string]interface{}, videoID uint) error {
	var people []models.Person
	if err := database.DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).WithContext(r.Context()).Where("id IN (?)", database.DB.Model(&models.VideoPerson{}).Select("person_id").Where("video_id = ?", videoID)).Order("id").Find(&people).Error; err != nil {
		return err
	}
	entries := []map[string]interface{}{}
	for _, person := range people {
		entry := map[string]interface{}{"Id": jellyfinID(jellyPerson, person.ID), "Name": person.DisplayName, "Type": "Actor"}
		if person.AvatarPath != "" {
			entry["PrimaryImageTag"] = jellyfinArtworkTag(person.AvatarPath)
		}
		entries = append(entries, entry)
	}
	item["People"] = entries
	return nil
}

func (s *JellyfinServer) listPeople(r *http.Request, q url.Values, start, limit int) ([]map[string]interface{}, int64, int, error) {
	if jellyfinParam(q, "ListItemIds") != "" || jellyfinParam(q, "ParentId") != "" {
		return nil, 0, start, errJellyfinQuery
	}
	return s.listFolders(r, q, 3, start, limit)
}

func (s *JellyfinServer) containingCollections(r *http.Request, q url.Values, start, limit int) ([]map[string]interface{}, int64, int, error) {
	if jellyfinParam(q, "ParentId") != "" {
		return nil, 0, start, errJellyfinQuery
	}
	return s.listFolders(r, q, 1, start, limit)
}

// relatedVideoQuery only reads existing relationships; the caller owns visibility and paging.
func (s *JellyfinServer) relatedVideoQuery(query *gorm.DB, sourceID uint) *gorm.DB {
	people := database.DB.Model(&models.VideoPerson{}).Select("video_id").Where("person_id IN (?)", database.DB.Model(&models.VideoPerson{}).Select("person_id").Where("video_id = ?", sourceID))
	collections := database.DB.Model(&models.CollectionVideo{}).Select("video_id").Where("collection_id IN (?)", database.DB.Model(&models.CollectionVideo{}).Select("collection_id").Where("video_id = ?", sourceID)).Where("collection_id IN (?)", database.DB.Model(&models.MediaCollection{}).Select("id"))
	tags := database.DB.Table("video_tags").Select("video_id").Where("tag_id IN (?)", database.DB.Table("video_tags").Select("tag_id").Where("video_id = ?", sourceID)).Where("tag_id IN (?)", database.DB.Model(&models.Tag{}).Select("id").Where("automatic_kind = ?", ""))
	sameSource := database.DB.Model(&models.VideoSameSourceRelation{}).Select("CASE WHEN video_a_id = ? THEN video_b_id ELSE video_a_id END", sourceID).Where("(video_a_id = ? OR video_b_id = ?) AND status = ?", sourceID, sourceID, models.VideoSameSourceStatusDetected)
	return query.Where("videos.id <> ?", sourceID).Where("(videos.id IN (?) OR videos.id IN (?) OR videos.id IN (?) OR videos.id IN (?))", people, collections, tags, sameSource)
}
