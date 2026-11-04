package services

import (
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"math/rand"
	"slices"
	"sort"
	"time"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func explorationVideoQuery(db *gorm.DB) (*gorm.DB, error) {
	return applyLibraryFilter(db.Model(&models.Video{}).Where("videos.is_stale = ?", false), LibraryFilter{}, time.Now())
}

// Candidate collection reads IDs only. It neither samples a capped recent pool
// nor consults viewing, ratings, AI, recommendations or playback weights.
func explorationCandidates(db *gorm.DB, scope string) ([]models.MediaExplorationEntry, error) {
	rows := []models.MediaExplorationEntry{}
	if scope != "image" {
		query, err := explorationVideoQuery(db)
		if err != nil {
			return nil, err
		}
		var ids []uint
		if err := query.Order("videos.id").Pluck("videos.id", &ids).Error; err != nil {
			return nil, err
		}
		var members []models.VideoVersionMember
		if err := db.Order("group_id, video_id").Find(&members).Error; err != nil {
			return nil, err
		}
		groupOf, allMembers := map[uint]uint{}, map[uint][]uint{}
		for _, m := range members {
			groupOf[m.VideoID] = m.GroupID
			allMembers[m.GroupID] = append(allMembers[m.GroupID], m.VideoID)
		}
		byKey := map[string][]uint{}
		for _, id := range ids {
			key := fmt.Sprintf("video:%d", id)
			if group := groupOf[id]; group != 0 {
				key = fmt.Sprintf("version-group:%d", group)
			}
			byKey[key] = append(byKey[key], id)
		}
		keys := make([]string, 0, len(byKey))
		for key := range byKey {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			eligible := byKey[key]
			id := eligible[rand.Intn(len(eligible))]
			memberIDs := []uint{id}
			if group := groupOf[id]; group != 0 {
				memberIDs = allMembers[group]
			}
			encoded, _ := json.Marshal(memberIDs)
			rows = append(rows, models.MediaExplorationEntry{UnitKey: key, MediaKind: "video", MediaID: id, MemberIDsJSON: string(encoded), SamplePointsJSON: "[]", Slot: -1, Available: true})
		}
	}
	if scope != "video" {
		var ids []uint
		q := applyImageFilterFrom(db.Model(&models.Image{}), ImageFilter{}, db)
		if err := q.Order("images.id").Pluck("images.id", &ids).Error; err != nil {
			return nil, err
		}
		for _, id := range ids {
			rows = append(rows, models.MediaExplorationEntry{UnitKey: fmt.Sprintf("image:%d", id), MediaKind: "image", MediaID: id, MemberIDsJSON: fmt.Sprintf("[%d]", id), SamplePointsJSON: "[]", Slot: -1, Available: true})
		}
	}
	return rows, nil
}

func explorationMembers(row models.MediaExplorationEntry) ([]uint, error) {
	var ids []uint
	err := json.Unmarshal([]byte(row.MemberIDsJSON), &ids)
	return ids, err
}

func explorationSyncCandidates(db *gorm.DB, state *models.MediaExplorationState, fresh bool, avoid map[string]bool) error {
	candidates, err := explorationCandidates(db, state.Scope)
	if err != nil {
		return err
	}
	if fresh {
		shuffleExploration(candidates)
		if len(avoid) > 0 {
			sort.SliceStable(candidates, func(i, j int) bool { return !avoid[candidates[i].UnitKey] && avoid[candidates[j].UnitKey] })
		}
		for i := range candidates {
			candidates[i].StateID, candidates[i].OrderPosition = state.ID, int64(i+1)
		}
		if len(candidates) == 0 {
			return nil
		}
		return db.Omit("State").CreateInBatches(&candidates, 100).Error
	}
	var previous []models.MediaExplorationEntry
	if err := db.Where("state_id = ?", state.ID).Order("order_position, id").Find(&previous).Error; err != nil {
		return err
	}
	old := map[string]models.MediaExplorationEntry{}
	seen := map[string]*time.Time{}
	var maxOrder int64
	for _, row := range previous {
		old[row.UnitKey] = row
		if row.OrderPosition > maxOrder {
			maxOrder = row.OrderPosition
		}
		if row.PresentedAt == nil {
			continue
		}
		ids, err := explorationMembers(row)
		if err != nil {
			return err
		}
		for _, id := range ids {
			key := fmt.Sprintf("%s:%d", row.MediaKind, id)
			if seen[key] == nil || seen[key].After(*row.PresentedAt) {
				seen[key] = row.PresentedAt
			}
		}
	}
	eligibleVideos := map[uint]bool{}
	if state.Scope != "image" {
		query, err := explorationVideoQuery(db)
		if err != nil {
			return err
		}
		var ids []uint
		if err := query.Pluck("videos.id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			eligibleVideos[id] = true
		}
	}
	active := map[string]bool{}
	updates, additions := []models.MediaExplorationEntry{}, []models.MediaExplorationEntry{}
	for _, candidate := range candidates {
		active[candidate.UnitKey] = true
		row, exists := old[candidate.UnitKey]
		if !exists {
			row = candidate
			row.StateID = state.ID
			maxOrder++
			row.OrderPosition = maxOrder
		}
		ids, err := explorationMembers(candidate)
		if err != nil {
			return err
		}
		at := row.PresentedAt
		for _, id := range ids {
			found := seen[fmt.Sprintf("%s:%d", candidate.MediaKind, id)]
			if found != nil && (at == nil || at.After(*found)) {
				at = found
			}
		}
		changed := !row.Available || row.MemberIDsJSON != candidate.MemberIDsJSON || (row.PresentedAt == nil && at != nil)
		// Preserve the current representative when it is still eligible.
		if exists && row.MediaKind == "video" {
			if !eligibleVideos[row.MediaID] || !slices.Contains(ids, row.MediaID) {
				row.MediaID, row.SamplePointsJSON, changed = candidate.MediaID, "[]", true
			}
		}
		// Keep the immutable presented-member fact when a group changes. The
		// live unit may adopt a new membership, but temporarily hidden former
		// members must not lose this round's history before becoming visible.
		if exists && row.PresentedAt != nil && row.MemberIDsJSON != candidate.MemberIDsJSON {
			history := old[candidate.UnitKey]
			history.ID, history.UnitKey = 0, "history:"+uuid.NewString()
			history.Available, history.SceneID, history.Slot, history.SamplePointsJSON = false, "", -1, "[]"
			maxOrder++
			history.OrderPosition = maxOrder
			additions = append(additions, history)
		}
		row.Available, row.MemberIDsJSON, row.PresentedAt = true, candidate.MemberIDsJSON, at
		if !exists {
			additions = append(additions, row)
		} else if changed {
			updates = append(updates, row)
		}
	}
	for _, row := range previous {
		if row.Available && !active[row.UnitKey] {
			row.Available = false
			updates = append(updates, row)
		}
	}
	if len(updates) > 0 {
		if err := db.Omit("State").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"available", "member_ids_json", "presented_at", "media_id", "sample_points_json"})}).CreateInBatches(&updates, 100).Error; err != nil {
			return err
		}
	}
	if len(additions) == 0 {
		return nil
	}
	shuffleExploration(additions)
	if err := db.Omit("State").CreateInBatches(&additions, 100).Error; err != nil {
		return err
	}
	// Randomly interleave new units while preserving the remaining deck's order,
	// including released, never-presented items at its tail.
	var pending []models.MediaExplorationEntry
	if err := db.Where("state_id = ? AND available = ? AND presented_at IS NULL AND order_position <= ?", state.ID, true, maxOrder-int64(len(additions))).Order("order_position, id").Find(&pending).Error; err != nil {
		return err
	}
	buckets := make(map[int][]models.MediaExplorationEntry)
	for _, row := range additions {
		if row.PresentedAt == nil {
			i := rand.Intn(len(pending) + 1)
			buckets[i] = append(buckets[i], row)
		}
	}
	merged := make([]models.MediaExplorationEntry, 0, len(pending)+len(additions))
	for i := 0; i <= len(pending); i++ {
		merged = append(merged, buckets[i]...)
		if i < len(pending) {
			merged = append(merged, pending[i])
		}
	}
	for i := range merged {
		merged[i].OrderPosition = int64(i + 1)
	}
	if len(merged) == 0 {
		return nil
	}
	return db.Omit("State").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"order_position"})}).CreateInBatches(&merged, 100).Error
}

func explorationRefreshAvailability(db *gorm.DB, state models.MediaExplorationState) error {
	candidates, err := explorationCandidates(db, state.Scope)
	if err != nil {
		return err
	}
	current := map[string]models.MediaExplorationEntry{}
	for _, row := range candidates {
		current[row.UnitKey] = row
	}
	var rows []models.MediaExplorationEntry
	if err := db.Where("state_id = ?", state.ID).Find(&rows).Error; err != nil {
		return err
	}
	changed := []models.MediaExplorationEntry{}
	for _, row := range rows {
		now, exists := current[row.UnitKey]
		available := exists && now.MemberIDsJSON == row.MemberIDsJSON
		if available != row.Available {
			row.Available = available
			changed = append(changed, row)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	return db.Omit("State").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"available"})}).CreateInBatches(&changed, 100).Error
}
