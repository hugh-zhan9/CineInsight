package services

import (
	"context"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
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

// I2：删除规则后，它真正插入的标签行改成无主留下，AI 自动打标仍不把这些标签当人工标签；标签不撤。
// 无主行不算「处理过」：之后的新规则照常对这条媒体记自己的账本。
func TestAutomationRuleDeleteKeepsRuleTagsOutOfAIManualCheck(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	tag := autoTag(t, "待整理")
	root := t.TempDir()
	ruled := autoVideo(t, filepath.Join(root, "ruled.mp4"), nil)
	manual := autoVideo(t, filepath.Join(root, "manual.mp4"), nil)
	if err := (&VideoService{}).AddTagToVideo(manual.ID, tag.ID); err != nil {
		t.Fatal(err)
	}
	input := AutomationRuleInput{Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: root}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: tag.ID}}}
	rule := saveAutoRule(t, svc, input)
	evaluateVideos(t, svc, ruled.ID, manual.ID)
	if err := svc.DeleteAutomationRule(rule.ID); err != nil {
		t.Fatal(err)
	}
	if !videoHasTag(t, ruled.ID, tag.ID) || !videoHasTag(t, manual.ID, tag.ID) {
		t.Fatal("删除规则不撤标签")
	}
	var orphans []models.AutomationRuleWrite
	database.DB.Where("rule_id IS NULL").Find(&orphans)
	if len(orphans) != 1 || orphans[0].MediaID != ruled.ID || !orphans[0].Inserted {
		t.Fatalf("只应留下规则真正插入的那行、且无主: %+v", orphans)
	}
	if ids := untaggedVideoIDs(t); !ids[ruled.ID] || ids[manual.ID] {
		t.Fatalf("删除规则后规则标签仍不算人工标签: %v", ids)
	}
	next := saveAutoRule(t, svc, input)
	evaluateVideos(t, svc, ruled.ID)
	if writes := ruleWrites(t, next.ID); len(writes) != 1 || writes[0].Inserted {
		t.Fatalf("新规则应照常记自己的账本（关系已在，inserted=false）: %+v", writes)
	}
	if ids := untaggedVideoIDs(t); !ids[ruled.ID] {
		t.Fatal("无主行仍让这条标签排除在人工标签之外")
	}
}

// M6(b)：用户手动加上规则加过的标签（去掉后又加回）就归用户；批量入口同一口径。
func TestAutomationRuleManualAddClaimsRuleInsertedTag(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	tag := autoTag(t, "待整理")
	root := t.TempDir()
	first := autoVideo(t, filepath.Join(root, "a.mp4"), nil)
	second := autoVideo(t, filepath.Join(root, "b.mp4"), nil)
	rule := saveAutoRule(t, svc, AutomationRuleInput{Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: root}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: tag.ID}}})
	evaluateVideos(t, svc, first.ID, second.ID)
	videos := &VideoService{}
	if err := videos.RemoveTagFromVideo(first.ID, tag.ID); err != nil {
		t.Fatal(err)
	}
	if result := videos.BatchAddTagToVideos([]uint{first.ID}, tag.ID); result.Failed != 0 {
		t.Fatalf("批量加标签失败: %+v", result)
	}
	writes := ruleWrites(t, rule.ID)
	if len(writes) != 2 || writes[0].MediaID != first.ID || writes[0].Inserted || !writes[1].Inserted {
		t.Fatalf("手动加回的那条应改成 inserted=false，另一条不动: %+v", writes)
	}
	if ids := untaggedVideoIDs(t); ids[first.ID] || !ids[second.ID] {
		t.Fatalf("手动加回的标签算人工标签: %v", ids)
	}
	if err := database.Transaction(func(tx *gorm.DB) error {
		return claimRuleInsertedTagsTx(tx, AutomationMediaVideo, []uint{second.ID, second.ID}, tag.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if writes := ruleWrites(t, rule.ID); writes[1].Inserted {
		t.Fatal("claimRuleInsertedTagsTx 应把这些媒体上的规则标签交给用户")
	}
}

// M6(a)：标签合并时，合并前这条媒体上只要有一个来源或目标标签是人工的，合并后的标签就归用户。
func TestAutomationRuleTagMergeReleasesManuallyOwnedTags(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	source := autoTag(t, "来源")
	target := autoTag(t, "目标")
	root, other := t.TempDir(), t.TempDir()
	hadTarget := autoVideo(t, filepath.Join(root, "a.mp4"), nil)
	ruleOnly := autoVideo(t, filepath.Join(root, "b.mp4"), nil)
	hadSource := autoVideo(t, filepath.Join(other, "c.mp4"), nil)
	videos := &VideoService{}
	if err := videos.AddTagToVideo(hadTarget.ID, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := videos.AddTagToVideo(hadSource.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	sourceRule := saveAutoRule(t, svc, AutomationRuleInput{Name: "加来源", Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: root}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: source.ID}}})
	targetRule := saveAutoRule(t, svc, AutomationRuleInput{Name: "加目标", Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: other}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: target.ID}}})
	evaluateVideos(t, svc, hadTarget.ID, ruleOnly.ID, hadSource.ID)
	if _, err := (&TagService{}).MergeTags([]uint{source.ID}, target.ID); err != nil {
		t.Fatal(err)
	}
	inserted := map[uint]bool{}
	for _, rule := range []AutomationRuleView{sourceRule, targetRule} {
		for _, write := range ruleWrites(t, rule.ID) {
			if write.TargetID != target.ID {
				t.Fatalf("账本应改写到目标标签: %+v", write)
			}
			inserted[write.MediaID] = write.Inserted
		}
	}
	if inserted[hadTarget.ID] || !inserted[ruleOnly.ID] || inserted[hadSource.ID] {
		t.Fatalf("合并前有人工标签的媒体应归用户: %v", inserted)
	}
	if ids := untaggedVideoIDs(t); ids[hadTarget.ID] || ids[hadSource.ID] || !ids[ruleOnly.ID] {
		t.Fatalf("AI 判定应随之改变: %v", ids)
	}
}

// M6(b) 图片侧：手动给图片加上规则加过的标签，同样归用户。
func TestAutomationRuleManualImageAddClaimsRuleTag(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	tag := autoTag(t, "截图")
	root := t.TempDir()
	image := models.Image{Name: "a.png", Path: filepath.Join(root, "a.png"), Directory: root, Size: 10}
	database.DB.Create(&image)
	saveAutoRule(t, svc, AutomationRuleInput{MediaKind: AutomationMediaImage, Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: root}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: tag.ID}}})
	if err := svc.evaluateNewMedia(context.Background(), AutomationMediaImage, []uint{image.ID}); err != nil {
		t.Fatal(err)
	}
	library := NewImageLibraryService()
	images := &ImageAITaggingService{db: database.DB}
	if got, _ := images.hasManualOfficialImageTags(context.Background(), image.ID); got {
		t.Fatal("规则标签不算手工标签")
	}
	if err := library.RemoveTagFromImage(image.ID, tag.ID); err != nil {
		t.Fatal(err)
	}
	if err := library.AddTagToImage(image.ID, tag.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := images.hasManualOfficialImageTags(context.Background(), image.ID); !got {
		t.Fatal("手动加回的图片标签算手工标签")
	}
}
