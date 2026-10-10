package services

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// 真实运行时冒烟（默认跳过）：CINEINSIGHT_SCENE_RUNTIME_IT=1 时在临时数据目录里跑一次真实的
// 准备（托管 Python / venv / 逐个 pin 的依赖 / 模型下载与 sha256 校验，会联网下载约 191 MB），
// 再用合成视频走一遍抽帧 → 真实 worker 取向量 → 建索引 → 中文文本检索。
// CINEINSIGHT_SCENE_RUNTIME_IT_DIR 可指定数据目录以便复用已下载的运行时；
// CINEINSIGHT_SCENE_MODEL_MIRROR 可指定模型镜像主机。绝不使用用户真实的 ~/.CineInsight。
func TestSceneRuntimeRealSmoke(t *testing.T) {
	if os.Getenv("CINEINSIGHT_SCENE_RUNTIME_IT") != "1" {
		t.Skip("设置 CINEINSIGHT_SCENE_RUNTIME_IT=1 才运行真实运行时冒烟")
	}
	dataDir := strings.TrimSpace(os.Getenv("CINEINSIGHT_SCENE_RUNTIME_IT_DIR"))
	if dataDir == "" {
		dataDir = t.TempDir()
	}
	if home, _ := os.UserHomeDir(); home != "" && strings.HasPrefix(filepath.Clean(dataDir), filepath.Join(home, ".CineInsight")) {
		t.Fatal("冒烟不得使用用户真实数据目录")
	}
	mirror := os.Getenv("CINEINSIGHT_SCENE_MODEL_MIRROR")
	runtime := NewSceneRuntime(dataDir, func() string { return mirror })

	started := time.Now()
	if status := runtime.Status(); status.State != SceneRuntimeStateAvailable {
		t.Logf("准备前状态: %s（%s）", status.State, status.Reason)
		lastStage := ""
		runtime.SetEventEmitter(func(status SceneRuntimeStatus) {
			if status.Stage != lastStage {
				lastStage = status.Stage
				t.Logf("[%6.1fs] stage=%s message=%s", time.Since(started).Seconds(), status.Stage, status.Message)
			}
		})
		if _, err := runtime.Prepare(context.Background()); err != nil {
			t.Fatalf("Prepare 失败: %v", err)
		}
		deadline := time.Now().Add(40 * time.Minute)
		for runtime.Status().Preparing && time.Now().Before(deadline) {
			time.Sleep(time.Second)
		}
		runtime.SetEventEmitter(nil)
	}
	status := runtime.Status()
	t.Logf("准备结束: state=%s elapsed=%.1fs error=%q", status.State, time.Since(started).Seconds(), status.Error)
	if status.State != SceneRuntimeStateAvailable {
		t.Fatalf("运行时未就绪: %+v", status)
	}
	if output, err := exec.Command(runtime.venvPython(), "-m", "pip", "freeze").CombinedOutput(); err == nil {
		t.Logf("venv 已安装包:\n%s", strings.TrimSpace(string(output)))
	}
	if output, err := exec.Command(runtime.venvPython(), "--version").CombinedOutput(); err == nil {
		t.Logf("venv 解释器: %s", strings.TrimSpace(string(output)))
	}

	ffmpeg, err := findThumbnailFFmpeg()
	if err != nil {
		t.Skip("没有 ffmpeg，跳过合成视频部分")
	}
	videoDir := t.TempDir()
	videoPath := filepath.Join(videoDir, "合成样片.mp4")
	synth := exec.Command(ffmpeg, "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=10:duration=15",
		"-f", "lavfi", "-i", "color=c=red:size=320x240:rate=10:duration=15",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1[v]", "-map", "[v]", videoPath)
	if output, err := synth.CombinedOutput(); err != nil {
		t.Fatalf("生成合成视频失败: %v %s", err, output)
	}

	host := newSceneRuntimeWorkerHost(runtime)
	defer host.Close()
	frameDir := t.TempDir()
	extractStart := time.Now()
	frames, err := ffmpegSceneFrames(context.Background(), videoPath, frameDir, 5000)
	if err != nil || len(frames) < 4 {
		t.Fatalf("抽帧失败: %v frames=%d", err, len(frames))
	}
	t.Logf("抽帧 %d 张用时 %.2fs", len(frames), time.Since(extractStart).Seconds())
	coldStart := time.Now()
	images, err := host.EmbedImages(context.Background(), frames)
	if err != nil {
		t.Fatalf("真实 worker 取图片向量失败: %v", err)
	}
	t.Logf("冷启动 + %d 张图片向量用时 %.2fs", len(frames), time.Since(coldStart).Seconds())
	warm := time.Now()
	if _, err := host.EmbedImages(context.Background(), frames); err != nil {
		t.Fatal(err)
	}
	t.Logf("热会话 %d 张图片向量用时 %.3fs", len(frames), time.Since(warm).Seconds())
	textStart := time.Now()
	texts, err := host.EmbedTexts(context.Background(), []string{"红色的画面", "彩色测试图案"})
	if err != nil {
		t.Fatalf("真实 worker 取中文文本向量失败: %v", err)
	}
	t.Logf("2 条中文文本向量用时 %.3fs", time.Since(textStart).Seconds())
	for _, vector := range append(images, texts...) {
		if len(vector) != sceneEmbeddingDims || math.Abs(float64(sceneDotFloat(vector, vector))-1) > 1e-3 {
			t.Fatalf("向量应为 512 维且 L2 归一化")
		}
	}
	for index, image := range images {
		t.Logf("帧 %d: 红色=%.3f 测试图案=%.3f", index, sceneDotFloat(texts[0], image), sceneDotFloat(texts[1], image))
	}
	last := images[len(images)-1]
	if sceneDotFloat(texts[0], last) <= sceneDotFloat(texts[0], images[0]) {
		t.Fatalf("“红色的画面”应更像末尾的纯红帧而不是开头的测试图案")
	}
	segments := buildSceneSegments(images, 5000, 30000)
	t.Logf("分段 %d 个: %+v", len(segments), sceneSegmentSpans(segments))

	// 端到端：真实运行时建索引 → 中文检索命中纯红区间。
	database.DB = dbtest.Open(t)
	if err := database.DB.Create(&models.Settings{ID: 1, VideoExtensions: ".mp4", SceneVisualProvider: SceneProviderLocal, SceneVisualIntervalSeconds: 5}).Error; err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(videoPath)
	video := models.Video{Name: filepath.Base(videoPath), Path: videoPath, Directory: videoDir, Size: info.Size(), Duration: 30}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	index := NewSceneIndexService(runtime, NewMediaWorkSlot())
	index.host.Close()
	index.host, index.vectors = host, host
	indexStart := time.Now()
	if _, err := index.Start(context.Background(), SceneIndexRequest{}); err != nil {
		t.Fatalf("建索引启动失败: %v", err)
	}
	for index.Status().Running {
		time.Sleep(50 * time.Millisecond)
	}
	indexStatus := index.Status()
	t.Logf("建索引用时 %.2fs: %+v", time.Since(indexStart).Seconds(), indexStatus)
	if indexStatus.Succeeded != 1 {
		t.Fatalf("真实建索引失败: %+v", indexStatus)
	}
	search := NewSceneSearchService(index)
	search.syncSubtitles = nil
	searchStart := time.Now()
	// limit=1：只取最相似的一段（limit 更大时两段相邻会按合同合并成一个区间）。
	result, err := search.Search(context.Background(), SceneSearchRequest{Query: "红色的画面", Mode: SceneModeVisual, Limit: 1})
	if err != nil || len(result.Hits) == 0 {
		t.Fatalf("真实检索没有命中: %+v %v", result, err)
	}
	t.Logf("检索用时 %.3fs，首条 [%d,%d) score=%.3f coverage=%+v", time.Since(searchStart).Seconds(),
		result.Hits[0].StartMS, result.Hits[0].EndMS, result.Hits[0].Score, result.Coverage)
	if result.Hits[0].StartMS < 15000 {
		t.Fatalf("“红色的画面”首条应落在后半段的纯红区间: %+v", result.Hits)
	}
}

func sceneSegmentSpans(segments []sceneSegmentDraft) [][2]int64 {
	spans := make([][2]int64, 0, len(segments))
	for _, segment := range segments {
		spans = append(spans, [2]int64{segment.StartMS, segment.EndMS})
	}
	return spans
}
