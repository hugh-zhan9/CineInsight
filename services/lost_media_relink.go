package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"video-master/database"

	"gorm.io/gorm"
)

// 找回（D-LD-03，详细设计 1.2）：两种配对都只读取候选、不写库；执行时逐条在路径锁内复核，保持记录 id。

// 配对结果。
const (
	LostMatchExact        = "exact"
	LostMatchSizeMismatch = "size_mismatch"
	LostMatchNameSize     = "name_size"
	LostMatchSizeOnly     = "size_only"
	LostMatchAmbiguous    = "ambiguous"
)

// LostRelinkMoveRequest 是「目录搬家」：OldDirectory 下（含子目录）的失联条目按相对路径拼到 NewDirectory 下。
type LostRelinkMoveRequest struct {
	Kind         string `json:"kind"`
	OldDirectory string `json:"old_directory"`
	NewDirectory string `json:"new_directory"`
}

// LostRelinkSearchRequest 是「在文件夹里找」：递归列出 Directory 里的媒体文件；ItemIDs 为空时匹配全部可找回的条目。
type LostRelinkSearchRequest struct {
	Kind      string `json:"kind"`
	Directory string `json:"directory"`
	ItemIDs   []uint `json:"item_ids"`
}

// LostRelinkCandidate 是一条配对候选。Selectable：可以执行；Selected：默认勾选（exact / name_size）。
// OutsideRoots：新路径不在任何当前扫描根内；Occupied：新路径已被另一条活动记录占用。两者都不可执行。
type LostRelinkCandidate struct {
	ID           uint     `json:"id"`
	Name         string   `json:"name"`
	OldPath      string   `json:"old_path"`
	Size         int64    `json:"size"`
	NewPath      string   `json:"new_path"`
	NewSize      int64    `json:"new_size"`
	Match        string   `json:"match"`
	OutsideRoots bool     `json:"outside_roots"`
	Occupied     bool     `json:"occupied"`
	Selectable   bool     `json:"selectable"`
	Selected     bool     `json:"selected"`
	Candidates   []string `json:"candidates"`
}

// LostRelinkPreview 是一次配对预览；Unmatched 是一个候选都没找到的可找回条目数。
type LostRelinkPreview struct {
	Kind      string                `json:"kind"`
	Items     []LostRelinkCandidate `json:"items"`
	Unmatched int                   `json:"unmatched"`
}

// cleanAbsoluteDir 把用户选的目录清理成绝对路径，并要求它是一个存在的目录（找回的新位置与搜索目录）。
func cleanAbsoluteDir(path string, mustExist bool) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", lostMediaInvalid("请选择一个文件夹")
	}
	path = filepath.Clean(path)
	if mustExist {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return "", lostMediaInvalid("所选文件夹不可用")
		}
	}
	return path, nil
}

// PreviewLostRelinkMove 按「相对路径相同 + 大小相同」配对：文件存在且大小相同为 exact，大小不同为 size_mismatch
// （不可勾选），不存在的不列出（计入 Unmatched）。
func (s *LostMediaService) PreviewLostRelinkMove(ctx context.Context, request LostRelinkMoveRequest) (*LostRelinkPreview, error) {
	if err := validLostMediaKind(request.Kind); err != nil {
		return nil, err
	}
	oldDir, err := cleanAbsoluteDir(request.OldDirectory, false)
	if err != nil {
		return nil, err
	}
	newDir, err := cleanAbsoluteDir(request.NewDirectory, true)
	if err != nil {
		return nil, err
	}
	if oldDir == newDir {
		return nil, lostMediaInvalid("新位置与旧目录相同")
	}
	classifier, err := newLostClassifier(request.Kind)
	if err != nil {
		return nil, fmt.Errorf("读取扫描目录失败: %w", err)
	}
	table := lostTable(request.Kind)
	under := func(query *gorm.DB) *gorm.DB {
		// SQL 只做粗筛（SQLite 的 LIKE 对 ASCII 不区分大小写），以下面的 pathUnderRoot 为准。
		return query.Where(table+".path LIKE ? ESCAPE '\\'", escapeSQLLikePrefix(scanRootChildPrefix(oldDir))+"%")
	}
	preview := &LostRelinkPreview{Kind: request.Kind, Items: []LostRelinkCandidate{}}
	err = forEachLostRowBatch(ctx, request.Kind, under, func(rows []lostRow) error {
		for _, row := range rows {
			if !pathUnderRoot(row.Path, oldDir) || !lostReasonActionable(classifier.reason(row)) {
				continue
			}
			relative, relErr := filepath.Rel(oldDir, row.Path)
			if relErr != nil || relative == "." || strings.HasPrefix(relative, "..") {
				continue
			}
			newPath := filepath.Join(newDir, relative)
			info, statErr := os.Stat(newPath)
			if statErr != nil || !info.Mode().IsRegular() {
				preview.Unmatched++
				continue
			}
			match := LostMatchExact
			if info.Size() != row.Size {
				match = LostMatchSizeMismatch
			}
			preview.Items = append(preview.Items, lostRelinkCandidateOf(row, newPath, info.Size(), match))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("读取失联条目失败: %w", err)
	}
	if err := finishRelinkPreview(request.Kind, preview); err != nil {
		return nil, err
	}
	return preview, nil
}

func lostRelinkCandidateOf(row lostRow, newPath string, newSize int64, match string) LostRelinkCandidate {
	return LostRelinkCandidate{ID: row.ID, Name: row.Name, OldPath: row.Path, Size: row.Size,
		NewPath: newPath, NewSize: newSize, Match: match, Candidates: []string{}}
}

// finishRelinkPreview 给候选标上 outside_roots / occupied，并算出可执行与默认勾选。
func finishRelinkPreview(kind string, preview *LostRelinkPreview) error {
	roots, err := activeMediaRoots(kind)
	if err != nil {
		return fmt.Errorf("读取扫描目录失败: %w", err)
	}
	paths := make([]string, 0, len(preview.Items))
	for _, item := range preview.Items {
		if item.NewPath != "" {
			paths = append(paths, item.NewPath)
		}
	}
	occupants, err := activePathOwners(kind, paths)
	if err != nil {
		return fmt.Errorf("检查新路径占用失败: %w", err)
	}
	for i := range preview.Items {
		item := &preview.Items[i]
		if item.NewPath == "" {
			continue
		}
		item.OutsideRoots = !pathInRoots(item.NewPath, roots)
		if owner, ok := occupants[item.NewPath]; ok && owner != item.ID {
			item.Occupied = true
		}
		pairable := item.Match == LostMatchExact || item.Match == LostMatchNameSize || item.Match == LostMatchSizeOnly
		item.Selectable = pairable && !item.OutsideRoots && !item.Occupied
		item.Selected = item.Selectable && item.Match != LostMatchSizeOnly
	}
	return nil
}

// activePathOwners 返回这些路径上活动记录的 id（分批查询）。
func activePathOwners(kind string, paths []string) (map[string]uint, error) {
	owners := make(map[string]uint, len(paths))
	table := lostTable(kind)
	for start := 0; start < len(paths); start += lostBatchSize {
		end := min(start+lostBatchSize, len(paths))
		var rows []struct {
			ID   uint
			Path string
		}
		if err := database.DB.Table(table).Select("id, path").
			Where(table+".deleted_at IS NULL AND "+table+".path IN ?", paths[start:end]).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			owners[row.Path] = row.ID
		}
	}
	return owners, nil
}

// PreviewLostRelinkSearch 在文件夹里找：「文件名 + 大小」唯一命中为 name_size（默认勾选）；否则「大小 + 扩展名」唯一命中
// 为 size_only（默认不勾）；多个候选为 ambiguous（列出候选、不配对）；同一个文件被多条记录命中时这几条都降为 ambiguous。
func (s *LostMediaService) PreviewLostRelinkSearch(ctx context.Context, request LostRelinkSearchRequest) (*LostRelinkPreview, error) {
	if err := validLostMediaKind(request.Kind); err != nil {
		return nil, err
	}
	dir, err := cleanAbsoluteDir(request.Directory, true)
	if err != nil {
		return nil, err
	}
	files, err := s.listMediaFiles(ctx, request.Kind, dir)
	if err != nil {
		return nil, err
	}
	rows, err := s.relinkSearchRows(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("读取失联条目失败: %w", err)
	}
	preview := &LostRelinkPreview{Kind: request.Kind, Items: []LostRelinkCandidate{}}
	preview.Items, preview.Unmatched = pairLostRowsByName(rows, files)
	if err := finishRelinkPreview(request.Kind, preview); err != nil {
		return nil, err
	}
	return preview, nil
}

// relinkSearchRows 取待找回的条目：给了 ItemIDs 就只取它们，否则取全部；都只留可找回的归类。
func (s *LostMediaService) relinkSearchRows(ctx context.Context, request LostRelinkSearchRequest) ([]lostRow, error) {
	classifier, err := newLostClassifier(request.Kind)
	if err != nil {
		return nil, err
	}
	rows := []lostRow{}
	keep := func(row lostRow) {
		if lostReasonActionable(classifier.reason(row)) {
			rows = append(rows, row)
		}
	}
	if len(request.ItemIDs) > 0 {
		found, err := loadLostRowsByID(ctx, request.Kind, request.ItemIDs)
		if err != nil {
			return nil, err
		}
		for _, id := range uniqueUintIDs(request.ItemIDs) {
			if row, ok := found[id]; ok {
				keep(row)
			}
		}
		return rows, nil
	}
	err = forEachLostRowBatch(ctx, request.Kind, nil, func(batch []lostRow) error {
		for _, row := range batch {
			keep(row)
		}
		return nil
	})
	return rows, err
}

type lostNameSizeKey struct {
	name string
	size int64
}

type lostSizeExtKey struct {
	size int64
	ext  string
}

// pairLostRowsByName 是搜索配对的纯逻辑（便于单测）：返回候选与一个候选都没有的条目数。
func pairLostRowsByName(rows []lostRow, files []ScannedFile) ([]LostRelinkCandidate, int) {
	byNameSize := map[lostNameSizeKey][]ScannedFile{}
	bySizeExt := map[lostSizeExtKey][]ScannedFile{}
	for _, file := range files {
		byNameSize[lostNameSizeKey{filepath.Base(file.Path), file.Size}] = append(byNameSize[lostNameSizeKey{filepath.Base(file.Path), file.Size}], file)
		key := lostSizeExtKey{file.Size, strings.ToLower(filepath.Ext(file.Path))}
		bySizeExt[key] = append(bySizeExt[key], file)
	}
	items := make([]LostRelinkCandidate, 0, len(rows))
	unmatched := 0
	hits := map[string]int{}
	for _, row := range rows {
		match, candidates := LostMatchNameSize, byNameSize[lostNameSizeKey{filepath.Base(row.Path), row.Size}]
		if len(candidates) == 0 {
			match, candidates = LostMatchSizeOnly, bySizeExt[lostSizeExtKey{row.Size, strings.ToLower(filepath.Ext(row.Path))}]
		}
		if len(candidates) == 0 {
			unmatched++
			continue
		}
		item := lostRelinkCandidateOf(row, "", 0, LostMatchAmbiguous)
		if len(candidates) == 1 {
			item.Match, item.NewPath, item.NewSize = match, candidates[0].Path, candidates[0].Size
			hits[item.NewPath]++
		}
		for _, candidate := range candidates {
			item.Candidates = append(item.Candidates, candidate.Path)
		}
		items = append(items, item)
	}
	for i := range items {
		if items[i].NewPath != "" && hits[items[i].NewPath] > 1 {
			items[i].Match, items[i].NewPath, items[i].NewSize = LostMatchAmbiguous, "", 0
		}
	}
	return items, unmatched
}

// listMediaFiles 递归列出文件夹里的媒体文件，扩展名白名单、黑名单与回收站目录的跳过规则与扫描一致
// （视频复用 scanDirectoryCollectContext，图片复用 scanImageDirectory）。
func (s *LostMediaService) listMediaFiles(ctx context.Context, kind, dir string) ([]ScannedFile, error) {
	if kind == LostMediaKindVideo {
		videos := s.videos
		if videos == nil {
			videos = &VideoService{}
		}
		files, err := videos.scanDirectoryCollectContext(ctx, dir, false, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("读取所选文件夹失败: %w", pathlessError(err))
		}
		return files, nil
	}
	refreshLegacyTrashDirsContext(ctx)
	var settings struct {
		ImageExtensions       string
		ScanExcludePaths      string
		ImageScanExcludePaths string
	}
	if err := database.DB.WithContext(ctx).Table("settings").
		Select("image_extensions, scan_exclude_paths, image_scan_exclude_paths").Limit(1).Scan(&settings).Error; err != nil {
		return nil, fmt.Errorf("获取设置失败: %w", err)
	}
	excluded := parseScanExcludePaths(settings.ImageScanExcludePaths)
	if len(excluded) == 0 {
		excluded = parseScanExcludePaths(settings.ScanExcludePaths)
	}
	files, err := scanImageDirectory(dir, parseImageExtensions(settings.ImageExtensions), excluded)
	if err != nil {
		return nil, fmt.Errorf("读取所选文件夹失败: %w", pathlessError(err))
	}
	return files, nil
}
