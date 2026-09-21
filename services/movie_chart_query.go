package services

import (
	"errors"
	"fmt"
	"time"

	"video-master/models"

	"gorm.io/gorm"
)

// 年度电影榜单的读路径（D-MC08）：榜单页分页查询、页面顶部
// 状态条、已看页分组、年份下拉。
//
// **全程不出网。** 这不是顺带的实现选择，而是「豆瓣不可达时直接读本地缓存渲染」
// 这条需求的实现方式：读路径只查本地三张表，压根不认识豆瓣存不存在，出网只发生在
// 后台刷新任务里（概要设计 §4.2）。因此一次代理配置错误、一次断网，都只会表现为
// 状态条上的一行失败文案，绝不会把缓存渲染成空榜单。
//
// 响应形状由需求设计文档 §6.1 拥有，逐字段只给调用方要用的东西，不透传存储对象。

const (
	// MovieChartOrderRelease 是默认排序：上映时间升序（旧片在前），未定档置底。
	// MovieChartOrderRating 是评分降序。两者都是**读接口**的排序，与
	// MovieChartSort*（豆瓣列表端点的 T/U/R/S）不是一回事，别混用。
	MovieChartOrderRelease = "release"
	MovieChartOrderRating  = "rating"

	// MovieChartPageSize 恒为 20（D-MC10），不接受调用方指定。
	MovieChartPageSize = 20

	// movieChartMinYear / movieChartMaxYearAhead 是年份入参的合法区间
	// [1900, 当前年+5]（需求设计文档 §6.1）。
	//
	// 只此一份：绑定层通过 ValidateChartYear 用同一份判断，两处各写一套上下界
	// 早晚会对不上。
	movieChartMinYear      = 1900
	movieChartMaxYearAhead = 5
)

// 读接口的三种拒绝。非法枚举值一律拒绝、不静默回退（app_watchlist.go:15 的既有态度）：
// 悄悄把 sort=relaese 当成 release、把 page=0 当成 1，界面上看到的就是一个「按了
// 没反应」的控件，而调用方永远不知道自己传错了。
//
// 叫 Order 而不是 Sort：ErrMovieChartSortUnsupported 已经归 movie_chart_douban.go
// 所有，指的是豆瓣列表端点的 T/U/R/S。两套排序的取值空间完全不同，共用一个错误
// 会让「排序不认识」这句话指不清是哪一层不认识。
var (
	ErrMovieChartOrderUnsupported = errors.New("不支持的榜单排序")
	ErrMovieChartYearOutOfRange   = errors.New("榜单年份超出可用区间")
	ErrMovieChartPageOutOfRange   = errors.New("榜单页码必须从 1 开始")
)

// movieChartVisibleScopes 是榜单里**看得见**的 release_scope 取值。
//
// 三个取值分别对应：确定在内地院线上映、上映信息未定（按 D-MC02 的裁决收），
// 以及空串——详情还没补全的条目。空串必须在列表里出现，只是打上「上映信息待确认」
// 的标：整年抓完到详情补完之间有约 1500 次限速请求（25 分钟），这段时间里把它们
// 藏起来，用户看到的就是一个逐渐长出来的半截榜单。
//
// excluded **永不出现**：那是已经判定过、确定不在内地院线上映的条目（含剧集）。
// 它留在库里不删（D-MC04），只是不进榜单。
var movieChartVisibleScopes = []string{
	models.MovieChartScopeTheatrical,
	models.MovieChartScopeUndetermined,
	"",
}

// movieChartHiddenMarks 是默认会从榜单里隐藏的标记：「不想看」与「已看」
// （概要设计 §4.3）。want 照常显示。
var movieChartHiddenMarks = []string{models.MovieChartMarkSkip, models.MovieChartMarkWatched}

// MovieChartPage 是榜单页一页的全部内容（需求设计文档 §6.1）。
//
// Year / Sort 回显请求参数，供前端丢弃过时响应：切年份、切排序时上一次请求可能
// 后到，回显让它能被认出来。
type MovieChartPage struct {
	Year     int                  `json:"year"`
	Sort     string               `json:"sort"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
	Total    int64                `json:"total"`
	Items    []MovieChartItemView `json:"items"`
	Cache    MovieChartCacheState `json:"cache"`
	Backfill MovieChartBackfill   `json:"backfill"`
}

// MovieChartItemView 是榜单里的一张卡片。
//
// 没有 Overview / Countries / Genres / Directors：需求设计文档 §6.1 明说列表不展示
// 它们，CardSubtitle（豆瓣原样的「2026 / 中国大陆 / 纪录片 / 张凌赫」）已经带了产地、
// 类型与导演。将来要详情弹窗时再加一个方法，不在列表里预先透传。
//
// 也没有海报地址：前端拿到的是 HasPoster 布尔，图走 /preview/douban-chart-poster/
// 代理路由（D-MC09）。HasPoster 为假时前端不发请求，省下一次
// 必然 404 的往返。
type MovieChartItemView struct {
	DoubanID      string  `json:"douban_id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	CardSubtitle  string  `json:"card_subtitle"`
	ReleaseDate   string  `json:"release_date"`
	ReleaseScope  string  `json:"release_scope"`
	Rating        float64 `json:"rating"`
	RatingCount   int     `json:"rating_count"`
	HasPoster     bool    `json:"has_poster"`
	Mark          string  `json:"mark"`
	DetailStatus  string  `json:"detail_status"`
	DetailError   string  `json:"detail_error"`
}

// MovieChartCacheState 驱动页面顶部状态条的三形态（概要设计 §4.2）。
//
// 三形态由前两个字段的组合决定，互斥且都不是空榜单：有成功时间无失败码＝「数据
// 更新于 <时间>」；有成功时间有失败码＝「数据来自缓存（<时间>），最近一次更新
// 失败：<文案>」；无成功时间有失败码＝「暂无该年数据，更新失败：<文案>」。
// 代理 / 网络错误因此永远不会被渲染成「查无数据」。
type MovieChartCacheState struct {
	LastRefreshedAt *time.Time `json:"last_refreshed_at" ts_type:"string"`
	LastFailure     string     `json:"last_failure"`
	Refreshing      bool       `json:"refreshing"`
}

// MovieChartBackfill 驱动「补全中 N/M」。Total 是该年**全部**条目，含 excluded：
// 分母是详情阶段要跑的条目数，不是榜单里看得见的条目数。
type MovieChartBackfill struct {
	Pending int `json:"pending"`
	Total   int `json:"total"`
}

// WatchedMovieYearGroup 是已看页的一组（D-MC11）。
// Year 为 0 表示上映年份未知，单列一组，不猜（需求设计文档 §7）。
type WatchedMovieYearGroup struct {
	Year  int                `json:"year"`
	Items []WatchedMovieView `json:"items"`
}

// WatchedMovieView 是已看页的一张卡片，字段全部取自标记行的**快照**。
//
// 不去 join 缓存表：已看记录是用户产生的事实，缓存被整年重建、条目从豆瓣下架之后
// 都必须照常显示（D-MC05）。
type WatchedMovieView struct {
	DoubanID  string    `json:"douban_id"`
	Title     string    `json:"title"`
	HasPoster bool      `json:"has_poster"`
	MarkedAt  time.Time `json:"marked_at" ts_type:"string"`
}

// ValidateChartYear 校验年份入参，供绑定层在转发前调用。
//
// 上界跟着时钟走（当前年 +5）而不是写死一个常数：豆瓣按 tags 年份收录待映片，
// 明后年的榜单是有内容的，而一个写死的上界会在某一年突然把用户挡在外面。
func (s *MovieChartService) ValidateChartYear(year int) error {
	if s == nil {
		return errors.New("年度榜单服务不可用")
	}
	max := s.now().Year() + movieChartMaxYearAhead
	if year < movieChartMinYear || year > max {
		return fmt.Errorf("%w：%d（可用区间 %d–%d）", ErrMovieChartYearOutOfRange, year, movieChartMinYear, max)
	}
	return nil
}

// movieChartOrderClause 把排序枚举翻成 ORDER BY，非法值拒绝。
//
// 两条排序表达式都必须在 SQLite 与 Postgres 上给出**同一个顺序**：
//
//   - 默认排序 CASE WHEN release_date = 空串 THEN 1 ELSE 0 END, release_date, id。
//     未定档条目的 release_date 是空串，直接按它升序排会把空串排到最前（两个后端
//     都是），而裁决要的是「未定档置底」，所以先用 CASE 分两桶。日期是定长
//     YYYY-MM-DD 的 ASCII 字符串，字符串序即时间序，两个后端一致。
//   - 评分排序 CASE WHEN release_scope = 空串 THEN 1 ELSE 0 END, rating DESC, id。
//     没有评分的条目 rating 是 0，降序天然落到最后，不需要为它分桶；**详情未补全
//     的条目要另外分一桶置底**，见下。
//
// 「详情未补全置底」是需求设计文档 §3 那张表的一行（release_scope 为空串时
// 「上映信息待确认」，排序置底），没有按排序方式加限定，所以两种排序都得照办。
// 默认排序是顺带满足的——未补全的条目必然没有 release_date，落进同一个桶；
// 评分排序没有这层巧合，必须显式分桶。这不只是形式上的合规：未补全的条目还没有按内地公映口径判过，
// 详情落地后可能整条从榜单里消失，而它此刻显示的评分是**列表阶段**的值，把这样一条
// 顶到「按评分」的头部，等于把一个可能马上要撤回的结果推荐给用户。
//
// 注意这一桶只收 release_scope 为空串的条目：§3 同一张表里「theatrical 无 date」与
// 「undetermined」也写着排序置底，但它们已经判定过、可以留在榜单上，评分也是详情
// 阶段的真值，在「按评分」里照常参与排名。要改这条口径得回到那份文档。
//
// 末尾的 id 是**确定性兜底**：同一天上映、同一个评分的条目在两个后端上默认顺序
// 并不相同（PG 的堆表顺序与 SQLite 的 rowid 顺序无关），没有它，翻页会在两个后端
// 上给出不同的切分点，同一条目可能在第 1 页出现两次、也可能一次都不出现。
func movieChartOrderClause(sort string) (string, string, error) {
	switch sort {
	case MovieChartOrderRelease:
		return MovieChartOrderRelease, "CASE WHEN release_date = '' THEN 1 ELSE 0 END ASC, release_date ASC, id ASC", nil
	case MovieChartOrderRating:
		return MovieChartOrderRating, "CASE WHEN release_scope = '' THEN 1 ELSE 0 END ASC, rating DESC, id ASC", nil
	}
	return "", "", fmt.Errorf("%w：%q", ErrMovieChartOrderUnsupported, sort)
}

// ListPage 读一页榜单。**只查本地表、没有任何副作用**，任何情况下都不出网。
//
// showMarked 是界面上的「不过滤」开关，它只解除**标记造成的隐藏**（不想看 / 已看），
// **不**放宽内地公映口径：excluded 在两种开关下都不出现。这是需求设计文档 §8
// 「待用户确认」的第 2 条按设计定下的范围，改动它要回到那份文档。
//
// D-MC12 的「打开榜单页」触发点**不在这里**，在 EnsureYearRefreshed，由绑定层的
// OpenMovieChartYear 在挂载与切年份时调一次。P-005 初版把它挂在本方法上，评审给出
// 了两条理由：翻页、切排序、轮询状态条都会调 ListPage，没有一个是「打开了页面」；
// 而且取消一轮抓取之后 refreshDue 仍然判到期（取消不写 last_failure 也不写
// last_refreshed_at，§7 说取消不是失败），于是前端下一次为刷新状态条而发的读就把
// 刚被取消的那一轮原样重启——取消按钮等于没有。读接口保持无副作用，这两件事一起
// 消失，而 refreshDue 的条件一个字都不用改。
func (s *MovieChartService) ListPage(year int, sort string, page int, showMarked bool) (*MovieChartPage, error) {
	if s == nil {
		return nil, errors.New("年度榜单服务不可用")
	}
	if err := s.ValidateChartYear(year); err != nil {
		return nil, err
	}
	normalizedSort, order, err := movieChartOrderClause(sort)
	if err != nil {
		return nil, err
	}
	if page < 1 {
		return nil, fmt.Errorf("%w：%d", ErrMovieChartPageOutOfRange, page)
	}

	result := &MovieChartPage{
		Year:     year,
		Sort:     normalizedSort,
		Page:     page,
		PageSize: MovieChartPageSize,
		Items:    make([]MovieChartItemView, 0, MovieChartPageSize),
	}
	if err := s.chartQuery(year, showMarked).Count(&result.Total).Error; err != nil {
		return nil, fmt.Errorf("统计 %d 年榜单条目失败: %w", year, err)
	}

	var rows []models.MovieChartEntry
	// 重新构造一次查询而不是复用上面那个：GORM 的链式调用会把条件累积在同一个
	// Statement 上，Count 之后接着 Find 会带上 Count 留下的痕迹。
	//
	// 越过末页时 Offset 把结果扫空，Items 为空、Total 照旧是真实值，**不报错**
	// （需求设计文档 §7）：分页器要靠 Total 才能把用户拉回有内容的那一页。
	err = s.chartQuery(year, showMarked).
		Order(order).
		Offset((page - 1) * MovieChartPageSize).
		Limit(MovieChartPageSize).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("读取 %d 年榜单失败: %w", year, err)
	}

	marks, err := s.loadMarksFor(rows)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result.Items = append(result.Items, MovieChartItemView{
			DoubanID:      row.DoubanID,
			Title:         row.Title,
			OriginalTitle: row.OriginalTitle,
			CardSubtitle:  row.CardSubtitle,
			ReleaseDate:   row.ReleaseDate,
			ReleaseScope:  row.ReleaseScope,
			Rating:        row.Rating,
			RatingCount:   row.RatingCount,
			HasPoster:     row.PosterURL != "",
			Mark:          marks[row.DoubanID],
			DetailStatus:  row.DetailStatus,
			DetailError:   row.DetailError,
		})
	}

	cache, err := s.cacheState(year)
	if err != nil {
		return nil, err
	}
	result.Cache = cache
	backfill, err := s.backfillState(year)
	if err != nil {
		return nil, err
	}
	result.Backfill = backfill
	return result, nil
}

// chartQuery 是榜单主查询的过滤条件，走 idx_movie_chart_entry_list(year, release_scope)。
//
// 标记造成的隐藏用 NOT IN 子查询而不是 LEFT JOIN：条数统计与取页两处要用同一个
// 条件，子查询两边都能直接挂上；join 还会让 douban_id / id 这些列名在 ORDER BY 里
// 变得有歧义，两个后端的报错形态还不一样。
func (s *MovieChartService) chartQuery(year int, showMarked bool) *gorm.DB {
	query := s.db.Model(&models.MovieChartEntry{}).
		Where("year = ? AND release_scope IN ?", year, movieChartVisibleScopes)
	if showMarked {
		return query
	}
	hidden := s.db.Model(&models.MovieChartMark{}).Select("douban_id").Where("mark IN ?", movieChartHiddenMarks)
	return query.Where("douban_id NOT IN (?)", hidden)
}

// loadMarksFor 取这一页条目各自的标记，返回 豆瓣 ID → 标记 的映射。
//
// 单独查一次而不是 join 进主查询：主查询要分页，join 之后 ORDER BY 的列名需要限定，
// 而这一页最多 20 个 ID，一次 IN 查询的代价可以忽略。没有标记的条目不在映射里，
// 取出来是空串，正好是「未标记」。
func (s *MovieChartService) loadMarksFor(rows []models.MovieChartEntry) (map[string]string, error) {
	marks := make(map[string]string, len(rows))
	if len(rows) == 0 {
		return marks, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.DoubanID)
	}
	var found []models.MovieChartMark
	if err := s.db.Where("douban_id IN ?", ids).Find(&found).Error; err != nil {
		return nil, fmt.Errorf("读取榜单标记失败: %w", err)
	}
	for _, mark := range found {
		marks[mark.DoubanID] = mark.Mark
	}
	return marks, nil
}

// cacheState 把年状态表翻成页面顶部状态条要用的三个字段。
//
// 没有该年的状态行时返回零值：LastRefreshedAt 为 nil、失败码为空，界面显示的是
// 「还没抓过」而不是「失败」——这两件事不能合并（D-MC07）。
//
// Refreshing 只在跑的**正是这一年**时为真：刷新是按年起的，用户切到 2019 年时
// 不该看到 2026 年那一轮的进度。
func (s *MovieChartService) cacheState(year int) (MovieChartCacheState, error) {
	state, err := s.loadYearState(year)
	if err != nil {
		return MovieChartCacheState{}, err
	}
	refreshingYear, running := s.RefreshStatus()
	cache := MovieChartCacheState{Refreshing: running && refreshingYear == year}
	if state != nil {
		cache.LastRefreshedAt = state.LastRefreshedAt
		cache.LastFailure = state.LastFailure
	}
	return cache, nil
}

// backfillState 统计详情补全进度：pending + running 是还没落定的，Total 是该年
// 全部条目（含 excluded，理由见 MovieChartBackfill）。
func (s *MovieChartService) backfillState(year int) (MovieChartBackfill, error) {
	var pending int64
	err := s.db.Model(&models.MovieChartEntry{}).
		Where("year = ? AND detail_status IN ?", year, []string{
			models.MovieChartDetailPending,
			models.MovieChartDetailRunning,
		}).Count(&pending).Error
	if err != nil {
		return MovieChartBackfill{}, fmt.Errorf("统计 %d 年待补全条目失败: %w", year, err)
	}
	var total int64
	if err := s.db.Model(&models.MovieChartEntry{}).Where("year = ?", year).Count(&total).Error; err != nil {
		return MovieChartBackfill{}, fmt.Errorf("统计 %d 年条目总数失败: %w", year, err)
	}
	return MovieChartBackfill{Pending: int(pending), Total: int(total)}, nil
}

// ListWatched 返回已看页：按**影片上映年份**倒序分组，年份取标记时的快照
// （D-MC11）。
//
// 组内按标记时间倒序（最近标的在前），同一时刻标的按 id 倒序兜底——没有这个兜底，
// 批量标记出来的同秒记录在两个后端上的顺序不一样。
//
// 年份 0 是「年份未知」，排在最后：它是详情还没补全就被标了已看的条目，不猜年份
// （需求设计文档 §7）。倒序排列天然把 0 放在末尾。
func (s *MovieChartService) ListWatched() ([]WatchedMovieYearGroup, error) {
	if s == nil {
		return nil, errors.New("年度榜单服务不可用")
	}
	var rows []models.MovieChartMark
	err := s.db.Where("mark = ?", models.MovieChartMarkWatched).
		Order("release_year DESC, marked_at DESC, id DESC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("读取已看影片失败: %w", err)
	}
	groups := make([]WatchedMovieYearGroup, 0)
	for _, row := range rows {
		view := WatchedMovieView{
			DoubanID:  row.DoubanID,
			Title:     row.Title,
			HasPoster: row.PosterURL != "",
			MarkedAt:  row.MarkedAt,
		}
		// 行已经按年份倒序排好，所以只要和上一组比一次年份就够，不用先建 map 再排序。
		if len(groups) > 0 && groups[len(groups)-1].Year == row.ReleaseYear {
			groups[len(groups)-1].Items = append(groups[len(groups)-1].Items, view)
			continue
		}
		groups = append(groups, WatchedMovieYearGroup{Year: row.ReleaseYear, Items: []WatchedMovieView{view}})
	}
	return groups, nil
}

// ListYears 返回年份下拉的选项：本地有缓存的年份 ∪ {当前年}，倒序。
//
// 当前年总是在列表里，哪怕一条都没抓过：用户得能选中它并触发第一次抓取，否则
// 空库状态下下拉框是空的，永远点不出第一轮。
func (s *MovieChartService) ListYears() ([]int, error) {
	if s == nil {
		return nil, errors.New("年度榜单服务不可用")
	}
	var cached []int
	err := s.db.Model(&models.MovieChartEntry{}).
		Distinct("year").Order("year DESC").Pluck("year", &cached).Error
	if err != nil {
		return nil, fmt.Errorf("读取榜单年份失败: %w", err)
	}
	current := s.now().Year()
	years := make([]int, 0, len(cached)+1)
	inserted := false
	for _, year := range cached {
		if !inserted && year < current {
			years = append(years, current)
			inserted = true
		}
		if year == current {
			inserted = true
		}
		years = append(years, year)
	}
	if !inserted {
		years = append(years, current)
	}
	return years, nil
}
