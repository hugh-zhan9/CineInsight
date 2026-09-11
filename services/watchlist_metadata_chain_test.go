package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 本文件覆盖 P-005 的三样东西：路由层的有序兜底（walkWatchlistMetadataChain）、
// FANZA 适配器、JavBus 适配器。
//
// 所有出网一律打 httptest 桩替身，**不碰 api.dmm.com 也不碰 www.javbus.com**。

const (
	fanzaTestAPIID       = "test-fanza-api-id-do-not-log"
	fanzaTestAffiliateID = "test-fanza-affiliate-id-do-not-log"
	javbusTestCode       = "ABC-123"
)

// ---------------------------------------------------------------------------
// 一、路由层的有序兜底
// ---------------------------------------------------------------------------

// stubWatchlistMetadataSource 是一个可编排的假源，用来把走链策略与真实 HTTP 隔开。
// 六类失败各造一次太贵也没必要——分类码是什么、走不走下一跳，这里一次就能说清。
type stubWatchlistMetadataSource struct {
	name string
	// searchCalls / detailCalls 记录被问了几次，用来断言「有没有走到这一跳」。
	searchCalls atomic.Int64
	detailCalls atomic.Int64

	candidates []WatchlistMetadataCandidate
	detail     *WatchlistMetadataDetail
	err        error
}

func (s *stubWatchlistMetadataSource) Name() string { return s.name }

func (s *stubWatchlistMetadataSource) Search(_ context.Context, _ WatchlistMetadataKind, _ string) ([]WatchlistMetadataCandidate, error) {
	s.searchCalls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return s.candidates, nil
}

func (s *stubWatchlistMetadataSource) Detail(_ context.Context, _ WatchlistMetadataKind, _ string) (*WatchlistMetadataDetail, error) {
	s.detailCalls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return s.detail, nil
}

// failingStubSource 造一个固定以某个分类失败的假源。
func failingStubSource(name string, failure WatchlistMetadataFailure) *stubWatchlistMetadataSource {
	return &stubWatchlistMetadataSource{
		name: name,
		err:  newWatchlistMetadataSourceError(name, failure, 0, "桩替身固定失败", nil),
	}
}

// succeedingStubSource 造一个固定成功的假源，候选与详情都如实写上自己的源名。
func succeedingStubSource(name string) *stubWatchlistMetadataSource {
	candidate := WatchlistMetadataCandidate{
		SourceName:   name,
		SourceItemID: name + "-item",
		Title:        name + " 给的结果",
	}
	return &stubWatchlistMetadataSource{
		name:       name,
		candidates: []WatchlistMetadataCandidate{candidate},
		detail:     &WatchlistMetadataDetail{WatchlistMetadataCandidate: candidate},
	}
}

// 这是本切片的头号规则：**兜底只在源明确返回「无此条目」时触发。**
//
// 六类失败逐一过一遍，断言的是同一件事——第二跳到底走没走。not_found 之外的五类
// 都不得触发兜底：把配置问题伪装成「查无此片」会让用户往错误方向排查（去核对番号，
// 而不是去填凭证、修代理），而且每次失败都白白多打一次抓取请求。
func TestWatchlistMetadataChainFallsBackOnlyOnNotFound(t *testing.T) {
	for _, tc := range []struct {
		failure      WatchlistMetadataFailure
		wantFallback bool
	}{
		{WatchlistMetadataFailureNotFound, true},
		{WatchlistMetadataFailureCredentialMissing, false},
		{WatchlistMetadataFailureCredentialInvalid, false},
		{WatchlistMetadataFailureProxyUnreachable, false},
		{WatchlistMetadataFailureNetworkUnreachable, false},
		{WatchlistMetadataFailureSourceError, false},
	} {
		t.Run(string(tc.failure), func(t *testing.T) {
			t.Run("Search", func(t *testing.T) {
				first := failingStubSource(WatchlistMetadataSourceFANZA, tc.failure)
				second := succeedingStubSource(WatchlistMetadataSourceJavBus)
				chain := []WatchlistMetadataSource{first, second}

				candidates, err := SearchWatchlistMetadataChain(context.Background(), chain, WatchlistMetadataKindAV, javbusTestCode)

				if first.searchCalls.Load() != 1 {
					t.Fatalf("首选源被问了 %d 次，期望 1", first.searchCalls.Load())
				}
				if got := second.searchCalls.Load() != 0; got != tc.wantFallback {
					t.Fatalf("兜底源被问了 %d 次（走没走兜底 = %v），期望走兜底 = %v",
						second.searchCalls.Load(), got, tc.wantFallback)
				}

				if tc.wantFallback {
					if err != nil {
						t.Fatalf("走了兜底却仍失败: %v", err)
					}
					if len(candidates) != 1 {
						t.Fatalf("候选数 = %d，期望 1", len(candidates))
					}
					return
				}
				// 不兜底的五类：错误必须原样返回，分类码与源名都不能被改写，
				// 否则用户看到的排查方向就是错的。
				if got := WatchlistMetadataFailureOf(err); got != tc.failure {
					t.Fatalf("失败分类 = %q，期望原样返回 %q", got, tc.failure)
				}
				var sourceErr *WatchlistMetadataSourceError
				if !errors.As(err, &sourceErr) {
					t.Fatalf("错误不是 *WatchlistMetadataSourceError: %v", err)
				}
				if sourceErr.Source != WatchlistMetadataSourceFANZA {
					t.Errorf("错误来源 = %q，期望 %q", sourceErr.Source, WatchlistMetadataSourceFANZA)
				}
				if candidates != nil {
					t.Errorf("失败时不应返回候选，got %v", candidates)
				}
			})

			t.Run("Detail", func(t *testing.T) {
				first := failingStubSource(WatchlistMetadataSourceFANZA, tc.failure)
				second := succeedingStubSource(WatchlistMetadataSourceJavBus)
				chain := []WatchlistMetadataSource{first, second}

				detail, err := DetailWatchlistMetadataChain(context.Background(), chain, WatchlistMetadataKindAV, javbusTestCode)

				if got := second.detailCalls.Load() != 0; got != tc.wantFallback {
					t.Fatalf("兜底源被问了 %d 次（走没走兜底 = %v），期望走兜底 = %v",
						second.detailCalls.Load(), got, tc.wantFallback)
				}
				if tc.wantFallback {
					if err != nil || detail == nil {
						t.Fatalf("走了兜底却仍失败: %v", err)
					}
					return
				}
				if got := WatchlistMetadataFailureOf(err); got != tc.failure {
					t.Fatalf("失败分类 = %q，期望原样返回 %q", got, tc.failure)
				}
				if detail != nil {
					t.Errorf("失败时不应返回详情，got %v", detail)
				}
			})
		})
	}
}

// 两跳都说「没有这个条目」时，最终状态就是 not_found——不是 source_error，
// 也不是一条没有分类码的错误。条目的 EnrichmentError 直接存这个分类码。
func TestWatchlistMetadataChainEndsNotFoundWhenBothHopsMiss(t *testing.T) {
	first := failingStubSource(WatchlistMetadataSourceFANZA, WatchlistMetadataFailureNotFound)
	second := failingStubSource(WatchlistMetadataSourceJavBus, WatchlistMetadataFailureNotFound)
	chain := []WatchlistMetadataSource{first, second}

	_, err := SearchWatchlistMetadataChain(context.Background(), chain, WatchlistMetadataKindAV, javbusTestCode)
	if got := WatchlistMetadataFailureOf(err); got != WatchlistMetadataFailureNotFound {
		t.Fatalf("最终失败分类 = %q，期望 %q", got, WatchlistMetadataFailureNotFound)
	}
	if first.searchCalls.Load() != 1 || second.searchCalls.Load() != 1 {
		t.Fatalf("两跳各应被问一次，实际 %d / %d", first.searchCalls.Load(), second.searchCalls.Load())
	}
	// 交出的是最后一跳的错误，源名如实记着最后问过谁。
	var sourceErr *WatchlistMetadataSourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("错误不是 *WatchlistMetadataSourceError: %v", err)
	}
	if sourceErr.Source != WatchlistMetadataSourceJavBus {
		t.Errorf("错误来源 = %q，期望最后一跳 %q", sourceErr.Source, WatchlistMetadataSourceJavBus)
	}
}

// SourceName 必须是**真正产出结果的那个源**，不是首选源。它会原样落到
// WatchlistEntry.SourceName，是之后按源重查详情的唯一依据；写成 fanza 而结果
// 其实来自 JavBus，重查必然查空。
func TestWatchlistMetadataChainReportsProducingSource(t *testing.T) {
	first := failingStubSource(WatchlistMetadataSourceFANZA, WatchlistMetadataFailureNotFound)
	second := succeedingStubSource(WatchlistMetadataSourceJavBus)
	chain := []WatchlistMetadataSource{first, second}

	candidates, err := SearchWatchlistMetadataChain(context.Background(), chain, WatchlistMetadataKindAV, javbusTestCode)
	if err != nil {
		t.Fatalf("走链失败: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("候选数 = %d，期望 1", len(candidates))
	}
	if candidates[0].SourceName != WatchlistMetadataSourceJavBus {
		t.Errorf("SourceName = %q，期望产出结果的源 %q", candidates[0].SourceName, WatchlistMetadataSourceJavBus)
	}

	detail, err := DetailWatchlistMetadataChain(context.Background(), chain, WatchlistMetadataKindAV, javbusTestCode)
	if err != nil {
		t.Fatalf("走链取详情失败: %v", err)
	}
	if detail.SourceName != WatchlistMetadataSourceJavBus {
		t.Errorf("详情 SourceName = %q，期望 %q", detail.SourceName, WatchlistMetadataSourceJavBus)
	}
}

// 首选源成功时**不问**兜底源：多打一次抓取请求换不来任何东西。
func TestWatchlistMetadataChainStopsAtFirstSuccess(t *testing.T) {
	first := succeedingStubSource(WatchlistMetadataSourceFANZA)
	second := succeedingStubSource(WatchlistMetadataSourceJavBus)
	chain := []WatchlistMetadataSource{first, second}

	candidates, err := SearchWatchlistMetadataChain(context.Background(), chain, WatchlistMetadataKindAV, javbusTestCode)
	if err != nil {
		t.Fatalf("走链失败: %v", err)
	}
	if candidates[0].SourceName != WatchlistMetadataSourceFANZA {
		t.Errorf("SourceName = %q，期望首选源 %q", candidates[0].SourceName, WatchlistMetadataSourceFANZA)
	}
	if second.searchCalls.Load() != 0 {
		t.Errorf("首选源已经成功，兜底源却被问了 %d 次", second.searchCalls.Load())
	}
}

// 没有失败分类码的错误同样不兜底：类型传错是调用方的问题，换个源问一遍没有意义。
func TestWatchlistMetadataChainDoesNotFallBackOnUnclassifiedError(t *testing.T) {
	first := &stubWatchlistMetadataSource{
		name: WatchlistMetadataSourceFANZA,
		err:  fmt.Errorf("%w：FANZA 不覆盖 %q", ErrWatchlistMetadataKindUnsupported, "movie"),
	}
	second := succeedingStubSource(WatchlistMetadataSourceJavBus)

	_, err := SearchWatchlistMetadataChain(context.Background(), []WatchlistMetadataSource{first, second}, WatchlistMetadataKindMovie, "沙丘")
	if !errors.Is(err, ErrWatchlistMetadataKindUnsupported) {
		t.Fatalf("错误 = %v，期望原样返回 ErrWatchlistMetadataKindUnsupported", err)
	}
	if second.searchCalls.Load() != 0 {
		t.Errorf("没有分类码的错误不该触发兜底，兜底源却被问了 %d 次", second.searchCalls.Load())
	}
}

// 空链与「没登记这个类型」是同一件事，给同一条错误，调用方只需判一次。
func TestWatchlistMetadataChainRejectsEmptyChain(t *testing.T) {
	if _, err := SearchWatchlistMetadataChain(context.Background(), nil, WatchlistMetadataKindAV, javbusTestCode); !errors.Is(err, ErrWatchlistMetadataKindUnsupported) {
		t.Errorf("空链搜索的错误 = %v，期望 ErrWatchlistMetadataKindUnsupported", err)
	}
	if _, err := DetailWatchlistMetadataChain(context.Background(), nil, WatchlistMetadataKindAV, javbusTestCode); !errors.Is(err, ErrWatchlistMetadataKindUnsupported) {
		t.Errorf("空链取详情的错误 = %v，期望 ErrWatchlistMetadataKindUnsupported", err)
	}
}

// 走链是有序的：三跳链上，排在前面的先问，且一旦不是 not_found 就立刻停。
func TestWatchlistMetadataChainAsksInOrder(t *testing.T) {
	first := failingStubSource("first", WatchlistMetadataFailureNotFound)
	second := failingStubSource("second", WatchlistMetadataFailureSourceError)
	third := succeedingStubSource("third")

	_, err := SearchWatchlistMetadataChain(context.Background(), []WatchlistMetadataSource{first, second, third}, WatchlistMetadataKindAV, javbusTestCode)
	if got := WatchlistMetadataFailureOf(err); got != WatchlistMetadataFailureSourceError {
		t.Fatalf("失败分类 = %q，期望在第二跳的 %q 上停住", got, WatchlistMetadataFailureSourceError)
	}
	if first.searchCalls.Load() != 1 || second.searchCalls.Load() != 1 {
		t.Errorf("前两跳各应被问一次，实际 %d / %d", first.searchCalls.Load(), second.searchCalls.Load())
	}
	if third.searchCalls.Load() != 0 {
		t.Errorf("第二跳不是 not_found，第三跳不该被问，实际 %d 次", third.searchCalls.Load())
	}
}

// ---------------------------------------------------------------------------
// 二、FANZA 适配器
// ---------------------------------------------------------------------------

// fanzaStub 是 FANZA 的桩替身。测试一律打到它，不碰 api.dmm.com。
type fanzaStub struct {
	server *httptest.Server
	hits   atomic.Int64

	mu       sync.Mutex
	requests []*url.URL
}

func newFANZAStub(t *testing.T, handler http.HandlerFunc) *fanzaStub {
	t.Helper()
	stub := &fanzaStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.hits.Add(1)
		stub.mu.Lock()
		stub.requests = append(stub.requests, r.URL)
		stub.mu.Unlock()
		if r.URL.Path != "/ItemList" {
			t.Errorf("桩替身收到未登记的路径 %q", r.URL.Path)
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *fanzaStub) lastQuery(t *testing.T) url.Values {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("桩替身没有收到任何请求")
	}
	return s.requests[len(s.requests)-1].Query()
}

// newFANZASourceFor 走生产路径同一个客户端构造器。测试也不自建 http.Client：
// 代理行为是要验证的东西之一，绕过它就等于没验证。
func newFANZASourceFor(t *testing.T, apiID string, affiliateID string, baseURL string, config WatchlistMetadataConfig) *FANZAWatchlistMetadataSource {
	t.Helper()
	client, err := NewWatchlistMetadataHTTPClient(config, 5*time.Second)
	if err != nil {
		t.Fatalf("构造出网客户端失败: %v", err)
	}
	source := NewFANZAWatchlistMetadataSource(apiID, affiliateID, client)
	source.baseURL = baseURL
	return source
}

func requireFANZAFailure(t *testing.T, err error, want WatchlistMetadataFailure) *WatchlistMetadataSourceError {
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
	if sourceErr.Source != WatchlistMetadataSourceFANZA {
		t.Errorf("Source = %q，期望 %q", sourceErr.Source, WatchlistMetadataSourceFANZA)
	}
	return sourceErr
}

const fanzaItemListBody = `{
  "result": {
    "result_count": 2,
    "items": [
      {
        "content_id": "abc00123",
        "product_id": "abc00123",
        "title": "作品タイトル",
        "date": "2020-03-04 10:00:00",
        "imageURL": {
          "list": "https://pics.example.test/abc00123pt.jpg",
          "small": "https://pics.example.test/abc00123ps.jpg",
          "large": "https://pics.example.test/abc00123pl.jpg"
        },
        "review": {"count": 12, "average": "4.50"},
        "iteminfo": {
          "genre": [{"id": 1, "name": "ジャンルA"}, {"id": 2, "name": "ジャンルB"}],
          "director": [{"id": 3, "name": "監督名"}],
          "actress": [{"id": 4, "name": "女優A"}, {"id": 5, "name": "女優B"}]
        }
      },
      {
        "content_id": "xyz00456",
        "title": "二番目",
        "date": "",
        "imageURL": {"list": "https://pics.example.test/xyz00456pt.jpg"},
        "review": {"average": 3.25},
        "iteminfo": {}
      }
    ]
  }
}`

// 搜索把字段映射对，并保持 FANZA 给的顺序（不重排、不筛选）。
func TestWatchlistMetadataFANZASearchMapsItems(t *testing.T) {
	stub := newFANZAStub(t, jsonHandler(t, http.StatusOK, fanzaItemListBody))
	source := newFANZASourceFor(t, fanzaTestAPIID, fanzaTestAffiliateID, stub.server.URL, WatchlistMetadataConfig{})

	candidates, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("候选数 = %d，期望 2", len(candidates))
	}

	first := candidates[0]
	if first.SourceName != WatchlistMetadataSourceFANZA {
		t.Errorf("SourceName = %q，期望 %q", first.SourceName, WatchlistMetadataSourceFANZA)
	}
	// SourceItemID 是承重字段：丢了它这条候选就再也回不到源上。
	if first.SourceItemID != "abc00123" {
		t.Errorf("SourceItemID = %q，期望 content_id \"abc00123\"", first.SourceItemID)
	}
	if first.Title != "作品タイトル" || first.OriginalTitle != "作品タイトル" {
		t.Errorf("标题 = %q / %q，期望都是日文原名", first.Title, first.OriginalTitle)
	}
	if first.Year != 2020 {
		t.Errorf("Year = %d，期望 2020", first.Year)
	}
	if first.Rating != 4.5 {
		t.Errorf("Rating = %v，期望 4.5（字符串形态的 average）", first.Rating)
	}
	// 海报按清晰度从高到低取，large 有值就用 large。
	if first.PosterURL != "https://pics.example.test/abc00123pl.jpg" {
		t.Errorf("PosterURL = %q，期望取 large", first.PosterURL)
	}
	// ItemList 不返回简介，留空而不是编一段。
	if first.Overview != "" {
		t.Errorf("Overview = %q，期望留空", first.Overview)
	}

	second := candidates[1]
	if second.SourceItemID != "xyz00456" {
		t.Errorf("第二条 SourceItemID = %q，顺序被改了", second.SourceItemID)
	}
	// 数字形态的 average 也要能解出来，否则一条**查到了**的响应会被判成 source_error。
	if second.Rating != 3.25 {
		t.Errorf("第二条 Rating = %v，期望 3.25（数字形态的 average）", second.Rating)
	}
	// 未定档时不猜年份。
	if second.Year != 0 {
		t.Errorf("第二条 Year = %d，期望 0", second.Year)
	}
	// large 缺失时退到 list。
	if second.PosterURL != "https://pics.example.test/xyz00456pt.jpg" {
		t.Errorf("第二条 PosterURL = %q，期望退到 list", second.PosterURL)
	}
}

// 请求参数要带齐 DMM 要求的定位参数与两个凭证，番号原样进 keyword。
func TestWatchlistMetadataFANZASearchSendsExpectedQuery(t *testing.T) {
	stub := newFANZAStub(t, jsonHandler(t, http.StatusOK, fanzaItemListBody))
	source := newFANZASourceFor(t, fanzaTestAPIID, fanzaTestAffiliateID, stub.server.URL, WatchlistMetadataConfig{})

	if _, err := source.Search(context.Background(), WatchlistMetadataKindAV, "  "+javbusTestCode+"  "); err != nil {
		t.Fatalf("搜索失败: %v", err)
	}

	query := stub.lastQuery(t)
	for key, want := range map[string]string{
		"api_id":       fanzaTestAPIID,
		"affiliate_id": fanzaTestAffiliateID,
		"site":         fanzaSite,
		"service":      fanzaService,
		"floor":        fanzaFloor,
		"output":       fanzaOutputJSON,
		"sort":         fanzaSortByMatch,
		// 番号是 AV 的匹配键，首尾空白要去掉，但内容不做任何归一化。
		"keyword": javbusTestCode,
	} {
		if got := query.Get(key); got != want {
			t.Errorf("query %s = %q，期望 %q", key, got, want)
		}
	}
}

// 详情走 ItemList 的 cid 过滤，并把类型、导演、女优都映射出来。
func TestWatchlistMetadataFANZADetailMapsCredits(t *testing.T) {
	stub := newFANZAStub(t, jsonHandler(t, http.StatusOK, fanzaItemListBody))
	source := newFANZASourceFor(t, fanzaTestAPIID, fanzaTestAffiliateID, stub.server.URL, WatchlistMetadataConfig{})

	detail, err := source.Detail(context.Background(), WatchlistMetadataKindAV, "abc00123")
	if err != nil {
		t.Fatalf("取详情失败: %v", err)
	}
	if got := stub.lastQuery(t).Get("cid"); got != "abc00123" {
		t.Errorf("query cid = %q，期望 \"abc00123\"", got)
	}
	if detail.SourceItemID != "abc00123" {
		t.Errorf("SourceItemID = %q，期望保留源条目 ID", detail.SourceItemID)
	}
	if strings.Join(detail.Genres, ",") != "ジャンルA,ジャンルB" {
		t.Errorf("Genres = %v，期望按源顺序取 iteminfo.genre", detail.Genres)
	}
	if strings.Join(detail.Directors, ",") != "監督名" {
		t.Errorf("Directors = %v，期望取 iteminfo.director", detail.Directors)
	}
	if strings.Join(detail.Cast, ",") != "女優A,女優B" {
		t.Errorf("Cast = %v，期望取 iteminfo.actress", detail.Cast)
	}
}

// not_found：200 加空 items 就是 FANZA 说「没有收录」。它是唯一会触发 JavBus
// 兜底的分类，必须在适配器里认定，不能留给调用方去猜一个空切片的含义。
func TestWatchlistMetadataFANZAEmptyItemsIsNotFound(t *testing.T) {
	stub := newFANZAStub(t, jsonHandler(t, http.StatusOK, `{"result":{"result_count":0,"items":[]}}`))
	source := newFANZASourceFor(t, fanzaTestAPIID, fanzaTestAffiliateID, stub.server.URL, WatchlistMetadataConfig{})

	_, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
	requireFANZAFailure(t, err, WatchlistMetadataFailureNotFound)

	// 详情同理：cid 查不到东西也是 not_found。
	_, err = source.Detail(context.Background(), WatchlistMetadataKindAV, "abc00123")
	requireFANZAFailure(t, err, WatchlistMetadataFailureNotFound)
}

// credential_missing：两个凭证缺任何一个都判定失败，而且**不发请求**。
// 发出去只会把一次配置问题变成一条真实的对外记录，还会白白触发一次兜底。
func TestWatchlistMetadataFANZACredentialMissingSendsNoRequest(t *testing.T) {
	for name, credentials := range map[string][2]string{
		"两个都没配":          {"", ""},
		"缺 api_id":       {"", fanzaTestAffiliateID},
		"缺 affiliate_id": {fanzaTestAPIID, ""},
		"只有空白":           {"   ", "   "},
	} {
		t.Run(name, func(t *testing.T) {
			stub := newFANZAStub(t, jsonHandler(t, http.StatusOK, fanzaItemListBody))
			source := newFANZASourceFor(t, credentials[0], credentials[1], stub.server.URL, WatchlistMetadataConfig{})

			_, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
			requireFANZAFailure(t, err, WatchlistMetadataFailureCredentialMissing)
			if hits := stub.hits.Load(); hits != 0 {
				t.Errorf("缺凭证时桩替身收到 %d 次请求，期望 0", hits)
			}
		})
	}
}

// 非 2xx 的分类：401 / 403 是 credential_invalid，其余是 source_error，404 是 not_found。
func TestWatchlistMetadataFANZAClassifiesHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		status  int
		failure WatchlistMetadataFailure
	}{
		{http.StatusUnauthorized, WatchlistMetadataFailureCredentialInvalid},
		{http.StatusForbidden, WatchlistMetadataFailureCredentialInvalid},
		{http.StatusNotFound, WatchlistMetadataFailureNotFound},
		{http.StatusInternalServerError, WatchlistMetadataFailureSourceError},
		{http.StatusTooManyRequests, WatchlistMetadataFailureSourceError},
	} {
		t.Run(fmt.Sprintf("HTTP%d", tc.status), func(t *testing.T) {
			stub := newFANZAStub(t, jsonHandler(t, tc.status, `{"result":{"errors":[{"message":"boom"}]}}`))
			source := newFANZASourceFor(t, fanzaTestAPIID, fanzaTestAffiliateID, stub.server.URL, WatchlistMetadataConfig{})

			_, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
			sourceErr := requireFANZAFailure(t, err, tc.failure)
			if sourceErr.HTTPStatus != tc.status {
				t.Errorf("HTTPStatus = %d，期望 %d", sourceErr.HTTPStatus, tc.status)
			}
		})
	}
}

// source_error：响应不是预期的 JSON。这不能被当成「没有收录」——
// 那会让一次源侧异常静默地触发兜底。
func TestWatchlistMetadataFANZAUnparsableBodyIsSourceError(t *testing.T) {
	stub := newFANZAStub(t, jsonHandler(t, http.StatusOK, `<html>not json</html>`))
	source := newFANZASourceFor(t, fanzaTestAPIID, fanzaTestAffiliateID, stub.server.URL, WatchlistMetadataConfig{})

	_, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
	requireFANZAFailure(t, err, WatchlistMetadataFailureSourceError)
}

// proxy_unreachable：代理这一段不通，与「目标站不通」分开。
func TestWatchlistMetadataFANZAProxyUnreachable(t *testing.T) {
	stub := newFANZAStub(t, jsonHandler(t, http.StatusOK, fanzaItemListBody))
	config := WatchlistMetadataConfig{ProxyURL: "http://" + closedLoopbackAddr(t)}
	source := newFANZASourceFor(t, fanzaTestAPIID, fanzaTestAffiliateID, stub.server.URL, config)

	_, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
	requireFANZAFailure(t, err, WatchlistMetadataFailureProxyUnreachable)
	if hits := stub.hits.Load(); hits != 0 {
		t.Errorf("走坏代理时桩替身收到 %d 次请求，期望 0", hits)
	}
}

// network_unreachable：连接失败与超时。都不得被当成「源没有收录」。
func TestWatchlistMetadataFANZANetworkUnreachable(t *testing.T) {
	t.Run("连接被拒", func(t *testing.T) {
		source := newFANZASourceFor(t, fanzaTestAPIID, fanzaTestAffiliateID, "http://"+closedLoopbackAddr(t), WatchlistMetadataConfig{})
		_, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
		requireFANZAFailure(t, err, WatchlistMetadataFailureNetworkUnreachable)
	})

	t.Run("超时", func(t *testing.T) {
		release := make(chan struct{})
		stub := newFANZAStub(t, func(w http.ResponseWriter, _ *http.Request) {
			<-release
			w.WriteHeader(http.StatusOK)
		})
		// 先放行再让 httptest 关服务器，否则 Close 会等在这个 handler 上。
		t.Cleanup(func() { close(release) })
		source := newFANZASourceFor(t, fanzaTestAPIID, fanzaTestAffiliateID, stub.server.URL, WatchlistMetadataConfig{})

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := source.Search(ctx, WatchlistMetadataKindAV, javbusTestCode)
		requireFANZAFailure(t, err, WatchlistMetadataFailureNetworkUnreachable)
	})
}

// 两个凭证都在 query 里，*url.Error 会把整个 URL 带进 Error()。
// 抹一个不抹另一个等于没抹。
func TestWatchlistMetadataFANZARedactsBothCredentials(t *testing.T) {
	source := newFANZASourceFor(t, fanzaTestAPIID, fanzaTestAffiliateID, "http://"+closedLoopbackAddr(t), WatchlistMetadataConfig{})

	_, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
	sourceErr := requireFANZAFailure(t, err, WatchlistMetadataFailureNetworkUnreachable)

	for _, secret := range []string{fanzaTestAPIID, fanzaTestAffiliateID} {
		if strings.Contains(sourceErr.Error(), secret) {
			t.Errorf("错误文案里带出了凭证 %q：%s", secret, sourceErr.Error())
		}
		// 顺着 Unwrap 打印一层也不能漏。
		if inner := errors.Unwrap(sourceErr); inner != nil && strings.Contains(inner.Error(), secret) {
			t.Errorf("内层错误里带出了凭证 %q：%s", secret, inner.Error())
		}
	}
}

// 适配器只接 av。类型传错立刻说清楚，而且这条错误不带分类码，走链时不会被
// 当成 not_found 去触发兜底。
func TestWatchlistMetadataFANZARejectsOtherKinds(t *testing.T) {
	stub := newFANZAStub(t, jsonHandler(t, http.StatusOK, fanzaItemListBody))
	source := newFANZASourceFor(t, fanzaTestAPIID, fanzaTestAffiliateID, stub.server.URL, WatchlistMetadataConfig{})

	for _, kind := range []WatchlistMetadataKind{
		WatchlistMetadataKindMovie,
		WatchlistMetadataKindTV,
		WatchlistMetadataKindShow,
		WatchlistMetadataKindAnime,
	} {
		if _, err := source.Search(context.Background(), kind, javbusTestCode); !errors.Is(err, ErrWatchlistMetadataKindUnsupported) {
			t.Errorf("Search(%s) 错误 = %v，期望 ErrWatchlistMetadataKindUnsupported", kind, err)
		}
		if got := WatchlistMetadataFailureOf(fmt.Errorf("%w", ErrWatchlistMetadataKindUnsupported)); got == WatchlistMetadataFailureNotFound {
			t.Error("类型不支持的错误不该带 not_found 分类")
		}
	}
	if hits := stub.hits.Load(); hits != 0 {
		t.Errorf("类型不对时桩替身收到 %d 次请求，期望 0", hits)
	}
}

// ---------------------------------------------------------------------------
// 三、JavBus 适配器
// ---------------------------------------------------------------------------

// javbusDetailPage 是一张按 JavBus 详情页公开结构写的样例页。
// **形态未经真实抓取验证**，残留风险里记着。
const javbusDetailPage = `<!DOCTYPE html><html><head><title>ABC-123</title></head><body>
<nav><a href="/genre/nav-noise">导航里的类型链接</a></nav>
<div class="container">
<h3>ABC-123 サンプル作品 &amp; 続編</h3>
<a class="bigImage" href="/pics/cover/abc123_b.jpg"><img src="/pics/cover/abc123_b.jpg"></a>
<div class="col-md-3 info">
<p><span class="header">識別碼:</span> <span style="color:#CC0000;"><b>ABC-123</b></span> <a class="btn btn-mini-new btn-warning">複製</a></p>
<p><span class="header">發行日期:</span> 2021-07-15</p>
<p><span class="header">長度:</span> 120分鐘</p>
<p><span class="header">導演:</span> <a href="/director/ab">監督サンプル</a></p>
<p><span class="header">製作商:</span> <a href="/studio/cd">スタジオ</a></p>
<p class="header">類別:</p>
<p><span class="genre"><label class="cbGenre"><a href="/genre/g1">ジャンルA</a></label></span>
<span class="genre"><label class="cbGenre"><a href="/genre/g2">ジャンルB</a></label></span>
<span class="genre"><label class="cbGenre"><a href="/genre/g1">ジャンルA</a></label></span></p>
<p class="star-show">演員</p>
<p><span class="genre"><a href="/star/s1">女優A</a></span>
<span class="genre"><a href="/star/s2">女優B</a></span></p>
</div>
</div></body></html>`

// newJavBusStub 建 JavBus 的桩替身。测试一律打到它，不碰 www.javbus.com。
func newJavBusStub(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int64, *sync.Map) {
	t.Helper()
	hits := &atomic.Int64{}
	headers := &sync.Map{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		headers.Store("User-Agent", r.Header.Get("User-Agent"))
		headers.Store("path", r.URL.Path)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server, hits, headers
}

func newJavBusSourceFor(t *testing.T, baseURL string, config WatchlistMetadataConfig) *JavBusWatchlistMetadataSource {
	t.Helper()
	client, err := NewWatchlistMetadataHTTPClient(config, 5*time.Second)
	if err != nil {
		t.Fatalf("构造出网客户端失败: %v", err)
	}
	source := NewJavBusWatchlistMetadataSource(client)
	source.baseURL = baseURL
	return source
}

func requireJavBusFailure(t *testing.T, err error, want WatchlistMetadataFailure) *WatchlistMetadataSourceError {
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
	if sourceErr.Source != WatchlistMetadataSourceJavBus {
		t.Errorf("Source = %q，期望 %q", sourceErr.Source, WatchlistMetadataSourceJavBus)
	}
	return sourceErr
}

func htmlHandler(t *testing.T, status int, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("桩替身写响应失败: %v", err)
		}
	}
}

// 详情页解析：番号、标题、日期、导演、类型、演员、封面各就各位。
func TestWatchlistMetadataJavBusDetailParsesPage(t *testing.T) {
	server, _, headers := newJavBusStub(t, htmlHandler(t, http.StatusOK, javbusDetailPage))
	source := newJavBusSourceFor(t, server.URL, WatchlistMetadataConfig{})

	detail, err := source.Detail(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
	if err != nil {
		t.Fatalf("取详情失败: %v", err)
	}

	// 番号就是路径，直接落在 URL 上。
	if path, _ := headers.Load("path"); path != "/"+javbusTestCode {
		t.Errorf("请求路径 = %v，期望 /%s", path, javbusTestCode)
	}
	// Go 默认 UA 基本等于自报家门，必须显式覆盖。
	if ua, _ := headers.Load("User-Agent"); ua != javbusUserAgent {
		t.Errorf("User-Agent = %v，期望 %q", ua, javbusUserAgent)
	}

	if detail.SourceName != WatchlistMetadataSourceJavBus {
		t.Errorf("SourceName = %q，期望 %q", detail.SourceName, WatchlistMetadataSourceJavBus)
	}
	// SourceItemID 以页面上的識別碼为准，而且要跳过同一行里的「複製」按钮。
	if detail.SourceItemID != "ABC-123" {
		t.Errorf("SourceItemID = %q，期望 \"ABC-123\"", detail.SourceItemID)
	}
	// HTML 实体要还原，空白要折叠。
	if detail.Title != "ABC-123 サンプル作品 & 続編" {
		t.Errorf("Title = %q", detail.Title)
	}
	if detail.OriginalTitle != detail.Title {
		t.Errorf("OriginalTitle = %q，期望与 Title 一致（JavBus 只登记日文原名）", detail.OriginalTitle)
	}
	if detail.Year != 2021 {
		t.Errorf("Year = %d，期望 2021", detail.Year)
	}
	if strings.Join(detail.Directors, ",") != "監督サンプル" {
		t.Errorf("Directors = %v", detail.Directors)
	}
	// 类型去重且保持首次出现的顺序；页头导航里的 /genre/ 链接不能混进来。
	if strings.Join(detail.Genres, ",") != "ジャンルA,ジャンルB" {
		t.Errorf("Genres = %v，期望去重、保序且不含页头导航里的链接", detail.Genres)
	}
	if strings.Join(detail.Cast, ",") != "女優A,女優B" {
		t.Errorf("Cast = %v", detail.Cast)
	}
	// 相对路径的封面要补成绝对地址。
	if detail.PosterURL != server.URL+"/pics/cover/abc123_b.jpg" {
		t.Errorf("PosterURL = %q，期望补成绝对地址", detail.PosterURL)
	}
	// JavBus 没有简介也没有评分，留空而不是编。
	if detail.Overview != "" || detail.Rating != 0 {
		t.Errorf("Overview = %q, Rating = %v，期望都为空值", detail.Overview, detail.Rating)
	}
}

// 番号是 AV 的主键，按它查只该有一条结果，所以搜索与取详情打同一个页面。
func TestWatchlistMetadataJavBusSearchReturnsSingleCandidate(t *testing.T) {
	server, hits, _ := newJavBusStub(t, htmlHandler(t, http.StatusOK, javbusDetailPage))
	source := newJavBusSourceFor(t, server.URL, WatchlistMetadataConfig{})

	candidates, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("候选数 = %d，期望 1", len(candidates))
	}
	if candidates[0].SourceItemID != "ABC-123" || candidates[0].SourceName != WatchlistMetadataSourceJavBus {
		t.Errorf("候选 = %+v", candidates[0])
	}
	if hits.Load() != 1 {
		t.Errorf("桩替身收到 %d 次请求，期望 1", hits.Load())
	}
}

// 404 是 JavBus 唯一一处「明确说没有这个番号」。
func TestWatchlistMetadataJavBusNotFound(t *testing.T) {
	server, _, _ := newJavBusStub(t, htmlHandler(t, http.StatusNotFound, `<html><body>404</body></html>`))
	source := newJavBusSourceFor(t, server.URL, WatchlistMetadataConfig{})

	_, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
	requireJavBusFailure(t, err, WatchlistMetadataFailureNotFound)
}

// **本适配器最要紧的一条规矩**：页面结构对不上一律 source_error，绝不 not_found。
//
// 源站改版会让所有选择器同时失效。那时报「这片不存在」，用户会去核对番号，
// 而真正该做的是修适配器；报 source_error 才把人指向正确的方向。
func TestWatchlistMetadataJavBusStructureChangeIsSourceErrorNotNotFound(t *testing.T) {
	for name, page := range map[string]string{
		"缺 h3 标题": `<html><body><div class="col-md-3 info">
<p><span class="header">識別碼:</span> <b>ABC-123</b></p></div></body></html>`,
		"缺信息栏": `<html><body><h3>ABC-123 タイトル</h3><div class="col-md-9">别的东西</div></body></html>`,
		"缺識別碼": `<html><body><h3>ABC-123 タイトル</h3><div class="col-md-3 info">
<p><span class="header">發行日期:</span> 2021-07-15</p></div></body></html>`,
		"整页改版":  `<html><body><main><article>全新的结构</article></main></body></html>`,
		"反爬拦截页": `<html><body>Checking your browser before accessing...</body></html>`,
		"空响应":   ``,
	} {
		t.Run(name, func(t *testing.T) {
			server, _, _ := newJavBusStub(t, htmlHandler(t, http.StatusOK, page))
			source := newJavBusSourceFor(t, server.URL, WatchlistMetadataConfig{})

			_, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
			sourceErr := requireJavBusFailure(t, err, WatchlistMetadataFailureSourceError)
			if got := WatchlistMetadataFailureOf(sourceErr); got == WatchlistMetadataFailureNotFound {
				t.Fatal("解析失败被误报成了 not_found")
			}
		})
	}
}

// 非 2xx（404 除外）一律 source_error，**不认 credential_invalid**：
// JavBus 压根不要凭证，403 只可能是反爬拦截；归成凭证错误会让用户去翻一个
// 根本不存在的设置项。
func TestWatchlistMetadataJavBusNon2xxIsSourceErrorNotCredentialInvalid(t *testing.T) {
	for _, status := range []int{
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusServiceUnavailable,
	} {
		t.Run(fmt.Sprintf("HTTP%d", status), func(t *testing.T) {
			server, _, _ := newJavBusStub(t, htmlHandler(t, status, `<html><body>blocked</body></html>`))
			source := newJavBusSourceFor(t, server.URL, WatchlistMetadataConfig{})

			_, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
			sourceErr := requireJavBusFailure(t, err, WatchlistMetadataFailureSourceError)
			if sourceErr.HTTPStatus != status {
				t.Errorf("HTTPStatus = %d，期望 %d", sourceErr.HTTPStatus, status)
			}
		})
	}
}

// 没有导演时 JavBus 写占位横线，那不是一个人名，不能收进 Directors。
func TestWatchlistMetadataJavBusSkipsPlaceholderDirector(t *testing.T) {
	page := `<html><body><h3>ABC-123 タイトル</h3><div class="col-md-3 info">
<p><span class="header">識別碼:</span> <b>ABC-123</b></p>
<p><span class="header">導演:</span> ----</p>
</div></body></html>`
	server, _, _ := newJavBusStub(t, htmlHandler(t, http.StatusOK, page))
	source := newJavBusSourceFor(t, server.URL, WatchlistMetadataConfig{})

	detail, err := source.Detail(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
	if err != nil {
		t.Fatalf("取详情失败: %v", err)
	}
	if len(detail.Directors) != 0 {
		t.Errorf("Directors = %v，期望空（占位横线不是人名）", detail.Directors)
	}
	// 可选字段缺失不该让整条结果作废。
	if detail.SourceItemID != "ABC-123" {
		t.Errorf("SourceItemID = %q，期望仍能取到", detail.SourceItemID)
	}
}

// 网络与代理失败照常分类，且都不得被当成「源没有收录」。
func TestWatchlistMetadataJavBusTransportFailures(t *testing.T) {
	t.Run("连接被拒", func(t *testing.T) {
		source := newJavBusSourceFor(t, "http://"+closedLoopbackAddr(t), WatchlistMetadataConfig{})
		_, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
		requireJavBusFailure(t, err, WatchlistMetadataFailureNetworkUnreachable)
	})

	t.Run("代理不通", func(t *testing.T) {
		server, hits, _ := newJavBusStub(t, htmlHandler(t, http.StatusOK, javbusDetailPage))
		config := WatchlistMetadataConfig{ProxyURL: "http://" + closedLoopbackAddr(t)}
		source := newJavBusSourceFor(t, server.URL, config)

		_, err := source.Search(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
		requireJavBusFailure(t, err, WatchlistMetadataFailureProxyUnreachable)
		if hits.Load() != 0 {
			t.Errorf("走坏代理时桩替身收到 %d 次请求，期望 0", hits.Load())
		}
	})
}

// 适配器只接 av，且类型不对时不发请求。
func TestWatchlistMetadataJavBusRejectsOtherKinds(t *testing.T) {
	server, hits, _ := newJavBusStub(t, htmlHandler(t, http.StatusOK, javbusDetailPage))
	source := newJavBusSourceFor(t, server.URL, WatchlistMetadataConfig{})

	for _, kind := range []WatchlistMetadataKind{
		WatchlistMetadataKindMovie,
		WatchlistMetadataKindTV,
		WatchlistMetadataKindShow,
		WatchlistMetadataKindAnime,
	} {
		if _, err := source.Search(context.Background(), kind, javbusTestCode); !errors.Is(err, ErrWatchlistMetadataKindUnsupported) {
			t.Errorf("Search(%s) 错误 = %v，期望 ErrWatchlistMetadataKindUnsupported", kind, err)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("类型不对时桩替身收到 %d 次请求，期望 0", hits.Load())
	}
}

// ---------------------------------------------------------------------------
// 四、两个真适配器串成的链
// ---------------------------------------------------------------------------

// 端到端跑一遍 av 链：FANZA 说没有 → JavBus 接手并给出结果，
// SourceName 如实写成 javbus。这是 D-WM07 想要的全部行为。
func TestWatchlistMetadataAVChainFallsBackFromFANZAToJavBus(t *testing.T) {
	fanzaStub := newFANZAStub(t, jsonHandler(t, http.StatusOK, `{"result":{"result_count":0,"items":[]}}`))
	javbusServer, javbusHits, _ := newJavBusStub(t, htmlHandler(t, http.StatusOK, javbusDetailPage))

	chain := []WatchlistMetadataSource{
		newFANZASourceFor(t, fanzaTestAPIID, fanzaTestAffiliateID, fanzaStub.server.URL, WatchlistMetadataConfig{}),
		newJavBusSourceFor(t, javbusServer.URL, WatchlistMetadataConfig{}),
	}

	candidates, err := SearchWatchlistMetadataChain(context.Background(), chain, WatchlistMetadataKindAV, javbusTestCode)
	if err != nil {
		t.Fatalf("走链失败: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("候选数 = %d，期望 1", len(candidates))
	}
	if candidates[0].SourceName != WatchlistMetadataSourceJavBus {
		t.Errorf("SourceName = %q，期望产出结果的源 %q", candidates[0].SourceName, WatchlistMetadataSourceJavBus)
	}
	if candidates[0].SourceItemID != "ABC-123" {
		t.Errorf("SourceItemID = %q，期望保留 JavBus 的源条目 ID", candidates[0].SourceItemID)
	}
	if fanzaStub.hits.Load() != 1 || javbusHits.Load() != 1 {
		t.Errorf("两跳各应打一次请求，实际 FANZA %d / JavBus %d", fanzaStub.hits.Load(), javbusHits.Load())
	}
}

// FANZA 缺凭证时**不走兜底**，JavBus 一个请求都不该收到。
// 这是把「配置问题」与「查无此片」分开的最直接证据。
func TestWatchlistMetadataAVChainDoesNotFallBackOnMissingFANZACredentials(t *testing.T) {
	fanzaStub := newFANZAStub(t, jsonHandler(t, http.StatusOK, fanzaItemListBody))
	javbusServer, javbusHits, _ := newJavBusStub(t, htmlHandler(t, http.StatusOK, javbusDetailPage))

	chain := []WatchlistMetadataSource{
		newFANZASourceFor(t, "", "", fanzaStub.server.URL, WatchlistMetadataConfig{}),
		newJavBusSourceFor(t, javbusServer.URL, WatchlistMetadataConfig{}),
	}

	_, err := SearchWatchlistMetadataChain(context.Background(), chain, WatchlistMetadataKindAV, javbusTestCode)
	requireFANZAFailure(t, err, WatchlistMetadataFailureCredentialMissing)
	if fanzaStub.hits.Load() != 0 {
		t.Errorf("缺凭证时 FANZA 桩替身收到 %d 次请求，期望 0", fanzaStub.hits.Load())
	}
	if javbusHits.Load() != 0 {
		t.Errorf("缺凭证不该触发兜底，JavBus 桩替身却收到 %d 次请求", javbusHits.Load())
	}
}

// 两跳都说没有时，最终就是 not_found。
func TestWatchlistMetadataAVChainEndsNotFoundWhenBothMiss(t *testing.T) {
	fanzaStub := newFANZAStub(t, jsonHandler(t, http.StatusOK, `{"result":{"result_count":0,"items":[]}}`))
	javbusServer, javbusHits, _ := newJavBusStub(t, htmlHandler(t, http.StatusNotFound, `<html><body>404</body></html>`))

	chain := []WatchlistMetadataSource{
		newFANZASourceFor(t, fanzaTestAPIID, fanzaTestAffiliateID, fanzaStub.server.URL, WatchlistMetadataConfig{}),
		newJavBusSourceFor(t, javbusServer.URL, WatchlistMetadataConfig{}),
	}

	_, err := SearchWatchlistMetadataChain(context.Background(), chain, WatchlistMetadataKindAV, javbusTestCode)
	requireJavBusFailure(t, err, WatchlistMetadataFailureNotFound)
	if javbusHits.Load() != 1 {
		t.Errorf("JavBus 应被问一次，实际 %d", javbusHits.Load())
	}
}
