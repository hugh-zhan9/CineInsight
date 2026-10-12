package services

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"video-master/database"
	"video-master/models"
)

// 批量加标签保持单条加标签的语义：同条目同标签的待审 AI 候选作废，其他候选不动（视频与图片）。
func TestMetadataBatchEditTagAddSupersedesPendingAICandidates(t *testing.T) {
	service := setupBatchEditTestDB(t)
	tag, other := beTag(t, "海边"), beTag(t, "夜景")
	video := beVideo(t, "beach.mp4", nil)
	image := beImage(t, "beach.jpg", nil)
	pending := models.AITagCandidate{VideoID: video.ID, MatchedTagID: &tag.ID, SuggestedName: tag.Name, NormalizedName: tag.Name, Confidence: "high", Status: models.AITagCandidateStatusPending}
	untouched := models.AITagCandidate{VideoID: video.ID, MatchedTagID: &other.ID, SuggestedName: other.Name, NormalizedName: other.Name, Confidence: "high", Status: models.AITagCandidateStatusPending}
	beMust(t, database.DB.Create(&pending).Error)
	beMust(t, database.DB.Create(&untouched).Error)
	imagePending := models.ImageAITagCandidate{ImageID: image.ID, MatchedTagID: &tag.ID, SuggestedName: tag.Name, Confidence: "high", Status: models.AITagCandidateStatusPending}
	beMust(t, database.DB.Create(&imagePending).Error)

	beApply(t, service, beVideoReq([]uint{video.ID}, MetadataBatchOp{Field: "tag", Action: "add", IDs: []uint{tag.ID}}))
	beApply(t, service, MetadataBatchEditRequest{Kind: BatchEditKindImage, Target: MetadataBatchEditTarget{IDs: []uint{image.ID}},
		Ops: []MetadataBatchOp{{Field: "tag", Action: "add", IDs: []uint{tag.ID}}}})

	var reloaded models.AITagCandidate
	beMust(t, database.DB.First(&reloaded, pending.ID).Error)
	if reloaded.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("video candidate status = %q", reloaded.Status)
	}
	var otherReloaded models.AITagCandidate
	beMust(t, database.DB.First(&otherReloaded, untouched.ID).Error)
	if otherReloaded.Status != models.AITagCandidateStatusPending {
		t.Fatalf("other candidate status = %q", otherReloaded.Status)
	}
	var imageReloaded models.ImageAITagCandidate
	beMust(t, database.DB.First(&imageReloaded, imagePending.ID).Error)
	if imageReloaded.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("image candidate status = %q", imageReloaded.Status)
	}
}

// beManyVideos 一次建 n 条没有年份的视频，返回 ID（升序）。
func beManyVideos(t *testing.T, n int) []uint {
	t.Helper()
	videos := make([]models.Video, 0, n)
	for index := 0; index < n; index++ {
		name := fmt.Sprintf("bulk-%04d.mp4", index)
		videos = append(videos, models.Video{Name: name, Path: "/bulk/" + name, Directory: "/bulk"})
	}
	beMust(t, database.DB.CreateInBatches(&videos, 200).Error)
	ids := make([]uint, 0, n)
	for _, video := range videos {
		ids = append(ids, video.ID)
	}
	return ids
}

// 超过 500 条按批提交：每批之后进度前进 500，全部写完、明细一条不少。
func TestMetadataBatchEditChunksMoreThanFiveHundredTargets(t *testing.T) {
	service := setupBatchEditTestDB(t)
	ids := beManyVideos(t, 1203)
	var progress []int
	service.afterChunk = func(processed int) { progress = append(progress, processed) }
	result := beApply(t, service, beVideoReq(ids, MetadataBatchOp{Field: "year", Action: "set", Value: beFloat(2020)}))
	if fmt.Sprint(progress) != "[500 1000 1203]" {
		t.Fatalf("chunk progress = %v", progress)
	}
	if result.Processed != 1203 || result.ChangedCount != 1203 || beOpChanges(result.Ops)["year:set"] != 1203 {
		t.Fatalf("result = %+v", result)
	}
	if count := beCount(t, &models.Video{}, "year = ?", 2020); count != 1203 {
		t.Fatalf("videos with year = %d", count)
	}
	if count := beCount(t, &models.MetadataBatchEditItem{}, "batch_id = ?", result.BatchID); count != 1203 {
		t.Fatalf("items = %d", count)
	}
	var batch models.MetadataBatchEdit
	beMust(t, database.DB.First(&batch, result.BatchID).Error)
	if batch.ItemCount != 1203 {
		t.Fatalf("item_count = %d", batch.ItemCount)
	}
	if status := service.Status(); status.Running || status.Done != 1203 || status.Total != 1203 || status.LastError != "" {
		t.Fatalf("status = %+v", status)
	}
}

// 取消在批与批之间生效：已提交的批次保留（连同撤销明细），结果报告已完成的条数；之后可以撤销已提交的部分。
func TestMetadataBatchEditCancelKeepsCommittedChunks(t *testing.T) {
	service := setupBatchEditTestDB(t)
	ids := beManyVideos(t, 1100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.afterChunk = func(processed int) {
		if processed == 500 {
			cancel()
		}
	}
	result, err := service.ApplyMetadataBatchEdit(ctx, beVideoReq(ids, MetadataBatchOp{Field: "year", Action: "set", Value: beFloat(2020)}))
	beMust(t, err)
	if !result.Cancelled || result.Processed != 500 || result.ChangedCount != 500 || result.BatchID == 0 {
		t.Fatalf("result = %+v", result)
	}
	if count := beCount(t, &models.Video{}, "year = ?", 2020); count != 500 {
		t.Fatalf("committed videos = %d, want 500", count)
	}
	list, err := service.ListMetadataBatchEdits()
	beMust(t, err)
	if len(list) != 1 || !list[0].Summary.Cancelled || list[0].ItemCount != 500 || !list[0].Undoable {
		t.Fatalf("list = %+v", list)
	}
	service.afterChunk = nil
	undo, err := service.UndoMetadataBatchEdit(context.Background(), result.BatchID)
	beMust(t, err)
	if undo.Restored != 500 || undo.ChangedSince != 0 {
		t.Fatalf("undo = %+v", undo)
	}
	if count := beCount(t, &models.Video{}, "year IS NOT NULL"); count != 0 {
		t.Fatalf("videos with year after undo = %d", count)
	}
}

// 同一时刻只允许一个批量编辑或撤销，第二个返回 batch_edit_busy。
func TestMetadataBatchEditRejectsConcurrentRun(t *testing.T) {
	service := setupBatchEditTestDB(t)
	video := beVideo(t, "busy.mp4", func(v *models.Video) { v.PersonalRating = beFloat(6) })
	earlier := beApply(t, service, beVideoReq([]uint{video.ID}, MetadataBatchOp{Field: "year", Action: "set", Value: beFloat(2000)}))
	_, end, err := service.begin(context.Background(), 0)
	beMust(t, err)
	if _, err := service.ApplyMetadataBatchEdit(context.Background(), beVideoReq([]uint{video.ID}, MetadataBatchOp{Field: "rating", Action: "clear"})); !errors.Is(err, ErrBatchEditBusy) {
		t.Fatalf("apply err = %v", err)
	}
	if _, err := service.UndoMetadataBatchEdit(context.Background(), earlier.BatchID); !errors.Is(err, ErrBatchEditBusy) {
		t.Fatalf("undo err = %v", err)
	}
	end(nil)
	if _, err := service.ApplyMetadataBatchEdit(context.Background(), beVideoReq([]uint{video.ID}, MetadataBatchOp{Field: "rating", Action: "clear"})); err != nil {
		t.Fatalf("apply after release err = %v", err)
	}
}

// 撤销被取消后再撤销：已恢复的明细行已随恢复删掉，重试只处理剩下的，不会把它们算成 changed_since；
// 跑完才写 undone_at，批次的 item_count 不变。
func TestMetadataBatchEditUndoRetryAfterCancelSkipsRestoredRows(t *testing.T) {
	service := setupBatchEditTestDB(t)
	ids := beManyVideos(t, 1100)
	result := beApply(t, service, beVideoReq(ids, MetadataBatchOp{Field: "year", Action: "set", Value: beFloat(2020)}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.afterChunk = func(processed int) {
		if processed == 500 {
			cancel()
		}
	}
	first, err := service.UndoMetadataBatchEdit(ctx, result.BatchID)
	beMust(t, err)
	if !first.Cancelled || first.Restored != 500 || first.ChangedSince != 0 {
		t.Fatalf("first undo = %+v", first)
	}
	if count := beCount(t, &models.MetadataBatchEditItem{}, "batch_id = ?", result.BatchID); count != 600 {
		t.Fatalf("items left after cancelled undo = %d, want 600", count)
	}
	list, err := service.ListMetadataBatchEdits()
	beMust(t, err)
	if len(list) != 1 || list[0].UndoneAt != nil || !list[0].Undoable || list[0].ItemCount != 1100 {
		t.Fatalf("list after cancelled undo = %+v", list)
	}
	service.afterChunk = nil
	retry := beUndo(t, service, result.BatchID)
	if retry.Cancelled || retry.Restored != 600 || retry.ChangedSince != 0 {
		t.Fatalf("retry undo = %+v, want restored 600 changed_since 0", retry)
	}
	if status := service.Status(); status.Done != 600 || status.Total != 600 {
		t.Fatalf("retry progress = %d/%d, want 600/600", status.Done, status.Total)
	}
	if count := beCount(t, &models.Video{}, "year IS NOT NULL"); count != 0 {
		t.Fatalf("videos with year after retry = %d", count)
	}
	var batch models.MetadataBatchEdit
	beMust(t, database.DB.First(&batch, result.BatchID).Error)
	if batch.UndoneAt == nil || batch.ItemCount != 1100 {
		t.Fatalf("batch after retry = %+v", batch)
	}
}

// 批量加标签同样把规则插入的同一关系交还用户（D-RA-04）：本来就有这个标签、这次没改变的条目也算。
func TestMetadataBatchEditTagAddClaimsRuleInsertedTag(t *testing.T) {
	service := setupBatchEditTestDB(t)
	tag := beTag(t, "待整理")
	video := beVideo(t, "rule.mp4", nil)
	rule := models.AutomationRule{Name: "下载目录", MediaKind: "video", Enabled: true, ConditionsJSON: "[]", ActionsJSON: "[]"}
	beMust(t, database.DB.Create(&rule).Error)
	beMust(t, database.DB.Exec("INSERT INTO video_tags (video_id, tag_id) VALUES (?, ?)", video.ID, tag.ID).Error)
	beMust(t, database.DB.Exec("INSERT INTO automation_rule_writes (rule_id, media_kind, media_id, action, target_id, inserted, created_at) VALUES (?, 'video', ?, 'tag', ?, true, CURRENT_TIMESTAMP)", rule.ID, video.ID, tag.ID).Error)

	beApply(t, service, beVideoReq([]uint{video.ID}, MetadataBatchOp{Field: "tag", Action: "add", IDs: []uint{tag.ID}}))

	var write models.AutomationRuleWrite
	beMust(t, database.DB.Where("media_kind = ? AND media_id = ? AND target_id = ?", "video", video.ID, tag.ID).First(&write).Error)
	if write.Inserted {
		t.Fatalf("用户批量加了同一标签后，规则账本应交还用户: %+v", write)
	}
}
