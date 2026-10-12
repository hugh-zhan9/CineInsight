package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// 失联数据治理（D-LD-*）的服务测试夹具。

func setupLostMediaTest(t *testing.T) (*LostMediaService, string) {
	t.Helper()
	db := dbtest.Open(t)
	database.DB = db
	if err := db.Create(&models.Settings{VideoExtensions: ".mp4", ImageExtensions: ".jpg"}).Error; err != nil {
		t.Fatalf("初始化设置失败: %v", err)
	}
	dataDir := t.TempDir()
	service := NewLostMediaService(dataDir)
	service.SetDependencies(&VideoService{}, NewImageService(), nil)
	return service, dataDir
}

func lostTestRoot(t *testing.T, kind string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if kind == LostMediaKindVideo {
		if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
			t.Fatal(err)
		}
	} else if err := database.DB.Create(&models.ImageDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	return root
}

func lostTestWriteFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lostTestStaleVideo 建一条活跃的失效视频行。
func lostTestStaleVideo(t *testing.T, path string, size int64, reason string) models.Video {
	t.Helper()
	video := models.Video{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path), Size: size, IsStale: true, StaleReason: reason}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	return video
}

// lostTestScannerTrashVideo 建一条扫描器因文件缺失软删的视频行与它的 missing 条目。
func lostTestScannerTrashVideo(t *testing.T, path string, size int64) (models.Video, models.VideoTrashEntry) {
	t.Helper()
	video := models.Video{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path), Size: size}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", video.ID).
		Updates(map[string]any{"deleted_at": time.Now(), "deleted_by": "scanner"}).Error; err != nil {
		t.Fatal(err)
	}
	entry := models.VideoTrashEntry{VideoID: video.ID, VideoName: video.Name, OriginalPath: path, DeletedBy: "scanner",
		FileSize: size, State: trashStateDeleted, Mode: models.TrashModeMissing}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	return video, entry
}

func lostTestImage(t *testing.T, path string, size int64, stale, scannerDeleted bool) models.Image {
	t.Helper()
	image := models.Image{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path), Size: size, IsStale: stale}
	if err := database.DB.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	if scannerDeleted {
		if err := database.DB.Unscoped().Model(&models.Image{}).Where("id = ?", image.ID).
			Updates(map[string]any{"deleted_at": time.Now(), "deleted_by": "scanner"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return image
}

// lostTestOfflineVolume 让 mount 下的路径按「卷未挂载」处理（mediaPathUnavailable 的单测替身）。
func lostTestOfflineVolume(t *testing.T, mount string) {
	t.Helper()
	fn := func(path string) error {
		if pathUnderRoot(filepath.Clean(path), mount) {
			return os.ErrNotExist
		}
		return nil
	}
	previous := mediaVolumeAvailableFn.Swap(&fn)
	t.Cleanup(func() { mediaVolumeAvailableFn.Store(previous) })
}

// lostReportItems 按报告里的目录分组逐页读出全部明细（ListLostMediaItems）。
func lostReportItems(t *testing.T, service *LostMediaService, report *LostMediaReport) map[uint]LostMediaItem {
	t.Helper()
	items := map[uint]LostMediaItem{}
	for _, root := range report.Roots {
		for _, dir := range root.Directories {
			var after uint
			for {
				page, err := service.ListLostMediaItems(lostTestContext(), LostMediaItemQuery{Kind: report.Kind, Directory: dir.Directory, AfterID: after, Limit: 2})
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range page.Items {
					items[item.ID] = item
					after = item.ID
				}
				if !page.HasMore {
					break
				}
			}
		}
	}
	return items
}

func lostTestContext() context.Context { return context.Background() }

// 找回、清除、回收在 lost_media 任务键下同一时刻只允许一个；StopAndWait 取消并等它收尾。
func TestLostMediaOperationsAreExclusiveAndStoppable(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	registry := NewBackgroundTaskRegistry()
	service.SetBackgroundTaskRegistry(registry)
	ctx, finish, err := service.begin(lostTestContext(), "purge", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.Snapshot(); len(got) != 1 || got[0] != string(BackgroundTaskLostMedia) {
		t.Fatalf("运行中应登记 lost_media: %v", got)
	}
	if _, err := service.ApplyLostRelink(lostTestContext(), LostRelinkApplyRequest{Kind: LostMediaKindVideo, Pairs: []LostRelinkPair{{ID: 1, NewPath: "/a"}}}); LostMediaErrorCode(err) != "lost_media_busy" {
		t.Fatalf("第二个请求应返回 lost_media_busy: %v", err)
	}
	if _, err := service.ReclaimOrphanFiles(lostTestContext(), []string{"faces"}); LostMediaErrorCode(err) != "lost_media_busy" {
		t.Fatalf("回收同样互斥: %v", err)
	}
	go func() {
		<-ctx.Done()
		finish()
	}()
	service.StopAndWait()
	// finish 先离开运行态、注销登记再放行 StopAndWait，所以这里一定已经收尾。
	if len(registry.Snapshot()) != 0 || service.Status().Running {
		t.Fatalf("StopAndWait 应等进行中的操作收尾")
	}
}
