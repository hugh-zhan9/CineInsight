package main

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
	"video-master/services"
)

func reviewTestApp(t *testing.T) (*App, uint) {
	t.Helper()
	db := openAppLiveDatabase(t, func() *gorm.DB { return dbtest.OpenRaw(t) })
	tag := models.Tag{Name: "审阅标签", Color: "#778899"}
	video := models.Video{Name: "synthetic.mp4", Path: "/synthetic/review.mp4"}
	if err := db.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	candidate := models.AITagCandidate{VideoID: video.ID, MatchedTagID: &tag.ID, SuggestedName: tag.Name, Confidence: "high", Status: "pending"}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{ctx: context.Background(), aiTaggingService: services.NewAITaggingService(), backgroundTasks: services.NewBackgroundTaskRegistry()}
	a.aiTaggingService.SetBackgroundTaskRegistry(a.backgroundTasks)
	t.Cleanup(a.stopAIReviewApprovals)
	return a, candidate.ID
}

func TestReviewAppMaintenanceGateAndTokenInvalidation(t *testing.T) {
	a, id := reviewTestApp(t)
	request := services.ReviewApprovalRequest{Scope: "loaded", IDs: []uint{id}}
	preview, err := a.PreviewAIReviewApproval("video", request)
	if err != nil {
		t.Fatal(err)
	}
	a.restoreMu.Lock()
	_, startErr := a.StartAIReviewApproval("video", preview.Token)
	_, previewErr := a.PreviewAIReviewApproval("video", request)
	a.restoreMu.Unlock()
	if !errors.Is(startErr, errDatabaseMaintenanceBusy) || !errors.Is(previewErr, errDatabaseMaintenanceBusy) {
		t.Fatalf("maintenance gate: %v %v", startErr, previewErr)
	}
	a.ctx = nil
	if err := a.enterDatabaseRestoreMode(false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.releaseDatabaseRestoreMode(); a.releasePlaybackMaintenance() })
	if _, err := a.StartAIReviewApproval("video", preview.Token); err == nil {
		t.Fatal("maintenance admitted approval")
	}
	a.resumeAfterDatabaseRestoreFailure()
	if _, err := a.StartAIReviewApproval("video", preview.Token); !errors.Is(err, services.ErrReviewExpired) {
		t.Fatalf("old preview survived maintenance: %v", err)
	}
	fresh, err := a.PreviewAIReviewApproval("video", request)
	if err != nil || fresh.Token == preview.Token {
		t.Fatalf("fresh %+v %v", fresh, err)
	}
	if err := a.CancelAIReviewApproval("video", fresh.Token); err != nil {
		t.Fatal(err)
	}
	var candidate models.AITagCandidate
	if err := database.DB.First(&candidate, id).Error; err != nil || candidate.Status != "pending" {
		t.Fatalf("unexpected mutation %+v %v", candidate, err)
	}
	if _, err := a.GetAIReviewApproval("unknown", "", 0, 200); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestReviewAppCommittedResultProjectsIntoTaskCenter(t *testing.T) {
	a, id := reviewTestApp(t)
	preview, err := a.PreviewAIReviewApproval("video", services.ReviewApprovalRequest{Scope: "loaded", IDs: []uint{id}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.StartAIReviewApproval("video", preview.Token); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var state services.ReviewApprovalState
	for time.Now().Before(deadline) {
		state, err = a.GetAIReviewApproval("video", preview.Token, 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		if state.State != "running" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if state.State != "completed" || state.Succeeded != 1 || len(state.Results) != 1 {
		t.Fatalf("result %+v", state)
	}
	item := a.taskCenterReviewApproval("video")
	if item.running || item.canStart || item.lastRun == nil || item.lastRun.Succeeded != 1 {
		t.Fatalf("task projection %+v", item)
	}
}

func TestReviewAppBothKindsBlockQuitWhenRunning(t *testing.T) {
	resetQuitGuardForTest(t)
	a := &App{backgroundTasks: services.NewBackgroundTaskRegistry()}
	for _, key := range []services.BackgroundTaskKey{services.BackgroundTaskAIReview, services.BackgroundTaskImageAIReview} {
		a.backgroundTasks.Begin(key)
		tasks := a.quitBlockingTasks()
		if len(tasks) != 1 || tasks[0].Key != string(key) {
			t.Fatalf("quit blocker %s: %+v", key, tasks)
		}
		if !a.beforeClose(nil) {
			t.Fatalf("running %s did not block quit", key)
		}
		a.backgroundTasks.End(key)
	}
}
