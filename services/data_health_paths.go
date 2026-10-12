package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// managedPathRoot 把列值规整成 media-details 之下的相对路径，并给出它所属的托管子目录（第一段，
// 如 videos、people）。绝对路径、越界路径与空值不归体检管，ok=false。
func managedPathRoot(value string) (clean, root string, ok bool) {
	if strings.TrimSpace(value) == "" || filepath.IsAbs(value) {
		return "", "", false
	}
	clean = filepath.Clean(filepath.FromSlash(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	if index := strings.IndexRune(clean, filepath.Separator); index >= 0 {
		root = clean[:index]
	}
	return clean, root, true
}

// managedDirAvailable：跟随符号链接 stat 成功且是目录。不存在、链接失效、读不到都算不可用。
func managedDirAvailable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// managedRoots 记下每个托管子目录此刻是否可用（审阅 I4）：外接盘没挂、链接失效或整个目录被删掉时，
// 其下的路径一律不判、不清——那是「目录不可用」，不是「这些文件丢了」，只报告目录不可用。
type managedRoots struct {
	base  string
	cache map[string]bool
}

func newManagedRoots(base string) *managedRoots {
	return &managedRoots{base: base, cache: map[string]bool{}}
}

func (r *managedRoots) available(root string) bool {
	if available, ok := r.cache[root]; ok {
		return available
	}
	available := managedDirAvailable(r.base) && managedDirAvailable(filepath.Join(r.base, root))
	r.cache[root] = available
	return available
}

// managedFileMissing 是写入前的复核：所属子目录现在仍可用、文件仍不存在（不用缓存，现查）。
func managedFileMissing(base, value string) bool {
	clean, root, ok := managedPathRoot(value)
	if !ok || !managedDirAvailable(base) || !managedDirAvailable(filepath.Join(base, root)) {
		return false
	}
	_, err := os.Lstat(filepath.Join(base, clean))
	return os.IsNotExist(err)
}

func (s *DataHealthService) managedRoot() string {
	return filepath.Join(s.dataDir, "media-details")
}

// scanManagedPaths 按 id 分页（每页 500）读出非空的路径列：所属子目录不可用的行记进 unavailable（按子目录计数），
// 子目录可用而文件不存在的交给 visit。
func (s *DataHealthService) scanManagedPaths(ctx context.Context, db *gorm.DB, spec managedPathSpec, roots *managedRoots,
	unavailable map[string]int64, visit func([]managedPathRow) error) error {
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
			clean, root, ok := managedPathRoot(row.Value)
			if !ok {
				continue
			}
			if !roots.available(root) {
				unavailable[root]++
				continue
			}
			if _, err := os.Lstat(filepath.Join(roots.base, clean)); os.IsNotExist(err) {
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
// 所属子目录不可用的行不判，单独列为「目录不可用」（只报告）。
func (s *DataHealthService) checkManagedPaths(ctx context.Context, db *gorm.DB) (DataHealthCheckResult, error) {
	roots := newManagedRoots(s.managedRoot())
	unavailable := map[string]int64{}
	groups := []DataHealthGroup{}
	var total int64
	for _, spec := range managedPathSpecs {
		group := DataHealthGroup{Key: spec.key, Label: spec.label, Repairable: true, Examples: []string{}}
		err := s.scanManagedPaths(ctx, db, spec, roots, unavailable, func(rows []managedPathRow) error {
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
	unavailableRows, unavailableGroups := managedUnavailableGroups(unavailable)
	groups = append(groups, unavailableGroups...)
	result := DataHealthCheckResult{Status: DataHealthStatusOK, Count: total, Groups: groups, Summary: "托管图片都在"}
	switch {
	case total > 0:
		result.Status, result.Repairable = DataHealthStatusProblem, true
		result.Summary = fmt.Sprintf("%d 条记录指向已不存在的托管图片，可以清空这些路径", total)
	case unavailableRows > 0:
		result.Status, result.Summary = DataHealthStatusWarning, "可检查的托管图片都在"
	}
	if unavailableRows > 0 {
		result.Summary += fmt.Sprintf("；%d 个托管目录不可用（不存在或链接失效），其下 %d 条记录未检查、修复也不会动它们",
			len(unavailableGroups), unavailableRows)
	}
	return result, nil
}

func managedUnavailableGroups(unavailable map[string]int64) (int64, []DataHealthGroup) {
	roots := make([]string, 0, len(unavailable))
	for root := range unavailable {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	var rows int64
	groups := make([]DataHealthGroup, 0, len(roots))
	for _, root := range roots {
		label := filepath.ToSlash(filepath.Join("media-details", root))
		rows += unavailable[root]
		groups = append(groups, DataHealthGroup{Key: "unavailable:" + root, Label: label + "（目录不可用）", Count: unavailable[root],
			Detail: fmt.Sprintf("目录不存在或链接失效，其下 %d 条未检查", unavailable[root]), Examples: []string{}})
	}
	return rows, groups
}

// repairManagedPaths 清空指向不存在文件的路径列：与检查同一个目录可用性守卫；每页一个短事务，写入前再现查一次
// 子目录仍可用、文件仍不存在，且只在列值未变时置空。
func (s *DataHealthService) repairManagedPaths(ctx context.Context, db *gorm.DB) (int64, error) {
	var cleared int64
	base := s.managedRoot()
	roots := newManagedRoots(base)
	unavailable := map[string]int64{}
	for _, spec := range managedPathSpecs {
		err := s.scanManagedPaths(ctx, db, spec, roots, unavailable, func(rows []managedPathRow) error {
			var batch int64
			err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				batch = 0
				for _, row := range rows {
					if !managedFileMissing(base, row.Value) {
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
