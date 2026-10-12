package services

import (
	"context"
	"errors"
	"fmt"
	"os"
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
// 两个后端都跑（dbtest：设了 CINEINSIGHT_TEST_PG_DSN 时是 Postgres 的独立 schema）；只有 SQLite 才有的检查单独跳过。

// healthMissingID 是一个肯定不存在的父记录 id。
const healthMissingID = 999999

// setupDataHealthTest 打开 dbtest 库并跑完 ApplySchema（含唯一索引），挂成 database.DB，建一条视频，返回它的 id。
func setupDataHealthTest(t *testing.T) (*DataHealthService, *gorm.DB, string, uint) {
	t.Helper()
	t.Setenv("DB_BACKEND", dbtest.Backend())
	dataDir := t.TempDir()
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema 失败: %v", err)
	}
	previousDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previousDB })
	kept := models.Video{Name: "kept.mp4", Path: filepath.Join(dataDir, "kept.mp4"), Directory: dataDir}
	if err := db.Create(&kept).Error; err != nil {
		t.Fatal(err)
	}
	backups := NewBackupService(dataDir)
	service := NewDataHealthService(dataDir)
	service.SetBackupService(backups)
	service.SetBackgroundTaskRegistry(NewBackgroundTaskRegistry())
	return service, db, dataDir, kept.ID
}

// skipUnlessSQLiteHealth 跳过只在 SQLite 上有的检查（数据库文件完整性、外键检查）。
func skipUnlessSQLiteHealth(t *testing.T) {
	t.Helper()
	if dbtest.IsPostgres() {
		t.Skip("这一项检查只在 SQLite 上有")
	}
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

// 干净的库：各项都通过；报告落库、只留最近 10 份；没接孤儿文件汇总时不列这一项；SQLite 才有完整性与外键两项。
func TestDataHealthCleanLibraryPassesAndKeepsTenReports(t *testing.T) {
	service, db, _, _ := setupDataHealthTest(t)
	wantKeys := "dangling_refs,missing_unique_indexes,derived_counts,managed_paths,backups"
	if !dbtest.IsPostgres() {
		wantKeys = "sqlite_integrity,foreign_keys," + wantKeys
		if _, err := service.backups.CreateBackup(context.Background()); err != nil {
			t.Fatal(err)
		}
		listed, _ := service.backups.ListBackups()
		if _, err := service.backups.VerifyDatabaseBackup(context.Background(), listed[0].Name); err != nil {
			t.Fatal(err)
		}
	}
	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, check := range report.Checks {
		keys = append(keys, check.Check)
		// Postgres 上不跑 pg_dump，备份项自然是「从没备份过」。
		if check.Status != DataHealthStatusOK && !(dbtest.IsPostgres() && check.Check == DataHealthCheckBackups) {
			t.Fatalf("干净的库 %s 应通过: %+v", check.Check, check)
		}
	}
	if strings.Join(keys, ",") != wantKeys {
		t.Fatalf("检查项不对: %v", keys)
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
	_, db, _, _ := setupDataHealthTest(t)
	for _, index := range dataHealthUniqueIndexes {
		present, err := healthIndexExists(db, index.name)
		if err != nil || !present {
			t.Fatalf("ApplySchema 后应有唯一索引 %s: err=%v", index.name, err)
		}
	}
}

// D-DH-04 / D-DH-05：悬挂引用——父记录彻底不存在才算（回收站里的父记录仍算存在）；覆盖三类忽略表、人脸观测
// （视频、图片、人物头像三种媒体）、人脸关系记账、待播队列、规则账本、批量编辑明细与两类历史记录。修复只删可清理表里的
// 悬挂行，历史记录只报不删，正常行一条不动；删人脸观测后按实际观测重算簇。
func TestDataHealthDanglingRefsRepairOnlyTouchesDanglingRows(t *testing.T) {
	service, db, dataDir, keptID := setupDataHealthTest(t)
	trashed := models.Video{Name: "trashed.mp4", Path: filepath.Join(dataDir, "trashed.mp4"), Directory: dataDir}
	if err := db.Create(&trashed).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&trashed).Error; err != nil { // 软删：仍在回收站
		t.Fatal(err)
	}
	image := models.Image{Name: "a.jpg", Path: filepath.Join(dataDir, "a.jpg"), Directory: dataDir}
	person := models.Person{DisplayName: "在世的人"}
	if err := db.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	missing := healthMissingID
	mustExec(t, db, "INSERT INTO near_duplicate_dismissals (video_low_id, video_high_id, created_at) VALUES (?, ?, CURRENT_TIMESTAMP), (?, ?, CURRENT_TIMESTAMP)", keptID, missing, keptID, trashed.ID)
	mustExec(t, db, "INSERT INTO image_near_duplicate_dismissals (image_low_id, image_high_id, created_at) VALUES (?, ?, CURRENT_TIMESTAMP)", image.ID, missing)
	mustExec(t, db, `INSERT INTO clip_dismissals (video_full_id, video_clip_id, full_source_size, full_source_mod_time_ns, clip_source_size, clip_source_mod_time_ns, created_at, updated_at)
		VALUES (?, ?, 1, 1, 1, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, keptID, missing)
	mustExec(t, db, "INSERT INTO video_version_suggestion_dismissals (video_low_id, video_high_id, created_at) VALUES (?, ?, CURRENT_TIMESTAMP)", keptID, missing)
	mustExec(t, db, "INSERT INTO face_clusters (id, status, observation_count, created_at, updated_at) VALUES (7, 'unnamed', 2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)")
	mustExec(t, db, `INSERT INTO face_observations (id, media_kind, media_id, source_fingerprint, frame_ms, bbox_hash, cluster_id, created_at, updated_at) VALUES
		(1, 'video', ?, 'a', 0, 'x', 7, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(2, 'video', ?, 'a', 0, 'y', 7, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(3, 'person_avatar', ?, 'a', -1, 'z', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(4, 'person_avatar', ?, 'a', -1, 'z', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, missing, keptID, missing, person.ID)
	mustExec(t, db, "INSERT INTO face_relation_writes (person_id, media_kind, media_id, cluster_id, created_at) VALUES (?, 'video', ?, 7, CURRENT_TIMESTAMP), (?, 'image', ?, 7, CURRENT_TIMESTAMP)", person.ID, missing, person.ID, image.ID)
	mustExec(t, db, "INSERT INTO playback_queue_entries (video_id, title, position, created_at) VALUES (?, 'gone', 1, CURRENT_TIMESTAMP), (?, 'kept', 2, CURRENT_TIMESTAMP)", missing, keptID)
	rule := models.AutomationRule{Name: "r", MediaKind: "image", Enabled: true, ConditionsJSON: "[]", ActionsJSON: "[]"}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO automation_rule_writes (rule_id, media_kind, media_id, action, target_id, inserted, created_at) VALUES (?, 'image', ?, 'tag', 1, true, CURRENT_TIMESTAMP)", rule.ID, missing)
	batch := models.MetadataBatchEdit{Kind: "video", SummaryJSON: "{}", ItemCount: 2}
	if err := db.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO metadata_batch_edit_items (batch_id, media_id, field, before_json, after_json) VALUES (?, ?, 'rating', '1', '2'), (?, ?, 'rating', '1', '2')", batch.ID, missing, batch.ID, keptID)
	project := models.VideoEditProject{Kind: "k", Title: "t", Status: "done", Mode: "m", RecipeJSON: "{}", AnalysisJSON: "{}", AcknowledgedJSON: "{}"}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO video_edit_items (project_id, seq, status, phase, progress, plan_json, output_name, publish_target, staged_size, output_path, output_video_id, work_dir, error_code, error_message)
		VALUES (?, 1, 'done', 'done', 1, '{}', 'o', '', 0, '', ?, '', '', '')`, project.ID, missing)
	mustExec(t, db, "INSERT INTO migration_staged_sources (video_id, staged_path, created_at, updated_at) VALUES (?, 'staged', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)", missing)

	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check := healthCheckByKey(t, report, DataHealthCheckDanglingRefs)
	if check.Status != DataHealthStatusProblem || check.Count != 12 || !check.Repairable {
		t.Fatalf("应报 12 行悬挂（10 行可清理 + 2 行历史）: %+v", check)
	}
	for key, want := range map[string]int64{"near_duplicate_dismissals": 1, "image_near_duplicate_dismissals": 1, "clip_dismissals": 1,
		"video_version_suggestion_dismissals": 1, "face_observations": 2, "face_relation_writes": 1, "playback_queue_entries": 1,
		"automation_rule_writes": 1, "metadata_batch_edit_items": 1, "video_edit_items.output_video_id": 1, "migration_staged_sources": 1} {
		if group, ok := healthGroupByKey(check, key); !ok || group.Count != want {
			t.Fatalf("%s 应有 %d 行悬挂: %+v", key, want, group)
		}
	}
	if group, _ := healthGroupByKey(check, "video_edit_items.output_video_id"); group.Repairable {
		t.Fatal("历史记录只报不修")
	}

	result, err := service.Repair(context.Background(), DataHealthCheckDanglingRefs)
	if err != nil || result.Repaired != 10 || result.Cancelled {
		t.Fatalf("应删除 10 行可清理的悬挂行: %+v err=%v", result, err)
	}
	if result.Result.Count != 2 || result.Result.Repairable {
		t.Fatalf("修完重跑只剩两条历史记录: %+v", result.Result)
	}
	for table, want := range map[string]int64{"near_duplicate_dismissals": 1, "image_near_duplicate_dismissals": 0, "clip_dismissals": 0,
		"video_version_suggestion_dismissals": 0, "face_observations": 2, "face_relation_writes": 1, "playback_queue_entries": 1,
		"automation_rule_writes": 0, "metadata_batch_edit_items": 1, "video_edit_items": 1, "migration_staged_sources": 1} {
		var count int64
		db.Table(table).Count(&count)
		if count != want {
			t.Fatalf("%s 应剩 %d 行，实际 %d", table, want, count)
		}
	}
	var survivors []uint
	db.Raw("SELECT id FROM face_observations ORDER BY id").Scan(&survivors)
	var cluster models.FaceCluster
	if err := db.First(&cluster, 7).Error; err != nil || cluster.ObservationCount != 1 || len(survivors) != 2 || survivors[0] != 2 || survivors[1] != 4 {
		t.Fatalf("留下视频与在世人物的观测，删观测后按实际观测重算簇: %+v survivors=%v err=%v", cluster, survivors, err)
	}
	state, _ := service.Latest(context.Background())
	if latest := healthCheckByKey(t, state.Report, DataHealthCheckDanglingRefs); latest.Count != 2 {
		t.Fatalf("最近一份报告里的这一项应换成重跑结果: %+v", latest)
	}
}

// D-DH-04 / D-DH-05：派生计数——四种计数漂移都能查出并按明细重算；人脸簇走 recomputeFaceClustersTx（一条观测都不剩的簇
// 连同人物候选与关系记账一起删）；截断的帧哈希不算漂移；play_count 不由明细派生，不在检查中。
func TestDataHealthDerivedCountsDetectAndRecompute(t *testing.T) {
	service, db, dataDir, keptID := setupDataHealthTest(t)
	mustExec(t, db, "UPDATE videos SET play_count = 7 WHERE id = ?", keptID) // 没有任何播放事件
	mustExec(t, db, "INSERT INTO subtitle_index_states (video_id, subtitle_path, segment_count, created_at, updated_at, last_checked_at) VALUES (?, 'a.srt', 5, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)", keptID)
	mustExec(t, db, "INSERT INTO subtitle_segments (video_id, segment_index, text) VALUES (?, 1, 'a'), (?, 2, 'b')", keptID, keptID)
	mustExec(t, db, "INSERT INTO scene_index_states (video_id, source_size, source_mtime_ns, interval_ms, segment_count, updated_at) VALUES (?, 1, 1, 1000, 3, CURRENT_TIMESTAMP)", keptID)
	mustExec(t, db, "INSERT INTO scene_visual_segments (video_id, start_ms, end_ms) VALUES (?, 0, 1000)", keptID)
	mustExec(t, db, "INSERT INTO face_clusters (id, status, observation_count, created_at, updated_at) VALUES (3, 'unnamed', 4, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), (9, 'unnamed', 2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)")
	mustExec(t, db, `INSERT INTO face_observations (media_kind, media_id, source_fingerprint, frame_ms, bbox_hash, cluster_id, created_at, updated_at)
		VALUES ('video', ?, 'a', 0, 'x', 3, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, keptID)
	person := models.Person{DisplayName: "候选人"}
	if err := db.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO face_person_candidates (cluster_id, person_id, similarity, created_at, updated_at) VALUES (9, ?, 0.9, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)", person.ID)
	mustExec(t, db, "INSERT INTO face_relation_writes (person_id, media_kind, media_id, cluster_id, created_at) VALUES (?, 'video', ?, 9, CURRENT_TIMESTAMP)", person.ID, keptID)
	second := models.Video{Name: "second.mp4", Path: filepath.Join(dataDir, "second.mp4"), Directory: dataDir}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO video_frame_hash_sequences (video_id, interval_ms, hashes, frame_count, source_size, source_mod_time_ns, computed_at, created_at, updated_at) VALUES (?, 500, ?, 3, 1, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)", keptID, make([]byte, 16))
	mustExec(t, db, "INSERT INTO video_frame_hash_sequences (video_id, interval_ms, hashes, frame_count, source_size, source_mod_time_ns, computed_at, created_at, updated_at) VALUES (?, 500, ?, 7, 1, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)", second.ID, make([]byte, 12))

	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check := healthCheckByKey(t, report, DataHealthCheckDerivedCounts)
	if check.Status != DataHealthStatusProblem || check.Count != 5 || len(check.Groups) != 4 {
		t.Fatalf("四种计数共 5 行漂移，截断的帧哈希不算: %+v", check)
	}
	if group, _ := healthGroupByKey(check, "subtitle_index_states.segment_count"); len(group.Examples) == 0 || group.Examples[0] != fmt.Sprintf("视频 %d：记 5，实际 2", keptID) {
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
	if err != nil || result.Repaired != 5 || result.Result.Status != DataHealthStatusOK {
		t.Fatalf("应重算 5 行并在重跑后通过: %+v err=%v", result, err)
	}
	var playCount, truncatedFrames, candidates, writes int64
	db.Raw("SELECT play_count FROM videos WHERE id = ?", keptID).Scan(&playCount)
	db.Raw("SELECT frame_count FROM video_frame_hash_sequences WHERE video_id = ?", second.ID).Scan(&truncatedFrames)
	db.Raw("SELECT COUNT(*) FROM face_person_candidates WHERE cluster_id = 9").Scan(&candidates)
	db.Raw("SELECT COUNT(*) FROM face_relation_writes WHERE cluster_id = 9").Scan(&writes)
	if playCount != 7 || truncatedFrames != 7 {
		t.Fatalf("修复不应动 play_count 或截断的帧哈希: %d %d", playCount, truncatedFrames)
	}
	var emptyCluster int64
	db.Model(&models.FaceCluster{}).Where("id = 9").Count(&emptyCluster)
	var kept models.FaceCluster
	if err := db.First(&kept, 3).Error; err != nil || kept.ObservationCount != 1 || emptyCluster != 0 || candidates != 0 || writes != 0 {
		t.Fatalf("簇 3 重算为 1；空簇 9 连同候选与记账一起删: %+v empty=%d candidates=%d writes=%d err=%v", kept, emptyCluster, candidates, writes, err)
	}
}

// D-DH-04 / D-DH-05：托管路径——数据目录内且文件不存在的才算；修复只清空这些列，文件在的、绝对路径与越界路径都不动。
// 审阅 I4：整个托管子目录不可用（不存在或链接失效）时，其下的行不判、不清，只报告目录不可用。
func TestDataHealthManagedPathsClearOnlyMissingFiles(t *testing.T) {
	service, db, dataDir, keptID := setupDataHealthTest(t)
	writeDataCareFile(t, filepath.Join(dataDir, "media-details/people/1/a.jpg"), "avatar")
	if err := os.MkdirAll(filepath.Join(dataDir, "media-details/watchlist"), 0755); err != nil {
		t.Fatal(err)
	}
	// videos 是一条失效的符号链接（外接盘没挂），collections 整个不存在。
	if err := os.Symlink(filepath.Join(dataDir, "unplugged-volume"), filepath.Join(dataDir, "media-details/videos")); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "UPDATE videos SET poster_path = 'videos/1/p.jpg', fanart_path = '/abs/elsewhere.jpg' WHERE id = ?", keptID)
	mustExec(t, db, `INSERT INTO people (id, display_name, avatar_path, created_at, updated_at) VALUES
		(1, 'a', 'people/1/a.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(2, 'b', '../outside.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		(3, 'c', 'people/3/gone.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	mustExec(t, db, "INSERT INTO media_collections (name, normalized_name, cover_path, created_at, updated_at) VALUES ('c', 'c', 'collections/1/cover.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)")
	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check := healthCheckByKey(t, report, DataHealthCheckManagedPaths)
	missingGroup, _ := healthGroupByKey(check, "people.avatar_path")
	videosGroup, videosOK := healthGroupByKey(check, "unavailable:videos")
	collectionsGroup, collectionsOK := healthGroupByKey(check, "unavailable:collections")
	if check.Count != 1 || !check.Repairable || missingGroup.Examples[0] != "media-details/people/3/gone.jpg" ||
		!videosOK || videosGroup.Count != 1 || videosGroup.Repairable || !collectionsOK || collectionsGroup.Count != 1 ||
		!strings.Contains(check.Summary, "目录不可用") {
		t.Fatalf("只算可用目录下缺失的文件，不可用的目录单独报告: %+v", check)
	}
	result, err := service.Repair(context.Background(), DataHealthCheckManagedPaths)
	if err != nil || result.Repaired != 1 || result.Result.Repairable || result.Result.Status != DataHealthStatusWarning {
		t.Fatalf("应只清空一条路径，重跑后只剩目录不可用的提醒: %+v err=%v", result, err)
	}
	var video models.Video
	db.Unscoped().First(&video, keptID)
	var avatars, covers []string
	db.Raw("SELECT avatar_path FROM people ORDER BY id").Scan(&avatars)
	db.Raw("SELECT cover_path FROM media_collections").Scan(&covers)
	if video.PosterPath != "videos/1/p.jpg" || video.FanartPath != "/abs/elsewhere.jpg" ||
		strings.Join(avatars, ",") != "people/1/a.jpg,../outside.jpg," || strings.Join(covers, ",") != "collections/1/cover.jpg" {
		t.Fatalf("修复只应清空可用目录下缺失文件的那一列: %q %q %v %v", video.PosterPath, video.FanartPath, avatars, covers)
	}
}

// D-DH-04：SQLite 外键悬挂只报不修。
func TestDataHealthForeignKeysReportOnly(t *testing.T) {
	skipUnlessSQLiteHealth(t)
	service, db, _, _ := setupDataHealthTest(t)
	if err := db.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
			return err
		}
		if err := conn.Exec("INSERT INTO subtitle_segments (video_id, segment_index, text) VALUES (?, 1, 'orphan')", healthMissingID).Error; err != nil {
			return err
		}
		return conn.Exec("PRAGMA foreign_keys = ON").Error
	}); err != nil {
		t.Fatal(err)
	}
	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fk := healthCheckByKey(t, report, DataHealthCheckForeignKeys)
	if group, ok := healthGroupByKey(fk, "subtitle_segments→videos"); fk.Status != DataHealthStatusProblem || !ok || group.Count != 1 || fk.Repairable {
		t.Fatalf("应报出外键悬挂且不可修: %+v", fk)
	}
}

// D-DH-04：唯一索引缺失（含它挡不住的重复组）只报不修。
func TestDataHealthMissingUniqueIndexReportOnly(t *testing.T) {
	service, db, _, _ := setupDataHealthTest(t)
	mustExec(t, db, "DROP INDEX idx_saved_library_views_name_active")
	mustExec(t, db, "INSERT INTO saved_library_views (name, created_at, updated_at) VALUES ('dup', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), ('dup', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)")
	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
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
	skipUnlessSQLiteHealth(t)
	path := filepath.Join(t.TempDir(), "broken.db")
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
	service, _, _, _ := setupDataHealthTest(t)
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
	if dbtest.IsPostgres() {
		return // 下面要真做一份 SQLite 备份
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
	service, db, _, _ := setupDataHealthTest(t)
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
}

// 审阅 R6：media-details 本身不可用（失效的符号链接）时，所有托管路径都不判、不清，只报告目录不可用。
func TestDataHealthManagedPathsWholeMediaDetailsUnavailable(t *testing.T) {
	service, db, dataDir, keptID := setupDataHealthTest(t)
	if err := os.Symlink(filepath.Join(dataDir, "unplugged-volume"), filepath.Join(dataDir, "media-details")); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "UPDATE videos SET poster_path = 'videos/1/p.jpg' WHERE id = ?", keptID)
	mustExec(t, db, "INSERT INTO people (id, display_name, avatar_path, created_at, updated_at) VALUES (1, 'a', 'people/1/a.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)")
	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check := healthCheckByKey(t, report, DataHealthCheckManagedPaths)
	videos, videosOK := healthGroupByKey(check, "unavailable:videos")
	people, peopleOK := healthGroupByKey(check, "unavailable:people")
	if check.Status != DataHealthStatusWarning || check.Count != 0 || check.Repairable || !videosOK || videos.Count != 1 || !peopleOK || people.Count != 1 {
		t.Fatalf("media-details 不可用时只报告目录不可用: %+v", check)
	}
	if _, err := service.Repair(context.Background(), DataHealthCheckManagedPaths); err != nil {
		t.Fatal(err)
	}
	var poster, avatar string
	db.Raw("SELECT poster_path FROM videos WHERE id = ?", keptID).Scan(&poster)
	db.Raw("SELECT avatar_path FROM people WHERE id = 1").Scan(&avatar)
	if poster != "videos/1/p.jpg" || avatar != "people/1/a.jpg" {
		t.Fatalf("修复不应清空任何路径: %q %q", poster, avatar)
	}
}
