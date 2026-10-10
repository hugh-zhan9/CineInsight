package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

func seedRecoverProject(t *testing.T, status string) models.VideoEditProject {
	t.Helper()
	project := models.VideoEditProject{Kind: models.VideoEditKindMerge, Title: "对账", Status: status, Revision: 3,
		Mode: models.VideoEditModePrecise, RecipeJSON: `{"v":1,"merge":{"sources":[]},"tracks":{"audio":[],"subtitle":[]}}`, AcknowledgedJSON: "[]"}
	if err := database.DB.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	return project
}

func seedRecoverItem(t *testing.T, project models.VideoEditProject, status, dir, target string, staged int64) models.VideoEditItem {
	t.Helper()
	plan, _ := json.Marshal(editPlan{V: editPlanVersion, Seq: 1, OutputDir: dir, Sidecar: editPlanSidecar{Mode: "none", CopySource: -1}})
	item := models.VideoEditItem{ProjectID: project.ID, Seq: 1, Status: status, Phase: models.VideoEditPhasePublish, PlanJSON: string(plan),
		OutputName: filepath.Base(target), PublishTarget: target, StagedSize: staged}
	if err := database.DB.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(dir, editWorkdirPrefix+strconvFormatUint(uint64(item.ID)))
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEditTestFile(t, filepath.Join(workdir, "seg_0000.mkv"), "segment")
	if err := database.DB.Model(&item).Update("work_dir", workdir).Error; err != nil {
		t.Fatal(err)
	}
	item.WorkDir = workdir
	return item
}

// 启动对账：running→interrupted；publish_target 存在且大小等于 staged_size、库里没有记录 → 补做入库；
// 大小不符 → 不入库、文件保留、工作目录删除；分析中回到 draft；只清扫名字合规且不在运行的工作目录。
func TestVideoEditRecoverOnStartup(t *testing.T) {
	service, _ := newFakeEditService(t)
	dir := t.TempDir()

	published := seedRecoverProject(t, models.VideoEditStatusRunning)
	goodTarget := filepath.Join(dir, "成品 (合并).mkv")
	writeEditTestFile(t, goodTarget, "finished-output")
	good := seedRecoverItem(t, published, models.VideoEditStatusRunning, dir, goodTarget, int64(len("finished-output")))

	mismatched := seedRecoverProject(t, models.VideoEditStatusRunning)
	badTarget := filepath.Join(dir, "半成品 (合并).mkv")
	writeEditTestFile(t, badTarget, "truncated")
	bad := seedRecoverItem(t, mismatched, models.VideoEditStatusRunning, dir, badTarget, 999)

	queued := seedRecoverProject(t, models.VideoEditStatusQueued)
	waiting := seedRecoverItem(t, queued, models.VideoEditStatusQueued, dir, "", 0)
	analyzing := seedRecoverProject(t, models.VideoEditStatusAnalyzing)

	if err := database.DB.Create(&models.ScanDirectory{Path: dir}).Error; err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, "子目录", editWorkdirPrefix+"999")
	foreign := filepath.Join(dir, editWorkdirPrefix+"007")
	for _, path := range []string{orphan, foreign} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// 模拟重启：以上都是「上一个进程」留下的；对账之后才排的项目（queued_at 晚于本进程启动）不能被动。
	service.bootTime = time.Now()
	fresh := seedRecoverProject(t, models.VideoEditStatusQueued)
	later := service.bootTime.Add(time.Second)
	if err := database.DB.Model(&fresh).Update("queued_at", &later).Error; err != nil {
		t.Fatal(err)
	}
	freshItem := seedRecoverItem(t, fresh, models.VideoEditStatusQueued, dir, "", 0)
	service.RecoverOnStartup(context.Background())
	if got := loadEditItemsForTest(t, fresh.ID); got[0].ID != freshItem.ID || got[0].Status != models.VideoEditStatusQueued {
		t.Fatalf("本进程里新排的项不能被对账改成中断: %+v", got)
	}
	if current, _ := service.GetProject(context.Background(), fresh.ID); current.Status != models.VideoEditStatusQueued {
		t.Fatalf("本进程里新排的项目应保持 queued: %s", current.Status)
	}
	removeEditWorkdir(freshItem.WorkDir)

	items := map[uint]models.VideoEditItem{}
	for _, project := range []models.VideoEditProject{published, mismatched, queued} {
		for _, item := range loadEditItemsForTest(t, project.ID) {
			items[item.ID] = item
		}
	}
	if got := items[good.ID]; got.Status != models.VideoEditStatusCompleted || got.OutputVideoID == nil {
		t.Fatalf("rename 后崩溃且大小一致的项应补做入库: %+v", got)
	}
	var output models.Video
	if err := database.DB.Where("path = ?", goodTarget).First(&output).Error; err != nil {
		t.Fatalf("应建成品记录: %v", err)
	}
	if got := items[bad.ID]; got.Status != models.VideoEditStatusInterrupted || got.OutputVideoID != nil {
		t.Fatalf("大小不符的项应中断且不入库: %+v", got)
	}
	if _, err := os.Stat(badTarget); err != nil {
		t.Fatal("对账不删 publish_target 指向的文件")
	}
	var count int64
	database.DB.Model(&models.Video{}).Where("path = ?", badTarget).Count(&count)
	if count != 0 {
		t.Fatal("大小不符的文件不能入库")
	}
	if got := items[waiting.ID]; got.Status != models.VideoEditStatusInterrupted {
		t.Fatalf("排队项应中断: %+v", got)
	}
	for _, item := range []models.VideoEditItem{good, bad, waiting} {
		if _, err := os.Stat(item.WorkDir); !os.IsNotExist(err) {
			t.Fatalf("项 %d 的工作目录应删除", item.ID)
		}
	}
	for project, want := range map[uint]string{published.ID: models.VideoEditStatusCompleted, mismatched.ID: models.VideoEditStatusInterrupted,
		queued.ID: models.VideoEditStatusInterrupted, analyzing.ID: models.VideoEditStatusDraft} {
		current, err := service.GetProject(context.Background(), project)
		if err != nil || current.Status != want {
			t.Fatalf("项目 %d 应为 %s: %+v %v", project, want, current, err)
		}
	}
	removed, err := service.SweepOrphanWorkdirs(context.Background())
	if err != nil || removed != 1 {
		t.Fatalf("应只清掉 1 个孤儿工作目录: removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("孤儿工作目录应删除")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("名字不规范的同前缀目录不能动")
	}
}
