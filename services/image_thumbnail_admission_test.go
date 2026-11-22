package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-master/models"
)

func TestImageJPEGGenerationReservesViewAndCancels(t *testing.T) {
	s := NewImageThumbnailService(t.TempDir())
	acquire := func(thumbnail bool) func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		release, err := s.acquireJPEGGeneration(ctx, thumbnail)
		if err != nil {
			t.Fatal(err)
		}
		return release
	}
	blocked := func(thumbnail bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		release, err := s.acquireJPEGGeneration(ctx, thumbnail)
		if !errors.Is(err, context.DeadlineExceeded) {
			if release != nil {
				release()
			}
			t.Fatalf("wanted queued cancellation, got %v", err)
		}
	}

	for range imageJPEGConcurrency - 1 {
		defer acquire(true)()
	}
	blocked(true)
	view := acquire(false)
	blocked(false)
	view()
	acquire(false)()
}

func TestImageViewCacheHitBypassesGenerationSlotsAndThumbnailLock(t *testing.T) {
	s := NewImageThumbnailService(t.TempDir())
	if err := os.MkdirAll(s.cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.cacheDir, "7.view.jpg")
	if err := os.WriteFile(path, []byte("cached"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlock, err := s.lockImageCache(ctx, filepath.Join(s.cacheDir, "7.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for range imageJPEGConcurrency {
		release, err := s.acquireJPEGGeneration(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	got, err := s.resolveViewCache(ctx, &models.Image{ID: 7}, imageDecoderSips, info)
	if err != nil || got.Path != path {
		t.Fatalf("expected cached view with zero generation slots, got %+v, %v", got, err)
	}
}

func TestImageJPEGGenerationRejectsChangedSource(t *testing.T) {
	s := NewImageThumbnailService(t.TempDir())
	source := filepath.Join(t.TempDir(), "source.heic")
	if err := os.WriteFile(source, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	oldInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("changed size"), 0600); err != nil {
		t.Fatal(err)
	}
	s.convertJPEG = func(context.Context, string, string, int) error {
		t.Fatal("decoder ran with a changed source")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = s.resolveViewCache(ctx, &models.Image{ID: 9, Path: source}, imageDecoderSips, oldInfo)
	if err == nil || err.Error() != "图片源文件在等待转码时已更改" {
		t.Fatalf("expected changed source error, got %v", err)
	}
}
