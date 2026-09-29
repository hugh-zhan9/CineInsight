package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TagService struct {
	// removeAvatar 由 App 接线时经 SetAvatarRemover 注入，撤销转换删除新建人物时清理头像文件。
	// avatarMu 保护它：注入与撤销可能在不同 goroutine 里。
	avatarMu     sync.RWMutex
	removeAvatar func(relativePath string) error
}

// isAITagEligible deliberately ignores the legacy type and activation fields.
// 「人物」分类的标签是人名，不进入 AI 词表（D-PC28 规则 6）。
func isAITagEligible(tag models.Tag) bool {
	return tag.AutomaticKind == "" && !tag.DeletedAt.IsValid() &&
		strings.TrimSpace(tag.Namespace) != personTagNamespace
}

// aiVocabularyChanged 判断一次标签编辑是否改变了 AI 可用词表的内容（D-PC28 规则 4）：
// 标签进出词表，或在词表内改了名字或分类。只改颜色不算。
func aiVocabularyChanged(before, after models.Tag) bool {
	eligibleBefore, eligibleAfter := isAITagEligible(before), isAITagEligible(after)
	if !eligibleBefore && !eligibleAfter {
		return false
	}
	return eligibleBefore != eligibleAfter || before.Name != after.Name ||
		strings.TrimSpace(before.Namespace) != strings.TrimSpace(after.Namespace)
}

// aiTagMatcher preserves exact names when legacy labels normalize to the same word.
type aiTagMatcher struct {
	exact      map[string]models.Tag
	normalized map[string]models.Tag
}

func newAITagMatcher(tags []models.Tag) aiTagMatcher {
	m := aiTagMatcher{exact: make(map[string]models.Tag), normalized: make(map[string]models.Tag)}
	for _, tag := range tags {
		if !isAITagEligible(tag) {
			continue
		}
		m.exact[tag.Name] = tag
		key := normalizeAITagName(tag.Name)
		if previous, exists := m.normalized[key]; exists && previous.ID != tag.ID {
			m.normalized[key] = models.Tag{} // Ambiguous: require an exact name.
		} else if !exists {
			m.normalized[key] = tag
		}
	}
	return m
}

func (m aiTagMatcher) match(names ...string) (models.Tag, bool) {
	for _, name := range names {
		if tag, ok := m.exact[strings.TrimSpace(name)]; ok {
			return tag, true
		}
	}
	for _, name := range names {
		if tag, ok := m.normalized[normalizeAITagName(name)]; ok && tag.ID != 0 {
			return tag, true
		}
	}
	return models.Tag{}, false
}

var ErrAITagLibraryEmptyConfirmationRequired = errors.New("AI 标签库非空，拒绝未经确认的空保存")

const (
	ShortVideoTagName             = "短视频"
	LowResolutionTagName          = "低清"
	shortVideoAutomaticTagKind    = "short_video"
	lowResolutionAutomaticTagKind = "low_resolution"
	automaticTagNamespace         = "自动"
)

type MergeTagsResult struct {
	TargetTagID     uint `json:"target_tag_id"`
	MergedTagCount  int  `json:"merged_tag_count"`
	VideoLinksMoved int  `json:"video_links_moved"`
	ImageLinksMoved int  `json:"image_links_moved"`
}

type ShortVideoTagSyncResult struct {
	TagID   uint  `json:"tag_id"`
	Added   int64 `json:"added"`
	Removed int64 `json:"removed"`
}

// GetAITagLibrary reads the legacy library selection for existing bindings.
// AI inference uses all non-automatic tags, independently of these legacy flags.
func (s *TagService) GetAITagLibrary() ([]models.Tag, error) {
	var tags []models.Tag
	err := database.DB.Where("is_system = ?", true).
		Order("namespace asc, sort_order asc, id asc").
		Find(&tags).Error
	return tags, err
}

func (s *TagService) SaveAITagLibrary(inputs []AITagLibraryInput) ([]models.Tag, error) {
	return s.saveAITagLibrary(inputs, false)
}

// ClearAITagLibrary is the explicit destructive counterpart to SaveAITagLibrary.
// Keeping the empty operation separate prevents a failed/stale client load from
// being interpreted as an intentional deletion of the entire AI tag library.
func (s *TagService) ClearAITagLibrary() ([]models.Tag, error) {
	return s.saveAITagLibrary(nil, true)
}

func (s *TagService) saveAITagLibrary(inputs []AITagLibraryInput, allowEmpty bool) ([]models.Tag, error) {
	normalizedNames := make(map[string]struct{}, len(inputs))
	for i := range inputs {
		inputs[i].Name = strings.TrimSpace(inputs[i].Name)
		inputs[i].Namespace = strings.TrimSpace(inputs[i].Namespace)
		inputs[i].Color = strings.TrimSpace(inputs[i].Color)
		if inputs[i].Name == "" {
			return nil, fmt.Errorf("标签名称不能为空")
		}
		normalized := normalizeAITagName(inputs[i].Name)
		if _, exists := normalizedNames[normalized]; exists {
			return nil, fmt.Errorf("标签名称重复: %s", inputs[i].Name)
		}
		normalizedNames[normalized] = struct{}{}
	}

	err := database.Transaction(func(tx *gorm.DB) error {
		var existingSystemTags []models.Tag
		if err := tx.Where("is_system = ?", true).Find(&existingSystemTags).Error; err != nil {
			return err
		}
		if len(inputs) == 0 && len(existingSystemTags) > 0 && !allowEmpty {
			return ErrAITagLibraryEmptyConfirmationRequired
		}
		submittedIDs := make(map[uint]struct{}, len(inputs))
		namespaceOrders := make(map[string]int)
		libraryChanged := false

		for _, input := range inputs {
			namespaceOrders[input.Namespace]++
			color := input.Color
			if color == "" {
				color = tagColorPalette[len(submittedIDs)%len(tagColorPalette)]
			}
			var tag models.Tag
			if input.ID > 0 {
				if err := tx.Unscoped().First(&tag, input.ID).Error; err != nil {
					return err
				}
				if tag.AutomaticKind != "" {
					return fmt.Errorf("自动标签不能加入 AI 标签库: %s", tag.Name)
				}
				var conflict models.Tag
				err := tx.Unscoped().Where("name = ? AND id <> ?", input.Name, tag.ID).First(&conflict).Error
				if err == nil {
					if conflict.AutomaticKind != "" {
						return fmt.Errorf("自动标签不能加入 AI 标签库: %s", conflict.Name)
					}
					if conflict.IsSystem {
						return fmt.Errorf("标签名称已存在于 AI 标签库: %s", input.Name)
					}
					// Reuse the existing manual tag instead of renaming over it. This
					// preserves all of its current video relationships when users add
					// that name to the AI library from an existing library row.
					tag = conflict
				}
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					if err != nil {
						return err
					}
				}
			} else {
				err := tx.Unscoped().Where("name = ?", input.Name).First(&tag).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if errors.Is(err, gorm.ErrRecordNotFound) {
					tag = models.Tag{Name: input.Name}
				}
			}
			if tag.AutomaticKind != "" {
				return fmt.Errorf("自动标签不能加入 AI 标签库: %s", tag.Name)
			}
			if input.Namespace == automaticTagNamespace && tag.Namespace != automaticTagNamespace {
				return fmt.Errorf("自动是系统分类，不能手动分配")
			}

			sortOrder := namespaceOrders[input.Namespace]
			nameChanged := tag.ID != 0 && tag.Name != input.Name
			changed := tag.ID == 0 ||
				tag.Name != input.Name ||
				tag.Namespace != input.Namespace ||
				tag.Color != color ||
				!tag.IsSystem ||
				tag.IsActive != input.IsActive ||
				tag.ReviewRequired != input.ReviewRequired ||
				tag.SortOrder != sortOrder ||
				tag.DeletedAt.IsValid()
			if changed {
				isNew := tag.ID == 0
				tag.Name = input.Name
				tag.Namespace = input.Namespace
				tag.Color = color
				tag.IsSystem = true
				tag.IsActive = input.IsActive
				tag.ReviewRequired = input.ReviewRequired
				tag.SortOrder = sortOrder
				tag.DeletedAt.Clear()
				if err := tx.Unscoped().Save(&tag).Error; err != nil {
					return err
				}
				if isNew && !input.IsActive {
					if err := tx.Model(&tag).UpdateColumn("is_active", false).Error; err != nil {
						return err
					}
					tag.IsActive = false
				}
				if nameChanged {
					if err := tx.Model(&models.AITagCandidate{}).
						Where("matched_tag_id = ? AND status = ?", tag.ID, models.AITagCandidateStatusPending).
						Updates(map[string]interface{}{
							"suggested_name":  tag.Name,
							"normalized_name": normalizeAITagName(tag.Name),
						}).Error; err != nil {
						return err
					}
					// 图片侧沿用改名作废策略：名称属于证据指纹，下一轮按新名称提出候选。
					if err := tx.Model(&models.ImageAITagCandidate{}).
						Where("matched_tag_id = ? AND status = ?", tag.ID, models.AITagCandidateStatusPending).
						Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
						return err
					}
				}
				libraryChanged = true
			}
			submittedIDs[tag.ID] = struct{}{}
		}

		for _, tag := range existingSystemTags {
			if _, submitted := submittedIDs[tag.ID]; submitted {
				continue
			}
			if err := tx.Model(&tag).Updates(map[string]interface{}{
				"is_system":       false,
				"is_active":       true,
				"review_required": false,
				"sort_order":      0,
			}).Error; err != nil {
				return err
			}
			libraryChanged = true
		}
		if !libraryChanged {
			return nil
		}
		return resetAITaggingAfterLibraryChange(tx)
	})
	if err != nil {
		return nil, err
	}
	return s.GetAITagLibrary()
}

func resetAITaggingAfterLibraryChange(tx *gorm.DB) error {
	if err := tx.Model(&models.AITagCandidate{}).
		Where("status = ?", models.AITagCandidateStatusPending).
		Where(`matched_tag_id IS NULL OR NOT EXISTS (
				SELECT 1 FROM tags
				WHERE tags.id = ai_tag_candidates.matched_tag_id
					AND tags.deleted_at IS NULL
					AND COALESCE(tags.automatic_kind, '') = ''
					AND TRIM(COALESCE(tags.namespace, '')) <> ?
			)`, personTagNamespace).
		Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
		return err
	}
	// 图片侧同理：标签被删除或成为自动标签后，指向它的待审候选失效。
	if err := tx.Model(&models.ImageAITagCandidate{}).
		Where("status = ?", models.AITagCandidateStatusPending).
		Where(`matched_tag_id IS NULL OR NOT EXISTS (
				SELECT 1 FROM tags
				WHERE tags.id = image_ai_tag_candidates.matched_tag_id
					AND tags.deleted_at IS NULL
					AND COALESCE(tags.automatic_kind, '') = ''
					AND TRIM(COALESCE(tags.namespace, '')) <> ?
			)`, personTagNamespace).
		Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
		return err
	}
	if err := tx.Model(&models.AITaggingState{}).
		Where(`NOT EXISTS (
				SELECT 1 FROM video_tags
				INNER JOIN tags ON tags.id = video_tags.tag_id
				WHERE video_tags.video_id = ai_tagging_states.video_id
					AND COALESCE(tags.automatic_kind, '') = ''
			)`).
		Updates(map[string]interface{}{
			"status":               models.AITaggingStateStatusPending,
			"skip_reason":          "",
			"evidence_fingerprint": "",
			"last_error":           "",
		}).Error; err != nil {
		return err
	}
	return nil
}

// Category names are derived from tags; no empty category is persisted.
func (s *TagService) CreateTagCategory(name string, tagIDs []uint) error {
	name = strings.TrimSpace(name)
	if name == "" || len(tagIDs) == 0 {
		return fmt.Errorf("分类名称和至少一个标签不能为空")
	}
	if name == automaticTagNamespace {
		return fmt.Errorf("自动是系统分类，不能手动创建")
	}
	return database.Transaction(func(tx *gorm.DB) error {
		if exists, err := tagCategoryExists(tx, name); err != nil {
			return err
		} else if exists {
			return fmt.Errorf("分类已存在: %s", name)
		}
		ids := make(map[uint]struct{}, len(tagIDs))
		for _, id := range tagIDs {
			if id == 0 {
				return fmt.Errorf("标签不存在")
			}
			ids[id] = struct{}{}
		}
		var tags []models.Tag
		if err := tx.Where("id IN ?", tagIDs).Find(&tags).Error; err != nil {
			return err
		}
		if len(tags) != len(ids) {
			return fmt.Errorf("标签不存在")
		}
		vocabularyChanged := false
		for _, tag := range tags {
			if tag.AutomaticKind != "" {
				return fmt.Errorf("自动标签不能加入分类")
			}
			after := tag
			after.Namespace = name
			vocabularyChanged = vocabularyChanged || aiVocabularyChanged(tag, after)
		}
		if err := tx.Model(&models.Tag{}).Where("id IN ?", tagIDs).Update("namespace", name).Error; err != nil {
			return err
		}
		if !vocabularyChanged {
			return nil
		}
		return resetAITaggingAfterLibraryChange(tx)
	})
}

func (s *TagService) RenameTagCategory(oldName, newName string) error {
	oldName, newName = strings.TrimSpace(oldName), strings.TrimSpace(newName)
	if oldName == "" || newName == "" || oldName == newName {
		return fmt.Errorf("分类名称无效")
	}
	if oldName == automaticTagNamespace || newName == automaticTagNamespace {
		return fmt.Errorf("自动是系统分类，不能手动改名")
	}
	return s.changeTagCategory(oldName, newName, false)
}

func (s *TagService) DeleteTagCategory(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("分类名称不能为空")
	}
	if name == automaticTagNamespace {
		return fmt.Errorf("自动是系统分类，不能手动删除")
	}
	return s.changeTagCategory(name, "", true)
}

func (s *TagService) changeTagCategory(oldName, newName string, deleting bool) error {
	return database.Transaction(func(tx *gorm.DB) error {
		var tags []models.Tag
		if err := tx.Where("namespace = ? AND COALESCE(automatic_kind, '') = ''", oldName).Find(&tags).Error; err != nil {
			return err
		}
		if len(tags) == 0 {
			return fmt.Errorf("分类不存在: %s", oldName)
		}
		if !deleting {
			if exists, err := tagCategoryExists(tx, newName); err != nil {
				return err
			} else if exists {
				return fmt.Errorf("分类已存在: %s", newName)
			}
		}
		ids := make([]uint, 0, len(tags))
		vocabularyChanged := false
		for _, tag := range tags {
			ids = append(ids, tag.ID)
			after := tag
			after.Namespace = newName
			vocabularyChanged = vocabularyChanged || aiVocabularyChanged(tag, after)
		}
		if err := tx.Model(&models.Tag{}).Where("id IN ?", ids).Update("namespace", newName).Error; err != nil {
			return err
		}
		if !vocabularyChanged {
			return nil
		}
		return resetAITaggingAfterLibraryChange(tx)
	})
}

func tagCategoryExists(tx *gorm.DB, name string) (bool, error) {
	var count int64
	err := tx.Model(&models.Tag{}).Where("LOWER(namespace) = LOWER(?) AND COALESCE(automatic_kind, '') = ''", name).Count(&count).Error
	return count > 0, err
}

// GetAllTags 获取所有标签
func (s *TagService) GetAllTags() ([]models.Tag, error) {
	var tags []models.Tag
	err := database.DB.Order("name").Find(&tags).Error
	return tags, err
}

// GetImageTags 仅返回实际关联过图片的标签（image_tags 中出现过的 tag），
// 供图片库筛选栏使用，避免展示只被视频使用的标签。
func (s *TagService) GetImageTags() ([]models.Tag, error) {
	var tags []models.Tag
	err := database.DB.
		Joins("JOIN image_tags ON image_tags.tag_id = tags.id").
		Group("tags.id").
		Order("tags.name").
		Find(&tags).Error
	return tags, err
}

// MergeTags retains targetTagID, unions all video and recommendation links into
// it, rewrites AI history references, and soft-deletes the source tags.
func (s *TagService) MergeTags(sourceTagIDs []uint, targetTagID uint) (*MergeTagsResult, error) {
	uniqueSources := make([]uint, 0, len(sourceTagIDs))
	seen := make(map[uint]struct{}, len(sourceTagIDs))
	for _, id := range sourceTagIDs {
		if id == 0 || id == targetTagID {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		uniqueSources = append(uniqueSources, id)
	}
	if targetTagID == 0 || len(uniqueSources) == 0 {
		return nil, fmt.Errorf("请选择目标标签和至少一个待合并标签")
	}

	result := &MergeTagsResult{TargetTagID: targetTagID, MergedTagCount: len(uniqueSources)}
	err := database.Transaction(func(tx *gorm.DB) error {
		var target models.Tag
		if err := tx.First(&target, targetTagID).Error; err != nil {
			return fmt.Errorf("目标标签不存在: %w", err)
		}
		var sources []models.Tag
		if err := tx.Where("id IN ?", uniqueSources).Find(&sources).Error; err != nil {
			return err
		}
		if len(sources) != len(uniqueSources) {
			return fmt.Errorf("部分待合并标签不存在")
		}
		if target.AutomaticKind != "" {
			return fmt.Errorf("自动标签不能作为合并目标")
		}
		for _, source := range sources {
			if source.AutomaticKind != "" {
				return fmt.Errorf("自动标签不能合并: %s", source.Name)
			}
		}

		insertResult := tx.Exec(`
			INSERT INTO video_tags(video_id, tag_id)
			SELECT video_id, ? FROM video_tags WHERE tag_id IN ?
			ON CONFLICT DO NOTHING
		`, targetTagID, uniqueSources)
		if insertResult.Error != nil {
			return insertResult.Error
		}
		result.VideoLinksMoved = int(insertResult.RowsAffected)
		if err := tx.Exec("DELETE FROM video_tags WHERE tag_id IN ?", uniqueSources).Error; err != nil {
			return err
		}

		// D-002: 图片侧关联同步改写到目标标签并按 (image_id, tag_id) 唯一键去重。
		imageInsertResult := tx.Exec(`
			INSERT INTO image_tags(image_id, tag_id)
			SELECT image_id, ? FROM image_tags WHERE tag_id IN ?
			ON CONFLICT DO NOTHING
		`, targetTagID, uniqueSources)
		if imageInsertResult.Error != nil {
			return imageInsertResult.Error
		}
		result.ImageLinksMoved = int(imageInsertResult.RowsAffected)
		if err := tx.Exec("DELETE FROM image_tags WHERE tag_id IN ?", uniqueSources).Error; err != nil {
			return err
		}

		pendingCandidates := tx.Model(&models.AITagCandidate{}).
			Where("matched_tag_id IN ? AND status = ?", uniqueSources, models.AITagCandidateStatusPending)
		if err := pendingCandidates.Updates(map[string]interface{}{
			"suggested_name":  target.Name,
			"normalized_name": normalizeAITagName(target.Name),
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.AITagCandidate{}).
			Where("matched_tag_id IN ?", uniqueSources).
			Update("matched_tag_id", targetTagID).Error; err != nil {
			return err
		}
		approvalTagIDs := append([]uint{targetTagID}, uniqueSources...)
		var approvals []models.AITagApprovalRecord
		if err := tx.Where("tag_id IN ?", approvalTagIDs).Order("id").Find(&approvals).Error; err != nil {
			return err
		}
		keptApprovalByVideo := make(map[uint]models.AITagApprovalRecord)
		for _, approval := range approvals {
			kept, exists := keptApprovalByVideo[approval.VideoID]
			if !exists || (kept.TagID != targetTagID && approval.TagID == targetTagID) {
				keptApprovalByVideo[approval.VideoID] = approval
			}
		}
		deleteApprovalIDs := make([]uint, 0)
		for _, approval := range approvals {
			if keptApprovalByVideo[approval.VideoID].ID != approval.ID {
				deleteApprovalIDs = append(deleteApprovalIDs, approval.ID)
			}
		}
		if len(deleteApprovalIDs) > 0 {
			if err := tx.Where("id IN ?", deleteApprovalIDs).Delete(&models.AITagApprovalRecord{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&models.AITagApprovalRecord{}).
			Where("tag_id IN ?", uniqueSources).
			Update("tag_id", targetTagID).Error; err != nil {
			return err
		}

		var sourcePreferenceScore float64
		if err := tx.Model(&models.ShortFeedTagPreference{}).
			Where("tag_id IN ?", uniqueSources).
			Select("COALESCE(SUM(score), 0)").
			Scan(&sourcePreferenceScore).Error; err != nil {
			return err
		}
		if sourcePreferenceScore != 0 {
			var targetPreference models.ShortFeedTagPreference
			err := tx.Where("tag_id = ?", targetTagID).First(&targetPreference).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				targetPreference = models.ShortFeedTagPreference{TagID: targetTagID, Score: sourcePreferenceScore}
				if err := tx.Create(&targetPreference).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else if err := tx.Model(&targetPreference).Update("score", targetPreference.Score+sourcePreferenceScore).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("tag_id IN ?", uniqueSources).Delete(&models.ShortFeedTagPreference{}).Error; err != nil {
			return err
		}
		if err := rewriteSavedViewTagIDsTx(tx, uniqueSources, targetTagID); err != nil {
			return err
		}
		if err := tx.Where("id IN ?", uniqueSources).Delete(&models.Tag{}).Error; err != nil {
			return err
		}
		vocabularyChanged := isAITagEligible(target)
		for _, source := range sources {
			vocabularyChanged = vocabularyChanged || isAITagEligible(source)
		}
		if !vocabularyChanged {
			return nil
		}
		return resetAITaggingAfterLibraryChange(tx)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *TagService) SyncShortVideoTags() (*ShortVideoTagSyncResult, error) {
	result := &ShortVideoTagSyncResult{}
	err := database.Transaction(func(tx *gorm.DB) error {
		return syncShortVideoTagsWithResult(tx, result)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func syncShortVideoTags(tx *gorm.DB) error {
	return syncShortVideoTagsWithResult(tx, &ShortVideoTagSyncResult{})
}

func syncShortVideoTagsWithResult(tx *gorm.DB, result *ShortVideoTagSyncResult) error {
	maxDurationSeconds, err := shortVideoMaxDurationSeconds(tx)
	if err != nil {
		return err
	}
	shortTag, err := ensureShortVideoAutomaticTag(tx, true)
	if err != nil {
		return err
	}
	lowTag, err := ensureLowResolutionAutomaticTag(tx, true)
	if err != nil {
		return err
	}
	result.TagID = shortTag.ID
	result.Added, result.Removed, err = syncAutomaticTagBulk(tx, shortTag, "v.is_stale = ? AND v.duration > ? AND v.duration < ?", false, 0, maxDurationSeconds)
	if err != nil {
		return err
	}
	_, _, err = syncAutomaticTagBulk(tx, lowTag, lowResolutionEligibility, false, 0, 0, 1920, 1920, 1080, 1080)
	return err
}

// lowResolutionEligibility 与 isLowResolutionVideo 同义（D-PC36）：
// w>0 && h>0 && max(w,h)<1920 && min(w,h)<1080，宽银幕 1080p（1920×800）不算低清。
const lowResolutionEligibility = "v.is_stale = ? AND v.width > ? AND v.height > ? AND v.width < ? AND v.height < ? AND (v.width < ? OR v.height < ?)"

func isLowResolutionVideo(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	return max(width, height) < 1920 && min(width, height) < 1080
}

// Sync one rule without changing rows for which the user recorded a decision.
func syncAutomaticTagBulk(tx *gorm.DB, tag *models.Tag, eligibility string, args ...interface{}) (int64, int64, error) {
	addArgs := append([]interface{}{tag.ID, tag.AutomaticKind, true}, args...)
	added := tx.Exec(`
        INSERT INTO video_tags(video_id, tag_id)
        SELECT v.id, ? FROM videos v
        LEFT JOIN video_automatic_tag_overrides o ON o.video_id = v.id AND o.automatic_kind = ?
        WHERE v.deleted_at IS NULL AND ((o.id IS NOT NULL AND o.present = ?) OR (o.id IS NULL AND (`+eligibility+`)))
        ON CONFLICT DO NOTHING
    `, addArgs...)
	if added.Error != nil {
		return 0, 0, added.Error
	}
	removeArgs := append([]interface{}{tag.ID, tag.AutomaticKind, false}, args...)
	removed := tx.Exec(`
        DELETE FROM video_tags WHERE tag_id = ? AND video_id IN (
            SELECT v.id FROM videos v
            LEFT JOIN video_automatic_tag_overrides o ON o.video_id = v.id AND o.automatic_kind = ?
            WHERE v.deleted_at IS NULL AND ((o.id IS NOT NULL AND o.present = ?) OR (o.id IS NULL AND NOT (`+eligibility+`)))
        )
    `, removeArgs...)
	if removed.Error != nil {
		return 0, 0, removed.Error
	}
	return added.RowsAffected, removed.RowsAffected, nil
}

func syncShortVideoTagForVideo(tx *gorm.DB, videoID uint) error {
	maxDurationSeconds, err := shortVideoMaxDurationSeconds(tx)
	if err != nil {
		return err
	}
	var video models.Video
	if err := tx.First(&video, videoID).Error; err != nil {
		return err
	}
	shortTag, err := ensureShortVideoAutomaticTag(tx, true)
	if err != nil {
		return err
	}
	lowTag, err := ensureLowResolutionAutomaticTag(tx, true)
	if err != nil {
		return err
	}
	if err := syncAutomaticTagForVideo(tx, &video, shortTag, !video.IsStale && video.Duration > 0 && video.Duration < maxDurationSeconds); err != nil {
		return err
	}
	return syncAutomaticTagForVideo(tx, &video, lowTag, !video.IsStale && isLowResolutionVideo(video.Width, video.Height))
}

func syncAutomaticTagForVideo(tx *gorm.DB, video *models.Video, tag *models.Tag, eligible bool) error {
	var override models.VideoAutomaticTagOverride
	err := tx.Where("video_id = ? AND automatic_kind = ?", video.ID, tag.AutomaticKind).First(&override).Error
	if err == nil {
		eligible = override.Present
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if eligible {
		return tx.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING", video.ID, tag.ID).Error
	}
	return tx.Exec("DELETE FROM video_tags WHERE video_id = ? AND tag_id = ?", video.ID, tag.ID).Error
}

func ensureShortVideoAutomaticTag(tx *gorm.DB, create bool) (*models.Tag, error) {
	return ensureAutomaticTag(tx, shortVideoAutomaticTagKind, ShortVideoTagName, tagColorPalette[0], create)
}

func ensureLowResolutionAutomaticTag(tx *gorm.DB, create bool) (*models.Tag, error) {
	return ensureAutomaticTag(tx, lowResolutionAutomaticTagKind, LowResolutionTagName, tagColorPalette[1], create)
}

func ensureAutomaticTag(tx *gorm.DB, kind, name, color string, create bool) (*models.Tag, error) {
	var tag models.Tag
	err := tx.Unscoped().Where("automatic_kind = ?", kind).Order("id").First(&tag).Error
	if err == nil {
		if err := reserveAutomaticTagName(tx, name, tag.ID); err != nil {
			return nil, err
		}
		if tag.Name != name {
			tag.Name = name
			if err := tx.Unscoped().Model(&tag).Update("name", name).Error; err != nil {
				return nil, err
			}
		}
		if tag.DeletedAt.IsValid() && create {
			tag.DeletedAt.Clear()
			tag.IsActive = true
			if err := tx.Unscoped().Save(&tag).Error; err != nil {
				return nil, err
			}
		}
		if tag.DeletedAt.IsValid() {
			return nil, nil
		}
		return &tag, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if !create {
		return nil, nil
	}
	for attempts := 0; attempts < 5; attempts++ {
		if err := reserveAutomaticTagName(tx, name, 0); err != nil {
			return nil, err
		}
		tag = models.Tag{Name: name, Color: color, Namespace: automaticTagNamespace, AutomaticKind: kind, IsActive: true}
		createResult := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&tag)
		if createResult.Error != nil {
			return nil, createResult.Error
		}
		if createResult.RowsAffected == 1 {
			return &tag, nil
		}
		if err := tx.Where("automatic_kind = ?", kind).First(&tag).Error; err == nil {
			return &tag, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("创建%s自动标签时发生并发冲突", name)
}

func reserveAutomaticTagName(tx *gorm.DB, name string, automaticTagID uint) error {
	var conflict models.Tag
	err := tx.Unscoped().Where("name = ? AND id <> ?", name, automaticTagID).First(&conflict).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	replacement, err := availableTagName(tx, name+"（原标签）", conflict.ID)
	if err != nil {
		return err
	}
	return tx.Unscoped().Model(&conflict).Update("name", replacement).Error
}

func availableTagName(tx *gorm.DB, preferred string, excludeID uint) (string, error) {
	for index := 0; ; index++ {
		candidate := preferred
		if index > 0 {
			candidate = fmt.Sprintf("%s %d", preferred, index+1)
		}
		var count int64
		if err := tx.Unscoped().Model(&models.Tag{}).Where("name = ? AND id <> ?", candidate, excludeID).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
	}
}

func shortVideoMaxDurationSeconds(tx *gorm.DB) (float64, error) {
	var settings models.Settings
	if err := tx.Select("short_feed_max_duration_minutes").First(&settings).Error; err != nil {
		return 0, err
	}
	minutes := settings.ShortFeedMaxDurationMinutes
	if minutes <= 0 {
		minutes = DefaultShortFeedMaxDurationMinutes
	}
	return float64(minutes * 60), nil
}

// 预设标签调色板（视觉和谐的 12 色）
var tagColorPalette = []string{
	"#0D9488", // 品牌青
	"#3b82f6", // 蓝
	"#ef4444", // 红
	"#10b981", // 绿
	"#f59e0b", // 琥珀
	"#8b5cf6", // 紫
	"#ec4899", // 粉
	"#06b6d4", // 青
	"#f97316", // 橙
	"#6366f1", // 靛蓝
	"#14b8a6", // 蓝绿
	"#e11d48", // 玫红
	"#84cc16", // 黄绿
}

// CreateTag 创建标签
func (s *TagService) CreateTag(name, color string) (*models.Tag, error) {
	return s.createTag(name, color, nil)
}

func (s *TagService) CreateTagWithCategory(name, color, category string) (*models.Tag, error) {
	category = strings.TrimSpace(category)
	if category == automaticTagNamespace {
		return nil, fmt.Errorf("自动是系统分类，不能手动分配")
	}
	return s.createTag(name, color, &category)
}

func (s *TagService) createTag(name, color string, category *string) (*models.Tag, error) {
	var tag *models.Tag
	err := database.Transaction(func(tx *gorm.DB) error {
		var err error
		tag, err = createTagInTx(tx, name, color, category)
		if err != nil {
			return err
		}
		if !isAITagEligible(*tag) {
			return nil
		}
		return resetAITaggingAfterLibraryChange(tx)
	})
	return tag, err
}

func createTagInTx(tx *gorm.DB, name, color string, category *string) (*models.Tag, error) {
	// 先检查是否存在活跃的同名标签
	var existing models.Tag
	if err := tx.Where("name = ?", name).First(&existing).Error; err == nil {
		return &existing, ErrTagExists
	}

	// 颜色为空时自动分配
	if color == "" {
		var count int64
		tx.Model(&models.Tag{}).Count(&count)
		color = tagColorPalette[int(count)%len(tagColorPalette)]
	}

	// 检查是否存在被软删除的同名标签，如果有则恢复
	var softDeleted models.Tag
	if err := tx.Unscoped().Where("name = ? AND deleted_at IS NOT NULL", name).First(&softDeleted).Error; err == nil {
		// 恢复软删除的标签
		softDeleted.Color = color
		if category != nil {
			softDeleted.Namespace = *category
		}
		softDeleted.IsActive = true
		softDeleted.DeletedAt.Clear()
		if err := tx.Unscoped().Save(&softDeleted).Error; err != nil {
			log.Printf("恢复软删除标签失败: name=%s err=%v", name, err)
			return nil, err
		}
		log.Printf("恢复软删除标签: id=%d name=%s", softDeleted.ID, name)
		return &softDeleted, nil
	}

	tag := &models.Tag{
		Name:     name,
		Color:    color,
		IsActive: true,
	}
	if category != nil {
		tag.Namespace = *category
	}
	err := tx.Create(tag).Error
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return tag, ErrTagExists
	}
	return tag, err
}

// UpdateTag 更新标签
func (s *TagService) UpdateTag(id uint, name, color string) error {
	return s.updateTag(id, name, color, nil)
}

// UpdateTagWithCategory edits a tag's display category together with
// its name and color, so the tag manager saves the row as one operation.
func (s *TagService) UpdateTagWithCategory(id uint, name, color, category string) error {
	category = strings.TrimSpace(category)
	return s.updateTag(id, name, color, &category)
}

func (s *TagService) updateTag(id uint, name, color string, category *string) error {
	return database.Transaction(func(tx *gorm.DB) error {
		var current models.Tag
		if err := tx.First(&current, id).Error; err != nil {
			return err
		}
		if current.AutomaticKind != "" {
			return fmt.Errorf("自动标签由应用维护，不能手动修改")
		}
		if category != nil && *category == automaticTagNamespace && current.Namespace != automaticTagNamespace {
			return fmt.Errorf("自动是系统分类，不能手动分配")
		}
		// 检查是否存在同名的活跃标签（排除自身）
		var existing models.Tag
		if err := tx.Where("name = ? AND id != ?", name, id).First(&existing).Error; err == nil {
			return ErrTagExists
		}

		// 如果存在被软删除的同名标签，先彻底删除它以避免唯一约束冲突
		if err := tx.Unscoped().Where("name = ? AND deleted_at IS NOT NULL", name).Delete(&models.Tag{}).Error; err != nil {
			return err
		}

		updates := map[string]interface{}{
			"name":  name,
			"color": color,
		}
		if category != nil {
			updates["namespace"] = *category
		}
		if err := tx.Model(&models.Tag{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		if current.Name != name {
			if err := tx.Model(&models.AITagCandidate{}).Where("matched_tag_id = ? AND status = ?", id, models.AITagCandidateStatusPending).Updates(map[string]interface{}{
				"suggested_name": name, "normalized_name": normalizeAITagName(name),
			}).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.ImageAITagCandidate{}).Where("matched_tag_id = ? AND status = ?", id, models.AITagCandidateStatusPending).Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
				return err
			}
		}
		after := current
		after.Name = name
		if category != nil {
			after.Namespace = *category
		}
		if !aiVocabularyChanged(current, after) {
			return nil
		}
		return resetAITaggingAfterLibraryChange(tx)
	})
}

// DeleteTag 删除标签
func (s *TagService) DeleteTag(id uint) error {
	return database.Transaction(func(tx *gorm.DB) error {
		var tag models.Tag
		if err := tx.First(&tag, id).Error; err != nil {
			log.Printf("删除标签失败: 未找到 id=%d err=%v", id, err)
			return err
		}
		return deleteTagTx(tx, &tag)
	})
}

// Shared by explicit deletion and tag-to-person conversion inside their transaction.
func deleteTagTx(tx *gorm.DB, tag *models.Tag) error {
	if tag.AutomaticKind != "" {
		return fmt.Errorf("自动标签由应用维护，不能手动删除")
	}
	inVocabulary := isAITagEligible(*tag) // 删除会写 DeletedAt，必须在删除前判定。
	if !inVocabulary {
		// 词表外的标签（「人物」分类）删除不改变 AI 词表，不做全库重排（META-01）：
		// 只有「删完就没有人工标签」的视频需要重新进入自动分析。定向对账必须在清除关联之前做，
		// 之后就不知道哪些视频挂过它了。
		if err := resetAITaggingForTagRemovalTx(tx, tag.ID); err != nil {
			return err
		}
	}
	if err := tx.Model(tag).Association("Videos").Clear(); err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM image_tags WHERE tag_id = ?", tag.ID).Error; err != nil {
		return err
	}
	if err := tx.Delete(tag).Error; err != nil {
		return err
	}
	if !inVocabulary {
		return nil
	}
	return resetAITaggingAfterLibraryChange(tx)
}

// resetAITaggingForTagRemovalTx 是删除词表外标签时的定向对账，替代全库的
// resetAITaggingAfterLibraryChange（META-01）。调用方在同一事务里、**清除该标签的关联之前**调用：
//   - 指向该标签的待审候选（视频、图片两侧）失效，与全量对账里「标签已删除」那一条同口径；
//   - 挂过该标签、且除它之外再没有人工（非自动）标签的视频，AI 状态改回 pending 并清空证据指纹，
//     让自动分析重新接手。其他视频的状态与指纹一律不动。
//
// 「挂过该标签的视频」用子查询在库里取，不先把 ID 读进内存再拼 IN 列表：一个人物标签可能挂着
// 成千上万个视频，IN 列表会撞上 SQLite / PostgreSQL 的参数个数上限。
func resetAITaggingForTagRemovalTx(tx *gorm.DB, tagID uint) error {
	if err := tx.Model(&models.AITagCandidate{}).
		Where("status = ? AND matched_tag_id = ?", models.AITagCandidateStatusPending, tagID).
		Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
		return err
	}
	if err := tx.Model(&models.ImageAITagCandidate{}).
		Where("status = ? AND matched_tag_id = ?", models.AITagCandidateStatusPending, tagID).
		Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
		return err
	}
	return tx.Model(&models.AITaggingState{}).
		Where("ai_tagging_states.video_id IN (SELECT removed.video_id FROM video_tags removed WHERE removed.tag_id = ?)", tagID).
		Where(`NOT EXISTS (
				SELECT 1 FROM video_tags
				INNER JOIN tags ON tags.id = video_tags.tag_id
				WHERE video_tags.video_id = ai_tagging_states.video_id
					AND video_tags.tag_id <> ?
					AND COALESCE(tags.automatic_kind, '') = ''
			)`, tagID).
		Updates(map[string]interface{}{
			"status":               models.AITaggingStateStatusPending,
			"skip_reason":          "",
			"evidence_fingerprint": "",
			"last_error":           "",
		}).Error
}

// rewriteSavedViewTagIDsTx 把活跃保存视图 tag_ids_json 里的来源标签 ID 换成目标 ID 并去重
// （D-PC35）。合并标签时与关系改写同一事务，保证桌面与 Jellyfin 的视图不会静默放宽或变空。
func rewriteSavedViewTagIDsTx(tx *gorm.DB, sourceIDs []uint, targetID uint) error {
	sources := make(map[uint]struct{}, len(sourceIDs))
	for _, id := range sourceIDs {
		sources[id] = struct{}{}
	}
	var views []models.SavedLibraryView
	if err := tx.Where("tag_ids_json <> ?", "[]").Find(&views).Error; err != nil {
		return err
	}
	for _, view := range views {
		var ids []uint
		if err := json.Unmarshal([]byte(view.TagIDsJSON), &ids); err != nil {
			continue // 历史脏数据不由合并操作修复。
		}
		rewritten := make([]uint, 0, len(ids))
		seen := make(map[uint]struct{}, len(ids))
		changed := false
		for _, id := range ids {
			if _, isSource := sources[id]; isSource {
				id = targetID
				changed = true
			}
			if _, dup := seen[id]; dup {
				changed = true
				continue
			}
			seen[id] = struct{}{}
			rewritten = append(rewritten, id)
		}
		if !changed {
			continue
		}
		payload, err := json.Marshal(rewritten)
		if err != nil {
			return err
		}
		if err := tx.Model(&models.SavedLibraryView{}).Where("id = ?", view.ID).
			Update("tag_ids_json", string(payload)).Error; err != nil {
			return err
		}
	}
	return nil
}

// TagUsageCount 是删除与合并确认框要显示的影响范围（D-PC37）。
type TagUsageCount struct {
	Videos int64 `json:"videos"`
	Images int64 `json:"images"`
	// TrashedVideos / TrashedImages 是仍挂着该标签、但已在回收站里的媒体数。删除或合并
	// 标签会一并清掉这些关联，确认框据此如实显示范围（恢复后它们也不再带这个标签）。
	TrashedVideos int64 `json:"trashed_videos"`
	TrashedImages int64 `json:"trashed_images"`
}

// GetTagUsageCounts 返回每个标签仍可见的视频与图片数；未使用的标签也带 0 值。
func (s *TagService) GetTagUsageCounts(ids []uint) (map[uint]TagUsageCount, error) {
	result := make(map[uint]TagUsageCount, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	for _, id := range ids {
		result[id] = TagUsageCount{}
	}
	type row struct {
		TagID uint
		Total int64
	}
	var videoRows, imageRows []row
	if err := database.DB.Table("video_tags").
		Select("video_tags.tag_id AS tag_id, COUNT(*) AS total").
		Joins("JOIN videos ON videos.id = video_tags.video_id AND videos.deleted_at IS NULL").
		Where("video_tags.tag_id IN ?", ids).Group("video_tags.tag_id").Scan(&videoRows).Error; err != nil {
		return nil, err
	}
	if err := database.DB.Table("image_tags").
		Select("image_tags.tag_id AS tag_id, COUNT(*) AS total").
		Joins("JOIN images ON images.id = image_tags.image_id AND images.deleted_at IS NULL").
		Where("image_tags.tag_id IN ?", ids).Group("image_tags.tag_id").Scan(&imageRows).Error; err != nil {
		return nil, err
	}
	var trashedVideoRows, trashedImageRows []row
	if err := database.DB.Table("video_tags").
		Select("video_tags.tag_id AS tag_id, COUNT(*) AS total").
		Joins("JOIN videos ON videos.id = video_tags.video_id AND videos.deleted_at IS NOT NULL").
		Where("video_tags.tag_id IN ?", ids).Group("video_tags.tag_id").Scan(&trashedVideoRows).Error; err != nil {
		return nil, err
	}
	if err := database.DB.Table("image_tags").
		Select("image_tags.tag_id AS tag_id, COUNT(*) AS total").
		Joins("JOIN images ON images.id = image_tags.image_id AND images.deleted_at IS NOT NULL").
		Where("image_tags.tag_id IN ?", ids).Group("image_tags.tag_id").Scan(&trashedImageRows).Error; err != nil {
		return nil, err
	}
	for _, r := range videoRows {
		c := result[r.TagID]
		c.Videos = r.Total
		result[r.TagID] = c
	}
	for _, r := range imageRows {
		c := result[r.TagID]
		c.Images = r.Total
		result[r.TagID] = c
	}
	for _, r := range trashedVideoRows {
		c := result[r.TagID]
		c.TrashedVideos = r.Total
		result[r.TagID] = c
	}
	for _, r := range trashedImageRows {
		c := result[r.TagID]
		c.TrashedImages = r.Total
		result[r.TagID] = c
	}
	return result, nil
}

// GetVideoAutomaticTagOverrides 列出该视频上所有人工覆盖（D-PC36）：present=true 是手动加上，
// false 是手动去掉。前端据此显示「手动」角标与「恢复自动」入口。
func (s *TagService) GetVideoAutomaticTagOverrides(videoID uint) ([]models.VideoAutomaticTagOverride, error) {
	overrides := []models.VideoAutomaticTagOverride{}
	err := database.DB.Where("video_id = ?", videoID).Order("automatic_kind").Find(&overrides).Error
	return overrides, err
}

// ClearVideoAutomaticTagOverride 删除覆盖行并立即对该视频自动对账，让它重新跟随自动规则。
func (s *TagService) ClearVideoAutomaticTagOverride(videoID uint, kind string) error {
	if kind != shortVideoAutomaticTagKind && kind != lowResolutionAutomaticTagKind {
		return fmt.Errorf("该自动标签不能恢复")
	}
	return database.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&models.Video{}, videoID).Error; err != nil {
			return err
		}
		if err := tx.Where("video_id = ? AND automatic_kind = ?", videoID, kind).
			Delete(&models.VideoAutomaticTagOverride{}).Error; err != nil {
			return err
		}
		return syncShortVideoTagForVideo(tx, videoID)
	})
}
