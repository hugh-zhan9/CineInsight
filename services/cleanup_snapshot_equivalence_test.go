package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// snapshotFixture 建一批视频：普通文件、缺失、目录、FIFO（非普通文件）；部分有感知哈希
// （含失效与不完整的行）、帧哈希序列与标签（含已删除的标签）。返回首轮口径的
// presentVideoIDs 与 stat 快照。
func snapshotFixture(t *testing.T, seed uint64, count int) (map[uint]struct{}, cleanupFileSnapshots, map[[2]uint]struct{}) {
	t.Helper()
	setupCleanupServiceTestDB(t)
	rng := rand.New(rand.NewPCG(seed, 99))
	root := t.TempDir()
	tags := make([]models.Tag, 4)
	for index := range tags {
		tags[index] = models.Tag{Name: fmt.Sprintf("tag-%d", index)}
		if err := database.DB.Create(&tags[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Delete(&tags[3]).Error; err != nil {
		t.Fatal(err)
	}
	contents := make([]uint64, 6)
	for index := range contents {
		contents[index] = rng.Uint64()
	}
	sources := make([][]uint64, 3)
	for index := range sources {
		sources[index] = randomFrameHashes(int64(seed)*10+int64(index), 60)
	}
	present := make(map[uint]struct{})
	files := make(cleanupFileSnapshots)
	ids := make([]uint, 0, count)
	for index := 0; index < count; index++ {
		path := filepath.Join(root, fmt.Sprintf("v%03d.mp4", index))
		mode := []string{"file", "file", "file", "file", "missing", "dir", "fifo"}[rng.IntN(7)]
		switch mode {
		case "file":
			mustWriteSizedFile(t, path, []byte(strings.Repeat("x", 10+index)))
		case "dir":
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
		case "fifo":
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Skipf("无法创建 FIFO: %v", err)
			}
		}
		video := models.Video{Name: filepath.Base(path), Path: path, Directory: root, Size: int64(10 + index),
			Duration: 100, Width: 1920, Height: 1080, Resolution: "1920x1080"}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, video.ID)
		if picked := rng.IntN(4); picked > 0 {
			if err := database.DB.Model(&video).Association("Tags").Append(tags[:picked]); err != nil {
				t.Fatal(err)
			}
		}
		info, statErr := os.Stat(path)
		files.record(video.ID, path, info, statErr)
		if statErr == nil && !info.IsDir() {
			present[video.ID] = struct{}{}
		}
		seedSnapshotFingerprints(t, rng, video, info, statErr, contents, sources)
	}
	excluded := make(map[[2]uint]struct{})
	for range count / 6 {
		excluded[cleanupVideoPairKey(ids[rng.IntN(len(ids))], ids[rng.IntN(len(ids))])] = struct{}{}
	}
	return present, files, excluded
}

func seedSnapshotFingerprints(t *testing.T, rng *rand.Rand, video models.Video, info os.FileInfo, statErr error, contents []uint64, sources [][]uint64) {
	t.Helper()
	size, mod := int64(1), int64(1)
	if statErr == nil {
		size, mod = info.Size(), info.ModTime().UnixNano()
	}
	frame := func() string {
		return fmt.Sprintf("%016x", equivalenceFlip(rng, contents[rng.IntN(len(contents))], rng.IntN(3)))
	}
	switch roll := rng.IntN(20); {
	case roll < 4: // 从没回填过
	default:
		row := models.VideoPerceptualHash{VideoID: video.ID, SourceSize: size, SourceModTimeNS: mod, ComputedAt: time.Now()}
		row.HashEarly = frame()
		row.HashMiddle, row.HashLate = row.HashEarly, row.HashEarly
		if roll < 7 {
			row.SourceSize++ // 失效
		} else if roll < 9 {
			row.HashLate = "" // 回填失败的不完整行
		}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if rng.IntN(2) == 0 {
		source := sources[rng.IntN(len(sources))]
		hashes := source
		if rng.IntN(2) == 0 {
			start := rng.IntN(30)
			hashes = source[start : start+20]
		}
		row := models.VideoFrameHashSequence{VideoID: video.ID, IntervalMS: clipFrameIntervalMS, Hashes: encodeFrameHashes(hashes),
			FrameCount: len(hashes), SourceSize: size, SourceModTimeNS: mod, ComputedAt: time.Now()}
		if rng.IntN(5) == 0 {
			row.SourceModTimeNS++ // 失效
		}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// 近似重复：首轮快照替代逐行 stat、只为成组成员读标签之后，结果与原实现（整库
// Preload("Video.Tags") + 逐行 os.Stat）逐字段相同，JSON 也逐字节相同（[] 与 null 不混）。
func TestNearDuplicateGroupsWithSnapshotEquivalentToLegacy(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3} {
		present, files, excluded := snapshotFixture(t, seed, 70)
		ctx := context.Background()
		wantGroups, wantPairs, wantStale, err := legacyLoadCleanupNearDuplicateGroups(ctx, excluded, present)
		if err != nil {
			t.Fatal(err)
		}
		for _, snapshot := range []cleanupFileSnapshots{files, nil} {
			gotGroups, gotPairs, gotStale, err := loadCleanupNearDuplicateGroups(ctx, excluded, present, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotGroups, wantGroups) || !reflect.DeepEqual(gotPairs, wantPairs) || gotStale != wantStale {
				t.Fatalf("seed %d snapshot=%v 不一致:\n got %+v %v %d\nwant %+v %v %d", seed, snapshot != nil, gotGroups, gotPairs, gotStale, wantGroups, wantPairs, wantStale)
			}
			gotJSON, _ := json.Marshal(gotGroups)
			wantJSON, _ := json.Marshal(wantGroups)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("seed %d JSON 不一致:\n got %s\nwant %s", seed, gotJSON, wantJSON)
			}
		}
		wantJSON, _ := json.Marshal(wantGroups)
		if len(wantGroups) < 2 || wantStale == 0 || !strings.Contains(string(wantJSON), `"tags":[]`) || !strings.Contains(string(wantJSON), `"name":"tag-0"`) {
			t.Fatalf("seed %d 夹具覆盖不足: groups=%d stale=%d", seed, len(wantGroups), wantStale)
		}
		t.Logf("seed %d groups=%d pairs=%d stale=%d identical", seed, len(wantGroups), len(wantPairs), wantStale)
	}
}

// 截取片段：用首轮快照与逐个现场 stat（files 为 nil，即原先的判据）结果相同。
func TestClipGroupsWithSnapshotEquivalentToPerRowStat(t *testing.T) {
	for _, seed := range []uint64{4, 5} {
		present, files, _ := snapshotFixture(t, seed, 70)
		svc := newClipFixtureCleanupService()
		wantGroups, wantStale, wantSkipped, err := svc.loadCleanupClipGroups(context.Background(), nil, present, nil)
		if err != nil {
			t.Fatal(err)
		}
		gotGroups, gotStale, gotSkipped, err := svc.loadCleanupClipGroups(context.Background(), nil, present, files)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotGroups, wantGroups) || gotStale != wantStale || gotSkipped != wantSkipped {
			t.Fatalf("seed %d 不一致: got %d/%d/%d want %d/%d/%d", seed, len(gotGroups), gotStale, gotSkipped, len(wantGroups), wantStale, wantSkipped)
		}
		if len(wantGroups) == 0 || wantStale == 0 {
			t.Fatalf("seed %d 夹具覆盖不足: groups=%d stale=%d", seed, len(wantGroups), wantStale)
		}
		t.Logf("seed %d clip groups=%d stale=%d identical", seed, len(wantGroups), wantStale)
	}
}
