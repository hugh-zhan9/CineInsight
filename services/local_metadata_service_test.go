package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

type rejectingMetadataTransport struct{}

func (rejectingMetadataTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network access is forbidden")
}

func TestDiscoverLocalMetadataSourcesUsesStablePriorityAndBlocksEscapingSymlinks(t *testing.T) {
	root := t.TempDir()
	videoPath := filepath.Join(root, "Movie.MP4")
	mustCreateFile(t, videoPath)
	for _, name := range []string{"Movie.nfo", "Movie-poster.JPG", "poster.png", "fanart.webp", "Movie-fanart.jpeg"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0644); err != nil {
			t.Fatalf("write source %s: %v", name, err)
		}
	}
	outside := filepath.Join(t.TempDir(), "cover.jpg")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatalf("write outside source: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "cover.jpg")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}

	sources, err := discoverLocalMetadataSources(videoPath)
	if err != nil {
		t.Fatalf("discover sources: %v", err)
	}
	if sources.nfo == nil || sources.nfo.name != "Movie.nfo" {
		t.Fatalf("NFO selection = %#v", sources.nfo)
	}
	if sources.poster == nil || sources.poster.name != "Movie-poster.JPG" {
		t.Fatalf("poster selection = %#v", sources.poster)
	}
	if sources.fanart == nil || sources.fanart.name != "Movie-fanart.jpeg" {
		t.Fatalf("fanart selection = %#v", sources.fanart)
	}
	if len(sources.warnings) < 3 {
		t.Fatalf("expected duplicate and symlink warnings: %#v", sources.warnings)
	}
	first, err := localMetadataManifest(sources.manifest)
	if err != nil {
		t.Fatalf("build first manifest: %v", err)
	}
	second, err := localMetadataManifest(sources.manifest)
	if err != nil || first != second || len(first) != 64 {
		t.Fatalf("manifest first=%q second=%q err=%v", first, second, err)
	}
}

func TestLocalMetadataDiffMapsEntitiesWithoutNetworkOrPathLeakage(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, videoPath)
	nfo := `<movie><title>Imported</title><originaltitle>Original</originaltitle><plot>Plot</plot>` +
		`<actor><name>Existing Person</name><thumb>https://example.invalid/person.jpg</thumb></actor>` +
		`<actor><name>New Person</name></actor><set><name>Existing Set</name></set>` +
		`<thumb>https://example.invalid/poster.jpg</thumb></movie>`
	if err := os.WriteFile(filepath.Join(root, "movie.nfo"), []byte(nfo), 0644); err != nil {
		t.Fatalf("write NFO: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "poster.jpg"), []byte("poster"), 0644); err != nil {
		t.Fatalf("write poster: %v", err)
	}
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, DisplayTitle: "Manual"}
	person := models.Person{DisplayName: "Existing Person"}
	collection := models.MediaCollection{Name: "Existing Set", NormalizedName: "existing set"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatalf("create person: %v", err)
	}
	if err := database.DB.Create(&collection).Error; err != nil {
		t.Fatalf("create collection: %v", err)
	}

	previousTransport := http.DefaultTransport
	http.DefaultTransport = rejectingMetadataTransport{}
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	diff, err := service.GetDiff(video.ID)
	if err != nil {
		t.Fatalf("get metadata diff: %v", err)
	}
	if diff.Title.ChangeType != "overwrite" || diff.Title.DefaultSelected || !diff.Title.RequiresOverwrite {
		t.Fatalf("title diff = %#v", diff.Title)
	}
	if diff.OriginalTitle.ChangeType != "fill" || !diff.OriginalTitle.DefaultSelected {
		t.Fatalf("original title diff = %#v", diff.OriginalTitle)
	}
	if len(diff.People.Source) != 2 || diff.People.Source[0].DefaultMode != "existing" || diff.People.Source[0].DefaultEntityID != person.ID || diff.People.Source[1].DefaultMode != "create_new" {
		t.Fatalf("people candidates = %#v", diff.People.Source)
	}
	if len(diff.Collection.Source) != 1 || diff.Collection.Source[0].DefaultEntityID != collection.ID {
		t.Fatalf("collection candidates = %#v", diff.Collection.Source)
	}
	if diff.Poster.SourceName != "poster.jpg" || !diff.Poster.DefaultSelected {
		t.Fatalf("poster diff = %#v", diff.Poster)
	}
	payload, err := json.Marshal(diff)
	if err != nil {
		t.Fatalf("marshal diff: %v", err)
	}
	if strings.Contains(string(payload), root) || strings.Contains(string(payload), "example.invalid") {
		t.Fatalf("diff leaked a source path or ignored URL: %s", payload)
	}
	var state models.VideoLocalMetadataState
	if err := database.DB.First(&state, "video_id = ?", video.ID).Error; err != nil {
		t.Fatalf("load observation state: %v", err)
	}
	if state.Status != LocalMetadataStateUpdateAvailable || state.ObservedManifestSHA256 != diff.ManifestSHA256 {
		t.Fatalf("observation state = %#v", state)
	}
}

func TestVideoJSONDoesNotExposeManagedArtworkPaths(t *testing.T) {
	video := models.Video{ID: 1, PosterPath: "videos/1/poster.jpg", FanartPath: "videos/1/fanart.jpg"}
	payload, err := json.Marshal(video)
	if err != nil {
		t.Fatalf("marshal video: %v", err)
	}
	if strings.Contains(string(payload), "PosterPath") || strings.Contains(string(payload), "poster.jpg") || strings.Contains(string(payload), "fanart.jpg") {
		t.Fatalf("video JSON exposed managed paths: %s", payload)
	}
}

func TestLocalMetadataApplyDefaultsFillsOnlyEmptyFields(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	dataDir := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, videoPath)
	nfo := `<movie><title>Imported title</title><originaltitle>Original title</originaltitle><plot>Imported plot</plot>` +
		`<actor><name>Actor One</name></actor><set><name>Series One</name></set></movie>`
	if err := os.WriteFile(filepath.Join(root, "movie.nfo"), []byte(nfo), 0644); err != nil {
		t.Fatalf("write NFO: %v", err)
	}
	writeTestPNG(t, filepath.Join(root, "poster.png"))
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, DisplayTitle: "Manual title"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	people := NewPersonService(dataDir)
	collections := NewCollectionService(dataDir)
	service := NewLocalMetadataService(dataDir, people, collections)

	result, err := service.ApplyDefaults(video.ID)
	if err != nil {
		t.Fatalf("apply defaults: %v", err)
	}
	if result.Status != LocalMetadataStateCurrent {
		t.Fatalf("apply result = %#v", result)
	}
	var loaded models.Video
	if err := database.DB.First(&loaded, video.ID).Error; err != nil {
		t.Fatalf("reload video: %v", err)
	}
	if loaded.DisplayTitle != "Manual title" || loaded.OriginalTitle != "Original title" || loaded.Description != "Imported plot" || loaded.PosterPath == "" {
		t.Fatalf("filled video = %#v", loaded)
	}
	if _, err := service.ResolveVideoArtwork(video.ID, "poster"); err != nil {
		t.Fatalf("resolve imported poster: %v", err)
	}
	var personCount, collectionCount, personRelationCount, collectionRelationCount int64
	database.DB.Model(&models.Person{}).Count(&personCount)
	database.DB.Model(&models.MediaCollection{}).Count(&collectionCount)
	database.DB.Model(&models.VideoPerson{}).Where("video_id = ?", video.ID).Count(&personRelationCount)
	database.DB.Model(&models.CollectionVideo{}).Where("video_id = ?", video.ID).Count(&collectionRelationCount)
	if personCount != 1 || collectionCount != 1 || personRelationCount != 1 || collectionRelationCount != 1 {
		t.Fatalf("entity counts people=%d collections=%d people_rel=%d collection_rel=%d", personCount, collectionCount, personRelationCount, collectionRelationCount)
	}
}

func TestLocalMetadataApplyRequiresOverwriteAndRejectsStaleDiff(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, videoPath)
	nfoPath := filepath.Join(root, "movie.nfo")
	if err := os.WriteFile(nfoPath, []byte(`<movie><title>Imported</title></movie>`), 0644); err != nil {
		t.Fatalf("write NFO: %v", err)
	}
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, DisplayTitle: "Manual"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	diff, err := service.GetDiff(video.ID)
	if err != nil {
		t.Fatalf("get diff: %v", err)
	}
	request := LocalMetadataApplyRequest{
		VideoID: video.ID, ManifestSHA256: diff.ManifestSHA256, CurrentSHA256: diff.CurrentSHA256, SelectedFields: []string{"title"},
	}
	if _, err := service.Apply(request); !errors.Is(err, ErrLocalMetadataOverwriteRequired) {
		t.Fatalf("missing overwrite confirmation error = %v", err)
	}
	request.OverwriteFields = []string{"title"}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Update("description", "concurrent edit").Error; err != nil {
		t.Fatalf("change current video: %v", err)
	}
	if _, err := service.Apply(request); !errors.Is(err, ErrLocalMetadataConflict) {
		t.Fatalf("stale current diff error = %v", err)
	}

	diff, err = service.GetDiff(video.ID)
	if err != nil {
		t.Fatalf("reload diff: %v", err)
	}
	request.ManifestSHA256, request.CurrentSHA256 = diff.ManifestSHA256, diff.CurrentSHA256
	if err := os.WriteFile(nfoPath, []byte(`<movie><title>Changed source</title></movie>`), 0644); err != nil {
		t.Fatalf("change NFO: %v", err)
	}
	if _, err := service.Apply(request); !errors.Is(err, ErrLocalMetadataConflict) {
		t.Fatalf("stale source diff error = %v", err)
	}
}

func TestLocalMetadataOverwritingPeopleKeepsExistingPersonEntities(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	dataDir := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, videoPath)
	if err := os.WriteFile(filepath.Join(root, "movie.nfo"), []byte(`<movie><actor><name>NFO Actor</name></actor></movie>`), 0644); err != nil {
		t.Fatalf("write NFO: %v", err)
	}
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	people := NewPersonService(dataDir)
	avatarSource := filepath.Join(t.TempDir(), "avatar.png")
	writeTestPNG(t, avatarSource)
	curated, err := people.CreatePerson("手工维护的人物", "Curated")
	if err != nil {
		t.Fatalf("create curated person: %v", err)
	}
	if curated, err = people.SetPersonAvatar(curated.ID, avatarSource); err != nil {
		t.Fatalf("set curated avatar: %v", err)
	}
	if curated.AvatarPath == "" {
		t.Fatal("fixture needs a managed avatar")
	}
	// The curated person is only attached to this one video.
	if err := database.DB.Create(&models.VideoPerson{VideoID: video.ID, PersonID: curated.ID}).Error; err != nil {
		t.Fatalf("attach curated person: %v", err)
	}

	service := NewLocalMetadataService(dataDir, people, NewCollectionService(dataDir))
	diff, err := service.GetDiff(video.ID)
	if err != nil {
		t.Fatalf("get diff: %v", err)
	}
	request := LocalMetadataApplyRequest{
		VideoID: video.ID, ManifestSHA256: diff.ManifestSHA256, CurrentSHA256: diff.CurrentSHA256,
		SelectedFields: []string{"people"}, OverwriteFields: []string{"people"},
		PeopleResolutions: []LocalMetadataResolution{{NormalizedName: "nfo actor", Mode: "create_new"}},
	}
	if _, err := service.Apply(request); err != nil {
		t.Fatalf("apply people overwrite: %v", err)
	}

	// The NFO no longer lists the curated person, so the relation goes away — but
	// importing metadata must never destroy the person record or its avatar.
	var relationCount int64
	if err := database.DB.Model(&models.VideoPerson{}).Where("video_id = ? AND person_id = ?", video.ID, curated.ID).Count(&relationCount).Error; err != nil {
		t.Fatalf("count relation: %v", err)
	}
	if relationCount != 0 {
		t.Fatalf("overwrite should drop the relation, got %d", relationCount)
	}
	var reloaded models.Person
	if err := database.DB.First(&reloaded, curated.ID).Error; err != nil {
		t.Fatalf("NFO import deleted an existing person: %v", err)
	}
	if reloaded.OriginalName != "Curated" {
		t.Fatalf("person record changed: %#v", reloaded)
	}
	if _, err := people.ResolvePersonAvatar(curated.ID); err != nil {
		t.Fatalf("NFO import deleted the managed avatar: %v", err)
	}
}

func TestLocalMetadataApplyCompensatesImportedArtworkOnTransactionFailure(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	dataDir := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, videoPath)
	if err := os.WriteFile(filepath.Join(root, "movie.nfo"), []byte(`<movie><actor><name>New Actor</name></actor></movie>`), 0644); err != nil {
		t.Fatalf("write NFO: %v", err)
	}
	writeTestPNG(t, filepath.Join(root, "poster.png"))
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	service := NewLocalMetadataService(dataDir, NewPersonService(dataDir), NewCollectionService(dataDir))
	diff, err := service.GetDiff(video.ID)
	if err != nil {
		t.Fatalf("get diff: %v", err)
	}
	_, err = service.Apply(LocalMetadataApplyRequest{
		VideoID: video.ID, ManifestSHA256: diff.ManifestSHA256, CurrentSHA256: diff.CurrentSHA256,
		SelectedFields:    []string{"poster", "people"},
		PeopleResolutions: []LocalMetadataResolution{{NormalizedName: "new actor", Mode: "existing", EntityID: 999999}},
	})
	if err == nil {
		t.Fatal("expected invalid mapping failure")
	}
	managedDirectory := filepath.Join(dataDir, "media-details", "videos", "1")
	entries, readErr := os.ReadDir(managedDirectory)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("read managed directory: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("transaction failure left managed artwork: %#v", entries)
	}
}

func TestVideoServiceAutoAppliesNewLocalMetadataButOnlyObservesLaterChanges(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, videoPath)
	nfoPath := filepath.Join(root, "movie.nfo")
	if err := os.WriteFile(nfoPath, []byte(`<movie><title>Initial local title</title></movie>`), 0644); err != nil {
		t.Fatalf("write initial NFO: %v", err)
	}
	dataDir := t.TempDir()
	metadata := NewLocalMetadataService(dataDir, NewPersonService(dataDir), NewCollectionService(dataDir))
	videoService := NewVideoService(nil)
	videoService.SetLocalMetadataObserver(metadata.ObserveVideo)

	video, err := videoService.AddVideo(videoPath)
	if err != nil {
		t.Fatalf("add video: %v", err)
	}
	if video.DisplayTitle != "Initial local title" {
		t.Fatalf("new video title = %q", video.DisplayTitle)
	}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Update("display_title", "Manual title").Error; err != nil {
		t.Fatalf("set manual title: %v", err)
	}
	if err := os.WriteFile(nfoPath, []byte(`<movie><title>Changed local title</title></movie>`), 0644); err != nil {
		t.Fatalf("change NFO: %v", err)
	}
	result := videoService.SyncAffectedDirectories([]models.ScanDirectory{{ID: 1, Path: root}}, []string{root})
	if result.Scanned != 1 {
		t.Fatalf("narrow reconciliation = %#v", result)
	}
	var loaded models.Video
	if err := database.DB.First(&loaded, video.ID).Error; err != nil {
		t.Fatalf("reload video: %v", err)
	}
	if loaded.DisplayTitle != "Manual title" {
		t.Fatalf("existing video metadata was overwritten: %q", loaded.DisplayTitle)
	}
	var state models.VideoLocalMetadataState
	if err := database.DB.First(&state, "video_id = ?", video.ID).Error; err != nil {
		t.Fatalf("load local metadata state: %v", err)
	}
	if state.Status != LocalMetadataStateUpdateAvailable {
		t.Fatalf("changed local metadata state = %#v", state)
	}
}

func TestLocalMetadataBatchApplyReportsPartialSuccess(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	requests := make([]LocalMetadataApplyRequest, 0, 2)
	for index, title := range []string{"First", "Second"} {
		videoPath := filepath.Join(root, fmt.Sprintf("movie-%d.mp4", index))
		mustCreateFile(t, videoPath)
		if err := os.WriteFile(strings.TrimSuffix(videoPath, ".mp4")+".nfo", []byte(`<movie><title>`+title+`</title></movie>`), 0644); err != nil {
			t.Fatalf("write NFO: %v", err)
		}
		video := models.Video{Name: filepath.Base(videoPath), Path: videoPath, Directory: root}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatalf("create video: %v", err)
		}
		diff, err := service.GetDiff(video.ID)
		if err != nil {
			t.Fatalf("get diff: %v", err)
		}
		requests = append(requests, LocalMetadataApplyRequest{
			VideoID: video.ID, ManifestSHA256: diff.ManifestSHA256, CurrentSHA256: diff.CurrentSHA256, SelectedFields: []string{"title"},
		})
	}
	requests[1].ManifestSHA256 = strings.Repeat("0", 64)

	result := service.ApplyBatch(LocalMetadataBatchApplyRequest{Requests: requests})
	if result.Requested != 2 || result.Succeeded != 1 || result.Failed != 1 || len(result.Results) != 1 || len(result.Failures) != 1 || result.Failures[0].ErrorCode != "metadata_conflict" {
		t.Fatalf("batch result = %#v", result)
	}
}

func TestLocalMetadataBatchRejectsMoreThanFiveHundredVideosWithoutWork(t *testing.T) {
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	videoIDs := make([]uint, localMetadataBatchMaxItems+1)
	requests := make([]LocalMetadataApplyRequest, localMetadataBatchMaxItems+1)
	for index := range videoIDs {
		videoIDs[index] = uint(index + 1)
		requests[index] = LocalMetadataApplyRequest{VideoID: uint(index + 1)}
	}
	preview := service.PreviewBatch(videoIDs)
	if preview.Requested != len(videoIDs) || len(preview.Diffs) != 0 || len(preview.Failures) != 1 || preview.Failures[0].ErrorCode != "batch_limit_exceeded" {
		t.Fatalf("oversized preview = %#v", preview)
	}
	result := service.ApplyBatch(LocalMetadataBatchApplyRequest{Requests: requests})
	if result.Requested != len(requests) || result.Failed != len(requests) || result.Succeeded != 0 || len(result.Failures) != 1 || result.Failures[0].ErrorCode != "batch_limit_exceeded" {
		t.Fatalf("oversized apply = %#v", result)
	}
}

func TestLocalMetadataBackfillCancelsInFlightWork(t *testing.T) {
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	service.backfillLoad = func(context.Context) ([]models.Video, error) {
		return []models.Video{{ID: 1, Name: "one.mp4"}, {ID: 2, Name: "two.mp4"}}, nil
	}
	started := make(chan struct{})
	service.backfillProcess = func(ctx context.Context, _ uint) (bool, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-ctx.Done()
		return false, ctx.Err()
	}
	status, err := service.StartBackfill(context.Background())
	if err != nil || !status.Running || status.Total != 2 {
		t.Fatalf("start status=%#v err=%v", status, err)
	}
	<-started
	if err := service.CancelBackfill(); err != nil {
		t.Fatalf("cancel backfill: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status = service.BackfillStatus()
		if status.Completed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !status.Completed || !status.Cancelled || status.Running || status.Processed != 0 {
		t.Fatalf("cancelled status = %#v", status)
	}
}

func TestObserveVideoSkipsContentHashingWhenSourceStatsAreUnchanged(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, videoPath)
	nfoPath := filepath.Join(root, "movie.nfo")
	if err := os.WriteFile(nfoPath, []byte(`<movie><title>AAAA</title></movie>`), 0644); err != nil {
		t.Fatalf("write NFO: %v", err)
	}
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, DisplayTitle: "Manual"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))

	if err := service.ObserveVideo(video.ID, false); err != nil {
		t.Fatalf("first observation: %v", err)
	}
	loadState := func() models.VideoLocalMetadataState {
		t.Helper()
		var state models.VideoLocalMetadataState
		if err := database.DB.First(&state, "video_id = ?", video.ID).Error; err != nil {
			t.Fatalf("load state: %v", err)
		}
		return state
	}
	first := loadState()
	if first.ObservedSourceStat == "" {
		t.Fatal("observation should record a cheap source fingerprint")
	}

	// Same size and mtime: a library scan must not re-read and re-hash every source file
	// just to conclude nothing changed.
	info, err := os.Stat(nfoPath)
	if err != nil {
		t.Fatalf("stat NFO: %v", err)
	}
	if err := os.WriteFile(nfoPath, []byte(`<movie><title>BBBB</title></movie>`), 0644); err != nil {
		t.Fatalf("rewrite NFO: %v", err)
	}
	if err := os.Chtimes(nfoPath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore mtime: %v", err)
	}
	if err := service.ObserveVideo(video.ID, false); err != nil {
		t.Fatalf("second observation: %v", err)
	}
	if loadState().ObservedManifestSHA256 != first.ObservedManifestSHA256 {
		t.Fatal("stat-identical sources should short-circuit before content hashing")
	}

	// A normal edit moves mtime, and that must still be detected.
	later := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(nfoPath, later, later); err != nil {
		t.Fatalf("advance mtime: %v", err)
	}
	if err := service.ObserveVideo(video.ID, false); err != nil {
		t.Fatalf("third observation: %v", err)
	}
	if loadState().ObservedManifestSHA256 == first.ObservedManifestSHA256 {
		t.Fatal("a real source change must update the observed manifest")
	}
}

func TestLocalMetadataUnusableExtraImageDoesNotBlockTheWholeImport(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, videoPath)
	if err := os.WriteFile(filepath.Join(root, "movie.nfo"), []byte(`<movie><title>可用标题</title></movie>`), 0644); err != nil {
		t.Fatalf("write nfo: %v", err)
	}
	writeTestPNG(t, filepath.Join(root, "movie-poster.png"))
	// A lower-priority candidate that is far too large to ever be used.
	oversized := make([]byte, managedImageMaxBytes+1)
	if err := os.WriteFile(filepath.Join(root, "folder.jpg"), oversized, 0644); err != nil {
		t.Fatalf("write oversized candidate: %v", err)
	}
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}

	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	diff, err := service.GetDiff(video.ID)
	if err != nil {
		t.Fatalf("an unusable extra image must not break metadata discovery: %v", err)
	}
	if diff.Status == LocalMetadataStateError {
		t.Fatalf("diff status = %#v", diff.Status)
	}
	if diff.Title.SourceValue != "可用标题" {
		t.Fatalf("NFO fields should still be importable: %#v", diff.Title)
	}
}

func TestLocalMetadataBackfillSurvivesSourcesRemovedMidApply(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	videoPath := filepath.Join(root, "vanishing.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	nfoPath := filepath.Join(root, "vanishing.nfo")
	if err := os.WriteFile(nfoPath, []byte(`<movie><title>会消失的标题</title></movie>`), 0644); err != nil {
		t.Fatalf("write nfo: %v", err)
	}
	video := models.Video{Name: "vanishing.mp4", Path: videoPath, Directory: root, Size: 5}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))

	diff, err := service.GetDiff(video.ID)
	if err != nil {
		t.Fatalf("initial diff: %v", err)
	}
	if !localMetadataDiffHasDefaults(diff) {
		t.Fatalf("fixture should offer defaults, got %#v", diff)
	}

	// An external tool removes the sources between the diff and the apply.
	if err := os.Remove(nfoPath); err != nil {
		t.Fatalf("remove nfo: %v", err)
	}

	// Callers rely on this nil-without-error contract; returning a result here would
	// make the backfill worker dereference nil and take the whole app down.
	result, err := service.ApplyDefaults(video.ID)
	if err != nil {
		t.Fatalf("apply defaults after sources vanished: %v", err)
	}
	if result != nil {
		t.Fatalf("apply defaults must report nothing to apply, got %#v", result)
	}

	applied, err := service.processLocalMetadataBackfillVideo(context.Background(), video.ID)
	if err != nil || applied {
		t.Fatalf("backfill step = %v err=%v", applied, err)
	}
}

func TestLocalMetadataStopBackfillWaitsForWorkerToExit(t *testing.T) {
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	service.backfillLoad = func(context.Context) ([]models.Video, error) {
		return []models.Video{{ID: 1, Name: "one.mp4"}, {ID: 2, Name: "two.mp4"}}, nil
	}
	started := make(chan struct{})
	var exited atomic.Bool
	service.backfillProcess = func(ctx context.Context, _ uint) (bool, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-ctx.Done()
		// Shutdown must not race with the tail of in-flight work.
		time.Sleep(20 * time.Millisecond)
		exited.Store(true)
		return false, ctx.Err()
	}
	if _, err := service.StartBackfill(context.Background()); err != nil {
		t.Fatalf("start backfill: %v", err)
	}
	<-started

	service.StopBackfill()

	if !exited.Load() {
		t.Fatal("StopBackfill returned while the backfill worker was still running")
	}
	status := service.BackfillStatus()
	if status.Running || !status.Cancelled || !status.Completed {
		t.Fatalf("status after stop = %#v", status)
	}
	// Stopping an idle service must stay a no-op instead of blocking or panicking.
	service.StopBackfill()
}

func writeTestPNG(t *testing.T, path string) {
	t.Helper()
	content, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode PNG fixture: %v", err)
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("write PNG fixture: %v", err)
	}
}
