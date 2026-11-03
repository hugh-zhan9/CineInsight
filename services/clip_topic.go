package services

import (
	"context"
	"database/sql"
	"errors"
	"gorm.io/gorm"
	"sort"
	"strings"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"
)

var ErrClipTopicInvalid = errors.New("clip_topic_invalid: 请检查专题、书签和操作范围")
var ErrClipTopicChanged = errors.New("clip_topic_changed: 专题已变化，请刷新后重试")
var ErrClipTopicFull = errors.New("clip_topic_full: 一个专题最多一万个书签，单次最多编辑200项")

type ClipTopicInput struct {
	ID               uint   `json:"id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Name             string `json:"name"`
	Note             string `json:"note"`
}
type ClipTopicQuery struct {
	CursorID uint `json:"cursor_id"`
	Limit    int  `json:"limit"`
}
type ClipTopicDTO struct {
	Topic       models.ClipTopic `json:"topic"`
	MemberCount int64            `json:"member_count"`
}
type ClipTopicPage struct {
	Items      []ClipTopicDTO `json:"items"`
	NextCursor uint           `json:"next_cursor"`
}
type ClipTopicMembersQuery struct {
	TopicID          uint   `json:"topic_id"`
	Revision         uint64 `json:"revision"`
	CursorPosition   int64  `json:"cursor_position"`
	CursorBookmarkID uint   `json:"cursor_bookmark_id"`
	Limit            int    `json:"limit"`
}
type ClipTopicMemberDTO struct {
	Bookmark BookmarkDTO `json:"bookmark"`
	Position int64       `json:"position"`
}
type ClipTopicMembersPage struct {
	Topic            ClipTopicDTO         `json:"topic"`
	Items            []ClipTopicMemberDTO `json:"items"`
	CursorPosition   int64                `json:"cursor_position"`
	CursorBookmarkID uint                 `json:"cursor_bookmark_id"`
}
type ClipTopicEdit struct {
	TopicID          uint   `json:"topic_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Action           string `json:"action"`
	BookmarkIDs      []uint `json:"bookmark_ids"`
	BeforeBookmarkID uint   `json:"before_bookmark_id"`
}
type BookmarkTopicReferences struct {
	BookmarkID uint   `json:"bookmark_id"`
	Revision   uint64 `json:"revision"`
	TopicCount int64  `json:"topic_count"`
}
type ClipBookmarkInput struct {
	TopicID               uint          `json:"topic_id"`
	ExpectedTopicRevision uint64        `json:"expected_topic_revision"`
	Bookmark              BookmarkInput `json:"bookmark"`
}
type ClipBookmarkResult struct {
	Topic    ClipTopicDTO `json:"topic"`
	Bookmark BookmarkDTO  `json:"bookmark"`
}

func validClipTopicIdentity(id uint, revision uint64) bool {
	return id > 0 && id <= experienceMaxSafeInteger && revision > 0 && revision < experienceMaxSafeInteger
}
func clipTopicLimit(limit int) (int, error) {
	if limit == 0 {
		return 50, nil
	}
	if limit < 1 || limit > 200 {
		return 0, ErrClipTopicInvalid
	}
	return limit, nil
}
func validateClipBookmarkIDs(ids []uint) error {
	if len(ids) == 0 {
		return ErrClipTopicInvalid
	}
	if len(ids) > 200 {
		return ErrClipTopicFull
	}
	seen := map[uint]bool{}
	for _, id := range ids {
		if id == 0 || id > experienceMaxSafeInteger || seen[id] {
			return ErrClipTopicInvalid
		}
		seen[id] = true
	}
	return nil
}

// Relation changes participate in the bookmark CAS, including the absence of
// a relationship. Conditional updates, not explicit locks, decide the winner.
func claimBookmarkVersion(tx *gorm.DB, id uint, expected uint64) error {
	if id == 0 || expected == 0 || expected >= experienceMaxSafeInteger {
		return ErrViewingNoteInvalid
	}
	update := tx.Model(&models.VideoBookmark{}).Where("id = ? AND revision = ?", id, expected).UpdateColumn("revision", expected+1)
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return ErrViewingNoteConflict
	}
	return nil
}
func claimClipBookmarks(tx *gorm.DB, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	ordered := append([]uint{}, ids...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	var rows []models.VideoBookmark
	if err := tx.Where("id IN ?", ordered).Order("id ASC").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(ordered) {
		return ErrViewingNoteConflict
	}
	for _, row := range rows {
		if err := claimBookmarkVersion(tx, row.ID, row.Revision); err != nil {
			return err
		}
	}
	return nil
}

// expected=0 is reserved for indirect deletion of an already locked bookmark.
func claimClipTopic(tx *gorm.DB, id uint, expected uint64) error {
	var topic models.ClipTopic
	if err := tx.First(&topic, id).Error; err != nil {
		return err
	}
	if topic.Revision == 0 || topic.Revision >= experienceMaxSafeInteger || expected != 0 && topic.Revision != expected {
		return ErrClipTopicChanged
	}
	update := tx.Model(&models.ClipTopic{}).Where("id = ? AND revision = ?", id, topic.Revision).Update("revision", topic.Revision+1)
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return ErrClipTopicChanged
	}
	return nil
}
func clipTopicDTOFrom(tx *gorm.DB, id uint) (ClipTopicDTO, error) {
	var result ClipTopicDTO
	if err := tx.First(&result.Topic, id).Error; err != nil {
		return result, err
	}
	err := tx.Model(&models.ClipTopicMember{}).Where("topic_id = ?", id).Count(&result.MemberCount).Error
	return result, err
}
func (s *BookmarkService) SaveClipTopic(ctx context.Context, input ClipTopicInput) (*ClipTopicDTO, error) {
	input.Name = strings.TrimSpace(input.Name)
	if !utf8.ValidString(input.Name) || !utf8.ValidString(input.Note) || utf8.RuneCountInString(input.Name) < 1 || utf8.RuneCountInString(input.Name) > 100 || utf8.RuneCountInString(input.Note) > 5000 || input.ID == 0 && input.ExpectedRevision != 0 || input.ID != 0 && !validClipTopicIdentity(input.ID, input.ExpectedRevision) {
		return nil, ErrClipTopicInvalid
	}
	var result ClipTopicDTO
	err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		if input.ID == 0 {
			topic := models.ClipTopic{Name: input.Name, Note: input.Note, Revision: 1}
			if err := tx.Create(&topic).Error; err != nil {
				return err
			}
			input.ID = topic.ID
		} else {
			if err := claimClipTopic(tx, input.ID, input.ExpectedRevision); err != nil {
				return err
			}
			if err := tx.Model(&models.ClipTopic{}).Where("id = ?", input.ID).Updates(map[string]any{"name": input.Name, "note": input.Note}).Error; err != nil {
				return err
			}
		}
		var err error
		result, err = clipTopicDTOFrom(tx, input.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
func (s *BookmarkService) ListClipTopics(ctx context.Context, query ClipTopicQuery) (*ClipTopicPage, error) {
	limit, err := clipTopicLimit(query.Limit)
	if err != nil || query.CursorID > experienceMaxSafeInteger {
		return nil, ErrClipTopicInvalid
	}
	result := &ClipTopicPage{Items: []ClipTopicDTO{}}
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			var topics []models.ClipTopic
			topicQuery := tx.Order("id DESC")
			if query.CursorID != 0 {
				topicQuery = topicQuery.Where("id < ?", query.CursorID)
			}
			if err := topicQuery.Limit(limit + 1).Find(&topics).Error; err != nil {
				return err
			}
			if len(topics) > limit {
				topics = topics[:limit]
				result.NextCursor = topics[len(topics)-1].ID
			}
			ids := []uint{}
			for _, topic := range topics {
				ids = append(ids, topic.ID)
			}
			counts := map[uint]int64{}
			if len(ids) > 0 {
				var rows []struct {
					TopicID uint
					Count   int64
				}
				if err := tx.Model(&models.ClipTopicMember{}).Select("topic_id,COUNT(*) AS count").Where("topic_id IN ?", ids).Group("topic_id").Scan(&rows).Error; err != nil {
					return err
				}
				for _, row := range rows {
					counts[row.TopicID] = row.Count
				}
			}
			for _, topic := range topics {
				result.Items = append(result.Items, ClipTopicDTO{Topic: topic, MemberCount: counts[topic.ID]})
			}
			return nil
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	})
	return result, err
}
func (s *BookmarkService) ListClipTopicMembers(ctx context.Context, query ClipTopicMembersQuery) (*ClipTopicMembersPage, error) {
	limit, err := clipTopicLimit(query.Limit)
	if err != nil || query.TopicID == 0 || query.TopicID > experienceMaxSafeInteger || query.Revision > experienceMaxSafeInteger || query.CursorPosition < 0 || query.CursorPosition > experienceMaxSafeInteger || query.CursorBookmarkID > experienceMaxSafeInteger || (query.CursorPosition == 0) != (query.CursorBookmarkID == 0) || query.CursorBookmarkID != 0 && query.Revision == 0 {
		return nil, ErrClipTopicInvalid
	}
	result := &ClipTopicMembersPage{Items: []ClipTopicMemberDTO{}}
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			var err error
			result.Topic, err = clipTopicDTOFrom(tx, query.TopicID)
			if err != nil {
				return err
			}
			if query.Revision != 0 && query.Revision != result.Topic.Topic.Revision {
				return ErrClipTopicChanged
			}
			rowsQuery := tx.Where("topic_id = ?", query.TopicID)
			if query.CursorBookmarkID != 0 {
				rowsQuery = rowsQuery.Where("position > ? OR (position = ? AND bookmark_id > ?)", query.CursorPosition, query.CursorPosition, query.CursorBookmarkID)
			}
			var members []models.ClipTopicMember
			if err := rowsQuery.Order("position ASC,bookmark_id ASC").Limit(limit + 1).Find(&members).Error; err != nil {
				return err
			}
			if len(members) > limit {
				members = members[:limit]
				last := members[len(members)-1]
				result.CursorPosition = last.Position
				result.CursorBookmarkID = last.BookmarkID
			}
			ids := []uint{}
			for _, member := range members {
				ids = append(ids, member.BookmarkID)
			}
			if len(ids) == 0 {
				return nil
			}
			var bookmarks []models.VideoBookmark
			if err := tx.Where("id IN ?", ids).Find(&bookmarks).Error; err != nil {
				return err
			}
			byID := map[uint]BookmarkDTO{}
			videoIDs := []uint{}
			for _, bookmark := range bookmarks {
				dto, err := bookmarkDTO(bookmark)
				if err != nil {
					return err
				}
				byID[bookmark.ID] = dto
				videoIDs = append(videoIDs, bookmark.VideoID)
			}
			visible, err := visibleVideoQuery(tx.Model(&models.Video{}))
			if err != nil {
				return err
			}
			var visibleIDs []uint
			if err := visible.Where("videos.id IN ?", videoIDs).Pluck("videos.id", &visibleIDs).Error; err != nil {
				return err
			}
			available := map[uint]bool{}
			for _, id := range visibleIDs {
				available[id] = true
			}
			for _, member := range members {
				bookmark, ok := byID[member.BookmarkID]
				if !ok {
					return ErrClipTopicChanged
				}
				bookmark.SourceAvailable = available[bookmark.VideoID]
				result.Items = append(result.Items, ClipTopicMemberDTO{Bookmark: bookmark, Position: member.Position})
			}
			return nil
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	})
	return result, err
}
func reorderClipTopic(tx *gorm.DB, topicID uint, order []uint) error {
	if len(order) == 0 {
		return nil
	}
	var maximum int64
	if err := tx.Model(&models.ClipTopicMember{}).Where("topic_id = ?", topicID).Select("COALESCE(MAX(position),0)").Scan(&maximum).Error; err != nil {
		return err
	}
	if maximum < 0 || maximum > (experienceMaxSafeInteger-int64(len(order))-1)/2 {
		return ErrClipTopicInvalid
	}
	offset := maximum + int64(len(order)) + 1
	if offset > experienceMaxSafeInteger-maximum {
		return ErrClipTopicInvalid
	}
	if err := tx.Model(&models.ClipTopicMember{}).Where("topic_id = ?", topicID).UpdateColumn("position", gorm.Expr("position + ?", offset)).Error; err != nil {
		return err
	}
	for i, id := range order {
		updated := tx.Model(&models.ClipTopicMember{}).Where("topic_id = ? AND bookmark_id = ?", topicID, id).UpdateColumn("position", i+1)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrClipTopicChanged
		}
	}
	return nil
}
func addClipTopicMembers(tx *gorm.DB, topicID uint, ids []uint) error {
	var existing []models.ClipTopicMember
	if err := tx.Where("topic_id = ?", topicID).Find(&existing).Error; err != nil {
		return err
	}
	present := map[uint]bool{}
	var position int64
	for _, member := range existing {
		present[member.BookmarkID] = true
		position = max(position, member.Position)
	}
	added := []uint{}
	for _, id := range ids {
		if !present[id] {
			added = append(added, id)
		}
	}
	if len(existing)+len(added) > 10000 {
		return ErrClipTopicFull
	}
	if position > experienceMaxSafeInteger-int64(len(added)) {
		return ErrClipTopicInvalid
	}
	for _, id := range added {
		position++
		if err := tx.Create(&models.ClipTopicMember{TopicID: topicID, BookmarkID: id, Position: position}).Error; err != nil {
			return err
		}
	}
	return nil
}
func (s *BookmarkService) EditClipTopic(ctx context.Context, input ClipTopicEdit) (*ClipTopicDTO, error) {
	if !validClipTopicIdentity(input.TopicID, input.ExpectedRevision) || input.BeforeBookmarkID > experienceMaxSafeInteger {
		return nil, ErrClipTopicInvalid
	}
	if err := validateClipBookmarkIDs(input.BookmarkIDs); err != nil {
		return nil, err
	}
	if input.Action != "add" && input.Action != "remove" && input.Action != "move" || input.Action != "move" && input.BeforeBookmarkID != 0 {
		return nil, ErrClipTopicInvalid
	}
	var result ClipTopicDTO
	err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		if input.Action != "move" {
			affected := append([]uint{}, input.BookmarkIDs...)
			if input.Action == "add" {
				var existing []uint
				if err := tx.Model(&models.ClipTopicMember{}).Where("topic_id = ? AND bookmark_id IN ?", input.TopicID, input.BookmarkIDs).Pluck("bookmark_id", &existing).Error; err != nil {
					return err
				}
				present := map[uint]bool{}
				for _, id := range existing {
					present[id] = true
				}
				affected = nil
				for _, id := range input.BookmarkIDs {
					if !present[id] {
						affected = append(affected, id)
					}
				}
			}
			if err := claimClipBookmarks(tx, affected); err != nil {
				return err
			}
		}
		if err := claimClipTopic(tx, input.TopicID, input.ExpectedRevision); err != nil {
			return err
		}
		if input.Action == "add" {
			if err := addClipTopicMembers(tx, input.TopicID, input.BookmarkIDs); err != nil {
				return err
			}
		} else {
			var members []models.ClipTopicMember
			if err := tx.Where("topic_id = ?", input.TopicID).Order("position ASC,bookmark_id ASC").Find(&members).Error; err != nil {
				return err
			}
			selected := map[uint]bool{}
			for _, id := range input.BookmarkIDs {
				selected[id] = true
			}
			remaining, moving := []uint{}, []uint{}
			foundBefore := input.BeforeBookmarkID == 0
			for _, member := range members {
				if member.BookmarkID == input.BeforeBookmarkID {
					foundBefore = true
				}
				if selected[member.BookmarkID] {
					moving = append(moving, member.BookmarkID)
				} else {
					remaining = append(remaining, member.BookmarkID)
				}
			}
			if len(moving) != len(input.BookmarkIDs) || !foundBefore || selected[input.BeforeBookmarkID] {
				return ErrClipTopicChanged
			}
			if input.Action == "remove" {
				if err := tx.Where("topic_id = ? AND bookmark_id IN ?", input.TopicID, input.BookmarkIDs).Delete(&models.ClipTopicMember{}).Error; err != nil {
					return err
				}
			} else {
				at := len(remaining)
				for i, id := range remaining {
					if id == input.BeforeBookmarkID {
						at = i
						break
					}
				}
				order := append([]uint{}, remaining[:at]...)
				order = append(order, moving...)
				order = append(order, remaining[at:]...)
				remaining = order
			}
			if err := reorderClipTopic(tx, input.TopicID, remaining); err != nil {
				return err
			}
		}
		var err error
		result, err = clipTopicDTOFrom(tx, input.TopicID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
func (s *BookmarkService) DeleteClipTopic(ctx context.Context, id uint, revision uint64) error {
	if !validClipTopicIdentity(id, revision) {
		return ErrClipTopicInvalid
	}
	return database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		var ids []uint
		if err := tx.Model(&models.ClipTopicMember{}).Where("topic_id = ?", id).Order("bookmark_id ASC").Pluck("bookmark_id", &ids).Error; err != nil {
			return err
		}
		if err := claimClipBookmarks(tx, ids); err != nil {
			return err
		}
		if err := claimClipTopic(tx, id, revision); err != nil {
			return err
		}
		return tx.Delete(&models.ClipTopic{}, id).Error
	})
}
func (s *BookmarkService) BookmarkTopicReferences(ctx context.Context, id uint, revision uint64) (*BookmarkTopicReferences, error) {
	if !validClipTopicIdentity(id, revision) {
		return nil, ErrClipTopicInvalid
	}
	result := &BookmarkTopicReferences{BookmarkID: id, Revision: revision}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			var bookmark models.VideoBookmark
			if err := tx.First(&bookmark, id).Error; err != nil {
				return err
			}
			if bookmark.Revision != revision {
				return ErrViewingNoteConflict
			}
			return tx.Model(&models.ClipTopicMember{}).Where("bookmark_id = ?", id).Count(&result.TopicCount).Error
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	})
	return result, err
}
func (s *BookmarkService) SaveClipBookmark(ctx context.Context, input ClipBookmarkInput) (*ClipBookmarkResult, error) {
	if !validClipTopicIdentity(input.TopicID, input.ExpectedTopicRevision) || input.Bookmark.ID != 0 {
		return nil, ErrClipTopicInvalid
	}
	bookmark, encoded, err := normalizeBookmarkInput(input.Bookmark)
	if err != nil {
		return nil, err
	}
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	result := &ClipBookmarkResult{}
	err = database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		if err := claimClipTopic(tx, input.TopicID, input.ExpectedTopicRevision); err != nil {
			return err
		}
		visible, err := visibleVideoQuery(tx.Model(&models.Video{}))
		if err != nil {
			return err
		}
		var count int64
		if err := visible.Where("videos.id = ?", bookmark.VideoID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrBookmarkSourceUnavailable
		}
		saved, err := saveBookmarkFrom(tx, bookmark, encoded)
		if err != nil {
			return err
		}
		if err := addClipTopicMembers(tx, input.TopicID, []uint{saved.ID}); err != nil {
			return err
		}
		result.Bookmark = *saved
		result.Topic, err = clipTopicDTOFrom(tx, input.TopicID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
