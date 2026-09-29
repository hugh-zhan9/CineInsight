package services

import (
	"context"
	"errors"
	"fmt"
	"io"
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

type backupRunnerCall struct {
	name string
	args []string
	env  []string
}

type fakeBackupRunner struct {
	missing  map[string]bool
	calls    []backupRunnerCall
	errFor   map[string]error
	failList bool
}

func (runner *fakeBackupRunner) LookPath(name string) (string, error) {
	if runner.missing[name] {
		return "", errors.New("missing")
	}
	return "/usr/bin/" + name, nil
}

func (runner *fakeBackupRunner) Run(_ context.Context, name string, args []string, env []string) error {
	runner.calls = append(runner.calls, backupRunnerCall{name: name, args: append([]string(nil), args...), env: append([]string(nil), env...)})
	if name == "pg_restore" && containsString(args, "--list") && runner.failList {
		return errors.New("invalid archive")
	}
	if err := runner.errFor[name]; err != nil {
		return err
	}
	if name == "pg_dump" {
		for index := range args {
			if args[index] == "--file" && index+1 < len(args) {
				return os.WriteFile(args[index+1], []byte("valid custom dump"), 0600)
			}
		}
	}
	if name == "pg_restore" && !containsString(args, "--list") {
		if len(args) == 0 {
			return errors.New("missing restore path")
		}
		if _, err := os.Stat(args[len(args)-1]); err != nil {
			return err
		}
	}
	return nil
}

func setupBackupServiceTest(t *testing.T, retention, interval int) (*BackupService, *fakeBackupRunner, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "backup.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Settings{}); err != nil {
		t.Fatal(err)
	}
	backupDirectory := filepath.Join(t.TempDir(), "backups")
	settings := models.Settings{
		BackupDirectory:      backupDirectory,
		BackupRetentionCount: retention,
		BackupIntervalHours:  interval,
	}
	if err := db.Create(&settings).Error; err != nil {
		t.Fatal(err)
	}
	previousDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previousDB })
	t.Setenv("PG_HOST", "localhost")
	t.Setenv("PG_PORT", "5432")
	t.Setenv("PG_USER", "cineinsight")
	t.Setenv("PG_PASSWORD", "do-not-leak")
	t.Setenv("PG_DB", "cineinsight_test")
	t.Setenv("PG_SSLMODE", "disable")
	runner := &fakeBackupRunner{missing: map[string]bool{}, errFor: map[string]error{}}
	service := NewBackupService(t.TempDir())
	service.runner = runner
	service.now = func() time.Time { return time.Date(2026, 8, 4, 12, 30, 45, 123, time.UTC) }
	return service, runner, backupDirectory
}

func TestBackupServiceCreatesValidatedDumpWithoutCredentialArgumentsAndRotates(t *testing.T) {
	service, runner, directory := setupBackupServiceTest(t, 2, 24)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	oldNames := []string{"cineinsight-20260801-000000.000000000.dump", "cineinsight-20260802-000000.000000000.dump"}
	for index, name := range oldNames {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Date(2026, 8, 1+index, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	foreignPath := filepath.Join(directory, "cineinsight-user-owned.dump")
	if err := os.WriteFile(foreignPath, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}

	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}
	if backup.Size == 0 {
		t.Fatal("backup should not be empty")
	}
	if len(runner.calls) != 2 || runner.calls[0].name != "pg_dump" || runner.calls[1].name != "pg_restore" {
		t.Fatalf("expected dump then validation, got %#v", runner.calls)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call.args, " "), "do-not-leak") {
			t.Fatal("password leaked into command arguments")
		}
		if !containsString(call.env, "PGPASSWORD=do-not-leak") {
			t.Fatal("password should be passed through the child environment")
		}
	}
	backups, err := service.ListBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 2 || backups[0].Name != backup.Name {
		t.Fatalf("retention should keep newest two backups: %#v", backups)
	}
	if _, err := os.Stat(filepath.Join(directory, oldNames[0])); !os.IsNotExist(err) {
		t.Fatalf("oldest backup should be rotated, stat err=%v", err)
	}
	if _, err := os.Stat(foreignPath); err != nil {
		t.Fatalf("non-CineInsight file must not be rotated: %v", err)
	}
}

func TestBackupServiceRestoreCreatesSafetyBackupBeforeTouchingDatabase(t *testing.T) {
	service, runner, directory := setupBackupServiceTest(t, 1, 24)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	requested := "cineinsight-20260803-120000.000000000.dump"
	if err := os.WriteFile(filepath.Join(directory, requested), []byte("existing dump"), 0600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := hashFile(filepath.Join(directory, requested))
	if err != nil {
		t.Fatal(err)
	}
	request := BackupRestoreRequest{Name: requested, Size: int64(len("existing dump")), Fingerprint: fingerprint}
	beforeCalled := false
	callsAtFence := -1
	reconnectCalled := false
	if err := service.RestoreBackupWithLifecycle(context.Background(), request, func() error {
		beforeCalled = true
		callsAtFence = len(runner.calls)
		return nil
	}, func() error {
		reconnectCalled = true
		return nil
	}); err != nil {
		t.Fatalf("RestoreBackup failed: %v", err)
	}
	if !beforeCalled || !reconnectCalled {
		t.Fatalf("restore lifecycle callbacks missing: before=%v reconnect=%v", beforeCalled, reconnectCalled)
	}
	if callsAtFence != 1 {
		t.Fatalf("write fence must be applied after archive validation but before the safety dump, calls at fence=%d %#v", callsAtFence, runner.calls)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("expected safety dump, its validation, selected validation, restore; got %#v", runner.calls)
	}
	if runner.calls[0].name != "pg_restore" || runner.calls[1].name != "pg_dump" || runner.calls[2].name != "pg_restore" || runner.calls[3].name != "pg_restore" {
		t.Fatalf("unexpected restore call order: %#v", runner.calls)
	}
	if !containsString(runner.calls[3].args, "--single-transaction") || !containsString(runner.calls[3].args, "--clean") {
		t.Fatalf("restore must be atomic and replace backed-up objects: %#v", runner.calls[3].args)
	}
	if got := runner.calls[3].args[len(runner.calls[3].args)-1]; filepath.Base(got) == requested || !strings.HasPrefix(filepath.Base(got), ".cineinsight-restore-") {
		t.Fatalf("restore should use a verified private copy, got %q", got)
	}
}

func TestBackupServiceInvalidArchiveDoesNotCreateOrRotateSafetyBackup(t *testing.T) {
	service, runner, directory := setupBackupServiceTest(t, 1, 24)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	name := "cineinsight-20260803-120000.000000000.dump"
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("not a custom dump"), 0600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	runner.failList = true
	request := BackupRestoreRequest{Name: name, Size: int64(len("not a custom dump")), Fingerprint: fingerprint}
	if err := service.RestoreBackup(context.Background(), request); err == nil {
		t.Fatal("invalid archive should be rejected")
	}
	if len(runner.calls) != 1 || runner.calls[0].name != "pg_restore" || !containsString(runner.calls[0].args, "--list") {
		t.Fatalf("invalid archive must fail before safety backup: %#v", runner.calls)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("selected archive must remain untouched: %v", err)
	}
}

func TestBackupServiceReconnectFailureRequiresRestart(t *testing.T) {
	service, _, directory := setupBackupServiceTest(t, 7, 24)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	name := "cineinsight-20260803-120000.000000000.dump"
	path := filepath.Join(directory, name)
	content := []byte("valid dump")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	request := BackupRestoreRequest{Name: name, Size: int64(len(content)), Fingerprint: fingerprint}
	err = service.RestoreBackupWithLifecycle(context.Background(), request, func() error { return nil }, func() error {
		return errors.New("reconnect failed")
	})
	if err == nil || !DatabaseRestoreRequiresRestart(err) {
		t.Fatalf("reconnect failure after restore must require restart: %v", err)
	}
}

func TestBackupServiceFatalMaintenanceEntryStopsBeforeRestore(t *testing.T) {
	service, runner, directory := setupBackupServiceTest(t, 7, 24)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	name := "cineinsight-20260803-120000.000000000.dump"
	path := filepath.Join(directory, name)
	content := []byte("valid dump")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	request := BackupRestoreRequest{Name: name, Size: int64(len(content)), Fingerprint: fingerprint}
	err = service.RestoreBackupWithLifecycle(context.Background(), request, func() error {
		return &DatabaseRestoreError{Fatal: true, Err: errors.New("close state unknown")}
	}, nil)
	if err == nil || !DatabaseRestoreRequiresRestart(err) {
		t.Fatalf("fatal maintenance entry must require restart: %v", err)
	}
	for _, call := range runner.calls {
		if call.name == "pg_restore" && !containsString(call.args, "--list") {
			t.Fatalf("restore must not run after fatal maintenance entry: %#v", runner.calls)
		}
	}
}

func TestBackupServiceRestoreRejectsUnlistedPathsWithoutRunningCommands(t *testing.T) {
	service, runner, _ := setupBackupServiceTest(t, 7, 24)
	if err := service.RestoreBackup(context.Background(), BackupRestoreRequest{Name: "../outside.dump", Size: 1, Fingerprint: "x"}); err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("no command should run for invalid restore target: %#v", runner.calls)
	}
}

func TestBackupServiceRestoreRejectsBackupChangedAfterConfirmation(t *testing.T) {
	service, runner, directory := setupBackupServiceTest(t, 7, 24)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	name := "cineinsight-20260803-120000.000000000.dump"
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("first payload"), 0600); err != nil {
		t.Fatal(err)
	}
	backups, err := service.ListBackups()
	if err != nil || len(backups) != 1 {
		t.Fatalf("ListBackups got=%#v err=%v", backups, err)
	}
	if err := os.WriteFile(path, []byte("other payload"), 0600); err != nil {
		t.Fatal(err)
	}
	request := BackupRestoreRequest{Name: backups[0].Name, Size: backups[0].Size, Fingerprint: backups[0].Fingerprint}
	if err := service.RestoreBackup(context.Background(), request); err == nil {
		t.Fatal("expected changed backup to be rejected")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("changed backup must be rejected before commands run: %#v", runner.calls)
	}
}

func TestBackupServiceMaybeBackupHonorsIntervalAndDisabledValue(t *testing.T) {
	service, runner, _ := setupBackupServiceTest(t, 7, 24)
	recent := service.now().Add(-time.Hour)
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("backup_last_success_at", &recent).Error; err != nil {
		t.Fatal(err)
	}
	created, err := service.MaybeBackup(context.Background())
	if err != nil || created || len(runner.calls) != 0 {
		t.Fatalf("recent backup should skip: created=%v err=%v calls=%#v", created, err, runner.calls)
	}
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Updates(map[string]any{"backup_interval_hours": 0, "backup_last_success_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	created, err = service.MaybeBackup(context.Background())
	if err != nil || created || len(runner.calls) != 0 {
		t.Fatalf("disabled interval should skip: created=%v err=%v calls=%#v", created, err, runner.calls)
	}
}

func TestBackupStatusReportsMissingPostgresTools(t *testing.T) {
	service, runner, _ := setupBackupServiceTest(t, 7, 24)
	runner.missing["pg_dump"] = true
	status := service.GetStatus()
	if status.Available || status.BackupAvailable || !strings.Contains(status.Reason, "pg_dump") {
		t.Fatalf("unexpected unavailable status: %#v", status)
	}
}

func TestBackupServiceNeverOverwritesTimestampCollision(t *testing.T) {
	service, _, directory := setupBackupServiceTest(t, 7, 24)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	name := backupFilePrefix + service.now().Format("20060102-150405.000000000") + backupFileSuffix
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("existing valid backup"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateBackup(context.Background()); err == nil {
		t.Fatal("timestamp collision must fail instead of overwriting")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "existing valid backup" {
		t.Fatalf("existing backup changed: content=%q err=%v", content, err)
	}
}

func TestBackupServiceMaybeBackupPerformsDueBackup(t *testing.T) {
	service, runner, directory := setupBackupServiceTest(t, 7, 24)
	created, err := service.MaybeBackup(context.Background())
	if err != nil || !created {
		t.Fatalf("due backup should run: created=%v err=%v", created, err)
	}
	if len(runner.calls) != 2 || runner.calls[0].name != "pg_dump" || runner.calls[1].name != "pg_restore" {
		t.Fatalf("expected dump then validation, got %#v", runner.calls)
	}
	backups, err := service.listBackupsIn(directory)
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one backup on disk: %#v err=%v", backups, err)
	}
	settings, err := (&SettingsService{}).GetSettings()
	if err != nil || settings.BackupLastSuccessAt == nil || settings.BackupLastError != "" {
		t.Fatalf("successful backup must record success: %+v err=%v", settings, err)
	}
	created, err = service.MaybeBackup(context.Background())
	if err != nil || created || len(runner.calls) != 2 {
		t.Fatalf("freshly recorded success must skip the next run: created=%v err=%v calls=%#v", created, err, runner.calls)
	}
}

func TestBackupServiceCreateBackupFailureRecordsLastError(t *testing.T) {
	service, runner, _ := setupBackupServiceTest(t, 7, 24)
	runner.errFor["pg_dump"] = errors.New("pg_dump: no space left on device")
	if _, err := service.CreateBackup(context.Background()); err == nil {
		t.Fatal("dump failure must surface an error")
	}
	settings, err := (&SettingsService{}).GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.BackupLastAttemptAt == nil {
		t.Fatal("failed attempt must record backup_last_attempt_at")
	}
	if settings.BackupLastSuccessAt != nil {
		t.Fatalf("failed backup must not record success: %v", settings.BackupLastSuccessAt)
	}
	if !strings.Contains(settings.BackupLastError, "no space left on device") {
		t.Fatalf("failure cause must land in backup_last_error, got %q", settings.BackupLastError)
	}
}

func TestBackupServiceRestoreProtectsRestoreTargetFromRotation(t *testing.T) {
	service, _, directory := setupBackupServiceTest(t, 1, 24)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	oldName := "cineinsight-20260801-000000.000000000.dump"
	requested := "cineinsight-20260803-120000.000000000.dump"
	for name, stamp := range map[string]time.Time{
		oldName:   time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		requested: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
	} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("valid dump"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint, err := hashFile(filepath.Join(directory, requested))
	if err != nil {
		t.Fatal(err)
	}
	request := BackupRestoreRequest{Name: requested, Size: int64(len("valid dump")), Fingerprint: fingerprint}
	if err := service.RestoreBackup(context.Background(), request); err != nil {
		t.Fatalf("RestoreBackup failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, requested)); err != nil {
		t.Fatalf("restore target must survive safety-backup rotation: %v", err)
	}
	safetyName := backupFilePrefix + service.now().Format("20060102-150405.000000000") + backupFileSuffix
	if _, err := os.Stat(filepath.Join(directory, safetyName)); err != nil {
		t.Fatalf("safety backup must be kept by rotation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, oldName)); !os.IsNotExist(err) {
		t.Fatalf("unprotected old backup should be rotated, stat err=%v", err)
	}
}

func TestBackupServiceSweepsStaleTempFiles(t *testing.T) {
	service, _, directory := setupBackupServiceTest(t, 7, 24)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	stale := []string{".cineinsight-backup-111.tmp", ".cineinsight-restore-222.tmp"}
	staleStamp := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range stale {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("leftover"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, staleStamp, staleStamp); err != nil {
			t.Fatal(err)
		}
	}
	freshTemp := filepath.Join(directory, ".cineinsight-backup-333.tmp")
	if err := os.WriteFile(freshTemp, []byte("in flight"), 0600); err != nil {
		t.Fatal(err)
	}
	foreignTemp := filepath.Join(directory, ".cineinsight-other.tmp")
	if err := os.WriteFile(foreignTemp, []byte("not ours"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(foreignTemp, staleStamp, staleStamp); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateBackup(context.Background()); err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}
	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Fatalf("stale temp file %s should be swept, stat err=%v", name, err)
		}
	}
	if _, err := os.Stat(freshTemp); err != nil {
		t.Fatalf("recent temp file must not be swept: %v", err)
	}
	if _, err := os.Stat(foreignTemp); err != nil {
		t.Fatalf("non-matching temp file must not be swept: %v", err)
	}
}

func TestExecPostgresToolRunnerErrorIncludesStderrTail(t *testing.T) {
	err := execPostgresToolRunner{}.Run(context.Background(), "/bin/sh", []string{"-c", "echo tail-marker >&2; exit 3"}, nil)
	if err == nil || !strings.Contains(err.Error(), "tail-marker") {
		t.Fatalf("error must include child stderr: %v", err)
	}
	err = execPostgresToolRunner{}.Run(context.Background(), "/bin/sh", []string{"-c", "i=0; while [ $i -lt 300 ]; do echo 0123456789; i=$((i+1)); done >&2; exit 1"}, nil)
	if err == nil {
		t.Fatal("expected failure with large stderr")
	}
	if len(err.Error()) > stderrTailLimit+200 {
		t.Fatalf("stderr capture must be bounded, got %d bytes", len(err.Error()))
	}
}

func TestBackupSettingNormalizationClampsRetentionAndInterval(t *testing.T) {
	retentionCases := map[int]int{-5: 7, 0: 7, 1: 1, 100: 100, 101: 100}
	for input, expected := range retentionCases {
		if got := normalizedBackupRetention(input); got != expected {
			t.Fatalf("normalizedBackupRetention(%d)=%d, expected %d", input, got, expected)
		}
	}
	intervalCases := map[int]int{-1: 24, 0: 0, 24: 24, 24 * 365: 24 * 365, 24*365 + 1: 24 * 365}
	for input, expected := range intervalCases {
		if got := normalizedBackupInterval(input); got != expected {
			t.Fatalf("normalizedBackupInterval(%d)=%d, expected %d", input, got, expected)
		}
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

// SQLite 备份走 VACUUM INTO，与 Postgres 那条路径的产物、后缀和校验都不同。
func TestSQLiteBackupWritesUsableSnapshotAndRotates(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("本用例针对 SQLite 后端；Postgres 路径由既有用例覆盖")
	}
	setupVideoServiceTestDB(t)
	t.Setenv("DB_BACKEND", "sqlite")
	if database.ActiveBackend() != database.BackendSQLite {
		t.Fatalf("测试前置：期望 SQLite 后端")
	}
	video := models.Video{Name: "backup.mp4", Path: t.TempDir() + "/backup.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("建行失败: %v", err)
	}

	directory := t.TempDir()
	svc := &BackupService{dataDir: t.TempDir(), runner: execPostgresToolRunner{}, now: time.Now}
	file, err := svc.performBackup(context.Background(), directory)
	if err != nil {
		t.Fatalf("SQLite 备份失败: %v", err)
	}
	if !strings.HasSuffix(file.Name, sqliteBackupFileSuffix) {
		t.Fatalf("SQLite 快照后缀应为 %s，实际 %s", sqliteBackupFileSuffix, file.Name)
	}
	if file.Size <= 0 || file.Fingerprint == "" {
		t.Fatalf("快照元信息不完整: %+v", file)
	}

	// 产物必须是能打开、且含本应用表的真库——不然"备份成功"是假的。
	snapshot := filepath.Join(directory, file.Name)
	if err := verifySQLiteSnapshot(snapshot); err != nil {
		t.Fatalf("快照不可用: %v", err)
	}
	// 快照里应当能读到备份前写入的那一行。
	opened, err := gorm.Open(sqlite.Open(snapshot+"?mode=ro"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开快照失败: %v", err)
	}
	var count int64
	if err := opened.Model(&models.Video{}).Count(&count).Error; err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if sqlDB, err := opened.DB(); err == nil {
		_ = sqlDB.Close()
	}
	if count != 1 {
		t.Fatalf("快照内容不含备份前写入的行: count=%d", count)
	}
}

func TestRestoreRejectsSnapshotFromTheOtherBackend(t *testing.T) {
	for _, tc := range []struct {
		backend database.Backend
		name    string
	}{
		{database.BackendSQLite, "cineinsight-20260901-120000.000000000.dump"},
		{database.BackendPostgres, "cineinsight-20260901-120000.000000000.sqlite"},
	} {
		if backupSuffixMatchesBackend(tc.name, tc.backend) {
			t.Fatalf("%s 上不应接受 %s", tc.backend, tc.name)
		}
	}
	// 同后端的快照必须仍然被接受，否则拒绝逻辑写反了也测不出来。
	if !backupSuffixMatchesBackend("cineinsight-20260901-120000.000000000.sqlite", database.BackendSQLite) {
		t.Fatalf("SQLite 快照应被 SQLite 后端接受")
	}
	if !backupSuffixMatchesBackend("cineinsight-20260901-120000.000000000.dump", database.BackendPostgres) {
		t.Fatalf("Postgres 快照应被 Postgres 后端接受")
	}
	// 两种后缀都要能被列表识别，否则历史快照会在界面上消失。
	for _, name := range []string{
		"cineinsight-20260901-120000.000000000.dump",
		"cineinsight-20260901-120000.000000000.sqlite",
	} {
		if !isBackupFileName(name) {
			t.Fatalf("备份列表应识别 %s", name)
		}
	}
}

func TestRestoreRefusesMismatchedBackendBeforeTouchingTheDatabase(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("本用例针对 SQLite 后端")
	}
	setupVideoServiceTestDB(t)
	t.Setenv("DB_BACKEND", "sqlite")

	fenced := false
	svc := &BackupService{dataDir: t.TempDir(), runner: execPostgresToolRunner{}, now: time.Now}
	err := svc.RestoreBackupWithLifecycle(
		context.Background(),
		BackupRestoreRequest{Name: "cineinsight-20260901-120000.000000000.dump", Size: 10, Fingerprint: "x"},
		func() error { fenced = true; return nil },
		func() error { return nil },
	)
	if err == nil {
		t.Fatalf("跨后端恢复应被拒绝")
	}
	if !strings.Contains(err.Error(), "与当前数据库后端不匹配") {
		t.Fatalf("拒绝理由应说清楚，实际: %v", err)
	}
	// 关键：拒绝发生在进维护模式之前，数据库完全没被碰过。
	if fenced {
		t.Fatalf("跨后端快照不应触发维护模式围栏")
	}
}

// ===== P-027 数据安全（D-PC54 / D-PC56）=====

// openLiveSQLiteLibrary 在 dataDir 下按生产路径打开一个 SQLite 库并跑完 ApplySchema
// （含维护屏障回调与默认设置行），挂成 database.DB。恢复会关掉并替换这个文件。
func openLiveSQLiteLibrary(t *testing.T, dataDir string) *gorm.DB {
	t.Helper()
	t.Setenv("DB_BACKEND", "sqlite")
	t.Setenv("SQLITE_PATH", "")
	db, err := gorm.Open(sqlite.Open(database.SQLiteDSN(database.SQLitePath(dataDir))), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 SQLite 库失败: %v", err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema 失败: %v", err)
	}
	previousDB := database.DB
	database.DB = db
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		database.DB = previousDB
	})
	return db
}

func countSQLiteSnapshots(t *testing.T, directory string) int {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("读取备份目录失败: %v", err)
	}
	count := 0
	for _, entry := range entries {
		if isBackupFileName(entry.Name()) && strings.HasSuffix(entry.Name(), sqliteBackupFileSuffix) {
			count++
		}
	}
	return count
}

func readVideoNamesFromFile(t *testing.T, path string) []string {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(database.SQLiteDSN(path)), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开库文件失败: %v", err)
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	var names []string
	if err := db.Model(&models.Video{}).Order("name").Pluck("name", &names).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	return names
}

// APP-01：默认的 SQLite 后端不需要任何 PG 工具，备份与恢复都应可用；backup_directory 是
// 实际解析后的目录。
func TestAPP01BackupStatusAvailableOnSQLiteWithoutPostgresTools(t *testing.T) {
	service, runner, directory := setupBackupServiceTest(t, 7, 24)
	t.Setenv("DB_BACKEND", "sqlite")
	runner.missing["pg_dump"] = true
	runner.missing["pg_restore"] = true

	status := service.GetStatus()
	if !status.Available || !status.BackupAvailable || !status.RestoreAvailable {
		t.Fatalf("SQLite 下备份与恢复应可用（不看 pg 工具）: %#v", status)
	}
	if status.Reason != "" {
		t.Fatalf("SQLite 下不应报不可用原因: %q", status.Reason)
	}
	if status.BackupDirectory != directory {
		t.Fatalf("backup_directory 应为实际目录 %s，实际 %s", directory, status.BackupDirectory)
	}

	// 未配置目录时返回的是数据目录下的 backups，而不是空串。
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("backup_directory", "").Error; err != nil {
		t.Fatal(err)
	}
	if got := service.GetStatus().BackupDirectory; got != filepath.Join(service.dataDir, "backups") {
		t.Fatalf("默认备份目录解析错误: %s", got)
	}

	// PG 后端仍沿用原判定：缺工具就不可用。
	t.Setenv("DB_BACKEND", "postgres")
	if pg := service.GetStatus(); pg.Available || pg.BackupAvailable || pg.RestoreAvailable {
		t.Fatalf("PG 后端缺 pg 工具时应不可用: %#v", pg)
	}
}

// APP-01：「在访达中显示」打开的就是 GetStatus 给出的那个目录，目录不存在时先建出来。
func TestAPP01RevealBackupDirectoryOpensResolvedDirectory(t *testing.T) {
	service, _, directory := setupBackupServiceTest(t, 7, 24)
	opened := ""
	oldOpen := openWithDefaultFn
	openWithDefaultFn = func(path string, isDir bool) error {
		if !isDir {
			t.Fatalf("应按目录打开")
		}
		opened = path
		return nil
	}
	defer func() { openWithDefaultFn = oldOpen }()

	if err := service.RevealBackupDirectory(); err != nil {
		t.Fatalf("RevealBackupDirectory 失败: %v", err)
	}
	if opened != directory || opened != service.GetStatus().BackupDirectory {
		t.Fatalf("打开的目录 %q 与状态里的 %q 不一致", opened, directory)
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		t.Fatalf("备份目录应已被创建: %v", err)
	}

	openWithDefaultFn = func(string, bool) error { return errors.New("open " + directory + ": failed") }
	err := service.RevealBackupDirectory()
	if err == nil || strings.Contains(err.Error(), directory) {
		t.Fatalf("打开失败应返回不含绝对路径的错误: %v", err)
	}
}

// APP-01：SQLite 上「备份 → 修改数据 → 恢复」后数据回到备份点；安全快照在进入维护围栏
// 之前就已写好；恢复成功返回 nil，不进入「必须重启」的错误态。
func TestAPP01SQLiteBackupModifyRestoreReturnsToBackupPoint(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("本用例针对 SQLite 后端的库文件恢复")
	}
	dataDir := t.TempDir()
	db := openLiveSQLiteLibrary(t, dataDir)
	if err := db.Create(&models.Video{Name: "before.mp4", Path: filepath.Join(dataDir, "before.mp4"), Directory: dataDir}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewBackupService(dataDir)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	directory := filepath.Join(dataDir, "backups")

	// 修改：改名并新增一条，恢复后两处都应回到备份点。
	if err := db.Model(&models.Video{}).Where("name = ?", "before.mp4").Update("name", "renamed.mp4").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Video{Name: "extra.mp4", Path: filepath.Join(dataDir, "extra.mp4"), Directory: dataDir}).Error; err != nil {
		t.Fatal(err)
	}

	snapshotsAtFence := -1
	var release func()
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	// 与 App.enterDatabaseRestoreMode(true) 相同的两步：立围栏、关连接。
	beforeRestore := func() error {
		snapshotsAtFence = countSQLiteSnapshots(t, directory)
		release = database.BeginMaintenance()
		return database.Close()
	}
	reconnected := false
	err = service.RestoreBackupWithLifecycle(context.Background(),
		BackupRestoreRequest{Name: backup.Name, Size: backup.Size, Fingerprint: backup.Fingerprint},
		beforeRestore,
		func() error { reconnected = true; return nil })
	if err != nil {
		t.Fatalf("SQLite 恢复应成功返回 nil，实际: %v", err)
	}
	if DatabaseRestoreRequiresRestart(err) {
		t.Fatalf("恢复成功不应进入必须重启的错误态")
	}
	if reconnected {
		t.Fatalf("SQLite 恢复换的是库文件，不应在旧进程里重连")
	}
	if snapshotsAtFence != 2 {
		t.Fatalf("进入维护围栏时安全快照应已写好（原备份 + 安全快照 = 2），实际 %d", snapshotsAtFence)
	}
	names := readVideoNamesFromFile(t, database.SQLitePath(dataDir))
	if len(names) != 1 || names[0] != "before.mp4" {
		t.Fatalf("恢复后数据应回到备份点，实际 %v", names)
	}
	// I-2：替换经同目录临时文件 + rename，成功后不留临时文件。
	if leftovers, err := filepath.Glob(filepath.Join(dataDir, ".*cineinsight-restore-*")); err != nil || len(leftovers) != 0 {
		t.Fatalf("恢复成功后不应留下临时库文件: %v err=%v", leftovers, err)
	}
}

// APP-01：安全快照失败即中止——此时还没进维护模式，库原样可用，错误可重试而不是致命。
func TestAPP01SQLiteRestoreSafetySnapshotFailureAbortsBeforeMaintenance(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("本用例针对 SQLite 后端的库文件恢复")
	}
	dataDir := t.TempDir()
	db := openLiveSQLiteLibrary(t, dataDir)
	if err := db.Create(&models.Video{Name: "live.mp4", Path: filepath.Join(dataDir, "live.mp4"), Directory: dataDir}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewBackupService(dataDir)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	directory := filepath.Join(dataDir, "backups")
	// 让安全快照的 VACUUM INTO 失败：在它要写的临时路径上预先放一个非空目录。
	fixed := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixed }
	blocker := filepath.Join(directory, fmt.Sprintf(".cineinsight-backup-%d.tmp", fixed.UnixNano()))
	if err := os.MkdirAll(filepath.Join(blocker, "occupied"), 0700); err != nil {
		t.Fatal(err)
	}

	fenced := false
	err = service.RestoreBackupWithLifecycle(context.Background(),
		BackupRestoreRequest{Name: backup.Name, Size: backup.Size, Fingerprint: backup.Fingerprint},
		func() error { fenced = true; return nil }, nil)
	if err == nil || !strings.Contains(err.Error(), "恢复前安全备份失败") {
		t.Fatalf("安全快照失败应中止恢复: %v", err)
	}
	if DatabaseRestoreRequiresRestart(err) {
		t.Fatalf("安全快照失败发生在维护模式之前，不应要求重启: %v", err)
	}
	if fenced {
		t.Fatalf("安全快照失败时不应进入维护模式")
	}
	var names []string
	if err := database.DB.Model(&models.Video{}).Pluck("name", &names).Error; err != nil {
		t.Fatalf("库应仍然可用: %v", err)
	}
	if len(names) != 1 || names[0] != "live.mp4" {
		t.Fatalf("库不应被修改: %v", names)
	}
	settings, err := (&SettingsService{}).GetSettings()
	if err != nil || !strings.Contains(settings.BackupLastError, "恢复前安全备份失败") {
		t.Fatalf("失败原因应记入 backup_last_error: %q err=%v", settings.BackupLastError, err)
	}
}

// APP-09：定时器的每一拍都调 MaybeBackup（到点才备份），维护模式期间跳过。
func TestAPP09PeriodicBackupTickRunsDueBackupAndSkipsDuringMaintenance(t *testing.T) {
	service, runner, directory := setupBackupServiceTest(t, 7, 24)

	release := database.BeginMaintenance()
	created, err := service.periodicTick(context.Background())
	release()
	if err != nil || created || len(runner.calls) != 0 {
		t.Fatalf("维护模式期间应跳过: created=%v err=%v calls=%#v", created, err, runner.calls)
	}

	created, err = service.periodicTick(context.Background())
	if err != nil || !created {
		t.Fatalf("到点的定时检查应执行备份: created=%v err=%v", created, err)
	}
	if backups, err := service.listBackupsIn(directory); err != nil || len(backups) != 1 {
		t.Fatalf("应有一份备份: %#v err=%v", backups, err)
	}

	created, err = service.periodicTick(context.Background())
	if err != nil || created {
		t.Fatalf("刚备份过，下一拍应按间隔跳过: created=%v err=%v", created, err)
	}
}

// APP-09：常驻期间按周期触发，而不只是启动时一次；ctx 取消后退出。
func TestAPP09PeriodicBackupLoopRunsOnTickerAndStopsOnCancel(t *testing.T) {
	service, _, directory := setupBackupServiceTest(t, 7, 24)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.runPeriodic(ctx, 5*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if backups, err := service.listBackupsIn(directory); err == nil && len(backups) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("定时循环没有触发备份")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后定时循环应退出")
	}
}

// APP01 / I-2 / m3：SQLite 恢复换库文件是原子的。复制中途出错时正式库文件原样可用（内容仍是恢复前、
// 结构完好）、临时文件被清掉。m3 之后临时库在关句柄**之前**备好：复制失败时还没进维护模式、句柄
// 还开着，结果是一次普通的恢复失败而不是「必须重启」（此前这里钉的是 DatabaseRestoreRequiresRestart）。
func TestAPP01SQLiteRestoreCopyFailureLeavesLiveLibraryIntact(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("本用例针对 SQLite 后端的库文件恢复")
	}
	dataDir := t.TempDir()
	db := openLiveSQLiteLibrary(t, dataDir)
	if err := db.Create(&models.Video{Name: "before.mp4", Path: filepath.Join(dataDir, "before.mp4"), Directory: dataDir}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewBackupService(dataDir)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if err := db.Model(&models.Video{}).Where("name = ?", "before.mp4").Update("name", "renamed.mp4").Error; err != nil {
		t.Fatal(err)
	}

	copied := int64(0)
	service.restoreCopy = func(dst io.Writer, src io.Reader) (int64, error) {
		written, _ := io.CopyN(dst, src, 4096)
		copied = written
		return written, errors.New("模拟复制中途磁盘写满")
	}
	var release func()
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	fenced := false
	err = service.RestoreBackupWithLifecycle(context.Background(),
		BackupRestoreRequest{Name: backup.Name, Size: backup.Size, Fingerprint: backup.Fingerprint},
		func() error {
			fenced = true
			release = database.BeginMaintenance()
			return database.Close()
		}, nil)
	if err == nil || DatabaseRestoreRequiresRestart(err) || !strings.Contains(err.Error(), "数据库未被修改") {
		t.Fatalf("关句柄之前的复制失败应是普通失败、不要求重启: %v", err)
	}
	if copied == 0 {
		t.Fatal("前置：替身应在写了一部分之后失败")
	}
	if fenced {
		t.Fatal("复制失败发生在进维护模式之前，不应进入维护模式")
	}
	// 句柄没关：库仍然可用，失败原因记进备份状态。
	var liveNames []string
	if err := database.DB.Model(&models.Video{}).Pluck("name", &liveNames).Error; err != nil || len(liveNames) != 1 || liveNames[0] != "renamed.mp4" {
		t.Fatalf("复制失败后库应仍可用且未被修改: %v err=%v", liveNames, err)
	}
	if settings, err := (&SettingsService{}).GetSettings(); err != nil || !strings.Contains(settings.BackupLastError, "写入临时库文件失败") {
		t.Fatalf("失败原因应记入 backup_last_error: %+v err=%v", settings, err)
	}

	livePath := database.SQLitePath(dataDir)
	if names := readVideoNamesFromFile(t, livePath); len(names) != 1 || names[0] != "renamed.mp4" {
		t.Fatalf("复制失败时正式库文件应保持恢复前的内容: %v", names)
	}
	check, err := gorm.Open(sqlite.Open(database.SQLiteDSN(livePath)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var integrity string
	if err := check.Raw("PRAGMA integrity_check").Scan(&integrity).Error; err != nil || integrity != "ok" {
		t.Fatalf("正式库文件应结构完好: %q err=%v", integrity, err)
	}
	if sqlDB, err := check.DB(); err == nil {
		_ = sqlDB.Close()
	}
	leftovers, err := filepath.Glob(filepath.Join(dataDir, ".*cineinsight-restore-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("失败后临时库文件应被清掉: %v err=%v", leftovers, err)
	}
}

// sqliteRestoreTempsIn 列出库目录里的恢复临时库文件（`.<库文件名>.cineinsight-restore-*.tmp`）。
func sqliteRestoreTempsIn(t *testing.T, livePath string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(livePath), "."+filepath.Base(livePath)+".cineinsight-restore-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// APP01 / m3：临时库在关句柄之前就已复制、落盘、关闭——进维护模式（beforeRestore）的那一刻，库目录里
// 已经有一份与快照等大的临时库；关句柄之后只剩删边车、rename、目录 fsync。
func TestAPP01SQLiteRestorePreparesTempLibraryBeforeClosingHandle(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("本用例针对 SQLite 后端的库文件恢复")
	}
	dataDir := t.TempDir()
	db := openLiveSQLiteLibrary(t, dataDir)
	if err := db.Create(&models.Video{Name: "before.mp4", Path: filepath.Join(dataDir, "before.mp4"), Directory: dataDir}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewBackupService(dataDir)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if err := db.Model(&models.Video{}).Where("name = ?", "before.mp4").Update("name", "renamed.mp4").Error; err != nil {
		t.Fatal(err)
	}
	livePath := database.SQLitePath(dataDir)
	var atFence []string
	var tempSize int64 = -1
	var release func()
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	err = service.RestoreBackupWithLifecycle(context.Background(),
		BackupRestoreRequest{Name: backup.Name, Size: backup.Size, Fingerprint: backup.Fingerprint},
		func() error {
			atFence = sqliteRestoreTempsIn(t, livePath)
			if len(atFence) == 1 {
				if info, err := os.Stat(atFence[0]); err == nil {
					tempSize = info.Size()
				}
			}
			release = database.BeginMaintenance()
			return database.Close()
		}, nil)
	if err != nil {
		t.Fatalf("恢复应成功: %v", err)
	}
	if len(atFence) != 1 || tempSize != backup.Size {
		t.Fatalf("进维护模式时临时库应已备好（与快照等大 %d）: temps=%v size=%d", backup.Size, atFence, tempSize)
	}
	if names := readVideoNamesFromFile(t, livePath); len(names) != 1 || names[0] != "before.mp4" {
		t.Fatalf("恢复后数据应回到备份点: %v", names)
	}
	if leftovers := sqliteRestoreTempsIn(t, livePath); len(leftovers) != 0 {
		t.Fatalf("成功后不应留下临时库: %v", leftovers)
	}
}

// APP01 / m3 / m4：库目录所在卷放不下一份快照时直接失败、不进维护模式、库照常可用；恢复开始时先清掉
// 上次崩溃留下的临时库（只删匹配命名的普通文件），再去算空间。
func TestAPP01SQLiteRestoreInsufficientSpaceFailsBeforeMaintenanceAndSweepsLeftovers(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("本用例针对 SQLite 后端的库文件恢复")
	}
	dataDir := t.TempDir()
	db := openLiveSQLiteLibrary(t, dataDir)
	if err := db.Create(&models.Video{Name: "live.mp4", Path: filepath.Join(dataDir, "live.mp4"), Directory: dataDir}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewBackupService(dataDir)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	livePath := database.SQLitePath(dataDir)
	leftover := filepath.Join(dataDir, "."+filepath.Base(livePath)+".cineinsight-restore-1234.tmp")
	unrelated := filepath.Join(dataDir, "."+filepath.Base(livePath)+".cineinsight-restore-keep.txt")
	for _, path := range []string{leftover, unrelated} {
		if err := os.WriteFile(path, []byte("left over"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// 名字匹配但不是普通文件：不碰。
	namedDir := filepath.Join(dataDir, "."+filepath.Base(livePath)+".cineinsight-restore-dir.tmp")
	if err := os.MkdirAll(namedDir, 0700); err != nil {
		t.Fatal(err)
	}

	var checkedDir string
	service.diskFree = func(path string) (uint64, error) {
		checkedDir = path
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Error("算空间之前应已清掉遗留的临时库")
		}
		return uint64(backup.Size - 1), nil
	}
	fenced := false
	err = service.RestoreBackupWithLifecycle(context.Background(),
		BackupRestoreRequest{Name: backup.Name, Size: backup.Size, Fingerprint: backup.Fingerprint},
		func() error { fenced = true; return nil }, nil)
	if err == nil || !strings.Contains(err.Error(), "剩余空间不足") || DatabaseRestoreRequiresRestart(err) {
		t.Fatalf("空间不足应直接失败、不要求重启: %v", err)
	}
	if fenced {
		t.Fatal("空间不足时不应进入维护模式")
	}
	if checkedDir != filepath.Dir(livePath) {
		t.Fatalf("应检查库文件所在目录的卷: %q", checkedDir)
	}
	if temps := sqliteRestoreTempsIn(t, livePath); len(temps) != 1 || temps[0] != namedDir {
		t.Fatalf("只应留下名字匹配的目录，不应新建临时库: %v", temps)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("不匹配命名的文件不得删除: %v", err)
	}
	var names []string
	if err := database.DB.Model(&models.Video{}).Pluck("name", &names).Error; err != nil || len(names) != 1 || names[0] != "live.mp4" {
		t.Fatalf("库应仍可用且未被修改: %v err=%v", names, err)
	}
	if settings, err := (&SettingsService{}).GetSettings(); err != nil || !strings.Contains(settings.BackupLastError, "剩余空间不足") {
		t.Fatalf("失败原因应记入 backup_last_error: %+v err=%v", settings, err)
	}
}

// APP01 / m4：关掉句柄之后库文件旁的 -wal 仍然非空，说明还有提交没写回主库：中止恢复、不删边车、
// 不动正式库，要求重启（重启后 SQLite 会先把 WAL 写回）；临时库被清掉。
func TestAPP01SQLiteRestoreAbortsWhenWALStillHoldsCommits(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("本用例针对 SQLite 后端的库文件恢复")
	}
	dataDir := t.TempDir()
	db := openLiveSQLiteLibrary(t, dataDir)
	if err := db.Create(&models.Video{Name: "before.mp4", Path: filepath.Join(dataDir, "before.mp4"), Directory: dataDir}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewBackupService(dataDir)
	backup, err := service.CreateBackup(context.Background())
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	livePath := database.SQLitePath(dataDir)
	walPath := livePath + "-wal"
	walContent := []byte("uncheckpointed frames")
	liveHash := ""
	var release func()
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	err = service.RestoreBackupWithLifecycle(context.Background(),
		BackupRestoreRequest{Name: backup.Name, Size: backup.Size, Fingerprint: backup.Fingerprint},
		func() error {
			release = database.BeginMaintenance()
			if err := database.Close(); err != nil {
				return err
			}
			hash, err := hashFile(livePath)
			if err != nil {
				return err
			}
			liveHash = hash
			// 模拟关闭之后仍有未 checkpoint 的提交（例如还有别的连接开着这个库）。
			return os.WriteFile(walPath, walContent, 0600)
		}, nil)
	if err == nil || !DatabaseRestoreRequiresRestart(err) || !strings.Contains(err.Error(), "WAL") {
		t.Fatalf("-wal 非空时应中止恢复并要求重启: %v", err)
	}
	if got, err := hashFile(livePath); err != nil || got != liveHash {
		t.Fatalf("正式库文件不得被替换: hash=%s want=%s err=%v", got, liveHash, err)
	}
	if got, err := os.ReadFile(walPath); err != nil || string(got) != string(walContent) {
		t.Fatalf("非空的 -wal 不得被删除: %q err=%v", got, err)
	}
	if leftovers := sqliteRestoreTempsIn(t, livePath); len(leftovers) != 0 {
		t.Fatalf("中止后临时库应被清掉: %v", leftovers)
	}
}

// APP02 / M-4：维护围栏生效期间（恢复、切换后端、切换后的待重启）「立即备份」直接拒绝；
// 备份状态只经普通通道写，围栏期间写不进正在被迁移的源库。恢复流程自己的记录仍经维护通道。
func TestAPP02ManualBackupRejectedDuringMaintenanceAndStatusNotWrittenThroughFence(t *testing.T) {
	dataDir := t.TempDir()
	db := openLiveSQLiteLibrary(t, dataDir)
	service := NewBackupService(dataDir)
	release := database.BeginMaintenance()
	t.Cleanup(release)

	if backup, err := service.CreateBackup(context.Background()); !errors.Is(err, ErrBackupDuringMaintenance) || backup != nil {
		t.Fatalf("维护期间手动备份应直接拒绝: %#v err=%v", backup, err)
	}
	if err := service.recordAttempt(true, nil); !errors.Is(err, database.ErrMaintenance) {
		t.Fatalf("备份状态的普通写入在围栏期间应被拒绝: %v", err)
	}
	var settings models.Settings
	if err := database.WithMaintenanceAccess(db).First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if settings.BackupLastAttemptAt != nil || settings.BackupLastSuccessAt != nil {
		t.Fatalf("围栏期间不得写入备份状态: %+v", settings)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "backups")); !os.IsNotExist(err) {
		t.Fatalf("被拒绝的备份不应产生备份目录或文件: %v", err)
	}
	if err := service.recordRestoreAttempt(false, errors.New("恢复失败")); err != nil {
		t.Fatalf("恢复流程在自己的围栏里仍应能记下结果: %v", err)
	}

	release()
	if _, err := service.CreateBackup(context.Background()); err != nil {
		t.Fatalf("围栏撤掉后备份应恢复可用: %v", err)
	}
}
