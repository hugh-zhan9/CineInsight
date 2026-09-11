package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// 海报下载的总超时。比资料查询长是有理由的：查询回来的是一小段 JSON，海报是几百
// KB 到几 MB 的图片，还可能经出网代理绕一圈。
const watchlistPosterDownloadTimeout = 60 * time.Second

// 下载侧的字节上限，取与 ManagedImageService 相同的 20 MiB。
//
// Import 自己也有这道上限，但它是**读完之后**才判的；下载侧必须先有界，否则一个
// 损坏或恶意的响应能把任意大小读进临时文件。两道上限取同一个值：下载侧多读一个
// 字节就够判「超了」，超出部分不必落盘。
const watchlistPosterMaxBytes = managedImageMaxBytes

// ErrWatchlistPosterTooLarge 标记「响应超过下载上限」。与格式不符、网络失败分开，
// 调用方据此给用户「海报太大」而不是一句笼统的下载失败。
var ErrWatchlistPosterTooLarge = errors.New("海报超过下载体积上限")

// NewWatchlistPosterHTTPClient 给海报下载建客户端。
//
// 它只是把 P-002 的工厂按海报用途定死超时——海报下载同样是外部请求，自建
// http.Client 会绕过用户配的资料源出网代理（AC-06）。返回的客户端自带连接池，
// 调用方建一次反复用；补全 worker 已经有一个用于资料查询的客户端时，直接把那个
// 传给 DownloadPoster 即可，不必再建。
func NewWatchlistPosterHTTPClient(config WatchlistMetadataConfig) (*http.Client, error) {
	return NewWatchlistMetadataHTTPClient(config, watchlistPosterDownloadTimeout)
}

// DownloadPoster 把 posterURL 下到临时文件，再交 ManagedImageService.Import 落到
// media-details/watchlist/<entryID>/<sha256>.<ext>，返回托管相对路径。
//
// 调用顺序是承重的（需求设计文档 §3.2 的回滚边界）：写回是单条 UPDATE，与海报落盘
// 不在同一原子单元，所以**必须先落盘、再写回数据库**；写回失败时用 RemovePoster
// 删掉刚落盘的图片。反过来会留下指向不存在文件的路径。Import 内容寻址，重复落盘
// 同一张图不产生新文件，返回值的 Created 为 false——那种情况下**不要**删图，它就是
// 条目当前已经在用的那张。
//
// client 必须由 NewWatchlistPosterHTTPClient（或补全链路共用的那个客户端）提供。
// 传 nil 直接失败，不退回 http.DefaultClient：静默直连会让配了代理的用户以为请求
// 走了代理，而它其实是从本机裸奔出去的。
//
// 下载失败只影响海报：调用方照常保留已经拿到的文字字段，条目只是没有图。
func (s *WatchlistService) DownloadPoster(ctx context.Context, client *http.Client, entryID uint, posterURL string) (managedImageImport, error) {
	if client == nil {
		return managedImageImport{}, errors.New("海报下载缺少 HTTP 客户端")
	}
	address, err := validateWatchlistPosterURL(posterURL)
	if err != nil {
		return managedImageImport{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return managedImageImport{}, fmt.Errorf("构造海报请求失败: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return managedImageImport{}, fmt.Errorf("下载海报失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return managedImageImport{}, fmt.Errorf("下载海报失败: HTTP %d", response.StatusCode)
	}

	temporary, err := os.CreateTemp("", "watchlist-poster-*")
	if err != nil {
		return managedImageImport{}, fmt.Errorf("创建海报临时文件失败: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	// 多读一个字节：读满 watchlistPosterMaxBytes+1 就说明响应超限，直接拒，
	// 不把超限内容交给 Import 再判一次。
	written, err := io.Copy(temporary, io.LimitReader(response.Body, watchlistPosterMaxBytes+1))
	if err != nil {
		return managedImageImport{}, fmt.Errorf("写入海报临时文件失败: %w", err)
	}
	if written > watchlistPosterMaxBytes {
		return managedImageImport{}, fmt.Errorf("%w（%d 字节）", ErrWatchlistPosterTooLarge, watchlistPosterMaxBytes)
	}
	if written == 0 {
		return managedImageImport{}, errors.New("海报响应为空")
	}
	if err := temporary.Close(); err != nil {
		return managedImageImport{}, fmt.Errorf("关闭海报临时文件失败: %w", err)
	}
	// 格式与体积的最终判据在 Import：只认 JPEG / PNG / WebP，路径必须落在托管根内。
	return s.images.Import("watchlist", entryID, temporaryPath)
}

// RemovePoster 删掉一张已落盘的海报，用在写回失败的回滚上（§3.2），也用在条目
// 换图之后清理旧图。路径为空或文件已经不在都算成功——这条路径的目的是「确保它不
// 在了」，不是「确保删掉过一次」。
func (s *WatchlistService) RemovePoster(relativePath string) error {
	return s.images.Remove(relativePath)
}

// validateWatchlistPosterURL 只放行 http 与 https。资料源给回来的地址是外部输入，
// 不挡住 file:// 这类协议就等于把本地文件读进托管目录。
func validateWatchlistPosterURL(posterURL string) (string, error) {
	address := strings.TrimSpace(posterURL)
	if address == "" {
		return "", errors.New("海报地址为空")
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return "", fmt.Errorf("海报地址无效: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("海报地址无效：只支持 http 与 https，收到 %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", errors.New("海报地址无效：没有主机名")
	}
	return address, nil
}
