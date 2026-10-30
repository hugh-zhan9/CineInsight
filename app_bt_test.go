package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
	"video-master/services"
)

func newBTAppTest(t *testing.T) (*App, string) {
	t.Helper()
	openAppLiveDatabase(t, func() *gorm.DB { return dbtest.OpenRaw(t) })
	if !dbtest.IsPostgres() {
		var rows []struct {
			Name string
			File string
		}
		if err := database.DB.Raw("PRAGMA database_list").Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Name == "main" {
				t.Setenv("SQLITE_PATH", row.File)
			}
		}
	}
	dir := t.TempDir()
	a := &App{videoService: services.NewVideoService(nil), settingsService: &services.SettingsService{}}
	a.videoService.SetBTPathPolicy(services.NewBTPathPolicy(nil))
	a.wireBT(dir)
	t.Cleanup(func() { a.stopBT(context.Background()) })
	return a, dir
}
func seedBTAppTask(t *testing.T, stage string) models.BTTask {
	t.Helper()
	id := uuid.NewString()
	connection := models.BTConnection{ID: uuid.NewString(), Slot: "local", Revision: 1}
	if err := database.DB.Create(&connection).Error; err != nil {
		t.Fatal(err)
	}
	task := models.BTTask{ID: id, ConnectionID: connection.ID, Revision: 4, Stage: stage, RootKey: strings.Repeat("a", 64), IntentKey: id, OwnerTag: id, SaveRoot: t.TempDir()}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	return task
}
func TestBTAppLocalInitializationBeforeBindingsAndNoWorker(t *testing.T) {
	a, _ := newBTAppTest(t)
	if _, err := a.ListBTTasks("", 10); !errors.Is(err, services.ErrBTRestorePending) {
		t.Fatal("bindings opened before bootstrap", err)
	}
	task := seedBTAppTask(t, "queued_add")
	if err := a.initializeBTLocal(); err != nil {
		t.Fatal(err)
	}
	view, err := a.GetBTTask(task.ID)
	if err != nil || view.Stage != "uncertain" {
		t.Fatal(view, err)
	}
	if a.startupError != "" {
		t.Fatal("BT polluted global startup")
	}
	// No startup context exists and no external worker is started by initialization.
	if a.ctx != nil {
		t.Fatal("bootstrap invented UI context")
	}
	if rows, err := a.ListBTTasks("", 10); err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
}
func TestBTAppUnknownRestoreErrorConsumesFenceBeforeResume(t *testing.T) {
	a, dir := newBTAppTest(t)
	task := seedBTAppTask(t, "active")
	if err := a.initializeBTLocal(); err != nil {
		t.Fatal(err)
	}
	if err := a.quiesceBT(context.Background()); err != nil {
		t.Fatal(err)
	}
	ticket, err := a.prepareBTRestore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Models a restore which committed but reported an ordinary error after reconnect.
	database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Updates(map[string]any{"stage": "active", "revision": 1, "pending_action": "", "unresolved_write": false})
	a.resumeBTAfterMaintenance()
	view, err := a.GetBTTask(task.ID)
	if err != nil || view.Stage != "uncertain" || !view.UnresolvedWrite || view.PendingAction != "restore" {
		t.Fatal("unknown restore resumed old intent", view, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bt-recovery", ticket.Scope+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("consumed marker retained", err)
	}
}
func TestBTAppFailedFenceConsumptionStaysClosedAndIsolated(t *testing.T) {
	a, dir := newBTAppTest(t)
	seedBTAppTask(t, "active")
	if err := a.initializeBTLocal(); err != nil {
		t.Fatal(err)
	}
	ticket, err := a.prepareBTRestore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bt-recovery", ticket.Scope+".json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	a.resumeBTAfterMaintenance()
	if _, err := a.ListBTTasks("", 10); !errors.Is(err, services.ErrBTRestorePending) {
		t.Fatal("corrupt marker opened BT", err)
	}
	if a.startupError != "" {
		t.Fatal("BT failure blocked app startup")
	}
	var count int64
	if err := database.DB.Model(&models.BTTask{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("ordinary DB access blocked", err)
	}
}
func TestBTAppBindingsAndTaskCenterRemainExternal(t *testing.T) {
	expected := []string{"GetBTConnection", "ConfigureBTConnection", "TestBTConnection", "ListBTIndexers", "SearchBTResources", "PreviewBTResource", "SubmitBTDownload", "ListBTTasks", "GetBTTask", "ControlBTTask"}
	typ := reflect.TypeOf(&App{})
	for _, name := range expected {
		if _, ok := typ.MethodByName(name); !ok {
			t.Fatal("missing frontend contract", name)
		}
	}
	a := &App{}
	recent := a.taskCenterRecent(&taskCenterSources{btDownloads: []services.BTTaskSummary{{ID: uuid.NewString(), Title: "Film", Status: "done"}}})
	if len(recent) != 1 || recent[0].Kind != "bt_download" || len(recent[0].Actions) != 1 || recent[0].Actions[0] != "open_bt_downloads" {
		t.Fatal(recent)
	}
	for _, key := range services.BackgroundTaskKeys() {
		if strings.Contains(key, "bt_") {
			t.Fatal("external downloader registered as local blocking task")
		}
	}
}

func TestBTAppOrdinaryRestoreErrorAfterDatabaseReplacementKeepsFence(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("models successful reconnect at the same SQLite physical identity")
	}
	a, dir := newBTAppTest(t)
	task := seedBTAppTask(t, "active")
	if err := a.initializeBTLocal(); err != nil {
		t.Fatal(err)
	}
	var databases []struct {
		Name string
		File string
	}
	if err := database.DB.Raw("PRAGMA database_list").Scan(&databases).Error; err != nil {
		t.Fatal(err)
	}
	path := ""
	for _, d := range databases {
		if d.Name == "main" {
			path = d.File
		}
	}
	if path == "" {
		t.Fatal("fixture requires file DB")
	}
	a.restoreDatabaseBackup = func(ctx context.Context, _ services.BackupRestoreRequest, before func() error, _ func() error) error {
		if err := before(); err != nil {
			t.Fatal("beforeRestore failed", err)
		}
		reopened, err := gorm.Open(sqlite.Open(database.SQLiteDSN(path)), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		database.DB = reopened
		t.Cleanup(func() { sql, _ := reopened.DB(); sql.Close() })
		// The restore may already have committed even though the external command fails.
		maintenance := database.WithMaintenanceAccessContext(ctx, reopened)
		if err := maintenance.Model(&models.BTTask{}).Where("id = ?", task.ID).Updates(map[string]any{"stage": "active", "revision": 1, "pending_action": "", "unresolved_write": false}).Error; err != nil {
			t.Fatal(err)
		}
		return errors.New("ordinary command error after commit")
	}
	err := a.RestoreDatabaseBackup(services.BackupRestoreRequest{})
	if err == nil || services.DatabaseRestoreRequiresRestart(err) {
		t.Fatal("fixture expected ordinary error", err)
	}
	view, err := a.GetBTTask(task.ID)
	if err != nil || view.Stage != "uncertain" || !view.UnresolvedWrite || view.PendingAction != "restore" {
		t.Fatal("ordinary error skipped freeze", view, err)
	}
	markers, _ := filepath.Glob(filepath.Join(dir, "bt-recovery", "*.json"))
	if len(markers) != 0 {
		t.Fatal("successful safe-point consumption left marker")
	}
}

func TestBTAppConfigOnlySwitchWritesTargetFenceWithoutChangingTargetTasks(t *testing.T) {
	dir, _ := prepareConfigOnlySwitchBack(t)
	a := newMaintenanceTestApp(dir)
	a.videoService = services.NewVideoService(nil)
	a.wireBT(dir)
	t.Cleanup(func() {
		a.releaseDatabaseRestoreMode()
		releasePlaybackAfterShutdownTest(a)
		a.stopBT(context.Background())
	})
	if err := a.initializeBTLocal(); err != nil {
		t.Fatal(err)
	}
	target, err := gorm.Open(sqlite.Open(database.SQLiteDSN(database.SQLitePath(dir))), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sql, _ := target.DB(); sql.Close() }()
	connection := models.BTConnection{ID: uuid.NewString(), Slot: "local", Revision: 3}
	if err := target.Create(&connection).Error; err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	task := models.BTTask{ID: id, ConnectionID: connection.ID, Revision: 7, Stage: "active", RootKey: strings.Repeat("b", 64), IntentKey: id, OwnerTag: id}
	if err := target.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	targetScope, err := services.BTCredentialScope(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	sourceScope, err := services.BTCredentialScope(context.Background(), database.DB)
	if err != nil {
		t.Fatal(err)
	}
	if sourceScope == targetScope {
		t.Fatal("fixture did not separate physical targets")
	}
	result, err := a.SwitchBackendConfigOnly("sqlite")
	if err != nil || result == nil || !result.Switched {
		t.Fatal(result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bt-recovery", targetScope+".json")); err != nil {
		t.Fatal("target marker missing", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bt-recovery", sourceScope+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source unnecessarily quarantined", err)
	}
	var after models.BTTask
	if err := target.First(&after, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if after.Stage != "active" || after.Revision != 7 || after.UnresolvedWrite {
		t.Fatal("config-only hook mutated target tasks")
	}
}

func TestBTAppContextDuringStartupIsRaceFree(t *testing.T) {
	a, _ := newBTAppTest(t)
	if err := a.initializeBTLocal(); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); <-start; a.ctx = context.Background() }()
	close(start)
	for i := 0; i < 1000; i++ {
		ctx, cancel, err := a.btContext()
		if err != nil {
			t.Fatal(err)
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		cancel()
	}
	<-done
}

func TestBTAppMaintenanceResumePublishesFirstAndPendingImportToRules(t *testing.T) {
	for _, runtimeReady := range []bool{false, true} {
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("runtime=%t/replay=%t", runtimeReady, replay), func(t *testing.T) {
				a, dir := newBTAppTest(t)
				if runtimeReady {
					a.ctx = context.Background()
				}
				a.shortFeedService = services.NewShortFeedService(a.videoService)
				a.initAutomationRules(dir)
				t.Cleanup(func() { a.stopDataCare(); a.stopFunctionalPolish(false); releasePlaybackAfterShutdownTest(a) })
				if err := a.initializeBTLocal(); err != nil {
					t.Fatal(err)
				}
				download := t.TempDir()
				namespace, err := services.WriteBTNamespaceMarker(download, "fixture")
				if err != nil {
					t.Fatal(err)
				}
				id := uuid.NewString()
				root := filepath.Join(namespace, id)
				if err = os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, "film.mp4")
				if err = os.WriteFile(path, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				old := time.Now().Add(-time.Hour)
				os.Chtimes(path, old, old)
				connection := models.BTConnection{ID: uuid.NewString(), Slot: "local", Revision: 1}
				if err = database.DB.Create(&connection).Error; err != nil {
					t.Fatal(err)
				}
				task := models.BTTask{ID: id, ConnectionID: connection.ID, Revision: 1, AttemptGeneration: 1, Stage: "active", RemoteState: "uploading", RootKey: strings.Repeat("c", 64), IntentKey: id, OwnerTag: id, SaveRoot: root, CanonicalRoot: root}
				if err = database.DB.Create(&task).Error; err != nil {
					t.Fatal(err)
				}
				key := strings.Repeat("d", 64)
				index := 0
				file := models.BTFile{TaskID: id, ManifestFileID: key, PathKey: &key, RelativePath: "film.mp4", Size: 1, Selected: true, Selectable: true, Progress: 1, EligibilityRevision: 1, ImportState: "pending", QBFileIndex: &index}
				if err = database.DB.Create(&file).Error; err != nil {
					t.Fatal(err)
				}
				database.DB.Create(&models.ScanDirectory{Path: filepath.Dir(namespace)})
				tag := models.Tag{Name: "BT fixture rule"}
				if err = database.DB.Create(&tag).Error; err != nil {
					t.Fatal(err)
				}
				if _, err = a.dataCare.rules.SaveAutomationRule(services.AutomationRuleInput{Name: "BT fixture", MediaKind: "video", Enabled: true, Conditions: []services.AutomationRuleCondition{{Type: "under_directory", Path: root}}, Actions: []services.AutomationRuleAction{{Type: "add_tag", TagID: tag.ID}}}); err != nil {
					t.Fatal(err)
				}
				importer := services.NewBTImporter(a.videoService, services.NewBTPathPolicy(nil), services.BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil }))
				if replay {
					a.videoService.SetNewVideoObserver(func(uint) { panic("publication interrupted") })
					interrupted := false
					func() {
						defer func() {
							if recover() != nil {
								interrupted = true
							}
						}()
						importer.Import(context.Background(), id, key)
					}()
					if !interrupted {
						t.Fatal("fixture did not leave pending publication")
					}
					a.videoService.SetNewVideoObserver(a.dataCare.rules.EnqueueVideo)
				}
				if err = a.quiesceBT(context.Background()); err != nil {
					t.Fatal(err)
				}
				a.stopDataCare()
				var triggered atomic.Bool
				accepted := false
				var publicationErr error
				const callback = "test:bt_app_resume_publish"
				if err = database.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table != "bt_tasks" || !triggered.CompareAndSwap(false, true) {
						return
					}
					// Execute a real first publication/replay at BT's recovery query,
					// before initialization can return and open normal admission.
					result, importErr := importer.Import(context.Background(), id, key)
					publicationErr = importErr
					if importErr != nil {
						return
					}
					deadline := time.Now().Add(time.Second)
					for time.Now().Before(deadline) {
						var count int64
						if queryErr := database.DB.Table("video_tags").Where("video_id = ? AND tag_id = ?", result.VideoID, tag.ID).Count(&count).Error; queryErr != nil {
							publicationErr = queryErr
							return
						}
						if count == 1 {
							accepted = true
							return
						}
						time.Sleep(5 * time.Millisecond)
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { database.DB.Callback().Query().Remove(callback) })
				a.resumeAfterDatabaseRestoreFailure()
				database.DB.Callback().Query().Remove(callback)
				if !triggered.Load() || publicationErr != nil || !accepted {
					t.Fatalf("publication reached BT recovery before its rule consumer resumed: triggered=%t accepted=%t err=%v", triggered.Load(), accepted, publicationErr)
				}
				database.DB.First(&file, "task_id = ? AND manifest_file_id = ?", id, key)
				if file.PublicationState != services.BTImportPublicationDone {
					t.Fatal("publication not completed after accepted rule event")
				}
			})
		}
	}
}

func TestBTAppSQLiteSymlinkReplacementConsumesPlannedScopeBeforeAdmission(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("SQLite actual symlink replacement")
	}
	resetQuitGuardForTest(t)
	quits := stubQuitRuntime(t)
	dir := t.TempDir()
	actual := filepath.Join(t.TempDir(), "physical.db")
	alias := filepath.Join(dir, "configured.db")
	first, err := gorm.Open(sqlite.Open(database.SQLiteDSN(actual)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := first.DB()
	sql.Close()
	if err = os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_BACKEND", "sqlite")
	t.Setenv("SQLITE_PATH", alias)
	db := openAppLiveDatabase(t, func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(database.SQLiteDSN(alias)), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		return db
	})
	a := newMaintenanceTestApp(dir)
	a.videoService = services.NewVideoService(nil)
	a.wireBT(dir)
	t.Cleanup(func() {
		a.releaseDatabaseRestoreMode()
		releasePlaybackAfterShutdownTest(a)
		a.stopBT(context.Background())
	})
	task := seedBTAppTask(t, "active")
	if err = a.initializeBTLocal(); err != nil {
		t.Fatal(err)
	}
	oldScope, err := services.BTCredentialScope(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := a.CreateDatabaseBackup()
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&models.BTTask{}).Where("id = ?", task.ID).Update("revision", 99).Error; err != nil {
		t.Fatal(err)
	}
	if err = a.RestoreDatabaseBackup(services.BackupRestoreRequest{Name: backup.Name, Size: backup.Size, Fingerprint: backup.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	if !waitQuit(t, quits) {
		t.Fatal("restore did not finish through internal quit")
	}
	info, err := os.Lstat(alias)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("existing replace behavior changed", err)
	}
	a.releaseDatabaseRestoreMode()
	releasePlaybackAfterShutdownTest(a)
	restored, err := gorm.Open(sqlite.Open(database.SQLiteDSN(alias)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = restored
	t.Cleanup(func() { sql, _ := restored.DB(); sql.Close() })
	if err = database.ApplySchema(restored); err != nil {
		t.Fatal(err)
	}
	newScope, err := services.BTCredentialScope(context.Background(), restored)
	if err != nil || newScope == oldScope {
		t.Fatal("fixture did not change physical scope", err)
	}
	restarted := &App{videoService: services.NewVideoService(nil), settingsService: &services.SettingsService{}}
	restarted.wireBT(dir)
	t.Cleanup(func() { restarted.stopBT(context.Background()) })
	if err = restarted.initializeBTLocal(); err != nil {
		t.Fatal(err)
	}
	view, err := restarted.GetBTTask(task.ID)
	if err != nil || view.Stage != "uncertain" || !view.UnresolvedWrite || view.PendingAction != "restore" {
		t.Fatal("restored snapshot escaped planned-scope quarantine", view, err)
	}
	if _, err = os.Stat(filepath.Join(dir, "bt-recovery", newScope+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new scope marker not consumed", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "bt-recovery", oldScope+".json")); err != nil {
		t.Fatal("old physical scope marker erased", err)
	}
	original, err := gorm.Open(sqlite.Open(database.SQLiteDSN(actual)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sql, _ := original.DB(); sql.Close() }()
	var retained models.BTTask
	if err = original.First(&retained, "id = ?", task.ID).Error; err != nil || retained.Revision != 99 {
		t.Fatal("backup replacement changed original symlink target", err)
	}
}
