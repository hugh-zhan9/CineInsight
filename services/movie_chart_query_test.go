package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"video-master/internal/dbtest"
	"video-master/models"
)

// 本文件按需求设计文档 §6.1（接口形状）、§7（边界条件）与概要设计 §4.2（读取与
// 缓存回落）逐条验收读路径。每个用例注明它钉的是哪一条。
//
// 读路径的第一条不变量是**不出网**，所以夹具给的适配器一旦被调用就判用例失败：
// 这条断言不是某一个用例的，它挂在每一个用例上。

// movieChartQueryYear 是读路径用例的年份，与夹具时钟同年（movieChartTestNow 是
// 2026-09-21）。选当年而不是往年，是因为 D-MC12 对当年的自动刷新条件最宽，
// 「读一页顺手起了一轮抓取」这种意外最容易在这一年暴露。
const movieChartQueryYear = 2026

type movieChartQueryHarness struct {
	*movieChartHarness
	// source 是夹具注入的假适配器，用例回看调用记录用。
	source *fakeMovieChartSource
}

// newMovieChartQueryHarness 起读路径用的夹具。
//
// 适配器是一个**陷阱**：读路径调到它就说明这一次读要出网了，直接判失败。它跑在
// 后台 goroutine 上，所以用 Errorf 而不是 Fatalf。
//
// 夹具**故意不写任何年状态**：这样每个用例里的年份照 D-MC12 都算「到期」，读接口
// 一旦哪天又顺手起一轮抓取，随便哪个用例都会立刻撞上这个陷阱。P-005 初版正好相反
// ——夹具先把年份记成刷新过，把触发点遮住了，那层遮挡也就让「读接口有没有副作用」
// 变得测不出来。
func newMovieChartQueryHarness(t *testing.T) *movieChartQueryHarness {
	t.Helper()
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			t.Errorf("读路径不得出网，却发起了列表请求 %+v", req)
			return MovieChartListPage{}, nil
		},
		detail: func(ctx context.Context, doubanID string) (*MovieChartDetail, error) {
			t.Errorf("读路径不得出网，却发起了详情请求 %s", doubanID)
			return nil, errors.New("读路径不得出网")
		},
	}
	return &movieChartQueryHarness{movieChartHarness: newMovieChartHarness(t, source), source: source}
}

// markYearFresh 把某一年记成「刚刚成功刷新过」。读路径不看它，只有需要摆出
// 「这一年不到期」这个前提的用例才用得上。
func (h *movieChartQueryHarness) markYearFresh(year int) {
	h.t.Helper()
	at := movieChartTestNow
	h.seedYearState(models.MovieChartYearState{Year: year, LastRefreshedAt: &at, LastAttemptAt: &at})
}

func (h *movieChartQueryHarness) seedYearState(state models.MovieChartYearState) {
	h.t.Helper()
	if err := h.db.Create(&state).Error; err != nil {
		h.t.Fatalf("写入 %d 年状态失败: %v", state.Year, err)
	}
}

// seed 写一条缓存条目，未指定的列取一组可用的默认值。
func (h *movieChartQueryHarness) seed(entry models.MovieChartEntry) models.MovieChartEntry {
	h.t.Helper()
	if entry.Year == 0 {
		entry.Year = movieChartQueryYear
	}
	if entry.Title == "" {
		entry.Title = "片" + entry.DoubanID
	}
	if entry.DetailStatus == "" {
		entry.DetailStatus = models.MovieChartDetailSucceeded
	}
	if err := h.db.Create(&entry).Error; err != nil {
		h.t.Fatalf("写入缓存条目 %s 失败: %v", entry.DoubanID, err)
	}
	return entry
}

// seedMark 直接写标记行。读路径只读它，不必绕经 MarkEntry（那条路径由
// movie_chart_marks_test.go 拥有）。
func (h *movieChartQueryHarness) seedMark(doubanID, mark string, releaseYear int, markedAt time.Time) {
	h.t.Helper()
	h.seedMarkWithPoster(doubanID, mark, releaseYear, markedAt,
		"https://img2.doubanio.com/view/photo/l/public/"+doubanID+".jpg")
}

// seedMarkWithPoster 同上，但海报快照由调用方给——空串用来验「没有海报」那一面。
func (h *movieChartQueryHarness) seedMarkWithPoster(doubanID, mark string, releaseYear int, markedAt time.Time, posterURL string) {
	h.t.Helper()
	row := models.MovieChartMark{
		DoubanID:    doubanID,
		Mark:        mark,
		ReleaseYear: releaseYear,
		Title:       "片" + doubanID,
		PosterURL:   posterURL,
		MarkedAt:    markedAt,
	}
	if err := h.db.Create(&row).Error; err != nil {
		h.t.Fatalf("写入标记 %s 失败: %v", doubanID, err)
	}
}

// 关于「ORDER BY 末尾的 id 兜底」这条断言能不能测出来，P-005 实测过一轮，结论
// 记在这里，省得下一个人重走：
//
//   - 默认排序（CASE + release_date + id）：让两条同日期的条目相邻，去掉 id 兜底
//     在 **SQLite 与 Postgres 上都会翻车**，用例抓得住。
//   - 评分排序：同分的一批要**足够多**（本文件用 12 条）。Postgres 的排序不稳定，
//     去掉 id 兜底后同分那批的顺序就变了，用例抓得住；**SQLite 抓不住**——表按
//     rowid 组织，同分行扫出来恒为 id 顺序，有没有兜底没有可观测差别。这条兜底
//     因此只有 Postgres 腿证得动，这正是双后端跑同一套用例要买的东西。
//
// 走过的弯路：初版另写了一个 shuffleHeapOrder 辅助函数，先改无关列、后改索引列，
// 想把行在物理顺序上挪到末尾来放大差异。实测把它整个去掉，上面两行结论一个字都
// 不变——detection 来自 Postgres 对同分批次的不稳定排序，与堆序、索引序无关。
// 那个辅助函数因此删掉了，别再加回来。

func movieChartItemIDs(page *MovieChartPage) []string {
	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.DoubanID)
	}
	return ids
}

func movieChartAssertIDs(t *testing.T, page *MovieChartPage, want []string) {
	t.Helper()
	got := movieChartItemIDs(page)
	if len(got) != len(want) {
		t.Fatalf("条目顺序 = %v，期望 %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("条目顺序 = %v，期望 %v", got, want)
		}
	}
}

// ---------- 排序（D-MC10） ----------

// TestMovieChartListPageSortsOldestFirst 钉住默认排序：上映时间升序、未定档置底、
// 同日期按 id 兜底（D-MC10，表达式见需求设计文档 §4.1）。
//
// 三条断言合在一个用例里，因为它们是同一条 ORDER BY 的三段，拆开反而看不出
// 「未定档那一桶排在已定档之后」这层关系。
func TestMovieChartListPageSortsOldestFirst(t *testing.T) {
	h := newMovieChartQueryHarness(t)
	// 插入顺序即 id 顺序，且**故意与目标顺序不同**：照插入顺序返回的实现会当场翻车。
	h.seed(models.MovieChartEntry{DoubanID: "301", ReleaseScope: models.MovieChartScopeTheatrical, ReleaseDate: "2026-05-01"})
	h.seed(models.MovieChartEntry{DoubanID: "302", ReleaseScope: models.MovieChartScopeUndetermined})
	h.seed(models.MovieChartEntry{DoubanID: "303", ReleaseScope: models.MovieChartScopeTheatrical, ReleaseDate: "2026-01-15"})
	h.seed(models.MovieChartEntry{DoubanID: "304", ReleaseScope: models.MovieChartScopeTheatrical, ReleaseDate: "2026-05-01"})
	h.seed(models.MovieChartEntry{DoubanID: "305", DetailStatus: models.MovieChartDetailPending})

	page, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, 1, false)
	if err != nil {
		t.Fatalf("读榜单失败: %v", err)
	}
	movieChartAssertIDs(t, page, []string{"303", "301", "304", "302", "305"})
	if page.Sort != MovieChartOrderRelease || page.Year != movieChartQueryYear || page.Page != 1 {
		t.Fatalf("回显字段不对: %+v", page)
	}
	if page.PageSize != MovieChartPageSize {
		t.Fatalf("每页条数恒为 %d，实际 %d", MovieChartPageSize, page.PageSize)
	}
	if page.Total != 5 {
		t.Fatalf("总数 = %d，期望 5", page.Total)
	}
	// 详情还没补全的条目照常出现，且带着让界面标「上映信息待确认」的两个字段。
	last := page.Items[len(page.Items)-1]
	if last.ReleaseScope != "" || last.DetailStatus != models.MovieChartDetailPending {
		t.Fatalf("未补全条目要带上可供界面标注的状态: %+v", last)
	}
}

// TestMovieChartListPageSortsRatingDescending 钉住评分排序：降序，同分按 id 兜底，
// 没有评分的（0 分）落到最后，**详情未补全的条目不论评分多高一律置底**
// （需求设计文档 §3 的表：release_scope 为空串时「上映信息待确认」，排序置底）。
func TestMovieChartListPageSortsRatingDescending(t *testing.T) {
	h := newMovieChartQueryHarness(t)
	h.seed(models.MovieChartEntry{DoubanID: "402", ReleaseScope: models.MovieChartScopeTheatrical, Rating: 9.1, ReleaseDate: "2026-03-03"})
	// 一条「评分比谁都高、但详情还没补全」的条目：它还没按内地公映口径判过，
	// 详情落地后可能整条消失，而这个 9.9 是列表阶段的值。少了那一桶，它会顶到
	// 第一位——这正是本条断言要挡的。
	h.seed(models.MovieChartEntry{DoubanID: "499", Rating: 9.9, DetailStatus: models.MovieChartDetailPending})
	// 同分的一批要足够多：只放两条时两个后端都退化成稳定排序，把 ORDER BY 末尾的
	// id 删掉照样绿。12 条能让 Postgres 的排序真的把它们打乱（见文件上方那段说明）。
	tied := make([]string, 0, 12)
	for index := 0; index < 12; index++ {
		id := fmt.Sprintf("41%02d", index)
		tied = append(tied, id)
		h.seed(models.MovieChartEntry{DoubanID: id, ReleaseScope: models.MovieChartScopeTheatrical, Rating: 8.4, ReleaseDate: "2026-02-02"})
	}
	h.seed(models.MovieChartEntry{DoubanID: "403", ReleaseScope: models.MovieChartScopeUndetermined})

	page, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRating, 1, false)
	if err != nil {
		t.Fatalf("读榜单失败: %v", err)
	}
	// 顺序：已补全的按评分降序（同分按 id），未补全的最后——哪怕它是全场最高分。
	want := append([]string{"402"}, tied...)
	want = append(want, "403", "499")
	movieChartAssertIDs(t, page, want)
	if page.Items[len(page.Items)-1].DoubanID != "499" {
		t.Fatalf("详情未补全的条目必须置底，实际末位是 %s", page.Items[len(page.Items)-1].DoubanID)
	}
	if page.Sort != MovieChartOrderRating {
		t.Fatalf("排序回显 = %q", page.Sort)
	}
}

// ---------- 榜单内容（D-MC02 / D-MC04） ----------

// TestMovieChartListPageScopeAndYearFilter 钉住榜单的内容口径：
// theatrical / undetermined / 空串三种进榜，excluded **永不出现**，别的年份不串台。
func TestMovieChartListPageScopeAndYearFilter(t *testing.T) {
	h := newMovieChartQueryHarness(t)
	h.seed(models.MovieChartEntry{DoubanID: "501", ReleaseScope: models.MovieChartScopeTheatrical, ReleaseDate: "2026-01-01"})
	h.seed(models.MovieChartEntry{DoubanID: "502", ReleaseScope: models.MovieChartScopeUndetermined})
	h.seed(models.MovieChartEntry{DoubanID: "503", DetailStatus: models.MovieChartDetailPending})
	h.seed(models.MovieChartEntry{DoubanID: "504", ReleaseScope: models.MovieChartScopeExcluded, ReleaseDate: "2026-02-02"})
	h.seed(models.MovieChartEntry{DoubanID: "505", Year: 2025, ReleaseScope: models.MovieChartScopeTheatrical, ReleaseDate: "2025-02-02"})

	for _, showMarked := range []bool{false, true} {
		page, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, 1, showMarked)
		if err != nil {
			t.Fatalf("读榜单失败(showMarked=%v): %v", showMarked, err)
		}
		// 「不过滤」只解除标记造成的隐藏，**不**放宽内地公映口径：504 两边都不出现。
		movieChartAssertIDs(t, page, []string{"501", "502", "503"})
		if page.Total != 3 {
			t.Fatalf("总数 = %d，期望 3(showMarked=%v)", page.Total, showMarked)
		}
	}
}

// TestMovieChartListPageMarkFiltering 钉住标记造成的隐藏（概要设计 §4.3）：
// 不想看与已看默认隐藏、想看照常显示，「不过滤」打开后三种都在。
func TestMovieChartListPageMarkFiltering(t *testing.T) {
	h := newMovieChartQueryHarness(t)
	h.seed(models.MovieChartEntry{DoubanID: "601", ReleaseScope: models.MovieChartScopeTheatrical, ReleaseDate: "2026-01-01"})
	h.seed(models.MovieChartEntry{DoubanID: "602", ReleaseScope: models.MovieChartScopeTheatrical, ReleaseDate: "2026-02-01"})
	h.seed(models.MovieChartEntry{DoubanID: "603", ReleaseScope: models.MovieChartScopeTheatrical, ReleaseDate: "2026-03-01"})
	h.seed(models.MovieChartEntry{DoubanID: "604", ReleaseScope: models.MovieChartScopeTheatrical, ReleaseDate: "2026-04-01"})
	h.seedMark("602", models.MovieChartMarkWant, 2026, movieChartTestNow)
	h.seedMark("603", models.MovieChartMarkSkip, 2026, movieChartTestNow)
	h.seedMark("604", models.MovieChartMarkWatched, 2026, movieChartTestNow)

	page, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, 1, false)
	if err != nil {
		t.Fatalf("读榜单失败: %v", err)
	}
	movieChartAssertIDs(t, page, []string{"601", "602"})
	if page.Total != 2 {
		t.Fatalf("隐藏之后的总数 = %d，期望 2", page.Total)
	}
	// 标记必须回给前端，否则「想看」按钮回不到已选中的样子。
	if page.Items[0].Mark != "" || page.Items[1].Mark != models.MovieChartMarkWant {
		t.Fatalf("标记回显不对: %+v", page.Items)
	}

	all, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, 1, true)
	if err != nil {
		t.Fatalf("读榜单(不过滤)失败: %v", err)
	}
	movieChartAssertIDs(t, all, []string{"601", "602", "603", "604"})
	if all.Total != 4 {
		t.Fatalf("不过滤时的总数 = %d，期望 4", all.Total)
	}
	if all.Items[2].Mark != models.MovieChartMarkSkip || all.Items[3].Mark != models.MovieChartMarkWatched {
		t.Fatalf("不过滤时标记回显不对: %+v", all.Items)
	}
}

// TestMovieChartListPageItemFields 钉住卡片字段的取值，尤其是 HasPoster：
// 前端据它决定要不要请求海报路由，取错就是每张卡片一次必然 404 的往返。
//
// D-MC14 之后 HasPoster 看的是 **poster_path（已经落盘）**，不是 poster_url
// （豆瓣上有这张图）：路由改成纯本地读之后，只有远程地址的条目请求过去只会是 404。
// 703 就是这一条的反例——它有地址、没落盘，HasPoster 必须为假。
func TestMovieChartListPageItemFields(t *testing.T) {
	h := newMovieChartQueryHarness(t)
	h.seed(models.MovieChartEntry{
		DoubanID:      "701",
		Title:         "沙丘",
		OriginalTitle: "Dune",
		CardSubtitle:  "2026 / 美国 / 科幻 / 导演A",
		ReleaseScope:  models.MovieChartScopeTheatrical,
		ReleaseDate:   "2026-03-20",
		Rating:        8.2,
		RatingCount:   4200,
		PosterURL:     "https://img2.doubanio.com/view/photo/l/public/701.jpg",
		PosterPath:    "movie_chart/1/701.jpg",
		DetailError:   "",
	})
	h.seed(models.MovieChartEntry{
		DoubanID:     "702",
		ReleaseScope: models.MovieChartScopeUndetermined,
		DetailStatus: models.MovieChartDetailFailed,
		DetailError:  string(WatchlistMetadataFailureNetworkUnreachable),
	})
	h.seed(models.MovieChartEntry{
		DoubanID:     "703",
		ReleaseScope: models.MovieChartScopeTheatrical,
		ReleaseDate:  "2026-03-21",
		PosterURL:    "https://img2.doubanio.com/view/photo/l/public/703.jpg",
	})

	page, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, 1, false)
	if err != nil {
		t.Fatalf("读榜单失败: %v", err)
	}
	first := page.Items[0]
	want := MovieChartItemView{
		DoubanID:      "701",
		Title:         "沙丘",
		OriginalTitle: "Dune",
		CardSubtitle:  "2026 / 美国 / 科幻 / 导演A",
		ReleaseDate:   "2026-03-20",
		ReleaseScope:  models.MovieChartScopeTheatrical,
		Rating:        8.2,
		RatingCount:   4200,
		HasPoster:     true,
		DetailStatus:  models.MovieChartDetailSucceeded,
	}
	if first != want {
		t.Fatalf("卡片字段 = %+v，期望 %+v", first, want)
	}
	var undetermined, remoteOnly MovieChartItemView
	for _, item := range page.Items {
		switch item.DoubanID {
		case "702":
			undetermined = item
		case "703":
			remoteOnly = item
		}
	}
	if undetermined.HasPoster {
		t.Fatalf("没有海报的条目 HasPoster 必须为假: %+v", undetermined)
	}
	// 有远程地址、还没落盘：路由是纯本地读，请求过去必然 404，所以也是假。
	if remoteOnly.DoubanID != "703" || remoteOnly.HasPoster {
		t.Fatalf("只有远程地址、没有落盘的条目 HasPoster 必须为假: %+v", remoteOnly)
	}
	// 失败分类码要原样带给界面，否则「上映信息待确认」下面那行原因就没了。
	if undetermined.DetailError != string(WatchlistMetadataFailureNetworkUnreachable) {
		t.Fatalf("失败分类码 = %q", undetermined.DetailError)
	}
}

// ---------- 入参校验（需求设计文档 §6.1） ----------

// TestMovieChartListPageRejectsInvalidInput 钉住「非法枚举值一律拒绝，不静默回退」。
//
// 先跑一次合法调用：没有它，把 ListPage 改成「什么都拒绝」也能让下面全绿。
func TestMovieChartListPageRejectsInvalidInput(t *testing.T) {
	h := newMovieChartQueryHarness(t)
	h.seed(models.MovieChartEntry{DoubanID: "801", ReleaseScope: models.MovieChartScopeTheatrical, ReleaseDate: "2026-01-01"})
	if _, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, 1, false); err != nil {
		t.Fatalf("合法入参应当通过: %v", err)
	}

	for _, sort := range []string{"", "Release", "release ", "relaese", "T", "rating_desc"} {
		if _, err := h.service.ListPage(movieChartQueryYear, sort, 1, false); !errors.Is(err, ErrMovieChartOrderUnsupported) {
			t.Fatalf("排序 %q 应拒绝，实际 %v", sort, err)
		}
	}
	// 区间是 [1900, 当前年+5]，夹具时钟是 2026 年，所以上界是 2031。
	for _, year := range []int{0, -1, 1899, 2032, 9999} {
		if _, err := h.service.ListPage(year, MovieChartOrderRelease, 1, false); !errors.Is(err, ErrMovieChartYearOutOfRange) {
			t.Fatalf("年份 %d 应拒绝，实际 %v", year, err)
		}
	}
	for _, year := range []int{1900, 2031} {
		if _, err := h.service.ListPage(year, MovieChartOrderRelease, 1, false); err != nil {
			t.Fatalf("边界年份 %d 应当通过: %v", year, err)
		}
	}
	for _, page := range []int{0, -1} {
		if _, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, page, false); !errors.Is(err, ErrMovieChartPageOutOfRange) {
			t.Fatalf("页码 %d 应拒绝，实际 %v", page, err)
		}
	}
	// 绑定层的刷新入口用的是同一份年份判断，不另写一套上下界。
	if err := h.service.ValidateChartYear(2032); !errors.Is(err, ErrMovieChartYearOutOfRange) {
		t.Fatalf("ValidateChartYear 应拒绝 2032: %v", err)
	}
	if err := h.service.ValidateChartYear(2031); err != nil {
		t.Fatalf("ValidateChartYear 应接受 2031: %v", err)
	}
	var absent *MovieChartService
	if _, err := absent.ListPage(movieChartQueryYear, MovieChartOrderRelease, 1, false); err == nil {
		t.Fatalf("服务为空时应报错")
	}
}

// ---------- 分页（需求设计文档 §7） ----------

// TestMovieChartListPagePaginates 钉住每页 20 条，以及「越过末页返回空 Items 与
// 真实 Total，不报错」。
func TestMovieChartListPagePaginates(t *testing.T) {
	h := newMovieChartQueryHarness(t)
	for index := 0; index < 25; index++ {
		h.seed(models.MovieChartEntry{
			DoubanID:     fmt.Sprintf("9%03d", index),
			ReleaseScope: models.MovieChartScopeTheatrical,
			ReleaseDate:  fmt.Sprintf("2026-01-%02d", index+1),
		})
	}

	first, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, 1, false)
	if err != nil {
		t.Fatalf("读第 1 页失败: %v", err)
	}
	if len(first.Items) != MovieChartPageSize || first.Total != 25 {
		t.Fatalf("第 1 页 = %d 条、总数 %d", len(first.Items), first.Total)
	}
	if first.Items[0].DoubanID != "9000" {
		t.Fatalf("第 1 页首条 = %s", first.Items[0].DoubanID)
	}

	second, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, 2, false)
	if err != nil {
		t.Fatalf("读第 2 页失败: %v", err)
	}
	if len(second.Items) != 5 || second.Total != 25 {
		t.Fatalf("第 2 页 = %d 条、总数 %d", len(second.Items), second.Total)
	}
	if second.Items[0].DoubanID != "9020" {
		t.Fatalf("第 2 页首条 = %s（分页切分点错了）", second.Items[0].DoubanID)
	}

	// 越过末页：空列表 + 真实总数，**不报错**。分页器要靠 Total 把用户拉回来。
	beyond, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, 9, false)
	if err != nil {
		t.Fatalf("越过末页不该报错: %v", err)
	}
	if len(beyond.Items) != 0 {
		t.Fatalf("越过末页应返回空列表: %+v", beyond.Items)
	}
	if beyond.Items == nil {
		t.Fatalf("空列表要是 []，不是 null——前端会对着 null 崩掉")
	}
	if beyond.Total != 25 {
		t.Fatalf("越过末页的总数 = %d，期望 25", beyond.Total)
	}
}

// ---------- 缓存状态条与补全进度（概要设计 §4.2） ----------

// TestMovieChartListPageCacheStates 逐个钉住三形态，其中第二形态就是 TC-05
// 「豆瓣不可达时回落缓存」：有缓存时间 + 有失败码 + **条目照常返回**。
func TestMovieChartListPageCacheStates(t *testing.T) {
	refreshed := movieChartTestNow.Add(-2 * time.Hour)
	cases := []struct {
		name string
		// state 为 nil 表示库里压根没有这一年的状态行。
		state       *models.MovieChartYearState
		wantCached  bool
		wantFailure string
	}{
		{
			// 「从未抓过」与「抓过且失败」是 D-MC07 明令不得合并的两种状态：
			// 前者顶部是「还没有数据，点刷新获取」，后者要写出失败原因。没有这一格，
			// 在 state == nil 的分支上凭空编一个失败码也是绿的。
			name: "从未抓过：既没有缓存时间也没有失败码",
		},
		{
			name:       "成功且无失败码",
			state:      &models.MovieChartYearState{Year: movieChartQueryYear, LastRefreshedAt: &refreshed, LastAttemptAt: &refreshed},
			wantCached: true,
		},
		{
			name: "有缓存但上次失败：显示缓存 + 失败原因",
			state: &models.MovieChartYearState{
				Year: movieChartQueryYear, LastRefreshedAt: &refreshed, LastAttemptAt: &movieChartTestNow,
				LastFailure: string(WatchlistMetadataFailureNetworkUnreachable),
			},
			wantCached:  true,
			wantFailure: string(WatchlistMetadataFailureNetworkUnreachable),
		},
		{
			name: "从未成功且上次失败：空态 + 失败原因",
			state: &models.MovieChartYearState{
				Year: movieChartQueryYear, LastAttemptAt: &movieChartTestNow,
				LastFailure: string(WatchlistMetadataFailureProxyUnreachable),
			},
			wantFailure: string(WatchlistMetadataFailureProxyUnreachable),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			h := newMovieChartQueryHarness(t)
			if testCase.state != nil {
				h.seedYearState(*testCase.state)
			}
			h.seed(models.MovieChartEntry{DoubanID: "1001", ReleaseScope: models.MovieChartScopeTheatrical, ReleaseDate: "2026-01-01"})

			page, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, 1, false)
			if err != nil {
				t.Fatalf("读榜单失败: %v", err)
			}
			// 三种形态下缓存内容都要照常出来——代理 / 网络错误绝不渲染成「查无数据」。
			if len(page.Items) != 1 {
				t.Fatalf("缓存里的条目必须照常返回: %+v", page.Items)
			}
			if testCase.wantCached != (page.Cache.LastRefreshedAt != nil) {
				t.Fatalf("缓存时间 = %v，期望有缓存 = %v", page.Cache.LastRefreshedAt, testCase.wantCached)
			}
			if page.Cache.LastFailure != testCase.wantFailure {
				t.Fatalf("失败分类码 = %q，期望 %q", page.Cache.LastFailure, testCase.wantFailure)
			}
			if page.Cache.Refreshing {
				t.Fatalf("没有刷新在跑时不该报 refreshing")
			}
		})
	}
}

// TestMovieChartListPageBackfillProgress 钉住「补全中 N/M」：分子是 pending 与
// running 两种状态，分母是该年**全部**条目（含不进榜单的 excluded）。
func TestMovieChartListPageBackfillProgress(t *testing.T) {
	h := newMovieChartQueryHarness(t)
	h.seed(models.MovieChartEntry{DoubanID: "1101", DetailStatus: models.MovieChartDetailPending})
	h.seed(models.MovieChartEntry{DoubanID: "1102", DetailStatus: models.MovieChartDetailRunning})
	h.seed(models.MovieChartEntry{DoubanID: "1103", ReleaseScope: models.MovieChartScopeTheatrical, ReleaseDate: "2026-01-01"})
	h.seed(models.MovieChartEntry{DoubanID: "1104", ReleaseScope: models.MovieChartScopeExcluded})
	h.seed(models.MovieChartEntry{DoubanID: "1105", DetailStatus: models.MovieChartDetailFailed, DetailError: "not_found"})
	// 别的年份不进分母。
	h.seed(models.MovieChartEntry{DoubanID: "1106", Year: 2025, DetailStatus: models.MovieChartDetailPending})

	page, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, 1, false)
	if err != nil {
		t.Fatalf("读榜单失败: %v", err)
	}
	if page.Backfill.Pending != 2 {
		t.Fatalf("待补全 = %d，期望 2（pending + running）", page.Backfill.Pending)
	}
	if page.Backfill.Total != 5 {
		t.Fatalf("条目总数 = %d，期望 5（含 excluded、不含别的年份）", page.Backfill.Total)
	}
}

// ---------- 读路径与后台刷新的边界（D-MC08 / D-MC12） ----------

// TestMovieChartReadPathNeverFetches 钉住 D-MC08 的读写分离：读接口**没有副作用**，
// 一次请求都不发——哪怕这一年照 D-MC12 完全算到期（库里没有任何年状态行）。
//
// 这条是 P-005 评审的产物。初版把 D-MC12 的触发点挂在 ListPage 上，于是：翻页、
// 切排序、轮询状态条都会起抓取；更糟的是用户点「取消」之后，refreshDue 仍判到期
// （取消不写 last_failure 也不写 last_refreshed_at），前端下一次为刷新状态条而发的
// 读就把刚取消的那一轮原样重启。触发点现在独立成 OpenMovieChartYear（绑定层），
// 读接口只读。
func TestMovieChartReadPathNeverFetches(t *testing.T) {
	h := newMovieChartQueryHarness(t)
	h.seed(models.MovieChartEntry{DoubanID: "1201", ReleaseScope: models.MovieChartScopeTheatrical, ReleaseDate: "2026-01-01"})
	h.seedMark("1201", models.MovieChartMarkWatched, 2026, movieChartTestNow)

	for page := 1; page <= 3; page++ {
		if _, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, page, true); err != nil {
			t.Fatalf("读第 %d 页失败: %v", page, err)
		}
	}
	if _, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRating, 1, false); err != nil {
		t.Fatalf("切排序读失败: %v", err)
	}
	if _, err := h.service.ListWatched(); err != nil {
		t.Fatalf("读已看页失败: %v", err)
	}
	if _, err := h.service.ListYears(); err != nil {
		t.Fatalf("读年份失败: %v", err)
	}

	listCalls, detailCalls := h.source.calls()
	if len(listCalls) != 0 || len(detailCalls) != 0 {
		t.Fatalf("读路径发起了出网请求: list=%v detail=%v", listCalls, detailCalls)
	}
	if _, running := h.service.RefreshStatus(); running {
		t.Fatalf("读接口不得起后台刷新")
	}
	// 年状态表也不许被读接口写出来：refreshDue 与状态条都靠它，读一次就落一行
	// last_attempt_at 会把「从未抓过」变成「抓过」，往年从此再也不自动抓。
	var states int64
	if err := h.db.Model(&models.MovieChartYearState{}).Count(&states).Error; err != nil {
		t.Fatalf("统计年状态失败: %v", err)
	}
	if states != 0 {
		t.Fatalf("读接口不得写年状态，实际留下 %d 行", states)
	}
}

// TestMovieChartCacheRefreshingIsScopedToYear 钉住状态条里的 Refreshing 只对
// **正在抓的那一年**为真：刷新是按年起的，用户切到别的年份不该看到它的进度。
func TestMovieChartCacheRefreshingIsScopedToYear(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	// 断言失败时也要放开桩适配器：不放开，夹具的 Cleanup 会等一个永远不结束的
	// 轮次，一次失败就变成一次十分钟的超时。
	defer close(release)
	var once sync.Once
	source := &fakeMovieChartSource{
		page: func(req movieChartPageRequest) (MovieChartListPage, error) {
			once.Do(func() { close(entered) })
			<-release
			return movieChartFakePage(), nil
		},
	}
	h := &movieChartQueryHarness{movieChartHarness: newMovieChartHarness(t, source), source: source}
	if err := h.service.StartRefresh(movieChartQueryYear); err != nil {
		t.Fatalf("起一轮刷新失败: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatalf("刷新没有真的跑起来")
	}

	running, err := h.service.ListPage(movieChartQueryYear, MovieChartOrderRelease, 1, false)
	if err != nil {
		t.Fatalf("刷新期间读榜单失败: %v", err)
	}
	if !running.Cache.Refreshing {
		t.Fatalf("正在抓的年份应报 refreshing")
	}
	other, err := h.service.ListPage(2019, MovieChartOrderRelease, 1, false)
	if err != nil {
		t.Fatalf("刷新期间读别的年份失败: %v", err)
	}
	if other.Cache.Refreshing {
		t.Fatalf("跑的是 %d 年，2019 年不该报 refreshing", movieChartQueryYear)
	}
}

// ---------- 已看页（D-MC11） ----------

// TestMovieChartListWatchedGroupsByReleaseYear 钉住已看页：按上映年份**快照**
// 倒序分组，年份 0 单列一组排最后，组内按标记时间倒序。
func TestMovieChartListWatchedGroupsByReleaseYear(t *testing.T) {
	h := newMovieChartQueryHarness(t)
	older := movieChartTestNow.Add(-48 * time.Hour)
	newer := movieChartTestNow.Add(-time.Hour)
	// 1301 有意不带海报：既没有快照地址，缓存表里也没有落盘的图。
	h.seedMarkWithPoster("1301", models.MovieChartMarkWatched, 2019, older, "")
	// D-MC14：已看卡片的图是**缓存行**引用的本地文件，所以 HasPoster 由
	// movie_chart_entries.poster_path 决定，标记行里的 poster_url 快照不再参与。
	h.seed(models.MovieChartEntry{DoubanID: "1303", PosterPath: "movie_chart/3/1303.jpg"})
	h.seed(models.MovieChartEntry{
		DoubanID:  "1302",
		PosterURL: "https://img2.doubanio.com/view/photo/l/public/1302.jpg",
	})
	h.seedMark("1302", models.MovieChartMarkWatched, 2026, older)
	h.seedMark("1303", models.MovieChartMarkWatched, 2026, newer)
	h.seedMark("1304", models.MovieChartMarkWatched, 0, newer)
	// 与 1304 同年份、同标记时刻：组内顺序只能由 id 兜底决定。
	h.seedMark("1307", models.MovieChartMarkWatched, 0, newer)
	// 另外两种标记不进已看页。
	h.seedMark("1305", models.MovieChartMarkWant, 2026, newer)
	h.seedMark("1306", models.MovieChartMarkSkip, 2026, newer)

	groups, err := h.service.ListWatched()
	if err != nil {
		t.Fatalf("读已看页失败: %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("分组数 = %d，期望 3: %+v", len(groups), groups)
	}
	if groups[0].Year != 2026 || groups[1].Year != 2019 || groups[2].Year != 0 {
		t.Fatalf("分组顺序应按年份倒序、年份未知排最后: %+v", groups)
	}
	if len(groups[0].Items) != 2 || groups[0].Items[0].DoubanID != "1303" {
		t.Fatalf("组内应按标记时间倒序: %+v", groups[0].Items)
	}
	if groups[0].Items[0].Title != "片1303" || !groups[0].Items[0].HasPoster {
		t.Fatalf("已看卡片的标题取标记快照、海报取缓存行的落盘文件: %+v", groups[0].Items[0])
	}
	// 1302 在缓存里只有远程地址、没有落盘：标记行里有 poster_url 快照也不算数。
	if groups[0].Items[1].DoubanID != "1302" || groups[0].Items[1].HasPoster {
		t.Fatalf("缓存里没有落盘的图时 HasPoster 必须为假: %+v", groups[0].Items[1])
	}
	if !groups[0].Items[0].MarkedAt.Equal(newer) {
		t.Fatalf("标记时间 = %v，期望 %v", groups[0].Items[0].MarkedAt, newer)
	}
	// 没有落盘海报的已看条目必须报 HasPoster=false：这张卡片一旦让前端去请求
	// 海报路由，那就是一次必然 404 的往返，而这个字段存在的全部意义就是省掉它。
	if groups[1].Items[0].DoubanID != "1301" || groups[1].Items[0].HasPoster {
		t.Fatalf("没有海报快照的已看条目 HasPoster 必须为假: %+v", groups[1].Items[0])
	}
	// 同年同一刻标记的两条，靠 id 倒序兜底——没有它，两个后端给出的顺序不一样。
	if len(groups[2].Items) != 2 {
		t.Fatalf("年份未知那一组应有 2 条: %+v", groups[2].Items)
	}
	if groups[2].Items[0].DoubanID != "1307" || groups[2].Items[1].DoubanID != "1304" {
		t.Fatalf("同一刻标记的组内顺序应按 id 倒序: %+v", groups[2].Items)
	}

	// 缓存被整年清空之后，已看页照样完整（D-MC05 的快照三列自立门户）。
	if err := h.db.Where("1 = 1").Delete(&models.MovieChartEntry{}).Error; err != nil {
		t.Fatalf("清空缓存失败: %v", err)
	}
	after, err := h.service.ListWatched()
	if err != nil {
		t.Fatalf("清空缓存后读已看页失败: %v", err)
	}
	if len(after) != 3 {
		t.Fatalf("清空缓存后仍应有 3 组: %+v", after)
	}
	// 快照三列自立门户；海报是缓存行引用的本地文件，缓存没了就只剩占位——
	// 这是 D-MC14 明码标价的代价，卡片其余内容照常完整。
	if after[0].Items[0].Title != "片1303" {
		t.Fatalf("清空缓存后标题仍应来自快照: %+v", after[0].Items[0])
	}
	if after[0].Items[0].HasPoster {
		t.Fatalf("缓存行没了就没有本地海报可发: %+v", after[0].Items[0])
	}
}

// TestMovieChartListWatchedTiebreak 单钉已看页组内的 id 兜底。
//
// 实测结论（P-005，12 条同年份、同标记时刻的记录，两个后端各跑一遍）：
//   - 把兜底**方向写反**（id ASC）→ 两个后端都当场翻车，本用例抓得住。
//   - 把兜底**整条去掉** → 两个后端都照样绿。原因是两边的天然顺序恰好就等于
//     id 倒序：SQLite 按 rowid 扫，Postgres 对 release_year DESC 走的是反向索引
//     扫描，同键行按 TID 倒着出来。也就是说这条兜底目前**测不出「删掉」**，
//     它守的是「将来有人换了查询形态、天然顺序不再凑巧」那一天。
//
// 别为了让它变红去构造更极端的夹具：加大同键批次（12 条已经够 Postgres 的排序
// 不稳定性在评分排序上暴露）在这条查询上不起作用，上面那两条结论就是量出来的。
func TestMovieChartListWatchedTiebreak(t *testing.T) {
	h := newMovieChartQueryHarness(t)
	markedAt := movieChartTestNow.Add(-time.Hour)
	ids := make([]string, 0, 12)
	for index := 0; index < 12; index++ {
		id := fmt.Sprintf("15%02d", index)
		ids = append(ids, id)
		h.seedMark(id, models.MovieChartMarkWatched, 2024, markedAt)
	}

	groups, err := h.service.ListWatched()
	if err != nil {
		t.Fatalf("读已看页失败: %v", err)
	}
	if len(groups) != 1 || len(groups[0].Items) != len(ids) {
		t.Fatalf("应当只有一组、%d 条: %+v", len(ids), groups)
	}
	// 插入顺序即 id 顺序，倒序兜底意味着最后插入的排最前。
	for index, item := range groups[0].Items {
		want := ids[len(ids)-1-index]
		if item.DoubanID != want {
			t.Fatalf("组内第 %d 条 = %s，期望 %s（同年同时刻只能由 id 倒序定序）", index, item.DoubanID, want)
		}
	}
}

// TestMovieChartListWatchedEmpty 钉住空态返回空切片而不是 null：前端对着 null
// 会崩。
func TestMovieChartListWatchedEmpty(t *testing.T) {
	h := newMovieChartQueryHarness(t)
	groups, err := h.service.ListWatched()
	if err != nil {
		t.Fatalf("读已看页失败: %v", err)
	}
	if groups == nil || len(groups) != 0 {
		t.Fatalf("空态应返回空切片: %+v", groups)
	}
}

// ---------- 年份下拉 ----------

// TestMovieChartListYears 钉住年份下拉：本地有缓存的年份 ∪ {当前年}，倒序，
// 当前年不重复出现。
func TestMovieChartListYears(t *testing.T) {
	h := newMovieChartQueryHarness(t)
	empty, err := h.service.ListYears()
	if err != nil {
		t.Fatalf("读年份失败: %v", err)
	}
	if len(empty) != 1 || empty[0] != movieChartQueryYear {
		t.Fatalf("空库时至少要有当前年: %v", empty)
	}

	h.seed(models.MovieChartEntry{DoubanID: "1401", Year: 2019})
	h.seed(models.MovieChartEntry{DoubanID: "1402", Year: 2019})
	h.seed(models.MovieChartEntry{DoubanID: "1403", Year: 2026})
	h.seed(models.MovieChartEntry{DoubanID: "1404", Year: 2028})

	years, err := h.service.ListYears()
	if err != nil {
		t.Fatalf("读年份失败: %v", err)
	}
	want := []int{2028, 2026, 2019}
	if len(years) != len(want) {
		t.Fatalf("年份 = %v，期望 %v", years, want)
	}
	for index := range want {
		if years[index] != want[index] {
			t.Fatalf("年份 = %v，期望 %v", years, want)
		}
	}
}

// TestMovieChartQueryBackendParity 是一条自证用例：它只断言当前跑的是哪个后端，
// 让「Postgres 腿真的跑过了」在日志里留下痕迹。排序表达式与分页切分点的双后端
// 一致性由上面几条用例在两个后端上各跑一遍来保证。
func TestMovieChartQueryBackendParity(t *testing.T) {
	t.Logf("movie chart query tests running on %s", dbtest.Backend())
}
