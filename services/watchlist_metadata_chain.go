package services

import (
	"context"
	"fmt"
)

// 有序兜底的走链策略（D-WM07）。
//
// 路由表只回答「这个类型该问谁、按什么顺序」，怎么走这条链是这里的事。兜底做在
// **路由层**而不是藏进首选源内部，为的是一件具体的事：结果来自哪个源必须可追溯。
// FANZA 内部悄悄转手问一次 JavBus，写进条目的 SourceName 就只能是 fanza，而那条
// 记录其实来自 JavBus——之后按 SourceName + SourceItemID 重查详情会查空。
// 走链拿到的候选与详情里的 SourceName 由**真正产出它的那个适配器**自己填，
// 这里一个字都不改。
//
// 兜底的触发条件只有一个：**上一个源明确返回 not_found**。
//
// 其余五类失败（credential_missing、credential_invalid、proxy_unreachable、
// network_unreachable、source_error）一律当场返回，不问下一个源。理由是两条：
//
//   - 把配置或网络问题伪装成「查无此片」，会让用户照着「换个番号试试」的方向排查，
//     而真正要做的是去填凭证或修代理。
//   - 凭证没配、代理不通这些毛病对链上每个源都一样，多打一次抓取请求换不来结果，
//     只是白白多一次对外记录。
//
// 没有分类码的错误（例如适配器拿到不该它接的类型）同样不兜底：那是调用方的问题，
// 换个源问一遍没有意义。

// SearchWatchlistMetadataChain 按链上顺序搜索，返回第一个成功的源给出的候选。
//
// chain 由 WatchlistMetadataRegistry.Chain 给出，顺序即询问顺序。
func SearchWatchlistMetadataChain(ctx context.Context, chain []WatchlistMetadataSource, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
	return walkWatchlistMetadataChain(chain, func(source WatchlistMetadataSource) ([]WatchlistMetadataCandidate, error) {
		return source.Search(ctx, kind, query)
	})
}

// DetailWatchlistMetadataChain 按链上顺序取详情，返回第一个成功的源给出的详情。
//
// 详情也走同一条链，规则一字不变。这一点值得说明：sourceItemID 是**某一个源**给的
// ID，拿去问另一个源通常对不上。但对不上的结果就是那个源说 not_found——代价是一次
// 白跑的请求，换来的是链对 ID 形态的宽容：FANZA 的 content_id（abc00123）与 JavBus
// 的番号（ABC-123）是两套写法，谁先认出来就由谁出详情，不需要在这里编一套没有
// 文档依据的换算规则。
func DetailWatchlistMetadataChain(ctx context.Context, chain []WatchlistMetadataSource, kind WatchlistMetadataKind, sourceItemID string) (*WatchlistMetadataDetail, error) {
	return walkWatchlistMetadataChain(chain, func(source WatchlistMetadataSource) (*WatchlistMetadataDetail, error) {
		return source.Detail(ctx, kind, sourceItemID)
	})
}

// walkWatchlistMetadataChain 是上面两个的公共走法：挨个问，第一个成功就返回；
// 只有 not_found 才继续问下一个；链走完了仍是 not_found，就把最后一跳的错误
// 原样交出去。
//
// 交出最后一跳的错误而不是另造一条：它的分类码本来就是 not_found（正是本轮要的
// 最终状态），而 Source 字段还如实记着最后问过谁。凭空合成一条错误只会把这点
// 线索抹掉。
func walkWatchlistMetadataChain[T any](chain []WatchlistMetadataSource, ask func(WatchlistMetadataSource) (T, error)) (T, error) {
	var zero T
	if len(chain) == 0 {
		// 空链与「没登记这个类型」是同一件事，给同一条错误，调用方只需判一次。
		return zero, fmt.Errorf("%w：链上没有可用的源", ErrWatchlistMetadataKindUnsupported)
	}

	var lastErr error
	for index, source := range chain {
		if source == nil {
			return zero, fmt.Errorf("%w：链上第 %d 个源为空", ErrWatchlistMetadataKindUnsupported, index+1)
		}
		result, err := ask(source)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if WatchlistMetadataFailureOf(err) != WatchlistMetadataFailureNotFound {
			// 唯一的提前退出：这五类换个源也是一样的结果，问下去只是白跑。
			return zero, err
		}
	}
	return zero, lastErr
}
