package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestDialogueGroupsPageBeforeHitsAndBindCursor(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	a := seedSceneVideo(t, "many.mp4", []byte("many"), 7200)
	b := seedSceneVideo(t, "other.mp4", []byte("other"), 7200)
	var text strings.Builder
	for i := 0; i < 2200; i++ {
		fmt.Fprintf(&text, "%d\n%s --> %s\n普通台词 %d\n\n", i+1, formatSRTTimestamp(int64(i)*1000), formatSRTTimestamp(int64(i)*1000+500), i)
	}
	if err := os.WriteFile(strings.TrimSuffix(a.Path, filepath.Ext(a.Path))+".srt", []byte(text.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureSubtitleIndexForVideo(a); err != nil {
		t.Fatal(err)
	}
	writeSceneSRT(t, b, nil)
	s := newTestSceneSearch(nil, false)
	ctx := context.Background()
	first, err := s.SearchDialogueVideoGroups(ctx, DialogueSearchRequest{Query: "普通台词", Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].VideoID != a.ID || first.Items[0].HitCount != 2200 || len(first.Items[0].Hits) != 3 || first.NextCursor == "" {
		t.Fatalf("groups %+v %v", first, err)
	}
	second, err := s.SearchDialogueVideoGroups(ctx, DialogueSearchRequest{Query: "普通台词", Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.Items[0].VideoID != b.ID || second.NextCursor != "" {
		t.Fatalf("other video crowded out %+v %v", second, err)
	}
	g := first.Items[0]
	page, err := s.ListDialogueVideoHits(ctx, DialogueSearchRequest{Query: "普通台词", VideoID: a.ID, Limit: 1, Cursor: g.NextCursor})
	if err != nil || len(page.Items) != 1 || page.Items[0].StartMS <= g.Hits[2].StartMS || page.Items[0].SourceVersion == "" {
		t.Fatalf("hit page %+v %v", page, err)
	}
	for _, request := range []DialogueSearchRequest{
		{Query: "changed", Cursor: first.NextCursor}, {Query: "普通台词", Cursor: first.NextCursor + "x"}, {Query: "普通台词", VideoID: b.ID, Cursor: g.NextCursor},
	} {
		if request.VideoID == 0 {
			_, err = s.SearchDialogueVideoGroups(ctx, request)
		} else {
			_, err = s.ListDialogueVideoHits(ctx, request)
		}
		if !errors.Is(err, ErrSearchCursorInvalid) {
			t.Fatalf("wrong cursor accepted: %v", err)
		}
	}
	// Data changes between pages do not reset the cursor or repeat prior IDs.
	if err := database.DB.Model(&models.SubtitleSegment{}).Where("video_id = ?", b.ID).Update("text", "gone").Error; err != nil {
		t.Fatal(err)
	}
	empty, err := s.SearchDialogueVideoGroups(ctx, DialogueSearchRequest{Query: "普通台词", Cursor: first.NextCursor})
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("page change %+v %v", empty, err)
	}
}
func TestDialogueLiteralQueryRangesAndUnavailableSource(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	video := seedSceneVideo(t, "literal.mp4", []byte("source"), 7200)
	writeSceneSRT(t, video, map[int]string{0: `100%_答案\在这里`, 1: "100xx答案在这里"})
	service := newTestSceneSearch(nil, false)
	page, err := service.SearchDialogueVideoGroups(context.Background(), DialogueSearchRequest{Query: `100%_答案\在`})
	if err != nil || len(page.Items) != 1 || page.Items[0].HitCount != 1 {
		t.Fatalf("literal %+v %v", page, err)
	}
	hit := page.Items[0].Hits[0]
	if hit.PreviewStartMS != 0 || hit.PreviewEndMS != 5500 || hit.SourceVersion == "" {
		t.Fatalf("context %+v", hit)
	}
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	again, err := service.ListDialogueVideoHits(context.Background(), DialogueSearchRequest{Query: `100%_答案\在`, VideoID: video.ID})
	if err != nil || len(again.Items) != 1 || again.Items[0].SourceVersion != "" {
		t.Fatalf("unavailable %+v %v", again, err)
	}
}
func TestSearchClipsFreezeSourcesWithoutCreatingBookmarks(t *testing.T) {
	queue, _, books := clipQueueFixture(t)
	ctx := context.Background()
	state := queueSnapshot(t, queue)
	session, err := (&VideoService{}).GetPreviewSession(books[0].VideoID)
	if err != nil {
		t.Fatal(err)
	}
	input := SearchClipRequest{QueueRevision: state.State.Revision, Items: []SearchClipInput{{VideoID: books[0].VideoID, Title: "search result", StartMS: 1000, EndMS: 3000, SourceVersion: session.SourceVersion}}}
	before := int64(0)
	database.DB.Model(&models.VideoBookmark{}).Count(&before)
	preview, err := queue.PreviewSearchClips(ctx, input)
	if err != nil || preview.PreviewToken == "" {
		t.Fatalf("preview %+v %v", preview, err)
	}
	var video models.Video
	database.DB.First(&video, books[0].VideoID)
	if err := os.WriteFile(video.Path, []byte(strings.Repeat("changed", 20)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.AppendClipQueue(ctx, AppendClipQueueRequest{PreviewToken: preview.PreviewToken}); err == nil {
		t.Fatal("changed source was queued")
	}
	conflict, err := queue.PreviewSearchClips(ctx, input)
	if err != nil || conflict.PreviewToken != "" || conflict.Conflicts != 1 {
		t.Fatalf("old hit accepted %+v %v", conflict, err)
	}
	mustNoteCount(t, &models.VideoBookmark{}, before)
}

func TestSearchClipAppendIsNeutralAndLeavesNoTemporaryBookmarks(t *testing.T) {
	queue, _, books := clipQueueFixture(t)
	ctx := context.Background()
	state := queueSnapshot(t, queue)
	session, err := (&VideoService{}).GetPreviewSession(books[0].VideoID)
	if err != nil {
		t.Fatal(err)
	}
	input := SearchClipRequest{QueueRevision: state.State.Revision, Items: []SearchClipInput{{VideoID: books[0].VideoID, Title: "temporary", StartMS: 1000, EndMS: 3000, SourceVersion: session.SourceVersion}}}
	preview, err := queue.PreviewSearchClips(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := queue.AppendClipQueue(ctx, AppendClipQueueRequest{PreviewToken: preview.PreviewToken})
	if err != nil || !result.PlayRequested {
		t.Fatalf("%+v %v", result, err)
	}
	current := waitQueueSnapshot(t, queue, func(snapshot *QueueSnapshot) bool { return snapshot.Session != nil })
	sendClipEvents(t, queue, current.Session, 3)
	repeat, err := queue.AppendClipQueue(ctx, AppendClipQueueRequest{PreviewToken: preview.PreviewToken})
	if err != nil || repeat.BatchID != result.BatchID {
		t.Fatalf("duplicate %+v %v", repeat, err)
	}
	var entry models.PlaybackQueueEntry
	if err := database.DB.First(&entry, result.FirstEntryID).Error; err != nil {
		t.Fatal(err)
	}
	if entry.OriginBookmarkID != nil {
		t.Fatal("temporary search created a bookmark origin")
	}
	mustNoteCount(t, &models.VideoBookmark{}, 2)
	mustNoteCount(t, &models.PlayEvent{}, 0)
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 0)
	var video models.Video
	database.DB.First(&video, books[0].VideoID)
	if video.PlayCount != 0 || video.IsWatched || video.WatchPositionSeconds != 0 || video.RandomPlayCount != 0 {
		t.Fatalf("polluted viewing facts %+v", video)
	}
}

type changingSceneVectors struct {
	*fakeSceneVectors
	before func()
}

func (s *changingSceneVectors) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	s.before()
	return s.fakeSceneVectors.EmbedTexts(ctx, texts)
}
func TestRankedSearchCapturesIndexSourceBeforeInference(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	query, near, _, _ := sceneTestVectors(93)
	video := seedSceneVideo(t, "scene.mp4", []byte("original"), 60)
	seedSceneIndex(t, video, SceneLocalModelID, 5000, []sceneSegmentFixture{{StartMS: 1000, EndMS: 5000, Vector: near}})
	vectors := &changingSceneVectors{fakeSceneVectors: &fakeSceneVectors{texts: map[string][]float32{"search": query}}, before: func() {
		if err := os.WriteFile(video.Path, []byte("replaced"), 0600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(video.Path)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Model(&models.SceneIndexState{}).Where("video_id = ?", video.ID).Update("source_mtime_ns", info.ModTime().UnixNano()).Error; err != nil {
			t.Fatal(err)
		}
	}}
	result, err := newTestSceneSearch(vectors, true).SearchVisualResults(context.Background(), SceneSearchRequest{Query: "search"})
	if err != nil || len(result.Items) != 1 || result.Extent != "ranked_top_k" {
		t.Fatalf("result %+v %v", result, err)
	}
	if result.Items[0].SourceVersion != "" {
		t.Fatal("post-query index fingerprint authorized an older result")
	}
}
func TestDialogueCursorRejectsExpiredOrNewDatabase(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	video := seedSceneVideo(t, "cursors.mp4", []byte("source"), 7200)
	writeSceneSRT(t, video, nil)
	service := newTestSceneSearch(nil, false)
	request, _ := normalizeDialogueRequest(DialogueSearchRequest{Query: "普通台词", VideoID: video.ID}, false)
	value := dialogueCursor{Binding: dialogueBinding(request, false), VideoID: video.ID, SegmentID: 1, Expires: 1}
	request.Cursor = service.signDialogueCursor(value)
	if _, err := service.ListDialogueVideoHits(context.Background(), request); !errors.Is(err, ErrSearchCursorInvalid) {
		t.Fatalf("expired cursor %v", err)
	}
	first, err := service.SearchDialogueVideoGroups(context.Background(), DialogueSearchRequest{Query: "普通台词"})
	if err != nil {
		t.Fatal(err)
	}
	request.Cursor = first.Items[0].NextCursor
	previous := database.DB
	database.DB = database.DB.Session(&gorm.Session{})
	defer func() { database.DB = previous }()
	if _, err := service.ListDialogueVideoHits(context.Background(), request); !errors.Is(err, ErrSearchCursorInvalid) {
		t.Fatalf("old database cursor %v", err)
	}
}
func TestSearchResultQueryTextIsNotLogged(t *testing.T) {
	setupSceneDB(t, SceneProviderExternal)
	video := seedSceneVideo(t, "private.mp4", []byte("source"), 7200)
	writeSceneSRT(t, video, map[int]string{0: "独有检索正文"})
	seedSceneIndex(t, video, sceneExternalModelID("privacy-model"), 5000, []sceneSegmentFixture{{StartMS: 0, EndMS: 5000, Caption: "独有检索正文"}})
	requests := configureSceneExternalEndpoint(t, "privacy-model")
	if err := database.DB.Model(&video).Update("display_title", "独有筛选正文").Error; err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSuffix(video.Path, filepath.Ext(video.Path)) + ".srt"
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(content), "独有检索正文", "独有检索正文 独有字幕重建正文")), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	previous := database.DB
	database.DB = database.DB.Session(&gorm.Session{Logger: logger.New(log.New(&output, "", 0), logger.Config{LogLevel: logger.Info})})
	defer func() { database.DB = previous }()
	service := newTestSceneSearch(nil, false)
	if _, err := service.SearchDialogueVideoGroups(context.Background(), DialogueSearchRequest{Query: "独有检索正文"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SearchVisualResults(context.Background(), SceneSearchRequest{Query: "独有检索正文", Filter: &LibraryFilter{Keyword: "独有筛选正文"}}); err != nil {
		t.Fatal(err)
	}
	if requests() != 0 {
		t.Fatal("local caption search contacted external provider")
	}
	if _, err := (&PersonService{}).ListPeople("独有人物正文", "", 0, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := (&CollectionService{}).ListCollections("独有作品正文", "", 0, 20); err != nil {
		t.Fatal(err)
	}
	for _, sortMode := range []string{LibrarySortBalanced, LibrarySortAddedDesc, LibrarySortRatingDesc} {
		if _, err := (&VideoService{}).SearchLibraryVideoPage(LibraryFilter{Keyword: "独有视频正文", SortMode: sortMode}, nil, 20); err != nil {
			t.Fatal(err)
		}
	}
	for _, secret := range []string{"独有检索正文", "独有筛选正文", "独有字幕重建正文", "独有人物正文", "独有作品正文", "独有视频正文"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("query or subtitle content reached SQL log: %s", secret)
		}
	}
}

func TestDialogueRefreshDoesNotReturnUnvalidatedReplacementCandidates(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	for i := 0; i < 4; i++ {
		video := seedSceneVideo(t, fmt.Sprintf("stale-%d.mp4", i), []byte("source"), 7200)
		writeSceneSRT(t, video, nil)
		if i < 3 {
			if err := os.Remove(strings.TrimSuffix(video.Path, filepath.Ext(video.Path)) + ".srt"); err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := newTestSceneSearch(nil, false).SearchDialogueVideoGroups(context.Background(), DialogueSearchRequest{Query: "普通台词", Limit: 1})
	if err != nil || len(result.Items) != 1 || result.Items[0].Title != "stale-3.mp4" {
		t.Fatalf("stale replacement candidate %+v %v", result, err)
	}
}
