package migrator

import (
	"context"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestMigrateClipTopicsAndFrozenQueueRoundTrip(t *testing.T) {
	source, middle, back := sequenceSQLite(t), dbtest.Open(t), sequenceSQLite(t)
	end := int64(2000)
	book := models.VideoBookmark{VideoID: 999, VideoTitle: "retained missing media", Title: "bookmark", Note: "note", StartMS: 1000, EndMS: &end, TagsJSON: `["精选"]`, SourceSize: 123, SourceModTimeNS: 1700000000123456789, Revision: 7}
	if err := source.Create(&book).Error; err != nil {
		t.Fatal(err)
	}
	topic := models.ClipTopic{Name: "topic", Note: "topic note", Revision: 4}
	if err := source.Create(&topic).Error; err != nil {
		t.Fatal(err)
	}
	if err := source.Create(&models.ClipTopicMember{TopicID: topic.ID, BookmarkID: book.ID, Position: 1}).Error; err != nil {
		t.Fatal(err)
	}
	batch := uuid.NewString()
	position, count := 1, 1
	start, size, mtime := int64(1000), int64(123), int64(1700000000123456789)
	clip := models.PlaybackQueueEntry{VideoID: book.VideoID, Title: "frozen title", EntryKind: "clip", Position: 5, ClipStartMS: &start, ClipEndMS: &end, SourceSize: &size, SourceModTimeNS: &mtime, OriginBookmarkID: &book.ID, PreviewBatchID: &batch, PreviewPosition: &position, PreviewCount: &count}
	if err := source.Create(&clip).Error; err != nil {
		t.Fatal(err)
	}
	whole := models.PlaybackQueueEntry{VideoID: 998, Title: "whole", Position: 3}
	if err := source.Create(&whole).Error; err != nil {
		t.Fatal(err)
	}
	state := models.PlaybackQueueState{ID: 1, Revision: 11, Player: "inline", Status: "interrupted", CurrentEntryID: &clip.ID}
	if err := source.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		if _, err := Migrate(context.Background(), Options{Source: step.from, Target: step.to, TargetBackend: database.Backend(step.to.Dialector.Name())}); err != nil {
			t.Fatal(err)
		}
		var got models.PlaybackQueueEntry
		if err := step.to.First(&got, clip.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.EntryKind != "clip" || got.ClipStartMS == nil || *got.ClipStartMS != start || got.ClipEndMS == nil || *got.ClipEndMS != end || got.SourceSize == nil || *got.SourceSize != size || got.SourceModTimeNS == nil || *got.SourceModTimeNS != mtime || got.PreviewBatchID == nil || *got.PreviewBatchID != batch || got.PreviewPosition == nil || *got.PreviewPosition != 1 || got.PreviewCount == nil || *got.PreviewCount != 1 || got.OriginBookmarkID == nil || *got.OriginBookmarkID != book.ID {
			t.Fatalf("lost clip snapshot %+v", got)
		}
		var gotBook models.VideoBookmark
		if err := step.to.First(&gotBook, book.ID).Error; err != nil || gotBook.Revision != 7 || gotBook.TagsJSON != book.TagsJSON || gotBook.SourceModTimeNS != book.SourceModTimeNS {
			t.Fatalf("lost source bookmark %+v %v", gotBook, err)
		}
		var gotTopic models.ClipTopic
		if err := step.to.First(&gotTopic, topic.ID).Error; err != nil || gotTopic.Revision != 4 || gotTopic.Note != topic.Note {
			t.Fatalf("lost topic %+v %v", gotTopic, err)
		}
		var member models.ClipTopicMember
		if err := step.to.First(&member, "topic_id = ? AND bookmark_id = ?", topic.ID, book.ID).Error; err != nil || member.Position != 1 {
			t.Fatalf("lost membership %+v %v", member, err)
		}
		var gotState models.PlaybackQueueState
		if err := step.to.First(&gotState, 1).Error; err != nil || gotState.Revision != 11 || gotState.Status != "interrupted" || gotState.CurrentEntryID == nil || *gotState.CurrentEntryID != clip.ID {
			t.Fatalf("queue resumed or changed %+v %v", gotState, err)
		}
	}
}
