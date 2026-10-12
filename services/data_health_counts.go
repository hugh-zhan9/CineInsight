package services

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// 派生计数（4.3 derived_counts）：只收逐项核实过、纯由明细推出的计数（2026-10-12 核对写入点）：
//   - subtitle_index_states.segment_count：唯一写入点 replaceSubtitleIndexTx / upsertSubtitleIndexStateTx
//     在同一事务里先删光该视频的字幕段再按插入条数（或 0）写入，等于该视频 subtitle_segments 的行数；
//   - scene_index_states.segment_count：replaceVideoSegments / recordStateFailure 同一事务里删光该视频
//     全部画面段再写 len(rows)（或 0），按模型清理时段与状态一起删，等于该视频 scene_visual_segments 的行数；
//   - face_clusters.observation_count：建簇记 1、归入 +1，删观测或移出后 recomputeFaceClustersTx 按
//     cluster_id 的实际观测行重算——定义就是 face_observations 里指向该簇的行数；
//   - video_frame_hash_sequences.frame_count：写入时等于 len(hashes)，编码为每帧 8 字节（encodeFrameHashes）；
//     长度不是 8 的倍数的序列是截断的坏行（decodeFrameHashes 当作没有序列），不算计数漂移，不进检查。
// 明确不检查：videos.play_count、videos.last_played_at、images.view_count——不是由明细派生的，
// play_count 在播放账本建立之前就有累计值，历史播放只补过一条「历史补记」。

type derivedCountSpec struct {
	key      string
	table    string
	keyCol   string
	noun     string // 示例里 keyCol 的叫法
	column   string
	label    string
	expected func(t string) string // t 是被检查表的表名或别名
	guard    func(t string) string // 可选：只在满足它的行上比较
}

var derivedCountSpecs = []derivedCountSpec{
	{key: "subtitle_index_states.segment_count", table: "subtitle_index_states", keyCol: "video_id", noun: "视频", column: "segment_count",
		label: "字幕索引段数", expected: func(t string) string {
			return "(SELECT COUNT(*) FROM subtitle_segments detail WHERE detail.video_id = " + t + ".video_id)"
		}},
	{key: "scene_index_states.segment_count", table: "scene_index_states", keyCol: "video_id", noun: "视频", column: "segment_count",
		label: "场景索引段数", expected: func(t string) string {
			return "(SELECT COUNT(*) FROM scene_visual_segments detail WHERE detail.video_id = " + t + ".video_id)"
		}},
	{key: "face_clusters.observation_count", table: "face_clusters", keyCol: "id", noun: "簇", column: "observation_count",
		label: "人脸簇观测数", expected: func(t string) string {
			return "(SELECT COUNT(*) FROM face_observations detail WHERE detail.cluster_id = " + t + ".id)"
		}},
	{key: "video_frame_hash_sequences.frame_count", table: "video_frame_hash_sequences", keyCol: "video_id", noun: "视频", column: "frame_count",
		label: "帧哈希帧数", expected: func(t string) string {
			return "(LENGTH(" + t + ".hashes) / 8)"
		}, guard: func(t string) string {
			return "LENGTH(" + t + ".hashes) % 8 = 0"
		}},
}

// predicate 是漂移判定：记的数不等于按明细算出的数。
func (spec derivedCountSpec) predicate(t string) string {
	where := fmt.Sprintf("%s.%s <> %s", t, spec.column, spec.expected(t))
	if spec.guard != nil {
		where = spec.guard(t) + " AND " + where
	}
	return where
}

// checkDerivedCounts 按计数列统计漂移行，示例给「记的 → 实际」。
func checkDerivedCounts(ctx context.Context, db *gorm.DB) (DataHealthCheckResult, error) {
	groups := []DataHealthGroup{}
	var total int64
	for _, spec := range derivedCountSpecs {
		where := spec.predicate("t")
		group := DataHealthGroup{Key: spec.key, Label: spec.label, Repairable: true, Examples: []string{}}
		if err := db.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %s t WHERE %s", spec.table, where)).Scan(&group.Count).Error; err != nil {
			return DataHealthCheckResult{}, fmt.Errorf("%s: %w", spec.key, err)
		}
		if group.Count == 0 {
			continue
		}
		var samples []struct {
			RowKey   uint
			Recorded int64
			Actual   int64
		}
		query := fmt.Sprintf("SELECT t.%s AS row_key, t.%s AS recorded, %s AS actual FROM %s t WHERE %s ORDER BY t.%s LIMIT ?",
			spec.keyCol, spec.column, spec.expected("t"), spec.table, where, spec.keyCol)
		if err := db.Raw(query, dataHealthExampleLimit).Scan(&samples).Error; err != nil {
			return DataHealthCheckResult{}, fmt.Errorf("%s: %w", spec.key, err)
		}
		for _, sample := range samples {
			group.Examples = append(group.Examples, fmt.Sprintf("%s %d：记 %d，实际 %d", spec.noun, sample.RowKey, sample.Recorded, sample.Actual))
		}
		total += group.Count
		groups = append(groups, group)
	}
	if total == 0 {
		return DataHealthCheckResult{Status: DataHealthStatusOK, Summary: "派生计数与明细一致"}, nil
	}
	return DataHealthCheckResult{
		Status:     DataHealthStatusProblem,
		Count:      total,
		Summary:    fmt.Sprintf("%d 行计数与明细不一致，可按明细重算", total),
		Groups:     groups,
		Repairable: true,
	}, nil
}

// repairDerivedCounts 按明细重算漂移的计数：每批 500 行一个短事务，UPDATE 带同一个漂移判定。
func repairDerivedCounts(ctx context.Context, db *gorm.DB) (int64, error) {
	var fixed int64
	for _, spec := range derivedCountSpecs {
		var lastKey uint
		for {
			if err := ctx.Err(); err != nil {
				return fixed, err
			}
			var keys []uint
			query := fmt.Sprintf("SELECT t.%s FROM %s t WHERE t.%s > ? AND %s ORDER BY t.%s LIMIT ?",
				spec.keyCol, spec.table, spec.keyCol, spec.predicate("t"), spec.keyCol)
			if err := db.Raw(query, lastKey, dataHealthBatchSize).Scan(&keys).Error; err != nil {
				return fixed, err
			}
			if len(keys) == 0 {
				break
			}
			lastKey = keys[len(keys)-1]
			var affected int64
			err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				result := tx.Exec(fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s.%s IN ? AND %s",
					spec.table, spec.column, spec.expected(spec.table), spec.table, spec.keyCol, spec.predicate(spec.table)), keys)
				affected = result.RowsAffected
				return result.Error
			})
			if err != nil {
				return fixed, err
			}
			fixed += affected
		}
	}
	return fixed, nil
}
