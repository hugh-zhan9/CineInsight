package services

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// ErrWatchlistMetadataKindUnsupported 表示这个类型还没有接上适配器。
//
// 它是一个**明确的结果**，不是占位。D-WM02 的五个类型现在都已登记，这条错误留给
// 两种情形：传进来一个不认识的类型，以及将来新增了类型却忘了配链。路由表宁可让
// 调用方拿到这条错误，也不要给一个什么都查不到的假适配器——那会让「源里没有这部片」
// 和「我们还没接这个源」在用户界面上长得一模一样。
var ErrWatchlistMetadataKindUnsupported = errors.New("该类型尚无在线资料源适配器")

// WatchlistMetadataRegistry 是按类型选链的路由表（D-WM01）。
//
// movie 按豆瓣 → TMDB 走链，其余非 av 类型为单跳；只有 not_found 触发兜底。
// av 不走链走聚合：链上所有源会被**并发全问一遍**，再逐字段择优，
// 所以那一行的顺序是择优兜底序而非询问序（见 watchlist_metadata_aggregate.go）。
//
// 路由表只回答「这个类型该问谁」，怎么走由 lookupWatchlistDetail 按类型分流。
type WatchlistMetadataRegistry struct {
	chains map[WatchlistMetadataKind][]WatchlistMetadataSource
}

// NewWatchlistMetadataRegistry 按配置装配路由表。
//
// 出网客户端在这里**建一次**，交给链上所有适配器共用：NewWatchlistMetadataHTTPClient
// 返回的客户端自带连接池，每个请求建一个会把池子废掉，也会让代理配置散成好几份。
//
// timeout 是单次请求的总超时，由调用方按用途给——资料查询要短，这里不替它决定。
//
// 代理地址填错时直接返回 ErrWatchlistMetadataProxyInvalid，不退回直连：用户配了
// 代理却让请求从本机裸奔出去，是最不该悄悄发生的事。
func NewWatchlistMetadataRegistry(config WatchlistMetadataConfig, timeout time.Duration) (*WatchlistMetadataRegistry, error) {
	client, err := NewWatchlistMetadataHTTPClient(config, timeout)
	if err != nil {
		return nil, err
	}

	tmdb := NewTMDBWatchlistMetadataSource(config.TMDBAPIKey, client)
	// Bangumi 的 token 允许为空：它的读接口匿名可读，缺 token 不妨碍装配，也不妨碍
	// 发请求（见 BangumiWatchlistMetadataSource.do）。
	bangumi := NewBangumiWatchlistMetadataSource(config.BangumiAccessToken, client)
	// av 的源都不需要凭证。
	javbus := NewJavBusWatchlistMetadataSource(client)
	jav321 := NewJav321WatchlistMetadataSource(client)
	fc2 := NewFC2WatchlistMetadataSource(client)
	// 这张表就是路由合同本身。接入新源时在这里加一行，不需要改 Chain，也不需要
	// 改任何适配器。
	//
	// **av 与其余类型的走法不同**：av 走聚合（并发问所有源、逐字段择优），
	// 其余走链（第一个成功就停）。分流在 lookupWatchlistDetail 里，不在这张表上。
	// 因此 av 这一行的顺序不是「询问顺序」，而是逐字段择优表没覆盖到的字段的
	// **兜底优先级**——择优表本身在 watchlist_metadata_aggregate.go。
	//
	// av 上挂三个源：JavBus + jav321 管片商番号，FC2 管 FC2-PPV。
	//
	// **番号形态的分流不在这里，也不在聚合器里**——三个源都会被问到，各自认自己的
	// 番号：FC2 适配器见到片商番号当场 not_found 且不发请求，JavBus / jav321 遇到
	// FC2 番号自然 404。聚合器既有的「单源失败不阻断其余源」原样处理，不需要第二张
	// 路由表。代价是一次查询会白跑两个必然失败的请求，换掉了一处隐藏的分流判断。
	return newWatchlistMetadataRegistry(map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {NewDoubanWatchlistMetadataSource(client), tmdb},
		WatchlistMetadataKindTV:    {tmdb},
		WatchlistMetadataKindShow:  {tmdb},
		WatchlistMetadataKindAnime: {bangumi},
		WatchlistMetadataKindAV:    {javbus, jav321, fc2},
	}), nil
}

func newWatchlistMetadataRegistry(chains map[WatchlistMetadataKind][]WatchlistMetadataSource) *WatchlistMetadataRegistry {
	return &WatchlistMetadataRegistry{chains: chains}
}

// Chain 返回该类型的适配器链，顺序即询问顺序。
//
// 未登记的类型返回 ErrWatchlistMetadataKindUnsupported。返回的是副本，调用方改
// 它不会动到路由表。
func (r *WatchlistMetadataRegistry) Chain(kind WatchlistMetadataKind) ([]WatchlistMetadataSource, error) {
	if r == nil || len(r.chains[kind]) == 0 {
		return nil, fmt.Errorf("%w：%q", ErrWatchlistMetadataKindUnsupported, string(kind))
	}
	return slices.Clone(r.chains[kind]), nil
}
