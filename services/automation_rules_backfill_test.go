package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"
)

// M8：入库时 EXIF 没解析成的图片，EXIF 补解析成功后经队列再评估一次。
func TestAutomationRuleReevaluatesImageAfterEXIFBackfill(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	tag := autoTag(t, "无定位")
	root := t.TempDir()
	path := filepath.Join(root, "b.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nx"), 0644); err != nil {
		t.Fatal(err)
	}
	image := models.Image{Name: "b.png", Path: path, Directory: root, Size: 9, Format: "png"}
	database.DB.Create(&image)
	rule := saveAutoRule(t, svc, AutomationRuleInput{MediaKind: AutomationMediaImage, Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondHasGPS, Value: ptrBool(false)}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: tag.ID}}})
	if err := svc.evaluateNewMedia(context.Background(), AutomationMediaImage, []uint{image.ID}); err != nil {
		t.Fatal(err)
	}
	if len(ruleWrites(t, rule.ID)) != 0 {
		t.Fatal("EXIF 未解析时不满足、不写账本")
	}
	backfill := NewImageEXIFBackfillService()
	backfill.SetParsedObserver(svc.EnqueueImage)
	if _, err := backfill.StartImageEXIFBackfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := waitImageEXIFBackfill(t, backfill); status.Processed != 1 || status.Failed != 0 {
		t.Fatalf("补解析应成功: %+v", status)
	}
	waitAutomationQueueIdle(t, svc)
	if writes := ruleWrites(t, rule.ID); len(writes) != 1 || !writes[0].Inserted || writes[0].MediaID != image.ID {
		t.Fatalf("补解析出「没有地理位置」后规则应生效: %+v", writes)
	}
}

// M9：视频工作台与超分的产出在技术信息补探测成功后也不评估规则；普通视频照常。
func TestAutomationRuleSkipsDerivedOutputsAfterBackfill(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	tag := autoTag(t, "4K")
	plain := createProbeTestVideo(t)
	edited := createProbeTestVideo(t)
	enhanced := createProbeTestVideo(t)
	if err := database.DB.Model(&models.Video{}).Where("id IN ?", []uint{plain.ID, edited.ID, enhanced.ID}).
		Updates(map[string]any{"height": 0, "width": 0}).Error; err != nil {
		t.Fatal(err)
	}
	project := models.VideoEditProject{Kind: "merge", Title: "p", Status: "completed", Mode: "copy", RecipeJSON: "{}", AnalysisJSON: "{}", AcknowledgedJSON: "[]"}
	if err := database.DB.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	editedID, enhancedID := edited.ID, enhanced.ID
	item := models.VideoEditItem{ProjectID: project.ID, Seq: 1, Status: models.VideoEditStatusCompleted, Phase: models.VideoEditPhaseDone, PlanJSON: "{}", OutputVideoID: &editedID}
	if err := database.DB.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	task := models.VideoEnhancementTask{VideoID: plain.ID, Profile: models.EnhancementProfileGeneral, Scale: 2, Status: models.EnhancementStatusCompleted,
		Phase: models.EnhancementPhaseEncode, OutputBasename: "x.mkv", OutputVideoID: &enhancedID}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	saveAutoRule(t, svc, AutomationRuleInput{Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondHeight, Min: ptrFloat(2000)}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: tag.ID}}})
	probe := newMediaProbeServiceWithRunner(func(ctx context.Context, path string) ([]byte, string, error) {
		return []byte(multiStreamFFProbeFixture), "", nil
	})
	backfill := NewTechnicalBackfillService(probe)
	backfill.SetProbedObserver(svc.EnqueueVideo)
	if _, err := backfill.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := waitForBackfill(t, backfill); status.Succeeded != 3 {
		t.Fatalf("三条都应补探测成功: %+v", status)
	}
	waitAutomationQueueIdle(t, svc)
	if !videoHasTag(t, plain.ID, tag.ID) || videoHasTag(t, edited.ID, tag.ID) || videoHasTag(t, enhanced.ID, tag.ID) {
		t.Fatal("只有普通视频应套用规则，工作台与超分产出不评估")
	}
}
