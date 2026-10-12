package services

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// 代码维护的引用悬挂（4.3 dangling_refs）：这些表对媒体只有逻辑引用、没有外键。父记录在 Unscoped 下
// 也查不到（直接查父表，不带 deleted_at 条件——回收站里的父记录仍算存在）才算悬挂。
// 历史记录（工作台成品、迁移暂存来源）只报；其余是本身没有独立意义的引用行，修复时删除。
// 观看笔记按设计比视频活得久，不在此列。

type danglingRefSpec struct {
	key        string
	table      string
	label      string
	predicate  func(t string) string // t 是被检查表的表名或别名
	reportOnly bool
	// faceClusters：删的是人脸观测，删完按实际观测重算受影响的簇（与删观测的既有路径同口径）。
	faceClusters bool
}

// healthParentMissing 是「父表里找不到这一行」：不加 deleted_at 条件，即 Unscoped。
func healthParentMissing(parent, ref string) string {
	return fmt.Sprintf("NOT EXISTS (SELECT 1 FROM %s parent_row WHERE parent_row.id = %s)", parent, ref)
}

// healthMediaMissing 是多态媒体引用的悬挂：kind 列决定父表。
func healthMediaMissing(kindRef, idRef string, parents map[string]string) string {
	clauses := ""
	for _, kind := range []string{"video", "image", "person_avatar"} {
		parent, ok := parents[kind]
		if !ok {
			continue
		}
		if clauses != "" {
			clauses += " OR "
		}
		clauses += fmt.Sprintf("(%s = '%s' AND %s)", kindRef, kind, healthParentMissing(parent, idRef))
	}
	return "(" + clauses + ")"
}

var healthMediaParents = map[string]string{"video": "videos", "image": "images"}

var danglingRefSpecs = []danglingRefSpec{
	{key: "near_duplicate_dismissals", table: "near_duplicate_dismissals", label: "近重复忽略（视频）", predicate: func(t string) string {
		return "(" + healthParentMissing("videos", t+".video_low_id") + " OR " + healthParentMissing("videos", t+".video_high_id") + ")"
	}},
	{key: "image_near_duplicate_dismissals", table: "image_near_duplicate_dismissals", label: "近重复忽略（图片）", predicate: func(t string) string {
		return "(" + healthParentMissing("images", t+".image_low_id") + " OR " + healthParentMissing("images", t+".image_high_id") + ")"
	}},
	{key: "clip_dismissals", table: "clip_dismissals", label: "片段忽略", predicate: func(t string) string {
		return "(" + healthParentMissing("videos", t+".video_full_id") + " OR " + healthParentMissing("videos", t+".video_clip_id") + ")"
	}},
	{key: "video_version_suggestion_dismissals", table: "video_version_suggestion_dismissals", label: "版本建议忽略", predicate: func(t string) string {
		return "(" + healthParentMissing("videos", t+".video_low_id") + " OR " + healthParentMissing("videos", t+".video_high_id") + ")"
	}},
	{key: "face_observations", table: "face_observations", label: "人脸观测", faceClusters: true, predicate: func(t string) string {
		return healthMediaMissing(t+".media_kind", t+".media_id", map[string]string{"video": "videos", "image": "images", "person_avatar": "people"})
	}},
	{key: "face_relation_writes", table: "face_relation_writes", label: "人脸关系记账", predicate: func(t string) string {
		return healthMediaMissing(t+".media_kind", t+".media_id", healthMediaParents)
	}},
	{key: "playback_queue_entries", table: "playback_queue_entries", label: "待播队列", predicate: func(t string) string {
		return healthParentMissing("videos", t+".video_id")
	}},
	{key: "automation_rule_writes", table: "automation_rule_writes", label: "规则账本", predicate: func(t string) string {
		return healthMediaMissing(t+".media_kind", t+".media_id", healthMediaParents)
	}},
	{key: "metadata_batch_edit_items", table: "metadata_batch_edit_items", label: "批量编辑明细", predicate: func(t string) string {
		return fmt.Sprintf("EXISTS (SELECT 1 FROM metadata_batch_edits batch WHERE batch.id = %s.batch_id AND %s)",
			t, healthMediaMissing("batch.kind", t+".media_id", healthMediaParents))
	}},
	{key: "video_edit_items.output_video_id", table: "video_edit_items", label: "工作台成品（历史记录，只报）", reportOnly: true, predicate: func(t string) string {
		return t + ".output_video_id IS NOT NULL AND " + healthParentMissing("videos", t+".output_video_id")
	}},
	{key: "migration_staged_sources", table: "migration_staged_sources", label: "迁移暂存来源（历史记录，只报）", reportOnly: true, predicate: func(t string) string {
		return t + ".video_id IS NOT NULL AND " + healthParentMissing("videos", t+".video_id")
	}},
}

// checkDanglingRefs 按表统计悬挂行，示例给前几条的 id。
func checkDanglingRefs(ctx context.Context, db *gorm.DB) (DataHealthCheckResult, error) {
	groups := []DataHealthGroup{}
	var total, repairable int64
	for _, spec := range danglingRefSpecs {
		where := spec.predicate("t")
		group := DataHealthGroup{Key: spec.key, Label: spec.label, Repairable: !spec.reportOnly, Examples: []string{}}
		if err := db.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %s t WHERE %s", spec.table, where)).Scan(&group.Count).Error; err != nil {
			return DataHealthCheckResult{}, fmt.Errorf("%s: %w", spec.table, err)
		}
		if group.Count == 0 {
			continue
		}
		var ids []uint
		if err := db.Raw(fmt.Sprintf("SELECT t.id FROM %s t WHERE %s ORDER BY t.id LIMIT ?", spec.table, where), dataHealthExampleLimit).
			Scan(&ids).Error; err != nil {
			return DataHealthCheckResult{}, fmt.Errorf("%s: %w", spec.table, err)
		}
		for _, id := range ids {
			group.Examples = append(group.Examples, fmt.Sprintf("id %d", id))
		}
		total += group.Count
		if group.Repairable {
			repairable += group.Count
		}
		groups = append(groups, group)
	}
	if total == 0 {
		return DataHealthCheckResult{Status: DataHealthStatusOK, Summary: "没有悬挂引用"}, nil
	}
	return DataHealthCheckResult{
		Status:     DataHealthStatusProblem,
		Count:      total,
		Summary:    fmt.Sprintf("%d 行引用的记录已彻底不存在，其中 %d 行可以清理", total, repairable),
		Groups:     groups,
		Repairable: repairable > 0,
	}, nil
}

// repairDanglingRefs 删除可清理的悬挂行：每批 500 条一个短事务，删除条件就是检查条件（期间新产生的
// 正常行不会被误删）；按 id 递增推进，批与批之间看取消。返回已删除的行数。
func repairDanglingRefs(ctx context.Context, db *gorm.DB) (int64, error) {
	var removed int64
	for _, spec := range danglingRefSpecs {
		if spec.reportOnly {
			continue
		}
		var lastID uint
		for {
			if err := ctx.Err(); err != nil {
				return removed, err
			}
			var ids []uint
			if err := db.Raw(fmt.Sprintf("SELECT t.id FROM %s t WHERE t.id > ? AND %s ORDER BY t.id LIMIT ?", spec.table, spec.predicate("t")),
				lastID, dataHealthBatchSize).Scan(&ids).Error; err != nil {
				return removed, err
			}
			if len(ids) == 0 {
				break
			}
			lastID = ids[len(ids)-1]
			deleted, err := deleteDanglingBatch(ctx, db, spec, ids)
			removed += deleted
			if err != nil {
				return removed, err
			}
		}
	}
	return removed, nil
}

func deleteDanglingBatch(ctx context.Context, db *gorm.DB, spec danglingRefSpec, ids []uint) (int64, error) {
	var deleted int64
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var clusterIDs []uint
		if spec.faceClusters {
			if err := tx.Raw(fmt.Sprintf("SELECT DISTINCT %s.cluster_id FROM %s WHERE %s.id IN ? AND %s.cluster_id IS NOT NULL AND %s",
				spec.table, spec.table, spec.table, spec.table, spec.predicate(spec.table)), ids).Scan(&clusterIDs).Error; err != nil {
				return err
			}
		}
		result := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s.id IN ? AND %s", spec.table, spec.table, spec.predicate(spec.table)), ids)
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		if len(clusterIDs) > 0 {
			if _, err := recomputeFaceClustersTx(ctx, tx, clusterIDs); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}
