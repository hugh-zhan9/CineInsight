package services

import (
	"context"
	"fmt"
	"sort"
	"video-master/database"
	"video-master/models"
)

// 私有指纹不会被 encoding/json 自动保存；必须从完整 Sources 显式重建，
// 包括 clip 的有方向指纹，才能在重启后正确重验忽略决定。
func restoreConsolidationFingerprints(plan *cleanupConsolidationPlan) {
	restoreConsolidationCurrentFingerprints(plan, nil)
}

func restoreConsolidationCurrentFingerprints(plan *cleanupConsolidationPlan, journal *cleanupConsolidationJournal) {
	if plan.Analysis == nil {
		return
	}
	byID := make(map[uint]FileMigrationSource, len(plan.Sources))
	for _, source := range plan.Sources {
		byID[source.Video.ID] = source.File
	}
	if journal != nil {
		for i, item := range plan.Preview.Items {
			if !item.Stay && len(journal.Items[i].Files) > 0 {
				byID[item.VideoID] = journal.Items[i].Files[0].TargetSnapshot
			}
		}
	}
	plan.Analysis.sourceFingerprints = make(map[uint]string, len(byID))
	for id, file := range byID {
		plan.Analysis.sourceFingerprints[id] = cleanupFileFingerprint(file.Size, file.ModTimeNS)
	}
	for i := range plan.Analysis.ClipGroups {
		group := &plan.Analysis.ClipGroups[i]
		full, clip := byID[group.Full.ID], byID[group.Clip.ID]
		group.fullFingerprint = mediaProbeFingerprint{size: full.Size, modTimeNS: full.ModTimeNS}
		group.clipFingerprint = mediaProbeFingerprint{size: clip.Size, modTimeNS: clip.ModTimeNS}
	}
}

func validateConsolidationSameSource(ctx context.Context, groups []CleanupSameSourceGroup) error {
	ids := make([]uint, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.RelationID)
	}
	relations := make(map[uint]models.VideoSameSourceRelation, len(groups))
	for start := 0; start < len(ids); start += 400 {
		end := start + 400
		if end > len(ids) {
			end = len(ids)
		}
		var rows []models.VideoSameSourceRelation
		if err := database.DB.WithContext(ctx).Where("id IN ?", ids[start:end]).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			relations[row.ID] = row
		}
	}
	for _, group := range groups {
		relation, exists := relations[group.RelationID]
		pair := cleanupVideoPairKey(group.Preferred.ID, group.Alternative.ID)
		if !exists || relation.VideoAID != pair[0] || relation.VideoBID != pair[1] || relation.Status != models.VideoSameSourceStatusDetected || (relation.ReviewedAt != nil) != group.Confirmed {
			return fmt.Errorf("同源审阅决定已变化，请重新审阅")
		}
	}
	return nil
}

func validateConsolidationSnapshot(ctx context.Context, plan *cleanupConsolidationPlan, journal *cleanupConsolidationJournal) (map[uint]models.Video, error) {
	if plan.Analysis == nil {
		return nil, fmt.Errorf("缺少原始分析，请重新审阅")
	}
	restoreConsolidationCurrentFingerprints(plan, journal)
	if journal != nil {
		if err := validateConsolidationMigrationDismissals(ctx, plan); err != nil {
			return nil, err
		}
	}
	original, _, err := consolidationAnalysisGroups(plan.Analysis)
	if err != nil {
		return nil, err
	}
	fresh, err := filterCleanupReviewDecisions(plan.Analysis)
	if err != nil {
		return nil, err
	}
	current, _, err := consolidationAnalysisGroups(fresh)
	if err != nil {
		return nil, err
	}
	if !sameConsolidationGroupSet(original, current) {
		return nil, fmt.Errorf("忽略或同源决定已变化，请重新审阅")
	}
	if err := validateConsolidationSameSource(ctx, plan.Analysis.SameSourceGroups); err != nil {
		return nil, err
	}

	// 精确组经预览接受的原地等价替换是已确认的选择；不能再按目录重新挑选。
	for _, selected := range plan.Preview.Groups {
		if selected.Kind == "exact" && !selected.KeeperPinned {
			key, err := consolidationGroupKey(selected.Kind, selected.MemberIDs)
			if err != nil {
				return nil, err
			}
			group, ok := current[key]
			if !ok || !consolidationContains(group.members, selected.KeeperID) {
				return nil, fmt.Errorf("保留项已变化")
			}
			group.keeper = selected.KeeperID
			current[key] = group
		}
	}
	request := CleanupConsolidationRequest{Groups: plan.Preview.Groups, Protections: plan.Protections}
	if err := validateConsolidationRequest(request, current); err != nil {
		return nil, err
	}
	expected := make(map[uint]FileMigrationSource, len(plan.Sources))
	for _, source := range plan.Sources {
		expected[source.Video.ID] = source.File
	}
	if journal != nil {
		for i, item := range plan.Preview.Items {
			if item.Stay {
				continue
			}
			if journal.Items[i].Phase != "committed" || len(journal.Items[i].Files) == 0 {
				return nil, fmt.Errorf("任务还有未完成项，请重新审阅")
			}
			source := journal.Items[i].Files[0].TargetSnapshot
			if source.Identity == "" || source.Size != item.Files[0].Source.Size {
				return nil, fmt.Errorf("缺少已验证目标版本，请重新审阅")
			}
			source.Path = item.DestinationPath
			source.RealPath = item.DestinationPath
			source.Identity = journal.Items[i].Files[0].PublishedIdentity
			expected[item.VideoID] = source
		}
	}
	ids := make([]uint, 0, len(expected))
	for id := range expected {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	videos := make(map[uint]models.Video, len(ids))
	for start := 0; start < len(ids); start += 400 {
		end := start + 400
		if end > len(ids) {
			end = len(ids)
		}
		var rows []models.Video
		if err := database.DB.WithContext(ctx).Where("id IN ?", ids[start:end]).Find(&rows).Error; err != nil {
			return nil, err
		}
		if len(rows) != end-start {
			return nil, fmt.Errorf("分析中的视频已删除，请重新审阅")
		}
		for _, video := range rows {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			source := expected[video.ID]
			if video.Path != source.Path || video.IsStale {
				return nil, fmt.Errorf("视频 %d 的路径或可用状态已变化，请重新审阅", video.ID)
			}
			if err := verifyConsolidationSource(source); err != nil {
				return nil, err
			}
			videos[video.ID] = video
		}
	}
	return videos, nil
}

// ReviewConsolidation 重新核对所有保护范围，只返回本轮组；LockedIDs 只包含外部
// 保护，本轮 keeper 由 UI 动态计算，切换保留项不会永远锁住旧 keeper。
func (s *CleanupService) ReviewConsolidation(ctx context.Context, taskID uint) (*CleanupConsolidationReview, error) {
	if taskID == 0 {
		return nil, fmt.Errorf("请指定集中整理任务")
	}
	var task models.CleanupConsolidationTask
	if err := database.DB.WithContext(ctx).First(&task, taskID).Error; err != nil {
		return nil, err
	}
	if task.Status != "completed" {
		return nil, fmt.Errorf("仅全部完成的集中整理可以进入本次清理审阅")
	}
	plan, journal, err := decodeConsolidation(ctx, task)
	if err != nil {
		return nil, err
	}
	videos, err := validateConsolidationSnapshot(ctx, &plan, &journal)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]CleanupConsolidationGroup)
	locked, external := make(map[uint]bool), make(map[uint]bool)
	for _, group := range plan.Preview.Groups {
		key, _ := consolidationGroupKey(group.Kind, group.MemberIDs)
		selected[key] = group
		locked[group.KeeperID] = true
	}
	for _, protection := range plan.Protections {
		key, _ := consolidationGroupKey(protection.Kind, protection.MemberIDs)
		if _, ok := selected[key]; ok && !protection.Skipped {
			continue
		}
		if protection.KeeperID != 0 {
			external[protection.KeeperID] = true
			locked[protection.KeeperID] = true
		}
		if protection.Skipped {
			for _, id := range protection.MemberIDs {
				external[id] = true
				locked[id] = true
			}
		}
	}
	result := &CleanupConsolidationReview{TaskID: task.ID, Groups: plan.Preview.Groups, Analysis: &CleanupAnalysis{Thresholds: plan.Analysis.Thresholds}, LockedIDs: []uint{}}
	sameSourceByKey := make(map[string]CleanupSameSourceGroup, len(plan.Analysis.SameSourceGroups))
	for _, group := range plan.Analysis.SameSourceGroups {
		key, _ := consolidationGroupKey("same-source", []uint{group.Preferred.ID, group.Alternative.ID})
		sameSourceByKey[key] = group
	}
	clipByKey := make(map[string]CleanupClipGroup, len(plan.Analysis.ClipGroups))
	for _, group := range plan.Analysis.ClipGroups {
		key, _ := consolidationGroupKey("clip", []uint{group.Full.ID, group.Clip.ID})
		clipByKey[key] = group
	}
	reasons := make(map[string]string)
	for _, entry := range []struct {
		kind   string
		groups []CleanupDuplicateGroup
	}{{"exact", plan.Analysis.DuplicateGroups}, {"near", plan.Analysis.NearDuplicateGroups}} {
		for _, group := range entry.groups {
			ids := []uint{group.Original.ID}
			for _, video := range group.Candidates {
				ids = append(ids, video.ID)
			}
			key, _ := consolidationGroupKey(entry.kind, ids)
			reasons[key] = group.Reason
		}
	}
	var ids []uint
	seen := make(map[uint]bool)
	for i := range result.Groups {
		group := &result.Groups[i]
		kept := make([]uint, 0, len(group.SelectedIDs))
		for _, id := range group.SelectedIDs {
			if !locked[id] {
				kept = append(kept, id)
			}
		}
		group.SelectedIDs = kept
		for _, id := range group.MemberIDs {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		key, _ := consolidationGroupKey(group.Kind, group.MemberIDs)
		switch group.Kind {
		case "exact", "near":
			members := []models.Video{}
			for _, id := range group.MemberIDs {
				if id != group.KeeperID {
					members = append(members, videos[id])
				}
			}
			duplicate := CleanupDuplicateGroup{Original: videos[group.KeeperID], Candidates: members, Reason: reasons[key]}
			if group.Kind == "exact" {
				result.Analysis.DuplicateGroups = append(result.Analysis.DuplicateGroups, duplicate)
			} else {
				result.Analysis.NearDuplicateGroups = append(result.Analysis.NearDuplicateGroups, duplicate)
			}
		case "same-source":
			original := sameSourceByKey[key]
			original.Preferred = videos[original.Preferred.ID]
			original.Alternative = videos[original.Alternative.ID]
			result.Analysis.SameSourceGroups = append(result.Analysis.SameSourceGroups, original)
		case "clip":
			original := clipByKey[key]
			original.Full = videos[original.Full.ID]
			original.Clip = videos[original.Clip.ID]
			result.Analysis.ClipGroups = append(result.Analysis.ClipGroups, original)
		}
	}
	for id := range external {
		result.LockedIDs = append(result.LockedIDs, id)
	}
	sort.Slice(result.LockedIDs, func(i, j int) bool { return result.LockedIDs[i] < result.LockedIDs[j] })
	curation, err := loadCleanupVideoCuration(ctx, ids)
	if err != nil {
		return nil, err
	}
	result.Analysis.Curation = curation
	return result, nil
}

// 同一内容经本任务验证搬迁后，目标卷可能采用另一时间精度。忽略决定的两侧
// 独立接受迁移前/后的有效版本，覆盖一项已提交而另一项仍在复制的中间时刻。
// 这里只解释决定；当前文件仍由 validateConsolidationSnapshot 严格按目标快照核验。
func validateConsolidationMigrationDismissals(ctx context.Context, plan *cleanupConsolidationPlan) error {
	before := make(map[uint]string, len(plan.Sources))
	changed := false
	for _, source := range plan.Sources {
		before[source.Video.ID] = cleanupFileFingerprint(source.File.Size, source.File.ModTimeNS)
		changed = changed || before[source.Video.ID] != plan.Analysis.sourceFingerprints[source.Video.ID]
	}
	if !changed {
		return nil
	}
	applies := func(id uint, fingerprint string) bool {
		return cleanupDismissalSideApplies(fingerprint, before, id) || cleanupDismissalSideApplies(fingerprint, plan.Analysis.sourceFingerprints, id)
	}
	groups, _, err := consolidationAnalysisGroups(plan.Analysis)
	if err != nil {
		return err
	}
	memberships := make(map[uint]map[string]bool)
	for key, group := range groups {
		if group.kind != "near" && group.kind != "same-source" && group.kind != "clip" {
			continue
		}
		for _, id := range group.members {
			if memberships[id] == nil {
				memberships[id] = make(map[string]bool)
			}
			memberships[id][key] = true
		}
	}
	var dismissals []models.NearDuplicateDismissal
	if err := database.DB.WithContext(ctx).Find(&dismissals).Error; err != nil {
		return err
	}
	for _, record := range dismissals {
		if !applies(record.VideoLowID, record.FingerprintA) || !applies(record.VideoHighID, record.FingerprintB) {
			continue
		}
		for group := range memberships[record.VideoLowID] {
			if memberships[record.VideoHighID][group] {
				return fmt.Errorf("迁移期间忽略决定已变化，请重新审阅")
			}
		}
	}
	if len(plan.Analysis.ClipGroups) > 0 {
		clips, err := loadClipDismissals()
		if err != nil {
			return err
		}
		for _, group := range plan.Analysis.ClipGroups {
			record, ok := clips[[2]uint{group.Full.ID, group.Clip.ID}]
			if ok && applies(group.Full.ID, cleanupFileFingerprint(record.FullSourceSize, record.FullSourceModTimeNS)) && applies(group.Clip.ID, cleanupFileFingerprint(record.ClipSourceSize, record.ClipSourceModTimeNS)) {
				return fmt.Errorf("迁移期间截取忽略决定已变化，请重新审阅")
			}
		}
	}
	if len(plan.Analysis.LowDuration)+len(plan.Analysis.LowResolution) > 0 {
		single, err := loadCleanupVideoDismissals()
		if err != nil {
			return err
		}
		for _, entry := range []struct {
			category string
			videos   []models.Video
		}{{models.CleanupDismissalCategoryShort, plan.Analysis.LowDuration}, {models.CleanupDismissalCategoryLow, plan.Analysis.LowResolution}} {
			for _, video := range entry.videos {
				record, ok := single[cleanupVideoDismissalKey{videoID: video.ID, category: entry.category}]
				if ok && applies(video.ID, record) {
					return fmt.Errorf("迁移期间单项忽略决定已变化，请重新审阅")
				}
			}
		}
	}
	return nil
}
