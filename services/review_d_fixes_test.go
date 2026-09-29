package services

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 修复 D（第三轮回收站修复复审 + 手机端 PIN 锁定体验）的回归测试。测试名里的 LIBxx / IMGxx / PLAYxx
// 是问题清单 ID，IA / Mn 是这一轮复审的问题编号。系统废纸篓是替身（system_trash_testhook_test.go）：
// 同卷重命名进「原目录/.Trash」。

// reviewDTrashSideStatFails 让 match 命中的「废纸篓一侧」路径 stat 返回 failure（例如 EPERM），其余照常。
func reviewDTrashSideStatFails(t *testing.T, match func(string) bool, failure error) {
	t.Helper()
	fn := func(path string) (os.FileInfo, error) {
		if match(path) {
			return nil, &os.PathError{Op: "stat", Path: path, Err: failure}
		}
		return os.Stat(path)
	}
	previous := trashSideStatFn.Swap(&fn)
	t.Cleanup(func() { trashSideStatFn.Store(previous) })
}

func reviewDInsideTrashDir(path string) bool {
	return strings.Contains(filepath.ToSlash(path), "/.Trash/")
}

func reviewDSameInode(t *testing.T, a, b string) bool {
	t.Helper()
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

// ---------- I-A：放回判定不依赖能读到废纸篓一侧 ----------

func TestLIB05ScanRestoresPutBackWhenTrashSideUnreadableIA(t *testing.T) {
	svc, video, entry, tag := p010PutBackFixture(t)
	// 废纸篓里同时还留着一个同 inode 的名字，且那一侧读不到（EPERM）。
	if err := os.Link(video.Path, entry.TrashPath); err != nil {
		t.Skip("无法创建硬链接")
	}
	reviewDTrashSideStatFails(t, reviewDInsideTrashDir, syscall.EPERM)

	page, err := NewTrashCenter(svc, nil).ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 1 || !page.Items[0].PutBack || page.Items[0].State != trashStateDeleted {
		t.Fatalf("读不到废纸篓一侧时列表仍应报告可恢复: %#v err=%v", page, err)
	}
	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: filepath.Dir(video.Path)}})
	if result.Restored != 1 || result.Added != 0 || len(result.Errors) != 0 {
		t.Fatalf("扫描应恢复原记录而不是新建: %+v", result)
	}
	var restored models.Video
	if err := database.DB.Preload("Tags").First(&restored, video.ID).Error; err != nil || len(restored.Tags) != 1 || restored.Tags[0].ID != tag.ID {
		t.Fatalf("原 ID 与标签应保留: %#v err=%v", restored, err)
	}
	var total int64
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("path = ?", video.Path).Count(&total).Error; err != nil || total != 1 {
		t.Fatalf("同路径只应有原记录: %d err=%v", total, err)
	}
	// 系统废纸篓里的那个名字不动（M1）。
	if _, err := os.Lstat(entry.TrashPath); err != nil {
		t.Fatalf("恢复不得删除废纸篓里的名字: %v", err)
	}
}

func TestIMG02ScanRestoresPutBackWhenTrashSideUnreadableIA(t *testing.T) {
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
	reviewDTrashSideStatFails(t, reviewDInsideTrashDir, syscall.EPERM)

	result, err := svc.SyncImageDirectories()
	if err != nil || result.Restored != 1 || result.Added != 0 || len(result.Errors) != 0 {
		t.Fatalf("读不到废纸篓一侧时图片扫描应恢复原记录: %#v err=%v", result, err)
	}
	if err := database.DB.First(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("原图片记录应被恢复: %v", err)
	}
	var total int64
	if err := database.DB.Unscoped().Model(&models.Image{}).Where("path = ?", path).Count(&total).Error; err != nil || total != 1 {
		t.Fatalf("同路径只应有原图片记录: %d err=%v", total, err)
	}
}

// ---------- M1：硬链接不删废纸篓那个名字，恢复事务失败时绝不动原路径 ----------

func TestLIB05HardLinkRestoreTxFailureLeavesOriginalUntouchedM1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	path := filepath.Join(t.TempDir(), "linked.mp4")
	video := p010Video(t, path, "linked-content")
	mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	if err := os.Link(entry.TrashPath, path); err != nil {
		t.Skip("无法创建硬链接")
	}
	center := NewTrashCenter(svc, nil)
	disable := reviewAFailDeletesOn(t, "video_trash_entries")
	result, err := center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Failed != 1 {
		t.Fatalf("注入失败后恢复应报失败: %#v err=%v", result, err)
	}
	if !reviewDSameInode(t, path, entry.TrashPath) {
		t.Fatal("恢复事务失败后原路径与废纸篓里的名字都必须原样保留")
	}
	if got := p010VideoEntry(t, video.ID); got.State != trashStateDeleted {
		t.Fatalf("条目应回到 deleted: %#v", got)
	}
	if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() {
		t.Fatalf("记录应仍是软删: %#v", got)
	}

	disable()
	result, err = center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("解除注入后恢复应成功: %#v err=%v", result, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件应在原路径: %v", err)
	}
	if _, err := os.Lstat(entry.TrashPath); err != nil {
		t.Fatalf("系统废纸篓里的名字不得被删除: %v", err)
	}
}

func TestIMG02HardLinkRestoreTxFailureLeavesOriginalUntouchedM1(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	path := filepath.Join(t.TempDir(), "linked.jpg")
	image := imageTrashTestCreateImage(t, path, "linked-image")
	mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010ImageEntry(t, image.ID)
	if err := os.Link(entry.TrashPath, path); err != nil {
		t.Skip("无法创建硬链接")
	}
	center := NewTrashCenter(nil, svc)
	disable := reviewAFailDeletesOn(t, "image_trash_entries")
	result, err := center.RestoreTrashEntries("image", []uint{entry.ID})
	if err != nil || result.Failed != 1 {
		t.Fatalf("注入失败后恢复应报失败: %#v err=%v", result, err)
	}
	if !reviewDSameInode(t, path, entry.TrashPath) {
		t.Fatal("恢复事务失败后原路径与废纸篓里的名字都必须原样保留")
	}
	disable()
	result, err = center.RestoreTrashEntries("image", []uint{entry.ID})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("解除注入后恢复应成功: %#v err=%v", result, err)
	}
	if !reviewDSameInode(t, path, entry.TrashPath) {
		t.Fatal("恢复成功后系统废纸篓里的名字也不动")
	}
}

// reviewDLegacyEntry 构造一条旧版（legacy_trash）条目：文件曾被旧版应用以硬链接 + 删除原名的方式移进
// <目录>/trash/，记录已软删。返回条目与它的废纸篓路径。
func reviewDLegacyEntry(t *testing.T, video models.Video) models.VideoTrashEntry {
	t.Helper()
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatal(err)
	}
	trashPath := filepath.Join(filepath.Dir(video.Path), DefaultTrashDirName, filepath.Base(video.Path))
	if err := os.MkdirAll(filepath.Dir(trashPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(video.Path, trashPath); err != nil {
		t.Skip("无法创建硬链接")
	}
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	entry := models.VideoTrashEntry{
		DeletedBy: "user", VideoID: video.ID, VideoName: video.Name, OriginalPath: video.Path, TrashPath: trashPath,
		FileMoved: true, FileSize: info.Size(), FileModTime: info.ModTime().UnixNano(), FileIdentity: stableFileIdentity(info),
		State: trashStateDeleted, Mode: models.TrashModeLegacyTrash,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestLIB05LegacyHardLinkRestoreKeepsOriginalAndCleansResidueAfterCommitM1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video := p010Video(t, filepath.Join(t.TempDir(), "movies", "old.mp4"), "legacy-content")
	entry := reviewDLegacyEntry(t, video)
	// 旧版恢复在 link 之后、remove 之前中断：原路径与 trash/ 里是同一个文件的两个名字。
	if err := os.Link(entry.TrashPath, video.Path); err != nil {
		t.Fatal(err)
	}
	center := NewTrashCenter(svc, nil)
	disable := reviewAFailDeletesOn(t, "video_trash_entries")
	result, err := center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Failed != 1 {
		t.Fatalf("注入失败后恢复应报失败: %#v err=%v", result, err)
	}
	if !reviewDSameInode(t, video.Path, entry.TrashPath) {
		t.Fatal("恢复事务失败时两个名字都必须保留，原路径文件绝不能被移走")
	}
	disable()
	result, err = center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("解除注入后恢复应成功: %#v err=%v", result, err)
	}
	if _, err := os.Stat(video.Path); err != nil {
		t.Fatalf("文件应在原路径: %v", err)
	}
	if _, err := os.Lstat(entry.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧版 trash/ 里的残留名字应在恢复提交后清掉: %v", err)
	}
}

// ---------- M2：清除类入口统一复用放回判定与卷离线检查 ----------

func TestLIB05PurgePutBackEntryReturnsNotPurgeableM2(t *testing.T) {
	svc, video, entry, _ := p010PutBackFixture(t)
	center := NewTrashCenter(svc, nil)
	result, err := center.PurgeTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Items[0].Code != TrashResultNotPurgeable || !strings.Contains(result.Items[0].Message, "恢复") {
		t.Fatalf("已放回的条目清除应返回 not_purgeable 并提示恢复: %#v err=%v", result, err)
	}
	if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() {
		t.Fatalf("记录不得被硬删: %#v", got)
	}
	// 硬链接：废纸篓里还有同 inode 的名字，同样拒绝，且不删那个名字。
	if err := os.Link(video.Path, entry.TrashPath); err != nil {
		t.Skip("无法创建硬链接")
	}
	result, err = center.PurgeTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Items[0].Code != TrashResultNotPurgeable {
		t.Fatalf("硬链接形态的放回也应拒绝清除: %#v err=%v", result, err)
	}
	if !reviewDSameInode(t, video.Path, entry.TrashPath) {
		t.Fatal("拒绝清除时两个名字都保留")
	}
}

func TestLIB05PurgeUnknownLocationEntryChecksPutBackAndVolumeM2(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	mount := filepath.Join(t.TempDir(), "external")
	createDirectoryRow(t, mount)
	video := p010Video(t, filepath.Join(mount, "movies", "a.mp4"), "unknown-location")
	entry := reviewAPendingTrashEntry(t, video)
	// 崩溃恢复分支 3：state=deleted、trash_path 为空、「文件位置未知」。
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", entry.ID).
		Updates(map[string]interface{}{"state": trashStateDeleted, "last_error": trashUnknownLocationMessage}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&video).Error; err != nil {
		t.Fatal(err)
	}
	center := NewTrashCenter(svc, nil)
	// 文件其实在原处（身份一致）：拒绝，提示恢复。
	result, err := center.PurgeTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Items[0].Code != TrashResultNotPurgeable {
		t.Fatalf("文件在原处时清除应返回 not_purgeable: %#v err=%v", result, err)
	}
	// 文件不在、卷离线：拒绝。
	if err := os.Remove(video.Path); err != nil {
		t.Fatal(err)
	}
	remount := reviewAUnmountVolume(t, mount)
	result, err = center.PurgeTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Items[0].Code != TrashResultVolumeOffline {
		t.Fatalf("卷离线时清除应返回 volume_offline: %#v err=%v", result, err)
	}
	var count int64
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", video.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("拒绝时不得硬删记录: %d err=%v", count, err)
	}
	// 卷在线、文件确实不在：清除成功。
	remount()
	result, err = center.PurgeTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("卷在线且文件不在时清除应成功: %#v err=%v", result, err)
	}
}

func TestLIB05RemoveGoneOnPutBackOrOfflineEntryRefusedM2(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	mount := filepath.Join(t.TempDir(), "external")
	createDirectoryRow(t, mount)
	path := filepath.Join(mount, "movies", "late.mp4")
	video := p010Video(t, path, "late-content")
	mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	held := filepath.Join(t.TempDir(), "held.mp4")
	if err := os.Rename(entry.TrashPath, held); err != nil {
		t.Skip("无法在临时目录之间移动文件")
	}
	center := NewTrashCenter(svc, nil)
	if page, err := center.ListTrashEntries(TrashFilter{Kind: "video"}); err != nil || page.Items[0].State != models.TrashStateFileGone {
		t.Fatalf("文件暂时不在时应为 file_gone: %#v err=%v", page, err)
	}
	// 卷离线：移除记录拒绝。
	remount := reviewAUnmountVolume(t, mount)
	removed, err := center.RemoveGoneTrashEntries("video", []uint{entry.ID})
	if err != nil || removed.Items[0].Code != TrashResultVolumeOffline {
		t.Fatalf("卷离线时移除记录应返回 volume_offline: %#v err=%v", removed, err)
	}
	remount()
	// 用户把文件放回了原处（不经列表对账）：移除记录拒绝并提示恢复。
	if err := os.Rename(held, path); err != nil {
		t.Skip("无法在临时目录之间移动文件")
	}
	removed, err = center.RemoveGoneTrashEntries("video", []uint{entry.ID})
	if err != nil || removed.Items[0].Code != TrashResultNotPurgeable || !strings.Contains(removed.Items[0].Message, "恢复") {
		t.Fatalf("已放回的条目移除记录应返回 not_purgeable: %#v err=%v", removed, err)
	}
	var count int64
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", video.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("拒绝时不得硬删记录: %d err=%v", count, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件应在原处: %v", err)
	}
}

// ---------- M3：符号链接与非 /Volumes 挂载点 ----------

func TestLIB07ScanRootSymlinkToUnmountedVolumeIsOfflineM3(t *testing.T) {
	setupVideoServiceTestDB(t)
	base := t.TempDir()
	// 卸载后留下的空挂载点目录（模拟 /Volumes/Ext），扫描根是指向它的软链接。
	mountPoint := filepath.Join(base, "Volumes", "Ext")
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "Movies")
	if err := os.Symlink(mountPoint, link); err != nil {
		t.Skip("无法创建符号链接")
	}
	if !scanRootOnline(link) {
		t.Fatal("对照：卷挂载时软链接根应在线")
	}
	resolvedMount, err := filepath.EvalSymlinks(mountPoint)
	if err != nil {
		t.Fatal(err)
	}
	// 挂载检查替身只认解析后的真实路径：原路径（软链接）不在 /Volumes 之下，旧实现会判为在线。
	remount := reviewAUnmountVolume(t, resolvedMount)
	if scanRootOnline(link) {
		t.Fatal("软链接指向已卸载的卷时扫描根应判为离线")
	}
	if !mediaPathOffline(filepath.Join(link, "a.mp4")) {
		t.Fatal("软链接根下的文件路径应判为离线")
	}
	createDirectoryRow(t, link)
	video := models.Video{Name: "a.mp4", Path: filepath.Join(link, "a.mp4"), Directory: link, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if marked, err := (&VideoService{}).MarkRootOffline(link); err != nil || marked != 1 {
		t.Fatalf("离线的软链接根应被标记: marked=%d err=%v", marked, err)
	}
	remount()

	// 软链接悬空（挂载点目录也没了）：EvalSymlinks 失败，按离线处理。
	if err := os.RemoveAll(filepath.Join(base, "Volumes")); err != nil {
		t.Fatal(err)
	}
	if scanRootOnline(link) {
		t.Fatal("悬空的软链接根应判为离线")
	}
	if mediaVolumeAvailable(filepath.Join(link, "sub", "a.mp4")) == nil {
		t.Fatal("路径途经悬空的软链接时应判为离线")
	}
	// 反面：卷在线、只是文件（及其父目录）不在，不是离线。
	online := filepath.Join(base, "online")
	if err := os.MkdirAll(online, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mediaVolumeAvailable(filepath.Join(online, "gone", "a.mp4")); err != nil {
		t.Fatalf("父目录不存在不是离线: %v", err)
	}
}

// ---------- M4：旧版启发式时间上界由数据推出 ----------

func reviewDLegacyCandidate(t *testing.T, root string, index int, deletedAt time.Time, image bool) string {
	t.Helper()
	dir := filepath.Join(root, fmt.Sprintf("case-%d", index))
	name := "a.mp4"
	if image {
		name = "a.jpg"
	}
	var model interface{}
	if image {
		row := &models.Image{Name: name, Path: filepath.Join(dir, name), Directory: dir, Size: 1}
		if err := database.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Delete(row).Error; err != nil {
			t.Fatal(err)
		}
		model = &models.Image{}
		reviewASetDeletedAt(t, model, row.ID, deletedAt)
	} else {
		row := &models.Video{Name: name, Path: filepath.Join(dir, name), Directory: dir, Size: 1}
		if err := database.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Delete(row).Error; err != nil {
			t.Fatal(err)
		}
		model = &models.Video{}
		reviewASetDeletedAt(t, model, row.ID, deletedAt)
	}
	createOldVideoFile(t, filepath.Join(dir, "trash", name))
	return filepath.Join(dir, "trash")
}

func TestLIB14LegacyHeuristicUpperBoundFollowsEarliestTrashEntryM4(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	local := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.Local) }
	// 视频条目表最早一条建于 2026-08-20；图片条目表为空（上界回落到 2026-07-30）。
	earliest := models.VideoTrashEntry{VideoID: 900001, VideoName: "x.mp4", OriginalPath: filepath.Join(root, "x.mp4"),
		State: trashStateDeleted, Mode: models.TrashModeRecordOnly, CreatedAt: local(2026, 8, 20, 12)}
	later := models.VideoTrashEntry{VideoID: 900002, VideoName: "y.mp4", OriginalPath: filepath.Join(root, "y.mp4"),
		State: trashStateDeleted, Mode: models.TrashModeRecordOnly, CreatedAt: local(2026, 9, 25, 12)}
	for _, entry := range []*models.VideoTrashEntry{&later, &earliest} {
		if err := database.DB.Create(entry).Error; err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name      string
		deletedAt time.Time
		image     bool
		want      bool
	}{
		{"视频 最早条目之前 8-10", local(2026, 8, 10, 10), false, true},
		{"视频 最早条目当天 23 点", local(2026, 8, 20, 23), false, true},
		{"视频 最早条目次日零点 超出", local(2026, 8, 21, 0), false, false},
		{"视频 下界前一天 超出", local(2026, 4, 15, 12), false, false},
		{"图片 表为空 7-30 当天", local(2026, 7, 30, 23), true, true},
		{"图片 表为空 7-31 超出", local(2026, 7, 31, 1), true, false},
		{"图片 不借用视频表的上界 8-10", local(2026, 8, 10, 10), true, false},
	}
	dirs := make([]string, len(cases))
	for index, tc := range cases {
		dirs[index] = reviewDLegacyCandidate(t, root, index, tc.deletedAt, tc.image)
	}
	refreshLegacyTrashDirs()
	for index, tc := range cases {
		if got := isUnrecordedLegacyTrashDir(dirs[index]); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}

	// 最早条目早于 2026-07-30：上界不前移，仍是 7-30（含）。图片表有了 9-01 的条目后，图片上界随之后移。
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", earliest.ID).Update("created_at", local(2026, 7, 1, 12)).Error; err != nil {
		t.Fatal(err)
	}
	imageEntry := models.ImageTrashEntry{ImageID: 900003, ImageName: "z.jpg", OriginalPath: filepath.Join(root, "z.jpg"),
		State: trashStateDeleted, Mode: models.TrashModeRecordOnly, CreatedAt: local(2026, 9, 1, 8)}
	if err := database.DB.Create(&imageEntry).Error; err != nil {
		t.Fatal(err)
	}
	more := []struct {
		name      string
		deletedAt time.Time
		image     bool
		want      bool
	}{
		{"视频 最早条目在 7-30 之前 7-30 当天", local(2026, 7, 30, 22), false, true},
		{"视频 最早条目在 7-30 之前 8-10 超出", local(2026, 8, 10, 10), false, false},
		{"图片 最早条目 9-01 8-25", local(2026, 8, 25, 10), true, true},
		{"图片 最早条目 9-01 9-02 超出", local(2026, 9, 2, 10), true, false},
	}
	moreDirs := make([]string, len(more))
	for index, tc := range more {
		moreDirs[index] = reviewDLegacyCandidate(t, root, 100+index, tc.deletedAt, tc.image)
	}
	refreshLegacyTrashDirs()
	for index, tc := range more {
		if got := isUnrecordedLegacyTrashDir(moreDirs[index]); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

// ---------- M5：PIN 每日锁在最后一次失败后 24 小时自动解除，桌面端可解除 ----------

// reviewDFail 让 auth 在 at 时刻记一次失败；key 每次不同，避开单来源限次。
func reviewDFail(t *testing.T, auth *shortFeedAuth, key string, at time.Time) (bool, int) {
	t.Helper()
	if _, ok := auth.beginAttempt(key, at); !ok {
		t.Fatalf("%s 的尝试不应被拦下", key)
	}
	return auth.failedAttempt(key, at)
}

func TestShortFeedDailyLockAutoReleases24hAfterLastFailurePLAY01M5(t *testing.T) {
	auth := newShortFeedAuth()
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	var last time.Time
	for i := 0; i <= shortFeedDailyFailureLimit; i++ {
		last = base.Add(time.Duration(i) * 4 * time.Second)
		locked, retry := reviewDFail(t, auth, fmt.Sprintf("10.0.%d.%d", i/200, i%200), last)
		if i < shortFeedDailyFailureLimit && locked && retry == shortFeedDailyLockedRetry {
			t.Fatalf("第 %d 次失败不应触发每日上限", i+1)
		}
		if i == shortFeedDailyFailureLimit && (!locked || retry != shortFeedDailyLockedRetry) {
			t.Fatalf("第 %d 次失败应触发每日上限", i+1)
		}
	}
	if until := auth.dailyLockedUntilAt(last); !until.Equal(last.Add(24 * time.Hour)) {
		t.Fatalf("解除时刻应为最后一次失败之后 24 小时: %v", until)
	}
	// 锁定期间被拒的请求不算失败，不顺延锁定。
	for _, offset := range []time.Duration{time.Hour, 10 * time.Hour, 23*time.Hour + 59*time.Minute} {
		if retry, ok := auth.beginAttempt("10.9.9.9", last.Add(offset)); ok || retry != shortFeedDailyLockedRetry {
			t.Fatalf("锁定期间（+%v）应拒绝新登录: retry=%d ok=%v", offset, retry, ok)
		}
	}
	if retry, ok := auth.beginAttempt("10.9.9.9", last.Add(24*time.Hour-time.Second)); ok || retry != shortFeedDailyLockedRetry {
		t.Fatal("差一秒满 24 小时仍应锁定")
	}
	release := last.Add(24 * time.Hour)
	if _, ok := auth.beginAttempt("10.8.8.8", release); !ok {
		t.Fatal("最后一次失败之后满 24 小时应自动解除")
	}
	if locked, _ := auth.loginLockStatus(release); locked {
		t.Fatal("自动解除后状态应为未锁定")
	}
	// 解除后重新计数：再失败 200 次不锁，第 201 次再锁（每 24 小时约 200 次的上界不变）。
	for i := 0; i <= shortFeedDailyFailureLimit; i++ {
		at := release.Add(time.Duration(i+1) * 4 * time.Second)
		locked, retry := reviewDFail(t, auth, fmt.Sprintf("10.1.%d.%d", i/200, i%200), at)
		if i < shortFeedDailyFailureLimit && locked && retry == shortFeedDailyLockedRetry {
			t.Fatalf("解除后第 %d 次失败不应触发每日上限", i+1)
		}
		if i == shortFeedDailyFailureLimit && (!locked || retry != shortFeedDailyLockedRetry) {
			t.Fatal("解除后超过 200 次失败应再次锁定")
		}
	}
}

func TestShortFeedLoginKeepsDailyCountAndUnlockKeepsSessionsPLAY01M5(t *testing.T) {
	svc, handler, _ := newShortFeedAccessFixture(t)
	if err := svc.SetShortFeedPIN("246800"); err != nil {
		t.Fatal(err)
	}
	auth := svc.authState()
	// 150 次失败发生在两小时前（避开每分钟预算与单来源限次）。
	early := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 150; i++ {
		if locked, retry := reviewDFail(t, auth, fmt.Sprintf("10.2.0.%d", i), early.Add(time.Duration(i)*4*time.Second)); locked && retry == shortFeedDailyLockedRetry {
			t.Fatalf("第 %d 次失败不应触发每日上限", i+1)
		}
	}
	// 主人成功登录一次：每日计数不清。
	session := loginShortFeed(t, handler, "246800", "192.168.1.9:1")
	later := time.Now().Add(-time.Hour)
	for i := 0; i < 50; i++ {
		if locked, retry := reviewDFail(t, auth, fmt.Sprintf("10.3.0.%d", i), later.Add(time.Duration(i)*4*time.Second)); locked && retry == shortFeedDailyLockedRetry {
			t.Fatalf("成功登录后第 %d 次失败不应触发（合计未超过 200）", 150+i+1)
		}
	}
	lastAt := later.Add(50 * 4 * time.Second)
	if locked, retry := reviewDFail(t, auth, "10.3.1.1", lastAt); !locked || retry != shortFeedDailyLockedRetry {
		t.Fatal("成功登录不清每日计数：合计第 201 次失败应触发每日上限")
	}
	rec := shortFeedLoginStatus(handler, "246800", "192.168.50.1:1", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("每日锁定后登录应 429，实际 %d", rec.Code)
	}
	payload := decodeShortFeedBody(t, rec)
	if payload["code"] != "pin_locked" || payload["daily_locked"] != true || payload["retry_after"] == nil || payload["locked_until"] == nil {
		t.Fatalf("429 应带 pin_locked、daily_locked、retry_after 与 locked_until: %v", payload)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("每日锁定的 429 应带 Retry-After")
	}
	status, err := svc.AccessStatus()
	if err != nil || !status.LoginLocked || status.LockedUntil == nil || !status.LockedUntil.Equal(lastAt.Add(24*time.Hour)) {
		t.Fatalf("状态应报告锁定与解除时刻: %+v err=%v", status, err)
	}

	svc.UnlockShortFeedLogin()
	if status, err = svc.AccessStatus(); err != nil || status.LoginLocked || status.LockedUntil != nil || !status.PINSet {
		t.Fatalf("解除后状态应为未锁定、PIN 仍在: %+v err=%v", status, err)
	}
	// PIN 未变：用原 PIN 能登录；解除前的会话仍有效。
	if cookie := loginShortFeed(t, handler, "246800", "192.168.50.1:2"); cookie.Value == "" {
		t.Fatal("解除后应能用原 PIN 登录")
	}
	dataReq := shortFeedRequest(http.MethodGet, "/short-api/status", "", "192.168.1.9:1")
	dataReq.AddCookie(session)
	dataRec := httptest.NewRecorder()
	handler.ServeHTTP(dataRec, dataReq)
	if dataRec.Code != http.StatusOK {
		t.Fatalf("解除锁定不得撤销已有会话，实际 %d", dataRec.Code)
	}
}

// ---------- M6：MarkRootOffline 的「其他根是否在线」判定在拿 scanSyncMu 之前 ----------

func TestMarkRootOfflineChecksOtherRootsBeforeScanLockLIB07M6(t *testing.T) {
	setupVideoServiceTestDB(t)
	base := t.TempDir()
	mount := filepath.Join(base, "external")
	other := filepath.Join(base, "local")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	createDirectoryRow(t, mount)
	createDirectoryRow(t, other)
	video := models.Video{Name: "a.mp4", Path: filepath.Join(mount, "a.mp4"), Directory: mount, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	resolvedOther, err := filepath.EvalSymlinks(other)
	if err != nil {
		t.Fatal(err)
	}
	otherChecked := make(chan struct{}, 8)
	fn := func(path string) error {
		clean := filepath.Clean(path)
		if pathIsEqualOrInside(clean, mount) {
			return fmt.Errorf("磁盘未挂载: %w", os.ErrNotExist)
		}
		if pathIsEqualOrInside(clean, other) || pathIsEqualOrInside(clean, resolvedOther) {
			select {
			case otherChecked <- struct{}{}:
			default:
			}
		}
		return nil
	}
	previous := mediaVolumeAvailableFn.Swap(&fn)
	t.Cleanup(func() { mediaVolumeAvailableFn.Store(previous) })

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
	case <-otherChecked:
	case <-time.After(3 * time.Second):
		t.Fatal("其他根的在线判定应在拿到 scanSyncMu 之前完成")
	}
	select {
	case <-done:
		t.Fatal("窄对账持锁时写库必须等待")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case marked := <-done:
		if marked != 1 {
			t.Fatalf("释放后应完成标记: %d", marked)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("释放后离线标记应继续")
	}
}

// ---------- M7：重映射预检图片路径冲突，并如实返回图片侧改写计数 ----------

func TestRemapDirectoryPrechecksImagePathConflictsLIB02M7(t *testing.T) {
	setupVideoServiceTestDB(t)
	parent := t.TempDir()
	oldRoot := filepath.Join(parent, "Photos")
	newRoot := filepath.Join(parent, "Photos 1")
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := createDirectoryRow(t, oldRoot)
	moved := models.Image{Name: "a.jpg", Path: filepath.Join(oldRoot, "pics", "a.jpg"), Directory: filepath.Join(oldRoot, "pics"), Size: 1}
	occupant := models.Image{Name: "a.jpg", Path: filepath.Join(newRoot, "pics", "a.jpg"), Directory: filepath.Join(newRoot, "pics"), Size: 1}
	for _, image := range []*models.Image{&moved, &occupant} {
		if err := database.DB.Create(image).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &DirectoryService{}
	if _, err := svc.UpdateDirectory(dir.ID, newRoot, "x", DirectoryUpdateModeRemap); err == nil || !strings.Contains(err.Error(), "图片") {
		t.Fatalf("图片路径冲突应在事务前返回可读错误: %v", err)
	}
	var unchanged models.Image
	if err := database.DB.First(&unchanged, moved.ID).Error; err != nil || unchanged.Path != moved.Path {
		t.Fatalf("预检失败时不得改写任何路径: %+v err=%v", unchanged, err)
	}
	var unchangedDir models.ScanDirectory
	if err := database.DB.First(&unchangedDir, dir.ID).Error; err != nil || unchangedDir.Path != oldRoot {
		t.Fatalf("预检失败时扫描目录不变: %+v err=%v", unchangedDir, err)
	}
	// 占用者被软删后不再撞唯一索引：重映射成功，并如实报告图片侧改写计数。
	if err := database.DB.Delete(&occupant).Error; err != nil {
		t.Fatal(err)
	}
	imageDir := models.ImageDirectory{Path: filepath.Join(oldRoot, "pics")}
	if err := database.DB.Create(&imageDir).Error; err != nil {
		t.Fatal(err)
	}
	result, err := svc.UpdateDirectory(dir.ID, newRoot, "x", DirectoryUpdateModeRemap)
	if err != nil {
		t.Fatalf("冲突解除后重映射应成功: %v", err)
	}
	if result.Rewritten.Images != 1 || result.Rewritten.ImageDirectories != 1 {
		t.Fatalf("结果应如实返回图片侧改写计数: %+v", result.Rewritten)
	}
	var rewritten models.Image
	if err := database.DB.First(&rewritten, moved.ID).Error; err != nil || rewritten.Path != filepath.Join(newRoot, "pics", "a.jpg") {
		t.Fatalf("图片路径应被改写: %+v err=%v", rewritten, err)
	}
}

// ---------- M8：StopPlaybackRelocation 可安全重入 ----------

func TestStopPlaybackRelocationReentrantKeepsBlockingWhileAnotherStopWaitsLIB13M8(t *testing.T) {
	// 模拟另一个 Stop 仍在 Wait：它登记着停止者身份。这一个 Stop 先返回时不得重新放行登记。
	playbackRelocateMu.Lock()
	playbackRelocateStoppers++
	playbackRelocateMu.Unlock()
	StopPlaybackRelocation()
	if _, ok := beginPlaybackRelocation(); ok {
		playbackRelocateWG.Done()
		t.Fatal("还有停止者在等待时不得登记新的重定位")
	}
	playbackRelocateMu.Lock()
	playbackRelocateStoppers--
	playbackRelocateMu.Unlock()
	if _, ok := beginPlaybackRelocation(); !ok {
		t.Fatal("最后一个停止者返回后应允许新的重定位")
	}
	playbackRelocateWG.Done()
}

func TestStopPlaybackRelocationConcurrentStopsLIB13M8(t *testing.T) {
	stop := make(chan struct{})
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
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
	var stoppers sync.WaitGroup
	for s := 0; s < 6; s++ {
		stoppers.Add(1)
		go func() {
			defer stoppers.Done()
			for i := 0; i < 30; i++ {
				StopPlaybackRelocation()
			}
		}()
	}
	stoppers.Wait()
	close(stop)
	workers.Wait()
	StopPlaybackRelocation()
	if _, ok := beginPlaybackRelocation(); !ok {
		t.Fatal("全部 Stop 返回后应允许新的重定位")
	}
	playbackRelocateWG.Done()
}

// ---------- M9：legacy_trash 行的轻量放回判定 ----------

func TestLIB05LegacyPutBackReportedRestorableNotFileGoneM9(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video := p010Video(t, filepath.Join(t.TempDir(), "movies", "legacy.mp4"), "legacy-put-back")
	entry := reviewDLegacyEntry(t, video)
	// 用户把旧版 trash/ 里的文件挪回原处：大小与 inode 不变。
	if err := os.Rename(entry.TrashPath, video.Path); err != nil {
		t.Fatal(err)
	}
	center := NewTrashCenter(svc, nil)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("列表失败: %#v err=%v", page, err)
	}
	item := page.Items[0]
	if !item.PutBack || item.State != trashStateDeleted || len(item.Actions) != 1 || item.Actions[0] != TrashActionRestore {
		t.Fatalf("legacy 放回应报告可恢复而不是 file_gone: %#v", item)
	}
	if purge, err := center.PurgeTrashEntries("video", []uint{entry.ID}); err != nil || purge.Items[0].Code != TrashResultNotPurgeable {
		t.Fatalf("legacy 放回的条目不得清除: %#v err=%v", purge, err)
	}
	restored, err := center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || restored.Succeeded != 1 {
		t.Fatalf("legacy 放回应能恢复: %#v err=%v", restored, err)
	}
	if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("原记录应恢复: %v", err)
	}
}

func TestLIB05LegacyFileGoneRevivedOnPutBackAndOtherInodeStaysGoneM9(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	back := p010Video(t, filepath.Join(root, "a", "back.mp4"), "back-content")
	other := p010Video(t, filepath.Join(root, "b", "other.mp4"), "other-content")
	backEntry := reviewDLegacyEntry(t, back)
	otherEntry := reviewDLegacyEntry(t, other)
	held := filepath.Join(root, "held.mp4")
	if err := os.Rename(backEntry.TrashPath, held); err != nil {
		t.Fatal(err)
	}
	// other：用户清掉了 trash/，原位置写了一个同样大小的新文件（inode 不同）。
	if err := os.Remove(otherEntry.TrashPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other.Path, []byte("other-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(other.Path); err != nil || sameFileInode(otherEntry.FileIdentity, info) {
		t.Skip("当前文件系统复用了 inode，无法构造该场景")
	}
	center := NewTrashCenter(svc, nil)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("列表失败: %#v err=%v", page, err)
	}
	for _, item := range page.Items {
		if item.State != models.TrashStateFileGone || item.PutBack {
			t.Fatalf("文件不在且原处不是同一 inode 时应为 file_gone: %#v", item)
		}
	}
	// back 被放回：file_gone 改回 deleted 并报告可恢复。
	if err := os.Rename(held, back.Path); err != nil {
		t.Fatal(err)
	}
	page, err = center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		switch item.ID {
		case backEntry.ID:
			if !item.PutBack || item.State != trashStateDeleted {
				t.Fatalf("放回后应改回 deleted 并可恢复: %#v", item)
			}
		case otherEntry.ID:
			if item.PutBack || item.State != models.TrashStateFileGone {
				t.Fatalf("inode 不同的新文件不是放回: %#v", item)
			}
		}
	}
}

// ---------- M10：用量不计已放回的行 ----------

func TestLIB05TrashUsageExcludesPutBackRowsM10(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	putBack := p010Video(t, filepath.Join(root, "t", "back.mp4"), "back-1234")   // 9 字节，放回
	inTrash := p010Video(t, filepath.Join(root, "t", "kept.mp4"), "kept-123456") // 11 字节，仍在废纸篓
	for _, video := range []models.Video{putBack, inTrash} {
		mustSetFileModTime(t, video.Path, time.Now().Add(-time.Hour))
		if err := svc.DeleteVideo(video.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	backEntry := p010VideoEntry(t, putBack.ID)
	if err := os.Rename(backEntry.TrashPath, putBack.Path); err != nil {
		t.Fatal(err)
	}
	legacyBack := p010Video(t, filepath.Join(root, "l", "lback.mp4"), "legacy-back-123")     // 15 字节，放回
	legacyKept := p010Video(t, filepath.Join(root, "m", "lkept.mp4"), "legacy-kept-1234567") // 19 字节
	legacyBackEntry := reviewDLegacyEntry(t, legacyBack)
	reviewDLegacyEntry(t, legacyKept)
	if err := os.Rename(legacyBackEntry.TrashPath, legacyBack.Path); err != nil {
		t.Fatal(err)
	}
	usage, err := NewTrashCenter(svc, nil).GetTrashUsage()
	if err != nil {
		t.Fatal(err)
	}
	if usage.Video.BytesInTrash != int64(len("kept-123456")+len("legacy-kept-1234567")) {
		t.Fatalf("bytes_in_trash 不应计已放回的行: %+v", usage.Video)
	}
	if usage.Video.LegacyBytes != int64(len("legacy-kept-1234567")) || usage.Video.LegacyCount != 2 || usage.Video.Count != 4 {
		t.Fatalf("legacy 字节同样不计已放回的行，条目数不变: %+v", usage.Video)
	}
}

// ---------- M11：迁移与重映射等待图片锁 ----------

func reviewDWaitsForImageLock(t *testing.T, label string, run func() error) {
	t.Helper()
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
	go func() { done <- run() }()
	select {
	case err := <-done:
		t.Fatalf("%s：图片路径锁被占用时不应开始: %v", label, err)
	case <-time.After(150 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s：释放后应完成: %v", label, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%s：释放图片锁后应继续", label)
	}
}

func TestMoveDirectoryWaitsForImagePathLockLIB06M11(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	source := filepath.Join(root, "Show")
	destination := filepath.Join(root, "Archive")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	p010Video(t, filepath.Join(source, "e01.mp4"), "episode")
	reviewDWaitsForImageLock(t, "迁移文件夹", func() error {
		_, err := (&VideoService{}).MoveDirectory(source, destination)
		return err
	})
}

func TestRemapDirectoryWaitsForImagePathLockLIB02M11(t *testing.T) {
	setupVideoServiceTestDB(t)
	parent := t.TempDir()
	oldRoot := filepath.Join(parent, "Movies")
	newRoot := filepath.Join(parent, "Movies 1")
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := createDirectoryRow(t, oldRoot)
	reviewDWaitsForImageLock(t, "重映射", func() error {
		_, err := (&DirectoryService{}).UpdateDirectory(dir.ID, newRoot, "x", DirectoryUpdateModeRemap)
		return err
	})
}

// ---------- IMG-13（并入 LIB-14）：图片扫描按路径判定回收站目录 ----------

func TestIMG13UserTrashDirWithoutSoftDeletedImagesIsScanned(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	lower := filepath.Join(root, "trash", "photo.jpg")
	upper := filepath.Join(root, "albums", "Trash", "photo2.jpg")
	mustCreateFile(t, lower)
	mustCreateFile(t, upper)
	// 父目录下没有任何软删图片：没有旧版回收站特征，trash/ 就是用户自己的目录。
	imageTestMustAddDirectory(t, svc, root)
	result := imageTestMustSync(t, svc)
	if result.Added != 2 {
		t.Fatalf("用户自己恰好叫 trash / Trash 的目录应照常收录: %+v", result)
	}
	for _, path := range []string{lower, upper} {
		var count int64
		if err := database.DB.Model(&models.Image{}).Where("path = ?", path).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("%s 应入库: count=%d err=%v", path, count, err)
		}
	}
}

// LIB-07：全量扫描的删除保护同样要解析符号链接。扫描根是指向已卸载卷的软链接时，
// 空挂载点不能被当成「文件都没了」，记录必须保留。
func TestLIB07RemovalGuardResolvesSymlinkedRootToUnmountedVolume(t *testing.T) {
	base := t.TempDir()
	mountPoint := filepath.Join(base, "Volumes", "Ext")
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "Movies")
	if err := os.Symlink(mountPoint, link); err != nil {
		t.Skip("无法创建符号链接")
	}
	guard := scanRemovalGuard{}
	if err := guard.capture(link); err != nil {
		t.Fatalf("对照：卷挂载时应能记下扫描根: %v", err)
	}
	if gone, err := guard.missing(filepath.Join(link, "a.mp4"), nil); err != nil || !gone {
		t.Fatalf("对照：卷挂载时文件不在应判为缺失: gone=%v err=%v", gone, err)
	}
	resolvedMount, err := filepath.EvalSymlinks(mountPoint)
	if err != nil {
		t.Fatal(err)
	}
	remount := reviewAUnmountVolume(t, resolvedMount)
	defer remount()
	if gone, err := guard.missing(filepath.Join(link, "a.mp4"), nil); gone || !errors.Is(err, errScanRootUnavailable) {
		t.Fatalf("软链接指向已卸载的卷时不能判为缺失: gone=%v err=%v", gone, err)
	}
	if err := guard.verify(link); err == nil {
		t.Fatal("软链接指向已卸载的卷时扫描根不应通过复核")
	}
	if err := (scanRemovalGuard{}).capture(link); err == nil {
		t.Fatal("软链接指向已卸载的卷时不应记下扫描根")
	}
}
