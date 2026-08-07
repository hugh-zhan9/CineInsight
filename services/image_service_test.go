package services

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupImageServiceTestDB(t *testing.T) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "image_service_test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	database.DB = db
	if err := db.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2.0}).Error; err != nil {
		t.Fatalf("初始化设置失败: %v", err)
	}
}

func imageTestMustAddDirectory(t *testing.T, svc *ImageService, path string) *models.ImageDirectory {
	t.Helper()
	dir, err := svc.AddImageDirectory(path, "")
	if err != nil {
		t.Fatalf("添加图片目录失败: %v", err)
	}
	return dir
}

func imageTestMustSync(t *testing.T, svc *ImageService) *ImageScanResult {
	t.Helper()
	result, err := svc.SyncImageDirectories()
	if err != nil {
		t.Fatalf("图片目录对账失败: %v", err)
	}
	return result
}

func imageTestActiveCount(t *testing.T) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Model(&models.Image{}).Count(&count).Error; err != nil {
		t.Fatalf("统计活跃图片失败: %v", err)
	}
	return count
}

func imageTestWriteFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
}

func TestImageDirectoryCRUD(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()

	dir, err := svc.AddImageDirectory("/photos/a", "相册A")
	if err != nil {
		t.Fatalf("添加目录失败: %v", err)
	}
	if dir.ID == 0 || dir.Path != "/photos/a" || dir.Alias != "相册A" {
		t.Fatalf("新增目录字段不符: %+v", dir)
	}

	if err := svc.UpdateImageDirectory(dir.ID, "/photos/b", "相册B"); err != nil {
		t.Fatalf("更新目录失败: %v", err)
	}
	dirs, err := svc.GetAllImageDirectories()
	if err != nil || len(dirs) != 1 {
		t.Fatalf("列出目录失败: dirs=%d err=%v", len(dirs), err)
	}
	if dirs[0].Path != "/photos/b" || dirs[0].Alias != "相册B" {
		t.Fatalf("更新后目录字段不符: %+v", dirs[0])
	}

	if err := svc.DeleteImageDirectory(dir.ID); err != nil {
		t.Fatalf("删除目录失败: %v", err)
	}
	dirs, err = svc.GetAllImageDirectories()
	if err != nil || len(dirs) != 0 {
		t.Fatalf("软删除后仍返回目录: dirs=%d err=%v", len(dirs), err)
	}
	var unscopedCount int64
	if err := database.DB.Unscoped().Model(&models.ImageDirectory{}).Count(&unscopedCount).Error; err != nil || unscopedCount != 1 {
		t.Fatalf("软删除应保留行: count=%d err=%v", unscopedCount, err)
	}
}

func TestImageSyncBatchIngest(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	mustCreateFile(t, filepath.Join(root, "a.jpg"))
	mustCreateFile(t, filepath.Join(root, "b.PNG"))
	mustCreateFile(t, filepath.Join(root, "sub", "c.heic"))
	mustCreateFile(t, filepath.Join(root, "notes.txt"))
	mustCreateFile(t, filepath.Join(root, "movie.mp4"))
	imageTestMustAddDirectory(t, svc, root)

	result := imageTestMustSync(t, svc)
	if result.Added != 3 || result.Relocated != 0 || result.Removed != 0 || len(result.Errors) != 0 {
		t.Fatalf("批量入库计数不符: %+v", result)
	}

	var image models.Image
	wantPath := filepath.Join(root, "sub", "c.heic")
	if err := database.DB.Where("path = ?", wantPath).First(&image).Error; err != nil {
		t.Fatalf("子目录图片未入库: %v", err)
	}
	if image.Name != "c.heic" || image.Directory != filepath.Join(root, "sub") || image.Size != 1 {
		t.Fatalf("入库字段不符: %+v", image)
	}
	if imageTestActiveCount(t) != 3 {
		t.Fatalf("活跃图片数应为 3, got %d", imageTestActiveCount(t))
	}
}

func TestImageSyncRescanIsIdempotent(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	mustCreateFile(t, filepath.Join(root, "a.jpg"))
	mustCreateFile(t, filepath.Join(root, "b.jpg"))
	imageTestMustAddDirectory(t, svc, root)

	first := imageTestMustSync(t, svc)
	if first.Added != 2 {
		t.Fatalf("首次入库计数不符: %+v", first)
	}
	second := imageTestMustSync(t, svc)
	if second.Added != 0 || second.Relocated != 0 || second.Removed != 0 || second.Skipped != 0 || len(second.Errors) != 0 {
		t.Fatalf("重扫应零变更: %+v", second)
	}
	if imageTestActiveCount(t) != 2 {
		t.Fatalf("重扫后活跃图片数漂移: %d", imageTestActiveCount(t))
	}
}

func TestImageSyncAddsOnlyTheNewFile(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	mustCreateFile(t, filepath.Join(root, "a.jpg"))
	imageTestMustAddDirectory(t, svc, root)
	imageTestMustSync(t, svc)

	mustCreateFile(t, filepath.Join(root, "new.jpg"))
	result := imageTestMustSync(t, svc)
	if result.Added != 1 || result.Relocated != 0 || result.Removed != 0 {
		t.Fatalf("新增单文件应只增一条: %+v", result)
	}
	if imageTestActiveCount(t) != 2 {
		t.Fatalf("活跃图片数应为 2, got %d", imageTestActiveCount(t))
	}
}

func TestImageSyncRelocationPreservesUserState(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	oldPath := filepath.Join(root, "keep.jpg")
	imageTestWriteFile(t, oldPath, 7)
	imageTestMustAddDirectory(t, svc, root)
	imageTestMustSync(t, svc)

	var image models.Image
	if err := database.DB.Where("path = ?", oldPath).First(&image).Error; err != nil {
		t.Fatalf("入库图片缺失: %v", err)
	}
	tag := models.Tag{Name: "旅行"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Model(&image).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("打标失败: %v", err)
	}
	rating := 8.5
	if err := database.DB.Model(&image).Updates(map[string]interface{}{
		"is_favorite":     true,
		"personal_rating": rating,
	}).Error; err != nil {
		t.Fatalf("写入收藏/评分失败: %v", err)
	}

	newPath := filepath.Join(root, "moved", "keep.jpg")
	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		t.Fatalf("创建迁移目录失败: %v", err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("移动文件失败: %v", err)
	}

	result := imageTestMustSync(t, svc)
	if result.Relocated != 1 || result.Added != 0 || result.Removed != 0 {
		t.Fatalf("迁移计数不符: %+v", result)
	}

	var relocated models.Image
	if err := database.DB.Preload("Tags").First(&relocated, image.ID).Error; err != nil {
		t.Fatalf("迁移后记录缺失: %v", err)
	}
	if relocated.Path != newPath || relocated.Directory != filepath.Join(root, "moved") {
		t.Fatalf("迁移后路径不符: %+v", relocated)
	}
	if !relocated.IsFavorite || relocated.PersonalRating == nil || *relocated.PersonalRating != rating {
		t.Fatalf("迁移应保留收藏/评分: %+v", relocated)
	}
	if len(relocated.Tags) != 1 || relocated.Tags[0].Name != "旅行" {
		t.Fatalf("迁移应保留标签: %+v", relocated.Tags)
	}
	if imageTestActiveCount(t) != 1 {
		t.Fatalf("迁移后活跃图片数应为 1, got %d", imageTestActiveCount(t))
	}
}

func TestImageSyncAmbiguousMigrationFallsBackToAddAndRemove(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	pathA := filepath.Join(root, "a", "dup.jpg")
	pathB := filepath.Join(root, "b", "dup.jpg")
	imageTestWriteFile(t, pathA, 5)
	imageTestWriteFile(t, pathB, 5)
	imageTestMustAddDirectory(t, svc, root)
	imageTestMustSync(t, svc)

	var oldIDs []uint
	if err := database.DB.Model(&models.Image{}).Order("id").Pluck("id", &oldIDs).Error; err != nil || len(oldIDs) != 2 {
		t.Fatalf("初始入库不符: ids=%v err=%v", oldIDs, err)
	}

	for _, move := range [][2]string{
		{pathA, filepath.Join(root, "c", "dup.jpg")},
		{pathB, filepath.Join(root, "d", "dup.jpg")},
	} {
		if err := os.MkdirAll(filepath.Dir(move[1]), 0755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
		if err := os.Rename(move[0], move[1]); err != nil {
			t.Fatalf("移动文件失败: %v", err)
		}
	}

	result := imageTestMustSync(t, svc)
	if result.Relocated != 0 {
		t.Fatalf("双向唯一不满足时不得猜测迁移: %+v", result)
	}
	if result.Added != 2 || result.Removed != 2 {
		t.Fatalf("歧义迁移应按新增+失踪处理: %+v", result)
	}
	for _, id := range oldIDs {
		var old models.Image
		if err := database.DB.Unscoped().First(&old, id).Error; err != nil {
			t.Fatalf("旧记录应保留（软删除）: %v", err)
		}
		if !old.DeletedAt.IsValid() {
			t.Fatalf("旧记录应为软删除态: id=%d", id)
		}
	}
	if imageTestActiveCount(t) != 2 {
		t.Fatalf("歧义迁移后活跃图片数应为 2, got %d", imageTestActiveCount(t))
	}
}

func TestImageSyncMissingFileRemovesRecordOnly(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	gonePath := filepath.Join(root, "gone.jpg")
	keptPath := filepath.Join(root, "kept.jpg")
	mustCreateFile(t, gonePath)
	mustCreateFile(t, keptPath)
	imageTestMustAddDirectory(t, svc, root)
	imageTestMustSync(t, svc)

	if err := os.Remove(gonePath); err != nil {
		t.Fatalf("删除文件失败: %v", err)
	}
	result := imageTestMustSync(t, svc)
	if result.Removed != 1 || result.Added != 0 || result.Relocated != 0 {
		t.Fatalf("失踪对账计数不符: %+v", result)
	}

	var gone models.Image
	if err := database.DB.Unscoped().Where("path = ?", gonePath).First(&gone).Error; err != nil {
		t.Fatalf("失踪记录应软删除保留: %v", err)
	}
	if !gone.DeletedAt.IsValid() {
		t.Fatalf("失踪记录应为软删除态: %+v", gone)
	}
	var trashCount int64
	if err := database.DB.Model(&models.ImageTrashEntry{}).Count(&trashCount).Error; err != nil || trashCount != 0 {
		t.Fatalf("失踪对账不得创建回收站条目: count=%d err=%v", trashCount, err)
	}
	if _, err := os.Stat(keptPath); err != nil {
		t.Fatalf("对账不得移动磁盘文件: %v", err)
	}
	if imageTestActiveCount(t) != 1 {
		t.Fatalf("失踪后活跃图片数应为 1, got %d", imageTestActiveCount(t))
	}
}

func TestImageSyncRespectsScanExcludePaths(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	excluded := filepath.Join(root, "private")
	mustCreateFile(t, filepath.Join(root, "public.jpg"))
	mustCreateFile(t, filepath.Join(excluded, "secret.jpg"))
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").
		Update("scan_exclude_paths", excluded).Error; err != nil {
		t.Fatalf("写入排除路径失败: %v", err)
	}
	imageTestMustAddDirectory(t, svc, root)

	result := imageTestMustSync(t, svc)
	if result.Added != 1 {
		t.Fatalf("排除路径应生效: %+v", result)
	}
	var count int64
	if err := database.DB.Model(&models.Image{}).Where("path LIKE ?", excluded+"%").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("排除路径内文件不得入库: count=%d err=%v", count, err)
	}
}

func TestImageSyncSkipsTrashAndHiddenPaths(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	mustCreateFile(t, filepath.Join(root, "visible.jpg"))
	mustCreateFile(t, filepath.Join(root, "trash", "deleted.jpg"))
	mustCreateFile(t, filepath.Join(root, "Trash", "deleted2.jpg"))
	mustCreateFile(t, filepath.Join(root, ".cache", "hidden-dir.jpg"))
	mustCreateFile(t, filepath.Join(root, ".hidden.jpg"))
	imageTestMustAddDirectory(t, svc, root)

	result := imageTestMustSync(t, svc)
	if result.Added != 1 {
		t.Fatalf("trash/隐藏路径应被过滤: %+v", result)
	}
	var images []models.Image
	if err := database.DB.Find(&images).Error; err != nil || len(images) != 1 || images[0].Name != "visible.jpg" {
		t.Fatalf("入库结果不符: images=%+v err=%v", images, err)
	}
}

func TestImageSyncExtensionFallbackAndOverride(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	mustCreateFile(t, filepath.Join(root, "a.jpg"))
	mustCreateFile(t, filepath.Join(root, "b.png"))
	mustCreateFile(t, filepath.Join(root, "c.nef"))
	imageTestMustAddDirectory(t, svc, root)

	// 空值回退默认清单：jpg/png/nef 均命中。
	result := imageTestMustSync(t, svc)
	if result.Added != 3 {
		t.Fatalf("默认扩展名清单应命中 3 个文件: %+v", result)
	}

	// 显式收窄为 .png：既有 jpg/nef 记录按失踪清理，仅 png 保留。
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").
		Update("image_extensions", ".png").Error; err != nil {
		t.Fatalf("更新扩展名设置失败: %v", err)
	}
	result = imageTestMustSync(t, svc)
	if result.Removed != 2 || result.Added != 0 {
		t.Fatalf("收窄扩展名后对账不符: %+v", result)
	}
	var images []models.Image
	if err := database.DB.Find(&images).Error; err != nil || len(images) != 1 || images[0].Name != "b.png" {
		t.Fatalf("收窄后仅应保留 png: images=%+v err=%v", images, err)
	}
}

func TestImageSyncUnavailableDirectoryDoesNotAbortOthers(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	goodRoot := t.TempDir()
	mustCreateFile(t, filepath.Join(goodRoot, "ok.jpg"))
	missingRoot := filepath.Join(t.TempDir(), "does-not-exist")
	imageTestMustAddDirectory(t, svc, missingRoot)
	imageTestMustAddDirectory(t, svc, goodRoot)

	result := imageTestMustSync(t, svc)
	if result.Added != 1 {
		t.Fatalf("失败目录不得中断其他目录: %+v", result)
	}
	if len(result.Errors) != 1 || result.Errors[0].Operation != "scan" || result.Errors[0].Directory != missingRoot {
		t.Fatalf("失败目录应计入结果错误: %+v", result.Errors)
	}
}

func TestImageSyncDoesNotTouchVideoLibrary(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, videoPath)
	mustCreateFile(t, filepath.Join(root, "photo.jpg"))
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频记录失败: %v", err)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatalf("创建视频扫描目录失败: %v", err)
	}
	imageTestMustAddDirectory(t, svc, root)

	result := imageTestMustSync(t, svc)
	if result.Added != 1 {
		t.Fatalf("图片对账计数不符: %+v", result)
	}
	var videoCount int64
	if err := database.DB.Model(&models.Video{}).Count(&videoCount).Error; err != nil || videoCount != 1 {
		t.Fatalf("图片对账不得影响视频记录: count=%d err=%v", videoCount, err)
	}
	var reloaded models.Video
	if err := database.DB.First(&reloaded, video.ID).Error; err != nil || reloaded.Path != videoPath {
		t.Fatalf("视频记录被改动: video=%+v err=%v", reloaded, err)
	}
	var mp4Images int64
	if err := database.DB.Model(&models.Image{}).Where("path = ?", videoPath).Count(&mp4Images).Error; err != nil || mp4Images != 0 {
		t.Fatalf("视频文件不得进入图片库: count=%d err=%v", mp4Images, err)
	}
}

func TestImageAddImageRejectsDuplicatePath(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	path := filepath.Join(root, "one.jpg")
	mustCreateFile(t, path)

	if _, err := svc.addImage(path); err != nil {
		t.Fatalf("首次入库失败: %v", err)
	}
	if _, err := svc.addImage(path); !errors.Is(err, ErrImageExists) {
		t.Fatalf("重复路径应返回 ErrImageExists, got %v", err)
	}
	if imageTestActiveCount(t) != 1 {
		t.Fatalf("重复入库不得产生第二条记录: %d", imageTestActiveCount(t))
	}
}
