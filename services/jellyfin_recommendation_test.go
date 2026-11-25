package services

import (
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"video-master/database"
	"video-master/models"
)

const fileballRecommendationPath = "/Users/" + jellyfinUserID + "/Items?EnableImages=false&EnableTotalRecordCount=false&ImageTypeLimit=0&IncludeItemTypes=Movie,Series,MusicArtist&Limit=3&Recursive=true&SortBy=IsFavoriteOrLiked,Random"

func fileballRecommendationIDs(t *testing.T, s *JellyfinServer, token, filters string, wantTotal int) []string {
	t.Helper()
	items, total := jellyfinItems(t, jellyfinRequest(s, "GET", fileballRecommendationPath+filters, token, ""))
	if total != wantTotal || len(items) != min(3, wantTotal) {
		t.Fatalf("got %d recommendations out of %d, want %d out of %d", len(items), total, min(3, wantTotal), wantTotal)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		id := item["Id"].(string)
		if slices.Contains(ids, id) {
			t.Fatalf("duplicate recommendation %s in one batch", id)
		}
		ids = append(ids, id)
	}
	return ids
}

func addFileballRecommendationVideo(t *testing.T, dir, name string) models.Video {
	t.Helper()
	video := models.Video{Name: name, Directory: dir, Path: filepath.Join(dir, name)}
	mustCreateFile(t, video.Path)
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	return video
}

func TestJellyfinFileballRecommendationsWithoutReplacement(t *testing.T) {
	s, token, first, second := jellyfinLibraryFixture(t)
	unseen := map[string]bool{jellyfinID(jellyVideo, first.ID): true, jellyfinID(jellyVideo, second.ID): true}
	for i := range 5 {
		video := addFileballRecommendationVideo(t, first.Directory, fmt.Sprintf("candidate-%d.mp4", i))
		unseen[jellyfinID(jellyVideo, video.ID)] = true
	}
	firstBatch := fileballRecommendationIDs(t, s, token, "", 7)
	s.Stop()
	s = jellyfinTestServer(t)
	secondBatch := fileballRecommendationIDs(t, s, token, "", 7)
	for _, id := range append(firstBatch, secondBatch...) {
		if !unseen[id] {
			t.Fatalf("video %s was repeated before the initial round ended, including across restart", id)
		}
		delete(unseen, id)
	}

	refill := fileballRecommendationIDs(t, s, token, "", 7)
	if !unseen[refill[0]] {
		t.Fatalf("last unseen video %v should come before refill: %v", unseen, refill)
	}
	for _, id := range fileballRecommendationIDs(t, s, token, "", 7) {
		if slices.Contains(refill, id) {
			t.Fatalf("video %s from the refill batch was immediately repeated", id)
		}
	}
}

func TestJellyfinFileballRecommendationsExhaustionAndVisibility(t *testing.T) {
	s, token, first, second := jellyfinLibraryFixture(t)
	root := first.Directory
	private := filepath.Join(root, "private")
	addFileballRecommendationVideo(t, private, "blacklisted.mp4")
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", private).Error; err != nil {
		t.Fatal(err)
	}
	hidden := addFileballRecommendationVideo(t, root, "hidden.mp4")
	if err := database.DB.Delete(&hidden).Error; err != nil {
		t.Fatal(err)
	}
	stale := addFileballRecommendationVideo(t, root, "stale.mp4")
	if err := database.DB.Model(&stale).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	if w := jellyfinRequest(s, "GET", fileballRecommendationPath+"&SortOrder=Sideways", token, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid random SortOrder returned %d", w.Code)
	}

	want := []string{jellyfinID(jellyVideo, first.ID), jellyfinID(jellyVideo, second.ID)}
	slices.Sort(want)
	for range 2 {
		got := fileballRecommendationIDs(t, s, token, "", 2)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("visible recommendations = %v, want %v", got, want)
		}
	}
	added := addFileballRecommendationVideo(t, root, "new.mp4")
	got := fileballRecommendationIDs(t, s, token, "", 3)
	if got[0] != jellyfinID(jellyVideo, added.ID) {
		t.Fatalf("newly added video should come before previously seen videos: %v", got)
	}
}

func TestJellyfinFileballRecommendationsResetOnlyRequestedScope(t *testing.T) {
	s, token, tagged, outside := jellyfinLibraryFixture(t)
	tag := models.Tag{Name: "narrow"}
	if err := database.DB.Model(&tagged).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	fileballRecommendationIDs(t, s, token, "", 2)
	got := fileballRecommendationIDs(t, s, token, "&ParentId="+jellyfinID(jellyTag, tag.ID), 1)
	if got[0] != jellyfinID(jellyVideo, tagged.ID) {
		t.Fatalf("recommendation left the requested tag scope: %v", got)
	}
	var history models.JellyfinRecommendationHistory
	if err := database.DB.Where("video_id = ?", outside.ID).First(&history).Error; err != nil {
		t.Fatalf("resetting the tag scope lost history for video %d outside it: %v", outside.ID, err)
	}
}
