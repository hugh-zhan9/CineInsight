package services

import (
	"errors"
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

// 修复 K 的回归测试。测试名里的 APPxx / LIBxx / IMGxx 是问题清单 ID：
//   - APP02：「待重启」终态下取路径写锁的入口不永久阻塞（lockLibraryPaths），维护入口本身照常拿锁；
//   - LIB05 / IMG02：legacy 行在清除（文件删掉之后）与 file_gone 的「移除记录」时改为墓碑，旧版 trash/ 目录继续登记。
// 系统废纸篓是替身（system_trash_testhook_test.go）：同卷重命名进「原目录/.Trash」。

func reviewKIsMaintenance(err error) bool {
	return err != nil && (errors.Is(err, database.ErrMaintenance) || strings.Contains(err.Error(), database.ErrMaintenance.Error()))
}

// reviewKBatchErr 把批量结果的第一项还原成错误（ok 时为 nil）。
func reviewKBatchErr(result *BatchResult, err error) error {
	if err != nil {
		return err
	}
	if result == nil || len(result.Items) == 0 {
		return errors.New("批量结果为空")
	}
	if result.Items[0].Code == TrashResultOK {
		return nil
	}
	return errors.New(result.Items[0].Message)
}

// reviewKWaitForPendingWriter 等到有写者在 libraryPathMutationMu 上排队：有写者在等时 TryRLock 失败。
// 调用方必须持有读锁（否则 TryRLock 失败也可能只是写锁被别人拿着）。
func reviewKWaitForPendingWriter(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if !libraryPathMutationMu.TryRLock() {
			return
		}
		libraryPathMutationMu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("写者没有进入等待")
		}
		runtime.Gosched()
	}
}

// ---------- APP-02：写锁入口在维护终态下立即返回 ErrMaintenance ----------

func TestAPP02PathWriteLockEntriesReturnMaintenanceWhenFencedFixK(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video := p010Video(t, filepath.Join(root, "a.mp4"), "a")
	trashed := p010Video(t, filepath.Join(root, "b.mp4"), "b")
	if err := svc.DeleteVideo(trashed.ID, true); err != nil {
		t.Fatal(err)
	}
	entry := p010VideoEntry(t, trashed.ID)
	folder := filepath.Join(root, "folder")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	scanRoot := models.ScanDirectory{Path: root}
	if err := database.DB.Create(&scanRoot).Error; err != nil {
		t.Fatal(err)
	}
	remapTarget := t.TempDir()

	undo := reviewIHoldPathWriteLockAndFence(t)
	defer undo()
	center := NewTrashCenter(svc, nil)
	entries := []struct {
		name string
		run  func() error
	}{
		{"lockLibraryPaths", func() error {
			unlock, err := lockLibraryPaths()
			if err == nil {
				unlock()
			}
			return err
		}},
		{"lockLibraryPathRewrite", func() error {
			unlock, err := lockLibraryPathRewrite()
			if err == nil {
				unlock()
			}
			return err
		}},
		{"RestoreTrashEntry", func() error { _, err := svc.RestoreTrashEntry(entry.ID); return err }},
		{"ReconcileTrashEntries", svc.ReconcileTrashEntries},
		{"PermanentlyDeleteVideos", func() error { return reviewKBatchErr(svc.PermanentlyDeleteVideos([]uint{video.ID}), nil) }},
		{"MoveVideo", func() error { _, err := svc.MoveVideo(video.ID, destination); return err }},
		{"BatchMoveVideos", func() error {
			result := svc.BatchMoveVideos([]uint{video.ID}, destination)
			if len(result.Errors) != 1 {
				return nil
			}
			return errors.New(result.Errors[0].Error)
		}},
		{"MoveDirectory", func() error { _, err := svc.MoveDirectory(folder, destination); return err }},
		{"RenameDirectory", func() error { _, err := svc.RenameDirectory(folder, "renamed"); return err }},
		{"remapDirectory", func() error {
			result := &DirectoryUpdateResult{Mode: DirectoryUpdateModeRemap, OldPath: root, NewPath: remapTarget, PathChanged: true}
			_, err := (&DirectoryService{}).remapDirectory(scanRoot, remapTarget, "x", result)
			return err
		}},
		{"TrashCenter.RestoreTrashEntries", func() error { return reviewKBatchErr(center.RestoreTrashEntries("video", []uint{entry.ID})) }},
		{"TrashCenter.PurgeTrashEntries", func() error { return reviewKBatchErr(center.PurgeTrashEntries("video", []uint{entry.ID})) }},
		{"TrashCenter.RemoveGoneTrashEntries", func() error {
			return reviewKBatchErr(center.RemoveGoneTrashEntries("video", []uint{entry.ID}))
		}},
		{"TrashCenter.ForceRemoveTrashRecords", func() error {
			return reviewKBatchErr(center.ForceRemoveTrashRecords("video", []uint{entry.ID}, TrashForceRemoveConfirmText))
		}},
	}
	for _, item := range entries {
		if err := reviewIWithin(t, item.name, 3*time.Second, item.run); !reviewKIsMaintenance(err) {
			t.Fatalf("%s 在维护终态下应立即返回 ErrMaintenance: %v", item.name, err)
		}
	}
	undo()

	// 撤掉之后一切照旧：文件、记录都没被动过。
	if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("记录不得被改动: %v", err)
	}
	if got := reviewGFileContent(t, video.Path); got != "a" {
		t.Fatalf("文件不得被移动: %q", got)
	}
	if got := p010VideoEntry(t, trashed.ID); got.State != trashStateDeleted || got.TrashPath != entry.TrashPath {
		t.Fatalf("回收站条目不得被改动: %#v", got)
	}
	if got := reviewGFileContent(t, entry.TrashPath); got != "b" {
		t.Fatalf("废纸篓里的文件不得被动: %q", got)
	}
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		t.Fatalf("文件夹不得被移动或改名: %v", err)
	}
	var reloaded models.ScanDirectory
	if err := database.DB.First(&reloaded, scanRoot.ID).Error; err != nil || reloaded.Path != root || reloaded.Alias != "" {
		t.Fatalf("扫描目录不得被重映射: %#v err=%v", reloaded, err)
	}
}

// 等锁期间围栏被立起（立围栏的一方没有经路径写锁）时返回 ErrMaintenance，而且不持有写锁。
// 修复 L m3 之后，等待中的写者由「维护开始」通知唤醒、立即返回；放弃的那次获取随后拿到写锁会立即释放（辅助 goroutine），
// 所以这里改为等写锁在合理时间内变为空闲。「拿到锁之后复查围栏」由 TestAPP02PathLockRechecksFenceAfterAcquireFixLM3 钉住。
func TestAPP02PathWriteLockRechecksFenceAfterAcquireFixK(t *testing.T) {
	setupVideoServiceTestDB(t)
	libraryPathMutationMu.RLock()
	readerHeld := true
	var release func()
	t.Cleanup(func() {
		if release != nil {
			release()
		}
		if readerHeld {
			libraryPathMutationMu.RUnlock()
		}
	})
	done := make(chan error, 1)
	go func() {
		unlock, err := lockLibraryPaths()
		if err == nil {
			unlock()
		}
		done <- err
	}()
	// 写者已过了拿锁前的那次检查、正在排队（读锁被持有）。
	reviewKWaitForPendingWriter(t)
	release = database.BeginMaintenance()
	libraryPathMutationMu.RUnlock()
	readerHeld = false
	select {
	case err := <-done:
		if !errors.Is(err, database.ErrMaintenance) {
			t.Fatalf("拿到写锁时围栏已生效，应放锁并返回 ErrMaintenance: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等锁的写者没有返回")
	}
	reviewLWaitPathLockFree(t, "被放弃的写锁获取必须在拿到锁后立即释放")
	release()
	release = nil
}

// 无围栏时行为不变：写者等现有读者、新读者不插队（写者优先），写者持锁期间读者拿不到锁。
func TestAPP02PathWriteLockWithoutFenceKeepsWriterPriorityFixK(t *testing.T) {
	setupVideoServiceTestDB(t)
	firstRelease, err := rLockLibraryPaths()
	if err != nil {
		t.Fatalf("无围栏时应拿到读锁: %v", err)
	}
	order := make(chan string, 3)
	writerErr := make(chan error, 1)
	go func() {
		unlock, err := lockLibraryPaths()
		if err == nil {
			order <- "writer"
			time.Sleep(50 * time.Millisecond)
			order <- "writer-done"
			unlock()
		}
		writerErr <- err
	}()
	reviewKWaitForPendingWriter(t)
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
		t.Fatalf("现有读者未释放前，写者与新读者都不得拿到锁: %s", got)
	case <-time.After(150 * time.Millisecond):
	}
	firstRelease()
	for _, want := range []string{"writer", "writer-done", "reader"} {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("加锁顺序不对: got=%s want=%s", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("等不到 %s", want)
		}
	}
	if err := <-writerErr; err != nil {
		t.Fatalf("无围栏时写者不应失败: %v", err)
	}
	if err := <-readerErr; err != nil {
		t.Fatalf("无围栏时读者不应失败: %v", err)
	}
}

// 维护入口（enterDatabaseRestoreMode 调用的 BeginLibraryMaintenance）不受新检查影响：照常等现有写者、照常拿锁，
// 围栏已经生效时也一样；它持锁并立起围栏之后，其他写锁入口立即被拒绝；撤掉之后写锁入口恢复正常。
func TestAPP02MaintenanceEntryStillAcquiresPathWriteLockFixK(t *testing.T) {
	setupVideoServiceTestDB(t)
	var pathRelease, fenceRelease func()
	t.Cleanup(func() {
		if fenceRelease != nil {
			fenceRelease()
		}
		if pathRelease != nil {
			pathRelease()
		}
	})
	acquire := func(label string) func() {
		t.Helper()
		got := make(chan func(), 1)
		go func() { got <- BeginLibraryMaintenance() }()
		select {
		case release := <-got:
			return release
		case <-time.After(3 * time.Second):
			t.Fatalf("%s：维护入口没有拿到路径写锁", label)
			return nil
		}
	}

	// 1. 维护入口等正在进行的写者做完，然后拿到锁。
	unlockWriter, err := lockLibraryPaths()
	if err != nil {
		t.Fatalf("无围栏时写锁入口应成功: %v", err)
	}
	got := make(chan func(), 1)
	go func() { got <- BeginLibraryMaintenance() }()
	select {
	case release := <-got:
		release()
		t.Fatal("维护入口应等待正在进行的写者")
	case <-time.After(150 * time.Millisecond):
	}
	unlockWriter()
	select {
	case pathRelease = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("写者放锁后维护入口应拿到路径写锁")
	}

	// 2. 与 enterDatabaseRestoreMode 同序：持路径写锁之后立围栏。此后写锁入口立即返回 ErrMaintenance。
	fenceRelease = database.BeginMaintenance()
	if err := reviewIWithin(t, "lockLibraryPaths", 3*time.Second, func() error {
		unlock, err := lockLibraryPaths()
		if err == nil {
			unlock()
		}
		return err
	}); !errors.Is(err, database.ErrMaintenance) {
		t.Fatalf("维护终态下写锁入口应立即返回 ErrMaintenance: %v", err)
	}

	// 3. 离开维护模式（恢复 / 切换失败）：写锁入口恢复正常。
	fenceRelease()
	fenceRelease = nil
	pathRelease()
	pathRelease = nil
	unlock, err := lockLibraryPaths()
	if err != nil {
		t.Fatalf("撤掉围栏后写锁入口应成功: %v", err)
	}
	unlock()

	// 4. 维护入口本身不查围栏：围栏已生效时照样拿到锁。
	fenceRelease = database.BeginMaintenance()
	pathRelease = acquire("围栏已生效")
	pathRelease()
	pathRelease = nil
	fenceRelease()
	fenceRelease = nil
}

// 守卫：libraryPathMutationMu 的写锁只经 lockLibraryPaths 与维护入口 BeginLibraryMaintenance 获取；
// 维护入口在服务层只允许下面列出的调用点（其余一律改用 lockLibraryPaths）。
func TestAPP02NoDirectPathWriteLockOutsideHelperFixK(t *testing.T) {
	writeLockOwners := map[string]bool{"lockLibraryPaths": true, "BeginLibraryMaintenance": true}
	// 服务层没有任何调用点可以把维护入口当普通写锁用（超分发布已改用 lockLibraryPaths）。
	maintenanceCallers := map[string]bool{}

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
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "BeginLibraryMaintenance" {
					if !maintenanceCallers[name+":"+fn.Name.Name] {
						t.Errorf("%s:%s 调用了维护入口 BeginLibraryMaintenance，普通写锁应改用 lockLibraryPaths（维护终态会永久阻塞）", name, fn.Name.Name)
					}
				}
				return true
			})
			// 修复 L m3：写锁经 acquireLibraryPath 以方法值（libraryPathMutationMu.Lock，不是调用）传入，
			// 所以查所有选择子表达式，调用与方法值都算获取。
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
				case "Lock", "TryLock":
					if !writeLockOwners[fn.Name.Name] {
						t.Errorf("%s:%s 直接获取 libraryPathMutationMu 写锁（%s），应改用 lockLibraryPaths（维护终态会永久阻塞）", name, fn.Name.Name, selector.Sel.Name)
					}
				}
				return true
			})
		}
	}
}

// ---------- LIB-05 / IMG-02：legacy 行清除与 file_gone 的移除记录改为墓碑 ----------

// reviewKResidue 在旧版 trash/ 目录里放一个不属于任何条目的残留文件（例如更早的旧版删除留下的），mtime 调到两小时前，
// 让扫描不会因为「刚修改」跳过它（否则「重扫不收录」的断言没有意义）。
func reviewKResidue(t *testing.T, trashDir, name, content string) string {
	t.Helper()
	path := filepath.Join(trashDir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSetFileModTime(t, path, time.Now().Add(-2*time.Hour))
	return path
}

func reviewKAssertVideoTombstone(t *testing.T, entry models.VideoTrashEntry, videoID uint) {
	t.Helper()
	var tomb models.VideoTrashEntry
	if err := database.DB.First(&tomb, entry.ID).Error; err != nil || tomb.State != trashStateRemoved || tomb.TrashPath != entry.TrashPath {
		t.Fatalf("legacy 条目应成为墓碑并保留 trash_path: %#v err=%v", tomb, err)
	}
	if tomb.Mode != entry.Mode {
		t.Fatalf("墓碑不改 mode: %q -> %q", entry.Mode, tomb.Mode)
	}
	if got := p011ReloadVideo(t, videoID); !got.DeletedAt.IsValid() {
		t.Fatalf("媒体记录应保持软删: %#v", got)
	}
}

func reviewKAssertRescanSkipsLegacyDir(t *testing.T, svc *VideoService, root, trashDir string) {
	t.Helper()
	refreshLegacyTrashDirs()
	if !isTrashDir(trashDir) {
		t.Fatal("墓碑应让旧版 trash/ 目录继续算作已登记")
	}
	scan := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if scan.Added != 0 || len(scan.Errors) != 0 {
		t.Fatalf("重扫不得把旧版 trash/ 里残留的文件当成新文件收录: %+v", scan)
	}
	if active := reviewIActiveUnder(t, root); len(active) != 0 {
		t.Fatalf("不应出现任何活跃记录: %#v", active)
	}
}

func TestLIB05LegacyPurgeLeavesTombstoneAndRescanSkipsResidueFixK(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	root := t.TempDir()
	video, entry := reviewIOldLegacyVideo(t, filepath.Join(root, "movies", "old.mp4"), "legacy-purge")
	trashDir := filepath.Dir(entry.TrashPath)
	residue := reviewKResidue(t, trashDir, "older.mp4", "older-legacy-residue")
	tag := models.Tag{Name: "purge-tomb-tag"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", video.ID, tag.ID).Error; err != nil {
		t.Fatal(err)
	}

	center := NewTrashCenter(svc, nil)
	purged, err := center.PurgeTrashEntries("video", []uint{entry.ID})
	if err != nil || purged.Succeeded != 1 {
		t.Fatalf("legacy 行清除应成功: %#v err=%v", purged, err)
	}
	if _, err := os.Stat(entry.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧版 trash/ 里的这个文件应被删除: %v", err)
	}
	reviewKAssertVideoTombstone(t, entry, video.ID)
	if got := reviewGFileContent(t, residue); got != "older-legacy-residue" {
		t.Fatalf("同目录里的其他文件不得被动: %q", got)
	}

	// 墓碑不出现在列表、用量、标签计数里，对它的操作一律视为不存在。
	if page, err := center.ListTrashEntries(TrashFilter{Kind: "video"}); err != nil || len(page.Items) != 0 {
		t.Fatalf("墓碑不得出现在回收站列表: %#v err=%v", page, err)
	}
	if list, err := svc.ListTrashEntries(); err != nil || len(list) != 0 {
		t.Fatalf("墓碑不得出现在旧版列表接口: %#v err=%v", list, err)
	}
	if usage, err := center.GetTrashUsage(); err != nil || usage.Video != (TrashKindUsage{}) {
		t.Fatalf("墓碑不得计入用量: %+v err=%v", usage, err)
	}
	counts, err := (&TagService{}).GetTagUsageCounts([]uint{tag.ID})
	if err != nil || counts[tag.ID].TrashedVideos != 0 || counts[tag.ID].Videos != 0 {
		t.Fatalf("墓碑不得计入标签用量: %#v err=%v", counts, err)
	}
	if again, err := center.PurgeTrashEntries("video", []uint{entry.ID}); err != nil || again.Failed != 1 {
		t.Fatalf("对墓碑再清除应失败: %#v err=%v", again, err)
	}

	reviewKAssertRescanSkipsLegacyDir(t, svc, root, trashDir)
}

func TestLIB05LegacyFileGoneRemoveRecordLeavesTombstoneAndRescanSkipsResidueFixK(t *testing.T) {
	for _, mode := range []string{models.TrashModeLegacyTrash, ""} {
		name := "mode=" + mode
		if mode == "" {
			name = "mode=empty_before_backfill"
		}
		t.Run(name, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			svc := &VideoService{}
			root := t.TempDir()
			video, entry := reviewIOldLegacyVideo(t, filepath.Join(root, "movies", "gone.mp4"), "legacy-gone")
			if mode != entry.Mode {
				if err := database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", entry.ID).Update("mode", mode).Error; err != nil {
					t.Fatal(err)
				}
				entry.Mode = mode
			}
			trashDir := filepath.Dir(entry.TrashPath)
			residue := reviewKResidue(t, trashDir, "older.mp4", "older-legacy-residue")
			// 用户在访达里删掉了 trash/ 里这个文件：列表对账把条目标为 file_gone，只提供 remove_record。
			if err := os.Remove(entry.TrashPath); err != nil {
				t.Fatal(err)
			}
			center := NewTrashCenter(svc, nil)
			page, err := center.ListTrashEntries(TrashFilter{Kind: "video"})
			if err != nil || len(page.Items) != 1 || page.Items[0].State != models.TrashStateFileGone ||
				len(page.Items[0].Actions) != 1 || page.Items[0].Actions[0] != TrashActionRemoveRecord {
				t.Fatalf("前提：列表应把条目标为 file_gone 且只提供 remove_record: %#v err=%v", page, err)
			}
			entry.State = models.TrashStateFileGone

			removed, err := center.RemoveGoneTrashEntries("video", []uint{entry.ID})
			if err != nil || removed.Succeeded != 1 {
				t.Fatalf("file_gone 的 legacy 行移除记录应成功: %#v err=%v", removed, err)
			}
			reviewKAssertVideoTombstone(t, entry, video.ID)
			if got := reviewGFileContent(t, residue); got != "older-legacy-residue" {
				t.Fatalf("不得动任何文件: %q", got)
			}
			if page, err := center.ListTrashEntries(TrashFilter{Kind: "video"}); err != nil || len(page.Items) != 0 {
				t.Fatalf("墓碑不得出现在回收站列表: %#v err=%v", page, err)
			}
			if usage, err := center.GetTrashUsage(); err != nil || usage.Video != (TrashKindUsage{}) {
				t.Fatalf("墓碑不得计入用量: %+v err=%v", usage, err)
			}

			reviewKAssertRescanSkipsLegacyDir(t, svc, root, trashDir)
		})
	}
}

// 非 legacy 行（系统废纸篓 trash 模式）的清除与 file_gone 的移除记录照旧硬删条目与媒体记录。
func TestLIB05NonLegacyPurgeAndGoneRemoveStillHardDeleteFixK(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	dir := t.TempDir()
	purgeVideo := p010Video(t, filepath.Join(dir, "purge.mp4"), "purge-me")
	goneVideo := p010Video(t, filepath.Join(dir, "gone.mp4"), "gone-me")
	for _, video := range []models.Video{purgeVideo, goneVideo} {
		if err := svc.DeleteVideo(video.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	purgeEntry := p010VideoEntry(t, purgeVideo.ID)
	goneEntry := p010VideoEntry(t, goneVideo.ID)
	if purgeEntry.Mode != models.TrashModeTrash || goneEntry.Mode != models.TrashModeTrash {
		t.Fatalf("前提：应为系统废纸篓条目: %q %q", purgeEntry.Mode, goneEntry.Mode)
	}
	if err := os.Remove(goneEntry.TrashPath); err != nil {
		t.Fatal(err)
	}
	reviewISetEntryState(t, &models.VideoTrashEntry{}, goneEntry.ID, models.TrashStateFileGone)

	center := NewTrashCenter(svc, nil)
	if result, err := center.PurgeTrashEntries("video", []uint{purgeEntry.ID}); err != nil || result.Succeeded != 1 {
		t.Fatalf("清除应成功: %#v err=%v", result, err)
	}
	if result, err := center.RemoveGoneTrashEntries("video", []uint{goneEntry.ID}); err != nil || result.Succeeded != 1 {
		t.Fatalf("移除记录应成功: %#v err=%v", result, err)
	}
	for _, pair := range []struct {
		entryID, videoID uint
	}{{purgeEntry.ID, purgeVideo.ID}, {goneEntry.ID, goneVideo.ID}} {
		if n := reviewGCountRows(t, &models.VideoTrashEntry{}, "id = ?", pair.entryID); n != 0 {
			t.Fatalf("非 legacy 条目应被硬删: entry=%d n=%d", pair.entryID, n)
		}
		if n := reviewGCountRows(t, &models.Video{}, "id = ?", pair.videoID); n != 0 {
			t.Fatalf("非 legacy 行的媒体记录应被硬删: video=%d n=%d", pair.videoID, n)
		}
	}
}

func reviewKLegacyImage(t *testing.T, svc *ImageService, root, content string) (*models.Image, models.ImageTrashEntry, string) {
	t.Helper()
	imageTestMustAddDirectory(t, svc, root)
	image := imageTrashTestCreateImage(t, filepath.Join(root, "pics", "old.jpg"), content)
	mustSetFileModTime(t, image.Path, time.Now().Add(-2*time.Hour))
	entry := reviewGLegacyImageEntry(t, image)
	residue := reviewKResidue(t, filepath.Dir(entry.TrashPath), "older.jpg", "older-image-residue")
	return image, entry, residue
}

func reviewKAssertImageTombstoneAndSyncSkips(t *testing.T, svc *ImageService, center *TrashService, root string, image *models.Image, entry models.ImageTrashEntry, residue string) {
	t.Helper()
	var tomb models.ImageTrashEntry
	if err := database.DB.First(&tomb, entry.ID).Error; err != nil || tomb.State != trashStateRemoved || tomb.TrashPath != entry.TrashPath {
		t.Fatalf("图片 legacy 条目应成为墓碑并保留 trash_path: %#v err=%v", tomb, err)
	}
	if n := reviewGCountRows(t, &models.Image{}, "id = ? AND deleted_at IS NOT NULL", image.ID); n != 1 {
		t.Fatalf("图片记录应保持软删: %d", n)
	}
	if got := reviewGFileContent(t, residue); got != "older-image-residue" {
		t.Fatalf("同目录里的其他文件不得被动: %q", got)
	}
	if page, err := center.ListTrashEntries(TrashFilter{Kind: "image"}); err != nil || len(page.Items) != 0 {
		t.Fatalf("图片墓碑不得出现在回收站列表: %#v err=%v", page, err)
	}
	if list, err := svc.ListImageTrashEntries(); err != nil || len(list) != 0 {
		t.Fatalf("图片墓碑不得出现在旧版列表接口: %#v err=%v", list, err)
	}
	if usage, err := center.GetTrashUsage(); err != nil || usage.Image != (TrashKindUsage{}) {
		t.Fatalf("图片墓碑不得计入用量: %+v err=%v", usage, err)
	}
	sync := imageTestMustSync(t, svc)
	if sync.Added != 0 {
		t.Fatalf("图片同步不得收录旧版 trash/ 里残留的文件: %+v", sync)
	}
	like := escapeSQLLikePrefix(scanRootChildPrefix(root)) + "%"
	if n := reviewGCountRows(t, &models.Image{}, `path LIKE ? ESCAPE '\' AND deleted_at IS NULL`, like); n != 0 {
		t.Fatalf("不应出现任何活跃图片记录: %d", n)
	}
}

func TestIMG02LegacyImagePurgeLeavesTombstoneAndSyncSkipsResidueFixK(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	image, entry, residue := reviewKLegacyImage(t, svc, root, "legacy-image-purge")
	center := NewTrashCenter(nil, svc)
	purged, err := center.PurgeTrashEntries("image", []uint{entry.ID})
	if err != nil || purged.Succeeded != 1 {
		t.Fatalf("图片 legacy 行清除应成功: %#v err=%v", purged, err)
	}
	if _, err := os.Stat(entry.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧版 trash/ 里的这张图片应被删除: %v", err)
	}
	reviewKAssertImageTombstoneAndSyncSkips(t, svc, center, root, image, entry, residue)
}

func TestIMG02LegacyImageFileGoneRemoveRecordLeavesTombstoneAndSyncSkipsResidueFixK(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	image, entry, residue := reviewKLegacyImage(t, svc, root, "legacy-image-gone")
	if err := os.Remove(entry.TrashPath); err != nil {
		t.Fatal(err)
	}
	center := NewTrashCenter(nil, svc)
	page, err := center.ListTrashEntries(TrashFilter{Kind: "image"})
	if err != nil || len(page.Items) != 1 || page.Items[0].State != models.TrashStateFileGone {
		t.Fatalf("前提：列表应把图片条目标为 file_gone: %#v err=%v", page, err)
	}
	removed, err := center.RemoveGoneTrashEntries("image", []uint{entry.ID})
	if err != nil || removed.Succeeded != 1 {
		t.Fatalf("file_gone 的图片 legacy 行移除记录应成功: %#v err=%v", removed, err)
	}
	reviewKAssertImageTombstoneAndSyncSkips(t, svc, center, root, image, entry, residue)
}
