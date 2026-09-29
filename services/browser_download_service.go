package services

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 浏览器插件推过来的下载任务队列（D-B04、D-B05）。
//
// 两条口径在这里定死：
//
//   - 队列自成一档并发，不进空闲门、也不占 MediaWorkSlot。空闲门只挡自动触发的
//     任务，而这些任务是用户在浏览器里点出来的；`-c copy` 是网络与 IO 密集而非
//     CPU 密集，排进重媒体槽会被超分、转封装那类长任务饿死。
//   - 落盘之后不自己写库，调既有扫描把下载目录追平（D-B05）。缩略图、技术信息
//     因此都走正常路径产生，不会出现"从这条路进来的视频少一半元数据"。
var (
	ErrBrowserDownloadDirectoryUnset = errors.New("还没有设置下载目录，请先在设置页选一个")
	ErrBrowserDownloadInvalidRequest = errors.New("下载请求不合法")
	ErrBrowserDownloadQueueFull      = errors.New("下载队列已满，等前面的任务跑完再试")
	// ErrBrowserDownloadNotInScanRoots 是「文件已保存但没有入库」的唯一原因句。界面在前面加
	// 「文件已保存，但没有入库：」，这里只给原因，不带路径（MEDIA-09、G-3）。
	ErrBrowserDownloadNotInScanRoots = errors.New("下载目录不在片库扫描目录里")
)

const (
	browserDownloadMaxQueue    = 100
	browserDownloadMaxTasks    = 200
	browserDownloadPartSuffix  = ".part"
	browserDownloadStateQueued = "queued"
	browserDownloadStateRun    = "running"
	browserDownloadStateImport = "importing"
	browserDownloadStateDone   = "done"
	browserDownloadStateFailed = "failed"
	browserDownloadStateCancel = "canceled"
	// browserDownloadStateInterrupted 是应用退出或崩溃时没跑完的任务（D-PC21）。请求头只在
	// 内存里，重启后续不上，只能回浏览器重新推送。
	browserDownloadStateInterrupted = "interrupted"

	// browserDownloadErrorMaxRunes 是 error 落盘前的长度上限（详细设计 §5.4）。
	browserDownloadErrorMaxRunes = 500
	// browserDownloadHistoryLimit 是 ListDownloadTasks 从表里带出的历史条数上限。
	browserDownloadHistoryLimit = 200
	// browserDownloadPersistedTerminalLimit 是 browser_download_tasks 里保留的终态行数上限（M-8）：
	// 每次写入终态之后按 created_at 倒序裁掉更早的，表不会随使用无限长。
	browserDownloadPersistedTerminalLimit = 500
	browserDownloadTrimBatch              = 500
)

// 入库状态（BrowserDownloadTask.ImportStatus，D-PC25）。空串表示还没到入库这一步或没有入库服务。
const (
	BrowserDownloadImportImported       = "imported"
	BrowserDownloadImportNotInScanRoots = "not_in_scan_roots"
	BrowserDownloadImportFailed         = "import_failed"
)

// 下载任务动作（重试、入库、打开位置）的结果码（G-3）。not_in_scan_roots / import_failed
// 与入库状态同名。
const (
	BrowserDownloadCodeOK                   = "ok"
	BrowserDownloadCodeTaskNotFound         = "task_not_found"
	BrowserDownloadCodeRetryRequiresBrowser = "retry_requires_browser"
	BrowserDownloadCodeNotRetryable         = "not_retryable"
	BrowserDownloadCodeQueueFull            = "queue_full"
	BrowserDownloadCodeNotFinished          = "not_finished"
	BrowserDownloadCodeFileMissing          = "file_missing"
	BrowserDownloadCodeRevealFailed         = "reveal_failed"
	BrowserDownloadCodeServiceUnavailable   = "service_unavailable"
	// 「加入扫描目录」前的预检（M-5）：目录在扫描黑名单里（加进去也扫不到）、目录包含已有的
	// 扫描目录（加进去会互相嵌套）、目录已不存在。
	BrowserDownloadCodeDirectoryExcluded = "directory_excluded"
	BrowserDownloadCodeDirectoryNested   = "directory_nested"
	BrowserDownloadCodeDirectoryMissing  = "directory_missing"
)

// BrowserDownloadTask 是对外（桥接与设置页）暴露的任务快照。
type BrowserDownloadTask struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Filename   string `json:"filename"`
	OutputPath string `json:"output_path"`
	// Directory 是这次下载实际使用的下载目录（派发时现读的设置）。
	Directory string `json:"directory"`
	PageURL   string `json:"page_url"`
	State     string `json:"state"`
	// VideoID 是入库之后对应的片库记录。界面靠它取缩略图——几个任务并排时
	// 光看文件名分不清谁是谁。没入库时为 0。
	VideoID uint `json:"video_id"`
	// 只有"已处理时长"，没有总时长：HLS 的总时长要额外探一次才知道，当前没做这一步。
	// 与其留一个永远是 0 的 TotalSeconds 让界面拿去算出一个假的百分比，不如不给。
	ProcessedSeconds float64 `json:"processed_seconds"`
	BytesWritten     int64   `json:"bytes_written"`
	// Error 是下载失败的原因，已经过 sanitizeBrowserDownloadError 清洗（内存与落盘同一份）。
	Error       string `json:"error"`
	ImportError string `json:"import_error"`
	// ImportStatus 取 BrowserDownloadImport* 之一；not_in_scan_roots 时界面给出「加入扫描目录 /
	// 重新入库 / 打开所在目录」三个动作。
	ImportStatus string `json:"import_status"`
	// Retryable 为 true 表示内存里仍有这次请求的规格（含请求头），同一会话内可以直接重试。
	Retryable  bool  `json:"retryable"`
	CreatedAt  int64 `json:"created_at"`
	UpdatedAt  int64 `json:"updated_at"`
	FinishedAt int64 `json:"finished_at"`
}

// BrowserDownloadActionResult 是下载任务动作的结果。业务上的「做不了」走 Code，不走 error。
type BrowserDownloadActionResult struct {
	Code    string               `json:"code"`
	Message string               `json:"message,omitempty"`
	Task    *BrowserDownloadTask `json:"task,omitempty"`
}

// BrowserDownloadSettings 是队列每次调度时现读的设置。
type BrowserDownloadSettings struct {
	Directory   string
	Concurrency int
}

// BrowserDownloadDeps 把队列对外部世界的依赖收在一处，单测可以整组替换。
type BrowserDownloadDeps struct {
	// Settings 每次调度现读：用户改了目录或并发，下一个任务立刻按新的来。
	Settings func() (BrowserDownloadSettings, error)
	// ImportDirectory 把下载目录追平进片库（D-B05），确认这一次下载的文件确实进了库，
	// 并返回它在库里的 ID（界面用它取缩略图）。为空则跳过入库。
	ImportDirectory func(directory, outputPath string) (uint, error)
	// FFmpegPath 解析 ffmpeg 可执行文件。
	FFmpegPath func() (string, error)
	// Tasks 是后台任务登记表，可为 nil。
	Tasks *BackgroundTaskRegistry
	// Now 供测试注入时钟。
	Now func() time.Time
	// Reveal 在系统文件管理器里定位文件，默认 revealPath；测试注入替身，免得真去开 Finder。
	Reveal func(path string) error
}

type browserDownloadEntry struct {
	task BrowserDownloadTask
	// 归一化后的请求随任务一起存：排队等一会儿再派发时，Referer / User-Agent /
	// Cookie 必须还在。从任务快照重建拿不回这些头，取流会直接 403。
	// 它只留在内存里，也不进 BrowserDownloadTask，因此不会随任务列表出现在界面上。
	request *browserDownloadNormalized
	cancel  context.CancelFunc
}

type BrowserDownloadService struct {
	deps BrowserDownloadDeps

	mu      sync.Mutex
	entries map[string]*browserDownloadEntry
	queue   []string
	running int
	seq     int64

	baseCtx context.Context
	wg      sync.WaitGroup

	// emitMu 串行化"取快照 + 投递"，避免两次并发变更把更旧的快照后发出去。
	// 回调一律在 mu 之外调用。
	emitMu   sync.Mutex
	emit     func([]BrowserDownloadTask)
	lastEmit time.Time

	// store 是 browser_download_tasks 的落库连接（D-PC21），在 mu 下读写；为 nil 时不落库。
	// persistMu 串行化落库：每次写都在锁内现取内存快照，最后一次写入因此总是内存里的最新状态。
	// 锁序固定为 persistMu → mu。
	store     func() *gorm.DB
	persistMu sync.Mutex

	// history 是表里历史任务的快照（已现算入库状态），推送事件直接用它（M-2）：进度事件
	// 逐行触发，若每次都在 emitMu 下查库、逐个 os.Stat 产物，一个慢盘就能把所有下载的进度
	// 推送一起卡住。快照只在任务落到终态、显式列举（ListDownloadTasks）与入库动作之后刷新；
	// historyMu 只护这份快照本身，historyRefreshMu 串行化刷新（查库与 Stat 都在它下面、不在
	// emitMu 与 mu 下）。
	historyMu        sync.Mutex
	history          []BrowserDownloadTask
	historyLoaded    bool
	historyRefreshMu sync.Mutex
}

// SetEventEmitter 注入任务变化的推送口。进度是逐行解析出来的，变化非常频繁，
// 因此只有状态迁移会立刻推送，纯进度更新按 500ms 节流——界面要的是"在动"，
// 不是每一行 ffmpeg 输出。
func (s *BrowserDownloadService) SetEventEmitter(emit func([]BrowserDownloadTask)) {
	if s == nil {
		return
	}
	s.emitMu.Lock()
	s.emit = emit
	s.emitMu.Unlock()
}

func (s *BrowserDownloadService) notify(force bool) {
	if s == nil {
		return
	}
	s.emitMu.Lock()
	hasEmitter := s.emit != nil
	s.emitMu.Unlock()
	if !hasEmitter {
		return
	}
	// 还没有历史快照（第一次推送早于任何一次列举）就先在锁外读一次：emitMu 下只拼内存快照。
	if !s.historyReady() {
		s.refreshHistory()
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	if s.emit == nil {
		return
	}
	now := s.deps.Now()
	if !force && now.Sub(s.lastEmit) < 500*time.Millisecond {
		return
	}
	s.lastEmit = now
	// 推送与 ListDownloadTasks 同口径（含表里的历史）：下载页拿事件整体替换列表，
	// 只推内存任务的话，重启前的历史会在第一次事件后消失。历史取快照，不查库不 Stat（M-2）。
	s.emit(mergeBrowserDownloadTasks(s.ListTasks(), s.cachedHistory()))
}

func NewBrowserDownloadService(deps BrowserDownloadDeps) *BrowserDownloadService {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.FFmpegPath == nil {
		deps.FFmpegPath = findBrowserDownloadFFmpeg
	}
	if deps.Reveal == nil {
		deps.Reveal = revealPath
	}
	return &BrowserDownloadService{
		deps:    deps,
		entries: make(map[string]*browserDownloadEntry),
		baseCtx: context.Background(),
	}
}

// SetStore 装上 browser_download_tasks 的落库连接（D-PC21）。db 每次现取：切换或恢复数据库
// 之后 database.DB 会换实例。没装时任务只在内存里（单测与数据库不可用时）。
func (s *BrowserDownloadService) SetStore(db func() *gorm.DB) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.store = db
	s.mu.Unlock()
	// 换了库，旧的历史快照不再作数，下一次推送或列举时重读。
	s.historyMu.Lock()
	s.history, s.historyLoaded = nil, false
	s.historyMu.Unlock()
}

func (s *BrowserDownloadService) db() *gorm.DB {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if store == nil {
		return nil
	}
	return store()
}

// Start 绑定生命周期上下文：应用退出时正在跑的 ffmpeg 一起收掉。
func (s *BrowserDownloadService) Start(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	s.mu.Lock()
	s.baseCtx = ctx
	s.mu.Unlock()
}

// Wait 等所有在跑的任务收尾，退出路径与测试用。
func (s *BrowserDownloadService) Wait() {
	if s == nil {
		return
	}
	s.wg.Wait()
}

func (s *BrowserDownloadService) Enqueue(request BrowserDownloadRequest) (BrowserDownloadTask, error) {
	if s == nil {
		return BrowserDownloadTask{}, ErrBrowserDownloadInvalidRequest
	}
	normalized, err := normalizeBrowserDownloadRequest(request)
	if err != nil {
		return BrowserDownloadTask{}, err
	}
	settings, err := s.settings()
	if err != nil {
		return BrowserDownloadTask{}, err
	}
	if strings.TrimSpace(settings.Directory) == "" {
		return BrowserDownloadTask{}, ErrBrowserDownloadDirectoryUnset
	}

	s.mu.Lock()
	if len(s.queue) >= browserDownloadMaxQueue {
		s.mu.Unlock()
		return BrowserDownloadTask{}, ErrBrowserDownloadQueueFull
	}
	s.seq++
	now := s.deps.Now()
	id := fmt.Sprintf("bd%d-%d", now.UnixMilli(), s.seq)
	entry := &browserDownloadEntry{request: normalized, task: BrowserDownloadTask{
		ID:        id,
		URL:       normalized.URL,
		Kind:      normalized.Kind,
		Title:     normalized.Title,
		PageURL:   normalized.PageURL,
		State:     browserDownloadStateQueued,
		CreatedAt: now.UnixMilli(),
		UpdatedAt: now.UnixMilli(),
	}}
	s.entries[id] = entry
	s.queue = append(s.queue, id)
	s.trimLocked()
	snapshot := entry.task
	s.mu.Unlock()

	s.persist(id)
	s.notify(true)
	s.pump()
	return snapshot, nil
}

// ListTasks 只返回内存里的任务（本次会话），桥接的 GET /bridge/v1/downloads 用它。
func (s *BrowserDownloadService) ListTasks() []BrowserDownloadTask {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks := make([]BrowserDownloadTask, 0, len(s.entries))
	for _, entry := range s.entries {
		task := entry.task
		task.Retryable = browserDownloadRetryableLocked(entry)
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt > tasks[j].CreatedAt })
	return tasks
}

func browserDownloadRetryableLocked(entry *browserDownloadEntry) bool {
	if entry.request == nil {
		return false
	}
	return entry.task.State == browserDownloadStateFailed || entry.task.State == browserDownloadStateCancel
}

func browserDownloadTerminal(state string) bool {
	switch state {
	case browserDownloadStateDone, browserDownloadStateFailed, browserDownloadStateCancel, browserDownloadStateInterrupted:
		return true
	}
	return false
}

// setBrowserDownloadStateLocked 统一改状态：更新时间与完成时间一起改，终态写 finished_at、非终态清空。
func setBrowserDownloadStateLocked(task *BrowserDownloadTask, state string, nowMilli int64) {
	task.State = state
	task.UpdatedAt = nowMilli
	if browserDownloadTerminal(state) {
		task.FinishedAt = nowMilli
	} else {
		task.FinishedAt = 0
	}
}

func (s *BrowserDownloadService) CancelTask(id string) error {
	if s == nil {
		return errors.New("下载服务未启用")
	}
	s.mu.Lock()
	entry, ok := s.entries[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("没有这个任务：%s", id)
	}
	cancel := entry.cancel
	queued := entry.task.State == browserDownloadStateQueued
	if queued {
		setBrowserDownloadStateLocked(&entry.task, browserDownloadStateCancel, s.deps.Now().UnixMilli())
		s.removeFromQueueLocked(id)
	}
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if queued {
		s.persist(id)
		s.refreshHistory()
	}
	s.notify(true)
	return nil
}

// pump 在有空位时把队首任务派出去。每次现读并发上限，用户改了立刻生效。
func (s *BrowserDownloadService) pump() {
	for {
		settings, err := s.settings()
		if err != nil {
			log.Printf("browser download: 读取设置失败 %v", err)
			return
		}
		limit := normalizeBrowserDownloadConcurrency(settings.Concurrency)

		s.mu.Lock()
		if s.running >= limit || len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		id := s.queue[0]
		s.queue = s.queue[1:]
		entry, ok := s.entries[id]
		if !ok || entry.task.State != browserDownloadStateQueued {
			s.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(s.baseCtx)
		entry.cancel = cancel
		entry.task.Directory = cleanBrowserDownloadDirectory(settings.Directory)
		setBrowserDownloadStateLocked(&entry.task, browserDownloadStateRun, s.deps.Now().UnixMilli())
		s.running++
		s.mu.Unlock()

		// 先落「running」再起 goroutine：同一任务后续的写入都在它之后发生。
		s.persist(id)
		s.wg.Add(1)
		go func(taskID string, taskCtx context.Context, cancelFn context.CancelFunc) {
			defer s.wg.Done()
			defer cancelFn()
			s.deps.Tasks.Begin(BackgroundTaskBrowserDownload)
			defer s.deps.Tasks.End(BackgroundTaskBrowserDownload)
			s.run(taskCtx, taskID, settings)
			s.mu.Lock()
			s.running--
			s.mu.Unlock()
			s.pump()
		}(id, ctx, cancel)
	}
}

func (s *BrowserDownloadService) run(ctx context.Context, id string, settings BrowserDownloadSettings) {
	request, ok := s.requestOf(id)
	if !ok {
		return
	}

	binary, err := s.deps.FFmpegPath()
	if err != nil {
		s.fail(id, err.Error())
		return
	}

	outputPath, partPath, err := reserveBrowserDownloadPath(settings.Directory, request.Title, request.VariantLabel, request.OutputExtension)
	if err != nil {
		s.fail(id, err.Error())
		return
	}
	s.update(id, func(task *BrowserDownloadTask) {
		task.Filename = filepath.Base(outputPath)
		task.OutputPath = outputPath
	})

	runErr := s.runFFmpeg(ctx, binary, request, partPath, id)
	if runErr != nil {
		// 只清 .part：最终文件在成功改名之前根本没有被创建过。
		_ = os.Remove(partPath)
		if ctx.Err() != nil {
			s.finishCanceled(id)
			return
		}
		s.fail(id, runErr.Error())
		return
	}

	finalPath, err := finalizeBrowserDownloadPath(partPath, outputPath)
	if err != nil {
		_ = os.Remove(partPath)
		s.fail(id, err.Error())
		return
	}
	// 实际落盘的名字可能与下载开始时预期的不同（那个名字在下载期间被占了），
	// 界面要显示真正落盘的那个。
	s.update(id, func(task *BrowserDownloadTask) {
		task.Filename = filepath.Base(finalPath)
		task.OutputPath = finalPath
	})

	s.update(id, func(task *BrowserDownloadTask) { task.State = browserDownloadStateImport })
	videoID, importErr := s.importDirectory(settings.Directory, finalPath)
	importStatus, importMessage := browserDownloadImportOutcome(videoID, importErr)
	s.update(id, func(task *BrowserDownloadTask) {
		task.State = browserDownloadStateDone
		task.VideoID = videoID
		// 入库失败与下载失败是两回事：文件已经在盘上了，别把它说成下载失败。
		task.ImportStatus = importStatus
		task.ImportError = importMessage
	})
}

// browserDownloadImportOutcome 把入库结果归成 ImportStatus 与一句原因（不带路径）。
func browserDownloadImportOutcome(videoID uint, err error) (string, string) {
	switch {
	case err == nil && videoID > 0:
		return BrowserDownloadImportImported, ""
	case err == nil:
		return "", ""
	case errors.Is(err, ErrBrowserDownloadNotInScanRoots):
		return BrowserDownloadImportNotInScanRoots, ErrBrowserDownloadNotInScanRoots.Error()
	default:
		return BrowserDownloadImportFailed, scrubPlaybackProxyPaths(err.Error())
	}
}

func (s *BrowserDownloadService) runFFmpeg(ctx context.Context, binary string, request *browserDownloadNormalized, partPath string, id string) error {
	args := buildBrowserDownloadArgs(request, partPath)
	cmd := exec.CommandContext(ctx, binary, args...)
	// 明确断开标准输入：ffmpeg 在某些错误分支上会等键盘输入，那会把任务挂死。
	cmd.Stdin = nil

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("无法读取 ffmpeg 进度：%w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{target: &stderr, limit: 8 << 10}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg 启动失败：%w", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.consumeProgress(stdout, id)
	}()
	<-done

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("ffmpeg 失败：%s", message)
	}
	return nil
}

var browserProgressLine = regexp.MustCompile(`^([a-z_]+)=(.*)$`)

// -progress pipe:1 的输出是逐行的 key=value，每组以 progress=continue|end 收尾。
func (s *BrowserDownloadService) consumeProgress(reader io.Reader, id string) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		match := browserProgressLine.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
		if match == nil {
			continue
		}
		switch match[1] {
		case "out_time_us", "out_time_ms":
			value, err := strconv.ParseInt(match[2], 10, 64)
			if err != nil || value < 0 {
				continue
			}
			// 字段名叫 out_time_ms，实际给的是微秒，这是 ffmpeg 的历史遗留。
			seconds := float64(value) / 1_000_000
			s.update(id, func(task *BrowserDownloadTask) { task.ProcessedSeconds = seconds })
		case "total_size":
			value, err := strconv.ParseInt(match[2], 10, 64)
			if err != nil || value < 0 {
				continue
			}
			s.update(id, func(task *BrowserDownloadTask) { task.BytesWritten = value })
		}
	}
}

func (s *BrowserDownloadService) importDirectory(directory, outputPath string) (uint, error) {
	if s.deps.ImportDirectory == nil {
		return 0, nil
	}
	return s.deps.ImportDirectory(directory, outputPath)
}

func (s *BrowserDownloadService) settings() (BrowserDownloadSettings, error) {
	if s.deps.Settings == nil {
		return BrowserDownloadSettings{}, errors.New("下载设置不可用")
	}
	return s.deps.Settings()
}

func (s *BrowserDownloadService) requestOf(id string) (*browserDownloadNormalized, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[id]
	if !ok || entry.request == nil {
		return nil, false
	}
	return entry.request, true
}

func (s *BrowserDownloadService) snapshot(id string) (BrowserDownloadTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[id]
	if !ok {
		return BrowserDownloadTask{}, false
	}
	return entry.task, true
}

func (s *BrowserDownloadService) update(id string, mutate func(task *BrowserDownloadTask)) {
	s.mu.Lock()
	entry, ok := s.entries[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	before := entry.task
	mutate(&entry.task)
	now := s.deps.Now().UnixMilli()
	entry.task.UpdatedAt = now
	stateChanged := entry.task.State != before.State
	if stateChanged {
		setBrowserDownloadStateLocked(&entry.task, entry.task.State, now)
	}
	if entry.task.State == browserDownloadStateDone {
		// 完成的任务不再需要请求规格（M-4）：它带着 Referer / Cookie，而完成的任务不能重试，
		// 留在内存里只是让 Cookie 多活一会儿。失败与取消的保留，同一会话内还要靠它重试。
		entry.request = nil
		// 地址同理（B-m-3）：换成与表里 display_url 相同的显示用地址，ListTasks 与推送事件
		// 不再带出签名 query 与 userinfo。
		entry.task.URL = browserDownloadDisplayURL(entry.task.URL)
	}
	reachedTerminal := stateChanged && browserDownloadTerminal(entry.task.State)
	// 只有落库列变了才写表：进度是逐行解析的，每一行都写一次表毫无意义。
	persistNeeded := stateChanged ||
		entry.task.Filename != before.Filename ||
		entry.task.Directory != before.Directory ||
		entry.task.Error != before.Error
	s.mu.Unlock()

	if persistNeeded {
		s.persist(id)
	}
	if reachedTerminal {
		// 终态会让历史变化（新行落定、M-8 的裁剪），在推送之前、锁外刷新快照。
		s.refreshHistory()
	}
	// 回调在锁外投递：它会回到 Wails 的事件层，不该被下载队列的锁牵着走。
	s.notify(stateChanged)
}

// fail 是 task.Error 唯一的写入口：原因先清洗再进内存，界面看到的与表里存的是同一份。
func (s *BrowserDownloadService) fail(id string, message string) {
	request, _ := s.requestOf(id)
	clean := sanitizeBrowserDownloadError(message, browserDownloadSecrets(request))
	s.update(id, func(task *BrowserDownloadTask) {
		task.State = browserDownloadStateFailed
		task.Error = clean
	})
}

// finishCanceled 收尾被取消的任务。生命周期上下文已结束说明是应用在退出，不是用户按了取消：
// 记成 interrupted，重启后显示「已中断」（D-PC21）。
func (s *BrowserDownloadService) finishCanceled(id string) {
	s.mu.Lock()
	lifecycleEnded := s.baseCtx != nil && s.baseCtx.Err() != nil
	s.mu.Unlock()
	state := browserDownloadStateCancel
	if lifecycleEnded {
		state = browserDownloadStateInterrupted
	}
	s.update(id, func(task *BrowserDownloadTask) {
		task.State = state
	})
}

func (s *BrowserDownloadService) removeFromQueueLocked(id string) {
	for index, queued := range s.queue {
		if queued == id {
			s.queue = append(s.queue[:index], s.queue[index+1:]...)
			return
		}
	}
}

// 任务列表不无限增长：终态任务超量时丢最早的。
func (s *BrowserDownloadService) trimLocked() {
	if len(s.entries) <= browserDownloadMaxTasks {
		return
	}
	terminal := make([]BrowserDownloadTask, 0, len(s.entries))
	for _, entry := range s.entries {
		if browserDownloadTerminal(entry.task.State) {
			terminal = append(terminal, entry.task)
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].CreatedAt < terminal[j].CreatedAt })
	excess := len(s.entries) - browserDownloadMaxTasks
	for index := 0; index < excess && index < len(terminal); index++ {
		delete(s.entries, terminal[index].ID)
	}
}

// ---- 任务记录落库（D-PC21、详细设计 §5.4）----
//
// 表里只有「看得见的元数据」：去掉 query 与 fragment 的地址、文件名、目录、状态、清洗过的错误。
// 请求头、Cookie、完整地址一律只留在内存（browserDownloadEntry.request），进程一退就没了——
// 这也是重启后的任务只能「在浏览器重新推送」、不能「重试」的原因。

var browserDownloadPersistedColumns = []string{"display_url", "file_name", "directory", "status", "error", "finished_at", "updated_at"}

// persist 把一个任务的当前状态写进表（按 task_uid upsert）。写失败只记日志：落库是为了
// 重启后还能看到历史，不能因为它让一次下载失败。
func (s *BrowserDownloadService) persist(id string) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	db := s.db()
	if db == nil {
		return
	}
	s.mu.Lock()
	entry, ok := s.entries[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	task := entry.task
	s.mu.Unlock()
	row := browserDownloadRowOf(task)
	err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "task_uid"}},
		DoUpdates: clause.AssignmentColumns(browserDownloadPersistedColumns),
	}).Create(&row).Error
	if err != nil {
		log.Printf("browser download: 写入任务记录失败 task=%s err=%v", id, err)
		return
	}
	if browserDownloadTerminal(task.State) {
		if err := trimBrowserDownloadRows(db); err != nil {
			log.Printf("browser download: 裁剪历史任务记录失败 err=%v", err)
		}
	}
}

// trimBrowserDownloadRows 只保留最近 browserDownloadPersistedTerminalLimit 条终态行（M-8），
// 按 created_at 倒序（与 ListDownloadTasks 带出历史的顺序一致）裁掉更早的。非终态行不裁：
// 它们要么是本次会话在跑的任务，要么等启动时被标成 interrupted。
func trimBrowserDownloadRows(db *gorm.DB) error {
	terminal := []string{browserDownloadStateDone, browserDownloadStateFailed, browserDownloadStateCancel, browserDownloadStateInterrupted}
	for {
		var ids []uint
		if err := db.Model(&models.BrowserDownloadTask{}).
			Where("status IN ?", terminal).
			Order("created_at DESC, id DESC").
			Offset(browserDownloadPersistedTerminalLimit).Limit(browserDownloadTrimBatch).
			Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := db.Where("id IN ?", ids).Delete(&models.BrowserDownloadTask{}).Error; err != nil {
			return err
		}
		if len(ids) < browserDownloadTrimBatch {
			return nil
		}
	}
}

func browserDownloadRowOf(task BrowserDownloadTask) models.BrowserDownloadTask {
	row := models.BrowserDownloadTask{
		TaskUID:    task.ID,
		DisplayURL: browserDownloadDisplayURL(task.URL),
		FileName:   task.Filename,
		Directory:  task.Directory,
		Status:     task.State,
		// task.Error 只经 fail() 写入，已经清洗过；这里再过一遍是落盘前的最后一道闸，
		// 以后谁绕开 fail() 直接改 Error 也漏不出去。
		Error:     sanitizeBrowserDownloadError(task.Error, nil),
		CreatedAt: time.UnixMilli(task.CreatedAt),
		UpdatedAt: time.UnixMilli(task.UpdatedAt),
	}
	if task.FinishedAt > 0 {
		finished := time.UnixMilli(task.FinishedAt)
		row.FinishedAt = &finished
	}
	return row
}

// MarkInterruptedOnStartup 把上次运行留下的非终态行（queued / running / importing）置为
// interrupted（D-PC21）。条件更新，重复调用无害；本次会话内存里已有的任务不受影响。
func (s *BrowserDownloadService) MarkInterruptedOnStartup() (int64, error) {
	if s == nil {
		return 0, nil
	}
	db := s.db()
	if db == nil {
		return 0, nil
	}
	s.mu.Lock()
	live := make([]string, 0, len(s.entries))
	for id := range s.entries {
		live = append(live, id)
	}
	s.mu.Unlock()
	now := s.deps.Now()
	query := db.Model(&models.BrowserDownloadTask{}).
		Where("status IN ?", []string{browserDownloadStateQueued, browserDownloadStateRun, browserDownloadStateImport})
	if len(live) > 0 {
		query = query.Where("task_uid NOT IN ?", live)
	}
	result := query.Updates(map[string]any{
		"status":      browserDownloadStateInterrupted,
		"finished_at": now,
		"updated_at":  now,
	})
	if result.Error == nil && result.RowsAffected > 0 {
		s.refreshHistory()
	}
	return result.RowsAffected, result.Error
}

// ListDownloadTasks 合并内存任务与表里的历史（D-PC21）。同一个 task_uid 以内存为准：
// 只有内存里的任务还握着请求规格，能重试。
//
// 这是历史快照的显式刷新点（M-2）：界面打开下载页、手动刷新时走这里，入库状态按当下
// 片库与扫描目录现算；事件推送复用这份快照。
func (s *BrowserDownloadService) ListDownloadTasks() []BrowserDownloadTask {
	if s == nil {
		return nil
	}
	s.refreshHistory()
	return mergeBrowserDownloadTasks(s.ListTasks(), s.cachedHistory())
}

// mergeBrowserDownloadTasks 把表里的历史并到内存任务后面，同一个 task_uid 以内存为准，
// 按创建时间倒序。
func mergeBrowserDownloadTasks(live, history []BrowserDownloadTask) []BrowserDownloadTask {
	tasks := make([]BrowserDownloadTask, 0, len(live)+len(history))
	tasks = append(tasks, live...)
	seen := make(map[string]struct{}, len(live))
	for _, task := range live {
		seen[task.ID] = struct{}{}
	}
	for _, task := range history {
		if _, ok := seen[task.ID]; ok {
			continue
		}
		tasks = append(tasks, task)
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].CreatedAt > tasks[j].CreatedAt })
	return tasks
}

// historyReady 报告历史快照是否已经读过（读过但为空也算）。
func (s *BrowserDownloadService) historyReady() bool {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	return s.historyLoaded
}

// cachedHistory 返回历史快照的副本；没有读过时为空。
func (s *BrowserDownloadService) cachedHistory() []BrowserDownloadTask {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	return append([]BrowserDownloadTask(nil), s.history...)
}

// refreshHistory 重读表里的历史并现算入库状态（查库与 os.Stat 都在这里）。不持有 mu、emitMu
// 或 persistMu；读失败时保留旧快照并记日志。刷新之间用 historyRefreshMu 串行，后一次刷新
// 读到的一定不比前一次旧。
func (s *BrowserDownloadService) refreshHistory() {
	if s == nil {
		return
	}
	s.historyRefreshMu.Lock()
	defer s.historyRefreshMu.Unlock()
	var history []BrowserDownloadTask
	if db := s.db(); db != nil {
		var rows []models.BrowserDownloadTask
		if err := db.Order("created_at DESC, id DESC").Limit(browserDownloadHistoryLimit).Find(&rows).Error; err != nil {
			log.Printf("browser download: 读取历史任务失败 err=%v", err)
			return
		}
		history = make([]BrowserDownloadTask, 0, len(rows))
		for _, row := range rows {
			history = append(history, browserDownloadTaskOfRow(row))
		}
		// 内存里也有的那几行会在合并时被内存版本替换；这里照样现算，免得它们之后被裁出内存
		// （trimLocked）时，推送里的历史版本缺了入库状态。
		annotateBrowserDownloadHistoryImport(db, history)
	}
	s.historyMu.Lock()
	s.history, s.historyLoaded = history, true
	s.historyMu.Unlock()
}

func browserDownloadTaskOfRow(row models.BrowserDownloadTask) BrowserDownloadTask {
	task := BrowserDownloadTask{
		ID:        row.TaskUID,
		URL:       row.DisplayURL,
		Filename:  row.FileName,
		Directory: row.Directory,
		State:     row.Status,
		Error:     row.Error,
		CreatedAt: row.CreatedAt.UnixMilli(),
		UpdatedAt: row.UpdatedAt.UnixMilli(),
	}
	if path, ok := browserDownloadRowOutputPath(row.Directory, row.FileName); ok {
		task.OutputPath = path
	}
	if row.FinishedAt != nil {
		task.FinishedAt = row.FinishedAt.UnixMilli()
	}
	return task
}

// browserDownloadRowOutputPath 由表里的目录与文件名拼出产物路径。文件名是落盘时清洗过的
// 单段名字；这里再确认一次它不带分隔符，拼出来的路径不会越出下载目录。
func browserDownloadRowOutputPath(directory, fileName string) (string, bool) {
	if strings.TrimSpace(directory) == "" || fileName == "" || fileName != filepath.Base(fileName) || fileName == "." || fileName == ".." {
		return "", false
	}
	return filepath.Join(directory, fileName), true
}

// browserDownloadHasOutputFile 报告任务的产物是否还在盘上：只有完成的任务，或在入库那一步
// 被中断（文件已改名落位）的任务才有产物。
func browserDownloadHasOutputFile(task BrowserDownloadTask) bool {
	if task.OutputPath == "" {
		return false
	}
	if task.State != browserDownloadStateDone && task.State != browserDownloadStateInterrupted {
		return false
	}
	info, err := os.Stat(task.OutputPath)
	return err == nil && info.Mode().IsRegular()
}

// annotateBrowserDownloadHistoryImport 给历史任务现算入库状态：表里不存入库结果，重启后
// 以片库与扫描目录的当前状态为准（用户可能已经把目录加进去了）。
func annotateBrowserDownloadHistoryImport(db *gorm.DB, tasks []BrowserDownloadTask) {
	indexes := make([]int, 0, len(tasks))
	paths := make([]string, 0, len(tasks))
	for index, task := range tasks {
		if browserDownloadHasOutputFile(task) {
			indexes = append(indexes, index)
			paths = append(paths, task.OutputPath)
		}
	}
	if len(paths) == 0 {
		return
	}
	var videos []models.Video
	if err := db.Select("id", "path").Where("path IN ?", paths).Find(&videos).Error; err != nil {
		log.Printf("browser download: 读取历史任务的入库状态失败 err=%v", err)
		return
	}
	imported := make(map[string]uint, len(videos))
	for _, video := range videos {
		imported[video.Path] = video.ID
	}
	var dirs []models.ScanDirectory
	if err := db.Find(&dirs).Error; err != nil {
		log.Printf("browser download: 读取扫描目录失败 err=%v", err)
		return
	}
	for _, index := range indexes {
		task := &tasks[index]
		if videoID, ok := imported[task.OutputPath]; ok {
			task.VideoID = videoID
			task.ImportStatus = BrowserDownloadImportImported
			continue
		}
		if !BrowserDownloadDirectoryCovered(dirs, task.Directory) {
			task.ImportStatus = BrowserDownloadImportNotInScanRoots
			task.ImportError = ErrBrowserDownloadNotInScanRoots.Error()
			continue
		}
		task.ImportStatus = BrowserDownloadImportFailed
		task.ImportError = errBrowserDownloadNotInLibrary.Error()
	}
}

// findTask 先查内存再查表。inMemory 为 false 时返回的是历史行拼出来的快照。
func (s *BrowserDownloadService) findTask(taskUID string) (BrowserDownloadTask, bool, bool, error) {
	s.mu.Lock()
	if entry, ok := s.entries[taskUID]; ok {
		task := entry.task
		task.Retryable = browserDownloadRetryableLocked(entry)
		s.mu.Unlock()
		return task, true, true, nil
	}
	s.mu.Unlock()
	db := s.db()
	if db == nil {
		return BrowserDownloadTask{}, false, false, nil
	}
	var row models.BrowserDownloadTask
	err := db.Where("task_uid = ?", taskUID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return BrowserDownloadTask{}, false, false, nil
	}
	if err != nil {
		return BrowserDownloadTask{}, false, false, fmt.Errorf("读取下载任务失败：%w", err)
	}
	return browserDownloadTaskOfRow(row), true, false, nil
}

// finishedTask 找到一个产物还在盘上的任务，是「入库 / 打开位置」三个动作的共同前提。
func (s *BrowserDownloadService) finishedTask(taskUID string) (BrowserDownloadTask, bool, BrowserDownloadActionResult, error) {
	task, found, inMemory, err := s.findTask(taskUID)
	if err != nil {
		return BrowserDownloadTask{}, false, BrowserDownloadActionResult{}, err
	}
	if !found {
		return BrowserDownloadTask{}, false, BrowserDownloadActionResult{Code: BrowserDownloadCodeTaskNotFound, Message: "没有这个下载任务"}, nil
	}
	if task.State != browserDownloadStateDone && task.State != browserDownloadStateInterrupted {
		return task, inMemory, BrowserDownloadActionResult{Code: BrowserDownloadCodeNotFinished, Message: "任务还没有下载完成", Task: &task}, nil
	}
	if !browserDownloadHasOutputFile(task) {
		return task, inMemory, BrowserDownloadActionResult{Code: BrowserDownloadCodeFileMissing, Message: "下载的文件已经不在了", Task: &task}, nil
	}
	return task, inMemory, BrowserDownloadActionResult{Code: BrowserDownloadCodeOK, Task: &task}, nil
}

// FinishedDownloadDirectory 返回一个已完成任务的下载目录，供「把下载目录加入扫描目录」使用。
// Code 不是 ok 时目录为空。
func (s *BrowserDownloadService) FinishedDownloadDirectory(taskUID string) (string, BrowserDownloadActionResult, error) {
	if s == nil {
		return "", BrowserDownloadActionResult{Code: BrowserDownloadCodeServiceUnavailable, Message: "下载服务未启用"}, nil
	}
	task, _, check, err := s.finishedTask(taskUID)
	if err != nil || check.Code != BrowserDownloadCodeOK {
		return "", check, err
	}
	return cleanBrowserDownloadDirectory(task.Directory), check, nil
}

// ReimportDownload 对一个已完成任务的下载目录重跑一次入库（D-PC25）：走的仍是
// ImportDirectory（既有 SyncAffectedDirectories 窄对账），判据仍是「这个文件进没进库」。
func (s *BrowserDownloadService) ReimportDownload(taskUID string) (BrowserDownloadActionResult, error) {
	if s == nil || s.deps.ImportDirectory == nil {
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeServiceUnavailable, Message: "入库服务未启用"}, nil
	}
	task, inMemory, check, err := s.finishedTask(taskUID)
	if err != nil || check.Code != BrowserDownloadCodeOK {
		return check, err
	}
	videoID, importErr := s.deps.ImportDirectory(cleanBrowserDownloadDirectory(task.Directory), task.OutputPath)
	status, message := browserDownloadImportOutcome(videoID, importErr)
	// 历史任务的入库状态是按片库与扫描目录现算的：这次入库（以及可能刚加进来的扫描目录）会改变
	// 同一目录下其他历史任务的状态，先刷新快照，之后的推送才带得上。
	s.refreshHistory()
	if inMemory {
		s.update(taskUID, func(current *BrowserDownloadTask) {
			current.VideoID = videoID
			current.ImportStatus = status
			current.ImportError = message
		})
		if refreshed, found, _, findErr := s.findTask(taskUID); findErr == nil && found {
			task = refreshed
		}
	} else {
		task.VideoID = videoID
		task.ImportStatus = status
		task.ImportError = message
		s.notify(true)
	}
	result := BrowserDownloadActionResult{Code: BrowserDownloadCodeOK, Task: &task}
	switch status {
	case BrowserDownloadImportNotInScanRoots, BrowserDownloadImportFailed:
		result.Code = status
		result.Message = message
	case "":
		result.Code = BrowserDownloadImportFailed
		result.Message = errBrowserDownloadNotInLibrary.Error()
	}
	return result, nil
}

// RevealDownload 在系统文件管理器里定位下载的文件（D-PC25「打开所在目录」）。
func (s *BrowserDownloadService) RevealDownload(taskUID string) (BrowserDownloadActionResult, error) {
	if s == nil {
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeServiceUnavailable, Message: "下载服务未启用"}, nil
	}
	task, _, check, err := s.finishedTask(taskUID)
	if err != nil || check.Code != BrowserDownloadCodeOK {
		return check, err
	}
	if err := s.deps.Reveal(task.OutputPath); err != nil {
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeRevealFailed, Message: scrubPlaybackProxyPaths(err.Error()), Task: &task}, nil
	}
	return BrowserDownloadActionResult{Code: BrowserDownloadCodeOK, Task: &task}, nil
}

// RetryDownload 在同一会话内重跑一个失败或取消的任务（D-PC21）。只有内存里还握着请求规格
// （含 Referer / Cookie）才重试得了；重启后的历史任务返回 retry_requires_browser。
// 沿用原任务 ID，表里同一行被改回 queued。
func (s *BrowserDownloadService) RetryDownload(taskUID string) (BrowserDownloadActionResult, error) {
	if s == nil {
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeServiceUnavailable, Message: "下载服务未启用"}, nil
	}
	s.mu.Lock()
	entry, ok := s.entries[taskUID]
	// 先判状态再判请求规格：完成的任务已经丢掉了请求规格（M-4），它该报「不可重试」，
	// 而不是「请回浏览器重新推送」。
	if ok && entry.task.State != browserDownloadStateFailed && entry.task.State != browserDownloadStateCancel {
		task := entry.task
		s.mu.Unlock()
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeNotRetryable, Message: "只有失败或已取消的任务可以重试", Task: &task}, nil
	}
	if !ok || entry.request == nil {
		s.mu.Unlock()
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeRetryRequiresBrowser, Message: "应用重启后请求信息已不在，请回浏览器重新推送"}, nil
	}
	if !browserDownloadRetryableLocked(entry) {
		task := entry.task
		s.mu.Unlock()
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeNotRetryable, Message: "只有失败或已取消的任务可以重试", Task: &task}, nil
	}
	if len(s.queue) >= browserDownloadMaxQueue {
		s.mu.Unlock()
		return BrowserDownloadActionResult{Code: BrowserDownloadCodeQueueFull, Message: ErrBrowserDownloadQueueFull.Error()}, nil
	}
	task := &entry.task
	task.Filename = ""
	task.OutputPath = ""
	task.VideoID = 0
	task.ProcessedSeconds = 0
	task.BytesWritten = 0
	task.Error = ""
	task.ImportError = ""
	task.ImportStatus = ""
	entry.cancel = nil
	setBrowserDownloadStateLocked(task, browserDownloadStateQueued, s.deps.Now().UnixMilli())
	s.queue = append(s.queue, taskUID)
	snapshot := entry.task
	snapshot.Retryable = false
	s.mu.Unlock()

	s.persist(taskUID)
	s.notify(true)
	s.pump()
	return BrowserDownloadActionResult{Code: BrowserDownloadCodeOK, Task: &snapshot}, nil
}

func cleanBrowserDownloadDirectory(directory string) string {
	clean := filepath.Clean(strings.TrimSpace(directory))
	if clean == "." {
		return ""
	}
	return clean
}

// ---- 错误与地址清洗（详细设计 §5.4、计划评审 I7）----

// browserDownloadURLPattern 匹配文本里形如 URL 的片段（scheme://…，到空白或引号为止）。
//
// 开头不加 \b（M-1）：\b 要求 scheme 前是非单词字符，`_https://…?sig=` 这种前面紧贴下划线或
// 字母数字的片段整个匹配不上，签名就原样留在文本里。去掉之后最坏是把前缀字母并进 scheme
// （xhttps://…），照样会被剥掉 query 与 userinfo。
var browserDownloadURLPattern = regexp.MustCompile(`(?i)[a-z][a-z0-9+.\-]*://[^\s'"<>]+`)

// browserDownloadSensitiveHeaderLine 匹配请求头样式的敏感行：Cookie、Set-Cookie、Authorization、
// Proxy-Authorization，以及 X- 开头的自定义头（签名、令牌常放在这里）。
var browserDownloadSensitiveHeaderLine = regexp.MustCompile(`(?i)(^|[^a-z0-9-])(set-cookie|cookie|proxy-authorization|authorization|x-[a-z0-9-]+)\s*:`)

var errBrowserDownloadNotInLibrary = errors.New("扫描后片库里没有这个文件")

// browserDownloadDisplayURL 是 display_url 列的唯一来源：只留 scheme://host/path。
func browserDownloadDisplayURL(raw string) string {
	return browserDownloadStripURL(strings.TrimSpace(raw))
}

// browserDownloadStripURL 去掉 query、fragment、路径参数（;jsessionid=…）与 userinfo。
// 手写而不走 url.Parse：解析失败的片段同样要剥干净，不能原样落库。
//
// 顺序是承重的（I-1）：先在 scheme:// 之后按第一个 / ? # 切出 authority，按**最后一个** @
// 去掉 userinfo，再在 path 上截断 ? # ;。反过来先截 ; 的话，`https://u:p;w@host/x` 会被截成
// `https://u:p`——用户名与半截密码被当成主机名原样留下。
func browserDownloadStripURL(raw string) string {
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd < 0 {
		if index := strings.IndexAny(raw, "?#;"); index >= 0 {
			raw = raw[:index]
		}
		return raw
	}
	rest := raw[schemeEnd+3:]
	authority, path := rest, ""
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		authority, path = rest[:end], rest[end:]
	}
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		authority = authority[at+1:]
	}
	if index := strings.IndexAny(path, "?#;"); index >= 0 {
		path = path[:index]
	}
	return raw[:schemeEnd+3] + authority + path
}

// sanitizeBrowserDownloadError 是 error 落盘（与进内存）前的清洗：
//  1. 删除包含 Cookie: / Set-Cookie: / Authorization: / X-…: 这类请求头样式的行；
//  2. 形如 URL 的片段去掉 query、fragment 与 userinfo，先换成占位符，免得 //host/path 被下一步
//     当成路径擦掉；
//  3. 请求里已知的敏感值（Cookie 与其各段取值、地址的 query / fragment 与各参数值、userinfo 的
//     用户名与密码）逐字擦掉——放在 URL 之后：先擦的话 <redacted> 会把 URL 截成两半，query 的后半截
//     就漏在外面了；
//  4. 其余绝对路径擦成 <path>；
//  5. 最多保留 500 个字符。
//
// 第 3 步按长度降序、一遍扫完（A-m1）：短值恰好是长值的一段（用户名 admin、密码 admin2024!）时，
// 先擦短的会让长值剩下半截（<redacted>2024!）露在外面；逐个 ReplaceAll 还会让后面的短值擦进前面
// 刚写下的 <redacted> 里。调用方给的顺序不作数，这里自己排。
//
// PG 的 text 存不下 NUL 与非法 UTF-8，这两样一并去掉。
func sanitizeBrowserDownloadError(message string, secrets []string) string {
	text := strings.ReplaceAll(strings.ToValidUTF8(message, ""), "\x00", "")
	redact := browserDownloadRedactor(secrets)

	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	kept := lines[:0]
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if browserDownloadSensitiveHeaderLine.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	text = strings.Join(kept, "\n")

	urls := make([]string, 0)
	text = browserDownloadURLPattern.ReplaceAllStringFunc(text, func(match string) string {
		suffix := ""
		for len(match) > 1 && strings.ContainsRune(":,.;!?)", rune(match[len(match)-1])) {
			suffix = string(match[len(match)-1]) + suffix
			match = match[:len(match)-1]
		}
		urls = append(urls, redact(browserDownloadStripURL(match)))
		return fmt.Sprintf("\x00%d\x00", len(urls)-1) + suffix
	})
	text = scrubPlaybackProxyPaths(redact(text))
	for index, stripped := range urls {
		text = strings.Replace(text, fmt.Sprintf("\x00%d\x00", index), stripped, 1)
	}

	text = strings.TrimSpace(text)
	if runes := []rune(text); len(runes) > browserDownloadErrorMaxRunes {
		text = strings.TrimSpace(string(runes[:browserDownloadErrorMaxRunes]))
	}
	return text
}

// browserDownloadRedactor 把待擦除值按长度降序排好，返回一遍扫完的替换函数：同一位置上最长的值先
// 命中，替换写下的 <redacted> 不会再被别的值匹配。
func browserDownloadRedactor(secrets []string) func(string) string {
	ordered := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			ordered = append(ordered, secret)
		}
	}
	if len(ordered) == 0 {
		return func(value string) string { return value }
	}
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	pairs := make([]string, 0, 2*len(ordered))
	for _, secret := range ordered {
		pairs = append(pairs, secret, "<redacted>")
	}
	return strings.NewReplacer(pairs...).Replace
}

// browserDownloadCredentialMinLength 是 userinfo 凭证做全文擦除的最短长度（A-m1）。1–2 个字符的
// 用户名或密码全文擦除会把整条报错擦成乱码（用户名 u 会抠掉每一个字母 u），这类值只在 URL 的
// userinfo 位置按结构剥离（browserDownloadStripURL）：工具把它单独打印出来时会留在报错里，
// 1–2 个字符的凭证本来也谈不上机密。
const browserDownloadCredentialMinLength = 3

// browserDownloadSecrets 列出这次请求里已知的敏感值，供清洗时逐字擦除。Cookie 与 query 里太短的值
// （<6）不擦：那种长度谈不上机密，擦了反而会把 "true"、"1" 之类的正常字样从报错里抠掉。
// userinfo 的用户名与密码门槛放低到 3（B-m-3、A-m1）：它们就是凭证，`admin:admin@host` 这种短凭证
// 同样要擦，代价是报错里恰好相同的字样也一并被抠掉。
func browserDownloadSecrets(request *browserDownloadNormalized) []string {
	if request == nil {
		return nil
	}
	seen := make(map[string]struct{})
	secrets := make([]string, 0)
	addValue := func(value string, minLength int) {
		value = strings.TrimSpace(value)
		if value == "" || len(value) < minLength {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		secrets = append(secrets, value)
	}
	add := func(value string) { addValue(value, 6) }
	addCredential := func(value string) { addValue(value, browserDownloadCredentialMinLength) }
	for _, header := range request.Headers {
		name, value, ok := strings.Cut(header, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "Cookie") {
			continue
		}
		add(value)
		for _, pair := range strings.Split(value, ";") {
			if _, cookieValue, ok := strings.Cut(pair, "="); ok {
				add(cookieValue)
			}
		}
	}
	if parsed, err := url.Parse(request.URL); err == nil {
		// userinfo 里的用户名与密码（I-1）：URL 形态的片段会被 browserDownloadStripURL 剥掉，
		// 这里再按值擦一遍，防它们以别的形态（ffmpeg 把凭证单独打印出来）出现在报错里。
		if parsed.User != nil {
			addCredential(parsed.User.Username())
			if password, ok := parsed.User.Password(); ok {
				addCredential(password)
			}
		}
		add(parsed.RawQuery)
		add(parsed.Fragment)
		for _, values := range parsed.Query() {
			for _, value := range values {
				add(value)
			}
		}
	}
	return secrets
}

// ---- 请求归一化与参数拼装 ----

type browserDownloadNormalized struct {
	URL             string
	Kind            string
	Title           string
	VariantLabel    string
	PageURL         string
	OutputExtension string
	// OutputFormat 是给 ffmpeg -f 用的封装器名字。输出文件名以 .part 结尾，
	// ffmpeg 无法从扩展名推断格式，必须显式给。
	OutputFormat string
	Headers      []string // 已经过滤好的 "Name: value" 行
	UserAgent    string
}

// browserDownloadMuxers 把输出扩展名映射到 ffmpeg 的封装器名字。
// 不在表里的容器直接拒绝建任务，而不是让 ffmpeg 抛一句看不懂的错——
// 插件内置下载器不经 ffmpeg，那条路仍然可用。
var browserDownloadMuxers = map[string]string{
	"mp4":  "mp4",
	"m4v":  "mp4",
	"mkv":  "matroska",
	"ts":   "mpegts",
	"webm": "webm",
	"mov":  "mov",
	"avi":  "avi",
	"flv":  "flv",
	"m4a":  "ipod",
	"mp3":  "mp3",
	"aac":  "adts",
	"ogv":  "ogg",
	"3gp":  "3gp",
	"wmv":  "asf",
	"mpg":  "mpeg",
	"mpeg": "mpeg",
}

var browserDownloadAllowedKinds = map[string]string{
	"hls":  "mp4",
	"file": "",
}

// normalizeBrowserDownloadRequest 是这条链路上唯一的入口校验，外部传进来的东西
// 到此为止都当成不可信：
//
//   - 协议只认 http/https。ffmpeg 认得 file、concat、pipe 等一堆协议，
//     放任协议等于把"读本机任意文件"的能力交出去；
//   - 请求头值里不许出现 CR/LF。ffmpeg 的 -headers 是一整段 CRLF 分隔的文本，
//     值里带换行就能多塞任意请求头；
//   - Kind 只认固定两种，决定输出容器。
func normalizeBrowserDownloadRequest(request BrowserDownloadRequest) (*browserDownloadNormalized, error) {
	rawURL := strings.TrimSpace(request.URL)
	if rawURL == "" {
		return nil, fmt.Errorf("%w：地址为空", ErrBrowserDownloadInvalidRequest)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w：地址解析失败 %v", ErrBrowserDownloadInvalidRequest, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("%w：只支持 http/https，收到 %q", ErrBrowserDownloadInvalidRequest, parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("%w：地址缺少主机名", ErrBrowserDownloadInvalidRequest)
	}

	kind := strings.ToLower(strings.TrimSpace(request.Kind))
	if kind == "" {
		kind = "hls"
	}
	defaultExtension, ok := browserDownloadAllowedKinds[kind]
	if !ok {
		return nil, fmt.Errorf("%w：不认识的类型 %q", ErrBrowserDownloadInvalidRequest, request.Kind)
	}
	extension := defaultExtension
	if kind == "file" {
		extension = sanitizeBrowserDownloadExtension(parsed.Path)
	}

	outputFormat, ok := browserDownloadMuxers[extension]
	if !ok {
		return nil, fmt.Errorf("%w：不支持的输出容器 %q，可以改用插件内置下载器", ErrBrowserDownloadInvalidRequest, extension)
	}

	normalized := &browserDownloadNormalized{
		URL:             rawURL,
		Kind:            kind,
		Title:           request.Title,
		VariantLabel:    request.VariantLabel,
		PageURL:         request.PageURL,
		OutputExtension: extension,
		OutputFormat:    outputFormat,
	}

	// 注意 Cookie 的去向：它会作为 -headers 的一部分进入 ffmpeg 进程的命令行参数，
	// 同一用户下的其他进程能通过 ps 看到（Linux 上还有 /proc/<pid>/cmdline）。
	// ffmpeg 没有"从文件读请求头"的入口，所以这一点无法在这一侧消除；插件那边
	// 因此把「推送时附带 Cookie」默认关掉，并在选项页写明这个代价。
	for name, value := range map[string]string{
		"Referer": request.Referer,
		"Origin":  request.Origin,
		"Cookie":  request.Cookie,
	} {
		clean := strings.TrimSpace(value)
		if clean == "" {
			continue
		}
		if strings.ContainsAny(clean, "\r\n") {
			return nil, fmt.Errorf("%w：请求头 %s 里有换行", ErrBrowserDownloadInvalidRequest, name)
		}
		normalized.Headers = append(normalized.Headers, name+": "+clean)
	}
	sort.Strings(normalized.Headers)

	userAgent := strings.TrimSpace(request.UserAgent)
	if strings.ContainsAny(userAgent, "\r\n") {
		return nil, fmt.Errorf("%w：User-Agent 里有换行", ErrBrowserDownloadInvalidRequest)
	}
	normalized.UserAgent = userAgent
	return normalized, nil
}

// buildBrowserDownloadArgs 拼 ffmpeg 参数。给的是参数数组、不经 shell，
// 所以地址里的引号、分号、反引号都只是普通字符，没有命令行注入面。
func buildBrowserDownloadArgs(request *browserDownloadNormalized, outputPath string) []string {
	args := []string{
		"-nostdin",
		"-v", "error",
		// 只放行取网络流真正需要的协议，**其中不含 file**。
		//
		// 这个选项在 -i 之前，作用于输入侧的解复用器，不影响输出文件的写入
		// （已实测：去掉 file 之后输出照常生成）。而放着 file 不动是有实害的：
		// HLS 的分片与密钥地址都来自播放列表，一份恶意播放列表把分片 URI 写成
		// file:///… 就能让 ffmpeg 去读本机文件，再把内容混进产出的视频里，
		// 而那个产出还会被自动扫描入库。
		"-protocol_whitelist", "http,https,tcp,tls,crypto,httpproxy",
		"-rw_timeout", "30000000",
	}
	if len(request.Headers) > 0 {
		args = append(args, "-headers", strings.Join(request.Headers, "\r\n")+"\r\n")
	}
	if request.UserAgent != "" {
		args = append(args, "-user_agent", request.UserAgent)
	}
	args = append(args,
		"-i", request.URL,
		// 必须显式指定封装格式：输出文件名以 .part 结尾（避免半成品被扫描收录），
		// 而 ffmpeg 是靠扩展名推断输出格式的，.part 它不认识，会直接拒绝初始化
		// 封装器（Unable to choose an output format）。
		"-f", request.OutputFormat,
		// 无损转封装，不重新编码。mp4 muxer 会在需要时自己套 aac_adtstoasc，
		// 显式写反而会在源本来就是 ASC 时报错。
		"-c", "copy",
		"-map", "0",
		"-progress", "pipe:1",
		"-nostats",
		// .part 是我们用 O_EXCL 抢下来的空文件，只可能是自己的，
		// 让 ffmpeg 直接覆盖它；不加 -y 的话它会因为"文件已存在"而失败。
		"-y",
		outputPath,
	)
	return args
}

// reserveBrowserDownloadPath 挑一个还没被占用的文件名，并用 .part 临时文件把它占住。
//
// **占位的是 .part，不是最终文件。** 早先的版本会先建一个空的最终文件来占名，
// 那个空文件带着 .mp4 这样的正经扩展名、在下载全程留在下载目录里，而下载目录通常
// 就是扫描目录：并发的另一个任务下载完成时会触发一次扫描，把这个 0 字节文件当成
// 一条视频记录扫进片库；应用被强制退出时它还会永久留在那里。.part 不在任何视频
// 扩展名白名单里，扫描不会碰它。
//
// 代价是下载期间最终文件名没被占住，所以改名那一步要再抢一次名字，见
// finalizeBrowserDownloadPath。
func reserveBrowserDownloadPath(directory, title, variantLabel, extension string) (string, string, error) {
	cleanDir := filepath.Clean(strings.TrimSpace(directory))
	if cleanDir == "" || cleanDir == "." {
		return "", "", ErrBrowserDownloadDirectoryUnset
	}
	info, err := os.Stat(cleanDir)
	if err != nil {
		return "", "", fmt.Errorf("下载目录不可用：%w", err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("下载目录不是一个目录：%s", cleanDir)
	}

	stem := sanitizeBrowserDownloadStem(title)
	if variantLabel != "" {
		stem += "_" + sanitizeBrowserDownloadStem(variantLabel)
	}
	ext := sanitizeBrowserDownloadExtension(extension)
	if ext == "" {
		ext = "mp4"
	}

	for index := 0; index < 1000; index++ {
		candidate := stem
		if index > 0 {
			candidate = fmt.Sprintf("%s (%d)", stem, index+1)
		}
		outputPath := filepath.Join(cleanDir, candidate+"."+ext)
		// 再确认一次结果确实落在下载目录里：文件名已经过滤过分隔符，
		// 这一条是防止将来有人放宽过滤时悄悄逃出目录。
		if filepath.Dir(outputPath) != cleanDir {
			return "", "", fmt.Errorf("%w：文件名越出了下载目录", ErrBrowserDownloadInvalidRequest)
		}
		// 最终文件名已经被别的东西占着就换下一个，绝不覆盖。
		if _, err := os.Stat(outputPath); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return "", "", fmt.Errorf("检查下载文件名失败：%w", err)
		}

		partPath := outputPath + browserDownloadPartSuffix
		partFile, err := os.OpenFile(partPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", "", fmt.Errorf("创建临时文件失败：%w", err)
		}
		_ = partFile.Close()
		return outputPath, partPath, nil
	}
	return "", "", errors.New("同名文件太多，取不到可用的文件名")
}

// finalizeBrowserDownloadPath 把下好的 .part 改成最终文件。
//
// 下载期间最终文件名没被占住，所以这里要再抢一次：先用 O_EXCL 建出来（抢到的就是
// 我们自己的空文件，改名覆盖它是安全的），抢不到就换个带序号的名字。任何情况下都
// 不覆盖别人的文件。返回真正落盘的路径——它可能与预期的名字不同。
func finalizeBrowserDownloadPath(partPath, preferredPath string) (string, error) {
	dir := filepath.Dir(preferredPath)
	ext := filepath.Ext(preferredPath)
	stem := strings.TrimSuffix(filepath.Base(preferredPath), ext)

	for index := 0; index < 1000; index++ {
		candidate := preferredPath
		if index > 0 {
			candidate = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, index+1, ext))
		}
		file, err := os.OpenFile(candidate, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", fmt.Errorf("创建下载文件失败：%w", err)
		}
		_ = file.Close()
		if err := os.Rename(partPath, candidate); err != nil {
			_ = os.Remove(candidate)
			return "", fmt.Errorf("下载完成但改名失败：%w", err)
		}
		return candidate, nil
	}
	return "", errors.New("同名文件太多，取不到可用的文件名")
}

var browserDownloadIllegalChars = regexp.MustCompile(`[\x00-\x1f\x7f/\\:*?"<>|]`)

func sanitizeBrowserDownloadStem(raw string) string {
	name := browserDownloadIllegalChars.ReplaceAllString(raw, " ")
	name = strings.Join(strings.Fields(name), " ")
	name = strings.Trim(name, " .")
	if runtime.GOOS == "windows" {
		if reserved := strings.ToLower(name); isReservedWindowsName(reserved) {
			name = ""
		}
	}
	if name == "" {
		return "browser-download"
	}
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
		name = strings.Trim(name, " .")
	}
	if name == "" {
		return "browser-download"
	}
	return name
}

func isReservedWindowsName(name string) bool {
	switch name {
	case "con", "prn", "aux", "nul":
		return true
	}
	if len(name) == 4 && (strings.HasPrefix(name, "com") || strings.HasPrefix(name, "lpt")) {
		return name[3] >= '1' && name[3] <= '9'
	}
	return false
}

var browserDownloadExtension = regexp.MustCompile(`^[a-zA-Z0-9]{1,8}$`)

func sanitizeBrowserDownloadExtension(raw string) string {
	value := raw
	if index := strings.LastIndex(value, "."); index >= 0 {
		value = value[index+1:]
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if !browserDownloadExtension.MatchString(value) {
		return ""
	}
	return value
}

// NormalizeBrowserDownloadConcurrency 与设置页同口径：非正取默认 2，超过 4 收到 4。
// 上限压得低是有意的：这些任务在跑 ffmpeg，同时开太多只会互相抢带宽。
func normalizeBrowserDownloadConcurrency(value int) int {
	if value <= 0 {
		return 2
	}
	if value > 4 {
		return 4
	}
	return value
}

func NormalizeBrowserDownloadConcurrency(value int) int {
	return normalizeBrowserDownloadConcurrency(value)
}

func findBrowserDownloadFFmpeg() (string, error) {
	if binary, err := exec.LookPath("ffmpeg"); err == nil {
		return binary, nil
	}
	if runtime.GOOS == "darwin" {
		for _, path := range []string{"/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg"} {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
		}
	}
	return "", errors.New("未找到 FFmpeg，无法下载网络流")
}

// limitedWriter 只留前 limit 字节：ffmpeg 出错时可能刷出很长的日志，
// 全存下来会把任务对象撑大，而有用的信息都在最前面。
type limitedWriter struct {
	target *strings.Builder
	limit  int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.target.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		w.target.Write(p[:remaining])
	}
	return len(p), nil
}

// BrowserDownloadImporterFromScan 把"下载完成后入库"接到既有扫描上（D-B05）。
// 下载目录不在扫描目录列表里时不报错也不入库——是否把它加进片库是用户的决定。
func BrowserDownloadImporterFromScan(videos *VideoService, directories func() ([]models.ScanDirectory, error)) func(string, string) (uint, error) {
	return func(directory, outputPath string) (uint, error) {
		if videos == nil || directories == nil {
			return 0, nil
		}
		dirs, err := directories()
		if err != nil {
			return 0, fmt.Errorf("读取扫描目录失败：%w", err)
		}
		if !BrowserDownloadDirectoryCovered(dirs, directory) {
			// 只给一句原因、不带路径：界面自己会说「文件已保存，但没有入库」（MEDIA-09）。
			return 0, ErrBrowserDownloadNotInScanRoots
		}
		result := videos.SyncAffectedDirectories(dirs, []string{directory})

		// 判据是"这一次下载的文件进没进库"，不是"整个目录扫得干不干净"。
		// 下载目录里别人的坏文件（下了一半的 mkv、损坏的分卷）扫描时照样会报错，
		// 那不是这次下载的失败——早先把任何扫描错误都算成入库失败，一次成功的
		// 下载会因为隔壁一个坏文件被判成失败。
		var imported models.Video
		err = database.DB.Where("path = ?", outputPath).First(&imported).Error
		if err == nil {
			if result != nil && len(result.Errors) > 0 {
				// 别人的错误如实记进日志，但不冒充这次任务的失败。
				log.Printf("下载入库成功，但同一目录里有 %d 个其他文件扫描出错，第一个：%s",
					len(result.Errors), result.Errors[0].Error)
			}
			return imported.ID, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, fmt.Errorf("确认入库结果失败：%w", err)
		}

		// 确实没进库：如果扫描正好为这个文件报了错，把那条原因带出来（擦掉路径）。
		if result != nil {
			for _, scanErr := range result.Errors {
				if scanErr.Path == outputPath {
					return 0, fmt.Errorf("扫描这个文件时出错：%s", scrubPlaybackProxyPaths(scanErr.Error))
				}
			}
		}
		return 0, errBrowserDownloadNotInLibrary
	}
}

// BrowserDownloadScanDirectoryCheck 把「把下载目录加入扫描目录」之前的预检归成一个动作结果（M-5）。
// validation 来自 DirectoryService.ValidateScanDirectory，excluded 表示目录落在扫描黑名单里
// （BrowserDownloadDirectoryExcluded）。
//
//   - 黑名单优先：目录在黑名单里时，不论加不加扫描目录都扫不到它，报 directory_excluded；
//   - 已在扫描范围内（与某个根相同，或在某个根之内）：covered=true、结果 ok，不必再加，直接重新入库；
//   - 目录包含已有的扫描根：加进去会互相嵌套，报 directory_nested，让用户去设置页自己决定；
//   - 目录已不存在：报 directory_missing。
//
// 文案不带路径（G-3）。
func BrowserDownloadScanDirectoryCheck(validation *ScanDirectoryValidation, excluded bool) (bool, BrowserDownloadActionResult) {
	switch {
	case excluded:
		return false, BrowserDownloadActionResult{Code: BrowserDownloadCodeDirectoryExcluded, Message: "下载目录在扫描黑名单里，加入扫描目录也不会入库，请先在设置页把它移出黑名单"}
	case validation == nil:
		return false, BrowserDownloadActionResult{Code: BrowserDownloadCodeDirectoryMissing, Message: "下载目录不可用"}
	case validation.DuplicateOf != "" || validation.NestedIn != "":
		return true, BrowserDownloadActionResult{Code: BrowserDownloadCodeOK}
	case len(validation.Contains) > 0:
		return false, BrowserDownloadActionResult{Code: BrowserDownloadCodeDirectoryNested, Message: "下载目录包含已有的扫描目录，直接加入会让扫描目录互相嵌套，请在设置页调整扫描目录"}
	case !validation.Exists:
		return false, BrowserDownloadActionResult{Code: BrowserDownloadCodeDirectoryMissing, Message: "下载目录已不存在"}
	}
	return false, BrowserDownloadActionResult{Code: BrowserDownloadCodeOK}
}

// BrowserDownloadDirectoryExcluded 报告下载目录是否落在扫描黑名单（Settings.ScanExcludePaths，
// 换行分隔）里：与扫描同一个判定（isScanPathExcluded）。
func BrowserDownloadDirectoryExcluded(scanExcludePaths, directory string) bool {
	return isScanPathExcluded(filepath.Clean(directory), parseScanExcludePaths(scanExcludePaths))
}

// BrowserDownloadDirectoryCovered 报告下载目录是否落在某个扫描目录之内（含相等）。
func BrowserDownloadDirectoryCovered(dirs []models.ScanDirectory, directory string) bool {
	target := filepath.Clean(directory)
	for _, dir := range dirs {
		root := filepath.Clean(dir.Path)
		if root == target {
			return true
		}
		if strings.HasPrefix(target, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
