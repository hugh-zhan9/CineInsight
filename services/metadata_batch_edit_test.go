package services

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// 批量编辑与撤销（D-MC-04 / D-MC-05）的服务测试，前缀 TestMetadataBatchEdit。

func setupBatchEditTestDB(t *testing.T) *MetadataBatchEditService {
	t.Helper()
	database.DB = dbtest.Open(t)
	beMust(t, database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2.0}).Error)
	return NewMetadataBatchEditService(t.TempDir())
}

func beMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func beFloat(value float64) *float64 { return &value }
func beInt(value int) *int           { return &value }

func beVideo(t *testing.T, name string, mutate func(*models.Video)) models.Video {
	t.Helper()
	video := models.Video{Name: name, Path: "/batch/" + name, Directory: "/batch"}
	if mutate != nil {
		mutate(&video)
	}
	beMust(t, database.DB.Create(&video).Error)
	return video
}

func beImage(t *testing.T, name string, rating *float64) models.Image {
	t.Helper()
	image := models.Image{Name: name, Path: "/batch-images/" + name, Directory: "/batch-images", PersonalRating: rating}
	beMust(t, database.DB.Create(&image).Error)
	return image
}

func beTag(t *testing.T, name string) models.Tag {
	t.Helper()
	tag := models.Tag{Name: name}
	beMust(t, database.DB.Create(&tag).Error)
	return tag
}

func bePerson(t *testing.T, name string) models.Person {
	t.Helper()
	person := models.Person{DisplayName: name}
	beMust(t, database.DB.Create(&person).Error)
	return person
}

func beCollection(t *testing.T, name string) models.MediaCollection {
	t.Helper()
	collection := models.MediaCollection{Name: name, NormalizedName: name}
	beMust(t, database.DB.Create(&collection).Error)
	return collection
}

func beRelate(t *testing.T, table, mediaColumn, relColumn string, mediaID, relID uint) {
	t.Helper()
	beMust(t, database.DB.Exec(fmt.Sprintf("INSERT INTO %s(%s, %s) VALUES (?, ?)", table, mediaColumn, relColumn), mediaID, relID).Error)
}

func beRelIDs(t *testing.T, table, mediaColumn, relColumn string, mediaID uint) []uint {
	t.Helper()
	var ids []uint
	beMust(t, database.DB.Table(table).Where(mediaColumn+" = ?", mediaID).Order(relColumn+" ASC").Pluck(relColumn, &ids).Error)
	return ids
}

func beReloadVideo(t *testing.T, id uint) models.Video {
	t.Helper()
	var video models.Video
	beMust(t, database.DB.Unscoped().First(&video, id).Error)
	return video
}

func beVideoReq(ids []uint, ops ...MetadataBatchOp) MetadataBatchEditRequest {
	return MetadataBatchEditRequest{Kind: BatchEditKindVideo, Target: MetadataBatchEditTarget{IDs: ids}, Ops: ops}
}

func beApply(t *testing.T, service *MetadataBatchEditService, req MetadataBatchEditRequest) *MetadataBatchEditResult {
	t.Helper()
	result, err := service.ApplyMetadataBatchEdit(context.Background(), req)
	beMust(t, err)
	return result
}

func beOpChanges(ops []MetadataBatchOpCount) map[string]int {
	counts := map[string]int{}
	for _, op := range ops {
		counts[op.Field+":"+op.Action] = op.Changes
	}
	return counts
}

func beCount(t *testing.T, model any, where string, args ...any) int64 {
	t.Helper()
	var count int64
	beMust(t, database.DB.Model(model).Where(where, args...).Count(&count).Error)
	return count
}

func TestMetadataBatchEditRejectsInvalidRequests(t *testing.T) {
	service := setupBatchEditTestDB(t)
	video := beVideo(t, "a.mp4", nil)
	tag := beTag(t, "剧情")
	auto := models.Tag{Name: "短视频", AutomaticKind: shortVideoAutomaticTagKind}
	beMust(t, database.DB.Create(&auto).Error)
	ids := []uint{video.ID}
	cases := map[string]MetadataBatchEditRequest{
		"没有操作":       beVideoReq(ids),
		"同字段设为又清空":   beVideoReq(ids, MetadataBatchOp{Field: "year", Action: "set", Value: beFloat(2001)}, MetadataBatchOp{Field: "year", Action: "clear"}),
		"同一标签加又去":    beVideoReq(ids, MetadataBatchOp{Field: "tag", Action: "add", IDs: []uint{tag.ID}}, MetadataBatchOp{Field: "tag", Action: "remove", IDs: []uint{tag.ID}}),
		"标签不存在":      beVideoReq(ids, MetadataBatchOp{Field: "tag", Action: "add", IDs: []uint{9999}}),
		"自动标签":       beVideoReq(ids, MetadataBatchOp{Field: "tag", Action: "add", IDs: []uint{auto.ID}}),
		"人物不存在":      beVideoReq(ids, MetadataBatchOp{Field: "person", Action: "remove", IDs: []uint{9999}}),
		"作品集不存在":     beVideoReq(ids, MetadataBatchOp{Field: "collection", Action: "add", IDs: []uint{9999}}),
		"年份太早":       beVideoReq(ids, MetadataBatchOp{Field: "year", Action: "set", Value: beFloat(1869)}),
		"年份太晚":       beVideoReq(ids, MetadataBatchOp{Field: "year", Action: "set", Value: beFloat(2101)}),
		"年份不是整数":     beVideoReq(ids, MetadataBatchOp{Field: "year", Action: "set", Value: beFloat(2000.5)}),
		"评分超过 10":    beVideoReq(ids, MetadataBatchOp{Field: "rating", Action: "set", Value: beFloat(10.5)}),
		"评分不是半分":     beVideoReq(ids, MetadataBatchOp{Field: "rating", Action: "set", Value: beFloat(3.3)}),
		"设为没有值":      beVideoReq(ids, MetadataBatchOp{Field: "rating", Action: "set"}),
		"空的关联":       beVideoReq(ids, MetadataBatchOp{Field: "tag", Action: "add"}),
		"未知动作":       beVideoReq(ids, MetadataBatchOp{Field: "year", Action: "add", IDs: []uint{1}}),
		"图片没有年份":     {Kind: "image", Target: MetadataBatchEditTarget{IDs: ids}, Ops: []MetadataBatchOp{{Field: "year", Action: "clear"}}},
		"图片没有作品集":    {Kind: "image", Target: MetadataBatchEditTarget{IDs: ids}, Ops: []MetadataBatchOp{{Field: "collection", Action: "add", IDs: []uint{1}}}},
		"未知类型":       {Kind: "audio", Target: MetadataBatchEditTarget{IDs: ids}, Ops: []MetadataBatchOp{{Field: "rating", Action: "clear"}}},
		"范围既有所选又有筛选": {Kind: "video", Target: MetadataBatchEditTarget{IDs: ids, Filter: &LibraryFilter{}}, Ops: []MetadataBatchOp{{Field: "rating", Action: "clear"}}},
		"没有范围":       {Kind: "video", Ops: []MetadataBatchOp{{Field: "rating", Action: "clear"}}},
		"视频用了图片筛选":   {Kind: "video", Target: MetadataBatchEditTarget{ImageFilter: &ImageFilter{}}, Ops: []MetadataBatchOp{{Field: "rating", Action: "clear"}}},
	}
	for name, req := range cases {
		if _, err := service.PreviewMetadataBatchEdit(req); !errors.Is(err, ErrInvalidBatchEdit) {
			t.Errorf("%s: preview err = %v, want invalid_batch_edit", name, err)
		}
		if _, err := service.ApplyMetadataBatchEdit(context.Background(), req); !errors.Is(err, ErrInvalidBatchEdit) {
			t.Errorf("%s: apply err = %v, want invalid_batch_edit", name, err)
		}
	}
	if count := beCount(t, &models.MetadataBatchEdit{}, "1 = 1"); count != 0 {
		t.Fatalf("invalid requests left %d batch records", count)
	}
}
