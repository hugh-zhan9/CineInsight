package services

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func waitAutomationRun(t *testing.T, svc *AutomationRuleService) AutomationRuleRunStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if status := svc.GetAutomationRuleRunStatus(); !status.Running {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待手动运行超时: %+v", svc.GetAutomationRuleRunStatus())
	return AutomationRuleRunStatus{}
}

// 预览只数可见范围内、条件满足、账本里还没有的条目；执行结果与预览一致，并记 last_run_at。
func TestAutomationRulePreviewAndRun(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	var emittedMu sync.Mutex
	var emitted []AutomationRuleRunStatus
	svc.SetEventEmitter(func(status AutomationRuleRunStatus) {
		emittedMu.Lock()
		emitted = append(emitted, status)
		emittedMu.Unlock()
	})
	tag := autoTag(t, "大文件")
	root := t.TempDir()
	handled := autoVideo(t, filepath.Join(root, "handled.mp4"), func(v *models.Video) { v.Size = 5000 })
	pending := autoVideo(t, filepath.Join(root, "pending.mp4"), func(v *models.Video) { v.Size = 5000 })
	small := autoVideo(t, filepath.Join(root, "small.mp4"), func(v *models.Video) { v.Size = 10 })
	stale := autoVideo(t, filepath.Join(root, "stale.mp4"), func(v *models.Video) { v.Size = 5000; v.IsStale = true; v.StaleReason = models.StaleReasonMissingFile })
	outside := autoVideo(t, filepath.Join(t.TempDir(), "outside.mp4"), func(v *models.Video) { v.Size = 5000 })
	rule := saveAutoRule(t, svc, AutomationRuleInput{Enabled: false,
		Conditions: []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: root}, {Type: automationCondSizeBetween, Min: ptrFloat(1000)}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: tag.ID}}})
	if err := svc.evaluateNewMedia(context.Background(), AutomationMediaVideo, []uint{handled.ID}); err != nil {
		t.Fatal(err)
	}
	if videoHasTag(t, handled.ID, tag.ID) {
		t.Fatal("停用的规则不参与新入库评估")
	}
	if _, err := applyAutomationWrites(context.Background(), AutomationMediaVideo, []automationPendingWrite{{RuleID: rule.ID, MediaID: handled.ID, Action: AutomationRuleAction{Type: automationActionAddTag, TagID: tag.ID}}}); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.PreviewAutomationRule(context.Background(), rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Count != 1 || len(preview.Samples) != 1 || preview.Samples[0].ID != pending.ID || preview.Samples[0].Name != "pending.mp4" {
		t.Fatalf("预览应只含 pending（排除已处理、不满足、失效、范围外）: %+v", preview)
	}
	if _, err := svc.PreviewAutomationRule(context.Background(), 9999); !errors.Is(err, ErrAutomationRuleNotFound) {
		t.Fatalf("预览不存在的规则应报 rule_not_found: %v", err)
	}

	if _, err := svc.RunAutomationRules([]uint{rule.ID}); err != nil {
		t.Fatal(err)
	}
	status := waitAutomationRun(t, svc)
	if !status.Completed || status.Cancelled || status.Error != "" || status.Total != 2 || status.Processed != 2 || status.Matched != 1 || status.Added != 1 {
		t.Fatalf("运行状态错误: %+v", status)
	}
	if !videoHasTag(t, pending.ID, tag.ID) || videoHasTag(t, small.ID, tag.ID) || videoHasTag(t, stale.ID, tag.ID) || videoHasTag(t, outside.ID, tag.ID) {
		t.Fatal("执行结果应与预览一致")
	}
	if got := ruleRow(t, rule.ID); got.LastRunAt == nil {
		t.Fatal("跑完应记 last_run_at")
	}
	// 结束状态在置位之后才推送：等它到。
	deadline := time.Now().Add(5 * time.Second)
	for {
		emittedMu.Lock()
		done := len(emitted) >= 2 && !emitted[len(emitted)-1].Running
		first := emitted[0]
		emittedMu.Unlock()
		if done {
			if !first.Running {
				t.Fatalf("第一条推送应是开始状态: %+v", first)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("应推送开始与结束状态")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if again, err := svc.PreviewAutomationRule(context.Background(), rule.ID); err != nil || again.Count != 0 {
		t.Fatalf("跑完后再预览应为 0: %+v err=%v", again, err)
	}
}

// 同一时刻只允许一次手动运行；取消后已提交的批次保留；维护中拒绝新运行。
func TestAutomationRuleRunBusyCancelAndStop(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	tag := autoTag(t, "批量")
	root := t.TempDir()
	videos := make([]models.Video, 0, 600)
	for i := 0; i < 600; i++ {
		path := filepath.Join(root, fmt.Sprintf("v%03d.mp4", i))
		videos = append(videos, models.Video{Name: filepath.Base(path), Path: path, Directory: root, Size: 1})
	}
	if err := database.DB.CreateInBatches(&videos, 200).Error; err != nil {
		t.Fatal(err)
	}
	rule := saveAutoRule(t, svc, AutomationRuleInput{Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: root}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: tag.ID}}})

	reached, release := make(chan struct{}), make(chan struct{})
	pages := 0
	svc.afterRunPage = func() {
		pages++
		if pages == 1 {
			close(reached)
			<-release
		}
	}
	if _, err := svc.RunAutomationRules([]uint{rule.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunAutomationRules([]uint{rule.ID}); !errors.Is(err, ErrAutomationRunBusy) {
		t.Fatalf("运行中再次运行应报 automation_run_busy: %v", err)
	}
	<-reached
	svc.CancelAutomationRuleRun()
	close(release)
	status := waitAutomationRun(t, svc)
	if !status.Cancelled || status.Error != "" || status.Processed != automationBatchSize || status.Added != automationBatchSize || status.Total != 600 {
		t.Fatalf("取消后应保留第一批: %+v", status)
	}
	var tagged int64
	database.DB.Table("video_tags").Where("tag_id = ?", tag.ID).Count(&tagged)
	if tagged != automationBatchSize {
		t.Fatalf("第一批 %d 条应已提交，实际 %d", automationBatchSize, tagged)
	}
	if got := ruleRow(t, rule.ID); got.LastRunAt != nil {
		t.Fatal("中途取消不记 last_run_at")
	}

	svc.afterRunPage = nil
	svc.StopAndWait()
	if _, err := svc.RunAutomationRules([]uint{rule.ID}); !errors.Is(err, database.ErrMaintenance) {
		t.Fatalf("停止后应拒绝新运行: %v", err)
	}
	svc.EnqueueVideo(videos[599].ID)
	if svc.queueRunning || len(svc.queue) != 0 {
		t.Fatal("停止后不应接受入队")
	}
	svc.Resume()
	if _, err := svc.RunAutomationRules([]uint{rule.ID}); err != nil {
		t.Fatal(err)
	}
	if status := waitAutomationRun(t, svc); status.Added != 100 || status.Cancelled {
		t.Fatalf("恢复后重跑应补完剩下的 100 条: %+v", status)
	}
}
