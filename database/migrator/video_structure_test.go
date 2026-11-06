package migrator

import (
	"bytes"
	"context"
	"gorm.io/gorm"
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestMigrateVideoStructurePublishedAndCheckpointRoundTrip(t *testing.T) {
	source, middle, back := sequenceSQLite(t), dbtest.Open(t), sequenceSQLite(t)
	video := models.Video{Name: "structure", Path: "/fixture/structure.mp4"}
	if err := source.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	published := uint64(1)
	state := models.VideoStructureState{VideoID: video.ID, SourceSize: 100, SourceModTimeNS: 1700000000123456789, SourceVersion: "new-source", AlgorithmVersion: "local_structure_v1", Revision: 7, Generation: 2, Status: "waiting_idle", Phase: "analyze", DurationMS: 240000, CheckpointMS: 120000, PendingStartMS: 100000, PendingFramePTS: 100050, PreviousSamplePTS: 119500, PreviousSample: bytes.Repeat([]byte{42}, 5184), PublishedGeneration: &published, PublishedSourceVersion: "old-source", PublishedAlgorithmVersion: "local_structure_v1"}
	if err := source.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	segments := []models.VideoStructureSegment{{StateID: state.ID, Generation: 1, Position: 0, StartMS: 0, EndMS: 240000, RepresentativeMS: 5000, RepresentativeReady: true, Basis: "interval"}, {StateID: state.ID, Generation: 2, Position: 0, StartMS: 0, EndMS: 100000, RepresentativeMS: 0, Basis: "change", Score: 0.4}}
	if err := source.Create(&segments).Error; err != nil {
		t.Fatal(err)
	}
	cutoff := uint(100)
	if err := source.Create(&models.Settings{AutoStructureEnabled: false, AutoStructureInitialCutoffID: &cutoff}).Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		if _, err := Migrate(context.Background(), Options{Source: step.from, Target: step.to, TargetBackend: database.Backend(step.to.Dialector.Name())}); err != nil {
			t.Fatal(err)
		}
		var got models.VideoStructureState
		if err := step.to.First(&got, state.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.Generation != 2 || got.PublishedGeneration == nil || *got.PublishedGeneration != 1 || got.SourceModTimeNS != state.SourceModTimeNS || got.PendingFramePTS != 100050 || got.CheckpointMS != 120000 || !bytes.Equal(got.PreviousSample, state.PreviousSample) {
			t.Fatalf("checkpoint lost: %+v", got)
		}
		var count int64
		if err := step.to.Model(&models.VideoStructureSegment{}).Where("state_id = ?", state.ID).Count(&count).Error; err != nil || count != 2 {
			t.Fatalf("segments lost: %d %v", count, err)
		}
		var settings models.Settings
		if err := step.to.First(&settings).Error; err != nil {
			t.Fatal(err)
		}
		if settings.AutoStructureEnabled || settings.AutoStructureInitialCutoffID == nil || *settings.AutoStructureInitialCutoffID != cutoff {
			t.Fatal("setting or cohort overwritten")
		}
	}
}
