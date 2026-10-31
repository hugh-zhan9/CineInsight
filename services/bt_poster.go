package services

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var errBTPoster = errors.New("poster_unavailable: 资源封面暂不可用")

const btPosterMaxBytes = 4 << 20

func btPosterURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || len(raw) > 8192 {
		return nil, errBTPoster
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errBTPoster
	}
	if port := u.Port(); port != "" && !((u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80")) {
		return nil, errBTPoster
	}
	return u, nil
}

// publicBTPosterIP excludes local and special-purpose networks, including mapped IPv4.
func publicBTPosterIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	// Public IPv6 destinations must be in the global unicast allocation, not
	// merely pass netip's broad definition (which includes discard/translation space).
	if ip.Is6() && (!netip.MustParsePrefix("2000::/3").Contains(ip) || netip.MustParsePrefix("3fff::/20").Contains(ip)) {
		return false
	}
	for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/23", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48"} {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return false
		}
	}
	return true
}

type btPosterLookup func(context.Context, string) ([]netip.Addr, error)
type btPosterClient func() (*http.Client, error)

// fetchBTPoster pins the destination before passing it to a direct dialer or proxy.
// The original hostname remains the HTTP Host and TLS certificate identity.
func fetchBTPoster(ctx context.Context, raw string, lookup btPosterLookup, factory btPosterClient) ([]byte, string, error) {
	u, err := btPosterURL(raw)
	if err != nil {
		return nil, "", errBTPoster
	}
	host := u.Hostname()
	ips, err := lookup(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, "", errBTPoster
	}
	for _, ip := range ips {
		if !publicBTPosterIP(ip) {
			return nil, "", errBTPoster
		}
	}
	client, err := factory()
	if err != nil {
		return nil, "", errBTPoster
	}
	base, ok := client.Transport.(*http.Transport)
	if !ok {
		return nil, "", errBTPoster
	}
	defer base.CloseIdleConnections()
	// Go's HTTP proxy serializer uses Request.Host in the absolute request URI.
	// HTTPS CONNECT can pin the IP independently; plain HTTP cannot. Do not
	// silently bypass a configured proxy for an HTTP-only cover.
	if u.Scheme == "http" && base.Proxy != nil {
		return nil, "", errBTPoster
	}
	transport := base.Clone()
	defer transport.CloseIdleConnections()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.ServerName = host
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	transport.TLSClientConfig.InsecureSkipVerify = false
	transport.MaxConnsPerHost = 2
	transport.ResponseHeaderTimeout = 10 * time.Second
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	originalHost := u.Host
	u.Host = net.JoinHostPort(ips[0].String(), port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", errBTPoster
	}
	req.Host = originalHost
	req.Header.Set("User-Agent", watchlistPosterUserAgent)
	req.Header.Set("Accept", "image/webp,image/png,image/jpeg")
	req.Header.Set("Referer", u.Scheme+"://"+originalHost+"/")
	client.Transport = transport
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, "", errBTPoster
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", errBTPoster
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, btPosterMaxBytes+1))
	if err != nil || len(data) == 0 || len(data) > btPosterMaxBytes {
		return nil, "", errBTPoster
	}
	if _, err = managedImageExtension(data); err != nil {
		return nil, "", errBTPoster
	}
	return data, http.DetectContentType(data), nil
}

// Poster accepts only a live search token, never a caller-supplied URL.
func (s *BTSearchService) Poster(ctx context.Context, token string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case s.posterSlots <- struct{}{}:
		defer func() { <-s.posterSlots }()
	case <-ctx.Done():
		return nil, "", errBTPoster
	}
	s.mu.Lock()
	entry, ok := s.tokens[token]
	generation := s.generation
	valid := ok && entry.generation == generation && s.now().Before(entry.expires)
	s.mu.Unlock()
	if !valid {
		return nil, "", ErrBTResultExpired
	}
	data, mime, err := s.fetchPoster(ctx, entry.release.PosterURL)
	s.mu.Lock()
	defer s.mu.Unlock()
	live, exists := s.tokens[token]
	if generation != s.generation || !exists || live.order != entry.order || !s.now().Before(entry.expires) {
		return nil, "", ErrBTResultExpired
	}
	return data, mime, err
}

func defaultBTPoster(ctx context.Context, address string) ([]byte, string, error) {
	return fetchBTPoster(ctx, address, func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}, func() (*http.Client, error) {
		return NewWatchlistPosterHTTPClient(LoadWatchlistMetadataConfig())
	})
}
