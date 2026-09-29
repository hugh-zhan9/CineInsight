package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
	"video-master/services"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// P-027：App 层的恢复与切换都经 enterDatabaseRestoreMode 进出维护模式。
// 这里的 App 只装本切片用到的服务：enterDatabaseRestoreMode / resume 对其余服务都判了 nil。

func newMaintenanceTestApp(dataDir string) *App {
	return &App{
		ctx:                   context.Background(),
		backupService:         services.NewBackupService(dataDir),
		databaseSwitchService: services.NewDatabaseSwitchService(dataDir),
		settingsService:       &services.SettingsService{},
		shortFeedService:      services.NewShortFeedService(services.NewVideoService(services.NewMediaProbeService())),
		backgroundTasks:       services.NewBackgroundTaskRegistry(),
	}
}

func openAppLiveDatabase(t *testing.T, open func() *gorm.DB) *gorm.DB {
	t.Helper()
	db := open()
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema 失败: %v", err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		database.DB = previous
	})
	return db
}

// APP-01：SQLite 上经 App 完整走一次恢复：数据回到备份点、结果是成功而不是「必须重启」的错误，
// 并以内部退出收尾（先放行、再 Quit）。
func TestAPP01RestoreDatabaseBackupOnSQLiteRestoresAndQuitsInternally(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("本用例针对 SQLite 后端的库文件恢复")
	}
	resetQuitGuardForTest(t)
	quits := stubQuitRuntime(t)
	dataDir := t.TempDir()
	t.Setenv("DB_BACKEND", "sqlite")
	t.Setenv("SQLITE_PATH", "")
	livePath := database.SQLitePath(dataDir)
	db := openAppLiveDatabase(t, func() *gorm.DB {
		opened, err := gorm.Open(sqlite.Open(database.SQLiteDSN(livePath)), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		return opened
	})
	if err := db.Create(&models.Video{Name: "before.mp4", Path: filepath.Join(dataDir, "before.mp4"), Directory: dataDir}).Error; err != nil {
		t.Fatal(err)
	}
	app := newMaintenanceTestApp(dataDir)
	t.Cleanup(app.releaseDatabaseRestoreMode)

	backup, err := app.CreateDatabaseBackup()
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if err := db.Model(&models.Video{}).Where("name = ?", "before.mp4").Update("name", "after.mp4").Error; err != nil {
		t.Fatal(err)
	}

	err = app.RestoreDatabaseBackup(services.BackupRestoreRequest{Name: backup.Name, Size: backup.Size, Fingerprint: backup.Fingerprint})
	if err != nil {
		t.Fatalf("SQLite 恢复应成功: %v", err)
	}
	if allowed := waitQuit(t, quits); !allowed {
		t.Fatal("恢复完成后的退出必须先 allowInternalQuit")
	}
	if !app.restoreTerminal {
		t.Fatal("恢复完成后应进入终态，拒绝再次恢复或切换")
	}

	restored, err := gorm.Open(sqlite.Open(database.SQLiteDSN(livePath)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if sqlDB, err := restored.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	var names []string
	if err := restored.Model(&models.Video{}).Pluck("name", &names).Error; err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "before.mp4" {
		t.Fatalf("数据应回到备份点: %v", names)
	}
}

// PLAY-01：离开维护模式时手机端服务按开关决定是否重新监听。此前恢复失败的续跑路径无条件
// startShortFeedServer，用户关掉的手机端访问会被重新打开。
func TestPLAY01ResumeAfterDatabaseMaintenanceKeepsDisabledShortFeedOff(t *testing.T) {
	openAppLiveDatabase(t, func() *gorm.DB { return dbtest.OpenRaw(t) })
	settings, err := (&services.SettingsService{}).GetSettings()
	if err != nil || settings.ShortFeedEnabled {
		t.Fatalf("前置：新库的手机端访问默认关闭: %+v err=%v", settings, err)
	}
	app := newMaintenanceTestApp(t.TempDir())
	app.resumeAfterDatabaseRestoreFailure()
	if app.shortFeedServer != nil {
		t.Fatal("手机端访问关闭时，离开维护模式不应启动手机端服务")
	}
}

// waitRestoreMuReleased 等后台切换 goroutine 结束（它全程持有 restoreMu）。
func waitRestoreMuReleased(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !app.restoreMu.TryLock() {
		if time.Now().After(deadline) {
			t.Fatal("切换结束后应释放 restoreMu")
		}
		time.Sleep(10 * time.Millisecond)
	}
	app.restoreMu.Unlock()
}

// APP-02：经 App 发起的切换在与恢复相同的维护模式下进行，但不关连接；结束后释放 restoreMu，
// 成功终态要求重启。I-1 之后成功**不**撤围栏：进入「待重启」终态（此前这里钉的是「离开维护模式
// 后应恢复写入」），写入被拒绝，恢复、再次切换、只改配置、清空目标库都返回 relaunch_pending。
func TestAPP02StartDatabaseSwitchUsesRestoreMaintenanceWithoutClosingConnection(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DB_BACKEND", "postgres")
	t.Setenv("SQLITE_PATH", "")
	db := openAppLiveDatabase(t, func() *gorm.DB { return dbtest.OpenRaw(t) })
	if err := db.Create(&models.Video{Name: "switch.mp4", Path: "/lib/switch.mp4", Directory: "/lib"}).Error; err != nil {
		t.Fatal(err)
	}
	app := newMaintenanceTestApp(dataDir)
	t.Cleanup(app.releaseDatabaseRestoreMode)

	if err := app.StartDatabaseSwitch("sqlite"); err != nil {
		t.Fatalf("发起切换失败: %v", err)
	}
	waitRestoreMuReleased(t, app)

	status := app.GetDatabaseSwitchStatus()
	if !status.Completed || !status.RelaunchRequired {
		t.Fatalf("切换应成功并要求重启: %#v", status)
	}
	if !database.MaintenanceActive() || app.restoreRelease == nil || !app.restoreTerminal {
		t.Fatalf("切换成功后应保持维护围栏、进入待重启终态: fenced=%v release=%v terminal=%v",
			database.MaintenanceActive(), app.restoreRelease != nil, app.restoreTerminal)
	}
	var count int64
	if err := database.WithMaintenanceAccess(database.DB).Model(&models.Video{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("切换不应关闭当前连接: count=%d err=%v", count, err)
	}
	if err := database.DB.Create(&models.Tag{Name: "切换后写入", Color: "#000000"}).Error; !errors.Is(err, database.ErrMaintenance) {
		t.Fatalf("待重启期间写入必须被拒绝（否则会落进旧库、重启后消失）: %v", err)
	}

	restoreErr := app.RestoreDatabaseBackup(services.BackupRestoreRequest{Name: "cineinsight-20260929-120000.000000000.dump", Size: 1, Fingerprint: "x"})
	if !errors.Is(restoreErr, services.ErrDatabaseRelaunchPending) {
		t.Fatalf("待重启时恢复备份应返回 relaunch_pending: %v", restoreErr)
	}
	if err := app.StartDatabaseSwitch("sqlite"); !errors.Is(err, services.ErrDatabaseRelaunchPending) {
		t.Fatalf("待重启时再次切换应返回 relaunch_pending: %v", err)
	}
	if result, err := app.SwitchBackendConfigOnly("sqlite"); err != nil || result.ReasonCode != services.DatabaseSwitchReasonRelaunchPending {
		t.Fatalf("待重启时只改配置应被拒绝: %#v err=%v", result, err)
	}
	if result, err := app.ClearMigrationTarget("sqlite", services.ClearMigrationTargetConfirmText); err != nil || result.ReasonCode != services.DatabaseSwitchReasonRelaunchPending {
		t.Fatalf("待重启时清空目标库应被拒绝: %#v err=%v", result, err)
	}
	if !database.MaintenanceActive() {
		t.Fatal("被拒绝的操作不得撤掉维护围栏")
	}
}

// APP02 / I-1 / M-3：切换进行中调用恢复立即被拒绝（不排队等切换结束）；退出时的取消按失败处理：
// 撤围栏、恢复写入、不进入待重启。
func TestAPP02RestoreDuringSwitchIsRejectedAndCancelledSwitchResumesWrites(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DB_BACKEND", "postgres")
	t.Setenv("SQLITE_PATH", "")
	db := openAppLiveDatabase(t, func() *gorm.DB { return dbtest.OpenRaw(t) })
	if err := db.Create(&models.Video{Name: "switch.mp4", Path: "/lib/switch.mp4", Directory: "/lib"}).Error; err != nil {
		t.Fatal(err)
	}
	app := newMaintenanceTestApp(dataDir)
	t.Cleanup(app.releaseDatabaseRestoreMode)

	restoreErrs := make(chan error, 1)
	var once sync.Once
	app.databaseSwitchService.SetProgressSink(func(status services.DatabaseSwitchStatus) {
		if !status.Running || status.Table == "" {
			return
		}
		once.Do(func() {
			done := make(chan error, 1)
			go func() {
				done <- app.RestoreDatabaseBackup(services.BackupRestoreRequest{Name: "cineinsight-20260929-120000.000000000.dump", Size: 1, Fingerprint: "x"})
			}()
			select {
			case err := <-done:
				restoreErrs <- err
			case <-time.After(2 * time.Second):
				restoreErrs <- errors.New("恢复在排队等切换结束，而不是立即被拒绝")
			}
			app.cancelDatabaseSwitchForShutdown()
		})
	})

	if err := app.StartDatabaseSwitch("sqlite"); err != nil {
		t.Fatalf("发起切换失败: %v", err)
	}
	waitRestoreMuReleased(t, app)
	if err := <-restoreErrs; !errors.Is(err, errDatabaseMaintenanceBusy) {
		t.Fatalf("切换进行中恢复应立即被拒绝: %v", err)
	}
	status := app.GetDatabaseSwitchStatus()
	if !status.Failed || status.RelaunchRequired {
		t.Fatalf("取消的切换应以失败收尾: %#v", status)
	}
	if database.MaintenanceActive() || app.restoreRelease != nil || app.restoreTerminal || app.databaseSwitchService.RelaunchPending() {
		t.Fatal("切换失败后应离开维护模式、不进入待重启")
	}
	if err := database.DB.Create(&models.Tag{Name: "失败后写入", Color: "#000000"}).Error; err != nil {
		t.Fatalf("切换失败后应恢复写入: %v", err)
	}
}
