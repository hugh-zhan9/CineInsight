package services

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// Run explicitly with a public upright face JPEG; no personal media is read by default.
func TestAppleVisionNativeIntegration(t *testing.T) {
	fixture := os.Getenv("CINEINSIGHT_TEST_APPLE_FACE")
	if fixture == "" {
		t.Skip("set CINEINSIGHT_TEST_APPLE_FACE to a public face JPEG")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runtime := NewFaceRuntime(t.TempDir(), nil)
	session, err := startFaceWorkerProcess(ctx, runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	faces, err := session.Detect(ctx, "native", fixture)
	if err != nil || len(faces) != 1 {
		t.Fatalf("faces=%d err=%v", len(faces), err)
	}
	if len(faces[0].Embedding) != 128 {
		t.Fatal("face shape")
	}
	box := faces[0].BBox
	if box.X < 0 || box.Y < 0 || box.X+box.W > 1 || box.Y+box.H > 1 {
		t.Fatal(box)
	}
	crop := filepath.Join(t.TempDir(), "crop.jpg")
	if err := cropFaceThumbnail(fixture, 0, box, crop); err != nil {
		t.Fatal(err)
	}
	crops, err := session.Detect(ctx, "crop", crop)
	if err != nil || len(crops) != 1 {
		t.Fatalf("crop faces=%d err=%v", len(crops), err)
	}
	v, _ := normalizeFaceEmbedding(faces[0].Embedding)
	w, _ := normalizeFaceEmbedding(crops[0].Embedding)
	if faceSimilarity(v, w) < faceClusterMergeThreshold {
		t.Fatal("projected crop does not match source face", faceSimilarity(v, w))
	}
	setupSceneDB(t, SceneProviderLocal)
	dir := t.TempDir()
	path := filepath.Join(dir, "native.mp4")
	ffmpeg, err := findThumbnailFFmpeg()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-y", "-loop", "1", "-i", fixture, "-t", "6", "-vf", "scale=320:320:force_original_aspect_ratio=decrease,pad=320:320:(ow-iw)/2:(oh-ih)/2", "-pix_fmt", "yuv420p", path).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	info, _ := os.Stat(path)
	video := models.Video{Name: "native.mp4", Path: path, Directory: dir, Size: info.Size(), Duration: 6}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	index := NewSceneIndexService(NewSceneRuntime(t.TempDir(), nil), nil)
	defer index.StopAndWait()
	if _, err := index.Start(ctx, SceneIndexRequest{VideoIDs: []uint{video.ID}, Provider: SceneProviderApple}); err != nil {
		t.Fatal(err)
	}
	status := waitSceneIndex(t, index, func(s SceneIndexStatus) bool { return !s.Running })
	if status.Succeeded != 1 {
		t.Fatal(status)
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	search := NewSceneSearchService(index)
	result, err := search.SearchByImage(ctx, SceneImageSearchRequest{ImageBase64: base64.StdEncoding.EncodeToString(data)})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].VideoID != video.ID {
		t.Fatalf("native image search %+v %v", result, err)
	}
	t.Logf("face cosine %.6f; native video image hit score %.6f", faceSimilarity(v, w), result.Hits[0].Score)
}
