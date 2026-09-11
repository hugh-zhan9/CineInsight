package services

import (
	"bytes"
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

// Bangumi 适配器，只覆盖 anime 类型（D-WM01）。movie / tv / show 走 TMDB，
// av 走 FANZA + JavBus，都不在本适配器里。
//
// 字段映射以 Bangumi 公开的 OpenAPI 规格（bangumi/api 仓库 open-api/v0.yaml）为准，
// **未经真实请求验证**。规格里 /v0 的四个读接口（搜索、条目、人物、角色）声明的都是
// OptionalHTTPBearer，因此本适配器在无 token 时照常发请求，见 do 的注释。

const (
	// WatchlistMetadataSourceBangumi 是写进 WatchlistEntry.SourceName 的源名。
	WatchlistMetadataSourceBangumi = "bangumi"

	bangumiAPIBaseURL = "https://api.bgm.tv"

	// bangumiUserAgent 是 Bangumi 明确要求的应用标识。
	//
	// 它不是可省的礼貌字段：Bangumi 的 API 使用规范（bangumi/api 的 docs-raw/user agent.md）
	// 点名「各请求库的默认 UA 可能被封禁」，并要求 UA 里带上开发者标识与应用名。Go 的
	// http.Client 不设 UA 时会发 "Go-http-client/1.1"，正是被点名的那一类，因此这里必须
	// 显式覆盖。取的是规范里「私有项目」那一档的形态（示例 sai/my-private-project）：
	// 开发者标识 + 应用名，不编造一个并不存在的项目主页 URL。
	//
	// 这个值改动时请保持可识别到本应用——源站是靠它来联系或限流具体调用方的。
	bangumiUserAgent = "hugh-zhan9/CineInsight"

	// bangumiSubjectTypeAnime 是 Bangumi 的条目类型枚举里的「动画」。
	// 规格里的取值为 1=书籍 2=动画 3=音乐 4=游戏 6=三次元（没有 5）。
	bangumiSubjectTypeAnime = 2

	// bangumiSearchLimit 限定单次搜索取回的候选数。候选是给用户挑的，一屏足够。
	bangumiSearchLimit = 25

	// bangumiSearchSortByMatch 保持源侧的匹配度排序。适配器合同要求「保持源给出的
	// 匹配度顺序」，这里显式声明而不是依赖服务端默认值。
	bangumiSearchSortByMatch = "match"
)

// BangumiWatchlistMetadataSource 只做「问 Bangumi 要数据并映射」，不落库、不下海报。
type BangumiWatchlistMetadataSource struct {
	// accessToken 可以为空。Bangumi 的读接口匿名可读，空 token 不阻止发请求。
	accessToken string
	// client 来自 NewWatchlistMetadataHTTPClient，由路由表建一次共用。这里不自建，
	// 否则用户配的资料源出网代理对本适配器不生效。
	client *http.Client
	// baseURL 留给测试指向桩替身；生产路径用上面的常量。
	baseURL string
}

// NewBangumiWatchlistMetadataSource 构造 Bangumi 适配器。client 必须来自
// NewWatchlistMetadataHTTPClient。accessToken 允许为空。
func NewBangumiWatchlistMetadataSource(accessToken string, client *http.Client) *BangumiWatchlistMetadataSource {
	return &BangumiWatchlistMetadataSource{
		accessToken: strings.TrimSpace(accessToken),
		client:      client,
		baseURL:     bangumiAPIBaseURL,
	}
}

func (s *BangumiWatchlistMetadataSource) Name() string { return WatchlistMetadataSourceBangumi }

// bangumiEnsureKind 守住覆盖范围：本适配器只接 anime。
//
// 路由表今天只把 anime 指过来，但适配器不能依赖这一点——它是被直接持有的对象，
// 传错类型要立刻说清楚，而不是拿动画的接口去查一部电影再返回一堆无关候选。
func bangumiEnsureKind(kind WatchlistMetadataKind) error {
	if kind == WatchlistMetadataKindAnime {
		return nil
	}
	return fmt.Errorf("%w：Bangumi 不覆盖 %q", ErrWatchlistMetadataKindUnsupported, string(kind))
}

// bangumiSearchRequest 是 POST /v0/search/subjects 的请求体。
type bangumiSearchRequest struct {
	Keyword string              `json:"keyword"`
	Sort    string              `json:"sort"`
	Filter  bangumiSearchFilter `json:"filter"`
}

// bangumiSearchFilter 只按类型收窄。
//
// 刻意不设 nsfw：成人内容有自己的源链（av → FANZA + JavBus），动画搜索没有理由去
// 动这个开关，保持源站默认即可。
type bangumiSearchFilter struct {
	Type []int `json:"type"`
}

// Search 按片名取候选。源明确表示没有收录时返回 not_found 分类的错误。
func (s *BangumiWatchlistMetadataSource) Search(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
	if err := bangumiEnsureKind(kind); err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("limit", strconv.Itoa(bangumiSearchLimit))
	params.Set("offset", "0")
	body := bangumiSearchRequest{
		Keyword: strings.TrimSpace(query),
		Sort:    bangumiSearchSortByMatch,
		Filter:  bangumiSearchFilter{Type: []int{bangumiSubjectTypeAnime}},
	}

	var payload struct {
		Data []bangumiSubject `json:"data"`
	}
	if err := s.do(ctx, http.MethodPost, "/v0/search/subjects", params, body, &payload); err != nil {
		return nil, err
	}
	if len(payload.Data) == 0 {
		// 200 加空 data 就是 Bangumi 说「没有收录」。这必须是 not_found 而不是空切片
		// 加 nil：上层要靠这个分类区别对待（D-WM07）。
		return nil, newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, http.StatusOK, "搜索无结果", nil)
	}

	candidates := make([]WatchlistMetadataCandidate, 0, len(payload.Data))
	for _, subject := range payload.Data {
		// 保持 Bangumi 给的顺序（已按 sort=match 排），这里不重排也不筛选。
		candidates = append(candidates, s.toCandidate(subject))
	}
	return candidates, nil
}

// Detail 按源条目 ID 取详情。sourceItemID 是 Bangumi 的 subject 数字 ID，
// 由同一个源的 Search 给出。
//
// 这里要发三次请求：条目本身、演职人员、角色。Bangumi 没有 TMDB 的
// append_to_response，导演在 /persons 的 relation 里，声优在 /characters 的
// actors 里，条目接口两者都不给。任一跳失败即整体失败——不做「部分成功」的降级，
// 那会让一条缺了主创的详情看起来和完整的一样。
func (s *BangumiWatchlistMetadataSource) Detail(ctx context.Context, kind WatchlistMetadataKind, sourceItemID string) (*WatchlistMetadataDetail, error) {
	if err := bangumiEnsureKind(kind); err != nil {
		return nil, err
	}
	subjectPath := "/v0/subjects/" + url.PathEscape(strings.TrimSpace(sourceItemID))

	var subject bangumiSubject
	if err := s.do(ctx, http.MethodGet, subjectPath, nil, nil, &subject); err != nil {
		return nil, err
	}
	detail := &WatchlistMetadataDetail{
		WatchlistMetadataCandidate: s.toCandidate(subject),
		Genres:                     bangumiGenresOf(subject),
	}

	var persons []bangumiRelatedPerson
	if err := s.do(ctx, http.MethodGet, subjectPath+"/persons", nil, nil, &persons); err != nil {
		return nil, err
	}
	for _, person := range persons {
		if !bangumiIsDirectorRelation(person.Relation) {
			continue
		}
		if name := strings.TrimSpace(person.Name); name != "" {
			detail.Directors = append(detail.Directors, name)
		}
	}

	var characters []bangumiRelatedCharacter
	if err := s.do(ctx, http.MethodGet, subjectPath+"/characters", nil, nil, &characters); err != nil {
		return nil, err
	}
	// 同一个声优常给多个角色配音，按角色平铺会出现重复。去重后保持首次出现的顺序
	// ——Bangumi 的角色列表是按主次排的，这个顺序对展示有意义。
	seen := make(map[string]struct{})
	for _, character := range characters {
		for _, actor := range character.Actors {
			name := strings.TrimSpace(actor.Name)
			if name == "" {
				continue
			}
			if _, duplicated := seen[name]; duplicated {
				continue
			}
			seen[name] = struct{}{}
			detail.Cast = append(detail.Cast, name)
		}
	}
	return detail, nil
}

// bangumiSubject 是 /v0 的条目结构，搜索结果与条目详情共用同一套字段。
type bangumiSubject struct {
	ID      int64  `json:"id"`
	Type    int    `json:"type"`
	Name    string `json:"name"`
	NameCN  string `json:"name_cn"`
	Summary string `json:"summary"`
	// Date 是 YYYY-MM-DD；未定档的条目可能给空串或 null。
	Date   string `json:"date"`
	Images struct {
		Large  string `json:"large"`
		Common string `json:"common"`
		Medium string `json:"medium"`
		Small  string `json:"small"`
		Grid   string `json:"grid"`
	} `json:"images"`
	Rating struct {
		Score float64 `json:"score"`
	} `json:"rating"`
	// MetaTags 是官方整理的公共标签，比用户标签干净，优先当类型标签用。
	MetaTags []string `json:"meta_tags"`
	Tags     []struct {
		Name string `json:"name"`
	} `json:"tags"`
}

// bangumiRelatedPerson 是 /v0/subjects/{id}/persons 的条目。只取用得上的两个字段。
type bangumiRelatedPerson struct {
	Name     string `json:"name"`
	Relation string `json:"relation"`
}

// bangumiRelatedCharacter 是 /v0/subjects/{id}/characters 的条目。
// 演员（声优）挂在角色下面，这是 Bangumi 给出 Cast 的唯一位置。
type bangumiRelatedCharacter struct {
	Actors []struct {
		Name string `json:"name"`
	} `json:"actors"`
}

// bangumiIsDirectorRelation 判定一条演职人员关系是不是「导演」。
//
// 两个字面量都要认：Bangumi 的中文条目用「导演」，日本动画条目沿用日文职称
// 「監督」的也很常见。其余职位（脚本、系列构成、动画制作）不算导演，不往
// Directors 里塞——那个字段的语义是导演，不是主创名单。
func bangumiIsDirectorRelation(relation string) bool {
	switch strings.TrimSpace(relation) {
	case "导演", "監督":
		return true
	default:
		return false
	}
}

// bangumiGenresOf 取类型标签。
//
// 优先用 meta_tags（官方整理的公共标签，如「科幻」「TV」），它没有用户标签里
// 「神作」「2008」这类噪声。条目冷门到没有 meta_tags 时退到 tags——这是同一份
// 数据的两种精度，不是降级路径。
func bangumiGenresOf(subject bangumiSubject) []string {
	var genres []string
	for _, tag := range subject.MetaTags {
		if name := strings.TrimSpace(tag); name != "" {
			genres = append(genres, name)
		}
	}
	if len(genres) > 0 {
		return genres
	}
	for _, tag := range subject.Tags {
		if name := strings.TrimSpace(tag.Name); name != "" {
			genres = append(genres, name)
		}
	}
	return genres
}

func (s *BangumiWatchlistMetadataSource) toCandidate(subject bangumiSubject) WatchlistMetadataCandidate {
	return WatchlistMetadataCandidate{
		SourceName: s.Name(),
		// SourceItemID 是承重字段：没有它就无法重查详情，也无法去重。
		SourceItemID: strconv.FormatInt(subject.ID, 10),
		// 界面是中文的，优先中文译名；没有译名时用原名，不留空。
		Title:         bangumiFirstNonEmpty(subject.NameCN, subject.Name),
		OriginalTitle: strings.TrimSpace(subject.Name),
		Overview:      subject.Summary,
		Year:          bangumiYearOf(subject.Date),
		Rating:        subject.Rating.Score,
		// 海报按清晰度从高到低取第一个有值的。Bangumi 给的是绝对地址，直接用。
		PosterURL: bangumiFirstNonEmpty(subject.Images.Large, subject.Images.Common, subject.Images.Medium),
	}
}

func bangumiFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// bangumiYearOf 从 YYYY-MM-DD 取年份。Bangumi 对未定档的条目给空串或 null，
// 这时返回 0——不猜一个年份出来，界面上「没有年份」比「错的年份」好。
func bangumiYearOf(date string) int {
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

// do 发一次 Bangumi 请求并把 2xx 的 JSON 解进 out，失败一律归成带分类码的
// *WatchlistMetadataSourceError。body 非 nil 时按 JSON 发出去。
//
// **与 TMDB 的关键差异：凭证为空时照发请求。** TMDB 的 api_key 是必传参数，没有它
// 出网必然失败，所以 P-003 在无凭证时直接判 credential_missing 且不发请求。Bangumi
// 不一样：规格里 /v0 的读接口声明的是 OptionalHTTPBearer，公开条目匿名可读。无 token
// 就拒发会把「本可以查到」变成一条假的配置错误。只有源自己以 401 / 403 拒绝时才归
// credential_invalid——那才是「它确实要凭证」的证据。
func (s *BangumiWatchlistMetadataSource) do(ctx context.Context, method string, path string, params url.Values, body any, out any) error {
	if s.client == nil {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "未注入出网客户端", nil)
	}

	endpoint := strings.TrimRight(s.baseURL, "/") + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "构造请求体失败", s.redact(err))
		}
		payload = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, 0, "构造请求失败", s.redact(err))
	}
	request.Header.Set("Accept", "application/json")
	// UA 必须显式设置，Go 默认的 "Go-http-client/1.1" 正是 Bangumi 点名可能封禁的形态。
	request.Header.Set("User-Agent", bangumiUserAgent)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if s.accessToken != "" {
		request.Header.Set("Authorization", "Bearer "+s.accessToken)
	}

	started := time.Now()
	response, err := s.client.Do(request)
	if err != nil {
		failure := classifyWatchlistMetadataTransportError(err)
		log.Printf("[WatchlistMetadata] source=%s path=%s elapsed_ms=%d failure=%s",
			s.Name(), path, time.Since(started).Milliseconds(), failure)
		return newWatchlistMetadataSourceError(s.Name(), failure, 0, "请求未能送达", s.redact(err))
	}
	defer response.Body.Close()

	raw, readErr := io.ReadAll(response.Body)
	log.Printf("[WatchlistMetadata] source=%s path=%s elapsed_ms=%d status=%d bytes=%d",
		s.Name(), path, time.Since(started).Milliseconds(), response.StatusCode, len(raw))
	if readErr != nil {
		// 响应读到一半断了是连接层的问题，不是源返回了坏数据。
		return newWatchlistMetadataSourceError(s.Name(), classifyWatchlistMetadataTransportError(readErr), response.StatusCode, "读取响应失败", s.redact(readErr))
	}

	if response.StatusCode == http.StatusNotFound {
		// Bangumi 对不存在的条目就是 404，这是它「明确说没有」的形态。
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureNotFound, response.StatusCode, bangumiErrorMessage(raw), nil)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return newWatchlistMetadataSourceError(s.Name(), classifyWatchlistMetadataHTTPStatus(response.StatusCode), response.StatusCode, bangumiErrorMessage(raw), nil)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return newWatchlistMetadataSourceError(s.Name(), WatchlistMetadataFailureSourceError, response.StatusCode, "响应不是预期的 JSON", s.redact(err))
	}
	return nil
}

// redact 抹掉错误文案里的 access token。
//
// token 走的是 Authorization 头而不是 query，*url.Error 带不出来，但这里照样抹：
// 成本是一次字符串检查，而漏掉一条泄漏路径的代价是把用户凭证抄进日志。
func (s *BangumiWatchlistMetadataSource) redact(err error) error {
	return redactWatchlistMetadataSecret(err, s.accessToken)
}

// bangumiErrorMessage 取 Bangumi 错误体里的可读原因。/v0 的错误形态是
// {"title": ..., "description": ..., "details": ...}，前两个字段是源站自己的文案，
// 不含用户凭证，可以安全带进错误里；details 可能回显请求内容，不取。
func bangumiErrorMessage(raw []byte) string {
	var payload struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return bangumiFirstNonEmpty(payload.Description, payload.Title)
}
