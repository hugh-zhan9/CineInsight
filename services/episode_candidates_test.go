package services

import (
	"context"
	"errors"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/btclient"
)

func TestEpisodeCandidateHintsSeparateTitleFromLibraryAndOwnedDownload(t *testing.T) {
	bt, collections, collection, _, request := episodeDownloadFixture(t)
	bt.search.mu.Lock()
	for token, title := range map[string]string{"single": "Example Series S01E01", "multi": "Example Series S01E01-E02", "unknown": "Example Series E01"} {
		bt.search.tokens[token] = btSearchToken{release: btclient.Release{Title: title}, generation: bt.search.generation, expires: time.Now().Add(time.Minute)}
	}
	bt.search.mu.Unlock()
	input := EpisodeCandidatesRequest{OriginToken: request.OriginToken, ResultTokens: []string{"single", "multi", "unknown"}}
	page, err := bt.DescribeEpisodeCandidates(context.Background(), collections, input)
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].Hint != "标题提示：可能包含第1季第1集，作品归属待核对" || page.Items[0].LibraryStatus != "" || page.Items[0].DownloadStatus != "" {
		t.Fatalf("not honest hints: %+v", page.Items)
	}
	for _, item := range page.Items[1:] {
		if item.Hint != "季集待核对：标题不足以确认单集" {
			t.Fatalf("ambiguous title inferred: %+v", item)
		}
	}
	request.FileEpisodeChoices = []EpisodeFileChoice{{FileID: request.SelectedFileIDs[0], Season: 1, Episode: 1}}
	if _, err := bt.SubmitEpisodeDownload(context.Background(), collections, request); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.BTTask{}).Where("id = ?", request.DraftID).Updates(map[string]any{"stage": "active", "pending_action": "", "unresolved_write": false}).Error; err != nil {
		t.Fatal(err)
	}
	page, err = bt.DescribeEpisodeCandidates(context.Background(), collections, input)
	if err != nil || page.Items[0].DownloadStatus == "" {
		t.Fatalf("owned mapped download missing: %+v %v", page, err)
	}
	var task models.BTTask
	if err := database.DB.First(&task, "id = ?", request.DraftID).Error; err != nil {
		t.Fatal(err)
	}
	for _, updates := range []map[string]any{{"slot": "retired:" + task.ConnectionID, "enabled": true}, {"slot": "local", "enabled": false}} {
		if err := database.DB.Model(&models.BTConnection{}).Where("id = ?", task.ConnectionID).Updates(updates).Error; err != nil {
			t.Fatal(err)
		}
		page, err = bt.DescribeEpisodeCandidates(context.Background(), collections, input)
		if err != nil || page.Items[0].DownloadStatus != "" {
			t.Fatalf("retired/disabled claimed managed: %+v %v", page, err)
		}
	}
	if err := database.DB.Model(&models.BTConnection{}).Where("id = ?", task.ConnectionID).Updates(map[string]any{"slot": "local", "enabled": true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.BTTask{}).Where("id = ?", request.DraftID).Update("stage", "released").Error; err != nil {
		t.Fatal(err)
	}
	page, err = bt.DescribeEpisodeCandidates(context.Background(), collections, input)
	if err != nil || page.Items[0].DownloadStatus != "" {
		t.Fatalf("released claimed managed: %+v %v", page, err)
	}
	// Membership may change after a search. Annotations show current local facts;
	// the original download token still rejects a stale submission independently.
	video := models.Video{Name: "Example Series S01E01.mkv", Path: "/offline/example.mkv", Directory: "/offline"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := collections.AddCollectionVideos(collection.ID, []uint{video.ID}); err != nil {
		t.Fatal(err)
	}
	page, err = bt.DescribeEpisodeCandidates(context.Background(), collections, input)
	if err != nil || page.Items[0].LibraryStatus == "" {
		t.Fatalf("explicit member missing: %+v %v", page, err)
	}
}
func TestEpisodeCandidateRejectsExpiredAndDuplicateResources(t *testing.T) {
	bt, collections, _, _, request := episodeDownloadFixture(t)
	_, err := bt.DescribeEpisodeCandidates(context.Background(), collections, EpisodeCandidatesRequest{OriginToken: request.OriginToken, ResultTokens: []string{"missing"}})
	if !errors.Is(err, ErrBTResultExpired) {
		t.Fatal(err)
	}
	_, err = bt.DescribeEpisodeCandidates(context.Background(), collections, EpisodeCandidatesRequest{OriginToken: "expired", ResultTokens: []string{"missing"}})
	if !errors.Is(err, ErrEpisodeContextChanged) {
		t.Fatal(err)
	}
}
