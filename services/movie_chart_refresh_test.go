package services

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

// 本文件按需求设计文档 §5（并发契约）与概要设计 §4.1（刷新流程）逐条验收。
// 每个用例注明它钉的是哪一条不变量。
//
// 限速不真睡：一年约 1500 条详情，真等 1 次/秒是 25 分钟。夹具把 sleep 换成
// 只记账的实现，间隔仍是 movieChartDetailInterval——「等过几次、每次等多久」照样
// 可断言，只是不消耗真实时间。

// ---------- 夹具 ----------

// movieChartHarnessStopBudget 是夹具收摊的等待预算，见 newMovieChartHarnessWithWatchlist。
// 取消之后剩下的只是几条本地语句，正常路径是毫秒级；这个数量级的余量只为把
// 「用例失败导致永远等下去」变成一条普通失败。
const movieChartHarnessStopBudget = 10 * time.Second

// movieChartTestNow 是夹具的固定时钟。纳秒位取 0：Postgres 的 timestamptz 只到
// 微秒，带纳秒的时间戳往返之后与原值不相等，断言会在 PG 腿上假红。
var movieChartTestNow = time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)

type movieChartHarness struct {
	t        *testing.T
	db       *gorm.DB
	service  *MovieChartService
	registry *BackgroundTaskRegistry

	mu     sync.Mutex
	clock  time.Time
	sleeps []time.Duration
	tasks  [][]string
}

func newMovieChartHarness(t *testing.T, source MovieChartSource) *movieChartHarness {
	t.Helper()
	return newMovieChartHarnessWithWatchlist(t, source, nil)
}

// newMovieChartHarnessWithWatchlist 是带片单服务的夹具。
//
// watchlist 允许为 nil：刷新、补全与读路径都不碰想看片单，只有标记的两个入口用得上
// （那一族用例走 movie_chart_marks_test.go 的夹具，它会真给一个）。
func newMovieChartHarnessWithWatchlist(t *testing.T, source MovieChartSource, watchlist *WatchlistService) *movieChartHarness {
	t.Helper()
	harness := &movieChartHarness{
		t:        t,
		db:       dbtest.Open(t),
		registry: NewBackgroundTaskRegistry(),
		clock:    movieChartTestNow,
	}
	harness.registry.SetOnChange(func(running []string) {
		harness.mu.Lock()
		harness.tasks = append(harness.tasks, running)
		harness.mu.Unlock()
	})
	service := NewMovieChartService(harness.db, watchlist)
	service.newSource = func() (MovieChartSource, error) { return source, nil }
	// 海报客户端默认是一个**陷阱**：本仓库不对豆瓣图床发真实请求（用户机器正被
	// 限流），需要海报的用例用 newMovieChartPosterHarness 显式装桩。注入 nil 的
	// watchlist 时托管图片服务本来就是 nil、海报阶段直接跳过，这道只是第二层。
	service.newPosterClient = func() (*http.Client, error) {
		t.Errorf("本用例没有装配海报客户端，却要出网下载海报")
		return nil, errors.New("测试未装配海报客户端")
	}
	service.now = harness.now
	service.sleep = harness.sleep
	service.SetBackgroundTaskRegistry(harness.registry)
	harness.service = service
	// 用例结束前一定要把后台那一轮收干净：不等它结束，dbtest 的 Cleanup 关掉连接
	// 之后那一轮还在往库里写，测试之间会互相看到对方的日志与失败。
	// dbtest.Open 的 Cleanup 注册得更早，因此一定排在这条之后执行。
	//
	// 等待有预算：桩适配器普遍是阻塞在用例给的通道上的，用例一旦在放开它之前
	// t.Fatalf，这里就会等一个永远不会结束的轮次——整个包卡到 go test 的 10 分钟
	// 超时，一次失败变成一次超时，变异测试就没人愿意做了。超时就记一条失败并往下走。
	t.Cleanup(func() {
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			service.StopRefreshAndWait()
		}()
		select {
		case <-stopped:
		case <-time.After(movieChartHarnessStopBudget):
			t.Errorf("后台刷新在用例结束后 %v 内没有收摊（多半是用例失败时没放开桩适配器的阻塞）", movieChartHarnessStopBudget)
		}
	})
	return harness
}

func (h *movieChartHarness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func (h *movieChartHarness) setClock(at time.Time) {
	h.mu.Lock()
	h.clock = at
	h.mu.Unlock()
}

func (h *movieChartHarness) sleep(ctx context.Context, d time.Duration) error {
	h.mu.Lock()
	h.sleeps = append(h.sleeps, d)
	h.mu.Unlock()
	return ctx.Err()
}

func (h *movieChartHarness) sleepCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sleeps)
}

func (h *movieChartHarness) taskSeen(key BackgroundTaskKey) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, snapshot := range h.tasks {
		for _, running := range snapshot {
			if running == string(key) {
				return true
			}
		}
	}
	return false
}

func (h *movieChartHarness) entry(doubanID string) models.MovieChartEntry {
	h.t.Helper()
	var entry models.MovieChartEntry
	if err := h.db.Where("douban_id = ?", doubanID).First(&entry).Error; err != nil {
		h.t.Fatalf("读取条目 %s 失败: %v", doubanID, err)
	}
	return entry
}

func (h *movieChartHarness) entryCount() int64 {
	h.t.Helper()
	var count int64
	if err := h.db.Model(&models.MovieChartEntry{}).Count(&count).Error; err != nil {
		h.t.Fatalf("统计条目失败: %v", err)
	}
	return count
}

// movieChartStatementTable 取一条语句打的是哪张表。Statement.Table 在 Before 钩子里
// 可能还没填，但 Execute 在跑回调之前一定先 Parse 过 Schema，所以退到 Schema.Table。
func movieChartStatementTable(tx *gorm.DB) string {
	if tx.Statement.Table != "" {
		return tx.Statement.Table
	}
	if tx.Statement.Schema != nil {
		return tx.Statement.Schema.Table
	}
	return ""
}

// hookQuery / hookUpdate 在测试库上挂一个只认 movie_chart_entries 的回调，用完即摘。
//
// 之所以要动到这一层：本文件里有三条不变量在库的**最终状态**上留不下任何区别——
// 「取消之后不再发认领 UPDATE」（发出去的语句反正都会失败）、「认领写进去之后读回
// 失败要把认领退回来」、「读回的行不是本次认领就别再往外发请求」。它们只能在语句
// 这一层观察，或者靠时序注入来触发。
func (h *movieChartHarness) hookQuery(name string, fn func(tx *gorm.DB)) {
	h.t.Helper()
	if err := h.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if movieChartStatementTable(tx) == "movie_chart_entries" {
			fn(tx)
		}
	}); err != nil {
		h.t.Fatalf("注册查询钩子失败: %v", err)
	}
	h.t.Cleanup(func() { _ = h.db.Callback().Query().Remove(name) })
}

func (h *movieChartHarness) hookUpdate(name string, when string, fn func(tx *gorm.DB)) {
	h.t.Helper()
	processor := h.db.Callback().Update()
	var err error
	if when == "before" {
		err = processor.Before("gorm:update").Register(name, func(tx *gorm.DB) {
			if movieChartStatementTable(tx) == "movie_chart_entries" {
				fn(tx)
			}
		})
	} else {
		err = processor.After("gorm:update").Register(name, func(tx *gorm.DB) {
			if movieChartStatementTable(tx) == "movie_chart_entries" {
				fn(tx)
			}
		})
	}
	if err != nil {
		h.t.Fatalf("注册更新钩子失败: %v", err)
	}
	h.t.Cleanup(func() { _ = h.db.Callback().Update().Remove(name) })
}

func (h *movieChartHarness) yearState(year int) models.MovieChartYearState {
	h.t.Helper()
	var state models.MovieChartYearState
	if err := h.db.Where("year = ?", year).First(&state).Error; err != nil {
		h.t.Fatalf("读取 %d 年状态失败: %v", year, err)
	}
	return state
}

// movieChartPageRequest 是假适配器收到的一次翻页请求。
type movieChartPageRequest struct {
	Year  int
	Sort  string
	Start int
}

// fakeMovieChartSource 复刻 MovieChartSource 的合同（固定翻 MovieChartPagesPerSort
// 页、不看返回条数、取消与 visit 错误立即中止），用来把编排与出网解耦地测掉。
// 真适配器那段翻页循环由 P-002 的 TC-01 钉住，本文件另有一条走真适配器的用例。
type fakeMovieChartSource struct {
	mu          sync.Mutex
	page        func(req movieChartPageRequest) (MovieChartListPage, error)
	detail      func(ctx context.Context, doubanID string) (*MovieChartDetail, error)
	listCalls   []movieChartPageRequest
	detailCalls []string
}

func (f *fakeMovieChartSource) Name() string { return "fake-movie-chart" }

func (f *fakeMovieChartSource) ListYear(ctx context.Context, year int, sortKey string, visit MovieChartListVisitor) error {
	for index := 0; index < MovieChartPagesPerSort; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		req := movieChartPageRequest{Year: year, Sort: sortKey, Start: index * MovieChartListPageSize}
		f.mu.Lock()
		f.listCalls = append(f.listCalls, req)
		f.mu.Unlock()
		page, err := f.page(req)
		if err != nil {
			return err
		}
		page.Sort, page.Start = sortKey, req.Start
		if err := visit(page); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeMovieChartSource) Detail(ctx context.Context, doubanID string) (*MovieChartDetail, error) {
	f.mu.Lock()
	f.detailCalls = append(f.detailCalls, doubanID)
	f.mu.Unlock()
	if f.detail == nil {
		return movieChartFakeDetail(doubanID), nil
	}
	return f.detail(ctx, doubanID)
}

func (f *fakeMovieChartSource) calls() ([]movieChartPageRequest, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]movieChartPageRequest(nil), f.listCalls...), append([]string(nil), f.detailCalls...)
}

func (f *fakeMovieChartSource) reset() {
	f.mu.Lock()
	f.listCalls, f.detailCalls = nil, nil
	f.mu.Unlock()
}

func movieChartFakeItem(id string) MovieChartListItem {
	return MovieChartListItem{
		DoubanID:     id,
		Title:        "片" + id,
		CardSubtitle: "2026 / 中国大陆 / 剧情",
		Rating:       8.1,
		RatingCount:  1000,
		PosterURL:    "https://img2.doubanio.com/view/photo/l/public/" + id + ".jpg",
	}
}

func movieChartFakePage(ids ...string) MovieChartListPage {
	page := MovieChartListPage{Items: make([]MovieChartListItem, 0, len(ids))}
	for _, id := range ids {
		page.Items = append(page.Items, movieChartFakeItem(id))
	}
	return page
}

func movieChartFakeDetail(id string) *MovieChartDetail {
	return &MovieChartDetail{
		DoubanID:       id,
		Title:          "片" + id,
		OriginalTitle:  "Movie " + id,
		ReleaseScope:   models.MovieChartScopeTheatrical,
		ReleaseDate:    "2026-03-20",
		ReleasePubdate: "2026-03-20(美国/中国大陆)",
		IsReleased:     true,
		Rating:         7.5,
		RatingCount:    320,
		Countries:      []string{"美国"},
		Genres:         []string{"剧情", "冒险"},
		Directors:      []string{"导演A"},
		Cast:           []string{"主演A", "主演B"},
		Overview:       "简介" + id,
	}
}

// ---------- 走真适配器的整轮闭环 ----------

// movieChartSortItems 让四种排序的结果**互相重叠**：并集只有四个豆瓣 ID，
// 合并去重之后表里必须恰好四行（D-MC03 + D-MC04）。
var movieChartSortItems = map[string][]string{
	MovieChartSortComprehensive: {"1001", "1002"},
	MovieChartSortRecentHeat:    {"1002", "1003"},
	MovieChartSortReleaseDate:   {"1003", "1004"},
	MovieChartSortRating:        {"1004", "1001"},
}

func movieChartListItemJSON(id string) string {
	return fmt.Sprintf(`{"id":"%s","title":"列表片名 %s","type":"movie","year":"2026","card_subtitle":"2026 / 中国大陆 / 剧情","rating":{"value":8.1,"count":1000},"pic":{"large":"https://img2.doubanio.com/view/photo/l/public/%s.jpg"}}`, id, id, id)
}

func movieChartDetailJSON(id string) string {
	return fmt.Sprintf(`{"id":"%s","title":"详情片名 %s","original_title":"Movie %s","type":"movie","is_tv":false,"is_released":true,
		"intro":"简介 %s","pubdate":["2026-03-20(美国/中国大陆)"],"countries":["美国"],"genres":["剧情","冒险"],
		"rating":{"value":7.5,"count":320},"directors":[{"name":"导演A"}],"actors":[{"name":"主演A"},{"name":"主演B"}]}`, id, id, id, id)
}

// 一轮刷新的闭环（TC-02 / TC-08 / TC-12 的抓取侧，走**真适配器**）：
//   - 四种排序各固定翻 25 页（start = 0,20,…,480），中间的空页不终止；
//   - 同一条目在四种排序里各出现一次，合并后表内恒为一行；
//   - 同一页里出现两次的豆瓣 ID 先去重再 upsert（不去重 Postgres 报 21000 整批失败）；
//   - 列表阶段成功后写 last_refreshed_at、清失败码，再逐条补全详情；
//   - 详情请求之间限速一次。
func TestMovieChartRefreshWalksAllSortsAndBackfillsDetails(t *testing.T) {
	var mu sync.Mutex
	starts := map[string][]int{}
	var detailIDs []string

	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == doubanMovieChartListPath {
			query := r.URL.Query()
			start, err := strconv.Atoi(query.Get("start"))
			if err != nil {
				t.Errorf("start 不是整数: %q", query.Get("start"))
			}
			sortKey := query.Get("sort")
			mu.Lock()
			starts[sortKey] = append(starts[sortKey], start)
			mu.Unlock()
			switch {
			case start == 40:
				// 实测过的瞬时空页：必须继续翻下一页，不终止、不记失败码（D-MC07）。
				io.WriteString(w, `{"items":[]}`)
			case start == 60:
				// 同一页里出现两次同一个豆瓣 ID：服务必须在构造批次前去重。
				id := movieChartSortItems[sortKey][0]
				io.WriteString(w, `{"items":[`+movieChartListItemJSON(id)+`,`+movieChartListItemJSON(id)+`]}`)
			default:
				parts := make([]string, 0, 2)
				for _, id := range movieChartSortItems[sortKey] {
					parts = append(parts, movieChartListItemJSON(id))
				}
				io.WriteString(w, `{"items":[`+strings.Join(parts, ",")+`]}`)
			}
			return
		}
		id := strings.TrimPrefix(r.URL.Path, doubanMovieChartDetailPath)
		mu.Lock()
		detailIDs = append(detailIDs, id)
		mu.Unlock()
		io.WriteString(w, movieChartDetailJSON(id))
	}

	harness := newMovieChartHarness(t, movieChartStub(t, handler))
	if err := harness.service.refreshYear(context.Background(), 2026); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(starts) != len(MovieChartSorts()) {
		t.Fatalf("排序数 = %d，期望 %d", len(starts), len(MovieChartSorts()))
	}
	for _, sortKey := range MovieChartSorts() {
		got := starts[sortKey]
		if len(got) != MovieChartPagesPerSort {
			t.Fatalf("排序 %s 翻了 %d 页，期望固定 %d 页（空页不得终止）", sortKey, len(got), MovieChartPagesPerSort)
		}
		for index, start := range got {
			if want := index * MovieChartListPageSize; start != want {
				t.Fatalf("排序 %s 第 %d 页 start=%d，期望 %d", sortKey, index, start, want)
			}
		}
	}

	if got := harness.entryCount(); got != 4 {
		t.Fatalf("合并去重后应为 4 行，实际 %d 行", got)
	}
	// 详情按 id 升序逐条认领，一条一次，不重复。
	if len(detailIDs) != 4 {
		t.Fatalf("详情请求 %d 次，期望 4 次: %v", len(detailIDs), detailIDs)
	}
	// 四条详情之间等三次，每次一秒（D-MC06）。
	if got := harness.sleepCount(); got != 3 {
		t.Fatalf("限速等待 %d 次，期望 3 次（4 条详情之间）", got)
	}
	for _, d := range harness.sleeps {
		if d != movieChartDetailInterval {
			t.Fatalf("限速间隔 = %v，期望 %v", d, movieChartDetailInterval)
		}
	}

	entry := harness.entry("1001")
	if entry.Title != "列表片名 1001" {
		t.Fatalf("片名归列表阶段拥有，不该被详情覆盖: %q", entry.Title)
	}
	if entry.OriginalTitle != "Movie 1001" || entry.Overview != "简介 1001" {
		t.Fatalf("详情字段未落库: %+v", entry)
	}
	if entry.ReleaseScope != models.MovieChartScopeTheatrical || entry.ReleaseDate != "2026-03-20" {
		t.Fatalf("上映判定未落库: scope=%q date=%q", entry.ReleaseScope, entry.ReleaseDate)
	}
	if entry.Genres != "剧情\n冒险" || entry.Cast != "主演A\n主演B" || entry.Countries != "美国" {
		t.Fatalf("多值列拼接不符: genres=%q cast=%q countries=%q", entry.Genres, entry.Cast, entry.Countries)
	}
	if entry.DetailStatus != models.MovieChartDetailSucceeded || entry.DetailError != "" || entry.DetailClaim != "" {
		t.Fatalf("补全状态未落定: %+v", entry)
	}
	if entry.DetailFetchedAt == nil || !entry.DetailFetchedAt.Equal(movieChartTestNow) {
		t.Fatalf("detail_fetched_at 未写: %v", entry.DetailFetchedAt)
	}
	if entry.ListFetchedAt == nil || !entry.ListFetchedAt.Equal(movieChartTestNow) {
		t.Fatalf("list_fetched_at 未写: %v", entry.ListFetchedAt)
	}
	if entry.Year != 2026 {
		t.Fatalf("year 应为请求年份，实际 %d", entry.Year)
	}

	state := harness.yearState(2026)
	if state.LastRefreshedAt == nil || !state.LastRefreshedAt.Equal(movieChartTestNow) {
		t.Fatalf("整轮成功后应写 last_refreshed_at: %v", state.LastRefreshedAt)
	}
	if state.LastAttemptAt == nil || state.LastFailure != "" {
		t.Fatalf("成功一轮的年状态不符: %+v", state)
	}
	if !harness.taskSeen(BackgroundTaskMovieChart) {
		t.Fatal("后台任务登记表没有出现 movie_chart")
	}
	if got := harness.registry.Snapshot(); len(got) != 0 {
		t.Fatalf("刷新结束后不该有残留任务: %v", got)
	}
}

// ---------- 增量语义 ----------

// TC-02：第二轮列表重抓只更新列表阶段拥有的列，**绝不**把已补全的条目打回 pending。
// 这一条钉的是 movieChartListDoUpdateColumns：清单里混进任何 detail_* 或 release_*，
// 已补全的两行都会被打回 pending，下面的「不得再请求详情」立刻判红。
func TestMovieChartSecondListRoundKeepsBackfilledDetails(t *testing.T) {
	round := 1
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			page := movieChartFakePage("2001", "2002")
			if round == 2 {
				for index := range page.Items {
					page.Items[index].Title = "第二轮片名"
					page.Items[index].Rating = 9.9
					page.Items[index].RatingCount = 2000
					page.Items[index].CardSubtitle = "第二轮副标题"
					page.Items[index].PosterURL = "https://img2.doubanio.com/view/photo/l/public/new.jpg"
				}
			}
			return page, nil
		},
	}
	harness := newMovieChartHarness(t, source)
	if err := harness.service.refreshYear(context.Background(), 2026); err != nil {
		t.Fatalf("第一轮失败: %v", err)
	}
	first := harness.entry("2001")
	if first.DetailStatus != models.MovieChartDetailSucceeded {
		t.Fatalf("第一轮应补全完成: %+v", first)
	}

	round = 2
	source.reset()
	source.detail = func(context.Context, string) (*MovieChartDetail, error) {
		t.Error("已补全的条目不得在第二轮再取一次详情——detail_* 被列表 upsert 打回了")
		return movieChartFakeDetail("x"), nil
	}
	harness.setClock(movieChartTestNow.Add(48 * time.Hour))
	if err := harness.service.refreshYear(context.Background(), 2026); err != nil {
		t.Fatalf("第二轮失败: %v", err)
	}

	if got := harness.entryCount(); got != 2 {
		t.Fatalf("两轮抓取后应仍为 2 行，实际 %d 行", got)
	}
	_, detailCalls := source.calls()
	if len(detailCalls) != 0 {
		t.Fatalf("第二轮不该有任何详情请求: %v", detailCalls)
	}
	second := harness.entry("2001")
	// 列表阶段拥有的列被更新。
	if second.Title != "第二轮片名" || second.CardSubtitle != "第二轮副标题" ||
		second.Rating != 9.9 || second.RatingCount != 2000 ||
		second.PosterURL != "https://img2.doubanio.com/view/photo/l/public/new.jpg" {
		t.Fatalf("列表阶段拥有的列没有被第二轮更新: %+v", second)
	}
	if second.ListFetchedAt == nil || !second.ListFetchedAt.Equal(movieChartTestNow.Add(48*time.Hour)) {
		t.Fatalf("list_fetched_at 未随第二轮更新: %v", second.ListFetchedAt)
	}
	// 详情阶段拥有的列一个都没被动。
	if second.DetailStatus != models.MovieChartDetailSucceeded {
		t.Fatalf("第二轮把已补全的条目打回了 %q", second.DetailStatus)
	}
	if second.ReleaseScope != models.MovieChartScopeTheatrical || second.ReleaseDate != "2026-03-20" ||
		second.ReleasePubdate != "2026-03-20(美国/中国大陆)" || second.OriginalTitle != "Movie 2001" ||
		second.Overview != "简介2001" || !second.IsReleased {
		t.Fatalf("详情阶段拥有的列被列表 upsert 覆盖了: %+v", second)
	}
	if second.DetailFetchedAt == nil || !second.DetailFetchedAt.Equal(movieChartTestNow) {
		t.Fatalf("detail_fetched_at 被改动了: %v", second.DetailFetchedAt)
	}
	// updated_at 必须由 DoUpdates 显式赋值：GORM 在这条路径上不维护它。不显式写的话
	// 这一列会永远停在**插入那一刻的真实时间**，和第二轮的时钟对不上。
	if !second.UpdatedAt.Equal(movieChartTestNow.Add(48 * time.Hour)) {
		t.Fatalf("冲突更新没有显式写 updated_at: %v", second.UpdatedAt)
	}
	// 第二轮也成功，last_refreshed_at 必须前移——这一次走的是年状态的 UPDATE 分支。
	if state := harness.yearState(2026); state.LastRefreshedAt == nil ||
		!state.LastRefreshedAt.Equal(movieChartTestNow.Add(48*time.Hour)) {
		t.Fatalf("第二轮成功后 last_refreshed_at 未前移: %v", state.LastRefreshedAt)
	}
}

// 年状态的 **UPDATE 分支**（该年已经有行时才走的那一条）。
//
// 单独立一条的原因：其余用例都从空表起步，writeYearState 走的是 INSERT，断言被
// 结构体字面量满足，assignments 那张图根本没跑过——而 assignments 才是稳态下唯一
// 会执行的代码。漏掉它最坏的后果正是 §4.2 要防的那件事：一次失败把
// last_refreshed_at 推到现在，页面于是在陈旧缓存上写「数据更新于 刚刚」，
// 30 天计时也跟着归零。
func TestMovieChartYearStateUpsertUpdatesExistingRow(t *testing.T) {
	failing := false
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			if failing {
				return MovieChartListPage{}, newWatchlistMetadataSourceError(
					"fake-movie-chart", WatchlistMetadataFailureProxyUnreachable, 0, "代理不通", nil)
			}
			return movieChartFakePage("7701"), nil
		},
	}
	harness := newMovieChartHarness(t, source)
	// 预置：40 天前成功过，之后失败过一次。此后每一次 writeYearState 都走 UPDATE。
	stale := movieChartTestNow.Add(-40 * 24 * time.Hour)
	if err := harness.db.Create(&models.MovieChartYearState{
		Year: 2026, LastRefreshedAt: &stale, LastAttemptAt: &stale,
		LastFailure: string(WatchlistMetadataFailureNetworkUnreachable),
	}).Error; err != nil {
		t.Fatalf("准备年状态失败: %v", err)
	}

	// (a) 成功一轮：三列都要动，失败码要清掉。
	if err := harness.service.refreshYear(context.Background(), 2026); err != nil {
		t.Fatalf("成功一轮失败: %v", err)
	}
	state := harness.yearState(2026)
	if state.LastRefreshedAt == nil || !state.LastRefreshedAt.Equal(movieChartTestNow) {
		t.Fatalf("成功一轮后 last_refreshed_at 未前移（仍是 %v）", state.LastRefreshedAt)
	}
	if state.LastAttemptAt == nil || !state.LastAttemptAt.Equal(movieChartTestNow) {
		t.Fatalf("成功一轮后 last_attempt_at 未前移: %v", state.LastAttemptAt)
	}
	if state.LastFailure != "" {
		t.Fatalf("成功一轮必须清掉上一轮的失败码，实际 %q", state.LastFailure)
	}

	// (b) 紧接着失败一轮：只动 last_attempt_at 与 last_failure。
	failing = true
	later := movieChartTestNow.Add(2 * time.Hour)
	harness.setClock(later)
	if err := harness.service.refreshYear(context.Background(), 2026); err == nil {
		t.Fatal("失败一轮应上抛错误")
	}
	state = harness.yearState(2026)
	if state.LastRefreshedAt == nil || !state.LastRefreshedAt.Equal(movieChartTestNow) {
		t.Fatalf("失败的一轮把 last_refreshed_at 推后了: %v（应仍为上一轮成功的时刻）", state.LastRefreshedAt)
	}
	if state.LastAttemptAt == nil || !state.LastAttemptAt.Equal(later) {
		t.Fatalf("失败的一轮也要写 last_attempt_at: %v", state.LastAttemptAt)
	}
	if state.LastFailure != string(WatchlistMetadataFailureProxyUnreachable) {
		t.Fatalf("失败分类码 = %q，期望 proxy_unreachable", state.LastFailure)
	}
}

// 批内重复（TC-12）：同一批里出现两条相同 douban_id 必须先去重。不去重时
// Postgres 报 21000（ON CONFLICT DO UPDATE command cannot affect row a second time）
// 整批失败，SQLite 不报——所以这一条只有跑 PG 腿才真正被覆盖。
func TestMovieChartUpsertDedupesDuplicateDoubanIDInOneBatch(t *testing.T) {
	harness := newMovieChartHarness(t, &fakeMovieChartSource{})
	first := movieChartFakeItem("3001")
	second := movieChartFakeItem("3001")
	second.Title = "后出现的片名"
	items := []MovieChartListItem{first, movieChartFakeItem("3002"), second}

	if err := harness.service.upsertListPage(context.Background(), 2026, items); err != nil {
		t.Fatalf("批内重复导致整批失败: %v", err)
	}
	if got := harness.entryCount(); got != 2 {
		t.Fatalf("去重后应为 2 行，实际 %d 行", got)
	}
	if got := harness.entry("3001").Title; got != "后出现的片名" {
		t.Fatalf("同一批里后出现的条目应覆盖先出现的，实际 %q", got)
	}
}

// resetYearDetailBacklog 的**年份范围**与**清哪几列**。
//
// 年份范围是承重的：不带 year 过滤的话，一次 2026 年的刷新会把 2025 年失败的条目
// 也翻成 pending 并抹掉它们的失败码，而 2026 年的详情阶段只认 2026 的行，那些被
// 翻过来的条目从此永远停在 pending，谁也不去补——界面上的「补全中 N/M」再也到不了头。
func TestMovieChartResetYearDetailBacklogIsScopedAndClearsColumns(t *testing.T) {
	harness := newMovieChartHarness(t, &fakeMovieChartSource{})
	seed := []models.MovieChartEntry{
		{DoubanID: "a1", Year: 2026, DetailStatus: models.MovieChartDetailFailed,
			DetailError: string(WatchlistMetadataFailureNotFound), DetailClaim: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{DoubanID: "a2", Year: 2026, DetailStatus: models.MovieChartDetailRunning,
			DetailClaim: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{DoubanID: "a3", Year: 2026, DetailStatus: models.MovieChartDetailSucceeded},
		{DoubanID: "a4", Year: 2025, DetailStatus: models.MovieChartDetailFailed,
			DetailError: string(WatchlistMetadataFailureSourceError), DetailClaim: "cccccccccccccccccccccccccccccccc"},
	}
	for index := range seed {
		if err := harness.db.Create(&seed[index]).Error; err != nil {
			t.Fatalf("准备条目 %s 失败: %v", seed[index].DoubanID, err)
		}
	}

	if err := harness.service.resetYearDetailBacklog(context.Background(), 2026); err != nil {
		t.Fatalf("重置失败: %v", err)
	}

	for _, id := range []string{"a1", "a2"} {
		row := harness.entry(id)
		if row.DetailStatus != models.MovieChartDetailPending {
			t.Fatalf("%s 应回到 pending，实际 %q", id, row.DetailStatus)
		}
		if row.DetailClaim != "" {
			t.Fatalf("%s 的认领标识未清空: %q（留着它下一轮的 ABA 比对就多一个没有主人的旧值）", id, row.DetailClaim)
		}
		if row.DetailError != "" {
			t.Fatalf("%s 的失败码未清空: %q（pending 的条目挂着失败码，界面会给排队中的条目显示失败提示）", id, row.DetailError)
		}
	}
	if got := harness.entry("a3").DetailStatus; got != models.MovieChartDetailSucceeded {
		t.Fatalf("已补全的条目不该被重置，实际 %q", got)
	}
	other := harness.entry("a4")
	if other.DetailStatus != models.MovieChartDetailFailed ||
		other.DetailError != string(WatchlistMetadataFailureSourceError) ||
		other.DetailClaim != "cccccccccccccccccccccccccccccccc" {
		t.Fatalf("重置越界动了 2025 年的条目: %+v", other)
	}
}

// 整轮刷新只作用于它自己那一年：详情阶段不抢别的年份的待办，列表 upsert 会把
// 重新出现在新一年榜单里的条目改判到新年份。
func TestMovieChartRefreshIsScopedToItsYear(t *testing.T) {
	year := 2025
	ids := []string{"5001", "5002"}
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			return movieChartFakePage(ids...), nil
		},
	}
	source.detail = func(_ context.Context, id string) (*MovieChartDetail, error) {
		if id == "5002" {
			return nil, newWatchlistMetadataSourceError("fake-movie-chart", WatchlistMetadataFailureNotFound, 404, "查无此片", nil)
		}
		return movieChartFakeDetail(id), nil
	}
	harness := newMovieChartHarness(t, source)
	if err := harness.service.refreshYear(context.Background(), year); err != nil {
		t.Fatalf("2025 年这一轮失败: %v", err)
	}
	// 再塞一条 2025 年、停在 pending 的条目：它是「详情阶段是否越界取待办」的探针。
	if err := harness.db.Create(&models.MovieChartEntry{
		DoubanID: "5003", Year: 2025, DetailStatus: models.MovieChartDetailPending,
	}).Error; err != nil {
		t.Fatalf("准备 2025 年待办失败: %v", err)
	}

	// 2026 年这一轮：5001 重新出现在新一年的榜单里，6001 是新条目。
	year, ids = 2026, []string{"5001", "6001"}
	source.reset()
	if err := harness.service.refreshYear(context.Background(), year); err != nil {
		t.Fatalf("2026 年这一轮失败: %v", err)
	}

	_, detailCalls := source.calls()
	if len(detailCalls) != 1 || detailCalls[0] != "6001" {
		t.Fatalf("详情阶段越界取了别的年份的待办，实际请求 %v（5003 属于 2025 年）", detailCalls)
	}
	if got := harness.entry("5001").Year; got != 2026 {
		t.Fatalf("重新出现在 2026 榜单里的条目应改判到新年份，实际 year=%d", got)
	}
	stale := harness.entry("5002")
	if stale.DetailStatus != models.MovieChartDetailFailed ||
		stale.DetailError != string(WatchlistMetadataFailureNotFound) {
		t.Fatalf("2026 年的刷新重置了 2025 年失败的条目: %+v", stale)
	}
	if got := harness.entry("5003").DetailStatus; got != models.MovieChartDetailPending {
		t.Fatalf("2025 年那条待办被动过了: %q", got)
	}
}

// 片名超过 size:200 时按 **rune** 截断。
//
// 两个后端上都必须有意义，所以断言的是**存进去的长度**而不是「插入没报错」：
// Postgres 不截断会直接 22001 整批失败，SQLite 不截断则会原样存下 250 个字符。
// 用汉字构造也是有意的——按字节切会切出半个字符，存进去就是乱码。
func TestMovieChartOverlongTitlesAreTruncatedByRune(t *testing.T) {
	long := strings.Repeat("影", 250)
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			page := movieChartFakePage("7801")
			page.Items[0].Title = long
			return page, nil
		},
	}
	source.detail = func(_ context.Context, id string) (*MovieChartDetail, error) {
		detail := movieChartFakeDetail(id)
		detail.OriginalTitle = long
		return detail, nil
	}
	harness := newMovieChartHarness(t, source)
	if err := harness.service.refreshYear(context.Background(), 2026); err != nil {
		t.Fatalf("超长片名让整轮失败了（Postgres 上就是 22001，这一年会被永久卡死）: %v", err)
	}

	entry := harness.entry("7801")
	want := strings.Repeat("影", movieChartTitleLimit)
	for _, item := range []struct {
		column string
		got    string
	}{{"title", entry.Title}, {"original_title", entry.OriginalTitle}} {
		if count := utf8.RuneCountInString(item.got); count != movieChartTitleLimit {
			t.Fatalf("%s 存了 %d 个字符，期望截到 %d", item.column, count, movieChartTitleLimit)
		}
		if !utf8.ValidString(item.got) {
			t.Fatalf("%s 存进去的不是合法 UTF-8——按字节切会切出半个汉字", item.column)
		}
		if item.got != want {
			t.Fatalf("%s 截断结果不符（前后不一致或切错位置）", item.column)
		}
	}
	if got := harness.entry("7801").DetailStatus; got != models.MovieChartDetailSucceeded {
		t.Fatalf("超长片名不该让这一条补不全: %q", got)
	}
}

// ---------- 失败与取消 ----------

// 列表阶段单页失败即中止整轮：不重试、不降级，已写入的条目全部保留，
// 年状态记下**源给出的**那一类分类码，last_refreshed_at 不动。
func TestMovieChartListPageFailureAbortsRoundAndKeepsRows(t *testing.T) {
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			if req.Sort == MovieChartSortComprehensive && req.Start == 40 {
				return MovieChartListPage{}, newWatchlistMetadataSourceError(
					"fake-movie-chart", WatchlistMetadataFailureNetworkUnreachable, 0, "连不上", nil)
			}
			return movieChartFakePage(fmt.Sprintf("40%02d", req.Start/MovieChartListPageSize)), nil
		},
	}
	harness := newMovieChartHarness(t, source)
	if err := harness.service.refreshYear(context.Background(), 2026); err == nil {
		t.Fatal("单页失败应中止整轮并上抛错误")
	}

	listCalls, detailCalls := source.calls()
	if len(listCalls) != 3 {
		t.Fatalf("应在第三页失败后立刻停手，实际请求了 %d 页", len(listCalls))
	}
	for _, call := range listCalls {
		if call.Sort != MovieChartSortComprehensive {
			t.Fatalf("中止后不该再抓下一种排序: %+v", call)
		}
	}
	if len(detailCalls) != 0 {
		t.Fatalf("列表阶段失败后不该进详情阶段: %v", detailCalls)
	}
	// 增量更新从不删除：失败前写进去的两页照常留着。
	if got := harness.entryCount(); got != 2 {
		t.Fatalf("已 upsert 的条目应保留 2 行，实际 %d 行", got)
	}
	state := harness.yearState(2026)
	if state.LastRefreshedAt != nil {
		t.Fatalf("失败的一轮不得写 last_refreshed_at: %v", state.LastRefreshedAt)
	}
	if state.LastAttemptAt == nil || !state.LastAttemptAt.Equal(movieChartTestNow) {
		t.Fatalf("每轮结束都要写 last_attempt_at: %v", state.LastAttemptAt)
	}
	// 六类不合并：源给的是 network_unreachable，不能被压平成 source_error。
	if state.LastFailure != string(WatchlistMetadataFailureNetworkUnreachable) {
		t.Fatalf("失败分类码 = %q，期望 network_unreachable", state.LastFailure)
	}
}

// 出网件装配失败（代理地址填错）不让这一轮悄悄什么都不做：照常写 proxy_unreachable。
func TestMovieChartSourceAssemblyFailureRecordsProxyClass(t *testing.T) {
	harness := newMovieChartHarness(t, &fakeMovieChartSource{})
	harness.service.newSource = func() (MovieChartSource, error) {
		return nil, fmt.Errorf("代理地址无法解析: %w", ErrWatchlistMetadataProxyInvalid)
	}
	if err := harness.service.refreshYear(context.Background(), 2026); err == nil {
		t.Fatal("装配失败应上抛错误")
	}
	state := harness.yearState(2026)
	if state.LastFailure != string(WatchlistMetadataFailureProxyUnreachable) {
		t.Fatalf("失败分类码 = %q，期望 proxy_unreachable", state.LastFailure)
	}
	if state.LastRefreshedAt != nil {
		t.Fatalf("装配失败不得写 last_refreshed_at: %v", state.LastRefreshedAt)
	}
}

// 取消不是失败：已写入的条目保留，last_refreshed_at 不更新，last_failure 一个字
// 都不碰——既不写新码（把用户的中止显示成故障），也不清旧码（抹掉上一轮真实的失败）。
func TestMovieChartCancelDuringListKeepsRowsAndWritesNoFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			if req.Start == 40 {
				cancel()
			}
			return movieChartFakePage(fmt.Sprintf("50%02d", req.Start/MovieChartListPageSize)), nil
		},
	}
	harness := newMovieChartHarness(t, source)
	// 先造一个「上次成功过、上次又失败过」的年状态，才能证明取消两者都不碰。
	previous := movieChartTestNow.Add(-72 * time.Hour)
	if err := harness.db.Create(&models.MovieChartYearState{
		Year: 2026, LastRefreshedAt: &previous, LastAttemptAt: &previous,
		LastFailure: string(WatchlistMetadataFailureSourceError),
	}).Error; err != nil {
		t.Fatalf("准备年状态失败: %v", err)
	}

	if err := harness.service.refreshYear(ctx, 2026); err == nil {
		t.Fatal("取消应上抛 ctx 错误")
	}
	if got := harness.entryCount(); got != 2 {
		t.Fatalf("取消前写进去的条目应保留 2 行，实际 %d 行", got)
	}
	state := harness.yearState(2026)
	if state.LastRefreshedAt == nil || !state.LastRefreshedAt.Equal(previous) {
		t.Fatalf("取消不得更新 last_refreshed_at: %v", state.LastRefreshedAt)
	}
	if state.LastFailure != string(WatchlistMetadataFailureSourceError) {
		t.Fatalf("取消不得改写 last_failure，实际 %q", state.LastFailure)
	}
	if state.LastAttemptAt == nil || !state.LastAttemptAt.Equal(movieChartTestNow) {
		t.Fatalf("取消这一轮仍要写 last_attempt_at: %v", state.LastAttemptAt)
	}
}

// 详情阶段中途取消：认领退回 pending、清空 claim，下一轮从这一条接着跑（可断点续跑）；
// 列表阶段已经成功，last_refreshed_at 照常留着。
func TestMovieChartCancelDuringDetailReleasesClaim(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var claimed string
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			return movieChartFakePage("6001"), nil
		},
	}
	harness := newMovieChartHarness(t, source)
	source.detail = func(context.Context, string) (*MovieChartDetail, error) {
		claimed = harness.entry("6001").DetailClaim
		cancel()
		return nil, context.Canceled
	}
	if err := harness.service.refreshYear(ctx, 2026); err != nil {
		t.Fatalf("详情阶段的取消不该让整轮返回错误: %v", err)
	}

	// 认领标识必须是 hex.EncodeToString(16 字节) 的 32 个十六进制字符。
	// uuid.NewString() 是 36 个字符，Postgres 会在 size:32 的列上直接报 22001。
	if len(claimed) != 32 {
		t.Fatalf("认领标识长度 = %d，期望 32", len(claimed))
	}
	if _, err := hex.DecodeString(claimed); err != nil {
		t.Fatalf("认领标识不是十六进制: %q", claimed)
	}
	entry := harness.entry("6001")
	if entry.DetailStatus != models.MovieChartDetailPending || entry.DetailClaim != "" {
		t.Fatalf("取消应把认领退回 pending 并清空标识: status=%q claim=%q", entry.DetailStatus, entry.DetailClaim)
	}
	if entry.DetailError != "" {
		t.Fatalf("取消不是失败，不该写分类码: %q", entry.DetailError)
	}
	state := harness.yearState(2026)
	if state.LastRefreshedAt == nil {
		t.Fatal("列表阶段已成功，last_refreshed_at 应当写下")
	}
}

// ---------- 详情阶段的并发契约 ----------

// TC-13 写回守卫（ABA）：认领之后用户点了刷新——状态回到 pending、又被**新一轮**
// 认领走（状态重新是 running，但 claim 换了）。迟到的写回必须被丢弃且不报错。
//
// 只比对状态的实现会在这里判红：写回时状态确实是 running。
func TestMovieChartLateDetailWriteBackIsDiscardedOnNewClaim(t *testing.T) {
	const newClaim = "ffffffffffffffffffffffffffffffff"
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			return movieChartFakePage("7001"), nil
		},
	}
	harness := newMovieChartHarness(t, source)
	source.detail = func(_ context.Context, id string) (*MovieChartDetail, error) {
		// 用户点刷新 → 状态回 pending；紧接着新一轮把它认领走 → 又是 running，
		// 但 claim 是另一个。这正是 ABA：状态值绕了一圈回到原地。
		if err := harness.db.Model(&models.MovieChartEntry{}).
			Where("douban_id = ?", id).
			Updates(map[string]any{
				"detail_status": models.MovieChartDetailRunning,
				"detail_claim":  newClaim,
			}).Error; err != nil {
			t.Fatalf("模拟新一轮认领失败: %v", err)
		}
		return movieChartFakeDetail(id), nil
	}
	if err := harness.service.refreshYear(context.Background(), 2026); err != nil {
		t.Fatalf("守卫不成立是正常分支，不该让整轮报错: %v", err)
	}

	entry := harness.entry("7001")
	if entry.DetailClaim != newClaim || entry.DetailStatus != models.MovieChartDetailRunning {
		t.Fatalf("迟到的写回覆盖了新一轮的认领: status=%q claim=%q", entry.DetailStatus, entry.DetailClaim)
	}
	if entry.ReleaseScope != "" || entry.ReleaseDate != "" || entry.OriginalTitle != "" {
		t.Fatalf("迟到的结果应当被丢弃，实际写进去了: %+v", entry)
	}
	if entry.DetailFetchedAt != nil {
		t.Fatalf("迟到的结果不该写 detail_fetched_at: %v", entry.DetailFetchedAt)
	}
}

// 守卫的**状态那一半**单独钉一遍：把条目改回 pending 但**故意不动 detail_claim**。
//
// 这样写不是为了模拟某条现有代码路径，而是因为守卫的两个条件各管一件事，必须分开
// 证明：claim 是 16 字节随机数，两个条件一起测时永远是 claim 先不匹配，状态那一半
// 就永远走不到。models/movie_chart.go 写明「写回路径只认 running，其余状态下到达的
// 结果一律丢弃」——去掉状态比对之后，任何把状态改走却漏清 claim 的路径都会被迟到的
// 结果覆盖。
func TestMovieChartLateDetailWriteBackRequiresRunningStatus(t *testing.T) {
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			return movieChartFakePage("7101"), nil
		},
	}
	harness := newMovieChartHarness(t, source)
	source.detail = func(_ context.Context, id string) (*MovieChartDetail, error) {
		if err := harness.db.Model(&models.MovieChartEntry{}).
			Where("douban_id = ?", id).
			Update("detail_status", models.MovieChartDetailPending).Error; err != nil {
			t.Fatalf("模拟状态被改走失败: %v", err)
		}
		return nil, newWatchlistMetadataSourceError("fake-movie-chart", WatchlistMetadataFailureNotFound, 404, "查无此片", nil)
	}
	if err := harness.service.refreshYear(context.Background(), 2026); err != nil {
		t.Fatalf("守卫不成立是正常分支，不该让整轮报错: %v", err)
	}
	entry := harness.entry("7101")
	if entry.DetailStatus != models.MovieChartDetailPending {
		t.Fatalf("迟到的写回改动了状态: %q", entry.DetailStatus)
	}
	if entry.DetailError != "" {
		t.Fatalf("迟到的失败码不该落到已经不是 running 的条目上: %q", entry.DetailError)
	}
}

// 认领影响 0 行＝在取待办与认领之间被别人拿走了：**跳过下一条，不报错**。
//
// 待办清单是一次性取出来的，取出之后到逐条认领之间有时间差——这个用例就落在那段
// 时间差里：处理第一条时第二条被另一个 worker 认领走，轮到它时条件更新必然影响 0 行。
func TestMovieChartClaimSkipsRowsTakenAfterListing(t *testing.T) {
	const foreignClaim = "00000000000000000000000000000000"
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			return movieChartFakePage("8001", "8002"), nil
		},
	}
	harness := newMovieChartHarness(t, source)
	source.detail = func(_ context.Context, id string) (*MovieChartDetail, error) {
		if id == "8001" {
			// 另一个 worker 在这期间认领走了 8002。
			if err := harness.db.Model(&models.MovieChartEntry{}).
				Where("douban_id = ?", "8002").
				Updates(map[string]any{
					"detail_status": models.MovieChartDetailRunning,
					"detail_claim":  foreignClaim,
				}).Error; err != nil {
				t.Fatalf("模拟他人认领失败: %v", err)
			}
		}
		return movieChartFakeDetail(id), nil
	}
	if err := harness.service.refreshYear(context.Background(), 2026); err != nil {
		t.Fatalf("认领不到不是错误，整轮不该失败: %v", err)
	}

	_, detailCalls := source.calls()
	if len(detailCalls) != 1 || detailCalls[0] != "8001" {
		t.Fatalf("被别人认领走的条目应跳过，实际详情请求 %v", detailCalls)
	}
	held := harness.entry("8002")
	if held.DetailStatus != models.MovieChartDetailRunning || held.DetailClaim != foreignClaim {
		t.Fatalf("别人持有的认领被动过了: status=%q claim=%q", held.DetailStatus, held.DetailClaim)
	}
	if harness.entry("8001").DetailStatus != models.MovieChartDetailSucceeded {
		t.Fatal("自己认领到的那一条应正常补全")
	}
}

// 详情阶段单条失败只影响该条：其余条目照常补全，整轮不中止。
// 失败后的条目在**下一轮刷新**里被放回 pending 重试；已成功的不再重取。
func TestMovieChartFailedDetailIsRetriedOnNextRefreshOnly(t *testing.T) {
	failing := true
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			return movieChartFakePage("9001", "9002"), nil
		},
	}
	source.detail = func(_ context.Context, id string) (*MovieChartDetail, error) {
		if id == "9002" && failing {
			return nil, newWatchlistMetadataSourceError("fake-movie-chart", WatchlistMetadataFailureNotFound, 404, "查无此片", nil)
		}
		return movieChartFakeDetail(id), nil
	}
	harness := newMovieChartHarness(t, source)
	if err := harness.service.refreshYear(context.Background(), 2026); err != nil {
		t.Fatalf("第一轮失败: %v", err)
	}
	if got := harness.entry("9001").DetailStatus; got != models.MovieChartDetailSucceeded {
		t.Fatalf("单条失败不该影响其他条目，9001 状态 = %q", got)
	}
	broken := harness.entry("9002")
	if broken.DetailStatus != models.MovieChartDetailFailed ||
		broken.DetailError != string(WatchlistMetadataFailureNotFound) || broken.DetailClaim != "" {
		t.Fatalf("失败条目落定不符: %+v", broken)
	}

	failing = false
	source.reset()
	if err := harness.service.refreshYear(context.Background(), 2026); err != nil {
		t.Fatalf("第二轮失败: %v", err)
	}
	_, detailCalls := source.calls()
	if len(detailCalls) != 1 || detailCalls[0] != "9002" {
		t.Fatalf("第二轮应只重试上一轮失败的那一条，实际 %v", detailCalls)
	}
	fixed := harness.entry("9002")
	if fixed.DetailStatus != models.MovieChartDetailSucceeded || fixed.DetailError != "" {
		t.Fatalf("重试后应补全成功: %+v", fixed)
	}
}

// 认领写进库之后、读回之前被取消：认领**必须退回去**。
//
// 不退的话调用方只会记一条日志就跳下一条，这一行永远停在 running：年内的补全进度
// 再也到不了头，而往年一旦有过 last_attempt_at 就不再自动刷新，只能等用户手点。
// 用查询钩子把「读回失败」变成确定事件——真实触发它要靠取消恰好落在这两条语句之间。
func TestMovieChartClaimReleasesItselfWhenReadBackFails(t *testing.T) {
	harness := newMovieChartHarness(t, &fakeMovieChartSource{})
	row := models.MovieChartEntry{DoubanID: "8801", Year: 2026, DetailStatus: models.MovieChartDetailPending}
	if err := harness.db.Create(&row).Error; err != nil {
		t.Fatalf("准备条目失败: %v", err)
	}

	harness.hookQuery("test:fail_entry_read_back", func(tx *gorm.DB) {
		tx.AddError(errors.New("测试注入：读回失败"))
	})
	_, _, claimed, err := harness.service.claimEntryDetail(context.Background(), row.ID)
	if err == nil {
		t.Fatal("读回失败应当上抛错误")
	}
	if claimed {
		t.Fatal("读不回来就不算认领成功")
	}
	// 摘掉钩子才能把行读出来看。
	if err := harness.db.Callback().Query().Remove("test:fail_entry_read_back"); err != nil {
		t.Fatalf("摘钩子失败: %v", err)
	}

	after := harness.entry("8801")
	if after.DetailStatus != models.MovieChartDetailPending || after.DetailClaim != "" {
		t.Fatalf("认领没退回去，这一行卡在了 %q（claim=%q）", after.DetailStatus, after.DetailClaim)
	}
}

// 读回来的行不是本次认领时，**不再往外发请求**。
//
// 触发点用更新钩子造：认领 UPDATE 刚落地就把这一行改走（用户点了刷新）。没有这道
// 复核，worker 会拿着一份过期的行照常出网，白跑一次请求，结果最后还是被写回守卫丢掉。
func TestMovieChartClaimRejectsRowChangedBeforeReadBack(t *testing.T) {
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			return movieChartFakePage("8901"), nil
		},
	}
	harness := newMovieChartHarness(t, source)
	if err := harness.service.runListPhase(context.Background(), 2026, source); err != nil {
		t.Fatalf("列表阶段失败: %v", err)
	}
	var once sync.Once
	harness.hookUpdate("test:steal_after_claim", "after", func(tx *gorm.DB) {
		once.Do(func() {
			// 走 Exec（Raw 链）而不是 Updates，免得再次落进本钩子。
			if err := harness.db.Exec(
				"UPDATE movie_chart_entries SET detail_status = ?, detail_claim = ? WHERE douban_id = ?",
				models.MovieChartDetailPending, "", "8901").Error; err != nil {
				t.Errorf("模拟认领后被改走失败: %v", err)
			}
		})
	})
	source.reset()
	harness.service.runDetailPhase(context.Background(), 2026, source, &movieChartRequestPacer{})

	_, detailCalls := source.calls()
	if len(detailCalls) != 0 {
		t.Fatalf("读回的行已经不是本次认领，不该再出网，实际请求 %v", detailCalls)
	}
}

// 取消落在「取完待办清单」与「认领第一条」之间时，循环顶上那道 ctx 检查是唯一的出口。
//
// 其余取消点都被 Detail 与限速之后的分支接住了，所以这条时序只能注入。观察点必须是
// **发没发语句**：取消之后发出去的 UPDATE 一律会失败，库的最终状态跟没发一模一样。
// 没有这道检查，一次取消会让 worker 空转完整年约 1500 条待办，每条发一次注定失败的
// 认领 UPDATE。
func TestMovieChartDetailLoopStopsIssuingClaimsAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			return movieChartFakePage("9201", "9202", "9203"), nil
		},
	}
	harness := newMovieChartHarness(t, source)
	if err := harness.service.runListPhase(context.Background(), 2026, source); err != nil {
		t.Fatalf("列表阶段失败: %v", err)
	}

	var canceled, updates atomic.Int64
	harness.hookQuery("test:cancel_after_pending_listing", func(tx *gorm.DB) {
		// 第一条查询就是取待办清单。
		if canceled.CompareAndSwap(0, 1) {
			cancel()
		}
	})
	harness.hookUpdate("test:count_updates_after_cancel", "before", func(tx *gorm.DB) {
		if canceled.Load() == 1 {
			updates.Add(1)
		}
	})
	source.reset()
	harness.service.runDetailPhase(ctx, 2026, source, &movieChartRequestPacer{})

	if got := updates.Load(); got != 0 {
		t.Fatalf("取消之后不该再对 movie_chart_entries 发任何 UPDATE，实际发了 %d 条", got)
	}
	_, detailCalls := source.calls()
	if len(detailCalls) != 0 {
		t.Fatalf("取消之后不该再出网，实际请求 %v", detailCalls)
	}
	for _, id := range []string{"9201", "9202", "9203"} {
		if got := harness.entry(id).DetailStatus; got != models.MovieChartDetailPending {
			t.Fatalf("%s 状态 = %q，取消后应原样留在 pending", id, got)
		}
	}
}

// 进程在补全途中被杀会留下 running 的残留行。下一轮列表阶段成功后把它放回 pending，
// 否则那一条永远不会再被补全。
func TestMovieChartInterruptedRunningRowIsResumedOnNextRefresh(t *testing.T) {
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			return movieChartFakePage("9101"), nil
		},
	}
	harness := newMovieChartHarness(t, source)
	if err := harness.service.runListPhase(context.Background(), 2026, source); err != nil {
		t.Fatalf("列表阶段失败: %v", err)
	}
	if err := harness.db.Model(&models.MovieChartEntry{}).
		Where("douban_id = ?", "9101").
		Updates(map[string]any{
			"detail_status": models.MovieChartDetailRunning,
			"detail_claim":  "11111111111111111111111111111111",
		}).Error; err != nil {
		t.Fatalf("制造残留失败: %v", err)
	}
	source.reset()
	if err := harness.service.refreshYear(context.Background(), 2026); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	_, detailCalls := source.calls()
	if len(detailCalls) != 1 || detailCalls[0] != "9101" {
		t.Fatalf("崩溃残留的 running 行应被放回队列，实际详情请求 %v", detailCalls)
	}
	if got := harness.entry("9101").DetailStatus; got != models.MovieChartDetailSucceeded {
		t.Fatalf("残留行补全后状态 = %q", got)
	}
}

// TestMovieChartYearStateSurvivesCanceledRound 钉住三条落定路径都不随本轮 ctx
// 取消（P-005 评审补齐，理由见 writeYearState 的注释）。
//
// 这条**不靠时序**：真实世界里的触发条件是「取消恰好落在事实发生之后、写入之前」，
// 那个窗口造不出确定性；但不变量本身与窗口无关——落定写入的正确性不该依赖本轮
// ctx 还活着。于是用例直接拿一个**已经取消**的 ctx 去调这三条路径，等价于那个窗口
// 的最坏情形，而且每次都一样。
func TestMovieChartYearStateSurvivesCanceledRound(t *testing.T) {
	cases := []struct {
		name   string
		settle func(harness *movieChartHarness, ctx context.Context)
		verify func(t *testing.T, state models.MovieChartYearState)
	}{
		{
			name: "失败",
			settle: func(harness *movieChartHarness, ctx context.Context) {
				harness.service.settleRefreshFailure(ctx, 2026,
					WatchlistMetadataFailureProxyUnreachable, errors.New("代理地址无效"))
			},
			verify: func(t *testing.T, state models.MovieChartYearState) {
				// 失败是已经发生过的事实，取消只是「别再抓了」，不是「忘掉它」。
				if state.LastFailure != string(WatchlistMetadataFailureProxyUnreachable) {
					t.Fatalf("失败分类码 = %q，取消不该把它吞掉", state.LastFailure)
				}
				if state.LastAttemptAt == nil {
					t.Fatal("失败也要记一次尝试时间")
				}
				if state.LastRefreshedAt != nil {
					t.Fatal("失败不得推后「上次成功刷新」")
				}
			},
		},
		{
			name: "取消",
			settle: func(harness *movieChartHarness, ctx context.Context) {
				harness.service.settleRefreshCanceled(ctx, 2026)
			},
			verify: func(t *testing.T, state models.MovieChartYearState) {
				if state.LastAttemptAt == nil {
					t.Fatal("取消也要记一次尝试时间")
				}
				if state.LastFailure != "" {
					t.Fatalf("取消不是失败，不该写分类码: %q", state.LastFailure)
				}
				if state.LastRefreshedAt != nil {
					t.Fatal("取消不得写「上次成功刷新」")
				}
			},
		},
		{
			name: "成功",
			settle: func(harness *movieChartHarness, ctx context.Context) {
				harness.service.settleRefreshSuccess(ctx, 2026)
			},
			verify: func(t *testing.T, state models.MovieChartYearState) {
				// 列表阶段已经整轮跑完了，取消晚到一步不该让这一年看起来从没抓过。
				if state.LastRefreshedAt == nil {
					t.Fatal("成功要写「上次成功刷新」")
				}
				if state.LastFailure != "" {
					t.Fatalf("成功要清空失败分类码: %q", state.LastFailure)
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newMovieChartHarness(t, &fakeMovieChartSource{})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			testCase.settle(harness, ctx)

			// yearState 查不到行会直接 Fatalf——「这一行压根没落库」正是要挡的那种失败。
			testCase.verify(t, harness.yearState(2026))
		})
	}
}

// ---------- 后台任务的启停 ----------

// 已经在跑时再点刷新：**返回错误，不排队、不并发**（需求设计文档 §7 末行）。
func TestMovieChartStartRefreshRejectsSecondRound(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			once.Do(func() { close(entered) })
			<-release
			return movieChartFakePage(), nil
		},
	}
	harness := newMovieChartHarness(t, source)
	if err := harness.service.StartRefresh(2026); err != nil {
		t.Fatalf("第一轮应当起得来: %v", err)
	}
	<-entered
	if _, running := harness.service.RefreshStatus(); !running {
		t.Fatal("刷新中时 RefreshStatus 应报 running")
	}
	err := harness.service.StartRefresh(2026)
	if err == nil {
		t.Fatal("第二轮应被拒绝")
	}
	if !strings.Contains(err.Error(), "正在刷新") {
		t.Fatalf("拒绝文案未指明原因: %v", err)
	}

	close(release)
	harness.service.StopRefreshAndWait()
	if _, running := harness.service.RefreshStatus(); running {
		t.Fatal("结束后不该还报 running")
	}
	// 上一轮结束后状态必须真的释放，否则刷新按钮再也点不动。
	if err := harness.service.StartRefresh(2026); err != nil {
		t.Fatalf("上一轮结束后应当能再起一轮: %v", err)
	}
	harness.service.StopRefreshAndWait()
}

// movieChartStopWindow 是「取消之后能不能挤进来一轮」的观察窗口，
// movieChartStopLateRound 是那一轮假想的时长。
//
// 方向是安全的：修好之后那个 goroutine **根本挤不进来**（它卡在 s.mu 上），
// StopRefreshAndWait 至多在窗口上耗掉这么久就返回；没修的话它必然挤得进来，
// 于是要等满 movieChartStopLateRound。两者差一个数量级，极端负载下最坏是变异
// 假绿，不是用例假红。
const (
	movieChartStopWindow    = 300 * time.Millisecond
	movieChartStopLateRound = 3 * time.Second
)

// TestMovieChartStopRefreshAndWaitIsAtomic 钉住 P-005 对 StopRefreshAndWait 的修复：
// 取消与「取出要等的那个通道」必须在同一次持锁里完成。
//
// 分成两次持锁时，两次之间恰好起来的那一轮会被**等待却没有被取消**——退出会卡到
// 它自己跑完为止，而一轮详情是约 1500 次限速请求。这条不变量在库里、在最终状态里
// 都留不下任何痕迹，只能把那个交错摆出来看：用例直接摆布刷新状态字段（同包可见），
// 让「取消」这一步的瞬间有另一个 goroutine 试着换上新一轮。
func TestMovieChartStopRefreshAndWaitIsAtomic(t *testing.T) {
	harness := newMovieChartHarness(t, &fakeMovieChartSource{})
	service := harness.service

	current := make(chan struct{})
	close(current) // 当前这一轮已经收摊，取消它之后应当立刻返回

	late := make(chan struct{})
	timer := time.AfterFunc(movieChartStopLateRound, func() { close(late) })
	defer timer.Stop()

	swapped := make(chan struct{})
	canceled := make(chan struct{})
	service.mu.Lock()
	service.refreshDone = current
	service.refreshCancel = func() {
		close(canceled)
		go func() {
			// 这一步能不能落在「取消与取通道之间」，完全取决于此刻
			// StopRefreshAndWait 是否还攥着 s.mu。
			service.mu.Lock()
			service.refreshDone = late
			service.mu.Unlock()
			close(swapped)
		}()
		select {
		case <-swapped:
		case <-time.After(movieChartStopWindow):
		}
	}
	service.mu.Unlock()

	started := time.Now()
	service.StopRefreshAndWait()
	elapsed := time.Since(started)

	select {
	case <-canceled:
	default:
		t.Fatal("StopRefreshAndWait 必须取消当前这一轮")
	}
	if elapsed >= movieChartStopLateRound {
		t.Fatalf("等了 %v：等的是取消之后才起来的那一轮，而它没有被取消", elapsed)
	}

	<-swapped
	service.mu.Lock()
	service.refreshDone, service.refreshCancel = nil, nil
	service.mu.Unlock()
}

// D-MC12 的刷新时机：当年看「距上次**成功**刷新 ≥30 天」，其余年份只在「从未抓过
// 且本地没有缓存」时抓一次。
func TestMovieChartEnsureYearRefreshedFollowsSchedule(t *testing.T) {
	recent := movieChartTestNow.Add(-29 * 24 * time.Hour)
	stale := movieChartTestNow.Add(-31 * 24 * time.Hour)
	attempted := movieChartTestNow.Add(-time.Hour)

	cases := []struct {
		name    string
		year    int
		state   *models.MovieChartYearState
		entries []string
		want    bool
	}{
		{name: "当年从未抓过：到期", year: 2026, want: true},
		{
			name:  "当年 29 天前成功过：未到期",
			year:  2026,
			state: &models.MovieChartYearState{Year: 2026, LastRefreshedAt: &recent, LastAttemptAt: &recent},
			want:  false,
		},
		{
			name:  "当年 31 天前成功过：到期",
			year:  2026,
			state: &models.MovieChartYearState{Year: 2026, LastRefreshedAt: &stale, LastAttemptAt: &stale},
			want:  true,
		},
		{
			// 上一轮失败过就不再自动出网：源持续不可用时，「从未成功」会让每打开一次
			// 页面就发约 100 次列表请求。页面照常显示失败原因与刷新按钮，手动随时能刷。
			name:  "当年只失败过、从未成功：不自动重抓",
			year:  2026,
			state: &models.MovieChartYearState{Year: 2026, LastAttemptAt: &attempted, LastFailure: "source_error"},
			want:  false,
		},
		{
			name: "当年 31 天前成功过、但上一轮失败了：不自动重抓",
			year: 2026,
			state: &models.MovieChartYearState{
				Year: 2026, LastRefreshedAt: &stale, LastAttemptAt: &attempted,
				LastFailure: "network_unreachable",
			},
			want: false,
		},
		{name: "往年本地无缓存且从未抓过：抓一次", year: 2019, want: true},
		{name: "往年本地有缓存：不抓", year: 2019, entries: []string{"1234567"}, want: false},
		{
			name:  "往年抓过但没抓到数据：不再自动出网",
			year:  2019,
			state: &models.MovieChartYearState{Year: 2019, LastAttemptAt: &attempted, LastFailure: "network_unreachable"},
			want:  false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := &fakeMovieChartSource{
				page: func(req movieChartPageRequest) (MovieChartListPage, error) {
					return movieChartFakePage(), nil
				},
			}
			harness := newMovieChartHarness(t, source)
			if testCase.state != nil {
				if err := harness.db.Create(testCase.state).Error; err != nil {
					t.Fatalf("准备年状态失败: %v", err)
				}
			}
			for _, id := range testCase.entries {
				if err := harness.db.Create(&models.MovieChartEntry{
					DoubanID: id, Year: testCase.year, DetailStatus: models.MovieChartDetailPending,
				}).Error; err != nil {
					t.Fatalf("准备缓存条目失败: %v", err)
				}
			}
			started, err := harness.service.EnsureYearRefreshed(testCase.year)
			if err != nil {
				t.Fatalf("判定刷新时机失败: %v", err)
			}
			if started != testCase.want {
				t.Fatalf("是否起刷新 = %v，期望 %v", started, testCase.want)
			}
			harness.service.StopRefreshAndWait()
		})
	}
}

// 已经有一轮在跑时，打开页面的自动路径**不报错**、也不排队，只是不起第二轮。
func TestMovieChartEnsureYearRefreshedSkipsWhileRunning(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	entered := make(chan struct{})
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			once.Do(func() { close(entered) })
			<-release
			return movieChartFakePage(), nil
		},
	}
	harness := newMovieChartHarness(t, source)
	if err := harness.service.StartRefresh(2026); err != nil {
		t.Fatalf("第一轮应当起得来: %v", err)
	}
	<-entered
	started, err := harness.service.EnsureYearRefreshed(2026)
	if err != nil {
		t.Fatalf("自动路径不该把「正在刷新」当成错误: %v", err)
	}
	if started {
		t.Fatal("已经在跑时不该再起一轮")
	}
	close(release)
	harness.service.StopRefreshAndWait()
}

// ---------- 海报阶段（D-MC14） ----------

// waitRoundFinished 等当前这一轮**自然跑完**，不取消它。
//
// 与 StopRefreshAndWait 的区别正是本节要的：那个会先取消，用它来等就永远看不到
// 一轮完整跑完的结果。
func (h *movieChartHarness) waitRoundFinished() {
	h.t.Helper()
	h.service.mu.Lock()
	done := h.service.refreshDone
	h.service.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(movieChartHarnessStopBudget):
		h.t.Fatalf("后台轮次没有在 %v 内结束", movieChartHarnessStopBudget)
	}
}

// seedChartEntry 按给定字段写一条缓存条目，未指定的取一组可用默认值。
func (h *movieChartPosterHarness) seedChartEntry(entry models.MovieChartEntry) models.MovieChartEntry {
	h.t.Helper()
	if entry.Year == 0 {
		entry.Year = 2026
	}
	if entry.Title == "" {
		entry.Title = "片" + entry.DoubanID
	}
	if entry.DetailStatus == "" {
		entry.DetailStatus = models.MovieChartDetailSucceeded
	}
	// ReleaseScope **不给默认值**：空串是「详情还没补全」这个真实状态，补图的取件
	// 条件必须照收（它只排除 excluded），默认成 theatrical 会把这一面遮住。
	if err := h.db.Create(&entry).Error; err != nil {
		h.t.Fatalf("写入条目 %s 失败: %v", entry.DoubanID, err)
	}
	return entry
}

// seedWatchedMark 直接写一条「已看」标记，供 excluded 但被标记过的用例用。
func (h *movieChartPosterHarness) seedWatchedMark(doubanID string, releaseYear int) {
	h.t.Helper()
	row := models.MovieChartMark{
		DoubanID:    doubanID,
		Mark:        models.MovieChartMarkWatched,
		ReleaseYear: releaseYear,
		Title:       "片" + doubanID,
		MarkedAt:    movieChartTestNow,
	}
	if err := h.db.Create(&row).Error; err != nil {
		h.t.Fatalf("写入标记 %s 失败: %v", doubanID, err)
	}
}

func movieChartPosterAddress(doubanID string) string {
	return "https://img2.doubanio.com/view/photo/l/public/" + doubanID + ".jpg"
}

// TestMovieChartPosterPhaseCoversEveryMissingPoster 钉住补图的取件条件：
// **poster_url 非空、poster_path 为空**，与 detail_status 无关。
//
// 三种「缺图」因此是同一件事，都被这一条捞起来：从未下过、上次下失败、被 LRU
// 淘汰；2026-09-22 之前落库、detail_status 早已是 succeeded 的存量条目（8001）
// 正是用户机器上的那一批，不必清任何东西就会被补上。
//
// 反面同样钉死：已经有图的不重下（8006），没有远程地址的不下（8005），
// 判定为不在内地院线上映的不下（8004——它在榜单页与已看页都永远不渲染，
// 给它花一次限速请求没有任何人会看到），别的年份不下（8007）。
func TestMovieChartPosterPhaseCoversEveryMissingPoster(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, movieChartJPEGHandler('p', 64))
	// 8001 是存量条目的形态：详情早就补全了，只是那时候海报不落盘。
	harness.seedChartEntry(models.MovieChartEntry{
		DoubanID: "8001", PosterURL: movieChartPosterAddress("8001"),
		ReleaseScope: models.MovieChartScopeTheatrical,
	})
	harness.seedChartEntry(models.MovieChartEntry{
		DoubanID: "8002", PosterURL: movieChartPosterAddress("8002"),
		ReleaseScope: models.MovieChartScopeUndetermined,
		DetailStatus: models.MovieChartDetailFailed,
		DetailError:  string(WatchlistMetadataFailureNetworkUnreachable),
	})
	// 8003 的 release_scope 是空串：详情还没补全，照样该有图（榜单里它以
	// 「上映信息待确认」出现，是看得见的卡片）。
	harness.seedChartEntry(models.MovieChartEntry{
		DoubanID: "8003", PosterURL: movieChartPosterAddress("8003"),
		DetailStatus: models.MovieChartDetailPending,
	})
	// 8004 判定为不在内地院线上映、又**没有任何标记**：两个页面都不会渲染它，
	// 不下。
	harness.seedChartEntry(models.MovieChartEntry{
		DoubanID: "8004", PosterURL: movieChartPosterAddress("8004"),
		ReleaseScope: models.MovieChartScopeExcluded,
	})
	// 8008 是同样的 excluded，但用户**标过已看**——已看页读的是标记表，一个字的
	// release_scope 过滤都没有，所以这张卡片会渲染，必须有图。
	harness.seedChartEntry(models.MovieChartEntry{
		DoubanID: "8008", PosterURL: movieChartPosterAddress("8008"),
		ReleaseScope: models.MovieChartScopeExcluded,
	})
	harness.seedWatchedMark("8008", 2026)
	harness.seedChartEntry(models.MovieChartEntry{
		DoubanID: "8005", ReleaseScope: models.MovieChartScopeTheatrical,
	})
	harness.seedChartEntry(models.MovieChartEntry{
		DoubanID: "8006", PosterURL: movieChartPosterAddress("8006"),
		ReleaseScope: models.MovieChartScopeTheatrical,
		PosterPath:   "movie_chart/6/already.jpg",
	})
	harness.seedChartEntry(models.MovieChartEntry{
		DoubanID: "8007", Year: 2025, PosterURL: movieChartPosterAddress("8007"),
	})

	harness.service.runPosterPhase(context.Background(), 2026, &movieChartRequestPacer{})

	for _, id := range []string{"8001", "8002", "8003", "8008"} {
		if got := harness.entry(id).PosterPath; got == "" {
			t.Fatalf("%s 缺图且有远程地址，必须被补上（与 detail_status 无关）", id)
		}
	}
	for _, id := range []string{"8004", "8005", "8007"} {
		if got := harness.entry(id).PosterPath; got != "" {
			t.Fatalf("%s 不该被补图，实际 %q", id, got)
		}
	}
	if got := harness.entry("8006").PosterPath; got != "movie_chart/6/already.jpg" {
		t.Fatalf("已经有图的条目不该重下，实际 %q", got)
	}
	if seen := harness.stub.seen(); len(seen) != 4 {
		t.Fatalf("应当只发四次取图请求，实际 %d 次: %v", len(seen), seen)
	}

	// 补图**不碰详情状态族**：条目的文字内容是完整的，只是没有图。
	if got := harness.entry("8002"); got.DetailStatus != models.MovieChartDetailFailed ||
		got.DetailError != string(WatchlistMetadataFailureNetworkUnreachable) {
		t.Fatalf("补图不得改写详情状态族: status=%q error=%q", got.DetailStatus, got.DetailError)
	}
	if got := harness.entry("8003").DetailStatus; got != models.MovieChartDetailPending {
		t.Fatalf("补图不得改写详情状态: %q", got)
	}
}

// TestMovieChartPosterFailureStaysQuiet 钉住失败的处置：403 是用户机器 2026-09-22
// 真实收到的响应。
//
// 四条一起钉：poster_path 留空（下一轮还会再试）、**不把条目标成详情失败**、
// 本轮**不重试**、后面的条目照常继续。
func TestMovieChartPosterFailureStaysQuiet(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "8101") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(movieChartJPEGBytes('q', 64))
	})
	harness.seedChartEntry(models.MovieChartEntry{DoubanID: "8101", PosterURL: movieChartPosterAddress("8101")})
	harness.seedChartEntry(models.MovieChartEntry{DoubanID: "8102", PosterURL: movieChartPosterAddress("8102")})

	harness.service.runPosterPhase(context.Background(), 2026, &movieChartRequestPacer{})

	failed := harness.entry("8101")
	if failed.PosterPath != "" {
		t.Fatalf("下载失败不得写 poster_path: %q", failed.PosterPath)
	}
	if failed.DetailStatus != models.MovieChartDetailSucceeded || failed.DetailError != "" {
		t.Fatalf("海报失败不该让条目变成详情失败: status=%q error=%q", failed.DetailStatus, failed.DetailError)
	}
	if got := harness.entry("8102").PosterPath; got == "" {
		t.Fatal("一条失败不得让后面的条目跟着不下")
	}
	seen := harness.stub.seen()
	if len(seen) != 2 {
		t.Fatalf("每条一轮最多试一次，实际发了 %d 次: %v", len(seen), seen)
	}
	if files := harness.posterFiles(); len(files) != 1 {
		t.Fatalf("只有成功的那一张该落盘，实际 %v", files)
	}
}

// TestMovieChartPosterPhaseSharesRateLimitWithDetails 钉住海报请求与详情请求共用
// 同一条 1 次/秒的节流（D-MC06）：各记各的话实际速率就是 2 次/秒。
func TestMovieChartPosterPhaseSharesRateLimitWithDetails(t *testing.T) {
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			if req.Sort == MovieChartSortComprehensive && req.Start == 0 {
				return movieChartFakePage("9201", "9202"), nil
			}
			return MovieChartListPage{}, nil
		},
	}
	harness := newMovieChartPosterHarness(t, source, movieChartJPEGHandler('r', 64))

	if err := harness.service.refreshYear(context.Background(), 2026); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}

	_, detailCalls := source.calls()
	if len(detailCalls) != 2 {
		t.Fatalf("详情请求 %d 次，期望 2 次", len(detailCalls))
	}
	if seen := harness.stub.seen(); len(seen) != 2 {
		t.Fatalf("取图请求 %d 次，期望 2 次: %v", len(seen), seen)
	}
	// 2 次详情 + 2 次取图 = 4 次出网，之间等 3 次。
	if got := harness.sleepCount(); got != 3 {
		t.Fatalf("限速等待 %d 次，期望 3 次（4 次出网之间）", got)
	}
	for _, d := range harness.sleeps {
		if d != movieChartDetailInterval {
			t.Fatalf("限速间隔 = %v，期望 %v", d, movieChartDetailInterval)
		}
	}
	for _, id := range []string{"9201", "9202"} {
		if got := harness.entry(id).PosterPath; got == "" {
			t.Fatalf("%s 的海报应当随这一轮落盘", id)
		}
	}
}

// TestMovieChartPosterPhaseResumesAfterCancel 钉住取消与断点续跑：取消时已经落盘的
// 保留，没轮到的下一轮接着补。
//
// 取消点做成确定的——注入的 sleep 在第一次限速等待时取消：那一刻第一条已经下完、
// 第二条还没发请求。
func TestMovieChartPosterPhaseResumesAfterCancel(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, movieChartJPEGHandler('s', 64))
	harness.seedChartEntry(models.MovieChartEntry{DoubanID: "9301", PosterURL: movieChartPosterAddress("9301")})
	harness.seedChartEntry(models.MovieChartEntry{DoubanID: "9302", PosterURL: movieChartPosterAddress("9302")})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := harness.service.sleep
	harness.service.sleep = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}
	harness.service.runPosterPhase(ctx, 2026, &movieChartRequestPacer{})
	harness.service.sleep = original

	if got := harness.entry("9301").PosterPath; got == "" {
		t.Fatal("取消前已经落盘的那一张必须保留")
	}
	if got := harness.entry("9302").PosterPath; got != "" {
		t.Fatalf("取消之后不该再补图: %q", got)
	}
	if seen := harness.stub.seen(); len(seen) != 1 {
		t.Fatalf("取消后不得再发请求，实际 %v", seen)
	}

	// 取消之后**不再自动**补：打开页面是补图的触发点，不挡住的话切个年份再切回来
	// 就把这一轮原样重启了，取消按钮形同虚设。
	//
	// 先把这一年记成刚刚成功刷新过，让整轮刷新不到期——否则 EnsureYearRefreshed
	// 走的是 D-MC12 的那一支，测的就不是补图这条路径了。
	fresh := movieChartTestNow
	if err := harness.db.Create(&models.MovieChartYearState{
		Year: 2026, LastRefreshedAt: &fresh, LastAttemptAt: &fresh,
	}).Error; err != nil {
		t.Fatalf("准备年状态失败: %v", err)
	}
	started, err := harness.service.EnsureYearRefreshed(2026)
	if err != nil {
		t.Fatalf("判定刷新时机失败: %v", err)
	}
	if started {
		t.Fatal("用户取消之后，打开页面不该自动把补图重启")
	}

	// 手动重来照常：下一轮从没补到的那一条接着跑，续跑靠的就是「poster_path 为空」
	// 这个条件本身。
	harness.service.runPosterPhase(context.Background(), 2026, &movieChartRequestPacer{})
	if got := harness.entry("9302").PosterPath; got == "" {
		t.Fatal("下一轮应当把剩下的补上")
	}
}

// TestMovieChartEnsureYearRefreshedStartsPosterOnlyPass 钉住 D-MC14 加的第三条
// 刷新时机：**整轮刷新不到期、但这一年还有条目缺图时，起一轮只补海报的**。
//
// 没有它，往年的缺图永远补不回来——refreshDue 一旦看到 last_attempt_at 就恒为
// false，而界面上没有「只补图」的按钮。用例用的正是这个前提：2019 年已经抓过，
// 整轮刷新不到期，适配器是陷阱（一次列表或详情请求都不许发）。
//
// 把 EnsureYearRefreshed 里的 ensureYearPosters 那一支删掉，本用例立刻红：
// started 变成 false，poster_path 一个都不会填。
func TestMovieChartEnsureYearRefreshedStartsPosterOnlyPass(t *testing.T) {
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			t.Errorf("只补海报的一轮不得发列表请求: %+v", req)
			return MovieChartListPage{}, nil
		},
		detail: func(ctx context.Context, doubanID string) (*MovieChartDetail, error) {
			t.Errorf("只补海报的一轮不得发详情请求: %s", doubanID)
			return nil, errors.New("不该发详情请求")
		},
	}
	harness := newMovieChartPosterHarness(t, source, movieChartJPEGHandler('t', 64))
	attempted := movieChartTestNow.Add(-365 * 24 * time.Hour)
	if err := harness.db.Create(&models.MovieChartYearState{
		Year: 2019, LastRefreshedAt: &attempted, LastAttemptAt: &attempted,
	}).Error; err != nil {
		t.Fatalf("准备年状态失败: %v", err)
	}
	harness.seedChartEntry(models.MovieChartEntry{DoubanID: "9401", Year: 2019, PosterURL: movieChartPosterAddress("9401")})
	harness.seedChartEntry(models.MovieChartEntry{DoubanID: "9402", Year: 2019, PosterURL: movieChartPosterAddress("9402")})

	started, err := harness.service.EnsureYearRefreshed(2019)
	if err != nil {
		t.Fatalf("判定刷新时机失败: %v", err)
	}
	if !started {
		t.Fatal("往年缺图时应当起一轮只补海报的")
	}
	harness.waitRoundFinished()

	for _, id := range []string{"9401", "9402"} {
		if got := harness.entry(id).PosterPath; got == "" {
			t.Fatalf("%s 的海报应当被补上", id)
		}
	}
	listCalls, detailCalls := source.calls()
	if len(listCalls) != 0 || len(detailCalls) != 0 {
		t.Fatalf("只补海报的一轮不得出网抓列表或详情: list=%v detail=%v", listCalls, detailCalls)
	}
	if !harness.taskSeen(BackgroundTaskMovieChart) {
		t.Fatal("只补海报的一轮也要进后台任务登记表，否则用户看不到、也取消不了")
	}
	// 补完之后没有缺图了，再打开页面不该再起一轮。
	started, err = harness.service.EnsureYearRefreshed(2019)
	if err != nil {
		t.Fatalf("判定刷新时机失败: %v", err)
	}
	if started {
		t.Fatal("没有缺图时不该起任何一轮")
	}
}

// TestMovieChartEnsureYearRefreshedStopsPosterPassAfterTotalFailure 钉住自动路径的
// 自我约束：一轮海报**一张都没成功**之后，打开页面不再自动重来一轮。
//
// 口径与 D-MC12「上一次尝试失败就不再自动出网」一致。用户机器此刻正被豆瓣限流，
// 没有这一条，每打开一次榜单页就是从头再打一遍约 1500 张图。
func TestMovieChartEnsureYearRefreshedStopsPosterPassAfterTotalFailure(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	attempted := movieChartTestNow.Add(-365 * 24 * time.Hour)
	if err := harness.db.Create(&models.MovieChartYearState{
		Year: 2019, LastRefreshedAt: &attempted, LastAttemptAt: &attempted,
	}).Error; err != nil {
		t.Fatalf("准备年状态失败: %v", err)
	}
	harness.seedChartEntry(models.MovieChartEntry{DoubanID: "9501", Year: 2019, PosterURL: movieChartPosterAddress("9501")})

	started, err := harness.service.EnsureYearRefreshed(2019)
	if err != nil {
		t.Fatalf("判定刷新时机失败: %v", err)
	}
	if !started {
		t.Fatal("第一次应当起一轮")
	}
	harness.waitRoundFinished()
	if got := harness.entry("9501").PosterPath; got != "" {
		t.Fatalf("全部失败时不该写 poster_path: %q", got)
	}

	started, err = harness.service.EnsureYearRefreshed(2019)
	if err != nil {
		t.Fatalf("判定刷新时机失败: %v", err)
	}
	if started {
		t.Fatal("上一轮一张都没成功之后，打开页面不该再自动补一轮")
	}
	if got := len(harness.stub.seen()); got != 1 {
		t.Fatalf("第二次打开页面不该再出网，累计请求 %d 次", got)
	}
}

// TestMovieChartEnsureYearRefreshedKeepsGoingAfterPartialSuccess 是上一条的另一面：
// 只要这一轮下成了一张，说明源是通的，下一次打开页面照常把剩下的接着补。
func TestMovieChartEnsureYearRefreshedKeepsGoingAfterPartialSuccess(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "9602") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(movieChartJPEGBytes('u', 64))
	})
	attempted := movieChartTestNow.Add(-365 * 24 * time.Hour)
	if err := harness.db.Create(&models.MovieChartYearState{
		Year: 2019, LastRefreshedAt: &attempted, LastAttemptAt: &attempted,
	}).Error; err != nil {
		t.Fatalf("准备年状态失败: %v", err)
	}
	harness.seedChartEntry(models.MovieChartEntry{DoubanID: "9601", Year: 2019, PosterURL: movieChartPosterAddress("9601")})
	harness.seedChartEntry(models.MovieChartEntry{DoubanID: "9602", Year: 2019, PosterURL: movieChartPosterAddress("9602")})

	started, err := harness.service.EnsureYearRefreshed(2019)
	if err != nil || !started {
		t.Fatalf("第一次应当起一轮: started=%v err=%v", started, err)
	}
	harness.waitRoundFinished()

	started, err = harness.service.EnsureYearRefreshed(2019)
	if err != nil {
		t.Fatalf("判定刷新时机失败: %v", err)
	}
	if !started {
		t.Fatal("上一轮下成过图，剩下的缺图应当继续补")
	}
	harness.waitRoundFinished()
	if got := len(harness.stub.seen()); got != 3 {
		t.Fatalf("累计请求 %d 次，期望 3 次（第一轮两条 + 第二轮重试失败的那一条）", got)
	}
}

// TestMovieChartPosterBacklogSkipsRowsWithoutRemoteAddress 钉住取件条件里的
// **poster_url 非空**这一项，而且钉的是**计数**那一侧。
//
// 下载侧那道地址校验挡得住请求（空地址过不了白名单），所以少了这一项，
// 「下载」与「落盘」两类断言都照样绿——空转发生在更上游：没有远程地址的行永远
// 拿不到 poster_path，于是永远留在 countPosterBacklog 里，打开一次页面就起一轮
// 补图，每行还记一条被拒的日志；只要同一年里有一张真下成了，「全军覆没」的抑制
// 也不会触发。这条用例直接断言计数为 0，并断言打开页面不起轮次。
func TestMovieChartPosterBacklogSkipsRowsWithoutRemoteAddress(t *testing.T) {
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			t.Errorf("不该起任何抓取: %+v", req)
			return MovieChartListPage{}, nil
		},
	}
	harness := newMovieChartPosterHarness(t, source, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("没有远程地址的条目不该发出取图请求: %s", r.URL)
	})
	attempted := movieChartTestNow.Add(-365 * 24 * time.Hour)
	if err := harness.db.Create(&models.MovieChartYearState{
		Year: 2019, LastRefreshedAt: &attempted, LastAttemptAt: &attempted,
	}).Error; err != nil {
		t.Fatalf("准备年状态失败: %v", err)
	}
	// 这一年只有「没有远程地址」与「excluded 且没被标记」两种永远补不了的行。
	harness.seedChartEntry(models.MovieChartEntry{
		DoubanID: "8301", Year: 2019, ReleaseScope: models.MovieChartScopeTheatrical,
	})
	harness.seedChartEntry(models.MovieChartEntry{
		DoubanID: "8302", Year: 2019, PosterURL: movieChartPosterAddress("8302"),
		ReleaseScope: models.MovieChartScopeExcluded,
	})

	backlog, err := harness.service.countPosterBacklog(2019)
	if err != nil {
		t.Fatalf("统计缺图条目失败: %v", err)
	}
	if backlog != 0 {
		t.Fatalf("永远补不了的行不得留在缺图计数里，实际 %d 条", backlog)
	}
	started, err := harness.service.EnsureYearRefreshed(2019)
	if err != nil {
		t.Fatalf("判定刷新时机失败: %v", err)
	}
	if started {
		t.Fatal("没有可补的图就不该起一轮——否则每打开一次页面就空转一轮")
	}
	// 再打开一次也一样：这条路径必须是稳定的空操作，不是靠某个一次性标记挡住的。
	if started, _ := harness.service.EnsureYearRefreshed(2019); started {
		t.Fatal("第二次打开页面同样不该起一轮")
	}
	if seen := harness.stub.seen(); len(seen) != 0 {
		t.Fatalf("不得发出任何取图请求，实际 %v", seen)
	}
}

// TestMovieChartPosterBacklogCoversMarkedExcludedEntries 单钉「excluded 但被标记过
// 的条目照样补图」这一条边界，两面都有：
//
// 判定为 excluded 的条目不进榜单页，但**已看页会渲染它**——ListWatched 读的是
// movie_chart_marks，没有任何 release_scope 过滤。真实路径是：条目以「上映信息
// 待确认」出现在榜单里、用户标了已看，随后的详情补全把它判成 excluded（剧集或
// 非内地院线）。把 excluded 一刀切排除，这张已看卡片就**永远**只有占位图。
func TestMovieChartPosterBacklogCoversMarkedExcludedEntries(t *testing.T) {
	harness := newMovieChartPosterHarness(t, &fakeMovieChartSource{}, movieChartJPEGHandler('w', 64))
	marked := harness.seedChartEntry(models.MovieChartEntry{
		DoubanID: "8401", PosterURL: movieChartPosterAddress("8401"),
		ReleaseScope: models.MovieChartScopeExcluded,
	})
	harness.seedWatchedMark("8401", 2026)
	harness.seedChartEntry(models.MovieChartEntry{
		DoubanID: "8402", PosterURL: movieChartPosterAddress("8402"),
		ReleaseScope: models.MovieChartScopeExcluded,
	})
	_ = marked

	harness.service.runPosterPhase(context.Background(), 2026, &movieChartRequestPacer{})

	if got := harness.entry("8401").PosterPath; got == "" {
		t.Fatal("标记过的 excluded 条目会在已看页渲染，必须有图")
	}
	if got := harness.entry("8402").PosterPath; got != "" {
		t.Fatalf("没有标记的 excluded 条目两个页面都不渲染，不该下载: %q", got)
	}
	if seen := harness.stub.seen(); len(seen) != 1 {
		t.Fatalf("只该为被标记的那一条取图，实际 %v", seen)
	}

	// 已看页据此报 HasPoster=true：这正是这条取件规则要保住的用户可见结果。
	groups, err := harness.service.ListWatched()
	if err != nil {
		t.Fatalf("读已看页失败: %v", err)
	}
	if len(groups) != 1 || len(groups[0].Items) != 1 {
		t.Fatalf("已看页应当只有这一条: %+v", groups)
	}
	if !groups[0].Items[0].HasPoster {
		t.Fatal("已看卡片必须报「有海报」，否则前端连请求都不会发，永远是占位图")
	}
}
