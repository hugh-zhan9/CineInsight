package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 模型结构快照（D-DH-03，审阅 R5）：备份清单的 models 与「来自更新版本」的判定依赖 currentModelSchema。
// 这里把它钉在 testdata/model_schema_golden.json：
//   - 新增表或列时快照要跟着更新（UPDATE_MODEL_SCHEMA_GOLDEN=1 go test ./services -run TestModelSchemaGolden）；
//   - 快照里有、当前模型却没有的表或列，必须登记进 retiredSchemaColumns——否则老版本做的备份会被当前版本
//     误判成「来自更新的版本」而拒绝恢复。
const modelSchemaGoldenPath = "testdata/model_schema_golden.json"

func TestModelSchemaGolden(t *testing.T) {
	current, err := currentModelSchema()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_MODEL_SCHEMA_GOLDEN") == "1" {
		writeModelSchemaGolden(t, current)
		return
	}
	data, err := os.ReadFile(modelSchemaGoldenPath)
	if err != nil {
		t.Fatalf("读取模型结构快照失败（UPDATE_MODEL_SCHEMA_GOLDEN=1 go test ./services -run TestModelSchemaGolden 生成）: %v", err)
	}
	var golden map[string][]string
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	if added := unknownModelSchema(current, golden, nil); len(added) > 0 {
		t.Fatalf("模型多了快照里没有的表或列：%s\n确认无误后更新快照：UPDATE_MODEL_SCHEMA_GOLDEN=1 go test ./services -run TestModelSchemaGolden",
			strings.Join(added, "、"))
	}
	if removed := unknownModelSchema(golden, current, retiredSchemaColumns); len(removed) > 0 {
		t.Fatalf("快照里的表或列已不在当前模型里：%s\n删除模型字段或模型时必须把它们登记进 retiredSchemaColumns（services/backup_schema.go），"+
			"否则老版本的备份会被判成「来自更新的版本」；登记后再更新快照。", strings.Join(removed, "、"))
	}
}

func writeModelSchemaGolden(t *testing.T, current map[string][]string) {
	t.Helper()
	tables := make([]string, 0, len(current))
	for table := range current {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	var builder strings.Builder
	builder.WriteString("{\n")
	for index, table := range tables {
		columns, _ := json.Marshal(current[table])
		name, _ := json.Marshal(table)
		builder.WriteString("  " + string(name) + ": " + string(columns))
		if index < len(tables)-1 {
			builder.WriteString(",")
		}
		builder.WriteString("\n")
	}
	builder.WriteString("}\n")
	if err := os.MkdirAll(filepath.Dir(modelSchemaGoldenPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelSchemaGoldenPath, []byte(builder.String()), 0644); err != nil {
		t.Fatal(err)
	}
}

// 退役登记生效：快照里的列从模型里删掉、登记进 retired 后不再报；没登记时报出来。
func TestModelSchemaGoldenRetiredCheck(t *testing.T) {
	golden := map[string][]string{"settings": {"id", "fanza_cookie"}, "old_table": {"id"}}
	current := map[string][]string{"settings": {"id"}}
	if removed := unknownModelSchema(golden, current, nil); strings.Join(removed, ",") != "old_table,settings.fanza_cookie" {
		t.Fatalf("没登记的删除应报出: %v", removed)
	}
	retired := map[string][]string{"settings": {"fanza_cookie"}, "old_table": {}}
	if removed := unknownModelSchema(golden, current, retired); len(removed) != 0 {
		t.Fatalf("登记过的删除不应再报: %v", removed)
	}
}
