package services

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// imageCleanupFullHash 只对采样命中的文件读完整内容，流式读取且可取消。
func imageCleanupFullHash(ctx context.Context, state imageCleanupFileState) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.Open(state.image.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !imageCleanupFileUnchanged(state, before) {
		return "", fmt.Errorf("图片文件已变化")
	}
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	after, err := os.Stat(state.image.Path)
	if err != nil {
		return "", err
	}
	if !imageCleanupFileUnchanged(state, after) || !os.SameFile(before, after) {
		return "", fmt.Errorf("图片文件已变化")
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func imageCleanupFileUnchanged(state imageCleanupFileState, info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Size() == state.size && info.ModTime().UnixNano() == state.modTimeNS
}

type imageCleanupVisual struct {
	rgb      [clipVerifyFrameBytes]byte
	aspect   float64
	animated bool
}

type imageCleanupVisualReader func(context.Context, imageCleanupFileState) (*imageCleanupVisual, error)

// 解码、方向、HEIC/RAW 支持和缓存失效仍由图片缩略图服务负责。
func imageCleanupThumbnailReader(thumbnails imagePerceptualHashThumbnailResolver) imageCleanupVisualReader {
	return func(ctx context.Context, state imageCleanupFileState) (*imageCleanupVisual, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if thumbnails == nil {
			return nil, fmt.Errorf("图片缩略图服务不可用")
		}
		before, err := os.Stat(state.image.Path)
		if err != nil {
			return nil, err
		}
		if !imageCleanupFileUnchanged(state, before) {
			return nil, fmt.Errorf("图片文件已变化")
		}
		animated, err := imageCleanupAnimated(ctx, state.image.Path)
		if err != nil {
			return nil, err
		}
		if animated {
			return &imageCleanupVisual{animated: true}, nil
		}
		media, err := thumbnails.ResolveImageThumbnail(ctx, state.image.ID)
		if err != nil {
			return nil, err
		}
		if media == nil {
			return nil, fmt.Errorf("图片缩略图不可用")
		}
		f, err := os.Open(media.Path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		decoded, _, err := image.Decode(f)
		if err != nil {
			return nil, err
		}
		after, err := os.Stat(state.image.Path)
		if err != nil {
			return nil, err
		}
		if !imageCleanupFileUnchanged(state, after) || !os.SameFile(before, after) {
			return nil, fmt.Errorf("图片文件已变化")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return imageCleanupVisualFromImage(decoded), nil
	}
}

func imageCleanupVisualFromImage(img image.Image) *imageCleanupVisual {
	bounds := img.Bounds()
	if bounds.Empty() {
		return nil
	}
	visual := &imageCleanupVisual{aspect: float64(bounds.Dx()) / float64(bounds.Dy())}
	for y := 0; y < clipVerifySide; y++ {
		for x := 0; x < clipVerifySide; x++ {
			x0, y0 := bounds.Min.X+x*bounds.Dx()/clipVerifySide, bounds.Min.Y+y*bounds.Dy()/clipVerifySide
			x1 := max(x0+1, bounds.Min.X+(x+1)*bounds.Dx()/clipVerifySide)
			y1 := max(y0+1, bounds.Min.Y+(y+1)*bounds.Dy()/clipVerifySide)
			var red, green, blue, count uint64
			for py := y0; py < y1; py++ {
				for px := x0; px < x1; px++ {
					r, g, b, _ := img.At(px, py).RGBA()
					red += uint64(r)
					green += uint64(g)
					blue += uint64(b)
					count++
				}
			}
			offset := (y*clipVerifySide + x) * 3
			visual.rgb[offset] = byte(red / count >> 8)
			visual.rgb[offset+1] = byte(green / count >> 8)
			visual.rgb[offset+2] = byte(blue / count >> 8)
		}
	}
	return visual
}

func imageCleanupVisualsMatch(a, b *imageCleanupVisual) bool {
	if a == nil || b == nil || a.animated || b.animated || a.aspect <= 0 || b.aspect <= 0 {
		return false
	}
	if math.Abs(a.aspect-b.aspect)/math.Max(a.aspect, b.aspect) > 0.02 {
		return false
	}
	// 结构检验排除纯色/低信息画面；颜色检验避免灰度轮廓相同就成组。
	var difference int
	for i := range a.rgb {
		difference += int(math.Abs(float64(a.rgb[i]) - float64(b.rgb[i])))
	}
	return difference <= len(a.rgb)*12 && clipRGBFramesMatch(a.rgb[:], b.rgb[:])
}

// 只读容器控制块，不解码第二条图片管线。APNG 的 acTL 必须早于 IDAT；
// WebP 扩展头 VP8X 的 0x02 表示动画。依据 PNG 3 / WebP RIFF 规范。
func imageCleanupAnimated(ctx context.Context, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var header [12]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return false, err
	}
	if string(header[:6]) == "GIF87a" || string(header[:6]) == "GIF89a" {
		return true, nil
	}
	if string(header[:8]) == "\x89PNG\r\n\x1a\n" {
		if _, err := f.Seek(8, io.SeekStart); err != nil {
			return false, err
		}
		for {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			var chunk [8]byte
			if _, err := io.ReadFull(f, chunk[:]); err != nil {
				return false, err
			}
			switch string(chunk[4:]) {
			case "acTL":
				return true, nil
			case "IDAT", "IEND":
				return false, nil
			}
			// 跳过内容与 CRC，元数据再大也不把它加载到内存。
			if _, err := f.Seek(int64(binary.BigEndian.Uint32(chunk[:4]))+4, io.SeekCurrent); err != nil {
				return false, err
			}
		}
	}
	if string(header[:4]) == "RIFF" && string(header[8:]) == "WEBP" {
		var chunk [9]byte
		if _, err := io.ReadFull(f, chunk[:]); err != nil {
			return false, err
		}
		return (string(chunk[:4]) == "VP8X" && chunk[8]&0x02 != 0) || string(chunk[:4]) == "ANIM" || string(chunk[:4]) == "ANMF", nil
	}
	return false, nil
}

// 每轮最多缓存 256 张 32×32 RGB 证据；错误只记 ID，避免坏文件被重复解码。
type imageCleanupVisualCache struct {
	read   imageCleanupVisualReader
	items  map[uint]*list.Element
	order  *list.List
	failed map[uint]struct{}
}

type imageCleanupCachedVisual struct {
	id     uint
	visual *imageCleanupVisual
}

func newImageCleanupVisualCache(read imageCleanupVisualReader) *imageCleanupVisualCache {
	return &imageCleanupVisualCache{read: read, items: make(map[uint]*list.Element), order: list.New(), failed: make(map[uint]struct{})}
}

func (c *imageCleanupVisualCache) get(ctx context.Context, state imageCleanupFileState) *imageCleanupVisual {
	id := state.image.ID
	if _, failed := c.failed[id]; failed {
		return nil
	}
	if cached := c.items[id]; cached != nil {
		c.order.MoveToFront(cached)
		return cached.Value.(imageCleanupCachedVisual).visual
	}
	visual, err := c.read(ctx, state)
	if err != nil || visual == nil {
		if ctx.Err() == nil {
			c.failed[id] = struct{}{}
		}
		return nil
	}
	c.items[id] = c.order.PushFront(imageCleanupCachedVisual{id, visual})
	if c.order.Len() > 256 {
		oldest := c.order.Back()
		delete(c.items, oldest.Value.(imageCleanupCachedVisual).id)
		c.order.Remove(oldest)
	}
	return visual
}

func (c *imageCleanupVisualCache) matches(ctx context.Context, a, b imageCleanupFileState) bool {
	// 缩略图只含首帧，不能据此判整个 GIF 的内容重复。
	for _, state := range []imageCleanupFileState{a, b} {
		if strings.EqualFold(state.image.Format, "gif") || strings.EqualFold(filepath.Ext(state.image.Path), ".gif") {
			return false
		}
	}
	left := c.get(ctx, a)
	if left == nil {
		return false
	}
	return imageCleanupVisualsMatch(left, c.get(ctx, b))
}
