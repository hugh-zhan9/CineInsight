package database

import (
	"path/filepath"
	"testing"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestInitUsesPostgresEnv(t *testing.T) {
	t.Setenv("PG_HOST", "127.0.0.1")
	t.Setenv("PG_PORT", "5432")
	t.Setenv("PG_USER", "user")
	t.Setenv("PG_PASSWORD", "pass")
	t.Setenv("PG_DB", "db")
	t.Setenv("PG_SSLMODE", "disable")

	err := Init()
	if err == nil {
		_ = Close()
		t.Fatalf("expected error when postgres is unreachable")
	}
}

func TestCleanupReimportedSoftDeletedVideosRemovesActiveDuplicatePath(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cleanup.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	deleted := models.Video{Name: "deleted.mp4", Path: "/tmp/deleted.mp4", Directory: "/tmp", Size: 1}
	activeReimport := models.Video{Name: "deleted.mp4", Path: "/tmp/deleted.mp4", Directory: "/tmp", Size: 1}
	activeNormal := models.Video{Name: "normal.mp4", Path: "/tmp/normal.mp4", Directory: "/tmp", Size: 1}
	if err := db.Create(&deleted).Error; err != nil {
		t.Fatalf("create deleted fixture: %v", err)
	}
	if err := db.Delete(&deleted).Error; err != nil {
		t.Fatalf("soft delete fixture: %v", err)
	}
	if err := db.Create(&activeReimport).Error; err != nil {
		t.Fatalf("create active reimport fixture: %v", err)
	}
	if err := db.Create(&activeNormal).Error; err != nil {
		t.Fatalf("create normal fixture: %v", err)
	}

	if err := cleanupReimportedSoftDeletedVideos(db); err != nil {
		t.Fatalf("cleanup reimported videos: %v", err)
	}

	var activeCount int64
	if err := db.Model(&models.Video{}).Where("path = ?", "/tmp/deleted.mp4").Count(&activeCount).Error; err != nil {
		t.Fatalf("count active reimport: %v", err)
	}
	if activeCount != 0 {
		t.Fatalf("expected reimported active row to be soft-deleted, got %d", activeCount)
	}

	if err := db.First(&activeNormal, activeNormal.ID).Error; err != nil {
		t.Fatalf("normal active row should remain visible: %v", err)
	}
}

func TestSettingsAutoMigrateAddsAITagBatchFieldsToLegacyRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy_settings.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开旧版测试库失败: %v", err)
	}
	if err := db.Exec(`CREATE TABLE settings (id integer primary key, ai_tagging_frame_count integer)`).Error; err != nil {
		t.Fatalf("创建旧版 settings 表失败: %v", err)
	}
	if err := db.Exec(`INSERT INTO settings(id, ai_tagging_frame_count) VALUES (1, 5)`).Error; err != nil {
		t.Fatalf("写入旧版设置失败: %v", err)
	}
	if err := db.AutoMigrate(&models.Settings{}); err != nil {
		t.Fatalf("升级 settings 表失败: %v", err)
	}

	var settings models.Settings
	if err := db.First(&settings, 1).Error; err != nil {
		t.Fatalf("读取升级后的旧设置失败: %v", err)
	}
	if settings.AITaggingImagesPerRequest != 10 {
		t.Fatalf("旧设置应获得单次请求图片上限默认值 10，实际 %d", settings.AITaggingImagesPerRequest)
	}
	if settings.AITaggingFrameCount != 5 {
		t.Fatalf("兼容字段不应在迁移时丢失，实际 %d", settings.AITaggingFrameCount)
	}
}
