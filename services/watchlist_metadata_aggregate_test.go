package services

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// 本文件钉 av 的多源聚合：逐字段择优表（D-AVM03）、失败分类聚合（D-AVM10）、
// 番号归一化（D-AVM05）与单源故障隔离。
//
// 夹具复用 watchlist_enrichment_test.go 的 fakeWatchlistSource / staticWatchlistSource
// ——聚合器的职责与 HTTP 无关，用假适配器能把择优规则单独钉住，源站改版不会误伤。

// ---------- 夹具 ----------

// failingWatchlistSource 是一个恒定失败的源。
func failingWatchlistSource(name string, failure WatchlistMetadataFailure) *fakeWatchlistSource {
	err := newWatchlistMetadataSourceError(name, failure, 0, "测试构造的失败", nil)
	return &fakeWatchlistSource{
		name: name,
		searchFn: func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
			return nil, err
		},
	}
}

// panickingWatchlistSource 模拟适配器里的 panic。
func panickingWatchlistSource(name string) *fakeWatchlistSource {
	return &fakeWatchlistSource{
		name: name,
		searchFn: func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
			panic("适配器炸了")
		},
	}
}

// recordingWatchlistSource 记下它实际收到的 query，用来验证归一化。
func recordingWatchlistSource(name string, seen *atomic.Value, detail WatchlistMetadataDetail) *fakeWatchlistSource {
	source := staticWatchlistSource(name, detail)
	inner := source.searchFn
	source.searchFn = func(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
		seen.Store(query)
		return inner(ctx, kind, query)
	}
	return source
}

// avDetail 少写点样板：只填本次用得上的字段。
func avDetail(title, overview, poster string, year int, rating float64, genres, cast, directors []string) WatchlistMetadataDetail {
	return WatchlistMetadataDetail{
		WatchlistMetadataCandidate: WatchlistMetadataCandidate{
			Title: title, Overview: overview, PosterURL: poster, Year: year, Rating: rating,
		},
		Genres: genres, Cast: cast, Directors: directors,
	}
}

func aggregateForTest(t *testing.T, sources []WatchlistMetadataSource, query string) (*WatchlistMetadataDetail, map[string]string) {
	t.Helper()
	detail, fields, err := AggregateWatchlistMetadataDetail(context.Background(), sources, WatchlistMetadataKindAV, query)
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	return detail, fields
}

// ---------- 用例 ----------

// 三源各给互补字段：合并结果齐备，且每个字段的归属都记对了。
func TestAggregateWatchlistMetadataMergesComplementaryFields(t *testing.T) {
	sources := []WatchlistMetadataSource{
		// JavBus：有封面、导演、年份，但**没有简介**（2026-09-13 真实请求确认）。
		staticWatchlistSource(WatchlistMetadataSourceJavBus,
			avDetail("ABC-123 日文標題", "", "https://javbus/cover.jpg", 2021, 0, nil, nil, []string{"苺原"})),
		staticWatchlistSource(WatchlistMetadataSourceJav321,
			avDetail("jav321 タイトル", "日文簡介", "", 2020, 4.5, nil, []string{"葵つかさ"}, nil)),
		// airav：中文片名与中文简介，这是引入它的全部理由。
		staticWatchlistSource(WatchlistMetadataSourceAirav,
			avDetail("中文片名", "中文簡介", "", 0, 0, []string{"中文標籤"}, nil, nil)),
	}
	detail, fields := aggregateForTest(t, sources, "abc-123")

	checks := []struct {
		field string
		got   any
		want  any
	}{
		{"Title（→source_title）", detail.Title, "中文片名"},
		{"Overview", detail.Overview, "中文簡介"},
		{"PosterURL", detail.PosterURL, "https://javbus/cover.jpg"},
		{"Year", detail.Year, 2020},
		{"Rating", detail.Rating, 4.5},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v，期望 %v", c.field, c.got, c.want)
		}
	}
	if len(detail.Cast) != 1 || detail.Cast[0] != "葵つかさ" {
		t.Errorf("Cast = %v，期望取 jav321 的演员", detail.Cast)
	}
	if len(detail.Directors) != 1 || detail.Directors[0] != "苺原" {
		t.Errorf("Directors = %v，期望取 JavBus 的导演", detail.Directors)
	}
	if len(detail.Genres) != 1 || detail.Genres[0] != "中文標籤" {
		t.Errorf("Genres = %v，期望取 airav 的标签", detail.Genres)
	}

	want := map[string]string{
		watchlistAVFieldSourceTitle: WatchlistMetadataSourceAirav,
		watchlistAVFieldOverview:    WatchlistMetadataSourceAirav,
		watchlistAVFieldGenres:      WatchlistMetadataSourceAirav,
		watchlistAVFieldPosterPath:  WatchlistMetadataSourceJavBus,
		watchlistAVFieldDirectors:   WatchlistMetadataSourceJavBus,
		watchlistAVFieldYear:        WatchlistMetadataSourceJav321,
		watchlistAVFieldRating:      WatchlistMetadataSourceJav321,
		watchlistAVFieldCast:        WatchlistMetadataSourceJav321,
	}
	for field, wantSource := range want {
		if fields[field] != wantSource {
			t.Errorf("归属[%s] = %q，期望 %q", field, fields[field], wantSource)
		}
	}

	// 聚合结果不归属任何单一源；番号是跨源主键。
	if detail.SourceName != WatchlistMetadataSourceAggregate {
		t.Errorf("SourceName = %q，期望 %q", detail.SourceName, WatchlistMetadataSourceAggregate)
	}
	if detail.SourceItemID != "ABC-123" {
		t.Errorf("SourceItemID = %q，期望归一化后的 ABC-123", detail.SourceItemID)
	}
}

// 单源失败不阻断其余源：缺的字段由存活的源补上。
func TestAggregateWatchlistMetadataToleratesSingleSourceFailure(t *testing.T) {
	sources := []WatchlistMetadataSource{
		staticWatchlistSource(WatchlistMetadataSourceJavBus,
			avDetail("日文標題", "", "https://javbus/cover.jpg", 0, 0, nil, nil, nil)),
		failingWatchlistSource(WatchlistMetadataSourceJav321, WatchlistMetadataFailureSourceError),
		staticWatchlistSource(WatchlistMetadataSourceAirav,
			avDetail("中文片名", "中文簡介", "", 0, 0, nil, nil, nil)),
	}
	detail, fields := aggregateForTest(t, sources, "ABC-123")

	if detail.Overview != "中文簡介" {
		t.Errorf("Overview = %q，期望由存活的 airav 补上", detail.Overview)
	}
	if detail.PosterURL != "https://javbus/cover.jpg" {
		t.Errorf("PosterURL = %q，期望由存活的 JavBus 补上", detail.PosterURL)
	}
	for field, source := range fields {
		if source == WatchlistMetadataSourceJav321 {
			t.Errorf("归属[%s] = jav321，但该源本次失败了", field)
		}
	}
}

// 一个源 panic 不得带倒其余源——聚合的全部价值就在于单源故障可以被吞掉。
func TestAggregateWatchlistMetadataRecoversSourcePanic(t *testing.T) {
	sources := []WatchlistMetadataSource{
		panickingWatchlistSource(WatchlistMetadataSourceJavBus),
		staticWatchlistSource(WatchlistMetadataSourceAirav, avDetail("中文片名", "", "", 0, 0, nil, nil, nil)),
	}
	detail, fields := aggregateForTest(t, sources, "ABC-123")
	if detail.Title != "中文片名" {
		t.Errorf("Title = %q，期望 panic 的源被吞掉、airav 照常产出", detail.Title)
	}
	if fields[watchlistAVFieldSourceTitle] != WatchlistMetadataSourceAirav {
		t.Errorf("归属[source_title] = %q，期望 airav", fields[watchlistAVFieldSourceTitle])
	}
}

// 全部源都说「没有这个番号」才上报 not_found。
func TestAggregateWatchlistMetadataAllNotFound(t *testing.T) {
	sources := []WatchlistMetadataSource{
		failingWatchlistSource(WatchlistMetadataSourceJavBus, WatchlistMetadataFailureNotFound),
		failingWatchlistSource(WatchlistMetadataSourceJav321, WatchlistMetadataFailureNotFound),
		failingWatchlistSource(WatchlistMetadataSourceAirav, WatchlistMetadataFailureNotFound),
	}
	_, _, err := AggregateWatchlistMetadataDetail(context.Background(), sources, WatchlistMetadataKindAV, "ABC-123")
	if got := WatchlistMetadataFailureOf(err); got != WatchlistMetadataFailureNotFound {
		t.Errorf("失败分类 = %q，期望 not_found", got)
	}
}

// 混合失败：代理不通压过「查无此片」（D-AVM10）。
//
// 这是要害——把配置问题伪装成查无此片，会让用户照着「换个番号试试」排查，
// 而真正该做的是去修代理。
func TestAggregateWatchlistMetadataPrefersActionableFailure(t *testing.T) {
	sources := []WatchlistMetadataSource{
		failingWatchlistSource(WatchlistMetadataSourceJavBus, WatchlistMetadataFailureNotFound),
		failingWatchlistSource(WatchlistMetadataSourceJav321, WatchlistMetadataFailureNotFound),
		failingWatchlistSource(WatchlistMetadataSourceAirav, WatchlistMetadataFailureProxyUnreachable),
	}
	_, _, err := AggregateWatchlistMetadataDetail(context.Background(), sources, WatchlistMetadataKindAV, "ABC-123")
	if got := WatchlistMetadataFailureOf(err); got != WatchlistMetadataFailureProxyUnreachable {
		t.Errorf("失败分类 = %q，期望 proxy_unreachable（不得被 not_found 盖过）", got)
	}
}

// 同一字段两源都给值时，采纳结果由表决定且可复现，不是「谁先返回谁赢」。
func TestAggregateWatchlistMetadataResolvesConflictByTable(t *testing.T) {
	sources := []WatchlistMetadataSource{
		staticWatchlistSource(WatchlistMetadataSourceJavBus, avDetail("JavBus 的片名", "JavBus 的简介", "", 0, 0, nil, nil, nil)),
		staticWatchlistSource(WatchlistMetadataSourceAirav, avDetail("airav 的片名", "airav 的简介", "", 0, 0, nil, nil, nil)),
	}
	// 连跑多次：并发完成顺序不该影响结果。
	for i := 0; i < 20; i++ {
		detail, fields := aggregateForTest(t, sources, "ABC-123")
		if detail.Title != "airav 的片名" || fields[watchlistAVFieldSourceTitle] != WatchlistMetadataSourceAirav {
			t.Fatalf("第 %d 次：Title = %q 归属 = %q，期望稳定取 airav", i, detail.Title, fields[watchlistAVFieldSourceTitle])
		}
	}
}

// 发给各源的是**归一化后**的番号，不是用户的原始输入。
func TestAggregateWatchlistMetadataNormalizesQuery(t *testing.T) {
	seen := &atomic.Value{}
	sources := []WatchlistMetadataSource{
		recordingWatchlistSource(WatchlistMetadataSourceJavBus, seen, avDetail("片名", "", "", 0, 0, nil, nil, nil)),
	}
	aggregateForTest(t, sources, "  abc_123 ")
	if got := seen.Load(); got != "ABC-123" {
		t.Errorf("源收到的 query = %v，期望归一化后的 ABC-123", got)
	}
}

// 只有一个源时聚合照常工作——这是 P-001 落地时的实际形态（av 只挂 JavBus）。
func TestAggregateWatchlistMetadataSingleSource(t *testing.T) {
	sources := []WatchlistMetadataSource{
		staticWatchlistSource(WatchlistMetadataSourceJavBus,
			avDetail("日文標題", "", "https://javbus/cover.jpg", 2021, 0, []string{"標籤"}, []string{"演員"}, []string{"導演"})),
	}
	detail, fields := aggregateForTest(t, sources, "ABC-123")
	if detail.Title != "日文標題" || detail.Year != 2021 {
		t.Errorf("单源结果不完整: Title=%q Year=%d", detail.Title, detail.Year)
	}
	// 简介 JavBus 给不了，也就没有归属——不是记一个空值。
	if _, ok := fields[watchlistAVFieldOverview]; ok {
		t.Errorf("归属里不该有 overview：JavBus 没有简介，期望该键缺席，实际 = %q", fields[watchlistAVFieldOverview])
	}
	if fields[watchlistAVFieldSourceTitle] != WatchlistMetadataSourceJavBus {
		t.Errorf("归属[source_title] = %q，期望 javbus", fields[watchlistAVFieldSourceTitle])
	}
}

// 空源清单与「没登记这个类型」是同一件事，给同一条错误。
func TestAggregateWatchlistMetadataEmptySources(t *testing.T) {
	_, _, err := AggregateWatchlistMetadataDetail(context.Background(), nil, WatchlistMetadataKindAV, "ABC-123")
	if !errors.Is(err, ErrWatchlistMetadataKindUnsupported) {
		t.Errorf("err = %v，期望 ErrWatchlistMetadataKindUnsupported", err)
	}
}

// 归属表编码：空表存空串，不存 "{}"——列的默认值就是空串，两种「没有」不该长成两个样子。
func TestEncodeWatchlistSourceFields(t *testing.T) {
	if got := encodeWatchlistSourceFields(nil); got != "" {
		t.Errorf("空归属编码 = %q，期望空串", got)
	}
	if got := encodeWatchlistSourceFields(map[string]string{}); got != "" {
		t.Errorf("空表编码 = %q，期望空串", got)
	}
	got := encodeWatchlistSourceFields(map[string]string{watchlistAVFieldOverview: WatchlistMetadataSourceAirav})
	if got != `{"overview":"airav"}` {
		t.Errorf("编码 = %q，期望 {\"overview\":\"airav\"}", got)
	}
}
