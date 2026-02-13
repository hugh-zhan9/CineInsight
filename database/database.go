package database

import (
	"fmt"
	"os"
	"path/filepath"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var DB *gorm.DB

// Init 初始化数据库
func Init() error {
	// 获取用户数据目录
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("获取用户目录失败: %w", err)
	}

	// 创建应用数据目录
	dataDir := filepath.Join(homeDir, ".video-master")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}

	// 数据库文件路径
	dbPath := filepath.Join(dataDir, "video-master.db")

	// 连接数据库
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}

	// 自动迁移数据表
	if err := db.AutoMigrate(&models.Video{}, &models.Tag{}, &models.Settings{}, &models.ScanDirectory{}); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}

	if err := cleanupDuplicateVideos(db); err != nil {
		return fmt.Errorf("清理重复视频失败: %w", err)
	}
	if err := ensureVideoPathUniqueIndex(db); err != nil {
		return fmt.Errorf("创建视频路径唯一索引失败: %w", err)
	}

	// 初始化默认设置
	var settings models.Settings
	if err := db.First(&settings).Error; err == gorm.ErrRecordNotFound {
		// 默认支持的视频格式
		defaultExts := ".mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt"
		settings = models.Settings{
			ConfirmBeforeDelete: true,
			DeleteOriginalFile:  false,
			VideoExtensions:     defaultExts,
			PlayWeight:          2.0, // 默认 1次播放 = 2次随机播放
			AutoScanOnStartup:   false,
			LogEnabled:          false,
		}
		db.Create(&settings)
	}

	DB = db
	return nil
}

func cleanupDuplicateVideos(db *gorm.DB) error {
	type duplicatePath struct {
		Path   string
		KeepID uint
	}

	var duplicates []duplicatePath
	if err := db.Raw(`
		SELECT path, MAX(id) AS keep_id
		FROM videos
		WHERE deleted_at IS NULL AND path <> ''
		GROUP BY path
		HAVING COUNT(*) > 1
	`).Scan(&duplicates).Error; err != nil {
		return err
	}

	for _, d := range duplicates {
		var duplicateIDs []uint
		if err := db.Raw(`
			SELECT id
			FROM videos
			WHERE path = ? AND deleted_at IS NULL AND id <> ?
		`, d.Path, d.KeepID).Scan(&duplicateIDs).Error; err != nil {
			return err
		}
		if len(duplicateIDs) == 0 {
			continue
		}

		if err := db.Exec(`
			INSERT OR IGNORE INTO video_tags(video_id, tag_id)
			SELECT ?, tag_id FROM video_tags WHERE video_id IN ?
		`, d.KeepID, duplicateIDs).Error; err != nil {
			return err
		}
		if err := db.Exec(`DELETE FROM video_tags WHERE video_id IN ?`, duplicateIDs).Error; err != nil {
			return err
		}
		if err := db.Unscoped().Where("id IN ?", duplicateIDs).Delete(&models.Video{}).Error; err != nil {
			return err
		}
	}

	return nil
}

func ensureVideoPathUniqueIndex(db *gorm.DB) error {
	return db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_videos_path_active
		ON videos(path)
		WHERE deleted_at IS NULL AND path <> ''
	`).Error
}

// Close 关闭数据库连接
func Close() error {
	sqlDB, err := DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
