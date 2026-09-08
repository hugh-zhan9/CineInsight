package services

import (
	"crypto/sha256"
	"golang.org/x/crypto/bcrypt"
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
		if at := strings.IndexByte(header, ' '); at >= 0 {
			header = header[at+1:]
		}
		for _, field := range strings.Split(header, ",") {
			key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
			if ok && strings.EqualFold(key, "Token") {
				return strings.Trim(value, "\"")
			}
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
	session := jellyfinSession{expires: time.Now().Add(30 * 24 * time.Hour), id: hexSessionID(key)}
	s.mu.Lock()
	for key, session := range s.sessions {
		if !time.Now().Before(session.expires) {
			delete(s.sessions, key)
		}
	}
	if generation != s.generation || !s.config.JellyfinEnabled {
		s.mu.Unlock()
		jellyfinError(w, 401, "登录配置已变更")
		return
	}
	if len(s.sessions) >= 64 {
		s.mu.Unlock()
		jellyfinError(w, 429, "登录会话已达上限")
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
