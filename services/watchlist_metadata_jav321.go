package services

import (
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// jav321 适配器，只覆盖 av 类型。零凭证。
//
// 它在聚合里的主要贡献是**简介**——JavBus 的详情页没有简介（真实请求确认），
// 而 jav321 有；此外它给演员、发行日期与评分。
//
// 与 JavBus 一样靠抓取 HTML，因此沿用同样的两条硬规矩：
//
//   - 页面结构对不上一律落 source_error，**绝不落 not_found**。源站改版会让所有
//     选择器同时失效，那时报「这片不存在」会把用户支去核对番号，而真正该做的是
//     修适配器。只有 HTTP 404 才是 jav321 说「没有这个番号」。
//   - 非 2xx 里**不认 credential_invalid**。它不要凭证，403 只可能是反爬拦截。
//
// **番号形态**：jav321 的详情页路径用的是 DMM 的补零编码（ssis00001），
// 直接拿标准番号 SSIS-001 去打 /video/ 会 404。但它有搜索入口会自己做这层换算：
// POST /search 带 sn=SSIS-001 → 301 → /video/ssis00001。
// 所以本适配器**把换算交给源站自己做**，不在我们这边猜——这正是
// 「不做源专有编码换算」那条既有裁决要防的事。

const (
	// WatchlistMetadataSourceJav321 的源名常量定义在 watchlist_metadata_aggregate.go，
	// 与择优表放在一起。

	jav321BaseURL = "https://www.jav321.com"

	// jav321SearchPath 接受标准番号并 301 到详情页；sn 是它的表单字段名。
	jav321SearchPath  = "/search"
	jav321SearchField = "sn"

	// jav321DetailPathPrefix 后面跟的是源站自己的编码（ssis00001 这种），
	// 不是标准番号。
	jav321DetailPathPrefix = "/video/"

	jav321UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

var (
	// jav321TitleRe 取 <h3> 整段。h3 的形态是「标题 演员 <small>番号 演员</small>」，
	// <small> 那截由 jav321SmallRe 单独剥掉。剥完仍会带着尾随的演员名——那是源站
	// 自己的排版习惯，不做进一步猜测式清洗（演员名另有 出演者 字段可取）。
	jav321TitleRe = regexp.MustCompile(`(?is)<h3>(.*?)</h3>`)
	jav321SmallRe = regexp.MustCompile(`(?is)<small>.*?</small>`)

	// jav321CoverRe 取 DMM 图床上的大图（pl）优先，缩略图（ps）兜底。
	jav321CoverRe = regexp.MustCompile(`(?i)(https?:)?//pics\.dmm\.co\.jp[^"'\s]+?\.jpg`)

	// jav321PlotRe 取简介。它是 col-md-12 里的一段裸文本，没有自己的类名——
	// 这是本适配器最脆的一处选择器，源站一改版它就失效（那时落 source_error）。
	jav321PlotRe = regexp.MustCompile(`(?is)<div class="row"><div class="col-md-12">([^<]{20,})</div>`)

	// jav321TagRe 剥标记用。
	jav321TagRe = regexp.MustCompile(`(?is)<[^>]+>`)
)

// jav321Labels 是详情页上的标签名。页面把它们写成 <b>标签</b>: 值。
const (
	jav321LabelCast    = "出演者"
	jav321LabelDate    = "配信開始日"
	jav321LabelRating  = "平均評価"
	jav321LabelMaker   = "メーカー"
	jav321LabelRuntime = "収録時間"
)

// Jav321WatchlistMetadataSource 只做「问 jav321 要数据并映射」，不落库、不下海报。
type Jav321WatchlistMetadataSource struct {
	// client 来自 NewWatchlistMetadataHTTPClient，由路由表建一次共用。这里不自建，
	// 否则用户配的资料源出网代理对本适配器不生效。
	client  *http.Client
	baseURL string
}

// NewJav321WatchlistMetadataSource 构造 jav321 适配器。client 必须来自
// NewWatchlistMetadataHTTPClient。
func NewJav321WatchlistMetadataSource(client *http.Client) *Jav321WatchlistMetadataSource {
	return &Jav321WatchlistMetadataSource{client: client, baseURL: jav321BaseURL}
}

func (s *Jav321WatchlistMetadataSource) Name() string { return WatchlistMetadataSourceJav321 }

func jav321EnsureKind(kind WatchlistMetadataKind) error {
	if kind == WatchlistMetadataKindAV {
		return nil
	}
	return fmt.Errorf("%w：jav321 不覆盖 %q", ErrWatchlistMetadataKindUnsupported, string(kind))
}

// Search 按标准番号取候选。
//
// 走的是源站的搜索入口，由它把标准番号换算成自己的编码并重定向到详情页。
// 落地页就是详情页，所以这里顺带把详情也解出来了——但仍如约只回候选，
// 详情由 Detail 再取一次。多一次往返换来的是「SourceItemID 一定回得到源上」。
func (s *Jav321WatchlistMetadataSource) Search(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
	if err := jav321EnsureKind(kind); err != nil {
		return nil, err
	}
	code := normalizeWatchlistAVCode(query)
	if code == "" {
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, 0, "番号为空", nil)
	}

	form := url.Values{}
	form.Set(jav321SearchField, code)
	page, finalPath, err := s.post(ctx, jav321SearchPath, form)
	if err != nil {
		return nil, err
	}
	// 搜到了就一定落在 /video/<源站编码> 上；没落上说明它没收录这个番号。
	itemID := strings.TrimPrefix(finalPath, jav321DetailPathPrefix)
	if itemID == finalPath || strings.TrimSpace(itemID) == "" {
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, http.StatusOK,
			"搜索没有落到详情页", nil)
	}
	detail, err := s.parseDetail(page, itemID)
	if err != nil {
		return nil, err
	}
	// 番号是 av 的主键，按它查本来就只有一条。
	return []WatchlistMetadataCandidate{detail.WatchlistMetadataCandidate}, nil
}

// Detail 按源站编码取详情。sourceItemID 来自本源 Search 给出的候选，
// 形如 ssis00001，**不是**标准番号。
func (s *Jav321WatchlistMetadataSource) Detail(ctx context.Context, kind WatchlistMetadataKind, sourceItemID string) (*WatchlistMetadataDetail, error) {
	if err := jav321EnsureKind(kind); err != nil {
		return nil, err
	}
	itemID := strings.TrimSpace(sourceItemID)
	if itemID == "" {
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, 0, "源条目 ID 为空", nil)
	}
	page, _, err := s.get(ctx, jav321DetailPathPrefix+url.PathEscape(itemID))
	if err != nil {
		return nil, err
	}
	return s.parseDetail(page, itemID)
}

// parseDetail 把详情页映射成内部结构。
//
// 标题解析不出来就判定页面结构对不上——它是本页最稳的一个锚点，它都没了，
// 其余字段的缺失也不能当成「这片没有这些信息」。
func (s *Jav321WatchlistMetadataSource) parseDetail(page string, itemID string) (*WatchlistMetadataDetail, error) {
	match := jav321TitleRe.FindStringSubmatch(page)
	if match == nil {
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, http.StatusOK,
			"页面结构解析失败（找不到标题（<h3>）），源站可能已改版或返回了反爬拦截页", nil)
	}
	title := jav321Text(jav321SmallRe.ReplaceAllString(match[1], " "))
	if title == "" {
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, http.StatusOK,
			"页面结构解析失败（标题为空）", nil)
	}

	detail := &WatchlistMetadataDetail{
		WatchlistMetadataCandidate: WatchlistMetadataCandidate{
			SourceName:   s.Name(),
			SourceItemID: itemID,
			Title:        title,
			// 源站只有一个标题，原题与标题同源。
			OriginalTitle: title,
			Year:          watchlistMetadataYearOf(jav321Field(page, jav321LabelDate)),
			Overview:      jav321Plot(page),
			Rating:        jav321Rating(page),
			PosterURL:     jav321Cover(page),
		},
		Cast: jav321Names(jav321Field(page, jav321LabelCast)),
	}
	return detail, nil
}

// jav321Field 取 <b>标签</b> 之后、下一个 <b> 或换行之前的那段值。
func jav321Field(page string, label string) string {
	pattern := regexp.MustCompile(`(?is)<b>` + regexp.QuoteMeta(label) + `</b>\s*:?\s*(.*?)(?:<b>|<br\s*/?>|</div>)`)
	match := pattern.FindStringSubmatch(page)
	if match == nil {
		return ""
	}
	return match[1]
}

// jav321Names 把一段可能含多个链接的标记切成名字列表。
func jav321Names(fragment string) []string {
	if strings.TrimSpace(fragment) == "" {
		return nil
	}
	// 先剥标记再解实体：页面用 &nbsp; 做名字之间的间隔，不解码的话它会原样
	// 变成名字的一部分（真实请求里见过「葵つかさ &nbsp;」这种）。
	plain := html.UnescapeString(jav321TagRe.ReplaceAllString(fragment, "\n"))
	return trimWatchlistNames(strings.Split(plain, "\n"))
}

// jav321Text 剥标记并压平空白。
func jav321Text(fragment string) string {
	text := jav321TagRe.ReplaceAllString(fragment, " ")
	return strings.Join(strings.Fields(html.UnescapeString(text)), " ")
}

// jav321Plot 取简介。取不到就留空——空简介是「这片没写简介」的正常状态，
// 不足以判定页面结构失效（标题才是那个判据）。
func jav321Plot(page string) string {
	match := jav321PlotRe.FindStringSubmatch(page)
	if match == nil {
		return ""
	}
	return jav321Text(match[1])
}

// jav321Rating 取平均評価。解析不出来就留 0，不猜。
func jav321Rating(page string) float64 {
	text := jav321Text(jav321Field(page, jav321LabelRating))
	if text == "" {
		return 0
	}
	value, err := strconv.ParseFloat(strings.Fields(text)[0], 64)
	if err != nil {
		return 0
	}
	return value
}

// jav321Cover 取封面。优先大图（pl），没有再退缩略图（ps）。
func jav321Cover(page string) string {
	matches := jav321CoverRe.FindAllString(page, -1)
	fallback := ""
	for _, candidate := range matches {
		absolute := candidate
		if strings.HasPrefix(absolute, "//") {
			absolute = "https:" + absolute
		}
		// DMM 的图床把双斜杠写进路径里也照样能取，但规整一下更稳。
		absolute = strings.Replace(absolute, "co.jp//", "co.jp/", 1)
		if strings.Contains(absolute, "pl.jpg") {
			return absolute
		}
		if fallback == "" {
			fallback = absolute
		}
	}
	return fallback
}

// get / post 共用同一套响应处理：状态码归类、读正文、返回最终路径。
//
// 最终路径是承重信息：搜索靠 301 落到详情页，落点里带着源站自己的编码。
func (s *Jav321WatchlistMetadataSource) get(ctx context.Context, path string) (string, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.baseURL, "/")+path, nil)
	if err != nil {
		return "", "", newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "构造请求失败", err)
	}
	return s.do(ctx, request, path)
}

func (s *Jav321WatchlistMetadataSource) post(ctx context.Context, path string, form url.Values) (string, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.baseURL, "/")+path, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "构造请求失败", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.do(ctx, request, path)
}

func (s *Jav321WatchlistMetadataSource) do(ctx context.Context, request *http.Request, path string) (string, string, error) {
	if s.client == nil {
		return "", "", newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "未注入出网客户端", nil)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("User-Agent", jav321UserAgent)

	started := time.Now()
	response, err := s.client.Do(request)
	if err != nil {
		failure := classifyWatchlistMetadataTransportError(err)
		log.Printf("[WatchlistMetadata] source=%s path=%s elapsed_ms=%d failure=%s",
			s.Name(), path, time.Since(started).Milliseconds(), failure)
		return "", "", newWatchlistMetadataSourceError(s.Name(), failure, 0, "请求未能送达", err)
	}
	defer response.Body.Close()

	body, readErr := io.ReadAll(response.Body)
	// 跟随重定向后的落点。搜索靠它拿到源站编码。
	finalPath := path
	if response.Request != nil && response.Request.URL != nil {
		finalPath = response.Request.URL.Path
	}
	log.Printf("[WatchlistMetadata] source=%s path=%s final=%s elapsed_ms=%d status=%d bytes=%d",
		s.Name(), path, finalPath, time.Since(started).Milliseconds(), response.StatusCode, len(body))
	if readErr != nil {
		return "", "", newWatchlistMetadataSourceError(s.Name(), classifyWatchlistMetadataTransportError(readErr), response.StatusCode, "读取响应失败", readErr)
	}
	if response.StatusCode == http.StatusNotFound {
		// 404 是 jav321 唯一一处「明确说没有这个番号」。
		return "", "", newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, response.StatusCode, "源站没有这个番号", nil)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// 不走 classifyWatchlistMetadataHTTPStatus：它会把 401/403 判成
		// credential_invalid，而 jav321 不要凭证，403 只可能是反爬拦截。
		return "", "", newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, response.StatusCode, "源站返回了非 2xx", nil)
	}
	return string(body), finalPath, nil
}
