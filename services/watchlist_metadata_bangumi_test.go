package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const bangumiTestAccessToken = "test-bangumi-token-do-not-log"

// bangumiRecordedRequest 留下一次请求的可断言痕迹。
//
// 记头和体而不只记 URL：Bangumi 的凭证走 Authorization 头、搜索条件走 JSON 体、
// 而 User-Agent 是源站明确要求的——这三样都不在 URL 里，只看 URL 等于没验证。
type bangumiRecordedRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

// bangumiStub 是 Bangumi 的桩替身。测试一律打到它，不碰 api.bgm.tv。
type bangumiStub struct {
	server *httptest.Server
	hits   atomic.Int64

	mu       sync.Mutex
	requests []bangumiRecordedRequest
}

// newBangumiStub 按「路径 → 处理函数」建桩。详情要发三次请求（条目、人物、角色），
// 用一张表比一个大 switch 更容易看出哪条路径没被覆盖。
func newBangumiStub(t *testing.T, routes map[string]http.HandlerFunc) *bangumiStub {
	t.Helper()
	stub := &bangumiStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.hits.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("桩替身读请求体失败: %v", err)
		}
		stub.mu.Lock()
		stub.requests = append(stub.requests, bangumiRecordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Header: r.Header.Clone(),
			Body:   body,
		})
		stub.mu.Unlock()

		handler, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("桩替身收到未登记的路径 %q", r.URL.Path)
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *bangumiStub) lastRequest(t *testing.T) bangumiRecordedRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("桩替身没有收到任何请求")
	}
	return s.requests[len(s.requests)-1]
}

func (s *bangumiStub) allRequests() []bangumiRecordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bangumiRecordedRequest(nil), s.requests...)
}

// newBangumiSourceFor 走生产路径同一个客户端构造器。测试也不自建 http.Client：
// 代理行为是要验证的东西之一，绕过它就等于没验证。
func newBangumiSourceFor(t *testing.T, token string, baseURL string, config WatchlistMetadataConfig) *BangumiWatchlistMetadataSource {
	t.Helper()
	client, err := NewWatchlistMetadataHTTPClient(config, 5*time.Second)
	if err != nil {
		t.Fatalf("构造出网客户端失败: %v", err)
	}
	source := NewBangumiWatchlistMetadataSource(token, client)
	source.baseURL = baseURL
	return source
}

func requireBangumiFailure(t *testing.T, err error, want WatchlistMetadataFailure) *WatchlistMetadataSourceError {
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
	if sourceErr.Source != WatchlistMetadataSourceBangumi {
		t.Errorf("Source = %q，期望 %q", sourceErr.Source, WatchlistMetadataSourceBangumi)
	}
	return sourceErr
}

const bangumiSearchBody = `{
  "total": 2,
  "limit": 25,
  "offset": 0,
  "data": [
    {
      "id": 253,
      "type": 2,
      "name": "カウボーイビバップ",
      "name_cn": "星际牛仔",
      "summary": "2071 年，人类移居各个行星。",
      "date": "1998-04-03",
      "images": {
        "large": "https://lain.bgm.tv/pic/cover/l/253.jpg",
        "common": "https://lain.bgm.tv/pic/cover/c/253.jpg",
        "medium": "https://lain.bgm.tv/pic/cover/m/253.jpg"
      },
      "rating": { "score": 8.9 }
    },
    {
      "id": 265,
      "type": 2,
      "name": "Cowboy Bebop: Tengoku no Tobira",
      "name_cn": "",
      "summary": "剧场版。",
      "date": "",
      "images": { "large": "", "common": "", "medium": "" },
      "rating": { "score": 8.1 }
    }
  ]
}`

// 候选映射：源条目 ID 必须保留（没有它就回不到源上），中文名优先，
// 缺名/缺图/缺日期的条目不被猜测填充。
func TestWatchlistMetadataBangumiSearchMapsAnimeCandidates(t *testing.T) {
	stub := newBangumiStub(t, map[string]http.HandlerFunc{
		"/v0/search/subjects": jsonHandler(t, http.StatusOK, bangumiSearchBody),
	})
	source := newBangumiSourceFor(t, bangumiTestAccessToken, stub.server.URL, WatchlistMetadataConfig{})

	candidates, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "星际牛仔")
	if err != nil {
		t.Fatalf("Search 失败: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("候选数 = %d，期望 2", len(candidates))
	}

	first := candidates[0]
	if first.SourceItemID != "253" {
		t.Errorf("SourceItemID = %q，期望 %q——丢了它这条候选就回不到源上", first.SourceItemID, "253")
	}
	if first.SourceName != WatchlistMetadataSourceBangumi {
		t.Errorf("SourceName = %q，期望 %q", first.SourceName, WatchlistMetadataSourceBangumi)
	}
	if first.Title != "星际牛仔" {
		t.Errorf("Title = %q，期望中文译名优先", first.Title)
	}
	if first.OriginalTitle != "カウボーイビバップ" {
		t.Errorf("OriginalTitle = %q，期望原名", first.OriginalTitle)
	}
	if first.Year != 1998 {
		t.Errorf("Year = %d，期望 1998", first.Year)
	}
	if first.Rating != 8.9 {
		t.Errorf("Rating = %v，期望 8.9", first.Rating)
	}
	if first.PosterURL != "https://lain.bgm.tv/pic/cover/l/253.jpg" {
		t.Errorf("PosterURL = %q，期望取 images.large", first.PosterURL)
	}
	if first.Overview != "2071 年，人类移居各个行星。" {
		t.Errorf("Overview = %q，期望原文不截断", first.Overview)
	}

	// 第二条缺中文名、缺图、缺日期：回退到原名，另两项留空/留零，不猜。
	second := candidates[1]
	if second.SourceItemID != "265" {
		t.Errorf("第二条 SourceItemID = %q，期望 %q", second.SourceItemID, "265")
	}
	if second.Title != "Cowboy Bebop: Tengoku no Tobira" {
		t.Errorf("缺中文名时 Title = %q，期望回退到原名", second.Title)
	}
	if second.Year != 0 {
		t.Errorf("日期为空时 Year = %d，期望 0（不猜年份）", second.Year)
	}
	if second.PosterURL != "" {
		t.Errorf("无图时 PosterURL = %q，期望空串", second.PosterURL)
	}
}

// Bangumi 规范要求带上标识应用的 User-Agent，Go 默认的 "Go-http-client/1.1"
// 正是它点名可能封禁的形态。这条断言钉住「UA 确实发出去了」。
func TestWatchlistMetadataBangumiSendsRequiredUserAgent(t *testing.T) {
	stub := newBangumiStub(t, map[string]http.HandlerFunc{
		"/v0/search/subjects": jsonHandler(t, http.StatusOK, bangumiSearchBody),
	})
	source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

	if _, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "星际牛仔"); err != nil {
		t.Fatalf("Search 失败: %v", err)
	}

	got := stub.lastRequest(t).Header.Get("User-Agent")
	if got != bangumiUserAgent {
		t.Fatalf("User-Agent = %q，期望 %q", got, bangumiUserAgent)
	}
	if got == "" || strings.HasPrefix(got, "Go-http-client") {
		t.Fatalf("User-Agent = %q，正是 Bangumi 点名可能封禁的默认 UA", got)
	}
	// 规范要求 UA 里带开发者标识与应用名，形如 <id>/<app>。
	if !strings.Contains(got, "/") {
		t.Errorf("User-Agent = %q，缺少 <开发者标识>/<应用名> 形态", got)
	}
}

// 搜索按 Bangumi 的 v0 规格发 POST，条件在 JSON 体里：关键词、匹配度排序、
// 类型收窄到动画（枚举值 2）。类型过滤写错会把漫画和游戏也搜进来。
func TestWatchlistMetadataBangumiSearchSendsTypedPostBody(t *testing.T) {
	stub := newBangumiStub(t, map[string]http.HandlerFunc{
		"/v0/search/subjects": jsonHandler(t, http.StatusOK, bangumiSearchBody),
	})
	source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

	if _, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "  星际牛仔  "); err != nil {
		t.Fatalf("Search 失败: %v", err)
	}

	request := stub.lastRequest(t)
	if request.Method != http.MethodPost {
		t.Errorf("方法 = %q，期望 POST", request.Method)
	}
	if request.Path != "/v0/search/subjects" {
		t.Errorf("路径 = %q，期望 /v0/search/subjects", request.Path)
	}
	if ct := request.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q，期望 application/json", ct)
	}

	var sent bangumiSearchRequest
	if err := json.Unmarshal(request.Body, &sent); err != nil {
		t.Fatalf("请求体不是预期的 JSON: %v（原文 %s）", err, request.Body)
	}
	if sent.Keyword != "星际牛仔" {
		t.Errorf("keyword = %q，期望去掉首尾空白后的片名", sent.Keyword)
	}
	if sent.Sort != bangumiSearchSortByMatch {
		t.Errorf("sort = %q，期望 %q（保持源侧匹配度顺序）", sent.Sort, bangumiSearchSortByMatch)
	}
	if len(sent.Filter.Type) != 1 || sent.Filter.Type[0] != bangumiSubjectTypeAnime {
		t.Errorf("filter.type = %v，期望 [%d]（只搜动画）", sent.Filter.Type, bangumiSubjectTypeAnime)
	}
	if !strings.Contains(request.Query, "limit=") {
		t.Errorf("查询串 %q 里没有 limit", request.Query)
	}
}

const bangumiSubjectBody = `{
  "id": 253,
  "type": 2,
  "name": "カウボーイビバップ",
  "name_cn": "星际牛仔",
  "summary": "2071 年，人类移居各个行星。",
  "date": "1998-04-03",
  "images": { "large": "https://lain.bgm.tv/pic/cover/l/253.jpg" },
  "rating": { "score": 8.9 },
  "meta_tags": ["科幻", "TV", "日本"],
  "tags": [{ "name": "神作" }, { "name": "1998" }]
}`

const bangumiPersonsBody = `[
  { "id": 1, "name": "渡辺信一郎", "relation": "导演" },
  { "id": 2, "name": "信本敬子", "relation": "脚本" },
  { "id": 3, "name": "菅野よう子", "relation": "音乐" },
  { "id": 4, "name": "川元利浩", "relation": "監督" },
  { "id": 5, "name": "", "relation": "导演" }
]`

const bangumiCharactersBody = `[
  { "id": 10, "name": "スパイク", "actors": [{ "id": 100, "name": "山寺宏一" }] },
  { "id": 11, "name": "フェイ", "actors": [{ "id": 101, "name": "林原めぐみ" }] },
  { "id": 12, "name": "その他", "actors": [{ "id": 100, "name": "山寺宏一" }, { "id": 102, "name": "  " }] }
]`

// 详情把三次请求的结果合成一条：条目给基本信息与类型标签，/persons 给导演，
// /characters 的 actors 给声优。
func TestWatchlistMetadataBangumiDetailMergesSubjectPersonsAndCharacters(t *testing.T) {
	stub := newBangumiStub(t, map[string]http.HandlerFunc{
		"/v0/subjects/253":            jsonHandler(t, http.StatusOK, bangumiSubjectBody),
		"/v0/subjects/253/persons":    jsonHandler(t, http.StatusOK, bangumiPersonsBody),
		"/v0/subjects/253/characters": jsonHandler(t, http.StatusOK, bangumiCharactersBody),
	})
	source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

	detail, err := source.Detail(context.Background(), WatchlistMetadataKindAnime, "253")
	if err != nil {
		t.Fatalf("Detail 失败: %v", err)
	}
	if detail.SourceItemID != "253" {
		t.Errorf("SourceItemID = %q，期望详情里也保住源条目 ID", detail.SourceItemID)
	}
	if detail.Title != "星际牛仔" {
		t.Errorf("Title = %q", detail.Title)
	}

	// meta_tags 是官方整理的公共标签，优先于带噪声的用户标签。
	wantGenres := []string{"科幻", "TV", "日本"}
	if strings.Join(detail.Genres, ",") != strings.Join(wantGenres, ",") {
		t.Errorf("Genres = %v，期望取 meta_tags %v", detail.Genres, wantGenres)
	}

	// 「导演」与「監督」都算导演；脚本、音乐不算；空名字丢掉。
	wantDirectors := []string{"渡辺信一郎", "川元利浩"}
	if strings.Join(detail.Directors, ",") != strings.Join(wantDirectors, ",") {
		t.Errorf("Directors = %v，期望 %v", detail.Directors, wantDirectors)
	}

	// 同一个声优配多个角色只出现一次，且保持首次出现的顺序；空名字丢掉。
	wantCast := []string{"山寺宏一", "林原めぐみ"}
	if strings.Join(detail.Cast, ",") != strings.Join(wantCast, ",") {
		t.Errorf("Cast = %v，期望去重后的 %v", detail.Cast, wantCast)
	}

	if got := len(stub.allRequests()); got != 3 {
		t.Errorf("请求数 = %d，期望 3（条目 + 人物 + 角色）", got)
	}
}

// 冷门条目没有 meta_tags 时退到用户 tags——同一份数据的两种精度。
func TestWatchlistMetadataBangumiDetailFallsBackToUserTagsForGenres(t *testing.T) {
	stub := newBangumiStub(t, map[string]http.HandlerFunc{
		"/v0/subjects/999": jsonHandler(t, http.StatusOK,
			`{"id":999,"name":"某冷门作","meta_tags":[],"tags":[{"name":"原创"},{"name":"  "}]}`),
		"/v0/subjects/999/persons":    jsonHandler(t, http.StatusOK, `[]`),
		"/v0/subjects/999/characters": jsonHandler(t, http.StatusOK, `[]`),
	})
	source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

	detail, err := source.Detail(context.Background(), WatchlistMetadataKindAnime, "999")
	if err != nil {
		t.Fatalf("Detail 失败: %v", err)
	}
	if strings.Join(detail.Genres, ",") != "原创" {
		t.Errorf("Genres = %v，期望回退到 tags 的 [原创]", detail.Genres)
	}
	if len(detail.Directors) != 0 || len(detail.Cast) != 0 {
		t.Errorf("空人物/角色列表不该造出主创：Directors=%v Cast=%v", detail.Directors, detail.Cast)
	}
}

// **本适配器与 TMDB 的关键差异**：Bangumi 的读接口匿名可读，token 为空时照发请求，
// 不得判 credential_missing。判错会把「本可以查到」变成一条假的配置错误。
func TestWatchlistMetadataBangumiWithoutTokenStillSendsRequest(t *testing.T) {
	stub := newBangumiStub(t, map[string]http.HandlerFunc{
		"/v0/search/subjects": jsonHandler(t, http.StatusOK, bangumiSearchBody),
	})
	source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

	candidates, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "星际牛仔")
	if err != nil {
		t.Fatalf("无 token 时 Search 失败: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("候选数 = %d，期望 2", len(candidates))
	}
	if hits := stub.hits.Load(); hits != 1 {
		t.Fatalf("桩替身收到 %d 次请求，期望 1——无 token 不得阻止发请求", hits)
	}
	if auth := stub.lastRequest(t).Header.Get("Authorization"); auth != "" {
		t.Errorf("无 token 时 Authorization = %q，期望不发这个头", auth)
	}
}

// 配了 token 就按 Bearer 发出去。
func TestWatchlistMetadataBangumiSendsBearerTokenWhenConfigured(t *testing.T) {
	stub := newBangumiStub(t, map[string]http.HandlerFunc{
		"/v0/search/subjects": jsonHandler(t, http.StatusOK, bangumiSearchBody),
	})
	source := newBangumiSourceFor(t, bangumiTestAccessToken, stub.server.URL, WatchlistMetadataConfig{})

	if _, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "星际牛仔"); err != nil {
		t.Fatalf("Search 失败: %v", err)
	}
	if got, want := stub.lastRequest(t).Header.Get("Authorization"), "Bearer "+bangumiTestAccessToken; got != want {
		t.Errorf("Authorization = %q，期望 %q", got, want)
	}
}

// 本适配器只接 anime。传错类型要立刻说清楚，而不是拿动画接口去查别的东西。
func TestWatchlistMetadataBangumiRejectsKindsItDoesNotCover(t *testing.T) {
	stub := newBangumiStub(t, map[string]http.HandlerFunc{})
	source := newBangumiSourceFor(t, bangumiTestAccessToken, stub.server.URL, WatchlistMetadataConfig{})

	for _, kind := range []WatchlistMetadataKind{
		WatchlistMetadataKindMovie,
		WatchlistMetadataKindTV,
		WatchlistMetadataKindShow,
		WatchlistMetadataKindAV,
	} {
		t.Run(string(kind), func(t *testing.T) {
			if _, err := source.Search(context.Background(), kind, "沙丘"); !errors.Is(err, ErrWatchlistMetadataKindUnsupported) {
				t.Errorf("Search 错误 = %v，期望 ErrWatchlistMetadataKindUnsupported", err)
			}
			if _, err := source.Detail(context.Background(), kind, "1"); !errors.Is(err, ErrWatchlistMetadataKindUnsupported) {
				t.Errorf("Detail 错误 = %v，期望 ErrWatchlistMetadataKindUnsupported", err)
			}
		})
	}
	if hits := stub.hits.Load(); hits != 0 {
		t.Errorf("不覆盖的类型发出了 %d 次请求，期望 0", hits)
	}
}

// not_found：搜索 200 加空 data，以及详情 404。它是唯一会触发 AV 兜底的分类
// （D-WM07），必须和其余五类分得干干净净。
func TestWatchlistMetadataBangumiNotFound(t *testing.T) {
	t.Run("搜索无结果", func(t *testing.T) {
		stub := newBangumiStub(t, map[string]http.HandlerFunc{
			"/v0/search/subjects": jsonHandler(t, http.StatusOK, `{"total":0,"limit":25,"offset":0,"data":[]}`),
		})
		source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

		candidates, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "不存在的片")
		requireBangumiFailure(t, err, WatchlistMetadataFailureNotFound)
		if candidates != nil {
			t.Errorf("报 not_found 时不应返回候选，got %v", candidates)
		}
	})

	t.Run("data 为 null", func(t *testing.T) {
		stub := newBangumiStub(t, map[string]http.HandlerFunc{
			"/v0/search/subjects": jsonHandler(t, http.StatusOK, `{"total":0,"data":null}`),
		})
		source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

		_, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "不存在的片")
		requireBangumiFailure(t, err, WatchlistMetadataFailureNotFound)
	})

	t.Run("详情 404", func(t *testing.T) {
		stub := newBangumiStub(t, map[string]http.HandlerFunc{
			"/v0/subjects/404404": jsonHandler(t, http.StatusNotFound,
				`{"title":"Not Found","description":"subject not found","details":{"path":"/v0/subjects/404404"}}`),
		})
		source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

		_, err := source.Detail(context.Background(), WatchlistMetadataKindAnime, "404404")
		sourceErr := requireBangumiFailure(t, err, WatchlistMetadataFailureNotFound)
		if sourceErr.HTTPStatus != http.StatusNotFound {
			t.Errorf("HTTPStatus = %d，期望 404", sourceErr.HTTPStatus)
		}
		// 取源站自己的文案当原因，details 里可能回显请求内容，不取。
		if !strings.Contains(sourceErr.Error(), "subject not found") {
			t.Errorf("错误文案 %q 里没有源站给的原因", sourceErr.Error())
		}
	})
}

// credential_invalid：401 / 403。凭证问题不得被当成「源没有收录」，
// 否则 AV 兜底会被一个配置错误触发，用户也会去排查错方向。
func TestWatchlistMetadataBangumiCredentialInvalidOnUnauthorized(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			stub := newBangumiStub(t, map[string]http.HandlerFunc{
				"/v0/search/subjects": jsonHandler(t, status, `{"title":"Unauthorized","description":"invalid token"}`),
			})
			source := newBangumiSourceFor(t, bangumiTestAccessToken, stub.server.URL, WatchlistMetadataConfig{})

			_, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "星际牛仔")
			sourceErr := requireBangumiFailure(t, err, WatchlistMetadataFailureCredentialInvalid)
			if sourceErr.HTTPStatus != status {
				t.Errorf("HTTPStatus = %d，期望 %d", sourceErr.HTTPStatus, status)
			}
		})
	}
}

// 源明确因缺凭证而拒绝（匿名请求吃 401）时，归 credential_invalid。
// 本适配器永远不产出 credential_missing——那个分类的定义是「不发请求」，
// 而 Bangumi 无 token 也该发。
func TestWatchlistMetadataBangumiNeverReportsCredentialMissing(t *testing.T) {
	stub := newBangumiStub(t, map[string]http.HandlerFunc{
		"/v0/search/subjects": jsonHandler(t, http.StatusUnauthorized, `{"title":"Unauthorized"}`),
	})
	source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

	_, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "星际牛仔")
	if got := WatchlistMetadataFailureOf(err); got == WatchlistMetadataFailureCredentialMissing {
		t.Fatalf("无 token 时归了 credential_missing，但 Bangumi 匿名可读，应当发请求后按源的答复归类")
	}
	requireBangumiFailure(t, err, WatchlistMetadataFailureCredentialInvalid)
	if hits := stub.hits.Load(); hits != 1 {
		t.Errorf("桩替身收到 %d 次请求，期望 1", hits)
	}
}

// source_error：其他非 2xx，以及响应解析不了。
func TestWatchlistMetadataBangumiSourceError(t *testing.T) {
	t.Run("500", func(t *testing.T) {
		stub := newBangumiStub(t, map[string]http.HandlerFunc{
			"/v0/search/subjects": jsonHandler(t, http.StatusInternalServerError, `{"title":"Internal Server Error"}`),
		})
		source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

		_, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "星际牛仔")
		requireBangumiFailure(t, err, WatchlistMetadataFailureSourceError)
	})

	t.Run("429 限流也归 source_error", func(t *testing.T) {
		stub := newBangumiStub(t, map[string]http.HandlerFunc{
			"/v0/search/subjects": jsonHandler(t, http.StatusTooManyRequests, `{"title":"Too Many Requests"}`),
		})
		source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

		_, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "星际牛仔")
		requireBangumiFailure(t, err, WatchlistMetadataFailureSourceError)
	})

	t.Run("响应不是 JSON", func(t *testing.T) {
		stub := newBangumiStub(t, map[string]http.HandlerFunc{
			"/v0/search/subjects": jsonHandler(t, http.StatusOK, `<html>Cloudflare</html>`),
		})
		source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

		_, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "星际牛仔")
		requireBangumiFailure(t, err, WatchlistMetadataFailureSourceError)
	})
}

// proxy_unreachable：代理这一段不通，与「目标站不通」分开。
func TestWatchlistMetadataBangumiProxyUnreachable(t *testing.T) {
	stub := newBangumiStub(t, map[string]http.HandlerFunc{
		"/v0/search/subjects": jsonHandler(t, http.StatusOK, bangumiSearchBody),
	})
	// 代理指向一个确定没人监听的端口：请求连桩替身都到不了。
	config := WatchlistMetadataConfig{ProxyURL: "http://" + closedLoopbackAddr(t)}
	source := newBangumiSourceFor(t, bangumiTestAccessToken, stub.server.URL, config)

	_, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "星际牛仔")
	requireBangumiFailure(t, err, WatchlistMetadataFailureProxyUnreachable)
	if hits := stub.hits.Load(); hits != 0 {
		t.Errorf("走坏代理时桩替身收到 %d 次请求，期望 0", hits)
	}
}

// network_unreachable：连接失败与超时。都不得被当成「源没有收录」。
func TestWatchlistMetadataBangumiNetworkUnreachable(t *testing.T) {
	t.Run("连接被拒", func(t *testing.T) {
		source := newBangumiSourceFor(t, "", "http://"+closedLoopbackAddr(t), WatchlistMetadataConfig{})
		_, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "星际牛仔")
		requireBangumiFailure(t, err, WatchlistMetadataFailureNetworkUnreachable)
	})

	t.Run("超时", func(t *testing.T) {
		release := make(chan struct{})
		stub := newBangumiStub(t, map[string]http.HandlerFunc{
			"/v0/search/subjects": func(w http.ResponseWriter, _ *http.Request) {
				<-release
				w.WriteHeader(http.StatusOK)
			},
		})
		// 先放行再让 httptest 关服务器，否则 Close 会等在这个 handler 上。
		t.Cleanup(func() { close(release) })
		source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := source.Search(ctx, WatchlistMetadataKindAnime, "星际牛仔")
		requireBangumiFailure(t, err, WatchlistMetadataFailureNetworkUnreachable)
	})
}

// 详情的三跳里任意一跳失败，整体就失败——不做「部分成功」的降级，
// 那会让一条缺了主创的详情看起来和完整的一样。
func TestWatchlistMetadataBangumiDetailPropagatesLaterHopFailures(t *testing.T) {
	t.Run("人物接口挂了", func(t *testing.T) {
		stub := newBangumiStub(t, map[string]http.HandlerFunc{
			"/v0/subjects/253":         jsonHandler(t, http.StatusOK, bangumiSubjectBody),
			"/v0/subjects/253/persons": jsonHandler(t, http.StatusInternalServerError, `{"title":"boom"}`),
		})
		source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

		detail, err := source.Detail(context.Background(), WatchlistMetadataKindAnime, "253")
		requireBangumiFailure(t, err, WatchlistMetadataFailureSourceError)
		if detail != nil {
			t.Errorf("失败时不应返回半条详情，got %+v", detail)
		}
	})

	t.Run("角色接口挂了", func(t *testing.T) {
		stub := newBangumiStub(t, map[string]http.HandlerFunc{
			"/v0/subjects/253":            jsonHandler(t, http.StatusOK, bangumiSubjectBody),
			"/v0/subjects/253/persons":    jsonHandler(t, http.StatusOK, bangumiPersonsBody),
			"/v0/subjects/253/characters": jsonHandler(t, http.StatusInternalServerError, `{"title":"boom"}`),
		})
		source := newBangumiSourceFor(t, "", stub.server.URL, WatchlistMetadataConfig{})

		detail, err := source.Detail(context.Background(), WatchlistMetadataKindAnime, "253")
		requireBangumiFailure(t, err, WatchlistMetadataFailureSourceError)
		if detail != nil {
			t.Errorf("失败时不应返回半条详情，got %+v", detail)
		}
	})
}

// 凭证不得出现在错误文案里——错误既进日志也显示给用户。
func TestWatchlistMetadataBangumiErrorDoesNotLeakAccessToken(t *testing.T) {
	// 让请求打到一个没人监听的地址，逼出带 URL 的传输层错误。
	source := newBangumiSourceFor(t, bangumiTestAccessToken, "http://"+closedLoopbackAddr(t), WatchlistMetadataConfig{})

	_, err := source.Search(context.Background(), WatchlistMetadataKindAnime, "星际牛仔")
	sourceErr := requireBangumiFailure(t, err, WatchlistMetadataFailureNetworkUnreachable)
	if strings.Contains(sourceErr.Error(), bangumiTestAccessToken) {
		t.Fatalf("错误文案里带出了 access token：%s", sourceErr.Error())
	}
	// 顺着 Unwrap 打印一层也不能漏。
	if inner := errors.Unwrap(sourceErr); inner != nil && strings.Contains(inner.Error(), bangumiTestAccessToken) {
		t.Fatalf("内层错误里带出了 access token：%s", inner.Error())
	}
}

// 路由表把 anime 指向 Bangumi，且与 TMDB 共用同一个出网客户端
// （NewWatchlistMetadataRegistry 只建一次）。
func TestWatchlistMetadataRegistryRoutesAnimeToBangumi(t *testing.T) {
	registry := newTestRegistry(t, WatchlistMetadataConfig{
		TMDBAPIKey:         tmdbTestAPIKey,
		BangumiAccessToken: bangumiTestAccessToken,
	})

	chain, err := registry.Chain(WatchlistMetadataKindAnime)
	if err != nil {
		t.Fatalf("Chain(anime) 失败: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("链长 = %d，期望 1", len(chain))
	}
	bangumi, ok := chain[0].(*BangumiWatchlistMetadataSource)
	if !ok {
		t.Fatalf("链首类型 = %T，期望 *BangumiWatchlistMetadataSource", chain[0])
	}
	if bangumi.Name() != WatchlistMetadataSourceBangumi {
		t.Errorf("源名 = %q，期望 %q", bangumi.Name(), WatchlistMetadataSourceBangumi)
	}
	if bangumi.accessToken != bangumiTestAccessToken {
		t.Errorf("token = %q，期望从配置里取到", bangumi.accessToken)
	}
	if bangumi.baseURL != bangumiAPIBaseURL {
		t.Errorf("baseURL = %q，期望生产地址 %q", bangumi.baseURL, bangumiAPIBaseURL)
	}

	// 出网客户端建一次共用：Bangumi 与 TMDB 必须拿到同一个实例，否则连接池和
	// 代理配置就散成了好几份。
	movie, err := registry.Chain(WatchlistMetadataKindMovie)
	if err != nil {
		t.Fatalf("Chain(movie) 失败: %v", err)
	}
	tmdb, ok := movie[0].(*TMDBWatchlistMetadataSource)
	if !ok {
		t.Fatalf("链首类型 = %T，期望 *TMDBWatchlistMetadataSource", movie[0])
	}
	if bangumi.client == nil {
		t.Fatal("Bangumi 适配器没有拿到出网客户端")
	}
	if bangumi.client != tmdb.client {
		t.Error("Bangumi 与 TMDB 拿到了不同的出网客户端，路由表建了多份")
	}
}

// 注册 anime 不得动到 P-003 已登记的 movie / tv / show。
func TestWatchlistMetadataRegistryKeepsTMDBRoutesAfterBangumi(t *testing.T) {
	registry := newTestRegistry(t, WatchlistMetadataConfig{TMDBAPIKey: tmdbTestAPIKey})

	for _, kind := range []WatchlistMetadataKind{
		WatchlistMetadataKindMovie,
		WatchlistMetadataKindTV,
		WatchlistMetadataKindShow,
	} {
		chain, err := registry.Chain(kind)
		if err != nil {
			t.Fatalf("Chain(%s) 失败: %v", kind, err)
		}
		if len(chain) != 1 || chain[0].Name() != WatchlistMetadataSourceTMDB {
			t.Errorf("Chain(%s) = %v，期望仍是单跳 TMDB", kind, chain)
		}
	}

	// av 自 P-005 起是 FANZA + JavBus 的两跳链，不再是「尚无适配器」。
	// 这里仍然断言它，是为了守住「注册新源不得动到别人的链」这条规矩。
	avChain, err := registry.Chain(WatchlistMetadataKindAV)
	if err != nil {
		t.Fatalf("Chain(av) 失败: %v", err)
	}
	if len(avChain) != 2 ||
		avChain[0].Name() != WatchlistMetadataSourceFANZA ||
		avChain[1].Name() != WatchlistMetadataSourceJavBus {
		t.Errorf("Chain(av) = %v，期望 FANZA 在前、JavBus 兜底在后", avChain)
	}
}

// Bangumi 的 token 为空照样能装配出路由表，并且链是可用的——
// 匿名可读的源不该因为没配凭证就从路由表里消失。
func TestWatchlistMetadataRegistryBuildsBangumiWithoutToken(t *testing.T) {
	registry := newTestRegistry(t, WatchlistMetadataConfig{})

	chain, err := registry.Chain(WatchlistMetadataKindAnime)
	if err != nil {
		t.Fatalf("Chain(anime) 失败: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("链长 = %d，期望 1", len(chain))
	}
	bangumi, ok := chain[0].(*BangumiWatchlistMetadataSource)
	if !ok {
		t.Fatalf("链首类型 = %T，期望 *BangumiWatchlistMetadataSource", chain[0])
	}
	if bangumi.accessToken != "" {
		t.Errorf("token = %q，期望空", bangumi.accessToken)
	}
}
