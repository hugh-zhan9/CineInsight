package services

import (
	"context"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestRecommendationFeedbackSnoozeDownweightRevokeAndMerge(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.AutoMigrate(&models.RecommendationFeedback{}); err != nil {
		t.Fatal(err)
	}
	tags := []models.Tag{{Name: "one"}, {Name: "two"}}
	for i := range tags {
		if err := database.DB.Create(&tags[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	videos := []models.Video{{Path: "/a.mp4", Name: "a", Tags: tags}, {Path: "/b.mp4", Name: "b"}}
	for i := range videos {
		if err := database.DB.Create(&videos[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &VideoService{}
	ctx := context.Background()
	first, err := svc.SetRecommendationFeedback(ctx, RecommendationFeedbackRequest{TargetKind: "tag", TargetID: tags[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.SetRecommendationFeedback(ctx, RecommendationFeedbackRequest{TargetKind: "tag", TargetID: tags[1].ID}); err != nil {
		t.Fatal(err)
	}
	pool, err := svc.collectRandomCandidatesFrom(database.DB, RandomPlayRequest{})
	if err != nil {
		t.Fatal(err)
	}
	weights, _ := randomSelectionWeights(pool.rows, pool.playWeight, pool.halfLifeDays, time.Now())
	for i, row := range pool.rows {
		if row.ID == videos[0].ID && weights[i] != .5 {
			t.Fatalf("multiple tags compounded: %v", weights)
		}
	}
	snooze, err := svc.SetRecommendationFeedback(ctx, RecommendationFeedbackRequest{TargetKind: "work_video", TargetID: videos[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	pool, err = svc.collectRandomCandidatesFrom(database.DB, RandomPlayRequest{})
	if err != nil || len(pool.rows) != 1 || pool.rows[0].ID != videos[1].ID {
		t.Fatalf("snooze %+v %v", pool, err)
	}
	if _, err := svc.SetRecommendationFeedback(ctx, RecommendationFeedbackRequest{TargetKind: "work_video", TargetID: videos[0].ID}); err == nil {
		t.Fatal("duplicate create extended snooze")
	}
	newer, err := svc.SetRecommendationFeedback(ctx, RecommendationFeedbackRequest{TargetKind: "work_video", TargetID: videos[0].ID, ID: snooze.ID, ExpectedRevision: snooze.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetRecommendationFeedback(ctx, RecommendationFeedbackRequest{TargetKind: "work_video", TargetID: videos[0].ID, ID: snooze.ID, ExpectedRevision: snooze.Revision}); err == nil {
		t.Fatal("stale revision renewed snooze")
	}
	if err = svc.RevokeRecommendationFeedback(ctx, snooze.ID, snooze.Revision); err == nil {
		t.Fatal("stale revoke accepted")
	}
	if err = svc.RevokeRecommendationFeedback(ctx, newer.ID, newer.Revision); err != nil {
		t.Fatal(err)
	}
	if err = svc.RevokeRecommendationFeedback(ctx, newer.ID, newer.Revision); err == nil {
		t.Fatal("double revoke accepted")
	}
	if _, err = (&TagService{}).MergeTags([]uint{tags[0].ID}, tags[1].ID); err != nil {
		t.Fatal(err)
	}
	items, err := svc.ListRecommendationFeedback(ctx)
	if err != nil || len(items) != 1 || items[0].TargetID != tags[1].ID {
		t.Fatalf("merge %+v %v", items, err)
	}
	if err = svc.RevokeRecommendationFeedback(ctx, first.ID, first.Revision); err == nil {
		t.Fatal("merged old feedback remained")
	}
	until := time.Now().Add(-time.Second)
	if err = database.DB.Create(&models.RecommendationFeedback{TargetKind: "work_video", TargetID: videos[0].ID, Effect: "snooze", ExpiresAt: &until, Revision: 1}).Error; err != nil {
		t.Fatal(err)
	}
	pool, err = svc.collectRandomCandidatesFrom(database.DB, RandomPlayRequest{})
	if err != nil || len(pool.rows) != 2 {
		t.Fatalf("expired snooze %+v %v", pool, err)
	}
}

func TestRecommendationFeedbackWorkIdentityAndCompareAndSwap(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.AutoMigrate(&models.RecommendationFeedback{}); err != nil {
		t.Fatal(err)
	}
	svc := &VideoService{}
	ctx := context.Background()
	videos := []models.Video{{Name: "a", Path: "/scope/a.mp4", Directory: "/scope"}, {Name: "b", Path: "/scope/b.mp4", Directory: "/scope"}, {Name: "outside", Path: "/outside/c.mp4", Directory: "/outside"}}
	for i := range videos {
		if err := database.DB.Create(&videos[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: "/scope"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetRecommendationFeedback(ctx, RecommendationFeedbackRequest{TargetKind: "work_video", TargetID: videos[2].ID}); err == nil {
		t.Fatal("outside scope preference accepted")
	}
	single, err := svc.SetRecommendationFeedback(ctx, RecommendationFeedbackRequest{TargetKind: "work_video", TargetID: videos[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	group := models.VideoVersionGroup{Title: "group", Revision: 1}
	if err := database.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := database.DB.Create(&models.VideoVersionMember{GroupID: group.ID, VideoID: videos[i].ID, Position: i + 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	pool, err := svc.collectRandomCandidatesFrom(database.DB, RandomPlayRequest{})
	if err != nil || len(pool.rows) != 0 {
		t.Fatalf("single feedback did not expand %+v %v", pool, err)
	}
	expired := time.Now().Add(-time.Second)
	database.DB.Model(single).Update("expires_at", expired)
	renewed, err := svc.SetRecommendationFeedback(ctx, RecommendationFeedbackRequest{TargetKind: "work_video", TargetID: videos[0].ID, ID: single.ID, ExpectedRevision: single.Revision})
	if err != nil || renewed.TargetKind != "work_video" {
		t.Fatalf("renew joined single %+v %v", renewed, err)
	}
	if err := svc.RevokeRecommendationFeedback(ctx, renewed.ID, renewed.Revision); err != nil {
		t.Fatal(err)
	}
	replacement, err := svc.SetRecommendationFeedback(ctx, RecommendationFeedbackRequest{TargetKind: "work_video", TargetID: videos[0].ID})
	if err != nil || replacement.TargetKind != "work_group" {
		t.Fatalf("group preference %+v %v", replacement, err)
	}
	if _, err := svc.SetRecommendationFeedback(ctx, RecommendationFeedbackRequest{TargetKind: "work_video", TargetID: videos[0].ID, ID: single.ID, ExpectedRevision: single.Revision}); err == nil {
		t.Fatal("stale deleted row overwrote replacement")
	}
	if err := database.DB.Delete(&group).Error; err != nil {
		t.Fatal(err)
	}
	pool, err = svc.collectRandomCandidatesFrom(database.DB, RandomPlayRequest{})
	if err != nil || len(pool.rows) != 2 {
		t.Fatalf("dissolved group preference leaked %+v %v", pool, err)
	}
}
