package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

func jellyfinObject(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// Fileball's detail screen prints each stream's DisplayTitle under "视频:" / "音频:" and reads the
// list from the item's top-level MediaStreams; an item without them shows two empty lines.
func TestJellyfinItemDetailCarriesStreamDisplayTitles(t *testing.T) {
	s, token, first, second := jellyfinLibraryFixture(t)
	jellyfinProbeFixture(t, first)
	item := jellyfinObject(t, jellyfinRequest(s, "GET", "/Users/"+jellyfinUserID+"/Items/"+jellyfinID(jellyVideo, first.ID), token, ""))
	streams, _ := item["MediaStreams"].([]interface{})
	if len(streams) != 4 {
		t.Fatalf("MediaStreams %v", item["MediaStreams"])
	}
	expect := []struct{ kind, title string }{{"Video", "1080p HEVC SDR"}, {"Audio", "Dolby Digital+ - 6 ch - Default"}, {"Audio", "DTS - 6 ch - Default"}, {"EmbeddedImage", "MJPEG"}}
	for i, want := range expect {
		stream := streams[i].(map[string]interface{})
		if stream["Type"] != want.kind || stream["DisplayTitle"] != want.title {
			t.Errorf("stream %d: %v / %v", i, stream["Type"], stream["DisplayTitle"])
		}
	}
	if video := streams[0].(map[string]interface{}); video["AspectRatio"] != "16:9" || video["VideoRange"] != "SDR" || video["VideoRangeType"] != "SDR" {
		t.Errorf("video stream facts %v", video)
	}
	for key, want := range map[string]interface{}{"Width": 1920.0, "Height": 1080.0, "Container": "mkv", "IsHD": true, "AspectRatio": "16:9", "HasSubtitles": false, "VideoType": "VideoFile", "CanDelete": true, "CanDownload": true, "PlayAccess": "Full"} {
		if item[key] != want {
			t.Errorf("%s = %v, want %v", key, item[key], want)
		}
	}
	sources := item["MediaSources"].([]interface{})
	sourceStreams := sources[0].(map[string]interface{})["MediaStreams"].([]interface{})
	if sourceStreams[0].(map[string]interface{})["DisplayTitle"] != "1080p HEVC SDR" {
		t.Errorf("MediaSource streams must carry the same DisplayTitle: %v", sourceStreams[0])
	}
	// A sidecar .srt shows up as an external subtitle stream and flips HasSubtitles.
	if err := os.WriteFile(jellyfinSubtitlePath(first), []byte("1\n00:00:00,000 --> 00:00:01,000\nhi\n"), 0600); err != nil {
		t.Fatal(err)
	}
	item = jellyfinObject(t, jellyfinRequest(s, "GET", "/Users/"+jellyfinUserID+"/Items/"+jellyfinID(jellyVideo, first.ID), token, ""))
	streams, _ = item["MediaStreams"].([]interface{})
	if item["HasSubtitles"] != true || len(streams) != 5 {
		t.Fatalf("sidecar subtitle: HasSubtitles=%v streams=%d", item["HasSubtitles"], len(streams))
	}
	if external := streams[4].(map[string]interface{}); external["Type"] != "Subtitle" || external["IsExternal"] != true || external["DisplayTitle"] == "" || external["Index"] != 4.0 {
		t.Errorf("external subtitle stream %v", external)
	}
	// Unprobed videos still answer with an empty list and no invented dimensions.
	item = jellyfinObject(t, jellyfinRequest(s, "GET", "/Users/"+jellyfinUserID+"/Items/"+jellyfinID(jellyVideo, second.ID), token, ""))
	if streams, ok := item["MediaStreams"].([]interface{}); !ok || len(streams) != 0 {
		t.Errorf("unprobed MediaStreams %v", item["MediaStreams"])
	}
	if _, ok := item["Width"]; ok {
		t.Errorf("unprobed video must not report Width: %v", item["Width"])
	}
	// The list endpoint shares the DTO, so search and browse results carry the same facts.
	items, _ := jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?ParentId="+jellyfinID(jellyView, 1)+"&Ids="+jellyfinID(jellyVideo, first.ID), token, ""))
	if len(items) != 1 || items[0]["Container"] != "mkv" || items[0]["CanDelete"] != true {
		t.Errorf("list DTO %v", items)
	}
}

func TestJellyfinStreamDisplayTitleFollowsJellyfin(t *testing.T) {
	width, height, channels, hdr := 1080, 1920, 2, true
	cases := []struct {
		row   models.MediaStream
		kind  string
		title string
	}{
		{models.MediaStream{StreamType: "video", CodecName: "h264", Width: &width, Height: &height}, "Video", "1080p H264"},
		{models.MediaStream{StreamType: "video", CodecName: "hevc", Width: &width, Height: &height, IsHDR: &hdr}, "Video", "1080p HEVC HDR"},
		{models.MediaStream{StreamType: "video", CodecName: "av1", Title: "Main feature"}, "Video", "Main feature - AV1"},
		{models.MediaStream{StreamType: "audio", CodecName: "aac", ChannelLayout: "stereo", Language: "eng", IsDefault: true}, "Audio", "English - AAC - Stereo - Default"},
		{models.MediaStream{StreamType: "audio", CodecName: "ac3", Channels: &channels, Language: "und"}, "Audio", "Dolby Digital - 2 ch"},
		{models.MediaStream{StreamType: "audio", CodecName: "dts", Profile: "DTS-HD MA", ChannelLayout: "5.1"}, "Audio", "DTS-HD MA - 5.1"},
		{models.MediaStream{StreamType: "audio", CodecName: "aac", Title: "Commentary", Language: "chi"}, "Audio", "Commentary - Chinese - AAC"},
		{models.MediaStream{StreamType: "subtitle", CodecName: "subrip", Language: "jpn", IsDefault: true}, "Subtitle", "Japanese - Default - SUBRIP"},
		{models.MediaStream{StreamType: "subtitle", CodecName: "ass"}, "Subtitle", "Und - ASS"},
	}
	for _, c := range cases {
		if got := jellyfinStreamDisplayTitle(c.row, c.kind, false); got != c.title {
			t.Errorf("%s %s: got %q want %q", c.kind, c.row.CodecName, got, c.title)
		}
	}
	for _, c := range []struct {
		w, h  int
		label string
	}{{1920, 1080, "1080p"}, {1080, 1920, "1080p"}, {3840, 2160, "4K"}, {2160, 3840, "4K"}, {2560, 1440, "1440p"}, {1280, 720, "720p"}, {720, 1280, "720p"}, {854, 480, "480p"}, {0, 0, ""}} {
		if got := jellyfinResolutionText(c.w, c.h); got != c.label {
			t.Errorf("%dx%d: %q want %q", c.w, c.h, got, c.label)
		}
	}
	if got := jellyfinAspectRatio(1080, 1920); got != "9:16" {
		t.Errorf("aspect %q", got)
	}
	if got := jellyfinFrameRate("30000/1001"); got == nil || got.(float64) < 29.9 || got.(float64) > 30 {
		t.Errorf("frame rate %v", got)
	}
	if got := jellyfinFrameRate("0/0"); got != nil {
		t.Errorf("unknown frame rate must be nil, got %v", got)
	}
}

// Search/Hints is Jellyfin's search endpoint; the alphabet-index and item-kind parameters arrive
// alongside SearchTerm from clients such as Fileball and must narrow, not 400.
func TestJellyfinSearchHintsAndSemanticFilters(t *testing.T) {
	s, token, first, second := jellyfinLibraryFixture(t)
	for _, tag := range []string{"alpha", "beta"} {
		if err := database.DB.Create(&models.Tag{Name: tag, Color: "#ffffff"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	hints := func(query string) ([]interface{}, float64) {
		body := jellyfinObject(t, jellyfinRequest(s, "GET", "/Search/Hints?"+query, token, ""))
		list, _ := body["SearchHints"].([]interface{})
		total, _ := body["TotalRecordCount"].(float64)
		return list, total
	}
	list, total := hints("SearchTerm=FIRST&IncludeItemTypes=Movie,Series,Episode&Limit=10&IncludePeople=false&IncludeMedia=true&IncludeGenres=false&IncludeStudios=false&IncludeArtists=false&UserId=" + jellyfinUserID)
	if total != 1 || len(list) != 1 {
		t.Fatalf("hints %v total %v", list, total)
	}
	hint := list[0].(map[string]interface{})
	if hint["ItemId"] != jellyfinID(jellyVideo, first.ID) || hint["Id"] != hint["ItemId"] || hint["Type"] != "Movie" || hint["MatchedTerm"] != "FIRST" || hint["IsFolder"] != false || hint["MediaType"] != "Video" || hint["PrimaryImageTag"] == nil {
		t.Errorf("hint %v", hint)
	}
	if list, total = hints("SearchTerm=second&IncludeItemTypes=Movie"); total != 1 || len(list) != 1 {
		t.Errorf("file-name search %v", list)
	}
	if list, total = hints("SearchTerm=zzz"); total != 0 || len(list) != 0 {
		t.Errorf("no match %v", list)
	}
	if list, total = hints("SearchTerm=first&IncludeMedia=false"); total != 0 || len(list) != 0 {
		t.Errorf("IncludeMedia=false must exclude videos: %v", list)
	}
	if list, _ = hints("SearchTerm=alp&IncludeItemTypes=BoxSet"); len(list) != 0 {
		t.Errorf("collections search should find nothing here: %v", list)
	}
	collection, err := NewCollectionService(t.TempDir()).CreateCollection("Alpha Set", "")
	if err != nil {
		t.Fatal(err)
	}
	list, total = hints("SearchTerm=alpha&IncludeItemTypes=BoxSet")
	if total != 1 || len(list) != 1 {
		t.Fatalf("collection hint %v", list)
	}
	if folder := list[0].(map[string]interface{}); folder["ItemId"] != jellyfinID(jellyCollection, collection.ID) || folder["Type"] != "BoxSet" || folder["IsFolder"] != true || folder["MediaType"] != nil || folder["RunTimeTicks"] != nil {
		t.Errorf("folder hint %v", folder)
	}
	for _, query := range []string{"Limit=10", "SearchTerm=%20", "SearchTerm=first&IncludeMedia=maybe", "SearchTerm=first&Unknown=1"} {
		if w := jellyfinRequest(s, "GET", "/Search/Hints?"+query, token, ""); w.Code != 400 {
			t.Errorf("%s: %d %s", query, w.Code, w.Body)
		}
	}
	view := jellyfinID(jellyView, 1)
	for query, want := range map[string]int{"IsMovie=true": 2, "IsMovie=false": 0, "IsSeries=true": 0, "IsSeries=false": 2, "IsNews=true": 0, "IsKids=false": 2, "IsSports=true": 0, "NameStartsWith=fi": 1, "NameStartsWith=F": 1, "NameStartsWith=%25": 0, "NameStartsWithOrGreater=s": 1, "NameLessThan=s": 1, "NameStartsWithOrGreater=a&NameLessThan=z": 2, "SearchTerm=first&IsMovie=true": 1} {
		items, total := jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?ParentId="+view+"&"+query, token, ""))
		if total != int(want) || len(items) != want {
			t.Errorf("%s: total=%d items=%d want %d", query, total, len(items), want)
		}
	}
	items, _ := jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?ParentId="+view+"&NameStartsWithOrGreater=s", token, ""))
	if len(items) != 1 || items[0]["Id"] != jellyfinID(jellyVideo, second.ID) {
		t.Errorf("NameStartsWithOrGreater picked %v", items)
	}
	if w := jellyfinRequest(s, "GET", "/Items?ParentId="+view+"&IsMovie=x", token, ""); w.Code != 400 {
		t.Errorf("IsMovie=x accepted: %d", w.Code)
	}
	// Folder lists honour the same parameters: folders are never movies, names filter in memory.
	for query, want := range map[string]int{"IsMovie=true": 0, "IsMovie=false": 2, "NameStartsWith=a": 1, "NameStartsWithOrGreater=b": 1, "NameLessThan=b": 1, "SearchTerm=ETA": 1} {
		items, total := jellyfinItems(t, jellyfinRequest(s, "GET", "/Genres?"+query, token, ""))
		if total != int(want) || len(items) != want {
			t.Errorf("genres %s: total=%d items=%d want %d", query, total, len(items), want)
		}
	}
}

// Detail screens request extras Jellyfin answers with empty collections; a 404 here makes clients
// treat the whole item as broken.
func TestJellyfinDetailExtrasAreEmptyNotMissing(t *testing.T) {
	s, token, first, _ := jellyfinLibraryFixture(t)
	id := jellyfinID(jellyVideo, first.ID)
	for _, path := range []string{"/Users/" + jellyfinUserID + "/Items/" + id + "/SpecialFeatures", "/Users/" + jellyfinUserID + "/Items/" + id + "/LocalTrailers", "/Items/" + id + "/ThemeSongs"} {
		w := jellyfinRequest(s, "GET", path, token, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var list []interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || list == nil || len(list) != 0 {
			t.Errorf("%s: %s", path, w.Body)
		}
	}
	for _, path := range []string{"/Items/" + id + "/Similar?Limit=12&Fields=PrimaryImageAspectRatio", "/Movies/" + id + "/Similar", "/Users/" + jellyfinUserID + "/Items/" + id + "/Intros"} {
		items, total := jellyfinItems(t, jellyfinRequest(s, "GET", path, token, ""))
		if total != 0 || len(items) != 0 {
			t.Errorf("%s: %v", path, items)
		}
	}
	if w := jellyfinRequest(s, "GET", "/Items/not-an-id/Similar", token, ""); w.Code != 400 {
		t.Errorf("invalid id accepted: %d", w.Code)
	}
	if w := jellyfinRequest(s, "GET", "/Items/"+jellyfinID(jellyVideo, first.ID+99)+"/Similar", token, ""); w.Code != 404 {
		t.Errorf("extras for an unknown item must be 404: %d", w.Code)
	}
	if w := jellyfinRequest(s, "GET", "/Items/"+jellyfinID(jellyView, 1)+"/SpecialFeatures", token, ""); w.Code != 200 {
		t.Errorf("extras for a visible folder: %d", w.Code)
	}
	if w := jellyfinRequest(s, "GET", "/Items/"+id+"/Unknown", token, ""); w.Code != 404 {
		t.Errorf("unknown extra must stay 404: %d", w.Code)
	}
}

// DELETE Items/{id} moves the file to the library trash through VideoService (V1.0.3); the
// desktop trash view can restore it. Folders, views and tags are not deletable.
func TestJellyfinDeleteMovesVideoToTrash(t *testing.T) {
	s, token, first, second := jellyfinLibraryFixture(t)
	user := jellyfinObject(t, jellyfinRequest(s, "GET", "/Users/Me", token, ""))
	if policy := user["Policy"].(map[string]interface{}); policy["EnableContentDeletion"] != true {
		t.Fatalf("clients gate delete on EnableContentDeletion: %v", policy)
	}
	target := "/Items/" + jellyfinID(jellyVideo, second.ID)
	if w := jellyfinRequest(s, "DELETE", target, "", ""); w.Code != 401 {
		t.Fatalf("anonymous delete: %d", w.Code)
	}
	for _, folder := range []string{jellyfinID(jellyView, 1), jellyfinID(jellyGroup, 2)} {
		if w := jellyfinRequest(s, "DELETE", "/Items/"+folder, token, ""); w.Code != 403 {
			t.Errorf("folder delete %s: %d", folder, w.Code)
		}
	}
	if w := jellyfinRequest(s, "DELETE", "/Items/not-an-id", token, ""); w.Code != 400 {
		t.Errorf("invalid id: %d", w.Code)
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	if w := jellyfinRequest(s, "DELETE", target, token, ""); w.Code != 204 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	// Deletions are always in the request log, with the item ID masked like every other route.
	if out := logs.String(); !strings.Contains(out, "DELETE /items/{id} 204") || strings.Contains(out, jellyfinID(jellyVideo, second.ID)) {
		t.Errorf("delete request log: %s", out)
	}
	if _, err := os.Stat(second.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("original should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(second.Path), DefaultTrashDirName, filepath.Base(second.Path))); err != nil {
		t.Errorf("file should sit in the trash folder: %v", err)
	}
	var entries int64
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("video_id = ?", second.ID).Count(&entries).Error; err != nil || entries != 1 {
		t.Errorf("trash entry count %d err %v", entries, err)
	}
	if w := jellyfinRequest(s, "GET", "/Users/"+jellyfinUserID+target, token, ""); w.Code != 404 {
		t.Errorf("deleted item still visible: %d", w.Code)
	}
	if w := jellyfinRequest(s, "DELETE", target, token, ""); w.Code != 404 {
		t.Errorf("second delete: %d", w.Code)
	}
	items, total := jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?ParentId="+jellyfinID(jellyView, 1), token, ""))
	if total != 1 || len(items) != 1 || items[0]["Id"] != jellyfinID(jellyVideo, first.ID) {
		t.Errorf("remaining %v", items)
	}
	// Revoked sessions cannot delete even inside the same process lifetime.
	s.Stop()
	s.mu.Lock()
	s.config.JellyfinEnabled = true
	s.mu.Unlock()
	if w := jellyfinRequest(s, "DELETE", "/Items/"+jellyfinID(jellyVideo, first.ID), token, ""); w.Code != 401 {
		t.Errorf("revoked token delete: %d", w.Code)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Errorf("first must survive the revoked delete: %v", err)
	}
}
