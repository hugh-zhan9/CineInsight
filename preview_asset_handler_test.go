package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// ---- 年度电影榜单海报（需求设计文档 §6.2，D-MC14 取代 D-MC09）----
//
// 这条路由现在是**纯本地磁盘读**，实现里没有任何 HTTP 客户端。本节的用例因此不再
// 装桩服务，而是反过来：把 http.DefaultTransport 换成一个一被用到就判失败的陷阱，
// 再让路由去取一张**库里有远程地址、本地却没有文件**的海报——改回即时回源的实现，
// 这两条会同时红（一条是陷阱被触发，一条是它没有回 404）。

// noEgressTransport 是「渲染路径不许出网」的陷阱：被调用就判用例失败。
type noEgressTransport struct {
	t *testing.T
}

func (n *noEgressTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	n.t.Errorf("渲染路径不得出网，却发起了请求: %s", request.URL)
	return nil, fmt.Errorf("渲染路径不得出网")
}

// forbidEgress 在用例期间掐掉进程默认的出网能力。
//
// 它盯的不只是这条路由自己写没写客户端：http.Get / http.DefaultClient 这类「顺手
// 一句」的出网同样走 DefaultTransport，换掉它之后任何一处都会当场暴露。
func forbidEgress(t *testing.T) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = &noEgressTransport{t: t}
	t.Cleanup(func() { http.DefaultTransport = previous })
}

// newChartPosterApp 起一个海报落在临时目录里的应用实例。
//
// 照 TestPersonAvatarHandlerServesOnlyManagedEntityAsset 的做法换掉服务字段：
// NewApp 里的那个指向用户真实的数据目录，用例不能往那里写东西。
func newChartPosterApp(t *testing.T) (*App, string) {
	t.Helper()
	dataDir := t.TempDir()
	app := NewApp()
	app.movieChart = services.NewMovieChartService(database.DB, services.NewWatchlistService(dataDir))
	return app, dataDir
}

// writeChartPosterFile 往托管目录里放一张海报，返回托管相对路径。
func writeChartPosterFile(t *testing.T, dataDir string, entryID uint, name string, content []byte) string {
	t.Helper()
	relative := filepath.Join("movie_chart", fmt.Sprintf("%d", entryID), name)
	full := filepath.Join(dataDir, "media-details", relative)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("建立海报目录失败: %v", err)
	}
	if err := os.WriteFile(full, content, 0644); err != nil {
		t.Fatalf("写入海报文件失败: %v", err)
	}
	return filepath.ToSlash(relative)
}

func chartPosterJPEG(filler byte) []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{filler}, 60)...)
}

// createChartEntryForTest 写一条缓存条目。签名保持两个值参不变——
// app_movie_chart_test.go 也在用它，那个文件不属于本切片。
func createChartEntryForTest(t *testing.T, doubanID, posterURL string) models.MovieChartEntry {
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
	return entry
}

// setChartPosterPathForTest 给条目写上托管相对路径。
func setChartPosterPathForTest(t *testing.T, entry models.MovieChartEntry, posterPath string) {
	t.Helper()
	if err := database.DB.Model(&models.MovieChartEntry{}).
		Where("id = ?", entry.ID).Update("poster_path", posterPath).Error; err != nil {
		t.Fatalf("写入托管路径失败: %v", err)
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
	app, _ := newChartPosterApp(t)
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

// TestDoubanChartPosterRouteServesLocalFile 钉住命中的那一面：图来自本地托管目录，
// 一次出网都没有。
func TestDoubanChartPosterRouteServesLocalFile(t *testing.T) {
	setupAppTestDB(t)
	forbidEgress(t)
	app, dataDir := newChartPosterApp(t)
	content := chartPosterJPEG('a')
	entry := createChartEntryForTest(t, "1292052",
		"https://img2.doubanio.com/view/photo/s_ratio_poster/public/p2900000.jpg")
	setChartPosterPathForTest(t, entry, writeChartPosterFile(t, dataDir, entry.ID, "poster.jpg", content))

	handler := newAssetHandler(app)
	req := httptest.NewRequest(http.MethodGet,
		"/preview/douban-chart-poster/1292052?url=http://127.0.0.1:9/evil.jpg", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, content) {
		t.Fatalf("响应体不是本地那张图: %d 字节", len(got))
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("content-type = %q", got)
	}
	// 落盘之后不再有「用 HTTP 缓存挡住重复回源」这回事：文件就在本地，而 URL 是按
	// 豆瓣 ID 给的、不带内容摘要，缓存住只会在换图之后继续发旧图。
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q，期望 no-store", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("必须带 nosniff，实际 %q", got)
	}
}

// TestDoubanChartPosterRouteNeverFetchesOnMiss 是本切片最要紧的一条：
// **未命中就是 404，绝不即时回源**。
//
// 三种未命中都在这里：库里有合法的豆瓣图床地址但还没落盘（回源实现会去取它，
// 因此这一条同时钉住「渲染路径零出网」）、poster_path 指向已经不存在的文件
// （被 LRU 淘汰或手工删了）、以及库里压根没有这一行。
// 标记表那一行也不再兜底取图：已看页的图同样只来自缓存行引用的本地文件。
func TestDoubanChartPosterRouteNeverFetchesOnMiss(t *testing.T) {
	setupAppTestDB(t)
	forbidEgress(t)
	app, dataDir := newChartPosterApp(t)

	// 1) 有合法远程地址、没有本地文件。
	createChartEntryForTest(t, "1000001",
		"https://img2.doubanio.com/view/photo/l/public/p1.jpg")
	// 2) poster_path 指向已经不在的文件。
	setChartPosterPathForTest(t, createChartEntryForTest(t, "1000002", ""), "movie_chart/77/gone.jpg")
	// 3) poster_path 想跑出托管根。
	setChartPosterPathForTest(t, createChartEntryForTest(t, "1000003", ""), "../../../etc/passwd")
	// 4) 只有标记行的快照地址（缓存被清空后的已看条目）。
	createChartMarkForTest(t, "1000004", "https://img9.doubanio.com/view/photo/l/public/p4.jpg")
	// 5) 两张表都没有这一行。
	_ = dataDir

	handler := newAssetHandler(app)
	for _, doubanID := range []string{"1000001", "1000002", "1000003", "1000004", "9999999"} {
		req := httptest.NewRequest(http.MethodGet, "/preview/douban-chart-poster/"+doubanID, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 期望 404（未命中不回源），实际 %d body=%s", doubanID, rec.Code, rec.Body.String())
		}
	}
}

// TestDoubanChartPosterRouteRejectsForeignManagedPath 钉住托管路径的越界判定：
// poster_path 来自本地库，但它终归是一个字符串，指到托管根之外就是 404。
func TestDoubanChartPosterRouteRejectsForeignManagedPath(t *testing.T) {
	setupAppTestDB(t)
	forbidEgress(t)
	app, dataDir := newChartPosterApp(t)
	outside := filepath.Join(t.TempDir(), "secret.jpg")
	if err := os.WriteFile(outside, chartPosterJPEG('b'), 0644); err != nil {
		t.Fatalf("写入外部文件失败: %v", err)
	}
	relative, err := filepath.Rel(filepath.Join(dataDir, "media-details"), outside)
	if err != nil {
		t.Fatalf("计算相对路径失败: %v", err)
	}
	setChartPosterPathForTest(t, createChartEntryForTest(t, "1100001", ""), filepath.ToSlash(relative))

	req := httptest.NewRequest(http.MethodGet, "/preview/douban-chart-poster/1100001", nil)
	rec := httptest.NewRecorder()
	newAssetHandler(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("越界的 poster_path 期望 404，实际 %d", rec.Code)
	}
}

// TestDoubanChartPosterRouteLogsOnlyRealFailures 钉住诊断出口：
// 只有库或托管目录真的出问题才记一行，寻常的「没有这张图」保持安静
// ——一页榜单几十张卡片，把未命中也记上就只是噪声。
func TestDoubanChartPosterRouteLogsOnlyRealFailures(t *testing.T) {
	setupAppTestDB(t)
	forbidEgress(t)
	app, dataDir := newChartPosterApp(t)
	entry := createChartEntryForTest(t, "4100001", "")
	setChartPosterPathForTest(t, entry, writeChartPosterFile(t, dataDir, entry.ID, "poster.jpg", chartPosterJPEG('c')))
	handler := newAssetHandler(app)

	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	fetch := func(doubanID string) int {
		req := httptest.NewRequest(http.MethodGet,
			"/preview/douban-chart-poster/"+doubanID+"?url=http://127.0.0.1:9/evil.jpg", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	// 命中、未命中、非法 ID 都不记。
	if code := fetch("4100001"); code != http.StatusOK {
		t.Fatalf("命中期望 200，实际 %d", code)
	}
	fetch("9999999")
	fetch("abc")
	if logs.Len() != 0 {
		t.Fatalf("成功与寻常未命中都不该记日志，实际: %q", logs.String())
	}

	// 库本身出问题才记：把表摘掉制造一次读取失败。
	if err := database.DB.Migrator().DropTable(&models.MovieChartEntry{}); err != nil {
		t.Fatalf("删表失败: %v", err)
	}
	logs.Reset()
	if code := fetch("4100001"); code != http.StatusInternalServerError {
		t.Fatalf("读库失败期望 500，实际 %d", code)
	}
	line := logs.String()
	for _, want := range []string{"[MovieChart] source=douban_poster", "douban_id=4100001", "elapsed_ms=", "reason=resolve_failed"} {
		if !strings.Contains(line, want) {
			t.Fatalf("故障日志缺少 %q，实际: %q", want, line)
		}
	}
	// 调用方塞进来的查询串不得进日志。
	if strings.Contains(line, "url=") || strings.Contains(line, "127.0.0.1") {
		t.Fatalf("日志泄露了调用方查询串: %q", line)
	}
}
