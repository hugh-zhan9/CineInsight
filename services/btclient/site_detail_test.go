package btclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func siteConfig(definition, key string) []byte {
	return []byte(fmt.Sprintf(`{"id":1,"enable":true,"implementation":"Cardigann","fields":[{"name":"definitionFile","value":%q},{"name":"baseUrl","value":"https://www.nicept.net/"},{"name":"apikey","value":%q},{"name":"cookie","value":%q}]}`, definition, key, key))
}
func TestSiteDetailCredentialsAndOrigin(t *testing.T) {
	spec, err := parseSiteDetailRequest(siteConfig("nicept-api", "private-token"), 1, "https://www.nicept.net/details.php?id=42&hit=1")
	if err != nil || spec.URL != "https://www.nicept.net/api/v1/detail/42?includes=extra" || spec.Headers.Get("Authorization") != "Bearer private-token" || spec.Headers.Get("X-Api-Key") != "" {
		t.Fatalf("invalid spec: %v", err)
	}
	for _, address := range []string{"https://evil.example/details.php?id=42", "http://www.nicept.net/details.php?id=42", "https://www.nicept.net/download.php?id=42", "https://secret@www.nicept.net/details.php?id=42", "https://www.nicept.net/details.php?id=42&id=43", "https://www.nicept.net/details.php?id=../private", "https://www.nicept.net/details.php?id=42#fragment"} {
		if _, err := parseSiteDetailRequest(siteConfig("nicept-api", "secret"), 1, address); err == nil {
			t.Error("accepted invalid source")
		}
	}
	if _, err := parseSiteDetailRequest(siteConfig("unknown", "secret"), 1, "https://www.nicept.net/details.php?id=42"); err != ErrSiteUnsupported {
		t.Fatal("unsupported accepted")
	}
	if _, err := parseSiteDetailRequest(siteConfig("nicept-api", ""), 1, "https://www.nicept.net/details.php?id=42"); err != ErrAuthentication {
		t.Fatal("missing credential accepted")
	}
	spec, err = parseSiteDetailRequest(siteConfig("nicept", "uid=x;pass=y"), 1, "https://www.nicept.net/details.php?id=42&download=1")
	if err != nil || spec.URL != "https://www.nicept.net/details.php?id=42" || spec.Headers.Get("Cookie") != "uid=x;pass=y" || spec.Headers.Get("Authorization") != "" {
		t.Fatal("cookie request incorrect")
	}
}
func TestSiteDetailClientOnlyReadsLocalConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/indexer/1" || r.Header.Get("X-Api-Key") != "local-key" {
			t.Error("wrong local request")
		}
		_, _ = w.Write(siteConfig("nicept-api", "site-secret"))
	}))
	defer server.Close()
	client, _ := New(server.URL, "local-key")
	defer client.Close()
	spec, err := client.SiteDetail(context.Background(), 1, "https://www.nicept.net/details.php?id=42")
	if err != nil || strings.Contains(spec.URL, "secret") {
		t.Fatal("unsafe spec")
	}
}
