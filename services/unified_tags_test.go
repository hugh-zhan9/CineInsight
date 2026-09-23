package services

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestUnifiedTagLibraryIncludesLegacyTypesAndExcludesAutomaticAndDeleted(t *testing.T) {
	setupVideoServiceTestDB(t)
	tags := []models.Tag{
		{Name: "普通"}, {Name: "历史AI", IsSystem: true},
		{Name: "历史停用", IsSystem: true}, {Name: "历史NULL"},
		{Name: "自动", AutomaticKind: "short_video"}, {Name: "已删除"},
	}
	if err := database.DB.Create(&tags).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&tags[2]).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&tags[3]).Update("automatic_kind", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&tags[5]).Error; err != nil {
		t.Fatal(err)
	}
	videoSvc := &AITaggingService{}
	imageSvc := &ImageAITaggingService{db: database.DB}
	videoTags, err := videoSvc.loadActiveTags()
	if err != nil {
		t.Fatal(err)
	}
	imageTags, err := imageSvc.loadActiveLibraryTags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, library := range [][]models.Tag{videoTags, imageTags} {
		if len(library) != 4 {
			t.Fatalf("library=%+v", library)
		}
		prompt := formatClosedTagLibraryForPrompt(library)
		for _, tag := range tags[:4] {
			if !strings.Contains(prompt, tag.Name) {
				t.Fatalf("missing %q in %q", tag.Name, prompt)
			}
			if _, err := videoSvc.resolveOfficialTagInTx(database.DB, models.AITagCandidate{MatchedTagID: &tag.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err := imageSvc.resolveOfficialImageTagInTx(database.DB, models.ImageAITagCandidate{MatchedTagID: &tag.ID}); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(prompt, "自动") || strings.Contains(prompt, "已删除") {
			t.Fatal(prompt)
		}
	}
	// Both formatters enforce eligibility even when passed an unfiltered list.
	if prompt := formatClosedTagLibraryForPrompt(tags); strings.Contains(prompt, "自动") || strings.Contains(prompt, "已删除") {
		t.Fatal(prompt)
	}
	for _, tag := range tags[4:] {
		if _, err := videoSvc.resolveOfficialTagInTx(database.DB, models.AITagCandidate{MatchedTagID: &tag.ID}); err == nil {
			t.Fatal("ineligible video approval")
		}
		if _, err := imageSvc.resolveOfficialImageTagInTx(database.DB, models.ImageAITagCandidate{MatchedTagID: &tag.ID}); err == nil {
			t.Fatal("ineligible image approval")
		}
	}
	if err := database.DB.Delete(&models.Tag{}, "id IN ?", []uint{tags[0].ID, tags[1].ID, tags[2].ID, tags[3].ID}).Error; err != nil {
		t.Fatal(err)
	}
	for _, loader := range []func() ([]models.Tag, error){videoSvc.loadActiveTags, func() ([]models.Tag, error) { return imageSvc.loadActiveLibraryTags(context.Background()) }} {
		got, err := loader()
		if err != nil || len(got) != 0 {
			t.Fatalf("only automatic library=%+v err=%v", got, err)
		}
	}
}

func TestUnifiedOrdinaryTagAIWorkflowRemainsClosed(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag, err := (&TagService{}).CreateTagWithCategory("海边", "", "场景")
	if err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "unified.mp4", Path: "/tmp/unified.mp4"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{{Label: tag.Name, Confidence: "high"}, {Label: "模型自创", Confidence: "high"}}}
	svc := newTestAITaggingService(client, nil)
	if err := svc.ProcessVideo(context.Background(), video.ID); err != nil {
		t.Fatal(err)
	}
	var candidates []models.AITagCandidate
	if err := database.DB.Find(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].MatchedTagID == nil || *candidates[0].MatchedTagID != tag.ID {
		t.Fatalf("candidates=%+v", candidates)
	}
	if _, err := svc.ApproveCandidate(candidates[0].ID); err != nil {
		t.Fatal(err)
	}
	if countRows(t, "tags") != 1 || countRows(t, "video_tags") != 1 {
		t.Fatal("approval changed vocabulary or missed link")
	}
}

func TestUnifiedTagRenameAndDeletePreserveAtomicCandidateChanges(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := models.Tag{Name: "旧名称", IsSystem: true}
	video := models.Video{Name: "rename.mp4", Path: "/tmp/unified-rename.mp4"}
	img := models.Image{Name: "rename.jpg", Path: "/tmp/unified-rename.jpg"}
	for _, row := range []any{&tag, &video, &img} {
		if err := database.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Model(&video).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&img).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	vc := models.AITagCandidate{VideoID: video.ID, MatchedTagID: &tag.ID, SuggestedName: tag.Name, NormalizedName: tag.Name, Confidence: "high", Status: "pending"}
	ic := models.ImageAITagCandidate{ImageID: img.ID, MatchedTagID: &tag.ID, SuggestedName: tag.Name, NormalizedName: tag.Name, Confidence: "high", Status: "pending"}
	for _, row := range []any{&vc, &ic} {
		if err := database.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &TagService{}
	if err := svc.UpdateTagWithCategory(tag.ID, "新名称", "#123456", "题材"); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.First(&vc, vc.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.First(&ic, ic.ID).Error; err != nil {
		t.Fatal(err)
	}
	if vc.Status != "pending" || vc.SuggestedName != "新名称" || ic.Status != "superseded" {
		t.Fatalf("vc=%+v ic=%+v", vc, ic)
	}
	if countRows(t, "video_tags") != 1 || countRows(t, "image_tags") != 1 {
		t.Fatal("rename lost links")
	}
	// A candidate-write failure must roll back earlier tag/link mutations.
	callback := "test:unified-candidate-failure"
	if err := database.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "image_ai_tag_candidates" {
			tx.AddError(errors.New("candidate write failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Callback().Update().Remove(callback) })
	if err := svc.UpdateTag(tag.ID, "失败改名", tag.Color); err == nil {
		t.Fatal("expected rename failure")
	}
	if err := svc.DeleteTag(tag.ID); err == nil {
		t.Fatal("expected delete failure")
	}
	if err := database.DB.First(&tag, tag.ID).Error; err != nil || tag.Name != "新名称" {
		t.Fatalf("tag=%+v err=%v", tag, err)
	}
	if countRows(t, "video_tags") != 1 || countRows(t, "image_tags") != 1 {
		t.Fatal("failed delete lost links")
	}
	if err := database.DB.Callback().Update().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteTag(tag.ID); err != nil {
		t.Fatal(err)
	}
	if countRows(t, "video_tags") != 0 || countRows(t, "image_tags") != 0 {
		t.Fatal("delete retained links")
	}
	if err := database.DB.First(&vc, vc.ID).Error; err != nil || vc.Status != "superseded" {
		t.Fatalf("vc=%+v err=%v", vc, err)
	}
}

func TestUnifiedEmptyPromptNeverAllowsInventedTags(t *testing.T) {
	prompt := buildAITaggingPromptText(AITaggingRequest{}, 1000)
	if strings.Contains(prompt, "new_candidate") || !strings.Contains(prompt, "禁止输出候选集之外的标签") {
		t.Fatal(prompt)
	}
}

func TestUnifiedCaseDistinctTagsKeepSeparateCandidatesAndApprovals(t *testing.T) {
	imageSvc := newImageAITaggingReviewTestService(t)
	upper := models.Tag{Name: "Action"}
	lower := models.Tag{Name: "action", IsSystem: true}
	video := models.Video{Name: "case.mp4", Path: "/tmp/unified-case.mp4"}
	img := imageAITaggingTestImage(t, "heic")
	for _, row := range []any{&upper, &lower, &video} {
		if err := database.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	tags := []models.Tag{upper, lower}
	matcher := newAITagMatcher(tags)
	if tag, ok := matcher.match("Action"); !ok || tag.ID != upper.ID {
		t.Fatalf("wrong exact match: %+v", tag)
	}
	if _, ok := matcher.match("ACTION"); ok {
		t.Fatal("ambiguous normalization must not choose a tag")
	}
	suggestions := []AITagSuggestion{{Label: "Action", Confidence: "high"}, {Label: "action", Confidence: "high"}}
	merged := mergeAITagSuggestions(nil, map[string]int{}, suggestions, matcher)
	if len(merged) != 2 {
		t.Fatal("batch merging collapsed distinct tags")
	}
	videoSvc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	for i := 0; i < 2; i++ {
		if _, err := videoSvc.persistSuggestions(video, tags, AITaggingEvidence{}, suggestions); err != nil {
			t.Fatal(err)
		}
	}
	var vc []models.AITagCandidate
	if err := database.DB.Where("video_id = ?", video.ID).Order("id").Find(&vc).Error; err != nil {
		t.Fatal(err)
	}
	if len(vc) != 2 || *vc[0].MatchedTagID != upper.ID || *vc[1].MatchedTagID != lower.ID {
		t.Fatalf("video candidates=%+v", vc)
	}
	for _, c := range vc {
		if _, err := videoSvc.ApproveCandidate(c.ID); err != nil {
			t.Fatal(err)
		}
	}
	if countRows(t, "video_tags") != 2 {
		t.Fatal("approval lost case-distinct tag")
	}
	if _, err := imageSvc.persistImageSuggestions(*img, tags, suggestions, AITaggingConfig{}); err != nil {
		t.Fatal(err)
	}
	var ic []models.ImageAITagCandidate
	if err := database.DB.Where("image_id = ? AND status = 'pending'", img.ID).Order("id").Find(&ic).Error; err != nil {
		t.Fatal(err)
	}
	if len(ic) != 2 || *ic[0].MatchedTagID != upper.ID || *ic[1].MatchedTagID != lower.ID {
		t.Fatalf("image candidates=%+v", ic)
	}
	if err := imageSvc.RejectImageAITagCandidate(ic[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := imageSvc.persistImageSuggestions(*img, tags, suggestions, AITaggingConfig{}); err != nil {
		t.Fatal(err)
	}
	ic = nil
	if err := database.DB.Where("image_id = ? AND status = 'pending'", img.ID).Find(&ic).Error; err != nil {
		t.Fatal(err)
	}
	if len(ic) != 1 || *ic[0].MatchedTagID != lower.ID {
		t.Fatalf("rejection crossed tag IDs: %+v", ic)
	}
	if _, err := imageSvc.ApproveImageAITagCandidate(ic[0].ID); err != nil {
		t.Fatal(err)
	}
	if !imageHasTag(t, img.ID, lower.ID) || imageHasTag(t, img.ID, upper.ID) {
		t.Fatal("wrong approved image tag")
	}
}

func TestUnifiedBatchAliasesKeepHighestConfidenceInEitherOrder(t *testing.T) {
	matcher := newAITagMatcher([]models.Tag{{ID: 10, Name: "Action"}})
	high := AITagSuggestion{Label: "Action", Confidence: "high"}
	medium := AITagSuggestion{Label: "ACTION", Confidence: "medium"}
	for _, batchOrder := range [][]AITagSuggestion{{high, medium}, {medium, high}} {
		positions := map[string]int{}
		var merged []AITagSuggestion
		for _, suggestion := range batchOrder {
			merged = mergeAITagSuggestions(merged, positions, []AITagSuggestion{suggestion}, matcher)
		}
		if len(merged) != 1 || merged[0].Label != "Action" || merged[0].Confidence != "high" {
			t.Fatalf("merged=%+v", merged)
		}
	}
}
