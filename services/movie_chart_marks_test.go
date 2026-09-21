package services

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

// 本文件按概要设计 §4.3（标记生命周期）、D-MC05 / D-MC13 与需求设计文档 §5、§7
// 逐条验收标记状态机。每个用例注明它钉的是哪一条不变量。
//
// 两条在库的**最终状态**里看不出来的不变量（「先建片单、再写标记」的顺序、
// 「重复点同一标记不再碰片单」），靠挂在连接上的语句钩子观察，见 watchStatements。

// ---------- 夹具 ----------

type movieChartMarkHarness struct {
	*movieChartHarness
	watchlist *WatchlistService
}

// newMovieChartMarkHarness 起一套标记用的夹具。
//
// WatchlistService 全程写全局 database.DB（services/watchlist_service.go 通篇如此），
// MovieChartService 写构造时注入的连接。生产里两者是同一个连接，测试必须显式对齐，
// 否则片单条目落进另一个库，「先建片单、再写标记」这条联动根本测不到。
//
// 换掉一个**全局**变量再在 Cleanup 里换回来，安全的前提是 services 包里没有任何
// 用例调 t.Parallel()：并行跑的用例会互相看见对方换上去的那个库。将来谁给这个包
// 加并行，这个夹具必须跟着改。
//
// 片单服务先于榜单服务构造：它如今是 NewMovieChartService 的构造参数（P-005 从
// 方法入参收上去的）。NewWatchlistService 只吃一个数据目录、构造时不碰数据库，
// 所以放在换 database.DB 之前是安全的。
func newMovieChartMarkHarness(t *testing.T) *movieChartMarkHarness {
	t.Helper()
	watchlist := NewWatchlistService(t.TempDir())
	// 标记路径不出网；适配器只是夹具必须给的一个参数。
	base := newMovieChartHarnessWithWatchlist(t, &fakeMovieChartSource{}, watchlist)
	previous := database.DB
	database.DB = base.db
	t.Cleanup(func() { database.DB = previous })
	return &movieChartMarkHarness{movieChartHarness: base, watchlist: watchlist}
}

// seedEntry 直接写一条缓存条目——标记的快照三列从这里取。
func (h *movieChartMarkHarness) seedEntry(doubanID, title, releaseDate string) models.MovieChartEntry {
	h.t.Helper()
	entry := models.MovieChartEntry{
		DoubanID:     doubanID,
		Year:         2026,
		Title:        title,
		ReleaseScope: models.MovieChartScopeTheatrical,
		ReleaseDate:  releaseDate,
		PosterURL:    "https://img2.doubanio.com/view/photo/" + doubanID + ".jpg",
		DetailStatus: models.MovieChartDetailSucceeded,
	}
	if err := h.db.Create(&entry).Error; err != nil {
		h.t.Fatalf("写入缓存条目 %s 失败: %v", doubanID, err)
	}
	return entry
}

func (h *movieChartMarkHarness) markRow(doubanID string) (models.MovieChartMark, bool) {
	h.t.Helper()
	var mark models.MovieChartMark
	err := h.db.Where("douban_id = ?", doubanID).First(&mark).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.MovieChartMark{}, false
	}
	if err != nil {
		h.t.Fatalf("读取标记 %s 失败: %v", doubanID, err)
	}
	return mark, true
}

func (h *movieChartMarkHarness) markCount() int64 {
	h.t.Helper()
	var count int64
	if err := h.db.Model(&models.MovieChartMark{}).Count(&count).Error; err != nil {
		h.t.Fatalf("统计标记失败: %v", err)
	}
	return count
}

func (h *movieChartMarkHarness) watchlistEntries() []models.WatchlistEntry {
	h.t.Helper()
	var entries []models.WatchlistEntry
	if err := h.db.Order("id ASC").Find(&entries).Error; err != nil {
		h.t.Fatalf("读取想看片单失败: %v", err)
	}
	return entries
}

// ---------- 语句顺序观察 ----------

// movieChartStatementLog 记录 watchlist_entries 与 movie_chart_marks 上的写语句。
//
// 「先建片单、再写标记」是需求设计文档 §5 的崩溃边界：两次写入都成功之后，库里
// 两行都在，谁先写的不留任何痕迹。这条不变量只能在语句这一层钉住。
type movieChartStatementLog struct {
	mu         sync.Mutex
	statements []string
}

func (l *movieChartStatementLog) record(statement string) {
	l.mu.Lock()
	l.statements = append(l.statements, statement)
	l.mu.Unlock()
}

func (l *movieChartStatementLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.statements...)
}

// indexOf 返回某条语句首次出现的位置，没出现过返回 -1。
func (l *movieChartStatementLog) indexOf(statement string) int {
	for at, seen := range l.snapshot() {
		if seen == statement {
			return at
		}
	}
	return -1
}

func (l *movieChartStatementLog) count(statement string) int {
	total := 0
	for _, seen := range l.snapshot() {
		if seen == statement {
			total++
		}
	}
	return total
}

const (
	createWatchlistStatement = "create watchlist_entries"
	deleteWatchlistStatement = "delete watchlist_entries"
	createMarkStatement      = "create movie_chart_marks"
	deleteMarkStatement      = "delete movie_chart_marks"
)

func (h *movieChartMarkHarness) watchStatements() *movieChartStatementLog {
	h.t.Helper()
	logged := &movieChartStatementLog{}
	watched := map[string]bool{"watchlist_entries": true, "movie_chart_marks": true}
	hook := func(verb string, register func(string, func(*gorm.DB)) error) {
		name := "movie_chart_mark_test_" + verb
		err := register(name, func(tx *gorm.DB) {
			if table := movieChartStatementTable(tx); watched[table] {
				logged.record(verb + " " + table)
			}
		})
		if err != nil {
			h.t.Fatalf("注册 %s 钩子失败: %v", verb, err)
		}
	}
	hook("create", h.db.Callback().Create().After("gorm:create").Register)
	hook("update", h.db.Callback().Update().After("gorm:update").Register)
	hook("delete", h.db.Callback().Delete().After("gorm:delete").Register)
	h.t.Cleanup(func() {
		_ = h.db.Callback().Create().Remove("movie_chart_mark_test_create")
		_ = h.db.Callback().Update().Remove("movie_chart_mark_test_update")
		_ = h.db.Callback().Delete().Remove("movie_chart_mark_test_delete")
	})
	return logged
}

// hookOnceAfterMarkQuery 在**第一次**读 movie_chart_marks 之后插一段代码，用完即摘。
//
// 「撤销只删我读到的那个标记」这条守卫，只能靠在读与删之间挤进一次改标记来验证：
// 事后看库的状态，分不出「守卫拦住了」与「压根没有并发」。
//
// 挂在查询之后而不是 DELETE 之前：GORM 默认把删除包在事务里，Before("gorm:delete")
// 是在那笔事务**内部**跑的，此时再从另一条连接写一次，SQLite 直接 database is locked。
// 读之后这个位置没有任何事务开着，注入的写入能完整跑完，正好落在读与删之间。
//
// 只注入一次，用 atomic.Bool 的 CAS 而不是 sync.Once：注入的写入自己也会读一次
// movie_chart_marks，在 Once.Do 内部再调一次同一个 Once 会死锁。用原子量而不是普通
// 布尔量，是因为其中一个用例的注入发生在**另一个 goroutine** 上，钩子会被两个
// goroutine 同时命中，普通布尔量在 -race 下就是一条数据竞争。
func (h *movieChartMarkHarness) hookOnceAfterMarkQuery(name string, fn func()) {
	h.t.Helper()
	var injected atomic.Bool
	err := h.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if movieChartStatementTable(tx) != "movie_chart_marks" {
			return
		}
		if !injected.CompareAndSwap(false, true) {
			return
		}
		fn()
	})
	if err != nil {
		h.t.Fatalf("注册查询后钩子失败: %v", err)
	}
	h.t.Cleanup(func() { _ = h.db.Callback().Query().Remove(name) })
}

// ---------- 用例 ----------

// TestMovieChartMarkWriteAndClearEachMark 钉住状态图前三条转换与三条撤销
// （概要设计 §4.3）：三种标记各自可写、快照三列取标记时的缓存行、只有 want 联动
// 片单，且 want 的写入顺序是**先建片单、再写标记**（需求设计文档 §5）。
func TestMovieChartMarkWriteAndClearEachMark(t *testing.T) {
	cases := []struct {
		mark          string
		wantWatchlist bool
	}{
		{models.MovieChartMarkWant, true},
		{models.MovieChartMarkSkip, false},
		{models.MovieChartMarkWatched, false},
	}
	for _, tc := range cases {
		t.Run(tc.mark, func(t *testing.T) {
			h := newMovieChartMarkHarness(t)
			entry := h.seedEntry("101", "沙丘", "2026-03-20")
			statements := h.watchStatements()

			result, err := h.service.MarkEntry(entry.DoubanID, tc.mark)
			if err != nil {
				t.Fatalf("标记 %s 失败: %v", tc.mark, err)
			}
			if result.Mark != tc.mark || result.WatchlistCreated != tc.wantWatchlist || result.WatchlistConflict {
				t.Fatalf("标记结果不对: %+v", result)
			}

			row, ok := h.markRow(entry.DoubanID)
			if !ok {
				t.Fatalf("标记行应写进库")
			}
			if row.Mark != tc.mark {
				t.Fatalf("标记值错误: %q", row.Mark)
			}
			// 快照三列：标记时的片名、上映年份与海报地址（D-MC05）。
			if row.Title != "沙丘" || row.ReleaseYear != 2026 || row.PosterURL != entry.PosterURL {
				t.Fatalf("快照三列错误: %+v", row)
			}
			if !row.MarkedAt.Equal(movieChartTestNow) {
				t.Fatalf("marked_at 应取标记时刻: %v", row.MarkedAt)
			}

			entries := h.watchlistEntries()
			if tc.wantWatchlist {
				if len(entries) != 1 || entries[0].Title != "沙丘" || entries[0].Kind != models.WatchlistKindMovie {
					t.Fatalf("想看应建一条 movie 片单条目: %+v", entries)
				}
				if row.WatchlistEntryID != entries[0].ID {
					t.Fatalf("标记应记下本次创建的片单条目 ID: got=%d want=%d", row.WatchlistEntryID, entries[0].ID)
				}
				// 顺序：片单条目先落库，标记行后落库。反过来崩溃会留下一个指向
				// 不存在片单条目的标记，撤销会照着那个 ID 去删别人的行。
				createdAt, markedAt := statements.indexOf(createWatchlistStatement), statements.indexOf(createMarkStatement)
				if createdAt < 0 || markedAt < 0 || createdAt > markedAt {
					t.Fatalf("必须先建片单条目再写标记行: %v", statements.snapshot())
				}
			} else {
				if len(entries) != 0 {
					t.Fatalf("%s 不应写想看片单: %+v", tc.mark, entries)
				}
				if row.WatchlistEntryID != 0 {
					t.Fatalf("%s 不应记录片单条目 ID: %d", tc.mark, row.WatchlistEntryID)
				}
			}

			if err := h.service.ClearMark(entry.DoubanID); err != nil {
				t.Fatalf("撤销 %s 失败: %v", tc.mark, err)
			}
			if _, ok := h.markRow(entry.DoubanID); ok {
				t.Fatalf("撤销后标记行应删除")
			}
			if remaining := h.watchlistEntries(); len(remaining) != 0 {
				t.Fatalf("撤销应删掉本次由榜单创建的片单条目: %+v", remaining)
			}
			if tc.wantWatchlist {
				// 撤销侧的顺序与改标记侧同构：片单条目先删，标记行后删。倒过来
				// 一旦删片单失败，那条记录就再没有任何标记指向它。
				deletedWatchlist, deletedMark := statements.indexOf(deleteWatchlistStatement), statements.indexOf(deleteMarkStatement)
				if deletedWatchlist < 0 || deletedMark < 0 || deletedWatchlist > deletedMark {
					t.Fatalf("撤销必须先删片单条目再删标记行: %v", statements.snapshot())
				}
			}
		})
	}
}

// TestMovieChartMarkKeepsUserTypedWatchlistEntry 是 TC-10：撞上用户手输的同名同类型
// 条目时标记照记、不记归属，撤销与改标记都**不动**那条记录（D-MC13）。
func TestMovieChartMarkKeepsUserTypedWatchlistEntry(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	manual, err := h.watchlist.Create("沙丘", models.WatchlistKindMovie)
	if err != nil {
		t.Fatalf("用户手输片单条目失败: %v", err)
	}
	entry := h.seedEntry("201", "沙丘", "2026-03-20")
	statements := h.watchStatements()

	result, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant)
	if err != nil {
		t.Fatalf("撞名时标记仍应成功: %v", err)
	}
	if !result.WatchlistConflict || result.WatchlistCreated {
		t.Fatalf("撞名应只报冲突、不报新建: %+v", result)
	}
	row, ok := h.markRow(entry.DoubanID)
	if !ok || row.Mark != models.MovieChartMarkWant {
		t.Fatalf("撞名时标记照记: %+v", row)
	}
	if row.WatchlistEntryID != 0 {
		t.Fatalf("撞名复用的是用户自己的条目，不得记归属: %d", row.WatchlistEntryID)
	}

	// 撤销不误删。
	if err := h.service.ClearMark(entry.DoubanID); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	entries := h.watchlistEntries()
	if len(entries) != 1 || entries[0].ID != manual.ID || entries[0].Title != "沙丘" {
		t.Fatalf("用户手输的片单条目必须原样保留: %+v", entries)
	}

	// 改标记（want → skip）同样不得动它。
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatalf("重新标记想看失败: %v", err)
	}
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkSkip); err != nil {
		t.Fatalf("改标记失败: %v", err)
	}
	entries = h.watchlistEntries()
	if len(entries) != 1 || entries[0].ID != manual.ID {
		t.Fatalf("改标记也不得删用户手输的条目: %+v", entries)
	}
	if deleted := statements.count(deleteWatchlistStatement); deleted != 0 {
		t.Fatalf("整个用例不应对 watchlist_entries 发出任何 DELETE: %v", statements.snapshot())
	}
}

// TestMovieChartMarkTransitions 跑遍状态图里的六条改标记转换（概要设计 §4.3）：
// 一个条目始终只有一行标记；离开 want 时**先**跑撤销副作用再落新标记；
// 进入 want 时新建片单条目并记下归属。
func TestMovieChartMarkTransitions(t *testing.T) {
	transitions := []struct{ from, to string }{
		{models.MovieChartMarkWant, models.MovieChartMarkSkip},
		{models.MovieChartMarkWant, models.MovieChartMarkWatched},
		{models.MovieChartMarkSkip, models.MovieChartMarkWatched},
		{models.MovieChartMarkSkip, models.MovieChartMarkWant},
		{models.MovieChartMarkWatched, models.MovieChartMarkWant},
		{models.MovieChartMarkWatched, models.MovieChartMarkSkip},
	}
	for _, tc := range transitions {
		t.Run(tc.from+"_to_"+tc.to, func(t *testing.T) {
			h := newMovieChartMarkHarness(t)
			entry := h.seedEntry("301", "沙丘", "2026-03-20")
			if _, err := h.service.MarkEntry(entry.DoubanID, tc.from); err != nil {
				t.Fatalf("写入初始标记失败: %v", err)
			}
			before, _ := h.markRow(entry.DoubanID)
			// 期间列表刷新改了缓存行：改标记是一次**新的**标记动作，快照三列
			// 按当下的缓存行重新取（D-MC05 的「标记时快照」）。
			if err := h.db.Model(&models.MovieChartEntry{}).Where("douban_id = ?", entry.DoubanID).
				Updates(map[string]any{
					"title":        "沙丘（重映）",
					"release_date": "2027-01-01",
					"poster_url":   "https://img2.doubanio.com/view/photo/301-new.jpg",
				}).Error; err != nil {
				t.Fatalf("更新缓存行失败: %v", err)
			}
			later := movieChartTestNow.Add(time.Hour)
			h.setClock(later)
			statements := h.watchStatements()

			result, err := h.service.MarkEntry(entry.DoubanID, tc.to)
			if err != nil {
				t.Fatalf("改标记失败: %v", err)
			}
			if result.Mark != tc.to {
				t.Fatalf("改标记结果错误: %+v", result)
			}
			if count := h.markCount(); count != 1 {
				t.Fatalf("一个条目只能有一行标记，实际 %d 行", count)
			}
			row, _ := h.markRow(entry.DoubanID)
			if row.Mark != tc.to {
				t.Fatalf("库里的标记值错误: %q", row.Mark)
			}
			if row.Title != "沙丘（重映）" || row.ReleaseYear != 2027 ||
				row.PosterURL != "https://img2.doubanio.com/view/photo/301-new.jpg" {
				t.Fatalf("改标记应按当下缓存行重取快照: %+v", row)
			}
			// updated_at 必须由 DoUpdates 显式赋值——GORM 在这条路径上不维护它，
			// 漏掉的话这一列会永远停在插入时刻。
			if !row.MarkedAt.Equal(later) || !row.UpdatedAt.Equal(later) {
				t.Fatalf("改标记应同时刷新 marked_at 与 updated_at: marked=%v updated=%v", row.MarkedAt, row.UpdatedAt)
			}

			entries := h.watchlistEntries()
			if tc.from == models.MovieChartMarkWant {
				// 离开 want：本次由榜单创建的那条片单记录必须先被删掉。
				deletedAt := statements.indexOf(deleteWatchlistStatement)
				if deletedAt < 0 {
					t.Fatalf("离开 want 应删掉榜单创建的片单条目: %v", statements.snapshot())
				}
				if markedAt := statements.indexOf(createMarkStatement); markedAt < 0 || deletedAt > markedAt {
					t.Fatalf("撤销副作用必须先于新标记落库: %v", statements.snapshot())
				}
				for _, remaining := range entries {
					if remaining.ID == before.WatchlistEntryID {
						t.Fatalf("榜单创建的片单条目 %d 应随改标记删除", before.WatchlistEntryID)
					}
				}
			}
			if tc.to == models.MovieChartMarkWant {
				if len(entries) != 1 || row.WatchlistEntryID != entries[0].ID || !result.WatchlistCreated {
					t.Fatalf("改成 want 应新建片单条目并记下归属: row=%+v entries=%+v result=%+v", row, entries, result)
				}
				createdAt, markedAt := statements.indexOf(createWatchlistStatement), statements.indexOf(createMarkStatement)
				if createdAt < 0 || markedAt < 0 || createdAt > markedAt {
					t.Fatalf("必须先建片单条目再写标记行: %v", statements.snapshot())
				}
			} else {
				if row.WatchlistEntryID != 0 {
					t.Fatalf("非 want 标记不得挂片单归属: %d", row.WatchlistEntryID)
				}
				if len(entries) != 0 {
					t.Fatalf("非 want 标记不应留下片单条目: %+v", entries)
				}
			}
		})
	}
}

// TestMovieChartMarkRepeatIsIdempotent 钉住需求设计文档 §7 的「对同一条目重复点
// 同一个标记 → 幂等，只刷新 marked_at」。
//
// 这条最容易写错的地方是 want：再调一次 WatchlistService.Create 必然撞上自己刚建
// 的那条，撞名分支会把 watchlist_entry_id 抹成 0，撤销时那条由榜单创建的片单记录
// 就再也删不掉了，界面还会弹一条莫名其妙的「该片名已在想看片单中」。
func TestMovieChartMarkRepeatIsIdempotent(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	entry := h.seedEntry("401", "沙丘", "2026-03-20")
	first, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant)
	if err != nil || !first.WatchlistCreated {
		t.Fatalf("首次标记想看失败: %+v %v", first, err)
	}
	created, _ := h.markRow(entry.DoubanID)
	if created.WatchlistEntryID == 0 {
		t.Fatalf("首次标记应记下片单条目 ID")
	}

	// 期间列表刷新改了缓存行：快照不跟着动，只有 marked_at 会刷新。
	if err := h.db.Model(&models.MovieChartEntry{}).Where("douban_id = ?", entry.DoubanID).
		Updates(map[string]any{
			"title":        "沙丘（重映）",
			"release_date": "2027-01-01",
			"poster_url":   "https://img2.doubanio.com/view/photo/401-new.jpg",
		}).Error; err != nil {
		t.Fatalf("更新缓存行失败: %v", err)
	}
	later := movieChartTestNow.Add(2 * time.Hour)
	h.setClock(later)
	statements := h.watchStatements()

	again, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant)
	if err != nil {
		t.Fatalf("重复标记失败: %v", err)
	}
	if again.WatchlistCreated || again.WatchlistConflict {
		t.Fatalf("重复点同一标记不应再动片单: %+v", again)
	}
	if createCalls := statements.count(createWatchlistStatement); createCalls != 0 {
		t.Fatalf("重复标记不得再建片单条目: %v", statements.snapshot())
	}
	if deleteCalls := statements.count(deleteWatchlistStatement); deleteCalls != 0 {
		t.Fatalf("重复标记不得删片单条目: %v", statements.snapshot())
	}

	row, _ := h.markRow(entry.DoubanID)
	if row.WatchlistEntryID != created.WatchlistEntryID {
		t.Fatalf("重复标记必须保住片单归属: got=%d want=%d", row.WatchlistEntryID, created.WatchlistEntryID)
	}
	if !row.MarkedAt.Equal(later) || !row.UpdatedAt.Equal(later) {
		t.Fatalf("重复标记应刷新 marked_at 与 updated_at: marked=%v updated=%v", row.MarkedAt, row.UpdatedAt)
	}
	if row.Title != "沙丘" || row.ReleaseYear != 2026 || row.PosterURL != entry.PosterURL {
		t.Fatalf("重复标记只刷新 marked_at，快照三列不动: %+v", row)
	}
	if count := h.markCount(); count != 1 {
		t.Fatalf("重复标记不应多出一行: %d", count)
	}
	if entries := h.watchlistEntries(); len(entries) != 1 {
		t.Fatalf("重复标记不应多出片单条目: %+v", entries)
	}

	// 归属没丢，所以撤销仍然删得掉那条由榜单创建的记录。
	if err := h.service.ClearMark(entry.DoubanID); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if entries := h.watchlistEntries(); len(entries) != 0 {
		t.Fatalf("撤销应删掉榜单创建的片单条目: %+v", entries)
	}
}

// TestMovieChartMarkSnapshotOutlivesCache 钉住 D-MC05：快照是标记时拷下来的值，
// 缓存行被整年重建（乃至删除）之后已看页仍然完整可用；撤销也不要求缓存行还在。
// 同时钉住上映年份**取不到就是 0、不拿 tags 年份猜**（需求设计文档 §7）。
func TestMovieChartMarkSnapshotOutlivesCache(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	dated := h.seedEntry("501", "沙丘", "2026-03-20")
	undated := h.seedEntry("502", "未定档新片", "")
	for _, doubanID := range []string{dated.DoubanID, undated.DoubanID} {
		if _, err := h.service.MarkEntry(doubanID, models.MovieChartMarkWatched); err != nil {
			t.Fatalf("标记已看失败: %v", err)
		}
	}
	if row, _ := h.markRow(undated.DoubanID); row.ReleaseYear != 0 {
		t.Fatalf("未定档条目的上映年份应为 0（不拿 tags 年份猜），实际 %d", row.ReleaseYear)
	}

	// 缓存被清空：删掉两条缓存行。
	if err := h.db.Where("1 = 1").Delete(&models.MovieChartEntry{}).Error; err != nil {
		t.Fatalf("清空缓存失败: %v", err)
	}
	row, ok := h.markRow(dated.DoubanID)
	if !ok || row.Title != "沙丘" || row.ReleaseYear != 2026 || row.PosterURL != dated.PosterURL {
		t.Fatalf("缓存清空后快照仍应完整: %+v", row)
	}
	// 改标记要缓存行（快照没有可信来源），撤销不要。
	if _, err := h.service.MarkEntry(dated.DoubanID, models.MovieChartMarkSkip); !errors.Is(err, ErrMovieChartEntryNotFound) {
		t.Fatalf("缓存里没有该条目时应拒绝标记: %v", err)
	}
	if err := h.service.ClearMark(dated.DoubanID); err != nil {
		t.Fatalf("缓存清空后仍应能撤销: %v", err)
	}
	if _, ok := h.markRow(dated.DoubanID); ok {
		t.Fatalf("撤销后标记行应删除")
	}
}

// TestMovieChartMarkRejectsUnknownEntryAndMark 钉住两条拒绝：缓存里没有的豆瓣 ID、
// 三个取值之外的标记（需求设计文档 §7 与 §6.1「非法枚举值一律拒绝，不静默回退」）。
func TestMovieChartMarkRejectsUnknownEntryAndMark(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	entry := h.seedEntry("601", "沙丘", "2026-03-20")

	if _, err := h.service.MarkEntry("999999", models.MovieChartMarkWant); !errors.Is(err, ErrMovieChartEntryNotFound) {
		t.Fatalf("缓存里没有的豆瓣 ID 应拒绝: %v", err)
	}
	for _, bad := range []string{"", "loved", "WANT", "want "} {
		if _, err := h.service.MarkEntry(entry.DoubanID, bad); !errors.Is(err, ErrMovieChartMarkUnsupported) {
			t.Fatalf("标记 %q 应拒绝: %v", bad, err)
		}
	}
	if count := h.markCount(); count != 0 {
		t.Fatalf("被拒绝的标记不得落库: %d", count)
	}
	if entries := h.watchlistEntries(); len(entries) != 0 {
		t.Fatalf("被拒绝的标记不得写片单: %+v", entries)
	}

	// 缺少片单服务时明确报错，而不是裸奔写一条没法撤销的标记。片单服务如今是
	// 构造参数，忘记注入只可能发生在构造点，所以这里另构造一个没有它的实例来验。
	noWatchlist := NewMovieChartService(h.db, nil)
	if _, err := noWatchlist.MarkEntry(entry.DoubanID, models.MovieChartMarkWant); err == nil {
		t.Fatalf("片单服务为空时应报错")
	}
	if err := noWatchlist.ClearMark(entry.DoubanID); err == nil {
		t.Fatalf("片单服务为空时应报错")
	}
	var absent *MovieChartService
	if _, err := absent.MarkEntry(entry.DoubanID, models.MovieChartMarkWant); err == nil {
		t.Fatalf("榜单服务为空时应报错")
	}
	if err := absent.ClearMark(entry.DoubanID); err == nil {
		t.Fatalf("榜单服务为空时应报错")
	}

	// 校验必须跑在**任何变更之前**：条目不在缓存里就整个拒绝，连上一个标记的
	// 撤销副作用都不许跑。把 loadChartEntry 挪到撤销副作用之后，下面这条想看
	// 记录就会在一次注定失败的调用里被删掉。
	linked := h.seedEntry("602", "银翼杀手", "2026-05-01")
	if _, err := h.service.MarkEntry(linked.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatalf("标记想看失败: %v", err)
	}
	before, _ := h.markRow(linked.DoubanID)
	if before.WatchlistEntryID == 0 {
		t.Fatalf("前提被破坏：这条标记应挂着片单归属")
	}
	if err := h.db.Where("douban_id = ?", linked.DoubanID).Delete(&models.MovieChartEntry{}).Error; err != nil {
		t.Fatalf("清掉缓存行失败: %v", err)
	}
	if _, err := h.service.MarkEntry(linked.DoubanID, models.MovieChartMarkSkip); !errors.Is(err, ErrMovieChartEntryNotFound) {
		t.Fatalf("缓存里没有该条目时应拒绝: %v", err)
	}
	after, ok := h.markRow(linked.DoubanID)
	if !ok || after.Mark != models.MovieChartMarkWant || after.WatchlistEntryID != before.WatchlistEntryID {
		t.Fatalf("被拒绝的标记不得改动既有标记: before=%+v after=%+v", before, after)
	}
	remaining := h.watchlistEntries()
	if len(remaining) != 1 || remaining[0].ID != before.WatchlistEntryID {
		t.Fatalf("被拒绝的标记不得删掉片单条目: %+v", remaining)
	}
}

// TestMovieChartMarkUndoToleratesUserDeletedWatchlistEntry 钉住撤销副作用的容错：
// 用户自己先在片单页删掉了那条由榜单创建的记录，撤销标记仍应成功——目的本就是
// 「那条记录不在了」，不该因为它已经不在而报错、把标记卡住。
func TestMovieChartMarkUndoToleratesUserDeletedWatchlistEntry(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	entry := h.seedEntry("901", "沙丘", "2026-03-20")
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatalf("标记想看失败: %v", err)
	}
	row, _ := h.markRow(entry.DoubanID)
	if row.WatchlistEntryID == 0 {
		t.Fatalf("标记应记下片单条目 ID")
	}
	// 用户在片单页自己删掉了它。
	if err := h.watchlist.Delete(row.WatchlistEntryID); err != nil {
		t.Fatalf("用户删除片单条目失败: %v", err)
	}

	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWatched); err != nil {
		t.Fatalf("片单条目已被用户删掉时改标记仍应成功: %v", err)
	}
	if got, _ := h.markRow(entry.DoubanID); got.Mark != models.MovieChartMarkWatched || got.WatchlistEntryID != 0 {
		t.Fatalf("改标记后应落在已看且不再挂归属: %+v", got)
	}

	// 撤销路径同样容错。
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant); err != nil {
		t.Fatalf("重新标记想看失败: %v", err)
	}
	again, _ := h.markRow(entry.DoubanID)
	if err := h.watchlist.Delete(again.WatchlistEntryID); err != nil {
		t.Fatalf("用户再次删除片单条目失败: %v", err)
	}
	if err := h.service.ClearMark(entry.DoubanID); err != nil {
		t.Fatalf("片单条目已被用户删掉时撤销仍应成功: %v", err)
	}
	if _, ok := h.markRow(entry.DoubanID); ok {
		t.Fatalf("撤销后标记行应删除")
	}
}

// TestMovieChartClearMarkIsIdempotent 钉住「撤销一个没有标记的条目是幂等的、不报错」
// （需求设计文档 §6.1 ClearMovieChartMark）。
func TestMovieChartClearMarkIsIdempotent(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	entry := h.seedEntry("701", "沙丘", "2026-03-20")
	if err := h.service.ClearMark(entry.DoubanID); err != nil {
		t.Fatalf("从未标记过的条目撤销应无错: %v", err)
	}
	if err := h.service.ClearMark("888888"); err != nil {
		t.Fatalf("缓存里都没有的豆瓣 ID 撤销应无错: %v", err)
	}
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkSkip); err != nil {
		t.Fatalf("标记失败: %v", err)
	}
	for round := 0; round < 2; round++ {
		if err := h.service.ClearMark(entry.DoubanID); err != nil {
			t.Fatalf("第 %d 次撤销应无错: %v", round+1, err)
		}
	}
	if count := h.markCount(); count != 0 {
		t.Fatalf("撤销后不应留下标记行: %d", count)
	}
}

// TestMovieChartClearMarkKeepsNewerMark 钉住撤销的条件更新守卫：读到标记之后、
// DELETE 发出之前有人把它改成了 want，撤销必须**什么都不删**。
//
// 这条不是一次普通的丢失更新。删掉那个较新的 want 之后，它刚建的片单记录就没有
// 任何标记指向它了：界面上是一条追溯不到来源的「想看」，之后没有任何撤销能再清掉
// 它。守卫只比 id 不够——改标记走的是按 douban_id 的 upsert，主键不变，比 id 会
// 照删不误，所以守卫比的是标记值与片单归属。
func TestMovieChartClearMarkKeepsNewerMark(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	entry := h.seedEntry("1001", "沙丘", "2026-03-20")
	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkSkip); err != nil {
		t.Fatalf("写入初始标记失败: %v", err)
	}

	h.hookOnceAfterMarkQuery("movie_chart_mark_test_race", func() {
		// 用户在别处把「不想看」改成了「想看」：片单里随之多了一条由榜单
		// 创建、归这个新标记所有的记录。
		//
		// 走未加锁的内核 markEntry：标记路径已经被 s.markMu 串行化，
		// 进程内不会再出现这种交错，同一个 goroutine 再进一次公开入口只会自锁。
		// 这个用例钉的是**库层守卫本身**——锁被挪走或绕开时的第二道防线。
		if _, err := h.service.markEntry(entry.DoubanID, models.MovieChartMarkWant); err != nil {
			t.Errorf("并发改标记失败: %v", err)
		}
	})

	if err := h.service.ClearMark(entry.DoubanID); err != nil {
		t.Fatalf("撤销撞上并发改标记应按成功处理，不报错: %v", err)
	}

	row, ok := h.markRow(entry.DoubanID)
	if !ok {
		t.Fatalf("较新的标记不得被撤销删掉")
	}
	if row.Mark != models.MovieChartMarkWant {
		t.Fatalf("留下的应是较新的那个标记: %q", row.Mark)
	}
	entries := h.watchlistEntries()
	if len(entries) != 1 {
		t.Fatalf("片单里应恰好剩那条由较新标记创建的记录: %+v", entries)
	}
	if row.WatchlistEntryID != entries[0].ID {
		t.Fatalf("片单记录必须仍有标记认领它，否则就是清不掉的孤儿: mark=%d entry=%d",
			row.WatchlistEntryID, entries[0].ID)
	}
	// 守卫拦下的是这一次；用户再点一次撤销，照常连片单记录一起清干净。
	if err := h.service.ClearMark(entry.DoubanID); err != nil {
		t.Fatalf("重新撤销失败: %v", err)
	}
	if _, ok := h.markRow(entry.DoubanID); ok {
		t.Fatalf("重新撤销后标记行应删除")
	}
	if remaining := h.watchlistEntries(); len(remaining) != 0 {
		t.Fatalf("重新撤销应删掉榜单创建的片单条目: %+v", remaining)
	}
}

// TestMovieChartClearMarkGuardTermsAreIndependent 分别钉住撤销守卫的两项条件。
//
// 起因：只有「skip(归属 0) → want(归属 N)」这一种交错时，mark 与 watchlist_entry_id
// 同时变了，去掉任意一项守卫都还拦得住，两项都没有被单独钉住。下面两个子用例各自
// 只让一项发生变化。
func TestMovieChartClearMarkGuardTermsAreIndependent(t *testing.T) {
	// 归属变了、标记值没变：want(A) →（撤销窗口内）skip → want(B)。改标记走的是
	// 同一行 upsert，行 id 与 mark 都和读到的一样，只有 watchlist_entry_id 从 A
	// 变成了 B。少了这一项守卫，撤销会删掉这个较新的 want，片单条目 B 随即无主。
	t.Run("只有归属变化", func(t *testing.T) {
		h := newMovieChartMarkHarness(t)
		entry := h.seedEntry("1002", "沙丘", "2026-03-20")
		if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant); err != nil {
			t.Fatalf("写入初始标记失败: %v", err)
		}
		before, _ := h.markRow(entry.DoubanID)
		// 先垫一条用户自己的片单记录，让 A 不是表里 id 最大的那行。否则 SQLite
		// 会把删掉的 A 的 rowid 原样发给下一次插入，B 与 A 相等，这个用例就退化成
		// 「什么都没变」，守卫去掉也测不出来。
		if _, err := h.watchlist.Create("占位片", models.WatchlistKindMovie); err != nil {
			t.Fatalf("垫片单条目失败: %v", err)
		}

		h.hookOnceAfterMarkQuery("movie_chart_mark_test_owner", func() {
			// 注入走未加锁的内核，理由同 TestMovieChartClearMarkKeepsNewerMark。
			if _, err := h.service.markEntry(entry.DoubanID, models.MovieChartMarkSkip); err != nil {
				t.Errorf("注入改标记失败: %v", err)
			}
			if _, err := h.service.markEntry(entry.DoubanID, models.MovieChartMarkWant); err != nil {
				t.Errorf("注入改回想看失败: %v", err)
			}
		})

		if err := h.service.ClearMark(entry.DoubanID); err != nil {
			t.Fatalf("撤销撞上并发改标记应按成功处理: %v", err)
		}
		row, ok := h.markRow(entry.DoubanID)
		if !ok {
			t.Fatalf("归属已经换人，撤销不得删掉这行标记")
		}
		if row.ID != before.ID || row.Mark != models.MovieChartMarkWant {
			t.Fatalf("前提被破坏：这个用例要求同一行、标记值不变: before=%+v after=%+v", before, row)
		}
		if row.WatchlistEntryID == before.WatchlistEntryID {
			t.Fatalf("前提被破坏：这个用例要求归属确实换了 id（拿到的还是 %d）", row.WatchlistEntryID)
		}
		entries := h.watchlistEntries()
		if len(entries) != 2 {
			t.Fatalf("片单里应只剩「占位片」与归属 B 两条: %+v", entries)
		}
		claimed := false
		for _, candidate := range entries {
			if candidate.ID == row.WatchlistEntryID {
				claimed = true
			}
		}
		if !claimed {
			t.Fatalf("较新标记的片单条目必须还在，且被它认领: mark=%d entries=%+v", row.WatchlistEntryID, entries)
		}
	})

	// 标记值变了、归属没变（两边都是 0）：skip → watched。这一种不会产生孤儿，
	// 但撤销照样不该删掉用户刚设的新标记。
	t.Run("只有标记值变化", func(t *testing.T) {
		h := newMovieChartMarkHarness(t)
		entry := h.seedEntry("1003", "沙丘", "2026-03-20")
		if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkSkip); err != nil {
			t.Fatalf("写入初始标记失败: %v", err)
		}
		before, _ := h.markRow(entry.DoubanID)

		h.hookOnceAfterMarkQuery("movie_chart_mark_test_markvalue", func() {
			if _, err := h.service.markEntry(entry.DoubanID, models.MovieChartMarkWatched); err != nil {
				t.Errorf("注入改标记失败: %v", err)
			}
		})

		if err := h.service.ClearMark(entry.DoubanID); err != nil {
			t.Fatalf("撤销撞上并发改标记应按成功处理: %v", err)
		}
		row, ok := h.markRow(entry.DoubanID)
		if !ok {
			t.Fatalf("标记值已经被改过，撤销不得删掉这行标记")
		}
		if row.ID != before.ID || row.WatchlistEntryID != before.WatchlistEntryID {
			t.Fatalf("前提被破坏：这个用例要求同一行、归属不变: before=%+v after=%+v", before, row)
		}
		if row.Mark != models.MovieChartMarkWatched {
			t.Fatalf("留下的应是较新的那个标记: %q", row.Mark)
		}
	})
}

// movieChartSerialiseBudget 是「第二次标记在第一次完成前不该跑完」的观察窗口。
//
// 方向是安全的：有锁时第二次调用**根本不可能**在窗口内跑完（它卡在 Lock 上），
// 所以不会假红；没锁时它只有几条本地语句，1 秒有两个数量级的余量，所以变异检查
// 稳定见红。极端负载下最坏的结果是变异**假绿**，不是用例假红。
const movieChartSerialiseBudget = time.Second

// TestMovieChartMarkSerialisesConcurrentMarks 钉住 MovieChartService.markMu：两次交错的
// MarkEntry 不得把片单条目留成无主行。
//
// 没有这把锁时的交错（Wails 给每个绑定调用各起一个 goroutine，双击就够了）：
// 外层读到「无标记」→ 内层整段跑完，建片单条目 N 并写 want → 外层拿着过期的
// 「无标记」写下 skip 覆盖掉它。最终库里是 skip、归属 0，而片单条目 N 没有任何
// 标记认领——界面上一条追溯不到来源、撤也撤不掉的「想看」。
func TestMovieChartMarkSerialisesConcurrentMarks(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	entry := h.seedEntry("1101", "沙丘", "2026-03-20")

	started := make(chan struct{})
	done := make(chan struct{})
	h.hookOnceAfterMarkQuery("movie_chart_mark_test_serialise", func() {
		go func() {
			defer close(done)
			close(started)
			// 走**公开入口**：这里要验的就是入口上的串行化。
			if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant); err != nil {
				t.Errorf("并发标记失败: %v", err)
			}
		}()
		<-started
		select {
		case <-done:
			// 有锁时走不到这里；真走到了，说明第二次标记整段插进了第一次中间。
		case <-time.After(movieChartSerialiseBudget):
		}
	})

	if _, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkSkip); err != nil {
		t.Fatalf("外层标记失败: %v", err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("外层放锁之后并发标记仍未结束")
	}

	// 真正的验收是最终状态自洽：库里有几条片单记录，就得有几条被标记认领。
	row, ok := h.markRow(entry.DoubanID)
	if !ok {
		t.Fatalf("两次标记之后应留下一行标记")
	}
	entries := h.watchlistEntries()
	if len(entries) != 1 {
		t.Fatalf("两次标记只该留下一条片单记录: %+v", entries)
	}
	if row.Mark != models.MovieChartMarkWant || row.WatchlistEntryID != entries[0].ID {
		t.Fatalf("片单记录必须被最终的标记认领，否则就是清不掉的孤儿: mark=%+v entries=%+v", row, entries)
	}
}

// TestMovieChartMarkTruncatesOverlongTitle 钉住快照片名按 rune 截到 size:200
// ——两处用的都是 P-003 的 movieChartTruncateTitle，不另写一份。
//
// 只在 SQLite 上跑：Postgres 的 movie_chart_entries.title 本身就是 varchar(200)，
// 超长片名根本进不了缓存表，这条守卫在 PG 腿上不可达。反过来说，正因为 SQLite
// 不校验长度，一条从 SQLite 库迁过去的超长片名会在 PG 上让标记写入报 22001。
func TestMovieChartMarkTruncatesOverlongTitle(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("Postgres 的 movie_chart_entries.title 是 varchar(200)，超长片名进不了缓存表")
	}
	h := newMovieChartMarkHarness(t)
	overlong := strings.Repeat("长", 250)
	entry := h.seedEntry("801", overlong, "2026-03-20")

	result, err := h.service.MarkEntry(entry.DoubanID, models.MovieChartMarkWant)
	if err != nil {
		t.Fatalf("超长片名也应标记成功: %v", err)
	}
	if !result.WatchlistCreated {
		t.Fatalf("超长片名应照常建片单条目: %+v", result)
	}
	row, _ := h.markRow(entry.DoubanID)
	if utf8.RuneCountInString(row.Title) != movieChartTitleLimit {
		t.Fatalf("标记快照片名应截到 %d 个字符，实际 %d", movieChartTitleLimit, utf8.RuneCountInString(row.Title))
	}
	if row.Title != string([]rune(overlong)[:movieChartTitleLimit]) {
		t.Fatalf("截断必须按 rune 切，不能切出半个字符: %q", row.Title)
	}
	entries := h.watchlistEntries()
	if len(entries) != 1 || utf8.RuneCountInString(entries[0].Title) != movieChartTitleLimit {
		t.Fatalf("片单条目也应拿到截断后的片名: %+v", entries)
	}
	if row.WatchlistEntryID != entries[0].ID {
		t.Fatalf("归属应记在截断后的那条片单条目上: %d", row.WatchlistEntryID)
	}
}

// TestMovieChartReleaseYearSnapshot 逐例钉住上映年份快照的取值：只认
// release_date 的 YYYY 前缀，取不到就是 0。
func TestMovieChartReleaseYearSnapshot(t *testing.T) {
	cases := []struct {
		releaseDate string
		want        int
	}{
		{"2026-03-20", 2026},
		{"1994-09-10", 1994},
		{"", 0},
		{"2026", 2026},
		{"abc", 0},
		{"202", 0},
		// Atoi 认得下面两个，但它们不是年份。少了 year <= 0 这一关，第二条会把
		// -26 当成上映年份存进快照。
		{"0000-01-01", 0},
		{"-026-03-20", 0},
	}
	for _, tc := range cases {
		if got := movieChartReleaseYear(tc.releaseDate); got != tc.want {
			t.Errorf("movieChartReleaseYear(%q)=%d, want %d", tc.releaseDate, got, tc.want)
		}
	}
}
