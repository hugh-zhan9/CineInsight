package services

import (
	"crypto/sha256"
	"errors"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
	"strings"
	"time"
	"video-master/models"
)

func jellyfinToken(r *http.Request) string {
	if token := r.Header.Get("X-Emby-Token"); token != "" {
		return token
	}
	for _, name := range []string{"Authorization", "X-Emby-Authorization"} {
		header := r.Header.Get(name)
		if strings.HasPrefix(strings.ToLower(header), "bearer ") {
			return strings.TrimSpace(header[7:])
		}
		if value, ok := jellyfinHeaderField(header, "Token"); ok {
			return value
		}
	}
	token := ""
	for key, values := range r.URL.Query() {
		if !strings.EqualFold(key, "api_key") && !strings.EqualFold(key, "ApiKey") {
			continue
		}
		for _, value := range values {
			// Two different keys are ambiguous, so neither counts as a credential.
			if token != "" && value != token {
				return ""
			}
			token = value
		}
	}
	return token
}

// jellyfinHeaderField reads one field of a MediaBrowser authorization header value
// (`MediaBrowser Client="…", DeviceId="…", Token="…"`).
func jellyfinHeaderField(header, name string) (string, bool) {
	if at := strings.IndexByte(header, ' '); at >= 0 {
		header = header[at+1:]
	}
	for _, field := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		if ok && strings.EqualFold(key, name) {
			return strings.Trim(value, "\""), true
		}
	}
	return "", false
}

// jellyfinAuthField returns the first non-empty field of that name in either authorization header.
func jellyfinAuthField(r *http.Request, name string) string {
	for _, header := range []string{"Authorization", "X-Emby-Authorization"} {
		value := r.Header.Get(header)
		if strings.HasPrefix(strings.ToLower(value), "bearer ") {
			continue
		}
		if field, ok := jellyfinHeaderField(value, name); ok && strings.TrimSpace(field) != "" {
			return strings.TrimSpace(field)
		}
	}
	return ""
}

func (s *JellyfinServer) login(w http.ResponseWriter, r *http.Request, config models.Settings, generation uint64) {
	var input struct {
		Username string
		Pw       string
	}
	if !jellyfinDecode(w, r, &input) {
		return
	}
	s.mu.Lock()
	if time.Since(s.loginWindow) >= time.Minute {
		s.loginWindow = time.Now()
		s.loginAttempts = 0
	}
	s.loginAttempts++
	limited := s.loginAttempts > 20
	s.mu.Unlock()
	if limited {
		jellyfinError(w, 429, "登录尝试过多，请稍后重试")
		return
	}
	select {
	case s.loginSlot <- struct{}{}:
		defer func() { <-s.loginSlot }()
	default:
		jellyfinError(w, 429, "登录处理中，请稍后重试")
		return
	}
	err := bcrypt.CompareHashAndPassword([]byte(config.JellyfinPasswordHash), []byte(input.Pw))
	if err != nil || input.Username != config.JellyfinUsername {
		jellyfinError(w, 401, "用户名或密码错误")
		return
	}
	token, err := jellyfinRandom(32)
	if err != nil {
		jellyfinError(w, 500, "登录失败")
		return
	}
	key := sha256.Sum256([]byte(token))
	now := s.now()
	s.mu.Lock()
	for key, session := range s.sessions {
		if !now.Before(session.expires) {
			delete(s.sessions, key)
		}
	}
	current := generation == s.generation && s.config.JellyfinEnabled
	s.mu.Unlock()
	if !current {
		jellyfinError(w, 401, "登录配置已变更")
		return
	}
	// 会话先落库再放进内存（D-PC47）：表里只有令牌的哈希，重启后令牌仍然有效。
	deviceID, client := jellyfinClientInfo(r)
	session, err := createJellyfinSession(key, deviceID, client, now)
	if errors.Is(err, errJellyfinSessionLimit) {
		jellyfinError(w, 429, "登录会话已达上限")
		return
	}
	if err != nil {
		log.Printf("[Jellyfin] 登录会话写入失败")
		jellyfinError(w, 500, "登录失败")
		return
	}
	s.mu.Lock()
	if generation != s.generation || !s.config.JellyfinEnabled {
		s.mu.Unlock()
		// 服务在登录途中被停掉或改了配置：这个令牌还没交给客户端，行也不留。
		if err := deleteJellyfinSession(key); err != nil {
			log.Printf("[Jellyfin] 未送达的登录会话清理失败")
		}
		jellyfinError(w, 401, "登录配置已变更")
		return
	}
	s.sessions[key] = session
	s.mu.Unlock()
	jellyfinJSON(w, map[string]interface{}{"User": s.userDTO(config), "AccessToken": token, "ServerId": config.JellyfinServerID, "SessionInfo": map[string]interface{}{"Id": session.id, "UserId": jellyfinUserID, "UserName": config.JellyfinUsername, "ServerId": config.JellyfinServerID, "SupportsMediaControl": false, "PlayState": map[string]interface{}{}}})
}

func hexSessionID(key [32]byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, b := range key[:16] {
		out[2*i] = digits[b>>4]
		out[2*i+1] = digits[b&15]
	}
	return string(out)
}
