package migrator

import (
	"fmt"
	"strings"
	"video-master/database"

	"gorm.io/gorm"
)

// Deleted rows can still be referenced by retained user records. Copying only
// MAX(live id) would make a future insert silently take one of those identities.
func readConsumedIDs(source *gorm.DB, all []any) (map[string]int64, error) {
	result := make(map[string]int64)
	for _, model := range all {
		table, auto := autoIncrementIDTable(source, model)
		if !auto {
			continue
		}
		present, err := migrationTablePresent(source, table)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		maximum, err := maximumID(source, table)
		if err != nil {
			return nil, err
		}
		var consumed int64
		switch database.Backend(source.Dialector.Name()) {
		case database.BackendSQLite:
			sequencesPresent, err := migrationTablePresent(source, "sqlite_sequence")
			if err != nil {
				return nil, err
			}
			if sequencesPresent {
				if err := source.Raw("SELECT COALESCE(MAX(seq), 0) FROM sqlite_sequence WHERE name = ?", table).Scan(&consumed).Error; err != nil {
					return nil, fmt.Errorf("读取表 %s 的序列失败: %w", table, err)
				}
			}
		case database.BackendPostgres:
			sequence, err := postgresSequence(source, table)
			if err != nil {
				return nil, err
			}
			var state struct {
				LastValue int64
				IsCalled  bool
			}
			if err := source.Raw("SELECT last_value, is_called FROM " + sequence.quoted()).Scan(&state).Error; err != nil {
				return nil, fmt.Errorf("读取表 %s 的序列失败: %w", table, err)
			}
			if state.IsCalled {
				consumed = state.LastValue
			}
		default:
			return nil, fmt.Errorf("不支持源库的自增序列: %s", source.Dialector.Name())
		}
		if consumed < 0 {
			return nil, fmt.Errorf("表 %s 的序列无效", table)
		}
		result[table] = max(maximum, consumed)
	}
	return result, nil
}

func restoreConsumedIDs(target *gorm.DB, backend database.Backend, all []any, consumed map[string]int64) error {
	for _, model := range all {
		table, auto := autoIncrementIDTable(target, model)
		if !auto {
			continue
		}
		maximum, err := maximumID(target, table)
		if err != nil {
			return err
		}
		floor := max(maximum, consumed[table])
		switch backend {
		case database.BackendSQLite:
			updated := target.Exec("UPDATE sqlite_sequence SET seq = ? WHERE name = ?", floor, table)
			if updated.Error != nil {
				return fmt.Errorf("设置表 %s 的序列失败: %w", table, updated.Error)
			}
			if updated.RowsAffected == 0 && floor > 0 {
				if err := target.Exec("INSERT INTO sqlite_sequence (name, seq) VALUES (?, ?)", table, floor).Error; err != nil {
					return fmt.Errorf("设置表 %s 的序列失败: %w", table, err)
				}
			}
		case database.BackendPostgres:
			sequence, err := postgresSequence(target, table)
			if err != nil {
				return err
			}
			if err := target.Exec("SELECT setval(?::regclass, ?, ?)", sequence.quoted(), max(floor, 1), floor > 0).Error; err != nil {
				return fmt.Errorf("设置表 %s 的序列失败: %w", table, err)
			}
		default:
			return fmt.Errorf("不支持目标库的自增序列: %s", backend)
		}
	}
	return nil
}

func maximumID(db *gorm.DB, table string) (int64, error) {
	var maximum int64
	if err := db.Unscoped().Table(table).Select("COALESCE(MAX(id), 0)").Scan(&maximum).Error; err != nil {
		return 0, fmt.Errorf("读取表 %s 的最大ID失败: %w", table, err)
	}
	return maximum, nil
}

type sequenceName struct{ SchemaName, SequenceName string }

func (name sequenceName) quoted() string {
	quote := func(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
	return quote(name.SchemaName) + "." + quote(name.SequenceName)
}

func postgresSequence(db *gorm.DB, table string) (sequenceName, error) {
	var result sequenceName
	err := db.Raw(`SELECT n.nspname AS schema_name, c.relname AS sequence_name
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.oid = pg_get_serial_sequence(?, 'id')::regclass`, table).Scan(&result).Error
	if err != nil {
		return result, fmt.Errorf("查询表 %s 的序列失败: %w", table, err)
	}
	if result.SchemaName == "" || result.SequenceName == "" {
		return result, fmt.Errorf("表 %s 缺少自增序列", table)
	}
	return result, nil
}

// Unlike Migrator.HasTable, retain metadata-query failures instead of treating a
// closed or cancelled source connection as an empty database.
func migrationTablePresent(db *gorm.DB, table string) (bool, error) {
	var present bool
	var err error
	switch database.Backend(db.Dialector.Name()) {
	case database.BackendSQLite:
		err = db.Raw("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)", table).Scan(&present).Error
	case database.BackendPostgres:
		err = db.Raw("SELECT to_regclass(?) IS NOT NULL", table).Scan(&present).Error
	default:
		return false, fmt.Errorf("不支持源库的自增序列: %s", db.Dialector.Name())
	}
	if err != nil {
		return false, fmt.Errorf("检查源表 %s 失败: %w", table, err)
	}
	return present, nil
}
