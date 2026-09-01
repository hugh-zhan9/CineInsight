// Package dbtest 给测试提供一个可切换后端的库连接。
//
// 存在的理由：应用支持 SQLite 与 PostgreSQL 两个后端，但测试历来只跑 SQLite，
// Postgres 分支是零覆盖的。加了第二个后端之后，「改 A 坏 B」才成为真实风险，
// 所以同一套测试必须能在两个后端上分别跑完。
//
// 默认仍是 SQLite（保持现有速度与行为）。设置 CINEINSIGHT_TEST_PG_DSN 后切到
// Postgres：每次 Open 建一个独立 schema 并在测试结束时删掉，让并行测试互不可见，
// 也避免为每个测试单独建库的开销。
package dbtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"video-master/database"
	"video-master/models"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// PostgresDSNEnv 是切到 Postgres 后端所需的连接串环境变量名。
const PostgresDSNEnv = "CINEINSIGHT_TEST_PG_DSN"

var schemaCounter atomic.Uint64

// Backend 返回本次测试运行使用的后端名，供需要分支断言的测试判断。
func Backend() string {
	if strings.TrimSpace(os.Getenv(PostgresDSNEnv)) != "" {
		return "postgres"
	}
	return "sqlite"
}

// IsPostgres 报告当前测试是否跑在 Postgres 上。
func IsPostgres() bool { return Backend() == "postgres" }

// Open 打开一个空库并跑完 AutoMigrate，返回可直接使用的连接。
// 不做任何数据初始化——调用方各自准备自己的夹具。
func Open(t *testing.T) *gorm.DB {
	t.Helper()
	db := open(t)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("迁移测试数据库失败(%s): %v", Backend(), err)
	}
	return db
}

// OpenRaw 打开一个空库但不迁移，供需要自己控制建表过程的测试使用
// （例如验证迁移函数本身在老库上的行为）。
func OpenRaw(t *testing.T) *gorm.DB {
	t.Helper()
	return open(t)
}

func open(t *testing.T) *gorm.DB {
	t.Helper()
	if dsn := strings.TrimSpace(os.Getenv(PostgresDSNEnv)); dsn != "" {
		return openPostgres(t, dsn)
	}
	return openSQLite(t)
}

func openSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	// 走与生产同一个 DSN 构造：测试库若不开外键约束，就测不出
	// "在 SQLite 上能插进去、迁到 Postgres 就炸" 这类问题。
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := gorm.Open(sqlite.Open(database.SQLiteDSN(path)), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 SQLite 测试库失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// openPostgres 为每个测试建一个独立 schema。用 schema 而不是独立数据库：
// 建库在 Postgres 上是重操作，几百个测试各建一次会把跑一遍套件的时间拉到不可用。
func openPostgres(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	schema := fmt.Sprintf("cit_%d_%d", os.Getpid(), schemaCounter.Add(1))

	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("连接 Postgres 测试库失败: %v", err)
	}
	if err := admin.Exec(fmt.Sprintf("CREATE SCHEMA %s", schema)).Error; err != nil {
		t.Fatalf("创建测试 schema 失败: %v", err)
	}
	if sqlDB, err := admin.DB(); err == nil {
		_ = sqlDB.Close()
	}

	scoped := dsn
	if strings.Contains(scoped, "search_path=") {
		t.Fatalf("测试连接串不应自带 search_path: %s", PostgresDSNEnv)
	}
	scoped = fmt.Sprintf("%s search_path=%s", scoped, schema)

	db, err := gorm.Open(postgres.Open(scoped), &gorm.Config{})
	if err != nil {
		t.Fatalf("连接测试 schema 失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		cleanup, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err != nil {
			return
		}
		_ = cleanup.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema)).Error
		if sqlDB, err := cleanup.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}
