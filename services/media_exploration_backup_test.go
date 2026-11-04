package services

import (
	"fmt"
	"testing"
	"time"
	"video-master/models"

	"gorm.io/gorm"
)

func seedExplorationBackupRows(t *testing.T, db *gorm.DB, videoID uint, at time.Time) []any {
	t.Helper()
	state := &models.MediaExplorationState{Scope: "all", Revision: 9, RoundID: "retained-round", SceneID: "retained-scene", SceneSequence: 4, ViewStateJSON: `{"mode":"wall","scale":0.7,"x":-300,"y":-200,"selected_id":0,"gallery_id":0}`, LastRequestID: "8:retained-request", LastRequestHash: "retained-digest", LastRequestKind: "present", LastResultRevision: 9, LastResultSceneID: "retained-scene", UpdatedAt: at}
	if err := db.Create(state).Error; err != nil {
		t.Fatal(err)
	}
	entry := &models.MediaExplorationEntry{StateID: state.ID, UnitKey: fmt.Sprintf("video:%d", videoID), MediaKind: "video", MediaID: videoID, MemberIDsJSON: fmt.Sprintf("[%d]", videoID), OrderPosition: 7, PresentedAt: &at, Available: true, SceneID: state.SceneID, Slot: 0, SamplePointsJSON: `[{"start_ms":1000,"end_ms":9000,"source_version":"retained-source"}]`}
	if err := db.Create(entry).Error; err != nil {
		t.Fatal(err)
	}
	history := &models.MediaExplorationEntry{StateID: state.ID, UnitKey: "history:retained-members", MediaKind: "video", MediaID: 999999, MemberIDsJSON: "[999999,999998]", OrderPosition: 8, PresentedAt: &at, Available: false, Slot: -1, SamplePointsJSON: "[]"}
	if err := db.Create(history).Error; err != nil {
		t.Fatal(err)
	}
	return []any{state, entry}
}

func assertExplorationBackup(t *testing.T, db *gorm.DB) {
	t.Helper()
	var history models.MediaExplorationEntry
	if err := db.Where("unit_key = ?", "history:retained-members").First(&history).Error; err != nil {
		t.Fatal(err)
	}
	if history.Available || history.PresentedAt == nil || history.MemberIDsJSON != "[999999,999998]" || history.SceneID != "" {
		t.Fatal("immutable presentation history lost")
	}
}

func assertExplorationBackupManifest(t *testing.T, manifest *BackupManifest) {
	t.Helper()
	for table, count := range map[string]int64{"media_exploration_states": 1, "media_exploration_entries": 2} {
		if len(manifest.Models[table]) == 0 {
			t.Fatal("exploration schema missing", table)
		}
		if manifest.Backend == "sqlite" && manifest.Rows[table] != count {
			t.Fatal("exploration rows missing", table)
		}
	}
}
