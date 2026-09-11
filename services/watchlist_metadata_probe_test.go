package services

import (
	"context"
	"net"
	"strings"
	"testing"

	"video-master/database"
	"video-master/models"
)

// probeTestSecrets 是所有用例共用的一组"凭证"。字面量里带 do-not-log，一旦它出现在
// 探测结果或错误文案里，断言的失败信息自己就说清了是什么问题。
const (
	probeTestTMDBKey         = "tmdb-key-do-not-log"
	probeTestBangumiToken    = "bangumi-token-do-not-log"
	probeTestFANZAAPIID      = "fanza-api-id-do-not-log"
	probeTestFANZAAffiliate  = "fanza-affiliate-do-not-log"
	probeTestProxyCredential = "proxy-password-do-not-log"
)

// clearWatchlistMetadataProbeEnv 把五个环境变量清空。
//
// 开发机上真配了 TMDB_API_KEY 之类的话，探测就会拿着真凭证去打真源站——用例会
// 因为外网状况时红时绿，还可能把真凭证写进测试输出。逐个清掉是唯一可靠的隔离。
func clearWatchlistMetadataProbeEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		envWatchlistMetadataProxyURL,
		envTMDBAPIKey,
		envBangumiAccessToken,
		envFANZAAPIID,
		envFANZAAffiliateID,
	} {
		t.Setenv(name, "")
	}
}

// 四种成因必须给出**互不相同**且指向具体动作的中文。混成一句"连接失败"的话，用户
// 只能挨个试：填 Key、换 Key、看代理、看网络是四个完全不同的方向。
func TestDescribeWatchlistMetadataProbeErrorSeparatesFourCauses(t *testing.T) {
	target, ok := watchlistMetadataProbeTargetOf(WatchlistMetadataSourceTMDB)
	if !ok {
		t.Fatal("tmdb 应当是可探测的源")
	}

	cases := []struct {
		failure WatchlistMetadataFailure
		// wants 是这句话必须提到的词：不校验整句文案（那会把用例变成复读机），
		// 只钉住"用户该去动哪里"这个承重信息。
		wants []string
	}{
		{WatchlistMetadataFailureCredentialMissing, []string{"凭证缺失", "TMDB API Key"}},
		{WatchlistMetadataFailureCredentialInvalid, []string{"凭证无效", "TMDB API Key"}},
		{WatchlistMetadataFailureProxyUnreachable, []string{"代理不通", "资料源出网代理"}},
		{WatchlistMetadataFailureNetworkUnreachable, []string{"网络不可达", "资料源出网代理"}},
	}

	seen := map[string]WatchlistMetadataFailure{}
	for _, testCase := range cases {
		message := describeWatchlistMetadataProbeError(target, string(testCase.failure), 0, "")
		for _, want := range testCase.wants {
			if !strings.Contains(message, want) {
				t.Fatalf("%s 的文案应当提到 %q，实际: %s", testCase.failure, want, message)
			}
		}
		if previous, duplicated := seen[message]; duplicated {
			t.Fatalf("%s 与 %s 给出了同一句文案: %s", testCase.failure, previous, message)
		}
		seen[message] = testCase.failure
	}
}

// 没拿到响应（status 0）时不复述适配器的占位文案；源站真答了话才把它的原因带上。
//
// 适配器对所有传输层失败都写 Detail="请求未能送达"，跟在"代理不通：检查……"后面
// 纯属噪音；而 401 带回来的 status_message 才是真正解释"为什么无效"的那句话。
func TestDescribeWatchlistMetadataProbeErrorAppendsSourceDetailOnlyWithStatus(t *testing.T) {
	target, _ := watchlistMetadataProbeTargetOf(WatchlistMetadataSourceTMDB)

	withoutStatus := describeWatchlistMetadataProbeError(target, string(WatchlistMetadataFailureNetworkUnreachable), 0, "请求未能送达")
	if strings.Contains(withoutStatus, "请求未能送达") || strings.Contains(withoutStatus, "HTTP") {
		t.Fatalf("没有响应时不该带占位文案或状态码，实际: %s", withoutStatus)
	}

	withStatus := describeWatchlistMetadataProbeError(target, string(WatchlistMetadataFailureCredentialInvalid), 401, "Invalid API key")
	if !strings.Contains(withStatus, "HTTP 401") || !strings.Contains(withStatus, "Invalid API key") {
		t.Fatalf("源站答话时应当带状态码与原因，实际: %s", withStatus)
	}
}

// 表单值覆盖已保存配置，留空则逐项回落——用户改一栏就能测那一栏，不必先保存。
func TestMergeWatchlistMetadataProbeConfigOverridesSavedPerField(t *testing.T) {
	setupVideoServiceTestDB(t)
	clearWatchlistMetadataProbeEnv(t)
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Updates(models.Settings{
		MetadataProxyURL:   "socks5://saved.example:1080",
		TMDBAPIKey:         "saved-tmdb",
		BangumiAccessToken: "saved-bangumi",
		FANZAAPIID:         "saved-fanza-api",
		FANZAAffiliateID:   "saved-fanza-affiliate",
	}).Error; err != nil {
		t.Fatalf("更新设置失败: %v", err)
	}

	// 只填 TMDB Key，其余留空。
	config := mergeWatchlistMetadataProbeConfig(WatchlistMetadataProbeInput{TMDBAPIKey: "  " + probeTestTMDBKey + "  "})
	if config.TMDBAPIKey != probeTestTMDBKey {
		t.Fatalf("表单值应当覆盖已保存值并去空白，实际: %q", config.TMDBAPIKey)
	}
	if config.ProxyURL != "socks5://saved.example:1080" || config.BangumiAccessToken != "saved-bangumi" ||
		config.FANZAAPIID != "saved-fanza-api" || config.FANZAAffiliateID != "saved-fanza-affiliate" {
		t.Fatalf("留空的字段应当逐项回落到已保存值，实际: %+v", config)
	}
}

// 代理地址回显必须脱敏：带口令的代理是常见形态，而这个字段既进界面也进日志。
func TestDescribeWatchlistMetadataProxyMasksCredential(t *testing.T) {
	if got := describeWatchlistMetadataProxy("   "); got != "" {
		t.Fatalf("空地址表示直连，应当回显空串，实际: %q", got)
	}

	masked := describeWatchlistMetadataProxy("socks5://user:" + probeTestProxyCredential + "@proxy.example:1080")
	if strings.Contains(masked, probeTestProxyCredential) {
		t.Fatalf("代理口令泄漏到回显里: %s", masked)
	}
	if !strings.Contains(masked, "proxy.example:1080") {
		t.Fatalf("应当保留主机以便用户认出自己填的地址，实际: %s", masked)
	}
}

// 代理地址填错时在装配阶段就被拦下，压根不发请求；文案要指名"资料源出网代理"。
//
// 这里同时钉住"不退回直连"：用户配了代理却让请求从本机裸奔出去，是最不该悄悄
// 发生的事。没有请求发出，也就没有裸奔的可能。
func TestProbeWatchlistMetadataSourceReportsInvalidProxy(t *testing.T) {
	setupVideoServiceTestDB(t)
	clearWatchlistMetadataProbeEnv(t)

	result := ProbeWatchlistMetadataSource(context.Background(), WatchlistMetadataProbeInput{
		Source:     WatchlistMetadataSourceTMDB,
		ProxyURL:   "127.0.0.1:1080", // 缺协议前缀
		TMDBAPIKey: probeTestTMDBKey,
	})
	if result.OK {
		t.Fatal("代理地址无效时探测不该判成功")
	}
	if result.Failure != string(WatchlistMetadataFailureProxyUnreachable) {
		t.Fatalf("期望 proxy_unreachable，实际: %q", result.Failure)
	}
	if !strings.Contains(result.Message, "资料源出网代理") {
		t.Fatalf("文案应当指名要改哪一栏，实际: %s", result.Message)
	}
	assertNoWatchlistMetadataProbeSecret(t, result)
}

// 代理连得上地址、连不上端口时归 proxy_unreachable，而不是 network_unreachable。
//
// 两者内层都可能是 connection refused，只看内层分不开；区分依据是 net/http 在
// 代理拨号失败时包出的 Op=="proxyconnect"。这条用例不碰外网：请求确实发了出去，
// 但它先要过代理，而代理端口已经关了。
func TestProbeWatchlistMetadataSourceReportsDeadProxy(t *testing.T) {
	setupVideoServiceTestDB(t)
	clearWatchlistMetadataProbeEnv(t)

	// 开一个监听再立刻关掉，拿到一个确定没人监听的端口。直接写死端口号会在
	// 别的进程恰好占用时变成"连上了"，用例就成了看运气。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("申请空闲端口失败: %v", err)
	}
	deadProxy := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("释放端口失败: %v", err)
	}

	result := ProbeWatchlistMetadataSource(context.Background(), WatchlistMetadataProbeInput{
		Source:     WatchlistMetadataSourceTMDB,
		ProxyURL:   deadProxy,
		TMDBAPIKey: probeTestTMDBKey,
	})
	if result.OK {
		t.Fatal("代理连不上时探测不该判成功")
	}
	if result.Failure != string(WatchlistMetadataFailureProxyUnreachable) {
		t.Fatalf("期望 proxy_unreachable，实际: %q（%s）", result.Failure, result.Message)
	}
	assertNoWatchlistMetadataProbeSecret(t, result)
}

// 凭证缺失是**不发请求**就能认定的（D-WM14）。这条用例因此完全不碰外网，
// 也正因为不碰外网，它同时证明了"缺凭证时确实没发请求"——真发了的话，
// 没有网络的环境里拿到的会是 network_unreachable。
func TestProbeWatchlistMetadataSourceReportsMissingCredential(t *testing.T) {
	setupVideoServiceTestDB(t)
	clearWatchlistMetadataProbeEnv(t)

	for _, testCase := range []struct {
		source string
		hint   string
	}{
		{WatchlistMetadataSourceTMDB, "TMDB API Key"},
		{WatchlistMetadataSourceFANZA, "FANZA API ID"},
	} {
		result := ProbeWatchlistMetadataSource(context.Background(), WatchlistMetadataProbeInput{Source: testCase.source})
		if result.OK {
			t.Fatalf("%s 缺凭证时不该判成功", testCase.source)
		}
		if result.Failure != string(WatchlistMetadataFailureCredentialMissing) {
			t.Fatalf("%s 期望 credential_missing，实际: %q（%s）", testCase.source, result.Failure, result.Message)
		}
		if !strings.Contains(result.Message, testCase.hint) {
			t.Fatalf("%s 的文案应当指名要填哪一栏，实际: %s", testCase.source, result.Message)
		}
		if result.Source != testCase.source {
			t.Fatalf("结果应当回显被测源名，实际: %q", result.Source)
		}
	}
}

// 探测的是补全链路**真正会用的那一个**适配器：走路由表取，不当场 new 一份。
// 自己 new 的话，路由表哪天换了装配参数，探测还在测老的那一份。
func TestWatchlistMetadataProbeTargetsResolveThroughRegistry(t *testing.T) {
	registry, err := NewWatchlistMetadataRegistry(WatchlistMetadataConfig{}, watchlistMetadataProbeTimeout)
	if err != nil {
		t.Fatalf("装配路由表失败: %v", err)
	}
	for _, target := range watchlistMetadataProbeTargets {
		source, err := watchlistMetadataProbeSourceFrom(registry, target)
		if err != nil {
			t.Fatalf("%s 应当能从 %s 链上取到，实际: %v", target.source, target.kind, err)
		}
		if source.Name() != target.source {
			t.Fatalf("取到的是 %q，期望 %q", source.Name(), target.source)
		}
	}
	if len(watchlistMetadataProbeTargets) != len(AllWatchlistMetadataProbeSources()) {
		t.Fatal("可探测源清单与目标表应当一一对应")
	}
}

// 不认识的源名要给一句人话，而不是 panic 或一个空结果。
func TestProbeWatchlistMetadataSourceRejectsUnknownSource(t *testing.T) {
	setupVideoServiceTestDB(t)
	clearWatchlistMetadataProbeEnv(t)

	result := ProbeWatchlistMetadataSource(context.Background(), WatchlistMetadataProbeInput{Source: "imdb"})
	if result.OK || !strings.Contains(result.Message, "未知的资料源") {
		t.Fatalf("未知源应当被拒绝并说明原因，实际: %+v", result)
	}
}

// 任何一条失败路径都不得把凭证带进结果——结果既回传给界面，也进日志。
func assertNoWatchlistMetadataProbeSecret(t *testing.T, result WatchlistMetadataProbeResult) {
	t.Helper()
	rendered := result.Source + "|" + result.Failure + "|" + result.ProxyURL + "|" + result.Message
	for _, secret := range []string{
		probeTestTMDBKey,
		probeTestBangumiToken,
		probeTestFANZAAPIID,
		probeTestFANZAAffiliate,
		probeTestProxyCredential,
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("凭证 %q 泄漏进了探测结果: %s", secret, rendered)
		}
	}
}

// 每个源都走一遍全部凭证都填上的失败路径，确认结果里一个凭证字符都没有。
//
// 上面几条用例各自只覆盖一条路径；这一条横着扫一遍，防止将来某个源单独加了
// 回显字段时漏掉脱敏。代理指向一个死端口，保证四个源都在同一个失败分类上。
func TestProbeWatchlistMetadataSourceNeverLeaksCredentials(t *testing.T) {
	setupVideoServiceTestDB(t)
	clearWatchlistMetadataProbeEnv(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("申请空闲端口失败: %v", err)
	}
	deadProxy := "http://user:" + probeTestProxyCredential + "@" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("释放端口失败: %v", err)
	}

	for _, source := range AllWatchlistMetadataProbeSources() {
		result := ProbeWatchlistMetadataSource(context.Background(), WatchlistMetadataProbeInput{
			Source:             source,
			ProxyURL:           deadProxy,
			TMDBAPIKey:         probeTestTMDBKey,
			BangumiAccessToken: probeTestBangumiToken,
			FANZAAPIID:         probeTestFANZAAPIID,
			FANZAAffiliateID:   probeTestFANZAAffiliate,
		})
		if result.OK {
			t.Fatalf("%s 走死代理不该判成功", source)
		}
		assertNoWatchlistMetadataProbeSecret(t, result)
	}
}
