package services

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"video-master/models"
)

// 列表响应按 2026-09-21 实测形态裁剪（概要设计 §2）：total 是恒为 500 的占位值、
// 会混入 type 不是 movie 的条目、year 也可能与请求年份不符。
const movieChartListFixture = `{"total":500,"start":0,"count":20,"items":[
	{"id":"36208465","title":"奥德赛","type":"movie","year":"2026","card_subtitle":"2026 / 美国 加拿大 / 剧情 冒险 / 克里斯托弗·诺兰","rating":{"value":8.1,"count":1024},"pic":{"large":"https://img2.doubanio.com/view/photo/l/public/p1.jpg","normal":"https://img2.doubanio.com/view/photo/m/public/p1.jpg"}},
	{"id":"37353574","title":"天桥风云之胜券在握","type":"tv","year":"2026"},
	{"id":"35651341","title":"去年的片","type":"movie","year":"2025"},
	{"id":"36999001","title":"只有小图的片","type":"movie","year":"2026","card_subtitle":"2026 / 中国大陆 / 剧情","pic":{"large":"","normal":"https://img9.doubanio.com/view/photo/m/public/p2.jpg"}},
	{"id":"36888002","title":"没给年份的片","type":"movie","card_subtitle":"中国大陆 / 剧情"}
]}`

// 详情响应：引进片《奥德赛》，pubdate 里同时有美国与中国大陆两项。
const movieChartDetailFixture = `{"id":"36208465","title":"奥德赛","original_title":"The Odyssey","type":"movie","is_tv":false,"is_released":false,
	"intro":"一段横跨爱琴海的归乡之旅。","pubdate":["2026-07-17(美国)","2026-08-14(中国大陆)"],
	"countries":["美国","加拿大"],"genres":["剧情","冒险"],"rating":{"value":8.1,"count":1024},
	"directors":[{"name":"克里斯托弗·诺兰"}],
	"actors":[{"name":"马特·达蒙"},{"name":"汤姆·赫兰德"},{"name":"安妮·海瑟薇"},{"name":"塞伦·希德"},{"name":"罗伯特·帕丁森"},{"name":"查理兹·塞隆"},{"name":"露皮塔·尼永奥"},{"name":"约翰·伯恩瑟尔"},{"name":"第九位不该出现"}]}`

func newMovieChartSourceForTest(t *testing.T, listBase, detailBase string, config WatchlistMetadataConfig) *DoubanMovieChartSource {
	t.Helper()
	client, err := NewWatchlistMetadataHTTPClient(config, 2*time.Second)
	if err != nil {
		t.Fatalf("构造出网客户端失败: %v", err)
	}
	source := NewDoubanMovieChartSource(client)
	source.listBaseURL = listBase
	source.detailBaseURL = detailBase
	return source
}

func movieChartStub(t *testing.T, handler http.HandlerFunc) *DoubanMovieChartSource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return newMovieChartSourceForTest(t, server.URL, server.URL, WatchlistMetadataConfig{})
}

func movieChartFixedResponse(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

// TC-01：参数拼装、固定 25 页的调用序列与列表字段映射。
func TestMovieChartDoubanListRequestShapeAndPageWalk(t *testing.T) {
	var starts []int
	source := movieChartStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rexxar/api/v2/movie/recommend" {
			t.Errorf("列表路径 = %s", r.URL.Path)
		}
		query := r.URL.Query()
		for key, want := range map[string]string{
			"refresh":             "0",
			"count":               "20",
			"uncollect":           "false",
			"playable":            "false",
			"selected_categories": "{}",
			"tags":                "2026",
			"sort":                MovieChartSortReleaseDate,
		} {
			if got := query.Get(key); got != want {
				t.Errorf("参数 %s = %q，期望 %q", key, got, want)
			}
		}
		// tags 只能带年份：地区是产地维度，带上会滤掉全部引进片（D-MC02）。
		if strings.ContainsAny(query.Get("tags"), ",，") {
			t.Errorf("tags 带了年份以外的维度: %q", query.Get("tags"))
		}
		if got := r.Header.Get("Referer"); got != "https://movie.douban.com/explore" {
			t.Errorf("列表 Referer = %q", got)
		}
		if r.Header.Get("User-Agent") == "" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("请求头缺失: UA=%q Accept=%q", r.Header.Get("User-Agent"), r.Header.Get("Accept"))
		}
		start, err := strconv.Atoi(query.Get("start"))
		if err != nil {
			t.Errorf("start 不是整数: %q", query.Get("start"))
		}
		starts = append(starts, start)
		io.WriteString(w, movieChartListFixture)
	})

	var pages []MovieChartListPage
	if err := source.ListYear(context.Background(), 2026, MovieChartSortReleaseDate, func(page MovieChartListPage) error {
		pages = append(pages, page)
		return nil
	}); err != nil {
		t.Fatalf("翻页失败: %v", err)
	}

	// 固定 25 页、步长 20，不由 total（恒为 500 的占位值）推导。
	if len(starts) != MovieChartPagesPerSort || len(pages) != MovieChartPagesPerSort {
		t.Fatalf("请求 %d 页、回调 %d 次，期望各 %d", len(starts), len(pages), MovieChartPagesPerSort)
	}
	for index, start := range starts {
		if want := index * MovieChartListPageSize; start != want {
			t.Fatalf("第 %d 页 start=%d，期望 %d", index, start, want)
		}
	}
	if last := starts[len(starts)-1]; last != 480 {
		t.Fatalf("最后一页 start=%d，期望 480", last)
	}

	first := pages[0]
	if first.Sort != MovieChartSortReleaseDate || first.Start != 0 {
		t.Fatalf("页码信息未回传: %+v", first)
	}
	// 没给年份的那条照收（请求已用 tags 约束年份），只单独计数；把它当跨年丢掉
	// 等于凭空少一部片，还把原因记错。
	if len(first.Items) != 3 || first.SkippedNonMovie != 1 || first.SkippedOtherYear != 1 || first.KeptWithoutYear != 1 {
		t.Fatalf("过滤与计数不符: items=%d 非电影=%d 跨年=%d 无年份=%d",
			len(first.Items), first.SkippedNonMovie, first.SkippedOtherYear, first.KeptWithoutYear)
	}
	if first.Items[2].DoubanID != "36888002" {
		t.Fatalf("没给年份的条目被丢掉了: %+v", first.Items)
	}
	item := first.Items[0]
	if item.DoubanID != "36208465" || item.Title != "奥德赛" ||
		item.CardSubtitle != "2026 / 美国 加拿大 / 剧情 冒险 / 克里斯托弗·诺兰" ||
		item.Rating != 8.1 || item.RatingCount != 1024 ||
		item.PosterURL != "https://img2.doubanio.com/view/photo/l/public/p1.jpg" {
		t.Fatalf("列表映射不完整: %+v", item)
	}
	// pic.large 为空时退到 pic.normal，取第一个非空。
	if got := first.Items[1].PosterURL; got != "https://img9.doubanio.com/view/photo/m/public/p2.jpg" {
		t.Fatalf("海报地址 = %q", got)
	}
}

// TC-08：HTTP 200 + 空 items 既不是「翻到底了」也不是 not_found，必须继续翻页。
func TestMovieChartDoubanEmptyPageDoesNotStopWalk(t *testing.T) {
	requested := 0
	source := movieChartStub(t, func(w http.ResponseWriter, r *http.Request) {
		requested++
		switch r.URL.Query().Get("start") {
		case "300":
			// 实测：密集请求时 start=300 返回 0 条，隔一会重试连续三次都是 20 条。
			io.WriteString(w, `{"total":500,"start":300,"count":20,"items":[]}`)
		case "400":
			// total 归零同样不能当终止条件——它本来就不是真实计数。
			io.WriteString(w, `{"total":0,"start":400,"count":20,"items":[]}`)
		default:
			io.WriteString(w, movieChartListFixture)
		}
	})

	pages, emptyPages, itemsAfterEmpty := 0, 0, 0
	err := source.ListYear(context.Background(), 2026, MovieChartSortComprehensive, func(page MovieChartListPage) error {
		pages++
		if len(page.Items) == 0 {
			emptyPages++
		}
		if page.Start > 300 {
			itemsAfterEmpty += len(page.Items)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("空页被当成了失败: %v", err)
	}
	if got := WatchlistMetadataFailureOf(err); got != "" {
		t.Fatalf("空页记下了失败码 %s", got)
	}
	if requested != MovieChartPagesPerSort || pages != MovieChartPagesPerSort {
		t.Fatalf("空页终止了翻页: 请求 %d 页、回调 %d 次", requested, pages)
	}
	if emptyPages != 2 {
		t.Fatalf("空页数 = %d，期望 2", emptyPages)
	}
	if itemsAfterEmpty == 0 {
		t.Fatal("空页之后没有再抓到任何条目")
	}
}

// 取消与落库失败都要就地中止，且**不得**被归成网络故障——取消不是失败（§7）。
func TestMovieChartDoubanListStopsOnCancelAndVisitorError(t *testing.T) {
	t.Run("取消", func(t *testing.T) {
		requested := 0
		source := movieChartStub(t, func(w http.ResponseWriter, r *http.Request) {
			requested++
			io.WriteString(w, movieChartListFixture)
		})
		ctx, cancel := context.WithCancel(context.Background())
		err := source.ListYear(ctx, 2026, MovieChartSortRating, func(page MovieChartListPage) error {
			cancel()
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后返回 %v", err)
		}
		if got := WatchlistMetadataFailureOf(err); got != "" {
			t.Fatalf("取消被记成失败码 %s", got)
		}
		if requested != 1 {
			t.Fatalf("取消后仍发了 %d 次请求", requested)
		}
	})

	t.Run("落库失败", func(t *testing.T) {
		requested := 0
		source := movieChartStub(t, func(w http.ResponseWriter, r *http.Request) {
			requested++
			io.WriteString(w, movieChartListFixture)
		})
		sentinel := errors.New("写库失败")
		err := source.ListYear(context.Background(), 2026, MovieChartSortRating, func(page MovieChartListPage) error {
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("visit 的错误未原样上抛: %v", err)
		}
		if requested != 1 {
			t.Fatalf("visit 报错后仍发了 %d 次请求", requested)
		}
	})
}

// TC-04：六类失败分类映射，逐条断言且互不合并。
func TestMovieChartDoubanFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		detail  bool
		handler http.HandlerFunc
		want    WatchlistMetadataFailure
	}{
		// 401 / 403 在这个源上是反爬拦截，不是凭证问题——本源没有凭证。
		{"列表 401", false, movieChartFixedResponse(401, ""), WatchlistMetadataFailureSourceError},
		{"详情 403", true, movieChartFixedResponse(403, ""), WatchlistMetadataFailureSourceError},
		{"详情 404 traversal_error", true, movieChartFixedResponse(404, `{"code":404,"msg":"traversal_error"}`), WatchlistMetadataFailureNotFound},
		{"详情 404 网页", true, movieChartFixedResponse(404, "<html>not found</html>"), WatchlistMetadataFailureSourceError},
		// 列表接口的 404 是接口变更或被拦，即便带着详情接口的业务体也不是「查无数据」。
		{"列表 404", false, movieChartFixedResponse(404, `{"code":404,"msg":"traversal_error"}`), WatchlistMetadataFailureSourceError},
		{"列表 500", false, movieChartFixedResponse(500, ""), WatchlistMetadataFailureSourceError},
		{"列表 429", false, movieChartFixedResponse(429, ""), WatchlistMetadataFailureSourceError},
		{"列表返回验证页", false, movieChartFixedResponse(200, "<html>需要验证</html>"), WatchlistMetadataFailureSourceError},
		{"列表条目缺 ID", false, movieChartFixedResponse(200, `{"items":[{"title":"奥德赛","type":"movie","year":"2026"}]}`), WatchlistMetadataFailureSourceError},
		{"列表条目 ID 非法", false, movieChartFixedResponse(200, `{"items":[{"id":"abc","title":"奥德赛","type":"movie","year":"2026"}]}`), WatchlistMetadataFailureSourceError},
		{"列表条目缺片名", false, movieChartFixedResponse(200, `{"items":[{"id":"36208465","type":"movie","year":"2026"}]}`), WatchlistMetadataFailureSourceError},
		{"详情 ID 不匹配", true, movieChartFixedResponse(200, strings.ReplaceAll(movieChartDetailFixture, `"id":"36208465"`, `"id":"9999999"`)), WatchlistMetadataFailureSourceError},
		{"详情缺片名", true, movieChartFixedResponse(200, `{"id":"36208465","type":"movie"}`), WatchlistMetadataFailureSourceError},
		{"详情不是电影数据", true, movieChartFixedResponse(200, strings.ReplaceAll(movieChartDetailFixture, `"type":"movie"`, `"type":"music"`)), WatchlistMetadataFailureSourceError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := movieChartStub(t, tc.handler)
			var err error
			if tc.detail {
				_, err = source.Detail(context.Background(), "36208465")
			} else {
				err = source.ListYear(context.Background(), 2026, MovieChartSortComprehensive, nil)
			}
			// 「不得合并」由上面这条等值断言承担：want 里除业务 404 外没有一行是
			// not_found 或 credential_*，任何合并都会在这里当场失败。再补一条
			// got != not_found 只是把等值断言重写一遍，永远不会成立，反而让读者
			// 以为多了一层覆盖。
			if got := WatchlistMetadataFailureOf(err); got != tc.want {
				t.Fatalf("分类 = %s，期望 %s（%v）", got, tc.want, err)
			}
		})
	}
}

// items 键缺失或为 null **不是**空页，必须判响应异常。
//
// 真实空页带着这个键、值是空数组（2026-09-21 实测 start=500：HTTP 200、3700 字节，
// "items": [] 与另外 11 个键齐全），所以两者分得开。不分开的话，一个完全不相干的
// 信封会被解析成功、25 页全空且没有失败码，服务据此写下 last_refreshed_at=now，
// 用户看到标着「数据更新于 刚刚」的空榜单——裁决禁止的静默空榜单，从「成功」这侧绕进来。
func TestMovieChartDoubanMissingItemsKeyIsSourceError(t *testing.T) {
	for name, body := range map[string]string{
		"限流信封":         `{"code":1002,"msg":"rate limited"}`,
		"items 为 null": `{"items":null}`,
		"空对象":          `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			requested := 0
			source := movieChartStub(t, func(w http.ResponseWriter, r *http.Request) {
				requested++
				io.WriteString(w, body)
			})
			err := source.ListYear(context.Background(), 2026, MovieChartSortComprehensive, func(page MovieChartListPage) error {
				t.Error("响应异常时不该回调出空页")
				return nil
			})
			if got := WatchlistMetadataFailureOf(err); got != WatchlistMetadataFailureSourceError {
				t.Fatalf("分类 = %s，期望 source_error（%v）", got, err)
			}
			// 第一页就该中止，不能继续把 25 页翻完然后报「成功」。
			if requested != 1 {
				t.Fatalf("响应异常后仍发了 %d 次请求", requested)
			}
		})
	}
}

// 超过大小上限的响应判响应异常，且这条判定必须由**大小**决定。
//
// 载荷用「空数组 + 大量尾随空白」：io.LimitReader 截断后它仍是合法 JSON，所以拿掉
// 大小上限这段判定后请求会变成一次成功的空页，测试随之失败。换成随便一串 "aaa…"
// 就绑不住——那种载荷截断后解析失败，会从 JSON 这条路掉进同一个 source_error。
func TestMovieChartDoubanOversizedResponseIsRejectedBySize(t *testing.T) {
	oversized := `{"items":[]}` + strings.Repeat(" ", doubanMovieChartMaxBytes)
	source := movieChartStub(t, movieChartFixedResponse(200, oversized))
	err := source.ListYear(context.Background(), 2026, MovieChartSortComprehensive, nil)
	if got := WatchlistMetadataFailureOf(err); got != WatchlistMetadataFailureSourceError {
		t.Fatalf("分类 = %s，期望 source_error（%v）", got, err)
	}
	var sourceErr *WatchlistMetadataSourceError
	if !errors.As(err, &sourceErr) || sourceErr.Detail != "豆瓣响应超过大小上限" {
		t.Fatalf("不是按大小上限拒绝的：%v", err)
	}
}

// 四个排序码是写进请求的字面量，必须逐字钉住。
//
// 其余测试都拿常量自己去比对 query，常量改错时它们会一起改错、全绿通过；而豆瓣
// 收到不认识的 sort 只会退回默认排序，不报错——D-MC03 的四排序覆盖就这么悄悄
// 少掉一份，没有任何信号。
func TestMovieChartSortCodesAreFixedLiterals(t *testing.T) {
	sorts := MovieChartSorts()
	want := []string{"T", "U", "R", "S"}
	if len(sorts) != len(want) {
		t.Fatalf("排序集合 = %v，期望 %v", sorts, want)
	}
	for index, value := range want {
		if sorts[index] != value {
			t.Fatalf("第 %d 个排序 = %q，期望 %q（顺序即抓取顺序）", index, sorts[index], value)
		}
	}
	for name, pair := range map[string][2]string{
		"综合":   {MovieChartSortComprehensive, "T"},
		"近期热度": {MovieChartSortRecentHeat, "U"},
		"首映时间": {MovieChartSortReleaseDate, "R"},
		"高分优先": {MovieChartSortRating, "S"},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("%s 排序码 = %q，期望 %q", name, pair[0], pair[1])
		}
	}
}

// 401 / 403 在这个无凭证的源上**不得**报成 credential_invalid：那会把用户指向一个
// 不存在的配置项，而豆瓣的 403 实际上是反爬拦截。同一主机上的
// watchlist_metadata_douban.go 也是这么判的，两个适配器不能给出两种结论。
func TestMovieChartDoubanAuthStatusesStaySourceError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			source := movieChartStub(t, movieChartFixedResponse(status, ""))
			for name, err := range map[string]error{
				"列表": source.ListYear(context.Background(), 2026, MovieChartSortComprehensive, nil),
				"详情": movieChartDetailError(source.Detail(context.Background(), "36208465")),
			} {
				// 等值断言即反向断言：source_error 与 credential_invalid /
				// credential_missing / not_found 互斥，判成其中任何一个都会在这里失败。
				if got := WatchlistMetadataFailureOf(err); got != WatchlistMetadataFailureSourceError {
					t.Fatalf("%s HTTP %d 分类 = %s，期望 source_error（不是凭证问题，本源没有凭证）（%v）", name, status, got, err)
				}
			}
		})
	}
}

// movieChartDetailError 只取 Detail 的错误，让上面的表能把两条路径写在一起。
func movieChartDetailError(_ *MovieChartDetail, err error) error { return err }

// 验证页跳转到别的主机＝source_error，不是「查无数据」。
func TestMovieChartDoubanCrossHostRedirectIsSourceError(t *testing.T) {
	challenge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"items":[]}`)
	}))
	t.Cleanup(challenge.Close)
	source := movieChartStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, challenge.URL+"/verify", http.StatusFound)
	})
	err := source.ListYear(context.Background(), 2026, MovieChartSortComprehensive, nil)
	if got := WatchlistMetadataFailureOf(err); got != WatchlistMetadataFailureSourceError {
		t.Fatalf("分类 = %s，期望 source_error（%v）", got, err)
	}
}

// TC-04 的传输层三类：代理与网络故障**绝不**能变成「查无数据」。
func TestMovieChartDoubanTransportFailuresAreNotNotFound(t *testing.T) {
	t.Run("代理拨号失败", func(t *testing.T) {
		source := newMovieChartSourceForTest(t, "http://movie.invalid", "http://movie.invalid",
			WatchlistMetadataConfig{ProxyURL: "http://" + closedLoopbackAddr(t)})
		err := source.ListYear(context.Background(), 2026, MovieChartSortComprehensive, nil)
		assertMovieChartFailure(t, err, WatchlistMetadataFailureProxyUnreachable)
	})

	t.Run("代理地址填错", func(t *testing.T) {
		// 填错在建客户端时就被拦下，不会静默直连；调用方据此给 proxy_unreachable。
		_, err := NewWatchlistMetadataHTTPClient(WatchlistMetadataConfig{ProxyURL: "127.0.0.1:1080"}, time.Second)
		if !errors.Is(err, ErrWatchlistMetadataProxyInvalid) {
			t.Fatalf("填错的代理地址没有被拦下: %v", err)
		}
		if got := classifyWatchlistMetadataTransportError(err); got != WatchlistMetadataFailureProxyUnreachable {
			t.Fatalf("分类 = %s，期望 proxy_unreachable", got)
		}
	})

	t.Run("目标站连不上", func(t *testing.T) {
		address := "http://" + closedLoopbackAddr(t)
		source := newMovieChartSourceForTest(t, address, address, WatchlistMetadataConfig{})
		_, err := source.Detail(context.Background(), "36208465")
		assertMovieChartFailure(t, err, WatchlistMetadataFailureNetworkUnreachable)
	})
}

// assertMovieChartFailure 只留等值断言：want 传的都是传输层分类，判成 not_found
// 或 credential_* 一律在这里失败，不需要再写一条永远不成立的反向条件。
func assertMovieChartFailure(t *testing.T, err error, want WatchlistMetadataFailure) {
	t.Helper()
	if got := WatchlistMetadataFailureOf(err); got != want {
		t.Fatalf("分类 = %s，期望 %s（代理与网络故障绝不能变成「查无数据」）（%v）", got, want, err)
	}
}

// 六类必须保持互不相等。credential_missing 在本源上永不产生（无凭证），但分类值
// 保留——把它并掉会让别处的排查失去一个方向（D-MC07）。
func TestMovieChartFailureClassesStayDistinct(t *testing.T) {
	seen := map[WatchlistMetadataFailure]bool{}
	for _, class := range []WatchlistMetadataFailure{
		WatchlistMetadataFailureCredentialMissing,
		WatchlistMetadataFailureCredentialInvalid,
		WatchlistMetadataFailureProxyUnreachable,
		WatchlistMetadataFailureNetworkUnreachable,
		WatchlistMetadataFailureNotFound,
		WatchlistMetadataFailureSourceError,
	} {
		if class == "" || seen[class] {
			t.Fatalf("分类码 %q 为空或重复，六类不得合并", class)
		}
		seen[class] = true
	}
	if len(seen) != 6 {
		t.Fatalf("分类码只剩 %d 类", len(seen))
	}
}

// §2.2 的详情字段映射。
func TestMovieChartDoubanDetailMapping(t *testing.T) {
	source := movieChartStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rexxar/api/v2/movie/36208465" {
			t.Errorf("详情路径 = %s", r.URL.Path)
		}
		if got := r.Header.Get("Referer"); !strings.HasSuffix(got, "/movie/subject/36208465/") {
			t.Errorf("详情 Referer = %q", got)
		}
		io.WriteString(w, movieChartDetailFixture)
	})
	detail, err := source.Detail(context.Background(), "36208465")
	if err != nil {
		t.Fatalf("取详情失败: %v", err)
	}
	if detail.DoubanID != "36208465" || detail.Title != "奥德赛" || detail.OriginalTitle != "The Odyssey" ||
		detail.Overview == "" || detail.IsReleased || detail.Rating != 8.1 || detail.RatingCount != 1024 {
		t.Fatalf("详情映射不完整: %+v", detail)
	}
	if len(detail.Countries) != 2 || detail.Countries[0] != "美国" || len(detail.Genres) != 2 {
		t.Fatalf("多值字段映射不完整: %+v", detail)
	}
	if len(detail.Directors) != 1 || detail.Directors[0] != "克里斯托弗·诺兰" {
		t.Fatalf("导演映射不完整: %+v", detail.Directors)
	}
	if len(detail.Cast) != doubanMovieChartMaxPeople || detail.Cast[0] != "马特·达蒙" {
		t.Fatalf("主演应截到前 %d 人: %+v", doubanMovieChartMaxPeople, detail.Cast)
	}
	// 上映日期取「中国大陆」那一项，不是 pubdate 里最早的那一项。
	if detail.ReleaseScope != models.MovieChartScopeTheatrical || detail.ReleaseDate != "2026-08-14" {
		t.Fatalf("上映判定 = %s / %s", detail.ReleaseScope, detail.ReleaseDate)
	}
	if detail.ReleasePubdate != "2026-07-17(美国)\n2026-08-14(中国大陆)" {
		t.Fatalf("pubdate 原文未原样保留: %q", detail.ReleasePubdate)
	}
}

// 详情表明是剧集时不报错，而是判 excluded 结束补全——报错会让它每轮刷新都被重试。
func TestMovieChartDoubanDetailSeriesIsExcluded(t *testing.T) {
	for name, body := range map[string]string{
		"is_tv":     strings.ReplaceAll(movieChartDetailFixture, `"is_tv":false`, `"is_tv":true`),
		"type 是 tv": strings.ReplaceAll(movieChartDetailFixture, `"type":"movie"`, `"type":"tv"`),
	} {
		t.Run(name, func(t *testing.T) {
			source := movieChartStub(t, movieChartFixedResponse(200, body))
			detail, err := source.Detail(context.Background(), "36208465")
			if err != nil {
				t.Fatalf("剧集详情报错了: %v", err)
			}
			if detail.ReleaseScope != models.MovieChartScopeExcluded || detail.ReleaseDate != "" {
				t.Fatalf("剧集判定 = %s / %s", detail.ReleaseScope, detail.ReleaseDate)
			}
		})
	}
}

// TC-09：pubdate 判定。前六行是概要设计 §2 表里的真实样本。
func TestMovieChartReleaseJudgement(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pubdate   []string
		countries []string
		wantScope string
		wantDate  string
	}{
		{"奥德赛 引进片有档期", []string{"2026-08-14(中国大陆)"}, []string{"美国", "加拿大"}, models.MovieChartScopeTheatrical, "2026-08-14"},
		{"挽救计划 多地区用 / 合并", []string{"2026-03-20(美国/中国大陆)"}, []string{"美国"}, models.MovieChartScopeTheatrical, "2026-03-20"},
		{"三心两意 未定档到日", []string{"2026(中国大陆)"}, []string{"中国大陆"}, models.MovieChartScopeTheatrical, ""},
		{"她的小梨涡 国产待映", []string{"2026(未定)"}, []string{"中国大陆"}, models.MovieChartScopeUndetermined, ""},
		{"爱上透明的你 纯网络上线", []string{"2026(中国大陆网络)"}, []string{"中国大陆"}, models.MovieChartScopeExcluded, ""},
		{"酉卯司鸣 电影节", []string{"2026(圣塞巴斯蒂安国际电影节)"}, []string{"中国大陆"}, models.MovieChartScopeExcluded, ""},

		{"多个内地档期取最早", []string{"2026-05-01(中国大陆)", "2026-02-14(中国大陆)"}, nil, models.MovieChartScopeTheatrical, "2026-02-14"},
		{"年份级与日期级并存取日期", []string{"2026(中国大陆)", "2026-04-05(中国大陆)"}, nil, models.MovieChartScopeTheatrical, "2026-04-05"},
		{"未定但不是国产不收", []string{"2026(未定)"}, []string{"美国"}, models.MovieChartScopeExcluded, ""},
		{"网络上线与未定并存仍按未定收", []string{"2026(中国大陆网络)", "2026(未定)"}, []string{"中国大陆"}, models.MovieChartScopeUndetermined, ""},
		{"没有 pubdate", nil, []string{"中国大陆"}, models.MovieChartScopeExcluded, ""},
		{"解析不了的项跳过", []string{"待定", "2026(中国大陆)"}, nil, models.MovieChartScopeTheatrical, ""},
		{"标注两侧的空白不影响", []string{" 2026-08-14( 美国 / 中国大陆 ) "}, nil, models.MovieChartScopeTheatrical, "2026-08-14"},
		{"中国香港不是中国大陆", []string{"2026-08-14(中国香港)"}, []string{"中国大陆"}, models.MovieChartScopeExcluded, ""},
		// countries 也必须精确相等。实测的 countries 是 ["中国大陆"] 这种单值数组，
		// 这一行是防御性的：万一哪天变成拼接串，子串匹配会把「未定」的条目误收进榜单。
		{"countries 是超串时不算命中", []string{"2026(未定)"}, []string{"中国大陆、中国香港"}, models.MovieChartScopeExcluded, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, date := judgeMovieChartRelease(tc.pubdate, tc.countries)
			if scope != tc.wantScope || date != tc.wantDate {
				t.Fatalf("判定 = %s / %q，期望 %s / %q", scope, date, tc.wantScope, tc.wantDate)
			}
		})
	}
}

func TestMovieChartDoubanRejectsInvalidInputBeforeRequest(t *testing.T) {
	source := movieChartStub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("不该发出请求: %s", r.URL.Path)
	})
	err := source.ListYear(context.Background(), 2026, "X", nil)
	if !errors.Is(err, ErrMovieChartSortUnsupported) {
		t.Fatalf("接受了未知排序: %v", err)
	}
	// 参数写错不是源的失败，不能带分类码在页面上显示成「豆瓣不可用」。
	if got := WatchlistMetadataFailureOf(err); got != "" {
		t.Fatalf("排序参数错误被记成失败码 %s", got)
	}
	for _, id := range []string{"", "  ", "../123", "1?key=secret", "douban:1", strings.Repeat("9", 17)} {
		if _, err := source.Detail(context.Background(), id); err == nil {
			t.Fatalf("接受了非法条目 ID: %q", id)
		}
	}
}
