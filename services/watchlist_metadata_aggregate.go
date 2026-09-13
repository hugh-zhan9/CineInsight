package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// av 的多源聚合（D-AVM01、D-AVM03、D-AVM10）。
//
// 与走链（watchlist_metadata_chain.go）是**两种策略并存**，不是替代：
// 走链「第一个成功就停」，整条记录归属单一源，是 movie/tv/show/anime 的合同；
// 聚合「并问所有源、逐字段择优」，只作用于 av。分流点只有一处，在
// lookupWatchlistDetail 里。
//
// 为什么 av 要聚合：没有任何一家能填满这张表。JavBus 的详情页压根没有简介
// （watchlist_metadata_javbus.go 里 Overview 写死为空，2026-09-13 真实请求确认），
// 而 airav 给中文片名与中文简介。单源补全出来的 av 条目，简介永远是空的。
//
// 本文件**不碰数据库、不下海报、不改条目状态**，与各适配器同一条边界。

const (
	// WatchlistMetadataSourceAggregate 是聚合结果写进 WatchlistEntry.SourceName 的值。
	//
	// 它是一个保留字，不对应任何真实源——因为逐字段择优之后，这条记录本来就不
	// 归属任何单一源。「哪个字段来自哪个源」记在 SourceFields 里。
	// 长度 9 字节，装得进 SourceName 的 size:16。
	WatchlistMetadataSourceAggregate = "aggregate"

	// 下面两个源名由 jav321 / airav 适配器接入时复用（它们还没写）。
	// 名字先在这里定下来，是因为优先级表现在就要按名字排序——把它们散到各自的
	// 适配器文件里再回头引用，只会让这张表在两个文件之间来回跳。
	WatchlistMetadataSourceJav321 = "jav321"
	WatchlistMetadataSourceAirav  = "airav"
)

// 逐字段择优表（D-AVM03）。键是**条目列名**，值是按优先级排开的源名。
//
// 规则只有两条，都可复现：
//
//   - 列在表里的字段，按它自己的顺序取第一个非空值；
//   - 没列到的字段，按 sources 传进来的顺序取第一个非空值（顺序由路由表决定）。
//
// 刻意**不做**「取更长的简介」「取分辨率更高的图」这类质量启发式：说不清为什么，
// 也复现不了，而且更长不等于更好。
//
// 这张表是代码常量，不做成用户可配——可配意味着要设计配置界面、校验、迁移和
// 「配错了怎么办」，而没有任何需求提出过这件事。
//
// 表里只列**真正能落库的字段**。适配器结构体上的 OriginalTitle（日文原题）不在
// 这里：本仓库没有存它的列，为它排优先级等于在为观察不到的事排序。
var watchlistAVFieldPriority = map[string][]string{
	// 界面是中文的，中文片名/简介/标签优先。
	watchlistAVFieldSourceTitle: {WatchlistMetadataSourceAirav, WatchlistMetadataSourceJav321, WatchlistMetadataSourceJavBus},
	watchlistAVFieldOverview:    {WatchlistMetadataSourceAirav, WatchlistMetadataSourceJav321, WatchlistMetadataSourceJavBus},
	watchlistAVFieldGenres:      {WatchlistMetadataSourceAirav, WatchlistMetadataSourceJav321, WatchlistMetadataSourceJavBus},
	// JavBus 的封面已经过真实请求验证可用。
	watchlistAVFieldPosterPath: {WatchlistMetadataSourceJavBus, WatchlistMetadataSourceJav321, WatchlistMetadataSourceAirav},
	// 三家都给发行日期与演员；jav321 的结构最规整。
	watchlistAVFieldYear: {WatchlistMetadataSourceJav321, WatchlistMetadataSourceAirav, WatchlistMetadataSourceJavBus},
	watchlistAVFieldCast: {WatchlistMetadataSourceJav321, WatchlistMetadataSourceAirav, WatchlistMetadataSourceJavBus},
	// jav321 详情页有「平均評価」（2026-09-13 真实页面确认）；另两家没有评分。
	watchlistAVFieldRating: {WatchlistMetadataSourceJav321, WatchlistMetadataSourceAirav, WatchlistMetadataSourceJavBus},
	// 只有 JavBus 明确解析导演（真实请求确认：導演: 苺原）。
	watchlistAVFieldDirectors: {WatchlistMetadataSourceJavBus, WatchlistMetadataSourceJav321, WatchlistMetadataSourceAirav},
}

// 归属表的键。用条目列名而不是适配器结构体的字段名——读这张表的人问的是
// 「库里这个值哪来的」，不是「适配器那个字段哪来的」。
//
// cast 与 directors 单列：它们一起编码进 credits 列，但完全可能来自不同的源
// （演员来自 jav321、导演来自 JavBus 就是当前的实际配置），合成一个 credits
// 键就说不清是谁给的了。
const (
	watchlistAVFieldSourceTitle = "source_title"
	watchlistAVFieldOverview    = "overview"
	watchlistAVFieldGenres      = "genres"
	watchlistAVFieldPosterPath  = "poster_path"
	watchlistAVFieldYear        = "year"
	watchlistAVFieldCast        = "cast"
	watchlistAVFieldDirectors   = "directors"
	watchlistAVFieldRating      = "rating"
)

// watchlistAVFailureRank 是全部源都失败时的上报顺序，越靠前越优先（D-AVM10）。
//
// 排序依据是**对用户的可操作性**：代理不通要去改配置，网络不通要看网络，
// 而「查无此片」什么也做不了。把配置问题伪装成查无此片，会让用户照着
// 「换个番号试试」排查，而真正该做的是去修代理。
//
// 只有全部源都报 not_found 时才上报 not_found——它排在最后就是这个意思。
var watchlistAVFailureRank = []WatchlistMetadataFailure{
	WatchlistMetadataFailureProxyUnreachable,
	WatchlistMetadataFailureNetworkUnreachable,
	WatchlistMetadataFailureCredentialInvalid,
	WatchlistMetadataFailureCredentialMissing,
	WatchlistMetadataFailureSourceError,
	WatchlistMetadataFailureNotFound,
}

// watchlistAggregateOutcome 是一个源问下来的结果，成败二选一。
type watchlistAggregateOutcome struct {
	source string
	detail *WatchlistMetadataDetail
	err    error
}

// AggregateWatchlistMetadataDetail 并发问所有源，逐字段择优合成一条详情。
//
// 返回的第二个值是**字段级归属**：键为条目列名，值为该字段最终采纳的源名。
// 它落到 WatchlistEntry.SourceFields，供诊断「这个简介到底哪来的」。
//
// 只要**有一个源成功**就算成功——另外两个炸了也不该让用户什么都拿不到。
// 全部失败时按 watchlistAVFailureRank 选一个分类上报。
//
// query 是用户手输的番号，这里统一归一化后再发给各源。
func AggregateWatchlistMetadataDetail(
	ctx context.Context,
	sources []WatchlistMetadataSource,
	kind WatchlistMetadataKind,
	query string,
) (*WatchlistMetadataDetail, map[string]string, error) {
	if len(sources) == 0 {
		// 与「没登记这个类型」是同一件事，给同一条错误，调用方只需判一次。
		return nil, nil, fmt.Errorf("%w：链上没有可用的源", ErrWatchlistMetadataKindUnsupported)
	}
	code := normalizeWatchlistAVCode(query)

	outcomes := askWatchlistAVSources(ctx, sources, kind, code)

	succeeded := make(map[string]*WatchlistMetadataDetail, len(outcomes))
	order := make([]string, 0, len(outcomes))
	var failures []error
	for _, outcome := range outcomes {
		if outcome.err != nil {
			failures = append(failures, outcome.err)
			continue
		}
		succeeded[outcome.source] = outcome.detail
		order = append(order, outcome.source)
	}
	if len(succeeded) == 0 {
		return nil, nil, pickWatchlistAVFailure(failures)
	}

	detail, fieldSources := mergeWatchlistAVDetails(succeeded, order)
	detail.SourceName = WatchlistMetadataSourceAggregate
	// 番号就是 av 的跨源主键，归一化后的它即是本条的源侧 ID。
	detail.SourceItemID = code
	log.Printf("[WatchlistMetadata] aggregate code=%s sources_ok=%d sources_failed=%d fields=%v",
		code, len(succeeded), len(failures), fieldSources)
	return detail, fieldSources, nil
}

// askWatchlistAVSources 并发问每个源，返回与 sources 同序的结果。
//
// 并发度就是源的个数（当前三个），不引入 worker pool——为三个请求建一套调度
// 设施是没有理由的开销。出网客户端由各适配器共用调用方注入的那一个，自带连接池。
func askWatchlistAVSources(
	ctx context.Context,
	sources []WatchlistMetadataSource,
	kind WatchlistMetadataKind,
	code string,
) []watchlistAggregateOutcome {
	outcomes := make([]watchlistAggregateOutcome, len(sources))
	var wait sync.WaitGroup
	for index, source := range sources {
		if source == nil {
			outcomes[index] = watchlistAggregateOutcome{
				err: fmt.Errorf("%w：链上第 %d 个源为空", ErrWatchlistMetadataKindUnsupported, index+1),
			}
			continue
		}
		wait.Add(1)
		go func(index int, source WatchlistMetadataSource) {
			defer wait.Done()
			name := source.Name()
			// 一个源 panic 不得带倒其余两个：聚合的全部价值就在于单源失败可以被吞掉。
			defer func() {
				if recovered := recover(); recovered != nil {
					outcomes[index] = watchlistAggregateOutcome{
						source: name,
						err: newWatchlistMetadataSourceError(name, WatchlistMetadataFailureSourceError, 0,
							fmt.Sprintf("适配器 panic：%v", recovered), nil),
					}
				}
			}()
			started := time.Now()
			detail, err := watchlistSourceDetail(ctx, source, kind, code)
			log.Printf("[WatchlistMetadata] aggregate source=%s code=%s elapsed_ms=%d failure=%s",
				name, code, time.Since(started).Milliseconds(), WatchlistMetadataFailureOf(err))
			outcomes[index] = watchlistAggregateOutcome{source: name, detail: detail, err: err}
		}(index, source)
	}
	wait.Wait()
	return outcomes
}

// mergeWatchlistAVDetails 按优先级表逐字段挑，并记下每个字段采纳了谁。
//
// order 是**实际成功**的源、按路由表给的顺序排列，用作优先级表没覆盖到的字段的
// 兜底顺序。
func mergeWatchlistAVDetails(succeeded map[string]*WatchlistMetadataDetail, order []string) (*WatchlistMetadataDetail, map[string]string) {
	merged := &WatchlistMetadataDetail{}
	fieldSources := make(map[string]string, len(watchlistAVFieldPriority))

	// take 走该字段的优先级顺序，把第一个非空值交给 assign 落位。
	take := func(field string, assign func(*WatchlistMetadataDetail) bool) {
		for _, name := range watchlistAVSourceOrder(field, order) {
			detail, ok := succeeded[name]
			if !ok {
				continue
			}
			if assign(detail) {
				fieldSources[field] = name
				return
			}
		}
	}

	take(watchlistAVFieldSourceTitle, func(d *WatchlistMetadataDetail) bool {
		if value := strings.TrimSpace(d.Title); value != "" {
			// 注意落点：源站片名进 SourceTitle 列，**不进条目的 title**。
			// 条目标题是用户手输的番号，补全一律不覆盖它。
			merged.Title = value
			return true
		}
		return false
	})
	take(watchlistAVFieldOverview, func(d *WatchlistMetadataDetail) bool {
		if value := strings.TrimSpace(d.Overview); value != "" {
			merged.Overview = value
			return true
		}
		return false
	})
	take(watchlistAVFieldGenres, func(d *WatchlistMetadataDetail) bool {
		if len(trimWatchlistNames(d.Genres)) > 0 {
			merged.Genres = d.Genres
			return true
		}
		return false
	})
	take(watchlistAVFieldPosterPath, func(d *WatchlistMetadataDetail) bool {
		if value := strings.TrimSpace(d.PosterURL); value != "" {
			merged.PosterURL = value
			return true
		}
		return false
	})
	take(watchlistAVFieldYear, func(d *WatchlistMetadataDetail) bool {
		if d.Year != 0 {
			merged.Year = d.Year
			return true
		}
		return false
	})
	take(watchlistAVFieldRating, func(d *WatchlistMetadataDetail) bool {
		if d.Rating != 0 {
			merged.Rating = d.Rating
			return true
		}
		return false
	})
	take(watchlistAVFieldCast, func(d *WatchlistMetadataDetail) bool {
		if len(trimWatchlistNames(d.Cast)) > 0 {
			merged.Cast = d.Cast
			return true
		}
		return false
	})
	take(watchlistAVFieldDirectors, func(d *WatchlistMetadataDetail) bool {
		if len(trimWatchlistNames(d.Directors)) > 0 {
			merged.Directors = d.Directors
			return true
		}
		return false
	})
	return merged, fieldSources
}

// watchlistAVSourceOrder 给出某个字段的询问顺序：表里配的排前面，其余按路由表顺序补在后面。
//
// 补在后面这一段是为了让「表里没列的源」也能被采纳，而不是因为没配就永远取不到值。
func watchlistAVSourceOrder(field string, fallback []string) []string {
	configured := watchlistAVFieldPriority[field]
	ordered := make([]string, 0, len(configured)+len(fallback))
	seen := make(map[string]struct{}, len(configured)+len(fallback))
	for _, name := range append(append([]string{}, configured...), fallback...) {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		ordered = append(ordered, name)
	}
	return ordered
}

// pickWatchlistAVFailure 从全部失败里挑一个最值得用户处理的上报（D-AVM10）。
//
// 返回的是那个源**原本的错误**，不另造一条：它的分类码、Source 字段与 HTTP 状态
// 都如实记着现场，凭空合成只会把线索抹掉。
func pickWatchlistAVFailure(failures []error) error {
	if len(failures) == 0 {
		// 调用方保证了失败集非空；真走到这里说明上面的判断被改坏了，如实说出来。
		return newWatchlistMetadataSourceError(WatchlistMetadataSourceAggregate,
			WatchlistMetadataFailureSourceError, 0, "没有任何源产出结果，也没有记录失败", nil)
	}
	for _, want := range watchlistAVFailureRank {
		for _, err := range failures {
			if WatchlistMetadataFailureOf(err) == want {
				return err
			}
		}
	}
	// 没有分类码的错误（例如适配器拿到不该它接的类型）落到这里，原样交出去。
	return failures[0]
}

// encodeWatchlistSourceFields 把字段归属编成 JSON。空表存空串而不是 "{}"——
// 列的默认值就是空串，两种「没有」不该在库里长成两个样子。
func encodeWatchlistSourceFields(fieldSources map[string]string) string {
	if len(fieldSources) == 0 {
		return ""
	}
	encoded, err := json.Marshal(fieldSources)
	if err != nil {
		log.Printf("[WatchlistEnrich] encode source fields failed err=%v", err)
		return ""
	}
	return string(encoded)
}

// watchlistAVSourceColumns 算出 source_title 与 source_fields 两列该写什么。
//
// **kind 边界在这里**（requirements D7）。写这两列的两条路径
// （settleEnrichmentSuccess 与 ApplyCandidate）都是全类型共用的，没有类型分支；
// 不在这里判一次，一条 movie 条目补全后就会跟着多出源站片名，界面上从「沙丘」
// 变成「沙丘 · Dune」——那是本次明确保证零变化的四个类型之一，而且现有的
// movie 测试一条都抓不到（新列不在任何既有断言里）。
//
// 非 av 一律返回两个空串，不是「保持原值」：类型是条目的一级维度，一条 movie
// 条目本就不该有源站片名。
//
// detail 为 nil 时同样返回空串——调用方在成功路径上不会传 nil，但这个函数没有
// 理由因为调用方的疏忽而 panic。
func watchlistAVSourceColumns(kind WatchlistMetadataKind, detail *WatchlistMetadataDetail, fieldSources map[string]string) (string, string) {
	if kind != WatchlistMetadataKindAV || detail == nil {
		return "", ""
	}
	return strings.TrimSpace(detail.Title), encodeWatchlistSourceFields(fieldSources)
}
