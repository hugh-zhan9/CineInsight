package models

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"reflect"
	"time"

	"github.com/jinzhu/now"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// SoftDeleteTime keeps GORM soft-delete behavior without exposing nested time fields to Wails bindings.
type SoftDeleteTime struct {
	value sql.NullTime
}

func (n *SoftDeleteTime) Scan(value interface{}) error {
	return n.value.Scan(value)
}

func (n SoftDeleteTime) Value() (driver.Value, error) {
	if !n.value.Valid {
		return nil, nil
	}
	return n.value.Time, nil
}

func (n SoftDeleteTime) MarshalJSON() ([]byte, error) {
	if n.value.Valid {
		return json.Marshal(n.value.Time)
	}
	return json.Marshal(nil)
}

func (n *SoftDeleteTime) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		n.Clear()
		return nil
	}
	if err := json.Unmarshal(data, &n.value.Time); err != nil {
		return err
	}
	n.value.Valid = true
	return nil
}

func (SoftDeleteTime) GormDataType() string {
	return "time"
}

func (n SoftDeleteTime) IsValid() bool {
	return n.value.Valid
}

func (n SoftDeleteTime) Time() time.Time {
	return n.value.Time
}

func (n *SoftDeleteTime) Set(t time.Time) {
	n.value = sql.NullTime{Time: t, Valid: true}
}

func (n *SoftDeleteTime) Clear() {
	n.value = sql.NullTime{}
}

func (SoftDeleteTime) QueryClauses(f *schema.Field) []clause.Interface {
	return []clause.Interface{softDeleteQueryClause{Field: f, ZeroValue: parseSoftDeleteZeroValue(f)}}
}

func (SoftDeleteTime) UpdateClauses(f *schema.Field) []clause.Interface {
	return []clause.Interface{softDeleteUpdateClause{Field: f, ZeroValue: parseSoftDeleteZeroValue(f)}}
}

func (SoftDeleteTime) DeleteClauses(f *schema.Field) []clause.Interface {
	return []clause.Interface{softDeleteDeleteClause{Field: f, ZeroValue: parseSoftDeleteZeroValue(f)}}
}

func parseSoftDeleteZeroValue(f *schema.Field) sql.NullString {
	if v, ok := f.TagSettings["ZEROVALUE"]; ok {
		if _, err := now.Parse(v); err == nil {
			return sql.NullString{String: v, Valid: true}
		}
	}
	return sql.NullString{Valid: false}
}

type softDeleteQueryClause struct {
	ZeroValue sql.NullString
	Field     *schema.Field
}

func (sd softDeleteQueryClause) Name() string {
	return ""
}

func (sd softDeleteQueryClause) Build(clause.Builder) {}

func (sd softDeleteQueryClause) MergeClause(*clause.Clause) {}

func (sd softDeleteQueryClause) ModifyStatement(stmt *gorm.Statement) {
	if _, ok := stmt.Clauses["soft_delete_enabled"]; !ok && !stmt.Statement.Unscoped {
		if c, ok := stmt.Clauses["WHERE"]; ok {
			if where, ok := c.Expression.(clause.Where); ok && len(where.Exprs) >= 1 {
				for _, expr := range where.Exprs {
					if orCond, ok := expr.(clause.OrConditions); ok && len(orCond.Exprs) == 1 {
						where.Exprs = []clause.Expression{clause.And(where.Exprs...)}
						c.Expression = where
						stmt.Clauses["WHERE"] = c
						break
					}
				}
			}
		}

		stmt.AddClause(clause.Where{Exprs: []clause.Expression{
			clause.Eq{Column: clause.Column{Table: clause.CurrentTable, Name: sd.Field.DBName}, Value: sd.ZeroValue},
		}})
		stmt.Clauses["soft_delete_enabled"] = clause.Clause{}
	}
}

type softDeleteUpdateClause struct {
	ZeroValue sql.NullString
	Field     *schema.Field
}

func (sd softDeleteUpdateClause) Name() string {
	return ""
}

func (sd softDeleteUpdateClause) Build(clause.Builder) {}

func (sd softDeleteUpdateClause) MergeClause(*clause.Clause) {}

func (sd softDeleteUpdateClause) ModifyStatement(stmt *gorm.Statement) {
	if stmt.SQL.Len() == 0 && !stmt.Statement.Unscoped {
		softDeleteQueryClause(sd).ModifyStatement(stmt)
	}
}

type softDeleteDeleteClause struct {
	ZeroValue sql.NullString
	Field     *schema.Field
}

func (sd softDeleteDeleteClause) Name() string {
	return ""
}

func (sd softDeleteDeleteClause) Build(clause.Builder) {}

func (sd softDeleteDeleteClause) MergeClause(*clause.Clause) {}

func (sd softDeleteDeleteClause) ModifyStatement(stmt *gorm.Statement) {
	if stmt.SQL.Len() == 0 && !stmt.Statement.Unscoped {
		curTime := stmt.DB.NowFunc()
		stmt.AddClause(clause.Set{{Column: clause.Column{Name: sd.Field.DBName}, Value: curTime}})
		stmt.SetColumn(sd.Field.DBName, curTime, true)

		if stmt.Schema != nil {
			_, queryValues := schema.GetIdentityFieldValuesMap(stmt.Context, stmt.ReflectValue, stmt.Schema.PrimaryFields)
			column, values := schema.ToQueryValues(stmt.Table, stmt.Schema.PrimaryFieldDBNames, queryValues)
			if len(values) > 0 {
				stmt.AddClause(clause.Where{Exprs: []clause.Expression{clause.IN{Column: column, Values: values}}})
			}

			if stmt.ReflectValue.CanAddr() && stmt.Dest != stmt.Model && stmt.Model != nil {
				_, queryValues = schema.GetIdentityFieldValuesMap(stmt.Context, reflect.ValueOf(stmt.Model), stmt.Schema.PrimaryFields)
				column, values = schema.ToQueryValues(stmt.Table, stmt.Schema.PrimaryFieldDBNames, queryValues)
				if len(values) > 0 {
					stmt.AddClause(clause.Where{Exprs: []clause.Expression{clause.IN{Column: column, Values: values}}})
				}
			}
		}

		softDeleteQueryClause(sd).ModifyStatement(stmt)
		stmt.AddClauseIfNotExists(clause.Update{})
		stmt.Build(stmt.DB.Callback().Update().Clauses...)
	}
}
