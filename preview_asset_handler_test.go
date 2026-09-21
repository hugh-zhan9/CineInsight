package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

func imageHandlerTestCreateImage(t *testing.T, path, format string) *models.Image {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取图片夹具失败: %v", err)
	}
	img := &models.Image{
		Name:      filepath.Base(path),
		Path:      path,
		Directory: filepath.Dir(path),
		Size:      info.Size(),
		Format:    format,
	}
	if err := database.DB.Create(img).Error; err != nil {
		t.Fatalf("创建图片记录失败: %v", err)
	}
	return img
}

func TestImageAssetRoutesMethodNotAllowed(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()
	handler := newAssetHandler(app)

	for _, path := range []string{"/preview/image/1", "/preview/image-thumbnail/1"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s POST 期望 405，实际 %d", path, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Fatalf("%s Allow 头错误: %q", path, got)
		}
	}
}

func TestImageAssetRoutesRejectInvalidID(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()
	handler := newAssetHandler(app)

	for _, path := range []string{
		"/preview/image/abc",
		"/preview/image/",
		"/preview/image-thumbnail/abc",
		"/preview/image-thumbnail/",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 期望 400，实际 %d", path, rec.Code)
		}
	}
}

func TestImageAssetRoutesMissingImageReturns404(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()
	handler := newAssetHandler(app)

	for _, path := range []string{"/preview/image/9999", "/preview/image-thumbnail/9999"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 期望 404，实际 %d", path, rec.Code)
		}
	}
}

func TestImageThumbnailHandlerServesGeneratedJPEG(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	sourcePath := filepath.Join(root, "photo.png")
	if err := os.WriteFile(sourcePath, []byte("fake-png"), 0644); err != nil {
		t.Fatalf("写入图片夹具失败: %v", err)
	}
	img := imageHandlerTestCreateImage(t, sourcePath, "png")

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("创建 ffmpeg stub 目录失败: %v", err)
	}
	ffmpegScript := "#!/bin/bash\ndestination=\"${@: -1}\"\nprintf 'jpeg-image-thumbnail' > \"$destination\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "ffmpeg"), []byte(ffmpegScript), 0755); err != nil {
		t.Fatalf("写入 ffmpeg stub 失败: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	app := NewApp()
	app.imageThumbnail = services.NewImageThumbnailService(root)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/preview/image-thumbnail/%d", img.ID), nil)
	rec := httptest.NewRecorder()
	newAssetHandler(app).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("content-type 错误: got=%s want=image/jpeg", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=300" {
		t.Fatalf("cache-control 错误: %q", got)
	}
	if rec.Body.String() != "jpeg-image-thumbnail" {
		t.Fatalf("缩略图响应体错误: %q", rec.Body.String())
	}
}

func TestImageThumbnailHandlerUnsupportedDecodeReturns404(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	sourcePath := filepath.Join(root, "photo.heic")
	if err := os.WriteFile(sourcePath, []byte("fake-heic"), 0644); err != nil {
		t.Fatalf("写入图片夹具失败: %v", err)
	}
	img := imageHandlerTestCreateImage(t, sourcePath, "heic")

	app := NewApp()
	app.imageThumbnail = services.NewImageThumbnailService(root)
	// 注入非 darwin stub 的哨兵错误，使降级路径在 darwin 上同样可测。
	app.imageThumbnail.SetDecodeRunnersForTest(
		func(ctx context.Context, src, dst string, maxEdge int) error {
			return services.ErrImageDecodeUnsupported
		},
		func(ctx context.Context, src string) (int, int, error) {
			return 0, 0, services.ErrImageDecodeUnsupported
		},
	)

	for _, path := range []string{
		fmt.Sprintf("/preview/image-thumbnail/%d", img.ID),
		fmt.Sprintf("/preview/image/%d", img.ID),
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		newAssetHandler(app).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 期望 404，实际 %d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestImageViewHandlerServesOriginalFile(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	sourcePath := filepath.Join(root, "photo.png")
	content := []byte("fake-png-original-bytes")
	if err := os.WriteFile(sourcePath, content, 0644); err != nil {
		t.Fatalf("写入图片夹具失败: %v", err)
	}
	img := imageHandlerTestCreateImage(t, sourcePath, "png")

	app := NewApp()
	app.imageThumbnail = services.NewImageThumbnailService(root)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/preview/image/%d", img.ID), nil)
	rec := httptest.NewRecorder()
	newAssetHandler(app).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content-type 错误: got=%s want=image/png", got)
	}
	if rec.Body.String() != string(content) {
		t.Fatalf("响应体错误: got=%q want=%q", rec.Body.String(), string(content))
	}

	headReq := httptest.NewRequest(http.MethodHead, fmt.Sprintf("/preview/image/%d", img.ID), nil)
	headRec := httptest.NewRecorder()
	newAssetHandler(app).ServeHTTP(headRec, headReq)
	if headRec.Code != http.StatusOK {
		t.Fatalf("HEAD 期望 200，实际 %d", headRec.Code)
	}
	if headRec.Body.Len() != 0 {
		t.Fatalf("HEAD 不应返回响应体: %q", headRec.Body.String())
	}
	if !strings.Contains(headRec.Header().Get("Content-Type"), "image/png") {
		t.Fatalf("HEAD content-type 错误: %q", headRec.Header().Get("Content-Type"))
	}
}

// 人脸裁剪图路由（D-020）：只接受纯数字的观测 id，只读 faces 目录内的文件。
func TestFaceCropRouteMethodAndIDBoundaries(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()
	handler := newAssetHandler(app)

	req := httptest.NewRequest(http.MethodPost, "/preview/face-crop/1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 期望 405，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow 头错误: %q", got)
	}

	// 路径穿越在 id 解析这一步就被挡住：这里只可能是一个十进制整数。
	for _, path := range []string{
		"/preview/face-crop/abc",
		"/preview/face-crop/",
		"/preview/face-crop/../secret.jpg",
		"/preview/face-crop/1/../../etc/passwd",
		"/preview/face-crop/-1",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 期望 400，实际 %d", path, rec.Code)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/preview/face-crop/9999", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的观测期望 404，实际 %d", rec.Code)
	}
}

func TestFaceCropRouteServesCropAndRefusesEscapes(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	app := NewApp()
	app.faceAnalysis = services.NewFaceAnalysisService(root, nil, nil)

	facesDir := app.faceAnalysis.FacesDir()
	if err := os.MkdirAll(facesDir, 0o755); err != nil {
		t.Fatalf("建裁剪图目录失败: %v", err)
	}
	observation := models.FaceObservation{
		MediaKind: models.FaceMediaKindImage, MediaID: 1, SourceFingerprint: "1-1",
		BBox: "0,0,1,1", BBoxHash: "000000999999", AppendStatus: models.FaceAppendStatusNone,
	}
	if err := database.DB.Create(&observation).Error; err != nil {
		t.Fatalf("建观测失败: %v", err)
	}
	cropName := fmt.Sprintf("%d.jpg", observation.ID)
	if err := os.WriteFile(filepath.Join(facesDir, cropName), []byte("jpeg-face-crop"), 0o644); err != nil {
		t.Fatalf("写裁剪图失败: %v", err)
	}
	if err := database.DB.Model(&models.FaceObservation{}).Where("id = ?", observation.ID).
		Update("crop_path", cropName).Error; err != nil {
		t.Fatalf("写 crop_path 失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/preview/face-crop/%d", observation.ID), nil)
	rec := httptest.NewRecorder()
	newAssetHandler(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("content-type 错误: %q", got)
	}
	if rec.Body.String() != "jpeg-face-crop" {
		t.Fatalf("响应体错误: %q", rec.Body.String())
	}

	// 库里的 crop_path 指到目录外：路由必须当作不存在，不能顺着读出去。
	outside := filepath.Join(root, "outside.jpg")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("写目录外文件失败: %v", err)
	}
	if err := database.DB.Model(&models.FaceObservation{}).Where("id = ?", observation.ID).
		Update("crop_path", "../outside.jpg").Error; err != nil {
		t.Fatalf("改 crop_path 失败: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/preview/face-crop/%d", observation.ID), nil)
	rec = httptest.NewRecorder()
	newAssetHandler(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("越界的 crop_path 期望 404，实际 %d body=%s", rec.Code, rec.Body.String())
	}
}

// ---- 年度电影榜单海报代理（需求设计文档 §6.2，D-MC09）----
//
// 全部用例都对着 httptest 桩服务跑，**不向 douban.com / doubanio.com 发真实请求**。
// 注入点只有「客户端怎么把请求发出去」这一层：库里存的仍是真实的
// https://img2.doubanio.com/... 形态，域名白名单照常在发请求之前跑一遍。

// doubanChartPosterStubTransport 把已经通过校验的请求改投到桩服务，并按序记下
// 原始地址，供用例断言路由到底要去取哪一个 URL。
type doubanChartPosterStubTransport struct {
	base  *url.URL
	mu    sync.Mutex
	calls []string
}

func (s *doubanChartPosterStubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.calls = append(s.calls, request.URL.String())
	s.mu.Unlock()
	routed := request.Clone(request.Context())
	routed.URL.Scheme = s.base.Scheme
	routed.URL.Host = s.base.Host
	routed.Host = ""
	return http.DefaultTransport.RoundTrip(routed)
}

func (s *doubanChartPosterStubTransport) requested() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func useDoubanChartPosterStub(t *testing.T, handler http.HandlerFunc) *doubanChartPosterStubTransport {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("解析桩服务地址失败: %v", err)
	}
	transport := &doubanChartPosterStubTransport{base: base}
	previous := doubanChartPosterClientProvider
	doubanChartPosterClientProvider = func() (*http.Client, error) {
		return &http.Client{Transport: transport}, nil
	}
	t.Cleanup(func() { doubanChartPosterClientProvider = previous })
	return transport
}

func createChartEntryForTest(t *testing.T, doubanID, posterURL string) {
	t.Helper()
	entry := models.MovieChartEntry{
		DoubanID:     doubanID,
		Year:         2026,
		Title:        "榜单片名",
		PosterURL:    posterURL,
		ReleaseScope: models.MovieChartScopeTheatrical,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("创建榜单条目失败: %v", err)
	}
}

func createChartMarkForTest(t *testing.T, doubanID, posterURL string) {
	t.Helper()
	mark := models.MovieChartMark{
		DoubanID:    doubanID,
		Mark:        models.MovieChartMarkWatched,
		ReleaseYear: 2026,
		Title:       "榜单片名",
		PosterURL:   posterURL,
		MarkedAt:    time.Now(),
	}
	if err := database.DB.Create(&mark).Error; err != nil {
		t.Fatalf("创建榜单标记失败: %v", err)
	}
}

func TestDoubanChartPosterRouteMethodAndIDBoundaries(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()
	handler := newAssetHandler(app)

	req := httptest.NewRequest(http.MethodPost, "/preview/douban-chart-poster/1292052", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 期望 405，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow 头错误: %q", got)
	}

	// 只认 ^[0-9]{1,16}$：路径穿越、超长值与任何非数字都停在解析这一步。
	for _, path := range []string{
		"/preview/douban-chart-poster/",
		"/preview/douban-chart-poster/abc",
		"/preview/douban-chart-poster/12a",
		"/preview/douban-chart-poster/-1",
		"/preview/douban-chart-poster/12345678901234567",
		"/preview/douban-chart-poster/../secret.jpg",
		"/preview/douban-chart-poster/1/../../etc/passwd",
		"/preview/douban-chart-poster/1292052.jpg",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 期望 400，实际 %d", path, rec.Code)
		}
	}
}

func TestDoubanChartPosterRouteMissingAddressReturns404(t *testing.T) {
	setupAppTestDB(t)
	transport := useDoubanChartPosterStub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("库里没有地址时不应发出上游请求: %s", r.URL)
	})
	// 条目在库里但 poster_url 为空，同样是「没有这张图」。
	createChartEntryForTest(t, "1000001", "")
	handler := newAssetHandler(NewApp())

	for _, path := range []string{
		"/preview/douban-chart-poster/9999999", // 两张表都没有这一行
		"/preview/douban-chart-poster/1000001", // 有行但地址为空
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 期望 404，实际 %d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if calls := transport.requested(); len(calls) != 0 {
		t.Fatalf("不应发出上游请求，实际: %v", calls)
	}
}

// TestDoubanChartPosterRouteRejectsAddressesOutsideWhitelist 是这条路由的安全用例
// （TC-11）：库里的地址来自豆瓣响应，仍是外部输入，取出来之后必须再过一次白名单。
// 其中 evildoubanio.com 一条专门钉住「裸 HasSuffix(host, "doubanio.com")」这个经典
// 错误——把这个判定换成裸 HasSuffix，本用例立刻失败。
func TestDoubanChartPosterRouteRejectsAddressesOutsideWhitelist(t *testing.T) {
	setupAppTestDB(t)
	transport := useDoubanChartPosterStub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("白名单外的地址不应发出上游请求: %s", r.URL)
	})

	cases := []struct {
		id      string
		address string
		reason  string
	}{
		{"2000001", "https://evildoubanio.com/p1.jpg", "后缀近似的外部域名"},
		{"2000002", "https://doubanio.com.evil.com/p1.jpg", "域名前缀伪装"},
		{"2000003", "http://img2.doubanio.com/p1.jpg", "明文 http"},
		{"2000004", "https://user:pass@img2.doubanio.com/p1.jpg", "地址内嵌凭证"},
		{"2000005", "https://img2.doubanio.com:8080/p1.jpg", "显式端口"},
		{"2000006", "file:///etc/passwd", "本地文件协议"},
		{"2000007", "https://127.0.0.1/p1.jpg", "回环地址"},
		{"2000008", "https://169.254.169.254/latest/meta-data/", "元数据服务"},
		{"2000009", "   ", "空白地址"},
		{"2000010", "https:///p1.jpg", "没有主机名"},
	}
	for _, testCase := range cases {
		createChartEntryForTest(t, testCase.id, testCase.address)
	}
	handler := newAssetHandler(NewApp())

	for _, testCase := range cases {
		req := httptest.NewRequest(http.MethodGet, "/preview/douban-chart-poster/"+testCase.id, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s（%s）期望 404，实际 %d body=%s", testCase.address, testCase.reason, rec.Code, rec.Body.String())
		}
	}
	if calls := transport.requested(); len(calls) != 0 {
		t.Fatalf("白名单外的地址不应发出任何上游请求，实际: %v", calls)
	}
}

func TestDoubanChartPosterRouteProxiesStoredAddress(t *testing.T) {
	setupAppTestDB(t)
	const stored = "https://img2.doubanio.com/view/photo/s_ratio_poster/public/p2900000.jpg"
	createChartEntryForTest(t, "1292052", stored)

	transport := useDoubanChartPosterStub(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Referer"); got != "https://img2.doubanio.com/" {
			t.Errorf("Referer 错误（末尾斜杠是必须的）: %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != services.WatchlistPosterUserAgent {
			t.Errorf("User-Agent 错误: %q", got)
		}
		if r.URL.Path != "/view/photo/s_ratio_poster/public/p2900000.jpg" {
			t.Errorf("上游路径错误: %q", r.URL.Path)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("上游不应带查询串: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg-chart-poster"))
	})

	handler := newAssetHandler(NewApp())
	req := httptest.NewRequest(http.MethodGet, "/preview/douban-chart-poster/1292052", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("content-type 未透传: %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=604800" {
		t.Fatalf("cache-control 错误: %q", got)
	}
	// 上游的 Content-Type 是原样透传的，必须同时禁掉浏览器的类型嗅探——这条路由
	// 与应用自己的页面同源，一份伪装成图片的 HTML 被嗅探成文档就等于同源执行。
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("必须带 nosniff，实际 %q", got)
	}
	if rec.Body.String() != "jpeg-chart-poster" {
		t.Fatalf("响应体错误: %q", rec.Body.String())
	}

	// 调用方塞进来的查询串一概不参与取图：地址只从库里来。
	evil := httptest.NewRequest(http.MethodGet, "/preview/douban-chart-poster/1292052?url=http://127.0.0.1:9/evil.jpg&host=evil.com", nil)
	evilRec := httptest.NewRecorder()
	handler.ServeHTTP(evilRec, evil)
	if evilRec.Code != http.StatusOK {
		t.Fatalf("带查询串的请求期望 200，实际 %d", evilRec.Code)
	}

	calls := transport.requested()
	if len(calls) != 2 {
		t.Fatalf("期望两次上游请求，实际 %v", calls)
	}
	for _, call := range calls {
		if call != stored {
			t.Fatalf("上游地址必须与库里存的一致，实际 %q", call)
		}
	}
}

// TestDoubanChartPosterRouteRejectsSVG 钉住 SVG 不当海报发：它是 image/*，但能内嵌
// <script>，而这条路由与应用自己的页面同源。真实海报是 CDN 上的位图，排除 SVG
// 不损失任何用例。
func TestDoubanChartPosterRouteRejectsSVG(t *testing.T) {
	setupAppTestDB(t)
	createChartEntryForTest(t, "1292052", "https://img2.doubanio.com/view/photo/l/public/p1.svg")
	useDoubanChartPosterStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	})

	handler := newAssetHandler(NewApp())
	req := httptest.NewRequest(http.MethodGet, "/preview/douban-chart-poster/1292052", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("SVG 期望 502，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<script") {
		t.Fatalf("响应体不得回显上游内容: %q", rec.Body.String())
	}

	// 带参数段的写法一样要挡住，别让 charset 绕过判定。
	for _, contentType := range []string{"image/svg+xml; charset=utf-8", "IMAGE/SVG+XML"} {
		if doubanChartPosterServableImage(contentType) {
			t.Fatalf("%q 不该被当成可发的海报", contentType)
		}
	}
	for _, contentType := range []string{"image/jpeg", "image/webp; charset=binary", "IMAGE/PNG"} {
		if !doubanChartPosterServableImage(contentType) {
			t.Fatalf("%q 是正常位图，应当放行", contentType)
		}
	}
}

// TestDoubanChartPosterRoutePrefersEntryAddress 钉住取址顺序：缓存表在前、标记表
// 兜底。两张表都有地址时必须用缓存表那一个——它是最近一次抓取的结果，标记表存的
// 是**标记那一刻的快照**，可能已经是失效的旧地址。
func TestDoubanChartPosterRoutePrefersEntryAddress(t *testing.T) {
	setupAppTestDB(t)
	const fresh = "https://img2.doubanio.com/view/photo/l/public/fresh.jpg"
	const snapshot = "https://img9.doubanio.com/view/photo/l/public/stale.jpg"
	createChartEntryForTest(t, "1292052", fresh)
	createChartMarkForTest(t, "1292052", snapshot)

	transport := useDoubanChartPosterStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg-chart-poster"))
	})
	handler := newAssetHandler(NewApp())
	req := httptest.NewRequest(http.MethodGet, "/preview/douban-chart-poster/1292052", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
	calls := transport.requested()
	if len(calls) != 1 || calls[0] != fresh {
		t.Fatalf("两张表都有地址时必须用缓存表的那一个，实际 %v", calls)
	}
}

// TestDefaultDoubanChartPosterClientRebuildsOnProxyChange 钉住取图客户端按代理地址
// 缓存、**代理一改立刻重建**。
//
// 不重建的后果不是性能问题而是语义问题：用户在设置页配上代理之后，海报请求还在
// 用之前那个直连客户端出去，直到重启应用为止——而 NewWatchlistMetadataHTTPClient
// 存在的全部意义就是「配了代理就绝不静默直连」。
func TestDefaultDoubanChartPosterClientRebuildsOnProxyChange(t *testing.T) {
	setupAppTestDB(t)
	resetDoubanChartPosterClientCache(t)
	if err := database.DB.Create(&models.Settings{PlayWeight: 2}).Error; err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}

	direct, err := defaultDoubanChartPosterClient()
	if err != nil {
		t.Fatalf("装配直连客户端失败: %v", err)
	}
	again, err := defaultDoubanChartPosterClient()
	if err != nil {
		t.Fatalf("再次装配失败: %v", err)
	}
	if again != direct {
		t.Fatal("代理没变时应当复用同一个客户端，否则每张海报都要重做一次 TLS 握手")
	}

	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").
		Update("metadata_proxy_url", "http://127.0.0.1:18080").Error; err != nil {
		t.Fatalf("改代理设置失败: %v", err)
	}
	proxied, err := defaultDoubanChartPosterClient()
	if err != nil {
		t.Fatalf("装配走代理的客户端失败: %v", err)
	}
	if proxied == direct {
		t.Fatal("代理改了必须换客户端，否则海报请求会继续从旧的直连客户端裸奔出去")
	}
	if proxied.CheckRedirect == nil {
		t.Fatal("重建出来的客户端也要带跳转白名单策略")
	}

	// 代理地址填错时报错、**不退回直连**：这条与 D-MC01 是同一条语义。
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").
		Update("metadata_proxy_url", "ftp://127.0.0.1:9").Error; err != nil {
		t.Fatalf("改代理设置失败: %v", err)
	}
	if _, err := defaultDoubanChartPosterClient(); err == nil {
		t.Fatal("代理地址非法时必须报错，不能静默直连")
	}
}

// resetDoubanChartPosterClientCache 清掉进程级的取图客户端缓存，用完恢复。
// 这个缓存是包级变量，别的用例先跑过就会把它填上，不清的话本用例断言的
// 「同一个实例」「换了实例」都可能是上一个用例留下的。
func resetDoubanChartPosterClientCache(t *testing.T) {
	t.Helper()
	doubanChartPosterClientMu.Lock()
	previousClient, previousProxy := doubanChartPosterClient, doubanChartPosterClientProxy
	doubanChartPosterClient, doubanChartPosterClientProxy = nil, ""
	doubanChartPosterClientMu.Unlock()
	t.Cleanup(func() {
		doubanChartPosterClientMu.Lock()
		doubanChartPosterClient, doubanChartPosterClientProxy = previousClient, previousProxy
		doubanChartPosterClientMu.Unlock()
	})
}

func TestDoubanChartPosterRouteFallsBackToMarkTable(t *testing.T) {
	setupAppTestDB(t)
	useDoubanChartPosterStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write([]byte("webp-chart-poster"))
	})

	// 缓存被清空后只剩标记行；以及缓存行在但地址为空、标记行有地址。
	createChartMarkForTest(t, "3000001", "https://img1.doubanio.com/view/photo/l/public/p1.jpg")
	createChartEntryForTest(t, "3000002", "")
	createChartMarkForTest(t, "3000002", "https://img9.doubanio.com/view/photo/l/public/p2.jpg")

	handler := newAssetHandler(NewApp())
	for _, id := range []string{"3000001", "3000002"} {
		req := httptest.NewRequest(http.MethodGet, "/preview/douban-chart-poster/"+id, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 期望 200，实际 %d body=%s", id, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != "image/webp" {
			t.Fatalf("%s content-type 未透传: %q", id, got)
		}
		if rec.Body.String() != "webp-chart-poster" {
			t.Fatalf("%s 响应体错误: %q", id, rec.Body.String())
		}
	}
}

func TestDoubanChartPosterRouteUpstreamFailuresReturn502(t *testing.T) {
	setupAppTestDB(t)
	createChartEntryForTest(t, "4000001", "https://img2.doubanio.com/view/photo/l/public/p1.jpg")

	mode := "status"
	useDoubanChartPosterStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "status":
			http.Error(w, "forbidden", http.StatusForbidden)
		case "html":
			// 防盗链页与验证页都是 200 + text/html。
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html>418</html>"))
		case "oversize":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(make([]byte, services.ManagedImageMaxBytes+1))
		case "empty":
			w.Header().Set("Content-Type", "image/jpeg")
		case "notype":
			_, _ = w.Write([]byte("jpeg-without-content-type"))
		}
	})

	handler := newAssetHandler(NewApp())
	for _, name := range []string{"status", "html", "oversize", "empty", "notype"} {
		mode = name
		req := httptest.NewRequest(http.MethodGet, "/preview/douban-chart-poster/4000001", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway {
			// 不打印响应体：oversize 一支带着 20 MiB。
			t.Fatalf("%s 期望 502，实际 %d（响应体 %d 字节）", name, rec.Code, rec.Body.Len())
		}
	}

	// 刚好压线的响应体仍然放行：上限是「超过才拒」。
	mode = "limit"
	limitBody := make([]byte, 1024)
	useDoubanChartPosterStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(limitBody)
	})
	req := httptest.NewRequest(http.MethodGet, "/preview/douban-chart-poster/4000001", nil)
	rec := httptest.NewRecorder()
	newAssetHandler(NewApp()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("正常响应期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != len(limitBody) {
		t.Fatalf("响应体长度错误: %d", rec.Body.Len())
	}
}

// 出网客户端装配失败（多半是资料源出网代理地址填错）不退回直连，也不当成上游故障。
func TestDoubanChartPosterRouteClientFailureReturns500(t *testing.T) {
	setupAppTestDB(t)
	createChartEntryForTest(t, "5000001", "https://img2.doubanio.com/view/photo/l/public/p1.jpg")

	previous := doubanChartPosterClientProvider
	doubanChartPosterClientProvider = func() (*http.Client, error) {
		return nil, services.ErrWatchlistMetadataProxyInvalid
	}
	t.Cleanup(func() { doubanChartPosterClientProvider = previous })

	req := httptest.NewRequest(http.MethodGet, "/preview/douban-chart-poster/5000001", nil)
	rec := httptest.NewRecorder()
	newAssetHandler(NewApp()).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("期望 500，实际 %d body=%s", rec.Code, rec.Body.String())
	}
}

// 跳转也必须受白名单约束：否则上游一个 302 就能把这条路由变成跳板。
func TestDoubanChartPosterRedirectPolicyKeepsWhitelist(t *testing.T) {
	allowed := []string{
		"https://img1.doubanio.com/view/photo/l/public/p1.jpg",
		"https://doubanio.com/p1.jpg",
	}
	for _, address := range allowed {
		request := httptest.NewRequest(http.MethodGet, address, nil)
		if err := doubanChartPosterRedirectPolicy(request, nil); err != nil {
			t.Fatalf("%s 应放行，实际 %v", address, err)
		}
	}

	refused := []string{
		"http://169.254.169.254/latest/meta-data/",
		"https://evildoubanio.com/p1.jpg",
		"http://img2.doubanio.com/p1.jpg",
		"https://127.0.0.1:8080/p1.jpg",
	}
	for _, address := range refused {
		request := httptest.NewRequest(http.MethodGet, address, nil)
		if err := doubanChartPosterRedirectPolicy(request, nil); err == nil {
			t.Fatalf("%s 应被拒绝", address)
		}
	}

	// 跳转次数上限。
	loop := httptest.NewRequest(http.MethodGet, "https://img1.doubanio.com/p1.jpg", nil)
	via := make([]*http.Request, doubanChartPosterMaxRedirects)
	if err := doubanChartPosterRedirectPolicy(loop, via); err == nil {
		t.Fatal("超过跳转上限应被拒绝")
	}
}

// 生产客户端确实装上了跳转策略，且超时按海报用途给。
func TestDefaultDoubanChartPosterClientCarriesRedirectPolicy(t *testing.T) {
	setupAppTestDB(t)
	client, err := defaultDoubanChartPosterClient()
	if err != nil {
		t.Fatalf("装配取图客户端失败: %v", err)
	}
	if client.CheckRedirect == nil {
		t.Fatal("取图客户端必须带跳转白名单策略")
	}
	if client.Timeout != doubanChartPosterTimeout {
		t.Fatalf("超时错误: %v", client.Timeout)
	}
}

// 失败时必须留下一行可 grep 的诊断，成功时必须一行都不留。
//
// 这条路由的失败全发生在远端（第三方 CDN 的防盗链、用户自配的出网代理），海报又
// 不落盘，日志是「封面不显示」唯一的排查入口。同时钉住不该出现的东西：海报地址、
// 主机名与查询串一律不得进日志。
func TestDoubanChartPosterFailureLogging(t *testing.T) {
	setupAppTestDB(t)
	createChartEntryForTest(t, "4100001", "https://img2.doubanio.com/view/photo/l/public/p1.jpg")
	createChartEntryForTest(t, "4100002", "https://evildoubanio.com/p1.jpg") // 过不了白名单
	createChartEntryForTest(t, "4100003", "")                                // 库里没有地址

	mode := "status"
	useDoubanChartPosterStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "status":
			http.Error(w, "forbidden", http.StatusForbidden)
		case "html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html>418</html>"))
		default:
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("jpeg-chart-poster"))
		}
	})
	handler := newAssetHandler(NewApp())

	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	fetch := func(doubanID string) {
		req := httptest.NewRequest(http.MethodGet, "/preview/douban-chart-poster/"+doubanID+"?url=http://127.0.0.1:9/evil.jpg", nil)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	for _, expectation := range []struct {
		mode   string
		wanted []string
	}{
		{"status", []string{"[MovieChart] source=douban_poster", "douban_id=4100001", "status=403", "bytes=0", "elapsed_ms=", "reason=upstream_status"}},
		{"html", []string{"douban_id=4100001", "status=200", "reason=not_image"}},
	} {
		logs.Reset()
		mode = expectation.mode
		fetch("4100001")
		line := logs.String()
		for _, want := range expectation.wanted {
			if !strings.Contains(line, want) {
				t.Fatalf("%s 的日志缺少 %q，实际: %q", expectation.mode, want, line)
			}
		}
		// 出网地址、主机名与调用方查询串都不得落进日志。
		for _, forbidden := range []string{"doubanio.com", "view/photo", "127.0.0.1", "url="} {
			if strings.Contains(line, forbidden) {
				t.Fatalf("%s 的日志泄露了 %q: %q", expectation.mode, forbidden, line)
			}
		}
	}

	// 出网客户端装配失败（代理地址填错）同样要留一行。
	logs.Reset()
	restore := doubanChartPosterClientProvider
	doubanChartPosterClientProvider = func() (*http.Client, error) {
		return nil, services.ErrWatchlistMetadataProxyInvalid
	}
	fetch("4100001")
	doubanChartPosterClientProvider = restore
	if !strings.Contains(logs.String(), "reason=client_unavailable") {
		t.Fatalf("装配失败应记 client_unavailable，实际: %q", logs.String())
	}

	// 库里的地址没过白名单：这不是「没有这张图」，是缓存里存着一个不该存在的地址，
	// 也是一道安全控制真的触发了，必须留痕——但被拒的地址本身一个字都不能进日志。
	logs.Reset()
	mode = "ok"
	fetch("4100002")
	rejected := logs.String()
	for _, want := range []string{"douban_id=4100002", "status=0", "bytes=0", "elapsed_ms=", "reason=address_rejected"} {
		if !strings.Contains(rejected, want) {
			t.Fatalf("白名单拒绝的日志缺少 %q，实际: %q", want, rejected)
		}
	}
	for _, forbidden := range []string{"evildoubanio", "doubanio.com", "https", "p1.jpg", "://"} {
		if strings.Contains(rejected, forbidden) {
			t.Fatalf("白名单拒绝的日志泄露了 %q: %q", forbidden, rejected)
		}
	}

	// 另外两种「没有这张图」保持安静：库里压根没有地址，以及 ID 本身不合法。
	logs.Reset()
	fetch("4100003")
	fetch("9999999")
	fetch("abc")
	if logs.Len() != 0 {
		t.Fatalf("空结果与非法 ID 不应记日志，实际: %q", logs.String())
	}

	// 成功路径一行都不记：一页榜单几十张图。
	logs.Reset()
	fetch("4100001")
	if logs.Len() != 0 {
		t.Fatalf("成功路径不应记日志，实际: %q", logs.String())
	}
}
