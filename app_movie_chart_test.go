package main

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
	"video-master/services"
)

// 年度电影榜单绑定层的用例（app_movie_chart.go）。
//
// 为什么绑定层要单独测：这一层不是纯转发。年份区间的校验**只在这里**——
// MovieChartService.StartRefresh 有意把它让给了绑定层（movie_chart_service.go 的
// 注释），所以把 RefreshMovieChart 里那三行删掉，服务侧的用例一条都不会红，而
// RefreshMovieChart(0) 会拿 tags=0 向豆瓣发约 100 次列表请求、再发上千次详情请求。
//
// **这些用例一个字节都不出网**：夹具把资料源出网代理设成一个协议非法的地址，
// NewWatchlistMetadataHTTPClient 会在装配阶段就返回 ErrWatchlistMetadataProxyInvalid
// 并且**不退回直连**（这条语义本身是 D-MC01 要的）。于是万一某次改动让一轮抓取真的
// 起来了，它也只会在出网之前失败，并在 movie_chart_year_states 里留下一行——这一行
// 正好是用例用来判断「到底起没起」的证据。
const movieChartBindingInvalidProxy = "ftp://127.0.0.1:9"

// newMovieChartBindingApp 起一个数据库已就绪、榜单服务已构造好的 App。
func newMovieChartBindingApp(t *testing.T) *App {
	t.Helper()
	setupAppTestDB(t)
	seedMovieChartBindingSettings(t, movieChartBindingInvalidProxy)
	app := NewApp()
	app.resetMovieChartService()
	if app.movieChartService() == nil {
		t.Fatal("数据库已就绪时榜单服务应当构造出来")
	}
	t.Cleanup(func() {
		if svc := app.movieChartService(); svc != nil {
			svc.StopRefreshAndWait()
		}
	})
	return app
}

func seedMovieChartBindingSettings(t *testing.T, proxyURL string) {
	t.Helper()
	if err := database.DB.Create(&models.Settings{PlayWeight: 2, MetadataProxyURL: proxyURL}).Error; err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}
}

// movieChartYearStateCount 数某一年的状态行。一轮抓取不论成功失败都会写这一行
// （settleRefreshSuccess / settleRefreshFailure），所以它是「这一年到底有没有起过
// 一轮」最可靠的证据。
func movieChartYearStateCount(t *testing.T, year int) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Model(&models.MovieChartYearState{}).Where("year = ?", year).Count(&count).Error; err != nil {
		t.Fatalf("统计 %d 年状态失败: %v", year, err)
	}
	return count
}

// waitForMovieChartYearState 等某一年的状态行落库。
//
// **不能改用 StopRefreshAndWait 来「等它跑完」**：取消是立刻生效的，而
// settleRefreshFailure 的写入走的是本轮的 ctx，取消恰好卡在写之前时那一行就丢了
// （日志里是 write year state failed err=context canceled）。等证据本身出现，
// 才不会把「没起轮」和「起了但被我自己掐断」混为一谈。
func waitForMovieChartYearState(t *testing.T, year int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if movieChartYearStateCount(t, year) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d 年的抓取没有留下年状态行", year)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// movieChartRoundSettleWindow 是「起了一轮的话，它写年状态要多久」的观察窗口。
//
// 方向是安全的：本文件里任何真起来的一轮都在装配出网客户端那一步就失败（夹具的
// 非法代理），从起 goroutine 到落一行 INSERT 是微秒级，这个窗口有几个数量级的
// 余量。所以「窗口内没有行」＝「压根没起轮」，极端负载下最坏是变异假绿。
const movieChartRoundSettleWindow = 500 * time.Millisecond

// TestRefreshMovieChartValidatesYear 钉住绑定层的年份校验——这条链路上**唯一**的
// 一道，删掉它 RefreshMovieChart(0) 就会真的起一轮 tags=0 的整年抓取。
func TestRefreshMovieChartValidatesYear(t *testing.T) {
	app := newMovieChartBindingApp(t)
	service := app.movieChartService()

	rejected := []int{0, -1, 1899, time.Now().Year() + 6}
	for _, year := range rejected {
		if err := app.RefreshMovieChart(year); !errors.Is(err, services.ErrMovieChartYearOutOfRange) {
			t.Fatalf("年份 %d 应被拒绝，实际 %v", year, err)
		}
	}
	// 给「万一真起了一轮」留出落库的时间再看证据，不掐断它（理由见
	// waitForMovieChartYearState）。
	time.Sleep(movieChartRoundSettleWindow)
	for _, year := range rejected {
		if count := movieChartYearStateCount(t, year); count != 0 {
			t.Fatalf("被拒绝的年份 %d 不得起抓取，却留下了 %d 行年状态", year, count)
		}
	}

	// 合法年份照常起一轮：它会在装配出网客户端这一步就失败（夹具的非法代理），
	// 因此不出网，但年状态行会落下来，证明绑定层确实把它转发下去了。
	year := time.Now().Year()
	if err := app.RefreshMovieChart(year); err != nil {
		t.Fatalf("合法年份应当起得来: %v", err)
	}
	waitForMovieChartYearState(t, year)
	service.StopRefreshAndWait()
}

// TestOpenMovieChartYearIsTheRefreshTrigger 钉住 D-MC12 的触发点搬到了这里：
// OpenMovieChartYear 校验年份、到期才起一轮，且已经在跑时**不报错**。
func TestOpenMovieChartYearIsTheRefreshTrigger(t *testing.T) {
	app := newMovieChartBindingApp(t)
	service := app.movieChartService()

	if _, err := app.OpenMovieChartYear(1899); !errors.Is(err, services.ErrMovieChartYearOutOfRange) {
		t.Fatalf("越界年份应被拒绝: %v", err)
	}

	year := time.Now().Year()
	started, err := app.OpenMovieChartYear(year)
	if err != nil {
		t.Fatalf("打开当年榜单页失败: %v", err)
	}
	if !started {
		t.Fatal("当年从未抓过，打开页面应当起一轮")
	}
	// 等失败分类码真的落库：D-MC12 的「上一轮失败过就不再自动出网」看的就是它。
	waitForMovieChartYearState(t, year)
	service.StopRefreshAndWait()

	// 上一轮失败过之后就不再自动出网（D-MC12 的执行期修订）：再打开一次不起轮。
	again, err := app.OpenMovieChartYear(year)
	if err != nil {
		t.Fatalf("再次打开榜单页失败: %v", err)
	}
	if again {
		t.Fatal("上一轮失败过之后不该再自动起轮")
	}
}

// TestListMovieChartValidatesInput 钉住读接口在绑定层上的三条拒绝，以及**读接口
// 不起抓取**：这一年从未抓过（照 D-MC12 算到期），读它也不许留下任何年状态行。
func TestListMovieChartValidatesInput(t *testing.T) {
	app := newMovieChartBindingApp(t)
	year := time.Now().Year()

	if _, err := app.ListMovieChart(1899, "release", 1, false); !errors.Is(err, services.ErrMovieChartYearOutOfRange) {
		t.Fatalf("越界年份应被拒绝: %v", err)
	}
	if _, err := app.ListMovieChart(year, "relaese", 1, false); !errors.Is(err, services.ErrMovieChartOrderUnsupported) {
		t.Fatalf("非法排序应被拒绝: %v", err)
	}
	if _, err := app.ListMovieChart(year, "release", 0, false); !errors.Is(err, services.ErrMovieChartPageOutOfRange) {
		t.Fatalf("非法页码应被拒绝: %v", err)
	}

	page, err := app.ListMovieChart(year, "rating", 1, true)
	if err != nil {
		t.Fatalf("合法入参应当通过: %v", err)
	}
	if page.Year != year || page.Sort != "rating" || page.PageSize != 20 {
		t.Fatalf("回显字段不对: %+v", page)
	}
	// 同上：给「万一读接口顺手起了一轮」留出落库时间再看，**不要先 StopRefreshAndWait**
	// ——取消会把那一行的写入一起掐掉，断言就永远是绿的（这正是 P-005 自查时踩到的
	// 假绿：变异把触发点塞回 ListPage，这条用例照样通过）。
	time.Sleep(movieChartRoundSettleWindow)
	if count := movieChartYearStateCount(t, year); count != 0 {
		t.Fatalf("读接口不得起抓取，却留下了 %d 行年状态", count)
	}
}

// TestMovieChartMarkBindings 钉住标记两个绑定的枚举校验与幂等撤销。
func TestMovieChartMarkBindings(t *testing.T) {
	app := newMovieChartBindingApp(t)
	createChartEntryForTest(t, "1292052", "https://img2.doubanio.com/view/photo/l/public/p1.jpg")

	for _, mark := range []string{"", "loved", "WANT"} {
		if _, err := app.MarkMovieChartEntry("1292052", mark); !errors.Is(err, services.ErrMovieChartMarkUnsupported) {
			t.Fatalf("标记 %q 应被拒绝: %v", mark, err)
		}
	}
	if _, err := app.MarkMovieChartEntry("999999", "watched"); !errors.Is(err, services.ErrMovieChartEntryNotFound) {
		t.Fatalf("缓存里没有的条目应被拒绝: %v", err)
	}

	result, err := app.MarkMovieChartEntry("1292052", "watched")
	if err != nil {
		t.Fatalf("标记已看失败: %v", err)
	}
	if result.Mark != "watched" || result.WatchlistCreated || result.WatchlistConflict {
		t.Fatalf("已看不该联动想看片单: %+v", result)
	}
	groups, err := app.ListWatchedMovies()
	if err != nil {
		t.Fatalf("读已看页失败: %v", err)
	}
	if len(groups) != 1 || len(groups[0].Items) != 1 || groups[0].Items[0].DoubanID != "1292052" {
		t.Fatalf("已看页应当有这一条: %+v", groups)
	}

	if err := app.ClearMovieChartMark("1292052"); err != nil {
		t.Fatalf("撤销标记失败: %v", err)
	}
	// 没有标记时幂等返回 nil。
	if err := app.ClearMovieChartMark("1292052"); err != nil {
		t.Fatalf("重复撤销应当幂等: %v", err)
	}

	years, err := app.ListMovieChartYears()
	if err != nil {
		t.Fatalf("读年份失败: %v", err)
	}
	if len(years) == 0 {
		t.Fatalf("年份下拉至少要有当前年: %v", years)
	}
}

// TestMovieChartBindingsWithoutService 钉住数据库没就绪时每个绑定都**明确报错**。
//
// CancelMovieChartRefresh 是这里唯一需要绑定层自己判 nil 的：
// MovieChartService.CancelRefresh 对 nil 接收者是静默空操作，直接转发会把
// 「服务压根没起来」回成一次成功的取消，界面上的取消按钮就成了摆设。
func TestMovieChartBindingsWithoutService(t *testing.T) {
	setupAppTestDB(t)
	app := NewApp()
	if app.movieChartService() != nil {
		t.Fatal("没调 resetMovieChartService 时不该有服务")
	}
	year := time.Now().Year()

	if err := app.CancelMovieChartRefresh(); err == nil {
		t.Fatal("服务不可用时取消必须报错，不能假装取消成功")
	}
	if _, err := app.ListMovieChart(year, "release", 1, false); err == nil {
		t.Fatal("服务不可用时读榜单应报错")
	}
	if err := app.RefreshMovieChart(year); err == nil {
		t.Fatal("服务不可用时刷新应报错")
	}
	if _, err := app.OpenMovieChartYear(year); err == nil {
		t.Fatal("服务不可用时打开榜单页应报错")
	}
	if _, err := app.MarkMovieChartEntry("1292052", "want"); err == nil {
		t.Fatal("服务不可用时标记应报错")
	}
	if err := app.ClearMovieChartMark("1292052"); err == nil {
		t.Fatal("服务不可用时撤销应报错")
	}
	if _, err := app.ListWatchedMovies(); err == nil {
		t.Fatal("服务不可用时已看页应报错")
	}
	if _, err := app.ListMovieChartYears(); err == nil {
		t.Fatal("服务不可用时年份下拉应报错")
	}
}

// TestCancelMovieChartRefreshForwardsToService 钉住取消确实转发下去了：
// 上面那条只证明了服务缺席时报错，不证明服务在场时真的调了 CancelRefresh。
func TestCancelMovieChartRefreshForwardsToService(t *testing.T) {
	app := newMovieChartBindingApp(t)
	if err := app.CancelMovieChartRefresh(); err != nil {
		t.Fatalf("没有抓取在跑时取消应当是空操作: %v", err)
	}
}

// hangingProxy 是一个只接受连接、永不应答的 TCP 监听器，用来把一轮抓取**钉死**在
// 出网这一步上。
//
// 它不是代理：代理协议一个字节都不实现，因为客户端在等应答的那一刻就已经卡住了，
// 这正是本用例需要的状态。**连接不会流向任何外部地址**，监听的是 127.0.0.1。
type hangingProxy struct {
	listener net.Listener
	mu       sync.Mutex
	conns    []net.Conn
	accepted int
}

func newHangingProxy(t *testing.T) *hangingProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起挂起代理失败: %v", err)
	}
	proxy := &hangingProxy{listener: listener}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			proxy.mu.Lock()
			proxy.conns = append(proxy.conns, conn)
			proxy.accepted++
			proxy.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		proxy.mu.Lock()
		for _, conn := range proxy.conns {
			_ = conn.Close()
		}
		proxy.mu.Unlock()
	})
	return proxy
}

func (p *hangingProxy) address() string { return "http://" + p.listener.Addr().String() }

func (p *hangingProxy) acceptedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accepted
}

// TestResetMovieChartServiceStopsPreviousRefresh 钉住 resetMovieChartService 里的
// old.StopRefreshAndWait()：换服务之前必须先把旧服务那一轮抓取收干净。
//
// 少了那一句，恢复备份失败续跑之后会有两个 MovieChartService 同时往同一批表里写，
// 而旧的那个握着的是**已经 Close 掉的连接**（enterDatabaseRestoreMode 关的就是它）。
//
// 用一个只接受连接、永不应答的本机监听器把旧轮次钉在出网这一步：它只有被取消才会
// 结束，所以「reset 返回之后旧轮次还在不在跑」这一问的答案就只由那一句决定。
func TestResetMovieChartServiceStopsPreviousRefresh(t *testing.T) {
	setupAppTestDB(t)
	proxy := newHangingProxy(t)
	seedMovieChartBindingSettings(t, proxy.address())
	app := NewApp()
	app.resetMovieChartService()
	old := app.movieChartService()
	t.Cleanup(func() {
		old.StopRefreshAndWait()
		if svc := app.movieChartService(); svc != nil {
			svc.StopRefreshAndWait()
		}
	})

	if err := app.RefreshMovieChart(time.Now().Year()); err != nil {
		t.Fatalf("起一轮抓取失败: %v", err)
	}
	// 等它真的挂在出网上，否则这一轮可能还没开始，收不收摊都看不出差别。
	deadline := time.Now().Add(30 * time.Second)
	for proxy.acceptedCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("抓取没有挂到出网这一步上")
		}
		time.Sleep(10 * time.Millisecond)
	}

	app.resetMovieChartService()

	if _, running := old.RefreshStatus(); running {
		t.Fatal("换服务之前必须先把旧服务那一轮收干净")
	}
	if app.movieChartService() == old {
		t.Fatal("reset 之后应当是一个新实例")
	}
}
