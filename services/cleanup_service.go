package services

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const partialHashChunkSize = 64 * 1024

type CleanupCriteria struct {
	MinDuration time.Duration `json:"min_duration"`
	MinWidth    int           `json:"min_width"`
	MinHeight   int           `json:"min_height"`
}

type CleanupDuplicateGroup struct {
	Original   models.Video   `json:"original"`
	Candidates []models.Video `json:"candidates"`
	Reason     string         `json:"reason"`
}

type CleanupSameSourceGroup struct {
	RelationID       uint         `json:"relation_id"`
	Preferred        models.Video `json:"preferred"`
	Alternative      models.Video `json:"alternative"`
	Confidence       string       `json:"confidence"`
	Reason           string       `json:"reason"`
	EstimatedSavings int64        `json:"estimated_savings"`
}

type CleanupAnalysis struct {
	DuplicateGroups     []CleanupDuplicateGroup  `json:"duplicate_groups"`
	NearDuplicateGroups []CleanupDuplicateGroup  `json:"near_duplicate_groups"`
	SameSourceGroups    []CleanupSameSourceGroup `json:"same_source_groups"`
	LowDuration         []models.Video           `json:"low_duration"`
	LowResolution       []models.Video           `json:"low_resolution"`
	// StaleHashCount 是还没有可用感知哈希的视频数（没回填过的 + 源文件变过失效的，
	// D-CD04）；这些视频暂不参与近似重复检测，可通过"补全感知哈希"一键补齐。
	// 与 StaleFrameHashCount 同口径：都只数本轮文件确实读得到的视频。
	StaleHashCount int64 `json:"stale_hash_count"`
	// ClipGroups 是"完整片 + 从它里面截下来的片段"候选（D-028）。
	// 建议保留完整片，前端默认不勾选，删除走回收站。
	ClipGroups []CleanupClipGroup `json:"clip_groups"`
	// StaleFrameHashCount 是还没有可用帧哈希序列的视频数（没回填过的 + 源文件
	// 变过失效的）；这些视频暂不参与截取片段识别，可通过"补全帧哈希"一键补齐。
	// 同样只数本轮文件读得到的视频——读不到的归 SkippedUnavailable 报。
	StaleFrameHashCount int64 `json:"stale_frame_hash_count"`
	// SkippedUnavailable 是本轮 os.Stat 失败或指向目录的视频数。外置盘没挂载时
	// 这个数会很大，而在有这个字段之前界面与插着盘跑出来的结果长得一模一样。
	SkippedUnavailable int `json:"skipped_unavailable"`
	// SkippedMetadata 是文件在、但时长或分辨率取不到的视频数。它们仍然参与精确
	// 重复（那只需要大小与采样哈希），只是不进低清与短视频两类。
	SkippedMetadata int `json:"skipped_metadata"`
}

type CleanupProgress struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Path    string `json:"path"`
}

type CleanupStatus struct {
	Running   bool            `json:"running"`
	Completed bool            `json:"completed"`
	Error     string          `json:"error"`
	Progress  CleanupProgress `json:"progress"`
	// Stale 表示缓存结果算出后库又发生了变化（删除、扫描、感知哈希补全等）。
	// 结果仍然保留供用户继续审阅，只是提示可能过期，由用户决定何时重新分析。
	Stale     bool             `json:"stale"`
	Analysis  *CleanupAnalysis `json:"analysis,omitempty"`
	StartedAt *time.Time       `json:"started_at,omitempty" ts_type:"string"`
	UpdatedAt *time.Time       `json:"updated_at,omitempty" ts_type:"string"`
}

type CleanupService struct {
	ctx                  context.Context
	mu                   sync.Mutex
	status               CleanupStatus
	invalidatedDuringRun bool
	// runID 每次启动分析自增。后台 goroutine 写完状态到发出 done 事件之间没有持锁，
	// 期间用户可能已经启动了新一轮；靠它判断自己是否仍是当前这轮，避免旧的收尾事件
	// 把 done 阶段盖到新一轮的状态上。
	runID    uint64
	registry *BackgroundTaskRegistry
}

// SetBackgroundTaskRegistry 接入后台任务登记表（D-014）。
// 清理分析没有取消入口，也不逐项处理，因此只登记运行区间，不装项间检查点。
func (s *CleanupService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

func (s *CleanupService) SetContext(ctx context.Context) {
	s.ctx = ctx
}

func (s *CleanupService) StartAnalysis(criteria CleanupCriteria) (*CleanupStatus, error) {
	s.mu.Lock()
	if s.status.Running {
		status := s.statusSnapshotLocked()
		s.mu.Unlock()
		return &status, nil
	}
	now := time.Now()
	s.status = CleanupStatus{
		Running:   true,
		Completed: false,
		StartedAt: &now,
		UpdatedAt: &now,
		Progress: CleanupProgress{
			Stage:   "load",
			Message: "正在准备清理候选分析…",
			Current: 0,
			Total:   0,
		},
	}
	s.invalidatedDuringRun = false
	s.runID++
	runID := s.runID
	status := s.statusSnapshotLocked()
	registry := s.registry
	s.mu.Unlock()
	registry.Begin(BackgroundTaskCleanup)

	go func() {
		// End 放在最外层 defer：它在 done 事件发出之后才执行，登记表因此可以
		// 当作"这一轮真的收尾了"的信号用（测试等待窗口不再靠猜时间）。
		defer registry.End(BackgroundTaskCleanup)
		analysis, _, err := s.analyzeCleanupCandidates(criteria)

		s.mu.Lock()
		now := time.Now()
		s.status.Running = false
		s.status.UpdatedAt = &now
		// 运行期间库发生了变化：结果仍然保留供审阅，只标记为可能过期。
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

		// done 事件必须在状态写入之后再发：前端收到 done 会立刻回读 Status()，
		// 先发事件会读到 running=true / analysis=nil，界面就永远停在"分析中"。
		// 失败同样要发终止事件，否则纯事件驱动的界面收不到任何结束信号。
		if err != nil {
			s.emitDoneForRun(runID, total, fmt.Sprintf("分析失败：%v", err))
			return
		}
		s.emitDoneForRun(runID, total, cleanupDoneMessage(analysis))
	}()

	return &status, nil
}

func (s *CleanupService) Status() *CleanupStatus {
	s.mu.Lock()
	status := s.statusSnapshotLocked()
	s.mu.Unlock()
	analysis, err := filterCleanupReviewDecisions(status.Analysis)
	if err != nil {
		// 读不到决策时不能把未经核对的旧候选交给用户；底层缓存保留供下次重试。
		status.Analysis = nil
		status.Error = fmt.Sprintf("读取清理审阅决定失败：%v", err)
		return &status
	}
	status.Analysis = analysis
	return &status
}

// InvalidateAnalysis 标记缓存结果可能已过期；结果本身保留，用户重开审阅界面
// 仍能看到并继续处理，由用户自己决定何时重新分析（不再静默丢弃并自动重跑）。
func (s *CleanupService) InvalidateAnalysis() {
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

func (s *CleanupService) statusSnapshotLocked() CleanupStatus {
	status := s.status
	if status.Analysis != nil {
		analysisCopy := *status.Analysis
		status.Analysis = &analysisCopy
	}
	return status
}

// AnalyzeCleanupCandidates 同步执行一次完整分析并发出终止事件，供 GetCleanupCandidates
// 这类同步调用方使用；异步任务走 analyzeCleanupCandidates，由 StartAnalysis 在写完状态后补发 done。
func (s *CleanupService) AnalyzeCleanupCandidates(criteria CleanupCriteria) (*CleanupAnalysis, error) {
	result, hashCandidates, err := s.analyzeCleanupCandidates(criteria)
	if err != nil {
		return nil, err
	}
	s.emitProgress("done", hashCandidates, hashCandidates, "", cleanupDoneMessage(result))
	return result, nil
}

func cleanupDoneMessage(result *CleanupAnalysis) string {
	message := fmt.Sprintf(
		"分析完成：重复组 %d，近似重复 %d，同源候选 %d，短视频 %d，低清视频 %d。",
		len(result.DuplicateGroups), len(result.NearDuplicateGroups), len(result.SameSourceGroups), len(result.LowDuration), len(result.LowResolution),
	)
	// 两项各自成句：只有一项非零时凑在一起会读成"跳过 0 个（文件不可访问），2 个
	// 取不到元数据"，一条报完成的消息里摆个 0 只会让人以为哪里没跑对。
	parts := make([]string, 0, 2)
	if result.SkippedUnavailable > 0 {
		parts = append(parts, fmt.Sprintf("跳过 %d 个（文件不可访问）", result.SkippedUnavailable))
	}
	if result.SkippedMetadata > 0 {
		parts = append(parts, fmt.Sprintf("%d 个取不到元数据、只参与精确重复", result.SkippedMetadata))
	}
	if len(parts) > 0 {
		message += strings.Join(parts, "，") + "。"
	}
	return message
}

// cleanupHasStoredMetadata 报告库里这条记录是否曾经被成功探测过。判据取自扫描侧的
// needsTechnicalRefreshDuringScan（services/video_scan.go），两处必须同口径，否则同一
// 个视频会在扫描看来是新鲜的、在清理看来要重探。额外多要一个 Width > 0：低清判定
// 读的是宽高两项，而扫描那条判据只看 Height。
//
// 它同时承担第二个职责：区分"真视频但此刻探不到"与"这条记录根本不是视频"。
func cleanupHasStoredMetadata(video models.Video) bool {
	return video.Duration > 0 && video.Resolution != "" && video.Width > 0 && video.Height > 0
}

// analyzeCleanupCandidates 返回分析结果和参与哈希比对的候选数；不发终止事件。
func (s *CleanupService) analyzeCleanupCandidates(criteria CleanupCriteria) (*CleanupAnalysis, int, error) {
	startedAt := time.Now()
	var videos []models.Video
	// 只分析扫描根之内的视频：改窄扫描目录后留下的范围外记录在列表里已经看不见，
	// 再把它们列成清理候选会让用户对着一批"不存在"的东西做决定。
	scopedQuery, err := applyScanRootScope(database.DB.Model(&models.Video{}).Order("id asc"))
	if err != nil {
		return nil, 0, err
	}
	if err := scopedQuery.Find(&videos).Error; err != nil {
		return nil, 0, err
	}
	// SQL 只裁到扫描根；黑名单是路径前缀集合，在 Go 侧过滤。
	scope, err := loadCleanupPathScope()
	if err != nil {
		return nil, 0, err
	}
	inScope := videos[:0]
	for _, video := range videos {
		if scope.contains(video.Path) {
			inScope = append(inScope, video)
		}
	}
	videos = inScope
	videoService := &VideoService{}

	log.Printf("[Cleanup] analysis started total_videos=%d criteria={min_duration=%s min_width=%d min_height=%d}",
		len(videos), criteria.MinDuration, criteria.MinWidth, criteria.MinHeight,
	)
	s.emitProgress("load", 0, len(videos), "", fmt.Sprintf("已读取 %d 条视频记录，正在整理候选…", len(videos)))

	result := &CleanupAnalysis{}
	sizeBuckets := make(map[int64][]models.Video)
	// presentVideoIDs 是本轮确认在范围内、文件也确实读得到的视频。近似重复那一步
	// 要用它数出"连感知哈希行都没有"的视频，而不是再把整库查一遍。
	presentVideoIDs := make(map[uint]struct{}, len(videos))

	for idx, video := range videos {
		info, err := os.Stat(video.Path)
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("[Cleanup] skip missing video id=%d path=%s", video.ID, video.Path)
			} else {
				log.Printf("[Cleanup] skip unreadable video id=%d path=%s err=%v", video.ID, video.Path, err)
			}
			result.SkippedUnavailable++
			continue
		}
		if info.IsDir() {
			log.Printf("[Cleanup] skip directory video id=%d path=%s", video.ID, video.Path)
			result.SkippedUnavailable++
			continue
		}
		presentVideoIDs[video.ID] = struct{}{}

		workingVideo := video
		// 大小一律以磁盘实测为准：库里的值可能滞后，而它正是精确重复的分桶键。
		workingVideo.Size = info.Size()
		hasStoredMetadata := cleanupHasStoredMetadata(video)
		hasMetadata := hasStoredMetadata && info.Size() == video.Size
		if !hasMetadata {
			// 只在库里这条不新鲜时才探测。早先是每轮对整库无条件跑一遍 ffprobe，
			// 既慢（本机 1439 个视频、大半在外置盘上），又让一次探测失败就把视频
			// 整条踢出所有类别——包括根本不需要元数据的精确重复。
			freshDuration, freshResolution, freshWidth, freshHeight := videoService.getVideoMetadata(video.Path)
			if freshDuration > 0 && freshResolution != "" && freshWidth > 0 && freshHeight > 0 {
				workingVideo.Duration = freshDuration
				workingVideo.Resolution = freshResolution
				workingVideo.Width = freshWidth
				workingVideo.Height = freshHeight
				hasMetadata = true
			} else {
				log.Printf("[Cleanup] metadata unavailable for candidate id=%d path=%s", video.ID, video.Path)
				result.SkippedMetadata++
				if !hasStoredMetadata {
					// 库里也从来没有过元数据，探测又失败：这条记录根本不是视频
					// （历史上误入库的源码文件之类）。它不该出现在任何候选里，
					// 否则用户会被引导去删一批自己的源文件。
					continue
				}
				// 库里有过元数据、只是此刻探测不到（文件损坏、外置盘抖动）：
				// 这是真视频，继续参与精确重复——那一类只看大小与采样哈希。
			}
		}

		if hasMetadata && criteria.MinDuration > 0 && time.Duration(workingVideo.Duration*float64(time.Second)) < criteria.MinDuration {
			result.LowDuration = append(result.LowDuration, workingVideo)
		}
		if hasMetadata && criteria.MinWidth > 0 && criteria.MinHeight > 0 && (workingVideo.Width < criteria.MinWidth || workingVideo.Height < criteria.MinHeight) {
			result.LowResolution = append(result.LowResolution, workingVideo)
		}
		// 元数据取不到的视频照样入桶：精确重复只看文件大小与采样哈希。
		sizeBuckets[workingVideo.Size] = append(sizeBuckets[workingVideo.Size], workingVideo)

		if shouldEmitCleanupProgress(idx+1, len(videos), 400) {
			s.emitProgress("group", idx+1, len(videos), video.Path, "正在按文件大小聚合候选…")
		}
	}

	hashCandidates := make([]models.Video, 0)
	for _, bucket := range sizeBuckets {
		if len(bucket) < 2 {
			continue
		}
		hashCandidates = append(hashCandidates, bucket...)
	}

	s.emitProgress("hash", 0, len(hashCandidates), "", fmt.Sprintf("发现 %d 个疑似重复文件，正在读取采样哈希…", len(hashCandidates)))

	duplicateBuckets := make(map[string][]models.Video)
	for idx, video := range hashCandidates {
		hash, err := getPartialHash(video.Path)
		if err != nil || hash == "" {
			if shouldEmitCleanupProgress(idx+1, len(hashCandidates), 50) {
				s.emitProgress("hash", idx+1, len(hashCandidates), video.Path, "正在读取疑似重复文件的采样哈希…")
			}
			continue
		}
		bucketKey := buildDuplicateBucketKey(video.Size, hash)
		duplicateBuckets[bucketKey] = append(duplicateBuckets[bucketKey], video)

		if shouldEmitCleanupProgress(idx+1, len(hashCandidates), 50) {
			s.emitProgress("hash", idx+1, len(hashCandidates), video.Path, "正在读取疑似重复文件的采样哈希…")
		}
	}

	for _, bucket := range duplicateBuckets {
		if len(bucket) < 2 {
			continue
		}
		sort.Slice(bucket, func(i, j int) bool {
			return isPreferredOriginal(bucket[i], bucket[j])
		})
		result.DuplicateGroups = append(result.DuplicateGroups, CleanupDuplicateGroup{
			Original:   bucket[0],
			Candidates: append([]models.Video(nil), bucket[1:]...),
			Reason:     "文件大小和采样哈希一致",
		})
	}

	exactPairs := make(map[[2]uint]struct{})
	for _, group := range result.DuplicateGroups {
		members := append([]models.Video{group.Original}, group.Candidates...)
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				exactPairs[cleanupVideoPairKey(members[i].ID, members[j].ID)] = struct{}{}
			}
		}
	}
	// 用户对一对视频说过"不是同片"或"不是同源"，两种说法表达的是同一个判断：
	// 这一对别再换一种相似关系冒出来。两张否决表合成一个排除集，近似重复、同源
	// 和截取三类共用；只有“忽略截取”仍按它自己的方向与文件版本规则处理。
	dismissed, err := loadCleanupReviewPairs()
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
	nearDuplicateGroups, nearPairs, staleHashCount, err := loadCleanupNearDuplicateGroups(excludedPairs, presentVideoIDs)
	if err != nil {
		return nil, 0, err
	}
	result.NearDuplicateGroups = nearDuplicateGroups
	result.StaleHashCount = staleHashCount
	for pair := range nearPairs {
		excludedPairs[pair] = struct{}{}
	}
	sameSourceGroups, err := loadCleanupSameSourceGroups(excludedPairs)
	if err != nil {
		return nil, 0, err
	}
	result.SameSourceGroups = sameSourceGroups

	// 已否认同片/同源的对也不能换成截取候选再问；尚未否认的近似/同源候选不排除。
	clipExcluded := cleanupExactDuplicatePairs(result.DuplicateGroups)
	for pair := range dismissed {
		clipExcluded[pair] = struct{}{}
	}
	clipGroups, staleFrameHashCount, err := loadCleanupClipGroups(clipExcluded, presentVideoIDs)
	if err != nil {
		return nil, 0, err
	}
	result.ClipGroups = clipGroups
	result.StaleFrameHashCount = staleFrameHashCount

	sort.Slice(result.DuplicateGroups, func(i, j int) bool {
		return result.DuplicateGroups[i].Original.ID < result.DuplicateGroups[j].Original.ID
	})
	// 匹配可能耗时较长，完成前再读决定，避免这期间刚忽略的项进入新结果。
	result, err = filterCleanupReviewDecisions(result)
	if err != nil {
		return nil, 0, err
	}

	log.Printf("[Cleanup] analysis completed elapsed=%s duplicate_groups=%d near_duplicate_groups=%d same_source_groups=%d low_duration=%d low_resolution=%d hash_candidates=%d stale_hash=%d skipped_unavailable=%d skipped_metadata=%d",
		time.Since(startedAt).Round(time.Millisecond),
		len(result.DuplicateGroups), len(result.NearDuplicateGroups), len(result.SameSourceGroups), len(result.LowDuration), len(result.LowResolution), len(hashCandidates),
		result.StaleHashCount, result.SkippedUnavailable, result.SkippedMetadata,
	)
	// done 事件由调用方在写完状态后发出，这里不发，避免前端收到 done 时回读到尚未写入结果的状态。
	return result, len(hashCandidates), nil
}

// emitDoneForRun 只在自己仍是当前这轮分析时写入 done 进度并发事件。
func (s *CleanupService) emitDoneForRun(runID uint64, total int, message string) {
	progress := CleanupProgress{Stage: "done", Message: message, Current: total, Total: total}
	s.mu.Lock()
	if s.runID != runID {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	s.status.Progress = progress
	s.status.UpdatedAt = &now
	s.mu.Unlock()

	if s.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(s.ctx, "cleanup-progress", progress)
}

// loadRejectedSameSourcePairs 返回用户已判"不是同源"的视频对。
func loadRejectedSameSourcePairs() (map[[2]uint]struct{}, error) {
	var relations []models.VideoSameSourceRelation
	if err := database.DB.Select("video_a_id", "video_b_id").
		Where("status = ?", models.VideoSameSourceStatusRejected).
		Find(&relations).Error; err != nil {
		return nil, err
	}
	pairs := make(map[[2]uint]struct{}, len(relations))
	for _, relation := range relations {
		pairs[cleanupVideoPairKey(relation.VideoAID, relation.VideoBID)] = struct{}{}
	}
	return pairs, nil
}

// excludedPairs 里是精确重复、近似重复已认领的对，以及用户忽略过的对。
func loadCleanupSameSourceGroups(excludedPairs map[[2]uint]struct{}) ([]CleanupSameSourceGroup, error) {
	var relations []models.VideoSameSourceRelation
	err := database.DB.Model(&models.VideoSameSourceRelation{}).
		Joins("INNER JOIN videos AS same_source_video_a ON same_source_video_a.id = video_same_source_relations.video_a_id AND same_source_video_a.deleted_at IS NULL").
		Joins("INNER JOIN videos AS same_source_video_b ON same_source_video_b.id = video_same_source_relations.video_b_id AND same_source_video_b.deleted_at IS NULL").
		Preload("VideoA.Tags").
		Preload("VideoB.Tags").
		Where("video_same_source_relations.status = ?", models.VideoSameSourceStatusDetected).
		Order("video_same_source_relations.id ASC").
		Find(&relations).Error
	if err != nil {
		return nil, err
	}

	scope, err := loadCleanupPathScope()
	if err != nil {
		return nil, err
	}
	groups := make([]CleanupSameSourceGroup, 0, len(relations))
	for _, relation := range relations {
		if _, excluded := excludedPairs[cleanupVideoPairKey(relation.VideoAID, relation.VideoBID)]; excluded {
			continue
		}
		// 任一侧落在扫描根之外或黑名单里，这一对就不该出现在清理候选里。
		if !scope.contains(relation.VideoA.Path) || !scope.contains(relation.VideoB.Path) {
			continue
		}
		preferred, alternative := relation.VideoA, relation.VideoB
		if isPreferredCleanupVideo(alternative, preferred) {
			preferred, alternative = alternative, preferred
		}
		reason := relation.Reasoning
		if reason == "" {
			reason = "画面指纹与 AI 复核判断为同源视频"
		}
		groups = append(groups, CleanupSameSourceGroup{
			RelationID: relation.ID, Preferred: preferred, Alternative: alternative,
			Confidence: relation.Confidence, Reason: reason, EstimatedSavings: alternative.Size,
		})
	}
	return groups, nil
}

func cleanupVideoPairKey(a, b uint) [2]uint {
	if a > b {
		a, b = b, a
	}
	return [2]uint{a, b}
}

func shouldEmitCleanupProgress(current int, total int, every int) bool {
	if total <= 0 {
		return false
	}
	if current <= 1 || current >= total {
		return true
	}
	return every > 0 && current%every == 0
}

func (s *CleanupService) emitProgress(stage string, current int, total int, currentPath string, message string) {
	progress := CleanupProgress{
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
	s.mu.Unlock()

	if s.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(s.ctx, "cleanup-progress", progress)
}

func buildDuplicateBucketKey(size int64, hash string) string {
	return fmt.Sprintf("%d:%s", size, hash)
}

func isPreferredOriginal(a, b models.Video) bool {
	return isPreferredCleanupVideo(a, b)
}

func isPreferredCleanupVideo(a, b models.Video) bool {
	aPixels := a.Width * a.Height
	bPixels := b.Width * b.Height
	if aPixels != bPixels {
		return aPixels > bPixels
	}
	if a.Size != b.Size {
		return a.Size > b.Size
	}
	if len(a.Tags) != len(b.Tags) {
		return len(a.Tags) > len(b.Tags)
	}
	return a.ID < b.ID
}

func getPartialHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}

	size := info.Size()
	hash := md5.New()

	if _, err := io.CopyN(hash, f, partialHashChunkSize); err != nil && err != io.EOF {
		return "", err
	}

	if size > partialHashChunkSize*3 {
		if _, err := f.Seek(size/2, io.SeekStart); err == nil {
			_, _ = io.CopyN(hash, f, partialHashChunkSize)
		}
	}

	if size > partialHashChunkSize {
		if _, err := f.Seek(size-partialHashChunkSize, io.SeekStart); err == nil {
			_, _ = io.Copy(hash, f)
		}
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
