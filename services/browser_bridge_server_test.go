package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeBridgeAuth struct {
	token   string
	enabled bool
	err     error
}

func (f *fakeBridgeAuth) BridgeCredentials() (string, bool, error) {
	return f.token, f.enabled, f.err
}

type fakeEnqueuer struct {
	enqueued  []BrowserDownloadRequest
	err       error
	canceled  []string
	cancelErr error
	tasks     []BrowserDownloadTask
}

func (f *fakeEnqueuer) Enqueue(request BrowserDownloadRequest) (BrowserDownloadTask, error) {
	f.enqueued = append(f.enqueued, request)
	if f.err != nil {
		return BrowserDownloadTask{}, f.err
	}
	return BrowserDownloadTask{ID: "bd1", URL: request.URL, Filename: "x.mp4", State: "queued"}, nil
}

func (f *fakeEnqueuer) ListTasks() []BrowserDownloadTask { return f.tasks }

func (f *fakeEnqueuer) CancelTask(id string) error {
	f.canceled = append(f.canceled, id)
	return f.cancelErr
}

func newBridgeTestServer(auth *fakeBridgeAuth, enqueuer *fakeEnqueuer) http.Handler {
	return NewBrowserBridgeServer(enqueuer, auth).Handler()
}

func bridgeRequest(t *testing.T, handler http.Handler, method, path string, body any, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, reader)
	request.RemoteAddr = "127.0.0.1:54321"
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if mutate != nil {
		mutate(request)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func validPayload() map[string]any {
	return map[string]any{
		"url":           "https://cdn.example.com/a.m3u8",
		"kind":          "hls",
		"title":         "某剧 第一集",
		"variant_label": "1080p",
		"page_url":      "https://site.example.com/watch/1",
		"referer":       "https://site.example.com/watch/1",
		"user_agent":    "UA/1",
		"origin":        "https://site.example.com",
		"cookie":        "",
	}
}

func TestBrowserBridgePingNeedsNoTokenAndLeaksNothing(t *testing.T) {
	handler := newBridgeTestServer(&fakeBridgeAuth{token: "secret", enabled: true}, &fakeEnqueuer{})
	recorder := bridgeRequest(t, handler, http.MethodGet, "/bridge/v1/ping", nil, nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("ping 应当返回 200，实际 %d", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("ping 回包不是 JSON: %v", err)
	}
	if body["app"] != "cineinsight" {
		t.Fatalf("ping 应当自报身份，实际 %v", body["app"])
	}
	// 不要令牌的端点上，除认领身份的两个字段外不该有别的信息。
	if len(body) != 2 {
		t.Fatalf("ping 回包字段过多，可能泄露信息: %v", body)
	}
	if strings.Contains(recorder.Body.String(), "secret") {
		t.Fatal("ping 回包里出现了令牌")
	}
}

func TestBrowserBridgePingRejectsNonGet(t *testing.T) {
	handler := newBridgeTestServer(&fakeBridgeAuth{token: "t", enabled: true}, &fakeEnqueuer{})
	recorder := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/ping", nil, nil)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("期望 405，实际 %d", recorder.Code)
	}
}

func TestBrowserBridgeRejectsMissingAndWrongToken(t *testing.T) {
	auth := &fakeBridgeAuth{token: "right-token", enabled: true}
	enqueuer := &fakeEnqueuer{}
	handler := newBridgeTestServer(auth, enqueuer)

	missing := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads", validPayload(), nil)
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("缺令牌应当 401，实际 %d", missing.Code)
	}

	wrong := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads", validPayload(), func(r *http.Request) {
		r.Header.Set(BrowserBridgeTokenHeader, "wrong-token")
	})
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("错令牌应当 401，实际 %d", wrong.Code)
	}
	// 前缀正确但长度不同的令牌同样要被拒
	prefix := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads", validPayload(), func(r *http.Request) {
		r.Header.Set(BrowserBridgeTokenHeader, "right-tok")
	})
	if prefix.Code != http.StatusUnauthorized {
		t.Fatalf("前缀令牌应当 401，实际 %d", prefix.Code)
	}
	if len(enqueuer.enqueued) != 0 {
		t.Fatalf("鉴权失败的请求不该进队列，实际进了 %d 条", len(enqueuer.enqueued))
	}
}

func TestBrowserBridgeRejectsWhenDisabledOrTokenEmpty(t *testing.T) {
	for _, testCase := range []struct {
		name string
		auth *fakeBridgeAuth
		code int
	}{
		{"桥接关闭", &fakeBridgeAuth{token: "t", enabled: false}, http.StatusForbidden},
		{"令牌为空", &fakeBridgeAuth{token: "   ", enabled: true}, http.StatusForbidden},
		{"设置读不出来", &fakeBridgeAuth{err: errors.New("boom")}, http.StatusInternalServerError},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			handler := newBridgeTestServer(testCase.auth, &fakeEnqueuer{})
			recorder := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads", validPayload(), func(r *http.Request) {
				r.Header.Set(BrowserBridgeTokenHeader, "t")
			})
			if recorder.Code != testCase.code {
				t.Fatalf("期望 %d，实际 %d", testCase.code, recorder.Code)
			}
		})
	}
}

func TestBrowserBridgeRejectsWebPageOrigin(t *testing.T) {
	handler := newBridgeTestServer(&fakeBridgeAuth{token: "t", enabled: true}, &fakeEnqueuer{})

	// 任意网页借环回地址发请求：连令牌都还没验就该被 Origin 挡掉。
	web := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads", validPayload(), func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example.com")
		r.Header.Set(BrowserBridgeTokenHeader, "t")
	})
	if web.Code != http.StatusForbidden {
		t.Fatalf("网页源应当 403，实际 %d", web.Code)
	}

	// 预检也一样
	preflight := bridgeRequest(t, handler, http.MethodOptions, "/bridge/v1/downloads", nil, func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example.com")
	})
	if preflight.Code != http.StatusForbidden {
		t.Fatalf("网页源的预检应当 403，实际 %d", preflight.Code)
	}
}

func TestBrowserBridgeAllowsExtensionOriginPreflight(t *testing.T) {
	handler := newBridgeTestServer(&fakeBridgeAuth{token: "t", enabled: true}, &fakeEnqueuer{})
	const origin = "chrome-extension://abcdefghijklmnopabcdefghijklmnop"

	recorder := bridgeRequest(t, handler, http.MethodOptions, "/bridge/v1/downloads", nil, func(r *http.Request) {
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
	})
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("扩展源的预检应当 204，实际 %d", recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("应当回显扩展源而不是 *，实际 %q", got)
	}
	if !strings.Contains(recorder.Header().Get("Access-Control-Allow-Headers"), BrowserBridgeTokenHeader) {
		t.Fatal("预检没有放行令牌头，扩展将无法发起请求")
	}
}

func TestBrowserBridgeRejectsNonLoopbackSource(t *testing.T) {
	handler := newBridgeTestServer(&fakeBridgeAuth{token: "t", enabled: true}, &fakeEnqueuer{})
	recorder := bridgeRequest(t, handler, http.MethodGet, "/bridge/v1/ping", nil, func(r *http.Request) {
		r.RemoteAddr = "192.168.1.20:5000"
	})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("非本机来源应当 403，实际 %d", recorder.Code)
	}
}

func TestBrowserBridgeBodyDiscipline(t *testing.T) {
	handler := newBridgeTestServer(&fakeBridgeAuth{token: "t", enabled: true}, &fakeEnqueuer{})
	withToken := func(r *http.Request) { r.Header.Set(BrowserBridgeTokenHeader, "t") }

	t.Run("非 JSON 的 Content-Type", func(t *testing.T) {
		recorder := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads", validPayload(), func(r *http.Request) {
			withToken(r)
			r.Header.Set("Content-Type", "text/plain")
		})
		if recorder.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("期望 415，实际 %d", recorder.Code)
		}
	})

	t.Run("未知字段", func(t *testing.T) {
		payload := validPayload()
		payload["surprise"] = "x"
		recorder := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads", payload, withToken)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("期望 400，实际 %d", recorder.Code)
		}
	})

	t.Run("空请求体", func(t *testing.T) {
		recorder := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads", nil, func(r *http.Request) {
			withToken(r)
			r.Header.Set("Content-Type", "application/json")
		})
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("期望 400，实际 %d", recorder.Code)
		}
	})

	t.Run("超长请求体", func(t *testing.T) {
		payload := validPayload()
		payload["title"] = strings.Repeat("长", 400_000)
		recorder := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads", payload, withToken)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("超长请求体期望 400，实际 %d", recorder.Code)
		}
	})
}

func TestBrowserBridgeEnqueueAndErrors(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	handler := newBridgeTestServer(&fakeBridgeAuth{token: "t", enabled: true}, enqueuer)
	withToken := func(r *http.Request) { r.Header.Set(BrowserBridgeTokenHeader, "t") }

	recorder := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads", validPayload(), withToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("正常推送应当 200，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if len(enqueuer.enqueued) != 1 || enqueuer.enqueued[0].Referer != "https://site.example.com/watch/1" {
		t.Fatalf("请求头没有原样传到队列：%+v", enqueuer.enqueued)
	}

	for _, testCase := range []struct {
		err  error
		code string
		want int
	}{
		{ErrBrowserDownloadDirectoryUnset, "download_directory_unset", http.StatusBadRequest},
		{fmt.Errorf("%w：地址不对", ErrBrowserDownloadInvalidRequest), "invalid_request", http.StatusBadRequest},
		{ErrBrowserDownloadQueueFull, "queue_full", http.StatusTooManyRequests},
		{errors.New("别的毛病"), "enqueue_failed", http.StatusInternalServerError},
	} {
		enqueuer.err = testCase.err
		recorder := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads", validPayload(), withToken)
		if recorder.Code != testCase.want {
			t.Fatalf("%v 期望 %d，实际 %d", testCase.err, testCase.want, recorder.Code)
		}
		var body map[string]string
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		if body["error"] != testCase.code {
			t.Fatalf("%v 期望错误码 %q，实际 %q", testCase.err, testCase.code, body["error"])
		}
	}
}

func TestBrowserBridgeCancelRouting(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	handler := newBridgeTestServer(&fakeBridgeAuth{token: "t", enabled: true}, enqueuer)
	withToken := func(r *http.Request) { r.Header.Set(BrowserBridgeTokenHeader, "t") }

	ok := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads/bd7/cancel", nil, withToken)
	if ok.Code != http.StatusOK {
		t.Fatalf("取消应当 200，实际 %d", ok.Code)
	}
	if len(enqueuer.canceled) != 1 || enqueuer.canceled[0] != "bd7" {
		t.Fatalf("取消的任务 ID 不对：%v", enqueuer.canceled)
	}

	bad := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads/bd7/delete", nil, withToken)
	if bad.Code != http.StatusNotFound {
		t.Fatalf("未知动作应当 404，实际 %d", bad.Code)
	}

	enqueuer.cancelErr = errors.New("没有这个任务")
	missing := bridgeRequest(t, handler, http.MethodPost, "/bridge/v1/downloads/nope/cancel", nil, withToken)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("取消不存在的任务应当 404，实际 %d", missing.Code)
	}
}

func TestBrowserBridgeStatusWhenDisabled(t *testing.T) {
	server := NewBrowserBridgeServer(&fakeEnqueuer{}, &fakeBridgeAuth{enabled: false})
	server.Start(context.Background())
	status := server.Status()
	if status.Running {
		t.Fatal("桥接关闭时不该启动服务")
	}
	if status.Port != 0 {
		t.Fatalf("桥接关闭时不该占端口，实际 %d", status.Port)
	}
}

func TestBrowserBridgeRefusesToStartWithoutToken(t *testing.T) {
	server := NewBrowserBridgeServer(&fakeEnqueuer{}, &fakeBridgeAuth{enabled: true, token: ""})
	server.Start(context.Background())
	status := server.Status()
	if status.Running {
		t.Fatal("没有令牌时不该启动：那等于开一个无闸门的下载入口")
	}
	if !strings.Contains(status.StartupError, "令牌") {
		t.Fatalf("状态里应当说明缺令牌，实际 %q", status.StartupError)
	}
}

func TestBrowserBridgePlayRequiresTokenAndPlayer(t *testing.T) {
	auth := &fakeBridgeAuth{token: "t", enabled: true}
	payload := map[string]any{
		"url": "https://cdn/a.m3u8", "referer": "https://page/x", "origin": "https://page", "user_agent": "UA/1",
	}

	// 没注入播放器：明确回 501，而不是假装播了
	server := NewBrowserBridgeServer(&fakeEnqueuer{}, auth)
	recorder := bridgeRequest(t, server.Handler(), http.MethodPost, "/bridge/v1/play", payload, func(r *http.Request) {
		r.Header.Set(BrowserBridgeTokenHeader, "t")
	})
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("没有播放器时期望 501，实际 %d", recorder.Code)
	}

	// 缺令牌一样要拦
	noToken := bridgeRequest(t, server.Handler(), http.MethodPost, "/bridge/v1/play", payload, nil)
	if noToken.Code != http.StatusUnauthorized {
		t.Fatalf("缺令牌应当 401，实际 %d", noToken.Code)
	}
}

// 播放器拿到的必须是本机代理地址，而不是源站地址。
//
// 原因：播放器传不进请求头。iina-cli 的 --mpv-http-header-fields 那条路实测不通，
// 而且浏览器的 User-Agent 里天然带逗号（"(KHTML, like Gecko)"），
// 而那个选项是逗号分隔的列表——值里的逗号会把请求头列表劈开，喂给播放器一堆畸形的头。
// 所以请求头改由代理在取源站时加，播放器只管播 127.0.0.1。
func TestBrowserBridgePlayGoesThroughLocalProxy(t *testing.T) {
	server := NewBrowserBridgeServer(&fakeEnqueuer{}, &fakeBridgeAuth{token: "t", enabled: true})
	server.SetStreamProxy(NewStreamProxy())
	var gotURL string
	var gotHeaders map[string]string
	server.SetStreamPlayer(func(url string, headers map[string]string) error {
		gotURL = url
		gotHeaders = headers
		return nil
	})

	recorder := bridgeRequest(t, server.Handler(), http.MethodPost, "/bridge/v1/play", map[string]any{
		"url":     "https://cdn/a.m3u8",
		"referer": "https://page/x",
		"origin":  "https://page",
		// 真实浏览器的 UA 就长这样，逗号是关键
		"user_agent": "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120",
	}, func(r *http.Request) {
		r.Header.Set(BrowserBridgeTokenHeader, "t")
		r.Host = "127.0.0.1:18110"
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if !strings.HasPrefix(gotURL, "http://127.0.0.1:18110/bridge/v1/stream/") {
		t.Fatalf("播放器应当拿到本机代理地址，实际 %q", gotURL)
	}
	// 入口必须带播放器认得的扩展名：结尾是 /root 时 IINA 连请求都不发（实测）。
	if !strings.HasSuffix(gotURL, ".m3u8") {
		t.Fatalf("代理入口必须带可播放的扩展名：%q", gotURL)
	}
	// 请求头有意不传给播放器：它本来就传不进去，传了只会被静默丢掉或劈坏
	if len(gotHeaders) != 0 {
		t.Fatalf("不该再给播放器传请求头：%+v", gotHeaders)
	}
}

func TestLaunchStreamInIINAUsesURLScheme(t *testing.T) {
	原Open := openURLSchemeFn
	t.Cleanup(func() { openURLSchemeFn = 原Open })

	var opened string
	openURLSchemeFn = func(deepLink string) error { opened = deepLink; return nil }

	// 非 http 协议不给播：这条会变成播放器的输入
	if err := LaunchStreamInIINA("file:///etc/passwd", nil); err == nil {
		t.Fatal("file:// 应当被拒")
	}

	if err := LaunchStreamInIINA("http://127.0.0.1:18110/bridge/v1/stream/abc/index.m3u8", nil); err != nil {
		t.Fatalf("正常调用不该失败：%v", err)
	}
	// 必须走 iina:// scheme：实测 iina-cli 对本机地址一次请求都不发，scheme 才通。
	if !strings.HasPrefix(opened, "iina://weblink?url=") {
		t.Fatalf("应当用 iina:// scheme 打开，实际 %q", opened)
	}
	// 地址要整体转义，不然查询串里的 & 会把参数截断
	if !strings.Contains(opened, "http%3A%2F%2F127.0.0.1%3A18110") {
		t.Fatalf("地址没有正确转义：%q", opened)
	}
}
