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
	// imageCleanupEnrichChunkSize 回填标签/描述时单条 IN 语句的 id 上限，
	// 远低于 Postgres/SQLite 的绑定参数上限。
	imageCleanupEnrichChunkSize = 500
	// imageCleanupMaxDistanceSamples 计算组内最大汉明距离时的成员采样上限：
	// 连通分量可以很大，两两比对是 O(n²)。
	imageCleanupMaxDistanceSamples = 64
)

func chunkUintIDs(ids []uint, size int) [][]uint {
	if size <= 0 || len(ids) <= size {
		return [][]uint{ids}
	}
	chunks := make([][]uint, 0, (len(ids)+size-1)/size)
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		chunks = append(chunks, ids[start:end])
	}
	return chunks
}

// ImageCleanupMember 审阅界面里的一张候选图片：内嵌 models.Image（JSON 平铺，
// 前端字段名不变），再补上审阅要用、但库里字段给不出的实测信息。
type ImageCleanupMember struct {
	models.Image
	// FileSize/ModTimeNS 来自分析时的 os.Stat，比库里的 Size 更能反映磁盘现状。
	FileSize  int64 `json:"file_size"`
	ModTimeNS int64 `json:"mod_time_ns"`
	// Description 是已生成完成的 AI 描述，没有则为空串。
	Description string `json:"description"`
}

// ImageCleanupDuplicateGroup 一组重复/近似重复图片：Original 为建议保留项
// （像素数优先、次按体积），Candidates 为其余成员。
type ImageCleanupDuplicateGroup struct {
	Original   ImageCleanupMember   `json:"original"`
	Candidates []ImageCleanupMember `json:"candidates"`
	Reason     string               `json:"reason"`
	// MaxHammingDistance 是组内两两感知哈希的最大距离，越小越像；精确重复组为 0。
	MaxHammingDistance int `json:"max_hamming_distance"`
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
	Running   bool                 `json:"running"`
	Completed bool                 `json:"completed"`
	Error     string               `json:"error"`
	Progress  ImageCleanupProgress `json:"progress"`
	// Stale 表示缓存结果算出后图片库又发生了变化（删除、恢复、忽略近似组等）。
	// 结果仍然保留供用户继续审阅，只是提示可能过期，由用户决定何时重新分析。
	Stale     bool                  `json:"stale"`
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
	// runID 每次启动分析自增；后台 goroutine 用它判断自己是否仍是当前这轮，
	// 避免旧的收尾事件把 done 阶段盖到新一轮的状态上。
	runID uint64
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
	s.runID++
	runID := s.runID
	status := s.statusSnapshotLocked()
	s.mu.Unlock()

	go func() {
		analysis, _, err := s.analyzeImageCleanupCandidates()

		s.mu.Lock()
		now := time.Now()
		s.status.Running = false
		s.status.UpdatedAt = &now
		// 运行期间图片库发生了变化：结果仍然保留供审阅，只标记为可能过期。
		staleDuringRun := s.invalidatedDuringRun
		s.invalidatedDuringRun = false
		if err != nil {
			s.status.Completed = false
			s.status.Error = err.Error()
			s.status.Analysis = nil
			s.status.Stale = false
		} else {
			s.status.Completed = true
			s.status.Error = ""
			s.status.Analysis = analysis
			s.status.Stale = staleDuringRun
		}
		total := s.status.Progress.Total
		s.mu.Unlock()

		// 与视频侧一致：done 阶段必须在结果写入之后才出现，否则观察者会看到
		// stage=done 却 running=true / analysis=nil。
		if err != nil {
			s.emitDoneForRun(runID, total, fmt.Sprintf("分析失败：%v", err))
			return
		}
		s.emitDoneForRun(runID, total, imageCleanupDoneMessage(analysis))
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

// InvalidateAnalysis 标记缓存结果可能已过期（删除/恢复图片后调用）；结果本身保留，
// 用户重开清理审阅仍能看到并继续处理，由用户自己决定何时重新分析。
func (s *ImageCleanupService) InvalidateAnalysis() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Running {
		s.invalidatedDuringRun = true
		return
	}
	if s.status.Analysis == nil {
		return
	}
	s.status.Stale = true
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

// AnalyzeImageCleanupCandidates 同步执行一次完整分析并发出终止事件（测试与同步调用方使用）；
// 异步任务走 analyzeImageCleanupCandidates，由 StartImageCleanupAnalysis 在写完状态后补发 done。
func (s *ImageCleanupService) AnalyzeImageCleanupCandidates() (*ImageCleanupAnalysis, error) {
	result, states, err := s.analyzeImageCleanupCandidates()
	if err != nil {
		return nil, err
	}
	s.emitProgress("done", states, states, "", imageCleanupDoneMessage(result))
	return result, nil
}

func imageCleanupDoneMessage(result *ImageCleanupAnalysis) string {
	return fmt.Sprintf(
		"分析完成：精确重复组 %d，近似重复组 %d，指纹过期 %d。",
		len(result.DuplicateGroups), len(result.NearDuplicateGroups), result.StaleHashCount,
	)
}

// emitDoneForRun 只在自己仍是当前这轮分析时写入 done 进度并回调事件。
func (s *ImageCleanupService) emitDoneForRun(runID uint64, total int, message string) {
	progress := ImageCleanupProgress{Stage: "done", Message: message, Current: total, Total: total}
	s.mu.Lock()
	if s.runID != runID {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	s.status.Progress = progress
	s.status.UpdatedAt = &now
	emitter := s.emitter
	s.mu.Unlock()

	if emitter != nil {
		emitter(progress)
	}
}

// analyzeImageCleanupCandidates 返回分析结果和参与比对的图片数；不发终止事件。
func (s *ImageCleanupService) analyzeImageCleanupCandidates() (*ImageCleanupAnalysis, int, error) {
	startedAt := time.Now()
	var images []models.Image
	if err := database.DB.Order("id asc").Find(&images).Error; err != nil {
		return nil, 0, err
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

	duplicateBuckets := make(map[string][]imageCleanupFileState)
	for idx, stateIdx := range hashCandidates {
		state := states[stateIdx]
		hash, err := getPartialHash(state.image.Path)
		if err == nil && hash != "" {
			bucketKey := buildDuplicateBucketKey(state.size, hash)
			duplicateBuckets[bucketKey] = append(duplicateBuckets[bucketKey], state)
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
			return isPreferredCleanupImage(bucket[i].image, bucket[j].image)
		})
		members := make([]ImageCleanupMember, 0, len(bucket))
		for _, state := range bucket {
			members = append(members, newImageCleanupMember(state))
		}
		result.DuplicateGroups = append(result.DuplicateGroups, ImageCleanupDuplicateGroup{
			Original:   members[0],
			Candidates: append([]ImageCleanupMember(nil), members[1:]...),
			Reason:     "文件大小和采样哈希一致",
		})
	}
	sort.Slice(result.DuplicateGroups, func(i, j int) bool {
		return result.DuplicateGroups[i].Original.ID < result.DuplicateGroups[j].Original.ID
	})

	exactPairs := make(map[[2]uint]struct{})
	for _, group := range result.DuplicateGroups {
		members := append([]ImageCleanupMember{group.Original}, group.Candidates...)
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				exactPairs[imageCleanupPairKey(members[i].ID, members[j].ID)] = struct{}{}
			}
		}
	}
	dismissed, err := loadImageNearDuplicateDismissals()
	if err != nil {
		return nil, 0, err
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

	// 标签与 AI 描述只为参与审阅的成员回填，不给全库做 Preload。
	// 它们只是展示信息：回填失败就少显示几行，不该把跑了几分钟的整轮扫描一起丢掉。
	if err := enrichImageCleanupMembers(result); err != nil {
		log.Printf("[ImageCleanup] enrich members failed (结果仍可用) err=%v", err)
	}

	log.Printf("[ImageCleanup] analysis completed elapsed=%s duplicate_groups=%d near_duplicate_groups=%d stale_hash_count=%d hash_candidates=%d",
		time.Since(startedAt).Round(time.Millisecond),
		len(result.DuplicateGroups), len(result.NearDuplicateGroups), result.StaleHashCount, len(hashCandidates),
	)
	// done 事件由调用方在写完状态后发出。
	return result, len(states), nil
}

// imageCleanupHashEntry 参与近似重复比对的一张图片及其解析后的 64 位 dHash。
type imageCleanupHashEntry struct {
	state imageCleanupFileState
	hash  uint64
}

func (e imageCleanupHashEntry) image() models.Image { return e.state.image }

// newImageCleanupMember 把分析期实测到的文件信息附到图片上；AI 描述稍后统一回填。
func newImageCleanupMember(state imageCleanupFileState) ImageCleanupMember {
	return ImageCleanupMember{Image: state.image, FileSize: state.size, ModTimeNS: state.modTimeNS}
}

// enrichImageCleanupMembers 只为出现在结果里的图片补标签和已完成的 AI 描述。
// 全库 Preload 在大图库上代价过高，而审阅界面又要靠这些信息判断该留哪一份。
func enrichImageCleanupMembers(result *ImageCleanupAnalysis) error {
	ids := make([]uint, 0)
	seen := make(map[uint]struct{})
	forEachImageCleanupMember(result, func(member *ImageCleanupMember) {
		if _, ok := seen[member.ID]; ok {
			return
		}
		seen[member.ID] = struct{}{}
		ids = append(ids, member.ID)
	})
	if len(ids) == 0 {
		return nil
	}

	tagsByID := make(map[uint][]models.Tag, len(ids))
	descriptionByID := make(map[uint]string, len(ids))
	// 分批查：一次 IN 的绑定参数受驱动限制（Postgres 65535 / SQLite 32766），
	// 重复成员多的大库会直接把整条语句打爆。
	for _, chunk := range chunkUintIDs(ids, imageCleanupEnrichChunkSize) {
		var tagged []models.Image
		if err := database.DB.Preload("Tags").Select("id").Where("id IN ?", chunk).Find(&tagged).Error; err != nil {
			return err
		}
		for _, image := range tagged {
			tagsByID[image.ID] = image.Tags
		}

		var descriptions []models.ImageAIDescription
		if err := database.DB.
			Select("image_id", "status", "description").
			Where("image_id IN ? AND status = ?", chunk, imageAIDescriptionStatusCompleted).
			Find(&descriptions).Error; err != nil {
			return err
		}
		for _, item := range descriptions {
			descriptionByID[item.ImageID] = item.Description
		}
	}

	forEachImageCleanupMember(result, func(member *ImageCleanupMember) {
		member.Tags = tagsByID[member.ID]
		member.Description = descriptionByID[member.ID]
	})
	return nil
}

func forEachImageCleanupMember(result *ImageCleanupAnalysis, visit func(*ImageCleanupMember)) {
	groups := [][]ImageCleanupDuplicateGroup{result.DuplicateGroups, result.NearDuplicateGroups}
	for _, set := range groups {
		for i := range set {
			visit(&set[i].Original)
			for j := range set[i].Candidates {
				visit(&set[i].Candidates[j])
			}
		}
	}
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
		valid = append(valid, imageCleanupHashEntry{state: state, hash: hash})
	}
	if len(valid) < 2 {
		return []ImageCleanupDuplicateGroup{}, staleCount
	}

	s.emitProgress("near", 0, len(valid), "", fmt.Sprintf("正在比对 %d 张图片的感知哈希…", len(valid)))

	bands := make(map[string][]int)
	adjacency := make(map[int]map[int]struct{})
	for index, entry := range valid {
		key := entry.state.image.PerceptualHash[:imageCleanupBandPrefixLen]
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
			pair := imageCleanupPairKey(left.image().ID, entry.image().ID)
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
			s.emitProgress("near", index+1, len(valid), entry.state.image.Path, "正在比对图片感知哈希…")
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
		entries := make([]imageCleanupHashEntry, 0, len(component))
		for _, memberIdx := range component {
			entries = append(entries, valid[memberIdx])
		}
		sort.Slice(entries, func(i, j int) bool {
			return isPreferredCleanupImage(entries[i].image(), entries[j].image())
		})
		// 与推荐保留项的最大汉明距离：界面用它给出"有多像"的量化说法。
		// 不用组内两两最大值——连通分量是链式的，链两端可以毫不相似，
		// 报出来会把一个刚判定为近似重复的组标成相似度 0%。
		maxDistance := 0
		sampled := entries
		if len(sampled) > imageCleanupMaxDistanceSamples {
			sampled = sampled[:imageCleanupMaxDistanceSamples]
		}
		for _, entry := range sampled[1:] {
			if distance := bits.OnesCount64(sampled[0].hash ^ entry.hash); distance > maxDistance {
				maxDistance = distance
			}
		}
		members := make([]ImageCleanupMember, 0, len(entries))
		for _, entry := range entries {
			members = append(members, newImageCleanupMember(entry.state))
		}
		groups = append(groups, ImageCleanupDuplicateGroup{
			Original:           members[0],
			Candidates:         append([]ImageCleanupMember(nil), members[1:]...),
			Reason:             "感知哈希相近，可能是同图不同尺寸或压缩",
			MaxHammingDistance: maxDistance,
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
