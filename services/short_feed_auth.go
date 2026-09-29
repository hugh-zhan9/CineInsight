package services

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"

	"golang.org/x/crypto/bcrypt"
)

// 手机端访问控制（D-PC45，详细设计 §8.6）。
//
// 边界：PIN 与会话属于 ShortFeedService（App 通过它改 PIN，改完必须立刻让全部会话
// 失效），HTTP 层只负责在所有数据路由前面校验。PIN 用 bcrypt 哈希存库，会话令牌是
// 32 字节随机数的十六进制，只在服务端内存里以 sha256 保存，重启应用即全部失效。

const (
	shortFeedSessionCookie = "cineinsight_feed_session"
	shortFeedSessionTTL    = 30 * 24 * time.Hour

	shortFeedPINMinRunes = 4
	shortFeedPINMaxRunes = 32
	// bcrypt 只看前 72 字节；32 个字符按 4 字节 UTF-8 算也是 128 字节，所以另设字节上限，
	// 免得超过 72 字节的后缀被静默忽略而给人错误的安全感。
	shortFeedPINMaxBytes = 72
	shortFeedBcryptCost  = 10

	// 每个客户端 IP 连续失败 5 次锁定 60 秒。
	shortFeedAuthMaxFailures = 5
	shortFeedAuthLockout     = 60 * time.Second
	// 失败计数的滑动窗口：很久以前的零星失败不该和现在的输错叠加成锁定。
	shortFeedAuthFailureWindow = 10 * time.Minute
	// 登录请求体上限：一个 PIN 最多几十字节。
	shortFeedAuthBodyLimit int64 = 1 << 10

	// 全局失败预算：所有来源合计，滚动 1 分钟内最多 20 次进入比对。换源地址（多网卡、
	// IPv6 隐私地址轮换、多台设备）也绕不过这一条。超出后全局冷却 60 秒起逐次翻倍、
	// 上限 15 分钟，一次成功登录后重置。已登录会话不受影响。
	shortFeedGlobalFailureBudget = 20
	shortFeedGlobalWindow        = time.Minute
	shortFeedGlobalCooldownBase  = 60 * time.Second
	shortFeedGlobalCooldownMax   = 15 * time.Minute

	// 失败计数表容量上限：超出时淘汰最旧的条目，表不会被伪造来源撑爆。
	shortFeedFailureTableCap = 1024
)

var (
	ErrShortFeedPINInvalid = errors.New("PIN 需为 4 到 32 个字符，且不能包含控制字符")
	// ErrShortFeedPINTooLong 单独一条：字节数超出 bcrypt 的有效范围。
	ErrShortFeedPINTooLong = errors.New("PIN 过长（最多 72 字节，约 24 个汉字）")
)

// ShortFeedAccessStatus 是设置页读取的手机端访问状态。PIN 哈希永远不下发，
// 前端只靠 pin_set 决定是否显示「建议设置 PIN」。
type ShortFeedAccessStatus struct {
	Enabled   bool   `json:"enabled"`
	PINSet    bool   `json:"pin_set"`
	Listening bool   `json:"listening"`
	URL       string `json:"url"`
}

type shortFeedSession struct {
	expiresAt time.Time
	// pinTag 是建立会话时 PIN 哈希的指纹。哈希一变（无论经由哪条路径）会话立即失效，
	// 不必依赖每个改 PIN 的入口都记得清会话表。
	pinTag string
}

type shortFeedFailure struct {
	attempts    int
	lastAttempt time.Time
	lockedUntil time.Time
}

// shortFeedAuth 的两把锁互不嵌套：mu 只保护会话表，failMu 只保护失败计数与全局预算。
// 会话校验（每个数据请求都走）因此不会被失败表上的登录请求拖慢。
type shortFeedAuth struct {
	mu       sync.Mutex
	sessions map[string]shortFeedSession

	failMu   sync.Mutex
	failures map[string]*shortFeedFailure
	// globalAttempts 是滚动窗口内进入比对的时刻；globalLockedUntil 是全局冷却截止；
	// globalLevel 是已经触发过几次冷却（决定下一次冷却时长）。
	globalAttempts    []time.Time
	globalLockedUntil time.Time
	globalLevel       int
}

func newShortFeedAuth() *shortFeedAuth {
	return &shortFeedAuth{
		sessions: map[string]shortFeedSession{},
		failures: map[string]*shortFeedFailure{},
	}
}

func shortFeedTokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func shortFeedPINTag(hash string) string {
	sum := sha256.Sum256([]byte(hash))
	return hex.EncodeToString(sum[:8])
}

// newSession 建立会话并返回明文令牌。令牌只在这里出现一次，日志与表里都没有它。
func (a *shortFeedAuth) newSession(pinHash string, now time.Time) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成会话令牌失败: %w", err)
	}
	token := hex.EncodeToString(raw)
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, session := range a.sessions {
		if !session.expiresAt.After(now) {
			delete(a.sessions, key)
		}
	}
	a.sessions[shortFeedTokenKey(token)] = shortFeedSession{
		expiresAt: now.Add(shortFeedSessionTTL),
		pinTag:    shortFeedPINTag(pinHash),
	}
	return token, nil
}

func (a *shortFeedAuth) validSession(token string, pinHash string, now time.Time) bool {
	if token == "" {
		return false
	}
	key := shortFeedTokenKey(token)
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[key]
	if !ok {
		return false
	}
	if !session.expiresAt.After(now) {
		delete(a.sessions, key)
		return false
	}
	return subtle.ConstantTimeCompare([]byte(session.pinTag), []byte(shortFeedPINTag(pinHash))) == 1
}

func (a *shortFeedAuth) revokeAll() {
	a.mu.Lock()
	a.sessions = map[string]shortFeedSession{}
	a.mu.Unlock()
}

// globalCooldownLocked 返回全局冷却的剩余秒数；不在冷却中返回 0。调用方持有 failMu。
func (a *shortFeedAuth) globalCooldownLocked(now time.Time) int {
	if a.globalLockedUntil.After(now) {
		return shortFeedRetryAfter(a.globalLockedUntil, now)
	}
	return 0
}

// beginAttempt 在比较 PIN 之前先「预扣」一次机会：并发的猜测请求不能借着「结果还没回来、
// 失败还没记账」绕过上限。key 是归一后的来源键（见 shortFeedClientKey）。
// 依次检查：全局冷却 → 该来源的锁定与次数 → 全局预算。成功登录会清空该来源的计数与全局状态。
func (a *shortFeedAuth) beginAttempt(key string, now time.Time) (retryAfter int, allowed bool) {
	a.failMu.Lock()
	defer a.failMu.Unlock()
	if remaining := a.globalCooldownLocked(now); remaining > 0 {
		return remaining, false
	}
	entry := a.failures[key]
	if entry == nil {
		a.evictFailuresLocked(now)
		entry = &shortFeedFailure{}
		a.failures[key] = entry
	}
	if entry.lockedUntil.After(now) {
		return shortFeedRetryAfter(entry.lockedUntil, now), false
	}
	if !entry.lockedUntil.IsZero() || now.Sub(entry.lastAttempt) > shortFeedAuthFailureWindow {
		entry.attempts = 0
		entry.lockedUntil = time.Time{}
	}
	if entry.attempts >= shortFeedAuthMaxFailures {
		entry.lockedUntil = now.Add(shortFeedAuthLockout)
		return shortFeedRetryAfter(entry.lockedUntil, now), false
	}

	// 全局预算：窗口外的旧记录先丢掉。
	kept := a.globalAttempts[:0]
	for _, at := range a.globalAttempts {
		if now.Sub(at) < shortFeedGlobalWindow {
			kept = append(kept, at)
		}
	}
	a.globalAttempts = kept
	if len(a.globalAttempts) >= shortFeedGlobalFailureBudget {
		cooldown := shortFeedGlobalCooldownBase
		for i := 0; i < a.globalLevel && cooldown < shortFeedGlobalCooldownMax; i++ {
			cooldown *= 2
		}
		if cooldown > shortFeedGlobalCooldownMax {
			cooldown = shortFeedGlobalCooldownMax
		}
		a.globalLevel++
		a.globalLockedUntil = now.Add(cooldown)
		a.globalAttempts = nil
		return shortFeedRetryAfter(a.globalLockedUntil, now), false
	}
	a.globalAttempts = append(a.globalAttempts, now)

	entry.attempts++
	entry.lastAttempt = now
	return 0, true
}

// failedAttempt 记录一次失败结果；返回这次失败是否已经触发该来源的锁定。
func (a *shortFeedAuth) failedAttempt(key string, now time.Time) (locked bool, retryAfter int) {
	a.failMu.Lock()
	defer a.failMu.Unlock()
	entry := a.failures[key]
	if entry == nil {
		return false, 0
	}
	if entry.attempts >= shortFeedAuthMaxFailures && !entry.lockedUntil.After(now) {
		entry.lockedUntil = now.Add(shortFeedAuthLockout)
	}
	if entry.lockedUntil.After(now) {
		return true, shortFeedRetryAfter(entry.lockedUntil, now)
	}
	return false, 0
}

// succeeded 一次成功登录：清掉该来源的计数，并重置全局预算与冷却档位。
func (a *shortFeedAuth) succeeded(key string) {
	a.failMu.Lock()
	delete(a.failures, key)
	a.globalAttempts = nil
	a.globalLockedUntil = time.Time{}
	a.globalLevel = 0
	a.failMu.Unlock()
}

// evictFailuresLocked 在新增条目前保证表未满：先清过期项，仍满则淘汰最旧的（优先淘汰
// 未在锁定中的）。表大小恒不超过 shortFeedFailureTableCap。
func (a *shortFeedAuth) evictFailuresLocked(now time.Time) {
	if len(a.failures) < shortFeedFailureTableCap {
		return
	}
	for key, entry := range a.failures {
		if entry.lockedUntil.After(now) {
			continue
		}
		if now.Sub(entry.lastAttempt) > shortFeedAuthFailureWindow {
			delete(a.failures, key)
		}
	}
	for len(a.failures) >= shortFeedFailureTableCap {
		oldestKey := ""
		var oldest *shortFeedFailure
		oldestLocked := true
		for key, entry := range a.failures {
			locked := entry.lockedUntil.After(now)
			switch {
			case oldest == nil,
				oldestLocked && !locked,
				locked == oldestLocked && entry.lastAttempt.Before(oldest.lastAttempt):
				oldestKey, oldest, oldestLocked = key, entry, locked
			}
		}
		delete(a.failures, oldestKey)
	}
}

func shortFeedRetryAfter(until time.Time, now time.Time) int {
	remaining := until.Sub(now)
	seconds := int((remaining + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

// shortFeedClientKey 把 TCP 对端地址归一成限次键。只信任 TCP 对端：服务前面没有反向代理，
// X-Forwarded-For 之类的头是客户端自己写的。端口不参与（同 IP 不同端口共享额度）；
// IPv4-mapped IPv6 还原成 IPv4；其余 IPv6 按 /64 前缀归并（一台主机能轮换整段 /64）。
func shortFeedClientKey(remoteAddr string) string {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.WithZone("").Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return addr.String()
	}
	return prefix.String()
}

func validateShortFeedPIN(pin string) error {
	if len(pin) > shortFeedPINMaxBytes {
		return ErrShortFeedPINTooLong
	}
	if !utf8.ValidString(pin) {
		return ErrShortFeedPINInvalid
	}
	count := utf8.RuneCountInString(pin)
	if count < shortFeedPINMinRunes || count > shortFeedPINMaxRunes {
		return ErrShortFeedPINInvalid
	}
	for _, r := range pin {
		if unicode.IsControl(r) {
			return ErrShortFeedPINInvalid
		}
	}
	return nil
}

func (s *ShortFeedService) authState() *shortFeedAuth {
	s.authOnce.Do(func() {
		if s.auth == nil {
			s.auth = newShortFeedAuth()
		}
	})
	return s.auth
}

// ShortFeedEnabled 读取手机端开关。读不出来按关闭处理：宁可不开放，也不在读库失败时把
// 内网服务放出去。
func (s *ShortFeedService) ShortFeedEnabled() bool {
	if database.DB == nil {
		return false
	}
	var settings models.Settings
	if err := database.DB.Select("short_feed_enabled").First(&settings).Error; err != nil {
		return false
	}
	return settings.ShortFeedEnabled
}

// ShouldStart 是 startShortFeedServer 入口的判定：只在开关打开时才监听端口。
// resumeAfterDatabaseRestoreFailure 也会调用 startShortFeedServer，所以判定放在启动函数里，
// 不放在某一个调用点（P-029 接线）。
func (s *ShortFeedService) ShouldStart() bool {
	return s.ShortFeedEnabled()
}

// SetShortFeedEnabled 持久化开关。服务的启停由 App 层随后调用。
func (s *ShortFeedService) SetShortFeedEnabled(enabled bool) error {
	return s.updateShortFeedSettings(map[string]interface{}{"short_feed_enabled": enabled})
}

func (s *ShortFeedService) updateShortFeedSettings(values map[string]interface{}) error {
	if database.DB == nil {
		return errors.New("数据库未初始化")
	}
	result := database.DB.Model(&models.Settings{}).Where("1 = 1").Updates(values)
	if result.Error != nil {
		return fmt.Errorf("保存手机端设置失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return errors.New("尚未初始化设置")
	}
	return nil
}

// SetShortFeedPIN 设置 PIN（4–32 字符、无控制字符），bcrypt cost 10；成功后全部会话失效。
func (s *ShortFeedService) SetShortFeedPIN(pin string) error {
	if err := validateShortFeedPIN(pin); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), shortFeedBcryptCost)
	if err != nil {
		return fmt.Errorf("生成 PIN 哈希失败: %w", err)
	}
	if err := s.updateShortFeedSettings(map[string]interface{}{"short_feed_pin_hash": string(hash)}); err != nil {
		return err
	}
	s.authState().revokeAll()
	return nil
}

// ClearShortFeedPIN 清除 PIN；成功后全部会话失效。
func (s *ShortFeedService) ClearShortFeedPIN() error {
	if err := s.updateShortFeedSettings(map[string]interface{}{"short_feed_pin_hash": ""}); err != nil {
		return err
	}
	s.authState().revokeAll()
	return nil
}

func (s *ShortFeedService) pinHash() (string, error) {
	if database.DB == nil {
		return "", errors.New("数据库未初始化")
	}
	var settings models.Settings
	// 读不到设置行（含行不存在）一律报错：鉴权路径据此返回 503，不放行。
	if err := database.DB.Select("short_feed_pin_hash").First(&settings).Error; err != nil {
		return "", err
	}
	return settings.ShortFeedPINHash, nil
}

// AccessStatus 返回开关与 PIN 是否已设。listening 与 url 取决于 HTTP 服务，由 App 层补齐。
func (s *ShortFeedService) AccessStatus() (ShortFeedAccessStatus, error) {
	if database.DB == nil {
		return ShortFeedAccessStatus{}, errors.New("数据库未初始化")
	}
	var settings models.Settings
	if err := database.DB.Select("short_feed_enabled", "short_feed_pin_hash").First(&settings).Error; err != nil {
		return ShortFeedAccessStatus{}, err
	}
	return ShortFeedAccessStatus{Enabled: settings.ShortFeedEnabled, PINSet: settings.ShortFeedPINHash != ""}, nil
}

// shortFeedPublicPath 是不需要会话的路径全集：页面壳与静态资源（不含数据），
// 以及登录两个入口。**默认拒绝**：不在这张表里的路径，包括今后新增的任何路由，
// 在设了 PIN 之后一律要会话。
//
// 保守判定：只有原始路径没有任何编码（RawPath 为空、解码后不含 % 与反斜杠）且已经是规范形态（不含 ..、//、.）
// 时才可能公开；仅允许多一个结尾的 "/"。否则鉴权看到的路径与 mux 最终分派的路径可能
// 不是同一个（%2F、%2e%2e、%252F），一律要求会话。
func shortFeedPublicPath(u *url.URL) bool {
	if u == nil || u.RawPath != "" {
		return false
	}
	p := u.Path
	// 解码后仍残留 % 或反斜杠说明请求里有二次编码（%252F）之类的花样；合法的页面与资源名没有这些。
	if strings.ContainsAny(p, "%\\") {
		return false
	}
	cleaned := path.Clean("/" + p)
	if p != cleaned && p != cleaned+"/" {
		return false
	}
	switch cleaned {
	case "/short", "/short-api/auth", "/short-api/auth/status", "/assets":
		return true
	}
	return strings.HasPrefix(cleaned, "/assets/")
}

// authorize 校验一个请求；返回 false 时已经写好响应。
func (s *ShortFeedHTTPServer) authorize(w http.ResponseWriter, r *http.Request) bool {
	if shortFeedPublicPath(r.URL) {
		return true
	}
	hash, err := s.feed.pinHash()
	if err != nil {
		// 读不出 PIN 状态时不放行。
		w.Header().Set("Cache-Control", "no-store")
		writeShortFeedError(w, http.StatusServiceUnavailable, "auth_unavailable", "暂时无法校验访问权限")
		return false
	}
	if hash == "" {
		return true
	}
	if cookie, err := r.Cookie(shortFeedSessionCookie); err == nil {
		if s.feed.authState().validSession(cookie.Value, hash, s.feed.now()) {
			return true
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeShortFeedError(w, http.StatusUnauthorized, "pin_required", "需要输入 PIN")
	return false
}

// handleAuthStatus 告诉页面「要不要显示 PIN 输入页」。不含任何数据。
func (s *ShortFeedHTTPServer) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	hash, err := s.feed.pinHash()
	if err != nil {
		writeShortFeedError(w, http.StatusServiceUnavailable, "auth_unavailable", "暂时无法校验访问权限")
		return
	}
	authenticated := hash == ""
	if !authenticated {
		if cookie, cookieErr := r.Cookie(shortFeedSessionCookie); cookieErr == nil {
			authenticated = s.feed.authState().validSession(cookie.Value, hash, s.feed.now())
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeShortFeedJSON(w, http.StatusOK, map[string]bool{
		"pin_required":  hash != "",
		"authenticated": authenticated,
	})
}

// handleAuth 用 PIN 换会话 Cookie。
func (s *ShortFeedHTTPServer) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !shortFeedSameOriginMutation(r) {
		writeShortFeedError(w, http.StatusForbidden, "forbidden_origin", "mutation origin must match short feed host")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		PIN string `json:"pin"`
	}
	if !decodeShortFeedMutationLimit(w, r, &body, shortFeedAuthBodyLimit) {
		return
	}
	hash, err := s.feed.pinHash()
	if err != nil {
		writeShortFeedError(w, http.StatusServiceUnavailable, "auth_unavailable", "暂时无法校验访问权限")
		return
	}
	if hash == "" {
		// 没设 PIN：无需登录，也不发会话。
		writeShortFeedJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
		return
	}

	auth := s.feed.authState()
	ip := shortFeedClientKey(r.RemoteAddr)
	now := s.feed.now()
	if retryAfter, allowed := auth.beginAttempt(ip, now); !allowed {
		writeShortFeedPINLocked(w, retryAfter)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.PIN)) != nil {
		if locked, retryAfter := auth.failedAttempt(ip, s.feed.now()); locked {
			writeShortFeedPINLocked(w, retryAfter)
			return
		}
		writeShortFeedError(w, http.StatusUnauthorized, "pin_invalid", "PIN 不正确")
		return
	}
	token, err := auth.newSession(hash, s.feed.now())
	if err != nil {
		// 错误文本不含令牌；也不记 PIN。
		writeShortFeedError(w, http.StatusInternalServerError, "session_failed", "登录失败，请重试")
		return
	}
	auth.succeeded(ip)
	http.SetCookie(w, &http.Cookie{
		Name:     shortFeedSessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(shortFeedSessionTTL / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	writeShortFeedJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func writeShortFeedPINLocked(w http.ResponseWriter, retryAfter int) {
	w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
	writeShortFeedJSON(w, http.StatusTooManyRequests, map[string]interface{}{
		"error":       "pin_locked",
		"code":        "pin_locked",
		"message":     "尝试次数过多，请稍后再试",
		"retry_after": retryAfter,
	})
}
