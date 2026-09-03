package services

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 模型不随应用打包（用户裁决：包体只带 sidecar 二进制，模型按需下载）。
// 来源固定为上游 release 包；下载后逐个文件对照清单里的 SHA-256，
// 对不上就整批丢弃——宁可不可用，也不拿一份来路不明的权重去跑。
const (
	enhancementModelSourceURL   = "https://github.com/xinntao/Real-ESRGAN/releases/download/v0.2.5.0/realesrgan-ncnn-vulkan-20220424-macos.zip"
	enhancementModelZipMaxSize  = int64(256) << 20
	enhancementModelFileMaxSize = int64(128) << 20
)

// EnhancementModelStatus 是模型下载的对外状态。
type EnhancementModelStatus struct {
	Running         bool   `json:"running"`
	Completed       bool   `json:"completed"`
	Cancelled       bool   `json:"cancelled"`
	DownloadedBytes int64  `json:"downloaded_bytes"`
	TotalBytes      int64  `json:"total_bytes"`
	Stage           string `json:"stage"`
	Message         string `json:"message"`
	Error           string `json:"error"`
	InstallDir      string `json:"install_dir"`
}

// EnhancementModelInstaller 负责把超分模型下载并安装到用户数据目录。
type EnhancementModelInstaller struct {
	mu         sync.Mutex
	status     EnhancementModelStatus
	cancel     context.CancelFunc
	installDir string
	emitter    func(EnhancementModelStatus)
	// 测试接缝：让用例不必真的走网络。
	fetch func(ctx context.Context, url string) (io.ReadCloser, int64, error)
	// 安装完成后回调（用来刷新能力探测）。
	onInstalled func()
}

func NewEnhancementModelInstaller(installDir string) *EnhancementModelInstaller {
	return &EnhancementModelInstaller{
		installDir: installDir,
		status:     EnhancementModelStatus{InstallDir: installDir},
		fetch:      fetchEnhancementModelArchive,
	}
}

func (s *EnhancementModelInstaller) SetEventEmitter(emitter func(EnhancementModelStatus)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

func (s *EnhancementModelInstaller) SetOnInstalled(hook func()) {
	s.mu.Lock()
	s.onInstalled = hook
	s.mu.Unlock()
}

func (s *EnhancementModelInstaller) Status() EnhancementModelStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Start 发起一次下载安装。已在进行中时返回当前状态，不重复下载。
func (s *EnhancementModelInstaller) Start(parent context.Context, required []enhancementManifestFile) (EnhancementModelStatus, error) {
	if strings.TrimSpace(s.installDir) == "" {
		return EnhancementModelStatus{}, fmt.Errorf("模型安装目录未配置")
	}
	if len(required) == 0 {
		return EnhancementModelStatus{}, fmt.Errorf("超分清单没有声明模型文件，无法校验下载内容")
	}
	s.mu.Lock()
	if s.status.Running {
		status := s.status
		s.mu.Unlock()
		return status, nil
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.status = EnhancementModelStatus{Running: true, Stage: "download", Message: "正在下载超分模型…", InstallDir: s.installDir}
	status := s.status
	s.mu.Unlock()
	s.emit(status)

	go func() {
		defer cancel()
		err := s.run(ctx, required)
		s.mu.Lock()
		s.status.Running = false
		switch {
		case err == nil:
			s.status.Completed = true
			s.status.Stage = "done"
			s.status.Message = "超分模型已就绪"
			s.status.Error = ""
		case ctx.Err() != nil:
			s.status.Cancelled = true
			s.status.Stage = "cancelled"
			s.status.Message = "已取消下载"
		default:
			s.status.Stage = "failed"
			s.status.Error = err.Error()
			s.status.Message = "下载失败"
		}
		hook := s.onInstalled
		final := s.status
		s.mu.Unlock()
		if final.Completed && hook != nil {
			hook()
		}
		s.emit(final)
	}()
	return status, nil
}

func (s *EnhancementModelInstaller) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *EnhancementModelInstaller) emit(status EnhancementModelStatus) {
	s.mu.Lock()
	emitter := s.emitter
	s.mu.Unlock()
	if emitter != nil {
		emitter(status)
	}
}

func (s *EnhancementModelInstaller) update(mutate func(*EnhancementModelStatus)) {
	s.mu.Lock()
	mutate(&s.status)
	status := s.status
	s.mu.Unlock()
	s.emit(status)
}

func (s *EnhancementModelInstaller) run(ctx context.Context, required []enhancementManifestFile) error {
	wanted := make(map[string]string, len(required))
	for _, item := range required {
		if strings.Contains(item.Path, "..") || filepath.IsAbs(item.Path) || item.SHA256 == "" {
			return fmt.Errorf("清单里的模型条目不合法: %s", item.Path)
		}
		wanted[item.Path] = item.SHA256
	}

	body, total, err := s.fetch(ctx, enhancementModelSourceURL)
	if err != nil {
		return err
	}
	defer body.Close()
	if total > enhancementModelZipMaxSize {
		return fmt.Errorf("上游包体积异常（%d 字节），拒绝下载", total)
	}
	s.update(func(status *EnhancementModelStatus) { status.TotalBytes = total })

	tempDir, err := os.MkdirTemp("", "cineinsight-enhance-models-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	archivePath := filepath.Join(tempDir, "models.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	counter := &enhancementDownloadCounter{onProgress: func(n int64) {
		s.update(func(status *EnhancementModelStatus) { status.DownloadedBytes = n })
	}}
	if _, err := io.Copy(archive, io.TeeReader(io.LimitReader(body, enhancementModelZipMaxSize+1), counter)); err != nil {
		archive.Close()
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.update(func(status *EnhancementModelStatus) {
		status.Stage = "verify"
		status.Message = "正在校验模型文件…"
	})

	staged := filepath.Join(tempDir, "staged")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		return err
	}
	if err := extractEnhancementModels(archivePath, staged, wanted); err != nil {
		return err
	}

	s.update(func(status *EnhancementModelStatus) {
		status.Stage = "install"
		status.Message = "正在安装…"
	})
	if err := os.MkdirAll(s.installDir, 0o755); err != nil {
		return err
	}
	for name := range wanted {
		src := filepath.Join(staged, name)
		dst := filepath.Join(s.installDir, name)
		if err := replaceEnhancementModelFile(src, dst); err != nil {
			return err
		}
	}
	log.Printf("[Enhancement] 模型安装完成 dir=%s files=%d", s.installDir, len(wanted))
	return nil
}

// extractEnhancementModels 只取清单声明的那几个文件，并逐个比对 SHA-256。
// 用 path.Base 抹掉压缩包里的目录结构，避免 zip slip。
func extractEnhancementModels(archivePath, destDir string, wanted map[string]string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("上游包无法解压: %w", err)
	}
	defer reader.Close()

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
		if entry.UncompressedSize64 > uint64(enhancementModelFileMaxSize) {
			return fmt.Errorf("模型文件体积异常: %s", name)
		}
		source, err := entry.Open()
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, name)
		file, err := os.Create(target)
		if err != nil {
			source.Close()
			return err
		}
		digest := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(file, digest), io.LimitReader(source, enhancementModelFileMaxSize))
		source.Close()
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if actual := hex.EncodeToString(digest.Sum(nil)); !strings.EqualFold(actual, expected) {
			return fmt.Errorf("模型文件校验失败: %s", name)
		}
		found[name] = struct{}{}
	}
	if len(found) != len(wanted) {
		missing := make([]string, 0)
		for name := range wanted {
			if _, ok := found[name]; !ok {
				missing = append(missing, name)
			}
		}
		return fmt.Errorf("上游包里缺少所需模型: %s", strings.Join(missing, ", "))
	}
	return nil
}

func replaceEnhancementModelFile(src, dst string) error {
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// 临时目录与安装目录可能不同卷，Rename 会失败，退回复制。
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		return err
	}
	if err := target.Sync(); err != nil {
		target.Close()
		return err
	}
	return target.Close()
}

type enhancementDownloadCounter struct {
	total      int64
	lastEmit   time.Time
	onProgress func(int64)
}

func (c *enhancementDownloadCounter) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	// 限流：下载几十兆会有成千上万次 Write，每次都发事件会把前端淹掉。
	if c.onProgress != nil && time.Since(c.lastEmit) > 200*time.Millisecond {
		c.lastEmit = time.Now()
		c.onProgress(c.total)
	}
	return len(p), nil
}

func fetchEnhancementModelArchive(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("下载超分模型失败: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, 0, fmt.Errorf("下载超分模型失败：上游返回 %d", response.StatusCode)
	}
	return response.Body, response.ContentLength, nil
}
