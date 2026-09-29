package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"
	"video-master/database/migrator"
	"video-master/internal/dbtest"
	"video-master/models"

	"github.com/joho/godotenv"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ===== P-027 后端切换（D-PC55 / APP-02）=====
//
// 这些用例把「当前后端」设成 postgres、把 SQLite 文件当作迁移目标：源库是 dbtest 的库
// （默认 SQLite，设了 CINEINSIGHT_TEST_PG_DSN 时是 Postgres 的独立 schema），迁移器不看
// 源库方言，目标是数据目录里的真实 library.db。这样双后端都能跑，也不碰任何共享库。

// openSwitchSource 打开源库、跑完 ApplySchema（含维护屏障回调），挂成 database.DB，并造一条视频与标签关联。
func openSwitchSource(t *testing.T) (*gorm.DB, models.Video) {
	t.Helper()
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("源库 ApplySchema 失败: %v", err)
	}
	previousDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previousDB })
	tag := models.Tag{Name: "切换前标签", Color: "#0f8f82"}
	if err := db.Create(&tag).Error; err != nil {
		t.Fatalf("建标签失败: %v", err)
	}
	video := models.Video{Name: "switch.mp4", Path: "/lib/switch.mp4", Directory: "/lib"}
	if err := db.Create(&video).Error; err != nil {
		t.Fatalf("建视频失败: %v", err)
	}
	if err := db.Model(&video).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("挂标签失败: %v", err)
	}
	return db, video
}

func useSQLiteAsSwitchTarget(t *testing.T) string {
	t.Helper()
	t.Setenv("DB_BACKEND", "postgres")
	t.Setenv("SQLITE_PATH", "")
	return t.TempDir()
}

func openSQLiteFile(t *testing.T, path string) (*gorm.DB, func()) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(database.SQLiteDSN(path)), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 SQLite 文件失败: %v", err)
	}
	return db, func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
}

// makeHalfMigratedTarget 用一个已取消的 ctx 调迁移器：标记（未完成）与表都已建好、数据还没搬，
// 正是「迁移中途失败」留下的目标库形态。
func makeHalfMigratedTarget(t *testing.T, source, target *gorm.DB, backend database.Backend) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := migrator.Migrate(ctx, migrator.Options{Source: source, Target: target, TargetBackend: backend}); !errors.Is(err, context.Canceled) {
		t.Fatalf("构造半迁移目标库失败: %v", err)
	}
	if err := migrator.Preflight(target); !errors.Is(err, migrator.ErrTargetHalfMigrated) {
		t.Fatalf("前置：目标库应处于半迁移状态: %v", err)
	}
}

func readSwitchConfig(t *testing.T, dataDir string) map[string]string {
	t.Helper()
	values, err := godotenv.Read(filepath.Join(dataDir, ".env"))
	if err != nil {
		t.Fatalf("读取后端配置失败: %v", err)
	}
	return values
}

func containsName(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// APP-02：迁移在维护模式下进行——迁移期间写入被拒绝，迁移器仍能经维护通道读完源库；
// 成功终态带 relaunch_required，配置里记下上一个后端。成功后**不**撤围栏（I-1，见
// TestAPP02SwitchSuccessKeepsMaintenanceFenceAndRejectsFurtherActions）。
func TestAPP02SwitchRunsUnderMaintenanceAndRejectsWrites(t *testing.T) {
	dataDir := useSQLiteAsSwitchTarget(t)
	_, video := openSwitchSource(t)
	service := NewDatabaseSwitchService(dataDir)

	var writeErrs []error
	var statuses []DatabaseSwitchStatus
	service.SetProgressSink(func(status DatabaseSwitchStatus) {
		statuses = append(statuses, status)
		if status.Running && status.Table != "" && len(writeErrs) == 0 {
			writeErrs = append(writeErrs,
				database.DB.Create(&models.Tag{Name: "迁移中写入", Color: "#000000"}).Error,
				database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Update("name", "迁移中改名.mp4").Error,
				database.Transaction(func(tx *gorm.DB) error { return nil }),
			)
		}
	})
	var release func()
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	entered, left := 0, 0
	err := service.SwitchWithLifecycle(context.Background(), "sqlite",
		func() error { entered++; release = database.BeginMaintenance(); return nil },
		func() { left++; release() })
	if err != nil {
		t.Fatalf("维护模式下的迁移应成功: %v", err)
	}
	// I-1 之后成功不再离开维护模式（此前钉的是 entered=1 left=1）。
	if entered != 1 || left != 0 {
		t.Fatalf("进入维护模式一次、成功后不离开: entered=%d left=%d", entered, left)
	}
	if len(writeErrs) != 3 {
		t.Fatalf("迁移过程中应尝试过写入: %v", writeErrs)
	}
	for _, writeErr := range writeErrs {
		if !errors.Is(writeErr, database.ErrMaintenance) {
			t.Fatalf("迁移期间写入必须被拒绝: %v", writeErr)
		}
	}

	final := service.SwitchStatus()
	if !final.Completed || !final.RelaunchRequired || final.Running {
		t.Fatalf("成功终态应带 relaunch_required: %#v", final)
	}
	if last := statuses[len(statuses)-1]; !last.RelaunchRequired {
		t.Fatalf("推给前端的终态事件也应带 relaunch_required: %#v", last)
	}
	config := readSwitchConfig(t, dataDir)
	if config["DB_BACKEND"] != "sqlite" || config["PREVIOUS_BACKEND"] != "postgres" {
		t.Fatalf("配置应写入目标后端与上一个后端: %#v", config)
	}

	target, closeTarget := openSQLiteFile(t, database.SQLitePath(dataDir))
	defer closeTarget()
	var names []string
	if err := target.Model(&models.Video{}).Pluck("name", &names).Error; err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "switch.mp4" {
		t.Fatalf("目标库应拿到迁移前的数据、且没有迁移中的改动: %v", names)
	}
	var sneaked int64
	if err := target.Model(&models.Tag{}).Where("name = ?", "迁移中写入").Count(&sneaked).Error; err != nil || sneaked != 0 {
		t.Fatalf("迁移中写入不应落进目标库: count=%d err=%v", sneaked, err)
	}
}

// APP02 / I-1：切换成功后维护围栏保持到重启——此后落进旧库的写入重启后就「消失」了。
// 写入仍被拒绝；再次迁移切换、只改配置、清空目标库（那正是下次启动要用的库）一律 relaunch_pending。
func TestAPP02SwitchSuccessKeepsMaintenanceFenceAndRejectsFurtherActions(t *testing.T) {
	dataDir := useSQLiteAsSwitchTarget(t)
	source, _ := openSwitchSource(t)
	service := NewDatabaseSwitchService(dataDir)
	var release func()
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	left := 0
	enter := func() error { release = database.BeginMaintenance(); return nil }
	if err := service.SwitchWithLifecycle(context.Background(), "sqlite", enter, func() { left++; release() }); err != nil {
		t.Fatalf("切换应成功: %v", err)
	}
	if left != 0 || !database.MaintenanceActive() || !service.RelaunchPending() {
		t.Fatalf("成功后应保持围栏并进入待重启: left=%d fenced=%v pending=%v", left, database.MaintenanceActive(), service.RelaunchPending())
	}
	if err := source.Create(&models.Tag{Name: "切换后写入", Color: "#000000"}).Error; !errors.Is(err, database.ErrMaintenance) {
		t.Fatalf("待重启期间写入必须被拒绝: %v", err)
	}
	if err := database.Transaction(func(tx *gorm.DB) error { return nil }); !errors.Is(err, database.ErrMaintenance) {
		t.Fatalf("待重启期间事务必须被拒绝: %v", err)
	}

	entered := 0
	err := service.SwitchWithLifecycle(context.Background(), "sqlite",
		func() error { entered++; return nil }, func() { left++ })
	if !errors.Is(err, ErrDatabaseRelaunchPending) || !strings.HasPrefix(err.Error(), DatabaseSwitchReasonRelaunchPending) {
		t.Fatalf("待重启时再次切换应返回 relaunch_pending: %v", err)
	}
	if entered != 0 || left != 0 {
		t.Fatalf("被拒绝的切换不得进出维护模式: entered=%d left=%d", entered, left)
	}
	configOnly, err := service.SwitchBackendConfigOnlyWithLifecycle("sqlite", nil, nil)
	if err != nil || configOnly.Switched || configOnly.ReasonCode != DatabaseSwitchReasonRelaunchPending {
		t.Fatalf("待重启时只改配置应被拒绝: %#v err=%v", configOnly, err)
	}
	cleared, err := service.ClearMigrationTarget("sqlite", ClearMigrationTargetConfirmText)
	if err != nil || cleared.Cleared || cleared.ReasonCode != DatabaseSwitchReasonRelaunchPending {
		t.Fatalf("待重启时清空目标库应被拒绝: %#v err=%v", cleared, err)
	}
	if _, err := os.Stat(database.SQLitePath(dataDir)); err != nil {
		t.Fatalf("刚迁移完、重启后要用的库文件不得被删除: %v", err)
	}
	if config := readSwitchConfig(t, dataDir); config["DB_BACKEND"] != "sqlite" || config["PREVIOUS_BACKEND"] != "postgres" {
		t.Fatalf("被拒绝的操作不得改动配置: %#v", config)
	}
	if final := service.SwitchStatus(); !final.Completed || !final.RelaunchRequired {
		t.Fatalf("终态应仍是完成、要求重启: %#v", final)
	}
}

// APP02 / M-3：迁移可以取消（退出应用时由 shutdown 调用）。取消按失败处理：离开维护模式、
// 源库恢复可写、不写配置；目标库留为半迁移，清空后可以重试。
func TestAPP02CancelRunningSwitchFailsAndLeavesMaintenance(t *testing.T) {
	dataDir := useSQLiteAsSwitchTarget(t)
	source, _ := openSwitchSource(t)
	service := NewDatabaseSwitchService(dataDir)
	// m2 之后取消带状态（置位「正在关闭」后不再复位），这里不再先空调一次 CancelRunningSwitch；
	// 「没有进行中的迁移时返回 false」由 TestAPP02CancelBeforeMigrationRegistersKeepsSwitchFromStarting 覆盖。
	cancelled := false
	service.SetProgressSink(func(status DatabaseSwitchStatus) {
		if status.Running && status.Table != "" && !cancelled {
			cancelled = service.CancelRunningSwitch()
		}
	})
	var release func()
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	left := 0
	err := service.SwitchWithLifecycle(context.Background(), "sqlite",
		func() error { release = database.BeginMaintenance(); return nil },
		func() { left++; release() })
	if !cancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后迁移应以 context.Canceled 失败: cancelled=%v err=%v", cancelled, err)
	}
	if left != 1 || database.MaintenanceActive() || service.RelaunchPending() {
		t.Fatalf("取消按失败处理：离开维护模式、不进入待重启: left=%d fenced=%v pending=%v", left, database.MaintenanceActive(), service.RelaunchPending())
	}
	if err := source.Create(&models.Tag{Name: "取消后写入", Color: "#000000"}).Error; err != nil {
		t.Fatalf("取消后源库应恢复可写: %v", err)
	}
	failed := service.SwitchStatus()
	if !failed.Failed || failed.RelaunchRequired || !strings.Contains(failed.Message, "已取消") {
		t.Fatalf("失败状态应说明已取消: %#v", failed)
	}
	if _, err := os.Stat(filepath.Join(dataDir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("取消的迁移不应写后端配置: %v", err)
	}
	if service.CancelRunningSwitch() {
		t.Fatal("迁移结束后不应还有可取消的迁移")
	}
	target, closeTarget := openSQLiteFile(t, database.SQLitePath(dataDir))
	if err := migrator.Preflight(target); !errors.Is(err, migrator.ErrTargetHalfMigrated) {
		closeTarget()
		t.Fatalf("取消后目标库应是半迁移状态: %v", err)
	}
	closeTarget()
	if cleared, err := service.ClearMigrationTarget("sqlite", ClearMigrationTargetConfirmText); err != nil || !cleared.Cleared {
		t.Fatalf("取消后应能清空目标库: %#v err=%v", cleared, err)
	}
	// 取消只在退出时调用、带状态（m2）：本进程之后不再开始迁移，重试发生在下次启动——新的服务实例。
	if err := service.SwitchWithLifecycle(context.Background(), "sqlite", nil, nil); !errors.Is(err, ErrDatabaseSwitchCancelledForShutdown) {
		t.Fatalf("取消之后同一进程不应再开始迁移: %v", err)
	}
	relaunched := NewDatabaseSwitchService(dataDir)
	if err := relaunched.SwitchWithLifecycle(context.Background(), "sqlite",
		func() error { release = database.BeginMaintenance(); return nil },
		func() { release() }); err != nil {
		t.Fatalf("清空后重试应成功: %v", err)
	}
}

// APP-02：迁移失败（目标库残留半迁移）时同样离开维护模式、不改配置，失败状态带目标库位置；
// 输对确认文字清空目标库后可以重试成功。
func TestAPP02ClearHalfMigratedSQLiteTargetThenRetrySucceeds(t *testing.T) {
	dataDir := useSQLiteAsSwitchTarget(t)
	source, _ := openSwitchSource(t)
	targetPath := database.SQLitePath(dataDir)
	target, closeTarget := openSQLiteFile(t, targetPath)
	makeHalfMigratedTarget(t, source, target, database.BackendSQLite)
	closeTarget()

	service := NewDatabaseSwitchService(dataDir)
	preflight, err := service.Preflight("sqlite")
	if err != nil || preflight.Empty || preflight.ReasonCode != "not_empty" {
		t.Fatalf("半迁移目标库预检应不通过: %#v err=%v", preflight, err)
	}

	var release func()
	// 最后一次重试成功后围栏保持（I-1），测试结束时撤掉，免得挡住后面的用例。
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	left := 0
	err = service.SwitchWithLifecycle(context.Background(), "sqlite",
		func() error { release = database.BeginMaintenance(); return nil },
		func() { left++; release() })
	if !errors.Is(err, migrator.ErrTargetHalfMigrated) {
		t.Fatalf("向半迁移目标库迁移应失败: %v", err)
	}
	if left != 1 {
		t.Fatalf("迁移失败也必须离开维护模式: left=%d", left)
	}
	if database.MaintenanceActive() || service.RelaunchPending() {
		t.Fatalf("失败后维护围栏应已撤掉、不进入待重启")
	}
	// APP02 / I-1：失败后源库恢复可写。
	if err := source.Create(&models.Tag{Name: "失败后写入", Color: "#000000"}).Error; err != nil {
		t.Fatalf("迁移失败后应恢复写入: %v", err)
	}
	failed := service.SwitchStatus()
	if !failed.Failed || failed.Location != targetPath || failed.RelaunchRequired {
		t.Fatalf("失败状态应带目标库位置、不要求重启: %#v", failed)
	}
	if _, err := os.Stat(filepath.Join(dataDir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("迁移失败不应写后端配置: %v", err)
	}

	wrong, err := service.ClearMigrationTarget("sqlite", "清除")
	if err != nil || wrong.Cleared || wrong.ReasonCode != "confirm_mismatch" {
		t.Fatalf("确认文字不对应拒绝: %#v err=%v", wrong, err)
	}
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("确认文字不对时不得删除目标库: %v", err)
	}

	cleared, err := service.ClearMigrationTarget("sqlite", ClearMigrationTargetConfirmText)
	if err != nil || !cleared.Cleared || !containsName(cleared.Removed, filepath.Base(targetPath)) {
		t.Fatalf("清空目标库失败: %#v err=%v", cleared, err)
	}
	if cleared.Location != targetPath {
		t.Fatalf("结果应带目标库位置: %#v", cleared)
	}
	for _, name := range cleared.Removed {
		if strings.Contains(name, string(filepath.Separator)) {
			t.Fatalf("清空结果只列文件名: %v", cleared.Removed)
		}
	}
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("目标库文件应已删除: %v", err)
	}
	preflight, err = service.Preflight("sqlite")
	if err != nil || !preflight.Empty {
		t.Fatalf("清空后预检应通过: %#v err=%v", preflight, err)
	}

	if err := service.SwitchWithLifecycle(context.Background(), "sqlite",
		func() error { release = database.BeginMaintenance(); return nil },
		func() { release() }); err != nil {
		t.Fatalf("清空后重试迁移应成功: %v", err)
	}
	retried, closeRetried := openSQLiteFile(t, targetPath)
	defer closeRetried()
	var count int64
	if err := retried.Model(&models.Video{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("重试后目标库应有数据: count=%d err=%v", count, err)
	}
}

// APP-02：当前正在用的库永远不能清空；SQLITE_PATH 指到数据目录外时也不删。
func TestAPP02ClearMigrationTargetRefusesActiveBackendAndOutsideDataDir(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SQLITE_PATH", "")
	t.Setenv("DB_BACKEND", "sqlite")
	livePath := database.SQLitePath(dataDir)
	if err := os.WriteFile(livePath, []byte("live library"), 0600); err != nil {
		t.Fatal(err)
	}
	service := NewDatabaseSwitchService(dataDir)
	result, err := service.ClearMigrationTarget("sqlite", ClearMigrationTargetConfirmText)
	if err != nil || result.Cleared || result.ReasonCode != "active_backend" {
		t.Fatalf("清空当前后端应被拒绝: %#v err=%v", result, err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("当前库文件不得被删除: %v", err)
	}

	t.Setenv("DB_BACKEND", "postgres")
	outside := filepath.Join(t.TempDir(), "elsewhere.db")
	if err := os.WriteFile(outside, []byte("user file"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SQLITE_PATH", outside)
	result, err = service.ClearMigrationTarget("sqlite", ClearMigrationTargetConfirmText)
	if err != nil || result.Cleared || result.ReasonCode != "outside_data_dir" {
		t.Fatalf("数据目录外的库文件应拒绝删除: %#v err=%v", result, err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("数据目录外的文件不得被删除: %v", err)
	}
}

// APP02 / M-8：数据目录里的子目录是指向数据目录外的符号链接时，经它指到的库文件同样不删。
func TestAPP02ClearMigrationTargetRefusesSymlinkedSubdirectoryLeavingDataDir(t *testing.T) {
	dataDir := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(dataDir, "linked")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	outside := filepath.Join(outsideDir, "lib.db")
	if err := os.WriteFile(outside, []byte("user file"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_BACKEND", "postgres")
	t.Setenv("SQLITE_PATH", filepath.Join(dataDir, "linked", "lib.db"))
	service := NewDatabaseSwitchService(dataDir)
	result, err := service.ClearMigrationTarget("sqlite", ClearMigrationTargetConfirmText)
	if err != nil || result.Cleared || result.ReasonCode != "outside_data_dir" {
		t.Fatalf("经符号链接绕出数据目录的库文件应拒绝删除: %#v err=%v", result, err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("数据目录外的文件不得被删除: %v", err)
	}
}

// APP-02：PostgreSQL 分支只删 AllModels 的表（含其隐式关联表）与迁移标记，库里的其他表不动。
// 目标库经 openTargetOverride 指到 dbtest 的库，不去连 PG_* 指向的共享库。
func TestAPP02ClearMigrationTargetPostgresBranchDropsOnlyApplicationTables(t *testing.T) {
	t.Setenv("DB_BACKEND", "sqlite")
	source, _ := openSwitchSource(t)
	target := dbtest.OpenRaw(t)
	backend := database.BackendSQLite
	if dbtest.IsPostgres() {
		backend = database.BackendPostgres
	}
	makeHalfMigratedTarget(t, source, target, backend)
	if err := target.Exec("CREATE TABLE user_notes (id BIGINT PRIMARY KEY, body TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := target.Exec("INSERT INTO user_notes (id, body) VALUES (1, 'keep me')").Error; err != nil {
		t.Fatal(err)
	}

	service := NewDatabaseSwitchService(t.TempDir())
	service.openTargetOverride = func(requested database.Backend) (*gorm.DB, func(), error) {
		if requested != database.BackendPostgres {
			t.Fatalf("应打开 postgres 目标，实际 %s", requested)
		}
		return target, func() {}, nil
	}
	result, err := service.ClearMigrationTarget("postgres", ClearMigrationTargetConfirmText)
	if err != nil || !result.Cleared {
		t.Fatalf("清空 PG 目标库失败: %#v err=%v", result, err)
	}
	for _, want := range []string{"videos", "tags", "settings", "video_tags", "image_tags", "cineinsight_migration_marker"} {
		if !containsName(result.Removed, want) {
			t.Fatalf("删除清单应含 %s: %v", want, result.Removed)
		}
	}
	if containsName(result.Removed, "user_notes") {
		t.Fatalf("不得删除本应用之外的表: %v", result.Removed)
	}
	var kept int64
	if err := target.Table("user_notes").Count(&kept).Error; err != nil || kept != 1 {
		t.Fatalf("其他表与数据必须原样保留: count=%d err=%v", kept, err)
	}
	if err := migrator.Preflight(target); err != nil {
		t.Fatalf("清空后目标库应可重新迁移: %v", err)
	}
}

// APP-02：「切回之前的后端」只改配置、不迁移；只允许切回配置里记下的上一个后端，且目标库非空。
func TestAPP02SwitchBackendConfigOnlyRequiresNonEmptyPreviousBackend(t *testing.T) {
	dataDir := useSQLiteAsSwitchTarget(t)
	service := NewDatabaseSwitchService(dataDir)

	result, err := service.SwitchBackendConfigOnlyWithLifecycle("sqlite", nil, nil)
	if err != nil || result.Switched || result.ReasonCode != "previous_unknown" {
		t.Fatalf("没有切换记录时应拒绝: %#v err=%v", result, err)
	}

	if err := persistBackendChoice(dataDir, database.BackendPostgres, database.BackendPostgres); err != nil {
		t.Fatal(err)
	}
	result, err = service.SwitchBackendConfigOnlyWithLifecycle("sqlite", nil, nil)
	if err != nil || result.Switched || result.ReasonCode != "not_previous" {
		t.Fatalf("目标不是上一个后端时应拒绝: %#v err=%v", result, err)
	}

	if err := persistBackendChoice(dataDir, database.BackendPostgres, database.BackendSQLite); err != nil {
		t.Fatal(err)
	}
	result, err = service.SwitchBackendConfigOnlyWithLifecycle("sqlite", nil, nil)
	if err != nil || result.Switched || result.ReasonCode != "target_empty" {
		t.Fatalf("目标库为空时应拒绝: %#v err=%v", result, err)
	}

	target, closeTarget := openSQLiteFile(t, database.SQLitePath(dataDir))
	if err := target.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatal(err)
	}
	if err := target.Create(&models.Video{Name: "old-library.mp4", Path: "/old/old-library.mp4", Directory: "/old"}).Error; err != nil {
		t.Fatal(err)
	}
	closeTarget()

	result, err = service.SwitchBackendConfigOnlyWithLifecycle("sqlite", nil, nil)
	if err != nil || !result.Switched || !result.RelaunchRequired {
		t.Fatalf("切回非空的上一个后端应成功并要求重启: %#v err=%v", result, err)
	}
	if !strings.Contains(result.Message, "不会带回") {
		t.Fatalf("结果应写明切换后的改动不会带回: %q", result.Message)
	}
	config := readSwitchConfig(t, dataDir)
	if config["DB_BACKEND"] != "sqlite" || config["PREVIOUS_BACKEND"] != "postgres" {
		t.Fatalf("只改配置：目标后端与上一个后端应互换: %#v", config)
	}
	reopened, closeReopened := openSQLiteFile(t, database.SQLitePath(dataDir))
	defer closeReopened()
	var names []string
	if err := reopened.Model(&models.Video{}).Pluck("name", &names).Error; err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "old-library.mp4" {
		t.Fatalf("只改配置不应迁移或改动目标库数据: %v", names)
	}
}

// APP-02：切换只改数据目录下的 .env，进程环境变量不变。「需要重启」必须按那份文件
// 判定下次启动的后端，否则切换成功后提示永远不出现。
func TestAPP02StatusPendingRestartFollowsPersistedBackendChoice(t *testing.T) {
	openSwitchSource(t)
	dataDir := t.TempDir()
	service := NewDatabaseSwitchService(dataDir)
	live := database.Backend(database.DB.Dialector.Name())
	other := database.BackendPostgres
	if live == database.BackendPostgres {
		other = database.BackendSQLite
	}

	t.Setenv("DB_BACKEND", string(live))
	if service.Status().PendingRestart {
		t.Fatalf("没有持久化的选择时不应提示重启")
	}
	if err := persistBackendChoice(dataDir, other, live); err != nil {
		t.Fatalf("写入后端选择失败: %v", err)
	}
	if !service.Status().PendingRestart {
		t.Fatalf("配置已改为 %s、仍连着 %s 时应提示重启", other, live)
	}
	if err := persistBackendChoice(dataDir, live, other); err != nil {
		t.Fatalf("写入后端选择失败: %v", err)
	}
	if service.Status().PendingRestart {
		t.Fatalf("配置与当前连接一致时不应提示重启")
	}
}

// APP02 / M-2：DB_BACKEND 来自进程环境时，数据目录下的文件压不过它（godotenv 不覆盖已有变量），
// 「需要重启」不能按那份文件判定。
func TestAPP02StatusIgnoresPersistedChoiceWhenBackendComesFromProcessEnv(t *testing.T) {
	openSwitchSource(t)
	dataDir := t.TempDir()
	service := NewDatabaseSwitchService(dataDir)
	live := database.Backend(database.DB.Dialector.Name())
	other := database.BackendPostgres
	if live == database.BackendPostgres {
		other = database.BackendSQLite
	}
	t.Setenv("DB_BACKEND", string(live))
	if err := persistBackendChoice(dataDir, other, live); err != nil {
		t.Fatal(err)
	}
	service.backendFromProcessEnvOverride = func() bool { return true }
	if service.Status().PendingRestart {
		t.Fatal("DB_BACKEND 来自进程环境时文件无法生效，不应提示重启")
	}
	service.backendFromProcessEnvOverride = func() bool { return false }
	if !service.Status().PendingRestart {
		t.Fatal("对照：DB_BACKEND 不来自进程环境时应按文件提示重启")
	}
}

// APP02 / M-1：旧版本写的数据目录 .env 只有 DB_BACKEND、没有 PREVIOUS_BACKEND，启动时不采用，
// 「需要重启」也不按它判定。
func TestAPP02StatusIgnoresLegacyBackendConfigWithoutPreviousBackend(t *testing.T) {
	openSwitchSource(t)
	dataDir := t.TempDir()
	service := NewDatabaseSwitchService(dataDir)
	live := database.Backend(database.DB.Dialector.Name())
	other := database.BackendPostgres
	if live == database.BackendPostgres {
		other = database.BackendSQLite
	}
	t.Setenv("DB_BACKEND", string(live))
	service.backendFromProcessEnvOverride = func() bool { return false }
	if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte("DB_BACKEND="+string(other)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if service.Status().PendingRestart {
		t.Fatal("旧格式的配置启动时不采用，不应提示重启")
	}
}

// APP-02：只改配置的切换与迁移并切换同一口径——检查通过后先立围栏再写配置，成功后围栏保持、
// 进入「待重启」；写配置失败时撤围栏，配置与状态都不变。
func TestAPP02ConfigOnlySwitchKeepsFenceUntilRelaunch(t *testing.T) {
	dataDir := useSQLiteAsSwitchTarget(t)
	service := NewDatabaseSwitchService(dataDir)
	service.backendFromProcessEnvOverride = func() bool { return false }
	target, closeTarget := openSQLiteFile(t, database.SQLitePath(dataDir))
	if err := target.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatal(err)
	}
	if err := target.Create(&models.Video{Name: "old.mp4", Path: "/old/old.mp4", Directory: "/old"}).Error; err != nil {
		t.Fatal(err)
	}
	closeTarget()
	if err := persistBackendChoice(dataDir, database.BackendPostgres, database.BackendSQLite); err != nil {
		t.Fatal(err)
	}

	// 写配置失败：撤围栏，不进入待重启。
	persistBackendChoiceFn = func(string, database.Backend, database.Backend) error { return errors.New("disk full") }
	t.Cleanup(func() { persistBackendChoiceFn = persistBackendChoice })
	entered, left := 0, 0
	if _, err := service.SwitchBackendConfigOnlyWithLifecycle("sqlite", func() error { entered++; return nil }, func() { left++ }); err == nil {
		t.Fatal("配置写不进去时应报错")
	}
	if entered != 1 || left != 1 || service.RelaunchPending() {
		t.Fatalf("写配置失败应撤围栏且不进入待重启: entered=%d left=%d pending=%v", entered, left, service.RelaunchPending())
	}
	if readSwitchConfig(t, dataDir)["DB_BACKEND"] != "postgres" {
		t.Fatal("写配置失败时配置不应改动")
	}
	persistBackendChoiceFn = persistBackendChoice

	// 成功：围栏先于写配置立起，之后保持；再次操作一律 relaunch_pending。
	entered, left = 0, 0
	result, err := service.SwitchBackendConfigOnlyWithLifecycle("sqlite",
		func() error {
			entered++
			if readSwitchConfig(t, dataDir)["DB_BACKEND"] != "postgres" {
				t.Error("围栏应在写配置之前立起")
			}
			return nil
		},
		func() { left++ })
	if err != nil || !result.Switched || !result.RelaunchRequired {
		t.Fatalf("切回上一个后端应成功: %#v err=%v", result, err)
	}
	if entered != 1 || left != 0 || !service.RelaunchPending() {
		t.Fatalf("成功后围栏保持并进入待重启: entered=%d left=%d pending=%v", entered, left, service.RelaunchPending())
	}
	if again, err := service.SwitchBackendConfigOnlyWithLifecycle("postgres", nil, nil); err != nil || again.ReasonCode != DatabaseSwitchReasonRelaunchPending {
		t.Fatalf("待重启时再次切换应被拒绝: %#v err=%v", again, err)
	}
}

// APP-02：DB_BACKEND 来自进程环境时，数据目录下的配置压不过它，应用内切换永远不会生效——
// 预检、迁移并切换、只改配置都直接拒绝，不进维护模式、不改配置。
func TestAPP02SwitchRejectedWhenBackendComesFromProcessEnv(t *testing.T) {
	dataDir := useSQLiteAsSwitchTarget(t)
	service := NewDatabaseSwitchService(dataDir)
	service.backendFromProcessEnvOverride = func() bool { return true }

	preflight, err := service.Preflight("sqlite")
	if err != nil || preflight.ReasonCode != DatabaseSwitchReasonBackendEnvLocked || preflight.Empty {
		t.Fatalf("预检应拒绝: %#v err=%v", preflight, err)
	}
	entered := 0
	err = service.SwitchWithLifecycle(context.Background(), "sqlite", func() error { entered++; return nil }, func() {})
	if err == nil || !strings.Contains(err.Error(), DatabaseSwitchReasonBackendEnvLocked) || entered != 0 {
		t.Fatalf("迁移并切换应在进维护模式之前拒绝: err=%v entered=%d", err, entered)
	}
	if err := persistBackendChoice(dataDir, database.BackendPostgres, database.BackendSQLite); err != nil {
		t.Fatal(err)
	}
	result, err := service.SwitchBackendConfigOnlyWithLifecycle("sqlite", func() error { entered++; return nil }, func() {})
	if err != nil || result.Switched || result.ReasonCode != DatabaseSwitchReasonBackendEnvLocked || entered != 0 {
		t.Fatalf("只改配置应拒绝: %#v err=%v entered=%d", result, err, entered)
	}
	if readSwitchConfig(t, dataDir)["DB_BACKEND"] != "postgres" {
		t.Fatal("被拒绝时配置不应改动")
	}
}

// APP02 / m2：取消带状态。退出时的取消落在迁移登记取消函数之前（App 还在预检、后台 goroutine
// 还没起来）时，CancelRunningSwitch 找不到可取消的迁移；之后才开始的迁移按取消处理、不开始——
// 不打开目标库、不进维护模式、不写配置，终态是失败并说明已取消。
func TestAPP02CancelBeforeMigrationRegistersKeepsSwitchFromStarting(t *testing.T) {
	dataDir := useSQLiteAsSwitchTarget(t)
	openSwitchSource(t)
	service := NewDatabaseSwitchService(dataDir)
	service.backendFromProcessEnvOverride = func() bool { return false }
	if service.CancelRunningSwitch() {
		t.Fatal("没有进行中的迁移时不应有可取消的迁移")
	}
	if !service.ShuttingDown() {
		t.Fatal("取消之后应记下「正在关闭」")
	}
	opened := 0
	service.openTargetOverride = func(database.Backend) (*gorm.DB, func(), error) {
		opened++
		return nil, nil, errors.New("正在关闭时不应打开目标库")
	}
	var statuses []DatabaseSwitchStatus
	service.SetProgressSink(func(status DatabaseSwitchStatus) { statuses = append(statuses, status) })
	entered, left := 0, 0
	err := service.SwitchWithLifecycle(context.Background(), "sqlite",
		func() error { entered++; return nil }, func() { left++ })
	if !errors.Is(err, ErrDatabaseSwitchCancelledForShutdown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("正在关闭时迁移应按取消处理: %v", err)
	}
	if opened != 0 || entered != 0 || left != 0 || service.RelaunchPending() {
		t.Fatalf("迁移不应开始: opened=%d entered=%d left=%d pending=%v", opened, entered, left, service.RelaunchPending())
	}
	final := service.SwitchStatus()
	if !final.Failed || final.Running || !strings.Contains(final.Message, "已取消") {
		t.Fatalf("终态应是失败并说明已取消: %#v", final)
	}
	if len(statuses) != 1 || !statuses[0].Failed {
		t.Fatalf("推给前端的只有这一条失败终态: %#v", statuses)
	}
	if _, err := os.Stat(filepath.Join(dataDir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("取消的迁移不应写后端配置: %v", err)
	}
	if service.CancelRunningSwitch() {
		t.Fatal("被拒绝的迁移不应留下可取消的登记")
	}
}

// APP02 / m12：切换锁被占用（经 App 时只可能是清空目标库）时，迁移也发布失败终态——App 在后台
// goroutine 里调它、发起时已返回成功，不发布的话前端一直等不到结果。
func TestAPP02SwitchWhileLockHeldPublishesFailedStatus(t *testing.T) {
	dataDir := useSQLiteAsSwitchTarget(t)
	openSwitchSource(t)
	service := NewDatabaseSwitchService(dataDir)
	service.backendFromProcessEnvOverride = func() bool { return false }
	var statuses []DatabaseSwitchStatus
	service.SetProgressSink(func(status DatabaseSwitchStatus) { statuses = append(statuses, status) })

	service.mu.Lock()
	entered := 0
	err := service.SwitchWithLifecycle(context.Background(), "sqlite", func() error { entered++; return nil }, func() {})
	service.mu.Unlock()
	if !errors.Is(err, errDatabaseSwitchBusy) || entered != 0 {
		t.Fatalf("锁被占用时应直接拒绝、不进维护模式: err=%v entered=%d", err, entered)
	}
	if len(statuses) != 1 || !statuses[0].Failed || statuses[0].Running || statuses[0].Message != errDatabaseSwitchBusy.Error() {
		t.Fatalf("应发布一条失败终态: %#v", statuses)
	}
	if final := service.SwitchStatus(); !final.Failed || final.Target != "sqlite" || final.Location != database.SQLitePath(dataDir) {
		t.Fatalf("轮询兜底同样看到失败终态: %#v", final)
	}
}

// APP02 / m12：DB_BACKEND 来自进程环境时，预检结果换成的错误与 relaunch_pending 一样带原因码前缀，
// App 的 StartDatabaseSwitch 直接返回它；迁移并切换同样返回这个错误。其他原因只带说明文字。
func TestAPP02PreflightErrCarriesBackendEnvLockedPrefix(t *testing.T) {
	dataDir := useSQLiteAsSwitchTarget(t)
	service := NewDatabaseSwitchService(dataDir)
	service.backendFromProcessEnvOverride = func() bool { return true }
	preflight, err := service.Preflight("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	locked := preflight.Err()
	if !errors.Is(locked, ErrDatabaseBackendEnvLocked) || !strings.HasPrefix(locked.Error(), DatabaseSwitchReasonBackendEnvLocked+":") {
		t.Fatalf("backend_env_locked 应带原因码前缀: %v", locked)
	}
	if err := service.SwitchWithLifecycle(context.Background(), "sqlite", nil, nil); !errors.Is(err, ErrDatabaseBackendEnvLocked) {
		t.Fatalf("迁移并切换应返回同一个错误: %v", err)
	}
	if err := (&DatabaseSwitchPreflight{Reachable: true, Empty: true}).Err(); err != nil {
		t.Fatalf("目标可用时不应报错: %v", err)
	}
	notEmpty := (&DatabaseSwitchPreflight{Reachable: true, ReasonCode: "not_empty", Message: "目标库非空"}).Err()
	if notEmpty == nil || notEmpty.Error() != "目标库非空" {
		t.Fatalf("其他原因只带说明文字: %v", notEmpty)
	}
}
