package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestCleanupConsolidationRecoveryEveryPhase(t *testing.T) {
	for _, phase := range []string{"planned", "staging", "copied", "verified", "publishing", "published", "source_staged", "before_commit", "committed"} {
		for _, copyFile := range []bool{false, true} {
			if phase == "copied" && !copyFile {
				continue
			}
			name := phase
			if copyFile {
				name += "-copy"
			}
			t.Run(name, func(t *testing.T) {
				s, p, a, b, _ := prepareConsolidationRun(t, "near")
				s.consolidationHooks = &consolidationExecutionHooks{forceCopy: copyFile, checkpoint: func(point string, _ uint, _ int) error {
					if point == phase {
						return ErrConsolidationConflict
					}
					return nil
				}}
				result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
				if err != nil {
					t.Fatal(err)
				}
				before := waitConsolidation(t, s, result.ID)
				if before.Status != "running" || before.ActiveSlot == nil {
					t.Fatalf("crash fixture finished: %+v", before)
				}
				other := &CleanupService{}
				other.SetConsolidationVideoService(&VideoService{})
				if err := other.ConfigureConsolidation(s.consolidationDataDir, nil); err != nil {
					t.Fatal(err)
				}
				if err := other.RecoverConsolidations(context.Background()); err != nil {
					t.Fatal(err)
				}
				after, err := other.ConsolidationStatus(result.ID)
				if err != nil {
					t.Fatal(err)
				}
				if after.Status != "interrupted" || after.ActiveSlot != nil {
					t.Fatalf("%+v", after)
				}
				var got models.Video
				if err := database.DB.First(&got, a.ID).Error; err != nil {
					t.Fatal(err)
				}
				if phase == "committed" {
					if after.Completed != 1 || got.Path != p.Items[0].DestinationPath {
						t.Fatalf("committed item not reconciled: %+v", after)
					}
					if string(mustReadBytes(t, got.Path)) != "aaaa" {
						t.Fatal("target lost")
					}
					if copyFile {
						var count int64
						database.DB.Model(&models.MigrationStagedSource{}).Count(&count)
						if count != 1 {
							t.Fatal("copied source not registered")
						}
					}
				} else {
					if after.Completed != 0 || got.Path != a.Path || string(mustReadBytes(t, a.Path)) != "aaaa" {
						t.Fatalf("rollback source failed: %+v", after)
					}
					if _, err := os.Stat(p.Items[0].DestinationPath); !os.IsNotExist(err) {
						t.Fatal("uncommitted target remains", err)
					}
					artifacts, _ := filepath.Glob(filepath.Join(p.Destination, "*.part"))
					if len(artifacts) != 0 {
						t.Fatal("owned temporary remains", artifacts)
					}
				}
				if string(mustReadBytes(t, b.Path)) != "bbbb" {
					t.Fatal("recovery deleted cleanup candidate")
				}
				if _, err := other.ReviewConsolidation(context.Background(), result.ID); err == nil {
					t.Fatal("recovery automatically entered cleanup")
				}
			})
		}
	}
}

func TestCleanupConsolidationRecoveryPreservesUnknownFilesAndForeignOwner(t *testing.T) {
	for _, scenario := range []string{"foreign", "replaced-target", "equivalent-target", "modified-target", "replaced-temp", "external-source"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, a, _, _ := prepareConsolidationRun(t, "near")
			phase := "published"
			if scenario == "replaced-temp" {
				phase = "verified"
			}
			if scenario == "external-source" {
				phase = "source_staged"
			}
			s.consolidationHooks = &consolidationExecutionHooks{forceCopy: true, checkpoint: func(point string, _ uint, _ int) error {
				if point == phase {
					return ErrConsolidationConflict
				}
				return nil
			}}
			result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
			if err != nil {
				t.Fatal(err)
			}
			_ = waitConsolidation(t, s, result.ID)
			path := p.Items[0].DestinationPath
			switch scenario {
			case "foreign":
				if err := database.DB.Model(&models.CleanupConsolidationTask{}).Where("id = ?", result.ID).Update("owner_scope", "another-machine").Error; err != nil {
					t.Fatal(err)
				}
			case "replaced-target":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("USER"), 0644); err != nil {
					t.Fatal(err)
				}
			case "equivalent-target":
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path, path+"-ours"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("aaaa"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "modified-target":
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("USER"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "replaced-temp":
				paths, _ := filepath.Glob(filepath.Join(p.Destination, "*.part"))
				path = paths[0]
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("USER"), 0644); err != nil {
					t.Fatal(err)
				}
			case "external-source":
				path = a.Path
				if err := os.WriteFile(path, []byte("USER"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			other := &CleanupService{}
			other.SetConsolidationVideoService(&VideoService{})
			if err := other.ConfigureConsolidation(s.consolidationDataDir, nil); err != nil {
				t.Fatal(err)
			}
			if err := other.RecoverConsolidations(context.Background()); err == nil {
				t.Fatal("uncertain recovery did not report")
			}
			state, err := other.ConsolidationStatus(result.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "foreign" {
				if state.Status != "running" || state.ActiveSlot == nil {
					t.Fatal("foreign owner taken over")
				}
				return
			}
			if state.Status != "interrupted" || !strings.Contains(state.Error, "保留") {
				t.Fatalf("%+v", state)
			}
			wanted := "USER"
			if scenario == "equivalent-target" {
				wanted = "aaaa"
			}
			if string(mustReadBytes(t, path)) != wanted {
				t.Fatal("external file was removed or replaced")
			}
		})
	}
}

func TestCleanupConsolidationRecoveryRejectsStaleTaskCAS(t *testing.T) {
	s, p, a, _, _ := prepareConsolidationRun(t, "near")
	s.consolidationHooks = &consolidationExecutionHooks{checkpoint: func(phase string, _ uint, _ int) error {
		if phase == "verified" {
			return database.DB.Model(&models.CleanupConsolidationTask{}).Where("preview_id = ?", p.PreviewID).Update("version", 999).Error
		}
		return nil
	}}
	result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	state := waitConsolidation(t, s, result.ID)
	if state.Status != "running" || state.Version != 999 || state.ActiveSlot == nil {
		t.Fatalf("stale executor updated task: %+v", state)
	}
	if string(mustReadBytes(t, a.Path)) != "aaaa" {
		t.Fatal("stale executor moved source")
	}
	if _, err := os.Stat(p.Items[0].DestinationPath); !os.IsNotExist(err) {
		t.Fatal("stale executor published")
	}
}

func TestCleanupConsolidationRecoveryCancellationKeepsActiveSlot(t *testing.T) {
	s, p, _, _, _ := prepareConsolidationRun(t, "near")
	s.consolidationHooks = &consolidationExecutionHooks{checkpoint: func(phase string, _ uint, _ int) error {
		if phase == "published" {
			return ErrConsolidationConflict
		}
		return nil
	}}
	task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = waitConsolidation(t, s, task.ID)
	other := &CleanupService{}
	other.SetConsolidationVideoService(&VideoService{})
	registry := NewBackgroundTaskRegistry()
	other.SetBackgroundTaskRegistry(registry)
	if err := other.ConfigureConsolidation(s.consolidationDataDir, nil); err != nil {
		t.Fatal(err)
	}
	hashing := make(chan struct{})
	other.consolidationHooks = &consolidationExecutionHooks{reconcile: func(ctx context.Context) error { close(hashing); <-ctx.Done(); return ctx.Err() }}
	result := make(chan error, 1)
	go func() { result <- other.RecoverConsolidations(context.Background()) }()
	<-hashing
	if len(registry.Snapshot()) != 1 || registry.Snapshot()[0] != "cleanup_consolidation" {
		t.Fatal("recovery not registered")
	}
	if err := other.StopConsolidationsAndWait(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("recovery not canceled", err)
	}
	state, err := other.ConsolidationStatus(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "running" || state.ActiveSlot == nil {
		t.Fatal("cancel claimed completed reconciliation", state)
	}
	if len(registry.Snapshot()) != 0 {
		t.Fatal("recovery registry leaked")
	}
	other.consolidationHooks = nil
	other.ResumeConsolidations()
	if err := other.RecoverConsolidations(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _ = other.ConsolidationStatus(task.ID)
	if state.Status != "interrupted" || state.ActiveSlot != nil {
		t.Fatal("second recovery did not finish", state)
	}
}

func TestCleanupConsolidationDataDirectoryReplacementCannotStealTask(t *testing.T) {
	s, p, _, _, _ := prepareConsolidationRun(t, "near")
	s.consolidationHooks = &consolidationExecutionHooks{checkpoint: func(phase string, _ uint, _ int) error {
		if phase == "verified" {
			return ErrConsolidationConflict
		}
		return nil
	}}
	task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = waitConsolidation(t, s, task.ID)
	if err := os.Rename(s.consolidationDataDir, s.consolidationDataDir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.consolidationDataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverConsolidations(context.Background()); err == nil {
		t.Fatal("old configuration ignored replaced data directory")
	}
	other := &CleanupService{}
	other.SetConsolidationVideoService(&VideoService{})
	if err := other.ConfigureConsolidation(s.consolidationDataDir, nil); err != nil {
		t.Fatal(err)
	}
	if other.consolidationOwner == s.consolidationOwner {
		t.Fatal("replacement reused old scope")
	}
	if err := other.RecoverConsolidations(context.Background()); err == nil {
		t.Fatal("replacement directory took task")
	}
	state, _ := other.ConsolidationStatus(task.ID)
	if state.Status != "running" || state.ActiveSlot == nil {
		t.Fatal("foreign task released")
	}
}

func TestConsolidationKernelLockHelper(t *testing.T) {
	directory := os.Getenv("CINEINSIGHT_CONSOLIDATION_LOCK_TEST_DIR")
	if directory == "" {
		t.Skip("subprocess helper")
	}
	release, err := acquireConsolidationLock(directory, "same-preview")
	if !errors.Is(err, ErrConsolidationBusy) {
		if release != nil {
			release()
		}
		t.Fatalf("another process acquired active lock: %v", err)
	}
}

func TestConsolidationKernelLockRejectsSecondProcess(t *testing.T) {
	directory := t.TempDir()
	release, err := acquireConsolidationLock(directory, "same-preview")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	command := exec.Command(os.Args[0], "-test.run=^TestConsolidationKernelLockHelper$", "-test.count=1")
	command.Env = append(os.Environ(), "CINEINSIGHT_CONSOLIDATION_LOCK_TEST_DIR="+directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("kernel lock subprocess: %v\n%s", err, output)
	}
}

func TestCleanupConsolidationRecoveryRejectsIncompleteRecords(t *testing.T) {
	for _, damage := range []string{"missing-plan", "missing-item", "position", "video-id", "schema-version"} {
		t.Run(damage, func(t *testing.T) {
			s, p, a, _, _ := prepareConsolidationRun(t, "near")
			s.consolidationHooks = &consolidationExecutionHooks{checkpoint: func(phase string, _ uint, _ int) error {
				if phase == "planned" {
					return ErrConsolidationConflict
				}
				return nil
			}}
			task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
			if err != nil {
				t.Fatal(err)
			}
			_ = waitConsolidation(t, s, task.ID)
			var item models.CleanupConsolidationTaskItem
			if err := database.DB.Where("task_id = ?", task.ID).First(&item).Error; err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "missing-plan":
				err = database.DB.Where("task_id = ?", task.ID).Delete(&models.CleanupConsolidationTaskPlan{}).Error
			case "missing-item":
				err = database.DB.Delete(&item).Error
			case "position":
				err = database.DB.Model(&item).Update("position", 4).Error
			default:
				var record cleanupConsolidationStoredItem
				if err = json.Unmarshal([]byte(item.JournalJSON), &record); err != nil {
					t.Fatal(err)
				}
				if damage == "video-id" {
					record.Item.VideoID++
				} else {
					record.SchemaVersion++
				}
				raw, _ := json.Marshal(record)
				err = database.DB.Model(&item).Update("journal_json", string(raw)).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ConsolidationStatus(task.ID); err == nil {
				t.Fatal("invalid detail accepted")
			}
			if err := s.RecoverConsolidations(context.Background()); err == nil {
				t.Fatal("invalid evidence recovered")
			}
			var parent models.CleanupConsolidationTask
			if err := database.DB.First(&parent, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if parent.Status != "running" || parent.ActiveSlot == nil {
				t.Fatal("damaged task slot released")
			}
			if string(mustReadBytes(t, a.Path)) != "aaaa" {
				t.Fatal("damaged evidence moved source")
			}
		})
	}
}
