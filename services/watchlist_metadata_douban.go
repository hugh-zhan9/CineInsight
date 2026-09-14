package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const WatchlistMetadataSourceDouban = "douban"

var doubanSubjectID = regexp.MustCompile(`^[0-9]{1,16}$`)

// DoubanWatchlistMetadataSource 使用豆瓣网页的公开搜索与移动端详情接口。
// 无需凭证；它们不是有稳定性承诺的开放 API。验证页、限流和未知响应均报
// source_error，只有明确无条目才允许走 TMDB 兜底。客户端由统一工厂注入。
type DoubanWatchlistMetadataSource struct {
	client        *http.Client
	searchBaseURL string
	detailBaseURL string
}

func NewDoubanWatchlistMetadataSource(client *http.Client) *DoubanWatchlistMetadataSource {
	return &DoubanWatchlistMetadataSource{client: client, searchBaseURL: "https://movie.douban.com", detailBaseURL: "https://m.douban.com"}
}
func (s *DoubanWatchlistMetadataSource) Name() string { return WatchlistMetadataSourceDouban }
func doubanEnsureKind(kind WatchlistMetadataKind) error {
	if kind != WatchlistMetadataKindMovie {
		return fmt.Errorf("%w：豆瓣当前只覆盖电影", ErrWatchlistMetadataKindUnsupported)
	}
	return nil
}

func (s *DoubanWatchlistMetadataSource) Search(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
	if err := doubanEnsureKind(kind); err != nil {
		return nil, err
	}
	var items []struct {
		ID            string `json:"id"`
		Title         string `json:"title"`
		OriginalTitle string `json:"sub_title"`
		Year          string `json:"year"`
		Image         string `json:"img"`
		Type          string `json:"type"`
		Episode       string `json:"episode"`
	}
	if err := s.get(ctx, s.searchBaseURL, "/j/subject_suggest", url.Values{"q": {query}}, &items); err != nil {
		return nil, err
	}
	// null 不是搜索空结果的合同（真实空结果是 []），不能把异常伪装成未收录。
	if items == nil {
		return nil, s.invalidResponse("搜索响应不是候选数组")
	}
	candidates := make([]WatchlistMetadataCandidate, 0, len(items))
	for _, item := range items {
		// suggest 把剧集的 type 也写成 movie，以 episode 排除已知剧集。
		// 详情还会按 is_tv / type 二次确认，避免缺集数字段的剧集被当电影写入。
		if item.Type != "movie" || (item.Episode != "" && item.Episode != "0") {
			continue
		}
		if !doubanSubjectID.MatchString(item.ID) || strings.TrimSpace(item.Title) == "" {
			return nil, s.invalidResponse("搜索结果缺少有效的条目 ID 或片名")
		}
		candidates = append(candidates, WatchlistMetadataCandidate{SourceName: s.Name(), SourceItemID: item.ID, Title: item.Title, OriginalTitle: item.OriginalTitle, Year: watchlistMetadataYearOf(item.Year), PosterURL: item.Image})
	}
	if len(candidates) == 0 {
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, http.StatusOK, "豆瓣未找到电影候选", nil)
	}
	return candidates, nil
}

func (s *DoubanWatchlistMetadataSource) Detail(ctx context.Context, kind WatchlistMetadataKind, sourceItemID string) (*WatchlistMetadataDetail, error) {
	if err := doubanEnsureKind(kind); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(sourceItemID)
	if !doubanSubjectID.MatchString(id) {
		return nil, s.invalidResponse("无效的豆瓣条目 ID")
	}
	var item struct {
		ID            string `json:"id"`
		Title         string `json:"title"`
		OriginalTitle string `json:"original_title"`
		Year          string `json:"year"`
		Intro         string `json:"intro"`
		Type          string `json:"type"`
		IsTV          bool   `json:"is_tv"`
		CoverURL      string `json:"cover_url"`
		Pic           struct {
			Large  string `json:"large"`
			Normal string `json:"normal"`
		} `json:"pic"`
		Rating struct {
			Value float64 `json:"value"`
		} `json:"rating"`
		Genres    []string `json:"genres"`
		Directors []struct {
			Name string `json:"name"`
		} `json:"directors"`
		Actors []struct {
			Name string `json:"name"`
		} `json:"actors"`
	}
	if err := s.get(ctx, s.detailBaseURL, "/rexxar/api/v2/movie/"+id, nil, &item); err != nil {
		return nil, err
	}
	if item.ID != id || strings.TrimSpace(item.Title) == "" {
		return nil, s.invalidResponse("详情缺少片名或条目 ID 不匹配")
	}
	if item.IsTV || item.Type == "tv" {
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, http.StatusOK, "该豆瓣条目是剧集，不属于电影", nil)
	}
	if item.Type != "movie" {
		return nil, s.invalidResponse("详情不是预期的电影数据")
	}
	detail := &WatchlistMetadataDetail{WatchlistMetadataCandidate: WatchlistMetadataCandidate{SourceName: s.Name(), SourceItemID: id, Title: item.Title, OriginalTitle: item.OriginalTitle, Year: watchlistMetadataYearOf(item.Year), Overview: item.Intro, Rating: item.Rating.Value, PosterURL: tmdbFirstNonEmpty(item.Pic.Large, item.CoverURL, item.Pic.Normal)}, Genres: item.Genres}
	for _, person := range item.Directors {
		if name := strings.TrimSpace(person.Name); name != "" {
			detail.Directors = append(detail.Directors, name)
		}
	}
	for _, person := range item.Actors {
		if name := strings.TrimSpace(person.Name); name != "" {
			detail.Cast = append(detail.Cast, name)
		}
	}
	return detail, nil
}

func (s *DoubanWatchlistMetadataSource) invalidResponse(detail string) error {
	return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, http.StatusOK, detail, nil)
}

func (s *DoubanWatchlistMetadataSource) get(ctx context.Context, base, path string, params url.Values, out any) error {
	if s.client == nil {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "未注入出网客户端", nil)
	}
	endpoint := strings.TrimRight(base, "/") + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "构造请求失败", err)
	}
	req.Header.Set("User-Agent", watchlistPosterUserAgent)
	req.Header.Set("Referer", strings.TrimRight(base, "/")+"/")
	req.Header.Set("Accept", "application/json")
	started := time.Now()
	response, err := s.client.Do(req)
	if err != nil {
		return newWatchlistMetadataSourceError(s.Name(), classifyWatchlistMetadataTransportError(err), 0, "请求未能送达", err)
	}
	defer response.Body.Close()
	// 只记路由、状态和计数，不记录搜索词、响应正文或验证页的查询参数。
	const maxBytes = 2 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	log.Printf("[WatchlistMetadata] source=douban path=%s status=%d bytes=%d elapsed_ms=%d", path, response.StatusCode, len(body), time.Since(started).Milliseconds())
	if err != nil {
		return newWatchlistMetadataSourceError(s.Name(), classifyWatchlistMetadataTransportError(err), response.StatusCode, "读取响应失败", err)
	}
	requested, _ := url.Parse(base)
	if response.Request.URL.Host != requested.Host {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, response.StatusCode, "豆瓣要求访问验证，暂时无法读取资料", nil)
	}
	// 只有移动详情接口明确的业务 404 才是缺失；搜索接口 404 可能是接口变更。
	if response.StatusCode == http.StatusNotFound && strings.HasPrefix(path, "/rexxar/api/v2/movie/") {
		var missing struct {
			Code    int    `json:"code"`
			Message string `json:"msg"`
		}
		if json.Unmarshal(body, &missing) == nil && missing.Code == 404 && missing.Message == "traversal_error" {
			return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, response.StatusCode, "豆瓣未收录该条目或条目已移除", nil)
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, response.StatusCode, "豆瓣拒绝请求或暂时不可用", nil)
	}
	if len(body) > maxBytes {
		return s.invalidResponse("豆瓣响应超过大小上限")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return s.invalidResponse("豆瓣未返回预期数据，可能需要访问验证或接口已变更")
	}
	return nil
}
