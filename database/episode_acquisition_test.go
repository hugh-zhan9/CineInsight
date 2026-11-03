package database_test

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestEpisodeRevisionUpgradeBackfillsWithoutResetting(t *testing.T) {
	db := dbtest.Open(t)
	collection := models.MediaCollection{Name: "legacy", NormalizedName: "legacy"}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&models.CollectionEpisodeRevision{}); err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	var state models.CollectionEpisodeRevision
	if err := db.First(&state, "collection_id = ?", collection.ID).Error; err != nil {
		t.Fatal(err)
	}
	if state.Revision != 1 {
		t.Fatal(state)
	}
	if err := db.Model(&state).Update("revision", 19).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&state, "collection_id = ?", collection.ID).Error; err != nil || state.Revision != 19 {
		t.Fatalf("reset revision: %+v %v", state, err)
	}
}
