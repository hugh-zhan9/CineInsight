package services

import "context"

// 年度电影榜单的数据源适配器合同（D-MC01）。
//
// 依赖方向是单向的：MovieChartService → 本合同 → 各适配器 → net/http。本文件与
// 各适配器实现**不碰数据库、不反向依赖 MovieChartService**——落库、增量 upsert、
// 补全状态机和标记都归 MovieChartService。适配器只做两件事：向豆瓣要数据，把响应
// 映射成这里的内部结构。
//
// 这条边界不是新立的，是 services/watchlist_metadata_source.go 开头那条的延用：
// 出网客户端一律由构造方注入（来自 NewWatchlistMetadataHTTPClient），失败一律用
// WatchlistMetadataSourceError 的六类分类码表达，适配器自己不建 http.Client、
// 不写库、不认识后台任务。
//
// 端点契约见 docs/loopx/design/2026-09-21-movie-year-chart/需求设计文档.md §2，
// 端点的实测行为（哪些字段可信、哪些不可信）见同目录 概要设计.md §2。

// 豆瓣榜单端点的四种排序。服务端自述 T=综合、U=近期热度、R=首映时间、S=高分优先，
// 且**只有降序**——「旧片在前」只能靠本地按 pubdate 重排，服务端给不了（概要设计 §2）。
//
// 单一排序拿不到一个可用的年度榜单：实测 tags=2024&sort=R 全年只能回溯到 11-22，
// 而 sort=R 与其余三种零重叠。因此 D-MC03 的覆盖策略是四种排序各翻满页再按豆瓣 ID
// 合并去重。合并去重由 MovieChartService 在库里做，适配器只负责单个排序的翻页。
const (
	MovieChartSortComprehensive = "T"
	MovieChartSortRecentHeat    = "U"
	MovieChartSortReleaseDate   = "R"
	MovieChartSortRating        = "S"
)

// MovieChartSorts 返回 D-MC03 声明的全部排序，顺序即抓取顺序。
// 调用方从这里取值，不要自己写字面量——排序集合变化时只改这一处。
func MovieChartSorts() []string {
	return []string{MovieChartSortComprehensive, MovieChartSortRecentHeat, MovieChartSortReleaseDate, MovieChartSortRating}
}

const (
	// MovieChartListPageSize 是列表请求的 count 参数，同时是翻页步长。
	MovieChartListPageSize = 20
	// MovieChartPagesPerSort 是每个排序**固定**翻的页数（start = 0, 20, …, 480）。
	//
	// 这个数字是硬编码的，不由响应决定，原因有两条实测依据（概要设计 §2）：
	//   - 响应里的 total 恒为 500 的占位值，不是真实计数，用它算分页只会算错；
	//   - start=500 起恒返回 0 条，而 500 以内的空页是**瞬时现象**（同一个 start
	//     密集请求时返回 0 条，隔一会重试连续三次都是 20 条）。拿空页当终止条件
	//     会随机漏掉整段数据。
	// 代价是每个排序最多浪费几次空请求，换来的是确定性。
	MovieChartPagesPerSort = 25
)

// MovieChartListItem 是列表阶段能拿到的一条电影。
//
// 这里**没有上映日期**：列表项只有年份，日期只能来自详情端点（概要设计 §2）。
// 也没有 Year——条目归到哪一年由请求参数决定（MovieChartEntry.Year 存的是抓取时
// 用的 tags 年份），响应里的 year 只用于校验，与请求年份不符的条目在适配器内就被
// 丢掉并计数，不会出现在这里。
type MovieChartListItem struct {
	// DoubanID 是承重身份：upsert 的冲突目标、标记表的关联键、海报代理的路径段。
	DoubanID string
	Title    string
	// CardSubtitle 是豆瓣原样的副标题（「2026 / 中国大陆 / 纪录片 / 张凌赫」），
	// 已含产地、类型与主创，前端直接展示，不在这里拆字段。
	CardSubtitle string
	Rating       float64
	RatingCount  int
	// PosterURL 是豆瓣图床的绝对地址。图床有防盗链（无 Referer 418、错 Referer 403），
	// 前端不能直连，取图归海报代理路由；适配器只负责把地址原样带出来。
	PosterURL string
}

// MovieChartListPage 是一页列表的映射结果。
//
// Items 为空**不是**失败，也不是「翻到底了」：它是实测过的瞬时现象，调用方应当继续
// 翻下一页（D-MC07）。两个 Skipped 计数用于让「这一页少了几条、为什么少」在日志与
// 刷新摘要里可见，不用去翻响应正文。
type MovieChartListPage struct {
	Sort  string
	Start int
	Items []MovieChartListItem
	// SkippedNonMovie 是 type 不是 movie 的条目数（端点会混入剧集等）。
	SkippedNonMovie int
	// SkippedOtherYear 是 year 与请求年份**不同**的条目数。豆瓣的 tags 过滤不保证严格。
	SkippedOtherYear int
	// KeptWithoutYear 是响应里没给年份、按请求年份收下的条目数。
	//
	// 与 SkippedOtherYear 分开计：「没给年份」不是「年份不符」，把它一起丢掉等于
	// 凭空少一部片。这个计数为正说明响应形态与预期有出入，值得在刷新摘要里露出来。
	KeptWithoutYear int
}

// MovieChartListVisitor 每翻到一页就被调用一次，按请求顺序。
//
// 返回错误立即中止本次翻页并原样上抛：调用方用它表达「取消」与「落库失败」，
// 适配器不解释这个错误，也不会替调用方重试。
type MovieChartListVisitor func(page MovieChartListPage) error

// MovieChartDetail 是详情阶段补全的字段。
//
// ReleaseScope / ReleaseDate 是 pubdate 判定算法（需求设计文档 §3）的输出，取值为
// models.MovieChartScope* 三者之一；ReleasePubdate 存 pubdate 原文，供界面展示与
// 事后审计「这条判定对不对」。多值字段在这里是切片，拼成 \n 分隔的列是落库方的事。
type MovieChartDetail struct {
	DoubanID       string
	Title          string
	OriginalTitle  string
	ReleaseScope   string
	ReleaseDate    string
	ReleasePubdate string
	IsReleased     bool
	Rating         float64
	RatingCount    int
	Countries      []string
	Genres         []string
	Directors      []string
	Cast           []string
	Overview       string
}

// MovieChartSource 是年度榜单的外部数据源适配器。
//
// 失败一律返回 *WatchlistMetadataSourceError（用 WatchlistMetadataFailureOf 取分类码），
// 六类互不合并——凭证、代理、网络错误**绝不**能变成「查无数据」，否则页面会把一次
// 配置问题渲染成「这一年没有电影」。
type MovieChartSource interface {
	// Name 是写进日志与失败记录的源名。
	Name() string

	// ListYear 按 (年份, 排序) 固定翻 MovieChartPagesPerSort 页，每页回调一次 visit。
	// 任何一页请求失败即中止并返回带分类码的错误——已经回调过的页由调用方自行保留
	// （D-MC04：增量 upsert，从不删除）。
	ListYear(ctx context.Context, year int, sort string, visit MovieChartListVisitor) error

	// Detail 按豆瓣 ID 取详情并完成上映判定。
	Detail(ctx context.Context, doubanID string) (*MovieChartDetail, error)
}
