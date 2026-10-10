package main

import (
	"context"
	"fmt"
	"time"
	"video-master/services"
)

func (a *App) viewingNotesContext() (context.Context, context.CancelFunc, error) {
	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, nil, fmt.Errorf("%s", reason)
	}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 30*time.Second)
	return ctx, cancel, nil
}
func (a *App) ListVideoBookmarks(q services.BookmarkQuery) (*services.BookmarkPage, error) {
	ctx, cancel, err := a.viewingNotesContext()
	if err != nil {
		return nil, err
	}
	defer cancel()
	return (&services.BookmarkService{}).List(ctx, q)
}
func (a *App) SaveVideoBookmark(input services.BookmarkInput) (*services.BookmarkDTO, error) {
	ctx, cancel, err := a.viewingNotesContext()
	if err != nil {
		return nil, err
	}
	defer cancel()
	return (&services.BookmarkService{}).Save(ctx, input)
}
func (a *App) DeleteVideoBookmark(id uint, revision uint64) error {
	ctx, cancel, err := a.viewingNotesContext()
	if err != nil {
		return err
	}
	defer cancel()
	return (&services.BookmarkService{}).Delete(ctx, id, revision)
}
func (a *App) ResolveVideoBookmark(id uint) (*services.BookmarkResolution, error) {
	ctx, cancel, err := a.viewingNotesContext()
	if err != nil {
		return nil, err
	}
	defer cancel()
	return (&services.BookmarkService{}).Resolve(ctx, id)
}
func (a *App) ListViewingDiary(q services.DiaryQuery) (*services.DiaryPage, error) {
	ctx, cancel, err := a.viewingNotesContext()
	if err != nil {
		return nil, err
	}
	defer cancel()
	return (&services.ViewingDiaryService{}).List(ctx, q)
}
func (a *App) SaveViewingDiary(input services.DiaryInput) (*services.DiaryEntryDTO, error) {
	ctx, cancel, err := a.viewingNotesContext()
	if err != nil {
		return nil, err
	}
	defer cancel()
	return (&services.ViewingDiaryService{}).Save(ctx, input)
}
func (a *App) DeleteViewingDiary(id uint, revision uint64) error {
	ctx, cancel, err := a.viewingNotesContext()
	if err != nil {
		return err
	}
	defer cancel()
	return (&services.ViewingDiaryService{}).Delete(ctx, id, revision)
}
func (a *App) GetViewingYearReview(year int) (*services.ViewingYearReview, error) {
	ctx, cancel, err := a.viewingNotesContext()
	if err != nil {
		return nil, err
	}
	defer cancel()
	return (&services.ViewingDiaryService{}).YearReview(ctx, year)
}
func (a *App) ListUnconfirmedPlaybackHistory(q services.HistoryQuery) (*services.PlaybackHistoryPage, error) {
	ctx, cancel, err := a.viewingNotesContext()
	if err != nil {
		return nil, err
	}
	defer cancel()
	return (&services.ViewingDiaryService{}).History(ctx, q)
}
