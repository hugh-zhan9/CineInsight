package services

import (
	"container/heap"
	"context"
	"path/filepath"
	"sort"

	"video-master/models"
)

// rankExactCleanupGroups 跨精确重复组集中保留目录（D-PC48-EXACT-DIR）。
// 每次选能覆盖最多剩余组的目录，同目录内仍按整理成果择优；不改变组成员和检测理由。
func rankExactCleanupGroups(ctx context.Context, groups []CleanupDuplicateGroup, curation map[uint]CleanupCuration) error {
	members := make([][]models.Video, len(groups))
	choices := make([]map[string]models.Video, len(groups))
	directories := make(map[string]*cleanupDirectoryChoice)
	for i, group := range groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		members[i] = append([]models.Video{group.Original}, group.Candidates...)
		sort.SliceStable(members[i], func(a, b int) bool {
			return isPreferredCleanupVideo(members[i][a], members[i][b], curation)
		})
		choices[i] = make(map[string]models.Video)
		for _, video := range members[i] {
			// 无目录不代表位于同一目录；单条候选不增加重复组覆盖。
			if video.Directory == "" || len(members[i]) < 2 {
				continue
			}
			path := filepath.Clean(video.Directory)
			if _, exists := choices[i][path]; exists {
				continue // 同组同目录只计最佳副本一次。
			}
			choices[i][path] = video
			directory := directories[path]
			if directory == nil {
				directory = &cleanupDirectoryChoice{path: path}
				directories[path] = directory
			}
			directory.groups = append(directory.groups, i)
			directory.remaining++
			directory.curation += curation[video.ID].Score()
		}
	}
	queue := make(cleanupDirectoryHeap, 0, len(directories))
	for _, directory := range directories {
		directory.index = len(queue)
		queue = append(queue, directory)
	}
	heap.Init(&queue)
	keepers := make(map[int]uint, len(groups))
	for queue.Len() > 0 && queue[0].remaining > 0 {
		directory := heap.Pop(&queue).(*cleanupDirectoryChoice)
		for _, i := range directory.groups {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, assigned := keepers[i]; assigned {
				continue
			}
			keepers[i] = choices[i][directory.path].ID
			for path, video := range choices[i] {
				other := directories[path]
				other.remaining--
				other.curation -= curation[video.ID].Score()
				if other.index >= 0 {
					heap.Fix(&queue, other.index)
				}
			}
		}
	}
	for i := range groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		keeperIndex := 0 // 未知目录的组沿用原有排序。
		for j, video := range members[i] {
			if video.ID == keepers[i] {
				keeperIndex = j
				break
			}
		}
		groups[i].Original = members[i][keeperIndex]
		groups[i].Candidates = append(members[i][:keeperIndex], members[i][keeperIndex+1:]...)
	}
	return ctx.Err()
}

type cleanupDirectoryChoice struct {
	path      string
	groups    []int
	remaining int
	curation  int
	index     int
}

// 更新已覆盖组对应的目录即可；避免每安排一个目录都扫描全部组和目录。
type cleanupDirectoryHeap []*cleanupDirectoryChoice

func (h cleanupDirectoryHeap) Len() int { return len(h) }

func (h cleanupDirectoryHeap) Less(i, j int) bool {
	if h[i].remaining != h[j].remaining {
		return h[i].remaining > h[j].remaining
	}
	if h[i].curation != h[j].curation {
		return h[i].curation > h[j].curation
	}
	return h[i].path < h[j].path
}

func (h cleanupDirectoryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}

func (h *cleanupDirectoryHeap) Push(value any) {
	directory := value.(*cleanupDirectoryChoice)
	directory.index = len(*h)
	*h = append(*h, directory)
}

func (h *cleanupDirectoryHeap) Pop() any {
	last := len(*h) - 1
	directory := (*h)[last]
	(*h)[last] = nil
	*h = (*h)[:last]
	directory.index = -1
	return directory
}
