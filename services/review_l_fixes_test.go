package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 修复 L（修复 I-1 + 修复 K 复审的 Minor）的回归测试。测试名里的 APPxx / LIBxx / IMGxx / PLAYxx 是问题清单 ID，
// FixL 后面是复审编号（M1…M7 = m1…m7）。系统废纸篓是替身（system_trash_testhook_test.go）：同卷重命名进「原目录/.Trash」。

// reviewLFailUpdatesOn 让 table 上经 GORM Update 回调的写入失败（与 reviewAFailDeletesOn 同构），返回提前解除的函数。
func reviewLFailUpdatesOn(t *testing.T, table string) func() {
	t.Helper()
	var armed atomic.Bool
	armed.Store(true)
	name := "review_l:fail_update_" + table
	if err := database.DB.Callback().Update().Before("gorm:update").Register(name, func(db *gorm.DB) {
		if armed.Load() && db.Statement.Schema != nil && db.Statement.Schema.Table == table {
			_ = db.AddError(errors.New("注入的恢复事务失败"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	disable := func() { armed.Store(false) }
	t.Cleanup(disable)
	return disable
}

// reviewLWaitPathLockFree 等全局路径锁在限时内完全空闲（没有持有者、没有排队的写者）：TryLock 成功即空闲，随即放掉。
// 被放弃的获取由辅助 goroutine 在拿到锁之后释放，是异步的，所以轮询等待。
func reviewLWaitPathLockFree(t *testing.T, message string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !libraryPathMutationMu.TryLock() {
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
		time.Sleep(time.Millisecond)
	}
	libraryPathMutationMu.Unlock()
}

// reviewLLockDirReadOnly 把目录改成只读（0555）：里面的文件读得到、删不掉。以 root 运行时权限位不起作用，跳过。
func reviewLLockDirReadOnly(t *testing.T, dir string) func() {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行时只读目录不能阻止删除")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	restored := false
	unlock := func() {
		if !restored {
			restored = true
			_ = os.Chmod(dir, 0o755)
		}
	}
	t.Cleanup(unlock)
	return unlock
}

// reviewLTombstoneVideoAt 在 path 上建一条「墓碑」的视频行：软删的视频记录 + state=removed 的 legacy_trash 条目
// （trash_path 在同目录的旧版 trash/ 里，文件不必存在）。模拟旧版删除之后被「移除记录」留下的墓碑。
func reviewLTombstoneVideoAt(t *testing.T, path string) (models.Video, models.VideoTrashEntry) {
	t.Helper()
	video := models.Video{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path), Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&video).Update("deleted_by", "user").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	entry := models.VideoTrashEntry{
		DeletedBy: "user", VideoID: video.ID, VideoName: video.Name, OriginalPath: path,
		TrashPath: filepath.Join(filepath.Dir(path), DefaultTrashDirName, "tomb-"+filepath.Base(path)),
		FileMoved: true, FileSize: 1, State: trashStateRemoved, Mode: models.TrashModeLegacyTrash,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	return video, entry
}

// reviewLTombstoneImageAt 是 reviewLTombstoneVideoAt 的图片版本。
func reviewLTombstoneImageAt(t *testing.T, path string) (models.Image, models.ImageTrashEntry) {
	t.Helper()
	image := models.Image{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path), Size: 1}
	if err := database.DB.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&image).Update("deleted_by", "user").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&image).Error; err != nil {
		t.Fatal(err)
	}
	entry := models.ImageTrashEntry{
		DeletedBy: "user", ImageID: image.ID, ImageName: image.Name, OriginalPath: path,
		TrashPath: filepath.Join(filepath.Dir(path), DefaultTrashDirName, "tomb-"+filepath.Base(path)),
		FileMoved: true, FileSize: 1, State: trashStateRemoved, Mode: models.TrashModeLegacyTrash,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	return image, entry
}

// reviewLFailDigest 让 legacy 放回的内容核对读哈希失败（EIO / EACCES 的替身），测试结束时还原。
func reviewLFailDigest(t *testing.T, cause error) {
	t.Helper()
	fn := func(path string) (string, error) {
		return "", &os.PathError{Op: "open", Path: path, Err: cause}
	}
	previous := legacyPutBackDigestFn.Swap(&fn)
	t.Cleanup(func() { legacyPutBackDigestFn.Store(previous) })
}

// ---------- m3：路径锁阻塞等待 + 维护开始通知，不轮询 ----------

func TestAPP02BatchWriterLetsReaderInBetweenItemsFixLM3(t *testing.T) {
	setupVideoServiceTestDB(t)
	const items = 20
	var finished atomic.Int32
	firstHeld := make(chan struct{})
	batchErr := make(chan error, 1)
	go func() {
		for index := 0; index < items; index++ {
			unlock, err := lockLibraryPaths()
			if err != nil {
				batchErr <- err
				return
			}
			if index == 0 {
				close(firstHeld)
			}
			time.Sleep(5 * time.Millisecond)
			unlock()
			finished.Add(1)
		}
		batchErr <- nil
	}()
	<-firstHeld
	readerAt := make(chan int32, 1)
	readerErr := make(chan error, 1)
	go func() {
		release, err := rLockLibraryPaths()
		if err != nil {
			readerErr <- err
			return
		}
		readerAt <- finished.Load()
		release()
	}()
	select {
	case at := <-readerAt:
		// 写者放锁时排着的读者先于下一个写者拿到锁：读者最迟在第二项之前进来，绝不会等到整批写完。
		if at >= items {
			t.Fatalf("批量写者逐项取写锁期间读者被饿死：第 %d 项之后才拿到读锁", at)
		}
	case err := <-readerErr:
		t.Fatalf("无围栏时读者不应失败: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("读者没有拿到读锁")
	}
	if err := <-batchErr; err != nil {
		t.Fatalf("无围栏时批量写者不应失败: %v", err)
	}
}

func TestAPP02WaitingReaderAndWriterReturnMaintenanceWhenFenceStartsFixLM3(t *testing.T) {
	setupVideoServiceTestDB(t)
	// 与 enterDatabaseRestoreMode 同序：维护入口先拿路径写锁，之后排上来的读者与写者都在等。
	pathRelease := BeginLibraryMaintenance()
	var fenceRelease func()
	t.Cleanup(func() {
		if fenceRelease != nil {
			fenceRelease()
		}
		if pathRelease != nil {
			pathRelease()
		}
	})
	readerErr := make(chan error, 1)
	writerErr := make(chan error, 1)
	go func() {
		release, err := rLockLibraryPaths()
		if err == nil {
			release()
		}
		readerErr <- err
	}()
	go func() {
		unlock, err := lockLibraryPaths()
		if err == nil {
			unlock()
		}
		writerErr <- err
	}()
	select {
	case err := <-readerErr:
		t.Fatalf("维护入口持有写锁、围栏未生效时读者应等待: %v", err)
	case err := <-writerErr:
		t.Fatalf("维护入口持有写锁、围栏未生效时写者应等待: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	// 立起围栏（进入「待重启」终态的那一刻）：等待中的读者与写者都在合理时间内返回 ErrMaintenance。
	fenceRelease = database.BeginMaintenance()
	for label, ch := range map[string]chan error{"读者": readerErr, "写者": writerErr} {
		select {
		case err := <-ch:
			if !errors.Is(err, database.ErrMaintenance) {
				t.Fatalf("维护开始后等待中的%s应返回 ErrMaintenance: %v", label, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("维护开始后等待中的%s没有返回", label)
		}
	}
	// 维护入口仍独占写锁：放弃的获取没有抢到、也没有持有任何锁。
	if libraryPathMutationMu.TryRLock() {
		libraryPathMutationMu.RUnlock()
		t.Fatal("维护期间写锁必须仍由维护入口持有")
	}

	// 离开维护（恢复 / 切换失败）：放弃的两次获取随后拿到锁，立即释放，锁回到空闲。
	fenceRelease()
	fenceRelease = nil
	pathRelease()
	pathRelease = nil
	reviewLWaitPathLockFree(t, "被放弃的获取拿到锁之后必须立即释放（不得泄漏或持有路径锁）")
	unlock, err := lockLibraryPaths()
	if err != nil {
		t.Fatalf("撤掉维护之后写锁入口应恢复正常: %v", err)
	}
	unlock()
	release, err := rLockLibraryPaths()
	if err != nil {
		t.Fatalf("撤掉维护之后读锁入口应恢复正常: %v", err)
	}
	release()
}

// m3 / m7：拿到锁之后复查围栏（新实现下的等价路径）。「维护开始」通知换成永不到达的替身，等待中的一方只能在拿到锁之后
// 发现围栏已生效：必须放锁并返回 ErrMaintenance，不能带着锁返回成功。
func TestAPP02PathLockRechecksFenceAfterAcquireFixLM3(t *testing.T) {
	setupVideoServiceTestDB(t)
	never := make(chan struct{})
	neverStarted := func() <-chan struct{} { return never }
	libraryMaintenanceStartedFn.Store(&neverStarted)
	t.Cleanup(func() { libraryMaintenanceStartedFn.Store(nil) })

	cases := []struct {
		name    string
		hold    func()
		release func()
		acquire func() (func(), error)
	}{
		{"读锁", libraryPathMutationMu.Lock, libraryPathMutationMu.Unlock, rLockLibraryPaths},
		{"写锁", libraryPathMutationMu.RLock, libraryPathMutationMu.RUnlock, lockLibraryPaths},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.hold()
			held := true
			var fenceRelease func()
			t.Cleanup(func() {
				if fenceRelease != nil {
					fenceRelease()
				}
				if held {
					tc.release()
				}
			})
			done := make(chan error, 1)
			go func() {
				unlock, err := tc.acquire()
				if err == nil {
					unlock()
				}
				done <- err
			}()
			select {
			case err := <-done:
				t.Fatalf("锁被占用时应等待: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			fenceRelease = database.BeginMaintenance()
			select {
			case err := <-done:
				t.Fatalf("通知不到达时，等待中的一方只能在拿到锁之后发现围栏: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			tc.release()
			held = false
			select {
			case err := <-done:
				if !errors.Is(err, database.ErrMaintenance) {
					t.Fatalf("拿到锁时围栏已生效，应放锁并返回 ErrMaintenance: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("拿到锁之后没有返回")
			}
			// 复查失败时调用方自己同步放锁：此刻锁已空闲。
			if !libraryPathMutationMu.TryLock() {
				t.Fatal("复查围栏失败后必须放掉拿到的锁")
			}
			libraryPathMutationMu.Unlock()
			fenceRelease()
			fenceRelease = nil
		})
	}
}

// ---------- m1：legacy 放回的哈希核对三态，读不出为「无法判定」 ----------

func TestLIB05LegacyPutBackUnreadableHashNeitherRestoresNorCreatesFixLM1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video, entry := reviewILegacyPutBackWithSHA(t, root, false)
	reviewLFailDigest(t, syscall.EIO)

	scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if scan.Restored != 0 || scan.Added != 0 {
		t.Fatalf("哈希读不出时扫描既不恢复也不新建: %+v", scan)
	}
	if len(scan.Errors) != 1 || scan.SkipBreakdown.ReadError != 1 || !strings.Contains(scan.Errors[0].Error, "无法读取原位置上的文件核对内容") {
		t.Fatalf("放回恢复应计入读取错误: %+v", scan)
	}
	if strings.Contains(scan.Errors[0].Error, root) {
		t.Fatalf("错误文案不得带路径: %q", scan.Errors[0].Error)
	}
	if active := reviewIActiveUnder(t, root); len(active) != 0 {
		t.Fatalf("不得新建记录: %#v", active)
	}
	if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() {
		t.Fatalf("原记录不得被恢复: %#v", got)
	}
	if got := p010VideoEntry(t, video.ID); got.State != trashStateDeleted {
		t.Fatalf("条目保持 deleted: %#v", got)
	}
	// 手动添加同一条路：报错，不恢复也不新建。
	if _, err := svc.AddVideo(video.Path); !errors.Is(err, errLegacyPutBackUnreadable) {
		t.Fatalf("手动添加应报无法核对: %v", err)
	}
	// 同路径软删判定：返回跳过。
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatal(err)
	}
	row, skip, err := softDeletedVideoPathSkip(video.Path, info)
	if err != nil || !errors.Is(skip, ErrVideoExists) || !errors.Is(skip, errLegacyPutBackUnreadable) || row == nil || row.ID != video.ID {
		t.Fatalf("softDeletedVideoPathSkip 对无法判定应返回跳过: row=%#v skip=%v err=%v", row, skip, err)
	}
	if active := reviewIActiveUnder(t, root); len(active) != 0 {
		t.Fatalf("不得新建记录: %#v", active)
	}

	// 显式恢复保持拒绝：EIO 报 error，EACCES 报 permission_denied；文件、记录都不动。
	center := NewTrashCenter(svc, nil)
	result, err := center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Succeeded != 0 || result.Items[0].Code != TrashResultError || !strings.Contains(result.Items[0].Message, "无法读取") {
		t.Fatalf("哈希读不出时显式恢复应拒绝: %#v err=%v", result, err)
	}
	reviewLFailDigest(t, syscall.EACCES)
	result, err = center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Succeeded != 0 || result.Items[0].Code != TrashResultPermissionDenied {
		t.Fatalf("没权限读取时显式恢复应报 permission_denied: %#v err=%v", result, err)
	}
	if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() {
		t.Fatalf("记录保持软删: %#v", got)
	}
	if got := reviewGFileContent(t, video.Path); got != "original-bytes-1" {
		t.Fatalf("原路径上的文件不得被动: %q", got)
	}
	if got := p010VideoEntry(t, video.ID); got.State != trashStateDeleted {
		t.Fatalf("条目保持 deleted: %#v", got)
	}

	// 读得出之后照常：哈希一致即恢复原记录。
	legacyPutBackDigestFn.Store(nil)
	scan = svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if scan.Restored != 1 || scan.Added != 0 {
		t.Fatalf("读得出且一致时应恢复原记录: %+v", scan)
	}
}

func TestIMG02LegacyPutBackUnreadableHashNeitherRestoresNorCreatesFixLM1(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	image := imageTrashTestCreateImage(t, filepath.Join(root, "pics", "hashed.jpg"), "image-bytes-01")
	entry := reviewGLegacyImageEntry(t, image)
	sha, err := fileSHA256Hex(entry.TrashPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.ImageTrashEntry{}).Where("id = ?", entry.ID).Update("file_sha256", sha).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(entry.TrashPath, image.Path); err != nil {
		t.Fatal(err)
	}
	reviewLFailDigest(t, syscall.EIO)

	result := imageTestMustSync(t, svc)
	if result.Restored != 0 || result.Added != 0 || len(result.Errors) != 1 || result.Errors[0].Operation != "restore" {
		t.Fatalf("图片哈希读不出时既不恢复也不新建，放回恢复计入读取错误: %+v", result)
	}
	if n := reviewGCountRows(t, &models.Image{}, "path = ? AND deleted_at IS NULL", image.Path); n != 0 {
		t.Fatalf("不得新建图片记录: %d", n)
	}
	info, err := os.Stat(image.Path)
	if err != nil {
		t.Fatal(err)
	}
	row, skip, err := softDeletedImagePathSkip(image.Path, info)
	if err != nil || !errors.Is(skip, ErrImageExists) || row == nil || row.ID != image.ID {
		t.Fatalf("softDeletedImagePathSkip 对无法判定应返回跳过: row=%#v skip=%v err=%v", row, skip, err)
	}
	restored, err := NewTrashCenter(nil, svc).RestoreTrashEntries("image", []uint{entry.ID})
	if err != nil || restored.Succeeded != 0 || !strings.Contains(restored.Items[0].Message, "无法读取") {
		t.Fatalf("图片显式恢复应拒绝: %#v err=%v", restored, err)
	}
	if n := reviewGCountRows(t, &models.Image{}, "id = ? AND deleted_at IS NOT NULL", image.ID); n != 1 {
		t.Fatalf("图片记录保持软删: %d", n)
	}
}

// ---------- m2：restoring 行不再卡死 ----------

func TestLIB05OccupiedRestoringEntryWithFileNotAtOriginalRevertsToDeletedFixLM2(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	center := NewTrashCenter(svc, nil)
	dir := t.TempDir()
	// interrupted 恢复中断、文件还在废纸篓里，原位置随后被另一条活跃记录收录（另一个文件）。
	interrupted := func(name string) (models.Video, models.VideoTrashEntry, models.Video) {
		path := filepath.Join(dir, name)
		video := p010Video(t, path, "original-"+name)
		mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
		if err := svc.DeleteVideo(video.ID, true); err != nil {
			t.Fatal(err)
		}
		entry := p010VideoEntry(t, video.ID)
		reviewISetEntryState(t, &models.VideoTrashEntry{}, entry.ID, trashStateRestoring)
		occupant := p010Video(t, path, "new-"+name)
		return video, entry, occupant
	}
	viaReconcile, reconcileEntry, reconcileOccupant := interrupted("a.mp4")

	// 修复前：列表不给动作（restoring 且不是 claimed）。
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("列表失败: %#v err=%v", page, err)
	}
	if item := page.Items[0]; item.State != trashStateRestoring || item.ClaimedByActive || len(item.Actions) != 0 {
		t.Fatalf("前提：文件不在原处的 restoring 行不是 claimed、没有动作: %#v", item)
	}

	// 启动对账：报 path_occupied，同时把条目退回 deleted。
	if err := svc.ReconcileTrashEntries(); !errors.Is(err, ErrTrashPathOccupied) {
		t.Fatalf("启动对账应报 path_occupied: %v", err)
	}
	// 显式恢复（另一条，启动对账之后才中断）同样退回。
	viaRestore, restoreEntry, _ := interrupted("b.mp4")
	result, err := center.RestoreTrashEntries("video", []uint{restoreEntry.ID})
	if err != nil || result.Items[0].Code != TrashResultPathOccupied {
		t.Fatalf("显式恢复应报 path_occupied: %#v err=%v", result, err)
	}
	for _, video := range []models.Video{viaReconcile, viaRestore} {
		got := p010VideoEntry(t, video.ID)
		if got.State != trashStateDeleted || !strings.Contains(got.LastError, ErrTrashPathOccupied.Error()) {
			t.Fatalf("被占用、文件不在原处的 restoring 行应退回 deleted: %#v", got)
		}
		if reloaded := p011ReloadVideo(t, video.ID); !reloaded.DeletedAt.IsValid() {
			t.Fatalf("记录保持软删: %#v", reloaded)
		}
	}

	// 成为普通条目：列表给出恢复与清除，清除 / 仍然移除记录都可用。
	page, err = center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if fmt.Sprint(item.Actions) != fmt.Sprint([]string{TrashActionRestore, TrashActionPurge}) {
			t.Fatalf("退回之后应是普通条目: %#v", item)
		}
	}
	purged, err := center.PurgeTrashEntries("video", []uint{reconcileEntry.ID})
	if err != nil || purged.Succeeded != 1 {
		t.Fatalf("退回之后应能清除: %#v err=%v", purged, err)
	}
	if _, err := os.Lstat(reconcileEntry.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("清除应删掉废纸篓里的文件: %v", err)
	}
	if got := reviewGFileContent(t, reconcileOccupant.Path); got != "new-a.mp4" {
		t.Fatalf("原位置上的新文件不得被动: %q", got)
	}
	forced, err := center.ForceRemoveTrashRecords("video", []uint{restoreEntry.ID}, TrashForceRemoveConfirmText)
	if err != nil || forced.Succeeded != 1 {
		t.Fatalf("退回之后「仍然移除记录」应可用: %#v err=%v", forced, err)
	}
	if got := reviewGFileContent(t, restoreEntry.TrashPath); got != "original-b.mp4" {
		t.Fatalf("移除记录不得动文件: %q", got)
	}
}

func TestIMG02OccupiedRestoringEntryWithFileNotAtOriginalRevertsToDeletedFixLM2(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	image := imageTrashTestCreateImage(t, path, "original-photo")
	mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010ImageEntry(t, image.ID)
	reviewISetEntryState(t, &models.ImageTrashEntry{}, entry.ID, trashStateRestoring)
	occupant := imageTrashTestCreateImage(t, path, "new-photo")

	if err := svc.ReconcileImageTrashEntries(); !errors.Is(err, ErrTrashPathOccupied) {
		t.Fatalf("启动对账应报 path_occupied: %v", err)
	}
	if got := p010ImageEntry(t, image.ID); got.State != trashStateDeleted {
		t.Fatalf("被占用、文件不在原处的图片 restoring 行应退回 deleted: %#v", got)
	}
	center := NewTrashCenter(nil, svc)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "image"})
	if err != nil || len(page.Items) != 1 || fmt.Sprint(page.Items[0].Actions) != fmt.Sprint([]string{TrashActionRestore, TrashActionPurge}) {
		t.Fatalf("退回之后应是普通条目: %#v err=%v", page, err)
	}
	forced, err := center.ForceRemoveTrashRecords("image", []uint{entry.ID}, TrashForceRemoveConfirmText)
	if err != nil || forced.Succeeded != 1 {
		t.Fatalf("退回之后「仍然移除记录」应可用: %#v err=%v", forced, err)
	}
	if got := reviewGFileContent(t, occupant.Path); got != "new-photo" {
		t.Fatalf("原位置上的新文件不得被动: %q", got)
	}
}

func TestLIB05ScanSkipsScannerRowWithRestoringMissingEntryFixLM2(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	path := filepath.Join(root, "movie.mp4")
	video := p010Video(t, path, "scanner-content")
	mustSetFileModTime(t, path, time.Now().Add(-2*time.Hour))
	// 扫描器因文件缺失软删（missing），之后文件回来、自动恢复在中途中断，条目停在 restoring。
	if err := svc.deleteVideoRecordBy(video.ID, false, "scanner"); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	if entry.Mode != models.TrashModeMissing {
		t.Fatalf("前提：扫描器软删的条目是 missing: %#v", entry)
	}
	reviewISetEntryState(t, &models.VideoTrashEntry{}, entry.ID, trashStateRestoring)

	scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if len(scan.Errors) != 0 || scan.Added != 0 || scan.Restored != 0 || scan.SkipBreakdown.Existing != 1 {
		t.Fatalf("找不到 deleted 的 missing 条目时应交给同路径软删判定跳过，不报 add 错误: %+v", scan)
	}
	if active := reviewIActiveUnder(t, root); len(active) != 0 {
		t.Fatalf("不得新建记录: %#v", active)
	}
	if err := svc.ReconcileTrashEntries(); err != nil {
		t.Fatalf("启动对账应完成恢复: %v", err)
	}
	if got := p011ReloadVideo(t, video.ID); got.DeletedAt.IsValid() {
		t.Fatalf("原记录应被恢复: %#v", got)
	}
}

// 图片侧没有扫描器的 missing 条目（扫描器软删靠 is_stale 恢复），同路径的 restoring 条目由 softDeletedImagePathSkip 跳过：
// 同步不新建、也不报错误。
func TestIMG02SyncSkipsRestoringEntryWithoutErrorsFixLM2(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	path := filepath.Join(root, "photo.jpg")
	image := imageTrashTestCreateImage(t, path, "restoring-photo")
	mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010ImageEntry(t, image.ID)
	if err := os.Rename(entry.TrashPath, path); err != nil {
		t.Fatal(err)
	}
	reviewISetEntryState(t, &models.ImageTrashEntry{}, entry.ID, trashStateRestoring)
	result := imageTestMustSync(t, svc)
	if result.Added != 0 || result.Restored != 0 || len(result.Errors) != 0 || result.Skipped != 1 {
		t.Fatalf("图片同路径 restoring 条目应跳过且不报错: %+v", result)
	}
}

// ---------- m4：墓碑的软删行按已硬删处理；trash/ 目录不在时清理墓碑 ----------

func TestLIB05TombstoneDoesNotMaskEarlierSoftDeletedRowsFixLM4(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	// 「只删记录」的屏蔽行，之后同一路径上又留下一条更新的墓碑行。
	blockedPath := filepath.Join(root, "movies", "blocked.mp4")
	blocked := p010Video(t, blockedPath, "blocked-content")
	mustSetFileModTime(t, blockedPath, time.Now().Add(-2*time.Hour))
	if err := svc.DeleteVideo(blocked.ID, false); err != nil {
		t.Fatal(err)
	}
	reviewLTombstoneVideoAt(t, blockedPath)
	// 扫描器缺失软删（missing，文件回来应自动恢复），之后同一路径上同样留下一条更新的墓碑行。
	missingPath := filepath.Join(root, "movies", "missing.mp4")
	missing := p010Video(t, missingPath, "missing-content")
	mustSetFileModTime(t, missingPath, time.Now().Add(-2*time.Hour))
	if err := svc.deleteVideoRecordBy(missing.ID, false, "scanner"); err != nil {
		t.Fatal(err)
	}
	reviewLTombstoneVideoAt(t, missingPath)

	scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	for _, scanErr := range scan.Errors {
		if scanErr.Operation != "refresh_metadata" {
			t.Fatalf("扫描出现意外错误: %+v", scan)
		}
	}
	if scan.Added != 0 {
		t.Fatalf("墓碑不得挡住更早的软删行而让扫描新建记录: %+v", scan)
	}
	if scan.SkipBreakdown.BlockedUserDelete != 1 {
		t.Fatalf("「只删记录」的屏蔽应照常生效: %+v", scan)
	}
	if scan.Restored != 1 {
		t.Fatalf("扫描器的缺失软删应照常自动恢复: %+v", scan)
	}
	if got := p011ReloadVideo(t, missing.ID); got.DeletedAt.IsValid() {
		t.Fatalf("缺失软删的原记录应被恢复: %#v", got)
	}
	if got := p011ReloadVideo(t, blocked.ID); !got.DeletedAt.IsValid() {
		t.Fatalf("屏蔽行保持软删: %#v", got)
	}
	active := reviewIActiveUnder(t, root)
	if len(active) != 1 || active[0].ID != missing.ID {
		t.Fatalf("只应有恢复的那一条活跃记录: %#v", active)
	}
}

func TestIMG02TombstoneDoesNotMaskEarlierSoftDeletedRowsFixLM4(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	// 扫描器软删（is_stale 恢复标记），之后同一路径上留下一条更新的墓碑行。
	stalePath := filepath.Join(root, "pics", "stale.jpg")
	stale := imageTrashTestCreateImage(t, stalePath, "stale-image")
	mustSetFileModTime(t, stalePath, time.Now().Add(-2*time.Hour))
	if err := svc.deleteMissingImageRecord(stale.ID); err != nil {
		t.Fatal(err)
	}
	reviewLTombstoneImageAt(t, stalePath)
	// 「只删记录」的屏蔽行 + 更新的墓碑行。
	blockedPath := filepath.Join(root, "pics", "blocked.jpg")
	blocked := imageTrashTestCreateImage(t, blockedPath, "blocked-image")
	mustSetFileModTime(t, blockedPath, time.Now().Add(-2*time.Hour))
	if err := svc.DeleteImage(blocked.ID, false); err != nil {
		t.Fatal(err)
	}
	reviewLTombstoneImageAt(t, blockedPath)

	result := imageTestMustSync(t, svc)
	if result.Added != 0 || result.Restored != 1 || len(result.Errors) != 0 {
		t.Fatalf("墓碑不得挡住更早的软删行: %+v", result)
	}
	if n := reviewGCountRows(t, &models.Image{}, "id = ? AND deleted_at IS NULL", stale.ID); n != 1 {
		t.Fatalf("扫描器软删的原图片应被恢复: %d", n)
	}
	if n := reviewGCountRows(t, &models.Image{}, "id = ? AND deleted_at IS NOT NULL", blocked.ID); n != 1 {
		t.Fatalf("屏蔽行保持软删: %d", n)
	}
	if n := reviewGCountRows(t, &models.Image{}, "path = ? AND deleted_at IS NULL", blockedPath); n != 0 {
		t.Fatalf("屏蔽的路径不得新建记录: %d", n)
	}
}

func TestLIB05EnhancementOutputPathIgnoresTombstoneRowFixLM4(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	const basename = "movie.enhanced-general-2x.mkv"
	target := filepath.Join(video.Directory, basename)
	_, tomb := reviewLTombstoneVideoAt(t, target)
	service := newEnhancementTestService(t, func(ctx context.Context, name string, args []string) (string, error) {
		return enhancementProbeJSON(1280, 720, 10, ""), nil
	})

	if err := service.ensureOutputNameFree(video, basename); err != nil {
		t.Fatalf("墓碑的软删记录不得占用输出路径: %v", err)
	}
	// 对照：同一行只要不是墓碑（普通的回收站条目），仍然占用。
	reviewISetEntryState(t, &models.VideoTrashEntry{}, tomb.ID, trashStateDeleted)
	if err := service.ensureOutputNameFree(video, basename); err == nil || !strings.Contains(err.Error(), "output_conflict") {
		t.Fatalf("回收站里的普通软删记录仍占用输出路径: %v", err)
	}
	reviewISetEntryState(t, &models.VideoTrashEntry{}, tomb.ID, trashStateRemoved)

	// 发布时的再查冲突同一口径。
	task := models.VideoEnhancementTask{VideoID: video.ID, Profile: "general", Status: models.EnhancementStatusRunning,
		OutputBasename: basename, SourceSize: video.Size}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(video.Directory, "final.cispart")
	if err := os.WriteFile(staging, []byte("final-matroska"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := service.publishOutput(context.Background(), task, video, staging); err != nil {
		t.Fatalf("墓碑不得让发布报 output_conflict: %v", err)
	}
	var output models.Video
	if err := database.DB.Where("path = ?", target).First(&output).Error; err != nil {
		t.Fatalf("产物应入库: %v", err)
	}
}

func TestLIB05SweepDropsTombstonesWhoseTrashDirIsGoneFixLM4(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	center := NewTrashCenter(svc, nil)
	root := t.TempDir()
	tombstone := func(name string) (models.Video, models.VideoTrashEntry) {
		video, entry := reviewIOldLegacyVideo(t, filepath.Join(root, name, "movie.mp4"), "legacy-"+name)
		result, err := center.ForceRemoveTrashRecords("video", []uint{entry.ID}, TrashForceRemoveConfirmText)
		if err != nil || result.Succeeded != 1 {
			t.Fatalf("前提：legacy 行移除后成为墓碑: %#v err=%v", result, err)
		}
		return video, entry
	}
	goneVideo, goneEntry := tombstone("gone")
	keptVideo, keptEntry := tombstone("kept")
	offlineVideo, offlineEntry := tombstone("offline")
	// 目录已不在：gone 在线；offline 所在的「卷」离线（替身），Lstat 同样报不存在。
	for _, entry := range []models.VideoTrashEntry{goneEntry, offlineEntry} {
		if err := os.RemoveAll(filepath.Dir(entry.TrashPath)); err != nil {
			t.Fatal(err)
		}
	}
	offlinePrefix := filepath.Join(root, "offline")
	fn := func(path string) error {
		if pathIsEqualOrInside(filepath.Clean(path), offlinePrefix) {
			return errors.New("卷未挂载（替身）")
		}
		return nil
	}
	previous := mediaVolumeAvailableFn.Swap(&fn)
	t.Cleanup(func() { mediaVolumeAvailableFn.Store(previous) })

	if err := svc.ReconcileTrashEntries(); err != nil {
		t.Fatalf("启动对账不应失败: %v", err)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", goneEntry.ID); n != 0 {
		t.Fatalf("目录已不存在的墓碑应被清理: %d", n)
	}
	if n := reviewGCountRows(t, &models.Video{}, "id = ?", goneVideo.ID); n != 0 {
		t.Fatalf("墓碑的软删记录应一并硬删: %d", n)
	}
	for _, kept := range []struct {
		entry models.VideoTrashEntry
		video models.Video
		why   string
	}{{keptEntry, keptVideo, "目录还在"}, {offlineEntry, offlineVideo, "卷离线"}} {
		if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ? AND state = ?", kept.entry.ID, trashStateRemoved); n != 1 {
			t.Fatalf("%s的墓碑必须保留: %d", kept.why, n)
		}
		if n := reviewGCountRows(t, &models.Video{}, "id = ? AND deleted_at IS NOT NULL", kept.video.ID); n != 1 {
			t.Fatalf("%s时软删记录保持原样: %d", kept.why, n)
		}
	}
}

func TestIMG02SweepDropsTombstonesWhoseTrashDirIsGoneFixLM4(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	image := imageTrashTestCreateImage(t, filepath.Join(root, "pics", "old.jpg"), "legacy-image")
	entry := reviewGLegacyImageEntry(t, image)
	if result, err := NewTrashCenter(nil, svc).ForceRemoveTrashRecords("image", []uint{entry.ID}, TrashForceRemoveConfirmText); err != nil || result.Succeeded != 1 {
		t.Fatalf("前提：图片 legacy 行移除后成为墓碑: %#v err=%v", result, err)
	}
	if err := svc.ReconcileImageTrashEntries(); err != nil {
		t.Fatal(err)
	}
	if n := reviewGCountRows(t, &models.ImageTrashEntry{}, "id = ? AND state = ?", entry.ID, trashStateRemoved); n != 1 {
		t.Fatalf("目录还在时墓碑保留: %d", n)
	}
	if err := os.RemoveAll(filepath.Dir(entry.TrashPath)); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileImageTrashEntries(); err != nil {
		t.Fatalf("启动对账不应失败: %v", err)
	}
	if n := reviewGCountRows(t, &models.ImageTrashEntry{}, "id = ?", entry.ID); n != 0 {
		t.Fatalf("目录已不存在的图片墓碑应被清理: %d", n)
	}
	if n := reviewGCountRows(t, &models.Image{}, "id = ?", image.ID); n != 0 {
		t.Fatalf("图片墓碑的软删记录应一并硬删: %d", n)
	}
}

// ---------- m5：恢复成功、残留硬链接名清不掉时留墓碑 ----------

func TestLIB05LegacyResidueCleanupFailureLeavesTombstoneFixLM5(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video, entry := reviewIOldLegacyVideo(t, filepath.Join(root, "movies", "old.mp4"), "legacy-residue")
	// 旧版恢复在 link 之后、remove 之前中断：原路径与 trash/ 里是同一个文件的两个名字。
	if err := os.Link(entry.TrashPath, video.Path); err != nil {
		t.Skip("无法创建硬链接")
	}
	trashDir := filepath.Dir(entry.TrashPath)
	unlockDir := reviewLLockDirReadOnly(t, trashDir)

	center := NewTrashCenter(svc, nil)
	result, err := center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("恢复本身应成功: %#v err=%v", result, err)
	}
	if got := p011ReloadVideo(t, video.ID); got.DeletedAt.IsValid() {
		t.Fatalf("记录应恢复: %#v", got)
	}
	var tomb models.VideoTrashEntry
	if err := database.DB.First(&tomb, entry.ID).Error; err != nil || tomb.State != trashStateRemoved || tomb.TrashPath != entry.TrashPath {
		t.Fatalf("残留名字清不掉时条目应改为墓碑（保留 trash_path），不硬删: %#v err=%v", tomb, err)
	}
	if !reviewDSameInode(t, video.Path, entry.TrashPath) {
		t.Fatal("两个名字都应还在")
	}
	// 墓碑不可见；目录继续登记，重扫不收录残留名字。
	if page, err := center.ListTrashEntries(TrashFilter{Kind: "video"}); err != nil || len(page.Items) != 0 {
		t.Fatalf("墓碑不得出现在列表: %#v err=%v", page, err)
	}
	refreshLegacyTrashDirs()
	if !isTrashDir(trashDir) {
		t.Fatal("墓碑应让旧版 trash/ 目录继续算作已登记")
	}
	scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	for _, scanErr := range scan.Errors {
		if scanErr.Operation != "refresh_metadata" {
			t.Fatalf("重扫出现意外错误: %+v", scan)
		}
	}
	if scan.Added != 0 {
		t.Fatalf("重扫不得把残留名字当成新文件收录: %+v", scan)
	}
	if active := reviewIActiveUnder(t, root); len(active) != 1 || active[0].ID != video.ID {
		t.Fatalf("只应有恢复的那一条活跃记录: %#v", active)
	}

	// 再次删除 / 永久删除：残留清不掉时拒绝，文件与记录都不动（墓碑挂在记录上，新条目也建不进去）。
	if err := svc.DeleteVideo(video.ID, true); !errors.Is(err, errTrashRestoreResidueRemains) || strings.Contains(err.Error(), root) {
		t.Fatalf("残留清不掉时再次删除应拒绝且文案不带路径: %v", err)
	}
	permanent := svc.PermanentlyDeleteVideos([]uint{video.ID})
	if permanent.Succeeded != 0 || permanent.Items[0].Code != TrashResultPermissionDenied {
		t.Fatalf("残留清不掉时永久删除应拒绝: %#v", permanent)
	}
	if got := reviewGFileContent(t, video.Path); got != "legacy-residue" {
		t.Fatalf("原文件不得被动: %q", got)
	}
	if got := p011ReloadVideo(t, video.ID); got.DeletedAt.IsValid() {
		t.Fatalf("记录保持活跃: %#v", got)
	}

	// 权限恢复之后再删：先补做清理、删掉墓碑，再照常移入废纸篓。
	unlockDir()
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatalf("残留可清理之后应能删除: %v", err)
	}
	if _, err := os.Lstat(entry.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("删除前应清掉残留名字: %v", err)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", entry.ID); n != 0 {
		t.Fatalf("墓碑应被删掉: %d", n)
	}
	if got := p010VideoEntry(t, video.ID); got.Mode != models.TrashModeTrash || got.State != trashStateDeleted {
		t.Fatalf("应建出新的回收站条目: %#v", got)
	}
}

func TestIMG02LegacyResidueCleanupFailureLeavesTombstoneFixLM5(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	image := imageTrashTestCreateImage(t, filepath.Join(root, "pics", "old.jpg"), "legacy-image-residue")
	entry := reviewGLegacyImageEntry(t, image)
	if err := os.Link(entry.TrashPath, image.Path); err != nil {
		t.Skip("无法创建硬链接")
	}
	trashDir := filepath.Dir(entry.TrashPath)
	unlockDir := reviewLLockDirReadOnly(t, trashDir)

	result, err := NewTrashCenter(nil, svc).RestoreTrashEntries("image", []uint{entry.ID})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("图片恢复本身应成功: %#v err=%v", result, err)
	}
	if n := reviewGCountRows(t, &models.ImageTrashEntry{}, "id = ? AND state = ? AND trash_path = ?", entry.ID, trashStateRemoved, entry.TrashPath); n != 1 {
		t.Fatalf("残留名字清不掉时图片条目应改为墓碑: %d", n)
	}
	sync := imageTestMustSync(t, svc)
	if sync.Added != 0 {
		t.Fatalf("图片同步不得收录残留名字: %+v", sync)
	}
	if err := svc.DeleteImage(image.ID, true); !errors.Is(err, errTrashRestoreResidueRemains) {
		t.Fatalf("残留清不掉时再次删除图片应拒绝: %v", err)
	}

	// trash/ 目录随后被整个删掉（残留名字随之不在）：启动对账清理墓碑，只删条目，活跃记录不动；之后照常删除。
	unlockDir()
	if err := os.RemoveAll(trashDir); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileImageTrashEntries(); err != nil {
		t.Fatalf("启动对账不应失败: %v", err)
	}
	if n := reviewGCountRows(t, &models.ImageTrashEntry{}, "id = ?", entry.ID); n != 0 {
		t.Fatalf("目录已不存在的墓碑应被清理: %d", n)
	}
	if n := reviewGCountRows(t, &models.Image{}, "id = ? AND deleted_at IS NULL", image.ID); n != 1 {
		t.Fatalf("墓碑引用的活跃记录不得被动: %d", n)
	}
	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatalf("墓碑清理之后应能删除: %v", err)
	}
}

// 残留名字清理成功时不留墓碑：条目随恢复一起消失（与修复前的结果一致）。
func TestLIB05LegacyResidueCleanedLeavesNoTombstoneFixLM5(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video, entry := reviewIOldLegacyVideo(t, filepath.Join(t.TempDir(), "movies", "old.mp4"), "legacy-cleaned")
	if err := os.Link(entry.TrashPath, video.Path); err != nil {
		t.Skip("无法创建硬链接")
	}
	if _, err := svc.RestoreTrashEntry(entry.ID); err != nil {
		t.Fatalf("恢复应成功: %v", err)
	}
	if _, err := os.Lstat(entry.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("残留名字应被清掉: %v", err)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", entry.ID); n != 0 {
		t.Fatalf("清理成功时不得留下墓碑: %d", n)
	}
	if err := svc.DeleteVideo(video.ID, false); err != nil {
		t.Fatalf("恢复后的记录应能照常再删: %v", err)
	}
}

// ---------- m6：对墓碑的任何操作都是「回收站条目不存在」 ----------

func TestLIB05TombstoneOperationsReturnEntryNotFoundFixLM6(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	center := NewTrashCenter(svc, nil)
	_, entry := reviewIOldLegacyVideo(t, filepath.Join(t.TempDir(), "movies", "old.mp4"), "legacy-tomb")
	if result, err := center.ForceRemoveTrashRecords("video", []uint{entry.ID}, TrashForceRemoveConfirmText); err != nil || result.Succeeded != 1 {
		t.Fatalf("前提：成为墓碑: %#v err=%v", result, err)
	}
	missingID := entry.ID + 1000
	for _, id := range []uint{entry.ID, missingID} {
		want := fmt.Sprintf("回收站条目不存在: %d", id)
		for name, call := range map[string]func() (*BatchResult, error){
			"restore": func() (*BatchResult, error) { return center.RestoreTrashEntries("video", []uint{id}) },
			"purge":   func() (*BatchResult, error) { return center.PurgeTrashEntries("video", []uint{id}) },
			"remove":  func() (*BatchResult, error) { return center.RemoveGoneTrashEntries("video", []uint{id}) },
			"force_remove": func() (*BatchResult, error) {
				return center.ForceRemoveTrashRecords("video", []uint{id}, TrashForceRemoveConfirmText)
			},
		} {
			result, err := call()
			if err != nil || len(result.Items) != 1 || result.Items[0].Code != TrashResultError || result.Items[0].Message != want {
				t.Fatalf("%s(%d) 应返回「%s」: %#v err=%v", name, id, want, result, err)
			}
		}
		_, err := svc.RestoreTrashEntry(id)
		if !errors.Is(err, ErrTrashEntryNotFound) || !errors.Is(err, gorm.ErrRecordNotFound) || err.Error() != want {
			t.Fatalf("旧版恢复接口(%d) 应返回「%s」: %v", id, want, err)
		}
	}
}

func TestIMG02TombstoneOperationsReturnEntryNotFoundFixLM6(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	center := NewTrashCenter(nil, svc)
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	image := imageTrashTestCreateImage(t, filepath.Join(root, "pics", "old.jpg"), "legacy-image-tomb")
	entry := reviewGLegacyImageEntry(t, image)
	if result, err := center.ForceRemoveTrashRecords("image", []uint{entry.ID}, TrashForceRemoveConfirmText); err != nil || result.Succeeded != 1 {
		t.Fatalf("前提：成为墓碑: %#v err=%v", result, err)
	}
	want := fmt.Sprintf("回收站条目不存在: %d", entry.ID)
	for name, call := range map[string]func() (*BatchResult, error){
		"restore": func() (*BatchResult, error) { return center.RestoreTrashEntries("image", []uint{entry.ID}) },
		"purge":   func() (*BatchResult, error) { return center.PurgeTrashEntries("image", []uint{entry.ID}) },
		"remove":  func() (*BatchResult, error) { return center.RemoveGoneTrashEntries("image", []uint{entry.ID}) },
		"force_remove": func() (*BatchResult, error) {
			return center.ForceRemoveTrashRecords("image", []uint{entry.ID}, TrashForceRemoveConfirmText)
		},
	} {
		result, err := call()
		if err != nil || len(result.Items) != 1 || result.Items[0].Code != TrashResultError || result.Items[0].Message != want {
			t.Fatalf("图片 %s 应返回「%s」: %#v err=%v", name, want, result, err)
		}
	}
	if _, err := svc.RestoreImageTrashEntry(entry.ID); !errors.Is(err, ErrTrashEntryNotFound) || err.Error() != want {
		t.Fatalf("图片旧版恢复接口应返回「%s」: %v", want, err)
	}
}
