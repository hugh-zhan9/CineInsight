package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
	"video-master/services"

	"github.com/joho/godotenv"
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

// APP02 / m2：退出在预检进行中到来（shutdown 先调 cancelDatabaseSwitchForShutdown，再等 restoreMu）。
// 取消带状态：预检之后复查到「正在关闭」就不再发起迁移、立即放锁，shutdown 不用等一整次迁移。
// 预检期间的取消与预检之前的取消对这次复查是同一回事（标志置位后不复位），这里在发起前调用。
func TestAPP02StartDatabaseSwitchCancelledDuringPreflightDoesNotStartAndReleasesLock(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DB_BACKEND", "postgres")
	t.Setenv("SQLITE_PATH", "")
	openAppLiveDatabase(t, func() *gorm.DB { return dbtest.OpenRaw(t) })
	app := newMaintenanceTestApp(dataDir)
	t.Cleanup(app.releaseDatabaseRestoreMode)
	var statusMu sync.Mutex
	var statuses []services.DatabaseSwitchStatus
	app.databaseSwitchService.SetProgressSink(func(status services.DatabaseSwitchStatus) {
		statusMu.Lock()
		statuses = append(statuses, status)
		statusMu.Unlock()
	})

	app.cancelDatabaseSwitchForShutdown()
	err := app.StartDatabaseSwitch("sqlite")
	if !errors.Is(err, services.ErrDatabaseSwitchCancelledForShutdown) {
		t.Fatalf("正在关闭时发起切换应按取消处理: %v", err)
	}
	if !app.restoreMu.TryLock() {
		t.Fatal("预检之后应立即释放 restoreMu，不能让 shutdown 等迁移")
	}
	app.restoreMu.Unlock()
	statusMu.Lock()
	published := len(statuses)
	statusMu.Unlock()
	if published != 0 {
		t.Fatalf("迁移不应开始（后台 goroutine 不应起来）: %#v", statuses)
	}
	if database.MaintenanceActive() || app.restoreRelease != nil || app.restoreTerminal || app.databaseSwitchService.RelaunchPending() {
		t.Fatal("迁移没开始，不应进维护模式或待重启")
	}
	if _, err := os.Stat(filepath.Join(dataDir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("不应写后端配置: %v", err)
	}
}

// APP02 / m11：App 层「切回之前的后端」的接线——与恢复、迁移共用 restoreMu 且用 TryLock（被占用时立即
// 拒绝、不排队）；成功时经 enterDatabaseRestoreMode 立起维护围栏并停在「待重启」终态。改成传 nil 生命周期
// 或改用 Lock 都会让这条用例变红。
// prepareConfigOnlySwitchBack 让当前后端是 postgres（源库是 dbtest 的库），上一个后端（SQLite）的库
// 非空、配置里记着它：「切回之前的后端」的前提。返回数据目录与后端配置文件路径。
func prepareConfigOnlySwitchBack(t *testing.T) (string, string) {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("DB_BACKEND", "postgres")
	t.Setenv("SQLITE_PATH", "")
	openAppLiveDatabase(t, func() *gorm.DB { return dbtest.OpenRaw(t) })
	target, err := gorm.Open(sqlite.Open(database.SQLiteDSN(database.SQLitePath(dataDir))), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := target.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatal(err)
	}
	if err := target.Create(&models.Video{Name: "old.mp4", Path: "/old/old.mp4", Directory: "/old"}).Error; err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := target.DB(); err == nil {
		_ = sqlDB.Close()
	}
	configPath := filepath.Join(dataDir, ".env")
	if err := os.WriteFile(configPath, []byte("DB_BACKEND=postgres\nPREVIOUS_BACKEND=sqlite\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dataDir, configPath
}

func TestAPP02SwitchBackendConfigOnlyWiresMaintenanceAndRejectsWhileBusy(t *testing.T) {
	dataDir, configPath := prepareConfigOnlySwitchBack(t)
	app := newMaintenanceTestApp(dataDir)
	t.Cleanup(app.releaseDatabaseRestoreMode)

	// 恢复或迁移正持有 restoreMu：立即拒绝。
	app.restoreMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := app.SwitchBackendConfigOnly("sqlite")
		done <- err
	}()
	select {
	case err := <-done:
		app.restoreMu.Unlock()
		if !errors.Is(err, errDatabaseMaintenanceBusy) {
			t.Fatalf("restoreMu 被占用时应立即拒绝: %v", err)
		}
	case <-time.After(2 * time.Second):
		app.restoreMu.Unlock()
		<-done
		t.Fatal("restoreMu 被占用时应立即拒绝，而不是排队等锁")
	}
	if values, err := godotenv.Read(configPath); err != nil || values["DB_BACKEND"] != "postgres" {
		t.Fatalf("被拒绝时配置不应改动: %#v err=%v", values, err)
	}
	if database.MaintenanceActive() {
		t.Fatal("被拒绝时不应进维护模式")
	}

	result, err := app.SwitchBackendConfigOnly("sqlite")
	if err != nil || result == nil || !result.Switched || !result.RelaunchRequired {
		t.Fatalf("切回上一个后端应成功: %#v err=%v", result, err)
	}
	if !database.MaintenanceActive() || app.restoreRelease == nil || !app.restoreTerminal || !app.databaseSwitchService.RelaunchPending() {
		t.Fatalf("成功后应经维护模式入口立起围栏并停在待重启: fenced=%v release=%v terminal=%v",
			database.MaintenanceActive(), app.restoreRelease != nil, app.restoreTerminal)
	}
	if err := database.DB.Create(&models.Tag{Name: "切回后写入", Color: "#000000"}).Error; !errors.Is(err, database.ErrMaintenance) {
		t.Fatalf("待重启期间写入必须被拒绝: %v", err)
	}
	if values, err := godotenv.Read(configPath); err != nil || values["DB_BACKEND"] != "sqlite" || values["PREVIOUS_BACKEND"] != "postgres" {
		t.Fatalf("配置应改为切回的后端: %#v err=%v", values, err)
	}
	// I-1：WebView 重载后前端靠 GetDatabaseSwitchStatus 补读「待重启」，只改配置同样要读得到。
	if status := app.GetDatabaseSwitchStatus(); !status.Completed || !status.RelaunchRequired || status.Target != "sqlite" {
		t.Fatalf("只改配置成功后应能补读到待重启终态: %#v", status)
	}
	// I-2：提示条要说「已切换到哪个」：Backend 仍是本进程的旧后端，NextBackend 是切换到的后端。
	if backend := app.GetDatabaseBackendStatus(); backend.Backend != "postgres" || backend.NextBackend != "sqlite" {
		t.Fatalf("后端状态应报下次启动的后端: %#v", backend)
	}
}

// APP02 / m7：ConfigureJellyfin 与恢复、切换共用 restoreMu 但用 TryLock——那两件事可能跑很久，设置页的
// 保存不能一直挂着等，拿不到锁立即拒绝；「待重启」终态（切换或切回成功）报「后端已切换，请先重启应用」，
// 恢复备份之后的终态仍报恢复口径。
func TestAPP02ConfigureJellyfinDoesNotWaitForMaintenanceAndReportsRelaunchPending(t *testing.T) {
	dataDir, _ := prepareConfigOnlySwitchBack(t)
	app := newMaintenanceTestApp(dataDir)
	t.Cleanup(app.releaseDatabaseRestoreMode)
	input := services.JellyfinConfigInput{Enabled: false}

	app.restoreMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := app.ConfigureJellyfin(input)
		done <- err
	}()
	select {
	case err := <-done:
		app.restoreMu.Unlock()
		if !errors.Is(err, errDatabaseMaintenanceBusy) || err.Error() != "数据库恢复或切换正在进行，请稍后再试" {
			t.Fatalf("恢复或切换进行中应立即拒绝: %v", err)
		}
	case <-time.After(2 * time.Second):
		app.restoreMu.Unlock()
		<-done
		t.Fatal("restoreMu 被占用时应立即拒绝，而不是排队等锁")
	}

	app.restoreTerminal = true
	if _, err := app.ConfigureJellyfin(input); err == nil || err.Error() != "数据库恢复后请先重启应用" {
		t.Fatalf("恢复备份之后的终态沿用恢复口径: %v", err)
	}
	app.restoreTerminal = false

	if result, err := app.SwitchBackendConfigOnly("sqlite"); err != nil || !result.Switched {
		t.Fatalf("切回上一个后端应成功: %#v err=%v", result, err)
	}
	if _, err := app.ConfigureJellyfin(input); !errors.Is(err, errJellyfinRelaunchPending) || err.Error() != "后端已切换，请先重启应用" {
		t.Fatalf("待重启时应报「后端已切换，请先重启应用」: %v", err)
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
