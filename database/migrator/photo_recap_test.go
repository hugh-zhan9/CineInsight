package migrator

import (
	"context"
	"gorm.io/gorm"
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestMigratePhotoRecapConditionsAndMapPolicyRoundTrip(t *testing.T) {
	source, middle, back := sequenceSQLite(t), dbtest.Open(t), sequenceSQLite(t)
	recap := models.PhotoRecap{Name: "同名可保留", DateFrom: "2026-01-01", DateTo: "2026-02-28", PersonIDsJSON: "[999,1000]", Revision: 4}
	if err := source.Create(&recap).Error; err != nil {
		t.Fatal(err)
	}
	if err := source.Create(&models.Settings{MapTilesEnabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		if _, err := Migrate(context.Background(), Options{Source: step.from, Target: step.to, TargetBackend: database.Backend(step.to.Dialector.Name())}); err != nil {
			t.Fatal(err)
		}
		var got models.PhotoRecap
		if err := step.to.First(&got, recap.ID).Error; err != nil || got.Name != recap.Name || got.DateFrom != recap.DateFrom || got.DateTo != recap.DateTo || got.PersonIDsJSON != recap.PersonIDsJSON || got.Revision != 4 {
			t.Fatalf("recap lost %+v %v", got, err)
		}
		var settings models.Settings
		if err := step.to.First(&settings).Error; err != nil || !settings.MapTilesEnabled {
			t.Fatalf("map policy lost %v", err)
		}
	}
}
