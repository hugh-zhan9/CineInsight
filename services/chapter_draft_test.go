package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"
)

func chapterDraftFixture(t *testing.T, handler http.HandlerFunc) (*ChapterService, models.Video, string) {
	t.Helper()
	video, source := notesFixture(t)
	text := "1\n00:00:00,000 --> 00:00:01,000\n第一句\n\n2\n00:00:02,000 --> 00:00:03,000\n第二句\n\n3\n00:00:04,000 --> 00:00:05,000\n第三句\n\n"
	if err := os.WriteFile(subtitleparser.SRTPathForVideo(video.Path), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Updates(map[string]any{"ai_tagging_base_url": server.URL + "/v1", "ai_tagging_model": "chapter-fixture", "ai_tagging_api_key": "fixture-key", "chapter_summary_enabled": true}).Error; err != nil {
		t.Fatal(err)
	}
	ai := NewTextAIService()
	t.Cleanup(ai.Invalidate)
	service := NewChapterService(ai)
	t.Cleanup(service.StopAndWait)
	return service, video, source
}
func chapterAutoReply(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Error(err)
		return
	}
	if json.Unmarshal(raw, &body) != nil || len(body.Messages) != 2 {
		t.Error("invalid model body")
		return
	}
	var chunk chapterChunk
	if json.Unmarshal([]byte(body.Messages[1].Content), &chunk) != nil {
		t.Error("invalid chunk")
		return
	}
	ids := []int{}
	for _, cue := range chunk.Cues {
		ids = append(ids, cue.ID)
	}
	if strings.Contains(body.Messages[1].Content, ".mp4") || strings.Contains(body.Messages[1].Content, "video_id") || strings.Contains(body.Messages[1].Content, "/Users/") {
		t.Error("private media metadata sent")
	}
	encoded, _ := json.Marshal(map[string]any{"chapters": []any{map[string]any{"title": "校对草稿", "summary": "仅根据字幕的摘要", "cue_ids": ids}}})
	textAIReply(w, string(encoded))
}
func preparedChapterRequest(t *testing.T, service *ChapterService, videoID uint, scope ChapterScope) StartChapterRequest {
	t.Helper()
	p, err := service.PrepareChapterDraft(context.Background(), PrepareChapterRequest{VideoID: videoID, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	return StartChapterRequest{RequestID: uuid.NewString(), VideoID: videoID, SourceVersion: p.SourceVersion, SubtitleFingerprint: p.SubtitleFingerprint, ExpectedChapterRevision: p.ExpectedChapterRevision, ProviderRevision: p.ProviderRevision, Scope: p.Scope, ConfirmedScope: true}
}
func waitChapterJob(t *testing.T, service *ChapterService, id uint, status string) *ChapterDraftView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view, err := service.GetChapterDraft(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if view.Job.Status == status {
			return view
		}
		if view.Job.Status == "failed" || view.Job.Status == "cancelled" || view.Job.Status == "obsolete" {
			t.Fatalf("wanted %s got %+v", status, view)
		}
		time.Sleep(5 * time.Millisecond)
	}
	view, _ := service.GetChapterDraft(context.Background(), id)
	t.Fatalf("job did not reach %s: %+v", status, view)
	return nil
}
func TestChapterDraftIdempotencyPartialApplicationAndRangeConflict(t *testing.T) {
	var calls atomic.Int64
	service, video, source := chapterDraftFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); chapterAutoReply(t, w, r) })
	ctx := context.Background()
	manual, err := service.SaveVideoChapters(ctx, SaveChaptersRequest{VideoID: video.ID, SourceVersion: source, Chapters: []ChapterInput{{Title: "保留前段", StartMS: 0, EndMS: 1000}, {Title: "替换中段", StartMS: 2000, EndMS: 3000}, {Title: "保留后段", Summary: "原文字", StartMS: 4000, EndMS: 6000}}})
	if err != nil {
		t.Fatal(err)
	}
	request := preparedChapterRequest(t, service, video.ID, ChapterScope{StartMS: 2100, EndMS: 2900})
	if request.Scope.StartMS != 2000 || request.Scope.EndMS != 3000 {
		t.Fatalf("unreviewed expansion %+v", request.Scope)
	}
	start, err := service.StartChapterDraft(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := service.StartChapterDraft(ctx, request)
	if err != nil || repeat.JobID != start.JobID {
		t.Fatalf("repeat %+v %v", repeat, err)
	}
	changed := request
	changed.Scope.StartMS = 2001
	if _, err := service.StartChapterDraft(ctx, changed); !errors.Is(err, ErrChapterRequestUsed) {
		t.Fatalf("same ID different input %v", err)
	}
	draft := waitChapterJob(t, service, start.JobID, "draft_ready")
	if len(draft.Chapters) != 1 || draft.Chapters[0].StartMS != 2000 || calls.Load() != 1 {
		t.Fatalf("draft %+v calls %d", draft, calls.Load())
	}
	before, _ := service.GetVideoChapterSets(ctx, video.ID)
	if before.Manual.Revision != manual.Revision {
		t.Fatal("draft overwrote manual chapters")
	}
	preview, err := service.PreviewChapterApplication(ctx, ChapterApplicationRequest{JobID: start.JobID, ExpectedJobRevision: draft.Job.Revision, ExpectedChapterRevision: manual.Revision})
	if err != nil || preview.ApplyToken == "" || preview.Retained != 2 || len(preview.RemoveIDs) != 1 {
		t.Fatalf("preview %+v %v", preview, err)
	}
	applied, err := service.ApplyChapterDraft(ctx, preview.ApplyToken)
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.ApplyChapterDraft(ctx, preview.ApplyToken)
	if err != nil || *again != *applied {
		t.Fatalf("reapplied %+v %v", again, err)
	}
	after, _ := service.GetVideoChapterSets(ctx, video.ID)
	if len(after.Manual.Chapters) != 3 || after.Manual.Chapters[0] != manual.Chapters[0] || after.Manual.Chapters[2] != manual.Chapters[2] {
		t.Fatalf("outside range changed %+v", after.Manual)
	}
	// A chapter crossing a partial boundary must be reported, never truncated.
	crossing := []ChapterInput{{ID: 88, Title: "跨界章", StartMS: 0, EndMS: 5000}}
	_, _, conflicts, err := mergeChapterApplication(crossing, draft.Chapters, request.Scope)
	if !errors.Is(err, ErrChapterRangeConflict) || len(conflicts) != 1 || conflicts[0].ID != 88 {
		t.Fatalf("crossing %+v %v", conflicts, err)
	}
}
func TestChapterChunksKeepTailAndRejectInventedEvidence(t *testing.T) {
	cues := []chapterCue{{ID: 1, StartMS: 0, EndMS: 1000, Text: strings.Repeat("字", 7900)}, {ID: 2, StartMS: 1000, EndMS: 2000, Text: strings.Repeat("字", 200)}, {ID: 3, StartMS: 2000, EndMS: 3000, Text: "尾段"}}
	chunks, count, err := chapterChunks(cues)
	if err != nil || len(chunks) != 2 || count != 8102 || len(chunks[1].Cues) != 2 || chunks[1].Cues[1].ID != 3 {
		t.Fatalf("chunks %+v count=%d %v", chunks, count, err)
	}
	for _, body := range []string{`{"chapters":[{"title":"x","summary":"s","cue_ids":[2,2]}]}`, `{"chapters":[{"title":"x","summary":"s","cue_ids":[999]}]}`, `{"chapters":[{"title":"x","summary":"s","cue_ids":[3,2]}]}`, `{"chapters":[{"title":"x","summary":"s","cue_ids":[2],"start_ms":0}]}`, `{"chapters":[{"title":"x","summary":"s","cue_ids":[2]},{"title":"y","summary":"s","cue_ids":[2]}]}`} {
		if _, err := parseChapterSummary(body, chunks[1]); !errors.Is(err, ErrTextAIOutput) {
			t.Fatalf("invalid evidence %s: %v", body, err)
		}
	}
	huge := []chapterCue{}
	for i := 0; i < 13; i++ {
		huge = append(huge, chapterCue{ID: i + 1, Text: strings.Repeat("字", 8000)})
	}
	if _, _, err := chapterChunks(huge); !errors.Is(err, ErrChapterSummaryTooLarge) {
		t.Fatal("silently truncated oversized summary")
	}
}
func TestChapterDraftCancellationCannotCommitLateHTTPResult(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	service, video, _ := chapterDraftFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		textAIReply(w, `{"chapters":[{"title":"late","summary":"late","cue_ids":[1]}]}`)
	})
	request := preparedChapterRequest(t, service, video.ID, ChapterScope{Whole: true})
	start, err := service.StartChapterDraft(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("no model request")
	}
	view, err := service.GetChapterDraft(context.Background(), start.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CancelChapterDraft(context.Background(), start.JobID, view.Job.Revision); err != nil {
		t.Fatal(err)
	}
	close(release)
	service.StopAndWait()
	after, err := service.GetChapterDraft(context.Background(), start.JobID)
	if err != nil || after.Job.Status != "cancelled" || len(after.Chapters) != 0 {
		t.Fatalf("late result %+v %v", after, err)
	}
}
func TestChapterTerminalCompactionRetainsRequestIdempotency(t *testing.T) {
	service, video, _ := chapterDraftFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("compacted request reached model") })
	request := preparedChapterRequest(t, service, video.ID, ChapterScope{Whole: true})
	digest := chapterRequestDigest(request)
	for i := 0; i < 25; i++ {
		id := uuid.NewString()
		d := "other"
		if i == 0 {
			id = request.RequestID
			d = digest
		}
		job := models.ChapterDraftJob{RequestID: id, RequestDigest: d, VideoID: &video.ID, TitleSnapshot: "private", Status: "failed", Revision: 1}
		if err := database.DB.Create(&job).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := service.RecoverInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := service.ListChapterDraftJobs(context.Background(), ChapterJobQuery{})
	if err != nil || len(page.Items) != 20 {
		t.Fatalf("page %+v %v", page, err)
	}
	repeat, err := service.StartChapterDraft(context.Background(), request)
	if err != nil || repeat.Status != "job_compacted" {
		t.Fatalf("compacted %+v %v", repeat, err)
	}
	var retained models.ChapterDraftJob
	if err := database.DB.Where("request_id = ?", request.RequestID).First(&retained).Error; err != nil {
		t.Fatal(err)
	}
	if !retained.Compacted || retained.VideoID != nil || retained.TitleSnapshot != "" {
		t.Fatalf("private tombstone %+v", retained)
	}
}
func TestChapterPrepareDisclosureDoesNotSendAndStaleSubtitleRejectsStart(t *testing.T) {
	service, video, _ := chapterDraftFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unapproved changed subtitle sent") })
	request := preparedChapterRequest(t, service, video.ID, ChapterScope{Whole: true})
	unconfirmed := request
	unconfirmed.ConfirmedScope = false
	if _, err := service.StartChapterDraft(context.Background(), unconfirmed); !errors.Is(err, ErrChapterInvalid) {
		t.Fatalf("unconfirmed scope admitted: %v", err)
	}
	mustNoteCount(t, &models.ChapterDraftJob{}, 0)
	if err := os.WriteFile(subtitleparser.SRTPathForVideo(video.Path), []byte(fmt.Sprintf("1\n00:00:00,000 --> 00:00:01,000\n%s\n", "changed")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartChapterDraft(context.Background(), request); !errors.Is(err, ErrChapterDraftObsolete) {
		t.Fatalf("unreviewed subtitle %v", err)
	}
}

func TestChapterApplicationCoordinatesSubtitleReplacementThroughCommit(t *testing.T) {
	service, video, _ := chapterDraftFixture(t, func(w http.ResponseWriter, r *http.Request) { chapterAutoReply(t, w, r) })
	ctx := context.Background()
	request := preparedChapterRequest(t, service, video.ID, ChapterScope{Whole: true})
	start, err := service.StartChapterDraft(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	draft := waitChapterJob(t, service, start.JobID, "draft_ready")
	preview, err := service.PreviewChapterApplication(ctx, ChapterApplicationRequest{JobID: start.JobID, ExpectedJobRevision: draft.Job.Revision})
	if err != nil {
		t.Fatal(err)
	}
	reached, release := make(chan struct{}), make(chan struct{})
	var intercepted atomic.Bool
	db := database.DB
	if err := db.Callback().Update().Before("gorm:update").Register("chapter_fixture:apply_pause", func(tx *gorm.DB) {
		if tx.Statement.Table == "chapter_draft_jobs" && intercepted.CompareAndSwap(false, true) {
			close(reached)
			select {
			case <-release:
			case <-time.After(3 * time.Second):
				tx.AddError(errors.New("fixture release missing"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Update().Remove("chapter_fixture:apply_pause")
	done := make(chan error, 1)
	go func() { _, err := service.ApplyChapterDraft(ctx, preview.ApplyToken); done <- err }()
	select {
	case <-reached:
	case <-time.After(3 * time.Second):
		t.Fatal("apply did not reach commit")
	}
	writerCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	unlock, lockErr := lockSubtitleFileContext(writerCtx, subtitleparser.SRTPathForVideo(video.Path))
	if lockErr == nil {
		err = os.WriteFile(subtitleparser.SRTPathForVideo(video.Path), []byte("1\n00:00:00,000 --> 00:00:01,000\nconcurrent replacement\n"), 0600)
		unlock()
		if err != nil {
			t.Error(err)
		}
	}
	close(release)
	applyErr := <-done
	if lockErr == nil && applyErr == nil {
		t.Fatal("old subtitle draft applied after coordinated subtitle replacement")
	}
	if lockErr != nil && !errors.Is(lockErr, context.DeadlineExceeded) {
		t.Fatal(lockErr)
	}
	if lockErr != nil && applyErr != nil {
		t.Fatalf("protected application failed: %v", applyErr)
	}
}

func TestChapterPurposeSwitchDoesNotCancelUnchangedPurpose(t *testing.T) {
	for _, purpose := range []string{"query_parse_enabled", "chapter_summary_enabled"} {
		t.Run(purpose, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			service, video, _ := chapterDraftFixture(t, func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
				}
				r.Body = io.NopCloser(strings.NewReader(string(raw)))
				chapterAutoReply(t, w, r)
			})
			if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("query_parse_enabled", true).Error; err != nil {
				t.Fatal(err)
			}
			request := preparedChapterRequest(t, service, video.ID, ChapterScope{Whole: true})
			start, err := service.StartChapterDraft(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("no model call")
			}
			if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update(purpose, false).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := service.textAI.Status(context.Background()); err != nil {
				t.Fatal(err)
			}
			close(release)
			want := "draft_ready"
			if purpose == "chapter_summary_enabled" {
				want = "cancelled"
			}
			waitChapterJob(t, service, start.JobID, want)
		})
	}
}

func TestChapterOverallDeadlineFailsWithoutLateDraftWrite(t *testing.T) {
	var calls atomic.Int64
	service, video, _ := chapterDraftFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		textAIReply(w, `{"chapters":[{"title":"late","summary":"late","cue_ids":[1]}]}`)
	})
	service.owner.budget = 150 * time.Millisecond
	request := preparedChapterRequest(t, service, video.ID, ChapterScope{Whole: true})
	start, err := service.StartChapterDraft(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitChapterJob(t, service, start.JobID, "failed")
	if failed.Job.ErrorCode != "summary_timeout" || len(failed.Chapters) != 0 || calls.Load() != 1 {
		t.Fatalf("deadline not isolated %+v calls=%d", failed, calls.Load())
	}
	service.StopAndWait()
	service.owner.closed = false
	again, err := service.StartChapterDraft(context.Background(), request)
	if err != nil || again.JobID != start.JobID || calls.Load() != 1 {
		t.Fatalf("timeout retried %+v %v", again, err)
	}
}

func TestChapterSegmentFailureKeepsOldManualAndDoesNotRetry(t *testing.T) {
	var calls atomic.Int64
	service, video, source := chapterDraftFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(500)
			return
		}
		chapterAutoReply(t, w, r)
	})
	text := fmt.Sprintf("1\n00:00:00,000 --> 00:00:01,000\n%s\n\n2\n00:00:02,000 --> 00:00:03,000\n%s\n", strings.Repeat("字", 7900), strings.Repeat("字", 7900))
	if err := os.WriteFile(subtitleparser.SRTPathForVideo(video.Path), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	manual, err := service.SaveVideoChapters(context.Background(), SaveChaptersRequest{VideoID: video.ID, SourceVersion: source, Chapters: []ChapterInput{{Title: "保留原文", StartMS: 0, EndMS: 4000}}})
	if err != nil {
		t.Fatal(err)
	}
	request := preparedChapterRequest(t, service, video.ID, ChapterScope{Whole: true})
	start, err := service.StartChapterDraft(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitChapterJob(t, service, start.JobID, "failed")
	if len(failed.Chapters) != 0 || failed.Job.CompletedChunks != 1 || calls.Load() != 2 {
		t.Fatalf("partial success leaked %+v calls=%d", failed, calls.Load())
	}
	sets, err := service.GetVideoChapterSets(context.Background(), video.ID)
	if err != nil || sets.Manual.Revision != manual.Revision || sets.Manual.Chapters[0].Title != "保留原文" {
		t.Fatalf("manual changed %+v %v", sets, err)
	}
}
func TestChapterInputChangeDuringModelCallObsoletesWholeDraft(t *testing.T) {
	var path string
	service, video, _ := chapterDraftFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if err := os.WriteFile(path, []byte("1\n00:00:00,000 --> 00:00:01,000\nnew source text\n"), 0600); err != nil {
			t.Error(err)
		}
		chapterAutoReply(t, w, r)
	})
	path = subtitleparser.SRTPathForVideo(video.Path)
	request := preparedChapterRequest(t, service, video.ID, ChapterScope{Whole: true})
	start, err := service.StartChapterDraft(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	obsolete := waitChapterJob(t, service, start.JobID, "obsolete")
	if len(obsolete.Chapters) != 0 {
		t.Fatal("obsolete output saved as draft")
	}
}

func TestChapterReadyCommitCoordinatesInputAndPurposeRevocation(t *testing.T) {
	for _, change := range []string{"subtitle", "purpose"} {
		t.Run(change, func(t *testing.T) {
			service, video, _ := chapterDraftFixture(t, func(w http.ResponseWriter, r *http.Request) { chapterAutoReply(t, w, r) })
			reached, release := make(chan struct{}), make(chan struct{})
			var once atomic.Bool
			db := database.DB
			if err := db.Callback().Update().Before("gorm:update").Register("chapter_fixture:ready_pause", func(tx *gorm.DB) {
				updates, _ := tx.Statement.Dest.(map[string]any)
				if tx.Statement.Table == "chapter_draft_jobs" && updates["status"] == "draft_ready" && once.CompareAndSwap(false, true) {
					close(reached)
					select {
					case <-release:
					case <-time.After(3 * time.Second):
						tx.AddError(errors.New("fixture release missing"))
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer db.Callback().Update().Remove("chapter_fixture:ready_pause")
			request := preparedChapterRequest(t, service, video.ID, ChapterScope{Whole: true})
			start, err := service.StartChapterDraft(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-reached:
			case <-time.After(3 * time.Second):
				t.Fatal("ready transition missing")
			}
			changedEarly := false
			var revoked chan struct{}
			if change == "subtitle" {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				unlock, err := lockSubtitleFileContext(ctx, subtitleparser.SRTPathForVideo(video.Path))
				if err == nil {
					changedEarly = true
					if err := os.WriteFile(subtitleparser.SRTPathForVideo(video.Path), []byte("1\n00:00:00,000 --> 00:00:01,000\nchanged before ready\n"), 0600); err != nil {
						t.Error(err)
					}
					unlock()
				} else if !errors.Is(err, context.DeadlineExceeded) {
					t.Error(err)
				}
			} else {
				revoked = make(chan struct{})
				go func() { service.textAI.InvalidatePurpose(TextAIChapterPurpose); close(revoked) }()
				select {
				case <-revoked:
					changedEarly = true
				case <-time.After(100 * time.Millisecond):
				}
			}
			close(release)
			if revoked != nil {
				<-revoked
			}
			service.owner.workers.Wait()
			var job models.ChapterDraftJob
			if err := db.First(&job, start.JobID).Error; err != nil {
				t.Fatal(err)
			}
			if changedEarly && job.Status == "draft_ready" {
				t.Fatalf("%s changed before ready commit but old output was published", change)
			}
			if !changedEarly && job.Status != "draft_ready" {
				t.Fatalf("coordinated commit failed %+v", job)
			}
		})
	}
}
