package services

import (
	"context"
	"os"
	"sort"
	"video-master/database"
	"video-master/models"
)

// legacyLoadCleanupNearDuplicateGroups 是 2026-10 性能优化之前 loadCleanupNearDuplicateGroups
// 的原样拷贝（整库 Preload("Video.Tags")、逐行 os.Stat），作为近似重复整类输出的参照实现。
func legacyLoadCleanupNearDuplicateGroups(ctx context.Context, excluded map[[2]uint]struct{}, presentVideoIDs map[uint]struct{}) ([]CleanupDuplicateGroup, map[[2]uint]struct{}, int64, error) {
	var rows []models.VideoPerceptualHash
	if err := database.DB.WithContext(ctx).Preload("Video.Tags").Order("video_id ASC").Find(&rows).Error; err != nil {
		return nil, nil, 0, err
	}
	scope, err := loadCleanupPathScope()
	if err != nil {
		return nil, nil, 0, err
	}
	var staleCount int64
	hashedVideoIDs := make(map[uint]struct{}, len(rows))
	valid := rows[:0]
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, err
		}
		if !scope.contains(row.Video.Path) {
			continue
		}
		hashedVideoIDs[row.VideoID] = struct{}{}
		info, err := os.Stat(row.Video.Path)
		if err != nil || !info.Mode().IsRegular() {
			// 文件读不到是另一回事，由 SkippedUnavailable 负责报，不算指纹问题。
			continue
		}
		if !perceptualHashRowComplete(row) {
			staleCount++
			continue
		}
		if info.Size() != row.SourceSize || info.ModTime().UnixNano() != row.SourceModTimeNS {
			staleCount++
			continue
		}
		valid = append(valid, row)
	}
	// 从没回填过的视频在这张表里根本没有行，早先因此一个都不进计数：一个没跑过
	// 补全的库会显示"感知哈希待重算 0"，提示与「重算感知哈希」按钮永不出现，而
	// 近似重复恒为空——用户看到的是"功能不准"，实际是从没启动过。口径与
	// stale_frame_hash_count 对齐（D-CD04）。
	for id := range presentVideoIDs {
		if _, hashed := hashedVideoIDs[id]; !hashed {
			staleCount++
		}
	}
	if len(valid) < 2 {
		return []CleanupDuplicateGroup{}, map[[2]uint]struct{}{}, staleCount, nil
	}

	bands := make(map[string][]int)
	adjacency := make(map[int]map[int]struct{})
	matchedPairs := make(map[[2]uint]struct{})
	for index, row := range valid {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, err
		}
		candidates := make(map[int]struct{})
		for _, key := range perceptualBandKeys(row) {
			if len(candidates) < perceptualHashMaxCandidates {
				for _, other := range bands[key] {
					if perceptualDurationsComparable(valid[other].Video.Duration, row.Video.Duration) {
						candidates[other] = struct{}{}
					}
					if len(candidates) >= perceptualHashMaxCandidates {
						break
					}
				}
			}
			bucket := append(bands[key], index)
			if len(bucket) > perceptualHashMaxBandNeighbors {
				bucket = bucket[len(bucket)-perceptualHashMaxBandNeighbors:]
			}
			bands[key] = bucket
		}
		for other := range candidates {
			left := valid[other]
			videoPair := cleanupVideoPairKey(left.VideoID, row.VideoID)
			if _, skip := excluded[videoPair]; skip || !perceptualRowsMatch(left, row) {
				continue
			}
			matchedPairs[videoPair] = struct{}{}
			if adjacency[other] == nil {
				adjacency[other] = make(map[int]struct{})
			}
			if adjacency[index] == nil {
				adjacency[index] = make(map[int]struct{})
			}
			adjacency[other][index] = struct{}{}
			adjacency[index][other] = struct{}{}
		}
	}

	edges := make([][2]int, 0, len(matchedPairs))
	for left, neighbors := range adjacency {
		for right := range neighbors {
			if left < right {
				edges = append(edges, [2]int{left, right})
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i][0] == edges[j][0] {
			return edges[i][1] < edges[j][1]
		}
		return edges[i][0] < edges[j][0]
	})
	covered := make(map[[2]int]struct{})
	assigned := make(map[int]struct{})
	groups := make([]CleanupDuplicateGroup, 0)
	for _, edge := range edges {
		if _, exists := covered[edge]; exists {
			continue
		}
		if _, exists := assigned[edge[0]]; exists {
			continue
		}
		if _, exists := assigned[edge[1]]; exists {
			continue
		}
		members := []int{edge[0], edge[1]}
		memberSet := map[int]struct{}{edge[0]: {}, edge[1]: {}}
		common := make([]int, 0)
		for candidate := range adjacency[edge[0]] {
			if _, ok := adjacency[edge[1]][candidate]; ok {
				common = append(common, candidate)
			}
		}
		sort.Ints(common)
		for _, candidate := range common {
			if _, exists := memberSet[candidate]; exists {
				continue
			}
			if _, exists := assigned[candidate]; exists {
				continue
			}
			matchesAll := true
			for _, member := range members {
				if _, ok := adjacency[candidate][member]; !ok {
					matchesAll = false
					break
				}
			}
			if matchesAll {
				members = append(members, candidate)
				memberSet[candidate] = struct{}{}
			}
		}
		videos := make([]models.Video, 0, len(members))
		for _, member := range members {
			videos = append(videos, valid[member].Video)
			assigned[member] = struct{}{}
		}
		for left := 0; left < len(members); left++ {
			for right := left + 1; right < len(members); right++ {
				pair := [2]int{members[left], members[right]}
				if pair[0] > pair[1] {
					pair[0], pair[1] = pair[1], pair[0]
				}
				covered[pair] = struct{}{}
			}
		}
		// 整理分在全部类别成组后由 rankCleanupCandidates 统一补上并重排。
		sort.Slice(videos, func(i, j int) bool { return isPreferredCleanupVideo(videos[i], videos[j], nil) })
		groups = append(groups, CleanupDuplicateGroup{
			Original: videos[0], Candidates: append([]models.Video(nil), videos[1:]...),
			Reason: "三帧感知哈希接近，可能是同片不同转码（不会默认选中）",
		})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Original.ID < groups[j].Original.ID })
	return groups, matchedPairs, staleCount, nil
}
