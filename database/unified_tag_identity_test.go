package database_test

import (
	"errors"
	"gorm.io/gorm"
	"strings"
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestUnifiedImageCandidateIndexMigration(t *testing.T) {
	db := dbtest.Open(t)
	img := models.Image{Name: "index.jpg", Path: "/tmp/unified-index.jpg"}
	tagA := models.Tag{Name: "Action"}
	tagB := models.Tag{Name: "action"}
	for _, row := range []any{&img, &tagA, &tagB} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_image_ai_tag_candidates_pending_unique ON image_ai_tag_candidates(image_id, normalized_name) WHERE status = 'pending'`).Error; err != nil {
		t.Fatal(err)
	}
	old := models.ImageAITagCandidate{ImageID: img.ID, MatchedTagID: &tagA.ID, NormalizedName: "old", SuggestedName: "Old", Status: "pending", Confidence: "high"}
	newer := models.ImageAITagCandidate{ImageID: img.ID, MatchedTagID: &tagA.ID, NormalizedName: "action", SuggestedName: "Action", Status: "pending", Confidence: "high"}
	for _, row := range []any{&old, &newer} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Fail the DDL after deduplication to verify atomic rollback on both engines.
	callback := "test:unified-index-failure"
	if err := db.Callback().Raw().Before("gorm:raw").Register(callback, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "CREATE UNIQUE INDEX IF NOT EXISTS idx_image_ai_tag_candidates_pending_tag") {
			tx.AddError(errors.New("index creation failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Raw().Remove(callback) })
	if err := database.EnsureImageAITaggingIndexes(db); err == nil {
		t.Fatal("expected migration failure")
	}
	if err := db.First(&old, old.ID).Error; err != nil || old.Status != "pending" {
		t.Fatalf("migration did not roll back: %+v %v", old, err)
	}
	if !db.Migrator().HasIndex(&models.ImageAITagCandidate{}, "idx_image_ai_tag_candidates_pending_unique") {
		t.Fatal("failed migration lost old constraint")
	}
	if err := db.Callback().Raw().Remove(callback); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := database.EnsureImageAITaggingIndexes(db); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(&old, old.ID).Error; err != nil || old.Status != "superseded" {
		t.Fatalf("old duplicate=%+v %v", old, err)
	}
	if db.Migrator().HasIndex(&models.ImageAITagCandidate{}, "idx_image_ai_tag_candidates_pending_unique") {
		t.Fatal("old constraint retained")
	}
	distinct := models.ImageAITagCandidate{ImageID: img.ID, MatchedTagID: &tagB.ID, NormalizedName: "action", SuggestedName: "action", Status: "pending", Confidence: "high"}
	if err := db.Create(&distinct).Error; err != nil {
		t.Fatalf("distinct tag rejected: %v", err)
	}
	duplicate := models.ImageAITagCandidate{ImageID: img.ID, MatchedTagID: &tagA.ID, NormalizedName: "different", Status: "pending", Confidence: "high"}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("same tag accepted twice")
	}
	if err := db.First(&newer, newer.ID).Error; err != nil || newer.Status != "pending" {
		t.Fatalf("newest candidate not retained: %+v %v", newer, err)
	}
}
