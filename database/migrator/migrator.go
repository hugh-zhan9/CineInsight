// Package migrator 在两个数据库后端之间全量复制数据。
//
// 三条贯穿全文的性质：
//
//   - **复制而不是移动。** 源库全程只读，迁移完成后仍然完整存在。这是"改回配置
//     重启即可回滚"能成立的唯一理由，任何改动都不得破坏它。
//   - **保留主键。** 关联表、外键、回收站恢复路径全都靠 ID 对上，重新分配 ID
//     等于把这些关系全部打断。
//   - **语义向量不跨后端携带。** 向量表由 PrepareSemanticVectorStorage 用裸 DDL
//     建，不在 models.AllModels() 里；索引元数据（SemanticIndexProfile 等）同样
//     不在。所以按 AllModels 驱动的复制天然不会搬它们，目标库启动后会报"需要
//     重建"——这正是想要的结果，不需要额外的清理逻辑。
package migrator

import (
	"context"
	"fmt"
	"reflect"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// batchSize 单批搬运的行数。取值不敏感，够大以摊薄往返开销，够小以免大表一次性
// 读进内存。
const batchSize = 500

// joinTables 是 GORM 的隐式多对多关联表。它们没有对应的模型，AllModels 里看不到，
// 必须单独列出来——漏掉它们的话，视频和图片的标签关联会在迁移后全部丢失。
//
// VideoPerson 与 CollectionVideo 是显式模型，已经在 AllModels 里，不在此列。
var joinTables = []joinTable{
	{Name: "video_tags", Columns: []string{"video_id", "tag_id"}},
	{Name: "image_tags", Columns: []string{"image_id", "tag_id"}},
}

type joinTable struct {
	Name    string
	Columns []string
}

// Progress 是一次迁移的进度快照。
type Progress struct {
	Table      string `json:"table"`
	TableIndex int    `json:"table_index"`
	TableTotal int    `json:"table_total"`
	Rows       int64  `json:"rows"`
}

// Options 描述一次迁移。
type Options struct {
	Source *gorm.DB
	Target *gorm.DB
	// TargetBackend 决定要不要在收尾时重置序列。从调用方传入而不是从
	// Target.Dialector 推断，是为了让测试能显式覆盖。
	TargetBackend database.Backend
	OnProgress    func(Progress)
}

// Result 是一次成功迁移的汇总。
type Result struct {
	Tables    int              `json:"tables"`
	RowCounts map[string]int64 `json:"row_counts"`
}

// migrationMarker 是"这个库已经被完整迁移过"的标记。
//
// 它刻意不在 models.AllModels() 里：它描述的是库本身的迁移状态，不是应用数据，
// 也不该被下一次迁移搬到别处去。目标库存在数据却没有这条标记，就说明上一次迁移
// 中途失败了。
type migrationMarker struct {
	ID        uint   `gorm:"primarykey"`
	Completed bool   `gorm:"not null;default:false"`
	Note      string `gorm:"not null;default:''"`
}

func (migrationMarker) TableName() string { return "cineinsight_migration_marker" }

// ErrTargetNotEmpty 目标库里已经有数据。
type ErrTargetNotEmpty struct {
	Table string
	Rows  int64
}

func (e *ErrTargetNotEmpty) Error() string {
	return fmt.Sprintf("目标库不是空的（%s 有 %d 行）：迁移只往空库里写，请先清空目标库或换一个", e.Table, e.Rows)
}

// ErrTargetHalfMigrated 目标库里有上一次未完成迁移的残留。
var ErrTargetHalfMigrated = fmt.Errorf("目标库残留着上一次未完成的迁移，必须先清空才能重试")

// Preflight 检查目标库能不能作为迁移目标。不写任何数据。
func Preflight(target *gorm.DB) error {
	if target == nil {
		return fmt.Errorf("目标库未连接")
	}
	// 有标记但未完成 = 上次迁移中途挂了。这种库不能续写：已经搬过去的部分
	// 无从判断完整性，继续写只会得到一个看起来正常、实际残缺的库。
	if target.Migrator().HasTable(&migrationMarker{}) {
		var marker migrationMarker
		if err := target.First(&marker).Error; err == nil && !marker.Completed {
			return ErrTargetHalfMigrated
		}
	}
	for _, model := range models.AllModels() {
		if !target.Migrator().HasTable(model) {
			continue
		}
		var count int64
		if err := target.Unscoped().Model(model).Count(&count).Error; err != nil {
			return fmt.Errorf("检查目标库失败: %w", err)
		}
		if count > 0 {
			return &ErrTargetNotEmpty{Table: tableNameOf(target, model), Rows: count}
		}
	}
	return nil
}

// Migrate 把源库的全部应用数据复制到目标库。
func Migrate(ctx context.Context, opts Options) (*Result, error) {
	if opts.Source == nil || opts.Target == nil {
		return nil, fmt.Errorf("源库或目标库未连接")
	}
	if err := Preflight(opts.Target); err != nil {
		return nil, err
	}
	if err := opts.Target.AutoMigrate(models.AllModels()...); err != nil {
		return nil, fmt.Errorf("目标库建表失败: %w", err)
	}
	if err := opts.Target.AutoMigrate(&migrationMarker{}); err != nil {
		return nil, fmt.Errorf("目标库建迁移标记失败: %w", err)
	}
	// 先落一条未完成标记：从这一刻起目标库就是"半迁移"状态，中途失败时
	// Preflight 认得出来。
	if err := opts.Target.Where("1 = 1").Delete(&migrationMarker{}).Error; err != nil {
		return nil, fmt.Errorf("重置迁移标记失败: %w", err)
	}
	if err := opts.Target.Create(&migrationMarker{Completed: false, Note: "migration in progress"}).Error; err != nil {
		return nil, fmt.Errorf("写入迁移标记失败: %w", err)
	}

	all := models.AllModels()
	total := len(all) + len(joinTables)
	result := &Result{Tables: total, RowCounts: map[string]int64{}}

	for index, model := range all {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := tableNameOf(opts.Source, model)
		rows, err := copyModel(opts, model)
		if err != nil {
			return nil, fmt.Errorf("复制表 %s 失败: %w", name, err)
		}
		result.RowCounts[name] = rows
		report(opts, Progress{Table: name, TableIndex: index + 1, TableTotal: total, Rows: rows})
	}

	for offset, join := range joinTables {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, err := copyJoinTable(opts, join)
		if err != nil {
			return nil, fmt.Errorf("复制关联表 %s 失败: %w", join.Name, err)
		}
		result.RowCounts[join.Name] = rows
		report(opts, Progress{Table: join.Name, TableIndex: len(all) + offset + 1, TableTotal: total, Rows: rows})
	}

	if opts.TargetBackend == database.BackendPostgres {
		if err := resetPostgresSequences(opts.Target, all); err != nil {
			return nil, err
		}
	}

	if err := opts.Target.Model(&migrationMarker{}).Where("1 = 1").
		Updates(map[string]any{"completed": true, "note": "migration completed"}).Error; err != nil {
		return nil, fmt.Errorf("标记迁移完成失败: %w", err)
	}
	return result, nil
}

// copyModel 分批把一张表搬过去。
//
// Unscoped：软删除行必须一起搬。回收站、"否认后不得重复确认"这些语义都依赖那些行
// 存在，漏掉它们等于在迁移中悄悄清空了回收站。
//
// SkipHooks：模型上的 BeforeCreate 之类钩子会改写数据（补时间戳、算派生字段）。
// 迁移要的是逐字节照搬，不是重新生成一份。
func copyModel(opts Options, model any) (int64, error) {
	modelType := reflect.TypeOf(model)
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}
	batchPtr := reflect.New(reflect.SliceOf(modelType))
	target := opts.Target.Session(&gorm.Session{SkipHooks: true, Logger: opts.Target.Logger})

	var copied int64
	err := opts.Source.Unscoped().Model(model).FindInBatches(batchPtr.Interface(), batchSize,
		func(tx *gorm.DB, _ int) error {
			batch := batchPtr.Elem()
			if batch.Len() == 0 {
				return nil
			}
			if err := target.Unscoped().Create(batchPtr.Interface()).Error; err != nil {
				return err
			}
			copied += int64(batch.Len())
			return nil
		}).Error
	if err != nil {
		return 0, err
	}
	return copied, nil
}

// copyJoinTable 搬运没有模型的隐式关联表。
func copyJoinTable(opts Options, join joinTable) (int64, error) {
	if !opts.Source.Migrator().HasTable(join.Name) {
		return 0, nil
	}
	var rows []map[string]any
	if err := opts.Source.Table(join.Name).Find(&rows).Error; err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	for start := 0; start < len(rows); start += batchSize {
		end := start + batchSize
		if end > len(rows) {
			end = len(rows)
		}
		if err := opts.Target.Table(join.Name).Create(rows[start:end]).Error; err != nil {
			return 0, err
		}
	}
	return int64(len(rows)), nil
}

// resetPostgresSequences 把每张表的自增序列推到当前最大 ID。
//
// 必须做：迁移是带着显式 ID 插入的，而显式 ID 不会推进 BIGSERIAL 序列。不重置的话，
// 迁移之后用户新建的第一条记录就会撞上已存在的主键——而且报错发生在迁移结束很久
// 之后，现场早就没了，非常难查。
func resetPostgresSequences(target *gorm.DB, all []any) error {
	for _, model := range all {
		if !target.Migrator().HasTable(model) {
			continue
		}
		table, autoIncrement := autoIncrementIDTable(target, model)
		// 不是每张表都有自增 id：video_people、collection_videos 这类是复合主键。
		// 对它们调 pg_get_serial_sequence('t','id') 不会返回 NULL，而是直接报
		// "column id does not exist"，所以必须先按模型 schema 判掉。
		if !autoIncrement {
			continue
		}
		sql := fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE((SELECT MAX(id) FROM %s), 1))
			 WHERE pg_get_serial_sequence('%s', 'id') IS NOT NULL`,
			table, table, table)
		if err := target.Exec(sql).Error; err != nil {
			return fmt.Errorf("重置表 %s 的序列失败: %w", table, err)
		}
	}
	return nil
}

// autoIncrementIDTable 返回表名，以及这张表是否有一个自增的 id 主键。
func autoIncrementIDTable(db *gorm.DB, model any) (string, bool) {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		return fmt.Sprintf("%T", model), false
	}
	field := stmt.Schema.LookUpField("id")
	return stmt.Schema.Table, field != nil && field.PrimaryKey && field.AutoIncrement
}

func tableNameOf(db *gorm.DB, model any) string {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		return fmt.Sprintf("%T", model)
	}
	return stmt.Schema.Table
}

func report(opts Options, progress Progress) {
	if opts.OnProgress != nil {
		opts.OnProgress(progress)
	}
}
