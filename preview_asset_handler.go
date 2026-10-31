package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"video-master/services"

	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"gorm.io/gorm"
)

func newAssetHandler(app *App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/preview/bt-poster/") {
			app.serveBTPoster(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/preview/queue/") {
			app.serveQueueMedia(w, r)
			return
		}
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
			if strings.HasPrefix(r.URL.Path, "/preview/video-artwork/") {
				app.serveVideoArtwork(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/preview/seek-sprite/") {
				app.serveSeekSprite(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/preview/frame/") {
				app.serveFrameAt(w, r)
				return
			}
		}

		http.NotFound(w, r)
	})
}

// startupStateMiddleware 挂在 Wails 资源链最外层（AssetServer.Middleware）：首页 index.html 由 Wails 从内嵌资源
// 直接送出，到不了 newAssetHandler，所以在这一层改。只改首页：在 <html> 上写明这次的启动状态（data-startup，
// 见 startupState），页面第一帧就按它定外壳表面，不必等 GetStartupError（D-UD-02）。启动状态在 wails.Run
// 之前就定了（数据库在 main 里打开）。其余请求原样交给 next，不缓冲。
func startupStateMiddleware(app *App) assetserver.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || (r.URL.Path != "/" && r.URL.Path != "/index.html") {
				next.ServeHTTP(w, r)
				return
			}
			page := &indexPageRecorder{header: w.Header(), code: http.StatusOK}
			next.ServeHTTP(page, r)
			body := page.body.Bytes()
			if page.code == http.StatusOK {
				body = withStartupState(body, app.startupState())
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			}
			w.WriteHeader(page.code)
			_, _ = w.Write(body)
		})
	}
}

// startupState 是写进首页的启动状态：ready 正常启动（默认页 home 在观看区，影院表面）；error 启动失败页
// （含回落保护拦下，D-DBG-03），跟随系统主题。页面内容仍以 GetStartupError 为准。
func (a *App) startupState() string {
	if a.startupError != "" {
		return "error"
	}
	return "ready"
}

var htmlStartTag = regexp.MustCompile(`(?i)<html[\s>]`)

// withStartupState 在首页第一个 <html> 标签上加 data-startup；没有这个标签时原样返回，前端照旧等 GetStartupError。
func withStartupState(page []byte, state string) []byte {
	loc := htmlStartTag.FindIndex(page)
	if loc == nil {
		return page
	}
	at := loc[0] + len("<html")
	attr := ` data-startup="` + state + `"`
	out := make([]byte, 0, len(page)+len(attr))
	out = append(out, page[:at]...)
	out = append(out, attr...)
	return append(out, page[at:]...)
}

// indexPageRecorder 收下 next 写出的首页，改完再一次写出；响应头直接落在外层 ResponseWriter 上。
type indexPageRecorder struct {
	header      http.Header
	code        int
	wroteHeader bool
	body        bytes.Buffer
}

func (p *indexPageRecorder) Header() http.Header { return p.header }

func (p *indexPageRecorder) WriteHeader(code int) {
	if p.wroteHeader {
		return
	}
	p.code, p.wroteHeader = code, true
}

func (p *indexPageRecorder) Write(b []byte) (int, error) {
	p.WriteHeader(http.StatusOK)
	return p.body.Write(b)
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

// 年度电影榜单的海报（需求设计文档 §6.2，D-MC14 取代 D-MC09）。
//
// 这条路由**只读本地磁盘，零出网**，实现里连一个 HTTP 客户端都没有——这不是
// 性能取舍，是本切片的全部意义。它原先是即时代理：每次渲染都去豆瓣图床取一次，
// 只靠 Cache-Control: max-age=604800 挡重复请求。那个头**从来没有生效过**：
// 海报经 Wails 的 AssetServer.Handler（main.go:41）即自定义协议提供，WKWebView
// 不对自定义协议响应做 HTTP 缓存。于是 2026-09-22 真机上豆瓣图床开始回 403/418。
//
// 海报现在由详情补全 worker 随详情一并落盘（services/movie_chart_artwork.go），
// 那里继承了已有的 1 次/秒限速、取消与断点续跑。这里命中就发文件，未命中回 404
// 让前端显示占位，**不即时回源**——在渲染路径上回源正是被限流的那件事。
// 出网侧的域名白名单、逐跳重定向复检、Referer 与 User-Agent 一并搬到了下载侧，
// 一样都没丢。
const doubanChartPosterPathPrefix = "/preview/douban-chart-poster/"

// serveDoubanChartPoster 按豆瓣 ID 发一张**已落盘**的海报。
//
// 入参只有路径里的豆瓣 ID：查询串、请求头一概不参与，路由也不接受调用方给的任何
// 地址——这条从 D-MC09 起就是它不成为 SSRF 跳板的依据，落盘之后更进一步，压根
// 没有出网件可供跳转。
func (a *App) serveDoubanChartPoster(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	doubanID, err := doubanChartPosterIDFromPath(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	asset, err := a.movieChartService().ResolveChartPoster(doubanID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 「还没下到」「下失败了」「被磁盘上限淘汰了」对调用方是同一个答复：
			// 现在没有这张图，显示占位。下一轮补全会把它补回来。
			http.Error(w, "chart poster not found", http.StatusNotFound)
			return
		}
		logDoubanChartPosterFailure(doubanID, started, "resolve_failed")
		http.Error(w, "chart poster unavailable", http.StatusInternalServerError)
		return
	}
	file, err := os.Open(asset.Path)
	if err != nil {
		// 解析到发送之间文件被删掉了（多半是 LRU 整理刚好插在中间）。
		http.Error(w, "chart poster not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", asset.MIME)
	// 类型来自本地嗅探，且只可能是 JPEG / PNG / WebP（ManagedImageService.Resolve
	// 只认这三种）。nosniff 仍然带着：这些字节来自第三方 CDN，而这条路由的响应与
	// 应用自己的页面同源，纵深防御比省一个头值钱。
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 与人物头像、合集封面、想看海报同口径：文件就在本地，路径又是按豆瓣 ID 给的
	// （不带内容摘要），缓存住只会在换图之后发旧图。
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, asset.DisplayName, asset.ModTime, file)
}

// logDoubanChartPosterFailure 是这条路由唯一的诊断出口，**只在真故障时**记一行。
//
// 落盘之后「没有这张图」是寻常结果（还没补到、补失败、被淘汰），不记——一页榜单
// 几十张卡片，那会是纯噪声。真正值得记的只剩一种：库或托管目录本身出了问题。
// 下载侧的失败由 services/movie_chart_refresh.go 的海报阶段各记各的。
//
// 字段照 services/watchlist_metadata_douban.go:164 的既有形状。**不记海报地址、
// 不记查询串**：各资料源适配器一律把出网地址当作不可记录的东西，这里守同一条规矩。
func logDoubanChartPosterFailure(doubanID string, started time.Time, reason string) {
	log.Printf("[MovieChart] source=douban_poster douban_id=%s elapsed_ms=%d reason=%s",
		doubanID, time.Since(started).Milliseconds(), reason)
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

func (a *App) serveThumbnail(w http.ResponseWriter, r *http.Request) {
	videoID, err := assetVideoIDFromPath(r.URL.Path, "/preview/thumbnail/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.writeVideoThumbnail(w, r, videoID)
}

// serveVideoArtwork 发 NFO 导入时存下的海报或背景图（D-UI-06、D-UI-12）：
// /preview/video-artwork/{videoID}?kind=poster|fanart。
//
// 海报找不到（没导入过、文件已不在）时直接发与 /preview/thumbnail/{id} 相同的缩略图，
// 并带 X-Artwork-Fallback: thumbnail 便于诊断——卡片因此总能拿到一张图。背景图找不到是 404，
// 前端先看详情里的 has_fanart 再请求。kind 缺省或非法是 400，视频不存在是 404。
// 缓存头与缩略图一致，Last-Modified 取图片文件的修改时间。
func (a *App) serveVideoArtwork(w http.ResponseWriter, r *http.Request) {
	videoID, err := assetVideoIDFromPath(r.URL.Path, "/preview/video-artwork/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "poster" && kind != "fanart" {
		http.Error(w, "invalid artwork kind", http.StatusBadRequest)
		return
	}
	asset, err := a.localMetadata.ResolveVideoArtworkAsset(videoID, kind)
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			http.Error(w, "video not found", http.StatusNotFound)
		case errors.Is(err, os.ErrNotExist) && kind == "poster":
			w.Header().Set("X-Artwork-Fallback", "thumbnail")
			a.writeVideoThumbnail(w, r, videoID)
		case errors.Is(err, os.ErrNotExist):
			http.Error(w, "artwork not found", http.StatusNotFound)
		default:
			http.Error(w, "artwork unavailable", http.StatusInternalServerError)
		}
		return
	}
	file, err := os.Open(asset.Path)
	if err != nil {
		http.Error(w, "artwork not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", asset.MIME)
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, asset.DisplayName, asset.ModTime, file)
}

// writeVideoThumbnail 是缩略图路由的发送部分，海报回落也走这里，两条路由发出的是同一张图。
func (a *App) writeVideoThumbnail(w http.ResponseWriter, r *http.Request, videoID uint) {
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

// serveFrameAt 返回某视频某毫秒的单帧 JPEG（/preview/frame/{id}?ms=&w=），
// 供场景检索命中与视频工作台切点预览。只接受纯数字 id 与非负整数毫秒。
func (a *App) serveFrameAt(w http.ResponseWriter, r *http.Request) {
	videoID, err := assetVideoIDFromPath(r.URL.Path, "/preview/frame/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ms, err := strconv.ParseInt(r.URL.Query().Get("ms"), 10, 64)
	if err != nil || ms < 0 {
		http.Error(w, "invalid ms", http.StatusBadRequest)
		return
	}
	width := 0
	if raw := r.URL.Query().Get("w"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 0 {
			http.Error(w, "invalid width", http.StatusBadRequest)
			return
		}
		width = parsed
	}
	data, modTime, err := a.thumbnailService.RenderFrameAt(r.Context(), videoID, ms, width)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrFrameAtOutOfRange):
			http.Error(w, "frame out of range", http.StatusNotFound)
		case errors.Is(err, os.ErrNotExist), errors.Is(err, gorm.ErrRecordNotFound):
			http.Error(w, "frame not found", http.StatusNotFound)
		default:
			http.Error(w, "frame unavailable", http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, "frame.jpg", modTime, bytes.NewReader(data))
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
