package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// P-021 独立安全评审修复批次的回归测试：限次绕过（C1）、按 ID 写接口的可见边界（I1）、
// 鉴权与路由路径一致性（I2）、Host 校验（I3），以及 Minor 1–8。

func shortFeedRemoteWithPort(ip string, port int) string { return fmt.Sprintf("%s:%d", ip, port) }

func shortFeedLoginStatus(handler http.Handler, pin string, remote string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := shortFeedRequest(http.MethodPost, "/short-api/auth", `{"pin":"`+pin+`"}`, remote)
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestShortFeedClientKeyNormalizesSourcePLAY01(t *testing.T) {
	same := [][2]string{
		{"192.168.1.5:1000", "192.168.1.5:2000"},
		{"[::ffff:192.168.1.5]:1000", "192.168.1.5:2000"},
		{"[2001:db8:1:2::1]:1", "[2001:db8:1:2:ffff:ffff:ffff:ffff]:9"},
		{"[fe80::1%en0]:1", "[fe80::2]:2"},
	}
	for _, pair := range same {
		if a, b := shortFeedClientKey(pair[0]), shortFeedClientKey(pair[1]); a != b {
			t.Fatalf("%s 与 %s 应共享限次键: %q vs %q", pair[0], pair[1], a, b)
		}
	}
	different := [][2]string{
		{"192.168.1.5:1", "192.168.1.6:1"},
		{"[2001:db8:1:2::1]:1", "[2001:db8:1:3::1]:1"},
	}
	for _, pair := range different {
		if a, b := shortFeedClientKey(pair[0]), shortFeedClientKey(pair[1]); a == b {
			t.Fatalf("%s 与 %s 不应共享限次键: %q", pair[0], pair[1], a)
		}
	}
}

func TestShortFeedLockoutSharedAcrossPortsAndIgnoresForwardedForPLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }
	if err := svc.SetShortFeedPIN("2468"); err != nil {
		t.Fatal(err)
	}
	// 同一 IP、每次换端口、每次伪造不同的 X-Forwarded-For：额度仍然共享。
	for attempt := 1; attempt <= 4; attempt++ {
		rec := shortFeedLoginStatus(handler, "0000", shortFeedRemoteWithPort("192.168.1.50", 4000+attempt), func(r *http.Request) {
			r.Header.Set("X-Forwarded-For", fmt.Sprintf("10.9.9.%d", attempt))
			r.Header.Set("X-Real-IP", fmt.Sprintf("10.8.8.%d", attempt))
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误应 401，实际 %d", attempt, rec.Code)
		}
	}
	fifth := shortFeedLoginStatus(handler, "0000", shortFeedRemoteWithPort("192.168.1.50", 4999), func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "10.7.7.7")
	})
	if fifth.Code != http.StatusTooManyRequests {
		t.Fatalf("同 IP 不同端口共 5 次失败应锁定，实际 %d", fifth.Code)
	}
	// 正确 PIN 换个端口、换个 X-Forwarded-For 也不放行。
	rec := shortFeedLoginStatus(handler, "2468", shortFeedRemoteWithPort("192.168.1.50", 5555), func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("锁定期间换端口/伪造头不应放行，实际 %d", rec.Code)
	}
}

func TestShortFeedLockoutSharedWithinIPv6PrefixPLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	if err := svc.SetShortFeedPIN("2468"); err != nil {
		t.Fatal(err)
	}
	// 同一 /64 内轮换地址（隐私地址）共享额度。
	for attempt := 1; attempt <= 4; attempt++ {
		rec := shortFeedLoginStatus(handler, "0000", fmt.Sprintf("[fd00:1:2:3::%x]:4000", attempt), nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误应 401，实际 %d", attempt, rec.Code)
		}
	}
	if rec := shortFeedLoginStatus(handler, "0000", "[fd00:1:2:3:aaaa::1]:4000", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("同 /64 第 5 次失败应锁定，实际 %d", rec.Code)
	}
	if rec := shortFeedLoginStatus(handler, "2468", "[fd00:1:2:3:bbbb::9]:4000", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("同 /64 的另一个地址正确 PIN 也应 429，实际 %d", rec.Code)
	}
	// IPv4-mapped 与 IPv4 是同一来源。
	for attempt := 1; attempt <= 5; attempt++ {
		remote := "192.168.7.7:4000"
		if attempt%2 == 0 {
			remote = "[::ffff:192.168.7.7]:4001"
		}
		shortFeedLoginStatus(handler, "0000", remote, nil)
	}
	if rec := shortFeedLoginStatus(handler, "2468", "192.168.7.7:9", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("IPv4 与其 mapped 形式应共享额度，实际 %d", rec.Code)
	}
}

func TestShortFeedConcurrentWrongPINsAtMostFiveCompareAgainstPLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	if err := svc.SetShortFeedPIN("2468"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	codes := make([]int, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = shortFeedLoginStatus(handler, "0000", shortFeedRemoteWithPort("192.168.1.77", 6000+i), nil).Code
		}(i)
	}
	wg.Wait()
	locked := 0
	for _, code := range codes {
		switch code {
		case http.StatusTooManyRequests:
			locked++
		case http.StatusUnauthorized:
		default:
			t.Fatalf("并发错误请求不应出现 %d", code)
		}
	}
	if locked < 5 {
		t.Fatalf("10 个并发错误请求里至少 5 个应被限次拦下（未进入比对）: %v", codes)
	}
	auth := svc.authState()
	auth.failMu.Lock()
	entry := auth.failures[shortFeedClientKey("192.168.1.77:1")]
	attempts := 0
	if entry != nil {
		attempts = entry.attempts
	}
	auth.failMu.Unlock()
	if attempts != shortFeedAuthMaxFailures {
		t.Fatalf("进入比对的次数应恰为 %d，实际 %d", shortFeedAuthMaxFailures, attempts)
	}
}

func TestShortFeedGlobalFailureBudgetCoolsAllSourcesButKeepsSessionsPLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }
	if err := svc.SetShortFeedPIN("2468"); err != nil {
		t.Fatal(err)
	}
	session := loginShortFeed(t, handler, "2468", "192.168.1.9:1")

	// 20 次失败分散在 5 个不同来源（每个来源只用 4 次，谁也没触发单源锁定）。
	for source := 1; source <= 5; source++ {
		for attempt := 0; attempt < 4; attempt++ {
			rec := shortFeedLoginStatus(handler, "0000", fmt.Sprintf("192.168.2.%d:4000", source), nil)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("来源 %d 第 %d 次应 401，实际 %d", source, attempt+1, rec.Code)
			}
		}
	}
	// 第 21 次（又一个全新来源）触发全局冷却。
	tripped := shortFeedLoginStatus(handler, "0000", "192.168.3.1:4000", nil)
	if tripped.Code != http.StatusTooManyRequests {
		t.Fatalf("超出全局预算应 429，实际 %d", tripped.Code)
	}
	payload := decodeShortFeedBody(t, tripped)
	if payload["code"] != "pin_locked" || payload["retry_after"] != float64(60) {
		t.Fatalf("全局冷却应为 pin_locked + retry_after=60: %v", payload)
	}
	// 另一个从未失败过的 IP，连正确 PIN 也被冷却。
	other := shortFeedLoginStatus(handler, "2468", "192.168.4.4:4000", nil)
	if other.Code != http.StatusTooManyRequests {
		t.Fatalf("全局冷却期间其他 IP 也应 429，实际 %d", other.Code)
	}
	// 已有会话不受影响。
	req := shortFeedRequest(http.MethodGet, "/short-api/status", "", "192.168.1.9:1")
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("全局冷却期间已登录会话应照常 200，实际 %d", rec.Code)
	}
	// 冷却过后可以再登录，且成功登录重置冷却档位。
	clock = clock.Add(61 * time.Second)
	if cookie := loginShortFeed(t, handler, "2468", "192.168.4.4:4000"); cookie.Value == "" {
		t.Fatal("冷却结束后应能登录")
	}
	auth := svc.authState()
	auth.failMu.Lock()
	level := auth.globalLevel
	auth.failMu.Unlock()
	if level != 0 {
		t.Fatalf("成功登录后冷却档位应重置，实际 %d", level)
	}
}

func TestShortFeedGlobalCooldownDoublesUpToCapAndResetsPLAY01(t *testing.T) {
	auth := newShortFeedAuth()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	key := 0
	trip := func() int {
		for i := 0; i < shortFeedGlobalFailureBudget; i++ {
			key++
			if _, ok := auth.beginAttempt(fmt.Sprintf("src-%d", key), now); !ok {
				t.Fatalf("预算内的第 %d 次不应被拒绝", i+1)
			}
		}
		key++
		retry, ok := auth.beginAttempt(fmt.Sprintf("src-%d", key), now)
		if ok {
			t.Fatal("超出预算应被拒绝")
		}
		return retry
	}
	want := []int{60, 120, 240, 480, 900, 900}
	for round, seconds := range want {
		if got := trip(); got != seconds {
			t.Fatalf("第 %d 次冷却应为 %d 秒，实际 %d", round+1, seconds, got)
		}
		now = now.Add(time.Duration(seconds)*time.Second + time.Second)
	}
	auth.succeeded("src-1")
	if got := trip(); got != 60 {
		t.Fatalf("成功登录后冷却应从 60 秒重来，实际 %d", got)
	}
}

func TestShortFeedFailureTableIsBoundedPLAY01(t *testing.T) {
	auth := newShortFeedAuth()
	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for i := 0; i < shortFeedFailureTableCap+500; i++ {
		auth.failMu.Lock()
		auth.globalAttempts = nil // 隔离全局预算，只看容量
		auth.failMu.Unlock()
		if _, ok := auth.beginAttempt(fmt.Sprintf("src-%d", i), base.Add(time.Duration(i)*time.Millisecond)); !ok {
			t.Fatalf("第 %d 个来源不应被拒绝", i)
		}
	}
	auth.failMu.Lock()
	defer auth.failMu.Unlock()
	if len(auth.failures) > shortFeedFailureTableCap {
		t.Fatalf("失败表不应超过 %d 条，实际 %d", shortFeedFailureTableCap, len(auth.failures))
	}
	if _, ok := auth.failures[fmt.Sprintf("src-%d", shortFeedFailureTableCap+499)]; !ok {
		t.Fatal("最新的来源应在表里")
	}
	if _, ok := auth.failures["src-0"]; ok {
		t.Fatal("最旧的来源应被淘汰")
	}
}

func TestShortFeedSessionCheckNotBlockedByFailureTablePLAY01(t *testing.T) {
	auth := newShortFeedAuth()
	now := time.Now()
	token, err := auth.newSession("hash", now)
	if err != nil {
		t.Fatal(err)
	}
	auth.failMu.Lock() // 模拟失败表被登录请求占着
	done := make(chan bool, 1)
	go func() { done <- auth.validSession(token, "hash", now) }()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("会话应有效")
		}
	case <-time.After(time.Second):
		t.Fatal("会话校验被失败表的锁拖住了")
	}
	auth.failMu.Unlock()
}

func TestShortFeedSessionTokensDifferAndExpireAfter30DaysPLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }
	if err := svc.SetShortFeedPIN("2468"); err != nil {
		t.Fatal(err)
	}
	first := loginShortFeed(t, handler, "2468", "192.168.1.1:1")
	second := loginShortFeed(t, handler, "2468", "192.168.1.1:2")
	if first.Value == second.Value {
		t.Fatal("两次登录的令牌必须不同")
	}
	status := func(c *http.Cookie) int {
		req := shortFeedRequest(http.MethodGet, "/short-api/status", "", "192.168.1.1:1")
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	clock = clock.Add(shortFeedSessionTTL - time.Second)
	if status(first) != http.StatusOK {
		t.Fatal("30 天内会话应有效")
	}
	clock = clock.Add(2 * time.Second)
	if status(first) != http.StatusUnauthorized || status(second) != http.StatusUnauthorized {
		t.Fatal("满 30 天后会话必须过期")
	}
}

func TestShortFeedPublicPathIsConservativePLAY01(t *testing.T) {
	newURL := func(target string) *httptest.ResponseRecorder { return nil }
	_ = newURL
	public := []string{"/short", "/short/", "/assets/app.js", "/assets/", "/short-api/auth", "/short-api/auth/status"}
	for _, target := range public {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if !shortFeedPublicPath(req.URL) {
			t.Fatalf("%s 应是公开路径", target)
		}
	}
	notPublic := []string{
		"/assets/%2e%2e/short-api/status",
		"/assets/..%2Fshort-api%2Fstatus",
		"/short%2Fx",
		"/assets/%252e%252e/short-api/status",
		"/assets/%252F",
		"/short-api%2Fauth",
		"/assets/../short-api/status",
		"/assets//app.js",
		"/short//",
		"/assets/./app.js",
		"/short-api/status",
		"/",
	}
	for _, target := range notPublic {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if shortFeedPublicPath(req.URL) {
			t.Fatalf("%s 不应被判为公开路径 (path=%q raw=%q)", target, req.URL.Path, req.URL.RawPath)
		}
	}
}

func TestShortFeedEncodedPathsRequireSessionPLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	if err := svc.SetShortFeedPIN("2468"); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		"/assets/%2e%2e/short-api/status",
		"/assets/..%2Fshort-api%2Fstatus",
		"/short%2F",
		"/assets/%252e%252e/short-api/status",
		"/short-api/auth%2Fstatus",
		"/short-api%2Ffeed/next",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, shortFeedRequest(http.MethodGet, target, "", "127.0.0.1:5000"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 应要求会话（401），实际 %d body=%s", target, rec.Code, rec.Body.String())
		}
	}
}

func TestShortFeedHostHeaderValidationPLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	_ = svc
	do := func(host string, target string) *httptest.ResponseRecorder {
		req := shortFeedRequest(http.MethodGet, target, "", "192.168.1.20:5000")
		req.Host = host
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	for _, host := range []string{
		"evil.example.com:18088", "evil.example.com", "127.0.0.1.evil.com:18088", "8.8.8.8:18088",
		"127.0.0.1:9999", "192.168.1.5:80", "", "[::1", "192.168.1.5:abc", "192.168.1.5:", "fe80::1", "localhost.evil.com:18088",
	} {
		// 公开页面与 API 一律先过 Host 校验。
		for _, target := range []string{"/short/", "/assets/app.js", "/short-api/status", "/short-api/auth/status"} {
			rec := do(host, target)
			if rec.Code != http.StatusMisdirectedRequest {
				t.Fatalf("Host=%q %s 应 421，实际 %d", host, target, rec.Code)
			}
			if payload := decodeShortFeedBody(t, rec); payload["code"] != "invalid_host" {
				t.Fatalf("Host=%q 421 应带 code=invalid_host: %v", host, payload)
			}
		}
	}
	for _, host := range []string{
		"127.0.0.1:18088", "192.168.1.5:18088", "10.1.2.3:18088", "172.16.9.9:18088", "169.254.1.1:18088",
		"localhost:18088", "LOCALHOST:18088", "[::1]:18088", "192.168.1.5",
	} {
		if rec := do(host, "/short-api/status"); rec.Code != http.StatusOK {
			t.Fatalf("Host=%q 应通过，实际 %d body=%s", host, rec.Code, rec.Body.String())
		}
	}
}

func TestShortFeedHostPortMustMatchActualListenPortAndListensIPv4OnlyPLAY01(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("无法监听本机端口: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	setupVideoServiceTestDB(t)
	server := NewShortFeedHTTPServer(NewShortFeedService(&VideoService{}), fstest.MapFS{
		"short.html": &fstest.MapFile{Data: []byte("x"), ModTime: time.Now()},
	}, ShortFeedHTTPServerConfig{BindAddress: "0.0.0.0", PortStart: port, PortEnd: port + 3})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.Start(ctx)
	defer server.Stop(context.Background())
	status := server.Status()
	if !status.Running {
		t.Skipf("端口不可用: %s", status.StartupError)
	}
	if status.Port != port {
		t.Skipf("端口被占用而回退，跳过: %d", status.Port)
	}
	if !strings.HasPrefix(server.listener.Addr().String(), "0.0.0.0:") {
		t.Fatalf("应只监听 IPv4: %s", server.listener.Addr())
	}
	if conn, err := net.DialTimeout("tcp6", fmt.Sprintf("[::1]:%d", port), 500*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("不应能经 IPv6 连上手机端服务")
	}
	handler := server.Handler()
	for host, want := range map[string]int{
		fmt.Sprintf("127.0.0.1:%d", port):   http.StatusOK,
		fmt.Sprintf("127.0.0.1:%d", port+1): http.StatusMisdirectedRequest, // 区间内但不是实际端口
	} {
		req := shortFeedRequest(http.MethodGet, "/short/", "", "127.0.0.1:5000")
		req.Host = host
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("Host=%s 应为 %d，实际 %d", host, want, rec.Code)
		}
	}
}

// I1：按 ID 的写接口必须先过可见边界。黑名单、扫描根之外、失效的条目一律 404，数据库不变。
func TestShortFeedWriteEndpointsRejectInvisibleItemsPLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	svc.SetImageThumbnailService(NewImageThumbnailService(t.TempDir()))
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
	tag := createShortFeedTag(t, "隐藏测试标签")

	hiddenVideos := map[string]models.Video{
		"blacklisted": createShortFeedVideo(t, blocked, "secret-black.mp4", 30, false),
		"out of root": createShortFeedVideo(t, outside, "secret-out.mp4", 30, false),
		"stale":       createShortFeedVideo(t, root, "secret-stale.mp4", 30, true),
	}
	hiddenImage := createShortFeedImage(t, blocked, "secret-image.jpg", "jpg", false)
	staleImage := createShortFeedImage(t, root, "secret-stale.jpg", "jpg", true)

	type action struct{ name, body string }
	videoActions := []action{
		{"delete", `{"confirm_move_to_trash":true}`},
		{"rating", `{"rating":9}`},
		{"watched", `{"watched":true}`},
		{"tag", fmt.Sprintf(`{"tag_id":%d,"attached":true}`, tag.ID)},
	}
	imageActions := []action{videoActions[0], videoActions[1], videoActions[3]}

	post := func(kind string, id uint, act action) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, shortFeedRequest(http.MethodPost, "/short-api/items/"+kind+"/"+strconvUint(id)+"/"+act.name, act.body, "127.0.0.1:5000"))
		return rec
	}
	check := func(label string, rec *httptest.ResponseRecorder, secret string) {
		t.Helper()
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 应 404，实际 %d body=%s", label, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), "secret") {
			t.Fatalf("%s 的 404 响应不得带出隐藏条目的字段: %s", label, rec.Body.String())
		}
	}
	for name, video := range hiddenVideos {
		for _, act := range videoActions {
			check(name+" video "+act.name, post("video", video.ID, act), video.Name)
		}
		var reloaded models.Video
		if err := database.DB.Preload("Tags").First(&reloaded, video.ID).Error; err != nil {
			t.Fatalf("%s: 记录不应被删除: %v", name, err)
		}
		if reloaded.PersonalRating != nil || reloaded.IsWatched || len(reloaded.Tags) != 0 {
			t.Fatalf("%s: 评分/已看/标签不应被改动: %+v", name, reloaded)
		}
		if _, err := os.Stat(video.Path); err != nil {
			t.Fatalf("%s: 文件必须原样保留: %v", name, err)
		}
	}
	for label, image := range map[string]models.Image{"blacklisted": hiddenImage, "stale": staleImage} {
		for _, act := range imageActions {
			check(label+" image "+act.name, post("image", image.ID, act), image.Name)
		}
		var reloaded models.Image
		if err := database.DB.Preload("Tags").First(&reloaded, image.ID).Error; err != nil {
			t.Fatalf("%s 图片记录不应被删除: %v", label, err)
		}
		if reloaded.PersonalRating != nil || len(reloaded.Tags) != 0 {
			t.Fatalf("%s 图片评分/标签不应被改动: %+v", label, reloaded)
		}
		if _, err := os.Stat(image.Path); err != nil {
			t.Fatalf("%s 图片文件必须原样保留: %v", label, err)
		}
	}
	var videoTrash, imageTrash int64
	database.DB.Model(&models.VideoTrashEntry{}).Count(&videoTrash)
	database.DB.Model(&models.ImageTrashEntry{}).Count(&imageTrash)
	if videoTrash != 0 || imageTrash != 0 {
		t.Fatalf("不可见条目不应产生回收站记录: video=%d image=%d", videoTrash, imageTrash)
	}
	// reloadItem 本身也有边界：写入之后条目被隐藏时返回不存在。
	if _, err := svc.reloadItem(videoRef(hiddenVideos["blacklisted"].ID)); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("reloadItem 对不可见条目应返回 ErrRecordNotFound，实际 %v", err)
	}
}

func TestShortFeedRestoreOnlyMobileDeletedVisibleItemsPLAY01(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	post := func(id uint, action string, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, shortFeedRequest(http.MethodPost, "/short-api/items/video/"+strconvUint(id)+"/"+action, body, "127.0.0.1:5000"))
		return rec
	}

	// 桌面端删掉的条目：手机端不能借「撤销」把它恢复出来。
	desktopDeleted := createShortFeedVideo(t, root, "desktop.mp4", 30, false)
	if err := (&VideoService{}).DeleteVideo(desktopDeleted.ID, true); err != nil {
		t.Fatalf("桌面端删除失败: %v", err)
	}
	if rec := post(desktopDeleted.ID, "restore", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("非手机端删除的条目撤销应 404，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	var stillTrashed int64
	database.DB.Model(&models.VideoTrashEntry{}).Where("video_id = ?", desktopDeleted.ID).Count(&stillTrashed)
	if stillTrashed != 1 {
		t.Fatal("桌面端删除的回收站条目不应被手机端撤销动过")
	}
	// 从没存在过的条目同样 404。
	if rec := post(999999, "restore", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的条目撤销应 404，实际 %d", rec.Code)
	}

	// 手机端删除 → 撤销成功。
	mine := createShortFeedVideo(t, root, "mine.mp4", 30, false)
	if rec := post(mine.ID, "delete", `{"confirm_move_to_trash":true}`); rec.Code != http.StatusOK {
		t.Fatalf("手机端删除应成功: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(mine.ID, "restore", ""); rec.Code != http.StatusOK {
		t.Fatalf("撤销本服务经手机端删除的条目应成功: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(mine.Path); err != nil {
		t.Fatalf("撤销后文件应回到原处: %v", err)
	}
	// 撤销过一次就没有第二次。
	if rec := post(mine.ID, "restore", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("重复撤销应 404，实际 %d", rec.Code)
	}

	// 手机端删除之后原目录进了黑名单：撤销不在可见边界内，按不存在处理。
	guarded := createShortFeedVideo(t, blocked, "guarded.mp4", 30, false)
	if rec := post(guarded.ID, "delete", `{"confirm_move_to_trash":true}`); rec.Code != http.StatusOK {
		t.Fatalf("手机端删除应成功: %d %s", rec.Code, rec.Body.String())
	}
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", blocked).Error; err != nil {
		t.Fatal(err)
	}
	if rec := post(guarded.ID, "restore", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("原路径已在黑名单时撤销应 404，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", "").Error; err != nil {
		t.Fatal(err)
	}
	if rec := post(guarded.ID, "restore", ""); rec.Code != http.StatusOK {
		t.Fatalf("取消黑名单后撤销应成功: %d %s", rec.Code, rec.Body.String())
	}
	_ = svc
}

func TestShortFeedRatingValidationReturnsFixedMessagePLAY01(t *testing.T) {
	_, handler, _ := newShortFeedAccessFixture(t)
	root := t.TempDir()
	video := createShortFeedVideo(t, root, "rate.mp4", 30, false)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, shortFeedRequest(http.MethodPost, "/short-api/items/video/"+strconvUint(video.ID)+"/rating", `{"rating":11}`, "127.0.0.1:5000"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("越界评分应 400，实际 %d", rec.Code)
	}
	if payload := decodeShortFeedBody(t, rec); payload["code"] != "invalid_rating" || strings.Contains(rec.Body.String(), "between") {
		t.Fatalf("应返回固定文案的 invalid_rating: %v", payload)
	}
}

// Minor 1：图片侧 liked/favorited 取自 images 表，互动表不再写这两列。
func TestShortFeedImageInteractionStateComesFromImagesTablePLAY02(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})
	svc.SetImageThumbnailService(NewImageThumbnailService(t.TempDir()))
	image := createShortFeedImage(t, root, "shared.jpg", "jpg", false)
	library := NewImageLibraryService()

	if _, err := library.SetImageLiked(image.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := library.SetImageFavorite(image.ID, true); err != nil {
		t.Fatal(err)
	}
	dto, err := svc.RecordPlayback(imageRef(image.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !dto.Liked || !dto.Favorited || dto.FavoritedAt == nil || dto.ViewCount != 1 {
		t.Fatalf("浏览图片的结果应带出桌面端的点赞/收藏与浏览次数: %+v", dto)
	}
	// 手机端点赞/收藏落到 images 表；互动表的 liked/favorited 列保持不动。
	if _, err := svc.SetLiked(imageRef(image.ID), false); err != nil {
		t.Fatal(err)
	}
	dto, err = svc.RecordPlayback(imageRef(image.ID))
	if err != nil || dto.Liked || !dto.Favorited || dto.ViewCount != 2 {
		t.Fatalf("取消点赞后再浏览应显示未点赞: %+v err=%v", dto, err)
	}
	var rows []models.ShortFeedImageInteraction
	if err := database.DB.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Liked || row.Favorited || row.LikedAt != nil || row.FavoritedAt != nil {
			t.Fatalf("图片互动表的 liked/favorited 列不该被写入: %+v", row)
		}
	}
}

// Minor 2：读不到设置行时鉴权路径返回 503，而不是放行。
func TestShortFeedAuthUnavailableWhenSettingsRowMissingPLAY01(t *testing.T) {
	_, handler, _ := newShortFeedAccessFixture(t)
	if err := database.DB.Unscoped().Where("1 = 1").Delete(&models.Settings{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/short-api/status", "/short-api/feed/next", "/short-media/video/1"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, shortFeedRequest(http.MethodGet, target, "", "127.0.0.1:5000"))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s 读不到设置行时应 503，实际 %d", target, rec.Code)
		}
		if payload := decodeShortFeedBody(t, rec); payload["code"] != "auth_unavailable" {
			t.Fatalf("503 应带 auth_unavailable: %v", payload)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, shortFeedRequest(http.MethodGet, "/short-api/auth/status", "", "127.0.0.1:5000"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("auth/status 读不到设置行时应 503，实际 %d", rec.Code)
	}
	if rec := shortFeedLoginStatus(handler, "1234", "127.0.0.1:5000", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("登录读不到设置行时应 503，实际 %d", rec.Code)
	}
}

// Minor 4：访问范围文案如实反映是否设了 PIN。
func TestShortFeedAllowedAccessTextReflectsPINPLAY14(t *testing.T) {
	svc, _, server := newShortFeedAccessFixture(t)
	before := server.Status().AllowedAccess
	if strings.Contains(before, "no login") || !strings.Contains(before, "no PIN") {
		t.Fatalf("未设 PIN 时应如实说明没有 PIN: %q", before)
	}
	if err := svc.SetShortFeedPIN("2468"); err != nil {
		t.Fatal(err)
	}
	after := server.Status().AllowedAccess
	if strings.Contains(after, "no login") || !strings.Contains(after, "PIN login required") {
		t.Fatalf("设了 PIN 后应说明需要 PIN 登录: %q", after)
	}
	if got := (*ShortFeedHTTPServer)(nil).Status().AllowedAccess; strings.Contains(got, "no login") {
		t.Fatalf("nil 服务的状态文案也不得写 no login: %q", got)
	}
}

// Minor 5：二维码地址由服务端决定。
func TestShortFeedPreferredLANURLPLAY14(t *testing.T) {
	_, _, server := newShortFeedAccessFixture(t)
	if got := server.PreferredLANURL(); got != "" {
		t.Fatalf("未运行时没有首选地址: %q", got)
	}
	server.mu.Lock()
	server.status.Running = true
	server.status.LANURLs = nil
	server.mu.Unlock()
	if got := server.PreferredLANURL(); got != "" {
		t.Fatalf("没有局域网地址时应返回空串: %q", got)
	}
	server.mu.Lock()
	server.status.LANURLs = []string{"http://192.168.1.20:18088/short/", "http://10.0.0.5:18088/short/"}
	server.mu.Unlock()
	if got := server.PreferredLANURL(); got != "http://192.168.1.20:18088/short/" {
		t.Fatalf("应取第一个局域网地址: %q", got)
	}
	if got := (*ShortFeedHTTPServer)(nil).PreferredLANURL(); got != "" {
		t.Fatalf("nil 服务应返回空串: %q", got)
	}
}

// Minor 6：HTTP 错误响应不回传底层错误原文；原始错误只进日志且擦掉路径。
func TestShortFeedErrorResponsesDoNotEchoInternalErrorsPLAY01(t *testing.T) {
	secretErr := errors.New(`open /Users/alice/Movies/secret-title.mp4: permission denied`)
	var logBuf bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldWriter)

	check := func(label string, rec *httptest.ResponseRecorder) {
		t.Helper()
		body := rec.Body.String()
		if strings.Contains(body, "/Users") || strings.Contains(body, "secret-title") || strings.Contains(body, "permission denied") {
			t.Fatalf("%s 的响应回传了内部错误: %s", label, body)
		}
		if rec.Code < 400 {
			t.Fatalf("%s 应为错误状态: %d", label, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	writeShortFeedItemResult(rec, nil, secretErr)
	check("item result", rec)
	rec = httptest.NewRecorder()
	writeShortFeedMutationResult(rec, nil, secretErr)
	check("mutation result", rec)
	rec = httptest.NewRecorder()
	(&ShortFeedHTTPServer{}).writeMediaError(rec, secretErr)
	check("media error", rec)
	if !strings.Contains(logBuf.String(), "<path>") || strings.Contains(logBuf.String(), "/Users/alice") {
		t.Fatalf("日志应记录原因但擦掉路径: %q", logBuf.String())
	}

	// 真实链路：next 在数据库故障时不回传原文。
	svc, handler, _ := newShortFeedAccessFixture(t)
	_ = svc
	if err := database.DB.Exec("DROP TABLE videos").Error; err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, shortFeedRequest(http.MethodGet, "/short-api/feed/next", "", "127.0.0.1:5000"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(strings.ToLower(rec.Body.String()), "no such table") {
		t.Fatalf("next 的 500 不应回传数据库错误原文: %d %s", rec.Code, rec.Body.String())
	}
	if payload := decodeShortFeedBody(t, rec); payload["code"] != "next_failed" {
		t.Fatalf("应保留错误码: %v", payload)
	}
}

// Minor 7：判定缓存键含源文件指纹，文件原地替换（大小相同）后不沿用旧结论。
func TestShortFeedMobileMIMECacheInvalidatedByFileReplacementPLAY13(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})
	video := createShortFeedVideo(t, root, "swap.mov", 20, false)
	addShortFeedSnapshot(t, video, "h264", "aac")
	if _, ok := svc.mobileMIMEForVideo(video); !ok {
		t.Fatal("有匹配快照的 h264/aac .mov 应可播")
	}
	// 同大小、不同修改时间的原地替换：快照与文件对不上，旧的「可播」不能沿用。
	replaced := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(video.Path, replaced, replaced); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.mobileMIMEForVideo(video); ok {
		t.Fatal("文件被原地替换后不应沿用缓存里的「可播」结论")
	}
	// 源文件读不到：没有证据，不承诺可播。
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.mobileMIMEForVideo(video); ok {
		t.Fatal("源文件不存在时不应可播")
	}
}
