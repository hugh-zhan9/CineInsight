package services

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const tmdbTestAPIKey = "test-api-key-do-not-log"

// tmdbStub 是 TMDB 的桩替身。它同时**数请求**——credential_missing 这一类的定义
// 就包含「不发请求」，没有计数就证明不了。
type tmdbStub struct {
	server *httptest.Server
	hits   atomic.Int64

	mu       sync.Mutex
	requests []*url.URL
}

func newTMDBStub(t *testing.T, handler http.HandlerFunc) *tmdbStub {
	t.Helper()
	stub := &tmdbStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.hits.Add(1)
		stub.mu.Lock()
		stub.requests = append(stub.requests, r.URL)
		stub.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *tmdbStub) lastRequest(t *testing.T) *url.URL {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("桩替身没有收到任何请求")
	}
	return s.requests[len(s.requests)-1]
}

// newTMDBTestClient 走的是生产路径同一个构造器。测试也不自建 http.Client：
// 代理行为是本切片要验证的东西之一，绕过它就等于没验证。
func newTMDBTestClient(t *testing.T, config WatchlistMetadataConfig) *http.Client {
	t.Helper()
	client, err := NewWatchlistMetadataHTTPClient(config, 5*time.Second)
	if err != nil {
		t.Fatalf("构造出网客户端失败: %v", err)
	}
	return client
}

func newTMDBSourceFor(t *testing.T, apiKey string, baseURL string, config WatchlistMetadataConfig) *TMDBWatchlistMetadataSource {
	t.Helper()
	source := NewTMDBWatchlistMetadataSource(apiKey, newTMDBTestClient(t, config))
	source.baseURL = baseURL
	source.imageBaseURL = "https://image.example.test/t/p/w500"
	return source
}

func jsonHandler(t *testing.T, status int, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("桩替身写响应失败: %v", err)
		}
	}
}

// closedLoopbackAddr 占一个端口再立刻放掉，得到一个确定连不上的地址。
func closedLoopbackAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占端口失败: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("释放端口失败: %v", err)
	}
	return addr
}

func requireFailure(t *testing.T, err error, want WatchlistMetadataFailure) *WatchlistMetadataSourceError {
	t.Helper()
	if err == nil {
		t.Fatalf("期望失败分类 %q，却没有错误", want)
	}
	if got := WatchlistMetadataFailureOf(err); got != want {
		t.Fatalf("失败分类 = %q，期望 %q（错误：%v）", got, want, err)
	}
	var sourceErr *WatchlistMetadataSourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("错误不是 *WatchlistMetadataSourceError：%v", err)
	}
	if sourceErr.Source != WatchlistMetadataSourceTMDB {
		t.Errorf("Source = %q，期望 %q", sourceErr.Source, WatchlistMetadataSourceTMDB)
	}
	return sourceErr
}

const tmdbMovieSearchBody = `{
  "page": 1,
  "results": [
    {
      "id": 438631,
      "title": "沙丘",
      "original_title": "Dune",
      "overview": "保罗·厄崔迪的故事。",
      "release_date": "2021-09-15",
      "vote_average": 7.786,
      "poster_path": "/d5NXSklXo0qyIYkgV94XAgMIckC.jpg"
    },
    {
      "id": 841,
      "title": "沙丘",
      "original_title": "Dune",
      "overview": "1984 年版。",
      "release_date": "",
      "vote_average": 6.2,
      "poster_path": null
    }
  ]
}`

func TestWatchlistMetadataTMDBSearchMapsMovieCandidates(t *testing.T) {
	stub := newTMDBStub(t, jsonHandler(t, http.StatusOK, tmdbMovieSearchBody))
	source := newTMDBSourceFor(t, tmdbTestAPIKey, stub.server.URL, WatchlistMetadataConfig{})

	candidates, err := source.Search(context.Background(), WatchlistMetadataKindMovie, "沙丘")
	if err != nil {
		t.Fatalf("Search 失败: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("候选数 = %d，期望 2", len(candidates))
	}

	first := candidates[0]
	// SourceItemID 是承重字段：丢了它就无法重查详情，也无法去重。
	if first.SourceItemID != "438631" {
		t.Errorf("SourceItemID = %q，期望 %q", first.SourceItemID, "438631")
	}
	if first.SourceName != WatchlistMetadataSourceTMDB {
		t.Errorf("SourceName = %q，期望 %q", first.SourceName, WatchlistMetadataSourceTMDB)
	}
	if first.Title != "沙丘" || first.OriginalTitle != "Dune" {
		t.Errorf("标题映射错：Title=%q OriginalTitle=%q", first.Title, first.OriginalTitle)
	}
	if first.Year != 2021 {
		t.Errorf("Year = %d，期望 2021", first.Year)
	}
	if first.Rating != 7.786 {
		t.Errorf("Rating = %v，期望 7.786", first.Rating)
	}
	if want := "https://image.example.test/t/p/w500/d5NXSklXo0qyIYkgV94XAgMIckC.jpg"; first.PosterURL != want {
		t.Errorf("PosterURL = %q，期望 %q", first.PosterURL, want)
	}
	if first.Overview != "保罗·厄崔迪的故事。" {
		t.Errorf("Overview = %q", first.Overview)
	}

	// 第二条缺发行日期与海报：不猜年份、不拼出一个指向 null 的图片地址。
	if candidates[1].Year != 0 {
		t.Errorf("缺 release_date 时 Year = %d，期望 0", candidates[1].Year)
	}
	if candidates[1].PosterURL != "" {
		t.Errorf("poster_path 为 null 时 PosterURL = %q，期望空串", candidates[1].PosterURL)
	}

	request := stub.lastRequest(t)
	if request.Path != "/search/movie" {
		t.Errorf("请求路径 = %q，期望 /search/movie", request.Path)
	}
	if got := request.Query().Get("query"); got != "沙丘" {
		t.Errorf("query = %q，期望 沙丘", got)
	}
	if got := request.Query().Get("api_key"); got != tmdbTestAPIKey {
		t.Errorf("api_key 未随请求发出，got %q", got)
	}
}

func TestWatchlistMetadataTMDBSearchUsesTVEndpointForTVAndShow(t *testing.T) {
	const body = `{"results":[{"id":1399,"name":"权力的游戏","original_name":"Game of Thrones","first_air_date":"2011-04-17","vote_average":8.4,"poster_path":"/x.jpg"}]}`
	for _, kind := range []WatchlistMetadataKind{WatchlistMetadataKindTV, WatchlistMetadataKindShow} {
		t.Run(string(kind), func(t *testing.T) {
			stub := newTMDBStub(t, jsonHandler(t, http.StatusOK, body))
			source := newTMDBSourceFor(t, tmdbTestAPIKey, stub.server.URL, WatchlistMetadataConfig{})

			candidates, err := source.Search(context.Background(), kind, "权力的游戏")
			if err != nil {
				t.Fatalf("Search 失败: %v", err)
			}
			if len(candidates) != 1 {
				t.Fatalf("候选数 = %d，期望 1", len(candidates))
			}
			// 剧集形态用的是 name / first_air_date，不是 title / release_date。
			if candidates[0].Title != "权力的游戏" || candidates[0].OriginalTitle != "Game of Thrones" {
				t.Errorf("剧集标题映射错：%+v", candidates[0])
			}
			if candidates[0].Year != 2011 {
				t.Errorf("Year = %d，期望 2011", candidates[0].Year)
			}
			if candidates[0].SourceItemID != "1399" {
				t.Errorf("SourceItemID = %q，期望 1399", candidates[0].SourceItemID)
			}
			if path := stub.lastRequest(t).Path; path != "/search/tv" {
				t.Errorf("%s 的请求路径 = %q，期望 /search/tv", kind, path)
			}
		})
	}
}

func TestWatchlistMetadataTMDBDetailMapsGenresAndCredits(t *testing.T) {
	const body = `{
	  "id": 438631,
	  "title": "沙丘",
	  "original_title": "Dune",
	  "release_date": "2021-09-15",
	  "vote_average": 7.8,
	  "poster_path": "/p.jpg",
	  "genres": [{"id": 878, "name": "科幻"}, {"id": 12, "name": "冒险"}],
	  "credits": {
	    "cast": [{"name": "提莫西·查拉梅"}, {"name": "丽贝卡·弗格森"}],
	    "crew": [{"name": "丹尼斯·维伦纽瓦", "job": "Director"}, {"name": "汉斯·季默", "job": "Original Music Composer"}]
	  }
	}`
	stub := newTMDBStub(t, jsonHandler(t, http.StatusOK, body))
	source := newTMDBSourceFor(t, tmdbTestAPIKey, stub.server.URL, WatchlistMetadataConfig{})

	detail, err := source.Detail(context.Background(), WatchlistMetadataKindMovie, "438631")
	if err != nil {
		t.Fatalf("Detail 失败: %v", err)
	}
	// 详情里同样必须带着源条目 ID。
	if detail.SourceItemID != "438631" {
		t.Errorf("SourceItemID = %q，期望 438631", detail.SourceItemID)
	}
	if strings.Join(detail.Genres, ",") != "科幻,冒险" {
		t.Errorf("Genres = %v", detail.Genres)
	}
	if strings.Join(detail.Directors, ",") != "丹尼斯·维伦纽瓦" {
		t.Errorf("Directors = %v，只应取 job=Director", detail.Directors)
	}
	if strings.Join(detail.Cast, ",") != "提莫西·查拉梅,丽贝卡·弗格森" {
		t.Errorf("Cast = %v", detail.Cast)
	}

	request := stub.lastRequest(t)
	if request.Path != "/movie/438631" {
		t.Errorf("请求路径 = %q，期望 /movie/438631", request.Path)
	}
	if got := request.Query().Get("append_to_response"); got != "credits" {
		t.Errorf("append_to_response = %q，期望 credits", got)
	}
}

func TestWatchlistMetadataTMDBDetailTakesTVCreatorsAsDirectors(t *testing.T) {
	const body = `{"id":1399,"name":"权力的游戏","created_by":[{"name":"大卫·贝尼奥夫"},{"name":"D.B.威斯"}],"credits":{"cast":[{"name":"艾米莉亚·克拉克"}]}}`
	stub := newTMDBStub(t, jsonHandler(t, http.StatusOK, body))
	source := newTMDBSourceFor(t, tmdbTestAPIKey, stub.server.URL, WatchlistMetadataConfig{})

	detail, err := source.Detail(context.Background(), WatchlistMetadataKindTV, "1399")
	if err != nil {
		t.Fatalf("Detail 失败: %v", err)
	}
	if strings.Join(detail.Directors, ",") != "大卫·贝尼奥夫,D.B.威斯" {
		t.Errorf("剧集主创 = %v，期望取 created_by", detail.Directors)
	}
	if path := stub.lastRequest(t).Path; path != "/tv/1399" {
		t.Errorf("请求路径 = %q，期望 /tv/1399", path)
	}
}

// TMDB 不覆盖 anime / av，适配器自己也要拦住，并且一个请求都不发。
func TestWatchlistMetadataTMDBRejectsKindsItDoesNotCover(t *testing.T) {
	for _, kind := range []WatchlistMetadataKind{WatchlistMetadataKindAnime, WatchlistMetadataKindAV} {
		t.Run(string(kind), func(t *testing.T) {
			stub := newTMDBStub(t, jsonHandler(t, http.StatusOK, tmdbMovieSearchBody))
			source := newTMDBSourceFor(t, tmdbTestAPIKey, stub.server.URL, WatchlistMetadataConfig{})

			if _, err := source.Search(context.Background(), kind, "随便"); !errors.Is(err, ErrWatchlistMetadataKindUnsupported) {
				t.Fatalf("Search(%s) 错误 = %v，期望 ErrWatchlistMetadataKindUnsupported", kind, err)
			}
			if _, err := source.Detail(context.Background(), kind, "1"); !errors.Is(err, ErrWatchlistMetadataKindUnsupported) {
				t.Fatalf("Detail(%s) 错误 = %v，期望 ErrWatchlistMetadataKindUnsupported", kind, err)
			}
			if hits := stub.hits.Load(); hits != 0 {
				t.Fatalf("不覆盖的类型发出了 %d 次请求，期望 0", hits)
			}
		})
	}
}

// credential_missing：凭证为空时**一个请求都不能发**。桩替身的计数器是证据。
func TestWatchlistMetadataTMDBCredentialMissingSendsNoRequest(t *testing.T) {
	stub := newTMDBStub(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("凭证为空时不应该发出任何请求")
		w.WriteHeader(http.StatusOK)
	})
	source := newTMDBSourceFor(t, "   ", stub.server.URL, WatchlistMetadataConfig{})

	_, searchErr := source.Search(context.Background(), WatchlistMetadataKindMovie, "沙丘")
	requireFailure(t, searchErr, WatchlistMetadataFailureCredentialMissing)
	_, detailErr := source.Detail(context.Background(), WatchlistMetadataKindMovie, "438631")
	requireFailure(t, detailErr, WatchlistMetadataFailureCredentialMissing)

	if hits := stub.hits.Load(); hits != 0 {
		t.Fatalf("credential_missing 路径发出了 %d 次请求，期望 0", hits)
	}
}

// credential_invalid：401 与 403。
func TestWatchlistMetadataTMDBCredentialInvalidOnUnauthorized(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			stub := newTMDBStub(t, jsonHandler(t, status, `{"status_code":7,"status_message":"Invalid API key: You must be granted a valid key."}`))
			source := newTMDBSourceFor(t, tmdbTestAPIKey, stub.server.URL, WatchlistMetadataConfig{})

			_, err := source.Search(context.Background(), WatchlistMetadataKindMovie, "沙丘")
			sourceErr := requireFailure(t, err, WatchlistMetadataFailureCredentialInvalid)
			if sourceErr.HTTPStatus != status {
				t.Errorf("HTTPStatus = %d，期望 %d", sourceErr.HTTPStatus, status)
			}
			if stub.hits.Load() != 1 {
				t.Errorf("请求次数 = %d，期望 1", stub.hits.Load())
			}
		})
	}
}

// proxy_unreachable：代理连不上，以及代理地址填错。两者都不能被误判成网络问题。
func TestWatchlistMetadataTMDBProxyUnreachable(t *testing.T) {
	t.Run("代理连不上", func(t *testing.T) {
		stub := newTMDBStub(t, jsonHandler(t, http.StatusOK, tmdbMovieSearchBody))
		// 代理指向一个确定没人监听的端口：请求连桩替身都到不了。
		config := WatchlistMetadataConfig{ProxyURL: "http://" + closedLoopbackAddr(t)}
		source := newTMDBSourceFor(t, tmdbTestAPIKey, stub.server.URL, config)

		_, err := source.Search(context.Background(), WatchlistMetadataKindMovie, "沙丘")
		requireFailure(t, err, WatchlistMetadataFailureProxyUnreachable)
		if hits := stub.hits.Load(); hits != 0 {
			t.Errorf("走坏代理时桩替身收到 %d 次请求，期望 0", hits)
		}
	})

	t.Run("代理地址填错", func(t *testing.T) {
		// 这一条在 NewWatchlistMetadataHTTPClient 就被拦下，连适配器都构造不出来。
		_, err := NewWatchlistMetadataHTTPClient(WatchlistMetadataConfig{ProxyURL: "127.0.0.1:1080"}, time.Second)
		if !errors.Is(err, ErrWatchlistMetadataProxyInvalid) {
			t.Fatalf("错误 = %v，期望 ErrWatchlistMetadataProxyInvalid", err)
		}
		if got := classifyWatchlistMetadataTransportError(err); got != WatchlistMetadataFailureProxyUnreachable {
			t.Fatalf("分类 = %q，期望 proxy_unreachable", got)
		}
	})
}

// network_unreachable：连接失败与超时。都不得被当成「源没有收录」。
func TestWatchlistMetadataTMDBNetworkUnreachable(t *testing.T) {
	t.Run("连接被拒", func(t *testing.T) {
		source := newTMDBSourceFor(t, tmdbTestAPIKey, "http://"+closedLoopbackAddr(t), WatchlistMetadataConfig{})
		_, err := source.Search(context.Background(), WatchlistMetadataKindMovie, "沙丘")
		requireFailure(t, err, WatchlistMetadataFailureNetworkUnreachable)
	})

	t.Run("超时", func(t *testing.T) {
		release := make(chan struct{})
		stub := newTMDBStub(t, func(w http.ResponseWriter, _ *http.Request) {
			<-release
			w.WriteHeader(http.StatusOK)
		})
		// 先放行再让 httptest 关服务器，否则 Close 会等在这个 handler 上。
		t.Cleanup(func() { close(release) })
		source := newTMDBSourceFor(t, tmdbTestAPIKey, stub.server.URL, WatchlistMetadataConfig{})

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := source.Search(ctx, WatchlistMetadataKindMovie, "沙丘")
		requireFailure(t, err, WatchlistMetadataFailureNetworkUnreachable)
	})
}

// not_found：搜索 200 加空结果，以及详情 404。它是唯一会触发 AV 兜底的分类，
// 必须和其余五类分得干干净净。
func TestWatchlistMetadataTMDBNotFound(t *testing.T) {
	t.Run("搜索无结果", func(t *testing.T) {
		stub := newTMDBStub(t, jsonHandler(t, http.StatusOK, `{"page":1,"results":[],"total_results":0}`))
		source := newTMDBSourceFor(t, tmdbTestAPIKey, stub.server.URL, WatchlistMetadataConfig{})

		candidates, err := source.Search(context.Background(), WatchlistMetadataKindMovie, "不存在的片")
		if candidates != nil {
			t.Errorf("无结果时不应返回候选，got %v", candidates)
		}
		requireFailure(t, err, WatchlistMetadataFailureNotFound)
	})

	t.Run("详情 404", func(t *testing.T) {
		stub := newTMDBStub(t, jsonHandler(t, http.StatusNotFound, `{"status_code":34,"status_message":"The resource you requested could not be found."}`))
		source := newTMDBSourceFor(t, tmdbTestAPIKey, stub.server.URL, WatchlistMetadataConfig{})

		_, err := source.Detail(context.Background(), WatchlistMetadataKindMovie, "999999999")
		sourceErr := requireFailure(t, err, WatchlistMetadataFailureNotFound)
		if sourceErr.Detail != "The resource you requested could not be found." {
			t.Errorf("Detail = %q，期望带上 TMDB 的 status_message", sourceErr.Detail)
		}
	})
}

// source_error：其他非 2xx，以及解析不了的响应。
func TestWatchlistMetadataTMDBSourceError(t *testing.T) {
	t.Run("500", func(t *testing.T) {
		stub := newTMDBStub(t, jsonHandler(t, http.StatusInternalServerError, `{"status_code":11,"status_message":"Internal error."}`))
		source := newTMDBSourceFor(t, tmdbTestAPIKey, stub.server.URL, WatchlistMetadataConfig{})

		_, err := source.Search(context.Background(), WatchlistMetadataKindMovie, "沙丘")
		sourceErr := requireFailure(t, err, WatchlistMetadataFailureSourceError)
		if sourceErr.HTTPStatus != http.StatusInternalServerError {
			t.Errorf("HTTPStatus = %d，期望 500", sourceErr.HTTPStatus)
		}
	})

	t.Run("429 也不是凭证问题", func(t *testing.T) {
		stub := newTMDBStub(t, jsonHandler(t, http.StatusTooManyRequests, `{"status_code":25,"status_message":"Your request count is over the allowed limit."}`))
		source := newTMDBSourceFor(t, tmdbTestAPIKey, stub.server.URL, WatchlistMetadataConfig{})

		_, err := source.Search(context.Background(), WatchlistMetadataKindMovie, "沙丘")
		requireFailure(t, err, WatchlistMetadataFailureSourceError)
	})

	t.Run("响应不是 JSON", func(t *testing.T) {
		stub := newTMDBStub(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte("<html>not json</html>")); err != nil {
				t.Errorf("写响应失败: %v", err)
			}
		})
		source := newTMDBSourceFor(t, tmdbTestAPIKey, stub.server.URL, WatchlistMetadataConfig{})

		_, err := source.Search(context.Background(), WatchlistMetadataKindMovie, "沙丘")
		requireFailure(t, err, WatchlistMetadataFailureSourceError)
	})
}

// 六类必须互不相同——合并任意两类都会让用户排查错方向（D-WM14）。
func TestWatchlistMetadataFailureCodesAreDistinct(t *testing.T) {
	codes := []WatchlistMetadataFailure{
		WatchlistMetadataFailureCredentialMissing,
		WatchlistMetadataFailureCredentialInvalid,
		WatchlistMetadataFailureProxyUnreachable,
		WatchlistMetadataFailureNetworkUnreachable,
		WatchlistMetadataFailureNotFound,
		WatchlistMetadataFailureSourceError,
	}
	seen := make(map[WatchlistMetadataFailure]bool, len(codes))
	for _, code := range codes {
		if code == "" {
			t.Fatal("分类码不能是空串——空串的语义是「没有分类」")
		}
		if seen[code] {
			t.Fatalf("分类码 %q 重复", code)
		}
		seen[code] = true
		// 分类码要直接写进 EnrichmentError（size:32）。
		if len(code) > 32 {
			t.Errorf("分类码 %q 超过 EnrichmentError 的 32 字节上限", code)
		}
	}
	// 没有分类的错误返回空串，不擅自归成 source_error。
	if got := WatchlistMetadataFailureOf(errors.New("普通错误")); got != "" {
		t.Errorf("非适配器错误的分类 = %q，期望空串", got)
	}
	if got := WatchlistMetadataFailureOf(nil); got != "" {
		t.Errorf("nil 的分类 = %q，期望空串", got)
	}
}

// 凭证在 query 里，*url.Error 会把整个 URL 带进 Error()。错误冒泡时不能带出 key。
func TestWatchlistMetadataTMDBErrorDoesNotLeakAPIKey(t *testing.T) {
	source := newTMDBSourceFor(t, tmdbTestAPIKey, "http://"+closedLoopbackAddr(t), WatchlistMetadataConfig{})

	_, err := source.Search(context.Background(), WatchlistMetadataKindMovie, "沙丘")
	if err == nil {
		t.Fatal("期望请求失败")
	}
	if strings.Contains(err.Error(), tmdbTestAPIKey) {
		t.Errorf("错误文案泄漏了 api_key：%v", err)
	}
	// 内层原因也要抹干净——有人打印 errors.Unwrap 的结果是完全可能的。
	for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
		if strings.Contains(cause.Error(), tmdbTestAPIKey) {
			t.Fatalf("错误链里泄漏了 api_key：%v", cause)
		}
	}
	// 抹掉文案不能把链子也抹掉。
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Errorf("错误链丢了 *url.Error，errors.Is / As 判定会跟着失效：%v", err)
	}
}
