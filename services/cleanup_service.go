package services

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
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
	"video-master/services/subtitleparser"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"gorm.io/gorm"
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
	// Confirmed 表示用户已在 AI 审阅里确认这一对同源（reviewed_at 非空），清理中心据此标出「已确认同源」。
	Confirmed bool `json:"confirmed"`
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
	// SkippedClipVerification 是因超时、读取失败或源变化而未完成复核的配对数。
	SkippedClipVerification int `json:"skipped_clip_verification"`
	// SkippedUnavailable 是本轮 os.Stat 失败或指向目录的视频数。外置盘没挂载时
	// 这个数会很大，而在有这个字段之前界面与插着盘跑出来的结果长得一模一样。
	SkippedUnavailable int `json:"skipped_unavailable"`
	// SkippedMetadata 是文件在、但时长或分辨率取不到的视频数。它们仍然参与精确
	// 重复（那只需要大小与采样哈希），只是不进低清与短视频两类。
	SkippedMetadata int `json:"skipped_metadata"`
	// Coverage 是各类别前置条件的覆盖率（D-PC50）：done==0 时界面显示「尚未计算」，
	// done<total 时显示「已算 X / Y」，让"没有重复"与"还没算"分得开。
	Coverage CleanupCoverage `json:"coverage"`
	// Curation 按视频 ID 给出每个候选命中的整理项（D-PC48），覆盖本结果里出现的全部视频。
	// 保留建议用其计分择优（精确重复先集中目录）；卡片据此显示收藏、评分、字幕等图标。
	Curation map[uint]CleanupCuration `json:"curation"`
	// Thresholds 是本轮「极短片段 / 极低分辨率」实际使用的阈值（D-PC36），类别标题按它显示；
	// 为 0 表示本轮没有评估该类别。
	Thresholds CleanupThresholds `json:"thresholds"`
	// sourceFingerprints 是本轮 os.Stat 得到的 size:mtimeNS（只含文件读得到的视频）。
	// 回读缓存时拿它判断忽略记录是否失效：不能拿之后重新 stat 的指纹冒充当时的版本。
	sourceFingerprints map[uint]string
}

// CleanupCuration 是一个视频上用户整理成果的命中情况（D-PC48）。8 项中成立的项数即整理分。
type CleanupCuration struct {
	Favorite    bool `json:"favorite"`
	Liked       bool `json:"liked"`
	Rating      bool `json:"rating"`
	People      bool `json:"people"`
	Tags        bool `json:"tags"` // 只算非自动标签
	Subtitle    bool `json:"subtitle"`
	Collections bool `json:"collections"`
	Progress    bool `json:"progress"` // is_watched || watch_position_seconds > 0
}

// Score 返回成立的整理项数。
func (c CleanupCuration) Score() int {
	score := 0
	for _, hit := range []bool{c.Favorite, c.Liked, c.Rating, c.People, c.Tags, c.Subtitle, c.Collections, c.Progress} {
		if hit {
			score++
		}
	}
	return score
}

// CleanupCoverageCount 是一个前置条件的完成度。Total 为范围内活跃且非失效的媒体数。
type CleanupCoverageCount struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

// CleanupSameSourceCoverage 是同源类别的覆盖率：同源只由 AI 打标的「查找同源」产生，
// 清理分析只读已有关系，Evaluated 是已经做过同源画面指纹的视频数。
type CleanupSameSourceCoverage struct {
	Evaluated int64 `json:"evaluated"`
	Total     int64 `json:"total"`
}

// CleanupCoverage 是视频清理各类别的覆盖率（D-PC50）。
type CleanupCoverage struct {
	PerceptualHash CleanupCoverageCount      `json:"perceptual_hash"`
	FrameHash      CleanupCoverageCount      `json:"frame_hash"`
	SameSource     CleanupSameSourceCoverage `json:"same_source"`
}

// CleanupThresholds 是「极短片段 / 极低分辨率」两类的判定阈值（秒、像素）。
type CleanupThresholds struct {
	ShortSeconds int `json:"short_seconds"`
	LowWidth     int `json:"low_width"`
	LowHeight    int `json:"low_height"`
}

type CleanupProgress struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Path    string `json:"path"`
}

type CleanupStatus struct {
	Running   bool `json:"running"`
	Completed bool `json:"completed"`
	// Cancelled 表示上一轮分析被用户取消（D-PC51），此时没有结果、也不算失败。
	Cancelled bool            `json:"cancelled"`
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
	clipFrame            clipFrameReader
	clipTimeout          time.Duration // 为零时使用默认的配对复核期限。
	mu                   sync.Mutex
	status               CleanupStatus
	invalidatedDuringRun bool
	// runID 每次启动分析自增。后台 goroutine 写完状态到发出 done 事件之间没有持锁，
	// 期间用户可能已经启动了新一轮；靠它判断自己是否仍是当前这轮，避免旧的收尾事件
	// 把 done 阶段盖到新一轮的状态上。
	runID    uint64
	registry *BackgroundTaskRegistry
	// cancel 取消当前这一轮异步分析（D-PC51）；只在 Running 期间非空。
	cancel context.CancelFunc
	// 集中整理的预览由 mu 保护；序号防止较早请求覆盖较晚请求。
	consolidationVideo           *VideoService
	consolidationPreview         *cleanupConsolidationPlan
	consolidationPreviewSequence uint64
	// consolidationMu 串行化启动、恢复与维护接受围栏；不会与 s.mu 同时等待 worker。
	consolidationMu        sync.Mutex
	consolidationDirectory FileMigrationDirectory
	consolidationDataDir   string
	consolidationOwner     string
	consolidationBlocked   bool
	consolidationCancel    context.CancelFunc
	consolidationDone      chan struct{}
	consolidationOnChange  func(CleanupConsolidationSummary)
	consolidationHooks     *consolidationExecutionHooks
}

// ErrCleanupAnalysisNotRunning 是取消时没有正在进行的分析。
var ErrCleanupAnalysisNotRunning = errors.New("清理分析未在运行")

// SetBackgroundTaskRegistry 接入后台任务登记表（D-014）。
// 清理分析不逐项处理，因此只登记运行区间，不装项间检查点；取消走 CancelAnalysis。
func (s *CleanupService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

func (s *CleanupService) SetContext(ctx context.Context) {
	s.ctx = ctx
}

// baseContext 是分析的父上下文：应用上下文（应用退出时取消），没有时用 Background。
func (s *CleanupService) baseContext() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

// CleanupCriteriaFromSettings 从设置读取「极短片段 / 极低分辨率」阈值（D-PC36）。
// ≤0 视为默认值（database.DefaultCleanup*）；设置行还不存在时同样用默认值。
func CleanupCriteriaFromSettings() (CleanupCriteria, error) {
	var settings models.Settings
	err := database.DB.Select("cleanup_short_seconds", "cleanup_low_width", "cleanup_low_height").First(&settings).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return CleanupCriteria{}, fmt.Errorf("读取清理阈值失败: %w", err)
	}
	return CleanupCriteria{
		MinDuration: time.Duration(positiveOrDefault(settings.CleanupShortSeconds, database.DefaultCleanupShortSeconds)) * time.Second,
		MinWidth:    positiveOrDefault(settings.CleanupLowWidth, database.DefaultCleanupLowWidth),
		MinHeight:   positiveOrDefault(settings.CleanupLowHeight, database.DefaultCleanupLowHeight),
	}, nil
}

// StartAnalysisFromSettings 用设置里的阈值启动一轮异步分析（D-PC36），前端不再写死阈值。
func (s *CleanupService) StartAnalysisFromSettings() (*CleanupStatus, error) {
	criteria, err := CleanupCriteriaFromSettings()
	if err != nil {
		return nil, err
	}
	return s.StartAnalysis(criteria)
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
	runCtx, cancel := context.WithCancel(s.baseContext())
	s.cancel = cancel
	status := s.statusSnapshotLocked()
	registry := s.registry
	s.mu.Unlock()
	registry.Begin(BackgroundTaskCleanup)

	go func() {
		// End 放在最外层 defer：它在 done 事件发出之后才执行，登记表因此可以
		// 当作"这一轮真的收尾了"的信号用（测试等待窗口不再靠猜时间）。
		defer registry.End(BackgroundTaskCleanup)
		defer cancel()
		analysis, _, err := s.analyzeCleanupCandidates(runCtx, criteria)
		// 取消只在 ctx 真的被取消时才算：同一轮里别的错误照常按失败报。
		cancelled := err != nil && runCtx.Err() != nil

		s.mu.Lock()
		now := time.Now()
		s.status.Running = false
		s.status.UpdatedAt = &now
		s.cancel = nil
		// 运行期间库发生了变化：结果仍然保留供审阅，只标记为可能过期。
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

		// done 事件必须在状态写入之后再发：前端收到 done 会立刻回读 Status()，
		// 先发事件会读到 running=true / analysis=nil，界面就永远停在"分析中"。
		// 失败与取消同样要发终止事件，否则纯事件驱动的界面收不到任何结束信号。
		switch {
		case cancelled:
			s.emitDoneForRun(runID, total, "已取消清理分析。")
		case err != nil:
			s.emitDoneForRun(runID, total, fmt.Sprintf("分析失败：%v", err))
		default:
			s.emitDoneForRun(runID, total, cleanupDoneMessage(analysis))
		}
	}()

	return &status, nil
}

// CancelAnalysis 取消进行中的异步分析（D-PC51）。分析在逐个视频、逐对比较之间检查取消，
// 状态在后台 goroutine 真正停下之后才变为 cancelled（期间 Running 仍为 true，不会并发起第二轮）。
// 截取片段正在做的那一对画面复核不受影响，最多等到它自己的期限（30 秒）结束。
func (s *CleanupService) CancelAnalysis() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.status.Running || s.cancel == nil {
		return ErrCleanupAnalysisNotRunning
	}
	s.cancel()
	return nil
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
	result, hashCandidates, err := s.analyzeCleanupCandidates(s.baseContext(), criteria)
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
	parts := make([]string, 0, 3)
	if result.SkippedUnavailable > 0 {
		parts = append(parts, fmt.Sprintf("跳过 %d 个（文件不可访问）", result.SkippedUnavailable))
	}
	if result.SkippedMetadata > 0 {
		parts = append(parts, fmt.Sprintf("%d 个取不到元数据、只参与精确重复", result.SkippedMetadata))
	}
	if result.SkippedClipVerification > 0 {
		parts = append(parts, fmt.Sprintf("%d 组截取片段配对复核未完成、已跳过", result.SkippedClipVerification))
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
// ctx 被取消时在下一个视频 / 下一对比较之前停下，返回 ctx.Err()。
func (s *CleanupService) analyzeCleanupCandidates(ctx context.Context, criteria CleanupCriteria) (*CleanupAnalysis, int, error) {
	startedAt := time.Now()
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	var videos []models.Video
	// 只分析扫描根之内的视频：改窄扫描目录后留下的范围外记录在列表里已经看不见，
	// 再把它们列成清理候选会让用户对着一批"不存在"的东西做决定。
	scopedQuery, err := applyScanRootScope(database.DB.WithContext(ctx).Model(&models.Video{}).Order("id asc"))
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

	result := &CleanupAnalysis{Thresholds: CleanupThresholds{
		ShortSeconds: int(criteria.MinDuration / time.Second),
		LowWidth:     criteria.MinWidth,
		LowHeight:    criteria.MinHeight,
	}}
	sizeBuckets := make(map[int64][]models.Video)
	// presentVideoIDs 是本轮确认在范围内、文件也确实读得到的视频。近似重复那一步
	// 要用它数出"连感知哈希行都没有"的视频，而不是再把整库查一遍。
	presentVideoIDs := make(map[uint]struct{}, len(videos))
	// fingerprints 是同一批视频本轮的 size:mtimeNS，忽略记录据此判断是否失效（D-PC31）。
	fingerprints := make(map[uint]string, len(videos))

	for idx, video := range videos {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
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
		fingerprints[video.ID] = cleanupFileFingerprint(info.Size(), info.ModTime().UnixNano())

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
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
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
		// 这里先按不含整理分的元组排；整理分在所有类别成组之后批量取齐，再统一重排（rankCleanupCandidates）。
		sort.Slice(bucket, func(i, j int) bool {
			return isPreferredCleanupVideo(bucket[i], bucket[j], nil)
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
	// 近似重复的忽略带双方指纹，任一侧文件变了就不再算数（D-PC31）。
	dismissed, err := loadCleanupReviewPairs(fingerprints)
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
	nearDuplicateGroups, nearPairs, staleHashCount, err := loadCleanupNearDuplicateGroups(ctx, excludedPairs, presentVideoIDs)
	if err != nil {
		return nil, 0, err
	}
	result.NearDuplicateGroups = nearDuplicateGroups
	result.StaleHashCount = staleHashCount
	for pair := range nearPairs {
		excludedPairs[pair] = struct{}{}
	}
	sameSourceGroups, err := loadCleanupSameSourceGroups(ctx, excludedPairs)
	if err != nil {
		return nil, 0, err
	}
	result.SameSourceGroups = sameSourceGroups

	// 已否认同片/同源的对也不能换成截取候选再问；尚未否认的近似/同源候选不排除。
	clipExcluded := cleanupExactDuplicatePairs(result.DuplicateGroups)
	for pair := range dismissed {
		clipExcluded[pair] = struct{}{}
	}
	clipGroups, staleFrameHashCount, skippedClipVerification, err := s.loadCleanupClipGroups(ctx, clipExcluded, presentVideoIDs)
	if err != nil {
		return nil, 0, err
	}
	result.ClipGroups = clipGroups
	result.StaleFrameHashCount = staleFrameHashCount
	result.SkippedClipVerification = skippedClipVerification

	// 覆盖率放在截取匹配之后：它只读指纹表的窄列，不影响前面几类的输入。
	coverage, err := loadCleanupCoverage(ctx, videos, fingerprints)
	if err != nil {
		return nil, 0, err
	}
	result.Coverage = coverage
	result.sourceFingerprints = fingerprints

	// 各类成组后一次取齐整理项；精确重复先集中目录，其余类别仍按整理成果排序。
	if err := rankCleanupCandidates(ctx, result); err != nil {
		return nil, 0, err
	}
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
// 这里的保留建议只按不含整理分的元组给出，rankCleanupCandidates 之后再按整理分重排。
func loadCleanupSameSourceGroups(ctx context.Context, excludedPairs map[[2]uint]struct{}) ([]CleanupSameSourceGroup, error) {
	var relations []models.VideoSameSourceRelation
	err := database.DB.WithContext(ctx).Model(&models.VideoSameSourceRelation{}).
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
		if isPreferredCleanupVideo(alternative, preferred, nil) {
			preferred, alternative = alternative, preferred
		}
		reason := relation.Reasoning
		if reason == "" {
			reason = "画面指纹与 AI 复核判断为同源视频"
		}
		groups = append(groups, CleanupSameSourceGroup{
			RelationID: relation.ID, Preferred: preferred, Alternative: alternative,
			Confidence: relation.Confidence, Reason: reason, EstimatedSavings: alternative.Size,
			Confirmed: relation.ReviewedAt != nil,
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

// isPreferredCleanupVideo 是保留建议的元组比较（D-PC48）：
// (整理分 DESC, 像素 DESC, 体积 DESC, ID ASC)。curation 为 nil 时整理分都记 0。
//
// 早先的规则是 像素 > 体积 > 标签数 > ID，而精确重复的查询又没预载标签，实际等于
// 「像素一样就留 ID 小的」——用户花心思整理过的那一份照样会被建议删掉。
func isPreferredCleanupVideo(a, b models.Video, curation map[uint]CleanupCuration) bool {
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

// cleanupRankChunkSize 是批量取整理项时单条 IN 语句的 id 上限，远低于两个后端的绑定参数上限。
const cleanupRankChunkSize = 500

// rankCleanupCandidates 在所有类别成组之后批量取齐整理项，精确重复先集中目录，
// 近似重复与同源仍按 isPreferredCleanupVideo 择优；同时给精确重复与极短/极低两类补上标签
// （它们来自不预载标签的主查询）。截取片段的保留项由时长决定，不参与重排。
//
// 整理项读不到时整轮分析失败：保留建议就是"建议删哪一份"，不能在缺数据时静默退回旧规则。
func rankCleanupCandidates(ctx context.Context, result *CleanupAnalysis) error {
	ids := cleanupAnalysisVideoIDs(result)
	curation, err := loadCleanupVideoCuration(ctx, ids)
	if err != nil {
		return fmt.Errorf("读取视频整理信息失败: %w", err)
	}
	result.Curation = curation

	rankGroup := func(group *CleanupDuplicateGroup) {
		members := append([]models.Video{group.Original}, group.Candidates...)
		sort.SliceStable(members, func(i, j int) bool { return isPreferredCleanupVideo(members[i], members[j], curation) })
		group.Original = members[0]
		group.Candidates = append([]models.Video(nil), members[1:]...)
	}
	if err := rankExactCleanupGroups(ctx, result.DuplicateGroups, curation); err != nil {
		return err
	}
	for i := range result.NearDuplicateGroups {
		rankGroup(&result.NearDuplicateGroups[i])
	}
	for i := range result.SameSourceGroups {
		group := &result.SameSourceGroups[i]
		if isPreferredCleanupVideo(group.Alternative, group.Preferred, curation) {
			group.Preferred, group.Alternative = group.Alternative, group.Preferred
		}
		group.EstimatedSavings = group.Alternative.Size
	}
	sort.Slice(result.DuplicateGroups, func(i, j int) bool {
		return result.DuplicateGroups[i].Original.ID < result.DuplicateGroups[j].Original.ID
	})
	sort.Slice(result.NearDuplicateGroups, func(i, j int) bool {
		return result.NearDuplicateGroups[i].Original.ID < result.NearDuplicateGroups[j].Original.ID
	})

	// 精确重复与极短/极低两类来自主查询，没有预载标签：卡片上的标签会显示成空。
	tagTargets := make([]uint, 0)
	for _, group := range result.DuplicateGroups {
		tagTargets = append(tagTargets, group.Original.ID)
		tagTargets = append(tagTargets, videoIDsOf(group.Candidates)...)
	}
	tagTargets = append(tagTargets, videoIDsOf(result.LowDuration)...)
	tagTargets = append(tagTargets, videoIDsOf(result.LowResolution)...)
	tags, err := loadCleanupVideoTags(ctx, uniqueUintIDs(tagTargets))
	if err != nil {
		return fmt.Errorf("读取视频标签失败: %w", err)
	}
	withTags := func(video *models.Video) {
		video.Tags = tags[video.ID]
		if video.Tags == nil {
			video.Tags = []models.Tag{}
		}
	}
	for i := range result.DuplicateGroups {
		withTags(&result.DuplicateGroups[i].Original)
		for j := range result.DuplicateGroups[i].Candidates {
			withTags(&result.DuplicateGroups[i].Candidates[j])
		}
	}
	for i := range result.LowDuration {
		withTags(&result.LowDuration[i])
	}
	for i := range result.LowResolution {
		withTags(&result.LowResolution[i])
	}
	return nil
}

// cleanupAnalysisVideoIDs 收集结果里出现的全部视频 ID（去重、保持首次出现顺序）。
func cleanupAnalysisVideoIDs(result *CleanupAnalysis) []uint {
	ids := make([]uint, 0)
	for _, groups := range [][]CleanupDuplicateGroup{result.DuplicateGroups, result.NearDuplicateGroups} {
		for _, group := range groups {
			ids = append(ids, group.Original.ID)
			ids = append(ids, videoIDsOf(group.Candidates)...)
		}
	}
	for _, group := range result.SameSourceGroups {
		ids = append(ids, group.Preferred.ID, group.Alternative.ID)
	}
	for _, group := range result.ClipGroups {
		ids = append(ids, group.Full.ID, group.Clip.ID)
	}
	ids = append(ids, videoIDsOf(result.LowDuration)...)
	ids = append(ids, videoIDsOf(result.LowResolution)...)
	return uniqueUintIDs(ids)
}

func videoIDsOf(videos []models.Video) []uint {
	ids := make([]uint, 0, len(videos))
	for _, video := range videos {
		ids = append(ids, video.ID)
	}
	return ids
}

// loadCleanupVideoCuration 分批一次取齐 8 项整理信息（D-PC48）。同名 .srt 看磁盘（索引可能滞后），
// 其余全部来自数据库；非自动标签与作品集都只算未删除的。
func loadCleanupVideoCuration(ctx context.Context, ids []uint) (map[uint]CleanupCuration, error) {
	curation := make(map[uint]CleanupCuration, len(ids))
	if len(ids) == 0 {
		return curation, nil
	}
	db := database.DB.WithContext(ctx)
	for _, chunk := range chunkUintIDs(ids, cleanupRankChunkSize) {
		var rows []struct {
			ID                   uint
			Path                 string
			IsFavorite           bool
			IsLiked              bool
			PersonalRating       *float64
			IsWatched            bool
			WatchPositionSeconds float64
		}
		if err := db.Model(&models.Video{}).
			Select("id", "path", "is_favorite", "is_liked", "personal_rating", "is_watched", "watch_position_seconds").
			Where("id IN ?", chunk).Scan(&rows).Error; err != nil {
			return nil, err
		}
		var withPeople, withTags, inCollections []uint
		if err := db.Model(&models.VideoPerson{}).Where("video_id IN ?", chunk).Distinct().Pluck("video_id", &withPeople).Error; err != nil {
			return nil, err
		}
		if err := db.Table("video_tags").
			Joins("JOIN tags ON tags.id = video_tags.tag_id").
			Where("video_tags.video_id IN ? AND COALESCE(tags.automatic_kind, '') = '' AND tags.deleted_at IS NULL", chunk).
			Distinct().Pluck("video_tags.video_id", &withTags).Error; err != nil {
			return nil, err
		}
		if err := db.Table("collection_videos").
			Joins("JOIN media_collections ON media_collections.id = collection_videos.collection_id").
			Where("collection_videos.video_id IN ? AND media_collections.deleted_at IS NULL", chunk).
			Distinct().Pluck("collection_videos.video_id", &inCollections).Error; err != nil {
			return nil, err
		}
		people, tagged, collected := idSet(withPeople), idSet(withTags), idSet(inCollections)
		for _, row := range rows {
			_, hasPeople := people[row.ID]
			_, hasTags := tagged[row.ID]
			_, inCollection := collected[row.ID]
			curation[row.ID] = CleanupCuration{
				Favorite:    row.IsFavorite,
				Liked:       row.IsLiked,
				Rating:      row.PersonalRating != nil,
				People:      hasPeople,
				Tags:        hasTags,
				Subtitle:    hasSameNameSubtitle(row.Path),
				Collections: inCollection,
				Progress:    row.IsWatched || row.WatchPositionSeconds > 0,
			}
		}
	}
	return curation, nil
}

// hasSameNameSubtitle 报告视频旁边有没有同名 .srt（普通文件）。
func hasSameNameSubtitle(videoPath string) bool {
	if strings.TrimSpace(videoPath) == "" {
		return false
	}
	info, err := os.Stat(subtitleparser.SRTPathForVideo(videoPath))
	return err == nil && info.Mode().IsRegular()
}

// loadCleanupVideoTags 分批为给定视频取标签（与列表口径一致的 Preload）。
func loadCleanupVideoTags(ctx context.Context, ids []uint) (map[uint][]models.Tag, error) {
	tags := make(map[uint][]models.Tag, len(ids))
	for _, chunk := range chunkUintIDs(ids, cleanupRankChunkSize) {
		if len(chunk) == 0 {
			continue
		}
		var tagged []models.Video
		if err := database.DB.WithContext(ctx).Preload("Tags").Select("id").Where("id IN ?", chunk).Find(&tagged).Error; err != nil {
			return nil, err
		}
		for _, video := range tagged {
			tags[video.ID] = video.Tags
		}
	}
	return tags, nil
}

// loadCleanupCoverage 统计三类前置条件的覆盖率（D-PC50）。
//
// Total 是范围内活跃且非失效的视频数。Done 数"有可用指纹"的视频：本轮文件读得到的，
// 指纹必须与文件一致；本轮读不到的（外置盘没插）无从核对，有完整的行就算已算过——
// 否则拔掉一块盘，界面就会把早已补全的指纹报成「尚未计算」。
// 只读三张指纹表的窄列，不读帧哈希的 blob。
func loadCleanupCoverage(ctx context.Context, videos []models.Video, fingerprints map[uint]string) (CleanupCoverage, error) {
	eligible := make(map[uint]struct{}, len(videos))
	for _, video := range videos {
		if !video.IsStale {
			eligible[video.ID] = struct{}{}
		}
	}
	total := int64(len(eligible))
	coverage := CleanupCoverage{
		PerceptualHash: CleanupCoverageCount{Total: total},
		FrameHash:      CleanupCoverageCount{Total: total},
		SameSource:     CleanupSameSourceCoverage{Total: total},
	}
	if total == 0 {
		return coverage, nil
	}
	matchesFile := func(videoID uint, size, modTimeNS int64) bool {
		current, present := fingerprints[videoID]
		return !present || current == cleanupFileFingerprint(size, modTimeNS)
	}
	db := database.DB.WithContext(ctx)

	var perceptual []models.VideoPerceptualHash
	if err := db.Select("video_id", "source_size", "source_mod_time_ns", "hash_early", "hash_middle", "hash_late").
		Find(&perceptual).Error; err != nil {
		return CleanupCoverage{}, err
	}
	for _, row := range perceptual {
		if _, ok := eligible[row.VideoID]; ok && perceptualHashRowComplete(row) && matchesFile(row.VideoID, row.SourceSize, row.SourceModTimeNS) {
			coverage.PerceptualHash.Done++
		}
	}

	var sequences []models.VideoFrameHashSequence
	if err := db.Select("video_id", "interval_ms", "frame_count", "source_size", "source_mod_time_ns", "last_error").
		Find(&sequences).Error; err != nil {
		return CleanupCoverage{}, err
	}
	for _, row := range sequences {
		if _, ok := eligible[row.VideoID]; ok && frameHashRowCurrent(row) && matchesFile(row.VideoID, row.SourceSize, row.SourceModTimeNS) {
			coverage.FrameHash.Done++
		}
	}

	var evaluated []uint
	if err := db.Model(&models.VideoVisualFingerprint{}).
		Where("algorithm_version = ?", sameSourceFingerprintVersion).
		Pluck("video_id", &evaluated).Error; err != nil {
		return CleanupCoverage{}, err
	}
	for _, id := range uniqueUintIDs(evaluated) {
		if _, ok := eligible[id]; ok {
			coverage.SameSource.Evaluated++
		}
	}
	return coverage, nil
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
