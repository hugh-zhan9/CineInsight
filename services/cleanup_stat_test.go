package services

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 分批并行 stat 必须与逐条 os.Stat 逐项相同（跨多个批次、含缺失与目录），顺序不变。
func TestCleanupStatBatcherMatchesSequentialStat(t *testing.T) {
	root := t.TempDir()
	rng := rand.New(rand.NewPCG(5, 6))
	paths := make([]string, 2*cleanupStatChunk+37)
	for index := range paths {
		paths[index] = filepath.Join(root, fmt.Sprintf("p%04d", index))
		switch rng.IntN(4) {
		case 0, 1:
			mustWriteSizedFile(t, paths[index], make([]byte, rng.IntN(64)))
		case 2:
			if err := os.Mkdir(paths[index], 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	stats := newCleanupStatBatcher(context.Background(), paths)
	for index, path := range paths {
		gotInfo, gotErr := stats.stat(index)
		wantInfo, wantErr := os.Stat(path)
		if (gotErr == nil) != (wantErr == nil) || (wantErr != nil && os.IsNotExist(gotErr) != os.IsNotExist(wantErr)) {
			t.Fatalf("%d 错误不一致: got %v want %v", index, gotErr, wantErr)
		}
		if wantErr == nil && (gotInfo.Name() != wantInfo.Name() || gotInfo.Size() != wantInfo.Size() ||
			gotInfo.Mode() != wantInfo.Mode() || !gotInfo.ModTime().Equal(wantInfo.ModTime())) {
			t.Fatalf("%d 结果不一致: got %v want %v", index, gotInfo, wantInfo)
		}
	}
}

// 首轮跨多个 stat 批次时，跳过计数、类别与快照口径都与逐条处理相同；取消立即生效。
func TestCleanupAnalysisFirstPassAcrossStatBatches(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	wantMissing, wantDirs, wantShort := 0, 0, 0
	for index := 0; index < cleanupStatChunk+300; index++ {
		path := filepath.Join(root, fmt.Sprintf("clip-%04d.mp4", index))
		size := int64(1000 + index)
		switch index % 9 {
		case 3:
			wantMissing++
		case 6:
			wantDirs++
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		default:
			mustWriteSizedFile(t, path, make([]byte, size))
		}
		duration := 100.0
		if index%5 == 0 && index%9 != 3 && index%9 != 6 {
			duration = 2
			wantShort++
		}
		video := models.Video{Name: filepath.Base(path), Path: path, Directory: root, Size: size,
			Duration: duration, Width: 1920, Height: 1080, Resolution: "1920x1080"}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &CleanupService{}
	result, err := svc.AnalyzeCleanupCandidates(CleanupCriteria{MinDuration: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.SkippedUnavailable != wantMissing+wantDirs || len(result.LowDuration) != wantShort || result.SkippedMetadata != 0 {
		t.Fatalf("首轮结果不一致: skipped=%d want %d short=%d want %d metadata=%d",
			result.SkippedUnavailable, wantMissing+wantDirs, len(result.LowDuration), wantShort, result.SkippedMetadata)
	}
	for index := 1; index < len(result.LowDuration); index++ {
		if result.LowDuration[index-1].ID >= result.LowDuration[index].ID {
			t.Fatal("类别顺序必须保持原先的 ID 升序")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := svc.analyzeCleanupCandidates(ctx, CleanupCriteria{}); err != context.Canceled {
		t.Fatalf("取消后必须返回 context.Canceled: %v", err)
	}
}
