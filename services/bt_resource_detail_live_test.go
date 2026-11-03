package services

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/services/btclient"
)

// Explicitly opt in; this reads a configured tracker, never downloads a torrent
// or writes real titles, URLs, bodies, images or credentials to test output/disk.
func TestBTResourceDetailLiveProbe(t *testing.T) {
	if os.Getenv("CINEINSIGHT_BT_DETAIL_PROBE") != "1" {
		t.Skip("explicit opt-in only")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal("home unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(home, "Library/Application Support/Prowlarr/config.xml"))
	if err != nil {
		t.Fatal("local configuration unavailable")
	}
	var config struct {
		Port string `xml:"Port"`
		Key  string `xml:"ApiKey"`
		Base string `xml:"UrlBase"`
	}
	if xml.Unmarshal(raw, &config) != nil {
		t.Fatal("invalid local configuration")
	}
	client, err := btclient.New("http://127.0.0.1:"+config.Port+config.Base, config.Key)
	if err != nil {
		t.Fatal("local client unavailable")
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	search := NewBTSearchService(client, 1)
	search.fetchDetail = func(ctx context.Context, spec btclient.SiteDetailRequest) ([]byte, error) {
		data, err := defaultBTDetail(ctx, spec)
		if err != nil {
			t.Logf("detail fetch stage: %v", err)
			return nil, err
		}
		var shape struct {
			Ret  *int `json:"ret"`
			Data struct {
				Data struct {
					ID    json.RawMessage `json:"id"`
					Extra json.RawMessage `json:"extra"`
				} `json:"data"`
			} `json:"data"`
		}
		decodeErr := json.Unmarshal(data, &shape)
		t.Logf("detail decode stage: json_ok=%v ret_present=%v extra_present=%v bytes=%d", decodeErr == nil, shape.Ret != nil, len(shape.Data.Data.Extra) > 0, len(data))
		return data, nil
	}
	rows, err := search.Search(ctx, "", []int{1})
	if err != nil || len(rows.Resources) == 0 {
		t.Fatal("real list unavailable")
	}
	detail, err := search.ResourceDetail(ctx, rows.Resources[0].Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real detail: description_chars=%d mediainfo_chars=%d images=%d", len([]rune(detail.Description)), len([]rune(detail.MediaInfo)), len(detail.ImagePaths))
	if detail.Description == "" && detail.MediaInfo == "" && len(detail.ImagePaths) == 0 {
		t.Fatal("real detail unexpectedly empty")
	}
	if len(detail.ImagePaths) > 0 {
		data, mime, err := search.DetailImage(ctx, rows.Resources[0].Token, 1, 0)
		if err != nil {
			t.Fatal("real screenshot unavailable")
		}
		t.Logf("real screenshot: bytes=%d mime=%s", len(data), mime)
	}
}
