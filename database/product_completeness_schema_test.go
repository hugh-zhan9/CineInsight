// 产品完善度批次（2026-09-29，P-001）的 schema 与迁移用例。
//
// 外部测试包 + dbtest 的真实 ApplySchema 入口：设置 CINEINSIGHT_TEST_PG_DSN 后同一批用例
// 在 Postgres 上照样跑。每个迁移都有「老库升级」与「新库」两类用例，且重复执行结果一致。
package database_test

import (
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

// applySchemaTwice 跑两遍 ApplySchema：第二遍代表「下一次启动」，结果必须与第一遍一致。
func applySchemaTwice(t *testing.T, db *gorm.DB) {
	t.Helper()
	for round := 1; round <= 2; round++ {
		if err := database.ApplySchema(db); err != nil {
			t.Fatalf("第 %d 次 ApplySchema 失败(%s): %v", round, dbtest.Backend(), err)
		}
	}
}

func loadSettings(t *testing.T, db *gorm.DB) models.Settings {
	t.Helper()
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	return settings
}

// dropSettingsColumns 把 settings 退回「没有这些列」的老库形状，再回收连接
// （Postgres 上同一连接先 ALTER 再查表会撞 cached plan，那是用例自造的状况）。
func dropSettingsColumns(t *testing.T, db *gorm.DB, columns ...string) {
	t.Helper()
	for _, column := range columns {
		if err := db.Migrator().DropColumn(&models.Settings{}, column); err != nil {
			t.Fatalf("模拟老库删除列 %s 失败(%s): %v", column, dbtest.Backend(), err)
		}
	}
	recycleConnections(t, db)
}

// ---------------------------------------------------------------- 新表与新列

func TestProductCompletenessSchemaCreatesNewTablesAndColumns(t *testing.T) {
	db := dbtest.OpenRaw(t)
	applySchemaTwice(t, db)
	migrator := db.Migrator()

	for _, model := range []interface{}{
		&models.MigrationStagedSource{}, &models.SubtitleJob{}, &models.TagPersonConversion{},
		&models.MovieVideoLink{}, &models.JellyfinSession{}, &models.BrowserDownloadTask{},
		&models.CleanupVideoDismissal{},
	} {
		if !migrator.HasTable(model) {
			t.Fatalf("缺少新表 %T", model)
		}
	}
	for _, check := range []struct {
		model  interface{}
		column string
	}{
		{&models.VideoTrashEntry{}, "mode"}, {&models.VideoTrashEntry{}, "delete_batch_id"},
		{&models.ImageTrashEntry{}, "mode"}, {&models.ImageTrashEntry{}, "delete_batch_id"},
		{&models.Video{}, "stale_reason"}, {&models.Video{}, "favorited_at"}, {&models.Image{}, "favorited_at"},
		{&models.Settings{}, "short_feed_enabled"}, {&models.Settings{}, "short_feed_pin_hash"},
		{&models.Settings{}, "cleanup_short_seconds"}, {&models.Settings{}, "cleanup_low_width"},
		{&models.Settings{}, "cleanup_low_height"}, {&models.Settings{}, "favorites_unified_at"},
		{&models.TranslationGlossaryEntry{}, "target_language"},
		{&models.NearDuplicateDismissal{}, "fingerprint_a"}, {&models.NearDuplicateDismissal{}, "fingerprint_b"},
		{&models.ImageNearDuplicateDismissal{}, "fingerprint_a"}, {&models.ImageNearDuplicateDismissal{}, "fingerprint_b"},
		{&models.CollectionSuggestion{}, "dismiss_key"}, {&models.CollectionSuggestion{}, "target_collection_id"},
		{&models.SubtitleIndexState{}, "has_sidecar"},
		{&models.FaceCluster{}, "ignored_at"}, {&models.SavedLibraryView{}, "person_ids_json"},
	} {
		if !migrator.HasColumn(check.model, check.column) {
			t.Fatalf("缺少列 %T.%s", check.model, check.column)
		}
	}
	for _, check := range []struct {
		model interface{}
		index string
	}{
		{&models.VideoTrashEntry{}, "idx_video_trash_entries_delete_batch_id"},
		{&models.ImageTrashEntry{}, "idx_image_trash_entries_delete_batch_id"},
		{&models.CollectionSuggestion{}, "idx_collection_suggestions_dismiss_key"},
		{&models.SubtitleJob{}, "idx_subtitle_jobs_status_id"},
		{&models.MigrationStagedSource{}, "idx_migration_staged_sources_staged_path"},
		{&models.MovieVideoLink{}, "idx_movie_video_links_pair"},
		{&models.JellyfinSession{}, "idx_jellyfin_sessions_token_hash"},
		{&models.BrowserDownloadTask{}, "idx_browser_download_tasks_task_uid"},
		{&models.CleanupVideoDismissal{}, "idx_cleanup_video_dismissals_video_category"},
	} {
		if !migrator.HasIndex(check.model, check.index) {
			t.Fatalf("缺少索引 %s(%s)", check.index, dbtest.Backend())
		}
	}
}

// 新表约束：唯一键真的生效，且新列的默认值符合契约。
func TestProductCompletenessNewTableConstraints(t *testing.T) {
	db := dbtest.OpenRaw(t)
	applySchemaTwice(t, db)

	video := models.Video{Name: "a.mp4", Path: "/lib/a.mp4", Directory: "/lib", Size: 1}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if video.StaleReason != "" || video.FavoritedAt != nil {
		t.Fatalf("新视频的失效原因应为空、收藏时间应为 NULL: %+v", video)
	}

	// cleanup_video_dismissals：(video_id, category) 唯一。
	if err := db.Create(&models.CleanupVideoDismissal{VideoID: video.ID, Category: models.CleanupDismissalCategoryShort, Fingerprint: "1:2"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.CleanupVideoDismissal{VideoID: video.ID, Category: models.CleanupDismissalCategoryLow}).Error; err != nil {
		t.Fatalf("同一视频不同类别应可共存: %v", err)
	}
	if err := db.Create(&models.CleanupVideoDismissal{VideoID: video.ID, Category: models.CleanupDismissalCategoryShort}).Error; err == nil {
		t.Fatal("同一视频同一类别应被唯一索引拒绝")
	}
	// 视频永久删除时级联清掉忽略记录（外键在两个后端都强制）。
	if err := db.Unscoped().Delete(&models.Video{}, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	var left int64
	if err := db.Model(&models.CleanupVideoDismissal{}).Count(&left).Error; err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("视频硬删后忽略记录应级联删除，剩 %d", left)
	}

	// migration_staged_sources：staged_path 唯一；video_id 可空且不建外键。
	staged := models.MigrationStagedSource{OriginalPath: "/lib/a.mp4", StagedPath: "/lib/.a.mp4.cineinsight-migrating-0123", Size: 10}
	if err := db.Create(&staged).Error; err != nil {
		t.Fatal(err)
	}
	if staged.State != models.MigrationStagedStatePending {
		t.Fatalf("暂存源默认状态应为 pending: %+v", staged)
	}
	if err := db.Create(&models.MigrationStagedSource{StagedPath: staged.StagedPath}).Error; err == nil {
		t.Fatal("同一暂存路径应被唯一索引拒绝")
	}
	gone := uint(987654)
	if err := db.Create(&models.MigrationStagedSource{VideoID: &gone, StagedPath: "/lib/.b"}).Error; err != nil {
		t.Fatalf("视频可能已被永久删除，暂存记录不该有外键: %v", err)
	}

	// jellyfin_sessions / browser_download_tasks：令牌哈希与任务标识唯一。
	hash := strings.Repeat("a", 64)
	now := time.Now()
	if err := db.Create(&models.JellyfinSession{TokenHash: hash, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}).Error; err != nil {
		t.Fatalf("64 位令牌哈希应存得下(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Create(&models.JellyfinSession{TokenHash: hash, LastSeenAt: now, ExpiresAt: now}).Error; err == nil {
		t.Fatal("令牌哈希应唯一")
	}
	uid := strings.Repeat("f", 32)
	if err := db.Create(&models.BrowserDownloadTask{TaskUID: uid, Status: "queued"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.BrowserDownloadTask{TaskUID: uid, Status: "queued"}).Error; err == nil {
		t.Fatal("任务标识应唯一")
	}

	// movie_video_links：(douban_id, video_id) 唯一，外键级联。
	other := models.Video{Name: "b.mp4", Path: "/lib/b.mp4", Directory: "/lib", Size: 1}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.MovieVideoLink{DoubanID: "1292052", VideoID: other.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.MovieVideoLink{DoubanID: "1292052", VideoID: other.ID}).Error; err == nil {
		t.Fatal("同一关联应被唯一索引拒绝")
	}

	// dismiss_key：64 位十六进制存得下，空默认值读回是真正的空串（varchar，不是补空格的 char）。
	suggestion := models.CollectionSuggestion{
		ScanRoot: "/lib", SeriesName: "S", NormalizedSeries: "s",
		Status: models.CollectionSuggestionStatusPending, Fingerprint: strings.Repeat("1", 64),
	}
	if err := db.Create(&suggestion).Error; err != nil {
		t.Fatal(err)
	}
	var reloaded models.CollectionSuggestion
	if err := db.First(&reloaded, suggestion.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.DismissKey != "" || reloaded.TargetCollectionID != nil {
		t.Fatalf("历史候选的 dismiss_key 应为空串、target_collection_id 应为 NULL: %+v", reloaded)
	}
	if err := db.Model(&reloaded).Update("dismiss_key", strings.Repeat("e", 64)).Error; err != nil {
		t.Fatalf("64 位 dismiss_key 应存得下(%s): %v", dbtest.Backend(), err)
	}
}

// ---------------------------------------------------------------- 回收站 mode 回填

func seedLegacyTrashRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	videoRows := []models.VideoTrashEntry{
		{VideoID: 1, VideoName: "moved", OriginalPath: "/lib/moved.mp4", TrashPath: "/lib/trash/moved.mp4", FileMoved: true, State: "deleted"},
		{VideoID: 2, VideoName: "scanned", OriginalPath: "/lib/scanned.mp4", DeletedBy: "scanner", State: "deleted"},
		{VideoID: 3, VideoName: "recordonly", OriginalPath: "/lib/ro.mp4", DeletedBy: "user", State: "deleted"},
		{VideoID: 4, VideoName: "unknown", OriginalPath: "/lib/u.mp4", State: "deleted"},
		// file_moved=true 优先于 deleted_by。
		{VideoID: 5, VideoName: "moved-scanner", OriginalPath: "/lib/p.mp4", TrashPath: "/lib/trash/p.mp4", FileMoved: true, DeletedBy: "scanner", State: "deleted"},
		// 旧版删除到一半的真实形状：pending_move / rollback 时 file_moved 仍是 false，但 trash_path
		// 已写好——文件在（或曾要去）旧版 trash/，必须归 legacy_trash（P-001 评审 I1）。
		{VideoID: 6, VideoName: "pending", OriginalPath: "/lib/pm.mp4", TrashPath: "/lib/trash/pm.mp4", FileMoved: false, DeletedBy: "user", State: "pending_move"},
		{VideoID: 8, VideoName: "rollback", OriginalPath: "/lib/rb.mp4", TrashPath: "/lib/trash/rb.mp4", FileMoved: false, DeletedBy: "user", State: "rollback"},
		// pending_move 但 trash_path 为空：没有旧版 trash/ 目标，仍按只删记录归类。
		{VideoID: 9, VideoName: "pending-no-path", OriginalPath: "/lib/np.mp4", FileMoved: false, DeletedBy: "user", State: "pending_move"},
	}
	imageRows := []models.ImageTrashEntry{
		{ImageID: 1, ImageName: "moved", OriginalPath: "/img/moved.jpg", TrashPath: "/img/trash/moved.jpg", FileMoved: true, State: "deleted"},
		{ImageID: 2, ImageName: "scanned", OriginalPath: "/img/scanned.jpg", DeletedBy: "scanner", State: "deleted"},
		{ImageID: 3, ImageName: "recordonly", OriginalPath: "/img/ro.jpg", DeletedBy: "user", State: "deleted"},
	}
	if err := db.Create(&videoRows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&imageRows).Error; err != nil {
		t.Fatal(err)
	}
}

func trashModes(t *testing.T, db *gorm.DB, table, key string) map[uint]string {
	t.Helper()
	type row struct {
		Key  uint
		Mode string
	}
	var rows []row
	if err := db.Table(table).Select(key + " AS key, mode").Scan(&rows).Error; err != nil {
		t.Fatalf("读取 %s.mode 失败: %v", table, err)
	}
	modes := map[uint]string{}
	for _, item := range rows {
		modes[item.Key] = item.Mode
	}
	return modes
}

func assertBackfilledTrashModes(t *testing.T, db *gorm.DB) {
	t.Helper()
	wantVideo := map[uint]string{
		1: models.TrashModeLegacyTrash, 2: models.TrashModeMissing, 3: models.TrashModeRecordOnly,
		4: models.TrashModeRecordOnly, 5: models.TrashModeLegacyTrash,
		6: models.TrashModeLegacyTrash, 8: models.TrashModeLegacyTrash, 9: models.TrashModeRecordOnly,
	}
	wantImage := map[uint]string{1: models.TrashModeLegacyTrash, 2: models.TrashModeMissing, 3: models.TrashModeRecordOnly}
	for id, want := range wantVideo {
		if got := trashModes(t, db, "video_trash_entries", "video_id")[id]; got != want {
			t.Fatalf("视频条目 %d 的 mode 应为 %s，实际 %q", id, want, got)
		}
	}
	for id, want := range wantImage {
		if got := trashModes(t, db, "image_trash_entries", "image_id")[id]; got != want {
			t.Fatalf("图片条目 %d 的 mode 应为 %s，实际 %q", id, want, got)
		}
	}
}

// 老库升级：表里已有没有 mode 列的历史行，AutoMigrate 加列后它们是空串，必须按
// file_moved / deleted_by 回填；再启动一次结果不变。
func TestMigrateTrashEntryModeBackfillsLegacyRows(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	seedLegacyTrashRows(t, db)
	// 退回没有 mode / delete_batch_id 的老库形状（先删 delete_batch_id 上的索引，否则删不了列）。
	for _, spec := range []struct {
		model interface{}
		index string
	}{
		{&models.VideoTrashEntry{}, "idx_video_trash_entries_delete_batch_id"},
		{&models.ImageTrashEntry{}, "idx_image_trash_entries_delete_batch_id"},
	} {
		if db.Migrator().HasIndex(spec.model, spec.index) {
			if err := db.Migrator().DropIndex(spec.model, spec.index); err != nil {
				t.Fatalf("删除索引 %s 失败: %v", spec.index, err)
			}
		}
		for _, column := range []string{"mode", "delete_batch_id"} {
			if err := db.Migrator().DropColumn(spec.model, column); err != nil {
				t.Fatalf("模拟老库删除列 %s 失败: %v", column, err)
			}
		}
	}
	recycleConnections(t, db)

	applySchemaTwice(t, db)
	assertBackfilledTrashModes(t, db)
}

// 新库：没有条目，迁移空转；之后新写入的条目带着显式 mode，重启不会被回填改写。
func TestMigrateTrashEntryModeFreshDatabaseAndExplicitModeSurvives(t *testing.T) {
	db := dbtest.OpenRaw(t)
	applySchemaTwice(t, db)
	entry := models.VideoTrashEntry{
		VideoID: 7, VideoName: "trashed", OriginalPath: "/lib/t.mp4", TrashPath: "/Users/x/.Trash/t.mp4",
		FileMoved: false, DeletedBy: "scanner", State: "deleted", Mode: models.TrashModeTrash,
		DeleteBatchID: strings.Repeat("ab", 16),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	applySchemaTwice(t, db)
	if got := trashModes(t, db, "video_trash_entries", "video_id")[7]; got != models.TrashModeTrash {
		t.Fatalf("显式写入的 mode 不该被回填改写，实际 %q", got)
	}
}

// ---------------------------------------------------------------- short_feed_enabled

func TestShortFeedEnabledIsOffForFreshDatabase(t *testing.T) {
	db := dbtest.OpenRaw(t)
	applySchemaTwice(t, db)
	settings := loadSettings(t, db)
	if settings.ShortFeedEnabled {
		t.Fatalf("新库的手机端开关应默认关闭(%s)", dbtest.Backend())
	}
	if settings.ShortFeedPINHash != "" {
		t.Fatalf("新库不该有 PIN: %q", settings.ShortFeedPINHash)
	}
}

// 老库升级：列是这一轮才建出来的，手机端必须保持开启。
func TestShortFeedEnabledIsOnWhenUpgradingAnOldDatabase(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	dropSettingsColumns(t, db, "short_feed_enabled", "short_feed_pin_hash")
	applySchemaTwice(t, db)
	if !loadSettings(t, db).ShortFeedEnabled {
		t.Fatalf("老库升级后手机端应保持开启(%s)", dbtest.Backend())
	}
}

// 用户关掉之后重启：迁移不能把它翻回去（判据「列刚建出来」已被消费）。
func TestShortFeedEnabledMigrationDoesNotOverrideUserChoice(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	dropSettingsColumns(t, db, "short_feed_enabled", "short_feed_pin_hash")
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Settings{}).Where("1 = 1").Update("short_feed_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	applySchemaTwice(t, db)
	if loadSettings(t, db).ShortFeedEnabled {
		t.Fatalf("重启不该覆盖用户关掉手机端的选择(%s)", dbtest.Backend())
	}
}

// 进程死在 AutoMigrate 与迁移之间：列已经在、老行是 NULL。下次启动要补成开启，
// 但已经是 false 的行（用户关掉的）不能动。
func TestShortFeedEnabledHealsNullRowsButKeepsExplicitFalse(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Settings{}).Where("1 = 1").UpdateColumn("short_feed_enabled", nil).Error; err != nil {
		t.Fatal(err)
	}
	applySchemaTwice(t, db)
	if !loadSettings(t, db).ShortFeedEnabled {
		t.Fatal("NULL 行应被补成开启")
	}
	if err := db.Model(&models.Settings{}).Where("1 = 1").Update("short_feed_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	applySchemaTwice(t, db)
	if loadSettings(t, db).ShortFeedEnabled {
		t.Fatal("显式 false 不该被翻回")
	}
}

// 双向迁移器逐行 Unscoped().Create：false 必须存得下（这列不能带 gorm default）。
func TestShortFeedEnabledFalseSurvivesRowCopy(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("1 = 1").Delete(&models.Settings{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().Create(&models.Settings{ID: 1, VideoExtensions: ".mp4", ShortFeedEnabled: false, CleanupShortSeconds: 9}).Error; err != nil {
		t.Fatal(err)
	}
	copied := loadSettings(t, db)
	if copied.ShortFeedEnabled || copied.CleanupShortSeconds != 9 {
		t.Fatalf("关掉的开关与自定义阈值应原样复制: %+v", copied)
	}
}

// ---------------------------------------------------------------- 清理阈值

func TestCleanupThresholdsDefaultsForFreshDatabase(t *testing.T) {
	db := dbtest.OpenRaw(t)
	applySchemaTwice(t, db)
	settings := loadSettings(t, db)
	if settings.CleanupShortSeconds != 5 || settings.CleanupLowWidth != 480 || settings.CleanupLowHeight != 320 {
		t.Fatalf("新库的清理阈值应为 5/480/320，实际 %d/%d/%d", settings.CleanupShortSeconds, settings.CleanupLowWidth, settings.CleanupLowHeight)
	}
}

func TestCleanupThresholdsAreFilledWhenUpgradingAnOldDatabase(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	dropSettingsColumns(t, db, "cleanup_short_seconds", "cleanup_low_width", "cleanup_low_height")
	applySchemaTwice(t, db)
	settings := loadSettings(t, db)
	if settings.CleanupShortSeconds != 5 || settings.CleanupLowWidth != 480 || settings.CleanupLowHeight != 320 {
		t.Fatalf("老库升级后清理阈值应为 5/480/320，实际 %d/%d/%d", settings.CleanupShortSeconds, settings.CleanupLowWidth, settings.CleanupLowHeight)
	}
}

// 用户设过的正值重启后不变；<= 0 视为默认并补齐。
func TestCleanupThresholdsKeepUserValuesAndHealNonPositive(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Settings{}).Where("1 = 1").Updates(map[string]interface{}{
		"cleanup_short_seconds": 12, "cleanup_low_width": 0, "cleanup_low_height": -3,
	}).Error; err != nil {
		t.Fatal(err)
	}
	applySchemaTwice(t, db)
	settings := loadSettings(t, db)
	if settings.CleanupShortSeconds != 12 {
		t.Fatalf("用户设的正值不该被覆盖，实际 %d", settings.CleanupShortSeconds)
	}
	if settings.CleanupLowWidth != 480 || settings.CleanupLowHeight != 320 {
		t.Fatalf("<=0 应补成默认，实际 %d/%d", settings.CleanupLowWidth, settings.CleanupLowHeight)
	}
}

// ---------------------------------------------------------------- 收藏与点赞并集

type unifyFixture struct {
	videoInteractionOnly, videoAlreadyFavorite, videoLikedOnly, videoBothTimes, videoNotFavorited models.Video
	imageInteractionOnly, imageLikedOnly                                                          models.Image
	interactionTime, mainTime                                                                     time.Time
}

func seedUnifyFixture(t *testing.T, db *gorm.DB) unifyFixture {
	t.Helper()
	fx := unifyFixture{
		interactionTime: time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC),
		mainTime:        time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC),
	}
	newVideo := func(name string) models.Video {
		video := models.Video{Name: name, Path: "/lib/" + name, Directory: "/lib", Size: 1}
		if err := db.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		return video
	}
	fx.videoInteractionOnly = newVideo("a.mp4")
	fx.videoAlreadyFavorite = newVideo("b.mp4")
	fx.videoLikedOnly = newVideo("c.mp4")
	fx.videoBothTimes = newVideo("d.mp4")
	fx.videoNotFavorited = newVideo("e.mp4")
	// b：主表已收藏、没有互动记录、没有收藏时间——取并集不能取消它。
	if err := db.Model(&models.Video{}).Where("id = ?", fx.videoAlreadyFavorite.ID).Update("is_favorite", true).Error; err != nil {
		t.Fatal(err)
	}
	// d：主表已有收藏时间，互动表另有一个时间——保留主表的。
	if err := db.Model(&models.Video{}).Where("id = ?", fx.videoBothTimes.ID).
		Updates(map[string]interface{}{"is_favorite": true, "favorited_at": fx.mainTime}).Error; err != nil {
		t.Fatal(err)
	}
	interactions := []models.ShortFeedInteraction{
		{VideoID: fx.videoInteractionOnly.ID, Favorited: true, FavoritedAt: &fx.interactionTime},
		{VideoID: fx.videoLikedOnly.ID, Liked: true},
		{VideoID: fx.videoBothTimes.ID, Favorited: true, FavoritedAt: &fx.interactionTime},
		// e：互动表里 favorited=false、liked=false，什么都不该发生。
		{VideoID: fx.videoNotFavorited.ID, ViewCount: 3},
	}
	if err := db.Create(&interactions).Error; err != nil {
		t.Fatal(err)
	}
	newImage := func(name string) models.Image {
		image := models.Image{Name: name, Path: "/img/" + name, Directory: "/img", Size: 1}
		if err := db.Create(&image).Error; err != nil {
			t.Fatal(err)
		}
		return image
	}
	fx.imageInteractionOnly = newImage("a.jpg")
	fx.imageLikedOnly = newImage("b.jpg")
	imageInteractions := []models.ShortFeedImageInteraction{
		{ImageID: fx.imageInteractionOnly.ID, Favorited: true, FavoritedAt: &fx.interactionTime},
		{ImageID: fx.imageLikedOnly.ID, Liked: true},
	}
	if err := db.Create(&imageInteractions).Error; err != nil {
		t.Fatal(err)
	}
	return fx
}

func reloadVideo(t *testing.T, db *gorm.DB, id uint) models.Video {
	t.Helper()
	var video models.Video
	if err := db.First(&video, id).Error; err != nil {
		t.Fatal(err)
	}
	return video
}

func reloadImage(t *testing.T, db *gorm.DB, id uint) models.Image {
	t.Helper()
	var image models.Image
	if err := db.First(&image, id).Error; err != nil {
		t.Fatal(err)
	}
	return image
}

func assertUnified(t *testing.T, db *gorm.DB, fx unifyFixture) {
	t.Helper()
	a := reloadVideo(t, db, fx.videoInteractionOnly.ID)
	if !a.IsFavorite || a.FavoritedAt == nil || !a.FavoritedAt.Equal(fx.interactionTime) {
		t.Fatalf("仅在互动表里收藏的视频应并入主表并取互动表时间: %+v", a)
	}
	b := reloadVideo(t, db, fx.videoAlreadyFavorite.ID)
	if !b.IsFavorite {
		t.Fatal("主表已收藏的视频不能被并集取消")
	}
	c := reloadVideo(t, db, fx.videoLikedOnly.ID)
	if !c.IsLiked || c.IsFavorite {
		t.Fatalf("互动表里点赞的视频应写入 is_liked，且不影响收藏: %+v", c)
	}
	d := reloadVideo(t, db, fx.videoBothTimes.ID)
	if !d.IsFavorite || d.FavoritedAt == nil || !d.FavoritedAt.Equal(fx.mainTime) {
		t.Fatalf("主表已有的收藏时间应保留: %+v", d)
	}
	e := reloadVideo(t, db, fx.videoNotFavorited.ID)
	if e.IsFavorite || e.IsLiked || e.FavoritedAt != nil {
		t.Fatalf("互动表里既没收藏也没点赞的视频不该被改动: %+v", e)
	}
	imageA := reloadImage(t, db, fx.imageInteractionOnly.ID)
	if !imageA.IsFavorite || imageA.FavoritedAt == nil || !imageA.FavoritedAt.Equal(fx.interactionTime) {
		t.Fatalf("图片侧收藏应并入主表并取互动表时间: %+v", imageA)
	}
	imageB := reloadImage(t, db, fx.imageLikedOnly.ID)
	if !imageB.IsLiked || imageB.IsFavorite {
		t.Fatalf("图片侧点赞应写入 is_liked: %+v", imageB)
	}
}

// 老库升级：favorites_unified_at 是 NULL，执行一次并盖章；用户之后取消的收藏不会被再次合并回来。
func TestUnifyFavoritesMergesUnionOnceForOldDatabase(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	fx := seedUnifyFixture(t, db)
	// 老库形状：没有 favorites_unified_at 这一列。
	dropSettingsColumns(t, db, "favorites_unified_at")
	if loadSettings(t, db).FavoritesUnifiedAt != nil {
		t.Fatal("模拟老库失败：升级前不该有盖章")
	}
	// 上面的 loadSettings 在 PG 连接上缓存了少一列的 SELECT * 计划；真实升级是新进程、
	// 不会带着旧计划进 ApplySchema，这里回收连接以模拟同样的前提。
	recycleConnections(t, db)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("老库升级失败(%s): %v", dbtest.Backend(), err)
	}
	assertUnified(t, db, fx)
	if loadSettings(t, db).FavoritesUnifiedAt == nil {
		t.Fatal("合并完成后必须盖章")
	}

	// 用户在桌面取消了 a 的收藏、取消了 c 的点赞；再启动几次，互动表里的旧数据不能再翻回来。
	if err := db.Model(&models.Video{}).Where("id = ?", fx.videoInteractionOnly.ID).
		Updates(map[string]interface{}{"is_favorite": false, "favorited_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Video{}).Where("id = ?", fx.videoLikedOnly.ID).Update("is_liked", false).Error; err != nil {
		t.Fatal(err)
	}
	applySchemaTwice(t, db)
	if reloadVideo(t, db, fx.videoInteractionOnly.ID).IsFavorite || reloadVideo(t, db, fx.videoLikedOnly.ID).IsLiked {
		t.Fatal("并集迁移只能执行一次：盖章之后不得再把互动表的旧数据合并回来")
	}
}

// 新库：没有旧互动数据要合并，直接盖章；之后互动表里出现的收藏不会被误合并。
func TestUnifyFavoritesFreshDatabaseIsStampedAndNeverMerges(t *testing.T) {
	db := dbtest.OpenRaw(t)
	applySchemaTwice(t, db)
	if loadSettings(t, db).FavoritesUnifiedAt == nil {
		t.Fatal("新库应直接盖章")
	}
	fx := seedUnifyFixture(t, db)
	applySchemaTwice(t, db)
	if reloadVideo(t, db, fx.videoInteractionOnly.ID).IsFavorite || reloadVideo(t, db, fx.videoLikedOnly.ID).IsLiked {
		t.Fatal("新库已盖章，不该再合并互动表")
	}
}

// ---------------------------------------------------------------- 片单唯一索引

// 老库升级：上一代 (title, kind) 唯一索引必须被显式删掉，新索引 (title, kind, source_item_id)
// 允许同名同类型但来源不同的条目共存，同来源仍被拒绝；撞名识别靠的 idx_watchlist_title 前缀不变。
func TestWatchlistTitleIndexReplacedForOldLibrary(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	// 退回上一代形状：只有 (title, kind) 唯一索引。
	if err := db.Migrator().DropIndex(&models.WatchlistEntry{}, "idx_watchlist_title_kind_source"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX idx_watchlist_title_kind ON watchlist_entries (title, kind)").Error; err != nil {
		t.Fatal(err)
	}
	seed := []models.WatchlistEntry{
		{Title: "沙丘", Kind: models.WatchlistKindMovie, EnrichmentStatus: models.WatchlistEnrichmentManual},
		{Title: "沙丘", Kind: models.WatchlistKindTV, EnrichmentStatus: models.WatchlistEnrichmentManual},
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatal(err)
	}
	applySchemaTwice(t, db)

	assertWatchlistSchemaUpgraded(t, db)
	if got := listWatchlist(t, db); len(got) != 2 || got[0].ID != seed[0].ID || got[1].ID != seed[1].ID {
		t.Fatalf("升级不应改动片单行: %+v", got)
	}

	movieA := models.WatchlistEntry{Title: "沙丘", Kind: models.WatchlistKindMovie, SourceName: "douban", SourceItemID: "1", EnrichmentStatus: models.WatchlistEnrichmentPending}
	if err := db.Create(&movieA).Error; err != nil {
		t.Fatalf("同名同类型但来源不同应可共存(%s): %v", dbtest.Backend(), err)
	}
	dup := models.WatchlistEntry{Title: "沙丘", Kind: models.WatchlistKindMovie, SourceName: "douban", SourceItemID: "1", EnrichmentStatus: models.WatchlistEnrichmentPending}
	err := db.Create(&dup).Error
	if err == nil {
		t.Fatal("同名同类型同来源应被唯一索引拒绝")
	}
	// services.watchlistTitleConflict 的两条判据：Postgres 认索引名（前缀 idx_watchlist_title_kind），
	// SQLite 认列清单前缀。新索引下两条都必须仍然命中。
	message := err.Error()
	if !strings.Contains(message, "idx_watchlist_title_kind") &&
		!strings.Contains(message, "UNIQUE constraint failed: watchlist_entries.title, watchlist_entries.kind") {
		t.Fatalf("撞名报错不再含 idx_watchlist_title 前缀，watchlistTitleConflict 会失配: %v", err)
	}
	// 手动条目（source_item_id 恒为空串）之间仍按 (title, kind) 撞名。
	manual := models.WatchlistEntry{Title: "沙丘", Kind: models.WatchlistKindMovie, EnrichmentStatus: models.WatchlistEnrichmentManual}
	if err := db.Create(&manual).Error; err == nil {
		t.Fatal("手动条目之间同名同类型应仍被拒绝")
	}
}

// 新库：只有新索引，旧名从未出现。
func TestWatchlistTitleIndexFreshDatabaseHasOnlyNewIndex(t *testing.T) {
	db := dbtest.OpenRaw(t)
	applySchemaTwice(t, db)
	migrator := db.Migrator()
	if !migrator.HasIndex(&models.WatchlistEntry{}, "idx_watchlist_title_kind_source") {
		t.Fatal("缺少 idx_watchlist_title_kind_source")
	}
	for _, legacy := range []string{"idx_watchlist_title_kind", "idx_watchlist_title"} {
		if migrator.HasIndex(&models.WatchlistEntry{}, legacy) {
			t.Fatalf("新库不该有旧索引 %s", legacy)
		}
	}
	assertIndexColumnOrder(t, db, "idx_watchlist_title_kind_source", "title", "kind", "source_item_id")
}

// ---------------------------------------------------------------- 术语表唯一键

func TestGlossaryUniqueKeyIncludesTargetLanguageForOldLibrary(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	// 退回上一代：没有 target_language，唯一键 (scope_key, source_term_lower)。
	if err := db.Migrator().DropIndex(&models.TranslationGlossaryEntry{}, "idx_translation_glossary_entries_scope_lang_term"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&models.TranslationGlossaryEntry{}, "target_language"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX idx_translation_glossary_entries_scope_term ON translation_glossary_entries (scope_key, source_term_lower)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO translation_glossary_entries (scope_key, source_term, source_term_lower, target_term, note, created_at, updated_at) VALUES (0, 'Neo', 'neo', '尼奥', '', ?, ?)",
		time.Now(), time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	recycleConnections(t, db)

	applySchemaTwice(t, db)

	migrator := db.Migrator()
	if migrator.HasIndex(&models.TranslationGlossaryEntry{}, "idx_translation_glossary_entries_scope_term") {
		t.Fatal("旧的术语唯一索引应已删除")
	}
	if !migrator.HasIndex(&models.TranslationGlossaryEntry{}, "idx_translation_glossary_entries_scope_lang_term") {
		t.Fatal("缺少新的术语唯一索引")
	}
	var legacy models.TranslationGlossaryEntry
	if err := db.Where("source_term_lower = ?", "neo").First(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if legacy.TargetLanguage != "" {
		t.Fatalf("历史术语的目标语言应为空（所有语言）: %+v", legacy)
	}
	assertIndexColumnOrder(t, db, "idx_translation_glossary_entries_scope_lang_term", "scope_key", "target_language", "source_term_lower")

	japanese := models.TranslationGlossaryEntry{ScopeKey: 0, TargetLanguage: "ja", SourceTerm: "Neo", SourceTermLower: "neo", TargetTerm: "ネオ"}
	if err := db.Create(&japanese).Error; err != nil {
		t.Fatalf("同源词不同目标语言应可共存(%s): %v", dbtest.Backend(), err)
	}
	dup := models.TranslationGlossaryEntry{ScopeKey: 0, TargetLanguage: "ja", SourceTerm: "NEO", SourceTermLower: "neo", TargetTerm: "x"}
	if err := db.Create(&dup).Error; err == nil {
		t.Fatal("同范围同语言同源词应被唯一索引拒绝")
	}
	allLanguages := models.TranslationGlossaryEntry{ScopeKey: 0, TargetLanguage: "", SourceTerm: "Neo", SourceTermLower: "neo", TargetTerm: "x"}
	if err := db.Create(&allLanguages).Error; err == nil {
		t.Fatal("同范围同源词的「所有语言」条目应与历史条目冲突")
	}
}

func TestGlossaryUniqueKeyFreshDatabase(t *testing.T) {
	db := dbtest.OpenRaw(t)
	applySchemaTwice(t, db)
	if db.Migrator().HasIndex(&models.TranslationGlossaryEntry{}, "idx_translation_glossary_entries_scope_term") {
		t.Fatal("新库不该有旧的术语唯一索引")
	}
	assertIndexColumnOrder(t, db, "idx_translation_glossary_entries_scope_lang_term", "scope_key", "target_language", "source_term_lower")
}

// ---------------------------------------------------------------- 整体幂等

func TestApplySchemaIsIdempotentForProductCompletenessSettings(t *testing.T) {
	db := dbtest.OpenRaw(t)
	applySchemaTwice(t, db)
	before := loadSettings(t, db)
	applySchemaTwice(t, db)
	after := loadSettings(t, db)
	var rows int64
	if err := db.Model(&models.Settings{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("重复 ApplySchema 不该多出设置行，实际 %d", rows)
	}
	if before.ShortFeedEnabled != after.ShortFeedEnabled ||
		before.CleanupShortSeconds != after.CleanupShortSeconds ||
		before.CleanupLowWidth != after.CleanupLowWidth ||
		before.CleanupLowHeight != after.CleanupLowHeight {
		t.Fatalf("重复 ApplySchema 结果应一致: before=%+v after=%+v", before, after)
	}
}
