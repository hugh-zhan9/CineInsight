package services

import (
	"context"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestReviewedTagMergeRejectsChangesAndRetainsNegativeDirection(t *testing.T) {
	setupVideoServiceTestDB(t)
	tags := []models.Tag{{Name: "source"}, {Name: "target"}}
	for i := range tags {
		if err := database.DB.Create(&tags[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	video := &VideoService{}
	service := &TagService{}
	row, err := video.SetRecommendationTagPreference(ctx, RecommendationTagPreferenceRequest{TagID: tags[0].ID, Effect: "boost"})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewTagMergePreferences(ctx, []uint{tags[0].ID}, tags[1].ID)
	if err != nil || preview.Effect != "boost" {
		t.Fatalf("%+v %v", preview, err)
	}
	if _, err := service.MergeTagsWithoutFeedback(ctx, []uint{tags[0].ID}, tags[1].ID); err == nil {
		t.Fatal("old path bypassed preference review")
	}
	if _, err := video.SetRecommendationTagPreference(ctx, RecommendationTagPreferenceRequest{TagID: tags[0].ID, Effect: "downweight", ID: row.ID, ExpectedRevision: row.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.MergeTagsFromPreferencePreview(ctx, preview.Token); err == nil {
		t.Fatal("obsolete preference review accepted")
	}
	preview, err = service.PreviewTagMergePreferences(ctx, []uint{tags[0].ID}, tags[1].ID)
	if err != nil || preview.Effect != "downweight" {
		t.Fatalf("%+v %v", preview, err)
	}
	if _, err := service.MergeTagsFromPreferencePreview(ctx, preview.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.MergeTagsFromPreferencePreview(ctx, preview.Token); err != nil {
		t.Fatalf("duplicate confirmation: %v", err)
	}
	rows, err := video.ListRecommendationFeedback(ctx)
	if err != nil || len(rows) != 1 || rows[0].TargetID != tags[1].ID || rows[0].Effect != "downweight" {
		t.Fatalf("%+v %v", rows, err)
	}
}

func TestReviewedTagMergeRejectsMaintenanceGeneration(t *testing.T) {
	setupVideoServiceTestDB(t)
	tags := []models.Tag{{Name: "source"}, {Name: "target"}}
	for i := range tags {
		if err := database.DB.Create(&tags[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	svc := &TagService{}
	if _, err := (&VideoService{}).SetRecommendationTagPreference(ctx, RecommendationTagPreferenceRequest{TagID: tags[0].ID, Effect: "boost"}); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.PreviewTagMergePreferences(ctx, []uint{tags[0].ID}, tags[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := svc.MergeTagsFromPreferencePreview(context.Background(), preview.Token); err == nil {
		t.Fatal("retired owner preview accepted")
	}
}
