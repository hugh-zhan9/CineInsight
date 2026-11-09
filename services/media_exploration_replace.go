package services

import (
	"encoding/json"
	"time"
	"video-master/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Refill only retires requested/unavailable slots. The root CAS and receipt in
// Update own the entire transition, including a possible automatic round reset.
func explorationRefill(db *gorm.DB, state *models.MediaExplorationState, requested []uint, reconcile bool) error {
	var current []models.MediaExplorationEntry
	if err := db.Where("state_id = ? AND scene_id = ?", state.ID, state.SceneID).Order("slot, id").Find(&current).Error; err != nil {
		return err
	}
	wanted := map[uint]bool{}
	for _, id := range requested {
		wanted[id] = true
	}
	for _, row := range current {
		delete(wanted, row.ID)
	}
	if len(wanted) > 0 {
		return ErrExplorationInvalid
	}
	if reconcile {
		if err := explorationSyncCandidates(db, state, false, nil); err != nil {
			return err
		}
		if err := db.Where("state_id = ? AND scene_id = ?", state.ID, state.SceneID).Order("slot, id").Find(&current).Error; err != nil {
			return err
		}
	}
	wanted = map[uint]bool{}
	for _, id := range requested {
		wanted[id] = true
	}
	targets := []models.MediaExplorationEntry{}
	for _, row := range current {
		if wanted[row.ID] || !row.Available {
			targets = append(targets, row)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	// Prefer unseen candidates. Old scene entries are excluded even when every
	// one of them is scheduled for replacement, so a batch cannot swap itself.
	candidates := []models.MediaExplorationEntry{}
	for len(candidates) < len(targets) {
		var batch []models.MediaExplorationEntry
		q := db.Where("state_id = ? AND scene_id = ? AND available = ?", state.ID, "", true)
		if len(candidates) > 0 {
			ids := []uint{}
			for _, row := range candidates {
				ids = append(ids, row.ID)
			}
			q = q.Where("id NOT IN ?", ids)
		}
		if err := q.Order("CASE WHEN presented_at IS NULL THEN 0 ELSE 1 END, order_position, id").Limit(len(targets) - len(candidates)).Find(&batch).Error; err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		videos, images, members, err := explorationMediaProjection(db, batch)
		if err != nil {
			return err
		}
		for _, row := range batch {
			valid := false
			if row.MediaKind == "video" {
				_, valid = videos[row.MediaID]
				valid = valid && explorationMembershipMatches(row, members)
			} else {
				_, valid = images[row.MediaID]
			}
			if valid {
				candidates = append(candidates, row)
			} else if err := db.Model(&models.MediaExplorationEntry{}).Where("id = ?", row.ID).Update("available", false).Error; err != nil {
				return err
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	reset := false
	for _, row := range candidates {
		if row.PresentedAt != nil {
			reset = true
		}
	}
	if reset {
		state.RoundID = uuid.NewString()
		if err := db.Model(&models.MediaExplorationEntry{}).Where("state_id = ? AND scene_id = ?", state.ID, "").Update("presented_at", nil).Error; err != nil {
			return err
		}
		if err := db.Where("state_id = ? AND unit_key LIKE ?", state.ID, "history:%").Delete(&models.MediaExplorationEntry{}).Error; err != nil {
			return err
		}
	}
	var maxOrder int64
	if err := db.Model(&models.MediaExplorationEntry{}).Where("state_id = ?", state.ID).Select("COALESCE(MAX(order_position), 0)").Scan(&maxOrder).Error; err != nil {
		return err
	}
	replacements := map[uint]uint{}
	now := time.Now()
	for i, next := range candidates {
		old := targets[i]
		maxOrder++
		if err := db.Model(&models.MediaExplorationEntry{}).Where("id = ?", old.ID).Updates(map[string]any{"scene_id": "", "slot": -1, "presented_at": now, "order_position": maxOrder}).Error; err != nil {
			return err
		}
		if err := db.Model(&models.MediaExplorationEntry{}).Where("id = ?", next.ID).Updates(map[string]any{"scene_id": state.SceneID, "slot": old.Slot, "presented_at": nil, "sample_points_json": "[]"}).Error; err != nil {
			return err
		}
		replacements[old.ID] = next.ID
	}
	var view ExplorationView
	if err := json.Unmarshal([]byte(state.ViewStateJSON), &view); err != nil {
		return err
	}
	if id, ok := replacements[view.SelectedID]; ok {
		view.SelectedID = id
	}
	if id, ok := replacements[view.GalleryID]; ok {
		view.GalleryID = id
	}
	for i, id := range view.GalleryIDs {
		if next, ok := replacements[id]; ok {
			view.GalleryIDs[i] = next
		}
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		return err
	}
	state.ViewStateJSON = string(encoded)
	return nil
}
