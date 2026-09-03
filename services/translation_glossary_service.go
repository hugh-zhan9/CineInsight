package services

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// ErrGlossaryTermConflict 表示改名后的源词与同作用域内另一条已存在的条目撞了唯一键。
var ErrGlossaryTermConflict = errors.New("translation_glossary_term_conflict")

const (
	maxGlossaryTermRunes = 200
	maxGlossaryNoteRunes = 500
)

// GlossaryTerm 是注入提示词的一条术语，只带翻译器需要的三个字段。
type GlossaryTerm struct {
	SourceTerm string `json:"source_term"`
	TargetTerm string `json:"target_term"`
	Note       string `json:"note"`
}

// TranslationGlossaryService 维护术语表并按视频解析生效集（D-033）。
type TranslationGlossaryService struct{}

func NewTranslationGlossaryService() *TranslationGlossaryService {
	return &TranslationGlossaryService{}
}

// glossaryScopeKey 把可空的作品集 ID 物化成唯一键用的 scope_key：0 表示全局。
func glossaryScopeKey(collectionID *uint) int64 {
	if collectionID == nil || *collectionID == 0 {
		return 0
	}
	return int64(*collectionID)
}

func normalizeGlossaryCollectionID(collectionID *uint) *uint {
	if collectionID == nil || *collectionID == 0 {
		return nil
	}
	value := *collectionID
	return &value
}

// List 返回一个作用域内的全部条目；collectionID 为 nil 或 0 时返回全局条目。
func (s *TranslationGlossaryService) List(collectionID *uint) ([]models.TranslationGlossaryEntry, error) {
	entries := []models.TranslationGlossaryEntry{}
	if err := database.DB.
		Where("scope_key = ?", glossaryScopeKey(collectionID)).
		Order("source_term_lower ASC, id ASC").
		Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}

func validateGlossaryEntry(entry models.TranslationGlossaryEntry) (models.TranslationGlossaryEntry, error) {
	entry.SourceTerm = strings.TrimSpace(entry.SourceTerm)
	entry.TargetTerm = strings.TrimSpace(entry.TargetTerm)
	entry.Note = strings.TrimSpace(entry.Note)
	if entry.SourceTerm == "" {
		return entry, errors.New("glossary source term is required")
	}
	if entry.TargetTerm == "" {
		return entry, errors.New("glossary target term is required")
	}
	if utf8.RuneCountInString(entry.SourceTerm) > maxGlossaryTermRunes {
		return entry, fmt.Errorf("glossary source term exceeds %d characters", maxGlossaryTermRunes)
	}
	if utf8.RuneCountInString(entry.TargetTerm) > maxGlossaryTermRunes {
		return entry, fmt.Errorf("glossary target term exceeds %d characters", maxGlossaryTermRunes)
	}
	if utf8.RuneCountInString(entry.Note) > maxGlossaryNoteRunes {
		return entry, fmt.Errorf("glossary note exceeds %d characters", maxGlossaryNoteRunes)
	}
	entry.CollectionID = normalizeGlossaryCollectionID(entry.CollectionID)
	entry.ScopeKey = glossaryScopeKey(entry.CollectionID)
	entry.SourceTermLower = strings.ToLower(entry.SourceTerm)
	return entry, nil
}

// Upsert 按唯一键 (scope_key, source_term_lower) 落库。带 ID 时按 ID 改写既有条目
// （允许改源词），改后撞上同作用域另一条则报冲突。
func (s *TranslationGlossaryService) Upsert(entry models.TranslationGlossaryEntry) (*models.TranslationGlossaryEntry, error) {
	normalized, err := validateGlossaryEntry(entry)
	if err != nil {
		return nil, err
	}

	if normalized.ID != 0 {
		var existing models.TranslationGlossaryEntry
		if err := database.DB.First(&existing, normalized.ID).Error; err != nil {
			return nil, err
		}
		var conflict models.TranslationGlossaryEntry
		err := database.DB.
			Where("scope_key = ? AND source_term_lower = ? AND id <> ?", normalized.ScopeKey, normalized.SourceTermLower, normalized.ID).
			First(&conflict).Error
		if err == nil {
			return nil, ErrGlossaryTermConflict
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		existing.CollectionID = normalized.CollectionID
		existing.ScopeKey = normalized.ScopeKey
		existing.SourceTerm = normalized.SourceTerm
		existing.SourceTermLower = normalized.SourceTermLower
		existing.TargetTerm = normalized.TargetTerm
		existing.Note = normalized.Note
		if err := database.DB.Save(&existing).Error; err != nil {
			return nil, err
		}
		return &existing, nil
	}

	var existing models.TranslationGlossaryEntry
	err = database.DB.
		Where("scope_key = ? AND source_term_lower = ?", normalized.ScopeKey, normalized.SourceTermLower).
		First(&existing).Error
	if err == nil {
		existing.SourceTerm = normalized.SourceTerm
		existing.TargetTerm = normalized.TargetTerm
		existing.Note = normalized.Note
		if err := database.DB.Save(&existing).Error; err != nil {
			return nil, err
		}
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err := database.DB.Create(&normalized).Error; err != nil {
		return nil, err
	}
	return &normalized, nil
}

func (s *TranslationGlossaryService) Delete(id uint) error {
	if id == 0 {
		return errors.New("glossary entry id is required")
	}
	return database.DB.Delete(&models.TranslationGlossaryEntry{}, id).Error
}

// ResolveForVideo 解析一个视频的术语生效集：所属全部活跃作品集的条目 + 全局条目。
// 同源词作品集级覆盖全局级；多作品集冲突取 updated_at 最新（同刻取 id 大者）并计数日志，
// 计数只记条数，不记词本身。
func (s *TranslationGlossaryService) ResolveForVideo(videoID uint) ([]GlossaryTerm, error) {
	if videoID == 0 {
		return nil, errors.New("video id is required")
	}
	collectionIDs := []uint{}
	if err := database.DB.Model(&models.CollectionVideo{}).
		Joins("JOIN media_collections ON media_collections.id = collection_videos.collection_id").
		Where("collection_videos.video_id = ? AND media_collections.deleted_at IS NULL", videoID).
		Pluck("collection_videos.collection_id", &collectionIDs).Error; err != nil {
		return nil, err
	}

	scopeKeys := make([]int64, 0, len(collectionIDs)+1)
	scopeKeys = append(scopeKeys, 0)
	for _, collectionID := range collectionIDs {
		scopeKeys = append(scopeKeys, int64(collectionID))
	}

	entries := []models.TranslationGlossaryEntry{}
	if err := database.DB.
		Where("scope_key IN ?", scopeKeys).
		Order("source_term_lower ASC, id ASC").
		Find(&entries).Error; err != nil {
		return nil, err
	}

	winners := make(map[string]models.TranslationGlossaryEntry, len(entries))
	conflicts := 0
	for _, entry := range entries {
		current, seen := winners[entry.SourceTermLower]
		if !seen {
			winners[entry.SourceTermLower] = entry
			continue
		}
		if current.ScopeKey != 0 && entry.ScopeKey != 0 {
			conflicts++
		}
		if glossaryEntryWins(entry, current) {
			winners[entry.SourceTermLower] = entry
		}
	}
	if conflicts > 0 {
		log.Printf("[Subtitle] glossary scope conflicts video_id=%d count=%d", videoID, conflicts)
	}

	keys := make([]string, 0, len(winners))
	for key := range winners {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	terms := make([]GlossaryTerm, 0, len(keys))
	for _, key := range keys {
		entry := winners[key]
		terms = append(terms, GlossaryTerm{
			SourceTerm: entry.SourceTerm,
			TargetTerm: entry.TargetTerm,
			Note:       entry.Note,
		})
	}
	return terms, nil
}

// glossaryEntryWins 报告 candidate 是否应当取代 current：作品集级压全局级，
// 两个都是作品集级时取 updated_at 更新的一条，同刻取 id 更大的一条以保证确定性。
func glossaryEntryWins(candidate, current models.TranslationGlossaryEntry) bool {
	if candidate.ScopeKey != 0 && current.ScopeKey == 0 {
		return true
	}
	if candidate.ScopeKey == 0 && current.ScopeKey != 0 {
		return false
	}
	if candidate.UpdatedAt.After(current.UpdatedAt) {
		return true
	}
	if candidate.UpdatedAt.Equal(current.UpdatedAt) {
		return candidate.ID > current.ID
	}
	return false
}
