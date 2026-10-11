package services

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 图片首轮跨多个 stat 批次时，跳过计数、覆盖率与精确重复都与逐条处理相同。
func TestImageCleanupFirstPassAcrossStatBatches(t *testing.T) {
	setupImageServiceTestDB(t)
	root := t.TempDir()
	wantSkipped, wantPairs := 0, 0
	total := cleanupStatChunk + 200
	for index := 0; index < total; index++ {
		path := filepath.Join(root, fmt.Sprintf("img-%04d.jpg", index))
		content := []byte(fmt.Sprintf("unique-content-%04d", index))
		if index%40 == 1 {
			// 与上一张字节相同：精确重复。
			content = []byte(fmt.Sprintf("unique-content-%04d", index-1))
			wantPairs++
		}
		hash := fmt.Sprintf("%016x", uint64(index)*0x9e3779b97f4a7c15)
		img := imageCleanupCreateImage(t, path, content, hash, 64, 48)
		switch index % 9 {
		case 3:
			if err := os.Remove(img.Path); err != nil {
				t.Fatal(err)
			}
			wantSkipped++
		case 6:
			if err := os.Remove(img.Path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(img.Path, 0o755); err != nil {
				t.Fatal(err)
			}
			wantSkipped++
		}
	}
	analysis, err := newImageCleanupTestService().AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatal(err)
	}
	gotPairs := 0
	for _, group := range analysis.DuplicateGroups {
		gotPairs += len(group.Candidates)
	}
	// 被删掉或换成目录的那张不再成对。
	for index := 1; index < total; index += 40 {
		if index%9 == 3 || index%9 == 6 || (index-1)%9 == 3 || (index-1)%9 == 6 {
			wantPairs--
		}
	}
	if analysis.SkippedUnavailable != wantSkipped || gotPairs != wantPairs ||
		analysis.Coverage.PerceptualHash.Done != int64(total) || analysis.Coverage.PerceptualHash.Total != int64(total) {
		t.Fatalf("首轮结果不一致: skipped=%d want %d pairs=%d want %d coverage=%+v",
			analysis.SkippedUnavailable, wantSkipped, gotPairs, wantPairs, analysis.Coverage)
	}
}

func TestCleanupFileDeviceKnownOnUnix(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, known := cleanupFileDevice(info); known != (runtime.GOOS != "windows") {
		t.Fatalf("设备号可用性与平台不符: %v", known)
	}
	if _, known := cleanupFileDevice(nil); known {
		t.Fatal("nil 不应有设备号")
	}
}
