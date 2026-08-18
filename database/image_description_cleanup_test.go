package database

import (
	"path/filepath"
	"testing"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// openLegacyCleanupTestDB 建一个"改造前"形态的库：正常迁移之后，手工补回已经从代码里
// 删掉的 image_ai_descriptions 表，模拟老用户升级上来的数据库。
func openLegacyCleanupTestDB(t *testing.T, withLegacyTable bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cleanup.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	// 语义索引三件套不在 AllModels 里，生产由 PrepareImageSemanticVectorStorage 单独建。
	if err := db.AutoMigrate(&models.SemanticIndexProfile{}, &models.ImageSemanticIndex{},
		&models.ImageSemanticIndexAttempt{}, &models.VideoSemanticIndex{}); err != nil {
		t.Fatalf("迁移语义索引表失败: %v", err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS image_semantic_vectors (
		image_id INTEGER NOT NULL,
		model_identifier TEXT NOT NULL,
		dimension INTEGER NOT NULL,
		embedding TEXT NOT NULL,
		PRIMARY KEY (image_id, model_identifier, dimension)
	)`).Error; err != nil {
		t.Fatalf("建图片语义向量表失败: %v", err)
	}
	if withLegacyTable {
		if err := db.Exec(`CREATE TABLE image_ai_descriptions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			image_id INTEGER NOT NULL,
			status TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT ''
		)`).Error; err != nil {
			t.Fatalf("建遗留描述表失败: %v", err)
		}
	}
	return db
}

func seedLegacyDescriptionData(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, imageID := range []uint{1, 2} {
		if err := db.Exec(`INSERT INTO image_ai_descriptions(image_id, status, description) VALUES (?, 'completed', ?)`,
			imageID, "一段描述").Error; err != nil {
			t.Fatalf("插入描述行失败: %v", err)
		}
		if err := db.Exec(`INSERT INTO image_semantic_vectors(image_id, model_identifier, dimension, embedding) VALUES (?, 'embed-v1', 2, '[0.1,0.2]')`,
			imageID).Error; err != nil {
			t.Fatalf("插入图片向量失败: %v", err)
		}
		if err := db.Create(&models.ImageSemanticIndex{
			ImageID: imageID, ModelIdentifier: "embed-v1", Dimension: 2, Generation: 1,
			ContentFingerprint: "fp",
		}).Error; err != nil {
			t.Fatalf("插入图片索引记录失败: %v", err)
		}
	}
	// 视频侧同类数据：清理必须一行都不碰。
	if err := db.Create(&models.VideoSemanticIndex{
		VideoID: 9, ModelIdentifier: "embed-v1", Dimension: 2, Generation: 1,
		ContentFingerprint: "video-fp",
	}).Error; err != nil {
		t.Fatalf("插入视频索引记录失败: %v", err)
	}
}

func countRows(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var count int64
	if err := db.Table(table).Count(&count).Error; err != nil {
		t.Fatalf("统计 %s 失败: %v", table, err)
	}
	return count
}

// TestCleanupLegacyImageDescriptionsRemovesDataAndTable 钉住 2026-08-18 裁决的删除动作：
// 描述行、图片语义向量与索引记录全部清空，描述表被 DROP。
func TestCleanupLegacyImageDescriptionsRemovesDataAndTable(t *testing.T) {
	db := openLegacyCleanupTestDB(t, true)
	seedLegacyDescriptionData(t, db)

	if countRows(t, db, "image_ai_descriptions") != 2 {
		t.Fatal("夹具本身应有描述行，否则这个测试没有意义")
	}

	CleanupLegacyImageDescriptions(db)

	if db.Migrator().HasTable("image_ai_descriptions") {
		t.Fatal("描述表应被删除")
	}
	if got := countRows(t, db, "image_semantic_vectors"); got != 0 {
		t.Fatalf("图片语义向量应清空，实际 %d 行", got)
	}
	var imageIndexes int64
	if err := db.Model(&models.ImageSemanticIndex{}).Count(&imageIndexes).Error; err != nil {
		t.Fatalf("统计图片索引记录失败: %v", err)
	}
	if imageIndexes != 0 {
		t.Fatalf("图片索引记录应清空，实际 %d 行", imageIndexes)
	}
	// 视频侧一行未动。
	var videoIndexes int64
	if err := db.Model(&models.VideoSemanticIndex{}).Count(&videoIndexes).Error; err != nil {
		t.Fatalf("统计视频索引记录失败: %v", err)
	}
	if videoIndexes != 1 {
		t.Fatalf("视频索引记录不该被清理触碰，实际 %d 行", videoIndexes)
	}
}

// TestCleanupLegacyImageDescriptionsIsIdempotent 钉住幂等：清理是启动时无条件调用的，
// 第二次以及全新安装都必须是纯空转，不能报错也不能误删别的东西。
func TestCleanupLegacyImageDescriptionsIsIdempotent(t *testing.T) {
	db := openLegacyCleanupTestDB(t, true)
	seedLegacyDescriptionData(t, db)

	CleanupLegacyImageDescriptions(db)
	// 表已删；再次调用必须空转。
	CleanupLegacyImageDescriptions(db)
	if db.Migrator().HasTable("image_ai_descriptions") {
		t.Fatal("第二次调用不应把表建回来")
	}

	// 全新安装：从来没有过描述表。清理必须不动任何数据。
	fresh := openLegacyCleanupTestDB(t, false)
	if err := fresh.Exec(`INSERT INTO image_semantic_vectors(image_id, model_identifier, dimension, embedding) VALUES (5, 'embed-v1', 2, '[0.3,0.4]')`).Error; err != nil {
		t.Fatalf("插入向量失败: %v", err)
	}
	CleanupLegacyImageDescriptions(fresh)
	if got := countRows(t, fresh, "image_semantic_vectors"); got != 1 {
		t.Fatalf("没有描述表时不该删除任何向量，实际剩 %d 行", got)
	}
}

// TestCleanupLegacyImageDescriptionsSurvivesNilDB 钉住调用侧的安全性：
// Init 里无条件调用，db 为 nil 时不能 panic。
func TestCleanupLegacyImageDescriptionsSurvivesNilDB(t *testing.T) {
	CleanupLegacyImageDescriptions(nil)
}
