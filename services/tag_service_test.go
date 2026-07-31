package services

import (
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestMergeTagsUnionsAssociationsAndSoftDeletesSources(t *testing.T) {
	setupVideoServiceTestDB(t)
	target := models.Tag{Name: "旅行", Color: "#111111", IsActive: true}
	sourceA := models.Tag{Name: "旅游", Color: "#222222", IsActive: true}
	sourceB := models.Tag{Name: "出游", Color: "#333333", IsActive: true}
	if err := database.DB.Create(&[]*models.Tag{&target, &sourceA, &sourceB}).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	videoA := models.Video{Name: "a.mp4", Path: "/tmp/merge-tag-a.mp4"}
	videoB := models.Video{Name: "b.mp4", Path: "/tmp/merge-tag-b.mp4"}
	if err := database.DB.Create(&[]*models.Video{&videoA, &videoB}).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := database.DB.Model(&videoA).Association("Tags").Append(&target, &sourceA); err != nil {
		t.Fatalf("关联视频A标签失败: %v", err)
	}
	if err := database.DB.Model(&videoB).Association("Tags").Append(&sourceB); err != nil {
		t.Fatalf("关联视频B标签失败: %v", err)
	}
	candidateA := models.AITagCandidate{VideoID: videoA.ID, SuggestedName: sourceA.Name, NormalizedName: sourceA.Name, MatchedTagID: &sourceA.ID, Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusApproved}
	candidateB := models.AITagCandidate{VideoID: videoA.ID, SuggestedName: target.Name, NormalizedName: target.Name, MatchedTagID: &target.ID, Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusApproved}
	if err := database.DB.Create(&[]*models.AITagCandidate{&candidateA, &candidateB}).Error; err != nil {
		t.Fatalf("创建 AI 候选失败: %v", err)
	}
	approvals := []models.AITagApprovalRecord{
		{VideoID: videoA.ID, TagID: sourceA.ID, CandidateID: candidateA.ID},
		{VideoID: videoA.ID, TagID: target.ID, CandidateID: candidateB.ID},
	}
	if err := database.DB.Create(&approvals).Error; err != nil {
		t.Fatalf("创建审批记录失败: %v", err)
	}
	preferences := []models.ShortFeedTagPreference{
		{TagID: target.ID, Score: 1.5},
		{TagID: sourceA.ID, Score: 2},
		{TagID: sourceB.ID, Score: -0.5},
	}
	if err := database.DB.Create(&preferences).Error; err != nil {
		t.Fatalf("创建短视频偏好失败: %v", err)
	}

	result, err := (&TagService{}).MergeTags([]uint{sourceA.ID, sourceB.ID}, target.ID)
	if err != nil {
		t.Fatalf("合并标签失败: %v", err)
	}
	if result.MergedTagCount != 2 || result.VideoLinksMoved != 1 {
		t.Fatalf("合并结果错误: %+v", result)
	}
	for _, videoID := range []uint{videoA.ID, videoB.ID} {
		var video models.Video
		if err := database.DB.Preload("Tags").First(&video, videoID).Error; err != nil {
			t.Fatalf("读取视频标签失败: %v", err)
		}
		if len(video.Tags) != 1 || video.Tags[0].ID != target.ID {
			t.Fatalf("视频 %d 应仅保留目标标签: %+v", videoID, video.Tags)
		}
	}
	var candidate models.AITagCandidate
	if err := database.DB.First(&candidate, candidateA.ID).Error; err != nil || candidate.MatchedTagID == nil || *candidate.MatchedTagID != target.ID {
		t.Fatalf("AI 候选引用未更新: %+v err=%v", candidate, err)
	}
	var approvalCount int64
	if err := database.DB.Model(&models.AITagApprovalRecord{}).Where("video_id = ? AND tag_id = ?", videoA.ID, target.ID).Count(&approvalCount).Error; err != nil || approvalCount != 1 {
		t.Fatalf("重复审批记录未合并: count=%d err=%v", approvalCount, err)
	}
	var preference models.ShortFeedTagPreference
	if err := database.DB.Where("tag_id = ?", target.ID).First(&preference).Error; err != nil || preference.Score != 3 {
		t.Fatalf("偏好权重未合并: %+v err=%v", preference, err)
	}
	var deletedCount int64
	if err := database.DB.Unscoped().Model(&models.Tag{}).Where("id IN ? AND deleted_at IS NOT NULL", []uint{sourceA.ID, sourceB.ID}).Count(&deletedCount).Error; err != nil || deletedCount != 2 {
		t.Fatalf("源标签未软删除: count=%d err=%v", deletedCount, err)
	}
}

func TestSyncShortVideoTagsReconcilesAgainstConfiguredDuration(t *testing.T) {
	setupVideoServiceTestDB(t)
	short := models.Video{Name: "short.mp4", Path: "/tmp/short-tag.mp4", Duration: 120}
	long := models.Video{Name: "long.mp4", Path: "/tmp/long-tag.mp4", Duration: 420}
	if err := database.DB.Create(&[]*models.Video{&short, &long}).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	result, err := (&TagService{}).SyncShortVideoTags()
	if err != nil {
		t.Fatalf("同步短视频标签失败: %v", err)
	}
	if result.Added != 1 || result.Removed != 0 || result.TagID == 0 {
		t.Fatalf("首次同步结果错误: %+v", result)
	}
	var tagged models.Video
	if err := database.DB.Preload("Tags").First(&tagged, short.ID).Error; err != nil || len(tagged.Tags) != 1 || tagged.Tags[0].Name != ShortVideoTagName {
		t.Fatalf("短视频未自动打标签: %+v err=%v", tagged.Tags, err)
	}

	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	settings.ShortFeedMaxDurationMinutes = 1
	if err := (&SettingsService{}).UpdateSettings(settings); err != nil {
		t.Fatalf("更新短视频时长失败: %v", err)
	}
	if err := database.DB.Preload("Tags").First(&tagged, short.ID).Error; err != nil {
		t.Fatalf("读取更新后标签失败: %v", err)
	}
	if len(tagged.Tags) != 0 {
		t.Fatalf("时长阈值缩短后应移除自动标签: %+v", tagged.Tags)
	}
}

func TestSyncShortVideoTagsKeepsExactAutomaticNameAndPreservesConflictingManualTag(t *testing.T) {
	setupVideoServiceTestDB(t)
	manualTag := models.Tag{Name: ShortVideoTagName, Color: "#999999", IsActive: true}
	short := models.Video{Name: "short.mp4", Path: "/tmp/collision-short.mp4", Duration: 30}
	long := models.Video{Name: "long.mp4", Path: "/tmp/collision-long.mp4", Duration: 600}
	if err := database.DB.Create(&manualTag).Error; err != nil {
		t.Fatalf("创建同名人工标签失败: %v", err)
	}
	if err := database.DB.Create(&[]*models.Video{&short, &long}).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := database.DB.Model(&long).Association("Tags").Append(&manualTag); err != nil {
		t.Fatalf("绑定人工标签失败: %v", err)
	}

	result, err := (&TagService{}).SyncShortVideoTags()
	if err != nil {
		t.Fatalf("同步短视频标签失败: %v", err)
	}
	if result.TagID == manualTag.ID {
		t.Fatal("自动标签不能接管同名人工标签记录")
	}
	var loadedLong models.Video
	if err := database.DB.Preload("Tags").First(&loadedLong, long.ID).Error; err != nil {
		t.Fatalf("读取长视频失败: %v", err)
	}
	if len(loadedLong.Tags) != 1 || loadedLong.Tags[0].ID != manualTag.ID || loadedLong.Tags[0].Name == ShortVideoTagName {
		t.Fatalf("人工标签关联不应被自动规则删除: %+v", loadedLong.Tags)
	}
	var automaticTag models.Tag
	if err := database.DB.First(&automaticTag, result.TagID).Error; err != nil {
		t.Fatalf("读取自动标签失败: %v", err)
	}
	if automaticTag.AutomaticKind != shortVideoAutomaticTagKind || automaticTag.Name != ShortVideoTagName {
		t.Fatalf("自动标签应固定显示为短视频: %+v", automaticTag)
	}
}

func TestMergeTagsAllowsSystemTagsWithinAITagLibrary(t *testing.T) {
	setupVideoServiceTestDB(t)
	target := models.Tag{Name: "动作", Namespace: "内容", Color: "#111111", IsSystem: true, IsActive: true}
	source := models.Tag{Name: "激烈动作", Namespace: "内容", Color: "#222222", IsSystem: true, IsActive: true}
	video := models.Video{Name: "action.mp4", Path: "/tmp/system-tag-merge.mp4"}
	if err := database.DB.Create(&[]*models.Tag{&target, &source}).Error; err != nil {
		t.Fatalf("创建 AI 标签失败: %v", err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := database.DB.Model(&video).Association("Tags").Append(&source); err != nil {
		t.Fatalf("关联来源标签失败: %v", err)
	}

	if _, err := (&TagService{}).MergeTags([]uint{source.ID}, target.ID); err != nil {
		t.Fatalf("同一 AI 标签库内应支持合并: %v", err)
	}
	if err := database.DB.Preload("Tags").First(&video, video.ID).Error; err != nil {
		t.Fatalf("读取合并后视频失败: %v", err)
	}
	if len(video.Tags) != 1 || video.Tags[0].ID != target.ID {
		t.Fatalf("AI 标签关联未合并: %+v", video.Tags)
	}
}

func TestMergeTagsDeduplicatesApprovalRecordsAcrossMultipleSources(t *testing.T) {
	setupVideoServiceTestDB(t)
	target := models.Tag{Name: "目标", Color: "#111111", IsActive: true}
	sourceA := models.Tag{Name: "来源A", Color: "#222222", IsActive: true}
	sourceB := models.Tag{Name: "来源B", Color: "#333333", IsActive: true}
	video := models.Video{Name: "approval.mp4", Path: "/tmp/multi-source-approval.mp4"}
	if err := database.DB.Create(&[]*models.Tag{&target, &sourceA, &sourceB}).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	candidateA := models.AITagCandidate{VideoID: video.ID, SuggestedName: sourceA.Name, NormalizedName: sourceA.Name, MatchedTagID: &sourceA.ID, Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusApproved}
	candidateB := models.AITagCandidate{VideoID: video.ID, SuggestedName: sourceB.Name, NormalizedName: sourceB.Name, MatchedTagID: &sourceB.ID, Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusApproved}
	if err := database.DB.Create(&[]*models.AITagCandidate{&candidateA, &candidateB}).Error; err != nil {
		t.Fatalf("创建候选失败: %v", err)
	}
	approvals := []models.AITagApprovalRecord{
		{VideoID: video.ID, TagID: sourceA.ID, CandidateID: candidateA.ID},
		{VideoID: video.ID, TagID: sourceB.ID, CandidateID: candidateB.ID},
	}
	if err := database.DB.Create(&approvals).Error; err != nil {
		t.Fatalf("创建审批记录失败: %v", err)
	}

	if _, err := (&TagService{}).MergeTags([]uint{sourceA.ID, sourceB.ID}, target.ID); err != nil {
		t.Fatalf("多来源审批标签合并失败: %v", err)
	}
	var loaded []models.AITagApprovalRecord
	if err := database.DB.Where("video_id = ?", video.ID).Find(&loaded).Error; err != nil {
		t.Fatalf("读取审批记录失败: %v", err)
	}
	if len(loaded) != 1 || loaded[0].TagID != target.ID {
		t.Fatalf("审批记录应去重并指向目标标签: %+v", loaded)
	}
}

func TestSaveAITagLibraryPreservesValidCandidatesAndSupersedesInvalidOnes(t *testing.T) {
	setupVideoServiceTestDB(t)
	existing := []models.Tag{
		{Name: "动作", Namespace: "行为", Color: "#111111", IsSystem: true, IsActive: true},
		{Name: "站立", Namespace: "姿态", Color: "#222222", IsSystem: true, IsActive: true},
		{Name: "跑步", Namespace: "行为", Color: "#555555", IsSystem: true, IsActive: true},
	}
	if err := database.DB.Create(&existing).Error; err != nil {
		t.Fatalf("创建系统标签失败: %v", err)
	}
	video := models.Video{Name: "pending.mp4", Path: "/tmp/tag-library-pending.mp4"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	retainedTagID := existing[0].ID
	removedTagID := existing[1].ID
	deactivatedTagID := existing[2].ID
	retainedCandidate := models.AITagCandidate{VideoID: video.ID, SuggestedName: "动作", NormalizedName: "动作", MatchedTagID: &retainedTagID, Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending}
	removedCandidate := models.AITagCandidate{VideoID: video.ID, SuggestedName: "站立", NormalizedName: "站立", MatchedTagID: &removedTagID, Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending}
	deactivatedCandidate := models.AITagCandidate{VideoID: video.ID, SuggestedName: "跑步", NormalizedName: "跑步", MatchedTagID: &deactivatedTagID, Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending}
	unmatchedCandidate := models.AITagCandidate{VideoID: video.ID, SuggestedName: "旧候选", NormalizedName: "旧候选", Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending}
	state := models.AITaggingState{VideoID: video.ID, Status: models.AITaggingStateStatusCompleted, EvidenceFingerprint: "old"}
	if err := database.DB.Create(&retainedCandidate).Error; err != nil {
		t.Fatalf("创建保留候选失败: %v", err)
	}
	if err := database.DB.Create(&removedCandidate).Error; err != nil {
		t.Fatalf("创建失效候选失败: %v", err)
	}
	if err := database.DB.Create(&deactivatedCandidate).Error; err != nil {
		t.Fatalf("创建停用候选失败: %v", err)
	}
	if err := database.DB.Create(&unmatchedCandidate).Error; err != nil {
		t.Fatalf("创建未匹配候选失败: %v", err)
	}
	if err := database.DB.Create(&state).Error; err != nil {
		t.Fatalf("创建分析状态失败: %v", err)
	}

	svc := &TagService{}
	saved, err := svc.SaveAITagLibrary([]AITagLibraryInput{
		{ID: existing[0].ID, Namespace: "行为", Name: "激烈动作", Color: "#333333", IsActive: true},
		{ID: existing[2].ID, Namespace: "行为", Name: "跑步", Color: "#555555", IsActive: false},
		{Namespace: "服饰", Name: "制服", Color: "#444444", IsActive: true},
	})
	if err != nil {
		t.Fatalf("保存 AI 标签库失败: %v", err)
	}
	if len(saved) != 3 {
		t.Fatalf("AI 标签库应包含 3 个标签，实际 %d", len(saved))
	}

	var removed models.Tag
	if err := database.DB.First(&removed, existing[1].ID).Error; err != nil {
		t.Fatalf("读取移出标签失败: %v", err)
	}
	if removed.IsSystem || !removed.IsActive || removed.Name != "站立" {
		t.Fatalf("移出标签应保留为普通标签: %+v", removed)
	}
	if err := database.DB.First(&retainedCandidate, retainedCandidate.ID).Error; err != nil {
		t.Fatalf("读取保留候选失败: %v", err)
	}
	if retainedCandidate.Status != models.AITagCandidateStatusPending || retainedCandidate.SuggestedName != "激烈动作" || retainedCandidate.NormalizedName != "激烈动作" {
		t.Fatalf("仍匹配有效标签的候选应保留并同步改名: %+v", retainedCandidate)
	}
	if err := database.DB.First(&removedCandidate, removedCandidate.ID).Error; err != nil {
		t.Fatalf("读取失效候选失败: %v", err)
	}
	if removedCandidate.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("已移出标签库的候选应失效，实际 %s", removedCandidate.Status)
	}
	if err := database.DB.First(&deactivatedCandidate, deactivatedCandidate.ID).Error; err != nil {
		t.Fatalf("读取停用候选失败: %v", err)
	}
	if deactivatedCandidate.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("已停用标签的候选应失效，实际 %s", deactivatedCandidate.Status)
	}
	if err := database.DB.First(&unmatchedCandidate, unmatchedCandidate.ID).Error; err != nil {
		t.Fatalf("读取未匹配候选失败: %v", err)
	}
	if unmatchedCandidate.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("无法匹配标签库的候选应失效，实际 %s", unmatchedCandidate.Status)
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

func TestSaveAITagLibraryReusesExistingManualTagAndPreservesVideoLinks(t *testing.T) {
	setupVideoServiceTestDB(t)
	manual := models.Tag{Name: "女上", Color: "#111111", IsActive: true}
	previousLibraryTag := models.Tag{Name: "旧 AI 标签", Namespace: "人物", Color: "#222222", IsSystem: true, IsActive: true}
	video := models.Video{Name: "existing-tag.mp4", Path: "/tmp/existing-tag.mp4"}
	if err := database.DB.Create(&[]*models.Tag{&manual, &previousLibraryTag}).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := database.DB.Model(&video).Association("Tags").Append(&manual); err != nil {
		t.Fatalf("关联已有普通标签失败: %v", err)
	}

	saved, err := (&TagService{}).SaveAITagLibrary([]AITagLibraryInput{
		{ID: previousLibraryTag.ID, Namespace: "人物", Name: manual.Name, Color: "#abcdef", IsActive: true},
	})
	if err != nil {
		t.Fatalf("已有普通标签应能直接加入 AI 标签库: %v", err)
	}
	if len(saved) != 1 || saved[0].ID != manual.ID || !saved[0].IsSystem || saved[0].Namespace != "人物" {
		t.Fatalf("应复用已有普通标签记录: %+v", saved)
	}
	var loadedVideo models.Video
	if err := database.DB.Preload("Tags").First(&loadedVideo, video.ID).Error; err != nil {
		t.Fatalf("读取视频标签失败: %v", err)
	}
	if len(loadedVideo.Tags) != 1 || loadedVideo.Tags[0].ID != manual.ID {
		t.Fatalf("加入 AI 标签库不得丢失已有视频关联: %+v", loadedVideo.Tags)
	}
	var oldTag models.Tag
	if err := database.DB.First(&oldTag, previousLibraryTag.ID).Error; err != nil {
		t.Fatalf("读取被替换的旧 AI 标签失败: %v", err)
	}
	if oldTag.IsSystem || !oldTag.IsActive || oldTag.Namespace != "" {
		t.Fatalf("旧 AI 标签应退出标签库但保留为普通标签: %+v", oldTag)
	}
}

func TestSaveAITagLibraryPromotesExistingManualTagByID(t *testing.T) {
	setupVideoServiceTestDB(t)
	manual := models.Tag{Name: "现有标签", Color: "#111111", IsActive: true}
	if err := database.DB.Create(&manual).Error; err != nil {
		t.Fatalf("创建普通标签失败: %v", err)
	}

	saved, err := (&TagService{}).SaveAITagLibrary([]AITagLibraryInput{
		{ID: manual.ID, Namespace: "自定义", Name: manual.Name, Color: manual.Color, IsActive: true},
	})
	if err != nil {
		t.Fatalf("按已有标签 ID 加入 AI 标签库失败: %v", err)
	}
	if len(saved) != 1 || saved[0].ID != manual.ID || !saved[0].IsSystem {
		t.Fatalf("应原地升级已有标签: %+v", saved)
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
