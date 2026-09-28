package services

import (
	"fmt"
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
	Person     models.Person `json:"person"`
	VideoCount int64         `json:"video_count"`
	ImageCount int64         `json:"image_count"`
}

func validatePersonConversionTag(tag models.Tag) error {
	if tag.AutomaticKind != "" {
		return fmt.Errorf("自动标签不能转为人物")
	}
	if strings.TrimSpace(tag.Namespace) != "人物" {
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
// atomically. A matching person is never chosen implicitly.
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
		now := time.Now()
		if err := tx.Exec(`INSERT INTO video_people (video_id, person_id, created_at)
			SELECT video_tags.video_id, ?, ? FROM video_tags
			JOIN videos ON videos.id = video_tags.video_id WHERE video_tags.tag_id = ?
			ON CONFLICT (video_id, person_id) DO NOTHING`, result.Person.ID, now, tag.ID).Error; err != nil {
			return fmt.Errorf("关联视频失败: %w", err)
		}
		if err := tx.Exec(`INSERT INTO image_people (image_id, person_id, created_at)
			SELECT image_tags.image_id, ?, ? FROM image_tags
			JOIN images ON images.id = image_tags.image_id WHERE image_tags.tag_id = ?
			ON CONFLICT (image_id, person_id) DO NOTHING`, result.Person.ID, now, tag.ID).Error; err != nil {
			return fmt.Errorf("关联图片失败: %w", err)
		}
		return deleteTagTx(tx, &tag)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
