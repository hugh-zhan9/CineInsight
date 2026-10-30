package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"

	"github.com/jackc/pgx/v5/stdlib"
	"path/filepath"
	"strconv"

	"gorm.io/gorm"
)

// BTCredentialFactory resolves a manager for the currently leased physical database.
type BTCredentialFactory func(context.Context, *gorm.DB) (*BTCredentialManager, error)

// BTCredentialScope contains only public DB identity, never a DSN or password.
func BTCredentialScope(ctx context.Context, db *gorm.DB) (string, error) {
	if db == nil || db.Dialector == nil {
		return "", ErrBTCredentials
	}
	identity := []string{db.Dialector.Name()}
	switch db.Dialector.Name() {
	case "sqlite":
		var rows []struct {
			Name string
			File string
		}
		if db.WithContext(ctx).Raw("PRAGMA database_list").Scan(&rows).Error != nil {
			return "", ErrBTCredentials
		}
		file := ""
		for _, row := range rows {
			if row.Name == "main" {
				file = row.File
			}
		}
		if file == "" {
			return "", ErrBTCredentials
		}
		absolute, err := filepath.Abs(file)
		if err != nil {
			return "", ErrBTCredentials
		}
		physical, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", ErrBTCredentials
		}
		return btSQLiteDatabaseScope(physical), nil
	case "postgres":
		sqlDB, err := db.DB()
		if err != nil {
			return "", ErrBTCredentials
		}
		connection, err := sqlDB.Conn(ctx)
		if err != nil {
			return "", ErrBTCredentials
		}
		defer connection.Close()
		// Both identities come from this borrowed connection. The client endpoint
		// distinguishes separate forwarded services reporting identical internal IPs.
		var clientEndpoint string
		err = connection.Raw(func(raw any) error {
			pgxConnection, ok := raw.(*stdlib.Conn)
			if !ok || pgxConnection == nil || pgxConnection.Conn() == nil {
				return ErrBTCredentials
			}
			socket := pgxConnection.Conn().PgConn().Conn()
			if socket == nil {
				return ErrBTCredentials
			}
			remote, ok := socket.RemoteAddr().(*net.TCPAddr)
			if !ok || remote.IP == nil || remote.IP.IsUnspecified() || remote.Port < 1 || remote.Port > 65535 {
				return ErrBTCredentials
			}
			clientEndpoint = remote.String()
			return nil
		})
		if err != nil {
			return "", ErrBTCredentials
		}
		var address, databaseName, schema string
		var port int
		err = connection.QueryRowContext(ctx, "SELECT COALESCE(inet_server_addr()::text, ''), COALESCE(inet_server_port(), 0), current_database(), COALESCE(current_schema(), '')").Scan(&address, &port, &databaseName, &schema)
		if err != nil || address == "" || port < 1 || databaseName == "" || schema == "" {
			return "", ErrBTCredentials
		}
		identity = append(identity, clientEndpoint, address, strconv.Itoa(port), databaseName, schema)
	default:
		return "", ErrBTCredentials
	}
	return btPublicDatabaseScope(identity), nil
}

// The same public SQLite identity encoding is used for an open physical DB and
// a planned restore replacement path. Keychain lookup still always resolves the
// actual open database through BTCredentialScope.
func btSQLiteDatabaseScope(canonicalPath string) string {
	return btPublicDatabaseScope([]string{"sqlite", canonicalPath})
}
func btPublicDatabaseScope(identity []string) string {
	raw, _ := json.Marshal(identity)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func btSystemCredentialFactory(ctx context.Context, db *gorm.DB) (*BTCredentialManager, error) {
	scope, err := BTCredentialScope(ctx, db)
	if err != nil {
		return nil, err
	}
	store := NewBTSystemSecretStore(scope)
	return NewBTCredentialManager(store), nil
}
