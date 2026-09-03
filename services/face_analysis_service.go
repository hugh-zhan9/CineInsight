package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 人脸分析（D-017、D-018、D-020）。
//
// 单 worker、可取消、可续跑，与技术信息补全同一套形状。每处理一件媒体之前先拿
// MediaWorkSlot：抽帧是吃满 CPU 的 ffmpeg 活，和转封装、帧哈希抢在一起谁都跑不动。
//
// 这个服务只产出观测与簇，绝不写 video_people / image_people——那要用户在审阅
// 面板里点一下（D-019，P-013 提供）。

const (
	faceAnalysisFailureLimit = 50
	// faceAnalysisScopeAll 等一组是 StartFaceAnalysis 的合法范围。
	FaceAnalysisScopeAll    = "all"
	FaceAnalysisScopeVideos = "videos"
	FaceAnalysisScopeImages = "images"
	// facesDirName 是裁剪图目录（5.3）。
	facesDirName = "faces"
	// faceWorkerRequestTimeout 是单张图的上限：模型是 CPU 跑的，4K 图慢的时候
	// 也就几秒，一分钟还没回来就是卡死了。
	faceWorkerRequestTimeout = 60 * time.Second
	// faceWorkerStartupTimeout 是等 worker 加载模型的上限：冷启动要读 190 MB 权重。
	faceWorkerStartupTimeout = 5 * time.Minute
)

// ErrFaceAnalysisNotRunning 表示当前没有分析任务可取消。
var ErrFaceAnalysisNotRunning = errors.New("face analysis is not running")

// ErrFaceAnalysisScopeInvalid 表示 scope 不在固定集合里。
var ErrFaceAnalysisScopeInvalid = errors.New("face analysis scope invalid")

// FaceAnalysisFailure 是逐项失败记录（不含路径全文，D-020）。
type FaceAnalysisFailure struct {
	MediaKind string `json:"media_kind"`
	MediaID   uint   `json:"media_id"`
	Name      string `json:"name"`
	Error     string `json:"error"`
}

// FaceAnalysisStatus 是分析任务的状态快照（7.2 `GetFaceAnalysisStatus`）。
type FaceAnalysisStatus struct {
	Running   bool `json:"running"`
	Preparing bool `json:"preparing"`
	Cancelled bool `json:"cancelled"`
	Completed bool `json:"completed"`
	// Interrupted 表示 sidecar 中途没了：本轮停下，下次启动对账续跑（4.4.2）。
	Interrupted     bool                  `json:"interrupted"`
	Scope           string                `json:"scope"`
	Total           int                   `json:"total"`
	Processed       int                   `json:"processed"`
	Succeeded       int                   `json:"succeeded"`
	Failed          int                   `json:"failed"`
	FacesDetected   int                   `json:"faces_detected"`
	ClustersCreated int                   `json:"clusters_created"`
	CurrentMedia    string                `json:"current_media"`
	StartedAt       *time.Time            `json:"started_at" ts_type:"string"`
	UpdatedAt       *time.Time            `json:"updated_at" ts_type:"string"`
	Failures        []FaceAnalysisFailure `json:"failures"`
	LastError       string                `json:"last_error"`
	// Gate 是空闲门状态（D-032）：自动路径被挡住时这里说明原因，显式启动恒为零值。
	Gate TaskGateState `json:"gate"`
}

// FaceDataUsage 是隐私分区展示的数据占用（D-020）。
type FaceDataUsage struct {
	ObservationCount int64 `json:"observation_count"`
	ClusterCount     int64 `json:"cluster_count"`
	CandidateCount   int64 `json:"candidate_count"`
	CropFileCount    int64 `json:"crop_file_count"`
	CropBytes        int64 `json:"crop_bytes"`
}

// DetectedFace 是 worker 返回的一张脸。
type DetectedFace struct {
	BBox      faceBBox
	Quality   float64
	Embedding []float32
}

// FaceWorkerSession 是常驻 sidecar 的最小表面。测试注入固定向量的假 worker，
// 真实实现是 faceWorkerProcess。
type FaceWorkerSession interface {
	// Detect 送一张图进去，返回这张图上的全部人脸。
	Detect(ctx context.Context, requestID, imagePath string) ([]DetectedFace, error)
	// Close 结束会话（真实实现会关 stdin 并等进程退出，超时则 kill）。
	Close() error
}

// faceMediaCandidate 是一件待分析的媒体。
type faceMediaCandidate struct {
	Kind        string
	ID          uint
	Name        string
	Path        string
	Duration    float64
	Fingerprint string
}

// faceFrame 是喂给 worker 的一张图。
type faceFrame struct {
	Path    string
	FrameMS *int64
}

// FaceAnalysisService 编排候选、抽帧、sidecar、观测与聚类。
type FaceAnalysisService struct {
	runtime        *FaceRuntime
	imageThumbnail *ImageThumbnailService
	facesDir       string

	mu        sync.Mutex
	status    FaceAnalysisStatus
	cancel    context.CancelFunc
	worker    sync.WaitGroup
	emitter   func(FaceAnalysisStatus)
	registry  *BackgroundTaskRegistry
	notifier  DesktopNotifier
	slot      *MediaWorkSlot
	pauseHook TaskPauseHook

	// 测试接缝。
	openSession   func(ctx context.Context) (FaceWorkerSession, error)
	extractFrames func(ctx context.Context, candidate faceMediaCandidate, dir string) ([]faceFrame, []string)
	resolveImage  func(ctx context.Context, imageID uint) (string, error)
	now           func() time.Time
}

// NewFaceAnalysisService 组装分析服务。dataDir 是应用数据根目录。
func NewFaceAnalysisService(dataDir string, runtime *FaceRuntime, thumbnails *ImageThumbnailService) *FaceAnalysisService {
	service := &FaceAnalysisService{
		runtime:        runtime,
		imageThumbnail: thumbnails,
		// 数据目录没解析出来时 facesDir 留空，不拼成相对路径 "faces"——
		// 那样 RemoveAll/MkdirAll 动的是进程 cwd 下的同名目录（ErrFaceDataDirUnavailable）。
		facesDir: faceDataDirFor(dataDir),
		now:      time.Now,
	}
	service.openSession = func(ctx context.Context) (FaceWorkerSession, error) {
		return startFaceWorkerProcess(ctx, runtime)
	}
	service.extractFrames = extractFaceFrames
	service.resolveImage = func(ctx context.Context, imageID uint) (string, error) {
		if thumbnails == nil {
			return "", errors.New("图片解码服务未初始化")
		}
		media, err := thumbnails.ResolveImageView(ctx, imageID)
		if err != nil {
			return "", err
		}
		return media.Path, nil
	}
	return service
}

// faceDataDirFor 返回裁剪图目录；dataDir 为空时返回空串（见 ErrFaceDataDirUnavailable）。
func faceDataDirFor(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	return filepath.Join(dataDir, facesDirName)
}

// dataDirAvailable 报告裁剪图目录是否落在真实的应用数据目录里。
func (s *FaceAnalysisService) dataDirAvailable() bool {
	return s != nil && s.facesDir != ""
}

// SetEventEmitter 注入 face-analysis-state 事件发射器。
func (s *FaceAnalysisService) SetEventEmitter(emitter func(FaceAnalysisStatus)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

// SetBackgroundTaskRegistry 接入后台任务登记表（D-014）。
func (s *FaceAnalysisService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

// SetDesktopNotifier 接入桌面通知（D-013）。
func (s *FaceAnalysisService) SetDesktopNotifier(notifier DesktopNotifier) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.notifier = notifier
	s.mu.Unlock()
}

// SetMediaWorkSlot 接入重媒体任务共享槽（D-007）。
func (s *FaceAnalysisService) SetMediaWorkSlot(slot *MediaWorkSlot) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.slot = slot
	s.mu.Unlock()
}

// FacesDir 是裁剪图目录，路由与用量统计都读它。
func (s *FaceAnalysisService) FacesDir() string {
	if s == nil {
		return ""
	}
	return s.facesDir
}

func normalizeFaceAnalysisScope(scope string) (string, error) {
	switch scope {
	case "", FaceAnalysisScopeAll:
		return FaceAnalysisScopeAll, nil
	case FaceAnalysisScopeVideos:
		return FaceAnalysisScopeVideos, nil
	case FaceAnalysisScopeImages:
		return FaceAnalysisScopeImages, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrFaceAnalysisScopeInvalid, scope)
	}
}

// Start 是显式启动路径：同一把锁下摘掉本轮的项间检查点，
// 用户主动点的任务永远不被空闲门挡住（D-030）。
func (s *FaceAnalysisService) Start(parent context.Context, scope string) (FaceAnalysisStatus, error) {
	return s.start(parent, scope, nil)
}

// StartWithPauseHook 是自动路径专用：装钩子与翻 Running 在同一把锁里完成（D-032）。
func (s *FaceAnalysisService) StartWithPauseHook(parent context.Context, scope string, hook TaskPauseHook) (FaceAnalysisStatus, error) {
	return s.start(parent, scope, hook)
}

func (s *FaceAnalysisService) start(parent context.Context, scope string, hook TaskPauseHook) (FaceAnalysisStatus, error) {
	if s == nil {
		return FaceAnalysisStatus{}, errors.New("face analysis service is not initialized")
	}
	normalized, err := normalizeFaceAnalysisScope(scope)
	if err != nil {
		return FaceAnalysisStatus{}, err
	}
	if !s.dataDirAvailable() {
		// 分析会写裁剪图、清临时残件：目录不可用就一步都不迈出去。
		return s.Status(), ErrFaceDataDirUnavailable
	}
	if s.runtime != nil {
		if status := s.runtime.Status(); status.State != FaceRuntimeStateAvailable {
			return s.Status(), fmt.Errorf("%w：%s", ErrFaceRuntimeUnavailable, status.Reason)
		}
	}
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	releasing := clearReplacedPauseHook(&s.pauseHook, hook)
	if s.status.Running {
		status := s.snapshotLocked()
		s.mu.Unlock()
		releaseTaskPauseHook(releasing)
		return status, nil
	}
	s.pauseHook = hook
	now := s.now()
	s.status = FaceAnalysisStatus{
		Running:   true,
		Preparing: true,
		Scope:     normalized,
		StartedAt: &now,
		UpdatedAt: &now,
		Failures:  []FaceAnalysisFailure{},
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	status := s.snapshotLocked()
	emitter, registry := s.emitter, s.registry
	s.worker.Add(1)
	s.mu.Unlock()
	releaseTaskPauseHook(releasing)
	registry.Begin(BackgroundTaskFace)
	emitFaceAnalysisStatus(emitter, status)
	go func() {
		defer s.worker.Done()
		defer registry.End(BackgroundTaskFace)
		s.run(ctx, normalized)
	}()
	return status, nil
}

// Cancel 取消当前分析（7.2 `CancelFaceAnalysis`）。
func (s *FaceAnalysisService) Cancel() error {
	if s == nil {
		return ErrFaceAnalysisNotRunning
	}
	s.mu.Lock()
	if !s.status.Running || s.cancel == nil {
		s.mu.Unlock()
		return ErrFaceAnalysisNotRunning
	}
	cancel := s.cancel
	s.status.Cancelled = true
	now := s.now()
	s.status.UpdatedAt = &now
	status, emitter := s.snapshotLocked(), s.emitter
	s.mu.Unlock()
	cancel()
	emitFaceAnalysisStatus(emitter, status)
	return nil
}

// StopAndWait 在应用退出时等 worker 收尾。
func (s *FaceAnalysisService) StopAndWait() {
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

// Status 返回状态快照（7.2 `GetFaceAnalysisStatus`）。
func (s *FaceAnalysisService) Status() FaceAnalysisStatus {
	if s == nil {
		return FaceAnalysisStatus{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *FaceAnalysisService) snapshotLocked() FaceAnalysisStatus {
	status := s.status
	status.Failures = append([]FaceAnalysisFailure(nil), s.status.Failures...)
	return status
}

func emitFaceAnalysisStatus(emitter func(FaceAnalysisStatus), status FaceAnalysisStatus) {
	if emitter != nil {
		emitter(status)
	}
}

func (s *FaceAnalysisService) update(mutate func(*FaceAnalysisStatus)) {
	s.mu.Lock()
	mutate(&s.status)
	now := s.now()
	s.status.UpdatedAt = &now
	status, emitter := s.snapshotLocked(), s.emitter
	s.mu.Unlock()
	emitFaceAnalysisStatus(emitter, status)
}

func (s *FaceAnalysisService) setGateState(state TaskGateState) {
	s.update(func(status *FaceAnalysisStatus) { status.Gate = state })
}

// waitForPauseHook 是项间检查点：自动路径在处理下一件媒体之前等空闲（D-032）。
func (s *FaceAnalysisService) waitForPauseHook(ctx context.Context) error {
	s.mu.Lock()
	hook := s.pauseHook
	s.mu.Unlock()
	if hook == nil {
		return nil
	}
	// 钩子在服务锁之外调用：它会阻塞很久，持锁等待会连 Status/Cancel 一起冻住。
	return hook.Wait(ctx, s.setGateState)
}

// scrubFacePaths 把媒体路径与临时目录从错误文案里抹掉（D-020：日志不记路径全文）。
//
// ffmpeg 与 sidecar 的报错习惯性把输入路径原样吐出来，而这些文案会进日志、
// 也会进状态里的逐项失败。媒体种类与 id 已经足够定位是哪一件。
func scrubFacePaths(message string, paths ...string) string {
	ordered := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		ordered = append(ordered, path)
	}
	// 长的先替：临时目录是帧路径的前缀，先替短的会把长的切成两半
	// （"<路径已省略>/frame-0.jpg"），文件名照样漏出去。
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, path := range ordered {
		message = strings.ReplaceAll(message, path, "<路径已省略>")
	}
	return message
}

func (s *FaceAnalysisService) recordFailure(candidate faceMediaCandidate, err error) {
	message := scrubFacePaths(err.Error(), candidate.Path)
	s.update(func(status *FaceAnalysisStatus) {
		status.Processed++
		status.Failed++
		if len(status.Failures) < faceAnalysisFailureLimit {
			status.Failures = append(status.Failures, FaceAnalysisFailure{
				MediaKind: candidate.Kind,
				MediaID:   candidate.ID,
				Name:      candidate.Name,
				Error:     truncateLogSnippet(message, 200),
			})
		}
	})
}

func (s *FaceAnalysisService) finish(cancelled bool, interrupted bool, lastError string) {
	s.mu.Lock()
	s.status.Running = false
	s.status.Preparing = false
	s.status.Cancelled = cancelled
	s.status.Interrupted = interrupted
	// 整轮失败（sidecar 起不来、候选查不出来）不算完成：界面与通知都得说失败。
	s.status.Completed = !cancelled && !interrupted && lastError == ""
	s.status.CurrentMedia = ""
	s.status.Gate = TaskGateState{}
	if lastError != "" {
		s.status.LastError = truncateLogSnippet(lastError, 300)
	}
	now := s.now()
	s.status.UpdatedAt = &now
	cancel := s.cancel
	s.cancel = nil
	status, emitter, notifier := s.snapshotLocked(), s.emitter, s.notifier
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	emitFaceAnalysisStatus(emitter, status)
	notifyFaceAnalysisTerminal(notifier, status)
}

// notifyFaceAnalysisTerminal 是终态通知（D-013）。取消不发：那是用户自己按的。
// 文案只带计数，不带任何路径——通知会留在系统通知中心（D-020）。
func notifyFaceAnalysisTerminal(notifier DesktopNotifier, status FaceAnalysisStatus) {
	if notifier == nil || status.Cancelled {
		return
	}
	if status.Interrupted {
		notifyDesktop(notifier, "人脸分析已中断", fmt.Sprintf("已处理 %d 项，可重新启动继续", status.Processed))
		return
	}
	if status.LastError != "" {
		// 整轮没起来（sidecar 装不起来、候选查不出来）：这不是"完成"，
		// 通知里也不能说完成。原因不带进文案——它可能含绝对路径（D-020）。
		notifyDesktop(notifier, "人脸分析失败", "任务未能开始，请在设置页查看原因")
		return
	}
	if status.Failed > 0 {
		notifyDesktop(notifier, "人脸分析失败", fmt.Sprintf("%d 项失败，已成功 %d 项", status.Failed, status.Succeeded))
		return
	}
	notifyDesktop(notifier, "人脸分析完成", fmt.Sprintf("已分析 %d 项，检出 %d 张人脸", status.Succeeded, status.FacesDetected))
}

func (s *FaceAnalysisService) run(ctx context.Context, scope string) {
	// 先对账：媒体永久删除后留下的孤儿观测在这里清掉（4.4.4 的"观测级联删"，
	// media_id 是多态引用，数据库层做不到级联）。
	if removed, err := s.pruneOrphanFaceData(ctx); err != nil {
		log.Printf("[Face] 孤儿观测对账失败 err=%v", err)
	} else if removed > 0 {
		log.Printf("[Face] 清理孤儿观测 count=%d", removed)
	}

	// 上一轮被 kill 时留下的裁剪图临时残件在这里清掉。
	if swept := s.sweepFaceCropTemps(); swept > 0 {
		log.Printf("[Face] 清理裁剪图临时残件 count=%d", swept)
	}

	candidates, err := s.loadCandidates(ctx, scope)
	if err != nil {
		if ctx.Err() != nil {
			s.finish(true, false, "")
			return
		}
		s.finish(false, false, err.Error())
		return
	}
	s.update(func(status *FaceAnalysisStatus) {
		status.Preparing = false
		status.Total = len(candidates)
	})
	if len(candidates) == 0 {
		s.finish(ctx.Err() != nil, false, "")
		return
	}

	session, err := s.openSession(ctx)
	if err != nil {
		if ctx.Err() != nil {
			s.finish(true, false, "")
			return
		}
		s.finish(false, false, err.Error())
		return
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			log.Printf("[Face] sidecar 关闭异常 err=%v", closeErr)
		}
	}()

	clusters, err := loadFaceClusterCentroids(ctx)
	if err != nil {
		s.finish(ctx.Err() != nil, false, err.Error())
		return
	}

	interrupted := false
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		if err := s.waitForPauseHook(ctx); err != nil {
			break
		}
		s.update(func(status *FaceAnalysisStatus) { status.CurrentMedia = candidate.Name })
		fatal, err := s.processCandidate(ctx, session, candidate, &clusters)
		switch {
		case err == nil:
			s.update(func(status *FaceAnalysisStatus) {
				status.Processed++
				status.Succeeded++
			})
		case ctx.Err() != nil:
			// 取消不算失败。
		case fatal:
			// sidecar 没了：本轮到此为止，状态标 interrupted，下次启动续跑。
			interrupted = true
			s.recordFailure(candidate, err)
		default:
			s.recordFailure(candidate, err)
		}
		if interrupted {
			break
		}
	}

	if ctx.Err() == nil && !interrupted {
		if err := s.refreshPersonCandidates(ctx); err != nil {
			log.Printf("[Face] 人物候选生成失败 err=%v", err)
		}
	}
	s.finish(ctx.Err() != nil, interrupted, "")
}

// loadCandidates 组装本轮要处理的媒体：无观测或源指纹变化者（4.4.3）。
// 人物头像种子无论 scope 都要取——它不产生候选面板条目，只提供"这个簇像谁"的依据。
func (s *FaceAnalysisService) loadCandidates(ctx context.Context, scope string) ([]faceMediaCandidate, error) {
	analyzed, err := loadAnalyzedFingerprints(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]faceMediaCandidate, 0)
	appendCandidate := func(candidate faceMediaCandidate) {
		if candidate.Path == "" {
			return
		}
		fingerprint, err := mediaProbeStat(candidate.Path)
		if err != nil {
			// 文件不在了：不入队也不记失败，扫描会把它标 stale。
			return
		}
		candidate.Fingerprint = formatFaceFingerprint(fingerprint)
		if analyzed[faceAnalyzedKey{Kind: candidate.Kind, ID: candidate.ID}] == candidate.Fingerprint {
			return
		}
		candidates = append(candidates, candidate)
	}

	if scope == FaceAnalysisScopeAll || scope == FaceAnalysisScopeVideos {
		var videos []models.Video
		if err := database.DB.WithContext(ctx).Model(&models.Video{}).
			Select("id", "name", "path", "duration").
			Where("is_stale = ?", false).Order("id ASC").Find(&videos).Error; err != nil {
			return nil, fmt.Errorf("load face analysis videos: %w", err)
		}
		for _, video := range videos {
			appendCandidate(faceMediaCandidate{
				Kind: models.FaceMediaKindVideo, ID: video.ID, Name: video.Name,
				Path: video.Path, Duration: video.Duration,
			})
		}
	}
	if scope == FaceAnalysisScopeAll || scope == FaceAnalysisScopeImages {
		var images []models.Image
		if err := database.DB.WithContext(ctx).Model(&models.Image{}).
			Select("id", "name", "path").
			Where("is_stale = ?", false).Order("id ASC").Find(&images).Error; err != nil {
			return nil, fmt.Errorf("load face analysis images: %w", err)
		}
		for _, image := range images {
			appendCandidate(faceMediaCandidate{
				Kind: models.FaceMediaKindImage, ID: image.ID, Name: image.Name, Path: image.Path,
			})
		}
	}

	var people []models.Person
	if err := database.DB.WithContext(ctx).Model(&models.Person{}).
		Select("id", "display_name", "avatar_path").
		Where("avatar_path <> ''").Order("id ASC").Find(&people).Error; err != nil {
		return nil, fmt.Errorf("load face seed people: %w", err)
	}
	for _, person := range people {
		appendCandidate(faceMediaCandidate{
			Kind: models.FaceMediaKindPersonAvatar, ID: person.ID,
			Name: person.DisplayName, Path: person.AvatarPath,
		})
	}
	return candidates, nil
}

type faceAnalyzedKey struct {
	Kind string
	ID   uint
}

// loadAnalyzedFingerprints 取每件媒体已分析过的指纹（同一件只可能有一个有效指纹，
// 旧指纹的观测在写入新指纹时就删掉了）。
func loadAnalyzedFingerprints(ctx context.Context) (map[faceAnalyzedKey]string, error) {
	type row struct {
		MediaKind         string
		MediaID           uint
		SourceFingerprint string
	}
	var rows []row
	if err := database.DB.WithContext(ctx).Model(&models.FaceObservation{}).
		Select("media_kind", "media_id", "source_fingerprint").
		Group("media_kind, media_id, source_fingerprint").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load face observation fingerprints: %w", err)
	}
	analyzed := make(map[faceAnalyzedKey]string, len(rows))
	for _, item := range rows {
		analyzed[faceAnalyzedKey{Kind: item.MediaKind, ID: item.MediaID}] = item.SourceFingerprint
	}
	return analyzed, nil
}

func formatFaceFingerprint(fingerprint mediaProbeFingerprint) string {
	return strconv.FormatInt(fingerprint.size, 10) + "-" + strconv.FormatInt(fingerprint.modTimeNS, 10)
}

// processCandidate 处理一件媒体。返回的 fatal 为真表示 sidecar 已经不可用，
// 继续喂图没有意义。
func (s *FaceAnalysisService) processCandidate(ctx context.Context, session FaceWorkerSession, candidate faceMediaCandidate, clusters *[]faceClusterCentroid) (fatal bool, err error) {
	s.mu.Lock()
	slot := s.slot
	s.mu.Unlock()
	if slot != nil {
		if err := slot.Acquire(ctx); err != nil {
			return false, err
		}
		defer slot.Release()
	}

	// 这一件媒体处理过程中会出现的全部路径：源文件、临时抽帧目录，以及解码矩阵
	// 给出的查看缓存（RAW/HEIC 会落在 image-thumbnails 下）。ffmpeg 与 sidecar
	// 的报错习惯把输入路径原样吐出来，出口只有一个，就在这里统一擦掉（D-020）。
	scrubTargets := []string{candidate.Path}
	defer func() {
		if err != nil {
			err = errors.New(scrubFacePaths(err.Error(), scrubTargets...))
		}
	}()

	workDir, err := os.MkdirTemp("", "cineinsight-face-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(workDir)
	scrubTargets = append(scrubTargets, workDir)

	frames, warnings := s.collectFrames(ctx, candidate, workDir)
	for _, frame := range frames {
		scrubTargets = append(scrubTargets, frame.Path)
	}
	for _, warning := range warnings {
		log.Printf("[Face] 取图告警 kind=%s id=%d warning=%s", candidate.Kind, candidate.ID,
			scrubFacePaths(warning, scrubTargets...))
	}
	if len(frames) == 0 {
		return false, errors.New("没有可分析的图像")
	}

	detections := make([]faceDetectionRecord, 0, len(frames))
	analyzed := 0
	var lastFrameErr error
	for index, frame := range frames {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		requestID := fmt.Sprintf("%s-%d-%d", candidate.Kind, candidate.ID, index)
		faces, err := session.Detect(ctx, requestID, frame.Path)
		if err != nil {
			if errors.Is(err, errFaceWorkerGone) {
				return true, err
			}
			// 单张失败只记一条告警：一整部片子不该因为一帧解码失败而算失败。
			log.Printf("[Face] 单张检测失败 kind=%s id=%d err=%s", candidate.Kind, candidate.ID,
				scrubFacePaths(err.Error(), scrubTargets...))
			lastFrameErr = err
			continue
		}
		analyzed++
		for _, face := range faces {
			detections = append(detections, faceDetectionRecord{
				Frame: frame,
				Face:  face,
			})
		}
	}
	if analyzed == 0 {
		// 一张都没成功看过：这不是"没有脸"，是没看成。写 no_face 标记会把这件媒体
		// 永久排除在候选之外，只能按失败记账、留给下一轮重试。
		if lastFrameErr != nil {
			return false, fmt.Errorf("全部图像检测失败: %w", lastFrameErr)
		}
		return false, errors.New("没有成功分析的图像")
	}

	written, droppedClusters, err := s.persistObservations(ctx, candidate, detections)
	if err != nil {
		return false, err
	}
	// 刚被删掉的簇不能留在内存快照里：下一条观测万一匹配上它，
	// attachToFaceCluster 只会撞到一个已经不存在的行。
	if len(droppedClusters) > 0 {
		*clusters = removeFaceClusterCentroids(*clusters, droppedClusters)
	}
	if len(written) > 0 {
		s.update(func(status *FaceAnalysisStatus) { status.FacesDetected += len(written) })
	}
	// 头像种子不参与聚类：它是"这个人长什么样"的参照，不是一次媒体观测。
	if candidate.Kind != models.FaceMediaKindPersonAvatar {
		created, err := s.clusterObservations(ctx, written, clusters)
		if err != nil {
			return false, err
		}
		if created > 0 {
			s.update(func(status *FaceAnalysisStatus) { status.ClustersCreated += created })
		}
	}
	return false, nil
}

// faceFrameSentinel 返回"不来自视频帧"的哨兵指针（图片、人物头像与 no_face 标记）。
func faceFrameSentinel() *int64 {
	sentinel := models.FaceFrameMSNone
	return &sentinel
}

// faceObservationKey 是一件媒体内部区分观测的键：帧位置 + bbox 量化哈希，
// 与数据库唯一键的后两列同口径。
func faceObservationKey(frameMS *int64, bboxHash string) string {
	frame := models.FaceFrameMSNone
	if frameMS != nil {
		frame = *frameMS
	}
	return strconv.FormatInt(frame, 10) + ":" + bboxHash
}

type faceDetectionRecord struct {
	Frame faceFrame
	Face  DetectedFace
}

func (s *FaceAnalysisService) collectFrames(ctx context.Context, candidate faceMediaCandidate, workDir string) ([]faceFrame, []string) {
	switch candidate.Kind {
	case models.FaceMediaKindVideo:
		return s.extractFrames(ctx, candidate, workDir)
	case models.FaceMediaKindImage:
		path, err := s.resolveImage(ctx, candidate.ID)
		if err != nil {
			return nil, []string{fmt.Sprintf("图片解码失败: %v", err)}
		}
		return []faceFrame{{Path: path, FrameMS: faceFrameSentinel()}}, nil
	default:
		// 人物头像是托管文件，直接读。
		return []faceFrame{{Path: candidate.Path, FrameMS: faceFrameSentinel()}}, nil
	}
}

// persistObservations 写入一件媒体的观测：先删这件媒体的旧观测（指纹已变），
// 再插入本轮的。同一事务里完成，重跑同一份源不会留下半新半旧的组合。
func (s *FaceAnalysisService) persistObservations(ctx context.Context, candidate faceMediaCandidate, detections []faceDetectionRecord) ([]models.FaceObservation, []uint, error) {
	rows := make([]models.FaceObservation, 0, len(detections)+1)
	seen := make(map[string]struct{}, len(detections))
	for _, detection := range detections {
		normalized, ok := normalizeFaceEmbedding(detection.Face.Embedding)
		if !ok {
			continue
		}
		box := detection.Face.BBox.normalized()
		hash := box.Hash()
		key := faceObservationKey(detection.Frame.FrameMS, hash)
		if _, exists := seen[key]; exists {
			// 同一帧同一个位置检出两次（量化后撞在一格里）：唯一键只容得下一条。
			continue
		}
		seen[key] = struct{}{}
		rows = append(rows, models.FaceObservation{
			MediaKind:         candidate.Kind,
			MediaID:           candidate.ID,
			SourceFingerprint: candidate.Fingerprint,
			FrameMS:           detection.Frame.FrameMS,
			BBox:              box.String(),
			BBoxHash:          hash,
			Quality:           detection.Face.Quality,
			Embedding:         encodeFaceEmbedding(normalized),
			AppendStatus:      models.FaceAppendStatusNone,
		})
	}
	if len(rows) == 0 {
		// 没检出脸也要留痕，否则每轮都要重新解码一遍（4.4.4）。
		rows = append(rows, models.FaceObservation{
			MediaKind:         candidate.Kind,
			MediaID:           candidate.ID,
			SourceFingerprint: candidate.Fingerprint,
			// 标记观测同样带哨兵帧位置：唯一键里不能出现 NULL（models.FaceFrameMSNone）。
			FrameMS:      faceFrameSentinel(),
			BBoxHash:     models.FaceNoFaceBBoxHash,
			AppendStatus: models.FaceAppendStatusNone,
		})
	}

	var stale []models.FaceObservation
	var droppedClusters []uint
	err := database.Transaction(func(tx *gorm.DB) error {
		droppedClusters = nil
		if err := tx.WithContext(ctx).Where("media_kind = ? AND media_id = ?", candidate.Kind, candidate.ID).
			Find(&stale).Error; err != nil {
			return err
		}
		if len(stale) > 0 {
			if err := tx.WithContext(ctx).Where("media_kind = ? AND media_id = ?", candidate.Kind, candidate.ID).
				Delete(&models.FaceObservation{}).Error; err != nil {
				return err
			}
			// 删了观测就必须回头修簇：observation_count 只加不减的话，
			// 重算过几轮的库里会攒出一堆"看起来有 5 条观测、其实一条都没有"的
			// 幽灵簇，候选门（≥3）与质心加权都跟着失真。
			dropped, err := recomputeFaceClustersTx(ctx, tx, faceClusterIDsOf(stale))
			if err != nil {
				return err
			}
			droppedClusters = dropped
		}
		return tx.WithContext(ctx).Create(&rows).Error
	})
	if err != nil {
		return nil, nil, err
	}
	s.removeCropFiles(stale)

	// 裁剪图在观测拿到 id 之后才能写：文件名就是 observation id（D-020）。
	written := make([]models.FaceObservation, 0, len(rows))
	frameByKey := make(map[string]faceFrame, len(detections))
	for _, detection := range detections {
		key := faceObservationKey(detection.Frame.FrameMS, detection.Face.BBox.normalized().Hash())
		if _, exists := frameByKey[key]; !exists {
			frameByKey[key] = detection.Frame
		}
	}
	for index := range rows {
		row := rows[index]
		if row.BBoxHash == models.FaceNoFaceBBoxHash {
			continue
		}
		frame, ok := frameByKey[faceObservationKey(row.FrameMS, row.BBoxHash)]
		if ok {
			if err := s.writeCrop(&row, candidate, frame); err != nil {
				log.Printf("[Face] 裁剪图写入失败 kind=%s id=%d err=%s", candidate.Kind, candidate.ID,
					scrubFacePaths(err.Error(), candidate.Path, frame.Path))
			}
		}
		written = append(written, row)
	}
	return written, droppedClusters, nil
}

func (s *FaceAnalysisService) writeCrop(observation *models.FaceObservation, candidate faceMediaCandidate, frame faceFrame) error {
	box, err := parseFaceBBox(observation.BBox)
	if err != nil {
		return err
	}
	orientation := 0
	if candidate.Kind != models.FaceMediaKindVideo {
		// 视频帧是 ffmpeg 现抽的 JPEG，不带 EXIF；图片与头像要按 EXIF 转正，
		// 否则 bbox 与像素对不上。
		orientation = faceCropOrientation(frame.Path)
	}
	if !s.dataDirAvailable() {
		return ErrFaceDataDirUnavailable
	}
	name := strconv.FormatUint(uint64(observation.ID), 10) + ".jpg"
	if err := cropFaceThumbnail(frame.Path, orientation, box, filepath.Join(s.facesDir, name)); err != nil {
		return err
	}
	if err := database.DB.Model(&models.FaceObservation{}).Where("id = ?", observation.ID).
		Update("crop_path", name).Error; err != nil {
		return err
	}
	observation.CropPath = name
	return nil
}

// removeCropFiles 删掉观测对应的裁剪图。只认 faces 目录下的纯文件名，
// 库里被写进别的东西也删不到目录外去。
func (s *FaceAnalysisService) removeCropFiles(observations []models.FaceObservation) {
	for _, observation := range observations {
		path, ok := s.cropFilePath(observation.CropPath)
		if !ok {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("[Face] 裁剪图删除失败 observation=%d err=%v", observation.ID, err)
		}
	}
}

// cropFilePath 把库里的 crop_path 解析成绝对路径，并保证它落在 faces 目录内
// （D-020：受控资源只读这一个目录）。
func (s *FaceAnalysisService) cropFilePath(cropPath string) (string, bool) {
	if !s.dataDirAvailable() || cropPath == "" {
		return "", false
	}
	name := filepath.Base(filepath.Clean(cropPath))
	if name == "." || name == string(filepath.Separator) || name == ".." {
		return "", false
	}
	full := filepath.Join(s.facesDir, name)
	relative, err := filepath.Rel(s.facesDir, full)
	if err != nil || relative == ".." || filepath.IsAbs(relative) ||
		len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return "", false
	}
	return full, true
}

// clusterObservations 增量聚类（D-017）：与簇代表点积 ≥ 阈值就归入最相似的簇，
// 否则新建一个未命名簇。归入已命名簇的新观测标 pending，等用户确认追加（D-019）——
// 这里绝不写 video_people / image_people。
func (s *FaceAnalysisService) clusterObservations(ctx context.Context, observations []models.FaceObservation, clusters *[]faceClusterCentroid) (int, error) {
	created := 0
	// 质量高的先聚：让簇代表从一开始就是清楚的那张脸。
	ordered := append([]models.FaceObservation(nil), observations...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Quality > ordered[j].Quality })

	for _, observation := range ordered {
		if ctx.Err() != nil {
			return created, ctx.Err()
		}
		vector, err := decodeFaceEmbedding(observation.Embedding)
		if err != nil {
			continue
		}
		normalized, ok := normalizeFaceEmbedding(vector)
		if !ok {
			continue
		}
		match, _ := bestFaceCluster(normalized, *clusters, faceClusterMergeThreshold)
		if match == nil {
			clusterID, err := s.createFaceCluster(ctx, observation, normalized)
			if err != nil {
				return created, err
			}
			*clusters = append(*clusters, faceClusterCentroid{ClusterID: clusterID, Vector: normalized, Count: 1})
			created++
			continue
		}
		updated := updateFaceCentroid(match.Vector, match.Count, normalized)
		if err := s.attachToFaceCluster(ctx, match.ClusterID, observation, updated); err != nil {
			return created, err
		}
		match.Vector = updated
		match.Count++
	}
	return created, nil
}

func (s *FaceAnalysisService) createFaceCluster(ctx context.Context, observation models.FaceObservation, centroid []float32) (uint, error) {
	cluster := models.FaceCluster{
		Status:                      models.FaceClusterStatusUnnamed,
		Centroid:                    encodeFaceEmbedding(centroid),
		ObservationCount:            1,
		RepresentativeObservationID: &observation.ID,
	}
	err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Create(&cluster).Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).Model(&models.FaceObservation{}).Where("id = ?", observation.ID).
			Update("cluster_id", cluster.ID).Error
	})
	if err != nil {
		return 0, err
	}
	return cluster.ID, nil
}

func (s *FaceAnalysisService) attachToFaceCluster(ctx context.Context, clusterID uint, observation models.FaceObservation, centroid []float32) error {
	return database.Transaction(func(tx *gorm.DB) error {
		var cluster models.FaceCluster
		if err := tx.WithContext(ctx).First(&cluster, clusterID).Error; err != nil {
			return err
		}
		updates := map[string]interface{}{
			"centroid":          encodeFaceEmbedding(centroid),
			"observation_count": cluster.ObservationCount + 1,
		}
		// 簇代表取质量最高的观测：面板上那张脸得让人认得出来。
		if cluster.RepresentativeObservationID == nil {
			updates["representative_observation_id"] = observation.ID
		} else {
			var representative models.FaceObservation
			if err := tx.WithContext(ctx).Select("id", "quality").
				First(&representative, *cluster.RepresentativeObservationID).Error; err == nil {
				if observation.Quality > representative.Quality {
					updates["representative_observation_id"] = observation.ID
				}
			} else if errors.Is(err, gorm.ErrRecordNotFound) {
				updates["representative_observation_id"] = observation.ID
			} else {
				return err
			}
		}
		if err := tx.WithContext(ctx).Model(&models.FaceCluster{}).Where("id = ?", clusterID).
			Updates(updates).Error; err != nil {
			return err
		}
		observationUpdates := map[string]interface{}{"cluster_id": clusterID}
		if cluster.Status == models.FaceClusterStatusNamed {
			// 已命名的簇吸收到新观测：只标 pending，等用户确认追加（D-019）。
			observationUpdates["append_status"] = models.FaceAppendStatusPending
		}
		return tx.WithContext(ctx).Model(&models.FaceObservation{}).Where("id = ?", observation.ID).
			Updates(observationUpdates).Error
	})
}

func loadFaceClusterCentroids(ctx context.Context) ([]faceClusterCentroid, error) {
	// 三种状态的簇都参与比对，被忽略的也算：用户忽略过的那张脸再出现时应当
	// 继续落进同一个被忽略的簇，而不是另起一个未命名簇又冒到审阅面板上
	// （requirements ID-02「否认后不再重复建议」）。
	var clusters []models.FaceCluster
	if err := database.DB.WithContext(ctx).Model(&models.FaceCluster{}).
		Select("id", "centroid", "observation_count", "status").
		Order("id ASC").Find(&clusters).Error; err != nil {
		return nil, fmt.Errorf("load face clusters: %w", err)
	}
	centroids := make([]faceClusterCentroid, 0, len(clusters))
	for _, cluster := range clusters {
		vector, err := decodeFaceEmbedding(cluster.Centroid)
		if err != nil {
			continue
		}
		normalized, ok := normalizeFaceEmbedding(vector)
		if !ok {
			continue
		}
		count := cluster.ObservationCount
		if count <= 0 {
			count = 1
		}
		centroids = append(centroids, faceClusterCentroid{ClusterID: cluster.ID, Vector: normalized, Count: count})
	}
	return centroids, nil
}

// refreshPersonCandidates 给观测数够多的未命名簇算"可能是谁"（D-017）。
// 只生成建议，不建立任何关系。
func (s *FaceAnalysisService) refreshPersonCandidates(ctx context.Context) error {
	seeds, err := loadFacePersonSeeds(ctx)
	if err != nil {
		return err
	}
	if len(seeds) == 0 {
		return nil
	}
	var rows []models.FaceCluster
	if err := database.DB.WithContext(ctx).Model(&models.FaceCluster{}).
		Select("id", "centroid", "observation_count", "status").
		Where("status = ?", models.FaceClusterStatusUnnamed).
		Where("observation_count >= ?", faceCandidateMinObservations).
		Order("id ASC").Find(&rows).Error; err != nil {
		return err
	}
	for _, cluster := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		vector, err := decodeFaceEmbedding(cluster.Centroid)
		if err != nil {
			continue
		}
		normalized, ok := normalizeFaceEmbedding(vector)
		if !ok {
			continue
		}
		for personID, seed := range seeds {
			similarity := faceSimilarity(normalized, seed)
			if similarity < facePersonSeedThreshold {
				continue
			}
			candidate := models.FacePersonCandidate{ClusterID: cluster.ID, PersonID: personID, Similarity: similarity}
			if err := upsertFacePersonCandidate(ctx, candidate); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadFacePersonSeeds 取每个人物的头像向量（同一人物有多条时取质量最高的）。
func loadFacePersonSeeds(ctx context.Context) (map[uint][]float32, error) {
	var observations []models.FaceObservation
	if err := database.DB.WithContext(ctx).Model(&models.FaceObservation{}).
		Select("id", "media_id", "quality", "embedding", "bbox_hash").
		Where("media_kind = ?", models.FaceMediaKindPersonAvatar).
		Where("bbox_hash <> ?", models.FaceNoFaceBBoxHash).
		Order("media_id ASC, quality DESC").Find(&observations).Error; err != nil {
		return nil, fmt.Errorf("load face person seeds: %w", err)
	}
	seeds := make(map[uint][]float32, len(observations))
	for _, observation := range observations {
		if _, exists := seeds[observation.MediaID]; exists {
			continue
		}
		vector, err := decodeFaceEmbedding(observation.Embedding)
		if err != nil {
			continue
		}
		normalized, ok := normalizeFaceEmbedding(vector)
		if !ok {
			continue
		}
		seeds[observation.MediaID] = normalized
	}
	return seeds, nil
}

func upsertFacePersonCandidate(ctx context.Context, candidate models.FacePersonCandidate) error {
	return database.Transaction(func(tx *gorm.DB) error {
		var existing models.FacePersonCandidate
		err := tx.WithContext(ctx).Where("cluster_id = ? AND person_id = ?", candidate.ClusterID, candidate.PersonID).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.WithContext(ctx).Create(&candidate).Error
		}
		if err != nil {
			return err
		}
		return tx.WithContext(ctx).Model(&models.FacePersonCandidate{}).Where("id = ?", existing.ID).
			Update("similarity", candidate.Similarity).Error
	})
}

// pruneOrphanFaceData 清掉媒体已永久删除的观测（4.4.4）。
// 软删除不算：视频/图片进回收站还能恢复，观测留着重新用。
func (s *FaceAnalysisService) pruneOrphanFaceData(ctx context.Context) (int64, error) {
	var removed int64
	var orphans []models.FaceObservation
	err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).
			Where("(media_kind = ? AND media_id NOT IN (?))", models.FaceMediaKindVideo,
				tx.Model(&models.Video{}).Unscoped().Select("id")).
			Or("(media_kind = ? AND media_id NOT IN (?))", models.FaceMediaKindImage,
				tx.Model(&models.Image{}).Unscoped().Select("id")).
			Or("(media_kind = ? AND media_id NOT IN (?))", models.FaceMediaKindPersonAvatar,
				tx.Model(&models.Person{}).Select("id")).
			Find(&orphans).Error; err != nil {
			return err
		}
		if len(orphans) == 0 {
			return nil
		}
		ids := make([]uint, 0, len(orphans))
		for _, orphan := range orphans {
			ids = append(ids, orphan.ID)
		}
		result := tx.WithContext(ctx).Where("id IN ?", ids).Delete(&models.FaceObservation{})
		if result.Error != nil {
			return result.Error
		}
		removed = result.RowsAffected
		// 与重新分析同一条口径：删了观测就在同一事务里把簇修回来。
		_, err := recomputeFaceClustersTx(ctx, tx, faceClusterIDsOf(orphans))
		return err
	})
	if err != nil {
		return 0, err
	}
	s.removeCropFiles(orphans)
	return removed, nil
}

// faceClusterIDsOf 取一批观测涉及的去重簇 id（未归簇的跳过）。
func faceClusterIDsOf(observations []models.FaceObservation) []uint {
	seen := make(map[uint]struct{}, len(observations))
	ids := make([]uint, 0, len(observations))
	for _, observation := range observations {
		if observation.ClusterID == nil {
			continue
		}
		if _, exists := seen[*observation.ClusterID]; exists {
			continue
		}
		seen[*observation.ClusterID] = struct{}{}
		ids = append(ids, *observation.ClusterID)
	}
	return ids
}

// removeFaceClusterCentroids 从内存快照里摘掉这些簇。
func removeFaceClusterCentroids(clusters []faceClusterCentroid, removed []uint) []faceClusterCentroid {
	if len(removed) == 0 {
		return clusters
	}
	drop := make(map[uint]struct{}, len(removed))
	for _, id := range removed {
		drop[id] = struct{}{}
	}
	kept := clusters[:0]
	for _, cluster := range clusters {
		if _, gone := drop[cluster.ClusterID]; gone {
			continue
		}
		kept = append(kept, cluster)
	}
	return kept
}

// recomputeFaceClustersTx 按实际观测行重算这些簇的观测数与代表；
// 一条观测都不剩的簇连同它的人物候选一起删除，返回被删掉的簇 id。
//
// 必须收在调用方的事务里：SQLite 的 _txlock=immediate 下再套一层事务必然自锁
// （设计 V1.0.11 的教训）。
func recomputeFaceClustersTx(ctx context.Context, tx *gorm.DB, clusterIDs []uint) ([]uint, error) {
	dropped := make([]uint, 0, len(clusterIDs))
	for _, clusterID := range clusterIDs {
		var count int64
		if err := tx.WithContext(ctx).Model(&models.FaceObservation{}).
			Where("cluster_id = ?", clusterID).Count(&count).Error; err != nil {
			return nil, err
		}
		if count == 0 {
			if err := tx.WithContext(ctx).Where("cluster_id = ?", clusterID).
				Delete(&models.FacePersonCandidate{}).Error; err != nil {
				return nil, err
			}
			if err := tx.WithContext(ctx).Delete(&models.FaceCluster{}, clusterID).Error; err != nil {
				return nil, err
			}
			dropped = append(dropped, clusterID)
			continue
		}
		var cluster models.FaceCluster
		if err := tx.WithContext(ctx).Select("id", "representative_observation_id").
			First(&cluster, clusterID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		updates := map[string]interface{}{"observation_count": count}
		// 代表观测可能正是被删掉的那条：换成剩下的里质量最高的。
		representativeGone := cluster.RepresentativeObservationID == nil
		if !representativeGone {
			var representative models.FaceObservation
			err := tx.WithContext(ctx).Select("id", "cluster_id").
				First(&representative, *cluster.RepresentativeObservationID).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				representativeGone = true
			case err != nil:
				return nil, err
			case representative.ClusterID == nil || *representative.ClusterID != clusterID:
				representativeGone = true
			}
		}
		if representativeGone {
			var best models.FaceObservation
			if err := tx.WithContext(ctx).Select("id").Where("cluster_id = ?", clusterID).
				Order("quality DESC, id ASC").First(&best).Error; err != nil {
				return nil, err
			}
			updates["representative_observation_id"] = best.ID
		}
		if err := tx.WithContext(ctx).Model(&models.FaceCluster{}).Where("id = ?", clusterID).
			Updates(updates).Error; err != nil {
			return nil, err
		}
	}
	return dropped, nil
}

// ClearFaceData 删掉全部人脸数据（D-020）：三张表 + 裁剪目录。
// 绝不触碰 people / video_people / image_people——那是用户自己维护的关系。
func (s *FaceAnalysisService) ClearFaceData() (FaceDataUsage, error) {
	if s == nil {
		return FaceDataUsage{}, errors.New("face analysis service is not initialized")
	}
	if !s.dataDirAvailable() {
		// 守卫只判 facesDir != "" 是不够的：空 dataDir 拼出来的 "faces" 是相对路径，
		// RemoveAll 会把进程 cwd 下的同名目录删掉。
		return FaceDataUsage{}, ErrFaceDataDirUnavailable
	}
	if status := s.Status(); status.Running {
		return FaceDataUsage{}, errors.New("人脸分析正在运行，请先取消后再清除数据")
	}
	err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&models.FacePersonCandidate{}).Error; err != nil {
			return err
		}
		if err := tx.Where("1 = 1").Delete(&models.FaceCluster{}).Error; err != nil {
			return err
		}
		return tx.Where("1 = 1").Delete(&models.FaceObservation{}).Error
	})
	if err != nil {
		return FaceDataUsage{}, err
	}
	if s.facesDir != "" {
		if err := os.RemoveAll(s.facesDir); err != nil && !os.IsNotExist(err) {
			return FaceDataUsage{}, err
		}
	}
	log.Printf("[Face] 已清除全部人脸数据")
	return s.DataUsage()
}

// DataUsage 统计人脸数据占用（7.2 `GetFaceDataUsage`）。
func (s *FaceAnalysisService) DataUsage() (FaceDataUsage, error) {
	if s == nil {
		return FaceDataUsage{}, errors.New("face analysis service is not initialized")
	}
	if !s.dataDirAvailable() {
		return FaceDataUsage{}, ErrFaceDataDirUnavailable
	}
	var usage FaceDataUsage
	if err := database.DB.Model(&models.FaceObservation{}).Count(&usage.ObservationCount).Error; err != nil {
		return FaceDataUsage{}, err
	}
	if err := database.DB.Model(&models.FaceCluster{}).Count(&usage.ClusterCount).Error; err != nil {
		return FaceDataUsage{}, err
	}
	if err := database.DB.Model(&models.FacePersonCandidate{}).Count(&usage.CandidateCount).Error; err != nil {
		return FaceDataUsage{}, err
	}
	entries, err := os.ReadDir(s.facesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return usage, nil
		}
		return FaceDataUsage{}, err
	}
	for _, entry := range entries {
		if entry.IsDir() || isFaceCropTempName(entry.Name()) {
			// 写裁剪图是"临时文件 + rename"，中途被 kill 会留下 .face-*.jpg。
			// 它们不是数据，不该算进用量里（每轮分析开始时会被清掉）。
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		usage.CropFileCount++
		usage.CropBytes += info.Size()
	}
	return usage, nil
}

// isFaceCropTempName 判断是不是裁剪图的临时残件（cropFaceThumbnail 的 CreateTemp 前缀）。
func isFaceCropTempName(name string) bool {
	return strings.HasPrefix(name, faceCropTempPrefix)
}

// sweepFaceCropTemps 清掉 faces 目录里的临时残件，返回清掉的个数。
func (s *FaceAnalysisService) sweepFaceCropTemps() int {
	if !s.dataDirAvailable() {
		return 0
	}
	entries, err := os.ReadDir(s.facesDir)
	if err != nil {
		return 0
	}
	swept := 0
	for _, entry := range entries {
		if entry.IsDir() || !isFaceCropTempName(entry.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(s.facesDir, entry.Name())); err == nil {
			swept++
		}
	}
	return swept
}

// FaceCropAsset 是裁剪图路由返回的资源。
type FaceCropAsset struct {
	Path    string
	ModTime time.Time
	MIME    string
}

// ResolveFaceCrop 解析裁剪图路由（`/preview/face-crop/{observationID}`，D-020）。
// 只认 faces 目录内的文件：库里的 crop_path 被写成 `../secrets` 也出不了这个目录。
func (s *FaceAnalysisService) ResolveFaceCrop(observationID uint) (*FaceCropAsset, error) {
	if s == nil {
		return nil, os.ErrNotExist
	}
	var observation models.FaceObservation
	if err := database.DB.Select("id", "crop_path").First(&observation, observationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	path, ok := s.cropFilePath(observation.CropPath)
	if !ok {
		return nil, os.ErrNotExist
	}
	// Lstat 而不是 Stat：软链接不跟随。faces 目录里只应该有我们自己写的普通
	// JPEG，出现别的东西（软链、目录、fifo）一律当作不存在，不顺着它读出去。
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrNotExist
	}
	return &FaceCropAsset{Path: path, ModTime: info.ModTime(), MIME: "image/jpeg"}, nil
}

// FaceCropPath 返回前端可用的裁剪图资源路径。
func FaceCropPath(observationID uint) string {
	return fmt.Sprintf("%s%d", faceCropRoutePrefix, observationID)
}

// faceCropRoutePrefix 与 preview_asset_handler.go 的路由前缀是同一个常量。
const faceCropRoutePrefix = "/preview/face-crop/"
