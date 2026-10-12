package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
)

// 托管路径（4.3 managed_paths）：这些列存的是托管目录 media-details 之下的相对路径
// （ManagedImageService 的内容寻址落盘）。位于数据目录内、而文件已经不在的，修复时把列置空，
// 界面回到「没有图」的状态，补全或导入时会重新落盘。软删除的行同样检查。

type managedPathSpec struct {
	key    string
	table  string
	column string
	label  string
}

var managedPathSpecs = []managedPathSpec{
	{"videos.poster_path", "videos", "poster_path", "视频海报副本"},
	{"videos.fanart_path", "videos", "fanart_path", "视频背景图副本"},
	{"people.avatar_path", "people", "avatar_path", "人物头像"},
	{"media_collections.cover_path", "media_collections", "cover_path", "作品集封面"},
	{"watchlist_entries.poster_path", "watchlist_entries", "poster_path", "想看封面"},
	{"movie_chart_entries.poster_path", "movie_chart_entries", "poster_path", "榜单海报"},
}

type managedPathRow struct {
	ID    uint
	Value string
}

// managedPathMissing 判定一条托管路径是否「在数据目录内且文件不存在」。绝对路径、越界路径不归体检管；
// 读不了（权限等）不算不存在。
func managedPathMissing(root, value string) bool {
	if strings.TrimSpace(value) == "" || filepath.IsAbs(value) {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	_, err := os.Lstat(filepath.Join(root, clean))
	return os.IsNotExist(err)
}

func (s *DataHealthService) managedRoot() string {
	return filepath.Join(s.dataDir, "media-details")
}

// scanManagedPaths 按 id 分页（每页 500）读出非空的路径列，把每页里指向不存在文件的行交给 visit。
func (s *DataHealthService) scanManagedPaths(ctx context.Context, db *gorm.DB, spec managedPathSpec, visit func([]managedPathRow) error) error {
	root := s.managedRoot()
	var lastID uint
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var rows []managedPathRow
		query := fmt.Sprintf("SELECT id, %s AS value FROM %s WHERE %s <> '' AND id > ? ORDER BY id LIMIT ?", spec.column, spec.table, spec.column)
		if err := db.Raw(query, lastID, dataHealthBatchSize).Scan(&rows).Error; err != nil {
			return fmt.Errorf("%s: %w", spec.key, err)
		}
		if len(rows) == 0 {
			return nil
		}
		lastID = rows[len(rows)-1].ID
		missing := make([]managedPathRow, 0)
		for _, row := range rows {
			if managedPathMissing(root, row.Value) {
				missing = append(missing, row)
			}
		}
		if len(missing) > 0 {
			if err := visit(missing); err != nil {
				return err
			}
		}
	}
}

// checkManagedPaths 按列统计指向不存在文件的托管路径，示例给相对数据目录的路径（不含绝对路径）。
func (s *DataHealthService) checkManagedPaths(ctx context.Context, db *gorm.DB) (DataHealthCheckResult, error) {
	groups := []DataHealthGroup{}
	var total int64
	for _, spec := range managedPathSpecs {
		group := DataHealthGroup{Key: spec.key, Label: spec.label, Repairable: true, Examples: []string{}}
		err := s.scanManagedPaths(ctx, db, spec, func(rows []managedPathRow) error {
			group.Count += int64(len(rows))
			for _, row := range rows {
				if len(group.Examples) < dataHealthExampleLimit {
					group.Examples = append(group.Examples, "media-details/"+filepath.ToSlash(filepath.Clean(filepath.FromSlash(row.Value))))
				}
			}
			return nil
		})
		if err != nil {
			return DataHealthCheckResult{}, err
		}
		if group.Count > 0 {
			total += group.Count
			groups = append(groups, group)
		}
	}
	if total == 0 {
		return DataHealthCheckResult{Status: DataHealthStatusOK, Summary: "托管图片都在"}, nil
	}
	return DataHealthCheckResult{
		Status:     DataHealthStatusProblem,
		Count:      total,
		Summary:    fmt.Sprintf("%d 条记录指向已不存在的托管图片，可以清空这些路径", total),
		Groups:     groups,
		Repairable: true,
	}, nil
}

// repairManagedPaths 清空指向不存在文件的路径列：每页一个短事务，写入前再判一次文件仍不存在，
// 且只在列值未变时置空。
func (s *DataHealthService) repairManagedPaths(ctx context.Context, db *gorm.DB) (int64, error) {
	var cleared int64
	root := s.managedRoot()
	for _, spec := range managedPathSpecs {
		err := s.scanManagedPaths(ctx, db, spec, func(rows []managedPathRow) error {
			var batch int64
			err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				batch = 0
				for _, row := range rows {
					if !managedPathMissing(root, row.Value) {
						continue
					}
					result := tx.Exec(fmt.Sprintf("UPDATE %s SET %s = '' WHERE id = ? AND %s = ?", spec.table, spec.column, spec.column),
						row.ID, row.Value)
					if result.Error != nil {
						return result.Error
					}
					batch += result.RowsAffected
				}
				return nil
			})
			if err == nil {
				cleared += batch
			}
			return err
		})
		if err != nil {
			return cleared, err
		}
	}
	return cleared, nil
}
