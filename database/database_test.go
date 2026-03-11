package database

import "testing"

func TestInitUsesPostgresEnv(t *testing.T) {
	t.Setenv("PG_HOST", "127.0.0.1")
	t.Setenv("PG_PORT", "5432")
	t.Setenv("PG_USER", "user")
	t.Setenv("PG_PASSWORD", "pass")
	t.Setenv("PG_DB", "db")
	t.Setenv("PG_SSLMODE", "disable")

	err := Init()
	if err == nil {
		_ = Close()
		t.Fatalf("expected error when postgres is unreachable")
	}
}
