package services

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestLocalMetadataServiceExportsDatabaseFieldsAndOrderedCollection(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}
	rating := 9.5
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, DisplayTitle: "片库标题", PersonalRating: &rating}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	tag := models.Tag{Name: "剧情", Color: "#fff"}
	person := models.Person{DisplayName: "演员甲"}
	first := models.MediaCollection{Name: "第一作品集", NormalizedName: "第一作品集"}
	second := models.MediaCollection{Name: "第二作品集", NormalizedName: "第二作品集"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&video).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.VideoPerson{VideoID: video.ID, PersonID: person.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.CollectionVideo{VideoID: video.ID, CollectionID: second.ID, Position: 2}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.CollectionVideo{VideoID: video.ID, CollectionID: first.ID, Position: 1}).Error; err != nil {
		t.Fatal(err)
	}

	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	result, err := service.ExportVideoNFO(context.Background(), video.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("multiple collections should produce a warning: %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(root, "movie.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	var exported exportedMovieNFO
	if err := xml.Unmarshal(content, &exported); err != nil {
		t.Fatal(err)
	}
	if exported.Title != "片库标题" || exported.UserRating != "9.5" || len(exported.Tags) != 1 || exported.Tags[0] != "剧情" || len(exported.Actors) != 1 || exported.Actors[0].Name != "演员甲" || exported.Set.Name != "第一作品集" {
		t.Fatalf("unexpected exported fields: %#v", exported)
	}
}

func TestLocalMetadataExportBatchUsesCurrentFilterAndCanCancel(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	tag := models.Tag{Name: "筛选标签", Color: "#fff"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	matched := createShortFeedVideo(t, root, "matched.mp4", 60, false, &tag)
	createShortFeedVideo(t, root, "other.mp4", 60, false)

	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	var mu sync.Mutex
	processed := []uint{}
	service.exportProcess = func(_ context.Context, videoID uint) (*LocalMetadataNFOExportResult, error) {
		mu.Lock()
		processed = append(processed, videoID)
		mu.Unlock()
		return &LocalMetadataNFOExportResult{}, nil
	}
	if _, err := service.StartExport(context.Background(), LocalMetadataExportRequest{Filter: LibraryFilter{TagIDs: []uint{tag.ID}}}); err != nil {
		t.Fatal(err)
	}
	waitForLocalMetadataExport(t, service)
	mu.Lock()
	if len(processed) != 1 || processed[0] != matched.ID {
		t.Fatalf("batch ignored current filter: %#v", processed)
	}
	mu.Unlock()

	started := make(chan struct{})
	service.exportProcess = func(ctx context.Context, _ uint) (*LocalMetadataNFOExportResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, err := service.StartExport(context.Background(), LocalMetadataExportRequest{}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := service.CancelExport(); err != nil {
		t.Fatal(err)
	}
	waitForLocalMetadataExport(t, service)
	if status := service.ExportStatus(); !status.Cancelled || !status.Completed || status.Running {
		t.Fatalf("unexpected cancelled status: %#v", status)
	}
}

func TestLocalMetadataExportReportsTruncatedFailuresBeyondLimit(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	extra := 3
	total := localMetadataFailureLimit + extra
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("v%03d.mp4", i)
		video := models.Video{Name: name, Path: filepath.Join(root, name), Directory: root}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	service.exportProcess = func(context.Context, uint) (*LocalMetadataNFOExportResult, error) {
		return nil, errors.New("写出失败")
	}
	if _, err := service.StartExport(context.Background(), LocalMetadataExportRequest{}); err != nil {
		t.Fatal(err)
	}
	waitForLocalMetadataExport(t, service)
	status := service.ExportStatus()
	if status.Failed != total || len(status.Failures) != localMetadataFailureLimit || status.FailuresTruncated != extra {
		t.Fatalf("truncation not reported: failed=%d listed=%d truncated=%d", status.Failed, len(status.Failures), status.FailuresTruncated)
	}
}

func TestLocalMetadataExportStopThenRestart(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	createShortFeedVideo(t, root, "movie.mp4", 60, false)
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	started := make(chan struct{})
	service.exportProcess = func(ctx context.Context, _ uint) (*LocalMetadataNFOExportResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, err := service.StartExport(context.Background(), LocalMetadataExportRequest{}); err != nil {
		t.Fatal(err)
	}
	<-started
	service.StopExport()
	if status := service.ExportStatus(); status.Running {
		t.Fatalf("StopExport should wait for the worker to exit: %#v", status)
	}
	service.exportProcess = func(context.Context, uint) (*LocalMetadataNFOExportResult, error) {
		return &LocalMetadataNFOExportResult{}, nil
	}
	if _, err := service.StartExport(context.Background(), LocalMetadataExportRequest{}); err != nil {
		t.Fatal(err)
	}
	waitForLocalMetadataExport(t, service)
	if status := service.ExportStatus(); !status.Completed || status.Cancelled || status.Succeeded != 1 {
		t.Fatalf("restart after stop should complete: %#v", status)
	}
}

func waitForLocalMetadataExport(t *testing.T, service *LocalMetadataService) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !service.ExportStatus().Running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("local metadata export did not finish")
}
