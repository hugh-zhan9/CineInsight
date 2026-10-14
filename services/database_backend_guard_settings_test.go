package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// D-DBG-05：回落保护出错页给的配置块（DB_BACKEND=postgres、PREVIOUS_BACKEND=sqlite + PG_*）存成数据目录下的 .env，
// 应用在 PostgreSQL 上跑起来之后，设置页「数据库」分区是正常的样子：当前与下次启动都是 postgres，不误报「待重启」；
// 「切回之前的后端」指向 sqlite，本机没有 SQLite 片库时按 target_empty 拒绝，不立围栏、不进「待重启」、不改配置。
func TestDBG05SnippetConfigOnPostgresKeepsSettingsSane(t *testing.T) {
	dataDir := t.TempDir()
	snippet := strings.Replace(
		database.BackendConfigSnippet(&database.BackendStatePostgres{Host: "127.0.0.1", Port: "1", User: "me", DBName: "cine"}),
		"（在这里填密码）", "s3cret", 1)
	configPath := filepath.Join(dataDir, ".env")
	if err := os.WriteFile(configPath, []byte(snippet), 0o600); err != nil {
		t.Fatal(err)
	}
	// 启动时这份文件整份被采用（带 PREVIOUS_BACKEND）：进程环境里就是它的值，DB_BACKEND 不来自启动环境。
	for key, value := range map[string]string{
		"DB_BACKEND": "postgres", "PREVIOUS_BACKEND": "sqlite", "PG_HOST": "127.0.0.1", "PG_PORT": "1",
		"PG_USER": "me", "PG_PASSWORD": "s3cret", "PG_DB": "cine", "PG_SSLMODE": "", "SQLITE_PATH": "",
	} {
		t.Setenv(key, value)
	}
	// 正在用的是 PostgreSQL 句柄（测试里不真连：端口 1 上没有服务，语义检索检测会报不可用）。
	live, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 user=me password=s3cret dbname=cine sslmode=disable connect_timeout=2"}),
		&gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = live
	t.Cleanup(func() {
		database.DB = previous
		if sqlDB, err := live.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	service := NewDatabaseSwitchService(dataDir)
	service.backendFromProcessEnvOverride = func() bool { return false }

	status := service.Status()
	if status.Backend != "postgres" || status.NextBackend != "postgres" || status.PendingRestart {
		t.Fatalf("配置块与正在用的库一致，不应提示重启: %#v", status)
	}
	if status.Location != "127.0.0.1:1/cine" {
		t.Fatalf("当前库位置应是配置块里的 PostgreSQL: %q", status.Location)
	}

	entered := 0
	result, err := service.SwitchBackendConfigOnlyWithLifecycle("sqlite",
		func() error { entered++; return nil }, func() {})
	if err != nil {
		t.Fatalf("切回之前的后端不应出错: %v", err)
	}
	if result.Switched || result.ReasonCode != "target_empty" || entered != 0 || service.RelaunchPending() {
		t.Fatalf("本机没有 SQLite 片库时应按 target_empty 拒绝、不立围栏: %#v entered=%d", result, entered)
	}
	if content, err := os.ReadFile(configPath); err != nil || string(content) != snippet {
		t.Fatalf("拒绝时不应改配置: %q err=%v", content, err)
	}
	if service.Status().PendingRestart {
		t.Fatal("拒绝之后仍不应提示重启")
	}
}
