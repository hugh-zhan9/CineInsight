package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strconv"
	"time"
	"video-master/database"
	"video-master/services"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// HealthMetric is a measured value, not a substitute for an unavailable check.
type HealthMetric struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value int64  `json:"value"`
	Unit  string `json:"unit"`
}

// HealthItem is a read-side view; LocalDetail is never part of an exported report.
type HealthItem struct {
	Key         string         `json:"key"`
	Label       string         `json:"label"`
	State       string         `json:"state"`
	ReasonCode  string         `json:"reason_code"`
	Detail      string         `json:"detail"`
	ObservedAt  *time.Time     `json:"observed_at,omitempty" ts_type:"string"`
	LocalDetail string         `json:"local_detail,omitempty"`
	ActionID    string         `json:"action_id,omitempty"`
	Metrics     []HealthMetric `json:"metrics"`
}

// HealthSection lets the UI display independent checks as they finish.
type HealthSection struct {
	Key       string       `json:"key"`
	CheckedAt time.Time    `json:"checked_at" ts_type:"string"`
	Items     []HealthItem `json:"items"`
}

// HealthReportExport reports cancellation separately from a successful save.
type HealthReportExport struct {
	Saved   bool   `json:"saved"`
	Message string `json:"message"`
}

var healthSectionKeys = []string{"database", "storage", "runtimes", "indexes", "tasks", "caches"}

var healthReasonText = map[string]string{
	"ready": "可用", "not_initialized": "尚未初始化", "check_failed": "检查失败，请到对应设置查看",
	"database_unavailable": "数据库正在维护、等待重启或尚未连接", "not_checked": "尚未检查",
	"disabled": "未启用", "unavailable": "当前不可用", "watching": "实时同步中",
	"no_roots": "尚未配置扫描目录", "root_unavailable": "实时同步暂不可用，请检查磁盘连接、权限与监听支持",
	"missing_ffmpeg": "未找到 FFmpeg", "missing_ffprobe": "未找到 ffprobe",
	"missing_runtime": "需要准备运行时", "missing_model": "需要准备模型",
	"unsupported_platform": "当前平台不支持此能力", "manual_prereq_required": "需要手动准备依赖",
	"preparing": "正在准备运行时", "models_missing": "超分模型尚未准备",
	"models_corrupt": "超分模型校验失败", "needs_rebuild": "索引需要重新构建",
	"syncing": "正在同步索引", "waiting_idle": "等待空闲条件", "running": "有任务正在运行",
	"last_run_failed": "上一轮有失败项，可在任务中心查看", "snapshot": "已有记录的快照",
}

func healthItem(key, label, state, code, action string) HealthItem {
	detail, known := healthReasonText[code]
	if !known {
		code, detail = "check_failed", healthReasonText["check_failed"]
	}
	return HealthItem{Key: key, Label: label, State: state, ReasonCode: code, Detail: detail, ActionID: action, Metrics: []HealthMetric{}}
}

func healthMetric(key, label string, value int64, unit string) HealthMetric {
	return HealthMetric{Key: key, Label: label, Value: value, Unit: unit}
}

func healthAvailable(key, label string, available bool, missing, action string) HealthItem {
	if available {
		return healthItem(key, label, "ok", "ready", action)
	}
	return healthItem(key, label, "unavailable", missing, action)
}

// GetSystemHealthSection reads one section without installing, scanning or repairing media.
func (a *App) GetSystemHealthSection(section string) (HealthSection, error) {
	result := HealthSection{Key: section, Items: []HealthItem{}}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 5*time.Second)
	defer cancel()
	switch section {
	case "database":
		result.Items = a.healthDatabase(ctx)
	case "storage":
		result.Items = a.healthStorage(ctx)
	case "runtimes":
		result.Items = a.healthRuntimes()
	case "indexes":
		result.Items = a.healthIndexes(ctx)
	case "tasks":
		result.Items = a.healthTasks()
	case "caches":
		result.Items = a.healthCaches(ctx)
	default:
		return result, errors.New("unknown_health_section: 未知运行状态分区")
	}
	result.CheckedAt = time.Now().UTC()
	return result, nil
}

func (a *App) healthDatabase(ctx context.Context) []HealthItem {
	item := healthItem("backend", "数据库", "ok", "ready", "action:settings:database")
	if reason := a.databaseUnavailableReason(); reason != "" {
		item = healthItem("backend", "数据库", "unavailable", "database_unavailable", "action:settings:database")
		item.Detail = reason
	} else {
		item.Detail = "当前使用 " + string(database.ActiveBackend())
	}
	items := []HealthItem{item}
	if item.State != "ok" || a.backupService == nil {
		return append(items, healthItem("backup", "数据库备份", "unknown", "not_checked", "action:settings:backup"))
	}
	status, err := a.backupService.HealthStatus(ctx)
	if err != nil {
		return append(items, healthItem("backup", "数据库备份", "unknown", "check_failed", "action:settings:backup"))
	}
	backup := healthAvailable("backup", "数据库备份", status.BackupAvailable, "unavailable", "action:settings:backup")
	if status.LastError != "" {
		backup = healthItem("backup", "数据库备份", "warning", "last_run_failed", "action:settings:backup")
	}
	backup.Metrics = []HealthMetric{healthMetric("retention", "保留份数", int64(status.RetentionCount), "份"), healthMetric("interval_hours", "备份间隔", int64(status.IntervalHours), "小时")}
	return append(items, backup)
}

func (a *App) healthStorage(ctx context.Context) []HealthItem {
	if a.databaseUnavailableReason() != "" || a.directoryService == nil {
		return []HealthItem{healthItem("roots", "扫描目录", "unknown", "database_unavailable", "action:settings:scan-dirs")}
	}
	dirs, err := a.directoryService.GetAllDirectoriesContext(ctx)
	if err != nil {
		return []HealthItem{healthItem("roots", "扫描目录", "unknown", "check_failed", "action:settings:scan-dirs")}
	}
	states := map[uint]services.LibraryWatchRootStatus{}
	if a.libraryWatcher != nil {
		status := a.libraryWatcher.Snapshot()
		if len(status.Roots) == 0 && a.settingsService != nil {
			settings, settingsErr := a.settingsService.GetSettingsContext(ctx)
			if settingsErr == nil && !settings.LibraryWatchEnabled {
				for _, dir := range dirs {
					status.Roots = append(status.Roots, services.LibraryWatchRootStatus{DirectoryID: dir.ID, State: services.LibraryWatchStateDisabled})
				}
			}
		}
		for _, root := range status.Roots {
			states[root.DirectoryID] = root
		}
	}
	items := make([]HealthItem, 0, len(dirs))
	for index, dir := range dirs {
		item := healthItem("root_"+strconv.Itoa(index+1), "扫描目录 "+strconv.Itoa(index+1), "unknown", "not_checked", "action:settings:scan-dirs")
		if state, exists := states[dir.ID]; exists {
			switch state.State {
			case services.LibraryWatchStateWatching:
				item.State, item.ReasonCode = "ok", "watching"
			case services.LibraryWatchStateDisabled:
				item.State, item.ReasonCode = "disabled", "disabled"
			case services.LibraryWatchStateUnavailable:
				item.State, item.ReasonCode = "unavailable", "root_unavailable"
			case services.LibraryWatchStateError:
				item.State, item.ReasonCode = "warning", "check_failed"
			}
			item.Detail = healthReasonText[item.ReasonCode]
		}
		item.LocalDetail = dir.Path
		items = append(items, item)
	}
	if len(dirs) == 0 {
		items = append(items, healthItem("roots", "视频扫描目录", "disabled", "no_roots", "action:settings:scan-dirs"))
	}
	if a.imageService != nil {
		imageDirs, err := a.imageService.GetAllImageDirectoriesContext(ctx)
		if err != nil {
			items = append(items, healthItem("image_roots", "图片扫描目录", "unknown", "check_failed", "action:settings:image-dirs"))
		} else {
			for index, dir := range imageDirs {
				item := healthItem("image_root_"+strconv.Itoa(index+1), "图片目录 "+strconv.Itoa(index+1), "unknown", "not_checked", "action:settings:image-dirs")
				item.Detail = "图片目录按需扫描，此处不探测磁盘；可到设置检查或扫描"
				item.LocalDetail = dir.Path
				items = append(items, item)
			}
		}
	}
	return items
}

func (a *App) healthRuntimes() []HealthItem {
	items := []HealthItem{}
	if a.subtitleService != nil {
		for _, tool := range a.subtitleService.MediaToolAvailability() {
			items = append(items, healthAvailable(tool.Name, tool.Name, tool.Available, "missing_"+tool.Name, "action:settings:subtitle-quality"))
		}
		statuses, checkedAt, checked := a.subtitleService.CachedEngineStatuses()
		if !checked {
			items = append(items, healthItem("subtitle", "字幕引擎", "unknown", "not_checked", "action:settings:subtitle-quality"))
		} else {
			for _, status := range statuses {
				key, label := string(status.Engine), "WhisperX 字幕引擎"
				if status.Engine == services.SubtitleEngineQwen {
					label = "Qwen 字幕引擎"
				} else if status.Engine != services.SubtitleEngineWhisperX {
					continue
				}
				item := healthAvailable(key, label, status.Available, string(status.ReasonCode), "action:settings:subtitle-quality")
				item.ObservedAt = &checkedAt
				item.Detail += "（缓存检查于 " + checkedAt.Local().Format("2006-01-02 15:04:05") + "）"
				items = append(items, item)
			}
		}
	} else {
		items = append(items, healthItem("subtitle", "字幕引擎", "unknown", "not_initialized", "action:settings:subtitle-quality"))
	}
	face := healthItem("face", "人脸识别", "unknown", "not_initialized", "action:settings:face")
	if a.faceRuntime != nil {
		face = healthItem("face", "人脸识别", "unknown", "not_checked", "action:settings:face")
	}
	if status, checkedAt, checked := a.faceRuntime.CachedStatus(); checked {
		code := "missing_runtime"
		switch status.State {
		case "available":
			code = "ready"
		case "missing_model":
			code = "missing_model"
		case "incompatible":
			code = "unsupported_platform"
		case "download_failed":
			code = "check_failed"
		}
		face = healthAvailable("face", "人脸识别", status.State == "available", code, "action:settings:face")
		if status.Preparing {
			face = healthItem("face", "人脸识别", "warning", "preparing", "action:settings:face")
		}
		face.Detail += "（缓存检查于 " + checkedAt.Local().Format("2006-01-02 15:04:05") + "）"
		face.ObservedAt = &checkedAt
	}
	items = append(items, face, a.healthSceneRuntime())
	enhance := healthItem("enhancement", "视频超分", "unknown", "not_initialized", "action:settings:enhance")
	if a.enhancement != nil {
		status := a.enhancement.Capability()
		code := status.ReasonCode
		if code == "platform_unsupported" {
			code = "unsupported_platform"
		}
		if code == "runtime_not_bundled" || code == "runtime_missing" || code == "runtime_unavailable" {
			code = "missing_runtime"
		}
		enhance = healthAvailable("enhancement", "视频超分", status.Available, code, "action:settings:enhance")
	}
	return append(items, enhance)
}

func (a *App) healthIndexes(ctx context.Context) []HealthItem {
	if a.databaseUnavailableReason() != "" {
		return []HealthItem{healthItem("indexes", "索引", "unknown", "database_unavailable", "action:settings:database")}
	}
	items := []HealthItem{}
	if a.subtitleSearchService != nil {
		status := a.subtitleSearchService.GetSubtitleIndexSyncStatus()
		item := healthItem("subtitles", "字幕索引", "unknown", "not_checked", "nav:page:videos")
		if status.LastSyncedAt != nil {
			item.State, item.ReasonCode, item.Detail = "ok", "snapshot", "上次同步："+status.LastSyncedAt.Local().Format("2006-01-02 15:04:05")
			item.Metrics = []HealthMetric{healthMetric("checked", "上次检查视频", int64(status.Checked), "部")}
		}
		if status.Running {
			item.State, item.ReasonCode, item.Detail = "warning", "syncing", healthReasonText["syncing"]
		} else if status.Error != "" {
			item.State, item.ReasonCode, item.Detail = "warning", "check_failed", healthReasonText["check_failed"]
		}
		items = append(items, item)
	}
	video := healthItem("video_semantic", "视频语义索引", "unavailable", "unavailable", "action:settings:ai-tags")
	if svc := a.semanticIndexService(); svc != nil {
		status, err := svc.HealthSnapshot(ctx)
		video = healthIndexItem(video, status, err, "部", "活跃视频")
	}
	items = append(items, video)
	photo := healthItem("image_semantic", "图片语义索引", "unavailable", "unavailable", "action:settings:ai-tags")
	if svc := a.imageSemanticIndexService(); svc != nil {
		status, err := svc.HealthSnapshot(ctx)
		photo = healthIndexItem(photo, status, err, "张", "活跃图片")
	}
	return append(items, photo, a.healthSceneIndex(ctx))
}

func healthIndexItem(item HealthItem, status services.SemanticHealthSnapshot, err error, unit, totalLabel string) HealthItem {
	if err != nil {
		return healthItem(item.Key, item.Label, "unknown", "check_failed", item.ActionID)
	}
	if !status.Available {
		return item
	}
	item.State, item.ReasonCode, item.Detail = "ok", "snapshot", "已发布索引数据；未测试外部查询接口"
	if status.NeedsRebuild {
		item.State, item.ReasonCode, item.Detail = "warning", "needs_rebuild", healthReasonText["needs_rebuild"]
	} else if !status.Built {
		item.State, item.ReasonCode, item.Detail = "warning", "not_checked", "尚未构建索引"
	}
	item.Metrics = []HealthMetric{healthMetric("indexed", "已索引", status.Coverage.Indexed, unit), healthMetric("total", totalLabel, status.Coverage.Total, unit)}
	return item
}

func (a *App) healthTasks() []HealthItem {
	if a.backgroundTasks == nil {
		return []HealthItem{healthItem("background", "后台任务", "unknown", "not_initialized", "action:task-center")}
	}
	item := healthItem("background", "后台任务", "ok", "ready", "action:task-center")
	running := len(a.backgroundTasks.Snapshot())
	item.Metrics = append(item.Metrics, healthMetric("running", "运行中", int64(running), "项"))
	if running > 0 {
		item.State, item.ReasonCode, item.Detail = "warning", "running", healthReasonText["running"]
	}
	if a.idleGate != nil {
		waiting, known := a.idleGate.WaitingSnapshot()
		if !known {
			item.State, item.ReasonCode, item.Detail = "unknown", "not_checked", "等待任务清单正在更新，请刷新"
			return []HealthItem{item}
		}
		item.Metrics = append(item.Metrics, healthMetric("waiting", "等待空闲", int64(len(waiting)), "项"))
		if running == 0 && len(waiting) > 0 {
			item.State, item.ReasonCode, item.Detail = "warning", "waiting_idle", healthReasonText["waiting_idle"]
		}
	}
	return []HealthItem{item}
}

func (a *App) healthCaches(ctx context.Context) []HealthItem {
	item := healthItem("proxies", "播放兼容缓存", "unknown", "not_checked", "action:settings:playback-proxy")
	if a.databaseUnavailableReason() != "" {
		return []HealthItem{healthItem(item.Key, item.Label, "unknown", "database_unavailable", item.ActionID)}
	}
	if a.playbackProxies != nil {
		usage, err := a.playbackProxies.RecordedHealthUsage(ctx)
		if err != nil {
			item.State, item.ReasonCode, item.Detail = "unknown", "check_failed", healthReasonText["check_failed"]
		} else {
			item.State, item.ReasonCode, item.Detail = "ok", "snapshot", "仅统计库内登记的代理；磁盘孤儿文件可到缓存设置中检查"
			item.Metrics = []HealthMetric{healthMetric("bytes", "登记占用", usage.TotalBytes, "bytes"), healthMetric("count", "登记代理", int64(usage.Count), "份"), healthMetric("limit_bytes", "上限（0 为不限）", usage.LimitBytes, "bytes")}
		}
	}
	return []HealthItem{item}
}

type diagnosticItem struct {
	Key        string           `json:"key"`
	Ordinal    int              `json:"ordinal"`
	State      string           `json:"state"`
	ReasonCode string           `json:"reason_code"`
	ObservedAt *time.Time       `json:"observed_at,omitempty"`
	Metrics    map[string]int64 `json:"metrics"`
}

type diagnosticSection struct {
	Key       string           `json:"key"`
	CheckedAt time.Time        `json:"checked_at"`
	Items     []diagnosticItem `json:"items"`
}

type diagnosticReport struct {
	SchemaVersion int                 `json:"schema_version"`
	GeneratedAt   time.Time           `json:"generated_at"`
	Platform      string              `json:"platform"`
	Architecture  string              `json:"architecture"`
	Backend       string              `json:"backend"`
	Sections      []diagnosticSection `json:"sections"`
}

func safeDiagnosticSection(section HealthSection) diagnosticSection {
	result := diagnosticSection{Key: section.Key, CheckedAt: section.CheckedAt, Items: []diagnosticItem{}}
	allowedMetrics := map[string]bool{"retention": true, "interval_hours": true, "checked": true, "indexed": true, "total": true, "running": true, "waiting": true, "bytes": true, "count": true, "orphan_bytes": true, "limit_bytes": true}
	for index, source := range section.Items {
		item := diagnosticItem{Ordinal: index + 1, State: "unknown", ReasonCode: "check_failed", Metrics: map[string]int64{}}
		item.ObservedAt = source.ObservedAt
		switch source.Key {
		case "backend", "backup", "roots", "image_roots", "ffmpeg", "ffprobe", "subtitle", "whisperx", "qwen", "face", "enhancement", "indexes", "subtitles", "video_semantic", "image_semantic", "background", "proxies":
			item.Key = source.Key
		default:
			item.Key = "item_" + strconv.Itoa(index+1)
		}
		switch source.State {
		case "ok", "warning", "unavailable", "disabled", "unknown":
			item.State = source.State
		}
		if _, known := healthReasonText[source.ReasonCode]; known {
			item.ReasonCode = source.ReasonCode
		}
		for _, metric := range source.Metrics {
			if allowedMetrics[metric.Key] {
				item.Metrics[metric.Key] = metric.Value
			}
		}
		result.Items = append(result.Items, item)
	}
	return result
}

var selectHealthReportPath = func(ctx context.Context) (string, error) {
	return wailsruntime.SaveFileDialog(ctx, wailsruntime.SaveDialogOptions{Title: "导出本地诊断报告", DefaultFilename: "cineinsight-diagnostics-" + time.Now().Format("20060102-150405") + ".json", Filters: []wailsruntime.FileFilter{{DisplayName: "JSON 诊断报告", Pattern: "*.json"}}})
}

// ExportSystemHealthReport saves only whitelisted state; no raw logs or media are read.
func (a *App) ExportSystemHealthReport() (HealthReportExport, error) {
	if a.ctx == nil {
		return HealthReportExport{}, errors.New("应用窗口尚未就绪")
	}
	path, err := selectHealthReportPath(a.ctx)
	if err != nil {
		return HealthReportExport{}, errors.New("打开保存对话框失败")
	}
	if path == "" {
		return HealthReportExport{Message: "已取消导出"}, nil
	}
	report := diagnosticReport{SchemaVersion: 1, GeneratedAt: time.Now().UTC(), Platform: runtime.GOOS, Architecture: runtime.GOARCH, Backend: string(database.ActiveBackend()), Sections: []diagnosticSection{}}
	for _, key := range healthSectionKeys {
		section, err := a.GetSystemHealthSection(key)
		if err != nil {
			return HealthReportExport{}, errors.New("生成诊断报告失败")
		}
		report.Sections = append(report.Sections, safeDiagnosticSection(section))
	}
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return HealthReportExport{}, errors.New("生成诊断报告失败")
	}
	if err := writeHealthReport(path, append(content, '\n')); err != nil {
		return HealthReportExport{}, err
	}
	return HealthReportExport{Saved: true, Message: "诊断报告已保存；不含路径、媒体内容、凭证或原始日志"}, nil
}

func writeHealthReport(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("报告文件已存在，请选择新的文件名")
		}
		return errors.New("无法创建诊断报告，请检查目录权限和磁盘空间")
	}
	created, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return errors.New("无法确认诊断文件身份")
	}
	_, writeErr := file.Write(content)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		if current, err := os.Lstat(path); err == nil && os.SameFile(created, current) {
			_ = os.Remove(path)
		}
		return errors.New("保存诊断报告失败，请检查磁盘空间和写入权限")
	}
	return nil
}
