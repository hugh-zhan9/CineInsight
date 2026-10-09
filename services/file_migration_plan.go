package services

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"video-master/database"
	"video-master/models"
)

// FileMigrationDirectory 固定用户所选路径及其解析后的真实目录和身份，提交时两者都要复查。
type FileMigrationDirectory struct {
	Path     string `json:"path"`
	RealPath string `json:"real_path"`
	Identity string `json:"identity"`
}

// FileMigrationSource 保存确认时的普通文件版本。Path 保留数据库路径，RealPath 用于文件操作。
type FileMigrationSource struct {
	Path      string `json:"path"`
	RealPath  string `json:"real_path"`
	Identity  string `json:"identity"`
	Size      int64  `json:"size"`
	ModTimeNS int64  `json:"mod_time_ns"`
	Mode      uint32 `json:"mode"`
}

// FileMigrationFile 是确切的单文件计划。CopyOnly 表示共享字幕，成功后也必须保留来源。
type FileMigrationFile struct {
	Kind        string              `json:"kind"`
	Source      FileMigrationSource `json:"source"`
	Destination string              `json:"destination"`
	CopyOnly    bool                `json:"copy_only"`
	CrossVolume bool                `json:"cross_volume"`
}

// FileMigrationItem 把视频与其可归属附件绑定；Stay 的文件全都保持原位。
type FileMigrationItem struct {
	VideoID         uint                `json:"video_id"`
	SourcePath      string              `json:"source_path"`
	DestinationPath string              `json:"destination_path"`
	Stay            bool                `json:"stay"`
	Files           []FileMigrationFile `json:"files"`
	Warnings        []string            `json:"warnings"`
}

type fileMigrationPlan struct {
	Destination      FileMigrationDirectory
	Items            []FileMigrationItem
	MoveBytes        int64
	CrossVolumeBytes int64
	CopyBytes        int64
	AvailableBytes   uint64
	Warnings         []string
	Errors           []string
}

type fileMigrationInventory struct {
	entries                map[string][]os.DirEntry
	videoExtensions        map[string]bool
	attachmentsByDirectory map[string]map[string][]os.DirEntry
	ownersByDirectory      map[string]map[string]int
	warningsByDirectory    map[string][]string
	warningsReported       map[string]bool
	canonicalParents       map[string]fileMigrationParent
	activePathOwners       map[string][]uint
	activePaths            []fileMigrationActivePath
	observePath            func(string) error
}

type fileMigrationActivePath struct {
	ID   uint
	Path string
}

type fileMigrationParent struct {
	realPath string
	key      string
}

var consolidationLanguageSuffix = regexp.MustCompile(`(?i)^\.[a-z]{2,3}(?:-[a-z0-9]{2,8})?(?:\.(?:forced|sdh|cc|default|[a-z]{2,4}))*$`)

func snapshotMigrationDirectory(path string) (FileMigrationDirectory, error) {
	if migrationPathHasParentTraversal(path) {
		return FileMigrationDirectory{}, fmt.Errorf("目标路径含有 .. 分量，请重新选择规范目录: %s", path)
	}
	if strings.TrimSpace(path) == "" {
		return FileMigrationDirectory{}, fmt.Errorf("目标目录不能为空")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return FileMigrationDirectory{}, err
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return FileMigrationDirectory{}, fmt.Errorf("读取目录 %s: %w", path, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return FileMigrationDirectory{}, err
	}
	if !info.IsDir() {
		return FileMigrationDirectory{}, fmt.Errorf("目标不是目录: %s", path)
	}
	identity := stableFileIdentity(info)
	if identity == "" {
		return FileMigrationDirectory{}, fmt.Errorf("无法核对目录身份: %s", path)
	}
	return FileMigrationDirectory{Path: absolute, RealPath: real, Identity: identity}, nil
}

func snapshotMigrationSource(path string) (FileMigrationSource, error) {
	if migrationPathHasParentTraversal(path) {
		return FileMigrationSource{}, fmt.Errorf("来源路径含有 .. 分量，请先核对库内路径: %s", path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return FileMigrationSource{}, err
	}
	// 允许 /var 等目录别名，但不把文件符号链接的指向物误当作用户授权的文件。
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return FileMigrationSource{}, err
	}
	real := filepath.Join(parent, filepath.Base(absolute))
	info, err := os.Lstat(real)
	if err != nil {
		return FileMigrationSource{}, err
	}
	if !info.Mode().IsRegular() {
		return FileMigrationSource{}, fmt.Errorf("不是普通文件: %s", path)
	}
	identity := stableFileIdentity(info)
	if identity == "" {
		return FileMigrationSource{}, fmt.Errorf("无法核对文件身份: %s", path)
	}
	if info.Mode().Perm()&0444 == 0 {
		return FileMigrationSource{}, fmt.Errorf("文件不可读: %s", path)
	}
	file, err := os.Open(real)
	if err != nil {
		return FileMigrationSource{}, err
	}
	opened, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return FileMigrationSource{}, statErr
	}
	if closeErr != nil {
		return FileMigrationSource{}, closeErr
	}
	if !os.SameFile(info, opened) || info.Size() != opened.Size() || !info.ModTime().Equal(opened.ModTime()) {
		return FileMigrationSource{}, fmt.Errorf("读取期间文件已变化: %s", path)
	}

	return FileMigrationSource{Path: path, RealPath: real, Identity: identity, Size: info.Size(), ModTimeNS: info.ModTime().UnixNano(), Mode: uint32(info.Mode().Perm())}, nil
}

func sameMigrationVolume(a, b string) bool {
	left, _, okLeft := strings.Cut(a, ":")
	right, _, okRight := strings.Cut(b, ":")
	return okLeft && okRight && left == right
}

func newFileMigrationInventory(ctx context.Context) (*fileMigrationInventory, error) {
	var settings models.Settings
	if err := database.DB.WithContext(ctx).Select("video_extensions").Limit(1).Find(&settings).Error; err != nil {
		return nil, err
	}
	extensions := make(map[string]bool)
	for _, ext := range strings.Split(defaultVideoExtensions+","+settings.VideoExtensions, ",") {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		extensions[ext] = true
	}
	return &fileMigrationInventory{entries: make(map[string][]os.DirEntry), videoExtensions: extensions,
		attachmentsByDirectory: make(map[string]map[string][]os.DirEntry),
		ownersByDirectory:      make(map[string]map[string]int), warningsByDirectory: make(map[string][]string), warningsReported: make(map[string]bool), canonicalParents: make(map[string]fileMigrationParent)}, nil
}

func migrationPathHasParentTraversal(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

const maxMigrationPathSymlinks = 40

// resolveMigrationPath 逐段读取链接，保留断开链接的真实目标占位。先处理链接再
// 处理 ..，避免把 link/../file 误清理成链接所在目录的 file。只有缺失路径可以
// 留作字面占位；循环、深度上限及其他读取错误都明确失败。
func resolveMigrationPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		workingDir, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = workingDir + string(filepath.Separator) + path
	}
	volume := filepath.VolumeName(path)
	resolved := volume + string(filepath.Separator)
	pending := strings.Split(filepath.ToSlash(strings.TrimPrefix(path, volume)), "/")
	seen := make(map[string]bool)
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, part)
		info, err := os.Lstat(next)
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.Join(append([]string{next}, pending...)...), nil
			}
			return "", fmt.Errorf("读取路径 %s: %w", next, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if !info.IsDir() && len(pending) > 0 {
				return "", fmt.Errorf("路径中间项不是目录: %s", next)
			}
			resolved = next
			continue
		}
		state := next + "\x00" + strings.Join(pending, "/")
		if seen[state] {
			return "", fmt.Errorf("路径符号链接存在循环: %s", next)
		}
		if links >= maxMigrationPathSymlinks {
			return "", fmt.Errorf("路径符号链接超过 %d 层: %s", maxMigrationPathSymlinks, path)
		}
		seen[state] = true
		links++
		target, err := os.Readlink(next)
		if err != nil {
			return "", fmt.Errorf("读取符号链接 %s: %w", next, err)
		}
		if filepath.IsAbs(target) {
			volume = filepath.VolumeName(target)
			resolved = volume + string(filepath.Separator)
			target = strings.TrimPrefix(target, volume)
		}
		pending = append(strings.Split(filepath.ToSlash(target), "/"), pending...)
	}
	return resolved, nil
}

// pathKey 以父目录身份和既有 NFC/大小写规则识别同一路径，不能以文件 inode
// 合并两个不同路径的硬链接。文件链接（含断开的链接）占用其真实目标路径。
func (inventory *fileMigrationInventory) pathKey(path string) (string, error) {
	if inventory.observePath != nil {
		if err := inventory.observePath(path); err != nil {
			return "", err
		}
	}
	// Abs/Dir 会清掉 ..；原始 DB 引用必须先遵循 OS 的链接遍历顺序。
	// 普通路径继续使用下方父目录缓存，避免逐文件重新遍历公共前缀。
	resolvedPath := path
	if migrationPathHasParentTraversal(path) {
		var err error
		resolvedPath, err = resolveMigrationPath(path)
		if err != nil {
			return "", fmt.Errorf("核对原始路径 %s: %w", path, err)
		}
	}
	absolute, err := filepath.Abs(resolvedPath)
	if err != nil {
		return "", err
	}
	// 解析后再次读取是为了取得目标父目录身份；外部持续更换链接也不能导致无限重入。
	for attempt := 0; attempt <= maxMigrationPathSymlinks; attempt++ {
		if inventory.observePath != nil {
			if err := inventory.observePath(absolute); err != nil {
				return "", err
			}
		}
		dir := filepath.Dir(absolute)
		parent, ok := inventory.canonicalParents[dir]
		if !ok {
			real, err := resolveMigrationPath(dir)
			if err != nil {
				return "", fmt.Errorf("核对目录别名 %s: %w", dir, err)
			}
			info, err := os.Stat(real)
			if err != nil {
				if !os.IsNotExist(err) {
					return "", err
				}
				parent = fileMigrationParent{realPath: real, key: "missing:" + subtitleFileLockKey(real)}
			} else {
				parent = fileMigrationParent{realPath: real, key: "directory:" + stableFileIdentity(info)}
			}
			inventory.canonicalParents[dir] = parent
		}
		realPath := filepath.Join(parent.realPath, filepath.Base(absolute))
		info, err := os.Lstat(realPath)
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("核对文件别名 %s: %w", path, err)
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			resolved, err := resolveMigrationPath(realPath)
			if err != nil {
				return "", fmt.Errorf("核对文件链接 %s: %w", path, err)
			}
			absolute = resolved
			continue
		}
		return parent.key + "/" + subtitleFileLockKey(filepath.Base(absolute)), nil
	}
	return "", fmt.Errorf("核对期间符号链接持续变化，请重新预览: %s", path)
}

// loadActivePaths 一次读取完整活跃库记录，不能只查分析成员或依赖 Name 列。
// 后续来源引用与目标占位均查同一索引，避免每个候选都扫描 videos 表。
func (inventory *fileMigrationInventory) loadActivePaths(ctx context.Context) error {
	if inventory.activePathOwners != nil {
		return nil
	}
	rows, err := loadMigrationActivePaths(ctx)
	if err != nil {
		return err
	}
	inventory.activePaths = rows
	owners := make(map[string][]uint, len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.TrimSpace(row.Path) == "" {
			continue
		}
		key, err := inventory.pathKey(row.Path)
		if err != nil {
			return err
		}
		owners[key] = append(owners[key], row.ID)
	}
	inventory.activePathOwners = owners
	return nil
}

func loadMigrationActivePaths(ctx context.Context) ([]fileMigrationActivePath, error) {
	var rows []fileMigrationActivePath
	err := database.DB.WithContext(ctx).Model(&models.Video{}).Select("id", "path").Order("id").Find(&rows).Error
	return rows, err
}

func (inventory *fileMigrationInventory) checkSourceOwner(source FileMigrationSource, videoID uint) error {
	key, err := inventory.pathKey(source.RealPath)
	if err != nil {
		return err
	}
	for _, id := range inventory.activePathOwners[key] {
		if id != videoID {
			return fmt.Errorf("来源 %s 同时被活跃视频 %d 引用，不能移动视频 %d，请先核对库记录", source.Path, id, videoID)
		}
	}
	return nil
}

func (inventory *fileMigrationInventory) directory(path string) ([]os.DirEntry, error) {
	if entries, ok := inventory.entries[path]; ok {
		return entries, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	inventory.entries[path] = entries
	return entries, nil
}

func subtitleBelongsToStem(name, stem string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".srt" && ext != ".ass" && ext != ".ssa" && ext != ".vtt" {
		return false
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return base == stem || (strings.HasPrefix(base, stem) && consolidationLanguageSuffix.MatchString(strings.TrimPrefix(base, stem)))
}

// indexAttachments 逐目录建立归属索引，避免每个视频都遍历全目录、重复产生
// 上千条其他视频字幕的告警。未入库视频仍参与共享判定。
func (inventory *fileMigrationInventory) indexAttachments(ctx context.Context, dir string) error {
	if _, ok := inventory.attachmentsByDirectory[dir]; ok {
		return nil
	}
	entries, err := inventory.directory(dir)
	if err != nil {
		return err
	}
	stems := make(map[string]int)
	for _, entry := range entries {
		if !entry.IsDir() && inventory.videoExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
			stems[strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))]++
		}
	}
	byStem := make(map[string][]os.DirEntry)
	owners := make(map[string]int)
	var warnings []string
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		base := strings.TrimSuffix(name, filepath.Ext(name))
		if ext == ".nfo" && !strings.EqualFold(name, "movie.nfo") && stems[base] > 0 {
			byStem[base] = append(byStem[base], entry)
			owners[name] = stems[base]
			continue
		}
		if ext == ".srt" || ext == ".ass" || ext == ".ssa" || ext == ".vtt" {
			// 一个字幕只可能归属到其文件名的点分前缀，复杂度取决于名称长度。
			for end := len(base); end > 0; end = strings.LastIndex(base[:end], ".") {
				stem := base[:end]
				if stems[stem] > 0 && subtitleBelongsToStem(name, stem) {
					byStem[stem] = append(byStem[stem], entry)
					owners[name] += stems[stem]
				}
			}
			if owners[name] > 0 {
				continue
			}
		}
		if ext == ".nfo" || ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" || ext == ".srt" || ext == ".ass" || ext == ".ssa" || ext == ".vtt" {
			warnings = append(warnings, "未处理归属不明附件: "+filepath.Join(dir, name))
		}
	}
	inventory.attachmentsByDirectory[dir] = byStem
	inventory.ownersByDirectory[dir] = owners
	inventory.warningsByDirectory[dir] = warnings
	return nil
}

// attachments 只识别明确同名或语言后缀附件，目录级资料只报告一次。
func (inventory *fileMigrationInventory) attachments(ctx context.Context, source FileMigrationSource) ([]FileMigrationFile, []string, error) {
	dir := filepath.Dir(source.RealPath)
	if err := inventory.indexAttachments(ctx, dir); err != nil {
		return nil, nil, err
	}
	stem := strings.TrimSuffix(filepath.Base(source.RealPath), filepath.Ext(source.RealPath))
	var files []FileMigrationFile
	var warnings []string
	if !inventory.warningsReported[dir] {
		warnings = append(warnings, inventory.warningsByDirectory[dir]...)
		inventory.warningsReported[dir] = true
	}
	for _, entry := range inventory.attachmentsByDirectory[dir][stem] {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		name := entry.Name()
		nfo := strings.EqualFold(filepath.Ext(name), ".nfo")
		shared := inventory.ownersByDirectory[dir][name] > 1
		if nfo && shared {
			warnings = append(warnings, "未处理共用 NFO: "+filepath.Join(dir, name))
			continue
		}
		snapshot, err := snapshotMigrationSource(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, fmt.Errorf("核对附件: %w", err)
		}
		kind := "subtitle"
		if nfo {
			kind = "nfo"
		}
		files = append(files, FileMigrationFile{Kind: kind, Source: snapshot, CopyOnly: !nfo && shared})
	}
	return files, warnings, nil
}

func migrationDestinationFiles(source FileMigrationSource, attachments []FileMigrationFile, targetDir, targetName string) []FileMigrationFile {
	files := make([]FileMigrationFile, 0, len(attachments)+1)
	files = append(files, FileMigrationFile{Kind: "video", Source: source, Destination: filepath.Join(targetDir, targetName)})
	sourceStem := strings.TrimSuffix(filepath.Base(source.RealPath), filepath.Ext(source.RealPath))
	targetStem := strings.TrimSuffix(targetName, filepath.Ext(targetName))
	for _, attachment := range attachments {
		attachment.Destination = filepath.Join(targetDir, targetStem+strings.TrimPrefix(filepath.Base(attachment.Source.RealPath), sourceStem))
		files = append(files, attachment)
	}
	return files
}

func (inventory *fileMigrationInventory) filesOccupied(ctx context.Context, files []FileMigrationFile, reserved map[string]bool) (bool, error) {
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		key, err := inventory.pathKey(file.Destination)
		if err != nil {
			return false, err
		}
		if reserved[key] || len(inventory.activePathOwners[key]) > 0 {
			return true, nil
		}
		if _, err := os.Lstat(file.Destination); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}

func addMigrationBytes(total *int64, size int64) error {
	if size < 0 || *total > math.MaxInt64-size {
		return fmt.Errorf("整理文件总大小超出范围")
	}
	*total += size
	return nil
}

// planConsolidationFiles 是现有迁移模块的只读入口，不修改媒体、数据库或扫描目录。
// 命名基于完整视频和附件一起避让；同一批按 ID 排序，保证重预览得到相同新名。
func (s *VideoService) planConsolidationFiles(ctx context.Context, videos []models.Video, destination string) (*fileMigrationPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	plan := &fileMigrationPlan{Items: []FileMigrationItem{}, Warnings: []string{}, Errors: []string{}}
	target, err := snapshotMigrationDirectory(destination)
	if err != nil {
		plan.Errors = append(plan.Errors, err.Error())
		return plan, nil
	}
	plan.Destination = target
	info, err := os.Stat(target.RealPath)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0222 == 0 {
		plan.Errors = append(plan.Errors, "目标目录不可写: "+target.Path)
	}
	free, err := enhancementDiskFree(target.RealPath)
	if err != nil {
		plan.Errors = append(plan.Errors, "无法读取目标可用空间: "+err.Error())
	} else {
		plan.AvailableBytes = free
	}
	inventory, err := newFileMigrationInventory(ctx)
	if err != nil {
		return nil, err
	}
	if err := inventory.loadActivePaths(ctx); err != nil {
		return nil, err
	}
	entries, err := inventory.directory(target.RealPath)
	if err != nil {
		plan.Errors = append(plan.Errors, "无法读取目标目录: "+err.Error())
		return plan, nil
	}
	reserved := make(map[string]bool, len(entries)+len(videos))
	for _, entry := range entries {
		key, err := inventory.pathKey(filepath.Join(target.RealPath, entry.Name()))
		if err != nil {
			return nil, err
		}
		reserved[key] = true
	}
	ordered := append([]models.Video(nil), videos...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	seenIDs := make(map[uint]bool)
	for _, video := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seenIDs[video.ID] {
			continue
		}
		seenIDs[video.ID] = true
		source, err := snapshotMigrationSource(video.Path)
		if err != nil {
			plan.Errors = append(plan.Errors, fmt.Sprintf("视频 %d: %v", video.ID, err))
			continue
		}
		item := FileMigrationItem{VideoID: video.ID, SourcePath: video.Path, Warnings: []string{}}
		attachments, warnings, err := inventory.attachments(ctx, source)
		if err != nil {
			plan.Errors = append(plan.Errors, fmt.Sprintf("视频 %d: %v", video.ID, err))
			continue
		}
		item.Warnings = warnings
		item.Stay = filepath.Dir(source.RealPath) == target.RealPath
		targetName := filepath.Base(source.RealPath)
		files := migrationDestinationFiles(source, attachments, target.RealPath, targetName)
		if !item.Stay {
			var ownershipError error
			for _, file := range files {
				if file.CopyOnly {
					continue
				}
				if err := inventory.checkSourceOwner(file.Source, video.ID); err != nil {
					ownershipError = err
					break
				}
			}
			if ownershipError != nil {
				plan.Errors = append(plan.Errors, ownershipError.Error())
				continue
			}
			for attempt := 0; ; attempt++ {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				occupied, err := inventory.filesOccupied(ctx, files, reserved)
				if err != nil {
					return nil, err
				}
				if !occupied {
					break
				}
				stem := strings.TrimSuffix(filepath.Base(source.RealPath), filepath.Ext(source.RealPath))
				suffix := "（保留版）"
				if attempt > 0 {
					suffix = fmt.Sprintf("（保留版-%d-%d）", video.ID, attempt)
				}
				targetName = stem + suffix + filepath.Ext(source.RealPath)
				files = migrationDestinationFiles(source, attachments, target.RealPath, targetName)
			}
		}
		item.DestinationPath = files[0].Destination
		for i := range files {
			files[i].CrossVolume = !sameMigrationVolume(files[i].Source.Identity, target.Identity)
			if item.Stay {
				continue
			}
			key, err := inventory.pathKey(files[i].Destination)
			if err != nil {
				return nil, err
			}
			reserved[key] = true
			if err := addMigrationBytes(&plan.MoveBytes, files[i].Source.Size); err != nil {
				return nil, err
			}
			if files[i].CrossVolume {
				if err := addMigrationBytes(&plan.CrossVolumeBytes, files[i].Source.Size); err != nil {
					return nil, err
				}
			}
			if files[i].CrossVolume || files[i].CopyOnly {
				if err := addMigrationBytes(&plan.CopyBytes, files[i].Source.Size); err != nil {
					return nil, err
				}
			}
		}
		item.Files = files
		plan.Items = append(plan.Items, item)
		plan.Warnings = append(plan.Warnings, warnings...)
	}
	if uint64(plan.CopyBytes) > plan.AvailableBytes {
		plan.Errors = append(plan.Errors, "目标目录可用空间不足")
	}
	plan.Warnings = uniqueConsolidationMessages(plan.Warnings)
	return plan, nil
}

func uniqueConsolidationMessages(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
