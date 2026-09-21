package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"video-master/database"
	"video-master/models"
	"video-master/services"
)

func newAssetHandler(app *App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/preview/person-avatar/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			app.servePersonAvatar(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/preview/collection-cover/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			app.serveCollectionCover(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/preview/watchlist-poster/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			app.serveWatchlistPoster(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, doubanChartPosterPathPrefix) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			app.serveDoubanChartPoster(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/preview/face-crop/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			app.serveFaceCrop(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/preview/image-thumbnail/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			app.serveImageThumbnail(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/preview/image/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			app.serveImageView(w, r)
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			if strings.HasPrefix(r.URL.Path, "/preview/media/") {
				app.servePreviewMedia(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/preview/thumbnail/") {
				app.serveThumbnail(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/preview/seek-sprite/") {
				app.serveSeekSprite(w, r)
				return
			}
		}

		http.NotFound(w, r)
	})
}

func (a *App) serveCollectionCover(w http.ResponseWriter, r *http.Request) {
	collectionID, err := assetVideoIDFromPath(r.URL.Path, "/preview/collection-cover/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	asset, err := a.collectionService.ResolveCollectionCover(collectionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "collection cover not found", http.StatusNotFound)
			return
		}
		http.Error(w, "collection cover unavailable", http.StatusInternalServerError)
		return
	}
	file, err := os.Open(asset.Path)
	if err != nil {
		http.Error(w, "collection cover not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", asset.MIME)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, asset.DisplayName, asset.ModTime, file)
}

func (a *App) servePersonAvatar(w http.ResponseWriter, r *http.Request) {
	personID, err := assetVideoIDFromPath(r.URL.Path, "/preview/person-avatar/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	asset, err := a.personService.ResolvePersonAvatar(personID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "person avatar not found", http.StatusNotFound)
			return
		}
		http.Error(w, "person avatar unavailable", http.StatusInternalServerError)
		return
	}
	file, err := os.Open(asset.Path)
	if err != nil {
		http.Error(w, "person avatar not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", asset.MIME)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, asset.DisplayName, asset.ModTime, file)
}

// serveWatchlistPoster 取想看条目补全下来的海报（D-WM09），形态照
// servePersonAvatar：条目不存在、没有海报、文件已丢都是 404，其余是 500。
func (a *App) serveWatchlistPoster(w http.ResponseWriter, r *http.Request) {
	entryID, err := assetVideoIDFromPath(r.URL.Path, "/preview/watchlist-poster/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	asset, err := a.watchlistService.ResolveWatchlistPoster(entryID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "watchlist poster not found", http.StatusNotFound)
			return
		}
		http.Error(w, "watchlist poster unavailable", http.StatusInternalServerError)
		return
	}
	file, err := os.Open(asset.Path)
	if err != nil {
		http.Error(w, "watchlist poster not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", asset.MIME)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, asset.DisplayName, asset.ModTime, file)
}

// 年度电影榜单的海报代理（需求设计文档 §6.2，D-MC09）。
//
// 为什么要这条路由：豆瓣图床拒绝盗链（2026-09-21 实测）——不带 Referer 回 418，
// 带 http://localhost:34115/ 或 wails://wails/ 这类外来源回 403，只有图片自身站点
// 根（或 douban.com 源）才回 200 image/jpeg。所以 webview 里裸写 <img src> 取不到图，
// 字节必须由 Go 这一侧取。海报**不落盘**（用户裁决），因此这里是即时代理。
// 体积上限与 User-Agent 都直接用 services 侧的导出别名，不在这里另存一份：
// services.ManagedImageMaxBytes 是托管图片的 20 MiB 上限，
// services.WatchlistPosterUserAgent 是各抓取型适配器共用的浏览器标识
// （Go 默认的 "Go-http-client/1.1" 在豆瓣图床上基本等于自报家门）。抄一份字面量
// 过来的话，两处迟早各自漂移。
const (
	doubanChartPosterPathPrefix = "/preview/douban-chart-poster/"

	// doubanChartPosterRootHost 是域名白名单的根域：主机必须**恰好**等于它，
	// 或以 "."+它 结尾。写成两段判断而不是一句 HasSuffix(host, "doubanio.com")，
	// 因为后者会把 evildoubanio.com 一起放进来。
	doubanChartPosterRootHost = "doubanio.com"

	// doubanChartPosterTimeout 与想看片单海报下载同值：回来的是几百 KB 到几 MB 的
	// 图片，还可能绕一圈出网代理，按 JSON 查询的短超时会误杀。
	doubanChartPosterTimeout = 60 * time.Second

	// doubanChartPosterMaxRedirects 是跟随跳转的上限，见 doubanChartPosterRedirectPolicy。
	doubanChartPosterMaxRedirects = 5
)

// doubanChartPosterClientProvider 是取图客户端的唯一来源，测试把它换成指向桩服务的
// 客户端（本仓库不对 douban.com / doubanio.com 发真实请求）。
//
// 它替换的只是「请求怎么发出去」，**不是「允许请求谁」**：地址一律先过
// validateDoubanChartPosterURL 的白名单，注入客户端绕不过那一步，测试里存进库的
// 地址也仍然是真实的 https://img2.doubanio.com/... 形态。
var doubanChartPosterClientProvider = defaultDoubanChartPosterClient

var (
	doubanChartPosterClientMu    sync.Mutex
	doubanChartPosterClient      *http.Client
	doubanChartPosterClientProxy string
)

// defaultDoubanChartPosterClient 按当前设置装配出网客户端，并按代理地址缓存复用。
//
// 客户端必须来自 NewWatchlistMetadataHTTPClient：自建 http.Client 会绕过用户配置的
// 资料源出网代理，配了代理的用户会以为请求走了代理、其实是裸奔出去的。
// 缓存的理由是每请求新建一个 http.Client 等于每张海报重做一次 TLS 握手，而每个
// Transport 自带的连接池不会随请求结束回收——一页榜单几十张图就能堆出几十个池。
// 代理地址一变就重建，设置改完立即生效。
func defaultDoubanChartPosterClient() (*http.Client, error) {
	config := services.LoadWatchlistMetadataConfig()
	doubanChartPosterClientMu.Lock()
	defer doubanChartPosterClientMu.Unlock()
	if doubanChartPosterClient != nil && doubanChartPosterClientProxy == config.ProxyURL {
		return doubanChartPosterClient, nil
	}
	client, err := services.NewWatchlistMetadataHTTPClient(config, doubanChartPosterTimeout)
	if err != nil {
		return nil, err
	}
	client.CheckRedirect = doubanChartPosterRedirectPolicy
	if doubanChartPosterClient != nil {
		doubanChartPosterClient.CloseIdleConnections()
	}
	doubanChartPosterClient = client
	doubanChartPosterClientProxy = config.ProxyURL
	return client, nil
}

// serveDoubanChartPoster 按豆瓣 ID 代理一张榜单海报。
//
// 这条路由**不接受调用方给的任何地址**：入参只有路径里的豆瓣 ID，真正要取的地址
// 从本地库里查出来。这是它不成为 SSRF 跳板的唯一依据，查询串、请求头一概不参与。
// 库里的值来自豆瓣响应，仍属外部输入，因此取出来之后还要再过一次域名白名单。
func (a *App) serveDoubanChartPoster(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	doubanID, err := doubanChartPosterIDFromPath(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	stored, err := lookupDoubanChartPosterURL(doubanID)
	if err != nil {
		logDoubanChartPosterFailure(doubanID, 0, 0, started, "lookup_failed")
		http.Error(w, "chart poster unavailable", http.StatusInternalServerError)
		return
	}
	address, referer, ok := validateDoubanChartPosterURL(stored)
	if !ok {
		// 对调用方，「库里没有地址」与「地址没过白名单」是同一个答复：没有这张图。
		// 但对我们不是——后者说明缓存里存着一个抓取侧本不该写进来的地址，是数据
		// 完整性信号；而且那是一道安全控制真的触发了，不记就没人知道它响过。
		// 前者是寻常的空结果，照旧不记。
		if stored != "" {
			// 只记豆瓣 ID。被拒的地址正是此刻最可疑的那个字符串，也正因为如此不能
			// 把它回显进日志文件；ID 足够定位到是哪一行。
			logDoubanChartPosterFailure(doubanID, 0, 0, started, "address_rejected")
		}
		http.Error(w, "chart poster not found", http.StatusNotFound)
		return
	}
	client, err := doubanChartPosterClientProvider()
	if err != nil {
		// 多半是资料源出网代理地址填错。不退回直连。
		logDoubanChartPosterFailure(doubanID, 0, 0, started, "client_unavailable")
		http.Error(w, "chart poster unavailable", http.StatusInternalServerError)
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, address, nil)
	if err != nil {
		logDoubanChartPosterFailure(doubanID, 0, 0, started, "request_invalid")
		http.Error(w, "chart poster unavailable", http.StatusInternalServerError)
		return
	}
	// 末尾的斜杠是必须的：图床认 https://img2.doubanio.com/，不认少一个斜杠的形态
	// （与 services/watchlist_artwork.go 的 validateWatchlistPosterURL 同一条踩坑）。
	request.Header.Set("Referer", referer)
	request.Header.Set("User-Agent", services.WatchlistPosterUserAgent)
	request.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")

	response, err := client.Do(request)
	if err != nil {
		// 跳转被白名单拦下也落在这里（reason 仍是 transport_failed，错误对象不记，
		// 它会带上被拒的那个地址）。
		logDoubanChartPosterFailure(doubanID, 0, 0, started, "transport_failed")
		http.Error(w, "chart poster upstream failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		logDoubanChartPosterFailure(doubanID, response.StatusCode, 0, started, "upstream_status")
		http.Error(w, "chart poster upstream failed", http.StatusBadGateway)
		return
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if !doubanChartPosterServableImage(contentType) {
		// 防盗链页、验证页与错误页都会以 200 + text/html 回来，它们不是图；
		// SVG 是图，但它带脚本，见 doubanChartPosterServableImage。
		logDoubanChartPosterFailure(doubanID, response.StatusCode, 0, started, "not_image")
		http.Error(w, "chart poster upstream failed", http.StatusBadGateway)
		return
	}
	// 多读一个字节就够判「超了」。先整段读进内存再落响应，是因为一旦开始往
	// ResponseWriter 写就改不了状态码，超限只能截断成一张坏图。
	body, err := io.ReadAll(io.LimitReader(response.Body, services.ManagedImageMaxBytes+1))
	if err != nil {
		logDoubanChartPosterFailure(doubanID, response.StatusCode, len(body), started, "read_failed")
		http.Error(w, "chart poster upstream failed", http.StatusBadGateway)
		return
	}
	if int64(len(body)) > services.ManagedImageMaxBytes {
		logDoubanChartPosterFailure(doubanID, response.StatusCode, len(body), started, "oversize")
		http.Error(w, "chart poster upstream failed", http.StatusBadGateway)
		return
	}
	if len(body) == 0 {
		logDoubanChartPosterFailure(doubanID, response.StatusCode, 0, started, "empty_body")
		http.Error(w, "chart poster upstream failed", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", contentType)
	// 上游的 Content-Type 是原样透传的，所以必须禁掉浏览器的类型嗅探：没有这一行，
	// 一份伪装成 image/jpeg 的 HTML 仍可能被当成文档渲染，而这条路由与应用自己的
	// 页面同源。配合上面只放行位图，两道一起挡住「从本应用源上执行外来内容」。
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 海报不落盘（D-MC09），webview 的 HTTP 缓存是唯一挡住「每次滚动都重取」的东西。
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// doubanChartPosterServableImage 判定这份 Content-Type 能不能当海报发出去。
//
// 只放行位图：必须是 image/*，且**明确排除 image/svg+xml**。SVG 是图片类型，但它
// 能内嵌 <script>，而这条路由的响应与应用自己的页面同源——一张被换掉的「海报」
// 就成了同源脚本执行点。海报是 CDN 上的 jpeg/webp/png，排除 SVG 不损失任何真实用例。
// 前提是上游被攻破或被劫持，所以这是纵深防御，不是主控制。
//
// 参数带 ;charset= 之类的参数段，判定只看分号前的媒体类型。
func doubanChartPosterServableImage(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(contentType))
	if index := strings.IndexByte(mediaType, ';'); index >= 0 {
		mediaType = strings.TrimSpace(mediaType[:index])
	}
	if !strings.HasPrefix(mediaType, "image/") {
		return false
	}
	return mediaType != "image/svg+xml"
}

// doubanChartPosterRedirectPolicy 让跳转目标受同一条白名单约束。
//
// 没有它，白名单只管得住第一跳：上游回一个 302 指向 http://169.254.169.254/ 时，
// http.Client 默认会跟过去，这条路由就成了一个由上游操纵的跳板。跳转目标按同一个
// validateDoubanChartPosterURL 判定，不满足就断掉（调用方看到的是 502）。
func doubanChartPosterRedirectPolicy(request *http.Request, via []*http.Request) error {
	if len(via) >= doubanChartPosterMaxRedirects {
		return fmt.Errorf("海报跳转次数过多")
	}
	if _, _, ok := validateDoubanChartPosterURL(request.URL.String()); !ok {
		return fmt.Errorf("海报跳转目标不在白名单内")
	}
	return nil
}

// logDoubanChartPosterFailure 是这条路由唯一的诊断出口，**只在失败时**记一行。
//
// 为什么这条路由要记而邻居们不记：邻居服务的是本应用自己写到本地磁盘的文件，失败
// 基本只有「文件没了」一种，路径还在请求里；这条路由取的是第三方 CDN（带防盗链）、
// 中间可能隔着用户自配的出网代理，而海报按裁决不落盘——每一种失败都发生在远端且
// 不留痕迹，不记的话用户报「封面不显示」就无从查起。成功路径不记：一页榜单几十张
// 图，那是纯噪声。
//
// 字段照 services/watchlist_metadata_douban.go:164 的既有形状。**不记海报地址、
// 不记查询串、不记响应正文**：各资料源适配器一律把出网地址当作不可记录的东西，
// 这里守同一条规矩，省得日后逐条推敲哪个地址算例外。status=0 表示请求还没发出去
// 或压根没拿到响应。
func logDoubanChartPosterFailure(doubanID string, status, bytes int, started time.Time, reason string) {
	log.Printf("[MovieChart] source=douban_poster douban_id=%s status=%d bytes=%d elapsed_ms=%d reason=%s",
		doubanID, status, bytes, time.Since(started).Milliseconds(), reason)
}

// doubanChartPosterIDFromPath 取路径里的豆瓣 ID，只接受 ^[0-9]{1,16}$。
//
// 不复用 assetVideoIDFromPath：豆瓣 ID 在库里是字符串主键，ParseUint 会把 "007"
// 规范成 7，查不到那一行。手写判定同时把 "../"、查询串残留和超长值挡在解析这一步。
func doubanChartPosterIDFromPath(path string) (string, error) {
	doubanID := strings.TrimPrefix(path, doubanChartPosterPathPrefix)
	if doubanID == path {
		return "", fmt.Errorf("invalid asset path")
	}
	if doubanID == "" || len(doubanID) > 16 {
		return "", fmt.Errorf("invalid asset id")
	}
	for index := 0; index < len(doubanID); index++ {
		if doubanID[index] < '0' || doubanID[index] > '9' {
			return "", fmt.Errorf("invalid asset id")
		}
	}
	return doubanID, nil
}

// lookupDoubanChartPosterURL 先查榜单缓存、再查标记表。
//
// 标记表是有意的第二跳：已看页在用户清掉缓存、或该条目从豆瓣下架之后仍然要有图，
// 标记行里的 poster_url 是标记那一刻的快照。两处都没有非空地址就是「没有这张图」，
// 由调用方回 404。返回的 error 只表示数据库本身出了问题。
func lookupDoubanChartPosterURL(doubanID string) (string, error) {
	if database.DB == nil {
		return "", fmt.Errorf("database unavailable")
	}
	var entryURLs []string
	if err := database.DB.Model(&models.MovieChartEntry{}).
		Where("douban_id = ?", doubanID).Limit(1).
		Pluck("poster_url", &entryURLs).Error; err != nil {
		return "", err
	}
	if len(entryURLs) > 0 && strings.TrimSpace(entryURLs[0]) != "" {
		return entryURLs[0], nil
	}
	var markURLs []string
	if err := database.DB.Model(&models.MovieChartMark{}).
		Where("douban_id = ?", doubanID).Limit(1).
		Pluck("poster_url", &markURLs).Error; err != nil {
		return "", err
	}
	if len(markURLs) > 0 && strings.TrimSpace(markURLs[0]) != "" {
		return markURLs[0], nil
	}
	return "", nil
}

// validateDoubanChartPosterURL 是白名单本身：只放行 https 且主机落在 doubanio.com
// 之下的地址，第二个返回值是发请求时用的 Referer（图片自身站点根，带末尾斜杠）。
//
// 三处刻意收紧：
//   - 主机判定分成「恰好等于」与「以 . 开头的后缀」两支，裸 HasSuffix 会放行
//     evildoubanio.com；
//   - 带用户名密码的地址（https://user:pass@host/）一律拒，凭证形态既无必要，
//     也是绕主机判定的常见手法；
//   - 显式端口一律拒。真实豆瓣图床地址不带端口，允许它只会多出一条把请求引到
//     同域其他端口的路径。
func validateDoubanChartPosterURL(raw string) (string, string, bool) {
	address := strings.TrimSpace(raw)
	if address == "" {
		return "", "", false
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return "", "", false
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return "", "", false
	}
	if parsed.User != nil {
		return "", "", false
	}
	if parsed.Port() != "" {
		return "", "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != doubanChartPosterRootHost && !strings.HasSuffix(host, "."+doubanChartPosterRootHost) {
		return "", "", false
	}
	return address, "https://" + host + "/", true
}

func (a *App) serveThumbnail(w http.ResponseWriter, r *http.Request) {
	videoID, err := assetVideoIDFromPath(r.URL.Path, "/preview/thumbnail/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	media, err := a.thumbnailService.ResolveThumbnail(r.Context(), videoID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "thumbnail not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("thumbnail unavailable: %v", err), http.StatusInternalServerError)
		return
	}
	file, err := os.Open(media.Path)
	if err != nil {
		http.Error(w, "thumbnail not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, filepath.Base(media.Path), media.ModTime, file)
}

func (a *App) serveSeekSprite(w http.ResponseWriter, r *http.Request) {
	videoID, err := assetVideoIDFromPath(r.URL.Path, "/preview/seek-sprite/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	media, err := a.thumbnailService.ResolveSeekSprite(r.Context(), videoID)
	if err != nil {
		if errors.Is(err, services.ErrSeekSpriteNotReady) || errors.Is(err, os.ErrNotExist) {
			http.Error(w, "seek sprite not found", http.StatusNotFound)
			return
		}
		http.Error(w, "seek sprite unavailable", http.StatusInternalServerError)
		return
	}
	file, err := os.Open(media.Path)
	if err != nil {
		http.Error(w, "seek sprite not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, filepath.Base(media.Path), media.ModTime, file)
}

// serveFaceCrop 是人脸裁剪图的受控路由（D-020）。
//
// 两道边界：路径里只接受纯数字的观测 id（assetVideoIDFromPath 会把 `..` 挡在
// ParseUint 上），而库里的 crop_path 由服务侧再校验一次必须落在 faces 目录内。
// 裁剪图不进任何缓存头之外的地方，也不带 Content-Disposition。
func (a *App) serveFaceCrop(w http.ResponseWriter, r *http.Request) {
	observationID, err := assetVideoIDFromPath(r.URL.Path, "/preview/face-crop/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	asset, err := a.faceAnalysis.ResolveFaceCrop(observationID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "face crop not found", http.StatusNotFound)
			return
		}
		http.Error(w, "face crop unavailable", http.StatusInternalServerError)
		return
	}
	file, err := os.Open(asset.Path)
	if err != nil {
		http.Error(w, "face crop not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", asset.MIME)
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, filepath.Base(asset.Path), asset.ModTime, file)
}

func (a *App) serveImageThumbnail(w http.ResponseWriter, r *http.Request) {
	imageID, err := assetVideoIDFromPath(r.URL.Path, "/preview/image-thumbnail/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	media, err := a.imageThumbnail.ResolveImageThumbnail(r.Context(), imageID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, services.ErrImageDecodeUnsupported) {
			http.Error(w, "image thumbnail not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("image thumbnail unavailable: %v", err), http.StatusInternalServerError)
		return
	}
	file, err := os.Open(media.Path)
	if err != nil {
		http.Error(w, "image thumbnail not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, filepath.Base(media.Path), media.ModTime, file)
}

func (a *App) serveImageView(w http.ResponseWriter, r *http.Request) {
	imageID, err := assetVideoIDFromPath(r.URL.Path, "/preview/image/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	media, err := a.imageThumbnail.ResolveImageView(r.Context(), imageID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, services.ErrImageDecodeUnsupported) {
			http.Error(w, "image not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("image unavailable: %v", err), http.StatusInternalServerError)
		return
	}
	file, err := os.Open(media.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "image not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("open image failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer file.Close()
	if media.MIME != "" {
		w.Header().Set("Content-Type", media.MIME)
	}
	http.ServeContent(w, r, filepath.Base(media.Path), media.ModTime, file)
}

func (a *App) servePreviewMedia(w http.ResponseWriter, r *http.Request) {
	videoID, err := previewVideoIDFromPath(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	media, err := a.videoService.ResolvePreviewMedia(videoID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "preview media not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("preview media unavailable: %v", err), http.StatusInternalServerError)
		return
	}

	file, err := os.Open(media.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "preview media not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("open preview media failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer file.Close()

	if media.MIME != "" {
		w.Header().Set("Content-Type", media.MIME)
	}

	http.ServeContent(w, r, media.DisplayName, media.ModTime, file)
}

func previewVideoIDFromPath(path string) (uint, error) {
	return assetVideoIDFromPath(path, "/preview/media/")
}

func assetVideoIDFromPath(path, prefix string) (uint, error) {
	videoIDText := strings.TrimPrefix(path, prefix)
	if videoIDText == "" || videoIDText == path {
		return 0, fmt.Errorf("invalid asset path")
	}

	videoID, err := strconv.ParseUint(videoIDText, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid asset id")
	}

	return uint(videoID), nil
}
