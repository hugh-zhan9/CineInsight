package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

func createStaleTestVideo(t *testing.T, path string) models.Video {
	t.Helper()
	video := models.Video{
		Name:      filepath.Base(path),
		Path:      path,
		Directory: filepath.Dir(path),
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	return video
}

func reloadVideo(t *testing.T, id uint) models.Video {
	t.Helper()
	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	return video
}

func TestMarkVideosStaleUnderRemovedRootMarksOnlyThatRoot(t *testing.T) {
	setupVideoServiceTestDB(t)
	removed := t.TempDir()
	kept := t.TempDir()

	inRemoved := createStaleTestVideo(t, filepath.Join(removed, "a.mp4"))
	inKept := createStaleTestVideo(t, filepath.Join(kept, "b.mp4"))

	svc := &VideoService{}
	marked, err := svc.MarkVideosStaleUnderRemovedRoot(removed, []string{kept})
	if err != nil {
		t.Fatalf("标记失效失败: %v", err)
	}
	if marked != 1 {
		t.Fatalf("应当只标记 1 条，实际 %d", marked)
	}
	if !reloadVideo(t, inRemoved.ID).IsStale {
		t.Fatal("被移除目录下的视频应当标为失效")
	}
	if reloadVideo(t, inKept.ID).IsStale {
		t.Fatal("其他目录下的视频不该被牵连")
	}
}

// 嵌套目录是这条逻辑唯一的要害：/media 与 /media/movies 同时配着、删掉 /media 时，
// /media/movies 下的视频仍归另一个根管，不能跟着失效。
func TestMarkVideosStaleUnderRemovedRootKeepsNestedRootVideos(t *testing.T) {
	setupVideoServiceTestDB(t)
	parent := t.TempDir()
	nested := filepath.Join(parent, "movies")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("建嵌套目录失败: %v", err)
	}

	directlyUnderParent := createStaleTestVideo(t, filepath.Join(parent, "top.mp4"))
	underNested := createStaleTestVideo(t, filepath.Join(nested, "inner.mp4"))

	svc := &VideoService{}
	// 删掉父目录，子目录仍然配置着
	marked, err := svc.MarkVideosStaleUnderRemovedRoot(parent, []string{nested})
	if err != nil {
		t.Fatalf("标记失效失败: %v", err)
	}
	if marked != 1 {
		t.Fatalf("只有父目录直属的那条该被标记，实际标了 %d 条", marked)
	}
	if !reloadVideo(t, directlyUnderParent.ID).IsStale {
		t.Fatal("父目录直属的视频应当失效")
	}
	if reloadVideo(t, underNested.ID).IsStale {
		t.Fatal("仍归子目录根管的视频不该失效")
	}
}

func TestMarkVideosStaleUnderRemovedRootLeavesFilesAndTrashAlone(t *testing.T) {
	setupVideoServiceTestDB(t)
	removed := t.TempDir()
	path := filepath.Join(removed, "keep-me.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatalf("造文件失败: %v", err)
	}
	createStaleTestVideo(t, path)

	svc := &VideoService{}
	if _, err := svc.MarkVideosStaleUnderRemovedRoot(removed, nil); err != nil {
		t.Fatalf("标记失效失败: %v", err)
	}

	// 磁盘文件一个字节都不该动
	if content, err := os.ReadFile(path); err != nil || string(content) != "video" {
		t.Fatalf("磁盘文件被动过了: err=%v content=%q", err, string(content))
	}
	// 也不该产生任何回收站条目——删一个目录往回收站灌上千条正是要避开的事
	var trashCount int64
	if err := database.DB.Model(&models.VideoTrashEntry{}).Count(&trashCount).Error; err != nil {
		t.Fatalf("统计回收站条目失败: %v", err)
	}
	if trashCount != 0 {
		t.Fatalf("标记失效不该产生回收站条目，实际 %d 条", trashCount)
	}
	// 记录仍在库里，没有被软删
	var alive int64
	if err := database.DB.Model(&models.Video{}).Count(&alive).Error; err != nil {
		t.Fatalf("统计视频失败: %v", err)
	}
	if alive != 1 {
		t.Fatalf("记录应当留在库里，实际 %d 条", alive)
	}
}

func TestMarkVideosStaleUnderRemovedRootIsIdempotent(t *testing.T) {
	setupVideoServiceTestDB(t)
	removed := t.TempDir()
	createStaleTestVideo(t, filepath.Join(removed, "a.mp4"))

	svc := &VideoService{}
	first, err := svc.MarkVideosStaleUnderRemovedRoot(removed, nil)
	if err != nil || first != 1 {
		t.Fatalf("首次标记应当标到 1 条: n=%d err=%v", first, err)
	}
	second, err := svc.MarkVideosStaleUnderRemovedRoot(removed, nil)
	if err != nil {
		t.Fatalf("重复标记失败: %v", err)
	}
	if second != 0 {
		t.Fatalf("已经失效的记录不该被重复计数，实际 %d", second)
	}
}

func TestMarkVideosStaleUnderRemovedRootRejectsEmptyRoot(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	if _, err := svc.MarkVideosStaleUnderRemovedRoot("  ", nil); err == nil {
		t.Fatal("空目录应当报错而不是把整库标失效")
	}
}

// 图片侧：删掉图片目录后记录按失踪对账软删（留在库里、磁盘文件不动），
// 把同一路径加回来后由既有的 restoreStaleImage 复活，而且复活的是同一行——
// 标签、评分这些挂在行上的东西因此一个不丢（D-S04）。
func TestMarkImagesStaleUnderRemovedRootHidesAndRestores(t *testing.T) {
	setupImageServiceTestDB(t)
	root := t.TempDir()
	imagePath := filepath.Join(root, "photo.jpg")
	if err := os.WriteFile(imagePath, []byte("jpeg-bytes"), 0o644); err != nil {
		t.Fatalf("造图片文件失败: %v", err)
	}

	svc := &ImageService{}
	dir := imageTestMustAddDirectory(t, svc, root)
	imageTestMustSync(t, svc)

	var original models.Image
	if err := database.DB.Where("path = ?", imagePath).First(&original).Error; err != nil {
		t.Fatalf("首轮扫描应当收录这张图: %v", err)
	}

	marked, err := svc.MarkImagesStaleUnderRemovedRoot(root, nil)
	if err != nil {
		t.Fatalf("标记图片失效失败: %v", err)
	}
	if marked != 1 {
		t.Fatalf("应当处理 1 张，实际 %d", marked)
	}

	// 从图库消失（查询过滤），但记录还在库里
	var visible int64
	if err := applyImageFilter(database.DB.Model(&models.Image{}), ImageFilter{}).Where("id = ?", original.ID).Count(&visible).Error; err != nil {
		t.Fatalf("统计图片失败: %v", err)
	}
	if visible != 0 {
		t.Fatal("失效的图片不该还出现在图库里")
	}
	var kept models.Image
	if err := database.DB.Unscoped().First(&kept, original.ID).Error; err != nil {
		t.Fatalf("记录应当留在库里: %v", err)
	}
	if !kept.IsStale {
		t.Fatal("隐藏时必须打上 is_stale，否则后续扫描不会自动恢复它")
	}
	// 磁盘文件不动
	if content, err := os.ReadFile(imagePath); err != nil || string(content) != "jpeg-bytes" {
		t.Fatalf("磁盘文件被动过了: err=%v", err)
	}

	// 把同一个路径加回来
	if err := svc.DeleteImageDirectory(dir.ID); err != nil {
		t.Fatalf("删除图片目录失败: %v", err)
	}
	imageTestMustAddDirectory(t, svc, root)
	result := imageTestMustSync(t, svc)
	if result.Restored != 1 {
		t.Fatalf("加回目录后应当恢复 1 张，实际 restored=%d added=%d", result.Restored, result.Added)
	}

	var restored models.Image
	if err := database.DB.First(&restored, original.ID).Error; err != nil {
		t.Fatalf("恢复后应当能正常查到: %v", err)
	}
	if restored.IsStale {
		t.Fatal("恢复后应当清掉 is_stale")
	}
	if restored.ID != original.ID {
		t.Fatal("必须复活原来那一行，否则挂在行上的标签评分全丢了")
	}
}

func TestMarkImagesStaleUnderRemovedRootKeepsNestedRootImages(t *testing.T) {
	setupImageServiceTestDB(t)
	parent := t.TempDir()
	nested := filepath.Join(parent, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("建嵌套目录失败: %v", err)
	}
	topPath := filepath.Join(parent, "top.jpg")
	innerPath := filepath.Join(nested, "inner.jpg")
	for _, path := range []string{topPath, innerPath} {
		if err := os.WriteFile(path, []byte("jpeg-bytes"), 0o644); err != nil {
			t.Fatalf("造图片文件失败: %v", err)
		}
	}

	svc := &ImageService{}
	imageTestMustAddDirectory(t, svc, parent)
	imageTestMustAddDirectory(t, svc, nested)
	imageTestMustSync(t, svc)

	marked, err := svc.MarkImagesStaleUnderRemovedRoot(parent, []string{nested})
	if err != nil {
		t.Fatalf("标记图片失效失败: %v", err)
	}
	if marked != 1 {
		t.Fatalf("只有父目录直属的那张该被处理，实际 %d", marked)
	}
	var innerCount int64
	if err := database.DB.Model(&models.Image{}).Where("path = ?", innerPath).Count(&innerCount).Error; err != nil {
		t.Fatalf("统计图片失败: %v", err)
	}
	if innerCount != 1 {
		t.Fatal("仍归子目录根管的图片不该被隐藏")
	}
}

// 历史遗留：早先删掉扫描目录时只删了配置行、没动记录，那批记录卡在
// "扫描看不见、列表看得见"的夹缝里。对账应当把它们收拾掉，而不是只处理
// "删除目录那一刻"——后者修不了已经掉进夹缝的记录。
func TestSyncScanDirectoriesMarksOrphanedVideosStale(t *testing.T) {
	setupVideoServiceTestDB(t)
	configured := t.TempDir()
	orphanRoot := t.TempDir()

	inConfigured := createStaleTestVideo(t, filepath.Join(configured, "keep.mp4"))
	if err := os.WriteFile(inConfigured.Path, []byte("video"), 0o644); err != nil {
		t.Fatalf("造文件失败: %v", err)
	}
	// 扫描会跳过 5 分钟内刚改动过的文件（recentActiveFileThreshold，防止收录正在写入
	// 的半成品）。刚写完就扫的话它会被当成"文件不见了"而软删，测的就不是这里要测的东西了。
	past := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(inConfigured.Path, past, past); err != nil {
		t.Fatalf("回拨文件时间失败: %v", err)
	}
	orphan := createStaleTestVideo(t, filepath.Join(orphanRoot, "orphan.mp4"))

	svc := &VideoService{}
	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: configured}})

	if !reloadVideo(t, orphan.ID).IsStale {
		t.Fatal("不属于任何已配置目录的记录应当被标失效")
	}
	if reloadVideo(t, inConfigured.ID).IsStale {
		t.Fatal("已配置目录下的记录不该被标失效")
	}
	if result.Stale < 1 {
		t.Fatalf("对账结果应当记上这次失效，实际 stale=%d", result.Stale)
	}
}

// 盘没插：目录仍然配置着、只是这一轮扫不了。底下的记录绝不能被当成孤儿。
func TestSyncScanDirectoriesKeepsVideosUnderUnreachableRoot(t *testing.T) {
	setupVideoServiceTestDB(t)
	missingRoot := filepath.Join(t.TempDir(), "not-mounted")
	video := createStaleTestVideo(t, filepath.Join(missingRoot, "on-external-disk.mp4"))

	svc := &VideoService{}
	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: missingRoot}})

	if !reloadVideo(t, video.ID).IsStale {
		t.Fatal("离线目录必须隐藏，但保留未删除记录")
	}
	if len(result.Errors) == 0 {
		t.Fatal("扫不到的根应当如实记一条错误")
	}
}

func TestSyncImageDirectoriesHidesOrphanedImages(t *testing.T) {
	setupImageServiceTestDB(t)
	configured := t.TempDir()
	orphanRoot := t.TempDir()
	keep := filepath.Join(configured, "keep.jpg")
	if err := os.WriteFile(keep, []byte("jpeg"), 0o644); err != nil {
		t.Fatalf("造图片失败: %v", err)
	}

	svc := &ImageService{}
	imageTestMustAddDirectory(t, svc, configured)
	imageTestMustSync(t, svc)

	// 造一条历史遗留：记录在库里，但它所在的目录早就不在配置里了
	orphan := models.Image{
		Name:      "orphan.jpg",
		Path:      filepath.Join(orphanRoot, "orphan.jpg"),
		Directory: orphanRoot,
	}
	if err := database.DB.Create(&orphan).Error; err != nil {
		t.Fatalf("造孤儿记录失败: %v", err)
	}

	imageTestMustSync(t, svc)

	var visible int64
	if err := applyImageFilter(database.DB.Model(&models.Image{}), ImageFilter{}).Where("id = ?", orphan.ID).Count(&visible).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if visible != 0 {
		t.Fatal("不属于任何已配置图片目录的记录应当从图库隐藏")
	}
	var kept models.Image
	if err := database.DB.Unscoped().First(&kept, orphan.ID).Error; err != nil {
		t.Fatalf("记录应当留在库里: %v", err)
	}
	if !kept.IsStale {
		t.Fatal("隐藏时必须打上 is_stale，否则目录加回来不会自动恢复")
	}
	// 配置目录下的那张不受影响
	var stillVisible int64
	database.DB.Model(&models.Image{}).Where("path = ?", keep).Count(&stillVisible)
	if stillVisible != 1 {
		t.Fatal("已配置目录下的图片不该被隐藏")
	}
}

func TestSyncImageDirectoriesKeepsImagesUnderUnreachableRoot(t *testing.T) {
	setupImageServiceTestDB(t)
	missingRoot := filepath.Join(t.TempDir(), "not-mounted")
	svc := &ImageService{}
	imageTestMustAddDirectory(t, svc, missingRoot)

	image := models.Image{Name: "p.jpg", Path: filepath.Join(missingRoot, "p.jpg"), Directory: missingRoot}
	if err := database.DB.Create(&image).Error; err != nil {
		t.Fatalf("造记录失败: %v", err)
	}

	imageTestMustSync(t, svc)

	var visible int64
	database.DB.Model(&models.Image{}).Where("id = ?", image.ID).Count(&visible)
	if visible != 1 {
		t.Fatal("目录仍配置着、只是扫不到时，底下的图片不能被隐藏")
	}
}

// 回归用例：下载目录里别人的坏文件（下了一半的 mkv、损坏的分卷）扫描时会报错，
// 但那不是这次下载的失败。早先把任何扫描错误都算成入库失败，一次成功的下载
// 会因为隔壁一个坏文件被判成"文件已保存，但没有入库"。
func TestBrowserDownloadImportIgnoresUnrelatedScanErrors(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	if err := database.DB.Create(&models.ScanDirectory{Path: root, Alias: "downloads"}).Error; err != nil {
		t.Fatalf("创建扫描目录失败: %v", err)
	}

	// 这次下载的产物：文件在盘上，记录已在库里
	ours := filepath.Join(root, "ours.mp4")
	if err := os.WriteFile(ours, []byte("video"), 0o644); err != nil {
		t.Fatalf("造文件失败: %v", err)
	}
	past := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(ours, past, past); err != nil {
		t.Fatalf("回拨时间失败: %v", err)
	}
	if err := database.DB.Create(&models.Video{Name: "ours.mp4", Path: ours, Directory: root}).Error; err != nil {
		t.Fatalf("创建视频记录失败: %v", err)
	}

	// 隔壁一个坏文件：扫描时 ffprobe 会失败
	broken := filepath.Join(root, "broken.mkv")
	if err := os.WriteFile(broken, []byte("not a real matroska"), 0o644); err != nil {
		t.Fatalf("造坏文件失败: %v", err)
	}
	if err := os.Chtimes(broken, past, past); err != nil {
		t.Fatalf("回拨时间失败: %v", err)
	}

	importer := BrowserDownloadImporterFromScan(&VideoService{}, func() ([]models.ScanDirectory, error) {
		var dirs []models.ScanDirectory
		err := database.DB.Find(&dirs).Error
		return dirs, err
	})
	if _, err := importer(root, ours); err != nil {
		t.Fatalf("这次下载的文件已经在库里，不该因为隔壁坏文件报失败：%v", err)
	}

	// 反面：产物确实没进库时，仍然要如实报失败
	missing := filepath.Join(root, "never-imported.mp4")
	if _, err := importer(root, missing); err == nil {
		t.Fatal("产物没进库时应当报失败")
	}
}
