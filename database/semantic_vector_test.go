package database

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type postgresNamedDialector struct{ gorm.Dialector }

func (postgresNamedDialector) Name() string { return "postgres" }

func TestPrepareSemanticVectorStorageDegradesExplicitlyWithoutPostgres(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "semantic.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	if err := db.AutoMigrate(&models.Video{}); err != nil {
		t.Fatalf("migrate videos: %v", err)
	}
	capability := PrepareSemanticVectorStorage(db)
	if capability.Available || capability.Backend != "sqlite" || capability.ReasonCode != "pgvector_requires_postgres" {
		t.Fatalf("unexpected capability: %+v", capability)
	}
	for _, model := range []any{&models.SemanticIndexProfile{}, &models.VideoSemanticIndex{}, &models.SemanticIndexAttempt{}} {
		if !db.Migrator().HasTable(model) {
			t.Fatalf("portable semantic metadata table missing for %T", model)
		}
	}
	if err := UpsertSemanticVector(db, capability, 1, "embed-model", 2, 1, []float64{0.1, 0.2}, time.Now()); !errors.Is(err, ErrSemanticVectorUnavailable) {
		t.Fatalf("SQLite pgvector write error = %v", err)
	}
}

func TestPrepareSemanticVectorStorageDoesNotReturnAvailableWhenExtensionCreationFails(t *testing.T) {
	dialector := postgresNamedDialector{Dialector: sqlite.Open(filepath.Join(t.TempDir(), "semantic.db"))}
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		t.Fatalf("open simulated PostgreSQL database: %v", err)
	}
	if err := db.AutoMigrate(&models.Video{}); err != nil {
		t.Fatalf("migrate videos: %v", err)
	}
	capability := PrepareSemanticVectorStorage(db)
	if capability.Available || capability.Backend != "postgres" || capability.ReasonCode != "extension_unavailable" || capability.Message == "" {
		t.Fatalf("extension failure was not reported explicitly: %+v", capability)
	}
	if !db.Migrator().HasTable(&models.SemanticIndexProfile{}) {
		t.Fatal("portable migration should remain available after extension failure")
	}
}
