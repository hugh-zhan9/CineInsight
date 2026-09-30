package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"gorm.io/gorm"
)

// 修复 P（修复 N 独立复审的 Minor，回收站「恢复后残留墓碑」相关）的回归测试。测试名里的 LIBxx / IMGxx 是问题清单 ID，
// FixP 后面是复审编号（M1…M4 = m-1…m-4）。系统废纸篓是替身（system_trash_testhook_test.go）。

// reviewPScannerSoftDeleteUnderTombstone 建一条挂「恢复后残留」墓碑的视频，文件拿走后以扫描器身份软删（修复 N I-1：
// 照常软删、墓碑保留、没有 missing 条目）。返回软删后的记录与墓碑。
func reviewPScannerSoftDeleteUnderTombstone(t *testing.T, svc *VideoService, root, content string) (models.Video, models.VideoTrashEntry) {
	t.Helper()
	video, tomb, _ := reviewNResidueTombstoneVideo(t, svc, root, content)
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	if err := svc.deleteVideoRecordBy(video.ID, false, "scanner"); err != nil {
		t.Fatalf("前提：扫描器软删挂墓碑的记录不得报错: %v", err)
	}
	deleted := p011ReloadVideo(t, video.ID)
	if !deleted.DeletedAt.IsValid() || deleted.DeletedBy != "scanner" {
		t.Fatalf("前提：记录应被扫描器软删: %#v", deleted)
	}
	return deleted, tomb
}

// reviewPPutBack 把文件内容写回原路径，mtime 调到两小时前（扫描不因「刚修改」跳过）。
func reviewPPutBack(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSetFileModTime(t, path, time.Now().Add(-2*time.Hour))
}

// ---------- m-1：原路径上的文件与记录不一致时，文案告诉用户怎么办 ----------

func TestLIB05ScannerRestoreMismatchMessageIsActionableFixPM1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video, _ := reviewPScannerSoftDeleteUnderTombstone(t, svc, root, "legacy-residue-mismatch")
	reviewPPutBack(t, video.Path, "a different, longer file than before")

	scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if scan.Restored != 0 || scan.Added != 0 {
		t.Fatalf("大小不同的文件不得恢复，也不得新建: %+v", scan)
	}
	var message string
	for _, scanErr := range scan.Errors {
		if scanErr.Operation == "add" {
			message = scanErr.Error
		}
	}
	if message != errScannerRestoreFileMismatch.Error() {
		t.Fatalf("应计入 add 错误并使用不一致文案: %q (%+v)", message, scan.Errors)
	}
	if !strings.Contains(message, "未自动恢复") || !strings.Contains(message, "改名后重新扫描") {
		t.Fatalf("文案应说明未自动恢复，并提示改名后重新扫描: %q", message)
	}
	if strings.Contains(message, root) || strings.Contains(message, "/") {
		t.Fatalf("文案不得带路径: %q", message)
	}
}

// ---------- m-2：只有 trash_path 不可用时，文案点明是上次恢复留下的旧版回收站文件夹 ----------

func TestLIB05SettleTombstoneTrashDirUnavailableNamesLegacyFolderFixPM2(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video, tomb, unlockTrashDir := reviewNResidueTombstoneVideo(t, svc, root, "legacy-residue-trash-offline")
	unlockTrashDir()
	trashDir := filepath.Dir(tomb.TrashPath)
	permission := fmt.Errorf("读不到挂载点（替身）: %w", os.ErrPermission)
	for _, tc := range []struct {
		name     string
		cause    error
		prefix   string
		code     string
		want     error
		folder   bool
		original string
	}{
		{"trash_path 所在位置离线、视频在线", nil, trashDir, TrashResultVolumeOffline, ErrTrashVolumeOffline, true, ""},
		{"trash_path 所在位置无权限、视频在线", permission, trashDir, TrashResultPermissionDenied, ErrTrashPermissionDenied, true, ""},
		{"视频当前路径所在位置离线", nil, video.Path, TrashResultVolumeOffline, ErrTrashVolumeOffline, false, ErrTrashVolumeOffline.Error()},
		{"整块盘离线（视频与 trash_path 都不可用）", nil, root, TrashResultVolumeOffline, ErrTrashVolumeOffline, false, ErrTrashVolumeOffline.Error()},
	} {
		restore := reviewNStubOffline(t, tc.cause, tc.prefix)
		if err := svc.DeleteVideo(video.ID, true); !errors.Is(err, tc.want) {
			t.Fatalf("%s：删除应被拒绝并按 errors.Is 命中 %v: %v", tc.name, tc.want, err)
		}
		deleted := svc.DeleteVideosDetailed([]uint{video.ID}, true, BatchDeleteOptions{})
		permanent := svc.PermanentlyDeleteVideos([]uint{video.ID})
		restore()
		for label, result := range map[string]*BatchResult{"删除": deleted, "永久删除": permanent} {
			if result.Succeeded != 0 || len(result.Items) != 1 || result.Items[0].Code != tc.code {
				t.Fatalf("%s：%s的结果码应为 %s: %#v", tc.name, label, tc.code, result)
			}
			message := result.Items[0].Message
			if strings.Contains(message, root) {
				t.Fatalf("%s：%s的文案不得带路径: %q", tc.name, label, message)
			}
			mentionsFolder := strings.Contains(message, "上次恢复留下的旧版回收站文件夹")
			if mentionsFolder != tc.folder {
				t.Fatalf("%s：%s的文案是否点明旧版回收站文件夹应为 %t: %q", tc.name, label, tc.folder, message)
			}
			if tc.original != "" && message != tc.original {
				t.Fatalf("%s：记录自己的位置不可用时保持原文案 %q: %q", tc.name, tc.original, message)
			}
			if tc.folder && tc.cause == nil && !strings.Contains(message, "接上该磁盘后重试") {
				t.Fatalf("%s：离线文案应提示接上磁盘后重试: %q", tc.name, message)
			}
			if tc.folder && tc.cause != nil && !strings.Contains(message, "权限") {
				t.Fatalf("%s：无权限文案应提到权限: %q", tc.name, message)
			}
		}
		if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ? AND state = ?", tomb.ID, trashStateRemoved); n != 1 {
			t.Fatalf("%s：墓碑必须保留: %d", tc.name, n)
		}
		if !reviewDSameInode(t, video.Path, tomb.TrashPath) {
			t.Fatalf("%s：残留名字与原文件都不得被动", tc.name)
		}
		if got := p011ReloadVideo(t, video.ID); got.DeletedAt.IsValid() {
			t.Fatalf("%s：记录保持活跃: %#v", tc.name, got)
		}
	}
}

func TestIMG02SettleTombstoneTrashDirOfflineNamesLegacyFolderFixPM2(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	image, tomb, unlockTrashDir := reviewNResidueTombstoneImage(t, svc, root, "legacy-image-trash-offline")
	unlockTrashDir()
	restore := reviewNStubOffline(t, nil, filepath.Dir(tomb.TrashPath))
	deleted := svc.DeleteImagesDetailed([]uint{image.ID}, true, BatchDeleteOptions{})
	restore()
	if deleted.Succeeded != 0 || len(deleted.Items) != 1 || deleted.Items[0].Code != TrashResultVolumeOffline {
		t.Fatalf("trash_path 离线时删除图片的结果码应为 volume_offline: %#v", deleted)
	}
	if message := deleted.Items[0].Message; !strings.Contains(message, "上次恢复留下的旧版回收站文件夹") || strings.Contains(message, root) {
		t.Fatalf("图片删除文案应点明旧版回收站文件夹且不带路径: %q", message)
	}
	if n := reviewGCountRows(t, &models.ImageTrashEntry{}, "id = ? AND state = ?", tomb.ID, trashStateRemoved); n != 1 {
		t.Fatalf("图片墓碑必须保留: %d", n)
	}
}

// ---------- m-3：测试缺口 ----------

// 视频先被窄扫描标为失效（missing_file），再被全量扫描以扫描器身份软删（挂墓碑、没有 missing 条目）；文件回来后
// 恢复为原 ID，失效标记与原因一并清掉（restoreScannerDeletedVideoWithoutEntry 写 is_stale 同时写 stale_reason）。
func TestLIB05StaleThenScannerSoftDeletedUnderTombstoneRestoresClearedFixPM3(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	dirs := []models.ScanDirectory{{Path: root}}
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	const content = "legacy-residue-stale"
	video, tomb, _ := reviewNResidueTombstoneVideo(t, svc, root, content)
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}

	// 1. 窄扫描（监听触发的对账）：只标失效，不软删。
	svc.SyncAffectedDirectories(dirs, []string{filepath.Dir(video.Path)})
	stale := p011ReloadVideo(t, video.ID)
	if stale.DeletedAt.IsValid() || !stale.IsStale || stale.StaleReason != models.StaleReasonMissingFile {
		t.Fatalf("前提：窄扫描应把记录标为 missing_file 失效: %#v", stale)
	}

	// 2. 全量扫描：扫描器软删，失效标记原样带进软删行，墓碑保留。
	if got := svc.SyncScanDirectories(dirs); got.Deleted != 1 {
		t.Fatalf("前提：全量扫描应软删这条失效记录: %+v", got)
	}
	deleted := p011ReloadVideo(t, video.ID)
	if !deleted.DeletedAt.IsValid() || deleted.DeletedBy != "scanner" || !deleted.IsStale || deleted.StaleReason != models.StaleReasonMissingFile {
		t.Fatalf("前提：扫描器软删的行仍带失效标记: %#v", deleted)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ? AND state = ?", tomb.ID, trashStateRemoved); n != 1 {
		t.Fatalf("前提：墓碑保留: %d", n)
	}

	// 3. 文件回到原处：恢复为原 ID，is_stale=false 且 stale_reason=''。
	reviewPPutBack(t, video.Path, content)
	if got := svc.SyncScanDirectories(dirs); got.Restored != 1 || got.Added != 0 {
		t.Fatalf("应恢复原记录、不新建: %+v", got)
	}
	restored := p011ReloadVideo(t, video.ID)
	if restored.DeletedAt.IsValid() || restored.IsStale || restored.StaleReason != "" {
		t.Fatalf("恢复后应为活跃、失效标记与原因都已清空: %#v", restored)
	}
	if n := reviewGCountRows(t, &models.Video{}, "path = ?", video.Path); n != 1 {
		t.Fatalf("同一路径只应有原来那一条记录: %d", n)
	}
}

// MoveVideo 之后永久删除挂墓碑的视频：残留判定用记录的当前路径（permanentlyDeleteVideo 传 video.Path），
// 清掉残留名字、删掉墓碑，再照常删文件、硬删记录。
func TestLIB05PermanentDeleteAfterMoveSettlesTombstoneByCurrentPathFixPM3(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video, tomb, unlockTrashDir := reviewNResidueTombstoneVideo(t, svc, root, "legacy-residue-moved-permanent")
	unlockTrashDir()
	destination := filepath.Join(root, "moved")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MoveVideo(video.ID, destination); err != nil {
		t.Fatalf("前提：移动应成功: %v", err)
	}
	moved := p011ReloadVideo(t, video.ID)
	if moved.Path == video.Path || moved.Path == tomb.OriginalPath {
		t.Fatalf("前提：记录路径应已改变: %#v", moved)
	}
	if !reviewDSameInode(t, moved.Path, tomb.TrashPath) {
		t.Skip("移动没有保留 inode，残留名字不再是当前文件的另一个名字")
	}

	permanent := svc.PermanentlyDeleteVideos([]uint{video.ID})
	if permanent.Succeeded != 1 || len(permanent.Items) != 1 || permanent.Items[0].Code != TrashResultOK {
		t.Fatalf("移动之后应按当前路径收尾并永久删除: %#v", permanent)
	}
	if _, err := os.Lstat(tomb.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("永久删除前应清掉残留名字: %v", err)
	}
	if _, err := os.Lstat(moved.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("当前路径上的文件应被永久删除: %v", err)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "video_id = ?", video.ID); n != 0 {
		t.Fatalf("墓碑应被删掉: %d", n)
	}
	if n := reviewGCountRows(t, &models.Video{}, "id = ?", video.ID); n != 0 {
		t.Fatalf("记录应被硬删: %d", n)
	}
}

// 图片重定位之后永久删除挂墓碑的图片：同上（permanentlyDeleteImage 传 image.Path）。
func TestIMG02PermanentDeleteAfterRelocateSettlesTombstoneByCurrentPathFixPM3(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	image, tomb, unlockTrashDir := reviewNResidueTombstoneImage(t, svc, root, "legacy-image-relocated-permanent")
	unlockTrashDir()
	relocated := filepath.Join(root, "pics", "renamed.jpg")
	if err := os.Rename(image.Path, relocated); err != nil {
		t.Fatal(err)
	}
	if err := svc.relocateImage(image.ID, relocated); err != nil {
		t.Fatalf("前提：重定位应成功: %v", err)
	}
	if !reviewDSameInode(t, relocated, tomb.TrashPath) {
		t.Fatal("前提：改名保留 inode，残留名字是当前文件的另一个名字")
	}

	permanent := svc.PermanentlyDeleteImages([]uint{image.ID})
	if permanent.Succeeded != 1 || len(permanent.Items) != 1 || permanent.Items[0].Code != TrashResultOK {
		t.Fatalf("重定位之后应按当前路径收尾并永久删除图片: %#v", permanent)
	}
	if _, err := os.Lstat(tomb.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("永久删除前应清掉图片的残留名字: %v", err)
	}
	if _, err := os.Lstat(relocated); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("当前路径上的图片应被永久删除: %v", err)
	}
	if n := reviewGCountRows(t, &models.ImageTrashEntry{}, "image_id = ?", image.ID); n != 0 {
		t.Fatalf("图片墓碑应被删掉: %d", n)
	}
	if n := reviewGCountRows(t, &models.Image{}, "id = ?", image.ID); n != 0 {
		t.Fatalf("图片记录应被硬删: %d", n)
	}
}

// restoreScannerDeletedVideoWithoutEntry 恢复之后重建字幕索引：扫描器软删时索引随记录清掉，文件回来、旁挂 .srt 还在时，
// 恢复后索引行（分段与索引状态）重新存在。
func TestLIB05ScannerRestoreWithoutEntryRebuildsSubtitleIndexFixPM3(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	const content = "legacy-residue-subtitle"
	video, _, _ := reviewNResidueTombstoneVideo(t, svc, root, content)
	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	if err := os.WriteFile(srtPath, []byte("1\n00:00:01,000 --> 00:00:02,000\nneedle phrase fixp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureSubtitleIndexForVideo(video); err != nil {
		t.Fatalf("前提：建立字幕索引: %v", err)
	}
	segments := func() int64 {
		t.Helper()
		return reviewGCountRows(t, &models.SubtitleSegment{}, "video_id = ? AND text LIKE ?", video.ID, "%needle phrase fixp%")
	}
	if n := segments(); n != 1 {
		t.Fatalf("前提：字幕已进索引: %d", n)
	}
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	if err := svc.deleteVideoRecordBy(video.ID, false, "scanner"); err != nil {
		t.Fatalf("前提：扫描器软删挂墓碑的记录不得报错: %v", err)
	}
	if n := segments(); n != 0 {
		t.Fatalf("前提：软删时字幕索引随记录清掉: %d", n)
	}

	reviewPPutBack(t, video.Path, content)
	if got := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}}); got.Restored != 1 || got.Added != 0 {
		t.Fatalf("应恢复原记录: %+v", got)
	}
	if n := segments(); n != 1 {
		t.Fatalf("恢复后字幕分段应已重建: %d", n)
	}
	if n := reviewGCountRows(t, &models.SubtitleIndexState{}, "video_id = ? AND segment_count = ?", video.ID, 1); n != 1 {
		t.Fatalf("恢复后字幕索引状态行应存在并记下分段数: %d", n)
	}
}

// 条件更新的 deleted_by='scanner' 守卫：调用方读到的是扫描器软删的行，读取之后这条记录被改成用户删除——恢复必须失败，
// 记录保持用户删除的软删状态（用户删除的记录不能被扫描自动恢复）。
func TestLIB05ScannerRestoreWithoutEntryRequiresScannerDeletionFixPM3(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video := p010Video(t, filepath.Join(t.TempDir(), "movies", "guard.mp4"), "guard-content")
	if err := database.DB.Model(&video).Update("deleted_by", "scanner").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	read := p011ReloadVideo(t, video.ID)
	if !read.DeletedAt.IsValid() || read.DeletedBy != "scanner" {
		t.Fatalf("前提：读到的是扫描器软删的行: %#v", read)
	}
	// 读取之后，另一个入口把它改成了用户删除。
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", video.ID).Update("deleted_by", "user").Error; err != nil {
		t.Fatal(err)
	}

	restored, err := svc.restoreScannerDeletedVideoWithoutEntry(read)
	if err == nil || restored != nil {
		t.Fatalf("用户删除的记录不得被恢复: restored=%#v err=%v", restored, err)
	}
	if errors.Is(err, errScannerRestoreFileMismatch) {
		t.Fatalf("文件与记录一致，拒绝只能来自条件更新: %v", err)
	}
	if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() || got.DeletedBy != "user" {
		t.Fatalf("记录应保持用户删除的软删: %#v", got)
	}
}

// ---------- m-4：扫描器分支的软删实际写入 0 行时不计为删除 ----------

// reviewPSoftDeleteAfterEntryLookup 在 deleteVideoRecordBatch 读完记录、查既有回收站条目（video_id = videoID）之后，
// 模拟另一个入口（用户删除）抢先软删了这条记录。只触发一次。
func reviewPSoftDeleteAfterEntryLookup(t *testing.T, videoID uint) *bool {
	t.Helper()
	fired := false
	name := "test:review-p-soft-delete-after-entry-lookup"
	if err := database.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "video_trash_entries" || !strings.Contains(tx.Statement.SQL.String(), "video_id") ||
			len(tx.Statement.Vars) == 0 || fmt.Sprint(tx.Statement.Vars[0]) != fmt.Sprint(videoID) {
			return
		}
		fired = true
		if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ? AND deleted_at IS NULL", videoID).
			Updates(map[string]interface{}{"deleted_by": "user", "deleted_at": time.Now()}).Error; err != nil {
			t.Errorf("模拟并发软删失败: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.DB.Callback().Query().Remove(name) })
	return &fired
}

func TestLIB05ScannerDeleteUnderTombstoneAlreadySoftDeletedIsNotCountedFixPM4(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video, tomb, _ := reviewNResidueTombstoneVideo(t, svc, root, "legacy-residue-race")
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	fired := reviewPSoftDeleteAfterEntryLookup(t, video.ID)

	code, err := svc.deleteVideoRecordBatch(video.ID, false, "scanner", "")
	if !*fired {
		t.Fatal("前提：应在读取之后模拟出别处的软删")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) || code != "" {
		t.Fatalf("软删写入 0 行时应与「记录已不在库」同一结果: code=%q err=%v", code, err)
	}
	if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() || got.DeletedBy != "user" {
		t.Fatalf("别处的软删保持原样，不得被改写成扫描器删除: %#v", got)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ? AND state = ?", tomb.ID, trashStateRemoved); n != 1 {
		t.Fatalf("墓碑保留: %d", n)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "video_id = ?", video.ID); n != 1 {
		t.Fatalf("不得另建条目: %d", n)
	}
}

// 扫描一侧：同样的竞争下 Deleted 不多算，按「记录已不在库」计入 delete 错误。
func TestLIB05ScanDoesNotCountScannerDeleteWhenRecordAlreadySoftDeletedFixPM4(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video, _, _ := reviewNResidueTombstoneVideo(t, svc, root, "legacy-residue-race-scan")
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	fired := reviewPSoftDeleteAfterEntryLookup(t, video.ID)

	scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if !*fired {
		t.Fatal("前提：扫描删除时应在读取之后模拟出别处的软删")
	}
	if scan.Deleted != 0 {
		t.Fatalf("记录已被别处软删，扫描不得计入 Deleted: %+v", scan)
	}
	found := false
	for _, scanErr := range scan.Errors {
		if scanErr.Operation == "delete" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应与「记录已不在库」一样计入 delete 错误: %+v", scan.Errors)
	}
	if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() || got.DeletedBy != "user" {
		t.Fatalf("别处的软删保持原样: %#v", got)
	}
}
