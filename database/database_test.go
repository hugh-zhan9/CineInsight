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

func TestMaintenanceGateWaitsForTransactionsAndRejectsNewOperations(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "maintenance.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := registerMaintenanceCallbacks(db); err != nil {
		t.Fatal(err)
	}
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	transactionStarted := make(chan struct{})
	finishTransaction := make(chan struct{})
	transactionDone := make(chan error, 1)
	go func() {
		transactionDone <- Transaction(func(_ *gorm.DB) error {
			close(transactionStarted)
			<-finishTransaction
			return nil
		})
	}()
	<-transactionStarted

	maintenanceAcquired := make(chan func(), 1)
	go func() { maintenanceAcquired <- BeginMaintenance() }()
	select {
	case <-maintenanceAcquired:
		t.Fatal("maintenance must wait for the active transaction")
	case <-time.After(20 * time.Millisecond):
	}
	close(finishTransaction)
	if err := <-transactionDone; err != nil {
		t.Fatal(err)
	}

	var release func()
	select {
	case release = <-maintenanceAcquired:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not acquire after transaction completed")
	}
	if err := DB.Create(&models.Settings{}).Error; !errors.Is(err, ErrMaintenance) {
		t.Fatalf("direct operation during maintenance error=%v", err)
	}
	if err := Transaction(func(_ *gorm.DB) error { return nil }); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("explicit transaction during maintenance error=%v", err)
	}
	if err := WithMaintenanceAccess(DB).Create(&models.Settings{}).Error; err != nil {
		t.Fatalf("restore lifecycle access during maintenance failed: %v", err)
	}
	release()
	if err := DB.Create(&models.Settings{}).Error; err != nil {
		t.Fatalf("operation after maintenance release failed: %v", err)
	}
}

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

func TestLibraryWatchSettingMigrationKeepsExistingInstallDisabled(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy_watch_settings.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开旧版测试库失败: %v", err)
	}
	if err := db.Exec(`CREATE TABLE settings (id integer primary key, video_extensions text)`).Error; err != nil {
		t.Fatalf("创建旧版 settings 表失败: %v", err)
	}
	if err := db.Exec(`INSERT INTO settings(id, video_extensions) VALUES (1, '.mp4')`).Error; err != nil {
		t.Fatalf("写入旧版设置失败: %v", err)
	}
	if err := db.AutoMigrate(&models.Settings{}); err != nil {
		t.Fatalf("升级 settings 表失败: %v", err)
	}
	if err := migrateLibraryWatchSetting(db, true, false); err != nil {
		t.Fatalf("迁移实时同步设置失败: %v", err)
	}

	var settings models.Settings
	if err := db.First(&settings, 1).Error; err != nil {
		t.Fatalf("读取升级后的设置失败: %v", err)
	}
	if settings.LibraryWatchEnabled {
		t.Fatal("已有安装升级后不应自动开启实时同步")
	}
}

func TestFreshSettingsEnableLibraryWatch(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "fresh_watch_settings.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开新测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&models.Settings{}); err != nil {
		t.Fatalf("迁移新 settings 表失败: %v", err)
	}
	settings := models.Settings{VideoExtensions: ".mp4", LibraryWatchEnabled: true}
	if err := db.Create(&settings).Error; err != nil {
		t.Fatalf("创建新安装默认设置失败: %v", err)
	}
	var loaded models.Settings
	if err := db.First(&loaded, settings.ID).Error; err != nil {
		t.Fatalf("读取新安装设置失败: %v", err)
	}
	if !loaded.LibraryWatchEnabled {
		t.Fatal("新安装应默认开启实时同步")
	}
}

func TestWorkflowFeatureSettingMigrationPreservesSafeUpgradeDefaults(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy_workflow_settings.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy settings database: %v", err)
	}
	if err := db.Exec(`CREATE TABLE settings (id integer primary key, video_extensions text)`).Error; err != nil {
		t.Fatalf("create legacy settings: %v", err)
	}
	if err := db.Exec(`INSERT INTO settings(id, video_extensions) VALUES (1, '.mp4')`).Error; err != nil {
		t.Fatalf("insert legacy settings: %v", err)
	}
	if err := db.AutoMigrate(&models.Settings{}); err != nil {
		t.Fatalf("migrate settings: %v", err)
	}
	if err := migrateWorkflowFeatureSettings(db, true, false, false); err != nil {
		t.Fatalf("migrate workflow settings: %v", err)
	}
	if err := migrateWorkflowFeatureSettings(db, true, true, true); err != nil {
		t.Fatalf("rerun workflow migration: %v", err)
	}
	var settings models.Settings
	if err := db.First(&settings, 1).Error; err != nil {
		t.Fatalf("load migrated settings: %v", err)
	}
	if settings.LocalMetadataEnabled {
		t.Fatal("upgrade must not enable automatic local metadata writes")
	}
	if !settings.AIQualityEnabled {
		t.Fatal("passive AI quality view should be visible after upgrade")
	}
}

func TestAIAttributionMigrationPreservesLegacyCandidatesAndRelations(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy_ai.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy ai database: %v", err)
	}
	approvedAt := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	if err := db.Exec(`CREATE TABLE ai_tag_candidates (
		id integer primary key autoincrement,
		video_id integer,
		suggested_name text not null,
		normalized_name text,
		matched_tag_id integer,
		confidence text not null,
		reasoning text,
		source_summary text,
		status text not null default 'pending',
		created_at datetime,
		updated_at datetime,
		approved_at datetime,
		rejected_at datetime
	)`).Error; err != nil {
		t.Fatalf("create legacy candidates table: %v", err)
	}
	if err := db.Exec(`INSERT INTO ai_tag_candidates(video_id,suggested_name,normalized_name,matched_tag_id,confidence,status,created_at,updated_at,approved_at)
		VALUES (1,'旧标签','旧标签',7,'high','approved',?,?,?)`, approvedAt, approvedAt, approvedAt).Error; err != nil {
		t.Fatalf("insert legacy candidate: %v", err)
	}
	if err := db.Exec(`CREATE TABLE video_same_source_relations (
		id integer primary key autoincrement,
		video_a_id integer,
		video_b_id integer,
		video_a_fingerprint text not null,
		video_b_fingerprint text not null,
		status text not null,
		confidence text not null default '',
		reasoning text not null default '',
		detection_version text not null,
		is_unread numeric not null default 1,
		rejected_at datetime,
		created_at datetime,
		updated_at datetime
	)`).Error; err != nil {
		t.Fatalf("create legacy relations table: %v", err)
	}
	if err := db.Exec(`INSERT INTO video_same_source_relations(video_a_id,video_b_id,video_a_fingerprint,video_b_fingerprint,status,confidence,detection_version,is_unread,created_at,updated_at)
		VALUES (1,2,'fp-a','fp-b','detected','high','same-source-v1',1,?,?)`, approvedAt, approvedAt).Error; err != nil {
		t.Fatalf("insert legacy relation: %v", err)
	}

	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("automigrate ai attribution: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("rerun ai attribution migration: %v", err)
	}

	var candidate models.AITagCandidate
	if err := db.First(&candidate, 1).Error; err != nil {
		t.Fatalf("load migrated candidate: %v", err)
	}
	if candidate.RunID != nil {
		t.Fatalf("legacy candidate must stay unattributed, got run_id=%v", *candidate.RunID)
	}
	if candidate.Status != models.AITagCandidateStatusApproved || candidate.SuggestedName != "旧标签" || candidate.Confidence != models.AITagConfidenceHigh {
		t.Fatalf("legacy candidate fields changed: %+v", candidate)
	}
	if candidate.ApprovedAt == nil || !candidate.ApprovedAt.UTC().Equal(approvedAt) {
		t.Fatalf("legacy approved_at changed: %v want %v", candidate.ApprovedAt, approvedAt)
	}

	var relation models.VideoSameSourceRelation
	if err := db.First(&relation, 1).Error; err != nil {
		t.Fatalf("load migrated relation: %v", err)
	}
	if relation.CurrentEvaluationID != nil {
		t.Fatalf("legacy relation must stay unlinked, got evaluation=%v", *relation.CurrentEvaluationID)
	}
	if relation.Status != models.VideoSameSourceStatusDetected || relation.DetectionVersion != "same-source-v1" || !relation.IsUnread {
		t.Fatalf("legacy relation fields changed: %+v", relation)
	}

	var runCount int64
	if err := db.Model(&models.AITaggingRun{}).Count(&runCount).Error; err != nil {
		t.Fatalf("count runs: %v", err)
	}
	var evaluationCount int64
	if err := db.Model(&models.AISameSourceEvaluation{}).Count(&evaluationCount).Error; err != nil {
		t.Fatalf("count evaluations: %v", err)
	}
	if runCount != 0 || evaluationCount != 0 {
		t.Fatalf("migration must not backfill AI history: runs=%d evaluations=%d", runCount, evaluationCount)
	}
}

func TestFreshSettingsEnableWorkflowFeatures(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "fresh_workflow_settings.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open fresh settings database: %v", err)
	}
	if err := db.AutoMigrate(&models.Settings{}); err != nil {
		t.Fatalf("migrate settings: %v", err)
	}
	settings := models.Settings{VideoExtensions: ".mp4", LocalMetadataEnabled: true, AIQualityEnabled: true}
	if err := db.Create(&settings).Error; err != nil {
		t.Fatalf("create fresh defaults: %v", err)
	}
	var loaded models.Settings
	if err := db.First(&loaded, settings.ID).Error; err != nil {
		t.Fatalf("load fresh settings: %v", err)
	}
	if !loaded.LocalMetadataEnabled || !loaded.AIQualityEnabled {
		t.Fatalf("fresh workflow defaults = %#v", loaded)
	}
}
