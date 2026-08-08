package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

type ShortFeedHTTPServerConfig struct {
	BindAddress string
	PortStart   int
	PortEnd     int
}

type ShortFeedHTTPServer struct {
	feed     *ShortFeedService
	assets   fs.FS
	config   ShortFeedHTTPServerConfig
	mu       sync.RWMutex
	server   *http.Server
	listener net.Listener
	status   ShortFeedServerStatus
}

func NewShortFeedHTTPServer(feed *ShortFeedService, assets fs.FS, config ShortFeedHTTPServerConfig) *ShortFeedHTTPServer {
	if config.BindAddress == "" {
		config.BindAddress = "0.0.0.0"
	}
	if config.PortStart == 0 {
		config.PortStart = DefaultShortFeedPortStart
	}
	if config.PortEnd == 0 {
		config.PortEnd = DefaultShortFeedPortEnd
	}
	if config.PortEnd < config.PortStart {
		config.PortEnd = config.PortStart
	}
	return &ShortFeedHTTPServer{
		feed:   feed,
		assets: assets,
		config: config,
		status: ShortFeedServerStatus{
			BindAddress:   config.BindAddress,
			AllowedAccess: "loopback/private-lan/link-local only, no login",
		},
	}
}

func (s *ShortFeedHTTPServer) Start(ctx context.Context) {
	if s == nil {
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
	for port := s.config.PortStart; port <= s.config.PortEnd; port++ {
		addr := net.JoinHostPort(s.config.BindAddress, strconv.Itoa(port))
		listener, listenErr = net.Listen("tcp", addr)
		if listenErr == nil {
			selectedPort = port
			break
		}
	}
	if listener == nil {
		s.status = ShortFeedServerStatus{
			Running:       false,
			BindAddress:   s.config.BindAddress,
			StartupError:  fmt.Sprintf("short feed server failed to listen on ports %d..%d: %v", s.config.PortStart, s.config.PortEnd, listenErr),
			AllowedAccess: "loopback/private-lan/link-local only, no login",
		}
		s.mu.Unlock()
		return
	}

	s.listener = listener
	s.server = &http.Server{Handler: s.Handler()}
	s.status = ShortFeedServerStatus{
		Running:       true,
		BindAddress:   s.config.BindAddress,
		Port:          selectedPort,
		URL:           fmt.Sprintf("http://127.0.0.1:%d/short/", selectedPort),
		LANURLs:       shortFeedLANURLs(selectedPort),
		FallbackUsed:  selectedPort != s.config.PortStart,
		AllowedAccess: "loopback/private-lan/link-local only, no login",
	}
	server := s.server
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Stop(shutdownCtx)
	}()

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.recordStartupError(err)
		}
	}()
}

func (s *ShortFeedHTTPServer) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	server := s.server
	s.server = nil
	s.listener = nil
	if s.status.StartupError == "" {
		s.status.Running = false
	}
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

func (s *ShortFeedHTTPServer) Status() ShortFeedServerStatus {
	if s == nil {
		return ShortFeedServerStatus{AllowedAccess: "loopback/private-lan/link-local only, no login"}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := s.status
	status.LANURLs = append([]string(nil), status.LANURLs...)
	return status
}

func (s *ShortFeedHTTPServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/short", s.handleShortRedirect)
	mux.HandleFunc("/short/", s.handleShortApp)
	mux.Handle("/assets/", http.FileServer(http.FS(s.assets)))
	mux.HandleFunc("/short-api/status", s.handleStatus)
	mux.HandleFunc("/short-api/feed/next", s.handleNext)
	mux.HandleFunc("/short-api/favorites", s.handleFavorites)
	mux.HandleFunc("/short-api/items/", s.handleItemMutation)
	mux.HandleFunc("/short-media/", s.handleMedia)
	mux.HandleFunc("/short-thumb/", s.handleThumbnail)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !shortFeedRemoteAllowed(r.RemoteAddr) {
			writeShortFeedError(w, http.StatusForbidden, "forbidden_source", "short feed only accepts loopback or private LAN requests")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *ShortFeedHTTPServer) recordStartupError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Running = false
	s.status.StartupError = err.Error()
}

func (s *ShortFeedHTTPServer) handleShortRedirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	http.Redirect(w, r, "/short/", http.StatusFound)
}

func (s *ShortFeedHTTPServer) handleShortApp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/short/" {
		http.NotFound(w, r)
		return
	}
	file, err := s.assets.Open("short.html")
	if err != nil {
		writeShortFeedError(w, http.StatusInternalServerError, "short_app_missing", "short feed frontend entry is missing")
		return
	}
	defer file.Close()
	info, _ := file.Stat()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if seeker, ok := file.(io.ReadSeeker); ok && info != nil {
		http.ServeContent(w, r, "short.html", info.ModTime(), seeker)
		return
	}
	_, _ = io.Copy(w, file)
}

func (s *ShortFeedHTTPServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeShortFeedJSON(w, http.StatusOK, s.Status())
}

func (s *ShortFeedHTTPServer) handleNext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	dto, err := s.feed.NextItem(parseShortFeedExcludeRefs(r.URL.Query().Get("exclude")))
	if err != nil {
		status := http.StatusInternalServerError
		code := "next_failed"
		if errors.Is(err, ErrShortFeedNoEligibleVideos) {
			status = http.StatusNotFound
			code = "no_eligible_videos"
		}
		writeShortFeedError(w, status, code, err.Error())
		return
	}
	writeShortFeedJSON(w, http.StatusOK, dto)
}

func (s *ShortFeedHTTPServer) handleFavorites(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	dtos, err := s.feed.FavoriteItems()
	if err != nil {
		writeShortFeedError(w, http.StatusInternalServerError, "favorites_failed", err.Error())
		return
	}
	writeShortFeedJSON(w, http.StatusOK, map[string]interface{}{"items": dtos})
}

func (s *ShortFeedHTTPServer) handleItemMutation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !shortFeedSameOriginMutation(r) {
		writeShortFeedError(w, http.StatusForbidden, "forbidden_origin", "mutation origin must match short feed host")
		return
	}

	ref, action, ok := parseShortFeedItemAction(r.URL.Path)
	if !ok {
		writeShortFeedError(w, http.StatusNotFound, "invalid_item_action", "invalid short feed item action")
		return
	}

	switch action {
	case "play":
		var req ShortFeedPlayRequest
		if !decodeShortFeedMutation(w, r, &req) {
			return
		}
		if req.Source != "short_feed" {
			writeShortFeedError(w, http.StatusBadRequest, "invalid_source", "play source must be short_feed")
			return
		}
		result, err := s.feed.RecordPlayback(ref)
		writeShortFeedMutationResult(w, result, err)
	case "like":
		var req ShortFeedLikeRequest
		if !decodeShortFeedMutation(w, r, &req) {
			return
		}
		result, err := s.feed.SetLiked(ref, req.Liked)
		writeShortFeedMutationResult(w, result, err)
	case "favorite":
		var req ShortFeedFavoriteRequest
		if !decodeShortFeedMutation(w, r, &req) {
			return
		}
		result, err := s.feed.SetFavorited(ref, req.Favorited)
		writeShortFeedMutationResult(w, result, err)
	case "delete":
		var req ShortFeedDeleteRequest
		if !decodeShortFeedMutation(w, r, &req) {
			return
		}
		if !req.ConfirmMoveToTrash {
			writeShortFeedError(w, http.StatusBadRequest, "delete_confirmation_required", "confirm_move_to_trash must be true")
			return
		}
		err := s.feed.DeleteItem(ref)
		if err != nil {
			writeShortFeedMutationResult(w, nil, err)
			return
		}
		writeShortFeedJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		writeShortFeedError(w, http.StatusNotFound, "invalid_item_action", "invalid short feed item action")
	}
}

func (s *ShortFeedHTTPServer) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ref, ok := parseShortFeedMediaPath(r.URL.Path)
	if !ok {
		writeShortFeedError(w, http.StatusBadRequest, "invalid_media_id", "invalid short media reference")
		return
	}
	media, err := s.feed.ResolveMedia(ref)
	if err != nil {
		s.writeMediaError(w, err)
		return
	}
	s.serveMediaFile(w, r, media)
}

// writeMediaError 把服务层错误映射成手机端能理解的状态码：不可用与不存在都收敛
// 成 404，避免把内部原因泄露给局域网。
func (s *ShortFeedHTTPServer) writeMediaError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrShortFeedUnsupportedMedia) ||
		errors.Is(err, ErrShortFeedNoEligibleVideos) ||
		errors.Is(err, ErrImageDecodeUnsupported) ||
		errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, gorm.ErrRecordNotFound) {
		writeShortFeedError(w, http.StatusNotFound, "media_not_found", "short feed media not found")
		return
	}
	writeShortFeedError(w, http.StatusInternalServerError, "media_unavailable", err.Error())
}

// serveMediaFile 统一用 ServeContent 下发，Range/If-Range/Last-Modified 免费获得。
func (s *ShortFeedHTTPServer) serveMediaFile(w http.ResponseWriter, r *http.Request, media *ShortFeedMedia) {
	file, err := os.Open(media.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeShortFeedError(w, http.StatusNotFound, "media_not_found", "short feed media not found")
			return
		}
		writeShortFeedError(w, http.StatusInternalServerError, "media_open_failed", err.Error())
		return
	}
	defer file.Close()
	if media.MIME != "" {
		w.Header().Set("Content-Type", media.MIME)
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, media.DisplayName, media.ModTime, file)
}

// parseShortFeedItemAction 解析 /short-api/items/{kind}/{id}/{action}。
// handleThumbnail 下发缩略图。收藏页与预取用它，避免手机反复拉原图。
func (s *ShortFeedHTTPServer) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ref, ok := parseShortFeedMediaRefPath(r.URL.Path, "/short-thumb/")
	if !ok {
		writeShortFeedError(w, http.StatusBadRequest, "invalid_media_id", "invalid short media reference")
		return
	}
	media, err := s.feed.ResolveThumbnail(ref)
	if err != nil {
		s.writeMediaError(w, err)
		return
	}
	s.serveMediaFile(w, r, media)
}

func parseShortFeedItemAction(path string) (ShortFeedMediaRef, string, bool) {
	trimmed := strings.TrimPrefix(path, "/short-api/items/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) != 3 {
		return ShortFeedMediaRef{}, "", false
	}
	ref, ok := parseShortFeedKindAndID(parts[0], parts[1])
	if !ok {
		return ShortFeedMediaRef{}, "", false
	}
	return ref, parts[2], true
}

// parseShortFeedMediaPath 解析 /short-media/{kind}/{id}。
func parseShortFeedMediaPath(path string) (ShortFeedMediaRef, bool) {
	return parseShortFeedMediaRefPath(path, "/short-media/")
}

func parseShortFeedMediaRefPath(path string, prefix string) (ShortFeedMediaRef, bool) {
	trimmed := strings.TrimPrefix(path, prefix)
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) != 2 {
		return ShortFeedMediaRef{}, false
	}
	return parseShortFeedKindAndID(parts[0], parts[1])
}

func parseShortFeedKindAndID(kindText string, idText string) (ShortFeedMediaRef, bool) {
	kind, ok := ParseShortFeedMediaKind(kindText)
	if !ok {
		return ShortFeedMediaRef{}, false
	}
	id, err := strconv.ParseUint(idText, 10, 64)
	if err != nil || id == 0 {
		return ShortFeedMediaRef{}, false
	}
	return ShortFeedMediaRef{Kind: kind, ID: uint(id)}, true
}

// parseShortFeedExcludeRefs 解析 "video:1,image:2" 形式的排除集；
// 无法识别的条目直接丢弃，不回退成任何默认类型。
func parseShortFeedExcludeRefs(value string) []ShortFeedMediaRef {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	refs := make([]ShortFeedMediaRef, 0, len(parts))
	for _, part := range parts {
		if ref, ok := ParseShortFeedMediaRef(part); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

func decodeShortFeedMutation(w http.ResponseWriter, r *http.Request, target interface{}) bool {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/json" {
		writeShortFeedError(w, http.StatusUnsupportedMediaType, "json_required", "mutation requires application/json")
		return false
	}
	if r.Body == nil || r.ContentLength == 0 {
		writeShortFeedError(w, http.StatusBadRequest, "json_body_required", "mutation requires a JSON object body")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeShortFeedError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeShortFeedError(w, http.StatusBadRequest, "invalid_json", "multiple JSON values are not allowed")
		return false
	}
	return true
}

func writeShortFeedMutationResult(w http.ResponseWriter, result *ShortFeedInteractionDTO, err error) {
	if err == nil {
		writeShortFeedJSON(w, http.StatusOK, result)
		return
	}
	if errors.Is(err, ErrShortFeedNoEligibleVideos) {
		writeShortFeedError(w, http.StatusBadRequest, "not_eligible", err.Error())
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeShortFeedError(w, http.StatusNotFound, "video_not_found", "short feed video not found")
		return
	}
	writeShortFeedError(w, http.StatusInternalServerError, "mutation_failed", err.Error())
}

func writeShortFeedJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeShortFeedError(w http.ResponseWriter, status int, code string, message string) {
	writeShortFeedJSON(w, status, map[string]string{
		"error":   code,
		"message": message,
	})
}

func shortFeedRemoteAllowed(remoteAddr string) bool {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

func shortFeedSameOriginMutation(r *http.Request) bool {
	host := r.Host
	if host == "" {
		return false
	}
	for _, header := range []string{"Origin", "Referer"} {
		raw := strings.TrimSpace(r.Header.Get(header))
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(parsed.Host, host) {
			return false
		}
	}
	return true
}

func shortFeedLANURLs(port int) []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	urls := []string{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			prefix, err := netip.ParsePrefix(addr.String())
			if err != nil {
				continue
			}
			ip := prefix.Addr()
			if ip.Is4() && (ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
				urls = append(urls, fmt.Sprintf("http://%s:%d/short/", ip.String(), port))
			}
		}
	}
	return urls
}
