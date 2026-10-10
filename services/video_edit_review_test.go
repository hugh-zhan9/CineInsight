package services

import (
	"sync"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

// 评审 1：worker 领到一项、还没登记取消函数时 StopAndWait 到达——必须立即取消，不能等整段编码跑完；
// 被领取的项以 interrupted 收尾，不能变成 completed。
func TestVideoEditStopBetweenClaimAndCancelInterrupts(t *testing.T) {
	service, media := newFakeEditService(t)
	media.block = make(chan struct{})
	stopped := make(chan struct{})
	var once sync.Once
	service.afterClaimHook = func() {
		once.Do(func() {
			go func() { service.StopAndWait(); close(stopped) }()
			for {
				service.mu.Lock()
				stopping := service.stopping
				service.mu.Unlock()
				if stopping {
					return
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
	view, _ := queuedMerge(t, service, media)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		close(media.block)
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
		}
		t.Fatal("StopAndWait 落在领取与登记取消之间时卡住，等到了整段编码结束")
	}
	if items := loadEditItemsForTest(t, view.ID); items[0].Status != models.VideoEditStatusInterrupted {
		t.Fatalf("被领取的项应以 interrupted 收尾: %+v", items[0])
	}
}

// 评审 7：启动对账之前 worker 不能领上一次进程留下的排队项（它们由 RecoverOnStartup 改成中断）。
func TestVideoEditWorkerSkipsItemsQueuedBeforeBoot(t *testing.T) {
	service, media := newFakeEditService(t)
	old := seedRecoverProject(t, models.VideoEditStatusQueued)
	before := service.bootTime.Add(-time.Minute)
	if err := database.DB.Model(&old).Update("queued_at", &before).Error; err != nil {
		t.Fatal(err)
	}
	leftover := seedRecoverItem(t, old, models.VideoEditStatusQueued, t.TempDir(), "", 0)
	view, _ := queuedMerge(t, service, media)
	if done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusCompleted, models.VideoEditStatusFailed); done.Status != models.VideoEditStatusCompleted {
		t.Fatalf("本次排队的项目应完成: %+v", done)
	}
	if items := loadEditItemsForTest(t, old.ID); items[0].ID != leftover.ID || items[0].Status != models.VideoEditStatusQueued {
		t.Fatalf("上一次进程留下的排队项不能被 worker 领取: %+v", items[0])
	}
}
