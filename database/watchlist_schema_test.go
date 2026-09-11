package database_test

import (
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

// legacyWatchlistEntry 复刻加入 kind 之前的表结构：只有片名，没有类型与补全状态，
// 也没有任何索引。升级路径的测试必须从这个形状出发——用当前模型建表再删索引是
// 造不出"老库"的，那样 kind 列已经在了。
type legacyWatchlistEntry struct {
	ID        uint   `gorm:"primarykey"`
	Title     string `gorm:"size:200;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (legacyWatchlistEntry) TableName() string { return "watchlist_entries" }

func openLegacyWatchlistDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := dbtest.OpenRaw(t)
	if err := db.AutoMigrate(&legacyWatchlistEntry{}); err != nil {
		t.Fatalf("建老表失败: %v", err)
	}
	return db
}

func listWatchlist(t *testing.T, db *gorm.DB) []models.WatchlistEntry {
	t.Helper()
	var entries []models.WatchlistEntry
	if err := db.Order("id").Find(&entries).Error; err != nil {
		t.Fatalf("读取想看片单失败: %v", err)
	}
	return entries
}

// 从未做过唯一性迁移的老库（S1）：表里有重复片名，升级时必须合并掉，保留最早的一条。
//
// 这条用例同时守住"去重函数不再自己建索引"：它若还调 CreateIndex(idx_watchlist_title)，
// 模型里已经没有这个索引名，GORM 会直接返回 failed to create index，ApplySchema 报错，
// 这里第一次调用就会炸。
func TestWatchlistUpgradeFromLegacyTableDeduplicatesTitles(t *testing.T) {
	db := openLegacyWatchlistDB(t)
	old := []legacyWatchlistEntry{{Title: "沙丘"}, {Title: "  沙丘  "}, {Title: "沙丘（1984）"}}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := database.ApplySchema(db); err != nil {
			t.Fatalf("第 %d 次 ApplySchema 失败: %v", i+1, err)
		}
	}
	entries := listWatchlist(t, db)
	if len(entries) != 2 {
		t.Fatalf("重复片名应合并成两条: %+v", entries)
	}
	if entries[0].ID != old[0].ID || entries[0].Title != "沙丘" || entries[1].ID != old[2].ID {
		t.Fatalf("应保留最早的一条并去掉首尾空白: %+v", entries)
	}
	if entries[0].CreatedAt.UnixMicro() != old[0].CreatedAt.UnixMicro() {
		t.Fatalf("去重不应改变添加时间: got=%v want=%v", entries[0].CreatedAt, old[0].CreatedAt)
	}
	// 存量条目不自动补全：状态落在 manual，否则升级完成即对整个片单发起一轮出网。
	for _, entry := range entries {
		if entry.Kind != models.WatchlistKindMovie || entry.EnrichmentStatus != models.WatchlistEnrichmentManual {
			t.Fatalf("存量行应迁成 movie/manual: %+v", entry)
		}
	}
	assertWatchlistSchemaUpgraded(t, db)
	assertTitleKindUniqueness(t, db, "共存检查")
}

// 已经是 title 唯一的库（S2）：去重函数必须整个跳过，不能因为 idx_watchlist_title
// 被删掉就重新放行并按单键硬删。
func TestWatchlistUpgradeFromTitleUniqueLibrary(t *testing.T) {
	db := openLegacyWatchlistDB(t)
	if err := db.Exec("CREATE UNIQUE INDEX idx_watchlist_title ON watchlist_entries (title)").Error; err != nil {
		t.Fatalf("建旧唯一索引失败: %v", err)
	}
	// "沙丘" 与 "  沙丘  " 在 unique(title) 下是两行合法数据，但去重一旦被放行，
	// 它会把后者 trim 成 "沙丘" 撞上前者并硬删掉一条。夹具必须带这一对——
	// 全用互不相同的标题的话，守卫被删掉这条用例也照样通过，什么都钉不住。
	//
	// 顺带钉下一个刻意的承诺：S2 路径上带首尾空白的老片名**保持原样**，不做 trim。
	// 服务层今天已经写不出这种片名了，但守卫的语义就是"早先版本已经去过重，别碰"，
	// 越界去 trim 就等于放行了那条硬删路径。
	old := []legacyWatchlistEntry{{Title: "沙丘"}, {Title: "  沙丘  "}, {Title: "沙丘（1984）"}, {Title: "银翼杀手 2049"}}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := database.ApplySchema(db); err != nil {
			t.Fatalf("第 %d 次 ApplySchema 失败: %v", i+1, err)
		}
	}
	entries := listWatchlist(t, db)
	if len(entries) != len(old) {
		t.Fatalf("升级不应删行: got=%d want=%d %+v", len(entries), len(old), entries)
	}
	for i, entry := range entries {
		if entry.ID != old[i].ID || entry.Title != old[i].Title {
			t.Fatalf("升级不应改动片名: got=%+v want=%+v", entry, old[i])
		}
		if entry.Kind != models.WatchlistKindMovie || entry.EnrichmentStatus != models.WatchlistEnrichmentManual {
			t.Fatalf("存量行应迁成 movie/manual: %+v", entry)
		}
	}
	assertWatchlistSchemaUpgraded(t, db)
	assertTitleKindUniqueness(t, db, "共存检查")
}
