package services

import (
	"hash/fnv"
	"path/filepath"
	"sync"
)

var subtitleFileMutationLocks [64]sync.Mutex

// lockSubtitleFile 按规范化后的 .srt 路径加锁：movie.mp4 与 movie.mkv 共用 movie.srt，
// 必须互斥，所以锁的键是字幕文件而不是视频 ID。
// 调用方传 subtitleparser.SRTPathForVideo(videoPath) 的结果，这里再做 Clean 规整。
func lockSubtitleFile(srtPath string) func() {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(filepath.Clean(srtPath)))
	lock := &subtitleFileMutationLocks[hasher.Sum32()%uint32(len(subtitleFileMutationLocks))]
	lock.Lock()
	return lock.Unlock
}
