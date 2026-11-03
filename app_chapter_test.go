package main

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

func TestChapterAppPurposeIsolationAndMaintenanceReachHTTP(t *testing.T) {
	for _, action := range []string{"query_off", "summary_off", "maintenance"} {
		t.Run(action, func(t *testing.T) {
			setupAppTestDB(t)
			started, release := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": `{"chapters":[{"title":"草稿","summary":"字幕内容","cue_ids":[1]}]}`}}}})
			}))
			defer server.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			root := t.TempDir()
			video := models.Video{Name: "chapter.mp4", Path: filepath.Join(root, "chapter.mp4"), Directory: root, Duration: 10, Size: 6}
			if err := os.WriteFile(video.Path, []byte("source"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "chapter.srt"), []byte("1\n00:00:00,000 --> 00:00:01,000\n字幕内容\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := database.DB.Create(&video).Error; err != nil {
				t.Fatal(err)
			}
			var settings models.Settings
			database.DB.First(&settings)
			settings.VideoExtensions = ".mp4"
			settings.AITaggingBaseURL = server.URL + "/v1"
			settings.AITaggingModel = "fixture"
			settings.AITaggingAPIKey = "fixture-key"
			settings.QueryParseEnabled = true
			settings.ChapterSummaryEnabled = true
			if err := database.DB.Save(&settings).Error; err != nil {
				t.Fatal(err)
			}
			ai := services.NewTextAIService()
			chapters := services.NewChapterService(ai)
			a := &App{ctx: context.Background(), textAI: ai, chapters: chapters, videoService: services.NewVideoService(nil), settingsService: &services.SettingsService{}, idleGate: services.NewIdleGate()}
			t.Cleanup(func() { a.stopFunctionalPolish(false) })
			writer := log.Writer()
			defer log.SetOutput(writer)
			prepared := a.PrepareChapterDraft(services.PrepareChapterRequest{VideoID: video.ID, Scope: services.ChapterScope{Whole: true}})
			if prepared.Status != "ok" {
				t.Fatal(prepared.Error)
			}
			p := prepared.Data
			start := a.StartChapterDraft(services.StartChapterRequest{RequestID: uuid.NewString(), VideoID: video.ID, SourceVersion: p.SourceVersion, SubtitleFingerprint: p.SubtitleFingerprint, ExpectedChapterRevision: p.ExpectedChapterRevision, ProviderRevision: p.ProviderRevision, Scope: p.Scope, ConfirmedScope: true})
			if start.Status != "ok" {
				t.Fatal(start.Error)
			}
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("model request not started")
			}
			if action == "maintenance" {
				a.stopFunctionalPolish(false)
			} else {
				if action == "query_off" {
					settings.QueryParseEnabled = false
				} else {
					settings.ChapterSummaryEnabled = false
				}
				if err := a.UpdateSettings(settings); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			want := "cancelled"
			if action == "query_off" {
				want = "draft_ready"
			}
			deadline := time.Now().Add(5 * time.Second)
			var got *services.ChapterDraftView
			for time.Now().Before(deadline) {
				var err error
				got, err = chapters.GetChapterDraft(context.Background(), start.Data.JobID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Job.Status == want {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if got.Job.Status != want {
				t.Fatalf("%s: expected %s got %s", action, want, got.Job.Status)
			}
			if action == "maintenance" {
				result := a.GetChapterDraft(start.Data.JobID)
				if result.Error == nil || result.Error.Code != "maintenance" {
					t.Fatalf("old owner admitted %+v", result)
				}
			}
		})
	}
}

func TestChapterTaskCenterKeepsOldOpenJobsBeyondRecentPage(t *testing.T) {
	setupAppTestDB(t)
	old := models.ChapterDraftJob{RequestID: uuid.NewString(), RequestDigest: "old", Status: "running", Revision: 1, TotalChunks: 3, CompletedChunks: 1}
	if err := database.DB.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		job := models.ChapterDraftJob{RequestID: uuid.NewString(), RequestDigest: "ready", Status: "draft_ready", Revision: 1}
		if err := database.DB.Create(&job).Error; err != nil {
			t.Fatal(err)
		}
	}
	a := &App{ctx: context.Background(), chapters: services.NewChapterService(nil)}
	src := a.loadTaskCenterSources()
	found := false
	for _, job := range src.chapterJobs {
		if job.ID == old.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("running chapter job was hidden by recent completed jobs")
	}
	if len(src.chapterJobs) > 101 {
		t.Fatalf("recent history became unbounded: %d", len(src.chapterJobs))
	}
}
