package services

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestFileMigrationConsolidationNoReplaceIsAtomic(t *testing.T) {
	for _, existing := range []string{"file", "symlink", "directory"} {
		t.Run(existing, func(t *testing.T) {
			root := t.TempDir()
			source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
			if err := os.WriteFile(source, []byte("source"), 0644); err != nil {
				t.Fatal(err)
			}
			switch existing {
			case "file":
				if err := os.WriteFile(target, []byte("USER"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(root, "missing"), target); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(target, 0755); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := publishConsolidationNoReplace(source, target); err == nil {
				t.Fatal("overwrote existing destination")
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("target identity changed", err)
			}
			if string(mustReadBytes(t, source)) != "source" {
				t.Fatal("source lost")
			}
		})
	}
	root := t.TempDir()
	target := filepath.Join(root, "winner")
	outcomes := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		source := filepath.Join(root, fmt.Sprintf("source-%d", i))
		if err := os.WriteFile(source, []byte(fmt.Sprint(i)), 0644); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() { defer wg.Done(); outcomes <- publishConsolidationNoReplace(source, target) }()
	}
	wg.Wait()
	close(outcomes)
	winners := 0
	for err := range outcomes {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("exclusive publication had %d winners", winners)
	}
}

func TestCleanupConsolidationDistinctStartsHaveOneActiveSlot(t *testing.T) {
	s, p, _, _, root := prepareConsolidationRun(t, "near")
	second := &CleanupService{status: s.status, runID: s.runID}
	second.SetConsolidationVideoService(&VideoService{})
	if err := second.ConfigureConsolidation(filepath.Join(root, "other-data"), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.StopConsolidationsAndWait() })
	request := CleanupConsolidationRequest{Destination: p.Destination, Groups: p.Groups, Protections: s.consolidationPreview.Protections}
	otherPreview, err := second.PreviewConsolidation(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	hook := &consolidationExecutionHooks{checkpoint: func(phase string, _ uint, _ int) error {
		if phase == "planned" {
			entered <- struct{}{}
			<-release
		}
		return nil
	}}
	s.consolidationHooks = hook
	second.consolidationHooks = hook
	type outcome struct {
		s      *CleanupService
		status *CleanupConsolidationStatus
		err    error
	}
	results := make(chan outcome, 2)
	barrier := make(chan struct{})
	go func() {
		<-barrier
		r, e := s.StartConsolidation(context.Background(), p.PreviewID, false)
		results <- outcome{s, r, e}
	}()
	go func() {
		<-barrier
		r, e := second.StartConsolidation(context.Background(), otherPreview.PreviewID, false)
		results <- outcome{second, r, e}
	}()
	close(barrier)
	a, b := <-results, <-results
	winner, loser := a, b
	if a.err != nil {
		winner, loser = b, a
	}
	if winner.err != nil || !errors.Is(loser.err, ErrConsolidationBusy) {
		close(release)
		t.Fatalf("winner=%v loser=%v", winner.err, loser.err)
	}
	<-entered
	var count int64
	database.DB.Model(&models.CleanupConsolidationTask{}).Where("active_slot IS NOT NULL").Count(&count)
	if count != 1 {
		close(release)
		t.Fatal("active slot count", count)
	}
	close(release)
	state := waitConsolidation(t, winner.s, winner.status.ID)
	if state.Status != "completed" {
		t.Fatalf("%+v", state)
	}
}

func TestFileMigrationConsolidationPartialFamilyRecovery(t *testing.T) {
	for _, phase := range []string{"published", "source_staged", "before_commit"} {
		t.Run(phase, func(t *testing.T) {
			s, r, a, _, root := consolidationTestFixture(t, "near")
			subtitle := strings.TrimSuffix(a.Path, ".mp4") + ".srt"
			if err := os.WriteFile(subtitle, []byte(writerTestSRT), 0644); err != nil {
				t.Fatal(err)
			}
			if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
			p, err := s.PreviewConsolidation(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			s.consolidationHooks = &consolidationExecutionHooks{checkpoint: func(point string, _ uint, index int) error {
				if point == phase && (index == 1 || phase == "before_commit") {
					return ErrConsolidationConflict
				}
				return nil
			}}
			result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
			if err != nil {
				t.Fatal(err)
			}
			_ = waitConsolidation(t, s, result.ID)
			if err := s.RecoverConsolidations(context.Background()); err != nil {
				t.Fatal(err)
			}
			if string(mustReadBytes(t, a.Path)) != "aaaa" || string(mustReadBytes(t, subtitle)) != writerTestSRT {
				t.Fatal("family not restored")
			}
			for _, file := range p.Items[0].Files {
				if _, err := os.Stat(file.Destination); !os.IsNotExist(err) {
					t.Fatal("family output remains", file.Destination)
				}
			}
		})
	}
}

func TestFileMigrationConsolidationCommitRechecksPublishedAndStagedFiles(t *testing.T) {
	for _, point := range []string{"source", "target"} {
		t.Run(point, func(t *testing.T) {
			s, p, a, _, _ := prepareConsolidationRun(t, "near")
			s.consolidationHooks = &consolidationExecutionHooks{forceCopy: true, checkpoint: func(phase string, _ uint, _ int) error {
				if phase != "before_commit" {
					return nil
				}
				path := a.Path
				if point == "target" {
					path = p.Items[0].DestinationPath
				}
				return os.WriteFile(path, []byte("USER changed"), 0644)
			}}
			result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
			if err != nil {
				t.Fatal(err)
			}
			state := waitConsolidation(t, s, result.ID)
			if state.Status != "failed" || state.Completed != 0 {
				t.Fatalf("%+v", state)
			}
			var video models.Video
			if err := database.DB.First(&video, a.ID).Error; err != nil || video.Path != a.Path {
				t.Fatal("DB switched to unverified target", err)
			}
			path := a.Path
			if point == "target" {
				path = p.Items[0].DestinationPath
			}
			if string(mustReadBytes(t, path)) != "USER changed" {
				t.Fatal("external change removed")
			}
		})
	}
}

func TestFileMigrationConsolidationRecordsActualTargetMetadata(t *testing.T) {
	for _, scenario := range []string{"complete", "recover", "target-changed"} {
		t.Run(scenario, func(t *testing.T) {
			s, r, a, b, root := consolidationTestFixture(t, "near")
			if err := os.Chmod(a.Path, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(a.Path, time.Unix(1700000000, 123456789), time.Unix(1700000000, 123456789)); err != nil {
				t.Fatal(err)
			}
			// 更新分析夹具的源版本；之后目标卷表现与源版本独立。
			s.status.Analysis.sourceFingerprints[a.ID] = cleanupFileFingerprint(4, time.Unix(1700000000, 123456789).UnixNano())
			if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
			p, err := s.PreviewConsolidation(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			s.consolidationHooks = &consolidationExecutionHooks{forceCopy: true, targetMetadata: func(path string) error {
				if err := os.Chmod(path, 0644); err != nil {
					return err
				}
				return os.Chtimes(path, time.Unix(1700000000, 0), time.Unix(1700000000, 0))
			}, checkpoint: func(phase string, _ uint, _ int) error {
				if scenario == "recover" && phase == "committed" {
					return ErrConsolidationConflict
				}
				if scenario == "target-changed" && phase == "verified" {
					paths, _ := filepath.Glob(filepath.Join(p.Destination, "*.part"))
					return os.Chmod(paths[0], 0600)
				}
				return nil
			}}
			task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
			if err != nil {
				t.Fatal(err)
			}
			state := waitConsolidation(t, s, task.ID)
			if scenario == "target-changed" {
				if state.Status != "failed" {
					t.Fatal("target snapshot change ignored", state.Status)
				}
				if _, err := os.Stat(a.Path); err != nil {
					t.Fatal("changed target lost source", err)
				}
				return
			}
			if scenario == "recover" {
				if err := s.RecoverConsolidations(context.Background()); err != nil {
					t.Fatal(err)
				}
				state, _ = s.ConsolidationStatus(task.ID)
				if state.Status != "interrupted" || state.Completed != 1 {
					t.Fatal("recovery rejected valid target metadata", state)
				}
			} else if state.Status != "completed" {
				t.Fatalf("%+v", state)
			}
			info, err := os.Stat(p.Items[0].DestinationPath)
			if err != nil || info.ModTime().UnixNano() != time.Unix(1700000000, 0).UnixNano() || info.Mode().Perm() != 0644 {
				t.Fatal("target representation not recorded", err)
			}
			var row models.CleanupConsolidationTask
			if err := database.DB.First(&row, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			_, journal, err := decodeConsolidation(context.Background(), row)
			if err != nil {
				t.Fatal(err)
			}
			actual := journal.Items[0].Files[0].TargetSnapshot
			if actual.ModTimeNS != info.ModTime().UnixNano() || actual.Mode != 0644 || actual.Size != 4 {
				t.Fatal("journal stored source metadata as target", actual)
			}
			if scenario == "complete" {
				if _, err := s.ReviewConsolidation(context.Background(), task.ID); err != nil {
					t.Fatal("review rejected quantized target", err)
				}
				if err := DismissNearDuplicateGroup([]uint{a.ID, b.ID}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.ReviewConsolidation(context.Background(), task.ID); err == nil {
					t.Fatal("dismissal using current target version ignored")
				}
			}
		})
	}
}

func TestFileMigrationConsolidationInventoryDoesNotHoldPathLock(t *testing.T) {
	s, r, a, _, root := consolidationTestFixture(t, "near")
	extra := consolidationTestVideo(t, filepath.Join(root, "extra", "extra.mp4"), "extra")
	if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
	p, err := s.PreviewConsolidation(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.consolidationHooks = &consolidationExecutionHooks{inventoryPath: func(ctx context.Context, path string) error {
		if path != filepath.Dir(extra.Path) {
			return nil
		}
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("inventory hook not reached")
	}
	readDone := make(chan error, 1)
	go func() {
		unlock, err := rLockLibraryPaths()
		if err != nil {
			readDone <- err
			return
		}
		defer unlock()
		var current models.Video
		readDone <- database.DB.First(&current, a.ID).Error
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("slow full inventory blocked library read")
	}
	close(release)
	state := waitConsolidation(t, s, task.ID)
	if state.Status != "completed" {
		t.Fatal(state.Status, state.Error)
	}
}

func TestFileMigrationConsolidationInventoryRejectsConcurrentChanges(t *testing.T) {
	for _, change := range []string{"new-db-alias", "db-path", "attachment", "target-directory", "source-alias"} {
		t.Run(change, func(t *testing.T) {
			s, r, a, _, root := consolidationTestFixture(t, "near")
			extra := consolidationTestVideo(t, filepath.Join(root, "extra", "a.mp4"), "extra")
			alias := filepath.Join(root, "alias")
			if err := os.Symlink(filepath.Dir(extra.Path), alias); err != nil {
				t.Fatal(err)
			}
			reference := models.Video{Name: "alias.mp4", Path: filepath.Join(alias, "a.mp4"), Directory: alias}
			if err := database.DB.Create(&reference).Error; err != nil {
				t.Fatal(err)
			}
			if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
			p, err := s.PreviewConsolidation(context.Background(), r)
			if err != nil || len(p.Errors) > 0 {
				t.Fatal(p, err)
			}
			mutate := func() error {
				switch change {
				case "new-db-alias":
					return database.DB.Create(&models.Video{Name: "late.mp4", Path: filepath.Dir(a.Path) + "/./" + filepath.Base(a.Path), Directory: filepath.Dir(a.Path)}).Error
				case "db-path":
					return database.DB.Model(&reference).Update("path", filepath.Dir(a.Path)+"/./"+filepath.Base(a.Path)).Error
				case "attachment":
					return os.WriteFile(strings.TrimSuffix(a.Path, ".mp4")+".srt", []byte(writerTestSRT), 0644)
				case "target-directory":
					if err := os.Rename(p.Destination, p.Destination+"-old"); err != nil {
						return err
					}
					return os.Mkdir(p.Destination, 0755)
				case "source-alias":
					if err := os.Remove(alias); err != nil {
						return err
					}
					return os.Symlink(filepath.Dir(a.Path), alias)
				}
				return nil
			}
			var once sync.Once
			var mutationErr error
			s.consolidationHooks = &consolidationExecutionHooks{checkpoint: func(phase string, _ uint, _ int) error {
				if phase == "inventory_ready" && change != "source-alias" {
					once.Do(func() { mutationErr = mutate() })
					return mutationErr
				}
				return nil
			}, inventoryPath: func(_ context.Context, path string) error {
				if change == "source-alias" && path == alias {
					once.Do(func() { mutationErr = mutate() })
					return mutationErr
				}
				return nil
			}}
			task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
			if err != nil {
				t.Fatal(err)
			}
			state := waitConsolidation(t, s, task.ID)
			if state.Status != "failed" || state.Completed != 0 {
				t.Fatal("concurrent inventory change accepted", change, state.Status, state.Error)
			}
			if (change == "new-db-alias" || change == "db-path") && !strings.Contains(state.Error, "活跃视频引用已变化") {
				t.Fatal("fixture did not reach raw pairs recheck", state.Error)
			}
			if string(mustReadBytes(t, a.Path)) != "aaaa" {
				t.Fatal("changed inventory moved source")
			}
		})
	}
}

func TestFileMigrationConsolidationRuntimeStorageCost(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("SQLite WAL cost uses isolated SQLite files")
	}
	var previousWAL, previousPayload int64
	for _, count := range []int{80, 160, 320} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			setupVideoServiceTestDB(t)
			root := t.TempDir()
			analysis := &CleanupAnalysis{}
			for i := 0; i < count; i++ {
				a := consolidationTestVideo(t, filepath.Join(root, "source", fmt.Sprintf("a%04d.mp4", i)), "a")
				b := consolidationTestVideo(t, filepath.Join(root, "target", fmt.Sprintf("b%04d.mp4", i)), "b")
				analysis.NearDuplicateGroups = append(analysis.NearDuplicateGroups, CleanupDuplicateGroup{Original: a, Candidates: []models.Video{b}})
			}
			s, r := consolidationTestService(t, analysis)
			r.Destination = filepath.Join(root, "target")
			if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
			p, err := s.PreviewConsolidation(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			rawDB, err := database.DB.DB()
			if err != nil {
				t.Fatal(err)
			}
			rawDB.SetMaxOpenConns(1)
			if err := database.DB.Exec("PRAGMA wal_autocheckpoint=0").Error; err != nil {
				t.Fatal(err)
			}
			if err := database.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error; err != nil {
				t.Fatal(err)
			}
			var files []struct {
				Seq  int
				Name string
				File string
			}
			if err := database.DB.Raw("PRAGMA database_list").Scan(&files).Error; err != nil {
				t.Fatal(err)
			}
			var dbPath string
			for _, file := range files {
				if file.Name == "main" {
					dbPath = file.File
				}
			}
			var beforePages int64
			if err := database.DB.Raw("PRAGMA page_count").Scan(&beforePages).Error; err != nil {
				t.Fatal(err)
			}
			var payload int64
			var itemUpdates, lockedPairs, allPairs, lockedFullFS, fullFS int
			const qc = "consolidation:cost-query"
			const uc = "consolidation:cost-update"
			if err := database.DB.Callback().Query().After("gorm:query").Register(qc, func(tx *gorm.DB) {
				if tx.Statement.Table == "videos" && len(tx.Statement.Selects) == 2 && tx.Statement.Selects[0] == "id" && tx.Statement.Selects[1] == "path" {
					allPairs++
					if libraryPathMutationMu.TryRLock() {
						libraryPathMutationMu.RUnlock()
					} else {
						lockedPairs++
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer database.DB.Callback().Query().Remove(qc)
			if err := database.DB.Callback().Update().Before("gorm:update").Register(uc, func(tx *gorm.DB) {
				if tx.Statement.Table == "cleanup_consolidation_task_plans" {
					t.Error("immutable plan updated")
				}
				if tx.Statement.Table == "cleanup_consolidation_task_items" {
					if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
						if journal, ok := values["journal_json"].(string); ok {
							payload += int64(len(journal))
							itemUpdates++
						}
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer database.DB.Callback().Update().Remove(uc)
			s.consolidationHooks = &consolidationExecutionHooks{inventoryPath: func(_ context.Context, _ string) error {
				fullFS++
				if libraryPathMutationMu.TryLock() {
					libraryPathMutationMu.Unlock()
				} else {
					lockedFullFS++
				}
				return nil
			}}
			started := time.Now()
			task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
			if err != nil {
				t.Fatal(err)
			}
			s.consolidationMu.Lock()
			done := s.consolidationDone
			s.consolidationMu.Unlock()
			if done != nil {
				select {
				case <-done:
				case <-time.After(120 * time.Second):
					t.Fatal("runtime cost task timed out")
				}
			}
			state, err := s.ConsolidationStatus(task.ID)
			if err != nil || state.Status != "completed" {
				t.Fatal(state, err)
			}
			info, err := os.Stat(dbPath + "-wal")
			if err != nil {
				t.Fatal(err)
			}
			var pages int64
			if err := database.DB.Raw("PRAGMA page_count").Scan(&pages).Error; err != nil {
				t.Fatal(err)
			}
			t.Logf("runtime n=%d elapsed=%s item_updates=%d journal_payload_MiB=%.3f WAL_MiB=%.3f pages_added=%d full_pairs_SQL=%d locked_pairs_SQL=%d full_FS_dirs=%d locked_full_FS=%d", count, time.Since(started), itemUpdates, float64(payload)/1048576, float64(info.Size())/1048576, pages-beforePages, allPairs, lockedPairs, fullFS, lockedFullFS)
			if lockedPairs != count || lockedFullFS != 0 || fullFS == 0 {
				t.Fatal("path lock boundary changed", lockedPairs, lockedFullFS, fullFS)
			}
			if previousWAL > 0 && (info.Size() > previousWAL*3 || payload > previousPayload*3) {
				t.Fatal("batch cost grows quadratically", previousWAL, info.Size(), previousPayload, payload)
			}
			previousWAL, previousPayload = info.Size(), payload
		})
	}
}
