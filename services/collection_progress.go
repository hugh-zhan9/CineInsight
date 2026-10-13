package services

import (
	"fmt"
	"sort"
	"time"

	"video-master/database"
)

// 作品集进度「追到哪了」（UI 二期 D-P2-02，详细设计 §2.1）：以作品集为单位，不加剧集模型。
//
//   - 一部：作品集成员按 position 排；同一版本组的成员合并算一部（位置取组内最小 position），代表视频取
//     组内 (版本顺序, 视频 ID) 最靠前的成员，与 CollapseVersions 同一规则；
//   - 看过：任一成员 is_watched；在看：没看过且任一成员有续播位置（watch_position_seconds > 0）；
//   - 活动时间：成员 last_played_at / watch_progress_updated_at / watched_at 的最大值；
//   - 下一部：最近一次看过或在看的那一部，没看完就是它（续播）；看完了就取它之后第一部没看完的；
//     后面都看完了取最前面一部没看完的；全部看完没有下一部（作品集算看完）。
//
// 只算活跃且未失效的视频；手机端与 Jellyfin 不用它。

const (
	collectionProgressMaxIDs       = 200
	collectionProgressDefaultLimit = 12
	collectionProgressMaxLimit     = 50
)

// CollectionProgressRequest 是 ListCollectionProgress 的参数。
type CollectionProgressRequest struct {
	// CollectionIDs 非空时只算这些（作品集页、封面墙），最多 200；不存在的 ID 跳过，结果按传入顺序。
	CollectionIDs []uint `json:"collection_ids"`
	// ContinueOnly 为 true 时返回「追到哪了」候选，忽略 CollectionIDs。
	ContinueOnly bool `json:"continue_only"`
	// Limit 只在 ContinueOnly 时生效：默认 12，最大 50。
	Limit int `json:"limit"`
}

// CollectionProgress 是一个作品集的进度汇总。
type CollectionProgress struct {
	CollectionID    uint                    `json:"collection_id"`
	Name            string                  `json:"name"`
	HasCover        bool                    `json:"has_cover"`
	TotalWorks      int                     `json:"total_works"`
	WatchedWorks    int                     `json:"watched_works"`
	InProgressWorks int                     `json:"in_progress_works"`
	Complete        bool                    `json:"complete"`
	Next            *CollectionProgressNext `json:"next,omitempty"`
	LastActivityAt  *time.Time              `json:"last_activity_at" ts_type:"string"`
}

// CollectionProgressNext 是该作品集的下一部。
type CollectionProgressNext struct {
	// VideoID 是这一部要播的视频：在看时取最近有进度的那个成员（续播位置按文件各自保存），否则取代表视频。
	VideoID              uint    `json:"video_id"`
	Title                string  `json:"title"`
	WorkIndex            int     `json:"work_index"` // 第几部，从 1 起
	Resume               bool    `json:"resume"`     // 这一部在看（有续播位置）
	WatchPositionSeconds float64 `json:"watch_position_seconds"`
	Duration             float64 `json:"duration"`
}

type collectionProgressRow struct {
	CollectionID           uint
	Position               int
	VideoID                uint
	DisplayTitle           string
	Name                   string
	IsWatched              bool
	WatchPositionSeconds   float64
	Duration               float64
	LastPlayedAt           *time.Time
	WatchProgressUpdatedAt *time.Time
	WatchedAt              *time.Time
	GroupID                *uint
	GroupPosition          *int
}

type collectionProgressWork struct {
	index      int
	members    []collectionProgressRow // 组内按 (版本顺序, 视频 ID) 排好，第一个是代表
	watched    bool
	inProgress bool
	activity   *time.Time
}

// ListCollectionProgress 返回作品集进度（D-P2-02）。数据库不可用时返回错误。
func (s *CollectionService) ListCollectionProgress(request CollectionProgressRequest) ([]CollectionProgress, error) {
	var ids []uint
	if request.ContinueOnly {
		candidates, err := continueCollectionCandidates()
		if err != nil {
			return nil, err
		}
		ids = candidates
	} else {
		ids = uniqueUintIDs(request.CollectionIDs)
		if len(ids) > collectionProgressMaxIDs {
			return nil, fmt.Errorf("一次最多查询 %d 个作品集的进度", collectionProgressMaxIDs)
		}
	}
	result := make([]CollectionProgress, 0, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	var collections []struct {
		ID        uint
		Name      string
		CoverPath string
	}
	if err := database.DB.Table("media_collections").Select("id", "name", "cover_path").
		Where("id IN ? AND deleted_at IS NULL", ids).Scan(&collections).Error; err != nil {
		return nil, fmt.Errorf("读取作品集失败: %w", err)
	}
	rowsByCollection, err := loadCollectionProgressRows(ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[uint]CollectionProgress, len(collections))
	for _, collection := range collections {
		progress := summarizeCollectionProgress(rowsByCollection[collection.ID])
		progress.CollectionID = collection.ID
		progress.Name = collection.Name
		progress.HasCover = collection.CoverPath != ""
		byID[collection.ID] = progress
	}

	if !request.ContinueOnly {
		for _, id := range ids {
			if progress, ok := byID[id]; ok {
				result = append(result, progress)
			}
		}
		return result, nil
	}
	for _, progress := range byID {
		if progress.TotalWorks >= 2 && progress.WatchedWorks+progress.InProgressWorks >= 1 && !progress.Complete {
			result = append(result, progress)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i].LastActivityAt, result[j].LastActivityAt
		if (a == nil) != (b == nil) {
			return a != nil
		}
		if a != nil && !a.Equal(*b) {
			return a.After(*b)
		}
		return result[i].CollectionID > result[j].CollectionID
	})
	limit := request.Limit
	if limit <= 0 {
		limit = collectionProgressDefaultLimit
	}
	if limit > collectionProgressMaxLimit {
		limit = collectionProgressMaxLimit
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// continueCollectionCandidates 是「追到哪了」的候选：至少一个活跃未失效的成员有活动时间的作品集。
func continueCollectionCandidates() ([]uint, error) {
	var ids []uint
	err := database.DB.Table("collection_videos AS cv").
		Joins("JOIN videos v ON v.id = cv.video_id AND v.deleted_at IS NULL AND v.is_stale = ?", false).
		Joins("JOIN media_collections c ON c.id = cv.collection_id AND c.deleted_at IS NULL").
		Where("(v.last_played_at IS NOT NULL OR v.watch_progress_updated_at IS NOT NULL OR v.watched_at IS NOT NULL)").
		Distinct("cv.collection_id").Order("cv.collection_id ASC").Pluck("cv.collection_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("读取在追的作品集失败: %w", err)
	}
	return ids, nil
}

// loadCollectionProgressRows 一次查询取出这些作品集的全部活跃成员，按 (作品集, position, 视频 ID) 排序。
func loadCollectionProgressRows(collectionIDs []uint) (map[uint][]collectionProgressRow, error) {
	var rows []collectionProgressRow
	err := database.DB.Table("collection_videos AS cv").
		Select(`cv.collection_id AS collection_id, cv.position AS position, v.id AS video_id,
			v.display_title AS display_title, v.name AS name, v.is_watched AS is_watched,
			v.watch_position_seconds AS watch_position_seconds, v.duration AS duration,
			v.last_played_at AS last_played_at, v.watch_progress_updated_at AS watch_progress_updated_at,
			v.watched_at AS watched_at, vm.group_id AS group_id, vm.position AS group_position`).
		Joins("JOIN videos v ON v.id = cv.video_id AND v.deleted_at IS NULL AND v.is_stale = ?", false).
		Joins("LEFT JOIN video_version_members vm ON vm.video_id = v.id").
		Where("cv.collection_id IN ?", collectionIDs).
		Order("cv.collection_id ASC").Order("cv.position ASC").Order("cv.video_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("读取作品集成员失败: %w", err)
	}
	grouped := make(map[uint][]collectionProgressRow)
	for _, row := range rows {
		grouped[row.CollectionID] = append(grouped[row.CollectionID], row)
	}
	return grouped, nil
}

// collectionProgressWorks 把成员合并成「部」：rows 已按 (position, 视频 ID) 排好，一个版本组第一次出现的
// 位置就是它的位置（组内最小 position）。
func collectionProgressWorks(rows []collectionProgressRow) []*collectionProgressWork {
	works := make([]*collectionProgressWork, 0, len(rows))
	byGroup := map[uint]*collectionProgressWork{}
	for _, row := range rows {
		if row.GroupID != nil {
			if work, ok := byGroup[*row.GroupID]; ok {
				work.members = append(work.members, row)
				continue
			}
		}
		work := &collectionProgressWork{index: len(works) + 1, members: []collectionProgressRow{row}}
		works = append(works, work)
		if row.GroupID != nil {
			byGroup[*row.GroupID] = work
		}
	}
	for _, work := range works {
		sort.SliceStable(work.members, func(i, j int) bool {
			a, b := work.members[i], work.members[j]
			if groupPositionOf(a) != groupPositionOf(b) {
				return groupPositionOf(a) < groupPositionOf(b)
			}
			return a.VideoID < b.VideoID
		})
		for _, member := range work.members {
			if member.IsWatched {
				work.watched = true
			}
			for _, at := range []*time.Time{member.LastPlayedAt, member.WatchProgressUpdatedAt, member.WatchedAt} {
				if at != nil && (work.activity == nil || at.After(*work.activity)) {
					value := *at
					work.activity = &value
				}
			}
		}
		if !work.watched {
			for _, member := range work.members {
				if member.WatchPositionSeconds > 0 {
					work.inProgress = true
					break
				}
			}
		}
	}
	return works
}

func groupPositionOf(row collectionProgressRow) int {
	if row.GroupPosition == nil {
		return 0
	}
	return *row.GroupPosition
}

func summarizeCollectionProgress(rows []collectionProgressRow) CollectionProgress {
	works := collectionProgressWorks(rows)
	progress := CollectionProgress{TotalWorks: len(works)}
	var anchor *collectionProgressWork
	for _, work := range works {
		if work.watched {
			progress.WatchedWorks++
		}
		if work.inProgress {
			progress.InProgressWorks++
		}
		if work.activity != nil && (progress.LastActivityAt == nil || work.activity.After(*progress.LastActivityAt)) {
			value := *work.activity
			progress.LastActivityAt = &value
		}
		if work.watched || work.inProgress {
			// 最近一次看过或在看的那一部；没有活动时间的排在有的之后，同时间取靠后的一部。
			if anchor == nil || laterActivity(work.activity, anchor.activity) || equalActivity(work.activity, anchor.activity) {
				anchor = work
			}
		}
	}
	progress.Complete = len(works) > 0 && progress.WatchedWorks == len(works)
	if progress.Complete || len(works) == 0 {
		return progress
	}
	next := nextCollectionWork(works, anchor)
	if next != nil {
		progress.Next = collectionProgressNextOf(next)
	}
	return progress
}

func laterActivity(a, b *time.Time) bool {
	if a == nil {
		return false
	}
	return b == nil || a.After(*b)
}

func equalActivity(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// nextCollectionWork 按 D-P2-02 的顺序选下一部；调用方已排除「全部看完」。
func nextCollectionWork(works []*collectionProgressWork, anchor *collectionProgressWork) *collectionProgressWork {
	if anchor != nil && !anchor.watched {
		return anchor
	}
	start := 0
	if anchor != nil {
		start = anchor.index // anchor.index 从 1 起，正好是它之后那一部的下标
	}
	for _, work := range works[start:] {
		if !work.watched {
			return work
		}
	}
	for _, work := range works {
		if !work.watched {
			return work
		}
	}
	return nil
}

func collectionProgressNextOf(work *collectionProgressWork) *CollectionProgressNext {
	chosen := work.members[0]
	if work.inProgress {
		// 续播位置按文件各自保存：播的必须是有进度的那个成员，取进度最近更新的一个。
		found := false
		for _, member := range work.members {
			if member.WatchPositionSeconds <= 0 {
				continue
			}
			if !found || laterActivity(member.WatchProgressUpdatedAt, chosen.WatchProgressUpdatedAt) {
				chosen = member
				found = true
			}
		}
	}
	return &CollectionProgressNext{
		VideoID:              chosen.VideoID,
		Title:                recapVideoTitle(chosen.DisplayTitle, chosen.Name),
		WorkIndex:            work.index,
		Resume:               work.inProgress,
		WatchPositionSeconds: chosen.WatchPositionSeconds,
		Duration:             chosen.Duration,
	}
}
