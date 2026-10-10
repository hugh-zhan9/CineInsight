package main

import (
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

func TestFrameAtHandlerServesExactFrameAndRejectsBadInput(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("fake-video"), 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}
	video := models.Video{Name: "clip.mp4", Path: videoPath, Directory: root, Duration: 30}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("创建 bin 目录失败: %v", err)
	}
	argsLog := filepath.Join(root, "args.log")
	script := fmt.Sprintf("#!/bin/bash\necho \"$@\" > %q\nprintf 'jpeg-frame'\n", argsLog)
	if err := os.WriteFile(filepath.Join(binDir, "ffmpeg"), []byte(script), 0755); err != nil {
		t.Fatalf("写入 ffmpeg stub 失败: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	app := NewApp()
	app.thumbnailService = services.NewThumbnailService(app.videoService, root)
	handler := newAssetHandler(app)
	serve := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}

	rec := serve(fmt.Sprintf("/preview/frame/%d?ms=12500&w=320", video.ID))
	if rec.Code != http.StatusOK || rec.Body.String() != "jpeg-frame" || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("应返回单帧 JPEG code=%d body=%q type=%q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("读取 ffmpeg 参数失败: %v", err)
	}
	for _, want := range []string{"-ss 12.500", "scale=320:-2"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("ffmpeg 参数缺少 %q: %s", want, args)
		}
	}

	cases := map[string]int{
		"/preview/frame/abc?ms=1":                           http.StatusBadRequest,
		fmt.Sprintf("/preview/frame/%d", video.ID):          http.StatusBadRequest,
		fmt.Sprintf("/preview/frame/%d?ms=-5", video.ID):    http.StatusBadRequest,
		fmt.Sprintf("/preview/frame/%d?ms=1&w=x", video.ID): http.StatusBadRequest,
		fmt.Sprintf("/preview/frame/%d?ms=31000", video.ID): http.StatusNotFound,
		"/preview/frame/999999?ms=1":                        http.StatusNotFound,
	}
	for target, want := range cases {
		if got := serve(target).Code; got != want {
			t.Fatalf("%s 期望 %d，实际 %d", target, want, got)
		}
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/preview/frame/%d?ms=1", video.ID), nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("POST 不应返回帧")
	}
}
