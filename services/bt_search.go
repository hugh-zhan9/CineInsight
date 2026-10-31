package services

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"video-master/services/btclient"
)

var ErrBTResultExpired = errors.New("result_expired: 请重新搜索资源")
var ErrBTSearch = errors.New("search_failed: 资源搜索失败")

// BTSearchSource is implemented by btclient.Client; credentials remain below this boundary.
type BTSearchSource interface {
	Indexers(context.Context) ([]btclient.Indexer, error)
	Search(context.Context, string, int, int) ([]btclient.Release, error)
	Torrent(context.Context, string, int) ([]byte, error)
}
type BTResource struct {
	Token       string  `json:"result_token"`
	Title       string  `json:"title"`
	IndexerID   int     `json:"indexer_id"`
	Indexer     string  `json:"indexer"`
	Size        string  `json:"size"`
	Seeders     *int    `json:"seeders"`
	PublishTime *string `json:"publish_time"`
	PosterPath  string  `json:"poster_path"`
}
type BTIndexerSearchStatus struct {
	IndexerID int    `json:"indexer_id"`
	Status    string `json:"status"`
	Coverage  string `json:"coverage"`
}
type BTSearchResult struct {
	Resources []BTResource            `json:"resources"`
	Indexers  []BTIndexerSearchStatus `json:"indexers"`
	Truncated bool                    `json:"truncated"`
}
type btSearchToken struct {
	release    btclient.Release
	expires    time.Time
	generation uint64
	order      uint64
}

// BTSearchService owns a bounded, backend-only cache of sensitive proxy links.
type BTSearchService struct {
	mu          sync.Mutex
	source      BTSearchSource
	generation  uint64
	tokens      map[string]btSearchToken
	now         func() time.Time
	timeout     time.Duration
	sequence    uint64
	posterSlots chan struct{}
	fetchPoster func(context.Context, string) ([]byte, string, error)
}

func NewBTSearchService(source BTSearchSource, generation uint64) *BTSearchService {
	return &BTSearchService{source: source, generation: generation, tokens: map[string]btSearchToken{}, now: time.Now, timeout: 90 * time.Second, posterSlots: make(chan struct{}, 2), fetchPoster: defaultBTPoster}
}

// Configure invalidates links and in-flight results even when the numeric revision is reused.
func (s *BTSearchService) Configure(source BTSearchSource, generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.source = source
	s.generation = max(s.generation+1, generation)
	s.tokens = map[string]btSearchToken{}
}
func (s *BTSearchService) Search(ctx context.Context, query string, ids []int) (*BTSearchResult, error) {
	query = strings.TrimSpace(query)
	if len([]rune(query)) > 200 {
		return nil, ErrBTSearch
	}
	s.mu.Lock()
	source, generation := s.source, s.generation
	s.mu.Unlock()
	if source == nil {
		return nil, ErrBTSearch
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	indexers, err := source.Indexers(ctx)
	if err != nil {
		return nil, ErrBTSearch
	}
	allowed := map[int]btclient.Indexer{}
	for _, indexer := range indexers {
		if indexer.Enable && indexer.Protocol == "torrent" && indexer.ID > 0 {
			allowed[indexer.ID] = indexer
		}
	}
	requested := map[int]bool{}
	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			return nil, ErrBTSearch
		}
		requested[id] = true
	}
	if len(ids) == 0 {
		for id := range allowed {
			requested[id] = true
		}
	}
	ordered := []int{}
	for id := range requested {
		ordered = append(ordered, id)
	}
	sort.Ints(ordered)
	type result struct {
		rows   []btclient.Release
		status string
	}
	results := make([]result, len(ordered))
	for i := range results {
		results[i].status = "not_queried"
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < 2; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					continue
				}
				rows, err := source.Search(ctx, query, ordered[i], 500)
				status := "ok"
				if err != nil {
					status = "failed"
					if ctx.Err() != nil {
						status = "timeout"
					}
				}
				results[i] = result{rows: rows, status: status}
			}
		}()
	}
feed:
	for i := range ordered {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.generation {
		return nil, ErrBTResultExpired
	}
	now := s.now()
	for token, entry := range s.tokens {
		if !now.Before(entry.expires) {
			delete(s.tokens, token)
		}
	}
	out := &BTSearchResult{Resources: []BTResource{}, Indexers: []BTIndexerSearchStatus{}}
	rows := []btclient.Release{}
	for i, id := range ordered {
		r := results[i]
		// Prowlarr may suppress tracker failures, even with HTTP 200.
		out.Indexers = append(out.Indexers, BTIndexerSearchStatus{IndexerID: id, Status: r.status, Coverage: "unknown"})
		if r.status != "ok" {
			continue
		}
		if len(r.rows) >= 500 {
			out.Truncated = true
		}
		for _, row := range r.rows {
			if row.IndexerID != id || row.Protocol != "torrent" || row.DownloadURL == "" || row.Size < 0 {
				continue
			}
			row.Indexer = allowed[id].Name
			rows = append(rows, row)
		}
	}
	// Merge before applying the global cap, so a later station's newer rows survive.
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].PublishDate, rows[j].PublishDate
		if a == nil {
			return false
		}
		if b == nil {
			return true
		}
		at, _ := time.Parse(time.RFC3339, *a)
		bt, _ := time.Parse(time.RFC3339, *b)
		return at.After(bt)
	})
	if len(rows) > 500 {
		rows = rows[:500]
		out.Truncated = true
	}
	for _, row := range rows {
		if len(s.tokens) >= 500 {
			var oldest string
			var order uint64
			for key, entry := range s.tokens {
				if oldest == "" || entry.order < order {
					oldest, order = key, entry.order
				}
			}
			delete(s.tokens, oldest)
		}
		token, err := btSecretRef()
		if err != nil {
			return nil, ErrBTSearch
		}
		s.sequence++
		s.tokens[token] = btSearchToken{order: s.sequence, release: row, expires: now.Add(10 * time.Minute), generation: generation}
		resource := BTResource{Token: token, Title: row.Title, IndexerID: row.IndexerID, Indexer: row.Indexer, Size: strconv.FormatInt(row.Size, 10), Seeders: row.Seeders, PublishTime: row.PublishDate}
		if _, err := btPosterURL(row.PosterURL); err == nil {
			resource.PosterPath = "/preview/bt-poster/" + token
		}
		out.Resources = append(out.Resources, resource)
	}
	return out, nil
}

// Fetch resolves a token backend-side, fetching only its originally bound proxy.
// P002 persists its resulting draft; the same token remains resolvable until expiry.
func (s *BTSearchService) Fetch(ctx context.Context, token string) ([]byte, error) {
	s.mu.Lock()
	entry, ok := s.tokens[token]
	source, generation := s.source, s.generation
	if !ok || !s.now().Before(entry.expires) || entry.generation != generation {
		s.mu.Unlock()
		return nil, ErrBTResultExpired
	}
	s.mu.Unlock()
	data, err := source.Torrent(ctx, entry.release.DownloadURL, entry.release.IndexerID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.generation {
		return nil, ErrBTResultExpired
	}
	if err != nil {
		return nil, ErrBTSearch
	}
	return data, nil
}

// FetchProxy supports typed magnet responses without exposing sensitive URLs to callers.
func (s *BTSearchService) FetchProxy(ctx context.Context, token string) (btclient.ProxyResult, error) {
	s.mu.Lock()
	entry, ok := s.tokens[token]
	source, generation := s.source, s.generation
	if !ok || !s.now().Before(entry.expires) || entry.generation != generation {
		s.mu.Unlock()
		return btclient.ProxyResult{}, ErrBTResultExpired
	}
	s.mu.Unlock()
	typed, ok := source.(interface {
		FetchTorrent(context.Context, string, int) (btclient.ProxyResult, error)
	})
	if !ok {
		return btclient.ProxyResult{}, ErrBTSearch
	}
	result, err := typed.FetchTorrent(ctx, entry.release.DownloadURL, entry.release.IndexerID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.generation {
		return btclient.ProxyResult{}, ErrBTResultExpired
	}
	if err != nil {
		return btclient.ProxyResult{}, ErrBTSearch
	}
	return result, nil
}

// Describe returns the public resource snapshot bound to a token, never its URL.
func (s *BTSearchService) Describe(token string) (BTResource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tokens[token]
	if !ok || entry.generation != s.generation || !s.now().Before(entry.expires) {
		return BTResource{}, ErrBTResultExpired
	}
	row := entry.release
	return BTResource{Token: token, Title: row.Title, IndexerID: row.IndexerID, Indexer: row.Indexer, Size: strconv.FormatInt(row.Size, 10), Seeders: row.Seeders, PublishTime: row.PublishDate}, nil
}
