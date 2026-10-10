package services

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func notesFixture(t *testing.T) (models.Video, string) {
	t.Helper()
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	video := createShortFeedVideo(t, root, "记忆.mp4", 120, false)
	session, err := (&VideoService{}).GetPreviewSession(video.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.SourceVersion) != 64 {
		t.Fatalf("missing source token: %+v", session)
	}
	return video, session.SourceVersion
}
func mustNoteCount(t *testing.T, model any, want int64) {
	t.Helper()
	var count int64
	if err := database.DB.Model(model).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%T count=%d want=%d", model, count, want)
	}
}

func TestBookmarkSourceIdentityCASAndDeletion(t *testing.T) {
	video, token := notesFixture(t)
	ctx := context.Background()
	svc := &BookmarkService{}
	end := int64(4000)
	input := BookmarkInput{VideoID: video.ID, SourceToken: token, StartMS: 0, EndMS: &end, Title: "起点", Note: "雨与海", Tags: []string{" 风景 ", "风景", "%_"}}
	row, err := svc.Save(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(row.Tags) != 2 || row.StartMS != 0 || row.Revision != 1 {
		t.Fatalf("bad create: %+v", row)
	}
	input.ID = row.ID
	input.Revision = row.Revision
	input.SourceToken = ""
	input.Note = "补注"
	edited, err := svc.Save(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Revision != 2 {
		t.Fatal(edited.Revision)
	}
	if _, err := svc.Save(ctx, input); !errors.Is(err, ErrViewingNoteConflict) {
		t.Fatalf("stale save: %v", err)
	}
	if err := svc.Delete(ctx, row.ID, 1); !errors.Is(err, ErrViewingNoteConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	newPath := filepath.Join(video.Directory, "renamed.mp4")
	if err := os.Rename(video.Path, newPath); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&video).Update("path", newPath).Error; err != nil {
		t.Fatal(err)
	}
	resolved, err := svc.Resolve(ctx, row.ID)
	if err != nil || resolved.Status != "ready" {
		t.Fatalf("rename broke position: %+v %v", resolved, err)
	}
	if err := os.WriteFile(newPath, []byte("new different content with new size"), 0600); err != nil {
		t.Fatal(err)
	}
	resolved, err = svc.Resolve(ctx, row.ID)
	if err != nil || resolved.Status != "source_changed" {
		t.Fatalf("replacement not detected: %+v %v", resolved, err)
	}
	input.ID = 0
	input.Revision = 0
	input.SourceToken = token
	if _, err := svc.Save(ctx, input); !errors.Is(err, ErrBookmarkSourceChanged) {
		t.Fatalf("old editor accepted new source: %v", err)
	}
	input.ID = row.ID
	input.Revision = edited.Revision
	input.SourceToken = ""
	input.Note = "继续编辑文字"
	edited, err = svc.Save(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Resolve(ctx, row.ID)
	if err != nil || again.Status != "source_changed" {
		t.Fatalf("text edit accepted source: %+v %v", again, err)
	}
	input.Revision = edited.Revision
	input.AcceptSourceToken = token
	if _, err := svc.Save(ctx, input); !errors.Is(err, ErrBookmarkSourceChanged) {
		t.Fatal(err)
	}
	input.AcceptSourceToken = resolved.SourceToken
	edited, err = svc.Save(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := svc.Resolve(ctx, row.ID)
	if err != nil || ready.Status != "ready" {
		t.Fatalf("reconfirm failed %+v %v", ready, err)
	}
	if err := database.DB.Unscoped().Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	unavailable, err := svc.Resolve(ctx, row.ID)
	if err != nil || unavailable.Status != "unavailable" || unavailable.Bookmark.Note != "继续编辑文字" {
		t.Fatalf("lost snapshot %+v %v", unavailable, err)
	}
	input.Revision = edited.Revision
	input.AcceptSourceToken = ""
	input.Note = "原片已删除"
	last, err := svc.Save(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, last.ID, last.Revision); err != nil {
		t.Fatal(err)
	}
	mustNoteCount(t, &models.VideoBookmark{}, 0)
	bytes, err := os.ReadFile(newPath)
	if err != nil || string(bytes) != "new different content with new size" {
		t.Fatalf("notes changed media %q %v", bytes, err)
	}
}

func TestBookmarkValidationSearchAndDuration(t *testing.T) {
	video, token := notesFixture(t)
	ctx := context.Background()
	svc := &BookmarkService{}
	base := BookmarkInput{VideoID: video.ID, SourceToken: token, Title: "原点", Tags: []string{`a"b`}}
	for _, change := range []func(*BookmarkInput){func(i *BookmarkInput) { i.StartMS = -1 }, func(i *BookmarkInput) { i.StartMS = 9007199254740992 }, func(i *BookmarkInput) { end := int64(0); i.EndMS = &end }, func(i *BookmarkInput) { i.Title = strings.Repeat("海", 201) }, func(i *BookmarkInput) { i.Tags = []string{strings.Repeat("海", 41)} }, func(i *BookmarkInput) { i.SourceToken = "" }} {
		in := base
		change(&in)
		if _, err := svc.Save(ctx, in); !errors.Is(err, ErrViewingNoteInvalid) {
			t.Fatalf("accepted invalid %+v: %v", in, err)
		}
	}
	another := createShortFeedVideo(t, video.Directory, "other.mp4", 120, false)
	wrong := base
	wrong.VideoID = another.ID
	if _, err := svc.Save(ctx, wrong); !errors.Is(err, ErrBookmarkSourceChanged) {
		t.Fatalf("cross-video token: %v", err)
	}
	info, _ := os.Stat(video.Path)
	size, mtime, now := info.Size(), info.ModTime().UnixNano(), time.Now()
	if err := database.DB.Create(&models.VideoTechnicalMetadata{VideoID: video.ID, SuccessfulSourceSize: &size, SuccessfulSourceModTimeNS: &mtime, ProbedAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	invalid := base
	invalid.StartMS = 120000
	if _, err := svc.Save(ctx, invalid); !errors.Is(err, ErrViewingNoteInvalid) {
		t.Fatalf("duration boundary: %v", err)
	}
	for _, title := range []string{"海Ⅰ", "海Ⅱ", "海Ⅲ"} {
		in := base
		in.Title = title
		if _, err := svc.Save(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.List(ctx, BookmarkQuery{Keyword: "海", Limit: 2})
	if err != nil || len(page.Items) != 2 || !page.HasMore {
		t.Fatalf("page %+v %v", page, err)
	}
	next, err := svc.List(ctx, BookmarkQuery{Keyword: "海", Limit: 2, CursorID: page.CursorID})
	if err != nil || len(next.Items) != 1 || next.HasMore || next.Items[0].ID >= page.CursorID {
		t.Fatalf("tail %+v %v", next, err)
	}
	literal, err := svc.List(ctx, BookmarkQuery{Keyword: `a"b`})
	if err != nil || len(literal.Items) != 3 {
		t.Fatalf("decoded tag search %+v %v", literal, err)
	}
	empty, err := svc.List(ctx, BookmarkQuery{Keyword: `\"`})
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("matched JSON serialization %+v %v", empty, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.List(cancelled, BookmarkQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestViewingDiaryManualEntriesDatesAndRecap(t *testing.T) {
	video, _ := notesFixture(t)
	ctx := context.Background()
	svc := &ViewingDiaryService{}
	rating := 8.5
	in := DiaryInput{Title: "片库外的电影", WatchedOn: "2024-02-29", Rating: &rating, Note: "%_é 海"}
	first, err := svc.Save(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Save(ctx, in); err != nil {
		t.Fatal(err)
	} // manual copies never silently merge
	in.VideoID = &video.ID
	in.Title = "ignored"
	in.Rating = nil
	in.WatchedOn = "2024-12-31"
	bound, err := svc.Save(ctx, in)
	if err != nil || bound.Title != video.Name || bound.Rating != nil {
		t.Fatalf("bound %+v %v", bound, err)
	}
	if _, err := svc.Save(ctx, DiaryInput{Title: "新年", WatchedOn: "2025-01-01"}); err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"2023-02-29", "0000-01-01", "2024-2-29", "2024-04-31"} {
		if _, err := svc.Save(ctx, DiaryInput{Title: "x", WatchedOn: date}); !errors.Is(err, ErrViewingNoteInvalid) {
			t.Fatalf("date %s %v", date, err)
		}
	}
	for _, r := range []float64{-1, 10.5, 8.3, math.NaN(), math.Inf(1)} {
		if _, err := svc.Save(ctx, DiaryInput{Title: "x", WatchedOn: "2024-02-29", Rating: &r}); !errors.Is(err, ErrViewingNoteInvalid) {
			t.Fatalf("rating %f %v", r, err)
		}
	}
	page, err := svc.List(ctx, DiaryQuery{Year: 2024, Limit: 1})
	if err != nil || len(page.Items) != 1 || !page.HasMore || page.Items[0].ID != bound.ID {
		t.Fatalf("page %+v %v", page, err)
	}
	next, err := svc.List(ctx, DiaryQuery{Year: 2024, Limit: 2, CursorID: page.CursorID, CursorDate: page.CursorDate})
	if err != nil || len(next.Items) != 2 || next.HasMore {
		t.Fatalf("tail %+v %v", next, err)
	}
	literal, err := svc.List(ctx, DiaryQuery{Year: 2024, Keyword: "%_É"})
	if err != nil || len(literal.Items) != 3 {
		t.Fatalf("keyword %+v %v", literal, err)
	}
	recap, err := svc.YearReview(ctx, 2024)
	if err != nil || recap.Total != 3 || recap.Manual != 3 || recap.Days != 2 || recap.Months[1] != 2 || recap.Months[11] != 1 || recap.RatedCount != 2 || *recap.AverageRating != 8.5 || recap.MostWatched[0].Count != 2 {
		t.Fatalf("recap %+v %v", recap, err)
	}
	empty, err := svc.YearReview(ctx, 2023)
	if err != nil || empty.Total != 0 || empty.AverageRating != nil {
		t.Fatalf("empty %+v %v", empty, err)
	}
	edit := DiaryInput{ID: first.ID, Revision: first.Revision, Title: first.Title, WatchedOn: first.WatchedOn, Note: "补记"}
	updated, err := svc.Save(ctx, edit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Save(ctx, edit); !errors.Is(err, ErrViewingNoteConflict) {
		t.Fatalf("CAS: %v", err)
	}
	if err := svc.Delete(ctx, updated.ID, updated.Revision); err != nil {
		t.Fatal(err)
	}
	var count int64
	database.DB.Unscoped().Model(&models.ViewingDiaryEntry{}).Where("id = ?", updated.ID).Count(&count)
	if count != 0 {
		t.Fatal("manual deletion retained unnecessary tombstone")
	}
	var current models.Video
	database.DB.First(&current, video.ID)
	if current.PersonalRating != nil || current.PlayCount != 0 || current.IsWatched {
		t.Fatal("diary changed video facts")
	}
}

func TestViewingDiaryFactAtomicityPersistentDedupAndTombstones(t *testing.T) {
	video, _ := notesFixture(t)
	ctx := context.Background()
	svc := &ViewingDiaryService{}
	dedup := newViewEventDedup(1)
	at := time.Date(2024, 12, 31, 16, 30, 0, 0, time.UTC)
	if recorded, err := dedup.record(video.ID, PlayEventSourceInlineView, "first", at); err != nil || !recorded {
		t.Fatalf("record %v %v", recorded, err)
	}
	if _, err := dedup.record(video.ID, PlayEventSourceInlineView, "second", at); err != nil {
		t.Fatal(err)
	}
	if recorded, err := dedup.record(video.ID, PlayEventSourceInlineView, "first", at); err != nil || recorded {
		t.Fatalf("LRU re-record %v %v", recorded, err)
	}
	var rows []models.ViewingDiaryEntry
	if err := database.DB.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Rating != nil || rows[0].RecordedAt == nil || !rows[0].RecordedAt.Equal(at) || rows[0].WatchedOn != at.In(time.Local).Format(time.DateOnly) {
		t.Fatalf("facts %+v", rows)
	}
	in := DiaryInput{ID: rows[0].ID, Revision: 1, VideoID: &video.ID, Title: "修订片名", WatchedOn: "2025-02-01", Note: "独立的日记"}
	edited, err := svc.Save(ctx, in)
	if err != nil || edited.DateBasis != models.DiaryDateOverride || !edited.RecordedAt.Equal(at) {
		t.Fatalf("edit %+v %v", edited, err)
	}
	if err := svc.Delete(ctx, edited.ID, edited.Revision); err != nil {
		t.Fatal(err)
	}
	var tomb models.ViewingDiaryEntry
	if err := database.DB.Unscoped().First(&tomb, edited.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !tomb.DeletedAt.IsValid() || tomb.Title != "" || tomb.Note != "" || tomb.VideoID != nil || tomb.RecordedAt != nil || tomb.SourceSessionKey == nil || tomb.SourcePlayEventID == nil {
		t.Fatalf("tomb %+v", tomb)
	}
	if recorded, err := newViewEventDedup(1).record(video.ID, PlayEventSourceInlineView, "first", at); err != nil || recorded {
		t.Fatalf("deleted resurrected %v %v", recorded, err)
	}
	history, err := svc.History(ctx, HistoryQuery{})
	if err != nil || len(history.Items) != 0 {
		t.Fatalf("known facts leaked to uncertain %+v %v", history, err)
	}
	if err := database.DB.Unscoped().Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	mustNoteCount(t, &models.PlayEvent{}, 0)
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 1)
	in.ID = rows[1].ID
	in.Revision = 1
	if _, err := svc.Save(ctx, in); err != nil {
		t.Fatalf("cannot edit orphan %v", err)
	}
}

func TestViewingDiaryWriterFailureRollsBackAndMobileLegacyRemainsHistory(t *testing.T) {
	video, _ := notesFixture(t)
	ctx := context.Background()
	dedup := newViewEventDedup(1)
	fail := errors.New("injected diary failure")
	if err := database.DB.Callback().Create().Before("gorm:create").Register("test:fail_diary", func(tx *gorm.DB) {
		if tx.Statement.Table == "viewing_diary_entries" {
			tx.AddError(fail)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Callback().Create().Remove("test:fail_diary") })
	if _, err := dedup.record(video.ID, PlayEventSourceInlineView, "retry", time.Now()); !errors.Is(err, fail) {
		t.Fatal(err)
	}
	mustNoteCount(t, &models.PlayEvent{}, 0)
	mobile := NewShortFeedService(&VideoService{})
	if _, err := mobile.RecordPlaybackSession(videoRef(video.ID), "mobile"); !errors.Is(err, fail) {
		t.Fatal(err)
	}
	var current models.Video
	database.DB.First(&current, video.ID)
	if current.RandomPlayCount != 0 {
		t.Fatal("failed diary changed count")
	}
	mustNoteCount(t, &models.PlayEvent{}, 0)
	database.DB.Callback().Create().Remove("test:fail_diary")
	if recorded, err := dedup.record(video.ID, PlayEventSourceInlineView, "retry", time.Now()); err != nil || !recorded {
		t.Fatalf("retry %v %v", recorded, err)
	}
	for i := 0; i < 3; i++ {
		if _, err := mobile.RecordPlaybackSession(videoRef(video.ID), "mobile"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mobile.RecordPlaybackSession(videoRef(video.ID), "rewatch"); err != nil {
		t.Fatal(err)
	}
	if _, err := mobile.RecordPlayback(videoRef(video.ID)); err != nil {
		t.Fatal(err)
	}
	database.DB.First(&current, video.ID)
	if current.RandomPlayCount != 3 {
		t.Fatalf("duplicate counters %+v", current)
	}
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 3)
	mustNoteCount(t, &models.PlayEvent{}, 4)
	history, err := (&ViewingDiaryService{}).History(ctx, HistoryQuery{Limit: 1})
	if err != nil || len(history.Items) != 1 || history.HasMore || history.Items[0].Source != models.PlayEventSourceMobileFeed || history.Items[0].Title != video.Name {
		t.Fatalf("uncertain history %+v %v", history, err)
	}
	for i := 0; i < 3; i++ {
		event := models.PlayEvent{VideoID: video.ID, PlayedAt: time.Now(), Source: models.PlayEventSourceDesktopPlay}
		if err := database.DB.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
	}
	page, err := (&ViewingDiaryService{}).History(ctx, HistoryQuery{Limit: 2})
	if err != nil || len(page.Items) != 2 || !page.HasMore {
		t.Fatalf("history page %+v %v", page, err)
	}
	next, err := (&ViewingDiaryService{}).History(ctx, HistoryQuery{Limit: 2, CursorID: page.CursorID})
	if err != nil || len(next.Items) != 2 || next.HasMore {
		t.Fatalf("history tail %+v %v", next, err)
	}
}
