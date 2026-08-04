package services

import (
	"fmt"
	"math"
	"os"
	"sort"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm/clause"
)

const (
	perceptualHashMaxBandNeighbors = 64
	perceptualHashMaxCandidates    = 256
)

func loadCleanupNearDuplicateGroups(excluded map[[2]uint]struct{}) ([]CleanupDuplicateGroup, map[[2]uint]struct{}, int64, error) {
	var rows []models.VideoPerceptualHash
	if err := database.DB.Preload("Video.Tags").Order("video_id ASC").Find(&rows).Error; err != nil {
		return nil, nil, 0, err
	}
	var staleCount int64
	valid := rows[:0]
	for _, row := range rows {
		if row.HashEarly == "" || row.HashMiddle == "" || row.HashLate == "" {
			continue
		}
		info, err := os.Stat(row.Video.Path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.Size() != row.SourceSize || info.ModTime().UnixNano() != row.SourceModTimeNS {
			staleCount++
			continue
		}
		valid = append(valid, row)
	}
	if len(valid) < 2 {
		return []CleanupDuplicateGroup{}, map[[2]uint]struct{}{}, staleCount, nil
	}

	bands := make(map[string][]int)
	adjacency := make(map[int]map[int]struct{})
	matchedPairs := make(map[[2]uint]struct{})
	for index, row := range valid {
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
		sort.Slice(videos, func(i, j int) bool { return isPreferredCleanupVideo(videos[i], videos[j]) })
		groups = append(groups, CleanupDuplicateGroup{
			Original: videos[0], Candidates: append([]models.Video(nil), videos[1:]...),
			Reason: "三帧感知哈希接近，可能是同片不同转码（不会默认选中）",
		})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Original.ID < groups[j].Original.ID })
	return groups, matchedPairs, staleCount, nil
}

// loadNearDuplicateDismissals 返回用户已忽略的近似重复视频对。
func loadNearDuplicateDismissals() (map[[2]uint]struct{}, error) {
	var dismissals []models.NearDuplicateDismissal
	if err := database.DB.Find(&dismissals).Error; err != nil {
		return nil, err
	}
	pairs := make(map[[2]uint]struct{}, len(dismissals))
	for _, dismissal := range dismissals {
		pairs[cleanupVideoPairKey(dismissal.VideoLowID, dismissal.VideoHighID)] = struct{}{}
	}
	return pairs, nil
}

// DismissNearDuplicateGroup 把一组视频的全部两两配对持久化为忽略，后续
// 清理分析不再把它们报为近似重复。
func DismissNearDuplicateGroup(videoIDs []uint) error {
	if len(videoIDs) < 2 {
		return fmt.Errorf("忽略近似重复组至少需要两个视频")
	}
	dismissals := make([]models.NearDuplicateDismissal, 0, len(videoIDs)*(len(videoIDs)-1)/2)
	for i := 0; i < len(videoIDs); i++ {
		for j := i + 1; j < len(videoIDs); j++ {
			pair := cleanupVideoPairKey(videoIDs[i], videoIDs[j])
			dismissals = append(dismissals, models.NearDuplicateDismissal{VideoLowID: pair[0], VideoHighID: pair[1]})
		}
	}
	return database.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&dismissals).Error
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
