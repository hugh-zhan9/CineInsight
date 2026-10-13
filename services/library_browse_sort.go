package services

import (
	"errors"
	"fmt"
	"time"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 海报墙的三种键集排序（UI 二期 §2.3，D-P2-01）：最近添加、年份、片名。与均衡 / 评分排序共用
// SearchLibraryVideoPage 的筛选、版本聚合与页结构，只是排序列与游标字段不同。

// titleSortKeyCursorMax 是 title_sort_key 的列宽（十六进制字符数）。
const titleSortKeyCursorMax = 512

func isKeysetBrowseSort(sortMode string) bool {
	switch sortMode {
	case LibrarySortAddedDesc, LibrarySortYearDesc, LibrarySortTitleAsc:
		return true
	}
	return false
}

// validateBrowseCursor 按模式校验三种新排序的游标：只带自己的字段，其余必须为空。
func validateBrowseCursor(sortMode string, cursor *LibraryVideoCursor) error {
	other := fmt.Errorf("%s 游标包含其他排序的字段", sortMode)
	if cursor.Score != 0 || cursor.Size != 0 || cursor.Rating != nil || cursor.RatingIsNull {
		return other
	}
	switch sortMode {
	case LibrarySortAddedDesc:
		if cursor.Year != nil || cursor.YearIsNull || cursor.TitleKey != "" {
			return other
		}
		if cursor.CreatedAt == nil {
			return errors.New("最近添加游标缺少添加时间")
		}
	case LibrarySortYearDesc:
		if cursor.CreatedAt != nil || cursor.TitleKey != "" {
			return other
		}
		if cursor.YearIsNull && cursor.Year != nil {
			return errors.New("空年份游标不能包含年份值")
		}
		if !cursor.YearIsNull && cursor.Year == nil {
			return errors.New("年份游标缺少年份值")
		}
	case LibrarySortTitleAsc:
		if cursor.CreatedAt != nil || cursor.Year != nil || cursor.YearIsNull {
			return other
		}
		// 空串是合法键（还没补齐的行排在最前）；非空时必须是列宽以内的小写十六进制。
		if len(cursor.TitleKey) > titleSortKeyCursorMax || len(cursor.TitleKey)%2 != 0 || !isLowerHex(cursor.TitleKey) {
			return errors.New("片名游标无效")
		}
	}
	return nil
}

func isLowerHex(text string) bool {
	for _, r := range text {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// searchLibraryBrowsePage 跑三种新排序之一。filter 已规范化、cursor 已校验。
// 同值（同一添加时间、同一年份、同一排序键）一律由 id 决断，空年份排在所有有年份的行之后，两个后端一致。
func (s *VideoService) searchLibraryBrowsePage(filter LibraryFilter, cursor *LibraryVideoCursor, limit int) (*LibraryVideoPage, error) {
	now := time.Now()
	query, err := applyLibraryFilter(database.DB.Model(&models.Video{}).Preload("Tags"), filter, now)
	if err != nil {
		return nil, err
	}
	if query, err = applyVersionCollapse(query, filter, now); err != nil {
		return nil, err
	}
	switch filter.SortMode {
	case LibrarySortAddedDesc:
		if cursor != nil {
			at, err := libraryCreatedAtCursorTime(*cursor.CreatedAt, cursor.ID)
			if err != nil {
				return nil, err
			}
			query = query.Where("(videos.created_at < ? OR (videos.created_at = ? AND videos.id < ?))", at, at, cursor.ID)
		}
		query = query.Order("videos.created_at DESC").Order("videos.id DESC")
	case LibrarySortYearDesc:
		if cursor != nil {
			if cursor.YearIsNull {
				query = query.Where("videos.year IS NULL AND videos.id < ?", cursor.ID)
			} else {
				query = query.Where("(videos.year < ? OR (videos.year = ? AND videos.id < ?) OR videos.year IS NULL)", *cursor.Year, *cursor.Year, cursor.ID)
			}
		}
		query = query.Order("CASE WHEN videos.year IS NULL THEN 1 ELSE 0 END ASC").Order("videos.year DESC").Order("videos.id DESC")
	case LibrarySortTitleAsc:
		if cursor != nil {
			query = query.Where("(videos.title_sort_key > ? OR (videos.title_sort_key = ? AND videos.id > ?))", cursor.TitleKey, cursor.TitleKey, cursor.ID)
		}
		query = query.Order("videos.title_sort_key ASC").Order("videos.id ASC")
	}
	var videos []models.Video
	if err := query.Limit(limit + 1).Find(&videos).Error; err != nil {
		return nil, err
	}
	var next *LibraryVideoCursor
	if len(videos) > limit {
		videos = videos[:limit]
		next = browseCursorAfter(filter.SortMode, videos[len(videos)-1])
	}
	page, err := newLibraryVideoPage(videos, next)
	if err != nil {
		return nil, err
	}
	return attachPageVersionGroups(page, filter)
}

func browseCursorAfter(sortMode string, last models.Video) *LibraryVideoCursor {
	next := &LibraryVideoCursor{SortMode: sortMode, ID: last.ID}
	switch sortMode {
	case LibrarySortAddedDesc:
		createdAt := last.CreatedAt
		next.CreatedAt = &createdAt
	case LibrarySortYearDesc:
		if last.Year == nil {
			next.YearIsNull = true
		} else {
			year := *last.Year
			next.Year = &year
		}
	case LibrarySortTitleAsc:
		next.TitleKey = last.TitleSortKey
	}
	return next
}

// libraryCreatedAtCursorTime 与 continueWatchingCursorTime 同理：游标行还在、回传值与它存的添加时间相差不到
// 1 毫秒时用库里的原值（SQLite 按文本比较时间，偏移与精度必须与存储逐字一致；前端若把时间截到毫秒也不漏行），
// 否则用回传值换到 time.Local。created_at 写入后不再变，游标行一般都在。
func libraryCreatedAtCursorTime(cursor time.Time, cursorID uint) (time.Time, error) {
	var row models.Video
	err := database.DB.Unscoped().Model(&models.Video{}).
		Select("videos.id", "videos.created_at").
		Where("videos.id = ?", cursorID).Take(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, fmt.Errorf("读取最近添加游标失败: %w", err)
	}
	if err == nil {
		if diff := row.CreatedAt.Sub(cursor); diff > -time.Millisecond && diff < time.Millisecond {
			return row.CreatedAt, nil
		}
	}
	return cursor.In(time.Local), nil
}
