package services

import (
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestCollectionRevisionTracksMemberAndEpisodeChanges(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewCollectionService(t.TempDir())
	collection, err := svc.CreateCollection("revision", "")
	if err != nil {
		t.Fatal(err)
	}
	read := func() int64 {
		var state models.CollectionEpisodeRevision
		if err := database.DB.First(&state, "collection_id = ?", collection.ID).Error; err != nil {
			t.Fatal(err)
		}
		return state.Revision
	}
	if read() != 1 {
		t.Fatal("new collection has no initial revision")
	}
	video := models.Video{Name: "show.S01E01.mp4", Path: "/revision/S01E01.mp4"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	previous := read()
	if err := svc.AddCollectionVideos(collection.ID, []uint{video.ID}); err != nil {
		t.Fatal(err)
	}
	if read() <= previous {
		t.Fatal("member append did not advance revision")
	}
	previous = read()
	season, episode := 1, 2
	if err := svc.SetEpisodeAssignments(collection.ID, []CollectionEpisodeEdit{{VideoID: video.ID, Season: &season, Episode: &episode}}); err != nil {
		t.Fatal(err)
	}
	if read() <= previous {
		t.Fatal("episode correction did not advance revision")
	}
	previous = read()
	if err := svc.SetSeasonExpectation(collection.ID, 1, 3, 0); err != nil {
		t.Fatal(err)
	}
	if read() <= previous {
		t.Fatal("season expectation did not advance revision")
	}
	previous = read()
	if err := svc.RemoveCollectionVideo(collection.ID, video.ID); err != nil {
		t.Fatal(err)
	}
	if read() <= previous {
		t.Fatal("member removal did not advance revision")
	}
}
