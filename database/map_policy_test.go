package database_test

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestMapPolicyUpgradeDoesNotEnableNetworking(t *testing.T) {
	db := dbtest.Open(t)
	settings := models.Settings{AITaggingModel: "configured"}
	if err := db.Create(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&models.Settings{}, "map_tiles_enabled"); err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&settings).Error; err != nil || settings.MapTilesEnabled || settings.AITaggingModel != "configured" {
		t.Fatalf("upgrade grant %+v %v", settings, err)
	}
}
