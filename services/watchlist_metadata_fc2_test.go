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

// FC2 官方站适配器的测试。页面样本按 2026-09-14 真实商品页的结构裁剪：
// og:title 带「FC2-PPV-<商品号> 片名」，简介在 meta description，
// 販売日写在一段带标记的文本里，标签是 /search/?tag= 链接。

const fc2TestArticleID = "4976527"

const fc2ArticlePage = `<html><head>
<meta property="og:title" content="FC2-PPV-4976527 【撮影バレ】過去最大級の作品">
<meta name="description" content="FC2-PPV-4976527 痴女、見せつけ、聞いただけでゾクゾクしてくるワードですよね">
<meta property="og:image" content="https://storage201000.contents.fc2.com/file/403/40278923/1789278733.81.jpg">
</head><body>
<li>PC iOS Android <p>販売日</p> : <p>2026/09/14</p> 商品ID : FC2 PPV 4976527</li>
<a href="/search/?tag=%E3%81%8A" class="tag tagTag">おっぱい</a>
<a href="/search/?tag=%E3%82%AE" class="tag tagTag">ギャル</a>
<a href="/search/?tag=%E3%81%8A" class="tag tagTag">おっぱい</a>
</body></html>`

func newFC2Stub(t *testing.T, page string, status int) (*httptest.Server, *sync.Map) {
	t.Helper()
	seen := &sync.Map{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store("path", r.URL.Path)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(server.Close)
	return server, seen
}

func newFC2SourceFor(t *testing.T, baseURL string) *FC2WatchlistMetadataSource {
	t.Helper()
	client, err := NewWatchlistMetadataHTTPClient(WatchlistMetadataConfig{}, 5*time.Second)
	if err != nil {
		t.Fatalf("构造出网客户端失败: %v", err)
	}
	source := NewFC2WatchlistMetadataSource(client)
	source.baseURL = baseURL
	return source
}

// 三种常见写法都要能落到同一个商品号上。
func TestWatchlistMetadataFC2AcceptsCommonCodeShapes(t *testing.T) {
	for _, query := range []string{
		"FC2-PPV-4976527", "fc2ppv-4976527", "FC2PPV4976527",
		"FC2-4976527", "4976527", "  fc2-ppv-4976527  ",
	} {
		server, seen := newFC2Stub(t, fc2ArticlePage, http.StatusOK)
		source := newFC2SourceFor(t, server.URL)

		candidates, err := source.Search(context.Background(), WatchlistMetadataKindAV, query)
		if err != nil {
			t.Errorf("[%s] 搜索失败: %v", query, err)
			continue
		}
		if candidates[0].SourceItemID != fc2TestArticleID {
			t.Errorf("[%s] SourceItemID = %q，期望 %q", query, candidates[0].SourceItemID, fc2TestArticleID)
		}
		if path, _ := seen.Load("path"); path != "/article/"+fc2TestArticleID+"/" {
			t.Errorf("[%s] 请求路径 = %v", query, path)
		}
	}
}

// **承重**：不是 FC2 形态的番号一律 not_found，而且**不发请求**。
//
// 这是「按番号形态分流」的落点。判定放在适配器自己身上，上层不再开第二张路由表：
// 片商番号交给 JavBus / jav321，它们那边也各自 404 收场。
func TestWatchlistMetadataFC2SkipsStudioCodesWithoutRequest(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	t.Cleanup(server.Close)
	source := newFC2SourceFor(t, server.URL)

	for _, query := range []string{"SSIS-001", "259LUXU-1234", "ABC-123", "沙丘", ""} {
		_, err := source.Search(context.Background(), WatchlistMetadataKindAV, query)
		if got := WatchlistMetadataFailureOf(err); got != WatchlistMetadataFailureNotFound {
			t.Errorf("[%s] 失败分类 = %q，期望 not_found", query, got)
		}
	}
	if hits != 0 {
		t.Errorf("非 FC2 番号不该发请求，实际发了 %d 次", hits)
	}
}

// 商品页各字段各就各位。
func TestWatchlistMetadataFC2DetailParsesArticle(t *testing.T) {
	server, _ := newFC2Stub(t, fc2ArticlePage, http.StatusOK)
	source := newFC2SourceFor(t, server.URL)

	detail, err := source.Detail(context.Background(), WatchlistMetadataKindAV, fc2TestArticleID)
	if err != nil {
		t.Fatalf("取详情失败: %v", err)
	}
	// 标题剥掉 og:title 前面那截番号——它本来就是用户手输的内容。
	if detail.Title != "【撮影バレ】過去最大級の作品" {
		t.Errorf("Title = %q，期望剥掉番号前缀", detail.Title)
	}
	// 简介同样带番号前缀，一并剥掉。
	if strings.HasPrefix(detail.Overview, "FC2") {
		t.Errorf("Overview 仍带番号前缀: %q", detail.Overview)
	}
	if !strings.Contains(detail.Overview, "痴女") {
		t.Errorf("Overview = %q，期望取到简介正文", detail.Overview)
	}
	if detail.Year != 2026 {
		t.Errorf("Year = %d，期望从販売日取到 2026", detail.Year)
	}
	// 标签去重保序。
	if len(detail.Genres) != 2 || detail.Genres[0] != "おっぱい" || detail.Genres[1] != "ギャル" {
		t.Errorf("Genres = %v，期望去重后的 [おっぱい ギャル]", detail.Genres)
	}
	if detail.PosterURL == "" {
		t.Error("PosterURL 为空，期望取到 og:image")
	}
	if detail.SourceName != WatchlistMetadataSourceFC2 {
		t.Errorf("SourceName = %q", detail.SourceName)
	}
}

// 404 是源站唯一一处「明确说没有这个商品号」。
func TestWatchlistMetadataFC2NotFoundOnly404(t *testing.T) {
	server, _ := newFC2Stub(t, "", http.StatusNotFound)
	source := newFC2SourceFor(t, server.URL)

	_, err := source.Detail(context.Background(), WatchlistMetadataKindAV, fc2TestArticleID)
	if got := WatchlistMetadataFailureOf(err); got != WatchlistMetadataFailureNotFound {
		t.Errorf("失败分类 = %q，期望 not_found", got)
	}
}

// **承重**：页面结构对不上必须落 source_error，绝不落 not_found。
func TestWatchlistMetadataFC2BrokenLayoutIsSourceError(t *testing.T) {
	server, _ := newFC2Stub(t, `<html><body><p>拦截页，没有 og:title</p></body></html>`, http.StatusOK)
	source := newFC2SourceFor(t, server.URL)

	_, err := source.Detail(context.Background(), WatchlistMetadataKindAV, fc2TestArticleID)
	if got := WatchlistMetadataFailureOf(err); got != WatchlistMetadataFailureSourceError {
		t.Errorf("失败分类 = %q，期望 source_error", got)
	}
}

// 403 不认 credential_invalid——本站不要凭证，403 只可能是拦截。
func TestWatchlistMetadataFC2ForbiddenIsNotCredentialFailure(t *testing.T) {
	server, _ := newFC2Stub(t, "", http.StatusForbidden)
	source := newFC2SourceFor(t, server.URL)

	_, err := source.Detail(context.Background(), WatchlistMetadataKindAV, fc2TestArticleID)
	if got := WatchlistMetadataFailureOf(err); got != WatchlistMetadataFailureSourceError {
		t.Errorf("失败分类 = %q，期望 source_error", got)
	}
}

// 只接 av，传错类型不带失败分类码——那是调用方的问题，不该触发任何兜底。
func TestWatchlistMetadataFC2RejectsOtherKinds(t *testing.T) {
	source := newFC2SourceFor(t, "http://127.0.0.1:1")
	_, err := source.Search(context.Background(), WatchlistMetadataKindMovie, "4976527")
	if err == nil {
		t.Fatal("传 movie 应当报错")
	}
	if got := WatchlistMetadataFailureOf(err); got != "" {
		t.Errorf("类型错误不该带失败分类码，实际 %q", got)
	}
}

var _ WatchlistMetadataSource = (*FC2WatchlistMetadataSource)(nil)
