package services

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"
)

// WatchlistService 只管理片名备忘，不参与扫描、下载或媒体关系维护。
type WatchlistService struct{}

// WatchlistPage 按添加顺序倒序返回；NextID 为零时没有下一页。
type WatchlistPage struct {
	Entries []models.WatchlistEntry `json:"entries"`
	NextID  uint                    `json:"next_id"`
}

var ErrWatchlistTitleExists = errors.New("该片名已在想看片单中")

var ErrWatchlistEntryNotFound = errors.New("这条想看记录已不存在，请刷新列表")

func validateWatchlistText(text string, required bool) (string, error) {
	text = strings.TrimSpace(text)
	if required && text == "" {
		return "", errors.New("请输入片名")
	}
	if !utf8.ValidString(text) || strings.ContainsFunc(text, unicode.IsControl) {
		return "", errors.New("片名不能包含控制字符或无效文本")
	}
	if utf8.RuneCountInString(text) > 200 {
		return "", errors.New("片名或搜索词不能超过 200 个字符")
	}
	return text, nil
}

// List 查询片名；cursorID 为零取首页，limit 沿用实体列表的有界页大小。
func (s *WatchlistService) List(keyword string, cursorID uint, limit int) (*WatchlistPage, error) {
	keyword, err := validateWatchlistText(keyword, false)
	if err != nil {
		return nil, err
	}
	limit = normalizeEntityPageLimit(limit)
	query := database.DB.Model(&models.WatchlistEntry{})
	if keyword != "" {
		query = query.Where("LOWER(title) LIKE ? ESCAPE '\\'", "%"+escapeSQLLike(strings.ToLower(keyword))+"%")
	}
	if cursorID != 0 {
		query = query.Where("id < ?", cursorID)
	}
	page := &WatchlistPage{Entries: make([]models.WatchlistEntry, 0)}
	if err := query.Order("id DESC").Limit(limit + 1).Find(&page.Entries).Error; err != nil {
		return nil, fmt.Errorf("读取想看片单失败: %w", err)
	}
	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		page.NextID = page.Entries[limit-1].ID
	}
	return page, nil
}

// Create 保存唯一片名，首尾空白由校验统一移除。
func (s *WatchlistService) Create(title string) (*models.WatchlistEntry, error) {
	title, err := validateWatchlistText(title, true)
	if err != nil {
		return nil, err
	}
	entry := &models.WatchlistEntry{Title: title}
	if err := database.DB.Create(entry).Error; err != nil {
		if watchlistTitleConflict(err) {
			return nil, ErrWatchlistTitleExists
		}
		return nil, fmt.Errorf("添加想看记录失败: %w", err)
	}
	return entry, nil
}

// Update 只改指定记录的片名，不会重新创建已移除的记录。
func (s *WatchlistService) Update(id uint, title string) error {
	title, err := validateWatchlistText(title, true)
	if err != nil {
		return err
	}
	result := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", id).Update("title", title)
	if result.Error != nil {
		if watchlistTitleConflict(result.Error) {
			return ErrWatchlistTitleExists
		}
		return fmt.Errorf("修改想看记录失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrWatchlistEntryNotFound
	}
	return nil
}

// Delete 移除指定片名备忘，不操作媒体记录或磁盘文件。
func (s *WatchlistService) Delete(id uint) error {
	result := database.DB.Where("id = ?", id).Delete(&models.WatchlistEntry{})
	if result.Error != nil {
		return fmt.Errorf("移除想看记录失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrWatchlistEntryNotFound
	}
	return nil
}

// Both PostgreSQL and SQLite identify the violated title constraint in errors.
func watchlistTitleConflict(err error) bool {
	return strings.Contains(err.Error(), "idx_watchlist_title") || strings.Contains(err.Error(), "UNIQUE constraint failed: watchlist_entries.title")
}
