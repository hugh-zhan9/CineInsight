package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupImageThumbnailTestDB(t *testing.T) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "image_thumbnail_test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	database.DB = db
}

func imageThumbnailTestCreateImage(t *testing.T, path, format string) *models.Image {
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

// imageThumbnailTestJPEGBytes 生成一张真实可解码的渐变 JPEG（dHash 非退化）。
func imageThumbnailTestJPEGBytes(t *testing.T, seed uint8) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x*8) + seed, G: uint8(y * 10), B: seed, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, nil); err != nil {
		t.Fatalf("编码测试 JPEG 失败: %v", err)
	}
	return buf.Bytes()
}

func imageThumbnailTestService(t *testing.T) *ImageThumbnailService {
	t.Helper()
	return NewImageThumbnailService(t.TempDir())
}

func TestImageDecoderRoutingMatrix(t *testing.T) {
	cases := []struct {
		format string
		want   imageDecoderKind
	}{
		{"jpg", imageDecoderFFmpeg},
		{"jpeg", imageDecoderFFmpeg},
		{"png", imageDecoderFFmpeg},
		{"gif", imageDecoderFFmpeg},
		{"webp", imageDecoderFFmpeg},
		{"JPG", imageDecoderFFmpeg},
		{"PnG", imageDecoderFFmpeg},
		{"heic", imageDecoderSips},
		{"heif", imageDecoderSips},
		{"dng", imageDecoderSips},
		{"cr2", imageDecoderSips},
		{"cr3", imageDecoderSips},
		{"nef", imageDecoderSips},
		{"arw", imageDecoderSips},
		{"orf", imageDecoderSips},
		{"raf", imageDecoderSips},
		{"rw2", imageDecoderSips},
		{"HEIC", imageDecoderSips},
		{"NEF", imageDecoderSips},
		{"bmp", imageDecoderUnsupported},
		{"", imageDecoderUnsupported},
	}
	for _, c := range cases {
		if got := imageDecoderForFormat(c.format); got != c.want {
			t.Fatalf("格式 %q 解码器路由错误: got=%d want=%d", c.format, got, c.want)
		}
	}
	mimeCases := map[string]string{
		"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png",
		"gif": "image/gif", "webp": "image/webp", "GIF": "image/gif", "heic": "",
	}
	for format, want := range mimeCases {
		if got := mimeByImageFormat(format); got != want {
			t.Fatalf("格式 %q MIME 映射错误: got=%q want=%q", format, got, want)
		}
	}
}

func TestImageDecoderRoutingFallbackToPathExt(t *testing.T) {
	// 早期入库未回填 Format 的历史记录：format 为空时回退路径扩展名分流。
	if got := imageDecoderForFormatAndPath("", "/Volumes/disk/a/b.JPG"); got != imageDecoderFFmpeg {
		t.Fatalf("空 format 应回退路径扩展名走 FFmpeg: got=%d", got)
	}
	if got := imageDecoderForFormatAndPath("", "/data/photo.heic"); got != imageDecoderSips {
		t.Fatalf("空 format 应回退路径扩展名走 sips: got=%d", got)
	}
	if got := imageDecoderForFormatAndPath("", "/data/photo.bmp"); got != imageDecoderUnsupported {
		t.Fatalf("空 format 且扩展名未收录应不支持: got=%d", got)
	}
	if got := imageDecoderForFormatAndPath("png", "/data/photo.heic"); got != imageDecoderFFmpeg {
		t.Fatalf("format 非空时应忽略路径: got=%d", got)
	}
	if got := mimeByImageFormatAndPath("", "/Volumes/disk/a/b.JPG"); got != "image/jpeg" {
		t.Fatalf("空 format MIME 应回退路径扩展名: got=%q", got)
	}
	if got := mimeByImageFormatAndPath("", "/data/photo.heic"); got != "" {
		t.Fatalf("未收录格式 MIME 应为空: got=%q", got)
	}
}

func TestImageThumbnailCacheHitAndMtimeInvalidation(t *testing.T) {
	setupImageThumbnailTestDB(t)
	root := t.TempDir()
	sourcePath := filepath.Join(root, "photo.png")
	if err := os.WriteFile(sourcePath, []byte("fake-png"), 0644); err != nil {
		t.Fatalf("写入图片夹具失败: %v", err)
	}
	img := imageThumbnailTestCreateImage(t, sourcePath, "png")

	svc := imageThumbnailTestService(t)
	svc.findFFmpeg = func() (string, error) { return "ffmpeg-stub", nil }
	ffmpegCalls := 0
	jpegBytes := imageThumbnailTestJPEGBytes(t, 0)
	svc.runFFmpeg = func(ctx context.Context, binary, src, dst string, maxEdge int) error {
		ffmpegCalls++
		if maxEdge != imageThumbnailMaxEdge {
			t.Fatalf("缩略图 maxEdge 错误: got=%d want=%d", maxEdge, imageThumbnailMaxEdge)
		}
		return os.WriteFile(dst, jpegBytes, 0644)
	}
	svc.probeFFprobe = func(ctx context.Context, src string) (int, int, error) { return 32, 24, nil }

	media, err := svc.ResolveImageThumbnail(context.Background(), img.ID)
	if err != nil {
		t.Fatalf("生成缩略图失败: %v", err)
	}
	if media.MIME != "image/jpeg" {
		t.Fatalf("缩略图 MIME 错误: %q", media.MIME)
	}
	wantCache := filepath.Join(svc.cacheDir, fmt.Sprintf("%d.jpg", img.ID))
	if media.Path != wantCache {
		t.Fatalf("缩略图缓存路径错误: got=%s want=%s", media.Path, wantCache)
	}
	if ffmpegCalls != 1 {
		t.Fatalf("首次生成期望调用 ffmpeg 一次，实际 %d", ffmpegCalls)
	}

	if _, err := svc.ResolveImageThumbnail(context.Background(), img.ID); err != nil {
		t.Fatalf("缓存命中失败: %v", err)
	}
	if ffmpegCalls != 1 {
		t.Fatalf("缓存命中不应重新生成，实际调用 %d 次", ffmpegCalls)
	}

	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(sourcePath, future, future); err != nil {
		t.Fatalf("修改源文件 mtime 失败: %v", err)
	}
	if _, err := svc.ResolveImageThumbnail(context.Background(), img.ID); err != nil {
		t.Fatalf("源 mtime 变化后重建失败: %v", err)
	}
	if ffmpegCalls != 2 {
		t.Fatalf("源 mtime 变化应重建缩略图，实际调用 %d 次", ffmpegCalls)
	}
}

func TestImageThumbnailSipsRouteAndUnsupportedSentinel(t *testing.T) {
	setupImageThumbnailTestDB(t)
	root := t.TempDir()
	heicPath := filepath.Join(root, "photo.heic")
	if err := os.WriteFile(heicPath, []byte("fake-heic"), 0644); err != nil {
		t.Fatalf("写入图片夹具失败: %v", err)
	}
	heicImage := imageThumbnailTestCreateImage(t, heicPath, "heic")

	svc := imageThumbnailTestService(t)
	svc.findFFmpeg = func() (string, error) {
		t.Fatal("HEIC 不应走 ffmpeg 路径")
		return "", nil
	}
	convertEdges := []int{}
	jpegBytes := imageThumbnailTestJPEGBytes(t, 3)
	svc.convertJPEG = func(ctx context.Context, src, dst string, maxEdge int) error {
		convertEdges = append(convertEdges, maxEdge)
		return os.WriteFile(dst, jpegBytes, 0644)
	}
	svc.probeSips = func(ctx context.Context, src string) (int, int, error) { return 32, 24, nil }

	if _, err := svc.ResolveImageThumbnail(context.Background(), heicImage.ID); err != nil {
		t.Fatalf("HEIC 缩略图生成失败: %v", err)
	}
	if len(convertEdges) != 1 || convertEdges[0] != imageThumbnailMaxEdge {
		t.Fatalf("HEIC 缩略图应以 maxEdge=%d 调用 sips runner，实际 %v", imageThumbnailMaxEdge, convertEdges)
	}

	// RAW 扩展名同样分流到 sips runner；stub 哨兵错误模拟非 darwin 降级。
	rawPath := filepath.Join(root, "photo.nef")
	if err := os.WriteFile(rawPath, []byte("fake-raw"), 0644); err != nil {
		t.Fatalf("写入 RAW 夹具失败: %v", err)
	}
	rawImage := imageThumbnailTestCreateImage(t, rawPath, "nef")
	svc.convertJPEG = func(ctx context.Context, src, dst string, maxEdge int) error {
		return ErrImageDecodeUnsupported
	}
	if _, err := svc.ResolveImageThumbnail(context.Background(), rawImage.ID); !errors.Is(err, ErrImageDecodeUnsupported) {
		t.Fatalf("非 darwin stub 应返回 ErrImageDecodeUnsupported，实际 %v", err)
	}
	if _, err := svc.ResolveImageView(context.Background(), rawImage.ID); !errors.Is(err, ErrImageDecodeUnsupported) {
		t.Fatalf("查看大图 stub 应返回 ErrImageDecodeUnsupported，实际 %v", err)
	}

	// 未收录格式不进入任何 runner，直接哨兵降级。
	bmpPath := filepath.Join(root, "photo.bmp")
	if err := os.WriteFile(bmpPath, []byte("fake-bmp"), 0644); err != nil {
		t.Fatalf("写入 bmp 夹具失败: %v", err)
	}
	bmpImage := imageThumbnailTestCreateImage(t, bmpPath, "bmp")
	if _, err := svc.ResolveImageThumbnail(context.Background(), bmpImage.ID); !errors.Is(err, ErrImageDecodeUnsupported) {
		t.Fatalf("未收录格式应返回 ErrImageDecodeUnsupported，实际 %v", err)
	}
}

func TestImageViewRegularServesOriginalFile(t *testing.T) {
	setupImageThumbnailTestDB(t)
	root := t.TempDir()
	sourcePath := filepath.Join(root, "photo.webp")
	if err := os.WriteFile(sourcePath, []byte("fake-webp"), 0644); err != nil {
		t.Fatalf("写入图片夹具失败: %v", err)
	}
	img := imageThumbnailTestCreateImage(t, sourcePath, "webp")

	svc := imageThumbnailTestService(t)
	media, err := svc.ResolveImageView(context.Background(), img.ID)
	if err != nil {
		t.Fatalf("查看常规格式失败: %v", err)
	}
	if media.Path != sourcePath {
		t.Fatalf("常规格式应原文件直出: got=%s want=%s", media.Path, sourcePath)
	}
	if media.MIME != "image/webp" {
		t.Fatalf("MIME 错误: %q", media.MIME)
	}
	if _, err := os.Stat(svc.cacheDir); !os.IsNotExist(err) {
		t.Fatalf("常规格式查看不应创建缓存目录: %v", err)
	}
}

func TestImageViewHeicTranscodeAndCacheHit(t *testing.T) {
	setupImageThumbnailTestDB(t)
	root := t.TempDir()
	sourcePath := filepath.Join(root, "photo.heic")
	if err := os.WriteFile(sourcePath, []byte("fake-heic"), 0644); err != nil {
		t.Fatalf("写入图片夹具失败: %v", err)
	}
	img := imageThumbnailTestCreateImage(t, sourcePath, "heic")

	svc := imageThumbnailTestService(t)
	convertCalls := 0
	jpegBytes := imageThumbnailTestJPEGBytes(t, 7)
	svc.convertJPEG = func(ctx context.Context, src, dst string, maxEdge int) error {
		convertCalls++
		if maxEdge != imageViewMaxEdge {
			t.Fatalf("查看大图 maxEdge 错误: got=%d want=%d", maxEdge, imageViewMaxEdge)
		}
		return os.WriteFile(dst, jpegBytes, 0644)
	}

	media, err := svc.ResolveImageView(context.Background(), img.ID)
	if err != nil {
		t.Fatalf("HEIC 查看大图生成失败: %v", err)
	}
	wantCache := filepath.Join(svc.cacheDir, fmt.Sprintf("%d.view.jpg", img.ID))
	if media.Path != wantCache {
		t.Fatalf("查看大图缓存路径错误: got=%s want=%s", media.Path, wantCache)
	}
	if media.MIME != "image/jpeg" {
		t.Fatalf("转码查看大图 MIME 错误: %q", media.MIME)
	}
	if _, err := svc.ResolveImageView(context.Background(), img.ID); err != nil {
		t.Fatalf("查看大图缓存命中失败: %v", err)
	}
	if convertCalls != 1 {
		t.Fatalf("查看大图缓存命中不应重复转码，实际 %d 次", convertCalls)
	}
}

func TestImageThumbnailDHashBackfillAndStaleRecompute(t *testing.T) {
	setupImageThumbnailTestDB(t)
	root := t.TempDir()
	sourcePath := filepath.Join(root, "photo.jpg")
	if err := os.WriteFile(sourcePath, []byte("source-v1"), 0644); err != nil {
		t.Fatalf("写入图片夹具失败: %v", err)
	}
	img := imageThumbnailTestCreateImage(t, sourcePath, "jpg")

	svc := imageThumbnailTestService(t)
	svc.findFFmpeg = func() (string, error) { return "ffmpeg-stub", nil }
	thumbnail := imageThumbnailTestJPEGBytes(t, 0)
	svc.runFFmpeg = func(ctx context.Context, binary, src, dst string, maxEdge int) error {
		return os.WriteFile(dst, thumbnail, 0644)
	}
	probeCalls := 0
	svc.probeFFprobe = func(ctx context.Context, src string) (int, int, error) {
		probeCalls++
		return 640, 480, nil
	}

	if _, err := svc.ResolveImageThumbnail(context.Background(), img.ID); err != nil {
		t.Fatalf("生成缩略图失败: %v", err)
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatalf("读取源文件信息失败: %v", err)
	}
	var stored models.Image
	if err := database.DB.First(&stored, img.ID).Error; err != nil {
		t.Fatalf("读取图片记录失败: %v", err)
	}
	if stored.Width != 640 || stored.Height != 480 {
		t.Fatalf("尺寸回填错误: %dx%d", stored.Width, stored.Height)
	}
	if len(stored.PerceptualHash) != 16 {
		t.Fatalf("dHash 回填缺失: %q", stored.PerceptualHash)
	}
	if stored.HashSourceSize != sourceInfo.Size() || stored.HashSourceModTimeNS != sourceInfo.ModTime().UnixNano() {
		t.Fatalf("哈希源指纹错误: size=%d modns=%d", stored.HashSourceSize, stored.HashSourceModTimeNS)
	}
	firstHash := stored.PerceptualHash
	if probeCalls != 1 {
		t.Fatalf("期望尺寸探测一次，实际 %d", probeCalls)
	}

	// 源文件变化（内容+mtime）→ 缓存失效重建 + 指纹 stale 重算；尺寸已回填不再探测。
	thumbnail = imageThumbnailTestJPEGBytes(t, 200)
	if err := os.WriteFile(sourcePath, []byte("source-v2-longer"), 0644); err != nil {
		t.Fatalf("重写源文件失败: %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(sourcePath, future, future); err != nil {
		t.Fatalf("修改源文件 mtime 失败: %v", err)
	}
	if _, err := svc.ResolveImageThumbnail(context.Background(), img.ID); err != nil {
		t.Fatalf("重建缩略图失败: %v", err)
	}
	newInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatalf("读取源文件信息失败: %v", err)
	}
	if err := database.DB.First(&stored, img.ID).Error; err != nil {
		t.Fatalf("读取图片记录失败: %v", err)
	}
	if stored.HashSourceSize != newInfo.Size() || stored.HashSourceModTimeNS != newInfo.ModTime().UnixNano() {
		t.Fatalf("stale 重算后指纹未更新: size=%d modns=%d", stored.HashSourceSize, stored.HashSourceModTimeNS)
	}
	if stored.PerceptualHash == firstHash {
		t.Fatalf("stale 重算后 dHash 未变化: %q", stored.PerceptualHash)
	}
	if probeCalls != 1 {
		t.Fatalf("尺寸已回填不应重复探测，实际 %d", probeCalls)
	}
}

func TestImageThumbnailBackfillOnCacheHit(t *testing.T) {
	setupImageThumbnailTestDB(t)
	root := t.TempDir()
	sourcePath := filepath.Join(root, "photo.gif")
	if err := os.WriteFile(sourcePath, []byte("fake-gif"), 0644); err != nil {
		t.Fatalf("写入图片夹具失败: %v", err)
	}
	img := imageThumbnailTestCreateImage(t, sourcePath, "gif")

	svc := imageThumbnailTestService(t)
	svc.findFFmpeg = func() (string, error) { return "ffmpeg-stub", nil }
	thumbnail := imageThumbnailTestJPEGBytes(t, 40)
	svc.runFFmpeg = func(ctx context.Context, binary, src, dst string, maxEdge int) error {
		return os.WriteFile(dst, thumbnail, 0644)
	}
	svc.probeFFprobe = func(ctx context.Context, src string) (int, int, error) { return 10, 10, nil }

	if _, err := svc.ResolveImageThumbnail(context.Background(), img.ID); err != nil {
		t.Fatalf("生成缩略图失败: %v", err)
	}
	// 人为清空哈希列：缓存仍有效，下一次命中应机会式补算。
	if err := database.DB.Model(&models.Image{}).Where("id = ?", img.ID).
		Updates(map[string]interface{}{"perceptual_hash": "", "hash_source_size": 0, "hash_source_mod_time_ns": 0}).Error; err != nil {
		t.Fatalf("清空哈希列失败: %v", err)
	}
	if _, err := svc.ResolveImageThumbnail(context.Background(), img.ID); err != nil {
		t.Fatalf("缓存命中失败: %v", err)
	}
	var stored models.Image
	if err := database.DB.First(&stored, img.ID).Error; err != nil {
		t.Fatalf("读取图片记录失败: %v", err)
	}
	if len(stored.PerceptualHash) != 16 || stored.HashSourceSize == 0 {
		t.Fatalf("缓存命中路径未回填 dHash: hash=%q size=%d", stored.PerceptualHash, stored.HashSourceSize)
	}
}

func TestImageThumbnailLRUEviction(t *testing.T) {
	setupImageThumbnailTestDB(t)
	root := t.TempDir()
	jpegBytes := imageThumbnailTestJPEGBytes(t, 12)
	size := int64(len(jpegBytes))

	svc := imageThumbnailTestService(t)
	svc.maxCacheBytes = 2*size + size/2
	svc.findFFmpeg = func() (string, error) { return "ffmpeg-stub", nil }
	svc.runFFmpeg = func(ctx context.Context, binary, src, dst string, maxEdge int) error {
		return os.WriteFile(dst, jpegBytes, 0644)
	}
	svc.probeFFprobe = func(ctx context.Context, src string) (int, int, error) { return 32, 24, nil }

	var cachePaths []string
	for i := 0; i < 3; i++ {
		sourcePath := filepath.Join(root, fmt.Sprintf("photo-%d.png", i))
		if err := os.WriteFile(sourcePath, []byte("fake-png"), 0644); err != nil {
			t.Fatalf("写入图片夹具失败: %v", err)
		}
		img := imageThumbnailTestCreateImage(t, sourcePath, "png")
		if i == 2 {
			// 前两个缓存做旧，保证第三次生成触发淘汰时最老的先被清除。
			old := time.Now().Add(-time.Hour)
			older := time.Now().Add(-2 * time.Hour)
			if err := os.Chtimes(cachePaths[0], older, older); err != nil {
				t.Fatalf("做旧缓存失败: %v", err)
			}
			if err := os.Chtimes(cachePaths[1], old, old); err != nil {
				t.Fatalf("做旧缓存失败: %v", err)
			}
		}
		media, err := svc.ResolveImageThumbnail(context.Background(), img.ID)
		if err != nil {
			t.Fatalf("生成缩略图 %d 失败: %v", i, err)
		}
		cachePaths = append(cachePaths, media.Path)
	}

	if _, err := os.Stat(cachePaths[0]); !os.IsNotExist(err) {
		t.Fatalf("最旧缓存应被 LRU 淘汰: %v", err)
	}
	if _, err := os.Stat(cachePaths[1]); err != nil {
		t.Fatalf("次旧缓存不应被淘汰: %v", err)
	}
	if _, err := os.Stat(cachePaths[2]); err != nil {
		t.Fatalf("新生成缓存必须保留: %v", err)
	}
}

// TestImageThumbnailDarwinSipsHeicEndToEnd 是 darwin-only 集成测试：Go 生成 PNG →
// sips 转 HEIC 夹具 → 全链路生成缩略图、查看大图并回填尺寸与 dHash（TC-2 自动化部分）。
func TestImageThumbnailDarwinSipsHeicEndToEnd(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sips 集成测试仅在 darwin 运行")
	}
	if _, err := exec.LookPath("sips"); err != nil {
		t.Skipf("环境缺少 sips: %v", err)
	}
	setupImageThumbnailTestDB(t)
	root := t.TempDir()

	pngPath := filepath.Join(root, "fixture.png")
	canvas := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 5), B: 90, A: 255})
		}
	}
	pngFile, err := os.Create(pngPath)
	if err != nil {
		t.Fatalf("创建 PNG 夹具失败: %v", err)
	}
	if err := png.Encode(pngFile, canvas); err != nil {
		t.Fatalf("编码 PNG 夹具失败: %v", err)
	}
	if err := pngFile.Close(); err != nil {
		t.Fatalf("关闭 PNG 夹具失败: %v", err)
	}

	heicPath := filepath.Join(root, "fixture.heic")
	if output, err := exec.Command("sips", "-s", "format", "heic", pngPath, "--out", heicPath).CombinedOutput(); err != nil {
		t.Skipf("环境 sips 不支持 heic 输出，跳过: %v %s", err, string(output))
	}
	if info, err := os.Stat(heicPath); err != nil || info.Size() == 0 {
		t.Skipf("sips 未产出有效 heic 夹具: %v", err)
	}

	img := imageThumbnailTestCreateImage(t, heicPath, "heic")
	svc := imageThumbnailTestService(t)

	media, err := svc.ResolveImageThumbnail(context.Background(), img.ID)
	if err != nil {
		t.Fatalf("HEIC 缩略图全链路失败: %v", err)
	}
	thumbFile, err := os.Open(media.Path)
	if err != nil {
		t.Fatalf("打开缩略图失败: %v", err)
	}
	defer thumbFile.Close()
	if _, err := jpeg.Decode(thumbFile); err != nil {
		t.Fatalf("缩略图不是有效 JPEG: %v", err)
	}

	view, err := svc.ResolveImageView(context.Background(), img.ID)
	if err != nil {
		t.Fatalf("HEIC 查看大图全链路失败: %v", err)
	}
	if filepath.Base(view.Path) != fmt.Sprintf("%d.view.jpg", img.ID) {
		t.Fatalf("查看大图缓存命名错误: %s", view.Path)
	}
	viewFile, err := os.Open(view.Path)
	if err != nil {
		t.Fatalf("打开查看大图失败: %v", err)
	}
	defer viewFile.Close()
	if _, err := jpeg.Decode(viewFile); err != nil {
		t.Fatalf("查看大图不是有效 JPEG: %v", err)
	}

	var stored models.Image
	if err := database.DB.First(&stored, img.ID).Error; err != nil {
		t.Fatalf("读取图片记录失败: %v", err)
	}
	if stored.Width != 64 || stored.Height != 48 {
		t.Fatalf("sips 尺寸回填错误: %dx%d", stored.Width, stored.Height)
	}
	if len(stored.PerceptualHash) != 16 {
		t.Fatalf("dHash 未回填: %q", stored.PerceptualHash)
	}
}
