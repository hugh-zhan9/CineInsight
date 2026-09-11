package models

import "time"

// 想看条目的类型维度。它只属于片单：Video / Jellyfin / NFO 一侧不引入这个字段。
// 类型决定在线补全走哪个资料源，也让同名不同类型的条目可以共存。
const (
	WatchlistKindMovie = "movie"
	WatchlistKindTV    = "tv"
	WatchlistKindAnime = "anime"
	WatchlistKindShow  = "show"
	WatchlistKindAV    = "av"
)

// 在线补全的状态机取值。写回路径只认 running，其余状态下到达的结果一律丢弃。
// manual 表示用户手工维护过，补全不再覆盖它——存量条目升级后也落在这里，
// 免得一次升级就触发整库出网。
const (
	WatchlistEnrichmentPending   = "pending"
	WatchlistEnrichmentRunning   = "running"
	WatchlistEnrichmentSucceeded = "succeeded"
	WatchlistEnrichmentFailed    = "failed"
	WatchlistEnrichmentManual    = "manual"
)

// WatchlistEntry 是手工维护的想看片名，不依赖本地媒体文件。
//
// 唯一键是 (title, kind) 而不是 title：同名不同类型（例如剧版与影版）是两条
// 独立的记录。索引名与列顺序都是承重的——services.watchlistTitleConflict 靠
// 报错里出现的索引名/列清单把撞名翻译成「该片名已在想看片单中」，改名或调列序
// 会让撞名静默退化成一条通用数据库错误。
//
// 这里的 gorm default 标签不触犯 2026-09-02 那条禁令。禁令管的是**默认 true 的
// 布尔列与默认非零的数值列**：GORM 会把零值字段当未设置并替换成标签默认值，
// 双向迁移器的 Unscoped().Create 因此会把用户关掉的开关翻回 true。本表每个默认值
// 都等于该列零值该有的语义（空串、0、movie、pending），替换后值不变，所以无害——
// 反过来说，将来给这里任何一列写上非零默认值（比如 default:50）都会踩中禁令。
// 替换行为实测见 database/watchlist_kind_schema_test.go 的
// TestWatchlistDefaultsApplyOnInsert。
type WatchlistEntry struct {
	ID    uint   `gorm:"primarykey;index:idx_watchlist_enrichment,priority:2" json:"id"`
	Title string `gorm:"size:200;not null;uniqueIndex:idx_watchlist_title_kind,priority:1" json:"title"`
	Kind  string `gorm:"size:16;not null;default:'movie';uniqueIndex:idx_watchlist_title_kind,priority:2" json:"kind"`

	// 补全状态族。idx_watchlist_enrichment(enrichment_status, id) 是后台 worker
	// 取待办的访问路径，按 id 升序让认领顺序确定。
	//
	// EnrichmentClaim 存本次认领的随机标识：状态可以回到 pending（用户重试），
	// 只比对状态值不足以区分「同一次认领」与「新一轮认领」，一次慢响应会落到
	// 重试后的新一轮上。写回时必须同时比对状态与这个标识。
	//
	// 长度上限 32 是硬的：标识要用 32 个十六进制字符（hex.EncodeToString 于 16 字节）
	// 这类写法。uuid.NewString() 是 36 字符，SQLite 不校验长度会默默存下，Postgres
	// 直接报 value too long——正是 internal/dbtest 要拦的那类「改 A 坏 B」。
	EnrichmentStatus string     `gorm:"size:16;not null;default:'pending';index:idx_watchlist_enrichment,priority:1" json:"enrichment_status"`
	EnrichmentError  string     `gorm:"size:32;not null;default:''" json:"enrichment_error"`
	SourceName       string     `gorm:"size:16;not null;default:''" json:"source_name"`
	SourceItemID     string     `gorm:"size:64;not null;default:''" json:"source_item_id"`
	EnrichmentClaim  string     `gorm:"size:32;not null;default:''" json:"-"`
	EnrichedAt       *time.Time `json:"enriched_at" ts_type:"string"`

	// 补全结果，用户可改。Year 为 0、Rating 为 0 表示未知；PosterPath 是托管
	// 图片目录下的相对路径，对外经 /preview/watchlist-poster/<id> 取图。
	Year       int     `gorm:"not null;default:0" json:"year"`
	Overview   string  `gorm:"type:text;not null;default:''" json:"overview"`
	Genres     string  `gorm:"type:text;not null;default:''" json:"genres"`
	Credits    string  `gorm:"type:text;not null;default:''" json:"credits"`
	Rating     float64 `gorm:"not null;default:0" json:"rating"`
	PosterPath string  `gorm:"type:text;not null;default:''" json:"-"`

	CreatedAt time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt time.Time `json:"updated_at" ts_type:"string"`
}
