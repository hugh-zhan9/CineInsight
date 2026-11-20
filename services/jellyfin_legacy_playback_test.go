package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"video-master/database"
	"video-master/models"
)

func TestJellyfinLegacyAVIDirectPlayAndRange(t *testing.T) {
	for _, ext := range []string{".AVI", ".DIVX"} {
		t.Run(ext, func(t *testing.T) { testJellyfinAVIDirectPlayAndRange(t, ext) })
	}
}

func testJellyfinAVIDirectPlayAndRange(t *testing.T, ext string) {
	t.Helper()
	ffmpeg, _ := requireEditFFmpeg(t)
	server, token, item, _ := jellyfinLibraryFixture(t)
	server.probe = NewMediaProbeService()
	item.Path = filepath.Join(item.Directory, "original"+ext)
	item.Name = filepath.Base(item.Path)
	runFixtureCommand(t, ffmpeg, "-hide_banner", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=160x96:r=25:d=1",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1", "-c:v", "mpeg4", "-threads", "2", "-c:a", "pcm_s16le", "-f", "avi", item.Path)
	original, err := os.ReadFile(item.Path)
	if err != nil {
		t.Fatal(err)
	}
	item.Size = int64(len(original))
	if err := database.DB.Model(&item).Updates(map[string]any{"path": item.Path, "name": item.Name, "size": item.Size}).Error; err != nil {
		t.Fatal(err)
	}
	itemID := jellyfinID(jellyVideo, item.ID)
	infoPath := "/Items/" + itemID + "/PlaybackInfo"
	profile := func(container, videoCodec string) string {
		return fmt.Sprintf(`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":%q,"VideoCodec":%q,"AudioCodec":"pcm_s16le"}]}}`, container, videoCodec)
	}
	w := jellyfinRequest(server, "POST", infoPath, token, profile("avi", "mpeg4"))
	var info struct{ MediaSources []map[string]any }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil || len(info.MediaSources) != 1 {
		t.Fatalf("AVI profile rejected %d %s", w.Code, w.Body)
	}
	source := info.MediaSources[0]
	url := fmt.Sprintf("/Videos/%s/stream.avi?Static=true", itemID)
	if source["Container"] != "avi" || source["DirectStreamUrl"] != url || source["SupportsDirectPlay"] != true ||
		source["SupportsDirectStream"] != false || source["SupportsTranscoding"] != false || strings.Contains(w.Body.String(), item.Directory) {
		t.Fatalf("incorrect direct-play metadata: %s", w.Body)
	}
	for _, p := range []string{
		profile("mp4", "mpeg4"), profile("avi", "h264"), profile("-divx", "mpeg4"),
		`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"avi","VideoCodec":"mpeg4","AudioCodec":"aac"}]}}`,
		`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"avi","VideoCodec":"mpeg4"}],"CodecProfiles":[{"Type":"Video","Container":"divx","Codec":"mpeg4","Conditions":[{"Condition":"LessThanEqual","Property":"Width","Value":"64"}]}]}}`,
		`{"EnableDirectPlay":false}`,
	} {
		w = jellyfinRequest(server, "POST", infoPath, token, p)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "NoCompatibleStream") {
			t.Fatalf("client constraint ignored: %d %s", w.Code, w.Body)
		}
	}
	for _, route := range []string{url, "/Videos/" + itemID + "/stream.divx?Static=true", "/Items/" + itemID + "/Download"} {
		r := httptest.NewRequest("GET", route, nil)
		r.RemoteAddr = "127.0.0.1:2"
		r.Header.Set("X-Emby-Token", token)
		r.Header.Set("Range", "bytes=0-31")
		w = httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusPartialContent || w.Header().Get("Content-Type") != "video/x-msvideo" ||
			w.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-31/%d", len(original)) || !bytes.Equal(w.Body.Bytes(), original[:32]) {
			t.Fatalf("not original AVI range at %s: %d %v", route, w.Code, w.Header())
		}
	}
	if w = jellyfinRequest(server, "GET", "/Videos/"+itemID+"/stream.mp4?Static=true", token, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("wrong container unexpectedly accepted: %d", w.Code)
	}
	w = jellyfinRequest(server, "HEAD", url, token, "")
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") != fmt.Sprint(len(original)) {
		t.Fatalf("bad AVI HEAD: %d %v", w.Code, w.Header())
	}
}

func TestJellyfinLegacyAliasRequiresMatchingProbe(t *testing.T) {
	video := models.Video{Path: "not-avi.divx"}
	streams := []models.MediaStream{{StreamType: "video", CodecName: "mpeg4"}}
	var profile jellyfinDeviceProfile
	if err := json.Unmarshal([]byte(`{"DirectPlayProfiles":[{"Type":"Video","Container":"avi","VideoCodec":"mpeg4"}]}`), &profile); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		format string
		want   bool
	}{{"", false}, {"mov,mp4,m4a,3gp,3g2,mj2", false}, {"avi", true}} {
		if got, reason := jellyfinCanDirectPlay(video, models.VideoTechnicalMetadata{FormatName: test.format}, streams, &profile, nil); got != test.want {
			t.Errorf("format %q: direct=%v, reason=%s", test.format, got, reason)
		}
	}
}
