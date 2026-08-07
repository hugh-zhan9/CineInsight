package services

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"
)

func imageTrashTestCreateImage(t *testing.T, path string, content string) *models.Image {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("创建图片文件失败: %v", err)
	}
	image := &models.Image{
		Name:      filepath.Base(path),
		Path:      path,
		Directory: filepath.Dir(path),
		Size:      int64(len(content)),
	}
	if err := database.DB.Create(image).Error; err != nil {
		t.Fatalf("创建图片记录失败: %v", err)
	}
	return image
}

func imageTrashTestEntries(t *testing.T, svc *ImageService) []models.ImageTrashEntry {
	t.Helper()
	entries, err := svc.ListImageTrashEntries()
	if err != nil {
		t.Fatalf("列出图片回收站失败: %v", err)
	}
	return entries
}

// TC-5 场景：删除入回收站——文件移动、软删记录、条目指纹齐备。
func TestImageTrashDeleteMovesFileAndCreatesEntry(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "photo.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "image-bytes")

	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatalf("删除图片失败: %v", err)
	}

	if _, err := os.Stat(imagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("期望原文件已移走, err=%v", err)
	}
	trashPath := filepath.Join(root, DefaultTrashDirName, "photo.jpg")
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("期望文件已移动到回收站: %v", err)
	}

	var deleted models.Image
	if err := database.DB.Unscoped().First(&deleted, image.ID).Error; err != nil {
		t.Fatalf("期望数据库仍可查到软删除记录: %v", err)
	}
	if !deleted.DeletedAt.IsValid() {
		t.Fatalf("期望图片记录已被软删除")
	}

	entries := imageTrashTestEntries(t, svc)
	if len(entries) != 1 {
		t.Fatalf("期望恰好一条回收站条目: %#v", entries)
	}
	entry := entries[0]
	if entry.ImageID != image.ID || entry.OriginalPath != imagePath || entry.TrashPath != trashPath {
		t.Fatalf("回收站条目与删除结果不一致: %#v", entry)
	}
	if entry.State != trashStateDeleted || !entry.FileMoved {
		t.Fatalf("条目应处于 deleted 且标记文件已移动: %#v", entry)
	}
	if entry.FileSize != int64(len("image-bytes")) || entry.FileModTime == 0 || entry.FileSHA256 == "" {
		t.Fatalf("条目指纹缺失: %#v", entry)
	}
}

// TC-5 场景：恢复成功回原路径，软删复活且标签、评分保留，条目物理删除。
func TestImageTrashRestoreRevivesRecordWithTagsAndRating(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "keep-state.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "restorable")

	tag := models.Tag{Name: "回收站标签", Color: "#abc"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Model(image).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("绑定标签失败: %v", err)
	}
	rating := 8.5
	if err := database.DB.Model(image).Update("personal_rating", rating).Error; err != nil {
		t.Fatalf("写入评分失败: %v", err)
	}

	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatalf("删除图片失败: %v", err)
	}
	entries := imageTrashTestEntries(t, svc)
	if len(entries) != 1 {
		t.Fatalf("读取回收站条目失败: %#v", entries)
	}

	restored, err := svc.RestoreImageTrashEntry(entries[0].ID)
	if err != nil {
		t.Fatalf("恢复图片失败: %v", err)
	}
	if restored.ID != image.ID || restored.Path != imagePath {
		t.Fatalf("恢复结果错误: %#v", restored)
	}
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatalf("图片文件未恢复到原路径: %v", err)
	}
	if _, err := os.Stat(entries[0].TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("回收站文件应已移回原路径: %v", err)
	}

	var active models.Image
	if err := database.DB.Preload("Tags").First(&active, image.ID).Error; err != nil {
		t.Fatalf("图片记录未恢复为活动状态: %v", err)
	}
	if len(active.Tags) != 1 || active.Tags[0].ID != tag.ID {
		t.Fatalf("恢复后标签应保留: %#v", active.Tags)
	}
	if active.PersonalRating == nil || *active.PersonalRating != rating {
		t.Fatalf("恢复后评分应保留: %#v", active.PersonalRating)
	}
	if remaining := imageTrashTestEntries(t, svc); len(remaining) != 0 {
		t.Fatalf("恢复后应物理删除回收站条目: %#v", remaining)
	}
}

// TC-5 场景：原路径已有同名文件时拒绝恢复且不覆盖。
func TestImageTrashRestoreRejectsOccupiedOriginalPath(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "occupied.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "original")

	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatalf("删除图片失败: %v", err)
	}
	entries := imageTrashTestEntries(t, svc)
	if len(entries) != 1 {
		t.Fatalf("读取回收站条目失败: %#v", entries)
	}
	if err := os.WriteFile(imagePath, []byte("replacement"), 0644); err != nil {
		t.Fatalf("创建占位文件失败: %v", err)
	}

	if _, err := svc.RestoreImageTrashEntry(entries[0].ID); err == nil {
		t.Fatalf("原路径被占用时恢复应失败")
	}
	content, err := os.ReadFile(imagePath)
	if err != nil || string(content) != "replacement" {
		t.Fatalf("恢复失败不应覆盖占位文件: content=%q err=%v", string(content), err)
	}
	if _, err := os.Stat(entries[0].TrashPath); err != nil {
		t.Fatalf("恢复失败后回收站文件应保留: %v", err)
	}
	remaining := imageTrashTestEntries(t, svc)
	if len(remaining) != 1 || remaining[0].State != trashStateDeleted || remaining[0].LastError == "" {
		t.Fatalf("恢复失败后条目应保留为可重试状态并留痕: %#v", remaining)
	}
	var stillDeleted models.Image
	if err := database.DB.Unscoped().First(&stillDeleted, image.ID).Error; err != nil || !stillDeleted.DeletedAt.IsValid() {
		t.Fatalf("恢复失败后记录应保持软删除: %#v err=%v", stillDeleted, err)
	}
}

// TC-5 场景：重复删除被 image_id 唯一条目拒绝，文件与记录不受影响。
func TestImageTrashRejectsDuplicateDelete(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "dup.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "dup-bytes")

	if err := database.DB.Create(&models.ImageTrashEntry{
		ImageID:      image.ID,
		ImageName:    image.Name,
		OriginalPath: image.Path,
		State:        trashStateDeleted,
	}).Error; err != nil {
		t.Fatalf("创建既有回收站条目失败: %v", err)
	}

	if err := svc.DeleteImage(image.ID, true); err == nil {
		t.Fatalf("已有活跃条目时删除应被拒绝")
	}
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatalf("拒绝重复删除后原文件应保留: %v", err)
	}
	if err := database.DB.First(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("拒绝重复删除后记录应保持活动: %v", err)
	}
	var entryCount int64
	if err := database.DB.Model(&models.ImageTrashEntry{}).Count(&entryCount).Error; err != nil || entryCount != 1 {
		t.Fatalf("不应产生第二条条目: count=%d err=%v", entryCount, err)
	}

	// 数据库层兜底：image_id 唯一索引直接拒绝第二条条目。
	if err := database.DB.Create(&models.ImageTrashEntry{
		ImageID:      image.ID,
		ImageName:    image.Name,
		OriginalPath: image.Path,
		State:        trashStateDeleted,
	}).Error; err == nil {
		t.Fatalf("image_id 唯一索引应拒绝重复条目")
	}
}

// TC-5 场景：deleteFile=false 仅软删记录——零条目、零文件移动。
func TestImageTrashDeleteRecordOnlyCreatesNoEntryAndKeepsFile(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "record-only.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "keep-me")

	if err := svc.DeleteImage(image.ID, false); err != nil {
		t.Fatalf("仅删除记录失败: %v", err)
	}

	if _, err := os.Stat(imagePath); err != nil {
		t.Fatalf("仅删除记录后原文件应保留: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, DefaultTrashDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("仅删除记录不应创建回收站目录: %v", err)
	}
	var deleted models.Image
	if err := database.DB.Unscoped().First(&deleted, image.ID).Error; err != nil || !deleted.DeletedAt.IsValid() {
		t.Fatalf("记录应被软删除: %#v err=%v", deleted, err)
	}
	if entries := imageTrashTestEntries(t, svc); len(entries) != 0 {
		t.Fatalf("仅删除记录不应创建回收站条目: %#v", entries)
	}
}

// TC-5 场景：源文件本就不存在时 deleteFile=true 仅落条目并软删（镜像视频侧）。
func TestImageTrashDeleteMissingSourceCreatesRecordOnlyEntry(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	image := &models.Image{
		Name:      "gone.jpg",
		Path:      filepath.Join(root, "gone.jpg"),
		Directory: root,
		Size:      3,
	}
	if err := database.DB.Create(image).Error; err != nil {
		t.Fatalf("创建图片记录失败: %v", err)
	}

	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatalf("删除缺失源文件的图片失败: %v", err)
	}
	entries := imageTrashTestEntries(t, svc)
	if len(entries) != 1 {
		t.Fatalf("应创建一条条目: %#v", entries)
	}
	if entries[0].FileMoved || entries[0].TrashPath != "" || entries[0].State != trashStateDeleted {
		t.Fatalf("缺失源文件不应标记文件移动: %#v", entries[0])
	}
	var deleted models.Image
	if err := database.DB.Unscoped().First(&deleted, image.ID).Error; err != nil || !deleted.DeletedAt.IsValid() {
		t.Fatalf("记录应被软删除: %#v err=%v", deleted, err)
	}
}

// 批量删除逐项记录失败原因。
func TestImageTrashBatchDeleteReportsPartialFailures(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imageA := imageTrashTestCreateImage(t, filepath.Join(root, "a.jpg"), "aa")
	imageB := imageTrashTestCreateImage(t, filepath.Join(root, "b.jpg"), "bb")

	result := svc.BatchDeleteImages([]uint{imageA.ID, 999999, imageB.ID}, false)
	if result.Requested != 3 || result.Succeeded != 2 || result.Failed != 1 {
		t.Fatalf("批量删除结果错误: %#v", result)
	}
	if len(result.Errors) != 1 || result.Errors[0].ImageID != 999999 {
		t.Fatalf("期望记录失败图片ID: %#v", result.Errors)
	}
	var remaining int64
	if err := database.DB.Model(&models.Image{}).Where("id IN ?", []uint{imageA.ID, imageB.ID}).Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("期望成功项已被软删除: remaining=%d err=%v", remaining, err)
	}
}

// 回收站列表按最新删除优先排序。
func TestImageTrashListReturnsNewestFirst(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	first := imageTrashTestCreateImage(t, filepath.Join(root, "first.jpg"), "f1")
	second := imageTrashTestCreateImage(t, filepath.Join(root, "second.jpg"), "s2")

	if err := svc.DeleteImage(first.ID, true); err != nil {
		t.Fatalf("删除第一张失败: %v", err)
	}
	if err := svc.DeleteImage(second.ID, true); err != nil {
		t.Fatalf("删除第二张失败: %v", err)
	}

	entries := imageTrashTestEntries(t, svc)
	if len(entries) != 2 {
		t.Fatalf("回收站条目数量错误: %#v", entries)
	}
	if entries[0].ImageID != second.ID || entries[1].ImageID != first.ID {
		t.Fatalf("回收站应按最新删除优先排序: %#v", entries)
	}
}

// TC-5 场景：pending_move 崩溃、文件尚未移动 → 对账取消删除，记录保持活跃。
func TestImageTrashReconcileCancelsPendingDeleteWhenFileNotMoved(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "pending-unmoved.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "still-here")

	info, err := os.Stat(imagePath)
	if err != nil {
		t.Fatalf("读取文件信息失败: %v", err)
	}
	digest, err := fileSHA256Hex(imagePath)
	if err != nil {
		t.Fatalf("计算摘要失败: %v", err)
	}
	entry := models.ImageTrashEntry{
		ImageID:      image.ID,
		ImageName:    image.Name,
		OriginalPath: image.Path,
		TrashPath:    NewTrashService().TrashTargetPath(image.Path, 0),
		FileSize:     info.Size(),
		FileModTime:  info.ModTime().UnixNano(),
		FileIdentity: stableFileIdentity(info),
		FileSHA256:   digest,
		State:        trashStatePendingMove,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("创建待移动条目失败: %v", err)
	}

	if err := svc.ReconcileImageTrashEntries(); err != nil {
		t.Fatalf("启动对账失败: %v", err)
	}
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatalf("取消后原文件应保留: %v", err)
	}
	if err := database.DB.First(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("取消后记录应保持活动: %v", err)
	}
	if entries := imageTrashTestEntries(t, svc); len(entries) != 0 {
		t.Fatalf("取消后条目应删除: %#v", entries)
	}
}

// TC-5 场景：pending_move 崩溃、文件已移入回收站 → 对账补提交事务。
func TestImageTrashReconcileCompletesPendingDeleteAfterFileMove(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "pending-moved.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "already-moved")

	info, err := os.Stat(imagePath)
	if err != nil {
		t.Fatalf("读取文件信息失败: %v", err)
	}
	digest, err := fileSHA256Hex(imagePath)
	if err != nil {
		t.Fatalf("计算摘要失败: %v", err)
	}
	trashService := NewTrashService()
	entry := models.ImageTrashEntry{
		ImageID:      image.ID,
		ImageName:    image.Name,
		OriginalPath: image.Path,
		TrashPath:    trashService.TrashTargetPath(image.Path, 0),
		FileSize:     info.Size(),
		FileModTime:  info.ModTime().UnixNano(),
		FileIdentity: stableFileIdentity(info),
		FileSHA256:   digest,
		State:        trashStatePendingMove,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("创建待移动条目失败: %v", err)
	}
	if err := trashService.MoveToTrashAt(imagePath, entry.TrashPath); err != nil {
		t.Fatalf("模拟文件已移动失败: %v", err)
	}

	if err := svc.ReconcileImageTrashEntries(); err != nil {
		t.Fatalf("启动对账失败: %v", err)
	}
	var deleted models.Image
	if err := database.DB.Unscoped().First(&deleted, image.ID).Error; err != nil || !deleted.DeletedAt.IsValid() {
		t.Fatalf("对账后图片应完成软删除: %#v err=%v", deleted, err)
	}
	var reconciled models.ImageTrashEntry
	if err := database.DB.First(&reconciled, entry.ID).Error; err != nil {
		t.Fatalf("读取对账条目失败: %v", err)
	}
	if reconciled.State != trashStateDeleted || !reconciled.FileMoved {
		t.Fatalf("待移动条目未完成: %#v", reconciled)
	}
	if _, err := os.Stat(entry.TrashPath); err != nil {
		t.Fatalf("回收站文件应保留: %v", err)
	}
}

// TC-5 场景：restoring 崩溃、文件已回原路径 → 对账复活记录并清理条目。
func TestImageTrashReconcileCompletesInterruptedRestore(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "restoring.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "restore-me")

	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatalf("删除图片失败: %v", err)
	}
	entries := imageTrashTestEntries(t, svc)
	if len(entries) != 1 {
		t.Fatalf("读取回收站失败: %#v", entries)
	}
	entry := entries[0]
	if err := database.DB.Model(&entry).Update("state", trashStateRestoring).Error; err != nil {
		t.Fatalf("标记恢复中失败: %v", err)
	}
	if err := NewTrashService().RestoreFromTrash(entry.TrashPath, entry.OriginalPath); err != nil {
		t.Fatalf("模拟文件已恢复失败: %v", err)
	}

	if err := svc.ReconcileImageTrashEntries(); err != nil {
		t.Fatalf("恢复对账失败: %v", err)
	}
	if err := database.DB.First(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("对账后图片应恢复活动状态: %v", err)
	}
	var entryCount int64
	if err := database.DB.Model(&models.ImageTrashEntry{}).Where("id = ?", entry.ID).Count(&entryCount).Error; err != nil || entryCount != 0 {
		t.Fatalf("对账后条目应删除: count=%d err=%v", entryCount, err)
	}
}

// TC-5 场景：回收站文件被外部改动（同尺寸不同内容）→ 指纹失配拒绝恢复并留痕。
func TestImageTrashRestoreRejectsFingerprintMismatch(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "tampered.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "original")

	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatalf("删除图片失败: %v", err)
	}
	entries := imageTrashTestEntries(t, svc)
	if len(entries) != 1 {
		t.Fatalf("读取回收站失败: %#v", entries)
	}
	entry := entries[0]
	// 换 inode + 同尺寸不同内容：强身份与 SHA-256 双双失配。
	if err := os.Remove(entry.TrashPath); err != nil {
		t.Fatalf("移除回收站文件失败: %v", err)
	}
	if err := os.WriteFile(entry.TrashPath, []byte("replaced"), 0644); err != nil {
		t.Fatalf("写入篡改文件失败: %v", err)
	}

	if _, err := svc.RestoreImageTrashEntry(entry.ID); err == nil {
		t.Fatalf("指纹失配时恢复应被拒绝")
	}
	if _, err := os.Stat(imagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("拒绝恢复后原路径不应出现文件: %v", err)
	}
	content, err := os.ReadFile(entry.TrashPath)
	if err != nil || string(content) != "replaced" {
		t.Fatalf("拒绝恢复不应移动被篡改文件: content=%q err=%v", string(content), err)
	}
	var retained models.ImageTrashEntry
	if err := database.DB.First(&retained, entry.ID).Error; err != nil {
		t.Fatalf("读取失败条目失败: %v", err)
	}
	if retained.State != trashStateDeleted || retained.LastError == "" {
		t.Fatalf("失败条目应回到 deleted 并保留诊断信息: %#v", retained)
	}
	var stillDeleted models.Image
	if err := database.DB.Unscoped().First(&stillDeleted, image.ID).Error; err != nil || !stillDeleted.DeletedAt.IsValid() {
		t.Fatalf("拒绝恢复后记录应保持软删除: %#v err=%v", stillDeleted, err)
	}
}

// TC-5 场景：软删记录的路径被新的活跃记录复用 → 拒绝恢复，文件与两条记录均不动。
func TestImageTrashRestoreRejectsPathReusedByNewRecord(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "reused.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "old-image")

	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatalf("删除图片失败: %v", err)
	}
	entries := imageTrashTestEntries(t, svc)
	if len(entries) != 1 {
		t.Fatalf("读取回收站失败: %#v", entries)
	}
	// 同路径被新的活跃记录占用（部分唯一索引允许：旧行已软删）。
	newcomer := &models.Image{
		Name:      "reused.jpg",
		Path:      imagePath,
		Directory: root,
		Size:      9,
	}
	if err := database.DB.Create(newcomer).Error; err != nil {
		t.Fatalf("创建复用路径的新记录失败: %v", err)
	}

	if _, err := svc.RestoreImageTrashEntry(entries[0].ID); err == nil {
		t.Fatalf("路径被新记录占用时恢复应被拒绝")
	}
	if _, err := os.Stat(entries[0].TrashPath); err != nil {
		t.Fatalf("拒绝恢复后回收站文件应保留: %v", err)
	}
	if err := database.DB.First(&models.Image{}, newcomer.ID).Error; err != nil {
		t.Fatalf("新记录应保持活动: %v", err)
	}
	var stillDeleted models.Image
	if err := database.DB.Unscoped().First(&stillDeleted, image.ID).Error; err != nil || !stillDeleted.DeletedAt.IsValid() {
		t.Fatalf("旧记录应保持软删除: %#v err=%v", stillDeleted, err)
	}
	if remaining := imageTrashTestEntries(t, svc); len(remaining) != 1 || remaining[0].State != trashStateDeleted {
		t.Fatalf("拒绝恢复后条目应保留: %#v", remaining)
	}
}

// rollback 崩溃对账：删除事务已回滚但文件补偿中断 → 对账把文件移回原路径并清理条目。
func TestImageTrashReconcileCompletesRollback(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "rollback.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "roll-me-back")

	info, err := os.Stat(imagePath)
	if err != nil {
		t.Fatalf("读取文件信息失败: %v", err)
	}
	digest, err := fileSHA256Hex(imagePath)
	if err != nil {
		t.Fatalf("计算摘要失败: %v", err)
	}
	trashService := NewTrashService()
	entry := models.ImageTrashEntry{
		ImageID:      image.ID,
		ImageName:    image.Name,
		OriginalPath: image.Path,
		TrashPath:    trashService.TrashTargetPath(image.Path, 0),
		FileSize:     info.Size(),
		FileModTime:  info.ModTime().UnixNano(),
		FileIdentity: stableFileIdentity(info),
		FileSHA256:   digest,
		State:        trashStateRollback,
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("创建回滚条目失败: %v", err)
	}
	if err := trashService.MoveToTrashAt(imagePath, entry.TrashPath); err != nil {
		t.Fatalf("模拟回滚前文件仍在回收站失败: %v", err)
	}

	if err := svc.ReconcileImageTrashEntries(); err != nil {
		t.Fatalf("回滚对账失败: %v", err)
	}
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatalf("回滚后文件应回到原路径: %v", err)
	}
	if _, err := os.Stat(entry.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("回滚后回收站不应残留文件: %v", err)
	}
	if err := database.DB.First(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("回滚后记录应保持活动: %v", err)
	}
	if entries := imageTrashTestEntries(t, svc); len(entries) != 0 {
		t.Fatalf("回滚后条目应删除: %#v", entries)
	}
}

// restoring 崩溃、文件仍在回收站 → 对账继续完成恢复（文件移回 + 复活记录 + 清理条目）。
func TestImageTrashReconcileRestoresFileStillInTrash(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "restoring-in-trash.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "still-in-trash")

	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatalf("删除图片失败: %v", err)
	}
	entries := imageTrashTestEntries(t, svc)
	if len(entries) != 1 {
		t.Fatalf("读取回收站失败: %#v", entries)
	}
	entry := entries[0]
	if err := database.DB.Model(&entry).Update("state", trashStateRestoring).Error; err != nil {
		t.Fatalf("标记恢复中失败: %v", err)
	}

	if err := svc.ReconcileImageTrashEntries(); err != nil {
		t.Fatalf("恢复对账失败: %v", err)
	}
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatalf("对账后文件应回到原路径: %v", err)
	}
	if _, err := os.Stat(entry.TrashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("对账后回收站不应残留文件: %v", err)
	}
	if err := database.DB.First(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("对账后图片应恢复活动状态: %v", err)
	}
	if remaining := imageTrashTestEntries(t, svc); len(remaining) != 0 {
		t.Fatalf("对账后条目应删除: %#v", remaining)
	}
}

// 存在文件未移动的 pending_move 残留时再次删除：先取消旧条目，再完成一次全新删除。
func TestImageTrashDeleteCancelsUnmovedPendingThenDeletesFresh(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "redo-delete.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "redo-bytes")

	info, err := os.Stat(imagePath)
	if err != nil {
		t.Fatalf("读取文件信息失败: %v", err)
	}
	digest, err := fileSHA256Hex(imagePath)
	if err != nil {
		t.Fatalf("计算摘要失败: %v", err)
	}
	stale := models.ImageTrashEntry{
		ImageID:      image.ID,
		ImageName:    image.Name,
		OriginalPath: image.Path,
		TrashPath:    NewTrashService().TrashTargetPath(image.Path, 0),
		FileSize:     info.Size(),
		FileModTime:  info.ModTime().UnixNano(),
		FileIdentity: stableFileIdentity(info),
		FileSHA256:   digest,
		State:        trashStatePendingMove,
	}
	if err := database.DB.Create(&stale).Error; err != nil {
		t.Fatalf("创建残留条目失败: %v", err)
	}

	if err := svc.DeleteImage(image.ID, true); err != nil {
		t.Fatalf("残留未移动条目时删除应成功: %v", err)
	}
	if _, err := os.Stat(imagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("期望原文件已移走: %v", err)
	}
	var deleted models.Image
	if err := database.DB.Unscoped().First(&deleted, image.ID).Error; err != nil || !deleted.DeletedAt.IsValid() {
		t.Fatalf("期望图片记录已软删除: %#v err=%v", deleted, err)
	}
	entries := imageTrashTestEntries(t, svc)
	if len(entries) != 1 {
		t.Fatalf("应恰好一条新条目: %#v", entries)
	}
	if entries[0].ID == stale.ID || entries[0].State != trashStateDeleted || !entries[0].FileMoved {
		t.Fatalf("旧条目应被取消并由新条目完成删除: %#v", entries[0])
	}
	if _, err := os.Stat(entries[0].TrashPath); err != nil {
		t.Fatalf("文件应位于新条目的回收站路径: %v", err)
	}
}

// 中断删除（pending_move 已移文件）对用户可见，恢复入口可直接取消并回滚文件。
func TestImageTrashRestoreCancelsVisibleInterruptedDelete(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageService()
	root := t.TempDir()
	imagePath := filepath.Join(root, "cancel-pending.jpg")
	image := imageTrashTestCreateImage(t, imagePath, "cancel-me")

	info, err := os.Stat(imagePath)
	if err != nil {
		t.Fatalf("读取文件信息失败: %v", err)
	}
	digest, err := fileSHA256Hex(imagePath)
	if err != nil {
		t.Fatalf("计算摘要失败: %v", err)
	}
	trashService := NewTrashService()
	entry := models.ImageTrashEntry{
		ImageID:      image.ID,
		ImageName:    image.Name,
		OriginalPath: image.Path,
		TrashPath:    trashService.TrashTargetPath(image.Path, 0),
		FileSize:     info.Size(),
		FileModTime:  info.ModTime().UnixNano(),
		FileIdentity: stableFileIdentity(info),
		FileSHA256:   digest,
		State:        trashStatePendingMove,
		LastError:    "上次移动中断",
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("创建中断条目失败: %v", err)
	}
	if err := trashService.MoveToTrashAt(imagePath, entry.TrashPath); err != nil {
		t.Fatalf("模拟中断删除的文件移动失败: %v", err)
	}

	restored, err := svc.RestoreImageTrashEntry(entry.ID)
	if err != nil {
		t.Fatalf("恢复中断删除失败: %v", err)
	}
	if restored.ID != image.ID {
		t.Fatalf("恢复了错误的图片: %#v", restored)
	}
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatalf("文件未恢复到原路径: %v", err)
	}
	if remaining := imageTrashTestEntries(t, svc); len(remaining) != 0 {
		t.Fatalf("恢复后中断条目应清理: %#v", remaining)
	}
}
