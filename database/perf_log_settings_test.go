package database

import (
	"path/filepath"
	"testing"

	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPerformanceLogSettingsUpgradeDefaultsAndKeepsOptOut(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "settings.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE settings (id integer primary key, log_enabled boolean)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO settings VALUES (1, false), (2, true)").Error; err != nil {
		t.Fatal(err)
	}
	migrate := func() {
		t.Helper()
		if err := db.AutoMigrate(&models.Settings{}); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(id uint, perf, system bool) {
		t.Helper()
		var value models.Settings
		if err := db.First(&value, id).Error; err != nil {
			t.Fatal(err)
		}
		if value.PerfLogEnabled == nil || *value.PerfLogEnabled != perf || value.LogEnabled != system {
			t.Fatalf("settings %d performance=%v system=%v, want %v/%v", id, value.PerformanceLogsEnabled(), value.LogEnabled, perf, system)
		}
	}
	migrate()
	expect(1, true, false)
	expect(2, true, true)
	if err := db.Model(&models.Settings{}).Where("id = ?", 1).Update("perf_log_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	migrate()
	expect(1, false, false)
	fresh := models.Settings{}
	if err := db.Create(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	expect(fresh.ID, true, false)
	disabled := false
	copied := models.Settings{PerfLogEnabled: &disabled}
	if err := db.Create(&copied).Error; err != nil {
		t.Fatal(err)
	}
	expect(copied.ID, false, false)
}

func TestPerformanceLogFreshAppDefaults(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "fresh.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if !settings.LogEnabled || !settings.PerformanceLogsEnabled() || settings.DiscoverVideoCount != 12 {
		t.Fatalf("fresh app defaults system=%v performance=%v discover=%v", settings.LogEnabled, settings.PerformanceLogsEnabled(), settings.DiscoverVideoCount)
	}
}
