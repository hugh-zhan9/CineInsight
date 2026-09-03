package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestAnalyzeCleanupCandidatesSkipsMissingFilesAndUsesFreshMetadata(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()

	ffprobeDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(ffprobeDir, 0755); err != nil {
		t.Fatalf("创建 ffprobe 目录失败: %v", err)
	}
	ffprobePath := filepath.Join(ffprobeDir, "ffprobe")
	ffprobeScript := `#!/bin/sh
cat <<'JSON'
{"streams":[{"width":1280,"height":720,"duration":"10.0"}],"format":{"duration":"10.0"}}
JSON
`
	if err := os.WriteFile(ffprobePath, []byte(ffprobeScript), 0755); err != nil {
		t.Fatalf("写入 ffprobe stub 失败: %v", err)
	}
	t.Setenv("PATH", ffprobeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	existingPath := filepath.Join(root, "existing.mp4")
	mustWriteSizedFile(t, existingPath, []byte("dummy"))

	existing := models.Video{Name: "existing.mp4", Path: existingPath, Directory: root, Size: 100, Duration: 2, Width: 320, Height: 240}
	missing := models.Video{Name: "missing.mp4", Path: filepath.Join(root, "missing.mp4"), Directory: root, Size: 100, Duration: 2, Width: 320, Height: 240}
	if err := database.DB.Create(&existing).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := database.DB.Create(&missing).Error; err != nil {
		t.Fatalf("创建缺失视频记录失败: %v", err)
	}

	svc := &CleanupService{}
	result, err := svc.AnalyzeCleanupCandidates(CleanupCriteria{
		MinDuration: 5 * time.Second,
		MinWidth:    480,
		MinHeight:   320,
	})
	if err != nil {
		t.Fatalf("分析清理候选失败: %v", err)
	}

	if len(result.LowDuration) != 0 {
		t.Fatalf("缺失文件或已刷新元数据后不应落入短视频，实际 %+v", result.LowDuration)
	}
	if len(result.LowResolution) != 0 {
		t.Fatalf("缺失文件或已刷新元数据后不应落入低清视频，实际 %+v", result.LowResolution)
	}
}

func TestCleanupBackgroundAnalysisKeepsStatusAfterCompletion(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)

	shortPath := filepath.Join(root, "short.mp4")
	mustWriteSizedFile(t, shortPath, []byte("short"))
	video := models.Video{Name: "short.mp4", Path: shortPath, Directory: root, Size: 5, Duration: 30, Width: 1280, Height: 720}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	svc := &CleanupService{}
	finished := watchCleanupRunFinished(svc)
	status, err := svc.StartAnalysis(CleanupCriteria{
		MinDuration: 5 * time.Second,
		MinWidth:    480,
		MinHeight:   320,
	})
	if err != nil {
		t.Fatalf("启动后台清理分析失败: %v", err)
	}
	if !status.Running || status.Completed {
		t.Fatalf("启动后状态错误: %+v", status)
	}

	// 等真正的收尾信号，而不是一个猜出来的时间窗：Postgres 全量并发下这一轮
	// 可能跑上好几秒，固定 2 秒会随机超时。
	waitForCleanupRunFinished(t, finished)

	status = svc.Status()
	if status.Running || !status.Completed || status.Error != "" {
		t.Fatalf("完成后状态错误: %+v", status)
	}
	if status.Analysis == nil || len(status.Analysis.LowDuration) != 1 {
		t.Fatalf("完成后应保留分析结果，实际 %+v", status.Analysis)
	}
	if status.Stale {
		t.Fatalf("刚完成的分析不应是过期状态: %+v", status)
	}
	// 失效只标记过期：结果保留供用户继续审阅，由用户决定何时重新分析。
	svc.InvalidateAnalysis()
	status = svc.Status()
	if !status.Completed || status.Analysis == nil {
		t.Fatalf("失效后仍应保留分析结果供审阅: %+v", status)
	}
	if !status.Stale {
		t.Fatalf("失效后应标记为过期: %+v", status)
	}
}

// 前端收到 cleanup-progress 的 done 事件后会立刻回读 Status()。done 阶段必须
// 在结果写入之后才出现，否则界面会回读到 running=true / analysis=nil 而卡在"分析中"。
func TestCleanupDoneProgressAppearsOnlyAfterAnalysisIsReadable(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)

	shortPath := filepath.Join(root, "short.mp4")
	mustWriteSizedFile(t, shortPath, []byte("short"))
	video := models.Video{Name: "short.mp4", Path: shortPath, Directory: root, Size: 5, Duration: 30, Width: 1280, Height: 720}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	svc := &CleanupService{}
	finished := watchCleanupRunFinished(svc)
	if _, err := svc.StartAnalysis(CleanupCriteria{MinDuration: 5 * time.Second, MinWidth: 480, MinHeight: 320}); err != nil {
		t.Fatalf("启动后台清理分析失败: %v", err)
	}

	// 一边轮询一边等收尾信号：轮询才抓得住"done 已出现但结果还读不到"的瞬间窗口，
	// 收尾信号则接管了原来那个固定 5 秒的截止时间（并发负载下会误报超时）。
	deadline := time.Now().Add(cleanupRunWaitTimeout)
	for {
		// 单次快照内同时检查阶段和结果，避免两次读取之间状态发生变化。
		status := svc.Status()
		if status.Progress.Stage == "done" {
			if status.Running || !status.Completed || status.Analysis == nil {
				t.Fatalf("done 阶段出现时结果必须已可读，实际 %+v", status)
			}
			return
		}
		select {
		case <-finished:
			// worker 已经收尾：此刻 done 必须已经可见，且结果必须可读。
			status = svc.Status()
			if status.Progress.Stage != "done" {
				t.Fatalf("收尾之后 done 阶段必须已可见，实际 %+v", status)
			}
			if status.Running || !status.Completed || status.Analysis == nil {
				t.Fatalf("done 阶段出现时结果必须已可读，实际 %+v", status)
			}
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 done 阶段超时，实际 %+v", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// cleanupRunWaitTimeout 只是防挂死的兜底，不再充当"应该多久跑完"的判据。
const cleanupRunWaitTimeout = 60 * time.Second

// watchCleanupRunFinished 用后台任务登记表当收尾信号：登记表的 End 是 goroutine
// 最外层的 defer，一定在 done 事件之后执行，因此"cleanup 从登记表消失"正好
// 等价于"这一轮真的收尾了"。必须在 StartAnalysis 之前调用。
func watchCleanupRunFinished(svc *CleanupService) <-chan struct{} {
	finished := make(chan struct{}, 1)
	registry := NewBackgroundTaskRegistry()
	registry.SetOnChange(func(running []string) {
		if len(running) != 0 {
			return
		}
		select {
		case finished <- struct{}{}:
		default:
		}
	})
	svc.SetBackgroundTaskRegistry(registry)
	return finished
}

func waitForCleanupRunFinished(t *testing.T, finished <-chan struct{}) {
	t.Helper()
	select {
	case <-finished:
	case <-time.After(cleanupRunWaitTimeout):
		t.Fatalf("等待清理分析收尾超时（%s）", cleanupRunWaitTimeout)
	}
}

// 上一轮的收尾事件不能盖到新一轮的状态上：goroutine 从写完状态到发 done 之间没有持锁，
// 期间用户可能已经点了"重新分析"。
func TestCleanupStaleDoneEventDoesNotOverwriteANewerRun(t *testing.T) {
	setupCleanupServiceTestDB(t)
	svc := &CleanupService{}

	// 模拟第 1 轮已经写完状态、还没来得及发 done 的时刻。
	svc.mu.Lock()
	svc.runID = 1
	staleRunID := svc.runID
	svc.mu.Unlock()

	// 第 2 轮启动，状态被整体重置为 running。
	svc.mu.Lock()
	svc.runID++
	svc.status = CleanupStatus{Running: true, Progress: CleanupProgress{Stage: "load", Message: "第二轮"}}
	svc.mu.Unlock()

	svc.emitDoneForRun(staleRunID, 42, "第一轮的收尾消息")

	status := svc.Status()
	if status.Progress.Stage != "load" || status.Progress.Message != "第二轮" {
		t.Fatalf("旧一轮的 done 事件不应改写新一轮的进度，实际 %+v", status.Progress)
	}
	if !status.Running {
		t.Fatalf("新一轮应仍在运行，实际 %+v", status)
	}
}

func TestAnalyzeCleanupCandidatesFindsPhysicalDuplicates(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	a := filepath.Join(root, "a.mp4")
	b := filepath.Join(root, "b.mp4")
	mustWriteSizedFile(t, a, []byte("same-content"))
	mustWriteSizedFile(t, b, []byte("same-content"))

	v1 := models.Video{Name: "a.mp4", Path: a, Directory: root, Size: 12, Width: 1920, Height: 1080}
	v2 := models.Video{Name: "b.mp4", Path: b, Directory: root, Size: 12, Width: 1280, Height: 720}
	if err := database.DB.Create(&v1).Error; err != nil {
		t.Fatalf("创建视频1失败: %v", err)
	}
	if err := database.DB.Create(&v2).Error; err != nil {
		t.Fatalf("创建视频2失败: %v", err)
	}

	svc := &CleanupService{}
	result, err := svc.AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析清理候选失败: %v", err)
	}

	if len(result.DuplicateGroups) != 1 {
		t.Fatalf("期望 1 个重复组，实际 %d", len(result.DuplicateGroups))
	}
	group := result.DuplicateGroups[0]
	if group.Original.ID != v1.ID {
		t.Fatalf("期望更高分辨率视频为原件: got=%d want=%d", group.Original.ID, v1.ID)
	}
	if len(group.Candidates) != 1 || group.Candidates[0].ID != v2.ID {
		t.Fatalf("重复候选错误: %+v", group.Candidates)
	}
}

func TestAnalyzeCleanupCandidatesSkipsNonVideoRecordsWithoutMetadata(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	firstPath := filepath.Join(root, "types.ts")
	secondPath := filepath.Join(root, "index.d.ts")
	sourceContent := []byte("export type A = string\n")
	mustWriteSizedFile(t, firstPath, sourceContent)
	mustWriteSizedFile(t, secondPath, sourceContent)

	first := models.Video{Name: "types.ts", Path: firstPath, Directory: root, Size: 23}
	second := models.Video{Name: "index.d.ts", Path: secondPath, Directory: root, Size: 23}
	if err := database.DB.Create(&first).Error; err != nil {
		t.Fatalf("创建 types.ts 记录失败: %v", err)
	}
	if err := database.DB.Create(&second).Error; err != nil {
		t.Fatalf("创建 index.d.ts 记录失败: %v", err)
	}

	svc := &CleanupService{}
	result, err := svc.AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析清理候选失败: %v", err)
	}
	if len(result.DuplicateGroups) != 0 {
		t.Fatalf("无法解析视频元数据的源码文件不应进入重复候选，实际 %+v", result.DuplicateGroups)
	}
}

func TestAnalyzeCleanupCandidatesFindsLowQualityVideos(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	shortPath := filepath.Join(root, "short.mp4")
	smallPath := filepath.Join(root, "small.mp4")
	mustWriteSizedFile(t, shortPath, []byte("short"))
	mustWriteSizedFile(t, smallPath, []byte("small"))

	short := models.Video{Name: "short.mp4", Path: shortPath, Directory: root, Size: 5, Duration: 2, Width: 1280, Height: 720}
	small := models.Video{Name: "small.mp4", Path: smallPath, Directory: root, Size: 5, Duration: 30, Width: 320, Height: 240}
	if err := database.DB.Create(&short).Error; err != nil {
		t.Fatalf("创建短视频失败: %v", err)
	}
	if err := database.DB.Create(&small).Error; err != nil {
		t.Fatalf("创建低清视频失败: %v", err)
	}

	svc := &CleanupService{}
	result, err := svc.AnalyzeCleanupCandidates(CleanupCriteria{
		MinDuration: 5 * time.Second,
		MinWidth:    480,
		MinHeight:   320,
	})
	if err != nil {
		t.Fatalf("分析清理候选失败: %v", err)
	}

	if len(result.LowDuration) != 1 || result.LowDuration[0].ID != short.ID {
		t.Fatalf("短时长候选错误: %+v", result.LowDuration)
	}
	if len(result.LowResolution) != 1 || result.LowResolution[0].ID != small.ID {
		t.Fatalf("低分辨率候选错误: %+v", result.LowResolution)
	}
}

func TestAnalyzeCleanupCandidatesIncludesDetectedSameSourceWithoutExactDuplicates(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)

	makeVideo := func(name, content string) models.Video {
		path := filepath.Join(root, name)
		mustWriteSizedFile(t, path, []byte(content))
		video := models.Video{Name: name, Path: path, Directory: root, Size: int64(len(content))}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatalf("创建视频 %s 失败: %v", name, err)
		}
		return video
	}

	a := makeVideo("same-a.mp4", "aaa")
	b := makeVideo("same-b.mp4", "bbb")
	c := makeVideo("duplicate-a.mp4", "dup")
	d := makeVideo("duplicate-b.mp4", "dup")
	e := makeVideo("rejected-a.mp4", "eee")
	f := makeVideo("rejected-b.mp4", "fff")
	tag := models.Tag{Name: "保留标签", Color: "#123456"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Model(&b).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("给候选添加标签失败: %v", err)
	}

	relations := []models.VideoSameSourceRelation{
		{VideoAID: a.ID, VideoBID: b.ID, Status: models.VideoSameSourceStatusDetected, Confidence: "high", Reasoning: "画面内容一致", DetectionVersion: "test"},
		{VideoAID: c.ID, VideoBID: d.ID, Status: models.VideoSameSourceStatusDetected, Confidence: "high", Reasoning: "精确重复", DetectionVersion: "test"},
		{VideoAID: e.ID, VideoBID: f.ID, Status: models.VideoSameSourceStatusRejected, Confidence: "medium", Reasoning: "已否认", DetectionVersion: "test"},
	}
	if err := database.DB.Create(&relations).Error; err != nil {
		t.Fatalf("创建同源关系失败: %v", err)
	}

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析清理候选失败: %v", err)
	}
	if len(result.DuplicateGroups) != 1 {
		t.Fatalf("应识别一组精确重复，实际 %d", len(result.DuplicateGroups))
	}
	if len(result.SameSourceGroups) != 1 {
		t.Fatalf("应只保留 detected 且非精确重复的同源关系，实际 %+v", result.SameSourceGroups)
	}
	group := result.SameSourceGroups[0]
	if group.RelationID != relations[0].ID || group.Preferred.ID != b.ID || group.Alternative.ID != a.ID {
		t.Fatalf("同源保留建议不稳定: %+v", group)
	}
	if group.EstimatedSavings != a.Size || group.Confidence != "high" || group.Reason != "画面内容一致" {
		t.Fatalf("同源候选信息不完整: %+v", group)
	}
}

func mockFFProbe(t *testing.T, root string) {
	t.Helper()
	ffprobeDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(ffprobeDir, 0755); err != nil {
		t.Fatalf("创建 ffprobe 目录失败: %v", err)
	}
	ffprobePath := filepath.Join(ffprobeDir, "ffprobe")
	script := `#!/bin/bash
target="${@: -1}"
name="$(basename "$target")"
case "$name" in
  short.mp4)
    duration="2.0"
    width=1280
    height=720
    ;;
  small.mp4)
    duration="30.0"
    width=320
    height=240
    ;;
  *)
    duration="12.0"
    width=1920
    height=1080
    ;;
esac
cat <<JSON
{"streams":[{"width":${width},"height":${height},"duration":"${duration}"}],"format":{"duration":"${duration}"}}
JSON
`
	if err := os.WriteFile(ffprobePath, []byte(script), 0755); err != nil {
		t.Fatalf("写入 ffprobe stub 失败: %v", err)
	}
	t.Setenv("PATH", ffprobeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func mustWriteSizedFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("写入文件失败: %v", err)
	}
}

func setupCleanupServiceTestDB(t *testing.T) {
	t.Helper()
	db := dbtest.Open(t)
	database.DB = db
}

// 改窄扫描根之后，范围外的旧记录在列表里已经看不见，也不该再被列成清理候选。
func TestCleanupAnalysisSkipsVideosOutsideScanRoots(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	insideDir := filepath.Join(root, "inside")
	outsideDir := filepath.Join(root, "outside")
	if err := os.MkdirAll(insideDir, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	// 两个同样内容的文件放在范围外：只有它们会被判为精确重复。
	payload := []byte("same-content-payload")
	outsideA := filepath.Join(outsideDir, "a.mp4")
	outsideB := filepath.Join(outsideDir, "b.mp4")
	for _, path := range []string{outsideA, outsideB} {
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			t.Fatalf("写文件失败: %v", err)
		}
	}
	insidePath := filepath.Join(insideDir, "kept.mp4")
	if err := os.WriteFile(insidePath, []byte("unique"), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	videos := []models.Video{
		{Name: "a.mp4", Path: outsideA, Directory: outsideDir, Size: int64(len(payload)), Duration: 600, Width: 1920, Height: 1080},
		{Name: "b.mp4", Path: outsideB, Directory: outsideDir, Size: int64(len(payload)), Duration: 600, Width: 1920, Height: 1080},
		{Name: "kept.mp4", Path: insidePath, Directory: insideDir, Size: 6, Duration: 600, Width: 1920, Height: 1080},
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	// 扫描根覆盖整个 root 时，范围外那一对会被认出来。
	wide := models.ScanDirectory{Path: root}
	if err := database.DB.Create(&wide).Error; err != nil {
		t.Fatalf("创建扫描目录失败: %v", err)
	}
	analysis, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("宽根分析失败: %v", err)
	}
	if len(analysis.DuplicateGroups) != 1 {
		t.Fatalf("宽根下应认出一组精确重复: %+v", analysis.DuplicateGroups)
	}

	// 把根改窄到 inside 之后，那一对整个从候选里消失。
	if err := database.DB.Model(&models.ScanDirectory{}).Where("id = ?", wide.ID).Update("path", insideDir).Error; err != nil {
		t.Fatalf("改窄扫描目录失败: %v", err)
	}
	analysis, err = (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("窄根分析失败: %v", err)
	}
	if len(analysis.DuplicateGroups) != 0 {
		t.Fatalf("范围外的重复不该再是候选: %+v", analysis.DuplicateGroups)
	}
}
