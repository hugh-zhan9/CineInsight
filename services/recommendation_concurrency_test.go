package services

import (
	"context"
	"errors"
	"testing"
	"video-master/database"
	"video-master/models"
)

func competingPreferences(t *testing.T, first, second func() error) {
	t.Helper()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, run := range []func() error{first, second} {
		go func(run func() error) { <-start; results <- run() }(run)
	}
	close(start)
	success := 0
	errorsSeen := []error{<-results, <-results}
	for _, err := range errorsSeen {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrRecommendationFeedbackConflict) {
			t.Fatalf("unexpected competing write error: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("expected exactly one accepted intent, got %d", success)
	}
}

func TestRecommendationConcurrentEmptyTargetDirections(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := models.Tag{Name: "concurrent"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	svc := &VideoService{}
	apply := func(effect string) func() error {
		return func() error {
			_, err := svc.SetRecommendationTagPreference(context.Background(), RecommendationTagPreferenceRequest{TagID: tag.ID, Effect: effect})
			return err
		}
	}
	competingPreferences(t, apply("boost"), apply("downweight"))
	var rows []models.RecommendationFeedback
	if err := database.DB.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Revision != 1 {
		t.Fatalf("conflicting rows: %+v", rows)
	}
	if err := database.DB.First(&tag, tag.ID).Error; err != nil {
		t.Fatal(err)
	}
	if tag.PreferenceRevision != 1 {
		t.Fatalf("loser changed root revision: %d", tag.PreferenceRevision)
	}
}

func TestRecommendationConcurrentReviewedMergeAndPreference(t *testing.T) {
	for _, operation := range []string{"create", "revoke"} {
		t.Run(operation, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			source, target := models.Tag{Name: "source"}, models.Tag{Name: "target"}
			if err := database.DB.Create(&source).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.DB.Create(&target).Error; err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			video := &VideoService{}
			tags := &TagService{}
			row, err := video.SetRecommendationTagPreference(ctx, RecommendationTagPreferenceRequest{TagID: source.ID, Effect: "boost"})
			if err != nil {
				t.Fatal(err)
			}
			preview, err := tags.PreviewTagMergePreferences(ctx, []uint{source.ID}, target.ID)
			if err != nil {
				t.Fatal(err)
			}
			merge := func() error { _, err := tags.MergeTagsFromPreferencePreview(ctx, preview.Token); return err }
			change := func() error {
				if operation == "revoke" {
					return video.RevokeRecommendationFeedback(ctx, row.ID, row.Revision)
				}
				_, err := video.SetRecommendationTagPreference(ctx, RecommendationTagPreferenceRequest{TagID: target.ID, Effect: "downweight"})
				return err
			}
			competingPreferences(t, merge, change)
			var all []models.RecommendationFeedback
			if err := database.DB.Find(&all).Error; err != nil {
				t.Fatal(err)
			}
			seen := map[uint]bool{}
			for _, pref := range all {
				if seen[pref.TargetID] {
					t.Fatal("duplicate target")
				}
				seen[pref.TargetID] = true
			}
		})
	}
}
