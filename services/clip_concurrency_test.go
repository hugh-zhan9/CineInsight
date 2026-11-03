package services

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestClipTopicConcurrentAddAndBookmarkDelete(t *testing.T) {
	service, topic, books := clipTopicFixture(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := service.EditClipTopic(context.Background(), ClipTopicEdit{TopicID: topic.Topic.ID, ExpectedRevision: topic.Topic.Revision, Action: "add", BookmarkIDs: []uint{books[0].ID}})
		results <- err
	}()
	go func() { <-start; results <- service.Delete(context.Background(), books[0].ID, books[0].Revision) }()
	close(start)
	success := 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if !errors.Is(err, ErrViewingNoteConflict) && !errors.Is(err, ErrClipTopicChanged) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("competing intents accepted: %d", success)
	}
	var bookCount, memberCount int64
	if err := database.DB.Model(&models.VideoBookmark{}).Where("id = ?", books[0].ID).Count(&bookCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.ClipTopicMember{}).Where("bookmark_id = ?", books[0].ID).Count(&memberCount).Error; err != nil {
		t.Fatal(err)
	}
	if bookCount != memberCount {
		t.Fatalf("lost relationship coordination: %d/%d", bookCount, memberCount)
	}
}
func TestClipQueueFullSnapshotCASRejectsLateBookmarkEdit(t *testing.T) {
	queue, topic, books := clipQueueFixture(t)
	ctx := context.Background()
	state := queueSnapshot(t, queue)
	preview, err := queue.PreviewClipQueue(ctx, ClipQueueRequest{TopicID: topic.Topic.ID, TopicRevision: topic.Topic.Revision, BookmarkIDs: []uint{books[0].ID}, QueueRevision: state.State.Revision})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var intercepted atomic.Bool
	db := database.DB
	if err := db.Callback().Update().Before("gorm:update").Register("clip_fixture:late_book", func(tx *gorm.DB) {
		if tx.Statement.Table != "playback_queue_states" || !intercepted.CompareAndSwap(false, true) {
			return
		}
		if dbtest.IsPostgres() {
			close(entered)
			select {
			case <-release:
			case <-time.After(5 * time.Second):
				tx.AddError(errors.New("fixture source edit did not finish"))
			}
		} else {
			// SQLite serializes writers. Inject the same late change in this transaction
			// to verify the SQL predicate; PostgreSQL below uses a genuine second writer.
			tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Model(&models.VideoBookmark{}).Where("id = ?", books[0].ID).UpdateColumn("revision", gorm.Expr("revision + 1")).Error)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Update().Remove("clip_fixture:late_book") })
	done := make(chan error, 1)
	go func() {
		_, err := queue.AppendClipQueue(ctx, AppendClipQueueRequest{PreviewToken: preview.PreviewToken})
		done <- err
	}()
	if dbtest.IsPostgres() {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("CAS not reached")
		}
		_, editErr := (&BookmarkService{}).Save(ctx, BookmarkInput{ID: books[0].ID, Revision: books[0].Revision, VideoID: books[0].VideoID, Title: "edited after preview validation", StartMS: books[0].StartMS, EndMS: books[0].EndMS})
		close(release)
		if editErr != nil {
			t.Fatal(editErr)
		}
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrClipPreviewExpired) {
			t.Fatalf("stale source committed: %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("append did not finish")
	}
	db.Callback().Update().Remove("clip_fixture:late_book")
	after := queueSnapshot(t, queue)
	if after.Total != state.Total || after.State.Revision != state.State.Revision {
		t.Fatalf("partial queue write %+v", after)
	}
}
