package services

import (
	"context"
	"testing"
	"video-master/database"
	"video-master/models"
)

// seedImageAITagCandidate 直接构造一个待审候选，绕开 AI 调用。
func seedImageAITagCandidate(t *testing.T, imageID uint, tag models.Tag, confidence string) models.ImageAITagCandidate {
	t.Helper()
	tagID := tag.ID
	candidate := models.ImageAITagCandidate{
		ImageID:        imageID,
		SuggestedName:  tag.Name,
		NormalizedName: normalizeAITagName(tag.Name),
		MatchedTagID:   &tagID,
		Confidence:     confidence,
		Status:         models.AITagCandidateStatusPending,
	}
	if err := database.DB.Omit("Image", "MatchedTag").Create(&candidate).Error; err != nil {
		t.Fatalf("构造候选失败: %v", err)
	}
	return candidate
}

func imageHasTag(t *testing.T, imageID, tagID uint) bool {
	t.Helper()
	var count int64
	if err := database.DB.Table("image_tags").
		Where("image_id = ? AND tag_id = ?", imageID, tagID).Count(&count).Error; err != nil {
		t.Fatalf("查询 image_tags 失败: %v", err)
	}
	return count > 0
}

func newImageAITaggingReviewTestService(t *testing.T) *ImageAITaggingService {
	t.Helper()
	return newImageAITaggingTestService(t, imageTaggingClientFunc(
		func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
			t.Fatal("审阅路径不应调用 AI")
			return nil, nil
		}))
}

// TestImageAITagApproveWritesOfficialTag 钉住核心验收：接受候选后标签真的挂到图片上，
// 并留下审批记录。
func TestImageAITagApproveWritesOfficialTag(t *testing.T) {
	svc := newImageAITaggingReviewTestService(t)
	library := imageAITaggingTestLibrary(t, "海边")
	img := imageAITaggingTestImage(t, "heic")
	candidate := seedImageAITagCandidate(t, img.ID, library[0], models.AITagConfidenceHigh)

	item, err := svc.ApproveImageAITagCandidate(candidate.ID)
	if err != nil {
		t.Fatalf("接受候选失败: %v", err)
	}
	if item.Status != models.AITagCandidateStatusApproved {
		t.Fatalf("候选状态不符: %+v", item)
	}
	if !imageHasTag(t, img.ID, library[0].ID) {
		t.Fatal("接受后标签未挂到图片上")
	}
	var approvalCount int64
	if err := database.DB.Model(&models.ImageAITagApprovalRecord{}).
		Where("image_id = ? AND tag_id = ?", img.ID, library[0].ID).Count(&approvalCount).Error; err != nil {
		t.Fatalf("查询审批记录失败: %v", err)
	}
	if approvalCount != 1 {
		t.Fatalf("审批记录数 = %d", approvalCount)
	}

	// 接受过的标签不算手工标签：同图的第二个候选仍应能正常接受。
	second := imageAITaggingTestLibrary(t, "日落")
	next := seedImageAITagCandidate(t, img.ID, second[0], models.AITagConfidenceHigh)
	if _, err := svc.ApproveImageAITagCandidate(next.ID); err != nil {
		t.Fatalf("第二个候选应可接受: %v", err)
	}
	if !imageHasTag(t, img.ID, second[0].ID) {
		t.Fatal("第二个标签未挂上")
	}
}

// TestImageAITagApproveSupersedesWhenManuallyTagged 钉住"人的判断优先"：
// 已有手工标签时接受候选不写入标签，而是把该图待审候选整体作废。
func TestImageAITagApproveSupersedesWhenManuallyTagged(t *testing.T) {
	svc := newImageAITaggingReviewTestService(t)
	library := imageAITaggingTestLibrary(t, "海边", "日落")
	img := imageAITaggingTestImage(t, "heic")

	manual := models.Tag{Name: "我自己打的"}
	if err := database.DB.Create(&manual).Error; err != nil {
		t.Fatalf("创建手工标签失败: %v", err)
	}
	if err := database.DB.Exec("INSERT INTO image_tags(image_id, tag_id) VALUES (?, ?)", img.ID, manual.ID).Error; err != nil {
		t.Fatalf("关联手工标签失败: %v", err)
	}

	first := seedImageAITagCandidate(t, img.ID, library[0], models.AITagConfidenceHigh)
	seedImageAITagCandidate(t, img.ID, library[1], models.AITagConfidenceHigh)

	item, err := svc.ApproveImageAITagCandidate(first.ID)
	if err != nil {
		t.Fatalf("接受应成功返回 superseded: %v", err)
	}
	if item.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("应置为 superseded: %+v", item)
	}
	if imageHasTag(t, img.ID, library[0].ID) {
		t.Fatal("已手工打标时不应写入 AI 标签")
	}
	var pending int64
	if err := database.DB.Model(&models.ImageAITagCandidate{}).
		Where("image_id = ? AND status = ?", img.ID, models.AITagCandidateStatusPending).
		Count(&pending).Error; err != nil {
		t.Fatalf("统计待审候选失败: %v", err)
	}
	if pending != 0 {
		t.Fatalf("该图待审候选应被整体作废，剩余 %d", pending)
	}
}

// TestImageAITagApproveRejectsIneligibleCandidates 覆盖不可接受的输入。
func TestImageAITagApproveRejectsIneligibleCandidates(t *testing.T) {
	svc := newImageAITaggingReviewTestService(t)
	library := imageAITaggingTestLibrary(t, "海边", "日落")
	img := imageAITaggingTestImage(t, "heic")

	// low 置信度不可接受。注意用不同标签：一张图同一个标签只允许有一条待审候选
	// （部分唯一索引），同名两条是生产代码造不出来的状态。
	low := seedImageAITagCandidate(t, img.ID, library[1], models.AITagConfidenceLow)
	if _, err := svc.ApproveImageAITagCandidate(low.ID); err == nil {
		t.Fatal("low 置信度候选不应可接受")
	}

	// 重复接受同一候选：第二次必须失败，且不产生第二条审批记录。
	ok := seedImageAITagCandidate(t, img.ID, library[0], models.AITagConfidenceHigh)
	if _, err := svc.ApproveImageAITagCandidate(ok.ID); err != nil {
		t.Fatalf("首次接受失败: %v", err)
	}
	if _, err := svc.ApproveImageAITagCandidate(ok.ID); err == nil {
		t.Fatal("重复接受应失败")
	}
	var approvalCount int64
	if err := database.DB.Model(&models.ImageAITagApprovalRecord{}).Count(&approvalCount).Error; err != nil {
		t.Fatalf("统计审批记录失败: %v", err)
	}
	if approvalCount != 1 {
		t.Fatalf("审批记录应只有一条，实际 %d", approvalCount)
	}
}

// TestImageAITagApproveRejectsWhenTagLeftLibrary 钉住候选产生后标签被停用/移出词表的情况。
func TestImageAITagApproveRejectsWhenTagLeftLibrary(t *testing.T) {
	svc := newImageAITaggingReviewTestService(t)
	library := imageAITaggingTestLibrary(t, "海边")
	img := imageAITaggingTestImage(t, "heic")
	candidate := seedImageAITagCandidate(t, img.ID, library[0], models.AITagConfidenceHigh)

	if err := database.DB.Model(&models.Tag{}).Where("id = ?", library[0].ID).
		Update("is_active", false).Error; err != nil {
		t.Fatalf("停用标签失败: %v", err)
	}
	if _, err := svc.ApproveImageAITagCandidate(candidate.ID); err == nil {
		t.Fatal("标签已停用时不应可接受")
	}
	if imageHasTag(t, img.ID, library[0].ID) {
		t.Fatal("停用标签不应被写入")
	}
}

// TestImageAITagApproveRejectsSoftDeletedImage 钉住软删图片拒绝审批。
func TestImageAITagApproveRejectsSoftDeletedImage(t *testing.T) {
	svc := newImageAITaggingReviewTestService(t)
	library := imageAITaggingTestLibrary(t, "海边")
	img := imageAITaggingTestImage(t, "heic")
	candidate := seedImageAITagCandidate(t, img.ID, library[0], models.AITagConfidenceHigh)

	if err := database.DB.Delete(&models.Image{}, img.ID).Error; err != nil {
		t.Fatalf("软删图片失败: %v", err)
	}
	if _, err := svc.ApproveImageAITagCandidate(candidate.ID); err == nil {
		t.Fatal("软删图片的候选不应可接受")
	}
	if imageHasTag(t, img.ID, library[0].ID) {
		t.Fatal("软删图片不应被写入标签")
	}
}

// TestImageAITagRejectRemovesFromPending 覆盖单条拒绝与整图批量拒绝。
func TestImageAITagRejectRemovesFromPending(t *testing.T) {
	svc := newImageAITaggingReviewTestService(t)
	library := imageAITaggingTestLibrary(t, "海边", "日落", "雪山")
	img := imageAITaggingTestImage(t, "heic")
	first := seedImageAITagCandidate(t, img.ID, library[0], models.AITagConfidenceHigh)
	seedImageAITagCandidate(t, img.ID, library[1], models.AITagConfidenceMedium)
	seedImageAITagCandidate(t, img.ID, library[2], models.AITagConfidenceHigh)

	if err := svc.RejectImageAITagCandidate(first.ID); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	if err := svc.RejectImageAITagCandidate(first.ID); err == nil {
		t.Fatal("重复拒绝应失败")
	}
	items, err := svc.ListImageAITagCandidates(img.ID, "", "")
	if err != nil {
		t.Fatalf("列出候选失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("待审候选应剩 2 条，实际 %d", len(items))
	}

	rejected, err := svc.RejectPendingImageAITagCandidatesByImage(img.ID)
	if err != nil {
		t.Fatalf("批量拒绝失败: %v", err)
	}
	if rejected != 2 {
		t.Fatalf("批量拒绝条数 = %d", rejected)
	}
	items, err = svc.ListImageAITagCandidates(img.ID, "", "")
	if err != nil {
		t.Fatalf("列出候选失败: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("待审候选应清空，实际 %d", len(items))
	}
	// 拒绝不得产生任何标签关联。
	var linkCount int64
	if err := database.DB.Table("image_tags").Where("image_id = ?", img.ID).Count(&linkCount).Error; err != nil {
		t.Fatalf("统计标签关联失败: %v", err)
	}
	if linkCount != 0 {
		t.Fatalf("拒绝不应写入标签，实际 %d 条关联", linkCount)
	}
}

// TestImageAITagListFiltersAndSummary 覆盖筛选与待审计数。
func TestImageAITagListFiltersAndSummary(t *testing.T) {
	svc := newImageAITaggingReviewTestService(t)
	library := imageAITaggingTestLibrary(t, "海边", "日落")
	first := imageAITaggingTestImage(t, "heic")
	second := imageAITaggingTestImage(t, "heic")
	seedImageAITagCandidate(t, first.ID, library[0], models.AITagConfidenceHigh)
	seedImageAITagCandidate(t, first.ID, library[1], models.AITagConfidenceMedium)
	seedImageAITagCandidate(t, second.ID, library[0], models.AITagConfidenceHigh)

	all, err := svc.ListImageAITagCandidates(0, "", "")
	if err != nil {
		t.Fatalf("列出全部候选失败: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("全部待审候选 = %d", len(all))
	}
	if all[0].Image == nil || all[0].Image.ID == 0 {
		t.Fatalf("候选应预载图片信息: %+v", all[0])
	}

	high, err := svc.ListImageAITagCandidates(0, models.AITagConfidenceHigh, "")
	if err != nil {
		t.Fatalf("按置信度筛选失败: %v", err)
	}
	if len(high) != 2 {
		t.Fatalf("high 置信度候选 = %d", len(high))
	}

	byImage, err := svc.ListImageAITagCandidates(second.ID, "", "")
	if err != nil {
		t.Fatalf("按图片筛选失败: %v", err)
	}
	if len(byImage) != 1 || byImage[0].ImageID != second.ID {
		t.Fatalf("按图片筛选结果不符: %+v", byImage)
	}

	summary, err := svc.GetImageAITaggingSummary()
	if err != nil {
		t.Fatalf("获取汇总失败: %v", err)
	}
	if summary.Pending != 3 || summary.PendingImages != 2 {
		t.Fatalf("汇总不符: %+v", summary)
	}
	if !summary.ConfigAvailable {
		t.Fatal("测试配置下 ConfigAvailable 应为真")
	}
}

// TestImageAITagCandidatesFollowTagLibraryChanges 钉住标签库变更对图片候选的影响：
// 重命名会作废受影响的待审候选（而不是就地改名，那会撞上部分唯一索引），
// 移出词表/停用同样作废。视频侧只做了自己那半边，图片侧漏掉过。
func TestImageAITagCandidatesFollowTagLibraryChanges(t *testing.T) {
	svc := newImageAITaggingReviewTestService(t)
	library := imageAITaggingTestLibrary(t, "海边", "日落")
	img := imageAITaggingTestImage(t, "heic")
	renamed := seedImageAITagCandidate(t, img.ID, library[0], models.AITagConfidenceHigh)
	dropped := seedImageAITagCandidate(t, img.ID, library[1], models.AITagConfidenceHigh)

	tagSvc := &TagService{}
	// 把「海边」改名，并且只提交它——「日落」因此被移出词表。
	if _, err := tagSvc.SaveAITagLibrary([]AITagLibraryInput{
		{ID: library[0].ID, Name: "海滨", Namespace: "场景", IsActive: true},
	}); err != nil {
		t.Fatalf("保存标签库失败: %v", err)
	}

	var renamedRow, droppedRow models.ImageAITagCandidate
	if err := database.DB.First(&renamedRow, renamed.ID).Error; err != nil {
		t.Fatalf("读取候选失败: %v", err)
	}
	if err := database.DB.First(&droppedRow, dropped.ID).Error; err != nil {
		t.Fatalf("读取候选失败: %v", err)
	}
	if renamedRow.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("改名的标签，其待审候选应作废: %+v", renamedRow)
	}
	if droppedRow.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("移出词表的标签，其待审候选应作废: %+v", droppedRow)
	}

	items, err := svc.ListImageAITagCandidates(img.ID, "", "")
	if err != nil {
		t.Fatalf("列出候选失败: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("待审列表应清空，实际 %+v", items)
	}
}
