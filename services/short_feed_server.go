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
	mux.HandleFunc("/short-api/videos/", s.handleVideoMutation)
	mux.HandleFunc("/short-media/", s.handleMedia)

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
	dto, err := s.feed.NextVideo(parseShortFeedExcludeIDs(r.URL.Query().Get("exclude")))
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
	dtos, err := s.feed.FavoriteVideos()
	if err != nil {
		writeShortFeedError(w, http.StatusInternalServerError, "favorites_failed", err.Error())
		return
	}
	writeShortFeedJSON(w, http.StatusOK, map[string]interface{}{"videos": dtos})
}

func (s *ShortFeedHTTPServer) handleVideoMutation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !shortFeedSameOriginMutation(r) {
		writeShortFeedError(w, http.StatusForbidden, "forbidden_origin", "mutation origin must match short feed host")
		return
	}

	videoID, action, ok := parseShortFeedVideoAction(r.URL.Path)
	if !ok {
		writeShortFeedError(w, http.StatusNotFound, "invalid_video_action", "invalid short feed video action")
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
		result, err := s.feed.RecordShortFeedPlayback(videoID)
		writeShortFeedMutationResult(w, result, err)
	case "like":
		var req ShortFeedLikeRequest
		if !decodeShortFeedMutation(w, r, &req) {
			return
		}
		result, err := s.feed.SetLiked(videoID, req.Liked)
		writeShortFeedMutationResult(w, result, err)
	case "favorite":
		var req ShortFeedFavoriteRequest
		if !decodeShortFeedMutation(w, r, &req) {
			return
		}
		result, err := s.feed.SetFavorited(videoID, req.Favorited)
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
		err := s.feed.DeleteVideo(videoID)
		if err != nil {
			writeShortFeedMutationResult(w, nil, err)
			return
		}
		writeShortFeedJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		writeShortFeedError(w, http.StatusNotFound, "invalid_video_action", "invalid short feed video action")
	}
}

func (s *ShortFeedHTTPServer) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	videoIDText := strings.TrimPrefix(r.URL.Path, "/short-media/")
	videoID64, err := strconv.ParseUint(videoIDText, 10, 64)
	if err != nil || videoID64 == 0 {
		writeShortFeedError(w, http.StatusBadRequest, "invalid_media_id", "invalid short media id")
		return
	}
	media, err := s.feed.ResolveMedia(uint(videoID64))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, gorm.ErrRecordNotFound) {
			writeShortFeedError(w, http.StatusNotFound, "media_not_found", "short feed media not found")
			return
		}
		writeShortFeedError(w, http.StatusInternalServerError, "media_unavailable", err.Error())
		return
	}
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
	http.ServeContent(w, r, media.DisplayName, media.ModTime, file)
}

func parseShortFeedVideoAction(path string) (uint, string, bool) {
	trimmed := strings.TrimPrefix(path, "/short-api/videos/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) != 2 {
		return 0, "", false
	}
	id, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || id == 0 {
		return 0, "", false
	}
	return uint(id), parts[1], true
}

func parseShortFeedExcludeIDs(value string) []uint {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	ids := make([]uint, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseUint(strings.TrimSpace(part), 10, 64)
		if err == nil && id > 0 {
			ids = append(ids, uint(id))
		}
	}
	return ids
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
