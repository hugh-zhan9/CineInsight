package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
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
	mux.HandleFunc("/short-api/feed/scopes", s.handleScopes)
	mux.HandleFunc("/short-api/tags", s.handleTags)
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
	dto, err := s.feed.NextItemFiltered(
		parseShortFeedExcludeRefs(r.URL.Query().Get("exclude")),
		r.URL.Query().Get("scope"),
		r.URL.Query().Get("media"),
	)
	if err != nil {
		status := http.StatusInternalServerError
		code := "next_failed"
		if errors.Is(err, ErrShortFeedNoEligibleVideos) {
			status = http.StatusNotFound
			code = "no_eligible_videos"
		}
		if strings.HasPrefix(err.Error(), "不支持的播放范围") {
			status = http.StatusBadRequest
			code = "invalid_scope"
		}
		if errors.Is(err, ErrShortFeedInvalidMediaFilter) {
			status = http.StatusBadRequest
			code = "invalid_media"
		}
		writeShortFeedError(w, status, code, err.Error())
		return
	}
	writeShortFeedJSON(w, http.StatusOK, dto)
}

func (s *ShortFeedHTTPServer) handleScopes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	scopes, err := s.feed.ScopeCountsFiltered(r.URL.Query().Get("media"))
	if err != nil {
		if errors.Is(err, ErrShortFeedInvalidMediaFilter) {
			writeShortFeedError(w, http.StatusBadRequest, "invalid_media", err.Error())
			return
		}
		writeShortFeedError(w, http.StatusInternalServerError, "scopes_failed", err.Error())
		return
	}
	writeShortFeedJSON(w, http.StatusOK, map[string]interface{}{"scopes": scopes})
}

func (s *ShortFeedHTTPServer) handleTags(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tags, err := s.feed.ListFeedTags()
		if err != nil {
			writeShortFeedError(w, http.StatusInternalServerError, "tags_failed", err.Error())
			return
		}
		writeShortFeedJSON(w, http.StatusOK, map[string]interface{}{"tags": tags})
	case http.MethodPost:
		// 新建标签与其他写操作同一套防线：同源校验 + 严格 JSON 体。
		if !shortFeedSameOriginMutation(r) {
			writeShortFeedError(w, http.StatusForbidden, "forbidden_origin", "mutation origin must match short feed host")
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		if !decodeShortFeedMutation(w, r, &body) {
			return
		}
		tag, err := s.feed.CreateFeedTag(body.Name)
		switch {
		case errors.Is(err, ErrShortFeedTagNameRequired):
			writeShortFeedError(w, http.StatusBadRequest, "tag_name_required", "标签名不能为空")
		case errors.Is(err, ErrShortFeedTagNameTooLong):
			writeShortFeedError(w, http.StatusBadRequest, "tag_name_too_long", fmt.Sprintf("标签名最多 %d 个字符", shortFeedTagNameMaxRunes))
		case errors.Is(err, ErrShortFeedTagNameInvalid):
			writeShortFeedError(w, http.StatusBadRequest, "tag_name_invalid", "标签名不能包含控制字符")
		case errors.Is(err, ErrShortFeedAutomaticTag):
			writeShortFeedError(w, http.StatusBadRequest, "automatic_tag", "该名称是系统自动标签，不能手动使用")
		case err != nil:
			// 数据库错误原文不回给局域网客户端。
			log.Printf("[ShortFeed] create tag failed: %v", err)
			writeShortFeedError(w, http.StatusInternalServerError, "tag_create_failed", "创建标签失败")
		default:
			writeShortFeedJSON(w, http.StatusOK, tag)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
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
	case "restore":
		// 撤销刚才那次删除。回收站里每个媒体最多一条记录，按 ref 找就是它。
		if err := s.feed.RestoreDeleted(ref); err != nil {
			writeShortFeedError(w, http.StatusBadRequest, "restore_failed", err.Error())
			return
		}
		writeShortFeedJSON(w, http.StatusOK, map[string]bool{"restored": true})
	case "rating":
		var req ShortFeedRatingRequest
		if !decodeShortFeedMutation(w, r, &req) {
			return
		}
		dto, err := s.feed.SetRating(ref, req.Rating)
		writeShortFeedItemResult(w, dto, err)
	case "watched":
		var req ShortFeedWatchedRequest
		if !decodeShortFeedMutation(w, r, &req) {
			return
		}
		dto, err := s.feed.SetWatched(ref, req.Watched)
		writeShortFeedItemResult(w, dto, err)
	case "tag":
		var req ShortFeedTagRequest
		if !decodeShortFeedMutation(w, r, &req) {
			return
		}
		dto, err := s.feed.SetItemTag(ref, req.TagID, req.Attached)
		writeShortFeedItemResult(w, dto, err)
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

// writeShortFeedItemResult 把改动后的整条 DTO 回给前端，
// 前端据此更新，不必猜写入结果。
func writeShortFeedItemResult(w http.ResponseWriter, dto *ShortFeedItemDTO, err error) {
	if err != nil {
		status := http.StatusInternalServerError
		code := "mutation_failed"
		if errors.Is(err, ErrShortFeedUnsupportedMedia) {
			status = http.StatusBadRequest
			code = "unsupported_media"
		}
		writeShortFeedError(w, status, code, err.Error())
		return
	}
	writeShortFeedJSON(w, http.StatusOK, dto)
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

// lanInterface 是 net.Interface 的可测试快照。
type lanInterface struct {
	Name  string
	Flags net.Flags
	Addrs []netip.Addr
}

// 手机连不上的接口：macOS 的互联网共享/虚拟机网桥（bridge、vmnet）、VPN 隧道
// （utun）、AirDrop 与热点（awdl、llw、ap、anpi）、容器网络（docker、veth、
// tap、tun、vboxnet）。这些地址列出来只会让人挨个去试。
var virtualInterfacePrefixes = []string{
	"anpi", "ap", "awdl", "bridge", "docker", "llw", "tap", "tun", "utun", "vboxnet", "veth", "vmnet",
}

func isVirtualInterfaceName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range virtualInterfacePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// primaryOutboundIPv4 借一次 UDP "连接"问内核默认路由从哪块网卡出去。UDP 不握手，
// 不会真发包，拿不到就返回零值，调用方照常列出其余候选。
func primaryOutboundIPv4() netip.Addr {
	conn, err := net.Dial("udp4", "203.0.113.1:9")
	if err != nil {
		return netip.Addr{}
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}
	}
	ip, ok := netip.AddrFromSlice(addr.IP.To4())
	if !ok {
		return netip.Addr{}
	}
	return ip
}

func collectLANInterfaces() []lanInterface {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	result := make([]lanInterface, 0, len(interfaces))
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		parsed := make([]netip.Addr, 0, len(addrs))
		for _, addr := range addrs {
			prefix, err := netip.ParsePrefix(addr.String())
			if err != nil {
				continue
			}
			parsed = append(parsed, prefix.Addr())
		}
		result = append(result, lanInterface{Name: iface.Name, Flags: iface.Flags, Addrs: parsed})
	}
	return result
}

// buildShortFeedLANURLs 只留手机真正连得上的地址，并把默认路由所在的那块网卡排在最前。
func buildShortFeedLANURLs(interfaces []lanInterface, primary netip.Addr, port int) []string {
	urls := []string{}
	seen := make(map[string]struct{})
	var primaryURL string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		if isVirtualInterfaceName(iface.Name) {
			continue
		}
		for _, ip := range iface.Addrs {
			if !ip.Is4() || !(ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
				continue
			}
			url := fmt.Sprintf("http://%s:%d/short/", ip.String(), port)
			if _, exists := seen[url]; exists {
				continue
			}
			seen[url] = struct{}{}
			if primary.IsValid() && ip == primary {
				primaryURL = url
				continue
			}
			urls = append(urls, url)
		}
	}
	if primaryURL != "" {
		urls = append([]string{primaryURL}, urls...)
	}
	return urls
}

func shortFeedLANURLs(port int) []string {
	return buildShortFeedLANURLs(collectLANInterfaces(), primaryOutboundIPv4(), port)
}
