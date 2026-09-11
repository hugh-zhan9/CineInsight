package services

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// 在线资料源的连接探测（P-009）。照 ProbeAITaggingConnection 的形状：用设置页
// **当前填写的值**发一次真请求，不要求先保存，返回值不含任何凭证。
//
// 探测不自己拼请求：它走 NewWatchlistMetadataRegistry 装出来的那一套适配器，
// 和补全链路用的是同一个客户端工厂、同一份出网配置、同一套错误分类。另写一套
// 请求逻辑的话，探测通过而补全失败（或反过来）就成了常态，探测也就没用了。

// watchlistMetadataProbeTimeout 是单次探测的总超时。
//
// 比 AI 打标那边的 30 秒短一半：这里只是敲一下门，而按钮是同步等的——代理连不上
// 时用户盯着转圈的时间就是这个值，30 秒太久。补全链路自己的超时不受这里影响。
const watchlistMetadataProbeTimeout = 15 * time.Second

// WatchlistMetadataProbeInput 是设置页「测试连接」传来的表单值。
//
// 留空的字段回退到已保存 / 环境变量配置（与 AITaggingConnectionTestInput 同一约定），
// 所以用户改完不必先保存就能测。前端只需要带上被测源用得到的那几项。
type WatchlistMetadataProbeInput struct {
	// Source 是要探测的源名：tmdb / bangumi / fanza / javbus。
	Source             string `json:"source"`
	ProxyURL           string `json:"proxy_url"`
	TMDBAPIKey         string `json:"tmdb_api_key"`
	BangumiAccessToken string `json:"bangumi_access_token"`
	FANZAAPIID         string `json:"fanza_api_id"`
	FANZAAffiliateID   string `json:"fanza_affiliate_id"`
}

// WatchlistMetadataProbeResult 是一次探测的结果。
//
// **不含任何凭证**，这是硬约束：结果既回传给界面也进日志。ProxyURL 回显的是
// describeProxyURL 脱敏后的形态（只剩协议和主机），用户据此确认这次到底是走代理
// 还是直连——带口令的代理地址是常见形态，原样回显等于把口令抄进日志。
type WatchlistMetadataProbeResult struct {
	OK     bool   `json:"ok"`
	Source string `json:"source"`
	// Failure 是 D-WM14 的失败分类码，成功时为空。界面据此区别对待，不必解析文案。
	Failure string `json:"failure"`
	// ProxyURL 为空表示这次是直连。
	ProxyURL   string `json:"proxy_url"`
	HTTPStatus int    `json:"http_status"`
	LatencyMS  int64  `json:"latency_ms"`
	Message    string `json:"message"`
}

// watchlistMetadataProbeTarget 说明一个源该怎么敲门：用哪个类型的链找到它，
// 以及拿什么关键词去问。
type watchlistMetadataProbeTarget struct {
	source string
	kind   WatchlistMetadataKind
	// query 只用来触发一次真实请求，命不命中都不影响结论——源明确回答
	// 「没有这个条目」同样证明网络、代理与凭证都是通的。
	query string
	// credentialHint 是凭证缺失时告诉用户去填哪一栏。JavBus 不要凭证，留空。
	credentialHint string
}

// watchlistMetadataProbeTargets 是四个源各自的探测目标。
//
// 关键词挑的都是长期存在的条目，好让「连接正常且取到结果」成为常态；但探测的
// 判据不依赖它们——见 query 的说明。
var watchlistMetadataProbeTargets = []watchlistMetadataProbeTarget{
	{source: WatchlistMetadataSourceTMDB, kind: WatchlistMetadataKindMovie, query: "Dune", credentialHint: "TMDB API Key"},
	{source: WatchlistMetadataSourceBangumi, kind: WatchlistMetadataKindAnime, query: "攻殻機動隊", credentialHint: "Bangumi Access Token"},
	{source: WatchlistMetadataSourceFANZA, kind: WatchlistMetadataKindAV, query: "SSIS-001", credentialHint: "FANZA API ID 与 Affiliate ID"},
	// JavBus 按番号直接打详情页，没有关键词搜索；这里给的就是一个番号。
	{source: WatchlistMetadataSourceJavBus, kind: WatchlistMetadataKindAV, query: "SSIS-001"},
}

// AllWatchlistMetadataProbeSources 返回可探测的源名，顺序即设置页的展示顺序。
func AllWatchlistMetadataProbeSources() []string {
	sources := make([]string, 0, len(watchlistMetadataProbeTargets))
	for _, target := range watchlistMetadataProbeTargets {
		sources = append(sources, target.source)
	}
	return sources
}

func watchlistMetadataProbeTargetOf(source string) (watchlistMetadataProbeTarget, bool) {
	name := strings.TrimSpace(strings.ToLower(source))
	for _, target := range watchlistMetadataProbeTargets {
		if target.source == name {
			return target, true
		}
	}
	return watchlistMetadataProbeTarget{}, false
}

// ProbeWatchlistMetadataSource 对一个在线资料源发一次真实请求，回答「这个源现在
// 能不能用」，并把四种成因分开：凭证缺失、凭证无效、代理不通、网络不可达。
//
// 分开是承重的：这四件事的动手方向完全不同（去填 Key / 去换 Key / 去看代理 / 去看
// 网络），混成一句「连接失败」用户只能挨个试。分类码由适配器在出网那一刻认定
// （D-WM14），这里只负责翻译成一句能照着做的话。
func ProbeWatchlistMetadataSource(ctx context.Context, input WatchlistMetadataProbeInput) WatchlistMetadataProbeResult {
	target, ok := watchlistMetadataProbeTargetOf(input.Source)
	result := WatchlistMetadataProbeResult{Source: target.source}
	if !ok {
		result.Source = strings.TrimSpace(input.Source)
		result.Message = "未知的资料源，无法探测"
		return result
	}

	config := mergeWatchlistMetadataProbeConfig(input)
	result.ProxyURL = describeWatchlistMetadataProxy(config.ProxyURL)

	// 代理地址填错在装配阶段就被拦下，压根不会发请求。这时先报代理——它对每个源
	// 都是坏的，先修它才谈得上别的。
	registry, err := NewWatchlistMetadataRegistry(config, watchlistMetadataProbeTimeout)
	if err != nil {
		result.Failure = string(WatchlistMetadataFailureProxyUnreachable)
		// 这条错误是 parseWatchlistMetadataProxyURL 写的，已经过 describeProxyURL
		// 脱敏，而且本身就写清了「缺协议前缀」「没有主机名」这类具体毛病——比任何
		// 转述都准，原样带出。
		result.Message = "代理地址无效：" + err.Error() + "。改好「资料源出网代理」后再测"
		return result
	}
	source, err := watchlistMetadataProbeSourceFrom(registry, target)
	if err != nil {
		result.Failure = string(WatchlistMetadataFailureSourceError)
		result.Message = "装配资料源失败：" + err.Error()
		return result
	}

	ctx, cancel := context.WithTimeout(ctx, watchlistMetadataProbeTimeout)
	defer cancel()
	started := time.Now()
	candidates, err := source.Search(ctx, target.kind, target.query)
	result.LatencyMS = time.Since(started).Milliseconds()
	if err == nil {
		result.OK = true
		result.Message = fmt.Sprintf("连接正常，已取到 %d 条结果（%d ms）", len(candidates), result.LatencyMS)
		return result
	}

	failure := WatchlistMetadataFailureOf(err)
	var sourceErr *WatchlistMetadataSourceError
	if errors.As(err, &sourceErr) {
		result.HTTPStatus = sourceErr.HTTPStatus
	}
	// 「源里没有这个条目」恰恰证明请求走通了：网络、代理、凭证都没问题。探测问的
	// 是通不通，不是收没收录，所以这一类算成功。
	if failure == WatchlistMetadataFailureNotFound {
		result.OK = true
		result.Message = fmt.Sprintf("连接正常，源已响应（探测关键词无结果，%d ms）", result.LatencyMS)
		return result
	}

	result.Failure = string(failure)
	detail := ""
	if sourceErr != nil {
		// 只取 Detail：它是源站自己的文案（TMDB 的 status_message、Bangumi 的
		// title/description 之类），不含用户凭证。**不取 Unwrap 出来的原因**——
		// 那一层可能带着请求 URL，而凭证就在 query 里。
		detail = sourceErr.Detail
	}
	result.Message = describeWatchlistMetadataProbeError(target, result.Failure, result.HTTPStatus, detail)
	return result
}

// mergeWatchlistMetadataProbeConfig 把表单值盖在已保存 / 环境变量配置上。
//
// 方向与 LoadWatchlistMetadataConfig 一致：**非空才覆盖**。留空即「用已保存的」，
// 这样用户只改一栏就能测那一栏，不必把整页重填一遍。
func mergeWatchlistMetadataProbeConfig(input WatchlistMetadataProbeInput) WatchlistMetadataConfig {
	config := LoadWatchlistMetadataConfig()
	if value := strings.TrimSpace(input.ProxyURL); value != "" {
		config.ProxyURL = value
	}
	if value := strings.TrimSpace(input.TMDBAPIKey); value != "" {
		config.TMDBAPIKey = value
	}
	if value := strings.TrimSpace(input.BangumiAccessToken); value != "" {
		config.BangumiAccessToken = value
	}
	if value := strings.TrimSpace(input.FANZAAPIID); value != "" {
		config.FANZAAPIID = value
	}
	if value := strings.TrimSpace(input.FANZAAffiliateID); value != "" {
		config.FANZAAffiliateID = value
	}
	return config
}

// watchlistMetadataProbeSourceFrom 从路由表里取出要探测的那个适配器。
//
// 走 Chain 而不是当场 new 一个：探测必须测到**补全链路真正会用的那一个**。自己
// new 一份的话，哪天路由表换了实现或改了装配参数，探测还在测老的那一份。
func watchlistMetadataProbeSourceFrom(registry *WatchlistMetadataRegistry, target watchlistMetadataProbeTarget) (WatchlistMetadataSource, error) {
	chain, err := registry.Chain(target.kind)
	if err != nil {
		return nil, err
	}
	for _, source := range chain {
		if source.Name() == target.source {
			return source, nil
		}
	}
	return nil, fmt.Errorf("%q 不在 %q 的适配器链上", target.source, string(target.kind))
}

// describeWatchlistMetadataProbeError 把分类码翻成一句能照着做的话。
//
// 每一句都要指向一个具体动作，而不是复述现象——用户点「测试连接」就是想知道
// 下一步该改哪里。
func describeWatchlistMetadataProbeError(target watchlistMetadataProbeTarget, failure string, status int, detail string) string {
	message := ""
	switch WatchlistMetadataFailure(failure) {
	case WatchlistMetadataFailureCredentialMissing:
		hint := target.credentialHint
		if hint == "" {
			hint = "该源凭证"
		}
		message = fmt.Sprintf("凭证缺失：还没填「%s」，填好后再测（凭证为空时不会发请求）", hint)
	case WatchlistMetadataFailureCredentialInvalid:
		hint := target.credentialHint
		if hint == "" {
			hint = "该源凭证"
		}
		message = fmt.Sprintf("凭证无效：源站拒绝了这套凭证，检查「%s」是否填错、已过期或没有开通权限", hint)
	case WatchlistMetadataFailureProxyUnreachable:
		message = "代理不通：检查「资料源出网代理」的地址与端口，以及本机代理程序是否在运行"
	case WatchlistMetadataFailureNetworkUnreachable:
		message = fmt.Sprintf("网络不可达：%d 秒内没能连上源站。国内直连这几个源大多要走代理，请在「资料源出网代理」填一个可用的代理地址", int(watchlistMetadataProbeTimeout/time.Second))
	default:
		message = "源站返回了异常响应，稍后再试；持续如此多半是源站改版或触发了反爬"
	}
	if status == 0 {
		// 压根没拿到响应。此时 Detail 是适配器自己的占位文案（「请求未能送达」
		// 之类），复述它只是加噪音——上面那句已经把该做的事说完了。
		return message
	}
	message += fmt.Sprintf("（HTTP %d）", status)
	// 有状态码才说明源站真答了话，这时的 Detail 是源站自己写的原因
	// （TMDB 的 status_message、Bangumi 的 title/description），带上最有用。
	if detail = strings.TrimSpace(detail); detail != "" {
		message += "。源站说明：" + detail
	}
	return message
}

// describeWatchlistMetadataProxy 给出可以安全回显与记录的代理地址描述。
// 空串表示直连。
func describeWatchlistMetadataProxy(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	// url.Parse 失败时给的是 nil，describeProxyURL 认这种形态并退回字符串规则。
	parsed, _ := url.Parse(value)
	return describeProxyURL(value, parsed)
}
