package services

import (
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestViewingDiaryConcurrentClaimsAcrossDedupInstances(t *testing.T) {
	video, _ := notesFixture(t)
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := newViewEventDedup(1).record(video.ID, PlayEventSourceInlineView, "same-fact", time.Now())
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	mustNoteCount(t, &models.PlayEvent{}, 1)
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 1)
	mobile := NewShortFeedService(&VideoService{})
	errors = make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := mobile.RecordPlaybackSession(videoRef(video.ID), "same-mobile")
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	mustNoteCount(t, &models.PlayEvent{}, 2)
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 2)
	var current models.Video
	if err := database.DB.First(&current, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.RandomPlayCount != 1 {
		t.Fatalf("mobile duplicate increased count: %d", current.RandomPlayCount)
	}
}
