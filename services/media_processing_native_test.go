package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestMediaProcessingRecoversOnlyProvenDeadOwnedPreviews(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process identity probing unavailable")
	}
	root := t.TempDir()
	for _, entry := range []struct {
		name   string
		pid    int
		marked bool
	}{{"preview-dead", 2147483647, true}, {"preview-live", os.Getpid(), true}, {"preview-unowned", 2147483647, false}} {
		dir := filepath.Join(root, entry.name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if entry.marked {
			raw, _ := json.Marshal(map[string]int{"version": 1, "pid": entry.pid})
			if err := os.WriteFile(filepath.Join(dir, ".owner.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := recoverProcessingPreviews(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "preview-dead")); !os.IsNotExist(err) {
		t.Fatal("dead owned preview survived")
	}
	for _, name := range []string{"preview-live", "preview-unowned"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatal("removed active or unowned content")
		}
	}
}

// 使用已校验运行时/模型与合成媒体，不连接或读取用户片库。
func TestMediaProcessingNativeTrials(t *testing.T) {
	runtimeDir := os.Getenv("CINEINSIGHT_TEST_ENHANCEMENT_RUNTIME")
	if runtimeDir == "" {
		t.Skip("set CINEINSIGHT_TEST_ENHANCEMENT_RUNTIME for native synthetic-media verification")
	}
	capability := ProbeEnhancementRuntime(runtimeDir, os.Getenv("CINEINSIGHT_TEST_ENHANCEMENT_MODELS"))
	if !capability.Available {
		t.Fatalf("configured runtime unavailable: %s %s", capability.ReasonCode, capability.Message)
	}
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	sourcePath := filepath.Join(root, "synthetic.png")
	raw := processingTestPNG(t, false)
	if err := os.WriteFile(sourcePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	photo := imageThumbnailTestCreateImage(t, sourcePath, "png")
	enhancer := NewEnhancementService(nil, nil, nil, root)
	enhancer.capability = capability
	enhancer.runCommand = func(ctx context.Context, name string, args []string) (string, error) {
		tail, err := runEnhancementCommand(ctx, name, args)
		if err != nil {
			t.Logf("synthetic native command failed: %s %v %s", filepath.Base(name), err, tail)
		}
		return tail, err
	}
	service := NewMediaProcessingService(context.Background(), root, NewMediaWorkSlot(), enhancer)
	defer service.StopAndWait(context.Background())
	wait := func(id string) *MediaProcessingView {
		t.Helper()
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			view, err := service.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if view.State == "ready" || view.State == "failed" {
				if view.State != "ready" {
					t.Fatalf("native processing: %s", view.Error)
				}
				return view
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("native trial timed out")
		return nil
	}
	parent, err := service.Begin(context.Background(), MediaProcessingRequest{Kind: "image", MediaID: photo.ID, Operation: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"general", "anime"} {
		view, err := service.Begin(context.Background(), MediaProcessingRequest{Kind: "image", MediaID: photo.ID, ParentID: parent.ID, Operation: "enhance", Profile: profile, X: 8, Y: 8, Width: 16, Height: 8})
		if err != nil {
			t.Fatal(err)
		}
		ready := wait(view.ID)
		if ready.Width != 32 || ready.Height != 16 || ready.ComparisonURL == "" {
			t.Fatal(ready)
		}
		if err = service.Save(context.Background(), view.ID, filepath.Join(root, profile+".png")); err != nil {
			t.Fatal(err)
		}
		if err = service.Release(context.Background(), view.ID); err != nil {
			t.Fatal(err)
		}
	}
	ffmpeg, err := findThumbnailFFmpeg()
	if err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(root, "synthetic.mp4")
	if _, err = runEnhancementCommand(context.Background(), ffmpeg, []string{"-v", "error", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=2", "-t", "2", "-an", "-c:v", "libx264", "-threads", "2", "-pix_fmt", "yuv420p", videoPath}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(videoPath)
	video := models.Video{Name: "synthetic.mp4", Path: videoPath, Directory: root, Size: info.Size(), Width: 64, Height: 64, Duration: 2}
	if err = database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	videoSource, err := service.Begin(context.Background(), MediaProcessingRequest{Kind: "video", MediaID: video.ID, Operation: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	trial, err := service.Begin(context.Background(), MediaProcessingRequest{Kind: "video", MediaID: video.ID, ParentID: videoSource.ID, Operation: "sample", Profile: "anime", StartSeconds: 0.5, DurationSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	ready := wait(trial.ID)
	if ready.Width != 128 || ready.Height != 128 || ready.Format != "mp4" || ready.ComparisonURL == "" {
		t.Fatal(ready)
	}
	file, release, err := service.OpenAsset(context.Background(), trial.ID, "result")
	if err != nil {
		t.Fatal(err)
	}
	outputPath := file.Name()
	release()
	probe, err := enhancer.probeEnhancementSource(context.Background(), outputPath)
	if err != nil || probe.Width != 128 || probe.Height != 128 || probe.Duration < 0.9 || probe.Duration > 1.1 {
		t.Fatalf("wrong sample: %#v %v", probe, err)
	}
	var tasks, events int64
	database.DB.Model(&models.VideoEnhancementTask{}).Count(&tasks)
	database.DB.Model(&models.PlayEvent{}).Count(&events)
	if tasks != 0 || events != 0 {
		t.Fatalf("trial leaked persistent tasks/views: %d/%d", tasks, events)
	}
	unchanged, err := os.ReadFile(sourcePath)
	if err != nil || string(unchanged) != string(raw) {
		t.Fatal("native image trial changed source")
	}
}
