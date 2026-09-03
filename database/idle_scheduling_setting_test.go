// 外部测试包：与 schema_ext_test.go 同理，dbtest 依赖 database，
// database 的内部测试再依赖 dbtest 会形成导入环。
package database_test

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

// 新装库：空闲调度默认开着，阈值 5 分钟（D-032）。
func TestIdleSchedulingDefaultsAreOnForFreshDatabase(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}

	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatalf("读取默认设置失败: %v", err)
	}
	if !settings.IdleSchedulingEnabled {
		t.Fatalf("新库的空闲调度应默认开启(%s): %+v", dbtest.Backend(), settings.IdleSchedulingEnabled)
	}
	if settings.IdleThresholdMinutes != 5 {
		t.Fatalf("新库的空闲阈值应为 5 分钟，实际 %d", settings.IdleThresholdMinutes)
	}
	if settings.IdleRequireACPower || settings.IdleWindowStart != "" || settings.IdleWindowEnd != "" {
		t.Fatalf("新库不该要求接电源、也不该设时间窗: %+v", settings)
	}
}

// 老库升级：表已存在、列是这次才建出来的，必须显式刷成 true。
// 漏掉这一步的话，老用户升级后会在毫不知情的情况下失去这个默认开启的功能。
func TestIdleSchedulingIsEnabledWhenUpgradingAnOldDatabase(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	// 造一个"没有这几列"的老库：删掉列，再按升级路径重跑一次 ApplySchema。
	for _, column := range []string{"idle_scheduling_enabled", "idle_threshold_minutes", "idle_require_ac_power", "idle_window_start", "idle_window_end"} {
		if err := db.Migrator().DropColumn(&models.Settings{}, column); err != nil {
			t.Fatalf("模拟老库删除列 %s 失败(%s): %v", column, dbtest.Backend(), err)
		}
	}
	if db.Migrator().HasColumn(&models.Settings{}, "idle_scheduling_enabled") {
		t.Fatalf("模拟老库失败：列仍然存在(%s)", dbtest.Backend())
	}
	recycleConnections(t, db)

	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("老库升级失败(%s): %v", dbtest.Backend(), err)
	}

	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatalf("读取升级后的设置失败: %v", err)
	}
	if !settings.IdleSchedulingEnabled {
		t.Fatalf("老库升级后空闲调度应为开启(%s)", dbtest.Backend())
	}
}

// recycleConnections 丢掉连接池里的旧连接。
//
// 只为还原真实的升级场景：老库是上一个版本的进程建的，新版本是拿一条全新连接
// 打开它的。这里在同一条连接上先 ALTER 再查表，Postgres 会拿之前缓存的结果类型
// 报 "cached plan must not change result type"——那是本用例自己造出来的状况，
// 生产路径不会遇到（ApplySchema 里对 settings 的第一次 SELECT 就在迁移之后）。
func recycleConnections(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接池失败: %v", err)
	}
	sqlDB.SetMaxIdleConns(0)
	sqlDB.SetMaxIdleConns(2)
}

// 双向迁移器是逐行 Unscoped().Create 复制的：默认值为 true 的布尔列一旦带上
// gorm default 标签，GORM 会把 false 当成"零值即未设置"跳过，用户关掉的开关
// 在迁移之后自己翻回 true。这一条把"false 存得下来"钉住。
func TestIdleSchedulingDisabledSurvivesRowCopy(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Where("1 = 1").Delete(&models.Settings{}).Error; err != nil {
		t.Fatalf("清空默认设置失败: %v", err)
	}

	// 逐行复制的写法：主键与全部列照抄，关掉的开关必须原样落库。
	source := models.Settings{ID: 1, VideoExtensions: ".mp4", IdleSchedulingEnabled: false, IdleThresholdMinutes: 30}
	if err := db.Unscoped().Create(&source).Error; err != nil {
		t.Fatalf("复制设置行失败(%s): %v", dbtest.Backend(), err)
	}

	var copied models.Settings
	if err := db.First(&copied).Error; err != nil {
		t.Fatalf("读取复制结果失败: %v", err)
	}
	if copied.IdleSchedulingEnabled {
		t.Fatalf("关掉的空闲调度开关不该在复制后翻回开启(%s)", dbtest.Backend())
	}
	if copied.IdleThresholdMinutes != 30 {
		t.Fatalf("阈值应原样复制，实际 %d", copied.IdleThresholdMinutes)
	}
}

// 用户主动关掉之后重启：迁移不能把它又刷回开着。
// 列已经存在了，这一轮迁移就必须空转。
func TestIdleSchedulingMigrationDoesNotOverrideUserChoice(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Model(&models.Settings{}).Where("1 = 1").Update("idle_scheduling_enabled", false).Error; err != nil {
		t.Fatalf("关闭空闲调度失败: %v", err)
	}

	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重跑 schema 失败(%s): %v", dbtest.Backend(), err)
	}

	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if settings.IdleSchedulingEnabled {
		t.Fatalf("重启不该覆盖用户关掉空闲调度的选择(%s)", dbtest.Backend())
	}
}
