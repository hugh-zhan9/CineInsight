package services

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func TestCollectionNameIsUniqueWhileActiveAndReusableAfterDelete(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewCollectionService(t.TempDir())
	collection, err := svc.CreateCollection("  Trilogy  ", "three films")
	if err != nil {
		t.Fatalf("创建作品集失败: %v", err)
	}
	if collection.Name != "Trilogy" || collection.NormalizedName != "trilogy" {
		t.Fatalf("作品集名称规范化错误: %#v", collection)
	}
	if _, err := svc.CreateCollection("TRILOGY", ""); !errors.Is(err, ErrCollectionNameConflict) {
		t.Fatalf("活跃规范化同名应冲突: %v", err)
	}
	video := createProbeTestVideo(t)
	if err := svc.AddCollectionVideo(collection.ID, video.ID); err != nil {
		t.Fatalf("加入作品集失败: %v", err)
	}
	if err := svc.DeleteCollection(collection.ID); err != nil {
		t.Fatalf("删除作品集失败: %v", err)
	}
	if err := database.DB.First(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("删除作品集不应删除视频: %v", err)
	}
	var relationCount int64
	if err := database.DB.Model(&models.CollectionVideo{}).Where("collection_id = ?", collection.ID).Count(&relationCount).Error; err != nil {
		t.Fatalf("统计作品集关系失败: %v", err)
	}
	if relationCount != 0 {
		t.Fatalf("删除作品集应删除自身关系: count=%d", relationCount)
	}
	recreated, err := svc.CreateCollection(" trilogy ", "new collection")
	if err != nil {
		t.Fatalf("删除后应允许新建同名作品集: %v", err)
	}
	if recreated.ID == collection.ID {
		t.Fatalf("删除后同名作品集必须获得新 ID: %d", recreated.ID)
	}
	if err := svc.DeleteCollection(collection.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("重复删除应返回 not found: %v", err)
	}
}

func TestCollectionSupportsMultipleMembershipAndStableAppend(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewCollectionService(t.TempDir())
	first, _ := svc.CreateCollection("First", "")
	second, _ := svc.CreateCollection("Second", "")
	videoA := createProbeTestVideo(t)
	videoB := createProbeTestVideo(t)
	if err := svc.AddCollectionVideo(first.ID, videoA.ID); err != nil {
		t.Fatalf("加入第一个作品集失败: %v", err)
	}
	if err := svc.AddCollectionVideo(first.ID, videoA.ID); err != nil {
		t.Fatalf("重复加入应幂等: %v", err)
	}
	if err := svc.AddCollectionVideo(first.ID, videoB.ID); err != nil {
		t.Fatalf("追加第二个视频失败: %v", err)
	}
	if err := svc.AddCollectionVideo(second.ID, videoA.ID); err != nil {
		t.Fatalf("同一视频应可加入多个作品集: %v", err)
	}
	var memberships []models.CollectionVideo
	if err := database.DB.Where("video_id = ?", videoA.ID).Order("collection_id ASC").Find(&memberships).Error; err != nil {
		t.Fatalf("读取多重归属失败: %v", err)
	}
	if len(memberships) != 2 {
		t.Fatalf("多重归属数量错误: %#v", memberships)
	}
	firstDetail, err := svc.GetCollectionDetail(first.ID)
	if err != nil {
		t.Fatalf("读取作品集详情失败: %v", err)
	}
	if len(firstDetail.Videos) != 2 || firstDetail.Videos[0].Video.ID != videoA.ID || firstDetail.Videos[0].Position != 1 || firstDetail.Videos[1].Video.ID != videoB.ID || firstDetail.Videos[1].Position != 2 {
		t.Fatalf("作品集追加顺序错误: %#v", firstDetail.Videos)
	}
}

func TestCollectionReorderPreservesHiddenVideoSlots(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewCollectionService(t.TempDir())
	collection, _ := svc.CreateCollection("Ordered", "")
	videos := []models.Video{createProbeTestVideo(t), createProbeTestVideo(t), createProbeTestVideo(t), createProbeTestVideo(t)}
	for _, video := range videos {
		if err := svc.AddCollectionVideo(collection.ID, video.ID); err != nil {
			t.Fatalf("添加作品集成员失败: %v", err)
		}
	}
	if err := database.DB.Delete(&videos[1]).Error; err != nil {
		t.Fatalf("软删除第二个成员失败: %v", err)
	}

	requested := []uint{videos[3].ID, videos[0].ID, videos[2].ID}
	if err := svc.ReorderCollectionVideos(collection.ID, requested); err != nil {
		t.Fatalf("重排活跃成员失败: %v", err)
	}
	var relations []models.CollectionVideo
	if err := database.DB.Where("collection_id = ?", collection.ID).Order("position ASC").Find(&relations).Error; err != nil {
		t.Fatalf("读取完整成员顺序失败: %v", err)
	}
	want := []uint{videos[3].ID, videos[1].ID, videos[0].ID, videos[2].ID}
	if len(relations) != len(want) {
		t.Fatalf("完整成员数量错误: %#v", relations)
	}
	for index, relation := range relations {
		if relation.VideoID != want[index] || relation.Position != index+1 {
			t.Fatalf("隐藏槽位未保留: index=%d got=%#v wantVideo=%d", index, relation, want[index])
		}
	}

	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", videos[1].ID).Update("deleted_at", nil).Error; err != nil {
		t.Fatalf("恢复隐藏视频失败: %v", err)
	}
	detail, err := svc.GetCollectionDetail(collection.ID)
	if err != nil {
		t.Fatalf("恢复后读取作品集失败: %v", err)
	}
	for index, member := range detail.Videos {
		if member.Video.ID != want[index] {
			t.Fatalf("恢复后顺序漂移: index=%d got=%d want=%d", index, member.Video.ID, want[index])
		}
	}
}

func TestCollectionRejectsIncompleteOrDuplicateReorderAtomically(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewCollectionService(t.TempDir())
	collection, _ := svc.CreateCollection("Conflict", "")
	first := createProbeTestVideo(t)
	second := createProbeTestVideo(t)
	_ = svc.AddCollectionVideo(collection.ID, first.ID)
	_ = svc.AddCollectionVideo(collection.ID, second.ID)
	if err := svc.ReorderCollectionVideos(collection.ID, []uint{second.ID}); !errors.Is(err, ErrCollectionOrderConflict) {
		t.Fatalf("不完整重排应冲突: %v", err)
	}
	if err := svc.ReorderCollectionVideos(collection.ID, []uint{second.ID, second.ID}); !errors.Is(err, ErrCollectionOrderConflict) {
		t.Fatalf("重复成员重排应冲突: %v", err)
	}
	detail, err := svc.GetCollectionDetail(collection.ID)
	if err != nil {
		t.Fatalf("读取冲突后的作品集失败: %v", err)
	}
	if detail.Videos[0].Video.ID != first.ID || detail.Videos[1].Video.ID != second.ID {
		t.Fatalf("无效重排不应产生部分更新: %#v", detail.Videos)
	}
}

func TestCollectionRemoveCompressesCompleteSequence(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewCollectionService(t.TempDir())
	collection, _ := svc.CreateCollection("Remove", "")
	videos := []models.Video{createProbeTestVideo(t), createProbeTestVideo(t), createProbeTestVideo(t)}
	for _, video := range videos {
		_ = svc.AddCollectionVideo(collection.ID, video.ID)
	}
	if err := database.DB.Delete(&videos[2]).Error; err != nil {
		t.Fatalf("软删除尾部视频失败: %v", err)
	}
	if err := svc.RemoveCollectionVideo(collection.ID, videos[0].ID); err != nil {
		t.Fatalf("移除作品集成员失败: %v", err)
	}
	var relations []models.CollectionVideo
	if err := database.DB.Where("collection_id = ?", collection.ID).Order("position ASC").Find(&relations).Error; err != nil {
		t.Fatalf("读取压缩序列失败: %v", err)
	}
	if len(relations) != 2 || relations[0].VideoID != videos[1].ID || relations[0].Position != 1 || relations[1].VideoID != videos[2].ID || relations[1].Position != 2 {
		t.Fatalf("移除后完整序列未压缩: %#v", relations)
	}
}

func TestCollectionCoverUsesManagedStorageAndDeleteCleansIt(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewCollectionService(t.TempDir())
	collection, _ := svc.CreateCollection("Covered", "")
	source := filepath.Join(t.TempDir(), "cover.webp")
	// RIFF size WEBP header plus local payload is enough for content-type detection.
	content := append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), []byte("cover-content")...)
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatalf("写入封面源文件失败: %v", err)
	}
	updated, err := svc.SetCollectionCover(collection.ID, source)
	if err != nil {
		t.Fatalf("设置作品集封面失败: %v", err)
	}
	if updated.CoverPath == "" || filepath.IsAbs(updated.CoverPath) {
		t.Fatalf("作品集封面必须保存相对路径: %#v", updated)
	}
	asset, err := svc.ResolveCollectionCover(collection.ID)
	if err != nil {
		t.Fatalf("解析作品集封面失败: %v", err)
	}
	if asset.MIME != "image/webp" {
		t.Fatalf("作品集封面 MIME 错误: %#v", asset)
	}
	if err := svc.DeleteCollection(collection.ID); err != nil {
		t.Fatalf("删除带封面的作品集失败: %v", err)
	}
	if _, err := os.Stat(asset.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("删除作品集应清理封面: err=%v", err)
	}
}

func TestCollectionValidatesEditableFields(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewCollectionService(t.TempDir())
	if _, err := svc.CreateCollection(" ", ""); err == nil {
		t.Fatal("空作品集名称应拒绝")
	}
	if _, err := svc.CreateCollection(strings.Repeat("集", 201), ""); err == nil {
		t.Fatal("超长作品集名称应拒绝")
	}
	if _, err := svc.CreateCollection("Valid", strings.Repeat("述", 4001)); err == nil {
		t.Fatal("超长作品集简介应拒绝")
	}
}
