package services

import (
	"context"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

var sceneModelHTTPClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: transport}
}()

func runScenePythonCommand(ctx context.Context, python string, args []string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, python, args...)
	cmd.Env = env
	return cmd.CombinedOutput()
}

func sceneRuntimePlatformSupported() bool {
	return runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
}

type sceneInterpreterProbe struct {
	usable   bool
	probedAt time.Time
}

// sceneInterpreterCacheTTL 是解释器探测结论的有效期。
const sceneInterpreterCacheTTL = 30 * time.Second

type sceneDownloadCounter struct {
	total      int64
	lastEmit   time.Time
	onProgress func(int64)
}

func (c *sceneDownloadCounter) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	// 限流：275 MB 会有成千上万次 Write，每次都发事件会把前端淹掉。
	if c.onProgress != nil && time.Since(c.lastEmit) > 200*time.Millisecond {
		c.lastEmit = time.Now()
		c.onProgress(c.total)
	}
	return len(p), nil
}
