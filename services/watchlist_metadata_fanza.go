package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FANZA 适配器，只覆盖 av 类型（D-WM01）。它是 av 链上的**首选**源，
// 查无此片时由 JavBus 兜底——兜底逻辑在路由层（watchlist_metadata_chain.go），
// 不在本文件里。这条边界是刻意的：兜底藏进首选源内部，就再也说不清一条结果
// 到底是谁给的。
//
// 走的是 DMM 联盟 API v3 的 ItemList 接口。字段映射按 DMM 公开的联盟 API 文档写，
// **没有凭证，未经任何真实请求验证**——已知不确定的形态都在下面各自的注释里点了名。

const (
	// WatchlistMetadataSourceFANZA 是写进 WatchlistEntry.SourceName 的源名。
	WatchlistMetadataSourceFANZA = "fanza"

	fanzaAPIBaseURL = "https://api.dmm.com/affiliate/v3"

	// ItemList 的三个定位参数，锁定 FANZA 的成人动画/AV 数字影片货架。
	// site 取 FANZA（成人侧），service=digital + floor=videoa 是 AV 单品所在的货架。
	fanzaSite    = "FANZA"
	fanzaService = "digital"
	fanzaFloor   = "videoa"

	// fanzaSearchHits 限定单次搜索取回的候选数。候选是给用户挑的，一屏足够。
	fanzaSearchHits = 20

	// fanzaSortByMatch 保持源侧的匹配度排序。适配器合同要求「保持源给出的匹配度
	// 顺序」，这里显式声明而不是依赖服务端默认值。
	fanzaSortByMatch = "match"

	// fanzaOutputJSON 必须显式要求 JSON：DMM v3 不带这个参数时返回 XML。
	fanzaOutputJSON = "json"
)

// FANZAWatchlistMetadataSource 只做「问 FANZA 要数据并映射」，不落库、不下海报。
type FANZAWatchlistMetadataSource struct {
	// apiID 与 affiliateID 都是 DMM 联盟 API 的必传参数，缺一不可，
	// 缺任何一个都在发请求前判 credential_missing（见 get）。
	apiID       string
	affiliateID string
	// client 来自 NewWatchlistMetadataHTTPClient，由路由表建一次共用。这里不自建，
	// 否则用户配的资料源出网代理对本适配器不生效。
	client *http.Client
	// baseURL 留给测试指向桩替身；生产路径用上面的常量。
	baseURL string
}

// NewFANZAWatchlistMetadataSource 构造 FANZA 适配器。client 必须来自
// NewWatchlistMetadataHTTPClient。两个凭证都允许为空，空值在运行期判
// credential_missing——装配期拒绝会让用户连「哪个源没配」都看不到。
func NewFANZAWatchlistMetadataSource(apiID string, affiliateID string, client *http.Client) *FANZAWatchlistMetadataSource {
	return &FANZAWatchlistMetadataSource{
		apiID:       strings.TrimSpace(apiID),
		affiliateID: strings.TrimSpace(affiliateID),
		client:      client,
		baseURL:     fanzaAPIBaseURL,
	}
}

func (s *FANZAWatchlistMetadataSource) Name() string { return WatchlistMetadataSourceFANZA }

// fanzaEnsureKind 守住覆盖范围：本适配器只接 av。
//
// 路由表今天只把 av 指过来，但适配器不能依赖这一点——它是被直接持有的对象，
// 传错类型要立刻说清楚。这条错误**不带失败分类码**，于是走链时不会被当成
// not_found，也就不会触发兜底：类型传错是调用方的问题，换个源问一遍没有意义。
func fanzaEnsureKind(kind WatchlistMetadataKind) error {
	if kind == WatchlistMetadataKindAV {
		return nil
	}
	return fmt.Errorf("%w：FANZA 不覆盖 %q", ErrWatchlistMetadataKindUnsupported, string(kind))
}

// Search 按番号取候选。源明确表示没有收录时返回 not_found 分类的错误。
//
// AV 的匹配键是番号（ABC-123 这种形态），不是片名——番号唯一且稳定，片名匹配
// 在这个品类里几乎不可用。这里把入参原样当关键词交给 ItemList 的 keyword，
// **不做任何番号到 content_id 的归一化**：FANZA 的 content_id 是加了位数补零的
// 另一套形态（abc00123），两者的换算规则没有公开文档可依，凭猜写一个只会在
// 用户看不见的地方悄悄查错片。归一化缺失带来的漏查在残留风险里记着。
func (s *FANZAWatchlistMetadataSource) Search(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
	if err := fanzaEnsureKind(kind); err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("keyword", strings.TrimSpace(query))
	params.Set("hits", strconv.Itoa(fanzaSearchHits))
	params.Set("offset", "1")
	params.Set("sort", fanzaSortByMatch)

	items, err := s.itemList(ctx, params)
	if err != nil {
		return nil, err
	}

	candidates := make([]WatchlistMetadataCandidate, 0, len(items))
	for _, item := range items {
		// 保持 FANZA 给的顺序（已按 sort=match 排），这里不重排也不筛选。
		candidates = append(candidates, s.toCandidate(item))
	}
	return candidates, nil
}

// Detail 按源条目 ID 取详情。sourceItemID 是 FANZA 的 content_id，
// 由同一个源的 Search 给出。
//
// ItemList 带 cid 过滤就是「按 ID 取单品」，FANZA 没有单独的详情接口，所以
// 详情与搜索共用一条通道；类型、导演、女优都在同一份 iteminfo 里，一次就够。
func (s *FANZAWatchlistMetadataSource) Detail(ctx context.Context, kind WatchlistMetadataKind, sourceItemID string) (*WatchlistMetadataDetail, error) {
	if err := fanzaEnsureKind(kind); err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("cid", strings.TrimSpace(sourceItemID))
	params.Set("hits", "1")

	items, err := s.itemList(ctx, params)
	if err != nil {
		return nil, err
	}

	item := items[0]
	detail := &WatchlistMetadataDetail{WatchlistMetadataCandidate: s.toCandidate(item)}
	for _, genre := range item.ItemInfo.Genre {
		if name := strings.TrimSpace(genre.Name); name != "" {
			detail.Genres = append(detail.Genres, name)
		}
	}
	for _, director := range item.ItemInfo.Director {
		if name := strings.TrimSpace(director.Name); name != "" {
			detail.Directors = append(detail.Directors, name)
		}
	}
	// AV 的「演员」就是 iteminfo.actress。男优在 FANZA 的联盟 API 里没有对应字段，
	// 取不到就是取不到，不拿制作商或标签去凑一个像演员表的东西。
	for _, actress := range item.ItemInfo.Actress {
		if name := strings.TrimSpace(actress.Name); name != "" {
			detail.Cast = append(detail.Cast, name)
		}
	}
	return detail, nil
}

// fanzaItem 是 ItemList 返回的单品。只声明用得上的字段。
type fanzaItem struct {
	ContentID string `json:"content_id"`
	ProductID string `json:"product_id"`
	Title     string `json:"title"`
	// Date 的形态是 "2020-01-01 10:00:00"，未定档时可能是空串。
	Date     string `json:"date"`
	ImageURL struct {
		List  string `json:"list"`
		Small string `json:"small"`
		Large string `json:"large"`
	} `json:"imageURL"`
	Review struct {
		// Average 在文档里是字符串（"4.50"），但同一个接口的数值字段在 DMM 各
		// 货架上出现过字符串与数字两种形态。这里收成 RawMessage 自己解，
		// 免得一个评分的形态差异把**查到了**的响应判成 source_error。
		Average json.RawMessage `json:"average"`
	} `json:"review"`
	ItemInfo struct {
		Genre    []fanzaNamed `json:"genre"`
		Director []fanzaNamed `json:"director"`
		Actress  []fanzaNamed `json:"actress"`
	} `json:"iteminfo"`
}

// fanzaNamed 是 iteminfo 下各类目的公共形态。只取名字，id 对本链路没有用处。
type fanzaNamed struct {
	Name string `json:"name"`
}

// fanzaEnvelope 是 ItemList 的响应外壳。
//
// 刻意**不声明 result.status**：它在 DMM 各接口上出现过数字与字符串两种形态，
// 而成功与否 HTTP 状态码已经说清楚了，多解一个形态不稳的字段只会凭空多一条
// 解析失败的路径。
type fanzaEnvelope struct {
	Result struct {
		Items  []fanzaItem `json:"items"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"result"`
}

// itemList 发一次 ItemList 请求并返回单品列表。空列表即 FANZA 说「没有收录」，
// 在这里就归成 not_found——它是 av 链上唯一会触发 JavBus 兜底的分类（D-WM07），
// 必须在适配器内部认定，不能留给调用方去猜一个空切片的含义。
func (s *FANZAWatchlistMetadataSource) itemList(ctx context.Context, params url.Values) ([]fanzaItem, error) {
	var payload fanzaEnvelope
	if err := s.get(ctx, "/ItemList", params, &payload); err != nil {
		return nil, err
	}
	if len(payload.Result.Items) == 0 {
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, http.StatusOK,
			watchlistMetadataFirstNonEmpty(fanzaErrorMessage(payload), "搜索无结果"), nil)
	}
	return payload.Result.Items, nil
}

func (s *FANZAWatchlistMetadataSource) toCandidate(item fanzaItem) WatchlistMetadataCandidate {
	return WatchlistMetadataCandidate{
		SourceName: s.Name(),
		// SourceItemID 是承重字段：没有它就无法重查详情，也无法去重。content_id
		// 才是 ItemList 的 cid 过滤认的那个 ID，product_id 只在拿不到它时顶上。
		SourceItemID: watchlistMetadataFirstNonEmpty(item.ContentID, item.ProductID),
		Title:        strings.TrimSpace(item.Title),
		// FANZA 只有一个日文标题，它本身就是原名，如实填进 OriginalTitle。
		OriginalTitle: strings.TrimSpace(item.Title),
		// ItemList 不返回剧情简介，取不到就留空，不拿标题或类目去凑一段。
		Overview: "",
		Year:     watchlistMetadataYearOf(item.Date),
		Rating:   fanzaRatingOf(item.Review.Average),
		// FANZA 给的是绝对地址，按清晰度从高到低取第一个有值的。
		PosterURL: watchlistMetadataFirstNonEmpty(item.ImageURL.Large, item.ImageURL.List, item.ImageURL.Small),
	}
}

// fanzaRatingOf 解析评分。文档里是字符串 "4.50"，但数字形态也照收——
// 两种都解不出来时给 0，而不是让整条响应失败：评分不是承重字段。
func fanzaRatingOf(raw json.RawMessage) float64 {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0
	}
	// 字符串形态带引号，先剥掉再按数字解析。
	if unquoted, err := strconv.Unquote(text); err == nil {
		text = unquoted
	}
	rating, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return 0
	}
	return rating
}

// fanzaErrorMessage 取 DMM 错误体里的可读原因。它是 DMM 自己的文案，
// 不含用户凭证，可以安全带进错误里。
func fanzaErrorMessage(payload fanzaEnvelope) string {
	for _, item := range payload.Result.Errors {
		if message := strings.TrimSpace(item.Message); message != "" {
			return message
		}
	}
	return ""
}

// get 发一次 FANZA 请求并把 2xx 的 JSON 解进 out，失败一律归成带分类码的
// *WatchlistMetadataSourceError。
func (s *FANZAWatchlistMetadataSource) get(ctx context.Context, path string, params url.Values, out any) error {
	// credential_missing 的定义就包含「不发请求」（D-WM14）。两个凭证都是 ItemList
	// 的必传参数，缺任何一个出网都必然失败，发出去只会把一次配置问题变成一条真实
	// 的对外记录——而且这条失败还会白白触发一次 JavBus 兜底。
	if s.apiID == "" || s.affiliateID == "" {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureCredentialMissing, 0,
			"未配置 FANZA 凭证（api_id 与 affiliate_id 都必填）", nil)
	}
	if s.client == nil {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "未注入出网客户端", nil)
	}

	query := url.Values{}
	for key, values := range params {
		query[key] = values
	}
	query.Set("api_id", s.apiID)
	query.Set("affiliate_id", s.affiliateID)
	query.Set("site", fanzaSite)
	query.Set("service", fanzaService)
	query.Set("floor", fanzaFloor)
	query.Set("output", fanzaOutputJSON)
	endpoint := strings.TrimRight(s.baseURL, "/") + path + "?" + query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "构造请求失败", s.redact(err))
	}
	request.Header.Set("Accept", "application/json")

	started := time.Now()
	response, err := s.client.Do(request)
	if err != nil {
		failure := classifyWatchlistMetadataTransportError(err)
		// 日志只记路径，不记完整 URL——两个凭证都在 query 里。
		log.Printf("[WatchlistMetadata] source=%s path=%s elapsed_ms=%d failure=%s",
			s.Name(), path, time.Since(started).Milliseconds(), failure)
		return newWatchlistMetadataSourceError(s.Name(), failure, 0, "请求未能送达", s.redact(err))
	}
	defer response.Body.Close()

	body, readErr := io.ReadAll(response.Body)
	log.Printf("[WatchlistMetadata] source=%s path=%s elapsed_ms=%d status=%d bytes=%d",
		s.Name(), path, time.Since(started).Milliseconds(), response.StatusCode, len(body))
	if readErr != nil {
		// 响应读到一半断了是连接层的问题，不是源返回了坏数据。
		return newWatchlistMetadataSourceError(s.Name(), classifyWatchlistMetadataTransportError(readErr), response.StatusCode, "读取响应失败", s.redact(readErr))
	}

	if response.StatusCode == http.StatusNotFound {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, response.StatusCode, "", nil)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failed fanzaEnvelope
		// 错误体解不出来也无所谓，分类靠状态码，文案只是锦上添花。
		_ = json.Unmarshal(body, &failed)
		return newWatchlistMetadataSourceError(s.Name(), classifyWatchlistMetadataHTTPStatus(response.StatusCode), response.StatusCode, fanzaErrorMessage(failed), nil)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, response.StatusCode, "响应不是预期的 JSON", s.redact(err))
	}
	return nil
}

// redact 抹掉错误文案里的两个凭证。*url.Error 会把整个请求 URL 带进 Error()，
// 而 api_id 与 affiliate_id 都在 query 里，原样冒泡等于把它们抄进日志和界面。
// 两个都要抹：漏掉一个和一个都没抹在后果上没有区别。
func (s *FANZAWatchlistMetadataSource) redact(err error) error {
	return redactWatchlistMetadataSecret(redactWatchlistMetadataSecret(err, s.apiID), s.affiliateID)
}

// 下面两个是 av 链上 FANZA 与 JavBus 共用的小工具。
//
// 它们与 tmdb* / bangumi* 的同名工具逻辑一致——那两套是 P-003 / P-004 各自加的
// 副本。收口要动 watchlist_metadata_source.go，不在本切片的改动范围内，因此这里
// 只保证**新增的两个适配器共用一份**，不再添第三、第四个副本。

// watchlistMetadataFirstNonEmpty 返回第一个去掉首尾空白后非空的值。
func watchlistMetadataFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// watchlistMetadataYearOf 从 YYYY-MM-DD 或 "YYYY-MM-DD HH:MM:SS" 取年份。
// 取不到时返回 0——不猜一个年份出来，界面上「没有年份」比「错的年份」好。
func watchlistMetadataYearOf(date string) int {
	date = strings.TrimSpace(date)
	if len(date) < 4 {
		return 0
	}
	year, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return year
}
