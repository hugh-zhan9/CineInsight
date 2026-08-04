package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
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
