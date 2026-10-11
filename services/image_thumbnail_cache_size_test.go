package services

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// legacyPruneImageCache 是改为增量记账之前 pruneImageCacheLocked 的原样拷贝（每次整目录扫描），
// 作为淘汰结果的参照实现。
func legacyPruneImageCache(cacheDir string, maxCacheBytes int64, keepPath string) {
	entries, err := filepath.Glob(filepath.Join(cacheDir, "*.jpg"))
	if err != nil {
		return
	}
	type cacheEntry struct {
		path    string
		size    int64
		modTime time.Time
	}
	files := make([]cacheEntry, 0, len(entries))
	var total int64
	for _, path := range entries {
		if strings.HasPrefix(filepath.Base(path), ".") {
			continue
		}
		info, statErr := os.Stat(path)
		if statErr != nil || info.IsDir() {
			continue
		}
		files = append(files, cacheEntry{path: path, size: info.Size(), modTime: info.ModTime()})
		total += info.Size()
	}
	if total <= maxCacheBytes {
		return
	}
	// 与生产同一淘汰口径：超限后淘汰到上限的 90%（留余量，免得满载后每次生成都整目录扫描）。
	target := maxCacheBytes - maxCacheBytes/10
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	for _, file := range files {
		if total <= target {
			return
		}
		if file.path == keepPath {
			continue
		}
		if err := os.Remove(file.path); err == nil || os.IsNotExist(err) {
			total -= file.size
		}
	}
}

func cacheListing(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	listing := make(map[string]int64, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		listing[entry.Name()] = info.Size()
	}
	return listing
}

func writeCacheFile(t *testing.T, path string, size int, modTime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// 增量记账后的淘汰与每次整目录扫描的原实现逐步比对：新增、覆盖重生成、外部删除、
// 点开头的临时文件、启动前已有的缓存，目录内容每一步都相同。
func TestImageCacheLedgerEvictsLikeFullScan(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3} {
		rng := rand.New(rand.NewPCG(seed, 11))
		legacyDir := t.TempDir()
		svc := NewImageThumbnailService(t.TempDir())
		if err := os.MkdirAll(svc.cacheDir, 0o755); err != nil {
			t.Fatal(err)
		}
		svc.maxCacheBytes = 60_000
		base := time.Now().Add(-48 * time.Hour)
		for index := 0; index < 25; index++ { // 启动前就在的缓存
			name, size, modTime := fmt.Sprintf("%d.jpg", 500+index), 1+rng.IntN(4000), base.Add(time.Duration(rng.IntN(3600))*time.Second)
			writeCacheFile(t, filepath.Join(legacyDir, name), size, modTime)
			writeCacheFile(t, filepath.Join(svc.cacheDir, name), size, modTime)
		}
		scans := 0
		for step := 0; step < 600; step++ {
			name := fmt.Sprintf("%d.jpg", 1+rng.IntN(120))
			if rng.IntN(2) == 0 {
				name = strings.Replace(name, ".jpg", ".view.jpg", 1)
			}
			switch roll := rng.IntN(100); {
			case roll < 5: // 外部删除
				_ = os.Remove(filepath.Join(legacyDir, name))
				_ = os.Remove(filepath.Join(svc.cacheDir, name))
			case roll < 8: // 生成中的临时文件
				temp := fmt.Sprintf(".%d-tmp.jpg", step)
				writeCacheFile(t, filepath.Join(legacyDir, temp), 3000, base)
				writeCacheFile(t, filepath.Join(svc.cacheDir, temp), 3000, base)
			default: // 生成（新增或覆盖），mtime 偶尔落在过去
				size, modTime := 1+rng.IntN(4000), base.Add(time.Duration(3600+step)*time.Second)
				if rng.IntN(10) == 0 {
					modTime = base.Add(time.Duration(rng.IntN(3600)) * time.Second)
				}
				legacyPath, newPath := filepath.Join(legacyDir, name), filepath.Join(svc.cacheDir, name)
				writeCacheFile(t, legacyPath, size, modTime)
				writeCacheFile(t, newPath, size, modTime)
				legacyPruneImageCache(legacyDir, svc.maxCacheBytes, legacyPath)
				svc.cacheMu.Lock()
				svc.recordImageCacheFileLocked(newPath, int64(size))
				if svc.cacheSizes == nil || svc.cacheBytes > svc.maxCacheBytes {
					scans++
				}
				svc.pruneImageCacheLocked(newPath)
				svc.cacheMu.Unlock()
			}
			if got, want := cacheListing(t, svc.cacheDir), cacheListing(t, legacyDir); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("seed %d step %d 目录不一致:\n got %v\nwant %v", seed, step, got, want)
			}
		}
		if scans == 0 || scans > 400 {
			t.Fatalf("seed %d 扫描次数异常: %d", seed, scans)
		}
		t.Logf("seed %d full scans=%d of ~550 generations, listings identical", seed, scans)
	}
}

// 账面未超限时不扫描目录；外部塞进来的文件要等账面自己超限的那次扫描一并纠正，
// 扫描之后磁盘总量回到上限以内、账面与磁盘一致。
func TestImageCacheLedgerCorrectsExternalChangesOnNextScan(t *testing.T) {
	svc := NewImageThumbnailService(t.TempDir())
	if err := os.MkdirAll(svc.cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	svc.maxCacheBytes = 10_000
	base := time.Now().Add(-time.Hour)
	generate := func(name string, size int, modTime time.Time) {
		path := filepath.Join(svc.cacheDir, name)
		writeCacheFile(t, path, size, modTime)
		svc.cacheMu.Lock()
		svc.recordImageCacheFileLocked(path, int64(size))
		svc.pruneImageCacheLocked(path)
		svc.cacheMu.Unlock()
	}
	diskTotal := func() int64 {
		var total int64
		for name, size := range cacheListing(t, svc.cacheDir) {
			if !strings.HasPrefix(name, ".") {
				total += size
			}
		}
		return total
	}
	generate("1.jpg", 2000, base) // 第一次建账
	if svc.cacheSizes == nil || svc.cacheBytes != 2000 {
		t.Fatalf("首次应扫描建账: %v %d", svc.cacheSizes, svc.cacheBytes)
	}
	// 外部塞进一个大文件：账面不知道，未超限时不扫描、不淘汰。
	writeCacheFile(t, filepath.Join(svc.cacheDir, "external.jpg"), 7000, base.Add(time.Second))
	generate("2.jpg", 2000, base.Add(2*time.Second))
	if svc.cacheBytes != 4000 || diskTotal() != 11_000 {
		t.Fatalf("未超限不应扫描: ledger=%d disk=%d", svc.cacheBytes, diskTotal())
	}
	// 覆盖重生成按差额记账。
	generate("2.jpg", 3000, base.Add(3*time.Second))
	if svc.cacheBytes != 5000 {
		t.Fatalf("覆盖应按差额记账: %d", svc.cacheBytes)
	}
	// 账面涨过上限 → 整目录扫描，按 mtime 淘汰（最旧的 1.jpg 与外部文件），账面与磁盘一致。
	generate("3.jpg", 6000, base.Add(4*time.Second))
	listing := cacheListing(t, svc.cacheDir)
	if diskTotal() > svc.maxCacheBytes || svc.cacheBytes != diskTotal() || listing["3.jpg"] != 6000 {
		t.Fatalf("超限扫描后应纠正: ledger=%d disk=%d listing=%v", svc.cacheBytes, diskTotal(), listing)
	}
	if _, ok := listing["1.jpg"]; ok {
		t.Fatalf("最旧的应先被淘汰: %v", listing)
	}
	// 外部删除：账面偏大，下一次超限检查扫描后按实况重建。
	for name := range listing {
		_ = os.Remove(filepath.Join(svc.cacheDir, name))
	}
	generate("4.jpg", 5000, base.Add(5*time.Second))
	if svc.cacheBytes != 5000 || diskTotal() != 5000 {
		t.Fatalf("外部删除后应按实况重建: ledger=%d disk=%d", svc.cacheBytes, diskTotal())
	}
}

// 超限后淘汰到上限的 90%：满载后连续生成不会每次都触发整目录扫描。
func TestImageCachePrunesToLowWaterMark(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageThumbnailService(t.TempDir())
	svc.maxCacheBytes = 100_000
	if err := os.MkdirAll(svc.cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	for index := 0; index < 11; index++ {
		writeCacheFile(t, filepath.Join(svc.cacheDir, fmt.Sprintf("%d.jpg", index+1)), 10_000, base.Add(time.Duration(index)*time.Minute))
	}
	svc.cacheMu.Lock()
	svc.pruneImageCacheLocked("")
	total := svc.cacheBytes
	svc.cacheMu.Unlock()
	if total > 90_000 {
		t.Fatalf("超限后应淘汰到上限的 90%% 以内，实际 %d", total)
	}
	if _, err := os.Stat(filepath.Join(svc.cacheDir, "1.jpg")); !os.IsNotExist(err) {
		t.Fatalf("最旧的缓存应先被淘汰: %v", err)
	}
	if _, err := os.Stat(filepath.Join(svc.cacheDir, "11.jpg")); err != nil {
		t.Fatalf("最新的缓存应保留: %v", err)
	}
}
