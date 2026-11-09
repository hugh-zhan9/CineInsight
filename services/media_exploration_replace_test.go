package services

import (
	"context"
	"reflect"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestMediaExplorationReplaceKeepsSlotsAndReceipt(t *testing.T) {
	s, state := explorationFixture(t, 1030)
	before := append([]ExplorationSceneEntry(nil), state.Entries...)
	req := explorationRequest(state, "replace")
	req.EntryIDs = []uint{before[0].EntryID}
	next := explorationUpdate(t, s, req)
	if next.SceneID != state.SceneID || next.Entries[0].EntryID == before[0].EntryID || next.Entries[0].Slot != 0 || !reflect.DeepEqual(next.Entries[1:], before[1:]) {
		t.Fatal("replacement moved the wall")
	}
	again := explorationUpdate(t, s, req)
	if !reflect.DeepEqual(next, again) {
		t.Fatal("replayed replacement changed result")
	}
	if _, err := s.Items(context.Background(), ExplorationItemsRequest{ExplorationContext: next.ExplorationContext, EntryIDs: req.EntryIDs}); err == nil {
		t.Fatal("retired entry remains addressable")
	}
}

func TestMediaExplorationReplaceAutomaticallyStartsRoundOutsideWall(t *testing.T) {
	s, state := explorationFixture(t, 1026)
	ids := []uint{}
	for _, e := range state.Entries {
		ids = append(ids, e.EntryID)
	}
	state = explorationPresent(t, s, state, ids)
	for i := 0; i < 3; i++ {
		oldRound := state.RoundID
		excluded := map[uint]bool{}
		for _, e := range state.Entries {
			excluded[e.MediaID] = true
		}
		req := explorationRequest(state, "replace")
		req.EntryIDs = []uint{state.Entries[0].EntryID}
		next := explorationUpdate(t, s, req)
		if excluded[next.Entries[0].MediaID] {
			t.Fatal("replacement repeats an item on the wall")
		}
		if i == 2 && next.RoundID == oldRound {
			t.Fatal("exhausted pool did not start another round")
		}
		state = next
	}
}

func TestMediaExplorationReplenishDeletedAndSmallPool(t *testing.T) {
	s, state := explorationFixture(t, 1025)
	deleted := state.Entries[4]
	if err := database.DB.Delete(&models.Image{}, deleted.MediaID).Error; err != nil {
		t.Fatal(err)
	}
	next := explorationUpdate(t, s, explorationRequest(state, "replenish"))
	if next.Entries[4].MediaID == deleted.MediaID || next.Entries[4].Slot != 4 {
		t.Fatal("deleted slot not replenished")
	}
	req := explorationRequest(next, "replace")
	req.EntryIDs = []uint{next.Entries[0].EntryID}
	unchanged := explorationUpdate(t, s, req)
	if !reflect.DeepEqual(next.Entries, unchanged.Entries) || next.RoundID != unchanged.RoundID {
		t.Fatal("no alternative must keep existing wall without cycling")
	}
}
