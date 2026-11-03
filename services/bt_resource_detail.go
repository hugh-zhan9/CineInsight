package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
	"video-master/services/btclient"
)

var ErrBTDetail = errors.New("detail_unavailable: 暂时无法读取站点详情")

type BTResourceDetail struct {
	Description string   `json:"description"`
	MediaInfo   string   `json:"media_info"`
	ImagePaths  []string `json:"image_paths"`
}

type btSiteDetailSource interface {
	SiteDetail(context.Context, int, string) (btclient.SiteDetailRequest, error)
}

func (s *BTSearchService) ResourceDetail(ctx context.Context, token string) (*BTResourceDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case s.posterSlots <- struct{}{}:
		defer func() { <-s.posterSlots }()
	case <-ctx.Done():
		return nil, ErrBTDetail
	}
	s.mu.Lock()
	entry, ok := s.tokens[token]
	generation := s.generation
	source := s.source
	if ok && entry.generation == generation && s.now().Before(entry.expires) {
		entry.detailOrder++
		s.tokens[token] = entry
	}
	s.mu.Unlock()
	if !ok || entry.generation != generation || !s.now().Before(entry.expires) {
		return nil, ErrBTResultExpired
	}
	provider, ok := source.(btSiteDetailSource)
	if !ok {
		return nil, btclient.ErrSiteUnsupported
	}
	request, err := provider.SiteDetail(ctx, entry.release.IndexerID, entry.release.InfoURL)
	if err != nil {
		return nil, err
	}
	data, err := s.fetchDetail(ctx, request)
	if err != nil {
		if errors.Is(err, btclient.ErrAuthentication) || errors.Is(err, errBTUnsafeResolution) {
			return nil, err
		}
		return nil, ErrBTDetail
	}
	detail, images, err := parseBTResourceDetail(data, request.Format, request.URL)
	if err != nil {
		return nil, err
	}
	// A malformed upstream body must not echo tracker credentials into the UI.
	for _, key := range []string{"Authorization", "Cookie"} {
		values := request.Headers.Values(key)
		for _, value := range values {
			secret := strings.TrimPrefix(value, "Bearer ")
			if secret != "" {
				detail.Description = strings.ReplaceAll(detail.Description, secret, "[已隐藏]")
				detail.MediaInfo = strings.ReplaceAll(detail.MediaInfo, secret, "[已隐藏]")
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	live, exists := s.tokens[token]
	if !exists || generation != s.generation || live.order != entry.order || live.detailOrder != entry.detailOrder || !s.now().Before(live.expires) || ctx.Err() != nil {
		return nil, ErrBTResultExpired
	}
	detail.ImagePaths = []string{}
	for index := range images {
		detail.ImagePaths = append(detail.ImagePaths, fmt.Sprintf("/preview/bt-detail-image/%s/%d/%d", token, entry.detailOrder, index))
	}
	live.images = images
	s.tokens[token] = live
	return detail, nil
}

func defaultBTDetail(ctx context.Context, request btclient.SiteDetailRequest) ([]byte, error) {
	data, _, err := fetchBTPublic(ctx, request.URL, request.Headers, 2<<20, btDetailLookup, btDetailClient)
	return data, err
}

func btDetailLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}
func btDetailClient() (*http.Client, error) {
	return NewWatchlistPosterHTTPClient(LoadWatchlistMetadataConfig())
}

func (s *BTSearchService) DetailImage(ctx context.Context, token string, detailOrder uint64, index int) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case s.posterSlots <- struct{}{}:
		defer func() { <-s.posterSlots }()
	case <-ctx.Done():
		return nil, "", ErrBTDetail
	}
	s.mu.Lock()
	entry, ok := s.tokens[token]
	generation := s.generation
	s.mu.Unlock()
	if !ok || entry.generation != generation || !s.now().Before(entry.expires) || entry.detailOrder != detailOrder || index < 0 || index >= len(entry.images) {
		return nil, "", ErrBTResultExpired
	}
	data, mime, err := s.fetchPoster(ctx, entry.images[index])
	s.mu.Lock()
	defer s.mu.Unlock()
	live, exists := s.tokens[token]
	if !exists || generation != s.generation || live.order != entry.order || live.detailOrder != detailOrder || !s.now().Before(live.expires) {
		return nil, "", ErrBTResultExpired
	}
	return data, mime, err
}

var btBBImage = regexp.MustCompile(`(?is)\[img(?:=[^\]]*)?\]\s*(https?://[^\[\]\s]+)\s*\[/img\]`)
var btBBTag = regexp.MustCompile(`(?i)\[/?(?:b|i|u|s|color|size|font|align|center|left|right|quote|code|pre|url|img|spoiler|hide|list|\*)(?:=[^\]]*)?\]`)

func parseBTResourceDetail(data []byte, format, baseURL string) (*BTResourceDetail, []string, error) {
	description, media := "", ""
	if format == "json" {
		var payload struct {
			Ret  *int `json:"ret"`
			Data struct {
				Data struct {
					ID    json.RawMessage `json:"id"`
					Extra *struct {
						Descr     *string `json:"descr"`
						MediaInfo string  `json:"media_info"`
					} `json:"extra"`
				} `json:"data"`
			} `json:"data"`
		}
		if json.Unmarshal(data, &payload) != nil || payload.Ret == nil || *payload.Ret != 0 || len(payload.Data.Data.ID) == 0 || payload.Data.Data.Extra == nil || payload.Data.Data.Extra.Descr == nil {
			return nil, nil, ErrBTDetail
		}
		requested, _ := url.Parse(baseURL)
		id := strings.Trim(string(payload.Data.Data.ID), "\"")
		if id != requested.Path[strings.LastIndex(requested.Path, "/")+1:] {
			return nil, nil, ErrBTDetail
		}
		description = *payload.Data.Data.Extra.Descr
		media = payload.Data.Data.Extra.MediaInfo
	} else {
		root, err := html.Parse(strings.NewReader(string(data)))
		if err != nil {
			return nil, nil, ErrBTDetail
		}
		var found *html.Node
		var visit func(*html.Node)
		visit = func(n *html.Node) {
			for _, a := range n.Attr {
				if a.Key == "id" && a.Val == "kdescr" {
					found = n
					return
				}
			}
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				visit(child)
			}
		}
		visit(root)
		if found == nil {
			return nil, nil, ErrBTDetail
		}
		var b strings.Builder
		_ = html.Render(&b, found)
		description = b.String()
	}
	images := []string{}
	addImage := func(raw string) {
		if len(images) >= 16 {
			return
		}
		base, _ := url.Parse(baseURL)
		relative, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return
		}
		target := base.ResolveReference(relative)
		if _, err := btPosterURL(target.String()); err != nil {
			return
		}
		for _, existing := range images {
			if existing == target.String() {
				return
			}
		}
		images = append(images, target.String())
	}
	for _, match := range btBBImage.FindAllStringSubmatch(description, 16) {
		addImage(match[1])
	}
	description = btBBImage.ReplaceAllString(description, "")
	text := btDetailText(description, addImage)
	mediaText := btDetailText(media, func(string) {})
	if len(text) > 256<<10 || len(mediaText) > 256<<10 {
		return nil, nil, ErrBTDetail
	}
	return &BTResourceDetail{Description: text, MediaInfo: mediaText, ImagePaths: []string{}}, images, nil
}

func btDetailText(raw string, image func(string)) string {
	root, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "iframe", "form", "object", "embed", "svg", "template", "noscript":
				return
			case "img":
				for _, a := range n.Attr {
					if a.Key == "src" || a.Key == "data-src" {
						image(a.Val)
					}
				}
				return
			case "br":
				b.WriteByte('\n')
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "pre", "tr", "li", "blockquote":
				b.WriteByte('\n')
			}
		}
	}
	walk(root)
	return strings.TrimSpace(btBBTag.ReplaceAllString(b.String(), ""))
}
