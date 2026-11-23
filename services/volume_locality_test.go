package services

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestExternalVolumeWarmThumbnailSkipsSourceStat(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	// Source file does not exist (simulating an unmounted/sleeping NAS or external HDD).
	missingNASPath := filepath.Join(root, "nas-share", "movie.mp4")
	video := models.Video{Name: "movie.mp4", Path: missingNASPath, Directory: filepath.Dir(missingNASPath), Duration: 120}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video failed: %v", err)
	}

	dataDir := filepath.Join(root, "data")
	service := NewThumbnailService(&VideoService{}, dataDir)
	if err := os.MkdirAll(service.cacheDir, 0755); err != nil {
		t.Fatalf("mkdir cache dir failed: %v", err)
	}
	cachePath := filepath.Join(service.cacheDir, strconv.FormatUint(uint64(video.ID), 10)+".jpg")
	if err := os.WriteFile(cachePath, []byte("warm-ssd-thumbnail"), 0644); err != nil {
		t.Fatalf("write warm cache failed: %v", err)
	}

	orig := isExternalOrRemotePathFunc
	isExternalOrRemotePathFunc = func(p string) bool { return p == missingNASPath }
	t.Cleanup(func() { isExternalOrRemotePathFunc = orig })

	media, err := service.ResolveThumbnail(context.Background(), video.ID)
	if err != nil {
		t.Fatalf("warm external thumbnail should succeed even when NAS source is offline: %v", err)
	}
	if media.Path != cachePath {
		t.Fatalf("unexpected cache path: got=%s want=%s", media.Path, cachePath)
	}
}

func TestExternalVolumeWarmImageThumbnailSkipsSourceStat(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	missingNASPhoto := filepath.Join(root, "nas-photos", "shot.jpg")
	img := models.Image{
		Name:           "shot.jpg",
		Path:           missingNASPhoto,
		Directory:      filepath.Dir(missingNASPhoto),
		Format:         "jpg",
		Width:          4000,
		Height:         3000,
		PerceptualHash: "0123456789abcdef",
	}
	if err := database.DB.Create(&img).Error; err != nil {
		t.Fatalf("create image failed: %v", err)
	}

	dataDir := filepath.Join(root, "data")
	service := NewImageThumbnailService(dataDir)
	if err := os.MkdirAll(service.cacheDir, 0755); err != nil {
		t.Fatalf("mkdir image cache dir failed: %v", err)
	}
	cachePath := filepath.Join(service.cacheDir, strconv.FormatUint(uint64(img.ID), 10)+".jpg")
	if err := os.WriteFile(cachePath, []byte("warm-ssd-image-thumb"), 0644); err != nil {
		t.Fatalf("write warm image cache failed: %v", err)
	}

	orig := isExternalOrRemotePathFunc
	isExternalOrRemotePathFunc = func(p string) bool { return p == missingNASPhoto }
	t.Cleanup(func() { isExternalOrRemotePathFunc = orig })

	media, err := service.ResolveImageThumbnail(context.Background(), img.ID)
	if err != nil {
		t.Fatalf("warm external image thumbnail should succeed when NAS is offline: %v", err)
	}
	if media.Path != cachePath {
		t.Fatalf("unexpected image cache path: got=%s want=%s", media.Path, cachePath)
	}
}
