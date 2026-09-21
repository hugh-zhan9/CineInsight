package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"video-master/models"
)

// MovieChartSourceDouban 是榜单源名，进日志与失败记录。
const MovieChartSourceDouban = "douban"

const (
	doubanMovieChartListPath   = "/rexxar/api/v2/movie/recommend"
	doubanMovieChartDetailPath = "/rexxar/api/v2/movie/"
	// 列表请求的 Referer 是探索页，与详情页不同，且**不**由 base 推出来：实测就是
	// 这个地址配合浏览器 UA 才拿得到 JSON（概要设计 §2）。
	doubanMovieChartListReferer = "https://movie.douban.com/explore"
	// 响应大小上限，与 watchlist 侧同值。超限判响应异常，不截断解析。
	doubanMovieChartMaxBytes = 2 << 20
	// 详情里导演与主演各取前几人。榜单只展示 card_subtitle，这两列是给将来的详情
	// 弹窗与审计用的，不需要整份名单。
	doubanMovieChartMaxPeople = 8
)

// 内地公映判定用到的两个标注。**必须精确相等**，不能用 strings.Contains：
// 「中国大陆网络」含「中国大陆」但它是纯网络上线，裁决明确排除（D-MC02）。
const (
	movieChartRegionMainland     = "中国大陆"
	movieChartRegionUndetermined = "未定"
)

// ErrMovieChartSortUnsupported 标记调用方传了 MovieChartSorts() 之外的排序。
// 这是调用方的编程错误，不是源的失败，因此不带六类分类码——不能让一个拼错的参数
// 在页面上显示成「豆瓣不可用」。
var ErrMovieChartSortUnsupported = errors.New("不支持的豆瓣榜单排序")

// doubanMovieChartPubdate 拆 pubdate 的一项：`2026-08-14(中国大陆)` 或 `2026(未定)`。
// 月日要么都在要么都不在；括号内可能是用 / 连接的多个地区（`2026-03-20(美国/中国大陆)`）。
var doubanMovieChartPubdate = regexp.MustCompile(`^(\d{4})(?:-(\d{2})-(\d{2}))?\((.+)\)$`)

// DoubanMovieChartSource 用豆瓣移动端 rexxar 接口抓年度榜单。
//
// 无凭证：这个源永远不会产生 credential_missing（分类值保留，不因为用不到就合并
// 到别的类里）。它也没有任何要从错误文案里抹掉的秘密，因此不需要 watchlist 侧的
// redactWatchlistMetadataSecret——请求 URL 里只有年份、排序和页码。
//
// 出网客户端由构造方注入，来自 NewWatchlistMetadataHTTPClient：用户配的资料源出网
// 代理必须对这条链路同样生效，自建 http.Client 会让代理只覆盖一部分请求。
type DoubanMovieChartSource struct {
	client *http.Client
	// 两个 base 生产环境是同一个主机，分开存只为测试能各自指向桩服务；同时它们是
	// 「响应最终 Host 必须等于请求 Host」这条验证页判定的基准。
	listBaseURL   string
	detailBaseURL string
}

var _ MovieChartSource = (*DoubanMovieChartSource)(nil)

func NewDoubanMovieChartSource(client *http.Client) *DoubanMovieChartSource {
	return &DoubanMovieChartSource{client: client, listBaseURL: "https://m.douban.com", detailBaseURL: "https://m.douban.com"}
}

func (s *DoubanMovieChartSource) Name() string { return MovieChartSourceDouban }

// ListYear 固定翻 MovieChartPagesPerSort 页，**不看返回条数决定是否继续**。
// 终止条件只有三个：页数翻满、请求失败、visit 返回错误（取消或落库失败）。
func (s *DoubanMovieChartSource) ListYear(ctx context.Context, year int, sort string, visit MovieChartListVisitor) error {
	if !movieChartSortSupported(sort) {
		return fmt.Errorf("%w：%q", ErrMovieChartSortUnsupported, sort)
	}
	for index := 0; index < MovieChartPagesPerSort; index++ {
		// 取消时直接回 ctx 的错误，不再发请求：让它走到 client.Do 会被归成
		// network_unreachable，而「用户点了取消」不是网络故障，也不该写失败码。
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := s.listPage(ctx, year, sort, index*MovieChartListPageSize)
		if err != nil {
			return err
		}
		if visit == nil {
			continue
		}
		if err := visit(*page); err != nil {
			return err
		}
	}
	return nil
}

func (s *DoubanMovieChartSource) listPage(ctx context.Context, year int, sort string, start int) (*MovieChartListPage, error) {
	// selected_categories 实测**不起过滤作用**（带 {"地区":"中国大陆"} 仍返回德国、
	// 加拿大的片），传空对象只为贴合真实客户端的请求形态，不能依赖它筛任何东西。
	// tags 只带年份不带地区：地区是**产地**维度，带上会滤掉全部引进片（D-MC02）。
	params := url.Values{
		"refresh":             {"0"},
		"start":               {strconv.Itoa(start)},
		"count":               {strconv.Itoa(MovieChartListPageSize)},
		"uncollect":           {"false"},
		"playable":            {"false"},
		"selected_categories": {"{}"},
		"tags":                {strconv.Itoa(year)},
		"sort":                {sort},
	}
	// total / start / count 三个响应字段有意不解析：total 恒为 500 的占位值，
	// 解析出来早晚有人拿它算分页。
	//
	// Items 是**指针**，为的是把「键缺失 / null」与「空数组」分开，不要简化掉：
	//
	//   - 2026-09-21 实测 start=500 的真实空页是 HTTP 200、3700 字节，键齐全
	//     （bottom_recommend_tags、count、filters、items、manual_tags、
	//     playable_filters、quick_mark、recommend_categories、recommend_tags、
	//     show_rating_filter、sorts、start），其中 "items": []。也就是说真空页
	//     一定带着这个键，两者分得开。
	//   - 只声明 items 的非指针结构体会把 {"code":1002,"msg":"rate limited"}
	//     这类完全不相干的信封也解析成功，25 页全空且没有任何失败码；服务据此
	//     写下 last_refreshed_at=now 并清空失败码，用户看到的是一个标着「数据
	//     更新于 刚刚」的空榜单——这正是裁决禁止的「静默显示空榜单」，只不过是
	//     从「成功」这一侧绕进来的。
	//
	// 同一条线 watchlist_metadata_douban.go:56-59 已经画过：null 不是空结果的合同。
	var payload struct {
		Items *[]struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			Type         string `json:"type"`
			Year         string `json:"year"`
			CardSubtitle string `json:"card_subtitle"`
			Rating       struct {
				Value float64 `json:"value"`
				Count int     `json:"count"`
			} `json:"rating"`
			Pic struct {
				Large  string `json:"large"`
				Normal string `json:"normal"`
			} `json:"pic"`
		} `json:"items"`
	}
	request := doubanMovieChartRequest{base: s.listBaseURL, path: doubanMovieChartListPath, params: params, referer: doubanMovieChartListReferer}
	if err := s.get(ctx, request, &payload); err != nil {
		return nil, err
	}
	if payload.Items == nil {
		return nil, s.invalidResponse("列表响应缺少 items 字段，可能需要访问验证或接口已变更")
	}
	items := *payload.Items
	page := &MovieChartListPage{Sort: sort, Start: start, Items: make([]MovieChartListItem, 0, len(items))}
	for _, item := range items {
		// 先按 type 过滤再校验身份：混进来的剧集本来就要丢，不该让它的字段形态
		// 把整页判成响应异常。
		if strings.TrimSpace(item.Type) != "movie" {
			page.SkippedNonMovie++
			continue
		}
		id := strings.TrimSpace(item.ID)
		title := strings.TrimSpace(item.Title)
		if !doubanSubjectID.MatchString(id) || title == "" {
			return nil, s.invalidResponse("榜单条目缺少有效的豆瓣 ID 或片名")
		}
		// 豆瓣的 tags 年份过滤不保证严格，跨年条目跳过并计数，不写库。
		//
		// 「没给年份」与「年份不同」必须分开：watchlistMetadataYearOf 对空串和
		// 解析不了的值都返回 0，一并当成跨年丢掉等于凭空少一部片，计数还把原因
		// 记错。请求本身已经用 tags=<year> 约束过，条目归到哪一年也由请求参数
		// 决定，所以没给年份的照收，只单独计数让它在日志里可见。
		switch itemYear := watchlistMetadataYearOf(item.Year); {
		case itemYear == 0:
			page.KeptWithoutYear++
		case itemYear != year:
			page.SkippedOtherYear++
			continue
		}
		page.Items = append(page.Items, MovieChartListItem{
			DoubanID:     id,
			Title:        title,
			CardSubtitle: strings.TrimSpace(item.CardSubtitle),
			Rating:       item.Rating.Value,
			RatingCount:  item.Rating.Count,
			PosterURL:    tmdbFirstNonEmpty(item.Pic.Large, item.Pic.Normal),
		})
	}
	if len(items) == 0 {
		// 空页是实测过的瞬时现象（概要设计 §2），既不是「翻到底了」也不是 not_found：
		// 记一条日志继续翻下一页。把它当失败会让一次抖动变成「这一年没有电影」。
		log.Printf("[MovieChart] source=%s 列表空页 year=%d sort=%s start=%d（实测为瞬时现象，继续翻页）", s.Name(), year, sort, start)
	}
	return page, nil
}

// Detail 取详情并完成上映判定。
//
// 判定为剧集时**不报错**：返回 release_scope=excluded 的正常结果，让调用方把这条
// 标成「已判定、不显示」并结束补全（需求设计文档 §2.2）。报错会让它留在 failed，
// 每次刷新都被重试一遍。
func (s *DoubanMovieChartSource) Detail(ctx context.Context, doubanID string) (*MovieChartDetail, error) {
	id := strings.TrimSpace(doubanID)
	if !doubanSubjectID.MatchString(id) {
		return nil, s.invalidResponse("无效的豆瓣条目 ID")
	}
	var item struct {
		ID            string                   `json:"id"`
		Title         string                   `json:"title"`
		OriginalTitle string                   `json:"original_title"`
		Intro         string                   `json:"intro"`
		Type          string                   `json:"type"`
		IsTV          bool                     `json:"is_tv"`
		IsReleased    bool                     `json:"is_released"`
		Pubdate       []string                 `json:"pubdate"`
		Countries     []string                 `json:"countries"`
		Genres        []string                 `json:"genres"`
		Directors     []doubanMovieChartPerson `json:"directors"`
		Actors        []doubanMovieChartPerson `json:"actors"`
		Rating        struct {
			Value float64 `json:"value"`
			Count int     `json:"count"`
		} `json:"rating"`
	}
	request := doubanMovieChartRequest{
		base:             s.detailBaseURL,
		path:             doubanMovieChartDetailPath + id,
		referer:          strings.TrimRight(s.detailBaseURL, "/") + "/movie/subject/" + id + "/",
		businessNotFound: true,
	}
	if err := s.get(ctx, request, &item); err != nil {
		return nil, err
	}
	if strings.TrimSpace(item.ID) != id || strings.TrimSpace(item.Title) == "" {
		return nil, s.invalidResponse("详情缺少片名或条目 ID 不匹配")
	}
	detail := &MovieChartDetail{
		DoubanID:      id,
		Title:         strings.TrimSpace(item.Title),
		OriginalTitle: strings.TrimSpace(item.OriginalTitle),
		IsReleased:    item.IsReleased,
		Rating:        item.Rating.Value,
		RatingCount:   item.Rating.Count,
		Countries:     movieChartTrimmedValues(item.Countries, 0),
		Genres:        movieChartTrimmedValues(item.Genres, 0),
		Directors:     movieChartPersonNames(item.Directors),
		Cast:          movieChartPersonNames(item.Actors),
		Overview:      item.Intro,
		// 原文照存，供界面展示与事后审计判定是否正确。
		ReleasePubdate: strings.Join(item.Pubdate, "\n"),
	}
	if item.IsTV || strings.TrimSpace(item.Type) == "tv" {
		detail.ReleaseScope = models.MovieChartScopeExcluded
		return detail, nil
	}
	if strings.TrimSpace(item.Type) != "movie" {
		return nil, s.invalidResponse("详情不是预期的电影数据")
	}
	detail.ReleaseScope, detail.ReleaseDate = judgeMovieChartRelease(item.Pubdate, item.Countries)
	return detail, nil
}

type doubanMovieChartPerson struct {
	Name string `json:"name"`
}

// judgeMovieChartRelease 是需求设计文档 §3 的 pubdate 判定算法。
//
// 逐项按 doubanMovieChartPubdate 解析，括号内**按 / 拆分**后逐段精确比较。两处都不能省：
//   - 不拆 /：`2026-03-20(美国/中国大陆)`（《挽救计划》实测样本）永远匹配不上；
//   - 不用精确相等：`2026(中国大陆网络)` 会被 strings.Contains 误判成院线公映，
//     而纯网络上线是裁决明确排除的。
//
// 输出：任一段是「中国大陆」→ theatrical，日期取这些项里最早的完整日期（全是年份级
// 则为空串）；否则任一段是「未定」且 countries 含「中国大陆」→ undetermined；
// 其余（电影节、纯网络、没有可解析的 pubdate）→ excluded。
func judgeMovieChartRelease(pubdate []string, countries []string) (scope string, date string) {
	mainland, undetermined := false, false
	earliest := ""
	for _, raw := range pubdate {
		match := doubanMovieChartPubdate.FindStringSubmatch(strings.TrimSpace(raw))
		if match == nil {
			continue
		}
		itemDate := ""
		if match[2] != "" && match[3] != "" {
			itemDate = match[1] + "-" + match[2] + "-" + match[3]
		}
		for _, region := range strings.Split(match[4], "/") {
			switch strings.TrimSpace(region) {
			case movieChartRegionMainland:
				mainland = true
				// YYYY-MM-DD 零填充，字符串比较即日期先后。
				if itemDate != "" && (earliest == "" || itemDate < earliest) {
					earliest = itemDate
				}
			case movieChartRegionUndetermined:
				undetermined = true
			}
		}
	}
	if mainland {
		return models.MovieChartScopeTheatrical, earliest
	}
	if undetermined && movieChartContainsValue(countries, movieChartRegionMainland) {
		return models.MovieChartScopeUndetermined, ""
	}
	return models.MovieChartScopeExcluded, ""
}

func movieChartSortSupported(sort string) bool {
	for _, value := range MovieChartSorts() {
		if value == sort {
			return true
		}
	}
	return false
}

// movieChartContainsValue 按**精确相等**判断，理由同 judgeMovieChartRelease。
func movieChartContainsValue(values []string, want string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

// movieChartTrimmedValues 去空白与空串；limit 为 0 表示不限条数。
func movieChartTrimmedValues(values []string, limit int) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
			if limit > 0 && len(result) == limit {
				break
			}
		}
	}
	return result
}

func movieChartPersonNames(people []doubanMovieChartPerson) []string {
	names := make([]string, 0, len(people))
	for _, person := range people {
		names = append(names, person.Name)
	}
	return movieChartTrimmedValues(names, doubanMovieChartMaxPeople)
}

func (s *DoubanMovieChartSource) invalidResponse(detail string) error {
	return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, http.StatusOK, detail, nil)
}

type doubanMovieChartRequest struct {
	base    string
	path    string
	params  url.Values
	referer string
	// businessNotFound 打开后，404 + {"code":404,"msg":"traversal_error"} 判 not_found。
	//
	// 只有详情接口能打开，而且这个开关**必须由调用方显式给**，不能靠路径前缀推断：
	// 列表接口 /rexxar/api/v2/movie/recommend 与详情接口 /rexxar/api/v2/movie/<id>
	// 共享同一个前缀，按前缀判断会把列表的 404 也认成「查无此片」，一整年的榜单
	// 就渲染成「这一年没有电影」。
	businessNotFound bool
}

// get 发一次请求并解析。传输层的分类复用 classifyWatchlistMetadataTransportError，
// 不抄第二份：代理与网络的区分口径必须与补全链路完全一致。
//
// 与 watchlist_metadata_douban.go 的同名方法只有一处有意的不同：Referer 由调用方给
// （列表用探索页、详情用条目页）。状态码的判定与它保持一致——非 2xx 一律 source_error，
// 理由见下面那段注释。
func (s *DoubanMovieChartSource) get(ctx context.Context, request doubanMovieChartRequest, out any) error {
	if s.client == nil {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "未注入出网客户端", nil)
	}
	endpoint := strings.TrimRight(request.base, "/") + request.path
	if len(request.params) > 0 {
		endpoint += "?" + request.params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "构造请求失败", err)
	}
	req.Header.Set("User-Agent", watchlistPosterUserAgent)
	req.Header.Set("Referer", request.referer)
	req.Header.Set("Accept", "application/json")
	started := time.Now()
	response, err := s.client.Do(req)
	if err != nil {
		return newWatchlistMetadataSourceError(s.Name(), classifyWatchlistMetadataTransportError(err), 0, "请求未能送达", err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, doubanMovieChartMaxBytes+1))
	// 只记路由、状态、字节数与耗时：不记响应正文，也不记请求参数（空页那条日志会
	// 单独带 year/sort/start，用来定位是哪一页空的，同样不涉及任何敏感内容）。
	log.Printf("[MovieChart] source=%s path=%s status=%d bytes=%d elapsed_ms=%d", s.Name(), request.path, response.StatusCode, len(body), time.Since(started).Milliseconds())
	if readErr != nil {
		return newWatchlistMetadataSourceError(s.Name(), classifyWatchlistMetadataTransportError(readErr), response.StatusCode, "读取响应失败", readErr)
	}
	requested, parseErr := url.Parse(request.base)
	if parseErr != nil || response.Request.URL.Host != requested.Host {
		// 跳到别的主机去了＝豆瓣要求访问验证，不是「没有这部片」。
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, response.StatusCode, "豆瓣要求访问验证，暂时无法读取资料", nil)
	}
	// 只有详情接口明确的业务 404 才是「无此条目」；列表接口的 404 是接口变更或被拦，
	// 归 source_error——把它当 not_found 会让一整年的榜单显示成「查无数据」。
	if response.StatusCode == http.StatusNotFound && request.businessNotFound {
		var missing struct {
			Code    int    `json:"code"`
			Message string `json:"msg"`
		}
		if json.Unmarshal(body, &missing) == nil && missing.Code == 404 && missing.Message == "traversal_error" {
			return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, response.StatusCode, "豆瓣未收录该条目或条目已移除", nil)
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// 其余非 2xx 一律 source_error，**有意不走 classifyWatchlistMetadataHTTPStatus**
		// （那个函数会把 401/403 归成 credential_invalid）。两条理由，别改回去：
		//
		//   - 这个源压根没有凭证。报「凭证无效」是把用户指向一个不存在的配置项，
		//     而豆瓣的 403 实际上是反爬拦截，该做的是过一会儿再试或检查出网出口。
		//   - 同一个主机上的 watchlist_metadata_douban.go 已经是这么判的。两个适配器
		//     打同一个站，不能对「403 是什么意思」给出两种结论。
		//
		// 六类并没有因此被合并：credential_invalid 仍由有凭证的源（TMDB、Bangumi）
		// 产生，credential_missing 仍在它们的「没填凭证」路径上产生，这里只是两者
		// 对一个无凭证的源不可达——这是诚实的结果，不是掩盖。
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, response.StatusCode, "豆瓣拒绝请求或暂时不可用", nil)
	}
	if len(body) > doubanMovieChartMaxBytes {
		return s.invalidResponse("豆瓣响应超过大小上限")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return s.invalidResponse("豆瓣未返回预期数据，可能需要访问验证或接口已变更")
	}
	return nil
}
