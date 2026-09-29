package services

import (
	"hash/fnv"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/text/unicode/norm"
)

var subtitleFileMutationLocks [64]sync.Mutex

// lockSubtitleFile 按规范化后的 .srt 路径加锁：movie.mp4 与 movie.mkv 共用 movie.srt，
// 必须互斥，所以锁的键是字幕文件而不是视频 ID。
// 调用方传 subtitleparser.SRTPathForVideo(videoPath) 的结果，键的规整见 subtitleFileLockKey。
func lockSubtitleFile(srtPath string) func() {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(subtitleFileLockKey(srtPath)))
	lock := &subtitleFileMutationLocks[hasher.Sum32()%uint32(len(subtitleFileMutationLocks))]
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
