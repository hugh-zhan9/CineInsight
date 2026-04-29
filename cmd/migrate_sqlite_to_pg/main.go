package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"video-master/models"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type videoTag struct {
	VideoID uint `gorm:"column:video_id"`
	TagID   uint `gorm:"column:tag_id"`
}

type sqliteSnapshot struct {
	Videos          []models.Video
	Tags            []models.Tag
	Settings        []models.Settings
	ScanDirectories []models.ScanDirectory
	VideoTags       []videoTag
}

func defaultSqlitePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户目录失败: %w", err)
	}
	return filepath.Join(homeDir, ".video-master", "video-master.db"), nil
}

func postgresDSNFromEnv() (string, error) {
	host := os.Getenv("PG_HOST")
	if host == "" {
		return "", fmt.Errorf("PG_HOST 不能为空")
	}
	user := os.Getenv("PG_USER")
	if user == "" {
		return "", fmt.Errorf("PG_USER 不能为空")
	}
	db := os.Getenv("PG_DB")
	if db == "" {
		return "", fmt.Errorf("PG_DB 不能为空")
	}
	port := os.Getenv("PG_PORT")
	if port == "" {
		port = "5432"
	}
	password := os.Getenv("PG_PASSWORD")
	sslmode := os.Getenv("PG_SSLMODE")
	if sslmode == "" {
		sslmode = "disable"
	}
	timezone := os.Getenv("PG_TIMEZONE")

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		host, port, user, password, db, sslmode,
	)
	if timezone != "" {
		dsn = fmt.Sprintf("%s TimeZone=%s", dsn, timezone)
	}
	return dsn, nil
}

func loadSqliteData(db *gorm.DB) (sqliteSnapshot, error) {
	var snapshot sqliteSnapshot
	if err := db.Unscoped().Find(&snapshot.Videos).Error; err != nil {
		return snapshot, err
	}
	if err := db.Unscoped().Find(&snapshot.Tags).Error; err != nil {
		return snapshot, err
	}
	if err := db.Unscoped().Find(&snapshot.Settings).Error; err != nil {
		return snapshot, err
	}
	if err := db.Unscoped().Find(&snapshot.ScanDirectories).Error; err != nil {
		return snapshot, err
	}
	if err := db.Table("video_tags").Find(&snapshot.VideoTags).Error; err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func ensurePostgresEmpty(db *gorm.DB) error {
	var count int64
	if err := db.Model(&models.Video{}).Unscoped().Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("postgres 数据库不为空，请先清理后再迁移")
	}
	return nil
}

func resetSequence(db *gorm.DB, table string) error {
	stmt := fmt.Sprintf("SELECT setval(pg_get_serial_sequence('%s','id'), COALESCE(MAX(id), 0)) FROM %s", table, table)
	return db.Exec(stmt).Error
}

func migrateSqliteToPostgres(sqlitePath string) error {
	_ = godotenv.Load()

	dsn, err := postgresDSNFromEnv()
	if err != nil {
		return err
	}

	sqliteDB, err := gorm.Open(sqlite.Open(sqlitePath), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("打开 sqlite 失败: %w", err)
	}
	pgDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("连接 postgres 失败: %w", err)
	}

	if err := pgDB.AutoMigrate(&models.Video{}, &models.SubtitleSegment{}, &models.SubtitleIndexState{}, &models.Tag{}, &models.Settings{}, &models.ScanDirectory{}); err != nil {
		return fmt.Errorf("postgres 迁移失败: %w", err)
	}
	if err := ensurePostgresEmpty(pgDB); err != nil {
		return err
	}

	snapshot, err := loadSqliteData(sqliteDB)
	if err != nil {
		return fmt.Errorf("读取 sqlite 数据失败: %w", err)
	}

	if len(snapshot.Settings) > 0 {
		if err := pgDB.Unscoped().Create(&snapshot.Settings).Error; err != nil {
			return err
		}
	}
	if len(snapshot.Tags) > 0 {
		if err := pgDB.Unscoped().CreateInBatches(&snapshot.Tags, 200).Error; err != nil {
			return err
		}
	}
	if len(snapshot.Videos) > 0 {
		if err := pgDB.Unscoped().CreateInBatches(&snapshot.Videos, 200).Error; err != nil {
			return err
		}
	}
	if len(snapshot.ScanDirectories) > 0 {
		if err := pgDB.Unscoped().CreateInBatches(&snapshot.ScanDirectories, 200).Error; err != nil {
			return err
		}
	}
	if len(snapshot.VideoTags) > 0 {
		if err := pgDB.Table("video_tags").CreateInBatches(&snapshot.VideoTags, 500).Error; err != nil {
			return err
		}
	}

	if err := resetSequence(pgDB, "videos"); err != nil {
		return err
	}
	if err := resetSequence(pgDB, "tags"); err != nil {
		return err
	}
	if err := resetSequence(pgDB, "settings"); err != nil {
		return err
	}
	if err := resetSequence(pgDB, "scan_directories"); err != nil {
		return err
	}

	return nil
}

func main() {
	defaultPath, err := defaultSqlitePath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	sqlitePath := flag.String("sqlite", defaultPath, "sqlite 数据库路径")
	flag.Parse()

	if err := migrateSqliteToPostgres(*sqlitePath); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	fmt.Println("迁移完成")
}
