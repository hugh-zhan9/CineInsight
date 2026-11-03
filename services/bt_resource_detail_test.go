package services

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
	"video-master/services/btclient"
)

func TestBTResourceDetailParsesSafeBody(t *testing.T) {
	body := `[b]简介[/b]<script>secret-script</script><iframe src="https://evil.example"></iframe><p>第二段</p>[img]https://images.example/a.jpg?key=secret[/img]<img src="/screen.png"><a href="download.php?passkey=secret">链接文字</a>`
	raw, _ := json.Marshal(map[string]any{"ret": 0, "data": map[string]any{"data": map[string]any{"id": 42, "extra": map[string]string{"descr": body, "media_info": "General\nVideo: HEVC"}, "download_url": "secret-download"}}})
	detail, images, err := parseBTResourceDetail(raw, "json", "https://www.nicept.net/api/v1/detail/42")
	if err != nil || !strings.Contains(detail.Description, "简介") || detail.MediaInfo != "General\nVideo: HEVC" || len(images) != 2 {
		t.Fatalf("parse: %v images=%d", err, len(images))
	}
	if strings.Contains(detail.Description, "secret") || strings.Contains(detail.Description, "script") || strings.Contains(detail.Description, "[b]") {
		t.Fatal("unsafe content escaped")
	}
	if images[1] != "https://www.nicept.net/screen.png" {
		t.Fatal("relative image incorrect")
	}
	for _, bad := range []string{`{}`, `{"ret":0,"data":null}`, `<html>login</html>`} {
		if _, _, err := parseBTResourceDetail([]byte(bad), "json", "https://www.nicept.net/"); err == nil {
			t.Error("invalid response accepted")
		}
	}
	htmlBody := `<html><form>password</form><div id="kdescr"><p>真实介绍</p><pre>Video: AVC</pre><img src="/image.png"><script>evil</script></div></html>`
	detail, images, err = parseBTResourceDetail([]byte(htmlBody), "html", "https://www.nicept.net/details.php?id=42")
	if err != nil || !strings.Contains(detail.Description, "Video: AVC") || strings.Contains(detail.Description, "password") || strings.Contains(detail.Description, "evil") || len(images) != 1 {
		t.Fatal("HTML parsing failed")
	}
	if _, _, err := parseBTResourceDetail([]byte(`<form>login</form>`), "html", "https://www.nicept.net/"); err == nil {
		t.Fatal("login page accepted")
	}
	many := strings.Repeat(`<img src="https://images.example/one.png">`, 20)
	_, images, err = parseBTResourceDetail([]byte(`<div id="kdescr">`+many+`</div>`), "html", "https://www.nicept.net/")
	if err != nil || len(images) != 1 {
		t.Fatal("duplicates not bounded")
	}
}

type btDetailSource struct {
	btFakeSearch
	calls int
}

func (f *btDetailSource) SiteDetail(context.Context, int, string) (btclient.SiteDetailRequest, error) {
	f.calls++
	return btclient.SiteDetailRequest{URL: "https://www.nicept.net/api/v1/detail/42", Format: "json", Headers: http.Header{"Authorization": []string{"Bearer private"}}}, nil
}
func TestBTResourceDetailTokenLifetimeAndImages(t *testing.T) {
	source := &btDetailSource{}
	s := NewBTSearchService(source, 1)
	// A live opaque token is mandatory; the URL remains private in its entry.
	token := "ci-bt-" + strings.Repeat("a", 48)
	s.tokens[token] = btSearchToken{release: btclient.Release{IndexerID: 1, InfoURL: "https://www.nicept.net/details.php?id=42"}, expires: time.Now().Add(time.Minute), generation: 1, order: 1}
	calls := 0
	s.fetchDetail = func(context.Context, btclient.SiteDetailRequest) ([]byte, error) {
		calls++
		return []byte(`{"ret":0,"data":{"data":{"id":42,"extra":{"descr":"body private [img]https://images.example/a.png[/img]","media_info":"HEVC"}}}}`), nil
	}
	detail, err := s.ResourceDetail(context.Background(), token)
	if err != nil || strings.Contains(detail.Description, "private") || len(detail.ImagePaths) != 1 || strings.Contains(detail.ImagePaths[0], "images.example") {
		t.Fatalf("detail %v", err)
	}
	detail.ImagePaths[0] = "mutated"
	again, err := s.ResourceDetail(context.Background(), token)
	if err != nil || calls != 2 || strings.Contains(again.ImagePaths[0], "mutated") {
		t.Fatal("snapshot mutated")
	}
	s.fetchPoster = func(_ context.Context, address string) ([]byte, string, error) {
		if address != "https://images.example/a.png" {
			t.Error("wrong image")
		}
		return []byte("image"), "image/png", nil
	}
	if _, _, err := s.DetailImage(context.Background(), token, 2, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DetailImage(context.Background(), token, 2, 1); !errors.Is(err, ErrBTResultExpired) {
		t.Fatal("index accepted")
	}
	s.Configure(source, 2)
	if _, err := s.ResourceDetail(context.Background(), token); !errors.Is(err, ErrBTResultExpired) {
		t.Fatal("expired details accepted")
	}
	if source.calls != 2 {
		t.Fatal("invalid token read credentials")
	}
}
func TestBTResourceDetailDiscardsLateResponse(t *testing.T) {
	source := &btDetailSource{}
	s := NewBTSearchService(source, 1)
	token := "opaque"
	s.tokens[token] = btSearchToken{expires: time.Now().Add(time.Minute), generation: 1, order: 1}
	entered, release := make(chan struct{}), make(chan struct{})
	s.fetchDetail = func(context.Context, btclient.SiteDetailRequest) ([]byte, error) {
		close(entered)
		<-release
		return []byte(`{"ret":0,"data":{"data":{"id":42,"extra":{"descr":"late"}}}}`), nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.ResourceDetail(context.Background(), token); done <- err }()
	<-entered
	s.Configure(source, 2)
	close(release)
	if err := <-done; !errors.Is(err, ErrBTResultExpired) {
		t.Fatal("late details published")
	}
}

func TestBTResourceDetailRejectsFailedWrongAndIncompletePayloads(t *testing.T) {
	for _, raw := range []string{
		`{"ret":-1,"data":{"data":{"id":42,"extra":{"descr":"failed"}}}}`,
		`{"ret":0,"data":{"data":{"id":43,"extra":{"descr":"wrong"}}}}`,
		`{"ret":0,"data":{"data":{"id":null,"extra":{"descr":"wrong"}}}}`,
		`{"ret":0,"data":{"data":{"id":42}}}`,
		`{"ret":0,"data":{"data":{"id":42,"extra":{}}}}`,
	} {
		if _, _, err := parseBTResourceDetail([]byte(raw), "json", "https://www.nicept.net/api/v1/detail/42"); err == nil {
			t.Error("failed/wrong/missing payload accepted")
		}
	}
	if _, _, err := parseBTResourceDetail([]byte(`{"ret":0,"data":{"data":{"id":42,"extra":{"descr":"","media_info":null}}}}`), "json", "https://www.nicept.net/api/v1/detail/42"); err != nil {
		t.Fatal("valid empty rejected")
	}
}

func TestBTDetailImageRejectsPriorBodyAndLateRefresh(t *testing.T) {
	s := NewBTSearchService(&btDetailSource{}, 1)
	token := "opaque"
	s.tokens[token] = btSearchToken{expires: time.Now().Add(time.Minute), generation: 1, order: 1, detailOrder: 2, images: []string{"https://images.example/a.png"}}
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	s.fetchPoster = func(context.Context, string) ([]byte, string, error) {
		calls++
		close(entered)
		<-release
		return []byte("old-image"), "image/png", nil
	}
	if _, _, err := s.DetailImage(context.Background(), token, 1, 0); !errors.Is(err, ErrBTResultExpired) || calls != 0 {
		t.Fatal("old body fetched")
	}
	done := make(chan error, 1)
	go func() { _, _, err := s.DetailImage(context.Background(), token, 2, 0); done <- err }()
	<-entered
	s.mu.Lock()
	entry := s.tokens[token]
	entry.detailOrder = 3
	s.tokens[token] = entry
	s.mu.Unlock()
	close(release)
	if err := <-done; !errors.Is(err, ErrBTResultExpired) {
		t.Fatal("late image published")
	}
}

func TestBTDetailDoesNotBlockTasksAndMaintenanceCancelsRead(t *testing.T) {
	service, _ := newBTTaskTest(t)
	out, err := service.Search(context.Background(), "film", nil)
	if err != nil {
		t.Fatal(err)
	}
	source := &btDetailSource{}
	service.search.mu.Lock()
	service.search.source = source
	service.search.mu.Unlock()
	started := make(chan struct{})
	service.search.fetchDetail = func(ctx context.Context, _ btclient.SiteDetailRequest) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := service.ResourceDetail(context.Background(), out.Resources[0].Token); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := service.List(ctx, "", 10); err != nil {
		t.Fatal("detail blocked task list", err)
	}
	if err := service.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrBTMaintenance) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("detail not cancelled")
	}
}

func TestBTDetailTransportCredentialsLimitsAndRedirects(t *testing.T) {
	status := 200
	body := []byte(`{"ret":0}`)
	calls := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Host != "example.com" || r.TLS.ServerName != "example.com" || r.Header.Get("Authorization") != "Bearer site-secret" || r.Header.Get("X-Api-Key") != "" {
			t.Error("authority/credential mismatch")
		}
		w.Header().Set("Location", "https://other.example/private")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	factory := func() (*http.Client, error) {
		transport := upstream.Client().Transport.(*http.Transport).Clone()
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "8.8.8.8:443" {
				t.Error("destination not pinned")
			}
			return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
		}
		return &http.Client{Transport: transport}, nil
	}
	lookup := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	headers := http.Header{"Authorization": []string{"Bearer site-secret"}}
	if _, _, err := fetchBTPublic(context.Background(), "https://example.com/api/v1/detail/42", headers, 2<<20, lookup, factory); err != nil {
		t.Fatal(err)
	}
	before := calls
	status = 302
	if _, _, err := fetchBTPublic(context.Background(), "https://example.com/api/v1/detail/42", headers, 2<<20, lookup, factory); err == nil || calls != before+1 {
		t.Fatal("redirect followed")
	}
	status = 403
	if _, _, err := fetchBTPublic(context.Background(), "https://example.com/api/v1/detail/42", headers, 2<<20, lookup, factory); !errors.Is(err, btclient.ErrAuthentication) {
		t.Fatal("auth error lost")
	}
	status = 200
	body = []byte(strings.Repeat("x", (2<<20)+1))
	if _, _, err := fetchBTPublic(context.Background(), "https://example.com/api/v1/detail/42", headers, 2<<20, lookup, factory); err == nil {
		t.Fatal("oversize accepted")
	}
	fakeDNS := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("198.18.0.185")}, nil
	}
	before = calls
	if _, _, err := fetchBTPublic(context.Background(), "https://example.com/api/v1/detail/42", headers, 2<<20, fakeDNS, factory); !errors.Is(err, errBTUnsafeResolution) || before != calls {
		t.Fatal("unsafe fake-IP accepted")
	}
}
