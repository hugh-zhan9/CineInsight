package main

import (
	"testing"
	"time"
	"video-master/database"
	"video-master/services"
)

func TestViewingNotesBindingsAndMaintenance(t *testing.T) {
	setupAppTestDB(t)
	a := &App{}
	row, err := a.SaveViewingDiary(services.DiaryInput{Title: "手填片名", WatchedOn: time.Now().Format(time.DateOnly)})
	if err != nil {
		t.Fatal(err)
	}
	page, err := a.ListViewingDiary(services.DiaryQuery{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("page %+v %v", page, err)
	}
	summary, err := a.GetViewingYearReview(time.Now().Year())
	if err != nil || summary.Total != 1 {
		t.Fatalf("summary %+v %v", summary, err)
	}
	release := database.BeginMaintenance()
	calls := []func() error{
		func() error { _, err := a.ListVideoBookmarks(services.BookmarkQuery{}); return err },
		func() error { _, err := a.SaveVideoBookmark(services.BookmarkInput{}); return err },
		func() error { return a.DeleteVideoBookmark(1, 1) },
		func() error { _, err := a.ResolveVideoBookmark(1); return err },
		func() error { _, err := a.ListViewingDiary(services.DiaryQuery{}); return err },
		func() error { _, err := a.SaveViewingDiary(services.DiaryInput{}); return err },
		func() error { return a.DeleteViewingDiary(row.ID, row.Revision) },
		func() error { _, err := a.GetViewingYearReview(2024); return err },
		func() error { _, err := a.ListUnconfirmedPlaybackHistory(services.HistoryQuery{}); return err },
	}
	for i, call := range calls {
		if err := call(); err == nil {
			release()
			t.Fatalf("binding %d bypassed maintenance", i)
		}
	}
	release()
	if err := a.DeleteViewingDiary(row.ID, row.Revision); err != nil {
		t.Fatal(err)
	}
}
