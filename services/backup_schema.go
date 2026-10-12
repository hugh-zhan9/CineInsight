package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"video-master/database"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 备份清单的表结构指纹与「来自更新版本」判定（D-DH-02 / D-DH-03）。

// sqliteSnapshotFacts 是从 SQLite 快照里读出的清单内容：快速完整性检查、各表行数与表结构。
type sqliteSnapshotFacts struct {
	QuickCheck string
	Rows       map[string]int64
	Tables     map[string][]string
}

// openSQLiteReadOnly 只读打开一个 SQLite 文件（快照），不做迁移——迁移会改动快照本身。
func openSQLiteReadOnly(path string) (*gorm.DB, func(), error) {
	db, err := gorm.Open(sqlite.Open(path+"?mode=ro"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, nil, fmt.Errorf("无法作为 SQLite 库打开: %w", err)
	}
	closeDB := func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	return db, closeDB, nil
}

// inspectSQLiteSnapshot 只读打开快照，做 PRAGMA quick_check、统计各表行数并读出表结构。
// quick_check 不是 ok 时只返回首条问题，不再统计（快照已不可信）。
func inspectSQLiteSnapshot(ctx context.Context, path string) (sqliteSnapshotFacts, error) {
	db, closeDB, err := openSQLiteReadOnly(path)
	if err != nil {
		return sqliteSnapshotFacts{}, err
	}
	defer closeDB()
	db = db.WithContext(ctx)
	quick, err := sqliteQuickCheck(db)
	if err != nil {
		return sqliteSnapshotFacts{}, err
	}
	facts := sqliteSnapshotFacts{QuickCheck: quick}
	if quick != "ok" {
		return facts, nil
	}
	if facts.Tables, err = sqliteSchemaTables(db); err != nil {
		return sqliteSnapshotFacts{}, err
	}
	facts.Rows = make(map[string]int64, len(facts.Tables))
	for table := range facts.Tables {
		var count int64
		if err := db.Raw("SELECT COUNT(*) FROM " + quoteSQLiteIdent(table)).Scan(&count).Error; err != nil {
			return sqliteSnapshotFacts{}, fmt.Errorf("统计表 %s 行数失败: %w", table, err)
		}
		facts.Rows[table] = count
	}
	return facts, nil
}

// sqliteQuickCheck 执行 PRAGMA quick_check，返回 "ok" 或首条问题。
func sqliteQuickCheck(db *gorm.DB) (string, error) {
	results, err := sqliteQuickCheckAll(db)
	if err != nil {
		return "", fmt.Errorf("完整性检查失败: %w", err)
	}
	if len(results) == 0 {
		return "", errors.New("完整性检查没有返回结果")
	}
	return results[0], nil
}

// sqliteQuickCheckAll 返回 PRAGMA quick_check 的全部结果行。库坏到连检查本身都报「malformed」或
// 「not a database」时，这句报错就是检查结论，作为一条问题返回而不是错误。
func sqliteQuickCheckAll(db *gorm.DB) ([]string, error) {
	var results []string
	err := db.Raw("PRAGMA quick_check").Scan(&results).Error
	if err != nil && sqliteCorruptionError(err) {
		return []string{err.Error()}, nil
	}
	return results, err
}

func sqliteCorruptionError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "database disk image is malformed") || strings.Contains(message, "file is not a database")
}

// sqliteSchemaTables 读出库里的表与列（跳过 sqlite_ 开头的内部表）。
func sqliteSchemaTables(db *gorm.DB) (map[string][]string, error) {
	var tables []string
	if err := db.Raw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`).
		Scan(&tables).Error; err != nil {
		return nil, fmt.Errorf("读取表结构失败: %w", err)
	}
	schema := make(map[string][]string, len(tables))
	for _, table := range tables {
		var columns []struct{ Name string }
		if err := db.Raw("PRAGMA table_info(" + quoteSQLiteIdent(table) + ")").Scan(&columns).Error; err != nil {
			return nil, fmt.Errorf("读取表 %s 的列失败: %w", table, err)
		}
		names := make([]string, 0, len(columns))
		for _, column := range columns {
			names = append(names, column.Name)
		}
		sort.Strings(names)
		schema[table] = names
	}
	return schema, nil
}

func quoteSQLiteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// parsePostgresListTables 从 pg_restore --list 的目录里取出表名（形如「215; 1259 16386 TABLE public videos owner」），
// 列为空：Postgres 备份只比表名（4.2）。
func parsePostgresListTables(listing string) map[string][]string {
	tables := map[string][]string{}
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || strings.HasPrefix(fields[0], ";") || !strings.HasSuffix(fields[0], ";") {
			continue
		}
		if fields[3] == "TABLE" && fields[4] != "DATA" {
			tables[fields[5]] = []string{}
		}
	}
	return tables
}

// knownBackupSchema 是当前版本认得的表与列（表 → 列集合；列集合为 nil 表示不按列核对）：
//   - AllModels 与它们的多对多连接表（4.2 的口径）；
//   - 只在 Postgres 上建的语义索引表、向量表与迁移标记表（裸 DDL 建的表只认表名）；
//   - 并上当前库里实际存在的表与列：AutoMigrate 不删列，老库里模型已经去掉的遗留列还在，
//     不并上它们的话，当前版本自己做的备份也会被判成「来自更新的版本」。
func knownBackupSchema(ctx context.Context, db *gorm.DB) (map[string]map[string]bool, error) {
	if db == nil {
		return nil, errors.New("数据库未初始化")
	}
	known := map[string]map[string]bool{}
	addModel := func(model any) error {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			return fmt.Errorf("解析模型 %T 失败: %w", model, err)
		}
		addKnownColumns(known, stmt.Schema.Table, stmt.Schema.DBNames)
		for _, relation := range stmt.Schema.Relationships.Many2Many {
			if relation.JoinTable != nil {
				addKnownColumns(known, relation.JoinTable.Table, relation.JoinTable.DBNames)
			}
		}
		return nil
	}
	semanticModels := []any{
		&models.SemanticIndexProfile{}, &models.VideoSemanticIndex{}, &models.SemanticIndexAttempt{},
		&models.ImageSemanticIndex{}, &models.ImageSemanticIndexAttempt{},
	}
	for _, model := range append(models.AllModels(), semanticModels...) {
		if err := addModel(model); err != nil {
			return nil, err
		}
	}
	applicationTables, err := database.ApplicationTables(db)
	if err != nil {
		return nil, err
	}
	for _, table := range applicationTables {
		if _, ok := known[table]; !ok {
			known[table] = nil
		}
	}
	live, err := liveSchemaTables(ctx, db)
	if err != nil {
		return nil, err
	}
	for table, columns := range live {
		if existing, ok := known[table]; ok && existing == nil {
			continue
		}
		addKnownColumns(known, table, columns)
	}
	return known, nil
}

func addKnownColumns(known map[string]map[string]bool, table string, columns []string) {
	set := known[table]
	if set == nil {
		set = map[string]bool{}
		known[table] = set
	}
	for _, column := range columns {
		set[column] = true
	}
}

// liveSchemaTables 读出当前库的表与列。
func liveSchemaTables(ctx context.Context, db *gorm.DB) (map[string][]string, error) {
	db = db.WithContext(ctx)
	if db.Dialector.Name() != string(database.BackendPostgres) {
		return sqliteSchemaTables(db)
	}
	var rows []struct {
		TableName  string
		ColumnName string
	}
	if err := db.Raw(`SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = current_schema()`).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取表结构失败: %w", err)
	}
	schema := map[string][]string{}
	for _, row := range rows {
		schema[row.TableName] = append(schema[row.TableName], row.ColumnName)
	}
	return schema, nil
}

// unknownBackupSchema 列出清单里当前版本不认识的表（「表」）与列（「表.列」），按字典序。
// 空表示兼容。tablesOnly 时只比表名（Postgres 备份，4.2）。
func unknownBackupSchema(schema BackupManifestSchema, known map[string]map[string]bool, tablesOnly bool) []string {
	unknown := []string{}
	for table, columns := range schema.Tables {
		knownColumns, ok := known[table]
		if !ok {
			unknown = append(unknown, table)
			continue
		}
		if tablesOnly || knownColumns == nil {
			continue
		}
		for _, column := range columns {
			if !knownColumns[column] {
				unknown = append(unknown, table+"."+column)
			}
		}
	}
	sort.Strings(unknown)
	return unknown
}
