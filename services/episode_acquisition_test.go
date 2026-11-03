package services

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"
)

func episodeDownloadFixture(t *testing.T) (*BTService, *CollectionService, models.MediaCollection, BTTaskView, EpisodeDownloadRequest) {
	t.Helper()
	bt, _ := newBTTaskTest(t)
	collections := NewCollectionService(t.TempDir())
	collection, err := collections.CreateCollection("Example Series", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := collections.SetSeasonExpectation(collection.ID, 1, 2, 0); err != nil {
		t.Fatal(err)
	}
	source, err := collections.PrepareEpisodeResourceSearch(context.Background(), EpisodeResourceSearchRequest{CollectionID: collection.ID, Season: 1, Episodes: []int{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	draft := btPreviewTest(t, bt)
	request := EpisodeDownloadRequest{DraftID: draft.ID, ExpectedRevision: draft.Revision, SelectedFileIDs: []string{draft.Files[0].ID}, OriginToken: source.OriginToken}
	return bt, collections, *collection, draft, request
}
func episodeImportedFixture(t *testing.T) (*CollectionService, models.MediaCollection, models.EpisodeAcquisitionOrigin, models.Video) {
	t.Helper()
	bt, collections, collection, _, request := episodeDownloadFixture(t)
	if _, err := bt.SubmitEpisodeDownload(context.Background(), collections, request); err != nil {
		t.Fatal(err)
	}
	var origin models.EpisodeAcquisitionOrigin
	if err := database.DB.First(&origin).Error; err != nil {
		t.Fatal(err)
	}
	var task models.BTTask
	var file models.BTFile
	if err := database.DB.First(&task, "id = ?", origin.TaskID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.First(&file, "task_id = ? AND manifest_file_id = ?", origin.TaskID, origin.FileID).Error; err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(task.SaveRoot, filepath.FromSlash(file.RelativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path), Size: info.Size()}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ?", origin.TaskID, origin.FileID).Updates(map[string]any{"video_id": video.ID, "import_state": "imported", "publication_state": BTImportPublicationDone, "progress": 1, "eligibility_revision": 1, "observed_size": info.Size(), "observed_mtime": info.ModTime().UnixNano()}).Error; err != nil {
		t.Fatal(err)
	}
	return collections, collection, origin, video
}
func associationRequest(origin models.EpisodeAcquisitionOrigin, episode int) EpisodeAssociationRequest {
	return EpisodeAssociationRequest{TaskID: origin.TaskID, Items: []EpisodeAssociationChoice{{FileID: origin.FileID, OriginID: origin.ID, ExpectedOriginRevision: origin.Revision, Season: 1, Episode: episode}}}
}

func TestEpisodeDownloadOriginIsAtomicAndIdempotent(t *testing.T) {
	bt, collections, collection, _, request := episodeDownloadFixture(t)
	ctx := context.Background()
	bad := request
	bad.FileEpisodeChoices = []EpisodeFileChoice{{FileID: "not-selected", Season: 1, Episode: 1}}
	if _, err := bt.SubmitEpisodeDownload(ctx, collections, bad); err == nil {
		t.Fatal("accepted unselected mapping")
	}
	var count int64
	database.DB.Model(&models.EpisodeAcquisitionOrigin{}).Count(&count)
	if count != 0 {
		t.Fatal("partial origin persisted")
	}
	var task models.BTTask
	database.DB.First(&task, "id = ?", request.DraftID)
	if task.Stage != "draft" {
		t.Fatalf("failed origin changed download intent: %s", task.Stage)
	}
	for range 2 {
		if _, err := bt.SubmitEpisodeDownload(ctx, collections, request); err != nil {
			t.Fatal(err)
		}
	}
	database.DB.Model(&models.EpisodeAcquisitionOrigin{}).Count(&count)
	if count != 1 {
		t.Fatalf("duplicate origins %d", count)
	}
	page, err := collections.ListEpisodeAcquisitions(ctx, EpisodeAcquisitionRequest{CollectionID: collection.ID})
	if err != nil || len(page.Items) != 1 || page.Items[0].State != "selected" {
		t.Fatalf("%+v %v", page, err)
	}
}
func TestEpisodeAssociationFillsUnmappedFileAndPreservesManualChoice(t *testing.T) {
	collections, collection, origin, video := episodeImportedFixture(t)
	ctx := context.Background()
	preview, err := collections.PreviewEpisodeAssociation(ctx, associationRequest(origin, 1))
	if err != nil || preview.PreviewToken == "" || preview.Conflicts != 0 {
		t.Fatalf("%+v %v", preview, err)
	}
	for range 2 {
		result, err := collections.ConfirmEpisodeAssociation(ctx, preview.PreviewToken)
		if err != nil || result.LinkedCount != 1 {
			t.Fatalf("%+v %v", result, err)
		}
	}
	var mapping models.CollectionEpisodeAssignment
	if err := database.DB.First(&mapping, "collection_id = ? AND video_id = ?", collection.ID, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	if *mapping.Season != 1 || *mapping.Episode != 1 {
		t.Fatalf("%+v", mapping)
	}
	page, err := collections.ListEpisodeAcquisitions(ctx, EpisodeAcquisitionRequest{TaskID: origin.TaskID})
	if err != nil || page.Items[0].State != "linked" {
		t.Fatalf("%+v %v", page, err)
	}
	database.DB.First(&origin, "id = ?", origin.ID)
	preview, err = collections.PreviewEpisodeAssociation(ctx, associationRequest(origin, 2))
	if err != nil || preview.Conflicts != 1 || preview.PreviewToken != "" {
		t.Fatalf("manual assignment overwritten: %+v %v", preview, err)
	}
}
func TestEpisodeAssociationAlreadyFilledAfterPreviewLeavesNewFileUnlinked(t *testing.T) {
	collections, collection, origin, video := episodeImportedFixture(t)
	ctx := context.Background()
	preview, err := collections.PreviewEpisodeAssociation(ctx, associationRequest(origin, 1))
	if err != nil {
		t.Fatal(err)
	}
	other := models.Video{Name: "Example.S01E01.mkv", Path: filepath.Join(filepath.Dir(video.Path), "other.mkv"), Directory: filepath.Dir(video.Path)}
	if err := database.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := collections.AddCollectionVideos(collection.ID, []uint{other.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := collections.ConfirmEpisodeAssociation(ctx, preview.PreviewToken); !errors.Is(err, ErrEpisodeAlreadyFilled) {
		t.Fatalf("expected already filled: %v", err)
	}
	var count int64
	database.DB.Model(&models.CollectionVideo{}).Where("collection_id = ? AND video_id = ?", collection.ID, video.ID).Count(&count)
	if count != 0 {
		t.Fatal("new video linked despite competing completion")
	}
	if _, err := os.Stat(video.Path); err != nil {
		t.Fatal("downloaded file was removed")
	}
	preview, err = collections.PreviewEpisodeAssociation(ctx, associationRequest(origin, 1))
	if err != nil || preview.PreviewToken != "" || preview.Items[0].Conflict != "episode_already_filled" {
		t.Fatalf("%+v %v", preview, err)
	}
}
func TestEpisodeSourceChecksPhysicalFileAndDeletedCollection(t *testing.T) {
	collections, collection, origin, video := episodeImportedFixture(t)
	ctx := context.Background()
	if err := os.WriteFile(video.Path, []byte("changed bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	preview, err := collections.PreviewEpisodeAssociation(ctx, associationRequest(origin, 1))
	if err != nil || preview.PreviewToken != "" || preview.Conflicts != 1 {
		t.Fatalf("changed file accepted %+v %v", preview, err)
	}
	if err := collections.DeleteCollection(collection.ID); err != nil {
		t.Fatal(err)
	}
	page, err := collections.ListEpisodeAcquisitions(ctx, EpisodeAcquisitionRequest{TaskID: origin.TaskID})
	if err != nil || page.Items[0].State != "detached" {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestEpisodeOwnerCancellationAndReceiptIsolation(t *testing.T) {
	collections, _, origin, video := episodeImportedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	preview, err := collections.PreviewEpisodeAssociation(ctx, associationRequest(origin, 1))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := collections.ConfirmEpisodeAssociation(context.Background(), preview.PreviewToken); !errors.Is(err, ErrEpisodeContextChanged) {
		t.Fatal(err)
	}
	preview, err = collections.PreviewEpisodeAssociation(context.Background(), associationRequest(origin, 1))
	if err != nil {
		t.Fatal(err)
	}
	result, err := collections.ConfirmEpisodeAssociation(context.Background(), preview.PreviewToken)
	if err != nil {
		t.Fatal(err)
	}
	result.VideoIDs[0] = 999
	result.LinkedCount = 999
	again, err := collections.ConfirmEpisodeAssociation(context.Background(), preview.PreviewToken)
	if err != nil || again.LinkedCount != 1 || again.VideoIDs[0] != video.ID {
		t.Fatalf("receipt alias: %+v %v", again, err)
	}
}

func TestEpisodeManualRevisionChangeRejectsWholeConfirmation(t *testing.T) {
	collections, collection, origin, _ := episodeImportedFixture(t)
	preview, err := collections.PreviewEpisodeAssociation(context.Background(), associationRequest(origin, 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := collections.SetSeasonExpectation(collection.ID, 1, 3, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := collections.ConfirmEpisodeAssociation(context.Background(), preview.PreviewToken); !errors.Is(err, ErrEpisodeContextChanged) {
		t.Fatal(err)
	}
	var count int64
	if err := database.DB.Model(&models.CollectionVideo{}).Where("collection_id = ?", collection.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("partial membership: %d %v", count, err)
	}
}

func TestEpisodeSourceListDoesNotClaimRetiredOrTerminalTransfers(t *testing.T) {
	bt, collections, collection, _, request := episodeDownloadFixture(t)
	if _, err := bt.SubmitEpisodeDownload(context.Background(), collections, request); err != nil {
		t.Fatal(err)
	}
	var task models.BTTask
	if err := database.DB.First(&task, "id = ?", request.DraftID).Error; err != nil {
		t.Fatal(err)
	}
	read := func() EpisodeAcquisitionItem {
		t.Helper()
		page, err := collections.ListEpisodeAcquisitions(context.Background(), EpisodeAcquisitionRequest{CollectionID: collection.ID})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("%+v %v", page, err)
		}
		return page.Items[0]
	}
	for _, stage := range []string{"released", "cancelled", "abandoned", "missing", "conflict", "uncertain"} {
		if err := database.DB.Model(&task).Update("stage", stage).Error; err != nil {
			t.Fatal(err)
		}
		item := read()
		if item.State == "transferring" || item.Reason == "" {
			t.Fatalf("terminal claim: %+v", item)
		}
	}
	if err := database.DB.Model(&task).Update("stage", "active").Error; err != nil {
		t.Fatal(err)
	}
	if item := read(); item.State != "transferring" {
		t.Fatal(item)
	}
	if err := database.DB.Model(&models.BTConnection{}).Where("id = ?", task.ConnectionID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if item := read(); item.State == "transferring" || item.Reason == "" {
		t.Fatal(item)
	}
}

func TestEpisodeSearchDoesNotTreatOfflineMembersAsMissing(t *testing.T) {
	_, service, collection, _, _ := episodeDownloadFixture(t)
	video := models.Video{Name: "Example Series S01E01.mkv", Path: "/offline/example1.mkv", Directory: "/offline", IsStale: true}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.AddCollectionVideo(collection.ID, video.ID); err != nil {
		t.Fatal(err)
	}
	for _, episode := range []int{1, 3} {
		_, err := service.PrepareEpisodeResourceSearch(context.Background(), EpisodeResourceSearchRequest{CollectionID: collection.ID, Season: 1, Episodes: []int{episode}})
		if !errors.Is(err, ErrEpisodeContextChanged) {
			t.Fatalf("searched nonmissing episode %d: %v", episode, err)
		}
	}
	if _, err := service.PrepareEpisodeResourceSearch(context.Background(), EpisodeResourceSearchRequest{CollectionID: collection.ID, Season: 1, Episodes: []int{2}}); err != nil {
		t.Fatal(err)
	}
}

func TestEpisodeSubmitReceiptSurvivesPostCommitReadFailure(t *testing.T) {
	bt, service, _, _, request := episodeDownloadFixture(t)
	db := database.DB
	originWritten := false
	if err := db.Callback().Create().After("gorm:create").Register("episode_fixture:origin", func(tx *gorm.DB) {
		if tx.Statement.Table == "episode_acquisition_origins" && tx.Error == nil {
			originWritten = true
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register("episode_fixture:read", func(tx *gorm.DB) {
		if originWritten && tx.Statement.Table == "bt_tasks" {
			tx.AddError(errors.New("fixture postcommit read failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Callback().Create().Remove("episode_fixture:origin")
		db.Callback().Query().Remove("episode_fixture:read")
	})
	if _, err := bt.SubmitEpisodeDownload(context.Background(), service, request); err == nil {
		t.Fatal("expected read failure")
	}
	db.Callback().Create().Remove("episode_fixture:origin")
	db.Callback().Query().Remove("episode_fixture:read")
	result, err := bt.SubmitEpisodeDownload(context.Background(), service, request)
	if err != nil || result.Stage != "queued_add" || result.Revision != request.ExpectedRevision+1 {
		t.Fatalf("lost committed receipt: %+v %v", result, err)
	}
	var count int64
	if err := db.Model(&models.EpisodeAcquisitionOrigin{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicated source %d %v", count, err)
	}
}
