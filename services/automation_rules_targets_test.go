package services

import (
	"context"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"
)

func ruleRow(t *testing.T, id uint) models.AutomationRule {
	t.Helper()
	var rule models.AutomationRule
	if err := database.DB.First(&rule, id).Error; err != nil {
		t.Fatalf("读取规则失败: %v", err)
	}
	return rule
}

func untaggedVideoIDs(t *testing.T) map[uint]bool {
	t.Helper()
	videos, err := NewAITaggingService().findUntaggedVideos(100)
	if err != nil {
		t.Fatalf("查询待打标视频失败: %v", err)
	}
	ids := map[uint]bool{}
	for _, video := range videos {
		ids[video.ID] = true
	}
	return ids
}

// D-RA-04：规则真正插入的标签不阻止 AI 自动打标；用户手动加的（包括规则来时已存在、记 inserted=false 的）仍然阻止。
func TestAutomationRuleTagsDoNotBlockAIAutoTagging(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	tag := autoTag(t, "待整理")
	root := t.TempDir()
	ruled := autoVideo(t, filepath.Join(root, "ruled.mp4"), nil)
	manual := autoVideo(t, filepath.Join(root, "manual.mp4"), nil)
	if err := (&VideoService{}).AddTagToVideo(manual.ID, tag.ID); err != nil {
		t.Fatal(err)
	}
	saveAutoRule(t, svc, AutomationRuleInput{Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: root}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: tag.ID}}})
	evaluateVideos(t, svc, ruled.ID, manual.ID)
	if !videoHasTag(t, ruled.ID, tag.ID) {
		t.Fatal("规则应加上标签")
	}
	ids := untaggedVideoIDs(t)
	if !ids[ruled.ID] || ids[manual.ID] {
		t.Fatalf("规则标签不算人工标签、手动标签照常阻止: %v", ids)
	}
	ruleTags, err := automationRuleInsertedTagIDs(database.DB, AutomationMediaVideo, ruled.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasNonAutomaticTags([]models.Tag{tag}, ruleTags) {
		t.Fatal("hasNonAutomaticTags 应排除规则插入的标签")
	}
	manualRuleTags, _ := automationRuleInsertedTagIDs(database.DB, AutomationMediaVideo, manual.ID)
	if !hasNonAutomaticTags([]models.Tag{tag}, manualRuleTags) {
		t.Fatal("规则来时已存在的标签仍是人工标签")
	}
}

// 处理路径（processVideoWithConfig）同一口径：只有规则标签的视频不按 already_tagged 跳过。
func TestAutomationRuleTagsDoNotSkipAIProcessing(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	person := models.Tag{Name: "某人", Namespace: personTagNamespace}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ruled := autoVideo(t, filepath.Join(root, "ruled.mp4"), nil)
	manual := autoVideo(t, filepath.Join(t.TempDir(), "manual.mp4"), nil)
	if err := (&VideoService{}).AddTagToVideo(manual.ID, person.ID); err != nil {
		t.Fatal(err)
	}
	saveAutoRule(t, svc, AutomationRuleInput{Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: root}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: person.ID}}})
	evaluateVideos(t, svc, ruled.ID)
	ai := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	for _, id := range []uint{ruled.ID, manual.ID} {
		if err := ai.ProcessVideo(context.Background(), id); err != nil {
			t.Fatalf("处理视频失败: %v", err)
		}
	}
	// 人物标签不进词表，词表为空：通过了人工标签检查的视频停在 empty_tag_library。
	reasons := map[uint]string{}
	var states []models.AITaggingState
	database.DB.Find(&states)
	for _, state := range states {
		reasons[state.VideoID] = state.SkipReason
	}
	if reasons[ruled.ID] != "empty_tag_library" || reasons[manual.ID] != "already_tagged" {
		t.Fatalf("规则标签不应触发 already_tagged: %v", reasons)
	}
}

// 图片 AI 打标的「已有手工标签」同口径（§3.4）。
func TestAutomationRuleImageTagsDoNotCountAsManual(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	tag := autoTag(t, "截图")
	root := t.TempDir()
	ruled := models.Image{Name: "a.png", Path: filepath.Join(root, "a.png"), Directory: root, Size: 10}
	manual := models.Image{Name: "b.png", Path: filepath.Join(t.TempDir(), "b.png"), Directory: "/x", Size: 10}
	database.DB.Create(&ruled)
	database.DB.Create(&manual)
	if err := NewImageLibraryService().AddTagToImage(manual.ID, tag.ID); err != nil {
		t.Fatal(err)
	}
	rule := saveAutoRule(t, svc, AutomationRuleInput{MediaKind: AutomationMediaImage, Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondExtensionIn, Exts: []string{".PNG"}}, {Type: automationCondUnderDirectory, Path: root}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: tag.ID}}})
	if err := svc.evaluateNewMedia(context.Background(), AutomationMediaImage, []uint{ruled.ID, manual.ID}); err != nil {
		t.Fatal(err)
	}
	if writes := ruleWrites(t, rule.ID); len(writes) != 1 || writes[0].MediaKind != AutomationMediaImage || !writes[0].Inserted {
		t.Fatalf("图片规则应只对目录下的图片加标签: %+v", writes)
	}
	images := &ImageAITaggingService{db: database.DB}
	if got, err := images.hasManualOfficialImageTags(context.Background(), ruled.ID); err != nil || got {
		t.Fatalf("规则插入的图片标签不算手工标签: %v err=%v", got, err)
	}
	if got, err := images.hasManualOfficialImageTags(context.Background(), manual.ID); err != nil || !got {
		t.Fatalf("手工标签仍算: %v err=%v", got, err)
	}
}

// D-RA-05：标签被删时引用它的动作失效；没剩动作的规则停用并注明原因，还有别的动作的规则照常。
func TestAutomationRuleTagDeleteDisablesRule(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	doomed := autoTag(t, "要删的")
	kept := autoTag(t, "留着的")
	root := t.TempDir()
	video := autoVideo(t, filepath.Join(root, "a.mp4"), nil)
	cond := []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: root}}
	only := saveAutoRule(t, svc, AutomationRuleInput{Name: "只有它", Enabled: true, Conditions: cond,
		Actions: []AutomationRuleAction{{Type: automationActionAddTag, TagID: doomed.ID}}})
	both := saveAutoRule(t, svc, AutomationRuleInput{Name: "两个", Enabled: true, Conditions: cond,
		Actions: []AutomationRuleAction{{Type: automationActionAddTag, TagID: doomed.ID}, {Type: automationActionAddTag, TagID: kept.ID}}})
	evaluateVideos(t, svc, video.ID)
	if err := (&TagService{}).DeleteTag(doomed.ID); err != nil {
		t.Fatal(err)
	}
	if rule := ruleRow(t, only.ID); rule.Enabled || rule.DisabledReason != automationDisabledTargetDeleted || rule.ActionsJSON != "[]" {
		t.Fatalf("没剩动作的规则应停用并注明 target_deleted: %+v", rule)
	}
	views, err := svc.ListAutomationRules()
	if err != nil {
		t.Fatal(err)
	}
	if !views[1].Enabled || len(views[1].Actions) != 1 || views[1].Actions[0].TagID != kept.ID || views[1].Actions[0].TargetName != "留着的" {
		t.Fatalf("还有动作的规则应保持启用、只去掉失效动作: %+v", views[1])
	}
	var remaining int64
	database.DB.Model(&models.AutomationRuleWrite{}).Where("action = ? AND target_id = ?", automationWriteTag, doomed.ID).Count(&remaining)
	if remaining != 0 || len(ruleWrites(t, both.ID)) != 1 {
		t.Fatalf("被删标签的账本行应清掉，别的保留: remaining=%d", remaining)
	}
	if _, err := svc.SetAutomationRuleEnabled(only.ID, true); invalidField(t, err) != "actions" {
		t.Fatalf("没有动作的规则不能直接启用: %v", err)
	}
}

// 标签合并：规则动作与账本 target_id 改写到目标标签；同一（规则，媒体）冲突时只留一行（目标原有的优先）。
func TestAutomationRuleTagMergeRewritesActionsAndLedger(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	source := autoTag(t, "来源")
	target := autoTag(t, "目标")
	root := t.TempDir()
	onlySource := autoVideo(t, filepath.Join(root, "a.mp4"), nil)
	bothTags := autoVideo(t, filepath.Join(root, "b.mp4"), nil)
	cond := []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: root}}
	sourceRule := saveAutoRule(t, svc, AutomationRuleInput{Name: "来源规则", Enabled: true, Conditions: cond,
		Actions: []AutomationRuleAction{{Type: automationActionAddTag, TagID: source.ID}}})
	mixedRule := saveAutoRule(t, svc, AutomationRuleInput{Name: "两个都有", Enabled: true, Conditions: cond,
		Actions: []AutomationRuleAction{{Type: automationActionAddTag, TagID: source.ID}, {Type: automationActionAddTag, TagID: target.ID}}})
	evaluateVideos(t, svc, onlySource.ID, bothTags.ID)
	if _, err := (&TagService{}).MergeTags([]uint{source.ID}, target.ID); err != nil {
		t.Fatal(err)
	}
	views, err := svc.ListAutomationRules()
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if len(view.Actions) != 1 || view.Actions[0].TagID != target.ID || !view.Enabled {
			t.Fatalf("规则动作应改写成目标标签并去重: %+v", view)
		}
	}
	for _, rule := range []AutomationRuleView{sourceRule, mixedRule} {
		for _, write := range ruleWrites(t, rule.ID) {
			if write.TargetID != target.ID {
				t.Fatalf("账本应改写到目标标签: %+v", write)
			}
		}
	}
	if got := ruleWrites(t, sourceRule.ID); len(got) != 2 {
		t.Fatalf("来源规则每条媒体一行: %+v", got)
	}
	if got := ruleWrites(t, mixedRule.ID); len(got) != 2 {
		t.Fatalf("冲突的组合只留一行: %+v", got)
	}
	// 改写后再评估：账本认得目标标签，不重复处理。
	evaluateVideos(t, svc, onlySource.ID, bothTags.ID)
	if got := ruleWrites(t, mixedRule.ID); len(got) != 2 {
		t.Fatalf("改写后的账本应挡住重复处理: %+v", got)
	}
}

// 加入作品集：排在现有最后；作品集被删后规则停用。
func TestAutomationRuleCollectionActionAndCollectionDelete(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	collections := NewCollectionService(t.TempDir())
	collection, err := collections.CreateCollection("待看", "")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	existing := autoVideo(t, filepath.Join(t.TempDir(), "x.mp4"), nil)
	if err := collections.AddCollectionVideos(collection.ID, []uint{existing.ID}); err != nil {
		t.Fatal(err)
	}
	first := autoVideo(t, filepath.Join(root, "a.mp4"), nil)
	second := autoVideo(t, filepath.Join(root, "b.mp4"), nil)
	rule := saveAutoRule(t, svc, AutomationRuleInput{Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: root}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddToCollection, CollectionID: collection.ID}}})
	evaluateVideos(t, svc, first.ID, second.ID)
	var relations []models.CollectionVideo
	database.DB.Where("collection_id = ?", collection.ID).Order("position").Find(&relations)
	if len(relations) != 3 || relations[1].VideoID != first.ID || relations[1].Position != 2 || relations[2].VideoID != second.ID || relations[2].Position != 3 {
		t.Fatalf("规则加入的视频应依次排在最后: %+v", relations)
	}
	if err := collections.DeleteCollection(collection.ID); err != nil {
		t.Fatal(err)
	}
	if got := ruleRow(t, rule.ID); got.Enabled || got.DisabledReason != automationDisabledTargetDeleted {
		t.Fatalf("作品集删除后规则应停用: %+v", got)
	}
	if len(ruleWrites(t, rule.ID)) != 0 {
		t.Fatal("作品集的账本行应清掉")
	}
}
