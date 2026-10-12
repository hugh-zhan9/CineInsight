package services

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// mcFixture 是完整度测试的夹具：full 什么都不缺，lacking[f] 只缺 f 这一项。
type mcFixture struct {
	service *MetadataCompletenessService
	full    models.Video
	lacking map[string]models.Video
	extra   map[string]models.Video
}

// mcVideo 在 dir 下建一条「什么都不缺」的视频，skip 里列出的项不补。
func mcVideo(t *testing.T, thumbnailDir, path string, skip map[string]bool, mutate func(*models.Video)) models.Video {
	t.Helper()
	year := 2001
	video := models.Video{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path),
		DisplayTitle: "标题", Year: &year, Description: "简介", PosterPath: "videos/p.jpg", FanartPath: "videos/f.jpg"}
	if skip[MetadataFieldTitle] {
		video.DisplayTitle = ""
	}
	if skip[MetadataFieldYear] {
		video.Year = nil
	}
	if skip[MetadataFieldDescription] {
		video.Description = ""
	}
	if skip[MetadataFieldPoster] {
		video.PosterPath = ""
	}
	if skip[MetadataFieldFanart] {
		video.FanartPath = ""
	}
	if mutate != nil {
		mutate(&video)
	}
	mcMust(t, database.DB.Create(&video).Error)
	if !skip[MetadataFieldPeople] {
		person := models.Person{DisplayName: "人物 " + path}
		mcMust(t, database.DB.Create(&person).Error)
		mcMust(t, database.DB.Create(&models.VideoPerson{VideoID: video.ID, PersonID: person.ID}).Error)
	}
	if !skip[MetadataFieldTags] {
		tag := models.Tag{Name: "标签 " + path}
		mcMust(t, database.DB.Create(&tag).Error)
		libVisTag(t, video.ID, tag.ID)
	}
	if !skip[MetadataFieldSubtitles] {
		mcMust(t, database.DB.Create(&models.SubtitleIndexState{VideoID: video.ID, SegmentCount: 3}).Error)
	}
	if !skip[MetadataFieldTechnical] {
		now := time.Now()
		mcMust(t, database.DB.Create(&models.VideoTechnicalMetadata{VideoID: video.ID, ProbedAt: &now}).Error)
	}
	if !skip[MetadataFieldThumbnail] {
		if err := os.MkdirAll(thumbnailDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(thumbnailDir, strconv.FormatUint(uint64(video.ID), 10)+".jpg"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return video
}

func mcMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mcSetLocalMetadataState(t *testing.T, videoID uint, status string) {
	t.Helper()
	mcMust(t, database.DB.Create(&models.VideoLocalMetadataState{VideoID: videoID, Status: status, LastCheckedAt: time.Now()}).Error)
}

// newMCFixture 建一个扫描根 /mc 与一组视频：full、每项各缺一项的、以及几种边界。
func newMCFixture(t *testing.T) mcFixture {
	t.Helper()
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	thumbnails := filepath.Join(dataDir, "thumbnails")
	mcMust(t, database.DB.Create(&models.ScanDirectory{Path: "/mc"}).Error)
	fixture := mcFixture{service: NewMetadataCompletenessService(dataDir), lacking: map[string]models.Video{}, extra: map[string]models.Video{}}
	fixture.full = mcVideo(t, thumbnails, "/mc/full/a.mp4", nil, nil)
	for _, field := range append(append([]string{}, videoMissingFieldOrder...), MetadataFieldThumbnail) {
		fixture.lacking[field] = mcVideo(t, thumbnails, "/mc/lack/"+field+".mp4", map[string]bool{field: true}, nil)
	}
	// 只有自动标签：算缺标签（与「未打标签」同口径）。
	autoOnly := mcVideo(t, thumbnails, "/mc/edge/auto-tag.mp4", map[string]bool{MetadataFieldTags: true}, nil)
	autoTag := models.Tag{Name: "短视频", AutomaticKind: "short_video"}
	mcMust(t, database.DB.Create(&autoTag).Error)
	libVisTag(t, autoOnly.ID, autoTag.ID)
	fixture.extra["auto_tag_only"] = autoOnly
	// 没有探测记录但有视频流：技术信息不算缺。
	streamOnly := mcVideo(t, thumbnails, "/mc/edge/stream.mp4", map[string]bool{MetadataFieldTechnical: true}, nil)
	mcMust(t, database.DB.Create(&models.MediaStream{VideoID: streamOnly.ID, StreamIndex: 0, StreamType: "video"}).Error)
	fixture.extra["stream_only"] = streamOnly
	// 探测记录存在但 probed_at 为空（只尝试过、失败了），也没有视频流：算缺。
	failedProbe := mcVideo(t, thumbnails, "/mc/edge/failed-probe.mp4", map[string]bool{MetadataFieldTechnical: true}, nil)
	attempted := time.Now()
	mcMust(t, database.DB.Create(&models.VideoTechnicalMetadata{VideoID: failedProbe.ID, LastAttemptAt: &attempted, LastError: "x"}).Error)
	mcMust(t, database.DB.Create(&models.MediaStream{VideoID: failedProbe.ID, StreamIndex: 0, StreamType: "audio"}).Error)
	fixture.extra["failed_probe"] = failedProbe
	// 旁挂字幕：有字幕。
	sidecar := mcVideo(t, thumbnails, "/mc/edge/sidecar.mp4", map[string]bool{MetadataFieldSubtitles: true}, nil)
	mcMust(t, database.DB.Create(&models.SubtitleIndexState{VideoID: sidecar.ID, HasSidecar: true}).Error)
	fixture.extra["sidecar"] = sidecar
	// 失效的、扫描根以外的：什么都缺，但不在默认可见范围里，不计入。
	everything := map[string]bool{}
	for _, field := range append(append([]string{}, videoMissingFieldOrder...), MetadataFieldThumbnail) {
		everything[field] = true
	}
	stale := mcVideo(t, thumbnails, "/mc/stale/x.mp4", everything, func(v *models.Video) { v.IsStale = true })
	outside := mcVideo(t, thumbnails, "/elsewhere/x.mp4", everything, nil)
	mcSetLocalMetadataState(t, fixture.full.ID, LocalMetadataStateUpdateAvailable)
	mcSetLocalMetadataState(t, fixture.lacking[MetadataFieldTitle].ID, LocalMetadataStateError)
	mcSetLocalMetadataState(t, fixture.lacking[MetadataFieldYear].ID, LocalMetadataStateCurrent)
	mcSetLocalMetadataState(t, stale.ID, LocalMetadataStateUpdateAvailable)
	mcSetLocalMetadataState(t, outside.ID, LocalMetadataStateError)
	return fixture
}

// expectedMissingIDs 是夹具里每一项应该命中的视频（升序）。
func (f mcFixture) expectedMissingIDs(field string) []uint {
	ids := []uint{f.lacking[field].ID}
	switch field {
	case MetadataFieldTags:
		ids = append(ids, f.extra["auto_tag_only"].ID)
	case MetadataFieldTechnical:
		ids = append(ids, f.extra["failed_probe"].ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func TestMetadataCompletenessCountsEachVideoFieldInVisibleScope(t *testing.T) {
	fixture := newMCFixture(t)
	result, err := fixture.service.GetMetadataCompleteness()
	if err != nil {
		t.Fatal(err)
	}
	visible := int64(1 + len(fixture.lacking) + len(fixture.extra))
	if result.VideoTotal != visible {
		t.Fatalf("video total = %d, want %d", result.VideoTotal, visible)
	}
	got := map[string]int64{}
	for _, item := range result.Videos {
		if item.Total != visible {
			t.Fatalf("%s total = %d", item.Field, item.Total)
		}
		got[item.Field] = item.Missing
	}
	want := map[string]int64{}
	for _, field := range videoMissingFieldOrder {
		want[field] = int64(len(fixture.expectedMissingIDs(field)))
	}
	want[MetadataFieldThumbnail] = 1
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("video missing = %v, want %v", got, want)
	}
	if len(result.Videos) != len(videoMissingFieldOrder)+1 || result.Videos[len(result.Videos)-1].Field != MetadataFieldThumbnail {
		t.Fatalf("video item order = %#v", result.Videos)
	}
	pending := map[string]int64{}
	for _, item := range result.VideoPending {
		pending[item.Field] = item.Missing
	}
	if want := map[string]int64{MetadataFieldNFOUpdate: 1, MetadataFieldNFOError: 1}; !reflect.DeepEqual(pending, want) {
		t.Fatalf("pending = %v, want %v", pending, want)
	}
}

func TestLibraryMissingFilterMatchesEachFieldAndStacksWithSmartView(t *testing.T) {
	fixture := newMCFixture(t)
	for field := range videoMissingSQL {
		if got, want := libVisIDs(t, LibraryFilter{Missing: field}), fixture.expectedMissingIDs(field); !reflect.DeepEqual(got, want) {
			t.Fatalf("missing=%s ids = %v, want %v", field, got, want)
		}
	}
	// 与智能视图正交、AND 叠加：收藏了 full 与缺年份的那条，「收藏 + 缺年份」只剩后者。
	lackingYear := fixture.lacking[MetadataFieldYear]
	mcMust(t, database.DB.Model(&models.Video{}).Where("id IN ?", []uint{fixture.full.ID, lackingYear.ID}).Update("is_favorite", true).Error)
	if got := libVisIDs(t, LibraryFilter{SmartView: LibraryViewFavorites, Missing: MetadataFieldYear}); !reflect.DeepEqual(got, []uint{lackingYear.ID}) {
		t.Fatalf("favorites+missing year = %v", got)
	}
	if got := libVisIDs(t, LibraryFilter{SmartView: LibraryViewFavorites, Missing: MetadataFieldTitle}); len(got) != 0 {
		t.Fatalf("favorites+missing title = %v", got)
	}
	// 「未打标签」视图与缺标签同口径，叠加后不变。
	if got := libVisIDs(t, LibraryFilter{SmartView: LibraryViewUntagged, Missing: MetadataFieldTags}); !reflect.DeepEqual(got, fixture.expectedMissingIDs(MetadataFieldTags)) {
		t.Fatalf("untagged+missing tags = %v", got)
	}
	// 本地资料有更新视图 + 缺年份：full 有更新但不缺年份，结果为空。
	if got := libVisIDs(t, LibraryFilter{SmartView: LibraryViewLocalMetadataUpdated, Missing: MetadataFieldYear}); len(got) != 0 {
		t.Fatalf("local updates+missing year = %v", got)
	}
	count, err := NewVideoService(nil).CountLibraryVideos(LibraryFilter{Missing: MetadataFieldPoster})
	if err != nil || count != 1 {
		t.Fatalf("count missing poster = %d, %v", count, err)
	}
}

func TestLibraryMissingFilterRejectsUnknownAndNonListFields(t *testing.T) {
	setupVideoServiceTestDB(t)
	for _, value := range []string{"bogus", MetadataFieldThumbnail, MetadataFieldNFOUpdate, MetadataFieldNFOError, MetadataFieldTakenAt} {
		if _, err := normalizeLibraryFilter(LibraryFilter{Missing: value}); !errors.Is(err, ErrInvalidMissingFilter) {
			t.Fatalf("missing=%q error = %v", value, err)
		}
		if _, err := NewVideoService(nil).CountLibraryVideos(LibraryFilter{Missing: value}); !errors.Is(err, ErrInvalidMissingFilter) {
			t.Fatalf("count missing=%q error = %v", value, err)
		}
	}
	filter, err := normalizeLibraryFilter(LibraryFilter{Missing: "  year "})
	if err != nil || filter.Missing != MetadataFieldYear {
		t.Fatalf("trimmed missing = %#v, %v", filter, err)
	}
	if _, err := normalizeImageFilter(ImageFilter{Missing: MetadataFieldYear}); !errors.Is(err, ErrInvalidMissingFilter) {
		t.Fatalf("image missing=year error = %v", err)
	}
}

func TestSavedLibraryViewPersistsMissingFilter(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := NewVideoService(nil)
	view, err := service.SaveLibraryView(SavedLibraryViewInput{Name: "缺海报", LibraryFilter: LibraryFilter{SmartView: LibraryViewUnwatched, Missing: MetadataFieldPoster}})
	if err != nil {
		t.Fatal(err)
	}
	var stored models.SavedLibraryView
	mcMust(t, database.DB.First(&stored, view.ID).Error)
	if stored.Missing != MetadataFieldPoster || stored.SmartView != LibraryViewUnwatched {
		t.Fatalf("stored view = %#v", stored)
	}
	updated, err := service.UpdateSavedLibraryView(view.ID, "缺年份", LibraryFilter{Missing: MetadataFieldYear})
	if err != nil || updated.Missing != MetadataFieldYear || updated.SmartView != "" {
		t.Fatalf("updated view = %#v, %v", updated, err)
	}
	cleared, err := service.UpdateSavedLibraryView(view.ID, "缺年份", LibraryFilter{})
	if err != nil || cleared.Missing != "" {
		t.Fatalf("cleared view = %#v, %v", cleared, err)
	}
	if _, err := service.SaveLibraryView(SavedLibraryViewInput{Name: "坏", LibraryFilter: LibraryFilter{Missing: MetadataFieldThumbnail}}); !errors.Is(err, ErrInvalidMissingFilter) {
		t.Fatalf("save invalid missing error = %v", err)
	}
}

func TestRandomPlayIgnoresMissingFilter(t *testing.T) {
	request, err := normalizeRandomPlayRequest(RandomPlayRequest{Filter: LibraryFilter{Missing: MetadataFieldPoster, SmartView: LibraryViewFavorites}})
	if err != nil || request.Filter.Missing != "" || request.Filter.SmartView != LibraryViewFavorites {
		t.Fatalf("random request = %#v, %v", request, err)
	}
}

// mcImage 建一张「什么都不缺」的图片（EXIF 已解析、有拍摄时间与定位、有人工标签和人物），skip 里的项不补。
func mcImage(t *testing.T, path string, skip map[string]bool, mutate func(*models.Image)) models.Image {
	t.Helper()
	now := time.Now()
	lat, lng := 31.2, 121.5
	image := models.Image{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path), ExifParsedAt: &now, TakenAt: &now, GPSLatitude: &lat, GPSLongitude: &lng}
	if skip[MetadataFieldTakenAt] {
		image.TakenAt = nil
	}
	if skip[MetadataFieldGPS] {
		image.GPSLatitude, image.GPSLongitude = nil, nil
	}
	if mutate != nil {
		mutate(&image)
	}
	mcMust(t, database.DB.Create(&image).Error)
	if !skip[MetadataFieldTags] {
		tag := models.Tag{Name: "图片标签 " + path}
		mcMust(t, database.DB.Create(&tag).Error)
		mcMust(t, database.DB.Exec("INSERT INTO image_tags (image_id, tag_id) VALUES (?, ?)", image.ID, tag.ID).Error)
	}
	if !skip[MetadataFieldPeople] {
		person := models.Person{DisplayName: "图片人物 " + path}
		mcMust(t, database.DB.Create(&person).Error)
		mcMust(t, database.DB.Create(&models.ImagePerson{ImageID: image.ID, PersonID: person.ID}).Error)
	}
	return image
}

func TestImageCompletenessExcludesUnparsedExifAndFilterMatches(t *testing.T) {
	setupVideoServiceTestDB(t)
	mcImage(t, "/pics/full/a.jpg", nil, nil)
	lacking := map[string]models.Image{}
	for _, field := range imageMissingFieldOrder {
		lacking[field] = mcImage(t, "/pics/lack/"+field+".jpg", map[string]bool{field: true}, nil)
	}
	// 没解析过 EXIF：拍摄时间与定位都空，但不算缺，只计入「未解析」。
	mcImage(t, "/pics/raw/u.jpg", map[string]bool{MetadataFieldTakenAt: true, MetadataFieldGPS: true}, func(image *models.Image) { image.ExifParsedAt = nil })
	mcImage(t, "/pics/stale/s.jpg", map[string]bool{MetadataFieldTakenAt: true, MetadataFieldGPS: true, MetadataFieldTags: true, MetadataFieldPeople: true}, func(image *models.Image) { image.IsStale = true })

	result, err := NewMetadataCompletenessService(t.TempDir()).GetMetadataCompleteness()
	if err != nil {
		t.Fatal(err)
	}
	visible := int64(2 + len(imageMissingFieldOrder))
	if result.ImageTotal != visible || result.ImageExifUnparsed != 1 {
		t.Fatalf("image total=%d unparsed=%d", result.ImageTotal, result.ImageExifUnparsed)
	}
	for _, item := range result.Images {
		wantTotal := visible
		if item.Field == MetadataFieldTakenAt || item.Field == MetadataFieldGPS {
			wantTotal = visible - 1
		}
		if item.Missing != 1 || item.Total != wantTotal {
			t.Fatalf("image item %#v, want missing 1 total %d", item, wantTotal)
		}
	}
	library := NewImageLibraryService()
	for _, field := range imageMissingFieldOrder {
		page, err := library.SearchImagePage(ImagePageRequest{Filter: ImageFilter{Missing: field}, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Images) != 1 || page.Images[0].ID != lacking[field].ID {
			t.Fatalf("image missing=%s page = %#v", field, page.Images)
		}
	}
	if _, err := library.SearchImagePage(ImagePageRequest{Filter: ImageFilter{Missing: "bogus"}, Limit: 10}); !errors.Is(err, ErrInvalidMissingFilter) {
		t.Fatalf("image invalid missing error = %v", err)
	}
}

func TestMetadataCompletenessByDirectoryRanksAndLimits(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	thumbnails := filepath.Join(dataDir, "thumbnails")
	mcMust(t, database.DB.Create(&models.ScanDirectory{Path: "/mc"}).Error)
	noYear := map[string]bool{MetadataFieldYear: true, MetadataFieldThumbnail: true}
	mcVideo(t, thumbnails, "/mc/b/1.mp4", noYear, nil)
	mcVideo(t, thumbnails, "/mc/b/2.mp4", noYear, nil)
	mcVideo(t, thumbnails, "/mc/b/3.mp4", nil, nil)
	mcVideo(t, thumbnails, "/mc/a/1.mp4", noYear, nil)
	mcVideo(t, thumbnails, "/mc/c/1.mp4", noYear, nil)
	mcVideo(t, thumbnails, "/mc/d/1.mp4", nil, nil)
	mcVideo(t, thumbnails, "/mc/z/1.mp4", noYear, func(v *models.Video) { v.IsStale = true })
	service := NewMetadataCompletenessService(dataDir)
	want := []MetadataCompletenessDirectory{{Directory: "/mc/b", Missing: 2, Total: 3}, {Directory: "/mc/a", Missing: 1, Total: 1}, {Directory: "/mc/c", Missing: 1, Total: 1}}
	for _, field := range []string{MetadataFieldYear, MetadataFieldThumbnail} {
		rows, err := service.GetMetadataCompletenessByDirectory("video", field, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(rows, want) {
			t.Fatalf("%s directories = %#v", field, rows)
		}
		limited, err := service.GetMetadataCompletenessByDirectory("video", field, 2)
		if err != nil || !reflect.DeepEqual(limited, want[:2]) {
			t.Fatalf("%s limited = %#v, %v", field, limited, err)
		}
	}
	if rows, err := service.GetMetadataCompletenessByDirectory("video", MetadataFieldPoster, 10); err != nil || len(rows) != 0 {
		t.Fatalf("nothing missing = %#v, %v", rows, err)
	}
	// 图片的拍摄时间只在 EXIF 解析过的图片里算：目录分母不含未解析的那张。
	mcImage(t, "/pics/x/1.jpg", map[string]bool{MetadataFieldTakenAt: true}, nil)
	mcImage(t, "/pics/x/2.jpg", nil, nil)
	mcImage(t, "/pics/x/3.jpg", map[string]bool{MetadataFieldTakenAt: true}, func(image *models.Image) { image.ExifParsedAt = nil })
	rows, err := service.GetMetadataCompletenessByDirectory("image", MetadataFieldTakenAt, 10)
	if err != nil || !reflect.DeepEqual(rows, []MetadataCompletenessDirectory{{Directory: "/pics/x", Missing: 1, Total: 2}}) {
		t.Fatalf("image taken_at directories = %#v, %v", rows, err)
	}
	for _, bad := range [][2]string{{"video", MetadataFieldTakenAt}, {"image", MetadataFieldYear}, {"audio", MetadataFieldYear}, {"video", "bogus"}} {
		if _, err := service.GetMetadataCompletenessByDirectory(bad[0], bad[1], 10); !errors.Is(err, ErrInvalidMissingFilter) {
			t.Fatalf("%v error = %v", bad, err)
		}
	}
}
