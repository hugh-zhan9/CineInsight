package services

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 浏览器插件桥接服务（D-B03）。
//
// 这条通道与手机端 feed 服务是两回事，不能挂在一起：feed 是只读浏览、绑 0.0.0.0、
// 无鉴权，那条边界是"同一 WiFi 下知道地址的人能看能删"，是已知且被接受的。桥接的
// 端点会让桌面端**按外部请求去取任意 URL 并往磁盘写文件**，风险等级完全不同，
// 所以这里三道闸一起上：
//
//  1. 只绑 127.0.0.1——局域网上根本连不到；
//  2. 除 ping 外每个请求都要带令牌，且按定长比较；
//  3. 带 Origin 的请求只放行 chrome-extension:// 源。
//
// 第 3 条是给"任意网页借环回地址驱使桌面端下载"这种攻击准备的：网页发不出带自定义
// 令牌头的简单请求，预检会先被这一条挡掉。
const (
	BrowserBridgePortStart = 18110
	BrowserBridgePortEnd   = 18130
	// BrowserBridgeProtocol 是插件用来确认"端口后面确实是 CineInsight"的协议号。
	// 不兼容地改了请求或响应格式时才 +1。
	BrowserBridgeProtocol = 1
	// BrowserBridgeTokenHeader 与插件 src/common/constants.js 的同名常量一一对应。
	BrowserBridgeTokenHeader = "X-CineInsight-Token"

	browserBridgeMaxBodyBytes = 1 << 20

	// streamProxyBasePath 是本地流代理的挂载点。这条路径下**不校验令牌**，
	// 凭据是路径里的随机会话 ID——播放器没法带自定义请求头。
	streamProxyBasePath = "/bridge/v1/stream"
)

// BrowserBridgeStatus 是设置页要显示的桥接状态。
type BrowserBridgeStatus struct {
	Running       bool   `json:"running"`
	Enabled       bool   `json:"enabled"`
	Port          int    `json:"port"`
	URL           string `json:"url"`
	StartupError  string `json:"startup_error"`
	AllowedAccess string `json:"allowed_access"`
}

// BrowserBridgeAuth 提供当前的桥接开关与令牌。每个请求都现读一次，
// 用户在设置页关掉桥接或换了令牌之后，下一个请求立刻按新的来。
type BrowserBridgeAuth interface {
	BridgeCredentials() (token string, enabled bool, err error)
}

// BrowserDownloadEnqueuer 是桥接与下载队列之间的唯一接口。
// 分开是为了让服务端的鉴权与请求纪律能独立于队列实现单测。
type BrowserDownloadEnqueuer interface {
	Enqueue(request BrowserDownloadRequest) (BrowserDownloadTask, error)
	ListTasks() []BrowserDownloadTask
	CancelTask(id string) error
}

// BrowserDownloadRequest 是插件推过来的载荷。字段与插件的推送体一一对应：
// 解码时禁止未知字段，两边对不上会立刻报错，而不是静默丢字段。
// BrowserPlayRequest 是"直接播放"的载荷。字段与插件那边逐一对应，
// 解码时禁止未知字段。
type BrowserPlayRequest struct {
	URL       string `json:"url"`
	Referer   string `json:"referer"`
	Origin    string `json:"origin"`
	UserAgent string `json:"user_agent"`
}

type BrowserDownloadRequest struct {
	URL          string `json:"url"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	VariantLabel string `json:"variant_label"`
	PageURL      string `json:"page_url"`
	Referer      string `json:"referer"`
	UserAgent    string `json:"user_agent"`
	Origin       string `json:"origin"`
	Cookie       string `json:"cookie"`
}

type BrowserBridgeServer struct {
	downloads BrowserDownloadEnqueuer
	auth      BrowserBridgeAuth
	// playStream 把一条流直接交给本机播放器。为 nil 时该端点回 501。
	playStream func(url string, headers map[string]string) error
	// proxy 把"必须带请求头才给"的流转成本机可播的地址。播放器传不进请求头
	// （iina-cli 那条路实测不通），只能由代理在取源站时加。
	proxy *StreamProxy

	mu       sync.RWMutex
	server   *http.Server
	listener net.Listener
	status   BrowserBridgeStatus
	// stopped 在 Stop 里关闭，让"等 ctx 结束再收摊"的那个 goroutine 也能退出。
	// 没有它的话，每次设置保存触发的重启都会留下一个一直阻塞到应用退出的 goroutine。
	stopped chan struct{}
}

func NewBrowserBridgeServer(downloads BrowserDownloadEnqueuer, auth BrowserBridgeAuth) *BrowserBridgeServer {
	return &BrowserBridgeServer{
		downloads: downloads,
		auth:      auth,
		status: BrowserBridgeStatus{
			AllowedAccess: "127.0.0.1 only, token required",
		},
	}
}

// Start 在环回地址上取第一个可用端口。桥接关闭或没有令牌时不启动——
// 没有令牌的桥接等于没有闸门，宁可不开。
func (s *BrowserBridgeServer) Start(ctx context.Context) {
	if s == nil {
		return
	}
	token, enabled, err := s.auth.BridgeCredentials()
	if err != nil {
		s.setStatus(BrowserBridgeStatus{StartupError: fmt.Sprintf("读取桥接设置失败: %v", err), AllowedAccess: browserBridgeAccessNote})
		return
	}
	if !enabled {
		s.setStatus(BrowserBridgeStatus{Enabled: false, AllowedAccess: browserBridgeAccessNote})
		return
	}
	if strings.TrimSpace(token) == "" {
		s.setStatus(BrowserBridgeStatus{
			Enabled:       true,
			StartupError:  "桥接已开启但还没有令牌，先在设置里生成一个",
			AllowedAccess: browserBridgeAccessNote,
		})
		return
	}

	s.mu.Lock()
	if s.server != nil {
		s.mu.Unlock()
		return
	}

	var listener net.Listener
	var listenErr error
	selectedPort := 0
	for port := BrowserBridgePortStart; port <= BrowserBridgePortEnd; port++ {
		listener, listenErr = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if listenErr == nil {
			selectedPort = port
			break
		}
	}
	if listener == nil {
		s.status = BrowserBridgeStatus{
			Enabled:       true,
			StartupError:  fmt.Sprintf("桥接服务在 %d..%d 上都没能监听: %v", BrowserBridgePortStart, BrowserBridgePortEnd, listenErr),
			AllowedAccess: browserBridgeAccessNote,
		}
		s.mu.Unlock()
		return
	}

	s.listener = listener
	s.server = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	s.stopped = make(chan struct{})
	stopped := s.stopped
	s.status = BrowserBridgeStatus{
		Running:       true,
		Enabled:       true,
		Port:          selectedPort,
		URL:           fmt.Sprintf("http://127.0.0.1:%d/", selectedPort),
		AllowedAccess: browserBridgeAccessNote,
	}
	server := s.server
	s.mu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = s.Stop(shutdownCtx)
		case <-stopped:
			// 已经被显式停掉了（设置变更触发的重启），这里直接退出，
			// 不然每重启一次就多一个挂到应用退出为止的 goroutine。
		}
	}()

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			s.status.Running = false
			s.status.StartupError = err.Error()
			s.mu.Unlock()
		}
	}()
}

const browserBridgeAccessNote = "127.0.0.1 only, token required"

func (s *BrowserBridgeServer) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	server := s.server
	stopped := s.stopped
	s.server = nil
	s.listener = nil
	s.stopped = nil
	s.status.Running = false
	s.status.Port = 0
	s.status.URL = ""
	s.mu.Unlock()
	if stopped != nil {
		close(stopped)
	}
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

// Restart 供设置变更后调用：开关或令牌变了就重开一次。
func (s *BrowserBridgeServer) Restart(ctx context.Context) {
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = s.Stop(stopCtx)
	cancel()
	s.Start(ctx)
}

func (s *BrowserBridgeServer) Status() BrowserBridgeStatus {
	if s == nil {
		return BrowserBridgeStatus{AllowedAccess: browserBridgeAccessNote}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *BrowserBridgeServer) setStatus(status BrowserBridgeStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

// SetStreamProxy 注入本地流代理。
func (s *BrowserBridgeServer) SetStreamProxy(proxy *StreamProxy) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.proxy = proxy
	s.mu.Unlock()
}

// SetStreamPlayer 注入"直接播放"的实现（桌面端用 iina-cli 起播放器）。
func (s *BrowserBridgeServer) SetStreamPlayer(play func(url string, headers map[string]string) error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.playStream = play
	s.mu.Unlock()
}

func (s *BrowserBridgeServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/bridge/v1/ping", s.handlePing)
	mux.HandleFunc("/bridge/v1/play", s.handlePlay)
	mux.HandleFunc(streamProxyBasePath+"/", s.handleStream)
	mux.HandleFunc("/bridge/v1/downloads", s.handleDownloads)
	mux.HandleFunc("/bridge/v1/downloads/", s.handleDownloadAction)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 已经只绑了环回地址，这里再查一次是纵深防御：将来谁改了绑定地址，
		// 这一条会立刻把非本机来源挡在外面。
		if !browserBridgeLoopbackOnly(r.RemoteAddr) {
			writeBridgeError(w, http.StatusForbidden, "forbidden_source", "桥接只接受本机请求")
			return
		}
		if !browserBridgeOriginAllowed(r) {
			writeBridgeError(w, http.StatusForbidden, "forbidden_origin", "只有浏览器扩展可以调用桥接")
			return
		}
		if r.Method == http.MethodOptions {
			writeBridgeCORS(w, r)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeBridgeCORS(w, r)
		mux.ServeHTTP(w, r)
	})
}

// ping 不要令牌：插件得先认出"这个端口后面是 CineInsight"才谈得上配对。
// 正因为它不要令牌，回包里只放认领身份必需的两个字段，不带版本、路径、库信息。
func (s *BrowserBridgeServer) handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeBridgeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "只支持 GET")
		return
	}
	writeBridgeJSON(w, http.StatusOK, map[string]any{
		"app":      "cineinsight",
		"protocol": BrowserBridgeProtocol,
	})
}

// handleStream 是给播放器取流用的，**有意不要令牌**：播放器没法带自定义请求头。
// 它的凭据是路径里那个随机会话 ID——猜不到就取不到东西，而且只在环回上可达、
// 会话还会过期。会话只能由带令牌的 /bridge/v1/play 创建。
func (s *BrowserBridgeServer) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBridgeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "只支持 GET")
		return
	}
	s.mu.RLock()
	proxy := s.proxy
	s.mu.RUnlock()
	if proxy == nil {
		http.Error(w, "stream proxy unavailable", http.StatusNotImplemented)
		return
	}
	proxy.ServeStream(w, r, streamProxyBasePath)
}

// handlePlay 把一条流交给本机播放器直接播，不下载、不落盘。
func (s *BrowserBridgeServer) handlePlay(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeBridgeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "只支持 POST")
		return
	}
	var request BrowserPlayRequest
	if !decodeBridgeBody(w, r, &request) {
		return
	}
	s.mu.RLock()
	play := s.playStream
	s.mu.RUnlock()
	if play == nil {
		writeBridgeError(w, http.StatusNotImplemented, "play_unavailable", "这台机器上没有可用的播放器")
		return
	}
	headers := map[string]string{
		"Referer":    request.Referer,
		"Origin":     request.Origin,
		"User-Agent": request.UserAgent,
	}

	// 不把源站地址直接交给播放器：它传不进请求头，认 Referer 的站点会直接 403
	// （iina-cli 的 --mpv-http-header-fields 实测传不进去）。改成让播放器播本机
	// 代理上的地址，请求头由代理在取源站时加。
	s.mu.RLock()
	proxy := s.proxy
	s.mu.RUnlock()

	playURL := request.URL
	if proxy != nil && r.Host != "" {
		sessionID, err := proxy.CreateSession(request.URL, headers)
		if err != nil {
			writeBridgeError(w, http.StatusBadRequest, "proxy_failed", err.Error())
			return
		}
		// 用请求自己的 Host 拼回地址：这个请求就是打到桥接端口上的，
		// 比去读服务状态里的端口更直接，也不依赖启动顺序。
		playURL = fmt.Sprintf("http://%s%s/%s/%s", r.Host, streamProxyBasePath, sessionID, proxy.RootName(sessionID))
		log.Printf("IINA 播放：建立代理会话 source=%s proxy=%s referer=%q", request.URL, playURL, request.Referer)
	} else {
		log.Printf("IINA 播放：没有代理，直接把源站地址交给播放器 url=%s（这条路带不了请求头）", request.URL)
	}

	// 走代理时不必再给播放器传请求头——那条路本来也传不进去。
	if err := play(playURL, nil); err != nil {
		log.Printf("IINA 播放：起播放器失败 err=%v", err)
		writeBridgeError(w, http.StatusBadRequest, "play_failed", err.Error())
		return
	}
	log.Printf("IINA 播放：已交给播放器 %s", playURL)
	writeBridgeJSON(w, http.StatusOK, map[string]any{"played": true})
}

func (s *BrowserBridgeServer) handleDownloads(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeBridgeJSON(w, http.StatusOK, map[string]any{"tasks": s.downloads.ListTasks()})
	case http.MethodPost:
		var request BrowserDownloadRequest
		if !decodeBridgeBody(w, r, &request) {
			return
		}
		task, err := s.downloads.Enqueue(request)
		if err != nil {
			writeBridgeEnqueueError(w, err)
			return
		}
		writeBridgeJSON(w, http.StatusOK, task)
	default:
		writeBridgeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "只支持 GET 与 POST")
	}
}

func (s *BrowserBridgeServer) handleDownloadAction(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeBridgeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "只支持 POST")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/bridge/v1/downloads/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "cancel" {
		writeBridgeError(w, http.StatusNotFound, "unknown_action", "只支持 /bridge/v1/downloads/{id}/cancel")
		return
	}
	if err := s.downloads.CancelTask(parts[0]); err != nil {
		writeBridgeError(w, http.StatusNotFound, "task_not_found", err.Error())
		return
	}
	writeBridgeJSON(w, http.StatusOK, map[string]any{"canceled": parts[0]})
}

// 令牌每次现读：用户在设置页关掉桥接或换令牌之后，正在跑的服务立刻按新的判。
func (s *BrowserBridgeServer) authorize(w http.ResponseWriter, r *http.Request) bool {
	token, enabled, err := s.auth.BridgeCredentials()
	if err != nil {
		writeBridgeError(w, http.StatusInternalServerError, "settings_unavailable", "读取桥接设置失败")
		return false
	}
	if !enabled || strings.TrimSpace(token) == "" {
		writeBridgeError(w, http.StatusForbidden, "bridge_disabled", "桥接已关闭")
		return false
	}
	provided := r.Header.Get(BrowserBridgeTokenHeader)
	// 定长比较：按字节短路比较会把令牌的正确前缀长度泄露给能计时的调用方。
	if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
		writeBridgeError(w, http.StatusUnauthorized, "invalid_token", "令牌不对")
		return false
	}
	return true
}

// 请求体纪律与手机端 feed 同口径：必须 JSON、限长、拒绝未知字段、只能有一个 JSON 值。
func decodeBridgeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/json" {
		writeBridgeError(w, http.StatusUnsupportedMediaType, "json_required", "请求体必须是 application/json")
		return false
	}
	if r.Body == nil || r.ContentLength == 0 {
		writeBridgeError(w, http.StatusBadRequest, "json_body_required", "缺少 JSON 请求体")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, browserBridgeMaxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeBridgeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeBridgeError(w, http.StatusBadRequest, "invalid_json", "请求体只能有一个 JSON 值")
		return false
	}
	return true
}

func writeBridgeEnqueueError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBrowserDownloadDirectoryUnset):
		writeBridgeError(w, http.StatusBadRequest, "download_directory_unset", err.Error())
	case errors.Is(err, ErrBrowserDownloadInvalidRequest):
		writeBridgeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, ErrBrowserDownloadQueueFull):
		writeBridgeError(w, http.StatusTooManyRequests, "queue_full", err.Error())
	default:
		writeBridgeError(w, http.StatusInternalServerError, "enqueue_failed", err.Error())
	}
}

// 只放行扩展源。没有 Origin 头的请求（curl、桌面端自测）按放行处理——
// 真正的闸门是令牌，Origin 这一条是专门用来挡住"网页借环回地址发请求"的。
func browserBridgeOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return parsed.Scheme == "chrome-extension" || parsed.Scheme == "moz-extension"
}

func writeBridgeCORS(w http.ResponseWriter, r *http.Request) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return
	}
	// 走到这里 origin 已经过了 browserBridgeOriginAllowed，回显它而不是回 *：
	// 回 * 等于对所有源开放，带凭据的请求也会被浏览器放行。
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, "+BrowserBridgeTokenHeader)
	w.Header().Set("Access-Control-Max-Age", "600")
}

func browserBridgeLoopbackOnly(remoteAddr string) bool {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return addr.IsLoopback()
}

func writeBridgeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeBridgeError(w http.ResponseWriter, status int, code string, message string) {
	writeBridgeJSON(w, status, map[string]string{"error": code, "message": message})
}
