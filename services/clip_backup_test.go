package services

import (
	"github.com/google/uuid"
	"gorm.io/gorm"
	"testing"
	"time"
	"video-master/models"
)

func seedClipBackupRows(t *testing.T, db *gorm.DB, videoID uint, at time.Time) []any {
	t.Helper()
	end := int64(2000)
	book := &models.VideoBookmark{VideoID: videoID, VideoTitle: "backup source", Title: "clip bookmark", Note: "场景与片段", StartMS: 1000, EndMS: &end, TagsJSON: "[]", SourceSize: 123, SourceModTimeNS: at.UnixNano(), Revision: 9, CreatedAt: at, UpdatedAt: at}
	topic := &models.ClipTopic{Name: "backup topic", Note: "topic description", Revision: 7, CreatedAt: at, UpdatedAt: at}
	for _, row := range []any{book, topic} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	member := &models.ClipTopicMember{TopicID: topic.ID, BookmarkID: book.ID, Position: 1}
	if err := db.Create(member).Error; err != nil {
		t.Fatal(err)
	}
	start, size, mtime := int64(1000), int64(123), at.UnixNano()
	position, count := 1, 1
	batch := uuid.NewString()
	entry := &models.PlaybackQueueEntry{VideoID: videoID, Title: "frozen backup clip", Position: 1, EntryKind: "clip", ClipStartMS: &start, ClipEndMS: &end, SourceSize: &size, SourceModTimeNS: &mtime, OriginBookmarkID: &book.ID, PreviewBatchID: &batch, PreviewPosition: &position, PreviewCount: &count, CreatedAt: at}
	if err := db.Create(entry).Error; err != nil {
		t.Fatal(err)
	}
	state := &models.PlaybackQueueState{ID: 1, Revision: 17, Player: "inline", Status: "interrupted", CurrentEntryID: &entry.ID, UpdatedAt: at}
	if err := db.Create(state).Error; err != nil {
		t.Fatal(err)
	}
	return []any{book, topic, member, entry, state}
}
func assertClipBackupPrivateFields(t *testing.T, db *gorm.DB) {
	t.Helper()
	var book models.VideoBookmark
	var entry models.PlaybackQueueEntry
	if err := db.First(&book).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if entry.SourceSize == nil || *entry.SourceSize != 123 || entry.SourceModTimeNS == nil || *entry.SourceModTimeNS != book.SourceModTimeNS || book.SourceSize != 123 || book.TagsJSON != "[]" {
		t.Fatalf("lost private clip source fields %+v %+v", book, entry)
	}
}
func assertClipBackupManifest(t *testing.T, manifest *BackupManifest) {
	t.Helper()
	for _, table := range []string{"clip_topics", "clip_topic_members", "video_bookmarks", "playback_queue_entries", "playback_queue_states"} {
		if len(manifest.Models[table]) == 0 {
			t.Fatalf("missing clip model %s", table)
		}
		if manifest.Backend == "sqlite" && manifest.Rows[table] != 1 {
			t.Fatalf("empty clip backup %s", table)
		}
	}
	older := map[string][]string{}
	for table, columns := range manifest.Models {
		older[table] = append([]string{}, columns...)
	}
	delete(older, "clip_topics")
	delete(older, "clip_topic_members")
	added := map[string]bool{"entry_kind": true, "clip_start_ms": true, "clip_end_ms": true, "source_size": true, "source_mtime_ns": true, "origin_bookmark_id": true, "preview_batch_id": true, "preview_position": true, "preview_count": true}
	columns := []string{}
	for _, name := range older["playback_queue_entries"] {
		if !added[name] {
			columns = append(columns, name)
		}
	}
	older["playback_queue_entries"] = columns
	if unknown := unknownModelSchema(manifest.Models, older, nil); len(unknown) != 11 {
		t.Fatalf("old queue shape accepted clip backup: %v", unknown)
	}
	if unknown := unknownModelSchema(older, manifest.Models, nil); len(unknown) != 0 {
		t.Fatalf("new shape rejected older queue backup: %v", unknown)
	}
}
