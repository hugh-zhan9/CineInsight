// 外部测试包，与 schema_ext_test.go 同因：dbtest 依赖 database，database 的内部测试
// 再依赖 dbtest 会形成导入环。这里验证 ApplySchema 之后年度电影榜单三张表的真实形态。
package database_test

import (
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

// movieChartIndexDefinition 取索引的建表语句原文，索引不存在时直接 Fatal。
func movieChartIndexDefinition(t *testing.T, db *gorm.DB, index string) string {
	t.Helper()
	query := "SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?"
	if dbtest.IsPostgres() {
		// 必须限定 schema：dbtest 给每个测试建一个独立 schema，索引名在库里不唯一，
		// 不加这一句会读到别的测试那份同名索引。
		query = "SELECT indexdef FROM pg_indexes WHERE indexname = ? AND schemaname = current_schema()"
	}
	var definition string
	if err := db.Raw(query, index).Scan(&definition).Error; err != nil {
		t.Fatalf("读取索引 %s 的定义失败(%s): %v", index, dbtest.Backend(), err)
	}
	if strings.TrimSpace(definition) == "" {
		t.Fatalf("索引 %s 不存在(%s)", index, dbtest.Backend())
	}
	return definition
}

// assertMovieChartIndex 钉住索引的唯一性与**精确**的列清单。
//
// 刻意比 assertIndexColumnOrder（watchlist_kind_schema_test.go）严一档：那个只看
// 列名出现的先后，(year, release_scope) 与 (year, release_scope, id) 在它眼里都算
// 通过。榜单这几个索引的列清单本身就是访问路径的契约，多一列少一列都不是设计里
// 的那一个，所以逐个位置比。
func assertMovieChartIndex(t *testing.T, db *gorm.DB, index string, unique bool, want ...string) {
	t.Helper()
	definition := movieChartIndexDefinition(t, db, index)
	if isUnique := strings.Contains(strings.ToUpper(definition), "UNIQUE"); isUnique != unique {
		t.Fatalf("%s 的唯一性应为 %v(%s): %q", index, unique, dbtest.Backend(), definition)
	}
	// 只看最后一对括号里的列清单：索引名本身也含列名，整串匹配会假通过。
	open, closing := strings.LastIndex(definition, "("), strings.LastIndex(definition, ")")
	if open < 0 || closing < open {
		t.Fatalf("%s 的定义取不到列清单(%s): %q", index, dbtest.Backend(), definition)
	}
	got := strings.Split(definition[open+1:closing], ",")
	for position, column := range got {
		got[position] = strings.Trim(strings.TrimSpace(column), "`\"[]")
	}
	if len(got) != len(want) {
		t.Fatalf("%s 的列清单必须是 %v(%s)，实际 %v", index, want, dbtest.Backend(), got)
	}
	for position := range want {
		if got[position] != want[position] {
			t.Fatalf("%s 的列清单必须是 %v(%s)，实际 %v", index, want, dbtest.Backend(), got)
		}
	}
}

// 三张表与六个索引在两个后端上都必须建得出来，且重复运行安全。
//
// 索引名是承重的：后续切片的 upsert 以 idx_movie_chart_entry_douban /
// idx_movie_chart_mark_douban / idx_movie_chart_year 为冲突目标，改名会让 upsert
// 退化成插入并撞唯一键。列清单同样承重，它就是榜单主查询与补全 worker 的访问路径。
func TestMovieChartSchemaOnBothBackends(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	// 幂等：老库升级会再跑一遍。
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重复建立 schema 应当幂等(%s): %v", dbtest.Backend(), err)
	}

	for _, model := range []interface{}{
		&models.MovieChartEntry{}, &models.MovieChartMark{}, &models.MovieChartYearState{},
	} {
		if !db.Migrator().HasTable(model) {
			t.Fatalf("%T 的表缺失(%s)", model, dbtest.Backend())
		}
	}

	assertMovieChartIndex(t, db, "idx_movie_chart_entry_douban", true, "douban_id")
	assertMovieChartIndex(t, db, "idx_movie_chart_entry_list", false, "year", "release_scope")
	assertMovieChartIndex(t, db, "idx_movie_chart_entry_detail", false, "detail_status", "id")
	assertMovieChartIndex(t, db, "idx_movie_chart_mark_douban", true, "douban_id")
	assertMovieChartIndex(t, db, "idx_movie_chart_mark_group", false, "mark", "release_year")
	assertMovieChartIndex(t, db, "idx_movie_chart_year", true, "year")
}

// 三个唯一键必须真的挡住重复，而不只是名字对得上。
//
// 同一条里顺带把 cast 这一列写进去再读回来：cast 是 SQL 保留字，两个后端上
// 建表与读写都得带引号才成立，只看 HasTable 是发现不了的。
func TestMovieChartUniqueKeysRejectDuplicates(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	now := time.Now()

	// detail_claim 存满 32 个十六进制字符：SQLite 不校验长度，Postgres 会在超长时
	// 报 value too long，这一列的上限必须容得下认领标识本身。
	claim := strings.Repeat("ab", 16)
	entry := models.MovieChartEntry{
		DoubanID: "36191693", Year: 2026, Title: "挽救计划",
		CardSubtitle: "2026 / 中国大陆 / 剧情", ReleaseScope: models.MovieChartScopeTheatrical,
		ReleaseDate: "2026-03-20", ReleasePubdate: "2026-03-20(美国/中国大陆)",
		Directors: "甲", Cast: "乙\n丙", Countries: "中国大陆",
		DetailStatus: models.MovieChartDetailRunning, DetailClaim: claim,
		ListFetchedAt: &now,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("写入榜单条目失败(%s): %v", dbtest.Backend(), err)
	}
	var loaded models.MovieChartEntry
	if err := db.First(&loaded, entry.ID).Error; err != nil {
		t.Fatalf("读回榜单条目失败(%s): %v", dbtest.Backend(), err)
	}
	if loaded.Cast != "乙\n丙" || loaded.DetailClaim != claim || loaded.ReleaseDate != "2026-03-20" {
		t.Fatalf("条目内容应原样读回(%s): %+v", dbtest.Backend(), loaded)
	}
	if err := db.Create(&models.MovieChartEntry{
		DoubanID: "36191693", Year: 2026, Title: "重复",
		DetailStatus: models.MovieChartDetailPending,
	}).Error; err == nil {
		t.Fatalf("同一个豆瓣 ID 的第二条应被唯一索引拒绝(%s)", dbtest.Backend())
	}
	if err := db.Create(&models.MovieChartEntry{
		DoubanID: "36191694", Year: 2026, Title: "另一部",
		DetailStatus: models.MovieChartDetailPending,
	}).Error; err != nil {
		t.Fatalf("不同豆瓣 ID 应可共存(%s): %v", dbtest.Backend(), err)
	}

	mark := models.MovieChartMark{
		DoubanID: "36191693", Mark: models.MovieChartMarkWatched, ReleaseYear: 2026,
		Title: "挽救计划", MarkedAt: now,
	}
	if err := db.Create(&mark).Error; err != nil {
		t.Fatalf("写入标记失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Create(&models.MovieChartMark{
		DoubanID: "36191693", Mark: models.MovieChartMarkWant, MarkedAt: now,
	}).Error; err == nil {
		t.Fatalf("一个豆瓣 ID 同时只能有一个标记(%s)", dbtest.Backend())
	}

	state := models.MovieChartYearState{Year: 2026, LastAttemptAt: &now}
	if err := db.Create(&state).Error; err != nil {
		t.Fatalf("写入年状态失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Create(&models.MovieChartYearState{Year: 2026}).Error; err == nil {
		t.Fatalf("同一年的第二条应被唯一索引拒绝(%s)", dbtest.Backend())
	}
	if err := db.Create(&models.MovieChartYearState{Year: 2025}).Error; err != nil {
		t.Fatalf("不同年份应可共存(%s): %v", dbtest.Backend(), err)
	}
}

// 三张表之间、以及对既有表都不许有外键，数据库这一侧也要能证明。
//
// 标记按豆瓣 ID 字符串逻辑关联条目缓存：缓存重建（换机器、清缓存、整年重抓）会
// 换掉 movie_chart_entries 的自增主键，若是强制外键，一次重建就会级联删掉用户自己
// 产生的"已看/想看/跳过"。watchlist_entry_id 同理——用户在片单里删掉一条记录，
// 不能连坐删掉榜单标记。所以这两种"悬空引用"必须能写进去。
func TestMovieChartMarkHasNoForeignKeyToEntryOrWatchlist(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	// 缓存表里根本没有这个豆瓣 ID，片单里也没有这个条目 ID。
	if err := db.Create(&models.MovieChartMark{
		DoubanID: "99999999", Mark: models.MovieChartMarkWatched, ReleaseYear: 2019,
		Title: "缓存已清空的老片", WatchlistEntryID: 987654, MarkedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("标记不得受外键约束(%s): %v", dbtest.Backend(), err)
	}

	// 反向也要成立：删掉缓存条目，标记还在。
	entry := models.MovieChartEntry{DoubanID: "12345678", Year: 2026, Title: "会被清掉的缓存", DetailStatus: models.MovieChartDetailPending}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("写入榜单条目失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Create(&models.MovieChartMark{
		DoubanID: "12345678", Mark: models.MovieChartMarkWant, ReleaseYear: 2026,
		Title: "会被清掉的缓存", MarkedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("写入标记失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Delete(&models.MovieChartEntry{}, entry.ID).Error; err != nil {
		t.Fatalf("删除榜单条目失败(%s): %v", dbtest.Backend(), err)
	}
	// 先确认缓存条目**真的**没了再看标记：MovieChartEntry 今天没有 gorm.DeletedAt，
	// 所以上面那一句是物理 DELETE，级联真要存在就会在这时候触发。哪天有人给缓存表
	// 加上软删除（本仓库很常见），它就变成 UPDATE，任何级联都不可能触发，下面
	// "标记还在" 就成了空过——用 Unscoped 计数把这个前提钉死。
	var remaining int64
	if err := db.Unscoped().Model(&models.MovieChartEntry{}).Where("id = ?", entry.ID).Count(&remaining).Error; err != nil {
		t.Fatalf("统计榜单条目失败(%s): %v", dbtest.Backend(), err)
	}
	if remaining != 0 {
		t.Fatalf("缓存条目必须被物理删除，这条用例才在验证级联(%s): count=%d", dbtest.Backend(), remaining)
	}
	var marks int64
	if err := db.Model(&models.MovieChartMark{}).Where("douban_id = ?", "12345678").Count(&marks).Error; err != nil {
		t.Fatalf("统计标记失败(%s): %v", dbtest.Backend(), err)
	}
	if marks != 1 {
		t.Fatalf("清掉缓存条目不得连坐删掉用户标记(%s): count=%d", dbtest.Backend(), marks)
	}
}
