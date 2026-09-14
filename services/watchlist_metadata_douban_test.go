package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 按 2026-09-14 真实搜索 / 移动网页接口裁剪，保留剧集也标 type=movie 的形态。
const doubanSearchFixture = `[{"id":"3001114","title":"沙丘","sub_title":"Dune","year":"2021","type":"movie","episode":"","img":"https://img9.doubanio.com/poster.jpg"},{"id":"34428979","title":"沙丘：预言 第一季","type":"movie","episode":"6"}]`
const doubanDetailFixture = `{"id":"3001114","title":"沙丘","original_title":"Dune","year":"2021","intro":"电影《沙丘》为观众呈现了一段神秘而感人至深的英雄之旅。","rating":{"value":7.7},"genres":["剧情","科幻","冒险"],"directors":[{"name":"丹尼斯·维伦纽瓦"}],"actors":[{"name":"提莫西·查拉梅"}],"type":"movie","is_tv":false,"pic":{"large":"https://img9.doubanio.com/poster.jpg"}}`

func doubanStub(t *testing.T, handler http.HandlerFunc) *DoubanWatchlistMetadataSource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewWatchlistMetadataHTTPClient(WatchlistMetadataConfig{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	source := NewDoubanWatchlistMetadataSource(client)
	source.searchBaseURL = server.URL
	source.detailBaseURL = server.URL
	return source
}

func TestWatchlistMetadataDoubanSearchAndDetail(t *testing.T) {
	source := doubanStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") == "" || r.Header.Get("User-Agent") == "" {
			t.Error("missing request headers")
		}
		switch r.URL.Path {
		case "/j/subject_suggest":
			if r.URL.Query().Get("q") != "沙丘" {
				t.Error("search query lost")
			}
			w.Write([]byte(doubanSearchFixture))
		case "/rexxar/api/v2/movie/3001114":
			w.Write([]byte(doubanDetailFixture))
		default:
			t.Error("unexpected path", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	ctx := context.Background()
	items, err := source.Search(ctx, WatchlistMetadataKindMovie, "沙丘")
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if items[0].SourceName != "douban" || items[0].OriginalTitle != "Dune" || items[0].Year != 2021 {
		t.Fatal(items)
	}
	detail, err := source.Detail(ctx, WatchlistMetadataKindMovie, items[0].SourceItemID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Title != "沙丘" || detail.Year != 2021 || detail.Rating != 7.7 || detail.Overview == "" || detail.PosterURL == "" || len(detail.Genres) != 3 || len(detail.Directors) != 1 || len(detail.Cast) != 1 {
		t.Fatalf("incomplete mapping: %+v", detail)
	}
}

func TestWatchlistMetadataDoubanFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		search     bool
		want       WatchlistMetadataFailure
	}{
		{"empty search", "[]", 200, true, WatchlistMetadataFailureNotFound},
		{"null search", "null", 200, true, WatchlistMetadataFailureSourceError},
		{"changed search", `{"results":[]}`, 200, true, WatchlistMetadataFailureSourceError},
		{"missing search id", `[{"type":"movie","title":"沙丘"}]`, 200, true, WatchlistMetadataFailureSourceError},
		{"explicit missing", `{"code":404,"msg":"traversal_error"}`, 404, false, WatchlistMetadataFailureNotFound},
		{"search endpoint removed", `{"code":404,"msg":"traversal_error"}`, 404, true, WatchlistMetadataFailureSourceError},
		{"html 404", `<html>not found</html>`, 404, false, WatchlistMetadataFailureSourceError},
		{"challenge", `<html>验证</html>`, 200, false, WatchlistMetadataFailureSourceError},
		{"forbidden", "", 403, false, WatchlistMetadataFailureSourceError},
		{"rate limited", "", 429, false, WatchlistMetadataFailureSourceError},
		{"wrong id", strings.ReplaceAll(doubanDetailFixture, "3001114", "9999999"), 200, false, WatchlistMetadataFailureSourceError},
		{"tv detail", strings.ReplaceAll(doubanDetailFixture, `"is_tv":false`, `"is_tv":true`), 200, false, WatchlistMetadataFailureNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := doubanStub(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) })
			var err error
			if tc.search {
				_, err = source.Search(context.Background(), WatchlistMetadataKindMovie, "沙丘")
			} else {
				_, err = source.Detail(context.Background(), WatchlistMetadataKindMovie, "3001114")
			}
			if got := WatchlistMetadataFailureOf(err); got != tc.want {
				t.Fatalf("got %s want %s: %v", got, tc.want, err)
			}
		})
	}
}

func TestWatchlistMetadataDoubanRejectsUnsupportedInputBeforeRequest(t *testing.T) {
	source := doubanStub(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") })
	if _, err := source.Search(context.Background(), WatchlistMetadataKindTV, "沙丘"); err == nil {
		t.Fatal("accepted TV")
	}
	for _, id := range []string{"../123", "", "1?key=secret", "tmdb:1"} {
		if _, err := source.Detail(context.Background(), WatchlistMetadataKindMovie, id); err == nil {
			t.Fatal("accepted invalid ID", id)
		}
	}
}

func TestWatchlistSelectedMovieCandidateKeepsSource(t *testing.T) {
	doubanHits, tmdbHits := 0, 0
	douban := doubanStub(t, func(w http.ResponseWriter, r *http.Request) {
		doubanHits++
		t.Error("TMDB selection reached Douban")
		w.Write([]byte(doubanDetailFixture))
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tmdbHits++
		if r.URL.Path != "/movie/3001114" {
			t.Error(r.URL.Path)
		}
		w.Write([]byte(`{"id":3001114,"title":"TMDB 的另一部片"}`))
	}))
	defer server.Close()
	tmdb := NewTMDBWatchlistMetadataSource("key", server.Client())
	tmdb.baseURL = server.URL
	chain := []WatchlistMetadataSource{douban, tmdb}
	detail, err := selectedWatchlistMetadataDetail(context.Background(), chain, WatchlistMetadataKindMovie, "tmdb:3001114")
	if err != nil || detail.SourceName != "tmdb" || detail.SourceItemID != "3001114" || doubanHits != 0 || tmdbHits != 1 {
		t.Fatalf("detail=%+v err=%v hits=%d/%d", detail, err, doubanHits, tmdbHits)
	}
	for _, id := range []string{"3001114", "unknown:3001114", "douban:"} {
		if _, err := selectedWatchlistMetadataDetail(context.Background(), chain, WatchlistMetadataKindMovie, id); err == nil {
			t.Fatal("accepted ambiguous selection", id)
		}
	}
}

func TestWatchlistMetadataDoubanFallbackOnlyWhenNotFound(t *testing.T) {
	for _, tc := range []struct {
		body      string
		wantCalls int
		want      WatchlistMetadataFailure
	}{
		{"[]", 1, ""}, {"<html>需要验证</html>", 0, WatchlistMetadataFailureSourceError},
	} {
		calls := 0
		douban := doubanStub(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tc.body)) })
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Write([]byte(`{"results":[{"id":42,"title":"沙丘"}]}`))
		}))
		tmdb := NewTMDBWatchlistMetadataSource("key", server.Client())
		tmdb.baseURL = server.URL
		items, err := SearchWatchlistMetadataChain(context.Background(), []WatchlistMetadataSource{douban, tmdb}, WatchlistMetadataKindMovie, "沙丘")
		server.Close()
		if calls != tc.wantCalls || WatchlistMetadataFailureOf(err) != tc.want {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
		if err == nil && (len(items) != 1 || items[0].SourceName != "tmdb") {
			t.Fatal(items)
		}
	}
}

func TestWatchlistMovieCandidateSelectionRoundTrip(t *testing.T) {
	for _, chosen := range []string{"douban", "tmdb"} {
		t.Run(chosen, func(t *testing.T) {
			detail := WatchlistMetadataDetail{WatchlistMetadataCandidate: WatchlistMetadataCandidate{SourceItemID: "3001114", Title: "沙丘", Year: 2021, Overview: "简介"}}
			douban := staticWatchlistSource("douban", detail)
			if chosen == "tmdb" {
				douban.searchFn = func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
					return nil, newWatchlistMetadataSourceError("douban", WatchlistMetadataFailureNotFound, 200, "无结果", nil)
				}
				douban.detailFn = func(context.Context, WatchlistMetadataKind, string) (*WatchlistMetadataDetail, error) {
					t.Fatal("TMDB ID was sent to Douban")
					return nil, nil
				}
			}
			harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{WatchlistMetadataKindMovie: {douban, staticWatchlistSource("tmdb", detail)}}, nil)
			entry := mustCreateWatchlistEntry(t, "用户输入的片名", "movie")
			candidates, err := harness.service.ListCandidates(entry.ID)
			if err != nil || len(candidates) != 1 {
				t.Fatalf("candidates=%+v err=%v", candidates, err)
			}
			if candidates[0].SourceItemID != chosen+":3001114" {
				t.Fatal(candidates)
			}
			if err := harness.service.ApplyCandidate(entry.ID, candidates[0].SourceItemID); err != nil {
				t.Fatal(err)
			}
			saved := reloadWatchlistEntry(t, entry.ID)
			if saved.SourceName != chosen || saved.SourceItemID != "3001114" || saved.EnrichmentStatus != "manual" || saved.Title != "用户输入的片名" {
				t.Fatalf("saved=%+v", saved)
			}
		})
	}
}
