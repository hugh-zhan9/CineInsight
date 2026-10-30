package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/services/btclient"
)

type btFakeSearch struct {
	mu          sync.Mutex
	active, max int
	gate        chan struct{}
	started     chan struct{}
}

func (f *btFakeSearch) Indexers(context.Context) ([]btclient.Indexer, error) {
	return []btclient.Indexer{{ID: 1, Enable: true, Protocol: "torrent", Name: "one"}, {ID: 2, Enable: true, Protocol: "torrent", Name: "two"}, {ID: 3, Enable: true, Protocol: "torrent", Name: "three"}}, nil
}
func (f *btFakeSearch) Search(ctx context.Context, q string, id, limit int) ([]btclient.Release, error) {
	f.mu.Lock()
	f.active++
	f.max = max(f.max, f.active)
	f.mu.Unlock()
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if id == 2 {
		return nil, errors.New("secret upstream error")
	}
	published := "2026-10-30T00:00:00Z"
	return []btclient.Release{{PublishDate: &published, Title: q, IndexerID: id, Protocol: "torrent", DownloadURL: "secret", Size: 2}}, nil
}
func (f *btFakeSearch) Torrent(context.Context, string, int) ([]byte, error) {
	return []byte("torrent"), nil
}
func TestBTSearchTokens(t *testing.T) {
	f := &btFakeSearch{}
	s := NewBTSearchService(f, 1)
	now := time.Now()
	s.now = func() time.Time { return now }
	out, err := s.Search(context.Background(), "film", nil)
	if err != nil || len(out.Resources) != 2 || len(out.Indexers) != 3 {
		t.Fatalf("%+v %v", out, err)
	}
	if out.Resources[0].Size != "2" {
		t.Fatal("size must be exact decimal text")
	}
	if out.Resources[0].PublishTime == nil || *out.Resources[0].PublishTime != "2026-10-30T00:00:00Z" {
		t.Fatal("publish_time omitted")
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("leak")
	}
	token := out.Resources[0].Token
	if _, err = s.Fetch(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	if _, err = s.Fetch(context.Background(), token); !errors.Is(err, ErrBTResultExpired) {
		t.Fatal(err)
	}
	s.Configure(f, 1)
	if _, err = s.Fetch(context.Background(), token); !errors.Is(err, ErrBTResultExpired) {
		t.Fatal(err)
	}
}
func TestBTSearchGenerationAndTimeout(t *testing.T) {
	f := &btFakeSearch{gate: make(chan struct{})}
	s := NewBTSearchService(f, 1)
	s.timeout = 20 * time.Millisecond
	out, err := s.Search(context.Background(), "film", nil)
	if err != nil || len(out.Resources) != 0 || f.max > 2 {
		t.Fatalf("%+v %v max%d", out, err, f.max)
	}
	s.timeout = time.Second
	f.started = make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { _, err := s.Search(context.Background(), "film", []int{1}); done <- err }()
	<-f.started
	s.Configure(f, 2)
	close(f.gate)
	if err := <-done; !errors.Is(err, ErrBTResultExpired) {
		t.Fatal(err)
	}
}

type btManyResults struct{ btFakeSearch }

func (f *btManyResults) Search(ctx context.Context, q string, id, limit int) ([]btclient.Release, error) {
	rows := make([]btclient.Release, 500)
	for i := range rows {
		rows[i] = btclient.Release{Title: q, IndexerID: id, Protocol: "torrent", DownloadURL: "secret"}
	}
	return rows, nil
}
func TestBTSearchCapacity(t *testing.T) {
	s := NewBTSearchService(&btManyResults{}, 1)
	now := time.Now()
	s.now = func() time.Time { return now }
	first, err := s.Search(context.Background(), "one", []int{1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Search(context.Background(), "two", []int{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.tokens) != 500 || !second.Truncated {
		t.Fatal("unbounded or missing truncation")
	}
	if _, err = s.Fetch(context.Background(), first.Resources[0].Token); !errors.Is(err, ErrBTResultExpired) {
		t.Fatal("old tokens not evicted")
	}
	for _, row := range second.Resources {
		if _, err = s.Fetch(context.Background(), row.Token); err != nil {
			t.Fatal("current result token evicted")
		}
	}
}
