package services

import (
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestSaveAITagLibraryUpdatesCreatesAndPreservesRemovedTagsAsManual(t *testing.T) {
	setupVideoServiceTestDB(t)
	existing := []models.Tag{
		{Name: "动作", Namespace: "行为", Color: "#111111", IsSystem: true, IsActive: true},
		{Name: "站立", Namespace: "姿态", Color: "#222222", IsSystem: true, IsActive: true},
	}
	if err := database.DB.Create(&existing).Error; err != nil {
		t.Fatalf("创建系统标签失败: %v", err)
	}
	video := models.Video{Name: "pending.mp4", Path: "/tmp/tag-library-pending.mp4"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	candidate := models.AITagCandidate{VideoID: video.ID, SuggestedName: "动作", NormalizedName: "动作", Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending}
	state := models.AITaggingState{VideoID: video.ID, Status: models.AITaggingStateStatusCompleted, EvidenceFingerprint: "old"}
	if err := database.DB.Create(&candidate).Error; err != nil {
		t.Fatalf("创建候选失败: %v", err)
	}
	if err := database.DB.Create(&state).Error; err != nil {
		t.Fatalf("创建分析状态失败: %v", err)
	}

	svc := &TagService{}
	saved, err := svc.SaveAITagLibrary([]AITagLibraryInput{
		{ID: existing[0].ID, Namespace: "行为", Name: "激烈动作", Color: "#333333", IsActive: true},
		{Namespace: "服饰", Name: "制服", Color: "#444444", IsActive: true},
	})
	if err != nil {
		t.Fatalf("保存 AI 标签库失败: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("AI 标签库应包含 2 个标签，实际 %d", len(saved))
	}

	var removed models.Tag
	if err := database.DB.First(&removed, existing[1].ID).Error; err != nil {
		t.Fatalf("读取移出标签失败: %v", err)
	}
	if removed.IsSystem || !removed.IsActive || removed.Name != "站立" {
		t.Fatalf("移出标签应保留为普通标签: %+v", removed)
	}
	if err := database.DB.First(&candidate, candidate.ID).Error; err != nil {
		t.Fatalf("读取候选失败: %v", err)
	}
	if candidate.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("旧标签库候选应失效，实际 %s", candidate.Status)
	}
	if err := database.DB.First(&state, state.ID).Error; err != nil {
		t.Fatalf("读取分析状态失败: %v", err)
	}
	if state.Status != models.AITaggingStateStatusPending || state.EvidenceFingerprint != "" {
		t.Fatalf("无正式标签的视频应等待按新标签库重跑: %+v", state)
	}
}

func TestSaveAITagLibraryAllowsEmptyButRejectsDuplicateNames(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &TagService{}
	if saved, err := svc.SaveAITagLibrary(nil); err != nil || len(saved) != 0 {
		t.Fatalf("用户应能保存空 AI 标签库，saved=%v err=%v", saved, err)
	}
	if _, err := svc.SaveAITagLibrary([]AITagLibraryInput{
		{Namespace: "分类A", Name: "重复", IsActive: true},
		{Namespace: "分类B", Name: "重复", IsActive: true},
	}); err == nil {
		t.Fatal("重复标签名应被拒绝")
	}
}

func TestSaveAITagLibraryPersistsUserDefinedCategoryAndInactiveState(t *testing.T) {
	setupVideoServiceTestDB(t)
	saved, err := (&TagService{}).SaveAITagLibrary([]AITagLibraryInput{{
		Namespace: "自定义分类",
		Name:      "用户标签",
		IsActive:  false,
	}})
	if err != nil {
		t.Fatalf("保存自定义 AI 标签失败: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("AI 标签库应包含 1 个标签，实际 %d", len(saved))
	}
	if saved[0].Namespace != "自定义分类" || saved[0].Name != "用户标签" || saved[0].IsActive {
		t.Fatalf("应原样保留用户分类、名称和停用状态: %+v", saved[0])
	}
}

func TestSaveAITagLibraryWithoutChangesKeepsPendingCandidates(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := models.Tag{Name: "动作", Namespace: "行为", Color: "#111111", IsSystem: true, IsActive: true, SortOrder: 1}
	video := models.Video{Name: "pending.mp4", Path: "/tmp/tag-library-unchanged.mp4"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	candidate := models.AITagCandidate{VideoID: video.ID, SuggestedName: tag.Name, NormalizedName: tag.Name, Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending}
	if err := database.DB.Create(&candidate).Error; err != nil {
		t.Fatalf("创建候选失败: %v", err)
	}

	if _, err := (&TagService{}).SaveAITagLibrary([]AITagLibraryInput{{
		ID: tag.ID, Namespace: tag.Namespace, Name: tag.Name, Color: tag.Color, IsActive: true,
	}}); err != nil {
		t.Fatalf("保存未变化标签库失败: %v", err)
	}
	if err := database.DB.First(&candidate, candidate.ID).Error; err != nil {
		t.Fatalf("读取候选失败: %v", err)
	}
	if candidate.Status != models.AITagCandidateStatusPending {
		t.Fatalf("标签库未变化时不应使候选失效，实际 %s", candidate.Status)
	}
}
