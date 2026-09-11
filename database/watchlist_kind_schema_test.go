package database_test

import (
	"strings"
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

// assertWatchlistSchemaUpgraded 检查升级后的结构终态：补全字段族到位、复合唯一索引
// 与待办索引到位、旧的单列唯一索引已经退掉。
func assertWatchlistSchemaUpgraded(t *testing.T, db *gorm.DB) {
	t.Helper()
	migrator := db.Migrator()
	for _, column := range []string{
		"kind", "enrichment_status", "enrichment_error",
		"source_name", "source_item_id", "enrichment_claim", "enriched_at",
	} {
		if !migrator.HasColumn(&models.WatchlistEntry{}, column) {
			t.Fatalf("升级后缺少列 %s", column)
		}
	}
	if !migrator.HasIndex(&models.WatchlistEntry{}, "idx_watchlist_title_kind") {
		t.Fatal("升级后缺少 idx_watchlist_title_kind")
	}
	if !migrator.HasIndex(&models.WatchlistEntry{}, "idx_watchlist_enrichment") {
		t.Fatal("升级后缺少 idx_watchlist_enrichment")
	}
	if migrator.HasIndex(&models.WatchlistEntry{}, "idx_watchlist_title") {
		t.Fatal("旧的单列唯一索引应已删除，否则同名不同类型无法共存")
	}
	assertIndexColumnOrder(t, db, "idx_watchlist_title_kind", "title", "kind")
	assertIndexColumnOrder(t, db, "idx_watchlist_enrichment", "enrichment_status", "id")
}

// assertIndexColumnOrder 钉住索引的列顺序。
//
// idx_watchlist_title_kind 必须是 (title, kind)：services.watchlistTitleConflict 在
// SQLite 上认的是报错里的列清单 "watchlist_entries.title, watchlist_entries.kind"，
// 调成 (kind, title) 判据立刻失配，撞名会静默退化成一条通用数据库错误。
// idx_watchlist_enrichment 必须是 (enrichment_status, id)：后台 worker 按状态过滤、
// 按 id 升序认领，列反过来这条访问路径就用不上索引了。
func assertIndexColumnOrder(t *testing.T, db *gorm.DB, index string, want ...string) {
	t.Helper()
	query := "SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?"
	if dbtest.IsPostgres() {
		// 必须限定 schema：dbtest 给每个测试建一个独立 schema，索引名在库里不唯一，
		// 不加这一句会读到别的测试那份同名索引。
		query = "SELECT indexdef FROM pg_indexes WHERE indexname = ? AND schemaname = current_schema()"
	}
	var definition string
	if err := db.Raw(query, index).Scan(&definition).Error; err != nil {
		t.Fatalf("读取 %s 的定义失败: %v", index, err)
	}
	// 只看最后一对括号里的列清单：索引名本身也含有列名，整串匹配会假通过。
	open, closing := strings.LastIndex(definition, "("), strings.LastIndex(definition, ")")
	if open < 0 || closing < open {
		t.Fatalf("%s 的定义取不到列清单: %q", index, definition)
	}
	columns := definition[open+1 : closing]
	position := -1
	for _, column := range want {
		at := strings.Index(columns, column)
		if at <= position {
			t.Fatalf("%s 的列顺序必须是 %v: %q", index, want, columns)
		}
		position = at
	}
}

// assertTitleKindUniqueness 验证唯一键确实是 (title, kind)：同名不同类型可以共存，
// 同名同类型仍被数据库拒绝。用完即删，不影响调用方的行数断言。
func assertTitleKindUniqueness(t *testing.T, db *gorm.DB, title string) {
	t.Helper()
	movie := models.WatchlistEntry{Title: title, Kind: models.WatchlistKindMovie, EnrichmentStatus: models.WatchlistEnrichmentPending}
	if err := db.Create(&movie).Error; err != nil {
		t.Fatalf("新建条目失败: %v", err)
	}
	tv := models.WatchlistEntry{Title: title, Kind: models.WatchlistKindTV, EnrichmentStatus: models.WatchlistEnrichmentPending}
	if err := db.Create(&tv).Error; err != nil {
		t.Fatalf("同名不同类型应可共存: %v", err)
	}
	dup := models.WatchlistEntry{Title: title, Kind: models.WatchlistKindTV, EnrichmentStatus: models.WatchlistEnrichmentPending}
	if err := db.Create(&dup).Error; err == nil {
		t.Fatal("同名同类型应被唯一索引拒绝")
	}
	if err := db.Delete(&models.WatchlistEntry{}, []uint{movie.ID, tv.ID}).Error; err != nil {
		t.Fatalf("清理检查数据失败: %v", err)
	}
}

// 已经是复合唯一的库（S3）：升级必须是空转。
//
// 夹具里必须有一对同名不同类型的记录，这是这条用例存在的全部理由：退役前的去重函数
// 按 title 单键判重并硬删（WatchlistEntry 没有 DeletedAt），一旦守卫失效放行，被删掉
// 的正是这一对里较晚的那条。夹具若用互不相同的标题构造，用例会永远绿而什么都抓不住。
func TestWatchlistUpgradeOnCompositeUniqueLibraryIsNoop(t *testing.T) {
	db := dbtest.Open(t)
	seed := []models.WatchlistEntry{
		{
			Title: "沙丘", Kind: models.WatchlistKindMovie,
			EnrichmentStatus: models.WatchlistEnrichmentSucceeded,
			SourceName:       "tmdb", SourceItemID: "438631",
			Year: 2021, Rating: 8.1, Overview: "厄拉科斯", Genres: "科幻", Credits: "维伦纽瓦",
		},
		// 同名不同类型——退役失败的去重会把这一条硬删掉。
		{Title: "沙丘", Kind: models.WatchlistKindTV, EnrichmentStatus: models.WatchlistEnrichmentManual},
		{Title: "沙丘（1984）", Kind: models.WatchlistKindMovie, EnrichmentStatus: models.WatchlistEnrichmentPending},
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatal(err)
	}
	before := listWatchlist(t, db)
	for i := 0; i < 2; i++ {
		if err := database.ApplySchema(db); err != nil {
			t.Fatalf("第 %d 次 ApplySchema 失败: %v", i+1, err)
		}
		after := listWatchlist(t, db)
		if len(after) != len(before) {
			t.Fatalf("第 %d 次升级改变了行数: got=%d want=%d %+v", i+1, len(after), len(before), after)
		}
		for index, entry := range after {
			want := before[index]
			if entry.ID != want.ID || entry.Title != want.Title || entry.Kind != want.Kind ||
				entry.EnrichmentStatus != want.EnrichmentStatus || entry.SourceName != want.SourceName ||
				entry.SourceItemID != want.SourceItemID || entry.Year != want.Year ||
				entry.Overview != want.Overview || entry.Genres != want.Genres ||
				entry.Credits != want.Credits || entry.Rating != want.Rating ||
				entry.CreatedAt.UnixMicro() != want.CreatedAt.UnixMicro() {
				t.Fatalf("第 %d 次升级改动了内容: got=%+v want=%+v", i+1, entry, want)
			}
		}
	}
	assertWatchlistSchemaUpgraded(t, db)
	assertTitleKindUniqueness(t, db, "共存检查")
}

// 已经有 kind 列、但两个索引都不在的库：手工删过索引，或上一次升级在建索引之前
// 中断，都会落到这里。去重函数必须照样跳过——它的前置条件是"还没有 kind 列"，
// 而不是"没有某个索引"。守卫若只看索引，这里会按 title 单键硬删掉同名不同类型的行。
func TestWatchlistUpgradeWithKindColumnButNoIndexes(t *testing.T) {
	db := dbtest.Open(t)
	for _, index := range []string{"idx_watchlist_title_kind", "idx_watchlist_enrichment"} {
		if err := db.Migrator().DropIndex(&models.WatchlistEntry{}, index); err != nil {
			t.Fatalf("删 %s 失败: %v", index, err)
		}
	}
	seed := []models.WatchlistEntry{
		{Title: "沙丘", Kind: models.WatchlistKindMovie, EnrichmentStatus: models.WatchlistEnrichmentSucceeded},
		{Title: "沙丘", Kind: models.WatchlistKindTV, EnrichmentStatus: models.WatchlistEnrichmentManual},
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := database.ApplySchema(db); err != nil {
			t.Fatalf("第 %d 次 ApplySchema 失败: %v", i+1, err)
		}
		entries := listWatchlist(t, db)
		if len(entries) != len(seed) {
			t.Fatalf("第 %d 次升级删了行: got=%+v", i+1, entries)
		}
		for index, entry := range entries {
			if entry.ID != seed[index].ID || entry.Kind != seed[index].Kind ||
				entry.EnrichmentStatus != seed[index].EnrichmentStatus {
				t.Fatalf("第 %d 次升级改了内容: got=%+v want=%+v", i+1, entry, seed[index])
			}
		}
	}
	assertWatchlistSchemaUpgraded(t, db)
}

// 省略 Kind / EnrichmentStatus 的写入必须落成 movie / pending，两条路都要成立：
// GORM 把标签里的字面默认值解析成 DefaultValueInterface，Create 时用它填零值字段
// （连结构体一起回填）；绕开 GORM 的裸 SQL 则由列默认值兜住。
//
// 这条用例钉的是"kind 不可能是空串"这个前提——migrateWatchlistKind 的第一步正是
// 建立在它之上，而 models/watchlist.go 的注释也据此断言字符串默认值是安全的。
func TestWatchlistDefaultsApplyOnInsert(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	entry := models.WatchlistEntry{Title: "只给片名"}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if entry.Kind != models.WatchlistKindMovie || entry.EnrichmentStatus != models.WatchlistEnrichmentPending {
		t.Fatalf("Create 应把结构体零值回填成 movie/pending: %+v", entry)
	}
	if err := db.Exec("INSERT INTO watchlist_entries (title, created_at, updated_at) VALUES (?, ?, ?)",
		"裸 SQL", entry.CreatedAt, entry.UpdatedAt).Error; err != nil {
		t.Fatalf("裸 SQL 插入失败: %v", err)
	}
	var rows []models.WatchlistEntry
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("应有两行: %+v", rows)
	}
	for _, row := range rows {
		if row.Kind != models.WatchlistKindMovie || row.EnrichmentStatus != models.WatchlistEnrichmentPending {
			t.Fatalf("落库值应为 movie/pending: %+v", row)
		}
	}
}

// 空库（新装）：AutoMigrate 直接建出终态，迁移空转两次都不报错。
func TestWatchlistSchemaOnFreshDatabase(t *testing.T) {
	db := dbtest.OpenRaw(t)
	for i := 0; i < 2; i++ {
		if err := database.ApplySchema(db); err != nil {
			t.Fatalf("第 %d 次 ApplySchema 失败: %v", i+1, err)
		}
	}
	assertWatchlistSchemaUpgraded(t, db)
	assertTitleKindUniqueness(t, db, "沙丘")
}
