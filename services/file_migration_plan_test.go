package services

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func consolidationTestVideo(t *testing.T, path, contents string) models.Video {
	t.Helper()
	mustCreateFile(t, path)
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path), Size: int64(len(contents)), Duration: 60, Width: 1920, Height: 1080, Resolution: "1920x1080"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	return video
}

func consolidationTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			result[path] = fmt.Sprintf("dir:%d:%d", info.Mode(), info.ModTime().UnixNano())
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[path] = fmt.Sprintf("%d:%d:%x", info.Mode(), info.ModTime().UnixNano(), sha256.Sum256(data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestFileMigrationPlanAttachmentsAndReadOnly(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	video := consolidationTestVideo(t, filepath.Join(source, "film.mp4"), "video")
	for _, name := range []string{"film.srt", "film.zh-CN.ass", "film.eng.forced.vtt", "film.ssa", "film.nfo", "movie.nfo", "poster.jpg", "film.part2.srt"} {
		mustCreateFile(t, filepath.Join(source, name))
	}
	before := consolidationTree(t, root)
	plan, err := (&VideoService{}).planConsolidationFiles(context.Background(), []models.Video{video}, target)
	if err != nil || len(plan.Errors) != 0 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if len(plan.Items) != 1 || len(plan.Items[0].Files) != 6 {
		t.Fatalf("同名+语言字幕+同名NFO应纳入: %+v", plan.Items)
	}
	if plan.MoveBytes != 10 || plan.CopyBytes != 0 || plan.CrossVolumeBytes != 0 {
		t.Fatalf("同盘移动字节错误: %+v", plan)
	}
	warnings := strings.Join(plan.Warnings, "\n")
	for _, name := range []string{"movie.nfo", "poster.jpg", "film.part2.srt"} {
		if !strings.Contains(warnings, name) {
			t.Fatalf("未处理附件未报告: %s", name)
		}
	}
	if !reflect.DeepEqual(before, consolidationTree(t, root)) {
		t.Fatal("预览改变了磁盘文件/目录")
	}
	var loaded models.Video
	if err := database.DB.First(&loaded, video.ID).Error; err != nil || loaded.Path != video.Path {
		t.Fatal("预览改变了库路径", err)
	}
}

func TestFileMigrationPlanSharedSubtitlesKeepSource(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	video := consolidationTestVideo(t, filepath.Join(source, "film.mp4"), "video")
	mustCreateFile(t, filepath.Join(source, "film.mkv")) // 未入库视频也使用这些同名附件。
	mustCreateFile(t, filepath.Join(source, "film.en.srt"))
	mustCreateFile(t, filepath.Join(source, "film.nfo"))
	plan, err := (&VideoService{}).planConsolidationFiles(context.Background(), []models.Video{video}, target)
	if err != nil || len(plan.Errors) != 0 {
		t.Fatalf("%+v %v", plan, err)
	}
	if len(plan.Items[0].Files) != 2 || !plan.Items[0].Files[1].CopyOnly || plan.CopyBytes != 1 {
		t.Fatalf("共享字幕必须复制留源，共用NFO不动: %+v", plan)
	}
	if !strings.Contains(strings.Join(plan.Warnings, "\n"), "共用 NFO") {
		t.Fatal("未报告共用NFO")
	}
}

func TestFileMigrationPlanConflictRenamesWholeFamilyAndIsStable(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	target := filepath.Join(root, "target")
	mustCreateFile(t, filepath.Join(target, "film.en.srt")) // 仅字幕占位也要整体换名。
	first := consolidationTestVideo(t, filepath.Join(root, "a", "film.mp4"), "one")
	second := consolidationTestVideo(t, filepath.Join(root, "b", "film.mp4"), "two")
	mustCreateFile(t, filepath.Join(root, "a", "film.en.srt"))
	mustCreateFile(t, filepath.Join(root, "b", "film.en.srt"))
	plan, err := (&VideoService{}).planConsolidationFiles(context.Background(), []models.Video{second, first, first}, target)
	if err != nil || len(plan.Errors) != 0 {
		t.Fatalf("%+v %v", plan, err)
	}
	if len(plan.Items) != 2 {
		t.Fatal("相同保留项必须去重")
	}
	if filepath.Base(plan.Items[0].DestinationPath) != "film（保留版）.mp4" {
		t.Fatalf("附件冲突未整体避让: %+v", plan.Items)
	}
	if !strings.HasPrefix(filepath.Base(plan.Items[1].DestinationPath), fmt.Sprintf("film（保留版-%d-", second.ID)) {
		t.Fatalf("同批冲突未稳定区分: %+v", plan.Items)
	}
	for _, item := range plan.Items {
		stem := strings.TrimSuffix(item.DestinationPath, ".mp4")
		if item.Files[1].Destination != stem+".en.srt" {
			t.Fatalf("字幕未随视频改名: %+v", item)
		}
	}
	again, err := (&VideoService{}).planConsolidationFiles(context.Background(), []models.Video{first, second}, target)
	if err != nil || !reflect.DeepEqual(plan.Items, again.Items) {
		t.Fatalf("重预览改变了名称: %+v %v", again, err)
	}
}

func TestFileMigrationPlanDatabaseOccupancyAndStay(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	video := consolidationTestVideo(t, filepath.Join(root, "source", "film.mp4"), "video")
	missing := models.Video{Path: filepath.Join(target, "film.mp4"), Directory: target}
	if err := database.DB.Create(&missing).Error; err != nil {
		t.Fatal(err)
	}
	plan, err := (&VideoService{}).planConsolidationFiles(context.Background(), []models.Video{video}, target)
	if err != nil || filepath.Base(plan.Items[0].DestinationPath) != "film（保留版）.mp4" {
		t.Fatalf("库路径占位未处理: %+v %v", plan, err)
	}
	stay, err := (&VideoService{}).planConsolidationFiles(context.Background(), []models.Video{video}, filepath.Dir(video.Path))
	if err != nil || !stay.Items[0].Stay || stay.MoveBytes != 0 || stay.Items[0].DestinationPath != stay.Items[0].Files[0].Source.RealPath {
		t.Fatalf("原地项不应重命名/复制: %+v %v", stay, err)
	}
}

func TestFileMigrationPlanInvalidFilesAndTargetIdentity(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := consolidationTestVideo(t, filepath.Join(root, "source", "film.mp4"), "video")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	before, err := snapshotMigrationDirectory(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, target+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	after, err := snapshotMigrationDirectory(target)
	if err != nil || before.Identity == after.Identity {
		t.Fatal("父目录替换未改变身份", err)
	}
	if err := os.Chmod(target, 0555); err != nil {
		t.Fatal(err)
	}
	plan, err := (&VideoService{}).planConsolidationFiles(context.Background(), []models.Video{video}, target)
	if err != nil || len(plan.Errors) == 0 {
		t.Fatalf("只读目标不能确认: %+v %v", plan, err)
	}
	if err := os.Chmod(target, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "source", "link.mp4")
	if err := os.Symlink(video.Path, link); err != nil {
		t.Fatal(err)
	}
	video.Path = link
	plan, err = (&VideoService{}).planConsolidationFiles(context.Background(), []models.Video{video}, target)
	if err != nil || len(plan.Errors) == 0 {
		t.Fatalf("文件符号链接不能冒充源: %+v %v", plan, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&VideoService{}).planConsolidationFiles(ctx, nil, target); err != context.Canceled {
		t.Fatalf("取消未传播: %v", err)
	}
	var total int64 = math.MaxInt64
	if err := addMigrationBytes(&total, 1); err == nil || total != math.MaxInt64 {
		t.Fatal("字节溢出未阻止")
	}
}

func TestFileMigrationPlanMissingAliasTargetWithoutName(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	target, alias := filepath.Join(root, "target"), filepath.Join(root, "alias")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	video := consolidationTestVideo(t, filepath.Join(root, "source", "film.mp4"), "movie")
	placeholder := models.Video{Path: filepath.Join(alias, "film.mp4"), Directory: alias}
	if err := database.DB.Create(&placeholder).Error; err != nil {
		t.Fatal(err)
	}
	plan, err := (&VideoService{}).planConsolidationFiles(context.Background(), []models.Video{video}, target)
	if err != nil || len(plan.Errors) != 0 {
		t.Fatalf("%+v %v", plan, err)
	}
	if filepath.Base(plan.Items[0].DestinationPath) != "film（保留版）.mp4" {
		t.Fatalf("空 Name 的活跃别名记录仍占用目标: %+v", plan.Items)
	}
}

func TestFileMigrationPlanUnicodeBatchNamesAndHardlinks(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	a := consolidationTestVideo(t, filepath.Join(root, "a", "caf\u00e9.mp4"), "a")
	b := consolidationTestVideo(t, filepath.Join(root, "b", "cafe\u0301.mp4"), "b")
	plan, err := (&VideoService{}).planConsolidationFiles(context.Background(), []models.Video{a, b}, target)
	if err != nil || len(plan.Errors) != 0 {
		t.Fatalf("%+v %v", plan, err)
	}
	if subtitleFileLockKey(plan.Items[0].DestinationPath) == subtitleFileLockKey(plan.Items[1].DestinationPath) {
		t.Fatal("同批 NFC/NFD 等价文件名未避让")
	}
	hardlink := filepath.Join(root, "a", "different-name.mp4")
	if err := os.Link(a.Path, hardlink); err != nil {
		t.Fatal(err)
	}
	other := models.Video{Path: hardlink, Directory: filepath.Dir(hardlink)}
	if err := database.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	plan, err = (&VideoService{}).planConsolidationFiles(context.Background(), []models.Video{a}, target)
	if err != nil || len(plan.Errors) != 0 || len(plan.Items) != 1 {
		t.Fatalf("不同路径的硬链接不会因本次移动失效，不应当成别名冲突: %+v %v", plan, err)
	}
}

func TestFileMigrationPlanLoadsActivePathsOnce(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	var videos []models.Video
	for i := 0; i < 80; i++ {
		videos = append(videos, consolidationTestVideo(t, filepath.Join(root, "source", fmt.Sprintf("%03d.mp4", i)), "video"))
	}
	queries := 0
	const callback = "test:consolidation-active-path-queries"
	if err := database.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "videos" {
			queries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Query().Remove(callback)
	plan, err := (&VideoService{}).planConsolidationFiles(context.Background(), videos, target)
	if err != nil || len(plan.Errors) != 0 || len(plan.Items) != len(videos) {
		t.Fatalf("%+v %v", plan, err)
	}
	if queries != 1 {
		t.Fatalf("应批量读取一次活跃路径，不能每个候选扫 videos: queries=%d", queries)
	}
}

func TestFileMigrationPlanDanglingFileLinksReserveTheirTargets(t *testing.T) {
	for _, mode := range []string{"absolute", "relative", "file-chain", "directory-chain", "directory-dotdot"} {
		t.Run(mode, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			root := t.TempDir()
			target := filepath.Join(root, "target")
			if err := os.Mkdir(target, 0755); err != nil {
				t.Fatal(err)
			}
			video := consolidationTestVideo(t, filepath.Join(root, "source", "film.mp4"), "movie")
			linkPath := filepath.Join(root, "file-link.mp4")
			linkTarget := filepath.Join(target, "film.mp4")
			switch mode {
			case "relative":
				linkTarget = filepath.Join("target", "film.mp4")
			case "file-chain":
				if err := os.Symlink(filepath.Join("target", "film.mp4"), filepath.Join(root, "middle.mp4")); err != nil {
					t.Fatal(err)
				}
				linkTarget = "middle.mp4"
			case "directory-chain":
				if err := os.Symlink("target", filepath.Join(root, "dir-alias")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("dir-alias", filepath.Join(root, "dir-alias-two")); err != nil {
					t.Fatal(err)
				}
				linkTarget = filepath.Join("dir-alias-two", "film.mp4")
			case "directory-dotdot":
				if err := os.Mkdir(filepath.Join(target, "child"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join("target", "child"), filepath.Join(root, "dir-alias")); err != nil {
					t.Fatal(err)
				}
				linkTarget = "dir-alias/../film.mp4" // 必须先解析链接，不能先把 .. 清掉。
			}
			if err := os.Symlink(linkTarget, linkPath); err != nil {
				t.Fatal(err)
			}
			alias := models.Video{Path: linkPath, Directory: root}
			if err := database.DB.Create(&alias).Error; err != nil {
				t.Fatal(err)
			}
			plan, err := (&VideoService{}).planConsolidationFiles(context.Background(), []models.Video{video}, target)
			if err != nil || len(plan.Errors) != 0 {
				t.Fatalf("%+v %v", plan, err)
			}
			if filepath.Base(plan.Items[0].DestinationPath) != "film（保留版）.mp4" {
				t.Fatalf("断开的链接仍应为将来指向的目标占位: %+v", plan.Items)
			}
			if _, err := os.Stat(linkPath); !os.IsNotExist(err) {
				t.Fatalf("预览不得让旧链接认领新内容: %v", err)
			}
		})
	}
}

func TestFileMigrationPlanLinkResolutionBoundaries(t *testing.T) {
	setupVideoServiceTestDB(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := newFileMigrationInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	future := filepath.Join(root, "future")
	alias := filepath.Join(root, "future-alias")
	if err := os.Symlink("future", alias); err != nil {
		t.Fatal(err)
	}
	directKey, err := inventory.pathKey(filepath.Join(future, "child", "film.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	aliasKey, err := inventory.pathKey(filepath.Join(alias, "child", "film.mp4"))
	if err != nil || aliasKey != directKey {
		t.Fatalf("缺失目录仍需按链接真实目标占位: %q %q %v", directKey, aliasKey, err)
	}
	if _, err := os.Stat(future); !os.IsNotExist(err) {
		t.Fatal("解析不得创建缺失目录", err)
	}
	resolved, err := resolveMigrationPath(filepath.Join(alias, "child", "film.mp4"))
	if err != nil || resolved != filepath.Join(future, "child", "film.mp4") {
		t.Fatalf("缺失路径不能丢掉剩余分量: %s %v", resolved, err)
	}

	first, second := filepath.Join(root, "cycle-a"), filepath.Join(root, "cycle-b")
	if err := os.Symlink("cycle-b", first); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("cycle-a", second); err != nil {
		t.Fatal(err)
	}
	if _, err := inventory.pathKey(first); err == nil || !strings.Contains(err.Error(), "循环") {
		t.Fatalf("循环必须明确拒绝: %v", err)
	}
	const links = 41
	for i := links - 1; i >= 0; i-- {
		target := fmt.Sprintf("chain-%02d", i+1)
		if i == links-1 {
			target = "missing-final.mp4"
		}
		if err := os.Symlink(target, filepath.Join(root, fmt.Sprintf("chain-%02d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := inventory.pathKey(filepath.Join(root, "chain-00")); err == nil || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("链接深度必须有明确上限: %v", err)
	}
	if _, err := inventory.pathKey(filepath.Join(root, "chain-01")); err != nil {
		t.Fatalf("边界内40层链接应可解析到缺失目标: %v", err)
	}
	regular := filepath.Join(root, "regular-file")
	mustCreateFile(t, regular)
	if _, err := inventory.pathKey(filepath.Join(regular, "film.mp4")); err == nil {
		t.Fatal("父路径不是目录的错误不得当成缺失路径吞掉")
	}
	if os.Geteuid() != 0 {
		private := filepath.Join(root, "unreadable")
		if err := os.Mkdir(private, 0700); err != nil {
			t.Fatal(err)
		}
		mustCreateFile(t, filepath.Join(private, "inside", "film.mp4"))
		if err := os.Chmod(private, 0000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(private, 0700)
		if _, err := inventory.pathKey(filepath.Join(private, "inside", "film.mp4")); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("读取权限错误不得当缺失路径处理: %v", err)
		}
	}
}

func TestFileMigrationPlanRawDatabaseParentTraversal(t *testing.T) {
	for _, scenario := range []string{"shared-source", "missing-target"} {
		t.Run(scenario, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			root := t.TempDir()
			source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
			video := consolidationTestVideo(t, filepath.Join(source, "film.mp4"), "movie")
			if err := os.Mkdir(target, 0755); err != nil {
				t.Fatal(err)
			}
			actualDir := source
			if scenario == "missing-target" {
				actualDir = target
			}
			if err := os.Mkdir(filepath.Join(actualDir, "child"), 0755); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(root, "dir-alias")
			if err := os.Symlink(filepath.Join(actualDir, "child"), alias); err != nil {
				t.Fatal(err)
			}
			// 原始 DB Path 含 ..，不能用 filepath.Join 构造，否则测试自身先清掉语义。
			raw := alias + string(filepath.Separator) + ".." + string(filepath.Separator) + "film.mp4"
			other := models.Video{Path: raw, Directory: root}
			if err := database.DB.Create(&other).Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "shared-source" {
				left, err := os.Stat(raw)
				if err != nil {
					t.Fatal(err)
				}
				right, err := os.Stat(video.Path)
				if err != nil || !os.SameFile(left, right) {
					t.Fatalf("夹具必须确实指向同一来源: %v", err)
				}
			}
			plan, err := (&VideoService{}).planConsolidationFiles(context.Background(), []models.Video{video}, target)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "shared-source" {
				if len(plan.Errors) == 0 || len(plan.Items) != 0 {
					t.Fatalf("原始路径里的 link/.. 仍引用来源，必须阻止移动: %+v", plan)
				}
			} else if len(plan.Errors) != 0 || len(plan.Items) != 1 || filepath.Base(plan.Items[0].DestinationPath) != "film（保留版）.mp4" {
				t.Fatalf("缺失目标的原始 link/.. 路径仍必须占位: %+v", plan)
			}
		})
	}
}

func TestFileMigrationPlanRejectsAmbiguousRawSourceAndDestination(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	actual := consolidationTestVideo(t, filepath.Join(root, "source", "film.mp4"), "AAAA")
	wrong := consolidationTestVideo(t, filepath.Join(root, "film.mp4"), "BBBB")
	info, err := os.Stat(actual.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(wrong.Path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "source", "child"), 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "dir-alias")
	if err := os.Symlink(filepath.Join(root, "source", "child"), alias); err != nil {
		t.Fatal(err)
	}
	rawDirectory := alias + string(filepath.Separator) + ".."
	rawSource := rawDirectory + string(filepath.Separator) + "film.mp4"
	left, err := os.Stat(rawSource)
	if err != nil {
		t.Fatal(err)
	}
	right, err := os.Stat(wrong.Path)
	if err != nil {
		t.Fatal(err)
	}
	if left.Size() != right.Size() || !left.ModTime().Equal(right.ModTime()) || os.SameFile(left, right) {
		t.Fatal("夹具应是同大小同mtime的不同文件")
	}
	if _, err := snapshotMigrationSource(rawSource); err == nil {
		t.Fatal("选中源含原始 link/.. 时必须明确拒绝，不能静默读取另一份等大小文件")
	}
	if _, err := snapshotMigrationDirectory(rawDirectory); err == nil {
		t.Fatal("目标含原始 link/.. 时必须明确拒绝，不能静默选择另一目录")
	}
	valid, err := snapshotMigrationSource(actual.Path)
	if err != nil || valid.Path != actual.Path {
		t.Fatal("正常源仍应保存原始 DB CAS 路径", err)
	}
}
