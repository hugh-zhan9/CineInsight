package database

import (
	"path/filepath"
	"testing"
	"time"
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

func TestMediaDetailSchemaPreservesLegacyVideosAndRelationships(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "media_details.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE videos (
		id integer primary key autoincrement,
		name text,
		path text,
		directory text,
		size integer,
		duration real,
		resolution text,
		width integer,
		height integer,
		is_stale numeric not null default 0,
		play_count integer not null default 0,
		random_play_count integer not null default 0,
		is_favorite numeric not null default 0,
		is_watched numeric not null default 0,
		watch_position_seconds real not null default 0,
		created_at datetime,
		updated_at datetime,
		deleted_at datetime
	)`).Error; err != nil {
		t.Fatalf("create legacy videos table: %v", err)
	}
	if err := db.Exec(`INSERT INTO videos(name,path,directory,size,created_at,updated_at) VALUES ('legacy.mp4','/tmp/legacy.mp4','/tmp',1,?,?)`, time.Now(), time.Now()).Error; err != nil {
		t.Fatalf("insert legacy video: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate media details: %v", err)
	}
	assertSQLiteIndexColumns(t, db, "idx_collection_videos_collection_position", []string{"collection_id", "position", "video_id"})
	assertSQLiteIndexColumns(t, db, "idx_collection_videos_video", []string{"video_id", "collection_id"})

	var legacy models.Video
	if err := db.First(&legacy, 1).Error; err != nil {
		t.Fatalf("load migrated legacy video: %v", err)
	}
	if legacy.DisplayTitle != "" || legacy.OriginalTitle != "" || legacy.PersonalRating != nil {
		t.Fatalf("legacy detail defaults changed: %+v", legacy)
	}

	personA := models.Person{DisplayName: "同名演员", OriginalName: "Actor A"}
	personB := models.Person{DisplayName: "同名演员", OriginalName: "Actor B"}
	if err := db.Create(&personA).Error; err != nil {
		t.Fatalf("create first same-name person: %v", err)
	}
	if err := db.Create(&personB).Error; err != nil {
		t.Fatalf("same-name people must be allowed: %v", err)
	}
	link := models.VideoPerson{VideoID: legacy.ID, PersonID: personA.ID}
	if err := db.Create(&link).Error; err != nil {
		t.Fatalf("create video-person link: %v", err)
	}
	if err := db.Delete(&legacy).Error; err != nil {
		t.Fatalf("soft delete video: %v", err)
	}
	var linkCount int64
	if err := db.Model(&models.VideoPerson{}).Where("video_id = ?", legacy.ID).Count(&linkCount).Error; err != nil {
		t.Fatalf("count preserved person links: %v", err)
	}
	if linkCount != 1 {
		t.Fatalf("soft delete must preserve person links, got %d", linkCount)
	}

	collection := models.MediaCollection{Name: "合集", NormalizedName: "合集"}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatalf("create collection: %v", err)
	}
	duplicate := models.MediaCollection{Name: "合集", NormalizedName: "合集"}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("active normalized collection names must be unique")
	}
	if err := db.Delete(&collection).Error; err != nil {
		t.Fatalf("soft delete collection: %v", err)
	}
	if err := db.Create(&duplicate).Error; err != nil {
		t.Fatalf("deleted collection name should be reusable: %v", err)
	}
}

func assertSQLiteIndexColumns(t *testing.T, db *gorm.DB, indexName string, want []string) {
	t.Helper()
	var rows []struct {
		Seq  int
		Name string
	}
	if err := db.Raw("PRAGMA index_info('" + indexName + "')").Scan(&rows).Error; err != nil {
		t.Fatalf("inspect index %s: %v", indexName, err)
	}
	if len(rows) != len(want) {
		t.Fatalf("index %s columns=%v want=%v", indexName, rows, want)
	}
	for index := range want {
		if rows[index].Name != want[index] {
			t.Fatalf("index %s column[%d]=%q want=%q", indexName, index, rows[index].Name, want[index])
		}
	}
}

func TestMediaDetailSchemaRejectsInvalidPersonalRating(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "rating.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Video{}); err != nil {
		t.Fatalf("automigrate video: %v", err)
	}
	invalid := 0.3
	video := models.Video{Name: "invalid.mp4", Path: "/tmp/invalid.mp4", Directory: "/tmp", PersonalRating: &invalid}
	if err := db.Create(&video).Error; err == nil {
		t.Fatal("non-half-step personal rating must be rejected")
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
