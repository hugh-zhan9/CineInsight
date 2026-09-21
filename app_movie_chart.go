package main

import (
	"errors"

	"video-master/database"
	"video-master/services"
)

// 年度电影榜单的 Wails 绑定（需求设计文档 §6.1）。
//
// 绑定层只做参数校验与转发，业务全在 MovieChartService。**非法枚举值一律拒绝，
// 不静默回退**——沿用 app_watchlist.go:15 记录的既有态度：一次前端缺陷若被悄悄
// 兜住，用户看到的是一个「按了没反应」的控件，而没有任何人知道参数传错了。
//
// 校验的实现不在这一层：年份区间、排序枚举、页码下界都由服务侧唯一拥有
// （services/movie_chart_query.go），这里只负责在转发前把它调起来。两处各写一套
// 上下界早晚会对不上，这是 movie_chart_service.go 在 StartRefresh 上已经记过的
// 判断。
//
// 读接口一律不出网（D-MC08）：豆瓣不可达时页面显示的是本地缓存加一行失败文案，
// 出网只发生在后台刷新任务里。

// resetMovieChartService 在数据库就绪后构造年度榜单服务，形态照
// resetImageAITaggingService。
//
// 不在 NewApp 里构造：database.DB 要等 main 里的 database.Init 才有值，在那之前
// 构造出来的服务会永久握着一个 nil 连接。
func (a *App) resetMovieChartService() {
	a.movieChartMu.Lock()
	old := a.movieChart
	a.movieChart = nil
	a.movieChartMu.Unlock()
	if old != nil {
		// 旧服务可能正在抓某一年，先让它收摊再换：两个服务同时往同一批表里写，
		// 认领与写回的守卫虽然拦得住，日志与进度却会交叉得看不懂。
		old.StopRefreshAndWait()
	}
	if database.DB == nil {
		return
	}
	svc := services.NewMovieChartService(database.DB, a.watchlistService)
	svc.SetBackgroundTaskRegistry(a.backgroundTasks)
	a.movieChartMu.Lock()
	a.movieChart = svc
	a.movieChartMu.Unlock()
}

// movieChartService 取当前的榜单服务。数据库没就绪时是 nil——服务侧每个入口都
// 判了 nil 接收者并返回「年度榜单服务不可用」，所以调用点不需要各自再判一次。
func (a *App) movieChartService() *services.MovieChartService {
	a.movieChartMu.RLock()
	defer a.movieChartMu.RUnlock()
	return a.movieChart
}

// OpenMovieChartYear 是「打开榜单页 / 切换年份」这个动作的入口，也是 D-MC12
// 自动刷新的**唯一触发点**。返回值表示这次是否真的起了一轮后台抓取。
//
// **前端必须在榜单页挂载时、以及每次切换年份之后各调一次**，而且只在这两处调：
// ListMovieChart 是纯读，翻页、切排序、轮询状态条都不会触发抓取。把触发点放在
// 读接口上时，用户点了「取消」之后，前端下一次为刷新状态条而发的读就会把那一轮
// 原样重启（取消不写失败码也不写成功时间，refreshDue 仍判到期），取消按钮形同
// 虚设——这就是它单独成一个绑定的原因。
//
// 不起抓取不是错误：没到期、已经有一轮在跑，都返回 (false, nil)。真要立刻抓一轮
// 走 RefreshMovieChart（用户点刷新按钮），那条路径在已经有一轮在跑时才报错。
func (a *App) OpenMovieChartYear(year int) (bool, error) {
	service := a.movieChartService()
	if err := service.ValidateChartYear(year); err != nil {
		return false, err
	}
	return service.EnsureYearRefreshed(year)
}

// ListMovieChart 读一页榜单。只查本地缓存表，不出网，**没有副作用**
// （不会顺手起抓取，见 OpenMovieChartYear）。
//
// sort ∈ {release, rating}，page ≥ 1，year ∈ [1900, 当前年+5]，越界返回错误。
// showMarked 是「不过滤」开关，它只解除标记造成的隐藏（不想看 / 已看），**不**
// 放宽内地公映口径。
func (a *App) ListMovieChart(year int, sort string, page int, showMarked bool) (*services.MovieChartPage, error) {
	return a.movieChartService().ListPage(year, sort, page, showMarked)
}

// RefreshMovieChart 手动起一轮后台刷新。已经在跑时返回错误，不排队、不并发。
func (a *App) RefreshMovieChart(year int) error {
	service := a.movieChartService()
	if err := service.ValidateChartYear(year); err != nil {
		return err
	}
	return service.StartRefresh(year)
}

// CancelMovieChartRefresh 中止当前这一轮。没有在跑时是空操作。
//
// 取消不是失败：已写入的条目全部保留，缓存时间不更新，也不记失败码。
func (a *App) CancelMovieChartRefresh() error {
	service := a.movieChartService()
	if service == nil {
		// 这里得自己判：CancelRefresh 对 nil 接收者是静默空操作，直接转发会把
		// 「服务压根没起来」回成一次成功的取消。
		return errors.New("年度榜单服务不可用")
	}
	service.CancelRefresh()
	return nil
}

// MarkMovieChartEntry 标记一个条目（want / skip / watched）。
// 条目不在本地缓存里时返回错误——标记的快照字段只能从缓存行取。
func (a *App) MarkMovieChartEntry(doubanID string, mark string) (*services.MovieChartMarkResult, error) {
	return a.movieChartService().MarkEntry(doubanID, mark)
}

// ClearMovieChartMark 撤销标记，没有标记时幂等返回 nil。
func (a *App) ClearMovieChartMark(doubanID string) error {
	return a.movieChartService().ClearMark(doubanID)
}

// ListWatchedMovies 返回已看页，按影片上映年份（标记时快照）倒序分组。
func (a *App) ListWatchedMovies() ([]services.WatchedMovieYearGroup, error) {
	return a.movieChartService().ListWatched()
}

// ListMovieChartYears 返回年份下拉的选项：本地有缓存的年份 ∪ {当前年}，倒序。
func (a *App) ListMovieChartYears() ([]int, error) {
	return a.movieChartService().ListYears()
}
