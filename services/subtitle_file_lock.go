package services

import (
	"fmt"
	"hash/fnv"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/text/unicode/norm"
)

var subtitleFileMutationLocks [64]sync.Mutex

// lockSubtitleFile 按规范化后的 .srt 路径加锁：movie.mp4 与 movie.mkv 共用 movie.srt，
// 必须互斥，所以锁的键是字幕文件而不是视频 ID。
// 调用方传 subtitleparser.SRTPathForVideo(videoPath) 的结果，键的规整见 subtitleFileLockKey。
func subtitleFileLockBucket(srtPath string) int {
	// 父目录别名（如 /var 与 /private/var）也必须落到同一个实际桶。
	if absolute, err := filepath.Abs(srtPath); err == nil {
		srtPath = absolute
		if parent, err := filepath.EvalSymlinks(filepath.Dir(absolute)); err == nil {
			srtPath = filepath.Join(parent, filepath.Base(absolute))
		}
	}
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(subtitleFileLockKey(srtPath)))
	return int(hasher.Sum32() % uint32(len(subtitleFileMutationLocks)))
}

func lockSubtitleFile(srtPath string) func() {
	lock := &subtitleFileMutationLocks[subtitleFileLockBucket(srtPath)]
	lock.Lock()
	return lock.Unlock
}

// subtitleFileLockKey 把字幕路径规整成锁的键：Clean → Unicode NFC → 小写。
//
// 转小写（MEDIA-05）：macOS 默认文件系统大小写不敏感，Movie.mp4 与 movie.mkv 落到的是同一个
// movie.srt，大小写不同的两个键会让两路写入同时进行。口径与旁挂字幕判定（IsSidecarSubtitleName
// 按小写比较）一致。在大小写敏感的文件系统上，这只会让大小写不同的两个文件多串行一次，不影响正确性。
//
// 先做 NFC 再转小写（复审 B M-5）：APFS / HFS+ 不区分 Unicode 规范化形式，「é」写成一个码位
// （NFC，常见于手输与数据库里存的路径）还是「e + 组合重音」（NFD，访达与 HFS+ 返回的文件名）指的是
// 同一个文件，而两种写法的字节不同，不规整就会拿到两把锁。
func subtitleFileLockKey(srtPath string) string {
	return strings.ToLower(norm.NFC.String(filepath.Clean(srtPath)))
}

// tryLockConsolidationSubtitles 按实际桶去重，避免两个路径碰撞时自锁。
// 不等待字幕任务，因此不会在全局路径锁内等待字幕锁。
func tryLockConsolidationSubtitles(paths []string) (func(), error) {
	seen := make(map[int]bool)
	var buckets []int
	for _, path := range paths {
		bucket := subtitleFileLockBucket(path)
		if !seen[bucket] {
			seen[bucket] = true
			buckets = append(buckets, bucket)
		}
	}
	sort.Ints(buckets)
	var held []int
	release := func() {
		for i := len(held) - 1; i >= 0; i-- {
			subtitleFileMutationLocks[held[i]].Unlock()
		}
	}
	for _, bucket := range buckets {
		if !subtitleFileMutationLocks[bucket].TryLock() {
			release()
			return nil, fmt.Errorf("字幕正在写入，请完成后重新整理")
		}
		held = append(held, bucket)
	}
	return release, nil
}
