package services

import (
	"bytes"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

// Query strings below were recorded from Infuse (jellyfin/jellyfin#7921) and Jellyfin Web.
func TestJellyfinAcceptsRealClientQueries(t *testing.T) {
	s, token, first, second := jellyfinLibraryFixture(t)
	view := jellyfinID(jellyView, 1)
	for _, query := range []string{
		"ExcludeLocationTypes=Virtual&Fields=DateCreated,Etag,Genres,MediaSources,Overview,ParentId,Path,People,ProviderIds,SortName&ParentId=" + view + "&StartIndex=0&Limit=200&IncludeItemTypes=Movie&Recursive=true&CollapseBoxSetItems=false",
		"SortBy=SortName,ProductionYear&SortOrder=Ascending&IncludeItemTypes=Movie&Recursive=true&Fields=PrimaryImageAspectRatio,MediaSourceCount&ImageTypeLimit=1&EnableImageTypes=Primary,Backdrop,Banner,Thumb&StartIndex=0&Limit=100&ParentId=" + view,
		"ParentId=" + view + "&Fields=Overview&Fields=Path&MediaTypes=Video&IsMissing=false&Filters=IsNotFolder&LocationTypes=FileSystem",
		"ParentId=" + view + "&SortBy=DatePlayed,SortName&SortOrder=Descending,Ascending",
		"ParentId=" + view + "&SortBy=Random",
		"ParentId=" + view + "&SortBy=IsFolder,SortName&SortOrder=Ascending&UserId=" + jellyfinUserID,
		"ParentId=" + view + "&IncludeItemTypes=Movie&IncludeItemTypes=Series&Recursive=true&EnableImages=true&enableimages=true",
		"ParentId=" + view + "&Filters=IsNotFolder,%20IsUnplayed",
		"parentid=" + view + "&Recursive=true",
	} {
		items, total := jellyfinItems(t, jellyfinRequest(s, "GET", "/Users/"+jellyfinUserID+"/Items?"+query, token, ""))
		if total != 2 || len(items) != 2 {
			t.Errorf("%s: total=%d items=%d", query, total, len(items))
		}
	}
	// Server-side overrides replace client case variants instead of colliding with them.
	for _, path := range []string{"/Users/" + jellyfinUserID + "/Items/Latest?sortby=Name&Limit=16", "/Users/" + jellyfinUserID + "/Items/Resume?isresumable=false&Limit=12", "/Items?ParentId=" + view + "&SortBy=ProductionYear&sortby=Name&SortOrder=Ascending&SortOrder=Descending"} {
		if w := jellyfinRequest(s, "GET", path, token, ""); w.Code != 200 {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	// Scope parameters this model can only satisfy trivially still narrow correctly.
	for _, query := range []string{"MediaTypes=Audio", "IsMissing=true", "ExcludeLocationTypes=FileSystem", "LocationTypes=Virtual", "ExcludeItemTypes=Movie", "Filters=IsFolder", "IncludeItemTypes=Series"} {
		items, total := jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?ParentId="+view+"&"+query, token, ""))
		if total != 0 || len(items) != 0 {
			t.Errorf("%s should be empty, got %d", query, total)
		}
	}
	for _, query := range []string{"Unknown=1", "SortOrder=Ascending,Sideways", "Recursive=maybe", "CollapseBoxSetItems=x", "IsMissing=x", "Limit=1&limit=2", "Recursive=true&Recursive=false", "EnableImages=true&EnableImages=false", "UserId=" + jellyfinUserID + "&userid=ff000000000000000000000000000002"} {
		if w := jellyfinRequest(s, "GET", "/Items?ParentId="+view+"&"+query, token, ""); w.Code != 400 && w.Code != 403 {
			t.Errorf("%s accepted: %d", query, w.Code)
		}
	}
	// SortOrder pairs with SortBy by position; ignored keys keep their position and repeated keys merge.
	for _, entry := range []struct {
		query string
		first uint
	}{
		{"SortBy=ProductionYear,Name&SortOrder=Ascending,Descending", second.ID},
		{"SortBy=Name&SortOrder=Ascending,Descending", first.ID},
		{"SortBy=ProductionYear,Name&SortOrder=Descending", second.ID},
		{"SortBy=ProductionYear&SortBy=Name&SortOrder=Ascending&SortOrder=Descending", second.ID},
		{"SortBy=Name", first.ID},
	} {
		items, _ := jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?ParentId="+view+"&"+entry.query, token, ""))
		if len(items) != 2 || items[0]["Id"] != jellyfinID(jellyVideo, entry.first) {
			t.Errorf("%s: unexpected order %v", entry.query, items)
		}
	}
}

func TestJellyfinGroupRecursiveReturnsVideos(t *testing.T) {
	s, token, first, second := jellyfinLibraryFixture(t)
	tag := models.Tag{Name: "tagged", Color: "#ffffff"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&first).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	collection, err := NewCollectionService(t.TempDir()).CreateCollection("boxset", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.CollectionVideo{CollectionID: collection.ID, VideoID: second.ID, Position: 1}).Error; err != nil {
		t.Fatal(err)
	}
	stale := models.Video{Name: "stale.mp4", Path: filepath.Join(first.Directory, "stale.mp4"), Directory: first.Directory, IsStale: true}
	if err := database.DB.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&stale).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	tags, sets := jellyfinID(jellyGroup, 2), jellyfinID(jellyGroup, 1)
	list := func(query string) ([]map[string]interface{}, int) {
		return jellyfinItems(t, jellyfinRequest(s, "GET", "/Users/"+jellyfinUserID+"/Items?"+query, token, ""))
	}
	items, total := list("ParentId=" + tags + "&Recursive=true&IncludeItemTypes=Movie,Series,BoxSet&Fields=Path&ExcludeLocationTypes=Virtual")
	if total != 1 || items[0]["Id"] != jellyfinID(jellyVideo, first.ID) {
		t.Fatalf("recursive tags: %d %v", total, items)
	}
	if items, total = list("ParentId=" + sets + "&Recursive=true"); total != 1 || items[0]["Id"] != jellyfinID(jellyVideo, second.ID) {
		t.Fatalf("recursive collections: %d %v", total, items)
	}
	for _, query := range []string{"ParentId=" + tags, "ParentId=" + tags + "&Recursive=false", "ParentId=" + tags + "&IsFavorite=false&Filters=IsUnplayed&SortBy=DateCreated&SortOrder=Descending"} {
		if items, total = list(query); total != 1 || items[0]["Type"] != "Folder" || items[0]["Id"] != jellyfinID(jellyTag, tag.ID) {
			t.Fatalf("tag folders %s: %d %v", query, total, items)
		}
	}
	if items, total = list("ParentId=" + sets + "&Recursive=true&IncludeItemTypes=BoxSet"); total != 1 || items[0]["Type"] != "BoxSet" {
		t.Fatalf("boxset folders: %d %v", total, items)
	}
	// Folder DTOs are never favourite/played, so those selections are empty; ID selections are rejected.
	for _, query := range []string{"ExcludeItemTypes=BoxSet", "MediaTypes=Video", "Filters=IsNotFolder", "Filters=IsFavorite", "IsFavorite=true", "IsPlayed=true"} {
		if _, total = list("ParentId=" + sets + "&" + query); total != 0 {
			t.Fatalf("%s on folders returned %d", query, total)
		}
	}
	if w := jellyfinRequest(s, "GET", "/Items?ParentId="+sets+"&Ids="+jellyfinID(jellyVideo, first.ID), token, ""); w.Code != 400 {
		t.Fatalf("video ids on folders accepted: %d", w.Code)
	}
	for _, path := range []string{"/Genres?ParentId=" + jellyfinID(jellyView, 1) + "&SortBy=SortName&Recursive=true&IncludeItemTypes=Movie", "/Tags?Recursive=true&IncludeItemTypes=Movie,Series"} {
		if items, total = jellyfinItems(t, jellyfinRequest(s, "GET", path, token, "")); total != 1 || items[0]["Name"] != "tagged" {
			t.Fatalf("%s: %d %v", path, total, items)
		}
	}
	// Folder descending order is the exact inverse of the ascending SQL order.
	for _, name := range []string{"apple", "Banana", "cherry"} {
		if err := database.DB.Create(&models.Tag{Name: name, Color: "#000000"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	names := func(items []map[string]interface{}) string {
		out := []string{}
		for _, item := range items {
			out = append(out, item["Name"].(string))
		}
		return strings.Join(out, ",")
	}
	if items, _ = list("ParentId=" + tags + "&SortBy=SortName&SortOrder=Descending"); names(items) != "tagged,cherry,Banana,apple" {
		t.Fatalf("descending folders: %s", names(items))
	}
	// SortOrder without SortBy is a no-op; ascending is the exact inverse of descending.
	for _, query := range []string{"", "&SortOrder=Descending", "&SortBy=SortName&SortOrder=Ascending"} {
		if items, _ = list("ParentId=" + tags + query); names(items) != "apple,Banana,cherry,tagged" {
			t.Fatalf("ascending folders %q: %s", query, names(items))
		}
	}
	// Several tag names select the union; repeated keys merge, whatever their casing.
	other := models.Tag{Name: "other", Color: "#ffffff"}
	if err := database.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&second).Association("Tags").Append(&other); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"Tags=tagged%7Cother", "Tags=tagged&Tags=other", "tags=tagged&Tags=other", "Genres=other%7Ctagged"} {
		if _, total = list(query); total != 2 {
			t.Fatalf("%s: union expected 2, got %d", query, total)
		}
	}
	if items, total = list("Tags=other"); total != 1 || items[0]["Id"] != jellyfinID(jellyVideo, second.ID) {
		t.Fatalf("single tag: %d %v", total, items)
	}
	// Soft-deleted tags leave both the folder list and the recursive video list.
	if err := database.DB.Delete(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if items, total = list("ParentId=" + tags + "&Recursive=true"); total != 1 || items[0]["Id"] != jellyfinID(jellyVideo, second.ID) {
		t.Fatalf("deleted tag still exposes videos: %d %v", total, items)
	}
	if items, _ = list("ParentId=" + tags); strings.Contains(names(items), "tagged") {
		t.Fatalf("deleted tag folder still listed: %s", names(items))
	}
}

func jellyfinProbeFixture(t *testing.T, v models.Video) {
	t.Helper()
	info, err := os.Stat(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	size, mod := info.Size(), info.ModTime().UnixNano()
	if err := database.DB.Create(&models.VideoTechnicalMetadata{VideoID: v.ID, SuccessfulSourceSize: &size, SuccessfulSourceModTimeNS: &mod}).Error; err != nil {
		t.Fatal(err)
	}
	width, height, channels, sdr := 1920, 1080, 6, false
	// Two default-flagged audio tracks: the first one (eac3) is primary, as in Jellyfin.
	// The attached picture is not a video stream for counting purposes.
	for _, stream := range []models.MediaStream{
		{VideoID: v.ID, StreamIndex: 0, StreamType: "video", CodecName: "hevc", Profile: "Main 10", Width: &width, Height: &height, IsHDR: &sdr},
		{VideoID: v.ID, StreamIndex: 1, StreamType: "audio", CodecName: "eac3", Channels: &channels, IsDefault: true},
		{VideoID: v.ID, StreamIndex: 2, StreamType: "audio", CodecName: "dts", Channels: &channels, IsDefault: true},
		{VideoID: v.ID, StreamIndex: 3, StreamType: "video", CodecName: "mjpeg", IsAttachedPic: true},
	} {
		if err := database.DB.Create(&stream).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestJellyfinProfileConditionsFollowJellyfinSemantics(t *testing.T) {
	s, token, v, _ := jellyfinLibraryFixture(t)
	jellyfinProbeFixture(t, v)
	id := jellyfinID(jellyVideo, v.ID)
	direct := func(body string) bool {
		w := jellyfinRequest(s, "POST", "/Items/"+id+"/PlaybackInfo?UserId="+jellyfinUserID+"&StartTimeTicks=0&IsPlayback=true&AutoOpenLiveStream=true", token, body)
		if w.Code != 200 {
			t.Fatalf("playbackinfo %d %s", w.Code, w.Body)
		}
		return strings.Contains(w.Body.String(), `"SupportsDirectPlay":true`)
	}
	base := `"DirectPlayProfiles":[{"Type":"Video","Container":"mp4,mkv","VideoCodec":"h264,h265","AudioCodec":"aac,eac3"}]`
	for _, entry := range []struct {
		name, profiles string
		want           bool
	}{
		{"unknown properties not required", `"CodecProfiles":[{"Type":"Video","Codec":"hevc","Conditions":[{"Condition":"NotEquals","Property":"IsAnamorphic","Value":"true","IsRequired":false},{"Condition":"LessThanEqual","Property":"VideoLevel","Value":"183","IsRequired":false},{"Condition":"NotEquals","Property":"IsInterlaced","Value":"true","IsRequired":false}]}]`, true},
		{"unknown property required by default", `"CodecProfiles":[{"Type":"Video","Codec":"hevc","Conditions":[{"Condition":"NotEquals","Property":"IsAnamorphic","Value":"true"}]}]`, false},
		{"known property without data, required", `"CodecProfiles":[{"Type":"Video","Codec":"hevc","Conditions":[{"Condition":"LessThanEqual","Property":"VideoBitrate","Value":"20000000","IsRequired":true}]}]`, false},
		{"known property without data, not required", `"CodecProfiles":[{"Type":"Video","Codec":"hevc","Conditions":[{"Condition":"LessThanEqual","Property":"VideoBitrate","Value":"1","IsRequired":false}]}]`, true},
		{"apply conditions not met skips profile", `"CodecProfiles":[{"Type":"Video","Codec":"hevc","ApplyConditions":[{"Condition":"Equals","Property":"VideoProfile","Value":"main"}],"Conditions":[{"Condition":"LessThanEqual","Property":"Width","Value":"640"}]}]`, true},
		{"apply conditions met enforces conditions", `"CodecProfiles":[{"Type":"Video","Codec":"hevc","ApplyConditions":[{"Condition":"Equals","Property":"VideoProfile","Value":"Main 10"}],"Conditions":[{"Condition":"LessThanEqual","Property":"Width","Value":"640"}]}]`, false},
		{"video-audio apply conditions", `"CodecProfiles":[{"Type":"VideoAudio","Codec":"eac3","ApplyConditions":[{"Condition":"GreaterThanEqual","Property":"AudioChannels","Value":"6"}],"Conditions":[{"Condition":"LessThanEqual","Property":"AudioChannels","Value":"2"}]}]`, false},
		{"subcontainer is ignored outside hls", `"CodecProfiles":[{"Type":"Video","Codec":"hevc","Container":"mkv","SubContainer":"ts","Conditions":[{"Condition":"LessThanEqual","Property":"Width","Value":"640"}]}]`, false},
		{"hls profile never matches a file", `"CodecProfiles":[{"Type":"Video","Codec":"hevc","Container":"hls","SubContainer":"mkv","Conditions":[{"Condition":"LessThanEqual","Property":"Width","Value":"640"}]}]`, true},
		{"audio-type profiles do not apply to video items", `"CodecProfiles":[{"Type":"Audio","Codec":"eac3","Conditions":[{"Condition":"LessThanEqual","Property":"AudioChannels","Value":"2"}]}]`, true},
		{"known property still enforced", `"CodecProfiles":[{"Type":"VideoAudio","Codec":"eac3","Conditions":[{"Condition":"LessThanEqual","Property":"AudioChannels","Value":"2","IsRequired":false}]}]`, false},
		{"derived stream counts and audio role", `"CodecProfiles":[{"Type":"VideoAudio","Conditions":[{"Condition":"NotEquals","Property":"IsSecondaryAudio","Value":"true"},{"Condition":"Equals","Property":"NumAudioStreams","Value":"2"}]},{"Type":"Video","Codec":"hevc","Conditions":[{"Condition":"Equals","Property":"NumVideoStreams","Value":"1"},{"Condition":"EqualsAny","Property":"VideoRangeType","Value":"SDR|HDR10"},{"Condition":"Equals","Property":"VideoRange","Value":"SDR"}]}]`, true},
		{"derived count enforced", `"CodecProfiles":[{"Type":"VideoAudio","Conditions":[{"Condition":"GreaterThanEqual","Property":"NumAudioStreams","Value":"3"}]}]`, false},
		{"container profile applies", `"ContainerProfiles":[{"Type":"Video","Container":"mkv","Conditions":[{"Condition":"LessThanEqual","Property":"Width","Value":"640"}]}]`, false},
		{"negative container list excludes this file", `"CodecProfiles":[{"Type":"Video","Codec":"hevc","Container":"-mkv","Conditions":[{"Condition":"LessThanEqual","Property":"Width","Value":"640"}]}]`, true},
		{"unknown operator fails", `"CodecProfiles":[{"Type":"Video","Codec":"hevc","Conditions":[{"Condition":"Contains","Property":"Width","Value":"19","IsRequired":false}]}]`, false},
	} {
		if got := direct(`{"DeviceProfile":{` + base + `,` + entry.profiles + `}}`); got != entry.want {
			t.Errorf("%s: direct play %v, want %v", entry.name, got, entry.want)
		}
	}
	// The primary audio track is the first default one (eac3), so the dts track never blocks direct
	// play, and the advertised default index points at it.
	w := jellyfinRequest(s, "POST", "/Items/"+id+"/PlaybackInfo", token, `{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"mkv","AudioCodec":"eac3"}]}}`)
	if !strings.Contains(w.Body.String(), `"SupportsDirectPlay":true`) || !strings.Contains(w.Body.String(), `"DefaultAudioStreamIndex":1`) {
		t.Errorf("primary audio track: %s", w.Body)
	}
	// An HDR stream leaves the HDR flavour unknown while VideoRange itself is enforced.
	hdr := true
	if err := database.DB.Model(&models.MediaStream{}).Where("video_id = ? AND stream_index = 0", v.ID).Update("is_hdr", &hdr).Error; err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		name, conditions string
		want             bool
	}{
		{"hdr range enforced", `[{"Condition":"Equals","Property":"VideoRange","Value":"SDR","IsRequired":false}]`, false},
		{"hdr flavour unknown, not required", `[{"Condition":"EqualsAny","Property":"VideoRangeType","Value":"SDR|HDR10","IsRequired":false}]`, true},
		{"hdr flavour unknown, required", `[{"Condition":"EqualsAny","Property":"VideoRangeType","Value":"SDR|HDR10","IsRequired":true}]`, false},
	} {
		if got := direct(`{"DeviceProfile":{` + base + `,"CodecProfiles":[{"Type":"Video","Codec":"hevc","Conditions":` + entry.conditions + `}]}}`); got != entry.want {
			t.Errorf("%s: direct play %v, want %v", entry.name, got, entry.want)
		}
	}
	if !direct(`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"mkv","VideoCodec":"h265"}]}}`) {
		t.Error("h265 alias rejected")
	}
	if direct(`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"-mkv,avi"}]}}`) {
		t.Error("negative DirectPlayProfile container accepted")
	}
}

func TestJellyfinRequestLogMasksIdentifiersAndValues(t *testing.T) {
	s, token, first, _ := jellyfinLibraryFixture(t)
	jellyfinProbeFixture(t, first)
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	id := jellyfinID(jellyVideo, first.ID)
	jellyfinRequest(s, "GET", "/Users/"+jellyfinUserID+"/Items?SearchTerm=private-name&SortBy=SortName&Unknown=1&api_key="+token, token, "")
	jellyfinRequest(s, "GET", "/Videos/"+id+"/stream?Static=true&api_key="+token, token, "")
	jellyfinRequest(s, "GET", "/Items/"+id+"/Images/Primary?tag=abc", token, "")
	jellyfinRequest(s, "GET", "/Items/"+id, token, "")
	jellyfinRequest(s, "POST", "/Sessions/Playing/Progress", token, `{"ItemId":"`+id+`","PositionTicks":10}`)
	// Newlines in the path, in parameter values and in profile text must not forge log lines.
	jellyfinRequest(s, "GET", "/Items/%0A[Jellyfin]%20GET%20/forged%20200?SortBy=Name%0A[Jellyfin]%20forged", token, "")
	jellyfinRequest(s, "POST", "/Items/"+id+"/PlaybackInfo", token, `{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video"}],"CodecProfiles":[{"Type":"Video","Codec":"hevc","Conditions":[{"Condition":"LessThanEqual","Property":"Width","Value":"640\n[Jellyfin] forged"}]}]}}`)
	outside := httptest.NewRequest("GET", "/System/Info?SortBy=outside", nil)
	outside.RemoteAddr = "8.8.8.8:4444"
	s.Handler().ServeHTTP(httptest.NewRecorder(), outside)
	out := buf.String()
	for _, want := range []string{"GET /users/{id}/items 400", "SortBy=SortName", "SearchTerm", "api_key", "GET /items/{id} 200", "GET /items/{id}/images/primary 404", "PlaybackInfo 视频", "Width LessThanEqual 640?[Jellyfin] forged", "SortBy=Name?[Jellyfin] forged", "GET /items/?[jellyfin] get /forged 200 404"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	for _, leak := range []string{"private-name", token, jellyfinUserID, id, first.Directory, "/videos/", "sessions/playing", "\n[Jellyfin] forged", "\n[jellyfin] get /forged", "outside", " 403 "} {
		if strings.Contains(out, leak) {
			t.Errorf("log leaks %q:\n%s", leak, out)
		}
	}
}

// Fileball's home and detail screens call these; Jellyfin never answers them with 404.
func TestJellyfinAnswersClientHomeEndpoints(t *testing.T) {
	s, token, first, _ := jellyfinLibraryFixture(t)
	for _, path := range []string{"/Shows/NextUp?UserId=" + jellyfinUserID + "&Limit=24", "/Studios?UserId=" + jellyfinUserID, "/Persons?Limit=10", "/Artists?UserId=" + jellyfinUserID} {
		if items, total := jellyfinItems(t, jellyfinRequest(s, "GET", path, token, "")); total != 0 || len(items) != 0 {
			t.Errorf("%s: expected empty page, got %d", path, total)
		}
	}
	if w := jellyfinRequest(s, "GET", "/Movies/Recommendations?UserId="+jellyfinUserID+"&categoryLimit=6", token, ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("recommendations: %d %s", w.Code, w.Body)
	}
	w := jellyfinRequest(s, "GET", "/DisplayPreferences/usersettings?userId="+jellyfinUserID+"&client=emby", token, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"Id":"usersettings"`) || !strings.Contains(w.Body.String(), `"Client":"emby"`) {
		t.Errorf("display preferences: %d %s", w.Code, w.Body)
	}
	if w := jellyfinRequest(s, "POST", "/DisplayPreferences/usersettings?userId="+jellyfinUserID+"&client=emby", token, `{"Id":"usersettings","SortBy":"DateCreated"}`); w.Code != 204 {
		t.Errorf("display preferences save: %d %s", w.Code, w.Body)
	}
	if w := jellyfinRequest(s, "POST", "/DisplayPreferences/usersettings", token, `not json`); w.Code != 400 {
		t.Errorf("display preferences bad body: %d", w.Code)
	}
	id := jellyfinID(jellyVideo, first.ID)
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	w = jellyfinRequest(s, "GET", "/Items/"+id+"/Download?api_key="+token, "", "")
	if w.Code != 200 || w.Body.String() != "0123456789" || !strings.Contains(w.Header().Get("Content-Disposition"), "first.mkv") || strings.Contains(w.Header().Get("Content-Disposition"), first.Directory) {
		t.Errorf("download: %d %q %s", w.Code, w.Header().Get("Content-Disposition"), w.Body)
	}
	if strings.Contains(buf.String(), "download") {
		t.Errorf("successful download transfers must not be logged:\n%s", buf.String())
	}
	if err := os.Remove(first.Path); err != nil {
		t.Fatal(err)
	}
	if w := jellyfinRequest(s, "GET", "/Items/"+id+"/Download?api_key="+token, "", ""); w.Code != 404 || w.Header().Get("Content-Disposition") != "" {
		t.Errorf("missing file download: %d %q", w.Code, w.Header().Get("Content-Disposition"))
	}
	if !strings.Contains(buf.String(), "GET /items/{id}/download 404") {
		t.Errorf("failed download must be logged:\n%s", buf.String())
	}
	if err := os.WriteFile(first.Path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/Items/"+id+"/Download", nil)
	r.RemoteAddr = "192.168.1.2:1"
	r.Header.Set("X-Emby-Token", token)
	r.Header.Set("Range", "bytes=2-4")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != 206 || rec.Body.String() != "234" {
		t.Errorf("download range: %d %s", rec.Code, rec.Body)
	}
	if w := jellyfinRequest(s, "GET", "/Items/"+id+"/Download", "", ""); w.Code != 401 {
		t.Errorf("download without token: %d", w.Code)
	}
}
