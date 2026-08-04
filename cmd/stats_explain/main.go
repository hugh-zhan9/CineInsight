// stats_explain 采集洞察页（P-008）关键聚合查询的 EXPLAIN 计划，作为
// "记录 EXPLAIN 关键路径" 的验收证据。它通过捕获 logger 执行真实的
// LibraryStatsService.GetStats()，保证被解释的 SQL 与线上代码逐字一致。
//
// 用法（在配置了 PG_HOST/PG_PORT/PG_USER/PG_PASSWORD/PG_DB 的终端）：
//
//	go run ./cmd/stats_explain > docs/loopx/design/2026-08-04-insights-explain.md
//
// 工具只读：直连数据库，不执行任何迁移或写入。
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"video-master/database"
	"video-master/services"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type sqlCapture struct {
	statements []string
}

func (c *sqlCapture) LogMode(logger.LogLevel) logger.Interface { return c }
func (c *sqlCapture) Info(context.Context, string, ...any)     {}
func (c *sqlCapture) Warn(context.Context, string, ...any)     {}
func (c *sqlCapture) Error(context.Context, string, ...any)    {}
func (c *sqlCapture) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	c.statements = append(c.statements, sql)
}

func main() {
	_ = godotenv.Load(".env")
	config, err := database.PostgresCLIConfigFromEnv()
	if err != nil {
		fatalf("读取数据库配置失败: %v", err)
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		config.Host, config.Port, config.User, config.Password, config.Database, config.SSLMode)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		fatalf("连接数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		fatalf("获取数据库句柄失败: %v", err)
	}
	defer sqlDB.Close()

	capture := &sqlCapture{}
	database.DB = db.Session(&gorm.Session{Logger: capture})
	stats, err := services.NewLibraryStatsService().GetStats()
	if err != nil {
		fatalf("执行洞察聚合失败: %v", err)
	}
	database.DB = db

	fmt.Printf("# 洞察页关键查询 EXPLAIN 记录\n\n")
	fmt.Printf("- 生成时间：%s\n", time.Now().Format(time.RFC3339))
	fmt.Printf("- 库规模：视频 %d 个，总时长 %.0f 秒\n", stats.Summary.VideoCount, stats.Summary.TotalDuration)
	fmt.Printf("- 采集方式：`cmd/stats_explain` 捕获 `LibraryStatsService.GetStats()` 实际执行的 SQL 后逐条 `EXPLAIN`\n\n")

	index := 0
	for _, statement := range capture.statements {
		trimmed := strings.TrimSpace(statement)
		if !strings.HasPrefix(strings.ToUpper(trimmed), "SELECT") {
			continue
		}
		index++
		fmt.Printf("## 查询 %d\n\n```sql\n%s\n```\n\n```text\n", index, trimmed)
		rows, err := db.Raw("EXPLAIN " + trimmed).Rows()
		if err != nil {
			fmt.Printf("EXPLAIN 失败: %v\n```\n\n", err)
			continue
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				break
			}
			fmt.Println(line)
		}
		rows.Close()
		fmt.Printf("```\n\n")
	}
	if index == 0 {
		fatalf("未捕获到任何 SELECT 语句")
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
