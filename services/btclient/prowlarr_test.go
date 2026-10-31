package btclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchAndProxyTorrent(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/p/api/v1/search":
			if r.URL.Query().Get("indexerIds") != "2" || r.URL.Query().Get("query") != "film" {
				t.Error(r.URL)
			}
			w.Write([]byte(`[{"title":"film","indexerId":2,"size":42,"protocol":"torrent"}]`))
		case "/p/2/download":
			if len(r.URL.Query()) != 1 || r.Header.Get("X-Api-Key") != "configured" {
				t.Error("wrong credential route")
			}
			w.Write([]byte("d4:infodee"))
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer server.Close()
	c, _ := New(server.URL+"/p", "configured")
	defer c.Close()
	rows, err := c.Search(context.Background(), "film", 2, 10)
	if err != nil || len(rows) != 1 || rows[0].Size != 42 {
		t.Fatalf("%+v %v", rows, err)
	}
	data, err := c.Torrent(context.Background(), server.URL+"/p/2/download?link=opaque&apikey=old&ApiKey=hidden&token=private", 2)
	if err != nil || len(data) == 0 {
		t.Fatal(err)
	}
	for _, u := range []string{"http://example.com/2/download?link=x", server.URL + "/p/3/download?link=x", server.URL + "/p/2/download", server.URL + "/other/2/download?link=x"} {
		if _, err := c.Torrent(context.Background(), u, 2); !errors.Is(err, ErrEndpoint) {
			t.Fatalf("accepted %s: %v", u, err)
		}
	}
	if calls != 2 {
		t.Fatalf("rejected URL was fetched: %d", calls)
	}
}
func TestEmptySearchAndWrongIndexer(t *testing.T) {
	body := "[]"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	defer server.Close()
	c, _ := New(server.URL, "")
	defer c.Close()
	rows, err := c.Search(context.Background(), "film", 1, 1)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("%+v %v", rows, err)
	}
	body = `[{"indexerId":2,"size":1}]`
	if _, err := c.Search(context.Background(), "film", 1, 1); !errors.Is(err, ErrResponse) {
		t.Fatal(err)
	}
}

func TestLatestSearchPreservesPoster(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/search" || r.URL.Query().Get("query") != "" || r.URL.Query().Get("type") != "search" {
			t.Error("latest must use a read-only empty search")
		}
		w.Write([]byte(`[{"indexerId":1,"size":1,"posterUrl":"https://images.example/cover.jpg"}]`))
	}))
	defer server.Close()
	c, _ := New(server.URL, "")
	defer c.Close()
	rows, err := c.Search(context.Background(), "", 1, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("latest: %+v %v", rows, err)
	}
	raw, _ := json.Marshal(rows[0])
	if !strings.Contains(string(raw), "https://images.example/cover.jpg") {
		t.Fatal("upstream poster was lost")
	}
}

func TestTypedMagnetProxy(t *testing.T) {
	location := "magnet:?xt=urn:btih:0123456789012345678901234567890123456789"
	status := 302
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", location)
		w.WriteHeader(status)
		if status == 200 {
			w.Write([]byte(location))
		}
	}))
	defer server.Close()
	c, _ := New(server.URL, "")
	defer c.Close()
	for _, code := range []int{302, 200} {
		status = code
		result, err := c.FetchTorrent(context.Background(), server.URL+"/1/download?link=x", 1)
		if err != nil || result.Magnet != location || result.Torrent != nil {
			t.Fatalf("%+v %v", result, err)
		}
	}
	status = 302
	location = "https://tracker.example/private"
	if _, err := c.FetchTorrent(context.Background(), server.URL+"/1/download?link=x", 1); !errors.Is(err, ErrResponse) {
		t.Fatal(err)
	}
}

func TestPublishDateValidation(t *testing.T) {
	body := `[{"indexerId":1,"size":1,"publishDate":"2026-10-30T08:00:00+08:00"}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	defer server.Close()
	c, _ := New(server.URL, "")
	defer c.Close()
	rows, err := c.Search(context.Background(), "film", 1, 1)
	if err != nil || rows[0].PublishDate == nil || *rows[0].PublishDate != "2026-10-30T00:00:00Z" {
		t.Fatalf("publish time: %+v %v", rows, err)
	}
	for _, value := range []string{`null`, `""`} {
		body = `[{"indexerId":1,"size":1,"publishDate":` + value + `}]`
		rows, err = c.Search(context.Background(), "film", 1, 1)
		if err != nil || rows[0].PublishDate != nil {
			t.Fatalf("empty publish time: %+v %v", rows, err)
		}
	}
	body = `[{"indexerId":1,"size":1,"publishDate":"not-a-date"}]`
	if _, err = c.Search(context.Background(), "film", 1, 1); !errors.Is(err, ErrResponse) {
		t.Fatal(err)
	}
}

func TestProwlarrVersionProjectsOnlyBoundedActualVersion(t *testing.T) {
	body := `{"version":"3.2.1.5076","appData":"secret-private-path","apiKey":"secret"}`
	status := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/system/status" {
			t.Error("unexpected request")
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer server.Close()
	c, _ := New(server.URL, "")
	defer c.Close()
	version, err := c.ProwlarrVersion(context.Background())
	if err != nil || version != "3.2.1.5076" {
		t.Fatal(version, err)
	}
	for _, invalid := range []string{`{}`, `{"version":""}`, `{"version":"bad\nvalue"}`, strings.Repeat("x", 65537)} {
		body = invalid
		if version, err = c.ProwlarrVersion(context.Background()); err == nil || version != "" {
			t.Fatal("invalid version exposed", version, err)
		}
	}
	status = 500
	body = "secret-upstream-body"
	_, err = c.ProwlarrVersion(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("upstream error leaked", err)
	}
}
