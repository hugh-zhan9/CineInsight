package main

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"video-master/internal/appdata"
	"video-master/services"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) processingService() (*services.MediaProcessingService, error) {
	a.processingMu.Lock()
	defer a.processingMu.Unlock()
	if a.processingPaused {
		return nil, errors.New("processing_stopped: 图片与样片处理已暂停")
	}
	if a.mediaProcessing == nil {
		dir, err := appdata.Resolve()
		if err != nil {
			return nil, err
		}
		a.mediaProcessing = services.NewMediaProcessingService(a.ctx, dir, a.mediaWorkSlot, a.enhancement)
		a.mediaProcessing.SetBackgroundTaskRegistry(a.backgroundTasks)
	}
	return a.mediaProcessing, nil
}
func (a *App) currentMediaProcessing() *services.MediaProcessingService {
	a.processingMu.Lock()
	defer a.processingMu.Unlock()
	return a.mediaProcessing
}
func (a *App) stopMediaProcessing(ctx context.Context) error {
	a.processingMu.Lock()
	a.processingPaused = true
	service := a.mediaProcessing
	a.processingMu.Unlock()
	if service == nil {
		return nil
	}
	if err := service.StopAndWait(ctx); err != nil {
		return errors.New("临时图片或样片尚未停止，请重试；原文件保持不变")
	}
	return nil
}
func (a *App) resumeMediaProcessing() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.stopMediaProcessing(ctx); err != nil {
		return
	}
	a.processingMu.Lock()
	a.mediaProcessing = nil
	a.processingPaused = false
	a.processingMu.Unlock()
}

// StartMediaProcessing 开始受控的图片处理或短样片试处理。
func (a *App) StartMediaProcessing(request services.MediaProcessingRequest) (*services.MediaProcessingView, error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, err
	}
	defer done()
	service, err := a.processingService()
	if err != nil {
		return nil, err
	}
	return service.Begin(ctx, request)
}

// GetMediaProcessing 只读取当前库的会话，不重新启动已取消任务。
func (a *App) GetMediaProcessing(id string) (*services.MediaProcessingView, error) {
	_, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, err
	}
	defer done()
	service := a.currentMediaProcessing()
	if service == nil {
		return nil, errors.New("processing_expired: 处理会话已关闭")
	}
	return service.Get(id)
}

// ReleaseMediaProcessing 取消并等待停止，然后回收本请求的临时文件。
func (a *App) ReleaseMediaProcessing(id string) error {
	service := a.currentMediaProcessing()
	if service == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return services.WithoutAbsolutePaths(service.Release(ctx, id))
}

// SetMediaProcessingDirty 只让更新的编辑修订改变退出提示，旧回包不能清掉新草稿。
func (a *App) SetMediaProcessingDirty(id string, revision uint64, dirty bool) error {
	service := a.currentMediaProcessing()
	if service == nil {
		return errors.New("processing_expired")
	}
	return service.SetDirty(id, revision, dirty)
}

var selectProcessingPath = func(ctx context.Context, directory, name, format string) (string, error) {
	return wailsruntime.SaveFileDialog(ctx, wailsruntime.SaveDialogOptions{Title: "保存处理后的副本", DefaultDirectory: directory, DefaultFilename: name, Filters: []wailsruntime.FileFilter{{DisplayName: strings.ToUpper(format) + " 文件", Pattern: "*." + format}}})
}

// SaveMediaProcessing 原生选择路径后再核对来源，只能另存，不覆盖现有目标。
func (a *App) SaveMediaProcessing(id string) (string, error) {
	if a.ctx == nil {
		return "", errors.New("应用窗口尚未就绪")
	}
	service := a.currentMediaProcessing()
	if service == nil {
		return "", errors.New("processing_expired")
	}
	view, err := service.Get(id)
	if err != nil {
		return "", err
	}
	if view.State != "ready" || view.ResultURL == "" {
		return "", errors.New("没有可保存的处理结果")
	}
	name := strings.TrimSuffix(view.Name, filepath.Ext(view.Name)) + ".processed." + view.Format
	directory, err := service.ExportDirectory(id)
	if err != nil {
		return "", err
	}
	target, err := selectProcessingPath(a.ctx, directory, name, view.Format)
	if err != nil || target == "" {
		return "", err
	}
	if err = service.Save(a.ctx, id, target); err != nil {
		return "", services.WithoutAbsolutePaths(err)
	}
	return target, nil
}

func (a *App) serveProcessingAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/preview/processing/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	service := a.currentMediaProcessing()
	if service == nil {
		http.NotFound(w, r)
		return
	}
	file, release, err := service.OpenAsset(r.Context(), parts[0], parts[1])
	if err != nil {
		http.Error(w, "处理资源已失效，请重新打开", http.StatusGone)
		return
	}
	defer release()
	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filepath.Base(file.Name()), info.ModTime(), file)
}
