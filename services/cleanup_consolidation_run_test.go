package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func prepareConsolidationRun(t *testing.T, kind string) (*CleanupService, *CleanupConsolidationPreview, models.Video, models.Video, string) {
	t.Helper()
	s, r, a, b, root := consolidationTestFixture(t, kind)
	if err := s.ConfigureConsolidation(filepath.Join(root, "app-data"), nil); err != nil {
		t.Fatal(err)
	}
	p, err := s.PreviewConsolidation(context.Background(), r)
	if err != nil || len(p.Errors) > 0 {
		t.Fatalf("preview=%+v err=%v", p, err)
	}
	t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
	return s, p, a, b, root
}

func waitConsolidation(t *testing.T, s *CleanupService, id uint) *CleanupConsolidationStatus {
	t.Helper()
	s.consolidationMu.Lock()
	done := s.consolidationDone
	s.consolidationMu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not finish")
		}
	}
	state, err := s.ConsolidationStatus(id)
	if err != nil || state == nil {
		t.Fatal(state, err)
	}
	return state
}

func TestCleanupConsolidationRunPreservesIDAndSeparatesCleanup(t *testing.T) {
	s, p, a, b, _ := prepareConsolidationRun(t, "near")
	tag := models.Tag{Name: "curated"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&a).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	result, err := s.StartConsolidation(context.Background(), p.PreviewID, true)
	if err != nil {
		t.Fatal(err)
	}
	state := waitConsolidation(t, s, result.ID)
	if state.Status != "completed" || state.Completed != 1 || state.ActiveSlot != nil || state.BytesDone != p.MoveBytes {
		t.Fatalf("%+v", state)
	}
	var got models.Video
	if err := database.DB.Preload("Tags").First(&got, a.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID || got.Path != p.Items[0].DestinationPath || len(got.Tags) < 1 || got.Tags[0].ID != tag.ID {
		t.Fatalf("lost ID/relations: %+v", got)
	}
	if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
		t.Fatalf("old source remains: %v", err)
	}
	if string(mustReadBytes(t, got.Path)) != "aaaa" || string(mustReadBytes(t, b.Path)) != "bbbb" {
		t.Fatal("keeper or deletion candidate changed")
	}
	duplicate, err := s.StartConsolidation(context.Background(), p.PreviewID, true)
	if err != nil || duplicate.ID != state.ID {
		t.Fatal("start was not idempotent", err)
	}
	var count int64
	database.DB.Model(&models.CleanupConsolidationTask{}).Count(&count)
	if count != 1 {
		t.Fatal(count)
	}
	var roots []models.ScanDirectory
	database.DB.Find(&roots)
	if len(roots) != 1 || roots[0].Path != p.Destination {
		t.Fatalf("%+v", roots)
	}
	if !s.status.Stale {
		t.Fatal("analysis was not invalidated")
	}
}

func TestCleanupConsolidationRunStayDoesNotTouchFiles(t *testing.T) {
	s, p, _, b, root := prepareConsolidationRun(t, "exact")
	if !p.Items[0].Stay || p.Groups[0].KeeperID != b.ID {
		t.Fatal("exact baseline changed")
	}
	before := consolidationTree(t, filepath.Join(root, "target"))
	result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	state := waitConsolidation(t, s, result.ID)
	if state.Status != "completed" || state.Completed != 1 || state.BytesDone != 0 {
		t.Fatalf("%+v", state)
	}
	after := consolidationTree(t, filepath.Join(root, "target"))
	if len(before) != len(after) {
		t.Fatal("stay wrote files")
	}
	for path, value := range before {
		if after[path] != value {
			t.Fatal("stay changed", path)
		}
	}
}

func TestCleanupConsolidationRunRechecksSourcesAndFixedTargets(t *testing.T) {
	for _, scenario := range []string{"source", "parent", "target", "source-alias", "target-alias", "attachment"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, a, _, root := prepareConsolidationRun(t, "near")
			switch scenario {
			case "source":
				if err := os.WriteFile(a.Path, []byte("changed"), 0644); err != nil {
					t.Fatal(err)
				}
			case "parent":
				if err := os.Rename(p.Destination, p.Destination+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(p.Destination, 0755); err != nil {
					t.Fatal(err)
				}
			case "target":
				if err := os.WriteFile(p.Items[0].DestinationPath, []byte("occupied"), 0644); err != nil {
					t.Fatal(err)
				}
			case "source-alias":
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(filepath.Dir(a.Path), alias); err != nil {
					t.Fatal(err)
				}
				if err := database.DB.Create(&models.Video{Path: filepath.Join(alias, filepath.Base(a.Path))}).Error; err != nil {
					t.Fatal(err)
				}
			case "target-alias":
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(p.Destination, alias); err != nil {
					t.Fatal(err)
				}
				if err := database.DB.Create(&models.Video{Path: filepath.Join(alias, filepath.Base(p.Items[0].DestinationPath))}).Error; err != nil {
					t.Fatal(err)
				}
			case "attachment":
				if err := os.WriteFile(strings.TrimSuffix(a.Path, ".mp4")+".srt", []byte(writerTestSRT), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.StartConsolidation(context.Background(), p.PreviewID, false); err == nil {
				t.Fatal("stale preview accepted")
			}
			var count int64
			database.DB.Model(&models.CleanupConsolidationTask{}).Count(&count)
			if count != 0 {
				t.Fatal("stale preview claimed a task")
			}
		})
	}
}

func TestCleanupConsolidationRunCancelSlowCopyAllowsLibraryReads(t *testing.T) {
	s, p, a, _, _ := prepareConsolidationRun(t, "near")
	reached := make(chan struct{})
	var once sync.Once
	s.consolidationHooks = &consolidationExecutionHooks{forceCopy: true, chunk: func(ctx context.Context, _ int64) error {
		once.Do(func() { close(reached) })
		<-ctx.Done()
		return ctx.Err()
	}}
	result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	read := make(chan error, 1)
	go func() {
		release, err := rLockLibraryPaths()
		if err != nil {
			read <- err
			return
		}
		defer release()
		var video models.Video
		read <- database.DB.First(&video, a.ID).Error
	}()
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow copy held global path lock or transaction")
	}
	if err := s.CancelConsolidation(); err != nil {
		t.Fatal(err)
	}
	state := waitConsolidation(t, s, result.ID)
	if state.Status != "cancelled" || state.Completed != 0 || state.ActiveSlot != nil {
		t.Fatalf("%+v", state)
	}
	if string(mustReadBytes(t, a.Path)) != "aaaa" {
		t.Fatal("cancel lost source")
	}
	entries, _ := os.ReadDir(p.Destination)
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".part") {
			t.Fatal("owned partial target leaked")
		}
	}
	if _, err := os.Stat(p.Items[0].DestinationPath); !os.IsNotExist(err) {
		t.Fatal("cancel published")
	}
}

func TestCleanupConsolidationRunConcurrencyAndLifecycle(t *testing.T) {
	s, p, _, _, root := prepareConsolidationRun(t, "near")
	entered, release := make(chan struct{}), make(chan struct{})
	s.consolidationHooks = &consolidationExecutionHooks{checkpoint: func(phase string, _ uint, _ int) error {
		if phase == "planned" {
			close(entered)
			<-release
		}
		return nil
	}}
	first, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	second := &CleanupService{}
	second.SetConsolidationVideoService(&VideoService{})
	if err := second.ConfigureConsolidation(filepath.Join(root, "app-data"), nil); err != nil {
		t.Fatal(err)
	}
	same, err := second.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil || same.ID != first.ID {
		t.Fatal("repeat other instance", err)
	}
	if err := second.RecoverConsolidations(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _ := second.ConsolidationStatus(first.ID)
	if state.Status != "running" || state.ActiveSlot == nil {
		t.Fatal("recovered active executor")
	}
	stopped := make(chan struct{})
	go func() { _ = s.StopConsolidationsAndWait(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("stop returned before file executor")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-stopped
	state = waitConsolidation(t, s, first.ID)
	if state.Status != "cancelled" {
		t.Fatalf("%+v", state)
	}
	if _, err := s.StartConsolidation(context.Background(), p.PreviewID, false); err == nil {
		t.Fatal("maintenance accepted task")
	}
	s.ResumeConsolidations()
	if _, err := s.StartConsolidation(context.Background(), p.PreviewID, false); err != nil {
		t.Fatal("resume not reopened", err)
	}
}

func TestCleanupConsolidationRunFailuresRetainSource(t *testing.T) {
	for _, scenario := range []string{"space", "checksum", "db", "source-after-copy", "new-owner-after-copy", "occupied-after-copy"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, a, _, _ := prepareConsolidationRun(t, "near")
			injected := false
			hooks := &consolidationExecutionHooks{forceCopy: true}
			switch scenario {
			case "space":
				hooks.freeBytes = func(string) (uint64, error) { return 0, nil }
			case "checksum":
				hooks.checkpoint = func(phase string, _ uint, _ int) error {
					if phase == "copied" {
						paths, _ := filepath.Glob(filepath.Join(p.Destination, "*.part"))
						if err := os.WriteFile(paths[0], []byte("evil"), 0644); err != nil {
							return err
						}
						stamp := time.Unix(0, p.Items[0].Files[0].Source.ModTimeNS)
						return os.Chtimes(paths[0], stamp, stamp)
					}
					return nil
				}
			case "db":
				if err := database.DB.Callback().Update().Before("gorm:update").Register("consolidation_fail_video", func(tx *gorm.DB) {
					if tx.Statement.Table == "videos" {
						tx.AddError(errors.New("injected video DB failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer database.DB.Callback().Update().Remove("consolidation_fail_video")
			default:
				hooks.checkpoint = func(phase string, _ uint, _ int) error {
					if phase != "verified" || injected {
						return nil
					}
					injected = true
					switch scenario {
					case "source-after-copy":
						return os.WriteFile(a.Path, []byte("user changed"), 0644)
					case "new-owner-after-copy":
						alias := filepath.Join(filepath.Dir(filepath.Dir(a.Path)), "late-alias")
						if err := os.Symlink(filepath.Dir(a.Path), alias); err != nil {
							return err
						}
						return database.DB.Create(&models.Video{Path: filepath.Join(alias, filepath.Base(a.Path))}).Error
					default:
						return os.WriteFile(p.Items[0].DestinationPath, []byte("external"), 0644)
					}
				}
			}
			s.consolidationHooks = hooks
			result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
			if err != nil {
				t.Fatal(err)
			}
			state := waitConsolidation(t, s, result.ID)
			if state.Status != "failed" || state.Completed != 0 {
				t.Fatalf("%+v", state)
			}
			var got models.Video
			if err := database.DB.First(&got, a.ID).Error; err != nil || got.Path != a.Path {
				t.Fatal(got.Path, err)
			}
			if _, err := os.Stat(a.Path); err != nil {
				t.Fatal("source lost", err)
			}
			if scenario == "occupied-after-copy" && string(mustReadBytes(t, p.Items[0].DestinationPath)) != "external" {
				t.Fatal("overwrote external target")
			}
		})
	}
}

func TestCleanupConsolidationRunPartialStopsBeforeNextItem(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	a := consolidationTestVideo(t, filepath.Join(root, "a", "a.mp4"), "aa")
	b := consolidationTestVideo(t, filepath.Join(root, "b", "b.mp4"), "bb")
	c := consolidationTestVideo(t, filepath.Join(root, "c", "c.mp4"), "cc")
	d := consolidationTestVideo(t, filepath.Join(root, "d", "d.mp4"), "dd")
	s, r := consolidationTestService(t, &CleanupAnalysis{NearDuplicateGroups: []CleanupDuplicateGroup{{Original: a, Candidates: []models.Video{b}}, {Original: c, Candidates: []models.Video{d}}}})
	r.Destination = target
	if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
	p, err := s.PreviewConsolidation(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	s.consolidationHooks = &consolidationExecutionHooks{checkpoint: func(phase string, id uint, _ int) error {
		if phase == "planned" && id == c.ID {
			return errors.New("second item failed")
		}
		return nil
	}}
	result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	state := waitConsolidation(t, s, result.ID)
	if state.Status != "failed" || state.Completed != 1 {
		t.Fatalf("%+v", state)
	}
	var got models.Video
	database.DB.First(&got, a.ID)
	if got.Path == a.Path {
		t.Fatal("rolled back committed item")
	}
	if string(mustReadBytes(t, c.Path)) != "cc" || string(mustReadBytes(t, b.Path)) != "bb" {
		t.Fatal("partial changed other source")
	}
	if _, err := s.ReviewConsolidation(context.Background(), result.ID); err == nil {
		t.Fatal("partial entered cleanup")
	}
}

func TestCleanupConsolidationSummariesAndProgressDoNotExpandLargePlans(t *testing.T) {
	setupVideoServiceTestDB(t)
	task := models.CleanupConsolidationTask{PreviewID: "large-summary", OwnerScope: "test", Status: "running", Version: 1, Total: 10000}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	plan := models.CleanupConsolidationTaskPlan{TaskID: task.ID, PlanJSON: strings.Repeat("a", 8*1024*1024)}
	item := models.CleanupConsolidationTaskItem{TaskID: task.ID, Position: 0, JournalJSON: "unchanged-journal"}
	if err := database.DB.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	const callback = "consolidation:summary-child-query"
	if err := database.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "cleanup_consolidation_task_plans" || tx.Statement.Table == "cleanup_consolidation_task_items" {
			t.Error("summary accessed child records")
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Query().Remove(callback)
	s := &CleanupService{}
	rows, err := s.ListConsolidations(100)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if rows[0].Total != 10000 {
		t.Fatal("list loaded full plan")
	}
	notifications := 0
	e := consolidationExecution{task: task, journal: cleanupConsolidationJournal{SchemaVersion: 1, Items: make([]cleanupConsolidationItemJournal, 10000)}, onChange: func(summary CleanupConsolidationSummary) {
		notifications++
		if summary.Total != 10000 {
			t.Fatal("event retained full plan")
		}
	}}
	started := time.Now()
	for i := 0; i < 1000; i++ {
		e.notify()
	}
	if time.Since(started) > time.Second {
		t.Fatal("summary event cost grew with plan size")
	}
	if err := e.progress(context.Background(), 123); err != nil {
		t.Fatal(err)
	}
	var stored models.CleanupConsolidationTask
	if err := database.DB.Select("bytes_done", "version").First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.BytesDone != 123 || stored.Version != 2 || notifications != 1001 {
		t.Fatal("progress reserialized journal or failed CAS", stored.Version, stored.BytesDone, notifications)
	}
	if err := database.DB.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.First(&item, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.JournalJSON != "unchanged-journal" {
		t.Fatal("progress changed child journal")
	}
}

func TestCleanupConsolidationCancelDoesNotRehashEarlierCompletedItems(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	a := consolidationTestVideo(t, filepath.Join(root, "one", "a.mp4"), "first")
	b := consolidationTestVideo(t, filepath.Join(root, "one", "b.mp4"), "candidate")
	if err := os.Truncate(a.Path, 32*1024*1024); err != nil {
		t.Fatal(err)
	}
	c := consolidationTestVideo(t, filepath.Join(root, "two", "c.mp4"), "second")
	d := consolidationTestVideo(t, filepath.Join(root, "two", "d.mp4"), "candidate")
	s, r := consolidationTestService(t, &CleanupAnalysis{NearDuplicateGroups: []CleanupDuplicateGroup{{Original: a, Candidates: []models.Video{b}}, {Original: c, Candidates: []models.Video{d}}}})
	r.Destination = target
	if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
	p, err := s.PreviewConsolidation(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	reconciled := 0
	s.consolidationHooks = &consolidationExecutionHooks{reconcile: func(context.Context) error { reconciled++; return nil }, checkpoint: func(phase string, id uint, _ int) error {
		if phase == "verified" && id == c.ID {
			return s.CancelConsolidation()
		}
		return nil
	}}
	task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	state := waitConsolidation(t, s, task.ID)
	if state.Status != "cancelled" || state.Completed != 1 || reconciled != 1 {
		t.Fatalf("cancel rescanned prior items: status=%s completed=%d reconciled=%d", state.Status, state.Completed, reconciled)
	}
	if _, err := os.Stat(p.Items[0].DestinationPath); err != nil {
		t.Fatal("committed video lost", err)
	}
	if string(mustReadBytes(t, c.Path)) != "second" {
		t.Fatal("cancelled source lost")
	}
}

func TestCleanupConsolidationRunCopiedFailureUsesCurrentItem(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	a := consolidationTestVideo(t, filepath.Join(root, "a", "a.mp4"), "aa")
	b := consolidationTestVideo(t, filepath.Join(root, "b", "b.mp4"), "bb")
	c := consolidationTestVideo(t, filepath.Join(root, "c", "c.mp4"), "cc")
	d := consolidationTestVideo(t, filepath.Join(root, "d", "d.mp4"), "dd")
	s, r := consolidationTestService(t, &CleanupAnalysis{NearDuplicateGroups: []CleanupDuplicateGroup{{Original: a, Candidates: []models.Video{b}}, {Original: c, Candidates: []models.Video{d}}}})
	r.Destination = target
	if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
	p, err := s.PreviewConsolidation(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	s.consolidationHooks = &consolidationExecutionHooks{forceCopy: true, checkpoint: func(phase string, id uint, index int) error {
		if phase == "copied" && id == c.ID && index == 0 {
			return errors.New("second item failed")
		}
		return nil
	}}
	result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	state := waitConsolidation(t, s, result.ID)
	if state.Status != "failed" || state.Completed != 1 {
		t.Fatalf("%+v", state)
	}
	var got models.Video
	database.DB.First(&got, a.ID)
	if got.Path == a.Path {
		t.Fatal("rolled back committed item")
	}
	if string(mustReadBytes(t, c.Path)) != "cc" || string(mustReadBytes(t, b.Path)) != "bb" {
		t.Fatal("partial changed other source")
	}
	if _, err := s.ReviewConsolidation(context.Background(), result.ID); err == nil {
		t.Fatal("partial entered cleanup")
	}
}
