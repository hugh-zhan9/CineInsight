package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 本文件覆盖 P-005 的三样东西：路由层的有序兜底（walkWatchlistMetadataChain）、
// JavBus 适配器。
//
// 所有出网一律打 httptest 桩替身，**不碰 api.dmm.com 也不碰 www.javbus.com**。

const (
	// 走链测试里「第一跳 / 第二跳」的桩源名。用中性名字而不是某个真源名：
	// 这些用例验的是 walkWatchlistMetadataChain 的规则本身，与具体是谁无关。
	chainTestFirstSource = "first-source"
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
				first := failingStubSource(chainTestFirstSource, tc.failure)
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
				if sourceErr.Source != chainTestFirstSource {
					t.Errorf("错误来源 = %q，期望 %q", sourceErr.Source, chainTestFirstSource)
				}
				if candidates != nil {
					t.Errorf("失败时不应返回候选，got %v", candidates)
				}
			})

			t.Run("Detail", func(t *testing.T) {
				first := failingStubSource(chainTestFirstSource, tc.failure)
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
	first := failingStubSource(chainTestFirstSource, WatchlistMetadataFailureNotFound)
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
// WatchlistEntry.SourceName，是之后按源重查详情的唯一依据；写成第一跳而结果
// 其实来自 JavBus，重查必然查空。
func TestWatchlistMetadataChainReportsProducingSource(t *testing.T) {
	first := failingStubSource(chainTestFirstSource, WatchlistMetadataFailureNotFound)
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
	first := succeedingStubSource(chainTestFirstSource)
	second := succeedingStubSource(WatchlistMetadataSourceJavBus)
	chain := []WatchlistMetadataSource{first, second}

	candidates, err := SearchWatchlistMetadataChain(context.Background(), chain, WatchlistMetadataKindAV, javbusTestCode)
	if err != nil {
		t.Fatalf("走链失败: %v", err)
	}
	if candidates[0].SourceName != chainTestFirstSource {
		t.Errorf("SourceName = %q，期望首选源 %q", candidates[0].SourceName, chainTestFirstSource)
	}
	if second.searchCalls.Load() != 0 {
		t.Errorf("首选源已经成功，兜底源却被问了 %d 次", second.searchCalls.Load())
	}
}

// 没有失败分类码的错误同样不兜底：类型传错是调用方的问题，换个源问一遍没有意义。
func TestWatchlistMetadataChainDoesNotFallBackOnUnclassifiedError(t *testing.T) {
	first := &stubWatchlistMetadataSource{
		name: chainTestFirstSource,
		err:  fmt.Errorf("%w：该源不覆盖 %q", ErrWatchlistMetadataKindUnsupported, "movie"),
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
// 二、（FANZA 适配器已退场，本节随之移除）
// ---------------------------------------------------------------------------

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
		headers.Store("Cookie", r.Header.Get("Cookie"))
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

// 年龄门回归：源站对不带 dv=1 的请求回 302 到验证页，而 Go 会跟过去拿回一个
// 解析不了的 200。这条测试复现那个真实行为，钉住「必须带 cookie」。
//
// 2026-09-13 的真实请求确认：无 cookie 时 GET /ABC-123 回 302，
// Location 为 /doc/driver-verify，验证页里没有 <h3> 也没有 bigImage。
// 修复前适配器每一次查询都因此落 source_error。
func TestWatchlistMetadataJavBusPassesAgeGate(t *testing.T) {
	const verifyPath = "/doc/driver-verify"
	// 桩模拟源站：没有 dv=1 就跳验证页，有就给详情页。
	server, _, headers := newJavBusStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == verifyPath {
			// 验证页：200，但没有任何适配器要找的结构。
			htmlHandler(t, http.StatusOK, `<html><body><p>請確認您已年滿十八歲</p></body></html>`)(w, r)
			return
		}
		if !strings.Contains(r.Header.Get("Cookie"), javbusAgeGateCookie) {
			http.Redirect(w, r, verifyPath, http.StatusFound)
			return
		}
		htmlHandler(t, http.StatusOK, javbusDetailPage)(w, r)
	})
	source := newJavBusSourceFor(t, server.URL, WatchlistMetadataConfig{})

	detail, err := source.Detail(context.Background(), WatchlistMetadataKindAV, javbusTestCode)
	if err != nil {
		t.Fatalf("带上年龄门 cookie 后仍取详情失败: %v", err)
	}
	if detail.SourceItemID != "ABC-123" {
		t.Errorf("SourceItemID = %q，期望 ABC-123", detail.SourceItemID)
	}
	// 没被重定向走：最后一次请求的路径仍是番号页，不是验证页。
	if path, _ := headers.Load("path"); path != "/"+javbusTestCode {
		t.Errorf("最终请求路径 = %v，期望 /%s（被重定向到验证页说明 cookie 没送出去）", path, javbusTestCode)
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
	// 年龄门 cookie 缺了就会被 302 到验证页，整个适配器不工作。
	if cookie, _ := headers.Load("Cookie"); cookie != javbusAgeGateCookie {
		t.Errorf("Cookie = %v，期望 %q", cookie, javbusAgeGateCookie)
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

// 走链在 av 上已不再使用（av 改走聚合），但手动重选候选仍走它
// （ListCandidates / ApplyCandidate）。这条用两个真适配器验它的兜底与终态。
func TestWatchlistMetadataAVChainEndsNotFoundWhenBothMiss(t *testing.T) {
	// 两个源都必须是**真 404**：200 加空页面按约定是 source_error（结构对不上），
	// 那样就测不到「链走完仍是 not_found」这个终态了。
	jav321Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(jav321Server.Close)
	javbusServer, javbusHits, _ := newJavBusStub(t, htmlHandler(t, http.StatusNotFound, `<html><body>404</body></html>`))

	chain := []WatchlistMetadataSource{
		newJavBusSourceFor(t, javbusServer.URL, WatchlistMetadataConfig{}),
		newJav321SourceFor(t, jav321Server.URL),
	}

	_, err := SearchWatchlistMetadataChain(context.Background(), chain, WatchlistMetadataKindAV, javbusTestCode)
	if got := WatchlistMetadataFailureOf(err); got != WatchlistMetadataFailureNotFound {
		t.Errorf("失败分类 = %q，期望 not_found", got)
	}
	if javbusHits.Load() != 1 {
		t.Errorf("JavBus 应被问一次，实际 %d", javbusHits.Load())
	}
}
