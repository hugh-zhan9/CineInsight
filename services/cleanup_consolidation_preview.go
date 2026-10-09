package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"video-master/database"
	"video-master/models"
)

// SetConsolidationVideoService 由 App 注入既有文件迁移所有者，不新建迁移服务。
func (s *CleanupService) SetConsolidationVideoService(video *VideoService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consolidationVideo = video
}

type consolidationAnalysisGroup struct {
	kind    string
	members []uint
	keeper  uint
}

func consolidationGroupKey(kind string, ids []uint) (string, error) {
	ordered := append([]uint(nil), ids...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	var key strings.Builder
	key.WriteString(kind)
	for i, id := range ordered {
		if id == 0 || (i > 0 && id == ordered[i-1]) {
			return "", fmt.Errorf("组成员必须是互不重复的视频 ID")
		}
		key.WriteByte(':')
		key.WriteString(strconv.FormatUint(uint64(id), 10))
	}
	if len(ordered) == 0 {
		return "", fmt.Errorf("组成员不能为空")
	}
	return key.String(), nil
}

func consolidationAnalysisGroups(analysis *CleanupAnalysis) (map[string]consolidationAnalysisGroup, map[uint]models.Video, error) {
	groups := make(map[string]consolidationAnalysisGroup)
	videos := make(map[uint]models.Video)
	add := func(kind string, keeper uint, members []models.Video) error {
		ids := make([]uint, len(members))
		for i, member := range members {
			ids[i] = member.ID
			videos[member.ID] = member
		}
		key, err := consolidationGroupKey(kind, ids)
		if err != nil {
			return err
		}
		if _, exists := groups[key]; exists {
			return fmt.Errorf("分析包含重复组，请重新分析")
		}
		groups[key] = consolidationAnalysisGroup{kind: kind, members: ids, keeper: keeper}
		return nil
	}
	for _, group := range analysis.DuplicateGroups {
		if err := add("exact", group.Original.ID, append([]models.Video{group.Original}, group.Candidates...)); err != nil {
			return nil, nil, err
		}
	}
	for _, group := range analysis.NearDuplicateGroups {
		if err := add("near", group.Original.ID, append([]models.Video{group.Original}, group.Candidates...)); err != nil {
			return nil, nil, err
		}
	}
	for _, group := range analysis.SameSourceGroups {
		if err := add("same-source", group.Preferred.ID, []models.Video{group.Preferred, group.Alternative}); err != nil {
			return nil, nil, err
		}
	}
	for _, group := range analysis.ClipGroups {
		if err := add("clip", group.Full.ID, []models.Video{group.Full, group.Clip}); err != nil {
			return nil, nil, err
		}
	}
	for _, video := range analysis.LowDuration {
		if err := add("low-duration", 0, []models.Video{video}); err != nil {
			return nil, nil, err
		}
	}
	for _, video := range analysis.LowResolution {
		if err := add("low-resolution", 0, []models.Video{video}); err != nil {
			return nil, nil, err
		}
	}
	return groups, videos, nil
}

func consolidationContains(ids []uint, wanted uint) bool {
	for _, id := range ids {
		if id == wanted {
			return true
		}
	}
	return false
}

func validateConsolidationRequest(request CleanupConsolidationRequest, groups map[string]consolidationAnalysisGroup) error {
	if len(request.Groups) == 0 {
		return fmt.Errorf("请至少选择一个相关重复组")
	}
	if len(request.Protections) != len(groups) {
		return fmt.Errorf("保留保护快照不完整，请重新打开清理审阅")
	}
	protections := make(map[string]CleanupConsolidationProtection, len(groups))
	for _, protection := range request.Protections {
		key, err := consolidationGroupKey(protection.Kind, protection.MemberIDs)
		if err != nil {
			return err
		}
		group, exists := groups[key]
		if !exists {
			return fmt.Errorf("保护组已变化，请重新审阅")
		}
		if _, exists := protections[key]; exists {
			return fmt.Errorf("保护快照包含重复组")
		}
		if group.keeper == 0 {
			if protection.KeeperID != 0 || protection.KeeperPinned {
				return fmt.Errorf("单条候选不能指定保留项")
			}
		} else {
			if !consolidationContains(group.members, protection.KeeperID) {
				return fmt.Errorf("保留项不在分析组内")
			}
			if protection.KeeperID != group.keeper && (!protection.KeeperPinned || (group.kind != "exact" && group.kind != "near")) {
				return fmt.Errorf("保留版本已变化或不允许切换，请重新审阅")
			}
		}
		protections[key] = protection
	}
	seen := make(map[string]bool, len(request.Groups))
	for _, selected := range request.Groups {
		if selected.Kind != "exact" && selected.Kind != "near" && selected.Kind != "same-source" && selected.Kind != "clip" {
			return fmt.Errorf("该类别不能纳入集中整理")
		}
		key, err := consolidationGroupKey(selected.Kind, selected.MemberIDs)
		if err != nil {
			return err
		}
		protection, exists := protections[key]
		if !exists || seen[key] {
			return fmt.Errorf("所选组已变化或重复，请重新审阅")
		}
		seen[key] = true
		if protection.Skipped {
			return fmt.Errorf("本组不删的组不能纳入集中整理")
		}
		if selected.KeeperID != protection.KeeperID || selected.KeeperPinned != protection.KeeperPinned {
			return fmt.Errorf("移动保留项与保护快照不一致")
		}
		selectedSet := make(map[uint]bool)
		for _, id := range selected.SelectedIDs {
			if !consolidationContains(selected.MemberIDs, id) || id == selected.KeeperID || selectedSet[id] {
				return fmt.Errorf("清理意向包含重复、非成员或保留项")
			}
			selectedSet[id] = true
		}
	}
	return nil
}

// loadConsolidationSources 分批查询完整保护范围，防止大量 IN 参数超过 SQLite
// 限制。任何分析后的删改都要求重新分析，不拿新文件冒充旧的重复判断。
func loadConsolidationSources(ctx context.Context, expected map[uint]models.Video, fingerprints map[uint]string) ([]cleanupConsolidationVideoSource, error) {
	ids := make([]uint, 0, len(expected))
	for id := range expected {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	sources := make([]cleanupConsolidationVideoSource, 0, len(ids))
	for start := 0; start < len(ids); start += 400 {
		end := start + 400
		if end > len(ids) {
			end = len(ids)
		}
		var rows []models.Video
		if err := database.DB.WithContext(ctx).Where("id IN ?", ids[start:end]).Find(&rows).Error; err != nil {
			return nil, err
		}
		if len(rows) != end-start {
			return nil, fmt.Errorf("分析中的视频已删除，请重新分析")
		}
		for _, video := range rows {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if video.Path != expected[video.ID].Path || video.IsStale {
				return nil, fmt.Errorf("视频 %d 的路径或可用状态已变化，请重新分析", video.ID)
			}
			file, err := snapshotMigrationSource(video.Path)
			if err != nil {
				return nil, fmt.Errorf("视频 %d 需要重新分析: %w", video.ID, err)
			}
			if fingerprints[video.ID] == "" || cleanupFileFingerprint(file.Size, file.ModTimeNS) != fingerprints[video.ID] {
				return nil, fmt.Errorf("视频 %d 的源文件已变化，请重新分析", video.ID)
			}
			sources = append(sources, cleanupConsolidationVideoSource{Video: video, File: file})
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Video.ID < sources[j].Video.ID })
	return sources, nil
}

// suggestedConsolidationDestination 先汇总默认保留项的引用数和唯一文件成本，
// 每个目录只调整在那里能原地保留的精确组。移入的替代项都已在目标目录，
// 因此只有默认保留项失去全部引用时才需从移动成本扣除，避免目录×组遍历。
func suggestedConsolidationDestination(ctx context.Context, groups []CleanupConsolidationGroup, sources map[uint]cleanupConsolidationVideoSource) (string, error) {
	type score struct {
		stays    int
		identity string
		releases map[uint]int
	}
	scores := make(map[string]*score)
	references := make(map[uint]int)
	maxStays := 0
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		references[group.KeeperID]++
		keeperDir := filepath.Dir(sources[group.KeeperID].File.RealPath)
		eligible := make(map[string]bool)
		for _, id := range group.MemberIDs {
			source := sources[id].File
			dir := filepath.Dir(source.RealPath)
			if scores[dir] == nil {
				scores[dir] = &score{identity: source.Identity, releases: make(map[uint]int)}
			}
			if dir == keeperDir || (group.Kind == "exact" && !group.KeeperPinned) {
				eligible[dir] = true
			}
		}
		for dir := range eligible {
			candidate := scores[dir]
			candidate.stays++
			if candidate.stays > maxStays {
				maxStays = candidate.stays
			}
			if dir != keeperDir {
				candidate.releases[group.KeeperID]++
			}
		}
	}
	inventory, err := newFileMigrationInventory(ctx)
	if err != nil {
		return "", err
	}
	bytesByKeeper := make(map[uint]int64, len(references))
	bytesByDirectory := make(map[string]int64)
	bytesByDevice := make(map[string]int64)
	var total int64
	for id := range references {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		source := sources[id].File
		attachments, _, err := inventory.attachments(ctx, source)
		if err != nil {
			return "", err
		}
		size := source.Size
		for _, attachment := range attachments {
			if err := addMigrationBytes(&size, attachment.Source.Size); err != nil {
				return "", err
			}
		}
		if err := addMigrationBytes(&total, size); err != nil {
			return "", err
		}
		bytesByKeeper[id] = size
		bytesByDirectory[filepath.Dir(source.RealPath)] += size
		device, _, _ := strings.Cut(source.Identity, ":")
		bytesByDevice[device] += size
	}
	var best string
	var bestTotal, bestCross int64
	for dir, candidate := range scores {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if candidate.stays != maxStays {
			continue
		}
		device, _, _ := strings.Cut(candidate.identity, ":")
		moving := total - bytesByDirectory[dir]
		cross := total - bytesByDevice[device]
		for id, released := range candidate.releases {
			if released != references[id] {
				continue
			}
			moving -= bytesByKeeper[id]
			if !sameMigrationVolume(sources[id].File.Identity, candidate.identity) {
				cross -= bytesByKeeper[id]
			}
		}
		if best == "" || cross < bestCross || (cross == bestCross && (moving < bestTotal || (moving == bestTotal && dir < best))) {
			best, bestTotal, bestCross = dir, moving, cross
		}
	}
	if best == "" {
		return "", fmt.Errorf("没有可用的候选目录")
	}
	return best, nil
}

func consolidationKeeperAtDestination(group CleanupConsolidationGroup, sources map[uint]cleanupConsolidationVideoSource, destination string) uint {
	if group.Kind != "exact" || group.KeeperPinned || filepath.Dir(sources[group.KeeperID].File.RealPath) == destination {
		return group.KeeperID
	}
	var replacement uint
	for _, id := range group.MemberIDs {
		if filepath.Dir(sources[id].File.RealPath) == destination && (replacement == 0 || id < replacement) {
			replacement = id
		}
	}
	if replacement != 0 {
		return replacement
	}
	return group.KeeperID
}

func resolveConsolidationGroups(request CleanupConsolidationRequest, sources map[uint]cleanupConsolidationVideoSource, destination string) ([]models.Video, error) {
	selectedKeys := make(map[string]int, len(request.Groups))
	for i := range request.Groups {
		group := &request.Groups[i]
		key, _ := consolidationGroupKey(group.Kind, group.MemberIDs)
		selectedKeys[key] = i
		group.KeeperID = consolidationKeeperAtDestination(*group, sources, destination)
	}
	locked := make(map[uint]bool)
	skipped := make(map[uint]bool)
	for i := range request.Protections {
		protection := &request.Protections[i]
		key, _ := consolidationGroupKey(protection.Kind, protection.MemberIDs)
		if index, ok := selectedKeys[key]; ok {
			protection.KeeperID = request.Groups[index].KeeperID
		}
		if protection.KeeperID != 0 {
			locked[protection.KeeperID] = true
		}
		if protection.Skipped {
			for _, id := range protection.MemberIDs {
				locked[id] = true
				skipped[id] = true
			}
		}
	}
	var videos []models.Video
	seen := make(map[uint]bool)
	claimedPaths := make(map[string]uint)
	for i := range request.Groups {
		group := &request.Groups[i]
		if skipped[group.KeeperID] {
			return nil, fmt.Errorf("视频 %d 同时受本组不删保护，不能移动，请调整相关组", group.KeeperID)
		}
		selected := make([]uint, 0, len(group.SelectedIDs))
		for _, id := range group.SelectedIDs {
			if !locked[id] {
				selected = append(selected, id)
			}
		}
		group.SelectedIDs = selected
		if !seen[group.KeeperID] {
			path := sources[group.KeeperID].File.RealPath
			if previous := claimedPaths[path]; previous != 0 && previous != group.KeeperID {
				return nil, fmt.Errorf("视频 %d 与 %d 指向同一来源路径，请先核对库记录", previous, group.KeeperID)
			}
			claimedPaths[path] = group.KeeperID
			seen[group.KeeperID] = true
			videos = append(videos, sources[group.KeeperID].Video)
		}
	}
	return videos, nil
}

// PreviewConsolidation 验证当前分析及完整保护，再由迁移模块生成确切路径。
// 每次请求立即撤销旧令牌；调用失败或被较新请求抢先时不能启动旧预览。
func (s *CleanupService) PreviewConsolidation(ctx context.Context, request CleanupConsolidationRequest) (*CleanupConsolidationPreview, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.consolidationPreviewSequence++
	sequence := s.consolidationPreviewSequence
	s.consolidationPreview = nil
	status := s.statusSnapshotLocked()
	version := s.runID
	videoService := s.consolidationVideo
	s.mu.Unlock()
	if videoService == nil {
		return nil, fmt.Errorf("集中整理迁移服务未初始化")
	}
	if !status.Completed || status.Running || status.Stale || status.Error != "" || status.Analysis == nil {
		return nil, fmt.Errorf("没有有效的清理分析，请先重新分析")
	}
	analysis, err := filterCleanupReviewDecisions(status.Analysis)
	if err != nil {
		return nil, err
	}
	groups, expected, err := consolidationAnalysisGroups(analysis)
	if err != nil {
		return nil, err
	}
	// 所有输入深拷贝；调用方后续修改选择不能改变已确认清单。
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	var ownedRequest CleanupConsolidationRequest
	if err := json.Unmarshal(encoded, &ownedRequest); err != nil {
		return nil, err
	}
	request = ownedRequest
	if err := validateConsolidationRequest(request, groups); err != nil {
		return nil, err
	}
	sources, err := loadConsolidationSources(ctx, expected, analysis.sourceFingerprints)
	if err != nil {
		return nil, err
	}
	byID := make(map[uint]cleanupConsolidationVideoSource, len(sources))
	for _, source := range sources {
		byID[source.Video.ID] = source
	}
	destination := request.Destination
	if strings.TrimSpace(destination) == "" {
		destination, err = suggestedConsolidationDestination(ctx, request.Groups, byID)
		if err != nil {
			return nil, err
		}
	}
	target, err := snapshotMigrationDirectory(destination)
	if err != nil {
		return &CleanupConsolidationPreview{AnalysisVersion: version, Destination: destination, Errors: []string{err.Error()}}, nil
	}
	videos, err := resolveConsolidationGroups(request, byID, target.RealPath)
	if err != nil {
		return nil, err
	}
	files, err := videoService.planConsolidationFiles(ctx, videos, destination)
	if err != nil {
		return nil, err
	}
	preview := CleanupConsolidationPreview{AnalysisVersion: version, Destination: files.Destination.RealPath,
		DestinationInfo: files.Destination, Groups: request.Groups, Items: files.Items, MoveBytes: files.MoveBytes,
		CrossVolumeBytes: files.CrossVolumeBytes, CopyBytes: files.CopyBytes, AvailableBytes: files.AvailableBytes, Warnings: files.Warnings, Errors: files.Errors}
	for _, item := range files.Items {
		if len(item.Files) == 0 {
			continue
		}
		old := byID[item.VideoID].File
		now := item.Files[0].Source
		if old != now {
			return nil, fmt.Errorf("预览期间源文件已变化，请重新预览")
		}
	}
	if files.Destination != target {
		return nil, fmt.Errorf("预览期间目标目录已变化，请重新预览")
	}
	check, err := videoService.CheckMoveTarget(target.RealPath)
	if err != nil {
		return nil, err
	}
	preview.InScanRoots = check.InScanRoots
	if len(preview.Errors) > 0 {
		return &preview, nil
	}
	// 忽略记录可能在文件预检期间变化，提交内存快照前再比较有效组集合。
	fresh, err := filterCleanupReviewDecisions(status.Analysis)
	if err != nil {
		return nil, err
	}
	freshGroups, _, err := consolidationAnalysisGroups(fresh)
	if err != nil {
		return nil, err
	}
	if !sameConsolidationGroupSet(groups, freshGroups) {
		return nil, fmt.Errorf("审阅决定已变化，请重新预览")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	preview.PreviewID = hex.EncodeToString(token[:])
	plan := cleanupConsolidationPlan{SchemaVersion: cleanupConsolidationSchemaVersion, Preview: preview, Protections: request.Protections, Sources: sources, Analysis: analysis}
	// JSON 分离返回值和服务拥有的快照；私有 sourceFingerprints 已显式进入 Sources。
	encoded, err = json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	var stored cleanupConsolidationPlan
	if err := json.Unmarshal(encoded, &stored); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sequence != s.consolidationPreviewSequence || version != s.runID || s.status.Stale || s.status.Running {
		return nil, fmt.Errorf("分析或选择已变化，请重新预览")
	}
	s.consolidationPreview = &stored
	return &preview, nil
}

func sameConsolidationGroupSet(a, b map[string]consolidationAnalysisGroup) bool {
	if len(a) != len(b) {
		return false
	}
	for key, group := range a {
		if other, ok := b[key]; !ok || other.keeper != group.keeper {
			return false
		}
	}
	return true
}
