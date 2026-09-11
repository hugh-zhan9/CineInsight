package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// 想看片单在线补全的源适配器合同（D-WM01）。
//
// 依赖方向是单向的：WatchlistService → 源路由表 → 各源适配器 → net/http。本文件
// 与各适配器实现**不碰数据库、不反向依赖 WatchlistService**——落库、状态机和海报
// 归 WatchlistService。适配器只做一件事：向外部要数据，映射成这里的内部结构。
//
// 这条边界不是洁癖：Bangumi（anime）与 FANZA + JavBus（av）是后续独立接入的，
// 只有适配器不牵扯片单侧状态，它们才能各自加一个文件就挂上路由表。

// WatchlistMetadataKind 是想看条目的类型维度（D-WM02），取值只有下面五个。
// models.WatchlistEntry.Kind 存的是同样的字面量，调用方转换后传进来。
type WatchlistMetadataKind string

const (
	WatchlistMetadataKindMovie WatchlistMetadataKind = "movie"
	WatchlistMetadataKindTV    WatchlistMetadataKind = "tv"
	WatchlistMetadataKindAnime WatchlistMetadataKind = "anime"
	WatchlistMetadataKindShow  WatchlistMetadataKind = "show"
	WatchlistMetadataKindAV    WatchlistMetadataKind = "av"
)

// AllWatchlistMetadataKinds 返回 D-WM02 声明的全部类型。路由表用它来保证「每个
// 类型要么有链，要么明确报尚无适配器」，不会有第三种沉默的结果。
func AllWatchlistMetadataKinds() []WatchlistMetadataKind {
	return []WatchlistMetadataKind{
		WatchlistMetadataKindMovie,
		WatchlistMetadataKindTV,
		WatchlistMetadataKindAnime,
		WatchlistMetadataKindShow,
		WatchlistMetadataKindAV,
	}
}

// WatchlistMetadataSource 是一个外部资料源的适配器。
//
// 实现者只负责「问外部要数据并映射」，不落库、不下载海报、不改条目状态。所有
// 出网都必须用 NewWatchlistMetadataHTTPClient 拿到的客户端，不要自建 http.Client
// ——否则用户配的资料源出网代理只会对其中一部分请求生效。
type WatchlistMetadataSource interface {
	// Name 是写进 WatchlistEntry.SourceName 的源名（tmdb / bangumi / fanza / javbus）。
	// 「结果来自哪个源」要可追溯，这是唯一的依据。
	Name() string

	// Search 按片名或番号取候选，保持源给出的匹配度顺序。
	// 源明确表示没有收录时返回 not_found 分类的错误，而不是空切片加 nil——
	// 「查无此片」是一个要被上层区分对待的结果（D-WM07 的 AV 兜底只认它）。
	Search(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error)

	// Detail 按源条目 ID 取详情。ID 来自同一个源此前给出的 SourceItemID。
	Detail(ctx context.Context, kind WatchlistMetadataKind, sourceItemID string) (*WatchlistMetadataDetail, error)
}

// WatchlistMetadataCandidate 是一条待用户确认的候选。
//
// SourceItemID 是承重字段：它落到 WatchlistEntry.SourceItemID，供重查详情与去重。
// 映射时丢掉它，这条候选就再也回不到源上了。
type WatchlistMetadataCandidate struct {
	SourceName    string
	SourceItemID  string
	Title         string
	OriginalTitle string
	// Year 取不到时为 0，不猜。
	Year int
	// Overview 是源给的简介原文，不截断——要不要截断是展示方的事。
	Overview string
	Rating   float64
	// PosterURL 是源上的绝对地址。下载与落盘归 WatchlistService（D-WM09），
	// 适配器只负责把地址拼对。
	PosterURL string
}

// WatchlistMetadataDetail 是选定候选后取到的完整信息。
// 内嵌候选，保证 SourceItemID 这类承重字段在详情里也一定在。
type WatchlistMetadataDetail struct {
	WatchlistMetadataCandidate
	Genres    []string
	Directors []string
	Cast      []string
}

// WatchlistMetadataFailure 是 D-WM14 的失败分类码。字面量与 §五 的表格一致，
// 可直接存进 WatchlistEntry.EnrichmentError（size:32）。
//
// 六类**互不合并**。把配置问题和「查无此片」混为一谈会让用户排查错方向；而
// not_found 与其余五类的区分还是承重的——只有它触发 AV 的 JavBus 兜底（D-WM07）。
type WatchlistMetadataFailure string

const (
	// WatchlistMetadataFailureCredentialMissing 该源凭证为空。认定后**不发请求**。
	WatchlistMetadataFailureCredentialMissing WatchlistMetadataFailure = "credential_missing"
	// WatchlistMetadataFailureCredentialInvalid HTTP 401 / 403。
	WatchlistMetadataFailureCredentialInvalid WatchlistMetadataFailure = "credential_invalid"
	// WatchlistMetadataFailureProxyUnreachable 资料源出网代理连不上，或地址填错。
	WatchlistMetadataFailureProxyUnreachable WatchlistMetadataFailure = "proxy_unreachable"
	// WatchlistMetadataFailureNetworkUnreachable 超时、DNS、连接失败。
	WatchlistMetadataFailureNetworkUnreachable WatchlistMetadataFailure = "network_unreachable"
	// WatchlistMetadataFailureNotFound 源明确返回「无此条目」。唯一触发 AV 兜底的分类。
	WatchlistMetadataFailureNotFound WatchlistMetadataFailure = "not_found"
	// WatchlistMetadataFailureSourceError 其他非 2xx、响应无法解析。
	WatchlistMetadataFailureSourceError WatchlistMetadataFailure = "source_error"
)

// WatchlistMetadataSourceError 是适配器对外的唯一失败形态：带分类码，可被上层
// 稳定判定，不需要谁去解析错误文案。
//
// Error() **不包含** Err 的文本。凭证可能出现在请求 URL 的 query 里（TMDB 的
// api_key、FANZA 的 api_id 都是这种形态），而 *url.Error 会把整个 URL 带进
// Error()——原样冒泡等于把用户的 key 抄进日志和界面。需要原因时走 Unwrap，
// 各适配器有责任在包进来之前先抹掉自己的凭证。
type WatchlistMetadataSourceError struct {
	Source  string
	Failure WatchlistMetadataFailure
	// HTTPStatus 为 0 表示压根没拿到响应。
	HTTPStatus int
	Detail     string
	Err        error
}

func (e *WatchlistMetadataSourceError) Error() string {
	head := fmt.Sprintf("%s 补全失败（%s", e.Source, e.Failure)
	if e.HTTPStatus != 0 {
		head += fmt.Sprintf(", HTTP %d", e.HTTPStatus)
	}
	head += "）"
	if e.Detail != "" {
		head += "：" + e.Detail
	}
	return head
}

func (e *WatchlistMetadataSourceError) Unwrap() error { return e.Err }

func newWatchlistMetadataSourceError(source string, failure WatchlistMetadataFailure, status int, detail string, err error) *WatchlistMetadataSourceError {
	return &WatchlistMetadataSourceError{Source: source, Failure: failure, HTTPStatus: status, Detail: detail, Err: err}
}

// WatchlistMetadataFailureOf 取出错误携带的分类码。不是适配器产生的错误返回空串
// ——由调用方决定怎么记，这里不擅自归成 source_error 而掩盖「没分类」这件事。
func WatchlistMetadataFailureOf(err error) WatchlistMetadataFailure {
	var sourceErr *WatchlistMetadataSourceError
	if errors.As(err, &sourceErr) {
		return sourceErr.Failure
	}
	return ""
}

// classifyWatchlistMetadataTransportError 把 client.Do 的错误归类。
//
// 只分两类：代理这一段不通，还是别的网络问题。超时、DNS 失败、连接被拒都落到
// network_unreachable——D-WM14 把它们放在同一格里，这里不再细分。
func classifyWatchlistMetadataTransportError(err error) WatchlistMetadataFailure {
	if err == nil {
		return ""
	}
	// 代理地址填错在发请求之前就被 NewWatchlistMetadataHTTPClient 拦下。填错和连不上
	// 对用户是同一件待办——去看代理配置——所以归同一类。
	if errors.Is(err, ErrWatchlistMetadataProxyInvalid) {
		return WatchlistMetadataFailureProxyUnreachable
	}
	// net/http 在代理拨号失败时把错误包成 Op == "proxyconnect" 的 *net.OpError
	// （Transport.dialConn 的 wrapErr，Go issue 16997 起就是这个约定）。这是区分
	// 「代理不通」与「目标站不通」的唯一结构化依据：两者内层都可能是 connection
	// refused，只看内层分不开。
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "proxyconnect" {
		return WatchlistMetadataFailureProxyUnreachable
	}
	return WatchlistMetadataFailureNetworkUnreachable
}

// classifyWatchlistMetadataHTTPStatus 把非 2xx 状态码归类。
//
// 这里**不处理 404**：「无此条目」是每个源自己的语义，有的用 404，有的用 200 加
// 空结果，还有的用业务码。各适配器在调用本函数之前先认定自己的 not_found。
func classifyWatchlistMetadataHTTPStatus(status int) WatchlistMetadataFailure {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return WatchlistMetadataFailureCredentialInvalid
	default:
		return WatchlistMetadataFailureSourceError
	}
}

// watchlistMetadataSecretMask 是凭证在错误文案里的替身。
const watchlistMetadataSecretMask = "redacted"

// watchlistMetadataRedactedError 保住错误链，同时换掉会泄漏凭证的文案。
//
// 直接 fmt.Errorf("%w") 会把内层错误的文本原样带出来，而换成 fmt.Errorf("%s")
// 又会断掉链子，errors.Is(err, context.DeadlineExceeded) 这类判定就失效了。
// 两者都要，只能自己包一层。
type watchlistMetadataRedactedError struct {
	message string
	err     error
}

func (e *watchlistMetadataRedactedError) Error() string { return e.message }

func (e *watchlistMetadataRedactedError) Unwrap() error { return e.err }

// redactWatchlistMetadataSecret 把凭证从错误里抹干净，错误链不动。
//
// 抹两个地方，缺一不可：
//
//   - *url.Error.URL。凭证在 query 里（TMDB 的 api_key、FANZA 的 api_id 都是
//     这种形态），而 *url.Error 就是 client.Do 失败时的形态。只改这一个导出
//     字段，Op 与内层 Err 原封不动——errors.Is(err, context.DeadlineExceeded)、
//     *net.OpError 的 proxyconnect 判定都照常。这个 *url.Error 是本次请求刚
//     分配出来的，没有别处在引用它，就地改是安全的。
//     只抹最外层的 Error() 不够：谁顺着 Unwrap 打印一层，凭证就又出来了。
//   - 外层文案。给不是 *url.Error 的形态兜底。
//
// secret 为空时原样返回——没有要抹的东西就不要多包一层。
func redactWatchlistMetadataSecret(err error, secret string) error {
	if err == nil {
		return nil
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return err
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		urlErr.URL = strings.ReplaceAll(urlErr.URL, secret, watchlistMetadataSecretMask)
	}
	if message := err.Error(); strings.Contains(message, secret) {
		return &watchlistMetadataRedactedError{message: strings.ReplaceAll(message, secret, watchlistMetadataSecretMask), err: err}
	}
	return err
}
