package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

func queuedMerge(t *testing.T, service *VideoEditService, media *fakeEditMedia) (*EditProjectView, []models.Video) {
	t.Helper()
	dir := t.TempDir()
	a, b := media.addFakeSource(t, dir, "a.mkv", 3000), media.addFakeSource(t, dir, "b.mkv", 2000)
	view := mustEditProject(t, service, models.VideoEditKindMerge, a.ID, b.ID)
	queueAllAcknowledged(t, service, view.ID)
	return view, []models.Video{a, b}
}

// 状态迁移：draft → queued → running → completed；成品入库、项记 output_video_id，原片不变。
func TestVideoEditQueueRunsToCompletion(t *testing.T) {
	service, media := newFakeEditService(t)
	view, videos := queuedMerge(t, service, media)
	done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
	if done.Status != models.VideoEditStatusCompleted || done.FinishedAt == nil || done.Progress != 1 {
		t.Fatalf("应完成: %+v", done)
	}
	output, path := outputPathOf(t, done, 1)
	if filepath.Base(path) != "a (合并).mkv" || output.Size == 0 {
		t.Fatalf("成品入库不对: %+v", output)
	}
	if raw, _ := os.ReadFile(videos[0].Path); string(raw) != "source:a.mkv" {
		t.Fatal("原片被改动")
	}
	var relations int64
	database.DB.Model(&models.VideoSameSourceRelation{}).Count(&relations)
	if relations != 0 {
		t.Fatal("编辑成品不建同源关系")
	}
}

// 取消排队项：直接 cancelled，worker 恢复后也不再执行。
func TestVideoEditCancelQueuedProject(t *testing.T) {
	service, media := newFakeEditService(t)
	pauseEditWorker(service)
	view, _ := queuedMerge(t, service, media)
	if err := service.CancelProject(context.Background(), view.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.CancelProject(context.Background(), view.ID); err != nil {
		t.Fatalf("重复取消应幂等: %v", err)
	}
	service.Resume()
	time.Sleep(100 * time.Millisecond)
	current, _ := service.GetProject(context.Background(), view.ID)
	if current.Status != models.VideoEditStatusCancelled || current.Items[0].Status != models.VideoEditStatusCancelled || media.runs != 0 {
		t.Fatalf("排队项取消后不应执行: %+v runs=%d", current, media.runs)
	}
}

// 取消运行项：取消 context、等 ffmpeg 退出，清理工作目录后 cancelled；不入库。
func TestVideoEditCancelRunningProjectRemovesWorkdir(t *testing.T) {
	service, media := newFakeEditService(t)
	media.block, media.started = make(chan struct{}), make(chan struct{}, 1)
	view, videos := queuedMerge(t, service, media)
	<-media.started
	workdir := filepath.Join(filepath.Dir(videos[0].Path), editWorkdirPrefix+"1")
	if _, err := os.Stat(workdir); err != nil {
		t.Fatalf("运行中应有工作目录: %v", err)
	}
	if err := service.CancelProject(context.Background(), view.ID); err != nil {
		t.Fatal(err)
	}
	waitForEditCondition(t, "运行项取消", func() bool {
		items := loadEditItemsForTest(t, view.ID)
		return items[0].Status == models.VideoEditStatusCancelled
	})
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Fatalf("取消后工作目录应删除: %v", err)
	}
	current, _ := service.GetProject(context.Background(), view.ID)
	if current.Status != models.VideoEditStatusCancelled || current.Items[0].ErrorCode != "cancelled" || current.Items[0].OutputVideoID != nil {
		t.Fatalf("取消后状态不对: %+v", current)
	}
}

// 来源指纹在排队后变化报 source_changed，文件消失报 source_missing；都不发布。
func TestVideoEditSourceChangedAndMissing(t *testing.T) {
	service, media := newFakeEditService(t)
	pauseEditWorker(service)
	changed, videos := queuedMerge(t, service, media)
	if err := os.WriteFile(videos[0].Path, []byte("source:a.mkv-modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing, others := queuedMerge(t, service, media)
	if err := os.Remove(others[1].Path); err != nil {
		t.Fatal(err)
	}
	service.Resume()
	for project, code := range map[uint]string{changed.ID: "source_changed", missing.ID: "source_missing"} {
		done := waitEditProject(t, service, project, 10*time.Second, models.VideoEditStatusFailed, models.VideoEditStatusCompleted)
		if done.Status != models.VideoEditStatusFailed || done.Items[0].ErrorCode != code || done.ErrorCode != code {
			t.Fatalf("应以 %s 失败: %+v", code, done)
		}
	}
	if media.runs != 0 {
		t.Fatalf("取媒体槽后先核对来源，变化或缺失时不应启动 ffmpeg，实际运行 %d 次", media.runs)
	}
}

// 来源在编码期间被改动：校验后再次核对指纹，报 source_changed，不发布、不入库。
func TestVideoEditSourceChangedDuringEncode(t *testing.T) {
	service, media := newFakeEditService(t)
	pauseEditWorker(service)
	view, videos := queuedMerge(t, service, media)
	encode := service.runProgress
	service.runProgress = func(ctx context.Context, name string, args []string, onProgress func(int64)) (string, error) {
		_ = os.WriteFile(videos[1].Path, []byte("source:b.mkv-rewritten-during-encode"), 0o644)
		return encode(ctx, name, args, onProgress)
	}
	service.Resume()
	done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusFailed, models.VideoEditStatusCompleted)
	if done.Items[0].ErrorCode != "source_changed" || done.Items[0].OutputVideoID != nil {
		t.Fatalf("编码期间来源变化应报 source_changed 且不发布: %+v", done.Items[0])
	}
	var count int64
	database.DB.Model(&models.Video{}).Where("name LIKE ?", "%(合并)%").Count(&count)
	if count != 0 {
		t.Fatal("不应入库")
	}
}

// 空间不足：预检阶段报 disk_full 阻止排队；排队后空间变少，执行前检查同样报 disk_full。
func TestVideoEditDiskFull(t *testing.T) {
	service, media := newFakeEditService(t)
	dir := t.TempDir()
	a, b := media.addFakeSource(t, dir, "a.mkv", 3000), media.addFakeSource(t, dir, "b.mkv", 2000)
	view := mustEditProject(t, service, models.VideoEditKindMerge, a.ID, b.ID)
	service.diskFree = func(string) (uint64, error) { return 1024, nil }
	pre, err := service.PreflightProject(context.Background(), view.ID)
	if err != nil || !editIssueCodes(pre.Errors)["disk_full"] || pre.Outputs[0].FreeBytes != 1024 {
		t.Fatalf("预检应报 disk_full: %v %+v", err, pre.Errors)
	}
	service.diskFree = func(string) (uint64, error) { return 1 << 40, nil }
	pauseEditWorker(service)
	queueAllAcknowledged(t, service, view.ID)
	service.diskFree = func(string) (uint64, error) { return 1024, nil }
	service.Resume()
	done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusFailed, models.VideoEditStatusCompleted)
	if done.Items[0].ErrorCode != "disk_full" || media.runs != 0 {
		t.Fatalf("执行前空间不足应报 disk_full 且不跑 ffmpeg: %+v runs=%d", done.Items[0], media.runs)
	}
}

// 入库事务失败：成品移回工作目录（随后随工作目录删除），目标路径不留文件、库里没有记录，报 publish_failed。
func TestVideoEditPublishTransactionFailureMovesFileBack(t *testing.T) {
	service, media := newFakeEditService(t)
	targetSeen := false
	var target string
	service.publishTxHook = func(tx *gormDB) error {
		var item models.VideoEditItem
		if err := tx.First(&item).Error; err == nil && item.PublishTarget != "" {
			target = item.PublishTarget
			_, err := os.Stat(target)
			targetSeen = err == nil
		}
		return errTestPublish
	}
	view, _ := queuedMerge(t, service, media)
	done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusFailed, models.VideoEditStatusCompleted)
	if done.Items[0].ErrorCode != "publish_failed" || !targetSeen {
		t.Fatalf("应在 rename 之后的事务里失败并报 publish_failed: %+v seen=%v", done.Items[0], targetSeen)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("事务失败后目标路径不能留下文件: %v", err)
	}
	var count int64
	database.DB.Model(&models.Video{}).Where("path = ?", target).Count(&count)
	items := loadEditItemsForTest(t, view.ID)
	if count != 0 || items[0].PublishTarget != "" || items[0].WorkDir != "" {
		t.Fatalf("不应入库，发布记录应清空: count=%d item=%+v", count, items[0])
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(target), editWorkdirPrefix+"1")); !os.IsNotExist(err) {
		t.Fatal("失败后工作目录应删除")
	}
}

// 「继续未完成项」：部分成功的去片头项目只重做失败项，已完成项的成品与记录不动。
func TestVideoEditRequeueOnlyUnfinishedItems(t *testing.T) {
	service, media := newFakeEditService(t)
	dir := t.TempDir()
	first, second := media.addFakeSource(t, dir, "ep1.mkv", 10000), media.addFakeSource(t, dir, "ep2.mkv", 10000)
	view := mustEditProject(t, service, models.VideoEditKindTrimIntro, first.ID, second.ID)
	mustUpdateRecipe(t, service, view, func(recipe *EditRecipe) {
		for i := range recipe.TrimIntro.Items {
			recipe.TrimIntro.Items[i].RemoveEndMS, recipe.TrimIntro.Items[i].Confirmed = 2000, true
		}
	})
	media.failSource = second.Path
	queueAllAcknowledged(t, service, view.ID)
	partial := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusPartial, models.VideoEditStatusFailed, models.VideoEditStatusCompleted)
	if partial.Status != models.VideoEditStatusPartial || partial.Items[0].Status != models.VideoEditStatusCompleted ||
		partial.Items[1].ErrorCode != "encode_failed" {
		t.Fatalf("应部分成功: %+v", partial)
	}
	firstOutput := *partial.Items[0].OutputVideoID
	if partial.Items[1].ErrorMessage == "" || containsAbsolutePath(partial.Items[1].ErrorMessage) {
		t.Fatalf("失败原因应保留 stderr 尾部且擦除路径: %q", partial.Items[1].ErrorMessage)
	}
	if _, err := service.RequeueProject(context.Background(), EditQueueRequest{ProjectID: view.ID, ExpectedRevision: partial.Revision + 1}); editErrorCodeOf(err) != "edit_project_conflict" {
		t.Fatalf("revision 不符应报冲突: %v", err)
	}
	media.mu.Lock()
	media.failSource, media.runs = "", 0
	media.mu.Unlock()
	result, err := service.RequeueProject(context.Background(), EditQueueRequest{ProjectID: view.ID, ExpectedRevision: partial.Revision})
	if err != nil || !result.Queued || len(result.Preflight.Outputs) != 1 || result.Preflight.Outputs[0].Seq != 2 {
		t.Fatalf("只应重新冻结第 2 项: %+v %v", result, err)
	}
	done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusCompleted, models.VideoEditStatusFailed, models.VideoEditStatusPartial)
	if done.Status != models.VideoEditStatusCompleted || *done.Items[0].OutputVideoID != firstOutput || done.Items[1].OutputVideoID == nil {
		t.Fatalf("继续后应全部完成且第 1 项不重做: %+v", done)
	}
	if media.runs != 2 {
		t.Fatalf("继续时只应执行第 2 项（1 段 + 拼接），实际 ffmpeg 运行 %d 次", media.runs)
	}
}

// 维护或退出时停止 worker：运行项与所属项目标 interrupted，工作目录删除；Resume 后可「继续」。
func TestVideoEditStopMarksRunningItemInterrupted(t *testing.T) {
	service, media := newFakeEditService(t)
	media.block, media.started = make(chan struct{}), make(chan struct{}, 1)
	view, videos := queuedMerge(t, service, media)
	<-media.started
	service.StopAndWait()
	items := loadEditItemsForTest(t, view.ID)
	current, _ := service.GetProject(context.Background(), view.ID)
	if items[0].Status != models.VideoEditStatusInterrupted || current.Status != models.VideoEditStatusInterrupted {
		t.Fatalf("停止后运行项与项目应为 interrupted: %+v %s", items[0], current.Status)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(videos[0].Path), editWorkdirPrefix+"1")); !os.IsNotExist(err) {
		t.Fatal("中断后工作目录应删除")
	}
	close(media.block)
	media.mu.Lock()
	media.block = nil
	media.mu.Unlock()
	service.Resume()
	if _, err := service.RequeueProject(context.Background(), EditQueueRequest{ProjectID: view.ID, ExpectedRevision: current.Revision}); err != nil {
		t.Fatal(err)
	}
	done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
	if done.Status != models.VideoEditStatusCompleted {
		t.Fatalf("继续后应完成: %+v", done)
	}
}

// 删除项目：排队/运行中拒绝；结束后只删项目记录，成品视频保留。
func TestVideoEditDeleteProject(t *testing.T) {
	service, media := newFakeEditService(t)
	pauseEditWorker(service)
	view, _ := queuedMerge(t, service, media)
	if err := service.DeleteProject(context.Background(), view.ID); editErrorCodeOf(err) != "edit_project_conflict" {
		t.Fatalf("排队中的项目不能删除: %v", err)
	}
	service.Resume()
	done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusCompleted)
	output, path := outputPathOf(t, done, 1)
	if err := service.DeleteProject(context.Background(), view.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetProject(context.Background(), view.ID); editErrorCodeOf(err) != "edit_project_not_found" {
		t.Fatalf("删除后项目应不存在: %v", err)
	}
	var count int64
	database.DB.Model(&models.VideoEditItem{}).Where("project_id = ?", view.ID).Count(&count)
	if _, err := os.Stat(path); err != nil || count != 0 || database.DB.First(&models.Video{}, output.ID).Error != nil {
		t.Fatalf("成品应保留、导出项记录应删除: %v count=%d", err, count)
	}
}
