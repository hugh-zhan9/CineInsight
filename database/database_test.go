package database

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/models"

	"github.com/joho/godotenv"
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
	// Init 会读数据目录下的 .env；不隔离家目录就会读到真实安装的配置。
	t.Setenv("HOME", t.TempDir())
	// APP02 / m11：Init 经 resolveStartupBackend 解析后端并写下启动缓存；测试结束时还原，
	// 否则本进程之后的用例都会沿用这里解析出的 postgres。
	resetBackendStartupForTest(t)
	unsetEnvForTest(t, "DB_BACKEND", "PREVIOUS_BACKEND")
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
	// 接入点：后端由启动缓存解析（PG_HOST → postgres），DB_BACKEND 不来自进程环境。
	backendStartup.mu.Lock()
	resolved, backend, fromEnv := backendStartup.resolved, backendStartup.backend, backendStartup.fromProcessEnv
	backendStartup.mu.Unlock()
	if !resolved || backend != BackendPostgres || fromEnv {
		t.Fatalf("Init 应经 resolveStartupBackend 记下启动时的后端: resolved=%v backend=%s fromEnv=%v", resolved, backend, fromEnv)
	}
}

// APP02 / m4：SQLite 启动时（打开库之前）清掉上次恢复崩溃留下的临时库文件；Init 同样经启动缓存解析后端。
func TestAPP02InitSweepsSQLiteRestoreLeftoversBeforeOpening(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetBackendStartupForTest(t)
	unsetEnvForTest(t, "PREVIOUS_BACKEND", "PG_HOST")
	t.Setenv("DB_BACKEND", "sqlite")
	libraryDir := t.TempDir()
	livePath := filepath.Join(libraryDir, "library.db")
	t.Setenv("SQLITE_PATH", livePath)
	leftover := filepath.Join(libraryDir, ".library.db.cineinsight-restore-98765.tmp")
	if err := os.WriteFile(leftover, []byte("half restored"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := DB
	t.Cleanup(func() {
		_ = Close()
		DB = previous
	})
	if err := Init(); err != nil {
		t.Fatalf("SQLite 启动失败: %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("启动时应清掉遗留的恢复临时库: %v", err)
	}
	if !BackendFromProcessEnv() || ActiveBackend() != BackendSQLite {
		t.Fatalf("Init 应经启动缓存记下进程环境给出的 sqlite: fromEnv=%v backend=%s", BackendFromProcessEnv(), ActiveBackend())
	}
}

// APP02 / m4：清扫只删库目录里名字符合 `.<库文件名>.cineinsight-restore-*.tmp` 的普通文件；
// 其他库的临时文件、名字不全的、目录、符号链接（连同它指向的文件）一概不动。
func TestAPP02SweepSQLiteRestoreTempsOnlyRemovesMatchingRegularFiles(t *testing.T) {
	dir := t.TempDir()
	livePath := filepath.Join(dir, "library.db")
	outside := filepath.Join(t.TempDir(), "target.tmp")
	if err := os.WriteFile(outside, []byte("user file"), 0600); err != nil {
		t.Fatal(err)
	}
	matching := []string{".library.db.cineinsight-restore-1.tmp", ".library.db.cineinsight-restore-abc123.tmp"}
	kept := []string{
		".library.db.cineinsight-restore-.tmp",      // 没有随机串
		".library.db.cineinsight-restore-1.tmp.bak", // 后缀不对
		".other.db.cineinsight-restore-1.tmp",       // 别的库
		"library.db.cineinsight-restore-1.tmp",      // 不是隐藏名
		".cineinsight-restore-1.tmp",                // 备份流程的临时文件
		"library.db",
	}
	for _, name := range append(append([]string{}, matching...), kept...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	namedDir := filepath.Join(dir, ".library.db.cineinsight-restore-dir.tmp")
	if err := os.MkdirAll(namedDir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".library.db.cineinsight-restore-link.tmp")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	if removed := SweepSQLiteRestoreTemps(livePath); removed != len(matching) {
		t.Fatalf("应只删 %d 个匹配的普通文件，实际 %d", len(matching), removed)
	}
	for _, name := range matching {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s 应被删除: %v", name, err)
		}
	}
	for _, path := range append([]string{namedDir, link, outside}, func() []string {
		paths := make([]string, 0, len(kept))
		for _, name := range kept {
			paths = append(paths, filepath.Join(dir, name))
		}
		return paths
	}()...) {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("%s 不应被删除: %v", path, err)
		}
	}
	if pattern := SQLiteRestoreTempPattern(livePath); pattern != ".library.db.cineinsight-restore-*.tmp" {
		t.Fatalf("临时库命名模式不对: %s", pattern)
	}
	if removed := SweepSQLiteRestoreTemps(filepath.Join(t.TempDir(), "gone", "library.db")); removed != 0 {
		t.Fatalf("库目录不存在时什么都不删: %d", removed)
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

// LIB04：cleanupReimportedSoftDeletedVideos 已退役。它曾在每次启动时把「与某条软删行同
// 路径」的活跃视频直接软删，与「身份不同则新建记录」正面冲突——新收录的文件会在下次
// 启动时被静默删除。这条用例断言相反的行为：同路径的活跃行经过 ApplySchema 之后仍然活跃，
// 反复启动也一样；软删行原样保留。
func TestCleanupReimportedSoftDeletedVideosRetiredLIB04KeepsActiveDuplicatePath(t *testing.T) {
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

	for round := 1; round <= 2; round++ {
		if err := ApplySchema(db); err != nil {
			t.Fatalf("第 %d 次 ApplySchema 失败: %v", round, err)
		}
		var activeCount int64
		if err := db.Model(&models.Video{}).Where("path = ?", "/tmp/deleted.mp4").Count(&activeCount).Error; err != nil {
			t.Fatalf("count active reimport: %v", err)
		}
		if activeCount != 1 {
			t.Fatalf("第 %d 次 ApplySchema 后同路径活跃行应仍然活跃，实际活跃行数 %d", round, activeCount)
		}
		var reloaded models.Video
		if err := db.First(&reloaded, activeReimport.ID).Error; err != nil {
			t.Fatalf("第 %d 次 ApplySchema 后重导入的活跃行应仍可见: %v", round, err)
		}
		if err := db.First(&activeNormal, activeNormal.ID).Error; err != nil {
			t.Fatalf("normal active row should remain visible: %v", err)
		}
		var softDeleted int64
		if err := db.Unscoped().Model(&models.Video{}).Where("id = ? AND deleted_at IS NOT NULL", deleted.ID).Count(&softDeleted).Error; err != nil {
			t.Fatalf("count soft deleted: %v", err)
		}
		if softDeleted != 1 {
			t.Fatalf("软删行应原样保留，实际 %d", softDeleted)
		}
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

func TestResolveBackendKeepsExistingPostgresInstallsOnPostgres(t *testing.T) {
	cases := []struct {
		name    string
		env     BackendEnv
		want    Backend
		wantErr bool
	}{
		{
			// 这一条是本次改动最硬的兼容性约束：现有安装的 .env 里有 PG_HOST
			// 而没有 DB_BACKEND，升级后必须仍然连原来那个库。判错会让用户看到
			// 一个空片库并以为数据丢了。
			name: "现有安装：有 PG_HOST 未设 DB_BACKEND",
			env:  BackendEnv{PGHost: "127.0.0.1"},
			want: BackendPostgres,
		},
		{
			name: "新机器：两者都没有",
			env:  BackendEnv{},
			want: BackendSQLite,
		},
		{
			name: "显式选 sqlite 时即使有 PG_HOST 也走 sqlite",
			env:  BackendEnv{Backend: "sqlite", PGHost: "127.0.0.1"},
			want: BackendSQLite,
		},
		{
			name: "显式选 postgres",
			env:  BackendEnv{Backend: "postgres", PGHost: "127.0.0.1"},
			want: BackendPostgres,
		},
		{
			name: "大小写与空白不敏感",
			env:  BackendEnv{Backend: "  SQLite "},
			want: BackendSQLite,
		},
		{
			// 静默回退会让用户以为连上了 A 实际连的是 B，必须报错。
			name:    "非法取值直接报错而不是回退",
			env:     BackendEnv{Backend: "mysql", PGHost: "127.0.0.1"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveBackend(tc.env)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际返回 %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("判定失败: %v", err)
			}
			if got != tc.want {
				t.Fatalf("后端判定错误: got=%s want=%s", got, tc.want)
			}
		})
	}
}

func TestSQLitePathPrefersOverrideAndNeverAdoptsLegacyFile(t *testing.T) {
	dataDir := t.TempDir()

	t.Setenv("SQLITE_PATH", "")
	got := SQLitePath(dataDir)
	want := filepath.Join(dataDir, DefaultSQLiteFileName)
	if got != want {
		t.Fatalf("默认路径错误: got=%s want=%s", got, want)
	}
	// 历史库文件即使存在也不该被默认路径命中——静默采纳一份很久以前的库，
	// 用户会看到陈旧片库且看不出发生了什么。
	if filepath.Base(got) == LegacySQLiteFileName {
		t.Fatalf("默认路径不得复用历史库文件名")
	}

	t.Setenv("SQLITE_PATH", "/tmp/custom-library.db")
	if got := SQLitePath(dataDir); got != "/tmp/custom-library.db" {
		t.Fatalf("SQLITE_PATH 覆盖未生效: %s", got)
	}
}

func TestOpenBackendCreatesSQLiteFileAndRejectsUnknownBackend(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SQLITE_PATH", filepath.Join(dataDir, "nested", "lib.db"))

	db, err := openBackend(BackendSQLite, dataDir)
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接失败: %v", err)
	}
	defer sqlDB.Close()
	if _, err := os.Stat(filepath.Join(dataDir, "nested", "lib.db")); err != nil {
		t.Fatalf("库文件未创建: %v", err)
	}

	if _, err := openBackend(Backend("mysql"), dataDir); err == nil {
		t.Fatalf("未知后端应报错")
	}
}

// APP-02：应用内切换后端只写数据目录下的 .env。它必须排在其他配置文件之前，
// 否则随应用分发的 .env 里的 DB_BACKEND 会压过用户的选择，重启后仍连回原来的库。
func TestAPP02DataDirEnvOverridesBundledEnvOnLoad(t *testing.T) {
	dataDir := t.TempDir()
	paths := envConfigPaths(dataDir)
	if len(paths) == 0 || paths[0] != filepath.Join(dataDir, BackendConfigFileName) {
		t.Fatalf("数据目录下的 .env 应排第一：%v", paths)
	}
	if got := envConfigPaths(""); len(got) == 0 || got[0] != ".env" {
		t.Fatalf("没有数据目录时仍应加载其他配置：%v", got)
	}

	// 注册还原后再清掉，让加载真正生效，测试结束时恢复原值（M-8：PREVIOUS_BACKEND 同样会被加载进来）。
	unsetEnvForTest(t, "DB_BACKEND", "PREVIOUS_BACKEND")
	if err := os.WriteFile(filepath.Join(dataDir, BackendConfigFileName), []byte("DB_BACKEND=sqlite\nPREVIOUS_BACKEND=postgres\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// 直接走生产的加载入口：数据目录下的文件确实被加载了。
	loadEnvConfig(dataDir)
	if got := os.Getenv("DB_BACKEND"); got != "sqlite" || os.Getenv("PREVIOUS_BACKEND") != "postgres" {
		t.Fatalf("loadEnvConfig 应加载数据目录下的 .env: DB_BACKEND=%q PREVIOUS_BACKEND=%q", got, os.Getenv("PREVIOUS_BACKEND"))
	}
	// 随应用分发的 .env 在它之后加载，压不过它。
	bundled := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(bundled, []byte("DB_BACKEND=postgres\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := godotenv.Load(bundled); err != nil {
		t.Fatal(err)
	}
	if got := ActiveBackend(); got != BackendSQLite {
		t.Fatalf("数据目录下的选择应生效，得到 %s", got)
	}
}

// unsetEnvForTest 先用 t.Setenv 登记还原，再真正删掉这些变量：godotenv 只填补不存在的变量。
func unsetEnvForTest(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

// resetBackendStartupForTest 让 resolveStartupBackend 回到「本进程还没启动过」的状态，结束时还原。
func resetBackendStartupForTest(t *testing.T) {
	t.Helper()
	backendStartup.mu.Lock()
	saved := struct {
		resolved       bool
		backend        Backend
		fromProcessEnv bool
	}{backendStartup.resolved, backendStartup.backend, backendStartup.fromProcessEnv}
	backendStartup.resolved, backendStartup.backend, backendStartup.fromProcessEnv = false, "", false
	backendStartup.mu.Unlock()
	t.Cleanup(func() {
		backendStartup.mu.Lock()
		backendStartup.resolved, backendStartup.backend, backendStartup.fromProcessEnv = saved.resolved, saved.backend, saved.fromProcessEnv
		backendStartup.mu.Unlock()
	})
}

// APP02 / M-1：09-01 以来旧版本写的数据目录 .env 只有 DB_BACKEND（那次切换当时从未生效），
// 启动时不采用；带 PREVIOUS_BACKEND 的新格式才加载。
func TestAPP02LoadEnvConfigSkipsLegacyDataDirConfigWithoutPreviousBackend(t *testing.T) {
	dataDir := t.TempDir()
	unsetEnvForTest(t, "DB_BACKEND", "PREVIOUS_BACKEND")
	path := filepath.Join(dataDir, BackendConfigFileName)
	if err := os.WriteFile(path, []byte("DB_BACKEND=postgres\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loadEnvConfig(dataDir)
	if got, ok := os.LookupEnv("DB_BACKEND"); ok {
		t.Fatalf("旧格式（缺 PREVIOUS_BACKEND）不应被采用，DB_BACKEND=%q", got)
	}

	if err := os.WriteFile(path, []byte("DB_BACKEND=postgres\nPREVIOUS_BACKEND=sqlite\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loadEnvConfig(dataDir)
	if got := os.Getenv("DB_BACKEND"); got != "postgres" {
		t.Fatalf("新格式应被采用，DB_BACKEND=%q", got)
	}
}

// APP02 / M-2：启动时在加载任何配置文件之前判定 DB_BACKEND 是否来自进程环境。
func TestAPP02StartupRecordsWhetherBackendCameFromProcessEnv(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, BackendConfigFileName), []byte("DB_BACKEND=postgres\nPREVIOUS_BACKEND=sqlite\n"), 0600); err != nil {
		t.Fatal(err)
	}

	resetBackendStartupForTest(t)
	unsetEnvForTest(t, "PREVIOUS_BACKEND", "PG_HOST")
	t.Setenv("DB_BACKEND", "sqlite")
	backend, err := resolveStartupBackend(dataDir)
	if err != nil || backend != BackendSQLite || !BackendFromProcessEnv() {
		t.Fatalf("进程环境的 DB_BACKEND 优先且应被记下: backend=%s fromEnv=%v err=%v", backend, BackendFromProcessEnv(), err)
	}

	resetBackendStartupForTest(t)
	unsetEnvForTest(t, "DB_BACKEND", "PREVIOUS_BACKEND", "PG_HOST")
	backend, err = resolveStartupBackend(dataDir)
	if err != nil || backend != BackendPostgres || BackendFromProcessEnv() {
		t.Fatalf("DB_BACKEND 来自数据目录文件时不算进程环境: backend=%s fromEnv=%v err=%v", backend, BackendFromProcessEnv(), err)
	}
}

// APP02 / m5：DB_BACKEND 在进程环境里存在即算来自进程环境，空值也算——godotenv 同样「存在即不覆盖」，
// 数据目录下的配置写不进来，应用内切换在重启后同样不会生效。
func TestAPP02StartupTreatsEmptyProcessBackendAsProcessEnv(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, BackendConfigFileName), []byte("DB_BACKEND=postgres\nPREVIOUS_BACKEND=sqlite\n"), 0600); err != nil {
		t.Fatal(err)
	}
	resetBackendStartupForTest(t)
	unsetEnvForTest(t, "PREVIOUS_BACKEND", "PG_HOST")
	t.Setenv("DB_BACKEND", "")
	backend, err := resolveStartupBackend(dataDir)
	if err != nil || backend != BackendSQLite || !BackendFromProcessEnv() {
		t.Fatalf("空值的 DB_BACKEND 也算来自进程环境: backend=%s fromEnv=%v err=%v", backend, BackendFromProcessEnv(), err)
	}
	if got, ok := os.LookupEnv("DB_BACKEND"); !ok || got != "" {
		t.Fatalf("数据目录下的配置不应覆盖进程环境里的空值: %q ok=%v", got, ok)
	}
}

// APP02 / I-1：PG 恢复备份后的重连（再次调用 Init）沿用启动时解析出的后端，不把本进程启动之后
// 才写入的数据目录 .env（切换后端、切回之前的后端）读进来而中途改连另一个后端。
func TestAPP02ReconnectKeepsStartupBackendIgnoringConfigWrittenAfterStartup(t *testing.T) {
	dataDir := t.TempDir()
	resetBackendStartupForTest(t)
	unsetEnvForTest(t, "DB_BACKEND", "PREVIOUS_BACKEND")
	t.Setenv("PG_HOST", "127.0.0.1")
	backend, err := resolveStartupBackend(dataDir)
	if err != nil || backend != BackendPostgres {
		t.Fatalf("启动时应按 PG_HOST 解析为 postgres: %s err=%v", backend, err)
	}
	// 启动之后切换写下的配置。
	if err := os.WriteFile(filepath.Join(dataDir, BackendConfigFileName), []byte("DB_BACKEND=sqlite\nPREVIOUS_BACKEND=postgres\n"), 0600); err != nil {
		t.Fatal(err)
	}
	backend, err = resolveStartupBackend(dataDir)
	if err != nil || backend != BackendPostgres {
		t.Fatalf("重连应沿用启动时的后端: %s err=%v", backend, err)
	}
	if got, ok := os.LookupEnv("DB_BACKEND"); ok {
		t.Fatalf("重连不应加载启动之后写入的配置，DB_BACKEND=%q", got)
	}
	if got := ActiveBackend(); got != BackendPostgres {
		t.Fatalf("当前后端不应中途改变: %s", got)
	}
}
