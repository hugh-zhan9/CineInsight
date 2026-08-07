package database

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openImageSchemaTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "image_schema.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func TestImageSchemaMigrationIsRepeatableAndCreatesAllTables(t *testing.T) {
	db := openImageSchemaTestDB(t)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate image schema: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("rerun image schema migration: %v", err)
	}
	for _, model := range []any{
		&models.Image{},
		&models.ImageDirectory{},
		&models.ImageTrashEntry{},
		&models.ImageAIDescription{},
		&models.ImageNearDuplicateDismissal{},
	} {
		if !db.Migrator().HasTable(model) {
			t.Fatalf("image table missing for %T", model)
		}
	}
	if !db.Migrator().HasTable("image_tags") {
		t.Fatal("image_tags join table missing")
	}
}

func TestImagePathUniqueIndexReleasesPathAfterSoftDelete(t *testing.T) {
	db := openImageSchemaTestDB(t)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate image schema: %v", err)
	}
	if err := ensureImagePathUniqueIndex(db); err != nil {
		t.Fatalf("ensure image path unique index: %v", err)
	}
	if err := ensureImagePathUniqueIndex(db); err != nil {
		t.Fatalf("rerun image path unique index: %v", err)
	}

	first := models.Image{Name: "a.jpg", Path: "/tmp/a.jpg", Directory: "/tmp", Size: 1, Format: "jpg"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first image: %v", err)
	}
	duplicate := models.Image{Name: "a.jpg", Path: "/tmp/a.jpg", Directory: "/tmp", Size: 1, Format: "jpg"}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("active duplicate image path must be rejected")
	}
	if err := db.Delete(&first).Error; err != nil {
		t.Fatalf("soft delete image: %v", err)
	}
	reimported := models.Image{Name: "a.jpg", Path: "/tmp/a.jpg", Directory: "/tmp", Size: 1, Format: "jpg"}
	if err := db.Create(&reimported).Error; err != nil {
		t.Fatalf("soft-deleted path must be reusable: %v", err)
	}
}

func TestEnsureImageQueryIndexesIsIdempotent(t *testing.T) {
	db := openImageSchemaTestDB(t)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate image schema: %v", err)
	}
	for run := 0; run < 2; run++ {
		ensureImageQueryIndexes(db)
	}
	for _, indexName := range []string{
		"idx_images_directory_active",
		"idx_images_size_active",
		"idx_images_favorite_active",
		"idx_images_created_active",
		"idx_images_rating_active",
		"idx_images_taken_sort_active",
		"idx_image_tags_image_tag",
		"idx_image_tags_tag_image",
	} {
		var count int64
		if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, indexName).Scan(&count).Error; err != nil {
			t.Fatalf("inspect index %s: %v", indexName, err)
		}
		if count != 1 {
			t.Fatalf("index %s count=%d want=1", indexName, count)
		}
	}
	assertSQLiteIndexColumns(t, db, "idx_images_created_active", []string{"created_at", "id"})
	assertSQLiteIndexColumns(t, db, "idx_image_tags_tag_image", []string{"tag_id", "image_id"})
}

func TestImageSchemaRejectsInvalidPersonalRating(t *testing.T) {
	db := openImageSchemaTestDB(t)
	if err := db.AutoMigrate(&models.Image{}); err != nil {
		t.Fatalf("automigrate image: %v", err)
	}
	invalid := 0.3
	image := models.Image{Name: "invalid.jpg", Path: "/tmp/invalid.jpg", Directory: "/tmp", PersonalRating: &invalid}
	if err := db.Create(&image).Error; err == nil {
		t.Fatal("non-half-step personal rating must be rejected")
	}
	valid := 7.5
	image = models.Image{Name: "valid.jpg", Path: "/tmp/valid.jpg", Directory: "/tmp", PersonalRating: &valid}
	if err := db.Create(&image).Error; err != nil {
		t.Fatalf("half-step personal rating must be accepted: %v", err)
	}
}

func TestImageTagsShareTagTableWithVideos(t *testing.T) {
	db := openImageSchemaTestDB(t)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate image schema: %v", err)
	}
	tag := models.Tag{Name: "共享标签"}
	if err := db.Create(&tag).Error; err != nil {
		t.Fatalf("create tag: %v", err)
	}
	image := models.Image{Name: "tagged.jpg", Path: "/tmp/tagged.jpg", Directory: "/tmp", Tags: []models.Tag{tag}}
	if err := db.Create(&image).Error; err != nil {
		t.Fatalf("create tagged image: %v", err)
	}
	var linkCount int64
	if err := db.Table("image_tags").Where("image_id = ? AND tag_id = ?", image.ID, tag.ID).Count(&linkCount).Error; err != nil {
		t.Fatalf("count image tag links: %v", err)
	}
	if linkCount != 1 {
		t.Fatalf("image tag link count=%d want=1", linkCount)
	}
}

func TestImageTrashEntryRejectsDuplicateImage(t *testing.T) {
	db := openImageSchemaTestDB(t)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate image schema: %v", err)
	}
	entry := models.ImageTrashEntry{ImageID: 1, ImageName: "a.jpg", OriginalPath: "/tmp/a.jpg", TrashPath: "/tmp/trash/a.jpg", State: "deleted"}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create trash entry: %v", err)
	}
	duplicate := models.ImageTrashEntry{ImageID: 1, ImageName: "a.jpg", OriginalPath: "/tmp/a.jpg", TrashPath: "/tmp/trash/a-2.jpg", State: "deleted"}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate trash entry for the same image must be rejected")
	}
}

func TestImageNearDuplicateDismissalPairIsUnique(t *testing.T) {
	db := openImageSchemaTestDB(t)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate image schema: %v", err)
	}
	dismissal := models.ImageNearDuplicateDismissal{ImageLowID: 1, ImageHighID: 2}
	if err := db.Create(&dismissal).Error; err != nil {
		t.Fatalf("create dismissal: %v", err)
	}
	duplicate := models.ImageNearDuplicateDismissal{ImageLowID: 1, ImageHighID: 2}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate dismissal pair must be rejected")
	}
}

func TestImageSemanticIndexTablesMirrorVideoSideUniqueKeys(t *testing.T) {
	db := openImageSchemaTestDB(t)
	if err := db.AutoMigrate(&models.Image{}); err != nil {
		t.Fatalf("automigrate images: %v", err)
	}
	capability := PrepareImageSemanticVectorStorage(db)
	if capability.Available || capability.Backend != "sqlite" || capability.ReasonCode != "pgvector_requires_postgres" {
		t.Fatalf("unexpected capability: %+v", capability)
	}
	assertSQLiteIndexColumns(t, db, "idx_image_semantic_model_dimension", []string{"image_id", "model_identifier", "dimension"})
	assertSQLiteIndexColumns(t, db, "idx_image_semantic_attempt_image_model_generation", []string{"image_id", "model_identifier", "generation"})

	index := models.ImageSemanticIndex{ImageID: 1, ModelIdentifier: "embed-model", Dimension: 2, Generation: 1, ContentFingerprint: "fp", IndexedAt: time.Now()}
	if err := db.Create(&index).Error; err != nil {
		t.Fatalf("create semantic index row: %v", err)
	}
	duplicateIndex := models.ImageSemanticIndex{ImageID: 1, ModelIdentifier: "embed-model", Dimension: 2, Generation: 2, ContentFingerprint: "fp2", IndexedAt: time.Now()}
	if err := db.Create(&duplicateIndex).Error; err == nil {
		t.Fatal("duplicate (image_id, model_identifier, dimension) must be rejected")
	}

	attempt := models.ImageSemanticIndexAttempt{ImageID: 1, ModelIdentifier: "embed-model", Generation: 1, Status: "failed"}
	if err := db.Create(&attempt).Error; err != nil {
		t.Fatalf("create semantic attempt row: %v", err)
	}
	duplicateAttempt := models.ImageSemanticIndexAttempt{ImageID: 1, ModelIdentifier: "embed-model", Generation: 1, Status: "pending"}
	if err := db.Create(&duplicateAttempt).Error; err == nil {
		t.Fatal("duplicate (image_id, model_identifier, generation) must be rejected")
	}
}

func TestPrepareImageSemanticVectorStorageDegradesExplicitlyWithoutPostgres(t *testing.T) {
	db := openImageSchemaTestDB(t)
	if err := db.AutoMigrate(&models.Image{}); err != nil {
		t.Fatalf("automigrate images: %v", err)
	}
	capability := PrepareImageSemanticVectorStorage(db)
	if capability.Available || capability.Backend != "sqlite" || capability.ReasonCode != "pgvector_requires_postgres" {
		t.Fatalf("unexpected capability: %+v", capability)
	}
	repeated := PrepareImageSemanticVectorStorage(db)
	if repeated.ReasonCode != "pgvector_requires_postgres" {
		t.Fatalf("repeated prepare must stay stable: %+v", repeated)
	}
	for _, model := range []any{&models.SemanticIndexProfile{}, &models.ImageSemanticIndex{}, &models.ImageSemanticIndexAttempt{}} {
		if !db.Migrator().HasTable(model) {
			t.Fatalf("portable image semantic metadata table missing for %T", model)
		}
	}
	if err := UpsertImageSemanticVector(db, capability, 1, "embed-model", 2, 1, []float64{0.1, 0.2}, time.Now()); !errors.Is(err, ErrSemanticVectorUnavailable) {
		t.Fatalf("SQLite pgvector write error = %v", err)
	}
	if err := EnsureImageSemanticVectorANNIndex(db, capability, 2); !errors.Is(err, ErrSemanticVectorUnavailable) {
		t.Fatalf("SQLite ANN index error = %v", err)
	}
}

func TestPrepareImageSemanticVectorStorageDoesNotReturnAvailableWhenExtensionCreationFails(t *testing.T) {
	dialector := postgresNamedDialector{Dialector: sqlite.Open(filepath.Join(t.TempDir(), "image_semantic.db"))}
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		t.Fatalf("open simulated PostgreSQL database: %v", err)
	}
	if err := db.AutoMigrate(&models.Image{}); err != nil {
		t.Fatalf("automigrate images: %v", err)
	}
	capability := PrepareImageSemanticVectorStorage(db)
	if capability.Available || capability.Backend != "postgres" || capability.ReasonCode != "extension_unavailable" || capability.Message == "" {
		t.Fatalf("extension failure was not reported explicitly: %+v", capability)
	}
	if !db.Migrator().HasTable(&models.ImageSemanticIndex{}) {
		t.Fatal("portable migration should remain available after extension failure")
	}
}

func TestFreshSettingsPersistImageExtensionsDefault(t *testing.T) {
	if DefaultImageExtensions != ".jpg,.jpeg,.png,.gif,.webp,.heic,.heif,.dng,.cr2,.cr3,.nef,.arw,.orf,.raf,.rw2" {
		t.Fatalf("default image extensions drifted: %s", DefaultImageExtensions)
	}
	db := openImageSchemaTestDB(t)
	if err := db.AutoMigrate(&models.Settings{}); err != nil {
		t.Fatalf("migrate settings: %v", err)
	}
	settings := models.Settings{VideoExtensions: ".mp4", ImageExtensions: DefaultImageExtensions}
	if err := db.Create(&settings).Error; err != nil {
		t.Fatalf("create fresh settings: %v", err)
	}
	var loaded models.Settings
	if err := db.First(&loaded, settings.ID).Error; err != nil {
		t.Fatalf("load fresh settings: %v", err)
	}
	if loaded.ImageExtensions != DefaultImageExtensions {
		t.Fatalf("fresh image extensions = %q", loaded.ImageExtensions)
	}
}

func TestLegacySettingsUpgradeLeavesImageExtensionsEmpty(t *testing.T) {
	db := openImageSchemaTestDB(t)
	if err := db.Exec(`CREATE TABLE settings (id integer primary key, video_extensions text)`).Error; err != nil {
		t.Fatalf("create legacy settings: %v", err)
	}
	if err := db.Exec(`INSERT INTO settings(id, video_extensions) VALUES (1, '.mp4')`).Error; err != nil {
		t.Fatalf("insert legacy settings: %v", err)
	}
	if err := db.AutoMigrate(&models.Settings{}); err != nil {
		t.Fatalf("migrate legacy settings: %v", err)
	}
	var settings models.Settings
	if err := db.First(&settings, 1).Error; err != nil {
		t.Fatalf("load migrated settings: %v", err)
	}
	if settings.ImageExtensions != "" {
		t.Fatalf("legacy image extensions must stay empty for caller-side fallback, got %q", settings.ImageExtensions)
	}
}
