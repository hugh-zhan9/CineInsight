package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 数据体检的只报不修各项（4.3）：数据库文件完整性、外键、唯一索引、备份状态、孤儿文件汇总。

// checkSQLiteIntegrity 是 PRAGMA quick_check。坏了没有修复，只能建议从备份恢复。
func checkSQLiteIntegrity(ctx context.Context, db *gorm.DB) (DataHealthCheckResult, error) {
	quick, err := sqliteQuickCheckAll(db)
	if err != nil {
		return DataHealthCheckResult{}, err
	}
	if len(quick) == 1 && quick[0] == "ok" {
		return DataHealthCheckResult{Status: DataHealthStatusOK, Summary: "数据库文件完整"}, nil
	}
	return DataHealthCheckResult{
		Status:   DataHealthStatusProblem,
		Count:    int64(len(quick)),
		Summary:  fmt.Sprintf("数据库文件有 %d 处问题，建议从备份恢复", len(quick)),
		Examples: limitStrings(quick, dataHealthExampleLimit),
	}, nil
}

// checkSQLiteForeignKeys 是 PRAGMA foreign_key_check：子行指向的父行不存在（例如老库在没开外键时写进的行）。
// 按「子表 → 父表」分组报告，只报不修。
func checkSQLiteForeignKeys(ctx context.Context, db *gorm.DB) (DataHealthCheckResult, error) {
	rows, err := db.Raw("PRAGMA foreign_key_check").Rows()
	if err != nil {
		return DataHealthCheckResult{}, err
	}
	defer rows.Close()
	groups := map[string]*DataHealthGroup{}
	var total int64
	for rows.Next() {
		var table, parent string
		var rowID *int64
		var fkID int64
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			return DataHealthCheckResult{}, err
		}
		key := table + "→" + parent
		group := groups[key]
		if group == nil {
			group = &DataHealthGroup{Key: key, Label: fmt.Sprintf("%s 指向 %s", table, parent), Examples: []string{}}
			groups[key] = group
		}
		group.Count++
		total++
		if rowID != nil && len(group.Examples) < dataHealthExampleLimit {
			group.Examples = append(group.Examples, fmt.Sprintf("第 %d 行", *rowID))
		}
	}
	if err := rows.Err(); err != nil {
		return DataHealthCheckResult{}, err
	}
	if total == 0 {
		return DataHealthCheckResult{Status: DataHealthStatusOK, Summary: "没有外键悬挂"}, nil
	}
	return DataHealthCheckResult{
		Status:  DataHealthStatusProblem,
		Count:   total,
		Summary: fmt.Sprintf("%d 行指向已不存在的父记录", total),
		Groups:  sortedHealthGroups(groups),
	}, nil
}

func sortedHealthGroups(groups map[string]*DataHealthGroup) []DataHealthGroup {
	list := make([]DataHealthGroup, 0, len(groups))
	for _, group := range groups {
		list = append(list, *group)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return list
}

func limitStrings(values []string, limit int) []string {
	if len(values) > limit {
		return append([]string{}, values[:limit]...)
	}
	return append([]string{}, values...)
}

func healthColumnList(columns []string) string {
	return strings.Join(columns, ", ")
}

// expectedUniqueIndex 是启动时应建好、建失败只记日志的唯一索引（database.ensureCoreQueryIndexes、
// ensureAITaggingIndexes）与活动路径部分唯一索引。与 database/database.go 的语句一一对应；
// TestDataHealthUniqueIndexListMatchesSchema 钉住两边一致。
type expectedUniqueIndex struct {
	name    string
	table   string
	columns []string
	where   string
}

var dataHealthUniqueIndexes = []expectedUniqueIndex{
	{"idx_videos_path_active", "videos", []string{"path"}, "deleted_at IS NULL AND path <> ''"},
	{"idx_images_path_active", "images", []string{"path"}, "deleted_at IS NULL"},
	{"idx_saved_library_views_name_active", "saved_library_views", []string{"name"}, "deleted_at IS NULL"},
	{"idx_enhancement_tasks_active_source", "video_enhancement_tasks", []string{"video_id"}, "status IN ('queued', 'running', 'cancel_requested')"},
	{"idx_ai_tag_approval_video_tag", "ai_tag_approval_records", []string{"video_id", "tag_id"}, ""},
	{"idx_ai_tag_approval_records_candidate_id", "ai_tag_approval_records", []string{"candidate_id"}, ""},
	{"idx_ai_tag_agent_step_run_round", "ai_tag_agent_steps", []string{"video_id", "evidence_fingerprint", "attempt", "round"}, ""},
	{"idx_video_visual_fingerprints_video_id", "video_visual_fingerprints", []string{"video_id"}, ""},
	{"idx_video_same_source_pair", "video_same_source_relations", []string{"video_a_id", "video_b_id"}, ""},
}

// checkMissingUniqueIndexes 查清单里的唯一索引是否在位；缺失的列出它挡不住的重复组。只报不修：
// 去重要人来判断哪一条留下。
func checkMissingUniqueIndexes(ctx context.Context, db *gorm.DB) (DataHealthCheckResult, error) {
	groups := []DataHealthGroup{}
	for _, index := range dataHealthUniqueIndexes {
		present, err := healthIndexExists(db, index.name)
		if err != nil {
			return DataHealthCheckResult{}, err
		}
		if present {
			continue
		}
		group, err := uniqueIndexDuplicates(db, index)
		if err != nil {
			return DataHealthCheckResult{}, err
		}
		groups = append(groups, group)
	}
	if len(groups) == 0 {
		return DataHealthCheckResult{Status: DataHealthStatusOK, Summary: "该有的唯一索引都在"}, nil
	}
	return DataHealthCheckResult{
		Status:  DataHealthStatusProblem,
		Count:   int64(len(groups)),
		Summary: fmt.Sprintf("%d 个唯一索引缺失；重启应用会重试建索引，有重复时要先人工去重", len(groups)),
		Groups:  groups,
	}, nil
}

func healthIndexExists(db *gorm.DB, name string) (bool, error) {
	var count int64
	query := `SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`
	if dataHealthIsSQLite(db) {
		query = `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`
	}
	if err := db.Raw(query, name).Scan(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// uniqueIndexDuplicates 统计缺失的唯一索引挡不住的重复组：按索引列分组、计数大于 1；任一列为 NULL
// 的行不参与（唯一索引本就不约束它们）。示例只给组内最小 id 与条数，不给路径等原值（G-3）。
func uniqueIndexDuplicates(db *gorm.DB, index expectedUniqueIndex) (DataHealthGroup, error) {
	conditions := []string{}
	if index.where != "" {
		conditions = append(conditions, "("+index.where+")")
	}
	for _, column := range index.columns {
		conditions = append(conditions, column+" IS NOT NULL")
	}
	grouped := fmt.Sprintf("SELECT MIN(id) AS first_id, COUNT(*) AS row_count FROM %s WHERE %s GROUP BY %s HAVING COUNT(*) > 1",
		index.table, strings.Join(conditions, " AND "), healthColumnList(index.columns))
	group := DataHealthGroup{Key: index.name, Label: fmt.Sprintf("%s（%s）", index.table, healthColumnList(index.columns)), Examples: []string{}}
	if err := db.Raw("SELECT COUNT(*) FROM (" + grouped + ") duplicate_groups").Scan(&group.Count).Error; err != nil {
		return group, err
	}
	var samples []struct {
		FirstID  uint
		RowCount int64
	}
	if err := db.Raw(grouped+" ORDER BY first_id LIMIT ?", dataHealthExampleLimit).Scan(&samples).Error; err != nil {
		return group, err
	}
	for _, sample := range samples {
		group.Examples = append(group.Examples, fmt.Sprintf("id %d 等 %d 条重复", sample.FirstID, sample.RowCount))
	}
	group.Detail = "索引缺失"
	if group.Count > 0 {
		group.Detail = fmt.Sprintf("索引缺失，%d 组重复", group.Count)
	}
	return group, nil
}

// checkBackups 汇总备份状态，链接到设置页「备份」分区：从没成功过、或最近一次校验没通过记为问题；
// 有旧格式备份或还没校验过记为提醒。
func (s *DataHealthService) checkBackups(ctx context.Context, _ *gorm.DB) (DataHealthCheckResult, error) {
	result := DataHealthCheckResult{Link: "settings:backup", Status: DataHealthStatusOK}
	if s.backups == nil {
		return result, fmt.Errorf("备份服务未接入")
	}
	summary, err := s.backups.DataHealthSummary(ctx)
	if err != nil {
		return result, err
	}
	result.Count = int64(summary.LegacyCount)
	success := DataHealthGroup{Key: "last_success", Label: "最近一次成功备份", Detail: "还没有成功的备份", Examples: []string{}}
	if summary.LastSuccessAt != nil {
		success.Detail = describeHealthAge(s.now().Sub(*summary.LastSuccessAt)) + "前"
	} else {
		result.Status = DataHealthStatusProblem
	}
	verify := DataHealthGroup{Key: "last_verification", Label: "最近一次校验", Detail: "还没有校验过", Examples: []string{}}
	switch {
	case summary.LastVerification == nil:
		result.Status = worseHealthStatus(result.Status, DataHealthStatusWarning)
	case summary.LastVerification.OK:
		verify.Detail = describeHealthAge(s.now().Sub(summary.LastVerification.VerifiedAt)) + "前，通过"
	default:
		verify.Detail = describeHealthAge(s.now().Sub(summary.LastVerification.VerifiedAt)) + "前，未通过"
		verify.Examples = limitStrings(summary.LastVerification.Problems, dataHealthExampleLimit)
		result.Status = DataHealthStatusProblem
	}
	legacy := DataHealthGroup{Key: "legacy", Label: "旧格式备份（仅数据库、未校验）", Count: int64(summary.LegacyCount),
		Detail: fmt.Sprintf("%d 份", summary.LegacyCount), Examples: []string{}}
	if summary.LegacyCount > 0 {
		result.Status = worseHealthStatus(result.Status, DataHealthStatusWarning)
	}
	result.Groups = []DataHealthGroup{success, verify, legacy}
	result.Summary = fmt.Sprintf("共 %d 份备份；最近一次成功备份：%s；最近一次校验：%s", summary.Total, success.Detail, verify.Detail)
	return result, nil
}

// checkOrphanFiles 只汇总孤儿文件（数量与大小），回收在「失联」页签。
func (s *DataHealthService) checkOrphanFiles(ctx context.Context, _ *gorm.DB) (DataHealthCheckResult, error) {
	files, bytes, err := s.orphans(ctx)
	if err != nil {
		return DataHealthCheckResult{}, err
	}
	result := DataHealthCheckResult{Link: "data-care:lost", Status: DataHealthStatusOK, Count: int64(files), Bytes: bytes,
		Summary: "没有孤儿文件"}
	if files > 0 {
		result.Status = DataHealthStatusWarning
		result.Summary = fmt.Sprintf("%d 个孤儿文件，可在「失联」页签回收", files)
	}
	return result, nil
}

func worseHealthStatus(current, candidate string) string {
	rank := map[string]int{DataHealthStatusOK: 0, DataHealthStatusWarning: 1, DataHealthStatusProblem: 2}
	if rank[candidate] > rank[current] {
		return candidate
	}
	return current
}

// describeHealthAge 把时长说成「N 分钟 / 小时 / 天」。
func describeHealthAge(age time.Duration) string {
	switch {
	case age < time.Hour:
		return fmt.Sprintf("%d 分钟", max(int(age.Minutes()), 0))
	case age < 48*time.Hour:
		return fmt.Sprintf("%d 小时", int(age.Hours()))
	default:
		return fmt.Sprintf("%d 天", int(age.Hours()/24))
	}
}
