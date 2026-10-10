package services

import (
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
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed scene_worker.py
var sceneWorkerScript string

// 场景检索运行时的状态机，与人脸运行时同一套取值。
const (
	SceneRuntimeStateAvailable      = "available"
	SceneRuntimeStateMissingPython  = "missing_python"
	SceneRuntimeStateMissingVenv    = "missing_venv"
	SceneRuntimeStateMissingModel   = "missing_model"
	SceneRuntimeStateDownloadFailed = "download_failed"
	SceneRuntimeStateIncompatible   = "incompatible"
)

var (
	// ErrSceneRuntimeUnavailable 表示本地运行时没有准备好（错误码 scene_runtime_unavailable）。
	ErrSceneRuntimeUnavailable = errors.New("scene_runtime_unavailable")
	// ErrSceneRuntimeUnsupported 表示当前平台没有场景检索 sidecar（非 macOS Apple Silicon）。
	ErrSceneRuntimeUnsupported = errors.New("scene_runtime_unsupported")
	// ErrSceneDataDirUnavailable 表示应用数据目录没解析出来；此时一律不在相对路径上动手。
	ErrSceneDataDirUnavailable = errors.New("scene_data_dir_unavailable")
	// errSceneModelDownload 区分"下载/校验模型失败"，状态机据此进入 download_failed。
	errSceneModelDownload = errors.New("scene_model_download_failed")
)

// SceneRuntimeStatus 是 GetSceneRuntimeStatus 与 scene-runtime-state 事件的载荷。
type SceneRuntimeStatus struct {
	State            string `json:"state"`
	Reason           string `json:"reason"`
	Identity         string `json:"identity"`
	ModelID          string `json:"model_id"`
	RuntimeDir       string `json:"runtime_dir"`
	ModelHost        string `json:"model_host"`
	ModelSize        string `json:"model_size"`
	MirrorConfigured bool   `json:"mirror_configured"`
	Preparing        bool   `json:"preparing"`
	Stage            string `json:"stage"`
	Message          string `json:"message"`
	DownloadedBytes  int64  `json:"downloaded_bytes"`
	TotalBytes       int64  `json:"total_bytes"`
	Cancelled        bool   `json:"cancelled"`
	Error            string `json:"error"`
}

// SceneRuntime 管理场景检索 sidecar 的托管 Python、venv、依赖与模型（仿 FaceRuntime）。
//
// 显式准备：不在启动或检索时偷偷安装；缺什么由 Status 说清楚，PrepareSceneRuntime 才动手。
// 网络出口只有三处：PyPI、必要时下载托管 Python、上述三个模型文件。
type SceneRuntime struct {
	baseDir string
	mirror  func() string

	mu              sync.Mutex
	preparing       bool
	stage           string
	message         string
	downloadFailed  bool
	failure         string
	cancelled       bool
	downloadedBytes int64
	totalBytes      int64
	cancel          context.CancelFunc
	emitter         func(SceneRuntimeStatus)
	worker          sync.WaitGroup

	interpreterMu    sync.Mutex
	interpreterCache map[string]faceInterpreterProbe
	observedMu       sync.Mutex
	observedStatus   *SceneRuntimeStatus
	observedAt       time.Time

	// 测试接缝。
	fetch      func(ctx context.Context, url string) (io.ReadCloser, int64, error)
	runPython  func(ctx context.Context, python string, args []string, env []string) ([]byte, error)
	supported  func() bool
	acceptPy   func(string) bool
	modelFiles []sceneModelFile
}

// NewSceneRuntime 创建运行时管理器。dataDir 是应用数据根目录；mirror 读设置里的镜像主机。
func NewSceneRuntime(dataDir string, mirror func() string) *SceneRuntime {
	return &SceneRuntime{
		baseDir:    dataDir,
		mirror:     mirror,
		fetch:      fetchSceneModelFile,
		runPython:  runFacePythonCommand,
		supported:  faceRuntimePlatformSupported,
		acceptPy:   scenePythonAcceptable,
		modelFiles: sceneModelFiles,
	}
}

func scenePythonAcceptable(path string) bool {
	return pythonVersionInRange(path, scenePythonMinMinor, scenePythonMaxMinor)
}

// SetEventEmitter 注入 scene-runtime-state 事件发射器。
func (r *SceneRuntime) SetEventEmitter(emitter func(SceneRuntimeStatus)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.emitter = emitter
	r.mu.Unlock()
}

// RuntimeDir 返回运行时根目录；数据目录未解析时返回空串。
func (r *SceneRuntime) RuntimeDir() string {
	if r == nil || strings.TrimSpace(r.baseDir) == "" {
		return ""
	}
	return filepath.Join(r.baseDir, sceneRuntimeDirName)
}

// DirAvailable 报告运行时目录是否落在真实的应用数据目录里。
func (r *SceneRuntime) DirAvailable() bool { return r.RuntimeDir() != "" }

// sub 在根目录不可用时返回空串，绝不返回相对路径（理由同 ErrFaceDataDirUnavailable）。
func (r *SceneRuntime) sub(parts ...string) string {
	root := r.RuntimeDir()
	if root == "" {
		return ""
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

func (r *SceneRuntime) venvDir() string    { return r.sub(sceneVenvDirName) }
func (r *SceneRuntime) modelDir() string   { return r.sub(sceneModelsDirName, sceneModelPackName) }
func (r *SceneRuntime) workerPath() string { return r.sub(sceneWorkerFileName) }

// TempDir 是抽帧临时目录的父目录（<runtime>/.tmp）。
func (r *SceneRuntime) TempDir() string { return r.sub(sceneTempDirName) }

func (r *SceneRuntime) venvPython() string {
	dir := r.venvDir()
	if dir == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "Scripts", "python.exe")
	}
	return filepath.Join(dir, "bin", "python3")
}

func (r *SceneRuntime) managedPython() string {
	if !r.DirAvailable() {
		return ""
	}
	return managedPythonPathIn(r.RuntimeDir())
}

func (r *SceneRuntime) venvMarkerPath() string {
	if dir := r.venvDir(); dir != "" {
		return filepath.Join(dir, ".cineinsight-scene-runtime")
	}
	return ""
}

func (r *SceneRuntime) modelMarkerPath() string {
	if dir := r.modelDir(); dir != "" {
		return filepath.Join(dir, ".cineinsight-scene-model")
	}
	return ""
}

// accept 是带 30 秒缓存的解释器判据（同人脸运行时的理由：Status 会被反复调用）。
func (r *SceneRuntime) accept(path string) bool {
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

func (r *SceneRuntime) invalidateInterpreterCache() {
	r.interpreterMu.Lock()
	r.interpreterCache = nil
	r.interpreterMu.Unlock()
}

func (r *SceneRuntime) mirrorHost() string {
	if r == nil || r.mirror == nil {
		return ""
	}
	return strings.TrimSpace(r.mirror())
}

// Status 返回当前状态，并记下这次检查的结果供健康面板只读。
func (r *SceneRuntime) Status() SceneRuntimeStatus {
	status := r.probeStatus()
	if r != nil {
		r.observedMu.Lock()
		r.observedStatus, r.observedAt = &status, time.Now()
		r.observedMu.Unlock()
	}
	return status
}

// CachedStatus 返回最近一次 Status 的结果；不启动 Python、不读文件、不读设置。
func (r *SceneRuntime) CachedStatus() (SceneRuntimeStatus, time.Time, bool) {
	if r == nil {
		return SceneRuntimeStatus{}, time.Time{}, false
	}
	r.observedMu.Lock()
	defer r.observedMu.Unlock()
	if r.observedStatus == nil {
		return SceneRuntimeStatus{}, time.Time{}, false
	}
	return *r.observedStatus, r.observedAt, true
}

func (r *SceneRuntime) probeStatus() SceneRuntimeStatus {
	if r == nil {
		return SceneRuntimeStatus{State: SceneRuntimeStateIncompatible, Reason: "场景检索运行时未初始化"}
	}
	mirror := r.mirrorHost()
	status := SceneRuntimeStatus{
		Identity:         sceneRuntimeIdentity,
		ModelID:          SceneLocalModelID,
		RuntimeDir:       r.RuntimeDir(),
		ModelHost:        sceneModelHost(mirror),
		ModelSize:        describeSceneModelSize(r.modelFiles),
		MirrorConfigured: mirror != "",
	}
	r.mu.Lock()
	status.Preparing, status.Stage, status.Message = r.preparing, r.stage, r.message
	status.DownloadedBytes, status.TotalBytes = r.downloadedBytes, r.totalBytes
	status.Cancelled, status.Error = r.cancelled, r.failure
	downloadFailed := r.downloadFailed
	r.mu.Unlock()

	if !r.supported() {
		status.State = SceneRuntimeStateIncompatible
		status.Reason = fmt.Sprintf("场景检索的本地模型仅支持 macOS Apple Silicon（当前 %s/%s）", runtime.GOOS, runtime.GOARCH)
		return status
	}
	if !r.DirAvailable() {
		status.State = SceneRuntimeStateIncompatible
		status.Reason = "应用数据目录未解析，场景检索运行时不可用（请先解决启动错误）"
		return status
	}
	switch {
	case !r.venvReady():
		if findPythonInterpreter(r.managedPython(), r.accept) == "" {
			status.State = SceneRuntimeStateMissingPython
			status.Reason = fmt.Sprintf("没有可用的 Python 3.%d–3.%d；准备模型时会下载一份托管解释器", scenePythonMinMinor, scenePythonMaxMinor)
			return status
		}
		status.State = SceneRuntimeStateMissingVenv
		status.Reason = "场景检索依赖尚未安装（onnxruntime、tokenizers 等）"
	case !r.modelReady():
		if downloadFailed {
			status.State = SceneRuntimeStateDownloadFailed
			status.Reason = "模型下载或校验失败：" + status.Error
			return status
		}
		status.State = SceneRuntimeStateMissingModel
		status.Reason = fmt.Sprintf("场景检索模型（Chinese-CLIP，约 %s）尚未下载", status.ModelSize)
	default:
		status.State = SceneRuntimeStateAvailable
	}
	return status
}

// Available 是建索引与检索前的快捷判定。
func (r *SceneRuntime) Available() bool {
	return r != nil && r.Status().State == SceneRuntimeStateAvailable
}

func (r *SceneRuntime) venvReady() bool {
	marker := r.venvMarkerPath()
	if marker == "" || !r.accept(r.venvPython()) {
		return false
	}
	data, err := os.ReadFile(marker)
	return err == nil && strings.TrimSpace(string(data)) == sceneRuntimeIdentity
}

// modelReady 靠标记文件 + 文件大小判定；哈希只在下载时校验（同人脸运行时）。
func (r *SceneRuntime) modelReady() bool {
	marker := r.modelMarkerPath()
	if marker == "" {
		return false
	}
	data, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(data)) != sceneRuntimeIdentity {
		return false
	}
	for _, file := range r.modelFiles {
		info, err := os.Stat(filepath.Join(r.modelDir(), file.Name))
		if err != nil || info.Size() != file.Bytes {
			return false
		}
	}
	return true
}

// WorkerEnvironment 返回启动 worker 所需的解释器、脚本与环境变量；运行时不可用时
// 返回 ErrSceneRuntimeUnavailable，绝不边跑边装。
func (r *SceneRuntime) WorkerEnvironment() (python string, script string, env []string, err error) {
	if r == nil {
		return "", "", nil, ErrSceneRuntimeUnavailable
	}
	if status := r.Status(); status.State != SceneRuntimeStateAvailable {
		return "", "", nil, ErrSceneRuntimeUnavailable
	}
	if err := r.writeWorkerScript(); err != nil {
		return "", "", nil, err
	}
	env = append(os.Environ(), "PYTHONUNBUFFERED=1", "CINEINSIGHT_SCENE_MODEL_DIR="+r.modelDir())
	return r.venvPython(), r.workerPath(), env, nil
}

func (r *SceneRuntime) pipEnv() []string {
	return append(os.Environ(), "PIP_DISABLE_PIP_VERSION_CHECK=1", "PIP_PROGRESS_BAR=off", "PYTHONUNBUFFERED=1")
}

// writeWorkerScript 把 embed 的 worker 落到运行时目录（内容一致就不重写）。
func (r *SceneRuntime) writeWorkerScript() error {
	if !r.DirAvailable() {
		return ErrSceneDataDirUnavailable
	}
	if err := os.MkdirAll(r.RuntimeDir(), 0o755); err != nil {
		return err
	}
	path := r.workerPath()
	if data, err := os.ReadFile(path); err == nil && string(data) == sceneWorkerScript {
		return nil
	}
	return os.WriteFile(path, []byte(sceneWorkerScript), 0o644)
}

// Prepare 显式准备运行时（PrepareSceneRuntime）。已在准备中时返回当前状态。
func (r *SceneRuntime) Prepare(parent context.Context) (SceneRuntimeStatus, error) {
	if r == nil {
		return SceneRuntimeStatus{}, ErrSceneRuntimeUnavailable
	}
	if !r.supported() {
		return r.Status(), ErrSceneRuntimeUnsupported
	}
	if !r.DirAvailable() {
		return r.Status(), ErrSceneDataDirUnavailable
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
	r.preparing, r.stage, r.message = true, "python", "正在准备 Python 运行时…"
	r.cancelled, r.failure, r.downloadFailed = false, "", false
	r.downloadedBytes, r.totalBytes = 0, 0
	r.cancel = cancel
	r.worker.Add(1)
	r.mu.Unlock()
	r.invalidateInterpreterCache()
	r.emit()

	go func() {
		defer r.worker.Done()
		defer cancel()
		err := r.prepare(ctx)
		r.invalidateInterpreterCache()
		r.mu.Lock()
		r.preparing, r.cancel = false, nil
		switch {
		case err == nil:
			r.stage, r.message, r.failure = "done", "场景检索模型已就绪", ""
		case ctx.Err() != nil:
			r.cancelled, r.stage, r.message = true, "cancelled", "已取消准备"
		default:
			r.stage, r.message = "failed", "准备失败"
			r.failure = redactFrameHashErrorPaths(boundedError(err, 600))
			r.downloadFailed = errors.Is(err, errSceneModelDownload)
		}
		r.mu.Unlock()
		r.emit()
	}()
	return r.Status(), nil
}

// CancelPrepare 取消正在进行的准备（CancelSceneRuntimePrepare）。
func (r *SceneRuntime) CancelPrepare() error {
	if r == nil {
		return ErrSceneRuntimeUnavailable
	}
	r.mu.Lock()
	cancel, preparing := r.cancel, r.preparing
	r.mu.Unlock()
	if !preparing || cancel == nil {
		return errors.New("scene_runtime_not_preparing")
	}
	cancel()
	return nil
}

// StopAndWait 在应用退出时等准备协程收尾。
func (r *SceneRuntime) StopAndWait() {
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

func (r *SceneRuntime) setStage(stage, message string) {
	r.mu.Lock()
	r.stage, r.message = stage, message
	r.mu.Unlock()
	r.emit()
}

func (r *SceneRuntime) setProgress(downloaded, total int64) {
	r.mu.Lock()
	r.downloadedBytes, r.totalBytes = downloaded, total
	r.mu.Unlock()
	r.emit()
}

func (r *SceneRuntime) emit() {
	r.mu.Lock()
	emitter := r.emitter
	r.mu.Unlock()
	if emitter != nil {
		emitter(r.Status())
	}
}

func (r *SceneRuntime) prepare(ctx context.Context) error {
	if !r.DirAvailable() {
		return ErrSceneDataDirUnavailable
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
		r.setStage("model", "正在下载场景检索模型（"+describeSceneModelSize(r.modelFiles)+"）…")
		if err := r.installModels(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *SceneRuntime) ensureVenv(ctx context.Context) error {
	if !r.DirAvailable() {
		return ErrSceneDataDirUnavailable
	}
	basePython := findPythonInterpreter(r.managedPython(), r.accept)
	if basePython == "" {
		r.setStage("python", "正在下载托管 Python…")
		requirement := fmt.Sprintf("Python 3.%d–3.%d", scenePythonMinMinor, scenePythonMaxMinor)
		downloaded, err := ensureManagedPythonRuntime(ctx, r.RuntimeDir(), "场景检索", requirement, r.acceptPy)
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
			return fmt.Errorf("创建场景检索虚拟环境失败: %s", truncateLogSnippet(string(output), 400))
		}
		r.invalidateInterpreterCache()
		if !r.accept(venvPython) {
			return errors.New("场景检索虚拟环境创建后未找到可用的 python 可执行文件")
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.setStage("deps", "正在安装场景检索依赖…")
	env := r.pipEnv()
	if output, err := r.runPython(ctx, venvPython, []string{"-m", "pip", "install", "--upgrade", "pip"}, env); err != nil {
		return fmt.Errorf("升级 pip 失败: %s", truncateLogSnippet(string(output), 400))
	}
	install := append([]string{"-m", "pip", "install"}, sceneRuntimePackages...)
	if output, err := r.runPython(ctx, venvPython, install, env); err != nil {
		return fmt.Errorf("安装场景检索依赖失败: %s", truncateLogSnippet(string(output), 400))
	}
	r.setStage("verify", "正在自检依赖…")
	check := []string{"-c", "import onnxruntime, numpy, PIL, tokenizers; print('ok')"}
	output, err := r.runPython(ctx, venvPython, check, env)
	if err != nil || !strings.Contains(string(output), "ok") {
		return fmt.Errorf("场景检索依赖自检失败: %s", truncateLogSnippet(string(output), 400))
	}
	return os.WriteFile(r.venvMarkerPath(), []byte(sceneRuntimeIdentity), 0o644)
}

// installModels 逐个下载清单里的模型文件、校验 sha256 后换入模型目录，最后写标记。
// 任何一个校验失败整批丢弃（暂存目录随函数返回删除）。
func (r *SceneRuntime) installModels(ctx context.Context) error {
	if !r.DirAvailable() {
		return ErrSceneDataDirUnavailable
	}
	mirror := r.mirrorHost()
	parent := r.sub(sceneModelsDirName)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	total := sceneModelTotalBytes(r.modelFiles)
	var done int64
	r.setProgress(0, total)
	for _, file := range r.modelFiles {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		written, err := r.downloadModelFile(ctx, sceneModelFileURL(mirror, file), filepath.Join(staging, file.Name), file, func(n int64) {
			r.setProgress(done+n, total)
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: %s: %v", errSceneModelDownload, file.Name, err)
		}
		done += written
		r.setProgress(done, total)
	}
	r.setStage("install", "正在安装场景检索模型…")
	if err := os.MkdirAll(r.modelDir(), 0o755); err != nil {
		return err
	}
	// 先撤标记再换文件：换到一半失败时状态是 missing_model，而不是一份半新半旧的"可用"。
	_ = os.Remove(r.modelMarkerPath())
	for _, file := range r.modelFiles {
		if err := replaceEnhancementModelFile(filepath.Join(staging, file.Name), filepath.Join(r.modelDir(), file.Name)); err != nil {
			return err
		}
	}
	if err := os.WriteFile(r.modelMarkerPath(), []byte(sceneRuntimeIdentity), 0o644); err != nil {
		return err
	}
	log.Printf("[Scene] 模型安装完成 files=%d mirror=%v", len(r.modelFiles), mirror != "")
	return nil
}

func (r *SceneRuntime) downloadModelFile(ctx context.Context, url, destination string, file sceneModelFile, progress func(int64)) (int64, error) {
	body, size, err := r.fetch(ctx, url)
	if err != nil {
		return 0, err
	}
	defer body.Close()
	if size > sceneModelFileMaxSize {
		return 0, fmt.Errorf("模型文件体积异常（%d 字节）", size)
	}
	out, err := os.Create(destination)
	if err != nil {
		return 0, err
	}
	digest := sha256.New()
	counter := &faceDownloadCounter{onProgress: progress}
	written, copyErr := io.Copy(io.MultiWriter(out, digest, counter), io.LimitReader(body, sceneModelFileMaxSize+1))
	closeErr := out.Close()
	if copyErr != nil {
		return 0, copyErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if written != file.Bytes {
		return 0, fmt.Errorf("大小与清单不符（%d 字节，期望 %d）", written, file.Bytes)
	}
	if actual := hex.EncodeToString(digest.Sum(nil)); !strings.EqualFold(actual, file.SHA256) {
		return 0, errors.New("sha256 与清单不符")
	}
	return written, nil
}

// fetchSceneModelFile 是模型下载的网络出口：只设响应头超时，整体中止靠 ctx。
func fetchSceneModelFile(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	response, err := faceModelHTTPClient.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("下载失败: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, 0, fmt.Errorf("上游返回 %d", response.StatusCode)
	}
	return response.Body, response.ContentLength, nil
}
