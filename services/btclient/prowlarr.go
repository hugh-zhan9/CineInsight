package btclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Release is private integration data, not a Wails/UI DTO. DownloadURL can contain
// credentials and must be replaced by a short-lived token before presentation.
type Release struct {
	Title       string  `json:"title"`
	PublishDate *string `json:"publishDate"`
	IndexerID   int     `json:"indexerId"`
	Indexer     string  `json:"indexer"`
	Size        int64   `json:"size"`
	Seeders     *int    `json:"seeders"`
	GUID        string  `json:"guid"`
	DownloadURL string  `json:"downloadUrl"`
	Protocol    string  `json:"protocol"`
}

// Search asks one indexer; the coordinator owns fan-out and its overall deadline.
// An empty result does not assert that Prowlarr searched the tracker successfully.
func (c *Client) Search(ctx context.Context, query string, indexerID, limit int) ([]Release, error) {
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > 200 || indexerID < 1 || limit < 1 || limit > 500 {
		return nil, ErrResponse
	}
	params := url.Values{"query": {query}, "type": {"search"}, "indexerIds": {strconv.Itoa(indexerID)}, "limit": {strconv.Itoa(limit)}}
	data, err := c.request(ctx, http.MethodGet, "/api/v1/search", params, nil, "", 4<<20)
	if err != nil {
		return nil, err
	}
	var rows []Release
	if json.Unmarshal(data, &rows) != nil {
		return nil, ErrResponse
	}
	for i := range rows {
		row := &rows[i]
		if row.PublishDate != nil {
			if strings.TrimSpace(*row.PublishDate) == "" {
				row.PublishDate = nil
			} else {
				published, err := time.Parse(time.RFC3339Nano, *row.PublishDate)
				if err != nil {
					return nil, ErrResponse
				}
				normalized := published.UTC().Format(time.RFC3339Nano)
				row.PublishDate = &normalized
			}
		}
		if row.IndexerID != indexerID || row.Size < 0 {
			return nil, ErrResponse
		}
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	if rows == nil {
		rows = []Release{}
	}
	return rows, nil
}

// Torrent fetches only a search result's same-origin Prowlarr proxy. It never
// follows a redirect to the tracker, nor forwards an API key to another origin.
func (c *Client) Torrent(ctx context.Context, downloadURL string, indexerID int) ([]byte, error) {
	return c.proxyTorrent(ctx, downloadURL, indexerID, false)
}

func (c *Client) proxyTorrent(ctx context.Context, downloadURL string, indexerID int, magnet bool) ([]byte, error) {
	u, err := url.Parse(downloadURL)
	if err != nil || indexerID < 1 || u.User != nil || u.Fragment != "" || u.Scheme != c.base.Scheme || !strings.EqualFold(u.Host, c.base.Host) || u.RawPath != "" {
		return nil, ErrEndpoint
	}
	prefix := strings.TrimRight(c.base.Path, "/")
	path := strings.TrimPrefix(u.Path, prefix)
	if u.Path != prefix+path || (path != "/"+strconv.Itoa(indexerID)+"/download" && path != "/api/v1/indexer/"+strconv.Itoa(indexerID)+"/download") {
		return nil, ErrEndpoint
	}
	sourceQuery := u.Query()
	// Rebuild only the proxy parameters; credentials belong exclusively in headers.
	query := url.Values{"link": {sourceQuery.Get("link")}}
	if file := sourceQuery.Get("file"); file != "" {
		query.Set("file", file)
	}
	if query.Get("link") == "" {
		return nil, ErrEndpoint
	}
	data, err := c.requestData(ctx, http.MethodGet, path, query, nil, "", 16<<20, magnet)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, ErrResponse
	}
	return data, nil
}

// ProxyResult is backend-only integration data. Never expose Magnet to Wails.
type ProxyResult struct {
	Torrent []byte
	Magnet  string
}

func validMagnet(raw string) bool {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\r\n") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "magnet" && u.Host == "" && u.Query().Get("xt") != ""
}

// FetchTorrent distinguishes a magnet response without following it or contacting peers.
func (c *Client) FetchTorrent(ctx context.Context, downloadURL string, indexerID int) (ProxyResult, error) {
	data, err := c.proxyTorrent(ctx, downloadURL, indexerID, true)
	if err != nil {
		return ProxyResult{}, err
	}
	if strings.HasPrefix(string(data), "magnet:") {
		if !validMagnet(string(data)) {
			return ProxyResult{}, ErrResponse
		}
		return ProxyResult{Magnet: string(data)}, nil
	}
	return ProxyResult{Torrent: data}, nil
}

// ProwlarrVersion projects only version from GET /api/v1/system/status. That
// response also contains private filesystem/configuration details, never exposed.
func (c *Client) ProwlarrVersion(ctx context.Context) (string, error) {
	data, err := c.request(ctx, http.MethodGet, "/api/v1/system/status", nil, nil, "", 64<<10)
	if err != nil {
		return "", err
	}
	var status struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &status) != nil || status.Version == "" || len(status.Version) > 128 || strings.ContainsAny(status.Version, "\r\n<>") {
		return "", ErrResponse
	}
	return status.Version, nil
}
