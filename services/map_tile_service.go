package services

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"net/http"
	"path/filepath"
	"sync"
	"time"
	"video-master/database"
)

const mapTileMaxBytes = 1024 * 1024
const mapTileCacheBudget = 256 * 1024 * 1024
const mapTileUserAgent = "CineInsight-PhotoMap/1"

type MapTileStatus struct {
	CacheError  string `json:"cache_error"`
	Enabled     bool   `json:"enabled"`
	Provider    string `json:"provider"`
	Generation  string `json:"generation"`
	CacheBytes  int64  `json:"cache_bytes"`
	BudgetBytes int64  `json:"budget_bytes"`
	CacheFull   bool   `json:"cache_full"`
}
type MapTilePolicyRequest struct {
	Enabled    bool   `json:"enabled"`
	Confirmed  bool   `json:"confirmed"`
	Generation string `json:"generation"`
}
type MapTileResult struct {
	Bytes   []byte
	Expires time.Time
	Offline bool
}
type mapTileCacheEntry struct {
	CacheControl   string    `json:"cache_control"`
	ExpiresHeader  string    `json:"expires_header"`
	Key            string    `json:"key"`
	Size           int64     `json:"size"`
	Expires        time.Time `json:"expires"`
	ETag           string    `json:"etag"`
	Modified       string    `json:"modified"`
	MustRevalidate bool      `json:"must_revalidate"`
}
type mapTileFlight struct {
	done   chan struct{}
	result *MapTileResult
	err    error
}
type MapTileService struct {
	mu                                             sync.Mutex
	store                                          *SettingsService
	directory                                      string
	client                                         *http.Client
	now                                            func() time.Time
	timeout                                        time.Duration
	budget, cacheBytes, reserved                   int64
	entries                                        map[string]mapTileCacheEntry
	blocked                                        map[string]bool
	flights                                        map[string]*mapTileFlight
	cacheLoaded, loaded, enabled, closed, clearing bool
	db                                             *gorm.DB
	generation                                     string
	grant                                          context.Context
	cancel                                         context.CancelFunc
	slots                                          chan struct{}
	workers                                        sync.WaitGroup
}

func NewMapTileService(dataDir string, store *SettingsService) *MapTileService {
	return &MapTileService{store: store, directory: filepath.Join(dataDir, "map-tiles"), client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, now: time.Now, timeout: 10 * time.Second, budget: mapTileCacheBudget, entries: map[string]mapTileCacheEntry{}, flights: map[string]*mapTileFlight{}, blocked: map[string]bool{}, slots: make(chan struct{}, 2)}
}
func (s *MapTileService) rotateLocked() {
	if s.cancel != nil {
		s.cancel()
	}
	s.generation = uuid.NewString()
	s.grant, s.cancel = context.WithCancel(context.Background())
}
func (s *MapTileService) refreshLocked(ctx context.Context) error {
	if s.closed || s.clearing {
		return ErrMapTileChanged
	}
	enabled, err := s.store.MapTilePolicy(ctx)
	if err != nil {
		if s.cancel != nil {
			s.cancel()
		}
		s.loaded = false
		return err
	}
	if !s.loaded || s.db != database.DB || s.enabled != enabled {
		s.rotateLocked()
		s.enabled = enabled
		s.db = database.DB
		s.loaded = true
	}
	return nil
}
func (s *MapTileService) cacheStatusLocked() *MapTileStatus {
	status := s.statusLocked()
	if err := s.loadCacheLocked(); err != nil {
		status.CacheError = "map_cache_unavailable"
	} else {
		status = s.statusLocked()
	}
	return status
}
func (s *MapTileService) statusLocked() *MapTileStatus {
	return &MapTileStatus{Enabled: s.enabled, Provider: "osm", Generation: s.generation, CacheBytes: s.cacheBytes, BudgetBytes: s.budget, CacheFull: s.cacheBytes+s.reserved+mapTileMaxBytes+16384 > s.budget}
}
func (s *MapTileService) Status(ctx context.Context) (*MapTileStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshLocked(ctx); err != nil {
		return nil, err
	}
	return s.cacheStatusLocked(), nil
}
func (s *MapTileService) SetEnabled(ctx context.Context, request MapTilePolicyRequest) (*MapTileStatus, error) {
	if request.Enabled && !request.Confirmed {
		return nil, ErrMapTileInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshLocked(ctx); err != nil {
		return nil, err
	}
	if request.Generation != s.generation {
		return nil, ErrMapTileChanged
	}
	if err := s.store.SetMapTilePolicy(ctx, s.enabled, request.Enabled); err != nil {
		return nil, err
	}
	if s.enabled != request.Enabled {
		s.enabled = request.Enabled
		s.rotateLocked()
	}
	return s.cacheStatusLocked(), nil
}
func mapTileKey(provider string, z, x, y int) (string, error) {
	if provider != "osm" || z < 0 || z > 19 || x < 0 || y < 0 || x >= 1<<z || y >= 1<<z {
		return "", ErrMapTileInvalid
	}
	return fmt.Sprintf("osm-%d-%d-%d", z, x, y), nil
}
func (s *MapTileService) Get(owner context.Context, provider string, z, x, y int, generation string) (*MapTileResult, error) {
	key, err := mapTileKey(provider, z, x, y)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(owner, s.timeout)
	defer cancel()
	s.mu.Lock()
	if err := s.refreshLocked(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if generation != s.generation {
		s.mu.Unlock()
		return nil, ErrMapTileChanged
	}
	if err := s.loadCacheLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if entry, ok := s.entries[key]; ok && (s.now().Before(entry.Expires) || !s.enabled && !entry.MustRevalidate) {
		body, err := s.readCacheLocked(entry)
		offline := !s.enabled
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return &MapTileResult{Bytes: body, Expires: entry.Expires, Offline: offline}, nil
	}
	if !s.enabled {
		s.mu.Unlock()
		return nil, ErrMapTilesDisabled
	}
	flightKey := generation + ":" + key
	flight := s.flights[flightKey]
	if flight == nil {
		flight = &mapTileFlight{done: make(chan struct{})}
		s.flights[flightKey] = flight
		s.workers.Add(1)
		grant := s.grant
		go s.fetchFlight(ctx, grant, generation, flightKey, key, z, x, y, flight)
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		if flight.err != nil {
			return nil, flight.err
		}
		copy := *flight.result
		copy.Bytes = append([]byte{}, copy.Bytes...)
		return &copy, nil
	}
}
func (s *MapTileService) fetchFlight(owner, grant context.Context, generation, flightKey, key string, z, x, y int, flight *mapTileFlight) {
	defer s.workers.Done()
	ctx, cancel := context.WithCancel(owner)
	stop := context.AfterFunc(grant, cancel)
	defer stop()
	defer cancel()
	result, err := s.fetch(ctx, generation, key, z, x, y)
	s.mu.Lock()
	flight.result, flight.err = result, err
	delete(s.flights, flightKey)
	close(flight.done)
	s.mu.Unlock()
}
func (s *MapTileService) StopAndWait() {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.workers.Wait()
}
func (s *MapTileService) Resume() { s.mu.Lock(); s.closed = false; s.loaded = false; s.mu.Unlock() }
func (s *MapTileService) ClearCache(ctx context.Context) (*MapTileStatus, error) {
	s.mu.Lock()
	if err := s.refreshLocked(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.clearing = true
	s.rotateLocked()
	s.mu.Unlock()
	s.workers.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() { s.clearing = false }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.clearCacheFilesLocked(); err != nil {
		return nil, err
	}
	s.entries = map[string]mapTileCacheEntry{}
	s.cacheBytes = 0
	s.blocked = map[string]bool{}
	s.cacheLoaded = true
	return s.cacheStatusLocked(), nil
}
