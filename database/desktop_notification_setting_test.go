// 外部测试包：与 idle_scheduling_setting_test.go 同理，dbtest 依赖 database，
// database 的内部测试再依赖 dbtest 会形成导入环。
package database_test

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// 新装库：桌面通知默认开着（D-013）。
func TestDesktopNotificationsDefaultOnForFreshDatabase(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}

	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatalf("读取默认设置失败: %v", err)
	}
	if !settings.DesktopNotificationsEnabled {
		t.Fatalf("新库的桌面通知应默认开启(%s)", dbtest.Backend())
	}
}

// 老库升级：表已存在、列是这次才建出来的，必须显式刷成 true。
// 漏掉这一步，老用户升级后会在毫不知情的情况下失去这个默认开启的功能。
func TestDesktopNotificationsEnabledWhenUpgradingAnOldDatabase(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Migrator().DropColumn(&models.Settings{}, "desktop_notifications_enabled"); err != nil {
		t.Fatalf("模拟老库删除列失败(%s): %v", dbtest.Backend(), err)
	}
	if db.Migrator().HasColumn(&models.Settings{}, "desktop_notifications_enabled") {
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
	if !settings.DesktopNotificationsEnabled {
		t.Fatalf("老库升级后桌面通知应为开启(%s)", dbtest.Backend())
	}
}

// 双向迁移器是逐行 Unscoped().Create 复制的：默认值为 true 的布尔列一旦带上
// gorm default 标签，GORM 会把 false 当成"零值即未设置"跳过，用户关掉的开关
// 在迁移之后自己翻回 true。这一条把"false 存得下来"钉住。
func TestDesktopNotificationsDisabledSurvivesRowCopy(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Where("1 = 1").Delete(&models.Settings{}).Error; err != nil {
		t.Fatalf("清空默认设置失败: %v", err)
	}

	source := models.Settings{ID: 1, VideoExtensions: ".mp4", DesktopNotificationsEnabled: false}
	if err := db.Unscoped().Create(&source).Error; err != nil {
		t.Fatalf("复制设置行失败(%s): %v", dbtest.Backend(), err)
	}

	var copied models.Settings
	if err := db.First(&copied).Error; err != nil {
		t.Fatalf("读取复制结果失败: %v", err)
	}
	if copied.DesktopNotificationsEnabled {
		t.Fatalf("关掉的桌面通知开关不该在复制后翻回开启(%s)", dbtest.Backend())
	}
}

// 用户主动关掉之后重启：列已经存在，这一轮迁移必须空转。
func TestDesktopNotificationsMigrationDoesNotOverrideUserChoice(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Model(&models.Settings{}).Where("1 = 1").Update("desktop_notifications_enabled", false).Error; err != nil {
		t.Fatalf("关闭桌面通知失败: %v", err)
	}

	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重跑 schema 失败(%s): %v", dbtest.Backend(), err)
	}

	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if settings.DesktopNotificationsEnabled {
		t.Fatalf("重启不该覆盖用户关掉桌面通知的选择(%s)", dbtest.Backend())
	}
}
