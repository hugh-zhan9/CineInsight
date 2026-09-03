package models

import (
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
