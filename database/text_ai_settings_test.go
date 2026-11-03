package database_test

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestTextAIUpgradeDoesNotEnableNewPurposes(t *testing.T) {
	db := dbtest.Open(t)
	settings := models.Settings{AITaggingBaseURL: "http://fixture.invalid", AITaggingModel: "configured"}
	if err := db.Create(&settings).Error; err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"query_parse_enabled", "chapter_summary_enabled"} {
		if err := db.Migrator().DropColumn(&models.Settings{}, field); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if settings.QueryParseEnabled || settings.ChapterSummaryEnabled || settings.AITaggingModel != "configured" {
		t.Fatalf("implicit text grant: %+v", settings)
	}
}
