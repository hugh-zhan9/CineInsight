package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestCleanupConsolidationReviewFreshDataAndExternalProtection(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	a := consolidationTestVideo(t, filepath.Join(root, "source", "a.mp4"), "aa")
	b := consolidationTestVideo(t, filepath.Join(root, "target", "b.mp4"), "bb")
	c := consolidationTestVideo(t, filepath.Join(root, "other", "c.mp4"), "cc")
	d := consolidationTestVideo(t, filepath.Join(root, "other", "d.mp4"), "dd")
	analysis := &CleanupAnalysis{NearDuplicateGroups: []CleanupDuplicateGroup{{Original: a, Candidates: []models.Video{b}}, {Original: c, Candidates: []models.Video{b}}, {Original: d, Candidates: []models.Video{c}}}}
	s, r := consolidationTestService(t, analysis)
	r.Destination = filepath.Dir(b.Path)
	r.Groups = []CleanupConsolidationGroup{{Kind: "near", MemberIDs: []uint{a.ID, b.ID}, KeeperID: a.ID, SelectedIDs: []uint{b.ID}}}
	for i := range r.Protections {
		if consolidationContains(r.Protections[i].MemberIDs, d.ID) {
			r.Protections[i].Skipped = true
		}
		if r.Protections[i].KeeperID == c.ID {
			r.Protections[i].KeeperID = b.ID
			r.Protections[i].KeeperPinned = true
		}
	}
	if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
	p, err := s.PreviewConsolidation(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	state := waitConsolidation(t, s, result.ID)
	if state.Status != "completed" {
		t.Fatalf("%+v", state)
	}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", a.ID).Updates(map[string]interface{}{"display_title": "new title", "is_favorite": true}).Error; err != nil {
		t.Fatal(err)
	}
	// 新实例完全没有内存分析，回读必须依赖持久快照恢复全部外部保护。
	other := &CleanupService{}
	review, err := other.ReviewConsolidation(context.Background(), result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Groups) != 1 || review.Groups[0].KeeperID != a.ID || len(review.Groups[0].SelectedIDs) != 0 {
		t.Fatalf("%+v", review)
	}
	if !consolidationContains(review.LockedIDs, b.ID) || !consolidationContains(review.LockedIDs, c.ID) || !consolidationContains(review.LockedIDs, d.ID) || consolidationContains(review.LockedIDs, a.ID) {
		t.Fatalf("external/static locks wrong: %v", review.LockedIDs)
	}
	if review.Analysis.NearDuplicateGroups[0].Original.Path != p.Items[0].DestinationPath || review.Analysis.NearDuplicateGroups[0].Original.DisplayTitle != "new title" || !review.Analysis.Curation[a.ID].Favorite {
		t.Fatal("review returned stale data")
	}
	if err := os.WriteFile(c.Path, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := other.ReviewConsolidation(context.Background(), result.ID); err == nil {
		t.Fatal("changed external protection source accepted")
	}
}

func TestCleanupConsolidationReviewRebuildsNearAndClipFingerprints(t *testing.T) {
	for _, kind := range []string{"near", "clip"} {
		t.Run(kind, func(t *testing.T) {
			s, p, a, b, _ := prepareConsolidationRun(t, kind)
			result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
			if err != nil {
				t.Fatal(err)
			}
			state := waitConsolidation(t, s, result.ID)
			if state.Status != "completed" {
				t.Fatalf("%+v", state)
			}
			other := &CleanupService{}
			review, err := other.ReviewConsolidation(context.Background(), result.ID)
			if err != nil || review.Groups[0].KeeperID != a.ID {
				t.Fatal(review, err)
			}
			if kind == "near" {
				err = DismissNearDuplicateGroup([]uint{a.ID, b.ID})
			} else {
				full, e1 := snapshotMigrationSource(p.Items[0].DestinationPath)
				clip, e2 := snapshotMigrationSource(b.Path)
				if e1 != nil || e2 != nil {
					t.Fatal(e1, e2)
				}
				err = database.DB.Create(&models.ClipDismissal{VideoFullID: a.ID, VideoClipID: b.ID, FullSourceSize: full.Size, FullSourceModTimeNS: full.ModTimeNS, ClipSourceSize: clip.Size, ClipSourceModTimeNS: clip.ModTimeNS}).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := other.ReviewConsolidation(context.Background(), result.ID); err == nil {
				t.Fatal("new dismissal ignored after JSON round trip")
			}
		})
	}
}

func TestCleanupConsolidationReviewRejectsChangedMovedFile(t *testing.T) {
	s, p, _, _, _ := prepareConsolidationRun(t, "near")
	result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = waitConsolidation(t, s, result.ID)
	if err := os.WriteFile(p.Items[0].DestinationPath, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewConsolidation(context.Background(), result.ID); err == nil {
		t.Fatal("changed moved file accepted")
	}
}

func TestCleanupConsolidationReviewUsesVersionsWithoutRehashing(t *testing.T) {
	s, p, _, _, _ := prepareConsolidationRun(t, "near")
	task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = waitConsolidation(t, s, task.ID)
	var row models.CleanupConsolidationTask
	if err := database.DB.First(&row, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	_, journal, err := decodeConsolidation(context.Background(), row)
	if err != nil {
		t.Fatal(err)
	}
	// 常规审阅按源版本核对，不读取并比对恢复专用的大文件内容摘要。
	journal.Items[0].Files[0].SHA256 = "recovery-only-digest"
	raw, err := json.Marshal(cleanupConsolidationStoredItem{SchemaVersion: 1, Item: journal.Items[0]})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.CleanupConsolidationTaskItem{}).Where("task_id = ? AND position = ?", row.ID, 0).Update("journal_json", string(raw)).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := s.ReviewConsolidation(context.Background(), task.ID); err != nil {
			t.Fatal("review scanned full content", err)
		}
	}
}

func TestCleanupConsolidationReviewRechecksSameSourceDecision(t *testing.T) {
	s, p, a, b, _ := prepareConsolidationRun(t, "same-source")
	relation := models.VideoSameSourceRelation{VideoAID: a.ID, VideoBID: b.ID, Status: models.VideoSameSourceStatusDetected}
	if err := database.DB.Create(&relation).Error; err != nil {
		t.Fatal(err)
	}
	task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	state := waitConsolidation(t, s, task.ID)
	if state.Status != "completed" {
		t.Fatalf("%+v", state)
	}
	review, err := s.ReviewConsolidation(context.Background(), task.ID)
	if err != nil || review.Groups[0].KeeperID != a.ID {
		t.Fatal("confirmed keeper changed", err)
	}
	now := time.Now()
	if err := database.DB.Model(&relation).Update("reviewed_at", &now).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewConsolidation(context.Background(), task.ID); err == nil {
		t.Fatal("same-source decision changed without re-review")
	}
}

func TestCleanupConsolidationReviewLargeBatchPreservesGroupOrder(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	analysis := &CleanupAnalysis{}
	const count = 240
	for i := 0; i < count; i++ {
		a := consolidationTestVideo(t, filepath.Join(root, fmt.Sprintf("%d-a.mp4", i)), "keeper")
		b := consolidationTestVideo(t, filepath.Join(root, fmt.Sprintf("%d-b.mp4", i)), "candidate")
		switch i % 3 {
		case 0:
			analysis.NearDuplicateGroups = append(analysis.NearDuplicateGroups, CleanupDuplicateGroup{Original: a, Candidates: []models.Video{b}, Reason: fmt.Sprint(i)})
		case 1:
			analysis.ClipGroups = append(analysis.ClipGroups, CleanupClipGroup{Full: a, Clip: b, OffsetSeconds: float64(i)})
		case 2:
			row := models.VideoSameSourceRelation{VideoAID: a.ID, VideoBID: b.ID, Status: models.VideoSameSourceStatusDetected}
			if err := database.DB.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			analysis.SameSourceGroups = append(analysis.SameSourceGroups, CleanupSameSourceGroup{RelationID: row.ID, Preferred: a, Alternative: b, Reason: fmt.Sprint(i)})
		}
	}
	s, r := consolidationTestService(t, analysis)
	r.Destination = root
	p, err := s.PreviewConsolidation(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	plan := s.consolidationPreview
	journal := newConsolidationJournal(*p)
	for i := range journal.Items {
		journal.Items[i].Phase = "committed"
		for j := range journal.Items[i].Files {
			journal.Items[i].Files[j].Phase = "committed"
		}
	}
	rawPlan, _ := json.Marshal(plan)
	task := models.CleanupConsolidationTask{PreviewID: p.PreviewID, OwnerScope: "test", Status: "completed", Version: 1, Total: count, Completed: count}
	if err := createConsolidationRecords(context.Background(), database.DB, &task, string(rawPlan), journal); err != nil {
		t.Fatal(err)
	}
	before := consolidationTree(t, root)
	review, err := s.ReviewConsolidation(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Groups) != count || len(review.Analysis.NearDuplicateGroups) != 80 || len(review.Analysis.SameSourceGroups) != 80 || len(review.Analysis.ClipGroups) != 80 {
		t.Fatal("large review lost groups")
	}
	for i, group := range review.Groups {
		if group.KeeperID != p.Groups[i].KeeperID || group.Kind != p.Groups[i].Kind {
			t.Fatal("group order or keeper changed")
		}
	}
	for _, group := range review.Analysis.NearDuplicateGroups {
		if group.Reason == "" {
			t.Fatal("near reason lost")
		}
	}
	if !reflect.DeepEqual(before, consolidationTree(t, root)) {
		t.Fatal("review mutated files")
	}
}

func TestCleanupConsolidationReviewDismissalDuringQuantizedCopy(t *testing.T) {
	for _, kind := range []string{"near", "clip"} {
		t.Run(kind, func(t *testing.T) {
			s, p, a, b, _ := prepareConsolidationRun(t, kind)
			old := p.Items[0].Files[0].Source.ModTimeNS
			s.consolidationHooks = &consolidationExecutionHooks{forceCopy: true, targetMetadata: func(path string) error {
				return os.Chtimes(path, time.Unix(0, old-int64(time.Second)), time.Unix(0, old-int64(time.Second)))
			}, checkpoint: func(phase string, _ uint, _ int) error {
				if phase != "copied" {
					return nil
				}
				if kind == "near" {
					return DismissNearDuplicateGroup([]uint{a.ID, b.ID})
				}
				full, err := snapshotMigrationSource(a.Path)
				if err != nil {
					return err
				}
				clip, err := snapshotMigrationSource(b.Path)
				if err != nil {
					return err
				}
				return database.DB.Create(&models.ClipDismissal{VideoFullID: a.ID, VideoClipID: b.ID, FullSourceSize: full.Size, FullSourceModTimeNS: full.ModTimeNS, ClipSourceSize: clip.Size, ClipSourceModTimeNS: clip.ModTimeNS}).Error
			}}
			task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
			if err != nil {
				t.Fatal(err)
			}
			state := waitConsolidation(t, s, task.ID)
			if state.Status != "completed" {
				t.Fatalf("bad fixture: %s %s", state.Status, state.Error)
			}
			other := &CleanupService{}
			if _, err := other.ReviewConsolidation(context.Background(), task.ID); err == nil {
				t.Fatal("new dismissal during copy ignored after target mtime changed")
			}
		})
	}
}

func TestCleanupConsolidationReviewDismissalAcrossTwoMovedVersions(t *testing.T) {
	for _, kind := range []string{"near", "clip"} {
		t.Run(kind, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			root := t.TempDir()
			a := consolidationTestVideo(t, filepath.Join(root, "source", "a.mp4"), "aa")
			b := consolidationTestVideo(t, filepath.Join(root, "source", "b.mp4"), "bb")
			c := consolidationTestVideo(t, filepath.Join(root, "target", "c.mp4"), "cc")
			analysis := &CleanupAnalysis{NearDuplicateGroups: []CleanupDuplicateGroup{{Original: b, Candidates: []models.Video{c}}}}
			if kind == "near" {
				analysis.NearDuplicateGroups = append(analysis.NearDuplicateGroups, CleanupDuplicateGroup{Original: a, Candidates: []models.Video{b}})
			} else {
				analysis.ClipGroups = []CleanupClipGroup{{Full: a, Clip: b}}
			}
			s, r := consolidationTestService(t, analysis)
			r.Destination = filepath.Dir(c.Path)
			if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
			p, err := s.PreviewConsolidation(context.Background(), r)
			if err != nil || len(p.Errors) > 0 {
				t.Fatal(p, err)
			}
			s.consolidationHooks = &consolidationExecutionHooks{forceCopy: true, targetMetadata: func(path string) error {
				info, err := os.Stat(path)
				if err != nil {
					return err
				}
				mt := info.ModTime().Add(-time.Second)
				return os.Chtimes(path, mt, mt)
			}, checkpoint: func(phase string, id uint, _ int) error {
				if phase != "committed" || id != a.ID {
					return nil
				}
				if kind == "near" {
					return DismissNearDuplicateGroup([]uint{a.ID, b.ID})
				}
				full, err := snapshotMigrationSource(p.Items[0].DestinationPath)
				if err != nil {
					return err
				}
				clip, err := snapshotMigrationSource(b.Path)
				if err != nil {
					return err
				}
				return database.DB.Create(&models.ClipDismissal{VideoFullID: a.ID, VideoClipID: b.ID, FullSourceSize: full.Size, FullSourceModTimeNS: full.ModTimeNS, ClipSourceSize: clip.Size, ClipSourceModTimeNS: clip.ModTimeNS}).Error
			}}
			task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
			if err != nil {
				t.Fatal(err)
			}
			state := waitConsolidation(t, s, task.ID)
			if state.Status != "completed" || state.Completed != 2 {
				t.Fatal(state.Status, state.Error)
			}
			other := &CleanupService{}
			if _, err := other.ReviewConsolidation(context.Background(), task.ID); err == nil {
				t.Fatal("intermediate target/source dismissal lost after second migration")
			}
		})
	}
}
