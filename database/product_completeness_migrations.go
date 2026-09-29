package database

import (
	"fmt"
	"log"
	"time"
	"video-master/models"

	"gorm.io/gorm"
)

// 产品完善度批次（2026-09-29）的一次性迁移与默认值。schema 本身由 models 与 AutoMigrate
// 负责，这里只放 AutoMigrate 表达不了的数据回填与索引替换。
//
// 每个函数都要满足两条：老库升级一次到位、重复执行结果不变。判据优先选「看数据」
// （mode = ''、favorites_unified_at IS NULL、列值 IS NULL / <= 0），而不是只看
// 「列刚建出来」这种活在内存里的事实：后者在进程死于 AutoMigrate 与迁移之间时会永久丢失。
// 唯一必须看「列刚建出来」的是 short_feed_enabled，见 migrateShortFeedEnabledSetting。

// 清理中心阈值默认值（D-PC36）。≤0 视为默认；默认值非零，所以 settings 三列不带
// gorm default 标签，由新库的初始化行与 migrateCleanupThresholdSettings 写入。
const (
	DefaultCleanupShortSeconds = 5
	DefaultCleanupLowWidth     = 480
	DefaultCleanupLowHeight    = 320
)

// 索引名。旧名只出现在「删除旧索引」的迁移里，新名与 models 上的标签一致。
const (
	watchlistTitleKindSourceIndex = "idx_watchlist_title_kind_source"
	watchlistTitleKindLegacyIndex = "idx_watchlist_title_kind"
	watchlistTitleLegacyIndex     = "idx_watchlist_title"

	glossaryScopeLangTermIndex   = "idx_translation_glossary_entries_scope_lang_term"
	glossaryScopeTermLegacyIndex = "idx_translation_glossary_entries_scope_term"
)

// migrateTrashEntryMode 回填回收站条目的 mode（D-PC01/03）。
//
// 看 file_moved、deleted_by，以及旧版中断状态（pending_move / rollback 且已有 trash_path）：file_moved=true 是旧版同目录 trash/ 里的
// 文件（legacy_trash）；file_moved=false 且是扫描器删的，是「文件已经不在」的软删（missing）；
// 其余是用户只删记录（record_only）。三条各自带 mode = ” 条件，幂等，可以反复执行。
// 用 UpdateColumn：这是迁移，不该把每个条目的 updated_at 一起刷新。
// 三条独立的字面量 UPDATE 而不是一个 CASE：CASE 分支全是绑定参数时 Postgres 推不出类型。
func migrateTrashEntryMode(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		for _, table := range []string{"video_trash_entries", "image_trash_entries"} {
			if !tx.Migrator().HasTable(table) {
				continue
			}
			// 旧版删除流程先以 pending_move 落条目并写好 trash_path，删除事务提交时才置
			// file_moved=true；移动失败或进程中断会留在 pending_move / rollback。这些行的文件
			// 在（或曾经要去）旧版 trash/，同样是 legacy_trash，不能因 file_moved=false 归成
			// record_only（P-001 评审 I1）。
			if err := tx.Table(table).
				Where("mode = ? AND (file_moved = ? OR (state IN ? AND trash_path <> ?))",
					"", true, []string{"pending_move", "rollback"}, "").
				UpdateColumn("mode", models.TrashModeLegacyTrash).Error; err != nil {
				return fmt.Errorf("回填 %s.mode(legacy_trash) 失败: %w", table, err)
			}
			if err := tx.Table(table).
				Where("mode = ? AND file_moved = ? AND deleted_by = ?", "", false, "scanner").
				UpdateColumn("mode", models.TrashModeMissing).Error; err != nil {
				return fmt.Errorf("回填 %s.mode(missing) 失败: %w", table, err)
			}
			if err := tx.Table(table).
				Where("mode = ?", "").
				UpdateColumn("mode", models.TrashModeRecordOnly).Error; err != nil {
				return fmt.Errorf("回填 %s.mode(record_only) 失败: %w", table, err)
			}
		}
		return nil
	})
}

// migrateShortFeedEnabledSetting 让老库升级后手机端保持开启（D-PC45）。
//
// 新库的默认是关，由 ApplySchema 的初始化行显式写 false；老库升级则要保持现状即开。
// 这一列不带 gorm default 标签（否则双向迁移器会把用户关掉的开关翻回去），所以只能靠
// 这里显式刷 true。
//
// 判据是「settings 表已存在、且这一列是这一轮才建出来的」，必须在 AutoMigrate 之前观测，
// 并且这个函数要紧贴 AutoMigrate 调用，中间不夹别的可失败步骤——判据在 AutoMigrate 提交
// 那一刻就被消费掉了，用户关掉之后重启因此不会被翻回。
//
// 额外的一道自愈：列已存在时只处理 NULL 行。新建的列在老行上恒为 NULL，用户主动关掉写入的
// 是 false，两者可分；这样即使进程死在 AutoMigrate 与本函数之间，下次启动也补得上，
// 而不会把已经关掉的开关翻回来。
func migrateShortFeedEnabledSetting(db *gorm.DB, settingsTableExisted, shortFeedEnabledColumnExisted bool) error {
	if !settingsTableExisted {
		return nil
	}
	query := db.Model(&models.Settings{})
	if shortFeedEnabledColumnExisted {
		query = query.Where("short_feed_enabled IS NULL")
	} else {
		query = query.Where("1 = 1")
	}
	return query.UpdateColumn("short_feed_enabled", true).Error
}

// migrateCleanupThresholdSettings 把三个清理阈值补成默认值 5 / 480 / 320（D-PC36）。
//
// 三列不带 gorm default（默认非零的数值列，同 ProxyCacheLimitBytes）。这里不需要「列刚建出来」
// 的判据：≤0 本来就等价于默认值，NULL 与 0 一起补，用户设过的正值不会被碰，重复执行不变。
func migrateCleanupThresholdSettings(db *gorm.DB) error {
	columns := []struct {
		name  string
		value int
	}{
		{"cleanup_short_seconds", DefaultCleanupShortSeconds},
		{"cleanup_low_width", DefaultCleanupLowWidth},
		{"cleanup_low_height", DefaultCleanupLowHeight},
	}
	for _, column := range columns {
		if err := db.Model(&models.Settings{}).
			Where(column.name+" IS NULL OR "+column.name+" <= ?", 0).
			UpdateColumn(column.name, column.value).Error; err != nil {
			return fmt.Errorf("补默认清理阈值 %s 失败: %w", column.name, err)
		}
	}
	return nil
}

// migrateUnifyFavorites 把手机端互动表里的收藏与点赞并进主表，收藏点赞从此只有一份数据（D-PC40）。
//
// 一次性靠 settings.favorites_unified_at：NULL 才执行，整个合并与写标记在同一个事务里，
// 中途失败整体回滚、下次仍是 NULL。取并集而不是覆盖：
//   - short_feed_interactions.favorited=true → videos.is_favorite=true，favorited_at 优先保留
//     主表已有值，其次取互动表时间，最后取当前时间；
//   - short_feed_interactions.liked=true → videos.is_liked=true（「反馈回流」开关关闭时手机端的
//     点赞从未投影过，只存在于互动表里）；
//   - 图片侧对 short_feed_image_interactions 同理。
//
// 主表上已有的收藏/点赞永远不会被取消。互动表的 favorited / liked 列保留不删。
// SQL 两个后端通用：全部列限定（避开 Postgres 42702），不用 UPDATE ... FROM，
// 互动表 video_id / image_id 有唯一索引，所以标量子查询至多返回一行。
func migrateUnifyFavorites(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.Settings{}) {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var pending int64
		if err := tx.Model(&models.Settings{}).Where("favorites_unified_at IS NULL").Count(&pending).Error; err != nil {
			return err
		}
		if pending == 0 {
			return nil
		}
		now := time.Now()
		merges := []struct {
			media, interactions, key string
		}{
			{"videos", "short_feed_interactions", "video_id"},
			{"images", "short_feed_image_interactions", "image_id"},
		}
		var merged int64
		for _, merge := range merges {
			favorite := tx.Exec(fmt.Sprintf(`UPDATE %[1]s
SET is_favorite = ?,
    favorited_at = COALESCE(%[1]s.favorited_at,
        (SELECT sfi.favorited_at FROM %[2]s sfi WHERE sfi.%[3]s = %[1]s.id), ?)
WHERE EXISTS (SELECT 1 FROM %[2]s sfi WHERE sfi.%[3]s = %[1]s.id AND sfi.favorited = ?)`,
				merge.media, merge.interactions, merge.key), true, now, true)
			if favorite.Error != nil {
				return fmt.Errorf("合并 %s 收藏失败: %w", merge.media, favorite.Error)
			}
			liked := tx.Exec(fmt.Sprintf(`UPDATE %[1]s
SET is_liked = ?
WHERE %[1]s.is_liked = ?
  AND EXISTS (SELECT 1 FROM %[2]s sfi WHERE sfi.%[3]s = %[1]s.id AND sfi.liked = ?)`,
				merge.media, merge.interactions, merge.key), true, false, true)
			if liked.Error != nil {
				return fmt.Errorf("合并 %s 点赞失败: %w", merge.media, liked.Error)
			}
			merged += favorite.RowsAffected + liked.RowsAffected
		}
		if err := tx.Model(&models.Settings{}).
			Where("favorites_unified_at IS NULL").
			UpdateColumn("favorites_unified_at", now).Error; err != nil {
			return err
		}
		if merged > 0 {
			log.Printf("[Migration] 收藏与点赞并集迁移：更新 %d 行", merged)
		}
		return nil
	})
}

// migrateGlossaryUniqueKey 把术语表唯一键换成 (scope_key, target_language, source_term_lower)（D-PC16）。
//
// 新索引换了名字，由 AutoMigrate 依标签建出（新键比旧键宽松，建索引不会撞数据）；
// 这里确认它在位之后才删旧索引，让术语表任何时刻都有唯一保护。
func migrateGlossaryUniqueKey(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.TranslationGlossaryEntry{}) {
		return nil
	}
	if !db.Migrator().HasIndex(&models.TranslationGlossaryEntry{}, glossaryScopeLangTermIndex) {
		return fmt.Errorf("AutoMigrate 未建立 %s，不能删除旧的术语唯一索引", glossaryScopeLangTermIndex)
	}
	return db.Exec("DROP INDEX IF EXISTS " + glossaryScopeTermLegacyIndex).Error
}

// replaceWatchlistTitleIndexes 在新的 (title, kind, source_item_id) 唯一索引就位之后，
// 在同一个事务里删掉两代旧索引：最早的单列 idx_watchlist_title 与 (title, kind) 的
// idx_watchlist_title_kind（D-PC52）。AutoMigrate 只增不减，不会替我们删。
// 新索引名必须以 idx_watchlist_title 开头，services.watchlistTitleConflict 靠这个前缀识别撞名。
func replaceWatchlistTitleIndexes(db *gorm.DB) error {
	if !db.Migrator().HasIndex(&models.WatchlistEntry{}, watchlistTitleKindSourceIndex) {
		return fmt.Errorf("AutoMigrate 未建立 %s，不能删除旧的片名唯一索引", watchlistTitleKindSourceIndex)
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, legacy := range []string{watchlistTitleLegacyIndex, watchlistTitleKindLegacyIndex} {
			if !tx.Migrator().HasIndex(&models.WatchlistEntry{}, legacy) {
				continue
			}
			if err := tx.Migrator().DropIndex(&models.WatchlistEntry{}, legacy); err != nil {
				return fmt.Errorf("删除旧索引 %s 失败: %w", legacy, err)
			}
		}
		return nil
	})
}

// migrateMovieChartMarkEntryOrigin 回填 movie_chart_marks.watchlist_entry_origin（APP-05/07）。
//
// 本批次之前只有榜单「想看」新建条目才会写 watchlist_entry_id，所以历史上任何
// 认领了条目却没有来源的行都是 chart。判据看数据（认领非 0 且来源为空），重复执行不变；
// 新写入的 enrichment / chart 行来源非空，不会被改写。
func migrateMovieChartMarkEntryOrigin(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.MovieChartMark{}) {
		return nil
	}
	if err := db.Model(&models.MovieChartMark{}).
		Where("watchlist_entry_id <> ? AND watchlist_entry_origin = ?", 0, "").
		UpdateColumn("watchlist_entry_origin", models.MovieChartOriginChart).Error; err != nil {
		return fmt.Errorf("回填 movie_chart_marks.watchlist_entry_origin 失败: %w", err)
	}
	return nil
}
