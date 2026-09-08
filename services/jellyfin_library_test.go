package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"gorm.io/gorm/logger"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

func jellyfinLibraryFixture(t *testing.T) (*JellyfinServer, string, models.Video, models.Video) {
	t.Helper()
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	first := models.Video{Name: "first.mkv", DisplayTitle: "First title", Path: filepath.Join(root, "first.mkv"), Directory: root, Duration: 100, Size: 10}
	second := models.Video{Name: "second.mp4", Path: filepath.Join(root, "second.mp4"), Directory: root, Duration: 100, Size: 10, IsFavorite: true}
	for _, video := range []*models.Video{&first, &second} {
		if err := os.WriteFile(video.Path, []byte("0123456789"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := jellyfinTestServer(t)
	return s, jellyfinLogin(t, s), first, second
}
func jellyfinItems(t *testing.T, w *httptest.ResponseRecorder) ([]map[string]interface{}, int) {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("query %d %s", w.Code, w.Body)
	}
	var result struct {
		Items            []map[string]interface{}
		TotalRecordCount int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Items == nil {
		t.Fatal("Items must be []")
	}
	return result.Items, result.TotalRecordCount
}
func TestJellyfinBrowsePagingAndVisibility(t *testing.T) {
	s, token, first, _ := jellyfinLibraryFixture(t)
	for _, v := range []models.Video{{Name: "stale", Path: filepath.Join(first.Directory, "stale.mp4"), Directory: first.Directory, IsStale: true}, {Name: "outside", Path: "/outside/test.mp4", Directory: "/outside"}} {
		if err := database.DB.Create(&v).Error; err != nil {
			t.Fatal(err)
		}
	}
	items, total := jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?Limit=1&SortBy=SortName", token, ""))
	if len(items) != 1 || total != 2 || items[0]["Name"] != "First title" {
		t.Fatalf("unexpected items %v total %d", items, total)
	}
	items, total = jellyfinItems(t, jellyfinRequest(s, "GET", "/Users/"+jellyfinUserID+"/Items?StartIndex=1&Limit=1&SortBy=SortName", token, ""))
	if len(items) != 1 || total != 2 || items[0]["Name"] != "second.mp4" {
		t.Fatalf("page 2 %v", items)
	}
	items, total = jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?StartIndex=2", token, ""))
	if len(items) != 0 || total != 2 {
		t.Fatal("final page")
	}
	items, _ = jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?Limit=0", token, ""))
	if len(items) != 0 {
		t.Fatal("zero limit")
	}
	for _, query := range []string{"Limit=-1", "SortOrder=garbage", "IsFavorite=invalid", "Recursive=maybe", "Unknown=1", "StartIndex=999999999999999999999999"} {
		if w := jellyfinRequest(s, "GET", "/Items?"+query, token, ""); w.Code != 400 {
			t.Fatalf("accepted %s: %d", query, w.Code)
		}
	}
	// Unknown sort keys are ignored (D-02 V1.0.2); only the column table can reach ORDER BY.
	for _, query := range []string{"SortBy=name%3BDELETE", "Limit=0&SortBy=unsupported"} {
		if w := jellyfinRequest(s, "GET", "/Items?"+query, token, ""); w.Code != 200 {
			t.Fatalf("rejected %s: %d %s", query, w.Code, w.Body)
		}
	}
	w := jellyfinRequest(s, "GET", "/Items/"+jellyfinID(jellyVideo, first.ID), token, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), first.Directory) {
		t.Fatalf("detail leaked path: %s", w.Body)
	}
	if err := database.DB.Delete(&first).Error; err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/Items/" + jellyfinID(jellyVideo, first.ID), "/Videos/" + jellyfinID(jellyVideo, first.ID) + "/stream"} {
		if w := jellyfinRequest(s, "GET", path, token, ""); w.Code != 404 {
			t.Fatalf("deleted accessible %d", w.Code)
		}
	}
	if err := database.DB.Where("1 = 1").Delete(&models.ScanDirectory{}).Error; err != nil {
		t.Fatal(err)
	}
	items, _ = jellyfinItems(t, jellyfinRequest(s, "GET", "/Items", token, ""))
	if len(items) != 0 {
		t.Fatal("orphan library exposed")
	}
}
func TestJellyfinTagsCollectionsAndSavedViewIntersection(t *testing.T) {
	s, token, first, second := jellyfinLibraryFixture(t)
	tag := models.Tag{Name: "tag", Color: "#ffffff"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&first).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	collection, err := NewCollectionService(t.TempDir()).CreateCollection("collection", "")
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []uint{second.ID, first.ID} {
		if err := database.DB.Create(&models.CollectionVideo{CollectionID: collection.ID, VideoID: id, Position: i + 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	view, err := s.video.SaveLibraryView(SavedLibraryViewInput{Name: "saved", LibraryFilter: LibraryFilter{TagIDs: []uint{tag.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	views, _ := jellyfinItems(t, jellyfinRequest(s, "GET", "/Users/"+jellyfinUserID+"/Views", token, ""))
	if len(views) != 10 {
		t.Fatalf("views %d", len(views))
	}
	items, total := jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?ParentId="+jellyfinID(jellyTag, tag.ID), token, ""))
	if total != 1 || items[0]["Id"] != jellyfinID(jellyVideo, first.ID) {
		t.Fatal(items)
	}
	items, total = jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?ParentId="+jellyfinID(jellyCollection, collection.ID), token, ""))
	if total != 2 || items[0]["Id"] != jellyfinID(jellyVideo, second.ID) {
		t.Fatal("collection order", items)
	}
	items, total = jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?ParentId="+jellyfinID(jellySaved, view.ID)+"&SearchTerm=second", token, ""))
	if total != 0 || len(items) != 0 {
		t.Fatal("saved filter lost")
	}
	if err := database.DB.Delete(collection).Error; err != nil {
		t.Fatal(err)
	}
	if w := jellyfinRequest(s, "GET", "/Items?ParentId="+jellyfinID(jellyCollection, collection.ID), token, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestJellyfinOriginalRangeSubtitleAndWatchState(t *testing.T) {
	s, token, first, _ := jellyfinLibraryFixture(t)
	id := jellyfinID(jellyVideo, first.ID)
	r := httptest.NewRequest("GET", "/Videos/"+id+"/stream?Static=true&api_key="+token, nil)
	r.RemoteAddr = "127.0.0.1:2"
	r.Header.Set("Range", "bytes=2-5")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "2345" || w.Header().Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("range %d %s", w.Code, w.Body)
	}
	w = jellyfinRequest(s, "HEAD", "/Videos/"+id+"/stream", token, "")
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "10" {
		t.Fatal("HEAD")
	}
	subtitle := jellyfinSubtitlePath(first)
	if err := os.WriteFile(subtitle, []byte("1\n00:00:01,000 --> 00:00:02,000\nhello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w = jellyfinRequest(s, "GET", "/Videos/"+id+"/"+id+"/Subtitles/0/Stream.srt", token, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "hello") {
		t.Fatalf("subtitle %d %s", w.Code, w.Body)
	}
	for _, event := range []struct {
		endpoint string
		ticks    int64
	}{{"Playing", 20e7}, {"Playing/Progress", 40e7}, {"Playing/Stopped", 30e7}} {
		w = jellyfinRequest(s, "POST", "/Sessions/"+event.endpoint, token, fmt.Sprintf(`{"ItemId":%q,"PositionTicks":%d}`, id, event.ticks))
		if w.Code != 204 {
			t.Fatalf("progress %d %s", w.Code, w.Body)
		}
	}
	w = jellyfinRequest(s, "POST", "/Users/"+jellyfinUserID+"/FavoriteItems/"+id, token, "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var video models.Video
	database.DB.First(&video, first.ID)
	if video.WatchPositionSeconds != 30 || !video.IsFavorite || video.PlayCount != 0 {
		t.Fatalf("state %+v", video)
	}
	w = jellyfinRequest(s, "DELETE", "/Users/"+jellyfinUserID+"/FavoriteItems/"+id, token, "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = jellyfinRequest(s, "POST", "/Sessions/Playing/Progress", token, fmt.Sprintf(`{"ItemId":%q,"PositionTicks":-1}`, id))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w = jellyfinRequest(s, "POST", "/Sessions/Playing/Stopped", token, fmt.Sprintf(`{"ItemId":%q,"PositionTicks":1000000000}`, id))
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	database.DB.First(&video, first.ID)
	if !video.IsWatched || video.IsFavorite || video.WatchPositionSeconds != 100 {
		t.Fatalf("completed %+v", video)
	}
	var events int64
	database.DB.Model(&models.PlayEvent{}).Count(&events)
	if events != 0 {
		t.Fatal("progress changed play ledger")
	}
	if err := os.Remove(first.Path); err != nil {
		t.Fatal(err)
	}
	if w := jellyfinRequest(s, "GET", "/Videos/"+id+"/stream", token, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestJellyfinPlaybackInfoUsesFreshMetadataAndNoTranscode(t *testing.T) {
	s, token, first, _ := jellyfinLibraryFixture(t)
	id := jellyfinID(jellyVideo, first.ID)
	info, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	size, mtime := info.Size(), info.ModTime().UnixNano()
	if err := database.DB.Create(&models.VideoTechnicalMetadata{VideoID: first.ID, SuccessfulSourceSize: &size, SuccessfulSourceModTimeNS: &mtime}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.MediaStream{VideoID: first.ID, StreamIndex: 0, StreamType: "video", CodecName: "hevc"}).Error; err != nil {
		t.Fatal(err)
	}
	w := jellyfinRequest(s, "POST", "/Items/"+id+"/PlaybackInfo", token, `{"EnableDirectPlay":true,"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"mkv","VideoCodec":"hevc"}]}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("playback %d %s", w.Code, w.Body)
	}
	var result struct {
		MediaSources  []map[string]interface{}
		PlaySessionId string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.MediaSources) != 1 || result.MediaSources[0]["SupportsTranscoding"] != false || result.MediaSources[0]["SupportsDirectPlay"] != true || len(result.PlaySessionId) != 32 {
		t.Fatal(result)
	}
	if strings.Contains(w.Body.String(), first.Directory) {
		t.Fatal("absolute path leak")
	}
	w = jellyfinRequest(s, "POST", "/Items/"+id+"/PlaybackInfo", token, `{"EnableDirectPlay":false}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "NoCompatibleStream") {
		t.Fatal("false capability")
	}
	if err := os.WriteFile(first.Path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	w = jellyfinRequest(s, "POST", "/Items/"+id+"/PlaybackInfo", token, `{}`)
	if w.Code != 503 {
		t.Fatal("stale snapshot used", w.Code)
	}
}

func TestReviewJellyfinCapabilities(t *testing.T) {
	s, token, v, _ := jellyfinLibraryFixture(t)
	info, _ := os.Stat(v.Path)
	size, mod := info.Size(), info.ModTime().UnixNano()
	if err := database.DB.Create(&models.VideoTechnicalMetadata{VideoID: v.ID, SuccessfulSourceSize: &size, SuccessfulSourceModTimeNS: &mod}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.MediaStream{VideoID: v.ID, StreamIndex: 0, StreamType: "video", CodecName: "hevc"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct{ query, body string }{
		{"", `{"EnableDirectPlay":true,"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"mp4","VideoCodec":"h264","AudioCodec":"aac"}]}}`},
		{"?EnableDirectPlay=false", `{}`},
	} {
		w := jellyfinRequest(s, "POST", "/Items/"+jellyfinID(jellyVideo, v.ID)+"/PlaybackInfo"+request.query, token, request.body)
		if w.Code == 200 && strings.Contains(w.Body.String(), `"SupportsDirectPlay":true`) {
			t.Errorf("incompatible request still advertises direct play: query=%q body=%s", request.query, request.body)
		}
	}
}
func TestReviewJellyfinFilterContract(t *testing.T) {
	s, token, v, _ := jellyfinLibraryFixture(t)
	if _, err := s.video.UpdateVideoWatchProgress(v.ID, 25, false); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"ExcludeItemIds=" + jellyfinID(jellyVideo, v.ID), "IsResumable=false"} {
		w := jellyfinRequest(s, "GET", "/Items?"+query, token, "")
		if w.Code == 400 {
			continue
		}
		items, _ := jellyfinItems(t, w)
		for _, item := range items {
			if item["Id"] == jellyfinID(jellyVideo, v.ID) {
				t.Errorf("filter ignored: %s", query)
			}
		}
	}
	w := jellyfinRequest(s, "GET", "/Items?ParentId="+jellyfinID(jellyGroup, 2)+"&SortOrder=sideways", token, "")
	if w.Code != 400 {
		t.Errorf("invalid folder sort order accepted: %d", w.Code)
	}
	tag := models.Tag{Name: "folder-type-check"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	items, total := jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?ParentId="+jellyfinID(jellyGroup, 2)+"&IncludeItemTypes=Movie", token, ""))
	if len(items) != 0 || total != 0 {
		t.Fatal("folder result ignored requested item type")
	}
}
func TestReviewJellyfinReadScansOutsideScope(t *testing.T) {
	s, token, _, _ := jellyfinLibraryFixture(t)
	outside := models.Video{Name: "outside.mp4", Path: filepath.Join(t.TempDir(), "outside.mp4")}
	if err := database.DB.Create(&outside).Error; err != nil {
		t.Fatal(err)
	}
	view, err := s.video.SaveLibraryView(SavedLibraryViewInput{Name: "no subtitles", LibraryFilter: LibraryFilter{SmartView: LibraryViewNoSubtitle}})
	if err != nil {
		t.Fatal(err)
	}
	w := jellyfinRequest(s, "GET", "/Items?ParentId="+jellyfinID(jellySaved, view.ID), token, "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var n int64
	database.DB.Model(&models.SubtitleIndexState{}).Where("video_id = ?", outside.ID).Count(&n)
	if n != 0 {
		t.Errorf("read request mutated subtitle index for out-of-scope video: %d rows", n)
	}
}
func TestReviewJellyfinHashLogOnSaveFailure(t *testing.T) {
	setupVideoServiceTestDB(t)
	if database.DB.Dialector.Name() != "sqlite" {
		t.Skip("SQLite trigger reproduction")
	}
	if err := database.DB.Exec(`CREATE TRIGGER review_reject_jellyfin BEFORE UPDATE ON settings BEGIN SELECT RAISE(ABORT, 'denied'); END;`).Error; err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	database.DB.Logger = logger.New(log.New(&buf, "", 0), logger.Config{LogLevel: logger.Warn})
	s := NewJellyfinServer(&VideoService{}, nil, nil)
	_, err := s.Configure(JellyfinConfigInput{Username: "viewer", Password: "test-password"})
	if err == nil {
		t.Fatal("expected rejected write")
	}
	if strings.Contains(buf.String(), "$2a$") || strings.Contains(buf.String(), "$2b$") {
		t.Error("failed Configure wrote bcrypt credential hash into SQL log")
	}
}
