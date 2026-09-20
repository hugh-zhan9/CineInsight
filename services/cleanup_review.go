package services

import "video-master/models"

// loadCleanupReviewPairs 收口“不是同片 / 不是同源”的否决，所有相似关系类别共用。
// 截取片段的“忽略”有方向与源文件版本边界，仍单独保存在 ClipDismissal。
func loadCleanupReviewPairs() (map[[2]uint]struct{}, error) {
	pairs, err := loadNearDuplicateDismissals()
	if err != nil {
		return nil, err
	}
	rejected, err := loadRejectedSameSourcePairs()
	if err != nil {
		return nil, err
	}
	for pair := range rejected {
		pairs[pair] = struct{}{}
	}
	return pairs, nil
}

// filterCleanupReviewDecisions 只过滤结果快照，不扫描媒体或重跑分析。
// 旧缓存与分析期间保存的决定都要在交给调用方前复核，不能只相信计算时的排除集。
func filterCleanupReviewDecisions(analysis *CleanupAnalysis) (*CleanupAnalysis, error) {
	if analysis == nil {
		return nil, nil
	}
	result := *analysis
	if len(analysis.NearDuplicateGroups)+len(analysis.SameSourceGroups)+len(analysis.ClipGroups) == 0 {
		return &result, nil
	}
	pairs, err := loadCleanupReviewPairs()
	if err != nil {
		return nil, err
	}
	result.NearDuplicateGroups = make([]CleanupDuplicateGroup, 0, len(analysis.NearDuplicateGroups))
	for _, group := range analysis.NearDuplicateGroups {
		result.NearDuplicateGroups = append(result.NearDuplicateGroups, splitReviewedCleanupGroup(group, pairs)...)
	}
	result.SameSourceGroups = make([]CleanupSameSourceGroup, 0, len(analysis.SameSourceGroups))
	for _, group := range analysis.SameSourceGroups {
		if _, denied := pairs[cleanupVideoPairKey(group.Preferred.ID, group.Alternative.ID)]; !denied {
			result.SameSourceGroups = append(result.SameSourceGroups, group)
		}
	}
	if len(analysis.ClipGroups) > 0 {
		dismissed, err := loadClipDismissals()
		if err != nil {
			return nil, err
		}
		result.ClipGroups = make([]CleanupClipGroup, 0, len(analysis.ClipGroups))
		for _, group := range analysis.ClipGroups {
			if _, denied := pairs[cleanupVideoPairKey(group.Full.ID, group.Clip.ID)]; denied {
				continue
			}
			full := clipSequence{video: group.Full, sourceSize: group.fullFingerprint.size, sourceMod: group.fullFingerprint.modTimeNS}
			clip := clipSequence{video: group.Clip, sourceSize: group.clipFingerprint.size, sourceMod: group.clipFingerprint.modTimeNS}
			if !clipDismissalStillApplies(dismissed, full, clip) {
				result.ClipGroups = append(result.ClipGroups, group)
			}
		}
	}
	return &result, nil
}

// 组内任意两人被判为不同片，都不能再放在同一组；其余成员仍可审阅。
// 与近似重复生成器一样，各组互不重叠、组内两两相容，保留原来的推荐顺序。
func splitReviewedCleanupGroup(group CleanupDuplicateGroup, denied map[[2]uint]struct{}) []CleanupDuplicateGroup {
	remaining := append([]models.Video{group.Original}, group.Candidates...)
	groups := make([]CleanupDuplicateGroup, 0, 1)
	for len(remaining) > 1 {
		members := []models.Video{remaining[0]}
		next := make([]models.Video, 0, len(remaining)-1)
		for _, candidate := range remaining[1:] {
			allowed := true
			for _, member := range members {
				if _, blocked := denied[cleanupVideoPairKey(member.ID, candidate.ID)]; blocked {
					allowed = false
					break
				}
			}
			if allowed {
				members = append(members, candidate)
			} else {
				next = append(next, candidate)
			}
		}
		if len(members) > 1 {
			groups = append(groups, CleanupDuplicateGroup{Original: members[0], Candidates: members[1:], Reason: group.Reason})
		}
		remaining = next
	}
	return groups
}
