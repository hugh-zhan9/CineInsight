package btclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// QBTorrent/QBProperties/QBFile are backend snapshots, never public task DTOs.
type QBTorrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
	Tags        string  `json:"tags"`
	Category    string  `json:"category"`
	State       string  `json:"state"`
	Progress    float64 `json:"progress"`
	AutoTMM     bool    `json:"auto_tmm"`
}
type QBProperties struct {
	Hash            string `json:"hash"`
	InfoHashV1      string `json:"infohash_v1"`
	InfoHashV2      string `json:"infohash_v2"`
	TotalDownloaded int64  `json:"total_downloaded"`
	SavePath        string `json:"save_path"`
}
type QBFile struct {
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
	Priority int     `json:"priority"`
	IsSeed   bool    `json:"is_seed"`
}

func validQBHash(hash string) bool {
	if len(hash) != 40 {
		return false
	}
	for _, c := range hash {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func (c *Client) QBTorrents(ctx context.Context, hash string) ([]QBTorrent, error) {
	if !validQBHash(hash) {
		return nil, ErrResponse
	}
	data, err := c.request(ctx, http.MethodGet, "/api/v2/torrents/info", url.Values{"hashes": {hash}}, nil, "", 2<<20)
	if err != nil {
		return nil, err
	}
	var out []QBTorrent
	if json.Unmarshal(data, &out) != nil {
		return nil, ErrResponse
	}
	for _, row := range out {
		if row.Hash != hash {
			return nil, ErrResponse
		}
	}
	return out, nil
}
func (c *Client) QBProperties(ctx context.Context, hash string) (QBProperties, error) {
	var out QBProperties
	if !validQBHash(hash) {
		return out, ErrResponse
	}
	data, err := c.request(ctx, http.MethodGet, "/api/v2/torrents/properties", url.Values{"hash": {hash}}, nil, "", 2<<20)
	if err != nil {
		return out, err
	}
	if json.Unmarshal(data, &out) != nil {
		return out, ErrResponse
	}
	return out, nil
}

// Match the accepted manifest budget, allowing worst-case JSON escaping plus
// bounded per-row protocol fields. Both wire bytes and decoded paths are capped.
const qbFileCountLimit = 10000
const qbFilePathByteLimit = 16 << 20
const qbFilesResponseLimit = 6*qbFilePathByteLimit + qbFileCountLimit*1024 + (64 << 10)

func (c *Client) QBFiles(ctx context.Context, hash string) ([]QBFile, error) {
	if !validQBHash(hash) {
		return nil, ErrResponse
	}
	data, err := c.request(ctx, http.MethodGet, "/api/v2/torrents/files", url.Values{"hash": {hash}}, nil, "", qbFilesResponseLimit)
	if err != nil {
		return nil, err
	}
	return decodeQBFiles(data)
}
func decodeQBFiles(data []byte) ([]QBFile, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, ErrResponse
	}
	out := make([]QBFile, 0, 128)
	seen := map[int]bool{}
	remaining := qbFilePathByteLimit
	for decoder.More() {
		// Check before decoding the next object, not after allocating a full array.
		if len(out) >= qbFileCountLimit {
			return nil, ErrResponse
		}
		var file QBFile
		if decoder.Decode(&file) != nil || file.Name == "" || file.Index < 0 || file.Size < 0 || seen[file.Index] || file.Progress < 0 || file.Progress > 1 {
			return nil, ErrResponse
		}
		if len(file.Name) > remaining {
			return nil, ErrResponseSize
		}
		remaining -= len(file.Name)
		seen[file.Index] = true
		out = append(out, file)
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim(']') {
		return nil, ErrResponse
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrResponse
	}
	return out, nil
}

// QBAddOptions are fixed by the coordinator, never forwarded from browser input.
type QBAddOptions struct {
	Torrent      []byte
	Magnet       string
	SavePath     string
	OwnerTag     string
	MetadataOnly bool
}

func (c *Client) QBAddStopped(ctx context.Context, o QBAddOptions) (string, error) {
	if (len(o.Torrent) == 0) == (o.Magnet == "") || len(o.Torrent) > 16<<20 || o.SavePath == "" || o.OwnerTag == "" || strings.ContainsAny(o.OwnerTag, ",\r\n") {
		return "", ErrResponse
	}
	if o.Magnet != "" && !validMagnet(o.Magnet) {
		return "", ErrResponse
	}
	// Sessions can restart or be upgraded without a CineInsight config change.
	// Revalidate at the write boundary instead of trusting a cached profile.
	if !o.MetadataOnly {
		if err := c.CheckQBProfile(ctx); err != nil {
			return "", err
		}
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	fields := map[string]string{"stopped": "true", "autoTMM": "false", "contentLayout": "Original", "savepath": o.SavePath, "useDownloadPath": "false", "tags": o.OwnerTag, "category": "CineInsight"}
	if o.MetadataOnly {
		if o.Magnet == "" {
			return "", ErrResponse
		}
		if err := c.CheckQBMetadataProfile(ctx); err != nil {
			return "", err
		}
		fields["stopped"] = "false"
		fields["stopCondition"] = "MetadataReceived"
	}
	if o.Magnet != "" {
		fields["urls"] = o.Magnet
	} else {
		part, err := form.CreateFormFile("torrents", "resource.torrent")
		if err != nil {
			return "", ErrResponse
		}
		if _, err = io.Copy(part, bytes.NewReader(o.Torrent)); err != nil {
			return "", ErrResponse
		}
	}
	for k, v := range fields {
		if form.WriteField(k, v) != nil {
			return "", ErrResponse
		}
	}
	if form.Close() != nil {
		return "", ErrResponse
	}
	data, err := c.request(ctx, http.MethodPost, "/api/v2/torrents/add", nil, &body, form.FormDataContentType(), 1024)
	if err != nil {
		return "", err
	}
	var result struct {
		Success int      `json:"success_count"`
		Failure int      `json:"failure_count"`
		Pending int      `json:"pending_count"`
		IDs     []string `json:"added_torrent_ids"`
	}
	if json.Unmarshal(data, &result) != nil || result.Success != 1 || result.Failure != 0 || result.Pending != 0 || len(result.IDs) != 1 || !validQBHash(result.IDs[0]) {
		return "", ErrResponse
	}
	return result.IDs[0], nil
}
func (c *Client) qbAction(ctx context.Context, path string, values url.Values) error {
	if err := c.CheckQBProfile(ctx); err != nil {
		return err
	}
	_, err := c.request(ctx, http.MethodPost, "/api/v2/torrents/"+path, nil, strings.NewReader(values.Encode()), "application/x-www-form-urlencoded", 1024)
	return err
}
func (c *Client) QBRun(ctx context.Context, hash string, start bool) error {
	if !validQBHash(hash) {
		return ErrResponse
	}
	action := "stop"
	if start {
		action = "start"
	}
	return c.qbAction(ctx, action, url.Values{"hashes": {hash}})
}
func (c *Client) QBPriority(ctx context.Context, hash string, indices []int, priority int) error {
	if !validQBHash(hash) || len(indices) == 0 || len(indices) > 10000 || (priority != 0 && priority != 1) {
		return ErrResponse
	}
	ids := make([]string, len(indices))
	for i, id := range indices {
		if id < 0 {
			return ErrResponse
		}
		ids[i] = strconv.Itoa(id)
	}
	return c.qbAction(ctx, "filePrio", url.Values{"hash": {hash}, "id": {strings.Join(ids, "|")}, "priority": {strconv.Itoa(priority)}})
}
func (c *Client) QBDeleteMetadata(ctx context.Context, hash string) error {
	if !validQBHash(hash) {
		return ErrResponse
	}
	return c.qbAction(ctx, "delete", url.Values{"hashes": {hash}, "deleteFiles": {"false"}})
}
func (c *Client) QBExport(ctx context.Context, hash string) ([]byte, error) {
	if !validQBHash(hash) {
		return nil, ErrResponse
	}
	return c.request(ctx, http.MethodGet, "/api/v2/torrents/export", url.Values{"hash": {hash}}, nil, "", 16<<20)
}

// QBMetadataBuild is a non-secret capability snapshot of the executing client.
type QBMetadataBuild struct {
	Libtorrent string `json:"libtorrent"`
}

func (c *Client) QBMetadataBuild(ctx context.Context) (QBMetadataBuild, error) {
	var result QBMetadataBuild
	data, err := c.request(ctx, http.MethodGet, "/api/v2/app/buildInfo", nil, nil, "", 4096)
	if err != nil {
		return result, err
	}
	if json.Unmarshal(data, &result) != nil || result.Libtorrent == "" {
		return result, ErrResponse
	}
	return result, nil
}

// CheckQBMetadataProfile requires the exact app/API and libtorrent build tested
// with an isolated loopback peer: atomic running add + MetadataReceived stops
// before any payload. A stopped add followed by start does not have this property.
func (c *Client) CheckQBMetadataProfile(ctx context.Context) error {
	if err := c.CheckQBProfile(ctx); err != nil {
		return err
	}
	build, err := c.QBMetadataBuild(ctx)
	if err != nil {
		return err
	}
	if build.Libtorrent != "2.0.13.0" {
		return ErrVersionUnsupported
	}
	return nil
}
