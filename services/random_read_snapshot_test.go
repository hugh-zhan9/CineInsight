package services

import (
	"errors"
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRandomReadSnapshotDoesNotWaitForSQLiteWriter(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "snapshot.db") + "?_journal_mode=WAL&_txlock=immediate&_busy_timeout=100"
	db, err := gorm.Open(sqlite.Open(dsn))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	pool.SetMaxOpenConns(2)
	run := func(conn *gorm.DB, query string) {
		t.Helper()
		if err := conn.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	run(db, "CREATE TABLE snapshot_values (n INTEGER)")
	run(db, "INSERT INTO snapshot_values VALUES (1)")
	writer := db.Begin()
	if writer.Error != nil {
		t.Fatal(writer.Error)
	}
	t.Cleanup(func() { writer.Rollback() })
	run(writer, "UPDATE snapshot_values SET n=2")
	assertValue := func(tx *gorm.DB, want int) {
		t.Helper()
		var got int
		if err := tx.Raw("SELECT n FROM snapshot_values").Scan(&got).Error; err != nil || got != want {
			t.Fatalf("snapshot read got %d, err=%v, want %d", got, err, want)
		}
	}
	sentinel := errors.New("test callback result")
	err = withRandomReadSnapshot(db, func(tx *gorm.DB) error {
		assertValue(tx, 1)
		if err := writer.Commit().Error; err != nil {
			t.Fatal(err)
		}
		assertValue(tx, 1)
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("snapshot: %v", err)
	}
	if err := withRandomReadSnapshot(db, func(tx *gorm.DB) error { assertValue(tx, 2); return nil }); err != nil {
		t.Fatal(err)
	}
}
