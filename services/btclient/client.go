// Package btclient contains bounded loopback clients for the user's local BT services.
package btclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrEndpoint       = errors.New("bt_endpoint_invalid: expected a loopback HTTP endpoint")
	ErrAuthentication = errors.New("bt_auth_failed: service authentication failed")
	ErrUnavailable    = errors.New("bt_service_unavailable: local service request failed")
	ErrResponse       = errors.New("bt_response_invalid: unexpected service response")
	ErrResponseSize   = errors.New("bt_response_too_large: service response exceeds limit")
)

// Client never follows redirects or inherits an environment HTTP proxy.
// Credentials and upstream response bodies are deliberately absent from errors.
type Client struct {
	base *url.URL
	http *http.Client
	key  string
}

// New constructs a client restricted to a literal loopback or localhost endpoint.
func New(endpoint, apiKey string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" || u.Opaque != "" {
		return nil, ErrEndpoint
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		host = "localhost"
	} else if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, ErrEndpoint
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, ErrEndpoint
		}
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	if strings.Contains(u.Path, "..") || u.RawPath != "" {
		return nil, ErrEndpoint
	}
	jar, _ := cookiejar.New(nil)
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		h, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrEndpoint
		}
		if strings.EqualFold(h, "localhost") {
			h = "127.0.0.1"
			_, port, _ := net.SplitHostPort(address)
			address = net.JoinHostPort(h, port)
		}
		ip := net.ParseIP(h)
		if ip == nil || !ip.IsLoopback() {
			return nil, ErrEndpoint
		}
		return dialer.DialContext(ctx, network, address)
	}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxConnsPerHost: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}
	return &Client{base: u, key: apiKey, http: &http.Client{Transport: transport, Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Close releases idle connections when a configuration generation is replaced.
func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) request(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string, limit int64) ([]byte, error) {
	return c.requestData(ctx, method, path, query, body, contentType, limit, false)
}

func (c *Client) requestData(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string, limit int64, magnet bool) ([]byte, error) {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.ContainsAny(path, "?#\\") || limit < 1 {
		return nil, ErrEndpoint
	}
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, ErrEndpoint
	}
	if c.key != "" {
		req.Header.Set("X-Api-Key", c.key)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, ErrAuthentication
	}
	if magnet && response.StatusCode >= 300 && response.StatusCode < 400 {
		location := response.Header.Get("Location")
		if validMagnet(location) {
			return []byte(location), nil
		}
		return nil, ErrResponse
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, ErrResponse
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, ErrUnavailable
	}
	if int64(len(data)) > limit {
		return nil, ErrResponseSize
	}
	return data, nil
}

// QBVersion reads protocol capabilities without modifying client state.
func (c *Client) QBVersion(ctx context.Context) (version, apiVersion string, err error) {
	v, err := c.request(ctx, http.MethodGet, "/api/v2/app/version", nil, nil, "", 128)
	if err != nil {
		return "", "", err
	}
	a, err := c.request(ctx, http.MethodGet, "/api/v2/app/webapiVersion", nil, nil, "", 128)
	if err != nil {
		return "", "", err
	}
	version, apiVersion = strings.TrimSpace(string(v)), strings.TrimSpace(string(a))
	if version == "" || apiVersion == "" || strings.ContainsAny(version+apiVersion, "<>\r\n") {
		return "", "", ErrResponse
	}
	return version, apiVersion, nil
}

// Login obtains an in-memory qBittorrent SID; passwords are never put in a URL.
func (c *Client) Login(ctx context.Context, username, password string) error {
	form := url.Values{"username": {username}, "password": {password}}
	data, err := c.request(ctx, http.MethodPost, "/api/v2/auth/login", nil, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", 128)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != "Ok." {
		return ErrAuthentication
	}
	return nil
}

// Indexer is the deliberately small Prowlarr projection exposed to the coordinator.
type Indexer struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Enable   bool   `json:"enable"`
}

// Indexers omits settings fields, which may contain tracker credentials.
func (c *Client) Indexers(ctx context.Context) ([]Indexer, error) {
	data, err := c.request(ctx, http.MethodGet, "/api/v1/indexer", nil, nil, "", 2<<20)
	if err != nil {
		return nil, err
	}
	var rows []Indexer
	if json.Unmarshal(data, &rows) != nil {
		return nil, ErrResponse
	}
	if rows == nil {
		rows = []Indexer{}
	}
	return rows, nil
}
