package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func seedPerceptualHashVideos(t *testing.T, names ...string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name), 0644); err != nil {
			t.Fatalf("写入视频文件失败: %v", err)
		}
		if err := database.DB.Create(&models.Video{Name: name, Path: path, Directory: root, Duration: 100}).Error; err != nil {
			t.Fatalf("创建视频失败: %v", err)
		}
	}
}

func newFakeHashService() *PerceptualHashService {
	service := NewPerceptualHashService()
	service.runner = &fakePerceptualFrameRunner{frames: [][]byte{gradientPerceptualFrame(false)}}
	return service
}

func waitForPerceptualHashStatus(t *testing.T, service *PerceptualHashService, accept func(PerceptualHashStatus) bool) PerceptualHashStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var status PerceptualHashStatus
	for time.Now().Before(deadline) {
		status = service.Status()
		if accept(status) {
			return status
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待感知哈希状态超时，实际 %+v", status)
	return status
}

// 项间检查点接到真实 worker 上（AC-21）：用户活跃时任务在处理下一项之前停住，
// 状态标成 waiting_idle；「忽略空闲立即运行」之后一路跑完。
//
// 同时钉住 bypass 的作用范围：它豁免的是这一轮，不是一项——否则第二个视频
// 又会卡住，界面上就成了"点一次只动一格"。
func TestPerceptualHashPauseHookWaitsThenRunsAfterBypass(t *testing.T) {
	setupVideoServiceTestDB(t)
	seedPerceptualHashVideos(t, "one.mp4", "two.mp4")

	gate := busyGate()
	registry := NewBackgroundTaskRegistry()
	registry.SetOnChange(gate.SyncRunningTasks)
	service := newFakeHashService()
	service.SetBackgroundTaskRegistry(registry)

	if _, err := service.StartWithPauseHook(context.Background(), gate.PauseHook(string(BackgroundTaskPerceptualHash))); err != nil {
		t.Fatalf("启动感知哈希失败: %v", err)
	}
	defer service.StopAndWait()

	// 任务在跑（登记表看得到），但第一项还没开始处理：卡在门口。
	if tasks := registry.Snapshot(); len(tasks) != 1 || tasks[0] != "phash" {
		t.Fatalf("运行中的任务应登记为 phash: %v", tasks)
	}
	status := waitForPerceptualHashStatus(t, service, func(status PerceptualHashStatus) bool { return status.Gate.WaitingIdle })
	if status.Gate.Reason != IdleWaitReasonUserActive {
		t.Fatalf("用户活跃时状态应为 waiting_idle/user_active: %+v", status.Gate)
	}
	if status.Processed != 0 {
		t.Fatalf("等待空闲期间不该处理任何一项，实际 processed=%d", status.Processed)
	}

	if err := gate.RunGatedTaskNow(string(BackgroundTaskPerceptualHash)); err != nil {
		t.Fatalf("立即运行失败: %v", err)
	}

	status = waitForPerceptualHashStatus(t, service, func(status PerceptualHashStatus) bool {
		return status.Completed && !status.Running
	})
	if status.Succeeded != 2 {
		t.Fatalf("两个视频都该处理完，实际 succeeded=%d（bypass 只豁免一项？）: %+v", status.Succeeded, status)
	}
	if status.Gate.WaitingIdle {
		t.Fatalf("收尾后应清掉门状态: %+v", status.Gate)
	}
	if tasks := registry.Snapshot(); len(tasks) != 0 {
		t.Fatalf("任务结束后登记表应清空: %v", tasks)
	}
	// 这一轮跑完，bypass 不许留到下一轮。
	if bypass := gate.GetIdleSchedulerStatus().BypassTasks; len(bypass) != 0 {
		t.Fatalf("跑完之后 bypass 应被清掉: %v", bypass)
	}
}

// 显式启动摘掉钩子，必须把已经停在检查点上的 worker 立刻放出来继续跑（Important 6）。
func TestPerceptualHashExplicitStartReleasesParkedWorker(t *testing.T) {
	setupVideoServiceTestDB(t)
	seedPerceptualHashVideos(t, "one.mp4", "two.mp4")

	gate := busyGate()
	service := newFakeHashService()
	if _, err := service.StartWithPauseHook(context.Background(), gate.PauseHook(string(BackgroundTaskPerceptualHash))); err != nil {
		t.Fatalf("启动感知哈希失败: %v", err)
	}
	defer service.StopAndWait()
	waitForPerceptualHashStatus(t, service, func(status PerceptualHashStatus) bool { return status.Gate.WaitingIdle })

	// 用户点了「补全」：显式启动摘掉这一轮的钩子。
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("显式启动失败: %v", err)
	}
	status := waitForPerceptualHashStatus(t, service, func(status PerceptualHashStatus) bool {
		return status.Completed && !status.Running
	})
	if status.Succeeded != 2 {
		t.Fatalf("摘钩子后应一路跑完，实际 %+v", status)
	}
	if status.Gate.WaitingIdle {
		t.Fatalf("钩子返回后才清门状态，收尾时应为零值: %+v", status.Gate)
	}
	if waiting := gate.GetIdleSchedulerStatus().Waiting; len(waiting) != 0 {
		t.Fatalf("放行后不该留下等待项: %+v", waiting)
	}
}

// 自动路径不给"已经在跑的那一轮"补装钩子：那一轮可能是用户显式启动的（Important 1）。
func TestPerceptualHashStartWithPauseHookDoesNotGateRunningExplicitTask(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "one.mp4")
	if err := os.WriteFile(path, []byte("one"), 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}
	if err := database.DB.Create(&models.Video{Name: "one.mp4", Path: path, Directory: root, Duration: 100}).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	gate := busyGate()
	service := NewPerceptualHashService()
	runner := &blockingPerceptualFrameRunner{started: make(chan struct{}), release: make(chan struct{})}
	service.runner = runner

	// 用户显式启动，worker 停在第一项的抽帧上。
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("显式启动失败: %v", err)
	}
	<-runner.started

	// 自动路径此刻插进来：只会拿到"已在运行"，不许给这一轮装上检查点。
	if _, err := service.StartWithPauseHook(context.Background(), gate.PauseHook(string(BackgroundTaskPerceptualHash))); err != nil {
		t.Fatalf("自动启动应返回已在运行: %v", err)
	}
	service.mu.Lock()
	hook := service.pauseHook
	service.mu.Unlock()
	if hook != nil {
		t.Fatal("显式启动的那一轮不该被补装项间检查点")
	}

	// 先放开 runner 再收尾：它的 Frame 会等 ctx 取消、再等 release，
	// 反过来的话 StopAndWait 就永远等不到 worker 退出。
	close(runner.release)
	service.StopAndWait()
}

// 停在检查点上的任务仍然可以取消：Cancel 立刻生效，不用等空闲。
func TestPerceptualHashCancelWhileWaitingIdle(t *testing.T) {
	setupVideoServiceTestDB(t)
	seedPerceptualHashVideos(t, "one.mp4")

	gate := busyGate()
	service := newFakeHashService()
	if _, err := service.StartWithPauseHook(context.Background(), gate.PauseHook(string(BackgroundTaskPerceptualHash))); err != nil {
		t.Fatalf("启动感知哈希失败: %v", err)
	}
	defer service.StopAndWait()
	waitForPerceptualHashStatus(t, service, func(status PerceptualHashStatus) bool { return status.Gate.WaitingIdle })

	if err := service.Cancel(); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	status := waitForPerceptualHashStatus(t, service, func(status PerceptualHashStatus) bool { return !status.Running })
	if !status.Cancelled {
		t.Fatalf("等待空闲期间取消应立刻收尾为已取消: %+v", status)
	}
	if status.Processed != 0 {
		t.Fatalf("取消时不该处理任何一项: %+v", status)
	}
	if waiting := gate.GetIdleSchedulerStatus().Waiting; len(waiting) != 0 {
		t.Fatalf("取消后不该留下等待项: %+v", waiting)
	}
}

// 没装钩子（显式启动）时任务一路直通，门里也不该出现等待项。
func TestPerceptualHashWithoutPauseHookRunsWithoutWaiting(t *testing.T) {
	setupVideoServiceTestDB(t)
	seedPerceptualHashVideos(t, "one.mp4")

	gate := busyGate()
	service := newFakeHashService()
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("启动感知哈希失败: %v", err)
	}
	defer service.StopAndWait()

	status := waitForPerceptualHashStatus(t, service, func(status PerceptualHashStatus) bool {
		return status.Completed && !status.Running
	})
	if status.Succeeded != 1 {
		t.Fatalf("显式启动应直接跑完: %+v", status)
	}
	if status.Gate.WaitingIdle {
		t.Fatalf("显式启动不该有门状态: %+v", status.Gate)
	}
	if waiting := gate.GetIdleSchedulerStatus().Waiting; len(waiting) != 0 {
		t.Fatalf("显式启动不该登记等待: %+v", waiting)
	}
}

// 图片 AI 打标的自动路径同样不能给"已经在跑的那一轮"补装钩子（Important 2）：
// 那一轮可能是用户在照片页显式点的「开始/继续生成」。
//
// 直接把服务摆成"正在跑"（busy 是它自己的运行标记），比拉起一整套 AI 夹具更能
// 精确地钉住这条规则：装钩子与判 Running 必须在同一把锁里，且已在跑就不装。
func TestImageAITaggingStartWithPauseHookDoesNotGateRunningExplicitTask(t *testing.T) {
	setupImageAITaggingTestDB(t)
	service := newImageAITaggingTestServiceWithThumbnailData(t, nil, imageAITaggingTestJPEG(t))
	service.mu.Lock()
	service.busy = true
	service.status.Running = true
	service.mu.Unlock()

	gate := busyGate()
	if _, err := service.StartImageAITaggingWithPauseHook(context.Background(), gate.PauseHook(string(BackgroundTaskImageAITagging))); !errors.Is(err, ErrImageAITaggingBusy) {
		t.Fatalf("自动启动应返回已在运行，实际 %v", err)
	}
	service.mu.Lock()
	hook := service.pauseHook
	service.mu.Unlock()
	if hook != nil {
		t.Fatal("显式启动的那一轮不该被补装项间检查点")
	}
	if waiting := gate.GetIdleSchedulerStatus().Waiting; len(waiting) != 0 {
		t.Fatalf("被拒绝的自动启动不该留下等待项: %+v", waiting)
	}
}
