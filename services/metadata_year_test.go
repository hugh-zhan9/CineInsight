package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

// D-MC-01：NFO 年份三种写法、越界忽略、前一项不可用时看下一项。
func TestParseLocalMovieNFOReadsYearFromThreeForms(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int // 0 表示没有年份
	}{
		{"year", `<year>1999</year><premiered>2001-02-03</premiered>`, 1999},
		{"premiered", `<premiered> 2004-05-06 </premiered><releasedate>2010-01-01</releasedate>`, 2004},
		{"releasedate", `<releasedate>1987-12-31</releasedate>`, 1987},
		{"year only digits", `<year>2100</year>`, 2100},
		{"lower bound", `<year>1870</year>`, 1870},
		{"below range ignored", `<year>1869</year>`, 0},
		{"above range ignored", `<year>2101</year>`, 0},
		{"zero falls through", `<year>0</year><premiered>2003-01-01</premiered>`, 2003},
		{"out of range falls through", `<year>3000</year><releasedate>1995-07-01</releasedate>`, 1995},
		{"five digits ignored", `<year>19990</year>`, 0},
		{"non numeric ignored", `<year>unknown</year>`, 0},
		{"absent", `<title>x</title>`, 0},
	}
	for _, tc := range cases {
		document, err := parseLocalMovieNFO([]byte(`<movie>` + tc.body + `</movie>`))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.want == 0 {
			if document.Year != nil {
				t.Fatalf("%s: year = %d, want none", tc.name, *document.Year)
			}
			continue
		}
		if document.Year == nil || *document.Year != tc.want {
			t.Fatalf("%s: year = %v, want %d", tc.name, document.Year, tc.want)
		}
	}
}

func metadataYearTestVideo(t *testing.T, nfo string, year *int) (models.Video, *LocalMetadataService) {
	t.Helper()
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, videoPath)
	if err := os.WriteFile(filepath.Join(root, "movie.nfo"), []byte(nfo), 0644); err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, Year: year}
	mcMust(t, database.DB.Create(&video).Error)
	dataDir := t.TempDir()
	return video, NewLocalMetadataService(dataDir, NewPersonService(dataDir), NewCollectionService(dataDir))
}

func reloadVideoYear(t *testing.T, id uint) *int {
	t.Helper()
	var video models.Video
	mcMust(t, database.DB.First(&video, id).Error)
	return video.Year
}

// 空就自动补：新视频（ApplyDefaults）与后台补全都把 NFO 年份填进空的年份。
func TestLocalMetadataFillsEmptyYearByDefault(t *testing.T) {
	video, service := metadataYearTestVideo(t, `<movie><year>2002</year></movie>`, nil)
	diff, err := service.GetDiff(video.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Year.ChangeType != "fill" || !diff.Year.DefaultSelected || diff.Year.SourceValue != "2002" || diff.Year.CurrentValue != "" {
		t.Fatalf("year diff = %#v", diff.Year)
	}
	if !localMetadataDiffHasDefaults(diff) {
		t.Fatal("year fill must count as a default for the backfill")
	}
	result, err := service.ApplyDefaults(video.ID)
	if err != nil {
		t.Fatal(err)
	}
	if year := reloadVideoYear(t, video.ID); year == nil || *year != 2002 {
		t.Fatalf("filled year = %v (result %#v)", year, result)
	}
	if result.Status != LocalMetadataStateCurrent {
		t.Fatalf("status = %s", result.Status)
	}
}

// 已有值时只标「有更新」，用户确认覆盖后才写。
func TestLocalMetadataExistingYearOnlyFlagsUpdate(t *testing.T) {
	existing := 1990
	video, service := metadataYearTestVideo(t, `<movie><premiered>2005-01-01</premiered></movie>`, &existing)
	diff, err := service.GetDiff(video.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Year.ChangeType != "overwrite" || diff.Year.DefaultSelected || !diff.Year.RequiresOverwrite || diff.Status != LocalMetadataStateUpdateAvailable {
		t.Fatalf("year diff = %#v status = %s", diff.Year, diff.Status)
	}
	if _, err := service.ApplyDefaults(video.ID); err != nil {
		t.Fatal(err)
	}
	if year := reloadVideoYear(t, video.ID); year == nil || *year != 1990 {
		t.Fatalf("defaults must not overwrite year, got %v", year)
	}
	var state models.VideoLocalMetadataState
	mcMust(t, database.DB.First(&state, "video_id = ?", video.ID).Error)
	if state.Status != LocalMetadataStateUpdateAvailable {
		t.Fatalf("state = %s", state.Status)
	}
	diff, err = service.GetDiff(video.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := LocalMetadataApplyRequest{VideoID: video.ID, ManifestSHA256: diff.ManifestSHA256, CurrentSHA256: diff.CurrentSHA256, SelectedFields: []string{"year"}}
	if _, err := service.Apply(request); !errors.Is(err, ErrLocalMetadataOverwriteRequired) {
		t.Fatalf("overwrite without confirmation error = %v", err)
	}
	request.OverwriteFields = []string{"year"}
	if _, err := service.Apply(request); err != nil {
		t.Fatal(err)
	}
	if year := reloadVideoYear(t, video.ID); year == nil || *year != 2005 {
		t.Fatalf("confirmed overwrite year = %v", year)
	}
}

// 年份是当前指纹的一部分：差异算出后年份被别处改了，应用会报冲突。
func TestLocalMetadataYearChangeInvalidatesDiff(t *testing.T) {
	video, service := metadataYearTestVideo(t, `<movie><year>2002</year></movie>`, nil)
	diff, err := service.GetDiff(video.ID)
	if err != nil {
		t.Fatal(err)
	}
	mcMust(t, database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Update("year", 2020).Error)
	request := LocalMetadataApplyRequest{VideoID: video.ID, ManifestSHA256: diff.ManifestSHA256, CurrentSHA256: diff.CurrentSHA256, SelectedFields: []string{"year"}}
	if _, err := service.Apply(request); !errors.Is(err, ErrLocalMetadataConflict) {
		t.Fatalf("stale year diff error = %v", err)
	}
}

func TestLocalMetadataExportWritesYear(t *testing.T) {
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, videoPath)
	nfoPath := filepath.Join(root, "movie.nfo")
	if err := os.WriteFile(nfoPath, []byte(`<movie><title>T</title><year>1950</year><plot>keep</plot></movie>`), 0644); err != nil {
		t.Fatal(err)
	}
	year := 2011
	if _, err := ExportLocalMetadataNFO(context.Background(), videoPath, LocalMetadataNFOExportInput{DisplayTitle: "T", Year: &year}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(nfoPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "<year>2011</year>") || strings.Contains(string(content), "1950") || strings.Count(string(content), "<year>") != 1 {
		t.Fatalf("exported NFO = %s", content)
	}
	document, err := parseLocalMovieNFO(content)
	if err != nil || document.Year == nil || *document.Year != 2011 {
		t.Fatalf("round trip year = %v, %v", document.Year, err)
	}
	// 没有年份时保留 NFO 原有的 <year>。
	if _, err := ExportLocalMetadataNFO(context.Background(), videoPath, LocalMetadataNFOExportInput{DisplayTitle: "T"}); err != nil {
		t.Fatal(err)
	}
	if content, _ = os.ReadFile(nfoPath); !strings.Contains(string(content), "<year>2011</year>") {
		t.Fatalf("nil year must keep existing element: %s", content)
	}
	bad := 1800
	if _, err := ExportLocalMetadataNFO(context.Background(), videoPath, LocalMetadataNFOExportInput{DisplayTitle: "T", Year: &bad}); !errors.Is(err, ErrLocalMetadataNFOInvalid) {
		t.Fatalf("out of range export error = %v", err)
	}
}

// 详情编辑整体替换：带年份就写、nil 清空、越界拒绝。
func TestUpdateVideoDetailsReplacesYear(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	details := NewVideoDetailService(NewPersonService(dataDir), NewCollectionService(dataDir))
	video := models.Video{Name: "a.mp4", Path: "/v/a.mp4", Directory: "/v"}
	mcMust(t, database.DB.Create(&video).Error)
	year := 1984
	updated, err := details.UpdateVideoDetails(VideoDetailsUpdate{VideoID: video.ID, Year: &year})
	if err != nil || updated.Video.Year == nil || *updated.Video.Year != 1984 {
		t.Fatalf("set year = %#v, %v", updated, err)
	}
	if _, err := details.UpdateVideoDetails(VideoDetailsUpdate{VideoID: video.ID}); err != nil {
		t.Fatal(err)
	}
	if got := reloadVideoYear(t, video.ID); got != nil {
		t.Fatalf("omitted year must clear, got %d", *got)
	}
	for _, bad := range []int{1869, 2101} {
		value := bad
		if _, err := details.UpdateVideoDetails(VideoDetailsUpdate{VideoID: video.ID, Year: &value}); !errors.Is(err, ErrInvalidVideoYear) {
			t.Fatalf("year %d error = %v", bad, err)
		}
	}
}

// Jellyfin 条目有年份才输出 ProductionYear。
func TestJellyfinItemProductionYear(t *testing.T) {
	s, token, video, _ := jellyfinLibraryFixture(t)
	path := "/Items/" + jellyfinID(jellyVideo, video.ID)
	if item := jellyfinObject(t, jellyfinRequest(s, "GET", path, token, "")); item["ProductionYear"] != nil {
		t.Fatalf("invented year: %v", item["ProductionYear"])
	}
	mcMust(t, database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Update("year", 1997).Error)
	if item := jellyfinObject(t, jellyfinRequest(s, "GET", path, token, "")); item["ProductionYear"] != float64(1997) {
		t.Fatalf("ProductionYear = %#v", item["ProductionYear"])
	}
}
