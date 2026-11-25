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
	"time"

	"video-master/database"
	"video-master/models"
)

func TestJellyfinLegacyAVIDirectPlayAndRange(t *testing.T) {
	for _, ext := range []string{".AVI", ".DIVX"} {
		t.Run(ext, func(t *testing.T) { testJellyfinAVIDirectPlayAndRange(t, ext) })
	}
}

func jellyfinOriginalFixture(t *testing.T, ext, format, videoCodec, audioCodec string) (*JellyfinServer, string, models.Video, []byte) {
	t.Helper()
	ffmpeg, _ := requireEditFFmpeg(t)
	server, token, item, _ := jellyfinLibraryFixture(t)
	server.probe = NewMediaProbeService()
	item.Path = filepath.Join(item.Directory, "original"+ext)
	item.Name = filepath.Base(item.Path)
	runFixtureCommand(t, ffmpeg, "-hide_banner", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=160x96:r=25:d=1",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1", "-c:v", videoCodec, "-threads", "2", "-c:a", audioCodec, "-f", format, item.Path)
	original, err := os.ReadFile(item.Path)
	if err != nil {
		t.Fatal(err)
	}
	item.Size = int64(len(original))
	if err := database.DB.Model(&item).Updates(map[string]any{"path": item.Path, "name": item.Name, "size": item.Size}).Error; err != nil {
		t.Fatal(err)
	}
	return server, token, item, original
}

func testJellyfinAVIDirectPlayAndRange(t *testing.T, ext string) {
	t.Helper()
	server, token, item, original := jellyfinOriginalFixture(t, ext, "avi", "mpeg4", "pcm_s16le")
	itemID := jellyfinID(jellyVideo, item.ID)
	infoPath := "/Items/" + itemID + "/PlaybackInfo"
	profile := func(container, videoCodec string) string {
		return fmt.Sprintf(`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":%q,"VideoCodec":%q,"AudioCodec":"pcm_s16le"}]}}`, container, videoCodec)
	}
	url := fmt.Sprintf("/Videos/%s/stream.avi?Static=true", itemID)
	for _, p := range []string{
		profile("avi", "mpeg4"),
		`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"avi","VideoCodec":"xvid","AudioCodec":"wav"}]}}`,
	} {
		w := jellyfinRequest(server, "POST", infoPath, token, p)
		var info struct{ MediaSources []map[string]any }
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil || len(info.MediaSources) != 1 {
			t.Fatalf("AVI profile rejected %d %s", w.Code, w.Body)
		}
		source := info.MediaSources[0]
		if source["Container"] != "avi" || source["DirectStreamUrl"] != url || source["SupportsDirectPlay"] != true ||
			source["SupportsDirectStream"] != false || source["SupportsTranscoding"] != false || strings.Contains(w.Body.String(), item.Directory) {
			t.Fatalf("incorrect direct-play metadata: %s", w.Body)
		}
	}
	for _, p := range []string{
		profile("mp4", "mpeg4"), profile("avi", "h264"), profile("-divx", "mpeg4"),
		`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"avi","VideoCodec":"mpeg4","AudioCodec":"aac"}]}}`,
		`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"avi","VideoCodec":"mpeg4"}],"CodecProfiles":[{"Type":"Video","Container":"divx","Codec":"mpeg4","Conditions":[{"Condition":"LessThanEqual","Property":"Width","Value":"64"}]}]}}`,
		`{"EnableDirectPlay":false}`,
	} {
		w := jellyfinRequest(server, "POST", infoPath, token, p)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "NoCompatibleStream") {
			t.Fatalf("client constraint ignored: %d %s", w.Code, w.Body)
		}
	}
	for _, route := range []string{url, "/Videos/" + itemID + "/stream.divx?Static=true", "/Items/" + itemID + "/Download"} {
		r := httptest.NewRequest("GET", route, nil)
		r.RemoteAddr = "127.0.0.1:2"
		r.Header.Set("X-Emby-Token", token)
		r.Header.Set("Range", "bytes=0-31")
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusPartialContent || w.Header().Get("Content-Type") != "video/x-msvideo" ||
			w.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-31/%d", len(original)) || !bytes.Equal(w.Body.Bytes(), original[:32]) {
			t.Fatalf("not original AVI range at %s: %d %v", route, w.Code, w.Header())
		}
	}
	if w := jellyfinRequest(server, "GET", "/Videos/"+itemID+"/stream.mp4?Static=true", token, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("wrong container unexpectedly accepted: %d", w.Code)
	}
	w := jellyfinRequest(server, "HEAD", url, token, "")
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
	}{{"", false}, {"mov,mp4,m4a,3gp,3g2,mj2", false}, {"avi", true}, {"asf, AvI ", true}} {
		if got, reason := jellyfinCanDirectPlay(video, models.VideoTechnicalMetadata{FormatName: test.format}, streams, &profile, nil); got != test.want {
			t.Errorf("format %q: direct=%v, reason=%s", test.format, got, reason)
		}
	}
}

func TestJellyfinFileballLegacyAVIDirectPlay(t *testing.T) {
	const bodyPrefix = `{"MaxStreamingBitrate":42000000,"DeviceProfile":{"MaxStreamingBitrate":42000000,"DirectPlayProfiles":[{"Type":"Video","Container":"mov,mp4,mkv,webm","VideoCodec":"h264,hevc,dvhe,dvh1,hev1,mpeg4,vp9","AudioCodec":"aac,mp3,wav,ac3,eac3,flac,truehd,dts,dca,opus"}]`
	const body = bodyPrefix + `}}`
	const widthLimit = `"Conditions":[{"Condition":"LessThanEqual","Property":"Width","Value":"64"}]`
	for _, fixture := range []struct{ videoCodec, audioCodec string }{{"mpeg4", "pcm_s16le"}, {"msmpeg4", "mp2"}} {
		t.Run(fixture.videoCodec+"+"+fixture.audioCodec, func(t *testing.T) {
			server, token, item, original := jellyfinOriginalFixture(t, ".avi", "avi", fixture.videoCodec, fixture.audioCodec)
			itemID := jellyfinID(jellyVideo, item.ID)
			infoPath := "/Items/" + itemID + "/PlaybackInfo"
			wantURL := "/Videos/" + itemID + "/stream.avi?Static=true"
			assertPlayback := func(t *testing.T, w *httptest.ResponseRecorder, token string, wantDirect bool) {
				t.Helper()
				var info struct {
					MediaSources []struct {
						Container, DirectStreamUrl string
						SupportsDirectPlay         bool
					}
					ErrorCode string
				}
				if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil {
					t.Fatalf("invalid PlaybackInfo: %d %s", w.Code, w.Body)
				}
				if !wantDirect {
					if info.ErrorCode != "NoCompatibleStream" || len(info.MediaSources) != 0 {
						t.Fatalf("expected NoCompatibleStream: %s", w.Body)
					}
					return
				}
				if info.ErrorCode != "" || len(info.MediaSources) != 1 || !info.MediaSources[0].SupportsDirectPlay ||
					info.MediaSources[0].Container != "avi" || info.MediaSources[0].DirectStreamUrl != wantURL {
					t.Fatalf("expected AVI direct play: %s", w.Body)
				}
				r := httptest.NewRequest("GET", info.MediaSources[0].DirectStreamUrl, nil)
				r.RemoteAddr = "127.0.0.1:2"
				r.Header.Set("X-Emby-Token", token)
				r.Header.Set("Range", "bytes=0-31")
				w = httptest.NewRecorder()
				server.Handler().ServeHTTP(w, r)
				if w.Code != http.StatusPartialContent || w.Header().Get("Content-Type") != "video/x-msvideo" ||
					w.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-31/%d", len(original)) || !bytes.Equal(w.Body.Bytes(), original[:32]) {
					t.Fatalf("not original AVI range: %d %v", w.Code, w.Header())
				}
			}
			for _, test := range []struct {
				name, client, userAgent, body string
				wantDirect                    bool
			}{
				{"Fileball", "Fileball", "", body, true},
				{"Filebar", "Filebar", "", body, true},
				{"Filebox user agent", "", "Filebox/1.4.13", body, true},
				{"non-Fileball", "Jellyfin", "", body, false},
				{"direct play disabled", "Fileball", "", `{"EnableDirectPlay":false,` + body[1:], false},
				{"request bitrate limit", "Fileball", "", strings.Replace(body, "42000000", "1", 1), false},
				{"profile streaming bitrate limit", "Fileball", "", strings.Replace(bodyPrefix, `"DeviceProfile":{"MaxStreamingBitrate":42000000`, `"DeviceProfile":{"MaxStreamingBitrate":1`, 1) + `}}`, false},
				{"profile static bitrate limit", "Fileball", "", bodyPrefix + `,"MaxStaticBitrate":1}}`, false},
				{"AVI excluded", "Fileball", "", strings.Replace(body, "mov,mp4,mkv,webm", "-avi", 1), false},
				{"DivX alias excluded", "Fileball", "", strings.Replace(body, `"}]`, `"},{"Type":"Video","Container":"-divx"}]`, 1), false},
				{"generic codec condition ignored", "Fileball", "", bodyPrefix + `,"CodecProfiles":[{"Type":"Video",` + widthLimit + `}]}}`, true},
				{"AVI codec condition enforced", "Fileball", "", bodyPrefix + `,"CodecProfiles":[{"Type":"Video","Container":"avi",` + widthLimit + `}]}}`, false},
				{"AVI container condition enforced", "Fileball", "", bodyPrefix + `,"ContainerProfiles":[{"Type":"Video","Container":"avi",` + widthLimit + `}]}}`, false},
			} {
				t.Run(test.name, func(t *testing.T) {
					r := httptest.NewRequest("POST", infoPath, strings.NewReader(test.body))
					r.RemoteAddr = "127.0.0.1:2"
					r.Header.Set("X-Emby-Token", token)
					if test.client != "" {
						r.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Client=%q`, test.client))
					}
					r.Header.Set("User-Agent", test.userAgent)
					w := httptest.NewRecorder()
					server.Handler().ServeHTTP(w, r)
					assertPlayback(t, w, token, test.wantDirect)
				})
			}

			now := server.now()
			server.now = func() time.Time { return now }
			r := httptest.NewRequest("POST", "/Users/AuthenticateByName", strings.NewReader(`{"Username":"viewer","Pw":"test-password"}`))
			r.RemoteAddr = "127.0.0.1:2"
			r.Header.Set("Authorization", `MediaBrowser Client="Fileball"`)
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, r)
			var login struct{ AccessToken string }
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &login) != nil || login.AccessToken == "" {
				t.Fatalf("Fileball login failed: %d", w.Code)
			}
			token = login.AccessToken
			t.Run("session created", func(t *testing.T) {
				assertPlayback(t, jellyfinRequest(server, "POST", infoPath, token, body), token, true)
			})
			clear(server.sessions)
			t.Run("session loaded", func(t *testing.T) {
				assertPlayback(t, jellyfinRequest(server, "POST", infoPath, token, body), token, true)
			})
			now = now.Add(jellyfinSessionSeenInterval)
			t.Run("session refreshed", func(t *testing.T) {
				assertPlayback(t, jellyfinRequest(server, "POST", infoPath, token, body), token, true)
			})
		})
	}
}

func TestJellyfinFileballOriginalDirectPlay(t *testing.T) {
	const defaultProfile = `{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"mov,mp4,mkv,webm","VideoCodec":"h264,hevc,dvhe,dvh1,hev1,mpeg4,vp9","AudioCodec":"aac,mp3,wav,ac3,eac3,flac,truehd,dts,dca,opus"}]}}`
	const widthLimit = `"Conditions":[{"Condition":"LessThanEqual","Property":"Width","Value":"64"}]`
	for _, fixture := range []struct {
		ext, format, videoCodec, audioCodec, container, stream string
	}{
		{".mov", "mov", "prores", "pcm_s16le", "mov", "stream.mov"},
		{".qt", "mov", "mjpeg", "pcm_s16be", "mov", "stream.mov"},
		{".flv", "flv", "flv", "aac", "flv", "stream.flv"},
		{".m4v", "mp4", "mpeg4", "alac", "m4v", "stream.m4v"},
		{".mkv", "matroska", "mjpeg", "pcm_s16le", "mkv", "stream"},
	} {
		t.Run(fixture.ext, func(t *testing.T) {
			server, token, item, original := jellyfinOriginalFixture(t, fixture.ext, fixture.format, fixture.videoCodec, fixture.audioCodec)
			id := jellyfinID(jellyVideo, item.ID)
			streamPath := "/Videos/" + id + "/"
			assertPlayback := func(t *testing.T, client, body, query string, wantDirect bool) {
				t.Helper()
				r := httptest.NewRequest("POST", "/Items/"+id+"/PlaybackInfo"+query, strings.NewReader(body))
				r.RemoteAddr = "127.0.0.1:2"
				r.Header.Set("X-Emby-Token", token)
				r.Header.Set("X-Emby-Client", client)
				w := httptest.NewRecorder()
				server.Handler().ServeHTTP(w, r)
				info := jellyfinObject(t, w)
				sources := info["MediaSources"].([]interface{})
				if !wantDirect {
					if len(sources) != 0 || info["ErrorCode"] != "NoCompatibleStream" {
						t.Fatalf("expected NoCompatibleStream: %s", w.Body)
					}
					return
				}
				if len(sources) != 1 || info["ErrorCode"] != nil {
					t.Fatalf("expected original direct play: %s", w.Body)
				}
				source := sources[0].(map[string]interface{})
				if source["Container"] != fixture.container || source["DirectStreamUrl"] != streamPath+fixture.stream+"?Static=true" ||
					source["SupportsDirectPlay"] != true || source["SupportsDirectStream"] != false || source["SupportsTranscoding"] != false {
					t.Fatalf("incorrect original source: %s", w.Body)
				}
			}
			for _, client := range []string{"Filebar", "Fileball"} {
				assertPlayback(t, client, defaultProfile, "", true)
			}
			assertPlayback(t, "Jellyfin", defaultProfile, "", false)
			if fixture.ext == ".flv" {
				if w := jellyfinRequest(server, "GET", streamPath+"stream.f4v?Static=true", token, ""); w.Code != http.StatusBadRequest {
					t.Fatalf("FLV must not be treated as F4V: %d", w.Code)
				}
			}
			if fixture.container != "mov" {
				return
			}
			strictProfile := fmt.Sprintf(`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"mov","VideoCodec":%q,"AudioCodec":"lpcm"}]}}`, fixture.videoCodec)
			assertPlayback(t, "Jellyfin", strictProfile, "", true)
			for _, test := range []struct{ name, body, query string }{
				{"disabled by body", `{"EnableDirectPlay":false,` + defaultProfile[1:], ""},
				{"disabled by query", defaultProfile, "?EnableDirectPlay=false"},
				{"bitrate limit", defaultProfile, "?MaxStreamingBitrate=1"},
				{"mov exclusion", strings.Replace(strictProfile, `"Container":"mov"`, `"Container":"-mov"`, 1), ""},
				{"qt exclusion alongside positive profile", strings.Replace(strictProfile, `"}]`, `"},{"Type":"Video","Container":"-qt"}]`, 1), ""},
				{"targeted codec condition", strings.Replace(defaultProfile, `]}}`, `],"CodecProfiles":[{"Type":"Video","Container":"qt",`+widthLimit+`}]}}`, 1), ""},
				{"targeted container condition", strings.Replace(defaultProfile, `]}}`, `],"ContainerProfiles":[{"Type":"Video","Container":"mov",`+widthLimit+`}]}}`, 1), ""},
			} {
				t.Run(test.name, func(t *testing.T) { assertPlayback(t, "Filebar", test.body, test.query, false) })
			}
			for _, route := range []string{streamPath + "stream.mov?Static=true", streamPath + "stream.qt?Static=true", "/Items/" + id + "/Download"} {
				w := jellyfinRequest(server, "GET", route, token, "")
				if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "video/quicktime" || !bytes.Equal(w.Body.Bytes(), original) {
					t.Fatalf("not original QuickTime bytes at %s: %d %v", route, w.Code, w.Header())
				}
				r := httptest.NewRequest("GET", route, nil)
				r.RemoteAddr = "127.0.0.1:2"
				r.Header.Set("X-Emby-Token", token)
				r.Header.Set("Range", "bytes=0-31")
				w = httptest.NewRecorder()
				server.Handler().ServeHTTP(w, r)
				if w.Code != http.StatusPartialContent || w.Header().Get("Content-Type") != "video/quicktime" ||
					w.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-31/%d", len(original)) || !bytes.Equal(w.Body.Bytes(), original[:32]) {
					t.Fatalf("not original QuickTime range at %s: %d %v", route, w.Code, w.Header())
				}
				w = jellyfinRequest(server, "HEAD", route, token, "")
				if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "video/quicktime" || w.Header().Get("Content-Length") != fmt.Sprint(len(original)) {
					t.Fatalf("bad QuickTime HEAD at %s: %d %v", route, w.Code, w.Header())
				}
			}
			if w := jellyfinRequest(server, "GET", streamPath+"stream.mp4?Static=true", token, ""); w.Code != http.StatusBadRequest {
				t.Fatalf("wrong container accepted: %d", w.Code)
			}
			if fixture.ext == ".qt" {
				if err := database.DB.Model(&models.VideoTechnicalMetadata{}).Where("video_id = ?", item.ID).Update("format_name", "matroska").Error; err != nil {
					t.Fatal(err)
				}
				assertPlayback(t, "Jellyfin", strictProfile, "", false)
			}
			if err := database.DB.Model(&models.MediaStream{}).Where("video_id = ? AND stream_type = ?", item.ID, "video").Update("is_attached_pic", true).Error; err != nil {
				t.Fatal(err)
			}
			assertPlayback(t, "Filebar", defaultProfile, "", false)
		})
	}
}

func TestJellyfinOriginalContentTypesAndAppleCodecAliases(t *testing.T) {
	for ext, want := range map[string]string{
		"mov": "video/quicktime", "qt": "video/quicktime", "mp4": "video/mp4", "f4v": "video/mp4",
		"m4v": "video/x-m4v", "mkv": "video/x-matroska", "webm": "video/webm",
		"avi": "video/x-msvideo", "divx": "video/x-msvideo", "wmv": "video/x-ms-wmv", "asf": "video/x-ms-asf",
		"mpg": "video/mpeg", "mpeg": "video/mpeg", "vob": "video/mpeg", "flv": "video/x-flv",
		"ts": "video/mp2t", "m2ts": "video/mp2t", "mts": "video/mp2t", "3gp": "video/3gpp",
		"rm": "application/vnd.rn-realmedia", "rmvb": "application/vnd.rn-realmedia-vbr",
		"unknown-cineinsight": "application/octet-stream",
	} {
		if got := jellyfinOriginalContentType("original." + strings.ToUpper(ext)); got != want {
			t.Errorf(".%s: MIME %q, want %q", ext, got, want)
		}
	}
	for alias, want := range map[string]string{
		"avc1": "h264", "h265": "hevc", "hev1": "hevc", "hvc1": "hevc",
		"mp4a": "aac", "twos": "pcm", "sowt": "pcm", "lpcm": "pcm",
	} {
		if got := jellyfinCodecName(alias); got != want {
			t.Errorf("%s: codec %q, want %q", alias, got, want)
		}
	}
}

func TestJellyfinFileballDirectContainer(t *testing.T) {
	for _, test := range []struct{ path, format, want string }{
		{"clip.qt", "mov,mp4,m4a,3gp,3g2,mj2", "mov"},
		{"clip.wmv", "", "asf"},
		{"clip.mts", "mpegts", "mts"},
		{"clip.f4v", "flv", "f4v"},
		{"clip.unknown", "unknown, MPEGTS ", "ts"},
		{"clip.unknown", "matroska,webm", "mkv"},
		{"clip.unknown", "unknown", ""},
	} {
		if got := jellyfinFileballDirectContainer(test.path, models.VideoTechnicalMetadata{FormatName: test.format}); got != test.want {
			t.Errorf("%s (%s): got %q, want %q", test.path, test.format, got, test.want)
		}
	}
}
