package services

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestMediaExplorationAssetPinsSourceAndRechecksVisibility(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(path, []byte("original fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "movie", Path: path, Directory: root, Duration: 100}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	s := NewMediaExplorationService()
	state, err := s.Open(context.Background(), "video")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	r := explorationRequest(state, "sample")
	r.EntryID = state.Entries[0].EntryID
	r.SourceVersion = videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano())
	state = explorationUpdate(t, s, r)
	assetContext := ExplorationAssetContext{ExplorationContext: state.ExplorationContext, EntryID: r.EntryID, SourceVersion: r.SourceVersion}
	vs := NewVideoService(nil)
	asset, err := s.ResolveAsset(context.Background(), assetContext, "video", video.ID, "media", 0, 0, nil, nil, vs)
	if err != nil {
		t.Fatal(err)
	}
	defer asset.Close()
	data, err := io.ReadAll(asset.Reader)
	if err != nil || string(data) != "original fixture" {
		t.Fatal("wrong source", err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	asset.Reader.Seek(0, io.SeekStart)
	if _, err := asset.Reader.Read(make([]byte, 4)); !errors.Is(err, ErrBookmarkSourceChanged) {
		t.Fatal("body kept reading after replacement", err)
	}
	if _, err := s.ResolveAsset(context.Background(), assetContext, "video", video.ID, "media", 0, 0, nil, nil, vs); !errors.Is(err, ErrBookmarkSourceChanged) {
		t.Fatal("next Range reopened new source", err)
	}
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("scan_exclude_paths", root).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAsset(context.Background(), assetContext, "video", video.ID, "media", 0, 0, nil, nil, vs); !errors.Is(err, ErrExplorationUnavailable) {
		t.Fatal("blacklist bypass", err)
	}
}

func TestMediaExplorationAssetRejectsForgedSceneAndCancelledRead(t *testing.T) {
	setupImageServiceTestDB(t)
	path := explorationJPEG(t)
	photo := models.Image{Name: "photo", Path: path, Directory: filepath.Dir(path), Format: "jpg"}
	if err := database.DB.Create(&photo).Error; err != nil {
		t.Fatal(err)
	}
	s := NewMediaExplorationService()
	state, err := s.Open(context.Background(), "image")
	if err != nil {
		t.Fatal(err)
	}
	r := ExplorationAssetContext{ExplorationContext: state.ExplorationContext, EntryID: state.Entries[0].EntryID}
	images := NewImageThumbnailService(t.TempDir())
	bad := r
	bad.SceneID = "forged"
	if _, err := s.ResolveAsset(context.Background(), bad, "image", photo.ID, "image", 0, 0, nil, images, nil); !errors.Is(err, ErrExplorationScene) {
		t.Fatal("forged scene", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	asset, err := s.ResolveAsset(ctx, r, "image", photo.ID, "image", 0, 0, nil, images, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer asset.Close()
	cancel()
	if _, err := asset.Reader.Read(make([]byte, 10)); !errors.Is(err, context.Canceled) {
		t.Fatal("late image read after cancellation", err)
	}
}
