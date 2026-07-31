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

func TestPersonServiceAllowsSameNameAndListsActiveVideoCount(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	first, err := svc.CreatePerson("周迅", "Zhou Xun")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	second, err := svc.CreatePerson("周迅", "")
	if err != nil {
		t.Fatalf("同名人物应允许创建: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("同名人物必须是不同实体: first=%d second=%d", first.ID, second.ID)
	}

	video := createProbeTestVideo(t)
	if err := svc.SetVideoPeople(video.ID, []uint{first.ID}); err != nil {
		t.Fatalf("关联人物失败: %v", err)
	}
	items, err := svc.ListPeople("zhou", "", 0, 20)
	if err != nil {
		t.Fatalf("按原始名称搜索人物失败: %v", err)
	}
	if len(items) != 1 || items[0].Person.ID != first.ID || items[0].ActiveVideoCount != 1 {
		t.Fatalf("人物候选或活跃视频数错误: %#v", items)
	}

	if err := database.DB.Delete(&video).Error; err != nil {
		t.Fatalf("软删除视频失败: %v", err)
	}
	items, err = svc.ListPeople("周迅", "", 0, 20)
	if err != nil {
		t.Fatalf("软删除后列出人物失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("视频软删除不应清理人物: %#v", items)
	}
	for _, item := range items {
		if item.ActiveVideoCount != 0 {
			t.Fatalf("软删除视频不应计入活跃关联数: %#v", item)
		}
	}
	var relationCount int64
	if err := database.DB.Model(&models.VideoPerson{}).Where("video_id = ?", video.ID).Count(&relationCount).Error; err != nil {
		t.Fatalf("统计人物关系失败: %v", err)
	}
	if relationCount != 1 {
		t.Fatalf("视频软删除应保留人物关系: count=%d", relationCount)
	}
}

func TestPersonServiceExplicitLastRelationshipRemovalCleansPerson(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	person, err := svc.CreatePerson("Actor", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	first := createProbeTestVideo(t)
	second := createProbeTestVideo(t)
	if err := svc.SetVideoPeople(first.ID, []uint{person.ID}); err != nil {
		t.Fatalf("关联第一个视频失败: %v", err)
	}
	if err := svc.SetVideoPeople(second.ID, []uint{person.ID, person.ID}); err != nil {
		t.Fatalf("幂等关联第二个视频失败: %v", err)
	}

	if err := svc.SetVideoPeople(first.ID, nil); err != nil {
		t.Fatalf("移除非最后关系失败: %v", err)
	}
	if err := database.DB.First(&models.Person{}, person.ID).Error; err != nil {
		t.Fatalf("仍有关联时人物不应删除: %v", err)
	}
	if err := svc.SetVideoPeople(second.ID, nil); err != nil {
		t.Fatalf("移除最后关系失败: %v", err)
	}
	if err := database.DB.First(&models.Person{}, person.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("显式移除最后关系后人物应删除: err=%v", err)
	}
}

func TestPersonServiceMaintainsRelationshipsFromPersonDetail(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	person, err := svc.CreatePerson("Maintained Actor", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	first := createProbeTestVideo(t)
	second := createProbeTestVideo(t)

	if err := svc.AddPersonVideo(person.ID, first.ID); err != nil {
		t.Fatalf("从人物详情关联视频失败: %v", err)
	}
	if err := svc.AddPersonVideo(person.ID, first.ID); err != nil {
		t.Fatalf("重复关联应保持幂等: %v", err)
	}
	if err := svc.AddPersonVideo(person.ID, second.ID); err != nil {
		t.Fatalf("关联第二个视频失败: %v", err)
	}
	var relationCount int64
	if err := database.DB.Model(&models.VideoPerson{}).Where("person_id = ?", person.ID).Count(&relationCount).Error; err != nil {
		t.Fatalf("统计人物关系失败: %v", err)
	}
	if relationCount != 2 {
		t.Fatalf("重复请求不得创建重复关系: count=%d", relationCount)
	}

	deleted, err := svc.RemovePersonVideo(person.ID, first.ID)
	if err != nil {
		t.Fatalf("解除非最后关系失败: %v", err)
	}
	if deleted {
		t.Fatal("仍有视频关系时不得删除人物")
	}
	if err := database.DB.First(&models.Person{}, person.ID).Error; err != nil {
		t.Fatalf("仍有关联时人物应保留: %v", err)
	}

	deleted, err = svc.RemovePersonVideo(person.ID, second.ID)
	if err != nil {
		t.Fatalf("解除最后关系失败: %v", err)
	}
	if !deleted {
		t.Fatal("解除最后关系应报告人物已清理")
	}
	if err := database.DB.First(&models.Person{}, person.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("解除最后关系后人物应删除: err=%v", err)
	}
}

func TestPersonServiceRejectsInvalidPersonDetailRelationshipTargets(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	person, err := svc.CreatePerson("Valid Actor", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	video := createProbeTestVideo(t)
	if err := svc.AddPersonVideo(person.ID, 999999); err == nil {
		t.Fatal("关联不存在的视频应失败")
	}
	if err := svc.AddPersonVideo(999999, video.ID); err == nil {
		t.Fatal("关联不存在的人物应失败")
	}
	var relationCount int64
	if err := database.DB.Model(&models.VideoPerson{}).Count(&relationCount).Error; err != nil {
		t.Fatalf("统计人物关系失败: %v", err)
	}
	if relationCount != 0 {
		t.Fatalf("无效关联请求必须整体回滚: count=%d", relationCount)
	}
}

func TestPersonServiceValidatesNamesAndRelationshipTargets(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	if _, err := svc.CreatePerson("   ", ""); err == nil {
		t.Fatal("空显示名称应被拒绝")
	}
	if _, err := svc.CreatePerson(strings.Repeat("界", 201), ""); err == nil {
		t.Fatal("超过 200 rune 的显示名称应被拒绝")
	}
	video := createProbeTestVideo(t)
	if err := svc.SetVideoPeople(video.ID, []uint{999999}); err == nil {
		t.Fatal("不存在的人物关系目标应被拒绝")
	}
	var relationCount int64
	if err := database.DB.Model(&models.VideoPerson{}).Where("video_id = ?", video.ID).Count(&relationCount).Error; err != nil {
		t.Fatalf("统计关系失败: %v", err)
	}
	if relationCount != 0 {
		t.Fatalf("无效关系请求必须整体回滚: count=%d", relationCount)
	}
}

func TestPersonAvatarIsManagedReplacedAndRemoved(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	svc := NewPersonService(dataDir)
	person, err := svc.CreatePerson("Avatar Person", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	sourceDir := t.TempDir()
	firstSource := filepath.Join(sourceDir, "first.png")
	secondSource := filepath.Join(sourceDir, "second.png")
	firstPNG := append([]byte("\x89PNG\r\n\x1a\n"), []byte("first-image-content")...)
	secondPNG := append([]byte("\x89PNG\r\n\x1a\n"), []byte("second-image-content")...)
	if err := os.WriteFile(firstSource, firstPNG, 0600); err != nil {
		t.Fatalf("写入第一张头像失败: %v", err)
	}
	if err := os.WriteFile(secondSource, secondPNG, 0600); err != nil {
		t.Fatalf("写入第二张头像失败: %v", err)
	}

	updated, err := svc.SetPersonAvatar(person.ID, firstSource)
	if err != nil {
		t.Fatalf("设置头像失败: %v", err)
	}
	if updated.AvatarPath == "" || filepath.IsAbs(updated.AvatarPath) || strings.Contains(updated.AvatarPath, firstSource) {
		t.Fatalf("数据库只能保存托管相对路径: %#v", updated)
	}
	firstManaged, err := svc.ResolvePersonAvatar(person.ID)
	if err != nil {
		t.Fatalf("解析托管头像失败: %v", err)
	}
	if firstManaged.MIME != "image/png" {
		t.Fatalf("头像 MIME 错误: %#v", firstManaged)
	}
	if content, err := os.ReadFile(firstManaged.Path); err != nil || string(content) != string(firstPNG) {
		t.Fatalf("托管头像内容错误: content=%q err=%v", content, err)
	}
	if err := os.Remove(firstSource); err != nil {
		t.Fatalf("删除外部源头像失败: %v", err)
	}
	if _, err := os.Stat(firstManaged.Path); err != nil {
		t.Fatalf("托管头像不应依赖外部源文件: %v", err)
	}

	if _, err := svc.SetPersonAvatar(person.ID, secondSource); err != nil {
		t.Fatalf("替换头像失败: %v", err)
	}
	if _, err := os.Stat(firstManaged.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("替换后旧托管头像应清理: err=%v", err)
	}
	secondManaged, err := svc.ResolvePersonAvatar(person.ID)
	if err != nil {
		t.Fatalf("解析替换头像失败: %v", err)
	}
	if secondManaged.Path == firstManaged.Path {
		t.Fatalf("不同内容头像应使用不同托管路径: %q", secondManaged.Path)
	}

	if err := svc.RemovePersonAvatar(person.ID); err != nil {
		t.Fatalf("移除头像失败: %v", err)
	}
	if _, err := os.Stat(secondManaged.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("移除后托管头像应清理: err=%v", err)
	}
	if _, err := svc.ResolvePersonAvatar(person.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("无头像应返回 not exist: err=%v", err)
	}
}

func TestManagedImageRejectsUnsupportedOrOversizedSources(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	person, err := svc.CreatePerson("Image Validation", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	unsupported := filepath.Join(t.TempDir(), "avatar.txt")
	if err := os.WriteFile(unsupported, []byte("plain text"), 0600); err != nil {
		t.Fatalf("写入不支持文件失败: %v", err)
	}
	if _, err := svc.SetPersonAvatar(person.ID, unsupported); err == nil {
		t.Fatal("非 JPEG/PNG/WebP 头像应被拒绝")
	}
	oversized := filepath.Join(t.TempDir(), "large.png")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatalf("创建超大头像失败: %v", err)
	}
	if _, err := file.Write([]byte("\x89PNG\r\n\x1a\n")); err != nil {
		t.Fatalf("写入超大头像头失败: %v", err)
	}
	if err := file.Truncate(managedImageMaxBytes + 1); err != nil {
		t.Fatalf("扩展超大头像失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭超大头像失败: %v", err)
	}
	if _, err := svc.SetPersonAvatar(person.ID, oversized); err == nil {
		t.Fatal("超过 20 MiB 的头像应被拒绝")
	}
}
