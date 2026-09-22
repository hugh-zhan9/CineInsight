package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
