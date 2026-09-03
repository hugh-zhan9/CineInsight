package services

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

// waitForNotifications 等到 stub 收到 want 条通知；多一条也算失败（"发一次且仅一次"）。
// 各服务把通知发在终态之后、调用方返回之前后仍有毫秒级的调度余量，故用轮询。
func waitForNotifications(t *testing.T, stub *stubDesktopNotifier, want int) []stubNotification {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := stub.Notifications()
		if len(got) == want {
			// 再等一小会确认没有第二条重复通知。
			time.Sleep(50 * time.Millisecond)
			if extra := stub.Notifications(); len(extra) != want {
				t.Fatalf("通知条数应为 %d，实际 %d：%+v", want, len(extra), extra)
			}
			return got
		}
		if len(got) > want {
			t.Fatalf("通知条数应为 %d，实际 %d：%+v", want, len(got), got)
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %d 条通知超时，实际 %+v", want, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertNotification(t *testing.T, got stubNotification, wantTitle, wantBodyFragment string) {
	t.Helper()
	if got.Title != wantTitle {
		t.Fatalf("通知标题 = %q，期望 %q", got.Title, wantTitle)
	}
	if !strings.Contains(got.Body, wantBodyFragment) {
		t.Fatalf("通知正文 %q 应包含 %q", got.Body, wantBodyFragment)
	}
	// 通知会进系统通知中心，绝对路径不该出现在那里（D-013）。
	if strings.Contains(got.Body, "/") {
		t.Fatalf("通知正文不该出现路径分隔符: %q", got.Body)
	}
}

// 字幕三种终态各发一条（成功 / 失败 / 需确认幻觉），取消不发。
func TestSubtitleQueueNotifiesEachTerminalStateOnce(t *testing.T) {
	stub := &stubDesktopNotifier{}
	queue := newSubtitleTaskQueue(nil, func(ctx context.Context, task *subtitleQueueTask) (*SubtitleGenerateResult, error) {
		switch task.Request.VideoID {
		case 1:
			return &SubtitleGenerateResult{Status: SubtitleResultStatusSuccess, VideoID: 1}, nil
		case 2:
			return nil, errors.New("未找到 FFmpeg，请重新安装依赖")
		case 3:
			return &SubtitleGenerateResult{
				Status:         SubtitleResultStatusValidationFailed,
				VideoID:        3,
				ValidationCode: SubtitleValidationCodeHallucinationDetected,
				ForceEligible:  true,
			}, nil
		default:
			return &SubtitleGenerateResult{Status: SubtitleResultStatusCancelled, VideoID: task.Request.VideoID}, nil
		}
	})
	queue.setDesktopNotifier(stub)

	if _, err := queue.submit(&subtitleQueueTask{
		Request: SubtitleGenerateRequest{VideoID: 1, Engine: SubtitleEngineWhisperX}, VideoName: "one.mp4",
	}); err != nil {
		t.Fatalf("成功任务不该报错: %v", err)
	}
	if _, err := queue.submit(&subtitleQueueTask{
		Request: SubtitleGenerateRequest{VideoID: 2, Engine: SubtitleEngineWhisperX}, VideoName: "two.mp4",
	}); err == nil {
		t.Fatal("失败任务应返回错误")
	}
	if _, err := queue.submit(&subtitleQueueTask{
		Request: SubtitleGenerateRequest{VideoID: 3, Engine: SubtitleEngineWhisperX}, VideoName: "three.mp4",
	}); err != nil {
		t.Fatalf("校验失败按结果返回，不该报错: %v", err)
	}

	got := waitForNotifications(t, stub, 3)
	assertNotification(t, got[0], "字幕生成完成", "one.mp4")
	assertNotification(t, got[1], "字幕生成失败", "two.mp4")
	assertNotification(t, got[2], "字幕需要确认", "three.mp4")
}

func TestSubtitleQueueDoesNotNotifyCancelledTask(t *testing.T) {
	stub := &stubDesktopNotifier{}
	queue := newSubtitleTaskQueue(nil, func(ctx context.Context, task *subtitleQueueTask) (*SubtitleGenerateResult, error) {
		return &SubtitleGenerateResult{Status: SubtitleResultStatusCancelled, VideoID: task.Request.VideoID}, nil
	})
	queue.setDesktopNotifier(stub)

	if _, err := queue.submit(&subtitleQueueTask{
		Request: SubtitleGenerateRequest{VideoID: 9, Engine: SubtitleEngineWhisperX}, VideoName: "nine.mp4",
	}); err != nil {
		t.Fatalf("取消不该报错: %v", err)
	}

	if got := stub.Notifications(); len(got) != 0 {
		t.Fatalf("用户取消的字幕任务不该发通知，实际 %+v", got)
	}
}

// 前台抑制与开关关闭在服务这一层同样生效：服务拿到的就是通知中心。
func TestSubtitleQueueRespectsForegroundAndSwitch(t *testing.T) {
	stub := &stubDesktopNotifier{}
	enabled := true
	center := NewDesktopNotificationCenter(stub, func() bool { return enabled })
	queue := newSubtitleTaskQueue(nil, func(ctx context.Context, task *subtitleQueueTask) (*SubtitleGenerateResult, error) {
		return &SubtitleGenerateResult{Status: SubtitleResultStatusSuccess, VideoID: task.Request.VideoID}, nil
	})
	queue.setDesktopNotifier(center)

	center.SetForeground(true)
	if _, err := queue.submit(&subtitleQueueTask{
		Request: SubtitleGenerateRequest{VideoID: 1, Engine: SubtitleEngineWhisperX}, VideoName: "one.mp4",
	}); err != nil {
		t.Fatal(err)
	}
	if got := stub.Notifications(); len(got) != 0 {
		t.Fatalf("窗口在前台时字幕完成不该发系统通知，实际 %+v", got)
	}

	center.SetForeground(false)
	enabled = false
	if _, err := queue.submit(&subtitleQueueTask{
		Request: SubtitleGenerateRequest{VideoID: 2, Engine: SubtitleEngineWhisperX}, VideoName: "two.mp4",
	}); err != nil {
		t.Fatal(err)
	}
	if got := stub.Notifications(); len(got) != 0 {
		t.Fatalf("开关关掉时字幕完成不该发系统通知，实际 %+v", got)
	}

	enabled = true
	if _, err := queue.submit(&subtitleQueueTask{
		Request: SubtitleGenerateRequest{VideoID: 3, Engine: SubtitleEngineWhisperX}, VideoName: "three.mp4",
	}); err != nil {
		t.Fatal(err)
	}
	got := waitForNotifications(t, stub, 1)
	assertNotification(t, got[0], "字幕生成完成", "three.mp4")
}

func TestEnhancementCompletionNotifiesOnce(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	service := newEnhancementTestService(t, fakeEnhancementCommands(t, video.Path, 250, 24))
	stub := &stubDesktopNotifier{}
	service.SetDesktopNotifier(stub)

	view, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"})
	if err != nil {
		t.Fatal(err)
	}
	waitEnhancementTask(t, view.ID, models.EnhancementStatusCompleted)
	service.StopAndWait()

	got := waitForNotifications(t, stub, 1)
	assertNotification(t, got[0], "视频超分完成", "movie.mp4")
}

func TestEnhancementFailureNotifiesOnce(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	service := newEnhancementTestService(t, fakeEnhancementCommands(t, video.Path, 250, 24))
	stub := &stubDesktopNotifier{}
	service.SetDesktopNotifier(stub)

	service.stopping = true
	view, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"})
	if err != nil {
		t.Fatal(err)
	}
	// 排队后改源文件：preflight 的哈希对不上，任务以 source_changed 失败。
	if err := os.WriteFile(video.Path, []byte("source-video-content-changed"), 0644); err != nil {
		t.Fatal(err)
	}
	service.stopping = false
	service.ensureWorker()
	waitEnhancementTask(t, view.ID, models.EnhancementStatusFailed)
	service.StopAndWait()

	got := waitForNotifications(t, stub, 1)
	assertNotification(t, got[0], "视频超分失败", "movie.mp4")
}

func TestBackupFailureNotifiesOnce(t *testing.T) {
	service, runner, _ := setupBackupServiceTest(t, 7, 24)
	stub := &stubDesktopNotifier{}
	service.SetDesktopNotifier(stub)
	runner.errFor["pg_dump"] = errors.New("pg_dump: no space left on device")

	if _, err := service.CreateBackup(context.Background()); err == nil {
		t.Fatal("备份失败必须返回错误")
	}

	got := waitForNotifications(t, stub, 1)
	assertNotification(t, got[0], "数据库备份失败", "备份未完成")
}

func TestBackupSuccessDoesNotNotify(t *testing.T) {
	service, _, _ := setupBackupServiceTest(t, 7, 24)
	stub := &stubDesktopNotifier{}
	service.SetDesktopNotifier(stub)

	if _, err := service.CreateBackup(context.Background()); err != nil {
		t.Fatalf("备份应成功: %v", err)
	}

	if got := stub.Notifications(); len(got) != 0 {
		t.Fatalf("备份成功不在触发清单里，实际 %+v", got)
	}
}

func TestSemanticIndexCompletionAndFailureNotifyOnce(t *testing.T) {
	capability := setupSemanticIndexTestDB(t)
	if err := database.DB.Create(&models.Video{Name: "movie.mp4", DisplayTitle: "夜行", Path: "/library/movie.mp4"}).Error; err != nil {
		t.Fatalf("建视频失败: %v", err)
	}
	provider := SemanticIndexConfigProviderFunc(func() (SemanticIndexConfig, error) {
		return SemanticIndexConfig{BaseURL: "http://unused", Model: "embed-v1"}, nil
	})

	service := NewSemanticIndexService(database.DB, capability, provider)
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			return []float64{0.1, 0.2, 0.3}, nil
		})
	}
	stub := &stubDesktopNotifier{}
	service.SetDesktopNotifier(stub)
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); err != nil {
		t.Fatalf("启动语义索引失败: %v", err)
	}
	status := waitSemanticIndex(t, service)
	if !status.Completed || status.Succeeded != 1 {
		t.Fatalf("索引应完成一项: %+v", status)
	}
	got := waitForNotifications(t, stub, 1)
	assertNotification(t, got[0], "视频语义索引完成", "已索引 1 项")

	failing := NewSemanticIndexService(database.DB, capability, provider)
	failing.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			return nil, errors.New("embedding endpoint unreachable")
		})
	}
	failingStub := &stubDesktopNotifier{}
	failing.SetDesktopNotifier(failingStub)
	if _, err := failing.Start(context.Background(), SemanticIndexBuildRequest{Rebuild: true}); err != nil {
		t.Fatalf("启动重建失败: %v", err)
	}
	failedStatus := waitSemanticIndex(t, failing)
	if failedStatus.Failed == 0 {
		t.Fatalf("应记录失败项: %+v", failedStatus)
	}
	failedNotifications := waitForNotifications(t, failingStub, 1)
	assertNotification(t, failedNotifications[0], "视频语义索引失败", "1 项索引失败")
}

func TestSemanticIndexCancelDoesNotNotify(t *testing.T) {
	capability := setupSemanticIndexTestDB(t)
	if err := database.DB.Create(&models.Video{Name: "movie.mp4", DisplayTitle: "夜行", Path: "/library/movie.mp4"}).Error; err != nil {
		t.Fatalf("建视频失败: %v", err)
	}
	release := make(chan struct{})
	service := NewSemanticIndexService(database.DB, capability, SemanticIndexConfigProviderFunc(func() (SemanticIndexConfig, error) {
		return SemanticIndexConfig{BaseURL: "http://unused", Model: "embed-v1"}, nil
	}))
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(ctx context.Context, _ string) ([]float64, error) {
			close(release)
			<-ctx.Done()
			return nil, ctx.Err()
		})
	}
	stub := &stubDesktopNotifier{}
	service.SetDesktopNotifier(stub)
	if _, err := service.Start(context.Background(), SemanticIndexBuildRequest{}); err != nil {
		t.Fatalf("启动语义索引失败: %v", err)
	}
	<-release
	if err := service.Cancel(); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	waitSemanticIndex(t, service)

	if got := stub.Notifications(); len(got) != 0 {
		t.Fatalf("取消的语义索引不该发通知，实际 %+v", got)
	}
}

func TestImageSemanticIndexCompletionNotifiesOnce(t *testing.T) {
	capability := setupImageSemanticIndexTestDB(t)
	createImageSemanticTestImage(t, "photo.jpg")
	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			return []float64{0.1, 0.2, 0.3}, nil
		})
	}
	stub := &stubDesktopNotifier{}
	service.SetDesktopNotifier(stub)

	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("启动图片语义索引失败: %v", err)
	}
	status := waitImageSemanticIndex(t, service)
	if !status.Completed || status.Succeeded != 1 {
		t.Fatalf("图片索引应完成一项: %+v", status)
	}

	got := waitForNotifications(t, stub, 1)
	assertNotification(t, got[0], "图片语义索引完成", "已索引 1 项")
}

func TestImageSemanticIndexFailureNotifiesOnce(t *testing.T) {
	capability := setupImageSemanticIndexTestDB(t)
	createImageSemanticTestImage(t, "photo.jpg")
	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			return nil, errors.New("embedding endpoint unreachable")
		})
	}
	stub := &stubDesktopNotifier{}
	service.SetDesktopNotifier(stub)

	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("启动图片语义索引失败: %v", err)
	}
	status := waitImageSemanticIndex(t, service)
	if status.Failed == 0 {
		t.Fatalf("应记录失败项: %+v", status)
	}

	got := waitForNotifications(t, stub, 1)
	assertNotification(t, got[0], "图片语义索引失败", "1 项索引失败")
}
