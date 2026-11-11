package services

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestLibraryDirectoryFilenameIntersectionAndSavedView(t *testing.T) {
	setupVideoServiceTestDB(t)
	videos := []models.Video{
		{Name: `预告%_\A.mp4`, Directory: `/电影%_\甲`, Path: `/电影%_\甲/预告%_\A.mp4`, Size: 10},
		{Name: `正片.mp4`, Directory: `/电影%_\甲`, Path: `/电影%_\甲/正片.mp4`, Size: 10},
		{Name: `预告%_\A.mp4`, Directory: `/其他`, Path: `/其他/预告%_\A.mp4`, Size: 10},
		{Name: `预告xxA.mp4`, Directory: `/电影xx甲`, Path: `/电影xx甲/预告xxA.mp4`, Size: 10},
	}
	for i := range videos {
		if err := database.DB.Create(&videos[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &VideoService{}
	for _, tc := range []struct {
		dir, name string
		count     int
	}{
		{` 电影%_\ `, `预告%_\a`, 1}, {`电影%_\`, "", 2}, {"", `预告%_\a`, 2}, {"", "", 4}, {"missing", `预告`, 0},
	} {
		filter := LibraryFilter{DirectoryKeyword: tc.dir, FilenameKeyword: tc.name}
		page, err := svc.SearchLibraryVideoPage(filter, nil, 10)
		if err != nil || len(page.Videos) != tc.count {
			t.Fatalf("%+v page=%+v err=%v", tc, page, err)
		}
		count, err := svc.CountLibraryVideos(filter)
		if err != nil || int(count) != tc.count {
			t.Fatalf("count=%d err=%v", count, err)
		}
	}
	filter := LibraryFilter{DirectoryKeyword: `电影%_\`, FilenameKeyword: `预告%_\a`, SortMode: LibrarySortSizeDesc}
	view, err := svc.SaveLibraryView(SavedLibraryViewInput{Name: "directory and file", LibraryFilter: filter})
	if err != nil {
		t.Fatal(err)
	}
	if view.DirectoryKeyword != filter.DirectoryKeyword || view.FilenameKeyword != filter.FilenameKeyword {
		t.Fatalf("lost conditions: %+v", view)
	}
	if _, err = svc.UpdateSavedLibraryView(view.ID, view.Name, LibraryFilter{}); err != nil {
		t.Fatal(err)
	}
	if err = database.DB.First(view, view.ID).Error; err != nil {
		t.Fatal(err)
	}
	if view.DirectoryKeyword != "" || view.FilenameKeyword != "" {
		t.Fatalf("failed clear: %+v", view)
	}
}

func TestLibraryFileSortsKeysetPagination(t *testing.T) {
	setupVideoServiceTestDB(t)
	names := []string{"", "a.mp4", "A.mp4", "a.mp4", "电影.mp4", "2.mp4", "10.mp4"}
	rows := make([]models.Video, len(names))
	for i, name := range names {
		rows[i] = models.Video{Name: name, Path: fmt.Sprintf("/sort/%d", i), Directory: "/sort", Size: int64(i % 3), DisplayTitle: fmt.Sprint(99 - i)}
		if err := database.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{LibrarySortFilenameAsc, LibrarySortFilenameDesc, LibrarySortSizeAsc, LibrarySortSizeDesc} {
		expected := append([]models.Video(nil), rows...)
		sort.Slice(expected, func(i, j int) bool {
			a, b := expected[i], expected[j]
			cmp := 0
			if mode == LibrarySortFilenameAsc || mode == LibrarySortFilenameDesc {
				if a.Name < b.Name {
					cmp = -1
				}
				if a.Name > b.Name {
					cmp = 1
				}
			} else {
				if a.Size < b.Size {
					cmp = -1
				}
				if a.Size > b.Size {
					cmp = 1
				}
			}
			if cmp == 0 {
				if a.ID < b.ID {
					cmp = -1
				} else {
					cmp = 1
				}
			}
			if mode == LibrarySortFilenameDesc || mode == LibrarySortSizeDesc {
				return cmp > 0
			}
			return cmp < 0
		})
		ids := make([]uint, len(expected))
		for i := range expected {
			ids[i] = expected[i].ID
		}
		for _, limit := range []int{1, 2, 20} {
			got := collectBrowsePages(t, LibraryFilter{SortMode: mode}, limit, nil)
			requireUintSlice(t, mode, got, ids)
		}
	}
	// Empty and singleton filters still terminate without an extra cursor.
	svc := &VideoService{}
	for _, filter := range []LibraryFilter{{FilenameKeyword: "missing", SortMode: LibrarySortSizeAsc}, {FilenameKeyword: "电影", SortMode: LibrarySortFilenameAsc}} {
		page, err := svc.SearchLibraryVideoPage(filter, nil, 1)
		if err != nil || page.NextCursor != nil {
			t.Fatalf("bad last page %+v %v", page, err)
		}
	}
}

func TestLibraryFileCursorRejectsMixedFields(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		cursor LibraryVideoCursor
	}{
		{LibrarySortFilenameAsc, LibraryVideoCursor{SortMode: LibrarySortSizeAsc, ID: 1}},
		{LibrarySortFilenameAsc, LibraryVideoCursor{SortMode: LibrarySortFilenameAsc, Size: 1, ID: 1}},
		{LibrarySortSizeAsc, LibraryVideoCursor{SortMode: LibrarySortSizeAsc, Filename: "a", ID: 1}},
		{LibrarySortSizeDesc, LibraryVideoCursor{SortMode: LibrarySortSizeDesc, Size: -1, ID: 1}},
		{LibrarySortBalanced, LibraryVideoCursor{SortMode: LibrarySortBalanced, Filename: "a", ID: 1}},
	} {
		if err := validateLibraryVideoCursor(tc.mode, &tc.cursor); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
	for _, mode := range []string{LibrarySortFilenameAsc, LibrarySortSizeAsc} {
		if err := validateLibraryVideoCursor(mode, &LibraryVideoCursor{SortMode: mode, ID: 1}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLibraryFileConditionsConstrainRandomCandidates(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := &VideoService{}
	var matching uint
	for i, entry := range []struct{ directory, name string }{{"电影", "预告.mp4"}, {"电影", "正片.mp4"}, {"其他", "预告.mp4"}} {
		dir := filepath.Join(root, entry.directory)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, entry.name)
		if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
		row := models.Video{Name: entry.name, Directory: dir, Path: path, Size: 4, Duration: 60}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			matching = row.ID
		}
	}
	pool, err := svc.collectRandomCandidatesFrom(database.DB, RandomPlayRequest{Filter: LibraryFilter{DirectoryKeyword: "电影", FilenameKeyword: "预告"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pool.rows) != 1 || pool.rows[0].ID != matching {
		t.Fatalf("random ignored conditions %+v", pool.rows)
	}
}
