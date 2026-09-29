package services

import (
	"context"
	"errors"
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
	// imageCleanupBandCount 近似重复分桶的段数：64 位 dHash 切成 8 段、每段 8 位，
	// 任一段完全相同即进入逐对比对（镜像视频侧 perceptualBandKeys 的分段方式）。
	imageCleanupBandCount = 8
	// imageCleanupMaxBandNeighbors 每个段桶保留的邻居上限，巨型桶（连拍）截断保护，
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
	// Curation 是这张图上用户整理成果的命中情况（D-PC48），保留建议按它的计分排序。
	Curation ImageCleanupCuration `json:"curation"`
}

// ImageCleanupCuration 是图片的整理项：收藏、评分非空、有人物、有标签。成立的项数即整理分。
type ImageCleanupCuration struct {
	Favorite bool `json:"favorite"`
	Rating   bool `json:"rating"`
	People   bool `json:"people"`
	Tags     bool `json:"tags"`
}

// Score 返回成立的整理项数。
func (c ImageCleanupCuration) Score() int {
	score := 0
	for _, hit := range []bool{c.Favorite, c.Rating, c.People, c.Tags} {
		if hit {
			score++
		}
	}
	return score
}

// ImageCleanupCoverage 是图片清理的覆盖率（D-PC50）：感知哈希已算 Done / Total，
// Total 为范围内活跃且非失效的图片数。
type ImageCleanupCoverage struct {
	PerceptualHash CleanupCoverageCount `json:"perceptual_hash"`
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
	// StaleHashCount 是没有可用感知哈希的图片数：从没回填过的、源文件变过失效的、
	// 以及哈希畸形的。这些图片暂不参与近似重复检测，可通过"补全指纹"一键补齐。
	StaleHashCount int64 `json:"stale_hash_count"`
	// SkippedUnavailable 是本轮 os.Stat 失败或不是普通文件的图片数。
	SkippedUnavailable int `json:"skipped_unavailable"`
	// Coverage 是感知哈希的覆盖率（D-PC50），让"没有重复"与"还没算"分得开。
	Coverage ImageCleanupCoverage `json:"coverage"`
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
	Running   bool `json:"running"`
	Completed bool `json:"completed"`
	// Cancelled 表示上一轮分析被用户取消（D-PC51），此时没有结果、也不算失败。
	Cancelled bool                 `json:"cancelled"`
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
	// registry 登记运行区间（key image_cleanup，D-PC51）；cancel 只在 Running 期间非空。
	registry *BackgroundTaskRegistry
	cancel   context.CancelFunc
}

// ErrImageCleanupAnalysisNotRunning 是取消时没有正在进行的图片清理分析。
var ErrImageCleanupAnalysisNotRunning = errors.New("图片清理分析未在运行")

func NewImageCleanupService() *ImageCleanupService {
	return &ImageCleanupService{}
}

// SetBackgroundTaskRegistry 接入后台任务登记表：图片清理分析登记为 image_cleanup（D-PC51），
// 与视频清理一样只登记运行区间，不装项间检查点。
func (s *ImageCleanupService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

// CancelImageCleanupAnalysis 取消进行中的异步分析（D-PC51）。分析在逐张图片、逐组比较之间
// 检查取消；后台 goroutine 真正停下之后状态才变为 cancelled（期间 Running 仍为 true）。
func (s *ImageCleanupService) CancelImageCleanupAnalysis() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.status.Running || s.cancel == nil {
		return ErrImageCleanupAnalysisNotRunning
	}
	s.cancel()
	return nil
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
	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	registry := s.registry
	status := s.statusSnapshotLocked()
	s.mu.Unlock()
	registry.Begin(BackgroundTaskImageCleanup)

	go func() {
		// End 放在最外层 defer：done 事件发出之后才离开登记表，与视频侧同一口径。
		defer registry.End(BackgroundTaskImageCleanup)
		defer cancel()
		analysis, _, err := s.analyzeImageCleanupCandidates(runCtx)
		cancelled := err != nil && runCtx.Err() != nil

		s.mu.Lock()
		now := time.Now()
		s.status.Running = false
		s.status.UpdatedAt = &now
		s.cancel = nil
		// 运行期间图片库发生了变化：结果仍然保留供审阅，只标记为可能过期。
		staleDuringRun := s.invalidatedDuringRun
		s.invalidatedDuringRun = false
		switch {
		case cancelled:
			s.status.Completed = false
			s.status.Cancelled = true
			s.status.Error = ""
			s.status.Analysis = nil
			s.status.Stale = false
		case err != nil:
			s.status.Completed = false
			s.status.Error = err.Error()
			s.status.Analysis = nil
			s.status.Stale = false
		default:
			s.status.Completed = true
			s.status.Error = ""
			s.status.Analysis = analysis
			s.status.Stale = staleDuringRun
		}
		total := s.status.Progress.Total
		s.mu.Unlock()

		// 与视频侧一致：done 阶段必须在结果写入之后才出现，否则观察者会看到
		// stage=done 却 running=true / analysis=nil。
		switch {
		case cancelled:
			s.emitDoneForRun(runID, total, "已取消图片清理分析。")
		case err != nil:
			s.emitDoneForRun(runID, total, fmt.Sprintf("分析失败：%v", err))
		default:
			s.emitDoneForRun(runID, total, imageCleanupDoneMessage(analysis))
		}
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
	result, states, err := s.analyzeImageCleanupCandidates(context.Background())
	if err != nil {
		return nil, err
	}
	s.emitProgress("done", states, states, "", imageCleanupDoneMessage(result))
	return result, nil
}

func imageCleanupDoneMessage(result *ImageCleanupAnalysis) string {
	message := fmt.Sprintf(
		"分析完成：精确重复组 %d，近似重复组 %d，待补全指纹 %d。",
		len(result.DuplicateGroups), len(result.NearDuplicateGroups), result.StaleHashCount,
	)
	if result.SkippedUnavailable > 0 {
		message += fmt.Sprintf("跳过 %d 张（文件不可访问）。", result.SkippedUnavailable)
	}
	return message
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
// ctx 被取消时在下一张图片 / 下一组比较之前停下，返回 ctx.Err()。
func (s *ImageCleanupService) analyzeImageCleanupCandidates(ctx context.Context) (*ImageCleanupAnalysis, int, error) {
	startedAt := time.Now()
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	var images []models.Image
	if err := applyImageVisibility(database.DB.WithContext(ctx).Model(&models.Image{}), database.DB).Order("id asc").Find(&images).Error; err != nil {
		return nil, 0, err
	}

	// 黑名单目录不参与清理审阅。上面的 applyImageVisibility 已经在 SQL 里滤过一道，
	// 这里再按路径滤一次：SQL 那道是 LIKE 前缀匹配，路径写法有出入时会漏。
	if excluded, err := imageScanExcludedPaths(database.DB); err == nil && len(excluded) > 0 {
		filtered := images[:0]
		for _, img := range images {
			if isScanPathExcluded(img.Path, excluded) {
				continue
			}
			filtered = append(filtered, img)
		}
		images = filtered
	}

	log.Printf("[ImageCleanup] analysis started total_images=%d", len(images))
	s.emitProgress("load", 0, len(images), "", fmt.Sprintf("已读取 %d 条图片记录，正在整理候选…", len(images)))

	states := make([]imageCleanupFileState, 0, len(images))
	sizeBuckets := make(map[int64][]int)
	skippedUnavailable := 0
	// unavailableHashed 是本轮读不到、但库里有格式完好指纹的图片：无从核对，覆盖率里照样算已算过，
	// 否则拔掉一块盘，界面就会把早已补全的指纹报成「尚未计算」。
	var unavailableHashed int64
	for idx, img := range images {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		info, err := os.Stat(img.Path)
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("[ImageCleanup] skip missing image id=%d path=%s", img.ID, img.Path)
			} else {
				log.Printf("[ImageCleanup] skip unreadable image id=%d path=%s err=%v", img.ID, img.Path, err)
			}
			skippedUnavailable++
			if imagePerceptualHashWellFormed(img.PerceptualHash) {
				unavailableHashed++
			}
			continue
		}
		if !info.Mode().IsRegular() {
			log.Printf("[ImageCleanup] skip non-regular image id=%d path=%s", img.ID, img.Path)
			skippedUnavailable++
			if imagePerceptualHashWellFormed(img.PerceptualHash) {
				unavailableHashed++
			}
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
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
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

	result := &ImageCleanupAnalysis{SkippedUnavailable: skippedUnavailable}
	for _, bucket := range duplicateBuckets {
		if len(bucket) < 2 {
			continue
		}
		// 整理分在成组之后批量取齐，再由 rankImageCleanupCandidates 统一重排。
		sort.Slice(bucket, func(i, j int) bool {
			return isPreferredCleanupImage(bucket[i].image, bucket[j].image, nil)
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
	// 忽略记录带双方指纹，任一侧文件变了就不再算数（D-PC31）。
	current := make(map[uint]string, len(states))
	for _, state := range states {
		current[state.image.ID] = cleanupFileFingerprint(state.size, state.modTimeNS)
	}
	dismissed, err := loadActiveImageNearDuplicateDismissals(current)
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

	nearGroups, staleHashCount := s.buildNearDuplicateGroups(ctx, states, excludedPairs)
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	result.NearDuplicateGroups = nearGroups
	result.StaleHashCount = staleHashCount
	result.Coverage = ImageCleanupCoverage{PerceptualHash: CleanupCoverageCount{
		Done:  int64(len(states)) - staleHashCount + unavailableHashed,
		Total: int64(len(images)),
	}}

	// 保留建议按整理成果重排（D-PC48）。整理项读不到时整轮失败：保留建议就是"建议删哪一份"。
	if err := rankImageCleanupCandidates(ctx, result); err != nil {
		return nil, 0, err
	}

	// 标签与 AI 描述只为参与审阅的成员回填，不给全库做 Preload。
	// 它们只是展示信息：回填失败就少显示几行，不该把跑了几分钟的整轮扫描一起丢掉。
	if err := enrichImageCleanupMembers(result); err != nil {
		log.Printf("[ImageCleanup] enrich members failed (结果仍可用) err=%v", err)
	}

	log.Printf("[ImageCleanup] analysis completed elapsed=%s duplicate_groups=%d near_duplicate_groups=%d stale_hash_count=%d hash_candidates=%d skipped_unavailable=%d",
		time.Since(startedAt).Round(time.Millisecond),
		len(result.DuplicateGroups), len(result.NearDuplicateGroups), result.StaleHashCount, len(hashCandidates), result.SkippedUnavailable,
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
	}

	forEachImageCleanupMember(result, func(member *ImageCleanupMember) {
		member.Tags = tagsByID[member.ID]
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

// imageCleanupBandKeys 把 16 位 hex 的 dHash 切成 8 段、每段 8 位，任一段相同即
// 成为候选对。与视频侧的 perceptualBandKeys 同一套分段，只是图片没有帧维度。
//
// 早先这里只用哈希的前 4 个 hex 当唯一一段，等于要求两张图的高 16 位完全一致才肯
// 比。实测库内距离 2~8 的图片对里只有 60% 能进入比对，另外四成连比都没比；理论上
// 一对距离恰好为 8 的图只有约 8.5% 的命中率。改成 8 段后同一批数据的覆盖是 100%。
// 顺带一提，前 4 个 hex 对应的是 8×8 网格最下面两行——裁剪或改比例时最不稳定的
// 那块，作为唯一的一段尤其不合适。
func imageCleanupBandKeys(hash string) []string {
	if len(hash) != imageCleanupBandCount*2 {
		return nil
	}
	keys := make([]string, 0, imageCleanupBandCount)
	for band := 0; band < imageCleanupBandCount; band++ {
		keys = append(keys, fmt.Sprintf("%d:%s", band, hash[band*2:band*2+2]))
	}
	return keys
}

// buildNearDuplicateGroups 库内 dHash 近似重复检测：无可用指纹的计数、
// 8 段分桶 + 邻居/候选上限、汉明距离 ≤ 阈值成边、连通分量成组。
// ctx 被取消时提前返回（结果不完整），调用方随后检查 ctx.Err()。
func (s *ImageCleanupService) buildNearDuplicateGroups(ctx context.Context, states []imageCleanupFileState, excluded map[[2]uint]struct{}) ([]ImageCleanupDuplicateGroup, int64) {
	var staleCount int64
	valid := make([]imageCleanupHashEntry, 0, len(states))
	// 四种情况都算"没有可用指纹"（D-CD04）：未回填、源文件变过、哈希畸形。早先只
	// 数中间那一种，于是一个从没回填过指纹的图库会显示"指纹过期 0"，界面既不提示也
	// 不给补全入口，近似重复永远是空的，用户看不出为什么。口径与视频侧的
	// stale_frame_hash_count 一致。
	for _, state := range states {
		raw := state.image.PerceptualHash
		if raw == "" {
			staleCount++
			continue
		}
		if state.image.HashSourceSize != state.size || state.image.HashSourceModTimeNS != state.modTimeNS {
			staleCount++
			continue
		}
		if len(raw) != 16 {
			log.Printf("[ImageCleanup] skip malformed perceptual hash id=%d hash=%q", state.image.ID, raw)
			staleCount++
			continue
		}
		hash, err := strconv.ParseUint(raw, 16, 64)
		if err != nil {
			log.Printf("[ImageCleanup] skip malformed perceptual hash id=%d hash=%q err=%v", state.image.ID, raw, err)
			staleCount++
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
		if ctx.Err() != nil {
			return nil, staleCount
		}
		candidates := make(map[int]struct{})
		for _, key := range imageCleanupBandKeys(entry.state.image.PerceptualHash) {
			if len(candidates) < imageCleanupMaxCandidates {
				for _, other := range bands[key] {
					candidates[other] = struct{}{}
					if len(candidates) >= imageCleanupMaxCandidates {
						break
					}
				}
			}
			bucket := append(bands[key], index)
			if len(bucket) > imageCleanupMaxBandNeighbors {
				bucket = bucket[len(bucket)-imageCleanupMaxBandNeighbors:]
			}
			bands[key] = bucket
		}

		for other := range candidates {
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
		if ctx.Err() != nil {
			return nil, staleCount
		}
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
			return isPreferredCleanupImage(entries[i].image(), entries[j].image(), nil)
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

// loadActiveImageNearDuplicateDismissals 返回仍然有效的近似重复图片忽略（低 ID 在前，D-PC31）：
// 两侧记录的指纹为空（历史行）或与 current 一致才算数；current 里没有的图片无从核对，照旧算数。
func loadActiveImageNearDuplicateDismissals(current map[uint]string) (map[[2]uint]struct{}, error) {
	var dismissals []models.ImageNearDuplicateDismissal
	if err := database.DB.Find(&dismissals).Error; err != nil {
		return nil, err
	}
	pairs := make(map[[2]uint]struct{}, len(dismissals))
	for _, dismissal := range dismissals {
		if !cleanupDismissalSideApplies(dismissal.FingerprintA, current, dismissal.ImageLowID) ||
			!cleanupDismissalSideApplies(dismissal.FingerprintB, current, dismissal.ImageHighID) {
			continue
		}
		pairs[imageCleanupPairKey(dismissal.ImageLowID, dismissal.ImageHighID)] = struct{}{}
	}
	return pairs, nil
}

// DismissImageNearDuplicateGroup 把一组图片的全部两两配对持久化为忽略（低/高 ID 排序，
// 幂等），后续清理分析不再把它们报为近似重复。每条忽略记下双方此刻的 size:mtimeNS，
// 任一侧文件变了这条忽略随之失效（D-PC31）；重复忽略会刷新指纹。
func DismissImageNearDuplicateGroup(imageIDs []uint) error {
	ids := uniqueUintIDs(imageIDs)
	pairs := make([][2]uint, 0, len(ids)*(len(ids)-1)/2)
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			pairs = append(pairs, imageCleanupPairKey(ids[i], ids[j]))
		}
	}
	if len(pairs) == 0 {
		return fmt.Errorf("忽略近似重复组至少需要两张不同的图片")
	}
	return dismissImageNearDuplicatePairs(ids, pairs)
}

// DismissImageNearDuplicateMember 把一张图移出近似重复组（D-PC31）：只否决它与组内其他成员的配对。
func DismissImageNearDuplicateMember(groupImageIDs []uint, memberID uint) error {
	ids := uniqueUintIDs(groupImageIDs)
	if memberID == 0 || !containsUintID(ids, memberID) {
		return fmt.Errorf("要移出的图片不在这一组里")
	}
	pairs := make([][2]uint, 0, len(ids)-1)
	for _, other := range ids {
		if other != memberID {
			pairs = append(pairs, imageCleanupPairKey(memberID, other))
		}
	}
	if len(pairs) == 0 {
		return fmt.Errorf("移出成员时组内至少还要有另一张图片")
	}
	return dismissImageNearDuplicatePairs(ids, pairs)
}

func dismissImageNearDuplicatePairs(imageIDs []uint, pairs [][2]uint) error {
	fingerprints, err := loadImageFileFingerprints(imageIDs)
	if err != nil {
		return err
	}
	dismissals := make([]models.ImageNearDuplicateDismissal, 0, len(pairs))
	for _, pair := range pairs {
		dismissals = append(dismissals, models.ImageNearDuplicateDismissal{
			ImageLowID: pair[0], ImageHighID: pair[1],
			FingerprintA: fingerprints[pair[0]], FingerprintB: fingerprints[pair[1]],
		})
	}
	// 同一对重复忽略要刷新指纹；pairs 来自去重后的 ID，批内没有重复键（PG 21000）。
	return database.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "image_low_id"}, {Name: "image_high_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"fingerprint_a", "fingerprint_b"}),
	}).Create(&dismissals).Error
}

// loadImageFileFingerprints 读出每张活跃图片此刻的 size:mtimeNS；图片不存在或文件读不到时报错：
// 空指纹表示"永不失效"，只留给历史行。
func loadImageFileFingerprints(imageIDs []uint) (map[uint]string, error) {
	var images []models.Image
	if err := database.DB.Select("id", "path").Where("id IN ?", imageIDs).Find(&images).Error; err != nil {
		return nil, err
	}
	if len(images) != len(imageIDs) {
		return nil, fmt.Errorf("部分图片不存在或已删除，无法记录忽略")
	}
	fingerprints := make(map[uint]string, len(images))
	for _, image := range images {
		fingerprint, err := statCleanupFileFingerprint(image.Path)
		if err != nil {
			return nil, fmt.Errorf("图片 %d 的文件当前无法访问，无法记录忽略", image.ID)
		}
		fingerprints[image.ID] = fingerprint
	}
	return fingerprints, nil
}

func imageCleanupPairKey(a, b uint) [2]uint {
	if a > b {
		a, b = b, a
	}
	return [2]uint{a, b}
}

// isPreferredCleanupImage Original 选择规则（D-PC48）：(整理分 DESC, 像素 DESC, 体积 DESC, ID ASC)。
// curation 为 nil 时整理分都记 0。
func isPreferredCleanupImage(a, b models.Image, curation map[uint]ImageCleanupCuration) bool {
	if aScore, bScore := curation[a.ID].Score(), curation[b.ID].Score(); aScore != bScore {
		return aScore > bScore
	}
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

// imagePerceptualHashWellFormed 报告指纹是否是 16 位十六进制（与近似重复的解析口径一致）。
func imagePerceptualHashWellFormed(raw string) bool {
	if len(raw) != 16 {
		return false
	}
	_, err := strconv.ParseUint(raw, 16, 64)
	return err == nil
}

// rankImageCleanupCandidates 批量取齐整理项并按 isPreferredCleanupImage 重排每组成员；
// 近似重复组的 MaxHammingDistance 随之按新的保留项重算。
func rankImageCleanupCandidates(ctx context.Context, result *ImageCleanupAnalysis) error {
	ids := make([]uint, 0)
	forEachImageCleanupMember(result, func(member *ImageCleanupMember) { ids = append(ids, member.ID) })
	curation, err := loadImageCleanupCuration(ctx, result, uniqueUintIDs(ids))
	if err != nil {
		return fmt.Errorf("读取图片整理信息失败: %w", err)
	}
	forEachImageCleanupMember(result, func(member *ImageCleanupMember) { member.Curation = curation[member.ID] })

	rank := func(group *ImageCleanupDuplicateGroup, near bool) {
		members := append([]ImageCleanupMember{group.Original}, group.Candidates...)
		sort.SliceStable(members, func(i, j int) bool {
			return isPreferredCleanupImage(members[i].Image, members[j].Image, curation)
		})
		group.Original = members[0]
		group.Candidates = append([]ImageCleanupMember(nil), members[1:]...)
		if near {
			group.MaxHammingDistance = imageKeeperMaxHammingDistance(members)
		}
	}
	for i := range result.DuplicateGroups {
		rank(&result.DuplicateGroups[i], false)
	}
	for i := range result.NearDuplicateGroups {
		rank(&result.NearDuplicateGroups[i], true)
	}
	sort.Slice(result.DuplicateGroups, func(i, j int) bool {
		return result.DuplicateGroups[i].Original.ID < result.DuplicateGroups[j].Original.ID
	})
	sort.Slice(result.NearDuplicateGroups, func(i, j int) bool {
		return result.NearDuplicateGroups[i].Original.ID < result.NearDuplicateGroups[j].Original.ID
	})
	return nil
}

// imageKeeperMaxHammingDistance 是与推荐保留项（members[0]）的最大汉明距离，采样上限同成组时。
func imageKeeperMaxHammingDistance(members []ImageCleanupMember) int {
	sampled := members
	if len(sampled) > imageCleanupMaxDistanceSamples {
		sampled = sampled[:imageCleanupMaxDistanceSamples]
	}
	if len(sampled) < 2 {
		return 0
	}
	keeper, err := strconv.ParseUint(sampled[0].PerceptualHash, 16, 64)
	if err != nil {
		return 0
	}
	maxDistance := 0
	for _, member := range sampled[1:] {
		hash, err := strconv.ParseUint(member.PerceptualHash, 16, 64)
		if err != nil {
			continue
		}
		if distance := bits.OnesCount64(keeper ^ hash); distance > maxDistance {
			maxDistance = distance
		}
	}
	return maxDistance
}

// loadImageCleanupCuration 分批取人物与标签；收藏与评分直接取自本轮分析读到的图片行。
func loadImageCleanupCuration(ctx context.Context, result *ImageCleanupAnalysis, ids []uint) (map[uint]ImageCleanupCuration, error) {
	curation := make(map[uint]ImageCleanupCuration, len(ids))
	if len(ids) == 0 {
		return curation, nil
	}
	db := database.DB.WithContext(ctx)
	people := make(map[uint]struct{})
	tagged := make(map[uint]struct{})
	for _, chunk := range chunkUintIDs(ids, imageCleanupEnrichChunkSize) {
		var withPeople, withTags []uint
		if err := db.Model(&models.ImagePerson{}).Where("image_id IN ?", chunk).Distinct().Pluck("image_id", &withPeople).Error; err != nil {
			return nil, err
		}
		if err := db.Table("image_tags").
			Joins("JOIN tags ON tags.id = image_tags.tag_id").
			Where("image_tags.image_id IN ? AND COALESCE(tags.automatic_kind, '') = '' AND tags.deleted_at IS NULL", chunk).
			Distinct().Pluck("image_tags.image_id", &withTags).Error; err != nil {
			return nil, err
		}
		for _, id := range withPeople {
			people[id] = struct{}{}
		}
		for _, id := range withTags {
			tagged[id] = struct{}{}
		}
	}
	forEachImageCleanupMember(result, func(member *ImageCleanupMember) {
		_, hasPeople := people[member.ID]
		_, hasTags := tagged[member.ID]
		curation[member.ID] = ImageCleanupCuration{
			Favorite: member.IsFavorite,
			Rating:   member.PersonalRating != nil,
			People:   hasPeople,
			Tags:     hasTags,
		}
	})
	return curation, nil
}
