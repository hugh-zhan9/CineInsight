package services

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 运行时状态机（D-016）：缺 Python、缺依赖、缺模型、下载失败、平台不支持
// 各有各的说法，界面照着说就行，不用猜。

func newFaceRuntimeForTest(t *testing.T) *FaceRuntime {
	t.Helper()
	runtime := NewFaceRuntime(t.TempDir(), func() string { return "" })
	runtime.supported = func() bool { return true }
	// 测试里"可用的解释器"= 运行时目录下的这个文件存在。真判据要起子进程问版本，
	// 在单测里既慢又看机器上装了什么；限定在运行时目录内，是为了不让
	// findPythonInterpreter 的 PATH 兜底把开发机上的系统 Python 算进来。
	runtime.acceptPy = func(path string) bool {
		if !strings.HasPrefix(path, runtime.RuntimeDir()) {
			return false
		}
		info, err := os.Stat(path)
		return err == nil && !info.IsDir()
	}
	return runtime
}

func TestFaceRuntimeStatusIncompatibleOffApplePlatforms(t *testing.T) {
	runtime := newFaceRuntimeForTest(t)
	runtime.supported = func() bool { return false }

	status := runtime.Status()
	if status.State != FaceRuntimeStateIncompatible {
		t.Fatalf("非 macOS Apple Silicon 应为 incompatible: %+v", status)
	}
	if !strings.Contains(status.Reason, "Apple Silicon") {
		t.Fatalf("原因里应说清平台限制: %q", status.Reason)
	}
	if _, err := runtime.Prepare(context.Background()); !errors.Is(err, ErrFaceRuntimeUnsupported) {
		t.Fatalf("不支持的平台上准备运行时应直接拒绝: %v", err)
	}
}

func TestFaceRuntimeStatusMissingPythonThenVenvThenModel(t *testing.T) {
	runtime := newFaceRuntimeForTest(t)

	// 一个可用解释器都没有。
	if status := runtime.Status(); status.State != FaceRuntimeStateMissingPython {
		t.Fatalf("没有解释器时应为 missing_python: %+v", status)
	}

	// 托管解释器就位，但依赖还没装。
	// 测试是在运行时背后直接动文件系统的，因此要显式失效解释器探测缓存——
	// 真实路径上这件事由准备流程自己做（或等 30 秒 TTL 过期）。
	writeFaceTestFile(t, runtime.managedPython(), "python")
	runtime.invalidateInterpreterCache()
	if status := runtime.Status(); status.State != FaceRuntimeStateMissingVenv {
		t.Fatalf("依赖未安装时应为 missing_venv: %+v", status)
	}

	// venv 与自检标记就位，只差模型。
	writeFaceTestFile(t, runtime.venvPython(), "python")
	writeFaceTestFile(t, runtime.venvMarkerPath(), faceRuntimeIdentity)
	runtime.invalidateInterpreterCache()
	status := runtime.Status()
	if status.State != FaceRuntimeStateMissingModel {
		t.Fatalf("模型未下载时应为 missing_model: %+v", status)
	}
	if status.ModelSourceURL != faceModelArchiveURL {
		t.Fatalf("未配镜像时来源应是官方地址: %q", status.ModelSourceURL)
	}
	if status.ModelSHA256 == "" {
		t.Fatal("状态里必须带模型包的 sha256，用户才能自己核对")
	}
}

func TestFaceRuntimeStatusUsesMirrorPrefix(t *testing.T) {
	runtime := newFaceRuntimeForTest(t)
	runtime.mirror = func() string { return "https://mirror.example.com/" }

	status := runtime.Status()
	if status.ModelSourceURL != "https://mirror.example.com/"+faceModelArchiveURL {
		t.Fatalf("镜像前缀应拼在官方地址前面: %q", status.ModelSourceURL)
	}
	if !status.MirrorConfigured {
		t.Fatal("配了镜像时状态应标出来")
	}
}

// 模型包下载失败（哈希不符）要停在 download_failed 并带上来源地址：
// 用户要能看出"是这个地址给了我一份不对的东西"，而不是一句"不可用"。
func TestFaceRuntimePrepareModelDownloadHashMismatchIsDownloadFailed(t *testing.T) {
	runtime := newFaceRuntimeForTest(t)
	prepareFaceRuntimeVenvStubs(t, runtime)
	runtime.modelFiles = []faceModelFile{{Name: "det.onnx", SHA256: strings.Repeat("a", 64), Bytes: 3}}
	runtime.archiveSHA256 = strings.Repeat("b", 64)
	runtime.fetch = func(ctx context.Context, url string) (io.ReadCloser, int64, error) {
		return io.NopCloser(strings.NewReader("not-the-real-archive")), int64(20), nil
	}

	status := prepareFaceRuntimeAndWait(t, runtime)
	if status.State != FaceRuntimeStateDownloadFailed {
		t.Fatalf("哈希不符应落在 download_failed: %+v", status)
	}
	if !strings.Contains(status.Error, "校验失败") {
		t.Fatalf("失败原因应说明是校验失败: %q", status.Error)
	}
	if !strings.Contains(status.Reason, faceModelArchiveURL) {
		t.Fatalf("失败原因里应带来源地址: %q", status.Reason)
	}
	// 半成品不能留下：下一次准备要从干净状态开始。
	if _, err := os.Stat(filepath.Join(runtime.modelPackDir(), "det.onnx")); !os.IsNotExist(err) {
		t.Fatalf("校验失败不应留下模型文件: %v", err)
	}
}

func TestFaceRuntimePrepareModelDownloadTransportErrorIsDownloadFailed(t *testing.T) {
	runtime := newFaceRuntimeForTest(t)
	prepareFaceRuntimeVenvStubs(t, runtime)
	runtime.fetch = func(ctx context.Context, url string) (io.ReadCloser, int64, error) {
		return nil, 0, errors.New("下载人脸模型失败：上游返回 404")
	}

	status := prepareFaceRuntimeAndWait(t, runtime)
	if status.State != FaceRuntimeStateDownloadFailed {
		t.Fatalf("下载不通应落在 download_failed: %+v", status)
	}
	if !strings.Contains(status.Error, "404") {
		t.Fatalf("失败原因应保留上游状态: %q", status.Error)
	}
}

// 走完一遍完整的准备：依赖装好、模型下载校验安装，状态变 available，
// 之后 WorkerEnvironment 才肯给出解释器与脚本。
func TestFaceRuntimePrepareInstallsModelsAndBecomesAvailable(t *testing.T) {
	runtime := newFaceRuntimeForTest(t)
	prepareFaceRuntimeVenvStubs(t, runtime)
	detContent := []byte("det-model-bytes")
	recContent := []byte("rec-model-bytes")
	runtime.modelFiles = []faceModelFile{
		{Name: "det.onnx", SHA256: sha256Hex(detContent), Bytes: int64(len(detContent))},
		{Name: "rec.onnx", SHA256: sha256Hex(recContent), Bytes: int64(len(recContent))},
	}
	archive := faceTestArchive(t, map[string][]byte{
		"det.onnx": detContent,
		"rec.onnx": recContent,
		// 包里多出来的文件不该被解出来：清单说要什么就只要什么。
		"unused.onnx": []byte("junk"),
	})
	runtime.archiveSHA256 = sha256Hex(archive)
	runtime.fetch = func(ctx context.Context, url string) (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(archive)), int64(len(archive)), nil
	}

	status := prepareFaceRuntimeAndWait(t, runtime)
	if status.State != FaceRuntimeStateAvailable {
		t.Fatalf("准备完成后应为 available: %+v", status)
	}
	if _, err := os.Stat(filepath.Join(runtime.modelPackDir(), "unused.onnx")); !os.IsNotExist(err) {
		t.Fatalf("清单外的文件不应被安装: %v", err)
	}
	python, script, env, err := runtime.WorkerEnvironment()
	if err != nil {
		t.Fatalf("运行时可用后应能给出 worker 环境: %v", err)
	}
	if python != runtime.venvPython() || script != runtime.workerPath() {
		t.Fatalf("worker 环境指向不对: python=%q script=%q", python, script)
	}
	if data, err := os.ReadFile(script); err != nil || string(data) != faceWorkerScript {
		t.Fatalf("worker 脚本应写成 embed 的内容: err=%v", err)
	}
	if !faceTestEnvContains(env, "CINEINSIGHT_FACE_MODEL_ROOT="+runtime.RuntimeDir()) {
		t.Fatalf("worker 环境应把模型根目录传给 sidecar: %v", env)
	}
}

// 运行时不可用时绝不"边跑边装"：分析只能拿到一个说明原因的错误。
func TestFaceRuntimeWorkerEnvironmentRefusesWhenUnavailable(t *testing.T) {
	runtime := newFaceRuntimeForTest(t)
	if _, _, _, err := runtime.WorkerEnvironment(); !errors.Is(err, ErrFaceRuntimeUnavailable) {
		t.Fatalf("未就绪时应拒绝给出 worker 环境: %v", err)
	}
}

func TestFaceRuntimeCancelPrepareWithoutRunningReportsError(t *testing.T) {
	runtime := newFaceRuntimeForTest(t)
	if err := runtime.CancelPrepare(); err == nil {
		t.Fatal("没有在准备时取消应报错")
	}
}

func TestFaceModelDownloadURLKeepsOfficialSuffix(t *testing.T) {
	if url := faceModelDownloadURL(""); url != faceModelArchiveURL {
		t.Fatalf("无镜像时应用官方地址: %q", url)
	}
	if url := faceModelDownloadURL("  https://m.example.com//  "); url != "https://m.example.com/"+faceModelArchiveURL {
		t.Fatalf("镜像前缀应去空白并只留一个斜杠: %q", url)
	}
}

// prepareFaceRuntimeVenvStubs 把"装依赖"这一段换成文件操作：
// 真去建 venv、跑 pip 要几十秒并且要联网。
func prepareFaceRuntimeVenvStubs(t *testing.T, runtime *FaceRuntime) {
	t.Helper()
	writeFaceTestFile(t, runtime.managedPython(), "python")
	runtime.runPython = func(ctx context.Context, python string, args []string, env []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "-m venv"):
			writeFaceTestFile(t, runtime.venvPython(), "python")
			return []byte(""), nil
		case strings.Contains(joined, "import onnxruntime"):
			return []byte("ok\n"), nil
		default:
			return []byte(""), nil
		}
	}
}

func prepareFaceRuntimeAndWait(t *testing.T, runtime *FaceRuntime) FaceRuntimeStatus {
	t.Helper()
	done := make(chan FaceRuntimeStatus, 8)
	runtime.SetEventEmitter(func(status FaceRuntimeStatus) {
		if !status.Preparing {
			select {
			case done <- status:
			default:
			}
		}
	})
	if _, err := runtime.Prepare(context.Background()); err != nil {
		t.Fatalf("准备运行时失败: %v", err)
	}
	select {
	case status := <-done:
		return status
	case <-time.After(30 * time.Second):
		t.Fatal("准备运行时没有在 30 秒内结束")
	}
	return FaceRuntimeStatus{}
}

func writeFaceTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func faceTestArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	// 固定顺序，包体字节稳定，哈希才可复现。
	names := []string{"det.onnx", "rec.onnx", "unused.onnx"}
	for _, name := range names {
		content, ok := files[name]
		if !ok {
			continue
		}
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("造测试包失败: %v", err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatalf("写测试包失败: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭测试包失败: %v", err)
	}
	return buffer.Bytes()
}

func faceTestEnvContains(env []string, entry string) bool {
	for _, item := range env {
		if item == entry {
			return true
		}
	}
	return false
}

// 解释器探测结果按路径缓存（Minor 10）：Status() 被反复调用时不该每次都起
// 一个 python 子进程去问版本；显式失效之后重新探测。
func TestFaceRuntimeCachesInterpreterProbe(t *testing.T) {
	runtime := newFaceRuntimeForTest(t)
	writeFaceTestFile(t, runtime.managedPython(), "python")
	probes := 0
	underlying := runtime.acceptPy
	runtime.acceptPy = func(path string) bool {
		probes++
		return underlying(path)
	}

	for i := 0; i < 5; i++ {
		runtime.Status()
	}
	if probes == 0 {
		t.Fatal("至少要探测一次")
	}
	first := probes
	if first > 2 {
		t.Fatalf("五次 Status() 不该反复探测（每次最多 venv + 托管两条路径），实际探测 %d 次", first)
	}

	runtime.invalidateInterpreterCache()
	runtime.Status()
	if probes <= first {
		t.Fatalf("显式失效后应重新探测: before=%d after=%d", first, probes)
	}
}
