package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func waitForBackfill(t *testing.T, svc *TechnicalBackfillService) TechnicalBackfillStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := svc.Status()
		if !status.Running {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待技术补全超时: %#v", svc.Status())
	return TechnicalBackfillStatus{}
}

func TestTechnicalBackfillIsExplicitSingleWorkerAndSkipsFreshSnapshots(t *testing.T) {
	setupVideoServiceTestDB(t)
	fresh := createProbeTestVideo(t)
	missing := createProbeTestVideo(t)
	info, err := mediaProbeStat(fresh.Path)
	if err != nil {
		t.Fatalf("读取新鲜文件指纹失败: %v", err)
	}
	now := time.Now()
	if err := database.DB.Create(&models.VideoTechnicalMetadata{
		VideoID: fresh.ID, SuccessfulSourceSize: &info.size, SuccessfulSourceModTimeNS: &info.modTimeNS, ProbedAt: &now,
	}).Error; err != nil {
		t.Fatalf("创建新鲜技术快照失败: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	probe := newMediaProbeServiceWithRunner(func(ctx context.Context, path string) ([]byte, string, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return []byte(multiStreamFFProbeFixture), "", nil
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	})
	svc := NewTechnicalBackfillService(probe)
	first, err := svc.Start(context.Background())
	if err != nil {
		t.Fatalf("启动技术补全失败: %v", err)
	}
	if !first.Running {
		t.Fatalf("技术补全应立即进入运行/准备状态: %#v", first)
	}
	<-started
	second, err := svc.Start(context.Background())
	if err != nil {
		t.Fatalf("重复启动技术补全失败: %v", err)
	}
	if second.StartedAt == nil || first.StartedAt == nil || !second.StartedAt.Equal(*first.StartedAt) {
		t.Fatalf("运行中重复 Start 应返回同一任务: first=%#v second=%#v", first, second)
	}
	close(release)
	status := waitForBackfill(t, svc)
	if !status.Completed || status.Cancelled || status.Total != 1 || status.Processed != 1 || status.Succeeded != 1 || status.Failed != 0 || calls.Load() != 1 {
		t.Fatalf("单 worker 补全状态错误: %#v calls=%d missing=%d", status, calls.Load(), missing.ID)
	}
	var automaticTagRelations int64
	if err := database.DB.Table("video_tags").
		Joins("JOIN tags ON tags.id = video_tags.tag_id").
		Where("video_tags.video_id = ? AND tags.automatic_kind = ?", missing.ID, shortVideoAutomaticTagKind).
		Count(&automaticTagRelations).Error; err != nil {
		t.Fatalf("查询补全后的短视频自动标签关系失败: %v", err)
	}
	if automaticTagRelations != 1 {
		t.Fatalf("补全后的短视频自动标签关系=%d want=1", automaticTagRelations)
	}
}

func TestTechnicalBackfillCandidateDiscoveryLoadsMetadataInBulk(t *testing.T) {
	setupVideoServiceTestDB(t)
	for index := 0; index < 8; index++ {
		createProbeTestVideo(t)
	}
	queryCount := 0
	const callbackName = "test:count-technical-backfill-candidate-queries"
	if err := database.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(*gorm.DB) {
		queryCount++
	}); err != nil {
		t.Fatalf("注册查询计数回调失败: %v", err)
	}
	defer database.DB.Callback().Query().Remove(callbackName)

	candidates, err := loadTechnicalBackfillCandidates(context.Background())
	if err != nil {
		t.Fatalf("批量加载技术补全候选失败: %v", err)
	}
	if len(candidates) != 8 || queryCount != 2 {
		t.Fatalf("候选发现应固定为视频与快照两次查询: candidates=%d queries=%d", len(candidates), queryCount)
	}
}

func TestTechnicalBackfillReportsAndRetriesShortVideoTagSyncFailure(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProbeTestVideo(t)
	probe := newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		return []byte(multiStreamFFProbeFixture), "", nil
	})
	firstRun := NewTechnicalBackfillService(probe)
	firstRun.syncVideoTag = func(uint) error { return errors.New("tag database unavailable") }
	if _, err := firstRun.Start(context.Background()); err != nil {
		t.Fatalf("启动标签同步失败补全任务失败: %v", err)
	}
	failed := waitForBackfill(t, firstRun)
	if failed.Processed != 1 || failed.Failed != 1 || failed.Succeeded != 0 || len(failed.Failures) != 1 {
		t.Fatalf("标签同步失败必须进入任务失败统计: %#v", failed)
	}
	var metadata models.VideoTechnicalMetadata
	if err := database.DB.First(&metadata, "video_id = ?", video.ID).Error; err != nil {
		t.Fatalf("读取标签同步失败状态失败: %v", err)
	}
	if metadata.ProbedAt == nil || metadata.LastError == "" {
		t.Fatalf("标签同步失败应保留成功快照并标记可重试错误: %#v", metadata)
	}

	secondRun := NewTechnicalBackfillService(probe)
	if _, err := secondRun.Start(context.Background()); err != nil {
		t.Fatalf("启动标签同步续跑失败: %v", err)
	}
	retried := waitForBackfill(t, secondRun)
	if retried.Total != 1 || retried.Succeeded != 1 || retried.Failed != 0 {
		t.Fatalf("标签同步失败记录应使下次补全重试: %#v", retried)
	}
}

func TestTechnicalBackfillContinuesAfterFailuresAndEmitsBoundedStatus(t *testing.T) {
	setupVideoServiceTestDB(t)
	for index := 0; index < 52; index++ {
		createProbeTestVideo(t)
	}
	var eventsMu sync.Mutex
	events := make([]TechnicalBackfillStatus, 0)
	probe := newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		return nil, "local decoder error", errors.New("exit status 1")
	})
	svc := NewTechnicalBackfillService(probe)
	svc.SetEventEmitter(func(status TechnicalBackfillStatus) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, status)
	})
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("启动失败补全任务失败: %v", err)
	}
	status := waitForBackfill(t, svc)
	if !status.Completed || status.Failed != 52 || status.Processed != 52 || status.Succeeded != 0 || len(status.Failures) != technicalBackfillFailureLimit {
		t.Fatalf("失败继续/摘要上限错误: %#v", status)
	}
	eventsMu.Lock()
	eventCount := len(events)
	eventsMu.Unlock()
	if eventCount < 52 {
		t.Fatalf("每项完成应发状态事件: count=%d", eventCount)
	}
}

func TestTechnicalBackfillCancellationStopsCurrentProbeAndFutureWork(t *testing.T) {
	setupVideoServiceTestDB(t)
	createProbeTestVideo(t)
	createProbeTestVideo(t)
	started := make(chan struct{})
	var calls atomic.Int32
	probe := newMediaProbeServiceWithRunner(func(ctx context.Context, _ string) ([]byte, string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-ctx.Done()
		return nil, "", ctx.Err()
	})
	svc := NewTechnicalBackfillService(probe)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("启动可取消任务失败: %v", err)
	}
	<-started
	if err := svc.Cancel(); err != nil {
		t.Fatalf("取消技术补全失败: %v", err)
	}
	status := waitForBackfill(t, svc)
	if !status.Cancelled || status.Completed || status.Running || status.Failed != 0 || calls.Load() != 1 {
		t.Fatalf("取消状态错误: %#v calls=%d", status, calls.Load())
	}
}

func TestTechnicalBackfillSkipsVideoDeletedAfterCandidateSnapshot(t *testing.T) {
	setupVideoServiceTestDB(t)
	first := createProbeTestVideo(t)
	second := createProbeTestVideo(t)
	probe := newMediaProbeServiceWithRunner(func(_ context.Context, path string) ([]byte, string, error) {
		if path == first.Path {
			if err := database.DB.Delete(&models.Video{}, second.ID).Error; err != nil {
				t.Fatalf("运行中软删除候选失败: %v", err)
			}
		}
		return []byte(multiStreamFFProbeFixture), "", nil
	})
	svc := NewTechnicalBackfillService(probe)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("启动删除竞态任务失败: %v", err)
	}
	status := waitForBackfill(t, svc)
	if status.Succeeded != 1 || status.Skipped != 1 || status.Processed != 2 || status.Failed != 0 {
		t.Fatalf("运行中删除应跳过: %#v", status)
	}
}

func TestTechnicalBackfillSkipsCurrentVideoDeletedDuringProbe(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProbeTestVideo(t)
	probe := newMediaProbeServiceWithRunner(func(_ context.Context, _ string) ([]byte, string, error) {
		if err := database.DB.Delete(&models.Video{}, video.ID).Error; err != nil {
			t.Fatalf("探测期间软删除当前视频失败: %v", err)
		}
		return []byte(multiStreamFFProbeFixture), "", nil
	})
	svc := NewTechnicalBackfillService(probe)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("启动探测中删除测试失败: %v", err)
	}
	status := waitForBackfill(t, svc)
	if status.Skipped != 1 || status.Succeeded != 0 || status.Failed != 0 || status.Processed != 1 {
		t.Fatalf("探测期间删除当前视频应跳过: %#v", status)
	}
	var metadataCount int64
	if err := database.DB.Model(&models.VideoTechnicalMetadata{}).Where("video_id = ?", video.ID).Count(&metadataCount).Error; err != nil {
		t.Fatalf("统计删除竞态技术快照失败: %v", err)
	}
	if metadataCount != 0 {
		t.Fatalf("探测期间删除不得写入技术快照: count=%d", metadataCount)
	}
}

func TestTechnicalBackfillNewServiceResumesFromPersistedSuccess(t *testing.T) {
	setupVideoServiceTestDB(t)
	createProbeTestVideo(t)
	second := createProbeTestVideo(t)
	firstRun := NewTechnicalBackfillService(newMediaProbeServiceWithRunner(func(_ context.Context, path string) ([]byte, string, error) {
		if path == second.Path {
			return nil, "failed", fmt.Errorf("probe failure")
		}
		return []byte(multiStreamFFProbeFixture), "", nil
	}))
	if _, err := firstRun.Start(context.Background()); err != nil {
		t.Fatalf("启动第一次补全失败: %v", err)
	}
	firstStatus := waitForBackfill(t, firstRun)
	if firstStatus.Succeeded != 1 || firstStatus.Failed != 1 {
		t.Fatalf("第一次补全状态错误: %#v", firstStatus)
	}

	var resumedPathsMu sync.Mutex
	resumedPaths := []string{}
	secondRun := NewTechnicalBackfillService(newMediaProbeServiceWithRunner(func(_ context.Context, path string) ([]byte, string, error) {
		resumedPathsMu.Lock()
		resumedPaths = append(resumedPaths, path)
		resumedPathsMu.Unlock()
		return []byte(multiStreamFFProbeFixture), "", nil
	}))
	_, err := secondRun.Start(context.Background())
	if err != nil {
		t.Fatalf("启动续跑失败: %v", err)
	}
	status := waitForBackfill(t, secondRun)
	resumedPathsMu.Lock()
	paths := append([]string(nil), resumedPaths...)
	resumedPathsMu.Unlock()
	if status.Total != 1 || status.Succeeded != 1 || len(paths) != 1 || paths[0] != second.Path {
		t.Fatalf("续跑处理错误: status=%#v paths=%v", status, paths)
	}
}

func TestTechnicalBackfillStartReturnsWhileCandidateDiscoveryIsPreparing(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewTechnicalBackfillService(newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		t.Fatal("准备阶段取消后不应执行探测")
		return nil, "", nil
	}))
	loaderStarted := make(chan struct{})
	svc.loadCandidates = func(ctx context.Context) ([]technicalBackfillCandidate, error) {
		close(loaderStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	status, err := svc.Start(context.Background())
	if err != nil {
		t.Fatalf("启动准备阶段任务失败: %v", err)
	}
	if !status.Running || !status.Preparing {
		t.Fatalf("Start 应立即返回准备状态: %#v", status)
	}
	<-loaderStarted
	if err := svc.Cancel(); err != nil {
		t.Fatalf("取消准备阶段任务失败: %v", err)
	}
	finished := waitForBackfill(t, svc)
	if !finished.Cancelled || finished.Completed || finished.Preparing {
		t.Fatalf("准备阶段取消状态错误: %#v", finished)
	}
}

func TestTechnicalBackfillCancelWithoutRunningTaskReturnsNotFound(t *testing.T) {
	svc := NewTechnicalBackfillService(NewMediaProbeService())
	if err := svc.Cancel(); !errors.Is(err, ErrTechnicalBackfillNotRunning) {
		t.Fatalf("无运行任务时取消错误=%v want=%v", err, ErrTechnicalBackfillNotRunning)
	}
}
