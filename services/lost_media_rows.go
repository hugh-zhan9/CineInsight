package services

import (
	"context"
	"path/filepath"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// lostRow 是一条失联记录（两种存法、两类媒体共用）。
type lostRow struct {
	ID          uint
	Name        string
	Path        string
	Directory   string
	Size        int64
	IsStale     bool
	StaleReason string
	DeletedBy   string
	DeletedAt   *time.Time
}

func (r lostRow) deleted() bool { return r.DeletedAt != nil }

func (r lostRow) source() string {
	if r.deleted() {
		return LostSourceScannerTrash
	}
	return LostSourceStale
}

// lostVideoCondition 是视频的两种存法（详细设计 1.1）：活跃的失效行，以及扫描器因文件缺失软删、
// 回收站条目仍是 missing 的行（与 addScannedVideo 自动恢复认的条目同一口径）。用户删除的不算。
func lostVideoCondition(query *gorm.DB) *gorm.DB {
	return query.Where("((videos.deleted_at IS NULL AND videos.is_stale = ?) OR (videos.deleted_at IS NOT NULL AND videos.deleted_by = ? AND EXISTS ("+
		"SELECT 1 FROM video_trash_entries lost_entry WHERE lost_entry.video_id = videos.id AND lost_entry.deleted_by = ? "+
		"AND lost_entry.file_moved = ? AND lost_entry.state = ? AND lost_entry.mode IN ?)))",
		true, "scanner", "scanner", false, trashStateDeleted, []string{models.TrashModeMissing, ""})
}

// lostImageCondition 是图片的两种存法：活跃且 is_stale，或扫描器软删且 is_stale（自动恢复的标记）。
func lostImageCondition(query *gorm.DB) *gorm.DB {
	return query.Where("images.is_stale = ? AND (images.deleted_at IS NULL OR images.deleted_by = ?)", true, "scanner")
}

// lostConditionQuery 是某类媒体失联条件的查询（不带列选择，可用于计数）。
func lostConditionQuery(db *gorm.DB, kind string) *gorm.DB {
	if kind == LostMediaKindVideo {
		return lostVideoCondition(db.Table("videos"))
	}
	return lostImageCondition(db.Table("images"))
}

// lostRowsQuery 是某类媒体的失联记录查询（不含软删条件，列都带表名）。
func lostRowsQuery(db *gorm.DB, kind string) *gorm.DB {
	table := lostTable(kind)
	columns := table + ".id, " + table + ".name, " + table + ".path, " + table + ".directory, " + table + ".size, " +
		table + ".is_stale, " + table + ".deleted_by, " + table + ".deleted_at"
	if kind == LostMediaKindVideo {
		columns += ", videos.stale_reason"
	}
	return lostConditionQuery(db, kind).Select(columns)
}

func lostTable(kind string) string {
	if kind == LostMediaKindVideo {
		return "videos"
	}
	return "images"
}

// forEachLostRowBatch 按 id 键集分批读出全部失联记录（每批 500 条）。
func forEachLostRowBatch(ctx context.Context, kind string, extra func(*gorm.DB) *gorm.DB, fn func([]lostRow) error) error {
	table := lostTable(kind)
	var after uint
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		query := lostRowsQuery(database.DB.WithContext(ctx), kind).Where(table+".id > ?", after)
		if extra != nil {
			query = extra(query)
		}
		var rows []lostRow
		if err := query.Order(table + ".id ASC").Limit(lostBatchSize).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		if err := fn(rows); err != nil {
			return err
		}
		if len(rows) < lostBatchSize {
			return nil
		}
		after = rows[len(rows)-1].ID
	}
}

// lostBatchSize 是读与写的批大小（公共约定：每批最多 500 条）。
const lostBatchSize = 500

// loadLostRowsByID 读出给定 id 里仍处于失联状态的记录（分批，避开 Postgres 绑定参数上限）。
func loadLostRowsByID(ctx context.Context, kind string, ids []uint) (map[uint]lostRow, error) {
	table := lostTable(kind)
	found := make(map[uint]lostRow, len(ids))
	ids = uniqueUintIDs(ids)
	for start := 0; start < len(ids); start += lostBatchSize {
		end := min(start+lostBatchSize, len(ids))
		var rows []lostRow
		if err := lostRowsQuery(database.DB.WithContext(ctx), kind).Where(table+".id IN ?", ids[start:end]).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			found[row.ID] = row
		}
	}
	return found, nil
}

// lostClassifier 给失联记录归类。图片的原因按 image_visibility.go 现算（hiddenImageReason），按根缓存在线状态。
type lostClassifier struct {
	kind       string
	imageRoots []string
	rootOnline map[string]bool
}

func newLostClassifier(kind string) (*lostClassifier, error) {
	classifier := &lostClassifier{kind: kind, rootOnline: map[string]bool{}}
	if kind == LostMediaKindImage {
		roots, err := activeImageRoots(database.DB)
		if err != nil {
			return nil, err
		}
		classifier.imageRoots = roots
	}
	return classifier, nil
}

func (c *lostClassifier) reason(row lostRow) string {
	if row.deleted() {
		return LostReasonMissing
	}
	if c.kind == LostMediaKindImage {
		return lostReasonFromStale(hiddenImageReason(row.Path, c.imageRoots, c.rootOnline))
	}
	return lostReasonFromStale(row.StaleReason)
}

// lostReasonFromStale 把 stale_reason（或图片现算的原因）映射到归类；未记录与未知取值都是「原因未知」。
func lostReasonFromStale(reason string) string {
	switch reason {
	case models.StaleReasonOfflineRoot:
		return LostReasonOffline
	case models.StaleReasonReadError:
		return LostReasonReadError
	case models.StaleReasonMissingFile, models.StaleReasonWatcherMissing:
		return LostReasonMissing
	case models.StaleReasonRemovedRoot:
		return LostReasonRemovedRoot
	case models.StaleReasonOutsideRoots:
		return LostReasonOutsideRoots
	}
	return LostReasonUnknown
}

// activeImageRoots / activeVideoRoots 是当前配置的扫描根（已清理、去重）。
func activeImageRoots(db *gorm.DB) ([]string, error) {
	var dirs []models.ImageDirectory
	if err := db.Find(&dirs).Error; err != nil {
		return nil, err
	}
	roots := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		roots = append(roots, dir.Path)
	}
	return cleanRootPaths(roots), nil
}

func activeVideoRoots(db *gorm.DB) ([]string, error) {
	var dirs []models.ScanDirectory
	if err := db.Find(&dirs).Error; err != nil {
		return nil, err
	}
	return cleanScanRoots(dirs), nil
}

func activeMediaRoots(kind string) ([]string, error) {
	if kind == LostMediaKindVideo {
		return activeVideoRoots(database.DB)
	}
	return activeImageRoots(database.DB)
}

func cleanRootPaths(paths []string) []string {
	roots := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		root := filepath.Clean(strings.TrimSpace(path))
		if root == "" || root == "." {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	return roots
}

// pathUnderRoot 报告 path 是否就是 root 或在它里面（按字符串前缀，大小写敏感，与 videoBelongsToRoots 同口径）。
func pathUnderRoot(path, root string) bool {
	return path == root || strings.HasPrefix(path, scanRootChildPrefix(root))
}

// pathInRoots 报告 path 是否落在 roots 的任一个里。
func pathInRoots(path string, roots []string) bool {
	path = filepath.Clean(path)
	for _, root := range roots {
		if pathUnderRoot(path, root) {
			return true
		}
	}
	return false
}
