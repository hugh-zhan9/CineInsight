package services

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

type polishBackupFixture struct {
	Rows               []any
	VideoID, DeletedID uint
}

func seedPolishBackup(t *testing.T, db *gorm.DB) polishBackupFixture {
	t.Helper()
	at := time.Date(2026, 10, 22, 7, 8, 9, 123456000, time.UTC)
	until := at.Add(7 * 24 * time.Hour)
	video := models.Video{Name: "backup.mp4", Path: "/fixture/backup.mp4"}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	gone := models.Video{Name: "deleted.mp4", Path: "/fixture/deleted.mp4"}
	if err := db.Create(&gone).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().Delete(&gone).Error; err != nil {
		t.Fatal(err)
	}
	collection := models.MediaCollection{Name: "Backup episodes", NormalizedName: "backup episodes"}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.CollectionVideo{CollectionID: collection.ID, VideoID: video.ID, Position: 1}).Error; err != nil {
		t.Fatal(err)
	}
	run := &models.TaskFailureRun{TaskKey: "technical", StartedAt: at, FinishedAt: &at, State: "interrupted", RecordsComplete: false}
	if err := db.Create(run).Error; err != nil {
		t.Fatal(err)
	}
	season, episode := 0, 2
	fixture := polishBackupFixture{VideoID: video.ID, DeletedID: gone.ID, Rows: []any{
		&models.RecommendationFeedback{TargetKind: "work_video", TargetID: gone.ID, Effect: "snooze", ExpiresAt: &until, Revision: 8, CreatedAt: at, UpdatedAt: at},
		run,
		&models.TaskFailureItem{RunID: run.ID, MediaKind: "video", MediaID: gone.ID, AttemptKey: "retained-attempt", SourceVersion: "old-source", StateVersion: "old-failure", ErrorCode: "failed", ErrorSummary: "保留已删除媒体失败", FailedAt: at},
		&models.CollectionEpisodeAssignment{CollectionID: collection.ID, VideoID: video.ID, Season: &season, Episode: &episode, Excluded: false, Revision: 9},
		&models.CollectionSeasonExpectation{CollectionID: collection.ID, Season: 0, EpisodeCount: 6, Revision: 10},
		&models.AITaggingState{VideoID: video.ID, Status: "pending", FailureRetrySourceVersion: strings.Repeat("b", 64), CreatedAt: at, UpdatedAt: at},
		&models.VideoTechnicalMetadata{VideoID: video.ID, LastError: "failed", FailureRetryClaim: "technical-claim", CreatedAt: at, UpdatedAt: at},
		&models.VideoPlaybackProxy{VideoID: video.ID, Status: "failed", Strategy: "remux", SourceSize: 123, SourceModTimeNS: 456, LastUsedAt: at, FailureRetryClaim: "proxy-claim", CreatedAt: at, UpdatedAt: at},
		&models.PlayEvent{VideoID: video.ID, Source: "desktop_controlled_view", PlayedAt: at},
		&models.ViewingDiaryEntry{VideoID: &video.ID, Title: "controlled", WatchedOn: "2026-10-22", Source: "desktop_controlled_view", Revision: 3, CreatedAt: at, UpdatedAt: at},
	}}
	for _, row := range fixture.Rows {
		if row == run {
			continue
		}
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&models.Settings{}).Where("id > 0").Update("desktop_player", "iina_controlled").Error; err != nil {
		t.Fatal(err)
	}
	return fixture
}
func assertPolishBackup(t *testing.T, db *gorm.DB, fixture polishBackupFixture) {
	t.Helper()
	for _, want := range fixture.Rows {
		got := reflect.New(reflect.TypeOf(want).Elem()).Interface()
		if err := db.First(got).Error; err != nil {
			t.Fatal(err)
		}
		expectedJSON, err := polishBackupJSON(want)
		if err != nil {
			t.Fatal(err)
		}
		actualJSON, err := polishBackupJSON(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(expectedJSON) != string(actualJSON) {
			t.Fatalf("restored %T differs\nwant %s\ngot %s", want, expectedJSON, actualJSON)
		}
	}
	var ai models.AITaggingState
	var technical models.VideoTechnicalMetadata
	var proxy models.VideoPlaybackProxy
	for _, row := range []any{&ai, &technical, &proxy} {
		if err := db.Where("video_id = ?", fixture.VideoID).First(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if ai.FailureRetrySourceVersion != strings.Repeat("b", 64) || technical.FailureRetryClaim != "technical-claim" || proxy.FailureRetryClaim != "proxy-claim" {
		t.Fatal("backup lost private owner authorization fields")
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if settings.DesktopPlayer != "iina_controlled" {
		t.Fatal("backup lost player preference")
	}
	var count int64
	if err := db.Unscoped().Model(&models.Video{}).Where("id = ?", fixture.DeletedID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("restore recreated a logical deleted media target")
	}
}

// JSON compares persisted values after normalizing timestamp locations, not instants.
func polishBackupJSON(row any) ([]byte, error) {
	value := reflect.ValueOf(row).Elem()
	clone := reflect.New(value.Type())
	clone.Elem().Set(value)
	timeType := reflect.TypeOf(time.Time{})
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		target := clone.Elem().Field(i)
		if !target.CanSet() {
			continue
		}
		if field.Type() == timeType {
			target.Set(reflect.ValueOf(field.Interface().(time.Time).UTC()))
		}
		if field.Type() == reflect.PointerTo(timeType) && !field.IsNil() {
			at := field.Elem().Interface().(time.Time).UTC()
			target.Set(reflect.ValueOf(&at))
		}
	}
	return json.Marshal(clone.Interface())
}

func mutatePolishBackup(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range []string{"task_failure_items", "task_failure_runs", "recommendation_feedbacks", "collection_episode_assignments", "collection_season_expectations"} {
		if err := db.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatal(err)
		}
	}
	for table, column := range map[string]string{"ai_tagging_states": "failure_retry_source_version", "video_technical_metadata": "failure_retry_claim", "video_playback_proxies": "failure_retry_claim"} {
		if err := db.Table(table).Where("video_id > 0").UpdateColumn(column, "").Error; err != nil {
			t.Fatal(err)
		}
	}
}
func assertPolishManifest(t *testing.T, directory string, backup *BackupFile) {
	t.Helper()
	manifest, err := loadBackupManifest(directory, backup.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"recommendation_feedbacks", "task_failure_runs", "task_failure_items", "collection_episode_assignments", "collection_season_expectations"} {
		if len(manifest.Models[table]) == 0 {
			t.Fatalf("backup model manifest omitted %s", table)
		}
		if _, ok := manifest.Schema.Tables[table]; !ok {
			t.Fatalf("physical snapshot omitted %s", table)
		}
		if manifest.Backend == "sqlite" && manifest.Rows[table] != 1 {
			t.Fatalf("backup %s did not contain actual row", table)
		}
	}
	unknown, err := backupSchemaUnknown(manifest)
	if err != nil || len(unknown) > 0 {
		t.Fatalf("current backup not compatible: %v %v", unknown, err)
	}
	older := map[string][]string{}
	for table, columns := range manifest.Models {
		older[table] = columns
	}
	for _, table := range []string{"recommendation_feedbacks", "task_failure_runs", "task_failure_items", "collection_episode_assignments", "collection_season_expectations"} {
		delete(older, table)
	}
	for table, column := range map[string]string{"settings": "desktop_player", "ai_tagging_states": "failure_retry_source_version", "video_technical_metadata": "failure_retry_claim", "video_playback_proxies": "failure_retry_claim"} {
		columns := []string{}
		for _, name := range older[table] {
			if name != column {
				columns = append(columns, name)
			}
		}
		older[table] = columns
	}
	if newer := unknownModelSchema(manifest.Models, older, nil); len(newer) != 9 {
		t.Fatalf("old model incorrectly accepted new-table backup: %v", newer)
	}
	if unknown := unknownModelSchema(older, manifest.Models, nil); len(unknown) != 0 {
		t.Fatalf("new model rejected additive older backup: %v", unknown)
	}
}

func TestFunctionalPolishSQLiteBackupRestoreActualRows(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("SQLite file restore; independent PG dump/restore test covers PostgreSQL")
	}
	dataDir := t.TempDir()
	db := openLiveSQLiteLibrary(t, dataDir)
	fixture := seedPolishBackup(t, db)
	svc := NewBackupService(dataDir)
	backup, err := svc.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertPolishManifest(t, filepath.Join(dataDir, "backups"), backup)
	mutatePolishBackup(t, db)
	var release func()
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	err = svc.RestoreBackupWithLifecycle(context.Background(), BackupRestoreRequest{Name: backup.Name, Size: backup.Size, Fingerprint: backup.Fingerprint}, func() error { release = database.BeginMaintenance(); return database.Close() }, nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release = nil
	restored, err := gorm.Open(sqlite.Open(database.SQLitePath(dataDir)+"?mode=ro"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { pool, _ := restored.DB(); pool.Close() }()
	assertPolishBackup(t, restored, fixture)
}

// dockerPolishBackupRunner uses the existing tool-runner seam to run real PG
// tools already present in the explicitly selected test container. Host files
// travel through stdin/stdout; production code does not depend on Docker.
type dockerPolishBackupRunner struct{ container string }

func (r dockerPolishBackupRunner) LookPath(name string) (string, error) {
	if name != "pg_dump" && name != "pg_restore" {
		return "", fmt.Errorf("unsupported fixture tool")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return "", err
	}
	err := exec.Command("docker", "exec", r.container, name, "--version").Run()
	return name, err
}
func (r dockerPolishBackupRunner) Run(ctx context.Context, name string, args, env []string) error {
	toolArgs := []string{}
	var inputPath, outputPath string
	for i := 0; i < len(args); i++ {
		if args[i] == "--file" && i+1 < len(args) {
			outputPath = args[i+1]
			i++
			continue
		}
		if name == "pg_restore" && i == len(args)-1 && filepath.IsAbs(args[i]) {
			inputPath = args[i]
			continue
		}
		toolArgs = append(toolArgs, args[i])
	}
	dockerArgs := []string{"exec", "-i"}
	keys := []string{"PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGSSLMODE"}
	for _, key := range keys {
		dockerArgs = append(dockerArgs, "-e", key)
	}
	dockerArgs = append(dockerArgs, r.container, name)
	dockerArgs = append(dockerArgs, toolArgs...)
	command := exec.CommandContext(ctx, "docker", dockerArgs...)
	command.Env = append(append(os.Environ(), env...), "PGHOST=127.0.0.1", "PGPORT=5432")
	if inputPath != "" {
		input, err := os.Open(inputPath)
		if err != nil {
			return err
		}
		defer input.Close()
		command.Stdin = input
	}
	command.Stdout = io.Discard
	if outputPath != "" {
		output, err := os.Create(outputPath)
		if err != nil {
			return err
		}
		defer output.Close()
		command.Stdout = output
	}
	diagnostic := &tailWriter{}
	command.Stderr = diagnostic
	if err := command.Run(); err != nil {
		return fmt.Errorf("real container %s: %w: %s", name, err, string(diagnostic.tail))
	}
	return nil
}
func TestFunctionalPolishPostgresBackupRestoreActualRows(t *testing.T) {
	dsn := os.Getenv(dbtest.PostgresDSNEnv)
	container := os.Getenv("CINEINSIGHT_TEST_PG_CONTAINER")
	if dsn == "" || container == "" {
		t.Skip("requires dedicated test DSN and explicit test container with pg_dump/pg_restore")
	}
	config, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("cine_polish_backup_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	var live *gorm.DB
	t.Cleanup(func() {
		if live != nil {
			pool, _ := live.DB()
			pool.Close()
		}
		database.DB = previous
		if err := admin.Exec("DROP DATABASE " + name + " WITH (FORCE)").Error; err != nil {
			t.Errorf("fixture database cleanup: %v", err)
		}
		pool, _ := admin.DB()
		pool.Close()
	})
	isolatedDSN := dsn + " dbname=" + name
	open := func() error {
		db, err := gorm.Open(postgres.Open(isolatedDSN), &gorm.Config{})
		if err != nil {
			return err
		}
		live = db
		database.DB = db
		return nil
	}
	if err := open(); err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(live); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_BACKEND", "postgres")
	t.Setenv("PG_HOST", config.Host)
	t.Setenv("PG_PORT", fmt.Sprint(config.Port))
	t.Setenv("PG_USER", config.User)
	t.Setenv("PG_PASSWORD", config.Password)
	t.Setenv("PG_DB", name)
	t.Setenv("PG_SSLMODE", "disable")
	fixture := seedPolishBackup(t, live)
	dataDir := t.TempDir()
	svc := NewBackupService(dataDir)
	svc.runner = dockerPolishBackupRunner{container: container}
	backup, err := svc.CreateBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertPolishManifest(t, filepath.Join(dataDir, "backups"), backup)
	mutatePolishBackup(t, live)
	var release func()
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	err = svc.RestoreBackupWithLifecycle(context.Background(), BackupRestoreRequest{Name: backup.Name, Size: backup.Size, Fingerprint: backup.Fingerprint}, func() error { release = database.BeginMaintenance(); return database.Close() }, open)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release = nil
	assertPolishBackup(t, live, fixture)
	t.Log("real pg_dump/pg_restore used an isolated fixture database; host native tools were not assumed")
}
