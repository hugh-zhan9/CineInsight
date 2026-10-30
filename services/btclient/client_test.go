package btclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEndpointBoundary(t *testing.T) {
	for _, endpoint := range []string{"https://example.com", "http://192.0.2.1:8081", "http://127.0.0.1@evil.test", "file:///tmp/x", "http://127.0.0.1:0", "http://127.0.0.1:99999", "http://127.0.0.1?key=x", "http://127.0.0.1/../x"} {
		if _, err := New(endpoint, ""); !errors.Is(err, ErrEndpoint) {
			t.Fatalf("accepted %s: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"http://localhost:8081", "http://127.0.0.1:9696/base", "http://[::1]:8081"} {
		c, err := New(endpoint, "")
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
}
func TestLoginVersionAndProjection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret" {
			t.Error("missing key")
		}
		switch r.URL.Path {
		case "/base/api/v2/auth/login":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("password") != "private" {
				t.Error("missing form password")
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "session", Path: "/"})
			w.Write([]byte("Ok."))
		case "/base/api/v2/app/version":
			if _, err := r.Cookie("SID"); err != nil {
				t.Error("no session")
			}
			w.Write([]byte("v5.2.3"))
		case "/base/api/v2/app/webapiVersion":
			w.Write([]byte("2.15.1"))
		case "/base/api/v1/indexer":
			w.Write([]byte(`[{"id":1,"name":"example","protocol":"torrent","enable":true,"fields":[{"name":"password","value":"private"}]}]`))
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer server.Close()
	c, err := New(server.URL+"/base", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.Login(context.Background(), "user", "private"); err != nil {
		t.Fatal(err)
	}
	v, a, err := c.QBVersion(context.Background())
	if err != nil || v != "v5.2.3" || a != "2.15.1" {
		t.Fatalf("%s %s %v", v, a, err)
	}
	rows, err := c.Indexers(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Name != "example" {
		t.Fatalf("%+v %v", rows, err)
	}
}
func TestResponseFailuresAreBoundedAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		code int
		body string
		want error
	}{{401, "password=private", ErrAuthentication}, {500, "http://tracker/?passkey=private", ErrResponse}, {302, "private", ErrResponse}, {200, strings.Repeat("x", 129), ErrResponseSize}, {200, "<html>private</html>", ErrResponse}} {
		t.Run(http.StatusText(tc.code)+tc.want.Error(), func(t *testing.T) {
			external := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { external++ }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c, _ := New(server.URL, "")
			defer c.Close()
			_, _, err := c.QBVersion(context.Background())
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "private") || external != 0 {
				t.Fatalf("%v external=%d", err, external)
			}
		})
	}
}
func TestCancelledRequest(t *testing.T) {
	c, _ := New("http://127.0.0.1:1", "")
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := c.QBVersion(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestHTTPSLocalhostUsesDNSCertificate(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/version") {
			w.Write([]byte("v5.2.3"))
		} else {
			w.Write([]byte("2.15.1"))
		}
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	defer server.Close()
	c, err := New(strings.Replace(server.URL, "127.0.0.1", "localhost", 1), "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	c.http.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
	if _, _, err = c.QBVersion(context.Background()); err != nil {
		t.Fatalf("valid localhost certificate rejected: %v", err)
	}
}

func TestVerifiedQBProfile(t *testing.T) {
	version := "v5.2.3"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/version") {
			w.Write([]byte(version))
		} else {
			w.Write([]byte("2.15.1"))
		}
	}))
	defer server.Close()
	c, _ := New(server.URL, "")
	defer c.Close()
	if err := c.CheckQBProfile(context.Background()); err != nil {
		t.Fatal(err)
	}
	version = "v6.0.0"
	if !errors.Is(c.CheckQBProfile(context.Background()), ErrVersionUnsupported) {
		t.Fatal("unverified profile accepted")
	}
}
