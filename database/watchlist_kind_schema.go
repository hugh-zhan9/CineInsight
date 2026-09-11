package database

import (
	"errors"
	"fmt"
	"video-master/models"

	"gorm.io/gorm"
)

// migrateWatchlistKind 把存量想看条目补齐类型与补全状态，并退掉单列唯一索引。
//
// 必须跑在 AutoMigrate **之后**：它依赖 AutoMigrate 建出的 kind / enrichment_status
// 两列，以及依结构体标签建出的复合唯一索引。这与 dedupeWatchlistTitlesBeforeSchema
// 的位置正好相反，后者必须跑在 AutoMigrate 之前才能让唯一索引一次建成。
//
// enrichmentColumnJustAdded 必须在 AutoMigrate **之前**观测（沿用 ApplySchema 里
// migrateLibraryWatchSetting 等一串设置迁移的同款做法）：enrichment_status 是
// NOT NULL DEFAULT 'pending'，AutoMigrate 加列时会把每一条存量行一并刷成
// 'pending'，加完之后再看列值已经分不出哪些行是升级前就有的。而存量条目恰恰
// 不该自动补全——否则升级完成的那一刻就会对整个片单发起一轮出网请求。
//
// 已知残留窗口（与相邻五个设置迁移同款，不是本次新引入的）：这个判断只活在内存里，
// 而它记录的事实是被 AutoMigrate 本身抹掉的。若进程在 ADD COLUMN 之后、本函数之前
// 死掉，或那一次 AutoMigrate 在别的模型上失败、下一次却成功了，再启动就会看到列
// 已存在而判成"不是刚加的"，存量行于是永久停在 pending，补全 worker 上线后会对
// 它们跑一轮。（AutoMigrate 内部按依赖重排模型，不按 AllModels() 的顺序执行，
// 所以窗口与 watchlist_entries 排在清单第几位无关。）
// 彻底消除它需要把默认值直接写成 'manual' 让 ADD COLUMN 原子回填，但那样 GORM 会
// 用同一个标签默认值回填**新建**条目的零值字段（实测：Create 会把零值写成标签默认
// 值并回填结构体），新条目就再也进不了 pending——那是更坏的交换。真要两全得改
// 设计里 §2.1 声明的默认值，属于设计裁决，不在本切片内自行决定。
//
// 三步各自幂等，整条可以反复重跑。不加 LOCK TABLE：这里只有集合写与 DDL，
// 没有"读进内存再写回"的窗口需要挡。
func migrateWatchlistKind(db *gorm.DB, enrichmentColumnJustAdded bool) error {
	if !db.Migrator().HasTable(&models.WatchlistEntry{}) {
		return nil
	}
	// ① 存量行补默认值。用 UpdateColumn 而不是 Update：这是迁移，不该把所有老条目
	//    的 updated_at 一起刷新。（AutoMigrate 之前那一步去重用的是 Update，会顺带
	//    刷新 updated_at——那是它原有的行为，本次不改。）
	//
	//    kind 的列默认值 'movie' 正好就是存量行该有的值，AutoMigrate 加列时就已经
	//    把每一行填好了，所以这一句实际只兜"手工把 kind 改成空串"这一种情况：
	//    GORM 会用标签默认值回填零值字段，裸 SQL 省略该列也由列默认值兜住，
	//    程序写不出空串（database/watchlist_kind_schema_test.go 的
	//    TestWatchlistDefaultsApplyOnInsert 钉住了这个前提）。
	if err := db.Model(&models.WatchlistEntry{}).
		Where("kind IS NULL OR kind = ?", "").
		UpdateColumn("kind", models.WatchlistKindMovie).Error; err != nil {
		// 唯一可能的失败是撞上 idx_watchlist_title_kind：库里同时有同名的 kind=''
		// 与 kind='movie' 两行。该保留哪一条只有人能决定，这里不猜，只把错误说到
		// 用户能照着修的程度。
		return fmt.Errorf("补齐想看条目类型失败（库里可能有同名且 kind 为空的手工数据，需要人工确认保留哪一条）: %w", err)
	}
	if enrichmentColumnJustAdded {
		// 本次刚加上列，表里此刻的行全部是升级前就有的，统一改判 manual。
		if err := db.Model(&models.WatchlistEntry{}).
			Where("enrichment_status = ?", models.WatchlistEnrichmentPending).
			UpdateColumn("enrichment_status", models.WatchlistEnrichmentManual).Error; err != nil {
			return err
		}
	}
	if err := db.Model(&models.WatchlistEntry{}).
		Where("enrichment_status IS NULL OR enrichment_status = ?", "").
		UpdateColumn("enrichment_status", models.WatchlistEnrichmentManual).Error; err != nil {
		return err
	}
	// ② 复合唯一索引由 AutoMigrate 依标签创建，这里只断言它在位。没有它就不能安全地
	//    删掉旧的单列唯一索引——那会让片单短暂失去任何撞名保护。
	if !db.Migrator().HasIndex(&models.WatchlistEntry{}, "idx_watchlist_title_kind") {
		return errors.New("AutoMigrate 未建立 idx_watchlist_title_kind，不能删除旧的单列唯一索引")
	}
	// ③ 旧的单列唯一索引让同名不同类型无法共存，确认新索引就位后删掉。
	if db.Migrator().HasIndex(&models.WatchlistEntry{}, "idx_watchlist_title") {
		if err := db.Migrator().DropIndex(&models.WatchlistEntry{}, "idx_watchlist_title"); err != nil {
			return err
		}
	}
	return nil
}
