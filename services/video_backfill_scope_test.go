package services

import (
	"context"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 三个真实任务入口共用夹具，只有外部媒体解码器替换为 stub。
func runScopedBackfill(t *testing.T, kind string, onCurrent func(uint)) (total, succeeded, skipped, failed int) {
	t.Helper()
	ctx := context.Background()
	switch kind {
	case "technical":
		probe := newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
			return []byte(multiStreamFFProbeFixture), "", nil
		})
		svc := NewTechnicalBackfillService(probe)
		defer svc.StopAndWait()
		svc.SetEventEmitter(func(s TechnicalBackfillStatus) {
			if onCurrent != nil {
				onCurrent(s.CurrentVideoID)
			}
		})
		if _, err := svc.Start(ctx); err != nil {
			t.Fatal(err)
		}
		s := waitForBackfill(t, svc)
		return s.Total, s.Succeeded, s.Skipped, s.Failed
	case "frame":
		svc := newTestFrameHashService(t, stubFrameHashRunner(ascendingGrayscaleFrame()))
		defer svc.StopAndWait()
		svc.SetEventEmitter(func(s FrameHashStatus) {
			if onCurrent != nil {
				onCurrent(s.CurrentVideoID)
			}
		})
		if _, err := svc.Start(ctx); err != nil {
			t.Fatal(err)
		}
		s := waitForFrameHashStatus(t, svc, func(s FrameHashStatus) bool { return !s.Running })
		return s.Total, s.Succeeded, s.Skipped, s.Failed
	default:
		svc := NewPerceptualHashService()
		svc.runner = &fakePerceptualFrameRunner{frames: [][]byte{gradientPerceptualFrame(false)}}
		defer svc.StopAndWait()
		svc.SetEventEmitter(func(s PerceptualHashStatus) {
			if onCurrent != nil {
				onCurrent(s.CurrentVideoID)
			}
		})
		if _, err := svc.Start(ctx); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			s := svc.Status()
			if !s.Running {
				return s.Total, s.Succeeded, s.Skipped, s.Failed
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("perceptual backfill timed out")
	}
	return
}

func seedScopedBackfillVideo(t *testing.T, path string, stale bool) models.Video {
	t.Helper()
	mustCreateFile(t, path)
	v := models.Video{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path), Duration: 120, IsStale: stale}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVideoBackfillsFilterScopeBeforeDiscovery(t *testing.T) {
	for _, kind := range []string{"technical", "perceptual", "frame"} {
		t.Run(kind, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			root := t.TempDir()
			excluded := filepath.Join(root, "blocked_%")
			if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", excluded).Error; err != nil {
				t.Fatal(err)
			}
			seedScopedBackfillVideo(t, filepath.Join(excluded, "child", "hidden.mp4"), false)
			seedScopedBackfillVideo(t, filepath.Join(root, "stale.mp4"), true)
			seedScopedBackfillVideo(t, filepath.Join(t.TempDir(), "outside.mp4"), false)
			// 同名前缀与 SQL 通配字符不得误伤相邻目录。
			seedScopedBackfillVideo(t, filepath.Join(root, "blocked_%_sibling", "visible.mp4"), false)
			seedScopedBackfillVideo(t, filepath.Join(root, "blocked_AB", "visible.mp4"), false)
			total, success, skipped, failed := runScopedBackfill(t, kind, nil)
			if total != 2 || success != 2 || skipped != 0 || failed != 0 {
				t.Fatalf("total=%d success=%d skipped=%d failed=%d", total, success, skipped, failed)
			}
		})
	}
}

func TestVideoBackfillsRecheckScopeBeforeProcessing(t *testing.T) {
	for _, kind := range []string{"technical", "perceptual", "frame"} {
		for _, change := range []string{"blacklist", "stale", "deleted"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				setupVideoServiceTestDB(t)
				v := seedScopedBackfillVideo(t, filepath.Join(t.TempDir(), "queued.mp4"), false)
				changed := false
				var updateErr error
				total, success, skipped, failed := runScopedBackfill(t, kind, func(id uint) {
					if id != v.ID || changed {
						return
					}
					changed = true
					switch change {
					case "blacklist":
						updateErr = database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", v.Directory).Error
					case "stale":
						updateErr = database.DB.Model(&v).Update("is_stale", true).Error
					case "deleted":
						updateErr = database.DB.Delete(&v).Error
					}
				})
				if updateErr != nil {
					t.Fatal(updateErr)
				}
				if !changed || total != 1 || success != 0 || skipped != 1 || failed != 0 {
					t.Fatalf("changed=%v total=%d success=%d skipped=%d failed=%d", changed, total, success, skipped, failed)
				}
			})
		}
	}
}
