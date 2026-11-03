package services

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"
)

func secondEpisodeFile(t *testing.T, first models.EpisodeAcquisitionOrigin) models.EpisodeAcquisitionOrigin {
	t.Helper()
	var task models.BTTask
	if err := database.DB.First(&task, "id = ?", first.TaskID).Error; err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(task.SaveRoot, "second.mp4")
	if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "second.mp4", Path: path, Directory: task.SaveRoot, Size: 4}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	key := btDigest([]byte("second.mp4"))
	file := models.BTFile{TaskID: task.ID, ManifestFileID: key, Position: 10, PathKey: &key, RelativePath: "second.mp4", Selectable: true, Selected: true, Size: 4, Progress: 1, EligibilityRevision: 1, ObservedSize: 4, ObservedMtime: info.ModTime().UnixNano(), ImportState: "imported", PublicationState: BTImportPublicationDone, VideoID: &video.ID}
	if err := database.DB.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	origin := first
	origin.ID = uuid.NewString()
	origin.FileID = key
	if err := database.DB.Create(&origin).Error; err != nil {
		t.Fatal(err)
	}
	return origin
}
func TestEpisodeConcurrentConfirmOnlyOneNewFileOccupiesEpisode(t *testing.T) {
	first, collection, origin, _ := episodeImportedFixture(t)
	secondOrigin := secondEpisodeFile(t, origin)
	second := NewCollectionService(t.TempDir())
	one, err := first.PreviewEpisodeAssociation(context.Background(), associationRequest(origin, 1))
	if err != nil {
		t.Fatal(err)
	}
	two, err := second.PreviewEpisodeAssociation(context.Background(), associationRequest(secondOrigin, 1))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, pair := range []struct {
		service *CollectionService
		token   string
	}{{first, one.PreviewToken}, {second, two.PreviewToken}} {
		go func(service *CollectionService, token string) {
			<-start
			_, err := service.ConfirmEpisodeAssociation(context.Background(), token)
			results <- err
		}(pair.service, pair.token)
	}
	close(start)
	success := 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if !errors.Is(err, ErrEpisodeAlreadyFilled) && !errors.Is(err, ErrCollectionOrderConflict) && !errors.Is(err, ErrEpisodeContextChanged) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("accepted %d competing new files", success)
	}
	var count int64
	if err := database.DB.Model(&models.CollectionVideo{}).Where("collection_id = ?", collection.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("membership: %d %v", count, err)
	}
}
func TestEpisodeBatchDuplicateTargetCannotPartiallyAssociate(t *testing.T) {
	service, collection, origin, _ := episodeImportedFixture(t)
	other := secondEpisodeFile(t, origin)
	request := associationRequest(origin, 1)
	request.Items = append(request.Items, associationRequest(other, 1).Items[0])
	preview, err := service.PreviewEpisodeAssociation(context.Background(), request)
	if err != nil || preview.Conflicts != 1 || preview.PreviewToken != "" {
		t.Fatalf("duplicate target preview %+v %v", preview, err)
	}
	var count int64
	if err := database.DB.Model(&models.CollectionVideo{}).Where("collection_id = ?", collection.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("preview wrote membership: %d %v", count, err)
	}
}

func TestEpisodeChangedSecondFileRollsBackWholeAssociation(t *testing.T) {
	service, collection, origin, _ := episodeImportedFixture(t)
	other := secondEpisodeFile(t, origin)
	request := associationRequest(origin, 1)
	request.Items = append(request.Items, associationRequest(other, 2).Items[0])
	preview, err := service.PreviewEpisodeAssociation(context.Background(), request)
	if err != nil || preview.Conflicts != 0 || preview.PreviewToken == "" {
		t.Fatalf("preview %+v %v", preview, err)
	}
	if err := database.DB.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ?", other.TaskID, other.FileID).Update("eligibility_revision", 2).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmEpisodeAssociation(context.Background(), preview.PreviewToken); !errors.Is(err, ErrEpisodeContextChanged) {
		t.Fatal(err)
	}
	var count int64
	if err := database.DB.Model(&models.CollectionVideo{}).Where("collection_id = ?", collection.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("partial membership: %d %v", count, err)
	}
	var unchanged models.EpisodeAcquisitionOrigin
	if err := database.DB.First(&unchanged, "id = ?", origin.ID).Error; err != nil || unchanged.Revision != origin.Revision || unchanged.ConfirmedMapping {
		t.Fatalf("partial source write: %+v %v", unchanged, err)
	}
}
