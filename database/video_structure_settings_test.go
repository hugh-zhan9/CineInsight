package database_test

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestVideoStructureSettingsDefaultAndStableCutoff(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if !settings.AutoStructureEnabled || settings.AutoStructureInitialCutoffID == nil || *settings.AutoStructureInitialCutoffID != 0 {
		t.Fatalf("defaults: %+v", settings)
	}
	if err := db.Model(&settings).Update("auto_structure_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Video{Name: "old", Path: "/structure-old.mp4"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if settings.AutoStructureEnabled || *settings.AutoStructureInitialCutoffID != 0 {
		t.Fatal("existing user choice or cutoff overwritten")
	}
}
func TestVideoStructureSettingsUpgradeEnablesAndFreezesExistingIDs(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	row := models.Video{Name: "before", Path: "/before.mp4"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&models.Settings{}, "auto_structure_enabled"); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Settings{}).Where("1=1").Update("auto_structure_initial_cutoff_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	recycleConnections(t, db)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if !settings.AutoStructureEnabled || settings.AutoStructureInitialCutoffID == nil || *settings.AutoStructureInitialCutoffID != row.ID {
		t.Fatalf("upgrade: %+v", settings)
	}
}

func TestVideoStructureSettingsRecoversInterruptedMigration(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	// A crash after adding the nullable column but before setting its default.
	if err := db.Model(&models.Settings{}).Where("1=1").Update("auto_structure_enabled", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if !settings.AutoStructureEnabled {
		t.Fatal("interrupted migration left automatic preparation disabled")
	}
	if err := db.Model(&settings).Update("auto_structure_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if settings.AutoStructureEnabled {
		t.Fatal("saved false was overwritten")
	}
}
