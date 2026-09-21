package models

import (
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// AllModels() 的顺序既是建表顺序，也是双向迁移器逐表复制的顺序。被外键引用的表
// 必须排在引用方之前，否则切换数据库后端时会在复制到引用方那张表时炸掉
// FOREIGN KEY constraint failed——而且只对"那张表里真有数据"的用户炸。
//
// 这一条是结构性守卫：不靠夹具碰巧覆盖到，而是把每个模型的关联关系解析出来
// 逐条核对。新加表、挪动顺序、给模型加外键，都会在这里立刻被拦住。
//
// many2many 不参与：隐式关联表没有模型，迁移器在全部模型复制完之后才搬它们。
func TestAllModelsIsTopologicallyOrdered(t *testing.T) {
	namer := schema.NamingStrategy{}
	cache := &sync.Map{}

	all := AllModels()
	position := make(map[string]int, len(all))
	parsed := make([]*schema.Schema, len(all))
	for index, model := range all {
		sch, err := schema.Parse(model, cache, namer)
		if err != nil {
			t.Fatalf("解析模型 %T 失败: %v", model, err)
		}
		if previous, duplicate := position[sch.Table]; duplicate {
			t.Fatalf("表 %s 在 AllModels() 里出现了两次（index %d 与 %d）", sch.Table, previous, index)
		}
		position[sch.Table] = index
		parsed[index] = sch
	}

	for index, sch := range parsed {
		// belongs-to / has-one（外键在本表）：被引用的表必须在前。
		for _, rel := range sch.Relationships.BelongsTo {
			requireBefore(t, position, rel.FieldSchema.Table, sch.Table, index, "belongs-to "+rel.Name)
		}
		// has-many / has-one（外键在对方表）：本表是被引用方，必须在对方之前。
		for _, rel := range append(append([]*schema.Relationship{}, sch.Relationships.HasMany...), sch.Relationships.HasOne...) {
			child := rel.FieldSchema.Table
			childIndex, known := position[child]
			if !known {
				continue
			}
			if childIndex < index {
				t.Fatalf("拓扑顺序错了：%s（index %d）的外键指向 %s（index %d），"+
					"被引用的表必须排在前面（关系 has-many/has-one %s）",
					child, childIndex, sch.Table, index, rel.Name)
			}
		}
	}
}

func requireBefore(t *testing.T, position map[string]int, referenced, referencing string, referencingIndex int, relation string) {
	t.Helper()
	if referenced == referencing {
		return // 自引用（如作品集封面指向自身）不构成顺序约束
	}
	referencedIndex, known := position[referenced]
	if !known {
		// 不在 AllModels() 里的表（如 GORM 隐式关联表）不参与排序约束。
		return
	}
	if referencedIndex >= referencingIndex {
		t.Fatalf("拓扑顺序错了：%s（index %d）的外键指向 %s（index %d），"+
			"被引用的表必须排在前面（关系 %s）",
			referencing, referencingIndex, referenced, referencedIndex, relation)
	}
}

// parseMovieChartSchemas 解析年度电影榜单的三个模型，供下面几条结构性守卫复用。
func parseMovieChartSchemas(t *testing.T) []*schema.Schema {
	t.Helper()
	namer := schema.NamingStrategy{}
	cache := &sync.Map{}
	all := []interface{}{&MovieChartEntry{}, &MovieChartMark{}, &MovieChartYearState{}}
	parsed := make([]*schema.Schema, 0, len(all))
	for _, model := range all {
		sch, err := schema.Parse(model, cache, namer)
		if err != nil {
			t.Fatalf("解析模型 %T 失败: %v", model, err)
		}
		parsed = append(parsed, sch)
	}
	return parsed
}

// 2026-09-02 禁令：布尔列不得默认 true，数值列不得默认非零。
//
// GORM 把零值字段当"未设置"并替换成标签默认值，双向迁移器的 Unscoped().Create
// 因此会把用户存的 false / 0 写成标签里的那个非零默认值——换一次后端，数据就变了。
// 榜单三张表的每个默认值都必须等于该列零值该有的语义。
//
// 守卫解析模型而不是读迁移后的库：标签是源头，库里是它的投影，拦在源头这一侧
// 两个后端都不用跑。
//
// 判类型必须看 GORMDataType 而不是 DataType：gorm v1.31.1 的 schema/field.go:320-328
// 会用 type: 标签的原文**覆盖** DataType（只有 bool/int/uint/float/string/time/bytes
// 这七个字面量才保留），本仓库已经在写这种拼法（models/video.go:17 的
// type:numeric(3,1)）。真按 DataType 分派的话，将来有人写
// `gorm:"type:smallint;not null;default:5"`，DataType 就是 "smallint"，switch 直接
// 落空、守卫放行——而这正是它要拦的那一类。GORMDataType 在覆盖发生之前就赋好了
// （同文件 316-318 行），留着的仍是 Int。
func TestMovieChartModelsHaveNoNonZeroNumericDefaults(t *testing.T) {
	for _, sch := range parseMovieChartSchemas(t) {
		for _, field := range sch.Fields {
			// 自增主键的默认值由数据库给，不带标签值，不在禁令范围内。
			if field.AutoIncrement || !field.HasDefaultValue {
				continue
			}
			value := strings.Trim(strings.TrimSpace(field.DefaultValue), "'")
			switch field.GORMDataType {
			case schema.Bool:
				if value != "false" {
					t.Fatalf("%s.%s 是布尔列，默认值只能是 false，实际 %q",
						sch.Table, field.DBName, field.DefaultValue)
				}
			case schema.Int, schema.Uint, schema.Float:
				if value != "0" {
					t.Fatalf("%s.%s 是数值列，默认值只能是 0，实际 %q",
						sch.Table, field.DBName, field.DefaultValue)
				}
			}
		}
	}
}

// 三张表之间、以及对既有表都不许有外键。
//
// 标记表按豆瓣 ID 字符串关联条目缓存表是**有意**的逻辑关联：缓存会被整年重建，
// 一旦改成外键，一次重建就会级联删掉用户自己产生的"已看/想看/跳过"记录。
// WatchlistEntryID 同理只存裸 uint——声明成 belongs-to 会让用户删一条片单记录就
// 连坐删掉标记。这条守卫顺带保住"追加在 AllModels() 末尾"的前提。
func TestMovieChartModelsDeclareNoForeignKeys(t *testing.T) {
	for _, sch := range parseMovieChartSchemas(t) {
		relations := &sch.Relationships
		if len(relations.BelongsTo)+len(relations.HasOne)+len(relations.HasMany)+len(relations.Many2Many) != 0 {
			t.Fatalf("%s 不得声明任何关联（会被 GORM 建成外键）: %v", sch.Table, relations.Relations)
		}
	}
}

// detail_claim 的长度上限必须是 32。
//
// 认领标识用 hex.EncodeToString 于 16 字节得到的 32 个十六进制字符；uuid.NewString()
// 是 36 字符，SQLite 不校验长度会默默存下，Postgres 直接报 value too long。
// 这正是 models/watchlist.go:52-55 记录过的那类"改 A 坏 B"。
func TestMovieChartDetailClaimSizeFitsHexIdentifier(t *testing.T) {
	sch := parseMovieChartSchemas(t)[0]
	field := sch.LookUpField("detail_claim")
	if field == nil {
		t.Fatal("movie_chart_entries 缺少 detail_claim 列")
	}
	if field.Size != 32 {
		t.Fatalf("detail_claim 的长度上限必须是 32，实际 %d", field.Size)
	}
}
