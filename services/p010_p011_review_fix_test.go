package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"github.com/fsnotify/fsnotify"
	"gorm.io/gorm"
)

// P-010 / P-011 独立评审问题（C1、I1~I6、Minor）的回归测试。测试名里的 LIBxx / IMGxx 是问题清单 ID。
// 测试进程里的系统废纸篓是替身（system_trash_testhook_test.go）：同卷重命名进「原目录/.Trash」。

// p010LegacyEraDeletedAt 落在旧版应用「移进 trash/ 却不建条目」的时间段（2026-04-16 至 2026-07-30）内。
var p010LegacyEraDeletedAt = time.Date(2026, 5, 20, 10, 30, 0, 0, time.Local)

// 一个视频已被旧版应用软删（没有回收站条目、deleted_at 在旧版时间段内），模拟旧版删除后的库状态。
func p010SoftDeletedVideoIn(t *testing.T, dir, name string) models.Video {
	t.Helper()
	video := models.Video{Name: name, Path: filepath.Join(dir, name), Directory: dir, Size: 7}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", video.ID).Update("deleted_at", p010LegacyEraDeletedAt).Error; err != nil {
		t.Fatal(err)
	}
	return video
}

// ---------- C1：旧版无条目 trash/ 目录不被重新收录 ----------

func TestLIB05LegacyTrashDirWithoutEntriesIsNotReimportedC1(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	mediaDir := filepath.Join(root, "movies")
	legacyFile := filepath.Join(mediaDir, "trash", "old-deleted.mp4")
	nested := filepath.Join(mediaDir, "trash", "sub", "nested.mp4")
	keep := filepath.Join(mediaDir, "keep.mp4")
	for _, path := range []string{legacyFile, nested, keep} {
		createOldVideoFile(t, path)
	}
	// 旧版应用删除过一个视频：库里只剩软删行（旧版时间段内），没有回收站条目，文件在 <目录>/trash/ 里。
	p010SoftDeletedVideoIn(t, mediaDir, "old-deleted.mp4")
	// 父目录从无删除记录的 trash、以及用户自己的 Trash（大写）照常扫描。
	plainTrash := filepath.Join(root, "other", "trash", "plain.mp4")
	userTrash := filepath.Join(root, "third", "Trash", "user.mp4")
	createOldVideoFile(t, plainTrash)
	createOldVideoFile(t, userTrash)

	result := (&VideoService{}).SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if result.Added != 3 {
		t.Fatalf("应只收录 keep / plain / user 三个文件: %+v", result)
	}
	var count int64
	if err := database.DB.Model(&models.Video{}).Where("path IN ?", []string{legacyFile, nested}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("旧版 trash/ 里的文件不能入库: count=%d err=%v", count, err)
	}
	if result.SkipBreakdown.LegacyTrash != 2 {
		t.Fatalf("旧版回收站里的两个视频应计入 legacy_trash: %+v", result.SkipBreakdown)
	}
	if result.Skipped != result.SkipBreakdown.Total() {
		t.Fatalf("skipped 必须等于各项之和: %+v", result)
	}
	if !isTrashPath(legacyFile) || !isTrashDir(filepath.Join(mediaDir, "trash")) {
		t.Fatal("监听与扫描应共用同一判定：旧版 trash/ 目录按回收站处理")
	}
	if isTrashPath(plainTrash) || isTrashPath(userTrash) {
		t.Fatal("父目录无删除记录的 trash、用户的 Trash 不是回收站")
	}
}

func TestIMG02LegacyTrashDirUnderSoftDeletedImageDirectoryIsSkippedC1(t *testing.T) {
	setupImageServiceTestDB(t)
	root := t.TempDir()
	image := imageTrashTestCreateImage(t, filepath.Join(root, "pics", "a.jpg"), "a")
	if err := database.DB.Delete(image).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Unscoped().Model(&models.Image{}).Where("id = ?", image.ID).Update("deleted_at", p010LegacyEraDeletedAt).Error; err != nil {
		t.Fatal(err)
	}
	// 旧版应用把 a.jpg 移进了 pics/trash/，同名冲突时带时间戳：a_20260520103000.jpg。
	if err := os.MkdirAll(filepath.Join(root, "pics", "trash"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(image.Path, filepath.Join(root, "pics", "trash", "a_20260520103000.jpg")); err != nil {
		t.Fatal(err)
	}
	refreshLegacyTrashDirs()
	if !isTrashDir(filepath.Join(root, "pics", "trash")) || !isTrashPath(filepath.Join(root, "pics", "trash", "b.jpg")) {
		t.Fatal("图片所在目录下、名为 trash 的目录应按旧版回收站处理")
	}
	// 区分大小写：只认恰为 trash 的目录名。判定基于路径字符串，与文件系统是否区分大小写无关。
	if isTrashDir(filepath.Join(root, "pics", "Trash")) {
		t.Fatal("大写 Trash 不应被认作旧版回收站")
	}
}

// LIB14：跳过分项按类别断言，而不仅是总和。
func TestScanSkipBreakdownPerCategoryLIB14(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("video_extensions", ".mp4,.ts").Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	mediaDir := filepath.Join(root, "lib")
	createOldVideoFile(t, filepath.Join(mediaDir, "fresh.mp4"))
	createOldVideoFile(t, filepath.Join(mediaDir, "downloading.tmp.mp4"))
	mustCreateFile(t, filepath.Join(mediaDir, "recent.mp4"))
	createOldVideoFile(t, filepath.Join(mediaDir, "trash", "legacy.mp4"))
	source := filepath.Join(mediaDir, "code.ts")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("export const answer = 42\nimport x from 'y'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSetFileModTime(t, source, time.Now().Add(-time.Hour))
	// 旧版应用删除过 legacy.mp4（软删、无条目、旧版时间段内），文件在 trash/ 里。
	p010SoftDeletedVideoIn(t, mediaDir, "legacy.mp4")
	// 用户只删了记录、文件仍在原处（大小与 mtime 都没变）。
	blocked := p010Video(t, filepath.Join(mediaDir, "blocked.mp4"), "blocked-content")
	mustSetFileModTime(t, blocked.Path, time.Now().Add(-time.Hour))
	if err := (&VideoService{}).DeleteVideo(blocked.ID, false); err != nil {
		t.Fatal(err)
	}

	result := (&VideoService{}).SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	got := result.SkipBreakdown
	if got.TempFile != 1 || got.RecentlyModified != 1 || got.NotVideo != 1 || got.LegacyTrash != 1 || got.BlockedUserDelete != 1 || got.ReadError != 0 {
		t.Fatalf("每个类别都应精确计数: %+v", got)
	}
	if result.Skipped != got.Total() || result.Skipped != 5 {
		t.Fatalf("skipped 应等于各项之和（5）: skipped=%d %+v", result.Skipped, got)
	}
}

// ---------- I1：卷离线不被当成文件已消失 ----------

func TestLIB05ListKeepsStateWhenVolumeOfflineI1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	mount := filepath.Join(t.TempDir(), "mnt")
	video := p010Video(t, filepath.Join(mount, "movies", "a.mp4"), "content-a")
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	// 卷被拔掉：卷挂载检查报告未挂载（替身，I-2），卷上的文件读不到（删掉废纸篓里那份来模拟）。
	remount := reviewAUnmountVolume(t, mount)
	if err := os.Remove(entry.TrashPath); err != nil {
		t.Fatal(err)
	}
	center := NewTrashCenter(svc, nil)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 1 || page.Items[0].State != trashStateDeleted {
		t.Fatalf("卷离线时列表不能把条目改成 file_gone: %#v err=%v", page, err)
	}
	if got := p010VideoEntry(t, video.ID); got.State != trashStateDeleted {
		t.Fatalf("状态不应被改写: %#v", got)
	}
	// 卷回来了但文件确实不在：这才是 file_gone。
	remount()
	if page, err = center.ListTrashEntries(TrashFilter{Kind: "video"}); err != nil || page.Items[0].State != models.TrashStateFileGone {
		t.Fatalf("卷在线且文件不在应对账为 file_gone: %#v err=%v", page, err)
	}
	// 文件重新出现（身份一致）时允许把 file_gone 改回 deleted。
	if err := os.WriteFile(entry.TrashPath, []byte("content-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(entry.TrashPath, time.Unix(0, entry.FileModTime), time.Unix(0, entry.FileModTime)); err != nil {
		t.Fatal(err)
	}
	// 新写的文件 inode 与记录不同，仍应保持 file_gone（不能靠大小蒙混）。
	if page, err = center.ListTrashEntries(TrashFilter{Kind: "video"}); err != nil || page.Items[0].State != models.TrashStateFileGone {
		t.Fatalf("身份不一致的文件不能让 file_gone 复活: %#v err=%v", page, err)
	}
}

func TestLIB05CrashRecoveryKeepsPendingWhenVolumeOfflineI1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	mount := filepath.Join(t.TempDir(), "mnt")
	video := p010Video(t, filepath.Join(mount, "a.mp4"), "content-a")
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
	// 卷被拔掉：挂载检查报告未挂载（替身，I-2），原路径上的文件读不到。
	remount := reviewAUnmountVolume(t, mount)
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileTrashEntries(); err == nil {
		t.Fatal("卷离线时对账应报告无法处理，而不是落库")
	}
	if got := p010VideoEntry(t, video.ID); got.State != trashStatePendingMove || got.LastError == trashUnknownLocationMessage {
		t.Fatalf("卷离线时 pending_move 必须保持原状: %#v", got)
	}
	if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("记录必须仍然活跃: %v", err)
	}
	// 卷回来了：文件确实找不到，落到分支 3。
	remount()
	if err := svc.ReconcileTrashEntries(); err != nil {
		t.Fatal(err)
	}
	if got := p010VideoEntry(t, video.ID); got.State != trashStateDeleted || got.LastError != trashUnknownLocationMessage {
		t.Fatalf("卷在线后应落入分支 3: %#v", got)
	}
}

func TestIMG02CrashRecoveryKeepsPendingWhenVolumeOfflineI1(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	mount := filepath.Join(t.TempDir(), "mnt")
	image := imageTrashTestCreateImage(t, filepath.Join(mount, "a.jpg"), "content-a")
	info, err := os.Stat(image.Path)
	if err != nil {
		t.Fatal(err)
	}
	entry := models.ImageTrashEntry{
		DeletedBy: "user", ImageID: image.ID, ImageName: image.Name, OriginalPath: image.Path,
		FileSize: info.Size(), FileModTime: info.ModTime().UnixNano(), FileIdentity: stableFileIdentity(info),
		State: trashStatePendingMove, Mode: models.TrashModeTrash, DeleteBatchID: newDeleteBatchID(),
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	// 卷被拔掉：挂载检查报告未挂载（替身，I-2），原路径上的文件读不到。
	reviewAUnmountVolume(t, mount)
	if err := os.Remove(image.Path); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileImageTrashEntries(); err == nil {
		t.Fatal("卷离线时对账应报告无法处理")
	}
	if got := p010ImageEntry(t, image.ID); got.State != trashStatePendingMove {
		t.Fatalf("卷离线时 pending_move 必须保持原状: %#v", got)
	}
	if err := database.DB.First(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("图片必须仍然活跃: %v", err)
	}
}

func TestLIB11StagedListingKeepsPendingWhenVolumeOfflineI1(t *testing.T) {
	setupVideoServiceTestDB(t)
	mount := filepath.Join(t.TempDir(), "mnt")
	// 卷未挂载（替身，I-2）：只凭「目录不存在」不再算离线。
	reviewAUnmountVolume(t, mount)
	onlineRoot := t.TempDir()
	offlineStaged := filepath.Join(mount, ".a.mp4.cineinsight-migrating-aaa")
	goneStaged := filepath.Join(onlineRoot, ".b.mp4.cineinsight-migrating-bbb")
	rows := []models.MigrationStagedSource{
		{OriginalPath: filepath.Join(mount, "a.mp4"), StagedPath: offlineStaged, Size: 1, State: models.MigrationStagedStatePending},
		{OriginalPath: filepath.Join(onlineRoot, "b.mp4"), StagedPath: goneStaged, Size: 1, State: models.MigrationStagedStatePending},
	}
	for i := range rows {
		if err := database.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	listed, err := (&VideoService{}).ListStagedSources()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != rows[0].ID {
		t.Fatalf("离线卷上的残留应保持 pending 并继续列出，在线卷上已消失的应置 cleaned: %#v", listed)
	}
	var offlineRow, goneRow models.MigrationStagedSource
	database.DB.First(&offlineRow, rows[0].ID)
	database.DB.First(&goneRow, rows[1].ID)
	if offlineRow.State != models.MigrationStagedStatePending || goneRow.State != models.MigrationStagedStateCleaned {
		t.Fatalf("状态不符: offline=%s gone=%s", offlineRow.State, goneRow.State)
	}
	// 移入废纸篓与删除同样不能把离线卷上的残留当成「已清理」。
	result := NewTrashCenter(nil, nil).TrashStagedSources([]uint{rows[0].ID})
	if result.Items[0].Code != TrashResultVolumeOffline {
		t.Fatalf("离线卷上的残留应返回 volume_offline: %#v", result)
	}
	deleted, err := (&VideoService{}).DeleteStagedSources([]uint{rows[0].ID})
	if err != nil || deleted.Cleaned != 0 || len(deleted.Failed) != 1 {
		t.Fatalf("离线卷上的残留不能被当成已清理: %#v err=%v", deleted, err)
	}
	database.DB.First(&offlineRow, rows[0].ID)
	if offlineRow.State != models.MigrationStagedStatePending {
		t.Fatalf("状态不应被改写: %s", offlineRow.State)
	}
}

// ---------- I2：身份核对不依赖设备号 ----------

func TestLIB05IdentityMatchIgnoresDeviceNumberI2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.mp4")
	if err := os.WriteFile(path, []byte("identity"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	real := trashFileIDOf(info)
	if real.Identity == "" {
		t.Skip("当前平台没有稳定文件身份")
	}
	otherDevice := real
	otherDevice.Identity = "999999:" + identityInode(real.Identity)
	if !otherDevice.strictMatch(info) {
		t.Fatal("设备号变了（外置盘重插）但 inode、大小、mtime 都没变，应视为同一文件")
	}
	otherInode := real
	otherInode.Identity = identityInode(real.Identity) + "1"
	if otherInode.strictMatch(info) || (trashFileID{Size: real.Size, ModTimeNS: real.ModTimeNS, Identity: "1:" + otherInode.Identity}).strictMatch(info) {
		t.Fatal("inode 不同必须判为不符")
	}
	if !legacyTrashFileMatches(path, info, otherDevice, "") {
		t.Fatal("旧行的强身份同样只比 inode")
	}

	// 端到端：清除时条目里记录的设备号与现在不同，仍能核对通过。
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video := p010Video(t, filepath.Join(t.TempDir(), "b.mp4"), "content-b")
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", entry.ID).
		Update("file_identity", "424242:"+identityInode(entry.FileIdentity)).Error; err != nil {
		t.Fatal(err)
	}
	result, err := NewTrashCenter(svc, nil).PurgeTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("设备号不同不应阻止清除: %#v err=%v", result, err)
	}
}

// ---------- I3：访达「放回原处」 ----------

func p010PutBackFixture(t *testing.T) (*VideoService, models.Video, models.VideoTrashEntry, models.Tag) {
	t.Helper()
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	path := filepath.Join(root, "putback.mp4")
	video := p010Video(t, path, "put-back-content")
	mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	tag := models.Tag{Name: "keep-me"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&video).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	// 用户在访达里选「放回原处」：重命名回去，inode、大小、mtime 都不变。
	if err := os.Rename(entry.TrashPath, video.Path); err != nil {
		t.Fatal(err)
	}
	return svc, video, entry, tag
}

func TestLIB05FinderPutBackRestoresOriginalRecordOnListI3(t *testing.T) {
	svc, video, entry, tag := p010PutBackFixture(t)
	center := NewTrashCenter(svc, nil)
	// 复审 Minor 1：列表只判定「可恢复」，不在列表路径上恢复（不拿全局路径写锁）。
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("放回原处的条目应仍在列表里并标为可恢复: %#v err=%v", page, err)
	}
	item := page.Items[0]
	if !item.PutBack || item.State != trashStateDeleted || len(item.Actions) != 1 || item.Actions[0] != TrashActionRestore {
		t.Fatalf("应报告 put_back 且只提供恢复: %#v", item)
	}
	if err := database.DB.First(&models.Video{}, video.ID).Error; err == nil {
		t.Fatal("列表不应自行恢复记录")
	}
	// 显式恢复：只还原数据库，文件不动。
	result, err := center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("显式恢复应成功: %#v err=%v", result, err)
	}
	var restored models.Video
	if err := database.DB.Preload("Tags").First(&restored, video.ID).Error; err != nil {
		t.Fatalf("原记录应被恢复（原 ID）: %v", err)
	}
	if len(restored.Tags) != 1 || restored.Tags[0].ID != tag.ID {
		t.Fatalf("标签应保留: %#v", restored.Tags)
	}
	var count int64
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", entry.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("条目应按恢复成功语义移除: %d err=%v", count, err)
	}
	if _, err := os.Stat(video.Path); err != nil {
		t.Fatalf("文件不应被动: %v", err)
	}
}

func TestLIB05FinderPutBackRestoresOriginalRecordOnScanI3(t *testing.T) {
	svc, video, entry, tag := p010PutBackFixture(t)
	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: filepath.Dir(video.Path)}})
	if result.Added != 0 || result.Restored != 1 {
		t.Fatalf("扫描应识别放回原处并恢复原记录，而不是新建: %+v", result)
	}
	var restored models.Video
	if err := database.DB.Preload("Tags").First(&restored, video.ID).Error; err != nil || len(restored.Tags) != 1 || restored.Tags[0].ID != tag.ID {
		t.Fatalf("原 ID 与标签应保留: %#v err=%v", restored, err)
	}
	var total int64
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("path = ?", video.Path).Count(&total).Error; err != nil || total != 1 {
		t.Fatalf("同路径只应有一条记录: %d err=%v", total, err)
	}
	var gone int64
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", entry.ID).Count(&gone).Error; err != nil || gone != 0 {
		t.Fatalf("条目应移除: %d err=%v", gone, err)
	}
}

// 列表先把条目标成 file_gone（此时文件还没放回），之后放回：扫描仍应识别。
func TestLIB05FinderPutBackAfterFileGoneMarkI3(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	path := filepath.Join(t.TempDir(), "late.mp4")
	video := p010Video(t, path, "late-content")
	mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	held := entry.TrashPath + ".held"
	if err := os.Rename(entry.TrashPath, held); err != nil {
		t.Fatal(err)
	}
	center := NewTrashCenter(svc, nil)
	if page, err := center.ListTrashEntries(TrashFilter{Kind: "video"}); err != nil || page.Items[0].State != models.TrashStateFileGone {
		t.Fatalf("文件暂时不在时应为 file_gone: %#v err=%v", page, err)
	}
	if err := os.Rename(held, path); err != nil {
		t.Fatal(err)
	}
	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: filepath.Dir(path)}})
	if result.Restored != 1 || result.Added != 0 {
		t.Fatalf("file_gone 的条目在文件放回后也应恢复原记录: %+v", result)
	}
}

func TestIMG02FinderPutBackRestoresOriginalImageOnScanI3(t *testing.T) {
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
	result, err := svc.SyncImageDirectories()
	if err != nil || result.Added != 0 || result.Restored != 1 {
		t.Fatalf("图片扫描应识别放回原处并恢复原记录: %#v err=%v", result, err)
	}
	if err := database.DB.First(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("原图片记录应被恢复: %v", err)
	}
	var gone int64
	if err := database.DB.Model(&models.ImageTrashEntry{}).Where("id = ?", entry.ID).Count(&gone).Error; err != nil || gone != 0 {
		t.Fatalf("条目应移除: %d err=%v", gone, err)
	}
}

func TestLIB04DecideSoftDeletedPathPutBackAndHistoricRowsI3(t *testing.T) {
	trashFacts := &softDeletedEntryFacts{Mode: models.TrashModeTrash, State: trashStateDeleted, FileSize: 5, FileModTime: 9, FileIdentity: "1:77"}
	cases := []struct {
		name     string
		entry    *softDeletedEntryFacts
		row      int64
		size     int64
		mtime    int64
		identity string
		want     softDeletedPathAction
	}{
		{"trash 身份一致 放回原处", trashFacts, 5, 5, 9, "2:77", softDeletedPutBack}, // 设备号不同也算
		{"trash inode 不同 新建", trashFacts, 5, 5, 9, "1:78", softDeletedCreateNew},
		{"trash 大小不同 新建", trashFacts, 5, 6, 9, "1:77", softDeletedCreateNew},
		{"trash mtime 不同 新建", trashFacts, 5, 5, 10, "1:77", softDeletedCreateNew},
		{"trash 读不到身份 新建", trashFacts, 5, 5, 9, "", softDeletedCreateNew},
		// 修复 G I-1：legacy_trash 旧行也按大小 + inode 判定放回（不比 mtime），扫描恢复原记录而不是新建。
		{"legacy_trash 大小 + inode 一致 放回原处", &softDeletedEntryFacts{Mode: models.TrashModeLegacyTrash, State: trashStateDeleted, FileSize: 5, FileModTime: 9, FileIdentity: "1:77"}, 5, 5, 123, "2:77", softDeletedPutBack},
		{"legacy_trash inode 不同 新建", &softDeletedEntryFacts{Mode: models.TrashModeLegacyTrash, State: trashStateDeleted, FileSize: 5, FileIdentity: "1:77"}, 5, 5, 9, "1:78", softDeletedCreateNew},
		{"legacy_trash 大小不同 新建", &softDeletedEntryFacts{Mode: models.TrashModeLegacyTrash, State: trashStateDeleted, FileSize: 5, FileIdentity: "1:77"}, 5, 6, 9, "1:77", softDeletedCreateNew},
		{"legacy_trash 没记录身份 新建", &softDeletedEntryFacts{Mode: models.TrashModeLegacyTrash, State: trashStateDeleted, FileSize: 5}, 5, 5, 9, "1:77", softDeletedCreateNew},
		{"legacy_trash 原路径是符号链接（身份为空） 新建", &softDeletedEntryFacts{Mode: models.TrashModeLegacyTrash, State: trashStateDeleted, FileSize: 5, FileIdentity: "1:77"}, 5, 5, 9, "", softDeletedCreateNew},
		{"回填前 mode 为空 file_moved 放回原处", &softDeletedEntryFacts{State: trashStateDeleted, FileMoved: true, FileSize: 5, FileIdentity: "1:77"}, 5, 5, 9, "1:77", softDeletedPutBack},
		{"回填前 mode 为空 未移动 新建", &softDeletedEntryFacts{State: trashStateDeleted, FileSize: 5, FileIdentity: "1:77"}, 5, 5, 9, "1:77", softDeletedCreateNew},
		{"record_only 从未移动 不认放回", &softDeletedEntryFacts{Mode: models.TrashModeRecordOnly, DeleteBatchID: "b", FileSize: 5, FileModTime: 9, FileIdentity: "1:77"}, 5, 5, 9, "1:77", softDeletedBlocked},
		// Minor 3：mtime=0 只比大小的兜底仅对新时代行生效。
		{"record_only 新时代 mtime=0 大小相同 屏蔽", &softDeletedEntryFacts{Mode: models.TrashModeRecordOnly, DeleteBatchID: "b", FileSize: 5}, 9, 5, 123, "", softDeletedBlocked},
		{"record_only 新时代 mtime=0 大小不同 新建", &softDeletedEntryFacts{Mode: models.TrashModeRecordOnly, DeleteBatchID: "b", FileSize: 5}, 5, 6, 123, "", softDeletedCreateNew},
		{"record_only 历史回填行 只比记录大小 相同则屏蔽", &softDeletedEntryFacts{Mode: models.TrashModeRecordOnly, FileSize: 0}, 5, 5, 123, "", softDeletedBlocked},
		{"record_only 历史回填行 记录大小不同 新建（即使条目大小相同）", &softDeletedEntryFacts{Mode: models.TrashModeRecordOnly, FileSize: 6}, 5, 6, 123, "", softDeletedCreateNew},
	}
	for _, tc := range cases {
		if got := decideSoftDeletedPath(tc.entry, tc.row, tc.size, tc.mtime, tc.identity); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

// ---------- I4：TrashStagedSources 允许目录 ----------

func TestLIB11TrashStagedSourcesAcceptsDirectoryI4(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	staged := filepath.Join(root, ".Season 1.cineinsight-migrating-abc")
	if err := os.MkdirAll(filepath.Join(staged, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "sub", "e01.mp4"), []byte("episode"), 0o644); err != nil {
		t.Fatal(err)
	}
	row := models.MigrationStagedSource{OriginalPath: filepath.Join(root, "Season 1"), StagedPath: staged, Size: 7, State: models.MigrationStagedStatePending}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	result := NewTrashCenter(nil, nil).TrashStagedSources([]uint{row.ID})
	if result.Succeeded != 1 || result.Items[0].Code != TrashResultOK {
		t.Fatalf("目录形态的暂存源应能移入废纸篓: %#v", result)
	}
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("暂存目录应被移走: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".Trash", filepath.Base(staged), "sub", "e01.mp4")); err != nil {
		t.Fatalf("整个目录应在（替身）废纸篓里: %v", err)
	}
	var got models.MigrationStagedSource
	if err := database.DB.First(&got, row.ID).Error; err != nil || got.State != models.MigrationStagedStateCleaned {
		t.Fatalf("应置 cleaned: %#v err=%v", got, err)
	}
}

// ---------- I5：永久删除只接受活跃且无条目的记录 ----------

func TestLIB04PermanentDeleteRejectsSoftDeletedAndEntryHoldersI5(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	// 已软删（有条目）：文件在废纸篓里，永久删除必须拒绝，文件与记录都不动。
	trashed := p010Video(t, filepath.Join(root, "trashed.mp4"), "trashed-content")
	if err := svc.DeleteVideo(trashed.ID, true); err != nil {
		t.Fatal(err)
	}
	// 只删记录（软删 + record_only 条目）：原路径上的文件同样不能被永久删除掉。
	recordOnly := p010Video(t, filepath.Join(root, "record-only.mp4"), "record-only-content")
	if err := svc.DeleteVideo(recordOnly.ID, false); err != nil {
		t.Fatal(err)
	}
	// 活跃但带着 pending_move 条目（删除进行中）。
	pending := p010Video(t, filepath.Join(root, "pending.mp4"), "pending-content")
	if err := database.DB.Create(&models.VideoTrashEntry{DeletedBy: "user", VideoID: pending.ID, VideoName: pending.Name,
		OriginalPath: pending.Path, State: trashStatePendingMove, Mode: models.TrashModeTrash, DeleteBatchID: newDeleteBatchID()}).Error; err != nil {
		t.Fatal(err)
	}
	result := svc.PermanentlyDeleteVideos([]uint{trashed.ID, recordOnly.ID, pending.ID})
	if result.Succeeded != 0 || result.Failed != 3 {
		t.Fatalf("三种情形都应被拒绝: %#v", result)
	}
	for _, item := range result.Items {
		if item.Code != TrashResultNotPurgeable {
			t.Errorf("应返回 not_purgeable: %#v", item)
		}
	}
	if _, err := os.Stat(recordOnly.Path); err != nil {
		t.Fatalf("只删记录的文件不能被永久删除: %v", err)
	}
	if _, err := os.Stat(pending.Path); err != nil {
		t.Fatalf("删除进行中的文件不能被永久删除: %v", err)
	}
	entry := p010VideoEntry(t, trashed.ID)
	if _, err := os.Stat(entry.TrashPath); err != nil {
		t.Fatalf("废纸篓里的文件不能被永久删除: %v", err)
	}
	var kept int64
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id IN ?", []uint{trashed.ID, recordOnly.ID, pending.ID}).Count(&kept).Error; err != nil || kept != 3 {
		t.Fatalf("记录都应保留: %d err=%v", kept, err)
	}
}

func TestIMG02PermanentDeleteRejectsSoftDeletedAndEntryHoldersI5(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	trashed := imageTrashTestCreateImage(t, filepath.Join(root, "t.jpg"), "trashed")
	if err := svc.DeleteImage(trashed.ID, true); err != nil {
		t.Fatal(err)
	}
	recordOnly := imageTrashTestCreateImage(t, filepath.Join(root, "r.jpg"), "record-only")
	if err := svc.DeleteImage(recordOnly.ID, false); err != nil {
		t.Fatal(err)
	}
	pending := imageTrashTestCreateImage(t, filepath.Join(root, "p.jpg"), "pending")
	if err := database.DB.Create(&models.ImageTrashEntry{DeletedBy: "user", ImageID: pending.ID, ImageName: pending.Name,
		OriginalPath: pending.Path, State: trashStatePendingMove, Mode: models.TrashModeTrash, DeleteBatchID: newDeleteBatchID()}).Error; err != nil {
		t.Fatal(err)
	}
	result := svc.PermanentlyDeleteImages([]uint{trashed.ID, recordOnly.ID, pending.ID})
	if result.Succeeded != 0 || result.Failed != 3 {
		t.Fatalf("三种情形都应被拒绝: %#v", result)
	}
	for _, path := range []string{recordOnly.Path, pending.Path, p010ImageEntry(t, trashed.ID).TrashPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("文件不能被永久删除: %v", err)
		}
	}
}

// ---------- I6：删除时的离线判断 ----------

func TestLIB04MoveVanishedFileOnOfflineRootDoesNotDegradeI6(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	parent := t.TempDir()
	root := filepath.Join(parent, "external")
	createDirectoryRow(t, root)
	video := p010Video(t, filepath.Join(root, "a.mp4"), "content-a")
	// 检查之后、移动之时盘被拔了：替身移动函数让根消失并报「文件不存在」。
	p010WithSystemTrash(t, func(string) (string, error) {
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		return "", os.ErrNotExist
	})
	err := svc.DeleteVideo(video.ID, true)
	if !errors.Is(err, ErrTrashVolumeOffline) {
		t.Fatalf("根离线时文件不存在不能降级成只清记录: %v", err)
	}
	if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("记录必须保持活跃: %v", err)
	}
	var entries int64
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("video_id = ?", video.ID).Count(&entries).Error; err != nil || entries != 0 {
		t.Fatalf("不应留下条目: %d err=%v", entries, err)
	}
}

func TestIMG02MoveVanishedFileOnOfflineRootDoesNotDegradeI6(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	parent := t.TempDir()
	root := filepath.Join(parent, "external")
	imageTestMustAddDirectory(t, svc, root)
	image := imageTrashTestCreateImage(t, filepath.Join(root, "a.jpg"), "content-a")
	p010WithSystemTrash(t, func(string) (string, error) {
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		return "", os.ErrNotExist
	})
	if err := svc.DeleteImage(image.ID, true); !errors.Is(err, ErrTrashVolumeOffline) {
		t.Fatalf("根离线时文件不存在不能降级: %v", err)
	}
	if err := database.DB.First(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("图片必须保持活跃: %v", err)
	}
}

// ---------- Minor 1：cgo 成功但未返回位置 ----------

func TestLIB05TrashSucceededWithoutLocationKeepsEntryAndFindsFileMinor1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	found := p010Video(t, filepath.Join(root, "found.mp4"), "found-content")
	lost := p010Video(t, filepath.Join(root, "lost.mp4"), "lost-content")
	// found：文件进了废纸篓顶层（替身的查找目录能找到），但系统没回报位置。
	// lost：文件被移到了查找目录之外，按身份找不到。
	p010WithSystemTrash(t, func(path string) (string, error) {
		if strings.HasSuffix(path, "lost.mp4") {
			elsewhere := filepath.Join(root, "elsewhere")
			if err := os.MkdirAll(elsewhere, 0o755); err != nil {
				return "", err
			}
			if err := os.Rename(path, filepath.Join(elsewhere, "lost.mp4")); err != nil {
				return "", err
			}
			return "", errTrashLocationUnknown
		}
		if _, err := fakeSystemTrashMove(path); err != nil {
			return "", err
		}
		return "", errTrashLocationUnknown
	})
	if err := svc.DeleteVideo(found.ID, true); err != nil {
		t.Fatalf("找得到文件时删除应完成: %v", err)
	}
	got := p010VideoEntry(t, found.ID)
	if got.State != trashStateDeleted || got.TrashPath != filepath.Join(root, ".Trash", "found.mp4") || !got.FileMoved || got.LastError != "" {
		t.Fatalf("应按身份补写 trash_path 并置 deleted: %#v", got)
	}
	if err := svc.DeleteVideo(lost.ID, true); err != nil {
		t.Fatalf("找不到文件时也要保证记录与文件不脱节: %v", err)
	}
	got = p010VideoEntry(t, lost.ID)
	if got.State != trashStateDeleted || got.TrashPath != "" || got.LastError != trashUnknownLocationMessage {
		t.Fatalf("找不到时应置 deleted 并写「文件位置未知」，条目不能被删除: %#v", got)
	}
	for _, id := range []uint{found.ID, lost.ID} {
		var video models.Video
		if err := database.DB.Unscoped().First(&video, id).Error; err != nil || !video.DeletedAt.IsValid() {
			t.Fatalf("记录应被软删: %v", err)
		}
	}
}

func TestIMG02TrashSucceededWithoutLocationKeepsEntryMinor1(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	image := imageTrashTestCreateImage(t, filepath.Join(root, "a.jpg"), "content-a")
	p010WithSystemTrash(t, func(path string) (string, error) {
		if _, err := fakeSystemTrashMove(path); err != nil {
			return "", err
		}
		return "", errTrashLocationUnknown
	})
	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatal(err)
	}
	got := p010ImageEntry(t, image.ID)
	if got.State != trashStateDeleted || got.TrashPath != filepath.Join(root, ".Trash", "a.jpg") || !got.FileMoved {
		t.Fatalf("图片也应按身份补写位置: %#v", got)
	}
}

// ---------- Minor 2：并发删除时不回滚他方已终结的文件 ----------

func TestLIB05ConcurrentFinishedEntryIsNotRolledBackMinor2(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video := p010Video(t, filepath.Join(root, "race.mp4"), "race-content")
	var target string
	// 替身：文件进废纸篓之后，「另一个并发的删除」已经把同一个条目终结为 deleted 并软删了记录。
	p010WithSystemTrash(t, func(path string) (string, error) {
		moved, err := fakeSystemTrashMove(path)
		if err != nil {
			return "", err
		}
		target = moved
		if err := database.DB.Model(&models.VideoTrashEntry{}).Where("video_id = ?", video.ID).
			Updates(map[string]interface{}{"state": trashStateDeleted, "file_moved": true, "trash_path": moved}).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Delete(&models.Video{}, video.ID).Error; err != nil {
			t.Fatal(err)
		}
		return moved, nil
	})
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatalf("他方已终结时应幂等返回成功: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("不能把文件回滚回原处: %v", err)
	}
	if _, err := os.Stat(video.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("原路径上不应再有文件（否则出现记录已删、文件被放回）: %v", err)
	}
}

// ---------- Minor 10：废纸篓路径被其他文件占用 ----------

func TestLIB05ReplacedTrashFileMarksGoneAndIsNeverDeletedMinor10(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video := p010Video(t, filepath.Join(root, "swap.mp4"), "original-content")
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	// 用户清空了废纸篓，系统又把同一个路径分给了另一个同名同大小的文件（不同 inode）。
	if err := os.Remove(entry.TrashPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.TrashPath, []byte("original-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(entry.TrashPath, time.Unix(0, entry.FileModTime), time.Unix(0, entry.FileModTime)); err != nil {
		t.Fatal(err)
	}
	center := NewTrashCenter(svc, nil)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("列表失败: %#v err=%v", page, err)
	}
	item := page.Items[0]
	if item.State != models.TrashStateFileGone || item.LastError != "废纸篓中的文件已被替换" {
		t.Fatalf("应置 file_gone 并写「废纸篓中的文件已被替换」: %#v", item)
	}
	purge, err := center.PurgeTrashEntries("video", []uint{entry.ID})
	if err != nil || purge.Succeeded != 0 || purge.Items[0].Code != TrashResultIdentityMismatch {
		t.Fatalf("清除不得删除这个不符的文件: %#v err=%v", purge, err)
	}
	removed, err := center.RemoveGoneTrashEntries("video", []uint{entry.ID})
	if err != nil || removed.Succeeded != 1 {
		t.Fatalf("移除记录应成功: %#v err=%v", removed, err)
	}
	if data, err := os.ReadFile(entry.TrashPath); err != nil || string(data) != "original-content" {
		t.Fatalf("占用路径的那个文件必须保留: %v", err)
	}
}

// ---------- Minor 13：崩溃恢复里有同名但身份不同的干扰文件 ----------

func TestLIB05CrashRecoveryIgnoresSameNameDecoyWithOtherIdentityMinor13(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video := p010Video(t, filepath.Join(root, "movie.mp4"), "genuine-content")
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
	// 真文件进了废纸篓，但被系统改了名；同名的 movie.mp4 是另一个文件（内容长度相同、mtime 相同，inode 不同）。
	trashDir := filepath.Join(root, ".Trash")
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	genuine := filepath.Join(trashDir, "movie 2.mp4")
	if err := os.Rename(video.Path, genuine); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(trashDir, "movie.mp4")
	if err := os.WriteFile(decoy, []byte("genuine-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(decoy, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileTrashEntries(); err != nil {
		t.Fatal(err)
	}
	if got := p010VideoEntry(t, video.ID); got.TrashPath != genuine || got.State != trashStateDeleted || !got.FileMoved {
		t.Fatalf("应按身份找到改名后的真文件，而不是同名干扰文件: %#v", got)
	}

	// 只有干扰文件时：找不到，落到「文件位置未知」，不能认错。
	other := p010Video(t, filepath.Join(root, "other.mp4"), "other-content")
	otherInfo, _ := os.Stat(other.Path)
	otherEntry := models.VideoTrashEntry{
		DeletedBy: "user", VideoID: other.ID, VideoName: other.Name, OriginalPath: other.Path,
		FileSize: otherInfo.Size(), FileModTime: otherInfo.ModTime().UnixNano(), FileIdentity: stableFileIdentity(otherInfo),
		State: trashStatePendingMove, Mode: models.TrashModeTrash, DeleteBatchID: newDeleteBatchID(),
	}
	if err := database.DB.Create(&otherEntry).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(other.Path); err != nil {
		t.Fatal(err)
	}
	decoyOnly := filepath.Join(trashDir, "other.mp4")
	if err := os.WriteFile(decoyOnly, []byte("other-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(decoyOnly, otherInfo.ModTime(), otherInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileTrashEntries(); err != nil {
		t.Fatal(err)
	}
	if got := p010VideoEntry(t, other.ID); got.TrashPath != "" || got.LastError != trashUnknownLocationMessage {
		t.Fatalf("只有身份不同的干扰文件时应判为位置未知: %#v", got)
	}
}

// ---------- Minor 6：路径前缀改写覆盖图片、回收站条目与迁移残留 ----------

func TestRewriteLibraryPathPrefixCoversImagesTrashAndStagedSourcesLIB06(t *testing.T) {
	setupVideoServiceTestDB(t)
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(filepath.Dir(oldRoot), "new")
	active := models.Image{Name: "a.jpg", Path: filepath.Join(oldRoot, "a.jpg"), Directory: oldRoot, Size: 1}
	deleted := models.Image{Name: "b.jpg", Path: filepath.Join(oldRoot, "sub", "b.jpg"), Directory: filepath.Join(oldRoot, "sub"), Size: 1}
	sibling := models.Image{Name: "c.jpg", Path: oldRoot + "-sibling/c.jpg", Directory: oldRoot + "-sibling", Size: 1}
	for _, image := range []*models.Image{&active, &deleted, &sibling} {
		if err := database.DB.Create(image).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Delete(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	imageEntry := models.ImageTrashEntry{ImageID: deleted.ID, ImageName: "b.jpg", OriginalPath: deleted.Path,
		TrashPath: filepath.Join(oldRoot, "trash", "b.jpg"), State: trashStateDeleted, Mode: models.TrashModeLegacyTrash}
	if err := database.DB.Create(&imageEntry).Error; err != nil {
		t.Fatal(err)
	}
	staged := models.MigrationStagedSource{OriginalPath: filepath.Join(oldRoot, "m.mp4"),
		StagedPath: filepath.Join(oldRoot, ".m.mp4.cineinsight-migrating-x"), Size: 1, State: models.MigrationStagedStatePending}
	if err := database.DB.Create(&staged).Error; err != nil {
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
	if counts.Images != 2 || counts.ImageTrashEntries != 1 || counts.StagedSources != 1 {
		t.Fatalf("计数不符: %+v", counts)
	}
	var gotDeleted, gotActive, gotSibling models.Image
	if err := database.DB.Unscoped().First(&gotDeleted, deleted.ID).Error; err != nil || gotDeleted.Path != filepath.Join(newRoot, "sub", "b.jpg") ||
		gotDeleted.Directory != filepath.Join(newRoot, "sub") || !gotDeleted.DeletedAt.IsValid() {
		t.Fatalf("软删图片的路径应被改写且仍是软删: %#v err=%v", gotDeleted, err)
	}
	if err := database.DB.First(&gotActive, active.ID).Error; err != nil || gotActive.Path != filepath.Join(newRoot, "a.jpg") || gotActive.Directory != newRoot {
		t.Fatalf("活跃图片的路径应被改写: %#v err=%v", gotActive, err)
	}
	if err := database.DB.First(&gotSibling, sibling.ID).Error; err != nil || gotSibling.Path != sibling.Path {
		t.Fatalf("同前缀的兄弟目录不能被改写: %#v err=%v", gotSibling, err)
	}
	var entry models.ImageTrashEntry
	if err := database.DB.First(&entry, imageEntry.ID).Error; err != nil ||
		entry.OriginalPath != filepath.Join(newRoot, "sub", "b.jpg") || entry.TrashPath != filepath.Join(newRoot, "trash", "b.jpg") {
		t.Fatalf("图片回收站条目应被改写: %#v err=%v", entry, err)
	}
	var source models.MigrationStagedSource
	if err := database.DB.First(&source, staged.ID).Error; err != nil ||
		source.OriginalPath != filepath.Join(newRoot, "m.mp4") || source.StagedPath != filepath.Join(newRoot, ".m.mp4.cineinsight-migrating-x") {
		t.Fatalf("迁移残留登记应被改写: %#v err=%v", source, err)
	}
}

// ---------- Minor 8：播放失败后台重定位全局串行且可取消 ----------

func TestPlaybackRelocationIsSerializedAndCancellableMinor8PLAY12(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	createDirectoryRow(t, root)
	// 文件被改到了别的位置：在线根里有同名同大小的唯一候选。
	moved := filepath.Join(root, "sub", "moved.mp4")
	createOldVideoFile(t, moved)
	video := models.Video{Name: "moved.mp4", Path: filepath.Join(root, "moved.mp4"), Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(playbackRelocateWG.Wait)

	// 占住全局信号量：后台重定位必须排队，不能并发去遍历。
	playbackRelocateSem <- struct{}{}
	held := true
	release := func() {
		if held {
			held = false
			<-playbackRelocateSem
		}
	}
	defer release()
	svc.reconcileAfterPlaybackFailure(&video, "file_missing")
	time.Sleep(150 * time.Millisecond)
	if got := p011ReloadVideo(t, video.ID); got.Path != video.Path {
		t.Fatalf("信号量被占用时重定位不应开始: %+v", got)
	}
	release()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := p011ReloadVideo(t, video.ID); got.Path == moved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("信号量释放后排队的重定位应继续并完成")
		}
		time.Sleep(10 * time.Millisecond)
	}
	playbackRelocateWG.Wait()

	// 取消：排队中的重定位在 StopPlaybackRelocation 之后退出，不改任何记录。
	other := models.Video{Name: "later.mp4", Path: filepath.Join(root, "later.mp4"), Directory: root, Size: 1}
	if err := database.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	createOldVideoFile(t, filepath.Join(root, "deep", "later.mp4"))
	playbackRelocateSem <- struct{}{}
	held = true
	svc.reconcileAfterPlaybackFailure(&other, "file_missing")
	done := make(chan struct{})
	go func() {
		StopPlaybackRelocation()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StopPlaybackRelocation 应能取消排队中的重定位并返回")
	}
	release()
	if got := p011ReloadVideo(t, other.ID); got.Path != other.Path {
		t.Fatalf("取消后不应再改路径: %+v", got)
	}
}

// ---------- Minor 9：删除暂存源前复核体积 ----------

func TestDeleteStagedSourcesRefusesWhenSizeChangedLIB11Minor9(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	fileStaged := filepath.Join(root, ".a.mp4.cineinsight-migrating-1")
	dirStaged := filepath.Join(root, ".d.cineinsight-migrating-2")
	okStaged := filepath.Join(root, ".ok.mp4.cineinsight-migrating-3")
	if err := os.WriteFile(fileStaged, []byte("now-much-longer-than-registered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirStaged, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirStaged, "x.bin"), []byte("12345678"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(okStaged, []byte("1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := []models.MigrationStagedSource{
		{OriginalPath: "/x/a.mp4", StagedPath: fileStaged, Size: 3, State: models.MigrationStagedStatePending},
		{OriginalPath: "/x/d", StagedPath: dirStaged, Size: 4, State: models.MigrationStagedStatePending},
		{OriginalPath: "/x/ok.mp4", StagedPath: okStaged, Size: 4, State: models.MigrationStagedStatePending},
	}
	for i := range rows {
		if err := database.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	result, err := (&VideoService{}).DeleteStagedSources([]uint{rows[0].ID, rows[1].ID, rows[2].ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Cleaned != 1 || len(result.Failed) != 2 {
		t.Fatalf("体积不一致的两项应被拒绝、一致的一项应被删除: %#v", result)
	}
	for _, failure := range result.Failed {
		if !strings.Contains(failure.Error, "大小") || strings.Contains(failure.Error, root) {
			t.Errorf("拒绝原因应说明体积不一致且不含路径: %#v", failure)
		}
	}
	for _, path := range []string{fileStaged, dirStaged} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("被拒绝的暂存源必须保留: %v", err)
		}
	}
	if _, err := os.Lstat(okStaged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("体积一致的暂存源应被删除: %v", err)
	}
	var pending int64
	if err := database.DB.Model(&models.MigrationStagedSource{}).Where("state = ?", models.MigrationStagedStatePending).Count(&pending).Error; err != nil || pending != 2 {
		t.Fatalf("被拒绝的两项应仍是 pending: %d err=%v", pending, err)
	}
}

// ---------- Minor 11：隐藏列表包含活跃但 is_stale 的图片 ----------

func TestIMG02ListHiddenImagesIncludesActiveStaleImagesMinor11(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	onlineRoot := t.TempDir()
	offlineRoot := filepath.Join(t.TempDir(), "unplugged")
	imageTestMustAddDirectory(t, svc, onlineRoot)
	imageTestMustAddDirectory(t, svc, offlineRoot)
	activeStale := models.Image{Name: "s.jpg", Path: filepath.Join(offlineRoot, "s.jpg"), Directory: offlineRoot, Size: 1, IsStale: true}
	activeFine := models.Image{Name: "f.jpg", Path: filepath.Join(onlineRoot, "f.jpg"), Directory: onlineRoot, Size: 1}
	for _, image := range []*models.Image{&activeStale, &activeFine} {
		if err := database.DB.Create(image).Error; err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.ListHiddenImages(0, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != activeStale.ID || page.Items[0].Reason != HiddenImageReasonOfflineRoot {
		t.Fatalf("活跃但 is_stale 的图片应被列出并附原因: %#v err=%v", page, err)
	}
}

// ---------- Minor 12：MarkRootOffline 复核在线状态，且不阻塞监听事件循环 ----------

func TestMarkRootOfflineRechecksRootOnlineLIB07Minor12(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	createDirectoryRow(t, root)
	video := models.Video{Name: "a.mp4", Path: filepath.Join(root, "a.mp4"), Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	marked, err := (&VideoService{}).MarkRootOffline(root)
	if err != nil || marked != 0 {
		t.Fatalf("根已经在线时不应标记: marked=%d err=%v", marked, err)
	}
	if got := p011ReloadVideo(t, video.ID); got.IsStale {
		t.Fatalf("在线根下的视频不应被标失效: %+v", got)
	}
}

func TestLibraryWatcherOfflineMarkingDoesNotBlockEventLoopLIB07Minor12(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	reconciled := make(chan []string, 4)
	backend := newFakeLibraryWatchBackend()
	service := newTestLibraryWatcher(backend, func(_ []models.ScanDirectory, affected []string) *ScanSyncResult {
		reconciled <- affected
		return &ScanSyncResult{}
	})
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	service.markRootOffline = func(string) {
		started <- struct{}{}
		<-release
	}
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: rootA}, {ID: 2, Path: rootB}}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		service.Close()
	}()

	backend.events <- fsnotify.Event{Name: rootA, Op: fsnotify.Remove}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("根被移除后应调用离线标记")
	}
	// 标记回调还卡着：事件循环必须仍然能处理另一个根的事件并派发对账。
	newFile := filepath.Join(rootB, "new.mp4")
	if err := os.WriteFile(newFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	backend.events <- fsnotify.Event{Name: newFile, Op: fsnotify.Create}
	select {
	case affected := <-reconciled:
		if len(affected) == 0 {
			t.Fatalf("对账应带上受影响目录: %v", affected)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("离线标记卡住时监听事件循环不应被阻塞")
	}
	close(release)
}
