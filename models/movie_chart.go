package models

import "time"

// 年度电影榜单的三张表。设计见
// docs/loopx/design/2026-09-21-movie-year-chart/需求设计文档.md §4。
//
// 三者之间、以及对既有表，都**没有外键**：标记表按豆瓣 ID 字符串关联条目缓存表，
// 是逻辑关联。这一条是承重的——榜单缓存会被整年重建（用户点刷新、换机器、清缓存），
// 缓存表的自增主键随之改变；若标记表拿外键指过去，一次缓存重建就会级联删掉用户
// 自己产生的「已看 / 想看 / 跳过」记录。没有外键也意味着这三张表不引入新的拓扑序
// 约束，因此直接追加在 AllModels() 末尾即可（见 models/schema.go 的说明）。

// 榜单条目的详情补全状态机取值。写回路径只认 running，其余状态下到达的结果一律丢弃。
const (
	MovieChartDetailPending   = "pending"
	MovieChartDetailRunning   = "running"
	MovieChartDetailSucceeded = "succeeded"
	MovieChartDetailFailed    = "failed"
)

// 上映范围：豆瓣 pubdate 判定算法的输出。
//
// 空串与 excluded 不是一回事，不能合并成一个零值：空串表示详情还没补全（界面显示
// 「上映信息待确认」，条目照常出现在榜单里），excluded 表示已经判定过、确定不在
// 内地院线上映（条目不进榜单）。
const (
	MovieChartScopeTheatrical   = "theatrical"
	MovieChartScopeUndetermined = "undetermined"
	MovieChartScopeExcluded     = "excluded"
)

// 用户对榜单条目的标记。空串表示未标记。
const (
	MovieChartMarkWant    = "want"
	MovieChartMarkSkip    = "skip"
	MovieChartMarkWatched = "watched"
)

// MovieChartEntry 是某一年榜单的本地缓存，一条对应豆瓣的一部电影。
//
// 这里的 gorm default 标签不触犯 2026-09-02 那条禁令。禁令管的是**默认 true 的布尔列
// 与默认非零的数值列**：GORM 把零值字段当未设置并替换成标签默认值，双向迁移器的
// Unscoped().Create 因此会把用户关掉的开关翻回 true。本表每个默认值都等于该列零值
// 该有的语义（空串、0、false、pending），替换后值不变，所以无害——反过来说，将来给
// 这里任何一列写上非零默认值都会踩中禁令。判断口径与 models/watchlist.go:33-39 一致，
// 由 models.TestMovieChartModelsHaveNoNonZeroNumericDefaults 逐字段守住。
type MovieChartEntry struct {
	// id 同时是 idx_movie_chart_entry_detail 的第二列：补全 worker 按状态过滤、
	// 按 id 升序认领，认领顺序才是确定的。与 idx_watchlist_enrichment 同形。
	ID uint `gorm:"primarykey;index:idx_movie_chart_entry_detail,priority:2" json:"id"`

	// DoubanID 是承重身份：列表阶段的 upsert 以它为冲突目标，标记表也按它关联。
	// 长度 16 对应 §2.1 的 ^[0-9]{1,16}$ 校验，不合规的响应直接判异常、不落库。
	DoubanID string `gorm:"size:16;not null;uniqueIndex:idx_movie_chart_entry_douban" json:"douban_id"`

	// Year 是抓取时用的 tags 年份，不是上映年份——两者可能不同（跨年上映、
	// 豆瓣改档），榜单按它分年，所以必须存请求参数而不是详情里的年份。
	// idx_movie_chart_entry_list(year, release_scope) 是榜单主查询的过滤路径。
	Year          int    `gorm:"not null;default:0;index:idx_movie_chart_entry_list,priority:1" json:"year"`
	Title         string `gorm:"size:200;not null;default:''" json:"title"`
	OriginalTitle string `gorm:"size:200;not null;default:''" json:"original_title"`
	// CardSubtitle 存豆瓣原样的副标题（「2026 / 中国大陆 / 纪录片 / 张凌赫」），
	// 前端直接展示，不再拆字段。
	CardSubtitle string `gorm:"type:text;not null;default:''" json:"card_subtitle"`

	ReleaseScope string `gorm:"size:16;not null;default:'';index:idx_movie_chart_entry_list,priority:2" json:"release_scope"`
	// ReleaseDate 是 YYYY-MM-DD 或空串（未定档）。存字符串不存 time：豆瓣的
	// pubdate 常常只到年份，落成时间类型就必须编一个月日出来。
	ReleaseDate string `gorm:"size:10;not null;default:''" json:"release_date"`
	// ReleasePubdate 存 pubdate 原文（\n 连接），供界面展示与事后审计判定是否正确。
	ReleasePubdate string `gorm:"type:text;not null;default:''" json:"release_pubdate"`
	IsReleased     bool   `gorm:"not null;default:false" json:"is_released"`

	Rating      float64 `gorm:"not null;default:0" json:"rating"`
	RatingCount int     `gorm:"not null;default:0" json:"rating_count"`

	// 多值列沿用仓库既有写法：\n 分隔，不建关联表。
	//
	// Cast 的列名 cast 是 SQL 保留字。建表与建索引由 GORM 生成、标识符都带引号，
	// 两个后端都建得出来；但将来手写 SQL 碰到这一列必须自己加引号（Postgres 用
	// "cast"，SQLite 用 `cast`），裸写会是语法错误。
	Countries string `gorm:"type:text;not null;default:''" json:"countries"`
	Genres    string `gorm:"type:text;not null;default:''" json:"genres"`
	Directors string `gorm:"type:text;not null;default:''" json:"directors"`
	Cast      string `gorm:"type:text;not null;default:''" json:"cast"`
	Overview  string `gorm:"type:text;not null;default:''" json:"overview"`
	// PosterURL 是豆瓣的远程地址，只给海报代理路由用，不下发给前端——前端拿到的是
	// HasPoster 布尔与代理路径。与 WatchlistEntry.PosterPath 同样的态度。
	PosterURL string `gorm:"type:text;not null;default:''" json:"-"`

	// 详情补全状态族。idx_movie_chart_entry_detail(detail_status, id) 是补全 worker
	// 取待办的访问路径。
	//
	// DetailClaim 存本次认领的随机标识。detail_status 会从 failed/succeeded 回到
	// pending（用户点刷新），只比对状态值区分不出「同一次认领」与「新一轮认领」，
	// 一次慢响应会落到重试后的新一轮上，所以写回必须同时比对状态与这个标识。
	//
	// 长度上限 32 是硬的：标识要用 hex.EncodeToString 于 16 字节得到的 32 个十六进制
	// 字符。**不得用 uuid.NewString()**——它是 36 字符，SQLite 不校验长度会默默存下，
	// Postgres 直接报 value too long（models/watchlist.go:52-55 记录的既有踩坑）。
	DetailStatus string `gorm:"size:16;not null;default:'pending';index:idx_movie_chart_entry_detail,priority:1" json:"detail_status"`
	DetailError  string `gorm:"size:32;not null;default:''" json:"detail_error"`
	DetailClaim  string `gorm:"size:32;not null;default:''" json:"-"`

	ListFetchedAt   *time.Time `json:"list_fetched_at" ts_type:"string"`
	DetailFetchedAt *time.Time `json:"detail_fetched_at" ts_type:"string"`

	CreatedAt time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt time.Time `json:"updated_at" ts_type:"string"`
}

// MovieChartMark 是用户对某部电影的标记，一个豆瓣 ID 同时只有一个标记。
//
// 按 DoubanID 关联而不是 MovieChartEntry.ID：缓存表的主键是本地自增值，缓存重建或
// 换机器后会变，豆瓣 ID 才是跨请求稳定的标识。Title / ReleaseYear / PosterURL 三列是
// **标记时的快照**，让已看页在缓存被清空、或该条目从豆瓣下架之后仍然完整可用——
// 已看记录是用户产生的事实，不能因为缓存变动而丢失。
//
// gorm default 禁令的核对同 MovieChartEntry：没有任何非零默认值的布尔或数值列。
type MovieChartMark struct {
	ID       uint   `gorm:"primarykey" json:"id"`
	DoubanID string `gorm:"size:16;not null;uniqueIndex:idx_movie_chart_mark_douban" json:"douban_id"`

	// idx_movie_chart_mark_group(mark, release_year) 是已看页的访问路径
	// （WHERE mark='watched' ORDER BY release_year DESC）。
	Mark        string `gorm:"size:16;not null;default:'';index:idx_movie_chart_mark_group,priority:1" json:"mark"`
	ReleaseYear int    `gorm:"not null;default:0;index:idx_movie_chart_mark_group,priority:2" json:"release_year"`
	Title       string `gorm:"size:200;not null;default:''" json:"title"`
	PosterURL   string `gorm:"type:text;not null;default:''" json:"-"`

	// WatchlistEntryID 非 0 表示这条想看片单记录是榜单建的，撤销标记时一并删除；
	// 0 表示撞名复用了已有条目、或这个标记根本不是 want。
	//
	// 有意只存一个裸 uint、不声明 belongs-to：声明了 GORM 会建外键，片单条目被用户
	// 删掉就会连坐删掉标记。跨边界的一致性靠「先建片单、再写标记」的固定顺序兜底，
	// 见需求设计文档 §5。
	WatchlistEntryID uint `gorm:"not null;default:0" json:"watchlist_entry_id"`

	MarkedAt  time.Time `gorm:"not null" json:"marked_at" ts_type:"string"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt time.Time `json:"updated_at" ts_type:"string"`
}

// MovieChartYearState 记录某一年榜单的抓取状态，一年一行。
//
// LastRefreshedAt 只在整轮列表阶段成功后才写，它驱动页面顶部状态条的三形态与
// 30 天过期判定；LastAttemptAt 每轮结束都写。两者分开，失败的一轮才不会把「上次
// 成功时间」推后、让过期的缓存看起来还新鲜。
type MovieChartYearState struct {
	ID   uint `gorm:"primarykey" json:"id"`
	Year int  `gorm:"not null;uniqueIndex:idx_movie_chart_year" json:"year"`

	LastRefreshedAt *time.Time `json:"last_refreshed_at" ts_type:"string"`
	LastAttemptAt   *time.Time `json:"last_attempt_at" ts_type:"string"`
	// LastFailure 存六类失败分类码，成功时清空。
	LastFailure string `gorm:"size:32;not null;default:''" json:"last_failure"`

	CreatedAt time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt time.Time `json:"updated_at" ts_type:"string"`
}
