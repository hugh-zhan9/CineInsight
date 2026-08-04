package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

func enhancementProbeJSON(width, height int, duration float64, extra string) string {
	return fmt.Sprintf(`{"format":{"duration":"%.3f"},"streams":[{"codec_type":"video","width":%d,"height":%d,"pix_fmt":"yuv420p","avg_frame_rate":"24/1","r_frame_rate":"24/1"%s},{"codec_type":"audio"}]}`,
		duration, width, height, extra)
}

// fakeEnhancementCommands 在进程内模拟 ffprobe/ffmpeg/sidecar 的行为。
func fakeEnhancementCommands(t *testing.T, sourcePath string, totalFrames int64, fps float64) enhancementCommandRunner {
	t.Helper()
	return func(ctx context.Context, name string, args []string) (string, error) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(name, "ffprobe") && strings.Contains(joined, "-count_packets"):
			return strconv.FormatInt(totalFrames, 10), nil
		case strings.Contains(name, "ffprobe"):
			path := args[len(args)-1]
			duration := float64(totalFrames) / fps
			if strings.HasSuffix(path, ".cispart") || strings.Contains(path, ".enhanced-") {
				return enhancementProbeJSON(640, 480, duration, ""), nil
			}
			return enhancementProbeJSON(320, 240, duration, ""), nil
		case strings.Contains(joined, "-f image2"):
			// 抽帧：向输出模板目录写 N 张 PNG。
			pattern := args[len(args)-1]
			dir := filepath.Dir(pattern)
			frames, _ := strconv.ParseInt(args[indexOfArg(args, "-frames:v")+1], 10, 64)
			for index := int64(1); index <= frames; index++ {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%06d.png", index)), []byte("png"), 0644); err != nil {
					return "", err
				}
			}
			return "", nil
		case strings.Contains(name, "sidecar"):
			inDir := args[indexOfArg(args, "-i")+1]
			outDir := args[indexOfArg(args, "-o")+1]
			entries, err := os.ReadDir(inDir)
			if err != nil {
				return "", err
			}
			for _, entry := range entries {
				if err := os.WriteFile(filepath.Join(outDir, entry.Name()), []byte("png2x"), 0644); err != nil {
					return "", err
				}
			}
			return "", nil
		case strings.Contains(joined, "-f concat"):
			staging := args[len(args)-1]
			return "", os.WriteFile(staging, []byte("final-matroska"), 0644)
		case strings.Contains(joined, "-f matroska"):
			segment := args[len(args)-1]
			return "", os.WriteFile(segment, []byte("segment"), 0644)
		case strings.Contains(joined, "-f null"):
			return "", nil
		}
		return "", fmt.Errorf("unexpected command: %s %s", name, joined)
	}
}

func indexOfArg(args []string, flag string) int {
	for index, arg := range args {
		if arg == flag {
			return index
		}
	}
	return -1
}

func newEnhancementTestService(t *testing.T, runner enhancementCommandRunner) *EnhancementService {
	t.Helper()
	service := NewEnhancementService(&VideoService{}, NewMediaProbeService(), nil)
	service.capability = EnhancementRuntimeCapability{
		Available: true, RuntimeVersion: EnhancementRuntimeIdentity,
		BinaryPath: "sidecar", ModelDir: "models",
	}
	service.runCommand = runner
	service.runCommandOutput = func(ctx context.Context, name string, args []string) (string, string, error) {
		out, err := runner(ctx, name, args)
		return out, "", err
	}
	service.diskFree = func(string) (uint64, error) { return 1 << 62, nil }
	service.findFFmpeg = func() (string, error) { return "ffmpeg", nil }
	service.findFFprobe = func() (string, error) { return "ffprobe", nil }
	return service
}

func createEnhancementSourceVideo(t *testing.T, name string) models.Video {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("source-video-content"), 0644); err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: name, Path: path, Directory: root, Size: int64(len("source-video-content")), Duration: 5}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	return video
}

func waitEnhancementTask(t *testing.T, taskID uint, wantStatus string) models.VideoEnhancementTask {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var task models.VideoEnhancementTask
		if err := database.DB.First(&task, taskID).Error; err == nil && task.Status == wantStatus {
			return task
		} else if err == nil && (task.Status == models.EnhancementStatusFailed || task.Status == models.EnhancementStatusCancelled) && wantStatus == models.EnhancementStatusCompleted {
			t.Fatalf("任务提前失败: %+v", task)
		}
		if time.Now().After(deadline) {
			var task models.VideoEnhancementTask
			_ = database.DB.First(&task, taskID).Error
			t.Fatalf("等待任务状态 %s 超时: %+v", wantStatus, task)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEnhancementCreateTaskRejectsClosedSetViolations(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	cases := []struct {
		name string
		json string
		want string
	}{
		{"HDR", enhancementProbeJSON(1280, 720, 10, `,"color_transfer":"smpte2084"`), "HDR"},
		{"interlaced", enhancementProbeJSON(1280, 720, 10, `,"field_order":"tt"`), "逐行"},
		{"oversize", enhancementProbeJSON(2560, 1440, 10, ""), "1920×1080"},
		{"ten-bit", strings.Replace(enhancementProbeJSON(1280, 720, 10, ""), "yuv420p", "yuv420p10le", 1), "8-bit"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service := newEnhancementTestService(t, func(ctx context.Context, name string, args []string) (string, error) {
				return testCase.json, nil
			})
			_, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"})
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("want rejection containing %q, got %v", testCase.want, err)
			}
		})
	}
}

func TestEnhancementCreateTaskChecksConflictAndDisk(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	probeOnly := func(ctx context.Context, name string, args []string) (string, error) {
		return enhancementProbeJSON(1280, 720, 10, ""), nil
	}

	conflictPath := filepath.Join(video.Directory, "movie.enhanced-general-2x.mkv")
	if err := os.WriteFile(conflictPath, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	service := newEnhancementTestService(t, probeOnly)
	if _, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"}); err == nil || !strings.Contains(err.Error(), "output_conflict") {
		t.Fatalf("want output_conflict, got %v", err)
	}
	_ = os.Remove(conflictPath)

	service = newEnhancementTestService(t, probeOnly)
	service.diskFree = func(string) (uint64, error) { return 1 << 20, nil }
	if _, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"}); err == nil || !strings.Contains(err.Error(), "disk_insufficient") {
		t.Fatalf("want disk_insufficient, got %v", err)
	}
}

func TestEnhancementCreateTaskIsIdempotentForActiveTask(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	service := newEnhancementTestService(t, func(ctx context.Context, name string, args []string) (string, error) {
		return enhancementProbeJSON(1280, 720, 10, ""), nil
	})
	// 阻止 worker 抢跑，专注队列语义。
	service.stopping = true
	first, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Profile != "general" {
		t.Fatalf("重复创建应返回既有活跃任务: first=%d second=%d profile=%s", first.ID, second.ID, second.Profile)
	}
	if err := service.CancelTask(first.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := waitEnhancementTask(t, first.ID, models.EnhancementStatusCancelled)
	if cancelled.ErrorCode != "cancelled" {
		t.Fatalf("queued 取消应立即终态: %+v", cancelled)
	}
}

func TestEnhancementPipelineCompletesAndPublishesAtomically(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	const totalFrames = 250 // 3 个批次（120+120+10）
	runner := fakeEnhancementCommands(t, video.Path, totalFrames, 24)
	service := newEnhancementTestService(t, runner)

	view, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"})
	if err != nil {
		t.Fatal(err)
	}
	task := waitEnhancementTask(t, view.ID, models.EnhancementStatusCompleted)
	service.StopAndWait()

	if task.TotalFrames != totalFrames || task.CommittedFrames != totalFrames {
		t.Fatalf("帧数记账错误: %+v", task)
	}
	if task.OutputVideoID == nil || task.RelationID == nil {
		t.Fatalf("完成任务必须携带输出与关系 ID: %+v", task)
	}
	if task.SourceSHA256 == "" {
		t.Fatalf("preflight 必须固化源哈希")
	}

	targetPath := filepath.Join(video.Directory, "movie.enhanced-general-2x.mkv")
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("输出文件缺失: %v", err)
	}
	var output models.Video
	if err := database.DB.First(&output, *task.OutputVideoID).Error; err != nil || output.Path != targetPath {
		t.Fatalf("输出视频记录错误: %+v err=%v", output, err)
	}
	var relation models.VideoSameSourceRelation
	if err := database.DB.First(&relation, *task.RelationID).Error; err != nil {
		t.Fatal(err)
	}
	if relation.VideoAID != video.ID || relation.VideoBID != output.ID {
		t.Fatalf("同源关系对未规范化: %+v", relation)
	}
	if relation.Status != models.VideoSameSourceStatusDetected || relation.IsUnread || relation.ReviewedAt == nil {
		t.Fatalf("生成关系必须已确认且不进入待审: %+v", relation)
	}
	if !strings.HasPrefix(relation.DetectionVersion, "enhancement-v1:general:") {
		t.Fatalf("detection_version=%q", relation.DetectionVersion)
	}
	if digest, err := sha256File(video.Path); err != nil || digest != task.SourceSHA256 {
		t.Fatalf("原文件必须保持不变: %v", err)
	}
	if _, err := os.Stat(enhancementWorkdir(models.VideoEnhancementTask{ID: task.ID, Video: video})); !os.IsNotExist(err) {
		t.Fatalf("完成后必须清理工作目录: %v", err)
	}

	// 相同源再次创建：目标名冲突必须拒绝。
	if _, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"}); err == nil || !strings.Contains(err.Error(), "output_conflict") {
		t.Fatalf("已发布输出后重复创建应 output_conflict: %v", err)
	}
}

func TestEnhancementSourceChangeFailsTaskAndCleansUp(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	runner := fakeEnhancementCommands(t, video.Path, 250, 24)
	service := newEnhancementTestService(t, runner)
	service.stopping = true
	view, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"})
	if err != nil {
		t.Fatal(err)
	}
	// 排队后修改源文件。
	if err := os.WriteFile(video.Path, []byte("source-video-content-changed"), 0644); err != nil {
		t.Fatal(err)
	}
	service.stopping = false
	service.ensureWorker()
	task := waitEnhancementTask(t, view.ID, models.EnhancementStatusFailed)
	service.StopAndWait()
	if task.ErrorCode != "source_changed" {
		t.Fatalf("error_code=%q", task.ErrorCode)
	}
	if _, err := os.Stat(enhancementWorkdir(models.VideoEnhancementTask{ID: task.ID, Video: video})); !os.IsNotExist(err) {
		t.Fatalf("失败后必须清理工作目录")
	}
}

func TestEnhancementRecoverOnStartupFailsTasksWhenRuntimeUnavailable(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	task := models.VideoEnhancementTask{
		VideoID: video.ID, Profile: "general", Scale: 2,
		Status: models.EnhancementStatusRunning, Phase: models.EnhancementPhaseEnhance,
		SourceSize: video.Size, OutputBasename: "movie.enhanced-general-2x.mkv",
		RuntimeVersion: EnhancementRuntimeIdentity,
	}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	service := NewEnhancementService(&VideoService{}, NewMediaProbeService(), nil)
	service.capability = EnhancementRuntimeCapability{ReasonCode: "runtime_unavailable", Message: "缺少运行时"}
	service.RecoverOnStartup(context.Background())
	var reloaded models.VideoEnhancementTask
	if err := database.DB.First(&reloaded, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != models.EnhancementStatusFailed || reloaded.ErrorCode != "runtime_unavailable" {
		t.Fatalf("运行时不可用应把活跃任务置失败: %+v", reloaded)
	}
}

func TestEnhancementCancelDuringChunkReachesCancelledAndCleansWorkdir(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	base := fakeEnhancementCommands(t, video.Path, 250, 24)
	sidecarStarted := make(chan struct{}, 1)
	runner := func(ctx context.Context, name string, args []string) (string, error) {
		if strings.Contains(name, "sidecar") {
			select {
			case sidecarStarted <- struct{}{}:
			default:
			}
			<-ctx.Done() // 模拟被杀死的推理进程
			return "", ctx.Err()
		}
		return base(ctx, name, args)
	}
	service := newEnhancementTestService(t, runner)
	view, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-sidecarStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("推理未开始")
	}
	if err := service.CancelTask(view.ID); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	task := waitEnhancementTask(t, view.ID, models.EnhancementStatusCancelled)
	service.StopAndWait()
	if task.ErrorCode != "cancelled" {
		t.Fatalf("error_code=%q", task.ErrorCode)
	}
	if _, err := os.Stat(enhancementWorkdir(models.VideoEnhancementTask{ID: task.ID, Video: video})); !os.IsNotExist(err) {
		t.Fatalf("取消后必须清理工作目录")
	}
	// 取消幂等：再次取消返回 nil。
	if err := service.CancelTask(view.ID); err != nil {
		t.Fatalf("重复取消应幂等: %v", err)
	}
	// 部分唯一索引语义：取消终态后可再次创建任务。
	if _, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"}); err != nil {
		t.Fatalf("取消后应可重新创建: %v", err)
	}
	service.StopAndWait()
}

func TestEnhancementManifestVerification(t *testing.T) {
	workdir := t.TempDir()
	manifest := enhancementSegmentManifest{Segments: []enhancementSegment{
		{Index: 0, Name: "seg-00000.cispart", Frames: 120},
		{Index: 1, Name: "seg-00001.cispart", Frames: 120},
	}}
	if err := os.WriteFile(filepath.Join(workdir, "seg-00000.cispart"), []byte("segment-a"), 0644); err != nil {
		t.Fatal(err)
	}
	digest, err := sha256File(filepath.Join(workdir, "seg-00000.cispart"))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Segments[0].Size = int64(len("segment-a"))
	manifest.Segments[0].SHA256 = digest

	// 第二段缺失：在缺口处截断（未提交批次重做）。
	work := manifest
	committed, err := manifestCommittedFrames(workdir, &work)
	if err != nil || committed != 120 || len(work.Segments) != 1 {
		t.Fatalf("缺失分段应截断: committed=%d err=%v", committed, err)
	}

	// 已提交分段被篡改：必须报错而不是拼接坏数据。
	if err := os.WriteFile(filepath.Join(workdir, "seg-00000.cispart"), []byte("tampered!"), 0644); err != nil {
		t.Fatal(err)
	}
	work = manifest
	if _, err := manifestCommittedFrames(workdir, &work); err == nil {
		t.Fatalf("哈希不符必须失败")
	}
}
