package btclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func qbTestProfile(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/api/v2/app/version":
		w.Write([]byte("v5.2.3"))
		return true
	case "/api/v2/app/webapiVersion":
		w.Write([]byte("2.15.1"))
		return true
	}
	return false
}

func TestQBStoppedAddAndExplicitIndices(t *testing.T) {
	hash := strings.Repeat("a", 40)
	actions := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if qbTestProfile(w, r) {
			return
		}
		actions = append(actions, r.URL.Path)
		switch r.URL.Path {
		case "/api/v2/torrents/add":
			if err := r.ParseMultipartForm(20 << 20); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("stopped") != "true" || r.FormValue("autoTMM") != "false" || r.FormValue("contentLayout") != "Original" || r.FormValue("useDownloadPath") != "false" || r.FormValue("rename") != "" {
				t.Error("unsafe add flags")
			}
			if _, _, err := r.FormFile("torrents"); err != nil {
				t.Error(err)
			}
			w.Write([]byte(`{"success_count":1,"failure_count":0,"pending_count":0,"added_torrent_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]}`))
		case "/api/v2/torrents/filePrio":
			r.ParseForm()
			if r.Form.Get("hash") != hash || r.Form.Get("id") != "4|8" || r.Form.Get("priority") != "1" {
				t.Error("wrong file IDs")
			}
		case "/api/v2/torrents/delete":
			r.ParseForm()
			if r.Form.Get("deleteFiles") != "false" {
				t.Error("destructive delete")
			}
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer server.Close()
	client, _ := New(server.URL, "")
	defer client.Close()
	ctx := context.Background()
	if _, err := client.QBAddStopped(ctx, QBAddOptions{Torrent: []byte("fixture"), SavePath: "/tmp/fixture", OwnerTag: "owned"}); err != nil {
		t.Fatal(err)
	}
	if err := client.QBPriority(ctx, hash, []int{4, 8}, 1); err != nil {
		t.Fatal(err)
	}
	if err := client.QBDeleteMetadata(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if err := client.QBRun(ctx, "all", true); err == nil {
		t.Fatal("broad selector accepted")
	}
	if len(actions) != 3 {
		t.Fatal(actions)
	}
}

func TestQBAddRequiresVersionedSingleResult(t *testing.T) {
	for _, body := range []string{`Ok.`, `{}`, `{"success_count":0,"failure_count":1,"pending_count":0,"added_torrent_ids":[]}`, `{"success_count":1,"failure_count":0,"pending_count":1,"added_torrent_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]}`, `{"success_count":2,"failure_count":0,"pending_count":0,"added_torrent_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]}`} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if qbTestProfile(w, r) {
					return
				}
				calls++
				w.Write([]byte(body))
			}))
			defer server.Close()
			c, _ := New(server.URL, "")
			defer c.Close()
			if _, err := c.QBAddStopped(context.Background(), QBAddOptions{Torrent: []byte("fixture"), SavePath: "/tmp/fixture", OwnerTag: "owned"}); err == nil {
				t.Fatal("ambiguous add accepted")
			}
			if calls != 1 {
				t.Fatal("write was retried")
			}
		})
	}
}

func TestQBMetadataBuildGateAndAtomicStopCondition(t *testing.T) {
	for _, version := range []string{"2.0.13.0", "2.0.10.0", "1.2.19.0", ""} {
		t.Run(version, func(t *testing.T) {
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/app/version":
					w.Write([]byte("v5.2.3"))
				case "/api/v2/app/webapiVersion":
					w.Write([]byte("2.15.1"))
				case "/api/v2/app/buildInfo":
					w.Write([]byte(`{"libtorrent":"` + version + `"}`))
				case "/api/v2/torrents/add":
					writes++
					r.ParseMultipartForm(1 << 20)
					if r.FormValue("stopped") != "false" || r.FormValue("stopCondition") != "MetadataReceived" || r.FormValue("urls") == "" {
						t.Error("metadata gate lost atomic stop condition")
					}
					w.Write([]byte(`{"success_count":1,"failure_count":0,"pending_count":0,"added_torrent_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]}`))
				default:
					t.Error("unexpected capability request", r.URL.Path)
				}
			}))
			defer server.Close()
			c, _ := New(server.URL, "")
			defer c.Close()
			hash, err := c.QBAddStopped(context.Background(), QBAddOptions{Magnet: "magnet:?xt=urn:btih:" + strings.Repeat("a", 40), MetadataOnly: true, SavePath: "/tmp/fixture", OwnerTag: "owned"})
			if version == "2.0.13.0" {
				if err != nil || writes != 1 || hash != strings.Repeat("a", 40) {
					t.Fatal("verified metadata profile rejected", err)
				}
			} else if err == nil || writes != 0 {
				t.Fatal("unverified metadata build wrote a task")
			}
		})
	}
}

func TestQBFilesAcceptsLargeBoundedManifestAndRejectsOversize(t *testing.T) {
	hash := strings.Repeat("a", 40)
	rows := make([]QBFile, qbFileCountLimit)
	for i := range rows {
		rows[i] = QBFile{Index: i, Name: strings.Repeat("d", 200) + "/" + fmt.Sprintf("%0200d", i), Size: 1, Priority: 1}
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 4<<20 {
		t.Fatal("fixture does not cross old response limit")
	}
	t.Run("legal", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(raw) }))
		defer server.Close()
		c, _ := New(server.URL, "")
		defer c.Close()
		got, err := c.QBFiles(context.Background(), hash)
		if err != nil || len(got) != qbFileCountLimit || got[len(got)-1].Name != rows[len(rows)-1].Name {
			t.Fatal("legal manifest rejected", err)
		}
	})
	t.Run("wire_limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Stream padding to avoid a second response-sized server allocation.
			chunk := strings.Repeat(" ", 1<<20)
			for written := 0; written <= qbFilesResponseLimit; written += len(chunk) {
				if _, err := io.WriteString(w, chunk); err != nil {
					return
				}
			}
		}))
		defer server.Close()
		c, _ := New(server.URL, "")
		defer c.Close()
		if _, err := c.QBFiles(context.Background(), hash); !errors.Is(err, ErrResponseSize) {
			t.Fatal("wire budget not enforced", err)
		}
	})
	t.Run("decoded_paths", func(t *testing.T) {
		excessive := []QBFile{{Index: 0, Name: strings.Repeat("x", qbFilePathByteLimit+1)}}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(excessive) }))
		defer server.Close()
		c, _ := New(server.URL, "")
		defer c.Close()
		if _, err := c.QBFiles(context.Background(), hash); !errors.Is(err, ErrResponseSize) {
			t.Fatal("decoded path budget not enforced", err)
		}
	})
}

func TestQBWriteRechecksProfileAfterClientRestart(t *testing.T) {
	var version atomic.Value
	version.Store("v5.2.3")
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/version":
			w.Write([]byte(version.Load().(string)))
		case "/api/v2/app/webapiVersion":
			w.Write([]byte("2.15.1"))
		default:
			writes++
			w.Write([]byte(`{"success_count":1,"failure_count":0,"pending_count":0,"added_torrent_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]}`))
		}
	}))
	defer server.Close()
	c, _ := New(server.URL, "")
	defer c.Close()
	ctx := context.Background()
	if err := c.CheckQBProfile(ctx); err != nil {
		t.Fatal(err)
	}
	version.Store("v4.6.7")
	if _, err := c.QBAddStopped(ctx, QBAddOptions{Torrent: []byte("fixture"), SavePath: "/tmp/fixture", OwnerTag: "owned"}); !errors.Is(err, ErrVersionUnsupported) {
		t.Fatal("stale stopped capability reused", err)
	}
	if err := c.QBRun(ctx, strings.Repeat("a", 40), true); !errors.Is(err, ErrVersionUnsupported) {
		t.Fatal("stale action capability reused", err)
	}
	if writes != 0 {
		t.Fatal("unsupported replacement client received writes")
	}
}

func TestQBFilesStreamingShapeAndCountBoundaries(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `[null]`, `[{}]`, `[{"index":0,"name":"x","size":-1}]`, `[{"index":0,"name":"x","progress":2}]`, `[{"index":0,"name":"x"},{"index":0,"name":"y"}]`, `[] {}`, `[]junk`, `[`} {
		if _, err := decodeQBFiles([]byte(raw)); err == nil {
			t.Fatal("invalid file array accepted", raw)
		}
	}
	if files, err := decodeQBFiles([]byte("[] \n\t")); err != nil || len(files) != 0 {
		t.Fatal("empty bounded response rejected", err)
	}
	var body strings.Builder
	body.WriteByte('[')
	for i := 0; i < qbFileCountLimit; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"index":%d,"name":"f%d","size":1}`, i, i)
	}
	fmt.Fprintf(&body, `,{"index":%d,"name":"`, qbFileCountLimit)
	body.WriteString(strings.Repeat("x", 4<<20))
	body.WriteString(`","size":1}]`)
	raw := []byte(body.String())
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := decodeQBFiles(raw)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrResponse) {
		t.Fatal("10001st item was not rejected", err)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > 12<<20 {
		t.Fatalf("10001st object was allocated before row limit: %d bytes", allocated)
	}
	t.Logf("count_guard_decode_allocated=%d", allocated)
}
func TestQBFilesShortObjectFloodHasBoundedDecodeAllocation(t *testing.T) {
	// The wire buffer is already bounded/read. A million short objects must not
	// allocate a million QBFile structs before count/field validation runs.
	raw := []byte("[" + strings.Repeat("{},", 1_000_000) + "{}]")
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := decodeQBFiles(raw)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("empty object flood accepted")
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > 1<<20 {
		t.Fatalf("short-object decoding allocated %d bytes", allocated)
	}
	t.Logf("short-object flood wire_bytes=%d decode_allocated=%d", len(raw), allocated)
}
