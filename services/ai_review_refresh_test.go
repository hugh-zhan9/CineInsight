package services

import (
	"context"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestReviewRefreshReturnsCurrentPendingVersionsWithinRequestedIDs(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := configuredAITag("动作", "#123456")
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	_, kept := seedVideoReview(t, "keep.mp4", tag)
	_, processed := seedVideoReview(t, "processed.mp4", tag)
	_, unrelated := seedVideoReview(t, "unrelated.mp4", tag)
	svc := NewAITaggingService()
	if _, err := svc.ApproveCandidate(processed.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&kept).UpdateColumn("reasoning", "刚刚重新分析的理由").Error; err != nil {
		t.Fatal(err)
	}
	items, err := svc.RefreshPendingReviewCandidates(context.Background(), []uint{kept.ID, kept.ID, processed.ID, 999999})
	if err != nil || len(items) != 1 || items[0].ID != kept.ID || items[0].Reasoning != "刚刚重新分析的理由" || items[0].Video == nil {
		t.Fatalf("current pending %+v %v unrelated=%d", items, err, unrelated.ID)
	}
	for _, ids := range [][]uint{{0}, make([]uint, 201)} {
		if _, err := svc.RefreshPendingReviewCandidates(context.Background(), ids); err == nil {
			t.Fatal("unbounded or invalid IDs accepted")
		}
	}
	empty, err := svc.RefreshPendingReviewCandidates(context.Background(), nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty %+v %v", empty, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.RefreshPendingReviewCandidates(ctx, []uint{kept.ID}); err == nil {
		t.Fatal("cancelled refresh succeeded")
	}
}

func TestReviewRefreshImageKeepsNewCandidateAfterOldBatchTarget(t *testing.T) {
	svc := newImageAITaggingReviewTestService(t)
	tag := configuredAITag("海边", "#123456")
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	image := models.Image{Name: "refresh.jpg", Path: "/review/refresh.jpg"}
	if err := database.DB.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	old := seedImageAITagCandidate(t, image.ID, tag, "high")
	if _, err := svc.ApproveImageAITagCandidate(old.ID); err != nil {
		t.Fatal(err)
	}
	fresh := seedImageAITagCandidate(t, image.ID, tag, "high")
	items, err := svc.RefreshPendingReviewCandidates(context.Background(), []uint{old.ID, fresh.ID})
	if err != nil || len(items) != 1 || items[0].ID != fresh.ID || items[0].Image == nil {
		t.Fatalf("fresh pending %+v %v", items, err)
	}
}
