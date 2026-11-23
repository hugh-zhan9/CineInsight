package main

import (
	"bytes"
	"container/list"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	defaultHotAssetCacheBytes = 64 << 20 // 64 MiB hot thumbnail bytes in RAM
	maxHotAssetEntryBytes     = 512 << 10
)

type hotAssetKey struct {
	path      string
	modTimeNS int64
}

type hotAssetEntry struct {
	key     hotAssetKey
	data    []byte
	modTime time.Time
}

// hotAssetByteCache keeps recently served thumbnail bytes in memory so WKWebView
// custom-protocol reloads (which ignore HTTP Cache-Control) hit RAM instead of
// repeatedly opening and reading files on every virtual-list scroll.
type hotAssetByteCache struct {
	mu       sync.Mutex
	maxBytes int64
	curBytes int64
	lru      *list.List
	items    map[hotAssetKey]*list.Element
}

func newHotAssetByteCache(maxBytes int64) *hotAssetByteCache {
	if maxBytes <= 0 {
		maxBytes = defaultHotAssetCacheBytes
	}
	return &hotAssetByteCache{
		maxBytes: maxBytes,
		lru:      list.New(),
		items:    make(map[hotAssetKey]*list.Element),
	}
}

func (c *hotAssetByteCache) Get(path string, modTime time.Time) ([]byte, bool) {
	if c == nil || path == "" {
		return nil, false
	}
	key := hotAssetKey{path: path, modTimeNS: modTime.UnixNano()}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(elem)
	return elem.Value.(*hotAssetEntry).data, true
}

func (c *hotAssetByteCache) Put(path string, modTime time.Time, data []byte) {
	if c == nil || path == "" || len(data) == 0 || int64(len(data)) > maxHotAssetEntryBytes || int64(len(data)) > c.maxBytes {
		return
	}
	key := hotAssetKey{path: path, modTimeNS: modTime.UnixNano()}
	copied := make([]byte, len(data))
	copy(copied, data)

	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*hotAssetEntry)
		c.curBytes += int64(len(copied)) - int64(len(entry.data))
		entry.data = copied
		entry.modTime = modTime
		c.lru.MoveToFront(elem)
	} else {
		elem := c.lru.PushFront(&hotAssetEntry{key: key, data: copied, modTime: modTime})
		c.items[key] = elem
		c.curBytes += int64(len(copied))
	}
	for c.curBytes > c.maxBytes {
		back := c.lru.Back()
		if back == nil {
			break
		}
		entry := back.Value.(*hotAssetEntry)
		c.lru.Remove(back)
		delete(c.items, entry.key)
		c.curBytes -= int64(len(entry.data))
	}
}

func (c *hotAssetByteCache) serveFile(w http.ResponseWriter, r *http.Request, path, name string, modTime time.Time, contentType, cacheControl string) error {
	if data, ok := c.Get(path, modTime); ok {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if cacheControl != "" {
			w.Header().Set("Cache-Control", cacheControl)
		}
		http.ServeContent(w, r, name, modTime, bytes.NewReader(data))
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, statErr := file.Stat()
	if statErr == nil && info.Size() > 0 && info.Size() <= maxHotAssetEntryBytes {
		data, readErr := io.ReadAll(io.LimitReader(file, maxHotAssetEntryBytes+1))
		if readErr == nil && len(data) > 0 && int64(len(data)) <= maxHotAssetEntryBytes {
			c.Put(path, modTime, data)
			if contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
			if cacheControl != "" {
				w.Header().Set("Cache-Control", cacheControl)
			}
			http.ServeContent(w, r, name, modTime, bytes.NewReader(data))
			return nil
		}
		_, _ = file.Seek(0, io.SeekStart)
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if cacheControl != "" {
		w.Header().Set("Cache-Control", cacheControl)
	}
	http.ServeContent(w, r, name, modTime, file)
	return nil
}
