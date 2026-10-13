package services

import (
	"context"
	"errors"
	"fmt"
	"log"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 片名排序键的服务层部分（UI 二期 §2.3）。键的算法与钩子在 models/video_title_sort.go。

// UpdateVideoTitleFields 是「map 更新 videos 且改到 display_title 或 name」的唯一写法：读出 videoID 这一行
// 当前的两列（含软删行），合上 updates 里的新值算出 title_sort_key，放进同一个 updates 一并执行。
//
// query 是调用方已经带好条件的 tx.Model(...).Where(...)，条件必须锁定 videoID 这一行（可以再加乐观条件，
// 例如 path = 旧路径）；返回值就是 query.Updates(updates) 的结果，调用方照旧看 Error 与 RowsAffected。
// updates 里没有标题列时等同于直接 Updates。
func UpdateVideoTitleFields(query *gorm.DB, videoID uint, updates map[string]interface{}) *gorm.DB {
	display, hasDisplay := updates["display_title"].(string)
	name, hasName := updates["name"].(string)
	if !hasDisplay && !hasName {
		return query.Updates(updates)
	}
	if !hasDisplay || !hasName {
		var current struct {
			DisplayTitle string
			Name         string
		}
		err := query.Session(&gorm.Session{NewDB: true}).Unscoped().Model(&models.Video{}).
			Select("display_title", "name").Where("id = ?", videoID).Take(&current).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			_ = query.AddError(fmt.Errorf("读取视频片名失败: %w", err))
			return query
		}
		if !hasDisplay {
			display = current.DisplayTitle
		}
		if !hasName {
			name = current.Name
		}
	}
	updates["title_sort_key"] = models.VideoTitleSortKey(display, name)
	return query.Updates(updates)
}

// videoTitleSortBackfillBatch 是启动补齐每批的行数。
const videoTitleSortBackfillBatch = 500

// BackfillVideoTitleSortKeys 补齐 title_sort_key 为空串的行（含软删行：恢复之后要能直接参与排序），每批 500，
// 按 id 递增推进，每行最多看一次；两列都为空的行算出来仍是空串，原样跳过。写回带上读到的两列做条件，
// 读与写之间被改过名的行由那次改名自己写了键，这里不覆盖。只写这一列，不动 updated_at，不走钩子。
// 返回补上的行数；ctx 取消时停在当前批之后。
func BackfillVideoTitleSortKeys(ctx context.Context) (int, error) {
	return backfillVideoTitleSortKeysFrom(ctx, database.DB)
}

func backfillVideoTitleSortKeysFrom(ctx context.Context, db *gorm.DB) (int, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	var lastID uint
	filled := 0
	for {
		if err := ctx.Err(); err != nil {
			return filled, err
		}
		var rows []struct {
			ID           uint
			DisplayTitle string
			Name         string
		}
		err := db.WithContext(ctx).Unscoped().Model(&models.Video{}).
			Select("id", "display_title", "name").
			Where("title_sort_key = ? AND id > ?", "", lastID).
			Order("id ASC").Limit(videoTitleSortBackfillBatch).Scan(&rows).Error
		if err != nil {
			return filled, fmt.Errorf("读取待补齐的片名排序键失败: %w", err)
		}
		if len(rows) == 0 {
			return filled, nil
		}
		err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for _, row := range rows {
				key := models.VideoTitleSortKey(row.DisplayTitle, row.Name)
				if key == "" {
					continue
				}
				result := tx.Unscoped().Model(&models.Video{}).
					Where("id = ? AND title_sort_key = ? AND display_title = ? AND name = ?", row.ID, "", row.DisplayTitle, row.Name).
					UpdateColumn("title_sort_key", key)
				if result.Error != nil {
					return result.Error
				}
				filled += int(result.RowsAffected)
			}
			return nil
		})
		if err != nil {
			return filled, fmt.Errorf("写入片名排序键失败: %w", err)
		}
		lastID = rows[len(rows)-1].ID
	}
}

// StartVideoTitleSortKeyBackfill 在后台跑一次补齐（应用启动、数据库就绪后调用），只记日志。
func StartVideoTitleSortKeyBackfill(ctx context.Context) {
	go func() {
		filled, err := BackfillVideoTitleSortKeys(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("片名排序键补齐失败 filled=%d err=%v", filled, err)
			return
		}
		if filled > 0 {
			log.Printf("片名排序键补齐完成 filled=%d", filled)
		}
	}()
}
