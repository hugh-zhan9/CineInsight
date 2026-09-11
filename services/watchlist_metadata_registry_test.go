package services

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func newTestRegistry(t *testing.T, config WatchlistMetadataConfig) *WatchlistMetadataRegistry {
	t.Helper()
	registry, err := NewWatchlistMetadataRegistry(config, 5*time.Second)
	if err != nil {
		t.Fatalf("装配路由表失败: %v", err)
	}
	return registry
}

// 路由表按类型选链：TMDB 覆盖 movie / tv / show，Bangumi 覆盖 anime，
// av 是 FANZA 在前、JavBus 兜底在后的两跳链。链的**顺序就是合同**，所以这里
// 逐个位置比对源名，而不只是看链里有没有这两个源。
func TestWatchlistMetadataRegistryRoutesKindsToExpectedChains(t *testing.T) {
	registry := newTestRegistry(t, WatchlistMetadataConfig{TMDBAPIKey: tmdbTestAPIKey})

	for _, routed := range []struct {
		kind    WatchlistMetadataKind
		sources []string
	}{
		{WatchlistMetadataKindMovie, []string{WatchlistMetadataSourceTMDB}},
		{WatchlistMetadataKindTV, []string{WatchlistMetadataSourceTMDB}},
		{WatchlistMetadataKindShow, []string{WatchlistMetadataSourceTMDB}},
		{WatchlistMetadataKindAnime, []string{WatchlistMetadataSourceBangumi}},
		{WatchlistMetadataKindAV, []string{WatchlistMetadataSourceFANZA, WatchlistMetadataSourceJavBus}},
	} {
		t.Run(string(routed.kind), func(t *testing.T) {
			chain, err := registry.Chain(routed.kind)
			if err != nil {
				t.Fatalf("Chain(%s) 失败: %v", routed.kind, err)
			}
			if len(chain) != len(routed.sources) {
				t.Fatalf("链长 = %d，期望 %d", len(chain), len(routed.sources))
			}
			for index, want := range routed.sources {
				if got := chain[index].Name(); got != want {
					t.Errorf("链上第 %d 个源 = %q，期望 %q", index+1, got, want)
				}
			}
		})
	}

	// 五个类型都登记了之后，「尚无适配器」只剩下认不出类型这一种触发方式。
	t.Run("未知类型", func(t *testing.T) {
		kind := WatchlistMetadataKind("bogus")
		chain, err := registry.Chain(kind)
		if !errors.Is(err, ErrWatchlistMetadataKindUnsupported) {
			t.Fatalf("Chain(%s) 错误 = %v，期望 ErrWatchlistMetadataKindUnsupported", kind, err)
		}
		if chain != nil {
			t.Errorf("报错时不应返回链，got %v", chain)
		}
		// 报错要说清是哪个类型，否则用户拿到的是一句无处下手的话。
		if !strings.Contains(err.Error(), string(kind)) {
			t.Errorf("错误文案 %q 里没有类型名 %q", err.Error(), kind)
		}
	})
}

// D-WM02 声明的五个类型，一个都不能落在「既没有链、也没有明确报错」的缝里。
func TestWatchlistMetadataRegistryAnswersEveryDeclaredKind(t *testing.T) {
	registry := newTestRegistry(t, WatchlistMetadataConfig{TMDBAPIKey: tmdbTestAPIKey})

	kinds := AllWatchlistMetadataKinds()
	if len(kinds) != 5 {
		t.Fatalf("类型数 = %d，期望 5（movie/tv/anime/show/av）", len(kinds))
	}
	for _, kind := range kinds {
		chain, err := registry.Chain(kind)
		switch {
		case err == nil && len(chain) == 0:
			t.Errorf("Chain(%s) 既没报错也没给链", kind)
		case err != nil && !errors.Is(err, ErrWatchlistMetadataKindUnsupported):
			t.Errorf("Chain(%s) 报了预期之外的错误: %v", kind, err)
		}
	}

	// 完全不认识的类型走同一条路：明确报错，不 panic、不返回空链加 nil。
	if _, err := registry.Chain(WatchlistMetadataKind("bogus")); !errors.Is(err, ErrWatchlistMetadataKindUnsupported) {
		t.Errorf("未知类型的错误 = %v，期望 ErrWatchlistMetadataKindUnsupported", err)
	}
}

// 出网客户端建一次反复用：三条链共用同一个适配器实例，也就共用同一个连接池。
func TestWatchlistMetadataRegistryReusesOneSourceInstance(t *testing.T) {
	registry := newTestRegistry(t, WatchlistMetadataConfig{TMDBAPIKey: tmdbTestAPIKey})

	movie, err := registry.Chain(WatchlistMetadataKindMovie)
	if err != nil {
		t.Fatalf("Chain(movie) 失败: %v", err)
	}
	tv, err := registry.Chain(WatchlistMetadataKindTV)
	if err != nil {
		t.Fatalf("Chain(tv) 失败: %v", err)
	}
	show, err := registry.Chain(WatchlistMetadataKindShow)
	if err != nil {
		t.Fatalf("Chain(show) 失败: %v", err)
	}
	if movie[0] != tv[0] || tv[0] != show[0] {
		t.Fatal("三条链拿到了不同的适配器实例，出网客户端就被建了多份")
	}

	tmdb, ok := movie[0].(*TMDBWatchlistMetadataSource)
	if !ok {
		t.Fatalf("链首类型 = %T，期望 *TMDBWatchlistMetadataSource", movie[0])
	}
	// 客户端必须来自 NewWatchlistMetadataHTTPClient，不能是 nil 或自建的。
	if tmdb.client == nil {
		t.Fatal("适配器没有拿到出网客户端")
	}
	if tmdb.apiKey != tmdbTestAPIKey {
		t.Errorf("凭证 = %q，期望从配置里取到", tmdb.apiKey)
	}
}

// Chain 返回副本：调用方改自己手里的链，不能动到路由表。
func TestWatchlistMetadataRegistryChainIsACopy(t *testing.T) {
	registry := newTestRegistry(t, WatchlistMetadataConfig{TMDBAPIKey: tmdbTestAPIKey})

	chain, err := registry.Chain(WatchlistMetadataKindMovie)
	if err != nil {
		t.Fatalf("Chain 失败: %v", err)
	}
	chain[0] = nil

	again, err := registry.Chain(WatchlistMetadataKindMovie)
	if err != nil {
		t.Fatalf("Chain 失败: %v", err)
	}
	if again[0] == nil {
		t.Fatal("路由表被调用方改坏了")
	}
}

// 代理地址填错时装配失败，不静默退回直连——用户配了代理却裸奔出网是最不该
// 悄悄发生的事（P-002 的 ErrWatchlistMetadataProxyInvalid）。
func TestWatchlistMetadataRegistryRejectsInvalidProxy(t *testing.T) {
	registry, err := NewWatchlistMetadataRegistry(WatchlistMetadataConfig{
		TMDBAPIKey: tmdbTestAPIKey,
		ProxyURL:   "127.0.0.1:1080",
	}, 5*time.Second)
	if !errors.Is(err, ErrWatchlistMetadataProxyInvalid) {
		t.Fatalf("错误 = %v，期望 ErrWatchlistMetadataProxyInvalid", err)
	}
	if registry != nil {
		t.Error("装配失败时不应返回路由表")
	}
}

// 凭证为空照样能装配出路由表：credential_missing 是一个**运行期**分类，由适配器
// 在发请求前认定（D-WM14），不是装配期就拒绝——否则用户连「哪个源没配」都看不到。
func TestWatchlistMetadataRegistryBuildsWithoutCredentials(t *testing.T) {
	registry := newTestRegistry(t, WatchlistMetadataConfig{})

	chain, err := registry.Chain(WatchlistMetadataKindMovie)
	if err != nil {
		t.Fatalf("Chain 失败: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("链长 = %d，期望 1", len(chain))
	}
}

// av 这条链是本仓库第一条多跳链，装配上有两件事要钉死：顺序（FANZA 在前）
// 与共用同一个出网客户端。
func TestWatchlistMetadataRegistryWiresAVChain(t *testing.T) {
	registry := newTestRegistry(t, WatchlistMetadataConfig{
		TMDBAPIKey:       tmdbTestAPIKey,
		FANZAAPIID:       fanzaTestAPIID,
		FANZAAffiliateID: fanzaTestAffiliateID,
	})

	chain, err := registry.Chain(WatchlistMetadataKindAV)
	if err != nil {
		t.Fatalf("Chain(av) 失败: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("链长 = %d，期望 2（FANZA 首选 + JavBus 兜底）", len(chain))
	}

	fanza, ok := chain[0].(*FANZAWatchlistMetadataSource)
	if !ok {
		t.Fatalf("链首类型 = %T，期望 *FANZAWatchlistMetadataSource", chain[0])
	}
	javbus, ok := chain[1].(*JavBusWatchlistMetadataSource)
	if !ok {
		t.Fatalf("链尾类型 = %T，期望 *JavBusWatchlistMetadataSource", chain[1])
	}

	if fanza.apiID != fanzaTestAPIID || fanza.affiliateID != fanzaTestAffiliateID {
		t.Errorf("FANZA 凭证 = %q/%q，期望从配置里取到", fanza.apiID, fanza.affiliateID)
	}
	if fanza.baseURL != fanzaAPIBaseURL {
		t.Errorf("FANZA baseURL = %q，期望生产地址 %q", fanza.baseURL, fanzaAPIBaseURL)
	}
	if javbus.baseURL != javbusBaseURL {
		t.Errorf("JavBus baseURL = %q，期望生产地址 %q", javbus.baseURL, javbusBaseURL)
	}

	// 出网客户端建一次共用：链上两个源以及既有的 TMDB 必须是同一个实例，
	// 否则连接池和代理配置就散成了好几份。
	movie, err := registry.Chain(WatchlistMetadataKindMovie)
	if err != nil {
		t.Fatalf("Chain(movie) 失败: %v", err)
	}
	tmdb, ok := movie[0].(*TMDBWatchlistMetadataSource)
	if !ok {
		t.Fatalf("链首类型 = %T，期望 *TMDBWatchlistMetadataSource", movie[0])
	}
	if fanza.client == nil || javbus.client == nil {
		t.Fatal("av 链上的适配器没有拿到出网客户端")
	}
	if fanza.client != tmdb.client || javbus.client != tmdb.client {
		t.Error("av 链与 TMDB 拿到了不同的出网客户端，路由表建了多份")
	}
}

// 注册 av 不得动到此前已登记的 movie / tv / show / anime。
func TestWatchlistMetadataRegistryKeepsExistingRoutesAfterAV(t *testing.T) {
	registry := newTestRegistry(t, WatchlistMetadataConfig{TMDBAPIKey: tmdbTestAPIKey})

	for _, routed := range []struct {
		kind   WatchlistMetadataKind
		source string
	}{
		{WatchlistMetadataKindMovie, WatchlistMetadataSourceTMDB},
		{WatchlistMetadataKindTV, WatchlistMetadataSourceTMDB},
		{WatchlistMetadataKindShow, WatchlistMetadataSourceTMDB},
		{WatchlistMetadataKindAnime, WatchlistMetadataSourceBangumi},
	} {
		chain, err := registry.Chain(routed.kind)
		if err != nil {
			t.Fatalf("Chain(%s) 失败: %v", routed.kind, err)
		}
		if len(chain) != 1 || chain[0].Name() != routed.source {
			t.Errorf("Chain(%s) = %v，期望仍是单跳 %s", routed.kind, chain, routed.source)
		}
	}
}

// FANZA 凭证为空照样能装配出 av 链：credential_missing 是运行期分类，
// 装配期就把源摘掉会让「哪个源没配」彻底看不见，也会让 JavBus 顶上去
// 变成静默的首选源。
func TestWatchlistMetadataRegistryKeepsAVChainWithoutFANZACredentials(t *testing.T) {
	registry := newTestRegistry(t, WatchlistMetadataConfig{})

	chain, err := registry.Chain(WatchlistMetadataKindAV)
	if err != nil {
		t.Fatalf("Chain(av) 失败: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("链长 = %d，期望 2", len(chain))
	}
	if chain[0].Name() != WatchlistMetadataSourceFANZA {
		t.Errorf("链首 = %q，期望仍是 %q", chain[0].Name(), WatchlistMetadataSourceFANZA)
	}
}
