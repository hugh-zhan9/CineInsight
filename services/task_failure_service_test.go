package services

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func newTaskFailureFixture(t *testing.T) (*TaskFailureService, uint, []models.Video) {
	t.Helper()
	setupVideoServiceTestDB(t)
	if err := database.DB.AutoMigrate(&models.TaskFailureRun{}, &models.TaskFailureItem{}); err != nil {
		t.Fatal(err)
	}
	store := NewTaskFailureService()
	t.Cleanup(store.StopAndWait)
	runID, err := store.Begin(TaskFailureAI)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	videos := []models.Video{}
	for i := 0; i < 73; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%d.mp4", i))
		if err := os.WriteFile(path, []byte("video"), 0600); err != nil {
			t.Fatal(err)
		}
		v := models.Video{Name: fmt.Sprintf("%d.mp4", i), Path: path, Directory: dir}
		if err := database.DB.Create(&v).Error; err != nil {
			t.Fatal(err)
		}
		state := models.AITaggingState{VideoID: v.ID, Status: models.AITaggingStateStatusFailed, AttemptCount: 1, LastError: "failed"}
		if err := database.DB.Create(&state).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.Record(runID, TaskFailureAI, v.ID, fmt.Sprint(i), "", "analysis_failed", "failed"); err != nil {
			t.Fatal(err)
		}
		videos = append(videos, v)
	}
	return store, runID, videos
}
func TestTaskFailureAll73FrozenAndConditionalClaims(t *testing.T) {
	store, runID, videos := newTaskFailureFixture(t)
	page, err := store.List(TaskFailureListRequest{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 50 || page.Total != 73 || page.NextCursor == 0 {
		t.Fatalf("first page %+v", page)
	}
	second, err := store.List(TaskFailureListRequest{RunID: runID, CursorID: page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 23 || second.Items[0].MediaID != videos[50].ID {
		t.Fatal("51st failure unreachable")
	}
	if err := store.Record(runID, TaskFailureAI, videos[0].ID, "0", "", "analysis_failed", "duplicate"); err != nil {
		t.Fatal(err)
	}
	preview, err := store.Preview(TaskFailureRetryRequest{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if preview.EligibleCount != 73 {
		t.Fatalf("eligible=%d", preview.EligibleCount)
	}
	// New failure after confirmation preview must not join the frozen set.
	if err := store.Record(runID, TaskFailureAI, videos[0].ID, "new-attempt", "", "analysis_failed", "later"); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.AITaggingState{}).Where("video_id = ?", videos[1].ID).Update("status", models.AITaggingStateStatusCompleted).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(videos[2].Path, []byte("changed source contents"), 0600); err != nil {
		t.Fatal(err)
	}
	store.RegisterOwners(NewAITaggingService(), nil, nil)
	op, err := store.Confirm(context.Background(), preview.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Confirm(context.Background(), preview.Token); err == nil {
		t.Fatal("double confirmation accepted")
	}
	var result *TaskFailureRetryPage
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, err = store.Results(op, 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		if result.State != "running" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if result.State != "completed" || result.Total != 73 || result.Accepted != 71 || result.Skipped != 2 {
		t.Fatalf("wrong submission result %+v", result)
	}
	var queued int64
	database.DB.Model(&models.AITaggingState{}).Where("status = ?", models.AITaggingStateStatusPending).Count(&queued)
	if queued != 71 {
		t.Fatalf("queued %d", queued)
	}
}
func TestTaskFailureIncompleteEmptyRestartAndLegalTransitions(t *testing.T) {
	store, runID, _ := newTaskFailureFixture(t)
	if _, err := store.Preview(TaskFailureRetryRequest{RunID: runID, ItemIDs: []uint{}}); err == nil {
		t.Fatal("empty selection accepted")
	}
	store.markIncomplete(runID)
	if _, err := store.Preview(TaskFailureRetryRequest{RunID: runID}); !errors.Is(err, ErrTaskFailureIncomplete) {
		t.Fatalf("incomplete error %v", err)
	}
	if err := store.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	var run models.TaskFailureRun
	database.DB.First(&run, runID)
	if run.State != "interrupted" || run.RecordsComplete {
		t.Fatalf("recovery %+v", run)
	}
	if err := store.Finish(runID, "completed"); err == nil {
		t.Fatal("terminal run changed")
	}
}
func TestTaskFailureMediaDeletionPreservesHistory(t *testing.T) {
	store, runID, videos := newTaskFailureFixture(t)
	if err := database.DB.Unscoped().Delete(&videos[0]).Error; err != nil {
		t.Fatal(err)
	}
	p, err := store.List(TaskFailureListRequest{RunID: runID})
	if err != nil || p.Total != 73 {
		t.Fatalf("history erased: %+v %v", p, err)
	}
}

func TestTaskFailureClaimDoesNotUpgradeFrozenFailure(t *testing.T) {
	store, runID, videos := newTaskFailureFixture(t)
	page, err := store.List(TaskFailureListRequest{RunID: runID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	ai := NewAITaggingService()
	ai.beforeFailureClaim = func(tx *gorm.DB) {
		if err := tx.Model(&models.AITaggingState{}).Where("video_id = ?", videos[0].ID).Updates(map[string]any{"attempt_count": 2, "updated_at": time.Now().Add(time.Second)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	result := ai.claimTaskFailures(context.Background(), []taskFailureFrozen{{Item: page.Items[0]}}, nil)
	if result[0].Status != "skipped" || result[0].Reason != "failure_changed" {
		t.Fatalf("new failure claimed: %+v", result)
	}
	var state models.AITaggingState
	database.DB.Where("video_id = ?", videos[0].ID).First(&state)
	if state.Status != models.AITaggingStateStatusFailed || state.AttemptCount != 2 {
		t.Fatalf("new attempt modified: %+v", state)
	}
}

func TestTaskFailureStableAttemptAndProgressCancellation(t *testing.T) {
	store, runID, videos := newTaskFailureFixture(t)
	if err := store.Record(runID, TaskFailureAI, videos[0].ID, "", "", "failed", "one"); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(runID, TaskFailureAI, videos[0].ID, "", "", "failed", "duplicate"); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(TaskFailureListRequest{RunID: runID})
	if err != nil || page.Total != 74 {
		t.Fatalf("duplicate attempt %+v %v", page, err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	store.claims[TaskFailureAI] = func(ctx context.Context, items []taskFailureFrozen, publish func(TaskFailureRetryItemResult)) []TaskFailureRetryItemResult {
		publish(taskFailureResult(items[0], "accepted", "queued"))
		close(started)
		<-release
		for _, item := range items[1:] {
			publish(taskFailureResult(item, "skipped", "cancelled"))
		}
		return nil
	}
	preview, err := store.Preview(TaskFailureRetryRequest{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	op, err := store.Confirm(context.Background(), preview.Token)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	result, err := store.Results(op, 0, 50)
	if err != nil || result.State != "running" || result.Accepted != 1 {
		t.Fatalf("no live progress %+v %v", result, err)
	}
	if err := store.Cancel(op); err != nil {
		t.Fatal(err)
	}
	close(release)
	store.StopAndWait()
	result, err = store.Results(op, 0, 50)
	if err != nil || result.State != "cancelled" || result.Skipped != 73 {
		t.Fatalf("cancel %+v %v", result, err)
	}
}

func TestTaskFailureOwnerClaimsRecoverWithoutRequeue(t *testing.T) {
	store, _, videos := newTaskFailureFixture(t)
	v := videos[0]
	fp, err := mediaProbeStat(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	technical := models.VideoTechnicalMetadata{VideoID: v.ID, LastError: "probe_failed", LastAttemptAt: &now, LastAttemptSourceSize: &fp.size, LastAttemptSourceModTimeNS: &fp.modTimeNS, FailureRetryClaim: "previous-process"}
	if err := database.DB.Create(&technical).Error; err != nil {
		t.Fatal(err)
	}
	proxy := models.VideoPlaybackProxy{VideoID: v.ID, Status: models.PlaybackProxyStatusFailed, Strategy: models.PlaybackProxyStrategyRemux, SourceSize: fp.size, SourceModTimeNS: fp.modTimeNS, LastUsedAt: now, LastError: "encode_failed", FailureRetryClaim: "previous-process"}
	if err := database.DB.Create(&proxy).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	database.DB.First(&technical, "video_id = ?", v.ID)
	database.DB.First(&proxy, "video_id = ?", v.ID)
	if technical.FailureRetryClaim != "" || proxy.FailureRetryClaim != "" || technical.LastError != "probe_failed" || proxy.Status != models.PlaybackProxyStatusFailed {
		t.Fatal("recovery changed underlying failed work")
	}
}

func TestTaskFailureTechnicalCancelReleasesUnstartedClaim(t *testing.T) {
	store, _, videos := newTaskFailureFixture(t)
	runID, err := store.Begin(TaskFailureTechnical)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range videos[:2] {
		fp, err := mediaProbeStat(v.Path)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		row := models.VideoTechnicalMetadata{VideoID: v.ID, LastError: "failed", LastAttemptAt: &now, LastAttemptSourceSize: &fp.size, LastAttemptSourceModTimeNS: &fp.modTimeNS}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.Record(runID, TaskFailureTechnical, v.ID, "", "", "probe_failed", "failed"); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.List(TaskFailureListRequest{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	probe := newMediaProbeServiceWithRunner(func(ctx context.Context, _ string) ([]byte, string, error) {
		close(started)
		<-ctx.Done()
		return nil, "", ctx.Err()
	})
	svc := NewTechnicalBackfillService(probe)
	results := svc.claimTaskFailures(context.Background(), []taskFailureFrozen{{Item: page.Items[0]}, {Item: page.Items[1]}}, nil)
	if results[0].Status != "accepted" || results[1].Status != "accepted" {
		t.Fatalf("queue %+v", results)
	}
	<-started
	svc.StopAndWait()
	reason, err := taskFailureEligibility(database.DB, TaskFailureTechnical, page.Items[1])
	if err != nil || reason != "" {
		t.Fatalf("cancel stranded original failure: %q %v", reason, err)
	}
}

func TestTaskFailureTechnicalClaimKeepsStatusAndCancelResponsive(t *testing.T) {
	store, _, videos := newTaskFailureFixture(t)
	v := videos[0]
	fp, err := mediaProbeStat(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	row := models.VideoTechnicalMetadata{VideoID: v.ID, LastError: "failed", LastAttemptSourceSize: &fp.size, LastAttemptSourceModTimeNS: &fp.modTimeNS}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	runID, err := store.Begin(TaskFailureTechnical)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(runID, TaskFailureTechnical, v.ID, "", "", "failed", "failed"); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(TaskFailureListRequest{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	svc := NewTechnicalBackfillService(NewMediaProbeService())
	svc.beforeFailureClaim = func(tx *gorm.DB) { close(entered); <-tx.Statement.Context.Done() }
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.claimTaskFailures(context.Background(), []taskFailureFrozen{{Item: page.Items[0]}}, nil)
	}()
	<-entered
	statusDone := make(chan TechnicalBackfillStatus, 1)
	go func() { statusDone <- svc.Status() }()
	select {
	case status := <-statusDone:
		if !status.Running || !status.Preparing {
			t.Fatalf("no reservation: %+v", status)
		}
	case <-time.After(time.Second):
		t.Fatal("status blocked by claim DB")
	}
	svc.StopAndWait()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt claim")
	}
}

func TestTaskFailureTechnicalWorkerPersistsBeyondSummaryLimit(t *testing.T) {
	store, _, _ := newTaskFailureFixture(t)
	probe := newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		return nil, "", errors.New("fixture probe failed")
	})
	svc := NewTechnicalBackfillService(probe)
	svc.SetTaskFailureService(store)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := waitForBackfill(t, svc)
	svc.StopAndWait()
	if status.Failed != 73 || len(status.Failures) != 50 {
		t.Fatalf("summary %+v", status)
	}
	runs, err := store.ListRuns(TaskFailureTechnical)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs %+v %v", runs, err)
	}
	page, err := store.List(TaskFailureListRequest{RunID: runs[0].ID, CursorID: 0, Limit: 200})
	if err != nil || page.Total != 73 || len(page.Items) != 73 || !page.RecordsComplete {
		t.Fatalf("lost actual worker failures %+v %v", page, err)
	}
}

func TestTaskFailureTechnicalRechecksQueuedSource(t *testing.T) {
	store, _, videos := newTaskFailureFixture(t)
	runID, err := store.Begin(TaskFailureTechnical)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range videos[:2] {
		fp, _ := mediaProbeStat(v.Path)
		row := models.VideoTechnicalMetadata{VideoID: v.ID, LastError: "failed", LastAttemptSourceSize: &fp.size, LastAttemptSourceModTimeNS: &fp.modTimeNS}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.Record(runID, TaskFailureTechnical, v.ID, "", "", "failed", "failed"); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.List(TaskFailureListRequest{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	probe := newMediaProbeServiceWithRunner(func(ctx context.Context, path string) ([]byte, string, error) {
		calls++
		if path == videos[0].Path {
			close(entered)
			<-release
		}
		return []byte(multiStreamFFProbeFixture), "", nil
	})
	svc := NewTechnicalBackfillService(probe)
	results := svc.claimTaskFailures(context.Background(), []taskFailureFrozen{{Item: page.Items[0]}, {Item: page.Items[1]}}, nil)
	if results[1].Status != "accepted" {
		t.Fatalf("not accepted: %+v", results)
	}
	<-entered
	if err := os.WriteFile(videos[1].Path, []byte("replacement after accepted"), 0600); err != nil {
		t.Fatal(err)
	}
	close(release)
	status := waitForBackfill(t, svc)
	svc.StopAndWait()
	if calls != 1 || status.Skipped != 1 || !strings.Contains(svc.TaskFailureWarning(), "源文件") {
		t.Fatalf("processed new source: calls=%d status=%+v", calls, status)
	}
}
func TestTaskFailureProxyRechecksQueuedSource(t *testing.T) {
	store, _, _ := newTaskFailureFixture(t)
	runID, err := store.Begin(TaskFailureProxy)
	if err != nil {
		t.Fatal(err)
	}
	videos := []models.Video{createProxyTestVideo(t, "first.mkv", 10), createProxyTestVideo(t, "second.mkv", 10)}
	for _, v := range videos {
		writeProxySnapshot(t, v, "h264", "aac", 0, 0)
		fp, _ := mediaProbeStat(v.Path)
		row := models.VideoPlaybackProxy{VideoID: v.ID, Status: models.PlaybackProxyStatusFailed, Strategy: models.PlaybackProxyStrategyRemux, SourceSize: fp.size, SourceModTimeNS: fp.modTimeNS, LastUsedAt: time.Now(), LastError: "failed"}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.Record(runID, TaskFailureProxy, v.ID, "", "", "failed", "failed"); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.List(TaskFailureListRequest{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	svc, stub := newProxyTestService(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	stub.beforeWrite = func([]string) {
		if len(stub.calls) == 1 {
			close(entered)
			<-release
		}
	}
	results := svc.claimTaskFailures(context.Background(), []taskFailureFrozen{{Item: page.Items[0]}, {Item: page.Items[1]}}, nil)
	if results[1].Status != "accepted" {
		t.Fatalf("not accepted %+v", results)
	}
	<-entered
	if err := os.WriteFile(videos[1].Path, []byte("replacement after accepted"), 0600); err != nil {
		t.Fatal(err)
	}
	close(release)
	status := waitForProxyIdle(t, svc)
	svc.StopAndWait()
	if len(stub.calls) != 1 || status.Skipped != 1 || status.Failed != 0 {
		t.Fatalf("new source encoded %+v calls=%d", status, len(stub.calls))
	}
	if status.Results[1].Code != PlaybackProxyCodeRetrySourceChanged {
		t.Fatalf("skip reason missing %+v", status.Results)
	}
}
func TestTaskFailureAIRechecksPendingSourceBeforeAnalysis(t *testing.T) {
	store, runID, videos := newTaskFailureFixture(t)
	page, err := store.List(TaskFailureListRequest{RunID: runID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewAITaggingService()
	result := svc.claimTaskFailures(context.Background(), []taskFailureFrozen{{Item: page.Items[0]}}, nil)
	if result[0].Status != "accepted" {
		t.Fatal(result)
	}
	if err := os.WriteFile(videos[0].Path, []byte("replacement after queue"), 0600); err != nil {
		t.Fatal(err)
	}
	svc.clientFactory = func(AITaggingConfig) AITaggingAIClient { t.Fatal("AI request for changed source"); return nil }
	if err := svc.processVideoWithConfig(context.Background(), videos[0], AITaggingConfig{}); err != nil {
		t.Fatal(err)
	}
	var state models.AITaggingState
	database.DB.Where("video_id = ?", videos[0].ID).First(&state)
	if state.Status != models.AITaggingStateStatusSkipped || state.SkipReason != "source_changed" || state.FailureRetrySourceVersion != "" {
		t.Fatalf("source guard %+v", state)
	}
	if err := svc.RetryVideo(videos[0].ID); err != nil {
		t.Fatal(err)
	}
	database.DB.Where("video_id = ?", videos[0].ID).First(&state)
	if state.FailureRetrySourceVersion != "" || state.Status != models.AITaggingStateStatusPending {
		t.Fatal("ordinary retry retained old grant")
	}
}

func TestTaskFailureRetentionKeepsUnresolvedAndIncompleteRuns(t *testing.T) {
	store, runID, _ := newTaskFailureFixture(t)
	if err := database.DB.Model(&models.TaskFailureRun{}).Where("id = ?", runID).Update("state", "cancelled").Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	incomplete := models.TaskFailureRun{TaskKey: TaskFailureAI, StartedAt: now, FinishedAt: &now, State: "completed", RecordsComplete: false}
	if err := database.DB.Create(&incomplete).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 22; i++ {
		row := models.TaskFailureRun{TaskKey: TaskFailureAI, StartedAt: now, FinishedAt: &now, State: "completed", RecordsComplete: true}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Prune(); err != nil {
		t.Fatal(err)
	}
	runs, err := store.ListRuns(TaskFailureAI)
	if err != nil || len(runs) != 22 {
		t.Fatalf("retention %+v %v", runs, err)
	}
	var count int64
	database.DB.Model(&models.TaskFailureItem{}).Where("run_id = ?", runID).Count(&count)
	if count != 73 {
		t.Fatal("unresolved cancelled failures were pruned")
	}
}

func TestTaskFailureTechnicalFrozenSourceAfterProbeGate(t *testing.T) {
	store, _, videos := newTaskFailureFixture(t)
	v := videos[0]
	fp, _ := mediaProbeStat(v.Path)
	row := models.VideoTechnicalMetadata{VideoID: v.ID, LastError: "failed", LastAttemptSourceSize: &fp.size, LastAttemptSourceModTimeNS: &fp.modTimeNS}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	runID, err := store.Begin(TaskFailureTechnical)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(runID, TaskFailureTechnical, v.ID, "", "", "failed", "failed"); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(TaskFailureListRequest{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	probe := newMediaProbeServiceWithRunner(func(ctx context.Context, path string) ([]byte, string, error) {
		calls++
		return []byte(multiStreamFFProbeFixture), "", nil
	})
	release, err := probe.acquireVideo(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewTechnicalBackfillService(probe)
	result := svc.claimTaskFailures(context.Background(), []taskFailureFrozen{{Item: page.Items[0]}}, nil)
	if result[0].Status != "accepted" {
		release()
		svc.StopAndWait()
		t.Fatal(result)
	}
	deadline := time.Now().Add(3 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		probe.gatesMu.Lock()
		waiting = probe.gates[v.ID].refs == 2
		probe.gatesMu.Unlock()
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !waiting {
		release()
		svc.StopAndWait()
		t.Fatal("worker did not reach probe gate")
	}
	if err := os.WriteFile(v.Path, []byte("new file after frozen source check before actual probe"), 0600); err != nil {
		release()
		svc.StopAndWait()
		t.Fatal(err)
	}
	release()
	status := waitForBackfill(t, svc)
	svc.StopAndWait()
	if calls != 0 {
		t.Fatalf("OLD RETRY GRANT PROBED NEW SOURCE: runner calls=%d status=%+v", calls, status)
	}
}

func TestTaskFailureProxyFrozenSourceAfterProbeGate(t *testing.T) {
	store, _, videos := newTaskFailureFixture(t)
	v := videos[0]
	fp, _ := mediaProbeStat(v.Path)
	row := models.VideoPlaybackProxy{VideoID: v.ID, Status: models.PlaybackProxyStatusFailed, Strategy: models.PlaybackProxyStrategyRemux, SourceSize: fp.size, SourceModTimeNS: fp.modTimeNS, LastUsedAt: time.Now(), LastError: "failed"}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	runID, err := store.Begin(TaskFailureProxy)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(runID, TaskFailureProxy, v.ID, "", "", "failed", "failed"); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(TaskFailureListRequest{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	probe := newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		calls++
		return []byte(multiStreamFFProbeFixture), "", nil
	})
	release, err := probe.acquireVideo(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewPlaybackProxyService(t.TempDir(), probe)
	svc.runFFmpeg = func(context.Context, []string) (string, error) {
		t.Error("changed source encoded")
		return "", errors.New("unexpected encode")
	}
	results := svc.claimTaskFailures(context.Background(), []taskFailureFrozen{{Item: page.Items[0]}}, nil)
	if results[0].Status != "accepted" {
		release()
		svc.StopAndWait()
		t.Fatal(results)
	}
	waiting := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		probe.gatesMu.Lock()
		waiting = probe.gates[v.ID].refs == 2
		probe.gatesMu.Unlock()
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !waiting {
		release()
		svc.StopAndWait()
		t.Fatal("proxy did not wait for technical gate")
	}
	if err := os.WriteFile(v.Path, []byte("changed while proxy waited for probe"), 0600); err != nil {
		release()
		svc.StopAndWait()
		t.Fatal(err)
	}
	release()
	status := waitForProxyIdle(t, svc)
	svc.StopAndWait()
	if calls != 0 || status.Skipped != 1 || status.Failed != 0 || status.Results[0].Code != PlaybackProxyCodeRetrySourceChanged {
		t.Fatalf("new source was processed: calls=%d status=%+v", calls, status)
	}
}

func TestTaskFailureCancelledFrozenGrantPreservesFailure(t *testing.T) {
	setupVideoServiceTestDB(t)
	v := createProxyTestVideo(t, "cancel-grant.mp4", 10)
	fp, _ := mediaProbeStat(v.Path)
	expected := videoSourceVersion(v.ID, fp.size, fp.modTimeNS)
	row := models.VideoTechnicalMetadata{VideoID: v.ID, LastError: "original failure", LastAttemptSourceSize: &fp.size, LastAttemptSourceModTimeNS: &fp.modTimeNS}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v.Path, []byte("new contents after frozen grant"), 0600); err != nil {
		t.Fatal(err)
	}
	probe := newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) { t.Fatal("runner reached"); return nil, "", nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := probe.RefreshForSource(ctx, v.ID, expected); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	var got models.VideoTechnicalMetadata
	if err := database.DB.First(&got, "video_id = ?", v.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.LastError != row.LastError || !got.UpdatedAt.Equal(row.UpdatedAt) {
		t.Fatalf("cancelled old grant rewrote failure for changed source: old=%q new=%q", row.LastError, got.LastError)
	}
}

func TestTaskFailurePreviewContextCancellationLeavesNoToken(t *testing.T) {
	store, run, _ := newTaskFailureFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	name := "test:cancel_preview_before_publish"
	database.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "task_failure_items" {
			cancel()
		}
	})
	defer database.DB.Callback().Query().Remove(name)
	if _, err := store.PreviewContext(ctx, TaskFailureRetryRequest{RunID: run}); !errors.Is(err, context.Canceled) {
		t.Fatalf("preview was not cancelled: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.previews) != 0 {
		t.Fatal("cancelled preview left confirmable token")
	}
}
