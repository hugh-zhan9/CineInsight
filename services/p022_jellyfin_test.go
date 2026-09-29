package services

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// ===== P-022 夹具 =====

// p022Clock 是可并发读写的假时钟（-race 下测试 goroutine 与请求 goroutine 同时读它）。
type p022Clock struct{ nanos atomic.Int64 }

func newP022Clock(at time.Time) *p022Clock {
	c := &p022Clock{}
	c.set(at)
	return c
}
func (c *p022Clock) set(at time.Time)           { c.nanos.Store(at.UnixNano()) }
func (c *p022Clock) now() time.Time             { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *p022Clock) advance(step time.Duration) { c.set(c.now().Add(step)) }

// p022Server 是一个带库、带假时钟的已启用服务（不监听端口），模拟「应用刚启动、内存会话表为空」。
func p022Server(t *testing.T, clock *p022Clock) *JellyfinServer {
	t.Helper()
	s := jellyfinTestServer(t)
	s.now = clock.now
	return s
}

// p022Do 发一个带 MediaBrowser 授权头的请求：客户端名与设备标识只出现在授权头里。
func p022Do(s *JellyfinServer, method, path, token, body, client, device string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.2:12345"
	r.Header.Set("Content-Type", "application/json")
	fields := []string{}
	if client != "" {
		fields = append(fields, fmt.Sprintf("Client=%q", client))
	}
	if device != "" {
		fields = append(fields, fmt.Sprintf("DeviceId=%q", device))
	}
	if token != "" {
		fields = append(fields, fmt.Sprintf("Token=%q", token))
	}
	if len(fields) > 0 {
		r.Header.Set("Authorization", "MediaBrowser "+strings.Join(fields, ", "))
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func p022Login(t *testing.T, s *JellyfinServer, client, device string) string {
	t.Helper()
	w := p022Do(s, "POST", "/Users/AuthenticateByName", "", `{"Username":"viewer","Pw":"test-password"}`, client, device)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	var body struct{ AccessToken string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.AccessToken) != 64 {
		t.Fatalf("login body %s err %v", w.Body, err)
	}
	return body.AccessToken
}

func p022SessionRows(t *testing.T) []models.JellyfinSession {
	t.Helper()
	var rows []models.JellyfinSession
	if err := database.DB.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func p022TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ===== PLAY-14：会话持久化 =====

// 登录只落令牌的 sha256 十六进制；Stop()（退出、维护模式）不作废，新进程（内存为空）按哈希查表后令牌照样有效。
func TestJellyfinPLAY14SessionSurvivesStopAndRestartStoringOnlyHash(t *testing.T) {
	setupVideoServiceTestDB(t)
	clock := newP022Clock(time.Now().UTC().Truncate(time.Second))
	s := p022Server(t, clock)
	token := p022Login(t, s, "Fileball", "device-1")
	rows := p022SessionRows(t)
	if len(rows) != 1 {
		t.Fatalf("session rows %d", len(rows))
	}
	row := rows[0]
	if row.TokenHash != p022TokenHash(token) || len(row.TokenHash) != 64 || row.TokenHash == token {
		t.Fatalf("token_hash %q is not sha256(token)", row.TokenHash)
	}
	if !row.LastSeenAt.Equal(clock.now()) || !row.ExpiresAt.Equal(clock.now().Add(30*24*time.Hour)) {
		t.Fatalf("seen %v expires %v", row.LastSeenAt, row.ExpiresAt)
	}
	if row.Client != "Fileball" || row.DeviceID != "device-1" {
		t.Fatalf("client/device %q %q", row.Client, row.DeviceID)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), row.TokenHash) {
		t.Fatalf("session JSON leaks token material: %s", raw)
	}

	s.Stop()
	if got := len(p022SessionRows(t)); got != 1 {
		t.Fatalf("Stop must not revoke persisted sessions: %d rows", got)
	}
	if w := jellyfinRequest(s, "GET", "/Users/Me", token, ""); w.Code != 503 {
		t.Fatalf("stopped server: %d", w.Code)
	}
	// 同一进程重新开服（进入维护模式后恢复）：内存已清，靠表恢复。
	s.mu.Lock()
	s.config.JellyfinEnabled = true
	s.mu.Unlock()
	if w := jellyfinRequest(s, "GET", "/Users/Me", token, ""); w.Code != 200 {
		t.Fatalf("token after Stop/restart: %d", w.Code)
	}
	// 新进程：全新的服务实例，内存会话表为空。按表取回时沿用表里的 last_seen_at，
	// 不到 10 分钟不重写（重启不等于一次刷新）。
	loggedInAt := clock.now()
	clock.advance(time.Minute)
	restarted := p022Server(t, clock)
	if w := jellyfinRequest(restarted, "GET", "/Users/Me", token, ""); w.Code != 200 {
		t.Fatalf("token after process restart: %d %s", w.Code, w.Body)
	}
	if rows := p022SessionRows(t); !rows[0].LastSeenAt.Equal(loggedInAt) {
		t.Fatalf("restart lookup rewrote last_seen_at: %v", rows[0].LastSeenAt)
	}
	if w := jellyfinRequest(restarted, "GET", "/Users/Me", strings.Repeat("0", 64), ""); w.Code != 401 {
		t.Fatalf("unknown token: %d", w.Code)
	}
}

// 用户在设置里关闭服务、改账号（任何一次成功保存）都删除全部会话；保存失败时一行不删。
func TestJellyfinPLAY14ConfigureRevokesAllPersistedSessions(t *testing.T) {
	setupVideoServiceTestDB(t)
	clock := newP022Clock(time.Now().UTC().Truncate(time.Second))
	s := p022Server(t, clock)
	first := p022Login(t, s, "Fileball", "a")
	second := p022Login(t, s, "Infuse", "b")
	if got := len(p022SessionRows(t)); got != 2 {
		t.Fatalf("rows %d", got)
	}
	if _, err := s.Configure(JellyfinConfigInput{Enabled: false, Username: "viewer"}); err != nil {
		t.Fatal(err)
	}
	if got := len(p022SessionRows(t)); got != 0 {
		t.Fatalf("disabling must delete every session row, %d left", got)
	}
	restarted := p022Server(t, clock)
	for _, token := range []string{first, second} {
		if w := jellyfinRequest(restarted, "GET", "/Users/Me", token, ""); w.Code != 401 {
			t.Fatalf("revoked token accepted after restart: %d", w.Code)
		}
	}

	// 改账号同样作废。
	third := p022Login(t, restarted, "Fileball", "a")
	if _, err := restarted.Configure(JellyfinConfigInput{Enabled: false, Username: "someone-else"}); err != nil {
		t.Fatal(err)
	}
	if got := len(p022SessionRows(t)); got != 0 {
		t.Fatalf("account change must delete sessions, %d left", got)
	}
	again := p022Server(t, clock)
	if w := jellyfinRequest(again, "GET", "/Users/Me", third, ""); w.Code != 401 {
		t.Fatalf("token survived account change: %d", w.Code)
	}

	// 设置写回失败：整个事务回滚，会话保留（与改动前一致）。
	if dbtest.IsPostgres() {
		return
	}
	fourth := p022Login(t, again, "Fileball", "a")
	if err := database.DB.Exec(`CREATE TRIGGER p022_reject_settings BEFORE UPDATE ON settings BEGIN SELECT RAISE(ABORT, 'denied'); END;`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := again.Configure(JellyfinConfigInput{Enabled: false, Username: "viewer"}); err == nil {
		t.Fatal("expected rejected save")
	}
	if got := len(p022SessionRows(t)); got != 1 {
		t.Fatalf("failed save must not delete sessions: %d rows", got)
	}
	if w := jellyfinRequest(p022Server(t, clock), "GET", "/Users/Me", fourth, ""); w.Code != 200 {
		t.Fatalf("session lost on failed save: %d", w.Code)
	}
}

// last_seen_at 连同 expires_at 每 10 分钟最多刷新一次（30 天滑动过期）；表是真值来源：
// 行被删掉后，到下一次刷新时内存副本随之作废。
func TestJellyfinPLAY14LastSeenRefreshIsThrottledAndSliding(t *testing.T) {
	setupVideoServiceTestDB(t)
	base := time.Now().UTC().Truncate(time.Second)
	clock := newP022Clock(base)
	s := p022Server(t, clock)
	token := p022Login(t, s, "", "")
	seen := func() models.JellyfinSession {
		t.Helper()
		rows := p022SessionRows(t)
		if len(rows) != 1 {
			t.Fatalf("rows %d", len(rows))
		}
		return rows[0]
	}
	get := func(server *JellyfinServer, want int) {
		t.Helper()
		if w := jellyfinRequest(server, "GET", "/Users/Me", token, ""); w.Code != want {
			t.Fatalf("at %v: %d, want %d", clock.now().Sub(base), w.Code, want)
		}
	}
	clock.set(base.Add(5 * time.Minute))
	get(s, 200)
	if row := seen(); !row.LastSeenAt.Equal(base) {
		t.Fatalf("refreshed before 10 minutes: %v", row.LastSeenAt)
	}
	clock.set(base.Add(11 * time.Minute))
	get(s, 200)
	refreshed := base.Add(11 * time.Minute)
	if row := seen(); !row.LastSeenAt.Equal(refreshed) || !row.ExpiresAt.Equal(refreshed.Add(30*24*time.Hour)) {
		t.Fatalf("refresh at 11 minutes: seen %v expires %v", row.LastSeenAt, row.ExpiresAt)
	}
	clock.set(base.Add(15 * time.Minute))
	get(s, 200)
	if row := seen(); !row.LastSeenAt.Equal(refreshed) {
		t.Fatalf("second refresh inside 10 minutes: %v", row.LastSeenAt)
	}
	// 滑动：最初的 30 天已过，但 11 分钟时顺延过，仍有效。
	clock.set(base.Add(30*24*time.Hour + time.Minute))
	get(s, 200)
	// 重启后按表里的时间继续滑动，而不是重新计时。
	clock.set(clock.now().Add(30*24*time.Hour + time.Second))
	get(p022Server(t, clock), 401)
	get(s, 401)

	// 表是真值来源：直接删行，下一次需要刷新的请求就失效。
	clock.set(base)
	token = p022Login(t, s, "", "")
	if err := database.DB.Where("1 = 1").Delete(&models.JellyfinSession{}).Error; err != nil {
		t.Fatal(err)
	}
	clock.set(base.Add(11 * time.Minute))
	get(s, 401)
	s.mu.Lock()
	cached := len(s.sessions)
	s.mu.Unlock()
	if cached != 0 {
		t.Fatalf("revoked session still cached: %d", cached)
	}
}

// 注销删表里的行：重启后该令牌不能再用。
func TestJellyfinPLAY14LogoutDeletesPersistedSession(t *testing.T) {
	setupVideoServiceTestDB(t)
	clock := newP022Clock(time.Now().UTC().Truncate(time.Second))
	s := p022Server(t, clock)
	token := p022Login(t, s, "", "")
	other := p022Login(t, s, "", "")
	if w := jellyfinRequest(s, "POST", "/Sessions/Logout", token, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	rows := p022SessionRows(t)
	if len(rows) != 1 || rows[0].TokenHash != p022TokenHash(other) {
		t.Fatalf("logout removed the wrong rows: %+v", rows)
	}
	restarted := p022Server(t, clock)
	if w := jellyfinRequest(restarted, "GET", "/Users/Me", token, ""); w.Code != 401 {
		t.Fatalf("logged-out token after restart: %d", w.Code)
	}
	if w := jellyfinRequest(restarted, "GET", "/Users/Me", other, ""); w.Code != 200 {
		t.Fatalf("other session lost: %d", w.Code)
	}
}

// -race：同一令牌在内存未命中、跨刷新边界时并发请求，同时另一个 goroutine 注销；
// 注销完成后内存里不能再冒出这个会话，表里也不能留行。
func TestJellyfinPLAY14ConcurrentLookupRefreshAndLogout(t *testing.T) {
	setupVideoServiceTestDB(t)
	base := time.Now().UTC().Truncate(time.Second)
	clock := newP022Clock(base)
	token := p022Login(t, p022Server(t, clock), "", "")
	clock.set(base.Add(11 * time.Minute))
	s := p022Server(t, clock)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if w := jellyfinRequest(s, "GET", "/Users/Me", token, ""); w.Code != 200 {
					t.Errorf("concurrent lookup: %d", w.Code)
					return
				}
			}
		}()
	}
	wg.Wait()
	if rows := p022SessionRows(t); len(rows) != 1 || !rows[0].LastSeenAt.Equal(clock.now()) {
		t.Fatalf("concurrent refresh: %+v", rows)
	}

	clock.set(base.Add(30 * time.Minute))
	restarted := p022Server(t, clock)
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 10; j++ {
				jellyfinRequest(restarted, "GET", "/Users/Me", token, "")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		if w := jellyfinRequest(restarted, "POST", "/Sessions/Logout", token, ""); w.Code != 204 && w.Code != 401 {
			t.Errorf("logout: %d", w.Code)
		}
	}()
	close(start)
	wg.Wait()
	if rows := p022SessionRows(t); len(rows) != 0 {
		t.Fatalf("logout left rows: %d", len(rows))
	}
	restarted.mu.Lock()
	cached := len(restarted.sessions)
	restarted.mu.Unlock()
	if cached != 0 {
		t.Fatalf("logged-out session resurrected in memory")
	}
	if w := jellyfinRequest(restarted, "GET", "/Users/Me", token, ""); w.Code != 401 {
		t.Fatalf("token after logout: %d", w.Code)
	}
}

// ===== PLAY-14：诊断 =====

// 诊断只含开关、监听、最近请求时间、最近客户端与最后一条失败（方法 + 脱敏路由形状 + 状态）；
// 不含令牌、密码、搜索词、ID 或文件路径。内网以外的请求不计入。
func TestJellyfinPLAY14DiagnosticsKeepLastFailureWithoutSecrets(t *testing.T) {
	s, token, first, _ := jellyfinLibraryFixture(t)
	clock := newP022Clock(time.Now().UTC().Truncate(time.Second))
	s.now = clock.now
	if diag := s.Diagnostics(); diag.LastRequestAt == nil || diag.LastFailure != nil {
		t.Fatalf("after login: %+v", diag)
	}
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	missing := jellyfinID(jellyVideo, first.ID+100)
	clock.advance(time.Minute)
	failedAt := clock.now()
	if w := p022Do(s, "GET", "/Items/"+missing+"?SearchTerm=secret-term&api_key="+token, token, "", "Fileball", "device-9"); w.Code != 404 {
		t.Fatalf("missing item: %d", w.Code)
	}
	diag := s.Diagnostics()
	status := s.Status()
	if diag.Enabled != status.Enabled || diag.Listening != status.Running || diag.LastClient != "Fileball" {
		t.Fatalf("diagnostics %+v status %+v", diag, status)
	}
	if diag.LastFailure == nil || diag.LastFailure.RouteShape != "GET /items/{id}" || diag.LastFailure.Status != 404 || !diag.LastFailure.At.Equal(failedAt) {
		t.Fatalf("last failure %+v", diag.LastFailure)
	}
	if !strings.Contains(logs.String(), "GET /items/{id} 404") {
		t.Fatalf("failure is a copy of the request log line:\n%s", logs.String())
	}
	// 成功的流不写日志：更新最近请求时间，不覆盖失败记录；不带客户端名的请求不清掉最近客户端。
	clock.advance(time.Minute)
	if w := jellyfinRequest(s, "GET", "/Videos/"+jellyfinID(jellyVideo, first.ID)+"/stream", token, ""); w.Code != 200 {
		t.Fatalf("stream: %d", w.Code)
	}
	diag = s.Diagnostics()
	if !diag.LastRequestAt.Equal(clock.now()) || diag.LastFailure.Status != 404 || diag.LastClient != "Fileball" {
		t.Fatalf("after stream %+v", diag)
	}
	// 客户端名里的换行与控制字符不进诊断。
	p022Do(s, "GET", "/Users/Me", token, "", "Evil\n[Jellyfin] forged", "")
	if diag = s.Diagnostics(); strings.ContainsAny(diag.LastClient, "\n\r") {
		t.Fatalf("client not sanitized: %q", diag.LastClient)
	}
	// 内网以外的请求在边界上就被拒，不计入诊断。
	before := *s.Diagnostics().LastRequestAt
	clock.advance(time.Minute)
	outside := httptest.NewRequest("GET", "/Items/"+missing, nil)
	outside.RemoteAddr = "8.8.8.8:1"
	s.Handler().ServeHTTP(httptest.NewRecorder(), outside)
	if after := s.Diagnostics(); !after.LastRequestAt.Equal(before) {
		t.Fatalf("outside request counted: %v", after.LastRequestAt)
	}
	raw, _ := json.Marshal(s.Diagnostics())
	for _, leak := range []string{token, p022TokenHash(token), "secret-term", "test-password", missing, first.Directory, jellyfinUserID, "device-9"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("diagnostics leak %q: %s", leak, raw)
		}
	}
	for _, key := range []string{`"enabled"`, `"listening"`, `"last_request_at"`, `"last_client"`, `"last_failure"`, `"route_shape"`, `"status"`, `"at"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("diagnostics JSON lacks %s: %s", key, raw)
		}
	}
}

// ===== PLAY-07：jellyfin_view 有效观看 =====

func p022Progress(t *testing.T, s *JellyfinServer, token, endpoint string, videoID uint, seconds float64, playSession, device string) {
	t.Helper()
	body := fmt.Sprintf(`{"ItemId":%q,"PositionTicks":%d`, jellyfinID(jellyVideo, videoID), int64(seconds*1e7))
	if playSession != "" {
		body += fmt.Sprintf(`,"PlaySessionId":%q`, playSession)
	}
	body += "}"
	if w := p022Do(s, "POST", "/Sessions/"+endpoint, token, body, "Fileball", device); w.Code != 204 {
		t.Fatalf("%s: %d %s", endpoint, w.Code, w.Body)
	}
}

func p022ViewEvents(t *testing.T, videoID uint) int {
	t.Helper()
	var events []models.PlayEvent
	if err := database.DB.Where("video_id = ?", videoID).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Source != PlayEventSourceJellyfinView {
			t.Fatalf("unexpected ledger source %q", event.Source)
		}
	}
	return len(events)
}

// 同一播放会话累计播放首次越过 viewThreshold（时长 100 → 50 秒）或看完时记一条；暂停与拖动不算播放，
// 开始上报只立起点；PlaySessionId 缺失时按条目 + 设备去重。
func TestJellyfinPLAY07ViewEventOncePerPlaySessionAfterThresholdOrCompletion(t *testing.T) {
	useFreshViewEventDedup(t, viewEventDedupCapacity)
	s, token, first, second := jellyfinLibraryFixture(t)
	clock := newP022Clock(time.Now().UTC().Truncate(time.Second))
	s.now = clock.now
	step := func(d time.Duration) { clock.advance(d) }

	// 会话 ps-1：0 → 10（10s）→ 40（30s）→ 暂停 30s → 拖到 80（5s）→ 84（4s）：累计 49，未到 50。
	p022Progress(t, s, token, "Playing", first.ID, 0, "ps-1", "dev-a")
	for _, report := range []struct {
		wall     time.Duration
		position float64
	}{{10 * time.Second, 10}, {30 * time.Second, 40}, {30 * time.Second, 40}, {5 * time.Second, 80}, {4 * time.Second, 84}} {
		step(report.wall)
		p022Progress(t, s, token, "Playing/Progress", first.ID, report.position, "ps-1", "dev-a")
	}
	if got := p022ViewEvents(t, first.ID); got != 0 {
		t.Fatalf("49 played seconds already recorded a view: %d", got)
	}
	step(2 * time.Second)
	p022Progress(t, s, token, "Playing/Progress", first.ID, 86, "ps-1", "dev-a")
	if got := p022ViewEvents(t, first.ID); got != 1 {
		t.Fatalf("crossing the threshold: %d events", got)
	}
	step(4 * time.Second)
	p022Progress(t, s, token, "Playing/Progress", first.ID, 90, "ps-1", "dev-a")
	step(time.Second)
	p022Progress(t, s, token, "Playing/Stopped", first.ID, 91, "ps-1", "dev-a")
	if got := p022ViewEvents(t, first.ID); got != 1 {
		t.Fatalf("same session recorded twice: %d", got)
	}

	// 会话 ps-2：从 90 开始、1 秒后停在 99（片尾区间）→ 看完即记。
	p022Progress(t, s, token, "Playing", first.ID, 90, "ps-2", "dev-a")
	step(time.Second)
	p022Progress(t, s, token, "Playing/Stopped", first.ID, 99, "ps-2", "dev-a")
	if got := p022ViewEvents(t, first.ID); got != 2 {
		t.Fatalf("completion did not record: %d", got)
	}

	// 只有开始上报（哪怕位置在片尾）不记；只暂停、只拖动也不记。
	p022Progress(t, s, token, "Playing", second.ID, 97, "ps-3", "dev-a")
	p022Progress(t, s, token, "Playing", second.ID, 10, "ps-4", "dev-a")
	for i := 0; i < 3; i++ {
		step(10 * time.Minute)
		p022Progress(t, s, token, "Playing/Progress", second.ID, 10, "ps-4", "dev-a")
	}
	p022Progress(t, s, token, "Playing", second.ID, 0, "ps-5", "dev-a")
	step(2 * time.Second)
	p022Progress(t, s, token, "Playing/Progress", second.ID, 90, "ps-5", "dev-a")
	if got := p022ViewEvents(t, second.ID); got != 0 {
		t.Fatalf("start-only / paused / seek-only sessions recorded: %d", got)
	}

	// 没有 PlaySessionId：同一设备同一条目只记一次，另一台设备另记。
	p022Progress(t, s, token, "Playing/Progress", second.ID, 5, "", "dev-a")
	step(60 * time.Second)
	p022Progress(t, s, token, "Playing/Progress", second.ID, 65, "", "dev-a")
	p022Progress(t, s, token, "Playing/Stopped", second.ID, 65, "", "dev-a")
	p022Progress(t, s, token, "Playing/Progress", second.ID, 5, "", "dev-a")
	step(60 * time.Second)
	p022Progress(t, s, token, "Playing/Progress", second.ID, 65, "", "dev-a")
	if got := p022ViewEvents(t, second.ID); got != 1 {
		t.Fatalf("item+device fallback: %d events", got)
	}
	p022Progress(t, s, token, "Playing/Progress", second.ID, 5, "", "dev-b")
	step(60 * time.Second)
	p022Progress(t, s, token, "Playing/Progress", second.ID, 65, "", "dev-b")
	if got := p022ViewEvents(t, second.ID); got != 2 {
		t.Fatalf("another device: %d events", got)
	}

	// 只追加账本：计数列不动。
	for _, id := range []uint{first.ID, second.ID} {
		var video models.Video
		database.DB.First(&video, id)
		if video.PlayCount != 0 || video.RandomPlayCount != 0 {
			t.Fatalf("view events changed counters: %+v", video)
		}
	}
}

// ===== PLAY-09：观看口径与片库一致 =====

// UserData 的续播位置只在 resumable 时如实报告；LastPlayedDate = max(last_played_at, watch_progress_updated_at)。
func TestJellyfinPLAY09UserDataFollowsResumable(t *testing.T) {
	at := func(hours int) *time.Time {
		value := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC).Add(time.Duration(hours) * time.Hour)
		return &value
	}
	cases := []struct {
		name   string
		video  models.Video
		ticks  int64
		played *time.Time
	}{
		{"in progress", models.Video{WatchPositionSeconds: 30, WatchProgressUpdatedAt: at(3), LastPlayedAt: at(1)}, 30e7, at(3)},
		{"rewatch after watched", models.Video{IsWatched: true, WatchedAt: at(2), WatchPositionSeconds: 40, WatchProgressUpdatedAt: at(4)}, 40e7, at(4)},
		{"breakpoint older than watched", models.Video{IsWatched: true, WatchedAt: at(4), WatchPositionSeconds: 40, WatchProgressUpdatedAt: at(2), LastPlayedAt: at(5)}, 0, at(5)},
		{"watched without progress time", models.Video{IsWatched: true, WatchedAt: at(4), WatchPositionSeconds: 40}, 0, nil},
		{"legacy watched without watched_at", models.Video{IsWatched: true, WatchPositionSeconds: 12}, 12e7, nil},
		{"no breakpoint", models.Video{LastPlayedAt: at(6)}, 0, at(6)},
	}
	for _, c := range cases {
		data := jellyfinUserData(c.video)
		if ticks, _ := data["PlaybackPositionTicks"].(int64); ticks != c.ticks {
			t.Errorf("%s: ticks %d want %d", c.name, ticks, c.ticks)
		}
		if resumable(&c.video) != (c.ticks > 0) {
			t.Errorf("%s: ticks disagree with resumable", c.name)
		}
		got, ok := data["LastPlayedDate"].(string)
		if c.played == nil {
			if ok {
				t.Errorf("%s: unexpected LastPlayedDate %s", c.name, got)
			}
		} else if got != c.played.Format(time.RFC3339) {
			t.Errorf("%s: LastPlayedDate %q want %s", c.name, got, c.played.Format(time.RFC3339))
		}
	}
}

// IsResumable、Filters=IsResumable、Items/Resume 与「继续观看」视图：集合是 resumableSQL，顺序与片库
// 「继续观看」（ListContinueWatchingWithFilter）一致；IsResumable=false 是它的补集（含 NULL 进度时间的行）。
// SortBy=DatePlayed 按 max(last_played_at, watch_progress_updated_at)，从未播放的排在最旧一端。
func TestJellyfinPLAY09ResumableSetOrderAndDatePlayedMatchLibrary(t *testing.T) {
	s, token, first, second := jellyfinLibraryFixture(t)
	root := first.Directory
	base := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	at := func(hours int) time.Time { return base.Add(time.Duration(hours) * time.Hour) }
	videos := map[string]models.Video{"A": first, "B": second}
	for _, name := range []string{"C", "D", "E", "F"} {
		video := models.Video{Name: name + ".mp4", Path: filepath.Join(root, name+".mp4"), Directory: root, Duration: 100, Size: 10}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		videos[name] = video
	}
	updates := map[string]map[string]interface{}{
		"A": {"watch_position_seconds": 30, "watch_progress_updated_at": at(3), "last_played_at": at(1)},
		"B": {"watch_position_seconds": 20, "watch_progress_updated_at": at(5)},
		"C": {"is_watched": true, "watched_at": at(2), "watch_position_seconds": 40, "watch_progress_updated_at": at(4)},
		"D": {"is_watched": true, "watched_at": at(4), "watch_position_seconds": 40, "watch_progress_updated_at": at(2)},
		"E": {"is_watched": true, "watched_at": at(4), "watch_position_seconds": 40, "last_played_at": at(6)},
		"F": {},
	}
	for name, columns := range updates {
		if len(columns) == 0 {
			continue
		}
		if err := database.DB.Model(&models.Video{}).Where("id = ?", videos[name].ID).Updates(columns).Error; err != nil {
			t.Fatal(err)
		}
	}
	names := map[string]string{}
	for name, video := range videos {
		names[jellyfinID(jellyVideo, video.ID)] = name
	}
	order := func(path string) string {
		t.Helper()
		items, _ := jellyfinItems(t, jellyfinRequest(s, "GET", path, token, ""))
		out := ""
		for _, item := range items {
			out += names[item["Id"].(string)]
		}
		return out
	}

	desktop, err := s.video.ListContinueWatchingWithFilter(LibraryFilter{}, "", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	want := ""
	for _, video := range desktop.Videos {
		want += names[jellyfinID(jellyVideo, video.ID)]
	}
	if want != "BCA" {
		t.Fatalf("desktop continue-watching fixture: %q", want)
	}
	for _, path := range []string{
		"/Items?IsResumable=true",
		"/Items?Filters=IsResumable",
		"/Users/" + jellyfinUserID + "/Items/Resume?Limit=12",
		"/Items?ParentId=" + jellyfinID(jellyView, 3),
	} {
		if got := order(path); got != want {
			t.Errorf("%s: %q want %q (desktop 继续观看)", path, got, want)
		}
	}
	if got := order("/Items?IsResumable=false&SortBy=SortName"); got != "DEF" {
		t.Errorf("IsResumable=false: %q want DEF", got)
	}
	items, _ := jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?SortBy=SortName", token, ""))
	wantTicks := map[string]float64{"A": 30e7, "B": 20e7, "C": 40e7, "D": 0, "E": 0, "F": 0}
	for _, item := range items {
		name := names[item["Id"].(string)]
		if ticks := item["UserData"].(map[string]interface{})["PlaybackPositionTicks"].(float64); ticks != wantTicks[name] {
			t.Errorf("%s: PlaybackPositionTicks %v want %v", name, ticks, wantTicks[name])
		}
	}
	if got := order("/Items?SortBy=DatePlayed&SortOrder=Descending"); got != "EBCADF" {
		t.Errorf("DatePlayed desc: %q want EBCADF", got)
	}
	if got := order("/Items?SortBy=DatePlayed&SortOrder=Ascending"); got != "FDACBE" {
		t.Errorf("DatePlayed asc: %q want FDACBE", got)
	}
}

// ===== MEDIA-08：HasSubtitles 与片库「无字幕」同口径 =====

func TestJellyfinMEDIA08HasSubtitlesMatchesLibraryNoSubtitleView(t *testing.T) {
	s, token, first, second := jellyfinLibraryFixture(t)
	root := first.Directory
	third := models.Video{Name: "third.mp4", Path: filepath.Join(root, "third.mp4"), Directory: root, Duration: 100, Size: 10}
	fourth := models.Video{Name: "fourth.mp4", Path: filepath.Join(root, "fourth.mp4"), Directory: root, Duration: 100, Size: 10}
	for _, video := range []*models.Video{&third, &fourth} {
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatal(err)
		}
	}
	// first：只有旁挂字幕（has_sidecar，名称.zh.ass 一类）；second：内嵌字幕流；third：同名 .srt 已入索引；fourth：都没有。
	rows := []interface{}{
		&models.SubtitleIndexState{VideoID: first.ID, HasSidecar: true},
		&models.MediaStream{VideoID: second.ID, StreamIndex: 0, StreamType: "subtitle", CodecName: "subrip"},
		&models.SubtitleIndexState{VideoID: third.ID, SegmentCount: 3},
		&models.SubtitleIndexState{VideoID: fourth.ID},
	}
	for _, row := range rows {
		if err := database.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	query, err := applyLibraryFilter(database.DB.Model(&models.Video{}), LibraryFilter{SmartView: LibraryViewNoSubtitle}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var noSubtitle []uint
	if err := query.Pluck("videos.id", &noSubtitle).Error; err != nil {
		t.Fatal(err)
	}
	if len(noSubtitle) != 1 || noSubtitle[0] != fourth.ID {
		t.Fatalf("desktop no_subtitle fixture: %v", noSubtitle)
	}
	want := map[uint]bool{first.ID: true, second.ID: true, third.ID: true, fourth.ID: false}
	items, _ := jellyfinItems(t, jellyfinRequest(s, "GET", "/Items", token, ""))
	if len(items) != 4 {
		t.Fatalf("items %d", len(items))
	}
	for id, has := range want {
		detail := jellyfinObject(t, jellyfinRequest(s, "GET", "/Items/"+jellyfinID(jellyVideo, id), token, ""))
		if detail["HasSubtitles"] != has {
			t.Errorf("video %d detail HasSubtitles=%v want %v", id, detail["HasSubtitles"], has)
		}
	}
	for _, item := range items {
		_, id, _ := jellyfinParseID(item["Id"].(string))
		if item["HasSubtitles"] != want[id] {
			t.Errorf("video %d list HasSubtitles=%v want %v", id, item["HasSubtitles"], want[id])
		}
	}
}

// ===== META-07：已删除的标签与人物不会让保存视图变空 =====

func TestJellyfinMETA07SavedViewIgnoresDeletedTagsAndPeople(t *testing.T) {
	s, token, first, second := jellyfinLibraryFixture(t)
	tags := []models.Tag{{Name: "kept"}, {Name: "doomed"}}
	for i := range tags {
		if err := database.DB.Create(&tags[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, link := range []struct {
		video models.Video
		tags  []models.Tag
	}{{first, tags}, {second, tags[:1]}} {
		if err := database.DB.Model(&link.video).Association("Tags").Append(link.tags); err != nil {
			t.Fatal(err)
		}
	}
	people := []models.Person{{DisplayName: "gone"}, {DisplayName: "stays"}}
	for i := range people {
		if err := database.DB.Create(&people[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, link := range []models.VideoPerson{{VideoID: first.ID, PersonID: people[0].ID}, {VideoID: second.ID, PersonID: people[1].ID}} {
		if err := database.DB.Create(&link).Error; err != nil {
			t.Fatal(err)
		}
	}
	save := func(name string, filter LibraryFilter) string {
		t.Helper()
		view, err := s.video.SaveLibraryView(SavedLibraryViewInput{Name: name, LibraryFilter: filter})
		if err != nil {
			t.Fatal(err)
		}
		return jellyfinID(jellySaved, view.ID)
	}
	byTags := save("both tags", LibraryFilter{TagIDs: []uint{tags[0].ID, tags[1].ID}})
	byStays := save("stays", LibraryFilter{PersonIDs: []uint{people[1].ID}})
	byGone := save("gone", LibraryFilter{PersonIDs: []uint{people[0].ID}, TagIDs: []uint{tags[0].ID}})
	ids := func(parent string) map[uint]bool {
		t.Helper()
		items, _ := jellyfinItems(t, jellyfinRequest(s, "GET", "/Items?ParentId="+parent, token, ""))
		out := map[uint]bool{}
		for _, item := range items {
			_, id, _ := jellyfinParseID(item["Id"].(string))
			out[id] = true
		}
		return out
	}
	check := func(label, parent string, want ...uint) {
		t.Helper()
		got := ids(parent)
		if len(got) != len(want) {
			t.Fatalf("%s: got %v want %v", label, got, want)
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("%s: got %v want %v", label, got, want)
			}
		}
	}
	check("tags before delete", byTags, first.ID)
	check("person filter is read from the view", byStays, second.ID)
	check("person+tag before delete", byGone, first.ID)

	if err := (&TagService{}).DeleteTag(tags[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := NewPersonService(t.TempDir()).DeletePerson(people[0].ID); err != nil {
		t.Fatal(err)
	}
	// 与桌面同口径：失效条件忽略，剩下的条件照常生效。
	desktopTags, err := s.video.FilterActiveTagIDs([]uint{tags[0].ID, tags[1].ID})
	if err != nil || desktopTags.Dropped != 1 {
		t.Fatalf("desktop filter %+v %v", desktopTags, err)
	}
	check("deleted tag ignored", byTags, first.ID, second.ID)
	check("deleted person ignored", byGone, first.ID, second.ID)
	check("remaining person still filters", byStays, second.ID)
}

// ===== 删除：不支持废纸篓时沿用既有失败映射 =====

// ErrTrashUnsupportedVolume 走 Jellyfin 删除失败的既有映射（500，日志只记视频 ID）：
// 远端客户端不提供永久删除，记录与文件都保持原样（详细设计 §2.1）。
func TestJellyfinLIB05TrashUnsupportedKeepsExistingDeleteFailureMapping(t *testing.T) {
	s, token, first, _ := jellyfinLibraryFixture(t)
	p010WithSystemTrash(t, func(string) (string, error) { return "", ErrTrashUnsupportedVolume })
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	id := jellyfinID(jellyVideo, first.ID)
	w := jellyfinRequest(s, "DELETE", "/Items/"+id, token, "")
	if w.Code != 500 || !strings.Contains(w.Body.String(), "删除失败，请在桌面端查看回收站状态") {
		t.Fatalf("unsupported volume: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("file must stay: %v", err)
	}
	if w := jellyfinRequest(s, "GET", "/Items/"+id, token, ""); w.Code != 200 {
		t.Fatalf("record must stay visible: %d", w.Code)
	}
	var trashed int64
	database.DB.Model(&models.VideoTrashEntry{}).Where("video_id = ? AND state = ?", first.ID, "deleted").Count(&trashed)
	if trashed != 0 {
		t.Fatalf("no trash entry may be finalized: %d", trashed)
	}
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "[Jellyfin]") && (strings.Contains(line, first.Directory) || strings.Contains(line, first.Name)) {
			t.Fatalf("Jellyfin log carries the media path: %s", line)
		}
	}
	if !strings.Contains(logs.String(), fmt.Sprintf("[Jellyfin] 删除视频 %d 失败", first.ID)) {
		t.Fatalf("failure log should name only the video ID:\n%s", logs.String())
	}
}
