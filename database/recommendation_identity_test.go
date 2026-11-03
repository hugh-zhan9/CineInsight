package database_test

import (
	"strings"
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestRecommendationIdentityUpgradeKeepsPreferenceAndViewDefaults(t *testing.T) {
	db := dbtest.Open(t)
	if err := db.Migrator().DropIndex(&models.RecommendationFeedback{}, "idx_recommendation_feedback_identity"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX idx_recommendation_feedback_target ON recommendation_feedbacks(target_kind,target_id,effect)").Error; err != nil {
		t.Fatal(err)
	}
	row := models.RecommendationFeedback{TargetKind: "tag", TargetID: 42, Effect: "downweight", Revision: 7}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"watch_state", "favorites_only", "min_duration_seconds", "max_duration_seconds"} {
		if err := db.Migrator().DropColumn(&models.SavedLibraryView{}, column); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("INSERT INTO saved_library_views(name, search_mode, smart_view, sort_mode) VALUES (?, ?, ?, ?)", "legacy", "file", "favorites", "balanced").Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := database.ApplySchema(db); err != nil {
			t.Fatal(err)
		}
	}
	if db.Migrator().HasIndex(&models.RecommendationFeedback{}, "idx_recommendation_feedback_target") {
		t.Fatal("old three-column key remains")
	}
	var got models.RecommendationFeedback
	if err := db.First(&got, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Revision != 7 || got.Effect != "downweight" {
		t.Fatalf("modified preference %+v", got)
	}
	if err := db.Create(&models.RecommendationFeedback{TargetKind: "tag", TargetID: 42, Effect: "boost", Revision: 1}).Error; err == nil {
		t.Fatal("two preference directions allowed")
	}
	var view models.SavedLibraryView
	if err := db.Where("name = ?", "legacy").First(&view).Error; err != nil {
		t.Fatal(err)
	}
	if view.WatchState != "any" || view.FavoritesOnly || view.MinDurationSeconds != 0 || view.MaxDurationSeconds != 0 || view.SmartView != "favorites" {
		t.Fatalf("changed legacy filters %+v", view)
	}
}

func TestRecommendationIdentityUpgradeRejectsConflictsWithoutDeletingRows(t *testing.T) {
	db := dbtest.Open(t)
	if err := db.Migrator().DropIndex(&models.RecommendationFeedback{}, "idx_recommendation_feedback_identity"); err != nil {
		t.Fatal(err)
	}
	for _, effect := range []string{"boost", "downweight"} {
		if err := db.Create(&models.RecommendationFeedback{TargetKind: "tag", TargetID: 2, Effect: effect, Revision: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	err := database.ApplySchema(db)
	if err == nil || !strings.Contains(err.Error(), "recommendation_identity_conflict") {
		t.Fatalf("%v", err)
	}
	var count int64
	db.Model(&models.RecommendationFeedback{}).Count(&count)
	if count != 2 {
		t.Fatal("conflicting preferences were removed")
	}
}
