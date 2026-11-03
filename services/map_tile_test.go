package services

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

type mapTestTransport func(*http.Request) (*http.Response, error)

func (f mapTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func tilePNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 256, 256))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
func tileFixture(t *testing.T, transport mapTestTransport) *MapTileService {
	t.Helper()
	setupImageServiceTestDB(t)
	svc := NewMapTileService(t.TempDir(), &SettingsService{})
	svc.client.Transport = transport
	t.Cleanup(svc.StopAndWait)
	return svc
}
func enableTiles(t *testing.T, s *MapTileService) *MapTileStatus {
	t.Helper()
	status, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	status, err = s.SetEnabled(context.Background(), MapTilePolicyRequest{Enabled: true, Confirmed: true, Generation: status.Generation})
	if err != nil {
		t.Fatal(err)
	}
	return status
}
func TestMapTilesOptInFixedURLCacheAndOffline(t *testing.T) {
	body := tilePNG(t)
	var calls atomic.Int64
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	s := tileFixture(t, func(r *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		if r.URL.String() != "https://tile.openstreetmap.org/0/0/0.png" || r.Header.Get("User-Agent") != mapTileUserAgent || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cache-Control") != "" {
			t.Errorf("unexpected outbound request %s %+v", r.URL, r.Header)
		}
		headers := http.Header{"Cache-Control": []string{"max-age=10"}, "Content-Type": []string{"image/png"}, "Etag": []string{`"v1"`}}
		if call == 2 {
			if r.Header.Get("If-None-Match") != `"v1"` {
				t.Error("missing conditional request")
			}
			return &http.Response{StatusCode: 304, Header: headers, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	s.now = func() time.Time { return now }
	status, err := s.Status(context.Background())
	if err != nil || status.Enabled || calls.Load() != 0 {
		t.Fatalf("implicit network %+v %v", status, err)
	}
	if _, err := s.Get(context.Background(), "osm", 0, 0, 0, status.Generation); !errors.Is(err, ErrMapTilesDisabled) {
		t.Fatal(err)
	}
	if _, err := s.SetEnabled(context.Background(), MapTilePolicyRequest{Enabled: true, Generation: status.Generation}); !errors.Is(err, ErrMapTileInvalid) {
		t.Fatal("unconfirmed map grant accepted")
	}
	status = enableTiles(t, s)
	first, err := s.Get(context.Background(), "osm", 0, 0, 0, status.Generation)
	if err != nil || !bytes.Equal(first.Bytes, body) {
		t.Fatalf("tile %v", err)
	}
	if _, err := s.Get(context.Background(), "osm", 0, 0, 0, status.Generation); err != nil || calls.Load() != 1 {
		t.Fatalf("cache bypass: %d %v", calls.Load(), err)
	}
	now = now.Add(11 * time.Second)
	if _, err := s.Get(context.Background(), "osm", 0, 0, 0, status.Generation); err != nil || calls.Load() != 2 {
		t.Fatalf("revalidation %d %v", calls.Load(), err)
	}
	off, err := s.SetEnabled(context.Background(), MapTilePolicyRequest{Generation: status.Generation})
	if err != nil {
		t.Fatal(err)
	}
	cached, err := s.Get(context.Background(), "osm", 0, 0, 0, off.Generation)
	if err != nil || !cached.Offline || calls.Load() != 2 {
		t.Fatalf("offline %+v %v", cached, err)
	}
	if _, err := s.Get(context.Background(), "https://other.invalid", 0, 0, 0, off.Generation); !errors.Is(err, ErrMapTileInvalid) {
		t.Fatal("arbitrary provider accepted")
	}
}
func TestMapDisableCancelsRequestsEvenWhenCacheIsDamaged(t *testing.T) {
	entered := make(chan struct{})
	s := tileFixture(t, func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	status := enableTiles(t, s)
	done := make(chan error, 1)
	go func() { _, err := s.Get(context.Background(), "osm", 0, 0, 0, status.Generation); done <- err }()
	<-entered
	if err := os.MkdirAll(s.directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.directory, "osm-1-0-0.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.cacheLoaded = false
	s.mu.Unlock()
	off, err := s.SetEnabled(context.Background(), MapTilePolicyRequest{Generation: status.Generation})
	if err != nil || off.Enabled || off.CacheError == "" {
		t.Fatalf("cache damage prevented revocation %+v %v", off, err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("revoked request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel HTTP")
	}
	cleaned, err := s.ClearCache(context.Background())
	if err != nil || cleaned.CacheBytes != 0 || cleaned.Enabled {
		t.Fatalf("cleanup %+v %v", cleaned, err)
	}
}
func TestMapCacheBudgetStopsNewRequestsAndSettingsSavePreservesPolicy(t *testing.T) {
	var calls atomic.Int64
	s := tileFixture(t, func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, errors.New("must not fetch") })
	status := enableTiles(t, s)
	var input models.Settings
	if err := database.DB.First(&input).Error; err != nil {
		t.Fatal(err)
	}
	input.MapTilesEnabled = false
	if err := (&SettingsService{}).UpdateSettings(input); err != nil {
		t.Fatal(err)
	}
	current, err := s.Status(context.Background())
	if err != nil || !current.Enabled {
		t.Fatalf("ordinary save revoked map policy %+v %v", current, err)
	}
	s.mu.Lock()
	s.budget = mapTileMaxBytes - 1
	s.mu.Unlock()
	if _, err := s.Get(context.Background(), "osm", 0, 0, 0, status.Generation); !errors.Is(err, ErrMapCacheFull) || calls.Load() != 0 {
		t.Fatalf("over-budget fetch %d %v", calls.Load(), err)
	}
}
func TestMapCacheHeadersRespectAgeAndNoCachePrecedence(t *testing.T) {
	now := time.Now()
	expires, must, _ := mapTileExpiration(now, http.Header{"Cache-Control": []string{"no-cache, max-age=600"}})
	if expires.After(now) || !must {
		t.Fatal("no-cache overridden by max-age")
	}
	expires, _, _ = mapTileExpiration(now, http.Header{"Cache-Control": []string{"max-age=600"}, "Age": []string{"590"}})
	if expires.Sub(now) != 10*time.Second {
		t.Fatalf("Age ignored: %s", expires.Sub(now))
	}
}

func TestMapTilesLimitConcurrencyAndCancelQueuedRequests(t *testing.T) {
	var calls, active, maximum atomic.Int64
	entered := make(chan struct{}, 3)
	s := tileFixture(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		n := active.Add(1)
		for {
			old := maximum.Load()
			if n <= old || maximum.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-r.Context().Done()
		active.Add(-1)
		return nil, r.Context().Err()
	})
	status := enableTiles(t, s)
	done := make(chan error, 3)
	for x := 0; x < 3; x++ {
		go func(x int) { _, err := s.Get(context.Background(), "osm", 2, x, 0, status.Generation); done <- err }(x)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("two requests did not start")
		}
	}
	if _, err := s.SetEnabled(context.Background(), MapTilePolicyRequest{Generation: status.Generation}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled tile succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("queued request survived disable")
		}
	}
	if maximum.Load() > 2 || calls.Load() != 2 {
		t.Fatalf("concurrency=%d calls=%d", maximum.Load(), calls.Load())
	}
}
func TestMapTileRedirectIsNotFollowed(t *testing.T) {
	var calls atomic.Int64
	s := tileFixture(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://other.invalid/tile.png"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	status := enableTiles(t, s)
	if _, err := s.Get(context.Background(), "osm", 0, 0, 0, status.Generation); !errors.Is(err, ErrMapTileUnavailable) || calls.Load() != 1 {
		t.Fatalf("redirect followed calls=%d err=%v", calls.Load(), err)
	}
}
func TestMapTileNoStoreRemovesPreviousCacheAndFullCacheCanRevalidate(t *testing.T) {
	body := tilePNG(t)
	var calls atomic.Int64
	now := time.Now()
	s := tileFixture(t, func(r *http.Request) (*http.Response, error) {
		count := calls.Add(1)
		headers := http.Header{"Content-Type": []string{"image/png"}, "Cache-Control": []string{"max-age=0"}, "Etag": []string{`"a"`}}
		if count > 1 {
			headers.Set("Cache-Control", "no-store")
			return &http.Response{StatusCode: 304, Header: headers, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	s.now = func() time.Time { return now }
	status := enableTiles(t, s)
	if _, err := s.Get(context.Background(), "osm", 0, 0, 0, status.Generation); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.budget = s.cacheBytes
	s.mu.Unlock()
	if _, err := s.Get(context.Background(), "osm", 0, 0, 0, status.Generation); err != nil {
		t.Fatalf("existing cache could not revalidate at budget: %v", err)
	}
	current, err := s.Status(context.Background())
	if err != nil || current.CacheBytes != 0 || calls.Load() != 2 {
		t.Fatalf("no-store cache survived %+v calls=%d err=%v", current, calls.Load(), err)
	}
}

func TestMap304RetainsOriginalCacheRestrictions(t *testing.T) {
	for _, control := range []string{"no-cache", "max-age=0, must-revalidate"} {
		t.Run(control, func(t *testing.T) {
			body := tilePNG(t)
			var calls atomic.Int64
			s := tileFixture(t, func(r *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"image/png"}, "Cache-Control": []string{control}, "Etag": []string{`"v1"`}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
				}
				return &http.Response{StatusCode: 304, Header: http.Header{"Etag": []string{`"v1"`}}, Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			status := enableTiles(t, s)
			for i := 0; i < 3; i++ {
				if _, err := s.Get(context.Background(), "osm", 0, 0, 0, status.Generation); err != nil {
					t.Fatal(err)
				}
			}
			if calls.Load() != 3 {
				t.Fatalf("304 removed original restriction %q: requests=%d", control, calls.Load())
			}
			off, err := s.SetEnabled(context.Background(), MapTilePolicyRequest{Generation: status.Generation})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(context.Background(), "osm", 0, 0, 0, off.Generation); !errors.Is(err, ErrMapTilesDisabled) {
				t.Fatalf("must-revalidate cache reused offline: %v", err)
			}
		})
	}
}

func TestMapTileRouteIsAbsentFromAuthenticatedLANServers(t *testing.T) {
	svc, handler, server := newShortFeedAccessFixture(t)
	if err := svc.SetShortFeedEnabled(true); err != nil {
		t.Fatal(err)
	}
	// All registered phone routes are explicit and none delegates desktop assets.
	for _, route := range server.routes() {
		if strings.Contains(route.pattern, "map-tile") {
			t.Fatal("map route registered on phone server")
		}
	}
	request := shortFeedRequest("GET", "/preview/map-tile/osm/0/0/0.png?generation=x", "", "192.168.1.2:1234")
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, request)
	if out.Header().Get("Content-Type") == "image/png" {
		t.Fatal("phone exposed desktop tile data")
	}
	jellyfin := jellyfinTestServer(t)
	token := jellyfinLogin(t, jellyfin)
	response := jellyfinRequest(jellyfin, "GET", "/preview/map-tile/osm/0/0/0.png?generation=x", token, "")
	if response.Code != 404 {
		t.Fatalf("Jellyfin map route status %d", response.Code)
	}
}
