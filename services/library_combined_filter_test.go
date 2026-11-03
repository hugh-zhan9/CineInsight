package services

import (
	"fmt"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestLibraryCombinedConditionsAndSavedView(t *testing.T) {
	setupVideoServiceTestDB(t)
	for i, input := range []models.Video{
		{Duration: 7200, IsFavorite: true}, {Duration: 7201, IsFavorite: true},
		{Duration: 0, IsFavorite: true}, {Duration: 3600, IsFavorite: true, IsWatched: true}, {Duration: 3600},
	} {
		input.Name, input.Path = fmt.Sprint(i), fmt.Sprintf("/combined/%d.mp4", i)
		if err := database.DB.Create(&input).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &VideoService{}
	filter := LibraryFilter{WatchState: "unwatched", FavoritesOnly: true, MinDurationSeconds: 3600, MaxDurationSeconds: 7200}
	page, err := svc.SearchLibraryVideoPage(filter, nil, 50)
	if err != nil || len(page.Videos) != 1 || page.Videos[0].Name != "0" {
		t.Fatalf("combined page=%+v err=%v", page, err)
	}
	count, err := svc.CountLibraryVideos(filter)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	view, err := svc.SaveLibraryView(SavedLibraryViewInput{Name: "combination", LibraryFilter: filter})
	if err != nil {
		t.Fatal(err)
	}
	var stored models.SavedLibraryView
	if err := database.DB.First(&stored, view.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.WatchState != "unwatched" || !stored.FavoritesOnly || stored.MinDurationSeconds != 3600 || stored.MaxDurationSeconds != 7200 {
		t.Fatalf("lost saved conditions: %+v", stored)
	}
	if _, err := svc.UpdateSavedLibraryView(view.ID, "reset", LibraryFilter{}); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.First(&stored, view.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.WatchState != "any" || stored.FavoritesOnly || stored.MaxDurationSeconds != 0 {
		t.Fatalf("failed to clear conditions: %+v", stored)
	}
}

func TestLibraryCombinedConditionValidationAndRandomIntersection(t *testing.T) {
	for _, filter := range []LibraryFilter{{WatchState: "other"}, {WatchState: "unwatched", SmartView: LibraryViewWatched}, {MinDurationSeconds: -1}, {MaxDurationSeconds: 86401}, {MinDurationSeconds: 20, MaxDurationSeconds: 10}} {
		if _, err := normalizeLibraryFilter(filter); err == nil {
			t.Fatalf("accepted %+v", filter)
		}
	}
	request, err := normalizeRandomPlayRequest(RandomPlayRequest{Filter: LibraryFilter{WatchState: "unwatched", FavoritesOnly: true, MaxDurationSeconds: 7200}, MaxDurationSeconds: 1800})
	if err != nil || request.WatchState != "unwatched" || !request.FavoritesOnly || request.MaxDurationSeconds != 1800 {
		t.Fatalf("%+v %v", request, err)
	}
	if _, err := normalizeRandomPlayRequest(RandomPlayRequest{WatchState: "watched", Filter: LibraryFilter{WatchState: "unwatched"}}); err == nil {
		t.Fatal("contradictory explicit states accepted")
	}
}
