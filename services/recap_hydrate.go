package services

import (
	"fmt"
	"video-master/models"

	"gorm.io/gorm"
)

// 回顾只水合 Top 列表真正要显示的那几行：一次 IN 查询，包含已进回收站的行（Unscoped），
// 由 Available 告诉前端能不能点开。

func hydrateRecapVideos(db *gorm.DB, ids []uint) (map[uint]RecapVideo, error) {
	result := map[uint]RecapVideo{}
	if len(ids) == 0 {
		return result, nil
	}
	var rows []struct {
		ID             uint
		Name           string
		DisplayTitle   string
		Duration       float64
		PersonalRating *float64
		IsStale        bool
		Deleted        int
	}
	if err := db.Unscoped().Model(&models.Video{}).
		Select("id, name, display_title, duration, personal_rating, is_stale, CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END AS deleted").
		Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = RecapVideo{ID: row.ID, Title: recapVideoTitle(row.DisplayTitle, row.Name), Name: row.Name,
			Duration: row.Duration, PersonalRating: row.PersonalRating, Available: row.Deleted == 0 && !row.IsStale}
	}
	return result, nil
}

func recapVideoTitle(displayTitle, name string) string {
	if displayTitle != "" {
		return displayTitle
	}
	return name
}

func hydrateRecapImages(db *gorm.DB, ids []uint) (map[uint]RecapImage, error) {
	result := map[uint]RecapImage{}
	if len(ids) == 0 {
		return result, nil
	}
	var rows []struct {
		ID        uint
		Name      string
		Width     int
		Height    int
		Directory string
		IsStale   bool
		Deleted   int
	}
	if err := db.Unscoped().Model(&models.Image{}).
		Select("id, name, width, height, directory, is_stale, CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END AS deleted").
		Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = RecapImage{ID: row.ID, Name: row.Name, Width: row.Width, Height: row.Height,
			Directory: row.Directory, Available: row.Deleted == 0 && !row.IsStale}
	}
	return result, nil
}

// recapEntityURL 与人物、作品集列表同一套受控路由：没有托管图片时不给地址。
func recapEntityURL(kind string, id uint, path string) string {
	if path == "" {
		return ""
	}
	return fmt.Sprintf("/preview/%s/%d", kind, id)
}
