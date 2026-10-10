package services

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func queueStoreFixture(t *testing.T) []models.Video {
	t.Helper()
	setupVideoServiceTestDB(t)
	if err := initializePlaybackQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	videos := make([]models.Video, 3)
	for i := range videos {
		videos[i] = models.Video{Name: fmt.Sprintf("part%d", i), DisplayTitle: fmt.Sprintf("第%d部", i), Path: fmt.Sprintf("/queue-test/%d.mp4", i), Directory: "/queue-test"}
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
	return videos
}
func queuePage(t *testing.T) *QueuePage {
	t.Helper()
	page, err := loadQueuePage(context.Background(), QueueQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	return page
}
func queueEdit(t *testing.T, in QueueEdit) *models.PlaybackQueueState {
	t.Helper()
	in.ExpectedRevision = queuePage(t).State.Revision
	state, err := editPlaybackQueue(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestQueueStorePagingDuplicatesSnapshotAndCAS(t *testing.T) {
	videos := queueStoreFixture(t)
	if p := queuePage(t); p.Total != 0 || p.State.Autoplay || p.State.Player != "system" || p.State.Status != "idle" {
		t.Fatalf("defaults: %+v", p)
	}
	ids := make([]uint, 51)
	for i := range ids {
		ids[i] = videos[i%3].ID
	}
	state := queueEdit(t, QueueEdit{Action: "append_videos", VideoIDs: ids})
	page, err := loadQueuePage(context.Background(), QueueQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 50 || !page.HasMore || page.Total != 51 || page.Items[0].ID == page.Items[3].ID || page.Items[0].VideoID != page.Items[3].VideoID {
		t.Fatalf("page or duplicate lost: %+v", page)
	}
	next := QueueQuery{Revision: state.Revision, CursorID: page.CursorID, CursorPosition: page.CursorPosition}
	last, err := loadQueuePage(context.Background(), next)
	if err != nil || len(last.Items) != 1 || last.HasMore {
		t.Fatalf("last: %+v %v", last, err)
	}
	if err := database.DB.Unscoped().Delete(&videos[0]).Error; err != nil {
		t.Fatal(err)
	}
	retained := queuePage(t)
	if retained.Total != 51 || retained.Items[0].Title != videos[0].DisplayTitle || retained.Items[0].SourceAvailable {
		t.Fatalf("delete lost snapshot: %+v", retained.Items[0])
	}
	queueEdit(t, QueueEdit{Action: "append_videos", VideoIDs: []uint{videos[1].ID}})
	if _, err = loadQueuePage(context.Background(), next); !errors.Is(err, ErrQueueChanged) {
		t.Fatalf("mixed pages: %v", err)
	}
	if _, err = editPlaybackQueue(context.Background(), QueueEdit{ExpectedRevision: state.Revision, Action: "clear"}); !errors.Is(err, ErrQueueChanged) {
		t.Fatalf("stale destructive command: %v", err)
	}
	if queuePage(t).Total != 52 {
		t.Fatal("stale clear wrote")
	}
}

func TestQueueStoreAtomicLimitsAndInvalidInputs(t *testing.T) {
	videos := queueStoreFixture(t)
	for _, in := range []QueueEdit{
		{Action: "append_videos"}, {Action: "append_videos", VideoIDs: []uint{0}},
		{Action: "clear", VideoIDs: []uint{videos[0].ID}}, {Action: "remove", EntryID: 999, BeforeEntryID: 1},
		{Action: "configure", Player: "system", Autoplay: queueBool(true)},
		{Action: "configure", Player: "other", Autoplay: queueBool(false)},
		{Action: "move", EntryID: 1, BeforeEntryID: 1},
	} {
		in.ExpectedRevision = 1
		if _, err := editPlaybackQueue(context.Background(), in); !errors.Is(err, ErrQueueInvalid) {
			t.Fatalf("invalid %+v: %v", in, err)
		}
	}
	for _, limit := range []int{-1, 201} {
		if _, err := loadQueuePage(context.Background(), QueueQuery{Limit: limit}); !errors.Is(err, ErrQueueInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := editPlaybackQueue(context.Background(), QueueEdit{ExpectedRevision: 1, Action: "append_videos", VideoIDs: []uint{videos[0].ID, 999999}}); !errors.Is(err, ErrQueueMediaUnavailable) {
		t.Fatal(err)
	}
	if p := queuePage(t); p.Total != 0 || p.State.Revision != 1 {
		t.Fatal("partial invalid append committed")
	}
	ids := make([]uint, 1001)
	for i := range ids {
		ids[i] = videos[0].ID
	}
	if _, err := editPlaybackQueue(context.Background(), QueueEdit{ExpectedRevision: 1, Action: "append_videos", VideoIDs: ids}); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		queueEdit(t, QueueEdit{Action: "append_videos", VideoIDs: ids[:1000]})
	}
	p := queuePage(t)
	if _, err := editPlaybackQueue(context.Background(), QueueEdit{ExpectedRevision: p.State.Revision, Action: "append_videos", VideoIDs: ids[:1]}); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	if got := queuePage(t); got.Total != 10000 || got.State.Revision != p.State.Revision {
		t.Fatalf("overflow partial state: %+v", got.State)
	}
}
func queueBool(v bool) *bool { return &v }

func TestQueueStoreCollectionOrderMoveAndRestart(t *testing.T) {
	v := queueStoreFixture(t)
	collection := models.MediaCollection{Name: "连续剧", NormalizedName: "连续剧"}
	if err := database.DB.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	for i, id := range []uint{v[2].ID, v[0].ID, v[1].ID} {
		if err := database.DB.Create(&models.CollectionVideo{CollectionID: collection.ID, VideoID: id, Position: i + 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	queueEdit(t, QueueEdit{Action: "replace_collection", CollectionID: collection.ID, StartVideoID: v[0].ID})
	p := queuePage(t)
	if len(p.Items) != 2 || p.Items[0].VideoID != v[0].ID || p.Items[1].VideoID != v[1].ID {
		t.Fatalf("order: %+v", p.Items)
	}
	if err := database.DB.Where("collection_id = ?", collection.ID).Delete(&models.CollectionVideo{}).Error; err != nil {
		t.Fatal(err)
	}
	queueEdit(t, QueueEdit{Action: "append_videos", VideoIDs: []uint{v[2].ID, v[0].ID}})
	p = queuePage(t)
	ids := []uint{p.Items[0].ID, p.Items[1].ID, p.Items[2].ID, p.Items[3].ID}
	queueEdit(t, QueueEdit{Action: "remove", EntryID: ids[1]})
	queueEdit(t, QueueEdit{Action: "move", EntryID: ids[0], BeforeEntryID: ids[3]})
	p = queuePage(t)
	actual := []uint{p.Items[0].ID, p.Items[1].ID, p.Items[2].ID}
	if !reflect.DeepEqual(actual, []uint{ids[2], ids[0], ids[3]}) {
		t.Fatal(actual)
	}
	queueEdit(t, QueueEdit{Action: "move", EntryID: ids[3], BeforeEntryID: ids[2]})
	queueEdit(t, QueueEdit{Action: "move", EntryID: ids[2]})
	queueEdit(t, QueueEdit{Action: "configure", Player: "inline", Autoplay: queueBool(true)})
	if err := database.DB.Model(&models.PlaybackQueueState{}).Where("id = 1").Updates(map[string]any{"current_entry_id": ids[3], "status": "playing", "active_token": "old-token"}).Error; err != nil {
		t.Fatal(err)
	}
	before := queuePage(t)
	if err := initializePlaybackQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	p = queuePage(t)
	if p.State.Status != "interrupted" || p.State.ActiveToken != "" || !p.State.Autoplay || p.State.Player != "inline" || p.Current.ID != ids[3] || p.State.Revision != before.State.Revision+1 {
		t.Fatalf("restart: %+v", p.State)
	}
	queueEdit(t, QueueEdit{Action: "remove", EntryID: ids[3]})
	if p = queuePage(t); p.Current != nil || p.State.Status != "idle" {
		t.Fatalf("current remove: %+v", p)
	}
	queueEdit(t, QueueEdit{Action: "clear"})
	if queuePage(t).Total != 0 {
		t.Fatal("clear failed")
	}
}

func TestQueueStoreConcurrentCASAndRollback(t *testing.T) {
	v := queueStoreFixture(t)
	plan, err := preparePlaybackQueueEdit(context.Background(), QueueEdit{ExpectedRevision: 1, Action: "append_videos", VideoIDs: []uint{v[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := applyPreparedQueueEdit(context.Background(), plan)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	won, lost := 0, 0
	for err := range results {
		if err == nil {
			won++
		} else if errors.Is(err, ErrQueueChanged) {
			lost++
		} else {
			t.Fatal(err)
		}
	}
	if won != 1 || lost != 1 || queuePage(t).Total != 1 {
		t.Fatalf("CAS won=%d lost=%d", won, lost)
	}
	p := queuePage(t)
	name := "test:queue-fail-create"
	if err := database.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "playback_queue_entries" {
			tx.AddError(errors.New("injected failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Callback().Create().Remove(name) })
	if _, err := editPlaybackQueue(context.Background(), QueueEdit{ExpectedRevision: p.State.Revision, Action: "append_videos", VideoIDs: []uint{v[0].ID}}); err == nil {
		t.Fatal("write failure hidden")
	}
	got := queuePage(t)
	if got.Total != p.Total || got.State.Revision != p.State.Revision {
		t.Fatal("failed edit left a revision or item")
	}
}

func TestQueueCollectionStartUsesSameStatementSnapshot(t *testing.T) {
	v := queueStoreFixture(t)
	collection := models.MediaCollection{Name: "reorder", NormalizedName: "reorder"}
	if err := database.DB.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	for i, video := range v {
		if err := database.DB.Create(&models.CollectionVideo{CollectionID: collection.ID, VideoID: video.ID, Position: i + 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	name := "test:queue_collection_reorder"
	var once sync.Once
	if err := database.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table != "videos" {
			return
		}
		once.Do(func() {
			if err := database.DB.Model(&models.CollectionVideo{}).Where("collection_id = ? AND video_id = ?", collection.ID, v[1].ID).Update("position", 1).Error; err != nil {
				tx.AddError(err)
			}
			if err := database.DB.Model(&models.CollectionVideo{}).Where("collection_id = ? AND video_id = ?", collection.ID, v[0].ID).Update("position", 2).Error; err != nil {
				tx.AddError(err)
			}
		})
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Query().Remove(name)
	videos, err := (&CollectionService{}).orderedActiveVideosFrom(database.DB, collection.ID, v[1].ID, false, 100)
	if err != nil || len(videos) != 3 || videos[0].ID != v[1].ID {
		t.Fatalf("reorder lost selected start: %+v %v", videos, err)
	}
}

func TestQueuePageConcurrentCurrentRemovalReportsChanged(t *testing.T) {
	v := queueStoreFixture(t)
	queueEdit(t, QueueEdit{Action: "append_videos", VideoIDs: []uint{v[0].ID}})
	before := queuePage(t)
	if err := database.DB.Model(&models.PlaybackQueueState{}).Where("id = 1").Update("current_entry_id", before.Items[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	name := "test:queue_current_removed"
	var once sync.Once
	database.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*models.PlaybackQueueEntry); !ok {
			return
		}
		once.Do(func() {
			_, err := editPlaybackQueue(context.Background(), QueueEdit{ExpectedRevision: before.State.Revision, Action: "clear"})
			if err != nil {
				tx.AddError(err)
			}
		})
	})
	defer database.DB.Callback().Query().Remove(name)
	if _, err := loadQueuePage(context.Background(), QueueQuery{}); !errors.Is(err, ErrQueueChanged) {
		t.Fatalf("concurrent removal classification: %v", err)
	}
}

func TestQueueMoveAcrossReorderedIDs(t *testing.T) {
	v := queueStoreFixture(t)
	queueEdit(t, QueueEdit{Action: "append_videos", VideoIDs: []uint{v[0].ID, v[1].ID, v[2].ID}})
	p := queuePage(t)
	a, b, c := p.Items[0].ID, p.Items[1].ID, p.Items[2].ID
	queueEdit(t, QueueEdit{Action: "move", EntryID: c, BeforeEntryID: a})
	queueEdit(t, QueueEdit{Action: "move", EntryID: c, BeforeEntryID: b})
	p = queuePage(t)
	if got := []uint{p.Items[0].ID, p.Items[1].ID, p.Items[2].ID}; !reflect.DeepEqual(got, []uint{a, c, b}) {
		t.Fatalf("move failed after prior reorder: %v", got)
	}
}

func TestQueuePreparedCollectionIsNotWidenedBeforeCommit(t *testing.T) {
	v := queueStoreFixture(t)
	c := models.MediaCollection{Name: "frozen", NormalizedName: "frozen"}
	if err := database.DB.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.CollectionVideo{CollectionID: c.ID, VideoID: v[0].ID, Position: 1}).Error; err != nil {
		t.Fatal(err)
	}
	plan, err := preparePlaybackQueueEdit(context.Background(), QueueEdit{Action: "replace_collection", CollectionID: c.ID, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.CollectionVideo{CollectionID: c.ID, VideoID: v[1].ID, Position: 2}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := applyPreparedQueueEdit(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	p := queuePage(t)
	if p.Total != 1 || p.Items[0].VideoID != v[0].ID {
		t.Fatalf("frozen plan expanded: %+v", p.Items)
	}
}

func TestQueueNextEpisodeUsesCurrentCollectionOrder(t *testing.T) {
	v := queueStoreFixture(t)
	c := models.MediaCollection{Name: "next", NormalizedName: "next"}
	if err := database.DB.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	for index, video := range []models.Video{v[2], v[1], v[0]} {
		if err := database.DB.Create(&models.CollectionVideo{CollectionID: c.ID, VideoID: video.ID, Position: index + 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	queueEdit(t, QueueEdit{Action: "replace_collection", CollectionID: c.ID, StartVideoID: v[1].ID, StartAfter: true})
	p := queuePage(t)
	if p.Total != 1 || p.Items[0].VideoID != v[0].ID {
		t.Fatalf("next episode used stale UI order: %+v", p.Items)
	}
	if _, err := editPlaybackQueue(context.Background(), QueueEdit{Action: "replace_collection", CollectionID: c.ID, StartVideoID: v[0].ID, StartAfter: true, ExpectedRevision: p.State.Revision}); !errors.Is(err, ErrQueueAtEnd) {
		t.Fatal(err)
	}
	if queuePage(t).State.Revision != p.State.Revision {
		t.Fatal("end of collection replaced queue")
	}
}

func TestQueueNextEpisodeClassifiesOnlyItsReadSnapshot(t *testing.T) {
	v := queueStoreFixture(t)
	c := models.MediaCollection{Name: "restored", NormalizedName: "restored"}
	if err := database.DB.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	for i, video := range v[:2] {
		if err := database.DB.Create(&models.CollectionVideo{CollectionID: c.ID, VideoID: video.ID, Position: i + 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Delete(&v[0]).Error; err != nil {
		t.Fatal(err)
	}
	name := "test:restore_queue_start_after_read"
	var once sync.Once
	database.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "videos" {
			once.Do(func() {
				if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", v[0].ID).Update("deleted_at", nil).Error; err != nil {
					tx.AddError(err)
				}
			})
		}
	})
	defer database.DB.Callback().Query().Remove(name)
	_, err := editPlaybackQueue(context.Background(), QueueEdit{Action: "replace_collection", CollectionID: c.ID, StartVideoID: v[0].ID, StartAfter: true, ExpectedRevision: 1})
	if !errors.Is(err, ErrQueueMediaUnavailable) {
		t.Fatalf("late restore changed the original snapshot's classification: %v", err)
	}
}
