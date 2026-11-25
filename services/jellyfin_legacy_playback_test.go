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

func jellyfinLegacyAVIFixture(t *testing.T, ext, videoCodec, audioCodec string) (*JellyfinServer, string, models.Video, []byte) {
	t.Helper()
	ffmpeg, _ := requireEditFFmpeg(t)
	server, token, item, _ := jellyfinLibraryFixture(t)
	server.probe = NewMediaProbeService()
	item.Path = filepath.Join(item.Directory, "original"+ext)
	item.Name = filepath.Base(item.Path)
	runFixtureCommand(t, ffmpeg, "-hide_banner", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=160x96:r=25:d=1",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1", "-c:v", videoCodec, "-threads", "2", "-c:a", audioCodec, "-f", "avi", item.Path)
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
	server, token, item, original := jellyfinLegacyAVIFixture(t, ext, "mpeg4", "pcm_s16le")
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
			server, token, item, original := jellyfinLegacyAVIFixture(t, ".avi", fixture.videoCodec, fixture.audioCodec)
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
