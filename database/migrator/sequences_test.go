package migrator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func sequenceSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(database.SQLiteDSN(filepath.Join(t.TempDir(), "sequence.db"))), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool, _ := db.DB(); _ = pool.Close() })
	return db
}

// With CINEINSIGHT_TEST_PG_DSN this is an actual SQLite -> PG -> SQLite trip,
// including deleted IDs which row-count/primary-key comparisons cannot observe.
func TestMigrationPreservesConsumedIDsAfterHardDeletion(t *testing.T) {
	source, middle, back := sequenceSQLite(t), dbtest.Open(t), sequenceSQLite(t)
	var videos []models.Video
	for _, name := range []string{"one", "two", "three"} {
		video := models.Video{Name: name, Path: "/fixture/" + name, Directory: "/fixture"}
		if err := source.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		videos = append(videos, video)
	}
	for i := 0; i < 3; i++ {
		id := videos[0].ID
		if i > 0 {
			id = videos[2].ID
		}
		if err := source.Create(&models.PlayEvent{VideoID: id, PlayedAt: time.Now(), Source: "inline_view"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := source.Unscoped().Delete(&videos[2]).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(context.Background(), Options{Source: source, Target: middle, TargetBackend: database.Backend(middle.Dialector.Name())}); err != nil {
		t.Fatal(err)
	}
	created := models.Video{Name: "next", Path: "/fixture/next", Directory: "/fixture"}
	if err := middle.Create(&created).Error; err != nil {
		t.Fatal(err)
	}
	event := models.PlayEvent{VideoID: created.ID, PlayedAt: time.Now(), Source: "inline_view"}
	if err := middle.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if created.ID != 4 || event.ID != 4 {
		t.Fatalf("deleted IDs were reused after first migration: video=%d event=%d", created.ID, event.ID)
	}
	// Source sequence reads must not consume any IDs themselves.
	copy := models.Video{Name: "source-next", Path: "/fixture/source-next", Directory: "/fixture"}
	if err := source.Create(&copy).Error; err != nil {
		t.Fatal(err)
	}
	if copy.ID != 4 {
		t.Fatalf("source sequence was changed: %d", copy.ID)
	}
	if err := middle.Unscoped().Delete(&created).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(context.Background(), Options{Source: middle, Target: back, TargetBackend: database.BackendSQLite}); err != nil {
		t.Fatal(err)
	}
	next := models.Video{Name: "back-next", Path: "/fixture/back-next", Directory: "/fixture"}
	if err := back.Create(&next).Error; err != nil {
		t.Fatal(err)
	}
	nextEvent := models.PlayEvent{VideoID: next.ID, PlayedAt: time.Now(), Source: "inline_view"}
	if err := back.Create(&nextEvent).Error; err != nil {
		t.Fatal(err)
	}
	if next.ID != 5 || nextEvent.ID != 5 {
		t.Fatalf("deleted IDs were reused after return: video=%d event=%d", next.ID, nextEvent.ID)
	}
}

func TestMigrationNeverAllocatedSequenceStartsAtOne(t *testing.T) {
	source, target := dbtest.Open(t), dbtest.Open(t)
	if _, err := Migrate(context.Background(), Options{Source: source, Target: target, TargetBackend: database.Backend(target.Dialector.Name())}); err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "first", Path: "/fixture/first", Directory: "/fixture"}
	if err := target.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if video.ID != 1 {
		t.Fatalf("never allocated sequence should start at one: %d", video.ID)
	}
}

func TestMigrationSourceSequenceFailureDoesNotMutateTarget(t *testing.T) {
	source, target := dbtest.Open(t), dbtest.Open(t)
	pool, err := source.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(context.Background(), Options{Source: source, Target: target, TargetBackend: database.Backend(target.Dialector.Name())}); err == nil {
		t.Fatal("closed source was treated as an empty database")
	}
	if target.Migrator().HasTable(&migrationMarker{}) {
		t.Fatal("target changed before reading source identities")
	}
}

func TestMigrationSequenceWriteFailureIsNotMarkedComplete(t *testing.T) {
	source, target := dbtest.Open(t), dbtest.Open(t)
	if err := source.Create(&models.Video{Name: "fixture", Path: "/fixture/file", Directory: "/fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	err := target.Callback().Raw().Before("gorm:raw").Register("test:sequence_failure", func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if strings.HasPrefix(sql, "UPDATE sqlite_sequence") || strings.HasPrefix(sql, "SELECT setval(") {
			tx.AddError(errors.New("injected sequence write failure"))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(context.Background(), Options{Source: source, Target: target, TargetBackend: database.Backend(target.Dialector.Name())}); err == nil {
		t.Fatal("sequence write failure was ignored")
	}
	if err := Preflight(target); !errors.Is(err, ErrTargetHalfMigrated) {
		t.Fatalf("incorrect completion: %v", err)
	}
}

func TestMigrationSourceReadFailureAfterCopyStartsIsNotAnEmptyTable(t *testing.T) {
	source, target := dbtest.Open(t), dbtest.Open(t)
	if err := source.Create(&models.Video{Name: "fixture", Path: "/fixture/file", Directory: "/fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := source.Create(&models.Tag{Name: "must be copied"}).Error; err != nil {
		t.Fatal(err)
	}
	pool, err := source.DB()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Migrate(context.Background(), Options{Source: source, Target: target, TargetBackend: database.Backend(target.Dialector.Name()), OnProgress: func(progress Progress) {
		if progress.TableIndex == len(models.AllModels()) {
			_ = pool.Close()
		}
	}})
	if err == nil {
		t.Fatal("source connection loss was reported as a successful empty-table copy")
	}
	if err := Preflight(target); !errors.Is(err, ErrTargetHalfMigrated) {
		t.Fatalf("incomplete copy was marked complete: %v", err)
	}
}
