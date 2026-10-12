package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// 数据维护 · 备份三件套、校验与恢复（D-DH-01 / D-DH-02 / D-DH-03）。

// setupCuratedBackupTest 开一个真 SQLite 库（openLiveSQLiteLibrary），在数据目录里铺好整理过的文件，
// 时间从固定点起每次调用前进一秒，保证每份备份名字不同。
func setupCuratedBackupTest(t *testing.T) (*BackupService, string, string) {
	t.Helper()
	if dbtest.IsPostgres() {
		t.Skip("本用例针对 SQLite 后端的备份三件套")
	}
	dataDir := t.TempDir()
	db := openLiveSQLiteLibrary(t, dataDir)
	if err := db.Create(&models.Video{Name: "kept.mp4", Path: filepath.Join(dataDir, "kept.mp4"), Directory: dataDir}).Error; err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"media-details/people/1/avatar.jpg":      "avatar",
		"media-details/collections/2/cover.png":  "cover",
		"wallpapers/w.jpg":                       "wallpaper",
		"subtitle-backups/3/a.srt":               "subtitle",
		"thumbnails/1.jpg":                       "cache, not curated",
		".env":                                   "PG_PASSWORD=secret",
		"media-details/people/1/.image-import-x": "in-flight temp",
	} {
		writeDataCareFile(t, filepath.Join(dataDir, filepath.FromSlash(rel)), body)
	}
	// 不跟随符号链接：指向数据目录外的链接不得进包。
	outside := filepath.Join(t.TempDir(), "outside.jpg")
	writeDataCareFile(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(dataDir, "media-details/people/1/link.jpg")); err != nil {
		t.Fatal(err)
	}
	service := NewBackupService(dataDir)
	clock := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	return service, dataDir, filepath.Join(dataDir, "backups")
}

func writeDataCareFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func readDataCareManifest(t *testing.T, directory, snapshot string) BackupManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, backupManifestName(snapshot)))
	if err != nil {
		t.Fatalf("读取清单失败: %v", err)
	}
	var manifest BackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("清单不是合法 JSON: %v", err)
	}
	return manifest
}

func backupDirNames(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

// D-DH-02：新备份是快照 + 文件包 + 清单三件套；文件包只收整理过的六个目录（不含缓存、.env、
// 符号链接与发布中的临时文件），清单记两份文件的大小与哈希、条目、表结构、行数与 quick_check。
func TestDataCareBackupWritesManifestTrio(t *testing.T) {
	service, _, directory := setupCuratedBackupTest(t)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	stem := backupStem(backup.Name)
	if got, want := backupDirNames(t, directory), []string{stem + ".files.tar", stem + ".manifest.json", backup.Name}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("备份目录应只有三件套: %v", got)
	}
	manifest := readDataCareManifest(t, directory, backup.Name)
	if manifest.Format != 1 || manifest.Backend != "sqlite" || manifest.QuickCheck != "ok" {
		t.Fatalf("清单头不对: %+v", manifest)
	}
	if hash, _ := hashFile(filepath.Join(directory, backup.Name)); manifest.Snapshot.SHA256 != hash || manifest.Snapshot.Size != backup.Size {
		t.Fatalf("快照哈希/大小与清单不一致")
	}
	filesPath := filepath.Join(directory, manifest.Files.Name)
	info, err := os.Stat(filesPath)
	if hash, _ := hashFile(filesPath); err != nil || manifest.Files.SHA256 != hash || manifest.Files.Size != info.Size() {
		t.Fatalf("文件包哈希/大小与清单不一致: %+v err=%v", manifest.Files, err)
	}
	paths := []string{}
	for _, entry := range manifest.Files.Entries {
		paths = append(paths, entry.Path)
	}
	want := "media-details/collections/2/cover.png,media-details/people/1/avatar.jpg,subtitle-backups/3/a.srt,wallpapers/w.jpg"
	if sort.Strings(paths); strings.Join(paths, ",") != want {
		t.Fatalf("文件包条目不对: %v", paths)
	}
	if manifest.Rows["videos"] != 1 || !containsString(manifest.Schema.Tables["videos"], "poster_path") {
		t.Fatalf("清单应记录行数与表结构: rows=%v", manifest.Rows["videos"])
	}
	if _, ok := manifest.Rows["sqlite_sequence"]; ok {
		t.Fatal("SQLite 内部表不进清单")
	}
	if hash, entryErr, ioErr := readBackupFilesPack(filesPath, manifest.Files, ""); ioErr != nil || entryErr != nil || hash != manifest.Files.SHA256 {
		t.Fatalf("文件包应能逐条读出并与清单一致: %v %v", entryErr, ioErr)
	}
	listed, err := service.ListBackups()
	if err != nil || len(listed) != 1 {
		t.Fatalf("列表应有一份: %v err=%v", listed, err)
	}
	if got := listed[0]; got.Legacy || got.Fingerprint != manifest.Snapshot.SHA256 || got.FileCount != 4 || got.FilesSize != manifest.Files.Size {
		t.Fatalf("列表应取清单里的大小与哈希: %+v", got)
	}
}

// D-DH-02：快照没通过 quick_check 就不发布，记一次失败。
func TestDataCareBackupQuickCheckFailureIsNotPublished(t *testing.T) {
	service, _, directory := setupCuratedBackupTest(t)
	service.inspectSnapshot = func(ctx context.Context, path string) (sqliteSnapshotFacts, error) {
		corruptSQLiteTail(t, path)
		return inspectSQLiteSnapshot(ctx, path)
	}
	if _, err := service.CreateBackup(context.Background()); err == nil || !strings.Contains(err.Error(), "未通过完整性检查") {
		t.Fatalf("坏快照应因 quick_check 不通过而失败: %v", err)
	}
	for _, name := range backupDirNames(t, directory) {
		if backupMemberPattern.MatchString(name) || strings.HasSuffix(name, ".tmp") {
			t.Fatalf("坏快照不应发布任何成员，也不留临时文件: %s", name)
		}
	}
	settings, err := (&SettingsService{}).GetSettings()
	if err != nil || settings.BackupLastError == "" || settings.BackupLastSuccessAt != nil {
		t.Fatalf("应记一次失败: %+v err=%v", settings, err)
	}
}

// corruptSQLiteTail 把库文件最后几页写成乱码：表页坏掉但库仍能打开，quick_check 报问题。
func corruptSQLiteTail(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for index := len(data) - 3*4096; index < len(data); index++ {
		if index > 4096 {
			data[index] = byte(index*7 + 13)
		}
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// D-DH-01：可用空间放不下「文件包 + 快照」时这次备份失败，原因码 backup_insufficient_space，什么都不发布。
func TestDataCareBackupInsufficientSpaceFails(t *testing.T) {
	service, _, directory := setupCuratedBackupTest(t)
	var checked string
	service.diskFree = func(path string) (uint64, error) {
		checked = path
		return 1024, nil
	}
	_, err := service.CreateBackup(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), BackupReasonInsufficientSpace+":") {
		t.Fatalf("空间不足应以原因码失败: %v", err)
	}
	if checked != directory {
		t.Fatalf("应检查备份目录所在的卷: %q", checked)
	}
	for _, name := range backupDirNames(t, directory) {
		if backupMemberPattern.MatchString(name) || strings.HasSuffix(name, ".tmp") {
			t.Fatalf("空间不足时不应发布任何成员: %s", name)
		}
	}
	settings, _ := (&SettingsService{}).GetSettings()
	if !strings.HasPrefix(settings.BackupLastError, BackupReasonInsufficientSpace) {
		t.Fatalf("失败原因应记入 backup_last_error: %q", settings.BackupLastError)
	}
}

// 4.1：轮转以快照为单位成组删除；有文件包没清单的半成品、失去快照的文件包与清单先清掉，
// 不占保留份数；只有快照的旧格式备份按普通备份参与轮转。
func TestDataCareBackupRotationRemovesWholeGroups(t *testing.T) {
	service, _, directory := setupCuratedBackupTest(t)
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("backup_retention_count", 2).Error; err != nil {
		t.Fatal(err)
	}
	writeDataCareFile(t, filepath.Join(directory, "cineinsight-20200101-000000.000000000.sqlite"), "legacy")
	writeDataCareFile(t, filepath.Join(directory, "cineinsight-20200102-000000.000000000.sqlite"), "half")
	writeDataCareFile(t, filepath.Join(directory, "cineinsight-20200102-000000.000000000.files.tar"), "half")
	writeDataCareFile(t, filepath.Join(directory, "cineinsight-20200103-000000.000000000.manifest.json"), "{}")
	writeDataCareFile(t, filepath.Join(directory, "cineinsight-20200104-000000.000000000.files.tar"), "orphan")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"cineinsight-20200101-000000.000000000.sqlite", "cineinsight-20200102-000000.000000000.sqlite"} {
		if err := os.Chtimes(filepath.Join(directory, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	first, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{}
	for _, name := range []string{first.Name, second.Name} {
		want = append(want, name, backupFilesName(name), backupManifestName(name))
	}
	sort.Strings(want)
	if got := backupDirNames(t, directory); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("应只剩两份完整的三件套:\n got %v\nwant %v", got, want)
	}
}

// restoreDataCareBackup 照 App.enterDatabaseRestoreMode(true) 的两步立围栏、关连接，再恢复。
func restoreDataCareBackup(t *testing.T, service *BackupService, backup BackupFile, beforeFence func()) (error, bool) {
	t.Helper()
	var release func()
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	fenced := false
	beforeRestore := func() error {
		fenced = true
		if beforeFence != nil {
			beforeFence()
		}
		release = database.BeginMaintenance()
		return database.Close()
	}
	err := service.RestoreBackupWithLifecycle(context.Background(),
		BackupRestoreRequest{Name: backup.Name, Size: backup.Size, Fingerprint: backup.Fingerprint}, beforeRestore, nil)
	return err, fenced
}

func dataCareFileBody(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "<missing>"
	}
	return string(data)
}

// D-DH-03：恢复带清单的备份时，整理过的文件随数据库一起回到备份点；改名留下的旧目录与暂存目录都不留。
// 老库里模型已删除的遗留列不让当前版本自己的备份被判成「来自更新版本」。
func TestDataCareRestoreBringsBackCuratedFiles(t *testing.T) {
	service, dataDir, _ := setupCuratedBackupTest(t)
	if err := database.DB.Exec("ALTER TABLE videos ADD COLUMN retired_column TEXT").Error; err != nil {
		t.Fatal(err)
	}
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	avatar := filepath.Join(dataDir, "media-details/people/1/avatar.jpg")
	wallpaper := filepath.Join(dataDir, "wallpapers/w.jpg")
	added := filepath.Join(dataDir, "media-details/people/9/new.jpg")
	if err := os.Remove(avatar); err != nil {
		t.Fatal(err)
	}
	writeDataCareFile(t, wallpaper, "changed after backup")
	writeDataCareFile(t, added, "added after backup")

	if err, _ := restoreDataCareBackup(t, service, *backup, nil); err != nil {
		t.Fatalf("恢复应成功: %v", err)
	}
	if dataCareFileBody(t, avatar) != "avatar" || dataCareFileBody(t, wallpaper) != "wallpaper" {
		t.Fatal("整理过的文件应回到备份点")
	}
	if _, err := os.Stat(added); !os.IsNotExist(err) {
		t.Fatalf("备份之后新增的文件应随目录一起被替换掉: %v", err)
	}
	if dataCareFileBody(t, filepath.Join(dataDir, "thumbnails/1.jpg")) == "<missing>" {
		t.Fatal("文件包之外的目录不应被动")
	}
	for _, pattern := range []string{"media-details/*.pre-restore-*", "*.pre-restore-*", ".cineinsight-restore-files-*"} {
		if leftovers, _ := filepath.Glob(filepath.Join(dataDir, pattern)); len(leftovers) != 0 {
			t.Fatalf("成功后不应留下旧目录或暂存目录: %v", leftovers)
		}
	}
}

// D-DH-02：没有清单的旧格式备份照常列出（标 legacy）、照常可恢复，且不动整理文件目录。
func TestDataCareLegacyBackupListedAndRestorableWithoutTouchingFiles(t *testing.T) {
	service, dataDir, directory := setupCuratedBackupTest(t)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{backupManifestName(backup.Name), backupFilesName(backup.Name)} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := service.ListBackups()
	if err != nil || len(listed) != 1 || !listed[0].Legacy || listed[0].Fingerprint != backup.Fingerprint {
		t.Fatalf("旧格式备份应照常列出并标 legacy: %+v err=%v", listed, err)
	}
	added := filepath.Join(dataDir, "media-details/people/9/new.jpg")
	writeDataCareFile(t, added, "added after backup")
	if err, _ := restoreDataCareBackup(t, service, listed[0], nil); err != nil {
		t.Fatalf("旧格式备份应可恢复: %v", err)
	}
	if dataCareFileBody(t, added) != "added after backup" {
		t.Fatal("旧格式备份恢复时不应动整理文件目录")
	}
	if names := readVideoNamesFromFile(t, database.SQLitePath(dataDir)); len(names) != 1 || names[0] != "kept.mp4" {
		t.Fatalf("数据库应恢复: %v", names)
	}
}

// D-DH-03：清单里有当前版本不认识的表或列，说明备份来自更新的版本：拒绝恢复，不进入维护、不做安全备份。
func TestDataCareRestoreRefusesBackupFromNewerVersion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*BackupManifest)
	}{
		{"unknown table", func(m *BackupManifest) { m.Schema.Tables["future_things"] = []string{"id"} }},
		{"unknown column", func(m *BackupManifest) {
			m.Schema.Tables["videos"] = append(m.Schema.Tables["videos"], "future_column")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, _, directory := setupCuratedBackupTest(t)
			backup, err := service.CreateBackup(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			manifest := readDataCareManifest(t, directory, backup.Name)
			tc.alter(&manifest)
			data, _ := json.Marshal(manifest)
			if err := os.WriteFile(filepath.Join(directory, backupManifestName(backup.Name)), data, 0600); err != nil {
				t.Fatal(err)
			}
			err, fenced := restoreDataCareBackup(t, service, *backup, nil)
			if err == nil || !strings.HasPrefix(err.Error(), BackupReasonFromNewerVersion+":") || DatabaseRestoreRequiresRestart(err) {
				t.Fatalf("应以 backup_from_newer_version 拒绝: %v", err)
			}
			if fenced || countSQLiteSnapshots(t, directory) != 1 {
				t.Fatalf("拒绝时不应进入维护或做安全备份: fenced=%v", fenced)
			}
			var count int64
			if err := database.DB.Model(&models.Video{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("库应原样可用: %d err=%v", count, err)
			}
		})
	}
}

// 表结构判定：Postgres 只比表名；列为 nil 的已知表（裸 DDL 建的）不按列核对。
func TestDataCareUnknownBackupSchema(t *testing.T) {
	known := map[string]map[string]bool{"videos": {"id": true, "name": true}, "video_semantic_vectors": nil}
	schema := BackupManifestSchema{Tables: map[string][]string{
		"videos": {"id", "name", "future"}, "video_semantic_vectors": {"anything"}, "future_table": {},
	}}
	if got := unknownBackupSchema(schema, known, false); strings.Join(got, ",") != "future_table,videos.future" {
		t.Fatalf("SQLite 应报出未知表与列: %v", got)
	}
	if got := unknownBackupSchema(schema, known, true); strings.Join(got, ",") != "future_table" {
		t.Fatalf("Postgres 只比表名: %v", got)
	}
	if got := unknownBackupSchema(BackupManifestSchema{Tables: map[string][]string{"videos": {"id"}}}, known, false); len(got) != 0 {
		t.Fatalf("已知的表与列应兼容: %v", got)
	}
}

// 4.2：文件恢复失败时数据库已经替换，不回滚；提示可从安全备份取回，改名留下的 .pre-restore-* 目录保留。
func TestDataCareRestoreFilesFailureKeepsPreRestoreDirectories(t *testing.T) {
	service, dataDir, _ := setupCuratedBackupTest(t)
	writeDataCareFile(t, filepath.Join(dataDir, "media-details/videos/5/poster.jpg"), "poster")
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.Video{Name: "after.mp4", Path: filepath.Join(dataDir, "after.mp4"), Directory: dataDir}).Error; err != nil {
		t.Fatal(err)
	}
	// 冻结时钟，在第三个目录（media-details/videos）的改名目标上先放一个非空目录，让它改名失败。
	frozen := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	stamp := frozen.Format("20060102-150405.000000000")
	err, _ = restoreDataCareBackup(t, service, *backup, func() {
		service.now = func() time.Time { return frozen }
		writeDataCareFile(t, filepath.Join(dataDir, "media-details/videos.pre-restore-"+stamp, "blocker"), "x")
	})
	if !DatabaseRestoreRequiresRestart(err) || !strings.HasPrefix(err.Error(), DatabaseRestoreReasonCommitted) ||
		!strings.Contains(err.Error(), "整理过的文件未能还原") || strings.Contains(err.Error(), dataDir) {
		t.Fatalf("应报「数据库已恢复、文件未能还原」并要求重启，且不带路径: %v", err)
	}
	if names := readVideoNamesFromFile(t, database.SQLitePath(dataDir)); len(names) != 1 || names[0] != "kept.mp4" {
		t.Fatalf("数据库已替换，不回滚: %v", names)
	}
	// 前两个目录已经换好，改名留下的旧目录（恢复前的内容）保留不删。
	if dataCareFileBody(t, filepath.Join(dataDir, "media-details/people.pre-restore-"+stamp, "1/avatar.jpg")) != "avatar" {
		t.Fatal("people 的旧目录应保留")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "media-details/collections.pre-restore-"+stamp)); err != nil {
		t.Fatalf("collections 的旧目录应保留: %v", err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dataDir, ".cineinsight-restore-files-*")); len(leftovers) != 0 {
		t.Fatalf("暂存目录应删除: %v", leftovers)
	}
}

// D-DH-03：校验——full 档全部通过并记下结果；改坏文件包或清单行数后对应的结论变成失败；
// 没有清单的旧格式备份只检查能否打开。
func TestDataCareVerifyBackupLevels(t *testing.T) {
	service, _, directory := setupCuratedBackupTest(t)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.VerifyDatabaseBackup(context.Background(), backup.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Level != BackupVerifyLevelFull || !result.HashOK || !result.FilesOK ||
		result.QuickCheck != "ok" || !result.RowsOK || !result.SchemaCompatible || len(result.Problems) != 0 {
		t.Fatalf("完好的备份应全部通过: %+v", result)
	}
	listed, _ := service.ListBackups()
	if len(listed) != 1 || listed[0].Verification == nil || !listed[0].Verification.OK {
		t.Fatalf("列表应带上最近一次校验结果: %+v", listed)
	}

	manifest := readDataCareManifest(t, directory, backup.Name)
	manifest.Rows["videos"] = 99
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(directory, backupManifestName(backup.Name)), data, 0600); err != nil {
		t.Fatal(err)
	}
	if result, _ := service.VerifyDatabaseBackup(context.Background(), backup.Name); result.OK || result.RowsOK || !result.HashOK {
		t.Fatalf("行数对不上应只让 rows_ok 失败: %+v", result)
	}

	packPath := filepath.Join(directory, backupFilesName(backup.Name))
	pack, _ := os.ReadFile(packPath)
	// 改的是文件内容（最后一处 "wallpaper"），不是 tar 头里的路径。
	index := strings.LastIndex(string(pack), "wallpaper")
	pack[index] = 'W'
	if err := os.WriteFile(packPath, pack, 0600); err != nil {
		t.Fatal(err)
	}
	result, _ = service.VerifyDatabaseBackup(context.Background(), backup.Name)
	if result.OK || result.HashOK || result.FilesOK || !containsString(result.Problems, "文件包条目内容与清单不一致") {
		t.Fatalf("文件包内容被改动应让哈希与条目两项都失败: %+v", result)
	}

	for _, name := range []string{backupManifestName(backup.Name), backupFilesName(backup.Name)} {
		_ = os.Remove(filepath.Join(directory, name))
	}
	if result, _ := service.VerifyDatabaseBackup(context.Background(), backup.Name); !result.OK || result.Level != BackupVerifyLevelLegacy {
		t.Fatalf("旧格式备份应按 legacy 档只检查能否打开: %+v", result)
	}
	if _, err := service.VerifyDatabaseBackup(context.Background(), "../escape.sqlite"); err == nil {
		t.Fatal("非法文件名应被拒绝")
	}
}

// D-DH-02 / D-DH-03：Postgres 备份的清单表名取自 pg_restore --list；校验按 archive 档（哈希 + 归档可读），
// 有当前版本不认识的表时校验报不兼容、恢复以 backup_from_newer_version 拒绝且不跑 pg_restore 恢复。
func TestDataCarePostgresBackupManifestAndNewerVersion(t *testing.T) {
	service, runner, directory := setupBackupServiceTest(t, 7, 24)
	runner.listing = ";\n; Archive created\n;\n215; 1259 16386 TABLE public videos cineinsight\n3456; 0 16386 TABLE DATA public videos cineinsight\n"
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manifest := readDataCareManifest(t, directory, backup.Name)
	if manifest.Backend != "postgres" || len(manifest.Schema.Tables) != 1 || manifest.Schema.Tables["videos"] == nil || manifest.Rows != nil {
		t.Fatalf("Postgres 清单只记表名: %+v", manifest)
	}
	result, err := service.VerifyDatabaseBackup(context.Background(), backup.Name)
	if err != nil || !result.OK || result.Level != BackupVerifyLevelArchive || !result.HashOK || !result.FilesOK {
		t.Fatalf("archive 档应通过: %+v err=%v", result, err)
	}

	runner.listing += "216; 1259 16390 TABLE public future_things cineinsight\n"
	service.now = func() time.Time { return time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC) }
	newer, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result, _ := service.VerifyDatabaseBackup(context.Background(), newer.Name); result.OK || result.SchemaCompatible {
		t.Fatalf("有未知表时应报不兼容: %+v", result)
	}
	runner.calls = nil
	err = service.RestoreBackup(context.Background(), BackupRestoreRequest{Name: newer.Name, Size: newer.Size, Fingerprint: newer.Fingerprint})
	if err == nil || !strings.HasPrefix(err.Error(), BackupReasonFromNewerVersion+":") {
		t.Fatalf("应以 backup_from_newer_version 拒绝: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("拒绝时不应调用任何 pg 工具: %#v", runner.calls)
	}
}

// 恢复前的安全备份不要求快照通过完整性检查：库坏了正是要恢复的时候，那份快照照样发布，问题记进清单。
func TestDataCareSafetyBackupKeepsDamagedSnapshotWithProblemRecorded(t *testing.T) {
	service, _, directory := setupCuratedBackupTest(t)
	service.inspectSnapshot = func(ctx context.Context, path string) (sqliteSnapshotFacts, error) {
		corruptSQLiteTail(t, path)
		return inspectSQLiteSnapshot(ctx, path)
	}
	if err := service.performSafetyBackup(context.Background(), directory, 7, ""); err != nil {
		t.Fatalf("安全备份不应因完整性检查失败: %v", err)
	}
	listed, err := service.ListBackups()
	if err != nil || len(listed) != 1 || listed[0].Legacy {
		t.Fatalf("安全备份应完整发布三件套: %+v err=%v", listed, err)
	}
	if manifest := readDataCareManifest(t, directory, listed[0].Name); manifest.QuickCheck == "ok" || manifest.QuickCheck == "" {
		t.Fatalf("清单应记下完整性问题: %q", manifest.QuickCheck)
	}
}

// D-DH-03：快照被改动（哪怕仍能打开）时 hash_ok 失败。
func TestDataCareVerifyDetectsChangedSnapshot(t *testing.T) {
	service, _, directory := setupCuratedBackupTest(t)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(directory, backup.Name), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte{0})
	_ = file.Close()
	result, err := service.VerifyDatabaseBackup(context.Background(), backup.Name)
	if err != nil || result.OK || result.HashOK || !result.FilesOK {
		t.Fatalf("快照被改动应只让哈希一项失败: %+v err=%v", result, err)
	}
}
