package services

import (
	"errors"
	"testing"

	"video-master/database"
	"video-master/models"
)

// 本文件验收 P-017 独立评审的修复项（META-01 / META-02 / META-14）。

// META01：删除「人物」分类标签（曾关联视频）后重排：只挂该标签的视频变为无人工标签。
func TestTagServiceMETA01DeletePersonTagWithMediaResetsAITagging(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := p017Video(t, "person-only.mp4")
	person := models.Tag{Name: "张三", Namespace: personTagNamespace}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", video.ID, person.ID).Error; err != nil {
		t.Fatal(err)
	}
	unused := models.Tag{Name: "李四", Namespace: personTagNamespace}
	if err := database.DB.Create(&unused).Error; err != nil {
		t.Fatal(err)
	}
	// 状态行挂在另一个已有人工标签的视频上，不应被无关删除影响。
	p017AIState(t, video.ID)
	svc := &TagService{}

	// 从未关联过媒体的人物标签：没有状态受影响，不重排。
	if err := svc.DeleteTag(unused.ID); err != nil {
		t.Fatal(err)
	}
	if got := p017Fingerprint(t, video.ID); got != "fp" {
		t.Fatalf("删除无关联的人物标签不应重排，fingerprint=%q", got)
	}
	if err := svc.DeleteTag(person.ID); err != nil {
		t.Fatal(err)
	}
	if got := p017Fingerprint(t, video.ID); got != "" {
		t.Fatalf("删除曾关联媒体的人物标签应重排，fingerprint=%q", got)
	}
}

// META01：来源全是人物分类标签、目标也不在词表时，合并不重排。
func TestTagServiceMETA01MergePersonTagsDoesNotReset(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := p017Video(t, "merge.mp4")
	mk := func(name, ns string) models.Tag {
		tag := models.Tag{Name: name, Namespace: ns}
		if err := database.DB.Create(&tag).Error; err != nil {
			t.Fatal(err)
		}
		return tag
	}
	src, dst := mk("甲", personTagNamespace), mk("乙", personTagNamespace)
	if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", video.ID, src.ID).Error; err != nil {
		t.Fatal(err)
	}
	p017AIState(t, video.ID)
	svc := &TagService{}
	if _, err := svc.MergeTags([]uint{src.ID}, dst.ID); err != nil {
		t.Fatal(err)
	}
	if got := p017Fingerprint(t, video.ID); got != "fp" {
		t.Fatalf("人物合并到人物不应重排，fingerprint=%q", got)
	}
}

// META02：改名硬删了与已转换标签同名的软删行后，撤销返回 tag_name_taken（不是 tag_gone），
// 最近转换列表的 undoable 如实反映。
func TestTagPersonConversionMETA02UndoAfterSoftDeletedRowHardDeletedReturnsNameTaken(t *testing.T) {
	tag, _, _ := conversionFixture(t)
	svc := &TagService{}
	result, err := svc.ConvertTagToPerson(TagPersonConversionRequest{TagID: tag.ID, TagName: tag.Name, CreateNew: true})
	if err != nil {
		t.Fatal(err)
	}
	records, _ := svc.ListTagPersonConversions(10)
	if len(records) != 1 || !records[0].Undoable {
		t.Fatalf("刚转换的记录应可撤销: %+v", records)
	}
	other, err := svc.CreateTag("别的名字", "")
	if err != nil {
		t.Fatal(err)
	}
	// 改成已转换标签的名字：updateTag 会硬删同名软删行。
	if err := svc.UpdateTag(other.ID, tag.Name, "#123456"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UndoTagPersonConversion(result.ConversionID); !errors.Is(err, ErrTagNameTaken) {
		t.Fatalf("应返回 tag_name_taken: %v", err)
	}
	if err := database.DB.First(&models.TagPersonConversion{}, result.ConversionID).Error; err != nil {
		t.Fatal(err)
	}
	records, _ = svc.ListTagPersonConversions(10)
	if records[0].Undoable {
		t.Fatalf("名字被占用后不应可撤销: %+v", records[0])
	}
}

// META02：本次新增的关系只按 INSERT 实际受影响行数记录，已存在的关系不算新增。
func TestTagPersonConversionMETA02InsertNewPersonRelationsUsesRowsAffected(t *testing.T) {
	_, videos, _ := conversionFixture(t)
	person := models.Person{DisplayName: "张三"}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	// 另一个写者刚为第一个视频加了关系。
	if err := database.DB.Exec("INSERT INTO video_people(video_id, person_id, created_at) VALUES (?, ?, CURRENT_TIMESTAMP)", videos[0].ID, person.ID).Error; err != nil {
		t.Fatal(err)
	}
	added, err := insertNewPersonRelations(database.DB, "video_people", "video_id", person.ID, []uint{videos[0].ID, videos[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != videos[1].ID {
		t.Fatalf("只有真正插入的行算新增: %v", added)
	}
}

// META14：使用计数同时给出回收站里的媒体数。
func TestTagServiceMETA14UsageCountsIncludeTrashed(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := models.Tag{Name: "常用"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	v1, v2 := p017Video(t, "1.mp4"), p017Video(t, "2.mp4")
	images := []models.Image{{Name: "a.jpg", Path: "/p017/a.jpg"}, {Name: "b.jpg", Path: "/p017/b.jpg"}}
	if err := database.DB.Create(&images).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{v1.ID, v2.ID} {
		if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", id, tag.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, image := range images {
		if err := database.DB.Exec("INSERT INTO image_tags(image_id, tag_id) VALUES (?, ?)", image.ID, tag.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Delete(&v2).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&images[1]).Error; err != nil {
		t.Fatal(err)
	}
	counts, err := (&TagService{}).GetTagUsageCounts([]uint{tag.ID})
	if err != nil {
		t.Fatal(err)
	}
	if counts[tag.ID] != (TagUsageCount{Videos: 1, Images: 1, TrashedVideos: 1, TrashedImages: 1}) {
		t.Fatalf("计数不符: %+v", counts[tag.ID])
	}
}
