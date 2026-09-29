package services

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"video-master/models"
	"video-master/services/subtitleparser"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// 预编译正则表达式（避免每次调用重复编译）
var (
	srtBlockSplitter     = regexp.MustCompile(`\r?\n\r?\n`)
	langDetectRe         = regexp.MustCompile(`auto-detected language:\s*(\w+)`)
	langDetectReFallback = regexp.MustCompile(`language:\s*(\w+)`)
)

// DeepL HTTP 客户端（带超时控制）
var deeplHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

type SubtitleService struct {
	ctx               context.Context
	mu                sync.Mutex
	transcriptionSlot chan struct{}
	pending           map[uint]*pendingSubtitleArtifact
	taskQueue         *subtitleTaskQueue
	// glossaryResolver 解析视频的术语生效集（D-033）。可替换是为了让翻译流程的
	// 测试不必先造出作品集与库表。
	glossaryResolver func(videoID uint, targetLanguage string) ([]GlossaryTerm, error)
	// translationCancels 按视频登记正在跑的「翻译已有字幕」任务，让前端能中途叫停。
	// 字幕生成走队列自带取消，这条路径不进队列，所以自己记一份。
	translationCancels map[uint][]*translationCancelEntry
	// eventSink 让测试截获本服务新增的事件（subtitle-failed 等）；为空时经 Wails runtime 发往前端。
	eventSink func(name string, payload any)

	// 引擎状态缓存（D-PC22）：每次探测都要起两个 Python 进程 import 运行时，60 秒内复用上一次的结果，
	// 准备前后失效。engineStatusProbe / now 是测试接缝。
	engineStatusMu    sync.Mutex
	engineStatusCache []SubtitleEngineStatus
	engineStatusAt    time.Time
	engineStatusProbe func() []SubtitleEngineStatus
	now               func() time.Time

	// prepareCancel 非空表示有一轮引擎准备正在进行，CancelEnginePreparation 调它杀掉 pip 等子进程。
	prepareMu     sync.Mutex
	prepareCancel context.CancelFunc

	BaseDir  string
	BinDir   string
	ModelDir string
}

type subtitleLocalOnlyASRContextKey struct{}

func withSubtitleLocalOnlyASR(ctx context.Context) context.Context {
	return context.WithValue(ctx, subtitleLocalOnlyASRContextKey{}, true)
}

func isSubtitleLocalOnlyASR(ctx context.Context) bool {
	value, _ := ctx.Value(subtitleLocalOnlyASRContextKey{}).(bool)
	return value
}

type pendingSubtitleArtifact struct {
	VideoID      uint
	VideoPath    string
	SRTPath      string
	Engine       SubtitleEngine
	SourceLang   string
	DetectedLang string
	// TranslationApplied 表示双语译文已经合并进临时文件（收尾时替换失败留下的现场），
	// 强制重试时不得再翻译一次。
	TranslationApplied bool
}

func NewSubtitleService(baseDir string) *SubtitleService {
	service := &SubtitleService{
		BaseDir:           baseDir,
		BinDir:            filepath.Join(baseDir, "bin"),
		ModelDir:          filepath.Join(baseDir, "models"),
		transcriptionSlot: make(chan struct{}, 1),
		glossaryResolver:  NewTranslationGlossaryService().ResolveForVideo,
	}
	service.taskQueue = service.newSubtitleTaskQueue()
	return service
}

// subtitleWriter 返回以应用数据目录为根的字幕写入器。写入器无状态，每次现取，
// BaseDir 为空（数据目录没解析出来）时，覆盖已有字幕会报错而不是写相对路径。
func (s *SubtitleService) subtitleWriter() *SubtitleFileWriter {
	return NewSubtitleFileWriter(s.BaseDir)
}

func (s *SubtitleService) SetContext(ctx context.Context) {
	s.ctx = ctx
	// 字幕索引的后台同步是包级的（「无字幕」视图、字幕搜索都经过它），完成事件由接了
	// Wails 上下文的字幕服务代发（D-PC23）；不必另外接线。
	setSubtitleIndexSyncEmitter(func(status SubtitleIndexSyncStatus) {
		s.emitEvent(subtitleIndexSyncedEvent, status)
	})
}

// emitEvent 发一条本服务的前端事件；测试经 eventSink 截获。
func (s *SubtitleService) emitEvent(name string, payload any) {
	s.mu.Lock()
	sink := s.eventSink
	s.mu.Unlock()
	if sink != nil {
		sink(name, payload)
		return
	}
	if s.ctx != nil && s.ctx.Err() == nil {
		wailsRuntime.EventsEmit(s.ctx, name, payload)
	}
}

func (s *SubtitleService) newSubtitleTaskQueue() *subtitleTaskQueue {
	return s.newPersistentSubtitleTaskQueue(func(ctx context.Context, task *subtitleQueueTask) (*SubtitleGenerateResult, error) {
		return s.executeSubtitleTask(ctx, task.TaskID, task.Request, task.VideoPath, task.Options)
	})
}

// newPersistentSubtitleTaskQueue 建一个把生命周期落到 subtitle_jobs 的队列（D-PC20）。
// 测试用它换掉执行器，同时保留落库。
func (s *SubtitleService) newPersistentSubtitleTaskQueue(executor subtitleTaskExecutor) *subtitleTaskQueue {
	queue := newSubtitleTaskQueue(s.emitSubtitleQueueSnapshot, executor)
	queue.jobs = &subtitleJobDBStore{service: s}
	return queue
}

func (s *SubtitleService) subtitleTaskQueue() *subtitleTaskQueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.taskQueue == nil {
		s.taskQueue = s.newSubtitleTaskQueue()
	}
	return s.taskQueue
}

func (s *SubtitleService) GetSubtitleQueueState() SubtitleQueueSnapshot {
	return s.subtitleTaskQueue().snapshot()
}

func (s *SubtitleService) CancelSubtitleTask(taskID uint) error {
	return s.subtitleTaskQueue().cancelTask(taskID)
}

func (s *SubtitleService) cachePendingSubtitle(artifact *pendingSubtitleArtifact) {
	if artifact == nil || artifact.VideoID == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = make(map[uint]*pendingSubtitleArtifact)
	}
	s.pending[artifact.VideoID] = artifact
}

func (s *SubtitleService) consumePendingSubtitle(videoID uint) *pendingSubtitleArtifact {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return nil
	}
	artifact := s.pending[videoID]
	delete(s.pending, videoID)
	return artifact
}

func (s *SubtitleService) peekPendingSubtitle(videoID uint) *pendingSubtitleArtifact {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return nil
	}
	if artifact := s.pending[videoID]; artifact != nil {
		copy := *artifact
		return &copy
	}
	return nil
}

// CancelGeneration 取消正在进行的字幕生成任务
func (s *SubtitleService) CancelGeneration() {
	if err := s.subtitleTaskQueue().cancelActiveTask(); err == nil {
		log.Printf("[Subtitle] generation cancelled by user")
	} else if !errors.Is(err, ErrSubtitleTaskNotFound) {
		log.Printf("[Subtitle] cancel active generation failed: %v", err)
	}
}

// QuiesceGeneration cancels queued and active subtitle writes and waits until
// the active task has released its database/file resources.
// 字幕索引的后台同步同样写库，一并取消并等它退出（恢复备份会关掉旧连接）。
func (s *SubtitleService) QuiesceGeneration() {
	s.subtitleTaskQueue().cancelAllAndWait()
	stopSubtitleIndexSyncAndWait()
}

// subtitleEngineStatusTTL 是引擎状态缓存的有效期（D-PC22）。
const subtitleEngineStatusTTL = 60 * time.Second

// GetEngineStatuses 返回各字幕引擎的可用性；60 秒内复用上一次探测的结果，准备前后失效。
func (s *SubtitleService) GetEngineStatuses() ([]SubtitleEngineStatus, error) {
	s.engineStatusMu.Lock()
	defer s.engineStatusMu.Unlock()
	now := s.clock()
	if s.engineStatusCache != nil && now.Sub(s.engineStatusAt) < subtitleEngineStatusTTL {
		return append([]SubtitleEngineStatus(nil), s.engineStatusCache...), nil
	}
	probe := s.engineStatusProbe
	if probe == nil {
		probe = s.probeEngineStatuses
	}
	statuses := probe()
	s.engineStatusCache, s.engineStatusAt = append([]SubtitleEngineStatus(nil), statuses...), now
	return statuses, nil
}

func (s *SubtitleService) probeEngineStatuses() []SubtitleEngineStatus {
	return []SubtitleEngineStatus{
		s.getWhisperXStatus(),
		s.getQwenStatus(),
	}
}

func (s *SubtitleService) invalidateEngineStatusCache() {
	s.engineStatusMu.Lock()
	s.engineStatusCache = nil
	s.engineStatusMu.Unlock()
}

func (s *SubtitleService) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *SubtitleService) getWhisperXStatus() SubtitleEngineStatus {
	ffmpegReady := s.findBinary("ffmpeg") != ""
	installed := s.isWhisperXInstalled()
	status := SubtitleEngineStatus{
		Engine:         SubtitleEngineWhisperX,
		DisplayName:    "WhisperX",
		Supported:      true,
		Available:      ffmpegReady && installed,
		NeedsPrepare:   false,
		PrepareMode:    SubtitlePrepareModeNone,
		ReasonCode:     SubtitleReasonReady,
		SourceLangMode: SubtitleSourceLangModeShared,
		ReasonMessage:  "WhisperX 已就绪",
		PrepareHint:    "",
	}

	if runtime.GOOS == "darwin" {
		status.PrepareMode = SubtitlePrepareModeManaged
		if !ffmpegReady {
			status.Available = false
			status.NeedsPrepare = true
			status.ReasonCode = SubtitleReasonMissingFFmpeg
			status.ReasonMessage = "缺少 FFmpeg，可通过应用自动准备。"
			status.PrepareHint = "准备 WhisperX 时会同时检查并安装 FFmpeg。"
			return status
		}
		if !installed {
			status.Available = false
			status.NeedsPrepare = true
			status.ReasonCode = SubtitleReasonMissingRuntime
			status.ReasonMessage = "缺少 WhisperX 运行时，可通过应用自动准备。"
			status.PrepareHint = "应用会创建私有 WhisperX sidecar 与模型缓存。"
			return status
		}
		return status
	}

	if !ffmpegReady {
		status.Available = false
		status.PrepareMode = SubtitlePrepareModeManualPrereq
		status.ReasonCode = SubtitleReasonManualPrereq
		status.ReasonMessage = "当前平台需要先手动安装 FFmpeg。"
		status.PrepareHint = "安装 FFmpeg 后，应用仍可继续准备 WhisperX 私有运行时。"
		return status
	}
	if !installed {
		status.Available = false
		status.NeedsPrepare = true
		status.PrepareMode = SubtitlePrepareModeManaged
		status.ReasonCode = SubtitleReasonMissingRuntime
		status.ReasonMessage = "共享前置条件已满足，可继续准备 WhisperX 运行时。"
		status.PrepareHint = "当前平台不会自动安装系统依赖，只会准备应用私有运行时。"
		return status
	}
	return status
}

func (s *SubtitleService) getQwenStatus() SubtitleEngineStatus {
	status := SubtitleEngineStatus{
		Engine:         SubtitleEngineQwen,
		DisplayName:    "Qwen3-ASR-1.7B",
		Supported:      false,
		Available:      false,
		NeedsPrepare:   false,
		PrepareMode:    SubtitlePrepareModeUnsupported,
		ReasonCode:     SubtitleReasonUnsupportedPlatform,
		SourceLangMode: SubtitleSourceLangModeIgnored,
		ReasonMessage:  "Qwen v1 当前仅在 macOS 上提供。",
		PrepareHint:    "",
	}

	if runtime.GOOS != "darwin" {
		return status
	}
	if runtime.GOARCH != "arm64" {
		status.ReasonMessage = "Qwen v1 当前仅默认启用 macOS arm64；amd64 需后续探测通过后再启用。"
		return status
	}

	ffmpegReady := s.findBinary("ffmpeg") != ""
	installed := s.isQwenInstalled()
	status.Supported = true
	status.PrepareMode = SubtitlePrepareModeManaged
	status.ReasonCode = SubtitleReasonReady
	status.ReasonMessage = "Qwen 已就绪"
	status.PrepareHint = ""
	status.Available = ffmpegReady && installed
	if !ffmpegReady {
		status.Available = false
		status.NeedsPrepare = true
		status.ReasonCode = SubtitleReasonMissingFFmpeg
		status.ReasonMessage = "缺少 FFmpeg，可通过应用自动准备。"
		status.PrepareHint = "准备 Qwen 时会同时检查并安装 FFmpeg。"
		return status
	}
	if !installed {
		status.Available = false
		status.NeedsPrepare = true
		status.ReasonCode = SubtitleReasonMissingRuntime
		status.ReasonMessage = "缺少 Qwen 运行时，可通过应用自动准备。"
		status.PrepareHint = "应用会创建独立的 Qwen ASR sidecar。"
		return status
	}
	return status
}

var (
	// ErrSubtitleEnginePreparing：已有一轮引擎准备在进行（pip 装在同一个虚拟环境里，不能两路并发）。
	ErrSubtitleEnginePreparing = errors.New("字幕引擎正在准备中，请等当前准备完成或先取消")
	// ErrSubtitleEnginePreparationCancelled：用户取消了引擎准备（CancelEnginePreparation）。
	ErrSubtitleEnginePreparationCancelled = errors.New("已取消字幕引擎准备")
)

// PrepareEngine 准备字幕引擎的运行时（D-PC22）：下载解释器、建虚拟环境、pip 安装全部经可取消的
// 子进程执行，CancelEnginePreparation 会杀掉它们；准备结果经桌面通知告知（文案不含失败原因）。
func (s *SubtitleService) PrepareEngine(engine SubtitleEngine) error {
	ctx, finish, err := s.beginEnginePreparation()
	if err != nil {
		return err
	}
	defer finish()
	// 准备前后都让引擎状态缓存失效：准备要按最新状态判断，准备完的结果也要立刻反映到界面。
	s.invalidateEngineStatusCache()
	defer s.invalidateEngineStatusCache()

	statuses, err := s.GetEngineStatuses()
	if err != nil {
		return err
	}
	var status *SubtitleEngineStatus
	for index := range statuses {
		if statuses[index].Engine == engine {
			status = &statuses[index]
			break
		}
	}
	if status == nil {
		return fmt.Errorf("不支持的字幕引擎: %s", engine)
	}
	if !status.Supported {
		return fmt.Errorf("%s", status.ReasonMessage)
	}
	if !status.NeedsPrepare {
		s.emitPrepareComplete(engine)
		return nil
	}

	err = s.runEnginePreparation(ctx, engine, *status)
	if err != nil && ctx.Err() != nil {
		log.Printf("[Subtitle] engine preparation cancelled engine=%s", engine)
		s.emitProgress("prepare", engine, "cancelled", 0, "已取消准备")
		return ErrSubtitleEnginePreparationCancelled
	}
	s.notifyEnginePreparation(status.DisplayName, err)
	if err != nil {
		return err
	}
	s.emitPrepareComplete(engine)
	return nil
}

func (s *SubtitleService) runEnginePreparation(ctx context.Context, engine SubtitleEngine, status SubtitleEngineStatus) error {
	if !status.Available && status.ReasonCode == SubtitleReasonMissingFFmpeg && runtime.GOOS == "darwin" {
		if err := s.downloadFFmpeg(ctx); err != nil {
			return err
		}
	}

	s.emitProgress("prepare", engine, "checking", 0, "准备运行时...")

	switch engine {
	case SubtitleEngineWhisperX:
		if runtime.GOOS != "darwin" && s.findBinary("ffmpeg") == "" {
			return ErrSubtitleDependencyUnsupportedPlatform
		}
		return s.installWhisperXRuntime(ctx)
	case SubtitleEngineQwen:
		return s.installQwenRuntime(ctx)
	default:
		return fmt.Errorf("不支持的字幕引擎: %s", engine)
	}
}

func (s *SubtitleService) emitPrepareComplete(engine SubtitleEngine) {
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, "subtitle-prepare-complete", map[string]interface{}{
			"engine": string(engine),
		})
	}
}

// beginEnginePreparation 登记一轮引擎准备；同一时间只允许一轮。返回的 finish 注销登记并释放 ctx。
func (s *SubtitleService) beginEnginePreparation() (context.Context, func(), error) {
	s.prepareMu.Lock()
	defer s.prepareMu.Unlock()
	if s.prepareCancel != nil {
		return nil, nil, ErrSubtitleEnginePreparing
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.prepareCancel = cancel
	finish := func() {
		s.prepareMu.Lock()
		s.prepareCancel = nil
		s.prepareMu.Unlock()
		cancel()
	}
	return ctx, finish, nil
}

// CancelEnginePreparation 取消正在进行的字幕引擎准备（杀掉 pip 等子进程）。没有进行中的准备时返回 false。
func (s *SubtitleService) CancelEnginePreparation() bool {
	s.prepareMu.Lock()
	cancel := s.prepareCancel
	s.prepareMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// notifyEnginePreparation 把准备结果发成系统通知（D-PC22）。失败原因不进文案：pip 与解释器的输出里
// 常带本机路径（AI-CONTEXT 2.20），原因在设置页与弹窗里看。
func (s *SubtitleService) notifyEnginePreparation(displayName string, err error) {
	notifier := s.subtitleTaskQueue().desktopNotifier()
	if notifier == nil {
		return
	}
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = "字幕引擎"
	}
	if err != nil {
		notifyDesktop(notifier, "字幕引擎准备失败", fmt.Sprintf("%s 未能准备完成，可在设置页「字幕」分区重试", name))
		return
	}
	notifyDesktop(notifier, "字幕引擎已就绪", fmt.Sprintf("%s 已准备完成，可以生成字幕", name))
}

func (s *SubtitleService) CheckDependencies() (map[string]bool, error) {
	statuses, err := s.GetEngineStatuses()
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool)
	result["ffmpeg"] = s.findBinary("ffmpeg") != ""
	for _, status := range statuses {
		if status.Engine == SubtitleEngineWhisperX {
			result["whisper"] = status.Available
			result["model"] = status.Available
		}
	}
	return result, nil
}

// findBinary searches for a binary in: local bin dir, Homebrew paths, system PATH
func (s *SubtitleService) findBinary(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	// 1. Local bin dir
	localPath := filepath.Join(s.BinDir, name)
	if _, err := os.Stat(localPath); err == nil {
		return localPath
	}

	// 2. Common Homebrew paths (macOS .app bundles have minimal PATH)
	if runtime.GOOS == "darwin" {
		brewPaths := []string{
			"/opt/homebrew/bin/" + name, // Apple Silicon
			"/usr/local/bin/" + name,    // Intel Mac
		}
		for _, p := range brewPaths {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}

	// 3. System PATH
	if p, err := exec.LookPath(name); err == nil {
		return p
	}

	return ""
}

func (s *SubtitleService) DownloadDependencies() error {
	return s.PrepareEngine(SubtitleEngineWhisperX)
}

// GenerateSubtitle 把一次字幕生成排进 FIFO 队列并等它结束。任务落 subtitle_jobs（D-PC20）；
// 强制生成接上同一视频待确认的那一行。收尾写回失败不再作为错误返回，而是带
// error_code=subtitle_replace_failed、pending_retained=true 的结果（临时文件已保留，可重试收尾）。
func (s *SubtitleService) GenerateSubtitle(req SubtitleGenerateRequest, videoPath string, options SubtitleGenerateOptions) (*SubtitleGenerateResult, error) {
	options = normalizeSubtitleGenerateOptions(options)
	req.SourceLang = normalizeSubtitleSourceLangForASR(req.SourceLang)
	task := &subtitleQueueTask{
		Request:   req,
		VideoPath: videoPath,
		VideoName: strings.TrimSpace(req.VideoName),
		Options:   options,
	}
	if options.ForceGenerate {
		task.claim = s.needsConfirmationClaim(req, videoPath)
	}
	result, err := s.subtitleTaskQueue().submit(task)
	if mapped := subtitleReplaceFailedResult(req, videoPath, err); mapped != nil {
		return mapped, nil
	}
	return result, err
}

func (s *SubtitleService) executeSubtitleTask(ctx context.Context, taskID uint, req SubtitleGenerateRequest, videoPath string, options SubtitleGenerateOptions) (*SubtitleGenerateResult, error) {
	s.emitGenerateProgress(taskID, req, "checking", 0, "初始化任务...")
	// 目标 .srt 若存在必须是普通文件：尽早报错，不要跑完识别才在写入器里失败。
	if err := ensureSubtitleTargetReplaceable(subtitleparser.SRTPathForVideo(videoPath)); err != nil {
		return nil, err
	}
	if options.ForceGenerate {
		if pending := s.peekPendingSubtitle(req.VideoID); pending != nil &&
			pending.VideoPath == videoPath &&
			pending.Engine == req.Engine &&
			(pending.SourceLang == "" || pending.SourceLang == req.SourceLang) {
			if _, err := os.Stat(pending.SRTPath); err == nil {
				unlockSubtitle := lockSubtitleFile(subtitleparser.SRTPathForVideo(videoPath))
				defer unlockSubtitle()
				s.emitGenerateProgress(taskID, req, "finalizing", 35, "使用上次校验结果强制生成...")
				if pending.TranslationApplied {
					// 上次已把译文合并进临时文件（只是替换失败），重试不能再翻译一遍。
					options.BilingualEnabled = false
				}
				result, err := s.finalizeSubtitleArtifact(ctx, taskID, req, pending.SRTPath, pending.DetectedLang, options)
				if err != nil {
					// 这一轮关掉了双语，finalize 自己报不出「已带译文」；必须沿用登记里的标记，
					// 否则再失败一次就丢了它，下一次重试会把双语字幕再翻译一遍。
					return nil, s.retainPendingAfterReplaceFailure(err, req, videoPath, pending.SRTPath, pending.DetectedLang, pending.TranslationApplied)
				}
				if result.Status == SubtitleResultStatusSuccess {
					s.consumePendingSubtitle(req.VideoID)
				}
				return result, nil
			}
		}
	}

	statuses, err := s.GetEngineStatuses()
	if err != nil {
		return nil, err
	}
	var engineStatus *SubtitleEngineStatus
	for idx := range statuses {
		if statuses[idx].Engine == req.Engine {
			engineStatus = &statuses[idx]
			break
		}
	}
	if engineStatus == nil {
		return nil, fmt.Errorf("不支持的字幕引擎: %s", req.Engine)
	}
	if !engineStatus.Supported {
		return nil, fmt.Errorf("%s", engineStatus.ReasonMessage)
	}
	if !engineStatus.Available {
		return nil, fmt.Errorf("%s", engineStatus.ReasonMessage)
	}

	// Extract audio and transcribe with the shared local-ASR execution slot.
	s.emitGenerateProgress(taskID, req, "extracting-audio", 10, "提取音频...")
	s.emitGenerateProgress(taskID, req, "transcribing", 20, fmt.Sprintf("使用 %s 转写音频...", engineStatus.DisplayName))
	detectedLang, segments, err := s.transcribeVideoLocally(ctx, videoPath, req.Engine, req.SourceLang, options.RecognitionConfig)
	if err != nil {
		if ctx.Err() != nil {
			s.emitCancelled(taskID, req.VideoID, req.Engine, "字幕生成已取消")
			return &SubtitleGenerateResult{Status: SubtitleResultStatusCancelled, VideoID: req.VideoID, Message: "字幕生成已取消"}, nil
		}
		return nil, err
	}

	return s.commitTranscription(ctx, taskID, req, videoPath, detectedLang, segments, options)
}

// commitTranscription 把转写结果落成字幕（D-PC13）：先写到 pending 临时文件，校验通过才
// Replace 到 .srt。幻觉、空结果、取消三条路径下，原 .srt 一个字节都不动。
func (s *SubtitleService) commitTranscription(ctx context.Context, taskID uint, req SubtitleGenerateRequest, videoPath string, detectedLang string, segments []subtitleparser.Segment, options SubtitleGenerateOptions) (*SubtitleGenerateResult, error) {
	s.emitGenerateProgress(taskID, req, "normalizing", 35, "整理转写结果...")
	srtPath := subtitleparser.SRTPathForVideo(videoPath)
	pendingPath := subtitlePendingPath(srtPath)
	unlockSubtitle := lockSubtitleFile(srtPath)
	defer unlockSubtitle()

	if plainTranscriptText(segments, 0) == "" {
		_ = os.Remove(pendingPath)
		return nil, errors.New("语音识别未产生有效字幕，视频可能没有清晰的语音内容")
	}
	if ctx.Err() != nil {
		_ = os.Remove(pendingPath)
		s.emitCancelled(taskID, req.VideoID, req.Engine, "字幕生成已取消")
		return &SubtitleGenerateResult{Status: SubtitleResultStatusCancelled, VideoID: req.VideoID, Message: "字幕生成已取消"}, nil
	}
	if err := writeSRT(pendingPath, segments); err != nil {
		_ = os.Remove(pendingPath)
		return nil, fmt.Errorf("写入字幕失败: %s", subtitleIOReason(err))
	}

	// 后处理：检测幻觉输出（forceGenerate 时跳过）
	if !options.ForceGenerate {
		s.emitGenerateProgress(taskID, req, "validating", 50, "校验字幕输出...")
		if err := s.validateSRT(pendingPath); err != nil {
			var validationErr *SubtitleValidationError
			if ok := errors.As(err, &validationErr); ok {
				// 校验未过：临时文件留给「强制生成」，原字幕不动。
				s.cachePendingSubtitle(&pendingSubtitleArtifact{
					VideoID:      req.VideoID,
					VideoPath:    videoPath,
					SRTPath:      pendingPath,
					Engine:       req.Engine,
					SourceLang:   req.SourceLang,
					DetectedLang: detectedLang,
				})
				return &SubtitleGenerateResult{
					Status:          SubtitleResultStatusValidationFailed,
					VideoID:         req.VideoID,
					Path:            srtPath,
					Message:         validationErr.Message,
					ValidationCode:  validationErr.Code,
					ForceEligible:   validationErr.ForceEligible,
					Engine:          req.Engine,
					SourceLang:      req.SourceLang,
					PendingRetained: true,
				}, nil
			}
			_ = os.Remove(pendingPath)
			return nil, err
		}
	}

	result, err := s.finalizeSubtitleArtifact(ctx, taskID, req, pendingPath, detectedLang, options)
	if err != nil {
		return nil, s.retainPendingAfterReplaceFailure(err, req, videoPath, pendingPath, detectedLang, false)
	}
	s.consumePendingSubtitle(req.VideoID)
	return result, nil
}

// subtitlePendingPath 由最终 .srt 路径得到生成流程使用的临时文件路径：同目录的隐藏文件
// `.<基本名>.cineinsight-pending.srt`。必须同目录（原子替换不能跨文件系统）；必须隐藏，
// 否则 IINA 等播放器会把「<基本名>.*.srt」当成外挂字幕自动加载，用户看到的是未校验的结果。
func subtitlePendingPath(srtPath string) string {
	stem := strings.TrimSuffix(filepath.Base(srtPath), filepath.Ext(srtPath))
	return filepath.Join(filepath.Dir(srtPath), "."+stem+subtitlePendingSuffix)
}

// subtitleFinalPathForPending 是 subtitlePendingPath 的逆运算；输入不是 pending 形态时返回错误，
// 不再静默地把任意路径改成「xxx.srt」。
func subtitleFinalPathForPending(pendingPath string) (string, error) {
	name := filepath.Base(pendingPath)
	stem := strings.TrimSuffix(name, subtitlePendingSuffix)
	if stem == name || !strings.HasPrefix(stem, ".") || len(stem) == 1 {
		return "", errors.New("不是字幕临时文件路径")
	}
	return filepath.Join(filepath.Dir(pendingPath), stem[1:]+".srt"), nil
}

// DiscardPendingSubtitle 放弃校验未过的临时字幕：删除临时文件并清掉登记（D-PC13）；
// 任务中心里这个视频待确认的行一并置为 cancelled（D-PC20）。
func (s *SubtitleService) DiscardPendingSubtitle(videoID uint) error {
	if err := s.discardRegisteredPendingSubtitle(videoID); err != nil {
		return err
	}
	return s.discardNeedsConfirmationJobs(videoID)
}

func (s *SubtitleService) discardRegisteredPendingSubtitle(videoID uint) error {
	artifact := s.consumePendingSubtitle(videoID)
	if artifact == nil {
		return nil
	}
	finalPath, err := subtitleFinalPathForPending(artifact.SRTPath)
	if err != nil {
		return nil
	}
	unlock := lockSubtitleFile(finalPath)
	defer unlock()
	// 别的视频（同目录同名、扩展名不同）还有待确认的行引用这个临时文件时不删（M-7）；
	// 本视频自己的待确认行随后由 discardNeedsConfirmationJobs 逐条结束。
	if db, err := subtitleJobsDB(); err == nil {
		shared, err := subtitlePendingReferencedElsewhere(db, artifact.SRTPath, 0, videoID)
		if err != nil {
			return fmt.Errorf("检查临时字幕的引用失败: %w", err)
		}
		if shared {
			return nil
		}
	}
	if err := os.Remove(artifact.SRTPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除临时字幕失败: %s", subtitleIOReason(err))
	}
	return nil
}

// GenerateTemporaryTranscript performs local ASR without writing an SRT or sending raw audio externally.
func (s *SubtitleService) GenerateTemporaryTranscript(ctx context.Context, video models.Video, charLimit int, recognitionConfig SubtitleRecognitionConfig) (TemporaryTranscriptEvidence, error) {
	statuses, err := s.GetEngineStatuses()
	if err != nil {
		return TemporaryTranscriptEvidence{}, err
	}
	available := make(map[SubtitleEngine]bool, len(statuses))
	for _, status := range statuses {
		available[status.Engine] = status.Available
	}
	engines := []SubtitleEngine{SubtitleEngineWhisperX, SubtitleEngineQwen}
	var failures []string
	localOnlyCtx := withSubtitleLocalOnlyASR(ctx)
	for _, engine := range engines {
		if !available[engine] {
			continue
		}
		detectedLang, segments, transcribeErr := s.transcribeVideoLocally(localOnlyCtx, video.Path, engine, "auto", recognitionConfig)
		if transcribeErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", engine, transcribeErr))
			continue
		}
		text := plainTranscriptText(segments, charLimit)
		if text == "" {
			failures = append(failures, fmt.Sprintf("%s: empty transcript", engine))
			continue
		}
		return TemporaryTranscriptEvidence{Text: text, DetectedLang: detectedLang, Engine: string(engine)}, nil
	}
	if len(failures) > 0 {
		return TemporaryTranscriptEvidence{}, fmt.Errorf("temporary transcript failed: %s", strings.Join(failures, "; "))
	}
	return TemporaryTranscriptEvidence{}, fmt.Errorf("no prepared local subtitle engine is available")
}

func (s *SubtitleService) transcribeVideoLocally(ctx context.Context, videoPath string, engine SubtitleEngine, sourceLang string, config SubtitleRecognitionConfig) (string, []subtitleparser.Segment, error) {
	if err := s.acquireTranscriptionSlot(ctx); err != nil {
		return "", nil, err
	}
	defer s.releaseTranscriptionSlot()
	tempFile, err := os.CreateTemp("", "cineinsight-local-asr-*.wav")
	if err != nil {
		return "", nil, fmt.Errorf("create temporary audio: %w", err)
	}
	tempWav := tempFile.Name()
	if closeErr := tempFile.Close(); closeErr != nil {
		_ = os.Remove(tempWav)
		return "", nil, fmt.Errorf("close temporary audio: %w", closeErr)
	}
	defer os.Remove(tempWav)
	if err := s.extractAudio(ctx, videoPath, tempWav); err != nil {
		return "", nil, err
	}
	switch engine {
	case SubtitleEngineWhisperX:
		return s.transcribeWhisperXWithLang(ctx, tempWav, sourceLang, config)
	case SubtitleEngineQwen:
		return s.transcribeQwenWithLang(ctx, tempWav, sourceLang)
	default:
		return "", nil, fmt.Errorf("不支持的字幕引擎: %s", engine)
	}
}

func (s *SubtitleService) acquireTranscriptionSlot(ctx context.Context) error {
	s.mu.Lock()
	if s.transcriptionSlot == nil {
		s.transcriptionSlot = make(chan struct{}, 1)
	}
	slot := s.transcriptionSlot
	s.mu.Unlock()
	select {
	case slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *SubtitleService) releaseTranscriptionSlot() {
	s.mu.Lock()
	slot := s.transcriptionSlot
	s.mu.Unlock()
	if slot != nil {
		<-slot
	}
}

func plainTranscriptText(segments []subtitleparser.Segment, charLimit int) string {
	var builder strings.Builder
	for _, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(text)
		if charLimit > 0 && len([]rune(builder.String())) >= charLimit {
			break
		}
	}
	return truncateRunes(builder.String(), charLimit)
}

// subtitleReplaceFailedError 表示收尾时原子替换失败：临时文件已按设计保留，等待强制重试。
type subtitleReplaceFailedError struct {
	err                error
	translationApplied bool
}

func (e *subtitleReplaceFailedError) Error() string { return e.err.Error() }
func (e *subtitleReplaceFailedError) Unwrap() error { return e.err }

// retainPendingAfterReplaceFailure 在收尾替换失败后重新登记保留下来的 pending 文件，
// 让「强制生成」可以直接重试收尾，不必重跑识别。其他错误原样返回。
//
// alreadyTranslated 是调用方已知的「临时文件在这一轮之前就带着译文」：重试时双语被关掉，
// 这一轮的 translationApplied 必然为 false，只看它会把标记丢掉。两者取或，已带译文的
// pending 在之后任何一次重试里都不会再翻译。
func (s *SubtitleService) retainPendingAfterReplaceFailure(err error, req SubtitleGenerateRequest, videoPath, pendingPath, detectedLang string, alreadyTranslated bool) error {
	var replaceErr *subtitleReplaceFailedError
	if errors.As(err, &replaceErr) {
		s.cachePendingSubtitle(&pendingSubtitleArtifact{
			VideoID:            req.VideoID,
			VideoPath:          videoPath,
			SRTPath:            pendingPath,
			Engine:             req.Engine,
			SourceLang:         req.SourceLang,
			DetectedLang:       detectedLang,
			TranslationApplied: replaceErr.translationApplied || alreadyTranslated,
		})
	}
	return err
}

// finalizeSubtitleArtifact 收尾一份已写好的 pending 字幕：可选的双语翻译在临时文件上完成，
// 最后经写入器 Replace 到同名 .srt 并删除临时文件（D-PC13）。调用方持有 lockSubtitleFile。
// pendingPath 必须是 subtitlePendingPath 形态；任何非成功出口都会删掉临时文件。
func (s *SubtitleService) finalizeSubtitleArtifact(ctx context.Context, taskID uint, req SubtitleGenerateRequest, pendingPath string, detectedLang string, options SubtitleGenerateOptions) (*SubtitleGenerateResult, error) {
	warnings := []string{}
	translationStatus := ""
	srtPath, err := subtitleFinalPathForPending(pendingPath)
	if err != nil {
		return nil, err
	}
	outputPrefix := strings.TrimSuffix(srtPath, filepath.Ext(srtPath))
	// keepPending 为真时（替换失败）保留临时文件与登记，允许强制重试而不必重跑识别。
	committed, keepPending := false, false
	defer func() {
		if !committed && !keepPending {
			_ = os.Remove(pendingPath)
		}
	}()

	if options.BilingualEnabled && strings.TrimSpace(options.BilingualLang) != "" {
		targetLang := normalizeSubtitleLanguageCode(options.BilingualLang)
		if targetLang == "" {
			targetLang = strings.TrimSpace(options.BilingualLang)
		}
		provider := normalizeSubtitleTranslationProvider(options.TranslationConfig.Provider)
		log.Printf("[Subtitle] bilingual: detected=%s target=%s provider=%s", detectedLang, targetLang, provider)

		if s.isSameLanguage(detectedLang, targetLang) {
			translationStatus = "skipped_same_language"
			log.Printf("[Subtitle] detected language matches target, skipping translation")
		} else {
			translator, err := s.subtitleTranslator(provider, options.TranslationConfig)
			if err != nil {
				translationStatus = "skipped_config_missing"
				warnings = append(warnings, fmt.Sprintf("双语翻译未执行：%v", err))
				log.Printf("[Subtitle] translation config unavailable provider=%s err=%v, keeping original SRT", provider, err)
				goto done
			}
			// 术语生效集按视频所属作品集与目标语言解析一次，整轮翻译共用（D-033、D-PC16）。
			// 只有能吃下术语表的翻译器才去查：DeepL 用不上，也就不该因为一次库读失败
			// 而多出一条它原本没有的失败路径（D-034）。
			var glossary []GlossaryTerm
			if _, injectable := translator.(ContextualTranslator); injectable {
				resolved, resolveErr := s.glossaryResolver(req.VideoID, targetLang)
				if resolveErr != nil {
					translationStatus = "failed"
					warnings = append(warnings, fmt.Sprintf("双语翻译失败，已保留原文字幕：读取术语表失败：%v", resolveErr))
					log.Printf("[Subtitle] resolve translation glossary failed video_id=%d err=%v", req.VideoID, resolveErr)
					goto done
				}
				glossary = resolved
			}
			s.emitGenerateProgress(taskID, req, "translating", 60, subtitleTranslationProgressMessage(provider))

			translatedSrtPath := outputPrefix + "_translated_temp.srt"
			defer os.Remove(translatedSrtPath)

			sourceLang := normalizeSubtitleLanguageCode(detectedLang)
			if sourceLang == "auto" || sourceLang == "unknown" {
				sourceLang = ""
			}
			fallbackCount, translateErr := s.translateSRTWithProgress(ctx, pendingPath, translatedSrtPath, sourceLang, targetLang, translator, glossary, nil)
			if translateErr != nil {
				if ctx.Err() != nil {
					s.emitCancelled(taskID, req.VideoID, req.Engine, "字幕生成已取消")
					return &SubtitleGenerateResult{Status: SubtitleResultStatusCancelled, VideoID: req.VideoID, Message: "字幕生成已取消"}, nil
				}
				translationStatus = "failed"
				warnings = append(warnings, fmt.Sprintf("双语翻译失败，已保留原文字幕：%v", translateErr))
				log.Printf("[Subtitle] subtitle translate failed via %s: %v, keeping original SRT", provider, translateErr)
				goto done
			}
			s.emitGenerateProgress(taskID, req, "merging", 85, "合并双语字幕...")
			if err := s.mergeBilingualSRT(pendingPath, translatedSrtPath, pendingPath); err != nil {
				translationStatus = "failed"
				warnings = append(warnings, fmt.Sprintf("双语字幕合并失败，已保留原文字幕：%v", err))
				log.Printf("[Subtitle] merge failed: %v", err)
			} else {
				translationStatus = "translated"
				// 回退提示只在译文真的写进去之后才说得通：合并失败时那份译文已经丢弃了。
				if fallbackCount > 0 {
					warnings = append(warnings, subtitleFallbackWarning(fallbackCount))
				}
			}
		}
	}

done:
	if ctx.Err() != nil {
		s.emitCancelled(taskID, req.VideoID, req.Engine, "字幕生成已取消")
		return &SubtitleGenerateResult{Status: SubtitleResultStatusCancelled, VideoID: req.VideoID, Message: "字幕生成已取消"}, nil
	}
	content, err := os.ReadFile(pendingPath)
	if err != nil {
		return nil, fmt.Errorf("读取待写入字幕失败: %s", subtitleIOReason(err))
	}
	writeResult, err := s.subtitleWriter().Replace(ctx, req.VideoID, srtPath, content)
	if err != nil {
		if ctx.Err() != nil {
			s.emitCancelled(taskID, req.VideoID, req.Engine, "字幕生成已取消")
			return &SubtitleGenerateResult{Status: SubtitleResultStatusCancelled, VideoID: req.VideoID, Message: "字幕生成已取消"}, nil
		}
		// 替换失败不是内容问题：保留临时文件并重新登记，用户可以「强制生成」重试收尾。
		// 登记由持有视频路径的调用方完成（retainPendingAfterReplaceFailure）。
		keepPending = true
		return nil, &subtitleReplaceFailedError{
			err: fmt.Errorf("写入字幕失败: %w", err), translationApplied: translationStatus == "translated",
		}
	}
	if writeResult.BackupID != "" {
		log.Printf("[Subtitle] replaced existing subtitle video_id=%d backup_id=%s", req.VideoID, writeResult.BackupID)
	}
	_ = os.Remove(pendingPath)
	committed = true

	if err := indexSubtitleFileForVideoID(req.VideoID, srtPath); err != nil {
		log.Printf("[Subtitle] index subtitle failed videoID=%d path=%s err=%v", req.VideoID, srtPath, err)
		warnings = append(warnings, fmt.Sprintf("字幕索引更新失败：%v", err))
	}

	s.emitGenerateProgress(taskID, req, "finalizing", 100, "完成收尾")

	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, "subtitle-success", map[string]interface{}{
			"taskID":             taskID,
			"videoID":            req.VideoID,
			"engine":             string(req.Engine),
			"path":               srtPath,
			"warnings":           warnings,
			"translation_status": translationStatus,
			"backup_id":          writeResult.BackupID,
		})
	}
	return &SubtitleGenerateResult{
		Status:            SubtitleResultStatusSuccess,
		VideoID:           req.VideoID,
		Path:              srtPath,
		Warnings:          warnings,
		TranslationStatus: translationStatus,
	}, nil
}

func (s *SubtitleService) extractAudio(ctx context.Context, videoPath, outputPath string) error {
	ffmpegBin := s.findBinary("ffmpeg")
	if ffmpegBin == "" {
		return fmt.Errorf("未找到 FFmpeg，请重新安装依赖")
	}

	log.Printf("[Subtitle] extractAudio: ffmpeg=%s input=%s output=%s\n", ffmpegBin, videoPath, outputPath)
	cmd := exec.CommandContext(ctx, ffmpegBin, "-i", videoPath, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", outputPath, "-y")
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("字幕生成已取消")
		}
		detail := string(output)
		log.Printf("[Subtitle] ffmpeg error: %s\n%s\n", err, detail)
		// User-friendly error messages
		if strings.Contains(detail, "moov atom not found") || strings.Contains(detail, "Invalid data found") {
			return fmt.Errorf("视频文件异常或已损坏，无法提取音频")
		}
		if strings.Contains(detail, "No such file") || strings.Contains(detail, "does not exist") {
			return fmt.Errorf("视频文件不存在")
		}
		if strings.Contains(detail, "Permission denied") {
			return fmt.Errorf("没有权限访问视频文件")
		}
		return fmt.Errorf("音频提取失败，请检查视频文件是否有效")
	}
	return nil
}

// findWhisperBin 查找 whisper 可执行文件
func (s *SubtitleService) findWhisperBin() string {
	whisperBin := s.findBinary("whisper-cli")
	if whisperBin == "" {
		whisperBin = s.findBinary("whisper-cpp")
	}
	if whisperBin == "" {
		whisperBin = s.findBinary("main")
	}
	return whisperBin
}

// transcribeCLIWithLang 转录音频并返回检测到的语言代码
func (s *SubtitleService) transcribeCLIWithLang(ctx context.Context, wavPath, outputPrefix, sourceLang string) (string, error) {
	whisperBin := s.findWhisperBin()
	if whisperBin == "" {
		return "", fmt.Errorf("未找到 Whisper，请重新安装依赖")
	}

	modelPath := filepath.Join(s.ModelDir, "ggml-medium.bin")

	log.Printf("[Subtitle] transcribeCLIWithLang: whisper=%s model=%s input=%s output=%s lang=%s\n", whisperBin, modelPath, wavPath, outputPrefix, sourceLang)

	cmd := exec.CommandContext(ctx, whisperBin,
		"-m", modelPath,
		"-f", wavPath,
		"-osrt",
		"-of", outputPrefix,
		"-l", sourceLang,
		"--no-fallback",
		"-et", "2.4",
		"-lpt", "-1.0",
		"-bo", "5",
		"-bs", "5",
	)

	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	// 从输出中提取检测到的语言（支持多种 whisper 输出格式）
	detectedLang := "en" // 默认英文
	if matches := langDetectRe.FindStringSubmatch(outputStr); len(matches) > 1 {
		detectedLang = strings.ToLower(matches[1])
		log.Printf("[Subtitle] detected language: %s", detectedLang)
	} else if matches := langDetectReFallback.FindStringSubmatch(outputStr); len(matches) > 1 {
		detectedLang = strings.ToLower(matches[1])
		log.Printf("[Subtitle] detected language (fallback): %s", detectedLang)
	} else {
		log.Printf("[Subtitle] WARNING: could not detect language from whisper output, defaulting to 'en'")
	}

	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("字幕生成已取消")
		}
		log.Printf("[Subtitle] whisper error: %s\n%s\n", err, outputStr)
		if strings.Contains(outputStr, "failed to open") || strings.Contains(outputStr, "no such file") {
			return "", fmt.Errorf("模型文件缺失，请重新安装依赖")
		}
		return "", fmt.Errorf("语音识别失败，请确保视频包含有效音频")
	}
	return detectedLang, nil
}

// validateSRT 检测 SRT 文件是否存在幻觉输出（大量重复文本）
func (s *SubtitleService) validateSRT(srtPath string) error {
	f, err := os.Open(srtPath)
	if err != nil {
		return fmt.Errorf("字幕文件生成失败")
	}
	defer f.Close()

	lineCounts := make(map[string]int)
	totalLines := 0
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.Contains(line, "-->") {
			continue
		}
		isNum := true
		for _, c := range line {
			if c < '0' || c > '9' {
				isNum = false
				break
			}
		}
		if isNum {
			continue
		}
		totalLines++
		lineCounts[line]++
	}

	if totalLines == 0 {
		log.Printf("[Subtitle] validateSRT: 字幕文件无有效文本行")
		return fmt.Errorf("语音识别未产生有效字幕，视频可能没有清晰的语音内容")
	}

	maxCount := 0
	maxLine := ""
	for line, count := range lineCounts {
		if count > maxCount {
			maxCount = count
			maxLine = line
		}
	}

	repeatRatio := float64(maxCount) / float64(totalLines)
	log.Printf("[Subtitle] validateSRT: totalLines=%d maxCount=%d ratio=%.2f maxLine=%q", totalLines, maxCount, repeatRatio, maxLine)

	if repeatRatio > 0.85 {
		return &SubtitleValidationError{
			Code:          SubtitleValidationCodeHallucinationDetected,
			Message:       fmt.Sprintf("检测到异常输出（疑似模型幻觉），字幕内容重复率 %.0f%%。可选择强制生成保留结果", repeatRatio*100),
			ForceEligible: true,
		}
	}

	segments, err := subtitleparser.ParseFile(srtPath)
	if err == nil && hasTokenizedTimingFailure(segments) {
		return &SubtitleValidationError{
			Code:          SubtitleValidationCodeHallucinationDetected,
			Message:       "检测到异常逐字字幕（大量单字或零时长片段），可选择强制生成保留结果",
			ForceEligible: true,
		}
	}
	if err != nil {
		log.Printf("[Subtitle] validateSRT: parse structured segments failed: %v", err)
	}

	return nil
}

func hasTokenizedTimingFailure(segments []subtitleparser.Segment) bool {
	if len(segments) < 30 {
		return false
	}

	zeroDurationCount := 0
	shortTextCount := 0
	startTimes := make(map[int64]struct{}, len(segments))
	for _, segment := range segments {
		if segment.EndTimeMs <= segment.StartTimeMs {
			zeroDurationCount++
		}
		text := strings.TrimSpace(strings.ReplaceAll(segment.Text, "\n", ""))
		if utf8.RuneCountInString(text) <= 2 {
			shortTextCount++
		}
		startTimes[segment.StartTimeMs] = struct{}{}
	}

	total := float64(len(segments))
	zeroRatio := float64(zeroDurationCount) / total
	shortRatio := float64(shortTextCount) / total
	uniqueStartRatio := float64(len(startTimes)) / total
	log.Printf("[Subtitle] validateSRT timing: segments=%d zeroRatio=%.2f shortRatio=%.2f uniqueStartRatio=%.2f", len(segments), zeroRatio, shortRatio, uniqueStartRatio)

	return shortRatio > 0.85 && (zeroRatio > 0.50 || uniqueStartRatio < 0.20)
}

// isSameLanguage 判断 whisper 检测到的语言与用户目标语言是否相同
func (s *SubtitleService) isSameLanguage(detected, target string) bool {
	return normalizeSubtitleLanguageCode(detected) == normalizeSubtitleLanguageCode(target)
}

func normalizeSubtitleGenerateOptions(options SubtitleGenerateOptions) SubtitleGenerateOptions {
	options.BilingualLang = strings.TrimSpace(options.BilingualLang)
	options.TranslationConfig.Provider = string(normalizeSubtitleTranslationProvider(options.TranslationConfig.Provider))
	options.RecognitionConfig.WhisperXModel = normalizeSubtitleWhisperXModel(options.RecognitionConfig.WhisperXModel)
	options.RecognitionConfig.WhisperXBatchSize = normalizeSubtitleWhisperXBatchSize(options.RecognitionConfig.WhisperXBatchSize)
	options.RecognitionConfig.WhisperXComputeType = normalizeSubtitleWhisperXComputeType(options.RecognitionConfig.WhisperXComputeType)
	return options
}

func normalizeSubtitleSourceLangForASR(value string) string {
	normalized := normalizeSubtitleLanguageCode(value)
	if normalized == "" {
		return "auto"
	}
	return normalized
}

// SRTEntry 表示一条 SRT 字幕
type SRTEntry struct {
	Index string
	Time  string
	Text  string
}

// parseSRTEntries 解析 SRT 文件为条目列表
func parseSRTEntries(srtPath string) ([]SRTEntry, error) {
	data, err := os.ReadFile(srtPath)
	if err != nil {
		return nil, err
	}
	// 经编码识别再切块：BOM 不再污染第一条的序号，非 UTF-8 也不会被当成乱码解析（D-PC14）。
	text, _, err := subtitleparser.DecodeSubtitleBytes(data)
	if err != nil {
		return nil, err
	}

	var entries []SRTEntry
	blocks := srtBlockSplitter.Split(strings.TrimSpace(text), -1)

	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 3 {
			continue
		}
		entry := SRTEntry{
			Index: strings.TrimSpace(lines[0]),
			Time:  strings.TrimSpace(lines[1]),
			Text:  strings.TrimSpace(strings.Join(lines[2:], "\n")),
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// translateDeepL 调用 DeepL API 翻译文本
func (s *SubtitleService) translateDeepL(ctx context.Context, texts []string, sourceLang, targetLang, apiKey string) ([]string, error) {
	// DeepL 目标语言代码需要大写
	targetUpper := strings.ToUpper(targetLang)
	// 中文需要特殊处理：DeepL 使用 ZH-HANS
	if targetUpper == "ZH" {
		targetUpper = "ZH-HANS"
	}

	// 构建请求体
	type DeepLRequest struct {
		Text       []string `json:"text"`
		TargetLang string   `json:"target_lang"`
		SourceLang string   `json:"source_lang,omitempty"`
	}

	reqBody := DeepLRequest{
		Text:       texts,
		TargetLang: targetUpper,
	}
	if sourceLang != "" {
		reqBody.SourceLang = strings.ToUpper(sourceLang)
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("构建翻译请求失败: %v", err)
	}

	// 判断是免费版还是付费版 API
	apiURL := "https://api-free.deepl.com/v2/translate"
	if !strings.HasSuffix(apiKey, ":fx") {
		apiURL = "https://api.deepl.com/v2/translate"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "DeepL-Auth-Key "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := deeplHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("翻译请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == 403 {
			return nil, fmt.Errorf("DeepL API Key 无效或已过期")
		}
		if resp.StatusCode == 456 {
			return nil, fmt.Errorf("DeepL 翻译额度已用完")
		}
		return nil, fmt.Errorf("DeepL API 返回 %d: %s", resp.StatusCode, string(respBody))
	}

	type DeepLTranslation struct {
		Text string `json:"text"`
	}
	type DeepLResponse struct {
		Translations []DeepLTranslation `json:"translations"`
	}

	var deeplResp DeepLResponse
	if err := json.NewDecoder(resp.Body).Decode(&deeplResp); err != nil {
		return nil, fmt.Errorf("解析翻译响应失败: %v", err)
	}

	results := make([]string, len(deeplResp.Translations))
	for i, t := range deeplResp.Translations {
		results[i] = t.Text
	}
	return results, nil
}

// translateSRTWithProgress 翻译 SRT 文件中的所有文本行，每翻完一批回报一次进度，
// 并返回有多少条字幕因为译文为空而回退成了原文。onBatch 可以为 nil；空译文回退是
// 手动翻译与字幕生成自动双语共同的行为，两条路径都走这里。
func (s *SubtitleService) translateSRTWithProgress(ctx context.Context, inputPath, outputPath, sourceLang, targetLang string, translator SubtitleTranslator, glossary []GlossaryTerm, onBatch func(done, total int)) (int, error) {
	if translator == nil {
		return 0, fmt.Errorf("subtitle translator is nil")
	}
	entries, err := parseSRTEntries(inputPath)
	if err != nil {
		return 0, fmt.Errorf("读取字幕文件失败: %v", err)
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("字幕文件为空")
	}

	// 收集文本行（DeepL 一次最多翻译 50 条）
	batchSize := 50
	var translatedEntries []SRTEntry
	// 滑动窗口：上一批尾部若干条 (原文, 译文) 作为只读上文，首批为空（D-035）。
	contextual, _ := translator.(ContextualTranslator)
	var preceding []ContextPair
	fallbackCount := 0

	for i := 0; i < len(entries); i += batchSize {
		end := i + batchSize
		if end > len(entries) {
			end = len(entries)
		}
		batch := entries[i:end]

		texts := make([]string, len(batch))
		for j, e := range batch {
			texts[j] = e.Text
		}

		translated, err := translateSubtitleBatch(ctx, translator, contextual, TranslationRequest{
			Texts:            texts,
			SourceLang:       sourceLang,
			TargetLang:       targetLang,
			Glossary:         glossary,
			PrecedingContext: preceding,
		})
		if err != nil {
			return fallbackCount, err
		}
		if len(translated) != len(batch) {
			return fallbackCount, fmt.Errorf("字幕翻译返回 %d 条，期望 %d 条", len(translated), len(batch))
		}

		// 空译文回退必须赶在喂给上文窗口之前，否则「原文 → 空」会作为示范样本进入下一批的提示词。
		var batchFallbacks int
		translated, batchFallbacks = applyTranslationFallback(texts, translated)
		fallbackCount += batchFallbacks

		preceding = trailingContextPairs(texts, translated, subtitleTranslationContextWindow)
		if onBatch != nil {
			onBatch(end, len(entries))
		}

		for j, e := range batch {
			translatedEntries = append(translatedEntries, SRTEntry{
				Index: e.Index,
				Time:  e.Time,
				Text:  translated[j],
			})
		}
	}

	// 写入翻译后的 SRT
	var buf strings.Builder
	for _, e := range translatedEntries {
		buf.WriteString(e.Index + "\n")
		buf.WriteString(e.Time + "\n")
		buf.WriteString(e.Text + "\n\n")
	}

	return fallbackCount, os.WriteFile(outputPath, []byte(buf.String()), 0644)
}

// applyTranslationFallback 是空译文回退的唯一实现（D-PC16），字幕生成的自动双语、
// 已有字幕的文件翻译、工作台选区重译三处共用。
//
// 空译文写出去就是一个只有序号和时间轴的块，parseSRTEntries 与编辑器解析器都会把它整条
// 丢掉：仅译文模式下这条字幕被永久删除，双语合并则因为两边条数对不上而让其后所有译文错位。
// 保留原文既保住了条目，也保住了对齐。sources 与 translated 等长；返回回退后的新切片与回退条数。
func applyTranslationFallback(sources, translated []string) ([]string, int) {
	result := make([]string, len(translated))
	fallbackCount := 0
	for index, translation := range translated {
		text := strings.TrimSpace(translation)
		if text == "" && index < len(sources) {
			text = sources[index]
			fallbackCount++
		}
		result[index] = text
	}
	return result, fallbackCount
}

// subtitleFallbackWarning 是「译文为空、已保留原文」的统一措辞：手动翻译与生成流程
// 的自动双语走同一套回退，就不该有两种口径。
func subtitleFallbackWarning(count int) string {
	return fmt.Sprintf("有 %d 条字幕没有返回译文，这些条目保留了原文。", count)
}

// mergeBilingualSRT 合并两个 SRT 文件为双语 SRT（每条字幕上行原文、下行翻译）
func (s *SubtitleService) mergeBilingualSRT(originalPath, translatedPath, outputPath string) error {
	merged, err := s.buildBilingualSRT(originalPath, translatedPath)
	if err != nil {
		return err
	}
	return writeFileAtomically(outputPath, merged, 0644)
}

// buildBilingualSRT 生成双语 SRT 的内容而不落盘，让调用方决定怎么写（生成流程写临时文件，
// 文件翻译经写入器 Replace）。
func (s *SubtitleService) buildBilingualSRT(originalPath, translatedPath string) ([]byte, error) {
	origEntries, err := parseSRTEntries(originalPath)
	if err != nil {
		return nil, fmt.Errorf("读取原文字幕失败: %v", err)
	}
	transEntries, err := parseSRTEntries(translatedPath)
	if err != nil {
		return nil, fmt.Errorf("读取翻译字幕失败: %v", err)
	}

	var buf strings.Builder
	maxLen := len(origEntries)
	if len(transEntries) > maxLen {
		maxLen = len(transEntries)
	}

	for i := 0; i < maxLen; i++ {
		idx := fmt.Sprintf("%d", i+1)
		var timeLine, origText, transText string

		if i < len(origEntries) {
			timeLine = origEntries[i].Time
			origText = origEntries[i].Text
		}
		if i < len(transEntries) {
			if timeLine == "" {
				timeLine = transEntries[i].Time
			}
			transText = transEntries[i].Text
		}

		buf.WriteString(idx + "\n")
		buf.WriteString(timeLine + "\n")
		// 原文在上，翻译在下。译文与原文相同时只写一行：空译文会被回退成原文
		// （见 translateSRTWithProgress），照双行写就成了同一句话叠两遍。
		if origText == transText {
			transText = ""
		}
		if origText != "" && transText != "" {
			buf.WriteString(origText + "\n" + transText + "\n\n")
		} else if origText != "" {
			buf.WriteString(origText + "\n\n")
		} else {
			buf.WriteString(transText + "\n\n")
		}
	}

	log.Printf("[Subtitle] mergeBilingualSRT: merged %d entries", maxLen)
	return []byte(buf.String()), nil
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("创建临时字幕文件失败: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return fmt.Errorf("设置临时字幕权限失败: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("写入临时字幕文件失败: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("同步临时字幕文件失败: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("关闭临时字幕文件失败: %w", err)
	}
	if err := replaceSubtitleFileAtomically(tempPath, path); err != nil {
		return fmt.Errorf("替换字幕文件失败: %w", err)
	}
	_ = syncSubtitleParentDirectory(filepath.Dir(path))
	return nil
}

// ErrSubtitleDependencyUnsupportedPlatform 表示当前平台没有自动下载字幕依赖的实现。
// 这不是临时失败，重试不会改变结果，只能由用户手工安装依赖后再用。
var ErrSubtitleDependencyUnsupportedPlatform = errors.New("当前平台不支持自动下载字幕依赖，请手工安装 Whisper 运行时与 FFmpeg 后重试")

// Download helpers
func (s *SubtitleService) downloadFFmpeg(ctx context.Context) error {
	if runtime.GOOS == "darwin" {
		return s.installBrewPackage(ctx, "ffmpeg", "FFmpeg")
	}
	return ErrSubtitleDependencyUnsupportedPlatform
}

func (s *SubtitleService) installWhisperMac() error {
	return s.installBrewPackage(context.Background(), "whisper-cpp", "Whisper")
}

// installBrewPackage installs a package via Homebrew with progress feedback.
// ctx 取消（引擎准备被取消）时杀掉 brew 子进程。
func (s *SubtitleService) installBrewPackage(ctx context.Context, pkg, displayName string) error {
	brewPath, err := exec.LookPath("brew")
	if err != nil {
		// Also check common brew paths
		for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
			if _, err := os.Stat(p); err == nil {
				brewPath = p
				break
			}
		}
		if brewPath == "" {
			return fmt.Errorf("未找到 Homebrew，请先安装 Homebrew (https://brew.sh)，然后重试")
		}
	}

	s.emitProgress("prepare", SubtitleEngineWhisperX, "preparing-runtime", 0, fmt.Sprintf("正在通过 Homebrew 安装 %s...", displayName))

	cmd := preparationCommand(ctx, brewPath, "install", pkg)
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("brew install %s 失败: %s\n%s", pkg, err, string(output))
	}

	s.emitProgress("prepare", SubtitleEngineWhisperX, "preparing-runtime", 100, fmt.Sprintf("%s 安装完成", displayName))
	return nil
}

func (s *SubtitleService) downloadModel() error {
	if err := os.MkdirAll(s.ModelDir, 0755); err != nil {
		return err
	}
	// 多语言 medium 模型（~1.5GB），支持自动语言检测
	url := "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-medium.bin"
	dest := filepath.Join(s.ModelDir, "ggml-medium.bin")
	s.emitProgress("prepare", SubtitleEngineWhisperX, "downloading-model", 0, "Downloading Model (~1.5GB)...")
	return s.downloadFile(url, dest, "model")
}

func (s *SubtitleService) downloadFile(url, dest, component string) error {
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	size := resp.ContentLength
	tracker := &ProgressTracker{
		Reader: resp.Body, Total: size,
		OnProgress: func(c int64) {
			if size > 0 {
				p := int(float64(c) / float64(size) * 100)
				s.emitProgress("prepare", SubtitleEngineWhisperX, "downloading-model", p, fmt.Sprintf("Downloading %d%%", p))
			}
		},
	}
	_, err = io.Copy(out, tracker)
	return err
}

func (s *SubtitleService) emitProgress(action string, engine SubtitleEngine, phase string, pct int, msg string) {
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, "subtitle-progress", map[string]interface{}{
			"action":      action,
			"engine":      string(engine),
			"phase":       phase,
			"percent":     pct,
			"message":     msg,
			"cancellable": action == "generate" || action == "prepare",
			"jobScope":    "single_active_v1",
		})
	}
}

func (s *SubtitleService) emitGenerateProgress(taskID uint, req SubtitleGenerateRequest, phase string, pct int, msg string) {
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, "subtitle-progress", map[string]interface{}{
			"taskID":      taskID,
			"videoID":     req.VideoID,
			"action":      "generate",
			"engine":      string(req.Engine),
			"phase":       phase,
			"percent":     pct,
			"message":     msg,
			"cancellable": true,
			"jobScope":    "queued_task_v1",
		})
	}
}

func (s *SubtitleService) emitSubtitleQueueSnapshot(snapshot SubtitleQueueSnapshot) {
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, "subtitle-queue", snapshot)
	}
}

func (s *SubtitleService) emitCancelled(taskID uint, videoID uint, engine SubtitleEngine, msg string) {
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, "subtitle-cancelled", map[string]interface{}{
			"taskID":  taskID,
			"videoID": videoID,
			"engine":  string(engine),
			"message": msg,
		})
	}
}

type ProgressTracker struct {
	io.Reader
	Total, Current int64
	OnProgress     func(int64)
}

func (pt *ProgressTracker) Read(p []byte) (n int, err error) {
	n, err = pt.Reader.Read(p)
	pt.Current += int64(n)
	if pt.OnProgress != nil {
		pt.OnProgress(pt.Current)
	}
	return
}

func unzipFile(src, destDir, targetPrefix string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		name := filepath.Base(f.Name)
		if strings.HasPrefix(name, targetPrefix) {
			fpath := filepath.Join(destDir, name)
			if f.FileInfo().IsDir() {
				continue
			}
			out, err := os.Create(fpath)
			if err != nil {
				return err
			}
			rc, err := f.Open()
			if err != nil {
				out.Close()
				return err
			}
			io.Copy(out, rc)
			out.Close()
			rc.Close()
			return nil
		}
	}
	return nil
}
