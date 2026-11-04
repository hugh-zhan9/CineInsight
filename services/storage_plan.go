package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"
	"video-master/database"
	"video-master/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// StorageGoalVolume 是当前可见媒体实际所在的在线文件系统，不以目录名充当卷身份。
type StorageGoalVolume struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	AvailableBytes uint64 `json:"available_bytes"`
	MediaBytes     int64  `json:"media_bytes"`
	Count          int    `json:"count"`
}
type StorageGoalVolumes struct {
	Epoch     string              `json:"epoch"`
	Items     []StorageGoalVolume `json:"items"`
	SampledAt time.Time           `json:"sampled_at" ts_type:"string"`
}
type StorageReviewCandidate struct {
	ID       uint   `json:"id"`
	Kind     string `json:"kind"`
	KeeperID uint   `json:"keeper_id"`
}

// StorageReviewSnapshot 由原清理审阅状态投影，只能在当前分析证据里减少候选。
type StorageReviewSnapshot struct {
	AnalysisKey string                   `json:"analysis_key"`
	LockedIDs   []uint                   `json:"locked_ids"`
	Candidates  []StorageReviewCandidate `json:"candidates"`
}
type StorageGoalRequest struct {
	Epoch       string                 `json:"epoch"`
	VolumeID    string                 `json:"volume_id"`
	TargetBytes int64                  `json:"target_bytes"`
	VideoReview *StorageReviewSnapshot `json:"video_review"`
	ImageReview *StorageReviewSnapshot `json:"image_review"`
}
type StorageGoalItem struct {
	Kind           string   `json:"kind"`
	ID             uint     `json:"id"`
	Title          string   `json:"title"`
	Bytes          int64    `json:"bytes"`
	Reason         string   `json:"reason"`
	ReasonKind     string   `json:"reason_kind"`
	KeeperID       uint     `json:"keeper_id"`
	KeeperTitle    string   `json:"keeper_title"`
	Suggested      bool     `json:"suggested"`
	IsFavorite     bool     `json:"is_favorite"`
	PersonalRating *float64 `json:"personal_rating"`
	physicalID     string
}

// StorageGoalPlan 只是审阅建议，不持有或签发删除授权。
type StorageGoalPlan struct {
	Epoch          string            `json:"epoch"`
	VolumeID       string            `json:"volume_id"`
	TargetBytes    int64             `json:"target_bytes"`
	AvailableBytes uint64            `json:"available_bytes"`
	PotentialBytes int64             `json:"potential_bytes"`
	ShortfallBytes int64             `json:"shortfall_bytes"`
	Count          int               `json:"count"`
	Items          []StorageGoalItem `json:"items"`
	Notices        []string          `json:"notices"`
	Truncated      bool              `json:"truncated"`
	GeneratedAt    time.Time         `json:"generated_at" ts_type:"string"`
}

type storageGoalFile struct {
	kind       string
	id         uint
	name, path string
	favorite   bool
	rating     *float64
	old        bool
}
type storageGoalEvidence struct {
	keepers      map[uint][]map[uint]bool
	fingerprints map[uint]string
	members      map[uint]bool
}

func newStorageGoalEvidence() storageGoalEvidence {
	return storageGoalEvidence{keepers: map[uint][]map[uint]bool{}, fingerprints: map[uint]string{}, members: map[uint]bool{}}
}
func (e storageGoalEvidence) group(ids []uint) {
	members := map[uint]bool{}
	for _, id := range ids {
		members[id] = true
	}
	for id := range members {
		e.members[id] = true
		e.keepers[id] = append(e.keepers[id], members)
	}
}
func (e storageGoalEvidence) canKeep(id, keeper uint) bool {
	if id == keeper {
		return false
	}
	for _, group := range e.keepers[id] {
		if group[keeper] {
			return true
		}
	}
	return false
}

func (s *StorageReportService) storageEpoch() string {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	if s.planDatabase != database.DB || s.planEpoch == "" {
		s.planDatabase = database.DB
		s.planEpoch = uuid.NewString()
	}
	return s.planEpoch
}
func (s *StorageReportService) goalStat(path string) (os.FileInfo, error) {
	if s.fileStat != nil {
		return s.fileStat(path)
	}
	return os.Stat(path)
}
func (s *StorageReportService) goalVolume(path string) (string, error) {
	if s.volumeID != nil {
		return s.volumeID(path)
	}
	return backupVolumeID(path)
}
func (s *StorageReportService) goalFree(path string) (uint64, error) {
	if s.freeSpace != nil {
		return s.freeSpace(path)
	}
	return enhancementDiskFree(path)
}

// eachStorageGoalFile 用ID游标逐页读取，不能把旧体检的500条显示上限当成全量候选。
func (s *StorageReportService) eachStorageGoalFile(ctx context.Context, db *gorm.DB, visit func(storageGoalFile) error) error {
	criteria := StorageUnwatchedCriteria{MinBytes: storageDefaultUnwatchedMinBytes, Months: storageDefaultUnwatchedMonths, Cutoff: s.now().AddDate(0, -storageDefaultUnwatchedMonths, 0)}
	for after := uint(0); ; {
		if err := ctx.Err(); err != nil {
			return err
		}
		query, err := visibleVideoQuery(db.WithContext(ctx).Model(&models.Video{}))
		if err != nil {
			return err
		}
		var rows []storageVideoRow
		if err = query.Select("videos.id,videos.name,videos.display_title,videos.path,videos.directory,videos.size,videos.duration,videos.width,videos.height,videos.last_played_at,videos.watch_progress_updated_at,videos.watched_at,videos.created_at,videos.is_favorite,videos.personal_rating").Where("videos.id > ?", after).Order("videos.id").Limit(512).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		last := map[uint]*time.Time{}
		var oldIDs []uint
		for _, row := range rows {
			last[row.ID] = storageLatest(row.LastPlayedAt, row.WatchProgressUpdatedAt, row.WatchedAt)
			if storageIsUnwatched(row, last[row.ID], criteria) {
				oldIDs = append(oldIDs, row.ID)
			}
		}
		if err = storageMergePlayEvents(oldIDs, last); err != nil {
			return err
		}
		for _, row := range rows {
			name := row.DisplayTitle
			if name == "" {
				name = row.Name
			}
			if err = visit(storageGoalFile{kind: "video", id: row.ID, name: name, path: row.Path, favorite: row.IsFavorite, rating: row.PersonalRating, old: storageIsUnwatched(row, last[row.ID], criteria)}); err != nil {
				return err
			}
		}
		after = rows[len(rows)-1].ID
	}
	for after := uint(0); ; {
		if err := ctx.Err(); err != nil {
			return err
		}
		var rows []models.Image
		query := applyImageVisibility(db.WithContext(ctx).Model(&models.Image{}), db)
		if err := query.Select("images.id,images.name,images.path,images.is_favorite,images.personal_rating").Where("images.id > ?", after).Order("images.id").Limit(512).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if err := visit(storageGoalFile{kind: "image", id: row.ID, name: row.Name, path: row.Path, favorite: row.IsFavorite, rating: row.PersonalRating}); err != nil {
				return err
			}
		}
		after = rows[len(rows)-1].ID
	}
	return nil
}

func (s *StorageReportService) GetStorageGoalVolumes(ctx context.Context) (*StorageGoalVolumes, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, errors.New("storage_plan_unsupported: 当前系统暂不支持按物理卷规划空间")
	}
	result := &StorageGoalVolumes{Items: []StorageGoalVolume{}, SampledAt: s.now()}
	byID := map[string]*StorageGoalVolume{}
	seen := map[string]bool{}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		result.Epoch = s.storageEpoch()
		return s.eachStorageGoalFile(ctx, db, func(file storageGoalFile) error {
			if scanVolumeAvailable(file.path) != nil {
				return nil
			}
			info, err := s.goalStat(file.path)
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
			id, err := s.goalVolume(file.path)
			if err != nil {
				return nil
			}
			volume := byID[id]
			if volume == nil {
				resolved, resolveErr := filepath.EvalSymlinks(file.path)
				if resolveErr != nil {
					return nil
				}
				dir := filepath.Dir(resolved)
				free, freeErr := s.goalFree(dir)
				if freeErr != nil {
					return nil
				}
				mount := dir
				for parent := filepath.Dir(mount); parent != mount; parent = filepath.Dir(mount) {
					parentID, parentErr := s.goalVolume(parent)
					if parentErr != nil || parentID != id {
						break
					}
					mount = parent
				}
				label := mount
				if mount == string(filepath.Separator) {
					label = "本机磁盘"
				}
				volume = &StorageGoalVolume{ID: id, Label: label, AvailableBytes: free}
				byID[id] = volume
			}
			identity := stableFileIdentity(info)
			if identity == "" {
				identity = filepath.Clean(file.path)
			}
			if !seen[identity] {
				seen[identity] = true
				volume.Count++
				volume.MediaBytes += info.Size()
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	for _, volume := range byID {
		result.Items = append(result.Items, *volume)
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Label < result.Items[j].Label })
	return result, nil
}

func videoGoalEvidence(status *CleanupStatus) (map[string]storageGoalEvidence, string, bool) {
	result := map[string]storageGoalEvidence{}
	for _, kind := range []string{"exact", "near", "same-source", "clip", "low-resolution", "low-duration"} {
		result[kind] = newStorageGoalEvidence()
	}
	if status == nil || status.Analysis == nil {
		return result, "", false
	}
	a := status.Analysis
	add := func(kind string, ids []uint) {
		e := result[kind]
		e.group(ids)
		for _, id := range ids {
			e.fingerprints[id] = a.sourceFingerprints[id]
		}
	}
	for _, pair := range []struct {
		kind   string
		groups []CleanupDuplicateGroup
	}{{"exact", a.DuplicateGroups}, {"near", a.NearDuplicateGroups}} {
		for _, group := range pair.groups {
			ids := []uint{group.Original.ID}
			for _, member := range group.Candidates {
				ids = append(ids, member.ID)
			}
			add(pair.kind, ids)
		}
	}
	for _, g := range a.SameSourceGroups {
		add("same-source", []uint{g.Preferred.ID, g.Alternative.ID})
	}
	for _, g := range a.ClipGroups {
		add("clip", []uint{g.Full.ID, g.Clip.ID})
	}
	for _, v := range a.LowResolution {
		add("low-resolution", []uint{v.ID})
	}
	for _, v := range a.LowDuration {
		add("low-duration", []uint{v.ID})
	}
	key := ""
	if status.StartedAt != nil {
		key = status.StartedAt.Format(time.RFC3339Nano)
	}
	return result, key, status.Completed && !status.Running && !status.Stale
}
func imageGoalEvidence(status *ImageCleanupStatus) (map[string]storageGoalEvidence, string, bool) {
	result := map[string]storageGoalEvidence{"exact": newStorageGoalEvidence(), "near": newStorageGoalEvidence()}
	if status == nil || status.Analysis == nil {
		return result, "", false
	}
	for _, pair := range []struct {
		kind   string
		groups []ImageCleanupDuplicateGroup
	}{{"exact", status.Analysis.DuplicateGroups}, {"near", status.Analysis.NearDuplicateGroups}} {
		for _, group := range pair.groups {
			members := append([]ImageCleanupMember{group.Original}, group.Candidates...)
			ids := []uint{}
			for _, member := range members {
				ids = append(ids, member.ID)
				result[pair.kind].fingerprints[member.ID] = cleanupFileFingerprint(member.FileSize, member.ModTimeNS)
			}
			result[pair.kind].group(ids)
		}
	}
	key := ""
	if status.StartedAt != nil {
		key = status.StartedAt.Format(time.RFC3339Nano)
	}
	return result, key, status.Completed && !status.Running && !status.Stale
}

// GetStorageGoalPlan 使用原清理证据与原审阅保护快照，仅生成建议，不执行删除。
func (s *StorageReportService) GetStorageGoalPlan(ctx context.Context, request StorageGoalRequest, videoStatus *CleanupStatus, imageStatus *ImageCleanupStatus) (*StorageGoalPlan, error) {
	if request.TargetBytes < 1 || request.TargetBytes > 1<<60 || request.VolumeID == "" {
		return nil, errors.New("storage_plan_invalid: 请选择磁盘并输入正数目标空间")
	}
	result := &StorageGoalPlan{Epoch: request.Epoch, VolumeID: request.VolumeID, TargetBytes: request.TargetBytes, Items: []StorageGoalItem{}, Notices: []string{"候选大小为逻辑估计；共享块、快照和废纸篓会影响实际释放量。处理后请刷新磁盘可用空间。"}, GeneratedAt: s.now()}
	videoEvidence, videoKey, videoReady := videoGoalEvidence(videoStatus)
	imageEvidence, imageKey, imageReady := imageGoalEvidence(imageStatus)
	evidence := map[string]map[string]storageGoalEvidence{"video": videoEvidence, "image": imageEvidence}
	known := map[string]map[uint]bool{"video": {}, "image": {}}
	locks := map[string]map[uint]bool{"video": {}, "image": {}}
	if videoStatus != nil && videoStatus.Analysis != nil {
		for _, group := range videoStatus.Analysis.SameSourceGroups {
			locks["video"][group.Preferred.ID] = true
		}
		for _, group := range videoStatus.Analysis.ClipGroups {
			locks["video"][group.Full.ID] = true
		}
	}
	candidates := map[string]map[uint]StorageReviewCandidate{"video": {}, "image": {}}
	for kind, groups := range evidence {
		for _, group := range groups {
			for id := range group.members {
				known[kind][id] = true
			}
		}
	}
	for _, input := range []struct {
		kind, key string
		ready     bool
		snapshot  *StorageReviewSnapshot
	}{{"video", videoKey, videoReady, request.VideoReview}, {"image", imageKey, imageReady, request.ImageReview}} {
		if input.snapshot != nil {
			if len(input.snapshot.LockedIDs) > 200000 || len(input.snapshot.Candidates) > 200000 {
				return nil, errors.New("storage_plan_invalid: 审阅快照过大")
			}
			for _, id := range input.snapshot.LockedIDs {
				locks[input.kind][id] = true
			}
		}
		if !input.ready || input.snapshot == nil || input.key == "" || input.snapshot.AnalysisKey != input.key {
			result.Notices = append(result.Notices, map[string]string{"video": "视频", "image": "图片"}[input.kind]+"重复候选尚无有效审阅快照；先打开对应清理审阅后重新生成。当前没有把未知保护状态的组成员计入目标。")
			continue
		}
		for _, candidate := range input.snapshot.Candidates {
			group, ok := evidence[input.kind][candidate.Kind]
			if !ok || !group.members[candidate.ID] {
				continue
			}
			if candidate.KeeperID != 0 && !group.canKeep(candidate.ID, candidate.KeeperID) {
				continue
			}
			if candidate.KeeperID == 0 && candidate.Kind != "low-duration" && candidate.Kind != "low-resolution" {
				continue
			}
			if candidate.KeeperID != 0 {
				locks[input.kind][candidate.KeeperID] = true
			}
			previous, exists := candidates[input.kind][candidate.ID]
			if !exists || goalReasonRank(candidate.Kind) < goalReasonRank(previous.Kind) {
				candidates[input.kind][candidate.ID] = candidate
			}
		}
	}
	type currentFile struct {
		file   storageGoalFile
		info   os.FileInfo
		volume string
	}
	current := map[string]map[uint]currentFile{"video": {}, "image": {}}
	var oldFiles []currentFile
	volumeFound := false
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		if request.Epoch != s.storageEpoch() {
			return errors.New("storage_plan_expired: 片库已变化，请刷新磁盘列表")
		}
		return s.eachStorageGoalFile(ctx, db, func(file storageGoalFile) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if scanVolumeAvailable(file.path) != nil {
				return nil
			}
			info, err := s.goalStat(file.path)
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
			volume, err := s.goalVolume(file.path)
			if err != nil {
				return nil
			}
			if known[file.kind][file.id] || locks[file.kind][file.id] {
				current[file.kind][file.id] = currentFile{file, info, volume}
			}
			if volume != request.VolumeID {
				return nil
			}
			if !volumeFound {
				resolved, resolveErr := filepath.EvalSymlinks(file.path)
				if resolveErr != nil {
					return nil
				}
				free, freeErr := s.goalFree(filepath.Dir(resolved))
				if freeErr != nil {
					return freeErr
				}
				result.AvailableBytes = free
				volumeFound = true
			}
			if file.kind == "video" && file.old && info.Size() >= storageDefaultUnwatchedMinBytes && !known[file.kind][file.id] && !locks[file.kind][file.id] {
				oldFiles = append(oldFiles, currentFile{file, info, volume})
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if !volumeFound {
		return nil, errors.New("storage_plan_volume_unavailable: 所选磁盘未在线或已无可见媒体")
	}
	protectedPhysical := map[string]bool{}
	physicalID := func(file currentFile) string {
		identity := stableFileIdentity(file.info)
		if identity == "" {
			identity = file.file.path
		}
		return identity
	}
	for kind, ids := range locks {
		for id := range ids {
			if file, ok := current[kind][id]; ok {
				identity := stableFileIdentity(file.info)
				if identity == "" {
					identity = file.file.path
				}
				protectedPhysical[identity] = true
			}
		}
	}
	countable := func(file currentFile) bool {
		link, err := os.Lstat(file.file.path)
		if err != nil || link.Mode()&os.ModeSymlink != 0 {
			return false
		}
		links, ok := fileLinkCount(file.info)
		if !ok || links != 1 {
			return false
		}
		identity := stableFileIdentity(file.info)
		if identity == "" {
			identity = file.file.path
		}
		if protectedPhysical[identity] {
			return false
		}
		return true
	}
	for _, file := range oldFiles {
		if countable(file) {
			result.Items = append(result.Items, StorageGoalItem{Kind: "video", ID: file.file.id, Title: file.file.name, Bytes: file.info.Size(), Reason: "至少2 GiB，超过12个月未观看", ReasonKind: "old", IsFavorite: file.file.favorite, PersonalRating: file.file.rating, physicalID: physicalID(file)})
		}
	}
	skippedSource := 0
	for kind, byID := range candidates {
		for id, candidate := range byID {
			if locks[kind][id] {
				continue
			}
			file, ok := current[kind][id]
			if !ok || file.volume != request.VolumeID {
				continue
			}
			group := evidence[kind][candidate.Kind]
			fingerprint := cleanupFileFingerprint(file.info.Size(), file.info.ModTime().UnixNano())
			if group.fingerprints[id] == "" || fingerprint != group.fingerprints[id] {
				skippedSource++
				continue
			}
			if candidate.KeeperID != 0 {
				keeper, exists := current[kind][candidate.KeeperID]
				if !exists || group.fingerprints[candidate.KeeperID] == "" || cleanupFileFingerprint(keeper.info.Size(), keeper.info.ModTime().UnixNano()) != group.fingerprints[candidate.KeeperID] {
					skippedSource++
					continue
				}
			}
			if !countable(file) {
				continue
			}
			keeperTitle := ""
			if candidate.KeeperID != 0 {
				keeperTitle = current[kind][candidate.KeeperID].file.name
			}
			result.Items = append(result.Items, StorageGoalItem{Kind: kind, ID: id, Title: file.file.name, Bytes: file.info.Size(), Reason: goalReasonLabel(candidate.Kind), ReasonKind: candidate.Kind, KeeperID: candidate.KeeperID, KeeperTitle: keeperTitle, IsFavorite: file.file.favorite, PersonalRating: file.file.rating, physicalID: physicalID(file)})
		}
	}
	sort.Slice(result.Items, func(i, j int) bool {
		a, b := result.Items[i], result.Items[j]
		if goalReasonRank(a.ReasonKind) != goalReasonRank(b.ReasonKind) {
			return goalReasonRank(a.ReasonKind) < goalReasonRank(b.ReasonKind)
		}
		if a.Bytes != b.Bytes {
			return a.Bytes > b.Bytes
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ID < b.ID
	})
	seenPhysical := map[string]bool{}
	unique := result.Items[:0]
	for _, item := range result.Items {
		if !seenPhysical[item.physicalID] {
			seenPhysical[item.physicalID] = true
			unique = append(unique, item)
		}
	}
	result.Items = unique
	if skippedSource > 0 {
		result.Notices = append(result.Notices, "部分源文件或分析依据已经变化，未计入方案；请在原清理页重新分析。")
	}
	var suggested int64
	for index := range result.Items {
		row := &result.Items[index]
		result.PotentialBytes += row.Bytes
		if row.ReasonKind == "exact" && suggested < request.TargetBytes {
			row.Suggested = true
			suggested += row.Bytes
		}
	}
	result.Count = len(result.Items)
	result.ShortfallBytes = request.TargetBytes - result.PotentialBytes
	if result.ShortfallBytes < 0 {
		result.ShortfallBytes = 0
	}
	if len(result.Items) > 5000 {
		result.Items = result.Items[:5000]
		result.Truncated = true
		result.Notices = append(result.Notices, "当前方案最多列出5000项；其余候选没有代为勾选，请在原清理审阅中继续核对。")
	}
	result.Notices = append(result.Notices, "符号链接、硬链接和被保留项指向的同一物理文件未计入释放量；近似重复、其他版本和长期未看项目需要手动审阅，不会自动勾选。")
	return result, nil
}
func goalReasonRank(kind string) int {
	switch kind {
	case "exact":
		return 0
	case "near":
		return 1
	case "same-source", "clip":
		return 2
	case "old":
		return 4
	default:
		return 3
	}
}
func goalReasonLabel(kind string) string {
	switch kind {
	case "exact":
		return "精确重复，保留项已排除"
	case "near":
		return "近似重复，需要对比确认"
	case "same-source":
		return "同源版本，需要核对画质、音轨和字幕"
	case "clip":
		return "已有完整片的截取片段"
	case "low-duration":
		return "极短片段，需要核对"
	case "low-resolution":
		return "极低分辨率，需要核对"
	}
	return kind
}
