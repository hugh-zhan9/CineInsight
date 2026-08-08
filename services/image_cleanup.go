package services

import (
	"fmt"
	"log"
	"math/bits"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm/clause"
)

const (
	// imageCleanupHammingThreshold 64 位 dHash 判定近似重复的最大汉明距离（设计 4.8.2 / D-012）。
	imageCleanupHammingThreshold = 8
	// imageCleanupBandPrefixLen 近似重复分桶使用的哈希 hex 前缀长度（16 位 hex 取前 4 字符）。
	imageCleanupBandPrefixLen = 4
	// imageCleanupMaxBandNeighbors 每个前缀桶保留的邻居上限，巨型桶（连拍）截断保护，
	// 镜像 perceptualHashMaxBandNeighbors 的常量思路。
	imageCleanupMaxBandNeighbors = 64
	// imageCleanupMaxCandidates 单张图片参与逐对比对的候选上限，镜像 perceptualHashMaxCandidates。
	imageCleanupMaxCandidates = 256
)

// ImageCleanupDuplicateGroup 一组重复/近似重复图片：Original 为建议保留项
// （像素数优先、次按体积），Candidates 为可删除候选（一律不自动勾选）。
type ImageCleanupDuplicateGroup struct {
	Original   models.Image   `json:"original"`
	Candidates []models.Image `json:"candidates"`
	Reason     string         `json:"reason"`
}

// ImageCleanupAnalysis 图片清理审阅分析产出（设计 4.8.2，无 LowDuration/LowResolution/SameSource）。
type ImageCleanupAnalysis struct {
	DuplicateGroups     []ImageCleanupDuplicateGroup `json:"duplicate_groups"`
	NearDuplicateGroups []ImageCleanupDuplicateGroup `json:"near_duplicate_groups"`
	// StaleHashCount 是源文件已变更、感知哈希失效待重算的图片数；这些图片
	// 暂不参与近似重复检测，浏览图片可自动刷新缩略图与指纹。
	StaleHashCount int64 `json:"stale_hash_count"`
}

// ImageCleanupProgress 分析进度快照，镜像 CleanupProgress。
type ImageCleanupProgress struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Path    string `json:"path"`
}

// ImageCleanupStatus 分析任务状态，镜像 CleanupStatus。
type ImageCleanupStatus struct {
	Running   bool                  `json:"running"`
	Completed bool                  `json:"completed"`
	Error     string                `json:"error"`
	Progress  ImageCleanupProgress  `json:"progress"`
	Analysis  *ImageCleanupAnalysis `json:"analysis,omitempty"`
	StartedAt *time.Time            `json:"started_at,omitempty" ts_type:"string"`
	UpdatedAt *time.Time            `json:"updated_at,omitempty" ts_type:"string"`
}

// ImageCleanupService 图片清理审阅分析服务，异步任务形态镜像 CleanupService。
type ImageCleanupService struct {
	mu                   sync.Mutex
	status               ImageCleanupStatus
	invalidatedDuringRun bool
	emitter              func(ImageCleanupProgress)
}

func NewImageCleanupService() *ImageCleanupService {
	return &ImageCleanupService{}
}

// SetEventEmitter 注入进度事件回调（app 层接 Wails 事件 image-cleanup-progress）。
func (s *ImageCleanupService) SetEventEmitter(emitter func(ImageCleanupProgress)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitter = emitter
}

// StartImageCleanupAnalysis 启动异步分析；已有分析在跑时返回当前状态不重复启动。
func (s *ImageCleanupService) StartImageCleanupAnalysis() (*ImageCleanupStatus, error) {
	s.mu.Lock()
	if s.status.Running {
		status := s.statusSnapshotLocked()
		s.mu.Unlock()
		return &status, nil
	}
	now := time.Now()
	s.status = ImageCleanupStatus{
		Running:   true,
		Completed: false,
		StartedAt: &now,
		UpdatedAt: &now,
		Progress: ImageCleanupProgress{
			Stage:   "load",
			Message: "正在准备图片清理候选分析…",
		},
	}
	s.invalidatedDuringRun = false
	status := s.statusSnapshotLocked()
	s.mu.Unlock()

	go func() {
		analysis, err := s.AnalyzeImageCleanupCandidates()
		s.mu.Lock()
		defer s.mu.Unlock()
		now := time.Now()
		s.status.Running = false
		if s.invalidatedDuringRun {
			s.status.Completed = false
			s.status.Error = ""
			s.status.Analysis = nil
			s.invalidatedDuringRun = false
			return
		}
		s.status.Completed = err == nil
		s.status.UpdatedAt = &now
		if err != nil {
			s.status.Error = err.Error()
			s.status.Analysis = nil
			return
		}
		s.status.Error = ""
		s.status.Analysis = analysis
	}()

	return &status, nil
}

// GetImageCleanupStatus 返回当前分析状态快照。
func (s *ImageCleanupService) GetImageCleanupStatus() *ImageCleanupStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.statusSnapshotLocked()
	return &status
}

// InvalidateAnalysis 丢弃已完成的缓存结果（删除/恢复图片后调用）；运行中的分析结果作废。
func (s *ImageCleanupService) InvalidateAnalysis() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Running {
		s.invalidatedDuringRun = true
		return
	}
	s.status.Completed = false
	s.status.Error = ""
	s.status.Analysis = nil
	s.status.Progress = ImageCleanupProgress{}
	now := time.Now()
	s.status.UpdatedAt = &now
}

func (s *ImageCleanupService) statusSnapshotLocked() ImageCleanupStatus {
	status := s.status
	if status.Analysis != nil {
		analysisCopy := *status.Analysis
		status.Analysis = &analysisCopy
	}
	return status
}

// imageCleanupFileState 分析快照内一张活跃图片的实时文件状态（os.Stat 一次，精确/近似共用）。
type imageCleanupFileState struct {
	image     models.Image
	size      int64
	modTimeNS int64
}

// AnalyzeImageCleanupCandidates 同步执行一次完整分析（Start 的 goroutine 调用，测试可直接调）。
func (s *ImageCleanupService) AnalyzeImageCleanupCandidates() (*ImageCleanupAnalysis, error) {
	startedAt := time.Now()
	var images []models.Image
	if err := database.DB.Order("id asc").Find(&images).Error; err != nil {
		return nil, err
	}

	// 黑名单目录不参与清理审阅：图片黑名单优先，空则回退通用黑名单（与扫描行为一致）。
	var settings models.Settings
	if err := database.DB.Select("scan_exclude_paths", "image_scan_exclude_paths").First(&settings).Error; err == nil {
		excluded := parseScanExcludePaths(settings.ImageScanExcludePaths)
		if len(excluded) == 0 {
			excluded = parseScanExcludePaths(settings.ScanExcludePaths)
		}
		if len(excluded) > 0 {
			filtered := images[:0]
			for _, img := range images {
				if isScanPathExcluded(img.Path, excluded) {
					continue
				}
				filtered = append(filtered, img)
			}
			images = filtered
		}
	}

	log.Printf("[ImageCleanup] analysis started total_images=%d", len(images))
	s.emitProgress("load", 0, len(images), "", fmt.Sprintf("已读取 %d 条图片记录，正在整理候选…", len(images)))

	states := make([]imageCleanupFileState, 0, len(images))
	sizeBuckets := make(map[int64][]int)
	for idx, img := range images {
		info, err := os.Stat(img.Path)
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("[ImageCleanup] skip missing image id=%d path=%s", img.ID, img.Path)
			} else {
				log.Printf("[ImageCleanup] skip unreadable image id=%d path=%s err=%v", img.ID, img.Path, err)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			log.Printf("[ImageCleanup] skip non-regular image id=%d path=%s", img.ID, img.Path)
			continue
		}
		sizeBuckets[info.Size()] = append(sizeBuckets[info.Size()], len(states))
		states = append(states, imageCleanupFileState{image: img, size: info.Size(), modTimeNS: info.ModTime().UnixNano()})

		if shouldEmitCleanupProgress(idx+1, len(images), 400) {
			s.emitProgress("group", idx+1, len(images), img.Path, "正在按文件大小聚合候选…")
		}
	}

	hashCandidates := make([]int, 0)
	for _, bucket := range sizeBuckets {
		if len(bucket) < 2 {
			continue
		}
		hashCandidates = append(hashCandidates, bucket...)
	}
	sort.Ints(hashCandidates)

	s.emitProgress("hash", 0, len(hashCandidates), "", fmt.Sprintf("发现 %d 个疑似重复文件，正在读取采样哈希…", len(hashCandidates)))

	duplicateBuckets := make(map[string][]models.Image)
	for idx, stateIdx := range hashCandidates {
		state := states[stateIdx]
		hash, err := getPartialHash(state.image.Path)
		if err == nil && hash != "" {
			bucketKey := buildDuplicateBucketKey(state.size, hash)
			duplicateBuckets[bucketKey] = append(duplicateBuckets[bucketKey], state.image)
		} else if err != nil {
			log.Printf("[ImageCleanup] partial hash failed image id=%d path=%s err=%v", state.image.ID, state.image.Path, err)
		}
		if shouldEmitCleanupProgress(idx+1, len(hashCandidates), 50) {
			s.emitProgress("hash", idx+1, len(hashCandidates), state.image.Path, "正在读取疑似重复文件的采样哈希…")
		}
	}

	result := &ImageCleanupAnalysis{}
	for _, bucket := range duplicateBuckets {
		if len(bucket) < 2 {
			continue
		}
		sort.Slice(bucket, func(i, j int) bool {
			return isPreferredCleanupImage(bucket[i], bucket[j])
		})
		result.DuplicateGroups = append(result.DuplicateGroups, ImageCleanupDuplicateGroup{
			Original:   bucket[0],
			Candidates: append([]models.Image(nil), bucket[1:]...),
			Reason:     "文件大小和采样哈希一致",
		})
	}
	sort.Slice(result.DuplicateGroups, func(i, j int) bool {
		return result.DuplicateGroups[i].Original.ID < result.DuplicateGroups[j].Original.ID
	})

	exactPairs := make(map[[2]uint]struct{})
	for _, group := range result.DuplicateGroups {
		members := append([]models.Image{group.Original}, group.Candidates...)
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				exactPairs[imageCleanupPairKey(members[i].ID, members[j].ID)] = struct{}{}
			}
		}
	}
	dismissed, err := loadImageNearDuplicateDismissals()
	if err != nil {
		return nil, err
	}
	excludedPairs := make(map[[2]uint]struct{}, len(exactPairs)+len(dismissed))
	for pair := range exactPairs {
		excludedPairs[pair] = struct{}{}
	}
	for pair := range dismissed {
		excludedPairs[pair] = struct{}{}
	}

	nearGroups, staleHashCount := s.buildNearDuplicateGroups(states, excludedPairs)
	result.NearDuplicateGroups = nearGroups
	result.StaleHashCount = staleHashCount

	log.Printf("[ImageCleanup] analysis completed elapsed=%s duplicate_groups=%d near_duplicate_groups=%d stale_hash_count=%d hash_candidates=%d",
		time.Since(startedAt).Round(time.Millisecond),
		len(result.DuplicateGroups), len(result.NearDuplicateGroups), result.StaleHashCount, len(hashCandidates),
	)
	s.emitProgress("done", len(states), len(states), "", fmt.Sprintf(
		"分析完成：精确重复组 %d，近似重复组 %d，指纹过期 %d。",
		len(result.DuplicateGroups), len(result.NearDuplicateGroups), result.StaleHashCount,
	))

	return result, nil
}

// imageCleanupHashEntry 参与近似重复比对的一张图片及其解析后的 64 位 dHash。
type imageCleanupHashEntry struct {
	image models.Image
	hash  uint64
}

// buildNearDuplicateGroups 库内 dHash 近似重复检测：stale 指纹跳过计数、
// 哈希前缀分桶 + 邻居/候选上限、汉明距离 ≤ 阈值成边、连通分量成组。
func (s *ImageCleanupService) buildNearDuplicateGroups(states []imageCleanupFileState, excluded map[[2]uint]struct{}) ([]ImageCleanupDuplicateGroup, int64) {
	var staleCount int64
	valid := make([]imageCleanupHashEntry, 0, len(states))
	for _, state := range states {
		raw := state.image.PerceptualHash
		if raw == "" {
			continue // 哈希未回填不参与近似分析，也不计 stale（无哈希≠stale）
		}
		if state.image.HashSourceSize != state.size || state.image.HashSourceModTimeNS != state.modTimeNS {
			staleCount++
			continue
		}
		if len(raw) != 16 {
			log.Printf("[ImageCleanup] skip malformed perceptual hash id=%d hash=%q", state.image.ID, raw)
			continue
		}
		hash, err := strconv.ParseUint(raw, 16, 64)
		if err != nil {
			log.Printf("[ImageCleanup] skip malformed perceptual hash id=%d hash=%q err=%v", state.image.ID, raw, err)
			continue
		}
		valid = append(valid, imageCleanupHashEntry{image: state.image, hash: hash})
	}
	if len(valid) < 2 {
		return []ImageCleanupDuplicateGroup{}, staleCount
	}

	s.emitProgress("near", 0, len(valid), "", fmt.Sprintf("正在比对 %d 张图片的感知哈希…", len(valid)))

	bands := make(map[string][]int)
	adjacency := make(map[int]map[int]struct{})
	for index, entry := range valid {
		key := entry.image.PerceptualHash[:imageCleanupBandPrefixLen]
		candidates := make([]int, 0, len(bands[key]))
		for _, other := range bands[key] {
			if len(candidates) >= imageCleanupMaxCandidates {
				break
			}
			candidates = append(candidates, other)
		}
		bucket := append(bands[key], index)
		if len(bucket) > imageCleanupMaxBandNeighbors {
			bucket = bucket[len(bucket)-imageCleanupMaxBandNeighbors:]
		}
		bands[key] = bucket

		for _, other := range candidates {
			left := valid[other]
			pair := imageCleanupPairKey(left.image.ID, entry.image.ID)
			if _, skip := excluded[pair]; skip {
				continue
			}
			if bits.OnesCount64(left.hash^entry.hash) > imageCleanupHammingThreshold {
				continue
			}
			if adjacency[other] == nil {
				adjacency[other] = make(map[int]struct{})
			}
			if adjacency[index] == nil {
				adjacency[index] = make(map[int]struct{})
			}
			adjacency[other][index] = struct{}{}
			adjacency[index][other] = struct{}{}
		}
		if shouldEmitCleanupProgress(index+1, len(valid), 400) {
			s.emitProgress("near", index+1, len(valid), entry.image.Path, "正在比对图片感知哈希…")
		}
	}

	// 连通分量成组（设计 4.8.2）：按索引升序 DFS，保证结果确定。
	visited := make(map[int]struct{})
	groups := make([]ImageCleanupDuplicateGroup, 0)
	for index := range valid {
		if _, seen := visited[index]; seen {
			continue
		}
		if len(adjacency[index]) == 0 {
			continue
		}
		component := make([]int, 0)
		stack := []int{index}
		visited[index] = struct{}{}
		for len(stack) > 0 {
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			component = append(component, current)
			neighbors := make([]int, 0, len(adjacency[current]))
			for neighbor := range adjacency[current] {
				neighbors = append(neighbors, neighbor)
			}
			sort.Ints(neighbors)
			for _, neighbor := range neighbors {
				if _, seen := visited[neighbor]; seen {
					continue
				}
				visited[neighbor] = struct{}{}
				stack = append(stack, neighbor)
			}
		}
		if len(component) < 2 {
			continue
		}
		members := make([]models.Image, 0, len(component))
		for _, memberIdx := range component {
			members = append(members, valid[memberIdx].image)
		}
		sort.Slice(members, func(i, j int) bool {
			return isPreferredCleanupImage(members[i], members[j])
		})
		groups = append(groups, ImageCleanupDuplicateGroup{
			Original:   members[0],
			Candidates: append([]models.Image(nil), members[1:]...),
			Reason:     "感知哈希相近，可能是同图不同尺寸或压缩（不会默认选中）",
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Original.ID < groups[j].Original.ID
	})
	return groups, staleCount
}

// loadImageNearDuplicateDismissals 返回用户已忽略的近似重复图片对（低 ID 在前）。
func loadImageNearDuplicateDismissals() (map[[2]uint]struct{}, error) {
	var dismissals []models.ImageNearDuplicateDismissal
	if err := database.DB.Find(&dismissals).Error; err != nil {
		return nil, err
	}
	pairs := make(map[[2]uint]struct{}, len(dismissals))
	for _, dismissal := range dismissals {
		pairs[imageCleanupPairKey(dismissal.ImageLowID, dismissal.ImageHighID)] = struct{}{}
	}
	return pairs, nil
}

// DismissImageNearDuplicateGroup 把一组图片的全部两两配对持久化为忽略（低/高 ID 排序，
// 幂等），后续清理分析不再把它们报为近似重复。
func DismissImageNearDuplicateGroup(imageIDs []uint) error {
	dismissals := make([]models.ImageNearDuplicateDismissal, 0, len(imageIDs)*(len(imageIDs)-1)/2)
	for i := 0; i < len(imageIDs); i++ {
		for j := i + 1; j < len(imageIDs); j++ {
			if imageIDs[i] == imageIDs[j] {
				continue
			}
			pair := imageCleanupPairKey(imageIDs[i], imageIDs[j])
			dismissals = append(dismissals, models.ImageNearDuplicateDismissal{ImageLowID: pair[0], ImageHighID: pair[1]})
		}
	}
	if len(dismissals) == 0 {
		return fmt.Errorf("忽略近似重复组至少需要两张不同的图片")
	}
	return database.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&dismissals).Error
}

func imageCleanupPairKey(a, b uint) [2]uint {
	if a > b {
		a, b = b, a
	}
	return [2]uint{a, b}
}

// isPreferredCleanupImage Original 选择规则：像素数优先、次按体积，最后低 ID 保证确定性。
func isPreferredCleanupImage(a, b models.Image) bool {
	aPixels := a.Width * a.Height
	bPixels := b.Width * b.Height
	if aPixels != bPixels {
		return aPixels > bPixels
	}
	if a.Size != b.Size {
		return a.Size > b.Size
	}
	return a.ID < b.ID
}

func (s *ImageCleanupService) emitProgress(stage string, current int, total int, currentPath string, message string) {
	progress := ImageCleanupProgress{
		Stage:   stage,
		Message: message,
		Current: current,
		Total:   total,
		Path:    currentPath,
	}
	s.mu.Lock()
	now := time.Now()
	s.status.Progress = progress
	s.status.UpdatedAt = &now
	emitter := s.emitter
	s.mu.Unlock()

	if emitter != nil {
		emitter(progress)
	}
}
