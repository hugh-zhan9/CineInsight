package services

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed face_worker.py
var faceWorkerScript string

// 运行时状态机（D-016）。available 之外的每个状态都要能对用户说清"缺什么"。
const (
	FaceRuntimeStateAvailable      = "available"
	FaceRuntimeStateMissingPython  = "missing_python"
	FaceRuntimeStateMissingVenv    = "missing_venv"
	FaceRuntimeStateMissingModel   = "missing_model"
	FaceRuntimeStateDownloadFailed = "download_failed"
	FaceRuntimeStateIncompatible   = "incompatible"
)

// ErrFaceRuntimeUnsupported 表示当前平台没有人脸 sidecar（非 macOS Apple Silicon）。
var ErrFaceRuntimeUnsupported = errors.New("人脸识别 sidecar 仅支持 macOS Apple Silicon")

// ErrFaceRuntimeUnavailable 表示运行时还没准备好，分析无从开始。
var ErrFaceRuntimeUnavailable = errors.New("人脸识别运行时未就绪")

// ErrFaceDataDirUnavailable 表示应用数据目录没解析出来（NewApp 的 dataDirErr 路径）。
//
// 这时 baseDir 是空串，filepath.Join 会给出 "face-runtime" / "faces" 这样的相对
// 路径——一旦拿它去 RemoveAll 或 MkdirAll，动的是进程 cwd 下的同名目录，也就是
// 仓库或用户当前目录。所有会删、会写的入口一律在这里挡住，不在相对路径上动手。
var ErrFaceDataDirUnavailable = errors.New("人脸数据目录不可用：应用数据目录未解析")

// FaceRuntimeStatus 是运行时对外的完整状态（7.2 `GetFaceRuntimeStatus`）。
type FaceRuntimeStatus struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	// Identity 是"这台机器上装的是哪一版"的确定答案（依赖版本 + 模型包）。
	Identity   string `json:"identity"`
	RuntimeDir string `json:"runtime_dir"`
	// ModelSourceURL 是本次会用的实际地址（已拼上镜像前缀），下载失败时界面要能
	// 把它原样显示出来，用户才知道该给哪个地址配镜像。
	ModelSourceURL   string `json:"model_source_url"`
	ModelSHA256      string `json:"model_sha256"`
	ModelArchiveSize string `json:"model_archive_size"`
	MirrorConfigured bool   `json:"mirror_configured"`
	// 准备过程（PrepareFaceRuntime）的进度。
	Preparing       bool   `json:"preparing"`
	Stage           string `json:"stage"`
	Message         string `json:"message"`
	DownloadedBytes int64  `json:"downloaded_bytes"`
	TotalBytes      int64  `json:"total_bytes"`
	Cancelled       bool   `json:"cancelled"`
	Error           string `json:"error"`
}

// FaceRuntime 管理人脸 sidecar 的托管 Python、venv、依赖与模型（D-016）。
//
// 与 WhisperX 一样是"显式准备"：不在启动时偷偷装东西，也不在分析时顺手补齐。
// 缺什么由 GetFaceRuntimeStatus 说清楚，用户按下"准备运行时"才动手。
type FaceRuntime struct {
	baseDir string
	// mirror 读设置里的镜像前缀。为 nil 时按"没配镜像"处理——运行时不应该因为
	// 读不到设置就完全不可用。
	mirror func() string

	mu        sync.Mutex
	preparing bool
	stage     string
	message   string
	// downloadFailed 是粘性标记：下载/校验失败后状态停在 download_failed，
	// 直到下一次准备开始或模型已经就位，用户才不会看到一个语焉不详的 missing_model。
	downloadFailed  bool
	failure         string
	cancelled       bool
	downloadedBytes int64
	totalBytes      int64
	cancel          context.CancelFunc
	emitter         func(FaceRuntimeStatus)
	worker          sync.WaitGroup

	// interpreterCache 缓存"这个解释器可用吗"的探测结果：真判据要起一个 python
	// 子进程问版本，而 Status() 会被设置页挂载与每一次准备进度事件反复调用，
	// 一次准备过程能问上几十遍（都没有解释器时一次要问八个候选）。
	//
	// 带 30 秒 TTL 而不是永久缓存：用户可能在应用运行期间自己装一个 Python，
	// 缓存不能把这件事挡在门外（与空闲探测的 30 秒缓存同一口径）。准备流程
	// 前后与 venv 刚建出来时另有显式失效。
	interpreterMu    sync.Mutex
	interpreterCache map[string]faceInterpreterProbe

	// 测试接缝。archiveSHA256 与 modelFiles 默认取自 manifest；让它们可替换是为了
	// 能在测试里用几十字节的假模型包走完"下载 → 校验 → 安装"这条路，
	// 真实清单里的两个文件加起来有 190 MB，测试里造不出内容对得上哈希的包。
	fetch         func(ctx context.Context, url string) (io.ReadCloser, int64, error)
	runPython     func(ctx context.Context, python string, args []string, env []string) ([]byte, error)
	supported     func() bool
	acceptPy      func(string) bool
	archiveSHA256 string
	modelFiles    []faceModelFile
}

// NewFaceRuntime 创建运行时管理器。dataDir 是应用数据根目录（~/.CineInsight）。
func NewFaceRuntime(dataDir string, mirror func() string) *FaceRuntime {
	return &FaceRuntime{
		baseDir:       dataDir,
		mirror:        mirror,
		fetch:         fetchFaceModelArchive,
		runPython:     runFacePythonCommand,
		supported:     faceRuntimePlatformSupported,
		acceptPy:      facePythonAcceptable,
		archiveSHA256: faceModelArchiveSHA256,
		modelFiles:    faceModelFiles,
	}
}

// SetEventEmitter 注入 face-runtime-state 事件发射器。
func (r *FaceRuntime) SetEventEmitter(emitter func(FaceRuntimeStatus)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.emitter = emitter
	r.mu.Unlock()
}

// RuntimeDir 返回运行时根目录；数据目录未解析时返回空串（见 ErrFaceDataDirUnavailable）。
func (r *FaceRuntime) RuntimeDir() string {
	if strings.TrimSpace(r.baseDir) == "" {
		return ""
	}
	return filepath.Join(r.baseDir, faceRuntimeDirName)
}

// DirAvailable 报告运行时目录是否落在真实的应用数据目录里。
func (r *FaceRuntime) DirAvailable() bool {
	return r != nil && r.RuntimeDir() != ""
}

// venvDir / modelDir / workerPath 在根目录不可用时一律返回空串：
// 空串传给 os.MkdirAll / os.RemoveAll 只会拿到一个明确的错误，
// 而 filepath.Join("", "venv") 会得到相对路径 "venv"，那才是危险的。
func (r *FaceRuntime) venvDir() string {
	if !r.DirAvailable() {
		return ""
	}
	return filepath.Join(r.RuntimeDir(), faceVenvDirName)
}

func (r *FaceRuntime) modelDir() string {
	if !r.DirAvailable() {
		return ""
	}
	return filepath.Join(r.RuntimeDir(), faceModelDirName)
}

// modelPackDir 是 insightface 找模型的位置：<root>/models/<pack>/。
func (r *FaceRuntime) modelPackDir() string {
	if dir := r.modelDir(); dir != "" {
		return filepath.Join(dir, faceModelPackName)
	}
	return ""
}

func (r *FaceRuntime) workerPath() string {
	if !r.DirAvailable() {
		return ""
	}
	return filepath.Join(r.RuntimeDir(), faceWorkerFileName)
}

func (r *FaceRuntime) venvPython() string {
	dir := r.venvDir()
	if dir == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "Scripts", "python.exe")
	}
	return filepath.Join(dir, "bin", "python3")
}

func (r *FaceRuntime) managedPython() string {
	if !r.DirAvailable() {
		return ""
	}
	return managedPythonPathIn(r.RuntimeDir())
}

// venvMarkerPath / modelMarkerPath 记录"这一版依赖/模型已经装好并自检通过"。
//
// 用标记文件而不是每次探测都跑一遍 import：导入 insightface 要一两秒，而设置页
// 每次打开都会问一次状态；模型文件有 174 MB，每次算 sha256 更不现实。哈希校验在
// 安装时做，之后靠标记 + 文件大小判定。用户手动删包/换文件会让标记失真，那时
// 分析启动会带着真实错误失败，而不是假装可用。
func (r *FaceRuntime) venvMarkerPath() string {
	if dir := r.venvDir(); dir != "" {
		return filepath.Join(dir, ".cineinsight-face-runtime")
	}
	return ""
}

func (r *FaceRuntime) modelMarkerPath() string {
	if dir := r.modelPackDir(); dir != "" {
		return filepath.Join(dir, ".cineinsight-face-models")
	}
	return ""
}

// faceInterpreterProbe 是一次解释器探测的结论与时间。
type faceInterpreterProbe struct {
	usable   bool
	probedAt time.Time
}

// faceInterpreterCacheTTL 是解释器探测结论的有效期。
const faceInterpreterCacheTTL = 30 * time.Second

// accept 是带缓存的解释器判据；所有判定都走它，不直接调 acceptPy。
func (r *FaceRuntime) accept(path string) bool {
	if path == "" {
		return false
	}
	now := time.Now()
	r.interpreterMu.Lock()
	if cached, ok := r.interpreterCache[path]; ok && now.Sub(cached.probedAt) < faceInterpreterCacheTTL {
		r.interpreterMu.Unlock()
		return cached.usable
	}
	r.interpreterMu.Unlock()

	result := r.acceptPy(path)

	r.interpreterMu.Lock()
	if r.interpreterCache == nil {
		r.interpreterCache = make(map[string]faceInterpreterProbe, 4)
	}
	r.interpreterCache[path] = faceInterpreterProbe{usable: result, probedAt: now}
	r.interpreterMu.Unlock()
	return result
}

// invalidateInterpreterCache 丢掉全部探测结论（装完/删掉解释器之后必须调用）。
func (r *FaceRuntime) invalidateInterpreterCache() {
	r.interpreterMu.Lock()
	r.interpreterCache = nil
	r.interpreterMu.Unlock()
}

func faceRuntimePlatformSupported() bool {
	return runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
}

// facePythonAcceptable 是人脸这一侧的解释器判据：3.10–3.13。
// 上限不是保守，是事实——onnxruntime 1.23.1 的 wheel 只发到 cp313。
func facePythonAcceptable(path string) bool {
	return pythonVersionInRange(path, facePythonMinMinor, facePythonMaxMinor)
}

func (r *FaceRuntime) mirrorPrefix() string {
	if r == nil || r.mirror == nil {
		return ""
	}
	return strings.TrimSpace(r.mirror())
}

// Status 返回当前状态（7.2 `GetFaceRuntimeStatus`）。
func (r *FaceRuntime) Status() FaceRuntimeStatus {
	if r == nil {
		return FaceRuntimeStatus{State: FaceRuntimeStateIncompatible, Reason: "人脸运行时未初始化"}
	}
	mirror := r.mirrorPrefix()
	status := FaceRuntimeStatus{
		Identity:         faceRuntimeIdentity,
		RuntimeDir:       r.RuntimeDir(),
		ModelSourceURL:   faceModelDownloadURL(mirror),
		ModelSHA256:      r.archiveSHA256,
		ModelArchiveSize: describeFaceModelSize(),
		MirrorConfigured: mirror != "",
	}
	r.mu.Lock()
	status.Preparing = r.preparing
	status.Stage = r.stage
	status.Message = r.message
	status.DownloadedBytes = r.downloadedBytes
	status.TotalBytes = r.totalBytes
	status.Cancelled = r.cancelled
	status.Error = r.failure
	downloadFailed := r.downloadFailed
	r.mu.Unlock()

	if !r.supported() {
		status.State = FaceRuntimeStateIncompatible
		status.Reason = fmt.Sprintf("人脸识别 sidecar 仅支持 macOS Apple Silicon（当前 %s/%s）", runtime.GOOS, runtime.GOARCH)
		return status
	}
	if !r.DirAvailable() {
		// 数据目录没解析出来（启动就已经报错的那条路径）：这时任何目录操作都会
		// 落在进程 cwd 上，宁可整块不可用，也不去动一个相对路径。
		status.State = FaceRuntimeStateIncompatible
		status.Reason = "应用数据目录未解析，人脸运行时不可用（请先解决启动错误）"
		return status
	}
	switch {
	case !r.venvReady():
		if !r.basePythonAvailable() {
			status.State = FaceRuntimeStateMissingPython
			status.Reason = fmt.Sprintf("没有可用的 Python %d.%d–%d.%d；准备运行时会下载一份托管解释器",
				3, facePythonMinMinor, 3, facePythonMaxMinor)
			return status
		}
		status.State = FaceRuntimeStateMissingVenv
		status.Reason = "人脸依赖尚未安装（onnxruntime、insightface 等）"
		return status
	case !r.modelReady():
		if downloadFailed {
			status.State = FaceRuntimeStateDownloadFailed
			status.Reason = "模型下载或校验失败：" + status.Error
			return status
		}
		status.State = FaceRuntimeStateMissingModel
		status.Reason = fmt.Sprintf("人脸模型（%s，%s）尚未下载", faceModelPackName, status.ModelArchiveSize)
		return status
	}
	status.State = FaceRuntimeStateAvailable
	status.Reason = ""
	return status
}

// Available 是给自动路径用的快捷判定（D-022：运行时不可用则静默跳过）。
func (r *FaceRuntime) Available() bool {
	return r != nil && r.Status().State == FaceRuntimeStateAvailable
}

func (r *FaceRuntime) basePythonAvailable() bool {
	return findPythonInterpreter(r.managedPython(), r.accept) != ""
}

func (r *FaceRuntime) venvReady() bool {
	marker := r.venvMarkerPath()
	if marker == "" || !r.accept(r.venvPython()) {
		return false
	}
	data, err := os.ReadFile(marker)
	return err == nil && strings.TrimSpace(string(data)) == faceRuntimeIdentity
}

func (r *FaceRuntime) modelReady() bool {
	marker := r.modelMarkerPath()
	if marker == "" {
		return false
	}
	data, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(data)) != faceRuntimeIdentity {
		return false
	}
	for _, file := range r.modelFiles {
		info, err := os.Stat(filepath.Join(r.modelPackDir(), file.Name))
		if err != nil || info.Size() != file.Bytes {
			return false
		}
	}
	return true
}

// WorkerEnvironment 返回启动 worker 所需的解释器、脚本与环境变量。
// 运行时不可用时返回 ErrFaceRuntimeUnavailable，绝不"边跑边装"。
func (r *FaceRuntime) WorkerEnvironment() (python string, script string, env []string, err error) {
	if r == nil {
		return "", "", nil, ErrFaceRuntimeUnavailable
	}
	status := r.Status()
	if status.State != FaceRuntimeStateAvailable {
		return "", "", nil, fmt.Errorf("%w：%s", ErrFaceRuntimeUnavailable, status.Reason)
	}
	if err := r.writeWorkerScript(); err != nil {
		return "", "", nil, err
	}
	return r.venvPython(), r.workerPath(), r.workerEnv(), nil
}

func (r *FaceRuntime) workerEnv() []string {
	return append(os.Environ(),
		"PYTHONUNBUFFERED=1",
		"CINEINSIGHT_FACE_MODEL_ROOT="+r.RuntimeDir(),
		"CINEINSIGHT_FACE_MODEL_PACK="+faceModelPackName,
		"CINEINSIGHT_FACE_DET_SIZE=640",
	)
}

func (r *FaceRuntime) pipEnv() []string {
	return append(os.Environ(),
		"PIP_DISABLE_PIP_VERSION_CHECK=1",
		"PIP_PROGRESS_BAR=off",
		"PYTHONUNBUFFERED=1",
	)
}

// writeWorkerScript 把 embed 的 worker 落到运行时目录（内容一致就不重写）。
func (r *FaceRuntime) writeWorkerScript() error {
	if !r.DirAvailable() {
		return ErrFaceDataDirUnavailable
	}
	if err := os.MkdirAll(r.RuntimeDir(), 0o755); err != nil {
		return err
	}
	path := r.workerPath()
	if data, err := os.ReadFile(path); err == nil && string(data) == faceWorkerScript {
		return nil
	}
	return os.WriteFile(path, []byte(faceWorkerScript), 0o644)
}

// Prepare 显式准备运行时（7.2 `PrepareFaceRuntime`）。已在准备中时返回当前状态。
func (r *FaceRuntime) Prepare(parent context.Context) (FaceRuntimeStatus, error) {
	if r == nil {
		return FaceRuntimeStatus{}, ErrFaceRuntimeUnavailable
	}
	if !r.supported() {
		return r.Status(), ErrFaceRuntimeUnsupported
	}
	if !r.DirAvailable() {
		return r.Status(), ErrFaceDataDirUnavailable
	}
	if parent == nil {
		parent = context.Background()
	}
	r.mu.Lock()
	if r.preparing {
		r.mu.Unlock()
		return r.Status(), nil
	}
	ctx, cancel := context.WithCancel(parent)
	r.preparing = true
	r.stage = "python"
	r.message = "正在准备 Python 运行时…"
	r.cancelled = false
	r.failure = ""
	r.downloadFailed = false
	r.downloadedBytes = 0
	r.totalBytes = 0
	r.cancel = cancel
	r.worker.Add(1)
	r.mu.Unlock()
	// 准备开始与结束各清一次缓存：这一趟正是解释器会被装出来/换掉的时候。
	r.invalidateInterpreterCache()
	r.emit()

	go func() {
		defer r.worker.Done()
		defer cancel()
		err := r.prepare(ctx)
		r.invalidateInterpreterCache()
		r.mu.Lock()
		r.preparing = false
		r.cancel = nil
		switch {
		case err == nil:
			r.stage = "done"
			r.message = "人脸识别运行时已就绪"
			r.failure = ""
		case ctx.Err() != nil:
			r.cancelled = true
			r.stage = "cancelled"
			r.message = "已取消准备"
		default:
			r.stage = "failed"
			r.message = "准备失败"
			r.failure = err.Error()
			r.downloadFailed = errors.Is(err, errFaceModelDownload)
		}
		r.mu.Unlock()
		r.emit()
	}()
	return r.Status(), nil
}

// CancelPrepare 取消正在进行的准备（7.2 `CancelFaceRuntimePrepare`）。
func (r *FaceRuntime) CancelPrepare() error {
	if r == nil {
		return ErrFaceRuntimeUnavailable
	}
	r.mu.Lock()
	cancel := r.cancel
	preparing := r.preparing
	r.mu.Unlock()
	if !preparing || cancel == nil {
		return errors.New("当前没有正在进行的人脸运行时准备")
	}
	cancel()
	return nil
}

// StopAndWait 在应用退出时等准备协程收尾。
func (r *FaceRuntime) StopAndWait() {
	if r == nil {
		return
	}
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.worker.Wait()
}

func (r *FaceRuntime) setStage(stage, message string) {
	r.mu.Lock()
	r.stage = stage
	r.message = message
	r.mu.Unlock()
	r.emit()
}

func (r *FaceRuntime) setProgress(downloaded, total int64) {
	r.mu.Lock()
	r.downloadedBytes = downloaded
	r.totalBytes = total
	r.mu.Unlock()
	r.emit()
}

func (r *FaceRuntime) emit() {
	r.mu.Lock()
	emitter := r.emitter
	r.mu.Unlock()
	if emitter == nil {
		return
	}
	emitter(r.Status())
}

func (r *FaceRuntime) prepare(ctx context.Context) error {
	if !r.DirAvailable() {
		return ErrFaceDataDirUnavailable
	}
	if err := os.MkdirAll(r.RuntimeDir(), 0o755); err != nil {
		return err
	}
	if err := r.writeWorkerScript(); err != nil {
		return err
	}
	if !r.venvReady() {
		if err := r.ensureVenv(ctx); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !r.modelReady() {
		r.setStage("model", "正在下载人脸模型（"+describeFaceModelSize()+"）…")
		if err := r.installModels(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *FaceRuntime) ensureVenv(ctx context.Context) error {
	// 这里会 RemoveAll(venvDir) 并往 venv 里装东西：目录不可用时绝不进这一步。
	if !r.DirAvailable() {
		return ErrFaceDataDirUnavailable
	}
	basePython := findPythonInterpreter(r.managedPython(), r.accept)
	if basePython == "" {
		r.setStage("python", "正在下载托管 Python…")
		requirement := fmt.Sprintf("Python 3.%d–3.%d", facePythonMinMinor, facePythonMaxMinor)
		// 这里有意传未加缓存的 acceptPy：下载器在解压前后各探一次，
		// 拿缓存的旧结论会把"刚解出来的解释器"判成不可用。
		downloaded, err := ensureManagedPythonRuntime(ctx, r.RuntimeDir(), "人脸识别", requirement, r.acceptPy)
		if err != nil {
			return err
		}
		r.invalidateInterpreterCache()
		basePython = downloaded
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	venvPython := r.venvPython()
	if !r.accept(venvPython) {
		r.setStage("venv", "正在创建虚拟环境…")
		_ = os.RemoveAll(r.venvDir())
		r.invalidateInterpreterCache()
		if output, err := r.runPython(ctx, basePython, []string{"-m", "venv", r.venvDir()}, r.pipEnv()); err != nil {
			return fmt.Errorf("创建人脸虚拟环境失败: %s", truncateLogSnippet(strings.TrimSpace(string(output)), 400))
		}
		// 解释器刚被创建出来：上一句的结论已经过期。
		r.invalidateInterpreterCache()
		if !r.accept(venvPython) {
			return errors.New("人脸虚拟环境创建后未找到可用的 python 可执行文件")
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	r.setStage("deps", "正在安装人脸识别依赖…")
	env := r.pipEnv()
	if output, err := r.runPython(ctx, venvPython, []string{"-m", "pip", "install", "--upgrade", "pip"}, env); err != nil {
		return fmt.Errorf("升级 pip 失败: %s", truncateLogSnippet(strings.TrimSpace(string(output)), 400))
	}
	install := append([]string{"-m", "pip", "install"}, faceRuntimePackages...)
	if output, err := r.runPython(ctx, venvPython, install, env); err != nil {
		return fmt.Errorf("安装人脸识别依赖失败: %s", truncateLogSnippet(strings.TrimSpace(string(output)), 400))
	}
	// insightface 单独装且不解依赖：它要 opencv-python（带 GUI 的那份），
	// 与已装的 headless 提供同一个 cv2，两个都装会互相覆盖文件。
	noDeps := append([]string{"-m", "pip", "install", "--no-deps"}, faceRuntimeNoDepsPackages...)
	if output, err := r.runPython(ctx, venvPython, noDeps, env); err != nil {
		return fmt.Errorf("安装 insightface 失败: %s", truncateLogSnippet(strings.TrimSpace(string(output)), 400))
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	r.setStage("verify", "正在自检依赖…")
	check := []string{"-c", "import onnxruntime, numpy, cv2, insightface; print('ok')"}
	output, err := r.runPython(ctx, venvPython, check, env)
	if err != nil || !strings.Contains(string(output), "ok") {
		return fmt.Errorf("人脸依赖自检失败: %s", truncateLogSnippet(strings.TrimSpace(string(output)), 400))
	}
	if err := os.WriteFile(r.venvMarkerPath(), []byte(faceRuntimeIdentity), 0o644); err != nil {
		return err
	}
	return nil
}

// errFaceModelDownload 把"下载/校验模型这一步失败"与其他失败区分开，
// 状态机据此进入 download_failed（4.4.4）。
var errFaceModelDownload = errors.New("人脸模型下载失败")

func (r *FaceRuntime) installModels(ctx context.Context) error {
	if !r.DirAvailable() {
		return ErrFaceDataDirUnavailable
	}
	mirror := r.mirrorPrefix()
	url := faceModelDownloadURL(mirror)
	tempDir, err := os.MkdirTemp("", "cineinsight-face-models-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	archivePath := filepath.Join(tempDir, "models.zip")
	if err := r.downloadArchive(ctx, url, archivePath); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", errFaceModelDownload, err)
	}

	r.setStage("verify", "正在校验人脸模型…")
	staged := filepath.Join(tempDir, "staged")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		return err
	}
	if err := extractFaceModels(archivePath, staged, r.modelFiles); err != nil {
		return fmt.Errorf("%w: %v", errFaceModelDownload, err)
	}

	r.setStage("install", "正在安装人脸模型…")
	if err := os.MkdirAll(r.modelPackDir(), 0o755); err != nil {
		return err
	}
	for _, file := range r.modelFiles {
		if err := replaceEnhancementModelFile(filepath.Join(staged, file.Name), filepath.Join(r.modelPackDir(), file.Name)); err != nil {
			return err
		}
	}
	if err := os.WriteFile(r.modelMarkerPath(), []byte(faceRuntimeIdentity), 0o644); err != nil {
		return err
	}
	// 只记文件数与来源主机，不记完整路径（D-020）。
	log.Printf("[Face] 模型安装完成 files=%d mirror=%v", len(r.modelFiles), mirror != "")
	return nil
}

func (r *FaceRuntime) downloadArchive(ctx context.Context, url, destination string) error {
	body, total, err := r.fetch(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()
	if total > faceModelArchiveMaxSize {
		return fmt.Errorf("模型包体积异常（%d 字节），拒绝下载", total)
	}
	if total <= 0 {
		total = faceModelArchiveSize
	}
	r.setProgress(0, total)

	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	digest := sha256.New()
	counter := &faceDownloadCounter{onProgress: func(n int64) { r.setProgress(n, total) }}
	_, copyErr := io.Copy(io.MultiWriter(file, digest, counter), io.LimitReader(body, faceModelArchiveMaxSize+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if actual := hex.EncodeToString(digest.Sum(nil)); !strings.EqualFold(actual, r.archiveSHA256) {
		return fmt.Errorf("模型包校验失败：sha256 与清单不符（来源 %s）", url)
	}
	r.setProgress(counter.total, total)
	return nil
}

// extractFaceModels 只取清单声明的两个模型文件并逐个比对 sha256。
// 用 path.Base 抹掉压缩包里的目录结构，避免 zip slip。
func extractFaceModels(archivePath, destDir string, files []faceModelFile) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("模型包无法解压: %w", err)
	}
	defer reader.Close()

	wanted := make(map[string]faceModelFile, len(files))
	for _, file := range files {
		wanted[file.Name] = file
	}
	found := make(map[string]struct{}, len(wanted))
	for _, entry := range reader.File {
		name := path.Base(entry.Name)
		expected, needed := wanted[name]
		if !needed {
			continue
		}
		if _, exists := found[name]; exists {
			continue
		}
		if entry.UncompressedSize64 > uint64(faceModelFileMaxSize) {
			return fmt.Errorf("模型文件体积异常: %s", name)
		}
		source, err := entry.Open()
		if err != nil {
			return err
		}
		target, err := os.Create(filepath.Join(destDir, name))
		if err != nil {
			source.Close()
			return err
		}
		digest := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(target, digest), io.LimitReader(source, faceModelFileMaxSize))
		source.Close()
		closeErr := target.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if actual := hex.EncodeToString(digest.Sum(nil)); !strings.EqualFold(actual, expected.SHA256) {
			return fmt.Errorf("模型文件校验失败: %s", name)
		}
		found[name] = struct{}{}
	}
	if len(found) != len(wanted) {
		missing := make([]string, 0, len(wanted))
		for name := range wanted {
			if _, ok := found[name]; !ok {
				missing = append(missing, name)
			}
		}
		return fmt.Errorf("模型包里缺少所需文件: %s", strings.Join(missing, ", "))
	}
	return nil
}

type faceDownloadCounter struct {
	total      int64
	lastEmit   time.Time
	onProgress func(int64)
}

func (c *faceDownloadCounter) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	// 限流：275 MB 会有成千上万次 Write，每次都发事件会把前端淹掉。
	if c.onProgress != nil && time.Since(c.lastEmit) > 200*time.Millisecond {
		c.lastEmit = time.Now()
		c.onProgress(c.total)
	}
	return len(p), nil
}

// faceModelHTTPClient 是模型下载专用客户端。
//
// 只设响应头超时、不设总超时：275 MB 在慢网上要下很久，一刀切的总超时会把
// 正常下载砍掉；而"连上了却半天不给响应头"是真卡死，本次实施就撞到过一次
// GitHub 直连挂住不动。整体中止靠 ctx（CancelFaceRuntimePrepare）。
var faceModelHTTPClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: transport}
}()

// fetchFaceModelArchive 是本切片唯一的网络出口：显式触发的模型包下载（D-039）。
// 注意准备运行时整体上还会经 pip 访问 PyPI、必要时下载托管 Python（都在
// PrepareFaceRuntime 里，设置页的披露文案如实写明）。
func fetchFaceModelArchive(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	response, err := faceModelHTTPClient.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("下载人脸模型失败: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, 0, fmt.Errorf("下载人脸模型失败：上游返回 %d（%s）", response.StatusCode, url)
	}
	return response.Body, response.ContentLength, nil
}

func runFacePythonCommand(ctx context.Context, python string, args []string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, python, args...)
	cmd.Env = env
	return cmd.CombinedOutput()
}
