package services

import (
	"fmt"
	"testing"
	"video-master/database"
	"video-master/models"
)

func ratingPointer(value float64) *float64 { return &value }

func TestLibraryFileSearchMatchesDisplayAndOriginalTitles(t *testing.T) {
	setupVideoServiceTestDB(t)
	videos := []models.Video{
		{Name: "opaque-a.mkv", Path: "/library/opaque-a.mkv", DisplayTitle: "春光乍泄", OriginalTitle: "Happy Together"},
		{Name: "opaque-b.mkv", Path: "/library/opaque-b.mkv", DisplayTitle: "In the Mood for Love", OriginalTitle: "花样年华"},
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatalf("创建标题搜索视频失败: %v", err)
	}
	svc := &VideoService{}
	for _, keyword := range []string{"春光", "happy together", "花样年华"} {
		page, err := svc.SearchLibraryVideoPage(LibraryFilter{Keyword: keyword}, nil, 20)
		if err != nil || len(page.Videos) != 1 {
			t.Fatalf("标题搜索 keyword=%q videos=%#v err=%v", keyword, page.Videos, err)
		}
	}
}

func TestLibraryRatingFilterDistinguishesZeroFromUnratedAndPersistsView(t *testing.T) {
	setupVideoServiceTestDB(t)
	videos := []models.Video{
		{Name: "zero.mkv", Path: "/rating/zero.mkv", PersonalRating: ratingPointer(0)},
		{Name: "half.mkv", Path: "/rating/half.mkv", PersonalRating: ratingPointer(0.5)},
		{Name: "unrated.mkv", Path: "/rating/unrated.mkv"},
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatalf("创建评分视频失败: %v", err)
	}
	min, max := 0.0, 0.0
	filter := LibraryFilter{MinRating: &min, MaxRating: &max, SortMode: LibrarySortRatingAsc}
	page, err := (&VideoService{}).SearchLibraryVideoPage(filter, nil, 20)
	if err != nil {
		t.Fatalf("评分范围查询失败: %v", err)
	}
	if len(page.Videos) != 1 || page.Videos[0].ID != videos[0].ID || page.Videos[0].PersonalRating == nil {
		t.Fatalf("0 分与未评分未区分: %#v", page.Videos)
	}
	view, err := (&VideoService{}).SaveLibraryView(SavedLibraryViewInput{Name: "Zero rating", LibraryFilter: filter})
	if err != nil {
		t.Fatalf("保存评分视图失败: %v", err)
	}
	if view.MinRating == nil || *view.MinRating != 0 || view.MaxRating == nil || *view.MaxRating != 0 || view.SortMode != LibrarySortRatingAsc {
		t.Fatalf("评分视图字段未持久化: %#v", view)
	}
}

func TestLibraryRatingPaginationTraversesNullSegmentWithoutDuplicates(t *testing.T) {
	for _, sortMode := range []string{LibrarySortRatingDesc, LibrarySortRatingAsc} {
		t.Run(sortMode, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			videos := []models.Video{
				{Name: "ten-a", Path: "/page/ten-a-" + sortMode, PersonalRating: ratingPointer(10)},
				{Name: "ten-b", Path: "/page/ten-b-" + sortMode, PersonalRating: ratingPointer(10)},
				{Name: "five", Path: "/page/five-" + sortMode, PersonalRating: ratingPointer(5)},
				{Name: "zero", Path: "/page/zero-" + sortMode, PersonalRating: ratingPointer(0)},
				{Name: "null-a", Path: "/page/null-a-" + sortMode},
				{Name: "null-b", Path: "/page/null-b-" + sortMode},
				{Name: "null-c", Path: "/page/null-c-" + sortMode},
			}
			if err := database.DB.Create(&videos).Error; err != nil {
				t.Fatalf("创建分页评分视频失败: %v", err)
			}
			var cursor *LibraryVideoCursor
			seen := map[uint]bool{}
			nullCursorSeen := false
			for {
				page, err := (&VideoService{}).SearchLibraryVideoPage(LibraryFilter{SortMode: sortMode}, cursor, 2)
				if err != nil {
					t.Fatalf("评分分页失败: %v", err)
				}
				for _, video := range page.Videos {
					if seen[video.ID] {
						t.Fatalf("分页重复视频 id=%d", video.ID)
					}
					seen[video.ID] = true
				}
				if page.NextCursor == nil {
					break
				}
				if page.NextCursor.RatingIsNull {
					nullCursorSeen = true
				}
				cursor = page.NextCursor
			}
			if len(seen) != len(videos) || !nullCursorSeen {
				t.Fatalf("未完整穿越 NULL 段: seen=%d want=%d nullCursor=%v", len(seen), len(videos), nullCursorSeen)
			}
		})
	}
}

func TestLibraryRatingCursorRejectsSortMismatch(t *testing.T) {
	setupVideoServiceTestDB(t)
	cursor := &LibraryVideoCursor{SortMode: LibrarySortRatingDesc, Rating: ratingPointer(5), ID: 1}
	if _, err := (&VideoService{}).SearchLibraryVideoPage(LibraryFilter{SortMode: LibrarySortRatingAsc}, cursor, 20); err == nil {
		t.Fatal("游标排序模式不匹配应拒绝")
	}
}

func TestLibraryBalancedPaginationSupportsMaximumPageSize(t *testing.T) {
	setupVideoServiceTestDB(t)
	videos := make([]models.Video, 201)
	for index := range videos {
		videos[index] = models.Video{
			Name: "maximum-page-video",
			Path: fmt.Sprintf("/maximum-page/%03d.mkv", index),
		}
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatalf("创建最大分页视频失败: %v", err)
	}

	svc := &VideoService{}
	first, err := svc.SearchLibraryVideoPage(LibraryFilter{}, nil, 200)
	if err != nil {
		t.Fatalf("查询最大分页第一页失败: %v", err)
	}
	if len(first.Videos) != 200 || first.NextCursor == nil {
		t.Fatalf("最大分页第一页错误: count=%d cursor=%#v", len(first.Videos), first.NextCursor)
	}
	second, err := svc.SearchLibraryVideoPage(LibraryFilter{}, first.NextCursor, 200)
	if err != nil {
		t.Fatalf("查询最大分页第二页失败: %v", err)
	}
	if len(second.Videos) != 1 || second.NextCursor != nil {
		t.Fatalf("最大分页第二页错误: count=%d cursor=%#v", len(second.Videos), second.NextCursor)
	}
}
