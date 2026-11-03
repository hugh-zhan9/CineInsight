package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"video-master/database"
	"video-master/models"
)

func clipQueueFixture(t *testing.T) (*PlaybackQueueService, ClipTopicDTO, []BookmarkDTO) {
	t.Helper()
	queue, videos := queueRuntimeFixture(t, false)
	bookmarks := &BookmarkService{}
	topic, err := bookmarks.SaveClipTopic(context.Background(), ClipTopicInput{Name: "预览批次"})
	if err != nil {
		t.Fatal(err)
	}
	books := []BookmarkDTO{}
	for i, video := range videos[:2] {
		session, err := (&VideoService{}).GetPreviewSession(video.ID)
		if err != nil {
			t.Fatal(err)
		}
		end := int64(20000 + i*1000)
		book, err := bookmarks.Save(context.Background(), BookmarkInput{VideoID: video.ID, StartMS: 10000, EndMS: &end, Title: video.Name, SourceToken: session.SourceVersion})
		if err != nil {
			t.Fatal(err)
		}
		books = append(books, *book)
	}
	topic, err = bookmarks.EditClipTopic(context.Background(), ClipTopicEdit{TopicID: topic.Topic.ID, ExpectedRevision: topic.Topic.Revision, Action: "add", BookmarkIDs: []uint{books[0].ID, books[1].ID}})
	if err != nil {
		t.Fatal(err)
	}
	for i := range books {
		var row models.VideoBookmark
		if err := database.DB.First(&row, books[i].ID).Error; err != nil {
			t.Fatal(err)
		}
		books[i].VideoBookmark = row
	}
	return queue, *topic, books
}
func TestClipQueuePreviewIsExplicitAndPlaybackDoesNotRecordViews(t *testing.T) {
	queue, topic, books := clipQueueFixture(t)
	ctx := context.Background()
	state := queueSnapshot(t, queue)
	preview, err := queue.PreviewClipQueue(ctx, ClipQueueRequest{TopicID: topic.Topic.ID, TopicRevision: topic.Topic.Revision, BookmarkIDs: []uint{books[0].ID}, QueueRevision: state.State.Revision})
	if err != nil || preview.PreviewToken == "" || preview.Conflicts != 0 {
		t.Fatalf("%+v %v", preview, err)
	}
	appended, err := queue.AppendClipQueue(ctx, AppendClipQueueRequest{PreviewToken: preview.PreviewToken})
	if err != nil || !appended.PlayRequested || len(appended.EntryIDs) != 1 {
		t.Fatalf("%+v %v", appended, err)
	}
	snapshot := waitQueueSnapshot(t, queue, func(snapshot *QueueSnapshot) bool { return snapshot.Session != nil })
	if snapshot.Session.EntryKind != "clip" || snapshot.Session.StartSeconds != 10 || snapshot.Session.ClipEndMS == nil || *snapshot.Session.ClipEndMS != 20000 {
		t.Fatalf("wrong clip session %+v", snapshot.Session)
	}
	session := snapshot.Session
	for i, kind := range []string{"loaded", "playing", "progress", "paused"} {
		position := 10.0
		if kind == "progress" || kind == "paused" {
			position = 15
		}
		err := queue.ReportInline(ctx, InlineQueueEvent{Token: session.Token, VideoID: session.VideoID, SourceVersion: session.SourceVersion, Seq: uint64(i + 1), ViewCycle: 1, Kind: kind, Position: position, Duration: 120})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := queue.Stop(ctx, session.Token); err != nil {
		t.Fatal(err)
	}
	var video models.Video
	if err := database.DB.First(&video, session.VideoID).Error; err != nil {
		t.Fatal(err)
	}
	if video.PlayCount != 0 || video.RandomPlayCount != 0 || video.IsWatched || video.WatchPositionSeconds != 0 {
		t.Fatalf("preview changed viewing facts %+v", video)
	}
	mustNoteCount(t, &models.PlayEvent{}, 0)
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 0)
}

func appendTopicBatch(t *testing.T, queue *PlaybackQueueService, topic ClipTopicDTO) *ClipQueueAppendResult {
	t.Helper()
	snapshot := queueSnapshot(t, queue)
	preview, err := queue.PreviewClipQueue(context.Background(), ClipQueueRequest{TopicID: topic.Topic.ID, TopicRevision: topic.Topic.Revision, All: true, QueueRevision: snapshot.State.Revision})
	if err != nil || preview.PreviewToken == "" {
		t.Fatalf("preview %+v %v", preview, err)
	}
	result, err := queue.AppendClipQueue(context.Background(), AppendClipQueueRequest{PreviewToken: preview.PreviewToken})
	if err != nil || !result.PlayRequested {
		t.Fatalf("append %+v %v", result, err)
	}
	return result
}
func sendClipEvents(t *testing.T, queue *PlaybackQueueService, session *QueueSession, end float64) {
	t.Helper()
	for i, kind := range []string{"loaded", "playing", "ended"} {
		position := session.StartSeconds
		if kind == "ended" {
			position = end
		}
		if err := queue.ReportInline(context.Background(), InlineQueueEvent{Token: session.Token, VideoID: session.VideoID, SourceVersion: session.SourceVersion, Seq: uint64(i + 1), ViewCycle: 1, Kind: kind, Position: position, Duration: 120}); err != nil {
			t.Fatal(err)
		}
	}
}
func TestClipQueueFrozenBatchDoesNotInheritAutoplayOrReachLaterVideo(t *testing.T) {
	queue, topic, books := clipQueueFixture(t)
	ctx := context.Background()
	appended := appendTopicBatch(t, queue, topic)
	first := waitQueueSnapshot(t, queue, func(snapshot *QueueSnapshot) bool { return snapshot.Session != nil })
	if first.State.Autoplay {
		t.Fatal("fixture must have whole-video autoplay off")
	}
	if err := queue.Edit(ctx, QueueEdit{ExpectedRevision: first.State.Revision, Action: "append_videos", VideoIDs: []uint{books[0].VideoID}}); err != nil {
		t.Fatal(err)
	}
	sendClipEvents(t, queue, first.Session, 20)
	second := waitQueueSnapshot(t, queue, func(snapshot *QueueSnapshot) bool {
		return snapshot.Session != nil && snapshot.Session.Token != first.Session.Token
	})
	if second.Current.ID != appended.EntryIDs[1] {
		t.Fatal("did not advance inside explicit batch")
	}
	if err := queue.ReportInline(ctx, InlineQueueEvent{Token: first.Session.Token, VideoID: first.Session.VideoID, SourceVersion: first.Session.SourceVersion, Seq: 4, ViewCycle: 1, Kind: "ended", Position: 20, Duration: 120}); !errors.Is(err, ErrQueueChanged) {
		t.Fatalf("duplicate token accepted: %v", err)
	}
	sendClipEvents(t, queue, second.Session, 21)
	done := queueSnapshot(t, queue)
	if done.Session != nil || done.State.ActiveToken != "" || done.Current.ID != appended.EntryIDs[1] || done.State.Status != "ended" {
		t.Fatalf("escaped frozen batch %+v", done)
	}
	mustNoteCount(t, &models.PlayEvent{}, 0)
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 0)
}
func TestClipQueueRejectsPartialReorderAndUnsupportedPlayer(t *testing.T) {
	queue, topic, _ := clipQueueFixture(t)
	ctx := context.Background()
	appended := appendTopicBatch(t, queue, topic)
	snapshot := waitQueueSnapshot(t, queue, func(snapshot *QueueSnapshot) bool { return snapshot.Session != nil })
	if err := queue.Edit(ctx, QueueEdit{ExpectedRevision: snapshot.State.Revision, Action: "move", EntryID: appended.EntryIDs[0]}); !errors.Is(err, ErrClipBatchOrder) {
		t.Fatalf("reordered clip: %v", err)
	}
	autoplay := false
	if err := queue.Edit(ctx, QueueEdit{ExpectedRevision: snapshot.State.Revision, Action: "configure", Player: "system", Autoplay: &autoplay}); !errors.Is(err, ErrClipPlayerUnsupported) {
		t.Fatalf("unsupported player: %v", err)
	}
	if current := queueSnapshot(t, queue); current.State.Player != "inline" || current.Session.Token != snapshot.Session.Token {
		t.Fatal("rejected change stopped preview")
	}
}
func TestClipQueueRemovingAnyActiveBatchMemberStopsBeforeEdit(t *testing.T) {
	queue, topic, _ := clipQueueFixture(t)
	ctx := context.Background()
	appended := appendTopicBatch(t, queue, topic)
	snapshot := waitQueueSnapshot(t, queue, func(snapshot *QueueSnapshot) bool { return snapshot.Session != nil })
	if err := queue.Edit(ctx, QueueEdit{ExpectedRevision: snapshot.State.Revision, Action: "remove", EntryID: appended.EntryIDs[1]}); err != nil {
		t.Fatal(err)
	}
	removed := queueSnapshot(t, queue)
	if removed.Session != nil || removed.State.ActiveToken != "" || removed.State.Status != "stopped" {
		t.Fatalf("batch continued after removal %+v", removed)
	}
	if err := queue.Play(ctx, appended.EntryIDs[0], removed.State.Revision); err != nil {
		t.Fatal(err)
	}
	replay := waitQueueSnapshot(t, queue, func(snapshot *QueueSnapshot) bool { return snapshot.Session != nil })
	sendClipEvents(t, queue, replay.Session, 20)
	done := queueSnapshot(t, queue)
	if done.Session != nil || done.State.LastErrorCode != "clip_batch_changed" {
		t.Fatalf("missing next position was skipped %+v", done)
	}
}

func TestClipQueueNextUnavailableStopsAtThatEntry(t *testing.T) {
	queue, topic, books := clipQueueFixture(t)
	appended := appendTopicBatch(t, queue, topic)
	first := waitQueueSnapshot(t, queue, func(snapshot *QueueSnapshot) bool { return snapshot.Session != nil })
	if err := database.DB.Model(&models.Video{}).Where("id = ?", books[1].VideoID).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	sendClipEvents(t, queue, first.Session, 20)
	failed := waitQueueSnapshot(t, queue, func(snapshot *QueueSnapshot) bool { return snapshot.State.Status == "failed" })
	if failed.Current == nil || failed.Current.ID != appended.EntryIDs[1] || failed.Session != nil {
		t.Fatalf("did not stop on unavailable next clip: %+v", failed)
	}
	mustNoteCount(t, &models.PlayEvent{}, 0)
}

func TestClipQueueThousandItemSnapshotAndLimit(t *testing.T) {
	queue, videos := queueRuntimeFixture(t, false)
	service := &BookmarkService{}
	ctx := context.Background()
	topic, err := service.SaveClipTopic(ctx, ClipTopicInput{Name: "thousand"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(videos[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	end := int64(2000)
	books := make([]models.VideoBookmark, 1000)
	for i := range books {
		books[i] = models.VideoBookmark{VideoID: videos[0].ID, Title: fmt.Sprintf("clip-%d", i), StartMS: 1000, EndMS: &end, TagsJSON: "[]", SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(), Revision: 1}
	}
	if err := database.DB.CreateInBatches(&books, 200).Error; err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < len(books); offset += 200 {
		ids := []uint{}
		for _, book := range books[offset : offset+200] {
			ids = append(ids, book.ID)
		}
		topic, err = service.EditClipTopic(ctx, ClipTopicEdit{TopicID: topic.Topic.ID, ExpectedRevision: topic.Topic.Revision, Action: "add", BookmarkIDs: ids})
		if err != nil {
			t.Fatal(err)
		}
	}
	state := queueSnapshot(t, queue)
	preview, err := queue.PreviewClipQueue(ctx, ClipQueueRequest{TopicID: topic.Topic.ID, TopicRevision: topic.Topic.Revision, All: true, QueueRevision: state.State.Revision})
	if err != nil || len(preview.Items) != 1000 || preview.PreviewToken == "" {
		t.Fatalf("limit preview: %+v %v", preview, err)
	}
	appended, err := queue.AppendClipQueue(ctx, AppendClipQueueRequest{PreviewToken: preview.PreviewToken})
	if err != nil || len(appended.EntryIDs) != 1000 {
		t.Fatalf("limit append count/error %v", err)
	}
	var unchanged models.VideoBookmark
	if err := database.DB.First(&unchanged, books[0].ID).Error; err != nil || unchanged.Revision != 2 {
		t.Fatalf("read-only source version changed %+v %v", unchanged, err)
	}
	var unchangedTopic models.ClipTopic
	if err := database.DB.First(&unchangedTopic, topic.Topic.ID).Error; err != nil || unchangedTopic.Revision != topic.Topic.Revision {
		t.Fatalf("topic version changed %+v %v", unchangedTopic, err)
	}
	extra := books[0]
	extra.ID = 0
	extra.Title = "extra"
	if err := database.DB.Create(&extra).Error; err != nil {
		t.Fatal(err)
	}
	topic, err = service.EditClipTopic(ctx, ClipTopicEdit{TopicID: topic.Topic.ID, ExpectedRevision: topic.Topic.Revision, Action: "add", BookmarkIDs: []uint{extra.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.PreviewClipQueue(ctx, ClipQueueRequest{TopicID: topic.Topic.ID, TopicRevision: topic.Topic.Revision, All: true, QueueRevision: queueSnapshot(t, queue).State.Revision}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("accepted >1000 items: %v", err)
	}
}
