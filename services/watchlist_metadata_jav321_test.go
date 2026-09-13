package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// jav321 适配器的测试。页面样本按 2026-09-13 真实页面的结构裁剪：
// 标题在 <h3> 里且尾部带 <small>番号 演员</small>，字段写成 <b>标签</b>: 值，
// 简介是 col-md-12 下的一段裸文本，封面在 pics.dmm.co.jp。

const jav321TestCode = "ABC-123"
const jav321TestItemID = "abc00123"

const jav321DetailPage = `<html><body>
<div class="panel-heading"><h3>禁欲の果てに。 葵つかさ <small>abc-123 葵つかさ</small></h3></div>
<div class="panel-body">
<b>出演者</b>: <a href="/star/x">葵つかさ</a> &nbsp; <a href="/star/y">乙白さやか</a> &nbsp;
<br><b>メーカー</b>: エスワン<br>
<b>品番</b>: abc-123<br>
<b>配信開始日</b>: 2021-02-19<br>
<b>収録時間</b>: 147 minutes<br>
<b>平均評価</b>: 4.5<br>
<img src="http://pics.dmm.co.jp/digital/video/abc00123/abc00123ps.jpg">
<a href="http://pics.dmm.co.jp//digital/video/abc00123/abc00123pl.jpg">大图</a>
</div>
<div class="row"><div class="col-md-12">豪華共演エモドラマ作！これは簡介の本文である。</div></div>
</body></html>`

// newJav321Stub 建 jav321 的桩替身：POST /search 会 302 到 /video/<编码>。
func newJav321Stub(t *testing.T, detailPage string) (*httptest.Server, *sync.Map) {
	t.Helper()
	seen := &sync.Map{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store("path", r.URL.Path)
		if r.URL.Path == jav321SearchPath {
			_ = r.ParseForm()
			seen.Store("sn", r.PostFormValue(jav321SearchField))
			http.Redirect(w, r, jav321DetailPathPrefix+jav321TestItemID, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(detailPage))
	}))
	t.Cleanup(server.Close)
	return server, seen
}

func newJav321SourceFor(t *testing.T, baseURL string) *Jav321WatchlistMetadataSource {
	t.Helper()
	client, err := NewWatchlistMetadataHTTPClient(WatchlistMetadataConfig{}, 5*time.Second)
	if err != nil {
		t.Fatalf("构造出网客户端失败: %v", err)
	}
	source := NewJav321WatchlistMetadataSource(client)
	source.baseURL = baseURL
	return source
}

func requireJav321Failure(t *testing.T, err error, want WatchlistMetadataFailure) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望失败分类 %q，却没有错误", want)
	}
	if got := WatchlistMetadataFailureOf(err); got != want {
		t.Fatalf("失败分类 = %q，期望 %q（%v）", got, want, err)
	}
}

// 搜索把标准番号交给源站，由它换算成自己的编码并重定向到详情页。
//
// 这是本适配器的要害：源站的 /video/ 只认补零编码（abc00123），直接拿
// ABC-123 去打会 404。换算必须由源站自己做——我们这边凭猜补零，会在用户
// 看不见的地方查错片。
func TestWatchlistMetadataJav321SearchDelegatesCodeMapping(t *testing.T) {
	server, seen := newJav321Stub(t, jav321DetailPage)
	source := newJav321SourceFor(t, server.URL)

	candidates, err := source.Search(context.Background(), WatchlistMetadataKindAV, "abc_123")
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	// 递给源站的是归一化后的标准番号，不是用户的原始输入。
	if sn, _ := seen.Load("sn"); sn != jav321TestCode {
		t.Errorf("递给源站的番号 = %v，期望归一化后的 %q", sn, jav321TestCode)
	}
	if len(candidates) != 1 {
		t.Fatalf("候选数 = %d，期望 1（番号是主键）", len(candidates))
	}
	// SourceItemID 是**源站的编码**，不是标准番号——Detail 要靠它回到源上。
	if candidates[0].SourceItemID != jav321TestItemID {
		t.Errorf("SourceItemID = %q，期望源站编码 %q", candidates[0].SourceItemID, jav321TestItemID)
	}
	if candidates[0].SourceName != WatchlistMetadataSourceJav321 {
		t.Errorf("SourceName = %q", candidates[0].SourceName)
	}
}

// 详情页各字段各就各位。
func TestWatchlistMetadataJav321DetailParsesPage(t *testing.T) {
	server, seen := newJav321Stub(t, jav321DetailPage)
	source := newJav321SourceFor(t, server.URL)

	detail, err := source.Detail(context.Background(), WatchlistMetadataKindAV, jav321TestItemID)
	if err != nil {
		t.Fatalf("取详情失败: %v", err)
	}
	if path, _ := seen.Load("path"); path != jav321DetailPathPrefix+jav321TestItemID {
		t.Errorf("请求路径 = %v", path)
	}
	// 标题剥掉 <small> 那截（它是「番号 + 演员」的重复）。
	if detail.Title != "禁欲の果てに。 葵つかさ" {
		t.Errorf("Title = %q", detail.Title)
	}
	if detail.Year != 2021 {
		t.Errorf("Year = %d，期望 2021", detail.Year)
	}
	if detail.Rating != 4.5 {
		t.Errorf("Rating = %v，期望 4.5", detail.Rating)
	}
	// 演员名之间用 &nbsp; 分隔，必须解码后再切，否则实体会粘进名字里。
	if len(detail.Cast) != 2 || detail.Cast[0] != "葵つかさ" || detail.Cast[1] != "乙白さやか" {
		t.Errorf("Cast = %v，期望 [葵つかさ 乙白さやか]", detail.Cast)
	}
	// 简介是引入这个源的主要理由——JavBus 给不了它。
	if !strings.Contains(detail.Overview, "簡介の本文") {
		t.Errorf("Overview = %q，期望取到简介正文", detail.Overview)
	}
	// 封面取大图（pl）而不是缩略图（ps），并规整掉源站写出来的双斜杠。
	if detail.PosterURL != "http://pics.dmm.co.jp/digital/video/abc00123/abc00123pl.jpg" {
		t.Errorf("PosterURL = %q，期望大图且路径规整", detail.PosterURL)
	}
}

// 404 是源站唯一一处「明确说没有这个番号」。
func TestWatchlistMetadataJav321NotFoundOnly404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	source := newJav321SourceFor(t, server.URL)

	_, err := source.Detail(context.Background(), WatchlistMetadataKindAV, jav321TestItemID)
	requireJav321Failure(t, err, WatchlistMetadataFailureNotFound)
}

// **承重**：页面结构对不上必须落 source_error，绝不落 not_found。
//
// 源站改版会让所有选择器同时失效。那时报「这片不存在」会把用户支去核对番号，
// 而真正该做的是修适配器。
func TestWatchlistMetadataJav321BrokenLayoutIsSourceError(t *testing.T) {
	server, _ := newJav321Stub(t, `<html><body><p>反爬拦截页，什么结构都没有</p></body></html>`)
	source := newJav321SourceFor(t, server.URL)

	_, err := source.Detail(context.Background(), WatchlistMetadataKindAV, jav321TestItemID)
	requireJav321Failure(t, err, WatchlistMetadataFailureSourceError)
}

// 搜索没落到详情页，说明源站没收录这个番号。
func TestWatchlistMetadataJav321SearchMissingRedirectIsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 不重定向，停在搜索页上。
		_, _ = w.Write([]byte(`<html><body>没有结果</body></html>`))
	}))
	t.Cleanup(server.Close)
	source := newJav321SourceFor(t, server.URL)

	_, err := source.Search(context.Background(), WatchlistMetadataKindAV, jav321TestCode)
	requireJav321Failure(t, err, WatchlistMetadataFailureNotFound)
}

// 非 403 之外的非 2xx 归 source_error；403 也不能归 credential_invalid——
// 这个源不要凭证，403 只可能是反爬。
func TestWatchlistMetadataJav321ForbiddenIsNotCredentialFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	source := newJav321SourceFor(t, server.URL)

	_, err := source.Detail(context.Background(), WatchlistMetadataKindAV, jav321TestItemID)
	requireJav321Failure(t, err, WatchlistMetadataFailureSourceError)
}

// 只接 av，传错类型当场说清楚，且**不带失败分类码**——那是调用方的问题，
// 换个源问一遍没有意义，不该触发任何兜底。
func TestWatchlistMetadataJav321RejectsOtherKinds(t *testing.T) {
	source := newJav321SourceFor(t, "http://127.0.0.1:1")
	_, err := source.Search(context.Background(), WatchlistMetadataKindMovie, "Dune")
	if err == nil {
		t.Fatal("传 movie 应当报错")
	}
	if got := WatchlistMetadataFailureOf(err); got != "" {
		t.Errorf("类型错误不该带失败分类码，实际 %q", got)
	}
}

// 空番号不发请求。
func TestWatchlistMetadataJav321EmptyCodeSendsNoRequest(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	t.Cleanup(server.Close)
	source := newJav321SourceFor(t, server.URL)

	_, err := source.Search(context.Background(), WatchlistMetadataKindAV, "   ")
	requireJav321Failure(t, err, WatchlistMetadataFailureNotFound)
	if hits != 0 {
		t.Errorf("空番号不该发请求，实际发了 %d 次", hits)
	}
}

var _ WatchlistMetadataSource = (*Jav321WatchlistMetadataSource)(nil)
