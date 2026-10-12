package services

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"video-master/database"
	"video-master/models"
)

func beUndo(t *testing.T, service *MetadataBatchEditService, batchID uint) *MetadataBatchUndoResult {
	t.Helper()
	result, err := service.UndoMetadataBatchEdit(context.Background(), batchID)
	beMust(t, err)
	return result
}

// 撤销恢复改动前的值；之后又被改过的（条目，字段）跳过并计入 changed_since；撤销过的批次再撤销报错（D-MC-05）。
func TestMetadataBatchEditUndoRestoresAndSkipsChangedSince(t *testing.T) {
	service := setupBatchEditTestDB(t)
	tag := beTag(t, "新标签")
	v1 := beVideo(t, "u1.mp4", func(v *models.Video) { v.Year = beInt(1990); v.PersonalRating = beFloat(5.5) })
	v2 := beVideo(t, "u2.mp4", nil)
	v3 := beVideo(t, "u3.mp4", func(v *models.Video) { v.PersonalRating = beFloat(9) })
	result := beApply(t, service, beVideoReq([]uint{v1.ID, v2.ID, v3.ID},
		MetadataBatchOp{Field: "year", Action: "set", Value: beFloat(2001)},
		MetadataBatchOp{Field: "rating", Action: "set", Value: beFloat(8)},
		MetadataBatchOp{Field: "tag", Action: "add", IDs: []uint{tag.ID}}))
	// 之后的改动：v2 的年份被改成 1999，v3 的标签被手动去掉。
	beMust(t, database.DB.Model(&models.Video{}).Where("id = ?", v2.ID).Update("year", 1999).Error)
	beMust(t, database.DB.Exec("DELETE FROM video_tags WHERE video_id = ?", v3.ID).Error)

	undo := beUndo(t, service, result.BatchID)
	if undo.Restored != 7 || undo.ChangedSince != 2 || undo.Cancelled {
		t.Fatalf("undo = %+v, want restored 7 changed_since 2", undo)
	}
	r1, r2, r3 := beReloadVideo(t, v1.ID), beReloadVideo(t, v2.ID), beReloadVideo(t, v3.ID)
	if r1.Year == nil || *r1.Year != 1990 || r1.PersonalRating == nil || *r1.PersonalRating != 5.5 {
		t.Fatalf("v1 = year %v rating %v", r1.Year, r1.PersonalRating)
	}
	if r2.Year == nil || *r2.Year != 1999 || r2.PersonalRating != nil {
		t.Fatalf("v2 = year %v rating %v（之后改过的年份不该被撤销）", r2.Year, r2.PersonalRating)
	}
	if r3.Year != nil || r3.PersonalRating == nil || *r3.PersonalRating != 9 {
		t.Fatalf("v3 = year %v rating %v", r3.Year, r3.PersonalRating)
	}
	for _, id := range []uint{v1.ID, v2.ID, v3.ID} {
		if tags := beRelIDs(t, "video_tags", "video_id", "tag_id", id); len(tags) != 0 {
			t.Fatalf("video %d tags after undo = %v", id, tags)
		}
	}
	list, err := service.ListMetadataBatchEdits()
	beMust(t, err)
	if len(list) != 1 || list[0].UndoneAt == nil || list[0].Undoable {
		t.Fatalf("list after undo = %+v", list)
	}
	if _, err := service.UndoMetadataBatchEdit(context.Background(), result.BatchID); !errors.Is(err, ErrBatchAlreadyUndone) {
		t.Fatalf("second undo err = %v", err)
	}
	if _, err := service.UndoMetadataBatchEdit(context.Background(), 9999); !errors.Is(err, ErrMetadataBatchMissing) {
		t.Fatalf("missing batch err = %v", err)
	}
}

// 关联类撤销只动本批次加上或去掉的那几个 ID：本来就有的、之后别处加上的都不动。
func TestMetadataBatchEditRelationUndoOnlyTouchesBatchIDs(t *testing.T) {
	service := setupBatchEditTestDB(t)
	tagA, tagB, tagC, tagD := beTag(t, "A"), beTag(t, "B"), beTag(t, "C"), beTag(t, "D")
	keep, gone := bePerson(t, "留下"), bePerson(t, "去掉")
	video := beVideo(t, "rel.mp4", nil)
	beRelate(t, "video_tags", "video_id", "tag_id", video.ID, tagA.ID)
	beRelate(t, "video_tags", "video_id", "tag_id", video.ID, tagD.ID)
	beRelate(t, "video_people", "video_id", "person_id", video.ID, keep.ID)
	beRelate(t, "video_people", "video_id", "person_id", video.ID, gone.ID)
	result := beApply(t, service, beVideoReq([]uint{video.ID},
		MetadataBatchOp{Field: "tag", Action: "add", IDs: []uint{tagA.ID, tagB.ID}},
		MetadataBatchOp{Field: "tag", Action: "remove", IDs: []uint{tagD.ID}},
		MetadataBatchOp{Field: "person", Action: "remove", IDs: []uint{gone.ID}}))
	beRelate(t, "video_tags", "video_id", "tag_id", video.ID, tagC.ID)

	undo := beUndo(t, service, result.BatchID)
	if undo.Restored != 2 || undo.ChangedSince != 0 {
		t.Fatalf("undo = %+v", undo)
	}
	if tags := beRelIDs(t, "video_tags", "video_id", "tag_id", video.ID); !reflect.DeepEqual(tags, []uint{tagA.ID, tagC.ID, tagD.ID}) {
		t.Fatalf("tags after undo = %v, want A C D", tags)
	}
	if people := beRelIDs(t, "video_people", "video_id", "person_id", video.ID); !reflect.DeepEqual(people, []uint{keep.ID, gone.ID}) {
		t.Fatalf("people after undo = %v", people)
	}
}

// 去掉的对象之后又被加回、或已被删除：这一行不再是改后的样子，撤销跳过并计入 changed_since。
func TestMetadataBatchEditRelationUndoSkipsReaddedOrDeletedTargets(t *testing.T) {
	service := setupBatchEditTestDB(t)
	tag, gone := beTag(t, "又加回来"), beTag(t, "被删掉")
	readded := beVideo(t, "readded.mp4", nil)
	deleted := beVideo(t, "deleted.mp4", nil)
	beRelate(t, "video_tags", "video_id", "tag_id", readded.ID, tag.ID)
	beRelate(t, "video_tags", "video_id", "tag_id", deleted.ID, gone.ID)
	first := beApply(t, service, beVideoReq([]uint{readded.ID}, MetadataBatchOp{Field: "tag", Action: "remove", IDs: []uint{tag.ID}}))
	second := beApply(t, service, beVideoReq([]uint{deleted.ID}, MetadataBatchOp{Field: "tag", Action: "remove", IDs: []uint{gone.ID}}))
	beRelate(t, "video_tags", "video_id", "tag_id", readded.ID, tag.ID)
	beMust(t, database.DB.Delete(&models.Tag{}, gone.ID).Error)
	for _, batchID := range []uint{first.BatchID, second.BatchID} {
		if undo := beUndo(t, service, batchID); undo.Restored != 0 || undo.ChangedSince != 1 {
			t.Fatalf("undo batch %d = %+v, want changed_since 1", batchID, undo)
		}
	}
	if tags := beRelIDs(t, "video_tags", "video_id", "tag_id", deleted.ID); len(tags) != 0 {
		t.Fatalf("deleted tag was re-linked: %v", tags)
	}
}

// 加回作品集接在末位，移出后压紧位置；撤销加入时把这次加进去的移出。
func TestMetadataBatchEditCollectionUndoKeepsPositionsCompact(t *testing.T) {
	service := setupBatchEditTestDB(t)
	collection := beCollection(t, "三部曲")
	first, second, third := beVideo(t, "c1.mp4", nil), beVideo(t, "c2.mp4", nil), beVideo(t, "c3.mp4", nil)
	beMust(t, database.DB.Create(&models.CollectionVideo{CollectionID: collection.ID, VideoID: first.ID, Position: 1}).Error)
	beMust(t, database.DB.Create(&models.CollectionVideo{CollectionID: collection.ID, VideoID: second.ID, Position: 2}).Error)
	removed := beApply(t, service, beVideoReq([]uint{first.ID}, MetadataBatchOp{Field: "collection", Action: "remove", IDs: []uint{collection.ID}}))
	added := beApply(t, service, beVideoReq([]uint{third.ID}, MetadataBatchOp{Field: "collection", Action: "add", IDs: []uint{collection.ID}}))
	order := func() []uint {
		var rows []models.CollectionVideo
		beMust(t, database.DB.Where("collection_id = ?", collection.ID).Order("position ASC").Find(&rows).Error)
		ids := []uint{}
		for index, row := range rows {
			if row.Position != index+1 {
				t.Fatalf("positions not compact: %+v", rows)
			}
			ids = append(ids, row.VideoID)
		}
		return ids
	}
	if got := order(); !reflect.DeepEqual(got, []uint{second.ID, third.ID}) {
		t.Fatalf("after edits = %v", got)
	}
	beUndo(t, service, removed.BatchID)
	if got := order(); !reflect.DeepEqual(got, []uint{second.ID, third.ID, first.ID}) {
		t.Fatalf("after undoing removal = %v", got)
	}
	beUndo(t, service, added.BatchID)
	if got := order(); !reflect.DeepEqual(got, []uint{second.ID, first.ID}) {
		t.Fatalf("after undoing addition = %v", got)
	}
}

// 只保留最近 20 次：第 21 次写入时最早的一次连同明细被删掉。
func TestMetadataBatchEditKeepsOnlyLatestTwentyBatches(t *testing.T) {
	service := setupBatchEditTestDB(t)
	video := beVideo(t, "prune.mp4", nil)
	batchIDs := []uint{}
	for index := 0; index < 21; index++ {
		result := beApply(t, service, beVideoReq([]uint{video.ID}, MetadataBatchOp{Field: "year", Action: "set", Value: beFloat(float64(1950 + index))}))
		batchIDs = append(batchIDs, result.BatchID)
		if index == 19 {
			if count := beCount(t, &models.MetadataBatchEdit{}, "1 = 1"); count != 20 {
				t.Fatalf("after 20 batches = %d", count)
			}
		}
	}
	if count := beCount(t, &models.MetadataBatchEdit{}, "1 = 1"); count != 20 {
		t.Fatalf("batches = %d, want 20", count)
	}
	if count := beCount(t, &models.MetadataBatchEdit{}, "id = ?", batchIDs[0]); count != 0 {
		t.Fatal("oldest batch should be pruned")
	}
	if count := beCount(t, &models.MetadataBatchEditItem{}, "batch_id = ?", batchIDs[0]); count != 0 {
		t.Fatalf("oldest batch items = %d", count)
	}
	if count := beCount(t, &models.MetadataBatchEdit{}, "id = ?", batchIDs[1]); count != 1 {
		t.Fatal("second batch should be kept")
	}
	list, err := service.ListMetadataBatchEdits()
	beMust(t, err)
	if len(list) != 20 || list[0].ID != batchIDs[20] || list[19].ID != batchIDs[1] {
		t.Fatalf("list order = first %d last %d", list[0].ID, list[len(list)-1].ID)
	}
	if _, err := service.UndoMetadataBatchEdit(context.Background(), batchIDs[0]); !errors.Is(err, ErrMetadataBatchMissing) {
		t.Fatalf("undo pruned batch err = %v", err)
	}
}

// 图片：标签加 / 去、人物加 / 去、评分设为 / 清空，按图片筛选取目标，撤销后复原。
func TestMetadataBatchEditImageOpsAndUndo(t *testing.T) {
	service := setupBatchEditTestDB(t)
	tag, old := beTag(t, "风景"), beTag(t, "旧标签")
	person, removed := bePerson(t, "同行"), bePerson(t, "路人")
	i1 := beImage(t, "i1.jpg", beFloat(3))
	i2 := beImage(t, "i2.jpg", nil)
	beImage(t, "other.png", beFloat(1))
	beRelate(t, "image_tags", "image_id", "tag_id", i1.ID, old.ID)
	beRelate(t, "image_people", "image_id", "person_id", i2.ID, removed.ID)
	req := MetadataBatchEditRequest{Kind: BatchEditKindImage,
		Target: MetadataBatchEditTarget{ImageFilter: &ImageFilter{Keyword: ".jpg"}},
		Ops: []MetadataBatchOp{
			{Field: "rating", Action: "set", Value: beFloat(4.5)},
			{Field: "tag", Action: "add", IDs: []uint{tag.ID}},
			{Field: "tag", Action: "remove", IDs: []uint{old.ID}},
			{Field: "person", Action: "add", IDs: []uint{person.ID}},
			{Field: "person", Action: "remove", IDs: []uint{removed.ID}},
		}}
	preview, err := service.PreviewMetadataBatchEdit(req)
	beMust(t, err)
	want := map[string]int{"rating:set": 2, "tag:add": 2, "tag:remove": 1, "person:add": 2, "person:remove": 1}
	if preview.TargetCount != 2 || !reflect.DeepEqual(beOpChanges(preview.Ops), want) {
		t.Fatalf("preview = %+v", preview)
	}
	result := beApply(t, service, req)
	for _, image := range []models.Image{i1, i2} {
		var reloaded models.Image
		beMust(t, database.DB.First(&reloaded, image.ID).Error)
		if reloaded.PersonalRating == nil || *reloaded.PersonalRating != 4.5 {
			t.Fatalf("image %d rating = %v", image.ID, reloaded.PersonalRating)
		}
		if tags := beRelIDs(t, "image_tags", "image_id", "tag_id", image.ID); !reflect.DeepEqual(tags, []uint{tag.ID}) {
			t.Fatalf("image %d tags = %v", image.ID, tags)
		}
		if people := beRelIDs(t, "image_people", "image_id", "person_id", image.ID); !reflect.DeepEqual(people, []uint{person.ID}) {
			t.Fatalf("image %d people = %v", image.ID, people)
		}
	}
	if count := beCount(t, &models.Person{}, "id = ?", removed.ID); count != 1 {
		t.Fatal("removed person entity was deleted")
	}
	clear := beApply(t, service, MetadataBatchEditRequest{Kind: BatchEditKindImage, Target: MetadataBatchEditTarget{IDs: []uint{i1.ID}},
		Ops: []MetadataBatchOp{{Field: "rating", Action: "clear"}}})
	beUndo(t, service, clear.BatchID)
	undo := beUndo(t, service, result.BatchID)
	if undo.Restored != 6 || undo.ChangedSince != 0 {
		t.Fatalf("undo = %+v", undo)
	}
	var r1, r2 models.Image
	beMust(t, database.DB.First(&r1, i1.ID).Error)
	beMust(t, database.DB.First(&r2, i2.ID).Error)
	if r1.PersonalRating == nil || *r1.PersonalRating != 3 || r2.PersonalRating != nil {
		t.Fatalf("ratings after undo = %v / %v", r1.PersonalRating, r2.PersonalRating)
	}
	if tags := beRelIDs(t, "image_tags", "image_id", "tag_id", i1.ID); !reflect.DeepEqual(tags, []uint{old.ID}) {
		t.Fatalf("i1 tags after undo = %v", tags)
	}
	if people := beRelIDs(t, "image_people", "image_id", "person_id", i2.ID); !reflect.DeepEqual(people, []uint{removed.ID}) {
		t.Fatalf("i2 people after undo = %v", people)
	}
}
