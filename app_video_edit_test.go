package main

import (
	"strings"
	"testing"

	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
	"video-master/services"
)

func seedAppVideoEditProject(t *testing.T, status, itemStatus string) models.VideoEditProject {
	t.Helper()
	project := models.VideoEditProject{Kind: models.VideoEditKindMerge, Title: "合并：测试", Status: status, Revision: 1,
		Mode: models.VideoEditModePrecise, RecipeJSON: `{"v":1}`, AcknowledgedJSON: "[]"}
	if err := database.DB.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	item := models.VideoEditItem{ProjectID: project.ID, Seq: 1, Status: itemStatus, Phase: models.VideoEditPhasePending, PlanJSON: "{}"}
	if err := database.DB.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	return project
}

// 有排队的导出项时退出须确认（视频编辑合同「维护与退出」），任务中心最近任务列出项目并给出取消与打开动作。
func TestVideoEditQuitGuardAndTaskCenter(t *testing.T) {
	resetQuitGuardForTest(t)
	previous := database.DB
	database.DB = dbtest.Open(t)
	t.Cleanup(func() { database.DB = previous })
	app := &App{backgroundTasks: services.NewBackgroundTaskRegistry(), videoEdit: services.NewVideoEditService(nil, nil, t.TempDir())}
	if tasks := app.quitBlockingTasks(); len(tasks) != 0 {
		t.Fatalf("没有导出项时不拦退出: %+v", tasks)
	}
	project := seedAppVideoEditProject(t, models.VideoEditStatusQueued, models.VideoEditStatusQueued)
	tasks := app.quitBlockingTasks()
	if len(tasks) != 1 || tasks[0].Key != "video_edit" || tasks[0].Queued != 1 || tasks[0].Running != 0 {
		t.Fatalf("排队的导出项应拦下退出: %+v", tasks)
	}
	snapshot := app.GetTaskCenterSnapshot()
	found := false
	for _, job := range snapshot.Recent {
		if job.Kind == TaskRecentKindVideoEdit && job.ID == "1" && job.Status == "queued" &&
			strings.Join(job.Actions, ",") == "cancel,open_video_edit" && job.Title == project.Title {
			found = true
		}
	}
	if !found {
		t.Fatalf("最近任务应列出视频工作台项目: %+v", snapshot.Recent)
	}
	status := app.GetVideoEditStatus()
	if status.QueuedItems != 1 || status.WorkerRunning {
		t.Fatalf("状态计数不对: %+v", status)
	}
}

// 数据库维护期间工作台入口直接报 video_edit_unavailable，不碰数据库。
func TestVideoEditAppRefusesDuringMaintenance(t *testing.T) {
	previous := database.DB
	database.DB = dbtest.Open(t)
	t.Cleanup(func() { database.DB = previous })
	app := &App{videoEdit: services.NewVideoEditService(nil, nil, t.TempDir())}
	release := database.BeginMaintenance()
	defer release()
	if _, err := app.CreateEditProject(services.EditProjectCreateRequest{Kind: "merge", VideoIDs: []uint{1, 2}}); err == nil ||
		!strings.HasPrefix(err.Error(), "video_edit_unavailable:") {
		t.Fatalf("维护期间应报 video_edit_unavailable: %v", err)
	}
	if _, err := app.ListEditProjects(10); err == nil {
		t.Fatal("维护期间列表也应拒绝")
	}
	_ = app.GetVideoEditStatus()
}
