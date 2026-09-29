package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 复审 A（回收站/扫描数据安全 + 手机端 PIN 加固）修复的回归测试。测试名里的 LIBxx / IMGxx / PLAYxx
// 是问题清单 ID，I-n / Minor n 是复审 A 的问题编号。测试进程里的系统废纸篓是替身
// （system_trash_testhook_test.go）：同卷重命名进「原目录/.Trash」。

// reviewAUnmountVolume 注入「卷未挂载」替身：prefixes 之下的路径一律报告卷未挂载，其余走真实检查。
// 返回「重新挂载」函数；测试结束时自动恢复。
func reviewAUnmountVolume(t *testing.T, prefixes ...string) func() {
	t.Helper()
	fn := func(path string) error {
		clean := filepath.Clean(path)
		for _, prefix := range prefixes {
			if pathIsEqualOrInside(clean, prefix) {
				return fmt.Errorf("磁盘未挂载: %w", os.ErrNotExist)
			}
		}
		return scanVolumeAvailable(path)
	}
	previous := mediaVolumeAvailableFn.Swap(&fn)
	var once sync.Once
	remount := func() { once.Do(func() { mediaVolumeAvailableFn.Store(previous) }) }
	t.Cleanup(remount)
	return remount
}

// reviewAFailDeletesOn 让事务里对 table 的 DELETE 报错（模拟恢复事务失败）。返回关闭注入的函数。
func reviewAFailDeletesOn(t *testing.T, table string) func() {
	t.Helper()
	var armed atomic.Bool
	armed.Store(true)
	name := "review_a:fail_delete_" + table
	if err := database.DB.Callback().Delete().Before("gorm:delete").Register(name, func(db *gorm.DB) {
		if armed.Load() && db.Statement.Schema != nil && db.Statement.Schema.Table == table {
			_ = db.AddError(errors.New("注入的恢复事务失败"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	// 每个测试都用新库（dbtest.Open），回调随库一起丢弃；这里只负责解除注入。
	disable := func() { armed.Store(false) }
	t.Cleanup(disable)
	return disable
}

// ---------- I-1：自动放回恢复失败时不得把用户文件移回废纸篓 ----------

func TestLIB05PutBackRestoreTxFailureLeavesFileAtOriginalI1(t *testing.T) {
	svc, video, entry, _ := p010PutBackFixture(t)
	center := NewTrashCenter(svc, nil)
	disable := reviewAFailDeletesOn(t, "video_trash_entries")

	result, err := center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Succeeded != 0 || result.Failed != 1 {
		t.Fatalf("注入失败后恢复应报失败: %#v err=%v", result, err)
	}
	if _, err := os.Stat(video.Path); err != nil {
		t.Fatalf("文件必须仍在原路径: %v", err)
	}
	if _, err := os.Stat(entry.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("不得把用户放回的文件移回废纸篓: %v", err)
	}
	if got := p010VideoEntry(t, video.ID); got.State != trashStateDeleted {
		t.Fatalf("条目状态应保持 deleted: %#v", got)
	}
	var still models.Video
	if err := database.DB.Unscoped().First(&still, video.ID).Error; err != nil || !still.DeletedAt.IsValid() {
		t.Fatalf("记录应仍是软删: %#v err=%v", still, err)
	}

	// 扫描发现放回时同样走这条路：失败只报错，文件不动。
	scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: filepath.Dir(video.Path)}})
	if scan.Restored != 0 || scan.Added != 0 || len(scan.Errors) == 0 {
		t.Fatalf("扫描时恢复失败应报错且不新建: %+v", scan)
	}
	if _, err := os.Stat(video.Path); err != nil {
		t.Fatalf("扫描失败后文件仍须在原路径: %v", err)
	}

	// 可再次尝试：注入解除后显式恢复成功。
	disable()
	result, err = center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("再次恢复应成功: %#v err=%v", result, err)
	}
	if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("原记录应被恢复: %v", err)
	}
}

func TestIMG02PutBackRestoreTxFailureLeavesFileAtOriginalI1(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	path := filepath.Join(root, "photo.jpg")
	image := imageTrashTestCreateImage(t, path, "photo-bytes")
	mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010ImageEntry(t, image.ID)
	if err := os.Rename(entry.TrashPath, path); err != nil {
		t.Fatal(err)
	}
	disable := reviewAFailDeletesOn(t, "image_trash_entries")
	result, err := svc.SyncImageDirectories()
	if err != nil || result.Restored != 0 || result.Added != 0 || len(result.Errors) == 0 {
		t.Fatalf("恢复事务失败时扫描应报错: %#v err=%v", result, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件必须仍在原路径: %v", err)
	}
	if _, err := os.Stat(entry.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("不得把放回的图片移回废纸篓: %v", err)
	}
	if got := p010ImageEntry(t, image.ID); got.State != trashStateDeleted {
		t.Fatalf("条目状态应保持 deleted: %#v", got)
	}
	disable()
	result, err = svc.SyncImageDirectories()
	if err != nil || result.Restored != 1 {
		t.Fatalf("再次扫描应恢复原记录: %#v err=%v", result, err)
	}
}

// 反面：这一次确实把文件从废纸篓移回了原处时，事务失败仍要把文件放回废纸篓（补偿不变）。
func TestLIB05RestoreTxFailureAfterMovingFromTrashCompensatesI1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video := p010Video(t, filepath.Join(t.TempDir(), "moved.mp4"), "moved-content")
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	reviewAFailDeletesOn(t, "video_trash_entries")
	if _, err := svc.RestoreTrashEntry(entry.ID); err == nil {
		t.Fatal("注入失败后恢复应报错")
	}
	if _, err := os.Stat(entry.TrashPath); err != nil {
		t.Fatalf("本次从废纸篓移出的文件应被补偿回废纸篓: %v", err)
	}
	if _, err := os.Stat(video.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("原路径不应留下文件: %v", err)
	}
	if got := p010VideoEntry(t, video.ID); got.State != trashStateDeleted {
		t.Fatalf("条目应回到 deleted: %#v", got)
	}
}

// ---------- I-2：父目录不存在不是离线 ----------

func reviewAPendingTrashEntry(t *testing.T, video models.Video) models.VideoTrashEntry {
	t.Helper()
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatal(err)
	}
	entry := models.VideoTrashEntry{
		DeletedBy: "user", VideoID: video.ID, VideoName: video.Name, OriginalPath: video.Path,
		FileSize: info.Size(), FileModTime: info.ModTime().UnixNano(), FileIdentity: stableFileIdentity(info),
		State: trashStatePendingMove, Mode: models.TrashModeTrash, DeleteBatchID: newDeleteBatchID(),
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestLIB05CrashRecoveryParentDirRemovedFallsToBranchThreeI2(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	base := t.TempDir()
	onlineDir := filepath.Join(base, "online", "movies")
	offlineMount := filepath.Join(base, "external")
	online := p010Video(t, filepath.Join(onlineDir, "a.mp4"), "content-a")
	offline := p010Video(t, filepath.Join(offlineMount, "b.mp4"), "content-b")
	reviewAPendingTrashEntry(t, online)
	reviewAPendingTrashEntry(t, offline)
	// 卷在线，只是父目录被删掉了：文件确实不在。另一块盘则是真的未挂载。
	if err := os.RemoveAll(onlineDir); err != nil {
		t.Fatal(err)
	}
	reviewAUnmountVolume(t, offlineMount)
	if err := os.Remove(offline.Path); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileTrashEntries(); err == nil {
		t.Fatal("离线卷上的条目应报告无法处理")
	}
	if got := p010VideoEntry(t, online.ID); got.State != trashStateDeleted || got.LastError != trashUnknownLocationMessage {
		t.Fatalf("卷在线、父目录被删应落入分支 3: %#v", got)
	}
	if got := p010VideoEntry(t, offline.ID); got.State != trashStatePendingMove {
		t.Fatalf("卷离线时 pending_move 必须保持原状: %#v", got)
	}
	if err := database.DB.First(&models.Video{}, offline.ID).Error; err != nil {
		t.Fatalf("离线卷上的记录应仍然活跃: %v", err)
	}
}

func TestLIB11StagedSourceParentDirRemovedIsCleanedI2(t *testing.T) {
	setupVideoServiceTestDB(t)
	base := t.TempDir()
	goneParent := filepath.Join(base, "removed-parent")
	offlineMount := filepath.Join(base, "external")
	reviewAUnmountVolume(t, offlineMount)
	rows := []models.MigrationStagedSource{
		{OriginalPath: filepath.Join(goneParent, "a.mp4"), StagedPath: filepath.Join(goneParent, ".a.mp4.cineinsight-migrating-1"), Size: 1, State: models.MigrationStagedStatePending},
		{OriginalPath: filepath.Join(offlineMount, "b.mp4"), StagedPath: filepath.Join(offlineMount, ".b.mp4.cineinsight-migrating-2"), Size: 1, State: models.MigrationStagedStatePending},
		{OriginalPath: filepath.Join(goneParent, "c.mp4"), StagedPath: filepath.Join(goneParent, ".c.mp4.cineinsight-migrating-3"), Size: 1, State: models.MigrationStagedStatePending},
		{OriginalPath: filepath.Join(goneParent, "d.mp4"), StagedPath: filepath.Join(goneParent, ".d.mp4.cineinsight-migrating-4"), Size: 1, State: models.MigrationStagedStatePending},
	}
	for i := range rows {
		if err := database.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 移入废纸篓与删除：父目录不存在按「文件已不在」处理，置 cleaned。
	trashed := NewTrashCenter(nil, nil).TrashStagedSources([]uint{rows[2].ID})
	if trashed.Items[0].Code != TrashResultFileMissing {
		t.Fatalf("父目录不存在的残留应按文件缺失处理: %#v", trashed)
	}
	deleted, err := (&VideoService{}).DeleteStagedSources([]uint{rows[3].ID})
	if err != nil || deleted.Cleaned != 1 || len(deleted.Failed) != 0 {
		t.Fatalf("父目录不存在的残留删除应直接置 cleaned: %#v err=%v", deleted, err)
	}
	listed, err := (&VideoService{}).ListStagedSources()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != rows[1].ID {
		t.Fatalf("只有离线卷上的残留保持 pending: %#v", listed)
	}
	for _, id := range []uint{rows[0].ID, rows[2].ID, rows[3].ID} {
		var row models.MigrationStagedSource
		if err := database.DB.First(&row, id).Error; err != nil || row.State != models.MigrationStagedStateCleaned {
			t.Fatalf("父目录不存在的残留应置 cleaned: %#v err=%v", row, err)
		}
	}
}

func TestLIB04DeleteWithParentDirRemovedIsFileMissingI2(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	createDirectoryRow(t, root)
	dir := filepath.Join(root, "season")
	video := models.Video{Name: "e01.mp4", Path: filepath.Join(dir, "e01.mp4"), Directory: dir, Size: 3}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	// 扫描根在线、父目录不存在：文件确实不在 → file_missing，只清库记录。
	result := svc.DeleteVideosDetailed([]uint{video.ID}, true, BatchDeleteOptions{})
	if result.Items[0].Code != TrashResultFileMissing {
		t.Fatalf("父目录不存在应按文件缺失处理: %#v", result)
	}
	if got := p010VideoEntry(t, video.ID); got.Mode != models.TrashModeMissing {
		t.Fatalf("条目应为 missing: %#v", got)
	}

	// 同样的形态，但卷未挂载：volume_offline，不降级。
	mount := filepath.Join(t.TempDir(), "external")
	createDirectoryRow(t, mount)
	offline := models.Video{Name: "x.mp4", Path: filepath.Join(mount, "sub", "x.mp4"), Directory: filepath.Join(mount, "sub"), Size: 3}
	if err := database.DB.Create(&offline).Error; err != nil {
		t.Fatal(err)
	}
	reviewAUnmountVolume(t, mount)
	result = svc.DeleteVideosDetailed([]uint{offline.ID}, true, BatchDeleteOptions{})
	if result.Items[0].Code != TrashResultVolumeOffline {
		t.Fatalf("卷未挂载应返回 volume_offline: %#v", result)
	}
	if active := p010ActiveVideos(t, offline.Path); len(active) != 1 {
		t.Fatal("卷未挂载时记录必须保持活跃")
	}
}

// ---------- Minor 3：卷离线时清除与恢复 ----------

func TestLIB05PurgeAndRestoreOnOfflineVolumeKeepStateMinor3(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	mount := filepath.Join(t.TempDir(), "external")
	video := p010Video(t, filepath.Join(mount, "movies", "a.mp4"), "content-a")
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	center := NewTrashCenter(svc, nil)
	// 卷被拔掉：废纸篓里那份读不到。
	remount := reviewAUnmountVolume(t, mount)
	if err := os.Remove(entry.TrashPath); err != nil {
		t.Fatal(err)
	}
	purge, err := center.PurgeTrashEntries("video", []uint{entry.ID})
	if err != nil || purge.Items[0].Code != TrashResultVolumeOffline {
		t.Fatalf("卷离线时清除应返回 volume_offline: %#v err=%v", purge, err)
	}
	var count int64
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", video.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("卷离线时不得硬删记录: %d err=%v", count, err)
	}
	restore, err := center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || restore.Items[0].Code != TrashResultVolumeOffline {
		t.Fatalf("卷离线时恢复应返回 volume_offline 而不是文件已不存在: %#v err=%v", restore, err)
	}
	if got := p010VideoEntry(t, video.ID); got.State != trashStateDeleted {
		t.Fatalf("卷离线时条目状态不变: %#v", got)
	}
	// 卷回来了、文件确实不在：清除按文件缺失硬删。
	remount()
	purge, err = center.PurgeTrashEntries("video", []uint{entry.ID})
	if err != nil || purge.Succeeded != 1 {
		t.Fatalf("卷在线且文件不在时清除应成功: %#v err=%v", purge, err)
	}
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", video.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("记录应被硬删: %d err=%v", count, err)
	}
}

// 废纸篓在别的卷（例如 ~/.Trash）且那一份还在、原路径所在卷未挂载：不得往空挂载点里恢复。
func TestLIB05RestoreIntoUnmountedVolumeRefusedMinor3(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	mount := filepath.Join(t.TempDir(), "external")
	homeTrash := filepath.Join(t.TempDir(), "home", ".Trash")
	video := p010Video(t, filepath.Join(mount, "movies", "a.mp4"), "content-a")
	p010WithSystemTrash(t, func(path string) (string, error) {
		if err := os.MkdirAll(homeTrash, 0o755); err != nil {
			return "", err
		}
		target := filepath.Join(homeTrash, filepath.Base(path))
		return target, os.Rename(path, target)
	})
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	reviewAUnmountVolume(t, mount)
	result, err := NewTrashCenter(svc, nil).RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Items[0].Code != TrashResultVolumeOffline {
		t.Fatalf("原路径所在卷未挂载时恢复应返回 volume_offline: %#v err=%v", result, err)
	}
	if _, err := os.Stat(entry.TrashPath); err != nil {
		t.Fatalf("废纸篓里的文件应原地保留: %v", err)
	}
	if _, err := os.Stat(video.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("不得往未挂载的位置恢复: %v", err)
	}
	if got := p010VideoEntry(t, video.ID); got.State != trashStateDeleted {
		t.Fatalf("条目状态不变: %#v", got)
	}
}

// ---------- I-3：C1 旧版 trash 启发式只认旧版特征 ----------

func reviewASetDeletedAt(t *testing.T, model interface{}, id uint, at time.Time) {
	t.Helper()
	if err := database.DB.Unscoped().Model(model).Where("id = ?", id).Update("deleted_at", at).Error; err != nil {
		t.Fatal(err)
	}
}

// 反例：同级目录只有新版删除（有条目）与扫描器软删（时间不在旧版时间段内）时，用户自己的 trash/
// 照常收录，删除也照常移入废纸篓，不降级为只删记录。
func TestLIB14UserTrashDirNotSkippedByNewDeletionsOrScannerSoftDeletesI3(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	mediaDir := filepath.Join(root, "movies")
	// 新版删除：有 trash 条目。文件名故意与用户 trash/ 里的文件相同。
	deleted := p010Video(t, filepath.Join(mediaDir, "keep.mp4"), "old-keep")
	if err := svc.DeleteVideo(deleted.ID, true); err != nil {
		t.Fatal(err)
	}
	// 旧版时间段内的软删行，但有条目（record_only）：不是旧版「无条目」形态。
	withEntry := p010Video(t, filepath.Join(mediaDir, "other.mp4"), "other")
	if err := svc.DeleteVideo(withEntry.ID, false); err != nil {
		t.Fatal(err)
	}
	reviewASetDeletedAt(t, &models.Video{}, withEntry.ID, p010LegacyEraDeletedAt)
	// 扫描器软删、没有条目（图片扫描的做法），时间是现在：不在旧版时间段内。
	scanned := imageTrashTestCreateImage(t, filepath.Join(mediaDir, "keep.jpg"), "img")
	if err := NewImageService().deleteMissingImageRecord(scanned.ID); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(mediaDir, "trash", "keep.mp4")
	otherFile := filepath.Join(mediaDir, "trash", "other.mp4")
	createOldVideoFile(t, userFile)
	createOldVideoFile(t, otherFile)

	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if result.SkipBreakdown.LegacyTrash != 0 {
		t.Fatalf("没有旧版特征时不应计入 legacy_trash: %+v", result.SkipBreakdown)
	}
	added := p010ActiveVideos(t, userFile)
	if len(added) != 1 || len(p010ActiveVideos(t, otherFile)) != 1 {
		t.Fatalf("用户自己的 trash/ 应被正常收录: %+v", result)
	}
	if isTrashPath(userFile) || isTrashDir(filepath.Dir(userFile)) {
		t.Fatal("用户的 trash/ 不应被判为回收站")
	}
	// 删除照常移入废纸篓（mode=trash），而不是降级为只删记录。
	if code, err := svc.deleteVideoBatchItem(added[0].ID, true, "user", newDeleteBatchID()); err != nil || code != TrashResultOK {
		t.Fatalf("删除应成功: code=%s err=%v", code, err)
	}
	if got := p010VideoEntry(t, added[0].ID); got.Mode != models.TrashModeTrash || !got.FileMoved {
		t.Fatalf("用户 trash/ 里的文件应移入废纸篓: %#v", got)
	}
}

// 真实旧版形态仍被扫描/监听跳过，但只用于跳过：删除不降级、MoveToTrash 不原样放行。
func TestLIB14RealLegacyTrashFormSkippedButDeletionNotDegradedI3(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	mediaDir := filepath.Join(root, "movies")
	p010SoftDeletedVideoIn(t, mediaDir, "film.mkv")
	legacyFile := filepath.Join(mediaDir, "trash", "film_20260520103000_2.mkv")
	createOldVideoFile(t, legacyFile)
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("video_extensions", ".mp4,.mkv").Error; err != nil {
		t.Fatal(err)
	}

	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if result.SkipBreakdown.LegacyTrash != 1 || len(p010ActiveVideos(t, legacyFile)) != 0 {
		t.Fatalf("旧版形态应被跳过并计入 legacy_trash: %+v", result)
	}
	if !isTrashPath(legacyFile) || isRecordedTrashPath(legacyFile) {
		t.Fatal("启发式目录只用于扫描跳过，不算已登记的回收站位置")
	}
	// 该目录里一个仍在库的文件（例如旧版之前收录的）被用户删除：照常移入废纸篓。
	inLibrary := p010Video(t, filepath.Join(mediaDir, "trash", "kept.mp4"), "kept")
	if code, err := svc.deleteVideoBatchItem(inLibrary.ID, true, "user", newDeleteBatchID()); err != nil || code != TrashResultOK {
		t.Fatalf("删除应成功: code=%s err=%v", code, err)
	}
	if got := p010VideoEntry(t, inLibrary.ID); got.Mode != models.TrashModeTrash || !got.FileMoved {
		t.Fatalf("启发式目录里的文件删除不得降级为只删记录: %#v", got)
	}
	moved, err := NewTrashService().MoveToTrash(legacyFile)
	if err != nil || moved == legacyFile {
		t.Fatalf("MoveToTrash 不得对启发式目录原样放行: moved=%s err=%v", moved, err)
	}
}

func TestLIB14LegacyHeuristicRequiresNameMatchAndDateRangeI3(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	cases := []struct {
		name      string
		deletedAt time.Time
		trashFile string
		want      bool
	}{
		{"时间段内 名字吻合", p010LegacyEraDeletedAt, "a.mp4", true},
		{"时间段内 带时间戳", p010LegacyEraDeletedAt, "a_20260601120000.mp4", true},
		{"末日当天 23 点", time.Date(2026, 7, 30, 23, 0, 0, 0, time.Local), "a.mp4", true},
		{"首日零点", time.Date(2026, 4, 16, 0, 0, 0, 0, time.Local), "a.mp4", true},
		{"次日零点 超出", time.Date(2026, 7, 31, 0, 0, 0, 0, time.Local), "a.mp4", false},
		{"首日前一秒 超出", time.Date(2026, 4, 15, 23, 59, 59, 0, time.Local), "a.mp4", false},
		{"时间段内 名字对不上", p010LegacyEraDeletedAt, "unrelated.mp4", false},
		{"时间段内 时间戳位数不对", p010LegacyEraDeletedAt, "a_2026060112.mp4", false},
	}
	for index, tc := range cases {
		dir := filepath.Join(root, fmt.Sprintf("case-%d", index))
		video := models.Video{Name: "a.mp4", Path: filepath.Join(dir, "a.mp4"), Directory: dir, Size: 1}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Delete(&video).Error; err != nil {
			t.Fatal(err)
		}
		reviewASetDeletedAt(t, &models.Video{}, video.ID, tc.deletedAt)
		createOldVideoFile(t, filepath.Join(dir, "trash", tc.trashFile))
	}
	refreshLegacyTrashDirs()
	for index, tc := range cases {
		trashDir := filepath.Join(root, fmt.Sprintf("case-%d", index), "trash")
		if got := isUnrecordedLegacyTrashDir(trashDir); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestLIB14LegacyTrashNameMatchesOldTrashTargetPathI3(t *testing.T) {
	cases := []struct {
		file, base string
		want       bool
	}{
		{"movie.mp4", "movie.mp4", true},
		{"movie_20260501101010.mp4", "movie.mp4", true},
		{"movie_20260501101010_3.mp4", "movie.mp4", true},
		{"movie_20260501101010_.mp4", "movie.mp4", false},
		{"movie_2026050110101.mp4", "movie.mp4", false},
		{"movie_20260501101010.mkv", "movie.mp4", false},
		{"movie-20260501101010.mp4", "movie.mp4", false},
		{"other.mp4", "movie.mp4", false},
		{"noext_20260501101010", "noext", true},
	}
	for _, tc := range cases {
		if got := legacyTrashNameMatches(tc.file, tc.base); got != tc.want {
			t.Errorf("%s vs %s: got %v want %v", tc.file, tc.base, got, tc.want)
		}
	}
}

// ---------- Minor 1：列表不拿全局写锁 ----------

func TestLIB05TrashListDoesNotTakePathWriteLockMinor1(t *testing.T) {
	svc, _, entry, _ := p010PutBackFixture(t)
	center := NewTrashCenter(svc, nil)
	// 模拟一次长时间的扫描持有路径读锁：列表若同步拿写锁就会卡住。
	libraryPathMutationMu.RLock()
	released := false
	release := func() {
		if !released {
			released = true
			libraryPathMutationMu.RUnlock()
		}
	}
	defer release()
	done := make(chan *TrashPage, 1)
	go func() {
		page, _ := center.ListTrashEntries(TrashFilter{Kind: "video"})
		done <- page
	}()
	select {
	case page := <-done:
		if page == nil || len(page.Items) != 1 || page.Items[0].ID != entry.ID || !page.Items[0].PutBack {
			t.Fatalf("列表应返回可恢复的条目: %#v", page)
		}
	case <-time.After(3 * time.Second):
		release()
		t.Fatal("扫描持有路径读锁时列表不应阻塞")
	}
}

// ---------- Minor 2：放回判定统一、遍历全部软删行、手动添加也恢复 ----------

func TestLIB05PutBackSameSizeMtimeDifferentInodeIsNotRestoredMinor2(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	path := filepath.Join(root, "same.mp4")
	video := p010Video(t, path, "same-content")
	mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	// 用户清空了废纸篓，又在原位置写了一个内容、大小、mtime 都相同的新文件：inode 不同，是另一个文件。
	if err := os.Remove(entry.TrashPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("same-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Unix(0, entry.FileModTime), time.Unix(0, entry.FileModTime)); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if stableFileIdentity(info) == "" || identityInode(stableFileIdentity(info)) == identityInode(entry.FileIdentity) {
		t.Skip("当前文件系统复用了 inode，无法构造该场景")
	}
	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if result.Added != 1 || result.Restored != 0 {
		t.Fatalf("大小与 mtime 相同但 inode 不同不得恢复原记录，应新建: %+v", result)
	}
	if active := p010ActiveVideos(t, path); len(active) != 1 || active[0].ID == video.ID {
		t.Fatalf("应是新记录: %#v", active)
	}
	page, err := NewTrashCenter(svc, nil).ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 1 || page.Items[0].PutBack {
		t.Fatalf("列表也不应把它判为放回: %#v err=%v", page, err)
	}
}

func TestLIB05PutBackPicksIdentityMatchingRowAmongSeveralMinor2(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	path := filepath.Join(root, "movie.mp4")
	first := p010Video(t, path, "first-version")
	if err := svc.DeleteVideo(first.ID, true); err != nil {
		t.Fatal(err)
	}
	firstEntry := p010VideoEntry(t, first.ID)
	// 同路径出现第二个文件，入库后也被删进废纸篓（替身会改名为 movie 2.mp4）。
	if err := os.WriteFile(path, []byte("second-version-longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := svc.AddVideo(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVideo(second.ID, true); err != nil {
		t.Fatal(err)
	}
	// 用户把第一个文件从废纸篓放回原处：最新的软删行是 second，身份一致的是 first。
	if err := os.Rename(firstEntry.TrashPath, path); err != nil {
		t.Fatal(err)
	}
	restored, err := svc.AddVideo(path)
	if err != nil || restored == nil || restored.ID != first.ID {
		t.Fatalf("手动添加应恢复身份一致的那条原记录: %#v err=%v", restored, err)
	}
	if active := p010ActiveVideos(t, path); len(active) != 1 || active[0].ID != first.ID {
		t.Fatalf("同路径只应有原记录: %#v", active)
	}
	if got := p010VideoEntry(t, second.ID); got.State != trashStateDeleted {
		t.Fatalf("另一条删除记录不受影响: %#v", got)
	}
}

func TestLIB05PutBackDetectedRequiresTrashCopyGoneOrSameFileMinor2(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "a.mp4")
	if err := os.WriteFile(original, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(original)
	identity := stableFileIdentity(info)
	if identity == "" {
		t.Skip("当前平台没有稳定文件身份")
	}
	facts := softDeletedEntryFacts{Mode: models.TrashModeTrash, State: trashStateDeleted, FileSize: info.Size(),
		FileModTime: info.ModTime().UnixNano(), FileIdentity: identity, TrashPath: filepath.Join(dir, ".Trash", "a.mp4")}
	if !putBackDetectedFor(facts, info) {
		t.Fatal("废纸篓那一份已不在：应认定为放回")
	}
	// 废纸篓路径上是另一个文件（不同 inode）：不认定。
	if err := os.MkdirAll(filepath.Dir(facts.TrashPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(facts.TrashPath, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if putBackDetectedFor(facts, info) {
		t.Fatal("废纸篓里还有另一个文件时不得认定为放回")
	}
	// 废纸篓路径与原路径是同一个文件（硬链接）：认定。
	if err := os.Remove(facts.TrashPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, facts.TrashPath); err != nil {
		t.Skip("无法创建硬链接")
	}
	if !putBackDetectedFor(facts, info) {
		t.Fatal("同一 inode 的两个名字应认定为放回")
	}
	for _, mode := range []string{models.TrashModeLegacyTrash, models.TrashModeRecordOnly, models.TrashModeMissing} {
		other := facts
		other.Mode = mode
		if putBackDetectedFor(other, info) {
			t.Fatalf("mode=%s 不做放回判定", mode)
		}
	}
}

// ---------- I5：软删但没有条目的记录不能被永久删除 ----------

func TestLIB04PermanentDeleteRejectsSoftDeletedWithoutEntryI5(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video := p010Video(t, filepath.Join(t.TempDir(), "historic.mp4"), "historic")
	// 历史软删行：没有回收站条目，文件还在原处。
	if err := database.DB.Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	result := svc.PermanentlyDeleteVideos([]uint{video.ID})
	if result.Succeeded != 0 || result.Items[0].Code != TrashResultNotPurgeable {
		t.Fatalf("软删无条目的记录应拒绝永久删除: %#v", result)
	}
	if _, err := os.Stat(video.Path); err != nil {
		t.Fatalf("文件必须保留: %v", err)
	}
	var count int64
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", video.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("记录必须保留: %d err=%v", count, err)
	}

	setupImageServiceTestDB(t)
	image := imageTrashTestCreateImage(t, filepath.Join(t.TempDir(), "historic.jpg"), "historic")
	if err := database.DB.Delete(image).Error; err != nil {
		t.Fatal(err)
	}
	imageResult := NewImageService().PermanentlyDeleteImages([]uint{image.ID})
	if imageResult.Succeeded != 0 || imageResult.Items[0].Code != TrashResultNotPurgeable {
		t.Fatalf("软删无条目的图片应拒绝永久删除: %#v", imageResult)
	}
	if _, err := os.Stat(image.Path); err != nil {
		t.Fatalf("图片文件必须保留: %v", err)
	}
}

// ---------- Minor 4：路径改写覆盖图片目录与图片黑名单，并持有图片侧锁 ----------

func TestRewriteLibraryPathPrefixCoversImageDirectoriesAndExclusionsLIB06Minor4(t *testing.T) {
	setupVideoServiceTestDB(t)
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(filepath.Dir(oldRoot), "new")
	active := models.ImageDirectory{Path: filepath.Join(oldRoot, "photos")}
	removed := models.ImageDirectory{Path: filepath.Join(oldRoot, "archive")}
	sibling := models.ImageDirectory{Path: oldRoot + "-sibling"}
	for _, dir := range []*models.ImageDirectory{&active, &removed, &sibling} {
		if err := database.DB.Create(dir).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Delete(&removed).Error; err != nil {
		t.Fatal(err)
	}
	exclusions := strings.Join([]string{filepath.Join(oldRoot, "photos", "private"), "/elsewhere/skip"}, "\n")
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("image_scan_exclude_paths", exclusions).Error; err != nil {
		t.Fatal(err)
	}
	var counts PathRewriteCounts
	if err := database.Transaction(func(tx *gorm.DB) error {
		var err error
		counts, err = rewriteLibraryPathPrefixTx(tx, oldRoot, newRoot)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if counts.ImageDirectories != 2 || counts.ImageScanExclusions != 1 {
		t.Fatalf("计数不符: %+v", counts)
	}
	var gotActive, gotRemoved, gotSibling models.ImageDirectory
	database.DB.First(&gotActive, active.ID)
	database.DB.Unscoped().First(&gotRemoved, removed.ID)
	database.DB.First(&gotSibling, sibling.ID)
	if gotActive.Path != filepath.Join(newRoot, "photos") || gotRemoved.Path != filepath.Join(newRoot, "archive") || gotSibling.Path != sibling.Path {
		t.Fatalf("图片扫描目录改写不符: %q %q %q", gotActive.Path, gotRemoved.Path, gotSibling.Path)
	}
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	got := parseScanExcludePaths(settings.ImageScanExcludePaths)
	if len(got) != 2 || !strings.Contains(settings.ImageScanExcludePaths, filepath.Join(newRoot, "photos", "private")) ||
		!strings.Contains(settings.ImageScanExcludePaths, "/elsewhere/skip") {
		t.Fatalf("图片黑名单改写不符: %q", settings.ImageScanExcludePaths)
	}
}

func TestRenameDirectoryWaitsForImagePathLockLIB06Minor4(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	source := filepath.Join(root, "Show")
	p010Video(t, filepath.Join(source, "e01.mp4"), "episode")
	imagePathMutationMu.RLock()
	held := true
	release := func() {
		if held {
			held = false
			imagePathMutationMu.RUnlock()
		}
	}
	defer release()
	done := make(chan error, 1)
	go func() {
		_, err := (&VideoService{}).RenameDirectory(source, "Show Renamed")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("图片路径锁被占用时改名不应开始: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("释放后改名应完成: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("释放图片锁后改名应继续")
	}
}

// ---------- Minor 5：重定位遍历中途取消、Stop 与 Add 不并发 ----------

// countdownContext 在 Err() 被调用 n 次之后报告已取消，用来确定性地在遍历中途取消。
type countdownContext struct {
	context.Context
	remaining atomic.Int32
}

func (c *countdownContext) Err() error {
	if c.remaining.Add(-1) < 0 {
		return context.Canceled
	}
	return nil
}

func TestPlaybackRelocationWalkCancelsMidTraversalLIB13Minor5(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	createDirectoryRow(t, root)
	for i := 0; i < 40; i++ {
		createOldVideoFile(t, filepath.Join(root, fmt.Sprintf("d%02d", i), "filler.mp4"))
	}
	// 唯一候选在遍历顺序的最后。
	createOldVideoFile(t, filepath.Join(root, "zz", "target.mp4"))
	video := models.Video{Name: "target.mp4", Path: filepath.Join(root, "target.mp4"), Directory: root, Size: 1}

	full, ambiguous, err := svc.findRelocatedVideoCandidate(context.Background(), &video)
	if err != nil || ambiguous || full == "" {
		t.Fatalf("不取消时应找到唯一候选: %q ambiguous=%v err=%v", full, ambiguous, err)
	}
	ctx := &countdownContext{Context: context.Background()}
	ctx.remaining.Store(5)
	found, _, err := svc.findRelocatedVideoCandidate(ctx, &video)
	if !errors.Is(err, context.Canceled) || found != "" {
		t.Fatalf("遍历中途取消应立即返回取消错误: found=%q err=%v", found, err)
	}
}

func TestStopPlaybackRelocationSerializesWithNewRelocationsLIB13Minor5(t *testing.T) {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if ctx, ok := beginPlaybackRelocation(); ok {
					go func() {
						defer playbackRelocateWG.Done()
						select {
						case <-ctx.Done():
						case <-time.After(time.Millisecond):
						}
					}()
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		StopPlaybackRelocation()
	}
	close(stop)
	wg.Wait()
	StopPlaybackRelocation()
	if _, ok := beginPlaybackRelocation(); !ok {
		t.Fatal("Stop 返回后应允许新的重定位")
	}
	playbackRelocateWG.Done()
}

// ---------- Minor 6：MarkRootOffline 与窄对账串行，并复查根状态 ----------

func TestMarkRootOfflineSerializedWithNarrowReconcileLIB07Minor6(t *testing.T) {
	setupVideoServiceTestDB(t)
	mount := filepath.Join(t.TempDir(), "external")
	createDirectoryRow(t, mount)
	video := models.Video{Name: "a.mp4", Path: filepath.Join(mount, "a.mp4"), Directory: mount, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	reviewAUnmountVolume(t, mount)
	svc := &VideoService{}
	svc.scanSyncMu.Lock()
	locked := true
	unlock := func() {
		if locked {
			locked = false
			svc.scanSyncMu.Unlock()
		}
	}
	defer unlock()
	done := make(chan int64, 1)
	go func() {
		marked, _ := svc.MarkRootOffline(mount)
		done <- marked
	}()
	select {
	case <-done:
		t.Fatal("窄对账进行中时离线标记必须等待")
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case marked := <-done:
		if marked != 1 {
			t.Fatalf("对账结束后应完成标记: %d", marked)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("释放后离线标记应继续")
	}
}

func TestMarkRootOfflineRevertsWhenRootReturnsDuringMarkingLIB07Minor6(t *testing.T) {
	setupVideoServiceTestDB(t)
	mount := filepath.Join(t.TempDir(), "external")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	createDirectoryRow(t, mount)
	video := models.Video{Name: "a.mp4", Path: filepath.Join(mount, "a.mp4"), Directory: mount, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	remount := reviewAUnmountVolume(t, mount)
	// 根在标记写库的那一刻回来了。
	var flipped atomic.Bool
	name := "review_a:remount_during_mark"
	if err := database.DB.Callback().Update().After("gorm:update").Register(name, func(db *gorm.DB) {
		if db.Statement.Schema != nil && db.Statement.Schema.Table == "videos" && flipped.CompareAndSwap(false, true) {
			remount()
		}
	}); err != nil {
		t.Fatal(err)
	}
	marked, err := (&VideoService{}).MarkRootOffline(mount)
	if err != nil || marked != 0 || !flipped.Load() {
		t.Fatalf("标记期间根回来应撤销标记: marked=%d flipped=%v err=%v", marked, flipped.Load(), err)
	}
	if got := p011ReloadVideo(t, video.ID); got.IsStale || got.StaleReason != "" {
		t.Fatalf("根在线时不得留下 offline_root: %+v", got)
	}
}

// ---------- Minor 7：legacy_trash 计数遵守隐藏/黑名单规则且不与 existing 重复 ----------

func TestLegacyTrashCountObeysHiddenBlacklistAndExistingLIB14Minor7(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	mediaDir := filepath.Join(root, "movies")
	legacyDir := filepath.Join(mediaDir, "trash")
	p010SoftDeletedVideoIn(t, mediaDir, "old.mp4")
	createOldVideoFile(t, filepath.Join(legacyDir, "old.mp4"))           // 计入
	createOldVideoFile(t, filepath.Join(legacyDir, ".hidden", "b.mp4"))  // 隐藏目录：不计
	createOldVideoFile(t, filepath.Join(legacyDir, ".c.mp4"))            // 隐藏文件：不计
	createOldVideoFile(t, filepath.Join(legacyDir, "excluded", "d.mp4")) // 黑名单：不计
	inLibrary := filepath.Join(legacyDir, "e.mp4")                       // 在库：按 existing 计
	createOldVideoFile(t, inLibrary)
	if err := database.DB.Create(&models.Video{Name: "e.mp4", Path: inLibrary, Directory: legacyDir, Size: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").
		Update("scan_exclude_paths", filepath.Join(legacyDir, "excluded")).Error; err != nil {
		t.Fatal(err)
	}
	result := (&VideoService{}).SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if result.SkipBreakdown.LegacyTrash != 1 || result.SkipBreakdown.Existing != 1 {
		t.Fatalf("legacy_trash 只计可见文件、在库文件只计 existing: %+v", result.SkipBreakdown)
	}
	if result.Skipped != result.SkipBreakdown.Total() {
		t.Fatalf("skipped 必须等于各项之和: %+v", result)
	}
}

// ---------- Minor 8：分页游标 ----------

func TestLIB05TrashPageCursorNeverZeroWhenMoreRemainMinor8(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	ids := make([]uint, 0, 3)
	for i := 0; i < 3; i++ {
		path := filepath.Join(root, fmt.Sprintf("p%d.mp4", i))
		video := p010Video(t, path, fmt.Sprintf("put-back-%d", i))
		mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
		if err := svc.DeleteVideo(video.ID, true); err != nil {
			t.Fatal(err)
		}
		entry := p010VideoEntry(t, video.ID)
		// 全部被放回原处：以前列表会就地恢复并跳过，当页一项不剩。
		if err := os.Rename(entry.TrashPath, path); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, entry.ID)
	}
	center := NewTrashCenter(svc, nil)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video", Limit: 2})
	if err != nil || !page.HasMore || page.NextCursor == 0 || len(page.Items) != 2 {
		t.Fatalf("还有更多时游标不能为 0: %#v err=%v", page, err)
	}
	next, err := center.ListTrashEntries(TrashFilter{Kind: "video", Limit: 2, CursorID: page.NextCursor})
	if err != nil || next.HasMore || len(next.Items) != 1 || next.Items[0].ID != ids[0] {
		t.Fatalf("第二页应是剩下的一条: %#v err=%v", next, err)
	}
}

// ---------- Minor 10：PIN 加固 ----------

func TestShortFeedPINMinimumSixCharactersPLAY01Minor10(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewShortFeedService(&VideoService{})
	for _, pin := range []string{"1234", "12345", "abcde"} {
		if err := svc.SetShortFeedPIN(pin); !errors.Is(err, ErrShortFeedPINInvalid) {
			t.Fatalf("%q 少于 6 位应被拒绝: %v", pin, err)
		}
	}
	for _, pin := range []string{"123456", "字字字字字字", strings.Repeat("9", 32)} {
		if err := svc.SetShortFeedPIN(pin); err != nil {
			t.Fatalf("%q 应合法: %v", pin, err)
		}
	}
	if !strings.Contains(ErrShortFeedPINInvalid.Error(), "6 到 32") {
		t.Fatalf("错误文案应说明 6 到 32 个字符: %q", ErrShortFeedPINInvalid.Error())
	}
}

func TestShortFeedDailyFailureCapLocksNewLoginsUntilPINResetPLAY01Minor10(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	if err := svc.SetShortFeedPIN("246800"); err != nil {
		t.Fatal(err)
	}
	session := loginShortFeed(t, handler, "246800", "192.168.1.9:1")
	auth := svc.authState()
	base := time.Now()
	// 200 次失败（换来源、4 秒一次，避开每来源与每分钟的限制）：还没超过上限。
	for i := 0; i < shortFeedDailyFailureLimit; i++ {
		at := base.Add(time.Duration(i) * 4 * time.Second)
		key := fmt.Sprintf("10.0.%d.%d", i/200, i%200)
		if _, ok := auth.beginAttempt(key, at); !ok {
			t.Fatalf("第 %d 次尝试不应被拦下", i+1)
		}
		if locked, retry := auth.failedAttempt(key, at); locked && retry == shortFeedDailyLockedRetry {
			t.Fatalf("第 %d 次失败不应触发每日上限", i+1)
		}
	}
	last := base.Add(time.Duration(shortFeedDailyFailureLimit) * 4 * time.Second)
	if _, ok := auth.beginAttempt("10.9.9.9", last); !ok {
		t.Fatal("第 201 次尝试本身仍允许进入比对")
	}
	if locked, retry := auth.failedAttempt("10.9.9.9", last); !locked || retry != shortFeedDailyLockedRetry {
		t.Fatalf("超过 200 次失败应触发每日上限: locked=%v retry=%d", locked, retry)
	}
	// 之后的新登录一律 429（即使过了几个小时、换了来源、PIN 正确）。
	if retry, ok := auth.beginAttempt("10.8.8.8", last.Add(3*time.Hour)); ok || retry != shortFeedDailyLockedRetry {
		t.Fatalf("每日上限锁定后应拒绝新登录: retry=%d ok=%v", retry, ok)
	}
	rec := shortFeedLoginStatus(handler, "246800", "192.168.50.1:1", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("每日上限锁定后登录应 429，实际 %d", rec.Code)
	}
	payload := decodeShortFeedBody(t, rec)
	if payload["code"] != "pin_locked" || payload["reset_required"] != true {
		t.Fatalf("429 应带 pin_locked 与 reset_required: %v", payload)
	}
	// 已登录会话不受影响。
	dataReq := shortFeedRequest(http.MethodGet, "/short-api/status", "", "192.168.1.9:1")
	dataReq.AddCookie(session)
	dataRec := httptest.NewRecorder()
	handler.ServeHTTP(dataRec, dataReq)
	if dataRec.Code != http.StatusOK {
		t.Fatalf("已登录会话不应受每日上限影响，实际 %d", dataRec.Code)
	}
	// 桌面端修改 PIN：失败表、冷却与每日计数全部重置，新 PIN 可登录。
	if err := svc.SetShortFeedPIN("135700"); err != nil {
		t.Fatal(err)
	}
	if cookie := loginShortFeed(t, handler, "135700", "192.168.50.1:2"); cookie.Value == "" {
		t.Fatal("修改 PIN 后应能登录")
	}

	// 清除 PIN 同样重置。
	for i := 0; i <= shortFeedDailyFailureLimit; i++ {
		at := last.Add(time.Duration(i) * 4 * time.Second)
		key := fmt.Sprintf("10.1.%d.%d", i/200, i%200)
		auth.beginAttempt(key, at)
		auth.failedAttempt(key, at)
	}
	if _, ok := auth.beginAttempt("10.7.7.7", last.Add(time.Hour)); ok {
		t.Fatal("应再次触发每日上限")
	}
	if err := svc.ClearShortFeedPIN(); err != nil {
		t.Fatal(err)
	}
	if _, ok := auth.beginAttempt("10.7.7.7", last.Add(time.Hour)); !ok {
		t.Fatal("清除 PIN 后每日上限应重置")
	}
}

// ---------- Minor 11 / 12：手机端删除映射、访问文案 ----------

func TestShortFeedDeleteMapsOfflineAndPermissionTo409PLAY01Minor11(t *testing.T) {
	cases := []struct {
		err     error
		code    string
		message string
	}{
		{ErrTrashVolumeOffline, "volume_offline", "文件所在磁盘当前未连接，未做任何改动"},
		{ErrTrashPermissionDenied, "permission_denied", "没有权限把文件移到废纸篓，请在桌面端处理"},
		{fmt.Errorf("继续上次删除失败: %w", ErrTrashVolumeOffline), "volume_offline", "文件所在磁盘当前未连接，未做任何改动"},
	}
	for _, tc := range cases {
		svc, handler, _ := newShortFeedAccessFixture(t)
		deleteErr := tc.err
		svc.deleteVideoFn = func(uint, bool) error { return deleteErr }
		video := createShortFeedVideo(t, t.TempDir(), "offline.mp4", 30, false)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, shortFeedRequest(http.MethodPost, "/short-api/items/video/"+strconvUint(video.ID)+"/delete", `{"confirm_move_to_trash":true}`, "127.0.0.1:5000"))
		if rec.Code != http.StatusConflict {
			t.Fatalf("%v 应返回 409，实际 %d body=%s", tc.err, rec.Code, rec.Body.String())
		}
		payload := decodeShortFeedBody(t, rec)
		if payload["code"] != tc.code || payload["message"] != tc.message {
			t.Fatalf("409 响应不符: %v", payload)
		}
	}
}

func TestShortFeedAllowedAccessTextUnavailableOnReadErrorPLAY14Minor12(t *testing.T) {
	_, _, server := newShortFeedAccessFixture(t)
	if err := database.DB.Unscoped().Where("1 = 1").Delete(&models.Settings{}).Error; err != nil {
		t.Fatal(err)
	}
	got := server.Status().AllowedAccess
	if got != shortFeedAccessUnavailable || strings.Contains(got, "no PIN") {
		t.Fatalf("读不到 PIN 状态时应说明状态不可用，而不是没设 PIN: %q", got)
	}
}

func TestLIB05ConcurrentStateChangeReportsFileInTrashMinor12(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video := p010Video(t, filepath.Join(t.TempDir(), "race.mp4"), "race-content")
	var moved string
	// 替身：文件进废纸篓之后，并发操作把条目改成了别的中间状态（不是 deleted）。
	p010WithSystemTrash(t, func(path string) (string, error) {
		target, err := fakeSystemTrashMove(path)
		if err != nil {
			return "", err
		}
		moved = target
		if err := database.DB.Model(&models.VideoTrashEntry{}).Where("video_id = ?", video.ID).Update("state", trashStateRollback).Error; err != nil {
			t.Fatal(err)
		}
		return target, nil
	})
	err := svc.DeleteVideo(video.ID, true)
	if err == nil || !strings.Contains(err.Error(), "文件已移入废纸篓") || !strings.Contains(err.Error(), "待对账") {
		t.Fatalf("文案应如实说明文件已进废纸篓、数据库待对账: %v", err)
	}
	if _, statErr := os.Stat(moved); statErr != nil {
		t.Fatalf("文件应留在废纸篓里，不被回滚: %v", statErr)
	}
}
