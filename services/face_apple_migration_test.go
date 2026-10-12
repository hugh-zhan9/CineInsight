package services

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

type migrationFaceWorker struct {
	detect func(context.Context, string, string) ([]DetectedFace, error)
}

func (w migrationFaceWorker) Detect(c context.Context, id, path string) ([]DetectedFace, error) {
	return w.detect(c, id, path)
}
func (w migrationFaceWorker) Close() error { return nil }
func TestFaceAppleMigrationPreservesDecisionsAndIsAtomic(t *testing.T) {
	for _, scenario := range []string{"success", "missing", "ambiguous", "cancel", "concurrent_review", "ignored"} {
		t.Run(scenario, func(t *testing.T) {
			setupFaceTestDB(t)
			person := seedFaceTestPerson(t, "person", false)
			media := seedFaceTestImage(t, "source.jpg")
			cluster := seedFaceCluster(t, models.FaceClusterStatusNamed, &person.ID, nil)
			obs := []models.FaceObservation{seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, media.ID, "one", models.FaceAppendStatusConfirmed, .9), seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, media.ID, "two", models.FaceAppendStatusPending, .8)}
			if scenario == "ignored" {
				when := time.Now().Add(time.Hour)
				if err := database.DB.Model(&cluster).Updates(map[string]any{"status": models.FaceClusterStatusIgnored, "ignored_at": when}).Error; err != nil {
					t.Fatal(err)
				}
			}
			old := make([]byte, 2048)
			old[1] = 1
			service := NewFaceAnalysisService(t.TempDir(), nil, nil)
			for i := range obs {
				obs[i].CropPath = itoa(obs[i].ID) + ".jpg"
				writeFaceTestImage(t, filepath.Join(service.facesDir, obs[i].CropPath))
				if err := database.DB.Model(&obs[i]).Updates(map[string]any{"embedding": old, "crop_path": obs[i].CropPath}).Error; err != nil {
					t.Fatal(err)
				}
			}
			for i := range obs {
				if err := database.DB.First(&obs[i], obs[i].ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := database.DB.Model(&cluster).Update("centroid", old).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.DB.Create(&models.ImagePerson{ImageID: media.ID, PersonID: person.ID}).Error; err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			worker := migrationFaceWorker{detect: func(context.Context, string, string) ([]DetectedFace, error) {
				calls++
				faces := []DetectedFace{faceAt(.1, .1, .9, faceUnitVector(0))}
				if calls == 2 {
					switch scenario {
					case "missing":
						return nil, errors.New("crop missing")
					case "ambiguous":
						return append(faces, faces...), nil
					case "cancel":
						cancel()
						return nil, ctx.Err()
					case "concurrent_review":
						if err := database.DB.Model(&models.FaceObservation{}).Where("id = ?", obs[1].ID).Updates(map[string]any{"cluster_id": nil, "append_status": models.FaceAppendStatusDismissed}).Error; err != nil {
							t.Fatal(err)
						}
					}
				}
				return faces, nil
			}}
			pending, err := service.migrateAppleFaces(ctx, worker)
			if scenario == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var after models.FaceCluster
			if err := database.DB.First(&after, cluster.ID).Error; err != nil {
				t.Fatal(err)
			}
			if after.PersonID == nil || *after.PersonID != person.ID || (after.Status != models.FaceClusterStatusNamed && scenario != "ignored") {
				t.Fatal("lost named decision", after)
			}
			for i, before := range obs {
				var current models.FaceObservation
				if err := database.DB.First(&current, before.ID).Error; err != nil {
					t.Fatal(err)
				}
				if scenario == "success" || scenario == "ignored" {
					if !current.UpdatedAt.Equal(before.UpdatedAt) {
						t.Fatal("migration changed observation time")
					}
					if _, err := decodeFaceEmbedding(current.Embedding); err != nil {
						t.Fatal(err)
					}
				} else if !bytes.Equal(current.Embedding, old) {
					t.Fatal("partial migration")
				}
				if scenario == "concurrent_review" && i == 1 {
					if current.ClusterID != nil || current.AppendStatus != models.FaceAppendStatusDismissed {
						t.Fatal("overwrote review")
					}
				} else if current.ClusterID == nil || *current.ClusterID != cluster.ID || current.AppendStatus != before.AppendStatus {
					t.Fatal("lost review state")
				}
			}
			if countFaceRows(t, &models.ImagePerson{}, "person_id = ?", person.ID) != 1 {
				t.Fatal("lost confirmed relation")
			}
			if scenario == "success" || scenario == "ignored" {
				if pending != 0 {
					t.Fatal(pending)
				}
				calls = 0
				if _, err := service.migrateAppleFaces(context.Background(), worker); err != nil || calls != 0 {
					t.Fatal("not resumable/idempotent", err, calls)
				}
			} else if scenario != "cancel" && pending != 2 {
				t.Fatal(pending)
			}
		})
	}
}
func TestFaceAppleVectorVersionAndThreshold(t *testing.T) {
	if _, err := decodeFaceEmbedding(make([]byte, 2048)); err == nil {
		t.Fatal("accepted legacy vector")
	}
	current := encodeFaceEmbedding(faceUnitVector(0))
	current[0] ^= 1
	if _, err := decodeFaceEmbedding(current); err == nil {
		t.Fatal("accepted changed version")
	}
	clusters := []faceClusterCentroid{{ClusterID: 1, Vector: faceUnitVector(0), Count: 1}}
	if c, _ := bestFaceCluster(faceMixedVector(0, 1, .8, .6), clusters, faceClusterMergeThreshold); c != nil {
		t.Fatal("old loose threshold still accepted")
	}
	if c, _ := bestFaceCluster(faceMixedVector(0, 1, .96, .28), clusters, faceClusterMergeThreshold); c == nil {
		t.Fatal("close face rejected")
	}
}

func TestFaceAppleMigrationWaitsForMediaSlotAndCancels(t *testing.T) {
	s := NewFaceAnalysisService(t.TempDir(), nil, nil)
	s.slot = NewMediaWorkSlot()
	if err := s.slot.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.slot.Release()
	called := false
	w := migrationFaceWorker{detect: func(context.Context, string, string) ([]DetectedFace, error) { called = true; return nil, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.detectMigrationFace(ctx, w, "test", "unused"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if called {
		t.Fatal("migration bypassed occupied media slot")
	}
}
func TestFaceAppleMigrationRespectsIdleGate(t *testing.T) {
	s := NewFaceAnalysisService(t.TempDir(), nil, nil)
	gate := busyGate()
	s.pauseHook = gate.PauseHook(string(BackgroundTaskFace))
	called := false
	w := migrationFaceWorker{detect: func(context.Context, string, string) ([]DetectedFace, error) { called = true; return nil, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.detectMigrationFace(ctx, w, "test", "unused"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if called {
		t.Fatal("migration bypassed idle gate")
	}
}
