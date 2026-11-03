package main

import (
	"context"
	"github.com/google/uuid"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

func TestLibraryQueryAppCancelAndSettingsDisableReachHTTP(t *testing.T) {
	for _, action := range []string{"cancel", "disable"} {
		t.Run(action, func(t *testing.T) {
			setupAppTestDB(t)
			started, release := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				close(started)
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			settings := models.Settings{VideoExtensions: ".mp4", AITaggingBaseURL: server.URL + "/v1", AITaggingModel: "fixture", AITaggingAPIKey: "fixture-key", QueryParseEnabled: true}
			var existing models.Settings
			if err := database.DB.First(&existing).Error; err == nil {
				settings.ID = existing.ID
			}
			if err := database.DB.Save(&settings).Error; err != nil {
				t.Fatal(err)
			}
			textAI := services.NewTextAIService()
			videos := services.NewVideoService(nil)
			a := &App{ctx: context.Background(), textAI: textAI, videoService: videos, libraryQueries: services.NewLibraryQueryService(videos, textAI), settingsService: &services.SettingsService{}, idleGate: services.NewIdleGate()}
			t.Cleanup(func() { a.stopFunctionalPolish(false) })
			writer := log.Writer()
			defer log.SetOutput(writer)
			status := a.GetTextAIStatus()
			if status.Status != "ok" {
				t.Fatal(status.Error)
			}
			requestID := uuid.NewString()
			done := make(chan services.LibraryQueryDraftResult, 1)
			go func() {
				done <- a.ParseLibraryQuery(services.ParseLibraryQueryRequest{RequestID: requestID, Text: "未看", Mode: "external", ProviderRevision: status.Data.ProviderRevision})
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("request not started")
			}
			if action == "cancel" {
				result := a.CancelLibraryQuery(requestID)
				if result.Status != "ok" || result.Data.Status != "cancelled" {
					t.Fatalf("cancel %+v", result)
				}
			} else {
				settings.QueryParseEnabled = false
				if err := a.UpdateSettings(settings); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case result := <-done:
				if result.Status != "error" || result.Error == nil || result.Error.Code != "cancelled" {
					t.Fatalf("late result %+v", result)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("App cancellation did not reach request")
			}
		})
	}
}
