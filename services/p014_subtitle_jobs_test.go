package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"
)

// P-014：字幕队列持久化（D-PC20）、引擎准备（D-PC22）、索引同步节流（D-PC23）与 has_sidecar 写入
// （D-PC17）。测试名里的问题 ID 与问题清单一一对应：MEDIA-04 失败与待确认可查可处理、MEDIA-10
// 中断与暂存跨重启、MEDIA-13 引擎准备、MEDIA-14 同步节流、MEDIA-08 旁挂字幕口径。

// ===== 测试夹具 =====

type p014EventRecorder struct {
	mu     sync.Mutex
	events []p014Event
}

type p014Event struct {
	name    string
	payload any
}

func (r *p014EventRecorder) record(name string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, p014Event{name: name, payload: payload})
}

func (r *p014EventRecorder) named(name string) []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var payloads []any
	for _, event := range r.events {
		if event.name == name {
			payloads = append(payloads, event.payload)
		}
	}
	return payloads
}

// p014Harness 是一个接了 subtitle_jobs 落库的字幕服务：非强制任务用 asr 模拟识别后走真实的
// commitTranscription；强制任务走真实的 executeSubtitleTask（只会复用临时字幕，引擎一律不可用，
// 真去识别就会失败，从而证明没有重跑识别）。
type p014Harness struct {
	service  *SubtitleService
	asrCalls atomic.Int32
	asr      atomic.Value // func(task *subtitleQueueTask) ([]subtitleparser.Segment, error)
	events   *p014EventRecorder
	notifier *stubDesktopNotifier
}

func newP014Harness(t *testing.T, baseDir string) *p014Harness {
	t.Helper()
	h := &p014Harness{service: NewSubtitleService(baseDir), events: &p014EventRecorder{}, notifier: &stubDesktopNotifier{}}
	h.setASR(func(*subtitleQueueTask) ([]subtitleparser.Segment, error) { return realSegments(), nil })
	h.service.eventSink = h.events.record
	h.service.engineStatusProbe = func() []SubtitleEngineStatus {
		return []SubtitleEngineStatus{
			{Engine: SubtitleEngineWhisperX, DisplayName: "WhisperX", Supported: true, ReasonMessage: "测试里引擎不可用"},
		}
	}
	h.service.taskQueue = h.service.newPersistentSubtitleTaskQueue(func(ctx context.Context, task *subtitleQueueTask) (*SubtitleGenerateResult, error) {
		if task.Options.ForceGenerate {
			return h.service.executeSubtitleTask(ctx, task.TaskID, task.Request, task.VideoPath, task.Options)
		}
		h.asrCalls.Add(1)
		asr := h.asr.Load().(func(*subtitleQueueTask) ([]subtitleparser.Segment, error))
		segments, err := asr(task)
		if err != nil {
			return nil, err
		}
		return h.service.commitTranscription(ctx, task.TaskID, task.Request, task.VideoPath, "en", segments, task.Options)
	})
	h.service.SetDesktopNotifier(h.notifier)
	return h
}

func (h *p014Harness) setASR(asr func(*subtitleQueueTask) ([]subtitleparser.Segment, error)) {
	h.asr.Store(asr)
}

func p014Request(video models.Video) SubtitleGenerateRequest {
	return SubtitleGenerateRequest{VideoID: video.ID, VideoName: video.Name, Engine: SubtitleEngineWhisperX, SourceLang: "en"}
}

func p014Input(video models.Video) SubtitleJobResolveInput {
	return SubtitleJobResolveInput{VideoPath: video.Path, VideoName: video.Name}
}

func mustLoadSubtitleJob(t *testing.T, id uint) models.SubtitleJob {
	t.Helper()
	var job models.SubtitleJob
	if err := database.DB.First(&job, id).Error; err != nil {
		t.Fatalf("读取字幕任务 %d 失败: %v", id, err)
	}
	return job
}

func mustOnlySubtitleJob(t *testing.T) models.SubtitleJob {
	t.Helper()
	var jobs []models.SubtitleJob
	if err := database.DB.Order("id ASC").Find(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("应只有一条字幕任务，实际 %+v", jobs)
	}
	return jobs[0]
}

func waitSubtitleJobStatus(t *testing.T, id uint, want SubtitleQueueTaskStatus) models.SubtitleJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		job := mustLoadSubtitleJob(t, id)
		if SubtitleQueueTaskStatus(job.Status) == want {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("字幕任务 %d 应进入 %s，实际 %+v", id, want, job)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertNoPath(t *testing.T, what, text string, paths ...string) {
	t.Helper()
	if strings.Contains(text, "/") {
		t.Fatalf("%s 不应含路径: %q", what, text)
	}
	for _, path := range paths {
		if path != "" && strings.Contains(text, path) {
			t.Fatalf("%s 不应含 %q: %q", what, path, text)
		}
	}
}

// syncSubtitleIndexForTest 跑完一轮全新的全库字幕索引同步（「立即同步」并等它结束）。
// D-PC23 之后视图前置同步改为节流 + 后台，依赖磁盘字幕的用例先显式同步一轮。
func syncSubtitleIndexForTest(t *testing.T) SubtitleIndexSyncStatus {
	t.Helper()
	db := database.DB
	deadline := time.After(10 * time.Second)
	for {
		run, started := subtitleIndexSync.request(db, true)
		select {
		case <-run.done:
		case <-deadline:
			t.Fatal("字幕索引同步超时")
		}
		if started {
			return subtitleIndexSync.status(db)
		}
	}
}

// waitSubtitleIndexSyncIdle 等正在跑的后台同步结束；没有在跑的就立即返回。
func waitSubtitleIndexSyncIdle(t *testing.T) {
	t.Helper()
	subtitleIndexSync.mu.Lock()
	run := subtitleIndexSync.run
	subtitleIndexSync.mu.Unlock()
	if run == nil {
		return
	}
	select {
	case <-run.done:
	case <-time.After(10 * time.Second):
		t.Fatal("后台字幕索引同步超时")
	}
}

type p014Clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *p014Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *p014Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func useSubtitleIndexSyncClock(t *testing.T) *p014Clock {
	t.Helper()
	clock := &p014Clock{now: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	subtitleIndexSync.mu.Lock()
	previousNow, previousEmit := subtitleIndexSync.now, subtitleIndexSync.emit
	subtitleIndexSync.now = clock.Now
	subtitleIndexSync.mu.Unlock()
	t.Cleanup(func() {
		stopSubtitleIndexSyncAndWait()
		subtitleIndexSync.mu.Lock()
		subtitleIndexSync.now, subtitleIndexSync.emit = previousNow, previousEmit
		subtitleIndexSync.mu.Unlock()
	})
	return clock
}

func noSubtitleViewIDs(t *testing.T) []uint {
	t.Helper()
	videos, err := (&VideoService{}).SearchLibraryVideos(LibraryFilter{SmartView: LibraryViewNoSubtitle}, 0, 0, 0, 50)
	if err != nil {
		t.Fatalf("无字幕视图查询失败: %v", err)
	}
	ids := make([]uint, 0, len(videos))
	for _, video := range videos {
		ids = append(ids, video.ID)
	}
	return ids
}

func sameIDs(got []uint, want ...uint) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[uint]int{}
	for _, id := range got {
		seen[id]++
	}
	for _, id := range want {
		if seen[id] == 0 {
			return false
		}
		seen[id]--
	}
	return true
}

// ===== MEDIA-04：失败与待确认的字幕任务都能查到并处理 =====

func TestMEDIA04FailedSubtitleJobIsListedWithScrubbedReasonAndRetryable(t *testing.T) {
	setupVideoServiceTestDB(t)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", nil)
	h := newP014Harness(t, t.TempDir())
	h.setASR(func(task *subtitleQueueTask) ([]subtitleparser.Segment, error) {
		return nil, fmt.Errorf("WhisperX 识别失败: Traceback File \"%s/worker.py\" while reading %s", filepath.Dir(task.VideoPath), task.VideoPath)
	})
	options := SubtitleGenerateOptions{
		BilingualEnabled: true, BilingualLang: "zh",
		TranslationConfig: SubtitleTranslationConfig{Provider: "llm", APIKey: "sk-p014-secret", DeepLAPIKey: "deepl-p014-secret", Model: "m"},
	}

	if _, err := h.service.GenerateSubtitle(p014Request(video), video.Path, options); err == nil {
		t.Fatal("识别失败仍应作为错误返回给调用方")
	}

	jobs, err := h.service.ListSubtitleJobs(0)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("失败任务应出现在任务列表: %+v err=%v", jobs, err)
	}
	job := jobs[0]
	if job.Status != SubtitleQueueTaskStatusFailed || job.VideoName != "movie.mp4" || job.VideoID != video.ID {
		t.Fatalf("失败任务记录不符: %+v", job)
	}
	if !strings.Contains(job.Message, "WhisperX 识别失败") {
		t.Fatalf("任务中心应能看到失败原因: %q", job.Message)
	}
	assertNoPath(t, "失败原因", job.Message, video.Directory)
	if len(job.Actions) != 1 || job.Actions[0] != string(SubtitleJobActionRetry) {
		t.Fatalf("失败任务应可重试: %+v", job.Actions)
	}
	row := mustLoadSubtitleJob(t, job.ID)
	if strings.Contains(row.OptionsJSON, "secret") {
		t.Fatalf("options_json 不得落翻译服务的 Key: %s", row.OptionsJSON)
	}
	if row.StartedAt == nil || row.FinishedAt == nil {
		t.Fatalf("终态行应记开始与结束时间: %+v", row)
	}

	failed := h.events.named(subtitleFailedEvent)
	if len(failed) != 1 {
		t.Fatalf("应发一条 subtitle-failed: %+v", failed)
	}
	event := failed[0].(SubtitleFailedEvent)
	if event.JobID != job.ID || event.VideoID != video.ID || event.Message != job.Message {
		t.Fatalf("subtitle-failed 载荷不符: %+v", event)
	}
	data, _ := json.Marshal(event)
	for _, key := range []string{`"job_id"`, `"video_id"`, `"message"`} {
		if !bytes.Contains(data, []byte(key)) {
			t.Fatalf("subtitle-failed 载荷缺 %s: %s", key, data)
		}
	}

	notes := waitForNotifications(t, h.notifier, 1)
	if notes[0].Title != "字幕生成失败" || !strings.Contains(notes[0].Body, "任务中心") || strings.Contains(notes[0].Body, "识别失败") {
		t.Fatalf("失败通知应指向任务中心且不含原因: %+v", notes[0])
	}
	assertNoPath(t, "通知正文", notes[0].Body)

	// 重试：按当前设置重新排队，同一行走完到 succeeded。
	h.setASR(func(*subtitleQueueTask) ([]subtitleparser.Segment, error) { return realSegments(), nil })
	resolved, err := h.service.ResolveSubtitleJob(job.ID, SubtitleJobActionRetry, p014Input(video))
	if err != nil || resolved.Status != SubtitleQueueTaskStatusQueued || resolved.JobID != job.ID || resolved.ErrorCode != "" {
		t.Fatalf("重试应重新入队: %+v err=%v", resolved, err)
	}
	waitSubtitleJobStatus(t, job.ID, SubtitleQueueTaskStatusSucceeded)
	if _, err := os.Stat(srtPath); err != nil {
		t.Fatalf("重试成功后应写出字幕: %v", err)
	}
	if got := mustLoadSubtitleJob(t, job.ID); got.Message != "" {
		t.Fatalf("成功后不应残留失败原因: %+v", got)
	}

	// 已成功的任务再点重试：条件更新输掉，报冲突而不是再跑一遍。
	again, err := h.service.ResolveSubtitleJob(job.ID, SubtitleJobActionRetry, p014Input(video))
	if err != nil || again.ErrorCode != SubtitleErrorJobConflict || again.Status != SubtitleQueueTaskStatusSucceeded {
		t.Fatalf("重复重试应报冲突: %+v err=%v", again, err)
	}
	if h.asrCalls.Load() != 2 {
		t.Fatalf("识别应只跑两次（首次 + 一次重试），实际 %d", h.asrCalls.Load())
	}
}

func TestMEDIA04HallucinationJobCanBeDiscardedFromTaskCenter(t *testing.T) {
	setupVideoServiceTestDB(t)
	original := []byte("1\n00:00:00,000 --> 00:00:01,000\nhand-made subtitle\n")
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", original)
	h := newP014Harness(t, t.TempDir())
	h.setASR(func(*subtitleQueueTask) ([]subtitleparser.Segment, error) { return hallucinatedSegments(), nil })

	result, err := h.service.GenerateSubtitle(p014Request(video), video.Path, SubtitleGenerateOptions{})
	if err != nil || result.Status != SubtitleResultStatusValidationFailed || !result.PendingRetained {
		t.Fatalf("幻觉应以待确认结果返回并保留临时文件: %+v err=%v", result, err)
	}
	pendingPath := subtitlePendingPath(srtPath)

	jobs, err := h.service.ListSubtitleJobs(10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("待确认任务应出现在任务列表: %+v err=%v", jobs, err)
	}
	job := jobs[0]
	if job.Status != SubtitleQueueTaskStatusNeedsConfirmation || !job.PendingRetained || job.ErrorCode != "" {
		t.Fatalf("待确认任务记录不符: %+v", job)
	}
	if strings.Join(job.Actions, ",") != "force,discard" {
		t.Fatalf("待确认任务应可强制生成或放弃: %+v", job.Actions)
	}
	if !strings.Contains(job.Message, "幻觉") {
		t.Fatalf("待确认说明应保留: %q", job.Message)
	}
	listed, _ := json.Marshal(jobs)
	if bytes.Contains(listed, []byte("cineinsight-pending")) || bytes.Contains(listed, []byte(video.Directory)) {
		t.Fatalf("任务列表不应下发临时文件路径: %s", listed)
	}
	if row := mustLoadSubtitleJob(t, job.ID); row.PendingArtifactPath != pendingPath {
		t.Fatalf("行里应记隐藏的临时文件路径: %q", row.PendingArtifactPath)
	}
	if !strings.HasPrefix(filepath.Base(pendingPath), ".") {
		t.Fatalf("临时文件应是隐藏文件名: %s", pendingPath)
	}
	if notes := waitForNotifications(t, h.notifier, 1); notes[0].Title != "字幕需要确认" || !strings.Contains(notes[0].Body, "任务中心") {
		t.Fatalf("待确认通知应指向任务中心: %+v", notes[0])
	}
	if got := h.events.named(subtitleFailedEvent); len(got) != 0 {
		t.Fatalf("疑似幻觉不是失败，不发 subtitle-failed: %+v", got)
	}

	resolved, err := h.service.ResolveSubtitleJob(job.ID, SubtitleJobActionDiscard, SubtitleJobResolveInput{})
	if err != nil || resolved.Status != SubtitleQueueTaskStatusCancelled || resolved.ErrorCode != "" {
		t.Fatalf("放弃应置为 cancelled: %+v err=%v", resolved, err)
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("放弃后临时文件应删除: %v", err)
	}
	if !bytes.Equal(mustReadBytes(t, srtPath), original) {
		t.Fatal("放弃不得改动原字幕")
	}
	if artifact := h.service.peekPendingSubtitle(video.ID); artifact != nil {
		t.Fatalf("放弃后内存登记应清掉: %+v", artifact)
	}
	if row := mustLoadSubtitleJob(t, job.ID); row.PendingArtifactPath != "" {
		t.Fatalf("放弃后行里不应再记临时文件: %+v", row)
	}
	again, err := h.service.ResolveSubtitleJob(job.ID, SubtitleJobActionDiscard, SubtitleJobResolveInput{})
	if err != nil || again.ErrorCode != SubtitleErrorJobConflict {
		t.Fatalf("重复放弃应报冲突: %+v err=%v", again, err)
	}
	if _, err := h.service.ResolveSubtitleJob(job.ID, SubtitleJobAction("delete"), SubtitleJobResolveInput{}); !errors.Is(err, ErrSubtitleJobActionUnsupported) {
		t.Fatalf("未知操作应被拒绝: %v", err)
	}
	if _, err := h.service.ResolveSubtitleJob(job.ID+100, SubtitleJobActionDiscard, SubtitleJobResolveInput{}); !errors.Is(err, ErrSubtitleJobNotFound) {
		t.Fatalf("不存在的任务应报 not found: %v", err)
	}
}

func TestMEDIA04ReplaceFailureReturnsCodeAndForceRetriesFinalizeWithoutRecognition(t *testing.T) {
	setupVideoServiceTestDB(t)
	original := []byte(writerTestSRT)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", original)
	// 数据目录为空时覆盖已有字幕无法先备份，Replace 必然失败：正是「收尾写回失败」的场景。
	h := newP014Harness(t, "")

	result, err := h.service.GenerateSubtitle(p014Request(video), video.Path, SubtitleGenerateOptions{})
	if err != nil {
		t.Fatalf("写回失败应以带错误码的结果返回，不是错误: %v", err)
	}
	if result.ErrorCode != SubtitleErrorReplaceFailed || !result.PendingRetained || !result.ForceEligible ||
		result.Status != SubtitleResultStatusValidationFailed {
		t.Fatalf("写回失败的结果不符: %+v", result)
	}
	assertNoPath(t, "写回失败说明", result.Message, video.Directory)
	pendingPath := subtitlePendingPath(srtPath)
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatalf("写回失败应保留临时文件: %v", err)
	}
	if !bytes.Equal(mustReadBytes(t, srtPath), original) {
		t.Fatal("写回失败时原字幕必须逐字节不变")
	}

	job := mustOnlySubtitleJob(t)
	if SubtitleQueueTaskStatus(job.Status) != SubtitleQueueTaskStatusNeedsConfirmation || job.PendingArtifactPath != pendingPath {
		t.Fatalf("写回失败的任务应进入 needs_confirmation 并记临时文件: %+v", job)
	}
	items, _ := h.service.ListSubtitleJobs(0)
	if len(items) != 1 || items[0].ErrorCode != SubtitleErrorReplaceFailed || strings.Join(items[0].Actions, ",") != "force,discard" {
		t.Fatalf("任务中心应能「重试收尾」: %+v", items)
	}
	failed := h.events.named(subtitleFailedEvent)
	if len(failed) != 1 || failed[0].(SubtitleFailedEvent).ErrorCode != SubtitleErrorReplaceFailed {
		t.Fatalf("写回失败应发带错误码的 subtitle-failed: %+v", failed)
	}
	if notes := waitForNotifications(t, h.notifier, 1); notes[0].Title != "字幕写入失败" || !strings.Contains(notes[0].Body, "任务中心") {
		t.Fatalf("写回失败通知不符: %+v", notes[0])
	}

	// 数据目录恢复可用后「重试收尾」：复用临时文件，不重跑识别。
	h.service.BaseDir = t.TempDir()
	resolved, err := h.service.ResolveSubtitleJob(job.ID, SubtitleJobActionForce, p014Input(video))
	if err != nil || resolved.Status != SubtitleQueueTaskStatusQueued {
		t.Fatalf("重试收尾应入队: %+v err=%v", resolved, err)
	}
	waitSubtitleJobStatus(t, job.ID, SubtitleQueueTaskStatusSucceeded)
	if h.asrCalls.Load() != 1 {
		t.Fatalf("重试收尾不得重跑识别，识别次数 %d", h.asrCalls.Load())
	}
	segments, err := subtitleparser.ParseFile(srtPath)
	if err != nil || len(segments) != 2 || segments[0].Text != "Hello there" {
		t.Fatalf("收尾后字幕应是识别结果: %+v err=%v", segments, err)
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("收尾成功后临时文件应删除: %v", err)
	}
}

func TestMEDIA04DialogForceGenerateResolvesSameJobRowAndNewRunSupersedesOldPending(t *testing.T) {
	setupVideoServiceTestDB(t)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", nil)
	h := newP014Harness(t, t.TempDir())
	h.setASR(func(*subtitleQueueTask) ([]subtitleparser.Segment, error) { return hallucinatedSegments(), nil })

	if result, err := h.service.GenerateSubtitle(p014Request(video), video.Path, SubtitleGenerateOptions{}); err != nil || result.Status != SubtitleResultStatusValidationFailed {
		t.Fatalf("第一次应待确认: %+v err=%v", result, err)
	}
	first := mustOnlySubtitleJob(t)

	// 对话框里的「强制生成」接上同一行，不在任务中心留下一条永远待确认的旧行。
	forced, err := h.service.GenerateSubtitle(p014Request(video), video.Path, SubtitleGenerateOptions{ForceGenerate: true})
	if err != nil || forced.Status != SubtitleResultStatusSuccess {
		t.Fatalf("强制生成应复用临时文件成功: %+v err=%v", forced, err)
	}
	if job := mustOnlySubtitleJob(t); job.ID != first.ID || SubtitleQueueTaskStatus(job.Status) != SubtitleQueueTaskStatusSucceeded {
		t.Fatalf("强制生成应结束同一行: %+v", job)
	}
	if h.asrCalls.Load() != 1 {
		t.Fatalf("强制生成不得重跑识别: %d", h.asrCalls.Load())
	}
	if _, err := os.Stat(srtPath); err != nil {
		t.Fatalf("强制生成应写出字幕: %v", err)
	}

	// 又一次待确认，之后同一视频开始新的一轮：旧行的临时文件会被重写，旧行随即被取代。
	if _, err := h.service.GenerateSubtitle(p014Request(video), video.Path, SubtitleGenerateOptions{}); err != nil {
		t.Fatal(err)
	}
	var pendingJob models.SubtitleJob
	if err := database.DB.Where("status = ?", string(SubtitleQueueTaskStatusNeedsConfirmation)).First(&pendingJob).Error; err != nil {
		t.Fatalf("应有一条待确认: %v", err)
	}
	h.setASR(func(*subtitleQueueTask) ([]subtitleparser.Segment, error) { return realSegments(), nil })
	if result, err := h.service.GenerateSubtitle(p014Request(video), video.Path, SubtitleGenerateOptions{}); err != nil || result.Status != SubtitleResultStatusSuccess {
		t.Fatalf("新一轮应成功: %+v err=%v", result, err)
	}
	superseded := mustLoadSubtitleJob(t, pendingJob.ID)
	if SubtitleQueueTaskStatus(superseded.Status) != SubtitleQueueTaskStatusCancelled || superseded.PendingArtifactPath != "" ||
		!strings.Contains(superseded.Message, "取代") {
		t.Fatalf("旧的待确认行应被新任务取代: %+v", superseded)
	}
}

func TestMEDIA04LegacyDiscardPendingSubtitleAlsoClosesJobRow(t *testing.T) {
	setupVideoServiceTestDB(t)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", nil)
	h := newP014Harness(t, t.TempDir())
	h.setASR(func(*subtitleQueueTask) ([]subtitleparser.Segment, error) { return hallucinatedSegments(), nil })
	if _, err := h.service.GenerateSubtitle(p014Request(video), video.Path, SubtitleGenerateOptions{}); err != nil {
		t.Fatal(err)
	}
	job := mustOnlySubtitleJob(t)

	if err := h.service.DiscardPendingSubtitle(video.ID); err != nil {
		t.Fatalf("放弃失败: %v", err)
	}
	if _, err := os.Stat(subtitlePendingPath(srtPath)); !os.IsNotExist(err) {
		t.Fatalf("临时文件应删除: %v", err)
	}
	if got := mustLoadSubtitleJob(t, job.ID); SubtitleQueueTaskStatus(got.Status) != SubtitleQueueTaskStatusCancelled {
		t.Fatalf("对话框里的放弃应同步结束任务中心的行: %+v", got)
	}
}

func TestMEDIA04SubtitleJobHistoryKeepsLatestHundredTerminalRows(t *testing.T) {
	setupVideoServiceTestDB(t)
	video, _ := mustCreateSubtitleVideo(t, "movie.mp4", nil)
	base := time.Now().Add(-time.Hour)
	create := func(status SubtitleQueueTaskStatus, offset int) uint {
		t.Helper()
		job := models.SubtitleJob{VideoID: video.ID, Engine: "whisperx", SourceLang: "en", Status: string(status)}
		if err := database.DB.Create(&job).Error; err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(offset) * time.Second)
		if err := database.DB.Model(&models.SubtitleJob{}).Where("id = ?", job.ID).UpdateColumn("updated_at", stamp).Error; err != nil {
			t.Fatal(err)
		}
		return job.ID
	}
	var terminal []uint
	statuses := []SubtitleQueueTaskStatus{SubtitleQueueTaskStatusSucceeded, SubtitleQueueTaskStatusFailed, SubtitleQueueTaskStatusCancelled, SubtitleQueueTaskStatusInterrupted}
	for i := 0; i < 105; i++ {
		terminal = append(terminal, create(statuses[i%len(statuses)], i))
	}
	pendingID := create(SubtitleQueueTaskStatusNeedsConfirmation, -10)
	queuedID := create(SubtitleQueueTaskStatusQueued, -20)

	pruneSubtitleJobHistory(database.DB)

	var remaining []uint
	if err := database.DB.Model(&models.SubtitleJob{}).Order("id ASC").Pluck("id", &remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 102 {
		t.Fatalf("应留下 100 条终态 + 待确认 + 排队，共 102 条，实际 %d", len(remaining))
	}
	kept := map[uint]bool{}
	for _, id := range remaining {
		kept[id] = true
	}
	for i, id := range terminal {
		if want := i >= 5; kept[id] != want {
			t.Fatalf("第 %d 条终态行（id=%d）保留=%v，期望 %v：应只删最旧的 5 条", i, id, kept[id], want)
		}
	}
	if !kept[pendingID] || !kept[queuedID] {
		t.Fatal("待确认与排队中的行不参与裁剪")
	}
}

// ===== MEDIA-10：中断的任务可以重新排队；待确认的暂存跨重启可用 =====

func TestMEDIA10InterruptedJobsAreMarkedAndCanBeRequeued(t *testing.T) {
	setupVideoServiceTestDB(t)
	videoA, srtA := mustCreateSubtitleVideo(t, "a.mp4", nil)
	videoB, _ := mustCreateSubtitleVideo(t, "b.mp4", nil)
	videoC, _ := mustCreateSubtitleVideo(t, "c.mp4", nil)
	// 上一次进程退出时留下的行：一条在跑、一条在排队，另有一条早已成功。
	leftover := []models.SubtitleJob{
		{VideoID: videoA.ID, Engine: "whisperx", SourceLang: "en", Status: string(SubtitleQueueTaskStatusRunning)},
		{VideoID: videoB.ID, Engine: "whisperx", SourceLang: "en", Status: string(SubtitleQueueTaskStatusQueued)},
		{VideoID: videoC.ID, Engine: "whisperx", SourceLang: "en", Status: string(SubtitleQueueTaskStatusSucceeded)},
	}
	for i := range leftover {
		if err := database.DB.Create(&leftover[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	h := newP014Harness(t, t.TempDir())

	// 本进程里已入队、正在跑的任务不会被当成上次中断的。
	release := make(chan struct{})
	h.setASR(func(*subtitleQueueTask) ([]subtitleparser.Segment, error) {
		<-release
		return realSegments(), nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := h.service.GenerateSubtitle(p014Request(videoC), videoC.Path, SubtitleGenerateOptions{})
		done <- err
	}()
	var live models.SubtitleJob
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := database.DB.Where("video_id = ? AND status = ?", videoC.ID, string(SubtitleQueueTaskStatusRunning)).First(&live).Error; err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("本进程的任务没有进入 running")
		}
		time.Sleep(10 * time.Millisecond)
	}

	count, err := h.service.MarkInterruptedSubtitleJobs()
	if err != nil || count != 2 {
		t.Fatalf("应标记上次留下的 2 条: count=%d err=%v", count, err)
	}
	for _, id := range []uint{leftover[0].ID, leftover[1].ID} {
		row := mustLoadSubtitleJob(t, id)
		if SubtitleQueueTaskStatus(row.Status) != SubtitleQueueTaskStatusInterrupted || row.FinishedAt == nil || row.Message == "" {
			t.Fatalf("遗留行应改为 interrupted: %+v", row)
		}
	}
	if got := mustLoadSubtitleJob(t, live.ID); SubtitleQueueTaskStatus(got.Status) != SubtitleQueueTaskStatusRunning {
		t.Fatalf("本进程正在跑的任务不应被标中断: %+v", got)
	}
	if got := mustLoadSubtitleJob(t, leftover[2].ID); SubtitleQueueTaskStatus(got.Status) != SubtitleQueueTaskStatusSucceeded {
		t.Fatalf("终态行不应被改动: %+v", got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("本进程的任务应正常完成: %v", err)
	}

	summary, err := h.service.GetInterruptedSubtitleJobs()
	if err != nil || summary.Count != 2 || len(summary.JobIDs) != 2 {
		t.Fatalf("应提示上次中断 2 个: %+v err=%v", summary, err)
	}
	items, _ := h.service.ListSubtitleJobs(0)
	for _, item := range items {
		if item.Status == SubtitleQueueTaskStatusInterrupted && strings.Join(item.Actions, ",") != "retry" {
			t.Fatalf("中断的任务应可重新排队: %+v", item)
		}
	}

	resolved, err := h.service.ResolveSubtitleJob(leftover[0].ID, SubtitleJobActionRetry, p014Input(videoA))
	if err != nil || resolved.Status != SubtitleQueueTaskStatusQueued {
		t.Fatalf("中断的任务应能重新排队: %+v err=%v", resolved, err)
	}
	waitSubtitleJobStatus(t, leftover[0].ID, SubtitleQueueTaskStatusSucceeded)
	if _, err := os.Stat(srtA); err != nil {
		t.Fatalf("重新排队后应生成字幕: %v", err)
	}
	if summary, _ := h.service.GetInterruptedSubtitleJobs(); summary.Count != 1 || summary.JobIDs[0] != leftover[1].ID {
		t.Fatalf("已重排的任务不再计入中断提示: %+v", summary)
	}
	h.service.DismissInterruptedSubtitleJobs()
	if summary, _ := h.service.GetInterruptedSubtitleJobs(); summary.Count != 0 {
		t.Fatalf("忽略后不再提示: %+v", summary)
	}
	if got := mustLoadSubtitleJob(t, leftover[1].ID); SubtitleQueueTaskStatus(got.Status) != SubtitleQueueTaskStatusInterrupted {
		t.Fatalf("忽略只关提示，任务留在历史里: %+v", got)
	}
	if again, _ := h.service.MarkInterruptedSubtitleJobs(); again != 0 {
		t.Fatalf("再次标记不应有新的中断: %d", again)
	}
}

func TestMEDIA10PendingConfirmationSurvivesRestartAndForceReusesIt(t *testing.T) {
	setupVideoServiceTestDB(t)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", nil)
	baseDir := t.TempDir()
	before := newP014Harness(t, baseDir)
	before.setASR(func(*subtitleQueueTask) ([]subtitleparser.Segment, error) { return hallucinatedSegments(), nil })
	if result, err := before.service.GenerateSubtitle(p014Request(video), video.Path, SubtitleGenerateOptions{}); err != nil || result.Status != SubtitleResultStatusValidationFailed {
		t.Fatalf("应待确认: %+v err=%v", result, err)
	}
	job := mustOnlySubtitleJob(t)

	// 「重启」：新的服务实例，内存里的 pending 登记为空。
	after := newP014Harness(t, baseDir)
	if count, err := after.service.MarkInterruptedSubtitleJobs(); err != nil || count != 0 {
		t.Fatalf("待确认不是中断: count=%d err=%v", count, err)
	}
	if after.service.peekPendingSubtitle(video.ID) != nil {
		t.Fatal("新实例不应带着上一次的内存登记")
	}
	resolved, err := after.service.ResolveSubtitleJob(job.ID, SubtitleJobActionForce, p014Input(video))
	if err != nil || resolved.Status != SubtitleQueueTaskStatusQueued {
		t.Fatalf("重启后强制生成应入队: %+v err=%v", resolved, err)
	}
	waitSubtitleJobStatus(t, job.ID, SubtitleQueueTaskStatusSucceeded)
	if after.asrCalls.Load() != 0 {
		t.Fatalf("重启后强制生成应复用临时文件，不重跑识别: %d", after.asrCalls.Load())
	}
	segments, err := subtitleparser.ParseFile(srtPath)
	if err != nil || len(segments) != 10 || segments[0].Text != "Thank you." {
		t.Fatalf("强制生成应写出上次暂存的结果: %+v err=%v", segments, err)
	}
	if _, err := os.Stat(subtitlePendingPath(srtPath)); !os.IsNotExist(err) {
		t.Fatalf("收尾后临时文件应删除: %v", err)
	}
}

func TestMEDIA10ForceWithMissingPendingFailsJobInsteadOfRerunning(t *testing.T) {
	setupVideoServiceTestDB(t)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", nil)
	h := newP014Harness(t, t.TempDir())
	h.setASR(func(*subtitleQueueTask) ([]subtitleparser.Segment, error) { return hallucinatedSegments(), nil })
	if _, err := h.service.GenerateSubtitle(p014Request(video), video.Path, SubtitleGenerateOptions{}); err != nil {
		t.Fatal(err)
	}
	job := mustOnlySubtitleJob(t)
	if err := os.Remove(subtitlePendingPath(srtPath)); err != nil {
		t.Fatal(err)
	}

	resolved, err := h.service.ResolveSubtitleJob(job.ID, SubtitleJobActionForce, p014Input(video))
	if err != nil || resolved.ErrorCode != SubtitleErrorPendingMissing || resolved.Status != SubtitleQueueTaskStatusFailed {
		t.Fatalf("临时文件不在时应报 subtitle_pending_missing: %+v err=%v", resolved, err)
	}
	if got := mustLoadSubtitleJob(t, job.ID); SubtitleQueueTaskStatus(got.Status) != SubtitleQueueTaskStatusFailed {
		t.Fatalf("行应改为 failed，任务中心给出重试: %+v", got)
	}
	if h.asrCalls.Load() != 1 {
		t.Fatalf("不应悄悄重跑识别: %d", h.asrCalls.Load())
	}
}

// ===== MEDIA-13：引擎准备可以取消；状态缓存；结果通知 =====

// writeFakePython 在 path 写一个假的 python3：版本探测回 3.10，import 检查失败（未安装），
// pip 的行为由 pipScript 决定。
func writeFakePython(t *testing.T, path, pipScript string) {
	t.Helper()
	script := "#!/bin/sh\ncase \"$1\" in\n  -c)\n    case \"$2\" in\n      *version_info*) echo \"3.10\"; exit 0 ;;\n    esac\n    exit 1 ;;\n  -m)\n    if [ \"$2\" = \"pip\" ]; then\n" +
		pipScript + "\n    fi\n    exit 0 ;;\nesac\nexit 1\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newFakeWhisperXService 建一个「缺 WhisperX 运行时」的字幕服务：ffmpeg、托管解释器与 venv 解释器都是假的，
// 准备流程只会跑到 pip。
func newFakeWhisperXService(t *testing.T, pipScript string) *SubtitleService {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("需要 /bin/sh")
	}
	service := NewSubtitleService(t.TempDir())
	if err := os.MkdirAll(service.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.BinDir, "ffmpeg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakePython(t, service.whisperXManagedPython(), pipScript)
	writeFakePython(t, service.whisperXVenvPython(), pipScript)
	return service
}

func TestMEDIA13CancelEnginePreparationKillsPipSubprocess(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pip-started")
	service := newFakeWhisperXService(t, fmt.Sprintf("      : > %q\n      exec sleep 30", marker))
	notifier := &stubDesktopNotifier{}
	service.SetDesktopNotifier(notifier)
	if status, _ := service.GetEngineStatuses(); !status[0].NeedsPrepare {
		t.Fatalf("夹具应是需要准备的状态: %+v", status[0])
	}

	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- service.PrepareEngine(SubtitleEngineWhisperX) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pip 子进程没有启动")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := service.PrepareEngine(SubtitleEngineWhisperX); !errors.Is(err, ErrSubtitleEnginePreparing) {
		t.Fatalf("同一时间只允许一轮准备: %v", err)
	}
	if !service.CancelEnginePreparation() {
		t.Fatal("应有进行中的准备可取消")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrSubtitleEnginePreparationCancelled) {
			t.Fatalf("取消后应返回已取消: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("取消没有终止 pip 子进程")
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("取消应立即生效，实际 %s", elapsed)
	}
	if service.CancelEnginePreparation() {
		t.Fatal("准备结束后不应还有可取消的准备")
	}
	time.Sleep(50 * time.Millisecond)
	if got := notifier.Notifications(); len(got) != 0 {
		t.Fatalf("用户取消不发通知: %+v", got)
	}
}

func TestMEDIA13EnginePreparationNotifiesResultWithoutReason(t *testing.T) {
	failing := newFakeWhisperXService(t, "      echo \"ERROR: could not install into /Users/p014-secret/venv\"; exit 1")
	failNotifier := &stubDesktopNotifier{}
	failing.SetDesktopNotifier(failNotifier)
	if err := failing.PrepareEngine(SubtitleEngineWhisperX); err == nil {
		t.Fatal("pip 失败时准备应报错")
	}
	notes := waitForNotifications(t, failNotifier, 1)
	if notes[0].Title != "字幕引擎准备失败" || !strings.Contains(notes[0].Body, "WhisperX") {
		t.Fatalf("失败通知不符: %+v", notes[0])
	}
	assertNoPath(t, "准备失败通知", notes[0].Body, "p014-secret", "could not install")

	succeeding := newFakeWhisperXService(t, "      exit 0")
	okNotifier := &stubDesktopNotifier{}
	succeeding.SetDesktopNotifier(okNotifier)
	if err := succeeding.PrepareEngine(SubtitleEngineWhisperX); err != nil {
		t.Fatalf("pip 成功时准备应成功: %v", err)
	}
	if notes := waitForNotifications(t, okNotifier, 1); notes[0].Title != "字幕引擎已就绪" {
		t.Fatalf("成功通知不符: %+v", notes[0])
	}
}

func TestMEDIA13EngineStatusCachedSixtySecondsAndInvalidatedAroundPreparation(t *testing.T) {
	service := NewSubtitleService(t.TempDir())
	var probes atomic.Int32
	service.engineStatusProbe = func() []SubtitleEngineStatus {
		probes.Add(1)
		return []SubtitleEngineStatus{{Engine: SubtitleEngineWhisperX, DisplayName: "WhisperX", Supported: true, Available: true}}
	}
	clock := &p014Clock{now: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	service.now = clock.Now

	first, _ := service.GetEngineStatuses()
	first[0].DisplayName = "被调用方改掉"
	second, _ := service.GetEngineStatuses()
	if probes.Load() != 1 {
		t.Fatalf("60 秒内应复用缓存，探测 %d 次", probes.Load())
	}
	if second[0].DisplayName != "WhisperX" {
		t.Fatalf("缓存不应被调用方改写: %+v", second[0])
	}
	clock.Advance(59 * time.Second)
	_, _ = service.GetEngineStatuses()
	if probes.Load() != 1 {
		t.Fatalf("59 秒时仍应命中缓存: %d", probes.Load())
	}
	clock.Advance(time.Second)
	_, _ = service.GetEngineStatuses()
	if probes.Load() != 2 {
		t.Fatalf("满 60 秒应重新探测: %d", probes.Load())
	}

	// 准备前失效（准备按最新状态判断），准备后也失效（结果立刻反映到界面）。
	if err := service.PrepareEngine(SubtitleEngineWhisperX); err != nil {
		t.Fatalf("已就绪的引擎准备应直接成功: %v", err)
	}
	if probes.Load() != 3 {
		t.Fatalf("准备前应重新探测: %d", probes.Load())
	}
	_, _ = service.GetEngineStatuses()
	if probes.Load() != 4 {
		t.Fatalf("准备后缓存应失效: %d", probes.Load())
	}
}

// ===== MEDIA-14：10 分钟内不会重复对全库做 stat =====

// D-PC23 把「无字幕」视图的前置同步改成节流 + 后台；原来钉住「首次查询前同步」的
// TestNoSubtitleViewSynchronizesFilesystemBeforeFirstQuery 随之改写为本用例。
func TestMEDIA14NoSubtitleViewReturnsCacheAndSyncsInBackground(t *testing.T) {
	setupVideoServiceTestDB(t)
	clock := useSubtitleIndexSyncClock(t)
	var synced []SubtitleIndexSyncStatus
	var syncedMu sync.Mutex
	setSubtitleIndexSyncEmitter(func(status SubtitleIndexSyncStatus) {
		syncedMu.Lock()
		synced = append(synced, status)
		syncedMu.Unlock()
	})
	root := t.TempDir()
	withSubtitle := models.Video{Name: "with-subtitle.mp4", Path: filepath.Join(root, "with-subtitle.mp4"), Directory: root}
	withoutSubtitle := models.Video{Name: "without-subtitle.mp4", Path: filepath.Join(root, "without-subtitle.mp4"), Directory: root}
	for _, video := range []*models.Video{&withSubtitle, &withoutSubtitle} {
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatal(err)
		}
		mustCreateFile(t, video.Path)
	}
	if err := os.WriteFile(filepath.Join(root, "with-subtitle.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nindexed in background\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 第一次：从没同步过。直接返回当前索引（两条都算无字幕），同步在后台进行。
	if got := noSubtitleViewIDs(t); !sameIDs(got, withSubtitle.ID, withoutSubtitle.ID) {
		t.Fatalf("首屏应直接返回缓存: %v", got)
	}
	waitSubtitleIndexSyncIdle(t)
	status := subtitleIndexSync.status(database.DB)
	if status.Running || status.LastSyncedAt == nil || status.Checked != 2 {
		t.Fatalf("后台同步应已完成并记下完成时间: %+v", status)
	}
	syncedMu.Lock()
	if len(synced) != 1 || synced[0].LastSyncedAt == nil {
		syncedMu.Unlock()
		t.Fatalf("同步完成应发 subtitle-index-synced: %+v", synced)
	}
	syncedMu.Unlock()
	if got := noSubtitleViewIDs(t); !sameIDs(got, withoutSubtitle.ID) {
		t.Fatalf("收到事件后刷新应看到同步结果: %v", got)
	}
	firstSync := *status.LastSyncedAt

	// 10 分钟内：磁盘上又多了字幕，查询也不再全库 stat，返回缓存。
	if err := os.WriteFile(filepath.Join(root, "without-subtitle.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nlater\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clock.Advance(9*time.Minute + 59*time.Second)
	if got := noSubtitleViewIDs(t); !sameIDs(got, withoutSubtitle.ID) {
		t.Fatalf("10 分钟内应返回缓存: %v", got)
	}
	if status := subtitleIndexSync.status(database.DB); status.Running || !status.LastSyncedAt.Equal(firstSync) {
		t.Fatalf("10 分钟内不应开始新的同步: %+v", status)
	}

	// 满 10 分钟：再查一次触发后台同步，完成后结果更新。
	clock.Advance(time.Second)
	if got := noSubtitleViewIDs(t); !sameIDs(got, withoutSubtitle.ID) {
		t.Fatalf("触发同步的这一次仍返回缓存: %v", got)
	}
	waitSubtitleIndexSyncIdle(t)
	if got := noSubtitleViewIDs(t); len(got) != 0 {
		t.Fatalf("第二轮同步后两条都有字幕: %v", got)
	}
	if status := subtitleIndexSync.status(database.DB); !status.LastSyncedAt.After(firstSync) {
		t.Fatalf("第二轮完成时间应更新: %+v", status)
	}
}

func TestMEDIA14SubtitleSearchDoesNotRescanWithinIntervalAndSyncNowForcesRun(t *testing.T) {
	setupVideoServiceTestDB(t)
	clock := useSubtitleIndexSyncClock(t)
	root := t.TempDir()
	video := models.Video{Name: "movie.mp4", Path: filepath.Join(root, "movie.mp4"), Directory: root}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	mustCreateFile(t, video.Path)
	search := &SubtitleSearchService{}

	if status := search.GetSubtitleIndexSyncStatus(); status.LastSyncedAt != nil || status.Running {
		t.Fatalf("还没同步过时不应有完成时间: %+v", status)
	}
	syncSubtitleIndexForTest(t)
	firstSync := *search.GetSubtitleIndexSyncStatus().LastSyncedAt

	// 同步之后才出现的字幕：10 分钟内搜索不会为它去全库 stat。
	if err := os.WriteFile(filepath.Join(root, "movie.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nneedle phrase\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Minute)
	matches, err := search.SearchSubtitleMatches("needle", 10)
	if err != nil || len(matches) != 0 {
		t.Fatalf("10 分钟内搜索只查现有索引: %+v err=%v", matches, err)
	}
	if status := search.GetSubtitleIndexSyncStatus(); status.Running || !status.LastSyncedAt.Equal(firstSync) {
		t.Fatalf("10 分钟内搜索不应开始同步: %+v", status)
	}

	// 「立即同步」不受间隔限制。
	status, err := search.SyncSubtitleIndexNow()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running && !status.LastSyncedAt.After(firstSync) {
		t.Fatalf("立即同步应马上开始一轮: %+v", status)
	}
	waitSubtitleIndexSyncIdle(t)
	if status := search.GetSubtitleIndexSyncStatus(); status.Running || !status.LastSyncedAt.After(firstSync) {
		t.Fatalf("立即同步完成后应更新时间: %+v", status)
	}
	matches, err = search.SearchSubtitleMatches("needle", 10)
	if err != nil || len(matches) != 1 || matches[0].Video.ID != video.ID {
		t.Fatalf("立即同步后应能搜到新字幕: %+v err=%v", matches, err)
	}
}

func TestMEDIA14IndexSyncStateFollowsDatabaseHandle(t *testing.T) {
	setupVideoServiceTestDB(t)
	useSubtitleIndexSyncClock(t)
	syncSubtitleIndexForTest(t)
	if status := subtitleIndexSync.status(database.DB); status.LastSyncedAt == nil {
		t.Fatal("同步后应有完成时间")
	}
	// 换库（恢复备份、切换后端）之后，上一个库的完成时间不能拿来跳过同步。
	setupVideoServiceTestDB(t)
	if status := subtitleIndexSync.status(database.DB); status.LastSyncedAt != nil || status.Running {
		t.Fatalf("新库上不应沿用旧库的同步状态: %+v", status)
	}
	run, started := subtitleIndexSync.request(database.DB, false)
	if run == nil || !started {
		t.Fatal("新库上的第一次请求应开始同步")
	}
	waitSubtitleIndexSyncIdle(t)
}

// ===== MEDIA-08：索引同步写 has_sidecar，旁挂 .ass 的视频离开「无字幕」视图 =====

func TestMEDIA08SidecarAssVideoLeavesNoSubtitleViewAfterIndexSync(t *testing.T) {
	setupVideoServiceTestDB(t)
	useSubtitleIndexSyncClock(t)
	root := t.TempDir()
	withAss := models.Video{Name: "movie.mp4", Path: filepath.Join(root, "movie.mp4"), Directory: root}
	plain := models.Video{Name: "plain.mp4", Path: filepath.Join(root, "plain.mp4"), Directory: root}
	for _, video := range []*models.Video{&withAss, &plain} {
		if err := database.DB.Create(video).Error; err != nil {
			t.Fatal(err)
		}
		mustCreateFile(t, video.Path)
	}
	assPath := filepath.Join(root, "movie.en.ass")
	if err := os.WriteFile(assPath, []byte("[Script Info]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 应用自己的临时文件不算旁挂字幕：plain 旁边只有它时仍是无字幕。
	if err := os.WriteFile(filepath.Join(root, ".plain.cineinsight-pending.srt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	syncSubtitleIndexForTest(t)
	hasSidecar := func(videoID uint) bool {
		t.Helper()
		var state models.SubtitleIndexState
		if err := database.DB.Where("video_id = ?", videoID).First(&state).Error; err != nil {
			t.Fatalf("同步后应有索引状态: %v", err)
		}
		return state.HasSidecar
	}
	if !hasSidecar(withAss.ID) || hasSidecar(plain.ID) {
		t.Fatalf("has_sidecar 写入不符: movie=%v plain=%v", hasSidecar(withAss.ID), hasSidecar(plain.ID))
	}
	if got := noSubtitleViewIDs(t); !sameIDs(got, plain.ID) {
		t.Fatalf("旁挂 .ass 的视频同步后不应再出现在无字幕视图: %v", got)
	}

	// 旁挂字幕删掉后，下一轮同步把标记清回 false。
	if err := os.Remove(assPath); err != nil {
		t.Fatal(err)
	}
	syncSubtitleIndexForTest(t)
	if hasSidecar(withAss.ID) {
		t.Fatal("旁挂字幕删除后 has_sidecar 应清回 false")
	}
	if got := noSubtitleViewIDs(t); !sameIDs(got, withAss.ID, plain.ID) {
		t.Fatalf("旁挂字幕删除后应回到无字幕视图: %v", got)
	}
}
