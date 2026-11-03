package services

import (
	"bytes"
	"encoding/json"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var mapTileFilePattern = regexp.MustCompile(`^osm-[0-9]+-[0-9]+-[0-9]+\.(png|json)$`)

func (s *MapTileService) cacheDirectoryLocked(create bool) error {
	info, err := os.Lstat(s.directory)
	if os.IsNotExist(err) {
		if !create {
			return nil
		}
		if err := os.MkdirAll(s.directory, 0700); err != nil {
			return ErrMapCacheUnavailable
		}
		info, err = os.Lstat(s.directory)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrMapCacheUnavailable
	}
	return nil
}
func (s *MapTileService) loadCacheLocked() error {
	if s.cacheLoaded {
		return nil
	}
	if err := s.cacheDirectoryLocked(false); err != nil {
		return err
	}
	files, err := os.ReadDir(s.directory)
	if os.IsNotExist(err) {
		s.cacheLoaded = true
		return nil
	}
	if err != nil {
		return ErrMapCacheUnavailable
	}
	entries := map[string]mapTileCacheEntry{}
	var total int64
	for _, file := range files {
		if !mapTileFilePattern.MatchString(file.Name()) {
			continue
		}
		info, err := file.Info()
		if err != nil || !info.Mode().IsRegular() {
			return ErrMapCacheUnavailable
		}
		total += info.Size()
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		if info.Size() > 16384 {
			return ErrMapCacheUnavailable
		}
		raw, err := os.ReadFile(filepath.Join(s.directory, file.Name()))
		if err != nil {
			return ErrMapCacheUnavailable
		}
		var entry mapTileCacheEntry
		if json.Unmarshal(raw, &entry) != nil || file.Name() != entry.Key+".json" || entry.Size <= 0 || entry.Size > mapTileMaxBytes {
			return ErrMapCacheUnavailable
		}
		imageInfo, err := os.Lstat(filepath.Join(s.directory, entry.Key+".png"))
		if err != nil || !imageInfo.Mode().IsRegular() || imageInfo.Size() != entry.Size {
			return ErrMapCacheUnavailable
		}
		entries[entry.Key] = entry
	}
	s.entries = entries
	s.cacheBytes = total
	s.cacheLoaded = true
	return nil
}
func (s *MapTileService) readCacheLocked(entry mapTileCacheEntry) ([]byte, error) {
	if err := s.cacheDirectoryLocked(false); err != nil {
		return nil, err
	}
	path := filepath.Join(s.directory, entry.Key+".png")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > mapTileMaxBytes {
		return nil, ErrMapCacheUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrMapCacheUnavailable
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, mapTileMaxBytes+1))
	if err != nil || int64(len(body)) != entry.Size || !validMapTilePNG(body) {
		return nil, ErrMapCacheUnavailable
	}
	return body, nil
}
func validMapTilePNG(body []byte) bool {
	if len(body) > mapTileMaxBytes {
		return false
	}
	config, err := png.DecodeConfig(bytes.NewReader(body))
	return err == nil && config.Width == 256 && config.Height == 256
}
func (s *MapTileService) writeCacheFileLocked(name string, body []byte) error {
	file, err := os.CreateTemp(s.directory, ".tile-")
	if err != nil {
		return ErrMapCacheUnavailable
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err := file.Write(body); err != nil {
		file.Close()
		return ErrMapCacheUnavailable
	}
	if err := file.Close(); err != nil {
		return ErrMapCacheUnavailable
	}
	if err := os.Rename(temporary, filepath.Join(s.directory, name)); err != nil {
		return ErrMapCacheUnavailable
	}
	return nil
}
func (s *MapTileService) storeCacheLocked(entry mapTileCacheEntry, body []byte) error {
	if err := s.cacheDirectoryLocked(true); err != nil {
		return err
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return ErrMapCacheUnavailable
	}
	var oldBytes int64
	for _, suffix := range []string{".png", ".json"} {
		if info, err := os.Lstat(filepath.Join(s.directory, entry.Key+suffix)); err == nil {
			oldBytes += info.Size()
		} else if !os.IsNotExist(err) {
			return ErrMapCacheUnavailable
		}
	}
	next := s.cacheBytes - oldBytes + int64(len(body)+len(encoded))
	if next > s.budget {
		return ErrMapCacheFull
	}
	if err := s.writeCacheFileLocked(entry.Key+".png", body); err != nil {
		return err
	}
	if err := s.writeCacheFileLocked(entry.Key+".json", encoded); err != nil {
		s.cacheLoaded = false
		return err
	}
	s.entries[entry.Key] = entry
	s.cacheBytes = next
	return nil
}
func (s *MapTileService) clearCacheFilesLocked() error {
	if err := s.cacheDirectoryLocked(false); err != nil {
		return err
	}
	files, err := os.ReadDir(s.directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return ErrMapCacheUnavailable
	}
	for _, file := range files {
		if !mapTileFilePattern.MatchString(file.Name()) && !strings.HasPrefix(file.Name(), ".tile-") {
			continue
		}
		if file.IsDir() {
			return ErrMapCacheUnavailable
		}
		if err := os.Remove(filepath.Join(s.directory, file.Name())); err != nil {
			return ErrMapCacheUnavailable
		}
	}
	return nil
}

func (s *MapTileService) removeCacheTileLocked(key string) error {
	for _, suffix := range []string{".png", ".json"} {
		if err := os.Remove(filepath.Join(s.directory, key+suffix)); err != nil && !os.IsNotExist(err) {
			return ErrMapCacheUnavailable
		}
	}
	delete(s.entries, key)
	s.cacheLoaded = false
	return s.loadCacheLocked()
}
