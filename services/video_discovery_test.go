package services

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestTonightBudgetScopeAndReadOnly(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	tag := models.Tag{Name: "目标"}
	person := models.Person{DisplayName: "人物"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	var eligible models.Video
	for i, spec := range []struct {
		name                              string
		duration                          float64
		outside, stale, watched, untagged bool
	}{
		{name: "boundary", duration: 5400}, {name: "too-long", duration: 5401}, {name: "unknown"},
		{name: "outside", duration: 20, outside: true}, {name: "stale", duration: 20, stale: true},
		{name: "watched", duration: 20, watched: true}, {name: "untagged", duration: 20, untagged: true},
	} {
		dir := root
		if spec.outside {
			dir = t.TempDir()
		}
		video := models.Video{Name: spec.name, Path: filepath.Join(dir, spec.name+".mp4"), Directory: dir, Duration: spec.duration, IsStale: spec.stale, IsWatched: spec.watched, IsFavorite: true, PlayCount: 4, RandomPlayCount: 2}
		if !spec.untagged {
			video.Tags = []models.Tag{tag}
		}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Create(&models.VideoPerson{VideoID: video.ID, PersonID: person.ID}).Error; err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			eligible = video
		}
	}
	request := TonightRequest{Filter: LibraryFilter{TagIDs: []uint{tag.ID}, PersonIDs: []uint{person.ID}}, MaxDurationSeconds: 5400, UnwatchedOnly: true, FavoritesOnly: true}
	got, err := (&VideoService{}).SuggestTonight(context.Background(), request)
	if err != nil || len(got) != 1 || got[0].Video.ID != eligible.ID {
		t.Fatalf("unexpected suggestions: %+v, %v", got, err)
	}
	if len(got[0].Video.Tags) != 1 || !strings.Contains(strings.Join(got[0].Reasons, " "), "时间预算") {
		t.Fatalf("missing tags/reasons: %+v", got[0])
	}
	var current models.Video
	if err := database.DB.First(&current, eligible.ID).Error; err != nil {
		t.Fatal(err)
	}
	var events int64
	if err := database.DB.Model(&models.PlayEvent{}).Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if current.PlayCount != 4 || current.RandomPlayCount != 2 || events != 0 {
		t.Fatalf("recommendation wrote playback history: %+v events=%d", current, events)
	}
	request.Filter.TagIDs = []uint{999999}
	got, err = (&VideoService{}).SuggestTonight(context.Background(), request)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty filter relaxed: %+v %v", got, err)
	}
}

func TestTonightRankingUnknownDurationAndLimit(t *testing.T) {
	setupVideoServiceTestDB(t)
	rating := 8.5
	rows := []models.Video{
		{Name: "unrated", Path: "/fixture/a", Directory: "/fixture", Duration: 10},
		{Name: "rated", Path: "/fixture/b", Directory: "/fixture", Duration: 10, PersonalRating: &rating},
		{Name: "favorite-watched", Path: "/fixture/c", Directory: "/fixture", Duration: 10, IsFavorite: true, IsWatched: true},
		{Name: "favorite-unwatched-unknown", Path: "/fixture/d", Directory: "/fixture", IsFavorite: true},
	}
	for i := range rows {
		if err := database.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err := (&VideoService{}).SuggestTonight(context.Background(), TonightRequest{})
	if err != nil || len(got) != 4 {
		t.Fatalf("suggestions: %+v %v", got, err)
	}
	for i, want := range []uint{rows[3].ID, rows[2].ID, rows[1].ID, rows[0].ID} {
		if got[i].Video.ID != want {
			t.Fatalf("wrong rank %d: %+v", i, got)
		}
	}
	if !strings.Contains(strings.Join(got[0].Reasons, " "), "时长未记录") {
		t.Fatal("unknown duration disguised")
	}
	got, err = (&VideoService{}).SuggestTonight(context.Background(), TonightRequest{MaxDurationSeconds: 10, Limit: 1})
	if err != nil || len(got) != 1 || got[0].Video.ID != rows[2].ID {
		t.Fatalf("budget/limit: %+v %v", got, err)
	}
}

func TestTonightRejectsInvalidAndCancelledRequests(t *testing.T) {
	setupVideoServiceTestDB(t)
	for _, request := range []TonightRequest{{MaxDurationSeconds: -1}, {MaxDurationSeconds: 86401}, {Limit: -1}, {Limit: 13}, {Filter: LibraryFilter{SearchMode: "semantic"}}} {
		if _, err := (&VideoService{}).SuggestTonight(context.Background(), request); err == nil {
			t.Fatalf("accepted invalid %+v", request)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&VideoService{}).SuggestTonight(ctx, TonightRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}

func TestTonightScopeReadUsesCallerDeadline(t *testing.T) {
	setupVideoServiceTestDB(t)
	pool, err := database.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	held, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := (&VideoService{}).SuggestTonight(ctx, TonightRequest{}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline: %v", err)
		}
	case <-time.After(time.Second):
		_ = held.Close()
		<-done
		t.Fatal("scope lookup waited without the caller deadline")
	}
}
