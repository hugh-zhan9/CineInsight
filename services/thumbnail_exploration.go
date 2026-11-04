package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const explorationThumbnailBudget int64 = 128 << 20

// One owner and cold slot for both media types; original thumbnail caches remain
// untouched. This is disposable, bounded image data, not exploration history.
type explorationThumbnailCache struct {
	mu       sync.Mutex
	accessed map[string]time.Time
	dir      string
	budget   int64
	slot     chan struct{}
}

// ResolveExplorationThumbnail adds small size tiers without changing old routes.
func (s *ThumbnailService) ResolveExplorationThumbnail(ctx context.Context, kind string, id uint, edge int, images *ImageThumbnailService) (*ThumbnailMedia, error) {
	if edge != 96 && edge != 256 || id == 0 {
		return nil, ErrExplorationInvalid
	}
	var sourcePath string
	var load func() (string, error)
	switch kind {
	case "video":
		video, err := s.videoService.GetVideo(id)
		if err != nil {
			return nil, err
		}
		sourcePath = video.Path
		load = func() (string, error) {
			media, err := s.ResolveThumbnail(ctx, id)
			if err != nil {
				return "", err
			}
			return media.Path, nil
		}
	case "image":
		if images == nil {
			return nil, ErrExplorationUnavailable
		}
		photo, err := loadImageForAsset(id)
		if err != nil {
			return nil, err
		}
		sourcePath = photo.Path
		load = func() (string, error) {
			media, err := images.ResolveImageThumbnail(ctx, id)
			if err != nil {
				return "", err
			}
			return media.Path, nil
		}
	default:
		return nil, ErrExplorationInvalid
	}
	before, err := os.Stat(sourcePath)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, ErrExplorationUnavailable
	}
	s.explorationOnce.Do(func() {
		s.explorationCache = &explorationThumbnailCache{dir: filepath.Join(s.cacheDir, "exploration"), budget: explorationThumbnailBudget, slot: make(chan struct{}, 1)}
	})
	key := fmt.Sprintf("%s:%d:%d:%d:%d", kind, id, before.Size(), before.ModTime().UnixNano(), edge)
	return s.explorationCache.resolve(ctx, key, edge, load, func() error {
		after, err := os.Stat(sourcePath)
		if err != nil {
			return err
		}
		if !os.SameFile(before, after) || !playbackProxyFingerprintOf(before).matches(playbackProxyFingerprintOf(after)) {
			return ErrBookmarkSourceChanged
		}
		return nil
	})
}

func (c *explorationThumbnailCache) resolve(ctx context.Context, key string, edge int, load func() (string, error), validate func() error) (*ThumbnailMedia, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(key))
	path := filepath.Join(c.dir, fmt.Sprintf("%x.jpg", hash))
	if stat, err := os.Stat(path); err == nil && stat.Mode().IsRegular() && stat.Size() > 0 {
		if err := validate(); err != nil {
			return nil, err
		}
		c.touch(path)
		return &ThumbnailMedia{Path: path, ModTime: stat.ModTime()}, nil
	}
	select {
	case c.slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.slot }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if stat, err := os.Stat(path); err == nil && stat.Size() > 0 {
		if err := validate(); err != nil {
			return nil, err
		}
		c.touch(path)
		return &ThumbnailMedia{Path: path, ModTime: stat.ModTime()}, nil
	}
	input, err := load()
	if err != nil {
		return nil, err
	}
	file, err := os.Open(input)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 8<<20+1))
	_ = file.Close()
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, ErrExplorationUnavailable
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width < 1 || config.Height < 1 || config.Width > 4096 || config.Height > 4096 {
		return nil, ErrExplorationUnavailable
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	resized := resizeExplorationThumbnail(decoded, edge)
	var output bytes.Buffer
	if err := jpeg.Encode(&output, resized, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	if err := validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return nil, err
	}
	if err := c.makeRoom(int64(output.Len())); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(c.dir, ".thumbnail-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(output.Bytes())
	closeErr := tmp.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validate(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	c.touch(path)
	return &ThumbnailMedia{Path: path, ModTime: stat.ModTime()}, nil
}

func (c *explorationThumbnailCache) touch(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessed == nil {
		c.accessed = make(map[string]time.Time)
	}
	c.accessed[path] = time.Now()
}

func (c *explorationThumbnailCache) makeRoom(incoming int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if incoming > c.budget {
		return ErrExplorationUnavailable
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}
	type cached struct {
		name string
		info os.FileInfo
	}
	files := []cached{}
	total := incoming
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jpg") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		files = append(files, cached{entry.Name(), info})
	}
	access := func(file cached) time.Time {
		if at, ok := c.accessed[filepath.Join(c.dir, file.name)]; ok {
			return at
		}
		return file.info.ModTime()
	}
	sort.Slice(files, func(i, j int) bool { return access(files[i]).Before(access(files[j])) })
	remainingFiles := len(files) + 1
	for _, file := range files {
		if total <= c.budget && remainingFiles <= 4096 {
			break
		}
		if err := os.Remove(filepath.Join(c.dir, file.name)); err != nil && !os.IsNotExist(err) {
			return err
		}
		total -= file.info.Size()
		remainingFiles--
		delete(c.accessed, filepath.Join(c.dir, file.name))
	}
	return nil
}

// Bilinear sampling bounds decoded output pixels, including large-DPR displays.
func resizeExplorationThumbnail(src image.Image, edge int) *image.RGBA {
	b := src.Bounds()
	ratio := math.Min(1, float64(edge)/float64(max(b.Dx(), b.Dy())))
	w, h := max(1, int(math.Round(float64(b.Dx())*ratio))), max(1, int(math.Round(float64(b.Dy())*ratio)))
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := math.Max(0, (float64(x)+.5)*float64(b.Dx())/float64(w)-.5)
			sy := math.Max(0, (float64(y)+.5)*float64(b.Dy())/float64(h)-.5)
			x0, y0 := int(sx), int(sy)
			dx, dy := sx-float64(x0), sy-float64(y0)
			coords := [4]image.Point{{x0, y0}, {min(x0+1, b.Dx()-1), y0}, {x0, min(y0+1, b.Dy()-1)}, {min(x0+1, b.Dx()-1), min(y0+1, b.Dy()-1)}}
			weights := [4]float64{(1 - dx) * (1 - dy), dx * (1 - dy), (1 - dx) * dy, dx * dy}
			var red, green, blue float64
			for i, p := range coords {
				r, g, bl, _ := src.At(b.Min.X+p.X, b.Min.Y+p.Y).RGBA()
				red += float64(r) * weights[i]
				green += float64(g) * weights[i]
				blue += float64(bl) * weights[i]
			}
			out.SetRGBA(x, y, color.RGBA{uint8(red / 257), uint8(green / 257), uint8(blue / 257), 255})
		}
	}
	return out
}
