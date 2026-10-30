package services

import (
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"video-master/models"
	"video-master/services/btmanifest"
)

type btCacheFiles struct {
	root   string
	taskID string
}

func btCacheDigest(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func (s *btCacheFiles) cacheDir(id string) string { return filepath.Join(s.root, id) }
func (s *btCacheFiles) cachePath(id, digest string) string {
	return filepath.Join(s.cacheDir(id), digest+".torrent")
}
func (s *btCacheFiles) cacheDirectory(id string, create bool) error {
	if !btTaskUUID(id) || id != s.taskID {
		return ErrBTPath
	}
	base := s.root
	if create {
		if os.MkdirAll(base, 0700) != nil {
			return ErrBTTask
		}
	}
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrBTPath
	}
	if create {
		if err = os.Mkdir(s.cacheDir(id), 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return ErrBTTask
		}
	}
	info, err = os.Lstat(s.cacheDir(id))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrBTPath
	}
	return nil
}

// Each task owns a private directory of immutable, complete digest versions.
// Publishing a new version never overwrites the cache an older DB snapshot uses.
func (s *btCacheFiles) writeCache(id string, data []byte) error {
	if len(data) == 0 || len(data) > btmanifest.MaxBytes {
		return ErrBTTask
	}
	if err := s.cacheDirectory(id, true); err != nil {
		return err
	}
	digest := btDigest(data)
	path := s.cachePath(id, digest)
	f, err := os.CreateTemp(s.cacheDir(id), ".preview-*")
	if err != nil {
		return ErrBTTask
	}
	defer func() { f.Close(); _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err != nil {
		return ErrBTTask
	}
	if f.Sync() != nil || f.Close() != nil {
		return ErrBTTask
	}
	if err = os.Link(f.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return ErrBTTask
		}
		if _, e := s.readCache(models.BTTask{ID: id, ManifestDigest: digest}); e != nil {
			return e
		}
	}
	dir, err := os.Open(s.cacheDir(id))
	if err != nil {
		return ErrBTTask
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrBTTask
	}
	for _, parent := range []string{s.root, filepath.Dir(s.root), filepath.Dir(filepath.Dir(s.root))} {
		fd, e := os.Open(parent)
		if e != nil {
			return ErrBTTask
		}
		e = fd.Sync()
		fd.Close()
		if e != nil {
			return ErrBTTask
		}
	}
	return nil
}
func (s *btCacheFiles) readCache(task models.BTTask) ([]byte, error) {
	if !btCacheDigest(task.ManifestDigest) || s.cacheDirectory(task.ID, false) != nil {
		return nil, ErrBTResultExpired
	}
	path := s.cachePath(task.ID, task.ManifestDigest)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > btmanifest.MaxBytes {
		return nil, ErrBTResultExpired
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrBTResultExpired
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, btmanifest.MaxBytes+1))
	if err != nil || len(data) > btmanifest.MaxBytes || btDigest(data) != task.ManifestDigest {
		return nil, ErrBTResultExpired
	}
	return data, nil
}
func (s *btCacheFiles) cacheEntries(id string) ([]os.DirEntry, error) {
	if err := s.cacheDirectory(id, false); err != nil {
		return nil, err
	}
	dir, err := os.Open(s.cacheDir(id))
	if err != nil {
		return nil, ErrBTTask
	}
	defer dir.Close()
	entries, err := dir.ReadDir(200)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, ErrBTTask
	}
	return entries, nil
}
func btVersionName(name string) (string, bool) {
	if !strings.HasSuffix(name, ".torrent") {
		return "", false
	}
	digest := strings.TrimSuffix(name, ".torrent")
	return digest, btCacheDigest(digest)
}
func btCacheTemp(name string) bool {
	if !strings.HasPrefix(name, ".preview-") {
		return false
	}
	tail := strings.TrimPrefix(name, ".preview-")
	if len(tail) < 1 || len(tail) > 10 {
		return false
	}
	for _, c := range tail {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func (s *btCacheFiles) removeCacheVersion(id, digest string) error {
	if !btCacheDigest(digest) || s.cacheDirectory(id, false) != nil {
		return ErrBTPath
	}
	if err := os.Remove(s.cachePath(id, digest)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrBTTask
	}
	return nil
}
func (s *btCacheFiles) removeTaskCaches(id string) error {
	entries, err := s.cacheEntries(id)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		digest, ok := btVersionName(entry.Name())
		if !ok {
			continue
		}
		if err = s.removeCacheVersion(id, digest); err != nil {
			return err
		}
	}
	// Unknown files are preserved; removal succeeds only for an empty task dir.
	_ = os.Remove(s.cacheDir(id))
	return nil
}
func btMagnetMatchesTask(raw []byte, t models.BTTask) bool {
	v1, v2, err := btMagnetHashes(string(raw))
	if err != nil {
		return false
	}
	return (v1 == "" || t.InfoHashV1 != nil && *t.InfoHashV1 == v1) && (v2 == "" || t.InfoHashV2 != nil && *t.InfoHashV2 == v2)
}

// An unresolved metadata operation can still need the original magnet after a
// complete exported manifest became the DB pointer. Only hash-verified private
// versions are considered; no URL is reconstructed from a file list.
func (s *btCacheFiles) readMetadataCache(task models.BTTask) ([]byte, error) {
	readMagnet := func(version models.BTTask) ([]byte, error) {
		if !btTaskUUID(version.ID) || !btCacheDigest(version.ManifestDigest) {
			return nil, ErrBTResultExpired
		}
		info, e := os.Lstat(s.cachePath(version.ID, version.ManifestDigest))
		if e != nil || info.Size() > 8192 {
			return nil, ErrBTResultExpired
		}
		return s.readCache(version)
	}
	if data, err := readMagnet(task); err == nil && btMagnetMatchesTask(data, task) {
		return data, nil
	}
	entries, err := s.cacheEntries(task.ID)
	if err != nil {
		return nil, ErrBTResultExpired
	}
	for _, entry := range entries {
		digest, ok := btVersionName(entry.Name())
		if !ok {
			continue
		}
		version := task
		version.ManifestDigest = digest
		if data, e := readMagnet(version); e == nil && btMagnetMatchesTask(data, task) {
			return data, nil
		}
	}
	return nil, ErrBTResultExpired
}
