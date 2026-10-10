package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 场景检索运行时状态机与准备流程（仿人脸运行时）：模型逐个文件下载、校验 sha256、换入目录。

func newSceneRuntimeForTest(t *testing.T, mirror string) *SceneRuntime {
	t.Helper()
	runtime := NewSceneRuntime(t.TempDir(), func() string { return mirror })
	runtime.supported = func() bool { return true }
	// 可用的解释器 = 运行时目录下的这个文件存在（不把开发机上的系统 Python 算进来）。
	runtime.acceptPy = func(path string) bool {
		if !strings.HasPrefix(path, runtime.RuntimeDir()) {
			return false
		}
		info, err := os.Stat(path)
		return err == nil && !info.IsDir()
	}
	return runtime
}

// sceneTestModelFiles 是几个字节的假模型清单，内容与哈希对得上。
func sceneTestModelFiles() ([]sceneModelFile, map[string][]byte) {
	contents := map[string][]byte{
		"onnx/model_quantized.onnx": []byte("fake-onnx-model"),
		"tokenizer.json":            []byte(`{"fake":true}`),
		"preprocessor_config.json":  []byte(`{"size":{"width":224,"height":224}}`),
	}
	files := make([]sceneModelFile, 0, len(sceneModelFiles))
	for _, file := range sceneModelFiles {
		data := contents[file.Remote]
		files = append(files, sceneModelFile{Remote: file.Remote, Name: file.Name, SHA256: sha256Hex(data), Bytes: int64(len(data))})
	}
	return files, contents
}

// stubSceneVenv 让准备流程里的 python 调用都"成功"：venv 命令写出解释器，自检输出 ok。
func stubSceneVenv(t *testing.T, runtime *SceneRuntime, calls *[]string) {
	t.Helper()
	writeFaceTestFile(t, runtime.managedPython(), "python")
	var mu sync.Mutex
	runtime.runPython = func(_ context.Context, _ string, args []string, _ []string) ([]byte, error) {
		mu.Lock()
		*calls = append(*calls, strings.Join(args, " "))
		mu.Unlock()
		if len(args) >= 2 && args[0] == "-m" && args[1] == "venv" {
			writeFaceTestFile(t, runtime.venvPython(), "python")
		}
		return []byte("ok"), nil
	}
}

func prepareSceneRuntimeAndWait(t *testing.T, runtime *SceneRuntime) SceneRuntimeStatus {
	t.Helper()
	if _, err := runtime.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare 失败: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if status := runtime.Status(); !status.Preparing {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("准备超时")
	return SceneRuntimeStatus{}
}

func TestSceneRuntimeStatusStateMachine(t *testing.T) {
	runtime := newSceneRuntimeForTest(t, "")
	if status := runtime.Status(); status.State != SceneRuntimeStateMissingPython {
		t.Fatalf("没有解释器时应为 missing_python: %+v", status)
	}
	writeFaceTestFile(t, runtime.managedPython(), "python")
	runtime.invalidateInterpreterCache()
	if status := runtime.Status(); status.State != SceneRuntimeStateMissingVenv {
		t.Fatalf("依赖未安装时应为 missing_venv: %+v", status)
	}
	writeFaceTestFile(t, runtime.venvPython(), "python")
	writeFaceTestFile(t, runtime.venvMarkerPath(), sceneRuntimeIdentity)
	runtime.invalidateInterpreterCache()
	status := runtime.Status()
	if status.State != SceneRuntimeStateMissingModel || status.ModelHost != "https://huggingface.co" || status.ModelID != SceneLocalModelID {
		t.Fatalf("只差模型时应为 missing_model 且来源是官方主机: %+v", status)
	}
	if _, _, _, err := runtime.WorkerEnvironment(); !errors.Is(err, ErrSceneRuntimeUnavailable) {
		t.Fatalf("不可用时不得拉起 worker: %v", err)
	}
	cached, _, ok := runtime.CachedStatus()
	if !ok || cached.State != SceneRuntimeStateMissingModel {
		t.Fatalf("CachedStatus 应返回最近一次检查结果: %+v %v", cached, ok)
	}
	runtime.supported = func() bool { return false }
	if status := runtime.Status(); status.State != SceneRuntimeStateIncompatible {
		t.Fatalf("不支持的平台应为 incompatible: %+v", status)
	}
	if _, err := runtime.Prepare(context.Background()); !errors.Is(err, ErrSceneRuntimeUnsupported) {
		t.Fatalf("不支持的平台上准备应直接拒绝: %v", err)
	}
	empty := NewSceneRuntime("", nil)
	empty.supported = func() bool { return true }
	if status := empty.Status(); status.State != SceneRuntimeStateIncompatible || empty.TempDir() != "" {
		t.Fatalf("数据目录未解析时不得落到相对路径: %+v", status)
	}
}

func TestSceneModelURLUsesMirrorHostAndPinnedRevision(t *testing.T) {
	file := sceneModelFiles[0]
	official := sceneModelFileURL("", file)
	if official != "https://huggingface.co/Xenova/chinese-clip-vit-base-patch16/resolve/f26904860903e70e050b8f48255e5f48401816e9/onnx/model_quantized.onnx" {
		t.Fatalf("官方地址不对: %s", official)
	}
	if mirrored := sceneModelFileURL(" https://hf-mirror.com/ ", file); !strings.HasPrefix(mirrored, "https://hf-mirror.com/Xenova/") {
		t.Fatalf("镜像应替换主机: %s", mirrored)
	}
	if sceneModelTotalBytes(sceneModelFiles) != 190842404+439124+546 {
		t.Fatal("清单字节数与合同不符")
	}
}

func TestSceneRuntimePrepareDownloadsVerifiesAndBecomesAvailable(t *testing.T) {
	runtime := newSceneRuntimeForTest(t, "https://hf-mirror.com")
	files, contents := sceneTestModelFiles()
	runtime.modelFiles = files
	var calls []string
	stubSceneVenv(t, runtime, &calls)
	var fetched []string
	runtime.fetch = func(_ context.Context, url string) (io.ReadCloser, int64, error) {
		fetched = append(fetched, url)
		for remote, data := range contents {
			if strings.HasSuffix(url, "/"+remote) {
				return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
			}
		}
		return nil, 0, errors.New("unexpected url")
	}
	status := prepareSceneRuntimeAndWait(t, runtime)
	if status.State != SceneRuntimeStateAvailable || status.Error != "" {
		t.Fatalf("准备完成后应可用: %+v", status)
	}
	if len(fetched) != 3 || !strings.HasPrefix(fetched[0], "https://hf-mirror.com/") {
		t.Fatalf("应从镜像主机逐个下载 3 个文件: %v", fetched)
	}
	joined := strings.Join(calls, "\n")
	for _, pin := range sceneRuntimePackages {
		if !strings.Contains(joined, pin) {
			t.Fatalf("依赖应逐个 pin 安装，缺 %s: %s", pin, joined)
		}
	}
	for _, file := range files {
		if data, err := os.ReadFile(filepath.Join(runtime.modelDir(), file.Name)); err != nil || !bytes.Equal(data, contents[file.Remote]) {
			t.Fatalf("模型文件 %s 未安装: %v", file.Name, err)
		}
	}
	if entries, _ := filepath.Glob(filepath.Join(runtime.RuntimeDir(), sceneModelsDirName, ".staging-*")); len(entries) != 0 {
		t.Fatalf("暂存目录应被删除: %v", entries)
	}
	python, script, env, err := runtime.WorkerEnvironment()
	if err != nil || python != runtime.venvPython() || !strings.HasSuffix(script, sceneWorkerFileName) || !faceTestEnvContains(env, "CINEINSIGHT_SCENE_MODEL_DIR="+runtime.modelDir()) {
		t.Fatalf("可用后应给出 worker 环境: %s %s %v", python, script, err)
	}
	if data, _ := os.ReadFile(script); string(data) != sceneWorkerScript {
		t.Fatal("worker 脚本应原样写入运行时目录")
	}
}

func TestSceneRuntimePrepareHashMismatchIsDownloadFailed(t *testing.T) {
	runtime := newSceneRuntimeForTest(t, "")
	files, contents := sceneTestModelFiles()
	runtime.modelFiles = files
	var calls []string
	stubSceneVenv(t, runtime, &calls)
	runtime.fetch = func(_ context.Context, url string) (io.ReadCloser, int64, error) {
		for remote, data := range contents {
			if strings.HasSuffix(url, "/"+remote) {
				tampered := append([]byte{}, data...)
				tampered[0] ^= 0xff
				return io.NopCloser(bytes.NewReader(tampered)), int64(len(tampered)), nil
			}
		}
		return nil, 0, errors.New("unexpected url")
	}
	status := prepareSceneRuntimeAndWait(t, runtime)
	if status.State != SceneRuntimeStateDownloadFailed || !strings.Contains(status.Error, "sha256") {
		t.Fatalf("哈希不符应为 download_failed: %+v", status)
	}
	if _, err := os.Stat(runtime.modelMarkerPath()); err == nil {
		t.Fatal("校验失败不得写模型标记")
	}
	if err := runtime.CancelPrepare(); err == nil {
		t.Fatal("没有准备在跑时取消应报错")
	}
}
