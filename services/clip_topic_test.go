package services

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"video-master/database"
	"video-master/models"
)

func clipTopicFixture(t *testing.T) (*BookmarkService, ClipTopicDTO, []BookmarkDTO) {
	t.Helper()
	video, source := notesFixture(t)
	service := &BookmarkService{}
	topic, err := service.SaveClipTopic(context.Background(), ClipTopicInput{Name: "片段专题"})
	if err != nil {
		t.Fatal(err)
	}
	books := []BookmarkDTO{}
	for i := 0; i < 3; i++ {
		end := int64((i + 1) * 10000)
		book, err := service.Save(context.Background(), BookmarkInput{VideoID: video.ID, StartMS: int64(i * 10000), EndMS: &end, Title: fmt.Sprintf("片段%d", i), SourceToken: source})
		if err != nil {
			t.Fatal(err)
		}
		books = append(books, *book)
	}
	return service, *topic, books
}
func TestClipTopicStableMembershipCASAndBookmarkDeletion(t *testing.T) {
	service, topic, books := clipTopicFixture(t)
	ctx := context.Background()
	ids := []uint{books[0].ID, books[1].ID, books[2].ID}
	added, err := service.EditClipTopic(ctx, ClipTopicEdit{TopicID: topic.Topic.ID, ExpectedRevision: topic.Topic.Revision, Action: "add", BookmarkIDs: ids})
	if err != nil || added.MemberCount != 3 {
		t.Fatalf("%+v %v", added, err)
	}
	duplicate, err := service.EditClipTopic(ctx, ClipTopicEdit{TopicID: topic.Topic.ID, ExpectedRevision: added.Topic.Revision, Action: "add", BookmarkIDs: []uint{books[0].ID}})
	if err != nil || duplicate.MemberCount != 3 {
		t.Fatalf("%+v %v", duplicate, err)
	}
	page, err := service.ListClipTopicMembers(ctx, ClipTopicMembersQuery{TopicID: topic.Topic.ID, Limit: 1})
	if err != nil || page.Items[0].Bookmark.ID != ids[0] || page.CursorBookmarkID == 0 {
		t.Fatalf("%+v %v", page, err)
	}
	moved, err := service.EditClipTopic(ctx, ClipTopicEdit{TopicID: topic.Topic.ID, ExpectedRevision: duplicate.Topic.Revision, Action: "move", BookmarkIDs: []uint{books[2].ID}, BeforeBookmarkID: books[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListClipTopicMembers(ctx, ClipTopicMembersQuery{TopicID: topic.Topic.ID, Revision: page.Topic.Topic.Revision, CursorPosition: page.CursorPosition, CursorBookmarkID: page.CursorBookmarkID}); !errors.Is(err, ErrClipTopicChanged) {
		t.Fatalf("mixed revision page: %v", err)
	}
	current, err := service.ListClipTopicMembers(ctx, ClipTopicMembersQuery{TopicID: topic.Topic.ID})
	if err != nil || current.Items[0].Bookmark.ID != ids[2] || current.Items[1].Bookmark.ID != ids[0] {
		t.Fatalf("move %+v %v", current, err)
	}
	other, err := service.SaveClipTopic(ctx, ClipTopicInput{Name: "另一个专题"})
	if err != nil {
		t.Fatal(err)
	}
	other, err = service.EditClipTopic(ctx, ClipTopicEdit{TopicID: other.Topic.ID, ExpectedRevision: other.Topic.Revision, Action: "add", BookmarkIDs: []uint{books[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	var currentBook models.VideoBookmark
	if err := database.DB.First(&currentBook, books[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	refs, err := service.BookmarkTopicReferences(ctx, books[0].ID, currentBook.Revision)
	if err != nil || refs.TopicCount != 2 {
		t.Fatalf("%+v %v", refs, err)
	}
	if err := service.Delete(ctx, books[0].ID, currentBook.Revision); err != nil {
		t.Fatal(err)
	}
	for id, revision := range map[uint]uint64{topic.Topic.ID: moved.Topic.Revision, other.Topic.ID: other.Topic.Revision} {
		var after models.ClipTopic
		if err := database.DB.First(&after, id).Error; err != nil || after.Revision != revision+1 {
			t.Fatalf("delete did not invalidate topic %+v %v", after, err)
		}
	}
	mustNoteCount(t, &models.VideoBookmark{}, 2)
	mustNoteCount(t, &models.Video{}, 1)
	if err := service.DeleteClipTopic(ctx, other.Topic.ID, other.Topic.Revision+1); err != nil {
		t.Fatal(err)
	}
	mustNoteCount(t, &models.VideoBookmark{}, 2)
}
func TestClipTopicAtomicCreateBookmarkAndInvalidBatch(t *testing.T) {
	service, topic, books := clipTopicFixture(t)
	ctx := context.Background()
	input := ClipBookmarkInput{TopicID: topic.Topic.ID, ExpectedTopicRevision: 99, Bookmark: BookmarkInput{VideoID: books[0].VideoID, Title: "new", StartMS: 0, SourceToken: "wrong"}}
	if _, err := service.SaveClipBookmark(ctx, input); err == nil {
		t.Fatal("accepted stale topic")
	}
	mustNoteCount(t, &models.VideoBookmark{}, 3)
	input.ExpectedTopicRevision = topic.Topic.Revision
	if _, err := service.SaveClipBookmark(ctx, input); !errors.Is(err, ErrBookmarkSourceChanged) {
		t.Fatal(err)
	}
	mustNoteCount(t, &models.VideoBookmark{}, 3)
	var unchanged models.ClipTopic
	if err := database.DB.First(&unchanged, topic.Topic.ID).Error; err != nil || unchanged.Revision != topic.Topic.Revision {
		t.Fatalf("partial root change %+v %v", unchanged, err)
	}
	session, err := (&VideoService{}).GetPreviewSession(books[0].VideoID)
	if err != nil {
		t.Fatal(err)
	}
	input.Bookmark.SourceToken = session.SourceVersion
	saved, err := service.SaveClipBookmark(ctx, input)
	if err != nil || saved.Topic.MemberCount != 1 || saved.Bookmark.EndMS != nil {
		t.Fatalf("point bookmark cannot be organized: %+v %v", saved, err)
	}
	before := saved.Topic.Topic.Revision
	if _, err := service.EditClipTopic(ctx, ClipTopicEdit{TopicID: topic.Topic.ID, ExpectedRevision: before, Action: "add", BookmarkIDs: []uint{books[0].ID, 999999}}); err == nil {
		t.Fatal("partial invalid batch accepted")
	}
	after, err := service.ListClipTopicMembers(ctx, ClipTopicMembersQuery{TopicID: topic.Topic.ID})
	if err != nil || after.Topic.MemberCount != 1 || after.Topic.Topic.Revision != before {
		t.Fatalf("partial batch %+v %v", after, err)
	}
}

func TestClipTopicRelationChangeInvalidatesOldBookmarkDeleteConfirmation(t *testing.T) {
	service, first, books := clipTopicFixture(t)
	ctx := context.Background()
	refs, err := service.BookmarkTopicReferences(ctx, books[0].ID, books[0].Revision)
	if err != nil || refs.TopicCount != 0 {
		t.Fatalf("%+v %v", refs, err)
	}
	if _, err := service.EditClipTopic(ctx, ClipTopicEdit{TopicID: first.Topic.ID, ExpectedRevision: first.Topic.Revision, Action: "add", BookmarkIDs: []uint{books[0].ID}}); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, refs.BookmarkID, refs.Revision); !errors.Is(err, ErrViewingNoteConflict) {
		t.Fatalf("stale confirmation deleted a new relation: %v", err)
	}
	mustNoteCount(t, &models.VideoBookmark{}, 3)
	mustNoteCount(t, &models.ClipTopicMember{}, 1)
	var current models.VideoBookmark
	if err := database.DB.First(&current, books[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	refreshed, err := service.BookmarkTopicReferences(ctx, current.ID, current.Revision)
	if err != nil || refreshed.TopicCount != 1 {
		t.Fatalf("%+v %v", refreshed, err)
	}
	if err := service.Delete(ctx, refreshed.BookmarkID, refreshed.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestClipTopicNewestPaginationDoesNotRepeatAfterInsert(t *testing.T) {
	service, first, _ := clipTopicFixture(t)
	ctx := context.Background()
	second, err := service.SaveClipTopic(ctx, ClipTopicInput{Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListClipTopics(ctx, ClipTopicQuery{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Topic.ID != second.Topic.ID || page.NextCursor != second.Topic.ID {
		t.Fatalf("first page %+v %v", page, err)
	}
	if _, err := service.SaveClipTopic(ctx, ClipTopicInput{Name: "new during pagination"}); err != nil {
		t.Fatal(err)
	}
	next, err := service.ListClipTopics(ctx, ClipTopicQuery{Limit: 1, CursorID: page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].Topic.ID != first.Topic.ID || next.NextCursor != 0 {
		t.Fatalf("second page %+v %v", next, err)
	}
}
