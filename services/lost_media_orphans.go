package services

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"video-master/database"
)

// 孤儿文件（D-LD-05，详细设计 1.4）：数据目录里以记录 id 命名、而所属记录在 Unscoped 下也查不到的缓存与副本。
// 软删进回收站的记录不算孤儿（Unscoped 能查到）。文件名解析不出 id 的一律不动。
//
// 壁纸副本（wallpapers/）不在这里：副本按内容哈希命名，库里也没有「当前壁纸设置」可以对照（只有系统桌面设置里有），
// 判定不了哪个文件还在用，按设计的判定依据无从实现，留待补上依据后再做。

// OrphanLocationProxies 是播放代理的位置键：沿用 playback_proxy_store.go 的孤儿统计与回收。
const OrphanLocationProxies = "proxies"

// orphanLocation 是一个按 id 命名的位置。dirs=true 时位置下的每个子目录名是 id（整个子目录归属那条记录）；
// 否则位置下的文件名按 pattern 的第一个分组解析出 id。
type orphanLocation struct {
	key     string
	label   string
	dir     []string
	table   string
	dirs    bool
	pattern *regexp.Regexp
}

var (
	orphanIDDirPattern = regexp.MustCompile(`^(\d+)$`)
	orphanLocations    = []orphanLocation{
		{key: "thumbnails", label: "视频缩略图与拖动预览帧", dir: []string{"thumbnails"}, table: "videos",
			pattern: regexp.MustCompile(`^(\d+)(?:\.jpg|\.seek\.f\d+-i\d+\.jpg)$`)},
		{key: "image_thumbnails", label: "图片缩略图", dir: []string{"image-thumbnails"}, table: "images",
			pattern: regexp.MustCompile(`^(\d+)(?:\.view)?\.jpg$`)},
		{key: "video_posters", label: "视频海报副本", dir: []string{"media-details", "videos"}, table: "videos", dirs: true, pattern: orphanIDDirPattern},
		{key: "person_images", label: "人物头像", dir: []string{"media-details", "people"}, table: "people", dirs: true, pattern: orphanIDDirPattern},
		{key: "collection_covers", label: "作品集封面", dir: []string{"media-details", "collections"}, table: "media_collections", dirs: true, pattern: orphanIDDirPattern},
		{key: "watchlist_posters", label: "想看海报", dir: []string{"media-details", "watchlist"}, table: "watchlist_entries", dirs: true, pattern: orphanIDDirPattern},
		{key: "subtitle_backups", label: "字幕备份", dir: []string{subtitleBackupDirName}, table: "videos", dirs: true, pattern: orphanIDDirPattern},
		{key: "faces", label: "人脸截图", dir: []string{facesDirName}, table: "face_observations", pattern: regexp.MustCompile(`^(\d+)\.jpg$`)},
	}
)

// OrphanFileLocation 是一个位置的孤儿文件个数与大小。
type OrphanFileLocation struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// OrphanFileReport 是孤儿文件统计（只读）。
type OrphanFileReport struct {
	Locations  []OrphanFileLocation `json:"locations"`
	TotalFiles int                  `json:"total_files"`
	TotalBytes int64                `json:"total_bytes"`
}

// OrphanReclaimResult 是一次回收的结果；删除失败只计数。
type OrphanReclaimResult struct {
	Files     int   `json:"files"`
	Bytes     int64 `json:"bytes"`
	Failed    int   `json:"failed"`
	Cancelled bool  `json:"cancelled"`
}

// orphanEntry 是位置下一个以 id 命名的文件或子目录。
type orphanEntry struct {
	id    uint
	path  string
	files int
	bytes int64
}

// listOrphanCandidates 列出位置下能解析出 id 的条目（还没判定归属）。目录不存在时为空。
func (s *LostMediaService) listOrphanCandidates(location orphanLocation) ([]orphanEntry, error) {
	if s.dataDir == "" || !filepath.IsAbs(s.dataDir) {
		return nil, errLostMediaNoDataDir
	}
	dir := filepath.Join(append([]string{s.dataDir}, location.dir...)...)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取%s目录失败: %w", location.label, pathlessError(err))
	}
	candidates := make([]orphanEntry, 0, len(entries))
	for _, entry := range entries {
		match := location.pattern.FindStringSubmatch(entry.Name())
		if match == nil || entry.IsDir() != location.dirs || (!location.dirs && !entry.Type().IsRegular()) {
			continue
		}
		id, parseErr := strconv.ParseUint(match[1], 10, 64)
		if parseErr != nil || id == 0 {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		candidate := orphanEntry{id: uint(id), path: path, files: 1, bytes: pathBytes(path)}
		if location.dirs {
			candidate.files = countFiles(path)
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

// countFiles 数目录里（递归）的文件个数。
func countFiles(dir string) int {
	count := 0
	_ = filepath.WalkDir(dir, func(_ string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			count++
		}
		return nil
	})
	return count
}

// existingIDs 返回 ids 里在 table 中（不看软删，等同 Unscoped）存在的那些（分批查询）。
func existingIDs(ctx context.Context, table string, ids []uint) (map[uint]bool, error) {
	ids = uniqueUintIDs(ids)
	existing := make(map[uint]bool, len(ids))
	for start := 0; start < len(ids); start += lostBatchSize {
		end := min(start+lostBatchSize, len(ids))
		var found []uint
		if err := database.DB.WithContext(ctx).Table(table).Where("id IN ?", ids[start:end]).Pluck("id", &found).Error; err != nil {
			return nil, err
		}
		for _, id := range found {
			existing[id] = true
		}
	}
	return existing, nil
}

// orphansIn 判定位置下哪些条目是孤儿（所属记录已彻底不存在）。
func (s *LostMediaService) orphansIn(ctx context.Context, location orphanLocation) ([]orphanEntry, error) {
	candidates, err := s.listOrphanCandidates(location)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}
	ids := make([]uint, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.id)
	}
	existing, err := existingIDs(ctx, location.table, ids)
	if err != nil {
		return nil, fmt.Errorf("核对%s归属失败: %w", location.label, err)
	}
	orphans := make([]orphanEntry, 0)
	for _, candidate := range candidates {
		if !existing[candidate.id] {
			orphans = append(orphans, candidate)
		}
	}
	return orphans, nil
}

// GetOrphanFileReport 按位置统计孤儿文件（只读）。播放代理的数字来自 GetPlaybackProxyUsage 的孤儿统计。
func (s *LostMediaService) GetOrphanFileReport(ctx context.Context) (*OrphanFileReport, error) {
	report := &OrphanFileReport{Locations: []OrphanFileLocation{}}
	for _, location := range orphanLocations {
		orphans, err := s.orphansIn(ctx, location)
		if err != nil {
			return nil, err
		}
		row := OrphanFileLocation{Key: location.key, Label: location.label}
		for _, orphan := range orphans {
			row.Files += orphan.files
			row.Bytes += orphan.bytes
		}
		report.add(row)
	}
	if s.proxies != nil && s.proxies.dirAvailable() {
		usage, err := s.proxies.GetPlaybackProxyUsage()
		if err != nil {
			return nil, err
		}
		report.add(OrphanFileLocation{Key: OrphanLocationProxies, Label: "播放兼容缓存", Files: usage.OrphanCount, Bytes: usage.OrphanBytes})
	}
	return report, nil
}

func (r *OrphanFileReport) add(location OrphanFileLocation) {
	r.Locations = append(r.Locations, location)
	r.TotalFiles += location.Files
	r.TotalBytes += location.Bytes
}

// OrphanFileTotals 是孤儿文件的合计（体检接入用）。
func (s *LostMediaService) OrphanFileTotals(ctx context.Context) (files int, bytes int64, err error) {
	report, err := s.GetOrphanFileReport(ctx)
	if err != nil {
		return 0, 0, err
	}
	return report.TotalFiles, report.TotalBytes, nil
}

// ReclaimOrphanFiles 回收所选位置的孤儿文件（在 lost_media 任务键下运行）。每个位置删除前现读一遍归属，
// 每一批（500 个 id）删除前再按 id 核对一次；删除失败只计数。未知位置键报错。
func (s *LostMediaService) ReclaimOrphanFiles(parent context.Context, keys []string) (*OrphanReclaimResult, error) {
	selected := map[string]bool{}
	for _, key := range keys {
		known := key == OrphanLocationProxies
		for _, location := range orphanLocations {
			known = known || location.key == key
		}
		if !known {
			return nil, lostMediaInvalid("未知的孤儿文件位置")
		}
		selected[key] = true
	}
	if len(selected) == 0 {
		return nil, lostMediaInvalid("没有选择要回收的位置")
	}
	ctx, finish, err := s.begin(parent, "reclaim", len(selected))
	if err != nil {
		return nil, err
	}
	defer finish()
	result := &OrphanReclaimResult{}
	for _, location := range orphanLocations {
		if !selected[location.key] {
			continue
		}
		if ctx.Err() != nil {
			result.Cancelled = true
			return result, nil
		}
		if err := s.reclaimLocation(ctx, location, result); err != nil {
			return result, err
		}
		s.advance()
	}
	if selected[OrphanLocationProxies] && s.proxies != nil && ctx.Err() == nil {
		removed, bytes, failed, err := s.proxies.RemoveOrphanPlaybackProxies()
		if err != nil {
			return result, err
		}
		result.Files, result.Bytes, result.Failed = result.Files+removed, result.Bytes+bytes, result.Failed+failed
		s.advance()
	}
	result.Cancelled = ctx.Err() != nil
	log.Printf("孤儿文件回收 locations=%d files=%d bytes=%d failed=%d", len(selected), result.Files, result.Bytes, result.Failed)
	return result, nil
}

func (s *LostMediaService) reclaimLocation(ctx context.Context, location orphanLocation, result *OrphanReclaimResult) error {
	orphans, err := s.orphansIn(ctx, location)
	if err != nil {
		return err
	}
	for start := 0; start < len(orphans); start += lostBatchSize {
		if ctx.Err() != nil {
			return nil
		}
		batch := orphans[start:min(start+lostBatchSize, len(orphans))]
		ids := make([]uint, 0, len(batch))
		for _, orphan := range batch {
			ids = append(ids, orphan.id)
		}
		existing, err := existingIDs(ctx, location.table, ids)
		if err != nil {
			return fmt.Errorf("核对%s归属失败: %w", location.label, err)
		}
		for _, orphan := range batch {
			if existing[orphan.id] {
				continue
			}
			if err := os.RemoveAll(orphan.path); err != nil {
				result.Failed += orphan.files
				continue
			}
			result.Files += orphan.files
			result.Bytes += orphan.bytes
		}
	}
	return nil
}
