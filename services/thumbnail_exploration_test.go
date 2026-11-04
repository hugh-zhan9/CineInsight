package services

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func explorationJPEG(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "original.jpg")
	picture := image.NewRGBA(image.Rect(0, 0, 640, 360))
	for y := 0; y < 360; y++ {
		for x := 0; x < 640; x++ {
			picture.SetRGBA(x, y, color.RGBA{uint8(x % 255), uint8(y % 255), 127, 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(file, picture, nil); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExplorationThumbnailDimensionsReuseAndBudget(t *testing.T) {
	input := explorationJPEG(t)
	c := &explorationThumbnailCache{dir: t.TempDir(), budget: 128 << 20, slot: make(chan struct{}, 1)}
	calls := 0
	load := func() (string, error) { calls++; return input, nil }
	validate := func() error { return nil }
	first, err := c.resolve(context.Background(), "first", 96, load, validate)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(file)
	file.Close()
	if err != nil || cfg.Width != 96 || cfg.Height != 54 {
		t.Fatalf("small tier %+v %v", cfg, err)
	}
	if _, err := c.resolve(context.Background(), "first", 96, load, validate); err != nil || calls != 1 {
		t.Fatal("hot cache decoded again", err)
	}
	stat, _ := os.Stat(first.Path)
	c.budget = stat.Size() + 10
	second, err := c.resolve(context.Background(), "second", 96, load, validate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Fatal("LRU budget not enforced")
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(input); err != nil {
		t.Fatal("source cache deleted")
	}
	large := resizeExplorationThumbnail(image.NewRGBA(image.Rect(0, 0, 500, 1000)), 256)
	if large.Bounds().Dx() != 128 || large.Bounds().Dy() != 256 {
		t.Fatal("portrait tier distorted")
	}
}

func TestExplorationThumbnailCancellationAndChangedSource(t *testing.T) {
	input := explorationJPEG(t)
	c := &explorationThumbnailCache{dir: t.TempDir(), budget: 128 << 20, slot: make(chan struct{}, 1)}
	c.slot <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if _, err := c.resolve(ctx, "cancel", 96, func() (string, error) { called = true; return input, nil }, func() error { return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatal("cancelled work started", err)
	}
	<-c.slot
	if _, err := c.resolve(context.Background(), "changed", 96, func() (string, error) { return input, nil }, func() error { return ErrBookmarkSourceChanged }); !errors.Is(err, ErrBookmarkSourceChanged) {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(c.dir)
	if len(entries) != 0 {
		t.Fatal("changed source cached")
	}
	// Waiting for the one cold slot is cancellable, unlike an unbounded decoder queue.
	c.slot <- struct{}{}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.resolve(ctx, "wait", 96, func() (string, error) { called = true; return input, nil }, func() error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	<-c.slot
}

func TestExplorationThumbnailHotAccessDoesNotInvalidateInflightRead(t *testing.T) {
	input := explorationJPEG(t)
	c := &explorationThumbnailCache{dir: t.TempDir(), budget: 128 << 20, slot: make(chan struct{}, 1)}
	load := func() (string, error) { return input, nil }
	validate := func() error { return nil }
	first, err := c.resolve(context.Background(), "shared", 96, load, validate)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Unix(1600000000, 0)
	if err := os.Chtimes(first.Path, past, past); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	sourceInfo, _ := source.Stat()
	delivery, err := os.Open(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer delivery.Close()
	deliveryInfo, _ := delivery.Stat()
	reader := &explorationAssetReader{reader: delivery, ctx: context.Background(), source: source, sourcePath: input, sourceInfo: sourceInfo, delivery: delivery, deliveryInfo: deliveryInfo}
	if _, err := c.resolve(context.Background(), "shared", 96, load, validate); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(make([]byte, 10)); err != nil {
		t.Fatal("hot access changed content identity", err)
	}
}
