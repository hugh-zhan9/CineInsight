package services

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// frameHashGrayscaleBytes 是一帧灰度小图的字节数：9 列 × 8 行。
	// dHash 比较的是同一行里相邻两列的亮度，所以宽度要比高度多一列。
	frameHashGrayscaleBytes = 72
	frameHashFailureLimit   = 50
	// frameHashFailureWriteTimeout 给"记下失败原因"这一次写入单独留的窗口：
	// 任务被取消时父 ctx 已经废了，失败原因仍然要落库，否则界面上只剩一个
	// 没有解释的失败计数。
	frameHashFailureWriteTimeout = 2 * time.Second
)

// ErrFrameHashNotRunning 表示帧哈希回填当前没有在跑，取消无从谈起。
var ErrFrameHashNotRunning = errors.New("帧哈希回填任务未运行")

// frameHashAbsolutePathPattern 匹配以 / 开头、处在词首的连续 token。
//
// ffmpeg 的 stderr 几乎总带着输入文件的绝对路径，而 last_error 是要落库、
// 会被备份带走、也会随数据库迁移走的持久数据；仓库的日志纪律是不记路径全文，
// 落库的错误原因同理。要求 / 前面是词首或引号/括号，是为了不去动 "fps=1000/2000"
// 这类正常内容里的斜杠。
var frameHashAbsolutePathPattern = regexp.MustCompile(`(^|[\s'"(\[<])/[^\s'")\]>]*`)

// redactFrameHashErrorPaths 把错误原文里的绝对路径换成 <path>。
//
// token 末尾的标点（常见的 ": Invalid data found" 里那个冒号）会一起被吃掉：
// 路径本身可以含冒号，宁可少一个冒号也不留半截真实路径。
func redactFrameHashErrorPaths(message string) string {
	return frameHashAbsolutePathPattern.ReplaceAllString(message, "${1}<path>")
}

// frameHashRawFrameRunner 单趟抽出一个视频的全部灰度小图，按时间顺序交给 consume。
//
// 做成函数变量而不是直接调 ffmpeg：单测要能喂进可控的 rawvideo 字节，把 dHash 的
// 位序钉死在测试里，而不是隔着一个真实的 ffmpeg 去猜。
//
// consume 收到的切片在下一帧读进来时就会被覆盖，实现方只能当场用完，不许留存。
type frameHashRawFrameRunner func(ctx context.Context, path string, consume func(frame []byte)) error

type FrameHashFailure struct {
	VideoID uint   `json:"video_id"`
	Name    string `json:"name"`
	Error   string `json:"error"`
}

type FrameHashStatus struct {
	Running          bool               `json:"running"`
	Preparing        bool               `json:"preparing"`
	Cancelled        bool               `json:"cancelled"`
	Completed        bool               `json:"completed"`
	Total            int                `json:"total"`
	Processed        int                `json:"processed"`
	Succeeded        int                `json:"succeeded"`
	Skipped          int                `json:"skipped"`
	Failed           int                `json:"failed"`
	CurrentVideoID   uint               `json:"current_video_id"`
	CurrentVideoName string             `json:"current_video_name"`
	StartedAt        *time.Time         `json:"started_at" ts_type:"string"`
	UpdatedAt        *time.Time         `json:"updated_at" ts_type:"string"`
	Failures         []FrameHashFailure `json:"failures"`
	// Gate 是空闲门状态（D-032）：自动路径被挡住时这里说明原因，显式启动恒为零值。
	Gate TaskGateState `json:"gate"`
}

type frameHashCandidate struct {
	ID   uint
	Name string
}

// FrameHashService 回填 video_frame_hash_sequences（D-026）。
//
// 单 worker、可取消、可续跑：源指纹没变的视频直接跳过，所以中断之后重跑不会重算
// 已经算好的部分。每处理一项之前先取 MediaWorkSlot（D-007）——抽帧是吃满 CPU 的
// ffmpeg 重活，与转封装、人脸抽帧共享同一个槽位，同一时刻只跑一个。
//
// 现有的三点感知哈希（PerceptualHashService）完全不受影响，两条流水线各写各的表。
type FrameHashService struct {
	rawFrames      frameHashRawFrameRunner
	slot           *MediaWorkSlot
	now            func() time.Time
	loadCandidates func(context.Context) ([]frameHashCandidate, error)
	mu             sync.Mutex
	stopMu         sync.Mutex
	status         FrameHashStatus
	cancel         context.CancelFunc
	worker         sync.WaitGroup
	emitter        func(FrameHashStatus)
	stopping       bool
	registry       *BackgroundTaskRegistry
	pauseHook      TaskPauseHook
}

// NewFrameHashService 构造回填服务。slot 为 nil 时不做并发限制（单测夹具用）。
func NewFrameHashService(slot *MediaWorkSlot) *FrameHashService {
	service := &FrameHashService{
		rawFrames: ffmpegFrameHashRawFrames,
		slot:      slot,
		now:       time.Now,
	}
	service.loadCandidates = loadFrameHashCandidates
	return service
}

func (s *FrameHashService) SetEventEmitter(emitter func(FrameHashStatus)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

// SetBackgroundTaskRegistry 接入后台任务登记表（D-014）。
func (s *FrameHashService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

// Start 是显式启动路径：同一把锁下摘掉当前这一轮的项间检查点，
// 用户主动点的任务永远不被空闲门挡住（D-030）。
func (s *FrameHashService) Start(parent context.Context) (FrameHashStatus, error) {
	return s.start(parent, nil)
}

// StartWithPauseHook 是自动路径专用：装钩子与翻 Running 在同一把锁里完成。
// 服务已经在跑时按"已在运行"返回且不装钩子——那一轮可能是用户显式启动的。
func (s *FrameHashService) StartWithPauseHook(parent context.Context, hook TaskPauseHook) (FrameHashStatus, error) {
	return s.start(parent, hook)
}

func (s *FrameHashService) start(parent context.Context, hook TaskPauseHook) (FrameHashStatus, error) {
	if s == nil || s.rawFrames == nil || s.loadCandidates == nil {
		return FrameHashStatus{}, errors.New("帧哈希回填服务未初始化")
	}
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	releasing := clearReplacedPauseHook(&s.pauseHook, hook)
	if s.stopping {
		s.mu.Unlock()
		releaseTaskPauseHook(releasing)
		return FrameHashStatus{}, errors.New("帧哈希回填任务正在停止")
	}
	if s.status.Running {
		status := cloneFrameHashStatus(s.status)
		s.mu.Unlock()
		releaseTaskPauseHook(releasing)
		return status, nil
	}
	s.pauseHook = hook
	now := s.now()
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.status = FrameHashStatus{
		Running:   true,
		Preparing: true,
		StartedAt: &now,
		UpdatedAt: &now,
		Failures:  []FrameHashFailure{},
	}
	status, emitter, registry := cloneFrameHashStatus(s.status), s.emitter, s.registry
	s.worker.Add(1)
	s.mu.Unlock()
	releaseTaskPauseHook(releasing)
	registry.Begin(BackgroundTaskFrameHash)
	emitFrameHashStatus(emitter, status)
	go func() {
		defer s.worker.Done()
		defer registry.End(BackgroundTaskFrameHash)
		s.prepareAndRun(ctx)
	}()
	return status, nil
}

func (s *FrameHashService) Status() FrameHashStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneFrameHashStatus(s.status)
}

func (s *FrameHashService) Cancel() error {
	s.mu.Lock()
	cancel, running := s.cancel, s.status.Running
	if running && cancel != nil {
		s.status.Cancelled = true
		now := s.now()
		s.status.UpdatedAt = &now
	}
	status, emitter := cloneFrameHashStatus(s.status), s.emitter
	s.mu.Unlock()
	if !running || cancel == nil {
		return ErrFrameHashNotRunning
	}
	cancel()
	emitFrameHashStatus(emitter, status)
	return nil
}

func (s *FrameHashService) StopAndWait() {
	if s == nil {
		return
	}
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	s.mu.Lock()
	s.stopping = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.worker.Wait()
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
}

func (s *FrameHashService) waitForPauseHook(ctx context.Context) error {
	s.mu.Lock()
	hook := s.pauseHook
	s.mu.Unlock()
	if hook == nil {
		return nil
	}
	// 钩子在服务锁之外调用：它会阻塞很久，持锁等待会连 Status/Cancel 一起冻住。
	return hook.Wait(ctx, s.setGateState)
}

func (s *FrameHashService) setGateState(state TaskGateState) {
	s.update(func(status *FrameHashStatus) { status.Gate = state })
}

func (s *FrameHashService) prepareAndRun(ctx context.Context) {
	candidates, err := s.loadCandidates(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			s.finish(true)
			return
		}
		s.failPreparation(err)
		return
	}
	if ctx.Err() != nil {
		s.finish(true)
		return
	}
	s.update(func(status *FrameHashStatus) {
		status.Preparing = false
		status.Total = len(candidates)
	})
	if len(candidates) == 0 {
		s.finish(false)
		return
	}
	s.run(ctx, candidates)
}

func (s *FrameHashService) run(ctx context.Context, candidates []frameHashCandidate) {
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		// 项间检查点在取槽位之前：停在门口的 worker 不该占着重媒体槽，
		// 否则转封装与人脸抽帧要陪它一起等到机器空闲。
		if err := s.waitForPauseHook(ctx); err != nil {
			break
		}
		if err := s.processCandidate(ctx, candidate); err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				break
			}
			s.recordFailure(candidate, err)
			continue
		}
	}
	s.finish(ctx.Err() != nil)
}

// processCandidate 处理一项：取槽位、重读记录、指纹判新、抽帧算哈希、落库。
// 返回错误由调用方统一记账，ctx 相关的错误一律当成"这一轮结束"。
func (s *FrameHashService) processCandidate(ctx context.Context, candidate frameHashCandidate) error {
	if s.slot != nil {
		if err := s.slot.Acquire(ctx); err != nil {
			return err
		}
		defer s.slot.Release()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.setCurrent(candidate)

	var video models.Video
	if err := database.DB.WithContext(ctx).Select("id", "name", "path").First(&video, candidate.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 排好候选之后视频被删了：跳过，不算失败。
			s.recordSkipped()
			return nil
		}
		return fmt.Errorf("重读视频记录失败: %w", err)
	}
	fresh, err := frameHashSequenceIsFresh(ctx, video)
	if err != nil {
		return err
	}
	if fresh {
		s.recordSkipped()
		return nil
	}
	if err := s.Refresh(ctx, video); err != nil {
		return err
	}
	s.recordSuccess()
	return nil
}

// Refresh 重算并写入一个视频的帧哈希序列。
// 失败时把原因写进 last_error（连同当时的源指纹），下一轮回填会再试一次。
func (s *FrameHashService) Refresh(ctx context.Context, video models.Video) error {
	info, err := os.Stat(video.Path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("视频路径不是普通文件")
	}
	hashes := make([]uint64, 0, 512)
	var hashErr error
	runErr := s.rawFrames(ctx, video.Path, func(frame []byte) {
		if hashErr != nil {
			return
		}
		hash, err := frameHashFromGrayscale(frame)
		if err != nil {
			hashErr = err
			return
		}
		hashes = append(hashes, hash)
	})
	if runErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.recordSequenceFailure(ctx, video.ID, info, runErr)
		return runErr
	}
	if hashErr != nil {
		s.recordSequenceFailure(ctx, video.ID, info, hashErr)
		return hashErr
	}
	if len(hashes) == 0 {
		// 一帧都没抽出来：文件坏了或者不是能解码的视频。记下来，别留一行空序列
		// 让匹配阶段以为"这个视频已经算过了"。
		emptyErr := errors.New("ffmpeg 没有抽出任何帧")
		s.recordSequenceFailure(ctx, video.ID, info, emptyErr)
		return emptyErr
	}
	row := models.VideoFrameHashSequence{
		VideoID:         video.ID,
		IntervalMS:      clipFrameIntervalMS,
		Hashes:          encodeFrameHashes(hashes),
		FrameCount:      len(hashes),
		SourceSize:      info.Size(),
		SourceModTimeNS: info.ModTime().UnixNano(),
		ComputedAt:      s.now(),
		LastError:       "",
	}
	return database.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "video_id"}}, UpdateAll: true,
	}).Create(&row).Error
}

func (s *FrameHashService) recordSequenceFailure(parent context.Context, videoID uint, info os.FileInfo, operationErr error) {
	row := models.VideoFrameHashSequence{
		VideoID:         videoID,
		IntervalMS:      clipFrameIntervalMS,
		Hashes:          []byte{},
		FrameCount:      0,
		SourceSize:      info.Size(),
		SourceModTimeNS: info.ModTime().UnixNano(),
		ComputedAt:      s.now(),
		// 先按既有口径截断，再抹路径：抹掉只会让串更短，1000 字节的上限仍然成立；
		// 被截断成半截的路径同样会被匹配掉（正则一路吃到串尾）。
		LastError: redactFrameHashErrorPaths(boundedError(operationErr, 1000)),
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), frameHashFailureWriteTimeout)
	defer cancel()
	_ = database.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "video_id"}}, UpdateAll: true,
	}).Create(&row).Error
}

// loadFrameHashCandidates 挑出需要回填的活跃视频：没有序列的，或者源文件变过的。
func loadFrameHashCandidates(ctx context.Context) ([]frameHashCandidate, error) {
	var videos []models.Video
	if err := database.DB.WithContext(ctx).Select("id", "name", "path").Order("id ASC").Find(&videos).Error; err != nil {
		return nil, fmt.Errorf("load frame hash videos: %w", err)
	}
	sequences, err := loadFrameHashFingerprints(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]frameHashCandidate, 0)
	for _, video := range videos {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sequence, exists := sequences[video.ID]
		if exists && frameHashSequenceMatchesFile(video.Path, sequence) {
			continue
		}
		candidates = append(candidates, frameHashCandidate{ID: video.ID, Name: video.Name})
	}
	return candidates, nil
}

func loadFrameHashFingerprints(ctx context.Context) (map[uint]models.VideoFrameHashSequence, error) {
	var rows []models.VideoFrameHashSequence
	if err := database.DB.WithContext(ctx).
		Select("video_id", "interval_ms", "frame_count", "source_size", "source_mod_time_ns", "last_error").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load frame hash sequences: %w", err)
	}
	byVideoID := make(map[uint]models.VideoFrameHashSequence, len(rows))
	for _, row := range rows {
		byVideoID[row.VideoID] = row
	}
	return byVideoID, nil
}

func frameHashSequenceIsFresh(ctx context.Context, video models.Video) (bool, error) {
	var row models.VideoFrameHashSequence
	err := database.DB.WithContext(ctx).
		Select("video_id", "interval_ms", "frame_count", "source_size", "source_mod_time_ns", "last_error").
		First(&row, "video_id = ?", video.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return frameHashSequenceMatchesFile(video.Path, row), nil
}

// frameHashSequenceMatchesFile 判断一行序列是否仍然对应磁盘上的那个文件。
//
// 带 last_error 的行不算新鲜：那是上一轮失败留下的记录，下一次回填要再试一次
// （与三点感知哈希同口径）。采样间隔与当前常量不一致的行同样要重算——不同间隔的
// 序列之间无法逐帧对齐，留着它只会让匹配阶段跳过这个视频。
func frameHashSequenceMatchesFile(path string, row models.VideoFrameHashSequence) bool {
	if row.LastError != "" || row.FrameCount <= 0 || row.IntervalMS != clipFrameIntervalMS {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Size() == row.SourceSize && info.ModTime().UnixNano() == row.SourceModTimeNS
}

// frameHashFromGrayscale 把一帧 9×8 灰度小图算成 64 位 dHash：
// 每行比较相邻两列的亮度，左亮于右记 1。位序与三点感知哈希一致（行优先、高位在前），
// 但那边落库是十六进制字符串，这里要的是能直接做异或与 popcount 的 uint64。
func frameHashFromGrayscale(grayscale []byte) (uint64, error) {
	if len(grayscale) != frameHashGrayscaleBytes {
		return 0, fmt.Errorf("灰度帧有 %d 字节，期望 %d", len(grayscale), frameHashGrayscaleBytes)
	}
	var value uint64
	for row := 0; row < 8; row++ {
		for column := 0; column < 8; column++ {
			value <<= 1
			if grayscale[row*9+column] > grayscale[row*9+column+1] {
				value |= 1
			}
		}
	}
	return value, nil
}

// ffmpegFrameHashRawFrames 单趟抽出整部片的灰度小图（D-026）。
//
// 一次进程、一条管子读到底：按时间点逐帧 seek 的做法在两小时的片上要起 3600 次
// ffmpeg，光进程与解码器初始化就把预算吃光了。
func ffmpegFrameHashRawFrames(ctx context.Context, path string, consume func(frame []byte)) error {
	ffmpegBin, err := findThumbnailFFmpeg()
	if err != nil {
		return err
	}
	// -fflags +discardcorrupt 容忍损坏的数据包（老编码器或部分下载的文件常见），
	// 与三点感知哈希同口径；fps 滤镜按 PTS 重采样，丢包不会让后续帧整体错位。
	command := exec.CommandContext(ctx, ffmpegBin,
		"-v", "warning",
		"-fflags", "+discardcorrupt",
		"-i", path,
		"-vf", fmt.Sprintf("fps=1000/%d,scale=9:8:flags=area,format=gray", clipFrameIntervalMS),
		"-f", "rawvideo",
		"pipe:1",
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return err
	}
	readErr := readFrameHashRawFrames(stdout, consume)
	if readErr != nil && command.Process != nil {
		// 不再读了就必须先杀进程：ffmpeg 还在往满了的管子里写，Wait 会永远不返回。
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil {
		return fmt.Errorf("读取 ffmpeg 灰度输出失败: %w: %s", readErr, truncateLogSnippet(stderr.String(), 400))
	}
	if waitErr != nil {
		return fmt.Errorf("ffmpeg 抽帧失败: %w: %s", waitErr, truncateLogSnippet(stderr.String(), 400))
	}
	return nil
}

func readFrameHashRawFrames(reader io.Reader, consume func(frame []byte)) error {
	buffered := bufio.NewReaderSize(reader, frameHashGrayscaleBytes*64)
	frame := make([]byte, frameHashGrayscaleBytes)
	for {
		if _, err := io.ReadFull(buffered, frame); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("灰度输出被截断，不是 %d 字节的整数倍", frameHashGrayscaleBytes)
			}
			return err
		}
		consume(frame)
	}
}

func (s *FrameHashService) setCurrent(candidate frameHashCandidate) {
	s.update(func(status *FrameHashStatus) {
		status.CurrentVideoID = candidate.ID
		status.CurrentVideoName = candidate.Name
	})
}

func (s *FrameHashService) recordSkipped() {
	s.update(func(status *FrameHashStatus) { status.Processed++; status.Skipped++ })
}

func (s *FrameHashService) recordSuccess() {
	s.update(func(status *FrameHashStatus) { status.Processed++; status.Succeeded++ })
}

func (s *FrameHashService) recordFailure(candidate frameHashCandidate, err error) {
	s.update(func(status *FrameHashStatus) {
		status.Processed++
		status.Failed++
		if len(status.Failures) < frameHashFailureLimit {
			status.Failures = append(status.Failures, FrameHashFailure{
				VideoID: candidate.ID, Name: candidate.Name, Error: boundedError(err, 500),
			})
		}
	})
}

func (s *FrameHashService) failPreparation(err error) {
	s.mu.Lock()
	s.status.Running = false
	s.status.Preparing = false
	s.status.Completed = true
	s.status.Failed = 1
	s.status.Failures = []FrameHashFailure{{Name: "准备帧哈希回填", Error: boundedError(err, 500)}}
	now := s.now()
	s.status.UpdatedAt = &now
	cancel := s.cancel
	s.cancel = nil
	status, emitter := cloneFrameHashStatus(s.status), s.emitter
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	emitFrameHashStatus(emitter, status)
}

func (s *FrameHashService) update(update func(*FrameHashStatus)) {
	s.mu.Lock()
	update(&s.status)
	now := s.now()
	s.status.UpdatedAt = &now
	status, emitter := cloneFrameHashStatus(s.status), s.emitter
	s.mu.Unlock()
	emitFrameHashStatus(emitter, status)
}

func (s *FrameHashService) finish(cancelled bool) {
	s.mu.Lock()
	s.status.Running = false
	s.status.Preparing = false
	s.status.Cancelled = cancelled
	s.status.Completed = !cancelled
	s.status.CurrentVideoID = 0
	s.status.CurrentVideoName = ""
	s.status.Gate = TaskGateState{}
	now := s.now()
	s.status.UpdatedAt = &now
	cancel := s.cancel
	s.cancel = nil
	status, emitter := cloneFrameHashStatus(s.status), s.emitter
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	emitFrameHashStatus(emitter, status)
}

func cloneFrameHashStatus(status FrameHashStatus) FrameHashStatus {
	status.Failures = append([]FrameHashFailure(nil), status.Failures...)
	return status
}

func emitFrameHashStatus(emitter func(FrameHashStatus), status FrameHashStatus) {
	if emitter != nil {
		emitter(status)
	}
}
