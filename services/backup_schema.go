package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
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
// 列为空。只作为清单里的参考信息（schema.tables），兼容性判定看 models。
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

// retiredSchemaColumns 是有意从模型里删掉的表与列（表 → 列；列表为空表示整张表退役）。
// **删除模型字段或整个模型时必须登记到这里**：老版本做的备份，清单 models 里还带着它们；不登记，
// 当前版本会把自己更早的备份判成「来自更新的版本」而拒绝恢复。
var retiredSchemaColumns = map[string][]string{}

var (
	modelSchemaOnce sync.Once
	modelSchema     map[string][]string
	modelSchemaErr  error
)

// semanticRawTables 是只在 Postgres 上用裸 DDL 建的语义向量表与它们的列（database/semantic_vector.go、
// image_semantic_vector.go）。改 DDL 时同步这里；TestModelSchemaGolden 会提醒更新快照。
var semanticRawTables = map[string][]string{
	"video_semantic_vectors": {"dimension", "embedding", "generation", "model_identifier", "updated_at", "video_id"},
	"image_semantic_vectors": {"dimension", "embedding", "generation", "image_id", "model_identifier", "updated_at"},
}

// currentModelSchema 是本版本的模型结构（表 → 列，按字典序）：AllModels 与它们的多对多连接表、Postgres 上的
// 语义索引模型与裸 DDL 向量表，用 GORM 的默认命名解析（与 database.Init 一致），不看任何库里的实际表——老库里
// AutoMigrate 留下的遗留列不算当前结构。清单的 models 记的就是它；恢复与校验拿备份的 models 和它比（D-DH-03）。
func currentModelSchema() (map[string][]string, error) {
	modelSchemaOnce.Do(func() {
		cache := &sync.Map{}
		tables := map[string][]string{}
		semanticModels := []any{
			&models.SemanticIndexProfile{}, &models.VideoSemanticIndex{}, &models.SemanticIndexAttempt{},
			&models.ImageSemanticIndex{}, &models.ImageSemanticIndexAttempt{},
		}
		for table, columns := range semanticRawTables {
			tables[table] = sortedColumns(columns)
		}
		for _, model := range append(models.AllModels(), semanticModels...) {
			parsed, err := schema.Parse(model, cache, schema.NamingStrategy{})
			if err != nil {
				modelSchemaErr = fmt.Errorf("解析模型 %T 失败: %w", model, err)
				return
			}
			tables[parsed.Table] = sortedColumns(parsed.DBNames)
			for _, relation := range parsed.Relationships.Many2Many {
				if relation.JoinTable != nil {
					tables[relation.JoinTable.Table] = sortedColumns(relation.JoinTable.DBNames)
				}
			}
		}
		modelSchema = tables
	})
	return modelSchema, modelSchemaErr
}

func sortedColumns(columns []string) []string {
	sorted := append([]string{}, columns...)
	sort.Strings(sorted)
	return sorted
}

// unknownModelSchema 列出备份清单 models 里当前版本不认识的表（「表」）与列（「表.列」），按字典序；
// 空表示兼容。认识 = 在当前模型结构里，或登记在 retired 里（整表退役或该列退役）。
// 清单没有 models（只有开发中的版本写过）时无从判断，按兼容处理。
func unknownModelSchema(recorded, current, retired map[string][]string) []string {
	unknown := []string{}
	for table, columns := range recorded {
		retiredColumns, tableRetired := retired[table]
		if tableRetired && len(retiredColumns) == 0 {
			continue
		}
		known := map[string]bool{}
		for _, column := range current[table] {
			known[column] = true
		}
		for _, column := range retiredColumns {
			known[column] = true
		}
		if _, ok := current[table]; !ok && !tableRetired {
			unknown = append(unknown, table)
			continue
		}
		for _, column := range columns {
			if !known[column] {
				unknown = append(unknown, table+"."+column)
			}
		}
	}
	sort.Strings(unknown)
	return unknown
}

// backupSchemaUnknown 是恢复与校验共用的判定：返回清单 models 里当前版本不认识的表与列。
func backupSchemaUnknown(manifest *BackupManifest) ([]string, error) {
	current, err := currentModelSchema()
	if err != nil {
		return nil, err
	}
	return unknownModelSchema(manifest.Models, current, retiredSchemaColumns), nil
}
