package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"video-master/models"
)

// exFAT / FAT / SMB 卷上，macOS 给带扩展属性（如 com.apple.provenance）的新文件旁边生成
// AppleDouble 文件 `._<name>`。超分工作目录建在源视频旁边，帧目录里因此每张 PNG 都多一个
// `._000001.png`：按 .png 后缀计数会得到双倍（用户实测「抽帧数量不完整（240/120）」），
// sidecar 也会试图解码这些伪 PNG。
func TestEnhancementPipelineIgnoresAppleDoubleCompanions(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	base := fakeEnhancementCommands(t, video.Path, 130, 24)
	frameName := regexp.MustCompile(`^\d{6}\.png$`)
	addCompanions := func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if frameName.MatchString(entry.Name()) {
				if err := os.WriteFile(filepath.Join(dir, "._"+entry.Name()), []byte("appledouble"), 0644); err != nil {
					return err
				}
			}
		}
		return nil
	}
	runner := func(ctx context.Context, name string, args []string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(name, "sidecar") {
			inDir := args[indexOfArg(args, "-i")+1]
			entries, err := os.ReadDir(inDir)
			if err != nil {
				return "", err
			}
			for _, entry := range entries {
				if !frameName.MatchString(entry.Name()) {
					return "", fmt.Errorf("decode %s failed", entry.Name())
				}
			}
			out, err := base(ctx, name, args)
			if err != nil {
				return out, err
			}
			return out, addCompanions(args[indexOfArg(args, "-o")+1])
		}
		out, err := base(ctx, name, args)
		if err == nil && strings.Contains(joined, "-f image2") {
			err = addCompanions(filepath.Dir(args[len(args)-1]))
		}
		return out, err
	}
	service := newEnhancementTestService(t, runner)
	view, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"})
	if err != nil {
		t.Fatal(err)
	}
	task := waitEnhancementTask(t, view.ID, models.EnhancementStatusCompleted)
	service.StopAndWait()
	if task.CommittedFrames != 130 {
		t.Fatalf("AppleDouble 伴生文件不应计入帧数: %+v", task)
	}
}

func TestCountEnhancementFramesCountsOnlyFrameFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"000001.png", "000002.png", "._000001.png", "._000002.png", "notes.png", ".DS_Store"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "000003.png"), 0755); err != nil {
		t.Fatal(err)
	}
	count, err := countEnhancementFrames(dir)
	if err != nil || count != 2 {
		t.Fatalf("只应统计 6 位序号帧文件 count=%d err=%v", count, err)
	}
	if err := removeAppleDoubleFrames(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"._000001.png", "._000002.png"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s 应被清理: %v", name, err)
		}
	}
	for _, name := range []string{"000001.png", "notes.png", ".DS_Store"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s 不应被删除: %v", name, err)
		}
	}
}
