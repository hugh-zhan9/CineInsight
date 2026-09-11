package database

import (
	"log"
	"strings"
	"video-master/models"

	"gorm.io/gorm"
)

// dedupeWatchlistTitlesBeforeSchema 在 AutoMigrate 之前把老库里重复的片名合并掉，
// 让随后建立的唯一索引一次建成。它保留最早的一条（片单没有媒体引用，也没有别的
// 用户字段），顺便把首尾空白修掉。新库无事可做。
//
// 它曾经叫 migrateWatchlistTitleUniqueness，并在去重之后自己建 idx_watchlist_title。
// 唯一键改成 (title, kind) 之后那个动作必须退役：索引由 AutoMigrate 依结构体标签
// 创建，这里再建一次会因为模型里已经没有 idx_watchlist_title 而直接报错，把应用挡在
// 启动之外。去重能力保留，建索引的职责交出去，名字也改成名副其实的。
//
// 守卫同样必须换，而且必须换成"看列"而不只是"看索引"。原来的守卫是"已有
// idx_watchlist_title 就跳过"，删掉那个索引之后它会失效放行，而放行后的去重是按
// title 单键判重并硬删（WatchlistEntry 没有 DeletedAt）——删掉的正是本次刚刚允许
// 共存的同名不同类型记录。
//
// 真正的前置条件是"库里还没有 kind 列"：那时每一行都隐含是 movie，按 title 去重
// 才恰好等价于按 (title, kind) 去重。kind 一旦存在，同名不同类型就是合法的两条
// 记录，再按 title 单键硬删就是在删用户数据——哪怕此刻两个索引都不在（手工删过
// 索引、或上次升级建索引前就中断），也一样不能删。所以先看列，索引只是补充。
func dedupeWatchlistTitlesBeforeSchema(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.WatchlistEntry{}) {
		return nil
	}
	// 复合索引建在 kind 上，列不在索引也不可能在——第二个判断因此是冗余的。
	// 留着是刻意的：这条路走下去是不可逆的硬删除，多一次 HasIndex 换一层
	// 兜底（比如某个后端上 HasColumn 判走了眼）划算。
	if db.Migrator().HasColumn(&models.WatchlistEntry{}, "kind") ||
		db.Migrator().HasIndex(&models.WatchlistEntry{}, "idx_watchlist_title_kind") {
		return nil
	}
	// 早先的版本已经按 title 去过重，不必再来一遍。
	if db.Migrator().HasIndex(&models.WatchlistEntry{}, "idx_watchlist_title") {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		// 这里是读后改：全表读进内存判重再删。表锁挡住并发写入，不让它在读与删之间
		// 插进一条新的重复片名。（随后的 migrateWatchlistKind 只有集合写与 DDL，
		// 没有这个窗口，所以刻意不加锁。）
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("LOCK TABLE watchlist_entries IN ACCESS EXCLUSIVE MODE").Error; err != nil {
				return err
			}
		}
		// 只读 id 与 title：这一步跑在 AutoMigrate 之前，老库里还没有 kind 与补全
		// 状态那几列，按整个结构体取列会查不到列。去重本身也只需要这两列。
		var entries []models.WatchlistEntry
		if err := tx.Select("id", "title").Order("id ASC").Find(&entries).Error; err != nil {
			return err
		}
		seen := map[string]uint{}
		for _, entry := range entries {
			title := strings.TrimSpace(entry.Title)
			if kept, exists := seen[title]; exists {
				if err := tx.Delete(&models.WatchlistEntry{}, entry.ID).Error; err != nil {
					return err
				}
				log.Printf("想看片单合并重复项 kept_id=%d removed_id=%d", kept, entry.ID)
				continue
			}
			seen[title] = entry.ID
			if title != entry.Title {
				if err := tx.Model(&models.WatchlistEntry{}).Where("id = ?", entry.ID).Update("title", title).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}
