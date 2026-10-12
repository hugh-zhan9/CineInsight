package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
	"video-master/services"
)

// 海报与背景图路由（D-UI-12）的夹具：NewApp 里的服务指向用户真实的数据目录，
// 这里换成临时目录；缩略图回落用 ffmpeg 桩生成一张固定内容的「缩略图」。
type artworkFixture struct {
	app     *App
	handler http.Handler
	dataDir string
	video   models.Video
}

func newArtworkFixture(t *testing.T) artworkFixture {
	t.Helper()
	setupAppTestDB(t)
	root := t.TempDir()
	dataDir := t.TempDir()
	videoPath := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("fake-video"), 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}
	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Size: 10, Duration: 30}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("创建 ffmpeg stub 目录失败: %v", err)
	}
	ffmpegScript := "#!/bin/bash\ndestination=\"${@: -1}\"\nprintf 'jpeg-thumbnail' > \"$destination\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "ffmpeg"), []byte(ffmpegScript), 0755); err != nil {
		t.Fatalf("写入 ffmpeg stub 失败: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	app := NewApp()
	app.thumbnailService = services.NewThumbnailService(app.videoService, root)
	app.localMetadata = services.NewLocalMetadataService(dataDir, services.NewPersonService(dataDir), services.NewCollectionService(dataDir))
	return artworkFixture{app: app, handler: newAssetHandler(app), dataDir: dataDir, video: video}
}

// writeArtwork 往托管目录放一张图并把相对路径写到视频的对应列上，返回图片的绝对路径。
func (f artworkFixture) writeArtwork(t *testing.T, column, name string, content []byte) string {
	t.Helper()
	relative := filepath.Join("videos", fmt.Sprintf("%d", f.video.ID), name)
	full := filepath.Join(f.dataDir, "media-details", relative)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("建立图片目录失败: %v", err)
	}
	if err := os.WriteFile(full, content, 0644); err != nil {
		t.Fatalf("写入图片失败: %v", err)
	}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", f.video.ID).Update(column, filepath.ToSlash(relative)).Error; err != nil {
		t.Fatalf("写入 %s 失败: %v", column, err)
	}
	return full
}

func (f artworkFixture) get(t *testing.T, path string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for key, values := range header {
		req.Header[key] = values
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func artworkPNG() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 40)...)
}

func TestVideoArtworkRouteServesPosterWithThumbnailCacheHeaders(t *testing.T) {
	fixture := newArtworkFixture(t)
	poster := chartPosterJPEG(0x11)
	fixture.writeArtwork(t, "poster_path", "poster.jpg", poster)

	rec := fixture.get(t, fmt.Sprintf("/preview/video-artwork/%d?kind=poster", fixture.video.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("content-type 错误: %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=300" {
		t.Fatalf("cache-control 应与缩略图一致: %q", got)
	}
	if got := rec.Header().Get("X-Artwork-Fallback"); got != "" {
		t.Fatalf("有海报时不应带回落头: %q", got)
	}
	if rec.Body.String() != string(poster) {
		t.Fatalf("应返回海报原图，实际 %q", rec.Body.String())
	}
}

func TestVideoArtworkRouteFallsBackToThumbnailWhenPosterMissing(t *testing.T) {
	fixture := newArtworkFixture(t)
	assertThumbnailFallback := func(label string) {
		t.Helper()
		rec := fixture.get(t, fmt.Sprintf("/preview/video-artwork/%d?kind=poster", fixture.video.ID), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s：期望 200，实际 %d body=%s", label, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Artwork-Fallback"); got != "thumbnail" {
			t.Fatalf("%s：回落头错误: %q", label, got)
		}
		if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
			t.Fatalf("%s：content-type 错误: %q", label, got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "private, max-age=300" {
			t.Fatalf("%s：cache-control 错误: %q", label, got)
		}
		if rec.Body.String() != "jpeg-thumbnail" {
			t.Fatalf("%s：应返回与缩略图路由相同的缩略图，实际 %q", label, rec.Body.String())
		}
	}
	// 没导入过海报。
	assertThumbnailFallback("未导入")
	// 导入过，但文件已经不在了。
	full := fixture.writeArtwork(t, "poster_path", "poster.jpg", chartPosterJPEG(0x22))
	if err := os.Remove(full); err != nil {
		t.Fatalf("删除海报失败: %v", err)
	}
	assertThumbnailFallback("文件已不在")

	thumb := fixture.get(t, fmt.Sprintf("/preview/thumbnail/%d", fixture.video.ID), nil)
	if thumb.Body.String() != "jpeg-thumbnail" || thumb.Header().Get("X-Artwork-Fallback") != "" {
		t.Fatalf("缩略图路由本身不应受影响: body=%q fallback=%q", thumb.Body.String(), thumb.Header().Get("X-Artwork-Fallback"))
	}
}

func TestVideoArtworkRouteServesFanartAndReturns404WhenAbsent(t *testing.T) {
	fixture := newArtworkFixture(t)
	path := fmt.Sprintf("/preview/video-artwork/%d?kind=fanart", fixture.video.ID)
	if rec := fixture.get(t, path, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("没有背景图期望 404，实际 %d", rec.Code)
	}

	fanart := artworkPNG()
	fixture.writeArtwork(t, "fanart_path", "fanart.png", fanart)
	rec := fixture.get(t, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content-type 错误: %q", got)
	}
	if got := rec.Header().Get("X-Artwork-Fallback"); got != "" {
		t.Fatalf("背景图不回落: %q", got)
	}
	if rec.Body.String() != string(fanart) {
		t.Fatalf("应返回背景图原图")
	}
	// 背景图在、海报不在：海报照样回落缩略图，不会拿背景图顶替。
	poster := fixture.get(t, fmt.Sprintf("/preview/video-artwork/%d?kind=poster", fixture.video.ID), nil)
	if poster.Body.String() != "jpeg-thumbnail" {
		t.Fatalf("海报应回落缩略图，实际 %q", poster.Body.String())
	}
}

func TestVideoArtworkRouteRejectsBadRequestsAndUnknownVideos(t *testing.T) {
	fixture := newArtworkFixture(t)
	fixture.writeArtwork(t, "poster_path", "poster.jpg", chartPosterJPEG(0x33))
	id := fixture.video.ID
	cases := []struct {
		path string
		want int
	}{
		{fmt.Sprintf("/preview/video-artwork/%d", id), http.StatusBadRequest},
		{fmt.Sprintf("/preview/video-artwork/%d?kind=", id), http.StatusBadRequest},
		{fmt.Sprintf("/preview/video-artwork/%d?kind=banner", id), http.StatusBadRequest},
		{fmt.Sprintf("/preview/video-artwork/%d?kind=POSTER", id), http.StatusBadRequest},
		{"/preview/video-artwork/abc?kind=poster", http.StatusBadRequest},
		{"/preview/video-artwork/?kind=poster", http.StatusBadRequest},
		{"/preview/video-artwork/../1?kind=poster", http.StatusBadRequest},
		// 视频不存在：海报也不回落缩略图，直接 404。
		{"/preview/video-artwork/999999?kind=poster", http.StatusNotFound},
		{"/preview/video-artwork/999999?kind=fanart", http.StatusNotFound},
	}
	for _, tc := range cases {
		rec := fixture.get(t, tc.path, nil)
		if rec.Code != tc.want {
			t.Fatalf("%s 期望 %d，实际 %d body=%s", tc.path, tc.want, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Artwork-Fallback"); got != "" {
			t.Fatalf("%s 不应带回落头: %q", tc.path, got)
		}
	}
	// 与缩略图同一套本地访问约束：只接 GET / HEAD。
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, fmt.Sprintf("/preview/video-artwork/%d?kind=poster", id), nil)
		rec := httptest.NewRecorder()
		fixture.handler.ServeHTTP(rec, req)
		thumbReq := httptest.NewRequest(method, fmt.Sprintf("/preview/thumbnail/%d", id), nil)
		thumbRec := httptest.NewRecorder()
		fixture.handler.ServeHTTP(thumbRec, thumbReq)
		if rec.Code != thumbRec.Code || rec.Code == http.StatusOK {
			t.Fatalf("%s 应与缩略图路由同样拒绝: artwork=%d thumbnail=%d", method, rec.Code, thumbRec.Code)
		}
	}
	head := httptest.NewRequest(http.MethodHead, fmt.Sprintf("/preview/video-artwork/%d?kind=poster", id), nil)
	headRec := httptest.NewRecorder()
	fixture.handler.ServeHTTP(headRec, head)
	if headRec.Code != http.StatusOK {
		t.Fatalf("HEAD 期望 200，实际 %d", headRec.Code)
	}
}

// 缓存按源文件修改时间失效：Last-Modified 取图片文件的 mtime，条件请求在文件没变时 304，换图后 200。
func TestVideoArtworkRouteRevalidatesByArtworkModTime(t *testing.T) {
	fixture := newArtworkFixture(t)
	full := fixture.writeArtwork(t, "fanart_path", "fanart.jpg", chartPosterJPEG(0x44))
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(full, old, old); err != nil {
		t.Fatalf("设置修改时间失败: %v", err)
	}
	path := fmt.Sprintf("/preview/video-artwork/%d?kind=fanart", fixture.video.ID)
	first := fixture.get(t, path, nil)
	lastModified := first.Header().Get("Last-Modified")
	if lastModified != old.Format(http.TimeFormat) {
		t.Fatalf("Last-Modified 应取图片 mtime: %q", lastModified)
	}
	conditional := http.Header{"If-Modified-Since": []string{lastModified}}
	if rec := fixture.get(t, path, conditional); rec.Code != http.StatusNotModified {
		t.Fatalf("文件没变时期望 304，实际 %d", rec.Code)
	}
	if err := os.WriteFile(full, chartPosterJPEG(0x55), 0644); err != nil {
		t.Fatalf("换图失败: %v", err)
	}
	newer := old.Add(time.Hour)
	if err := os.Chtimes(full, newer, newer); err != nil {
		t.Fatalf("设置修改时间失败: %v", err)
	}
	rec := fixture.get(t, path, conditional)
	if rec.Code != http.StatusOK || rec.Body.String() != string(chartPosterJPEG(0x55)) {
		t.Fatalf("换图后应返回新图: code=%d", rec.Code)
	}
}
