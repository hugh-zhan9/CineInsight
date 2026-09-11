package services

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

// 出网配置的优先级方向：设置里的非空值覆盖环境变量（D-WM10）。写反了用户在设置页
// 改完不生效，只能回头改环境变量——这正是既有
// TestSettingsAITaggingConfigProviderLoadsDatabaseSettings 钉住的方向，本用例把
// 同一条规矩钉在资料源配置上。
func TestLoadWatchlistMetadataConfigPrefersSettingsOverEnv(t *testing.T) {
	setupVideoServiceTestDB(t)
	t.Setenv(envWatchlistMetadataProxyURL, "http://env.example:8080")
	t.Setenv(envTMDBAPIKey, "env-tmdb")
	t.Setenv(envBangumiAccessToken, "env-bangumi")
	t.Setenv(envFANZAAPIID, "env-fanza-api")
	t.Setenv(envFANZAAffiliateID, "env-fanza-affiliate")

	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Updates(models.Settings{
		MetadataProxyURL:   "socks5://db.example:1080",
		TMDBAPIKey:         "db-tmdb",
		BangumiAccessToken: "db-bangumi",
		FANZAAPIID:         "db-fanza-api",
		FANZAAffiliateID:   "db-fanza-affiliate",
	}).Error; err != nil {
		t.Fatalf("更新设置失败: %v", err)
	}

	config := LoadWatchlistMetadataConfig()
	want := WatchlistMetadataConfig{
		ProxyURL:           "socks5://db.example:1080",
		TMDBAPIKey:         "db-tmdb",
		BangumiAccessToken: "db-bangumi",
		FANZAAPIID:         "db-fanza-api",
		FANZAAffiliateID:   "db-fanza-affiliate",
	}
	if config != want {
		t.Fatalf("期望优先读取数据库配置，实际: %+v", config)
	}
}

// 设置为空时才回落到环境变量，且是逐项回落——设置里只填了一项，其余仍走环境变量。
func TestLoadWatchlistMetadataConfigFallsBackToEnvPerField(t *testing.T) {
	setupVideoServiceTestDB(t)
	t.Setenv(envWatchlistMetadataProxyURL, "http://env.example:8080")
	t.Setenv(envTMDBAPIKey, "env-tmdb")
	t.Setenv(envBangumiAccessToken, "env-bangumi")
	t.Setenv(envFANZAAPIID, "env-fanza-api")
	t.Setenv(envFANZAAffiliateID, "env-fanza-affiliate")

	// 只有 TMDB 在设置页填了；其余四项留空。
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").
		Update("tmdb_api_key", "db-tmdb").Error; err != nil {
		t.Fatalf("更新设置失败: %v", err)
	}

	config := LoadWatchlistMetadataConfig()
	want := WatchlistMetadataConfig{
		ProxyURL:           "http://env.example:8080",
		TMDBAPIKey:         "db-tmdb",
		BangumiAccessToken: "env-bangumi",
		FANZAAPIID:         "env-fanza-api",
		FANZAAffiliateID:   "env-fanza-affiliate",
	}
	if config != want {
		t.Fatalf("期望逐项回落环境变量，实际: %+v", config)
	}
}

// 库还没初始化时读配置不能炸：应用启动早期就可能问到这里。
func TestLoadWatchlistMetadataConfigWithoutDatabase(t *testing.T) {
	previous := database.DB
	database.DB = nil
	t.Cleanup(func() { database.DB = previous })
	t.Setenv(envTMDBAPIKey, "env-tmdb")

	if config := LoadWatchlistMetadataConfig(); config.TMDBAPIKey != "env-tmdb" {
		t.Fatalf("库未初始化时应只读环境变量，实际: %+v", config)
	}
}

// 代理地址留空＝直连。这里**特意**设了 HTTP_PROXY：Transport 的默认 Proxy 是
// http.ProxyFromEnvironment，忘了覆盖就会悄悄走上环境里那个代理，而那是没人要求过
// 的隐式通道。
func TestNewWatchlistMetadataHTTPClientConnectsDirectlyWithoutProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://should-not-be-used.example:3128")
	t.Setenv("HTTPS_PROXY", "http://should-not-be-used.example:3128")

	client, err := NewWatchlistMetadataHTTPClient(WatchlistMetadataConfig{}, 5*time.Second)
	if err != nil {
		t.Fatalf("未配置代理时不该报错: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("期望 *http.Transport，实际 %T", client.Transport)
	}
	if transport.Proxy != nil {
		request, _ := http.NewRequest(http.MethodGet, "https://api.themoviedb.org/3/movie/1", nil)
		used, proxyErr := transport.Proxy(request)
		t.Fatalf("代理地址为空时应直连，实际 Proxy 返回 %v（err=%v）", used, proxyErr)
	}
	if client.Timeout != 5*time.Second {
		t.Fatalf("期望透传调用方给的超时，实际 %v", client.Timeout)
	}
}

// http 与 socks5 两种代理都要生效（AC-06）。这一条钉的是「Transport 确实拿到了
// 配置的代理 URL」，下面两条再用真代理服务器钉「请求真的从那里出去」。
func TestNewWatchlistMetadataHTTPClientUsesConfiguredProxy(t *testing.T) {
	cases := []string{
		"http://127.0.0.1:8080",
		"https://proxy.example:8443",
		"socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080",
		"http://user:secret@127.0.0.1:8080",
	}
	for _, proxy := range cases {
		t.Run(proxy, func(t *testing.T) {
			client, err := NewWatchlistMetadataHTTPClient(WatchlistMetadataConfig{ProxyURL: proxy}, time.Second)
			if err != nil {
				t.Fatalf("构造客户端失败: %v", err)
			}
			transport, ok := client.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("期望 *http.Transport，实际 %T", client.Transport)
			}
			request, err := http.NewRequest(http.MethodGet, "https://api.themoviedb.org/3/movie/1", nil)
			if err != nil {
				t.Fatalf("构造请求失败: %v", err)
			}
			used, err := transport.Proxy(request)
			if err != nil {
				t.Fatalf("解析代理失败: %v", err)
			}
			if used == nil || used.String() != proxy {
				t.Fatalf("期望走代理 %s，实际 %v", proxy, used)
			}
		})
	}
}

// 地址非法要给出可区分的错误，**不能**静默退化为直连——那会让用户以为代理生效了，
// 而请求其实是从本机裸奔出去的。
func TestNewWatchlistMetadataHTTPClientRejectsInvalidProxy(t *testing.T) {
	cases := map[string]string{
		"缺协议前缀":     "127.0.0.1:1080",
		"只有主机名":     "proxy.example",
		"不支持的协议":    "ftp://127.0.0.1:2121",
		"socks4 不认": "socks4://127.0.0.1:1080",
		"有协议没主机":    "socks5://",
		"控制字符":      "http://127.0.0.1:1080/\x7f",
	}
	for name, proxy := range cases {
		t.Run(name, func(t *testing.T) {
			client, err := NewWatchlistMetadataHTTPClient(WatchlistMetadataConfig{ProxyURL: proxy}, time.Second)
			if err == nil {
				t.Fatalf("非法代理地址 %q 必须报错，不能静默直连（返回了 %v）", proxy, client)
			}
			if client != nil {
				t.Fatalf("报错时不该返回可用客户端，实际 %v", client)
			}
			if !errors.Is(err, ErrWatchlistMetadataProxyInvalid) {
				t.Fatalf("期望可区分的代理配置错误，实际: %v", err)
			}
		})
	}
}

// 带认证的代理写成 socks5://user:pw@host 是常见形态。地址填错时的报错会进日志、
// 也会显示给用户，绝不能把口令原样带出去。
func TestNewWatchlistMetadataHTTPClientRedactsProxyCredentialsInErrors(t *testing.T) {
	cases := []string{
		"socks4://alice:hunter2@127.0.0.1:1080",    // 协议不支持
		"ftp://alice:hunter2@127.0.0.1:2121",       // 协议不支持
		"alice:hunter2@127.0.0.1:1080",             // 没有协议前缀
		"http://alice:hunter2@127.0.0.1:8080/\x7f", // url.Parse 直接失败
		// 口令里带一个未转义的 '/'：authority 在 '/' 处被截断，早先那版按
		// 「第一个 '/' 之前找 @」的写法会认为没有 userinfo 而原样回显。
		"http://alice:hunter2/x@127.0.0.1:8080",
		// 三斜杠打字错误：口令落在**路径**里而不是 userinfo 里，结构化脱敏
		// 看不到它。
		"http:///alice:hunter2@127.0.0.1",
	}
	for _, proxy := range cases {
		t.Run(proxy, func(t *testing.T) {
			_, err := NewWatchlistMetadataHTTPClient(WatchlistMetadataConfig{ProxyURL: proxy}, time.Second)
			if err == nil {
				t.Fatalf("期望报错，实际通过")
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("报错里带出了代理口令: %v", err)
			}
			if strings.Contains(err.Error(), "alice") {
				t.Fatalf("报错里带出了代理用户名: %v", err)
			}
			if !strings.Contains(err.Error(), proxyCredentialMask+"@") {
				t.Fatalf("期望 userinfo 被替换成 %s@，实际: %v", proxyCredentialMask, err)
			}
		})
	}
}

// 脱敏只该动 userinfo，不该把正常地址改得面目全非——错误信息还得能指认是哪个地址。
func TestDescribeProxyURLKeepsAddressesIdentifiable(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		// 解析得动的：只回显协议和主机，路径/查询串一律丢掉。
		{"socks5://127.0.0.1:1080", "socks5://127.0.0.1:1080"},
		{"http://proxy.example:8080", "http://proxy.example:8080"},
		{"socks5://alice:pw@127.0.0.1:1080", "socks5://" + proxyCredentialMask + "@127.0.0.1:1080"},
		{"socks5://alice@127.0.0.1:1080", "socks5://" + proxyCredentialMask + "@127.0.0.1:1080"},
		// 路径、查询串、片段里的 @ 都不是凭证，不该被当成 userinfo 误报。
		{"http://proxy.example:8080/a@b", "http://proxy.example:8080"},
		{"http://proxy.example:8080?x=a@b", "http://proxy.example:8080"},
		{"http://proxy.example:8080#a@b", "http://proxy.example:8080"},
		// 解析不出结构的：退回字符串规则，地址本身照样看得出来。
		{"127.0.0.1:1080", "127.0.0.1:1080"},
		{"proxy.example", "proxy.example"},
		{"socks5://", "socks5://"},
	}
	for _, item := range cases {
		parsed, err := url.Parse(item.raw)
		if err != nil {
			parsed = nil
		}
		if got := describeProxyURL(item.raw, parsed); got != item.want {
			t.Errorf("describeProxyURL(%q) = %q，期望 %q", item.raw, got, item.want)
		}
	}
}

// 真·HTTP 代理：请求必须落到代理上，而不是直接打到目标服务器。
func TestNewWatchlistMetadataHTTPClientRoutesThroughHTTPProxy(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("origin"))
	}))
	t.Cleanup(origin.Close)

	var proxied atomic.Int64
	// 正向代理收到的是 absolute-form 请求行（GET http://host/path），据此断定
	// 这次请求确实经过了代理。
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			t.Errorf("代理应收到 absolute-form 请求，实际 %q", r.URL.String())
		}
		proxied.Add(1)
		w.Write([]byte("via-proxy"))
	}))
	t.Cleanup(proxy.Close)

	client, err := NewWatchlistMetadataHTTPClient(WatchlistMetadataConfig{ProxyURL: proxy.URL}, 10*time.Second)
	if err != nil {
		t.Fatalf("构造客户端失败: %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)

	body := mustGetBody(t, client, origin.URL+"/3/search/movie")
	if body != "via-proxy" {
		t.Fatalf("期望响应来自代理，实际 %q", body)
	}
	if proxied.Load() != 1 {
		t.Fatalf("期望代理处理 1 次请求，实际 %d", proxied.Load())
	}
}

// 真·SOCKS5 代理：AC-06 点名要支持 SOCKS5，只做 HTTP 不算完成。net/http 的
// Transport 原生认 socks5://，因此这里不需要额外的拨号器依赖，用一个最小可用的
// SOCKS5 服务端把这条路走通即可。
func TestNewWatchlistMetadataHTTPClientRoutesThroughSOCKS5Proxy(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("origin-via-socks5"))
	}))
	t.Cleanup(origin.Close)

	proxyAddr, relayed := startTestSOCKS5Proxy(t)
	client, err := NewWatchlistMetadataHTTPClient(
		WatchlistMetadataConfig{ProxyURL: "socks5://" + proxyAddr}, 10*time.Second)
	if err != nil {
		t.Fatalf("构造客户端失败: %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)

	body := mustGetBody(t, client, origin.URL+"/3/search/movie")
	if body != "origin-via-socks5" {
		t.Fatalf("期望取到目标服务器响应，实际 %q", body)
	}
	if relayed.Load() != 1 {
		t.Fatalf("期望 SOCKS5 代理转发 1 条连接，实际 %d", relayed.Load())
	}
}

func mustGetBody(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return string(payload)
}

// startTestSOCKS5Proxy 起一个只够用的 SOCKS5 服务端（无认证 + CONNECT），返回监听
// 地址与「成功转发过几条连接」的计数器。够验证 socks5:// 这条路是通的，不追求完整
// 实现——真代理由用户自己提供。
func startTestSOCKS5Proxy(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听 SOCKS5 端口失败: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	var relayed atomic.Int64
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveTestSOCKS5Conn(conn, &relayed)
		}
	}()
	return listener.Addr().String(), &relayed
}

func serveTestSOCKS5Conn(client net.Conn, relayed *atomic.Int64) {
	defer client.Close()

	// 握手：VER(0x05) NMETHODS METHODS...，回「无需认证」。
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(client, greeting); err != nil || greeting[0] != 0x05 {
		return
	}
	if _, err := io.ReadFull(client, make([]byte, int(greeting[1]))); err != nil {
		return
	}
	if _, err := client.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	// 请求：VER CMD RSV ATYP ADDR PORT。只实现 CONNECT。
	request := make([]byte, 4)
	if _, err := io.ReadFull(client, request); err != nil {
		return
	}
	if request[0] != 0x05 || request[1] != 0x01 {
		client.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	var host string
	switch request[3] {
	case 0x01, 0x04:
		size := 4
		if request[3] == 0x04 {
			size = 16
		}
		address := make([]byte, size)
		if _, err := io.ReadFull(client, address); err != nil {
			return
		}
		host = net.IP(address).String()
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(client, length); err != nil {
			return
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(client, name); err != nil {
			return
		}
		host = string(name)
	default:
		client.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(client, port); err != nil {
		return
	}

	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port))))
	upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		client.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	// 回 succeeded，绑定地址回 0.0.0.0:0——客户端不看它。
	if _, err := client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	relayed.Add(1)

	go func() {
		io.Copy(upstream, client)
		upstream.Close()
	}()
	io.Copy(client, upstream)
}
