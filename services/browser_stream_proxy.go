package services

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 本地流代理：让播放器能播那些"必须带请求头才给"的流。
//
// 为什么需要它：这类站点的 CDN 认 Referer，不带就 403。而 IINA 走 iina-cli 时
// 传不进请求头——实测过，用 --mpv-http-header-fields-append 时 IINA 能打开流，
// 但发出去的请求里一个 Referer 都没有，选项被静默忽略了。
//
// 于是换个思路：播放器只播 127.0.0.1 上的地址，请求头的事由代理在取源站时处理。
// 这条路不依赖播放器认不认某个选项，而且 Cookie 这类头也能一起带上——那是
// iina-cli 无论如何都做不到的。
//
// 边界与桥接一致：只绑环回；会话 ID 是随机秘密，猜不到就访问不了；会话会过期。
const (
	streamProxySessionTTL = 6 * time.Hour
	streamProxyMaxSession = 32
	// 取源站的超时。直播流会长时间挂着，所以只限制建立连接与响应头。
	streamProxyResponseTimeout = 30 * time.Second
)

type streamProxySession struct {
	targetURL string
	headers   map[string]string
	// rootName 是代理地址最后那一段的文件名，**必须带一个像样的扩展名**。
	// IINA 是靠扩展名判断"这是不是能播的东西"的：地址结尾是 /root 时它连请求
	// 都不发（实测——代理侧一条访问日志都没有），换成 /index.m3u8 才认。
	rootName  string
	createdAt time.Time
}

// StreamProxy 把"带请求头才能取的流"转成本机可直接播的地址。
type StreamProxy struct {
	mu       sync.Mutex
	sessions map[string]*streamProxySession
	client   *http.Client
	now      func() time.Time
}

func NewStreamProxy() *StreamProxy {
	return &StreamProxy{
		sessions: make(map[string]*streamProxySession),
		client: &http.Client{
			Timeout: 0, // 整体不限时：直播流会一直读下去
			Transport: &http.Transport{
				ResponseHeaderTimeout: streamProxyResponseTimeout,
			},
		},
		now: time.Now,
	}
}

// RootName 返回这条会话的入口文件名，调用方用它拼出给播放器的地址。
func (p *StreamProxy) RootName(id string) string {
	session := p.session(id)
	if session == nil {
		return "index.m3u8"
	}
	return session.rootName
}

// streamProxyRootName 按源地址的扩展名决定入口文件名。
// 认不出来时按 m3u8 处理——这条链路上绝大多数是 HLS。
func streamProxyRootName(targetURL string) string {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return "index.m3u8"
	}
	path := strings.ToLower(parsed.Path)
	dot := strings.LastIndex(path, ".")
	if dot < 0 || dot == len(path)-1 {
		return "index.m3u8"
	}
	ext := path[dot+1:]
	switch ext {
	case "m3u8", "m3u":
		return "index.m3u8"
	case "mp4", "m4v", "mkv", "webm", "mov", "avi", "flv", "ts", "mpd":
		return "media." + ext
	default:
		return "index.m3u8"
	}
}

// CreateSession 登记一条流，返回会话 ID。播放器随后按这个 ID 来取。
func (p *StreamProxy) CreateSession(targetURL string, headers map[string]string) (string, error) {
	if !isProxyableURL(targetURL) {
		return "", fmt.Errorf("只支持 http/https 地址")
	}
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成会话失败: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(raw)

	clean := make(map[string]string, len(headers))
	for name, value := range headers {
		value = strings.TrimSpace(value)
		// 换行会让代理发出多余的头，直接丢掉这一条而不是清洗后照发。
		if value == "" || strings.ContainsAny(value, "\r\n") {
			continue
		}
		clean[name] = value
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
	if len(p.sessions) >= streamProxyMaxSession {
		return "", fmt.Errorf("同时代理的流太多了，等前面的播完再试")
	}
	p.sessions[id] = &streamProxySession{
		targetURL: targetURL,
		headers:   clean,
		rootName:  streamProxyRootName(targetURL),
		createdAt: p.now(),
	}
	return id, nil
}

func (p *StreamProxy) session(id string) *streamProxySession {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
	return p.sessions[id]
}

func (p *StreamProxy) pruneLocked() {
	deadline := p.now().Add(-streamProxySessionTTL)
	for id, session := range p.sessions {
		if session.createdAt.Before(deadline) {
			delete(p.sessions, id)
		}
	}
}

// ServeStream 处理 /bridge/v1/stream/{sessionID}/... 上的请求。
//
// 两种路径：
//
//	.../root        取这条流本身（通常是 m3u8）
//	.../seg?u=<enc> 取播放列表里引用到的地址（分片、密钥、子清单）
func (p *StreamProxy) ServeStream(w http.ResponseWriter, r *http.Request, basePath string) {
	rest := strings.TrimPrefix(r.URL.Path, basePath)
	parts := strings.SplitN(strings.TrimPrefix(rest, "/"), "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	session := p.session(parts[0])
	if session == nil {
		// 会话不存在或已过期。ID 是随机秘密，猜不到，所以这里不必区分两种情况。
		http.Error(w, "stream session not found", http.StatusNotFound)
		return
	}

	target := session.targetURL
	switch {
	case strings.HasPrefix(parts[1], "seg/"):
		// 路径形如 seg/<base64><.ext>。扩展名只是为了让播放器肯收，
		// 解码前先去掉——base64url 里不会出现点号，按最后一个点切是安全的。
		encoded := strings.TrimPrefix(parts[1], "seg/")
		if dot := strings.LastIndex(encoded, "."); dot > 0 {
			encoded = encoded[:dot]
		}
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || !isProxyableURL(string(decoded)) {
			http.Error(w, "bad target", http.StatusBadRequest)
			return
		}
		target = string(decoded)
	default:
		// 其余任何名字都当入口。名字本身不重要，重要的是它带着播放器认得的
		// 扩展名——会话 ID 才是凭据。
	}

	p.forward(w, r, session, target, fmt.Sprintf("%s/%s", basePath, parts[0]))
}

func (p *StreamProxy) forward(w http.ResponseWriter, r *http.Request, session *streamProxySession, target, sessionBase string) {
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		http.Error(w, "bad target", http.StatusBadRequest)
		return
	}
	for name, value := range session.headers {
		request.Header.Set(name, value)
	}
	// 播放器的 Range 请求要透传，不然拖动进度条就废了。
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		request.Header.Set("Range", rangeHeader)
	}

	response, err := p.client.Do(request)
	if err != nil {
		log.Printf("流代理：取源站失败 target=%s err=%v", target, err)
		http.Error(w, fmt.Sprintf("upstream failed: %v", err), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	// 这一行是排查的关键：源站到底回了什么。403 说明请求头还是不够，
	// 404/410 多半是地址过期了，200 才轮得到播放器自己的问题。
	log.Printf("流代理：源站应答 status=%d type=%q target=%s",
		response.StatusCode, response.Header.Get("Content-Type"), target)

	// 播放列表要改写：里面的地址是源站的，播放器直接去取就又没有请求头了。
	if isPlaylistResponse(response, target) {
		body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		if err != nil {
			http.Error(w, "read playlist failed", http.StatusBadGateway)
			return
		}
		rewritten := RewritePlaylistThroughProxy(string(body), target, sessionBase)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write([]byte(rewritten))
		return
	}

	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

// RewritePlaylistThroughProxy 把播放列表里的地址改写成走代理的地址。
//
// 要改的有三类：分片行（不以 # 开头的行）、EXT-X-KEY 的 URI、EXT-X-MAP 的 URI。
// 漏掉任何一类，播放器都会绕过代理直接去源站，于是又变回没有请求头的老样子。
func RewritePlaylistThroughProxy(playlist, baseURL, sessionBase string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return playlist
	}
	proxied := func(raw string) string {
		resolved, err := base.Parse(strings.TrimSpace(raw))
		if err != nil {
			return raw
		}
		encoded := base64.RawURLEncoding.EncodeToString([]byte(resolved.String()))
		// 扩展名必须留在**路径**里，不能只把地址塞进查询串。
		// ffmpeg 的 HLS 解复用器（mpv/IINA 用的就是它）会按 allowed_segment_extensions
		// 检查分片路径的扩展名，路径是 /seg 时它直接拒绝加载：
		//   "URL ... is not in allowed_segment_extensions"
		// 表现就是能打开但放不动、进度条拖不了。
		return fmt.Sprintf("%s/seg/%s%s", sessionBase, encoded, segmentExtension(resolved.Path))
	}

	lines := strings.Split(playlist, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			lines[index] = proxied(trimmed)
			continue
		}
		if strings.HasPrefix(trimmed, "#EXT-X-KEY:") || strings.HasPrefix(trimmed, "#EXT-X-MAP:") ||
			strings.HasPrefix(trimmed, "#EXT-X-MEDIA:") || strings.HasPrefix(trimmed, "#EXT-X-I-FRAME-STREAM-INF:") {
			lines[index] = rewriteURIAttribute(trimmed, proxied)
		}
	}
	return strings.Join(lines, "\n")
}

// rewriteURIAttribute 改写标签里的 URI="..." 属性，其余属性原样保留。
func rewriteURIAttribute(line string, proxied func(string) string) string {
	const marker = `URI="`
	start := strings.Index(line, marker)
	if start < 0 {
		return line
	}
	valueStart := start + len(marker)
	end := strings.Index(line[valueStart:], `"`)
	if end < 0 {
		return line
	}
	original := line[valueStart : valueStart+end]
	return line[:valueStart] + proxied(original) + line[valueStart+end:]
}

// segmentExtension 取源分片路径的扩展名，供改写后的地址沿用。
// 认不出来时给 .ts——HLS 的默认容器，也是 ffmpeg 一定接受的扩展名之一。
func segmentExtension(path string) string {
	dot := strings.LastIndex(path, ".")
	if dot < 0 || dot == len(path)-1 {
		return ".ts"
	}
	ext := strings.ToLower(path[dot:])
	// 只放行播放器认得的那些，别把源站路径里的怪东西原样带进来。
	switch ext {
	case ".ts", ".m4s", ".mp4", ".m4a", ".aac", ".mp3", ".vtt", ".key", ".bin", ".fmp4", ".cmfv", ".cmfa":
		return ext
	default:
		return ".ts"
	}
}

func isPlaylistResponse(response *http.Response, target string) bool {
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "mpegurl") {
		return true
	}
	// 不少站点把 m3u8 当 text/plain 发，只能看路径。
	if parsed, err := url.Parse(target); err == nil {
		path := strings.ToLower(parsed.Path)
		return strings.HasSuffix(path, ".m3u8") || strings.HasSuffix(path, ".m3u")
	}
	return false
}

func isProxyableURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return (scheme == "http" || scheme == "https") && parsed.Host != ""
}
