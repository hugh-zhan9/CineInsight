package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"video-master/models"

	"gorm.io/gorm"
)

// 本文件验收 D-MC14 的下载侧：海报落盘、落盘与写库的顺序与回滚、从只读路由搬过来
// 的三样安全控制（域名白名单、逐跳重定向复检、Referer/User-Agent），以及磁盘上限。
//
// **一个真实请求都不发**：本仓库不对 douban.com / doubanio.com 出网（用户机器
// 2026-09-22 正被豆瓣限流，再打一轮只会让处境更糟）。桩客户端只替换「请求怎么
// 发出去」，**不替换「允许请求谁」**——地址一律先过 validateMovieChartPosterURL，
// 库里存的也仍然是真实的 https://img2.doubanio.com/... 形态。

// movieChartPosterRequest 是桩服务收到的一次取图请求。
type movieChartPosterRequest struct {
	URL       string
	Referer   string
	UserAgent string
	Accept    string
}

// movieChartPosterStub 把已经通过白名单的请求改投到本地桩服务，并按序记下原地址。
type movieChartPosterStub struct {
	mu       sync.Mutex
	base     *url.URL
	requests []movieChartPosterRequest
}

func (s *movieChartPosterStub) RoundTrip(request *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.requests = append(s.requests, movieChartPosterRequest{
		URL:       request.URL.String(),
		Referer:   request.Header.Get("Referer"),
		UserAgent: request.Header.Get("User-Agent"),
		Accept:    request.Header.Get("Accept"),
	})
	s.mu.Unlock()
	routed := request.Clone(request.Context())
	routed.URL.Scheme = s.base.Scheme
	routed.URL.Host = s.base.Host
	routed.Host = ""
	return http.DefaultTransport.RoundTrip(routed)
}

func (s *movieChartPosterStub) seen() []movieChartPosterRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]movieChartPosterRequest(nil), s.requests...)
}

// movieChartPosterHarness 是带托管图片服务的刷新夹具。
type movieChartPosterHarness struct {
	*movieChartHarness
	dataDir string
	stub    *movieChartPosterStub
}

// newMovieChartPosterHarness 起一个带磁盘的夹具：想看片单服务只用来提供托管图片
// 根目录（榜单海报落在 media-details/movie_chart/ 之下，与 watchlist/ 互不干扰）。
func newMovieChartPosterHarness(t *testing.T, source MovieChartSource, handler http.HandlerFunc) *movieChartPosterHarness {
	t.Helper()
	dataDir := t.TempDir()
	harness := newMovieChartHarnessWithWatchlist(t, source, NewWatchlistService(dataDir))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("解析桩服务地址失败: %v", err)
	}
	stub := &movieChartPosterStub{base: base}
	harness.service.newPosterClient = func() (*http.Client, error) {
		// 生产客户端装的就是这条策略（见 newMovieChartPosterClient），桩客户端
		// 照装：换掉的只是传输层，白名单与逐跳复检必须原样生效。
		return &http.Client{Transport: stub, CheckRedirect: movieChartPosterRedirectPolicy}, nil
	}
	return &movieChartPosterHarness{movieChartHarness: harness, dataDir: dataDir, stub: stub}
}

// posterRoot 是榜单海报的托管根目录。
func (h *movieChartPosterHarness) posterRoot() string {
	return filepath.Join(h.dataDir, "media-details", movieChartPosterEntity)
}

// posterFiles 列出已经落盘的海报，返回相对 posterRoot 的路径，已排序。
func (h *movieChartPosterHarness) posterFiles() []string {
	h.t.Helper()
	var files []string
	err := filepath.WalkDir(h.posterRoot(), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		relative, err := filepath.Rel(h.posterRoot(), path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		h.t.Fatalf("列出已落盘海报失败: %v", err)
	}
	sort.Strings(files)
	return files
}

// seedPosterEntry 写一条带远程海报地址、尚未落盘的条目。
func (h *movieChartPosterHarness) seedPosterEntry(doubanID string) models.MovieChartEntry {
	h.t.Helper()
	entry := models.MovieChartEntry{
		DoubanID:     doubanID,
		Year:         2026,
		Title:        "片" + doubanID,
		PosterURL:    "https://img2.doubanio.com/view/photo/l/public/" + doubanID + ".jpg",
		ReleaseScope: models.MovieChartScopeTheatrical,
		DetailStatus: models.MovieChartDetailSucceeded,
	}
	if err := h.db.Create(&entry).Error; err != nil {
		h.t.Fatalf("写入条目 %s 失败: %v", doubanID, err)
	}
	return entry
}

// placeExistingPoster 伪造「上一次运行留下的一张海报」：直接把文件放进托管目录，
// 并让条目指向它，返回托管相对路径。
func (h *movieChartPosterHarness) placeExistingPoster(t *testing.T, doubanID string, filler byte, modTime time.Time) string {
	t.Helper()
	entry := h.seedPosterEntry(doubanID)
	relative := filepath.Join(movieChartPosterEntity, strconv.FormatUint(uint64(entry.ID), 10), doubanID+".jpg")
	full := filepath.Join(h.dataDir, "media-details", relative)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("建立海报目录失败: %v", err)
	}
	if err := os.WriteFile(full, movieChartJPEGBytes(filler, 64), 0644); err != nil {
		t.Fatalf("写入海报文件失败: %v", err)
	}
	if err := os.Chtimes(full, modTime, modTime); err != nil {
		t.Fatalf("调整 mtime 失败: %v", err)
	}
	slashed := filepath.ToSlash(relative)
	if err := h.db.Model(&models.MovieChartEntry{}).
		Where("id = ?", entry.ID).Update("poster_path", slashed).Error; err != nil {
		t.Fatalf("写入托管路径失败: %v", err)
	}
	return slashed
}

// movieChartJPEGBytes 造一段能被 http.DetectContentType 认成 image/jpeg 的字节，
// filler 决定内容，从而决定内容寻址后的文件名。
func movieChartJPEGBytes(filler byte, size int) []byte {
	header := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	if size <= len(header) {
		size = len(header) + 1
	}
	return append(header, bytes.Repeat([]byte{filler}, size-len(header))...)
}

func movieChartJPEGHandler(filler byte, size int) http.HandlerFunc {
	body := movieChartJPEGBytes(filler, size)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(body)
	}
}

// ---------- 落盘顺序与回滚 ----------

// TestMovieChartPosterWritesFileBeforeRow 钉住承重的顺序：**先落盘、再写库**。
//
// 断言挂在 UPDATE 的 before 钩子上：写 poster_path 的那一刻，文件必须已经在磁盘上。
// 把 storeEntryPoster 改成先写库再落盘，这条钩子里看到的目录是空的，用例立刻红。
// 顺带钉住搬家途中不许掉的两个请求头：Referer 是图片自身站点根、**带末尾斜杠**
// （少一个斜杠豆瓣图床回 403），User-Agent 是浏览器标识。
func TestMovieChartPosterWritesFileBeforeRow(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, movieChartJPEGHandler('a', 64))
	entry := harness.seedPosterEntry("2001")

	var filesAtUpdate int
	harness.hookUpdate("test:poster_order", "before", func(tx *gorm.DB) {
		filesAtUpdate = len(harness.posterFiles())
	})

	client, err := harness.service.newPosterClient()
	if err != nil {
		t.Fatalf("装配桩客户端失败: %v", err)
	}
	if err := harness.service.storeEntryPoster(context.Background(), client, entry.ID, entry.PosterURL); err != nil {
		t.Fatalf("落盘海报失败: %v", err)
	}
	if filesAtUpdate != 1 {
		t.Fatalf("写 poster_path 时文件必须已经落盘（先盘后库），当时磁盘上有 %d 个文件", filesAtUpdate)
	}

	stored := harness.entry("2001")
	if stored.PosterPath == "" {
		t.Fatal("poster_path 未写回")
	}
	if !strings.HasPrefix(stored.PosterPath, movieChartPosterEntity+"/") {
		t.Fatalf("托管相对路径应落在 movie_chart/ 之下: %q", stored.PosterPath)
	}
	asset, err := harness.service.ResolveChartPoster("2001")
	if err != nil {
		t.Fatalf("按豆瓣 ID 取本地海报失败: %v", err)
	}
	if asset.MIME != "image/jpeg" {
		t.Fatalf("MIME = %q", asset.MIME)
	}

	seen := harness.stub.seen()
	if len(seen) != 1 {
		t.Fatalf("应当只发一次取图请求，实际 %v", seen)
	}
	if seen[0].URL != entry.PosterURL {
		t.Fatalf("取图地址必须与库里存的一致: %q", seen[0].URL)
	}
	if seen[0].Referer != "https://img2.doubanio.com/" {
		t.Fatalf("Referer 必须是图片自身站点根且带末尾斜杠，实际 %q", seen[0].Referer)
	}
	if seen[0].UserAgent != watchlistPosterUserAgent {
		t.Fatalf("User-Agent 必须是浏览器标识，实际 %q", seen[0].UserAgent)
	}
	// Accept 与 Referer、User-Agent 是同一套「像浏览器」的姿态，一起钉住：
	// 少了它同样可能在图床那边被当成脚本，而三者里只钉两个，下一次搬家时
	// 剩下那一个看起来就是可有可无的。
	if seen[0].Accept != "image/avif,image/webp,image/*,*/*;q=0.8" {
		t.Fatalf("Accept 头错误: %q", seen[0].Accept)
	}
}

// TestMovieChartPosterRemovesFileWhenRowWriteFails 钉住回滚：写库失败就把刚落盘的
// 图删掉，绝不留下一行指向不存在文件的 poster_path。
//
// 两种失败都覆盖：数据库报错，以及守卫不成立（这一行已经被别人放了图进去）。
func TestMovieChartPosterRemovesFileWhenRowWriteFails(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, movieChartJPEGHandler('b', 64))
	entry := harness.seedPosterEntry("2002")
	client, err := harness.service.newPosterClient()
	if err != nil {
		t.Fatalf("装配桩客户端失败: %v", err)
	}

	harness.hookUpdate("test:poster_write_fails", "before", func(tx *gorm.DB) {
		_ = tx.AddError(errors.New("注入的写库失败"))
	})
	if err := harness.service.storeEntryPoster(context.Background(), client, entry.ID, entry.PosterURL); err == nil {
		t.Fatal("写库失败时 storeEntryPoster 必须报错")
	}
	if files := harness.posterFiles(); len(files) != 0 {
		t.Fatalf("写库失败必须把刚落盘的图删掉，磁盘上仍有 %v", files)
	}
	if stored := harness.entry("2002"); stored.PosterPath != "" {
		t.Fatalf("poster_path 不该被写进去: %q", stored.PosterPath)
	}
	_ = harness.db.Callback().Update().Remove("test:poster_write_fails")

	// 守卫不成立：写回之前这一行已经有别人放进去的图。
	if err := harness.db.Model(&models.MovieChartEntry{}).
		Where("id = ?", entry.ID).Update("poster_path", "movie_chart/9/other.jpg").Error; err != nil {
		t.Fatalf("预置已有海报失败: %v", err)
	}
	if err := harness.service.storeEntryPoster(context.Background(), client, entry.ID, entry.PosterURL); err == nil {
		t.Fatal("守卫不成立时必须报错，不能假装写成功")
	}
	if files := harness.posterFiles(); len(files) != 0 {
		t.Fatalf("守卫不成立同样要回滚落盘的图，磁盘上仍有 %v", files)
	}
	if stored := harness.entry("2002"); stored.PosterPath != "movie_chart/9/other.jpg" {
		t.Fatalf("已有的 poster_path 不得被覆盖: %q", stored.PosterPath)
	}
}

// TestMovieChartPosterKeepsExistingFileOnRepeatedImport 钉住 Created == false 的
// 那一支：同样的字节重复落盘不产生新文件，此时写库失败**不许删图**——那张图正是
// 别的行在用的那一张，删掉就是把一张有人引用的图删了。
func TestMovieChartPosterKeepsExistingFileOnRepeatedImport(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, movieChartJPEGHandler('c', 64))
	first := harness.seedPosterEntry("2003")
	client, err := harness.service.newPosterClient()
	if err != nil {
		t.Fatalf("装配桩客户端失败: %v", err)
	}
	if err := harness.service.storeEntryPoster(context.Background(), client, first.ID, first.PosterURL); err != nil {
		t.Fatalf("首次落盘失败: %v", err)
	}
	before := harness.posterFiles()
	if len(before) != 1 {
		t.Fatalf("首次落盘后应有 1 个文件，实际 %v", before)
	}

	// 把行退回缺图状态，再下一次同样的字节：Import 命中已有文件，Created 为 false。
	if err := harness.db.Model(&models.MovieChartEntry{}).
		Where("id = ?", first.ID).Update("poster_path", "").Error; err != nil {
		t.Fatalf("重置 poster_path 失败: %v", err)
	}
	harness.hookUpdate("test:poster_write_fails_again", "before", func(tx *gorm.DB) {
		_ = tx.AddError(errors.New("注入的写库失败"))
	})
	if err := harness.service.storeEntryPoster(context.Background(), client, first.ID, first.PosterURL); err == nil {
		t.Fatal("写库失败时必须报错")
	}
	after := harness.posterFiles()
	if len(after) != 1 || after[0] != before[0] {
		t.Fatalf("重复落盘同一张图时不得删文件（它可能正被别的行引用），前 %v 后 %v", before, after)
	}
	if got := harness.entry("2003").PosterPath; got != "" {
		t.Fatalf("写库失败后 poster_path 仍应为空: %q", got)
	}
}

// ---------- 白名单与逐跳重定向（从只读路由搬过来的安全控制） ----------

// TestMovieChartPosterRejectsAddressesOutsideWhitelist 钉住库里的地址仍然要过一道
// 白名单，而且**被拒的地址一个请求都不发**。
//
// evildoubanio.com 一条专门钉住「裸 HasSuffix(host, "doubanio.com")」这个经典错误：
// 把主机判定换成裸 HasSuffix，本用例立刻失败。
func TestMovieChartPosterRejectsAddressesOutsideWhitelist(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("白名单外的地址不该发出任何请求: %s", r.URL)
	})
	client, err := harness.service.newPosterClient()
	if err != nil {
		t.Fatalf("装配桩客户端失败: %v", err)
	}

	cases := []struct {
		address string
		reason  string
	}{
		{"https://evildoubanio.com/p1.jpg", "后缀近似的外部域名"},
		{"https://doubanio.com.evil.com/p1.jpg", "域名前缀伪装"},
		{"http://img2.doubanio.com/p1.jpg", "明文 http"},
		{"https://user:pass@img2.doubanio.com/p1.jpg", "地址内嵌凭证"},
		{"https://img2.doubanio.com:8080/p1.jpg", "显式端口"},
		{"file:///etc/passwd", "本地文件协议"},
		{"https://127.0.0.1/p1.jpg", "回环地址"},
		{"https://169.254.169.254/latest/meta-data/", "元数据服务"},
		{"   ", "空白地址"},
		{"https:///p1.jpg", "没有主机名"},
	}
	for index, testCase := range cases {
		entry := harness.seedPosterEntry(fmt.Sprintf("3%d00", index))
		if err := harness.db.Model(&models.MovieChartEntry{}).
			Where("id = ?", entry.ID).Update("poster_url", testCase.address).Error; err != nil {
			t.Fatalf("写入地址失败: %v", err)
		}
		err := harness.service.storeEntryPoster(context.Background(), client, entry.ID, testCase.address)
		if !errors.Is(err, ErrMovieChartPosterAddressRejected) {
			t.Fatalf("%s（%s）应被白名单拒绝，实际 %v", testCase.address, testCase.reason, err)
		}
	}
	if seen := harness.stub.seen(); len(seen) != 0 {
		t.Fatalf("白名单外的地址不得发出请求，实际 %v", seen)
	}
	// 放行的那一面也要证：真实形态的图床地址必须过得去。
	for _, address := range []string{
		"https://img2.doubanio.com/view/photo/l/public/p1.jpg",
		"https://doubanio.com/p1.jpg",
		"https://IMG9.Doubanio.com/p1.jpg",
	} {
		if _, _, ok := validateMovieChartPosterURL(address); !ok {
			t.Fatalf("%s 是合法的图床地址，应当放行", address)
		}
	}
}

// TestMovieChartPosterRedirectPolicyKeepsWhitelist 钉住**逐跳**复检：白名单只管得住
// 第一跳，上游一个 302 就能把取图变成跳板。
func TestMovieChartPosterRedirectPolicyKeepsWhitelist(t *testing.T) {
	for _, address := range []string{
		"https://img1.doubanio.com/view/photo/l/public/p1.jpg",
		"https://doubanio.com/p1.jpg",
	} {
		request := httptest.NewRequest(http.MethodGet, address, nil)
		if err := movieChartPosterRedirectPolicy(request, nil); err != nil {
			t.Fatalf("%s 应放行，实际 %v", address, err)
		}
	}
	for _, address := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"https://evildoubanio.com/p1.jpg",
		"http://img2.doubanio.com/p1.jpg",
		"https://127.0.0.1:8080/p1.jpg",
	} {
		request := httptest.NewRequest(http.MethodGet, address, nil)
		if err := movieChartPosterRedirectPolicy(request, nil); err == nil {
			t.Fatalf("%s 应被拒绝", address)
		}
	}
	loop := httptest.NewRequest(http.MethodGet, "https://img1.doubanio.com/p1.jpg", nil)
	if err := movieChartPosterRedirectPolicy(loop, make([]*http.Request, movieChartPosterMaxRedirects)); err == nil {
		t.Fatal("超过跳转上限应被拒绝")
	}
}

// TestMovieChartPosterFollowsWhitelistedRedirectOnly 把逐跳复检放进真实的下载里：
// 跳到白名单内照常取到图，跳到白名单外整个下载失败且**不落盘**。
func TestMovieChartPosterFollowsWhitelistedRedirectOnly(t *testing.T) {
	target := "https://img9.doubanio.com/view/photo/l/public/moved.jpg"
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/moved.jpg"):
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(movieChartJPEGBytes('d', 64))
		case strings.HasSuffix(r.URL.Path, "/4001.jpg"):
			http.Redirect(w, r, target, http.StatusFound)
		default:
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		}
	})
	client, err := harness.service.newPosterClient()
	if err != nil {
		t.Fatalf("装配桩客户端失败: %v", err)
	}

	allowed := harness.seedPosterEntry("4001")
	if err := harness.service.storeEntryPoster(context.Background(), client, allowed.ID, allowed.PosterURL); err != nil {
		t.Fatalf("跳转到白名单内的地址应当取得到图: %v", err)
	}
	if got := harness.entry("4001").PosterPath; got == "" {
		t.Fatal("跳转之后的图应当落盘")
	}

	refused := harness.seedPosterEntry("4002")
	if err := harness.service.storeEntryPoster(context.Background(), client, refused.ID, refused.PosterURL); err == nil {
		t.Fatal("跳转到白名单外的地址必须失败")
	}
	if got := harness.entry("4002").PosterPath; got != "" {
		t.Fatalf("被拒的跳转不得落盘: %q", got)
	}
	if files := harness.posterFiles(); len(files) != 1 {
		t.Fatalf("磁盘上应当只有被放行那一张，实际 %v", files)
	}
}

// TestNewMovieChartPosterClientCarriesRedirectPolicy 钉住生产客户端确实装了逐跳
// 复检、超时按海报用途给——桩客户端装了不算数，生产那条路径漏装就等于没有复检。
func TestNewMovieChartPosterClientCarriesRedirectPolicy(t *testing.T) {
	client, err := newMovieChartPosterClient()
	if err != nil {
		t.Fatalf("装配取图客户端失败: %v", err)
	}
	if client.CheckRedirect == nil {
		t.Fatal("取图客户端必须带跳转白名单策略")
	}
	request := httptest.NewRequest(http.MethodGet, "https://evildoubanio.com/p1.jpg", nil)
	if err := client.CheckRedirect(request, nil); err == nil {
		t.Fatal("装上的策略必须真的会拒绝白名单外的跳转")
	}
	if client.Timeout != watchlistPosterDownloadTimeout {
		t.Fatalf("超时 = %v，期望 %v", client.Timeout, watchlistPosterDownloadTimeout)
	}
}

// TestMovieChartPosterRejectsNonImageResponses 钉住非图片响应落不进托管目录：
// 防盗链页与验证页都是 200 + text/html，SVG 是图但能内嵌脚本，两者都不许存。
func TestMovieChartPosterRejectsNonImageResponses(t *testing.T) {
	mode := "html"
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html>418</html>"))
		case "svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
		case "empty":
			w.Header().Set("Content-Type", "image/jpeg")
		case "status":
			// 2026-09-22 真机上豆瓣图床回的就是这个。
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	})
	client, err := harness.service.newPosterClient()
	if err != nil {
		t.Fatalf("装配桩客户端失败: %v", err)
	}
	for index, name := range []string{"html", "svg", "empty", "status"} {
		mode = name
		entry := harness.seedPosterEntry(fmt.Sprintf("500%d", index))
		if err := harness.service.storeEntryPoster(context.Background(), client, entry.ID, entry.PosterURL); err == nil {
			t.Fatalf("%s 响应不该被当成海报存下来", name)
		}
		if got := harness.entry(entry.DoubanID).PosterPath; got != "" {
			t.Fatalf("%s 响应不得写 poster_path: %q", name, got)
		}
	}
	if files := harness.posterFiles(); len(files) != 0 {
		t.Fatalf("非图片响应不得落盘，实际 %v", files)
	}
}

// TestMovieChartPosterRejectsOversizeResponse 钉住下载侧的体积上限：超限的响应
// 不交给 Import，也不落盘。
func TestMovieChartPosterRejectsOversizeResponse(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(movieChartJPEGBytes('e', int(watchlistPosterMaxBytes)+16))
	})
	client, err := harness.service.newPosterClient()
	if err != nil {
		t.Fatalf("装配桩客户端失败: %v", err)
	}
	entry := harness.seedPosterEntry("5100")
	err = harness.service.storeEntryPoster(context.Background(), client, entry.ID, entry.PosterURL)
	if !errors.Is(err, ErrWatchlistPosterTooLarge) {
		t.Fatalf("超限响应应报「海报太大」，实际 %v", err)
	}
	if files := harness.posterFiles(); len(files) != 0 {
		t.Fatalf("超限响应不得落盘，实际 %v", files)
	}
}

// ---------- 磁盘上限与 LRU ----------

// TestMovieChartPosterCacheEvictsOldestAndClearsRowFirst 钉住三件事：
//
//  1. 超过上限时按 mtime 从旧到新淘汰，刚落盘的那一张永不被淘汰；
//  2. 淘汰的顺序与落盘**正好相反**——先清 poster_path，再删文件。断言挂在清行的
//     UPDATE 钩子上：那一刻文件必须还在。改成先删文件再清行，用例立刻红；
//  3. 被淘汰的条目回到「缺图」状态（poster_path 为空），因此会被下一轮补图捡起来。
func TestMovieChartPosterCacheEvictsOldestAndClearsRowFirst(t *testing.T) {
	filler := byte('a')
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(movieChartJPEGBytes(filler, 64))
	})
	// 上限只容得下两张 64 字节的图，第三张落盘后必须淘汰最旧的那一张。
	harness.service.posterCacheLimit = 150
	client, err := harness.service.newPosterClient()
	if err != nil {
		t.Fatalf("装配桩客户端失败: %v", err)
	}

	ids := []string{"6001", "6002", "6003"}
	paths := make(map[string]string, len(ids))
	for index, id := range ids {
		filler = byte('a' + index)
		entry := harness.seedPosterEntry(id)
		if index == 2 {
			// 第三张落盘时才会触发淘汰：这一刻清行的 UPDATE 必须发生在删文件之前。
			harness.hookUpdate("test:evict_order", "before", func(tx *gorm.DB) {
				dest, ok := tx.Statement.Dest.(map[string]any)
				if !ok {
					return
				}
				value, ok := dest["poster_path"].(string)
				if !ok || value != "" {
					return
				}
				// 正在清某一行的 poster_path：被清的那张图此刻必须还在磁盘上。
				if files := harness.posterFiles(); len(files) != 3 {
					t.Errorf("清 poster_path 时文件必须还在（先库后盘），当时磁盘上有 %d 个文件", len(files))
				}
			})
		}
		if err := harness.service.storeEntryPoster(context.Background(), client, entry.ID, entry.PosterURL); err != nil {
			t.Fatalf("落盘 %s 失败: %v", id, err)
		}
		paths[id] = harness.entry(id).PosterPath
		// mtime 的精度在某些文件系统上只有一秒，靠 Chtimes 把落盘顺序做成确定的。
		older := time.Now().Add(-time.Duration(len(ids)-index) * time.Hour)
		full := filepath.Join(harness.dataDir, "media-details", filepath.FromSlash(paths[id]))
		if err := os.Chtimes(full, older, older); err != nil {
			t.Fatalf("调整 %s 的 mtime 失败: %v", id, err)
		}
	}

	if got := harness.entry("6001").PosterPath; got != "" {
		t.Fatalf("最旧的那一张应当被淘汰、行退回缺图状态，实际 %q", got)
	}
	if got := harness.entry("6002").PosterPath; got == "" {
		t.Fatal("6002 不该被淘汰")
	}
	if got := harness.entry("6003").PosterPath; got == "" {
		t.Fatal("刚落盘的那一张永远不该被淘汰")
	}
	files := harness.posterFiles()
	if len(files) != 2 {
		t.Fatalf("淘汰后应剩 2 个文件，实际 %v", files)
	}
	for _, name := range files {
		if movieChartPosterEntity+"/"+name == paths["6001"] {
			t.Fatalf("被淘汰的文件应当已经删掉: %v", files)
		}
	}

	// 淘汰过的条目回到缺图状态，下一轮补图会把它捡起来（取件条件只看
	// poster_url 非空、poster_path 为空）。
	var backlog []models.MovieChartEntry
	if err := movieChartPosterBacklog(harness.db, 2026).Find(&backlog).Error; err != nil {
		t.Fatalf("查缺图条目失败: %v", err)
	}
	if len(backlog) != 1 || backlog[0].DoubanID != "6001" {
		t.Fatalf("被淘汰的条目应当重新成为补图对象，实际 %+v", backlog)
	}
}

// TestMovieChartPosterCacheNeverEvictsTheImageItJustStored 钉住 keepRelative：
// 上限比单张图还小时，整理会想把磁盘上的每一张都淘汰掉——**唯独不能淘汰这一次
// 刚写进去的那一张**。没有这道保护，一次存盘会清掉自己刚写的行、删掉自己刚写的
// 文件，然后一本正经地返回成功：条目从此缺图，而且不留任何痕迹。
func TestMovieChartPosterCacheNeverEvictsTheImageItJustStored(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, movieChartJPEGHandler('v', 64))
	// 上限小于一张图：整理必然进入淘汰循环，而唯一的候选就是刚落盘的那一张。
	harness.service.posterCacheLimit = 10
	client, err := harness.service.newPosterClient()
	if err != nil {
		t.Fatalf("装配桩客户端失败: %v", err)
	}
	entry := harness.seedPosterEntry("6201")
	if err := harness.service.storeEntryPoster(context.Background(), client, entry.ID, entry.PosterURL); err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	stored := harness.entry("6201")
	if stored.PosterPath == "" {
		t.Fatal("刚落盘的行不该被自己的整理清掉")
	}
	if files := harness.posterFiles(); len(files) != 1 {
		t.Fatalf("刚落盘的文件不该被自己的整理删掉，实际 %v", files)
	}
	if _, err := harness.service.ResolveChartPoster("6201"); err != nil {
		t.Fatalf("刚落盘的海报应当取得到: %v", err)
	}
}

// TestMovieChartPosterCacheCountsWhatWasAlreadyOnDisk 钉住上限量的是**磁盘**，
// 不是「本进程这次存了多少」。
//
// 字节记账是增量的（省掉每存一张就走一遍目录树），所以它必须在本进程第一次落盘时
// 先量一遍打底。不量的话，上一次运行留下的图对这一轮完全不可见，上限就退化成
// 「每个进程各 1 GiB」——用户重启几次应用，磁盘上就能堆出几倍于上限的海报。
// 用例因此先把「上一轮留下的两张图」直接放进托管目录，再存第三张。
func TestMovieChartPosterCacheCountsWhatWasAlreadyOnDisk(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, movieChartJPEGHandler('x', 64))
	harness.service.posterCacheLimit = 150

	older := harness.placeExistingPoster(t, "6301", 'y', movieChartTestNow.Add(-48*time.Hour))
	newer := harness.placeExistingPoster(t, "6302", 'z', movieChartTestNow.Add(-24*time.Hour))

	client, err := harness.service.newPosterClient()
	if err != nil {
		t.Fatalf("装配桩客户端失败: %v", err)
	}
	entry := harness.seedPosterEntry("6303")
	if err := harness.service.storeEntryPoster(context.Background(), client, entry.ID, entry.PosterURL); err != nil {
		t.Fatalf("落盘失败: %v", err)
	}

	if got := harness.entry("6301").PosterPath; got != "" {
		t.Fatalf("上一轮留下的最旧那张必须被淘汰、行退回缺图状态，实际 %q", got)
	}
	if got := harness.entry("6302").PosterPath; got != newer {
		t.Fatalf("第二旧的那张还在上限之内，不该被淘汰: %q", got)
	}
	if got := harness.entry("6303").PosterPath; got == "" {
		t.Fatal("刚落盘的那一张不该被淘汰")
	}
	files := harness.posterFiles()
	if len(files) != 2 {
		t.Fatalf("淘汰后应剩 2 个文件，实际 %v", files)
	}
	for _, name := range files {
		if movieChartPosterEntity+"/"+name == older {
			t.Fatalf("被淘汰的文件应当已经删掉: %v", files)
		}
	}
}

// TestMovieChartPosterCacheKeepsFileWhenRowClearFails 钉住淘汰侧的失败处置：
// 清 poster_path 失败就**不删文件**，两边仍然自洽（多一张没人看的图，而不是一行
// 指向空的路径）。
func TestMovieChartPosterCacheKeepsFileWhenRowClearFails(t *testing.T) {
	filler := byte('a')
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(movieChartJPEGBytes(filler, 64))
	})
	harness.service.posterCacheLimit = 150
	client, err := harness.service.newPosterClient()
	if err != nil {
		t.Fatalf("装配桩客户端失败: %v", err)
	}
	for index, id := range []string{"6101", "6102"} {
		filler = byte('a' + index)
		entry := harness.seedPosterEntry(id)
		if err := harness.service.storeEntryPoster(context.Background(), client, entry.ID, entry.PosterURL); err != nil {
			t.Fatalf("落盘 %s 失败: %v", id, err)
		}
	}
	// 第三张：清行的 UPDATE 注入失败。
	filler = 'c'
	third := harness.seedPosterEntry("6103")
	harness.hookUpdate("test:clear_fails", "before", func(tx *gorm.DB) {
		dest, ok := tx.Statement.Dest.(map[string]any)
		if !ok {
			return
		}
		if value, ok := dest["poster_path"].(string); ok && value == "" {
			_ = tx.AddError(errors.New("注入的清行失败"))
		}
	})
	if err := harness.service.storeEntryPoster(context.Background(), client, third.ID, third.PosterURL); err != nil {
		t.Fatalf("落盘 6103 失败: %v", err)
	}
	if files := harness.posterFiles(); len(files) != 3 {
		t.Fatalf("清行失败时不得删文件，实际剩 %v", files)
	}
	for _, id := range []string{"6101", "6102", "6103"} {
		if got := harness.entry(id).PosterPath; got == "" {
			t.Fatalf("%s 的 poster_path 不该被清掉: %q", id, got)
		}
	}
}

// ---------- 只读解析 ----------

// TestResolveChartPosterIsLocalOnly 钉住渲染侧的解析：**只认本地文件**。
// 库里有远程地址但没落盘、poster_path 指向不存在的文件、内容不是位图，
// 一律 os.ErrNotExist，绝不回源。
func TestResolveChartPosterIsLocalOnly(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("解析本地海报不得出网: %s", r.URL)
	})

	harness.seedPosterEntry("7001") // 有 poster_url，没有 poster_path
	missing := harness.seedPosterEntry("7002")
	if err := harness.db.Model(&models.MovieChartEntry{}).Where("id = ?", missing.ID).
		Update("poster_path", "movie_chart/999/deadbeef.jpg").Error; err != nil {
		t.Fatalf("写入失效路径失败: %v", err)
	}
	escaping := harness.seedPosterEntry("7003")
	if err := harness.db.Model(&models.MovieChartEntry{}).Where("id = ?", escaping.ID).
		Update("poster_path", "../../../etc/passwd").Error; err != nil {
		t.Fatalf("写入越界路径失败: %v", err)
	}

	for _, doubanID := range []string{"7001", "7002", "7003", "9999999"} {
		if _, err := harness.service.ResolveChartPoster(doubanID); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s 应当报「没有这张图」，实际 %v", doubanID, err)
		}
	}
	if seen := harness.stub.seen(); len(seen) != 0 {
		t.Fatalf("解析路径上不得有任何出网，实际 %v", seen)
	}
}
