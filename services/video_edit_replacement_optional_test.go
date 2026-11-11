package services

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func replacementSemanticMetadata(t *testing.T, videoID uint) {
	t.Helper()
	if err := database.DB.AutoMigrate(&models.VideoSemanticIndex{}, &models.SemanticIndexAttempt{}); err != nil {
		t.Fatal(err)
	}
	index := models.VideoSemanticIndex{VideoID: videoID, ModelIdentifier: "fixture", Dimension: 2, Generation: 1, ContentFingerprint: "old", IndexedAt: time.Now()}
	if err := database.DB.Create(&index).Error; err != nil {
		t.Fatal(err)
	}
	attempt := models.SemanticIndexAttempt{VideoID: videoID, ModelIdentifier: "fixture", Generation: 1, Status: models.SemanticIndexAttemptCompleted, ContentFingerprint: "old", Dimension: 2}
	if err := database.DB.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
}
func TestEditReplacementOptionalSemanticMetadataAbsentOrPresent(t *testing.T) {
	for _, present := range []bool{false, true} {
		name := "absent"
		if present {
			name = "present"
		}
		t.Run(name, func(t *testing.T) {
			s, item, plan, staged := replacementFixture(t)
			other := models.Video{Name: "other.avi", Path: "/unrelated/other.avi"}
			if err := database.DB.Create(&other).Error; err != nil {
				t.Fatal(err)
			}
			if present {
				replacementSemanticMetadata(t, plan.Sources[0].VideoID)
				replacementSemanticMetadata(t, other.ID)
			} else {
				for _, model := range []any{&models.VideoSemanticIndex{}, &models.SemanticIndexAttempt{}} {
					if database.DB.Migrator().HasTable(model) {
						t.Fatal("fixture unexpectedly installed optional table")
					}
				}
			}
			if err := s.publishEditReplacement(context.Background(), item, plan, staged); err != nil {
				t.Fatal(err)
			}
			current, err := loadEditItemByID(context.Background(), item.ID)
			if err != nil || current.Status != models.VideoEditStatusCompleted || current.OutputVideoID == nil || *current.OutputVideoID != plan.Sources[0].VideoID {
				t.Fatal(current, err)
			}
			if !present {
				return
			}
			for _, model := range []any{&models.VideoSemanticIndex{}, &models.SemanticIndexAttempt{}} {
				var targetCount, otherCount int64
				if err = database.DB.Model(model).Where("video_id = ?", plan.Sources[0].VideoID).Count(&targetCount).Error; err != nil {
					t.Fatal(err)
				}
				if err = database.DB.Model(model).Where("video_id = ?", other.ID).Count(&otherCount).Error; err != nil {
					t.Fatal(err)
				}
				if targetCount != 0 || otherCount != 1 {
					t.Fatal("optional invalidation lost its scope", targetCount, otherCount)
				}
			}
		})
	}
}
func TestEditReplacementOptionalSemanticDeleteErrorRollsBack(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	replacementSemanticMetadata(t, plan.Sources[0].VideoID)
	statement := &gorm.Statement{DB: database.DB}
	if err := statement.Parse(&models.VideoSemanticIndex{}); err != nil {
		t.Fatal(err)
	}
	name := "test:replacement_optional_semantic_delete_failure"
	failure := errors.New("semantic delete denied")
	callbacks := database.DB.Callback().Delete()
	if err := callbacks.Before("gorm:delete").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == statement.Schema.Table {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { callbacks.Remove(name) })
	if err := s.publishEditReplacement(context.Background(), item, plan, staged); !errors.Is(err, failure) {
		t.Fatal("optional delete error swallowed", err)
	}
	if !replacementHasSHA(plan.Sources[0].Path, plan.Sources[0].SHA256) {
		t.Fatal("SQL failure did not restore original")
	}
	var row models.Video
	if err := database.DB.First(&row, plan.Sources[0].VideoID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Width != 0 || row.Description != "manual" {
		t.Fatal("partial replacement committed", row)
	}
	var count int64
	if err := database.DB.Model(&models.VideoSemanticIndex{}).Where("video_id = ?", row.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("optional data did not roll back", count, err)
	}
}

func TestEditReplacementSemanticCatalogErrorIsNotAbsence(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	failure := errors.New("semantic catalog unavailable")
	statement := &gorm.Statement{DB: database.DB}
	if err := statement.Parse(&models.VideoSemanticIndex{}); err != nil {
		t.Fatal(err)
	}
	name := "test:replacement_semantic_catalog_error"
	callbacks := database.DB.Callback().Row()
	if err := callbacks.Before("gorm:row").Register(name, func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if (strings.Contains(sql, "sqlite_master") || strings.Contains(sql, "to_regclass")) && len(tx.Statement.Vars) > 0 && tx.Statement.Vars[0] == statement.Schema.Table {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { callbacks.Remove(name) })
	if err := s.publishEditReplacement(context.Background(), item, plan, staged); !errors.Is(err, failure) {
		t.Fatal("catalog failure treated as optional absence", err)
	}
	if !replacementHasSHA(plan.Sources[0].Path, plan.Sources[0].SHA256) {
		t.Fatal("catalog error changed source")
	}
}
func TestEditReplacementRequiredDerivedTableAbsenceStillFails(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	if err := database.DB.Migrator().DropTable(&models.VideoFrameHashSequence{}); err != nil {
		t.Fatal(err)
	}
	if err := s.publishEditReplacement(context.Background(), item, plan, staged); err == nil {
		t.Fatal("missing required derived table was ignored")
	}
	if !replacementHasSHA(plan.Sources[0].Path, plan.Sources[0].SHA256) {
		t.Fatal("required SQL failure did not restore source")
	}
}
func TestEditReplacementPGSemanticVectorsAreScopedAndRemoved(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	if database.DB.Dialector.Name() != "postgres" {
		t.Skip("requires PostgreSQL pgvector")
	}
	capability := database.PrepareSemanticVectorStorage(database.DB)
	if !capability.Available {
		t.Skip("pgvector unavailable: " + capability.ReasonCode)
	}
	other := models.Video{Name: "other.avi", Path: "/unrelated/other.avi"}
	if err := database.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{plan.Sources[0].VideoID, other.ID} {
		if err := database.UpsertSemanticVector(database.DB, capability, id, "fixture", 2, 1, []float64{0.1, 0.9}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.publishEditReplacement(context.Background(), item, plan, staged); err != nil {
		t.Fatal(err)
	}
	var targetCount, otherCount int64
	if err := database.DB.Table("video_semantic_vectors").Where("video_id = ?", plan.Sources[0].VideoID).Count(&targetCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Table("video_semantic_vectors").Where("video_id = ?", other.ID).Count(&otherCount).Error; err != nil {
		t.Fatal(err)
	}
	if targetCount != 0 || otherCount != 1 {
		t.Fatal("stale vectors remained visible or unrelated vectors removed", targetCount, otherCount)
	}
}
