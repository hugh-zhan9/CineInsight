package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 代理任务的逐项结果码（D-006）。created 是唯一的成功值。
const (
	PlaybackProxyCodeCreated       = "created"
	PlaybackProxyCodeAlreadyExists = "already_exists"
	PlaybackProxyCodeInProgress    = "in_progress"
	PlaybackProxyCodeSourceChanged = "source_changed"
	PlaybackProxyCodeEncodeFailed  = "encode_failed"
	PlaybackProxyCodeDiskFull      = "disk_full"
	PlaybackProxyCodeFileMissing   = "file_missing"
	PlaybackProxyCodeProbeFailed   = "probe_failed"
	// PlaybackProxyCodeCancelled 是被取消的项。取消的项也要有逐项结果，
	// 否则 Processed 永远追不上 Total，界面上看着像"卡在半路"。
	PlaybackProxyCodeCancelled = "cancelled"
)

// ErrPlaybackProxyNotRunning 表示当前没有代理任务在跑，取消无从谈起。
var ErrPlaybackProxyNotRunning = errors.New("playback proxy task is not running")

// ErrPlaybackProxyStopping 表示上一轮刚被取消、worker 还在退出中。
//
// 这时候不能把新项塞进队列：那一轮的 ctx 已经死了，worker 出门时会把队列清空，
// 用户的请求会被静默吞掉。宁可明确报"正在取消"让他重试一次，也不要悄悄丢。
// 与 LocalMetadataService.StartExport 的"正在停止"同一套路。
var ErrPlaybackProxyStopping = errors.New("播放代理任务正在停止，请稍后重试")

// playbackProxyResultLimit 是状态里保留的逐项结果条数上限。
const playbackProxyResultLimit = 200

// playbackProxyDurationTolerance 是产物时长与源时长的允许偏差（±2%，流程步骤 7）。
const playbackProxyDurationTolerance = 0.02

// PlaybackProxyItemResult 是一项代理任务的结果。Code 为 created 时 Strategy 有值。
type PlaybackProxyItemResult struct {
	VideoID  uint   `json:"video_id"`
	Name     string `json:"name"`
	Code     string `json:"code"`
	Strategy string `json:"strategy"`
	Message  string `json:"message"`
}

// PlaybackProxyStatus 是代理任务总览，也是 playback-proxy-state 事件的载荷。
type PlaybackProxyStatus struct {
	Running          bool                      `json:"running"`
	Cancelled        bool                      `json:"cancelled"`
	Completed        bool                      `json:"completed"`
	Queued           int                       `json:"queued"`
	Total            int                       `json:"total"`
	Processed        int                       `json:"processed"`
	Succeeded        int                       `json:"succeeded"`
	Skipped          int                       `json:"skipped"`
	Failed           int                       `json:"failed"`
	CurrentVideoID   uint                      `json:"current_video_id"`
	CurrentVideoName string                    `json:"current_video_name"`
	StartedAt        *time.Time                `json:"started_at" ts_type:"string"`
	UpdatedAt        *time.Time                `json:"updated_at" ts_type:"string"`
	Results          []PlaybackProxyItemResult `json:"results"`
}

// PlaybackProxyService 生成并管理兼容性转封装代理（D-001..D-007）。
//
// 单 worker + FIFO 队列：转封装是吃满 CPU 的 ffmpeg 重活，而且每一项处理前还要
// 抢 MediaWorkSlot（帧哈希与人脸抽帧共用那一个槽），并发跑没有任何好处。
//
// 代理是派生缓存：不入 videos 表、不写用户媒体目录、淘汰只删自己的文件。
type PlaybackProxyService struct {
	dataDir string
	probe   *MediaProbeService

	// runFFmpeg / probeDuration 可注入：测试用 stub 断言参数数组与落位。
	runFFmpeg     func(ctx context.Context, args []string) (string, error)
	probeDuration func(ctx context.Context, path string) (float64, error)
	now           func() time.Time
	// onQueueDrained 是测试注入点：dequeue 取空之后、收尾之前调一次。
	// 存在的唯一理由是能复现"取空与翻 Running 之间入队"那个窗口。
	onQueueDrained func()

	mu       sync.Mutex
	status   PlaybackProxyStatus
	queue    []uint
	inFlight map[uint]struct{}
	cancel   context.CancelFunc
	stopping bool
	worker   sync.WaitGroup
	emitter  func(PlaybackProxyStatus)
	registry *BackgroundTaskRegistry
	notifier DesktopNotifier
	slot     *MediaWorkSlot

	touchMu   sync.Mutex
	touchedAt map[uint]time.Time
}

// NewPlaybackProxyService 构造代理服务。dataDir 是应用数据目录（代理写在它的 proxies/ 下）。
func NewPlaybackProxyService(dataDir string, probe *MediaProbeService) *PlaybackProxyService {
	// 数据目录解析失败（appdata.Resolve 出错）时这里是空串。留着空串而不是退到
	// 相对路径 `proxies/`：后者会以进程工作目录为基准删文件。会删东西的入口
	// 一律靠 dirAvailable() 直接拒绝。
	dataDir = strings.TrimSpace(dataDir)
	if dataDir != "" {
		log.Printf("播放代理目录 = %s", filepath.Join(dataDir, playbackProxyDirName))
	} else {
		log.Printf("播放代理目录不可用：应用数据目录未解析出来，代理功能停用")
	}
	return &PlaybackProxyService{
		dataDir:       dataDir,
		probe:         probe,
		runFFmpeg:     runPlaybackProxyFFmpeg,
		probeDuration: probePlaybackProxyDuration,
		now:           time.Now,
		inFlight:      make(map[uint]struct{}),
		touchedAt:     make(map[uint]time.Time),
	}
}

// SetEventEmitter 注入 playback-proxy-state 事件回调。
func (s *PlaybackProxyService) SetEventEmitter(emitter func(PlaybackProxyStatus)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

// SetBackgroundTaskRegistry 接入后台任务登记表（D-014）。
func (s *PlaybackProxyService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

// SetDesktopNotifier 接入桌面通知（D-013）：一轮跑完发一条。
func (s *PlaybackProxyService) SetDesktopNotifier(notifier DesktopNotifier) {
	s.mu.Lock()
	s.notifier = notifier
	s.mu.Unlock()
}

// SetMediaWorkSlot 注入重媒体处理槽（D-007）。未注入时不做跨任务限流。
func (s *PlaybackProxyService) SetMediaWorkSlot(slot *MediaWorkSlot) {
	s.mu.Lock()
	s.slot = slot
	s.mu.Unlock()
}

// Status 返回当前任务状态快照。
func (s *PlaybackProxyService) Status() PlaybackProxyStatus {
	if s == nil {
		return PlaybackProxyStatus{Results: []PlaybackProxyItemResult{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *PlaybackProxyService) snapshotLocked() PlaybackProxyStatus {
	status := s.status
	status.Queued = len(s.queue)
	status.Results = append([]PlaybackProxyItemResult(nil), s.status.Results...)
	if status.Results == nil {
		status.Results = []PlaybackProxyItemResult{}
	}
	return status
}

func (s *PlaybackProxyService) emitStatus() {
	if s == nil {
		return
	}
	s.mu.Lock()
	status, emitter := s.snapshotLocked(), s.emitter
	s.mu.Unlock()
	if emitter != nil {
		emitter(status)
	}
}

// CreatePlaybackProxy 为一个视频生成代理（详情抽屉与行菜单入口）。
func (s *PlaybackProxyService) CreatePlaybackProxy(parent context.Context, videoID uint) (PlaybackProxyStatus, error) {
	return s.enqueue(parent, []uint{videoID})
}

// BatchCreatePlaybackProxies 批量入队，单 worker FIFO 处理，逐项出结果。
func (s *PlaybackProxyService) BatchCreatePlaybackProxies(parent context.Context, videoIDs []uint) (PlaybackProxyStatus, error) {
	return s.enqueue(parent, videoIDs)
}

// BatchCreatePlaybackProxiesForFilter 为当前筛选命中的视频批量生成代理。
//
// 筛选在这一层解析成 ID 列表，与「当前筛选写出 NFO」同一套路：几万个 ID 没必要
// 搬过 Wails 绑定，前端只要把它已经在用的筛选 DTO 递过来。
func (s *PlaybackProxyService) BatchCreatePlaybackProxiesForFilter(parent context.Context, filter LibraryFilter) (PlaybackProxyStatus, error) {
	if s == nil {
		return PlaybackProxyStatus{}, errors.New("播放代理服务未初始化")
	}
	videoIDs, err := collectPlaybackProxyFilterIDs(filter)
	if err != nil {
		return s.Status(), err
	}
	if len(videoIDs) == 0 {
		return s.Status(), nil
	}
	return s.enqueue(parent, videoIDs)
}

// collectPlaybackProxyFilterIDs 解析筛选命中的视频 ID，顺序稳定按 id 升序。
func collectPlaybackProxyFilterIDs(filter LibraryFilter) ([]uint, error) {
	if database.DB == nil {
		return nil, errors.New("数据库未初始化")
	}
	if libraryFilterNeedsSubtitleSync(filter) {
		if err := syncSubtitleIndexesFromFilesystem(); err != nil {
			return nil, err
		}
	}
	query, err := applyLibraryFilter(database.DB.Model(&models.Video{}).Select("videos.id"), filter, time.Now())
	if err != nil {
		return nil, err
	}
	var videoIDs []uint
	if err := query.Order("videos.id ASC").Pluck("videos.id", &videoIDs).Error; err != nil {
		return nil, fmt.Errorf("读取当前筛选结果失败: %w", err)
	}
	return videoIDs, nil
}

// EnqueueAutoCandidates 是扫描后自动路径的入口（D-006）。
//
// 只挑本次新增视频里内嵌白名单不命中的那些——白名单命中的本来就能直接内嵌播放，
// 给它们做代理纯属浪费。被 LRU 淘汰过的代理不在这里重建：候选集是"本次新增"，
// 淘汰过的视频不会再出现在里面，抖动天然不存在。
func (s *PlaybackProxyService) EnqueueAutoCandidates(parent context.Context, videoIDs []uint) (PlaybackProxyStatus, error) {
	if s == nil {
		return PlaybackProxyStatus{}, errors.New("播放代理服务未初始化")
	}
	candidates, err := filterPlaybackProxyCandidates(videoIDs)
	if err != nil {
		return s.Status(), err
	}
	if len(candidates) == 0 {
		return s.Status(), nil
	}
	return s.enqueue(parent, candidates)
}

// filterPlaybackProxyCandidates 留下内嵌白名单不命中的活跃视频。
// 白名单本身一个字都不改（4.1.5 不变行为）。
func filterPlaybackProxyCandidates(videoIDs []uint) ([]uint, error) {
	if len(videoIDs) == 0 || database.DB == nil {
		return nil, nil
	}
	var videos []models.Video
	if err := database.DB.Select("id", "path").Where("id IN ?", videoIDs).Order("id ASC").Find(&videos).Error; err != nil {
		return nil, fmt.Errorf("读取代理候选视频失败: %w", err)
	}
	candidates := make([]uint, 0, len(videos))
	for _, video := range videos {
		if _, inline := inlinePreviewMIME(video.Path); inline {
			continue
		}
		candidates = append(candidates, video.ID)
	}
	return candidates, nil
}

func (s *PlaybackProxyService) enqueue(parent context.Context, videoIDs []uint) (PlaybackProxyStatus, error) {
	if s == nil {
		return PlaybackProxyStatus{}, errors.New("播放代理服务未初始化")
	}
	if parent == nil {
		parent = context.Background()
	}
	seen := make(map[uint]struct{}, len(videoIDs))
	s.mu.Lock()
	if s.stopping {
		status := s.snapshotLocked()
		s.mu.Unlock()
		return status, ErrPlaybackProxyStopping
	}
	if s.inFlight == nil {
		s.inFlight = make(map[uint]struct{})
	}
	queued := make([]uint, 0, len(videoIDs))
	busy := make([]uint, 0)
	for _, videoID := range videoIDs {
		if videoID == 0 {
			continue
		}
		if _, duplicate := seen[videoID]; duplicate {
			continue
		}
		seen[videoID] = struct{}{}
		if _, running := s.inFlight[videoID]; running {
			busy = append(busy, videoID)
			continue
		}
		s.inFlight[videoID] = struct{}{}
		queued = append(queued, videoID)
	}
	if len(queued) == 0 && len(busy) == 0 {
		status := s.snapshotLocked()
		s.mu.Unlock()
		return status, nil
	}
	startWorker := !s.status.Running
	now := s.now()
	if startWorker {
		s.status = PlaybackProxyStatus{
			Running:   true,
			StartedAt: &now,
			UpdatedAt: &now,
			Results:   []PlaybackProxyItemResult{},
		}
	}
	s.status.Total += len(queued) + len(busy)
	s.status.UpdatedAt = &now
	s.queue = append(s.queue, queued...)
	for _, videoID := range busy {
		// 同一个视频已经在队列里或正在处理：本次合并进去，不是失败。
		s.appendResultLocked(PlaybackProxyItemResult{
			VideoID: videoID,
			Code:    PlaybackProxyCodeInProgress,
			Message: "该视频的代理任务已在进行中",
		})
	}
	var ctx context.Context
	if startWorker {
		ctx, s.cancel = context.WithCancel(parent)
		s.worker.Add(1)
	}
	status, emitter, registry := s.snapshotLocked(), s.emitter, s.registry
	s.mu.Unlock()

	if startWorker {
		registry.Begin(BackgroundTaskProxy)
		go func() {
			defer s.worker.Done()
			defer registry.End(BackgroundTaskProxy)
			s.run(ctx)
		}()
	}
	if emitter != nil {
		emitter(status)
	}
	return status, nil
}

// CancelPlaybackProxyTask 取消当前这一轮：ctx 取消 + 清空队列。
func (s *PlaybackProxyService) CancelPlaybackProxyTask() error {
	if s == nil {
		return ErrPlaybackProxyNotRunning
	}
	s.mu.Lock()
	if !s.status.Running || s.cancel == nil {
		s.mu.Unlock()
		return ErrPlaybackProxyNotRunning
	}
	cancel := s.cancel
	s.status.Cancelled = true
	s.stopping = true
	now := s.now()
	s.status.UpdatedAt = &now
	for _, videoID := range s.queue {
		delete(s.inFlight, videoID)
		// 队列里还没轮到的项同样要有结果，Processed 才追得上 Total。
		s.appendResultLocked(PlaybackProxyItemResult{
			VideoID: videoID, Code: PlaybackProxyCodeCancelled, Message: "已取消",
		})
	}
	s.queue = nil
	status, emitter := s.snapshotLocked(), s.emitter
	s.mu.Unlock()
	cancel()
	if emitter != nil {
		emitter(status)
	}
	return nil
}

// StopAndWait 停掉 worker 并等它退出（关机路径）。
func (s *PlaybackProxyService) StopAndWait() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.worker.Wait()
}

func (s *PlaybackProxyService) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			s.finish(true)
			return
		}
		videoID, ok := s.dequeue()
		if !ok {
			if hook := s.drainHook(); hook != nil {
				hook()
			}
			// 队列空了就收尾——但"空"与"翻 Running"必须在同一把锁里判定：
			// 中间隔着一个窗口的话，正好在这个窗口里入队的项会被 finish 清掉，
			// 而入队方看到 Running=true 又不会另起 worker，那一项就永远没人处理。
			if s.finish(false) {
				return
			}
			continue
		}
		s.processOne(ctx, videoID)
	}
}

func (s *PlaybackProxyService) drainHook() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onQueueDrained
}

func (s *PlaybackProxyService) dequeue() (uint, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return 0, false
	}
	videoID := s.queue[0]
	s.queue = s.queue[1:]
	s.status.CurrentVideoID = videoID
	now := s.now()
	s.status.UpdatedAt = &now
	return videoID, true
}

// recordCancelled 给被取消的项记一条 cancelled 结果。
func (s *PlaybackProxyService) recordCancelled(videoID uint, name string) {
	s.recordResult(PlaybackProxyItemResult{
		VideoID: videoID, Name: name, Code: PlaybackProxyCodeCancelled, Message: "已取消",
	})
}

// finish 收尾。队列在锁内仍然非空时不收尾，返回 false 让 worker 接着跑。
func (s *PlaybackProxyService) finish(cancelled bool) bool {
	s.mu.Lock()
	if !cancelled && len(s.queue) > 0 {
		s.mu.Unlock()
		return false
	}
	s.status.Running = false
	s.status.Cancelled = cancelled
	s.status.Completed = !cancelled
	s.status.CurrentVideoID = 0
	s.status.CurrentVideoName = ""
	now := s.now()
	s.status.UpdatedAt = &now
	cancel := s.cancel
	s.cancel = nil
	s.stopping = false
	s.queue = nil
	s.inFlight = make(map[uint]struct{})
	status, emitter, notifier := s.snapshotLocked(), s.emitter, s.notifier
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if emitter != nil {
		emitter(status)
	}
	// 一轮跑完发一条通知（D-013）：只报成功/失败条数，不带路径也不带失败原因
	// （底层报错常含绝对路径，通知会留在系统通知中心里）。取消不发：那是用户按的。
	if !cancelled && status.Total > 0 {
		notifyDesktop(notifier, "播放代理生成完成",
			fmt.Sprintf("成功 %d 项，失败 %d 项", status.Succeeded, status.Failed))
	}
	return true
}

func (s *PlaybackProxyService) setCurrentName(name string) {
	s.mu.Lock()
	s.status.CurrentVideoName = name
	now := s.now()
	s.status.UpdatedAt = &now
	status, emitter := s.snapshotLocked(), s.emitter
	s.mu.Unlock()
	if emitter != nil {
		emitter(status)
	}
}

func (s *PlaybackProxyService) recordResult(result PlaybackProxyItemResult) {
	s.mu.Lock()
	delete(s.inFlight, result.VideoID)
	s.appendResultLocked(result)
	status, emitter := s.snapshotLocked(), s.emitter
	s.mu.Unlock()
	if emitter != nil {
		emitter(status)
	}
}

func (s *PlaybackProxyService) appendResultLocked(result PlaybackProxyItemResult) {
	s.status.Processed++
	switch result.Code {
	case PlaybackProxyCodeCreated:
		s.status.Succeeded++
	case PlaybackProxyCodeAlreadyExists, PlaybackProxyCodeInProgress, PlaybackProxyCodeCancelled:
		// 取消不是失败：用户自己按的。
		s.status.Skipped++
	default:
		s.status.Failed++
	}
	if len(s.status.Results) < playbackProxyResultLimit {
		s.status.Results = append(s.status.Results, result)
	}
	now := s.now()
	s.status.UpdatedAt = &now
}

// processOne 走完设计 4.1.3 的八步。
func (s *PlaybackProxyService) processOne(ctx context.Context, videoID uint) {
	// 每一项处理前抢重媒体槽（D-007）：转封装、帧哈希、人脸抽帧共用同一个槽位。
	s.mu.Lock()
	slot := s.slot
	s.mu.Unlock()
	if slot != nil {
		if err := slot.Acquire(ctx); err != nil {
			s.recordCancelled(videoID, "")
			return
		}
		defer slot.Release()
	}

	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		code, message := PlaybackProxyCodeFileMissing, "视频记录不存在"
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			code, message = PlaybackProxyCodeEncodeFailed, err.Error()
		}
		s.recordResult(PlaybackProxyItemResult{VideoID: videoID, Code: code, Message: message})
		return
	}
	s.setCurrentName(video.Name)

	info, err := os.Stat(video.Path)
	if err != nil || info.IsDir() {
		if err != nil && os.IsNotExist(err) {
			s.markVideoStale(videoID)
		}
		if err == nil && info.IsDir() {
			s.markVideoStale(videoID)
		}
		s.failItem(video, playbackProxyFingerprint{}, models.PlaybackProxyStrategyRemux,
			PlaybackProxyCodeFileMissing, "源文件不存在或不是文件")
		return
	}
	before := playbackProxyFingerprintOf(info)

	if existing := s.resolveValidProxy(videoID, before, false); existing != nil {
		s.recordResult(PlaybackProxyItemResult{
			VideoID: videoID, Name: video.Name, Code: PlaybackProxyCodeAlreadyExists,
			Strategy: existing.Strategy, Message: "该视频已有可用代理",
		})
		return
	}

	snapshot, ready, err := loadPlaybackProxySnapshot(video)
	if err != nil {
		s.failItem(video, before, "", PlaybackProxyCodeProbeFailed, err.Error())
		return
	}
	if !ready {
		// 快照缺失或不新鲜：先同步探测一次，探不出来就是 probe_failed。
		if s.probe == nil {
			s.failItem(video, before, "", PlaybackProxyCodeProbeFailed, "技术信息探测服务未初始化")
			return
		}
		if err := s.probe.Refresh(ctx, videoID); err != nil {
			if ctx.Err() != nil {
				s.recordCancelled(videoID, video.Name)
				return
			}
			s.failItem(video, before, "", PlaybackProxyCodeProbeFailed, err.Error())
			return
		}
		snapshot, ready, err = loadPlaybackProxySnapshot(video)
		if err != nil || !ready {
			message := "技术信息探测后仍无可用视频流"
			if err != nil {
				message = err.Error()
			}
			s.failItem(video, before, "", PlaybackProxyCodeProbeFailed, message)
			return
		}
	}

	tempPath, err := s.newTempPath(videoID)
	if err != nil {
		s.failItem(video, before, "", PlaybackProxyCodeEncodeFailed, err.Error())
		return
	}
	plan := planPlaybackProxy(snapshot, video.Path, tempPath)
	startedAt := s.now()
	stderr, runErr := s.runFFmpeg(ctx, plan.Args)
	if runErr != nil {
		removePlaybackProxyTemp(tempPath)
		if ctx.Err() != nil {
			s.recordCancelled(videoID, video.Name)
			return
		}
		if playbackProxyIsDiskFull(runErr, stderr) {
			// 磁盘满不触发淘汰：淘汰只在成功写入之后跑（D-005）。
			s.failItem(video, before, plan.Strategy, PlaybackProxyCodeDiskFull, "磁盘空间不足")
			return
		}
		s.failItem(video, before, plan.Strategy, PlaybackProxyCodeEncodeFailed, playbackProxyFailureMessage(runErr, stderr))
		return
	}

	afterInfo, err := os.Stat(video.Path)
	if err != nil {
		removePlaybackProxyTemp(tempPath)
		s.failItem(video, before, plan.Strategy, PlaybackProxyCodeFileMissing, "源文件在编码期间消失")
		return
	}
	if !before.matches(playbackProxyFingerprintOf(afterInfo)) {
		// 步骤 6：源在编码期间被替换，这份产物对不上任何一版源，直接丢弃。
		removePlaybackProxyTemp(tempPath)
		s.failItem(video, before, plan.Strategy, PlaybackProxyCodeSourceChanged, "源文件已变化，未生成")
		return
	}

	if err := s.verifyOutput(ctx, tempPath, video.Duration); err != nil {
		removePlaybackProxyTemp(tempPath)
		if ctx.Err() != nil {
			s.recordCancelled(videoID, video.Name)
			return
		}
		s.failItem(video, before, plan.Strategy, PlaybackProxyCodeEncodeFailed, err.Error())
		return
	}

	outputSize, err := s.publish(videoID, before, tempPath)
	if err != nil {
		removePlaybackProxyTemp(tempPath)
		s.failItem(video, before, plan.Strategy, PlaybackProxyCodeEncodeFailed, err.Error())
		return
	}
	now := s.now()
	row := models.VideoPlaybackProxy{
		VideoID:         videoID,
		SourceSize:      before.size,
		SourceModTimeNS: before.modTimeNS,
		Strategy:        plan.Strategy,
		Status:          models.PlaybackProxyStatusReady,
		OutputSize:      outputSize,
		LastUsedAt:      now,
	}
	if err := upsertProxyRow(row); err != nil {
		// 表写不进去就不能留下无主文件：删掉产物，本项算失败。
		removePlaybackProxyTemp(s.proxyPath(videoID, before))
		s.failItem(video, before, plan.Strategy, PlaybackProxyCodeEncodeFailed, err.Error())
		return
	}
	s.forgetTouch(videoID)
	log.Printf("播放代理已生成 video_id=%d strategy=%s output_size=%d elapsed_ms=%d",
		videoID, plan.Strategy, outputSize, s.now().Sub(startedAt).Milliseconds())
	s.recordResult(PlaybackProxyItemResult{
		VideoID: videoID, Name: video.Name, Code: PlaybackProxyCodeCreated, Strategy: plan.Strategy,
	})
	// 成功写入之后按上限整理一次（D-005）。
	if _, err := s.EnforcePlaybackProxyLimit(); err != nil {
		log.Printf("播放代理 LRU 整理失败 video_id=%d err=%v", videoID, err)
	}
}

// failItem 落一行 failed 记录并记结果。失败行让详情抽屉能显示上次为什么没做出来。
func (s *PlaybackProxyService) failItem(video models.Video, source playbackProxyFingerprint, strategy, code, message string) {
	if strategy == "" {
		strategy = models.PlaybackProxyStrategyRemux
	}
	// 唯一的擦路径收口：写表与回报前端都从这里过一遍（探测失败的报错也带路径）。
	message = scrubPlaybackProxyPaths(message)
	now := s.now()
	row := models.VideoPlaybackProxy{
		VideoID:         video.ID,
		SourceSize:      source.size,
		SourceModTimeNS: source.modTimeNS,
		Strategy:        strategy,
		Status:          models.PlaybackProxyStatusFailed,
		OutputSize:      0,
		LastUsedAt:      now,
		LastError:       message,
	}
	if err := upsertProxyRow(row); err != nil {
		log.Printf("写入播放代理失败记录失败 video_id=%d err=%v", video.ID, err)
	}
	log.Printf("播放代理生成失败 video_id=%d strategy=%s code=%s", video.ID, strategy, code)
	s.recordResult(PlaybackProxyItemResult{
		VideoID: video.ID, Name: video.Name, Code: code, Strategy: strategy, Message: message,
	})
}

func (s *PlaybackProxyService) markVideoStale(videoID uint) {
	if database.DB == nil {
		return
	}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", videoID).Update("is_stale", true).Error; err != nil {
		log.Printf("标记视频失效失败（播放代理） video_id=%d err=%v", videoID, err)
	}
}

// newTempPath 在同卷临时目录里挑一个未被占用的文件名。
// 只造名字不建文件：ffmpeg 自己创建输出，我们不需要先占位。
func (s *PlaybackProxyService) newTempPath(videoID uint) (string, error) {
	dir := s.tempDir()
	if dir == "" {
		return "", ErrPlaybackProxyDirUnavailable
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建播放代理临时目录失败: %w", err)
	}
	name := fmt.Sprintf("%d-%d.mp4", videoID, s.now().UnixNano())
	return filepath.Join(dir, name), nil
}

// verifyOutput 校验产物可被 ffprobe 读出且时长与源相差不超过 2%（流程步骤 7）。
// 源时长未知（老库没探过）时只校验可读性——没有基准就没法比。
func (s *PlaybackProxyService) verifyOutput(ctx context.Context, path string, sourceDuration float64) error {
	duration, err := s.probeDuration(ctx, path)
	if err != nil {
		return fmt.Errorf("产物校验失败: %w", err)
	}
	if duration <= 0 {
		return errors.New("产物校验失败: 时长为零")
	}
	if sourceDuration <= 0 {
		return nil
	}
	if math.Abs(duration-sourceDuration)/sourceDuration > playbackProxyDurationTolerance {
		return fmt.Errorf("产物时长 %.3fs 与源 %.3fs 相差超过 2%%", duration, sourceDuration)
	}
	return nil
}

// publish 把临时产物 rename 到最终路径（同卷，原子），并返回产物大小。
// 同一个视频换指纹之后文件名也变了，所以这里顺手删掉上一份的旧文件。
func (s *PlaybackProxyService) publish(videoID uint, source playbackProxyFingerprint, tempPath string) (int64, error) {
	if !s.dirAvailable() {
		return 0, ErrPlaybackProxyDirUnavailable
	}
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		return 0, fmt.Errorf("创建播放代理目录失败: %w", err)
	}
	target := s.proxyPath(videoID, source)
	if previous, err := loadProxyRow(videoID); err == nil && previous != nil {
		if old := s.proxyPathForRow(*previous); old != target {
			if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
				log.Printf("删除旧播放代理文件失败 video_id=%d err=%v", videoID, err)
			}
		}
	}
	if err := os.Rename(tempPath, target); err != nil {
		return 0, fmt.Errorf("发布播放代理失败: %w", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return 0, fmt.Errorf("读取播放代理产物失败: %w", err)
	}
	return info.Size(), nil
}

func removePlaybackProxyTemp(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("清理播放代理临时文件失败 err=%v", err)
	}
}

func playbackProxyFailureMessage(err error, stderr string) string {
	if stderr == "" {
		return err.Error()
	}
	return fmt.Sprintf("%v: %s", err, stderr)
}
