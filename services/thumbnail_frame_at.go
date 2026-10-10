package services

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// 指定时间点的单帧预览（场景检索命中与视频工作台切点共用）。帧只在内存里
// 按「视频 + 源 size/mtime + 毫秒 + 宽度」缓存，源文件一变就自然失配；
// 不落盘、不写库，路由也不暴露源路径。
const (
	frameAtRoutePrefix   = "/preview/frame/"
	frameAtDefaultWidth  = 480
	frameAtMinWidth      = 64
	frameAtMaxWidth      = 960
	frameAtCacheEntries  = 64
	frameAtTimeout       = 10 * time.Second
	frameAtMaxImageBytes = 4 << 20
	frameAtConcurrency   = 2
)

// ErrFrameAtOutOfRange 表示请求的时间点超出了已知时长。
var ErrFrameAtOutOfRange = errors.New("frame_out_of_range")

type frameAtRunner func(ctx context.Context, binary, sourcePath string, seekSeconds float64, width int) ([]byte, error)

type frameAtImage struct {
	key     string
	data    []byte
	modTime time.Time
}

// frameAtCache 是固定容量的 LRU；只由 ThumbnailService 持有。
type frameAtCache struct {
	mu      sync.Mutex
	limit   int
	order   *list.List
	entries map[string]*list.Element
}

func newFrameAtCache(limit int) *frameAtCache {
	return &frameAtCache{limit: limit, order: list.New(), entries: map[string]*list.Element{}}
}

func (c *frameAtCache) get(key string) (frameAtImage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return frameAtImage{}, false
	}
	c.order.MoveToFront(element)
	return element.Value.(frameAtImage), true
}

func (c *frameAtCache) put(image frameAtImage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[image.key]; ok {
		element.Value = image
		c.order.MoveToFront(element)
		return
	}
	c.entries[image.key] = c.order.PushFront(image)
	for c.order.Len() > c.limit {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(frameAtImage).key)
	}
}

func (c *frameAtCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// FrameAtPath 返回某视频某毫秒的单帧预览路由。
func FrameAtPath(videoID uint, ms int64, width int) string {
	return fmt.Sprintf("%s%d?ms=%d&w=%d", frameAtRoutePrefix, videoID, ms, width)
}

// NormalizeFrameAtWidth 把请求宽度夹到允许范围；0 表示默认宽度。
func NormalizeFrameAtWidth(width int) int {
	if width <= 0 {
		return frameAtDefaultWidth
	}
	if width < frameAtMinWidth {
		return frameAtMinWidth
	}
	if width > frameAtMaxWidth {
		return frameAtMaxWidth
	}
	return width
}

// RenderFrameAt 精确定位到 ms 抽一帧 JPEG。ms 超出库内已知时长返回
// ErrFrameAtOutOfRange；源文件不存在返回 os.ErrNotExist 系错误。
func (s *ThumbnailService) RenderFrameAt(ctx context.Context, videoID uint, ms int64, width int) ([]byte, time.Time, error) {
	if videoID == 0 {
		return nil, time.Time{}, fmt.Errorf("视频 ID 不能为空")
	}
	if ms < 0 {
		return nil, time.Time{}, ErrFrameAtOutOfRange
	}
	width = NormalizeFrameAtWidth(width)
	video, err := s.videoService.GetVideo(videoID)
	if err != nil {
		return nil, time.Time{}, err
	}
	if video.Duration > 0 && float64(ms) > video.Duration*1000 {
		return nil, time.Time{}, ErrFrameAtOutOfRange
	}
	info, err := os.Stat(video.Path)
	if err != nil {
		return nil, time.Time{}, err
	}
	if info.IsDir() {
		return nil, time.Time{}, fmt.Errorf("预览源路径不是文件")
	}
	key := fmt.Sprintf("%d:%d:%d:%d:%d", videoID, info.Size(), info.ModTime().UnixNano(), ms, width)
	if cached, ok := s.frameCache.get(key); ok {
		return cached.data, cached.modTime, nil
	}
	binary, err := s.findFFmpeg()
	if err != nil {
		return nil, time.Time{}, err
	}
	select {
	case s.frameSem <- struct{}{}:
	case <-ctx.Done():
		return nil, time.Time{}, ctx.Err()
	}
	defer func() { <-s.frameSem }()
	runCtx, cancel := context.WithTimeout(ctx, frameAtTimeout)
	defer cancel()
	data, err := s.runFrameAt(runCtx, binary, video.Path, float64(ms)/1000, width)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("抽取预览帧失败: %w", err)
	}
	if len(data) == 0 {
		return nil, time.Time{}, ErrFrameAtOutOfRange
	}
	image := frameAtImage{key: key, data: data, modTime: info.ModTime()}
	s.frameCache.put(image)
	return image.data, image.modTime, nil
}

func runFrameAtFFmpeg(ctx context.Context, binary, sourcePath string, seekSeconds float64, width int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary,
		"-v", "error", "-nostdin",
		"-ss", strconv.FormatFloat(seekSeconds, 'f', 3, 64),
		"-i", sourcePath,
		"-map", "0:V:0", "-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:-2:flags=bicubic", width),
		"-q:v", "4", "-f", "image2", "-c:v", "mjpeg", "pipe:1",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, frameAtMaxImageBytes+1))
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", waitErr, truncateLogSnippet(redactFrameHashErrorPaths(stderr.String()), 400))
	}
	if len(data) > frameAtMaxImageBytes {
		return nil, fmt.Errorf("预览帧超过 %d 字节", frameAtMaxImageBytes)
	}
	return data, nil
}
