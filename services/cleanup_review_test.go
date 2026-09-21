package services

import (
	"os"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func TestCleanupCachedClipUsesReviewedSourceVersion(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	full := clipFixtureVideo(t, root, "movie.mp4", "movie-content")
	clip := clipFixtureVideo(t, root, "excerpt.mp4", "clip")
	hashes := randomFrameHashes(83, 40)
	seedFrameHashSequence(t, full, hashes)
	seedFrameHashSequence(t, clip, hashes[8:24])
	analysis, err := newClipFixtureCleanupService().AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatal(err)
	}
	if err := DismissClipCandidate(full.ID, clip.ID); err != nil {
		t.Fatal(err)
	}
	oldCache := &CleanupService{status: CleanupStatus{Completed: true, Analysis: analysis}}
	if err := os.WriteFile(clip.Path, []byte("re-encoded-clip"), 0600); err != nil {
		t.Fatal(err)
	}
	seedFrameHashSequenceReplacing(t, clip, hashes[8:24])
	// 新文件允许重新评估，但不能拿新指纹让旧缓存中已经忽略的那版再次出现。
	if got := oldCache.Status(); got.Error != "" || len(got.Analysis.ClipGroups) != 0 {
		t.Fatalf("old candidate returned after source changed: %+v", got)
	}
	fresh, err := newClipFixtureCleanupService().AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil || len(fresh.ClipGroups) != 1 {
		t.Fatalf("changed source must remain eligible: %+v err=%v", fresh, err)
	}
	newCache := &CleanupService{status: CleanupStatus{Completed: true, Analysis: fresh}}
	if got := newCache.Status(); got.Error != "" || len(got.Analysis.ClipGroups) != 1 {
		t.Fatalf("old decision suppressed newly analyzed source: %+v", got)
	}
}

func TestCleanupStatusDoesNotExposeUnreviewedCacheWhenDecisionReadFails(t *testing.T) {
	setupCleanupServiceTestDB(t)
	analysis := &CleanupAnalysis{NearDuplicateGroups: []CleanupDuplicateGroup{{Original: models.Video{ID: 1}, Candidates: []models.Video{{ID: 2}}}}}
	svc := &CleanupService{status: CleanupStatus{Completed: true, Analysis: analysis}}
	if err := database.DB.Migrator().DropTable(&models.NearDuplicateDismissal{}); err != nil {
		t.Fatal(err)
	}
	if got := svc.Status(); got.Error == "" || got.Analysis != nil {
		t.Fatalf("decision read failure must not restore candidates: %+v", got)
	}
	if svc.status.Analysis != analysis {
		t.Fatal("temporary read error discarded cached analysis")
	}
}

func TestCleanupReviewSplitsConflictingGroupWithoutMutatingCache(t *testing.T) {
	group := CleanupDuplicateGroup{Original: models.Video{ID: 1}, Candidates: []models.Video{{ID: 2}, {ID: 3}, {ID: 4}}}
	denied := map[[2]uint]struct{}{{1, 2}: {}, {1, 4}: {}, {2, 3}: {}, {3, 4}: {}}
	groups := splitReviewedCleanupGroup(group, denied)
	if len(groups) != 2 || groups[0].Original.ID != 1 || groups[0].Candidates[0].ID != 3 || groups[1].Original.ID != 2 || groups[1].Candidates[0].ID != 4 {
		t.Fatalf("unreviewed pairs were lost or denied pairs regrouped: %+v", groups)
	}
	if len(group.Candidates) != 3 || group.Candidates[0].ID != 2 {
		t.Fatal("cached candidates were mutated")
	}
}

func TestCleanupAnalysisHonorsDecisionSavedDuringMatching(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	full := clipFixtureVideo(t, root, "movie.mp4", "movie-content")
	clip := clipFixtureVideo(t, root, "excerpt.mp4", "clip")
	hashes := randomFrameHashes(84, 40)
	seedFrameHashSequence(t, full, hashes)
	seedFrameHashSequence(t, clip, hashes[8:24])
	seedPerceptualHashRow(t, full, strings.Repeat("0", 16))
	seedPerceptualHashRow(t, clip, strings.Repeat("0", 16))
	// 近似重复已算好、截取匹配刚读序列时，模拟另一审阅入口保存决定。
	saved := false
	var saveErr error
	callback := "test:review-during-clip-matching"
	if err := database.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_frame_hash_sequences" && !saved {
			saved = true
			saveErr = DismissNearDuplicateGroup([]uint{full.ID, clip.ID})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Query().Remove(callback)
	result, err := newClipFixtureCleanupService().AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil || saveErr != nil || !saved {
		t.Fatalf("analysis=%v decision=%v saved=%v", err, saveErr, saved)
	}
	if len(result.NearDuplicateGroups) != 0 || len(result.ClipGroups) != 0 {
		t.Fatalf("decision saved during analysis was ignored: %+v", result)
	}
}

func TestCleanupStatusHonorsPersistedReviewDecisions(t *testing.T) {
	for _, decision := range []string{"near", "same-source", "clip"} {
		t.Run(decision, func(t *testing.T) {
			setupCleanupServiceTestDB(t)
			root := t.TempDir()
			mockFFProbe(t, root)
			full := clipFixtureVideo(t, root, "movie.mp4", "movie-content")
			clip := clipFixtureVideo(t, root, "excerpt.mp4", "clip")
			hashes := randomFrameHashes(82, 40)
			seedFrameHashSequence(t, full, hashes)
			seedFrameHashSequence(t, clip, hashes[8:24])
			seedPerceptualHashRow(t, full, strings.Repeat("0", 16))
			seedPerceptualHashRow(t, clip, strings.Repeat("0", 16))
			analysis, err := newClipFixtureCleanupService().AnalyzeCleanupCandidates(CleanupCriteria{})
			if err != nil {
				t.Fatal(err)
			}
			if len(analysis.NearDuplicateGroups) != 1 || len(analysis.ClipGroups) != 1 {
				t.Fatalf("expected near and clip candidates: %+v", analysis)
			}
			relation := models.VideoSameSourceRelation{VideoAID: full.ID, VideoBID: clip.ID, Status: models.VideoSameSourceStatusDetected, Confidence: "high", DetectionVersion: "test"}
			if err := database.DB.Create(&relation).Error; err != nil {
				t.Fatal(err)
			}
			analysis.SameSourceGroups = []CleanupSameSourceGroup{{RelationID: relation.ID, Preferred: full, Alternative: clip}}
			svc := &CleanupService{status: CleanupStatus{Completed: true, Analysis: analysis}}
			switch decision {
			case "near":
				err = DismissNearDuplicateGroup([]uint{full.ID, clip.ID})
			case "same-source":
				err = (&AISameSourceService{now: time.Now}).RejectRelation(relation.ID)
			case "clip":
				err = DismissClipCandidate(full.ID, clip.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			// 重开面板、另一调用方回读，都必须服从已落库的决定；不是只摘掉前端行。
			for i := 0; i < 2; i++ {
				status := svc.Status()
				if status.Error != "" || status.Analysis == nil || len(status.Analysis.ClipGroups) != 0 {
					t.Fatalf("reviewed clip returned from cache: %+v", status)
				}
				if decision != "clip" && (len(status.Analysis.NearDuplicateGroups) != 0 || len(status.Analysis.SameSourceGroups) != 0) {
					t.Fatalf("rejected pair returned in another category: %+v", status.Analysis)
				}
				if decision == "clip" && (len(status.Analysis.NearDuplicateGroups) != 1 || len(status.Analysis.SameSourceGroups) != 1) {
					t.Fatal("ignoring a clip must not imply rejecting same-source evidence")
				}
			}
			// 新服务重新分析（重启），以及分析完成前已保存的决定，仍沿用相同口径。
			fresh, err := newClipFixtureCleanupService().AnalyzeCleanupCandidates(CleanupCriteria{})
			if err != nil || len(fresh.ClipGroups) != 0 {
				t.Fatalf("reviewed clip returned after reanalysis: %+v err=%v", fresh, err)
			}
			if len(analysis.ClipGroups) != 1 || len(analysis.NearDuplicateGroups) != 1 {
				t.Fatal("reading status mutated shared analysis slices")
			}
		})
	}
}
