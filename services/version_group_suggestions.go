package services

import (
	"context"
	"sort"
	"time"

	"gorm.io/gorm"

	"video-master/database"
	"video-master/models"
)

// 版本组建议只来源于 status=detected 的同源配对：双方活跃、非失效、不在任何组内，时长都已知且
// 较短者 ≥ 较长者的 90%（排除截取片段），配对没被忽略。按并查集连成候选组，每组 ≤20 个；
// 合并两个候选组时，若跨组有被否认（rejected）或被忽略的配对就不合并，免得传递连出用户否认过的组合。

type versionSuggestionEdge struct {
	VideoAID  uint
	VideoBID  uint
	UpdatedAt time.Time
}

type versionPairKey struct{ low, high uint }

func newVersionPairKey(a, b uint) versionPairKey {
	if a > b {
		a, b = b, a
	}
	return versionPairKey{low: a, high: b}
}

type versionSuggestionComponent struct {
	members []uint
	latest  time.Time
}

func loadVersionSuggestionEdges(db *gorm.DB) ([]versionSuggestionEdge, error) {
	shorter := "(CASE WHEN va.duration <= vb.duration THEN va.duration ELSE vb.duration END)"
	longer := "(CASE WHEN va.duration <= vb.duration THEN vb.duration ELSE va.duration END)"
	var edges []versionSuggestionEdge
	err := db.Table("video_same_source_relations AS r").
		Select("r.video_a_id, r.video_b_id, r.updated_at").
		Joins("JOIN videos va ON va.id = r.video_a_id").
		Joins("JOIN videos vb ON vb.id = r.video_b_id").
		Where("r.status = ?", models.VideoSameSourceStatusDetected).
		Where("va.deleted_at IS NULL AND vb.deleted_at IS NULL").
		Where("COALESCE(va.is_stale, ?) = ? AND COALESCE(vb.is_stale, ?) = ?", false, false, false, false).
		Where("va.duration > 0 AND vb.duration > 0").
		Where(shorter+" >= ? * "+longer, versionSuggestionMinRatio).
		Where("NOT EXISTS (SELECT 1 FROM video_version_members m WHERE m.video_id = r.video_a_id OR m.video_id = r.video_b_id)").
		Where(`NOT EXISTS (SELECT 1 FROM video_version_suggestion_dismissals d
			WHERE (d.video_low_id = r.video_a_id AND d.video_high_id = r.video_b_id)
			   OR (d.video_low_id = r.video_b_id AND d.video_high_id = r.video_a_id))`).
		Order("r.updated_at DESC, r.id DESC").
		Scan(&edges).Error
	return edges, err
}

// loadVersionCannotLinks 读出被否认的同源配对与被忽略的建议配对，只保留两端都在候选里的。
func loadVersionCannotLinks(db *gorm.DB, candidates map[uint]bool) (map[versionPairKey]bool, error) {
	blocked := map[versionPairKey]bool{}
	type pair struct{ A, B uint }
	var rejected []pair
	if err := db.Model(&models.VideoSameSourceRelation{}).Select("video_a_id AS a, video_b_id AS b").
		Where("status = ?", models.VideoSameSourceStatusRejected).Scan(&rejected).Error; err != nil {
		return nil, err
	}
	var dismissed []pair
	if err := db.Model(&models.VideoVersionSuggestionDismissal{}).
		Select("video_low_id AS a, video_high_id AS b").Scan(&dismissed).Error; err != nil {
		return nil, err
	}
	for _, p := range append(rejected, dismissed...) {
		if candidates[p.A] && candidates[p.B] {
			blocked[newVersionPairKey(p.A, p.B)] = true
		}
	}
	return blocked, nil
}

// groupVersionSuggestionEdges 按关系时间从新到旧逐条合并，遵守 20 个上限与不可连配对。
func groupVersionSuggestionEdges(edges []versionSuggestionEdge, blocked map[versionPairKey]bool) []*versionSuggestionComponent {
	owner := map[uint]*versionSuggestionComponent{}
	var components []*versionSuggestionComponent
	componentOf := func(id uint) *versionSuggestionComponent {
		if component, ok := owner[id]; ok {
			return component
		}
		component := &versionSuggestionComponent{members: []uint{id}}
		owner[id] = component
		components = append(components, component)
		return component
	}
	compatible := func(left, right *versionSuggestionComponent) bool {
		if len(left.members)+len(right.members) > versionGroupMaxMembers {
			return false
		}
		for _, a := range left.members {
			for _, b := range right.members {
				if blocked[newVersionPairKey(a, b)] {
					return false
				}
			}
		}
		return true
	}
	for _, edge := range edges {
		if edge.VideoAID == edge.VideoBID || blocked[newVersionPairKey(edge.VideoAID, edge.VideoBID)] {
			continue
		}
		left, right := componentOf(edge.VideoAID), componentOf(edge.VideoBID)
		if left == right || !compatible(left, right) {
			continue
		}
		// latest 只取真正连进来的关系时间，被跳过的配对不算。
		left.members = append(left.members, right.members...)
		for _, at := range []time.Time{right.latest, edge.UpdatedAt} {
			if at.After(left.latest) {
				left.latest = at
			}
		}
		for _, id := range right.members {
			owner[id] = left
		}
		right.members = nil
	}
	result := make([]*versionSuggestionComponent, 0, len(components))
	for _, component := range components {
		if len(component.members) >= versionGroupMinMembers {
			result = append(result, component)
		}
	}
	minID := func(component *versionSuggestionComponent) uint {
		low := component.members[0]
		for _, id := range component.members[1:] {
			if id < low {
				low = id
			}
		}
		return low
	}
	sort.SliceStable(result, func(i, j int) bool {
		if len(result[i].members) != len(result[j].members) {
			return len(result[i].members) > len(result[j].members)
		}
		if !result[i].latest.Equal(result[j].latest) {
			return result[i].latest.After(result[j].latest)
		}
		return minID(result[i]) < minID(result[j])
	})
	return result
}

// ListSuggestions 返回候选组（成员数多、关系新的在前）；limit 默认 50、最大 200。只读，从不建组。
func (s *VersionGroupService) ListSuggestions(ctx context.Context, limit int) ([]VersionGroupSuggestion, error) {
	if limit <= 0 {
		limit = versionSuggestionDefault
	}
	if limit > versionSuggestionMax {
		limit = versionSuggestionMax
	}
	suggestions := []VersionGroupSuggestion{}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		edges, err := loadVersionSuggestionEdges(db)
		if err != nil || len(edges) == 0 {
			return err
		}
		candidates := map[uint]bool{}
		for _, edge := range edges {
			candidates[edge.VideoAID], candidates[edge.VideoBID] = true, true
		}
		blocked, err := loadVersionCannotLinks(db, candidates)
		if err != nil {
			return err
		}
		components := groupVersionSuggestionEdges(edges, blocked)
		if len(components) > limit {
			components = components[:limit]
		}
		var ids []uint
		for _, component := range components {
			ids = append(ids, component.members...)
		}
		var videos []models.Video
		if err := db.Where("id IN ?", ids).Find(&videos).Error; err != nil {
			return err
		}
		byID := make(map[uint]models.Video, len(videos))
		for _, video := range videos {
			byID[video.ID] = video
		}
		for _, component := range components {
			if suggestion, ok := buildVersionSuggestion(component, byID); ok {
				suggestions = append(suggestions, suggestion)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return suggestions, nil
}

// buildVersionSuggestion 把成员按画质排好（像素、时长、大小从高到低，ID 升序兜底），第一位作为建议的主版本。
func buildVersionSuggestion(component *versionSuggestionComponent, byID map[uint]models.Video) (VersionGroupSuggestion, bool) {
	videos := make([]models.Video, 0, len(component.members))
	for _, id := range component.members {
		video, ok := byID[id]
		if !ok {
			return VersionGroupSuggestion{}, false
		}
		videos = append(videos, video)
	}
	sort.SliceStable(videos, func(i, j int) bool {
		left, right := videos[i], videos[j]
		if lp, rp := left.Width*left.Height, right.Width*right.Height; lp != rp {
			return lp > rp
		}
		if left.Duration != right.Duration {
			return left.Duration > right.Duration
		}
		if left.Size != right.Size {
			return left.Size > right.Size
		}
		return left.ID < right.ID
	})
	suggestion := VersionGroupSuggestion{LatestRelationAt: component.latest, VideoIDs: []uint{}, Members: []VersionGroupMember{}}
	for index, video := range videos {
		suggestion.VideoIDs = append(suggestion.VideoIDs, video.ID)
		suggestion.Members = append(suggestion.Members, versionMemberFromVideo(video, index+1))
	}
	return suggestion, true
}

func versionMemberFromVideo(video models.Video, position int) VersionGroupMember {
	return versionMemberRow{
		VideoID: video.ID, Position: position, DisplayTitle: video.DisplayTitle, Name: video.Name,
		Resolution: video.Resolution, Width: video.Width, Height: video.Height, Size: video.Size,
		Duration: video.Duration, IsWatched: video.IsWatched, WatchPositionSeconds: video.WatchPositionSeconds,
		PersonalRating: video.PersonalRating, IsStale: video.IsStale,
	}.member()
}
