package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"video-master/database"
	"video-master/models"

	"github.com/google/uuid"
)

func explorationFixture(t *testing.T, count int) (*MediaExplorationService, *ExplorationSnapshot) {
	t.Helper()
	setupImageServiceTestDB(t)
	rows := make([]models.Image, count)
	for i := range rows {
		rows[i] = models.Image{Name: fmt.Sprintf("image-%d", i), Path: fmt.Sprintf("/explore/%d.jpg", i), Directory: "/explore"}
	}
	if len(rows) > 0 {
		if err := database.DB.CreateInBatches(&rows, 100).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := NewMediaExplorationService()
	state, err := s.Open(context.Background(), "all")
	if err != nil {
		t.Fatal(err)
	}
	return s, state
}

func explorationRequest(state *ExplorationSnapshot, action string) ExplorationRequest {
	return ExplorationRequest{ExplorationContext: state.ExplorationContext, RoundID: state.RoundID, ExpectedRevision: state.Revision, RequestID: fmt.Sprintf("%d:%s", state.Revision, uuid.NewString()), Action: action}
}
func explorationUpdate(t *testing.T, s *MediaExplorationService, r ExplorationRequest) *ExplorationSnapshot {
	t.Helper()
	state, err := s.Update(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func explorationPresent(t *testing.T, s *MediaExplorationService, state *ExplorationSnapshot, ids []uint) *ExplorationSnapshot {
	t.Helper()
	for len(ids) > 0 {
		n := len(ids)
		if n > 256 {
			n = 256
		}
		r := explorationRequest(state, "present")
		r.EntryIDs = ids[:n]
		state = explorationUpdate(t, s, r)
		ids = ids[n:]
	}
	return state
}

func TestMediaExplorationFullPoolWithoutReplacementAndResume(t *testing.T) {
	s, state := explorationFixture(t, 2000)
	if state.AvailableCount != 2000 || len(state.Entries) != 1024 {
		t.Fatalf("initial counts %+v", state)
	}
	seen := map[uint]bool{}
	for batch := 0; batch < 2; batch++ {
		ids := []uint{}
		for _, item := range state.Entries {
			if seen[item.MediaID] {
				t.Fatalf("repeated media %d", item.MediaID)
			}
			seen[item.MediaID] = true
			ids = append(ids, item.EntryID)
		}
		state = explorationPresent(t, s, state, ids)
		if batch == 0 {
			state = explorationUpdate(t, s, explorationRequest(state, "next"))
			if len(state.Entries) != 976 {
				t.Fatalf("tail %d", len(state.Entries))
			}
		}
	}
	if len(seen) != 2000 || state.RemainingCount != 0 || state.Status != "round_complete" {
		t.Fatalf("pool not exhausted seen=%d remaining=%d status=%s", len(seen), state.RemainingCount, state.Status)
	}
	before := state.SceneID
	state = explorationUpdate(t, s, explorationRequest(state, "next"))
	if state.SceneID != before {
		t.Fatal("exhaustion silently reshuffled")
	}
	reopened, err := NewMediaExplorationService().Open(context.Background(), "all")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.SceneID != state.SceneID || reopened.PresentedCount != 2000 || reopened.DatabaseEpoch == state.DatabaseEpoch {
		t.Fatal("resume or process epoch lost")
	}
	if _, err := s.Update(context.Background(), explorationRequest(reopened, "next")); !errors.Is(err, ErrExplorationDatabase) {
		t.Fatalf("wrong epoch %v", err)
	}
}

func TestMediaExplorationPartialPresentationAndReceipts(t *testing.T) {
	s, state := explorationFixture(t, 1100)
	oldScene := state.SceneID
	shown := []uint{}
	shownMedia := map[uint]bool{}
	for _, e := range state.Entries[:100] {
		shown = append(shown, e.EntryID)
		shownMedia[e.MediaID] = true
	}
	state = explorationPresent(t, s, state, shown)
	r := explorationRequest(state, "next")
	state = explorationUpdate(t, s, r)
	if state.SceneID == oldScene || len(state.Entries) != 1000 || state.PresentedCount != 100 {
		t.Fatal("unseen reservations consumed or tail wrong")
	}
	for _, e := range state.Entries {
		if shownMedia[e.MediaID] {
			t.Fatal("shown item reissued")
		}
	}
	duplicate := explorationUpdate(t, s, r)
	if duplicate.Revision != state.Revision || duplicate.SceneID != state.SceneID {
		t.Fatal("duplicate advanced scene")
	}
	r.Action = "refresh"
	if _, err := s.Update(context.Background(), r); !errors.Is(err, ErrExplorationInvalid) {
		t.Fatalf("rebound request %v", err)
	}
	r.ExpectedRevision = state.Revision
	if _, err := s.Update(context.Background(), r); !errors.Is(err, ErrExplorationInvalid) {
		t.Fatalf("identity rebound to new revision %v", err)
	}
	if _, err := s.Items(context.Background(), ExplorationItemsRequest{ExplorationContext: ExplorationContext{DatabaseEpoch: state.DatabaseEpoch, Scope: "all", SceneID: oldScene}, EntryIDs: shown[:1]}); !errors.Is(err, ErrExplorationScene) {
		t.Fatalf("old scene %v", err)
	}
}

func TestMediaExplorationEmptySingletonAndIndependentScopes(t *testing.T) {
	for _, n := range []int{0, 1, 23, 24, 1024, 1025} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, state := explorationFixture(t, n)
			if int(state.AvailableCount) != n || len(state.Entries) > 1024 {
				t.Fatal("boundary")
			}
			if n == 0 && state.Status != "empty" {
				t.Fatal("empty not distinguished")
			}
			other, err := s.Open(context.Background(), "video")
			if err != nil || other.Status != "empty" {
				t.Fatal("scope leakage", err)
			}
			again, err := s.Open(context.Background(), "all")
			if err != nil || again.SceneID != state.SceneID {
				t.Fatal("scope reset", err)
			}
		})
	}
}

func TestMediaExplorationRollbackConcurrencyAndMaintenance(t *testing.T) {
	s, state := explorationFixture(t, 50)
	r := explorationRequest(state, "present")
	r.EntryIDs = []uint{state.Entries[0].EntryID, 999999}
	if _, err := s.Update(context.Background(), r); !errors.Is(err, ErrExplorationInvalid) {
		t.Fatal(err)
	}
	current, err := s.Open(context.Background(), "all")
	if err != nil || current.Revision != state.Revision || current.PresentedCount != 0 {
		t.Fatal("failed batch changed state")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Update(context.Background(), explorationRequest(state, "next"))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("CAS winners %d", success)
	}
	release := database.BeginMaintenance()
	defer release()
	if _, err := s.Open(context.Background(), "all"); !errors.Is(err, database.ErrMaintenance) {
		t.Fatal("maintenance not fenced", err)
	}
}

func TestMediaExplorationVisibilityDeletionAndNewImports(t *testing.T) {
	s, state := explorationFixture(t, 3)
	first := state.Entries[0]
	state = explorationPresent(t, s, state, []uint{first.EntryID})
	if err := database.DB.Delete(&models.Image{}, first.MediaID).Error; err != nil {
		t.Fatal(err)
	}
	state = explorationUpdate(t, s, explorationRequest(state, "refresh"))
	card, err := s.Items(context.Background(), ExplorationItemsRequest{ExplorationContext: state.ExplorationContext, EntryIDs: []uint{first.EntryID}})
	if err != nil || card.Items[0].UnavailableReason == "" || card.Items[0].ThumbnailURL != "" {
		t.Fatalf("invisible exposed %+v %v", card, err)
	}
	if err := database.DB.Unscoped().Model(&models.Image{}).Where("id = ?", first.MediaID).Update("deleted_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	newImage := models.Image{Name: "new", Path: "/explore/new.jpg"}
	if err := database.DB.Create(&newImage).Error; err != nil {
		t.Fatal(err)
	}
	state = explorationUpdate(t, s, explorationRequest(state, "next"))
	found := false
	for _, e := range state.Entries {
		if e.MediaID == first.MediaID {
			t.Fatal("restored shown item resurrected")
		}
		if e.MediaID == newImage.ID {
			found = true
		}
	}
	if !found || state.AvailableCount != 4 || state.PresentedCount != 1 {
		t.Fatal("new import or restored history lost")
	}
}

func TestMediaExplorationVersionGroupsAndSampleIdempotency(t *testing.T) {
	s, _ := explorationFixture(t, 0)
	root := t.TempDir()
	path := filepath.Join(root, "one.mp4")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	a := models.Video{Name: "one", Path: path, Directory: root, Duration: 100}
	b := models.Video{Name: "two", Path: filepath.Join(root, "two.mp4"), Directory: root, Duration: 100}
	if err := database.DB.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	group := models.VideoVersionGroup{Title: "same", Revision: 1}
	if err := database.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	members := []models.VideoVersionMember{{VideoID: a.ID, GroupID: group.ID, Position: 1}, {VideoID: b.ID, GroupID: group.ID, Position: 2}}
	if err := database.DB.Create(&members).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := s.Open(context.Background(), "video")
	if err != nil || len(state.Entries) != 1 {
		t.Fatal("versions not grouped", err)
	}
	entry := state.Entries[0]
	src := a.Path
	if entry.MediaID == b.ID {
		src = b.Path
	}
	info, _ := os.Stat(src)
	r := explorationRequest(state, "sample")
	r.EntryID = entry.EntryID
	r.SourceVersion = videoSourceVersion(entry.MediaID, info.Size(), info.ModTime().UnixNano())
	state = explorationUpdate(t, s, r)
	again := explorationUpdate(t, s, r)
	if !reflect.DeepEqual(state.Entries[0].Samples, again.Entries[0].Samples) || len(again.Entries[0].Samples) != 1 {
		t.Fatal("sample replay consumed a new point")
	}
	state = explorationPresent(t, s, state, []uint{entry.EntryID})
	if err := database.DB.Where("group_id = ?", group.ID).Delete(&models.VideoVersionMember{}).Error; err != nil {
		t.Fatal(err)
	}
	state = explorationUpdate(t, s, explorationRequest(state, "next"))
	if state.RemainingCount != 0 || state.AvailableCount != 2 {
		t.Fatalf("split resurrected shown members %+v", state)
	}
	var played int64
	database.DB.Model(&models.PlayEvent{}).Count(&played)
	if played != 0 {
		t.Fatal("exploration wrote watching")
	}
}

func TestMediaExplorationViewPersistsAndRejectsForeignSelection(t *testing.T) {
	s, state := explorationFixture(t, 2)
	r := explorationRequest(state, "save_view")
	r.View = &ExplorationView{Mode: "gallery", Scale: 1.2, X: -312, Y: -95, SelectedID: state.Entries[0].EntryID, GalleryID: state.Entries[1].EntryID}
	state = explorationUpdate(t, s, r)
	got, err := s.Open(context.Background(), "all")
	if err != nil || !reflect.DeepEqual(got.View, *r.View) || got.PresentedCount != 0 {
		t.Fatalf("camera persisted incorrectly %+v %v", got, err)
	}
	r = explorationRequest(state, "save_view")
	r.View = &ExplorationView{Mode: "wall", Scale: .4, SelectedID: 999999}
	if _, err := s.Update(context.Background(), r); !errors.Is(err, ErrExplorationInvalid) {
		t.Fatalf("foreign selection %v", err)
	}
	after, err := s.Open(context.Background(), "all")
	if err != nil || after.Revision != state.Revision || !reflect.DeepEqual(after.View, got.View) {
		t.Fatal("invalid camera mutated state")
	}
}

func TestMediaExplorationBlacklistRecheckedAfterSceneCreation(t *testing.T) {
	s, state := explorationFixture(t, 2)
	id := state.Entries[0].EntryID
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("image_scan_exclude_paths", "/explore").Error; err != nil {
		t.Fatal(err)
	}
	items, err := s.Items(context.Background(), ExplorationItemsRequest{ExplorationContext: state.ExplorationContext, EntryIDs: []uint{id}})
	if err != nil || items.Items[0].UnavailableReason == "" || items.Items[0].Title != "" {
		t.Fatalf("blacklisted media leaked %+v %v", items, err)
	}
	state = explorationUpdate(t, s, explorationRequest(state, "next"))
	if state.AvailableCount != 0 || state.Status != "empty" {
		t.Fatal("scope not rechecked")
	}
}

func TestMediaExplorationSchemaDoesNotCascadeMediaDeletion(t *testing.T) {
	s, state := explorationFixture(t, 1)
	item := state.Entries[0]
	state = explorationPresent(t, s, state, []uint{item.EntryID})
	if err := database.DB.Unscoped().Delete(&models.Image{}, item.MediaID).Error; err != nil {
		t.Fatal(err)
	}
	var retained models.MediaExplorationEntry
	if err := database.DB.First(&retained, item.EntryID).Error; err != nil || retained.PresentedAt == nil {
		t.Fatalf("logical presentation reference cascaded %v", err)
	}
	if _, err := s.Items(context.Background(), ExplorationItemsRequest{ExplorationContext: state.ExplorationContext, EntryIDs: []uint{item.EntryID, item.EntryID}}); !errors.Is(err, ErrExplorationInvalid) {
		t.Fatal("duplicate IDs accepted")
	}
}

func explorationVersionFixture(t *testing.T) (*MediaExplorationService, *ExplorationSnapshot, []models.Video, models.VideoVersionGroup) {
	t.Helper()
	setupVideoServiceTestDB(t)
	rows := []models.Video{{Name: "a", Path: "/versions/a.mp4", Directory: "/versions"}, {Name: "b", Path: "/versions/b.mp4", Directory: "/versions"}, {Name: "c", Path: "/versions/c.mp4", Directory: "/versions", IsStale: true}}
	if err := database.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	group := models.VideoVersionGroup{Title: "group", Revision: 1}
	if err := database.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&[]models.VideoVersionMember{{VideoID: rows[0].ID, GroupID: group.ID, Position: 1}, {VideoID: rows[1].ID, GroupID: group.ID, Position: 2}}).Error; err != nil {
		t.Fatal(err)
	}
	s := NewMediaExplorationService()
	state, err := s.Open(context.Background(), "video")
	if err != nil {
		t.Fatal(err)
	}
	return s, state, rows, group
}

func TestMediaExplorationRepresentativeMustRemainInItsGroup(t *testing.T) {
	s, state, rows, group := explorationVersionFixture(t)
	if err := database.DB.Model(&models.MediaExplorationEntry{}).Where("id = ?", state.Entries[0].EntryID).Update("media_id", rows[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Where("video_id = ?", rows[1].ID).Delete(&models.VideoVersionMember{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", rows[2].ID).Update("is_stale", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.VideoVersionMember{VideoID: rows[2].ID, GroupID: group.ID, Position: 2}).Error; err != nil {
		t.Fatal(err)
	}
	state = explorationUpdate(t, s, explorationRequest(state, "next"))
	ids := []uint{}
	for _, e := range state.Entries {
		ids = append(ids, e.EntryID)
	}
	items, err := s.Items(context.Background(), ExplorationItemsRequest{ExplorationContext: state.ExplorationContext, EntryIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Items) != 2 {
		t.Fatal("wrong number of works")
	}
	for _, item := range items.Items {
		if item.UnavailableReason != "" {
			t.Fatal("departed representative poisoned group", item.UnavailableReason)
		}
	}
}

func TestMediaExplorationHiddenDepartedMemberKeepsPresentationHistory(t *testing.T) {
	s, state, rows, _ := explorationVersionFixture(t)
	state = explorationPresent(t, s, state, []uint{state.Entries[0].EntryID})
	if err := database.DB.Model(&models.Video{}).Where("id = ?", rows[1].ID).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Where("video_id = ?", rows[1].ID).Delete(&models.VideoVersionMember{}).Error; err != nil {
		t.Fatal(err)
	}
	state = explorationUpdate(t, s, explorationRequest(state, "next"))
	if err := database.DB.Model(&models.Video{}).Where("id = ?", rows[1].ID).Update("is_stale", false).Error; err != nil {
		t.Fatal(err)
	}
	state = explorationUpdate(t, s, explorationRequest(state, "next"))
	if state.RemainingCount != 0 || state.AvailableCount != 2 || state.PresentedCount != 2 {
		t.Fatalf("hidden shown member resurrected: available=%d shown=%d remaining=%d", state.AvailableCount, state.PresentedCount, state.RemainingCount)
	}
}

func TestMediaExplorationNewImportsAfterEmptyRound(t *testing.T) {
	s, state := explorationFixture(t, 0)
	row := models.Image{Name: "new", Path: "/new/image.jpg", Directory: "/new"}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	state = explorationUpdate(t, s, explorationRequest(state, "next"))
	if state.Status != "ready" || state.AvailableCount != 1 || len(state.Entries) != 1 || state.Entries[0].MediaID != row.ID {
		t.Fatal("empty scope could not discover import")
	}
}

func TestMediaExplorationStaleRevisionCannotOverwriteSameScene(t *testing.T) {
	s, original := explorationFixture(t, 2)
	current := explorationPresent(t, s, original, []uint{original.Entries[0].EntryID})
	request := explorationRequest(original, "save_view")
	request.View = &ExplorationView{Mode: "wall", Scale: 2, X: -500, Y: -500}
	if _, err := s.Update(context.Background(), request); !errors.Is(err, ErrExplorationConflict) {
		t.Fatalf("stale camera accepted: %v", err)
	}
	actual, err := s.Open(context.Background(), "all")
	if err != nil || actual.Revision != current.Revision || actual.PresentedCount != 1 || actual.View.Scale != current.View.Scale {
		t.Fatal("stale write changed the current scene")
	}
}
