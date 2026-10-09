package models

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestCleanupConsolidationTaskHasNoMediaForeignKey(t *testing.T) {
	models := []interface{}{&CleanupConsolidationTask{}, &CleanupConsolidationTaskPlan{PlanJSON: "private-plan"}, &CleanupConsolidationTaskItem{JournalJSON: "private-journal"}}
	previous := -1
	for _, model := range models {
		parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatal(err)
		}
		if len(parsed.Relationships.Relations) != 0 || parsed.PrioritizedPrimaryField == nil || parsed.PrioritizedPrimaryField.Name != "ID" {
			t.Fatal("日志需要普通主键且不得有媒体级联，关系数量：", len(parsed.Relationships.Relations))
		}
		found := -1
		for i, registered := range AllModels() {
			if reflect.TypeOf(registered) == reflect.TypeOf(model) {
				found = i
			}
		}
		if found <= previous {
			t.Fatal("任务父/计划/明细必须依序进入迁移清单")
		}
		previous = found
		data, err := json.Marshal(model)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "private-") {
			t.Fatal("不得暴露内部JSON")
		}
	}
	parsed, _ := schema.Parse(&CleanupConsolidationTask{}, &sync.Map{}, schema.NamingStrategy{})
	if parsed.LookUpField("PlanJSON") != nil || parsed.LookUpField("JournalJSON") != nil {
		t.Fatal("热父行不能保存大JSON")
	}
}
