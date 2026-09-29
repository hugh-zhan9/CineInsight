package services

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 修复 G（修复 D 复审提出的回收站问题）的回归测试。测试名里的 LIBxx / IMGxx 是问题清单 ID，
// I1 / Mn 是修复 D 复审的问题编号（I-1、m1…m8）。系统废纸篓是替身（system_trash_testhook_test.go）：
// 同卷重命名进「原目录/.Trash」。

// reviewGLockDir 把 dir 真实 chmod 000（m4：不再用 stat 替身模拟 EPERM），返回提前恢复权限的函数；
// 测试结束时一定恢复。以 root 运行时权限位不起作用，跳过。
func reviewGLockDir(t *testing.T, dir string) func() {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行时 chmod 000 不能阻止访问")
	}
	if err := os.Chmod(dir, 0); err != nil {
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

// reviewGLegacyImageEntry 构造一条旧版（legacy_trash）图片条目：文件被旧版应用以硬链接 + 删除原名的方式
// 移进 <目录>/trash/，记录已由用户软删。
func reviewGLegacyImageEntry(t *testing.T, image *models.Image) models.ImageTrashEntry {
	t.Helper()
	info, err := os.Stat(image.Path)
	if err != nil {
		t.Fatal(err)
	}
	trashPath := filepath.Join(filepath.Dir(image.Path), DefaultTrashDirName, filepath.Base(image.Path))
	if err := os.MkdirAll(filepath.Dir(trashPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(image.Path, trashPath); err != nil {
		t.Skip("无法创建硬链接")
	}
	if err := os.Remove(image.Path); err != nil {
		t.Fatal(err)
	}
	entry := models.ImageTrashEntry{
		DeletedBy: "user", ImageID: image.ID, ImageName: image.Name, OriginalPath: image.Path, TrashPath: trashPath,
		FileMoved: true, FileSize: info.Size(), FileModTime: info.ModTime().UnixNano(), FileIdentity: stableFileIdentity(info),
		State: trashStateDeleted, Mode: models.TrashModeLegacyTrash,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(image).Update("deleted_by", "user").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(image).Error; err != nil {
		t.Fatal(err)
	}
	return entry
}

func reviewGCountRows(t *testing.T, model interface{}, where string, args ...interface{}) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Unscoped().Model(model).Where(where, args...).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func reviewGFileContent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", filepath.Base(path), err)
	}
	return string(data)
}

// ---------- I-1 源头：legacy 放回后扫描恢复原记录，不重复收录 ----------

func TestLIB05ScanRestoresLegacyPutBackInsteadOfDuplicatingI1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video := p010Video(t, filepath.Join(root, "movies", "old.mp4"), "legacy-scan-content")
	tag := models.Tag{Name: "legacy-keep"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&video).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	entry := reviewDLegacyEntry(t, video)
	// 用户把旧版 trash/ 里的文件挪回原处：大小与 inode 不变，mtime 不可信（旧行不比）。
	if err := os.Rename(entry.TrashPath, video.Path); err != nil {
		t.Fatal(err)
	}
	mustSetFileModTime(t, video.Path, time.Now().Add(-2*time.Hour))

	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if result.Restored != 1 || result.Added != 0 || len(result.Errors) != 0 {
		t.Fatalf("扫描应恢复 legacy 原记录而不是新建: %+v", result)
	}
	var restored models.Video
	if err := database.DB.Preload("Tags").First(&restored, video.ID).Error; err != nil || len(restored.Tags) != 1 || restored.Tags[0].ID != tag.ID {
		t.Fatalf("原 ID 与标签应保留: %#v err=%v", restored, err)
	}
	if total := reviewGCountRows(t, &models.Video{}, "path = ?", video.Path); total != 1 {
		t.Fatalf("同路径只应有原记录: %d", total)
	}
	if left := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", entry.ID); left != 0 {
		t.Fatalf("条目应按恢复成功语义移除: %d", left)
	}
	// 再扫一轮：仍然只有这一条。
	if again := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}}); again.Added != 0 {
		t.Fatalf("第二轮扫描不应新建: %+v", again)
	}
}

func TestIMG02ScanRestoresLegacyPutBackInsteadOfDuplicatingI1(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	image := imageTrashTestCreateImage(t, filepath.Join(root, "pics", "old.jpg"), "legacy-image-bytes")
	entry := reviewGLegacyImageEntry(t, image)
	if err := os.Rename(entry.TrashPath, image.Path); err != nil {
		t.Fatal(err)
	}

	result := imageTestMustSync(t, svc)
	if result.Restored != 1 || result.Added != 0 || len(result.Errors) != 0 {
		t.Fatalf("图片扫描应恢复 legacy 原记录而不是新建: %+v", result)
	}
	if err := database.DB.First(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("原图片记录应被恢复: %v", err)
	}
	if total := reviewGCountRows(t, &models.Image{}, "path = ?", image.Path); total != 1 {
		t.Fatalf("同路径只应有原图片记录: %d", total)
	}
	if left := reviewGCountRows(t, &models.ImageTrashEntry{}, "id = ?", entry.ID); left != 0 {
		t.Fatalf("条目应移除: %d", left)
	}
}

// ---------- I-1 兜底：原路径已有另一条活跃记录时只提供「移除记录」，只删这一行 ----------

func TestLIB05PutBackClaimedByActiveRecordOffersRemoveRecordOnlyI1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()

	// 场景一：legacy 放回后被（修复前的）扫描重复收录。
	legacy := p010Video(t, filepath.Join(root, "a", "legacy.mp4"), "legacy-claimed")
	legacyEntry := reviewDLegacyEntry(t, legacy)
	if err := os.Rename(legacyEntry.TrashPath, legacy.Path); err != nil {
		t.Fatal(err)
	}
	legacyDup := models.Video{Name: legacy.Name, Path: legacy.Path, Directory: legacy.Directory, Size: legacy.Size}
	if err := database.DB.Create(&legacyDup).Error; err != nil {
		t.Fatal(err)
	}

	// 场景二：trash 模式放回、修复 D 之前读不到废纸篓时被重复收录，条目此前已被标成 file_gone；
	// 废纸篓里另有一个同 inode 的名字（硬链接）。
	trashed := p010Video(t, filepath.Join(root, "b", "trashed.mp4"), "trash-claimed")
	mustSetFileModTime(t, trashed.Path, time.Now().Add(-time.Hour))
	if err := svc.DeleteVideo(trashed.ID, true); err != nil {
		t.Fatal(err)
	}
	trashEntry := p010VideoEntry(t, trashed.ID)
	if err := os.Rename(trashEntry.TrashPath, trashed.Path); err != nil {
		t.Fatal(err)
	}
	hardLinked := os.Link(trashed.Path, trashEntry.TrashPath) == nil
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", trashEntry.ID).
		Update("state", models.TrashStateFileGone).Error; err != nil {
		t.Fatal(err)
	}
	trashedDup := models.Video{Name: trashed.Name, Path: trashed.Path, Directory: trashed.Directory, Size: trashed.Size}
	if err := database.DB.Create(&trashedDup).Error; err != nil {
		t.Fatal(err)
	}
	before := map[string]os.FileInfo{}
	for _, path := range []string{legacy.Path, trashed.Path} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = info
	}

	center := NewTrashCenter(svc, nil)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("列表失败: %#v err=%v", page, err)
	}
	for _, item := range page.Items {
		if item.PutBack || !item.ClaimedByActive || len(item.Actions) != 1 || item.Actions[0] != TrashActionRemoveRecord {
			t.Fatalf("原位置已被活跃记录占用时不报 put_back，只提供 remove_record: %#v", item)
		}
		wantState := trashStateDeleted
		if item.ID == trashEntry.ID {
			wantState = models.TrashStateFileGone
		}
		if item.State != wantState {
			t.Fatalf("列表不得改写这类行的状态（file_gone 不得被复活）: %#v", item)
		}
	}
	if got := p010VideoEntry(t, trashed.ID); got.State != models.TrashStateFileGone {
		t.Fatalf("reviveGoneRow 不得把这类行改回 deleted: %#v", got)
	}
	// 恢复仍是 path_occupied（既有行为）。
	if restored, err := center.RestoreTrashEntries("video", []uint{legacyEntry.ID}); err != nil || restored.Items[0].Code != TrashResultPathOccupied {
		t.Fatalf("恢复应报 path_occupied: %#v err=%v", restored, err)
	}

	// 「移除记录」与「清除」都只硬删这一行及其条目。
	if removed, err := center.RemoveGoneTrashEntries("video", []uint{legacyEntry.ID}); err != nil || removed.Succeeded != 1 {
		t.Fatalf("移除记录应成功: %#v err=%v", removed, err)
	}
	if purged, err := center.PurgeTrashEntries("video", []uint{trashEntry.ID}); err != nil || purged.Succeeded != 1 {
		t.Fatalf("清除应只删记录并成功: %#v err=%v", purged, err)
	}
	for _, tc := range []struct {
		old, dup models.Video
		entryID  uint
	}{{legacy, legacyDup, legacyEntry.ID}, {trashed, trashedDup, trashEntry.ID}} {
		if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", tc.entryID); n != 0 {
			t.Fatalf("条目应被硬删: %d", n)
		}
		if n := reviewGCountRows(t, &models.Video{}, "id = ?", tc.old.ID); n != 0 {
			t.Fatalf("重复的旧记录应被硬删: %d", n)
		}
		var dup models.Video
		if err := database.DB.First(&dup, tc.dup.ID).Error; err != nil || dup.Path != tc.old.Path {
			t.Fatalf("活跃记录不得受影响: %#v err=%v", dup, err)
		}
		info, err := os.Stat(tc.old.Path)
		if err != nil || !os.SameFile(info, before[tc.old.Path]) || info.Size() != before[tc.old.Path].Size() {
			t.Fatalf("磁盘上的文件不得被动: %v", err)
		}
	}
	if hardLinked {
		if !reviewDSameInode(t, trashed.Path, trashEntry.TrashPath) {
			t.Fatal("清除重复记录时废纸篓里的同 inode 名字也不得被删")
		}
	}
}

func TestIMG02PutBackClaimedByActiveRecordOffersRemoveRecordOnlyI1(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	path := filepath.Join(root, "photo.jpg")
	image := imageTrashTestCreateImage(t, path, "claimed-photo")
	mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010ImageEntry(t, image.ID)
	if err := os.Rename(entry.TrashPath, path); err != nil {
		t.Fatal(err)
	}
	dup := models.Image{Name: image.Name, Path: path, Directory: image.Directory, Size: image.Size}
	if err := database.DB.Create(&dup).Error; err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	center := NewTrashCenter(nil, svc)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "image"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("列表失败: %#v err=%v", page, err)
	}
	item := page.Items[0]
	if item.PutBack || !item.ClaimedByActive || len(item.Actions) != 1 || item.Actions[0] != TrashActionRemoveRecord || item.State != trashStateDeleted {
		t.Fatalf("原位置已被活跃图片占用时只提供 remove_record: %#v", item)
	}
	if removed, err := center.RemoveGoneTrashEntries("image", []uint{entry.ID}); err != nil || removed.Succeeded != 1 {
		t.Fatalf("移除记录应成功: %#v err=%v", removed, err)
	}
	if n := reviewGCountRows(t, &models.ImageTrashEntry{}, "id = ?", entry.ID); n != 0 {
		t.Fatalf("条目应被硬删: %d", n)
	}
	if n := reviewGCountRows(t, &models.Image{}, "id = ?", image.ID); n != 0 {
		t.Fatalf("重复的旧图片记录应被硬删: %d", n)
	}
	if err := database.DB.First(&models.Image{}, dup.ID).Error; err != nil {
		t.Fatalf("活跃图片记录不得受影响: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || reviewGFileContent(t, path) != "claimed-photo" {
		t.Fatalf("磁盘上的文件不得被动: %v", err)
	}
}

// ---------- m1：符号链接不会导致永久删除 ----------

func TestLIB05SymlinkToLegacyTrashFileIsNotPutBackAndRestoreKeepsRealFileM1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video := p010Video(t, filepath.Join(t.TempDir(), "movies", "linked.mp4"), "the-only-copy")
	entry := reviewDLegacyEntry(t, video)
	// 原路径上是一个指向旧版 trash/ 里真实文件的符号链接。
	if err := os.Symlink(entry.TrashPath, video.Path); err != nil {
		t.Skip("无法创建符号链接")
	}
	center := NewTrashCenter(svc, nil)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 1 || page.Items[0].PutBack || page.Items[0].ClaimedByActive || page.Items[0].State != trashStateDeleted {
		t.Fatalf("原路径是符号链接时不得认定放回: %#v err=%v", page, err)
	}
	if putBackAtPath(videoEntryFacts(entry), video.Path) {
		t.Fatal("放回判定不得跟随符号链接")
	}

	restored, err := center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || restored.Items[0].Code != TrashResultPathOccupied {
		t.Fatalf("原位置是符号链接时恢复应拒绝（path_occupied）: %#v err=%v", restored, err)
	}
	if got := reviewGFileContent(t, entry.TrashPath); got != "the-only-copy" {
		t.Fatalf("旧版 trash/ 里的真实文件必须仍在: %q", got)
	}
	if info, err := os.Lstat(video.Path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("原路径上的符号链接不得被改动: %v", err)
	}
	if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() {
		t.Fatalf("记录应仍是软删: %#v", got)
	}
	if got := p010VideoEntry(t, video.ID); got.State != trashStateDeleted {
		t.Fatalf("条目应回到 deleted: %#v", got)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", entry.ID); n != 1 {
		t.Fatalf("条目应保留: %d", n)
	}
}

// 旧版中断回滚（rollback）遇到原路径是指向 trash/ 文件的符号链接：跟随链接会判成「同一个文件」并删掉 trash/
// 里那个名字（其实是唯一的真实文件）。必须什么都不删。
func TestLIB05LegacyRollbackWithSymlinkOriginalKeepsRealFileM1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video := p010Video(t, filepath.Join(t.TempDir(), "movies", "rollback.mp4"), "rollback-only-copy")
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatal(err)
	}
	trashPath := filepath.Join(filepath.Dir(video.Path), DefaultTrashDirName, filepath.Base(video.Path))
	if err := os.MkdirAll(filepath.Dir(trashPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(video.Path, trashPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(trashPath, video.Path); err != nil {
		t.Skip("无法创建符号链接")
	}
	entry := models.VideoTrashEntry{
		DeletedBy: "user", VideoID: video.ID, VideoName: video.Name, OriginalPath: video.Path, TrashPath: trashPath,
		FileMoved: true, FileSize: info.Size(), FileModTime: info.ModTime().UnixNano(), FileIdentity: stableFileIdentity(info),
		State: trashStateRollback, Mode: models.TrashModeLegacyTrash,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileTrashEntries(); err == nil {
		t.Fatal("原路径是符号链接时回滚对账应报错而不是删除")
	}
	if got := reviewGFileContent(t, trashPath); got != "rollback-only-copy" {
		t.Fatalf("trash/ 里的真实文件必须仍在: %q", got)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", entry.ID); n != 1 {
		t.Fatalf("条目应保留等待处理: %d", n)
	}
}

func TestLIB05HardLinkedRegularNamesRequiresRealHardLinkM1(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.mp4")
	if err := os.WriteFile(a, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.mp4")
	if err := os.WriteFile(other, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.mp4")
	if err := os.Link(a, link); err != nil {
		t.Skip("无法创建硬链接")
	}
	symlink := filepath.Join(dir, "sym.mp4")
	if err := os.Symlink(a, symlink); err != nil {
		t.Skip("无法创建符号链接")
	}
	cases := []struct {
		name string
		x, y string
		want bool
	}{
		{"同一文件的两个硬链接", a, link, true},
		{"符号链接与目标", symlink, a, false},
		{"目标与符号链接", a, symlink, false},
		{"内容相同的两个文件", a, other, false},
		{"一侧不存在", a, filepath.Join(dir, "missing.mp4"), false},
	}
	for _, tc := range cases {
		if got := hardLinkedRegularNames(tc.x, tc.y); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}

	// 恢复提交之后的残留清理：原路径是指向 trash/ 文件的符号链接时绝不删，真正的硬链接残留照常清掉。
	trashDir := filepath.Join(dir, DefaultTrashDirName)
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(trashDir, "real.mp4")
	if err := os.WriteFile(real, []byte("only-copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(dir, "real.mp4")
	if err := os.Symlink(real, original); err != nil {
		t.Fatal(err)
	}
	removeLegacyTrashLinkAfterRestore(models.TrashModeLegacyTrash, true, original, real)
	if got := reviewGFileContent(t, real); got != "only-copy" {
		t.Fatalf("原路径是符号链接时不得删除 trash/ 里的真实文件: %q", got)
	}
	residue := filepath.Join(trashDir, "a.mp4")
	if err := os.Link(a, residue); err != nil {
		t.Fatal(err)
	}
	removeLegacyTrashLinkAfterRestore(models.TrashModeLegacyTrash, true, a, residue)
	if _, err := os.Lstat(residue); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("真正的硬链接残留应被清掉: %v", err)
	}
	if got := reviewGFileContent(t, a); got != "a" {
		t.Fatalf("原路径上的文件不得受影响: %q", got)
	}
}

// ---------- m2：旧版启发式排除扫描器造成的软删 ----------

func TestLIB14UserTrashBesideScannerSoftDeletesIsScannedM2(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	// 图片：扫描器发现文件缺失时软删（deleted_by=scanner，不建条目），时间恰在旧版时间段内。
	photoDir := filepath.Join(root, "pics")
	scannedImage := imageTrashTestCreateImage(t, filepath.Join(photoDir, "a.jpg"), "img")
	if err := NewImageService().deleteMissingImageRecord(scannedImage.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(scannedImage.Path); err != nil { // 扫描器正是因为文件不见了才软删
		t.Fatal(err)
	}
	reviewASetDeletedAt(t, &models.Image{}, scannedImage.ID, p010LegacyEraDeletedAt)
	userPhoto := filepath.Join(photoDir, "trash", "a.jpg")
	mustCreateFile(t, userPhoto)
	// 视频：历史上扫描器软删、没有条目的行，同样在旧版时间段内。
	movieDir := filepath.Join(root, "movies")
	scannedVideo := models.Video{Name: "a.mp4", Path: filepath.Join(movieDir, "a.mp4"), Directory: movieDir, Size: 1, DeletedBy: "scanner"}
	if err := database.DB.Create(&scannedVideo).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&scannedVideo).Error; err != nil {
		t.Fatal(err)
	}
	reviewASetDeletedAt(t, &models.Video{}, scannedVideo.ID, p010LegacyEraDeletedAt)
	userMovie := filepath.Join(movieDir, "trash", "a.mp4")
	createOldVideoFile(t, userMovie)

	refreshLegacyTrashDirs()
	if isTrashDir(filepath.Dir(userPhoto)) || isTrashDir(filepath.Dir(userMovie)) {
		t.Fatal("只有扫描器软删的同名媒体时，用户自己的 trash/ 不是旧版回收站")
	}
	result := (&VideoService{}).SyncScanDirectories([]models.ScanDirectory{{Path: movieDir}})
	if result.SkipBreakdown.LegacyTrash != 0 || len(p010ActiveVideos(t, userMovie)) != 1 {
		t.Fatalf("用户 trash/ 里的视频应照常收录: %+v", result)
	}
	imageSvc := NewImageService()
	imageTestMustAddDirectory(t, imageSvc, photoDir)
	imageResult := imageTestMustSync(t, imageSvc)
	if n := reviewGCountRows(t, &models.Image{}, "path = ? AND deleted_at IS NULL", userPhoto); n != 1 {
		t.Fatalf("用户 trash/ 里的图片应照常收录: %+v", imageResult)
	}
	// 对照：同样的形态但软删来自用户（旧版删除）仍按旧版回收站跳过。
	legacyDir := filepath.Join(root, "old")
	p010SoftDeletedVideoIn(t, legacyDir, "b.mp4")
	createOldVideoFile(t, filepath.Join(legacyDir, "trash", "b.mp4"))
	refreshLegacyTrashDirs()
	if !isTrashDir(filepath.Join(legacyDir, "trash")) {
		t.Fatal("旧版删除留下的 trash/ 仍应被认出")
	}
}

// ---------- m3：「仍然移除记录（不动文件）」与权限错误的结果码 ----------

func TestLIB05PermissionDeniedIsNotReportedAsVolumeOfflineM3(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	video := p010Video(t, filepath.Join(lib, "movies", "a.mp4"), "perm-content")
	mustSetFileModTime(t, video.Path, time.Now().Add(-time.Hour))
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	held := filepath.Join(base, "held.mp4")
	if err := os.Rename(entry.TrashPath, held); err != nil {
		t.Fatal(err)
	}
	center := NewTrashCenter(svc, nil)
	if page, err := center.ListTrashEntries(TrashFilter{Kind: "video"}); err != nil || page.Items[0].State != models.TrashStateFileGone {
		t.Fatalf("文件不在时应为 file_gone: %#v err=%v", page, err)
	}
	unlock := reviewGLockDir(t, lib)
	// 解析路径（EvalSymlinks）遇到 EACCES：是权限问题，不是磁盘未连接。
	if err := mediaPathUnavailable(video.Path); !errors.Is(err, ErrTrashLocationPermissionDenied) || !errors.Is(err, ErrTrashPermissionDenied) {
		t.Fatalf("权限错误应报 permission_denied: %v", err)
	}
	removed, err := center.RemoveGoneTrashEntries("video", []uint{entry.ID})
	if err != nil || removed.Items[0].Code != TrashResultPermissionDenied {
		t.Fatalf("移除记录遇到权限错误应返回 permission_denied 而不是 volume_offline: %#v err=%v", removed, err)
	}
	purged, err := center.PurgeTrashEntries("video", []uint{entry.ID})
	if err != nil || purged.Items[0].Code != TrashResultPermissionDenied {
		t.Fatalf("清除遇到权限错误应返回 permission_denied: %#v err=%v", purged, err)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", entry.ID); n != 1 {
		t.Fatalf("拒绝时不得删除条目: %d", n)
	}
	// 扫描根本身读不到：同样是权限问题。
	if err := scanRootAvailability(filepath.Join(lib, "movies")); !errors.Is(err, os.ErrPermission) || errors.Is(err, errScanRootUnavailable) {
		t.Fatalf("扫描根因权限读不到时应报权限错误: %v", err)
	}
	unlock()
	if got := reviewGFileContent(t, held); got != "perm-content" {
		t.Fatalf("文件不得被动: %q", got)
	}
}

func TestLIB05ForceRemoveTrashRecordsRequiresConfirmAndTouchesNoFilesM3(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	base := t.TempDir()
	mount := filepath.Join(base, "external")
	createDirectoryRow(t, mount)
	// 一条仍在废纸篓里的条目、一条 file_gone 条目，所在的卷随后离线。
	kept := p010Video(t, filepath.Join(mount, "kept.mp4"), "kept-in-trash")
	gone := p010Video(t, filepath.Join(mount, "gone.mp4"), "gone-content")
	for _, video := range []models.Video{kept, gone} {
		mustSetFileModTime(t, video.Path, time.Now().Add(-time.Hour))
		if err := svc.DeleteVideo(video.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	keptEntry := p010VideoEntry(t, kept.ID)
	goneEntry := p010VideoEntry(t, gone.ID)
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", goneEntry.ID).
		Update("state", models.TrashStateFileGone).Error; err != nil {
		t.Fatal(err)
	}
	// 一条停在中断删除里的条目（记录仍活跃）：不在「仍然移除记录」的范围内。
	pendingVideo := p010Video(t, filepath.Join(mount, "pending.mp4"), "pending")
	pendingEntry := reviewAPendingTrashEntry(t, pendingVideo)
	reviewAUnmountVolume(t, mount)

	center := NewTrashCenter(svc, nil)
	if removed, err := center.RemoveGoneTrashEntries("video", []uint{goneEntry.ID}); err != nil || removed.Items[0].Code != TrashResultVolumeOffline {
		t.Fatalf("前提：离线时移除记录被拒绝: %#v err=%v", removed, err)
	}
	for _, confirm := range []string{"", "移除", " 移除记录", "移除记录 "} {
		if result, err := center.ForceRemoveTrashRecords("video", []uint{keptEntry.ID, goneEntry.ID}, confirm); !errors.Is(err, ErrTrashForceRemoveUnconfirmed) || result != nil {
			t.Fatalf("确认文案 %q 不符时应整批拒绝: %#v err=%v", confirm, result, err)
		}
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id IN ?", []uint{keptEntry.ID, goneEntry.ID}); n != 2 {
		t.Fatalf("未确认时不得删除任何条目: %d", n)
	}

	result, err := center.ForceRemoveTrashRecords("video", []uint{keptEntry.ID, goneEntry.ID, pendingEntry.ID}, TrashForceRemoveConfirmText)
	if err != nil || result.Succeeded != 2 || result.Failed != 1 {
		t.Fatalf("确认后应移除两条、拒绝中断条目: %#v err=%v", result, err)
	}
	for _, item := range result.Items {
		if item.ID == pendingEntry.ID && item.Code != TrashResultNotPurgeable {
			t.Fatalf("中断条目应返回 not_purgeable: %#v", item)
		}
	}
	for _, id := range []uint{kept.ID, gone.ID} {
		if n := reviewGCountRows(t, &models.Video{}, "id = ?", id); n != 0 {
			t.Fatalf("记录应被硬删: %d", n)
		}
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id IN ?", []uint{keptEntry.ID, goneEntry.ID}); n != 0 {
		t.Fatalf("条目应被硬删: %d", n)
	}
	// 不做任何文件操作：废纸篓里的文件原样保留。
	if got := reviewGFileContent(t, keptEntry.TrashPath); got != "kept-in-trash" {
		t.Fatalf("废纸篓里的文件不得被动: %q", got)
	}
	if got := reviewGFileContent(t, goneEntry.TrashPath); got != "gone-content" {
		t.Fatalf("文件不得被动: %q", got)
	}
	// 中断条目与它的活跃记录保持原样。
	if got := p010VideoEntry(t, pendingVideo.ID); got.State != trashStatePendingMove {
		t.Fatalf("中断条目不得被改动: %#v", got)
	}
	if err := database.DB.First(&models.Video{}, pendingVideo.ID).Error; err != nil {
		t.Fatalf("活跃记录不得被删: %v", err)
	}
}

// ---------- m5：用量按卷分组，离线卷上的行不逐条 stat ----------

func TestLIB05TrashUsageChecksEachVolumeOnceAndSkipsOfflineRowsM5(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	base := t.TempDir()
	mount := filepath.Join(base, "external")
	createDirectoryRow(t, mount)
	contents := []string{"one-1", "two-22", "three-333"}
	videos := make([]models.Video, 0, len(contents))
	for index, content := range contents {
		video := p010Video(t, filepath.Join(mount, "sub", fmt.Sprintf("v%d.mp4", index)), content)
		mustSetFileModTime(t, video.Path, time.Now().Add(-time.Hour))
		if err := svc.DeleteVideo(video.ID, true); err != nil {
			t.Fatal(err)
		}
		videos = append(videos, video)
	}
	// 第一条被放回了原处：卷在线时它不计入字节。
	putBack := p010VideoEntry(t, videos[0].ID)
	if err := os.Rename(putBack.TrashPath, videos[0].Path); err != nil {
		t.Fatal(err)
	}
	center := NewTrashCenter(svc, nil)
	online, err := center.GetTrashUsage()
	if err != nil || online.Video.BytesInTrash != int64(len(contents[1])+len(contents[2])) {
		t.Fatalf("卷在线时不计已放回的行: %+v err=%v", online, err)
	}

	var checks atomic.Int64
	fn := func(path string) error {
		if pathIsEqualOrInside(filepath.Clean(path), mount) {
			checks.Add(1)
			return fmt.Errorf("磁盘未挂载: %w", os.ErrNotExist)
		}
		return scanVolumeAvailable(path)
	}
	previous := mediaVolumeAvailableFn.Swap(&fn)
	t.Cleanup(func() { mediaVolumeAvailableFn.Store(previous) })

	offline, err := center.GetTrashUsage()
	if err != nil {
		t.Fatal(err)
	}
	// 离线卷上的行不 stat 原路径，一律按仍在废纸篓计入（包括其实已放回的那一条）。
	if want := int64(len(contents[0]) + len(contents[1]) + len(contents[2])); offline.Video.BytesInTrash != want || offline.Video.Count != 3 {
		t.Fatalf("离线卷上的行应按未放回计入: %+v want=%d", offline.Video, want)
	}
	if got := checks.Load(); got != 1 {
		t.Fatalf("每个卷只应做一次挂载检查，实际 %d 次", got)
	}
}

// ---------- m6：「其他根是否在线」在拿全局路径读锁之前判定 ----------

func TestMarkRootOfflineChecksOtherRootsBeforePathLockLIB07M6(t *testing.T) {
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

	// 删除 / 恢复正持有全局路径写锁。
	libraryPathMutationMu.Lock()
	locked := true
	unlock := func() {
		if locked {
			locked = false
			libraryPathMutationMu.Unlock()
		}
	}
	defer unlock()
	done := make(chan int64, 1)
	go func() {
		marked, _ := (&VideoService{}).MarkRootOffline(mount)
		done <- marked
	}()
	select {
	case <-otherChecked:
	case <-time.After(3 * time.Second):
		t.Fatal("其他根的在线判定应在拿到全局路径读锁之前完成")
	}
	select {
	case <-done:
		t.Fatal("写锁被占用时写库必须等待")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case marked := <-done:
		if marked != 1 {
			t.Fatalf("释放后应完成标记: %d", marked)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("释放写锁后离线标记应继续")
	}
}

// ---------- m7：旧版回收站目录集合每轮图片同步只刷新一次 ----------

func TestIMG13LegacyTrashIndexRefreshedOncePerImageSyncM7(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "image_service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "refreshLegacyTrashDirs" {
					counts[fn.Name.Name]++
				}
			}
			return true
		})
	}
	if counts["SyncImageDirectories"] != 1 || counts["scanImageDirectory"] != 0 || len(counts) != 1 {
		t.Fatalf("旧版回收站目录集合应只在 SyncImageDirectories 开头刷新一次，不按根刷新: %v", counts)
	}
}
