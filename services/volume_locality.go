package services

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// externalVolumeRevalidateInterval limits how frequently background SWR
	// revalidation checks a NAS/HDD source file once a local SSD thumbnail exists.
	externalVolumeRevalidateInterval = 10 * time.Minute
	externalVolumeRevalidateTimeout  = 4 * time.Second
	volumeLocalityCacheTTL           = 5 * time.Minute
)

var isExternalOrRemotePathFunc = defaultIsExternalOrRemotePath

type volumeLocalityEntry struct {
	external  bool
	checkedAt time.Time
}

var (
	volumeLocalityMu    sync.RWMutex
	volumeLocalityCache = map[string]volumeLocalityEntry{}
)

// IsExternalOrRemotePath reports whether path lives on a mounted external drive
// or network filesystem (NAS / SMB / NFS / AFP / external HDD) where synchronous
// per-request os.Stat calls on warm thumbnail cache hits cause severe latency.
func IsExternalOrRemotePath(path string) bool {
	return isExternalOrRemotePathFunc(path)
}

func defaultIsExternalOrRemotePath(path string) bool {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." {
		return false
	}
	if isObviouslyExternalPath(cleaned) {
		return true
	}
	key := volumeLocalityCacheKey(cleaned)
	now := time.Now()
	volumeLocalityMu.RLock()
	entry, ok := volumeLocalityCache[key]
	volumeLocalityMu.RUnlock()
	if ok && now.Sub(entry.checkedAt) < volumeLocalityCacheTTL {
		return entry.external
	}
	external := probeVolumeLocality(cleaned)
	volumeLocalityMu.Lock()
	if len(volumeLocalityCache) > 256 {
		volumeLocalityCache = map[string]volumeLocalityEntry{}
	}
	volumeLocalityCache[key] = volumeLocalityEntry{external: external, checkedAt: now}
	volumeLocalityMu.Unlock()
	return external
}

func volumeLocalityCacheKey(cleaned string) string {
	dir := filepath.Dir(cleaned)
	if dir == "" {
		return cleaned
	}
	parts := strings.Split(strings.TrimPrefix(dir, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) >= 2 {
		return string(filepath.Separator) + filepath.Join(parts[0], parts[1])
	}
	return dir
}
