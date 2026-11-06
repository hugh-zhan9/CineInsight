package services

import (
	"bytes"
	"gorm.io/gorm"
	"testing"
	"time"
	"video-master/models"
)

func seedStructureBackupRows(t *testing.T, db *gorm.DB, videoID uint, at time.Time) []any {
	t.Helper()
	published := uint64(1)
	state := &models.VideoStructureState{VideoID: videoID, SourceVersion: "next-source", AlgorithmVersion: structureAlgorithmVersion, Revision: 7, Generation: 2, Status: "waiting_idle", Phase: "analyze", DurationMS: 240000, CheckpointMS: 120000, PendingStartMS: 100000, PendingFramePTS: 100050, PreviousSamplePTS: 119500, PreviousSample: bytes.Repeat([]byte{42}, structureFrameBytes), PublishedGeneration: &published, PublishedSourceVersion: "previous-source", PublishedAlgorithmVersion: structureAlgorithmVersion, CreatedAt: at, UpdatedAt: at}
	if err := db.Create(state).Error; err != nil {
		t.Fatal(err)
	}
	rows := []models.VideoStructureSegment{{StateID: state.ID, Generation: 1, Position: 0, StartMS: 0, EndMS: 240000, RepresentativeMS: 5000, RepresentativeReady: true, Basis: "interval"}, {StateID: state.ID, Generation: 2, Position: 0, StartMS: 0, EndMS: 100000, RepresentativeMS: 0, Basis: "change", Score: .4}}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Settings{}).Where("1=1").Updates(map[string]any{"auto_structure_enabled": false, "auto_structure_initial_cutoff_id": 100}).Error; err != nil {
		t.Fatal(err)
	}
	return []any{state}
}
func assertStructureBackup(t *testing.T, db *gorm.DB) {
	t.Helper()
	var state models.VideoStructureState
	if err := db.First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.PublishedGeneration == nil || *state.PublishedGeneration != 1 || state.Generation != 2 || state.CheckpointMS != 120000 || state.PendingFramePTS != 100050 || !bytes.Equal(state.PreviousSample, bytes.Repeat([]byte{42}, structureFrameBytes)) {
		t.Fatalf("structure checkpoint lost: %+v", state)
	}
	var rows []models.VideoStructureSegment
	if err := db.Order("generation").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Generation != 1 || !rows[0].RepresentativeReady || rows[1].Generation != 2 || rows[1].EndMS != 100000 {
		t.Fatalf("staging/publication boundary lost: %+v", rows)
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if settings.AutoStructureEnabled || settings.AutoStructureInitialCutoffID == nil || *settings.AutoStructureInitialCutoffID != 100 {
		t.Fatal("automatic preparation setting/cohort lost")
	}
}
