package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

// §2.3 海报墙的三种新排序：翻页连续无重复无遗漏（含相同值、空年份）、片名按拼音、版本组一张卡、游标逐模式校验。

type browseSeed struct {
	title string
	year  *int
	added time.Time
}

func seedBrowseVideos(t *testing.T, seeds []browseSeed) []models.Video {
	t.Helper()
	videos := make([]models.Video, 0, len(seeds))
	for index, seed := range seeds {
		video := models.Video{
			Name: fmt.Sprintf("file-%02d.mp4", index), DisplayTitle: seed.title, Year: seed.year,
			Path: fmt.Sprintf("/library/browse/%02d.mp4", index), Directory: "/library/browse",
			Size: 1000, CreatedAt: seed.added,
		}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatalf("建视频失败: %v", err)
		}
		videos = append(videos, video)
	}
	return videos
}

// collectBrowsePages 按 limit 一页页翻完，每页之间把游标经 JSON 往返一次（前端原样回传），mutate 可改写回传的游标。
func collectBrowsePages(t *testing.T, filter LibraryFilter, limit int, mutate func(*LibraryVideoCursor)) []uint {
	t.Helper()
	service := &VideoService{}
	var cursor *LibraryVideoCursor
	ids := []uint{}
	for pages := 0; pages < 100; pages++ {
		page, err := service.SearchLibraryVideoPage(filter, cursor, limit)
		if err != nil {
			t.Fatalf("%s 第 %d 页失败: %v", filter.SortMode, pages+1, err)
		}
		ids = append(ids, pageVideoIDs(page)...)
		if page.NextCursor == nil {
			return ids
		}
		encoded, err := json.Marshal(page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		cursor = &LibraryVideoCursor{}
		if err := json.Unmarshal(encoded, cursor); err != nil {
			t.Fatal(err)
		}
		if mutate != nil {
			mutate(cursor)
		}
	}
	t.Fatal("翻页没有结束")
	return nil
}

func expectedBrowseOrder(videos []models.Video, sortMode string) []uint {
	ordered := append([]models.Video{}, videos...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		switch sortMode {
		case LibrarySortAddedDesc:
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.After(b.CreatedAt)
			}
			return a.ID > b.ID
		case LibrarySortYearDesc:
			if (a.Year == nil) != (b.Year == nil) {
				return a.Year != nil
			}
			if a.Year != nil && *a.Year != *b.Year {
				return *a.Year > *b.Year
			}
			return a.ID > b.ID
		default:
			ka, kb := models.VideoTitleSortKey(a.DisplayTitle, a.Name), models.VideoTitleSortKey(b.DisplayTitle, b.Name)
			if ka != kb {
				return ka < kb
			}
			return a.ID < b.ID
		}
	})
	ids := make([]uint, 0, len(ordered))
	for _, video := range ordered {
		ids = append(ids, video.ID)
	}
	return ids
}

func TestLibraryBrowseSortsPaginateWithoutGapsOrDuplicates(t *testing.T) {
	setupVideoServiceTestDB(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 123456789, time.Local)
	seeds := []browseSeed{
		{"大话西游", intPtr(1995), base},
		{"阿凡达", intPtr(2009), base}, // 与上一条同一添加时间
		{"霸王别姬", nil, base.Add(time.Minute)},
		{"茶馆", intPtr(2009), base.Add(-time.Minute)}, // 与阿凡达同年
		{"阿凡达", nil, base.Add(time.Hour)},            // 同名：同一排序键
		{"Zootopia", intPtr(2016), base.Add(time.Nanosecond * 500)},
		{"", intPtr(1995), base}, // 显示标题为空：按文件名排
		{"银河护卫队", nil, base.Add(2 * time.Hour)},
		{"2046", intPtr(2004), base.Add(time.Microsecond)},
		{"阿凡达", intPtr(2009), base},
		{"重庆森林", intPtr(1994), base.Add(-time.Hour)},
	}
	videos := seedBrowseVideos(t, seeds)
	for _, sortMode := range []string{LibrarySortAddedDesc, LibrarySortYearDesc, LibrarySortTitleAsc} {
		want := expectedBrowseOrder(videos, sortMode)
		for _, limit := range []int{1, 2, 3, 5, 200} {
			got := collectBrowsePages(t, LibraryFilter{SortMode: sortMode}, limit, nil)
			requireUintSlice(t, fmt.Sprintf("%s limit=%d", sortMode, limit), got, want)
		}
	}
	// 前端把添加时间截到毫秒、换成 UTC 回传：仍然不漏不重。
	got := collectBrowsePages(t, LibraryFilter{SortMode: LibrarySortAddedDesc}, 2, func(cursor *LibraryVideoCursor) {
		truncated := cursor.CreatedAt.UTC().Truncate(time.Millisecond)
		cursor.CreatedAt = &truncated
	})
	requireUintSlice(t, "截到毫秒的添加时间游标", got, expectedBrowseOrder(videos, LibrarySortAddedDesc))
}

func TestLibraryTitleSortFollowsPinyin(t *testing.T) {
	setupVideoServiceTestDB(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.Local)
	videos := seedBrowseVideos(t, []browseSeed{
		{"大话西游", nil, base}, {"茶馆", nil, base}, {"阿凡达", nil, base}, {"霸王别姬", nil, base},
	})
	page, err := (&VideoService{}).SearchLibraryVideoPage(LibraryFilter{SortMode: LibrarySortTitleAsc}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	titles := []string{}
	for _, video := range page.Videos {
		titles = append(titles, video.DisplayTitle)
	}
	if got := strings.Join(titles, " < "); got != "阿凡达 < 霸王别姬 < 茶馆 < 大话西游" {
		t.Fatalf("片名应按拼音排: %s（视频 %d 条）", got, len(videos))
	}
}

func TestLibraryBrowseSortsCollapseVersionGroups(t *testing.T) {
	setupVideoServiceTestDB(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.Local)
	videos := seedBrowseVideos(t, []browseSeed{
		{"阿凡达", intPtr(2009), base}, {"阿凡达 4K", intPtr(2009), base.Add(time.Minute)}, {"茶馆", nil, base.Add(time.Hour)},
	})
	if _, err := NewVersionGroupService().Create(context.Background(), []uint{videos[0].ID, videos[1].ID}, "阿凡达"); err != nil {
		t.Fatal(err)
	}
	for _, sortMode := range []string{LibrarySortAddedDesc, LibrarySortYearDesc, LibrarySortTitleAsc} {
		filter := LibraryFilter{SortMode: sortMode, CollapseVersions: true}
		got := collectBrowsePages(t, filter, 1, nil)
		if len(got) != 2 || !containsUint(got, videos[0].ID) || containsUint(got, videos[1].ID) {
			t.Fatalf("%s：版本组应只出代表一张卡: %v", sortMode, got)
		}
		page, err := (&VideoService{}).SearchLibraryVideoPage(filter, nil, 10)
		if err != nil || len(page.VersionGroups) != 1 {
			t.Fatalf("%s：应带版本组汇总: %+v err=%v", sortMode, page.VersionGroups, err)
		}
		if count := mustLibraryCount(t, filter); count != 2 {
			t.Fatalf("%s：计数应按卡片: %d", sortMode, count)
		}
	}
}

func containsUint(values []uint, target uint) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestLibraryCursorValidationPerSortMode(t *testing.T) {
	now := time.Now()
	year := 2009
	rating := 8.0
	cases := []struct {
		name   string
		mode   string
		cursor LibraryVideoCursor
		ok     bool
	}{
		{"最近添加", LibrarySortAddedDesc, LibraryVideoCursor{SortMode: LibrarySortAddedDesc, CreatedAt: &now, ID: 1}, true},
		{"最近添加缺时间", LibrarySortAddedDesc, LibraryVideoCursor{SortMode: LibrarySortAddedDesc, ID: 1}, false},
		{"最近添加带年份", LibrarySortAddedDesc, LibraryVideoCursor{SortMode: LibrarySortAddedDesc, CreatedAt: &now, Year: &year, ID: 1}, false},
		{"最近添加带分数", LibrarySortAddedDesc, LibraryVideoCursor{SortMode: LibrarySortAddedDesc, CreatedAt: &now, Score: 1, ID: 1}, false},
		{"年份", LibrarySortYearDesc, LibraryVideoCursor{SortMode: LibrarySortYearDesc, Year: &year, ID: 1}, true},
		{"空年份", LibrarySortYearDesc, LibraryVideoCursor{SortMode: LibrarySortYearDesc, YearIsNull: true, ID: 1}, true},
		{"空年份带年份值", LibrarySortYearDesc, LibraryVideoCursor{SortMode: LibrarySortYearDesc, YearIsNull: true, Year: &year, ID: 1}, false},
		{"年份缺值", LibrarySortYearDesc, LibraryVideoCursor{SortMode: LibrarySortYearDesc, ID: 1}, false},
		{"年份带片名键", LibrarySortYearDesc, LibraryVideoCursor{SortMode: LibrarySortYearDesc, Year: &year, TitleKey: "ab", ID: 1}, false},
		{"片名", LibrarySortTitleAsc, LibraryVideoCursor{SortMode: LibrarySortTitleAsc, TitleKey: "9600ab", ID: 1}, true},
		{"片名空键", LibrarySortTitleAsc, LibraryVideoCursor{SortMode: LibrarySortTitleAsc, ID: 1}, true},
		{"片名非十六进制", LibrarySortTitleAsc, LibraryVideoCursor{SortMode: LibrarySortTitleAsc, TitleKey: "zz", ID: 1}, false},
		{"片名大写", LibrarySortTitleAsc, LibraryVideoCursor{SortMode: LibrarySortTitleAsc, TitleKey: "AB", ID: 1}, false},
		{"片名过长", LibrarySortTitleAsc, LibraryVideoCursor{SortMode: LibrarySortTitleAsc, TitleKey: strings.Repeat("a", 514), ID: 1}, false},
		{"片名带评分", LibrarySortTitleAsc, LibraryVideoCursor{SortMode: LibrarySortTitleAsc, TitleKey: "ab", Rating: &rating, ID: 1}, false},
		{"片名缺 ID", LibrarySortTitleAsc, LibraryVideoCursor{SortMode: LibrarySortTitleAsc, TitleKey: "ab"}, false},
		{"模式不匹配", LibrarySortTitleAsc, LibraryVideoCursor{SortMode: LibrarySortYearDesc, Year: &year, ID: 1}, false},
		{"均衡带添加时间", LibrarySortBalanced, LibraryVideoCursor{SortMode: LibrarySortBalanced, CreatedAt: &now, ID: 1}, false},
		{"评分带片名键", LibrarySortRatingDesc, LibraryVideoCursor{SortMode: LibrarySortRatingDesc, Rating: &rating, TitleKey: "ab", ID: 1}, false},
		{"评分照旧", LibrarySortRatingDesc, LibraryVideoCursor{SortMode: LibrarySortRatingDesc, Rating: &rating, ID: 1}, true},
		{"均衡照旧", LibrarySortBalanced, LibraryVideoCursor{SortMode: LibrarySortBalanced, Score: 3, Size: 10, ID: 1}, true},
	}
	for _, tc := range cases {
		cursor := tc.cursor
		err := validateLibraryVideoCursor(tc.mode, &cursor)
		if (err == nil) != tc.ok {
			t.Errorf("%s：ok=%v err=%v", tc.name, tc.ok, err)
		}
	}
	if _, err := normalizeLibraryFilter(LibraryFilter{SortMode: "title_desc"}); err == nil {
		t.Fatal("未知排序模式应拒绝")
	}
	for _, mode := range []string{LibrarySortAddedDesc, LibrarySortYearDesc, LibrarySortTitleAsc} {
		if _, err := normalizeLibraryFilter(LibraryFilter{SortMode: mode}); err != nil {
			t.Fatalf("%s 应是合法排序: %v", mode, err)
		}
	}
}
