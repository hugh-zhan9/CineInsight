package services

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"video-master/database"
	"video-master/models"
)

// 想看片单补全的出网配置（D-WM10）。三家源的凭证与资料源出网代理沿用
// AITaggingAPIKey 的既有形态：明文列 + 环境变量兜底。
const (
	envWatchlistMetadataProxyURL = "WATCHLIST_METADATA_PROXY_URL"
	envTMDBAPIKey                = "TMDB_API_KEY"
	envBangumiAccessToken        = "BANGUMI_ACCESS_TOKEN"
	envFANZAAPIID                = "FANZA_API_ID"
	envFANZAAffiliateID          = "FANZA_AFFILIATE_ID"
)

// WatchlistMetadataConfig 是补全链路唯一的出网配置来源：代理地址加三家源的凭证。
// 适配器、海报下载与连接探测都只读它，不各自去翻设置或环境变量。
type WatchlistMetadataConfig struct {
	// ProxyURL 为空表示直连。非空时必须是 http / https / socks5 / socks5h。
	ProxyURL           string
	TMDBAPIKey         string
	BangumiAccessToken string
	// FANZA 要两个：DMM 联盟 API 的 api_id 与 affiliate_id 都是必传参数。
	FANZAAPIID       string
	FANZAAffiliateID string
}

// ErrWatchlistMetadataProxyInvalid 标记「代理地址填错了」，与「代理连不上」和
// 「网络不可达」分开。调用方据此给出 D-WM14 的 proxy_unreachable 分类，而不是
// 悄悄直连——用户配了代理却走直连，是他最不该被蒙在鼓里的一件事。
var ErrWatchlistMetadataProxyInvalid = errors.New("资料源出网代理地址无效")

// LoadWatchlistMetadataConfig 读取补全链路的出网配置。
//
// 优先级方向照仓库既有次序（services/ai_tagging_config.go 的 Load）：先取环境
// 变量做底子，再用设置里的**非空**值逐项覆盖，也就是**设置覆盖环境变量**。写反
// 过来会让用户在设置页改完不生效，只能去改环境变量——既有测试
// TestSettingsAITaggingConfigProviderLoadsDatabaseSettings 钉的就是这个方向。
//
// 凭证是否齐备不在这里判断：缺凭证是 D-WM14 的 credential_missing 分类，由各源
// 适配器在发请求前自行认定，因此本函数不返回错误。
func LoadWatchlistMetadataConfig() WatchlistMetadataConfig {
	config := WatchlistMetadataConfig{
		ProxyURL:           strings.TrimSpace(os.Getenv(envWatchlistMetadataProxyURL)),
		TMDBAPIKey:         strings.TrimSpace(os.Getenv(envTMDBAPIKey)),
		BangumiAccessToken: strings.TrimSpace(os.Getenv(envBangumiAccessToken)),
		FANZAAPIID:         strings.TrimSpace(os.Getenv(envFANZAAPIID)),
		FANZAAffiliateID:   strings.TrimSpace(os.Getenv(envFANZAAffiliateID)),
	}

	if database.DB == nil {
		return config
	}
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return config
	}
	if value := strings.TrimSpace(settings.MetadataProxyURL); value != "" {
		config.ProxyURL = value
	}
	if value := strings.TrimSpace(settings.TMDBAPIKey); value != "" {
		config.TMDBAPIKey = value
	}
	if value := strings.TrimSpace(settings.BangumiAccessToken); value != "" {
		config.BangumiAccessToken = value
	}
	if value := strings.TrimSpace(settings.FANZAAPIID); value != "" {
		config.FANZAAPIID = value
	}
	if value := strings.TrimSpace(settings.FANZAAffiliateID); value != "" {
		config.FANZAAffiliateID = value
	}
	return config
}

// NewWatchlistMetadataHTTPClient 按配置构造外部资料源请求用的客户端。补全链路的
// 每一次出网——适配器请求、海报下载、连接探测——都从这里拿客户端，不自建，
// 否则代理配了也只对其中一部分生效。
//
// timeout 是单次请求的总超时，由调用方按用途给：资料查询要短，海报下载要长。
// 传 0 即 http.Client 的原义「不设总超时」，此时中止只能靠 ctx。
//
// 返回的客户端自带一套连接池，调用方应当**建一次反复用**，不要每个请求建一个。
//
// 地址填错时返回 ErrWatchlistMetadataProxyInvalid 而不是退回直连：静默直连会让
// 用户以为代理生效了，而请求其实是从本机裸奔出去的。
func NewWatchlistMetadataHTTPClient(config WatchlistMetadataConfig, timeout time.Duration) (*http.Client, error) {
	proxyURL, err := parseWatchlistMetadataProxyURL(config.ProxyURL)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Clone 带来的默认值是 http.ProxyFromEnvironment。这里必须显式覆盖：
	// 代理地址留空的语义是「直连」，不是「改从 HTTP_PROXY / ALL_PROXY 里找一个」。
	// 出网代理有自己的环境变量兜底（envWatchlistMetadataProxyURL），已经在
	// LoadWatchlistMetadataConfig 里合并过了，不需要第二条隐式通道。
	if proxyURL == nil {
		transport.Proxy = nil
	} else {
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// parseWatchlistMetadataProxyURL 把配置里的代理地址解析成 http.Transport 认得的
// URL。返回 (nil, nil) 表示「没配代理，直连」。
//
// 协议白名单就是 net/http 的 Transport 原生支持的四种（见 transport.go 对
// Proxy 字段的说明）：http、https、socks5、socks5h。AC-06 点名要支持 SOCKS5，
// 而 Transport 本来就认 socks5://，不需要额外的拨号器依赖。放行白名单之外的
// 协议没有意义——Transport 会等到真正发请求时才报错，那时错误已经离配置很远了。
func parseWatchlistMetadataProxyURL(raw string) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		// 地址里只要出现过 '@'，解析失败的原因就一个字都不引用。
		//
		// url.Error 的 Error() 会把原始地址整个带出来，这好办——剥掉外层即可；
		// 但**剥到里层照样可能引用输入的片段**：口令写成 user:pw@host 而 pw 不是
		// 合法端口时，net/url 给的原文就是 invalid port ":pw" after host，口令就在
		// 报错里。没有 '@' 就不可能有 userinfo，那时引用原因是安全的。
		if strings.ContainsRune(value, '@') {
			return nil, fmt.Errorf("%w：%s 解析失败", ErrWatchlistMetadataProxyInvalid, describeProxyURL(value, nil))
		}
		reason := err
		var parseErr *url.Error
		if errors.As(err, &parseErr) {
			reason = parseErr.Err
		}
		return nil, fmt.Errorf("%w：%s 解析失败（%v）", ErrWatchlistMetadataProxyInvalid, describeProxyURL(value, nil), reason)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		// 只填 127.0.0.1:1080 也会落到这里：url.Parse 的 getScheme 见开头是数字就
		// 放弃取协议，留下一个空 Scheme，空协议过不了这张白名单。
		return nil, fmt.Errorf("%w：%s 缺少可用的协议前缀，需要 http://、https://、socks5:// 或 socks5h://", ErrWatchlistMetadataProxyInvalid, describeProxyURL(value, parsed))
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("%w：%s 没有主机名或端口", ErrWatchlistMetadataProxyInvalid, describeProxyURL(value, parsed))
	}
	return parsed, nil
}

// proxyCredentialMask 替换掉代理地址里的 userinfo。用普通字母而不是 "***"：
// url.Userinfo 会把 '*' 转义成 %2A，出来的文案没法看。
const proxyCredentialMask = "redacted"

// describeProxyURL 给出一个可以安全写进日志和界面的代理地址描述。带认证的代理
// 写成 socks5://user:pw@host:1080 是常见形态，而这些错误既进日志也显示给用户，
// 原样回显等于把口令抄进日志。
//
// 解析成功且协议与主机都拿得到时，只回显 **协议和主机**，别的一概丢掉：口令只
// 可能出现在 userinfo 里，而路径、查询串、片段对一个代理地址毫无意义，却完全
// 可能装着用户手滑写进去的口令——http:///user:pw@host 这种三斜杠打字错误，口令
// 就落在路径里。只留 scheme 和 host，用户仍认得出自己填的是哪个地址，而且没有
// 可泄漏的余地。
//
// 剩下的情况（压根没解析成功、没解析出协议、或没有主机）没有可信的结构可依，
// 退回字符串规则：整串里最后一个 '@' 之前全抹掉。这会误伤路径里的 '@'，但口令
// 里带一个未转义的 '/' 时恰恰只有这条路能兜住，宁可过度打码也不能漏。
func describeProxyURL(raw string, parsed *url.URL) string {
	if parsed != nil && parsed.Scheme != "" && parsed.Host != "" {
		if parsed.User != nil {
			return parsed.Scheme + "://" + proxyCredentialMask + "@" + parsed.Host
		}
		return parsed.Scheme + "://" + parsed.Host
	}

	at := strings.LastIndexByte(raw, '@')
	if at < 0 {
		return raw
	}
	prefix := ""
	if index := strings.Index(raw, "://"); index >= 0 && index+3 <= at {
		prefix = raw[:index+3]
	}
	return prefix + proxyCredentialMask + "@" + raw[at+1:]
}
