package services

import (
	"errors"
	"gorm.io/gorm"
	"strings"
	"unicode"
	"unicode/utf8"
	"video-master/models"
)

var (
	ErrViewingNoteInvalid        = errors.New("invalid_viewing_note: 请检查片名、日期、时间、评分和文本长度")
	ErrViewingNoteConflict       = errors.New("viewing_note_conflict: 记录已被修改或删除，请刷新后重试")
	ErrBookmarkSourceChanged     = errors.New("bookmark_source_changed: 原片已改变，请重新核对位置")
	ErrBookmarkSourceUnavailable = errors.New("bookmark_source_unavailable: 原片不可用")
)

// Same literal-search whitespace semantics as JavaScript String.trim.
func trimLiteralKeyword(text string) string {
	return strings.TrimFunc(text, func(r rune) bool {
		return unicode.Is(unicode.Zs, r) || r == '\t' || r == '\v' || r == '\f' || r == '\n' || r == '\r' || r == '\u2028' || r == '\u2029' || r == '\ufeff'
	})
}

func validNoteText(title, note string) bool {
	return utf8.ValidString(title) && utf8.ValidString(note) && strings.TrimSpace(title) != "" && utf8.RuneCountInString(title) <= 200 && utf8.RuneCountInString(note) <= 10000
}

func notePageLimit(limit int) (int, error) {
	if limit == 0 {
		return 50, nil
	}
	if limit < 1 || limit > 200 {
		return 0, ErrViewingNoteInvalid
	}
	return limit, nil
}

// Database availability only. No page-wide filesystem walk or false claim that
// a file has been checked. Resolve checks the single chosen source on demand.
func noteAvailableVideos(db *gorm.DB, ids []uint) (map[uint]bool, error) {
	result := make(map[uint]bool)
	if len(ids) == 0 {
		return result, nil
	}
	var videos []struct {
		ID      uint
		IsStale bool
	}
	err := db.Model(&models.Video{}).Select("id", "is_stale").Where("id IN ?", ids).Find(&videos).Error
	for _, video := range videos {
		result[video.ID] = !video.IsStale
	}
	return result, err
}
