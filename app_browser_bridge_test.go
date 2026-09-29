package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
	"video-master/services"

	"gorm.io/gorm"
)

// MEDIA-09：「把下载目录加入扫描目录」加目录后窄对账，把这一个下载收进片库；目录已在扫描
// 范围内时不重复添加。任务取自重启后的历史行（内存里没有它），三个入库动作照样可用。
func TestAddDownloadDirectoryToScanImportsDownloadMEDIA09(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2.0}).Error; err != nil {
		t.Fatalf("初始化设置失败: %v", err)
	}
	downloadDir := filepath.Join(t.TempDir(), "downloads")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(downloadDir, "已下载.mp4")
	if err := os.WriteFile(outputPath, []byte("fake-media"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	row := models.BrowserDownloadTask{
		TaskUID: "bd-history-1", DisplayURL: "https://cdn/a.m3u8", FileName: "已下载.mp4",
		Directory: downloadDir, Status: "done", FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatalf("写入历史任务失败: %v", err)
	}

	directories := &services.DirectoryService{}
	downloads := services.NewBrowserDownloadService(services.BrowserDownloadDeps{
		ImportDirectory: services.BrowserDownloadImporterFromScan(&services.VideoService{}, directories.GetAllDirectories),
	})
	downloads.SetStore(func() *gorm.DB { return database.DB })
	app := &App{directoryService: directories, settingsService: &services.SettingsService{}, browserDownloads: downloads}

	if listed := app.ListDownloadTasks(); len(listed) != 1 || listed[0].ImportStatus != services.BrowserDownloadImportNotInScanRoots {
		t.Fatalf("历史任务应当现算出 not_in_scan_roots: %+v", listed)
	}

	result, err := app.AddDownloadDirectoryToScan(row.TaskUID)
	if err != nil || result.Code != services.BrowserDownloadCodeOK || result.Task == nil || result.Task.ImportStatus != services.BrowserDownloadImportImported {
		t.Fatalf("加入扫描目录后应当入库成功: %+v err=%v", result, err)
	}
	dirs, err := directories.GetAllDirectories()
	if err != nil || len(dirs) != 1 || dirs[0].Path != downloadDir {
		t.Fatalf("下载目录应当被加入扫描目录: %+v err=%v", dirs, err)
	}
	var video models.Video
	if err := database.DB.Where("path = ?", outputPath).First(&video).Error; err != nil {
		t.Fatalf("下载的文件应当进了片库: %v", err)
	}
	if result.Task.VideoID != video.ID {
		t.Fatalf("任务应当带上入库后的视频 ID: task=%d video=%d", result.Task.VideoID, video.ID)
	}

	// 再点一次：目录已在扫描范围内，不重复添加。
	again, err := app.AddDownloadDirectoryToScan(row.TaskUID)
	if err != nil || again.Code != services.BrowserDownloadCodeOK {
		t.Fatalf("重复加入应当直接重新入库: %+v err=%v", again, err)
	}
	if dirs, _ := directories.GetAllDirectories(); len(dirs) != 1 {
		t.Fatalf("目录已覆盖时不该重复添加: %+v", dirs)
	}

	// 重启后的任务不能重试：请求头不在内存里。
	if retry, err := app.RetryDownload(row.TaskUID); err != nil || retry.Code != services.BrowserDownloadCodeRetryRequiresBrowser {
		t.Fatalf("历史任务重试应返回 retry_requires_browser: %+v err=%v", retry, err)
	}
}
