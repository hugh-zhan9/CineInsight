package services

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	managedPythonMacARM64    = "https://github.com/astral-sh/python-build-standalone/releases/download/20260303/cpython-3.10.20%2B20260303-aarch64-apple-darwin-install_only_stripped.tar.gz"
	managedPythonMacAMD64    = "https://github.com/astral-sh/python-build-standalone/releases/download/20260303/cpython-3.10.20%2B20260303-x86_64-apple-darwin-install_only_stripped.tar.gz"
	subtitlePrepareWaitDelay = 5 * time.Second
)

// Shared Python preparation remains owned by the face and scene runtimes.
func managedPythonPathIn(runtimeDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(runtimeDir, "python", "python.exe")
	}
	return filepath.Join(runtimeDir, "python", "bin", "python3")
}

func findPythonInterpreter(managedPython string, accept func(string) bool) string {
	if path := managedPython; accept(path) {
		return path
	}

	candidates := []string{}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/opt/homebrew/bin/python3",
			"/usr/local/bin/python3",
			"/Library/Frameworks/Python.framework/Versions/3.12/bin/python3",
			"/Library/Frameworks/Python.framework/Versions/3.11/bin/python3",
			"/Library/Frameworks/Python.framework/Versions/3.10/bin/python3",
			"/usr/bin/python3",
		)
	} else if runtime.GOOS == "windows" {
		candidates = append(candidates, "python.exe", "python3.exe")
	} else {
		candidates = append(candidates, "/usr/bin/python3", "/usr/local/bin/python3")
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if strings.Contains(candidate, string(os.PathSeparator)) {
			if accept(candidate) {
				return candidate
			}
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil && accept(path) {
			return path
		}
	}

	if path, err := exec.LookPath("python3"); err == nil && accept(path) {
		return path
	}
	if path, err := exec.LookPath("python"); err == nil && accept(path) {
		return path
	}
	return ""
}

func pythonVersionInRange(path string, minMinor, maxMinor int) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}

	cmd := exec.Command(path, "-c", `import sys; print(f"{sys.version_info[0]}.{sys.version_info[1]}")`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}

	version := strings.TrimSpace(string(output))
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	if maxMinor > 0 {
		return major == 3 && minor >= minMinor && minor <= maxMinor
	}
	return major > 3 || (major == 3 && minor >= minMinor)
}

func preparationCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	applyEnhancementProcessGroup(cmd)
	cmd.Cancel = func() error {
		killEnhancementProcessGroup(cmd)
		return nil
	}
	cmd.WaitDelay = subtitlePrepareWaitDelay
	return cmd
}

// venvUsable 报告已有的虚拟环境能否直接复用：解释器版本够，且 `python -m pip --version` 能跑。
// 建 venv 时被取消（或被外部弄坏）会留下一个有解释器、没有 pip 的目录，只看解释器就会一直复用它，
// 之后每次 pip install 都失败（M-5）。ctx 取消时返回 ctx 的错误，由调用方直接收手，不把
// 一个好好的 venv 当成损坏删掉。

func venvUsable(ctx context.Context, venvPython string, meetsMinimumVersion func(string) bool) (bool, error) {
	if !meetsMinimumVersion(venvPython) {
		return false, ctx.Err()
	}
	if err := preparationCommand(ctx, venvPython, "-m", "pip", "--version").Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		log.Printf("[PythonRuntime] existing venv has no usable pip, rebuilding err=%v", err)
		return false, nil
	}
	return true, nil
}

// ensureManagedPythonRuntime 把 python-build-standalone 解到 runtimeDir/python 并返回
// 解释器路径；已经装好（accept 接受）时直接返回，不重复下载。
//
// 人脸与场景运行时复用此下载入口，字幕不再使用 Python。
func ensureManagedPythonRuntime(ctx context.Context, runtimeDir, label, requirement string, accept func(string) bool) (string, error) {
	pythonPath := managedPythonPathIn(runtimeDir)
	if accept(pythonPath) {
		return pythonPath, nil
	}

	var url string
	switch {
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		url = managedPythonMacARM64
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		url = managedPythonMacAMD64
	default:
		return "", fmt.Errorf("当前平台缺少 %s，且暂未实现自动下载: %s/%s", requirement, runtime.GOOS, runtime.GOARCH)
	}

	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		return "", err
	}

	archivePath := filepath.Join(runtimeDir, "python-runtime.tar.gz")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("下载 %s Python runtime 失败: %w", label, err)
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("下载 %s Python runtime 失败: %w", label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("下载 %s Python runtime 失败: HTTP %d", label, resp.StatusCode)
	}

	out, err := os.Create(archivePath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return "", err
	}
	out.Close()

	extractCmd := exec.CommandContext(ctx, "tar", "-xzf", archivePath, "-C", runtimeDir)
	output, err := extractCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("解压 %s Python runtime 失败: %s", label, strings.TrimSpace(string(output)))
	}

	if !accept(pythonPath) {
		return "", fmt.Errorf("%s Python runtime 解压完成，但未找到可用的 %s", label, requirement)
	}
	return pythonPath, nil
}
