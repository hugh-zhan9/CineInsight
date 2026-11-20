package services

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"video-master/database"
	"video-master/models"
)

func TestJellyfinConcurrentColdPlaybackInfoProbesOriginalOnce(t *testing.T) {
	s, token, video, _ := jellyfinLibraryFixture(t)
	var probes atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	s.probe = newMediaProbeServiceWithRunner(func(ctx context.Context, path string) ([]byte, string, error) {
		if path != video.Path {
			t.Errorf("expected original probe, got %q", path)
		}
		if probes.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return []byte(`{"format":{"format_name":"matroska,webm","duration":"100","bit_rate":"85000000"},"streams":[{"index":0,"codec_type":"video","codec_name":"hevc","width":3840,"height":2160}]}`), "", nil
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	})
	url := "/Items/" + jellyfinID(jellyVideo, video.ID) + "/PlaybackInfo?MaxStreamingBitrate=100000000"
	responses := make(chan *httptest.ResponseRecorder, 2)
	request := func() { responses <- jellyfinRequest(s, "GET", url, token, "") }
	go request()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never began probing")
	}
	go request()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.probe.gatesMu.Lock()
		waiting := s.probe.gates[video.ID].refs == 2
		s.probe.gatesMu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second request never reached the probe gate")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	for range 2 {
		select {
		case w := <-responses:
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"Codec":"hevc"`) || !strings.Contains(w.Body.String(), `"SupportsDirectPlay":true`) {
				t.Fatalf("expected original high-quality stream: status %d, body %s", w.Code, w.Body)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("PlaybackInfo response did not complete")
		}
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("concurrent requests probed %d times, want one", got)
	}
	if err := s.probe.Refresh(context.Background(), video.ID); err != nil || probes.Load() != 2 {
		t.Fatalf("explicit refresh must still probe: count %d, err %v", probes.Load(), err)
	}
}

func TestJellyfinOriginalPlaybackSkipsTagAndRepeatedStreamQueries(t *testing.T) {
	s, token, video, _ := jellyfinLibraryFixture(t)
	tag := models.Tag{Name: "catalog-only"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&video).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatal(err)
	}
	size, mtime := info.Size(), info.ModTime().UnixNano()
	if err := database.DB.Create(&models.VideoTechnicalMetadata{
		VideoID: video.ID, SuccessfulSourceSize: &size, SuccessfulSourceModTimeNS: &mtime, FormatName: "matroska,webm",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.MediaStream{VideoID: video.ID, StreamIndex: 0, StreamType: "video", CodecName: "hevc"}).Error; err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	const hook = "test:jellyfin_original_query_counts"
	query := database.DB.Callback().Query()
	if err := query.Before("gorm:query").Register(hook, func(tx *gorm.DB) {
		counts[tx.Statement.Table]++
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = query.Remove(hook) })
	item := jellyfinID(jellyVideo, video.ID)
	w := jellyfinRequest(s, "GET", "/Items/"+item+"/PlaybackInfo", token, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"Codec":"hevc"`) || counts["media_streams"] != 1 || counts["tags"] != 0 || counts["video_tags"] != 0 {
		t.Fatalf("unexpected warm startup query counts: %v; %d %s", counts, w.Code, w.Body)
	}
	clear(counts)
	r := httptest.NewRequest("GET", "/Videos/"+item+"/stream", nil)
	r.Header.Set("X-Emby-Token", token)
	r.Header.Set("Range", "bytes=3-7")
	r.RemoteAddr = "127.0.0.1:2"
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "34567" || w.Header().Get("Content-Range") != "bytes 3-7/10" ||
		counts["tags"] != 0 || counts["video_tags"] != 0 || counts["media_streams"] != 0 {
		t.Fatalf("unexpected Range behavior: %v; %d %s, %v", counts, w.Code, w.Body, w.Header())
	}
}
