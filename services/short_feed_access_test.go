package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"video-master/database"
	"video-master/models"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// P-021 手机端访问控制、可见性与体验（后端）的回归测试。
// 测试名里的问题 ID 与问题清单一致：PLAY01 可见性/访问控制/删除、PLAY02 收藏点赞唯一化、
// PLAY13 手机端格式白名单、PLAY14 二维码与端口文案。

func newShortFeedAccessFixture(t *testing.T) (*ShortFeedService, http.Handler, *ShortFeedHTTPServer) {
	t.Helper()
	setupVideoServiceTestDB(t)
	svc := NewShortFeedService(&VideoService{})
	server := NewShortFeedHTTPServer(svc, fstest.MapFS{
		"short.html":       &fstest.MapFile{Data: []byte("<div>short</div>"), ModTime: time.Now()},
		"assets/app.js":    &fstest.MapFile{Data: []byte("console.log(1)"), ModTime: time.Now()},
		"assets/style.css": &fstest.MapFile{Data: []byte("body{}"), ModTime: time.Now()},
	}, ShortFeedHTTPServerConfig{BindAddress: "127.0.0.1", PortStart: 18088, PortEnd: 18088})
	return svc, server.Handler(), server
}

func shortFeedRequest(method string, target string, body string, remote string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = remote
	req.Host = "127.0.0.1:18088"
	return req
}

func decodeShortFeedBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON: %v body=%s", err, rec.Body.String())
	}
	return payload
}

// loginShortFeed 用 PIN 登录并返回会话 Cookie。
func loginShortFeed(t *testing.T, handler http.Handler, pin string, remote string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, shortFeedRequest(http.MethodPost, "/short-api/auth", `{"pin":"`+pin+`"}`, remote))
	if rec.Code != http.StatusOK {
		t.Fatalf("登录应成功: code=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == shortFeedSessionCookie {
			return cookie
		}
	}
	t.Fatalf("登录成功但没有会话 Cookie")
	return nil
}

func TestShortFeedRouteGuardCoversEveryRoutePLAY01(t *testing.T) {
	svc, handler, server := newShortFeedAccessFixture(t)
	root := t.TempDir()
	video := createShortFeedVideo(t, root, "guard.mp4", 30, false)
	if err := svc.SetShortFeedPIN("2468"); err != nil {
		t.Fatalf("设置 PIN 失败: %v", err)
	}

	public := map[string]bool{
		"/short":                 true,
		"/short/":                true,
		"/assets/":               true,
		"/short-api/auth":        true,
		"/short-api/auth/status": true,
	}
	routes := server.routes()
	if len(routes) < 10 {
		t.Fatalf("路由表异常，只有 %d 条", len(routes))
	}
	for _, route := range routes {
		samplePath := route.pattern
		if strings.HasSuffix(samplePath, "/") && route.pattern != "/short/" && route.pattern != "/assets/" {
			// 前缀路由给一个真实存在的样例，避免因为 404 而漏检。
			switch route.pattern {
			case "/short-api/items/":
				samplePath += "video/" + strconvUint(video.ID) + "/like"
			case "/short-media/", "/short-thumb/":
				samplePath += "video/" + strconvUint(video.ID)
			}
		}
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			body := ""
			if method == http.MethodPost {
				body = `{"liked":true}`
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, shortFeedRequest(method, samplePath, body, "127.0.0.1:5000"))
			if public[route.pattern] {
				if rec.Code == http.StatusUnauthorized {
					t.Fatalf("公开路由 %s %s 不该要求 PIN", method, route.pattern)
				}
				continue
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("已设 PIN 且无会话时 %s %s 应返回 401，实际 %d body=%s", method, samplePath, rec.Code, rec.Body.String())
			}
			payload := decodeShortFeedBody(t, rec)
			if payload["code"] != "pin_required" {
				t.Fatalf("%s 的 401 应带 code=pin_required: %v", samplePath, payload)
			}
		}
	}

	// 默认拒绝：不在路由表里的路径（今后新增却忘了登记的路由）也不能绕过。
	for _, extra := range []string{
		"/short-api/brand-new-route",
		"/short-api/",
		"/short-media/video/1/extra",
		"/short-thumb/image/1",
		"/short/../short-api/status",
		"/assets/../short-api/status",
		"/short/x",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, shortFeedRequest(http.MethodGet, extra, "", "127.0.0.1:5000"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 应默认要求会话，实际 %d", extra, rec.Code)
		}
	}

	// 页面壳与静态资源不鉴权（不含数据）。
	for _, target := range []string{"/short/", "/assets/app.js"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, shortFeedRequest(http.MethodGet, target, "", "127.0.0.1:5000"))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 应公开可读，实际 %d", target, rec.Code)
		}
	}

	// 登录后所有数据路由放行。
	cookie := loginShortFeed(t, handler, "2468", "127.0.0.1:5000")
	for _, target := range []string{"/short-api/status", "/short-api/feed/scopes", "/short-api/tags", "/short-api/favorites", "/short-media/video/" + strconvUint(video.ID)} {
		req := shortFeedRequest(http.MethodGet, target, "", "127.0.0.1:5000")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("带会话访问 %s 应放行，实际 %d body=%s", target, rec.Code, rec.Body.String())
		}
	}
}

func TestShortFeedNoPINLeavesRoutesOpenPLAY01(t *testing.T) {
	_, handler, _ := newShortFeedAccessFixture(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, shortFeedRequest(http.MethodGet, "/short-api/status", "", "127.0.0.1:5000"))
	if rec.Code != http.StatusOK {
		t.Fatalf("未设 PIN 应沿用原有放行行为，实际 %d", rec.Code)
	}
	status := httptest.NewRecorder()
	handler.ServeHTTP(status, shortFeedRequest(http.MethodGet, "/short-api/auth/status", "", "127.0.0.1:5000"))
	payload := decodeShortFeedBody(t, status)
	if payload["pin_required"] != false || payload["authenticated"] != true {
		t.Fatalf("未设 PIN 时 auth/status 应报无需登录: %v", payload)
	}
}

func TestShortFeedPINLoginLockoutAndCookiePLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }
	if err := svc.SetShortFeedPIN("2468"); err != nil {
		t.Fatalf("设置 PIN 失败: %v", err)
	}
	// PIN 用 bcrypt 存，库里没有明文。
	hash, _ := svc.pinHash()
	if hash == "" || strings.Contains(hash, "2468") || bcrypt.CompareHashAndPassword([]byte(hash), []byte("2468")) != nil {
		t.Fatalf("库里应存 bcrypt 哈希而不是明文: %q", hash)
	}

	attacker := "192.168.1.50:4000"
	for attempt := 1; attempt <= 4; attempt++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, shortFeedRequest(http.MethodPost, "/short-api/auth", `{"pin":"0000"}`, attacker))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误应返回 401，实际 %d", attempt, rec.Code)
		}
	}
	fifth := httptest.NewRecorder()
	handler.ServeHTTP(fifth, shortFeedRequest(http.MethodPost, "/short-api/auth", `{"pin":"0000"}`, attacker))
	if fifth.Code != http.StatusTooManyRequests {
		t.Fatalf("连续 5 次失败应锁定，实际 %d", fifth.Code)
	}
	locked := decodeShortFeedBody(t, fifth)
	if locked["code"] != "pin_locked" || locked["retry_after"] != float64(60) {
		t.Fatalf("锁定响应应为 pin_locked + retry_after=60: %v", locked)
	}

	// 锁定期间连正确 PIN 也不放行。
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, shortFeedRequest(http.MethodPost, "/short-api/auth", `{"pin":"2468"}`, attacker))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("锁定期间正确 PIN 也应 429，实际 %d", rec.Code)
	}
	// 另一个 IP 不受影响：限次按客户端 IP 计。
	other := loginShortFeed(t, handler, "2468", "192.168.1.60:4000")
	if other.Value == "" {
		t.Fatalf("其他 IP 应能正常登录")
	}

	clock = clock.Add(61 * time.Second)
	cookie := loginShortFeed(t, handler, "2468", attacker)

	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("Cookie 属性不符: %+v", cookie)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(cookie.Value) {
		t.Fatalf("会话令牌应为 32 字节随机数的十六进制: %q", cookie.Value)
	}
}

func TestShortFeedPINChangeRevokesSessionsPLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	if err := svc.SetShortFeedPIN("2468"); err != nil {
		t.Fatal(err)
	}
	cookie := loginShortFeed(t, handler, "2468", "127.0.0.1:5000")
	get := func(c *http.Cookie) int {
		req := shortFeedRequest(http.MethodGet, "/short-api/status", "", "127.0.0.1:5000")
		if c != nil {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if get(cookie) != http.StatusOK {
		t.Fatal("登录后应放行")
	}
	if err := svc.SetShortFeedPIN("1357"); err != nil {
		t.Fatal(err)
	}
	if get(cookie) != http.StatusUnauthorized {
		t.Fatal("PIN 变更后旧会话必须失效")
	}
	fresh := loginShortFeed(t, handler, "1357", "127.0.0.1:5000")
	if get(fresh) != http.StatusOK {
		t.Fatal("新 PIN 登录应放行")
	}
	if err := svc.ClearShortFeedPIN(); err != nil {
		t.Fatal(err)
	}
	if get(nil) != http.StatusOK {
		t.Fatal("清除 PIN 后无需会话")
	}
	// 重新设 PIN 时，此前签发过的会话不会「复活」。
	if err := svc.SetShortFeedPIN("1357"); err != nil {
		t.Fatal(err)
	}
	if get(fresh) != http.StatusUnauthorized {
		t.Fatal("清除后再设 PIN，旧会话不应复活")
	}
}

func TestShortFeedAuthRequestDisciplinePLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	if err := svc.SetShortFeedPIN("2468"); err != nil {
		t.Fatal(err)
	}
	post := func(mutate func(*http.Request), body string) *httptest.ResponseRecorder {
		req := shortFeedRequest(http.MethodPost, "/short-api/auth", body, "127.0.0.1:5000")
		if mutate != nil {
			mutate(req)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, `{"pin":"2468"}`); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("非 JSON 应 415，实际 %d", rec.Code)
	}
	if rec := post(nil, `{"pin":"2468","extra":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("未知字段应 400，实际 %d", rec.Code)
	}
	if rec := post(nil, `{"pin":"2468"}{"pin":"2468"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("多个 JSON 值应 400，实际 %d", rec.Code)
	}
	huge := `{"pin":"` + strings.Repeat("9", 2048) + `"}`
	if rec := post(nil, huge); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超过 1 KiB 应 413，实际 %d", rec.Code)
	}
	if rec := post(func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, `{"pin":"2468"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("跨源登录应 403，实际 %d", rec.Code)
	}
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, shortFeedRequest(http.MethodGet, "/short-api/auth", "", "127.0.0.1:5000"))
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 登录入口应 405，实际 %d", getRec.Code)
	}
}

func TestShortFeedPINValidationAndStatusPLAY01(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewShortFeedService(&VideoService{})
	for _, bad := range []string{"", "123", strings.Repeat("a", 33), "12\x0034", "ab\ncd", "tab\there", strings.Repeat("字", 25)} {
		if err := svc.SetShortFeedPIN(bad); err == nil {
			t.Fatalf("非法 PIN %q 应被拒绝", bad)
		}
	}
	if hash, _ := svc.pinHash(); hash != "" {
		t.Fatalf("被拒绝的 PIN 不应落库")
	}
	if err := svc.SetShortFeedPIN("1234"); err != nil {
		t.Fatalf("4 个字符应合法: %v", err)
	}
	if err := svc.SetShortFeedPIN(strings.Repeat("a", 32)); err != nil {
		t.Fatalf("32 个字符应合法: %v", err)
	}

	status, err := svc.AccessStatus()
	if err != nil || !status.PINSet {
		t.Fatalf("设置后 pin_set 应为 true: %+v err=%v", status, err)
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), "$2") || strings.Contains(string(raw), "hash") {
		t.Fatalf("状态不得下发 PIN 哈希: %s", raw)
	}
	var keys map[string]interface{}
	_ = json.Unmarshal(raw, &keys)
	for _, key := range []string{"enabled", "pin_set", "listening", "url"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("状态缺少字段 %s: %s", key, raw)
		}
	}

	if svc.ShouldStart() {
		t.Fatalf("新库默认关闭，ShouldStart 应为 false")
	}
	if err := svc.SetShortFeedEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !svc.ShouldStart() || !svc.ShortFeedEnabled() {
		t.Fatalf("开启后 ShouldStart 应为 true")
	}
	if err := svc.SetShortFeedEnabled(false); err != nil {
		t.Fatal(err)
	}
	if svc.ShouldStart() {
		t.Fatalf("关闭后 ShouldStart 应为 false")
	}
	if err := svc.ClearShortFeedPIN(); err != nil {
		t.Fatal(err)
	}
	if status, _ = svc.AccessStatus(); status.PINSet {
		t.Fatalf("清除后 pin_set 应为 false")
	}
}

// PLAY-01 主体：黑名单目录里的视频在 feed、收藏页、媒体与缩略图接口里都不可见，
// 且与桌面同一口径（扫描根之外、失效同样不可见）。
func TestShortFeedBlacklistedAndOutOfRootVideosAreHiddenPLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	outside := t.TempDir()
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", blocked).Error; err != nil {
		t.Fatal(err)
	}

	visible := createShortFeedVideo(t, root, "visible.mp4", 30, false)
	hidden := createShortFeedVideo(t, blocked, "hidden.mp4", 30, false)
	outOfRoot := createShortFeedVideo(t, outside, "outside.mp4", 30, false)
	for _, id := range []uint{visible.ID, hidden.ID, outOfRoot.ID} {
		if _, err := svc.SetFavorited(videoRef(id), true); err != nil && id == visible.ID {
			t.Fatalf("可见视频收藏应成功: %v", err)
		}
	}
	// 绕过服务层可见检查，直接把隐藏项标成收藏，模拟「先收藏、后加入黑名单」。
	for _, id := range []uint{hidden.ID, outOfRoot.ID} {
		if err := database.DB.Model(&models.Video{}).Where("id = ?", id).Update("is_favorite", true).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc.invalidateCandidates()

	// feed：反复抽签永远只会是可见那条。
	for i := 0; i < 15; i++ {
		dto, err := svc.NextItem(nil)
		if err != nil {
			t.Fatalf("取下一条失败: %v", err)
		}
		if dto.ID != visible.ID {
			t.Fatalf("feed 不该抽到黑名单/范围外的视频: got=%d", dto.ID)
		}
	}
	counts, err := svc.ScopeCounts()
	if err != nil || counts[0].Count != 1 {
		t.Fatalf("范围计数只应统计可见视频: %+v err=%v", counts, err)
	}

	favorites, err := svc.FavoriteItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(favorites) != 1 || favorites[0].ID != visible.ID {
		t.Fatalf("收藏页只应含可见视频: %+v", favorites)
	}

	// 媒体：可见 200，隐藏 404（服务层给 ErrRecordNotFound）。
	if _, err := svc.ResolveMedia(videoRef(hidden.ID)); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("黑名单视频的 ResolveMedia 应按不存在处理，实际 %v", err)
	}
	if _, err := svc.ResolveMedia(videoRef(outOfRoot.ID)); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("扫描根之外的视频 ResolveMedia 应按不存在处理，实际 %v", err)
	}
	for id, want := range map[uint]int{visible.ID: http.StatusOK, hidden.ID: http.StatusNotFound, outOfRoot.ID: http.StatusNotFound} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, shortFeedRequest(http.MethodGet, "/short-media/video/"+strconvUint(id), "", "127.0.0.1:5000"))
		if rec.Code != want {
			t.Fatalf("GET /short-media/video/%d 应为 %d，实际 %d", id, want, rec.Code)
		}
	}

	// 互动接口同样不可对不可见视频操作。
	if _, err := svc.SetLiked(videoRef(hidden.ID), true); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("对黑名单视频点赞应按不存在处理，实际 %v", err)
	}
	if _, err := svc.RecordPlayback(videoRef(hidden.ID)); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("对黑名单视频记录播放应按不存在处理，实际 %v", err)
	}

	// 取消黑名单后立刻恢复可见（记录本身没动）。
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", "").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveMedia(videoRef(hidden.ID)); err != nil {
		t.Fatalf("取消黑名单后应恢复可见: %v", err)
	}
}

func TestShortFeedBlacklistedImagesAreHiddenFromMediaAndThumbnailPLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	svc.SetImageThumbnailService(NewImageThumbnailService(t.TempDir()))
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", blocked).Error; err != nil {
		t.Fatal(err)
	}
	hidden := createShortFeedImage(t, blocked, "hidden.jpg", "jpg", false)
	for _, prefix := range []string{"/short-media/image/", "/short-thumb/image/"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, shortFeedRequest(http.MethodGet, prefix+strconvUint(hidden.ID), "", "127.0.0.1:5000"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 黑名单图片应 404，实际 %d", prefix, rec.Code)
		}
	}
	if _, err := svc.SetLiked(imageRef(hidden.ID), true); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("对黑名单图片点赞应按不存在处理，实际 %v", err)
	}
}

// 删除遇到「该磁盘不支持废纸篓」时手机端返回 409，不提供永久删除。
func TestShortFeedDeleteUnsupportedTrashReturns409PLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	svc.deleteVideoFn = func(uint, bool) error { return ErrTrashUnsupportedVolume }
	root := t.TempDir()
	video := createShortFeedVideo(t, root, "nodelete.mp4", 30, false)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, shortFeedRequest(http.MethodPost, "/short-api/items/video/"+strconvUint(video.ID)+"/delete", `{"confirm_move_to_trash":true}`, "127.0.0.1:5000"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("应返回 409，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	payload := decodeShortFeedBody(t, rec)
	if payload["code"] != "trash_unsupported" || payload["message"] != "该磁盘不支持废纸篓，请在桌面端处理" {
		t.Fatalf("409 响应不符: %v", payload)
	}
	if _, err := os.Stat(video.Path); err != nil {
		t.Fatalf("不支持废纸篓时文件必须原样保留: %v", err)
	}
	var alive int64
	database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Count(&alive)
	if alive != 1 {
		t.Fatalf("记录不应被删除")
	}
}

func TestShortFeedDesktopLikeAndFavoriteSurviveMobileActivityPLAY02(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := createShortFeedVideo(t, root, "shared.mp4", 30, false)
	other := createShortFeedVideo(t, root, "other.mp4", 30, false)
	svc := NewShortFeedService(&VideoService{})
	desktop := &VideoService{}

	if _, err := desktop.SetVideoLiked(video.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := desktop.SetVideoFavorite(video.ID, true); err != nil {
		t.Fatal(err)
	}
	// 经过一次同步、手机端浏览与别的条目的点赞之后，桌面点赞仍在。
	if _, err := svc.SyncFeedback(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordPlayback(videoRef(video.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetLiked(videoRef(other.ID), true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncFeedback(); err != nil {
		t.Fatal(err)
	}
	if !videoIsLiked(t, video.ID) {
		t.Fatal("桌面点赞不该被手机端同步/浏览清掉")
	}
	dto, err := svc.reloadItem(videoRef(video.ID))
	if err != nil || !dto.Liked || !dto.Favorited {
		t.Fatalf("手机端应看到桌面的点赞与收藏: %+v err=%v", dto, err)
	}

	// 桌面取消收藏，手机端也显示取消，收藏页里消失。
	if _, err := desktop.SetVideoFavorite(video.ID, false); err != nil {
		t.Fatal(err)
	}
	dto, err = svc.reloadItem(videoRef(video.ID))
	if err != nil || dto.Favorited {
		t.Fatalf("桌面取消收藏后手机端应同步取消: %+v err=%v", dto, err)
	}
	items, err := svc.FavoriteItems()
	if err != nil || len(items) != 0 {
		t.Fatalf("收藏页应为空: %+v err=%v", items, err)
	}
	counts, err := svc.ScopeCounts()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range counts {
		if c.Scope == ShortFeedScopeFavorites && c.Count != 0 {
			t.Fatalf("收藏范围计数应为 0: %+v", counts)
		}
	}

	// 手机端收藏 → 桌面可见，且 favorited_at 被维护。
	result, err := svc.SetFavorited(videoRef(video.ID), true)
	if err != nil || !result.Favorited || result.FavoritedAt == nil {
		t.Fatalf("手机收藏结果不符: %+v err=%v", result, err)
	}
	var reloaded models.Video
	if err := database.DB.First(&reloaded, video.ID).Error; err != nil || !reloaded.IsFavorite || reloaded.FavoritedAt == nil {
		t.Fatalf("手机收藏应写入 videos.is_favorite/favorited_at: %+v err=%v", reloaded, err)
	}
	// 互动表的 favorited/liked 列不再读写。
	var interactions []models.ShortFeedInteraction
	if err := database.DB.Find(&interactions).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range interactions {
		if row.Liked || row.Favorited || row.FavoritedAt != nil || row.LikedAt != nil {
			t.Fatalf("互动表的 liked/favorited 列不该被写入: %+v", row)
		}
	}
	// 手机端取消点赞同样落到 videos.is_liked。
	if _, err := svc.SetLiked(videoRef(video.ID), false); err != nil {
		t.Fatal(err)
	}
	if videoIsLiked(t, video.ID) {
		t.Fatal("手机取消点赞应写入 videos.is_liked")
	}
}

func vKey(v models.Video) string { return "video:" + strconvUint(v.ID) }

func TestShortFeedFavoritesOrderByFavoritedAtNullsLastPLAY02(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})
	svc.SetImageThumbnailService(NewImageThumbnailService(t.TempDir()))

	older := createShortFeedVideo(t, root, "older.mp4", 30, false)
	newer := createShortFeedVideo(t, root, "newer.mp4", 30, false)
	legacyA := createShortFeedVideo(t, root, "legacy-a.mp4", 30, false)
	legacyB := createShortFeedVideo(t, root, "legacy-b.mp4", 30, false)
	image := createShortFeedImage(t, root, "photo.jpg", "jpg", false)

	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	setAt := func(model interface{}, id uint, at *time.Time) {
		if err := database.DB.Model(model).Where("id = ?", id).Updates(map[string]interface{}{"is_favorite": true, "favorited_at": at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	older1, newer1, mid := base, base.Add(2*time.Hour), base.Add(time.Hour)
	setAt(&models.Video{}, older.ID, &older1)
	setAt(&models.Video{}, newer.ID, &newer1)
	setAt(&models.Image{}, image.ID, &mid)
	setAt(&models.Video{}, legacyA.ID, nil)
	setAt(&models.Video{}, legacyB.ID, nil)
	// 浏览会刷新互动表的 updated_at，但不该影响收藏页顺序。
	if _, err := svc.RecordPlayback(videoRef(older.ID)); err != nil {
		t.Fatal(err)
	}

	items, err := svc.FavoriteItems()
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.Ref().Key())
	}
	want := []string{
		vKey(newer), // 最近收藏
		"image:" + strconvUint(image.ID),
		vKey(older),
		vKey(legacyB), // NULL 最后，同为 NULL 按 id 倒序
		vKey(legacyA),
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("收藏页顺序错误\n got=%v\nwant=%v", got, want)
	}
}

func TestShortFeedMobileInlineMIMEAcceptsQuickTimeWithSnapshotPLAY13(t *testing.T) {
	video := models.Video{Path: "/x/clip.mov"}
	h264 := &playbackProxySnapshot{HasVideo: true, VideoCodec: "h264", HasAudio: true, AudioCodec: "aac"}
	cases := []struct {
		name     string
		video    models.Video
		snapshot *playbackProxySnapshot
		mime     string
		ok       bool
	}{
		{"mov h264 aac", video, h264, "video/quicktime", true},
		{"mov hevc 无音频", video, &playbackProxySnapshot{HasVideo: true, VideoCodec: "HEVC"}, "video/quicktime", true},
		{"mov prores 不行", video, &playbackProxySnapshot{HasVideo: true, VideoCodec: "prores", HasAudio: true, AudioCodec: "aac"}, "", false},
		{"mov h264 但音频 pcm 不行", video, &playbackProxySnapshot{HasVideo: true, VideoCodec: "h264", HasAudio: true, AudioCodec: "pcm_s16le"}, "", false},
		{"mov 没有快照不承诺", video, nil, "", false},
		{"m4v 走白名单原路径", models.Video{Path: "/x/a.m4v"}, nil, "video/x-m4v", true},
		{"mkv 不认", models.Video{Path: "/x/a.mkv"}, h264, "", false},
		{"mp4 走内嵌白名单", models.Video{Path: "/x/a.mp4"}, nil, "video/mp4", true},
	}
	for _, c := range cases {
		mimeType, ok := mobileInlineMIME(c.video, c.snapshot)
		if mimeType != c.mime || ok != c.ok {
			t.Fatalf("%s: got (%q,%v) want (%q,%v)", c.name, mimeType, ok, c.mime, c.ok)
		}
	}
	// 桌面内嵌白名单一个字都没改：.mov 仍不在其中。
	if _, ok := inlinePreviewMIME("/x/clip.mov"); ok {
		t.Fatal("inlinePreviewMIMEs 不应收入 .mov")
	}
}

func addShortFeedSnapshot(t *testing.T, video models.Video, videoCodec string, audioCodec string) {
	t.Helper()
	fingerprint, err := mediaProbeStat(video.Path)
	if err != nil {
		t.Fatal(err)
	}
	size, mtime, probed := fingerprint.size, fingerprint.modTimeNS, time.Now()
	if err := database.DB.Create(&models.VideoTechnicalMetadata{
		VideoID: video.ID, SuccessfulSourceSize: &size, SuccessfulSourceModTimeNS: &mtime, ProbedAt: &probed,
	}).Error; err != nil {
		t.Fatal(err)
	}
	streams := []models.MediaStream{{VideoID: video.ID, StreamIndex: 0, StreamType: "video", CodecName: videoCodec}}
	if audioCodec != "" {
		streams = append(streams, models.MediaStream{VideoID: video.ID, StreamIndex: 1, StreamType: "audio", CodecName: audioCodec})
	}
	if err := database.DB.Create(&streams).Error; err != nil {
		t.Fatal(err)
	}
}

func TestShortFeedMovPlayableAndUnplayableCountPLAY13(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})

	playable := createShortFeedVideo(t, root, "ok.mov", 20, false)
	addShortFeedSnapshot(t, playable, "h264", "aac")
	prores := createShortFeedVideo(t, root, "prores.mov", 20, false)
	addShortFeedSnapshot(t, prores, "prores", "aac")
	unprobed := createShortFeedVideo(t, root, "unprobed.mov", 20, false)
	mkv := createShortFeedVideo(t, root, "clip.mkv", 20, false)
	watchedMKV := createShortFeedVideo(t, root, "watched.mkv", 20, false)
	if err := database.DB.Model(&models.Video{}).Where("id = ?", watchedMKV.ID).Update("is_watched", true).Error; err != nil {
		t.Fatal(err)
	}
	_ = unprobed
	_ = mkv

	// feed 只会抽到能播的 .mov，DTO 带 quicktime MIME。
	for i := 0; i < 8; i++ {
		dto, err := svc.NextItem(nil)
		if err != nil {
			t.Fatalf("取下一条失败: %v", err)
		}
		if dto.ID != playable.ID || dto.MediaMIME != "video/quicktime" || dto.MediaURL == "" {
			t.Fatalf("应只抽到可播的 .mov: %+v", dto)
		}
	}
	// 下发字节用 quicktime MIME。
	media, err := svc.ResolveMedia(videoRef(playable.ID))
	if err != nil || media.MIME != "video/quicktime" {
		t.Fatalf("ResolveMedia 应下发 video/quicktime: %+v err=%v", media, err)
	}
	// 不可播的仍拒绝。
	if _, err := svc.ResolveMedia(videoRef(prores.ID)); !errors.Is(err, ErrShortFeedNoEligibleVideos) {
		t.Fatalf("prores 应无可播内容，实际 %v", err)
	}

	counts, err := svc.ScopeCounts()
	if err != nil {
		t.Fatal(err)
	}
	byScope := map[string]ShortFeedScopeCount{}
	for _, c := range counts {
		byScope[c.Scope] = c
	}
	if byScope[ShortFeedScopeAll].Count != 1 || byScope[ShortFeedScopeAll].UnplayableCount != 4 {
		t.Fatalf("全部范围应为 1 条可播 + 4 条不可播: %+v", byScope[ShortFeedScopeAll])
	}
	// 「未看」范围里，已看的那条 mkv 不计入不可播。
	if byScope[ShortFeedScopeUnwatched].UnplayableCount != 3 {
		t.Fatalf("未看范围的不可播应为 3: %+v", byScope[ShortFeedScopeUnwatched])
	}
	// 仅图片时不谈视频不可播。
	imageOnly, err := svc.ScopeCountsFiltered(ShortFeedMediaFilterImage)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range imageOnly {
		if c.UnplayableCount != 0 {
			t.Fatalf("仅图片的范围不应有视频不可播计数: %+v", imageOnly)
		}
	}
	raw, _ := json.Marshal(byScope[ShortFeedScopeAll])
	if !strings.Contains(string(raw), `"unplayable_count":4`) {
		t.Fatalf("scopes 应带 unplayable_count 字段: %s", raw)
	}
}

func TestShortFeedQRCodeAndPortMessagePLAY14(t *testing.T) {
	if _, err := ShortFeedQRCodeDataURL("javascript:alert(1)"); err == nil {
		t.Fatal("非 http 地址应被拒绝")
	}
	if _, err := ShortFeedQRCodeDataURL(""); err == nil {
		t.Fatal("空地址应被拒绝")
	}
	dataURL, err := ShortFeedQRCodeDataURL("http://192.168.1.20:18088/short/")
	if err != nil {
		t.Fatalf("生成二维码失败: %v", err)
	}
	if runtime.GOOS == "darwin" && dataURL == "" && cgoEnabledForTest {
		t.Fatal("darwin + cgo 应能生成二维码")
	}
	if dataURL != "" {
		// 支持二维码的平台：必须是能解码的 PNG data URL。
		const prefix = "data:image/png;base64,"
		if !strings.HasPrefix(dataURL, prefix) {
			t.Fatalf("data URL 前缀不符: %.40s", dataURL)
		}
		raw, err := base64.StdEncoding.DecodeString(dataURL[len(prefix):])
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("二维码不是合法 PNG: %v", err)
		}
		if img.Bounds().Dx() < 100 || img.Bounds().Dx() != img.Bounds().Dy() {
			t.Fatalf("二维码尺寸异常: %v", img.Bounds())
		}
	}

	// 端口全被占用时的报错是中文。
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	server := NewShortFeedHTTPServer(NewShortFeedService(&VideoService{}), fstest.MapFS{}, ShortFeedHTTPServerConfig{BindAddress: "127.0.0.1", PortStart: port, PortEnd: port})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.Start(ctx)
	status := server.Status()
	if status.Running || !strings.Contains(status.StartupError, "端口") || strings.Contains(status.StartupError, "failed to listen") {
		t.Fatalf("端口冲突报错应为中文: %+v", status)
	}
}

// 本切片里所有 is_stale 写入都带原因（详细设计 §3.1）。
func TestShortFeedStaleWritesCarryReasonPLAY01(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})
	video := createShortFeedVideo(t, root, "gone.mp4", 20, false)
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveMedia(videoRef(video.ID)); err == nil {
		t.Fatal("文件不存在应报错")
	}
	var stale models.Video
	if err := database.DB.First(&stale, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !stale.IsStale || stale.StaleReason != models.StaleReasonMissingFile {
		t.Fatalf("失效必须带原因: stale=%v reason=%q", stale.IsStale, stale.StaleReason)
	}
}
