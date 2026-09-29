package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TagPersonConversionPreview struct {
	TagID      uint            `json:"tag_id"`
	TagName    string          `json:"tag_name"`
	VideoCount int64           `json:"video_count"`
	ImageCount int64           `json:"image_count"`
	People     []models.Person `json:"people"`
}

type TagPersonConversionRequest struct {
	TagID          uint   `json:"tag_id"`
	TagName        string `json:"tag_name"`
	TargetPersonID uint   `json:"target_person_id"`
	CreateNew      bool   `json:"create_new"`
}

type TagPersonConversionResult struct {
	// ConversionID 是 tag_person_conversions 的记录 ID，撤销时凭它复原（D-PC34）。
	ConversionID uint          `json:"conversion_id"`
	Person       models.Person `json:"person"`
	VideoCount   int64         `json:"video_count"`
	ImageCount   int64         `json:"image_count"`
}

// 撤销与转换记录的哨兵错误；消息即错误码（沿用 ErrFacePersonNotFound 的做法），前端按码分支。
var (
	ErrConversionNotFound   = errors.New("conversion_not_found")
	ErrConversionNotApplied = errors.New("conversion_not_applied")
	ErrTagNameTaken         = errors.New("tag_name_taken")
)

const conversionIDChunk = 500

func validatePersonConversionTag(tag models.Tag) error {
	if tag.AutomaticKind != "" {
		return fmt.Errorf("自动标签不能转为人物")
	}
	if strings.TrimSpace(tag.Namespace) != personTagNamespace {
		return fmt.Errorf("只有“人物”分类的标签可以转为人物")
	}
	_, _, err := validatePersonNames(tag.Name, "")
	return err
}

// Counts include soft-deleted media: their relationships must survive restoration.
func countTagPersonMedia(tx *gorm.DB, tagID uint) (int64, int64, error) {
	var videos, images int64
	if err := tx.Table("video_tags").Joins("JOIN videos ON videos.id = video_tags.video_id").
		Where("video_tags.tag_id = ?", tagID).Count(&videos).Error; err != nil {
		return 0, 0, err
	}
	if err := tx.Table("image_tags").Joins("JOIN images ON images.id = image_tags.image_id").
		Where("image_tags.tag_id = ?", tagID).Count(&images).Error; err != nil {
		return 0, 0, err
	}
	return videos, images, nil
}

func (s *TagService) PreviewTagPersonConversion(tagID uint) (*TagPersonConversionPreview, error) {
	if tagID == 0 {
		return nil, fmt.Errorf("请选择要转换的标签")
	}
	var tag models.Tag
	if err := database.DB.First(&tag, tagID).Error; err != nil {
		return nil, fmt.Errorf("读取标签失败: %w", err)
	}
	if err := validatePersonConversionTag(tag); err != nil {
		return nil, err
	}
	result := &TagPersonConversionPreview{TagID: tag.ID, TagName: tag.Name, People: []models.Person{}}
	var err error
	result.VideoCount, result.ImageCount, err = countTagPersonMedia(database.DB, tag.ID)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(tag.Name)
	if err := database.DB.Where("LOWER(display_name) = LOWER(?) OR LOWER(original_name) = LOWER(?)", name, name).
		Order("id").Find(&result.People).Error; err != nil {
		return nil, fmt.Errorf("查找同名人物失败: %w", err)
	}
	return result, nil
}

// ConvertTagToPerson transfers both kinds of media and removes the source tag
// atomically. A matching person is never chosen implicitly. The same transaction
// writes a tag_person_conversions record so the conversion can be undone.
func (s *TagService) ConvertTagToPerson(input TagPersonConversionRequest) (*TagPersonConversionResult, error) {
	if input.TagID == 0 || input.TagName == "" || input.CreateNew != (input.TargetPersonID == 0) {
		return nil, fmt.Errorf("请明确选择新建人物或关联已有的人物")
	}
	result := &TagPersonConversionResult{}
	err := database.Transaction(func(tx *gorm.DB) error {
		var tag models.Tag
		// PostgreSQL serializes conversions of the same tag; SQLite uses its
		// existing immediate write transaction and ignores this locking clause.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&tag, input.TagID).Error; err != nil {
			return fmt.Errorf("标签不存在或已经转换，请刷新后重试: %w", err)
		}
		if tag.Name != input.TagName {
			return fmt.Errorf("标签名称已变更，请重新打开转换窗口")
		}
		if err := validatePersonConversionTag(tag); err != nil {
			return err
		}
		var err error
		result.VideoCount, result.ImageCount, err = countTagPersonMedia(tx, tag.ID)
		if err != nil {
			return err
		}
		if input.CreateNew {
			result.Person = models.Person{DisplayName: strings.TrimSpace(tag.Name)}
			if err := tx.Create(&result.Person).Error; err != nil {
				return err
			}
		} else if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&result.Person, input.TargetPersonID).Error; err != nil {
			return fmt.Errorf("所选人物已不存在，请重新选择: %w", err)
		}
		videoIDs, err := tagPersonMediaIDs(tx, "video_tags", "video_id", "videos", tag.ID)
		if err != nil {
			return err
		}
		imageIDs, err := tagPersonMediaIDs(tx, "image_tags", "image_id", "images", tag.ID)
		if err != nil {
			return err
		}
		// 只记录本次真正新插入的人物关系，撤销时才不会删掉转换前就存在的关系。
		addedVideoIDs, err := insertNewPersonRelations(tx, "video_people", "video_id", result.Person.ID, videoIDs)
		if err != nil {
			return fmt.Errorf("关联视频失败: %w", err)
		}
		addedImageIDs, err := insertNewPersonRelations(tx, "image_people", "image_id", result.Person.ID, imageIDs)
		if err != nil {
			return fmt.Errorf("关联图片失败: %w", err)
		}
		record := models.TagPersonConversion{
			TagID: tag.ID, PersonID: result.Person.ID, PersonCreated: input.CreateNew,
			VideoIDsJSON: marshalIDList(videoIDs), ImageIDsJSON: marshalIDList(imageIDs),
			AddedVideoPersonIDsJSON: marshalIDList(addedVideoIDs), AddedImagePersonIDsJSON: marshalIDList(addedImageIDs),
			State: models.TagPersonConversionApplied,
		}
		if err := tx.Create(&record).Error; err != nil {
			return fmt.Errorf("写入转换记录失败: %w", err)
		}
		result.ConversionID = record.ID
		if err := deleteTagTx(tx, &tag); err != nil {
			return err
		}
		// 转换属于「转人物」：候选与待重排状态需要对账（人物分类的标签本不在词表内）。
		return resetAITaggingAfterLibraryChange(tx)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func marshalIDList(ids []uint) string {
	if ids == nil {
		ids = []uint{}
	}
	payload, _ := json.Marshal(ids)
	return string(payload)
}

func parseIDList(raw string) []uint {
	var ids []uint
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}

// tagPersonMediaIDs 与 countTagPersonMedia 同一口径：包含软删除媒体，它们的关系恢复后仍要在。
func tagPersonMediaIDs(tx *gorm.DB, relTable, mediaColumn, mediaTable string, tagID uint) ([]uint, error) {
	ids := []uint{}
	err := tx.Table(relTable).
		Joins("JOIN "+mediaTable+" ON "+mediaTable+".id = "+relTable+"."+mediaColumn).
		Where(relTable+".tag_id = ?", tagID).Order(relTable+"."+mediaColumn).
		Pluck(relTable+"."+mediaColumn, &ids).Error
	return ids, err
}

// insertNewPersonRelations 插入人物的关系并返回**本次真正新增**的媒体 ID：以 INSERT 的
// RowsAffected 为准（ON CONFLICT DO NOTHING 命中已有行时为 0），不先查再插——先查后插在
// 并发下会把别人刚加的关系记成本次新增，撤销时误删。
func insertNewPersonRelations(tx *gorm.DB, table, mediaColumn string, personID uint, mediaIDs []uint) ([]uint, error) {
	added := make([]uint, 0, len(mediaIDs))
	now := time.Now()
	for _, id := range mediaIDs {
		result := tx.Exec("INSERT INTO "+table+" ("+mediaColumn+", person_id, created_at) VALUES (?, ?, ?) ON CONFLICT ("+mediaColumn+", person_id) DO NOTHING",
			id, personID, now)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 1 {
			added = append(added, id)
		}
	}
	return added, nil
}

// SetAvatarRemover 注入删除托管头像文件的能力：撤销转换可能删掉新建的人物及其头像，
// 而 TagService 自己不持有托管图片目录。未注入时不动文件（只留下一张孤立头像）。
func (s *TagService) SetAvatarRemover(remove func(relativePath string) error) {
	s.avatarMu.Lock()
	defer s.avatarMu.Unlock()
	s.removeAvatar = remove
}

// SetAvatarRemoverIfUnset 只在尚未注入时注入。正式做法是构造/启动时注入一次（P-029 接线项）；
// 在那之前 App 的撤销入口用它兜底，既不改写已注入的实现，也不与撤销并发写同一个字段。
func (s *TagService) SetAvatarRemoverIfUnset(remove func(relativePath string) error) {
	s.avatarMu.Lock()
	defer s.avatarMu.Unlock()
	if s.removeAvatar == nil {
		s.removeAvatar = remove
	}
}

func (s *TagService) avatarRemover() func(relativePath string) error {
	s.avatarMu.RLock()
	defer s.avatarMu.RUnlock()
	return s.removeAvatar
}

// TagPersonConversionUndoResult 是撤销后的回执，供前端刷新标签与人物列表。
type TagPersonConversionUndoResult struct {
	ConversionID  uint       `json:"conversion_id"`
	Tag           models.Tag `json:"tag"`
	VideoCount    int64      `json:"video_count"`
	ImageCount    int64      `json:"image_count"`
	PersonDeleted bool       `json:"person_deleted"`
}

// UndoTagPersonConversion 完整复原一次「标签转人物」（D-PC34）：单一事务，任何一步失败整体回滚。
func (s *TagService) UndoTagPersonConversion(conversionID uint) (*TagPersonConversionUndoResult, error) {
	result := &TagPersonConversionUndoResult{ConversionID: conversionID}
	orphanAvatar := ""
	err := database.Transaction(func(tx *gorm.DB) error {
		var record models.TagPersonConversion
		if err := tx.First(&record, conversionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConversionNotFound
			}
			return err
		}
		// ① 条件更新抢占状态；输的一方（已撤销或并发撤销）拿到 0 行。
		claim := tx.Model(&models.TagPersonConversion{}).
			Where("id = ? AND state = ?", conversionID, models.TagPersonConversionApplied).
			Updates(map[string]any{"state": models.TagPersonConversionUndone, "undone_at": time.Now()})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			return ErrConversionNotApplied
		}
		// ② 还原标签。标签行仍是活跃的，说明期间有人用同名重新建过（会复活同一行）。
		var tag models.Tag
		if err := tx.Unscoped().First(&tag, record.TagID).Error; err != nil {
			// 标签行已不在：软删行被同名的新建或改名硬删了（updateTag 的既有行为），对撤销
			// 来说与「名字被占用」是同一件事，用契约内的错误码。
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTagNameTaken
			}
			return err
		}
		if !tag.DeletedAt.IsValid() {
			return ErrTagNameTaken
		}
		var taken int64
		if err := tx.Model(&models.Tag{}).Where("name = ? AND id <> ?", tag.Name, tag.ID).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return ErrTagNameTaken
		}
		tag.DeletedAt.Clear()
		if err := tx.Unscoped().Save(&tag).Error; err != nil {
			return err
		}
		result.Tag = tag
		// ③ 重新插入原打标关系，跳过已被永久删除的媒体。
		videoIDs, imageIDs := parseIDList(record.VideoIDsJSON), parseIDList(record.ImageIDsJSON)
		for _, chunk := range chunkUintIDs(videoIDs, conversionIDChunk) {
			if len(chunk) == 0 {
				continue
			}
			if err := tx.Exec(`INSERT INTO video_tags(video_id, tag_id) SELECT id, ? FROM videos WHERE id IN ? ON CONFLICT DO NOTHING`,
				tag.ID, chunk).Error; err != nil {
				return err
			}
		}
		for _, chunk := range chunkUintIDs(imageIDs, conversionIDChunk) {
			if len(chunk) == 0 {
				continue
			}
			if err := tx.Exec(`INSERT INTO image_tags(image_id, tag_id) SELECT id, ? FROM images WHERE id IN ? ON CONFLICT DO NOTHING`,
				tag.ID, chunk).Error; err != nil {
				return err
			}
		}
		result.VideoCount, result.ImageCount = int64(len(videoIDs)), int64(len(imageIDs))
		// ④ 删除本次新增的人物关系。
		for _, chunk := range chunkUintIDs(parseIDList(record.AddedVideoPersonIDsJSON), conversionIDChunk) {
			if len(chunk) == 0 {
				continue
			}
			if err := tx.Where("person_id = ? AND video_id IN ?", record.PersonID, chunk).Delete(&models.VideoPerson{}).Error; err != nil {
				return err
			}
		}
		for _, chunk := range chunkUintIDs(parseIDList(record.AddedImagePersonIDsJSON), conversionIDChunk) {
			if len(chunk) == 0 {
				continue
			}
			if err := tx.Where("person_id = ? AND image_id IN ?", record.PersonID, chunk).Delete(&models.ImagePerson{}).Error; err != nil {
				return err
			}
		}
		// ⑤ 新建的人物若已没有任何关系就一并删除；人物已被用户删掉则无事可做。
		if record.PersonCreated {
			var person models.Person
			err := tx.First(&person, record.PersonID).Error
			if err == nil {
				remaining, err := personHasRemainingRelations(tx, person.ID)
				if err != nil {
					return err
				}
				if !remaining {
					if err := deletePersonTx(tx, person.ID); err != nil {
						return err
					}
					result.PersonDeleted = true
					orphanAvatar = person.AvatarPath
				}
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		// ⑥ 词表对账。
		return resetAITaggingAfterLibraryChange(tx)
	})
	if err != nil {
		return nil, err
	}
	if remove := s.avatarRemover(); orphanAvatar != "" && remove != nil {
		if err := remove(orphanAvatar); err != nil {
			log.Printf("撤销标签转人物后清理头像失败: %v", err)
		}
	}
	return result, nil
}

// TagPersonConversionRecord 是「最近转换」列表的一行。
type TagPersonConversionRecord struct {
	ID            uint   `json:"id"`
	TagID         uint   `json:"tag_id"`
	TagName       string `json:"tag_name"`
	PersonID      uint   `json:"person_id"`
	PersonName    string `json:"person_name"`
	PersonCreated bool   `json:"person_created"`
	VideoCount    int    `json:"video_count"`
	ImageCount    int    `json:"image_count"`
	State         string `json:"state"`
	// Undoable 为 true 表示现在撤销能成功：记录仍是 applied，标签行还在回收态、且没有同名活跃标签。
	Undoable  bool       `json:"undoable"`
	CreatedAt time.Time  `json:"created_at" ts_type:"string"`
	UndoneAt  *time.Time `json:"undone_at" ts_type:"string"`
}

// ListTagPersonConversions 按时间倒序返回最近的转换记录（默认 20，上限 100）。
func (s *TagService) ListTagPersonConversions(limit int) ([]TagPersonConversionRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var rows []models.TagPersonConversion
	if err := database.DB.Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]TagPersonConversionRecord, 0, len(rows))
	for _, row := range rows {
		record := TagPersonConversionRecord{
			ID: row.ID, TagID: row.TagID, PersonID: row.PersonID, PersonCreated: row.PersonCreated,
			VideoCount: len(parseIDList(row.VideoIDsJSON)), ImageCount: len(parseIDList(row.ImageIDsJSON)),
			State: row.State, CreatedAt: row.CreatedAt, UndoneAt: row.UndoneAt,
		}
		var tag models.Tag
		if err := database.DB.Unscoped().Select("id", "name", "deleted_at").First(&tag, row.TagID).Error; err == nil {
			record.TagName = tag.Name
			if row.State == models.TagPersonConversionApplied && tag.DeletedAt.IsValid() {
				var taken int64
				if err := database.DB.Model(&models.Tag{}).Where("name = ? AND id <> ?", tag.Name, tag.ID).Count(&taken).Error; err == nil {
					record.Undoable = taken == 0
				}
			}
		}
		var person models.Person
		if err := database.DB.Select("id", "display_name").First(&person, row.PersonID).Error; err == nil {
			record.PersonName = person.DisplayName
		}
		records = append(records, record)
	}
	return records, nil
}
