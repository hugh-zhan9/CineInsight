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

// libraryMatchInputColumns 是想看自动匹配读的视频列：改了它们，想看条目的匹配结果可能变（D-P2-03）。
var libraryMatchInputColumns = []string{"display_title", "original_title", "name", "year"}

// UpdateVideoTitleFields 是「map 更新 videos 且改到 display_title 或 name」的唯一写法：读出 videoID 这一行
// 当前的两列（含软删行），合上 updates 里的新值算出 title_sort_key，与 updates 一并执行。updates 不被改动
// （键写在副本里）。
//
// query 是调用方已经带好条件的 tx.Model(...).Where(...)，条件必须锁定 videoID 这一行（可以再加乐观条件，
// 例如 path = 旧路径）；返回值就是 Updates 的结果，调用方照旧看 Error 与 RowsAffected。updates 里没有标题列时
// 等同于直接 Updates。改到片名、原始片名、文件名或年份并且写成功时，通知想看自动匹配稍后重跑一轮。
func UpdateVideoTitleFields(query *gorm.DB, videoID uint, updates map[string]interface{}) *gorm.DB {
	display, hasDisplay := updates["display_title"].(string)
	name, hasName := updates["name"].(string)
	if !hasDisplay && !hasName {
		return notifyIfMatchInputsChanged(query.Updates(updates), updates)
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
	withKey := make(map[string]interface{}, len(updates)+1)
	for column, value := range updates {
		withKey[column] = value
	}
	withKey["title_sort_key"] = models.VideoTitleSortKey(display, name)
	return notifyIfMatchInputsChanged(query.Updates(withKey), updates)
}

func notifyIfMatchInputsChanged(result *gorm.DB, updates map[string]interface{}) *gorm.DB {
	if result.Error != nil || result.RowsAffected == 0 {
		return result
	}
	for _, column := range libraryMatchInputColumns {
		if _, ok := updates[column]; ok {
			notifyLibraryMatchInputsChanged("video-metadata")
			break
		}
	}
	return result
}

// videoTitleSortReconcileBatch 是启动对账每批的行数。
const videoTitleSortReconcileBatch = 500

type videoTitleSortRow struct {
	ID           uint
	DisplayTitle string
	Name         string
	TitleSortKey string
}

// ReconcileVideoTitleSortKeys 重算全部视频（含软删行：恢复之后要能直接参与排序）的排序键，存的与算出来的不一致
// 就改正：存量空键、与改名并发时错过的写入、排序规则版本变了（键的版本前缀不同）都在这里收敛。每批 500，按 id
// 递增推进；写回带上读到的两列与旧键做条件，读与写之间被改过名的行由那次改名自己写了键，这里不覆盖。只写这一列，
// 不动 updated_at，不走钩子。返回改正的行数；ctx 取消时停在当前批之后。
func ReconcileVideoTitleSortKeys(ctx context.Context) (int, error) {
	return reconcileVideoTitleSortKeysFrom(ctx, database.DB)
}

func reconcileVideoTitleSortKeysFrom(ctx context.Context, db *gorm.DB) (int, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	var lastID uint
	fixed := 0
	for {
		if err := ctx.Err(); err != nil {
			return fixed, err
		}
		var rows []videoTitleSortRow
		err := db.WithContext(ctx).Unscoped().Model(&models.Video{}).
			Select("id", "display_title", "name", "title_sort_key").
			Where("id > ?", lastID).
			Order("id ASC").Limit(videoTitleSortReconcileBatch).Scan(&rows).Error
		if err != nil {
			return fixed, fmt.Errorf("读取片名排序键失败: %w", err)
		}
		if len(rows) == 0 {
			return fixed, nil
		}
		lastID = rows[len(rows)-1].ID
		stale := make([]videoTitleSortRow, 0)
		for _, row := range rows {
			if models.VideoTitleSortKey(row.DisplayTitle, row.Name) != row.TitleSortKey {
				stale = append(stale, row)
			}
		}
		if len(stale) > 0 {
			err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				for _, row := range stale {
					result := tx.Unscoped().Model(&models.Video{}).
						Where("id = ? AND title_sort_key = ? AND display_title = ? AND name = ?", row.ID, row.TitleSortKey, row.DisplayTitle, row.Name).
						UpdateColumn("title_sort_key", models.VideoTitleSortKey(row.DisplayTitle, row.Name))
					if result.Error != nil {
						return result.Error
					}
					fixed += int(result.RowsAffected)
				}
				return nil
			})
			if err != nil {
				return fixed, fmt.Errorf("写入片名排序键失败: %w", err)
			}
		}
	}
}

// StartVideoTitleSortKeyReconcile 在后台跑一次对账（应用启动、数据库就绪后调用），只记日志。
func StartVideoTitleSortKeyReconcile(ctx context.Context) {
	go func() {
		fixed, err := ReconcileVideoTitleSortKeys(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("片名排序键对账失败 fixed=%d err=%v", fixed, err)
			return
		}
		if fixed > 0 {
			log.Printf("片名排序键对账完成 fixed=%d", fixed)
		}
	}()
}
