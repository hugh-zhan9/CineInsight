package services

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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
	if hash, entryErr, ioErr := readBackupFilesPack(filesPath, manifest.Files, nil); ioErr != nil || entryErr != nil || hash != manifest.Files.SHA256 {
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

// dataCareFileBody 读出文件内容；读不到时返回 "<missing>"。
func dataCareFileBody(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "<missing>"
	}
	return string(data)
}

// D-DH-03：恢复带清单的备份时，整理过的文件随数据库一起回到备份点；暂存目录建在各收录目录的同级
// （同一父目录，改名不跨盘）；改名留下的旧目录与暂存目录都不留。
func TestDataCareRestoreBringsBackCuratedFiles(t *testing.T) {
	service, dataDir, _ := setupCuratedBackupTest(t)
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

	service.rename = func(from, to string) error {
		if strings.Contains(filepath.Base(from), backupFilesStagingPrefix) && filepath.Dir(from) != filepath.Dir(to) {
			t.Errorf("暂存目录应与目标同级: %s → %s", from, to)
		}
		return os.Rename(from, to)
	}
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
	for _, pattern := range []string{"media-details/*.pre-restore-*", "*.pre-restore-*", ".cineinsight-restore-*", "media-details/.cineinsight-restore-*"} {
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

// D-DH-03：清单 models（做备份的版本的模型结构）里有当前版本不认识的表或列，说明备份来自更新的版本：
// 拒绝恢复，不进入维护、不做安全备份。
func TestDataCareRestoreRefusesBackupFromNewerVersion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*BackupManifest)
	}{
		{"unknown table", func(m *BackupManifest) { m.Models["future_things"] = []string{"id"} }},
		{"unknown column", func(m *BackupManifest) {
			m.Models["videos"] = append(m.Models["videos"], "future_column")
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

// 表结构判定看清单 models：当前模型里有、或登记为退役（整表或单列）的都认识；清单没有 models 时按兼容处理。
func TestDataCareUnknownModelSchema(t *testing.T) {
	current := map[string][]string{"videos": {"id", "name"}}
	retired := map[string][]string{"videos": {"old_column"}, "gone_table": {}}
	recorded := map[string][]string{
		"videos": {"id", "name", "old_column", "future"}, "gone_table": {"anything"}, "future_table": {},
	}
	if got := unknownModelSchema(recorded, current, retired); strings.Join(got, ",") != "future_table,videos.future" {
		t.Fatalf("应只报出未知表与未知列: %v", got)
	}
	if got := unknownModelSchema(nil, current, retired); len(got) != 0 {
		t.Fatalf("没有 models 的清单按兼容处理: %v", got)
	}
	models, err := currentModelSchema()
	if err != nil || !containsString(models["videos"], "poster_path") || strings.Join(models["video_tags"], ",") != "tag_id,video_id" {
		t.Fatalf("当前模型结构应含模型列与连接表: videos=%v video_tags=%v err=%v", models["videos"], models["video_tags"], err)
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

// D-DH-02 / D-DH-03：Postgres 备份清单的 schema.tables 取自 pg_restore --list（参考信息）；校验按 archive 档
// （哈希 + 归档可读）；models 里有当前版本不认识的表时校验报不兼容、恢复以 backup_from_newer_version 拒绝且不跑 pg 工具。
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

	// 库里多出的表只进参考用的 schema.tables，不影响兼容判定；判定看 models。
	runner.listing += "216; 1259 16390 TABLE public leftover_things cineinsight\n"
	service.now = func() time.Time { return time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC) }
	newer, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result, _ := service.VerifyDatabaseBackup(context.Background(), newer.Name); !result.SchemaCompatible {
		t.Fatalf("库里的遗留表不让备份不兼容: %+v", result)
	}
	newerManifest := readDataCareManifest(t, directory, newer.Name)
	newerManifest.Models["future_things"] = []string{"id"}
	data, _ := json.Marshal(newerManifest)
	if err := os.WriteFile(filepath.Join(directory, backupManifestName(newer.Name)), data, 0600); err != nil {
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

// 4.2（审阅 I1）：文件包在进入维护之前就核对——缺失、内容被改、大小不符都以 backup_files_invalid 拒绝，
// 不立围栏、不做安全备份，库与整理文件原样。
func TestDataCareRestoreRefusesInvalidFilesPackBeforeTouchingAnything(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, packPath string)
	}{
		{"missing", func(t *testing.T, packPath string) { _ = os.Remove(packPath) }},
		{"content changed", func(t *testing.T, packPath string) {
			pack, _ := os.ReadFile(packPath)
			pack[strings.LastIndex(string(pack), "wallpaper")] = 'W'
			_ = os.WriteFile(packPath, pack, 0600)
		}},
		{"truncated", func(t *testing.T, packPath string) { _ = os.Truncate(packPath, 1024) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, dataDir, directory := setupCuratedBackupTest(t)
			backup, err := service.CreateBackup(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			tc.damage(t, filepath.Join(directory, backupFilesName(backup.Name)))
			added := filepath.Join(dataDir, "media-details/people/9/new.jpg")
			writeDataCareFile(t, added, "added after backup")
			err, fenced := restoreDataCareBackup(t, service, *backup, nil)
			if err == nil || !strings.HasPrefix(err.Error(), BackupReasonFilesInvalid+":") || DatabaseRestoreRequiresRestart(err) || strings.Contains(err.Error(), dataDir) {
				t.Fatalf("应以 backup_files_invalid 拒绝且不带路径: %v", err)
			}
			if fenced || countSQLiteSnapshots(t, directory) != 1 || dataCareFileBody(t, added) != "added after backup" {
				t.Fatalf("拒绝时不应进入维护、做安全备份或动整理文件: fenced=%v", fenced)
			}
		})
	}
}

// 4.2（审阅 I1）：解包落点放不下全部条目时，恢复在进入维护之前以 backup_insufficient_space 拒绝。
func TestDataCareRestoreRefusesWhenFilesCannotBeUnpacked(t *testing.T) {
	service, dataDir, directory := setupCuratedBackupTest(t)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	service.diskFree = func(path string) (uint64, error) { return 4, nil }
	err, fenced := restoreDataCareBackup(t, service, *backup, nil)
	if err == nil || !strings.HasPrefix(err.Error(), BackupReasonInsufficientSpace+":") || !strings.Contains(err.Error(), "整理过的文件") || strings.Contains(err.Error(), dataDir) {
		t.Fatalf("应以 backup_insufficient_space 拒绝: %v", err)
	}
	if fenced || countSQLiteSnapshots(t, directory) != 1 {
		t.Fatalf("拒绝时不应进入维护或做安全备份: fenced=%v", fenced)
	}
}

// D-DH-03（审阅 I3）：兼容判定看清单 models（做备份的版本的模型结构），不看库里的实际列。从带遗留列的老库做的备份，
// 拿到全新数据目录上校验与恢复都兼容；清单没有 models 时按兼容处理。
func TestDataCareLeftoverColumnBackupCompatibleOnFreshDataDir(t *testing.T) {
	oldService, _, oldDirectory := setupCuratedBackupTest(t)
	if err := database.DB.Exec("ALTER TABLE videos ADD COLUMN fanza_leftover TEXT").Error; err != nil {
		t.Fatal(err)
	}
	backup, err := oldService.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manifest := readDataCareManifest(t, oldDirectory, backup.Name)
	if !containsString(manifest.Schema.Tables["videos"], "fanza_leftover") || containsString(manifest.Models["videos"], "fanza_leftover") {
		t.Fatal("遗留列只进 schema.tables，不进 models")
	}
	freshService, _, freshDirectory := setupCuratedBackupTest(t)
	if err := os.MkdirAll(freshDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{backup.Name, backupFilesName(backup.Name), backupManifestName(backup.Name)} {
		data, _ := os.ReadFile(filepath.Join(oldDirectory, name))
		if err := os.WriteFile(filepath.Join(freshDirectory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := freshService.VerifyDatabaseBackup(context.Background(), backup.Name); err != nil || !result.OK || !result.SchemaCompatible {
		t.Fatalf("全新数据目录上应兼容: %+v err=%v", result, err)
	}
	manifest.Models = nil
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(freshDirectory, backupManifestName(backup.Name)), data, 0600); err != nil {
		t.Fatal(err)
	}
	if result, _ := freshService.VerifyDatabaseBackup(context.Background(), backup.Name); !result.SchemaCompatible {
		t.Fatalf("没有 models 的清单按兼容处理: %+v", result)
	}
	if err, _ := restoreDataCareBackup(t, freshService, *backup, nil); err != nil {
		t.Fatalf("全新数据目录上应能恢复: %v", err)
	}
}

// 4.2（审阅 I2b）：暂存目录改名就位失败时，刚改走的原目录要改回来，正在用的目录不能消失。
func TestDataCareFilesSwapRenamesOriginalBackOnFailure(t *testing.T) {
	service, dataDir, _ := setupCuratedBackupTest(t)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	added := filepath.Join(dataDir, "media-details/people/9/new.jpg")
	writeDataCareFile(t, added, "added after backup")
	service.rename = func(from, to string) error {
		if strings.HasPrefix(filepath.Base(from), backupFilesStagingPrefix+"people-") {
			return errors.New("injected rename failure")
		}
		return os.Rename(from, to)
	}
	err, _ = restoreDataCareBackup(t, service, *backup, nil)
	if !DatabaseRestoreRequiresRestart(err) || !strings.Contains(err.Error(), "整理过的文件未能还原") {
		t.Fatalf("应报文件未能还原: %v", err)
	}
	if dataCareFileBody(t, added) != "added after backup" || dataCareFileBody(t, filepath.Join(dataDir, "media-details/people/1/avatar.jpg")) != "avatar" {
		t.Fatal("就位失败时原目录应改回原名、内容不变")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dataDir, "media-details", "people.pre-restore-*")); len(leftovers) != 0 {
		t.Fatalf("改回之后不应留下 people 的旧目录: %v", leftovers)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dataDir, "media-details", backupFilesStagingPrefix+"*")); len(leftovers) != 0 {
		t.Fatalf("暂存目录应删除: %v", leftovers)
	}
}

// newRecoveryFixture 建一个只有数据目录的备份服务，返回它与 media-details 的真实路径（已解析符号链接）。
func newRecoveryFixture(t *testing.T) (*BackupService, *stubDesktopNotifier, string, string) {
	t.Helper()
	setupBackupServiceTest(t, 7, 24) // 只为 GetStatus 有设置可读
	dataDir := t.TempDir()
	service := NewBackupService(dataDir)
	notifier := &stubDesktopNotifier{}
	service.SetDesktopNotifier(notifier)
	if err := os.MkdirAll(filepath.Join(dataDir, "media-details"), 0755); err != nil {
		t.Fatal(err)
	}
	mediaDetails, _ := filepath.EvalSymlinks(filepath.Join(dataDir, "media-details"))
	return service, notifier, dataDir, mediaDetails
}

// 4.2（审阅 R3 / I-2 / M1）：swapping 状态的标记——只按标记里记的名字改回（不按时间猜），前缀对不上的条目一律不动；
// 记下的暂存目录清掉、标记删掉；改回过目录时发一次桌面通知，备份状态带上说明。没有标记时什么也不动。
func TestDataCareRecoverInterruptedFilesRestore(t *testing.T) {
	service, notifier, dataDir, mediaDetails := newRecoveryFixture(t)
	people := filepath.Join(dataDir, "media-details/people")
	recorded, newer := "people.pre-restore-20261011-090000.000000000", "people.pre-restore-20261012-090000.000000000"
	writeDataCareFile(t, filepath.Join(mediaDetails, recorded, "1/a.jpg"), "recorded")
	writeDataCareFile(t, filepath.Join(mediaDetails, newer, "1/a.jpg"), "newer but not recorded")
	staging := backupFilesStagingPrefix + "people-20261011-090000.000000000"
	writeDataCareFile(t, filepath.Join(mediaDetails, staging, "x"), "staged")
	writeDataCareFile(t, filepath.Join(mediaDetails, "other.pre-restore-1", "x"), "not ours")
	writeDataCareFile(t, filepath.Join(mediaDetails, "keepme", "x"), "not a staging directory")
	resolvedData, _ := filepath.EvalSymlinks(dataDir)
	writeDataCareFile(t, filepath.Join(resolvedData, "wallpapers/current.jpg"), "current")

	service.RecoverInterruptedFilesRestore()
	if _, err := os.Stat(people); !os.IsNotExist(err) || len(notifier.Notifications()) != 0 {
		t.Fatalf("没有标记时不应动任何目录: err=%v", err)
	}
	if err := service.writeFilesRestoreMarker(filesRestoreMarker{State: filesRestoreSwapping, Stamp: "20261011-090000.000000000", Targets: []filesRestoreTarget{
		{Dir: "media-details/people", Parent: mediaDetails, Base: "people", Aside: recorded, Staging: staging},
		{Dir: "wallpapers", Parent: resolvedData, Base: "wallpapers", Aside: "wallpapers.pre-restore-20261011-090000.000000000", Staging: backupFilesStagingPrefix + "wallpapers-x"},
		{Dir: "media-details/watchlist", Parent: mediaDetails, Base: "watchlist", Aside: "other.pre-restore-1", Staging: backupFilesStagingPrefix + "watchlist-x"},
		{Dir: "media-details/collections", Parent: mediaDetails, Base: "collections", Aside: "collections.pre-restore-1", Staging: "keepme"},
		{Dir: "../escape", Parent: mediaDetails, Base: "people", Aside: recorded, Staging: staging},
	}}); err != nil {
		t.Fatal(err)
	}
	service.RecoverInterruptedFilesRestore()

	if dataCareFileBody(t, filepath.Join(people, "1/a.jpg")) != "recorded" {
		t.Fatal("缺失的目录应由标记里记下的旧目录改回")
	}
	if dataCareFileBody(t, filepath.Join(mediaDetails, newer, "1/a.jpg")) != "newer but not recorded" {
		t.Fatal("标记里没记的旧目录不动")
	}
	if dataCareFileBody(t, filepath.Join(mediaDetails, "other.pre-restore-1", "x")) != "not ours" ||
		dataCareFileBody(t, filepath.Join(mediaDetails, "keepme", "x")) != "not a staging directory" {
		t.Fatal("前缀对不上的旧目录名、暂存目录名一律不动")
	}
	if dataCareFileBody(t, filepath.Join(resolvedData, "wallpapers/current.jpg")) != "current" {
		t.Fatal("目录还在的不应被改动")
	}
	if _, err := os.Stat(filepath.Join(mediaDetails, staging)); !os.IsNotExist(err) {
		t.Fatalf("记下的暂存目录应清掉: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, backupFilesRestoreMarker)); !os.IsNotExist(err) {
		t.Fatalf("处理完应删掉标记: %v", err)
	}
	if notes := notifier.Notifications(); len(notes) != 1 || !strings.Contains(notes[0].Body, "1 个目录") {
		t.Fatalf("应发一次桌面通知: %+v", notes)
	}
	if status := service.GetStatus(); !strings.Contains(status.Notice, "中途中断") {
		t.Fatalf("备份状态应带上说明: %+v", status)
	}
}

// 4.2（审阅 I-2）：解包阶段（staging）崩溃——只删记下的暂存目录，原目录保持原样，删掉标记并说明一次；
// 没有任何标记引用的暂存目录（前缀对得上的）也会被清掉。
func TestDataCareRecoverFromStagingStateKeepsOriginals(t *testing.T) {
	service, notifier, _, mediaDetails := newRecoveryFixture(t)
	writeDataCareFile(t, filepath.Join(mediaDetails, "people/1/a.jpg"), "original")
	staging := backupFilesStagingPrefix + "people-20261012-090000.000000000"
	writeDataCareFile(t, filepath.Join(mediaDetails, staging, "1/a.jpg"), "half unpacked")
	orphan := backupFilesStagingPrefix + "collections-20261001-000000.000000000"
	writeDataCareFile(t, filepath.Join(mediaDetails, orphan, "x"), "orphan staging")
	// 解包阶段还没开始改名：即使 collections 不见了、旁边又恰好有同名旧目录，也不该去改名。
	collectionsAside := "collections.pre-restore-20261012-090000.000000000"
	writeDataCareFile(t, filepath.Join(mediaDetails, collectionsAside, "x"), "not to be renamed")
	if err := service.writeFilesRestoreMarker(filesRestoreMarker{State: filesRestoreStaging, Stamp: "20261012-090000.000000000", Targets: []filesRestoreTarget{
		{Dir: "media-details/people", Parent: mediaDetails, Base: "people", Aside: "people.pre-restore-20261012-090000.000000000", Staging: staging},
		{Dir: "media-details/collections", Parent: mediaDetails, Base: "collections", Aside: collectionsAside, Staging: backupFilesStagingPrefix + "collections-20261012-090000.000000000"},
	}}); err != nil {
		t.Fatal(err)
	}
	service.RecoverInterruptedFilesRestore()
	if dataCareFileBody(t, filepath.Join(mediaDetails, "people/1/a.jpg")) != "original" {
		t.Fatal("解包阶段中断时原目录保持原样")
	}
	if _, err := os.Stat(filepath.Join(mediaDetails, "collections")); !os.IsNotExist(err) || dataCareFileBody(t, filepath.Join(mediaDetails, collectionsAside, "x")) != "not to be renamed" {
		t.Fatal("staging 状态下不改名")
	}
	for _, name := range []string{staging, orphan} {
		if _, err := os.Stat(filepath.Join(mediaDetails, name)); !os.IsNotExist(err) {
			t.Fatalf("暂存目录 %s 应清掉: %v", name, err)
		}
	}
	if service.filesRestoreMarkerExists() {
		t.Fatal("处理完应删掉标记")
	}
	if notes := notifier.Notifications(); len(notes) != 1 || !strings.Contains(notes[0].Body, "保持原样") {
		t.Fatalf("应说明一次整理文件保持原样: %+v", notes)
	}
}

// 审阅 M2：标记里有目录改不回（标记留着）时，新的恢复在进入维护之前以 backup_restore_recovery_pending 拒绝，
// 不覆盖标记；被引用的暂存目录不被清扫。
func TestDataCareRestoreRefusedWhileRecoveryPending(t *testing.T) {
	service, dataDir, directory := setupCuratedBackupTest(t)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mediaDetails, _ := filepath.EvalSymlinks(filepath.Join(dataDir, "media-details"))
	_ = os.RemoveAll(filepath.Join(mediaDetails, "people"))
	aside := "people.pre-restore-20261012-090000.000000000"
	staging := backupFilesStagingPrefix + "people-20261012-090000.000000000"
	writeDataCareFile(t, filepath.Join(mediaDetails, aside, "1/a.jpg"), "before")
	writeDataCareFile(t, filepath.Join(mediaDetails, staging, "x"), "staged")
	orphan := filepath.Join(mediaDetails, backupFilesStagingPrefix+"collections-20261001-000000.000000000")
	writeDataCareFile(t, filepath.Join(orphan, "x"), "orphan staging")
	marker := filesRestoreMarker{State: filesRestoreSwapping, Stamp: "20261012-090000.000000000", Targets: []filesRestoreTarget{
		{Dir: "media-details/people", Parent: mediaDetails, Base: "people", Aside: aside, Staging: staging},
	}}
	if err := service.writeFilesRestoreMarker(marker); err != nil {
		t.Fatal(err)
	}
	service.rename = func(from, to string) error { return errors.New("still locked") }
	before, _ := os.ReadFile(filepath.Join(dataDir, backupFilesRestoreMarker))
	err, fenced := restoreDataCareBackup(t, service, *backup, nil)
	if err == nil || !strings.HasPrefix(err.Error(), BackupReasonRestoreRecoveryPending+":") || fenced || countSQLiteSnapshots(t, directory) != 1 {
		t.Fatalf("应以 backup_restore_recovery_pending 拒绝: err=%v fenced=%v", err, fenced)
	}
	if after, _ := os.ReadFile(filepath.Join(dataDir, backupFilesRestoreMarker)); string(after) != string(before) {
		t.Fatal("拒绝时不应覆盖标记")
	}
	if _, err := os.Stat(filepath.Join(mediaDetails, aside)); err != nil {
		t.Fatalf("改不回的旧目录应原样留着: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mediaDetails, staging)); err == nil {
		t.Fatal("swapping 状态下记下的暂存目录照常清掉（它不是恢复所需的）")
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatal("标记还在时现场一概不清扫")
	}
	service.rename = nil
	service.RecoverInterruptedFilesRestore()
	if dataCareFileBody(t, filepath.Join(mediaDetails, "people/1/a.jpg")) != "before" || service.filesRestoreMarkerExists() {
		t.Fatal("能改名之后应改回并删掉标记")
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("收拾完之后散落的暂存目录清掉")
	}
}

// 审阅 M6：新的出错来源（遍历收录目录、读文件）进 backup_last_error 与界面文案时不带绝对路径。
func TestDataCareBackupErrorsCarryNoAbsolutePaths(t *testing.T) {
	service, dataDir, _ := setupCuratedBackupTest(t)
	locked := filepath.Join(dataDir, "media-details/people/1")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0755) })
	_, err := service.CreateBackup(context.Background())
	if err == nil || strings.Contains(err.Error(), dataDir) {
		t.Fatalf("读不了的收录目录应让备份失败，且错误不带路径: %v", err)
	}
	settings, _ := (&SettingsService{}).GetSettings()
	if settings.BackupLastError == "" || strings.Contains(settings.BackupLastError, dataDir) {
		t.Fatalf("backup_last_error 不应带路径: %q", settings.BackupLastError)
	}
}

// 审阅 M8：Postgres 恢复后重连失败时文件包还原排在后面、这次没做——提示里要说整理过的文件未能还原。
func TestDataCarePostgresReconnectFailureMentionsUnrestoredFiles(t *testing.T) {
	service, _, _ := setupBackupServiceTest(t, 7, 24)
	clock := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = service.RestoreBackupWithLifecycle(context.Background(),
		BackupRestoreRequest{Name: backup.Name, Size: backup.Size, Fingerprint: backup.Fingerprint},
		nil, func() error { return errors.New("connection refused") })
	if !DatabaseRestoreRequiresRestart(err) || !strings.HasPrefix(err.Error(), DatabaseRestoreReasonCommitted) || !strings.Contains(err.Error(), "整理过的文件未能还原") {
		t.Fatalf("重连失败应说明整理文件未还原: %v", err)
	}
}

// 文件包里的恶意条目（越界路径、绝对路径、符号链接、清单之外的条目、不在收录目录下的条目）一律拒绝，
// 也不会写到暂存目录之外——哪怕清单本身也被改成列着它们。
func TestDataCareFilesPackRejectsMaliciousEntries(t *testing.T) {
	for _, name := range []string{"../escape.txt", "/abs/escape.txt", "media-details/people/../../escape.txt", "thumbnails/1.jpg",
		"media-details/people", "media-details/people\\x.jpg", "media-details/people/./a.jpg", ""} {
		if validBackupPackPath(name) {
			t.Fatalf("不应接受包内路径 %q", name)
		}
	}
	if !validBackupPackPath("media-details/people/1/a.jpg") || !validBackupPackPath("subtitle-backups/3/x.srt") {
		t.Fatal("收录目录下的规整相对路径应接受")
	}
	for _, tc := range []struct {
		name     string
		typeflag byte
		listed   bool
	}{
		{"../escape.txt", tar.TypeReg, true},
		{"/tmp/cineinsight-escape.txt", tar.TypeReg, true},
		{"media-details/people/../../escape.txt", tar.TypeReg, true},
		{"media-details/people/link.jpg", tar.TypeSymlink, true},
		{"media-details/people/1/extra.jpg", tar.TypeReg, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			packPath := filepath.Join(dir, "pack.tar")
			files := writeCraftedPack(t, packPath, tc.name, tc.typeflag, tc.listed)
			staging := filepath.Join(dir, "staging", "root")
			_, entryErr, ioErr := readBackupFilesPack(packPath, files, func(name string) string {
				return filepath.Join(staging, filepath.FromSlash(name))
			})
			if ioErr != nil || entryErr == nil {
				t.Fatalf("应拒绝条目 %q: entryErr=%v ioErr=%v", tc.name, entryErr, ioErr)
			}
			for _, escaped := range []string{filepath.Join(dir, "escape.txt"), filepath.Join(dir, "staging", "escape.txt"), "/tmp/cineinsight-escape.txt"} {
				if _, err := os.Lstat(escaped); err == nil {
					t.Fatalf("不应写出 %s", escaped)
				}
			}
		})
	}
}

// writeCraftedPack 写一个只含一个条目的文件包；listed 时清单也列着它（大小与哈希都对）。
func writeCraftedPack(t *testing.T, packPath, name string, typeflag byte, listed bool) BackupManifestFiles {
	t.Helper()
	file, err := os.Create(packPath)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	body := []byte("payload")
	header := &tar.Header{Name: name, Typeflag: typeflag, Mode: 0644, Size: int64(len(body))}
	if typeflag == tar.TypeSymlink {
		header.Linkname, header.Size, body = "/etc/passwd", 0, nil
	}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	files := BackupManifestFiles{Name: "pack.tar", Entries: []BackupManifestEntry{}}
	if listed {
		files.Entries = append(files.Entries, BackupManifestEntry{Path: name, Size: int64(len(body)), SHA256: fmt.Sprintf("%x", sha256.Sum256(body))})
	} else {
		files.Entries = append(files.Entries, BackupManifestEntry{Path: "media-details/people/1/expected.jpg", Size: 1, SHA256: "x"})
	}
	return files
}

// fakeVolumes 把路径（解析过符号链接）按前缀分到假的「卷」上，每个卷有自己的剩余空间。
type fakeVolumes struct {
	prefixes map[string]string // 解析后的目录前缀 → 卷名
	free     map[string]uint64
}

func (v *fakeVolumes) volumeOf(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	best, volume := "", "A"
	for prefix, name := range v.prefixes {
		if strings.HasPrefix(resolved+"/", prefix+"/") && len(prefix) > len(best) {
			best, volume = prefix, name
		}
	}
	return volume, nil
}

func (v *fakeVolumes) diskFree(path string) (uint64, error) {
	volume, err := v.volumeOf(path)
	if err != nil {
		return 0, err
	}
	return v.free[volume], nil
}

// setupTwoVolumeRestore：media-details 是指向另一块「盘」B 的符号链接，其余（数据目录、备份目录）在 A 上。
func setupTwoVolumeRestore(t *testing.T, removeCurated bool) (*BackupService, *BackupFile, *fakeVolumes) {
	t.Helper()
	service, dataDir, _ := setupCuratedBackupTest(t)
	other := t.TempDir()
	if removeCurated {
		for _, dir := range []string{"media-details", "wallpapers", "subtitle-backups"} {
			_ = os.RemoveAll(filepath.Join(dataDir, dir))
		}
		_ = os.MkdirAll(filepath.Join(other, "media-details"), 0755)
	} else if err := os.Rename(filepath.Join(dataDir, "media-details"), filepath.Join(other, "media-details")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "media-details"), filepath.Join(dataDir, "media-details")); err != nil {
		t.Fatal(err)
	}
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resolvedData, _ := filepath.EvalSymlinks(dataDir)
	resolvedOther, _ := filepath.EvalSymlinks(other)
	volumes := &fakeVolumes{prefixes: map[string]string{resolvedData: "A", resolvedOther: "B"}, free: map[string]uint64{"A": 1 << 40, "B": 1 << 40}}
	service.volumeOf, service.diskFree = volumes.volumeOf, volumes.diskFree
	service.blockSizeOf = func(string) int64 { return restoreSpaceBlock }
	return service, backup, volumes
}

// twoVolumeUnpackBlocks：B 盘上四个 media-details 收录目录各一个暂存目录，people/1 与 collections/2 两个子目录，
// 头像与封面两个文件——共 8 块。
const twoVolumeUnpackBlocks = 8

// 4.2（审阅 R1 / M4）：恢复前按卷合计空间——B 盘（链接过去的 media-details）要放下解出的文件与目录（各按那块盘的
// 分配块取整），A 盘要放下临时库 + 临时副本 + 安全备份 + 其余条目之和；任何一块不够就在进入维护之前拒绝。
func TestDataCareRestoreSpaceIsCheckedPerVolume(t *testing.T) {
	refused := func(t *testing.T, err error, fenced bool) {
		t.Helper()
		if err == nil || !strings.HasPrefix(err.Error(), BackupReasonInsufficientSpace+":") || fenced || DatabaseRestoreRequiresRestart(err) {
			t.Fatalf("应在进入维护前以 backup_insufficient_space 拒绝: err=%v fenced=%v", err, fenced)
		}
	}
	t.Run("other volume one byte short", func(t *testing.T) {
		service, backup, volumes := setupTwoVolumeRestore(t, false)
		volumes.free["B"] = twoVolumeUnpackBlocks*restoreSpaceBlock - 1
		err, fenced := restoreDataCareBackup(t, service, *backup, nil)
		refused(t, err, fenced)
	})
	t.Run("other volume exactly enough", func(t *testing.T) {
		service, backup, volumes := setupTwoVolumeRestore(t, false)
		volumes.free["B"] = twoVolumeUnpackBlocks * restoreSpaceBlock
		if err, _ := restoreDataCareBackup(t, service, *backup, nil); err != nil {
			t.Fatalf("B 盘刚好够时应能恢复: %v", err)
		}
	})
	t.Run("rounding follows the volume's allocation block", func(t *testing.T) {
		service, backup, volumes := setupTwoVolumeRestore(t, false)
		volumes.free["B"] = twoVolumeUnpackBlocks * restoreSpaceBlock // 按 4 KiB 够，按 64 KiB 不够
		service.blockSizeOf = func(path string) int64 {
			if volume, _ := volumes.volumeOf(path); volume == "B" {
				return 64 << 10
			}
			return restoreSpaceBlock
		}
		err, fenced := restoreDataCareBackup(t, service, *backup, nil)
		refused(t, err, fenced)
	})
	t.Run("same volume needs are summed", func(t *testing.T) {
		service, backup, volumes := setupTwoVolumeRestore(t, false)
		snapshot := uint64(roundUpToBlock(backup.Size, restoreSpaceBlock))
		// 临时库、临时副本、壁纸与字幕备份各自都放得下，加上安全备份就放不下。
		volumes.free["A"] = 2*snapshot + 2*restoreSpaceBlock
		err, fenced := restoreDataCareBackup(t, service, *backup, nil)
		refused(t, err, fenced)
	})
	t.Run("zero-entry pack still checks the database side", func(t *testing.T) {
		service, backup, volumes := setupTwoVolumeRestore(t, true)
		if backup.FileCount != 0 {
			t.Fatalf("前置：文件包应为空: %+v", backup)
		}
		volumes.free["A"] = uint64(backup.Size)
		err, fenced := restoreDataCareBackup(t, service, *backup, nil)
		refused(t, err, fenced)
	})
}

// D-DH-01（审阅 R2a）：收录目录存在却进不去（失效的符号链接）时不能当空目录打包：清单记进 unavailable_roots、
// 不出条目，备份状态与列表带提示；media-details 本身失效时其下四个目录都算不可用。
func TestDataCareBackupRecordsUnavailableRoots(t *testing.T) {
	service, dataDir, directory := setupCuratedBackupTest(t)
	if err := os.Symlink(filepath.Join(dataDir, "unplugged"), filepath.Join(dataDir, "media-details/videos")); err != nil {
		t.Fatal(err)
	}
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manifest := readDataCareManifest(t, directory, backup.Name)
	if strings.Join(manifest.UnavailableRoots, ",") != "media-details/videos" || strings.Join(backup.UnavailableRoots, ",") != "media-details/videos" {
		t.Fatalf("失效链接的收录目录应记为不可用: %v / %v", manifest.UnavailableRoots, backup.UnavailableRoots)
	}
	if status := service.GetStatus(); !strings.Contains(status.FilesWarning, "media-details/videos") || strings.Contains(status.FilesWarning, dataDir) {
		t.Fatalf("备份状态应提示哪个目录没备份进去（不带绝对路径）: %q", status.FilesWarning)
	}
	if listed, _ := service.ListBackups(); len(listed) != 1 || strings.Join(listed[0].UnavailableRoots, ",") != "media-details/videos" {
		t.Fatalf("列表应带上不可用的目录: %+v", listed)
	}

	other := t.TempDir()
	if err := os.Rename(filepath.Join(dataDir, "media-details"), filepath.Join(other, "media-details")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "gone"), filepath.Join(dataDir, "media-details")); err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(second.UnavailableRoots, ","); got != "media-details/people,media-details/collections,media-details/videos,media-details/watchlist" {
		t.Fatalf("media-details 失效时其下四个目录都不可用: %s", got)
	}
	if service.GetStatus().FilesWarning == "" {
		t.Fatal("仍有不可用目录时提示保留")
	}
}

// 4.2（审阅 R2b）：备份时不可用的目录恢复时原样不动；文件包要还原的目录现在不可用（失效链接）时，
// 在进入维护之前以 backup_files_target_unavailable 拒绝。
func TestDataCareRestoreRespectsUnavailableRoots(t *testing.T) {
	t.Run("roots unavailable at backup time are left alone", func(t *testing.T) {
		service, dataDir, _ := setupCuratedBackupTest(t)
		videos := filepath.Join(dataDir, "media-details/videos")
		if err := os.Symlink(filepath.Join(dataDir, "unplugged"), videos); err != nil {
			t.Fatal(err)
		}
		backup, err := service.CreateBackup(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(videos)
		writeDataCareFile(t, filepath.Join(videos, "5/poster.jpg"), "current poster")
		_ = os.Remove(filepath.Join(dataDir, "media-details/people/1/avatar.jpg"))
		if err, _ := restoreDataCareBackup(t, service, *backup, nil); err != nil {
			t.Fatalf("恢复应成功: %v", err)
		}
		if dataCareFileBody(t, filepath.Join(videos, "5/poster.jpg")) != "current poster" {
			t.Fatal("备份时不可用的目录恢复时不动")
		}
		if dataCareFileBody(t, filepath.Join(dataDir, "media-details/people/1/avatar.jpg")) != "avatar" {
			t.Fatal("其余目录照常还原")
		}
	})
	t.Run("target unavailable now refuses", func(t *testing.T) {
		service, dataDir, directory := setupCuratedBackupTest(t)
		backup, err := service.CreateBackup(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		people := filepath.Join(dataDir, "media-details/people")
		if err := os.RemoveAll(people); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dataDir, "unplugged"), people); err != nil {
			t.Fatal(err)
		}
		err, fenced := restoreDataCareBackup(t, service, *backup, nil)
		if err == nil || !strings.HasPrefix(err.Error(), BackupReasonFilesTargetUnavailable+":") || fenced || strings.Contains(err.Error(), dataDir) {
			t.Fatalf("应以 backup_files_target_unavailable 拒绝: err=%v fenced=%v", err, fenced)
		}
		if info, err := os.Lstat(people); err != nil || info.Mode()&os.ModeSymlink == 0 || countSQLiteSnapshots(t, directory) != 1 {
			t.Fatal("拒绝时不应动那条链接，也不做安全备份")
		}
	})
}

// 4.2（审阅 R6 / R3）：后面的目录（wallpapers）就位失败时，它自己改回原样，前面已经换好的目录保留各自的
// .pre-restore-*；改名期间进行中标记在、记着每个目录的旧目录名，失败改回之后标记删掉。
func TestDataCareFilesSwapLaterFolderFailureKeepsEarlierPreRestore(t *testing.T) {
	service, dataDir, _ := setupCuratedBackupTest(t)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeDataCareFile(t, filepath.Join(dataDir, "wallpapers/w.jpg"), "current wallpaper")
	var markerDuringSwap filesRestoreMarker
	service.rename = func(from, to string) error {
		if strings.HasPrefix(filepath.Base(from), backupFilesStagingPrefix+"people-") {
			data, err := os.ReadFile(filepath.Join(dataDir, backupFilesRestoreMarker))
			if err != nil || json.Unmarshal(data, &markerDuringSwap) != nil {
				t.Errorf("改名开始前应已写下进行中标记: %v", err)
			}
		}
		if strings.HasPrefix(filepath.Base(from), backupFilesStagingPrefix+"wallpapers-") {
			return errors.New("injected rename failure")
		}
		return os.Rename(from, to)
	}
	err, _ = restoreDataCareBackup(t, service, *backup, nil)
	if !DatabaseRestoreRequiresRestart(err) || !strings.Contains(err.Error(), "整理过的文件未能还原") {
		t.Fatalf("应报文件未能还原: %v", err)
	}
	if dataCareFileBody(t, filepath.Join(dataDir, "wallpapers/w.jpg")) != "current wallpaper" {
		t.Fatal("就位失败的目录应改回原样")
	}
	for _, dir := range []string{"people", "collections"} {
		if asides, _ := filepath.Glob(filepath.Join(dataDir, "media-details", dir+".pre-restore-*")); len(asides) != 1 {
			t.Fatalf("已经换好的 %s 应保留它的 .pre-restore-*: %v", dir, asides)
		}
	}
	if dataCareFileBody(t, filepath.Join(dataDir, "media-details/people/1/avatar.jpg")) != "avatar" {
		t.Fatal("已经换好的目录是备份里的内容")
	}
	if asides, _ := filepath.Glob(filepath.Join(dataDir, "wallpapers.pre-restore-*")); len(asides) != 0 {
		t.Fatalf("改回之后不应留下 wallpapers 的旧目录: %v", asides)
	}
	if len(markerDuringSwap.Targets) != 6 || markerDuringSwap.Targets[0].Dir != "media-details/people" || markerDuringSwap.Targets[0].Aside == "" {
		t.Fatalf("标记应记下每个目录与它的旧目录名: %+v", markerDuringSwap)
	}
	if _, err := os.Stat(filepath.Join(dataDir, backupFilesRestoreMarker)); !os.IsNotExist(err) {
		t.Fatalf("失败改回之后应删掉标记: %v", err)
	}
}

// 4.2（审阅 I-1）：收录目录是指向别处（另一块盘、名字也不同）的有效符号链接时，还原动的是链接指向的真实目录：
// 链接原样保留、文件落进它指向的目录；改名期间旧内容在真实目录同级的 .pre-restore-* 里，暂存目录也在那里。
func TestDataCareRestoreKeepsSymlinkedCuratedRoot(t *testing.T) {
	service, dataDir, _ := setupCuratedBackupTest(t)
	external := t.TempDir()
	store := filepath.Join(external, "videos-store")
	writeDataCareFile(t, filepath.Join(store, "5/poster.jpg"), "backed up poster")
	link := filepath.Join(dataDir, "media-details/videos")
	if err := os.Symlink(store, link); err != nil {
		t.Fatal(err)
	}
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeDataCareFile(t, filepath.Join(store, "5/poster.jpg"), "changed after backup")
	resolvedExternal, _ := filepath.EvalSymlinks(external)
	resolvedData, _ := filepath.EvalSymlinks(dataDir)
	// 空间记在链接指向的那块盘（C）上：C 放不下时在进入维护之前拒绝。
	volumes := &fakeVolumes{prefixes: map[string]string{resolvedData: "A", resolvedExternal: "C"}, free: map[string]uint64{"A": 1 << 40, "C": 0}}
	service.volumeOf, service.diskFree = volumes.volumeOf, volumes.diskFree
	if err, fenced := restoreDataCareBackup(t, service, *backup, nil); err == nil || !strings.HasPrefix(err.Error(), BackupReasonInsufficientSpace+":") || fenced {
		t.Fatalf("链接指向的盘放不下时应拒绝: err=%v fenced=%v", err, fenced)
	}
	volumes.free["C"] = 1 << 40
	sawAside := false
	service.rename = func(from, to string) error {
		if strings.HasPrefix(filepath.Base(from), backupFilesStagingPrefix+"videos-store-") {
			if filepath.Dir(from) != resolvedExternal {
				t.Errorf("暂存目录应建在真实目录的同级: %s", from)
			}
			asides, _ := filepath.Glob(filepath.Join(resolvedExternal, "videos-store.pre-restore-*", "5/poster.jpg"))
			sawAside = len(asides) == 1 && dataCareFileBody(t, asides[0]) == "changed after backup"
		}
		return os.Rename(from, to)
	}
	if err, _ := restoreDataCareBackup(t, service, *backup, nil); err != nil {
		t.Fatalf("恢复应成功: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("符号链接应原样保留: %v", err)
	}
	if target, _ := os.Readlink(link); target != store {
		t.Fatalf("链接仍指向原来的目录: %s", target)
	}
	if dataCareFileBody(t, filepath.Join(store, "5/poster.jpg")) != "backed up poster" {
		t.Fatal("文件应还原进链接指向的目录")
	}
	if !sawAside {
		t.Fatal("改名期间旧内容应在真实目录同级的 .pre-restore-* 里")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(resolvedExternal, "videos-store.pre-restore-*")); len(leftovers) != 0 {
		t.Fatalf("成功后旧目录应删除: %v", leftovers)
	}
}

// 审阅 M6：改名前再查一次——某个收录目录在还原过程中变得不可用时，不动它，这次还原记为不完整，其余目录照常换好。
func TestDataCareFilesSwapSkipsRootThatBecameUnavailable(t *testing.T) {
	service, dataDir, _ := setupCuratedBackupTest(t)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wallpapers := filepath.Join(dataDir, "wallpapers")
	swapped := false
	service.rename = func(from, to string) error {
		if !swapped {
			swapped = true // 第一次改名时把 wallpapers 换成一条失效的链接（盘被拔了）
			_ = os.RemoveAll(wallpapers)
			_ = os.Symlink(filepath.Join(dataDir, "unplugged"), wallpapers)
		}
		return os.Rename(from, to)
	}
	_ = os.Remove(filepath.Join(dataDir, "media-details/people/1/avatar.jpg"))
	err, _ = restoreDataCareBackup(t, service, *backup, nil)
	if !DatabaseRestoreRequiresRestart(err) || !strings.Contains(err.Error(), "整理过的文件未能还原") {
		t.Fatalf("应报整理文件未能（完整）还原: %v", err)
	}
	if info, err := os.Lstat(wallpapers); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("变得不可用的目录不应被动")
	}
	if dataCareFileBody(t, filepath.Join(dataDir, "media-details/people/1/avatar.jpg")) != "avatar" {
		t.Fatal("其余目录照常还原")
	}
	if _, err := os.Stat(filepath.Join(dataDir, backupFilesRestoreMarker)); !os.IsNotExist(err) {
		t.Fatalf("处理完应删掉标记: %v", err)
	}
}

// 审阅 M7：收录目录存在却列不出（没有权限）时记进 unavailable_roots，备份照常完成。
func TestDataCareBackupTreatsUnlistableRootAsUnavailable(t *testing.T) {
	service, dataDir, _ := setupCuratedBackupTest(t)
	people := filepath.Join(dataDir, "media-details/people")
	if err := os.Chmod(people, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(people, 0755) })
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatalf("列不出的收录目录不应让整份备份失败: %v", err)
	}
	if strings.Join(backup.UnavailableRoots, ",") != "media-details/people" {
		t.Fatalf("列不出的收录目录应记为不可用: %v", backup.UnavailableRoots)
	}
}

// 审阅 M3：备份时的空间检查只算还没写的文件包（快照已经在盘上），剩余空间刚好放得下文件包时备份照常完成。
func TestDataCareBackupSpaceCountsOnlyWhatRemains(t *testing.T) {
	service, dataDir, _ := setupCuratedBackupTest(t)
	estimate, err := estimateBackupFilesPack(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	service.diskFree = func(string) (uint64, error) { return uint64(estimate), nil }
	if _, err := service.CreateBackup(context.Background()); err != nil {
		t.Fatalf("剩余空间刚好放得下文件包时应能备份: %v", err)
	}
}

// G-3：设置页显示的「最近失败」落库前擦掉绝对路径。
func TestDataCareBackupLastErrorStoredWithoutPaths(t *testing.T) {
	service, _, _ := setupBackupServiceTest(t, 7, 24)
	if err := service.recordAttempt(false, errors.New("创建备份临时文件失败: open /Users/someone/backups/.tmp: permission denied")); err != nil {
		t.Fatal(err)
	}
	settings, _ := (&SettingsService{}).GetSettings()
	if strings.Contains(settings.BackupLastError, "/Users/someone") || !strings.Contains(settings.BackupLastError, "permission denied") {
		t.Fatalf("backup_last_error 不应带绝对路径: %q", settings.BackupLastError)
	}
}

// 4.2（审阅 I-2）：标记在建暂存目录、解包之前就写下（state=staging）；解包失败时暂存目录与标记都清掉，原目录不动。
func TestDataCareFilesRestoreMarkerWrittenBeforeUnpacking(t *testing.T) {
	service, dataDir, directory := setupCuratedBackupTest(t)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeDataCareFile(t, filepath.Join(dataDir, "wallpapers/w.jpg"), "current wallpaper")
	var seen filesRestoreMarker
	service.beforeUnpack = func() {
		data, err := os.ReadFile(filepath.Join(dataDir, backupFilesRestoreMarker))
		if err != nil || json.Unmarshal(data, &seen) != nil {
			t.Errorf("解包之前应已写下标记: %v", err)
		}
		// 在解包这一步出错（文件包在维护期间被截断）。
		_ = os.Truncate(filepath.Join(directory, backupFilesName(backup.Name)), 1024)
	}
	err, _ = restoreDataCareBackup(t, service, *backup, nil)
	if !DatabaseRestoreRequiresRestart(err) || !strings.Contains(err.Error(), "整理过的文件未能还原") {
		t.Fatalf("应报整理文件未能还原: %v", err)
	}
	if seen.State != filesRestoreStaging || len(seen.Targets) != 6 || seen.Targets[0].Staging == "" {
		t.Fatalf("标记应处于 staging 状态并记下暂存目录: %+v", seen)
	}
	if dataCareFileBody(t, filepath.Join(dataDir, "wallpapers/w.jpg")) != "current wallpaper" {
		t.Fatal("解包失败时原目录不动")
	}
	for _, target := range seen.Targets {
		if _, err := os.Stat(filepath.Join(target.Parent, target.Staging)); !os.IsNotExist(err) {
			t.Fatalf("暂存目录应清掉: %s", target.Staging)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, backupFilesRestoreMarker)); !os.IsNotExist(err) {
		t.Fatalf("原目录没动过，标记应删掉: %v", err)
	}
}
