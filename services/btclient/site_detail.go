package btclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var ErrSiteUnsupported = errors.New("detail_unsupported: 当前站点尚未适配详情读取")

// SiteDetailRequest contains backend-only tracker credentials. Never return it
// through Wails or log its URL/headers. Prowlarr remains their configuration owner.
type SiteDetailRequest struct {
	URL     string
	Headers http.Header
	Format  string
}

func (c *Client) SiteDetail(ctx context.Context, indexerID int, infoURL string) (SiteDetailRequest, error) {
	if indexerID < 1 {
		return SiteDetailRequest{}, ErrResponse
	}
	data, err := c.request(ctx, http.MethodGet, "/api/v1/indexer/"+strconv.Itoa(indexerID), nil, nil, "", 2<<20)
	if err != nil {
		return SiteDetailRequest{}, err
	}
	return parseSiteDetailRequest(data, indexerID, infoURL)
}

func parseSiteDetailRequest(data []byte, indexerID int, infoURL string) (SiteDetailRequest, error) {
	var config struct {
		ID             int    `json:"id"`
		Implementation string `json:"implementation"`
		Enable         bool   `json:"enable"`
		Fields         []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"fields"`
	}
	if json.Unmarshal(data, &config) != nil || config.ID != indexerID || !config.Enable {
		return SiteDetailRequest{}, ErrResponse
	}
	fields := map[string]string{}
	for _, field := range config.Fields {
		var value string
		if json.Unmarshal(field.Value, &value) == nil {
			fields[field.Name] = value
		}
	}
	definition := fields["definitionFile"]
	if config.Implementation != "Cardigann" || (definition != "nicept-api" && definition != "nicept") {
		return SiteDetailRequest{}, ErrSiteUnsupported
	}
	base, err := url.Parse(fields["baseUrl"])
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Port() != "" && base.Port() != "443") || base.RawPath != "" || (base.Path != "" && base.Path != "/") {
		return SiteDetailRequest{}, ErrEndpoint
	}
	info, err := url.Parse(infoURL)
	if err != nil || info.User != nil || info.Fragment != "" || info.RawPath != "" || info.Scheme != base.Scheme || !strings.EqualFold(info.Host, base.Host) || info.Path != "/details.php" {
		return SiteDetailRequest{}, ErrEndpoint
	}
	ids := info.Query()["id"]
	if len(ids) != 1 || ids[0] == "" || len(ids[0]) > 20 || strings.Trim(ids[0], "0123456789") != "" {
		return SiteDetailRequest{}, ErrEndpoint
	}
	id := ids[0]
	headers := http.Header{}
	format := "html"
	target := *base
	if definition == "nicept-api" {
		key := fields["apikey"]
		if key == "" || strings.ContainsAny(key, "\r\n") || len(key) > 8192 {
			return SiteDetailRequest{}, ErrAuthentication
		}
		headers.Set("Authorization", "Bearer "+key)
		headers.Set("Accept", "application/json")
		target.Path = "/api/v1/detail/" + id
		target.RawQuery = "includes=extra"
		format = "json"
	} else {
		cookie := fields["cookie"]
		if cookie == "" || strings.ContainsAny(cookie, "\r\n") || len(cookie) > 16384 {
			return SiteDetailRequest{}, ErrAuthentication
		}
		headers.Set("Cookie", cookie)
		target.Path = "/details.php"
		target.RawQuery = url.Values{"id": {id}}.Encode()
	}
	return SiteDetailRequest{URL: target.String(), Headers: headers, Format: format}, nil
}
