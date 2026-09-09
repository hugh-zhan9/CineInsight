package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"video-master/database"
	"video-master/models"
)

const jellyfinUserID = "ff000000000000000000000000000001"

// JellyfinConfigInput is desktop-only; an empty password preserves the saved hash.
type JellyfinConfigInput struct {
	Enabled  bool   `json:"enabled"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// JellyfinStatus never exposes password hashes or session tokens.
type JellyfinStatus struct {
	Enabled      bool     `json:"enabled"`
	Running      bool     `json:"running"`
	Port         int      `json:"port"`
	Username     string   `json:"username"`
	PasswordSet  bool     `json:"password_set"`
	LANURLs      []string `json:"lan_urls"`
	StartupError string   `json:"startup_error"`
}

type jellyfinSession struct {
	expires time.Time
	id      string
}

// JellyfinServer owns only protocol authentication and the HTTP listener.
type JellyfinServer struct {
	lifecycle     sync.Mutex
	mu            sync.Mutex
	writes        sync.Mutex
	requests      sync.WaitGroup
	server        *http.Server
	config        models.Settings
	status        JellyfinStatus
	generation    uint64
	sessions      map[[32]byte]jellyfinSession
	loginWindow   time.Time
	loginAttempts int
	loginSlot     chan struct{}
	video         *VideoService
	thumbnail     *ThumbnailService
	probe         *MediaProbeService
	// Set by the library adapter; authentication always runs before dispatch.
	api http.Handler
}

// NewJellyfinServer creates a disabled server without opening a port.
func NewJellyfinServer(video *VideoService, thumbnail *ThumbnailService, probe *MediaProbeService) *JellyfinServer {
	s := &JellyfinServer{video: video, thumbnail: thumbnail, probe: probe, sessions: make(map[[32]byte]jellyfinSession), loginSlot: make(chan struct{}, 1)}
	s.api = http.HandlerFunc(s.serveLibrary)
	return s
}

// Configure serializes persistence, revocation and listener replacement.
func (s *JellyfinServer) Configure(input JellyfinConfigInput) (JellyfinStatus, error) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	var config models.Settings
	if err := database.DB.First(&config).Error; err != nil {
		return s.Status(), fmt.Errorf("读取 Jellyfin 设置失败")
	}
	if input.Port == 0 {
		input.Port = 8096
	}
	input.Username = strings.TrimSpace(input.Username)
	if input.Port < 1024 || input.Port > 65535 {
		return s.Status(), fmt.Errorf("端口必须在 1024–65535 之间")
	}
	if utf8.RuneCountInString(input.Username) > 64 || strings.IndexFunc(input.Username, unicode.IsControl) >= 0 || (input.Enabled && input.Username == "") {
		return s.Status(), fmt.Errorf("用户名不能为空、不能含控制字符且最多 64 字符")
	}
	if input.Password != "" {
		if len(input.Password) < 8 || len(input.Password) > 72 {
			return s.Status(), fmt.Errorf("密码必须为 8–72 字节")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			return s.Status(), fmt.Errorf("密码处理失败")
		}
		config.JellyfinPasswordHash = string(hash)
	}
	if input.Enabled && config.JellyfinPasswordHash == "" {
		return s.Status(), fmt.Errorf("请先设置登录密码")
	}
	if config.JellyfinServerID == "" {
		id, err := jellyfinRandom(16)
		if err != nil {
			return s.Status(), err
		}
		config.JellyfinServerID = id
	}
	config.JellyfinEnabled, config.JellyfinPort, config.JellyfinUsername = input.Enabled, input.Port, input.Username
	// GORM's error logger interpolates SQL parameters, including password hashes.
	result := database.DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Model(&models.Settings{}).Where("id = ?", config.ID).Updates(map[string]interface{}{
		"jellyfin_enabled": config.JellyfinEnabled, "jellyfin_port": config.JellyfinPort, "jellyfin_username": config.JellyfinUsername, "jellyfin_password_hash": config.JellyfinPasswordHash, "jellyfin_server_id": config.JellyfinServerID,
	})
	if result.Error != nil || result.RowsAffected != 1 {
		return s.Status(), fmt.Errorf("保存 Jellyfin 设置失败")
	}
	s.stopLocked()
	s.startLocked(config)
	return s.Status(), nil
}

// Start reads persisted settings. Disabled is the installation default.
func (s *JellyfinServer) Start() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	var config models.Settings
	if err := database.DB.First(&config).Error; err != nil {
		s.mu.Lock()
		s.status.StartupError = "读取 Jellyfin 设置失败"
		s.mu.Unlock()
		return
	}
	s.stopLocked()
	s.startLocked(config)
}

func (s *JellyfinServer) startLocked(config models.Settings) {
	if config.JellyfinPort == 0 {
		config.JellyfinPort = 8096
	}
	s.mu.Lock()
	s.config = config
	s.status = JellyfinStatus{Enabled: config.JellyfinEnabled, Port: config.JellyfinPort, Username: config.JellyfinUsername, PasswordSet: config.JellyfinPasswordHash != "", LANURLs: []string{}}
	defer s.mu.Unlock()
	if !config.JellyfinEnabled {
		return
	}
	if config.JellyfinUsername == "" || config.JellyfinPasswordHash == "" || len(config.JellyfinServerID) != 32 {
		s.status.StartupError = "Jellyfin 登录配置不完整"
		s.config.JellyfinEnabled = false
		return
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(config.JellyfinPort)))
	if err != nil {
		s.status.StartupError = fmt.Sprintf("端口 %d 无法监听，请检查是否被占用", config.JellyfinPort)
		s.config.JellyfinEnabled = false
		return
	}
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	s.server = server
	s.status.Running = true
	for _, url := range shortFeedLANURLs(config.JellyfinPort) {
		s.status.LANURLs = append(s.status.LANURLs, strings.TrimSuffix(url, "short/"))
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.server == server {
				s.status.Running = false
				s.status.StartupError = "Jellyfin 监听异常结束"
				s.config.JellyfinEnabled = false
			}
		}
	}()
}

// Stop revokes credentials and closes active streams before returning.
func (s *JellyfinServer) Stop() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.stopLocked()
}
func (s *JellyfinServer) stopLocked() {
	s.mu.Lock()
	server := s.server
	s.server = nil
	s.config.JellyfinEnabled = false
	s.status.Running = false
	s.generation++
	s.sessions = make(map[[32]byte]jellyfinSession)
	s.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
	// Admission and WaitGroup.Add share mu with disabling, so Wait cannot race Add.
	s.requests.Wait()
}

// Status returns the public desktop configuration and listener status.
func (s *JellyfinServer) Status() JellyfinStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.status
	status.LANURLs = append([]string{}, status.LANURLs...)
	return status
}

type jellyfinIdentity struct {
	generation uint64
	token      [32]byte
}
type jellyfinIdentityKey struct{}

// Handler puts the same authentication boundary around every resource route.
func (s *JellyfinServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &jellyfinStatusWriter{ResponseWriter: w}
		w = recorder
		path := strings.ToLower(strings.TrimSuffix(r.URL.Path, "/"))
		for _, prefix := range []string{"/jellyfin", "/emby"} {
			if strings.HasPrefix(path, prefix+"/") {
				path = strings.TrimPrefix(path, prefix)
				break
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if !shortFeedRemoteAllowed(r.RemoteAddr) || !shortFeedSameOriginMutation(r) {
			jellyfinError(w, 403, "访问来源不允许")
			return
		}
		// Logging starts inside the LAN boundary so outside hosts cannot fill app.log.
		defer jellyfinLogRequest(r.Method, path, r.URL.Query(), recorder)
		s.mu.Lock()
		config, generation := s.config, s.generation
		if config.JellyfinEnabled {
			s.requests.Add(1)
		}
		s.mu.Unlock()
		if !config.JellyfinEnabled {
			jellyfinError(w, 503, "Jellyfin 服务未启用")
			return
		}
		defer s.requests.Done()
		if path == "/system/info/public" && (r.Method == "GET" || r.Method == "HEAD") {
			jellyfinJSON(w, s.systemInfo(config, r.Host))
			return
		}
		if path == "/users/authenticatebyname" && r.Method == "POST" {
			s.login(w, r, config, generation)
			return
		}
		token := jellyfinToken(r)
		identity := jellyfinIdentity{generation: generation, token: sha256.Sum256([]byte(token))}
		if token == "" || !s.authorized(identity) {
			jellyfinError(w, 401, "需要有效的登录令牌")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), jellyfinIdentityKey{}, identity))
		urlCopy := *r.URL
		urlCopy.Path = path
		r.URL = &urlCopy
		if path == "/sessions/logout" && r.Method == "POST" {
			s.mu.Lock()
			delete(s.sessions, identity.token)
			s.mu.Unlock()
			w.WriteHeader(204)
			return
		}
		if (path == "/users/me" || path == "/users/"+jellyfinUserID) && r.Method == "GET" {
			jellyfinJSON(w, s.userDTO(config))
			return
		}
		if path == "/system/info" && r.Method == "GET" {
			jellyfinJSON(w, s.systemInfo(config, r.Host))
			return
		}
		if s.api != nil {
			s.api.ServeHTTP(w, r)
			return
		}
		jellyfinError(w, 404, "不支持的 Jellyfin 接口")
	})
}

func (s *JellyfinServer) authorized(identity jellyfinIdentity) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[identity.token]
	return ok && s.config.JellyfinEnabled && identity.generation == s.generation && time.Now().Before(session.expires)
}

func (s *JellyfinServer) systemInfo(config models.Settings, host string) map[string]interface{} {
	return map[string]interface{}{"Id": config.JellyfinServerID, "ServerName": "CineInsight", "ProductName": "Jellyfin Server", "Version": "10.10.7", "StartupWizardCompleted": true, "LocalAddress": "http://" + host}
}
func (s *JellyfinServer) userDTO(config models.Settings) map[string]interface{} {
	return map[string]interface{}{"Id": jellyfinUserID, "Name": config.JellyfinUsername, "ServerId": config.JellyfinServerID, "HasPassword": true, "HasConfiguredPassword": true, "EnableAutoLogin": false, "Configuration": map[string]interface{}{"SubtitleMode": "Default", "PlayDefaultAudioTrack": true}, "Policy": map[string]interface{}{"IsAdministrator": false, "IsDisabled": false, "EnableMediaPlayback": true, "EnableContentDeletion": true, "EnableContentDeletionFromFolders": []string{}, "EnableContentDownloading": true, "EnableAudioPlaybackTranscoding": false, "EnableVideoPlaybackTranscoding": false, "EnablePlaybackRemuxing": false, "EnableAllFolders": true}}
}
func jellyfinRandom(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成认证标识失败")
	}
	return hex.EncodeToString(raw), nil
}
func jellyfinJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}
func jellyfinError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"Message": message})
}
func jellyfinDecode(w http.ResponseWriter, r *http.Request, out interface{}) bool {
	// The raw writer lets MaxBytesReader keep its close-after-reply hint; errors still go through w.
	r.Body = http.MaxBytesReader(jellyfinUnwrap(w), r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(out); err != nil {
		jellyfinError(w, 400, "请求 JSON 无效或超过 1 MiB")
		return false
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		jellyfinError(w, 400, "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}

// jellyfinStatusWriter records the response status for the request log.
type jellyfinStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *jellyfinStatusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *jellyfinStatusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

// ReadFrom keeps http.ServeContent on the underlying sendfile path.
func (w *jellyfinStatusWriter) ReadFrom(src io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if from, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return from.ReadFrom(src)
	}
	return io.Copy(w.ResponseWriter, src)
}

// Unwrap follows the http.ResponseController convention.
func (w *jellyfinStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func jellyfinUnwrap(w http.ResponseWriter) http.ResponseWriter {
	if recorder, ok := w.(*jellyfinStatusWriter); ok {
		return recorder.ResponseWriter
	}
	return w
}

// jellyfinLogSafe keeps one record on one line: control and non-printable runes become '?'
// and the text is capped so a client cannot pad or forge log lines.
func jellyfinLogSafe(text string, max int) string {
	runes := []rune(text)
	if len(runes) > max {
		runes = append(runes[:max], '…')
	}
	for i, r := range runes {
		if r < 0x20 || r == 0x7f || !unicode.IsPrint(r) {
			runes[i] = '?'
		}
	}
	return string(runes)
}

// Only enumerated parameters are logged with their values; everything else is logged by name.
var jellyfinLoggedValues = map[string]bool{"sortby": true, "sortorder": true, "includeitemtypes": true, "excludeitemtypes": true, "filters": true, "mediatypes": true, "excludelocationtypes": true, "locationtypes": true, "recursive": true, "limit": true, "startindex": true, "fields": true, "isfavorite": true, "isplayed": true, "isresumable": true, "ismissing": true, "collapseboxsetitems": true, "static": true, "enabledirectplay": true, "maxstreamingbitrate": true, "enableimages": true, "imagetypelimit": true, "enableimagetypes": true, "enabletotalrecordcount": true, "enableuserdata": true, "groupitems": true, "ismovie": true, "isseries": true, "isnews": true, "iskids": true, "issports": true, "includepeople": true, "includemedia": true, "includegenres": true, "includestudios": true, "includeartists": true}

// jellyfinLogRequest writes the route shape, status and parameter names (§九): IDs are masked,
// and tokens, search terms and media paths never appear. Successful media transfers and
// progress reports are too frequent to log; every rejected request is logged.
func jellyfinLogRequest(method, path string, query url.Values, recorder *jellyfinStatusWriter) {
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	if status < 400 && (strings.HasPrefix(path, "/videos/") || strings.Contains(path, "/images/") || strings.HasSuffix(path, "/download") || strings.HasPrefix(path, "/sessions/playing")) {
		return
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if compact := strings.ReplaceAll(segment, "-", ""); len(compact) == 32 && strings.Trim(compact, "0123456789abcdef") == "" {
			segments[i] = "{id}"
		}
	}
	params := make([]string, 0, len(query))
	for key, values := range query {
		if jellyfinLoggedValues[strings.ToLower(key)] {
			params = append(params, jellyfinLogSafe(key+"="+strings.Join(values, "|"), 120))
		} else {
			params = append(params, jellyfinLogSafe(key, 40))
		}
	}
	sort.Strings(params)
	summary := jellyfinLogSafe(strings.Join(params, ","), 400)
	if summary == "" {
		summary = "-"
	}
	log.Printf("[Jellyfin] %s %s %d 参数=%s", jellyfinLogSafe(method, 16), jellyfinLogSafe(strings.Join(segments, "/"), 120), status, summary)
}
