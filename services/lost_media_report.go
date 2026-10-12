package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"video-master/database"
	"video-master/models"
)

// LostMediaItem 是一条失联条目。Purgeable 同时表示可找回（两者同一口径，详细设计 1.1）。
type LostMediaItem struct {
	ID        uint   `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Directory string `json:"directory"`
	Size      int64  `json:"size"`
	Source    string `json:"source"` // stale / scanner_trash
	Reason    string `json:"reason"` // LostReason*
	Purgeable bool   `json:"purgeable"`
}

// LostMediaReport 是失联报告：按「扫描根 → 目录」分组，组内只给计数、总大小与前 5 条示例。
type LostMediaReport struct {
	Kind      string               `json:"kind"`
	Total     int                  `json:"total"`
	TotalSize int64                `json:"total_size"`
	Purgeable int                  `json:"purgeable"`
	Reasons   map[string]int       `json:"reasons"`
	Roots     []LostMediaRootGroup `json:"roots"`
}

// LostMediaRootGroup 是一个扫描根下的失联条目。State：active（当前扫描根）/ removed（已移除的根，
// 从路径前缀推断）/ other（推不出来，「其他位置」，Root 为空）。
type LostMediaRootGroup struct {
	Root        string                    `json:"root"`
	Label       string                    `json:"label"`
	State       string                    `json:"state"`
	Count       int                       `json:"count"`
	Size        int64                     `json:"size"`
	Purgeable   int                       `json:"purgeable"`
	Reasons     map[string]int            `json:"reasons"`
	Directories []LostMediaDirectoryGroup `json:"directories"`
}

// LostMediaDirectoryGroup 是根下一个目录（videos.directory / images.directory）的失联条目。
type LostMediaDirectoryGroup struct {
	Directory string          `json:"directory"`
	Count     int             `json:"count"`
	Size      int64           `json:"size"`
	Purgeable int             `json:"purgeable"`
	Reasons   map[string]int  `json:"reasons"`
	Samples   []LostMediaItem `json:"samples"`
}

const (
	lostRootActive  = "active"
	lostRootRemoved = "removed"
	lostRootOther   = "other"
	lostSampleSize  = 5
)

type lostRootCandidate struct {
	path  string
	label string
	state string
}

// lostRootIndex 把路径归到所属扫描根：最长的当前根优先，其次最长的已移除根，都不是就是「其他位置」。
type lostRootIndex struct {
	active  []lostRootCandidate
	removed []lostRootCandidate
}

func loadLostRootIndex(kind string) (*lostRootIndex, error) {
	type dirRow struct {
		path, alias string
		removed     bool
	}
	var rows []dirRow
	if kind == LostMediaKindVideo {
		var dirs []models.ScanDirectory
		if err := database.DB.Unscoped().Order("id ASC").Find(&dirs).Error; err != nil {
			return nil, fmt.Errorf("读取扫描目录失败: %w", err)
		}
		for _, dir := range dirs {
			rows = append(rows, dirRow{dir.Path, dir.Alias, dir.DeletedAt.IsValid()})
		}
	} else {
		var dirs []models.ImageDirectory
		if err := database.DB.Unscoped().Order("id ASC").Find(&dirs).Error; err != nil {
			return nil, fmt.Errorf("读取图片扫描目录失败: %w", err)
		}
		for _, dir := range dirs {
			rows = append(rows, dirRow{dir.Path, dir.Alias, dir.DeletedAt.IsValid()})
		}
	}
	index := &lostRootIndex{}
	seen := map[string]bool{}
	// 先收当前根，再收已移除的根：同一路径既有当前行又有已移除行时按当前根算。
	for _, wantRemoved := range []bool{false, true} {
		for _, row := range rows {
			root := cleanRootPaths([]string{row.path})
			if len(root) == 0 || row.removed != wantRemoved || seen[root[0]] {
				continue
			}
			seen[root[0]] = true
			candidate := lostRootCandidate{path: root[0], label: rootLabel(root[0], row.alias), state: lostRootActive}
			if wantRemoved {
				candidate.state = lostRootRemoved
				index.removed = append(index.removed, candidate)
			} else {
				index.active = append(index.active, candidate)
			}
		}
	}
	return index, nil
}

func rootLabel(path, alias string) string {
	if alias = strings.TrimSpace(alias); alias != "" {
		return alias
	}
	return path
}

func (index *lostRootIndex) locate(path string) lostRootCandidate {
	for _, candidates := range [][]lostRootCandidate{index.active, index.removed} {
		best := lostRootCandidate{}
		for _, candidate := range candidates {
			if pathUnderRoot(path, candidate.path) && len(candidate.path) > len(best.path) {
				best = candidate
			}
		}
		if best.path != "" {
			return best
		}
	}
	return lostRootCandidate{label: "其他位置", state: lostRootOther}
}

func lostItemOf(row lostRow, reason string) LostMediaItem {
	return LostMediaItem{
		ID: row.ID, Name: row.Name, Path: row.Path, Directory: row.Directory, Size: row.Size,
		Source: row.source(), Reason: reason, Purgeable: lostReasonActionable(reason),
	}
}

// GetLostMediaReport 返回某类媒体的失联报告（只读）。
func (s *LostMediaService) GetLostMediaReport(ctx context.Context, kind string) (*LostMediaReport, error) {
	if err := validLostMediaKind(kind); err != nil {
		return nil, err
	}
	classifier, err := newLostClassifier(kind)
	if err != nil {
		return nil, fmt.Errorf("读取扫描目录失败: %w", err)
	}
	index, err := loadLostRootIndex(kind)
	if err != nil {
		return nil, err
	}
	report := &LostMediaReport{Kind: kind, Reasons: map[string]int{}, Roots: []LostMediaRootGroup{}}
	roots := map[string]*LostMediaRootGroup{}
	directories := map[string]map[string]*LostMediaDirectoryGroup{}
	err = forEachLostRowBatch(ctx, kind, nil, func(rows []lostRow) error {
		for _, row := range rows {
			item := lostItemOf(row, classifier.reason(row))
			root := index.locate(row.Path)
			group := roots[root.path]
			if group == nil {
				group = &LostMediaRootGroup{Root: root.path, Label: root.label, State: root.state, Reasons: map[string]int{}}
				roots[root.path] = group
				directories[root.path] = map[string]*LostMediaDirectoryGroup{}
			}
			dir := directories[root.path][row.Directory]
			if dir == nil {
				dir = &LostMediaDirectoryGroup{Directory: row.Directory, Reasons: map[string]int{}, Samples: []LostMediaItem{}}
				directories[root.path][row.Directory] = dir
			}
			report.Total++
			report.TotalSize += row.Size
			report.Reasons[item.Reason]++
			group.Count++
			group.Size += row.Size
			group.Reasons[item.Reason]++
			dir.Count++
			dir.Size += row.Size
			dir.Reasons[item.Reason]++
			if item.Purgeable {
				report.Purgeable++
				group.Purgeable++
				dir.Purgeable++
			}
			if len(dir.Samples) < lostSampleSize {
				dir.Samples = append(dir.Samples, item)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("读取失联条目失败: %w", err)
	}
	for key, group := range roots {
		for _, dir := range directories[key] {
			group.Directories = append(group.Directories, *dir)
		}
		sort.Slice(group.Directories, func(i, j int) bool { return group.Directories[i].Directory < group.Directories[j].Directory })
		report.Roots = append(report.Roots, *group)
	}
	stateOrder := map[string]int{lostRootActive: 0, lostRootRemoved: 1, lostRootOther: 2}
	sort.Slice(report.Roots, func(i, j int) bool {
		a, b := report.Roots[i], report.Roots[j]
		if stateOrder[a.State] != stateOrder[b.State] {
			return stateOrder[a.State] < stateOrder[b.State]
		}
		return a.Root < b.Root
	})
	return report, nil
}

// LostMediaItemQuery 是组内明细查询：Directory 精确匹配，按 id 键集分页（AfterID 之后）。
type LostMediaItemQuery struct {
	Kind      string `json:"kind"`
	Directory string `json:"directory"`
	AfterID   uint   `json:"after_id"`
	Limit     int    `json:"limit"`
}

// LostMediaItemPage 是一页组内明细；HasMore 为真时用最后一条的 id 作为下一页的 AfterID。
type LostMediaItemPage struct {
	Items   []LostMediaItem `json:"items"`
	HasMore bool            `json:"has_more"`
}

// ListLostMediaItems 读一个目录分组里的失联条目（只读）。
func (s *LostMediaService) ListLostMediaItems(ctx context.Context, query LostMediaItemQuery) (*LostMediaItemPage, error) {
	if err := validLostMediaKind(query.Kind); err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, lostBatchSize)
	table := lostTable(query.Kind)
	var rows []lostRow
	if err := lostRowsQuery(database.DB.WithContext(ctx), query.Kind).
		Where(table+".directory = ? AND "+table+".id > ?", query.Directory, query.AfterID).
		Order(table + ".id ASC").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取失联条目失败: %w", err)
	}
	classifier, err := newLostClassifier(query.Kind)
	if err != nil {
		return nil, fmt.Errorf("读取扫描目录失败: %w", err)
	}
	page := &LostMediaItemPage{Items: make([]LostMediaItem, 0, len(rows))}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	for _, row := range rows {
		page.Items = append(page.Items, lostItemOf(row, classifier.reason(row)))
	}
	return page, nil
}
