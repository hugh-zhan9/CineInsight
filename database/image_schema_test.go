package database

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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

// openImageSchemaTestDBWithForeignKeys 打开一个开启外键强制的 SQLite；SQLite 默认不强制外键，
// 不显式打开时级联断言会全部空过。
func openImageSchemaTestDBWithForeignKeys(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "image_schema_fk.db") + "?_foreign_keys=on"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite with foreign keys: %v", err)
	}
	var enabled int
	if err := db.Raw(`PRAGMA foreign_keys`).Scan(&enabled).Error; err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if enabled != 1 {
		t.Fatal("foreign key enforcement is off, cascade assertions would pass vacuously")
	}
	return db
}

func TestImageAITaggingSchemaMirrorsVideoSideIndexShape(t *testing.T) {
	db := openImageSchemaTestDB(t)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate image schema: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("rerun image schema migration: %v", err)
	}
	for _, model := range []any{
		&models.ImageAITagCandidate{},
		&models.ImageAITagApprovalRecord{},
		&models.ImageAITaggingState{},
	} {
		if !db.Migrator().HasTable(model) {
			t.Fatalf("image AI tagging table missing for %T", model)
		}
	}
	for _, tableName := range []string{
		"image_ai_tag_candidates",
		"image_ai_tag_approval_records",
		"image_ai_tagging_states",
	} {
		if !db.Migrator().HasTable(tableName) {
			t.Fatalf("image AI tagging table name drifted, %s missing", tableName)
		}
	}
	for run := 0; run < 2; run++ {
		EnsureImageAITaggingIndexes(db)
	}
	for _, expectation := range []struct {
		index   string
		columns []string
	}{
		{"idx_image_ai_tag_candidates_image_status", []string{"image_id", "status"}},
		{"idx_image_ai_tag_candidates_matched_status", []string{"matched_tag_id", "status"}},
		{"idx_image_ai_tag_candidates_status_approved", []string{"status", "approved_at"}},
		{"idx_image_ai_tag_candidates_status_rejected", []string{"status", "rejected_at"}},
		{"idx_image_ai_tag_candidates_normalized_name", []string{"normalized_name"}},
		{"idx_image_ai_tag_candidates_confidence", []string{"confidence"}},
		{"idx_image_ai_tag_approval_image_tag", []string{"image_id", "tag_id"}},
		{"idx_image_ai_tag_approval_records_candidate_id", []string{"candidate_id"}},
		{"idx_image_ai_tag_approval_records_image_id", []string{"image_id"}},
		{"idx_image_ai_tag_approval_records_tag_id", []string{"tag_id"}},
		{"idx_image_ai_tagging_states_image_id", []string{"image_id"}},
		{"idx_image_ai_tagging_states_status_processed", []string{"status", "last_processed_at"}},
		{"idx_image_ai_tagging_states_evidence_fingerprint", []string{"evidence_fingerprint"}},
	} {
		assertSQLiteIndexColumns(t, db, expectation.index, expectation.columns)
	}
	// 图片侧是单轮请求，没有 run 历史表，候选表不得带 run_id。
	if db.Migrator().HasColumn(&models.ImageAITagCandidate{}, "run_id") {
		t.Fatal("image candidate table must not carry run_id")
	}
}

func TestImageAITaggingUniqueKeysMirrorVideoSide(t *testing.T) {
	db := openImageSchemaTestDB(t)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate image schema: %v", err)
	}
	EnsureImageAITaggingIndexes(db)

	approval := models.ImageAITagApprovalRecord{ImageID: 1, TagID: 2, CandidateID: 3}
	if err := db.Create(&approval).Error; err != nil {
		t.Fatalf("create approval record: %v", err)
	}
	sameImageAndTag := models.ImageAITagApprovalRecord{ImageID: 1, TagID: 2, CandidateID: 4}
	if err := db.Create(&sameImageAndTag).Error; err == nil {
		t.Fatal("duplicate (image_id, tag_id) approval record must be rejected")
	}
	sameCandidate := models.ImageAITagApprovalRecord{ImageID: 5, TagID: 6, CandidateID: 3}
	if err := db.Create(&sameCandidate).Error; err == nil {
		t.Fatal("duplicate candidate_id approval record must be rejected")
	}

	state := models.ImageAITaggingState{ImageID: 1, Status: models.AITaggingStateStatusPending}
	if err := db.Create(&state).Error; err != nil {
		t.Fatalf("create tagging state: %v", err)
	}
	duplicateState := models.ImageAITaggingState{ImageID: 1, Status: models.AITaggingStateStatusCompleted}
	if err := db.Create(&duplicateState).Error; err == nil {
		t.Fatal("duplicate image_id tagging state must be rejected")
	}
}

func TestImageAITaggingRowsCascadeWhenImageIsHardDeleted(t *testing.T) {
	db := openImageSchemaTestDBWithForeignKeys(t)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate image schema: %v", err)
	}

	tag := models.Tag{Name: "海边"}
	if err := db.Create(&tag).Error; err != nil {
		t.Fatalf("create tag: %v", err)
	}
	deleted := models.Image{Name: "deleted.jpg", Path: "/tmp/deleted.jpg", Directory: "/tmp", Format: "jpg"}
	kept := models.Image{Name: "kept.jpg", Path: "/tmp/kept.jpg", Directory: "/tmp", Format: "jpg"}
	if err := db.Create(&[]*models.Image{&deleted, &kept}).Error; err != nil {
		t.Fatalf("create images: %v", err)
	}

	for _, image := range []*models.Image{&deleted, &kept} {
		candidate := models.ImageAITagCandidate{
			ImageID:        image.ID,
			SuggestedName:  tag.Name,
			NormalizedName: tag.Name,
			MatchedTagID:   &tag.ID,
			Confidence:     models.AITagConfidenceHigh,
			Status:         models.AITagCandidateStatusApproved,
		}
		if err := db.Create(&candidate).Error; err != nil {
			t.Fatalf("create candidate for image %d: %v", image.ID, err)
		}
		approval := models.ImageAITagApprovalRecord{ImageID: image.ID, TagID: tag.ID, CandidateID: candidate.ID}
		if err := db.Create(&approval).Error; err != nil {
			t.Fatalf("create approval record for image %d: %v", image.ID, err)
		}
		state := models.ImageAITaggingState{
			ImageID:             image.ID,
			Status:              models.AITaggingStateStatusCompleted,
			EvidenceFingerprint: "fingerprint",
		}
		if err := db.Create(&state).Error; err != nil {
			t.Fatalf("create tagging state for image %d: %v", image.ID, err)
		}
	}

	if err := db.Unscoped().Delete(&models.Image{}, deleted.ID).Error; err != nil {
		t.Fatalf("hard delete image: %v", err)
	}

	for _, table := range []string{"image_ai_tag_candidates", "image_ai_tag_approval_records", "image_ai_tagging_states"} {
		var orphaned int64
		if err := db.Table(table).Where("image_id = ?", deleted.ID).Count(&orphaned).Error; err != nil {
			t.Fatalf("count %s rows for deleted image: %v", table, err)
		}
		if orphaned != 0 {
			t.Fatalf("%s left %d rows behind after hard delete", table, orphaned)
		}
		var survived int64
		if err := db.Table(table).Where("image_id = ?", kept.ID).Count(&survived).Error; err != nil {
			t.Fatalf("count %s rows for kept image: %v", table, err)
		}
		if survived != 1 {
			t.Fatalf("%s rows for the untouched image = %d want=1", table, survived)
		}
	}

	var tagCount int64
	if err := db.Model(&models.Tag{}).Where("id = ?", tag.ID).Count(&tagCount).Error; err != nil {
		t.Fatalf("count shared tag: %v", err)
	}
	if tagCount != 1 {
		t.Fatal("deleting an image must not remove the shared tag vocabulary row")
	}
}

// modelsWithoutImageAITagging 返回去掉本切片三张新表后的模型集合，用于与全量迁移做结构差分。
func modelsWithoutImageAITagging(t *testing.T) []interface{} {
	t.Helper()
	excluded := map[string]bool{
		"*models.ImageAITagCandidate":      true,
		"*models.ImageAITagApprovalRecord": true,
		"*models.ImageAITaggingState":      true,
	}
	all := models.AllModels()
	filtered := make([]interface{}, 0, len(all))
	for _, model := range all {
		if excluded[fmt.Sprintf("%T", model)] {
			continue
		}
		filtered = append(filtered, model)
	}
	if len(all)-len(filtered) != len(excluded) {
		t.Fatalf("baseline filter matched %d models, want %d", len(all)-len(filtered), len(excluded))
	}
	return filtered
}

// dumpSQLiteTableStructure 把若干表的列、外键与索引摊成有序的文本快照。
// 不直接比对 sqlite_master.sql：GORM 生成 CREATE TABLE 时外键约束的书写顺序不稳定，
// 原文比对会随机失败。
func dumpSQLiteTableStructure(t *testing.T, db *gorm.DB, tables []string) []string {
	t.Helper()
	dumped := make([]string, 0)
	for _, table := range tables {
		var exists int64
		if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&exists).Error; err != nil {
			t.Fatalf("probe table %s: %v", table, err)
		}
		dumped = append(dumped, fmt.Sprintf("table %s exists=%d", table, exists))

		var columns []struct {
			Cid       int
			Name      string
			Type      string
			Notnull   int
			DfltValue *string `gorm:"column:dflt_value"`
			Pk        int
		}
		if err := db.Raw("PRAGMA table_info('" + table + "')").Scan(&columns).Error; err != nil {
			t.Fatalf("inspect columns of %s: %v", table, err)
		}
		for _, column := range columns {
			dflt := "<nil>"
			if column.DfltValue != nil {
				dflt = *column.DfltValue
			}
			dumped = append(dumped, fmt.Sprintf("column %s.%d %s %s notnull=%d default=%s pk=%d", table, column.Cid, column.Name, column.Type, column.Notnull, dflt, column.Pk))
		}

		var foreignKeys []struct {
			Table    string
			From     string
			To       string
			OnUpdate string `gorm:"column:on_update"`
			OnDelete string `gorm:"column:on_delete"`
		}
		if err := db.Raw("PRAGMA foreign_key_list('" + table + "')").Scan(&foreignKeys).Error; err != nil {
			t.Fatalf("inspect foreign keys of %s: %v", table, err)
		}
		foreignKeyLines := make([]string, 0, len(foreignKeys))
		for _, key := range foreignKeys {
			foreignKeyLines = append(foreignKeyLines, fmt.Sprintf("foreignkey %s.%s -> %s.%s on_update=%s on_delete=%s", table, key.From, key.Table, key.To, key.OnUpdate, key.OnDelete))
		}
		sort.Strings(foreignKeyLines)
		dumped = append(dumped, foreignKeyLines...)

		var indexes []struct {
			Name   string
			Unique int
			Origin string
		}
		if err := db.Raw("PRAGMA index_list('" + table + "')").Scan(&indexes).Error; err != nil {
			t.Fatalf("inspect indexes of %s: %v", table, err)
		}
		indexLines := make([]string, 0, len(indexes))
		for _, index := range indexes {
			var indexColumns []struct {
				Seqno int
				Name  string
			}
			if err := db.Raw("PRAGMA index_info('" + index.Name + "')").Scan(&indexColumns).Error; err != nil {
				t.Fatalf("inspect index %s: %v", index.Name, err)
			}
			columnNames := make([]string, 0, len(indexColumns))
			for _, indexColumn := range indexColumns {
				columnNames = append(columnNames, indexColumn.Name)
			}
			indexLines = append(indexLines, fmt.Sprintf("index %s.%s unique=%d origin=%s columns=%s", table, index.Name, index.Unique, index.Origin, strings.Join(columnNames, ",")))
		}
		sort.Strings(indexLines)
		dumped = append(dumped, indexLines...)
	}
	return dumped
}

func TestImageAITaggingTablesLeaveVideoAITagSchemaUntouched(t *testing.T) {
	videoAITagTables := []string{
		"ai_tag_candidates",
		"ai_tagging_runs",
		"ai_tag_approval_records",
		"ai_tagging_states",
		"ai_tag_agent_steps",
	}

	baseline := openImageSchemaTestDB(t)
	if err := baseline.AutoMigrate(modelsWithoutImageAITagging(t)...); err != nil {
		t.Fatalf("automigrate baseline schema: %v", err)
	}
	ensureAITaggingIndexes(baseline)

	current := openImageSchemaTestDB(t)
	if err := current.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate current schema: %v", err)
	}
	ensureAITaggingIndexes(current)
	EnsureImageAITaggingIndexes(current)

	want := dumpSQLiteTableStructure(t, baseline, videoAITagTables)
	got := dumpSQLiteTableStructure(t, current, videoAITagTables)
	if len(want) == 0 {
		t.Fatal("baseline dump is empty, the comparison would be vacuous")
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("video AI tagging schema changed\nbaseline:\n%s\ncurrent:\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
}
