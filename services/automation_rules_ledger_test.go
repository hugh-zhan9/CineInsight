package services

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func waitAutomationQueueIdle(t *testing.T, svc *AutomationRuleService) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		idle := !svc.queueRunning && len(svc.queue) == 0
		svc.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待规则队列超时")
}

// newVideoRecorder 记录 SetNewVideoObserver 收到的 id。
type newVideoRecorder struct {
	mu  sync.Mutex
	ids []uint
}

func (r *newVideoRecorder) observe(id uint) {
	r.mu.Lock()
	r.ids = append(r.ids, id)
	r.mu.Unlock()
}

func (r *newVideoRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ids)
}

// D-RA-03：关系本来就存在时也记账（inserted=false）；规则插入的标签被用户去掉后不再加回。
func TestAutomationRuleLedgerRecordsExistingRelationAndRemembersRemoval(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	tag := autoTag(t, "待整理")
	root := t.TempDir()
	already := autoVideo(t, filepath.Join(root, "a.mp4"), nil)
	fresh := autoVideo(t, filepath.Join(root, "b.mp4"), nil)
	other := autoVideo(t, filepath.Join(t.TempDir(), "c.mp4"), nil)
	if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", already.ID, tag.ID).Error; err != nil {
		t.Fatal(err)
	}
	rule := saveAutoRule(t, svc, AutomationRuleInput{Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondUnderDirectory, Path: root}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: tag.ID}}})

	evaluateVideos(t, svc, already.ID, fresh.ID, other.ID)
	writes := ruleWrites(t, rule.ID)
	if len(writes) != 2 || writes[0].MediaID != already.ID || writes[0].Inserted || writes[1].MediaID != fresh.ID || !writes[1].Inserted {
		t.Fatalf("账本应记两行：已存在的 inserted=false、新加的 inserted=true: %+v", writes)
	}
	if writes[0].Action != automationWriteTag || writes[0].TargetID != tag.ID || writes[0].MediaKind != AutomationMediaVideo {
		t.Fatalf("账本组合错误: %+v", writes[0])
	}
	if !videoHasTag(t, fresh.ID, tag.ID) || videoHasTag(t, other.ID, tag.ID) {
		t.Fatal("只有目录下的视频应加上标签")
	}
	// 用户把两条都去掉：再次评估不加回。
	if err := (&VideoService{}).RemoveTagFromVideo(already.ID, tag.ID); err != nil {
		t.Fatal(err)
	}
	if err := (&VideoService{}).RemoveTagFromVideo(fresh.ID, tag.ID); err != nil {
		t.Fatal(err)
	}
	evaluateVideos(t, svc, already.ID, fresh.ID)
	if videoHasTag(t, already.ID, tag.ID) || videoHasTag(t, fresh.ID, tag.ID) {
		t.Fatal("用户去掉的标签不应被规则加回")
	}
	if got := ruleWrites(t, rule.ID); len(got) != 2 {
		t.Fatalf("账本不应新增行: %+v", got)
	}
}

// D-RA-02：技术属性未知时不满足、也不写账本；补探测成功后经队列再评估一次。
func TestAutomationRuleUnknownValueReevaluatesAfterBackfill(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewAutomationRuleService("")
	tag := autoTag(t, "4K")
	video := createProbeTestVideo(t)
	if err := database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Updates(map[string]any{"height": 0, "width": 0}).Error; err != nil {
		t.Fatal(err)
	}
	rule := saveAutoRule(t, svc, AutomationRuleInput{Enabled: true,
		Conditions: []AutomationRuleCondition{{Type: automationCondHeight, Min: ptrFloat(2000)}, {Type: automationCondVideoCodecIn, Codecs: []string{"HEVC"}}},
		Actions:    []AutomationRuleAction{{Type: automationActionAddTag, TagID: tag.ID}}})
	evaluateVideos(t, svc, video.ID)
	if videoHasTag(t, video.ID, tag.ID) || len(ruleWrites(t, rule.ID)) != 0 {
		t.Fatal("高度未知时规则不满足，也不应写账本")
	}

	probe := newMediaProbeServiceWithRunner(func(ctx context.Context, path string) ([]byte, string, error) {
		return []byte(multiStreamFFProbeFixture), "", nil
	})
	backfill := NewTechnicalBackfillService(probe)
	backfill.SetProbedObserver(svc.EnqueueVideo)
	if _, err := backfill.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := waitForBackfill(t, backfill); status.Succeeded != 1 {
		t.Fatalf("补探测应成功: %+v", status)
	}
	waitAutomationQueueIdle(t, svc)
	if !videoHasTag(t, video.ID, tag.ID) {
		t.Fatal("补探测出 2160 高、hevc 主视频流后规则应生效（封面图流不算）")
	}
	if writes := ruleWrites(t, rule.ID); len(writes) != 1 || !writes[0].Inserted {
		t.Fatalf("账本应记一行 inserted=true: %+v", writes)
	}
}

// D-RA-05：创建入口只对新建的行入队；同路径已存在、访达放回原处恢复、回收站恢复、扫描搬家纠偏都不入队。
func TestAutomationRuleNewVideoObserverOnlyForNewRows(t *testing.T) {
	svc, video, _, _ := p010PutBackFixture(t)
	recorder := &newVideoRecorder{}
	svc.SetNewVideoObserver(recorder.observe)

	if _, err := svc.AddVideo(video.Path); err != nil {
		t.Fatalf("放回原处应恢复原记录: %v", err)
	}
	if recorder.count() != 0 {
		t.Fatalf("放回原处恢复的旧记录不应入队: %v", recorder.ids)
	}
	if _, err := svc.AddVideo(video.Path); !errors.Is(err, ErrVideoExists) {
		t.Fatalf("同路径再次添加应报已存在: %v", err)
	}
	newPath := filepath.Join(filepath.Dir(video.Path), "brand-new.mp4")
	mustCreateFile(t, newPath)
	created, err := svc.AddVideo(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if recorder.count() != 1 || recorder.ids[0] != created.ID {
		t.Fatalf("新建的行应入队一次: %v want %d", recorder.ids, created.ID)
	}
}

func TestAutomationRuleTrashRestoreAndRelocationDoNotEnqueue(t *testing.T) {
	svc, video, entry, _ := p010PutBackFixture(t)
	recorder := &newVideoRecorder{}
	svc.SetNewVideoObserver(recorder.observe)
	center := NewTrashCenter(svc, nil)
	if result, err := center.RestoreTrashEntries("video", []uint{entry.ID}); err != nil || result.Succeeded != 1 {
		t.Fatalf("回收站恢复应成功: %+v err=%v", result, err)
	}
	if recorder.count() != 0 {
		t.Fatalf("回收站恢复不应入队: %v", recorder.ids)
	}

	root := t.TempDir()
	createDirectoryRow(t, root)
	moved := models.Video{Name: "movie.mp4", Path: filepath.Join(root, "old", "movie.mp4"), Directory: filepath.Join(root, "old"), Size: 1}
	if err := database.DB.Create(&moved).Error; err != nil {
		t.Fatal(err)
	}
	createOldVideoFile(t, filepath.Join(root, "moved", "movie.mp4"))
	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if result.Relocated != 1 || result.Added != 0 {
		t.Fatalf("扫描应按搬家纠偏原记录: %+v", result)
	}
	if recorder.count() != 0 {
		t.Fatalf("搬家纠偏不应入队: %v (video=%d)", recorder.ids, video.ID)
	}
}
