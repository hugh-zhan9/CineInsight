package migrator

import (
	"context"
	"gorm.io/gorm"
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestMigrateLibraryExperiencePreferencesAndFiltersRoundTrip(t *testing.T) {
	source, middle, back := sequenceSQLite(t), dbtest.Open(t), sequenceSQLite(t)
	settings := models.Settings{QueryParseEnabled: true, ChapterSummaryEnabled: true}
	if err := source.Create(&settings).Error; err != nil {
		t.Fatal(err)
	}
	tag := models.Tag{Name: "positive", PreferenceRevision: 5}
	view := models.SavedLibraryView{Name: "compound", SearchMode: "file", WatchState: "unwatched", FavoritesOnly: true, MinDurationSeconds: 1200, MaxDurationSeconds: 7200, SortMode: "balanced"}
	if err := source.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := source.Create(&view).Error; err != nil {
		t.Fatal(err)
	}
	pref := models.RecommendationFeedback{TargetKind: "tag", TargetID: tag.ID, Effect: "boost", Revision: 8}
	if err := source.Create(&pref).Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		if _, err := Migrate(context.Background(), Options{Source: step.from, Target: step.to, TargetBackend: database.Backend(step.to.Dialector.Name())}); err != nil {
			t.Fatal(err)
		}
		var gotSettings models.Settings
		if err := step.to.First(&gotSettings).Error; err != nil || !gotSettings.QueryParseEnabled || !gotSettings.ChapterSummaryEnabled {
			t.Fatalf("text-purpose flags lost %+v %v", gotSettings, err)
		}
		var gotTag models.Tag
		var gotView models.SavedLibraryView
		var gotPref models.RecommendationFeedback
		if err := step.to.First(&gotTag, tag.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := step.to.First(&gotView, view.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := step.to.First(&gotPref, pref.ID).Error; err != nil {
			t.Fatal(err)
		}
		if gotTag.PreferenceRevision != 5 || gotView.WatchState != "unwatched" || !gotView.FavoritesOnly || gotView.MinDurationSeconds != 1200 || gotView.MaxDurationSeconds != 7200 || gotPref.Effect != "boost" || gotPref.Revision != 8 {
			t.Fatalf("round trip lost conditions: %+v %+v %+v", gotTag, gotView, gotPref)
		}
	}
}
