package services

import (
	"reflect"
	"strconv"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 预览只计实际改变的条目，执行写入与预览一致，且只写真正改变的行（D-MC-04）。
func TestMetadataBatchEditPreviewAndApplyCountOnlyActualChanges(t *testing.T) {
	service := setupBatchEditTestDB(t)
	tagA, tagZ := beTag(t, "A"), beTag(t, "Z")
	person := bePerson(t, "演员")
	collection := beCollection(t, "系列")
	v1 := beVideo(t, "v1.mp4", func(v *models.Video) { v.Year = beInt(2001); v.PersonalRating = beFloat(7) })
	v2 := beVideo(t, "v2.mp4", nil)
	v3 := beVideo(t, "v3.mp4", func(v *models.Video) { v.Year = beInt(2005) })
	beRelate(t, "video_tags", "video_id", "tag_id", v1.ID, tagA.ID)
	beRelate(t, "video_tags", "video_id", "tag_id", v3.ID, tagZ.ID)
	beRelate(t, "video_people", "video_id", "person_id", v1.ID, person.ID)
	beMust(t, database.DB.Create(&models.CollectionVideo{CollectionID: collection.ID, VideoID: v3.ID, Position: 1}).Error)

	req := beVideoReq([]uint{v1.ID, v2.ID, v3.ID, 9999},
		MetadataBatchOp{Field: "year", Action: "set", Value: beFloat(2001)},
		MetadataBatchOp{Field: "rating", Action: "clear"},
		MetadataBatchOp{Field: "tag", Action: "add", IDs: []uint{tagA.ID}},
		MetadataBatchOp{Field: "tag", Action: "remove", IDs: []uint{tagZ.ID}},
		MetadataBatchOp{Field: "person", Action: "remove", IDs: []uint{person.ID}},
		MetadataBatchOp{Field: "collection", Action: "add", IDs: []uint{collection.ID}},
	)
	want := map[string]int{"year:set": 2, "rating:clear": 1, "tag:add": 2, "tag:remove": 1, "person:remove": 1, "collection:add": 2}
	preview, err := service.PreviewMetadataBatchEdit(req)
	beMust(t, err)
	if preview.TargetCount != 3 || preview.ChangedCount != 3 {
		t.Fatalf("preview target=%d changed=%d, want 3/3（不存在的 ID 不算）", preview.TargetCount, preview.ChangedCount)
	}
	if got := beOpChanges(preview.Ops); !reflect.DeepEqual(got, want) {
		t.Fatalf("preview changes = %v, want %v", got, want)
	}
	if count := beCount(t, &models.MetadataBatchEdit{}, "1 = 1"); count != 0 {
		t.Fatalf("预览不应写记录, got %d", count)
	}

	result := beApply(t, service, req)
	if got := beOpChanges(result.Ops); !reflect.DeepEqual(got, want) {
		t.Fatalf("apply changes = %v, want %v", got, want)
	}
	if result.BatchID == 0 || result.Processed != 3 || result.ChangedCount != 3 || result.Cancelled {
		t.Fatalf("result = %+v", result)
	}
	for _, id := range []uint{v1.ID, v2.ID, v3.ID} {
		video := beReloadVideo(t, id)
		if video.Year == nil || *video.Year != 2001 || video.PersonalRating != nil {
			t.Fatalf("video %d year=%v rating=%v", id, video.Year, video.PersonalRating)
		}
		if tags := beRelIDs(t, "video_tags", "video_id", "tag_id", id); !reflect.DeepEqual(tags, []uint{tagA.ID}) {
			t.Fatalf("video %d tags = %v", id, tags)
		}
		if people := beRelIDs(t, "video_people", "video_id", "person_id", id); len(people) != 0 {
			t.Fatalf("video %d people = %v", id, people)
		}
	}
	var positions []models.CollectionVideo
	beMust(t, database.DB.Where("collection_id = ?", collection.ID).Order("position ASC").Find(&positions).Error)
	if len(positions) != 3 || positions[0].VideoID != v3.ID || positions[1].VideoID != v1.ID || positions[2].VideoID != v2.ID || positions[2].Position != 3 {
		t.Fatalf("collection positions = %+v", positions)
	}
	// 每个实际改变的（条目，字段）一行：v1 年份不变、评分清空、标签不变、去人物、加作品集；
	// v2 年份、标签、作品集；v3 年份、标签（加 A 去 Z 合成一行）。
	var items []models.MetadataBatchEditItem
	beMust(t, database.DB.Where("batch_id = ?", result.BatchID).Order("media_id ASC, field ASC").Find(&items).Error)
	label := map[uint]string{v1.ID: "1", v2.ID: "2", v3.ID: "3"}
	got := map[string]string{}
	for _, item := range items {
		got[item.Field+"@"+label[item.MediaID]] = item.BeforeJSON + "→" + item.AfterJSON
	}
	wantItems := map[string]string{
		"rating@1": "7→null", "person@1": "[" + beID(person.ID) + "]→[]", "collection@1": "[]→[" + beID(collection.ID) + "]",
		"year@2": "null→2001", "tag@2": "[]→[" + beID(tagA.ID) + "]", "collection@2": "[]→[" + beID(collection.ID) + "]",
		"year@3": "2005→2001", "tag@3": "[" + beID(tagZ.ID) + "]→[" + beID(tagA.ID) + "]",
	}
	if !reflect.DeepEqual(got, wantItems) {
		t.Fatalf("items = %v\nwant %v", got, wantItems)
	}
	var batch models.MetadataBatchEdit
	beMust(t, database.DB.First(&batch, result.BatchID).Error)
	if batch.ItemCount != len(items) || batch.Kind != "video" {
		t.Fatalf("batch = %+v", batch)
	}
}

func beID(value uint) string { return strconv.FormatUint(uint64(value), 10) }

// 已经是目标值的条目一行都不写：updated_at 不动、没有撤销明细、也不留批次记录。
func TestMetadataBatchEditLeavesUnchangedRowsUntouched(t *testing.T) {
	service := setupBatchEditTestDB(t)
	tag := beTag(t, "A")
	video := beVideo(t, "same.mp4", func(v *models.Video) { v.Year = beInt(1999) })
	beRelate(t, "video_tags", "video_id", "tag_id", video.ID, tag.ID)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	beMust(t, database.DB.Model(&models.Video{}).Where("id = ?", video.ID).UpdateColumn("updated_at", old).Error)
	result := beApply(t, service, beVideoReq([]uint{video.ID},
		MetadataBatchOp{Field: "year", Action: "set", Value: beFloat(1999)},
		MetadataBatchOp{Field: "rating", Action: "clear"},
		MetadataBatchOp{Field: "tag", Action: "add", IDs: []uint{tag.ID}}))
	if result.BatchID != 0 || result.ChangedCount != 0 || result.Processed != 1 {
		t.Fatalf("result = %+v", result)
	}
	if reloaded := beReloadVideo(t, video.ID); !reloaded.UpdatedAt.Equal(old) {
		t.Fatalf("updated_at moved: %v -> %v", old, reloaded.UpdatedAt)
	}
	if count := beCount(t, &models.MetadataBatchEdit{}, "1 = 1"); count != 0 {
		t.Fatalf("没有改变时不应留批次记录, got %d", count)
	}
}

// 「当前筛选的全部结果」在执行时重新解析：预览之后新进入筛选的条目也会被改。
func TestMetadataBatchEditFilterTargetIsResolvedAtApplyTime(t *testing.T) {
	service := setupBatchEditTestDB(t)
	tag := beTag(t, "待整理")
	matching := []models.Video{beVideo(t, "f1.mp4", nil), beVideo(t, "f2.mp4", nil)}
	other := beVideo(t, "other.mp4", nil)
	stale := beVideo(t, "stale.mp4", func(v *models.Video) { v.IsStale = true })
	for _, video := range append(matching, stale) {
		beRelate(t, "video_tags", "video_id", "tag_id", video.ID, tag.ID)
	}
	req := MetadataBatchEditRequest{Kind: BatchEditKindVideo,
		Target: MetadataBatchEditTarget{Filter: &LibraryFilter{TagIDs: []uint{tag.ID}}},
		Ops:    []MetadataBatchOp{{Field: "year", Action: "set", Value: beFloat(1988)}}}
	preview, err := service.PreviewMetadataBatchEdit(req)
	beMust(t, err)
	if preview.TargetCount != 2 {
		t.Fatalf("preview target = %d, want 2（失效记录不在默认范围）", preview.TargetCount)
	}
	late := beVideo(t, "late.mp4", nil)
	beRelate(t, "video_tags", "video_id", "tag_id", late.ID, tag.ID)
	result := beApply(t, service, req)
	if result.TargetCount != 3 || result.ChangedCount != 3 {
		t.Fatalf("apply result = %+v, want 3 targets", result)
	}
	for _, id := range []uint{matching[0].ID, matching[1].ID, late.ID} {
		if video := beReloadVideo(t, id); video.Year == nil || *video.Year != 1988 {
			t.Fatalf("video %d year = %v", id, video.Year)
		}
	}
	for _, id := range []uint{other.ID, stale.ID} {
		if video := beReloadVideo(t, id); video.Year != nil {
			t.Fatalf("video %d outside the filter changed: %v", id, *video.Year)
		}
	}
}

// 批量去掉人物关联不删除人物本身，即使那是这个人最后一条关联（不同于详情编辑，D-MC-04）。
func TestMetadataBatchEditPersonRemovalKeepsPerson(t *testing.T) {
	service := setupBatchEditTestDB(t)
	person := bePerson(t, "只演过一部")
	video := beVideo(t, "only.mp4", nil)
	beRelate(t, "video_people", "video_id", "person_id", video.ID, person.ID)
	result := beApply(t, service, beVideoReq([]uint{video.ID}, MetadataBatchOp{Field: "person", Action: "remove", IDs: []uint{person.ID}}))
	if beOpChanges(result.Ops)["person:remove"] != 1 {
		t.Fatalf("result = %+v", result)
	}
	if people := beRelIDs(t, "video_people", "video_id", "person_id", video.ID); len(people) != 0 {
		t.Fatalf("relation still there: %v", people)
	}
	if count := beCount(t, &models.Person{}, "id = ?", person.ID); count != 1 {
		t.Fatal("批量去人物关联把人物删掉了")
	}
}

// 批量加人物与单条从人物页加同一语义：这批目标上人脸链路的写入记录被释放。
func TestMetadataBatchEditPersonAddReleasesFaceRelationWrites(t *testing.T) {
	service := setupBatchEditTestDB(t)
	person := bePerson(t, "人脸认出来的")
	video := beVideo(t, "face.mp4", nil)
	beRelate(t, "video_people", "video_id", "person_id", video.ID, person.ID)
	beMust(t, database.DB.Create(&models.FaceRelationWrite{PersonID: person.ID, MediaKind: models.FaceMediaKindVideo, MediaID: video.ID, ClusterID: 7}).Error)
	beApply(t, service, beVideoReq([]uint{video.ID}, MetadataBatchOp{Field: "person", Action: "add", IDs: []uint{person.ID}}))
	if count := beCount(t, &models.FaceRelationWrite{}, "person_id = ?", person.ID); count != 0 {
		t.Fatalf("face relation writes = %d, want released", count)
	}
}
