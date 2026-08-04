package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EnhancementService 是视频超分任务的唯一入口：PostgreSQL 任务表为事实源，
// 全应用单 worker 串行执行（P-012 定稿 §4）。原视频只读；所有失败/取消
// 路径都必须清理隐藏工作目录，不遗留输出记录。
type EnhancementService struct {
	capability   EnhancementRuntimeCapability
	videoService *VideoService
	probe        *MediaProbeService
	sameSource   *AISameSourceService

	// 测试接缝：外部命令、磁盘空间、ffmpeg/ffprobe 定位、时间源。
	runCommand       enhancementCommandRunner
	runCommandOutput enhancementOutputRunner
	diskFree         func(path string) (uint64, error)
	findFFmpeg       func() (string, error)
	findFFprobe      func() (string, error)
	now              func() time.Time

	mu            sync.Mutex
	workerRunning bool
	stopping      bool
	currentTaskID uint
	currentCancel context.CancelFunc
	publishing    bool
	worker        sync.WaitGroup
	emitter       func(EnhancementTaskView)
	parentCtx     context.Context
}

type enhancementCommandRunner func(ctx context.Context, name string, args []string) (stderrTail string, err error)

// enhancementOutputRunner 捕获完整 stdout（上限 16 MiB）与 4 KiB stderr 尾部，
// 供 ffprobe 等需要完整 JSON 输出的调用使用（不能走合并 tail）。
type enhancementOutputRunner func(ctx context.Context, name string, args []string) (stdout string, stderrTail string, err error)

// EnhancementTaskView 是暴露给前端的任务快照。
type EnhancementTaskView struct {
	models.VideoEnhancementTask
	VideoName string `json:"video_name"`
}

// EnhancementCreateRequest 创建一个超分任务。
type EnhancementCreateRequest struct {
	VideoID uint   `json:"video_id"`
	Profile string `json:"profile"`
}

// ErrEnhancementPublishInProgress 表示任务已进入原子发布，不能取消。
var ErrEnhancementPublishInProgress = errors.New("publish_in_progress")

// NewEnhancementService 创建服务并探测运行时能力。
func NewEnhancementService(videoService *VideoService, probe *MediaProbeService, sameSource *AISameSourceService) *EnhancementService {
	return &EnhancementService{
		capability:       ProbeEnhancementRuntime(""),
		videoService:     videoService,
		probe:            probe,
		sameSource:       sameSource,
		runCommand:       runEnhancementCommand,
		runCommandOutput: runEnhancementCommandOutput,
		diskFree:         enhancementDiskFree,
		findFFmpeg:       findThumbnailFFmpeg,
		findFFprobe:      findFFProbeBinary,
		now:              time.Now,
	}
}

// SetEventEmitter 注册状态事件回调。
func (s *EnhancementService) SetEventEmitter(emitter func(EnhancementTaskView)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

// Capability 返回运行时能力状态。
func (s *EnhancementService) Capability() EnhancementRuntimeCapability {
	return s.capability
}

// CreateTask 校验输入闭集与磁盘下限后排队任务；同源视频已有活跃任务时
// 返回该任务（幂等，不重复排队）。
func (s *EnhancementService) CreateTask(ctx context.Context, request EnhancementCreateRequest) (*EnhancementTaskView, error) {
	if !s.capability.Available {
		return nil, fmt.Errorf("%w: %s", ErrEnhancementUnavailable, s.capability.Message)
	}
	spec, ok := EnhancementProfiles[request.Profile]
	if !ok {
		return nil, fmt.Errorf("未知超分配置: %q（可选 general/anime）", request.Profile)
	}
	var video models.Video
	if err := database.DB.First(&video, request.VideoID).Error; err != nil {
		return nil, err
	}
	if video.IsStale {
		return nil, fmt.Errorf("unsupported_input: 视频路径已失效")
	}
	info, err := os.Lstat(video.Path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unsupported_input: 源文件不可读或不是常规文件")
	}

	probeInfo, err := s.probeEnhancementSource(ctx, video.Path)
	if err != nil {
		return nil, err
	}
	if err := validateEnhancementInput(probeInfo); err != nil {
		return nil, err
	}

	outputBasename := EnhancementOutputBasename(video.Path, spec.Profile)
	if err := s.ensureOutputNameFree(video, outputBasename); err != nil {
		return nil, err
	}
	if err := s.ensureDiskFloor(video.Path, info.Size(), probeInfo.Width, probeInfo.Height); err != nil {
		return nil, err
	}

	task := models.VideoEnhancementTask{
		VideoID:         video.ID,
		Profile:         spec.Profile,
		Scale:           spec.Scale,
		Status:          models.EnhancementStatusQueued,
		Phase:           models.EnhancementPhasePreflight,
		SourceSize:      info.Size(),
		SourceModTimeNS: info.ModTime().UnixNano(),
		RuntimeVersion:  s.capability.RuntimeVersion,
		ModelVersion:    spec.ModelName,
		OutputBasename:  outputBasename,
	}
	err = database.Transaction(func(tx *gorm.DB) error {
		var active models.VideoEnhancementTask
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("video_id = ? AND status IN ?", video.ID, enhancementActiveStatuses()).
			First(&active).Error
		if findErr == nil {
			task = active
			return nil
		}
		if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}
		return tx.Create(&task).Error
	})
	if err != nil {
		message := err.Error()
		if strings.Contains(message, "duplicate key") || strings.Contains(message, "UNIQUE constraint") {
			var active models.VideoEnhancementTask
			if readErr := database.DB.Where("video_id = ? AND status IN ?", video.ID, enhancementActiveStatuses()).
				First(&active).Error; readErr == nil {
				view := s.taskView(active, video.Name)
				return &view, nil
			}
		}
		return nil, err
	}
	view := s.taskView(task, video.Name)
	s.emit(view)
	s.ensureWorker()
	return &view, nil
}

// EnhancementVideoPreflight 是创建弹窗展示的每视频预检信息。
type EnhancementVideoPreflight struct {
	OutputBasenameGeneral string `json:"output_basename_general"`
	OutputBasenameAnime   string `json:"output_basename_anime"`
	RequiredBytes         int64  `json:"required_bytes"`
	FreeBytes             uint64 `json:"free_bytes"`
}

// PreflightVideo 返回指定视频的输出名与磁盘下限（与后端实际预检同一公式）。
func (s *EnhancementService) PreflightVideo(videoID uint) (*EnhancementVideoPreflight, error) {
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return nil, err
	}
	free, err := s.diskFree(filepath.Dir(video.Path))
	if err != nil {
		free = 0
	}
	return &EnhancementVideoPreflight{
		OutputBasenameGeneral: EnhancementOutputBasename(video.Path, "general"),
		OutputBasenameAnime:   EnhancementOutputBasename(video.Path, "anime"),
		RequiredBytes:         EnhancementRequiredDiskBytes(video.Size, video.Width, video.Height),
		FreeBytes:             free,
	}, nil
}

// ListTasks 返回全部活跃任务和最近 limit 条历史任务。
func (s *EnhancementService) ListTasks(limit int) ([]EnhancementTaskView, error) {
	if limit <= 0 {
		limit = 20
	}
	var tasks []models.VideoEnhancementTask
	if err := database.DB.Preload("Video").
		Where("status IN ?", enhancementActiveStatuses()).
		Order("created_at ASC, id ASC").Find(&tasks).Error; err != nil {
		return nil, err
	}
	var recent []models.VideoEnhancementTask
	if err := database.DB.Preload("Video").
		Where("status NOT IN ?", enhancementActiveStatuses()).
		Order("updated_at DESC").Limit(limit).Find(&recent).Error; err != nil {
		return nil, err
	}
	views := make([]EnhancementTaskView, 0, len(tasks)+len(recent))
	for _, task := range append(tasks, recent...) {
		views = append(views, s.taskView(task, task.Video.Name))
	}
	return views, nil
}

// CancelTask 请求取消：queued 直接终态；running 标记 cancel_requested 并
// 终止当前子进程组；进入 publish 后拒绝（原子提交不可中断）。
func (s *EnhancementService) CancelTask(taskID uint) error {
	var task models.VideoEnhancementTask
	if err := database.DB.First(&task, taskID).Error; err != nil {
		return err
	}
	switch task.Status {
	case models.EnhancementStatusCancelled:
		return nil // 取消幂等
	case models.EnhancementStatusQueued:
		if err := s.transitionStatus(task.ID, models.EnhancementStatusQueued, models.EnhancementStatusCancelled, "cancelled", "用户取消"); err != nil {
			// CAS 竞争：任务恰好已被 worker 领取，按运行中取消处理。
			return s.cancelRunningTask(task.ID)
		}
		s.cleanupTaskWorkdir(task)
		s.emitTaskByID(task.ID)
		return nil
	case models.EnhancementStatusRunning, models.EnhancementStatusCancelRequested:
		return s.cancelRunningTask(task.ID)
	default:
		return fmt.Errorf("任务已结束（%s），无法取消", task.Status)
	}
}

func (s *EnhancementService) cancelRunningTask(taskID uint) error {
	s.mu.Lock()
	publishing := s.publishing && s.currentTaskID == taskID
	cancel := s.currentCancel
	isCurrent := s.currentTaskID == taskID
	s.mu.Unlock()
	if publishing {
		return ErrEnhancementPublishInProgress
	}
	_ = database.DB.Model(&models.VideoEnhancementTask{}).
		Where("id = ? AND status IN ?", taskID, []string{models.EnhancementStatusQueued, models.EnhancementStatusRunning}).
		Update("status", models.EnhancementStatusCancelRequested).Error
	if isCurrent && cancel != nil {
		cancel()
	}
	s.emitTaskByID(taskID)
	return nil
}

// RetryTask 复用同一任务记录重试失败/已取消任务：清零进度、重跑全部 preflight。
func (s *EnhancementService) RetryTask(taskID uint) (*EnhancementTaskView, error) {
	if !s.capability.Available {
		return nil, fmt.Errorf("%w: %s", ErrEnhancementUnavailable, s.capability.Message)
	}
	var task models.VideoEnhancementTask
	if err := database.DB.Preload("Video").First(&task, taskID).Error; err != nil {
		return nil, err
	}
	if task.Status != models.EnhancementStatusFailed && task.Status != models.EnhancementStatusCancelled {
		return nil, fmt.Errorf("只有失败或已取消的任务可以重试（当前 %s）", task.Status)
	}
	s.cleanupTaskWorkdir(task)
	info, err := os.Lstat(task.Video.Path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unsupported_input: 源文件不可读或不是常规文件")
	}
	spec := EnhancementProfiles[task.Profile]
	updates := map[string]any{
		"status": models.EnhancementStatusQueued, "phase": models.EnhancementPhasePreflight,
		"source_size": info.Size(), "source_mod_time_ns": info.ModTime().UnixNano(), "source_sha256": "",
		"runtime_version": s.capability.RuntimeVersion, "model_version": spec.ModelName,
		"total_frames": 0, "committed_frames": 0,
		"error_code": "", "error_summary": "",
		"started_at": nil, "finished_at": nil,
	}
	if err := database.DB.Model(&models.VideoEnhancementTask{}).
		Where("id = ? AND status IN ?", task.ID, []string{models.EnhancementStatusFailed, models.EnhancementStatusCancelled}).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	s.emitTaskByID(task.ID)
	s.ensureWorker()
	view := s.taskView(task, task.Video.Name)
	view.Status = models.EnhancementStatusQueued
	return &view, nil
}

// RecoverOnStartup 恢复既有 queued/running 任务并对孤立工作目录做有界对账。
// 不创建新任务；rename 后崩溃的任务由 reconcilePublishedTask 完成入库。
func (s *EnhancementService) RecoverOnStartup(parent context.Context) {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	s.parentCtx = parent
	// 对账登记到 worker 组：StopAndWait/恢复模式会等待进行中的对账发布。
	s.worker.Add(1)
	s.mu.Unlock()
	defer s.worker.Done()
	if !s.capability.Available {
		// 运行时不可用（含版本更换）：按定稿把旧活跃任务置为失败并清理。
		var tasks []models.VideoEnhancementTask
		if err := database.DB.Where("status IN ?", enhancementActiveStatuses()).Find(&tasks).Error; err != nil {
			return
		}
		for _, task := range tasks {
			s.failTask(task.ID, "runtime_unavailable", s.capability.Message)
			s.cleanupTaskWorkdir(task)
		}
		return
	}
	var tasks []models.VideoEnhancementTask
	if err := database.DB.Preload("Video").Where("status IN ?", enhancementActiveStatuses()).Find(&tasks).Error; err != nil {
		return
	}
	for _, task := range tasks {
		if task.RuntimeVersion != "" && task.RuntimeVersion != s.capability.RuntimeVersion {
			s.failTask(task.ID, "runtime_unavailable", "应用升级后原运行时不再可用，请显式重试")
			continue
		}
		// 崩溃时残留的 cancel_requested：完成用户取消（清理并终态），
		// 否则部分唯一索引会永久阻塞该源视频的新任务。
		if task.Status == models.EnhancementStatusCancelRequested {
			s.finishCancelled(task)
			continue
		}
		if task.Status == models.EnhancementStatusRunning && task.Phase == models.EnhancementPhasePublish {
			s.reconcilePublishedTask(parent, task)
		}
	}
	s.ensureWorker()
}

// StopAndWait 停止 worker 并等待退出；不改变任务状态（应用退出≠用户取消）。
func (s *EnhancementService) StopAndWait() {
	s.mu.Lock()
	s.stopping = true
	cancel := s.currentCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.worker.Wait()
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
}

func enhancementActiveStatuses() []string {
	return []string{models.EnhancementStatusQueued, models.EnhancementStatusRunning, models.EnhancementStatusCancelRequested}
}

func (s *EnhancementService) ensureWorker() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workerRunning || s.stopping {
		return
	}
	parent := s.parentCtx
	if parent == nil {
		parent = context.Background()
	}
	s.workerRunning = true
	s.worker.Add(1)
	go s.runWorker(parent)
}

func (s *EnhancementService) runWorker(parent context.Context) {
	defer func() {
		s.mu.Lock()
		s.workerRunning = false
		s.mu.Unlock()
		s.worker.Done()
	}()
	for {
		if parent.Err() != nil {
			return
		}
		s.mu.Lock()
		stopping := s.stopping
		s.mu.Unlock()
		if stopping {
			return
		}
		task, ok := s.claimNextTask()
		if !ok {
			return
		}
		ctx, cancel := context.WithCancel(parent)
		s.mu.Lock()
		s.currentTaskID = task.ID
		s.currentCancel = cancel
		s.publishing = false
		s.mu.Unlock()
		err := s.processTask(ctx, task)
		cancel()
		s.mu.Lock()
		s.currentTaskID = 0
		s.currentCancel = nil
		s.publishing = false
		s.mu.Unlock()
		if err != nil && parent.Err() != nil {
			// 生命周期停止：任务保持可恢复状态，不写终态。
			return
		}
	}
}

// claimNextTask 按 FIFO 取最老的活跃任务（含中断后的 running 恢复）。
func (s *EnhancementService) claimNextTask() (models.VideoEnhancementTask, bool) {
	var task models.VideoEnhancementTask
	err := database.DB.Preload("Video").
		Where("status IN ?", []string{models.EnhancementStatusQueued, models.EnhancementStatusRunning}).
		Order("created_at ASC, id ASC").First(&task).Error
	if err != nil {
		return task, false
	}
	if task.Status == models.EnhancementStatusQueued {
		if err := s.transitionStatus(task.ID, models.EnhancementStatusQueued, models.EnhancementStatusRunning, "", ""); err != nil {
			return task, false
		}
		task.Status = models.EnhancementStatusRunning
	}
	return task, true
}

func (s *EnhancementService) transitionStatus(taskID uint, from, to, errorCode, errorSummary string) error {
	updates := map[string]any{"status": to}
	if to == models.EnhancementStatusRunning {
		now := s.now()
		updates["started_at"] = &now
	}
	if to == models.EnhancementStatusCancelled || to == models.EnhancementStatusFailed || to == models.EnhancementStatusCompleted {
		now := s.now()
		updates["finished_at"] = &now
	}
	if errorCode != "" {
		updates["error_code"] = errorCode
		updates["error_summary"] = sanitizeEnhancementError(errorSummary)
	}
	result := database.DB.Model(&models.VideoEnhancementTask{}).
		Where("id = ? AND status = ?", taskID, from).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *EnhancementService) failTask(taskID uint, code, summary string) {
	now := s.now()
	_ = database.DB.Model(&models.VideoEnhancementTask{}).
		Where("id = ? AND status IN ?", taskID, enhancementActiveStatuses()).
		Updates(map[string]any{
			"status": models.EnhancementStatusFailed, "error_code": code,
			"error_summary": sanitizeEnhancementError(summary), "finished_at": &now,
		}).Error
	// 失败不得留下临时文件（D-007）：终态统一清理隐藏工作目录。
	var task models.VideoEnhancementTask
	if err := database.DB.First(&task, taskID).Error; err == nil {
		s.cleanupTaskWorkdir(task)
	}
	s.emitTaskByID(taskID)
}

func (s *EnhancementService) updatePhase(taskID uint, phase string) {
	_ = database.DB.Model(&models.VideoEnhancementTask{}).
		Where("id = ?", taskID).Update("phase", phase).Error
	s.emitTaskByID(taskID)
}

func (s *EnhancementService) taskView(task models.VideoEnhancementTask, videoName string) EnhancementTaskView {
	task.Video = models.Video{}
	return EnhancementTaskView{VideoEnhancementTask: task, VideoName: videoName}
}

func (s *EnhancementService) emit(view EnhancementTaskView) {
	s.mu.Lock()
	emitter := s.emitter
	s.mu.Unlock()
	if emitter != nil {
		emitter(view)
	}
}

func (s *EnhancementService) emitTaskByID(taskID uint) {
	var task models.VideoEnhancementTask
	if err := database.DB.Preload("Video").First(&task, taskID).Error; err != nil {
		return
	}
	s.emit(s.taskView(task, task.Video.Name))
}

// taskCancelRequested 检查数据库中的取消请求（批间边界）。
func (s *EnhancementService) taskCancelRequested(taskID uint) bool {
	var status string
	if err := database.DB.Model(&models.VideoEnhancementTask{}).
		Where("id = ?", taskID).Pluck("status", &status).Error; err != nil {
		return false
	}
	return status == models.EnhancementStatusCancelRequested
}

func (s *EnhancementService) ensureOutputNameFree(video models.Video, basename string) error {
	target := filepath.Join(filepath.Dir(video.Path), basename)
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("output_conflict: 输出文件名已被占用: %s", basename)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("output_conflict: 无法检查输出路径: %v", err)
	}
	var existing models.Video
	if err := database.DB.Unscoped().Where("path = ?", target).First(&existing).Error; err == nil {
		return fmt.Errorf("output_conflict: 输出路径已被片库记录占用（含回收站）")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}

func (s *EnhancementService) ensureDiskFloor(sourcePath string, sourceSize int64, width, height int) error {
	required := EnhancementRequiredDiskBytes(sourceSize, width, height)
	free, err := s.diskFree(filepath.Dir(sourcePath))
	if err != nil {
		return fmt.Errorf("disk_insufficient: 无法检查磁盘空间: %v", err)
	}
	if free < uint64(required) {
		return fmt.Errorf("disk_insufficient: 同卷可用空间不足（需要至少 %.1f GiB）", float64(required)/(1<<30))
	}
	return nil
}

func (s *EnhancementService) cleanupTaskWorkdir(task models.VideoEnhancementTask) {
	if task.Video.Path == "" {
		var video models.Video
		if err := database.DB.Unscoped().First(&video, task.VideoID).Error; err != nil {
			return
		}
		task.Video = video
	}
	workdir := enhancementWorkdir(task)
	if workdir == "" {
		return
	}
	_ = os.RemoveAll(workdir)
}

func enhancementWorkdir(task models.VideoEnhancementTask) string {
	if task.Video.Path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(task.Video.Path), fmt.Sprintf(".cineinsight-enhance-%d", task.ID))
}

// sanitizeEnhancementError 清洗绝对路径并截断到 4KiB（P-012 §3）。
func sanitizeEnhancementError(message string) string {
	message = semanticAbsolutePathPattern.ReplaceAllString(message, "[redacted-path]")
	if len(message) > 4096 {
		trimmed := message[len(message)-4096:]
		// 回退到 rune 边界，保证字节上限 4 KiB 且不截断多字节字符。
		for len(trimmed) > 0 && !utf8.RuneStart(trimmed[0]) {
			trimmed = trimmed[1:]
		}
		message = trimmed
	}
	return message
}

func runEnhancementCommand(ctx context.Context, name string, args []string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		// 终止整个子进程组，保证 sidecar 的解码线程一起退出。
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	tail := &tailBuffer{limit: 4096}
	command.Stderr = tail
	command.Stdout = tail
	if err := command.Run(); err != nil {
		return tail.String(), fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return tail.String(), nil
}

func runEnhancementCommandOutput(ctx context.Context, name string, args []string) (string, string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	stdout := &boundedBuffer{limit: 16 << 20}
	stderr := &tailBuffer{limit: 4096}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return stdout.String(), stderr.String(), nil
}

// boundedBuffer 保留前 limit 字节（超出丢弃，用于防御异常输出体积）。
type boundedBuffer struct {
	limit int
	data  []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remain := b.limit - len(b.data)
	if remain > 0 {
		if len(p) > remain {
			b.data = append(b.data, p[:remain]...)
		} else {
			b.data = append(b.data, p...)
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string { return string(b.data) }

type tailBuffer struct {
	limit int
	data  []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = b.data[len(b.data)-b.limit:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string { return string(b.data) }

func enhancementDiskFree(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

// logEnhancement 只输出 ID、版本、计数与已清洗错误（P-012 运行与安全影响）。
func logEnhancement(format string, args ...any) {
	log.Printf("[Enhance] "+format, args...)
}
