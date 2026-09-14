package services

import (
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// FC2 官方站适配器，覆盖 av 类型里的 FC2-PPV 作品。零凭证。
//
// **为什么它和 JavBus / jav321 并列挂在 av 上，而不是单开一个类型**：
// 用户裁决复用 av 类型、按番号形态分流。分流没有做成一张额外的路由表——
// 那会让「谁负责哪个番号」散在两处。做法是把三个源都挂上，各自认自己的番号：
// 本适配器见到不是 FC2 形态的番号当场返回 not_found 且**不发请求**；
// 反过来 JavBus / jav321 遇到 FC2 番号会自然 404。聚合器既有的
// 「单源失败不阻断其余源」原样就能处理，不需要任何新机制。
//
// 代价写明：一次 FC2 查询会白跑 JavBus 与 jav321 两个请求（各自 404）。
// 补全是后台逐条跑的，这点开销换掉了一处隐藏的分流判断，划算。
//
// 数据来自官方商品页，是 FC2 作品最权威的出处——不像第三方聚合站那样
// 存在「番号对得上、内容是另一部片」的风险。
//
// 与其余抓取型源同样的两条硬规矩：页面结构对不上落 source_error 不落
// not_found；403 不认 credential_invalid（本站不要凭证）。

const (
	// WatchlistMetadataSourceFC2 是写进 WatchlistEntry.SourceName 的源名。
	WatchlistMetadataSourceFC2 = "fc2"

	fc2BaseURL           = "https://adult.contents.fc2.com"
	fc2ArticlePathPrefix = "/article/"

	fc2UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

var (
	// fc2CodeRe 认 FC2 番号并取出官方商品号。
	//
	// 覆盖用户常见的几种写法：FC2-PPV-1234567、FC2PPV-1234567、FC2-1234567，
	// 以及只给商品号的 1234567（7~8 位纯数字在 av 语境下只可能是 FC2——
	// 片商番号一定带字母前缀）。归一化已经把分隔符统一成半角连字符、字母转大写，
	// 所以这里只认这一种形态。
	fc2CodeRe = regexp.MustCompile(`^(?:FC2-?(?:PPV-?)?)?(\d{6,10})$`)

	fc2OgTitleRe     = regexp.MustCompile(`(?is)<meta[^>]+property="og:title"[^>]+content="([^"]*)"`)
	fc2OgImageRe     = regexp.MustCompile(`(?is)<meta[^>]+property="og:image"[^>]+content="([^"]*)"`)
	fc2DescriptionRe = regexp.MustCompile(`(?is)<meta[^>]+name="description"[^>]+content="([^"]*)"`)
	// fc2SaleDateRe 取「販売日 : 2026/09/14」。页面把它写在一段带标记的文本里，
	// 所以允许标签夹在中间。
	fc2SaleDateRe = regexp.MustCompile(`(?is)販売日\s*(?:<[^>]+>\s*)*[：:]\s*(?:<[^>]+>\s*)*(\d{4})[/-](\d{1,2})[/-](\d{1,2})`)
	fc2TagRe      = regexp.MustCompile(`(?is)href="/search/\?tag=[^"]*"[^>]*>([^<]{1,24})</a>`)
	// fc2CodePrefixRe 把 og:title 前面那截番号剥掉，只留片名。
	fc2CodePrefixRe = regexp.MustCompile(`^\s*FC2[-\s]?PPV[-\s]?\d+\s*`)
)

// FC2WatchlistMetadataSource 只做「问 FC2 官方站要数据并映射」，不落库、不下海报。
type FC2WatchlistMetadataSource struct {
	// client 来自 NewWatchlistMetadataHTTPClient，由路由表建一次共用。
	client  *http.Client
	baseURL string
}

// NewFC2WatchlistMetadataSource 构造 FC2 适配器。client 必须来自
// NewWatchlistMetadataHTTPClient。
func NewFC2WatchlistMetadataSource(client *http.Client) *FC2WatchlistMetadataSource {
	return &FC2WatchlistMetadataSource{client: client, baseURL: fc2BaseURL}
}

func (s *FC2WatchlistMetadataSource) Name() string { return WatchlistMetadataSourceFC2 }

func fc2EnsureKind(kind WatchlistMetadataKind) error {
	if kind == WatchlistMetadataKindAV {
		return nil
	}
	return fmt.Errorf("%w：FC2 不覆盖 %q", ErrWatchlistMetadataKindUnsupported, string(kind))
}

// fc2ArticleIDOf 从归一化后的番号里取官方商品号。不是 FC2 形态时返回空串。
func fc2ArticleIDOf(code string) string {
	match := fc2CodeRe.FindStringSubmatch(strings.TrimSpace(code))
	if match == nil {
		return ""
	}
	return match[1]
}

// Search 按 FC2 番号取候选。
//
// **不是 FC2 形态的番号一律 not_found 且不发请求**——这就是「按番号形态分流」
// 的落点。判定放在适配器自己身上，而不是上层再开一张表：每个源本来就最清楚
// 自己服务哪种番号。
func (s *FC2WatchlistMetadataSource) Search(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
	if err := fc2EnsureKind(kind); err != nil {
		return nil, err
	}
	articleID := fc2ArticleIDOf(normalizeWatchlistAVCode(query))
	if articleID == "" {
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, 0,
			"不是 FC2 番号形态，本源不覆盖", nil)
	}
	detail, err := s.fetchArticle(ctx, articleID)
	if err != nil {
		return nil, err
	}
	// 商品号唯一，按它查只有一条。
	return []WatchlistMetadataCandidate{detail.WatchlistMetadataCandidate}, nil
}

// Detail 按官方商品号取详情。sourceItemID 是纯数字的商品号（4976527）。
func (s *FC2WatchlistMetadataSource) Detail(ctx context.Context, kind WatchlistMetadataKind, sourceItemID string) (*WatchlistMetadataDetail, error) {
	if err := fc2EnsureKind(kind); err != nil {
		return nil, err
	}
	articleID := fc2ArticleIDOf(normalizeWatchlistAVCode(sourceItemID))
	if articleID == "" {
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, 0,
			"不是 FC2 商品号形态，本源不覆盖", nil)
	}
	return s.fetchArticle(ctx, articleID)
}

func (s *FC2WatchlistMetadataSource) fetchArticle(ctx context.Context, articleID string) (*WatchlistMetadataDetail, error) {
	page, err := s.get(ctx, fc2ArticlePathPrefix+articleID+"/")
	if err != nil {
		return nil, err
	}
	return s.parseArticle(page, articleID)
}

// parseArticle 把商品页映射成内部结构。
//
// og:title 是最稳的锚点，它没了就说明页面结构对不上——那时报「这片不存在」
// 会把用户支去核对番号，而真正该做的是修适配器。
func (s *FC2WatchlistMetadataSource) parseArticle(page string, articleID string) (*WatchlistMetadataDetail, error) {
	rawTitle := fc2MetaValue(fc2OgTitleRe, page)
	if rawTitle == "" {
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, http.StatusOK,
			"页面结构解析失败（找不到 og:title），源站可能已改版或返回了拦截页", nil)
	}
	// og:title 形如「FC2-PPV-4976527 片名」，番号那截剥掉——它已经是用户手输的内容。
	title := strings.TrimSpace(fc2CodePrefixRe.ReplaceAllString(rawTitle, ""))
	if title == "" {
		title = rawTitle
	}

	detail := &WatchlistMetadataDetail{
		WatchlistMetadataCandidate: WatchlistMetadataCandidate{
			SourceName:   s.Name(),
			SourceItemID: articleID,
			Title:        title,
			// 官方站只有一个标题。
			OriginalTitle: title,
			Overview:      fc2Overview(page),
			Year:          fc2SaleYear(page),
			PosterURL:     fc2MetaValue(fc2OgImageRe, page),
		},
		Genres: fc2Tags(page),
	}
	return detail, nil
}

// fc2MetaValue 取一个 meta 标签的 content 并解实体。
func fc2MetaValue(pattern *regexp.Regexp, page string) string {
	match := pattern.FindStringSubmatch(page)
	if match == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(match[1]))
}

// fc2Overview 取简介。description 里同样带着番号前缀，一并剥掉。
func fc2Overview(page string) string {
	text := fc2MetaValue(fc2DescriptionRe, page)
	if text == "" {
		return ""
	}
	return strings.TrimSpace(fc2CodePrefixRe.ReplaceAllString(text, ""))
}

// fc2SaleYear 取販売日的年份。取不到留 0，不猜。
func fc2SaleYear(page string) int {
	match := fc2SaleDateRe.FindStringSubmatch(page)
	if match == nil {
		return 0
	}
	return watchlistMetadataYearOf(match[1])
}

// fc2Tags 取商品标签，去重后保序。
func fc2Tags(page string) []string {
	matches := fc2TagRe.FindAllStringSubmatch(page, -1)
	if matches == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	tags := make([]string, 0, len(matches))
	for _, match := range matches {
		tag := strings.TrimSpace(html.UnescapeString(match[1]))
		if tag == "" {
			continue
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
}

func (s *FC2WatchlistMetadataSource) get(ctx context.Context, path string) (string, error) {
	if s.client == nil {
		return "", newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "未注入出网客户端", nil)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.baseURL, "/")+path, nil)
	if err != nil {
		return "", newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "构造请求失败", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("User-Agent", fc2UserAgent)

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
		return "", newWatchlistMetadataSourceError(s.Name(), classifyWatchlistMetadataTransportError(readErr), response.StatusCode, "读取响应失败", readErr)
	}
	if response.StatusCode == http.StatusNotFound {
		return "", newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, response.StatusCode, "源站没有这个商品号", nil)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// 不走 classifyWatchlistMetadataHTTPStatus：它把 401/403 判成
		// credential_invalid，而本站不要凭证，403 只可能是拦截。
		return "", newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, response.StatusCode, "源站返回了非 2xx", nil)
	}
	return string(body), nil
}
