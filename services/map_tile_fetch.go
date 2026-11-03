package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func mapTileExpiration(now time.Time, headers http.Header) (time.Time, bool, bool) {
	expires := now.Add(7 * 24 * time.Hour)
	found := false
	must, noStore, noCache := false, false, false
	for _, part := range strings.Split(headers.Get("Cache-Control"), ",") {
		pair := strings.SplitN(strings.TrimSpace(strings.ToLower(part)), "=", 2)
		switch pair[0] {
		case "no-store":
			noStore = true
		case "no-cache":
			noCache = true
			expires = now
			found = true
			must = true
		case "must-revalidate":
			must = true
		case "max-age":
			if len(pair) == 2 {
				if seconds, err := strconv.ParseInt(strings.Trim(pair[1], `"`), 10, 64); err == nil && seconds >= 0 && seconds <= int64((1<<63-1)/int64(time.Second)) {
					age, _ := strconv.ParseInt(headers.Get("Age"), 10, 64)
					if date, err := http.ParseTime(headers.Get("Date")); err == nil {
						age = max(age, int64(now.Sub(date)/time.Second))
					}
					expires = now.Add(time.Duration(max(int64(0), seconds-max(int64(0), age))) * time.Second)
					found = true
				}
			}
		}
	}
	if !found {
		if value, err := http.ParseTime(headers.Get("Expires")); err == nil {
			expires = value
		}
	}
	if noCache {
		expires = now
	}
	return expires, must, noStore
}
func (s *MapTileService) fetch(ctx context.Context, generation, key string, z, x, y int) (*MapTileResult, error) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed || s.clearing || !s.enabled || s.generation != generation {
		s.mu.Unlock()
		return nil, ErrMapTileChanged
	}
	if s.blocked[key] {
		s.mu.Unlock()
		return nil, ErrMapCacheFull
	}
	entry, hasEntry := s.entries[key]
	if hasEntry && s.now().Before(entry.Expires) {
		body, err := s.readCacheLocked(entry)
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return &MapTileResult{Bytes: body, Expires: entry.Expires}, nil
	}
	// Reserve the bounded response plus metadata before requesting a new tile.
	reserve := int64(mapTileMaxBytes + 16384)
	if hasEntry {
		reserve = 0
	}
	if s.cacheBytes+s.reserved+reserve > s.budget {
		s.mu.Unlock()
		return nil, ErrMapCacheFull
	}
	s.reserved += reserve
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.reserved -= reserve; s.mu.Unlock() }()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://tile.openstreetmap.org/%d/%d/%d.png", z, x, y), nil)
	if err != nil {
		return nil, ErrMapTileInvalid
	}
	request.Header.Set("User-Agent", mapTileUserAgent)
	request.Header.Set("Accept", "image/png")
	if hasEntry {
		if entry.ETag != "" {
			request.Header.Set("If-None-Match", entry.ETag)
		}
		if entry.Modified != "" {
			request.Header.Set("If-Modified-Since", entry.Modified)
		}
	}
	response, err := s.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrMapTileUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotModified {
		return nil, ErrMapTileUnavailable
	}
	headers := response.Header.Clone()
	if response.StatusCode == http.StatusNotModified && hasEntry {
		if headers.Get("Cache-Control") == "" {
			headers.Set("Cache-Control", entry.CacheControl)
		}
		if headers.Get("Expires") == "" {
			headers.Set("Expires", entry.ExpiresHeader)
		}
	}
	if len(headers.Get("Cache-Control")) > 4096 || len(headers.Get("Expires")) > 128 {
		return nil, ErrMapTileUnavailable
	}
	expires, must, noStore := mapTileExpiration(s.now(), headers)
	var body []byte
	if response.StatusCode == http.StatusNotModified {
		if !hasEntry {
			return nil, ErrMapTileUnavailable
		}
		s.mu.Lock()
		body, err = s.readCacheLocked(entry)
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
	} else {
		if strings.TrimSpace(strings.SplitN(response.Header.Get("Content-Type"), ";", 2)[0]) != "image/png" {
			return nil, ErrMapTileUnavailable
		}
		body, err = io.ReadAll(io.LimitReader(response.Body, mapTileMaxBytes+1))
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrMapTileUnavailable
		}
		if !validMapTilePNG(body) {
			return nil, ErrMapTileUnavailable
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	next := mapTileCacheEntry{CacheControl: headers.Get("Cache-Control"), ExpiresHeader: headers.Get("Expires"), Key: key, Size: int64(len(body)), Expires: expires, ETag: entry.ETag, Modified: entry.Modified, MustRevalidate: must}
	if value := response.Header.Get("ETag"); len(value) <= 1024 && value != "" {
		next.ETag = value
	}
	if value := response.Header.Get("Last-Modified"); len(value) <= 1024 && value != "" {
		next.Modified = value
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.clearing || !s.enabled || s.generation != generation {
		return nil, ErrMapTileChanged
	}
	if !noStore {
		if err := s.storeCacheLocked(next, body); err != nil {
			if err == ErrMapCacheFull {
				s.blocked[key] = true
			}
			return nil, err
		}
	} else if hasEntry {
		if err := s.removeCacheTileLocked(key); err != nil {
			return nil, err
		}
	}
	return &MapTileResult{Bytes: body, Expires: expires}, nil
}
