package services

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 修复 I（修复 G 复审发现 + 修复 F 复审 m1）的回归测试。测试名里的 LIBxx / IMGxx / APPxx 是问题清单 ID，
// FixI 后面是这一轮的问题编号（IA = I-A，Ma…Mf = m-a…m-f，M1 = 修复 F 复审 m1）。系统废纸篓是替身
// （system_trash_testhook_test.go）：同卷重命名进「原目录/.Trash」。

// reviewIOldLegacyVideo 建一条旧版（legacy_trash）视频条目，文件留在 <目录>/trash/ 里；mtime 调到两小时前，
// 让扫描不会因为「刚修改」跳过 trash/ 里的文件（否则「重扫不新建」的断言没有意义）。
func reviewIOldLegacyVideo(t *testing.T, path, content string) (models.Video, models.VideoTrashEntry) {
	t.Helper()
	video := p010Video(t, path, content)
	mustSetFileModTime(t, path, time.Now().Add(-2*time.Hour))
	return video, reviewDLegacyEntry(t, video)
}

func reviewIActiveUnder(t *testing.T, root string) []models.Video {
	t.Helper()
	var videos []models.Video
	if err := database.DB.Where(`path LIKE ? ESCAPE '\'`, escapeSQLLikePrefix(scanRootChildPrefix(root))+"%").Find(&videos).Error; err != nil {
		t.Fatal(err)
	}
	return videos
}

func reviewISetEntryState(t *testing.T, model interface{}, id uint, state string) {
	t.Helper()
	if err := database.DB.Model(model).Where("id = ?", id).Update("state", state).Error; err != nil {
		t.Fatal(err)
	}
}

// ---------- I-A：移除 legacy 条目后旧版 trash/ 目录不失去登记 ----------

func TestLIB05ForceRemoveLegacyLeavesTombstoneAndRescanSkipsTrashDirFixIIA(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video, entry := reviewIOldLegacyVideo(t, filepath.Join(root, "movies", "old.mp4"), "legacy-in-trash")

	center := NewTrashCenter(svc, nil)
	result, err := center.ForceRemoveTrashRecords("video", []uint{entry.ID}, TrashForceRemoveConfirmText)
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("legacy 行「仍然移除记录」应成功: %#v err=%v", result, err)
	}
	var tomb models.VideoTrashEntry
	if err := database.DB.First(&tomb, entry.ID).Error; err != nil || tomb.State != trashStateRemoved || tomb.TrashPath != entry.TrashPath {
		t.Fatalf("legacy 条目应成为墓碑并保留 trash_path: %#v err=%v", tomb, err)
	}
	if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() {
		t.Fatalf("媒体记录应保持软删: %#v", got)
	}
	if got := reviewGFileContent(t, entry.TrashPath); got != "legacy-in-trash" {
		t.Fatalf("不得动任何文件: %q", got)
	}

	refreshLegacyTrashDirs()
	if !isTrashDir(filepath.Dir(entry.TrashPath)) {
		t.Fatal("墓碑应让旧版 trash/ 目录继续算作已登记")
	}
	scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if scan.Added != 0 || len(scan.Errors) != 0 {
		t.Fatalf("重扫不得把旧版 trash/ 里的文件当成新文件收录: %+v", scan)
	}
	if active := reviewIActiveUnder(t, root); len(active) != 0 {
		t.Fatalf("不应出现任何活跃记录: %#v", active)
	}
}

func TestLIB05ClaimedLegacyRemoveRecordWithHardLinkResidueRescanKeepsOneActiveFixIIA(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video, entry := reviewIOldLegacyVideo(t, filepath.Join(root, "movies", "claimed.mp4"), "claimed-legacy")
	// 旧版恢复在 link 之后中断（或用户以硬链接放回）：原路径与 trash/ 里是同一个文件的两个名字，
	// 修复前的扫描又在原路径上重复收录了一条。
	if err := os.Link(entry.TrashPath, video.Path); err != nil {
		t.Skip("无法创建硬链接")
	}
	dup := models.Video{Name: video.Name, Path: video.Path, Directory: video.Directory, Size: video.Size}
	if err := database.DB.Create(&dup).Error; err != nil {
		t.Fatal(err)
	}

	center := NewTrashCenter(svc, nil)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 1 || !page.Items[0].ClaimedByActive {
		t.Fatalf("应报 claimed_by_active: %#v err=%v", page, err)
	}
	removed, err := center.RemoveGoneTrashEntries("video", []uint{entry.ID})
	if err != nil || removed.Succeeded != 1 {
		t.Fatalf("remove_record 应成功: %#v err=%v", removed, err)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ? AND state = ?", entry.ID, trashStateRemoved); n != 1 {
		t.Fatalf("claimed 的 legacy 行应成为墓碑: %d", n)
	}
	if !reviewDSameInode(t, video.Path, entry.TrashPath) {
		t.Fatal("两个名字都不得被删")
	}

	scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	// 原位置那条活跃记录没有技术信息，扫描会顺手 ffprobe 它（测试文件不是真视频，refresh_metadata 必然失败），与本用例无关。
	for _, scanErr := range scan.Errors {
		if scanErr.Operation != "refresh_metadata" {
			t.Fatalf("重扫出现意外错误: %+v", scan)
		}
	}
	if scan.Added != 0 {
		t.Fatalf("重扫不得收录 trash/ 里的同 inode 残留名字: %+v", scan)
	}
	active := reviewIActiveUnder(t, root)
	if len(active) != 1 || active[0].ID != dup.ID {
		t.Fatalf("重扫后只应有原位置上的那一条活跃记录: %#v", active)
	}
}

func TestLIB05TombstoneHiddenFromListUsageAndTagCountsFixIIA(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video, entry := reviewIOldLegacyVideo(t, filepath.Join(t.TempDir(), "movies", "tagged.mp4"), "tagged-legacy")
	tag := models.Tag{Name: "tomb-tag"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", video.ID, tag.ID).Error; err != nil {
		t.Fatal(err)
	}
	center := NewTrashCenter(svc, nil)
	before, err := center.GetTrashUsage()
	if err != nil || before.Video.Count != 1 || before.Video.LegacyCount != 1 || before.Video.BytesInTrash != video.Size {
		t.Fatalf("前提：墓碑之前计入用量: %+v err=%v", before, err)
	}
	counts, err := (&TagService{}).GetTagUsageCounts([]uint{tag.ID})
	if err != nil || counts[tag.ID].TrashedVideos != 1 {
		t.Fatalf("前提：墓碑之前计入回收站中的标签用量: %#v err=%v", counts, err)
	}

	if result, err := center.ForceRemoveTrashRecords("video", []uint{entry.ID}, TrashForceRemoveConfirmText); err != nil || result.Succeeded != 1 {
		t.Fatalf("移除应成功: %#v err=%v", result, err)
	}

	page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("墓碑不得出现在回收站列表: %#v err=%v", page, err)
	}
	if legacyList, err := svc.ListTrashEntries(); err != nil || len(legacyList) != 0 {
		t.Fatalf("墓碑不得出现在旧版列表接口: %#v err=%v", legacyList, err)
	}
	after, err := center.GetTrashUsage()
	if err != nil || after.Video != (TrashKindUsage{}) {
		t.Fatalf("墓碑不得计入用量: %+v err=%v", after, err)
	}
	counts, err = (&TagService{}).GetTagUsageCounts([]uint{tag.ID})
	if err != nil || counts[tag.ID].TrashedVideos != 0 || counts[tag.ID].Videos != 0 {
		t.Fatalf("墓碑不得计入标签用量: %#v err=%v", counts, err)
	}
	// 墓碑对回收站接口一律视为不存在，也不可恢复。
	for name, call := range map[string]func() (*BatchResult, error){
		"restore": func() (*BatchResult, error) { return center.RestoreTrashEntries("video", []uint{entry.ID}) },
		"purge":   func() (*BatchResult, error) { return center.PurgeTrashEntries("video", []uint{entry.ID}) },
		"remove":  func() (*BatchResult, error) { return center.RemoveGoneTrashEntries("video", []uint{entry.ID}) },
		"force_remove": func() (*BatchResult, error) {
			return center.ForceRemoveTrashRecords("video", []uint{entry.ID}, TrashForceRemoveConfirmText)
		},
	} {
		result, err := call()
		if err != nil || result.Succeeded != 0 || result.Failed != 1 {
			t.Fatalf("%s 对墓碑应失败: %#v err=%v", name, result, err)
		}
	}
	// 修复 L m6：恢复与其他操作同一个「回收站条目不存在」（原先是「该条目当前不可恢复」）。
	if _, err := svc.RestoreTrashEntry(entry.ID); !errors.Is(err, ErrTrashEntryNotFound) {
		t.Fatalf("旧版恢复接口对墓碑应返回回收站条目不存在: %v", err)
	}
	if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() {
		t.Fatalf("记录应保持软删: %#v", got)
	}
}

func TestIMG02ForceRemoveLegacyImageTombstoneAndSyncSkipsTrashDirFixIIA(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	image := imageTrashTestCreateImage(t, filepath.Join(root, "pics", "old.jpg"), "legacy-image-in-trash")
	entry := reviewGLegacyImageEntry(t, image)

	result, err := NewTrashCenter(nil, svc).ForceRemoveTrashRecords("image", []uint{entry.ID}, TrashForceRemoveConfirmText)
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("图片 legacy 行「仍然移除记录」应成功: %#v err=%v", result, err)
	}
	if n := reviewGCountRows(t, &models.ImageTrashEntry{}, "id = ? AND state = ?", entry.ID, trashStateRemoved); n != 1 {
		t.Fatalf("图片 legacy 条目应成为墓碑: %d", n)
	}
	if n := reviewGCountRows(t, &models.Image{}, "id = ? AND deleted_at IS NOT NULL", image.ID); n != 1 {
		t.Fatalf("图片记录应保持软删: %d", n)
	}
	sync := imageTestMustSync(t, svc)
	if sync.Added != 0 {
		t.Fatalf("图片同步不得收录旧版 trash/ 里的文件: %+v", sync)
	}
	if n := reviewGCountRows(t, &models.Image{}, "path = ? AND deleted_at IS NULL", entry.TrashPath); n != 0 {
		t.Fatalf("trash/ 里的文件不应有活跃记录: %d", n)
	}
	if list, err := svc.ListImageTrashEntries(); err != nil || len(list) != 0 {
		t.Fatalf("图片墓碑不得出现在旧版列表接口: %#v err=%v", list, err)
	}
}

// ---------- m-a：原路径是指向废纸篓文件的符号链接时拒绝清除 ----------

func TestLIB05PurgeRefusedWhenOriginalSymlinksToSystemTrashFileFixIMa(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	dir := t.TempDir()
	linked := p010Video(t, filepath.Join(dir, "linked.mp4"), "only-copy-in-trash")
	elsewhere := p010Video(t, filepath.Join(dir, "elsewhere.mp4"), "purgeable-copy")
	for _, video := range []models.Video{linked, elsewhere} {
		mustSetFileModTime(t, video.Path, time.Now().Add(-time.Hour))
		if err := svc.DeleteVideo(video.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	linkedEntry := p010VideoEntry(t, linked.ID)
	elsewhereEntry := p010VideoEntry(t, elsewhere.ID)
	other := filepath.Join(dir, "unrelated.bin")
	if err := os.WriteFile(other, []byte("unrelated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linkedEntry.TrashPath, linked.Path); err != nil {
		t.Skip("无法创建符号链接")
	}
	if err := os.Symlink(other, elsewhere.Path); err != nil {
		t.Fatal(err)
	}

	center := NewTrashCenter(svc, nil)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("列表失败: %#v err=%v", page, err)
	}
	for _, item := range page.Items {
		want := []string{}
		if item.ID == elsewhereEntry.ID {
			want = []string{TrashActionPurge} // 恢复必然失败，清除不影响原位置指向的别的文件
		}
		if !item.OriginalSymlink || fmt.Sprint(item.Actions) != fmt.Sprint(want) {
			t.Fatalf("符号链接行的动作不对: %#v want=%v", item, want)
		}
	}
	purged, err := center.PurgeTrashEntries("video", []uint{linkedEntry.ID})
	if err != nil || purged.Items[0].Code != TrashResultPathOccupied {
		t.Fatalf("指向废纸篓文件的符号链接应让清除拒绝: %#v err=%v", purged, err)
	}
	if got := reviewGFileContent(t, linkedEntry.TrashPath); got != "only-copy-in-trash" {
		t.Fatalf("废纸篓里的唯一一份不得被删: %q", got)
	}
	if got := p011ReloadVideo(t, linked.ID); !got.DeletedAt.IsValid() {
		t.Fatalf("拒绝时记录保持软删: %#v", got)
	}
	purged, err = center.PurgeTrashEntries("video", []uint{elsewhereEntry.ID})
	if err != nil || purged.Succeeded != 1 {
		t.Fatalf("符号链接指向别处时清除照常: %#v err=%v", purged, err)
	}
	if got := reviewGFileContent(t, other); got != "unrelated" {
		t.Fatalf("符号链接指向的别的文件不得受影响: %q", got)
	}
}

// ---------- m-b：legacy 放回在恢复原记录前核对内容哈希 ----------

// reviewILegacyPutBackWithSHA 建一条记录了 file_sha256 的 legacy 条目，并把文件挪回原处（大小与 inode 不变）。
// tamper=true 时再原地改写成同样长度的另一段内容（inode 不变，模拟 inode 被复用或放回后被改过）。
func reviewILegacyPutBackWithSHA(t *testing.T, root string, tamper bool) (models.Video, models.VideoTrashEntry) {
	t.Helper()
	video, entry := reviewIOldLegacyVideo(t, filepath.Join(root, "movies", "hashed.mp4"), "original-bytes-1")
	sha, err := fileSHA256Hex(entry.TrashPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", entry.ID).Update("file_sha256", sha).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(entry.TrashPath, video.Path); err != nil {
		t.Fatal(err)
	}
	if tamper {
		if err := os.WriteFile(video.Path, []byte("replaced-bytes-2"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustSetFileModTime(t, video.Path, time.Now().Add(-2*time.Hour))
	if !putBackAtPath(videoEntryFacts(entry), video.Path) {
		t.Fatal("前提：大小 + inode 判定认为已放回")
	}
	return video, entry
}

func TestLIB05LegacyPutBackWithMismatchedHashIsNotRestoredFixIMb(t *testing.T) {
	t.Run("扫描不恢复、按新文件收录", func(t *testing.T) {
		setupVideoServiceTestDB(t)
		svc := &VideoService{}
		root := t.TempDir()
		video, entry := reviewILegacyPutBackWithSHA(t, root, true)
		scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
		if scan.Restored != 0 || scan.Added != 1 || len(scan.Errors) != 0 {
			t.Fatalf("哈希不一致时不得恢复原记录，应按新文件收录: %+v", scan)
		}
		if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() {
			t.Fatalf("原记录不得被恢复: %#v", got)
		}
		if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ? AND state = ?", entry.ID, trashStateDeleted); n != 1 {
			t.Fatalf("原条目应原样保留: %d", n)
		}
	})
	t.Run("显式恢复拒绝", func(t *testing.T) {
		setupVideoServiceTestDB(t)
		svc := &VideoService{}
		video, entry := reviewILegacyPutBackWithSHA(t, t.TempDir(), true)
		result, err := NewTrashCenter(svc, nil).RestoreTrashEntries("video", []uint{entry.ID})
		if err != nil || result.Succeeded != 0 || !strings.Contains(result.Items[0].Message, "不一致") {
			t.Fatalf("哈希不一致时显式恢复应拒绝: %#v err=%v", result, err)
		}
		if got := p011ReloadVideo(t, video.ID); !got.DeletedAt.IsValid() {
			t.Fatalf("记录保持软删: %#v", got)
		}
		if got := reviewGFileContent(t, video.Path); got != "replaced-bytes-2" {
			t.Fatalf("原路径上的文件不得被动: %q", got)
		}
	})
	t.Run("哈希一致照常恢复", func(t *testing.T) {
		setupVideoServiceTestDB(t)
		svc := &VideoService{}
		root := t.TempDir()
		video, _ := reviewILegacyPutBackWithSHA(t, root, false)
		scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
		if scan.Restored != 1 || scan.Added != 0 {
			t.Fatalf("哈希一致时应恢复原记录: %+v", scan)
		}
		if got := p011ReloadVideo(t, video.ID); got.DeletedAt.IsValid() {
			t.Fatalf("原记录应恢复: %#v", got)
		}
	})
}

func TestIMG02LegacyPutBackWithMismatchedHashIsNotRestoredFixIMb(t *testing.T) {
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
	if err := os.WriteFile(image.Path, []byte("image-bytes-02"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := imageTestMustSync(t, svc)
	if result.Restored != 0 || result.Added != 1 {
		t.Fatalf("图片哈希不一致时不得恢复原记录，应按新文件收录: %+v", result)
	}
	if n := reviewGCountRows(t, &models.Image{}, "id = ? AND deleted_at IS NOT NULL", image.ID); n != 1 {
		t.Fatalf("原图片记录不得被恢复: %d", n)
	}
}

// ---------- m-c：恢复中断（restoring）的条目 ----------

// reviewIInterruptedTrashRestore 删一条视频到（替身）废纸篓，再模拟一次恢复中断：文件已移回原处，条目停在 restoring。
func reviewIInterruptedTrashRestore(t *testing.T, svc *VideoService, path string) (models.Video, models.VideoTrashEntry) {
	t.Helper()
	video := p010Video(t, path, "restoring-content")
	mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	if err := os.Rename(entry.TrashPath, video.Path); err != nil {
		t.Fatal(err)
	}
	reviewISetEntryState(t, &models.VideoTrashEntry{}, entry.ID, trashStateRestoring)
	entry.State = trashStateRestoring
	return video, entry
}

func TestLIB05ScanSkipsPathOfInterruptedRestoreAndStartupReconcileFinishesFixIMc(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video, entry := reviewIInterruptedTrashRestore(t, svc, filepath.Join(root, "movie.mp4"))

	scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if scan.Added != 0 || len(scan.Errors) != 0 {
		t.Fatalf("原路径上有 restoring 条目时扫描不得新建记录: %+v", scan)
	}
	if active := reviewIActiveUnder(t, root); len(active) != 0 {
		t.Fatalf("不应有新的活跃记录: %#v", active)
	}
	if _, err := svc.AddVideo(video.Path); !errors.Is(err, ErrVideoExists) {
		t.Fatalf("手动添加同样跳过: %v", err)
	}
	if err := svc.ReconcileTrashEntries(); err != nil {
		t.Fatalf("启动对账应完成恢复: %v", err)
	}
	if got := p011ReloadVideo(t, video.ID); got.DeletedAt.IsValid() {
		t.Fatalf("原记录应被恢复: %#v", got)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", entry.ID); n != 0 {
		t.Fatalf("条目应按恢复成功移除: %d", n)
	}
	if total := reviewGCountRows(t, &models.Video{}, "path = ?", video.Path); total != 1 {
		t.Fatalf("同路径只应有原记录: %d", total)
	}
}

func TestIMG02SyncSkipsPathOfInterruptedRestoreFixIMc(t *testing.T) {
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
	if result.Added != 0 {
		t.Fatalf("原路径上有 restoring 条目时图片同步不得新建记录: %+v", result)
	}
	if n := reviewGCountRows(t, &models.Image{}, "path = ? AND deleted_at IS NULL", path); n != 0 {
		t.Fatalf("不应有新的活跃图片记录: %d", n)
	}
	if err := svc.ReconcileImageTrashEntries(); err != nil {
		t.Fatalf("启动对账应完成恢复: %v", err)
	}
	if err := database.DB.First(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("原图片记录应被恢复: %v", err)
	}
}

func TestLIB05RestoringClaimedByActiveCanBeRemovedFixIMc(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	dir := t.TempDir()
	// trash 模式：恢复中断、文件已在原处，修复前的扫描又在原位置新建了一条。
	trashed, trashedEntry := reviewIInterruptedTrashRestore(t, svc, filepath.Join(dir, "a", "trashed.mp4"))
	trashedDup := models.Video{Name: trashed.Name, Path: trashed.Path, Directory: trashed.Directory, Size: trashed.Size}
	if err := database.DB.Create(&trashedDup).Error; err != nil {
		t.Fatal(err)
	}
	// legacy_trash：同样的形态。
	legacy, legacyEntry := reviewIOldLegacyVideo(t, filepath.Join(dir, "b", "legacy.mp4"), "legacy-restoring")
	if err := os.Rename(legacyEntry.TrashPath, legacy.Path); err != nil {
		t.Fatal(err)
	}
	reviewISetEntryState(t, &models.VideoTrashEntry{}, legacyEntry.ID, trashStateRestoring)
	legacyDup := models.Video{Name: legacy.Name, Path: legacy.Path, Directory: legacy.Directory, Size: legacy.Size}
	if err := database.DB.Create(&legacyDup).Error; err != nil {
		t.Fatal(err)
	}
	// 对照：恢复中断但原位置没被占用，启动对账能处理。
	plain, _ := reviewIInterruptedTrashRestore(t, svc, filepath.Join(dir, "c", "plain.mp4"))

	// 启动对账对被占用的两行永远报 path_occupied（修复前它们就此卡死：列表无动作、「仍然移除记录」拒绝）。
	if err := svc.ReconcileTrashEntries(); err == nil || !errors.Is(err, ErrTrashPathOccupied) {
		t.Fatalf("前提：启动对账对被占用的 restoring 行报 path_occupied: %v", err)
	}
	if got := p011ReloadVideo(t, plain.ID); got.DeletedAt.IsValid() {
		t.Fatalf("前提：未被占用的 restoring 行由启动对账恢复: %#v", got)
	}

	center := NewTrashCenter(svc, nil)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("列表失败: %#v err=%v", page, err)
	}
	for _, item := range page.Items {
		if item.State != trashStateRestoring || !item.ClaimedByActive || fmt.Sprint(item.Actions) != fmt.Sprint([]string{TrashActionRemoveRecord}) {
			t.Fatalf("被占用的 restoring 行应只提供 remove_record: %#v", item)
		}
	}
	forced, err := center.ForceRemoveTrashRecords("video", []uint{trashedEntry.ID}, TrashForceRemoveConfirmText)
	if err != nil || forced.Succeeded != 1 {
		t.Fatalf("「仍然移除记录」应能处理被占用的 restoring 行: %#v err=%v", forced, err)
	}
	removed, err := center.RemoveGoneTrashEntries("video", []uint{legacyEntry.ID})
	if err != nil || removed.Succeeded != 1 {
		t.Fatalf("remove_record 应能处理被占用的 restoring 行: %#v err=%v", removed, err)
	}
	if n := reviewGCountRows(t, &models.Video{}, "id = ?", trashed.ID); n != 0 {
		t.Fatalf("trash 模式的重复旧记录应被硬删: %d", n)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", trashedEntry.ID); n != 0 {
		t.Fatalf("trash 模式的条目应被硬删: %d", n)
	}
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ? AND state = ?", legacyEntry.ID, trashStateRemoved); n != 1 {
		t.Fatalf("legacy 条目应成为墓碑: %d", n)
	}
	for _, dup := range []models.Video{trashedDup, legacyDup} {
		if err := database.DB.First(&models.Video{}, dup.ID).Error; err != nil {
			t.Fatalf("活跃记录不得受影响: %v", err)
		}
	}
	if got := reviewGFileContent(t, trashed.Path); got != "restoring-content" {
		t.Fatalf("文件不得被动: %q", got)
	}
	if got := reviewGFileContent(t, legacy.Path); got != "legacy-restoring" {
		t.Fatalf("文件不得被动: %q", got)
	}

	// 没被占用的 restoring 行：不是 claimed，「仍然移除记录」拒绝，列表不提供动作。
	unclaimed := p010Video(t, filepath.Join(dir, "d", "unclaimed.mp4"), "unclaimed")
	mustSetFileModTime(t, unclaimed.Path, time.Now().Add(-time.Hour))
	if err := svc.DeleteVideo(unclaimed.ID, true); err != nil {
		t.Fatal(err)
	}
	unclaimedEntry := p010VideoEntry(t, unclaimed.ID)
	reviewISetEntryState(t, &models.VideoTrashEntry{}, unclaimedEntry.ID, trashStateRestoring)
	page, err = center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.ID == unclaimedEntry.ID && (item.ClaimedByActive || len(item.Actions) != 0) {
			t.Fatalf("未被占用的 restoring 行不是 claimed: %#v", item)
		}
	}
	forced, err = center.ForceRemoveTrashRecords("video", []uint{unclaimedEntry.ID}, TrashForceRemoveConfirmText)
	if err != nil || forced.Items[0].Code != TrashResultNotPurgeable {
		t.Fatalf("未被占用的 restoring 行应拒绝: %#v err=%v", forced, err)
	}
	if got := p010VideoEntry(t, unclaimed.ID); got.State != trashStateRestoring {
		t.Fatalf("拒绝时不得改动条目: %#v", got)
	}
}

// ---------- m-d：hardLinkedRegularNames 其余守卫位点与其他未钉住的拒绝 ----------

// reviewILegacyInterruptedVideo 建一条活跃视频与它的旧版中断条目（legacy_trash，state 由调用方给定）：
// 真实文件在 <目录>/trash/ 里，原路径上是指向它的符号链接。
func reviewILegacyInterruptedVideo(t *testing.T, state string) (models.Video, models.VideoTrashEntry) {
	t.Helper()
	video := p010Video(t, filepath.Join(t.TempDir(), "movies", "interrupted.mp4"), "interrupted-only-copy")
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
		FileMoved: state != trashStatePendingMove, FileSize: info.Size(), FileModTime: info.ModTime().UnixNano(),
		FileIdentity: stableFileIdentity(info), State: state, Mode: models.TrashModeLegacyTrash,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	return video, entry
}

func reviewIAssertSymlinkFixtureIntact(t *testing.T, originalPath, trashPath, content string) {
	t.Helper()
	if got := reviewGFileContent(t, trashPath); got != content {
		t.Fatalf("trash/ 里的真实文件必须仍在: %q", got)
	}
	if info, err := os.Lstat(originalPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("原路径上的符号链接不得被改动: %v", err)
	}
}

func TestLIB05LegacyInterruptedDeleteCancelWithSymlinkOriginalKeepsRealFileFixIMd(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	video, entry := reviewILegacyInterruptedVideo(t, trashStatePendingMove)
	// 恢复（= 取消中断的删除）走 cancelInterruptedDeletion：跟随链接的 Stat 会报「同一个文件」。
	if _, err := svc.RestoreTrashEntry(entry.ID); !errors.Is(err, errLegacyResidueNotHardLink) {
		t.Fatalf("原路径是符号链接时取消中断删除应拒绝: %v", err)
	}
	reviewIAssertSymlinkFixtureIntact(t, video.Path, entry.TrashPath, "interrupted-only-copy")
	if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", entry.ID); n != 1 {
		t.Fatalf("条目应保留: %d", n)
	}
	if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("记录应保持活跃: %v", err)
	}
}

func TestLIB05LegacyPendingDeleteReconcileNeverRemovesOriginalViaSymlinkFixIMd(t *testing.T) {
	t.Run("原路径是指向 trash 文件的符号链接", func(t *testing.T) {
		setupVideoServiceTestDB(t)
		svc := &VideoService{}
		video, entry := reviewILegacyInterruptedVideo(t, trashStatePendingMove)
		if err := svc.ReconcileTrashEntries(); !errors.Is(err, errLegacyResidueNotHardLink) {
			t.Fatalf("对账应拒绝删除原路径那个名字: %v", err)
		}
		reviewIAssertSymlinkFixtureIntact(t, video.Path, entry.TrashPath, "interrupted-only-copy")
	})
	t.Run("trash 一侧是指向原文件的符号链接", func(t *testing.T) {
		setupVideoServiceTestDB(t)
		svc := &VideoService{}
		video := p010Video(t, filepath.Join(t.TempDir(), "movies", "real.mp4"), "real-original")
		info, err := os.Stat(video.Path)
		if err != nil {
			t.Fatal(err)
		}
		trashPath := filepath.Join(filepath.Dir(video.Path), DefaultTrashDirName, filepath.Base(video.Path))
		if err := os.MkdirAll(filepath.Dir(trashPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(video.Path, trashPath); err != nil {
			t.Skip("无法创建符号链接")
		}
		entry := models.VideoTrashEntry{
			DeletedBy: "user", VideoID: video.ID, VideoName: video.Name, OriginalPath: video.Path, TrashPath: trashPath,
			FileSize: info.Size(), FileModTime: info.ModTime().UnixNano(), FileIdentity: stableFileIdentity(info),
			State: trashStatePendingMove, Mode: models.TrashModeLegacyTrash,
		}
		if err := database.DB.Create(&entry).Error; err != nil {
			t.Fatal(err)
		}
		if err := svc.ReconcileTrashEntries(); !errors.Is(err, errLegacyResidueNotHardLink) {
			t.Fatalf("对账应拒绝删除原路径上的真实文件: %v", err)
		}
		if got := reviewGFileContent(t, video.Path); got != "real-original" {
			t.Fatalf("原路径上的真实文件不得被删: %q", got)
		}
	})
}

// reviewILegacyInterruptedImage 是 reviewILegacyInterruptedVideo 的图片版本。
func reviewILegacyInterruptedImage(t *testing.T, state string, active bool) (*models.Image, models.ImageTrashEntry) {
	t.Helper()
	image := imageTrashTestCreateImage(t, filepath.Join(t.TempDir(), "pics", "interrupted.jpg"), "image-only-copy")
	info, err := os.Stat(image.Path)
	if err != nil {
		t.Fatal(err)
	}
	trashPath := filepath.Join(filepath.Dir(image.Path), DefaultTrashDirName, filepath.Base(image.Path))
	if err := os.MkdirAll(filepath.Dir(trashPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(image.Path, trashPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(trashPath, image.Path); err != nil {
		t.Skip("无法创建符号链接")
	}
	entry := models.ImageTrashEntry{
		DeletedBy: "user", ImageID: image.ID, ImageName: image.Name, OriginalPath: image.Path, TrashPath: trashPath,
		FileMoved: state != trashStatePendingMove, FileSize: info.Size(), FileModTime: info.ModTime().UnixNano(),
		FileIdentity: stableFileIdentity(info), State: state, Mode: models.TrashModeLegacyTrash,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if !active {
		if err := database.DB.Delete(image).Error; err != nil {
			t.Fatal(err)
		}
	}
	return image, entry
}

func TestIMG02LegacyInterruptedDeletionGuardsWithSymlinksFixIMd(t *testing.T) {
	t.Run("取消中断删除", func(t *testing.T) {
		setupImageServiceTestDB(t)
		svc := NewImageService()
		image, entry := reviewILegacyInterruptedImage(t, trashStatePendingMove, true)
		if _, err := svc.RestoreImageTrashEntry(entry.ID); !errors.Is(err, errLegacyResidueNotHardLink) {
			t.Fatalf("原路径是符号链接时取消中断删除应拒绝: %v", err)
		}
		reviewIAssertSymlinkFixtureIntact(t, image.Path, entry.TrashPath, "image-only-copy")
	})
	t.Run("启动对账 pending_move", func(t *testing.T) {
		setupImageServiceTestDB(t)
		svc := NewImageService()
		image, entry := reviewILegacyInterruptedImage(t, trashStatePendingMove, true)
		if err := svc.ReconcileImageTrashEntries(); !errors.Is(err, errLegacyResidueNotHardLink) {
			t.Fatalf("对账应拒绝删除 trash/ 里的名字: %v", err)
		}
		reviewIAssertSymlinkFixtureIntact(t, image.Path, entry.TrashPath, "image-only-copy")
	})
	t.Run("启动对账 rollback", func(t *testing.T) {
		setupImageServiceTestDB(t)
		svc := NewImageService()
		image, entry := reviewILegacyInterruptedImage(t, trashStateRollback, true)
		if err := svc.ReconcileImageTrashEntries(); !errors.Is(err, errLegacyResidueNotHardLink) {
			t.Fatalf("回滚对账应拒绝删除 trash/ 里的名字: %v", err)
		}
		reviewIAssertSymlinkFixtureIntact(t, image.Path, entry.TrashPath, "image-only-copy")
	})
}

func TestIMG02SymlinkToLegacyTrashFileListRestoreAndPurgeFixIMd(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	image, entry := reviewILegacyInterruptedImage(t, trashStateDeleted, false)
	center := NewTrashCenter(nil, svc)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "image"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("列表失败: %#v err=%v", page, err)
	}
	if item := page.Items[0]; item.PutBack || item.ClaimedByActive || len(item.Actions) != 0 || !item.OriginalSymlink {
		t.Fatalf("图片原位置是指向 trash/ 文件的符号链接时不认定放回、不提供 restore / purge: %#v", item)
	}
	restored, err := center.RestoreTrashEntries("image", []uint{entry.ID})
	if err != nil || restored.Items[0].Code != TrashResultPathOccupied {
		t.Fatalf("图片恢复应拒绝（path_occupied）: %#v err=%v", restored, err)
	}
	purged, err := center.PurgeTrashEntries("image", []uint{entry.ID})
	if err != nil || purged.Items[0].Code != TrashResultPathOccupied {
		t.Fatalf("图片清除应拒绝（path_occupied）: %#v err=%v", purged, err)
	}
	reviewIAssertSymlinkFixtureIntact(t, image.Path, entry.TrashPath, "image-only-copy")
	if n := reviewGCountRows(t, &models.Image{}, "id = ? AND deleted_at IS NOT NULL", image.ID); n != 1 {
		t.Fatalf("图片记录应保持软删: %d", n)
	}
	if got := p010ImageEntry(t, image.ID); got.State != trashStateDeleted {
		t.Fatalf("条目应回到 deleted: %#v", got)
	}
}

func TestLIB05ForceRemoveOnImagesAndInterruptedStatesFixIMd(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	dir := t.TempDir()
	trashed := imageTrashTestCreateImage(t, filepath.Join(dir, "trashed.jpg"), "trashed-photo")
	mustSetFileModTime(t, trashed.Path, time.Now().Add(-time.Hour))
	if err := svc.DeleteImage(trashed.ID, true); err != nil {
		t.Fatal(err)
	}
	trashedEntry := p010ImageEntry(t, trashed.ID)
	rollbackImage, rollbackEntry := reviewILegacyInterruptedImage(t, trashStateRollback, true)
	restoringImage := imageTrashTestCreateImage(t, filepath.Join(dir, "restoring.jpg"), "restoring-photo")
	mustSetFileModTime(t, restoringImage.Path, time.Now().Add(-time.Hour))
	if err := svc.DeleteImage(restoringImage.ID, true); err != nil {
		t.Fatal(err)
	}
	restoringEntry := p010ImageEntry(t, restoringImage.ID)
	reviewISetEntryState(t, &models.ImageTrashEntry{}, restoringEntry.ID, trashStateRestoring)

	center := NewTrashCenter(nil, svc)
	result, err := center.ForceRemoveTrashRecords("image", []uint{trashedEntry.ID, rollbackEntry.ID, restoringEntry.ID}, TrashForceRemoveConfirmText)
	if err != nil || result.Succeeded != 1 || result.Failed != 2 {
		t.Fatalf("图片：trash 行移除，rollback / 未被占用的 restoring 行拒绝: %#v err=%v", result, err)
	}
	for _, item := range result.Items {
		if item.ID != trashedEntry.ID && item.Code != TrashResultNotPurgeable {
			t.Fatalf("中断条目应返回 not_purgeable: %#v", item)
		}
	}
	if n := reviewGCountRows(t, &models.Image{}, "id = ?", trashed.ID); n != 0 {
		t.Fatalf("trash 模式的图片记录应被硬删: %d", n)
	}
	if got := reviewGFileContent(t, trashedEntry.TrashPath); got != "trashed-photo" {
		t.Fatalf("不得动废纸篓里的文件: %q", got)
	}
	if err := database.DB.First(&models.Image{}, rollbackImage.ID).Error; err != nil {
		t.Fatalf("rollback 条目的活跃记录不得被删: %v", err)
	}
	if got := p010ImageEntry(t, restoringImage.ID); got.State != trashStateRestoring {
		t.Fatalf("restoring 条目不得被改动: %#v", got)
	}

	// 视频侧的 rollback 同样拒绝。
	setupVideoServiceTestDB(t)
	videoSvc := &VideoService{}
	video, entry := reviewILegacyInterruptedVideo(t, trashStateRollback)
	videoResult, err := NewTrashCenter(videoSvc, nil).ForceRemoveTrashRecords("video", []uint{entry.ID}, TrashForceRemoveConfirmText)
	if err != nil || videoResult.Items[0].Code != TrashResultNotPurgeable {
		t.Fatalf("视频 rollback 条目应拒绝: %#v err=%v", videoResult, err)
	}
	if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("rollback 条目的活跃记录不得被删: %v", err)
	}
}

func TestLIB04DeleteEntriesReportPermissionDeniedFixIMd(t *testing.T) {
	t.Run("视频：读不到文件（真实 chmod 000）", func(t *testing.T) {
		setupVideoServiceTestDB(t)
		svc := &VideoService{}
		movies := filepath.Join(t.TempDir(), "movies")
		video := p010Video(t, filepath.Join(movies, "locked.mp4"), "locked")
		unlock := reviewGLockDir(t, movies)
		result := svc.DeleteVideosDetailed([]uint{video.ID}, true, BatchDeleteOptions{})
		unlock()
		if result.Items[0].Code != TrashResultPermissionDenied {
			t.Fatalf("读不到文件时删除应报 permission_denied: %#v", result)
		}
		if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
			t.Fatalf("记录应保持活跃: %v", err)
		}
		if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "video_id = ?", video.ID); n != 0 {
			t.Fatalf("不得留下条目: %d", n)
		}
	})
	t.Run("视频：废纸篓拒绝（NSFileWriteNoPermissionError）", func(t *testing.T) {
		setupVideoServiceTestDB(t)
		svc := &VideoService{}
		video := p010Video(t, filepath.Join(t.TempDir(), "denied.mp4"), "denied")
		p010WithSystemTrash(t, func(path string) (string, error) {
			return "", mapSystemTrashError(true, 513, 0, "no permission", path)
		})
		result := svc.DeleteVideosDetailed([]uint{video.ID}, true, BatchDeleteOptions{})
		if result.Items[0].Code != TrashResultPermissionDenied {
			t.Fatalf("废纸篓报没有权限时应返回 permission_denied: %#v", result)
		}
		if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "video_id = ?", video.ID); n != 0 {
			t.Fatalf("pending 条目应被撤销: %d", n)
		}
		if got := reviewGFileContent(t, video.Path); got != "denied" {
			t.Fatalf("文件不得被动: %q", got)
		}
	})
	t.Run("图片：读不到文件（真实 chmod 000）与废纸篓拒绝", func(t *testing.T) {
		setupImageServiceTestDB(t)
		svc := NewImageService()
		pics := filepath.Join(t.TempDir(), "pics")
		locked := imageTrashTestCreateImage(t, filepath.Join(pics, "locked.jpg"), "locked-photo")
		unlock := reviewGLockDir(t, pics)
		result := svc.DeleteImagesDetailed([]uint{locked.ID}, true, BatchDeleteOptions{})
		unlock()
		if result.Items[0].Code != TrashResultPermissionDenied {
			t.Fatalf("图片读不到文件时删除应报 permission_denied: %#v", result)
		}
		denied := imageTrashTestCreateImage(t, filepath.Join(t.TempDir(), "denied.jpg"), "denied-photo")
		p010WithSystemTrash(t, func(path string) (string, error) {
			return "", mapSystemTrashError(true, 513, 0, "no permission", path)
		})
		result = svc.DeleteImagesDetailed([]uint{denied.ID}, true, BatchDeleteOptions{})
		if result.Items[0].Code != TrashResultPermissionDenied {
			t.Fatalf("图片废纸篓报没有权限时应返回 permission_denied: %#v", result)
		}
		for _, image := range []*models.Image{locked, denied} {
			if err := database.DB.First(&models.Image{}, image.ID).Error; err != nil {
				t.Fatalf("图片记录应保持活跃: %v", err)
			}
		}
	})
}

// ---------- m-e：「仍然移除记录」不对 record_only 开放 ----------

func TestLIB05ForceRemoveRejectsRecordOnlyFixIMe(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	dir := t.TempDir()
	blocked := p010Video(t, filepath.Join(dir, "blocked.mp4"), "blocked")
	if err := svc.DeleteVideo(blocked.ID, false); err != nil {
		t.Fatal(err)
	}
	blockedEntry := p010VideoEntry(t, blocked.ID)
	if blockedEntry.Mode != models.TrashModeRecordOnly {
		t.Fatalf("前提：只删记录建 record_only 条目: %#v", blockedEntry)
	}
	missing := models.Video{Name: "missing.mp4", Path: filepath.Join(dir, "missing.mp4"), Directory: dir, Size: 1}
	if err := database.DB.Create(&missing).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVideo(missing.ID, true); err != nil {
		t.Fatal(err)
	}
	missingEntry := p010VideoEntry(t, missing.ID)
	if missingEntry.Mode != models.TrashModeMissing {
		t.Fatalf("前提：文件不在时建 missing 条目: %#v", missingEntry)
	}

	center := NewTrashCenter(svc, nil)
	result, err := center.ForceRemoveTrashRecords("video", []uint{blockedEntry.ID, missingEntry.ID}, TrashForceRemoveConfirmText)
	if err != nil || result.Succeeded != 1 || result.Failed != 1 {
		t.Fatalf("record_only 拒绝、missing 移除: %#v err=%v", result, err)
	}
	if item := result.Items[0]; item.ID != blockedEntry.ID || item.Code != TrashResultNotPurgeable || !strings.Contains(item.Message, "允许重新收录") {
		t.Fatalf("record_only 应返回 not_purgeable 与可读原因: %#v", item)
	}
	if got := p010VideoEntry(t, blocked.ID); got.State != trashStateDeleted || got.Mode != models.TrashModeRecordOnly {
		t.Fatalf("record_only 条目（屏蔽）不得被移除: %#v", got)
	}
	if n := reviewGCountRows(t, &models.Video{}, "id = ?", missing.ID); n != 0 {
		t.Fatalf("missing 行应被硬删: %d", n)
	}

	setupImageServiceTestDB(t)
	imageSvc := NewImageService()
	photo := imageTrashTestCreateImage(t, filepath.Join(t.TempDir(), "blocked.jpg"), "blocked-photo")
	if err := imageSvc.DeleteImage(photo.ID, false); err != nil {
		t.Fatal(err)
	}
	photoEntry := p010ImageEntry(t, photo.ID)
	imageResult, err := NewTrashCenter(nil, imageSvc).ForceRemoveTrashRecords("image", []uint{photoEntry.ID}, TrashForceRemoveConfirmText)
	if err != nil || imageResult.Items[0].Code != TrashResultNotPurgeable {
		t.Fatalf("图片 record_only 同样拒绝: %#v err=%v", imageResult, err)
	}
	if n := reviewGCountRows(t, &models.ImageTrashEntry{}, "id = ?", photoEntry.ID); n != 1 {
		t.Fatalf("图片 record_only 条目不得被移除: %d", n)
	}
}

// ---------- m-f：同一路径不是「两个名字」；硬链接数在 unix 上都能读到 ----------

func TestLIB05HardLinkedRegularNamesSamePathIsFalseFixIMf(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.mp4")
	if err := os.WriteFile(a, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := filepath.Join(dir, "b.mp4")
	if err := os.Link(a, b); err != nil {
		t.Skip("无法创建硬链接")
	}
	if hardLinkedRegularNames(a, a) {
		t.Fatal("同一个路径不是同一文件的两个名字（硬链接数 ≥ 2 只说明别处还有名字）")
	}
	if hardLinkedRegularNames(a, dir+string(os.PathSeparator)+"."+string(os.PathSeparator)+"a.mp4") {
		t.Fatal("清理后相同的路径同样不算两个名字")
	}
	if !hardLinkedRegularNames(a, b) {
		t.Fatal("真正的两个硬链接名字应成立")
	}
	info, err := os.Lstat(a)
	if err != nil {
		t.Fatal(err)
	}
	links, ok := fileLinkCount(info)
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		if !ok || links != 2 {
			t.Fatalf("unix 上应读到硬链接数 2: %d %v", links, ok)
		}
	}
}

// ---------- 修复 F 复审 m1：维护终态下路径读锁不永久阻塞 ----------

// reviewIHoldPathWriteLockAndFence 模拟 enterDatabaseRestoreMode 之后的「待重启」终态：路径写锁被持有，维护围栏生效。
// 返回按相反顺序撤掉两者的函数（测试结束时一定撤掉）。
func reviewIHoldPathWriteLockAndFence(t *testing.T) func() {
	t.Helper()
	libraryPathMutationMu.Lock()
	release := database.BeginMaintenance()
	done := false
	undo := func() {
		if !done {
			done = true
			release()
			libraryPathMutationMu.Unlock()
		}
	}
	t.Cleanup(undo)
	return undo
}

func reviewIWithin(t *testing.T, label string, limit time.Duration, run func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("%s 在维护终态下挂住了（%v 内没有返回）", label, limit)
		return nil
	}
}

func reviewIScanResultErr(result *ScanSyncResult) error {
	if result == nil || len(result.Errors) == 0 {
		return nil
	}
	return errors.New(result.Errors[0].Error)
}

func TestAPP02PathReadLockEntriesReturnMaintenanceWhenFencedFixIM1(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video := p010Video(t, filepath.Join(root, "a.mp4"), "a")
	undo := reviewIHoldPathWriteLockAndFence(t)
	defer undo()

	isMaintenance := func(err error) bool {
		return err != nil && (errors.Is(err, database.ErrMaintenance) || strings.Contains(err.Error(), database.ErrMaintenance.Error()))
	}
	entries := []struct {
		name string
		run  func() error
	}{
		{"AddDirectory", func() error { _, err := (&DirectoryService{}).AddDirectory(root, "x"); return err }},
		{"DeleteDirectory", func() error { return (&DirectoryService{}).DeleteDirectory(1) }},
		{"AddVideo", func() error { _, err := svc.AddVideo(video.Path); return err }},
		{"DeleteVideo", func() error { return svc.DeleteVideo(video.ID, true) }},
		{"RelocateVideo", func() error { return svc.RelocateVideo(video.ID, video.Path) }},
		{"RenameVideo", func() error { return svc.RenameVideo(video.ID, "b.mp4") }},
		{"MarkVideosStaleUnderRemovedRoot", func() error { _, err := svc.MarkVideosStaleUnderRemovedRoot(root, nil); return err }},
		{"SyncScanDirectories", func() error {
			return reviewIScanResultErr(svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}}))
		}},
		{"SyncAffectedDirectories", func() error {
			return reviewIScanResultErr(svc.SyncAffectedDirectories([]models.ScanDirectory{{Path: root}}, []string{root}))
		}},
		{"BatchDeleteVideos", func() error {
			result := svc.BatchDeleteVideos([]uint{video.ID}, true)
			if len(result.Errors) != 1 {
				return nil
			}
			return errors.New(result.Errors[0].Error)
		}},
		{"DeleteVideosDetailed", func() error {
			result := svc.DeleteVideosDetailed([]uint{video.ID}, true, BatchDeleteOptions{})
			return errors.New(result.Items[0].Message)
		}},
	}
	for _, entry := range entries {
		if err := reviewIWithin(t, entry.name, 3*time.Second, entry.run); !isMaintenance(err) {
			t.Fatalf("%s 在维护终态下应立即返回 ErrMaintenance: %v", entry.name, err)
		}
	}
	undo()
	// 撤掉之后一切照旧：文件、记录都没被动过。
	if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("记录不得被改动: %v", err)
	}
	if got := reviewGFileContent(t, video.Path); got != "a" {
		t.Fatalf("文件不得被改动: %q", got)
	}
}

func TestAPP02PathReadLockRechecksFenceWhileWaitingFixIM1(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	// 先只有写锁（enterDatabaseRestoreMode 拿到路径写锁、还没立围栏的那一刻）：读者在等。
	libraryPathMutationMu.Lock()
	locked := true
	var release func()
	t.Cleanup(func() {
		if release != nil {
			release()
		}
		if locked {
			libraryPathMutationMu.Unlock()
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := (&DirectoryService{}).AddDirectory(root, "x")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("写锁被持有、围栏未生效时读者应等待: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	// 围栏随后生效：等待中的读者必须由「维护开始」通知唤醒并返回（修复 L m3，原先是下一次轮询时发现），而不是一直等写锁。
	release = database.BeginMaintenance()
	select {
	case err := <-done:
		if !errors.Is(err, database.ErrMaintenance) {
			t.Fatalf("围栏生效后应返回 ErrMaintenance: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("围栏生效后等待中的读者没有返回")
	}
	release()
	release = nil
	libraryPathMutationMu.Unlock()
	locked = false
	var count int64
	if err := database.DB.Model(&models.ScanDirectory{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("被拒绝的读者不得写入: %d err=%v", count, err)
	}
}

func TestAPP02PathReadLockKeepsWriterPriorityWithoutFenceFixIM1(t *testing.T) {
	setupVideoServiceTestDB(t)
	firstRelease, err := rLockLibraryPaths()
	if err != nil {
		t.Fatalf("无围栏时应拿到读锁: %v", err)
	}
	order := make(chan string, 2)
	writerDone := make(chan struct{})
	go func() {
		libraryPathMutationMu.Lock()
		order <- "writer"
		libraryPathMutationMu.Unlock()
		close(writerDone)
	}()
	// 等写者进入等待：有写者在等时 TryRLock 失败。
	deadline := time.Now().Add(3 * time.Second)
	for {
		if !libraryPathMutationMu.TryRLock() {
			break
		}
		libraryPathMutationMu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("写者没有进入等待")
		}
		runtime.Gosched()
	}
	readerErr := make(chan error, 1)
	go func() {
		release, err := rLockLibraryPaths()
		if err == nil {
			order <- "reader"
			release()
		}
		readerErr <- err
	}()
	select {
	case got := <-order:
		t.Fatalf("有写者在等时新读者不得插队: %s", got)
	case <-time.After(150 * time.Millisecond):
	}
	firstRelease()
	if first := <-order; first != "writer" {
		t.Fatalf("写者应先于新读者拿到锁: %s", first)
	}
	if second := <-order; second != "reader" {
		t.Fatalf("写者之后新读者应拿到锁: %s", second)
	}
	if err := <-readerErr; err != nil {
		t.Fatalf("无围栏时读者不应失败: %v", err)
	}
	<-writerDone
}

func TestAPP02NoDirectPathReadLockOutsideHelperFixIM1(t *testing.T) {
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range matches {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// 修复 L m3：读锁经 acquireLibraryPath 以方法值（libraryPathMutationMu.RLock，不是调用）传入，
			// 所以查所有选择子表达式，调用与方法值都算获取：只有 rLockLibraryPaths 可以引用 RLock / TryRLock。
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				owner, ok := selector.X.(*ast.Ident)
				if !ok || owner.Name != "libraryPathMutationMu" {
					return true
				}
				switch selector.Sel.Name {
				case "RLock", "TryRLock":
					if fn.Name.Name != "rLockLibraryPaths" {
						t.Errorf("%s:%s 在 rLockLibraryPaths 之外获取路径读锁（libraryPathMutationMu.%s），应改用 rLockLibraryPaths（维护终态会永久阻塞）",
							name, fn.Name.Name, selector.Sel.Name)
					}
				}
				return true
			})
		}
	}
}
