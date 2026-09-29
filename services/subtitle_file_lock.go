package services

import (
	"hash/fnv"
	"path/filepath"
	"strings"
	"sync"
)

var subtitleFileMutationLocks [64]sync.Mutex

// lockSubtitleFile 按规范化后的 .srt 路径加锁：movie.mp4 与 movie.mkv 共用 movie.srt，
// 必须互斥，所以锁的键是字幕文件而不是视频 ID。
// 调用方传 subtitleparser.SRTPathForVideo(videoPath) 的结果，这里再做 Clean 规整。
//
// 键统一转小写（MEDIA-05）：macOS 默认文件系统大小写不敏感，Movie.mp4 与 movie.mkv 落到的
// 是同一个 movie.srt，大小写不同的两个键会让两路写入同时进行。口径与旁挂字幕判定
// （IsSidecarSubtitleName 按小写比较）一致。在大小写敏感的文件系统上，这只会让大小写
// 不同的两个文件多串行一次，不影响正确性。
func lockSubtitleFile(srtPath string) func() {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(strings.ToLower(filepath.Clean(srtPath))))
	lock := &subtitleFileMutationLocks[hasher.Sum32()%uint32(len(subtitleFileMutationLocks))]
	lock.Lock()
	return lock.Unlock
}
