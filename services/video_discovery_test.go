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
	request := RandomPlayRequest{Filter: LibraryFilter{TagIDs: []uint{tag.ID}, PersonIDs: []uint{person.ID}}, MaxDurationSeconds: 5400, WatchState: RandomWatchUnwatched, FavoritesOnly: true}
	got, err := (&VideoService{}).SuggestTonight(context.Background(), request, 0)
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
	request.ExcludeIDs = []uint{eligible.ID}
	got, err = (&VideoService{}).SuggestTonight(context.Background(), request, 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("recent random exclusion relaxed: %+v %v", got, err)
	}
	request.ExcludeIDs = nil
	request.Filter.TagIDs = []uint{999999}
	got, err = (&VideoService{}).SuggestTonight(context.Background(), request, 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty filter relaxed: %+v %v", got, err)
	}
}

func TestTonightDrawsWithoutRepeatsAndKeepsUnknownDurationHonest(t *testing.T) {
	setupVideoServiceTestDB(t)
	rating := 8.5
	rows := []models.Video{
		{Name: "unrated", Path: "/fixture/a", Directory: "/fixture", Duration: 10},
		{Name: "rated", Path: "/fixture/b", Directory: "/fixture", Duration: 10, PersonalRating: &rating, PlayCount: 2, RandomPlayCount: 1},
		{Name: "favorite-watched", Path: "/fixture/c", Directory: "/fixture", Duration: 10, IsFavorite: true, IsWatched: true},
		{Name: "favorite-unwatched-unknown", Path: "/fixture/d", Directory: "/fixture", IsFavorite: true},
	}
	for i := range rows {
		if err := database.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err := (&VideoService{}).SuggestTonight(context.Background(), RandomPlayRequest{}, 0)
	if err != nil || len(got) != 4 {
		t.Fatalf("suggestions: %+v %v", got, err)
	}
	seen := map[uint]bool{}
	for _, item := range got {
		if seen[item.Video.ID] {
			t.Fatalf("duplicate candidate %d", item.Video.ID)
		}
		seen[item.Video.ID] = true
		reasons := strings.Join(item.Reasons, " ")
		switch item.Video.ID {
		case rows[3].ID:
			if !strings.Contains(reasons, "时长未记录") {
				t.Fatalf("unknown duration disguised: %v", item.Reasons)
			}
		case rows[1].ID:
			if !strings.Contains(reasons, "个人评分 8.5") || !strings.Contains(reasons, "播放过 3 次") {
				t.Fatalf("rating/play facts: %v", item.Reasons)
			}
		case rows[0].ID:
			if strings.Contains(reasons, "评分") || !strings.Contains(reasons, "还没播放过") {
				t.Fatalf("unrated must not report a rating: %v", item.Reasons)
			}
		}
	}
	got, err = (&VideoService{}).SuggestTonight(context.Background(), RandomPlayRequest{MaxDurationSeconds: 10}, 1)
	if err != nil || len(got) != 1 || got[0].Video.ID == rows[3].ID {
		t.Fatalf("budget/limit: %+v %v", got, err)
	}
}

// 「今晚看什么」与随机播放共用均衡加权：播放越少越容易被抽到，而不是固定排序或等概率。
func TestTonightFavorsLessPlayedVideos(t *testing.T) {
	setupVideoServiceTestDB(t)
	fresh := models.Video{Name: "fresh", Path: "/fixture/fresh", Directory: "/fixture", Duration: 10}
	worn := models.Video{Name: "worn", Path: "/fixture/worn", Directory: "/fixture", Duration: 10, PlayCount: 50, IsFavorite: true}
	for _, video := range []*models.Video{&fresh, &worn} {
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatal(err)
		}
	}
	freshFirst := 0
	for range 200 {
		got, err := (&VideoService{}).SuggestTonight(context.Background(), RandomPlayRequest{}, 1)
		if err != nil || len(got) != 1 {
			t.Fatalf("draw: %+v %v", got, err)
		}
		if got[0].Video.ID == fresh.ID {
			freshFirst++
		}
	}
	// 权重约 101:1，期望约 198 次；等概率或「收藏优先」排序都会远低于下限。
	if freshFirst < 170 {
		t.Fatalf("less-played video drawn only %d/200 times", freshFirst)
	}
}

func TestTonightRejectsInvalidAndCancelledRequests(t *testing.T) {
	setupVideoServiceTestDB(t)
	for _, request := range []RandomPlayRequest{{MaxDurationSeconds: -1}, {MaxDurationSeconds: 86401}, {WatchState: "later"}, {Filter: LibraryFilter{SearchMode: "semantic"}}} {
		if _, err := (&VideoService{}).SuggestTonight(context.Background(), request, 0); err == nil {
			t.Fatalf("accepted invalid %+v", request)
		}
	}
	for _, limit := range []int{-1, 13} {
		if _, err := (&VideoService{}).SuggestTonight(context.Background(), RandomPlayRequest{}, limit); err == nil {
			t.Fatalf("accepted invalid limit %d", limit)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&VideoService{}).SuggestTonight(ctx, RandomPlayRequest{}, 0); !errors.Is(err, context.Canceled) {
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
	go func() { _, err := (&VideoService{}).SuggestTonight(ctx, RandomPlayRequest{}, 0); done <- err }()
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

func TestTonightWatchedOnlyForRewatching(t *testing.T) {
	setupVideoServiceTestDB(t)
	rows := []models.Video{
		{Name: "watched", Path: "/fixture/w", Directory: "/fixture", Duration: 10, IsWatched: true},
		{Name: "unwatched", Path: "/fixture/u", Directory: "/fixture", Duration: 10},
	}
	for i := range rows {
		if err := database.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err := (&VideoService{}).SuggestTonight(context.Background(), RandomPlayRequest{WatchState: RandomWatchWatched}, 0)
	if err != nil || len(got) != 1 || got[0].Video.ID != rows[0].ID {
		t.Fatalf("watched state should keep only watched videos: %+v %v", got, err)
	}
	if !strings.Contains(strings.Join(got[0].Reasons, " "), "看过，适合重温") {
		t.Fatalf("missing rewatch reason: %+v", got[0].Reasons)
	}
	got, err = (&VideoService{}).SuggestTonight(context.Background(), RandomPlayRequest{WatchState: RandomWatchUnwatched}, 0)
	if err != nil || len(got) != 1 || got[0].Video.ID != rows[1].ID {
		t.Fatalf("unwatched state should filter out watched videos: %+v %v", got, err)
	}
}
