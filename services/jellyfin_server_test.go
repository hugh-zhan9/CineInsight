package services

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func jellyfinTestServer(t *testing.T) *JellyfinServer {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	s := NewJellyfinServer(&VideoService{}, nil, nil)
	s.config = models.Settings{JellyfinEnabled: true, JellyfinPort: 8096, JellyfinUsername: "viewer", JellyfinPasswordHash: string(hash), JellyfinServerID: strings.Repeat("a", 32)}
	return s
}
func jellyfinRequest(s *JellyfinServer, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.2:12345"
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("X-Emby-Token", token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func jellyfinLogin(t *testing.T, s *JellyfinServer) string {
	t.Helper()
	w := jellyfinRequest(s, "POST", "/Users/AuthenticateByName", "", `{"Username":"viewer","Pw":"test-password"}`)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	var body struct{ AccessToken string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.AccessToken) != 64 {
		t.Fatal("missing random token")
	}
	return body.AccessToken
}
func TestJellyfinAuthenticationBoundary(t *testing.T) {
	s := jellyfinTestServer(t)
	if w := jellyfinRequest(s, "POST", "/Users/AuthenticateByName", "", `{"Username":"viewer","Pw":"wrong"}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	token := jellyfinLogin(t, s)
	for _, path := range []string{"/Items", "/Videos/1/stream", "/Items/1/Images/Primary", "/Users/Me"} {
		if w := jellyfinRequest(s, "GET", path, "", ""); w.Code != 401 {
			t.Fatalf("unguarded %s: %d", path, w.Code)
		}
	}
	for _, header := range []string{"Authorization", "X-Emby-Authorization", "X-Emby-Token", "query"} {
		r := httptest.NewRequest("GET", "/Users/Me", nil)
		r.RemoteAddr = "127.0.0.1:10"
		switch header {
		case "query":
			r.URL.RawQuery = "api_key=" + token
		case "X-Emby-Token":
			r.Header.Set(header, token)
		default:
			r.Header.Set(header, `MediaBrowser Client="Fileball", Token="`+token+`"`)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d", header, w.Code)
		}
		if bytes.Contains(w.Body.Bytes(), []byte("PasswordHash")) || bytes.Contains(w.Body.Bytes(), []byte("test-password")) {
			t.Fatal("credentials exposed")
		}
	}
	for _, remote := range []string{"8.8.8.8:123", "invalid"} {
		r := httptest.NewRequest("GET", "/System/Info/Public", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-For", "192.168.1.2")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	r := httptest.NewRequest("GET", "http://192.168.1.1:8096/Users/Me", nil)
	r.RemoteAddr = "192.168.1.2:1"
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("X-Emby-Token", token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := jellyfinRequest(s, "POST", "/Sessions/Logout", token, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := jellyfinRequest(s, "GET", "/Users/Me", token, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
}
func TestJellyfinExpiredRevokedAndBoundedSessions(t *testing.T) {
	s := jellyfinTestServer(t)
	token := jellyfinLogin(t, s)
	key := sha256.Sum256([]byte(token))
	session := s.sessions[key]
	session.expires = time.Now().Add(-time.Second)
	s.sessions[key] = session
	if w := jellyfinRequest(s, "GET", "/Users/Me", token, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	token = jellyfinLogin(t, s)
	s.Stop()
	if w := jellyfinRequest(s, "GET", "/Users/Me", token, ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
	s = jellyfinTestServer(t)
	s.loginAttempts = 20
	s.loginWindow = time.Now()
	if w := jellyfinRequest(s, "POST", "/Users/AuthenticateByName", "", `{}`); w.Code != 429 {
		t.Fatal(w.Code)
	}
	s = jellyfinTestServer(t)
	for i := 0; i < 64; i++ {
		key := [32]byte{byte(i)}
		s.sessions[key] = jellyfinSession{expires: time.Now().Add(time.Hour)}
	}
	if w := jellyfinRequest(s, "POST", "/Users/AuthenticateByName", "", `{"Username":"viewer","Pw":"test-password"}`); w.Code != 429 {
		t.Fatal(w.Code)
	}
}
func TestJellyfinConfigureAndGenericSavePreserveCredentials(t *testing.T) {
	setupVideoServiceTestDB(t)
	s := NewJellyfinServer(&VideoService{}, nil, nil)
	defer s.Stop()
	status, err := s.Configure(JellyfinConfigInput{Username: "viewer", Password: "test-password"})
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.Running || !status.PasswordSet || status.Port != 8096 {
		t.Fatalf("bad defaults %+v", status)
	}
	var before models.Settings
	database.DB.First(&before)
	// Observe actual GORM update columns, which must exclude owned fields even
	// if a generic save loaded its snapshot before an independent configuration.
	var selected []string
	const callback = "test:jellyfin-omit"
	if err := database.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "settings" {
			selected = append(selected, tx.Statement.Omits...)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Update().Remove(callback)
	if err := (&SettingsService{}).UpdateSettings(models.Settings{JellyfinUsername: "overwrite", JellyfinPasswordHash: "overwrite"}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"JellyfinEnabled", "JellyfinPort", "JellyfinUsername", "JellyfinPasswordHash", "JellyfinServerID"} {
		found := false
		for _, omit := range selected {
			if omit == field {
				found = true
			}
		}
		if !found {
			t.Fatalf("not omitted: %s", field)
		}
	}
	var after models.Settings
	database.DB.First(&after)
	if before.JellyfinPasswordHash != after.JellyfinPasswordHash || after.JellyfinUsername != "viewer" || after.JellyfinServerID != before.JellyfinServerID {
		t.Fatal("generic save changed credentials")
	}
	if _, err := s.Configure(JellyfinConfigInput{Username: "viewer"}); err != nil {
		t.Fatal(err)
	}
	database.DB.First(&after)
	if before.JellyfinPasswordHash != after.JellyfinPasswordHash {
		t.Fatal("blank password must preserve hash")
	}
}
func TestJellyfinConcurrentConfigurationMatchesListener(t *testing.T) {
	setupVideoServiceTestDB(t)
	s := NewJellyfinServer(&VideoService{}, nil, nil)
	defer s.Stop()
	var wg sync.WaitGroup
	for _, name := range []string{"first", "second"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if _, err := s.Configure(JellyfinConfigInput{Username: name, Password: "test-password"}); err != nil {
				t.Error(err)
			}
		}(name)
	}
	wg.Wait()
	var config models.Settings
	database.DB.First(&config)
	if s.Status().Username != config.JellyfinUsername {
		t.Fatal("runtime and persisted config differ")
	}
}
func TestJellyfinPortConflictIsVisibleAndNeverFallsBack(t *testing.T) {
	setupVideoServiceTestDB(t)
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	s := NewJellyfinServer(&VideoService{}, nil, nil)
	defer s.Stop()
	status, err := s.Configure(JellyfinConfigInput{Enabled: true, Port: port, Username: "viewer", Password: "test-password"})
	if err != nil {
		t.Fatal(err)
	}
	if status.Running || status.StartupError == "" || status.Port != port || !status.Enabled {
		t.Fatalf("conflict %+v", status)
	}
	if w := jellyfinRequest(s, http.MethodGet, "/System/Info/Public", "", ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestJellyfinStopCancelsAndDrainsReadHandlers(t *testing.T) {
	s := jellyfinTestServer(t)
	token := jellyfinLogin(t, s)
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(cancelled)
		<-release
	})
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	s.server = server.Config
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		r, _ := http.NewRequest("GET", server.URL+"/Items", nil)
		r.Header.Set("X-Emby-Token", token)
		response, err := server.Client().Do(r)
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("request not cancelled")
	}
	select {
	case <-stopped:
		t.Fatal("Stop returned before handler exited")
	default:
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not drain")
	}
	<-clientDone
}
