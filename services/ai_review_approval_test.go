package services

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func seedVideoReview(t *testing.T, name string, tag models.Tag) (models.Video, models.AITagCandidate) {
	t.Helper()
	video := models.Video{Name: name, Path: "/review/" + name}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	row := models.AITagCandidate{VideoID: video.ID, MatchedTagID: &tag.ID, SuggestedName: tag.Name, NormalizedName: tag.Name, Reasoning: "检索线索", Confidence: "high", Status: "pending"}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return video, row
}
func finishReviewApproval(t *testing.T, w *ReviewApprovalWorkflow, p ReviewApprovalPreview) ReviewApprovalState {
	t.Helper()
	if _, err := w.Start(context.Background(), p.Token); err != nil {
		t.Fatal(err)
	}
	waitReviewWorkflow(t, w)
	state, err := w.State(p.Token, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestReviewApprovalVideoFreezesEligibleIDsAndExactVersions(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := configuredAITag("动作", "#123456")
	other := configuredAITag("海边", "#123456")
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	_, first := seedVideoReview(t, "first.mp4", tag)
	deleted, excluded := seedVideoReview(t, "deleted.mp4", tag)
	changedMedia, changed := seedVideoReview(t, "changed.mp4", tag)
	if err := database.DB.Delete(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	w := NewAITaggingService().ReviewApproval()
	p, err := w.Preview(context.Background(), ReviewApprovalRequest{Scope: "filtered", Filter: ReviewSearchRequest{Keyword: "检索线索"}})
	if err != nil || p.Matched != 3 || p.Eligible != 2 || p.Excluded != 1 || p.MediaCount != 2 || p.LinkCount != 2 {
		t.Fatalf("preview %+v %v", p, err)
	}
	_, added := seedVideoReview(t, "new.mp4", tag)
	if err := database.DB.Unscoped().Model(&deleted).Update("deleted_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	// Bypass updated_at deliberately: timestamp-only version checking would approve the wrong tag.
	if err := database.DB.Model(&changed).UpdateColumn("matched_tag_id", other.ID).Error; err != nil {
		t.Fatal(err)
	}
	state := finishReviewApproval(t, w, p)
	if state.Total != 2 || state.Succeeded != 1 || state.Skipped != 1 || state.Failed != 0 {
		t.Fatalf("state %+v", state)
	}
	for _, row := range []models.AITagCandidate{excluded, changed, added} {
		var got models.AITagCandidate
		if err := database.DB.First(&got, row.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.Status != "pending" {
			t.Fatalf("unconfirmed candidate %d changed: %s", row.ID, got.Status)
		}
	}
	var official int64
	if err := database.DB.Table("video_tags").Where("video_id = ?", changedMedia.ID).Count(&official).Error; err != nil || official != 0 {
		t.Fatalf("wrong tag committed: %d %v", official, err)
	}
	var approved models.AITagCandidate
	if err := database.DB.First(&approved, first.ID).Error; err != nil || approved.Status != "approved" {
		t.Fatalf("approved %+v %v", approved, err)
	}
}

func TestReviewApprovalLoadedScopeDeduplicatesAndSkipsDeletedTag(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := configuredAITag("动作", "#123456")
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	_, row := seedVideoReview(t, "file.mp4", tag)
	w := NewAITaggingService().ReviewApproval()
	p, err := w.Preview(context.Background(), ReviewApprovalRequest{Scope: "loaded", IDs: []uint{row.ID, row.ID, 99999}})
	if err != nil || p.Matched != 2 || p.Eligible != 1 || p.Excluded != 1 {
		t.Fatalf("loaded %+v %v", p, err)
	}
	if err := database.DB.Delete(&tag).Error; err != nil {
		t.Fatal(err)
	}
	state := finishReviewApproval(t, w, p)
	if state.Succeeded != 0 || state.Skipped != 1 {
		t.Fatalf("deleted tag %+v", state)
	}
	for _, request := range []ReviewApprovalRequest{{Scope: "unknown"}, {Scope: "loaded", IDs: []uint{0}}, {Scope: "filtered", IDs: []uint{row.ID}}, {Scope: "filtered", Filter: ReviewSearchRequest{Status: "approved"}}} {
		if _, err := NewAITaggingService().ReviewApproval().Preview(context.Background(), request); err == nil {
			t.Fatalf("invalid request accepted %+v", request)
		}
	}
}

func TestReviewApprovalImagePreservesExcludedAndChangedEvidence(t *testing.T) {
	svc := newImageAITaggingReviewTestService(t)
	tag := configuredAITag("海岸", "#123456")
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	images := []models.Image{{Name: "one.jpg", Path: "/review/one.jpg"}, {Name: "two.jpg", Path: "/review/two.jpg"}, {Name: "low.jpg", Path: "/review/low.jpg"}}
	if err := database.DB.Create(&images).Error; err != nil {
		t.Fatal(err)
	}
	one := seedImageAITagCandidate(t, images[0].ID, tag, "high")
	two := seedImageAITagCandidate(t, images[1].ID, tag, "high")
	low := seedImageAITagCandidate(t, images[2].ID, tag, "low")
	w := svc.ReviewApproval()
	p, err := w.Preview(context.Background(), ReviewApprovalRequest{Scope: "filtered", Filter: ReviewSearchRequest{Keyword: "海岸"}})
	if err != nil || p.Matched != 3 || p.Eligible != 2 {
		t.Fatalf("preview %+v %v", p, err)
	}
	if err := database.DB.Model(&two).UpdateColumn("reasoning", "新理由").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&low).UpdateColumn("confidence", "high").Error; err != nil {
		t.Fatal(err)
	}
	state := finishReviewApproval(t, w, p)
	if state.Total != 2 || state.Succeeded != 1 || state.Skipped != 1 {
		t.Fatalf("state %+v", state)
	}
	if !imageHasTag(t, one.ImageID, tag.ID) || imageHasTag(t, two.ImageID, tag.ID) || imageHasTag(t, low.ImageID, tag.ID) {
		t.Fatal("approval escaped frozen image set")
	}
}

func TestReviewApprovalCommittedSuccessDoesNotNeedDTOHydration(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := configuredAITag("动作", "#123456")
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	_, row := seedVideoReview(t, "file.mp4", tag)
	w := NewAITaggingService().ReviewApproval()
	p, err := w.Preview(context.Background(), ReviewApprovalRequest{Scope: "loaded", IDs: []uint{row.ID}})
	if err != nil {
		t.Fatal(err)
	}
	const callback = "test:post_commit_dto_failure"
	if err := database.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "ai_tag_candidates" && len(tx.Statement.Preloads) > 0 {
			tx.AddError(errors.New("DTO unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Query().Remove(callback)
	state := finishReviewApproval(t, w, p)
	if state.Succeeded != 1 || state.Failed != 0 || state.Results[0].ID != row.ID {
		t.Fatalf("committed %+v", state)
	}
	var count int64
	if err := database.DB.Table("video_tags").Where("video_id = ? AND tag_id = ?", row.VideoID, tag.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("commit missing %d %v", count, err)
	}
}

func TestReviewRevisionIncludesRawFieldsAndExactTime(t *testing.T) {
	base := models.AITagCandidate{ID: 1, VideoID: 2, Status: "pending", Reasoning: "a|b", SourceSummary: "c", UpdatedAt: time.Now()}
	original := videoReviewRevision(base)
	mutations := []models.AITagCandidate{base, base, base, base}
	mutations[0].Reasoning = "a"
	mutations[0].SourceSummary = "b|c"
	mutations[1].UpdatedAt = base.UpdatedAt.Add(time.Nanosecond)
	mutations[2].SourceSummary = "changed"
	run := uint(1)
	mutations[3].RunID = &run
	for i, changed := range mutations {
		if videoReviewRevision(changed) == original {
			t.Fatal(fmt.Sprintf("mutation %d not detected", i))
		}
	}
	zone := base
	zone.UpdatedAt = zone.UpdatedAt.In(time.FixedZone("test", 9*3600))
	if videoReviewRevision(zone) != original {
		t.Fatal("same instant changed revision")
	}
	if reviewRevision("\xff") == reviewRevision("\xfe") {
		t.Fatal("raw bytes collapsed")
	}
}

func TestReviewApprovalDatabaseUnavailableStopsWithoutFailingEveryMember(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := configuredAITag("动作", "#123456")
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	_, one := seedVideoReview(t, "one.mp4", tag)
	_, two := seedVideoReview(t, "two.mp4", tag)
	w := NewAITaggingService().ReviewApproval()
	p, err := w.Preview(context.Background(), ReviewApprovalRequest{Scope: "loaded", IDs: []uint{one.ID, two.ID}})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	state := finishReviewApproval(t, w, p)
	if state.State != "failed" || state.Processed != 0 || state.Remaining != 2 {
		t.Fatalf("unavailable %+v", state)
	}
}

func TestReviewApprovalDoesNotFreezeAReplacementVersionAfterMatching(t *testing.T) {
	for _, kind := range []string{"video", "image"} {
		t.Run(kind, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			tag := configuredAITag("原标签", "#123456")
			replacement := configuredAITag("新标签", "#123456")
			if err := database.DB.Create(&tag).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.DB.Create(&replacement).Error; err != nil {
				t.Fatal(err)
			}
			var id uint
			var w *ReviewApprovalWorkflow
			table := "ai_tag_candidates"
			if kind == "video" {
				_, candidate := seedVideoReview(t, "race.mp4", tag)
				id = candidate.ID
				w = NewAITaggingService().ReviewApproval()
			} else {
				image := models.Image{Name: "race.jpg", Path: "/review/race.jpg"}
				if err := database.DB.Create(&image).Error; err != nil {
					t.Fatal(err)
				}
				candidate := seedImageAITagCandidate(t, image.ID, tag, "high")
				id = candidate.ID
				table = "image_ai_tag_candidates"
				w = NewImageAITaggingService(database.DB, nil, nil).ReviewApproval()
			}
			var changed atomic.Bool
			name := "test:change_between_match_and_members"
			if err := database.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == table && len(tx.Statement.Preloads) > 0 && changed.CompareAndSwap(false, true) {
					if err := database.DB.Table(table).Where("id = ?", id).Updates(map[string]interface{}{"matched_tag_id": replacement.ID, "reasoning": "改写后的理由"}).Error; err != nil {
						tx.AddError(err)
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer database.DB.Callback().Query().Remove(name)
			preview, err := w.Preview(context.Background(), ReviewApprovalRequest{Scope: "filtered", Filter: ReviewSearchRequest{TagID: tag.ID}})
			if err != nil || !changed.Load() || preview.Matched != 1 || preview.Eligible != 0 || preview.Token != "" {
				t.Fatalf("replacement entered preview %+v changed=%v err=%v", preview, changed.Load(), err)
			}
		})
	}
}

func TestReviewApprovalCancellationCoversWaitingForATransactionConnection(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := configuredAITag("动作", "#123456")
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	_, candidate := seedVideoReview(t, "pool.mp4", tag)
	w := NewAITaggingService().ReviewApproval()
	preview, err := w.Preview(context.Background(), ReviewApprovalRequest{Scope: "loaded", IDs: []uint{candidate.ID}})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	held, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	waits := sqlDB.Stats().WaitCount
	if _, err := w.Start(context.Background(), preview.Token); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for sqlDB.Stats().WaitCount == waits && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sqlDB.Stats().WaitCount == waits {
		t.Fatal("approval did not enter pool wait")
	}
	done := make(chan struct{})
	go func() { w.CloseAndWait(); close(done) }()
	stoppedWhileHeld := false
	select {
	case <-done:
		stoppedWhileHeld = true
	case <-time.After(250 * time.Millisecond):
	}
	_ = held.Close()
	<-done
	if !stoppedWhileHeld {
		t.Fatal("cancelled approval waited for an unrelated held connection")
	}
	state, err := w.State(preview.Token, 0, 0)
	if err != nil || state.Succeeded != 0 || state.Remaining != 1 {
		t.Fatalf("cancelled %+v %v", state, err)
	}
}
