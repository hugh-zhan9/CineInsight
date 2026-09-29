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
)

// 修复 N（修复 L 复审发现的「恢复后残留墓碑」边界问题）的回归测试。测试名里的 LIBxx / IMGxx 是问题清单 ID，
// FixN 后面是复审编号（I1 / I2 = I-1 / I-2，M1…M3 = m1…m3）。系统废纸篓是替身（system_trash_testhook_test.go）。

// reviewNResidueTombstoneVideo 建一条「恢复成功、旧版 trash/ 里的残留硬链接名清不掉」的视频（修复 L m5 的墓碑）：
// 记录活跃、条目是墓碑（state=removed，保留 trash_path），trash/ 目录只读。返回解除只读的函数（之后残留名字可以清理）。
func reviewNResidueTombstoneVideo(t *testing.T, svc *VideoService, root, content string) (models.Video, models.VideoTrashEntry, func()) {
	t.Helper()
	video, entry := reviewIOldLegacyVideo(t, filepath.Join(root, "movies", "old.mp4"), content)
	if err := os.Link(entry.TrashPath, video.Path); err != nil {
		t.Skip("无法创建硬链接")
	}
	unlock := reviewLLockDirReadOnly(t, filepath.Dir(entry.TrashPath))
	result, err := NewTrashCenter(svc, nil).RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("前提：恢复本身应成功: %#v err=%v", result, err)
	}
	var tomb models.VideoTrashEntry
	if err := database.DB.First(&tomb, entry.ID).Error; err != nil || tomb.State != trashStateRemoved || tomb.TrashPath != entry.TrashPath {
		t.Fatalf("前提：残留名字清不掉时条目改为挂在活跃记录上的墓碑: %#v err=%v", tomb, err)
	}
	restored := p011ReloadVideo(t, video.ID)
	if restored.DeletedAt.IsValid() {
		t.Fatalf("前提：记录应已恢复: %#v", restored)
	}
	return restored, tomb, unlock
}

// reviewNResidueTombstoneImage 是 reviewNResidueTombstoneVideo 的图片版本（图片目录需已加入）。
func reviewNResidueTombstoneImage(t *testing.T, svc *ImageService, root, content string) (models.Image, models.ImageTrashEntry, func()) {
	t.Helper()
	image := imageTrashTestCreateImage(t, filepath.Join(root, "pics", "old.jpg"), content)
	entry := reviewGLegacyImageEntry(t, image)
	if err := os.Link(entry.TrashPath, image.Path); err != nil {
		t.Skip("无法创建硬链接")
	}
	unlock := reviewLLockDirReadOnly(t, filepath.Dir(entry.TrashPath))
	result, err := NewTrashCenter(nil, svc).RestoreTrashEntries("image", []uint{entry.ID})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("前提：图片恢复本身应成功: %#v err=%v", result, err)
	}
	var tomb models.ImageTrashEntry
	if err := database.DB.First(&tomb, entry.ID).Error; err != nil || tomb.State != trashStateRemoved || tomb.TrashPath != entry.TrashPath {
		t.Fatalf("前提：图片条目改为挂在活跃记录上的墓碑: %#v err=%v", tomb, err)
	}
	var restored models.Image
	if err := database.DB.First(&restored, image.ID).Error; err != nil {
		t.Fatalf("前提：图片记录应已恢复: %v", err)
	}
	return restored, tomb, unlock
}

// reviewNStubOffline 让 prefixes 之下的位置在卷挂载检查里报「未挂载」（替身）；cause 非 nil 时改报这个错误
// （例如包装 os.ErrPermission 的权限问题）。返回提前还原的函数。
func reviewNStubOffline(t *testing.T, cause error, prefixes ...string) func() {
	t.Helper()
	fn := func(path string) error {
		for _, prefix := range prefixes {
			if pathIsEqualOrInside(filepath.Clean(path), prefix) {
				if cause != nil {
					return cause
				}
				return errors.New("卷未挂载（替身）")
			}
		}
		return nil
	}
	previous := mediaVolumeAvailableFn.Swap(&fn)
	restored := false
	restore := func() {
		if !restored {
			restored = true
			mediaVolumeAvailableFn.Store(previous)
		}
	}
	t.Cleanup(restore)
	return restore
}

func reviewNVideoTagCount(t *testing.T, videoID uint) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Table("video_tags").Where("video_id = ?", videoID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func reviewNImageTagCount(t *testing.T, imageID uint) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Table("image_tags").Where("image_id = ?", imageID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

// ---------- I-1：扫描器软删挂墓碑的记录——照常软删、墓碑保留；过滤与清理只作用于用户删除的记录 ----------

func TestLIB05ScannerSoftDeleteUnderResidueTombstoneRestoresOriginalIDFixNI1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	const content = "legacy-residue-n1"
	video, tomb, unlockTrashDir := reviewNResidueTombstoneVideo(t, svc, root, content)
	trashDir := filepath.Dir(tomb.TrashPath)
	tag := models.Tag{Name: "修复N保留的标签"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.AddTagToVideo(video.ID, tag.ID); err != nil {
		t.Fatal(err)
	}
	scan := func(step string) *ScanSyncResult {
		t.Helper()
		result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
		for _, scanErr := range result.Errors {
			if scanErr.Operation != "refresh_metadata" {
				t.Fatalf("%s：扫描不得报错（修复前每次扫描都报 delete 错误）: %+v", step, result)
			}
		}
		if result.Added != 0 {
			t.Fatalf("%s：不得新建记录: %+v", step, result)
		}
		return result
	}
	putBack := func() {
		t.Helper()
		if err := os.WriteFile(video.Path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		mustSetFileModTime(t, video.Path, time.Now().Add(-2*time.Hour))
	}
	assertRestored := func(step string) {
		t.Helper()
		got := p011ReloadVideo(t, video.ID)
		if got.DeletedAt.IsValid() || got.IsStale {
			t.Fatalf("%s：应恢复为原 ID 的活跃记录: %#v", step, got)
		}
		if n := reviewNVideoTagCount(t, video.ID); n != 1 {
			t.Fatalf("%s：标签应保留: %d", step, n)
		}
		if n := reviewGCountRows(t, &models.Video{}, "path = ?", video.Path); n != 1 {
			t.Fatalf("%s：同一路径只应有原来那一条记录: %d", step, n)
		}
	}

	// 1. 文件不见了：扫描器照常软删（deleted_by='scanner'），不做残留收尾、不报错，墓碑保留、trash/ 继续登记。
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	if got := scan("缺失软删"); got.Deleted != 1 {
		t.Fatalf("扫描器应软删这条记录: %+v", got)
	}
	if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() || got.DeletedBy != "scanner" {
		t.Fatalf("记录应被扫描器软删: %#v", got)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ? AND state = ? AND trash_path = ?", tomb.ID, trashStateRemoved, tomb.TrashPath); n != 1 {
		t.Fatalf("墓碑必须保留: %d", n)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "video_id = ?", video.ID); n != 1 {
		t.Fatalf("不得另建条目: %d", n)
	}
	refreshLegacyTrashDirs()
	if !isTrashDir(trashDir) {
		t.Fatal("墓碑应让旧版 trash/ 目录继续算作已登记")
	}

	// 2. 文件回到原处：恢复为原 ID（标签保留），墓碑原样保留。
	putBack()
	if got := scan("文件回来"); got.Restored != 1 {
		t.Fatalf("应自动恢复原记录: %+v", got)
	}
	assertRestored("文件回来")
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ? AND state = ?", tomb.ID, trashStateRemoved); n != 1 {
		t.Fatalf("恢复后墓碑仍挂在活跃记录上: %d", n)
	}

	// 3. 再次缺失并软删；之后 trash/ 目录消失，启动清理只删墓碑，不硬删扫描器软删的记录。
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	if got := scan("再次缺失"); got.Deleted != 1 {
		t.Fatalf("扫描器应再次软删: %+v", got)
	}
	unlockTrashDir()
	if err := os.RemoveAll(trashDir); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileTrashEntries(); err != nil {
		t.Fatalf("启动对账不应失败: %v", err)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", tomb.ID); n != 0 {
		t.Fatalf("目录已不存在的墓碑应被清理: %d", n)
	}
	if n := reviewGCountRows(t, &models.Video{}, "id = ? AND deleted_at IS NOT NULL AND deleted_by = ?", video.ID, "scanner"); n != 1 {
		t.Fatalf("扫描器软删的记录不得被硬删: %d", n)
	}
	if n := reviewNVideoTagCount(t, video.ID); n != 1 {
		t.Fatalf("清理墓碑不得动记录的标签: %d", n)
	}

	// 4. 文件再回来：没有任何条目也按扫描器的缺失软删恢复原 ID，不算「用户删除」的屏蔽。
	putBack()
	got := scan("清理后文件回来")
	if got.Restored != 1 || got.SkipBreakdown.BlockedUserDelete != 0 {
		t.Fatalf("清理墓碑之后应照常自动恢复: %+v", got)
	}
	assertRestored("清理后文件回来")
}

func TestIMG02ScannerSoftDeleteUnderResidueTombstoneRestoresOriginalIDFixNI1(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	const content = "legacy-image-residue-n1"
	image, tomb, unlockTrashDir := reviewNResidueTombstoneImage(t, svc, root, content)
	trashDir := filepath.Dir(tomb.TrashPath)
	tag := models.Tag{Name: "修复N保留的图片标签"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.Image{ID: image.ID}).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	sync := func(step string) *ImageScanResult {
		t.Helper()
		result := imageTestMustSync(t, svc)
		if len(result.Errors) != 0 || result.Added != 0 {
			t.Fatalf("%s：图片同步不得报错、不得新建: %+v", step, result)
		}
		return result
	}
	putBack := func() {
		t.Helper()
		if err := os.WriteFile(image.Path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	assertRestored := func(step string) {
		t.Helper()
		if n := reviewGCountRows(t, &models.Image{}, "id = ? AND deleted_at IS NULL", image.ID); n != 1 {
			t.Fatalf("%s：应恢复为原 ID 的活跃图片: %d", step, n)
		}
		if n := reviewNImageTagCount(t, image.ID); n != 1 {
			t.Fatalf("%s：图片标签应保留: %d", step, n)
		}
		if n := reviewGCountRows(t, &models.Image{}, "path = ?", image.Path); n != 1 {
			t.Fatalf("%s：同一路径只应有原来那一条图片记录: %d", step, n)
		}
	}

	// 1. 文件不见了：deleteMissingImageRecord 照常软删、不收尾、不报错，墓碑保留。
	if err := os.Remove(image.Path); err != nil {
		t.Fatal(err)
	}
	if got := sync("缺失软删"); got.Removed != 1 {
		t.Fatalf("扫描器应软删这张图片: %+v", got)
	}
	if n := reviewGCountRows(t, &models.Image{}, "id = ? AND deleted_at IS NOT NULL AND deleted_by = ? AND is_stale = ?", image.ID, "scanner", true); n != 1 {
		t.Fatalf("图片应被扫描器软删并带恢复标记: %d", n)
	}
	if n := reviewGCountRows(t, &models.ImageTrashEntry{}, "id = ? AND state = ?", tomb.ID, trashStateRemoved); n != 1 {
		t.Fatalf("图片墓碑必须保留: %d", n)
	}

	// 2. 文件回到原处：restoreStaleImage 恢复原 ID（修复前墓碑把这条行遮挡掉，同步会新建一张）。
	putBack()
	if got := sync("文件回来"); got.Restored != 1 {
		t.Fatalf("应自动恢复原图片: %+v", got)
	}
	assertRestored("文件回来")
	if n := reviewGCountRows(t, &models.ImageTrashEntry{}, "id = ? AND state = ?", tomb.ID, trashStateRemoved); n != 1 {
		t.Fatalf("恢复后图片墓碑仍在: %d", n)
	}

	// 3. 再次缺失；trash/ 目录消失后的启动清理只删墓碑，不硬删扫描器软删的图片。
	if err := os.Remove(image.Path); err != nil {
		t.Fatal(err)
	}
	if got := sync("再次缺失"); got.Removed != 1 {
		t.Fatalf("扫描器应再次软删: %+v", got)
	}
	unlockTrashDir()
	if err := os.RemoveAll(trashDir); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileImageTrashEntries(); err != nil {
		t.Fatalf("启动对账不应失败: %v", err)
	}
	if n := reviewGCountRows(t, &models.ImageTrashEntry{}, "id = ?", tomb.ID); n != 0 {
		t.Fatalf("目录已不存在的图片墓碑应被清理: %d", n)
	}
	if n := reviewGCountRows(t, &models.Image{}, "id = ? AND deleted_at IS NOT NULL AND deleted_by = ?", image.ID, "scanner"); n != 1 {
		t.Fatalf("扫描器软删的图片不得被硬删: %d", n)
	}

	// 4. 文件再回来：照常恢复原 ID。
	putBack()
	if got := sync("清理后文件回来"); got.Restored != 1 {
		t.Fatalf("清理墓碑之后应照常自动恢复: %+v", got)
	}
	assertRestored("清理后文件回来")
}

// 手动添加（AddVideo）遇到挂墓碑、被扫描器软删的同路径行：与扫描器的缺失软删同一判定（跳过，交给扫描自动恢复），
// 不按墓碑的旧版条目事实新建重复记录。
func TestLIB05AddVideoTreatsScannerRowUnderTombstoneAsMissingFixNI1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	const content = "legacy-residue-add"
	video, _, _ := reviewNResidueTombstoneVideo(t, svc, root, content)
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	if err := svc.deleteVideoRecordBy(video.ID, false, "scanner"); err != nil {
		t.Fatalf("扫描器软删挂墓碑的记录不得报错: %v", err)
	}
	if err := os.WriteFile(video.Path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddVideo(video.Path); !errors.Is(err, ErrVideoExists) || errors.Is(err, ErrVideoBlockedByUserDelete) {
		t.Fatalf("手动添加应跳过（交给扫描恢复原记录），不按用户删除屏蔽: %v", err)
	}
	if n := reviewGCountRows(t, &models.Video{}, "path = ?", video.Path); n != 1 {
		t.Fatalf("不得新建重复记录: %d", n)
	}
}

// 与扫描器 missing 条目的自动恢复同一口径：原路径上回来的文件大小与记录不同，不恢复、不新建，扫描计入 add 错误
// （文案不带路径）；墓碑与记录都不动。
func TestLIB05ScannerRowUnderTombstoneWithDifferentFileIsNotRestoredFixNI1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video, tomb, _ := reviewNResidueTombstoneVideo(t, svc, root, "legacy-residue-size")
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	if err := svc.deleteVideoRecordBy(video.ID, false, "scanner"); err != nil {
		t.Fatalf("扫描器软删挂墓碑的记录不得报错: %v", err)
	}
	if err := os.WriteFile(video.Path, []byte("a different, longer file"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSetFileModTime(t, video.Path, time.Now().Add(-2*time.Hour))

	scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if scan.Restored != 0 || scan.Added != 0 {
		t.Fatalf("大小不同的文件不得恢复原记录，也不得新建: %+v", scan)
	}
	found := false
	for _, scanErr := range scan.Errors {
		if scanErr.Operation == "add" && scanErr.Error != "" {
			found = true
			if strings.Contains(scanErr.Error, root) {
				t.Fatalf("错误文案不得带路径: %+v", scanErr)
			}
		}
	}
	if !found {
		t.Fatalf("应计入 add 错误: %+v", scan)
	}
	if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() || got.DeletedBy != "scanner" {
		t.Fatalf("记录保持扫描器软删: %#v", got)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ? AND state = ?", tomb.ID, trashStateRemoved); n != 1 {
		t.Fatalf("墓碑保留: %d", n)
	}
}

// ---------- I-2：收尾前先确认位置可用；离线 / 无权限时墓碑保留、删除被拒绝 ----------

func TestLIB05SettleTombstoneRefusesWhenLocationUnavailableFixNI2(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	const content = "legacy-residue-offline"
	video, tomb, unlockTrashDir := reviewNResidueTombstoneVideo(t, svc, root, content)
	// 残留名字此后可以清理：下面的拒绝只能来自位置可用性判定。
	unlockTrashDir()
	trashDir := filepath.Dir(tomb.TrashPath)
	permission := fmt.Errorf("读不到挂载点（替身）: %w", os.ErrPermission)
	for _, tc := range []struct {
		name   string
		cause  error
		prefix string
		want   error
		code   string
	}{
		{"trash_path 所在位置离线", nil, trashDir, ErrTrashVolumeOffline, TrashResultVolumeOffline},
		{"记录当前路径所在位置离线", nil, video.Path, ErrTrashVolumeOffline, TrashResultVolumeOffline},
		{"trash_path 所在位置无权限", permission, trashDir, ErrTrashPermissionDenied, TrashResultPermissionDenied},
	} {
		restore := reviewNStubOffline(t, tc.cause, tc.prefix)
		if err := svc.DeleteVideo(video.ID, true); !errors.Is(err, tc.want) {
			t.Fatalf("%s：删除应被拒绝并报 %v: %v", tc.name, tc.want, err)
		}
		permanent := svc.PermanentlyDeleteVideos([]uint{video.ID})
		if permanent.Succeeded != 0 || permanent.Items[0].Code != tc.code {
			t.Fatalf("%s：永久删除应被拒绝（%s）: %#v", tc.name, tc.code, permanent)
		}
		restore()
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

	// 对照：位置可用时照常收尾并删除。
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatalf("位置可用时应能收尾并删除: %v", err)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", tomb.ID); n != 0 {
		t.Fatalf("收尾后墓碑应被删掉: %d", n)
	}
}

func TestIMG02SettleTombstoneRefusesWhenLocationUnavailableFixNI2(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	image, tomb, unlockTrashDir := reviewNResidueTombstoneImage(t, svc, root, "legacy-image-offline")
	unlockTrashDir()
	restore := reviewNStubOffline(t, nil, filepath.Dir(tomb.TrashPath))
	if err := svc.DeleteImage(image.ID, true); !errors.Is(err, ErrTrashVolumeOffline) {
		t.Fatalf("trash_path 所在位置离线时删除图片应被拒绝: %v", err)
	}
	if permanent := svc.PermanentlyDeleteImages([]uint{image.ID}); permanent.Succeeded != 0 || permanent.Items[0].Code != TrashResultVolumeOffline {
		t.Fatalf("离线时永久删除图片应被拒绝: %#v", permanent)
	}
	restore()
	if n := reviewGCountRows(t, &models.ImageTrashEntry{}, "id = ? AND state = ?", tomb.ID, trashStateRemoved); n != 1 {
		t.Fatalf("图片墓碑必须保留: %d", n)
	}
	if !reviewDSameInode(t, image.Path, tomb.TrashPath) {
		t.Fatal("图片残留名字与原文件都不得被动")
	}
	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatalf("位置可用时应能收尾并删除图片: %v", err)
	}
}

// ---------- m1：残留判定用记录的当前路径；残留不是当前文件的另一个名字时单独给文案 ----------

func TestLIB05SettleTombstoneUsesCurrentPathAfterMoveFixNM1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video, tomb, unlockTrashDir := reviewNResidueTombstoneVideo(t, svc, root, "legacy-residue-moved")
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

	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatalf("移动之后应按当前路径收尾并删除: %v", err)
	}
	if _, err := os.Lstat(tomb.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("删除前应清掉残留名字: %v", err)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", tomb.ID); n != 0 {
		t.Fatalf("墓碑应被删掉: %d", n)
	}
	if got := p010VideoEntry(t, video.ID); got.Mode != models.TrashModeTrash || got.State != trashStateDeleted || got.OriginalPath != moved.Path {
		t.Fatalf("应按当前路径建出新的回收站条目: %#v", got)
	}
}

func TestIMG02SettleTombstoneUsesCurrentPathAfterRelocateFixNM1(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	image, tomb, unlockTrashDir := reviewNResidueTombstoneImage(t, svc, root, "legacy-image-relocated")
	unlockTrashDir()
	relocated := filepath.Join(root, "pics", "renamed.jpg")
	if err := os.Rename(image.Path, relocated); err != nil {
		t.Fatal(err)
	}
	if err := svc.relocateImage(image.ID, relocated); err != nil {
		t.Fatalf("前提：重定位应成功: %v", err)
	}
	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatalf("重定位之后应按当前路径收尾并删除图片: %v", err)
	}
	if _, err := os.Lstat(tomb.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("删除前应清掉图片的残留名字: %v", err)
	}
	if got := p010ImageEntry(t, image.ID); got.Mode != models.TrashModeTrash || got.OriginalPath != relocated {
		t.Fatalf("应按当前路径建出新的图片回收站条目: %#v", got)
	}
}

// m1 / m3：残留名字还在、但不是当前文件的另一个名字（另一个文件）——单独的文案，不说成权限问题；墓碑保留、删除被拒绝。
func TestLIB05SettleTombstoneRefusesUnconfirmedResidueFixNM1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	const content = "legacy-residue-unconfirmed"
	video, tomb, unlockTrashDir := reviewNResidueTombstoneVideo(t, svc, root, content)
	unlockTrashDir()
	// 残留名字换成另一个文件：不是当前文件的硬链接。
	if err := os.Remove(tomb.TrashPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tomb.TrashPath, []byte("another-file"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := svc.DeleteVideo(video.ID, true)
	if !errors.Is(err, errTrashTombstoneResidueUnconfirmed) || errors.Is(err, errTrashRestoreResidueRemains) || errors.Is(err, os.ErrPermission) {
		t.Fatalf("非硬链接残留应单独报「无法确认的文件」，不说成权限问题: %v", err)
	}
	if strings.Contains(err.Error(), root) || !strings.Contains(err.Error(), "访达") {
		t.Fatalf("文案不带路径，并提示在访达里检查该文件夹: %v", err)
	}
	permanent := svc.PermanentlyDeleteVideos([]uint{video.ID})
	if permanent.Succeeded != 0 || permanent.Items[0].Code != TrashResultError || permanent.Items[0].Message != errTrashTombstoneResidueUnconfirmed.Error() {
		t.Fatalf("永久删除同样拒绝，结果码 error 并带这条文案: %#v", permanent)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ? AND state = ?", tomb.ID, trashStateRemoved); n != 1 {
		t.Fatalf("墓碑必须保留: %d", n)
	}
	if got := reviewGFileContent(t, tomb.TrashPath); got != "another-file" {
		t.Fatalf("无法确认的文件不得被动: %q", got)
	}
	if got := reviewGFileContent(t, video.Path); got != content {
		t.Fatalf("原文件不得被动: %q", got)
	}
	if got := p011ReloadVideo(t, video.ID); got.DeletedAt.IsValid() {
		t.Fatalf("记录保持活跃: %#v", got)
	}
}

// ---------- m2：墓碑清理只在包含它、且在线的扫描根存在时进行 ----------

func TestLIB05SweepKeepsTombstonesWithoutOnlineScanRootFixNM2(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	center := NewTrashCenter(svc, nil)
	online, outside, offline := t.TempDir(), t.TempDir(), t.TempDir()
	for _, root := range []string{online, offline} {
		if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
			t.Fatal(err)
		}
	}
	tombstone := func(base string) (models.Video, models.VideoTrashEntry) {
		t.Helper()
		video, entry := reviewIOldLegacyVideo(t, filepath.Join(base, "movies", "movie.mp4"), "legacy-"+filepath.Base(base))
		if result, err := center.ForceRemoveTrashRecords("video", []uint{entry.ID}, TrashForceRemoveConfirmText); err != nil || result.Succeeded != 1 {
			t.Fatalf("前提：legacy 行移除后成为墓碑: %#v err=%v", result, err)
		}
		if err := os.RemoveAll(filepath.Dir(entry.TrashPath)); err != nil {
			t.Fatal(err)
		}
		return video, entry
	}
	onlineVideo, onlineEntry := tombstone(online)
	outsideVideo, outsideEntry := tombstone(outside)
	offlineVideo, offlineEntry := tombstone(offline)
	reviewNStubOffline(t, nil, offline)

	if err := svc.ReconcileTrashEntries(); err != nil {
		t.Fatalf("启动对账不应失败: %v", err)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", onlineEntry.ID); n != 0 {
		t.Fatalf("在线扫描根里、目录已不存在的墓碑应被清理: %d", n)
	}
	if n := reviewGCountRows(t, &models.Video{}, "id = ?", onlineVideo.ID); n != 0 {
		t.Fatalf("用户删除的墓碑记录一并硬删: %d", n)
	}
	for _, kept := range []struct {
		entry models.VideoTrashEntry
		video models.Video
		why   string
	}{{outsideEntry, outsideVideo, "不属于任何扫描根"}, {offlineEntry, offlineVideo, "扫描根不可用"}} {
		if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ? AND state = ?", kept.entry.ID, trashStateRemoved); n != 1 {
			t.Fatalf("%s时墓碑必须保留: %d", kept.why, n)
		}
		if n := reviewGCountRows(t, &models.Video{}, "id = ? AND deleted_at IS NOT NULL", kept.video.ID); n != 1 {
			t.Fatalf("%s时记录保持原样: %d", kept.why, n)
		}
	}
}

// ---------- m3：测试缺口 ----------

// trash_path 为空的墓碑不登记任何目录、不涉及文件，不需要扫描根也照常清理；用户删除的记录一并硬删，
// 扫描器软删的记录只删条目（修复 N I-1）。
func TestLIB05SweepDropsTombstonesWithEmptyTrashPathFixNM3(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	userVideo, userEntry := reviewLTombstoneVideoAt(t, filepath.Join(root, "movies", "user.mp4"))
	scannerVideo, scannerEntry := reviewLTombstoneVideoAt(t, filepath.Join(root, "movies", "scanner.mp4"))
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", scannerVideo.ID).Update("deleted_by", "scanner").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("id IN ?", []uint{userEntry.ID, scannerEntry.ID}).
		Update("trash_path", "").Error; err != nil {
		t.Fatal(err)
	}

	if err := svc.ReconcileTrashEntries(); err != nil {
		t.Fatalf("启动对账不应失败: %v", err)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id IN ?", []uint{userEntry.ID, scannerEntry.ID}); n != 0 {
		t.Fatalf("trash_path 为空的墓碑应被清理: %d", n)
	}
	if n := reviewGCountRows(t, &models.Video{}, "id = ?", userVideo.ID); n != 0 {
		t.Fatalf("用户删除的墓碑记录一并硬删: %d", n)
	}
	if n := reviewGCountRows(t, &models.Video{}, "id = ? AND deleted_at IS NOT NULL", scannerVideo.ID); n != 1 {
		t.Fatalf("扫描器软删的记录只删条目、不硬删: %d", n)
	}
}

// 恢复事务的提交结果无法确认时：记录已活跃、条目是墓碑（恢复事务里改成的墓碑，修复 L m5）算已提交；
// 记录仍是软删、条目是墓碑既不算已提交也不算已回滚。
func TestLIB05ConfirmRestoreOutcomeActiveRecordWithTombstoneIsCommittedFixNM3(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := p010Video(t, filepath.Join(t.TempDir(), "movies", "restored.mp4"), "restored")
	entry := models.VideoTrashEntry{
		DeletedBy: "user", VideoID: video.ID, VideoName: video.Name, OriginalPath: video.Path,
		TrashPath: filepath.Join(video.Directory, DefaultTrashDirName, video.Name), FileMoved: true, FileSize: video.Size,
		State: trashStateRemoved, Mode: models.TrashModeLegacyTrash,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	committed, rolledBack, err := confirmRestoreTransactionOutcome(video.ID, entry.ID)
	if err != nil || !committed || rolledBack {
		t.Fatalf("记录活跃 + 条目是墓碑应算已提交: committed=%v rolledBack=%v err=%v", committed, rolledBack, err)
	}
	if err := database.DB.Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	committed, rolledBack, err = confirmRestoreTransactionOutcome(video.ID, entry.ID)
	if err != nil || committed || rolledBack {
		t.Fatalf("记录软删 + 条目是墓碑不得算已提交或已回滚: committed=%v rolledBack=%v err=%v", committed, rolledBack, err)
	}
}

func TestIMG02ConfirmRestoreOutcomeActiveRecordWithTombstoneIsCommittedFixNM3(t *testing.T) {
	setupImageServiceTestDB(t)
	image := imageTrashTestCreateImage(t, filepath.Join(t.TempDir(), "pics", "restored.jpg"), "restored-image")
	entry := models.ImageTrashEntry{
		DeletedBy: "user", ImageID: image.ID, ImageName: image.Name, OriginalPath: image.Path,
		TrashPath: filepath.Join(image.Directory, DefaultTrashDirName, image.Name), FileMoved: true, FileSize: image.Size,
		State: trashStateRemoved, Mode: models.TrashModeLegacyTrash,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	committed, rolledBack, err := confirmImageRestoreTransactionOutcome(image.ID, entry.ID)
	if err != nil || !committed || rolledBack {
		t.Fatalf("图片记录活跃 + 条目是墓碑应算已提交: committed=%v rolledBack=%v err=%v", committed, rolledBack, err)
	}
	if err := database.DB.Delete(image).Error; err != nil {
		t.Fatal(err)
	}
	committed, rolledBack, err = confirmImageRestoreTransactionOutcome(image.ID, entry.ID)
	if err != nil || committed || rolledBack {
		t.Fatalf("图片记录软删 + 条目是墓碑不得算已提交或已回滚: committed=%v rolledBack=%v err=%v", committed, rolledBack, err)
	}
}
