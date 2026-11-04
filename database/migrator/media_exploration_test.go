package migrator

import (
	"context"
	"reflect"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

func seedExplorationRows(t *testing.T, db *gorm.DB) models.MediaExplorationState {
	t.Helper()
	state := models.MediaExplorationState{Scope: "all", Revision: 8, RoundID: "round-fixture", SceneID: "scene-fixture", SceneSequence: 2, ViewStateJSON: `{"mode":"wall","scale":0.4,"x":-42,"y":-88}`, LastRequestID: "7:fixture", LastRequestHash: "receipt-hash", LastRequestKind: "present", LastResultRevision: 8, LastResultSceneID: "scene-fixture"}
	if err := db.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC)
	rows := []models.MediaExplorationEntry{
		{StateID: state.ID, UnitKey: "video:987", MediaKind: "video", MediaID: 987, MemberIDsJSON: "[987]", OrderPosition: 2, PresentedAt: &at, Available: true, SceneID: state.SceneID, Slot: 0, SamplePointsJSON: `[{"start_ms":1200,"end_ms":9200,"source_version":"fixture-source"}]`},
		{StateID: state.ID, UnitKey: "image:654", MediaKind: "image", MediaID: 654, MemberIDsJSON: "[654]", OrderPosition: 3, Available: false, SceneID: "", Slot: -1, SamplePointsJSON: "[]"},
		{StateID: state.ID, UnitKey: "history:retained", MediaKind: "video", MediaID: 888, MemberIDsJSON: "[888,889]", OrderPosition: 4, PresentedAt: &at, Available: false, SceneID: "", Slot: -1, SamplePointsJSON: "[]"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return state
}

func TestMigrateMediaExplorationRoundTrip(t *testing.T) {
	source, middle, back := sequenceSQLite(t), dbtest.Open(t), sequenceSQLite(t)
	state := seedExplorationRows(t, source)
	var original []models.MediaExplorationEntry
	if err := source.Order("id").Find(&original).Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		if _, err := Migrate(context.Background(), Options{Source: step.from, Target: step.to, TargetBackend: database.Backend(step.to.Dialector.Name())}); err != nil {
			t.Fatal(err)
		}
		var got models.MediaExplorationState
		if err := step.to.First(&got, state.ID).Error; err != nil {
			t.Fatal(err)
		}
		got.UpdatedAt, state.UpdatedAt = time.Time{}, time.Time{}
		if !reflect.DeepEqual(got, state) {
			t.Fatalf("state/receipt changed: %+v", got)
		}
		var rows []models.MediaExplorationEntry
		if err := step.to.Order("id").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(original) {
			t.Fatal("entries lost")
		}
		for i := range rows {
			if rows[i].PresentedAt != nil && !rows[i].PresentedAt.Equal(*original[i].PresentedAt) {
				t.Fatal("presentation time changed")
			}
			originalRow := original[i]
			rows[i].PresentedAt, originalRow.PresentedAt = nil, nil
			if !reflect.DeepEqual(rows[i], originalRow) {
				t.Fatalf("entry changed: %+v", rows[i])
			}
		}
	}
}
