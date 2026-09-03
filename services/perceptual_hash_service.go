package services

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const perceptualHashDistanceThreshold = 7

const perceptualHashFailureWriteTimeout = 2 * time.Second

type PerceptualHashFailure struct {
	VideoID uint   `json:"video_id"`
	Name    string `json:"name"`
	Error   string `json:"error"`
}

type PerceptualHashStatus struct {
	Running        bool                    `json:"running"`
	Cancelled      bool                    `json:"cancelled"`
	Completed      bool                    `json:"completed"`
	Total          int                     `json:"total"`
	Processed      int                     `json:"processed"`
	Succeeded      int                     `json:"succeeded"`
	Skipped        int                     `json:"skipped"`
	Failed         int                     `json:"failed"`
	CurrentVideoID uint                    `json:"current_video_id"`
	StartedAt      *time.Time              `json:"started_at" ts_type:"string"`
	UpdatedAt      *time.Time              `json:"updated_at" ts_type:"string"`
	Failures       []PerceptualHashFailure `json:"failures"`
	// Gate 是空闲门状态（D-032）：自动路径被挡住时这里说明原因，显式启动恒为零值。
	Gate TaskGateState `json:"gate"`
}

type perceptualFrameRunner interface {
	Frame(context.Context, string, float64) ([]byte, error)
}

type ffmpegPerceptualFrameRunner struct{}

func (ffmpegPerceptualFrameRunner) Frame(ctx context.Context, path string, second float64) ([]byte, error) {
	ffmpegBin, err := findThumbnailFFmpeg()
	if err != nil {
		return nil, err
	}
	// -fflags +discardcorrupt 容忍损坏的 NAL 单元/数据包（老编码器或部分下载的文件常见），
	// 让 ffmpeg 尽量从可解码的 GOP 中恢复出一帧，而不是直接报错退出。
	ss := strconv.FormatFloat(second, 'f', 3, 64)
	frame, stderrText, err := extractPerceptualFrame(ctx, ffmpegBin, []string{"-ss", ss, "-i", path})
	if err != nil {
		return nil, err
	}
	if len(frame) == 0 && second > 1 {
		// 输入侧 seek（-ss 在 -i 之前）依赖 mp4 关键帧索引；索引不完整或文件尾部
		// 码流损坏（部分下载）时，目标时间点之后可能解不出任何帧。回退到更早的
		// 时间点再试一次——对感知哈希而言，近似位置的帧足够用于近重复判断。
		frame, stderrText, err = extractPerceptualFrame(ctx, ffmpegBin, []string{"-ss", strconv.FormatFloat(second*0.5, 'f', 3, 64), "-i", path})
		if err != nil {
			return nil, err
		}
	}
	if len(frame) != 72 {
		return nil, fmt.Errorf("ffmpeg returned %d grayscale bytes, want 72: %s", len(frame), truncateLogSnippet(stderrText, 400))
	}
	return frame, nil
}

func extractPerceptualFrame(ctx context.Context, ffmpegBin string, seekArgs []string) ([]byte, string, error) {
	args := append([]string{"-v", "warning", "-fflags", "+discardcorrupt"}, seekArgs...)
	args = append(args, "-frames:v", "1", "-vf", "scale=9:8:flags=lanczos,format=gray", "-f", "rawvideo", "pipe:1")
	command := exec.CommandContext(ctx, ffmpegBin, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, stderr.String(), fmt.Errorf("ffmpeg frame extraction failed: %w: %s", err, truncateLogSnippet(stderr.String(), 400))
	}
	return stdout.Bytes(), stderr.String(), nil
}

type PerceptualHashService struct {
	runner    perceptualFrameRunner
	now       func() time.Time
	mu        sync.Mutex
	stopMu    sync.Mutex
	status    PerceptualHashStatus
	cancel    context.CancelFunc
	worker    sync.WaitGroup
	emitter   func(PerceptualHashStatus)
	stopping  bool
	registry  *BackgroundTaskRegistry
	pauseHook TaskPauseHook
}

func NewPerceptualHashService() *PerceptualHashService {
	return &PerceptualHashService{runner: ffmpegPerceptualFrameRunner{}, now: time.Now}
}

func (s *PerceptualHashService) SetEventEmitter(emitter func(PerceptualHashStatus)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

// SetBackgroundTaskRegistry 接入后台任务登记表（D-014）。
func (s *PerceptualHashService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

func (s *PerceptualHashService) waitForPauseHook(ctx context.Context) error {
	s.mu.Lock()
	hook := s.pauseHook
	s.mu.Unlock()
	if hook == nil {
		return nil
	}
	// 钩子在服务锁之外调用：它会阻塞很久，持锁等待会连 Status/Cancel 一起冻住。
	return hook.Wait(ctx, s.setGateState)
}

func (s *PerceptualHashService) setGateState(state TaskGateState) {
	s.update(func(status *PerceptualHashStatus) { status.Gate = state })
}

// Start 是显式启动路径：同一把锁下摘掉当前这一轮的项间检查点（D-030）。
func (s *PerceptualHashService) Start(parent context.Context) (PerceptualHashStatus, error) {
	return s.start(parent, nil)
}

// StartWithPauseHook 是自动路径专用：装钩子与翻 Running 在同一把锁里完成；
// 已经在跑的那一轮不补装钩子（它可能是用户显式启动的）。
func (s *PerceptualHashService) StartWithPauseHook(parent context.Context, hook TaskPauseHook) (PerceptualHashStatus, error) {
	return s.start(parent, hook)
}

func (s *PerceptualHashService) start(parent context.Context, hook TaskPauseHook) (PerceptualHashStatus, error) {
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	releasing := clearReplacedPauseHook(&s.pauseHook, hook)
	if s.stopping {
		s.mu.Unlock()
		releaseTaskPauseHook(releasing)
		return PerceptualHashStatus{}, errors.New("感知哈希任务正在停止")
	}
	if s.status.Running {
		status := clonePerceptualHashStatus(s.status)
		s.mu.Unlock()
		releaseTaskPauseHook(releasing)
		return status, nil
	}
	var videos []models.Video
	if err := database.DB.WithContext(parent).Select("id", "name", "path", "duration").Order("id ASC").Find(&videos).Error; err != nil {
		s.mu.Unlock()
		releaseTaskPauseHook(releasing)
		return PerceptualHashStatus{}, err
	}
	s.pauseHook = hook
	now := s.now()
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.status = PerceptualHashStatus{Running: true, Total: len(videos), StartedAt: &now, UpdatedAt: &now, Failures: []PerceptualHashFailure{}}
	status, emitter := clonePerceptualHashStatus(s.status), s.emitter
	registry := s.registry
	s.worker.Add(1)
	s.mu.Unlock()
	releaseTaskPauseHook(releasing)
	registry.Begin(BackgroundTaskPerceptualHash)
	emitPerceptualHashStatus(emitter, status)
	go s.run(ctx, videos, registry)
	return status, nil
}

func (s *PerceptualHashService) Status() PerceptualHashStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clonePerceptualHashStatus(s.status)
}

func (s *PerceptualHashService) Cancel() error {
	s.mu.Lock()
	cancel, running := s.cancel, s.status.Running
	s.mu.Unlock()
	if !running || cancel == nil {
		return errors.New("感知哈希任务未运行")
	}
	cancel()
	return nil
}

func (s *PerceptualHashService) StopAndWait() {
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

func (s *PerceptualHashService) run(ctx context.Context, videos []models.Video, registry *BackgroundTaskRegistry) {
	defer s.worker.Done()
	defer registry.End(BackgroundTaskPerceptualHash)
	for _, video := range videos {
		if ctx.Err() != nil {
			break
		}
		if err := s.waitForPauseHook(ctx); err != nil {
			break
		}
		s.update(func(status *PerceptualHashStatus) { status.CurrentVideoID = video.ID })
		current, err := perceptualHashCurrent(video)
		if err == nil && current {
			s.update(func(status *PerceptualHashStatus) { status.Processed++; status.Skipped++ })
			continue
		}
		if err := s.Refresh(ctx, video); err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				break
			}
			s.update(func(status *PerceptualHashStatus) {
				status.Processed++
				status.Failed++
				if len(status.Failures) < 50 {
					status.Failures = append(status.Failures, PerceptualHashFailure{VideoID: video.ID, Name: video.Name, Error: boundedError(err, 500)})
				}
			})
			continue
		}
		s.update(func(status *PerceptualHashStatus) { status.Processed++; status.Succeeded++ })
	}
	s.finish(ctx.Err() != nil)
}

func perceptualHashCurrent(video models.Video) (bool, error) {
	info, err := os.Stat(video.Path)
	if err != nil || !info.Mode().IsRegular() {
		return false, err
	}
	var row models.VideoPerceptualHash
	if err := database.DB.First(&row, "video_id = ?", video.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return row.SourceSize == info.Size() && row.SourceModTimeNS == info.ModTime().UnixNano() && row.HashEarly != "" && row.HashMiddle != "" && row.HashLate != "", nil
}

func (s *PerceptualHashService) Refresh(ctx context.Context, video models.Video) error {
	info, err := os.Stat(video.Path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("视频路径不是普通文件")
	}
	seconds := perceptualSampleSeconds(video.Duration)
	hashes := make([]string, 0, len(seconds))
	for _, second := range seconds {
		frame, err := s.runner.Frame(ctx, video.Path, second)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.recordFailure(ctx, video.ID, info, err)
			return err
		}
		hash, err := perceptualDifferenceHash(frame)
		if err != nil {
			s.recordFailure(ctx, video.ID, info, err)
			return err
		}
		hashes = append(hashes, hash)
	}
	row := models.VideoPerceptualHash{
		VideoID: video.ID, SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(),
		HashEarly: hashes[0], HashMiddle: hashes[1], HashLate: hashes[2], ComputedAt: s.now(), LastError: "",
	}
	return database.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "video_id"}}, UpdateAll: true,
	}).Create(&row).Error
}

func (s *PerceptualHashService) recordFailure(parent context.Context, videoID uint, info os.FileInfo, operationErr error) {
	message := boundedError(operationErr, 1000)
	row := models.VideoPerceptualHash{VideoID: videoID, SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(), ComputedAt: s.now(), LastError: message}
	ctx, cancel := context.WithTimeout(parent, perceptualHashFailureWriteTimeout)
	defer cancel()
	_ = database.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "video_id"}}, UpdateAll: true}).Create(&row).Error
}

func perceptualSampleSeconds(duration float64) []float64 {
	if duration <= 0 {
		return []float64{0, 1, 2}
	}
	return []float64{duration * 0.15, duration * 0.5, duration * 0.85}
}

func perceptualDifferenceHash(grayscale []byte) (string, error) {
	if len(grayscale) != 72 {
		return "", fmt.Errorf("grayscale frame has %d bytes, want 72", len(grayscale))
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
	encoded := make([]byte, 8)
	for index := 7; index >= 0; index-- {
		encoded[index] = byte(value)
		value >>= 8
	}
	return hex.EncodeToString(encoded), nil
}

func perceptualHashDistance(left, right string) (int, error) {
	leftBytes, err := hex.DecodeString(left)
	if err != nil || len(leftBytes) != 8 {
		return 0, errors.New("invalid perceptual hash")
	}
	rightBytes, err := hex.DecodeString(right)
	if err != nil || len(rightBytes) != 8 {
		return 0, errors.New("invalid perceptual hash")
	}
	distance := 0
	for index := range leftBytes {
		distance += bits.OnesCount8(leftBytes[index] ^ rightBytes[index])
	}
	return distance, nil
}

func perceptualRowsMatch(left, right models.VideoPerceptualHash) bool {
	total := 0
	for _, pair := range [][2]string{{left.HashEarly, right.HashEarly}, {left.HashMiddle, right.HashMiddle}, {left.HashLate, right.HashLate}} {
		distance, err := perceptualHashDistance(pair[0], pair[1])
		if err != nil || distance > perceptualHashDistanceThreshold*2 {
			return false
		}
		total += distance
	}
	return total <= perceptualHashDistanceThreshold*3
}

func (s *PerceptualHashService) update(update func(*PerceptualHashStatus)) {
	s.mu.Lock()
	update(&s.status)
	now := s.now()
	s.status.UpdatedAt = &now
	status, emitter := clonePerceptualHashStatus(s.status), s.emitter
	s.mu.Unlock()
	emitPerceptualHashStatus(emitter, status)
}

func (s *PerceptualHashService) finish(cancelled bool) {
	s.mu.Lock()
	s.status.Running = false
	s.status.Cancelled = cancelled
	s.status.Completed = true
	s.status.CurrentVideoID = 0
	s.status.Gate = TaskGateState{}
	now := s.now()
	s.status.UpdatedAt = &now
	s.cancel = nil
	status, emitter := clonePerceptualHashStatus(s.status), s.emitter
	s.mu.Unlock()
	emitPerceptualHashStatus(emitter, status)
}

func clonePerceptualHashStatus(status PerceptualHashStatus) PerceptualHashStatus {
	status.Failures = append([]PerceptualHashFailure(nil), status.Failures...)
	return status
}

func emitPerceptualHashStatus(emitter func(PerceptualHashStatus), status PerceptualHashStatus) {
	if emitter != nil {
		emitter(status)
	}
}

func boundedError(err error, limit int) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > limit {
		return message[:limit]
	}
	return message
}
