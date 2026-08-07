package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
