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

// TMDB 适配器，覆盖 movie / tv / show 三个类型（D-WM01）。anime 走 Bangumi、
// av 走 FANZA + JavBus，都不在本适配器里。
//
// 字段映射以 TMDB 公开的 v3 文档为准，**未经真实凭证验证**。

const (
	// WatchlistMetadataSourceTMDB 是写进 WatchlistEntry.SourceName 的源名。
	WatchlistMetadataSourceTMDB = "tmdb"

	tmdbAPIBaseURL = "https://api.themoviedb.org/3"
	// tmdbImageBaseURL 按官方文档应当取自 /configuration，但该前缀多年未变，
	// 为一个常量多发一次请求并不划算。改版时这里会整体失效而不是悄悄错一半。
	tmdbImageBaseURL = "https://image.tmdb.org/t/p/w500"
	// 界面是中文的，取中文条目；TMDB 在缺中文时会回退到原始语言。
	tmdbLanguage = "zh-CN"
)

// TMDBWatchlistMetadataSource 只做「问 TMDB 要数据并映射」，不落库、不下海报。
type TMDBWatchlistMetadataSource struct {
	apiKey string
	// client 来自 NewWatchlistMetadataHTTPClient，由路由表建一次共用。这里不自建，
	// 否则用户配的资料源出网代理对本适配器不生效。
	client *http.Client
	// baseURL / imageBaseURL 留给测试指向桩替身；生产路径用上面的常量。
	baseURL      string
	imageBaseURL string
}

// NewTMDBWatchlistMetadataSource 构造 TMDB 适配器。client 必须来自
// NewWatchlistMetadataHTTPClient。
func NewTMDBWatchlistMetadataSource(apiKey string, client *http.Client) *TMDBWatchlistMetadataSource {
	return &TMDBWatchlistMetadataSource{
		apiKey:       strings.TrimSpace(apiKey),
		client:       client,
		baseURL:      tmdbAPIBaseURL,
		imageBaseURL: tmdbImageBaseURL,
	}
}

func (s *TMDBWatchlistMetadataSource) Name() string { return WatchlistMetadataSourceTMDB }

// tmdbSegmentFor 把类型映射到 TMDB 的接口段。
//
// tv 与 show 都落到 /tv：TMDB 没有「综艺纪录片」这一顶层类型，综艺在它那里就是
// 剧集。代价明确：纪录**电影**若被用户归到 show，只会在剧集库里搜，搜不到就是
// not_found。这是本切片已知的映射取舍，不是缺陷掩盖。
func tmdbSegmentFor(kind WatchlistMetadataKind) (string, error) {
	switch kind {
	case WatchlistMetadataKindMovie:
		return "movie", nil
	case WatchlistMetadataKindTV, WatchlistMetadataKindShow:
		return "tv", nil
	default:
		return "", fmt.Errorf("%w：TMDB 不覆盖 %q", ErrWatchlistMetadataKindUnsupported, string(kind))
	}
}

// Search 按片名取候选。源明确没有收录时返回 not_found 分类的错误。
func (s *TMDBWatchlistMetadataSource) Search(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
	segment, err := tmdbSegmentFor(kind)
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("query", query)
	params.Set("page", "1")
	if kind == WatchlistMetadataKindMovie {
		// 成人内容不走 TMDB——av 有自己的源链。
		params.Set("include_adult", "false")
	}

	var payload struct {
		Results []tmdbItem `json:"results"`
	}
	if err := s.get(ctx, "/search/"+segment, params, &payload); err != nil {
		return nil, err
	}
	if len(payload.Results) == 0 {
		// 200 加空结果就是 TMDB 说「没有收录」。这必须是 not_found 而不是空切片
		// 加 nil：上层要靠这个分类区别对待（D-WM07）。
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, http.StatusOK, "搜索无结果", nil)
	}

	candidates := make([]WatchlistMetadataCandidate, 0, len(payload.Results))
	for _, item := range payload.Results {
		// 保持 TMDB 给的顺序，它已经是按匹配度排的；这里不重排也不筛选。
		candidates = append(candidates, s.toCandidate(item))
	}
	return candidates, nil
}

// Detail 按源条目 ID 取详情。sourceItemID 是 TMDB 的数字 ID，由同一个源的 Search 给出。
func (s *TMDBWatchlistMetadataSource) Detail(ctx context.Context, kind WatchlistMetadataKind, sourceItemID string) (*WatchlistMetadataDetail, error) {
	segment, err := tmdbSegmentFor(kind)
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	// 一次把演职员带回来，省一轮往返。
	params.Set("append_to_response", "credits")

	var payload tmdbItem
	if err := s.get(ctx, "/"+segment+"/"+url.PathEscape(strings.TrimSpace(sourceItemID)), params, &payload); err != nil {
		return nil, err
	}

	detail := &WatchlistMetadataDetail{WatchlistMetadataCandidate: s.toCandidate(payload)}
	for _, genre := range payload.Genres {
		if name := strings.TrimSpace(genre.Name); name != "" {
			detail.Genres = append(detail.Genres, name)
		}
	}
	// 电影的导演在 crew 里，剧集的主创在 created_by 里；两处都取，同一个条目只会命中一处。
	for _, member := range payload.Credits.Crew {
		if member.Job == "Director" {
			if name := strings.TrimSpace(member.Name); name != "" {
				detail.Directors = append(detail.Directors, name)
			}
		}
	}
	for _, creator := range payload.CreatedBy {
		if name := strings.TrimSpace(creator.Name); name != "" {
			detail.Directors = append(detail.Directors, name)
		}
	}
	for _, member := range payload.Credits.Cast {
		if name := strings.TrimSpace(member.Name); name != "" {
			detail.Cast = append(detail.Cast, name)
		}
	}
	return detail, nil
}

// tmdbItem 同时覆盖电影与剧集两种形态：TMDB 给电影 title / release_date，
// 给剧集 name / first_air_date。一个结构体两套字段，取非空的那套。
type tmdbItem struct {
	ID            int64   `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	Name          string  `json:"name"`
	OriginalName  string  `json:"original_name"`
	Overview      string  `json:"overview"`
	ReleaseDate   string  `json:"release_date"`
	FirstAirDate  string  `json:"first_air_date"`
	VoteAverage   float64 `json:"vote_average"`
	PosterPath    string  `json:"poster_path"`
	Genres        []struct {
		Name string `json:"name"`
	} `json:"genres"`
	CreatedBy []struct {
		Name string `json:"name"`
	} `json:"created_by"`
	Credits struct {
		Cast []struct {
			Name string `json:"name"`
		} `json:"cast"`
		Crew []struct {
			Name string `json:"name"`
			Job  string `json:"job"`
		} `json:"crew"`
	} `json:"credits"`
}

func (s *TMDBWatchlistMetadataSource) toCandidate(item tmdbItem) WatchlistMetadataCandidate {
	candidate := WatchlistMetadataCandidate{
		SourceName: s.Name(),
		// SourceItemID 是承重字段：没有它就无法重查详情，也无法去重。
		SourceItemID:  strconv.FormatInt(item.ID, 10),
		Title:         tmdbFirstNonEmpty(item.Title, item.Name),
		OriginalTitle: tmdbFirstNonEmpty(item.OriginalTitle, item.OriginalName),
		Overview:      item.Overview,
		Year:          tmdbYearOf(tmdbFirstNonEmpty(item.ReleaseDate, item.FirstAirDate)),
		Rating:        item.VoteAverage,
	}
	if path := strings.TrimSpace(item.PosterPath); path != "" {
		candidate.PosterURL = strings.TrimRight(s.imageBaseURL, "/") + "/" + strings.TrimLeft(path, "/")
	}
	return candidate
}

func tmdbFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// tmdbYearOf 从 YYYY-MM-DD 取年份。TMDB 对未定档的条目给空串，这时返回 0——
// 不猜一个年份出来，界面上「没有年份」比「错的年份」好。
func tmdbYearOf(date string) int {
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

// get 发一次 TMDB 请求并把 2xx 的 JSON 解进 out，失败一律归成带分类码的
// *WatchlistMetadataSourceError。
func (s *TMDBWatchlistMetadataSource) get(ctx context.Context, path string, params url.Values, out any) error {
	if s.apiKey == "" {
		// credential_missing 的定义就包含「不发请求」（D-WM14）：凭证为空时出网
		// 没有任何可能成功，只会把一次配置问题变成一条真实的对外记录。
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureCredentialMissing, 0, "未配置 TMDB 凭证", nil)
	}
	if s.client == nil {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "未注入出网客户端", nil)
	}

	query := url.Values{}
	for key, values := range params {
		query[key] = values
	}
	query.Set("api_key", s.apiKey)
	query.Set("language", tmdbLanguage)
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
		// 日志只记路径，不记完整 URL——api_key 在 query 里。
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
		// TMDB 对不存在的条目就是 404（status_code 34），这是它「明确说没有」的形态。
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, response.StatusCode, tmdbStatusMessage(body), nil)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return newWatchlistMetadataSourceError(s.Name(), classifyWatchlistMetadataHTTPStatus(response.StatusCode), response.StatusCode, tmdbStatusMessage(body), nil)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, response.StatusCode, "响应不是预期的 JSON", s.redact(err))
	}
	return nil
}

// redact 抹掉错误文案里的 api_key。*url.Error 会把整个请求 URL 带进 Error()，
// 而 TMDB 的凭证就在 query 里，原样冒泡等于把它抄进日志和界面。
func (s *TMDBWatchlistMetadataSource) redact(err error) error {
	return redactWatchlistMetadataSecret(err, s.apiKey)
}

// tmdbStatusMessage 取 TMDB 错误体里的 status_message 当作可读原因。它是 TMDB
// 自己的文案（如 "Invalid API key"），不含用户凭证，可以安全带进错误里。
func tmdbStatusMessage(body []byte) string {
	var payload struct {
		StatusMessage string `json:"status_message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.StatusMessage)
}
