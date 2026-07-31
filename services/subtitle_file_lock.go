package services

import "sync"

var subtitleFileMutationLocks [64]sync.Mutex

func lockSubtitleFile(videoID uint) func() {
	lock := &subtitleFileMutationLocks[videoID%uint(len(subtitleFileMutationLocks))]
	lock.Lock()
	return lock.Unlock
}
