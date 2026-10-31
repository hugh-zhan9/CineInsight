package services

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"

	"strings"
	"testing"
	"time"
	"video-master/services/btclient"
)

func TestBTPosterDestinationAndContent(t *testing.T) {
	var imageBytes bytes.Buffer
	_ = png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	body, status, calls := imageBytes.Bytes(), http.StatusOK, 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Host != "images.example" {
			t.Errorf("destination URL=%s Host=%s", r.URL.Host, r.Host)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials escaped")
		}
		w.Header().Set("Location", "http://127.0.0.1/private")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	defer proxy.Close()
	factory := func() (*http.Client, error) {
		return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "8.8.8.8:80" {
				t.Error("destination not pinned:", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, proxy.Listener.Addr().String())
		}}}, nil
	}
	lookup := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	data, mime, err := fetchBTPoster(context.Background(), "http://images.example/cover?signature=private", lookup, factory)
	if err != nil || mime != "image/png" || !bytes.Equal(data, body) {
		t.Fatalf("valid image: %s %v", mime, err)
	}
	for _, bad := range []string{"file:///private", "http://user:secret@images.example/x", "http://images.example:8080/x", "https://images.example/x#private"} {
		before := calls
		if _, _, err := fetchBTPoster(context.Background(), bad, lookup, factory); err == nil || before != calls {
			t.Fatal("unsafe URL fetched")
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.64.0.1", "::1", "::ffff:127.0.0.1", "2001:db8::1", "64:ff9b:1::a00:1", "100::1", "3fff::1"} {
		privateLookup := func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr(ip)}, nil }
		before := calls
		if _, _, err := fetchBTPoster(context.Background(), "http://images.example/x", privateLookup, factory); err == nil || calls != before {
			t.Fatalf("private resolution accepted: %s", ip)
		}
	}
	status = http.StatusFound
	if _, _, err := fetchBTPoster(context.Background(), "http://images.example/x", lookup, factory); err == nil {
		t.Fatal("redirect accepted")
	}
	status = http.StatusOK
	for _, bad := range [][]byte{nil, []byte("<svg xmlns='http://www.w3.org/2000/svg'></svg>"), bytes.Repeat([]byte{'x'}, btPosterMaxBytes+1)} {
		body = bad
		if _, _, err := fetchBTPoster(context.Background(), "http://images.example/x", lookup, factory); err == nil {
			t.Fatal("invalid/oversize content accepted")
		}
	}
}

func TestBTPosterExpiredNeverResolves(t *testing.T) {
	s := NewBTSearchService(&btFakeSearch{}, 1)
	if _, _, err := s.Poster(context.Background(), "unknown"); err != ErrBTResultExpired {
		t.Fatal(err)
	}
	out, err := s.Search(context.Background(), "film", []int{1})
	if err != nil {
		t.Fatal(err)
	}
	s.Configure(&btFakeSearch{}, 2)
	if _, _, err := s.Poster(context.Background(), out.Resources[0].Token); err != ErrBTResultExpired {
		t.Fatal(err)
	}
}

type btLatestResults struct{ btFakeSearch }

func (s *btLatestResults) Search(_ context.Context, _ string, id, limit int) ([]btclient.Release, error) {
	old, newer := "2025-01-01T00:00:00Z", "2026-10-31T00:00:00Z"
	if id == 2 {
		return []btclient.Release{{Title: "newest", IndexerID: 2, Protocol: "torrent", DownloadURL: "secret", PublishDate: &newer, PosterURL: "https://images.example/private?key=secret"}}, nil
	}
	rows := make([]btclient.Release, 500)
	for i := range rows {
		rows[i] = btclient.Release{Title: "older", IndexerID: id, Protocol: "torrent", DownloadURL: "secret", PublishDate: &old}
	}
	rows[0].PublishDate = nil
	return rows, nil
}
func TestBTLatestSortsAcrossStationsBeforeLimit(t *testing.T) {
	s := NewBTSearchService(&btLatestResults{}, 1)
	out, err := s.Search(context.Background(), "", []int{1, 2})
	if err != nil || len(out.Resources) != 500 || !out.Truncated {
		t.Fatalf("%v %v", out, err)
	}
	first := out.Resources[0]
	if first.Title != "newest" || !strings.HasPrefix(first.PosterPath, "/preview/bt-poster/ci-bt-") {
		t.Fatalf("latest/poster lost: %+v", first)
	}
	if _, err := s.Fetch(context.Background(), first.Token); err != nil {
		t.Fatal(err)
	}
	if out.Resources[499].PublishTime == nil {
		t.Fatal("undated resource sorted before dated ones")
	}
}

func TestBTPosterHTTPSProxyPinsConnectAndVerifiesTLS(t *testing.T) {
	var imageBytes bytes.Buffer
	_ = png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "example.com" || r.TLS.ServerName != "example.com" {
			t.Error("original authority lost")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Authorization") != "" {
			t.Error("credential leak")
		}
		_, _ = w.Write(imageBytes.Bytes())
	}))
	defer upstream.Close()
	connects := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connects++
		if r.Method != "CONNECT" || r.Host != "8.8.8.8:443" {
			t.Error("CONNECT destination not pinned", r.Method, r.Host)
			http.Error(w, "bad", 400)
			return
		}
		remote, err := net.Dial("tcp", upstream.Listener.Addr().String())
		if err != nil {
			t.Error(err)
			return
		}
		local, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			remote.Close()
			t.Error(err)
			return
		}
		_, _ = local.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() { _, _ = io.Copy(remote, local); remote.Close() }()
		go func() { _, _ = io.Copy(local, remote); local.Close() }()
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	factory := func() (*http.Client, error) {
		tr := upstream.Client().Transport.(*http.Transport).Clone()
		tr.Proxy = http.ProxyURL(proxyURL)
		return &http.Client{Transport: tr}, nil
	}
	lookup := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	if _, _, err := fetchBTPoster(context.Background(), "https://example.com/x", lookup, factory); err != nil {
		t.Fatal(err)
	}
	before := connects
	if _, _, err := fetchBTPoster(context.Background(), "http://example.com/x", lookup, factory); err == nil || connects != before {
		t.Fatal("HTTP proxy must not resolve an unpinned hostname or bypass proxy")
	}
	if _, _, err := fetchBTPoster(context.Background(), "https://wrong.example/x", lookup, factory); err == nil {
		t.Fatal("invalid TLS identity accepted")
	}
}

func TestBTPosterDiscardsInflightAfterInvalidation(t *testing.T) {
	for _, invalidate := range []string{"generation", "evicted"} {
		t.Run(invalidate, func(t *testing.T) {
			s := NewBTSearchService(&btFakeSearch{}, 1)
			out, err := s.Search(context.Background(), "film", []int{1})
			if err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}), make(chan struct{})
			s.fetchPoster = func(context.Context, string) ([]byte, string, error) {
				close(started)
				<-release
				return []byte("image"), "image/png", nil
			}
			result := make(chan error, 1)
			go func() { _, _, err := s.Poster(context.Background(), out.Resources[0].Token); result <- err }()
			<-started
			if invalidate == "generation" {
				s.Configure(&btFakeSearch{}, 2)
			} else {
				s.mu.Lock()
				delete(s.tokens, out.Resources[0].Token)
				s.mu.Unlock()
			}
			close(release)
			if err := <-result; err != ErrBTResultExpired {
				t.Fatal("stale poster served", err)
			}
		})
	}
}

func TestBTPosterDoesNotBlockTasksAndMaintenanceCancelsImages(t *testing.T) {
	s, _ := newBTTaskTest(t)
	out, err := s.Search(context.Background(), "film", nil)
	if err != nil || len(out.Resources) == 0 {
		t.Fatalf("search: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{}, 2)
	s.search.fetchPoster = func(ctx context.Context, _ string) ([]byte, string, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, "", ctx.Err()
	}
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, _, err := s.Poster(ctx, out.Resources[0].Token); done <- err }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("images were serialized with task commands")
		}
	}
	listCtx, listCancel := context.WithTimeout(ctx, time.Second)
	defer listCancel()
	if _, err := s.List(listCtx, "", 10); err != nil {
		t.Fatal("slow images blocked task list", err)
	}
	if err := s.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != ErrBTMaintenance {
				t.Fatal("maintenance did not invalidate image", err)
			}
		case <-ctx.Done():
			t.Fatal("image not cancelled")
		}
	}
}
