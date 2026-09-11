package services

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

// posterPNG 造一张真 PNG：Import 的格式判据是 http.DetectContentType，伪造的字节
// 过不了，测试里必须是编码器出来的东西。
func posterPNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := 0; x < 4; x++ {
		for y := 0; y < 4; y++ {
			canvas.Set(x, y, color.RGBA{R: shade, G: shade, B: shade, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatalf("编码测试 PNG 失败: %v", err)
	}
	return buffer.Bytes()
}

// managedWatchlistFiles 列出托管目录里所有想看海报，用来断言「没有孤儿文件」。
func managedWatchlistFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	root := filepath.Join(dataDir, "media-details", "watchlist")
	found := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			found = append(found, path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("遍历托管海报目录失败: %v", err)
	}
	return found
}

func newPosterTestClient(t *testing.T, config WatchlistMetadataConfig) *http.Client {
	t.Helper()
	client, err := NewWatchlistPosterHTTPClient(config)
	if err != nil {
		t.Fatalf("构造海报下载客户端失败: %v", err)
	}
	return client
}

// 下载 → 落盘走 ManagedImageService，路径落在 watchlist/<id>/ 下；同一张图重复下载
// 内容寻址去重，不产生第二个文件，Created 也据此为 false——写回失败时要靠这个标志
// 判断该不该删图（§3.2）。
func TestWatchlistPosterDownloadImportsUnderWatchlistEntity(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	svc := NewWatchlistService(dataDir)
	entry, err := svc.Create("沙丘", "")
	if err != nil {
		t.Fatal(err)
	}
	content := posterPNG(t, 20)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer origin.Close()
	client := newPosterTestClient(t, WatchlistMetadataConfig{})

	imported, err := svc.DownloadPoster(context.Background(), client, entry.ID, origin.URL+"/poster.png")
	if err != nil {
		t.Fatalf("下载海报失败: %v", err)
	}
	if !imported.Created {
		t.Fatalf("首次落盘应报告新建: %+v", imported)
	}
	wantPrefix := "watchlist/" + itoaUint(entry.ID) + "/"
	if !strings.HasPrefix(imported.RelativePath, wantPrefix) || !strings.HasSuffix(imported.RelativePath, ".png") {
		t.Fatalf("海报相对路径不对: %q", imported.RelativePath)
	}
	saved, err := os.ReadFile(filepath.Join(dataDir, "media-details", filepath.FromSlash(imported.RelativePath)))
	if err != nil {
		t.Fatalf("读取落盘海报失败: %v", err)
	}
	if !bytes.Equal(saved, content) {
		t.Fatal("落盘内容与下载内容不一致")
	}

	again, err := svc.DownloadPoster(context.Background(), client, entry.ID, origin.URL+"/poster.png")
	if err != nil {
		t.Fatalf("重复下载失败: %v", err)
	}
	if again.Created || again.RelativePath != imported.RelativePath {
		t.Fatalf("同一张图重复落盘应去重: %+v", again)
	}
	if files := managedWatchlistFiles(t, dataDir); len(files) != 1 {
		t.Fatalf("去重后应只有一个文件: %v", files)
	}
}

// AC-06：海报下载也是外部请求，必须经资料源出网代理发出。这条用例把代理指向一个
// 本地服务器，断言请求落在代理上而源站一次都没被访问——自建 http.Client 会让这条
// 断言失败。
func TestWatchlistPosterDownloadGoesThroughMetadataProxy(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	svc := NewWatchlistService(dataDir)
	entry, err := svc.Create("沙丘", "")
	if err != nil {
		t.Fatal(err)
	}
	originHit := false
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHit = true
		http.Error(w, "origin should not be reached", http.StatusTeapot)
	}))
	defer origin.Close()
	proxyHit := ""
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHit = r.URL.String()
		_, _ = w.Write(posterPNG(t, 90))
	}))
	defer proxy.Close()

	client := newPosterTestClient(t, WatchlistMetadataConfig{ProxyURL: proxy.URL})
	if _, err := svc.DownloadPoster(context.Background(), client, entry.ID, origin.URL+"/poster.png"); err != nil {
		t.Fatalf("经代理下载海报失败: %v", err)
	}
	if originHit {
		t.Fatal("配了代理却直连源站")
	}
	if proxyHit != origin.URL+"/poster.png" {
		t.Fatalf("代理收到的请求地址不对: %q", proxyHit)
	}
}

// 代理地址填错时不静默直连：客户端工厂直接拒绝，海报下载连发都不发。
func TestWatchlistPosterClientRejectsInvalidProxy(t *testing.T) {
	if _, err := NewWatchlistPosterHTTPClient(WatchlistMetadataConfig{ProxyURL: "127.0.0.1:1080"}); !errors.Is(err, ErrWatchlistMetadataProxyInvalid) {
		t.Fatalf("缺协议前缀的代理地址应被拒绝: %v", err)
	}
}

// 下载侧必须先有界：Import 的 20 MiB 上限是读完之后才判的，光靠它挡不住把超大响应
// 读进临时文件。超限时不留任何托管文件。
func TestWatchlistPosterDownloadRejectsOversizeResponse(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	svc := NewWatchlistService(dataDir)
	entry, err := svc.Create("沙丘", "")
	if err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.CopyN(w, endlessByte('A'), watchlistPosterMaxBytes+2)
	}))
	defer origin.Close()

	_, err = svc.DownloadPoster(context.Background(), newPosterTestClient(t, WatchlistMetadataConfig{}), entry.ID, origin.URL+"/poster.png")
	if !errors.Is(err, ErrWatchlistPosterTooLarge) {
		t.Fatalf("超限响应应被拒绝: %v", err)
	}
	if files := managedWatchlistFiles(t, dataDir); len(files) != 0 {
		t.Fatalf("超限时不应留下托管文件: %v", files)
	}
}

// 海报失败不连累文字字段：格式不符被 Import 拒掉之后，条目的简介、年份照旧，
// poster_path 仍为空，托管目录里没有半成品。
func TestWatchlistPosterDownloadFailureKeepsTextFields(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	svc := NewWatchlistService(dataDir)
	entry, err := svc.Create("沙丘", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", entry.ID).
		Updates(map[string]any{"overview": "沙漠与香料", "year": 2021}).Error; err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not a poster</html>"))
	}))
	defer origin.Close()

	if _, err := svc.DownloadPoster(context.Background(), newPosterTestClient(t, WatchlistMetadataConfig{}), entry.ID, origin.URL+"/poster.png"); err == nil {
		t.Fatal("非图片响应应被拒绝")
	}
	var saved models.WatchlistEntry
	if err := database.DB.First(&saved, entry.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Overview != "沙漠与香料" || saved.Year != 2021 || saved.PosterPath != "" {
		t.Fatalf("下载失败不应改动文字字段: %+v", saved)
	}
	if files := managedWatchlistFiles(t, dataDir); len(files) != 0 {
		t.Fatalf("格式不符时不应留下托管文件: %v", files)
	}
}

// 入口自己的边界：没有客户端就失败（不退回 http.DefaultClient，那会绕开代理）、
// 空地址与非 http(s) 协议一律拒绝，且都不发请求。
func TestWatchlistPosterDownloadRejectsBadInput(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	svc := NewWatchlistService(dataDir)
	client := newPosterTestClient(t, WatchlistMetadataConfig{})

	if _, err := svc.DownloadPoster(context.Background(), nil, 1, "http://example.invalid/p.png"); err == nil {
		t.Fatal("缺客户端时不应退回默认客户端")
	}
	for _, address := range []string{"", "   ", "file:///etc/passwd", "ftp://example.invalid/p.png", "example.invalid/p.png", "http://"} {
		if _, err := svc.DownloadPoster(context.Background(), client, 1, address); err == nil {
			t.Errorf("非法海报地址应被拒绝: %q", address)
		}
	}
	if files := managedWatchlistFiles(t, dataDir); len(files) != 0 {
		t.Fatalf("非法输入不应留下托管文件: %v", files)
	}
}

// TC-04：删除条目连带清掉海报，托管目录里不留孤儿文件；同名不同类型的另一条不受牵连。
func TestWatchlistDeleteRemovesPoster(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	svc := NewWatchlistService(dataDir)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(posterPNG(t, uint8(len(r.URL.Path)*10)))
	}))
	defer origin.Close()
	client := newPosterTestClient(t, WatchlistMetadataConfig{})

	kept, err := svc.Create("沙丘", "")
	if err != nil {
		t.Fatal(err)
	}
	removed, err := svc.Create("银翼杀手", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []*models.WatchlistEntry{kept, removed} {
		imported, err := svc.DownloadPoster(context.Background(), client, entry.ID, origin.URL+"/"+entry.Title+".png")
		if err != nil {
			t.Fatalf("下载海报失败: %v", err)
		}
		if err := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", entry.ID).
			Update("poster_path", imported.RelativePath).Error; err != nil {
			t.Fatal(err)
		}
		entry.PosterPath = imported.RelativePath
	}
	if files := managedWatchlistFiles(t, dataDir); len(files) != 2 {
		t.Fatalf("两条条目应各有一张海报: %v", files)
	}

	if err := svc.Delete(removed.ID); err != nil {
		t.Fatalf("删除条目失败: %v", err)
	}
	files := managedWatchlistFiles(t, dataDir)
	if len(files) != 1 || !strings.HasSuffix(filepath.ToSlash(files[0]), kept.PosterPath) {
		t.Fatalf("删除后应只剩保留条目的海报: %v", files)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "media-details", filepath.FromSlash(removed.PosterPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("被删条目的海报应已清理: %v", err)
	}
	// 已经不在的路径再删一次仍算成功：回滚路径要的是「确保它不在」。
	if err := svc.RemovePoster(removed.PosterPath); err != nil {
		t.Fatalf("重复清理应幂等: %v", err)
	}
	if err := svc.RemovePoster(""); err != nil {
		t.Fatalf("空路径清理应无副作用: %v", err)
	}
	// 没有海报的条目照常删除，不因为清理路径而报错。
	bare, err := svc.Create("异形", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(bare.ID); err != nil {
		t.Fatalf("无海报条目删除失败: %v", err)
	}
	if err := svc.Delete(bare.ID); !errors.Is(err, ErrWatchlistEntryNotFound) {
		t.Fatalf("重复删除应明确不存在: %v", err)
	}
}

// /preview/watchlist-poster/<id> 的服务端来源：拿得到图就回文件，条目不存在、没有
// 海报、文件已丢都统一回 os.ErrNotExist，路由据此给 404。
func TestResolveWatchlistPoster(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	svc := NewWatchlistService(dataDir)
	entry, err := svc.Create("沙丘", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveWatchlistPoster(entry.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("没有海报应是不存在: %v", err)
	}
	if _, err := svc.ResolveWatchlistPoster(entry.ID + 100); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("条目不存在应是不存在: %v", err)
	}

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(posterPNG(t, 40))
	}))
	defer origin.Close()
	imported, err := svc.DownloadPoster(context.Background(), newPosterTestClient(t, WatchlistMetadataConfig{}), entry.ID, origin.URL+"/poster.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", entry.ID).
		Update("poster_path", imported.RelativePath).Error; err != nil {
		t.Fatal(err)
	}
	asset, err := svc.ResolveWatchlistPoster(entry.ID)
	if err != nil {
		t.Fatalf("解析海报失败: %v", err)
	}
	if asset.MIME != "image/png" || asset.DisplayName != filepath.Base(imported.RelativePath) || asset.ModTime.IsZero() {
		t.Fatalf("海报资产信息不对: %+v", asset)
	}
	if _, err := os.Stat(asset.Path); err != nil {
		t.Fatalf("海报文件应存在: %v", err)
	}

	// 文件被外部删掉而路径还在库里：仍然是 404，不是 500。
	if err := os.Remove(asset.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveWatchlistPoster(entry.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("文件已丢应是不存在: %v", err)
	}
}

// 白名单只放宽到 watchlist，其他 entityType 仍然被拒。
func TestManagedImageRejectsUnknownEntity(t *testing.T) {
	dataDir := t.TempDir()
	images := NewManagedImageService(dataDir)
	source := filepath.Join(dataDir, "poster.png")
	if err := os.WriteFile(source, posterPNG(t, 10), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := images.Import("watchlists", 1, source); err == nil {
		t.Fatal("未登记的 entityType 应被拒绝")
	}
	if _, err := images.Import("watchlist", 1, source); err != nil {
		t.Fatalf("watchlist 应在白名单内: %v", err)
	}
}

// endlessByte 造一个永远读得出内容的 body，用来试下载侧的字节上限。
type endlessByte byte

func (b endlessByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

func itoaUint(value uint) string {
	return strconv.FormatUint(uint64(value), 10)
}
