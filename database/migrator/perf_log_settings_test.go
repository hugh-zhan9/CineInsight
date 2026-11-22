package migrator

import (
	"context"
	"testing"

	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

func TestMigratePreservesPerformanceLogOptOut(t *testing.T) {
	source, middle, back := sequenceSQLite(t), dbtest.Open(t), sequenceSQLite(t)
	disabled := false
	if err := source.Create(&models.Settings{PerfLogEnabled: &disabled}).Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		backend := database.Backend(step.to.Dialector.Name())
		if _, err := Migrate(context.Background(), Options{Source: step.from, Target: step.to, TargetBackend: backend}); err != nil {
			t.Fatal(err)
		}
		var got models.Settings
		if err := step.to.First(&got).Error; err != nil {
			t.Fatal(err)
		}
		if got.PerfLogEnabled == nil || got.PerformanceLogsEnabled() || got.LogEnabled {
			t.Fatalf("performance or system log opt-out was lost on %s", backend)
		}
	}
}
