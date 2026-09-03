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
)

// BrowserDownloadTask 是对外（桥接与设置页）暴露的任务快照。
type BrowserDownloadTask struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Filename   string `json:"filename"`
	OutputPath string `json:"output_path"`
	PageURL    string `json:"page_url"`
	State      string `json:"state"`
	// VideoID 是入库之后对应的片库记录。界面靠它取缩略图——几个任务并排时
	// 光看文件名分不清谁是谁。没入库时为 0。
	VideoID uint `json:"video_id"`
	// 只有"已处理时长"，没有总时长：HLS 的总时长要额外探一次才知道，当前没做这一步。
	// 与其留一个永远是 0 的 TotalSeconds 让界面拿去算出一个假的百分比，不如不给。
	ProcessedSeconds float64 `json:"processed_seconds"`
	BytesWritten     int64   `json:"bytes_written"`
	Error            string  `json:"error"`
	ImportError      string  `json:"import_error"`
	CreatedAt        int64   `json:"created_at"`
	UpdatedAt        int64   `json:"updated_at"`
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
	defer s.emitMu.Unlock()
	if s.emit == nil {
		return
	}
	now := s.deps.Now()
	if !force && now.Sub(s.lastEmit) < 500*time.Millisecond {
		return
	}
	s.lastEmit = now
	s.emit(s.ListTasks())
}

func NewBrowserDownloadService(deps BrowserDownloadDeps) *BrowserDownloadService {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.FFmpegPath == nil {
		deps.FFmpegPath = findBrowserDownloadFFmpeg
	}
	return &BrowserDownloadService{
		deps:    deps,
		entries: make(map[string]*browserDownloadEntry),
		baseCtx: context.Background(),
	}
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

	s.notify(true)
	s.pump()
	return snapshot, nil
}

func (s *BrowserDownloadService) ListTasks() []BrowserDownloadTask {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks := make([]BrowserDownloadTask, 0, len(s.entries))
	for _, entry := range s.entries {
		tasks = append(tasks, entry.task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt > tasks[j].CreatedAt })
	return tasks
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
	if entry.task.State == browserDownloadStateQueued {
		entry.task.State = browserDownloadStateCancel
		entry.task.UpdatedAt = s.deps.Now().UnixMilli()
		s.removeFromQueueLocked(id)
	}
	s.mu.Unlock()

	if cancel != nil {
		cancel()
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
		entry.task.State = browserDownloadStateRun
		entry.task.UpdatedAt = s.deps.Now().UnixMilli()
		s.running++
		s.mu.Unlock()

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
	s.update(id, func(task *BrowserDownloadTask) {
		task.State = browserDownloadStateDone
		task.VideoID = videoID
		// 入库失败与下载失败是两回事：文件已经在盘上了，别把它说成下载失败。
		if importErr != nil {
			task.ImportError = importErr.Error()
		}
	})
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
	before := entry.task.State
	mutate(&entry.task)
	entry.task.UpdatedAt = s.deps.Now().UnixMilli()
	stateChanged := entry.task.State != before
	s.mu.Unlock()

	// 回调在锁外投递：它会回到 Wails 的事件层，不该被下载队列的锁牵着走。
	s.notify(stateChanged)
}

func (s *BrowserDownloadService) fail(id string, message string) {
	s.update(id, func(task *BrowserDownloadTask) {
		task.State = browserDownloadStateFailed
		task.Error = message
	})
}

func (s *BrowserDownloadService) finishCanceled(id string) {
	s.update(id, func(task *BrowserDownloadTask) {
		if task.State != browserDownloadStateCancel {
			task.State = browserDownloadStateCancel
		}
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
		switch entry.task.State {
		case browserDownloadStateDone, browserDownloadStateFailed, browserDownloadStateCancel:
			terminal = append(terminal, entry.task)
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].CreatedAt < terminal[j].CreatedAt })
	excess := len(s.entries) - browserDownloadMaxTasks
	for index := 0; index < excess && index < len(terminal); index++ {
		delete(s.entries, terminal[index].ID)
	}
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
		if !browserDownloadDirectoryCovered(dirs, directory) {
			return 0, fmt.Errorf("下载目录不在片库扫描目录里，文件已保存但没有入库：%s", directory)
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

		// 确实没进库：如果扫描正好为这个文件报了错，把那条原因带出来。
		if result != nil {
			for _, scanErr := range result.Errors {
				if scanErr.Path == outputPath {
					return 0, fmt.Errorf("文件已保存，但入库失败：%s", scanErr.Error)
				}
			}
		}
		return 0, fmt.Errorf("文件已保存，但没有进入片库：%s", outputPath)
	}
}

func browserDownloadDirectoryCovered(dirs []models.ScanDirectory, directory string) bool {
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
