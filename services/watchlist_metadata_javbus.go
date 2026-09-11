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
	"strings"
	"time"
)

// JavBus 适配器，只覆盖 av 类型（D-WM01）。它是 av 链上的**兜底**源，只有 FANZA
// 明确返回「无此条目」时才会被问到——这条顺序由路由层实现
// （watchlist_metadata_chain.go），本文件对自己排第几一无所知。
//
// JavBus 没有官方 API，本适配器靠**抓取详情页 HTML** 取数据，而且源站有反爬。
// 这两件事决定了下面两条硬规矩：
//
//   - 页面结构对不上一律落 source_error，**绝不落 not_found**。源站改版会让所有
//     选择器同时失效，那时报「这片不存在」会把用户支去核对番号，而真正该做的是
//     修适配器。只有 HTTP 404 才是 JavBus 说「没有这个番号」。
//   - 非 2xx 里**不认 credential_invalid**。JavBus 不要凭证，403 只可能是反爬拦截；
//     归成 credential_invalid 会让用户去翻一个根本不存在的凭证设置。
//
// 字段映射按 JavBus 详情页的公开结构写，**未经真实请求验证**。

const (
	// WatchlistMetadataSourceJavBus 是写进 WatchlistEntry.SourceName 的源名。
	WatchlistMetadataSourceJavBus = "javbus"

	javbusBaseURL = "https://www.javbus.com"

	// javbusUserAgent 是发给 JavBus 的浏览器标识。
	//
	// 这里**不用**本应用的自有标识（Bangumi 那种做法）：JavBus 没有 API 也没有
	// 开发者通道，只有反爬，Go 默认的 "Go-http-client/1.1" 基本等于自报家门。
	// 取一个常见桌面浏览器 UA 是抓取侧唯一能做的事。
	//
	// **这个值没有验证过能不能过它的反爬。** 被拦时表现为非 2xx（多半 403），
	// 按上面的规矩落 source_error。要调整就改这一个常量。
	javbusUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

	// javbusInfoMarker 是详情页右侧信息栏的容器标记。识別碼、發行日期、導演、
	// 類別、演員全在它下面。它同时充当「页面结构还对得上」的锚点之一。
	javbusInfoMarker = `col-md-3 info`
)

// JavBus 详情页的解析规则。集中放在一处，源站改版时只需要看这一块。
//
// 用正则而不是 HTML 解析器：golang.org/x/net/html 目前只是间接依赖，引它要动
// go.mod，而本切片不改那个文件。代价是选择器更脆——正因为脆，解析不出来才必须
// 落 source_error（见文件头）。
var (
	// javbusTitleRe 取 <h3> 里的片名。JavBus 的 h3 文本是「番号 + 标题」。
	javbusTitleRe = regexp.MustCompile(`(?is)<h3[^>]*>(.*?)</h3>`)
	// javbusBigImageTagRe 先整段框出封面链接的 <a> 标签，再由 javbusHrefRe 取 href。
	// 分两步是为了不依赖 class 与 href 的书写顺序。
	javbusBigImageTagRe = regexp.MustCompile(`(?is)<a[^>]*\bbigImage\b[^>]*>`)
	javbusHrefRe        = regexp.MustCompile(`(?is)href="([^"]*)"`)
	// javbusInfoRowRe 取信息栏里的一行：<span class="header">标签:</span> 值</p>。
	javbusInfoRowRe = regexp.MustCompile(`(?is)<span[^>]*\bheader\b[^>]*>(.*?)</span>(.*?)</p>`)
	// 类型与演员靠链接前缀区分：类型链到 /genre/，演员链到 /star/。
	// 这比按出现位置切段可靠——两者在页面上是相邻的两块同构标记。
	javbusGenreLinkRe = regexp.MustCompile(`(?is)<a[^>]+href="[^"]*/genre/[^"]*"[^>]*>(.*?)</a>`)
	javbusStarLinkRe  = regexp.MustCompile(`(?is)<a[^>]+href="[^"]*/star/[^"]*"[^>]*>(.*?)</a>`)
	javbusTagRe       = regexp.MustCompile(`(?s)<[^>]*>`)
)

// JavBus 信息栏里用到的行标签（源站是繁体中文）。
const (
	javbusLabelCode     = "識別碼"
	javbusLabelDate     = "發行日期"
	javbusLabelDirector = "導演"
)

// JavBusWatchlistMetadataSource 只做「抓 JavBus 页面并映射」，不落库、不下海报。
type JavBusWatchlistMetadataSource struct {
	// client 来自 NewWatchlistMetadataHTTPClient，由路由表建一次共用。这里不自建，
	// 否则用户配的资料源出网代理对本适配器不生效——而这个源恰恰是最需要代理的那个。
	client *http.Client
	// baseURL 留给测试指向桩替身；生产路径用上面的常量。
	baseURL string
}

// NewJavBusWatchlistMetadataSource 构造 JavBus 适配器。client 必须来自
// NewWatchlistMetadataHTTPClient。本源不需要任何凭证，因此也永远不会给出
// credential_missing / credential_invalid。
func NewJavBusWatchlistMetadataSource(client *http.Client) *JavBusWatchlistMetadataSource {
	return &JavBusWatchlistMetadataSource{client: client, baseURL: javbusBaseURL}
}

func (s *JavBusWatchlistMetadataSource) Name() string { return WatchlistMetadataSourceJavBus }

// javbusEnsureKind 守住覆盖范围：本适配器只接 av。错误不带失败分类码，
// 走链时不会被当成 not_found。
func javbusEnsureKind(kind WatchlistMetadataKind) error {
	if kind == WatchlistMetadataKindAV {
		return nil
	}
	return fmt.Errorf("%w：JavBus 不覆盖 %q", ErrWatchlistMetadataKindUnsupported, string(kind))
}

// Search 按番号取候选。
//
// JavBus 的番号就是详情页的路径（/ABC-123），所以「搜索」与「取详情」打的是同
// 一个页面，命中即唯一一条候选。这不是偷懒：番号是 AV 的主键，按它查本来就只
// 该有一个结果，拿关键词去搜索页再挑一遍只会引入错配。
func (s *JavBusWatchlistMetadataSource) Search(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
	detail, err := s.Detail(ctx, kind, query)
	if err != nil {
		return nil, err
	}
	return []WatchlistMetadataCandidate{detail.WatchlistMetadataCandidate}, nil
}

// Detail 按番号取详情。sourceItemID 就是番号本身。
func (s *JavBusWatchlistMetadataSource) Detail(ctx context.Context, kind WatchlistMetadataKind, sourceItemID string) (*WatchlistMetadataDetail, error) {
	if err := javbusEnsureKind(kind); err != nil {
		return nil, err
	}
	code := strings.TrimSpace(sourceItemID)
	if code == "" {
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, 0, "番号为空", nil)
	}

	page, err := s.fetch(ctx, "/"+url.PathEscape(code))
	if err != nil {
		return nil, err
	}
	return s.parseDetail(page)
}

// parseDetail 把详情页 HTML 映射成 Detail。
//
// 三个锚点缺一即 source_error：<h3> 标题、信息栏容器、識別碼。它们是页面的骨架，
// 任何一个取不到都说明抓到的不是一张认得的详情页——可能是改版，可能是反爬挡板页，
// 也可能是重定向后的首页。**这三种都不是「没有这个番号」**，所以一律 source_error。
// 其余字段（封面、日期、导演、类型、演员）缺了就留空，不让一条可用的结果整个作废。
func (s *JavBusWatchlistMetadataSource) parseDetail(page string) (*WatchlistMetadataDetail, error) {
	title := ""
	if match := javbusTitleRe.FindStringSubmatch(page); match != nil {
		title = javbusText(match[1])
	}
	if title == "" {
		return nil, s.structureError("找不到标题（<h3>）")
	}

	markerAt := strings.Index(page, javbusInfoMarker)
	if markerAt < 0 {
		return nil, s.structureError("找不到信息栏（" + javbusInfoMarker + "）")
	}
	// 类型与演员只在信息栏之后出现，从锚点起截断，避开页头导航里的同类链接。
	info := page[markerAt:]
	rows := javbusInfoRows(info)

	// 識別碼行里除了番号还可能跟着「複製」按钮之类的附加标记，番号本身不含空白，
	// 取第一个空白分隔的片段即可，一条规则覆盖带不带按钮两种形态。
	code := javbusFirstField(rows[javbusLabelCode])
	if code == "" {
		return nil, s.structureError("找不到識別碼")
	}

	detail := &WatchlistMetadataDetail{
		WatchlistMetadataCandidate: WatchlistMetadataCandidate{
			SourceName: s.Name(),
			// SourceItemID 用页面上的識別碼而不是请求时传进来的字符串：以源为准，
			// 大小写或写法有出入时留下的也是源认的那一个。
			SourceItemID: code,
			Title:        title,
			// JavBus 只登记日文原名，它既是标题也是原名。
			OriginalTitle: title,
			// 详情页没有剧情简介，也没有评分。取不到就留空，不编。
			Overview:  "",
			Rating:    0,
			Year:      watchlistMetadataYearOf(javbusFirstField(rows[javbusLabelDate])),
			PosterURL: s.coverURL(page),
		},
		Genres: javbusLinkTexts(info, javbusGenreLinkRe),
		Cast:   javbusLinkTexts(info, javbusStarLinkRe),
	}
	// JavBus 对没有导演的条目写占位横线，那不是一个人名。
	if director := javbusText(rows[javbusLabelDirector]); director != "" && !javbusIsPlaceholder(director) {
		detail.Directors = []string{director}
	}
	return detail, nil
}

// structureError 造一条「页面结构对不上」的错误。分类固定 source_error——
// 这是本适配器最要紧的一条规矩，集中在一个函数里免得哪条分支写漏。
func (s *JavBusWatchlistMetadataSource) structureError(reason string) error {
	return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, http.StatusOK,
		"页面结构解析失败（"+reason+"），源站可能已改版或返回了反爬拦截页", nil)
}

// coverURL 取封面地址并补成绝对地址。JavBus 的 bigImage 有时给相对路径。
// 取不到封面不是错误，返回空串即可——海报是可选的。
func (s *JavBusWatchlistMetadataSource) coverURL(page string) string {
	tag := javbusBigImageTagRe.FindString(page)
	if tag == "" {
		return ""
	}
	match := javbusHrefRe.FindStringSubmatch(tag)
	if match == nil {
		return ""
	}
	href := strings.TrimSpace(html.UnescapeString(match[1]))
	if href == "" {
		return ""
	}
	base, err := url.Parse(s.baseURL)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(href)
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

// javbusInfoRows 把信息栏拆成「标签 → 值（原始 HTML）」。值保留标记，
// 因为導演这类行要从里面再取链接文本。
func javbusInfoRows(info string) map[string]string {
	rows := make(map[string]string)
	for _, match := range javbusInfoRowRe.FindAllStringSubmatch(info, -1) {
		label := strings.Trim(javbusText(match[1]), ":：")
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		// 同名标签只认第一次出现的，后面的不覆盖。
		if _, seen := rows[label]; seen {
			continue
		}
		rows[label] = match[2]
	}
	return rows
}

// javbusLinkTexts 按给定规则取出链接文本，去重并保持首次出现的顺序——
// 页面上的类型和演员顺序是有意义的，重排等于丢信息。
func javbusLinkTexts(info string, pattern *regexp.Regexp) []string {
	var texts []string
	seen := make(map[string]struct{})
	for _, match := range pattern.FindAllStringSubmatch(info, -1) {
		text := javbusText(match[1])
		if text == "" {
			continue
		}
		if _, duplicated := seen[text]; duplicated {
			continue
		}
		seen[text] = struct{}{}
		texts = append(texts, text)
	}
	return texts
}

// javbusText 把一段 HTML 压成可读文本：去标记、还原实体、折叠空白。
func javbusText(raw string) string {
	text := javbusTagRe.ReplaceAllString(raw, " ")
	text = html.UnescapeString(text)
	return strings.Join(strings.Fields(text), " ")
}

// javbusFirstField 取文本里第一个空白分隔的片段。番号与日期都不含空白，
// 用它能稳定跳过同一行里跟着的按钮、备注之类的附加文本。
func javbusFirstField(raw string) string {
	fields := strings.Fields(javbusText(raw))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// javbusIsPlaceholder 判定一个值是不是 JavBus 的占位横线（"----"）。
// 源站对缺失的导演就写这个，原样收下会让界面显示一个叫「----」的导演。
func javbusIsPlaceholder(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	return strings.Trim(trimmed, "-—–ー") == ""
}

// fetch 抓一个 JavBus 页面，返回 2xx 的 HTML 正文。失败一律归成带分类码的
// *WatchlistMetadataSourceError。
//
// 状态码只分两档：404 是「没有这个番号」，其余非 2xx 一律 source_error。
// 刻意**不走 classifyWatchlistMetadataHTTPStatus**——那个分类器会把 401 / 403 判成
// credential_invalid，而 JavBus 压根不要凭证，403 只可能是反爬拦截。
func (s *JavBusWatchlistMetadataSource) fetch(ctx context.Context, path string) (string, error) {
	if s.client == nil {
		return "", newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "未注入出网客户端", nil)
	}

	endpoint := strings.TrimRight(s.baseURL, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "构造请求失败", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("User-Agent", javbusUserAgent)

	started := time.Now()
	response, err := s.client.Do(request)
	if err != nil {
		failure := classifyWatchlistMetadataTransportError(err)
		log.Printf("[WatchlistMetadata] source=%s path=%s elapsed_ms=%d failure=%s",
			s.Name(), path, time.Since(started).Milliseconds(), failure)
		return "", newWatchlistMetadataSourceError(s.Name(), failure, 0, "请求未能送达", err)
	}
	defer response.Body.Close()

	body, readErr := io.ReadAll(response.Body)
	log.Printf("[WatchlistMetadata] source=%s path=%s elapsed_ms=%d status=%d bytes=%d",
		s.Name(), path, time.Since(started).Milliseconds(), response.StatusCode, len(body))
	if readErr != nil {
		// 响应读到一半断了是连接层的问题，不是源返回了坏页面。
		return "", newWatchlistMetadataSourceError(s.Name(), classifyWatchlistMetadataTransportError(readErr), response.StatusCode, "读取响应失败", readErr)
	}

	if response.StatusCode == http.StatusNotFound {
		// JavBus 对没收录的番号就是 404，这是它唯一一处「明确说没有」。
		return "", newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, response.StatusCode, "源站没有这个番号", nil)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, response.StatusCode, "源站返回了非 2xx（可能是反爬拦截）", nil)
	}
	return string(body), nil
}
