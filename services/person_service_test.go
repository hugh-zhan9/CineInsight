package services

import (
	"errors"
	"fmt"
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

func mustCountImagePeople(t *testing.T, personID uint) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Model(&models.ImagePerson{}).Where("person_id = ?", personID).Count(&count).Error; err != nil {
		t.Fatalf("统计图片人物关系失败: %v", err)
	}
	return count
}

// 人物同时覆盖视频与图片后（D-015），最后关系判定必须跨两种媒体：仅剩图片关系时
// 移除最后一条视频关系不得清理人物，两侧都空才走既有的清理流程。
func TestPersonKeepsPersonWhileImageRelationsRemain(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	person, err := svc.CreatePerson("跨媒体人物", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	video := createProbeTestVideo(t)
	image := mustCreateTestImage(t, "person-cross-media.jpg", 1024)
	if err := svc.AddPersonVideos(person.ID, []uint{video.ID}); err != nil {
		t.Fatalf("关联视频失败: %v", err)
	}
	if err := svc.AddPersonImages(person.ID, []uint{image.ID}); err != nil {
		t.Fatalf("关联图片失败: %v", err)
	}

	deleted, err := svc.RemovePersonVideo(person.ID, video.ID)
	if err != nil {
		t.Fatalf("解除视频关系失败: %v", err)
	}
	if deleted {
		t.Fatal("仍有图片关系时不得清理人物")
	}
	if err := database.DB.First(&models.Person{}, person.ID).Error; err != nil {
		t.Fatalf("仍有图片关系时人物应保留: %v", err)
	}
	if count := mustCountImagePeople(t, person.ID); count != 1 {
		t.Fatalf("人物未被清理时图片关系应保留: count=%d", count)
	}

	deleted, err = svc.RemovePersonImage(person.ID, image.ID)
	if err != nil {
		t.Fatalf("解除图片关系失败: %v", err)
	}
	if !deleted {
		t.Fatal("两侧关系皆空后应报告人物已清理")
	}
	if err := database.DB.First(&models.Person{}, person.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("两侧关系皆空后人物应删除: err=%v", err)
	}
}

// 反方向：图片侧的两条移除路径在「视频侧仍有关系」时都必须让人物活着。
// 上一个用例只钉住了「两侧皆空才清理」，这里钉住「另一侧还在就不清理」。
func TestRemovePersonImageKeepsPersonWithVideoRelations(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	person, err := svc.CreatePerson("视频保底人物", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	video := createProbeTestVideo(t)
	image := mustCreateTestImage(t, "keep-by-video.jpg", 1024)
	if err := svc.AddPersonVideos(person.ID, []uint{video.ID}); err != nil {
		t.Fatalf("关联视频失败: %v", err)
	}
	if err := svc.AddPersonImages(person.ID, []uint{image.ID}); err != nil {
		t.Fatalf("关联图片失败: %v", err)
	}

	deleted, err := svc.RemovePersonImage(person.ID, image.ID)
	if err != nil {
		t.Fatalf("解除图片关系失败: %v", err)
	}
	if deleted {
		t.Fatal("仍有视频关系时不得清理人物")
	}
	if err := database.DB.First(&models.Person{}, person.ID).Error; err != nil {
		t.Fatalf("仍有视频关系时人物应保留: %v", err)
	}
	var videoRelations int64
	if err := database.DB.Model(&models.VideoPerson{}).Where("person_id = ?", person.ID).Count(&videoRelations).Error; err != nil {
		t.Fatalf("统计视频人物关系失败: %v", err)
	}
	if videoRelations != 1 {
		t.Fatalf("解除图片关系不得动到视频关系: count=%d", videoRelations)
	}
	if count := mustCountImagePeople(t, person.ID); count != 0 {
		t.Fatalf("图片关系应已删除: count=%d", count)
	}
}

func TestSetImagePeopleKeepsPersonWithVideoRelations(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	person, err := svc.CreatePerson("清空图片人物", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	video := createProbeTestVideo(t)
	image := mustCreateTestImage(t, "keep-by-video-set.jpg", 1024)
	if err := svc.AddPersonVideos(person.ID, []uint{video.ID}); err != nil {
		t.Fatalf("关联视频失败: %v", err)
	}
	if err := svc.SetImagePeople(image.ID, []uint{person.ID}); err != nil {
		t.Fatalf("设置图片人物失败: %v", err)
	}

	if err := svc.SetImagePeople(image.ID, nil); err != nil {
		t.Fatalf("清空图片人物失败: %v", err)
	}
	if err := database.DB.First(&models.Person{}, person.ID).Error; err != nil {
		t.Fatalf("仍有视频关系时人物应保留: %v", err)
	}
	var videoRelations int64
	if err := database.DB.Model(&models.VideoPerson{}).Where("person_id = ?", person.ID).Count(&videoRelations).Error; err != nil {
		t.Fatalf("统计视频人物关系失败: %v", err)
	}
	if videoRelations != 1 {
		t.Fatalf("清空图片人物不得动到视频关系: count=%d", videoRelations)
	}
	if count := mustCountImagePeople(t, person.ID); count != 0 {
		t.Fatalf("图片关系应已清空: count=%d", count)
	}
}

// 清理判定数的是关系行，不是活跃媒体：唯一的视频关系指向软删除视频时，
// 移除最后一条图片关系仍不得清理人物——那条关系还在表里。
func TestRemovePersonImageKeepsPersonWhenOnlyVideoIsSoftDeleted(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	person, err := svc.CreatePerson("软删视频保底人物", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	video := createProbeTestVideo(t)
	image := mustCreateTestImage(t, "keep-by-soft-deleted-video.jpg", 1024)
	if err := svc.AddPersonVideos(person.ID, []uint{video.ID}); err != nil {
		t.Fatalf("关联视频失败: %v", err)
	}
	if err := svc.AddPersonImages(person.ID, []uint{image.ID}); err != nil {
		t.Fatalf("关联图片失败: %v", err)
	}
	if err := database.DB.Delete(&video).Error; err != nil {
		t.Fatalf("软删除视频失败: %v", err)
	}

	detail, err := svc.GetPersonDetail(person.ID, 0, 50)
	if err != nil {
		t.Fatalf("读取人物详情失败: %v", err)
	}
	if detail.Person.ActiveVideoCount != 0 || detail.Person.ActiveImageCount != 1 {
		t.Fatalf("活跃计数应只数未软删除媒体: %#v", detail.Person)
	}

	deleted, err := svc.RemovePersonImage(person.ID, image.ID)
	if err != nil {
		t.Fatalf("解除图片关系失败: %v", err)
	}
	if deleted {
		t.Fatal("软删除视频保留的关系仍算关系，不得清理人物")
	}
	if err := database.DB.First(&models.Person{}, person.ID).Error; err != nil {
		t.Fatalf("软删除视频保留关系时人物应保留: %v", err)
	}
}

// SetVideoPeople 与 UpdateVideoDetails 走的是另外两条清理路径，同样不得误删仍有
// 图片关系的人物。
func TestSetVideoPeopleKeepsPersonWithImageRelations(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	person, err := svc.CreatePerson("图片保底人物", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	video := createProbeTestVideo(t)
	image := mustCreateTestImage(t, "person-set-video.jpg", 1024)
	if err := svc.SetVideoPeople(video.ID, []uint{person.ID}); err != nil {
		t.Fatalf("关联视频失败: %v", err)
	}
	if err := svc.AddPersonImages(person.ID, []uint{image.ID}); err != nil {
		t.Fatalf("关联图片失败: %v", err)
	}

	if err := svc.SetVideoPeople(video.ID, nil); err != nil {
		t.Fatalf("清空视频人物失败: %v", err)
	}
	if err := database.DB.First(&models.Person{}, person.ID).Error; err != nil {
		t.Fatalf("仍有图片关系时人物应保留: %v", err)
	}

	detailService := NewVideoDetailService(svc, NewCollectionService(t.TempDir()))
	second := createProbeTestVideo(t)
	if _, err := detailService.UpdateVideoDetails(VideoDetailsUpdate{VideoID: second.ID, PersonIDs: []uint{person.ID}}); err != nil {
		t.Fatalf("从作品信息关联人物失败: %v", err)
	}
	if _, err := detailService.UpdateVideoDetails(VideoDetailsUpdate{VideoID: second.ID, PersonIDs: []uint{}}); err != nil {
		t.Fatalf("从作品信息移除人物失败: %v", err)
	}
	if err := database.DB.First(&models.Person{}, person.ID).Error; err != nil {
		t.Fatalf("作品信息移除后仍有图片关系的人物应保留: %v", err)
	}
	if count := mustCountImagePeople(t, person.ID); count != 1 {
		t.Fatalf("人物未被清理时图片关系应保留: count=%d", count)
	}
}

// 图片软删除保留关系、不触发清理，与视频侧一致。
func TestImageSoftDeleteKeepsPersonAndRelation(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	person, err := svc.CreatePerson("软删除人物", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	image := mustCreateTestImage(t, "person-soft-delete.jpg", 2048)
	if err := svc.AddPersonImages(person.ID, []uint{image.ID}); err != nil {
		t.Fatalf("关联图片失败: %v", err)
	}
	if err := database.DB.Delete(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("软删除图片失败: %v", err)
	}

	if err := database.DB.First(&models.Person{}, person.ID).Error; err != nil {
		t.Fatalf("图片软删除不应清理人物: %v", err)
	}
	if count := mustCountImagePeople(t, person.ID); count != 1 {
		t.Fatalf("图片软删除应保留人物关系: count=%d", count)
	}
	items, err := svc.ListPeople("软删除人物", "", 0, 20)
	if err != nil {
		t.Fatalf("列出人物失败: %v", err)
	}
	if len(items) != 1 || items[0].ActiveImageCount != 0 {
		t.Fatalf("软删除图片不应计入活跃关联数: %#v", items)
	}
}

// AddPersonImages / SetImagePeople 的幂等与清理语义与视频侧一一对应。
func TestPersonImageRelationsAreIdempotentAndValidated(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	person, err := svc.CreatePerson("图片人物", "Image Person")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	other, err := svc.CreatePerson("另一个人物", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	first := mustCreateTestImage(t, "relation-first.jpg", 100)
	second := mustCreateTestImage(t, "relation-second.jpg", 200)

	if err := svc.AddPersonImages(person.ID, []uint{first.ID, first.ID, second.ID}); err != nil {
		t.Fatalf("批量关联图片失败: %v", err)
	}
	if err := svc.AddPersonImages(person.ID, []uint{first.ID}); err != nil {
		t.Fatalf("重复关联应保持幂等: %v", err)
	}
	if count := mustCountImagePeople(t, person.ID); count != 2 {
		t.Fatalf("重复请求不得创建重复关系: count=%d", count)
	}
	if err := svc.AddPersonImages(person.ID, nil); err == nil {
		t.Fatal("空图片列表应报错")
	}
	if err := svc.AddPersonImages(person.ID, []uint{first.ID, 999999}); err == nil {
		t.Fatal("不存在的图片应报错")
	}
	if count := mustCountImagePeople(t, person.ID); count != 2 {
		t.Fatalf("失败的批量关联不得留下部分写入: count=%d", count)
	}

	if err := svc.SetImagePeople(first.ID, []uint{person.ID, other.ID}); err != nil {
		t.Fatalf("设置图片人物失败: %v", err)
	}
	detail, err := NewImageLibraryService().GetImageDetail(first.ID)
	if err != nil {
		t.Fatalf("读取图片详情失败: %v", err)
	}
	if len(detail.People) != 2 {
		t.Fatalf("图片详情应带出两个人物: %#v", detail.People)
	}
	if err := svc.SetImagePeople(first.ID, []uint{person.ID}); err != nil {
		t.Fatalf("移除图片人物失败: %v", err)
	}
	if err := database.DB.First(&models.Person{}, other.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("移除唯一关系后人物应被清理: err=%v", err)
	}
	if err := svc.SetImagePeople(second.ID, []uint{999999}); err == nil {
		t.Fatal("不存在的人物应报错")
	}
}

// 人物详情的视频与图片各自分页、不混排：GetPersonDetail 给图片首页，
// GetPersonImages 继续翻页。
func TestPersonDetailPagesVideosAndImagesSeparately(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	person, err := svc.CreatePerson("分页人物", "")
	if err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	videoIDs := make([]uint, 0, 3)
	for i := 0; i < 3; i++ {
		video := createProbeTestVideo(t)
		videoIDs = append(videoIDs, video.ID)
	}
	if err := svc.AddPersonVideos(person.ID, videoIDs); err != nil {
		t.Fatalf("关联视频失败: %v", err)
	}
	imageIDs := make([]uint, 0, 3)
	for i := 0; i < 3; i++ {
		image := mustCreateTestImage(t, fmt.Sprintf("paged-%d.jpg", i), int64(100+i))
		imageIDs = append(imageIDs, image.ID)
	}
	if err := svc.AddPersonImages(person.ID, imageIDs); err != nil {
		t.Fatalf("关联图片失败: %v", err)
	}

	detail, err := svc.GetPersonDetail(person.ID, 0, 2)
	if err != nil {
		t.Fatalf("读取人物详情失败: %v", err)
	}
	if detail.Person.ActiveVideoCount != 3 || detail.Person.ActiveImageCount != 3 {
		t.Fatalf("两侧活跃计数错误: %#v", detail.Person)
	}
	if len(detail.Images) != 2 || detail.NextImageID != imageIDs[1] {
		t.Fatalf("图片首页或游标错误: images=%d next=%d", len(detail.Images), detail.NextImageID)
	}
	if detail.Images[0].ID != imageIDs[2] || detail.Images[1].ID != imageIDs[1] {
		t.Fatalf("图片首页应按 id 倒序: %#v", detail.Images)
	}

	// 视频翻页只推进视频游标：图片区块不重复下发（前端本来就丢弃翻页响应里的
	// 图片字段），也绝不能把图片游标跟着推走。
	secondVideoPage, err := svc.GetPersonDetail(person.ID, detail.NextVideoID, 2)
	if err != nil {
		t.Fatalf("视频翻页失败: %v", err)
	}
	if len(secondVideoPage.Videos) != 1 || secondVideoPage.Videos[0].ID != videoIDs[0] {
		t.Fatalf("视频第二页错误: %#v", secondVideoPage.Videos)
	}
	if len(secondVideoPage.Images) != 0 || secondVideoPage.NextImageID != 0 {
		t.Fatalf("翻视频页不应再下发图片区块: images=%d next=%d", len(secondVideoPage.Images), secondVideoPage.NextImageID)
	}

	imagePage, err := svc.GetPersonImages(person.ID, detail.NextImageID, 2)
	if err != nil {
		t.Fatalf("图片翻页失败: %v", err)
	}
	if len(imagePage.Images) != 1 || imagePage.Images[0].ID != imageIDs[0] {
		t.Fatalf("图片第二页错误: %#v", imagePage.Images)
	}
	if imagePage.NextImageID != 0 {
		t.Fatalf("最后一页不应给出游标: next=%d", imagePage.NextImageID)
	}

	// 人物不存在时与 GetPersonDetail 对称报错，而不是伪装成「没有图片」。
	if _, err := svc.GetPersonImages(person.ID+9999, 0, 2); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("不存在的人物应返回 ErrRecordNotFound: err=%v", err)
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
