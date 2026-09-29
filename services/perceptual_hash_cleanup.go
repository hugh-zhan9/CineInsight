package services

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	perceptualHashMaxBandNeighbors = 64
	perceptualHashMaxCandidates    = 256
)

// presentVideoIDs 是调用方本轮已确认在扫描范围内、文件也读得到的视频集合。
// 它只用来数出"连感知哈希行都没有"的视频——那部分早先完全不进任何计数。
// ctx 被取消时在下一行 / 下一个比较之前停下。
func loadCleanupNearDuplicateGroups(ctx context.Context, excluded map[[2]uint]struct{}, presentVideoIDs map[uint]struct{}) ([]CleanupDuplicateGroup, map[[2]uint]struct{}, int64, error) {
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

// perceptualHashRowComplete 报告三帧哈希是否都已算出（回填失败的行三项为空）。
func perceptualHashRowComplete(row models.VideoPerceptualHash) bool {
	return row.HashEarly != "" && row.HashMiddle != "" && row.HashLate != ""
}

// loadActiveNearDuplicateDismissals 只返回仍然有效的近似重复忽略（D-PC31）：两侧记录的指纹
// 为空（历史行）或与 current 一致才算数；current 里没有的视频（本轮读不到）无从核对，照旧算数。
func loadActiveNearDuplicateDismissals(current map[uint]string) (map[[2]uint]struct{}, error) {
	var dismissals []models.NearDuplicateDismissal
	if err := database.DB.Find(&dismissals).Error; err != nil {
		return nil, err
	}
	pairs := make(map[[2]uint]struct{}, len(dismissals))
	for _, dismissal := range dismissals {
		if !cleanupDismissalSideApplies(dismissal.FingerprintA, current, dismissal.VideoLowID) ||
			!cleanupDismissalSideApplies(dismissal.FingerprintB, current, dismissal.VideoHighID) {
			continue
		}
		pairs[cleanupVideoPairKey(dismissal.VideoLowID, dismissal.VideoHighID)] = struct{}{}
	}
	return pairs, nil
}

// DismissNearDuplicateGroup 把一组视频的全部两两配对持久化为忽略，后续
// 清理分析不再把它们报为近似重复。每条忽略记下双方此刻的 size:mtimeNS，
// 任一侧文件变了这条忽略随之失效（D-PC31）。
func DismissNearDuplicateGroup(videoIDs []uint) error {
	ids := uniqueUintIDs(videoIDs)
	if len(ids) < 2 {
		return fmt.Errorf("忽略近似重复组至少需要两个视频")
	}
	pairs := make([][2]uint, 0, len(ids)*(len(ids)-1)/2)
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			pairs = append(pairs, cleanupVideoPairKey(ids[i], ids[j]))
		}
	}
	return dismissNearDuplicatePairs(ids, pairs)
}

// DismissNearDuplicateMember 把一个成员移出近似重复组（D-PC31）：只否决它与组内其他成员的配对，
// 其余成员之间的关系不动，下次分析仍可成组。
func DismissNearDuplicateMember(groupVideoIDs []uint, memberID uint) error {
	ids := uniqueUintIDs(groupVideoIDs)
	if memberID == 0 || !containsUintID(ids, memberID) {
		return fmt.Errorf("要移出的视频不在这一组里")
	}
	pairs := make([][2]uint, 0, len(ids)-1)
	for _, other := range ids {
		if other != memberID {
			pairs = append(pairs, cleanupVideoPairKey(memberID, other))
		}
	}
	if len(pairs) == 0 {
		return fmt.Errorf("移出成员时组内至少还要有另一个视频")
	}
	return dismissNearDuplicatePairs(ids, pairs)
}

// dismissNearDuplicatePairs 写入（或刷新指纹）一批近似重复忽略，并否决这些对上待审的同源关系。
func dismissNearDuplicatePairs(videoIDs []uint, pairs [][2]uint) error {
	fingerprints, err := loadVideoFileFingerprints(videoIDs)
	if err != nil {
		return err
	}
	dismissals := make([]models.NearDuplicateDismissal, 0, len(pairs))
	for _, pair := range pairs {
		dismissals = append(dismissals, models.NearDuplicateDismissal{
			VideoLowID: pair[0], VideoHighID: pair[1],
			FingerprintA: fingerprints[pair[0]], FingerprintB: fingerprints[pair[1]],
		})
	}
	// "不是同片"也是对同源判断的否决：这些对上还在待审的同源关系一并判掉，
	// 否则 AI 标签管理里的"视频同源待审"会继续拿同一对来问。两步同一事务：
	// 只写了忽略表而没判关系，清理面板与同源待审就会各说各话。
	now := time.Now()
	return database.Transaction(func(tx *gorm.DB) error {
		// 同一对重复忽略要刷新指纹：上一条可能记的是重编码之前的那两个文件。
		// pairs 由调用方从去重后的 ID 生成，批内没有重复键（PG 21000）。
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "video_low_id"}, {Name: "video_high_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"fingerprint_a", "fingerprint_b"}),
		}).Create(&dismissals).Error; err != nil {
			return err
		}
		return rejectDetectedSameSourceRelationsForPairsTx(tx, pairs, now)
	})
}

// loadVideoFileFingerprints 读出每个活跃视频此刻的 size:mtimeNS。视频不存在或文件读不到时报错：
// 空指纹表示"永不失效"，只留给历史行，新写入的忽略必须带上真实指纹。
func loadVideoFileFingerprints(videoIDs []uint) (map[uint]string, error) {
	var videos []models.Video
	if err := database.DB.Select("id", "path").Where("id IN ?", videoIDs).Find(&videos).Error; err != nil {
		return nil, err
	}
	if len(videos) != len(videoIDs) {
		return nil, fmt.Errorf("部分视频不存在或已删除，无法记录忽略")
	}
	fingerprints := make(map[uint]string, len(videos))
	for _, video := range videos {
		fingerprint, err := statCleanupFileFingerprint(video.Path)
		if err != nil {
			return nil, fmt.Errorf("视频 %d 的文件当前无法访问，无法记录忽略", video.ID)
		}
		fingerprints[video.ID] = fingerprint
	}
	return fingerprints, nil
}

func containsUintID(ids []uint, target uint) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func perceptualBandKeys(row models.VideoPerceptualHash) []string {
	hashes := []string{row.HashEarly, row.HashMiddle, row.HashLate}
	keys := make([]string, 0, 24)
	for frame, hash := range hashes {
		if len(hash) != 16 {
			continue
		}
		for band := 0; band < 8; band++ {
			keys = append(keys, fmt.Sprintf("%d:%d:%s", frame, band, hash[band*2:band*2+2]))
		}
	}
	return keys
}

func perceptualDurationsComparable(left, right float64) bool {
	if left <= 0 || right <= 0 {
		return false
	}
	tolerance := math.Max(3, math.Max(left, right)*0.02)
	return math.Abs(left-right) <= tolerance
}
