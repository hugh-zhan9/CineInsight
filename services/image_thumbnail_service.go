package services

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // 缩略图 JPEG 解码（dHash 回填）
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

const (
	imageThumbnailRoutePrefix       = "/preview/image-thumbnail/"
	imageViewRoutePrefix            = "/preview/image/"
	imageThumbnailMaxEdge           = 480
	imageViewMaxEdge                = 2560
	imageThumbnailCacheLimit  int64 = 256 << 20
	imageDecodeTimeout              = 30 * time.Second
)

// ErrImageDecodeUnsupported 表示当前平台/格式没有可用的图片解码器（D-006：
// HEIC/RAW 仅 darwin 支持，其他平台占位降级为 404）。
var ErrImageDecodeUnsupported = errors.New("IMAGE_DECODE_UNSUPPORTED")

// ImageMedia 描述可由 HTTP 资源处理器返回的图片资源。
type ImageMedia struct {
	Path    string
	ModTime time.Time
	MIME    string
}

// imageDecoderKind 表示解码矩阵的分流结果（设计 4.2.2 D-006）。
type imageDecoderKind int

const (
	imageDecoderUnsupported imageDecoderKind = iota
	imageDecoderFFmpeg
	imageDecoderSips
)

// imageDecoderForFormat 按小写扩展名（无点）分流解码器；未收录格式一律降级为
// 不支持（占位 404），不做未约定的兜底解码。
func imageDecoderForFormat(format string) imageDecoderKind {
	return imageDecoderForFormatAndPath(format, "")
}

// imageDecoderForFormatAndPath 在 format 为空（早期入库未回填 Format 的历史记录）
// 时回退用路径扩展名分流，保证存量记录仍可出图。
func imageDecoderForFormatAndPath(format, path string) imageDecoderKind {
	if format == "" && path != "" {
		format = strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	}
	switch strings.ToLower(format) {
	case "jpg", "jpeg", "png", "gif", "webp":
		return imageDecoderFFmpeg
	case "heic", "heif", "dng", "cr2", "cr3", "nef", "arw", "orf", "raf", "rw2":
		return imageDecoderSips
	default:
		return imageDecoderUnsupported
	}
}

// mimeByImageFormat 返回常规格式原文件直出时的 Content-Type。
func mimeByImageFormat(format string) string {
	return mimeByImageFormatAndPath(format, "")
}

// mimeByImageFormatAndPath 与解码矩阵一致，format 为空时回退路径扩展名。
func mimeByImageFormatAndPath(format, path string) string {
	if format == "" && path != "" {
		format = strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	}
	switch strings.ToLower(format) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

// ImageThumbnailService 管理可重建的本地图片缩略图/查看大图缓存
// （独立目录 <dataDir>/image-thumbnails/，与视频 thumbnails/ 零交叉）。
type ImageThumbnailService struct {
	cacheDir      string
	maxCacheBytes int64
	findFFmpeg    func() (string, error)
	runFFmpeg     func(ctx context.Context, binary, sourcePath, destinationPath string, maxEdge int) error
	probeFFprobe  func(ctx context.Context, sourcePath string) (width, height int, err error)
	convertJPEG   func(ctx context.Context, sourcePath, destinationPath string, maxEdge int) error
	probeSips     func(ctx context.Context, sourcePath string) (width, height int, err error)
	locks         sync.Map
	cacheMu       sync.Mutex
}

// NewImageThumbnailService 创建图片缩略图服务，缓存根为 <dataDir>/image-thumbnails/。
func NewImageThumbnailService(dataDir string) *ImageThumbnailService {
	return &ImageThumbnailService{
		cacheDir:      filepath.Join(dataDir, "image-thumbnails"),
		maxCacheBytes: imageThumbnailCacheLimit,
		findFFmpeg:    findThumbnailFFmpeg,
		runFFmpeg:     runImageThumbnailFFmpeg,
		probeFFprobe:  probeImageDimensionsFFprobe,
		convertJPEG:   sipsConvertJPEG,
		probeSips:     sipsProbeDimensions,
	}
}

// ImageThumbnailPath 返回主前端可使用的图片缩略图资源路径。
func ImageThumbnailPath(imageID uint) string {
	return fmt.Sprintf("%s%d", imageThumbnailRoutePrefix, imageID)
}

// ImageViewPath 返回主前端可使用的图片查看资源路径。
func ImageViewPath(imageID uint) string {
	return fmt.Sprintf("%s%d", imageViewRoutePrefix, imageID)
}

// SetDecodeRunnersForTest 覆盖 HEIC/RAW 解码 runner，用于在 darwin 上复现非
// darwin 的降级路径或注入夹具；仅测试使用。nil 参数保持原 runner 不变。
func (s *ImageThumbnailService) SetDecodeRunnersForTest(
	convert func(ctx context.Context, sourcePath, destinationPath string, maxEdge int) error,
	probe func(ctx context.Context, sourcePath string) (width, height int, err error),
) {
	if convert != nil {
		s.convertJPEG = convert
	}
	if probe != nil {
		s.probeSips = probe
	}
}

// loadImageForAsset 读取活跃图片记录；记录不存在时映射为 os.ErrNotExist（路由 404）。
func loadImageForAsset(imageID uint) (*models.Image, error) {
	if imageID == 0 {
		return nil, fmt.Errorf("图片 ID 不能为空")
	}
	var img models.Image
	if err := database.DB.First(&img, imageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("图片 %d 不存在: %w", imageID, os.ErrNotExist)
		}
		return nil, err
	}
	return &img, nil
}

// ResolveImageThumbnail 返回有效缩略图缓存，必要时按解码矩阵生成，并机会式回填
// width/height 与 dHash 指纹（设计 4.2.2）。
func (s *ImageThumbnailService) ResolveImageThumbnail(ctx context.Context, imageID uint) (*ImageMedia, error) {
	img, err := loadImageForAsset(imageID)
	if err != nil {
		return nil, err
	}
	decoder := imageDecoderForFormatAndPath(img.Format, img.Path)
	if decoder == imageDecoderUnsupported {
		return nil, fmt.Errorf("图片格式 %q 无可用解码器: %w", img.Format, ErrImageDecodeUnsupported)
	}
	sourceInfo, err := os.Stat(img.Path)
	if err != nil {
		return nil, err
	}
	if sourceInfo.IsDir() {
		return nil, fmt.Errorf("图片源路径不是文件")
	}
	if err := os.MkdirAll(s.cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("创建图片缩略图缓存目录失败: %w", err)
	}

	lockValue, _ := s.locks.LoadOrStore(imageID, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	cachePath := filepath.Join(s.cacheDir, strconv.FormatUint(uint64(imageID), 10)+".jpg")
	if media, ok := validThumbnailCache(cachePath, sourceInfo.ModTime()); ok {
		s.backfillImageMetadata(ctx, img, decoder, sourceInfo, cachePath)
		return &ImageMedia{Path: media.Path, ModTime: media.ModTime, MIME: "image/jpeg"}, nil
	}

	if err := s.generateJPEGCache(ctx, img, decoder, cachePath, imageThumbnailMaxEdge, sourceInfo); err != nil {
		return nil, err
	}
	cacheInfo, err := os.Stat(cachePath)
	if err != nil {
		return nil, err
	}
	s.backfillImageMetadata(ctx, img, decoder, sourceInfo, cachePath)
	return &ImageMedia{Path: cachePath, ModTime: cacheInfo.ModTime(), MIME: "image/jpeg"}, nil
}

// ResolveImageView 返回查看大图：常规格式原文件直出；HEIC/RAW 在 darwin 转码为
// <id>.view.jpg（长边 2560），其余平台经 stub 返回 ErrImageDecodeUnsupported。
func (s *ImageThumbnailService) ResolveImageView(ctx context.Context, imageID uint) (*ImageMedia, error) {
	img, err := loadImageForAsset(imageID)
	if err != nil {
		return nil, err
	}
	decoder := imageDecoderForFormatAndPath(img.Format, img.Path)
	if decoder == imageDecoderUnsupported {
		return nil, fmt.Errorf("图片格式 %q 无可用解码器: %w", img.Format, ErrImageDecodeUnsupported)
	}
	sourceInfo, err := os.Stat(img.Path)
	if err != nil {
		return nil, err
	}
	if sourceInfo.IsDir() {
		return nil, fmt.Errorf("图片源路径不是文件")
	}
	if decoder == imageDecoderFFmpeg {
		return &ImageMedia{Path: img.Path, ModTime: sourceInfo.ModTime(), MIME: mimeByImageFormatAndPath(img.Format, img.Path)}, nil
	}
	return s.resolveViewCache(ctx, img, decoder, sourceInfo)
}

// ResolveImageFeedView 手机端（局域网 Feed）用的大图。与桌面端的区别只有一条：
// 浏览器能直接解码的格式**不再直出原文件**——一张 30MB 的 JPEG 走 WiFi 要好几秒，
// 而手机屏幕用不上那个分辨率。超过 inlineOriginalMaxBytes 的一律给降采样后的
// JPEG 缓存（长边 imageViewMaxEdge）；生成失败时退回原文件，保证仍然能看。
func (s *ImageThumbnailService) ResolveImageFeedView(ctx context.Context, imageID uint, inlineOriginalMaxBytes int64) (*ImageMedia, error) {
	img, err := loadImageForAsset(imageID)
	if err != nil {
		return nil, err
	}
	decoder := imageDecoderForFormatAndPath(img.Format, img.Path)
	if decoder == imageDecoderUnsupported {
		return nil, fmt.Errorf("图片格式 %q 无可用解码器: %w", img.Format, ErrImageDecodeUnsupported)
	}
	sourceInfo, err := os.Stat(img.Path)
	if err != nil {
		return nil, err
	}
	if sourceInfo.IsDir() {
		return nil, fmt.Errorf("图片源路径不是文件")
	}

	original := &ImageMedia{Path: img.Path, ModTime: sourceInfo.ModTime(), MIME: mimeByImageFormatAndPath(img.Format, img.Path)}
	// 小图直出：为了省几十 KB 去转码反而更慢。
	if decoder == imageDecoderFFmpeg && inlineOriginalMaxBytes > 0 && sourceInfo.Size() <= inlineOriginalMaxBytes {
		return original, nil
	}

	media, err := s.resolveViewCache(ctx, img, decoder, sourceInfo)
	if err != nil {
		if decoder == imageDecoderFFmpeg {
			// 没有 ffmpeg 或转码失败：宁可慢也要能看，退回原文件。
			log.Printf("[ShortFeed] 图片降采样失败，退回原文件 id=%d err=%v", imageID, err)
			return original, nil
		}
		return nil, err
	}
	return media, nil
}

// resolveViewCache 取（必要时生成）长边 imageViewMaxEdge 的 JPEG 缓存。
func (s *ImageThumbnailService) resolveViewCache(ctx context.Context, img *models.Image, decoder imageDecoderKind, sourceInfo os.FileInfo) (*ImageMedia, error) {
	if err := os.MkdirAll(s.cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("创建图片缩略图缓存目录失败: %w", err)
	}

	lockValue, _ := s.locks.LoadOrStore(img.ID, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	cachePath := filepath.Join(s.cacheDir, strconv.FormatUint(uint64(img.ID), 10)+".view.jpg")
	if media, ok := validThumbnailCache(cachePath, sourceInfo.ModTime()); ok {
		return &ImageMedia{Path: media.Path, ModTime: media.ModTime, MIME: "image/jpeg"}, nil
	}

	if err := s.generateJPEGCache(ctx, img, decoder, cachePath, imageViewMaxEdge, sourceInfo); err != nil {
		return nil, err
	}
	cacheInfo, err := os.Stat(cachePath)
	if err != nil {
		return nil, err
	}
	return &ImageMedia{Path: cachePath, ModTime: cacheInfo.ModTime(), MIME: "image/jpeg"}, nil
}

// generateJPEGCache 按解码矩阵生成 JPEG 到 cachePath：临时文件 + os.Rename 原子
// 发布，成功后执行统一 LRU 淘汰。调用方须已持有该图片的 per-ID 锁。
func (s *ImageThumbnailService) generateJPEGCache(ctx context.Context, img *models.Image, decoder imageDecoderKind, cachePath string, maxEdge int, sourceInfo os.FileInfo) error {
	tempFile, err := os.CreateTemp(s.cacheDir, fmt.Sprintf(".%d-*.jpg", img.ID))
	if err != nil {
		return fmt.Errorf("创建图片缩略图临时文件失败: %w", err)
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("关闭图片缩略图临时文件失败: %w", err)
	}
	if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("准备图片缩略图临时路径失败: %w", err)
	}
	defer os.Remove(tempPath)

	switch decoder {
	case imageDecoderFFmpeg:
		ffmpegPath, err := s.findFFmpeg()
		if err != nil {
			return err
		}
		if err := s.runFFmpeg(ctx, ffmpegPath, img.Path, tempPath, maxEdge); err != nil {
			return fmt.Errorf("生成图片缩略图失败: %w", err)
		}
	case imageDecoderSips:
		if err := s.convertJPEG(ctx, img.Path, tempPath, maxEdge); err != nil {
			if errors.Is(err, ErrImageDecodeUnsupported) {
				return err
			}
			return fmt.Errorf("生成图片缩略图失败: %w", err)
		}
	default:
		return ErrImageDecodeUnsupported
	}

	generatedInfo, err := os.Stat(tempPath)
	if err != nil {
		return fmt.Errorf("读取生成的图片缩略图失败: %w", err)
	}
	if generatedInfo.IsDir() || generatedInfo.Size() == 0 {
		return fmt.Errorf("生成的图片缩略图为空")
	}
	if runtime.GOOS == "windows" {
		if err := os.Remove(cachePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("替换旧图片缩略图失败: %w", err)
		}
	}
	if err := os.Rename(tempPath, cachePath); err != nil {
		return fmt.Errorf("提交图片缩略图缓存失败: %w", err)
	}
	if sourceInfo.ModTime().After(time.Now()) {
		_ = os.Chtimes(cachePath, sourceInfo.ModTime(), sourceInfo.ModTime())
	}
	s.cacheMu.Lock()
	s.pruneImageCacheLocked(cachePath)
	s.cacheMu.Unlock()
	return nil
}

// pruneImageCacheLocked 对缓存目录内全部 <id>.jpg 与 <id>.view.jpg 执行统一 LRU
// （按 mtime 淘汰，镜像 pruneSeekSpriteCacheLocked）。点开头的临时文件不参与，
// 避免误删并发生成中的产物。调用方须持有 cacheMu。
func (s *ImageThumbnailService) pruneImageCacheLocked(keepPath string) {
	entries, err := filepath.Glob(filepath.Join(s.cacheDir, "*.jpg"))
	if err != nil {
		return
	}
	type cacheEntry struct {
		path    string
		size    int64
		modTime time.Time
	}
	files := make([]cacheEntry, 0, len(entries))
	var total int64
	for _, path := range entries {
		if strings.HasPrefix(filepath.Base(path), ".") {
			continue
		}
		info, statErr := os.Stat(path)
		if statErr != nil || info.IsDir() {
			continue
		}
		files = append(files, cacheEntry{path: path, size: info.Size(), modTime: info.ModTime()})
		total += info.Size()
	}
	if total <= s.maxCacheBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	for _, file := range files {
		if total <= s.maxCacheBytes {
			return
		}
		if file.path == keepPath {
			continue
		}
		if err := os.Remove(file.path); err == nil || os.IsNotExist(err) {
			total -= file.size
		}
	}
}

// backfillImageMetadata 机会式回填：width/height 为 0 时探测尺寸；perceptual_hash
// 缺失或源指纹（size + mtime 纳秒）不符时用缩略图 JPEG 重算 dHash。失败仅记日志，
// 不影响缩略图返回（设计 4.2.2 dHash 回填）。
func (s *ImageThumbnailService) backfillImageMetadata(ctx context.Context, img *models.Image, decoder imageDecoderKind, sourceInfo os.FileInfo, thumbnailPath string) {
	updates := map[string]interface{}{}

	if img.Width == 0 || img.Height == 0 {
		width, height, err := s.probeImageDimensions(ctx, img.Path, decoder)
		if err != nil {
			log.Printf("[ImageThumbnail] 尺寸探测回填失败 image=%d path=%s err=%v", img.ID, img.Path, err)
		} else {
			updates["width"] = width
			updates["height"] = height
		}
	}

	if img.PerceptualHash == "" ||
		img.HashSourceSize != sourceInfo.Size() ||
		img.HashSourceModTimeNS != sourceInfo.ModTime().UnixNano() {
		hash, err := imageThumbnailDHash(thumbnailPath)
		if err != nil {
			log.Printf("[ImageThumbnail] dHash 回填失败 image=%d path=%s err=%v", img.ID, img.Path, err)
		} else {
			updates["perceptual_hash"] = fmt.Sprintf("%016x", hash)
			updates["hash_source_size"] = sourceInfo.Size()
			updates["hash_source_mod_time_ns"] = sourceInfo.ModTime().UnixNano()
		}
	}

	if len(updates) == 0 {
		return
	}
	if err := database.DB.Model(&models.Image{}).Where("id = ?", img.ID).Updates(updates).Error; err != nil {
		log.Printf("[ImageThumbnail] 元数据回填写库失败 image=%d err=%v", img.ID, err)
	}
}

// probeImageDimensions 按解码矩阵探测像素尺寸：常规格式 ffprobe，HEIC/RAW sips。
func (s *ImageThumbnailService) probeImageDimensions(ctx context.Context, sourcePath string, decoder imageDecoderKind) (int, int, error) {
	switch decoder {
	case imageDecoderFFmpeg:
		return s.probeFFprobe(ctx, sourcePath)
	case imageDecoderSips:
		return s.probeSips(ctx, sourcePath)
	default:
		return 0, 0, ErrImageDecodeUnsupported
	}
}

// imageThumbnailDHash 解码缩略图 JPEG 并计算全图 dHash（复用 differenceHash）。
func imageThumbnailDHash(thumbnailPath string) (uint64, error) {
	file, err := os.Open(thumbnailPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	decoded, _, err := image.Decode(file)
	if err != nil {
		return 0, fmt.Errorf("解码缩略图失败: %w", err)
	}
	return differenceHash(decoded, decoded.Bounds()), nil
}

// runImageThumbnailFFmpeg 用 ffmpeg 生成静态图片缩略图（无 -ss，设计 4.2.2）。
func runImageThumbnailFFmpeg(ctx context.Context, binary, sourcePath, destinationPath string, maxEdge int) error {
	ctx, cancel := context.WithTimeout(ctx, imageDecodeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary,
		"-v", "error",
		"-i", sourcePath,
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:-2:force_original_aspect_ratio=decrease", maxEdge),
		"-q:v", "4",
		"-y", destinationPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, truncateLogSnippet(string(output), 400))
	}
	return nil
}

// probeImageDimensionsFFprobe 用 ffprobe 读取常规图片的像素宽高。
func probeImageDimensionsFFprobe(ctx context.Context, sourcePath string) (int, int, error) {
	binary, err := findFFProbeBinary()
	if err != nil {
		return 0, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, imageDecodeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "csv=p=0",
		sourcePath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("ffprobe: %w: %s", err, truncateLogSnippet(string(output), 400))
	}
	fields := strings.Split(strings.TrimSpace(string(output)), ",")
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("ffprobe 未返回有效尺寸: %s", truncateLogSnippet(string(output), 200))
	}
	width, widthErr := strconv.Atoi(strings.TrimSpace(fields[0]))
	height, heightErr := strconv.Atoi(strings.TrimSpace(fields[1]))
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("ffprobe 尺寸解析失败: %s", truncateLogSnippet(string(output), 200))
	}
	return width, height, nil
}
