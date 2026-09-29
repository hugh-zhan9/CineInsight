package services

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"github.com/fsnotify/fsnotify"
	"gorm.io/gorm"
)

// 产品完善度 P-011：可见性、扫描、迁移与重命名（后端）的回归测试。
// 测试名里的 LIBxx / PLAYxx 是问题清单 ID（去掉连字符）。

var legacyTrashEntrySeq atomic.Uint32

// registerLegacyTrashEntry 登记一条旧版回收站条目，让 trashPath 所在目录进入旧版回收站目录集合。
func registerLegacyTrashEntry(t *testing.T, original, trashPath string) {
	t.Helper()
	entry := models.VideoTrashEntry{
		VideoID:      900000 + uint(legacyTrashEntrySeq.Add(1)),
		VideoName:    filepath.Base(original),
		OriginalPath: original,
		TrashPath:    trashPath,
		FileMoved:    true,
		State:        trashStateDeleted,
		Mode:         models.TrashModeLegacyTrash,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("登记旧版回收站条目失败: %v", err)
	}
	refreshLegacyTrashDirs()
}

func createOldVideoFile(t *testing.T, path string) {
	t.Helper()
	mustCreateFile(t, path)
	mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
}

func createDirectoryRow(t *testing.T, path string) models.ScanDirectory {
	t.Helper()
	dir := models.ScanDirectory{Path: path, Alias: "root"}
	if err := database.DB.Create(&dir).Error; err != nil {
		t.Fatalf("创建扫描目录失败: %v", err)
	}
	return dir
}

func p011ReloadVideo(t *testing.T, id uint) models.Video {
	t.Helper()
	var video models.Video
	if err := database.DB.Unscoped().First(&video, id).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	return video
}

// ---------- LIB-14：回收站判定与扫描回报 ----------

func TestUserNamedTrashDirectoryIsScannedLIB14(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	userTrash := filepath.Join(root, "Trash", "keep.mp4")
	// macOS 文件系统不区分大小写，两个目录必须放在不同父目录下。
	legacyTrash := filepath.Join(root, "legacy", "trash", "old.mp4")
	createOldVideoFile(t, userTrash)
	createOldVideoFile(t, legacyTrash)

	svc := &VideoService{}
	files, err := svc.ScanDirectory(root)
	if err != nil || len(files) != 2 {
		t.Fatalf("没有旧版回收站条目时，名为 Trash/trash 的目录都应被扫描: files=%v err=%v", files, err)
	}

	registerLegacyTrashEntry(t, filepath.Join(root, "old.mp4"), legacyTrash)
	files, err = svc.ScanDirectory(root)
	if err != nil || len(files) != 1 || files[0] != userTrash {
		t.Fatalf("只有旧版回收站目录被跳过，用户的 Trash 目录仍应被扫描: files=%v err=%v", files, err)
	}
	if !isTrashPath(legacyTrash) || isTrashPath(userTrash) {
		t.Fatalf("isTrashPath 只认旧版回收站目录: legacy=%v user=%v", isTrashPath(legacyTrash), isTrashPath(userTrash))
	}
	if !isTrashPath(filepath.Join(root, ".Trash", "a.mp4")) || !isTrashPath(filepath.Join(root, ".Trashes", "501", "a.mp4")) {
		t.Fatal("系统废纸篓目录名（.Trash / .Trashes）仍应被识别")
	}
}

func TestLegacyTrashDirsIncludeUnbackfilledMovedEntriesLIB14(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	trashPath := filepath.Join(root, "trash", "old.mp4")
	// 回填之前的旧版条目 mode 为空、file_moved=true，同样属于旧版回收站。
	entry := models.VideoTrashEntry{VideoID: 1, VideoName: "old.mp4", OriginalPath: filepath.Join(root, "old.mp4"),
		TrashPath: trashPath, FileMoved: true, State: trashStateDeleted}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	refreshLegacyTrashDirs()
	if !isTrashPath(trashPath) {
		t.Fatal("mode 为空且 file_moved 的条目应计入旧版回收站目录")
	}
	if err := database.DB.Model(&entry).Update("mode", models.TrashModeRecordOnly).Error; err != nil {
		t.Fatal(err)
	}
	refreshLegacyTrashDirs()
	if isTrashPath(trashPath) {
		t.Fatal("record_only 条目不属于旧版回收站")
	}
}

func TestScanSkipBreakdownSumsToSkippedLIB14(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	createOldVideoFile(t, filepath.Join(root, "stable.mp4"))
	createOldVideoFile(t, filepath.Join(root, "downloading.temp.mp4"))
	mustCreateFile(t, filepath.Join(root, "recent.mp4"))
	createOldVideoFile(t, filepath.Join(root, "notes.txt"))

	result := (&VideoService{}).SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if result.Added != 1 {
		t.Fatalf("应只新增稳定视频: %+v", result)
	}
	breakdown := result.SkipBreakdown
	if breakdown.TempFile != 1 || breakdown.RecentlyModified != 1 {
		t.Fatalf("临时后缀与刚修改的文件应分项计数: %+v", breakdown)
	}
	if result.Skipped != breakdown.Total() || result.Skipped != 2 {
		t.Fatalf("skipped 必须等于各项之和且不含无关文件: skipped=%d breakdown=%+v", result.Skipped, breakdown)
	}

	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	var keys map[string]int
	if err := json.Unmarshal(decoded["skip_breakdown"], &keys); err != nil {
		t.Fatalf("payload 缺少 skip_breakdown: %s", payload)
	}
	for _, key := range []string{"existing", "blocked_user_delete", "recently_modified", "temp_file", "not_video", "read_error"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("skip_breakdown 缺少 %s: %v", key, keys)
		}
	}
}

func TestScanSkipReasonMapsBlockedByUserDeleteLIB14(t *testing.T) {
	if reason, ok := scanAddSkipReason(ErrVideoBlockedByUserDelete); !ok || reason != skipReasonBlockedUserDelete {
		t.Fatalf("屏蔽错误应计入 blocked_user_delete: %q %v", reason, ok)
	}
	if reason, ok := scanAddSkipReason(ErrVideoExists); !ok || reason != skipReasonExisting {
		t.Fatalf("已存在应计入 existing: %q %v", reason, ok)
	}
	if _, ok := scanAddSkipReason(errors.New("boom")); ok {
		t.Fatal("其他错误不是跳过")
	}
	result := &ScanSyncResult{}
	result.recordSkip(skipReasonBlockedUserDelete)
	result.recordSkip(skipReasonExisting)
	result.recordError("add", "", "", errors.New("boom"))
	if result.SkipBreakdown.BlockedUserDelete != 1 || result.SkipBreakdown.Existing != 1 || result.SkipBreakdown.ReadError != 1 ||
		result.Skipped != result.SkipBreakdown.Total() || result.Skipped != 3 {
		t.Fatalf("分项与总数不一致: %+v", result)
	}
}

func TestScanSummaryEventCarriesBreakdownLIB08(t *testing.T) {
	result := &ScanSyncResult{Added: 2, Restored: 1}
	result.recordSkip(skipReasonTempFile)
	payload, err := json.Marshal(LibraryScanSummaryEvent{Trigger: ScanTriggerStartup, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, want := range []string{`"trigger":"startup"`, `"skip_breakdown"`, `"temp_file":1`, `"restored":1`} {
		if !strings.Contains(text, want) {
			t.Fatalf("事件载荷缺少 %s: %s", want, text)
		}
	}
	summary := SummarizeScanResult(result)
	if summary == nil || summary.SkipBreakdown.TempFile != 1 || summary.Skipped != 1 {
		t.Fatalf("watcher 摘要应带 skip_breakdown: %+v", summary)
	}
}

// ---------- LIB-01 / LIB-10：失效原因 ----------

func TestRemovedRootAndOrphanStaleReasonsLIB01(t *testing.T) {
	setupVideoServiceTestDB(t)
	removed := t.TempDir()
	kept := t.TempDir()
	orphanRoot := t.TempDir()
	removedVideo := createScanVisibilityVideo(t, filepath.Join(removed, "a.mp4"), false)
	keptVideo := createScanVisibilityVideo(t, filepath.Join(kept, "b.mp4"), false)
	orphan := createScanVisibilityVideo(t, filepath.Join(orphanRoot, "c.mp4"), false)

	svc := &VideoService{}
	marked, err := svc.MarkVideosStaleUnderRemovedRoot(removed, []string{kept})
	if err != nil || marked != 1 {
		t.Fatalf("应只标记被移除根下的视频: marked=%d err=%v", marked, err)
	}
	if got := p011ReloadVideo(t, removedVideo.ID); !got.IsStale || got.StaleReason != models.StaleReasonRemovedRoot {
		t.Fatalf("removed_root 原因缺失: %+v", got)
	}

	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: kept}})
	if result.Stale != 1 {
		t.Fatalf("孤儿对账应标记一条: %+v", result)
	}
	if got := p011ReloadVideo(t, orphan.ID); !got.IsStale || got.StaleReason != models.StaleReasonOutsideRoots {
		t.Fatalf("outside_roots 原因缺失: %+v", got)
	}
	if got := p011ReloadVideo(t, keptVideo.ID); got.IsStale || got.StaleReason != "" {
		t.Fatalf("配置根下的视频不受影响: %+v", got)
	}
}

func TestFullScanOfflineRootWritesOfflineReasonLIB07(t *testing.T) {
	setupVideoServiceTestDB(t)
	parent := t.TempDir()
	offlineRoot := filepath.Join(parent, "external")
	video := models.Video{Name: "a.mp4", Path: filepath.Join(offlineRoot, "a.mp4"), Directory: offlineRoot, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	result := (&VideoService{}).SyncScanDirectories([]models.ScanDirectory{{Path: offlineRoot}})
	if result.Stale != 1 || result.Deleted != 0 {
		t.Fatalf("离线根只标失效不删除: %+v", result)
	}
	if got := p011ReloadVideo(t, video.ID); !got.IsStale || got.StaleReason != models.StaleReasonOfflineRoot || got.DeletedAt.IsValid() {
		t.Fatalf("启动/全量扫描的离线判定应写 offline_root: %+v", got)
	}
}

func TestMarkRootOfflineSkipsNestedOnlineRootLIB07(t *testing.T) {
	setupVideoServiceTestDB(t)
	// 在线的父根 + 一个不存在（离线）的子根：子根下的视频仍归在线父根管，不能被标离线。
	onlineParent := t.TempDir()
	createDirectoryRow(t, onlineParent)
	offlineChild := filepath.Join(onlineParent, "usb")
	createDirectoryRow(t, offlineChild)
	childVideo := models.Video{Name: "b.mp4", Path: filepath.Join(offlineChild, "b.mp4"), Directory: offlineChild, Size: 1}
	parentVideo := models.Video{Name: "c.mp4", Path: filepath.Join(onlineParent, "c.mp4"), Directory: onlineParent, Size: 1}
	// 完全独立的离线根：其下视频应被标记。
	offlineRoot := filepath.Join(t.TempDir(), "gone")
	createDirectoryRow(t, offlineRoot)
	insideOffline := models.Video{Name: "a.mp4", Path: filepath.Join(offlineRoot, "a.mp4"), Directory: offlineRoot, Size: 1}
	for _, v := range []*models.Video{&childVideo, &parentVideo, &insideOffline} {
		if err := database.DB.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}

	svc := &VideoService{}
	marked, err := svc.MarkRootOffline(offlineRoot)
	if err != nil || marked != 1 {
		t.Fatalf("离线根下的视频应被标记: marked=%d err=%v", marked, err)
	}
	if got := p011ReloadVideo(t, insideOffline.ID); !got.IsStale || got.StaleReason != models.StaleReasonOfflineRoot {
		t.Fatalf("offline_root 缺失: %+v", got)
	}
	marked, err = svc.MarkRootOffline(offlineChild)
	if err != nil || marked != 0 {
		t.Fatalf("仍属于其他可用根的视频不能被标离线: marked=%d err=%v", marked, err)
	}
	for _, id := range []uint{childVideo.ID, parentVideo.ID} {
		if got := p011ReloadVideo(t, id); got.IsStale {
			t.Fatalf("在线根管辖的视频被误标: %+v", got)
		}
	}
	if marked, err := svc.MarkRootOffline(" "); err == nil || marked != 0 {
		t.Fatal("空路径应报错")
	}
}

func TestNarrowReconcileClearsOfflineRootAndRefinesMissingLIB07(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	dir := createDirectoryRow(t, root)
	present := createScanVisibilityVideo(t, filepath.Join(root, "present.mp4"), false)
	absent := createScanVisibilityVideo(t, filepath.Join(root, "absent.mp4"), false)
	if err := os.Remove(absent.Path); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{present.ID, absent.ID} {
		if err := markVideoStale(id, models.StaleReasonOfflineRoot).Error; err != nil {
			t.Fatal(err)
		}
	}

	result := (&VideoService{}).SyncAffectedDirectories([]models.ScanDirectory{dir}, []string{root})
	if result.Restored != 1 {
		t.Fatalf("文件存在的记录应恢复: %+v", result)
	}
	if got := p011ReloadVideo(t, present.ID); got.IsStale || got.StaleReason != "" {
		t.Fatalf("恢复后必须清空失效原因: %+v", got)
	}
	if got := p011ReloadVideo(t, absent.ID); !got.IsStale || got.StaleReason != models.StaleReasonMissingFile {
		t.Fatalf("根已回来但文件仍缺失，原因应从 offline_root 改为 missing_file: %+v", got)
	}
}

// staleGuardPendingFixes 是扫描出的、不在 P-010 / P-011 可修改范围内的违规点。每一项都带说明，由后续切片修复后
// 从这里删除；新增违规点不允许往这里加。key 是「文件:函数名」。
var staleGuardPendingFixes = map[string]string{}

// TestStaleWritesAlwaysCarryReasonLIB10 用 AST 扫描 services 下全部非测试 .go 文件：
// 视频表上任何把 is_stale 写成 true 的 Update / Updates / UpdateColumn(s) / 原生 SQL，必须在同一语句里写非空 stale_reason；
// 写成 false 时必须同时把 stale_reason 清空。images 表没有 stale_reason 列，不受此约束。
func TestStaleWritesAlwaysCarryReasonLIB10(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("扫描 services 源文件失败: %v", err)
	}
	updateNames := map[string]bool{"Update": true, "Updates": true, "UpdateColumn": true, "UpdateColumns": true}
	rawNames := map[string]bool{"Exec": true, "Raw": true}
	stringOf := func(expr ast.Expr) (string, bool) {
		lit, ok := expr.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(lit.Value)
		return value, err == nil
	}
	isEmptyString := func(expr ast.Expr) bool {
		value, ok := stringOf(expr)
		return ok && value == ""
	}
	isModelsImage := func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := selector.X.(*ast.Ident)
		return ok && pkg.Name == "models" && selector.Sel.Name == "Image"
	}
	// imageVariables 找出函数里类型是 models.Image（含指针、切片）的形参与局部变量名。
	imageVariables := func(fn *ast.FuncDecl) map[string]bool {
		names := make(map[string]bool)
		typeIsImage := func(expr ast.Expr) bool {
			found := false
			ast.Inspect(expr, func(node ast.Node) bool {
				if isModelsImage(node) {
					found = true
				}
				return !found
			})
			return found
		}
		if fn.Type.Params != nil {
			for _, field := range fn.Type.Params.List {
				if typeIsImage(field.Type) {
					for _, ident := range field.Names {
						names[ident.Name] = true
					}
				}
			}
		}
		ast.Inspect(fn, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.ValueSpec:
				if typed.Type != nil && typeIsImage(typed.Type) {
					for _, ident := range typed.Names {
						names[ident.Name] = true
					}
				}
			case *ast.AssignStmt:
				for index, rhs := range typed.Rhs {
					if index < len(typed.Lhs) && typeIsImage(rhs) {
						if ident, ok := typed.Lhs[index].(*ast.Ident); ok {
							names[ident.Name] = true
						}
					}
				}
			}
			return true
		})
		return names
	}
	// receiverMentionsImages 沿调用链找 models.Image、Table("images") 或图片类型的变量。
	receiverMentionsImages := func(expr ast.Expr, imageVars map[string]bool) bool {
		mentions := false
		ast.Inspect(expr, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.SelectorExpr:
				if isModelsImage(typed) {
					mentions = true
				}
			case *ast.Ident:
				if imageVars[typed.Name] {
					mentions = true
				}
			case *ast.BasicLit:
				if value, ok := stringOf(typed); ok && value == "images" {
					mentions = true
				}
			}
			return !mentions
		})
		return mentions
	}

	violations := make(map[string]string)
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(raw), "\n")
		nearbyReason := func(line int) bool {
			for i := max(0, line-7); i < min(len(lines), line+6); i++ {
				if strings.Contains(lines[i], `"stale_reason"`) {
					return true
				}
			}
			return false
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			imageVars := imageVariables(fn)
			ast.Inspect(fn, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				key := name + ":" + fn.Name.Name
				position := fset.Position(call.Pos())
				report := func(message string) {
					violations[key] = message + "（" + name + ":" + strconv.Itoa(position.Line) + "）"
				}
				if rawNames[selector.Sel.Name] {
					for _, arg := range call.Args {
						sql, ok := stringOf(arg)
						if !ok {
							continue
						}
						lower := strings.ToLower(sql)
						if strings.Contains(lower, "is_stale") && strings.Contains(lower, "update") && strings.Contains(lower, " set ") &&
							!strings.Contains(lower, "images") && !strings.Contains(lower, "stale_reason") {
							report("原生 SQL 写了 is_stale 却没有写 stale_reason")
						}
					}
					return true
				}
				if !updateNames[selector.Sel.Name] || receiverMentionsImages(selector.X, imageVars) {
					return true
				}
				for index, arg := range call.Args {
					if column, ok := stringOf(arg); ok && column == "is_stale" && index == 0 {
						// 单列写法：值在下一个参数，原因没法在同一语句里写，只能靠紧邻的另一条带原因的更新。
						if !nearbyReason(position.Line) {
							report("单列 Update(\"is_stale\", …) 没有紧邻的 stale_reason 写入")
						}
						continue
					}
					literal, ok := arg.(*ast.CompositeLit)
					if !ok {
						continue
					}
					var staleValue, reasonValue ast.Expr
					hasStale, hasReason := false, false
					for _, element := range literal.Elts {
						pair, ok := element.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						fieldName := ""
						if text, ok := stringOf(pair.Key); ok {
							fieldName = text
						} else if ident, ok := pair.Key.(*ast.Ident); ok {
							fieldName = ident.Name
						}
						switch fieldName {
						case "is_stale", "IsStale":
							hasStale, staleValue = true, pair.Value
						case "stale_reason", "StaleReason":
							hasReason, reasonValue = true, pair.Value
						}
					}
					if !hasStale {
						continue
					}
					if !hasReason {
						report("写 is_stale 的同一语句里没有 stale_reason")
						continue
					}
					if ident, ok := staleValue.(*ast.Ident); ok && ident.Name == "true" && isEmptyString(reasonValue) {
						report("is_stale=true 时 stale_reason 不能是空串")
					}
					if ident, ok := staleValue.(*ast.Ident); ok && ident.Name == "false" && !isEmptyString(reasonValue) {
						report("is_stale=false 时必须把 stale_reason 清空")
					}
				}
				return true
			})
		}
	}
	for key, message := range violations {
		if _, pending := staleGuardPendingFixes[key]; pending {
			continue
		}
		t.Errorf("%s: %s", key, message)
	}
	// 待修白名单里的项被修好后必须从白名单删除，避免它变成永久豁免。
	for key := range staleGuardPendingFixes {
		if _, still := violations[key]; !still {
			t.Errorf("待修白名单项 %s 已不再违规，请从 staleGuardPendingFixes 删除", key)
		}
	}
}

func TestRecheckVideosRestoresAndDeduplicatesDirectoriesLIB10(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	dir := createDirectoryRow(t, root)
	first := createScanVisibilityVideo(t, filepath.Join(root, "sub", "a.mp4"), false)
	second := createScanVisibilityVideo(t, filepath.Join(root, "sub", "b.mp4"), false)
	gone := createScanVisibilityVideo(t, filepath.Join(root, "vanished", "c.mp4"), false)
	if err := os.RemoveAll(filepath.Dir(gone.Path)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{first.ID, second.ID, gone.ID} {
		if err := markVideoStale(id, models.StaleReasonPlayFailed).Error; err != nil {
			t.Fatal(err)
		}
	}
	_ = dir

	summary, err := (&VideoService{}).RecheckVideos([]uint{first.ID, second.ID, gone.ID})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Restored != 2 {
		t.Fatalf("同目录两条应一次窄对账恢复: %+v", summary)
	}
	for _, id := range []uint{first.ID, second.ID} {
		if got := p011ReloadVideo(t, id); got.IsStale || got.StaleReason != "" {
			t.Fatalf("重新检查后应恢复: %+v", got)
		}
	}
	if got := p011ReloadVideo(t, gone.ID); !got.IsStale {
		t.Fatalf("目录已不存在的记录应保持失效: %+v", got)
	}

	empty, err := (&VideoService{}).RecheckVideos(nil)
	if err != nil || empty.Scanned != 0 {
		t.Fatalf("空输入应是空操作: %+v err=%v", empty, err)
	}
}

func TestReaddRemovedRootReturnsOriginalRootLIB10(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	dir := createDirectoryRow(t, root)
	video := createScanVisibilityVideo(t, filepath.Join(root, "a", "movie.mp4"), false)
	if _, err := (&VideoService{}).ReaddRemovedRoot(video.ID); err == nil {
		t.Fatal("没有被移除的根时应报错，不能猜一个目录")
	}
	if err := (&DirectoryService{}).DeleteDirectory(dir.ID); err != nil {
		t.Fatal(err)
	}
	got, err := (&VideoService{}).ReaddRemovedRoot(video.ID)
	if err != nil || got != root {
		t.Fatalf("应返回原扫描根: got=%q err=%v", got, err)
	}
}

// ---------- LIB-02：编辑路径 = 重映射 ----------

func TestRemapDirectoryKeepsRecordsAndRewritesPathsLIB02(t *testing.T) {
	setupVideoServiceTestDB(t)
	parent := t.TempDir()
	oldRoot := filepath.Join(parent, "Movies")
	newRoot := filepath.Join(parent, "Movies 1")
	videoPath := filepath.Join(oldRoot, "sub", "movie.mp4")
	createOldVideoFile(t, videoPath)
	dir := createDirectoryRow(t, oldRoot)

	tag := models.Tag{Name: "保留标签"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	rating := 4.5
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: filepath.Dir(videoPath), Size: 1, PersonalRating: &rating}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&video).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	if err := markVideoStale(video.ID, models.StaleReasonOfflineRoot).Error; err != nil {
		t.Fatal(err)
	}
	trash := models.VideoTrashEntry{VideoID: 9001, VideoName: "x.mp4", OriginalPath: filepath.Join(oldRoot, "x.mp4"), State: trashStateDeleted, Mode: models.TrashModeRecordOnly}
	if err := database.DB.Create(&trash).Error; err != nil {
		t.Fatal(err)
	}
	excluded := filepath.Join(oldRoot, "skip")
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", excluded).Error; err != nil {
		t.Fatal(err)
	}
	index := models.SubtitleIndexState{VideoID: video.ID, SubtitlePath: filepath.Join(oldRoot, "sub", "movie.srt")}
	if err := database.DB.Create(&index).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldRoot, newRoot); err != nil {
		t.Fatal(err)
	}

	result, err := (&DirectoryService{}).UpdateDirectory(dir.ID, newRoot, "renamed", DirectoryUpdateModeRemap)
	if err != nil {
		t.Fatalf("重映射失败: %v", err)
	}
	if !result.PathChanged || result.Rewritten.Videos != 1 || result.Rewritten.TrashEntries != 1 ||
		result.Rewritten.ScanExclusions != 1 || result.Rewritten.SubtitleIndexes != 1 || result.Rewritten.Directories != 1 {
		t.Fatalf("改写条数不对: %+v", result)
	}

	var loaded models.Video
	if err := database.DB.Preload("Tags").First(&loaded, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(newRoot, "sub", "movie.mp4")
	if loaded.Path != wantPath || loaded.Directory != filepath.Dir(wantPath) || loaded.IsStale || loaded.StaleReason != "" {
		t.Fatalf("路径与失效标记应一起改写: %+v", loaded)
	}
	if len(loaded.Tags) != 1 || loaded.Tags[0].ID != tag.ID || loaded.PersonalRating == nil || *loaded.PersonalRating != 4.5 {
		t.Fatalf("重映射后标签与评分必须保留原 ID 上: %+v", loaded)
	}
	var loadedDir models.ScanDirectory
	if err := database.DB.First(&loadedDir, dir.ID).Error; err != nil || loadedDir.Path != newRoot || loadedDir.Alias != "renamed" {
		t.Fatalf("扫描目录未更新: %+v err=%v", loadedDir, err)
	}

	// 窄对账之后不能出现重复记录（旧行为是新建一条、元数据留在旧记录上）。
	dirs := []models.ScanDirectory{loadedDir}
	if sync := (&VideoService{}).SyncAffectedDirectories(dirs, []string{newRoot}); sync.Added != 0 {
		t.Fatalf("重映射后窄对账不应新增记录: %+v", sync)
	}
	var count int64
	if err := database.DB.Model(&models.Video{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("视频记录数应保持 1: %d err=%v", count, err)
	}
}

func TestRemapDirectoryValidationLIB02(t *testing.T) {
	setupVideoServiceTestDB(t)
	parent := t.TempDir()
	oldRoot := filepath.Join(parent, "a")
	other := filepath.Join(parent, "other")
	for _, dir := range []string{oldRoot, other, filepath.Join(other, "nested"), filepath.Join(parent, "free")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	row := createDirectoryRow(t, oldRoot)
	createDirectoryRow(t, other)
	svc := &DirectoryService{}
	cases := map[string]string{
		"不存在":       filepath.Join(parent, "missing"),
		"与其他根相同":    other,
		"嵌套在其他根内":   filepath.Join(other, "nested"),
		"包含原路径":     parent,
		"新路径在原路径之内": filepath.Join(oldRoot, "child"),
	}
	if err := os.MkdirAll(filepath.Join(oldRoot, "child"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, target := range cases {
		if _, err := svc.UpdateDirectory(row.ID, target, "x", DirectoryUpdateModeRemap); err == nil {
			t.Fatalf("%s：应拒绝重映射", name)
		}
	}
	if _, err := svc.UpdateDirectory(row.ID, filepath.Join(parent, "free"), "x", "bogus"); err == nil {
		t.Fatal("未知 mode 应被拒绝")
	}
	var unchanged models.ScanDirectory
	if err := database.DB.First(&unchanged, row.ID).Error; err != nil || unchanged.Path != oldRoot {
		t.Fatalf("失败的重映射不能改动扫描目录: %+v", unchanged)
	}

	// 新路径下已有同路径的活跃记录：给出明确错误，不让唯一索引在事务中途失败。
	free := filepath.Join(parent, "free")
	if err := database.DB.Create(&models.Video{Name: "m.mp4", Path: filepath.Join(oldRoot, "m.mp4"), Directory: oldRoot, Size: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.Video{Name: "m.mp4", Path: filepath.Join(free, "m.mp4"), Directory: free, Size: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateDirectory(row.ID, free, "x", DirectoryUpdateModeRemap); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("撞路径应给出可读错误: %v", err)
	}
}

func TestReplaceDirectoryMarksOldRootRecordsRemovedLIB02(t *testing.T) {
	setupVideoServiceTestDB(t)
	parent := t.TempDir()
	oldRoot := filepath.Join(parent, "old")
	newRoot := filepath.Join(parent, "new")
	if err := os.MkdirAll(newRoot, 0755); err != nil {
		t.Fatal(err)
	}
	row := createDirectoryRow(t, oldRoot)
	video := models.Video{Name: "a.mp4", Path: filepath.Join(oldRoot, "a.mp4"), Directory: oldRoot, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	result, err := (&DirectoryService{}).UpdateDirectory(row.ID, newRoot, "n", DirectoryUpdateModeReplace)
	if err != nil || result.MarkedStale != 1 {
		t.Fatalf("replace 应标记旧根记录: %+v err=%v", result, err)
	}
	if got := p011ReloadVideo(t, video.ID); !got.IsStale || got.StaleReason != models.StaleReasonRemovedRoot || got.Path != video.Path {
		t.Fatalf("replace 不改记录路径，只标 removed_root: %+v", got)
	}
	var loaded models.ScanDirectory
	if err := database.DB.First(&loaded, row.ID).Error; err != nil || loaded.Path != newRoot {
		t.Fatalf("路径应更新: %+v", loaded)
	}

	// 只改别名：不动记录。
	if _, err := (&DirectoryService{}).UpdateDirectory(row.ID, newRoot, "again", DirectoryUpdateModeReplace); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.First(&loaded, row.ID).Error; err != nil || loaded.Alias != "again" {
		t.Fatalf("别名应更新: %+v", loaded)
	}
}

func TestValidateScanDirectoryReportsDuplicateAndNestingLIB14(t *testing.T) {
	setupVideoServiceTestDB(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	inner := filepath.Join(root, "inner")
	if err := os.MkdirAll(inner, 0755); err != nil {
		t.Fatal(err)
	}
	createDirectoryRow(t, root)
	createDirectoryRow(t, filepath.Join(parent, "far", "deep"))
	svc := &DirectoryService{}

	got, err := svc.ValidateScanDirectory(root)
	if err != nil || !got.Exists || got.DuplicateOf != root {
		t.Fatalf("重复目录: %+v err=%v", got, err)
	}
	got, _ = svc.ValidateScanDirectory(inner)
	if got.NestedIn != root || got.DuplicateOf != "" {
		t.Fatalf("嵌套目录: %+v", got)
	}
	got, _ = svc.ValidateScanDirectory(parent)
	if len(got.Contains) != 2 || got.NestedIn != "" {
		t.Fatalf("包含已有根: %+v", got)
	}
	got, _ = svc.ValidateScanDirectory(filepath.Join(parent, "nope"))
	if got.Exists {
		t.Fatalf("不存在的目录: %+v", got)
	}
	if _, err := svc.ValidateScanDirectory("  "); err == nil {
		t.Fatal("空路径应报错")
	}
}

// ---------- LIB-03：重命名扩展名规则 ----------

func TestRenameVideoOnlyTreatsConfiguredSuffixAsExtensionLIB03(t *testing.T) {
	cases := []struct {
		name    string
		oldName string
		input   string
		want    string
	}{
		{"点号文件名补回扩展名", "The.Matrix.1999.1080p.mkv", "The.Matrix.1999.1080p", "The.Matrix.1999.1080p.mkv"},
		{"多段点号", "a.b.c.mp4", "a.b.c", "a.b.c.mp4"},
		{"显式改为配置内的扩展名", "movie.mkv", "movie.mp4", "movie.mp4"},
		{"没有扩展名", "clip.mp4", "renamed", "renamed.mp4"},
		{"新增点号后缀", "clip.mp4", "clip.part2", "clip.part2.mp4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("video_extensions", ".mp4,.mkv").Error; err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			oldPath := filepath.Join(root, tc.oldName)
			mustCreateFile(t, oldPath)
			video := models.Video{Name: tc.oldName, Path: oldPath, Directory: root, Size: 1}
			if err := database.DB.Create(&video).Error; err != nil {
				t.Fatal(err)
			}
			if err := (&VideoService{}).RenameVideo(video.ID, tc.input); err != nil {
				t.Fatalf("重命名失败: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, tc.want)); err != nil {
				entries, _ := os.ReadDir(root)
				t.Fatalf("磁盘文件应为 %s: %v (目录内容 %v)", tc.want, err, entries)
			}
			if got := p011ReloadVideo(t, video.ID); got.Name != tc.want || got.Path != filepath.Join(root, tc.want) {
				t.Fatalf("记录应与磁盘一致: %+v", got)
			}
		})
	}
}

func TestRenameVideoUnchangedNameIsNoOpLIB03(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "The.Matrix.1999.1080p.mp4")
	mustCreateFile(t, path)
	video := models.Video{Name: filepath.Base(path), Path: path, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	// 预填的名字（去掉真扩展名）原样确认：规范化后与当前文件名相同，直接返回。
	if err := (&VideoService{}).RenameVideo(video.ID, "The.Matrix.1999.1080p"); err != nil {
		t.Fatalf("名称未变应直接返回 nil: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件不应被改动: %v", err)
	}
}

// ---------- LIB-06 / LIB-09 / LIB-11：迁移 ----------

func TestMoveDirectoryRewritesTrashEntriesAndExclusionsLIB06(t *testing.T) {
	setupVideoServiceTestDB(t)
	parent := t.TempDir()
	source := filepath.Join(parent, "src", "series")
	destinationParent := filepath.Join(parent, "dst")
	if err := os.MkdirAll(destinationParent, 0755); err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(source, "ep1.mp4")
	createOldVideoFile(t, videoPath)
	dir := createDirectoryRow(t, source)
	video := models.Video{Name: "ep1.mp4", Path: videoPath, Directory: source, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	trash := models.VideoTrashEntry{VideoID: 9002, VideoName: "gone.mp4", OriginalPath: filepath.Join(source, "gone.mp4"),
		State: trashStateDeleted, Mode: models.TrashModeRecordOnly}
	if err := database.DB.Create(&trash).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", filepath.Join(source, "skip")).Error; err != nil {
		t.Fatal(err)
	}

	result, err := (&VideoService{}).MoveDirectory(source, destinationParent)
	if err != nil {
		t.Fatalf("迁移文件夹失败: %v", err)
	}
	destination := filepath.Join(destinationParent, "series")
	if result.VideosUpdated != 1 || result.DirectoriesUpdated != 1 || result.TrashEntriesUpdated != 1 || result.ScanExclusionsUpdated != 1 {
		t.Fatalf("结果字段应如实填写: %+v", result)
	}
	var entry models.VideoTrashEntry
	if err := database.DB.First(&entry, trash.ID).Error; err != nil || entry.OriginalPath != filepath.Join(destination, "gone.mp4") {
		t.Fatalf("回收站条目应同步改写: %+v err=%v", entry, err)
	}
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil || strings.TrimSpace(settings.ScanExcludePaths) != filepath.Join(destination, "skip") {
		t.Fatalf("黑名单应同步改写: %q err=%v", settings.ScanExcludePaths, err)
	}
	var loadedDir models.ScanDirectory
	if err := database.DB.First(&loadedDir, dir.ID).Error; err != nil || loadedDir.Path != destination {
		t.Fatalf("扫描目录应同步改写: %+v", loadedDir)
	}
	if got := p011ReloadVideo(t, video.ID); got.Path != filepath.Join(destination, "ep1.mp4") {
		t.Fatalf("视频路径应改写: %+v", got)
	}
}

func TestCheckMoveTargetLIB09(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	createDirectoryRow(t, root)
	outside := t.TempDir()
	svc := &VideoService{}
	for target, want := range map[string]bool{
		root:                                   true,
		filepath.Join(root, "sub", "deeper"):   true,
		outside:                                false,
		filepath.Join(filepath.Dir(root), "x"): false,
	} {
		check, err := svc.CheckMoveTarget(target)
		if err != nil || check.InScanRoots != want {
			t.Fatalf("CheckMoveTarget(%s) = %+v err=%v，期望 in_scan_roots=%v", target, check, err, want)
		}
	}
	if _, err := svc.CheckMoveTarget(" "); err == nil {
		t.Fatal("空目标应报错")
	}
}

func forceCrossFilesystemLinks(t *testing.T) {
	t.Helper()
	previous := linkFile
	linkFile = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { linkFile = previous })
}

func TestCrossFilesystemMoveRegistersStagedSourceLIB11(t *testing.T) {
	setupVideoServiceTestDB(t)
	forceCrossFilesystemLinks(t)
	root := t.TempDir()
	destination := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, videoPath)
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}

	svc := &VideoService{}
	result, err := svc.MoveVideo(video.ID, destination)
	if err != nil || result.Warning == "" {
		t.Fatalf("跨盘迁移应成功并带警告: %+v err=%v", result, err)
	}
	staged, err := svc.ListStagedSources()
	if err != nil || len(staged) != 1 {
		t.Fatalf("暂存源文件应在迁移事务里登记: %+v err=%v", staged, err)
	}
	row := staged[0]
	if row.State != models.MigrationStagedStatePending || row.OriginalPath != videoPath || row.VideoID == nil || *row.VideoID != video.ID ||
		!strings.Contains(filepath.Base(row.StagedPath), migrationStagingMarker) || row.Size != 1 {
		t.Fatalf("登记内容不对: %+v", row)
	}
	if _, err := os.Stat(row.StagedPath); err != nil {
		t.Fatalf("暂存文件应仍在磁盘上: %v", err)
	}

	// 用户在 Finder 里手工删掉：列表对账时置为 cleaned。
	if err := os.Remove(row.StagedPath); err != nil {
		t.Fatal(err)
	}
	staged, err = svc.ListStagedSources()
	if err != nil || len(staged) != 0 {
		t.Fatalf("文件不存在的行应对账为 cleaned 且不再返回: %+v err=%v", staged, err)
	}
	var stored models.MigrationStagedSource
	if err := database.DB.First(&stored, row.ID).Error; err != nil || stored.State != models.MigrationStagedStateCleaned || stored.CleanedAt == nil {
		t.Fatalf("状态应置为 cleaned: %+v err=%v", stored, err)
	}
}

func TestCrossFilesystemMoveRollsBackWhenStagedRegistrationFailsLIB11(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	destination := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, videoPath)
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	// 链接钩子里抢先占用暂存路径的唯一键，让事务内的登记必然失败。
	previous := linkFile
	linkFile = func(oldname, newname string) error {
		blocker := models.MigrationStagedSource{OriginalPath: "x", StagedPath: oldname, State: models.MigrationStagedStatePending}
		if err := database.DB.Create(&blocker).Error; err != nil {
			t.Errorf("预置冲突行失败: %v", err)
		}
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { linkFile = previous })

	if _, err := (&VideoService{}).MoveVideo(video.ID, destination); err == nil {
		t.Fatal("登记失败时迁移应整体失败")
	}
	if got := p011ReloadVideo(t, video.ID); got.Path != videoPath {
		t.Fatalf("事务回滚后视频路径应保持: %+v", got)
	}
	if _, err := os.Stat(videoPath); err != nil {
		t.Fatalf("文件应回滚到原位: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "movie.mp4")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("目标副本应被清理: %v", err)
	}
}

func TestCrossFilesystemMoveDirectoryRegistersStagedFolderLIB11(t *testing.T) {
	setupVideoServiceTestDB(t)
	forceCrossFilesystemLinks(t)
	parent := t.TempDir()
	source := filepath.Join(parent, "src", "series")
	destinationParent := filepath.Join(parent, "dst")
	if err := os.MkdirAll(destinationParent, 0755); err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(source, "ep1.mp4")
	createOldVideoFile(t, videoPath)
	createDirectoryRow(t, source)
	video := models.Video{Name: "ep1.mp4", Path: videoPath, Directory: source, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}

	svc := &VideoService{}
	result, err := svc.MoveDirectory(source, destinationParent)
	if err != nil || result.Warning == "" {
		t.Fatalf("跨盘迁移文件夹应成功并带警告: %+v err=%v", result, err)
	}
	staged, err := svc.ListStagedSources()
	if err != nil || len(staged) != 1 || staged[0].OriginalPath != source || staged[0].Size != 1 {
		t.Fatalf("暂存文件夹应登记: %+v err=%v", staged, err)
	}

	deleted, err := svc.DeleteStagedSources([]uint{staged[0].ID})
	if err != nil || deleted.Cleaned != 1 || len(deleted.Failed) != 0 {
		t.Fatalf("清理失败: %+v err=%v", deleted, err)
	}
	if _, err := os.Stat(staged[0].StagedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("暂存文件夹应已删除: %v", err)
	}
	if remaining, err := svc.ListStagedSources(); err != nil || len(remaining) != 0 {
		t.Fatalf("清理后不应再列出: %+v err=%v", remaining, err)
	}
	// 幂等：再次清理同一条是空操作。
	if again, err := svc.DeleteStagedSources([]uint{staged[0].ID}); err != nil || again.Cleaned != 0 {
		t.Fatalf("重复清理应是空操作: %+v err=%v", again, err)
	}
}

func TestDeleteStagedSourcesRefusesNonStagingPathsLIB11(t *testing.T) {
	setupVideoServiceTestDB(t)
	precious := filepath.Join(t.TempDir(), "precious.mp4")
	mustCreateFile(t, precious)
	row := models.MigrationStagedSource{OriginalPath: precious, StagedPath: precious, State: models.MigrationStagedStatePending}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	result, err := (&VideoService{}).DeleteStagedSources([]uint{row.ID})
	if err != nil || result.Cleaned != 0 || len(result.Failed) != 1 {
		t.Fatalf("不像暂存文件的路径必须拒绝: %+v err=%v", result, err)
	}
	if _, err := os.Stat(precious); err != nil {
		t.Fatalf("文件不应被删除: %v", err)
	}
	if empty, err := (&VideoService{}).DeleteStagedSources(nil); err != nil || empty.Cleaned != 0 {
		t.Fatalf("空输入应是空操作: %+v err=%v", empty, err)
	}
}

// ---------- LIB-07：监听与巡检 ----------

func TestLibraryWatcherMarksRemovedRootOfflineLIB07(t *testing.T) {
	root := t.TempDir()
	backend := newFakeLibraryWatchBackend()
	service := newTestLibraryWatcher(backend, func(_ []models.ScanDirectory, _ []string) *ScanSyncResult { return &ScanSyncResult{} })
	marked := make(chan string, 4)
	service.markRootOffline = func(path string) { marked <- path }
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	backend.events <- fsnotify.Event{Name: root, Op: fsnotify.Remove}
	select {
	case got := <-marked:
		if got != filepath.Clean(root) {
			t.Fatalf("标记的根不对: %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("根被移除时应调用 MarkRootOffline")
	}
}

func TestLibraryWatcherMarksOfflineAtStartupWhenRootMissingLIB07(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "unplugged")
	service := newTestLibraryWatcher(newFakeLibraryWatchBackend(), func(_ []models.ScanDirectory, _ []string) *ScanSyncResult { return &ScanSyncResult{} })
	marked := make(chan string, 4)
	service.markRootOffline = func(path string) { marked <- path }
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: missing}}); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	select {
	case got := <-marked:
		if got != missing {
			t.Fatalf("标记的根不对: %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("启动时根不可用应标记离线")
	}
}

func TestLibraryWatcherPatrolRetriesRecoveredRootAndQueuesReconcileLIB07(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "unplugged")
	reconciled := make(chan []string, 4)
	service := newTestLibraryWatcher(newFakeLibraryWatchBackend(), func(_ []models.ScanDirectory, affected []string) *ScanSyncResult {
		reconciled <- affected
		return &ScanSyncResult{}
	})
	service.markRootOffline = func(string) {}
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: missing}}); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if rootState(service.Snapshot(), 1) != LibraryWatchStateUnavailable {
		t.Fatalf("启动时缺失的根应为 unavailable: %+v", service.Snapshot())
	}

	// 仍不可用：巡检不做任何事，也不排队对账。
	service.patrolUnavailableRoots(time.Time{})
	select {
	case got := <-reconciled:
		t.Fatalf("根仍不可用时不应对账: %v", got)
	case <-time.After(60 * time.Millisecond):
	}

	if err := os.Mkdir(missing, 0755); err != nil {
		t.Fatal(err)
	}
	service.patrolUnavailableRoots(time.Time{})
	if rootState(service.Snapshot(), 1) != LibraryWatchStateWatching {
		t.Fatalf("根恢复后巡检应重注册监听: %+v", service.Snapshot())
	}
	select {
	case got := <-reconciled:
		if len(got) != 1 || got[0] != missing {
			t.Fatalf("应对恢复的根排队窄对账: %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("根恢复后应排队窄对账")
	}
}

func TestLibraryWatcherRetryRootQueuesReconcileOnlyAfterUnavailableLIB07(t *testing.T) {
	root := t.TempDir()
	reconciled := make(chan []string, 4)
	service := newTestLibraryWatcher(newFakeLibraryWatchBackend(), func(_ []models.ScanDirectory, affected []string) *ScanSyncResult {
		reconciled <- affected
		return &ScanSyncResult{}
	})
	if err := service.Start(context.Background(), []models.ScanDirectory{{ID: 1, Path: root}}); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	// 一直在线的根手动重试：不是「恢复」，不需要对账。
	if _, err := service.RetryRoot(1); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-reconciled:
		t.Fatalf("在线根重试不应排队对账: %v", got)
	case <-time.After(80 * time.Millisecond):
	}
}

func TestLibraryWatcherOfflineThenRecoveryEndToEndLIB07(t *testing.T) {
	setupVideoServiceTestDB(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "external")
	dir := createDirectoryRow(t, root)
	videoPath := filepath.Join(root, "a.mp4")
	video := models.Video{Name: "a.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}

	svc := &VideoService{}
	service := newTestLibraryWatcher(newFakeLibraryWatchBackend(), func(dirs []models.ScanDirectory, affected []string) *ScanSyncResult {
		return svc.SyncAffectedDirectories(dirs, affected)
	})
	service.videoService = svc
	service.markRootOffline = func(path string) { _, _ = svc.MarkRootOffline(path) }
	if err := service.Start(context.Background(), []models.ScanDirectory{dir}); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	// 离线标记在工作 goroutine 里执行（Minor 12），不在监听事件循环里同步拿写锁：轮询等它落库。
	markDeadline := time.Now().Add(3 * time.Second)
	for {
		got := p011ReloadVideo(t, video.ID)
		if got.IsStale && got.StaleReason == models.StaleReasonOfflineRoot {
			break
		}
		if time.Now().After(markDeadline) {
			t.Fatalf("运行中根不可用应标 offline_root（与启动一致）: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 盘重新插上：文件回来，巡检重连并对账，失效原因与标记一起清除。
	createOldVideoFile(t, videoPath)
	service.patrolUnavailableRoots(time.Time{})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := p011ReloadVideo(t, video.ID); !got.IsStale {
			if got.StaleReason != "" {
				t.Fatalf("恢复后原因必须清空: %+v", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("重连后记录应自动恢复")
}

// ---------- LIB-13 / PLAY-12：播放失败 ----------

// p011StopPlaybackRelocationOnCleanup 让测试结束前确定性地等后台重定位 goroutine 退出（PLAY-12 偶发失败）：
// 播放失败会在后台遍历扫描根、读写 database.DB（SQLite 测试库就在 t.TempDir() 里）。它若活过测试本身，
// 就会在 TempDir 清理时往目录里写 -wal / -shm，报「directory not empty」，或读到下一个测试的库。
// 必须在 setupVideoServiceTestDB 之后调用：t.Cleanup 后进先出，这样它在关库、删临时目录之前执行。
// StopPlaybackRelocation 取消并等待（WaitGroup），不靠 sleep。
func p011StopPlaybackRelocationOnCleanup(t *testing.T) {
	t.Helper()
	t.Cleanup(StopPlaybackRelocation)
}

func TestPlaybackMissingFileOnOfflineRootReturnsImmediatelyWithoutRelocationLIB13(t *testing.T) {
	setupVideoServiceTestDB(t)
	p011StopPlaybackRelocationOnCleanup(t)
	parent := t.TempDir()
	offlineRoot := filepath.Join(parent, "external")
	createDirectoryRow(t, offlineRoot)
	// 另一个在线根里有同名同大小的文件：离线时绝不能拿它去重定位。
	onlineRoot := t.TempDir()
	createDirectoryRow(t, onlineRoot)
	createOldVideoFile(t, filepath.Join(onlineRoot, "movie.mp4"))
	video := models.Video{Name: "movie.mp4", Path: filepath.Join(offlineRoot, "movie.mp4"), Directory: offlineRoot, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}

	relocated := make(chan VideoRelocatedEvent, 1)
	SetVideoRelocatedNotifier(func(event VideoRelocatedEvent) { relocated <- event })
	t.Cleanup(func() { SetVideoRelocatedNotifier(nil) })

	result, err := (&VideoService{}).PlayVideo(video.ID)
	if err != nil || result == nil || result.DispatchSucceeded {
		t.Fatalf("离线播放应返回领域失败: %+v err=%v", result, err)
	}
	if result.ReconcileResult == nil || !result.ReconcileResult.DidMarkStale || result.ReconcileResult.NeedsReload || result.ReconcileResult.UpdatedVideo == nil {
		t.Fatalf("应立即返回就地更新所需的结果: %+v", result.ReconcileResult)
	}
	if got := p011ReloadVideo(t, video.ID); !got.IsStale || got.StaleReason != models.StaleReasonOfflineRoot {
		t.Fatalf("根离线应标 offline_root: %+v", got)
	}
	if updated := result.ReconcileResult.UpdatedVideo; updated.StaleReason != models.StaleReasonOfflineRoot {
		t.Fatalf("返回的视频应带失效原因: %+v", updated)
	}
	if result.Reason != "offline_root" || result.ReconcileResult.Reason != "offline_root" || !strings.Contains(result.UserMessage, "磁盘未连接") {
		t.Fatalf("播放结果应带 reason=offline_root 与磁盘未连接文案: reason=%q msg=%q", result.Reason, result.UserMessage)
	}
	select {
	case event := <-relocated:
		t.Fatalf("离线根不应做重定位: %+v", event)
	case <-time.After(150 * time.Millisecond):
	}
	if got := p011ReloadVideo(t, video.ID); got.Path != video.Path {
		t.Fatalf("路径不应被改写: %+v", got)
	}
	if reason := playbackFailureReason("file_missing", true); reason != playbackReasonOfflineRoot {
		t.Fatalf("离线原因映射错误: %s", reason)
	}
}

func TestPlaybackMissingFileOnOnlineRootMarksThenRelocatesInBackgroundPLAY12(t *testing.T) {
	setupVideoServiceTestDB(t)
	p011StopPlaybackRelocationOnCleanup(t)
	root := t.TempDir()
	createDirectoryRow(t, root)
	movedPath := filepath.Join(root, "moved", "movie.mp4")
	createOldVideoFile(t, movedPath)
	video := models.Video{Name: "movie.mp4", Path: filepath.Join(root, "old", "movie.mp4"), Directory: filepath.Join(root, "old"), Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}

	relocated := make(chan VideoRelocatedEvent, 1)
	SetVideoRelocatedNotifier(func(event VideoRelocatedEvent) { relocated <- event })
	t.Cleanup(func() { SetVideoRelocatedNotifier(nil) })

	result, err := (&VideoService{}).PlayVideo(video.ID)
	if err != nil || result == nil || result.DispatchSucceeded || result.ReconcileResult == nil || !result.ReconcileResult.DidMarkStale {
		t.Fatalf("文件缺失应立即返回并标失效: %+v err=%v", result, err)
	}
	if result.Reason != "missing_file" {
		t.Fatalf("在线根缺失文件应报 reason=missing_file: %q", result.Reason)
	}
	if result.ReconcileResult.DidRelocate {
		t.Fatalf("重定位不应在返回之前同步完成: %+v", result.ReconcileResult)
	}
	select {
	case event := <-relocated:
		if event.VideoID != video.ID || event.NewPath != movedPath {
			t.Fatalf("事件载荷不对: %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("后台重定位成功后应发 video-relocated 事件")
	}
	if got := p011ReloadVideo(t, video.ID); got.Path != movedPath || got.IsStale || got.StaleReason != "" {
		t.Fatalf("重定位后应改路径并清失效原因: %+v", got)
	}
}

func TestPlaybackMissingFileOnOnlineRootWithoutCandidateStaysMissingPLAY12(t *testing.T) {
	setupVideoServiceTestDB(t)
	p011StopPlaybackRelocationOnCleanup(t)
	root := t.TempDir()
	createDirectoryRow(t, root)
	video := models.Video{Name: "gone.mp4", Path: filepath.Join(root, "gone.mp4"), Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&VideoService{}).PlayVideo(video.ID); err != nil {
		t.Fatal(err)
	}
	// 等后台重定位确定性地结束（没有候选，它什么都不改），再断言记录仍是 missing_file。
	StopPlaybackRelocation()
	if got := p011ReloadVideo(t, video.ID); !got.IsStale || got.StaleReason != models.StaleReasonMissingFile {
		t.Fatalf("在线根下文件缺失应标 missing_file: %+v", got)
	}
	if reason := playbackFailureReason("file_missing", false); reason != playbackReasonMissingFile {
		t.Fatalf("缺失原因映射错误: %s", reason)
	}
	if reason := playbackFailureReason("dispatch_failed", false); reason != playbackReasonError {
		t.Fatalf("其他失败应映射为 error: %s", reason)
	}
}

// ---------- 其余：路径改写工具 ----------

func TestRewriteLibraryPathPrefixIgnoresSiblingPrefixLIB06(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	oldPrefix := filepath.Join(root, "Movies")
	sibling := filepath.Join(root, "Movies2")
	inside := models.Video{Name: "a.mp4", Path: filepath.Join(oldPrefix, "a.mp4"), Directory: oldPrefix, Size: 1}
	near := models.Video{Name: "b.mp4", Path: filepath.Join(sibling, "b.mp4"), Directory: sibling, Size: 1}
	for _, v := range []*models.Video{&inside, &near} {
		if err := database.DB.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	softDeleted := models.Video{Name: "c.mp4", Path: filepath.Join(oldPrefix, "c.mp4"), Directory: oldPrefix, Size: 1}
	if err := database.DB.Create(&softDeleted).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&softDeleted).Error; err != nil {
		t.Fatal(err)
	}

	newPrefix := filepath.Join(root, "Films")
	err := database.Transaction(func(tx *gorm.DB) error {
		counts, err := rewriteLibraryPathPrefixTx(tx, oldPrefix, newPrefix)
		if err != nil {
			return err
		}
		if counts.Videos != 2 {
			t.Errorf("应改写活跃与软删两条，且不碰同前缀的兄弟目录: %+v", counts)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := p011ReloadVideo(t, near.ID); got.Path != near.Path {
		t.Fatalf("兄弟目录不应被改写: %+v", got)
	}
	if got := p011ReloadVideo(t, softDeleted.ID); got.Path != filepath.Join(newPrefix, "c.mp4") {
		t.Fatalf("软删记录也应改写: %+v", got)
	}
}
