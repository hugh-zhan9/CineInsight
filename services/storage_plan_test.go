package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func storageGoalTestService(t *testing.T) (*StorageReportService, string, string) {
	t.Helper()
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	a, b := filepath.Join(root, "disk-a"), filepath.Join(root, "disk-b")
	if err := os.Mkdir(a, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(b, 0700); err != nil {
		t.Fatal(err)
	}
	s := NewStorageReportService()
	s.volumeID = func(path string) (string, error) {
		if strings.HasPrefix(path, a+string(filepath.Separator)) || path == a {
			return "disk-a", nil
		}
		if strings.HasPrefix(path, b+string(filepath.Separator)) || path == b {
			return "disk-b", nil
		}
		return "other", nil
	}
	s.freeSpace = func(string) (uint64, error) { return 9 << 30, nil }
	return s, a, b
}
func storageGoalVideo(t *testing.T, dir, name string) models.Video {
	t.Helper()
	path := filepath.Join(dir, name)
	data := []byte("synthetic-media!")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	row := models.Video{Name: name, Path: path, Directory: dir, Size: int64(len(data)), Duration: 60, Width: 640, Height: 360}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}
func storageGoalStatus(t *testing.T, rows ...models.Video) *CleanupStatus {
	t.Helper()
	started := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	analysis := &CleanupAnalysis{DuplicateGroups: []CleanupDuplicateGroup{{Original: rows[0], Candidates: rows[1:]}}, NearDuplicateGroups: []CleanupDuplicateGroup{{Original: rows[0], Candidates: rows[1:]}}, sourceFingerprints: map[uint]string{}}
	for _, row := range rows {
		info, err := os.Stat(row.Path)
		if err != nil {
			t.Fatal(err)
		}
		analysis.sourceFingerprints[row.ID] = cleanupFileFingerprint(info.Size(), info.ModTime().UnixNano())
	}
	return &CleanupStatus{Completed: true, Analysis: analysis, StartedAt: &started}
}

func TestStorageGoalPlanKeepsVolumeProtectionAndNoSideEffects(t *testing.T) {
	s, a, b := storageGoalTestService(t)
	keeper := storageGoalVideo(t, a, "keeper.mp4")
	candidate := storageGoalVideo(t, a, "candidate.mp4")
	otherDisk := storageGoalVideo(t, b, "other.mp4")
	status := storageGoalStatus(t, keeper, candidate, otherDisk)
	volumes, err := s.GetStorageGoalVolumes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes.Items) != 2 {
		t.Fatal(volumes)
	}
	review := &StorageReviewSnapshot{AnalysisKey: status.StartedAt.Format(time.RFC3339Nano), LockedIDs: []uint{keeper.ID}, Candidates: []StorageReviewCandidate{{ID: candidate.ID, Kind: "near", KeeperID: keeper.ID}, {ID: candidate.ID, Kind: "exact", KeeperID: keeper.ID}, {ID: otherDisk.ID, Kind: "exact", KeeperID: keeper.ID}}}
	request := StorageGoalRequest{Epoch: volumes.Epoch, VolumeID: "disk-a", TargetBytes: 100, VideoReview: review}
	plan, err := s.GetStorageGoalPlan(context.Background(), request, status, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Count != 1 || plan.Items[0].ID != candidate.ID || plan.Items[0].ReasonKind != "exact" || !plan.Items[0].Suggested || plan.PotentialBytes != candidate.Size || plan.ShortfallBytes != 100-candidate.Size {
		t.Fatalf("bad plan: %#v", plan)
	}
	if plan.Items[0].KeeperTitle != keeper.Name || plan.AvailableBytes != 9<<30 {
		t.Fatal(plan)
	}
	var events, trash int64
	database.DB.Model(&models.PlayEvent{}).Count(&events)
	database.DB.Model(&models.VideoTrashEntry{}).Count(&trash)
	if events != 0 || trash != 0 {
		t.Fatal("read-only plan wrote activity")
	}
	for _, row := range []models.Video{keeper, candidate, otherDisk} {
		if _, err := os.Stat(row.Path); err != nil {
			t.Fatal("planning removed a file")
		}
	}
	review.LockedIDs = append(review.LockedIDs, candidate.ID)
	plan, err = s.GetStorageGoalPlan(context.Background(), request, status, nil)
	if err != nil || plan.Count != 0 || plan.ShortfallBytes != 100 {
		t.Fatalf("ignored protection: %#v %v", plan, err)
	}
	request.Epoch = "old-library"
	if _, err = s.GetStorageGoalPlan(context.Background(), request, status, nil); err == nil {
		t.Fatal("accepted another library epoch")
	}
}

func TestStorageGoalUnknownReviewChangedSourcesAndLinksAreNotCounted(t *testing.T) {
	s, a, _ := storageGoalTestService(t)
	keeper := storageGoalVideo(t, a, "keeper.mp4")
	candidate := storageGoalVideo(t, a, "candidate.mp4")
	status := storageGoalStatus(t, keeper, candidate)
	volumes, err := s.GetStorageGoalVolumes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := StorageGoalRequest{Epoch: volumes.Epoch, VolumeID: "disk-a", TargetBytes: 100}
	plan, err := s.GetStorageGoalPlan(context.Background(), request, status, nil)
	if err != nil || plan.Count != 0 {
		t.Fatalf("counted unknown review: %#v %v", plan, err)
	}
	request.VideoReview = &StorageReviewSnapshot{AnalysisKey: status.StartedAt.Format(time.RFC3339Nano), LockedIDs: []uint{keeper.ID}, Candidates: []StorageReviewCandidate{{ID: candidate.ID, Kind: "exact", KeeperID: keeper.ID}}}
	alias := filepath.Join(a, "hardlink.mp4")
	if err = os.Link(candidate.Path, alias); err != nil {
		t.Fatal(err)
	}
	plan, err = s.GetStorageGoalPlan(context.Background(), request, status, nil)
	if err != nil || plan.Count != 0 {
		t.Fatalf("counted one hardlink as freed blocks: %#v %v", plan, err)
	}
	if err = os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(candidate.Path, []byte("changed source content"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err = s.GetStorageGoalPlan(context.Background(), request, status, nil)
	if err != nil || plan.Count != 0 {
		t.Fatalf("counted stale analysis: %#v %v", plan, err)
	}
	if !strings.Contains(strings.Join(plan.Notices, " "), "分析依据") {
		t.Fatal("missing changed-source notice")
	}
}

func TestStorageGoalProtectionFollowsPhysicalAliasesAcrossMedia(t *testing.T) {
	s, a, _ := storageGoalTestService(t)
	keeper := storageGoalVideo(t, a, "keeper.mp4")
	candidate := storageGoalVideo(t, a, "candidate.mp4")
	status := storageGoalStatus(t, keeper, candidate)
	alias := filepath.Join(a, "protected.png")
	if err := os.Symlink(candidate.Path, alias); err != nil {
		t.Fatal(err)
	}
	photo := models.Image{Name: "protected.png", Path: alias, Directory: a, Format: "png"}
	if err := database.DB.Create(&photo).Error; err != nil {
		t.Fatal(err)
	}
	volumes, err := s.GetStorageGoalVolumes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := StorageGoalRequest{Epoch: volumes.Epoch, VolumeID: "disk-a", TargetBytes: 100, VideoReview: &StorageReviewSnapshot{AnalysisKey: status.StartedAt.Format(time.RFC3339Nano), LockedIDs: []uint{keeper.ID}, Candidates: []StorageReviewCandidate{{ID: candidate.ID, Kind: "exact", KeeperID: keeper.ID}}}, ImageReview: &StorageReviewSnapshot{LockedIDs: []uint{photo.ID}}}
	plan, err := s.GetStorageGoalPlan(context.Background(), request, status, nil)
	if err != nil || plan.Count != 0 {
		t.Fatalf("broke physical keeper alias: %#v %v", plan, err)
	}
}

func TestStorageGoalReadsBeyondOneBatchAndDoesNotApplyTheOld500ItemCap(t *testing.T) {
	s, a, _ := storageGoalTestService(t)
	started := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	members := make([]ImageCleanupMember, 0, 513)
	for index := 0; index < 513; index++ {
		path := filepath.Join(a, fmt.Sprintf("p-%04d.png", index))
		if err := os.WriteFile(path, []byte("synthetic image"), 0600); err != nil {
			t.Fatal(err)
		}
		row := models.Image{Name: filepath.Base(path), Path: path, Directory: a, Format: "png"}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(path)
		members = append(members, ImageCleanupMember{Image: row, FileSize: info.Size(), ModTimeNS: info.ModTime().UnixNano()})
	}
	status := &ImageCleanupStatus{Completed: true, StartedAt: &started, Analysis: &ImageCleanupAnalysis{DuplicateGroups: []ImageCleanupDuplicateGroup{{Original: members[0], Candidates: members[1:]}}}}
	volumes, err := s.GetStorageGoalVolumes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes.Items) != 1 || volumes.Items[0].Count != 513 {
		t.Fatalf("lost a batch: %#v", volumes)
	}
	review := &StorageReviewSnapshot{AnalysisKey: started.Format(time.RFC3339Nano), LockedIDs: []uint{members[0].ID}}
	for _, member := range members[1:] {
		review.Candidates = append(review.Candidates, StorageReviewCandidate{ID: member.ID, Kind: "exact", KeeperID: members[0].ID})
	}
	plan, err := s.GetStorageGoalPlan(context.Background(), StorageGoalRequest{Epoch: volumes.Epoch, VolumeID: "disk-a", TargetBytes: 100000, ImageReview: review}, nil, status)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Count != 512 || len(plan.Items) != 512 || plan.Items[len(plan.Items)-1].ID != members[512].ID {
		t.Fatalf("old display cap leaked into candidate selection: count=%d listed=%d", plan.Count, len(plan.Items))
	}
}
