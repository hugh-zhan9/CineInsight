package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"video-master/models"
)

// 年度电影榜单的海报落盘（D-MC14，取代 D-MC09）。
//
// 为什么改成落盘：海报原先由只读路由即时回源，指望 Cache-Control 挡住重复请求。
// 那条缓解手段**从一开始就没生效**——海报经 Wails 的 AssetServer.Handler 即自定义
// 协议提供，WKWebView 不对自定义协议响应做 HTTP 缓存，于是每次渲染都真的出一次网，
// 2026-09-22 真机上豆瓣图床开始回 403/418。
//
// 现在的分工是硬的：
//
//   - **下载只发生在详情补全 worker 里**（movie_chart_refresh.go 的海报阶段），
//     因此自动继承它已有的 1 次/秒限速、取消与断点续跑，不另起一套调度；
//   - **渲染路径零出网**：只读路由只认本地文件，命中就发、未命中就 404
//     （前端显示占位），**不即时回源**。
//
// 从只读路由搬过来、不许在搬家途中掉的三样东西，都在本文件里：
// 域名白名单（只放行 https + doubanio.com）、逐跳重定向复检、以及取图时的
// Referer（图片自身站点根，**带末尾斜杠**）与浏览器 User-Agent。

const (
	// movieChartPosterRootHost 是域名白名单的根域：主机必须**恰好**等于它，或以
	// "."+它 结尾。写成两段判断而不是一句 HasSuffix(host, "doubanio.com")，
	// 因为后者会把 evildoubanio.com 一起放进来。
	movieChartPosterRootHost = "doubanio.com"

	// movieChartPosterMaxRedirects 是跟随跳转的上限，见 movieChartPosterRedirectPolicy。
	movieChartPosterMaxRedirects = 5

	// movieChartPosterEntity 是托管图片的实体类型，落到
	// media-details/movie_chart/<条目 id>/<sha256>.<ext>。
	movieChartPosterEntity = "movie_chart"

	// movieChartPosterCacheMaxBytes 是海报缓存的磁盘上限（用户裁决：1 GiB）。
	//
	// 算一下就知道它有多宽：一年的榜单约 1500 条、单张海报几百 KB，浏览一整年
	// 大约 150 MB，1 GiB 因此够六到七个年份，**淘汰在正常使用下几乎不会发生**。
	// 这正是想要的——淘汰是防失控的兜底，不是常态：一旦成为常态，用户看到的就是
	// 海报时有时无、翻回去又得重下，而重下意味着重新出网，恰好是本切片要消灭的
	// 那件事。
	movieChartPosterCacheMaxBytes int64 = 1 << 30
)

// ErrMovieChartPosterAddressRejected 表示库里存的海报地址没过白名单。
//
// 与网络失败分开：它说明缓存里存着一个抓取侧本不该写进来的地址，是数据完整性
// 信号，也是一道安全控制真的触发了，日志要认得出来。
var ErrMovieChartPosterAddressRejected = errors.New("海报地址不在白名单内")

// newMovieChartPosterClient 按当前设置装配取图客户端。
//
// 必须走 NewWatchlistPosterHTTPClient：自建 http.Client 会绕过用户配置的资料源
// 出网代理，配了代理的用户会以为请求走了代理、其实是裸奔出去的；代理地址填错时
// 它返回错误而**不退回直连**（D-MC01）。超时按海报用途给（60 秒）：回来的是几百
// KB 到几 MB 的图片，还可能绕一圈代理，按 JSON 查询的短超时会误杀。
//
// 每轮刷新装配一次（与 newSource 同口径），所以用户改完代理设置下一轮即生效。
func newMovieChartPosterClient() (*http.Client, error) {
	client, err := NewWatchlistPosterHTTPClient(LoadWatchlistMetadataConfig())
	if err != nil {
		return nil, err
	}
	client.CheckRedirect = movieChartPosterRedirectPolicy
	return client, nil
}

// movieChartPosterRedirectPolicy 让跳转目标受同一条白名单约束。
//
// 没有它，白名单只管得住第一跳：上游回一个 302 指向 http://169.254.169.254/ 时，
// http.Client 默认会跟过去，取图就成了一个由上游操纵的跳板。
func movieChartPosterRedirectPolicy(request *http.Request, via []*http.Request) error {
	if len(via) >= movieChartPosterMaxRedirects {
		return fmt.Errorf("海报跳转次数过多")
	}
	if _, _, ok := validateMovieChartPosterURL(request.URL.String()); !ok {
		return fmt.Errorf("海报跳转目标不在白名单内")
	}
	return nil
}

// validateMovieChartPosterURL 是白名单本身：只放行 https 且主机落在 doubanio.com
// 之下的地址，第二个返回值是发请求时用的 Referer（图片自身站点根，带末尾斜杠）。
//
// 三处刻意收紧：
//   - 主机判定分成「恰好等于」与「以 . 开头的后缀」两支，裸 HasSuffix 会放行
//     evildoubanio.com；
//   - 带用户名密码的地址（https://user:pass@host/）一律拒，凭证形态既无必要，
//     也是绕主机判定的常见手法；
//   - 显式端口一律拒。真实豆瓣图床地址不带端口，允许它只会多出一条把请求引到
//     同域其他端口的路径。
//
// 地址只从本地库取、从不接受调用方传入（D-MC09 留下的这一条不变），但库里的值
// 来自豆瓣响应，仍属外部输入，所以取出来之后还要再过这一道。
func validateMovieChartPosterURL(raw string) (string, string, bool) {
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
	if host != movieChartPosterRootHost && !strings.HasSuffix(host, "."+movieChartPosterRootHost) {
		return "", "", false
	}
	// 末尾的斜杠是必须的：图床认 https://img2.doubanio.com/，不认少一个斜杠的
	// 形态（与 validateWatchlistPosterURL 同一条踩坑，2026-09-21 实测 403）。
	return address, "https://" + host + "/", true
}

// posterImages 返回托管图片服务，没有注入时为 nil（调用方各自判）。
func (s *MovieChartService) posterImages() *ManagedImageService {
	if s == nil {
		return nil
	}
	return s.images
}

// ResolveChartPoster 按豆瓣 ID 取一张**已落盘**的海报，供只读路由使用。
//
// 这条路径上没有任何出网件，连客户端都取不到：库里没有 poster_path、文件不在、
// 或者内容不是位图，一律 os.ErrNotExist，由路由回 404、前端显示占位。
// **不回源**——即时回源正是 2026-09-22 把客户端打进豆瓣黑名单的那件事。
func (s *MovieChartService) ResolveChartPoster(doubanID string) (ManagedImageAsset, error) {
	if s == nil {
		return ManagedImageAsset{}, errors.New("年度榜单服务不可用")
	}
	images := s.posterImages()
	if images == nil {
		return ManagedImageAsset{}, errors.New("托管图片服务不可用")
	}
	var paths []string
	if err := s.db.Model(&models.MovieChartEntry{}).
		Where("douban_id = ?", doubanID).Limit(1).
		Pluck("poster_path", &paths).Error; err != nil {
		return ManagedImageAsset{}, fmt.Errorf("读取榜单海报路径失败: %w", err)
	}
	if len(paths) == 0 || strings.TrimSpace(paths[0]) == "" {
		return ManagedImageAsset{}, os.ErrNotExist
	}
	return images.Resolve(paths[0])
}

// storeEntryPoster 下一张海报并把托管相对路径写回条目。
//
// **顺序是承重的**（与 WatchlistService.DownloadPoster 同一条）：落盘与写库不在
// 同一个原子单元里，所以先落盘、再写库；写库失败（或守卫不成立）就把刚落盘的图
// 删掉。反过来会留下一个指向不存在文件的 poster_path，而那正是前端永远拿不到图、
// 却每次渲染都要发一次必然 404 的请求的形态。
//
// Import 是内容寻址的：同样的字节重复落盘不产生新文件，返回值的 Created 为 false。
// **那种情况下不许删**——那张图很可能正是别的行（或本行的上一次成功）在用的那一张，
// 删掉就把一张有人引用的图删了。代价是极少数情况下留一个没人引用的孤儿文件，
// 它对渲染无害，下一次磁盘整理会把它收走。
//
// 返回错误只用于日志与测试断言：海报失败**不影响条目本身**，调用方照常往下跑。
func (s *MovieChartService) storeEntryPoster(ctx context.Context, client *http.Client, entryID uint, posterURL string) error {
	images := s.posterImages()
	if images == nil {
		return errors.New("托管图片服务不可用")
	}
	imported, size, err := downloadMovieChartPoster(ctx, client, images, entryID, posterURL)
	if err != nil {
		return err
	}
	written, writeErr := s.writeBackPosterPath(ctx, entryID, imported.RelativePath)
	if writeErr != nil || !written {
		// 回滚：只删本次真正新建出来的文件。
		if imported.Created {
			if removeErr := images.Remove(imported.RelativePath); removeErr != nil {
				log.Printf("[MovieChart] rollback poster id=%d failed err=%v", entryID, removeErr)
			}
		}
		if writeErr != nil {
			return fmt.Errorf("写回海报路径失败: %w", writeErr)
		}
		return errors.New("写回海报路径时条目已变（守卫不成立）")
	}
	s.prunePosterCache(imported, size)
	return nil
}

// writeBackPosterPath 带守卫写回托管路径，返回是否真的写进去了。
//
// 守卫是 poster_path 仍为空：这一列的空串就是「这条还缺图」，非空即已有别的
// 写入者放了一张图进去，此刻不能覆盖——覆盖会让那张图变成没人引用的孤儿。
// 影响 0 行不是错误，调用方按回滚处理（删掉本次落盘的文件）。
func (s *MovieChartService) writeBackPosterPath(ctx context.Context, entryID uint, relativePath string) (bool, error) {
	result := s.db.WithContext(ctx).Model(&models.MovieChartEntry{}).
		Where("id = ? AND poster_path = ?", entryID, "").
		Update("poster_path", relativePath)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// downloadMovieChartPoster 把海报下到临时文件，再交 ManagedImageService.Import
// 落到 media-details/movie_chart/<条目 id>/<sha256>.<ext>。
//
// client 传 nil 直接失败，不退回 http.DefaultClient：静默直连会让配了代理的用户
// 以为请求走了代理，而它其实是从本机裸奔出去的。
func downloadMovieChartPoster(ctx context.Context, client *http.Client, images *ManagedImageService, entryID uint, posterURL string) (managedImageImport, int64, error) {
	if client == nil {
		return managedImageImport{}, 0, errors.New("海报下载缺少 HTTP 客户端")
	}
	address, referer, ok := validateMovieChartPosterURL(posterURL)
	if !ok {
		return managedImageImport{}, 0, ErrMovieChartPosterAddressRejected
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return managedImageImport{}, 0, fmt.Errorf("构造海报请求失败: %w", err)
	}
	// 防盗链：豆瓣图床对不带 Referer 的请求回 418，对外来源（localhost、wails://）
	// 回 403，只有图片自身站点根放行（2026-09-21 实测）。末尾斜杠必须带。
	request.Header.Set("Referer", referer)
	// Go 默认的 "Go-http-client/1.1" 在这类源站上基本等于自报家门。
	request.Header.Set("User-Agent", watchlistPosterUserAgent)
	request.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")
	response, err := client.Do(request)
	if err != nil {
		return managedImageImport{}, 0, fmt.Errorf("下载海报失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// 403 / 418 就落在这里：记一条，条目本身不受影响。
		return managedImageImport{}, 0, fmt.Errorf("下载海报失败: HTTP %d", response.StatusCode)
	}

	temporary, err := os.CreateTemp("", "movie-chart-poster-*")
	if err != nil {
		return managedImageImport{}, 0, fmt.Errorf("创建海报临时文件失败: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	// 多读一个字节：读满上限 +1 就说明响应超限，直接拒，不把超限内容交给 Import
	// 再判一次。下载侧必须先有界，否则一个损坏或恶意的响应能把任意大小读进临时文件。
	written, err := io.Copy(temporary, io.LimitReader(response.Body, watchlistPosterMaxBytes+1))
	if err != nil {
		return managedImageImport{}, 0, fmt.Errorf("写入海报临时文件失败: %w", err)
	}
	if written > watchlistPosterMaxBytes {
		return managedImageImport{}, 0, fmt.Errorf("%w（%d 字节）", ErrWatchlistPosterTooLarge, watchlistPosterMaxBytes)
	}
	if written == 0 {
		return managedImageImport{}, 0, errors.New("海报响应为空")
	}
	if err := temporary.Close(); err != nil {
		return managedImageImport{}, 0, fmt.Errorf("关闭海报临时文件失败: %w", err)
	}
	// 格式的最终判据在 Import：只认 JPEG / PNG / WebP。防盗链页与验证页是
	// 200 + text/html，到这里会被判成格式不符而拒，落不进托管目录；SVG 同理
	// （它是图，但能内嵌脚本，而海报最终与应用页面同源渲染）。
	imported, err := images.Import(movieChartPosterEntity, entryID, temporaryPath)
	if err != nil {
		return managedImageImport{}, 0, err
	}
	// 第二个返回值是这张图落盘后的字节数（内容寻址，文件内容就是这段字节），
	// 供磁盘上限做增量记账，省掉「每存一张就把整棵托管目录树走一遍」。
	return imported, written, nil
}

// prunePosterCache 按磁盘上限做一次 LRU 整理，形态照
// ImageThumbnailService.pruneImageCacheLocked：按 mtime 从旧到新淘汰，直到总量
// 回到上限之下，**刚落盘的那一张（imported）永不淘汰**——不然一次存盘可以把自己
// 刚写的行清掉、文件删掉，然后报成功，条目从此缺图而没有任何痕迹。
//
// 与那一处的两点不同：
//
//  1. 这里的文件被数据库行引用着，所以淘汰一张要做两件事，而且顺序与落盘**正好
//     相反**：先清 poster_path，再删文件。
//
//     落盘：先文件、后库行——库行写不成功，留下的是一个没人引用的文件；
//     淘汰：先库行、后文件——库行清不掉就别删文件，两边仍然自洽。
//
//     反过来（先删文件再清库行）一旦在中间失败，就会留下一行指向不存在文件的
//     poster_path：has_poster 仍是真，前端每次渲染都发一次必然 404 的请求，而且
//     再也不会被补图（补图的取件条件是 poster_path 为空）——那是永久的破图。
//     反之，一个没有库行引用的文件只是垃圾，下一次整理就把它收走。
//
//  2. **不是每存一张就走一遍目录树**。缩略图那边的目录是扁的、上限也常触发；
//     这里是 movie_chart/<条目 id>/ 的两层树，几个年份就是上万个文件，而 1 GiB
//     的上限几乎永不触发——「每秒一次全量 WalkDir + 每文件一次 Info()」全花在
//     一个不会发生的判断上。改成增量记账：进程内第一次量一遍，之后每存一张按
//     字节数累加，**只有累加到超过上限才真的走一遍目录树**（那一遍同时把总数
//     校准回磁盘的真实值）。
//
// 记账只由刷新 worker 触碰，而同一时刻只有一轮刷新（startRound 拒绝并发，且上一轮
// 收尾与下一轮开始都经过 s.mu，天然有 happens-before），所以它不需要自己的锁。
// 记账会漂移（外部删文件、回滚删图都不通知它），漂移只影响「什么时候去量一遍」，
// 每次真正淘汰时都会重新校准，进程重启也会重量。
//
// 不为淘汰单独记「最近使用时间」：读路径是纯文件读，若要记就得在渲染路径上写库，
// 那与「渲染路径只读」冲突。mtime 因此近似于「落盘时间」，淘汰顺序是最旧的先走。
func (s *MovieChartService) prunePosterCache(imported managedImageImport, size int64) {
	images := s.posterImages()
	if images == nil {
		return
	}
	limit := s.posterCacheLimit
	if limit <= 0 {
		return
	}
	if s.posterCacheBytes < 0 {
		// 本进程第一次落盘：量一遍打底。
		total, err := s.measurePosterCache(images)
		if err != nil {
			return
		}
		s.posterCacheBytes = total
	} else if imported.Created {
		// 内容寻址命中已有文件（Created 为假）时磁盘没有变大，不能累加。
		s.posterCacheBytes += size
	}
	if s.posterCacheBytes <= limit {
		return
	}

	files, err := images.listEntityFiles(movieChartPosterEntity)
	if err != nil {
		log.Printf("[MovieChart] list poster cache failed err=%v", err)
		return
	}
	var total int64
	for _, file := range files {
		total += file.Size
	}
	// 走了一遍目录树，顺手把记账校准回磁盘的真实值。
	s.posterCacheBytes = total
	if total <= limit {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModTime.Before(files[j].ModTime) })
	evicted := 0
	for _, file := range files {
		if total <= limit {
			break
		}
		if file.RelativePath == imported.RelativePath {
			continue
		}
		// 先清库行。清不掉就跳过这一张，文件留着——留一张多余的图，比留一行
		// 指向空的路径便宜得多。
		if err := s.clearPosterPath(file.RelativePath); err != nil {
			log.Printf("[MovieChart] clear poster path failed path=%s err=%v", file.RelativePath, err)
			continue
		}
		if err := images.Remove(file.RelativePath); err != nil {
			log.Printf("[MovieChart] remove poster failed path=%s err=%v", file.RelativePath, err)
			continue
		}
		total -= file.Size
		evicted++
	}
	s.posterCacheBytes = total
	if evicted > 0 {
		log.Printf("[MovieChart] poster cache pruned evicted=%d remaining_bytes=%d limit=%d",
			evicted, total, limit)
	}
}

// measurePosterCache 量一遍托管目录里海报的字节总数。
func (s *MovieChartService) measurePosterCache(images *ManagedImageService) (int64, error) {
	files, err := images.listEntityFiles(movieChartPosterEntity)
	if err != nil {
		log.Printf("[MovieChart] list poster cache failed err=%v", err)
		return 0, err
	}
	var total int64
	for _, file := range files {
		total += file.Size
	}
	return total, nil
}

// clearPosterPath 把引用某个托管路径的条目退回「缺图」状态。
//
// SET 右侧是参数，不是列引用：Postgres 里不带限定的列名在某些 SET 右侧是歧义的
// （42702），这条语句刻意不给它机会。
func (s *MovieChartService) clearPosterPath(relativePath string) error {
	return s.db.Model(&models.MovieChartEntry{}).
		Where("poster_path = ?", relativePath).
		Update("poster_path", "").Error
}
