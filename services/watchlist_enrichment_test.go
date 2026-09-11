package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

// 本文件按需求设计文档 §3.1 的转换表逐行验收，外加 §3.2 的并发与回滚边界。
// 每个用例注明它钉的是转换表的哪一行。

// ---------- 夹具 ----------

// fakeWatchlistSource 是一个可编排的源适配器，用来把状态机与出网解耦地测掉。
type fakeWatchlistSource struct {
	name     string
	searchFn func(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error)
	detailFn func(ctx context.Context, kind WatchlistMetadataKind, sourceItemID string) (*WatchlistMetadataDetail, error)
}

func (f *fakeWatchlistSource) Name() string { return f.name }

func (f *fakeWatchlistSource) Search(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
	return f.searchFn(ctx, kind, query)
}

func (f *fakeWatchlistSource) Detail(ctx context.Context, kind WatchlistMetadataKind, sourceItemID string) (*WatchlistMetadataDetail, error) {
	if f.detailFn == nil {
		return nil, newWatchlistMetadataSourceError(f.name, WatchlistMetadataFailureSourceError, 0, "测试未编排 Detail", nil)
	}
	return f.detailFn(ctx, kind, sourceItemID)
}

// staticWatchlistSource 是最常用的一种：搜到一个候选，详情固定。
func staticWatchlistSource(name string, detail WatchlistMetadataDetail) *fakeWatchlistSource {
	detail.SourceName = name
	return &fakeWatchlistSource{
		name: name,
		searchFn: func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
			return []WatchlistMetadataCandidate{detail.WatchlistMetadataCandidate}, nil
		},
		detailFn: func(context.Context, WatchlistMetadataKind, string) (*WatchlistMetadataDetail, error) {
			copied := detail
			return &copied, nil
		},
	}
}

// failingRoundTripper 让任何一次出网都直接把测试判红。credential_missing 的定义
// 包含「不发请求」，这是唯一能把"没发"证成事实的写法。
type failingRoundTripper struct {
	t *testing.T
}

func (f *failingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	f.t.Errorf("credential_missing 路径不得发出任何 HTTP 请求，却请求了 %s", request.URL.Host)
	return nil, errors.New("unexpected request")
}

// enrichTestHarness 把一个接好假源、假事件口与登记表的服务交给用例。
type enrichTestHarness struct {
	service  *WatchlistService
	dataDir  string
	registry *BackgroundTaskRegistry

	mu       sync.Mutex
	progress []WatchlistEnrichProgress
	tasks    [][]string
}

func newEnrichHarness(t *testing.T, chains map[WatchlistMetadataKind][]WatchlistMetadataSource, poster *http.Client) *enrichTestHarness {
	t.Helper()
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	harness := &enrichTestHarness{
		service:  NewWatchlistService(dataDir),
		dataDir:  dataDir,
		registry: NewBackgroundTaskRegistry(),
	}
	if poster == nil {
		poster = &http.Client{}
	}
	harness.service.enrich.sources = func() (watchlistEnrichmentSources, error) {
		return watchlistEnrichmentSources{registry: newWatchlistMetadataRegistry(chains), poster: poster}, nil
	}
	harness.service.SetEnrichmentEventEmitter(func(progress WatchlistEnrichProgress) {
		harness.mu.Lock()
		defer harness.mu.Unlock()
		harness.progress = append(harness.progress, progress)
	})
	harness.registry.SetOnChange(func(running []string) {
		harness.mu.Lock()
		defer harness.mu.Unlock()
		harness.tasks = append(harness.tasks, running)
	})
	harness.service.SetBackgroundTaskRegistry(harness.registry)
	return harness
}

func (h *enrichTestHarness) statuses() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.progress))
	for _, event := range h.progress {
		out = append(out, event.Status)
	}
	return out
}

func (h *enrichTestHarness) events() []WatchlistEnrichProgress {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]WatchlistEnrichProgress(nil), h.progress...)
}

func (h *enrichTestHarness) taskSnapshots() [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]string(nil), h.tasks...)
}

func mustCreateWatchlistEntry(t *testing.T, title, kind string) models.WatchlistEntry {
	t.Helper()
	entry := models.WatchlistEntry{Title: title, Kind: kind, EnrichmentStatus: models.WatchlistEnrichmentPending}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("创建想看条目失败: %v", err)
	}
	return entry
}

func reloadWatchlistEntry(t *testing.T, id uint) models.WatchlistEntry {
	t.Helper()
	var entry models.WatchlistEntry
	if err := database.DB.First(&entry, id).Error; err != nil {
		t.Fatalf("读取想看条目失败: %v", err)
	}
	return entry
}

// posterServer 起一个回真 PNG 的站点，并计数被取了几次。
func posterServer(t *testing.T, shade uint8) (*httptest.Server, *int32) {
	t.Helper()
	body := posterPNG(t, shade)
	var mu sync.Mutex
	count := int32(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, &count
}

// ---------- 转换表：pending → running → succeeded ----------

// 钉转换表第 1 行（认领影响 1 行 → running）与第 4 行（写回成功 → succeeded，
// 写影片字段、海报路径、SourceName、SourceItemID、EnrichedAt）。
// 同时钉「每次状态落定发一次进度事件」与后台任务登记。
func TestWatchlistEnrichClaimsPendingThenWritesBack(t *testing.T) {
	poster, posterHits := posterServer(t, 0x20)
	detail := WatchlistMetadataDetail{
		WatchlistMetadataCandidate: WatchlistMetadataCandidate{
			SourceItemID: "438631",
			Title:        "沙丘",
			Year:         2021,
			Overview:     "厄拉科斯。",
			Rating:       7.8,
			PosterURL:    poster.URL + "/dune.png",
		},
		Genres:    []string{"科幻", "冒险"},
		Directors: []string{"Denis Villeneuve"},
		Cast:      []string{"Timothée Chalamet", "Zendaya"},
	}
	harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {staticWatchlistSource("tmdb", detail)},
	}, poster.Client())
	entry := mustCreateWatchlistEntry(t, "沙丘", models.WatchlistKindMovie)

	harness.service.runEnrichmentOnce(context.Background())

	saved := reloadWatchlistEntry(t, entry.ID)
	if saved.EnrichmentStatus != models.WatchlistEnrichmentSucceeded {
		t.Fatalf("补全后状态应为 succeeded，实际 %q（错误码 %q）", saved.EnrichmentStatus, saved.EnrichmentError)
	}
	if saved.EnrichmentError != "" || saved.EnrichmentClaim != "" {
		t.Errorf("落定后应清空错误码与认领标识，实际 error=%q claim=%q", saved.EnrichmentError, saved.EnrichmentClaim)
	}
	if saved.SourceName != "tmdb" || saved.SourceItemID != "438631" {
		t.Errorf("来源追溯字段错误: %+v", saved)
	}
	if saved.EnrichedAt == nil || saved.EnrichedAt.IsZero() {
		t.Errorf("成功补全必须记 EnrichedAt，实际 %v", saved.EnrichedAt)
	}
	if saved.Year != 2021 || saved.Overview != "厄拉科斯。" || saved.Rating != 7.8 {
		t.Errorf("影片字段未写回: %+v", saved)
	}
	if saved.Title != "沙丘" {
		t.Errorf("补全不得覆盖用户填的片名，实际 %q", saved.Title)
	}
	var genres []string
	if err := json.Unmarshal([]byte(saved.Genres), &genres); err != nil || len(genres) != 2 || genres[0] != "科幻" {
		t.Errorf("类型标签编码错误 %q: %v", saved.Genres, err)
	}
	var credits WatchlistCredits
	if err := json.Unmarshal([]byte(saved.Credits), &credits); err != nil ||
		len(credits.Directors) != 1 || len(credits.Cast) != 2 {
		t.Errorf("主创编码错误 %q: %v", saved.Credits, err)
	}
	if saved.PosterPath == "" {
		t.Fatalf("海报路径未写回")
	}
	if *posterHits != 1 {
		t.Errorf("海报应只下载一次，实际 %d 次", *posterHits)
	}
	if files := managedWatchlistFiles(t, harness.dataDir); len(files) != 1 {
		t.Errorf("托管目录应恰好一张海报，实际 %v", files)
	}
	if _, err := harness.service.ResolveWatchlistPoster(entry.ID); err != nil {
		t.Errorf("写回的海报路径必须能解析回文件: %v", err)
	}

	// 进度事件：认领进 running 一次，落定 succeeded 一次，不多不少。
	if got := harness.statuses(); len(got) != 2 ||
		got[0] != models.WatchlistEnrichmentRunning || got[1] != models.WatchlistEnrichmentSucceeded {
		t.Fatalf("进度事件序列错误: %v", got)
	}
	last := harness.events()[1]
	if last.EntryID != entry.ID || last.Title != "沙丘" || last.Kind != models.WatchlistKindMovie || last.SourceName != "tmdb" {
		t.Errorf("进度事件载荷错误: %+v", last)
	}

	// 后台任务登记：这一轮里 watchlist_enrich 出现过，跑完又消失。
	snapshots := harness.taskSnapshots()
	if len(snapshots) != 2 {
		t.Fatalf("应有一对 Begin/End 快照，实际 %v", snapshots)
	}
	if len(snapshots[0]) != 1 || snapshots[0][0] != string(BackgroundTaskWatchlistEnrich) {
		t.Errorf("Begin 快照应含 watchlist_enrich，实际 %v", snapshots[0])
	}
	if len(snapshots[1]) != 0 {
		t.Errorf("End 之后不应再有运行中的任务，实际 %v", snapshots[1])
	}
}

// 没有 pending 时整轮是空操作：不认领、不出网、也不在登记表里闪一下。
func TestWatchlistEnrichSkipsRunWithoutPendingEntries(t *testing.T) {
	harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {&fakeWatchlistSource{
			name: "tmdb",
			searchFn: func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
				t.Errorf("没有 pending 条目时不应询问任何源")
				return nil, nil
			},
		}},
	}, nil)
	entry := mustCreateWatchlistEntry(t, "已手工维护", models.WatchlistKindMovie)
	if err := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", entry.ID).
		Update("enrichment_status", models.WatchlistEnrichmentManual).Error; err != nil {
		t.Fatal(err)
	}

	harness.service.runEnrichmentOnce(context.Background())

	if saved := reloadWatchlistEntry(t, entry.ID); saved.EnrichmentStatus != models.WatchlistEnrichmentManual {
		t.Errorf("manual 条目不应被认领，实际 %q", saved.EnrichmentStatus)
	}
	if snapshots := harness.taskSnapshots(); len(snapshots) != 0 {
		t.Errorf("无待办时不应登记后台任务，实际 %v", snapshots)
	}
	if events := harness.events(); len(events) != 0 {
		t.Errorf("无待办时不应发进度事件，实际 %v", events)
	}
}

// ---------- 转换表第 2 行：认领影响 0 行 → 跳过，不报错 ----------

func TestWatchlistEnrichClaimSkipsEntryHeldByAnotherWorker(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := NewWatchlistService(t.TempDir())
	entry := mustCreateWatchlistEntry(t, "被抢走的条目", models.WatchlistKindMovie)

	first, claimed, err := service.claimEnrichment(entry.ID)
	if err != nil || !claimed {
		t.Fatalf("首次认领应成功: claimed=%t err=%v", claimed, err)
	}
	if len(first.EnrichmentClaim) != 32 {
		t.Fatalf("认领标识必须是 32 个字符（列是 size:32），实际 %d 个：%q", len(first.EnrichmentClaim), first.EnrichmentClaim)
	}
	if first.EnrichmentStatus != models.WatchlistEnrichmentRunning {
		t.Fatalf("认领后状态应为 running，实际 %q", first.EnrichmentStatus)
	}

	// 第二个 worker 对同一条再认领：影响 0 行，既不算成功也不是错误。
	second, claimed, err := service.claimEnrichment(entry.ID)
	if err != nil {
		t.Fatalf("没抢到不该报错: %v", err)
	}
	if claimed {
		t.Fatalf("同一条目不得被两个 worker 同时持有，第二次却拿到了 %+v", second)
	}
	if saved := reloadWatchlistEntry(t, entry.ID); saved.EnrichmentClaim != first.EnrichmentClaim {
		t.Errorf("没抢到的一方不得改写认领标识，实际 %q", saved.EnrichmentClaim)
	}
}

// 认领标识必须每次都不同，否则 ABA 守卫形同虚设。
func TestWatchlistEnrichClaimTokensAreDistinctAndBounded(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		claim, err := newWatchlistEnrichmentClaim()
		if err != nil {
			t.Fatalf("生成认领标识失败: %v", err)
		}
		if len(claim) != 32 {
			t.Fatalf("认领标识必须 32 字符（EnrichmentClaim 是 size:32；uuid.NewString 的 36 字符会在 SQLite 上静默截断、在 PG 上报错），实际 %d：%q", len(claim), claim)
		}
		if _, duplicate := seen[claim]; duplicate {
			t.Fatalf("认领标识重复: %q", claim)
		}
		seen[claim] = struct{}{}
	}
}

// ---------- 转换表第 6 行：写回时守卫不成立 → 丢弃，不改状态，删掉临时图片 ----------

// §3.2 的时序图：认领之后、写回之前用户改了标题。写回必须影响 0 行，条目字段
// 一个不变，刚落盘的海报要删掉，不留孤儿文件。
func TestWatchlistEnrichDiscardsResultWhenUserEditsDuringRun(t *testing.T) {
	poster, _ := posterServer(t, 0x40)
	released := make(chan struct{})
	edited := make(chan struct{})
	detail := WatchlistMetadataDetail{
		WatchlistMetadataCandidate: WatchlistMetadataCandidate{
			SourceItemID: "438631", Title: "沙丘", Year: 2021,
			Overview: "不该写进去的简介", Rating: 7.8, PosterURL: poster.URL + "/dune.png",
		},
	}
	source := staticWatchlistSource("tmdb", detail)
	inner := source.searchFn
	source.searchFn = func(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
		// 认领已经完成，此刻放用户去改名，改完再让源返回。
		close(edited)
		<-released
		return inner(ctx, kind, query)
	}
	harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {source},
	}, poster.Client())
	entry := mustCreateWatchlistEntry(t, "沙丘", models.WatchlistKindMovie)

	done := make(chan struct{})
	go func() {
		defer close(done)
		harness.service.runEnrichmentOnce(context.Background())
	}()

	<-edited
	if err := harness.service.Update(entry.ID, "沙丘 2021"); err != nil {
		t.Fatalf("补全期间的编辑必须照常成功（不得被补全阻塞）: %v", err)
	}
	close(released)
	<-done

	saved := reloadWatchlistEntry(t, entry.ID)
	if saved.Title != "沙丘 2021" {
		t.Errorf("用户输入优先，片名应是用户改的，实际 %q", saved.Title)
	}
	if saved.EnrichmentStatus != models.WatchlistEnrichmentManual {
		t.Errorf("编辑后状态应停在 manual，丢弃不得改变条目状态，实际 %q", saved.EnrichmentStatus)
	}
	if saved.Year != 0 || saved.Overview != "" || saved.SourceItemID != "" || saved.EnrichedAt != nil {
		t.Errorf("被丢弃的结果不得写进任何字段: %+v", saved)
	}
	if saved.PosterPath != "" {
		t.Errorf("被丢弃的结果不得写海报路径，实际 %q", saved.PosterPath)
	}
	if files := managedWatchlistFiles(t, harness.dataDir); len(files) != 0 {
		t.Errorf("丢弃时必须删掉已下载的临时图片，托管目录却留下 %v", files)
	}
	// 丢弃这条路径按定义没有改变状态，因此只有认领时的 running 与编辑转 manual
	// 两个事件，没有第三个"落定"。
	for _, status := range harness.statuses() {
		if status == models.WatchlistEnrichmentSucceeded || status == models.WatchlistEnrichmentFailed {
			t.Errorf("丢弃不得发出落定事件，实际序列 %v", harness.statuses())
			break
		}
	}
}

// 用户在补全期间删除条目：写回同样影响 0 行，海报不能留在托管目录里（TC-04）。
func TestWatchlistEnrichDiscardsResultWhenUserDeletesDuringRun(t *testing.T) {
	poster, _ := posterServer(t, 0x60)
	released := make(chan struct{})
	searched := make(chan struct{})
	detail := WatchlistMetadataDetail{
		WatchlistMetadataCandidate: WatchlistMetadataCandidate{
			SourceItemID: "1", Title: "沙丘", Year: 2021, PosterURL: poster.URL + "/dune.png",
		},
	}
	source := staticWatchlistSource("tmdb", detail)
	inner := source.searchFn
	source.searchFn = func(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
		close(searched)
		<-released
		return inner(ctx, kind, query)
	}
	harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {source},
	}, poster.Client())
	entry := mustCreateWatchlistEntry(t, "沙丘", models.WatchlistKindMovie)

	done := make(chan struct{})
	go func() {
		defer close(done)
		harness.service.runEnrichmentOnce(context.Background())
	}()

	<-searched
	if err := harness.service.Delete(entry.ID); err != nil {
		t.Fatalf("补全期间的删除必须照常成功: %v", err)
	}
	close(released)
	<-done

	var count int64
	database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", entry.ID).Count(&count)
	if count != 0 {
		t.Errorf("被删除的条目不得因写回而复活")
	}
	if files := managedWatchlistFiles(t, harness.dataDir); len(files) != 0 {
		t.Errorf("条目已删除时下载的海报必须清掉，托管目录却留下 %v", files)
	}
}

// ---------- ABA：重试后的慢响应不得覆盖新一轮 ----------

// 状态会回到 pending（用户重试），只比对状态值不足以区分「同一次认领」与
// 「新一轮认领」。这个用例就是认领标识存在的理由。
func TestWatchlistEnrichStaleClaimCannotOverwriteRetriedRound(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := NewWatchlistService(t.TempDir())
	entry := mustCreateWatchlistEntry(t, "沙丘", models.WatchlistKindMovie)

	// 第一轮认领，拿到 k1。
	first, claimed, err := service.claimEnrichment(entry.ID)
	if err != nil || !claimed {
		t.Fatalf("首轮认领应成功: %v", err)
	}
	// 第一轮还没回来，用户先让它失败落定，再手动重试 → 回到 pending。
	service.settleEnrichmentFailure(first, WatchlistMetadataFailureNetworkUnreachable, errors.New("超时"))
	if err := service.RetryEnrichment(entry.ID); err != nil {
		t.Fatalf("重试应成功: %v", err)
	}
	if saved := reloadWatchlistEntry(t, entry.ID); saved.EnrichmentStatus != models.WatchlistEnrichmentPending || saved.EnrichmentError != "" {
		t.Fatalf("重试后应回到 pending 且清空错误码，实际 %+v", saved)
	}

	// 第二轮认领，拿到 k2。
	second, claimed, err := service.claimEnrichment(entry.ID)
	if err != nil || !claimed {
		t.Fatalf("二轮认领应成功: %v", err)
	}
	if second.EnrichmentClaim == first.EnrichmentClaim {
		t.Fatalf("两轮认领标识不得相同: %q", second.EnrichmentClaim)
	}

	// 现在第一轮的慢响应到了。状态恰好又是 running——只比对状态就会覆盖新一轮。
	written, err := service.writeBackEnrichment(entry.ID, first.EnrichmentClaim, map[string]any{
		"enrichment_status": models.WatchlistEnrichmentSucceeded,
		"overview":          "上一轮的旧结果",
	})
	if err != nil {
		t.Fatalf("写回不应报错: %v", err)
	}
	if written {
		t.Fatalf("上一轮的认领标识不得写回新一轮")
	}
	saved := reloadWatchlistEntry(t, entry.ID)
	if saved.EnrichmentStatus != models.WatchlistEnrichmentRunning || saved.Overview != "" {
		t.Fatalf("新一轮的状态被旧响应污染了: %+v", saved)
	}

	// 本轮自己的标识照常写得进去。
	written, err = service.writeBackEnrichment(entry.ID, second.EnrichmentClaim, map[string]any{
		"enrichment_status": models.WatchlistEnrichmentSucceeded,
		"overview":          "本轮结果",
	})
	if err != nil || !written {
		t.Fatalf("本轮标识应写得进去: written=%t err=%v", written, err)
	}
	if saved := reloadWatchlistEntry(t, entry.ID); saved.Overview != "本轮结果" {
		t.Fatalf("本轮结果未写回: %+v", saved)
	}
}

// ---------- 转换表第 5 行：六类失败分类，互不合并 ----------

// 每一类都走完整的 worker 路径（认领 → 出网 → 写回 failed + 分类码），
// 用真实的 TMDB 适配器，保证分类来自 P-003 那套而不是测试里另起的一套。
func TestWatchlistEnrichClassifiesFailuresIntoSixDistinctCodes(t *testing.T) {
	// credential_missing 必须在**发请求之前**认定，用会判红的 RoundTripper 证明。
	t.Run("credential_missing", func(t *testing.T) {
		client := &http.Client{Transport: &failingRoundTripper{t: t}}
		source := NewTMDBWatchlistMetadataSource("", client)
		assertWatchlistEnrichFailure(t, source, nil, WatchlistMetadataFailureCredentialMissing)
	})

	t.Run("credential_invalid", func(t *testing.T) {
		server := newTMDBStubServer(t, http.StatusUnauthorized, `{"status_message":"Invalid API key"}`)
		assertWatchlistEnrichFailure(t, newTMDBSourceAgainst(server), server.Client(), WatchlistMetadataFailureCredentialInvalid)
	})

	t.Run("not_found", func(t *testing.T) {
		// TMDB 对「没有收录」的形态是 200 加空 results。
		server := newTMDBStubServer(t, http.StatusOK, `{"results":[]}`)
		assertWatchlistEnrichFailure(t, newTMDBSourceAgainst(server), server.Client(), WatchlistMetadataFailureNotFound)
	})

	t.Run("source_error", func(t *testing.T) {
		server := newTMDBStubServer(t, http.StatusInternalServerError, `{"status_message":"boom"}`)
		assertWatchlistEnrichFailure(t, newTMDBSourceAgainst(server), server.Client(), WatchlistMetadataFailureSourceError)
	})

	t.Run("network_unreachable", func(t *testing.T) {
		// 起一个服务器只为拿到一个确定没人监听的地址，然后立刻关掉。
		server := newTMDBStubServer(t, http.StatusOK, `{"results":[]}`)
		address := server.URL
		server.Close()
		source := NewTMDBWatchlistMetadataSource("k", &http.Client{Timeout: 2 * time.Second})
		source.baseURL = address
		assertWatchlistEnrichFailure(t, source, nil, WatchlistMetadataFailureNetworkUnreachable)
	})

	t.Run("proxy_unreachable", func(t *testing.T) {
		// 代理地址语法合法但没人监听：net/http 把拨代理的失败包成
		// Op == "proxyconnect"，这是区分「代理不通」与「目标站不通」的唯一依据。
		client, err := NewWatchlistMetadataHTTPClient(
			WatchlistMetadataConfig{ProxyURL: "http://" + closedLocalAddress(t)}, 2*time.Second)
		if err != nil {
			t.Fatalf("构造带代理的客户端失败: %v", err)
		}
		source := NewTMDBWatchlistMetadataSource("k", client)
		source.baseURL = "http://example.invalid"
		assertWatchlistEnrichFailure(t, source, nil, WatchlistMetadataFailureProxyUnreachable)
	})
}

// 代理地址压根填错时连客户端都建不出来。这一轮仍要认领并落 failed，否则条目会
// 无声地卡在 pending，用户完全看不出哪里不对。
func TestWatchlistEnrichMarksProxyMisconfigurationOnEveryPendingEntry(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := NewWatchlistService(t.TempDir())
	service.enrich.sources = func() (watchlistEnrichmentSources, error) {
		return watchlistEnrichmentSources{}, fmt.Errorf("%w：缺少协议前缀", ErrWatchlistMetadataProxyInvalid)
	}
	first := mustCreateWatchlistEntry(t, "条目一", models.WatchlistKindMovie)
	second := mustCreateWatchlistEntry(t, "条目二", models.WatchlistKindMovie)

	service.runEnrichmentOnce(context.Background())

	for _, id := range []uint{first.ID, second.ID} {
		saved := reloadWatchlistEntry(t, id)
		if saved.EnrichmentStatus != models.WatchlistEnrichmentFailed ||
			saved.EnrichmentError != string(WatchlistMetadataFailureProxyUnreachable) {
			t.Errorf("条目 %d 应落 failed/proxy_unreachable，实际 %q/%q", id, saved.EnrichmentStatus, saved.EnrichmentError)
		}
	}
}

// 尚无适配器的类型（本切片里的 av）归 source_error，不新开第七个分类码。
func TestWatchlistEnrichUnsupportedKindFailsWithSourceError(t *testing.T) {
	harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {staticWatchlistSource("tmdb", WatchlistMetadataDetail{})},
	}, nil)
	entry := mustCreateWatchlistEntry(t, "SSIS-001", models.WatchlistKindAV)

	harness.service.runEnrichmentOnce(context.Background())

	saved := reloadWatchlistEntry(t, entry.ID)
	if saved.EnrichmentStatus != models.WatchlistEnrichmentFailed ||
		saved.EnrichmentError != string(WatchlistMetadataFailureSourceError) {
		t.Fatalf("尚无适配器的类型应落 failed/source_error，实际 %q/%q", saved.EnrichmentStatus, saved.EnrichmentError)
	}
	if len(saved.EnrichmentError) > 32 {
		t.Errorf("分类码必须放得进 size:32 的列，实际 %d 字节", len(saved.EnrichmentError))
	}
}

// 失败码全都放得进 EnrichmentError 的 size:32。
func TestWatchlistEnrichFailureCodesFitTheColumn(t *testing.T) {
	for _, failure := range []WatchlistMetadataFailure{
		WatchlistMetadataFailureCredentialMissing,
		WatchlistMetadataFailureCredentialInvalid,
		WatchlistMetadataFailureProxyUnreachable,
		WatchlistMetadataFailureNetworkUnreachable,
		WatchlistMetadataFailureNotFound,
		WatchlistMetadataFailureSourceError,
	} {
		if len(failure) > 32 {
			t.Errorf("分类码 %q 超过 EnrichmentError 的 size:32", failure)
		}
	}
}

// D-WM07：兜底只在前一个源明确 not_found 时才走；其余分类一律就地失败，
// 不多打一次抓取请求。
func TestWatchlistEnrichFallsBackOnlyOnNotFound(t *testing.T) {
	cases := []struct {
		name         string
		firstFailure WatchlistMetadataFailure
		wantFallback bool
		wantCode     WatchlistMetadataFailure
	}{
		{"not_found 触发兜底", WatchlistMetadataFailureNotFound, true, ""},
		{"凭证错误不触发兜底", WatchlistMetadataFailureCredentialInvalid, false, WatchlistMetadataFailureCredentialInvalid},
		{"网络错误不触发兜底", WatchlistMetadataFailureNetworkUnreachable, false, WatchlistMetadataFailureNetworkUnreachable},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fallbackCalled := false
			primary := &fakeWatchlistSource{
				name: "primary",
				searchFn: func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
					return nil, newWatchlistMetadataSourceError("primary", testCase.firstFailure, 0, "", nil)
				},
			}
			fallback := staticWatchlistSource("fallback", WatchlistMetadataDetail{
				WatchlistMetadataCandidate: WatchlistMetadataCandidate{SourceItemID: "f-1", Title: "兜底结果"},
			})
			inner := fallback.searchFn
			fallback.searchFn = func(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
				fallbackCalled = true
				return inner(ctx, kind, query)
			}
			harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
				WatchlistMetadataKindMovie: {primary, fallback},
			}, nil)
			entry := mustCreateWatchlistEntry(t, "待补全", models.WatchlistKindMovie)

			harness.service.runEnrichmentOnce(context.Background())

			if fallbackCalled != testCase.wantFallback {
				t.Fatalf("兜底调用与预期不符: called=%t want=%t", fallbackCalled, testCase.wantFallback)
			}
			saved := reloadWatchlistEntry(t, entry.ID)
			if testCase.wantFallback {
				if saved.EnrichmentStatus != models.WatchlistEnrichmentSucceeded || saved.SourceName != "fallback" {
					t.Fatalf("兜底结果应写回: %+v", saved)
				}
				return
			}
			if saved.EnrichmentStatus != models.WatchlistEnrichmentFailed || saved.EnrichmentError != string(testCase.wantCode) {
				t.Fatalf("应就地失败为 %q，实际 %q/%q", testCase.wantCode, saved.EnrichmentStatus, saved.EnrichmentError)
			}
		})
	}
}

// 链上最后一个源也 not_found 时，条目落 failed/not_found 而不是无声成功。
func TestWatchlistEnrichExhaustedChainSettlesNotFound(t *testing.T) {
	notFound := &fakeWatchlistSource{
		name: "tmdb",
		searchFn: func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
			return nil, newWatchlistMetadataSourceError("tmdb", WatchlistMetadataFailureNotFound, 200, "搜索无结果", nil)
		},
	}
	harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {notFound},
	}, nil)
	entry := mustCreateWatchlistEntry(t, "查无此片", models.WatchlistKindMovie)

	harness.service.runEnrichmentOnce(context.Background())

	saved := reloadWatchlistEntry(t, entry.ID)
	if saved.EnrichmentStatus != models.WatchlistEnrichmentFailed ||
		saved.EnrichmentError != string(WatchlistMetadataFailureNotFound) {
		t.Fatalf("应落 failed/not_found，实际 %q/%q", saved.EnrichmentStatus, saved.EnrichmentError)
	}
	if got := harness.statuses(); len(got) != 2 || got[1] != models.WatchlistEnrichmentFailed {
		t.Errorf("失败同样要发一次落定事件: %v", got)
	}
}

// recordingWatchlistChain 造一条两跳的链：首跳按给定分类失败，次跳出详情。
// asked 按询问顺序记下被问过的源名，用来证明「问了谁」而不只是「结果是什么」。
func recordingWatchlistChain(firstFailure WatchlistMetadataFailure, asked *[]string) []WatchlistMetadataSource {
	primary := &fakeWatchlistSource{
		name: "primary",
		searchFn: func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
			*asked = append(*asked, "primary")
			return nil, newWatchlistMetadataSourceError("primary", firstFailure, 0, "", nil)
		},
	}
	fallback := staticWatchlistSource("fallback", WatchlistMetadataDetail{
		WatchlistMetadataCandidate: WatchlistMetadataCandidate{SourceItemID: "f-1", Title: "兜底结果"},
	})
	inner := fallback.searchFn
	fallback.searchFn = func(ctx context.Context, kind WatchlistMetadataKind, query string) ([]WatchlistMetadataCandidate, error) {
		*asked = append(*asked, "fallback")
		return inner(ctx, kind, query)
	}
	return []WatchlistMetadataSource{primary, fallback}
}

// D-WM07 的兜底规则只该有一份：lookupWatchlistDetail 走的就是
// walkWatchlistMetadataChain。用同一批源各跑一遍，比对「问了谁」与失败分类——
// 哪天有人在其中一处改了退出条件，这里立刻判红。
func TestWatchlistEnrichLookupSharesChainWalkerFallbackRule(t *testing.T) {
	const kind = WatchlistMetadataKindMovie
	for _, testCase := range []struct {
		name         string
		firstFailure WatchlistMetadataFailure
		wantAsked    []string
	}{
		{"首跳 not_found 才问第二跳", WatchlistMetadataFailureNotFound, []string{"primary", "fallback"}},
		{"首跳 source_error 就地停下", WatchlistMetadataFailureSourceError, []string{"primary"}},
		{"首跳 credential_invalid 就地停下", WatchlistMetadataFailureCredentialInvalid, []string{"primary"}},
		{"首跳 network_unreachable 就地停下", WatchlistMetadataFailureNetworkUnreachable, []string{"primary"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// 两条各自记账的链，免得一方的调用序列污染另一方。
			var lookupAsked, walkAsked []string
			lookupChain := recordingWatchlistChain(testCase.firstFailure, &lookupAsked)
			walkChain := recordingWatchlistChain(testCase.firstFailure, &walkAsked)
			registry := newWatchlistMetadataRegistry(map[WatchlistMetadataKind][]WatchlistMetadataSource{kind: lookupChain})

			lookupDetail, lookupErr := lookupWatchlistDetail(context.Background(), registry, kind, "待补全")
			walkDetail, walkErr := walkWatchlistMetadataChain(walkChain, func(source WatchlistMetadataSource) (*WatchlistMetadataDetail, error) {
				return watchlistSourceDetail(context.Background(), source, kind, "待补全")
			})

			if got := strings.Join(lookupAsked, ","); got != strings.Join(testCase.wantAsked, ",") {
				t.Fatalf("lookupWatchlistDetail 问过 %v，期望 %v", lookupAsked, testCase.wantAsked)
			}
			if got, want := strings.Join(lookupAsked, ","), strings.Join(walkAsked, ","); got != want {
				t.Fatalf("两处的询问序列分叉了: lookup=%v walk=%v", lookupAsked, walkAsked)
			}
			if WatchlistMetadataFailureOf(lookupErr) != WatchlistMetadataFailureOf(walkErr) {
				t.Fatalf("两处的失败分类分叉了: lookup=%v walk=%v", lookupErr, walkErr)
			}
			if (lookupDetail == nil) != (walkDetail == nil) {
				t.Fatalf("两处的详情有无分叉了: lookup=%v walk=%v", lookupDetail, walkDetail)
			}
			if len(testCase.wantAsked) == 1 {
				if WatchlistMetadataFailureOf(lookupErr) != testCase.firstFailure {
					t.Fatalf("应原样交出首跳的错误 %q，实际 %v", testCase.firstFailure, lookupErr)
				}
				return
			}
			// 兜底成功这一跳还要证同源：写回条目的 SourceName 与 SourceItemID
			// 必须出自同一个源，否则拿这个 ID 回那个源重查会查空。
			if lookupErr != nil {
				t.Fatalf("兜底应当成功，实际 %v", lookupErr)
			}
			if lookupDetail.SourceName != "fallback" || lookupDetail.SourceItemID != "f-1" {
				t.Fatalf("详情应出自兜底源本身: name=%q id=%q", lookupDetail.SourceName, lookupDetail.SourceItemID)
			}
		})
	}
}

// 两跳都 not_found 时交出的是**最后一跳**的错误，Source 还如实记着最后问过谁。
func TestWatchlistEnrichLookupReportsLastHopWhenChainExhausted(t *testing.T) {
	const kind = WatchlistMetadataKindMovie
	chain := []WatchlistMetadataSource{
		&fakeWatchlistSource{
			name: "primary",
			searchFn: func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
				return nil, newWatchlistMetadataSourceError("primary", WatchlistMetadataFailureNotFound, 0, "", nil)
			},
		},
		&fakeWatchlistSource{
			name: "fallback",
			searchFn: func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
				return nil, newWatchlistMetadataSourceError("fallback", WatchlistMetadataFailureNotFound, 0, "", nil)
			},
		},
	}
	registry := newWatchlistMetadataRegistry(map[WatchlistMetadataKind][]WatchlistMetadataSource{kind: chain})

	detail, err := lookupWatchlistDetail(context.Background(), registry, kind, "查无此片")

	if detail != nil {
		t.Fatalf("链走完应当无详情，实际 %+v", detail)
	}
	var sourceErr *WatchlistMetadataSourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("应交出源侧错误，实际 %v", err)
	}
	if sourceErr.Source != "fallback" || sourceErr.Failure != WatchlistMetadataFailureNotFound {
		t.Fatalf("应原样交出最后一跳的错误，实际 source=%q failure=%q", sourceErr.Source, sourceErr.Failure)
	}
}

// 失败之后不自动重试：再跑一轮，条目仍停在 failed，源一次都不会被再问。
func TestWatchlistEnrichDoesNotAutoRetryAfterFailure(t *testing.T) {
	searches := 0
	source := &fakeWatchlistSource{
		name: "tmdb",
		searchFn: func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
			searches++
			return nil, newWatchlistMetadataSourceError("tmdb", WatchlistMetadataFailureSourceError, 500, "", nil)
		},
	}
	harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {source},
	}, nil)
	entry := mustCreateWatchlistEntry(t, "会失败的条目", models.WatchlistKindMovie)

	harness.service.runEnrichmentOnce(context.Background())
	harness.service.runEnrichmentOnce(context.Background())
	harness.service.runEnrichmentOnce(context.Background())

	if searches != 1 {
		t.Fatalf("失败后不得自动重试，源却被问了 %d 次", searches)
	}
	if saved := reloadWatchlistEntry(t, entry.ID); saved.EnrichmentStatus != models.WatchlistEnrichmentFailed {
		t.Fatalf("失败的条目应停在 failed 等用户手动重试，实际 %q", saved.EnrichmentStatus)
	}

	// 用户点重试之后才会再问一次。
	if err := harness.service.RetryEnrichment(entry.ID); err != nil {
		t.Fatalf("重试应成功: %v", err)
	}
	harness.service.runEnrichmentOnce(context.Background())
	if searches != 2 {
		t.Fatalf("手动重试后应再问一次源，实际共 %d 次", searches)
	}
}

// ---------- 转换表：用户编辑 / 用户手动重试 ----------

// 「pending / running / succeeded / failed / manual + 用户编辑 → manual」。
func TestWatchlistEnrichUserEditAlwaysMovesEntryToManual(t *testing.T) {
	harness := newEnrichHarness(t, nil, nil)
	for _, from := range []string{
		models.WatchlistEnrichmentPending,
		models.WatchlistEnrichmentRunning,
		models.WatchlistEnrichmentSucceeded,
		models.WatchlistEnrichmentFailed,
		models.WatchlistEnrichmentManual,
	} {
		entry := mustCreateWatchlistEntry(t, "编辑-"+from, models.WatchlistKindMovie)
		if err := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", entry.ID).
			Updates(map[string]any{"enrichment_status": from, "enrichment_error": "source_error"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := harness.service.Update(entry.ID, "改过名的-"+from); err != nil {
			t.Fatalf("从 %s 编辑应成功: %v", from, err)
		}
		saved := reloadWatchlistEntry(t, entry.ID)
		if saved.EnrichmentStatus != models.WatchlistEnrichmentManual {
			t.Errorf("从 %s 编辑后应为 manual，实际 %q", from, saved.EnrichmentStatus)
		}
		// 转换表里这一行的效果列是空的：编辑不清失败分类码，只有手动重试才清。
		if saved.EnrichmentError != "source_error" {
			t.Errorf("编辑不应改动失败分类码，实际 %q", saved.EnrichmentError)
		}
	}
}

// 「succeeded / failed / manual + 用户手动重试 → pending，清空 EnrichmentError」，
// 以及未列出的转换（running / pending 重试）一律不发生。
func TestWatchlistEnrichRetryOnlyAppliesToSettledStates(t *testing.T) {
	harness := newEnrichHarness(t, nil, nil)
	for _, from := range []string{
		models.WatchlistEnrichmentSucceeded,
		models.WatchlistEnrichmentFailed,
		models.WatchlistEnrichmentManual,
	} {
		entry := mustCreateWatchlistEntry(t, "重试-"+from, models.WatchlistKindMovie)
		if err := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", entry.ID).
			Updates(map[string]any{"enrichment_status": from, "enrichment_error": "network_unreachable"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := harness.service.RetryEnrichment(entry.ID); err != nil {
			t.Fatalf("从 %s 重试应成功: %v", from, err)
		}
		saved := reloadWatchlistEntry(t, entry.ID)
		if saved.EnrichmentStatus != models.WatchlistEnrichmentPending || saved.EnrichmentError != "" {
			t.Errorf("从 %s 重试后应为 pending 且清空错误码，实际 %q/%q", from, saved.EnrichmentStatus, saved.EnrichmentError)
		}
	}

	for _, from := range []string{models.WatchlistEnrichmentPending, models.WatchlistEnrichmentRunning} {
		entry := mustCreateWatchlistEntry(t, "不可重试-"+from, models.WatchlistKindMovie)
		if err := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", entry.ID).
			Update("enrichment_status", from).Error; err != nil {
			t.Fatal(err)
		}
		if err := harness.service.RetryEnrichment(entry.ID); !errors.Is(err, ErrWatchlistEnrichmentNotRetryable) {
			t.Errorf("从 %s 重试应明确拒绝，实际 %v", from, err)
		}
		if saved := reloadWatchlistEntry(t, entry.ID); saved.EnrichmentStatus != from {
			t.Errorf("被拒绝的重试不得改状态，实际 %q", saved.EnrichmentStatus)
		}
	}

	if err := harness.service.RetryEnrichment(99999); !errors.Is(err, ErrWatchlistEntryNotFound) {
		t.Errorf("对不存在的条目重试应报不存在，实际 %v", err)
	}
}

// ---------- 转换表：应用启动恢复 ----------

// 进程重启后残留的 running 刷回 pending，认领标识一并作废，并各发一次进度事件。
func TestWatchlistEnrichRecoversInterruptedRunningEntries(t *testing.T) {
	harness := newEnrichHarness(t, nil, nil)
	interrupted := mustCreateWatchlistEntry(t, "中断的条目", models.WatchlistKindMovie)
	if err := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", interrupted.ID).
		Updates(map[string]any{
			"enrichment_status": models.WatchlistEnrichmentRunning,
			"enrichment_claim":  "0123456789abcdef0123456789abcdef",
		}).Error; err != nil {
		t.Fatal(err)
	}
	// 其余状态不受恢复影响。
	untouched := map[string]uint{}
	for _, status := range []string{
		models.WatchlistEnrichmentPending,
		models.WatchlistEnrichmentSucceeded,
		models.WatchlistEnrichmentFailed,
		models.WatchlistEnrichmentManual,
	} {
		entry := mustCreateWatchlistEntry(t, "保持-"+status, models.WatchlistKindMovie)
		if err := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", entry.ID).
			Update("enrichment_status", status).Error; err != nil {
			t.Fatal(err)
		}
		untouched[status] = entry.ID
	}

	if err := harness.service.recoverInterruptedEnrichment(); err != nil {
		t.Fatalf("中断恢复失败: %v", err)
	}

	saved := reloadWatchlistEntry(t, interrupted.ID)
	if saved.EnrichmentStatus != models.WatchlistEnrichmentPending {
		t.Fatalf("残留的 running 应刷回 pending，实际 %q", saved.EnrichmentStatus)
	}
	if saved.EnrichmentClaim != "" {
		t.Errorf("恢复必须作废认领标识，实际 %q", saved.EnrichmentClaim)
	}
	for status, id := range untouched {
		if got := reloadWatchlistEntry(t, id); got.EnrichmentStatus != status {
			t.Errorf("恢复不应动 %s 的条目，实际 %q", status, got.EnrichmentStatus)
		}
	}
	events := harness.events()
	if len(events) != 1 || events[0].EntryID != interrupted.ID || events[0].Status != models.WatchlistEnrichmentPending {
		t.Errorf("恢复应对每条残留发一次进度事件，实际 %+v", events)
	}

	// 没有残留时是空操作。
	harness.mu.Lock()
	harness.progress = nil
	harness.mu.Unlock()
	if err := harness.service.recoverInterruptedEnrichment(); err != nil {
		t.Fatalf("空恢复不应报错: %v", err)
	}
	if events := harness.events(); len(events) != 0 {
		t.Errorf("没有残留时不应发事件，实际 %+v", events)
	}
}

// StartEnrichment 把恢复接在 worker 前面：残留的 running 会被恢复成 pending，
// 随后由起来的 worker 重新认领并补全。
func TestWatchlistEnrichStartRecoversThenReclaims(t *testing.T) {
	harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {staticWatchlistSource("tmdb", WatchlistMetadataDetail{
			WatchlistMetadataCandidate: WatchlistMetadataCandidate{SourceItemID: "9", Title: "沙丘", Year: 2021},
		})},
	}, nil)
	entry := mustCreateWatchlistEntry(t, "重启前在跑", models.WatchlistKindMovie)
	if err := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", entry.ID).
		Updates(map[string]any{
			"enrichment_status": models.WatchlistEnrichmentRunning,
			"enrichment_claim":  "ffffffffffffffffffffffffffffffff",
		}).Error; err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	harness.service.StartEnrichment(ctx)
	t.Cleanup(harness.service.StopEnrichmentAndWait)

	deadline := time.Now().Add(5 * time.Second)
	for {
		saved := reloadWatchlistEntry(t, entry.ID)
		if saved.EnrichmentStatus == models.WatchlistEnrichmentSucceeded {
			if saved.EnrichmentClaim != "" {
				t.Errorf("落定后认领标识应清空，实际 %q", saved.EnrichmentClaim)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("重启恢复后未能重新补全，停在 %q", saved.EnrichmentStatus)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------- 补全不阻塞片单的增删改查 ----------

// 一条条目正卡在出网上（源迟迟不返回），其余条目的增删改查必须照常。这是
// 「不用 SELECT ... FOR UPDATE」的直接可观察后果。
func TestWatchlistEnrichDoesNotBlockWatchlistCRUD(t *testing.T) {
	released := make(chan struct{})
	entered := make(chan struct{})
	source := &fakeWatchlistSource{
		name: "tmdb",
		searchFn: func(context.Context, WatchlistMetadataKind, string) ([]WatchlistMetadataCandidate, error) {
			close(entered)
			<-released
			return nil, newWatchlistMetadataSourceError("tmdb", WatchlistMetadataFailureNotFound, 0, "", nil)
		},
	}
	harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {source},
	}, nil)
	blocked := mustCreateWatchlistEntry(t, "卡在出网的条目", models.WatchlistKindMovie)

	done := make(chan struct{})
	go func() {
		defer close(done)
		harness.service.runEnrichmentOnce(context.Background())
	}()
	<-entered

	finished := make(chan error, 1)
	go func() {
		if _, err := harness.service.List("", 0, 50); err != nil {
			finished <- err
			return
		}
		created, err := harness.service.Create("补全期间新增的条目", "")
		if err != nil {
			finished <- err
			return
		}
		if err := harness.service.Update(created.ID, "补全期间改名的条目"); err != nil {
			finished <- err
			return
		}
		if err := harness.service.Delete(created.ID); err != nil {
			finished <- err
			return
		}
		// 连正在补全的那一条也能删：认领没有拿任何数据库锁。
		finished <- harness.service.Delete(blocked.ID)
	}()

	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("补全期间的片单增删改查不应失败: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(released)
		<-done
		t.Fatal("补全阻塞了片单的增删改查")
	}
	close(released)
	<-done
}

// ---------- 后台任务登记 ----------

func TestWatchlistEnrichBackgroundTaskKeyIsRegistered(t *testing.T) {
	if !IsBackgroundTaskKey(string(BackgroundTaskWatchlistEnrich)) {
		t.Fatalf("watchlist_enrich 必须在固定 key 集合里")
	}
	keys := BackgroundTaskKeys()
	if keys[len(keys)-1] != string(BackgroundTaskWatchlistEnrich) {
		t.Errorf("新 key 应追加在面板顺序末尾，实际顺序尾部是 %q", keys[len(keys)-1])
	}
	seen := 0
	for _, key := range keys {
		if key == string(BackgroundTaskWatchlistEnrich) {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("watchlist_enrich 在顺序表里应恰好出现一次，实际 %d 次", seen)
	}
}

// ---------- 海报与回滚边界 ----------

// 海报下载失败只影响图：文字字段照常写回，条目只是没有图。
func TestWatchlistEnrichKeepsTextFieldsWhenPosterDownloadFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {staticWatchlistSource("tmdb", WatchlistMetadataDetail{
			WatchlistMetadataCandidate: WatchlistMetadataCandidate{
				SourceItemID: "1", Title: "沙丘", Year: 2021, Overview: "简介", PosterURL: server.URL + "/missing.png",
			},
		})},
	}, server.Client())
	entry := mustCreateWatchlistEntry(t, "沙丘", models.WatchlistKindMovie)

	harness.service.runEnrichmentOnce(context.Background())

	saved := reloadWatchlistEntry(t, entry.ID)
	if saved.EnrichmentStatus != models.WatchlistEnrichmentSucceeded {
		t.Fatalf("海报失败不该让整条补全失败，实际 %q/%q", saved.EnrichmentStatus, saved.EnrichmentError)
	}
	if saved.Year != 2021 || saved.Overview != "简介" {
		t.Errorf("文字字段应照常写回: %+v", saved)
	}
	if saved.PosterPath != "" {
		t.Errorf("没下下来就不该有海报路径，实际 %q", saved.PosterPath)
	}
	if files := managedWatchlistFiles(t, harness.dataDir); len(files) != 0 {
		t.Errorf("托管目录不应有文件，实际 %v", files)
	}
}

// 换图之后清掉旧图，不留孤儿文件。
func TestWatchlistEnrichReplacesPosterAndCleansTheOldOne(t *testing.T) {
	first, _ := posterServer(t, 0x10)
	second, _ := posterServer(t, 0x90)
	detail := WatchlistMetadataDetail{
		WatchlistMetadataCandidate: WatchlistMetadataCandidate{
			SourceItemID: "1", Title: "沙丘", PosterURL: first.URL + "/a.png",
		},
	}
	source := staticWatchlistSource("tmdb", detail)
	harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {source},
	}, first.Client())
	entry := mustCreateWatchlistEntry(t, "沙丘", models.WatchlistKindMovie)

	harness.service.runEnrichmentOnce(context.Background())
	firstPath := reloadWatchlistEntry(t, entry.ID).PosterPath
	if firstPath == "" {
		t.Fatal("首轮应落一张海报")
	}

	// 换一张图重跑：源换成第二张的地址。
	source.detailFn = func(context.Context, WatchlistMetadataKind, string) (*WatchlistMetadataDetail, error) {
		replaced := detail
		replaced.SourceName = "tmdb"
		replaced.PosterURL = second.URL + "/b.png"
		return &replaced, nil
	}
	if err := harness.service.RetryEnrichment(entry.ID); err != nil {
		t.Fatal(err)
	}
	harness.service.runEnrichmentOnce(context.Background())

	saved := reloadWatchlistEntry(t, entry.ID)
	if saved.PosterPath == "" || saved.PosterPath == firstPath {
		t.Fatalf("换图后海报路径应变化，实际 %q（原 %q）", saved.PosterPath, firstPath)
	}
	if files := managedWatchlistFiles(t, harness.dataDir); len(files) != 1 {
		t.Errorf("换图后应只剩新图一张，实际 %v", files)
	}
}

// 源条目 ID 装不下 size:64 的列时按 source_error 落定，不截断存进去——截断后
// 那条 ID 再也回不到源上，而库里看着像是成功了。
func TestWatchlistEnrichRejectsOversizedSourceItemID(t *testing.T) {
	harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {staticWatchlistSource("tmdb", WatchlistMetadataDetail{
			WatchlistMetadataCandidate: WatchlistMetadataCandidate{
				SourceItemID: strings.Repeat("9", 65), Title: "沙丘",
			},
		})},
	}, nil)
	entry := mustCreateWatchlistEntry(t, "沙丘", models.WatchlistKindMovie)

	harness.service.runEnrichmentOnce(context.Background())

	saved := reloadWatchlistEntry(t, entry.ID)
	if saved.EnrichmentStatus != models.WatchlistEnrichmentFailed ||
		saved.EnrichmentError != string(WatchlistMetadataFailureSourceError) {
		t.Fatalf("应落 failed/source_error，实际 %q/%q", saved.EnrichmentStatus, saved.EnrichmentError)
	}
	if saved.SourceItemID != "" {
		t.Errorf("不得把超长 ID 写进库，实际 %q", saved.SourceItemID)
	}
}

// 空的类型标签与主创存空串，不存 "[]" / "{}"——列的默认值就是空串。
func TestWatchlistEnrichEncodesEmptyListsAsEmptyString(t *testing.T) {
	if got := encodeWatchlistGenres(nil); got != "" {
		t.Errorf("空类型标签应存空串，实际 %q", got)
	}
	if got := encodeWatchlistGenres([]string{" ", ""}); got != "" {
		t.Errorf("全是空白的类型标签应存空串，实际 %q", got)
	}
	if got := encodeWatchlistCredits(&WatchlistMetadataDetail{}); got != "" {
		t.Errorf("空主创应存空串，实际 %q", got)
	}
	encoded := encodeWatchlistCredits(&WatchlistMetadataDetail{Directors: []string{" 诺兰 "}, Cast: nil})
	var credits WatchlistCredits
	if err := json.Unmarshal([]byte(encoded), &credits); err != nil {
		t.Fatalf("主创应是合法 JSON: %v", err)
	}
	if len(credits.Directors) != 1 || credits.Directors[0] != "诺兰" {
		t.Errorf("主创应去掉首尾空白，实际 %+v", credits)
	}
}

// ---------- 辅助 ----------

// assertWatchlistEnrichFailure 用给定的源跑完一轮，断言条目落 failed 加指定分类码。
func assertWatchlistEnrichFailure(t *testing.T, source WatchlistMetadataSource, poster *http.Client, want WatchlistMetadataFailure) {
	t.Helper()
	harness := newEnrichHarness(t, map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {source},
	}, poster)
	entry := mustCreateWatchlistEntry(t, "待补全", models.WatchlistKindMovie)

	harness.service.runEnrichmentOnce(context.Background())

	saved := reloadWatchlistEntry(t, entry.ID)
	if saved.EnrichmentStatus != models.WatchlistEnrichmentFailed {
		t.Fatalf("应落 failed，实际 %q", saved.EnrichmentStatus)
	}
	if saved.EnrichmentError != string(want) {
		t.Fatalf("分类码应为 %q，实际 %q", want, saved.EnrichmentError)
	}
	if saved.EnrichmentClaim != "" {
		t.Errorf("落定后应清空认领标识，实际 %q", saved.EnrichmentClaim)
	}
	if got := harness.statuses(); len(got) != 2 ||
		got[0] != models.WatchlistEnrichmentRunning || got[1] != models.WatchlistEnrichmentFailed {
		t.Errorf("状态落定各发一次进度事件，实际 %v", got)
	}
	if events := harness.events(); len(events) == 2 && events[1].Failure != string(want) {
		t.Errorf("进度事件应带上分类码，实际 %q", events[1].Failure)
	}
}

func newTMDBStubServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func newTMDBSourceAgainst(server *httptest.Server) *TMDBWatchlistMetadataSource {
	source := NewTMDBWatchlistMetadataSource("test-key", server.Client())
	source.baseURL = server.URL
	return source
}

// closedLocalAddress 返回一个确定没人监听的本机地址：先占一个端口拿到地址，
// 再立刻释放。比硬编码端口可靠。
func closedLocalAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用临时端口失败: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("释放临时端口失败: %v", err)
	}
	return address
}
