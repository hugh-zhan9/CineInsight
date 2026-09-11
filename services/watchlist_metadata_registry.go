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
// 链是**有序**的：排在前面的源先问。movie / tv / show / anime 各是单跳；av 是
// FANZA 在前、JavBus 兜底在后，且兜底只在前一个返回 not_found 时才走（D-WM07）。
// 路由表只回答「这个类型该问谁、按什么顺序」，走链的策略在
// SearchWatchlistMetadataChain / DetailWatchlistMetadataChain 里。
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
	// FANZA 的两个凭证允许为空，缺了在运行期判 credential_missing（D-WM14）；
	// JavBus 不需要任何凭证。两者都照常装配，否则用户连「哪个源没配」都看不到。
	fanza := NewFANZAWatchlistMetadataSource(config.FANZAAPIID, config.FANZAAffiliateID, client)
	javbus := NewJavBusWatchlistMetadataSource(client)
	// 这张表就是路由合同本身。接入新源时在这里加一行，不需要改 Chain，也不需要
	// 改任何适配器。
	//
	// av 是目前唯一的多跳链：FANZA 在前、JavBus 兜底在后。**顺序即合同**，而且
	// 兜底只在 FANZA 明确返回 not_found 时才走——那条策略在
	// walkWatchlistMetadataChain 里，不在这张表上，这里只负责把顺序排对。
	return newWatchlistMetadataRegistry(map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {tmdb},
		WatchlistMetadataKindTV:    {tmdb},
		WatchlistMetadataKindShow:  {tmdb},
		WatchlistMetadataKindAnime: {bangumi},
		WatchlistMetadataKindAV:    {fanza, javbus},
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
