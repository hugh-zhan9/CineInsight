package services

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// 数据体检（D-DH-04 / D-DH-05）：每个检查项一个造出问题的夹具；修复只动悬挂行；play_count 不在检查中。

func setupDataHealthTest(t *testing.T) (*DataHealthService, *gorm.DB, string) {
	t.Helper()
	if dbtest.IsPostgres() {
		t.Skip("体检夹具用真 SQLite 库（含外键与完整性检查）；Postgres 由集成方全量跑")
	}
	dataDir := t.TempDir()
	db := openLiveSQLiteLibrary(t, dataDir)
	if err := db.Create(&models.Video{Name: "kept.mp4", Path: filepath.Join(dataDir, "kept.mp4"), Directory: dataDir}).Error; err != nil {
		t.Fatal(err)
	}
	backups := NewBackupService(dataDir)
	service := NewDataHealthService(dataDir)
	service.SetBackupService(backups)
	service.SetBackgroundTaskRegistry(NewBackgroundTaskRegistry())
	return service, db, dataDir
}

func healthCheckByKey(t *testing.T, report *DataHealthReportView, key string) DataHealthCheckResult {
	t.Helper()
	for _, check := range report.Checks {
		if check.Check == key {
			return check
		}
	}
	t.Fatalf("报告里没有 %s", key)
	return DataHealthCheckResult{}
}

func healthGroupByKey(result DataHealthCheckResult, key string) (DataHealthGroup, bool) {
	for _, group := range result.Groups {
		if group.Key == key {
			return group, true
		}
	}
	return DataHealthGroup{}, false
}

func mustExec(t *testing.T, db *gorm.DB, sql string, args ...any) {
	t.Helper()
	if err := db.Exec(sql, args...).Error; err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// 干净的库：各项都通过；报告落库、只留最近 10 份；没接孤儿文件汇总时不列这一项。
func TestDataHealthCleanLibraryPassesAndKeepsTenReports(t *testing.T) {
	service, db, _ := setupDataHealthTest(t)
	if _, err := service.backups.CreateBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	listed, _ := service.backups.ListBackups()
	if _, err := service.backups.VerifyDatabaseBackup(context.Background(), listed[0].Name); err != nil {
		t.Fatal(err)
	}
	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, check := range report.Checks {
		keys = append(keys, check.Check)
		if check.Status != DataHealthStatusOK {
			t.Fatalf("干净的库 %s 应通过: %+v", check.Check, check)
		}
	}
	if strings.Join(keys, ",") != "sqlite_integrity,foreign_keys,dangling_refs,missing_unique_indexes,derived_counts,managed_paths,backups" {
		t.Fatalf("检查项顺序不对: %v", keys)
	}
	for range 10 {
		if _, err := service.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	db.Model(&models.DataHealthReport{}).Count(&count)
	state, err := service.Latest(context.Background())
	if err != nil || count != 10 || state.Report == nil || state.Running || state.Report.ID <= report.ID {
		t.Fatalf("应只留最近 10 份且能读回最新一份: count=%d state=%+v err=%v", count, state, err)
	}
}

// TestDataHealthUniqueIndexListMatchesSchema：清单里的每个唯一索引都由 ApplySchema 建出来——
// 名字与 database/database.go 的语句漂移时这里失败。
func TestDataHealthUniqueIndexListMatchesSchema(t *testing.T) {
	_, db, _ := setupDataHealthTest(t)
	for _, index := range dataHealthUniqueIndexes {
		present, err := healthIndexExists(db, index.name)
		if err != nil || !present {
			t.Fatalf("ApplySchema 后应有唯一索引 %s: err=%v", index.name, err)
		}
	}
}

// D-DH-04 / D-DH-05：悬挂引用——父记录彻底不存在才算（回收站里的父记录仍算存在）；修复只删可清理表里的
// 悬挂行，历史记录只报不删，正常行一条不动；删人脸观测后按实际观测重算簇。
func TestDataHealthDanglingRefsRepairOnlyTouchesDanglingRows(t *testing.T) {
	service, db, dataDir := setupDataHealthTest(t)
	trashed := models.Video{Name: "trashed.mp4", Path: filepath.Join(dataDir, "trashed.mp4"), Directory: dataDir}
	if err := db.Create(&trashed).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&trashed).Error; err != nil { // 软删：仍在回收站
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO near_duplicate_dismissals (video_low_id, video_high_id, created_at) VALUES (1, 999, CURRENT_TIMESTAMP), (1, ?, CURRENT_TIMESTAMP)", trashed.ID)
	mustExec(t, db, "INSERT INTO face_clusters (id, status, observation_count, created_at, updated_at) VALUES (7, 'unnamed', 2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)")
	mustExec(t, db, `INSERT INTO face_observations (id, media_kind, media_id, source_fingerprint, frame_ms, bbox_hash, cluster_id, created_at, updated_at)
		VALUES (1, 'video', 999, 'a', 0, 'x', 7, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), (2, 'video', 1, 'a', 0, 'y', 7, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	mustExec(t, db, "INSERT INTO playback_queue_entries (video_id, title, position, created_at) VALUES (999, 'gone', 1, CURRENT_TIMESTAMP), (1, 'kept', 2, CURRENT_TIMESTAMP)")
	rule := models.AutomationRule{Name: "r", MediaKind: "image", Enabled: true, ConditionsJSON: "[]", ActionsJSON: "[]"}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO automation_rule_writes (rule_id, media_kind, media_id, action, target_id, inserted, created_at) VALUES (?, 'image', 999, 'tag', 1, true, CURRENT_TIMESTAMP)", rule.ID)
	batch := models.MetadataBatchEdit{Kind: "video", SummaryJSON: "{}", ItemCount: 2}
	if err := db.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO metadata_batch_edit_items (batch_id, media_id, field, before_json, after_json) VALUES (?, 999, 'rating', '1', '2'), (?, 1, 'rating', '1', '2')", batch.ID, batch.ID)
	project := models.VideoEditProject{Kind: "k", Title: "t", Status: "done", Mode: "m", RecipeJSON: "{}", AnalysisJSON: "{}", AcknowledgedJSON: "{}"}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO video_edit_items (project_id, seq, status, phase, progress, plan_json, output_name, publish_target, staged_size, output_path, output_video_id, work_dir, error_code, error_message)
		VALUES (?, 1, 'done', 'done', 1, '{}', 'o', '', 0, '', 999, '', '', '')`, project.ID)
	mustExec(t, db, "INSERT INTO migration_staged_sources (video_id, staged_path, created_at, updated_at) VALUES (999, 'staged', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)")

	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check := healthCheckByKey(t, report, DataHealthCheckDanglingRefs)
	if check.Status != DataHealthStatusProblem || check.Count != 7 || !check.Repairable {
		t.Fatalf("应报 7 行悬挂（5 行可清理 + 2 行历史）: %+v", check)
	}
	if group, ok := healthGroupByKey(check, "near_duplicate_dismissals"); !ok || group.Count != 1 || group.Examples[0] != "id 1" {
		t.Fatalf("父记录在回收站里不算悬挂: %+v", group)
	}
	if group, _ := healthGroupByKey(check, "video_edit_items.output_video_id"); group.Repairable {
		t.Fatal("历史记录只报不修")
	}

	result, err := service.Repair(context.Background(), DataHealthCheckDanglingRefs)
	if err != nil || result.Repaired != 5 || result.Cancelled {
		t.Fatalf("应删除 5 行可清理的悬挂行: %+v err=%v", result, err)
	}
	if result.Result.Count != 2 || result.Result.Repairable {
		t.Fatalf("修完重跑只剩两条历史记录: %+v", result.Result)
	}
	for table, want := range map[string]int64{"near_duplicate_dismissals": 1, "face_observations": 1, "playback_queue_entries": 1,
		"automation_rule_writes": 0, "metadata_batch_edit_items": 1, "video_edit_items": 1, "migration_staged_sources": 1} {
		var count int64
		db.Table(table).Count(&count)
		if count != want {
			t.Fatalf("%s 应剩 %d 行，实际 %d", table, want, count)
		}
	}
	var cluster models.FaceCluster
	if err := db.First(&cluster, 7).Error; err != nil || cluster.ObservationCount != 1 {
		t.Fatalf("删观测后应按实际观测重算簇: %+v err=%v", cluster, err)
	}
	state, _ := service.Latest(context.Background())
	if latest := healthCheckByKey(t, state.Report, DataHealthCheckDanglingRefs); latest.Count != 2 {
		t.Fatalf("最近一份报告里的这一项应换成重跑结果: %+v", latest)
	}
}

// D-DH-04 / D-DH-05：派生计数——四种计数漂移都能查出并按明细重算；截断的帧哈希不算漂移；
// play_count 不由明细派生，不在检查中。
func TestDataHealthDerivedCountsDetectAndRecompute(t *testing.T) {
	service, db, _ := setupDataHealthTest(t)
	mustExec(t, db, "UPDATE videos SET play_count = 7 WHERE id = 1") // 没有任何播放事件
	mustExec(t, db, "INSERT INTO subtitle_index_states (video_id, subtitle_path, segment_count, created_at, updated_at, last_checked_at) VALUES (1, 'a.srt', 5, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)")
	mustExec(t, db, "INSERT INTO subtitle_segments (video_id, segment_index, text) VALUES (1, 1, 'a'), (1, 2, 'b')")
	mustExec(t, db, "INSERT INTO scene_index_states (video_id, source_size, source_mtime_ns, interval_ms, segment_count, updated_at) VALUES (1, 1, 1, 1000, 3, CURRENT_TIMESTAMP)")
	mustExec(t, db, "INSERT INTO scene_visual_segments (video_id, start_ms, end_ms) VALUES (1, 0, 1000)")
	mustExec(t, db, "INSERT INTO face_clusters (id, status, observation_count, created_at, updated_at) VALUES (3, 'unnamed', 4, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)")
	mustExec(t, db, `INSERT INTO face_observations (media_kind, media_id, source_fingerprint, frame_ms, bbox_hash, cluster_id, created_at, updated_at)
		VALUES ('video', 1, 'a', 0, 'x', 3, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	second := models.Video{Name: "second.mp4", Path: "/second.mp4", Directory: "/"}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO video_frame_hash_sequences (video_id, interval_ms, hashes, frame_count, source_size, source_mod_time_ns, computed_at, created_at, updated_at) VALUES (1, 500, ?, 3, 1, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)", make([]byte, 16))
	mustExec(t, db, "INSERT INTO video_frame_hash_sequences (video_id, interval_ms, hashes, frame_count, source_size, source_mod_time_ns, computed_at, created_at, updated_at) VALUES (?, 500, ?, 7, 1, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)", second.ID, make([]byte, 12))

	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check := healthCheckByKey(t, report, DataHealthCheckDerivedCounts)
	if check.Status != DataHealthStatusProblem || check.Count != 4 || len(check.Groups) != 4 {
		t.Fatalf("四种计数各一行漂移，截断的帧哈希不算: %+v", check)
	}
	if group, _ := healthGroupByKey(check, "subtitle_index_states.segment_count"); group.Examples[0] != "视频 1：记 5，实际 2" {
		t.Fatalf("示例应给记的数与实际数: %+v", group)
	}
	for _, spec := range derivedCountSpecs {
		for _, excluded := range []string{"play_count", "last_played_at", "view_count"} {
			if strings.Contains(spec.key, excluded) {
				t.Fatalf("%s 不由明细派生，不应检查", excluded)
			}
		}
	}
	result, err := service.Repair(context.Background(), DataHealthCheckDerivedCounts)
	if err != nil || result.Repaired != 4 || result.Result.Status != DataHealthStatusOK {
		t.Fatalf("应重算 4 行并在重跑后通过: %+v err=%v", result, err)
	}
	var playCount, truncatedFrames int
	db.Raw("SELECT play_count FROM videos WHERE id = 1").Scan(&playCount)
	db.Raw("SELECT frame_count FROM video_frame_hash_sequences WHERE video_id = ?", second.ID).Scan(&truncatedFrames)
	if playCount != 7 || truncatedFrames != 7 {
		t.Fatalf("修复不应动 play_count 或截断的帧哈希: %d %d", playCount, truncatedFrames)
	}
}

// D-DH-04 / D-DH-05：托管路径——数据目录内且文件不存在的才算；修复只清空这些列，文件在的、
// 绝对路径与越界路径都不动。
func TestDataHealthManagedPathsClearOnlyMissingFiles(t *testing.T) {
	service, db, dataDir := setupDataHealthTest(t)
	writeDataCareFile(t, filepath.Join(dataDir, "media-details/people/1/a.jpg"), "avatar")
	mustExec(t, db, "UPDATE videos SET poster_path = 'videos/1/gone.jpg', fanart_path = '/abs/elsewhere.jpg' WHERE id = 1")
	mustExec(t, db, "INSERT INTO people (id, display_name, avatar_path, created_at, updated_at) VALUES (1, 'a', 'people/1/a.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), (2, 'b', '../outside.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)")
	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check := healthCheckByKey(t, report, DataHealthCheckManagedPaths)
	if check.Count != 1 || check.Groups[0].Examples[0] != "media-details/videos/1/gone.jpg" {
		t.Fatalf("只有指向不存在文件的数据目录内路径算问题: %+v", check)
	}
	result, err := service.Repair(context.Background(), DataHealthCheckManagedPaths)
	if err != nil || result.Repaired != 1 || result.Result.Status != DataHealthStatusOK {
		t.Fatalf("应清空一条路径: %+v err=%v", result, err)
	}
	var video models.Video
	db.Unscoped().First(&video, 1)
	var avatars []string
	db.Raw("SELECT avatar_path FROM people ORDER BY id").Scan(&avatars)
	if video.PosterPath != "" || video.FanartPath != "/abs/elsewhere.jpg" || strings.Join(avatars, ",") != "people/1/a.jpg,../outside.jpg" {
		t.Fatalf("修复只应清空缺失文件的那一列: %q %q %v", video.PosterPath, video.FanartPath, avatars)
	}
}

// D-DH-04：SQLite 外键悬挂与唯一索引缺失（含它挡不住的重复组）只报不修。
func TestDataHealthForeignKeysAndMissingUniqueIndexesReportOnly(t *testing.T) {
	service, db, _ := setupDataHealthTest(t)
	if err := db.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
			return err
		}
		if err := conn.Exec("INSERT INTO subtitle_segments (video_id, segment_index, text) VALUES (999, 1, 'orphan')").Error; err != nil {
			return err
		}
		return conn.Exec("PRAGMA foreign_keys = ON").Error
	}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "DROP INDEX idx_saved_library_views_name_active")
	mustExec(t, db, "INSERT INTO saved_library_views (name, created_at, updated_at) VALUES ('dup', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), ('dup', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)")
	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fk := healthCheckByKey(t, report, DataHealthCheckForeignKeys)
	if group, ok := healthGroupByKey(fk, "subtitle_segments→videos"); fk.Status != DataHealthStatusProblem || !ok || group.Count != 1 || fk.Repairable {
		t.Fatalf("应报出外键悬挂且不可修: %+v", fk)
	}
	unique := healthCheckByKey(t, report, DataHealthCheckMissingUniqueIndexes)
	group, ok := healthGroupByKey(unique, "idx_saved_library_views_name_active")
	if unique.Status != DataHealthStatusProblem || unique.Count != 1 || !ok || group.Count != 1 || unique.Repairable ||
		!strings.Contains(group.Examples[0], "2 条重复") {
		t.Fatalf("应报出缺失的唯一索引与一组重复，且不可修: %+v", unique)
	}
	if _, err := service.Repair(context.Background(), DataHealthCheckMissingUniqueIndexes); err == nil {
		t.Fatal("只报不修的项不应接受修复")
	}
}

// D-DH-04：数据库文件坏了时 quick_check 报问题（没有修复，建议从备份恢复）。
func TestDataHealthSQLiteIntegrityReportsCorruption(t *testing.T) {
	_, _, dataDir := setupDataHealthTest(t)
	path := filepath.Join(dataDir, "broken.db")
	broken, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, broken, "CREATE TABLE things (id INTEGER PRIMARY KEY, body TEXT)")
	mustExec(t, broken, "CREATE INDEX idx_things_body ON things(body)")
	for index := range 2000 {
		mustExec(t, broken, "INSERT INTO things (body) VALUES (?)", strings.Repeat("x", 50)+string(rune('a'+index%26)))
	}
	if sqlDB, err := broken.DB(); err == nil {
		_ = sqlDB.Close()
	}
	corruptSQLiteTail(t, path)
	reopened, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if sqlDB, err := reopened.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	result, err := checkSQLiteIntegrity(context.Background(), reopened)
	if err != nil || result.Status != DataHealthStatusProblem || result.Count == 0 || len(result.Examples) == 0 || result.Repairable {
		t.Fatalf("坏库应报问题且不可修: %+v err=%v", result, err)
	}
}

// D-DH-04：备份项——没有成功备份记为问题；孤儿文件汇总接上后列出并链接到失联页签。
func TestDataHealthBackupsAndOrphanSummary(t *testing.T) {
	service, _, _ := setupDataHealthTest(t)
	service.SetOrphanFileSummary(func(ctx context.Context) (int, int64, error) { return 3, 4096, nil })
	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	backups := healthCheckByKey(t, report, DataHealthCheckBackups)
	if backups.Status != DataHealthStatusProblem || backups.Link != "settings:backup" {
		t.Fatalf("从没备份过应记为问题并链接到设置: %+v", backups)
	}
	orphans := healthCheckByKey(t, report, DataHealthCheckOrphanFiles)
	if orphans.Status != DataHealthStatusWarning || orphans.Count != 3 || orphans.Bytes != 4096 || orphans.Link != "data-care:lost" {
		t.Fatalf("孤儿文件只汇总并链接到失联页签: %+v", orphans)
	}
	if _, err := service.backups.CreateBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, _ = service.Run(context.Background())
	if backups := healthCheckByKey(t, report, DataHealthCheckBackups); backups.Status != DataHealthStatusWarning {
		t.Fatalf("有备份但从没校验过应是提醒: %+v", backups)
	}
}

// 4.3：单项超时记 timeout 继续下一项；取消时不保存半份报告；同时只允许一个体检或修复；
// 运行期间登记 data_health 任务键。
func TestDataHealthTimeoutCancelAndBusy(t *testing.T) {
	service, db, _ := setupDataHealthTest(t)
	service.checkTimeout = time.Nanosecond
	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range report.Checks {
		if check.Status != DataHealthStatusTimeout {
			t.Fatalf("超时的项应记 timeout 并继续: %+v", check)
		}
	}
	service.checkTimeout = dataHealthCheckTimeout
	registry := NewBackgroundTaskRegistry()
	service.SetBackgroundTaskRegistry(registry)
	var busyErr error
	sawRunning := false
	service.SetProgressEmitter(func(progress DataHealthProgress) {
		if progress.Phase == "check" && busyErr == nil {
			sawRunning = containsString(registry.Snapshot(), string(BackgroundTaskDataHealth))
			_, busyErr = service.Repair(context.Background(), DataHealthCheckDanglingRefs)
			service.Cancel()
		}
	})
	var before int64
	db.Model(&models.DataHealthReport{}).Count(&before)
	if _, err := service.Run(context.Background()); !errors.Is(err, ErrDataHealthCancelled) {
		t.Fatalf("取消应返回 ErrDataHealthCancelled: %v", err)
	}
	var after int64
	db.Model(&models.DataHealthReport{}).Count(&after)
	if !errors.Is(busyErr, ErrDataHealthBusy) || !sawRunning || after != before || len(registry.Snapshot()) != 0 {
		t.Fatalf("busy=%v running=%v reports %d→%d", busyErr, sawRunning, before, after)
	}
	if database.DB == nil {
		t.Fatal("体检不应关库")
	}
}
