package services

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"

	"gorm.io/gorm"
)

// The writer SQLite DSN deliberately uses BEGIN IMMEDIATE. go-sqlite3
// ignores read-only TxOptions, so this exclusively read-only stage pins a
// connection and begins its snapshot explicitly. Do not use it for writes.
func withRandomReadSnapshot(db *gorm.DB, read func(*gorm.DB) error) error {
	if db.Dialector.Name() != "sqlite" {
		return db.Transaction(read, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	}
	return db.Session(&gorm.Session{}).Connection(func(tx *gorm.DB) (err error) {
		conn, ok := tx.Statement.ConnPool.(*sql.Conn)
		if !ok {
			return errors.New("sqlite snapshot requires a dedicated connection")
		}
		if _, err := conn.ExecContext(tx.Statement.Context, "BEGIN DEFERRED"); err != nil {
			return err
		}
		defer func() {
			// The request may have been canceled. End the transaction before
			// returning the connection to the pool regardless.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, rollbackErr := conn.ExecContext(ctx, "ROLLBACK"); rollbackErr != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
				err = errors.Join(err, rollbackErr)
			}
		}()
		return read(tx.Session(&gorm.Session{NewDB: true}))
	})
}
