package services

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// P-010 的回收站、删除与按身份屏蔽测试。测试进程里的系统废纸篓是替身
// （system_trash_testhook_test.go）：同卷重命名进「原目录/.Trash」。

func p010Video(t *testing.T, path, content string) models.Video {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
	video := models.Video{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path), Size: int64(len(content))}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频记录失败: %v", err)
	}
	return video
}

func p010VideoEntry(t *testing.T, videoID uint) models.VideoTrashEntry {
	t.Helper()
	var entry models.VideoTrashEntry
	if err := database.DB.Where("video_id = ?", videoID).First(&entry).Error; err != nil {
		t.Fatalf("读取视频回收站条目失败: %v", err)
	}
	return entry
}

func p010ImageEntry(t *testing.T, imageID uint) models.ImageTrashEntry {
	t.Helper()
	var entry models.ImageTrashEntry
	if err := database.DB.Where("image_id = ?", imageID).First(&entry).Error; err != nil {
		t.Fatalf("读取图片回收站条目失败: %v", err)
	}
	return entry
}

func p010ActiveVideos(t *testing.T, path string) []models.Video {
	t.Helper()
	var videos []models.Video
	if err := database.DB.Where("path = ?", path).Find(&videos).Error; err != nil {
		t.Fatalf("读取活跃视频失败: %v", err)
	}
	return videos
}

func p010WithSystemTrash(t *testing.T, mover func(string) (string, error)) {
	t.Helper()
	previous := systemTrashMove
	systemTrashMove = mover
	t.Cleanup(func() { systemTrashMove = previous })
}

// LIB04 / LIB05：删除并移入（替身）废纸篓 → 同路径新文件入库 → ApplySchema → 新记录仍然活跃。
func TestLIB04DeleteToTrashThenReimportSurvivesApplySchema(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	path := filepath.Join(root, "movie.mp4")
	old := p010Video(t, path, "old-content")

	if err := svc.DeleteVideo(old.ID, true); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	entry := p010VideoEntry(t, old.ID)
	if entry.Mode != models.TrashModeTrash || entry.State != trashStateDeleted || !entry.FileMoved || entry.TrashPath == "" || len(entry.DeleteBatchID) != 32 {
		t.Fatalf("条目应为已移入废纸篓的 trash 模式: %#v", entry)
	}
	if entry.FileSHA256 != "" {
		t.Fatalf("trash 模式不应整文件哈希: %#v", entry)
	}

	// 同路径出现新文件（内容不同）：这是新文件，照常新建记录。
	if err := os.WriteFile(path, []byte("brand-new-file-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := svc.AddVideo(path)
	if err != nil {
		t.Fatalf("同路径新文件应能入库: %v", err)
	}
	if added.ID == old.ID {
		t.Fatalf("应新建记录而不是复活旧记录")
	}

	// 启动时的 ApplySchema 不能再把这条新记录静默软删。
	for round := 1; round <= 2; round++ {
		if err := database.ApplySchema(database.DB); err != nil {
			t.Fatalf("第 %d 次 ApplySchema 失败: %v", round, err)
		}
		if active := p010ActiveVideos(t, path); len(active) != 1 || active[0].ID != added.ID {
			t.Fatalf("第 %d 次 ApplySchema 后新记录应仍然活跃: %#v", round, active)
		}
	}
}

// LIB04：三个场景——record_only 且身份未变 → 屏蔽；身份变了 → 新建；历史行（无条目）按大小判定。
func TestLIB04BlockByIdentityRecordOnlyAndHistoricRows(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()

	// 1. record_only，文件未动 → 屏蔽，且同时满足 ErrVideoExists（既有调用方按 skipped 计）。
	pathA := filepath.Join(root, "a.mp4")
	a := p010Video(t, pathA, "content-a")
	if err := svc.DeleteVideo(a.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddVideo(pathA); !errors.Is(err, ErrVideoBlockedByUserDelete) || !errors.Is(err, ErrVideoExists) {
		t.Fatalf("身份未变应被屏蔽: %v", err)
	}
	if active := p010ActiveVideos(t, pathA); len(active) != 0 {
		t.Fatalf("屏蔽时不应出现活跃记录: %#v", active)
	}

	// 2. record_only，文件被替换（大小变了）→ 新建。
	if err := os.WriteFile(pathA, []byte("replaced-with-longer-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if added, err := svc.AddVideo(pathA); err != nil || added.ID == a.ID {
		t.Fatalf("身份变了应新建记录: %v", err)
	}

	// 3. 历史行：没有条目。大小相同 → 屏蔽；大小不同 → 新建。
	pathB := filepath.Join(root, "b.mp4")
	b := p010Video(t, pathB, "hist")
	if err := database.DB.Delete(&b).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddVideo(pathB); !errors.Is(err, ErrVideoBlockedByUserDelete) {
		t.Fatalf("历史行大小相同应被屏蔽: %v", err)
	}
	if err := os.WriteFile(pathB, []byte("historic-but-different"), 0o644); err != nil {
		t.Fatal(err)
	}
	if added, err := svc.AddVideo(pathB); err != nil || added.ID == b.ID {
		t.Fatalf("历史行大小不同应新建: %v", err)
	}
}

func TestLIB04DecideSoftDeletedPathTable(t *testing.T) {
	cases := []struct {
		name  string
		entry *softDeletedEntryFacts
		row   int64
		size  int64
		mtime int64
		want  softDeletedPathAction
	}{
		{"missing+scanner 自动恢复", &softDeletedEntryFacts{Mode: models.TrashModeMissing, DeletedBy: "scanner"}, 5, 5, 9, softDeletedAutoRestore},
		{"missing+user 新建", &softDeletedEntryFacts{Mode: models.TrashModeMissing, DeletedBy: "user"}, 5, 5, 9, softDeletedCreateNew},
		{"record_only 身份未变 屏蔽", &softDeletedEntryFacts{Mode: models.TrashModeRecordOnly, FileSize: 5, FileModTime: 9, DeleteBatchID: "b"}, 5, 5, 9, softDeletedBlocked},
		{"record_only mtime 变了 新建", &softDeletedEntryFacts{Mode: models.TrashModeRecordOnly, FileSize: 5, FileModTime: 9, DeleteBatchID: "b"}, 5, 5, 10, softDeletedCreateNew},
		{"record_only 大小变了 新建", &softDeletedEntryFacts{Mode: models.TrashModeRecordOnly, FileSize: 5, FileModTime: 9, DeleteBatchID: "b"}, 5, 6, 9, softDeletedCreateNew},
		{"record_only 新时代行无 mtime 只比大小", &softDeletedEntryFacts{Mode: models.TrashModeRecordOnly, FileSize: 5, DeleteBatchID: "b"}, 5, 5, 9, softDeletedBlocked},
		{"trash 新建", &softDeletedEntryFacts{Mode: models.TrashModeTrash, FileSize: 5, FileModTime: 9}, 5, 5, 9, softDeletedCreateNew},
		{"legacy_trash 新建", &softDeletedEntryFacts{Mode: models.TrashModeLegacyTrash}, 5, 5, 9, softDeletedCreateNew},
		{"无条目 大小相同 屏蔽", nil, 5, 5, 9, softDeletedBlocked},
		{"无条目 大小不同 新建", nil, 5, 6, 9, softDeletedCreateNew},
	}
	for _, tc := range cases {
		if got := decideSoftDeletedPath(tc.entry, tc.row, tc.size, tc.mtime, ""); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

// LIB04：扫描同步同样遵守屏蔽表——record_only 且身份未变的文件不被重新收录，被替换的文件会被收录。
func TestLIB04ScanDoesNotReimportRecordOnlyButImportsReplacedFile(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	path := filepath.Join(root, "scan-me.mp4")
	video := p010Video(t, path, "same-content")
	old := time.Now().Add(-10 * time.Minute)
	mustSetFileModTime(t, path, old)
	// 记录里的 size/mtime 以删除时磁盘上的为准。
	if err := svc.DeleteVideo(video.ID, false); err != nil {
		t.Fatal(err)
	}
	dirs := []models.ScanDirectory{{Path: root, Alias: "root"}}
	if result := svc.SyncScanDirectories(dirs); result.Added != 0 {
		t.Fatalf("身份未变不应重新收录: %#v", result)
	}
	if err := os.WriteFile(path, []byte("replaced-file-with-other-size"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSetFileModTime(t, path, old)
	if result := svc.SyncScanDirectories(dirs); result.Added != 1 {
		t.Fatalf("文件被替换后应被收录: %#v", result)
	}
}

// 不支持废纸篓：记录与条目都保持原状，文件原地不动；旧签名返回 ErrTrashUnsupportedVolume（LIB-04 补充场景）。
func TestLIB04UnsupportedVolumeLeavesRecordAndFileUntouched(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	path := filepath.Join(root, "usb.mp4")
	video := p010Video(t, path, "usb-content")
	p010WithSystemTrash(t, func(string) (string, error) { return "", ErrTrashUnsupportedVolume })

	err := svc.DeleteVideo(video.ID, true)
	if !errors.Is(err, ErrTrashUnsupportedVolume) {
		t.Fatalf("旧签名应返回 ErrTrashUnsupportedVolume: %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("文件应原地保留: %v", statErr)
	}
	if active := p010ActiveVideos(t, path); len(active) != 1 {
		t.Fatalf("记录应保持活跃")
	}
	var entries int64
	if err := database.DB.Model(&models.VideoTrashEntry{}).Count(&entries).Error; err != nil || entries != 0 {
		t.Fatalf("不应留下条目: %d err=%v", entries, err)
	}

	result := svc.DeleteVideosDetailed([]uint{video.ID}, true, BatchDeleteOptions{})
	if len(result.Items) != 1 || result.Items[0].Code != TrashResultTrashUnsupported || result.Failed != 1 || len(result.BatchID) != 32 {
		t.Fatalf("新接口应给出 trash_unsupported 结果码与 batch_id: %#v", result)
	}

	// 用户随后选择「永久删除」：核对身份 → 删文件 → 硬删记录。
	perm := svc.PermanentlyDeleteVideos([]uint{video.ID})
	if perm.Succeeded != 1 || perm.Items[0].Code != TrashResultOK {
		t.Fatalf("永久删除应成功: %#v", perm)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("文件应被删除: %v", statErr)
	}
	var count int64
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", video.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("记录应被硬删: %d err=%v", count, err)
	}
}

func TestLIB04PermanentDeleteRefusesWhenFileSizeChanged(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	path := filepath.Join(root, "swapped.mp4")
	video := p010Video(t, path, "original")
	if err := os.WriteFile(path, []byte("a-different-file-entirely"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := svc.PermanentlyDeleteVideos([]uint{video.ID})
	if result.Items[0].Code != TrashResultIdentityMismatch {
		t.Fatalf("身份不符应拒绝永久删除: %#v", result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件必须保留: %v", err)
	}
}

func TestLIB04PermissionDeniedAndVolumeOfflineDoNotDegrade(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	path := filepath.Join(root, "perm.mp4")
	video := p010Video(t, path, "perm")
	p010WithSystemTrash(t, func(string) (string, error) { return "", ErrTrashPermissionDenied })
	result := svc.DeleteVideosDetailed([]uint{video.ID}, true, BatchDeleteOptions{})
	if result.Items[0].Code != TrashResultPermissionDenied {
		t.Fatalf("应返回 permission_denied: %#v", result)
	}
	if active := p010ActiveVideos(t, path); len(active) != 1 {
		t.Fatalf("记录应保持活跃")
	}

	// 卷离线：文件不在、所属扫描根打不开 → volume_offline，不降级为只删记录。
	offlineRoot := filepath.Join(t.TempDir(), "unmounted")
	offlinePath := filepath.Join(offlineRoot, "gone.mp4")
	offline := models.Video{Name: "gone.mp4", Path: offlinePath, Directory: offlineRoot}
	if err := database.DB.Create(&offline).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: offlineRoot}).Error; err != nil {
		t.Fatal(err)
	}
	result = svc.DeleteVideosDetailed([]uint{offline.ID}, true, BatchDeleteOptions{})
	if result.Items[0].Code != TrashResultVolumeOffline {
		t.Fatalf("应返回 volume_offline: %#v", result)
	}
	if active := p010ActiveVideos(t, offlinePath); len(active) != 1 {
		t.Fatalf("离线根不得降级为只删记录")
	}
	var entries int64
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("video_id = ?", offline.ID).Count(&entries).Error; err != nil || entries != 0 {
		t.Fatalf("离线时不应留下条目: %d err=%v", entries, err)
	}
}

func TestLIB04FileMissingWhenRootReachableRemovesRecordWithMissingEntry(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	path := filepath.Join(root, "vanished.mp4")
	video := models.Video{Name: "vanished.mp4", Path: path, Directory: root, Size: 3}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	result := svc.DeleteVideosDetailed([]uint{video.ID}, true, BatchDeleteOptions{})
	if result.Items[0].Code != TrashResultFileMissing || result.Succeeded != 1 {
		t.Fatalf("应返回 file_missing 且计为成功: %#v", result)
	}
	entry := p010VideoEntry(t, video.ID)
	if entry.Mode != models.TrashModeMissing || entry.DeleteBatchID != result.BatchID {
		t.Fatalf("条目应为 missing 模式并带批次标识: %#v", entry)
	}
}

// 守卫：每条建条目的代码路径都写入非空 mode 与 delete_batch_id（P-001 评审要求；
// 启动时的回填会掩盖漏写，所以要在源码层面卡住）。
func TestLIB04EveryTrashEntryCreationPathSetsModeAndBatch(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// 函数体里是否有对某个变量 .Mode 的赋值（字面量之后按分支赋值的写法）。
			assignsMode := false
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if assign, ok := node.(*ast.AssignStmt); ok {
					for _, lhs := range assign.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Mode" {
							assignsMode = true
						}
					}
				}
				return true
			})
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				lit, ok := node.(*ast.CompositeLit)
				if !ok || len(lit.Elts) == 0 {
					return true
				}
				selector, ok := lit.Type.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := selector.X.(*ast.Ident)
				if !ok || pkg.Name != "models" || (selector.Sel.Name != "VideoTrashEntry" && selector.Sel.Name != "ImageTrashEntry") {
					return true
				}
				found++
				keys := map[string]bool{}
				for _, elt := range lit.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if ident, ok := kv.Key.(*ast.Ident); ok {
							keys[ident.Name] = true
						}
					}
				}
				position := fset.Position(lit.Pos())
				if !keys["DeleteBatchID"] {
					t.Errorf("%s: 建 %s 条目时没有写 DeleteBatchID", position, selector.Sel.Name)
				}
				if !keys["Mode"] && !assignsMode {
					t.Errorf("%s: 建 %s 条目的函数里没有给 Mode 赋值", position, selector.Sel.Name)
				}
				return true
			})
		}
	}
	if found == 0 {
		t.Fatal("没有找到任何建条目的代码路径，守卫失效")
	}
}

// 与上面的静态守卫配套：行为上走遍视频与图片的每种删除路径，条目的 mode 与 delete_batch_id 都非空。
func TestLIB04EveryDeleteModeWritesNonEmptyModeAndBatch(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	imageSvc := NewImageService()
	root := t.TempDir()
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}

	trashVideo := p010Video(t, filepath.Join(root, "v-trash.mp4"), "v-trash")
	recordVideo := p010Video(t, filepath.Join(root, "v-record.mp4"), "v-record")
	missingVideo := models.Video{Name: "v-missing.mp4", Path: filepath.Join(root, "v-missing.mp4"), Directory: root, Size: 1}
	scannerVideo := models.Video{Name: "v-scanner.mp4", Path: filepath.Join(root, "v-scanner.mp4"), Directory: root, Size: 1}
	for _, v := range []*models.Video{&missingVideo, &scannerVideo} {
		if err := database.DB.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.DeleteVideo(trashVideo.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVideo(recordVideo.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVideo(missingVideo.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.deleteVideoBy(scannerVideo.ID, false, "scanner"); err != nil {
		t.Fatal(err)
	}
	wantVideo := map[uint]string{
		trashVideo.ID: models.TrashModeTrash, recordVideo.ID: models.TrashModeRecordOnly,
		missingVideo.ID: models.TrashModeMissing, scannerVideo.ID: models.TrashModeMissing,
	}
	for id, mode := range wantVideo {
		entry := p010VideoEntry(t, id)
		if entry.Mode != mode || len(entry.DeleteBatchID) != 32 {
			t.Errorf("视频 %d 条目 mode=%q batch=%q，期望 mode=%s 且批次非空", id, entry.Mode, entry.DeleteBatchID, mode)
		}
	}

	trashImage := imageTrashTestCreateImage(t, filepath.Join(root, "i-trash.jpg"), "i-trash")
	recordImage := imageTrashTestCreateImage(t, filepath.Join(root, "i-record.jpg"), "i-record")
	missingImage := &models.Image{Name: "i-missing.jpg", Path: filepath.Join(root, "i-missing.jpg"), Directory: root, Size: 1}
	if err := database.DB.Create(missingImage).Error; err != nil {
		t.Fatal(err)
	}
	if err := imageSvc.DeleteImage(trashImage.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := imageSvc.DeleteImage(recordImage.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := imageSvc.DeleteImage(missingImage.ID, true); err != nil {
		t.Fatal(err)
	}
	wantImage := map[uint]string{
		trashImage.ID: models.TrashModeTrash, recordImage.ID: models.TrashModeRecordOnly, missingImage.ID: models.TrashModeMissing,
	}
	for id, mode := range wantImage {
		entry := p010ImageEntry(t, id)
		if entry.Mode != mode || len(entry.DeleteBatchID) != 32 {
			t.Errorf("图片 %d 条目 mode=%q batch=%q，期望 mode=%s 且批次非空", id, entry.Mode, entry.DeleteBatchID, mode)
		}
	}
}

// IMG02：图片只删记录后可恢复，且不会被重新收录；恢复后不重复。
func TestIMG02RecordOnlyImageIsRestorableAndNotReimported(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageTestMustAddDirectory(t, svc, root)
	path := filepath.Join(root, "keep.jpg")
	image := imageTrashTestCreateImage(t, path, "pixels")
	mustSetFileModTime(t, path, time.Now().Add(-10*time.Minute))
	// 记录里的 mtime 以删除时磁盘上的为准，这里重新读取一次不需要；直接删除。
	if err := svc.DeleteImage(image.ID, false); err != nil {
		t.Fatal(err)
	}
	if result, err := svc.SyncImageDirectories(); err != nil || result.Added != 0 || result.Restored != 0 {
		t.Fatalf("只删记录的图片不应被扫描重新收录: %#v err=%v", result, err)
	}
	var active int64
	if err := database.DB.Model(&models.Image{}).Where("path = ?", path).Count(&active).Error; err != nil || active != 0 {
		t.Fatalf("不应出现活跃记录: %d err=%v", active, err)
	}

	center := NewTrashCenter(nil, svc)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "image"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Mode != models.TrashModeRecordOnly {
		t.Fatalf("回收站应列出 record_only 条目: %#v err=%v", page, err)
	}
	restored, err := center.RestoreTrashEntries("image", []uint{page.Items[0].ID})
	if err != nil || restored.Succeeded != 1 {
		t.Fatalf("恢复应成功: %#v err=%v", restored, err)
	}
	if err := database.DB.Model(&models.Image{}).Where("path = ?", path).Count(&active).Error; err != nil || active != 1 {
		t.Fatalf("恢复后应有一条活跃记录: %d err=%v", active, err)
	}
	if result, err := svc.SyncImageDirectories(); err != nil || result.Added != 0 {
		t.Fatalf("恢复后再扫描不应重复收录: %#v err=%v", result, err)
	}
}

// IMG02：record_only 恢复只还原数据库；原路径已有活跃记录时报 path_occupied。
func TestIMG02RestoreRecordOnlyReportsPathOccupied(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	path := filepath.Join(root, "dup.jpg")
	image := imageTrashTestCreateImage(t, path, "first")
	if err := svc.DeleteImage(image.ID, false); err != nil {
		t.Fatal(err)
	}
	// 文件被替换后新文件入库，占用了原路径。
	if err := os.WriteFile(path, []byte("second-file-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.addImage(path); err != nil {
		t.Fatalf("身份变了的新文件应能入库: %v", err)
	}
	entry := p010ImageEntry(t, image.ID)
	center := NewTrashCenter(nil, svc)
	result, err := center.RestoreTrashEntries("image", []uint{entry.ID})
	if err != nil || result.Items[0].Code != TrashResultPathOccupied || !strings.Contains(result.Items[0].Message, "原位置已收录了新文件") {
		t.Fatalf("应报 path_occupied 及中文文案: %#v err=%v", result, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("恢复被拒绝时文件不能被动: %v", err)
	}
}

// LIB05 / IMG01：清除 = 核对身份 → 删废纸篓文件 → 硬删记录与条目（级联）；record_only 不可清除。
func TestLIB05PurgeRemovesTrashFileAndHardDeletesRecord(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	trashed := p010Video(t, filepath.Join(root, "purge-me.mp4"), "purge-content")
	recordOnly := p010Video(t, filepath.Join(root, "keep.mp4"), "keep-content")
	tag := models.Tag{Name: "purge-tag", Color: "#fff"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&trashed).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVideo(trashed.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVideo(recordOnly.ID, false); err != nil {
		t.Fatal(err)
	}
	trashEntry := p010VideoEntry(t, trashed.ID)
	recordEntry := p010VideoEntry(t, recordOnly.ID)

	center := NewTrashCenter(svc, nil)
	result, err := center.PurgeTrashEntries("video", []uint{trashEntry.ID, recordEntry.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Code != TrashResultOK || result.Items[1].Code != TrashResultNotPurgeable {
		t.Fatalf("trash 应清除成功、record_only 应 not_purgeable: %#v", result)
	}
	if _, statErr := os.Stat(trashEntry.TrashPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("废纸篓文件应被删除: %v", statErr)
	}
	var count int64
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", trashed.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("视频记录应被硬删: %d err=%v", count, err)
	}
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", trashEntry.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("条目应被删除: %d err=%v", count, err)
	}
	var joins int64
	if err := database.DB.Table("video_tags").Where("video_id = ?", trashed.ID).Count(&joins).Error; err != nil || joins != 0 {
		t.Fatalf("标签关联应级联删除: %d err=%v", joins, err)
	}
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", recordOnly.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("record_only 的记录不应被清除: %d err=%v", count, err)
	}
}

func TestLIB05PurgeRefusesWhenTrashFileIdentityChanged(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video := p010Video(t, filepath.Join(root, "swap.mp4"), "original-content")
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	// 废纸篓里的文件被同名换成了别的文件（大小不同）。
	if err := os.Remove(entry.TrashPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.TrashPath, []byte("someone-elses-file-with-other-size"), 0o644); err != nil {
		t.Fatal(err)
	}
	center := NewTrashCenter(svc, nil)
	result, err := center.PurgeTrashEntries("video", []uint{entry.ID})
	if err != nil || result.Items[0].Code != TrashResultIdentityMismatch {
		t.Fatalf("身份不符应拒绝清除: %#v err=%v", result, err)
	}
	if _, statErr := os.Stat(entry.TrashPath); statErr != nil {
		t.Fatalf("不符的文件必须保留: %v", statErr)
	}
	// 恢复同样拒绝。
	restored, err := center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || restored.Items[0].Code != TrashResultIdentityMismatch {
		t.Fatalf("身份不符应拒绝恢复: %#v err=%v", restored, err)
	}
	if _, statErr := os.Stat(video.Path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("恢复被拒绝时原路径不应出现文件: %v", statErr)
	}
}

// LIB05：外部清空废纸篓后，列表当页对账为 file_gone，只提供「移除记录」；清除与恢复各自给出明确结果。
func TestLIB05ListReconcilesFileGoneAndRemoveGone(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video := p010Video(t, filepath.Join(root, "emptied.mp4"), "emptied")
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	if err := os.Remove(entry.TrashPath); err != nil {
		t.Fatal(err)
	}
	center := NewTrashCenter(svc, nil)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("列表失败: %#v err=%v", page, err)
	}
	if page.Items[0].State != models.TrashStateFileGone || len(page.Items[0].Actions) != 1 || page.Items[0].Actions[0] != TrashActionRemoveRecord {
		t.Fatalf("应对账为 file_gone 且只提供移除记录: %#v", page.Items[0])
	}
	if got := p010VideoEntry(t, video.ID); got.State != models.TrashStateFileGone {
		t.Fatalf("对账结果应落库: %#v", got)
	}
	usage, err := center.GetTrashUsage()
	if err != nil || usage.Video.GoneCount != 1 || usage.Video.BytesInTrash != 0 {
		t.Fatalf("用量应反映 file_gone: %#v err=%v", usage, err)
	}
	restored, err := center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || restored.Items[0].Code != TrashResultFileGone {
		t.Fatalf("file_gone 恢复应给出 file_gone: %#v err=%v", restored, err)
	}
	removed, err := center.RemoveGoneTrashEntries("video", []uint{entry.ID})
	if err != nil || removed.Succeeded != 1 {
		t.Fatalf("移除已清除记录应成功: %#v err=%v", removed, err)
	}
	var count int64
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", video.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("记录应被硬删: %d err=%v", count, err)
	}
}

// LIB05：游标分页、筛选与用量。
func TestLIB05ListPaginationFiltersAndUsage(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	var ids []uint
	for _, name := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		v := p010Video(t, filepath.Join(root, name+".mp4"), "content-"+name)
		ids = append(ids, v.ID)
	}
	for i, id := range ids {
		if err := svc.DeleteVideo(id, i%2 == 0); err != nil {
			t.Fatal(err)
		}
	}
	center := NewTrashCenter(svc, nil)
	first, err := center.ListTrashEntries(TrashFilter{Kind: "video", Limit: 2})
	if err != nil || len(first.Items) != 2 || !first.HasMore {
		t.Fatalf("第一页应有 2 条且有更多: %#v err=%v", first, err)
	}
	second, err := center.ListTrashEntries(TrashFilter{Kind: "video", Limit: 2, CursorID: first.NextCursor})
	if err != nil || len(second.Items) != 2 || !second.HasMore || second.Items[0].ID >= first.Items[1].ID {
		t.Fatalf("第二页应接在第一页之后: %#v err=%v", second, err)
	}
	third, err := center.ListTrashEntries(TrashFilter{Kind: "video", Limit: 2, CursorID: second.NextCursor})
	if err != nil || len(third.Items) != 1 || third.HasMore {
		t.Fatalf("第三页应只剩 1 条: %#v err=%v", third, err)
	}
	onlyTrash, err := center.ListTrashEntries(TrashFilter{Kind: "video", Mode: models.TrashModeTrash})
	if err != nil || len(onlyTrash.Items) != 3 {
		t.Fatalf("按模式筛选应得 3 条 trash: %#v err=%v", onlyTrash, err)
	}
	byName, err := center.ListTrashEntries(TrashFilter{Kind: "video", Query: "CHAR"})
	if err != nil || len(byName.Items) != 1 || byName.Items[0].Name != "charlie.mp4" {
		t.Fatalf("按名称搜索（不分大小写）失败: %#v err=%v", byName, err)
	}
	usage, err := center.GetTrashUsage()
	if err != nil || usage.Video.Count != 5 || usage.Video.BytesInTrash <= 0 || usage.Image.Count != 0 {
		t.Fatalf("用量不符: %#v err=%v", usage, err)
	}
	if _, err := center.ListTrashEntries(TrashFilter{Kind: "audio"}); err == nil {
		t.Fatalf("未知类型应报错")
	}
	empty, err := center.ListTrashEntries(TrashFilter{Kind: "image"})
	if err != nil || len(empty.Items) != 0 || empty.HasMore || empty.NextCursor != 0 {
		t.Fatalf("空回收站应返回空页: %#v err=%v", empty, err)
	}
}

// LIB12 / IMG09：整批撤销——同一次删除的条目按批次恢复；图片同理。
func TestLIB12RestoreTrashBatchUndoesWholeDelete(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	imageSvc := NewImageService()
	root := t.TempDir()
	var videoIDs []uint
	for _, name := range []string{"b1", "b2", "b3"} {
		videoIDs = append(videoIDs, p010Video(t, filepath.Join(root, name+".mp4"), "video-"+name).ID)
	}
	other := p010Video(t, filepath.Join(root, "other.mp4"), "other")
	result := svc.DeleteVideosDetailed(videoIDs, true, BatchDeleteOptions{})
	if result.Succeeded != 3 || len(result.BatchID) != 32 {
		t.Fatalf("批量删除应全部成功: %#v", result)
	}
	if err := svc.DeleteVideo(other.ID, true); err != nil {
		t.Fatal(err)
	}
	center := NewTrashCenter(svc, imageSvc)
	restored, err := center.RestoreTrashBatch("video", result.BatchID)
	if err != nil || restored.Succeeded != 3 || restored.BatchID != result.BatchID {
		t.Fatalf("按批次恢复应恢复 3 项: %#v err=%v", restored, err)
	}
	for _, id := range videoIDs {
		var v models.Video
		if err := database.DB.First(&v, id).Error; err != nil {
			t.Fatalf("视频 %d 应已恢复: %v", id, err)
		}
		if _, err := os.Stat(v.Path); err != nil {
			t.Fatalf("文件应回到原路径: %v", err)
		}
	}
	if err := database.DB.First(&models.Video{}, other.ID).Error; err == nil {
		t.Fatalf("别的批次的视频不应被恢复")
	}

	// 图片：IMG09 恢复闭环，批量删除后整批撤销。
	var imageIDs []uint
	for _, name := range []string{"i1", "i2"} {
		imageIDs = append(imageIDs, imageTrashTestCreateImage(t, filepath.Join(root, name+".jpg"), "img-"+name).ID)
	}
	imageResult := imageSvc.DeleteImagesDetailed(imageIDs, true, BatchDeleteOptions{})
	if imageResult.Succeeded != 2 {
		t.Fatalf("图片批量删除应成功: %#v", imageResult)
	}
	imageRestored, err := center.RestoreTrashBatch("image", imageResult.BatchID)
	if err != nil || imageRestored.Succeeded != 2 {
		t.Fatalf("图片按批次恢复失败: %#v err=%v", imageRestored, err)
	}
	if _, err := center.RestoreTrashBatch("image", ""); err == nil {
		t.Fatalf("空批次标识应报错")
	}
}

// IMG09：目录删除同样带批次，且与目录直属规则一致。
func TestIMG09DeleteImagesInDirectoryCarriesBatchAndRestores(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	a := imageTrashTestCreateImage(t, filepath.Join(root, "dir", "a.jpg"), "aaa")
	b := imageTrashTestCreateImage(t, filepath.Join(root, "dir", "b.jpg"), "bbb")
	nested := imageTrashTestCreateImage(t, filepath.Join(root, "dir", "sub", "c.jpg"), "ccc")
	result, err := svc.DeleteImagesInDirectoryDetailed(filepath.Join(root, "dir"), true, BatchDeleteOptions{})
	if err != nil || result.Succeeded != 2 {
		t.Fatalf("应只删目录直属的两张: %#v err=%v", result, err)
	}
	if err := database.DB.First(&models.Image{}, nested.ID).Error; err != nil {
		t.Fatalf("子目录图片不应被删: %v", err)
	}
	center := NewTrashCenter(nil, svc)
	restored, err := center.RestoreTrashBatch("image", result.BatchID)
	if err != nil || restored.Succeeded != 2 {
		t.Fatalf("整批撤销失败: %#v err=%v", restored, err)
	}
	for _, id := range []uint{a.ID, b.ID} {
		if err := database.DB.First(&models.Image{}, id).Error; err != nil {
			t.Fatalf("图片 %d 应已恢复: %v", id, err)
		}
	}
	if _, err := svc.DeleteImagesInDirectoryDetailed("   ", true, BatchDeleteOptions{}); err == nil {
		t.Fatalf("空目录应报错")
	}
}

// IMG12：record_only 与 trash 删除都不做整文件哈希（大文件/外置盘代价）。
func TestIMG12DeleteDoesNotHashWholeFile(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	a := p010Video(t, filepath.Join(root, "big-a.mp4"), "aaaa")
	b := p010Video(t, filepath.Join(root, "big-b.mp4"), "bbbb")
	if err := svc.DeleteVideo(a.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVideo(b.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []models.VideoTrashEntry{p010VideoEntry(t, a.ID), p010VideoEntry(t, b.ID)} {
		if entry.FileSHA256 != "" || entry.FileSize == 0 || entry.FileModTime == 0 {
			t.Fatalf("只应记录身份，不应哈希: %#v", entry)
		}
	}
}

// IMG12：批量删除逐项上报进度，并可在两项之间取消；已完成的保留，未处理的计为 cancelled。
func TestIMG12BatchDeleteReportsProgressAndCancelsBetweenItems(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	var ids []uint
	for _, name := range []string{"c1", "c2", "c3", "c4"} {
		ids = append(ids, p010Video(t, filepath.Join(root, name+".mp4"), "x-"+name).ID)
	}
	var progress [][2]int
	requestID := "req-cancel-test"
	result := svc.DeleteVideosDetailed(ids, false, BatchDeleteOptions{
		RequestID: requestID,
		Progress: func(done, total int) {
			progress = append(progress, [2]int{done, total})
			if done == 2 {
				CancelBatchDelete(requestID)
			}
		},
	})
	if result.Succeeded != 2 || result.Cancelled != 2 || result.Failed != 0 || len(result.Items) != 4 {
		t.Fatalf("应完成 2 项、取消 2 项: %#v", result)
	}
	if len(progress) != 2 || progress[0] != [2]int{1, 4} || progress[1] != [2]int{2, 4} {
		t.Fatalf("进度事件不符: %v", progress)
	}
	for _, item := range result.Items[2:] {
		if item.Code != TrashResultCancelled {
			t.Fatalf("未处理的项应为 cancelled: %#v", result.Items)
		}
	}
	for _, id := range ids[2:] {
		if err := database.DB.First(&models.Video{}, id).Error; err != nil {
			t.Fatalf("被取消的项不应被删除: %v", err)
		}
	}
	if CancelBatchDelete(requestID) {
		t.Fatalf("结束后的 requestID 不应再能被取消")
	}
	if CancelBatchDelete("never-registered") {
		t.Fatalf("未登记的 requestID 应返回 false")
	}
}

func TestIMG12ImageBatchCancelAndPermanentDelete(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	var ids []uint
	for _, name := range []string{"p1", "p2", "p3"} {
		ids = append(ids, imageTrashTestCreateImage(t, filepath.Join(root, name+".jpg"), "px-"+name).ID)
	}
	requestID := "img-cancel"
	result := svc.DeleteImagesDetailed(ids, true, BatchDeleteOptions{
		RequestID: requestID,
		Progress: func(done, total int) {
			if done == 1 {
				CancelBatchDelete(requestID)
			}
		},
	})
	if result.Succeeded != 1 || result.Cancelled != 2 {
		t.Fatalf("图片批量取消结果不符: %#v", result)
	}

	p010WithSystemTrash(t, func(string) (string, error) { return "", ErrTrashUnsupportedVolume })
	unsupported := svc.DeleteImagesDetailed(ids[1:2], true, BatchDeleteOptions{})
	if unsupported.Items[0].Code != TrashResultTrashUnsupported {
		t.Fatalf("图片应返回 trash_unsupported: %#v", unsupported)
	}
	perm := svc.PermanentlyDeleteImages(ids[1:2])
	if perm.Succeeded != 1 {
		t.Fatalf("图片永久删除应成功: %#v", perm)
	}
	var count int64
	if err := database.DB.Unscoped().Model(&models.Image{}).Where("id = ?", ids[1]).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("图片记录应被硬删: %d err=%v", count, err)
	}
}

// 崩溃恢复三分支（mode=trash 的 pending_move，trash_path 为空）。
func TestLIB05CrashRecoveryThreeBranches(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()

	pending := func(video models.Video) models.VideoTrashEntry {
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

	// 分支 1：文件没动 → 回滚为活跃记录，条目被取消。
	untouched := p010Video(t, filepath.Join(root, "untouched.mp4"), "untouched")
	untouchedEntry := pending(untouched)
	// 分支 2：文件已在废纸篓顶层（替身的 .Trash），trash_path 未记 → 按身份找到并补写。
	moved := p010Video(t, filepath.Join(root, "moved.mp4"), "moved-content")
	movedEntry := pending(moved)
	trashedPath, err := fakeSystemTrashMove(moved.Path)
	if err != nil {
		t.Fatal(err)
	}
	// 分支 3：文件哪里都找不到。
	lost := p010Video(t, filepath.Join(root, "lost.mp4"), "lost-content")
	lostEntry := pending(lost)
	if err := os.Remove(lost.Path); err != nil {
		t.Fatal(err)
	}

	if err := svc.ReconcileTrashEntries(); err != nil {
		t.Fatalf("启动对账失败: %v", err)
	}

	var gone int64
	if err := database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", untouchedEntry.ID).Count(&gone).Error; err != nil || gone != 0 {
		t.Fatalf("分支 1：条目应被取消: %d err=%v", gone, err)
	}
	if err := database.DB.First(&models.Video{}, untouched.ID).Error; err != nil {
		t.Fatalf("分支 1：记录应保持活跃: %v", err)
	}

	got := p010VideoEntry(t, moved.ID)
	if got.ID != movedEntry.ID || got.State != trashStateDeleted || got.TrashPath != trashedPath || !got.FileMoved || got.LastError != "" {
		t.Fatalf("分支 2：应补写 trash_path 并置 deleted: %#v", got)
	}
	var movedVideo models.Video
	if err := database.DB.Unscoped().First(&movedVideo, moved.ID).Error; err != nil || !movedVideo.DeletedAt.IsValid() {
		t.Fatalf("分支 2：记录应被软删: %#v err=%v", movedVideo, err)
	}

	got = p010VideoEntry(t, lost.ID)
	if got.ID != lostEntry.ID || got.State != trashStateDeleted || got.TrashPath != "" || got.LastError != "文件位置未知" || got.FileMoved {
		t.Fatalf("分支 3：应置 deleted 并写「文件位置未知」: %#v", got)
	}
	var lostVideo models.Video
	if err := database.DB.Unscoped().First(&lostVideo, lost.ID).Error; err != nil || !lostVideo.DeletedAt.IsValid() {
		t.Fatalf("分支 3：记录应被软删: %#v err=%v", lostVideo, err)
	}

	// 分支 3 的条目在回收站里只提供「移除记录」（当页对账为 file_gone）。
	center := NewTrashCenter(svc, nil)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "video", Query: "lost"})
	if err != nil || len(page.Items) != 1 || page.Items[0].State != models.TrashStateFileGone || page.Items[0].Actions[0] != TrashActionRemoveRecord {
		t.Fatalf("位置未知的条目应只能移除记录: %#v err=%v", page, err)
	}
}

func TestLIB05CrashRecoveryThreeBranchesForImages(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	pending := func(image *models.Image) models.ImageTrashEntry {
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
		return entry
	}
	untouched := imageTrashTestCreateImage(t, filepath.Join(root, "u.jpg"), "u-content")
	pending(untouched)
	moved := imageTrashTestCreateImage(t, filepath.Join(root, "m.jpg"), "m-content")
	pending(moved)
	trashedPath, err := fakeSystemTrashMove(moved.Path)
	if err != nil {
		t.Fatal(err)
	}
	lost := imageTrashTestCreateImage(t, filepath.Join(root, "l.jpg"), "l-content")
	pending(lost)
	if err := os.Remove(lost.Path); err != nil {
		t.Fatal(err)
	}

	if err := svc.ReconcileImageTrashEntries(); err != nil {
		t.Fatalf("启动对账失败: %v", err)
	}
	var n int64
	if err := database.DB.Model(&models.ImageTrashEntry{}).Where("image_id = ?", untouched.ID).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("分支 1：条目应被取消: %d err=%v", n, err)
	}
	if err := database.DB.First(&models.Image{}, untouched.ID).Error; err != nil {
		t.Fatalf("分支 1：记录应保持活跃: %v", err)
	}
	if got := p010ImageEntry(t, moved.ID); got.State != trashStateDeleted || got.TrashPath != trashedPath || !got.FileMoved {
		t.Fatalf("分支 2：%#v", got)
	}
	if got := p010ImageEntry(t, lost.ID); got.State != trashStateDeleted || got.TrashPath != "" || got.LastError != "文件位置未知" {
		t.Fatalf("分支 3：%#v", got)
	}
	for _, id := range []uint{moved.ID, lost.ID} {
		var img models.Image
		if err := database.DB.Unscoped().First(&img, id).Error; err != nil || !img.DeletedAt.IsValid() {
			t.Fatalf("图片 %d 应被软删: %v", id, err)
		}
	}
}

// legacy_trash 旧行（同目录 trash/ 里的文件，按旧的 SHA-256 校验）仍可恢复；purge 也认旧校验。
func TestLIB05LegacyTrashRowStillRestorableAndPurgeable(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	legacyDir := filepath.Join(root, DefaultTrashDirName)
	restorePath := filepath.Join(root, "legacy-restore.mp4")
	purgePath := filepath.Join(root, "legacy-purge.mp4")
	restoreVideo := models.Video{Name: "legacy-restore.mp4", Path: restorePath, Directory: root, Size: 6}
	purgeVideo := models.Video{Name: "legacy-purge.mp4", Path: purgePath, Directory: root, Size: 5}
	for _, v := range []*models.Video{&restoreVideo, &purgeVideo} {
		if err := database.DB.Create(v).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Delete(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	mk := func(video models.Video, name, content string) models.VideoTrashEntry {
		trashPath := filepath.Join(legacyDir, name)
		if err := os.MkdirAll(legacyDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(trashPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		digest, err := fileSHA256Hex(trashPath)
		if err != nil {
			t.Fatal(err)
		}
		entry := models.VideoTrashEntry{
			DeletedBy: "user", VideoID: video.ID, VideoName: video.Name, OriginalPath: video.Path, TrashPath: trashPath,
			FileMoved: true, FileSize: int64(len(content)), FileSHA256: digest, State: trashStateDeleted,
			Mode: models.TrashModeLegacyTrash, DeleteBatchID: "",
		}
		if err := database.DB.Create(&entry).Error; err != nil {
			t.Fatal(err)
		}
		return entry
	}
	restoreEntry := mk(restoreVideo, "legacy-restore.mp4", "legacy")
	purgeEntry := mk(purgeVideo, "legacy-purge.mp4", "purge")

	center := NewTrashCenter(svc, nil)
	restored, err := center.RestoreTrashEntries("video", []uint{restoreEntry.ID})
	if err != nil || restored.Succeeded != 1 {
		t.Fatalf("legacy_trash 应仍可恢复: %#v err=%v", restored, err)
	}
	if _, err := os.Stat(restorePath); err != nil {
		t.Fatalf("文件应回到原路径: %v", err)
	}
	usage, err := center.GetTrashUsage()
	if err != nil || usage.Video.LegacyCount != 1 || usage.Video.LegacyBytes != 5 {
		t.Fatalf("旧版用量不符: %#v err=%v", usage, err)
	}
	purged, err := center.PurgeTrashEntries("video", []uint{purgeEntry.ID})
	if err != nil || purged.Succeeded != 1 {
		t.Fatalf("legacy_trash 应可清除: %#v err=%v", purged, err)
	}
	if _, err := os.Stat(purgeEntry.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧版回收站文件应被删除: %v", err)
	}
}

// 废纸篓在用户清空后重用同名路径：新条目认领该路径，旧条目退为 file_gone 且不再占用唯一索引。
func TestLIB05ReusedTrashPathClaimsFromGoneEntry(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	path := filepath.Join(root, "same-name.mp4")
	first := p010Video(t, path, "first-file")
	if err := svc.DeleteVideo(first.ID, true); err != nil {
		t.Fatal(err)
	}
	firstEntry := p010VideoEntry(t, first.ID)
	if err := os.Remove(firstEntry.TrashPath); err != nil { // 用户清空了废纸篓
		t.Fatal(err)
	}
	second := p010Video(t, path, "second-file-longer")
	if err := svc.DeleteVideo(second.ID, true); err != nil {
		t.Fatalf("重用同名废纸篓路径的删除应成功: %v", err)
	}
	secondEntry := p010VideoEntry(t, second.ID)
	if secondEntry.TrashPath != firstEntry.TrashPath {
		t.Fatalf("测试前提：应重用同一路径 %q vs %q", secondEntry.TrashPath, firstEntry.TrashPath)
	}
	reloaded := p010VideoEntry(t, first.ID)
	if reloaded.State != models.TrashStateFileGone || reloaded.TrashPath != "" {
		t.Fatalf("旧条目应退为 file_gone 并释放路径: %#v", reloaded)
	}
}

// 恢复：trash 模式移回原路径；视频恢复时清空 stale_reason。
func TestLIB05RestoreFromTrashBringsFileBackAndClearsStaleReason(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video := p010Video(t, filepath.Join(root, "back.mp4"), "back-content")
	if err := database.DB.Model(&models.Video{}).Where("id = ?", video.ID).
		Updates(map[string]interface{}{"is_stale": true, "stale_reason": models.StaleReasonMissingFile}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVideo(video.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, video.ID)
	center := NewTrashCenter(svc, nil)
	restored, err := center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || restored.Succeeded != 1 {
		t.Fatalf("恢复应成功: %#v err=%v", restored, err)
	}
	if _, err := os.Stat(video.Path); err != nil {
		t.Fatalf("文件应回到原路径: %v", err)
	}
	if _, err := os.Stat(entry.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("废纸篓里不应再有该文件: %v", err)
	}
	var back models.Video
	if err := database.DB.First(&back, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	if back.IsStale || back.StaleReason != "" {
		t.Fatalf("恢复后 is_stale 与 stale_reason 必须一起清空: %#v", back)
	}
}

// 恢复时原路径已被占用（文件或活跃记录）→ path_occupied，废纸篓里的文件不动。
func TestLIB05RestoreRefusesOccupiedOriginalPath(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	path := filepath.Join(root, "occupied.mp4")
	old := p010Video(t, path, "old-file")
	if err := svc.DeleteVideo(old.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, old.ID)
	newer := p010Video(t, path, "new-file-content")
	center := NewTrashCenter(svc, nil)
	restored, err := center.RestoreTrashEntries("video", []uint{entry.ID})
	if err != nil || restored.Items[0].Code != TrashResultPathOccupied {
		t.Fatalf("应报 path_occupied: %#v err=%v", restored, err)
	}
	if _, err := os.Stat(entry.TrashPath); err != nil {
		t.Fatalf("废纸篓里的文件不能被动: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new-file-content" || newer.ID == 0 {
		t.Fatalf("原路径上的新文件不能被覆盖: %q err=%v", got, err)
	}
}

// 迁移残留：经系统废纸篓移动并置 cleaned；已不在的行直接 cleaned；重复调用幂等。
func TestLIB05TrashStagedSourcesMovesAndMarksCleaned(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	staged := filepath.Join(root, ".movie.mp4.cineinsight-migrating-abc123")
	if err := os.WriteFile(staged, []byte("staged-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := []models.MigrationStagedSource{
		{OriginalPath: filepath.Join(root, "movie.mp4"), StagedPath: staged, Size: 12, State: models.MigrationStagedStatePending},
		{OriginalPath: filepath.Join(root, "ghost.mp4"), StagedPath: filepath.Join(root, ".ghost.cineinsight-migrating-def"), Size: 3, State: models.MigrationStagedStatePending},
	}
	for i := range rows {
		if err := database.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	center := NewTrashCenter(nil, nil)
	usage, err := center.GetTrashUsage()
	if err != nil || usage.Staged.Count != 2 || usage.Staged.Bytes != 15 {
		t.Fatalf("残留用量不符: %#v err=%v", usage, err)
	}
	result := center.TrashStagedSources([]uint{rows[0].ID, rows[1].ID, 9999})
	if result.Succeeded != 2 || result.Failed != 1 || result.Items[1].Code != TrashResultFileMissing {
		t.Fatalf("结果不符: %#v", result)
	}
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("暂存文件应被移走: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".Trash", filepath.Base(staged))); err != nil {
		t.Fatalf("暂存文件应在（替身）废纸篓里: %v", err)
	}
	for _, row := range rows {
		var got models.MigrationStagedSource
		if err := database.DB.First(&got, row.ID).Error; err != nil || got.State != models.MigrationStagedStateCleaned || got.CleanedAt == nil {
			t.Fatalf("应置 cleaned: %#v err=%v", got, err)
		}
	}
	if again := center.TrashStagedSources([]uint{rows[0].ID}); again.Succeeded != 1 {
		t.Fatalf("重复调用应幂等: %#v", again)
	}
}

// NSError → 错误映射表（详细设计 §2.1），平台无关的部分。
func TestLIB05MapSystemTrashErrorTable(t *testing.T) {
	const path = "/Volumes/Disk/secret dir/movie.mp4"
	cases := []struct {
		name    string
		cocoa   bool
		code    int
		posix   int
		message string
		is      error
	}{
		{"no such file", true, 4, 0, "", os.ErrNotExist},
		{"read no such file", true, 260, 0, "", os.ErrNotExist},
		{"feature unsupported", true, 3328, 0, "", ErrTrashUnsupportedVolume},
		{"posix ENOTSUP", true, 512, int(syscall.ENOTSUP), "", ErrTrashUnsupportedVolume},
		{"posix EXDEV", true, 512, int(syscall.EXDEV), "", ErrTrashUnsupportedVolume},
		{"no permission", true, 513, 0, "", ErrTrashPermissionDenied},
	}
	for _, tc := range cases {
		if err := mapSystemTrashError(tc.cocoa, tc.code, tc.posix, tc.message, path); !errors.Is(err, tc.is) {
			t.Errorf("%s: got %v want %v", tc.name, err, tc.is)
		}
	}
	// 非 Cocoa domain 的 ENOTSUP 不算「不支持」。
	if err := mapSystemTrashError(false, 512, int(syscall.ENOTSUP), "boom", path); errors.Is(err, ErrTrashUnsupportedVolume) {
		t.Errorf("非 Cocoa domain 不应映射为不支持: %v", err)
	}
	other := mapSystemTrashError(true, 640, 0, "The file "+path+" couldn't be moved", path)
	if other == nil || strings.Contains(other.Error(), path) || strings.Contains(other.Error(), "secret dir") || !strings.HasPrefix(other.Error(), "移到废纸篓失败: ") {
		t.Fatalf("其他错误应带前缀且擦除路径: %v", other)
	}
}

// IMG01：图片清除同样释放空间——删废纸篓文件、硬删记录（含标签关联）。
func TestIMG01PurgeImageFreesTrashAndHardDeletes(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	image := imageTrashTestCreateImage(t, filepath.Join(root, "purge.jpg"), "purge-pixels")
	tag := models.Tag{Name: "img-purge-tag", Color: "#eee"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(image).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010ImageEntry(t, image.ID)
	center := NewTrashCenter(nil, svc)
	usage, err := center.GetTrashUsage()
	if err != nil || usage.Image.Count != 1 || usage.Image.BytesInTrash != int64(len("purge-pixels")) {
		t.Fatalf("清除前用量不符: %#v err=%v", usage, err)
	}
	result, err := center.PurgeTrashEntries("image", []uint{entry.ID})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("清除应成功: %#v err=%v", result, err)
	}
	if _, statErr := os.Stat(entry.TrashPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("废纸篓文件应被删除: %v", statErr)
	}
	usage, err = center.GetTrashUsage()
	if err != nil || usage.Image.Count != 0 || usage.Image.BytesInTrash != 0 {
		t.Fatalf("清除后用量应归零: %#v err=%v", usage, err)
	}
	var count int64
	if err := database.DB.Unscoped().Model(&models.Image{}).Where("id = ?", image.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("图片记录应被硬删: %d err=%v", count, err)
	}
}

// IMG02：图片删除到废纸篓后，同路径的新文件照常入库（与视频一致）。
func TestIMG02ImageTrashedThenNewFileAtSamePathIsAdded(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	path := filepath.Join(root, "again.jpg")
	old := imageTrashTestCreateImage(t, path, "old-pixels")
	if err := svc.DeleteImage(old.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("brand-new-pixels-here"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := svc.addImage(path)
	if err != nil || added.ID == old.ID {
		t.Fatalf("同路径新文件应新建记录: %v", err)
	}
	if err := database.ApplySchema(database.DB); err != nil {
		t.Fatal(err)
	}
	var active int64
	if err := database.DB.Model(&models.Image{}).Where("path = ?", path).Count(&active).Error; err != nil || active != 1 {
		t.Fatalf("新记录应仍活跃: %d err=%v", active, err)
	}
	if _, err := svc.addImage(path); !errors.Is(err, ErrImageExists) {
		t.Fatalf("已有活跃记录应返回 ErrImageExists: %v", err)
	}
}

// IMG02：图片 record_only 被屏蔽时同时满足 ErrImageBlockedByUserDelete 与 ErrImageExists。
func TestIMG02BlockedImageSatisfiesBothSentinels(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	path := filepath.Join(root, "blocked.jpg")
	image := imageTrashTestCreateImage(t, path, "blocked-pixels")
	if err := svc.DeleteImage(image.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.addImage(path); !errors.Is(err, ErrImageBlockedByUserDelete) || !errors.Is(err, ErrImageExists) {
		t.Fatalf("应被屏蔽: %v", err)
	}
}

// IMG02（D-PC06）：扫描隐藏列表——原因现算、分页、用户删除的不在其中；RecheckImages 触发全量对账。
func TestIMG02ListHiddenImagesInfersReasonsAndRecheck(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	onlineRoot := t.TempDir()
	offlineRoot := filepath.Join(t.TempDir(), "unplugged")
	imageTestMustAddDirectory(t, svc, onlineRoot)
	imageTestMustAddDirectory(t, svc, offlineRoot)

	missing := models.Image{Name: "m.jpg", Path: filepath.Join(onlineRoot, "m.jpg"), Directory: onlineRoot, Size: 1, DeletedBy: "scanner", IsStale: true}
	offline := models.Image{Name: "o.jpg", Path: filepath.Join(offlineRoot, "o.jpg"), Directory: offlineRoot, Size: 1, DeletedBy: "scanner", IsStale: true}
	removedRoot := models.Image{Name: "r.jpg", Path: filepath.Join(t.TempDir(), "r.jpg"), Directory: "x", Size: 1, DeletedBy: "scanner", IsStale: true}
	byUser := models.Image{Name: "u.jpg", Path: filepath.Join(onlineRoot, "u.jpg"), Directory: onlineRoot, Size: 1, DeletedBy: "user"}
	for _, image := range []*models.Image{&missing, &offline, &removedRoot, &byUser} {
		if err := database.DB.Create(image).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Delete(image).Error; err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.ListHiddenImages(0, 2)
	if err != nil || len(page.Items) != 2 || !page.HasMore {
		t.Fatalf("第一页应有 2 条且有更多: %#v err=%v", page, err)
	}
	rest, err := svc.ListHiddenImages(page.NextCursor, 10)
	if err != nil || len(rest.Items) != 1 || rest.HasMore {
		t.Fatalf("第二页应只剩 1 条: %#v err=%v", rest, err)
	}
	reasons := map[string]string{}
	for _, item := range append(page.Items, rest.Items...) {
		reasons[item.Name] = item.Reason
	}
	want := map[string]string{"m.jpg": HiddenImageReasonMissingFile, "o.jpg": HiddenImageReasonOfflineRoot, "r.jpg": HiddenImageReasonRemovedRoot}
	if len(reasons) != 3 {
		t.Fatalf("用户删除的图片不应出现在扫描隐藏里: %v", reasons)
	}
	for name, reason := range want {
		if reasons[name] != reason {
			t.Errorf("%s 的原因应为 %s，实际 %s", name, reason, reasons[name])
		}
	}
	if _, err := svc.RecheckImages(nil); err == nil {
		t.Fatalf("没有选择图片时应报错")
	}
	// 文件回来后，重新检查会把 is_stale 软删的图片自动恢复。
	if err := os.WriteFile(missing.Path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSetFileModTime(t, missing.Path, time.Now().Add(-10*time.Minute))
	result, err := svc.RecheckImages([]uint{missing.ID})
	if err != nil || result.Restored != 1 {
		t.Fatalf("重新检查应恢复文件已回来的图片: %#v err=%v", result, err)
	}
	after, err := svc.ListHiddenImages(0, 10)
	if err != nil || len(after.Items) != 2 {
		t.Fatalf("恢复后应只剩 2 张隐藏: %#v err=%v", after, err)
	}
}

// 超分任务以 RESTRICT 外键引用源视频（P-010 整合时发现）：已结束的任务随清除一起删，
// 进行中的任务让清除在动文件之前就被拒绝，废纸篓里的文件与记录都保持原样。
func TestLIB05PurgeHandlesEnhancementTasks(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	finished := p010Video(t, filepath.Join(root, "finished.mp4"), "finished-content")
	active := p010Video(t, filepath.Join(root, "active.mp4"), "active-content")
	for _, spec := range []struct {
		videoID uint
		status  string
	}{{finished.ID, models.EnhancementStatusCompleted}, {active.ID, models.EnhancementStatusRunning}} {
		task := models.VideoEnhancementTask{VideoID: spec.videoID, Profile: "general", Scale: 2, Status: spec.status, OutputBasename: "out"}
		if err := database.DB.Create(&task).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []models.Video{finished, active} {
		if err := svc.DeleteVideo(v.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	finishedEntry := p010VideoEntry(t, finished.ID)
	activeEntry := p010VideoEntry(t, active.ID)

	result, err := NewTrashCenter(svc, nil).PurgeTrashEntries("video", []uint{finishedEntry.ID, activeEntry.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Code != TrashResultOK {
		t.Fatalf("已结束超分任务的视频应能清除: %#v", result.Items[0])
	}
	if result.Items[1].Code == TrashResultOK {
		t.Fatalf("有进行中超分任务的视频不应被清除: %#v", result.Items[1])
	}
	var tasks int64
	database.DB.Model(&models.VideoEnhancementTask{}).Where("video_id = ?", finished.ID).Count(&tasks)
	if tasks != 0 {
		t.Fatalf("已结束的超分任务应随清除删除: %d", tasks)
	}
	if _, statErr := os.Stat(activeEntry.TrashPath); statErr != nil {
		t.Fatalf("被拒绝的清除不应删除废纸篓文件: %v", statErr)
	}
	var count int64
	database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", active.ID).Count(&count)
	if count != 1 {
		t.Fatalf("被拒绝的清除应保留记录: %d", count)
	}
}
